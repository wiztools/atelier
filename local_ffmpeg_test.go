package main

// Tests for the local ffmpeg tools (local_ffmpeg.go): the pure arg builders,
// timestamp/rate parsing, the concat-list writer, tool executions against
// fake ffmpeg/ffprobe shell scripts (the writeFakeWhisper pattern), the
// registry gates, and one full harness turn that persists a split video plus
// a multi-kind (screenshot + extracted audio) turn.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeFFmpegScript fakes the ffmpeg CLI: it appends its args to args.txt next
// to itself, captures the concat demuxer's playlist (the file after "-i" when
// "-f concat" was seen) as concat-list.txt, and writes fake media bytes to the
// last argument — every builder passes the output path last. The payload
// carries JPEG magic so image sniffing (isImageBytes) accepts a screenshot.
const fakeFFmpegScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" >> "$dir/args.txt"
seen_concat=0
prev=""
out=""
for a in "$@"; do
  if [ "$prev" = "concat" ]; then seen_concat=1; fi
  if [ "$seen_concat" = 1 ] && [ "$prev" = "-i" ] && [ -f "$a" ]; then cp "$a" "$dir/concat-list.txt" 2>/dev/null; fi
  prev="$a"
  out="$a"
done
printf '\377\330\377FAKE-MEDIA' > "$out"
`

// fakeFFprobeScript fakes ffprobe with one fixed, well-formed JSON report:
// h264 1920x1080 ~29.97fps + aac in an MP4-family container, 12.5 seconds.
const fakeFFprobeScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"12.500","format_name":"mov,mp4,m4a,3gp,3g2,mj2","bit_rate":"1200000","size":"1875000"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"30000/1001"},{"codec_type":"audio","codec_name":"aac"}]}
JSON
`

// fakeFFprobeDriftScript fakes an ffprobe whose SECOND call reports different
// dimensions — drives the join auto-mode's re-encode branch.
const fakeFFprobeDriftScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -e "$dir/probed" ]; then
cat <<'JSON'
{"format":{"duration":"8.000","format_name":"mov,mp4,m4a,3gp,3g2,mj2"},"streams":[{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"avg_frame_rate":"30000/1001"},{"codec_type":"audio","codec_name":"aac"}]}
JSON
else
  : > "$dir/probed"
