package main

// Local ffmpeg tools: the six video/audio transform tools that run on the
// locally installed ffmpeg CLI (see local_tools.go for binary detection and
// the shared runners). They follow the media-tool conventions of
// tools_registry.go — attachment-driven sources (the turn's media slots carry
// attached, @-mentioned, or history-fallback clips), outputs staged as temp
// files wrapped in ToolVideoResult/ToolAudioResult/ToolImageResult so the
// existing artifact, carry-forward, and telemetry pipelines serve them
// unchanged. Everything ffmpeg-specific lives here: arg builders (pure, so
// tests can pin them), the concat-list writer, ffprobe parsing, and the tool
// definitions with their planner-facing descriptions.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ffmpegModelName is the Model stamped on every ffmpeg tool result and
// telemetry activity. ffmpeg is a binary, not a model, but the media-result
// fields and the usage ledger key on Model — a stable label keeps ffmpeg
// consumption visible as its own row.
const ffmpegModelName = "ffmpeg"

// ffmpegToolsConfigured reports whether the ffmpeg-backed media tools should
// be offered: the local ffmpeg CLI must be detected (PATH or the Settings
// override). Like the fal tool gates, this avoids offering a tool that is
// guaranteed to fail at call time.
func ffmpegToolsConfigured(config AppConfig) bool {
	_, ok := resolveLocalFFmpegBinary(config)
	return ok
}

// localMediaEditUnavailableNote is the code-authored note delivered to the
// final model — as its own trailing user message, never in the system prompt —
// when triage flagged the turn as a local media edit and no ffmpeg CLI is
// configured. It tells the model what actually happened (the capability is
// absent, not broken) and the exact remedy to relay, so a from-knowledge
// answer can't masquerade as a failed edit or hand the user a raw CLI recipe.
const localMediaEditUnavailableNote = "Atelier note: the user's latest request asks for a local video edit — capturing a frame, splitting or trimming a clip, joining clips, or extracting/replacing audio — but no ffmpeg CLI was detected on this machine, so Atelier has no tool that can perform it. Do not claim the edit was done and do not attempt it through other tools. Tell the user plainly that local video editing needs a one-time install: install ffmpeg with `brew install ffmpeg` (or set an explicit binary in Settings → Video Tools); Atelier detects it automatically on the next message."

// mediaEditFallbackNotice returns the deterministic one-line blockquote for
// the chat reply when the final model's answer did not already mention ffmpeg
// — the remedy is always visible to the user, without duplicating what the
// model said. Empty when the flag is off or the answer covered it.
func mediaEditFallbackNotice(unavailable bool, assistantContent string) string {
	if !unavailable {
		return ""
	}
	if strings.Contains(strings.ToLower(assistantContent), "ffmpeg") {
		return ""
	}
	return "> ⚠️ Local video editing isn't available yet — install ffmpeg (`brew install ffmpeg`, or set the binary in Settings → Video Tools) and Atelier picks it up automatically."
}

// ffmpegToolDefinitions assembles the ffmpeg-backed tool catalog. probe_media
// additionally needs ffprobe (detected separately — it powers the JSON report);
// the transform tools degrade without it (they re-encode rather than probe).
func ffmpegToolDefinitions(config AppConfig) []HarnessToolDefinition {
	definitions := []HarnessToolDefinition{
		screenshotVideoToolDefinition(),
		splitVideoToolDefinition(),
		joinVideosToolDefinition(),
		extractAudioToolDefinition(),
		replaceAudioToolDefinition(),
	}
	if _, ok := resolveLocalFFprobeBinary(config); ok {
		definitions = append(definitions, probeMediaToolDefinition())
	}
	return definitions
}

// ---------------------------------------------------------------------------
// Timestamps
// ---------------------------------------------------------------------------

// parseMediaTimestamp reads a planner-supplied media timestamp: bare seconds
// ("42", "12.5") or a clock value ("1:30", "00:01:30.5" — via
// parseClockTimestamp, which handles the 2–3 segment forms). Negative and
// non-numeric values are rejected. The original string is forwarded to ffmpeg
// (it accepts both forms); this validates and yields seconds for arithmetic.
func parseMediaTimestamp(token string) (float64, bool) {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(token, "-") {
		return 0, false
	}
	if !strings.Contains(token, ":") {
		seconds, err := strconv.ParseFloat(token, 64)
		if err != nil || seconds < 0 {
			return 0, false
		}
		return seconds, true
	}
	return parseClockTimestamp(token)
}

