package main

// Local ffmpeg tools: the seven video/audio transform tools that run on the
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
	"math"
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
const localMediaEditUnavailableNote = "Atelier note: the user's latest request asks for a local video edit — capturing a frame, splitting or trimming a clip, joining clips, cropping/resizing/rotating a clip, or extracting/replacing audio — but no ffmpeg CLI was detected on this machine, so Atelier has no tool that can perform it. Do not claim the edit was done and do not attempt it through other tools. Tell the user plainly that local video editing needs a one-time install: install ffmpeg with `brew install ffmpeg` (or set an explicit binary in Settings → Video Tools); Atelier detects it automatically on the next message."

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
		transformVideoToolDefinition(),
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

// screenshotTimestampsCap bounds how many frames one screenshot_video call may
// capture. The batch form exists so "N frames at equal intervals" is one call
// instead of N planner calls against the 3-per-round cap; 16 keeps a stray
// "screenshot every second" request from turning into hundreds of seeks.
const screenshotTimestampsCap = 16

// splitTimestampTokens splits a planner-supplied screenshot `at` value into its
// individual timestamps: a single timestamp passes through alone, a list is
// comma-separated ("0,9.08,18.17" — spaces after commas tolerated). Tokens are
// trimmed but not validated; parseMediaTimestamp is the per-token check. Order
// is preserved — it is the frame order the user asked for.
func splitTimestampTokens(at string) []string {
	raw := strings.FieldsFunc(at, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	tokens := make([]string, 0, len(raw))
	for _, token := range raw {
		if token = strings.TrimSpace(token); token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

// equalIntervalTimestamps spreads count frame captures evenly across a clip of
// the given duration: i/count × duration for i = 0..count-1 — the first frame
// at 0 and the last at (count-1)/count × duration, so the clip's final frame is
// never requested (on a looping clip it would duplicate the first). Values are
// pre-rendered as bare-second strings, the form ffmpeg takes directly.
func equalIntervalTimestamps(durationSeconds float64, count int) []string {
	timestamps := make([]string, 0, count)
	for i := 0; i < count; i++ {
		seconds := float64(i) * durationSeconds / float64(count)
		timestamps = append(timestamps, strconv.FormatFloat(seconds, 'f', -1, 64))
	}
	return timestamps
}

// screenshotTimestampsPhrase renders a timestamp list for summaries and result
// prompts: up to six values joined with commas, longer lists show the first
// five and an ellipsis — "0, 9.083, 18.167, 27.25, 36.333, …".
func screenshotTimestampsPhrase(timestamps []string) string {
	shown := timestamps
	if len(shown) > 6 {
		shown = shown[:5]
	}
	phrase := strings.Join(shown, ", ")
	if len(timestamps) > 6 {
		phrase += ", …"
	}
	return phrase
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
// displayWidth/displayHeight carry the clip's DISPLAY size for an anamorphic
// source (frames stored squeezed, stretched on playback): the frame is
// resampled to what a player shows, because JPEG carries no aspect metadata
// to correct the squeeze at view time. Zero means square pixels — the stored
// frame is captured untouched.
func ffmpegScreenshotArgs(input, at, output string, displayWidth, displayHeight int) []string {
	args := []string{"-ss", at, "-i", input}
	if displayWidth > 0 && displayHeight > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d,setsar=1", displayWidth, displayHeight))
	}
	return append(args, "-frames:v", "1", "-q:v", "2", output)
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

// ffmpegTransformArgs applies -vf/-af filter chains with the house re-encode
// preset — geometry and speed filters cannot stream-copy, so every transform
// re-encodes. An empty chain is omitted (a speed-only transform passes just
// -af), since an empty filtergraph argument is a CLI error.
func ffmpegTransformArgs(input, videoFilters, audioFilters, output string) []string {
	args := []string{"-i", input}
	if videoFilters != "" {
		args = append(args, "-vf", videoFilters)
	}
	if audioFilters != "" {
		args = append(args, "-af", audioFilters)
	}
	return append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "18",
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", output)
}

// videoTransformFilters resolves one transform_video call into its -vf chain
// plus the op phrases for the summary, applying the ops in transform_image's
// order: aspectRatio crop (or, with mode blur/pad, the fill that lands the
// frame on a target-aspect canvas), then width/height resize, then rotate,
// then flip (rotation is not aspect-aware — it turns the finished frame), then
// the playback-speed rescale (setpts only touches timestamps, so it composes
// with any geometry). srcWidth and srcHeight are the source clip's dimensions
// and must be positive whenever aspectRatio or a fill mode is set; the other
// branches need no source geometry. With both width and height and no
// aspectRatio, the default cover idiom
// (force_original_aspect_ratio=increase + crop) lands on exactly those pixels
// while center-trimming the overflowing side — mode blur/pad instead contains
// the frame on exactly those pixels and fills the remainder. The shape is
// never stretched.
func videoTransformFilters(call HarnessToolCall, srcWidth, srcHeight int) (string, []string) {
	var chain []string
	var ops []string
	aspect := strings.TrimSpace(call.AspectRatio)
	mode := strings.TrimSpace(call.Mode)
	fill := transformFillMode(call)
	if aspect != "" {
		aspectWidth, aspectHeight, _ := parseAspectRatio(aspect)
		if fill {
			canvasWidth, canvasHeight := aspectFillDimensions(srcWidth, srcHeight, aspectWidth, aspectHeight)
			canvasWidth, canvasHeight = evenDown(canvasWidth), evenDown(canvasHeight)
			chain = append(chain, fillFilterChain(mode, srcWidth, srcHeight, canvasWidth, canvasHeight))
			ops = append(ops, fillOpPhrase(mode, aspect, canvasWidth, canvasHeight))
		} else {
			cropWidth, cropHeight := aspectCropDimensions(srcWidth, srcHeight, aspectWidth, aspectHeight)
			cropWidth, cropHeight = evenDown(cropWidth), evenDown(cropHeight)
			chain = append(chain, fmt.Sprintf("crop=%d:%d", cropWidth, cropHeight))
			ops = append(ops, fmt.Sprintf("cropped to %s (center %dx%d)", aspect, cropWidth, cropHeight))
		}
	}
	switch {
	case call.Width > 0 && call.Height > 0:
		if fill && aspect == "" {
			// The fill already names the exact canvas, so no separate resize
			// phrase — the frame was contained onto exactly these pixels.
			chain = append(chain, fillFilterChain(mode, srcWidth, srcHeight, call.Width, call.Height))
			ops = append(ops, fillOpPhrase(mode, "", call.Width, call.Height))
		} else {
			if aspect == "" {
				chain = append(chain, fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d",
					call.Width, call.Height, call.Width, call.Height))
			} else {
				chain = append(chain, fmt.Sprintf("scale=%d:%d", call.Width, call.Height))
			}
			ops = append(ops, fmt.Sprintf("resized to %dx%d", call.Width, call.Height))
		}
	case call.Width > 0:
		chain = append(chain, fmt.Sprintf("scale=%d:-2", call.Width))
		ops = append(ops, fmt.Sprintf("resized to %d pixels wide", call.Width))
	case call.Height > 0:
		chain = append(chain, fmt.Sprintf("scale=-2:%d", call.Height))
		ops = append(ops, fmt.Sprintf("resized to %d pixels tall", call.Height))
	}
	switch call.Rotate {
	case 90:
		chain = append(chain, "transpose=1")
		ops = append(ops, "rotated 90° clockwise")
	case 180:
		chain = append(chain, "transpose=1,transpose=1")
		ops = append(ops, "rotated 180°")
	case 270:
		chain = append(chain, "transpose=2")
		ops = append(ops, "rotated 270° clockwise")
	}
	switch strings.TrimSpace(call.Flip) {
	case "horizontal":
		chain = append(chain, "hflip")
		ops = append(ops, "flipped horizontal")
	case "vertical":
		chain = append(chain, "vflip")
		ops = append(ops, "flipped vertical")
	}
	if call.Speed != 0 {
		chain = append(chain, "setpts=PTS/"+formatPlaybackSpeed(call.Speed))
		ops = append(ops, formatPlaybackSpeed(call.Speed)+"x playback speed")
	}
	return strings.Join(chain, ","), ops
}

// formatPlaybackSpeed renders a playback multiplier in its shortest exact form
// (3, 0.5, 1.75) for both the setpts expression and the op phrase.
func formatPlaybackSpeed(speed float64) string {
	return strconv.FormatFloat(speed, 'f', -1, 64)
}

// atempoChain decomposes a playback multiplier into atempo filter instances,
// each inside the filter's [0.5, 2.0] tempo envelope — one instance tops out
// at 2x, so a 3x speedup or a 0.25x crawl needs a chain. atempo changes tempo
// without shifting pitch, keeping the audio in sync with the setpts-rescaled
// video. speed must be positive and within transform_video's validated range.
func atempoChain(speed float64) string {
	stages := []string{}
	for speed > 2.0 {
		stages = append(stages, "atempo=2.0")
		speed /= 2.0
	}
	for speed < 0.5 {
		stages = append(stages, "atempo=0.5")
		speed /= 0.5
	}
	return strings.Join(append(stages, "atempo="+formatPlaybackSpeed(speed)), ",")
}

// videoPortionSpeedFilterComplex builds the single-invocation filter_complex
// behind a portion speed change: the head before startSec, the
// [startSec, endSec) middle rescaled by speed, and (when endBounded) the tail
// from endSec are trimmed, timestamp-reset, and concatenated — concat requires
// each segment to start at zero, hence the per-segment PTS-STARTPTS resets,
// and the middle's audio rides the same atempo chain as a whole-clip change so
// it stays pitch-preserved and in sync. hasAudio selects the atrim/asetpts
// chains (referencing [0:a] on a silent clip is a filtergraph error);
// geometryFilters (crop/scale/rotate/flip from videoTransformFilters) is
// applied to the joined output, so shape ops cover the whole clip. The final
// video label is always [vout] and the audio label [acat]; the args builder
// maps them.
func videoPortionSpeedFilterComplex(geometryFilters string, startSec, endSec float64, endBounded, hasAudio bool, speed float64) string {
	segments := 1
	if startSec > 0 {
		segments++
	}
	if endBounded {
		segments++
	}
	speedExpr := "(PTS-STARTPTS)/" + formatPlaybackSpeed(speed)
	var parts []string
	var labels strings.Builder
	// Head: [clip start, startSec) at normal speed.
	if startSec > 0 {
		bound := "end=" + formatPlaybackSpeed(startSec)
		parts = append(parts, "[0:v]trim="+bound+",setpts=PTS-STARTPTS[v0]")
		labels.WriteString("[v0]")
		if hasAudio {
			parts = append(parts, "[0:a]atrim="+bound+",asetpts=PTS-STARTPTS[a0]")
			labels.WriteString("[a0]")
		}
	}
	// Middle: [startSec, endSec) rescaled by speed (to the clip's end when
	// endBounded is false).
	middleBound := "start=" + formatPlaybackSpeed(startSec)
	if endBounded {
		middleBound += ":end=" + formatPlaybackSpeed(endSec)
	}
	parts = append(parts, "[0:v]trim="+middleBound+",setpts="+speedExpr+"[v1]")
	labels.WriteString("[v1]")
	if hasAudio {
		parts = append(parts, "[0:a]atrim="+middleBound+",asetpts="+speedExpr+","+atempoChain(speed)+"[a1]")
		labels.WriteString("[a1]")
	}
	// Tail: [endSec, clip end) at normal speed.
	if endBounded {
		bound := "start=" + formatPlaybackSpeed(endSec)
		parts = append(parts, "[0:v]trim="+bound+",setpts=PTS-STARTPTS[v2]")
		labels.WriteString("[v2]")
		if hasAudio {
			parts = append(parts, "[0:a]atrim="+bound+",asetpts=PTS-STARTPTS[a2]")
			labels.WriteString("[a2]")
		}
	}
	concat := labels.String() + "concat=n=" + strconv.Itoa(segments) + ":v=1:a="
	if hasAudio {
		concat += "1"
	} else {
		concat += "0"
	}
	if geometryFilters == "" {
		concat += "[vout]"
	} else {
		concat += "[vcat]"
	}
	if hasAudio {
		concat += "[acat]"
	}
	parts = append(parts, concat)
	if geometryFilters != "" {
		parts = append(parts, "[vcat]"+geometryFilters+"[vout]")
	}
	return strings.Join(parts, ";")
}

// ffmpegPortionSpeedArgs runs a portion speed change's filter_complex with the
// house re-encode preset. Every segment re-encodes once into uniform streams —
// the concat filter cannot stream-copy, so the normal-speed head and tail are
// re-encoded too.
func ffmpegPortionSpeedArgs(input, filterComplex string, hasAudio bool, output string) []string {
	args := []string{"-i", input, "-filter_complex", filterComplex, "-map", "[vout]"}
	if hasAudio {
		args = append(args, "-map", "[acat]")
	}
	return append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "18",
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", output)
}

// portionSpeedPhrase renders the op phrase for a portion speed change:
// "3x playback speed from 10s to 20s", with "the start"/"the end" for the
// open bounds.
func portionSpeedPhrase(startSec, endSec float64, endBounded bool, speed float64) string {
	startLabel := "the start"
	if startSec > 0 {
		startLabel = formatPlaybackSpeed(startSec) + "s"
	}
	endLabel := "the end"
	if endBounded {
		endLabel = formatPlaybackSpeed(endSec) + "s"
	}
	return fmt.Sprintf("%sx playback speed from %s to %s", formatPlaybackSpeed(speed), startLabel, endLabel)
}

// transformVideoPortionFacts resolves what a portion speed change needs from
// the attached clip: its total duration (to validate the bounds and clamp an
// end past the clip) and whether it carries audio (the filter_complex must not
// reference [0:a] on a silent clip). ffprobe answers both for any container;
// the MP4 box sniff (mvhd duration, a 'soun' trak) covers generation outputs
// when ffprobe is unavailable — the same fallback shape as the aspect crop's
// dimension resolution.
func transformVideoPortionFacts(ctx context.Context, config AppConfig, input, sourceDataURL string) (float64, bool, error) {
	if _, ok := resolveLocalFFprobeBinary(config); ok {
		if probe, err := probeStagedMedia(ctx, config, input, "video"); err == nil && probe.Duration > 0 {
			return probe.Duration, probe.AudioCodec != "", nil
		}
	}
	if data, _, err := decodeMediaDataURL(sourceDataURL); err == nil {
		if seconds, ok := mp4DurationSeconds(data); ok {
			return seconds, mp4HasAudioTrack(data), nil
		}
	}
	return 0, false, errors.New("transform_video could not read the attached clip's duration for the portion speed change — ffprobe is unavailable and the clip is not a readable MP4")
}

// screenshotClipDuration resolves the attached clip's duration for
// screenshot_video's equal-interval batch form (count without at). ffprobe
// answers for any container; the MP4 box sniff (mvhd) covers generation
// outputs when ffprobe is unavailable — the same fallback shape as
// transformVideoPortionFacts. The error names the explicit-at escape so the
// planner can repair into a timestamp list instead of retrying the same call.
func screenshotClipDuration(ctx context.Context, config AppConfig, input, sourceDataURL string) (float64, error) {
	if _, ok := resolveLocalFFprobeBinary(config); ok {
		if probe, err := probeStagedMedia(ctx, config, input, "video"); err == nil && probe.Duration > 0 {
			return probe.Duration, nil
		}
	}
	if data, _, err := decodeMediaDataURL(sourceDataURL); err == nil {
		if seconds, ok := mp4DurationSeconds(data); ok {
			return seconds, nil
		}
	}
	return 0, errors.New(`screenshot_video could not read the clip's duration for the equal-interval capture (ffprobe is unavailable and the clip is not a readable MP4) — capture explicit timestamps instead, e.g. {"name":"screenshot_video","at":"0,30,60"}`)
}

// aspectCorrection describes an anamorphic clip: frames stored at
// StoredWidth×StoredHeight, displayed at DisplayWidth×DisplayHeight by
// players stretching non-square pixels. SampleAspectRatio and
// DisplayAspectRatio carry ffprobe's strings when it answered (empty on the
// MP4-sniff path, which only sees the two sizes).
type aspectCorrection struct {
	StoredWidth        int
	StoredHeight       int
	DisplayWidth       int
	DisplayHeight      int
	SampleAspectRatio  string
	DisplayAspectRatio string
}

// screenshotAspectCorrection resolves whether the attached clip is
// anamorphic — frames stored at one size, displayed at another — and the
// display size screenshots must be resampled to (a raw frame dump carries
// the squeezed storage size, and JPEG has no aspect metadata to fix it at
// view time). ffprobe's sample_aspect_ratio answers for any container;
// without ffprobe the MP4 sniff compares tkhd's presentation size against
// the coded sample size in stsd — a difference is the container's own
// stretch. A 90°/270° rotation swaps the display axes (the stretch rotates
// with the frame, and ffmpeg auto-rotates the captured frame before the
// resample). Fail-soft: ok false means square pixels, nothing readable, or
// no way to check — capture proceeds untouched rather than failing.
func screenshotAspectCorrection(ctx context.Context, config AppConfig, input, sourceDataURL string) (aspectCorrection, bool) {
	if _, ok := resolveLocalFFprobeBinary(config); ok {
		if probe, err := probeStagedMedia(ctx, config, input, "video"); err == nil {
			num, den, _ := parseVideoRational(probe.SampleAspectRatio)
			if displayWidth, displayHeight, stretched := anamorphicDisplayDimensions(probe.Width, probe.Height, num, den); stretched {
				correction := aspectCorrection{
					StoredWidth:        probe.Width,
					StoredHeight:       probe.Height,
					DisplayWidth:       displayWidth,
					DisplayHeight:      displayHeight,
					SampleAspectRatio:  probe.SampleAspectRatio,
					DisplayAspectRatio: probe.DisplayAspectRatio,
				}
				if probe.Rotation%180 != 0 {
					correction.DisplayWidth, correction.DisplayHeight = displayHeight, displayWidth
				}
				return correction, true
			}
			return aspectCorrection{}, false
		}
	}
	if data, _, err := decodeMediaDataURL(sourceDataURL); err == nil {
		codedWidth, codedHeight, ok := mp4CodedVideoDimensions(data)
		if !ok {
			return aspectCorrection{}, false
		}
		storedWidth, storedHeight, ok := mp4VideoDimensions(data)
		if !ok {
			return aspectCorrection{}, false
		}
		displayWidth, displayHeight := int(math.Round(storedWidth)), int(math.Round(storedHeight))
		if displayWidth != codedWidth || displayHeight != codedHeight {
			return aspectCorrection{
				StoredWidth:   codedWidth,
				StoredHeight:  codedHeight,
				DisplayWidth:  displayWidth,
				DisplayHeight: displayHeight,
			}, true
		}
	}
	return aspectCorrection{}, false
}

// aspectCorrectionNotice renders the user-facing notice for a resampled
// capture: what the clip stores, what players show, and that the frames were
// captured at the display size. DisplayAspectRatio (ffprobe's "16:9") rides
// along when known.
func aspectCorrectionNotice(correction aspectCorrection) string {
	aspect := "non-square pixels"
	if correction.DisplayAspectRatio != "" {
		aspect += ", " + correction.DisplayAspectRatio
	}
	return fmt.Sprintf("the video stores frames at %dx%d and stretches them to %dx%d on playback (%s) — captured frames were resampled to the display size so they are not squeezed",
		correction.StoredWidth, correction.StoredHeight, correction.DisplayWidth, correction.DisplayHeight, aspect)
}

// evenDown floors a pixel count to the nearest even value — H.264's yuv420p
// chroma needs even dimensions, and a computed crop rect can land odd (a
// 1000x333 clip cropped to 1:1 is 333x333).
func evenDown(value int) int {
	return value - value%2
}

// containScaleDimensions scales width×height to the largest even-pixel frame
// that fits inside canvasWidth×canvasHeight without changing aspect — the
// fill modes' foreground. Truncation before evenDown keeps the frame inside
// the canvas even when the exact scale lands on a rounding boundary.
func containScaleDimensions(width, height, canvasWidth, canvasHeight int) (int, int) {
	if width <= 0 || height <= 0 || canvasWidth <= 0 || canvasHeight <= 0 {
		return width, height
	}
	scale := math.Min(float64(canvasWidth)/float64(width), float64(canvasHeight)/float64(height))
	fittedWidth := evenDown(int(float64(width) * scale))
	fittedHeight := evenDown(int(float64(height) * scale))
	if fittedWidth < 2 {
		fittedWidth = 2
	}
	if fittedHeight < 2 {
		fittedHeight = 2
	}
	return fittedWidth, fittedHeight
}

// transformFillMode reports whether the call's mode asks transform_video to
// fill the added canvas (blur/pad) instead of cropping it away.
func transformFillMode(call HarnessToolCall) bool {
	mode := strings.TrimSpace(call.Mode)
	return mode == "blur" || mode == "pad"
}

// fillFilterChain builds the blur/pad filter graph that lands a width×height
// source on a canvasWidth×canvasHeight canvas without cropping: the foreground
// is contained (even-pixel, aspect-preserving) and centered; blur fills the
// remaining canvas with an enlarged, heavily blurred copy of the frame (the
// standard vertical-video background), pad letterboxes with black bars. The
// graph's internal labels let it sit inside a longer -vf chain or a
// filter_complex segment — its input and output stay unlabeled.
func fillFilterChain(mode string, srcWidth, srcHeight, canvasWidth, canvasHeight int) string {
	fgWidth, fgHeight := containScaleDimensions(srcWidth, srcHeight, canvasWidth, canvasHeight)
	if mode == "pad" {
		return fmt.Sprintf("scale=%d:%d,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
			fgWidth, fgHeight, canvasWidth, canvasHeight)
	}
	sigma := canvasWidth / 40
	if canvasHeight < canvasWidth {
		sigma = canvasHeight / 40
	}
	if sigma < 10 {
		sigma = 10
	}
	return fmt.Sprintf("split=2[bg][fg];"+
		"[bg]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,gblur=sigma=%d[bgf];"+
		"[fg]scale=%d:%d[fgf];"+
		"[bgf][fgf]overlay=(W-w)/2:(H-h)/2",
		canvasWidth, canvasHeight, canvasWidth, canvasHeight, sigma, fgWidth, fgHeight)
}

// fillOpPhrase renders the op phrase for a fill: "filled to 9:16 (1080x1920)
// with a blurred background" / "padded to 1080x1920 (black bars)". An empty
// aspect names a canvas that came from explicit width+height.
func fillOpPhrase(mode, aspect string, canvasWidth, canvasHeight int) string {
	shape := fmt.Sprintf("%dx%d", canvasWidth, canvasHeight)
	if aspect != "" {
		shape = fmt.Sprintf("%s (%s)", aspect, shape)
	}
	if mode == "pad" {
		return "padded to " + shape + " (black bars)"
	}
	return "filled to " + shape + " with a blurred background"
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
// projected, never passed through). The aspect fields surface anamorphic
// sources — frames stored at Width×Height but stretched on playback: pixels
// that aren't square (SampleAspectRatio "4:3" say), so players show
// DisplayWidth×DisplayHeight instead.
type ToolProbeResult struct {
	Kind               string  `json:"kind"`
	Duration           float64 `json:"durationSeconds"`
	Width              int     `json:"width,omitempty"`
	Height             int     `json:"height,omitempty"`
	FPS                float64 `json:"fps,omitempty"`
	VideoCodec         string  `json:"videoCodec,omitempty"`
	AudioCodec         string  `json:"audioCodec,omitempty"`
	Format             string  `json:"format,omitempty"`
	BitRate            string  `json:"bitrate,omitempty"`
	SizeBytes          int64   `json:"sizeBytes,omitempty"`
	SampleAspectRatio  string  `json:"sampleAspectRatio,omitempty"`
	DisplayAspectRatio string  `json:"displayAspectRatio,omitempty"`
	DisplayWidth       int     `json:"displayWidth,omitempty"`
	DisplayHeight      int     `json:"displayHeight,omitempty"`
	Rotation           int     `json:"rotation,omitempty"`
}

// ffprobeSideData is the rotation-bearing subset of ffprobe's per-stream
// side_data_list entries.
type ffprobeSideData struct {
	Rotation float64 `json:"rotation"`
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
		CodecType          string            `json:"codec_type"`
		CodecName          string            `json:"codec_name"`
		Width              int               `json:"width"`
		Height             int               `json:"height"`
		AvgFrameRate       string            `json:"avg_frame_rate"`
		SampleAspectRatio  string            `json:"sample_aspect_ratio"`
		DisplayAspectRatio string            `json:"display_aspect_ratio"`
		SideDataList       []ffprobeSideData `json:"side_data_list"`
		Tags               map[string]string `json:"tags"`
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
				result.Rotation = ffprobeRotation(stream.SideDataList, stream.Tags)
				// The aspect fields only carry the anomaly: SampleAspectRatio
				// reports for every clip with a known ratio (square included),
				// the display size only when it differs from the stored one.
				if num, den, ok := parseVideoRational(stream.SampleAspectRatio); ok && num > 0 {
					result.SampleAspectRatio = stream.SampleAspectRatio
					if displayWidth, displayHeight, stretched := anamorphicDisplayDimensions(stream.Width, stream.Height, num, den); stretched {
						result.DisplayAspectRatio = stream.DisplayAspectRatio
						result.DisplayWidth = displayWidth
						result.DisplayHeight = displayHeight
					}
				}
			}
		case "audio":
			if result.AudioCodec == "" {
				result.AudioCodec = stream.CodecName
			}
		}
	}
	return result, nil
}

// parseVideoRational reads ffprobe's rational strings ("4:3", "1:1", "0:1")
// as integers. ok is false for anything that is not N:D with a nonzero D;
// num == 0 is ffprobe's "unknown" (0:1), which callers treat as square.
func parseVideoRational(value string) (num, den int, ok bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	num, numErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	den, denErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if numErr != nil || denErr != nil || den == 0 {
		return 0, 0, false
	}
	return num, den, true
}

// anamorphicDisplayDimensions computes the square-pixel display size of a
// frame stored width×height under a sample aspect ratio: displayWidth =
// round(width × num/den), height unchanged — SAR stretches width only. ok is
// false for square pixels (num == den), unknown or degenerate ratios, and a
// ratio that rounds back onto the stored width.
func anamorphicDisplayDimensions(width, height, num, den int) (displayWidth, displayHeight int, ok bool) {
	if width <= 0 || height <= 0 || num <= 0 || den <= 0 || num == den {
		return 0, 0, false
	}
	scaled := int(math.Round(float64(width) * float64(num) / float64(den)))
	if scaled <= 0 || scaled == width {
		return 0, 0, false
	}
	return scaled, height, true
}

// ffprobeRotation reads a stream's rotation metadata: modern ffprobe reports
// the display matrix's degrees in side_data_list, older builds a "rotate"
// tag. Upright (or unparseable) is 0.
func ffprobeRotation(sideData []ffprobeSideData, tags map[string]string) int {
	for _, data := range sideData {
		if data.Rotation != 0 {
			return int(math.Round(data.Rotation))
		}
	}
	if tagged := strings.TrimSpace(tags["rotate"]); tagged != "" {
		if degrees, err := strconv.ParseFloat(tagged, 64); err == nil && degrees != 0 {
			return int(math.Round(degrees))
		}
	}
	return 0
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

// screenshotVideoToolDefinition exposes screenshot_video: one or more frames
// of the attached video, as JPEGs attached to the reply. The batch forms are
// one call regardless of frame count — the plan cap is 3 calls per round, so
// "10 screenshots at equal intervals" as 10 calls is structurally unreachable
// (conv_8ba2eae289b5f884d7064b18: 3 of 10 frames, loop budget spent).
func screenshotVideoToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "screenshot_video",
		Title:       "Screenshot video",
		Description: "Use this when the user asks to grab a frame, take a screenshot, still, or thumbnail of an attached video. Requires an attached video clip (one attached or @-mentioned this turn, or the conversation's newest video). at names the frame's timestamp — seconds (\"42\", \"12.5\") or clock (\"00:01:30\") — and may be a comma-separated list to capture several specific frames in ONE call (\"0,9.08,18.17\"). count is the equal-interval batch form: omit at and set count to N to capture N frames spread evenly across the whole clip — the tool reads the duration itself, so prefer it for \"N screenshots/screenshots at equal intervals\" requests and plan it as a single call with no probe_media round. When both are set, at wins. Frames are captured at the clip's display size — a clip that stores frames squeezed and stretches them on playback (non-square pixels) is resampled so its frames come out correctly shaped. Each captured JPEG frame is attached to the assistant reply and becomes the conversation's newest image.",
		Example:     `{"name":"screenshot_video","at":"4.5"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: screenshotVideoParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			at := strings.TrimSpace(call.At)
			if at == "" {
				if call.Count < 1 {
					return []string{prefix + ".at or .count is required for screenshot_video (the frame timestamp, or how many equal-interval frames to capture)"}
				}
				if call.Count > screenshotTimestampsCap {
					return []string{fmt.Sprintf("%s.count must be between 1 and %d for screenshot_video (capture more via multiple calls)", prefix, screenshotTimestampsCap)}
				}
				return nil
			}
			tokens := splitTimestampTokens(at)
			if len(tokens) == 0 {
				return []string{prefix + ".at is required for screenshot_video (the timestamp of the frame)"}
			}
			if len(tokens) > screenshotTimestampsCap {
				return []string{fmt.Sprintf("%s.at lists %d timestamps; screenshot_video captures at most %d per call", prefix, len(tokens), screenshotTimestampsCap)}
			}
			for _, token := range tokens {
				if _, ok := parseMediaTimestamp(token); !ok {
					return []string{prefix + ".at must be a timestamp in seconds (\"42\") or clock (\"00:01:30\") for screenshot_video, or a comma-separated list of them"}
				}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedVideo(tools.AttachedVideos)
			if source == "" {
				return nil, "screenshot requires an attached video clip", errors.New("screenshot_video requires an attached video clip — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "screenshot failed", err
			}
			defer os.RemoveAll(staging)
			input, err := stageMediaDataURL(staging, "input", source)
			if err != nil {
				return nil, "screenshot failed", err
			}
			// Resolve the frame list: explicit timestamps in the planner's
			// order, or count frames spread evenly across the clip (the
			// duration read here, not by a probe_media round).
			at := strings.TrimSpace(call.At)
			var timestamps []string
			equalIntervals := false
			if at != "" {
				timestamps = splitTimestampTokens(at)
			} else {
				duration, err := screenshotClipDuration(ctx, tools.Config, input, source)
				if err != nil {
					return nil, "screenshot failed", err
				}
				timestamps = equalIntervalTimestamps(duration, call.Count)
				equalIntervals = true
			}
			// Anamorphic capture correction: a clip that stores frames squeezed
			// and stretches them on playback must be resampled to its display
			// size, or every frame comes out squeezed. Fail-soft — the capture
			// itself must never fail over this check.
			correction, aspectCorrected := screenshotAspectCorrection(ctx, tools.Config, input, source)
			images := make([]string, 0, len(timestamps))
			var missed []string
			for i, timestamp := range timestamps {
				framePath := filepath.Join(staging, fmt.Sprintf("frame_%02d.jpg", i+1))
				captureErr := runLocalFFmpeg(ctx, tools.Config, ffmpegScreenshotArgs(input, timestamp, framePath, correction.DisplayWidth, correction.DisplayHeight))
				if captureErr == nil {
					var data []byte
					if data, captureErr = os.ReadFile(framePath); captureErr == nil && len(data) == 0 {
						captureErr = errors.New("the frame file was empty")
					}
					if captureErr == nil {
						images = append(images, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(data))
					}
				}
				if captureErr != nil {
					// A single-frame call keeps the historical hard error; in a
					// batch, one bad timestamp (past the clip's end, say)
					// must not discard the frames that did capture.
					if len(timestamps) == 1 {
						return nil, "screenshot failed", fmt.Errorf("ffmpeg produced no frame at %s: %v", timestamp, captureErr)
					}
					missed = append(missed, timestamp)
				}
			}
			if len(images) == 0 {
				return nil, "screenshot failed", fmt.Errorf("ffmpeg produced no frames at %s", screenshotTimestampsPhrase(timestamps))
			}
			prompt := "frame at " + timestamps[0]
			summary := fmt.Sprintf("captured the frame at %s from the attached video with ffmpeg", timestamps[0])
			if len(timestamps) > 1 {
				prompt = fmt.Sprintf("%d frames at %s", len(timestamps), screenshotTimestampsPhrase(timestamps))
				summary = fmt.Sprintf("captured %d of %d frames from the attached video with ffmpeg", len(images), len(timestamps))
				if equalIntervals {
					summary = fmt.Sprintf("captured %d of %d frames at equal intervals across the attached video with ffmpeg", len(images), len(timestamps))
				}
			}
			var notices []string
			if aspectCorrected {
				notices = append(notices, aspectCorrectionNotice(correction))
			}
			for _, timestamp := range missed {
				notices = append(notices, fmt.Sprintf("no frame was captured at %s — the timestamp is likely past the end of the clip", timestamp))
			}
			output := ToolImageResult{
				Model:   ffmpegModelName,
				Prompt:  prompt,
				Count:   len(images),
				Images:  images,
				Notices: notices,
			}
			return output, summary, nil
		},
		Activity: ffmpegActivity("screenshot"),
	}
}

// splitVideoToolDefinition exposes split_video: cut a segment out of the
// attached video (trim away the rest). One call keeps exactly one segment —
// the description teaches the planner that a multi-part split is one call per
// part in the same plan (conv_e11bdd971b1b1e674d23c6e3: "split into two
// parts at the 10s mark" produced only the tail clip because the planner
// mapped the whole task onto a single start=10 call).
func splitVideoToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "split_video",
		Title:       "Split video",
		Description: "Use this when the user asks to cut, trim, split, or crop out a portion of an attached video — keep a segment, drop the beginning or end, or split off the tail. Each call keeps exactly ONE segment and attaches one clip: to split a clip into parts, plan one call per part in the same plan — splitting a clip at 10s is two calls, {\"end\":\"10\"} for the first part and {\"start\":\"10\"} for the second, not one call. Requires an attached video clip (one attached or @-mentioned this turn, or the conversation's newest video). start and end are timestamps in seconds (\"10\", \"12.5\") or clock (\"00:01:30\"); omit start for the beginning of the clip, omit end for through the end. mode \"accurate\" (the default) re-encodes so the cut lands on the exact frame; \"fast\" copies the streams without re-encoding — instant and lossless, but cuts land on the video's keyframes so the clip may begin slightly before the requested start (the right choice for long videos and for clips from the same generator). The resulting clip is attached to the assistant reply and becomes the conversation's newest video.",
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

// transformVideoToolDefinition exposes transform_video: crop an attached
// video to an aspect ratio, resize it to exact dimensions, rotate, flip, or
// change its playback speed — the video sibling of transform_image, on one
// ffmpeg filter chain.
func transformVideoToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "transform_video",
		Title:       "Transform video",
		Description: "Use this when the user asks to change the shape or size of an attached video — crop it to an aspect ratio, convert it between shapes (e.g. a 16:9 clip to 9:16 vertical for Reels/Shorts/TikTok, or to 1:1), downscale or resize it to specific pixel dimensions (for example a clip too large for a video model's input limit), rotate a sideways clip, mirror it — or to speed up or slow down its playback, in whole or in part. It crops to a shape, not a region of time — cutting a segment out is split_video. Requires an attached video clip (one attached or @-mentioned this turn, or the conversation's newest video). aspectRatio changes the shape to a W:H ratio like \"1:1\", \"4:5\", \"9:16\", or \"16:9\"; mode picks how the new shape is reached — \"crop\" (the default) trims the longer side from the center, \"blur\" fills the added canvas with an enlarged, heavily blurred copy of the frame (the standard vertical-video background; keeps the whole frame visible), \"pad\" letterboxes with black bars; to have new scene content GENERATED in the added area instead of bars or blur, use reframe_video. width and height resize — both together lands on exactly those pixels (reaching them via the same mode: center-cropped by default, blur/pad-filled otherwise, never stretched), one alone preserves aspect; rotate is 90, 180, or 270 clockwise; flip is \"horizontal\" or \"vertical\"; speed is a playback multiplier — 3 plays three times as fast, 0.5 at half speed (slow motion), between 0.25 and 8, with the audio tempo-adjusted to stay in sync (pitch preserved). Alone, speed applies to the WHOLE clip; with the optional start and end bounds (timestamps in seconds or clock form, like split_video's) it applies to that PORTION only — speed 3 with start 10 and end 20 plays 10–20s at 3x while everything before and after keeps its normal speed (e.g. \"make the middle of this clip twice as fast\", \"slow-mo the dive between 3s and 7s\"). At least one operation is required. The video is re-encoded to H.264 with its audio kept. The result is attached to the assistant reply and becomes the conversation's newest video.",
		Example:     `{"name":"transform_video","width":1280,"height":720}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: transformVideoParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			aspect := strings.TrimSpace(call.AspectRatio)
			if aspect != "" {
				if _, _, ok := parseAspectRatio(aspect); !ok {
					return []string{prefix + `.aspectRatio must be a W:H ratio like "1:1" or "16:9" for transform_video`}
				}
			}
			switch strings.TrimSpace(call.Mode) {
			case "", "crop", "blur", "pad":
			default:
				return []string{prefix + `.mode must be "crop", "blur", or "pad" for transform_video`}
			}
			if transformFillMode(call) && aspect == "" && (call.Width <= 0 || call.Height <= 0) {
				return []string{prefix + `.mode "blur" and "pad" need a shape target for transform_video — aspectRatio, or width and height together (a lone width or height only resizes)`}
			}
			if call.Width < 0 || call.Height < 0 {
				return []string{prefix + ".width and .height must be positive pixel counts for transform_video"}
			}
			if (call.Width > 0 && call.Width%2 != 0) || (call.Height > 0 && call.Height%2 != 0) {
				return []string{prefix + ".width and .height must be even pixel counts for transform_video (video encoders require even dimensions)"}
			}
			if call.Speed != 0 && (call.Speed < 0.25 || call.Speed > 8) {
				return []string{prefix + ".speed must be a playback multiplier between 0.25 and 8 for transform_video (2 plays twice as fast, 0.5 at half speed)"}
			}
			if call.Speed == 1 {
				return []string{prefix + ".speed must not be 1 for transform_video — a multiplier of 1 leaves playback unchanged"}
			}
			startToken := strings.TrimSpace(call.Start)
			endToken := strings.TrimSpace(call.End)
			if startToken != "" || endToken != "" {
				if call.Speed == 0 {
					return []string{prefix + ".start and .end are only valid with speed for transform_video — to cut a segment out, use split_video"}
				}
				startSec, hasStart := parseMediaTimestamp(startToken)
				if startToken != "" && !hasStart {
					return []string{prefix + `.start must be a timestamp in seconds ("10") or clock ("00:01:30") for transform_video`}
				}
				endSec, hasEnd := parseMediaTimestamp(endToken)
				if endToken != "" && !hasEnd {
					return []string{prefix + `.end must be a timestamp in seconds ("20") or clock ("00:00:20") for transform_video`}
				}
				if hasStart && hasEnd && endSec <= startSec {
					return []string{prefix + ".end must be after start for transform_video"}
				}
			}
			if call.Width == 0 && call.Height == 0 && aspect == "" && call.Rotate == 0 && strings.TrimSpace(call.Flip) == "" && call.Speed == 0 {
				return []string{prefix + ".transform_video needs at least one of aspectRatio, width, height, rotate, flip, or speed"}
			}
			switch call.Rotate {
			case 0, 90, 180, 270:
			default:
				return []string{prefix + ".rotate must be 90, 180, or 270 for transform_video"}
			}
			switch strings.TrimSpace(call.Flip) {
			case "", "horizontal", "vertical":
			default:
				return []string{prefix + `.flip must be "horizontal" or "vertical" for transform_video`}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedVideo(tools.AttachedVideos)
			if source == "" {
				return nil, "transform requires an attached video clip", errors.New("transform_video requires an attached video clip — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-ffmpeg-*")
			if err != nil {
				return nil, "transform failed", err
			}
			defer os.RemoveAll(staging)
			input, err := stageMediaDataURL(staging, "input", source)
			if err != nil {
				return nil, "transform failed", err
			}
			srcWidth, srcHeight := 0, 0
			if strings.TrimSpace(call.AspectRatio) != "" || transformFillMode(call) {
				srcWidth, srcHeight, err = transformVideoSourceDimensions(ctx, tools.Config, input, source)
				if err != nil {
					return nil, "transform failed", err
				}
			}
			staged := filepath.Join(staging, "transformed.mp4")
			var args []string
			var ops []string
			notices := []string{
				"the video was re-encoded (H.264, CRF 18) — crop, resize, rotate, flip, and speed changes cannot stream-copy.",
			}
			// Portion speed change: the bounds resolve against the clip's
			// real duration — an end at/past the end clamps open (the tail
			// segment would be empty and break the concat), and only a
			// portion with a normal-speed head or tail remaining needs the
			// filter_complex; one covering the whole clip falls through to
			// the plain whole-clip run.
			startSec, endSec := 0.0, 0.0
			endBounded := false
			hasAudio := false
			if call.Speed != 0 && (strings.TrimSpace(call.Start) != "" || strings.TrimSpace(call.End) != "") {
				startSec, _ = parseMediaTimestamp(strings.TrimSpace(call.Start)) // validation guarantees these parse
				var hasEnd bool
				if endRaw := strings.TrimSpace(call.End); endRaw != "" {
					endSec, hasEnd = parseMediaTimestamp(endRaw)
				}
				duration, audio, factsErr := transformVideoPortionFacts(ctx, tools.Config, input, source)
				if factsErr != nil {
					return nil, "transform failed", factsErr
				}
				hasAudio = audio
				if startSec >= duration {
					return nil, "transform failed", fmt.Errorf("transform_video's start (%ss) is at or past the end of the attached clip (%ss)", formatPlaybackSpeed(startSec), formatPlaybackSpeed(duration))
				}
				if hasEnd && endSec >= duration {
					endBounded = false
					notices = append(notices, fmt.Sprintf("the requested end (%ss) is at or past the clip's end (%ss), so the speed change was applied through the end of the clip.", formatPlaybackSpeed(endSec), formatPlaybackSpeed(duration)))
				} else {
					endBounded = hasEnd
				}
			}
			if startSec > 0 || endBounded {
				// Geometry ops shape the WHOLE output (they run after the
				// concat), so the portion only rescales the middle's
				// timestamps; the speed op phrase lands after the shape ops.
				geometryCall := call
				geometryCall.Speed = 0
				filters, geometryOps := videoTransformFilters(geometryCall, srcWidth, srcHeight)
				ops = append(ops, geometryOps...)
				ops = append(ops, portionSpeedPhrase(startSec, endSec, endBounded, call.Speed))
				filterComplex := videoPortionSpeedFilterComplex(filters, startSec, endSec, endBounded, hasAudio, call.Speed)
				args = ffmpegPortionSpeedArgs(input, filterComplex, hasAudio, staged)
				notices = append(notices, "the normal-speed head and tail were re-encoded too — the joined segments must share one uniform stream.")
			} else {
				filters, transformOps := videoTransformFilters(call, srcWidth, srcHeight)
				ops = transformOps
				audioFilters := ""
				if call.Speed != 0 {
					// atempo keeps the audio in sync with the setpts-rescaled
					// video; a clip with no audio stream simply ignores -af.
					audioFilters = atempoChain(call.Speed)
				}
				args = ffmpegTransformArgs(input, filters, audioFilters, staged)
			}
			if err := runLocalFFmpeg(ctx, tools.Config, args); err != nil {
				return nil, "transform failed", err
			}
			tempPath, err := promoteStagedOutput(staged, "atelier-video-*", ".mp4")
			if err != nil {
				return nil, "transform failed", err
			}
			output := ToolVideoResult{
				Model:   ffmpegModelName,
				Prompt:  "transform: " + strings.Join(ops, ", "),
				Count:   1,
				Videos:  []ToolVideoFile{{TempPath: tempPath, MimeType: "video/mp4"}},
				Notices: notices,
			}
			return output, fmt.Sprintf("transformed the attached video with ffmpeg (%s)", strings.Join(ops, ", ")), nil
		},
		Activity: ffmpegActivity("transform"),
	}
}

// transformVideoSourceDimensions resolves the source clip's dimensions for an
// aspect-ratio crop: ffprobe when it resolves (any container), then the MP4
// tkhd read off the attached bytes — generation outputs are MP4, so the crop
// works even on a machine where ffprobe went missing.
func transformVideoSourceDimensions(ctx context.Context, config AppConfig, input, sourceDataURL string) (int, int, error) {
	width, height := 0, 0
	if _, ok := resolveLocalFFprobeBinary(config); ok {
		if probe, err := probeStagedMedia(ctx, config, input, "video"); err == nil {
			width, height = probe.Width, probe.Height
		}
	}
	if width <= 0 || height <= 0 {
		if data, _, err := decodeMediaDataURL(sourceDataURL); err == nil {
			if w, h, ok := mp4VideoDimensions(data); ok {
				width, height = int(w), int(h)
			}
		}
	}
	if width <= 0 || height <= 0 {
		return 0, 0, errors.New("transform_video could not read the attached clip's dimensions for the aspect-ratio crop — ffprobe is unavailable and the clip is not a readable MP4")
	}
	return width, height, nil
}

// probeMediaToolDefinition exposes probe_media: ffprobe facts of an attached
// clip as evidence — durations for split planning, codec facts for joins.
func probeMediaToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "probe_media",
		Title:       "Probe media",
		Description: "Use this when the user asks about an attached clip's properties (how long is it, what resolution, what codecs, does it have audio) or when planning cuts and joins needs exact numbers. Runs the locally installed ffprobe on the newest attached video (or the attached audio when no video is attached) and returns duration, dimensions, frame rate, codecs, and container as evidence — flagging non-square-pixel clips that store frames at one size and display them larger (the display size and aspect ratio ride along). No parameters.",
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
		dims := fmt.Sprintf("%dx%d", result.Width, result.Height)
		if result.DisplayWidth > 0 && result.DisplayHeight > 0 {
			dims += fmt.Sprintf(" (displays as %dx%d", result.DisplayWidth, result.DisplayHeight)
			if result.DisplayAspectRatio != "" {
				dims += ", " + result.DisplayAspectRatio
			}
			dims += ")"
		}
		parts = append(parts, dims)
	}
	if result.Rotation != 0 {
		parts = append(parts, fmt.Sprintf("rotation %d°", result.Rotation))
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
			"at":    stringParam(`The timestamp of the frame to capture — seconds ("42", "12.5") or clock ("00:01:30"); "0" is the first frame. May be a comma-separated list to capture several specific frames in one call ("0,9.08,18.17"). Either at or count is required; at wins when both are set.`),
			"count": intParam(fmt.Sprintf(`How many frames to capture at equal intervals across the whole clip (1–%d) — the tool reads the clip's duration itself, so no probe is needed. The batch form for "N screenshots at equal intervals"; omit at. Either at or count is required; at wins when both are set.`, screenshotTimestampsCap)),
		},
		"required": []string{},
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

func transformVideoParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"aspectRatio": stringParam(`Optional — change the shape to a W:H ratio like "1:1", "4:5", "9:16", "16:9"; how the new shape is reached is set by mode. Applied before any resize.`),
			"mode":        stringParam(`Optional — how aspectRatio (or width+height together) is reached: "crop" (the default) trims the longer side from the center; "blur" fills the added canvas with an enlarged, heavily blurred copy of the frame (the standard vertical-video background); "pad" letterboxes with black bars. To have new scene content generated in the added area, use reframe_video.`),
			"width":       intParam("Optional — resize to this pixel width (with height, exactly those pixels via mode's shape rule; alone, aspect-preserving). Must be even."),
			"height":      intParam("Optional — resize to this pixel height (with width, exactly those pixels via mode's shape rule; alone, aspect-preserving). Must be even."),
			"rotate":      intParam(`Optional — rotate clockwise: 90, 180, or 270.`),
			"flip":        stringParam(`Optional — "horizontal" or "vertical".`),
			"speed":       numberParam("Optional — playback-speed multiplier: 3 plays three times as fast, 0.5 at half speed (slow motion). Between 0.25 and 8, not 1. The audio is tempo-adjusted to stay in sync. Alone it applies to the whole clip; with start/end it applies to that portion only."),
			"start":       stringParam(`Optional (requires speed) — portion bound in seconds ("10") or clock ("00:01:30"): the speed change applies from here; before it the clip plays at normal speed. Use split_video to cut a segment out instead.`),
			"end":         stringParam(`Optional (requires speed) — portion bound: the speed change applies until here; after it the clip plays at normal speed.`),
		},
		"required": []string{},
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