cat <<'JSON'
{"format":{"duration":"12.500","format_name":"mov,mp4,m4a,3gp,3g2,mj2"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"30000/1001"},{"codec_type":"audio","codec_name":"aac"}]}
JSON
fi
`

func videoDataURL(payload string) string {
	// A minimal MP4 header (the ftyp box is all isVideoBytes checks) so the
	// fixture survives attachment persistence like a real clip.
	frame := append([]byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, []byte(payload)...)
	return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(frame)
}

// fakeFFmpegArgs reads the args the fake ffmpeg recorded (one per line),
// space-joined for substring assertions.
func fakeFFmpegArgs(t *testing.T, bin string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bin, "args.txt"))
	if err != nil {
		t.Fatalf("the fake ffmpeg never ran: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return strings.Join(lines, " ")
}

// ffmpegTestConfig returns a config whose ffmpeg/ffprobe overrides point at
// fake scripts under a fresh dir, plus the dir (args.txt/concat-list.txt land
// there). The caller decides whether ffprobe is wired at all.
func ffmpegTestConfig(t *testing.T, ffmpegScript, ffprobeScript string) (AppConfig, string) {
	t.Helper()
	withRealLocalLookup(t)
	dir := t.TempDir()
	config := defaultAppConfig()
	config.Providers.Local.FFmpeg.Binary = writeFakeWhisper(t, filepath.Join(dir, "bin"), "ffmpeg", ffmpegScript)
	if ffprobeScript != "" {
		config.Providers.Local.FFprobe.Binary = writeFakeWhisper(t, filepath.Join(dir, "bin"), "ffprobe", ffprobeScript)
	}
	return config, filepath.Join(dir, "bin")
}

// executeFFmpegTool builds a registry + gateway over the config, sets the
// attachment slots, and executes one call.
func executeFFmpegTool(t *testing.T, config AppConfig, attachments HarnessToolExecutionContext, name string, call HarnessToolCall) HarnessToolResult {
	t.Helper()
	registry := defaultHarnessToolRegistry(context.Background(), config, nil)
	if _, ok := registry.Get(name); !ok {
		t.Fatalf("tool %q is not in the registry (gating or wiring bug)", name)
	}
	gateway := newToolGateway(nil, config, registry)
	gateway.tools.AttachedVideos = attachments.AttachedVideos
	gateway.tools.AttachedAudios = attachments.AttachedAudios
	return gateway.Execute(context.Background(), ToolExecutionRequest{Name: name, Call: call})
}

// TestParseMediaTimestamp pins the hybrid timestamp grammar: bare seconds or
// 2–3 segment clock values, never negative or non-numeric.
func TestParseMediaTimestamp(t *testing.T) {
	cases := []struct {
		token  string
		want   float64
		wantOK bool
	}{
		{"42", 42, true},
		{"12.5", 12.5, true},
		{"0", 0, true},
		{"1:30", 90, true},
		{"00:01:30.5", 90.5, true},
		{"", 0, false},
		{"-5", 0, false},
		{"abc", 0, false},
		{"1:2:3:4", 0, false},
		{"1:", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseMediaTimestamp(tc.token)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parseMediaTimestamp(%q) = (%v, %v), want (%v, %v)", tc.token, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestParseFrameRate(t *testing.T) {
	cases := []struct {
		rate string
		want float64
	}{
		{"30000/1001", 30000.0 / 1001.0},
		{"25/1", 25},
		{"30", 30},
		{"0/0", 0},
		{"nonsense", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := parseFrameRate(tc.rate); got != tc.want {
			t.Errorf("parseFrameRate(%q) = %v, want %v", tc.rate, got, tc.want)
		}
	}
}

func TestConcatCompatibility(t *testing.T) {
	base := ToolProbeResult{VideoCodec: "h264", Width: 1920, Height: 1080, FPS: 30000.0 / 1001.0, AudioCodec: "aac"}
	if !concatCompatible(base, base) {
		t.Fatal("identical probes must be concat-compatible")
	}
	differing := []ToolProbeResult{
		{VideoCodec: "vp9", Width: 1920, Height: 1080, FPS: 30, AudioCodec: "aac"},
		{VideoCodec: "h264", Width: 1280, Height: 1080, FPS: 30, AudioCodec: "aac"},
		{VideoCodec: "h264", Width: 1920, Height: 1080, FPS: 24, AudioCodec: "aac"},
		{VideoCodec: "h264", Width: 1920, Height: 1080, FPS: 30, AudioCodec: "opus"},
		{VideoCodec: "", Width: 1920, Height: 1080, FPS: 30, AudioCodec: "aac"},
	}
	for _, other := range differing {
		if concatCompatible(base, other) {
			t.Errorf("probe %+v must not be concat-compatible with %+v", other, base)
		}
	}
	if !mp4Family("mov,mp4,m4a,3gp,3g2,mj2") || mp4Family("webm") {
		t.Fatal("mp4Family must match the mp4/mov family and reject webm")
	}
}

// TestConcatListFileContents pins the playlist writer: one line per clip in
// sequence order, single quotes escaped.
func TestConcatListFileContents(t *testing.T) {
	got := concatListFileContents([]string{"/tmp/a.mp4", "/tmp/it's.mov", "/tmp/c.webm"})
	want := "file '/tmp/a.mp4'\nfile '/tmp/it'\\''s.mov'\nfile '/tmp/c.webm'\n"
	if got != want {
		t.Fatalf("concatListFileContents = %q, want %q", got, want)
	}
}

// TestFFmpegArgBuilders pins every builder's exact argument shape.
func TestFFmpegArgBuilders(t *testing.T) {
	if got := ffmpegScreenshotArgs("in.mp4", "4.5", "out.jpg"); strings.Join(got, " ") != "-ss 4.5 -i in.mp4 -frames:v 1 -q:v 2 out.jpg" {
		t.Errorf("screenshot args = %v", got)
	}
	cases := []struct {
		name string
		got  []string
		want string
	}{
		{"split accurate full", ffmpegSplitArgs("in.mp4", "10", 15, false, "out.mp4"),
			"-ss 10 -i in.mp4 -t 15 -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"split accurate to end", ffmpegSplitArgs("in.mp4", "", 0, false, "out.mp4"),
			"-i in.mp4 -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"split fast segment", ffmpegSplitArgs("in.mp4", "00:01:30", 5.5, true, "out.mp4"),
			"-ss 00:01:30 -i in.mp4 -t 5.5 -c copy -avoid_negative_ts make_zero out.mp4"},
		{"join copy", ffmpegJoinArgs("list.txt", true, "out.mp4"),
			"-f concat -safe 0 -i list.txt -c copy -movflags +faststart out.mp4"},
		{"join reencode", ffmpegJoinArgs("list.txt", false, "out.mp4"),
			"-f concat -safe 0 -i list.txt -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"extract copy", ffmpegExtractAudioArgs("in.mp4", "out.m4a", true),
			"-i in.mp4 -vn -c:a copy out.m4a"},
		{"extract convert", ffmpegExtractAudioArgs("in.mp4", "out.mp3", false),
			"-i in.mp4 -vn -c:a libmp3lame -b:a 192k out.mp3"},
		{"replace copy video", ffmpegReplaceAudioArgs("v.mp4", "a.mp3", true, "out.mp4"),
			"-i v.mp4 -i a.mp3 -map 0:v:0 -map 1:a:0 -c:v copy -c:a aac -b:a 192k -shortest -movflags +faststart out.mp4"},
		{"replace reencode video", ffmpegReplaceAudioArgs("v.webm", "a.mp3", false, "out.mp4"),
			"-i v.webm -i a.mp3 -map 0:v:0 -map 1:a:0 -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -shortest -movflags +faststart out.mp4"},
	}
	for _, tc := range cases {
		if joined := strings.Join(tc.got, " "); joined != tc.want {
			t.Errorf("%s args =\n%q\nwant\n%q", tc.name, joined, tc.want)
		}
	}
}

// TestFFmpegToolValidation pins the per-tool Validate rules the planner sees
// as correction messages.
func TestFFmpegToolValidation(t *testing.T) {
	screenshot := screenshotVideoToolDefinition()
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], ".at is required") {
		t.Errorf("screenshot without at = %v, want the .at required error", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "banana"}); len(errors) == 0 || !strings.Contains(errors[0], ".at must be a timestamp") {
		t.Errorf("screenshot with a bad at = %v", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "00:01:30"}); len(errors) != 0 {
		t.Errorf("screenshot with a clock at = %v, want none", errors)
	}

	split := splitVideoToolDefinition()
	if errors := split.Validate("toolCalls[0]", HarnessToolCall{Mode: "quick"}); len(errors) == 0 || !strings.Contains(errors[0], `.mode must be "fast" or "accurate"`) {
		t.Errorf("split with a bad mode = %v", errors)
	}
	if errors := split.Validate("toolCalls[0]", HarnessToolCall{Start: "10", End: "5"}); len(errors) == 0 || !strings.Contains(errors[0], ".end must be after start") {
		t.Errorf("split with end before start = %v", errors)
	}
	if errors := split.Validate("toolCalls[0]", HarnessToolCall{Start: "10", End: "25", Mode: "fast"}); len(errors) != 0 {
		t.Errorf("valid split = %v, want none", errors)
	}

	join := joinVideosToolDefinition()
	if errors := join.Validate("toolCalls[0]", HarnessToolCall{Mode: "turbo"}); len(errors) == 0 || !strings.Contains(errors[0], `.mode must be "auto"`) {
		t.Errorf("join with a bad mode = %v", errors)
	}
	if errors := join.Validate("toolCalls[0]", HarnessToolCall{Mode: "copy"}); len(errors) != 0 {
		t.Errorf("join copy mode = %v, want none", errors)
	}
}

func TestFFmpegScreenshotExecutes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{At: "4.5"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok || typed.Count != 1 || len(typed.Images) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	if !strings.HasPrefix(typed.Images[0], "data:image/jpeg;base64,") {
		t.Fatalf("image payload = %q", typed.Images[0][:40])
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-ss 4.5") {
		t.Fatalf("ffmpeg args = %q, want the -ss seek", args)
	}
}

func TestFFmpegScreenshotRequiresVideo(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{}, "screenshot_video", HarnessToolCall{At: "1"})
	if result.Status == "completed" || !strings.Contains(result.Error, "requires an attached video clip") {
		t.Fatalf("result = %+v, want the attachment error", result)
	}
}

func TestFFmpegSplitExecutes(t *testing.T) {
	t.Run("accurate default", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "split_video", HarnessToolCall{Start: "10", End: "25"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, ok := result.Result.(ToolVideoResult)
		if !ok || typed.Model != ffmpegModelName || len(typed.Videos) != 1 {
			t.Fatalf("result payload = %+v", result.Result)
		}
		defer os.Remove(typed.Videos[0].TempPath)
		if typed.Videos[0].MimeType != "video/mp4" {
			t.Fatalf("mimeType = %q", typed.Videos[0].MimeType)
		}
		data, err := os.ReadFile(typed.Videos[0].TempPath)
		if err != nil || !strings.HasPrefix(string(data), "\xff\xd8\xffFAKE-MEDIA") {
			t.Fatalf("output file = %q (%v), want the fake ffmpeg payload", data, err)
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-t 15 ") || !strings.Contains(args, "libx264") {
			t.Fatalf("split args = %q, want -t 15 and a re-encode", args)
		}
	})
	t.Run("fast mode notices the keyframe caveat", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "split_video", HarnessToolCall{Start: "10", End: "25", Mode: "fast"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "keyframes") {
			t.Fatalf("notices = %v, want the keyframe caveat", result.Notices)
		}
	})
}

// TestFFmpegJoinHonorsAttachmentOrder pins the sequence contract: the concat
// playlist lists the clips in attachment order, and a forced copy skips
// probing entirely.
func TestFFmpegJoinHonorsAttachmentOrder(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE"), videoDataURL("CLIP-TWO")},
	}, "join_videos", HarnessToolCall{Mode: "copy"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed := result.Result.(ToolVideoResult)
	defer os.Remove(typed.Videos[0].TempPath)
	list, err := os.ReadFile(filepath.Join(bin, "concat-list.txt"))
	if err != nil {
		t.Fatalf("the fake ffmpeg never captured a concat list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(list)), "\n")
	if len(lines) != 2 {
		t.Fatalf("concat list = %q, want two lines", list)
	}
	if !strings.Contains(lines[0], "input-001.mp4") || !strings.Contains(lines[1], "input-002.mp4") {
		t.Fatalf("concat order = %q, want attachment order (001 before 002)", list)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-c copy") || strings.Contains(args, "libx264") {
		t.Fatalf("join args = %q, want a forced stream copy", args)
	}
}

func TestFFmpegJoinAutoMode(t *testing.T) {
	t.Run("matching clips stream-copy", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE"), videoDataURL("CLIP-TWO")},
		}, "join_videos", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-c copy") {
			t.Fatalf("auto join args = %q, want a stream copy for matching clips", args)
		}
		if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "stream copy") {
			t.Fatalf("notices = %v, want the copy explanation", result.Notices)
		}
	})
	t.Run("drifting clips re-encode", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeDriftScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE"), videoDataURL("CLIP-TWO")},
		}, "join_videos", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "re-encoded") {
			t.Fatalf("notices = %v, want the re-encode explanation", result.Notices)
		}
	})
	t.Run("needs at least two clips", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("ONLY-ONE")},
		}, "join_videos", HarnessToolCall{})
		if result.Status == "completed" || !strings.Contains(result.Error, "at least two") {
			t.Fatalf("result = %+v, want the two-clip error", result)
		}
	})
}

func TestFFmpegExtractAudioExecutes(t *testing.T) {
	t.Run("aac copies into m4a", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "extract_audio", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, ok := result.Result.(ToolAudioResult)
		if !ok || len(typed.Audios) != 1 {
			t.Fatalf("result payload = %+v", result.Result)
		}
		defer os.Remove(typed.Audios[0].TempPath)
		if !strings.HasSuffix(typed.Audios[0].TempPath, ".m4a") || typed.Audios[0].MimeType != "audio/mp4" {
			t.Fatalf("audio file = %+v, want a copied .m4a", typed.Audios[0])
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-c:a copy") {
			t.Fatalf("extract args = %q, want a stream copy for AAC", args)
		}
		if len(result.Notices) != 0 {
			t.Fatalf("a copy should carry no conversion notice, got %v", result.Notices)
		}
	})
	t.Run("without ffprobe converts to mp3 with a notice", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, "")
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "extract_audio", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolAudioResult)
		defer os.Remove(typed.Audios[0].TempPath)
		if !strings.HasSuffix(typed.Audios[0].TempPath, ".mp3") || typed.Audios[0].MimeType != "audio/mpeg" {
			t.Fatalf("audio file = %+v, want a converted .mp3", typed.Audios[0])
		}
		if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "converted to MP3") {
			t.Fatalf("notices = %v, want the conversion explanation", result.Notices)
		}
	})
}

func TestFFmpegReplaceAudioExecutes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		AttachedAudios: []string{wavDataURL()},
	}, "replace_audio", HarnessToolCall{})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolVideoResult)
	if !ok || len(typed.Videos) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	defer os.Remove(typed.Videos[0].TempPath)
	args := fakeFFmpegArgs(t, bin)
	for _, want := range []string{"-map 0:v:0", "-map 1:a:0", "-c:v copy", "-shortest"} {
		if !strings.Contains(args, want) {
			t.Fatalf("replace args = %q, want %q", args, want)
		}
	}
}

func TestFFmpegReplaceAudioRequiresBothInputs(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "replace_audio", HarnessToolCall{})
	if result.Status == "completed" || !strings.Contains(result.Error, "attached audio clip") {
		t.Fatalf("result = %+v, want the audio attachment error", result)
	}
}

func TestFFmpegProbeMediaExecutes(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "probe_media", HarnessToolCall{})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolProbeResult)
	if !ok {
		t.Fatalf("result payload = %+v", result.Result)
	}
	if typed.Duration != 12.5 || typed.Width != 1920 || typed.Height != 1080 || typed.VideoCodec != "h264" || typed.AudioCodec != "aac" {
		t.Fatalf("probe = %+v", typed)
	}
	if absFloat(typed.FPS-30000.0/1001.0) > 0.001 {
		t.Fatalf("fps = %v", typed.FPS)
	}
	if !strings.Contains(result.Summary, "12.5") || !strings.Contains(result.Summary, "1920x1080") {
		t.Fatalf("summary = %q", result.Summary)
	}
}

// TestFFmpegRegistryGating pins the tool gates: nothing without a binary, the
// five transform tools with ffmpeg alone, plus probe_media only when ffprobe
// also resolves.
func TestFFmpegRegistryGating(t *testing.T) {
	newRegistryNames := func(t *testing.T, found map[string]string) []string {
		t.Helper()
		stubLocalLookup(t, found)
		return defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil).Names()
	}
	if names := newRegistryNames(t, map[string]string{}); containsString(names, "split_video") {
		t.Fatal("ffmpeg tools must be absent without a detected binary")
	}
	names := newRegistryNames(t, map[string]string{"ffmpeg": "/opt/test/bin/ffmpeg"})
	for _, want := range []string{"screenshot_video", "split_video", "join_videos", "extract_audio", "replace_audio"} {
		if !containsString(names, want) {
			t.Errorf("registry with ffmpeg = %v, want %q", names, want)
		}
	}
	if containsString(names, "probe_media") {
		t.Error("probe_media must stay hidden without ffprobe")
	}
	names = newRegistryNames(t, map[string]string{"ffmpeg": "/opt/test/bin/ffmpeg", "ffprobe": "/opt/test/bin/ffprobe"})
	if !containsString(names, "probe_media") {
		t.Errorf("registry with ffmpeg+ffprobe = %v, want probe_media", names)
	}
}

// TestFFmpegSiblingFFprobeResolution pins the ffmpeg-directory fallback: with
// ffprobe nowhere on PATH and no override, a sibling of the resolved ffmpeg
// binary resolves.
func TestFFmpegSiblingFFprobeResolution(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := writeFakeWhisper(t, dir, "ffmpeg", fakeFFmpegScript)
	sibling := writeFakeWhisper(t, dir, "ffprobe", fakeFFprobeScript)
	// "ffmpeg" resolves from the table; the sibling lookup passes ffprobe's
	// absolute path through the same stubbed LookPath.
	stubLocalLookup(t, map[string]string{"ffmpeg": ffmpegPath, sibling: sibling})
	config := defaultAppConfig()
	resolved, ok := resolveLocalFFprobeBinary(config)
	if !ok || resolved.path != sibling {
		t.Fatalf("resolved = %+v (ok %v), want the ffmpeg sibling", resolved, ok)
	}
}

// TestHarnessToolPlanSchemaHasFFmpegParams pins the format-schema planner
// path: the grammar must be able to emit the ffmpeg tools' params.
func TestHarnessToolPlanSchemaHasFFmpegParams(t *testing.T) {
	schema := harnessToolPlanSchema(filesystemToolRegistry())
	items := schema["properties"].(map[string]any)["toolCalls"].(map[string]any)["items"].(map[string]any)
	properties := items["properties"].(map[string]any)
	for _, param := range []string{"at", "start", "end", "mode"} {
		if _, ok := properties[param].(map[string]any); !ok {
			t.Errorf("plan schema properties missing %q: %+v", param, properties)
		}
	}
}

func TestApplyKwargsFFmpegParams(t *testing.T) {
	var call HarnessToolCall
	applyKwargs(&call, "at='4.5', start='10', end='25', mode='fast'")
	if call.At != "4.5" || call.Start != "10" || call.End != "25" || call.Mode != "fast" {
		t.Fatalf("applyKwargs = %+v", call)
	}
}

// TestHarnessFFmpegTurnPersistsMedia drives a full turn whose plan calls
// screenshot_video AND extract_audio on one attached clip — the multi-kind
// combination the local tools make reachable — and pins that both artifacts
// persist on the single saved turn.
func TestHarnessFFmpegTurnPersistsMedia(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withRealLocalLookup(t)

	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	var finalBodies []string
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				switch payload["model"] {
				case "harness-model":
					harnessCalls++
					switch harnessCalls {
					case 1: // triage
						return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"text","toolTask":"Screenshot the clip and extract its audio.","reason":"The user asked for a frame and the audio track."}`), nil
					case 2: // plan
						return chatCompletion("harness-model", `{"brief":"Screenshot at 4.5s and extract the audio track.","needsTools":true,"reason":"both transforms","toolCalls":[{"name":"screenshot_video","at":"4.5"},{"name":"extract_audio"}]}`), nil
					case 3:
						return chatCompletion("harness-model", `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`), nil
					}
					t.Fatalf("unexpected harness call #%d", harnessCalls)
					return nil, nil
				default:
					return chatCompletion("chat-box-model", `"Frame and audio"`), nil
				}
			}
			encoded, _ := json.Marshal(payload)
			finalBodies = append(finalBodies, string(encoded))
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here are the frame and the audio."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}},
			}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-ffmpeg-turn", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Grab the frame at 4.5 seconds and give me the audio track.",
			Videos:  []string{videoDataURL("CLIP-ONE")},
		}},
	})

	if len(finalBodies) == 0 {
		t.Fatal("the final model was never called")
	}

	conversations, err := listConversations(config.Storage)
	if err != nil || len(conversations) != 1 {
		t.Fatalf("listConversations = %v (%d conversations)", err, len(conversations))
	}
	loaded, err := newHistoryStore(config.Storage).loadForAppend(conversations[0].ID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		t.Fatalf("loadForAppend: %v", err)
	}
	turnData, err := os.ReadFile(filepath.Join(loaded.TurnsDir, "turn_000002.json"))
	if err != nil {
		t.Fatalf("ReadFile assistant turn: %v", err)
	}
	var savedTurn HistoryTurn
	if err := json.Unmarshal(turnData, &savedTurn); err != nil {
		t.Fatalf("Unmarshal turn: %v", err)
	}
	var imagePath, audioPath string
	for _, content := range savedTurn.Content {
		switch content.Type {
		case "image":
			imagePath = content.Path
		case "audio":
			audioPath = content.Path
		}
	}
	if !strings.Contains(imagePath, "img_") {
		t.Fatalf("saved turn has no image artifact: %+v", savedTurn.Content)
	}
	if !strings.Contains(audioPath, "aud_") || !strings.HasSuffix(audioPath, ".m4a") {
		t.Fatalf("saved turn has no audio artifact: %+v", savedTurn.Content)
	}
	if _, err := os.Stat(filepath.Join(loaded.ArtifactsDir, filepath.Base(imagePath))); err != nil {
		t.Fatalf("image artifact unreadable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(loaded.ArtifactsDir, filepath.Base(audioPath))); err != nil {
		t.Fatalf("audio artifact unreadable: %v", err)
	}
	joined := strings.Join(finalBodies, "\n")
	if strings.Contains(joined, "tempPath") {
		t.Fatal("a media temp path leaked into model evidence")
	}
}