// ---------------------------------------------------------------------------
// Staging: attached media data URLs → real files ffmpeg can read
// ---------------------------------------------------------------------------

// stagedMediaExtension maps an attached media type onto the file extension the
// staged input file gets, so ffmpeg's format detection starts from a hint it
// trusts (it still sniffs the content).
func stagedMediaExtension(mediaType string) string {
	switch {
	case strings.HasPrefix(mediaType, "video/"):
		return videoExtensionForMediaType(mediaType)
	case strings.HasPrefix(mediaType, "audio/"):
		return audioExtensionForMediaType(mediaType)
	case strings.HasPrefix(mediaType, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mediaType, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mediaType, "image/heic"), strings.HasPrefix(mediaType, "image/heif"):
		return ".heic"
	case strings.HasPrefix(mediaType, "image/avif"):
		return ".avif"
	case strings.HasPrefix(mediaType, "image/tiff"):
		return ".tiff"
	case strings.HasPrefix(mediaType, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mediaType, "image/bmp"):
		return ".bmp"
	case strings.HasPrefix(mediaType, "image/"):
		return ".png"
	default:
		return ".bin"
	}
}

// stageMediaDataURL decodes an attached-media data URL into a real file inside
// dir (named base+ext by media type) — the ffmpeg sibling of the whisper
// runner's temp-file decode. ffmpeg and ffprobe read real files, not data
// URLs.
func stageMediaDataURL(dir, base, dataURL string) (string, error) {
	data, mediaType, err := decodeMediaDataURL(dataURL)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, base+stagedMediaExtension(mediaType))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// promoteStagedOutput moves a finished output file out of the per-call staging
// directory into its own temp file, so the executor can RemoveAll the staging
// dir while the harness later moves the output into the conversation's
// artifacts directory (the same lifecycle writeTempVideo establishes).
func promoteStagedOutput(stagedPath, prefix, ext string) (string, error) {
	final, err := os.CreateTemp("", prefix+ext)
	if err != nil {
		return "", err
	}
	name := final.Name()
	final.Close()
	os.Remove(name)
	if err := moveFile(stagedPath, name); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// ---------------------------------------------------------------------------
// Arg builders (pure — pinned by tests)
// ---------------------------------------------------------------------------

// ffmpegScreenshotArgs grabs one frame at a timestamp as JPEG. -ss before -i
// is a fast input seek; re-encoding exactly one frame keeps it accurate.
func ffmpegScreenshotArgs(input, at, output string) []string {
	return []string{"-ss", at, "-i", input, "-frames:v", "1", "-q:v", "2", output}
}

// ffmpegSplitArgs cuts a segment. fast copies streams (instant, but cut points
// land on keyframes); accurate re-encodes for an exact cut. startRaw keeps the
// caller's format (seconds or clock) while durationSeconds is precomputed from
// parsed values; durationSeconds <= 0 means through the end of the input.
func ffmpegSplitArgs(input, startRaw string, durationSeconds float64, fast bool, output string) []string {
	args := []string{}
	if startRaw != "" {
		args = append(args, "-ss", startRaw)
	}
	args = append(args, "-i", input)
	if durationSeconds > 0 {
		args = append(args, "-t", strconv.FormatFloat(durationSeconds, 'f', -1, 64))
	}
	if fast {
		args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "18",
			"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart")
	}
	return append(args, output)
}

// ffmpegJoinArgs concatenates via the concat demuxer (listFile holds the
// clips in sequence order). copy keeps every stream byte-identical (requires
// matching inputs); re-encode normalizes anything into H.264/AAC MP4.
func ffmpegJoinArgs(listFile string, streamCopy bool, output string) []string {
	args := []string{"-f", "concat", "-safe", "0", "-i", listFile}
	if streamCopy {
		args = append(args, "-c", "copy")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "18",
			"-c:a", "aac", "-b:a", "192k")
	}
	return append(args, "-movflags", "+faststart", output)
}

// ffmpegExtractAudioArgs pulls the audio track: copy streams the codec as-is
// into a matching container; convert re-encodes to MP3 192kbps — the safe
// output for any codec the copy containers cannot hold.
func ffmpegExtractAudioArgs(input, output string, streamCopy bool) []string {
	args := []string{"-i", input, "-vn"}
	if streamCopy {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "libmp3lame", "-b:a", "192k")
	}
	return append(args, output)
}