// TestDecodeTriageMediaEdit pins the advisory decode: a real boolean is read,
// an absent field stays false, and a mis-typed value is dropped without
// sinking the routing decision.
func TestDecodeTriageMediaEdit(t *testing.T) {
	decision, err := decodeTriageDecision(`{"needsTools":false,"responseMode":"text","toolTask":"","reason":"edit","mediaEdit":true}`)
	if err != nil || !decision.MediaEdit {
		t.Fatalf("decision = %+v (err %v), want mediaEdit true", decision, err)
	}
	decision, err = decodeTriageDecision(`{"needsTools":false,"responseMode":"text","toolTask":"","reason":"no edit"}`)
	if err != nil || decision.MediaEdit {
		t.Fatalf("decision = %+v (err %v), want mediaEdit false when absent", decision, err)
	}
	decision, err = decodeTriageDecision(`{"needsTools":false,"responseMode":"text","toolTask":"","reason":"edit","mediaEdit":"true"}`)
	if err != nil || decision.MediaEdit {
		t.Fatalf("decision = %+v (err %v), a mis-typed mediaEdit must stay false", decision, err)
	}
}

func TestMediaEditFallbackNotice(t *testing.T) {
	if got := mediaEditFallbackNotice(false, "I cannot edit videos."); got != "" {
		t.Fatalf("notice with flag off = %q, want none", got)
	}
	if got := mediaEditFallbackNotice(true, "Install FFmpeg with brew first."); got != "" {
		t.Fatalf("notice when the answer covers ffmpeg = %q, want none", got)
	}
	got := mediaEditFallbackNotice(true, "I cannot do that right now.")
	if !strings.Contains(got, "brew install ffmpeg") {
		t.Fatalf("notice = %q, want the install remedy", got)
	}
}

// mediaEditTurnConfig builds the mocked-Ollama harness used by the mediaEdit
// notice tests: one triage call answering triageJSON, one streamed final
// response (finalText), and title generation. Returns the captured final
// request bodies.
func mediaEditTurnConfig(t *testing.T, config AppConfig, triageJSON, finalText string) ([]string, error) {
	t.Helper()
	var finalBodies []string
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				switch payload["model"] {
				case "harness-model":
					harnessCalls++
					if harnessCalls == 1 {
						return chatCompletion("harness-model", triageJSON), nil
					}
					t.Fatalf("unexpected harness call #%d (needsTools false must skip planning)", harnessCalls)
					return nil, nil
				default:
					return chatCompletion("chat-box-model", `"Edited"`), nil
				}
			}
			encoded, _ := json.Marshal(payload)
			finalBodies = append(finalBodies, string(encoded))
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":`+strconv.Quote(finalText)+`},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}},
			}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})
	app.runChatStream(context.Background(), "request-media-edit", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Cut this clip from 5 to 10 seconds.",
			Videos:  []string{videoDataURL("CLIP-ONE")},
		}},
	})
	return finalBodies, nil
}

// savedAssistantContent reads the assistant turn's text content for the first
// conversation under the config's storage.
func savedAssistantContent(t *testing.T, config AppConfig) string {
	t.Helper()
	conversations, err := listConversations(config.Storage)
	if err != nil || len(conversations) != 1 {
		t.Fatalf("listConversations = %v (%d conversations)", err, len(conversations))
	}
	loaded, err := newHistoryStore(config.Storage).loadForAppend(conversations[0].ID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		t.Fatalf("loadForAppend: %v", err)
	}
	turnData, err := os.ReadFile(filepath.Join(loaded.TurnsDir, "turn_000002.json"))
	if err != nil {
		t.Fatalf("ReadFile assistant turn: %v", err)
	}
	var savedTurn HistoryTurn
	if err := json.Unmarshal(turnData, &savedTurn); err != nil {
		t.Fatalf("Unmarshal turn: %v", err)
	}
	var text string
	for _, content := range savedTurn.Content {
		if content.Type == "text" {
			text = content.Text
		}
	}
	return text
}