// ffmpegReplaceAudioArgs lays audio under a video: video streams are copied
// when the output container holds them, otherwise re-encoded; audio always
// becomes AAC (uniform, plays everywhere); -shortest ends the output when the
// shorter input ends.
func ffmpegReplaceAudioArgs(video, audio string, copyVideo bool, output string) []string {
	args := []string{"-i", video, "-i", audio, "-map", "0:v:0", "-map", "1:a:0"}
	if copyVideo {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "18")
	}
	return append(args, "-c:a", "aac", "-b:a", "192k", "-shortest", "-movflags", "+faststart", output)
}

// concatListFileContents renders the concat demuxer's playlist: one
// "file '<path>'" line per clip, in sequence order. Single quotes in a path
// are escaped with the concat demuxer's quote-escaping idiom.
func concatListFileContents(paths []string) string {
	var builder strings.Builder
	for _, path := range paths {
		escaped := strings.ReplaceAll(path, "'", `'\''`)
		fmt.Fprintf(&builder, "file '%s'\n", escaped)
	}
	return builder.String()
}

// ---------------------------------------------------------------------------
// ffprobe: metadata for evidence and codec-aware decisions
// ---------------------------------------------------------------------------

// ToolProbeResult is probe_media's evidence payload: the compact facts of an
// attached clip. No media slices — everything rides the standard role:"tool"
// path verbatim (it is already small; the ffprobe JSON is parsed and
// projected, never passed through).
type ToolProbeResult struct {
	Kind       string  `json:"kind"`
	Duration   float64 `json:"durationSeconds"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	VideoCodec string  `json:"videoCodec,omitempty"`
	AudioCodec string  `json:"audioCodec,omitempty"`
	Format     string  `json:"format,omitempty"`
	BitRate    string  `json:"bitrate,omitempty"`
	SizeBytes  int64   `json:"sizeBytes,omitempty"`
}

// ffprobeReport is the subset of ffprobe -show_format -show_streams JSON the
// tools consume.
type ffprobeReport struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
		BitRate    string `json:"bit_rate"`
		Size       string `json:"size"`
	} `json:"format"`
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
	} `json:"streams"`
}

// probeStagedMedia runs ffprobe over a staged media file and parses it into
// the compact ToolProbeResult. kind labels the clip for evidence ("video" or
// "audio").
func probeStagedMedia(ctx context.Context, config AppConfig, path, kind string) (ToolProbeResult, error) {
	output, err := runLocalFFprobe(ctx, config, []string{
		"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path,
	})
	if err != nil {
		return ToolProbeResult{}, err
	}
	var report ffprobeReport
	if err := json.Unmarshal(output, &report); err != nil {
		return ToolProbeResult{}, fmt.Errorf("ffprobe output could not be parsed: %v: %s", err, truncateLocalToolOutput(output))
	}
	result := ToolProbeResult{Kind: kind, Format: report.Format.FormatName, BitRate: report.Format.BitRate}
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(report.Format.Duration), 64); err == nil {
		result.Duration = seconds
	}
	if size, err := strconv.ParseInt(strings.TrimSpace(report.Format.Size), 10, 64); err == nil {
		result.SizeBytes = size
	}
	for _, stream := range report.Streams {
		switch stream.CodecType {
		case "video":
			if result.VideoCodec == "" {
				result.VideoCodec = stream.CodecName
				result.Width = stream.Width
				result.Height = stream.Height
				result.FPS = parseFrameRate(stream.AvgFrameRate)
			}
		case "audio":
			if result.AudioCodec == "" {
				result.AudioCodec = stream.CodecName
			}
		}
	}
	return result, nil
}

// parseFrameRate reads ffprobe's rational frame rate ("30000/1001", "25/1")
// as a float. Unparseable or degenerate values yield 0.
func parseFrameRate(rate string) float64 {
	parts := strings.Split(strings.TrimSpace(rate), "/")
	numerator, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0
	}
	if len(parts) == 1 {
		return numerator
	}
	denominator, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil || denominator == 0 {
		return 0
	}
	return numerator / denominator
}

// concatCompatible reports whether two probed clips can be stream-copied into
// one concat output: same video codec, dimensions, frame rate, and audio
// codec. A missing video codec (audio-only clip) is never concat-copy-safe.
func concatCompatible(a, b ToolProbeResult) bool {
	if a.VideoCodec == "" || b.VideoCodec == "" {
		return false
	}
	return a.VideoCodec == b.VideoCodec &&
		a.Width == b.Width && a.Height == b.Height &&
		absFloat(a.FPS-b.FPS) < 0.01 &&
		a.AudioCodec == b.AudioCodec
}

// mp4Family reports whether a probed clip's container can hold copied H.264
// video (mp4/mov/m4v families — ffprobe reports "mov,mp4,m4a,3gp,3g2,mj2").
func mp4Family(format string) bool {
	return strings.Contains(format, "mp4") || strings.Contains(format, "mov") || strings.Contains(format, "m4v")
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// screenshotVideoToolDefinition exposes screenshot_video: one frame of the
// attached video at a timestamp, as a JPEG attached to the reply.
func screenshotVideoToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "screenshot_video",
		Title:       "Screenshot video",
		Description: "Use this when the user asks to grab a frame, take a screenshot, still, or thumbnail of an attached video at a specific moment. Requires an attached video clip (one attached or @-mentioned this turn, or the conversation's newest video). at is the timestamp of the frame — seconds (\"42\", \"12.5\") or clock (\"00:01:30\") — and is required; \"0\" is the first frame. The captured JPEG frame is attached to the assistant reply and becomes the conversation's newest image.",
		Example:     `{"name":"screenshot_video","at":"4.5"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: screenshotVideoParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			at := strings.TrimSpace(call.At)
			if at == "" {
				return []string{prefix + ".at is required for screenshot_video (the timestamp of the frame)"}
			}
			if _, ok := parseMediaTimestamp(at); !ok {
				return []string{prefix + ".at must be a timestamp in seconds (\"42\") or clock (\"00:01:30\") for screenshot_video"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedVideo(tools.AttachedVideos)
			if source == "" {
				return nil, "screenshot requires an attached video clip", errors.New("screenshot_video requires an attached video clip — ask the user to attach one first")
			}
			at := strings.TrimSpace(call.At)
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "screenshot failed", err
			}
			defer os.RemoveAll(staging)
			input, err := stageMediaDataURL(staging, "input", source)
			if err != nil {
				return nil, "screenshot failed", err
			}
			framePath := filepath.Join(staging, "screenshot.jpg")
			if err := runLocalFFmpeg(ctx, tools.Config, ffmpegScreenshotArgs(input, at, framePath)); err != nil {
				return nil, "screenshot failed", err
			}
			data, err := os.ReadFile(framePath)
			if err != nil || len(data) == 0 {
				return nil, "screenshot failed", fmt.Errorf("ffmpeg produced no frame at %s", at)
			}
			dataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
			output := ToolImageResult{
				Model:  ffmpegModelName,
				Prompt: "frame at " + at,
				Count:  1,
				Images: []string{dataURL},
			}
			return output, fmt.Sprintf("captured the frame at %s from the attached video with ffmpeg", at), nil
		},
		Activity: ffmpegActivity("screenshot"),
	}
}