// TestHarnessMediaEditWithoutFFmpegNotifies pins the first-run experience: a
// triage-flagged video edit with no ffmpeg CLI reaches the final model with
// the install note in its messages, and the user-visible reply carries the
// deterministic fallback when the model's own prose didn't mention ffmpeg.
func TestHarnessMediaEditWithoutFFmpegNotifies(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	// Each subtest gets its own HOME (and so its own history) via newConfig —
	// with no withRealLocalLookup, TestMain's pin keeps ffmpeg undetected, the
	// exact first-run-without-ffmpeg state.
	newConfig := func(t *testing.T) AppConfig {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		config := defaultAppConfig()
		config.Storage = ConfigStorage{
			Root:      filepath.Join(home, ".atelier"),
			History:   filepath.Join(home, ".atelier", "history"),
			Artifacts: filepath.Join(home, ".atelier", "history"),
		}
		config.Providers.Ollama.BaseURL = "http://ollama.test"
		config.Providers.Ollama.Models.Primary = "chat-box-model"
		config.Providers.Ollama.Models.Harness = "harness-model"
		return config
	}
	const triageJSON = `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"local video edit, no ffmpeg installed","mediaEdit":true}`

	t.Run("note reaches the model and the fallback reaches the reply", func(t *testing.T) {
		config := newConfig(t)
		if err := writeAppConfig(config); err != nil {
			t.Fatalf("writeAppConfig: %v", err)
		}
		finalBodies, _ := mediaEditTurnConfig(t, config, triageJSON, "I can't cut clips on this machine yet.")
		if len(finalBodies) == 0 {
			t.Fatal("the final model was never called")
		}
		joined := strings.Join(finalBodies, "\n")
		if !strings.Contains(joined, "brew install ffmpeg") {
			t.Fatal("the install note never reached the final model's messages")
		}
		if reply := savedAssistantContent(t, config); !strings.Contains(reply, "brew install ffmpeg") {
			t.Fatalf("reply = %q, want the deterministic install fallback", reply)
		}
	})
	t.Run("a model answer that names ffmpeg suppresses the fallback", func(t *testing.T) {
		config := newConfig(t)
		if err := writeAppConfig(config); err != nil {
			t.Fatalf("writeAppConfig: %v", err)
		}
		finalBodies, _ := mediaEditTurnConfig(t, config, triageJSON, "You need ffmpeg installed for that — brew install ffmpeg once.")
		joined := strings.Join(finalBodies, "\n")
		if !strings.Contains(joined, "brew install ffmpeg") {
			t.Fatal("the install note never reached the final model's messages")
		}
		if reply := savedAssistantContent(t, config); strings.Contains(reply, "⚠️") {
			t.Fatalf("reply = %q, no fallback blockquote is needed when the model covered it", reply)
		}
	})
	t.Run("no note once ffmpeg is configured", func(t *testing.T) {
		config := newConfig(t)
		// Restore the real lookup so the override below actually resolves —
		// TestMain's pin would otherwise keep ffmpeg undetected here too.
		withRealLocalLookup(t)
		config.Providers.Local.FFmpeg.Binary = writeFakeWhisper(t, t.TempDir(), "ffmpeg", fakeFFmpegScript)
		if err := writeAppConfig(config); err != nil {
			t.Fatalf("writeAppConfig: %v", err)
		}
		finalBodies, _ := mediaEditTurnConfig(t, config, triageJSON, "I can't cut clips on this machine yet.")
		joined := strings.Join(finalBodies, "\n")
		if strings.Contains(joined, "Atelier note:") || strings.Contains(joined, "no ffmpeg CLI was detected") {
			t.Fatal("the install note must not appear when ffmpeg is configured")
		}
		if reply := savedAssistantContent(t, config); strings.Contains(reply, "⚠️") {
			t.Fatalf("reply = %q, no fallback is warranted with ffmpeg configured", reply)
		}
	})
}