// splitVideoToolDefinition exposes split_video: cut a segment out of the
// attached video (trim away the rest).
func splitVideoToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "split_video",
		Title:       "Split video",
		Description: "Use this when the user asks to cut, trim, split, or crop out a portion of an attached video — keep a segment, drop the beginning or end, or split off the tail. Requires an attached video clip (one attached or @-mentioned this turn, or the conversation's newest video). start and end are timestamps in seconds (\"10\", \"12.5\") or clock (\"00:01:30\"); omit start for the beginning of the clip, omit end for through the end. mode \"accurate\" (the default) re-encodes so the cut lands on the exact frame; \"fast\" copies the streams without re-encoding — instant and lossless, but cuts land on the video's keyframes so the clip may begin slightly before the requested start (the right choice for long videos and for clips from the same generator). The resulting clip is attached to the assistant reply and becomes the conversation's newest video.",
		Example:     `{"name":"split_video","start":"10","end":"25"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: splitVideoParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			switch strings.TrimSpace(call.Mode) {
			case "", "fast", "accurate":
			default:
				return []string{prefix + `.mode must be "fast" or "accurate" for split_video`}
			}
			start, hasStart := parseMediaTimestamp(call.Start)
			end, hasEnd := parseMediaTimestamp(call.End)
			if strings.TrimSpace(call.Start) != "" && !hasStart {
				return []string{prefix + `.start must be a timestamp in seconds ("10") or clock ("00:01:30") for split_video`}
			}
			if strings.TrimSpace(call.End) != "" && !hasEnd {
				return []string{prefix + `.end must be a timestamp in seconds ("25") or clock ("00:00:25") for split_video`}
			}
			if hasStart && hasEnd && end <= start {
				return []string{prefix + ".end must be after start for split_video"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedVideo(tools.AttachedVideos)
			if source == "" {
				return nil, "split requires an attached video clip", errors.New("split_video requires an attached video clip — ask the user to attach one first")
			}
			startRaw := strings.TrimSpace(call.Start)
			endRaw := strings.TrimSpace(call.End)
			fast := strings.TrimSpace(call.Mode) == "fast"
			duration := 0.0
			if endRaw != "" {
				end, _ := parseMediaTimestamp(endRaw)
				start, _ := parseMediaTimestamp(startRaw)
				duration = end - start
			}
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "split failed", err
			}
			defer os.RemoveAll(staging)
			input, err := stageMediaDataURL(staging, "input", source)
			if err != nil {
				return nil, "split failed", err
			}
			staged := filepath.Join(staging, "split.mp4")
			if err := runLocalFFmpeg(ctx, tools.Config, ffmpegSplitArgs(input, startRaw, duration, fast, staged)); err != nil {
				return nil, "split failed", err
			}
			tempPath, err := promoteStagedOutput(staged, "atelier-video-*", ".mp4")
			if err != nil {
				return nil, "split failed", err
			}
			output := ToolVideoResult{
				Model:  ffmpegModelName,
				Prompt: splitPrompt(startRaw, endRaw),
				Count:  1,
				Videos: []ToolVideoFile{{TempPath: tempPath, MimeType: "video/mp4"}},
			}
			if fast {
				output.Notices = append(output.Notices, "fast mode cuts on the video's keyframes, so the clip may begin slightly before the requested start; pass mode \"accurate\" for an exact cut.")
			}
			return output, fmt.Sprintf("split the attached video%s with ffmpeg (%s)", splitRangePhrase(startRaw, endRaw), splitModePhrase(fast)), nil
		},
		Activity: ffmpegActivity("split"),
	}
}

// joinVideosToolDefinition exposes join_videos: concatenate attached clips
// into one, honoring attachment order as the sequence.
func joinVideosToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "join_videos",
		Title:       "Join videos",
		Description: "Use this when the user asks to combine, concatenate, merge, stitch, or join multiple video clips into one. Requires at least two videos attached or @-mentioned this turn — the clips are joined in EXACTLY the order they were attached or mentioned; that order is the sequence, so tell the user to reorder the attachments if they want a different one (older conversation clips are not picked up automatically). mode \"auto\" (the default) copies the streams untouched when every clip matches in codec, resolution, frame rate, and audio (fast, lossless — clips from the same generator always match) and re-encodes otherwise; \"copy\" forces stream copy; \"reencode\" forces re-encoding. The joined video is attached to the assistant reply and becomes the conversation's newest video.",
		Example:     `{"name":"join_videos"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: joinVideosParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			switch strings.TrimSpace(call.Mode) {
			case "", "auto", "copy", "reencode":
				return nil
			default:
				return []string{prefix + `.mode must be "auto", "copy", or "reencode" for join_videos`}
			}
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			sources := nonEmptyVideos(tools.AttachedVideos)
			if len(sources) < 2 {
				return nil, "join needs at least two clips", errors.New("join_videos needs at least two video clips — ask the user to attach or @-mention the clips to join, in sequence order")
			}
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "join failed", err
			}
			defer os.RemoveAll(staging)
			inputs := make([]string, 0, len(sources))
			for i, source := range sources {
				input, err := stageMediaDataURL(staging, fmt.Sprintf("input-%03d", i+1), source)
				if err != nil {
					return nil, "join failed", err
				}
				inputs = append(inputs, input)
			}
			listFile := filepath.Join(staging, "concat.txt")
			if err := os.WriteFile(listFile, []byte(concatListFileContents(inputs)), 0o644); err != nil {
				return nil, "join failed", err
			}
			streamCopy, notice := joinCopyMode(ctx, tools.Config, call.Mode, inputs)
			staged := filepath.Join(staging, "joined.mp4")
			if err := runLocalFFmpeg(ctx, tools.Config, ffmpegJoinArgs(listFile, streamCopy, staged)); err != nil {
				return nil, "join failed", err
			}
			tempPath, err := promoteStagedOutput(staged, "atelier-video-*", ".mp4")
			if err != nil {
				return nil, "join failed", err
			}
			output := ToolVideoResult{
				Model:  ffmpegModelName,
				Prompt: fmt.Sprintf("joined %d clips in attachment order", len(sources)),
				Count:  1,
				Videos: []ToolVideoFile{{TempPath: tempPath, MimeType: "video/mp4"}},
			}
			if notice != "" {
				output.Notices = append(output.Notices, notice)
			}
			return output, fmt.Sprintf("joined %d clips in attachment order with ffmpeg (%s)", len(sources), joinModePhrase(streamCopy)), nil
		},
		Activity: ffmpegActivity("join"),
	}
}

// joinCopyMode decides whether the join can stream-copy: an explicit mode
// wins; "auto" probes every input and copies only when all clips are
// concat-compatible. The notice explains what auto decided (or why it could
// not), because a silent re-encode on five clips is a long wait.
func joinCopyMode(ctx context.Context, config AppConfig, mode string, inputs []string) (streamCopy bool, notice string) {
	switch strings.TrimSpace(mode) {
	case "copy":
		return true, ""
	case "reencode":
		return false, ""
	}
	probes := make([]ToolProbeResult, 0, len(inputs))
	for _, input := range inputs {
		probe, err := probeStagedMedia(ctx, config, input, "video")
		if err != nil {
			return false, "the clips' formats could not be compared (ffprobe failed), so the join was re-encoded to be safe; pass mode \"copy\" to force a stream copy."
		}
		probes = append(probes, probe)
	}
	for i := 1; i < len(probes); i++ {
		if !concatCompatible(probes[0], probes[i]) {
			return false, "the clips differ in codec, resolution, frame rate, or audio, so the join was re-encoded into one uniform format."
		}
	}
	return true, "the clips matched in codec, resolution, frame rate, and audio, so they were joined by stream copy (lossless, no re-encoding)."
}

// extractAudioToolDefinition exposes extract_audio: pull the audio track out
// of the attached video as a clip attached to the reply.
func extractAudioToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "extract_audio",
		Title:       "Extract audio",
		Description: "Use this when the user asks to extract, pull out, or export the audio track of an attached video (get the sound from a clip, make a standalone audio file of it). Requires an attached video clip. The track is copied without re-encoding when its codec allows (AAC → .m4a, MP3 → .mp3) and otherwise converted to MP3 192kbps with a notice. The audio clip is attached to the assistant reply and becomes the conversation's newest audio — a transcribe_audio call in the same turn can transcribe it directly.",
		Example:     `{"name":"extract_audio"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: extractAudioParamSchema(),
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedVideo(tools.AttachedVideos)
			if source == "" {
				return nil, "audio extraction requires an attached video clip", errors.New("extract_audio requires an attached video clip — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "audio extraction failed", err
			}
			defer os.RemoveAll(staging)
			input, err := stageMediaDataURL(staging, "input", source)
			if err != nil {
				return nil, "audio extraction failed", err
			}
			ext, mimeType, streamCopy, convertNotice := extractAudioTarget(ctx, tools.Config, input)
			staged := filepath.Join(staging, "audio"+ext)
			if err := runLocalFFmpeg(ctx, tools.Config, ffmpegExtractAudioArgs(input, staged, streamCopy)); err != nil {
				return nil, "audio extraction failed", err
			}
			tempPath, err := promoteStagedOutput(staged, "atelier-audio-*", ext)
			if err != nil {
				return nil, "audio extraction failed", err
			}
			output := ToolAudioResult{
				Model:  ffmpegModelName,
				Prompt: "audio track extracted from the attached video",
				Count:  1,
				Audios: []ToolAudioFile{{TempPath: tempPath, MimeType: mimeType}},
			}
			if convertNotice != "" {
				output.Notices = append(output.Notices, convertNotice)
			}
			return output, fmt.Sprintf("extracted the audio track from the attached video with ffmpeg (%s)", extractModePhrase(streamCopy, ext)), nil
		},
		Activity: ffmpegActivity("extract-audio"),
	}
}

// extractAudioTarget picks the extraction output: copy AAC into .m4a or MP3
// into .mp3 when the probed codec says the container holds it untouched;
// anything else (or no probe) converts to MP3 with a notice explaining why.
func extractAudioTarget(ctx context.Context, config AppConfig, input string) (ext, mimeType string, streamCopy bool, notice string) {
	probe, err := probeStagedMedia(ctx, config, input, "video")
	if err != nil {
		return ".mp3", "audio/mpeg", false, "the audio codec could not be probed, so the track was converted to MP3; install ffprobe (it ships with ffmpeg) to copy the original codec instead."
	}
	switch probe.AudioCodec {
	case "aac":
		return ".m4a", "audio/mp4", true, ""
	case "mp3":
		return ".mp3", "audio/mpeg", true, ""
	case "":
		return ".mp3", "audio/mpeg", false, "the video appears to have no audio stream; the extracted file may be silent."
	default:
		return ".mp3", "audio/mpeg", false, fmt.Sprintf("the audio codec (%s) has no copy-safe standalone container here, so the track was converted to MP3 192kbps.", probe.AudioCodec)
	}
}

// replaceAudioToolDefinition exposes replace_audio: lay a new audio track
// under the attached video (narration, music, sound over a clip).
func replaceAudioToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "replace_audio",
		Title:       "Replace audio",
		Description: "Use this when the user asks to put different audio under a video — add narration, music, or a sound effect (often one generate_speech or generate_sound just produced) to a silent clip, or swap a video's soundtrack. Requires an attached video AND an attached audio clip (attached, @-mentioned, or generated earlier this turn). The video stream is copied when the container allows it; the audio is encoded to AAC; the result ends when the shorter of the two inputs ends. The new video is attached to the assistant reply and becomes the conversation's newest video.",
		Example:     `{"name":"replace_audio"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: replaceAudioParamSchema(),
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			video := firstAttachedVideo(tools.AttachedVideos)
			if video == "" {
				return nil, "audio replacement requires an attached video clip", errors.New("replace_audio requires an attached video clip — ask the user to attach one first")
			}
			audio := firstAttachedAudio(tools.AttachedAudios)
			if audio == "" {
				return nil, "audio replacement requires an attached audio clip", errors.New("replace_audio requires an attached audio clip — ask the user to attach the new audio (or generate it first in this turn)")
			}
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "audio replacement failed", err
			}
			defer os.RemoveAll(staging)
			videoPath, err := stageMediaDataURL(staging, "video", video)
			if err != nil {
				return nil, "audio replacement failed", err
			}
			audioPath, err := stageMediaDataURL(staging, "audio", audio)
			if err != nil {
				return nil, "audio replacement failed", err
			}
			copyVideo, notice := replaceAudioCopyVideo(ctx, tools.Config, videoPath)
			staged := filepath.Join(staging, "replaced.mp4")
			if err := runLocalFFmpeg(ctx, tools.Config, ffmpegReplaceAudioArgs(videoPath, audioPath, copyVideo, staged)); err != nil {
				return nil, "audio replacement failed", err
			}
			tempPath, err := promoteStagedOutput(staged, "atelier-video-*", ".mp4")
			if err != nil {
				return nil, "audio replacement failed", err
			}
			output := ToolVideoResult{
				Model:  ffmpegModelName,
				Prompt: "attached audio laid under the attached video",
				Count:  1,
				Videos: []ToolVideoFile{{TempPath: tempPath, MimeType: "video/mp4"}},
			}
			if notice != "" {
				output.Notices = append(output.Notices, notice)
			}
			return output, "replaced the attached video's audio with the attached audio clip using ffmpeg", nil
		},
		Activity: ffmpegActivity("replace-audio"),
	}
}

// replaceAudioCopyVideo decides whether the video stream can be copied into
// the MP4 output: MP4-family containers hold H.264/HEVC as-is; anything else
// (WebM/VP9, say) re-encodes, with a notice because that costs time.
func replaceAudioCopyVideo(ctx context.Context, config AppConfig, videoPath string) (streamCopy bool, notice string) {
	probe, err := probeStagedMedia(ctx, config, videoPath, "video")
	if err != nil {
		// Fail safe toward copying: generated clips are overwhelmingly MP4, and
		// a copy that cannot work fails loudly in ffmpeg rather than silently
		// burning a re-encode.
		return true, ""
	}
	if mp4Family(probe.Format) {
		return true, ""
	}
	return false, fmt.Sprintf("the video's container (%s) cannot hold a copied stream inside MP4, so the video was re-encoded.", probe.Format)
}

// probeMediaToolDefinition exposes probe_media: ffprobe facts of an attached
// clip as evidence — durations for split planning, codec facts for joins.
func probeMediaToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "probe_media",
		Title:       "Probe media",
		Description: "Use this when the user asks about an attached clip's properties (how long is it, what resolution, what codecs, does it have audio) or when planning cuts and joins needs exact numbers. Runs the locally installed ffprobe on the newest attached video (or the attached audio when no video is attached) and returns duration, dimensions, frame rate, codecs, and container as evidence. No parameters.",
		Example:     `{"name":"probe_media"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: probeMediaParamSchema(),
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedVideo(tools.AttachedVideos)
			kind := "video"
			if source == "" {
				source = firstAttachedAudio(tools.AttachedAudios)
				kind = "audio"
			}
			if source == "" {
				return nil, "probe requires an attached clip", errors.New("probe_media requires an attached video or audio clip — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "probe failed", err
			}
			defer os.RemoveAll(staging)
			input, err := stageMediaDataURL(staging, "input", source)
			if err != nil {
				return nil, "probe failed", err
			}
			result, err := probeStagedMedia(ctx, tools.Config, input, kind)
			if err != nil {
				return nil, "probe failed", err
			}
			return result, probeSummary(result), nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			activity.Provider = "ffmpeg"
			activity.Command = []string{"ffprobe", "json"}
			return activity
		},
	}
}

// ffmpegActivity is the shared activity builder for the ffmpeg media tools:
// default media fields (kind/count/model ride the result type) plus the
// ffmpeg command and provider attribution. Provider is pre-filled so the
// engine layer's fal/ollama attribution (toolActivityFromResult) skips these —
// ffmpeg results are neither.
func ffmpegActivity(verb string) func(result HarnessToolResult) HarnessToolActivity {
	return func(result HarnessToolResult) HarnessToolActivity {
		activity := defaultHarnessToolActivity(result)
		activity.Provider = "ffmpeg"
		activity.Command = []string{"ffmpeg", verb}
		return activity
	}
}

// ---------------------------------------------------------------------------
// Summary phrases
// ---------------------------------------------------------------------------

func splitPrompt(start, end string) string {
	switch {
	case start != "" && end != "":
		return "segment " + start + "–" + end
	case start != "":
		return "segment from " + start
	case end != "":
		return "segment through " + end
	default:
		return "full clip"
	}
}

func splitRangePhrase(start, end string) string {
	phrase := splitPrompt(start, end)
	if phrase == "full clip" {
		return ""
	}
	return " " + phrase
}

func splitModePhrase(fast bool) string {
	if fast {
		return "fast stream copy"
	}
	return "re-encoded for an exact cut"
}

func joinModePhrase(streamCopy bool) string {
	if streamCopy {
		return "stream copy"
	}
	return "re-encoded"
}

func extractModePhrase(streamCopy bool, ext string) string {
	if streamCopy {
		if ext == ".m4a" {
			return "AAC copied without re-encoding"
		}
		return "MP3 copied without re-encoding"
	}
	return "converted to MP3"
}

func probeSummary(result ToolProbeResult) string {
	var parts []string
	if result.Duration > 0 {
		parts = append(parts, fmt.Sprintf("%gs", result.Duration))
	}
	if result.Width > 0 && result.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", result.Width, result.Height))
	}
	if result.FPS > 0 {
		parts = append(parts, fmt.Sprintf("%ggfps", result.FPS))
	}
	codecs := result.VideoCodec
	if result.AudioCodec != "" {
		if codecs != "" {
			codecs += " + " + result.AudioCodec
		} else {
			codecs = result.AudioCodec
		}
	}
	if codecs != "" {
		parts = append(parts, codecs)
	}
	if result.Format != "" {
		parts = append(parts, "("+strings.SplitN(result.Format, ",", 2)[0]+")")
	}
	detail := strings.Join(parts, ", ")
	if detail == "" {
		return fmt.Sprintf("probed the attached %s with ffprobe (no streams reported)", result.Kind)
	}
	return fmt.Sprintf("probed the attached %s with ffprobe: %s", result.Kind, detail)
}

// ---------------------------------------------------------------------------
// Param schemas
// ---------------------------------------------------------------------------

func screenshotVideoParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"at": stringParam("The timestamp of the frame to capture — seconds (\"42\", \"12.5\") or clock (\"00:01:30\"). Required; \"0\" is the first frame."),
		},
		"required": []string{"at"},
	}
}

func splitVideoParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"start": stringParam("Optional — where the kept segment begins, in seconds (\"10\") or clock (\"00:01:30\"). Omit for the beginning of the clip."),
			"end":   stringParam("Optional — where the kept segment ends (exclusive), in seconds (\"25\") or clock (\"00:00:25\"). Omit for through the end."),
			"mode":  enumParam("Optional — \"accurate\" (the default) re-encodes for an exact cut; \"fast\" copies streams without re-encoding (instant, but cuts land on keyframes).", "fast", "accurate"),
		},
		"required": []string{},
	}
}

func joinVideosParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode": enumParam("Optional — \"auto\" (the default) copies when every clip matches and re-encodes otherwise; \"copy\" forces a lossless stream copy; \"reencode\" forces re-encoding.", "auto", "copy", "reencode"),
		},
		"required": []string{},
	}
}

func extractAudioParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             []string{},
	}
}

func replaceAudioParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             []string{},
	}
}

func probeMediaParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             []string{},
	}
}
