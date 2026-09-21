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
	"sort"
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

// fakeFFprobeNoAudioScript fakes ffprobe over a silent clip — h264 1920x1080
// with no audio stream, 12.5 seconds — driving the portion-speed path's
// audio-less filter_complex.
const fakeFFprobeNoAudioScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"12.500","format_name":"mov,mp4,m4a,3gp,3g2,mj2","bit_rate":"1200000","size":"1875000"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"30000/1001"}]}
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

// fakeFFprobeAnamorphicScript fakes ffprobe over the squeezed-source movie:
// frames stored 960x720 with 4:3 sample aspect, so players stretch them to a
// 1280x720 (16:9) display — the screenshot tool must resample its captures.
const fakeFFprobeAnamorphicScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"12.500","format_name":"mov,mp4,m4a,3gp,3g2,mj2","bit_rate":"1200000","size":"1875000"},"streams":[{"codec_type":"video","codec_name":"h264","width":960,"height":720,"avg_frame_rate":"30000/1001","sample_aspect_ratio":"4:3","display_aspect_ratio":"16:9"},{"codec_type":"audio","codec_name":"aac"}]}
JSON
`

// fakeFFprobeUnknownSARScript fakes ffprobe over a clip whose sample aspect
// is unknown (0:1) — ffmpeg's marker for "no aspect metadata". Unknown is
// treated as square pixels: no resample, no notice.
const fakeFFprobeUnknownSARScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"12.500","format_name":"mov,mp4,m4a,3gp,3g2,mj2"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"30000/1001","sample_aspect_ratio":"0:1","display_aspect_ratio":"0:1"}]}
JSON
`

// fakeFFprobeRotatedAnamorphicScript fakes ffprobe over an anamorphic clip
// with a 90° display-matrix rotation: stored 960x720 at 4:3 sample aspect,
// played sideways — display becomes 720x1280 after the rotation swaps the
// axes.
const fakeFFprobeRotatedAnamorphicScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"12.500","format_name":"mov,mp4,m4a,3gp,3g2,mj2"},"streams":[{"codec_type":"video","codec_name":"h264","width":960,"height":720,"avg_frame_rate":"30000/1001","sample_aspect_ratio":"4:3","display_aspect_ratio":"16:9","side_data_list":[{"side_data_type":"Display Matrix","rotation":-90}]}]}
JSON
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
	cases := []struct {
		name string
		got  []string
		want string
	}{
		{"screenshot square pixels", ffmpegScreenshotArgs("in.mp4", "4.5", "out.jpg", 0, 0),
			"-ss 4.5 -i in.mp4 -frames:v 1 -q:v 2 out.jpg"},
		{"screenshot resamples anamorphic", ffmpegScreenshotArgs("in.mp4", "4.5", "out.jpg", 1280, 720),
			"-ss 4.5 -i in.mp4 -vf scale=1280:720,setsar=1 -frames:v 1 -q:v 2 out.jpg"},
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
		{"transform", ffmpegTransformArgs("in.mp4", "crop=864:1080", "", "out.mp4"),
			"-i in.mp4 -vf crop=864:1080 -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"transform with speed", ffmpegTransformArgs("in.mp4", "setpts=PTS/3", "atempo=2.0,atempo=1.5", "out.mp4"),
			"-i in.mp4 -vf setpts=PTS/3 -af atempo=2.0,atempo=1.5 -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"transform speed only omits -vf", ffmpegTransformArgs("in.mp4", "", "atempo=0.5", "out.mp4"),
			"-i in.mp4 -af atempo=0.5 -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"portion speed with audio", ffmpegPortionSpeedArgs("in.mp4", "FC", true, "out.mp4"),
			"-i in.mp4 -filter_complex FC -map [vout] -map [acat] -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"portion speed without audio", ffmpegPortionSpeedArgs("in.mp4", "FC", false, "out.mp4"),
			"-i in.mp4 -filter_complex FC -map [vout] -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
	}
	for _, tc := range cases {
		if joined := strings.Join(tc.got, " "); joined != tc.want {
			t.Errorf("%s args =\n%q\nwant\n%q", tc.name, joined, tc.want)
		}
	}
}

// TestVideoTransformFilters pins the -vf chain resolution: op order
// (aspect crop → resize → rotate → flip), the cover idiom for exact
// width+height, the -2 aspect-preserving single-dim scales, and the
// even-dimension floor on computed crop rects.
func TestVideoTransformFilters(t *testing.T) {
	cases := []struct {
		name       string
		call       HarnessToolCall
		srcWidth   int
		srcHeight  int
		wantFilter string
		wantOps    string
	}{
		{"exact size cover-crops", HarnessToolCall{Width: 1280, Height: 720}, 1344, 768,
			"scale=1280:720:force_original_aspect_ratio=increase,crop=1280:720",
			"resized to 1280x720"},
		{"aspect crop only", HarnessToolCall{AspectRatio: "4:5"}, 1920, 1080,
			"crop=864:1080",
			"cropped to 4:5 (center 864x1080)"},
		{"aspect plus exact size", HarnessToolCall{AspectRatio: "4:5", Width: 1080, Height: 1350}, 1920, 1080,
			"crop=864:1080,scale=1080:1350",
			"cropped to 4:5 (center 864x1080), resized to 1080x1350"},
		{"aspect plus width", HarnessToolCall{AspectRatio: "16:9", Width: 1280}, 1920, 1080,
			"crop=1920:1080,scale=1280:-2",
			"cropped to 16:9 (center 1920x1080), resized to 1280 pixels wide"},
		{"width only", HarnessToolCall{Width: 640}, 0, 0,
			"scale=640:-2", "resized to 640 pixels wide"},
		{"height only", HarnessToolCall{Height: 720}, 0, 0,
			"scale=-2:720", "resized to 720 pixels tall"},
		{"rotate 90", HarnessToolCall{Rotate: 90}, 0, 0,
			"transpose=1", "rotated 90° clockwise"},
		{"rotate 180", HarnessToolCall{Rotate: 180}, 0, 0,
			"transpose=1,transpose=1", "rotated 180°"},
		{"rotate 270", HarnessToolCall{Rotate: 270}, 0, 0,
			"transpose=2", "rotated 270° clockwise"},
		{"flip horizontal", HarnessToolCall{Flip: "horizontal"}, 0, 0,
			"hflip", "flipped horizontal"},
		{"flip vertical", HarnessToolCall{Flip: "vertical"}, 0, 0,
			"vflip", "flipped vertical"},
		{"odd crop floors to even", HarnessToolCall{AspectRatio: "1:1"}, 1000, 333,
			"crop=332:332", "cropped to 1:1 (center 332x332)"},
		{"speed up only", HarnessToolCall{Speed: 3}, 0, 0,
			"setpts=PTS/3", "3x playback speed"},
		{"slow motion only", HarnessToolCall{Speed: 0.5}, 0, 0,
			"setpts=PTS/0.5", "0.5x playback speed"},
		{"fractional speed", HarnessToolCall{Speed: 1.75}, 0, 0,
			"setpts=PTS/1.75", "1.75x playback speed"},
		{"everything combined", HarnessToolCall{AspectRatio: "16:9", Width: 1280, Height: 720, Rotate: 90, Flip: "vertical", Speed: 2}, 1344, 768,
			"crop=1344:756,scale=1280:720,transpose=1,vflip,setpts=PTS/2",
			"cropped to 16:9 (center 1344x756), resized to 1280x720, rotated 90° clockwise, flipped vertical, 2x playback speed"},
	}
	for _, tc := range cases {
		filter, ops := videoTransformFilters(tc.call, tc.srcWidth, tc.srcHeight)
		if filter != tc.wantFilter {
			t.Errorf("%s filter = %q, want %q", tc.name, filter, tc.wantFilter)
		}
		if joined := strings.Join(ops, ", "); joined != tc.wantOps {
			t.Errorf("%s ops = %q, want %q", tc.name, joined, tc.wantOps)
		}
	}
}

// TestAtempoChain pins the tempo decomposition: one instance inside
// [0.5, 2.0] when the multiplier already fits, chained 2.0/0.5 stages beyond.
func TestAtempoChain(t *testing.T) {
	cases := []struct {
		speed float64
		want  string
	}{
		{3, "atempo=2.0,atempo=1.5"},
		{2, "atempo=2"},
		{1.75, "atempo=1.75"},
		{0.5, "atempo=0.5"},
		{0.25, "atempo=0.5,atempo=0.5"},
		{0.3, "atempo=0.5,atempo=0.6"},
		{8, "atempo=2.0,atempo=2.0,atempo=2"},
	}
	for _, tc := range cases {
		if got := atempoChain(tc.speed); got != tc.want {
			t.Errorf("atempoChain(%v) = %q, want %q", tc.speed, got, tc.want)
		}
	}
}

// TestVideoPortionSpeedFilterComplex pins the portion-speed graph: head and
// tail trims at normal speed around a rescaled middle, per-segment timestamp
// resets, the audio chains only when the clip carries audio, and geometry
// applied to the joined output.
func TestVideoPortionSpeedFilterComplex(t *testing.T) {
	cases := []struct {
		name       string
		geometry   string
		start      float64
		end        float64
		endBounded bool
		hasAudio   bool
		speed      float64
		want       string
	}{
		{"3x middle with audio", "", 10, 20, true, true, 3,
			"[0:v]trim=end=10,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=10,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=10:end=20,setpts=(PTS-STARTPTS)/3[v1];[0:a]atrim=start=10:end=20,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1];" +
				"[0:v]trim=start=20,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=20,asetpts=PTS-STARTPTS[a2];" +
				"[v0][a0][v1][a1][v2][a2]concat=n=3:v=1:a=1[vout][acat]"},
		{"2x middle without audio", "", 4, 8, true, false, 2,
			"[0:v]trim=end=4,setpts=PTS-STARTPTS[v0];" +
				"[0:v]trim=start=4:end=8,setpts=(PTS-STARTPTS)/2[v1];" +
				"[0:v]trim=start=8,setpts=PTS-STARTPTS[v2];" +
				"[v0][v1][v2]concat=n=3:v=1:a=0[vout]"},
		{"half-speed opening, no head", "", 0, 4.5, true, true, 0.5,
			"[0:v]trim=start=0:end=4.5,setpts=(PTS-STARTPTS)/0.5[v1];[0:a]atrim=start=0:end=4.5,asetpts=(PTS-STARTPTS)/0.5,atempo=0.5[a1];" +
				"[0:v]trim=start=4.5,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=4.5,asetpts=PTS-STARTPTS[a2];" +
				"[v1][a1][v2][a2]concat=n=2:v=1:a=1[vout][acat]"},
		{"3x to the end, no tail", "", 10, 0, false, true, 3,
			"[0:v]trim=end=10,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=10,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=10,setpts=(PTS-STARTPTS)/3[v1];[0:a]atrim=start=10,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1];" +
				"[v0][a0][v1][a1]concat=n=2:v=1:a=1[vout][acat]"},
		{"geometry covers the joined output", "crop=864:1080", 10, 20, true, true, 3,
			"[0:v]trim=end=10,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=10,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=10:end=20,setpts=(PTS-STARTPTS)/3[v1];[0:a]atrim=start=10:end=20,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1];" +
				"[0:v]trim=start=20,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=20,asetpts=PTS-STARTPTS[a2];" +
				"[v0][a0][v1][a1][v2][a2]concat=n=3:v=1:a=1[vcat][acat];[vcat]crop=864:1080[vout]"},
	}
	for _, tc := range cases {
		if got := videoPortionSpeedFilterComplex(tc.geometry, tc.start, tc.end, tc.endBounded, tc.hasAudio, tc.speed); got != tc.want {
			t.Errorf("%s filter_complex =\n%q\nwant\n%q", tc.name, got, tc.want)
		}
	}
}

// TestPortionSpeedPhrase pins the op phrase for the summary and prompt.
func TestPortionSpeedPhrase(t *testing.T) {
	cases := []struct {
		name       string
		start      float64
		end        float64
		endBounded bool
		speed      float64
		want       string
	}{
		{"bounded", 10, 20, true, 3, "3x playback speed from 10s to 20s"},
		{"open end", 10, 0, false, 0.5, "0.5x playback speed from 10s to the end"},
		{"open start", 0, 4.5, true, 2, "2x playback speed from the start to 4.5s"},
	}
	for _, tc := range cases {
		if got := portionSpeedPhrase(tc.start, tc.end, tc.endBounded, tc.speed); got != tc.want {
			t.Errorf("%s phrase = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestFFmpegToolValidation pins the per-tool Validate rules the planner sees
// as correction messages.
func TestFFmpegToolValidation(t *testing.T) {
	screenshot := screenshotVideoToolDefinition()
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], ".at or .count is required") {
		t.Errorf("screenshot without at or count = %v, want the .at-or-.count required error", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "banana"}); len(errors) == 0 || !strings.Contains(errors[0], ".at must be a timestamp") {
		t.Errorf("screenshot with a bad at = %v", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "00:01:30"}); len(errors) != 0 {
		t.Errorf("screenshot with a clock at = %v, want none", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "0, 9.08,00:01:30"}); len(errors) != 0 {
		t.Errorf("screenshot with an at list = %v, want none", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "0,9.08,banana"}); len(errors) == 0 || !strings.Contains(errors[0], "comma-separated list") {
		t.Errorf("screenshot with a bad at list token = %v, want the list-form timestamp error", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{Count: 10}); len(errors) != 0 {
		t.Errorf("screenshot with count = %v, want none", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "4.5", Count: 10}); len(errors) != 0 {
		t.Errorf("screenshot with at and count = %v, want none (at wins)", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{Count: 0}); len(errors) == 0 || !strings.Contains(errors[0], ".at or .count is required") {
		t.Errorf("screenshot with count 0 = %v, want the .at-or-.count required error", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{Count: screenshotTimestampsCap + 1}); len(errors) == 0 || !strings.Contains(errors[0], ".count must be between 1 and") {
		t.Errorf("screenshot with an oversized count = %v, want the range error", errors)
	}
	tokens := make([]string, screenshotTimestampsCap+1)
	for i := range tokens {
		tokens[i] = strconv.Itoa(i)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: strings.Join(tokens, ",")}); len(errors) == 0 || !strings.Contains(errors[0], "at most") {
		t.Errorf("screenshot with an oversized at list = %v, want the cap error", errors)
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

	transform := transformVideoToolDefinition()
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], ".transform_video needs at least one of") {
		t.Errorf("transform without ops = %v, want the at-least-one error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{AspectRatio: "wide"}); len(errors) == 0 || !strings.Contains(errors[0], ".aspectRatio must be a W:H ratio") {
		t.Errorf("transform with a bad ratio = %v", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Width: 1279}); len(errors) == 0 || !strings.Contains(errors[0], "must be even") {
		t.Errorf("transform with an odd width = %v", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Rotate: 45}); len(errors) == 0 || !strings.Contains(errors[0], ".rotate must be 90, 180, or 270") {
		t.Errorf("transform with a bad rotate = %v", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Flip: "diagonal"}); len(errors) == 0 || !strings.Contains(errors[0], `.flip must be "horizontal" or "vertical"`) {
		t.Errorf("transform with a bad flip = %v", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Speed: 10}); len(errors) == 0 || !strings.Contains(errors[0], ".speed must be a playback multiplier between 0.25 and 8") {
		t.Errorf("transform with an out-of-range speed = %v, want the range error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Speed: 0.1}); len(errors) == 0 || !strings.Contains(errors[0], ".speed must be a playback multiplier between 0.25 and 8") {
		t.Errorf("transform with a too-slow speed = %v, want the range error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Width: 1280, Speed: 1}); len(errors) == 0 || !strings.Contains(errors[0], ".speed must not be 1") {
		t.Errorf("transform with speed 1 = %v, want the no-op error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Start: "10"}); len(errors) == 0 || !strings.Contains(errors[0], ".start and .end are only valid with speed") {
		t.Errorf("transform with bounds but no speed = %v, want the bounds-need-speed error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Speed: 2, Start: "banana"}); len(errors) == 0 || !strings.Contains(errors[0], `.start must be a timestamp`) {
		t.Errorf("transform with a bad portion start = %v, want the timestamp error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Speed: 2, Start: "20", End: "10"}); len(errors) == 0 || !strings.Contains(errors[0], ".end must be after start") {
		t.Errorf("transform with end before start = %v, want the ordering error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Width: 1280, Height: 720}); len(errors) != 0 {
		t.Errorf("valid transform = %v, want none", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Speed: 3}); len(errors) != 0 {
		t.Errorf("valid speed-only transform = %v, want none", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Speed: 2, Start: "00:00:10", End: "00:00:20"}); len(errors) != 0 {
		t.Errorf("valid portion transform = %v, want none", errors)
	}
}

// TestSplitVideoDescriptionTeachesOneSegmentPerCall pins the planner-facing
// count contract on split_video: one call keeps one segment, so a multi-part
// split is one call per part in the same plan. conv_e11bdd971b1b1e674d23c6e3:
// "split into two parts at the 10s mark" produced only the tail clip because
// the planner mapped the whole task onto a single start=10 call.
func TestSplitVideoDescriptionTeachesOneSegmentPerCall(t *testing.T) {
	desc := splitVideoToolDefinition().Description
	for _, fragment := range []string{"ONE segment", `{"end":"10"}`, `{"start":"10"}`, "same plan"} {
		if !strings.Contains(desc, fragment) {
			t.Fatalf("split_video description = %q, want it to include %q", desc, fragment)
		}
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

// TestSplitTimestampTokens pins the at-list tokenizer: single values pass
// through, commas (with tolerant spacing) split, and junk collapses away.
func TestSplitTimestampTokens(t *testing.T) {
	cases := []struct {
		name string
		at   string
		want []string
	}{
		{"single seconds", "4.5", []string{"4.5"}},
		{"single clock", "00:01:30", []string{"00:01:30"}},
		{"comma list", "0,9.08,18.17", []string{"0", "9.08", "18.17"}},
		{"spaced list", "0, 9.08 , 18.17", []string{"0", "9.08", "18.17"}},
		{"blank tokens drop", "0,,9.08,", []string{"0", "9.08"}},
		{"empty", "", nil},
		{"only separators", " , ,", nil},
	}
	for _, tc := range cases {
		got := splitTimestampTokens(tc.at)
		if len(got) != len(tc.want) {
			t.Errorf("%s tokens = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s tokens = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// TestEqualIntervalTimestamps pins the count-mode spacing: i/count × duration,
// first frame at 0, last at (count-1)/count — never the clip's final frame.
// 90.833333s / 10 is the conv_8ba2eae289b5f884d7064b18 arithmetic the planner
// had to do in-plan (and got right); here the tool does it.
func TestEqualIntervalTimestamps(t *testing.T) {
	got := equalIntervalTimestamps(90.833333, 10)
	if len(got) != 10 {
		t.Fatalf("timestamps = %v, want 10 entries", got)
	}
	if got[0] != "0" {
		t.Errorf("first timestamp = %q, want \"0\"", got[0])
	}
	if got[1] != "9.0833333" {
		t.Errorf("second timestamp = %q, want 90.833333/10", got[1])
	}
	if got[9] != "81.7499997" {
		t.Errorf("last timestamp = %q, want 9/10 of the duration", got[9])
	}
	if got := equalIntervalTimestamps(12.5, 4); len(got) != 4 || got[1] != "3.125" || got[3] != "9.375" {
		t.Errorf("quarter intervals = %v, want 0/3.125/6.25/9.375", got)
	}
	if got := equalIntervalTimestamps(12.5, 1); len(got) != 1 || got[0] != "0" {
		t.Errorf("single-frame intervals = %v, want just the first frame", got)
	}
}

// TestScreenshotTimestampsPhrase pins the capped list rendering.
func TestScreenshotTimestampsPhrase(t *testing.T) {
	if got := screenshotTimestampsPhrase([]string{"0", "4.5"}); got != "0, 4.5" {
		t.Errorf("short phrase = %q", got)
	}
	seven := []string{"0", "1", "2", "3", "4", "5", "6"}
	if got := screenshotTimestampsPhrase(seven); got != "0, 1, 2, 3, 4, …" {
		t.Errorf("long phrase = %q, want the first five and an ellipsis", got)
	}
}

// TestScreenshotDescriptionTeachesBatch pins the planner-facing batch
// contract: the description is what rides the planner prompt, and without it a
// "10 screenshots at equal intervals" turn plans 10 calls against the 3-per-
// round cap (conv_8ba2eae289b5f884d7064b18).
func TestScreenshotDescriptionTeachesBatch(t *testing.T) {
	desc := screenshotVideoToolDefinition().Description
	for _, fragment := range []string{"comma-separated", "count", "equal intervals", "single call", "no probe_media", "at wins"} {
		if !strings.Contains(desc, fragment) {
			t.Fatalf("screenshot_video description = %q, want it to include %q", desc, fragment)
		}
	}
}

// TestHarnessPlanSchemaAdmitsScreenshotCount pins that the format-schema
// planner grammar frees `count` like the per-tool param schema does — the two
// are maintained separately, and a grammar without the property silently
// takes the batch form away from every Ollama-planned turn.
func TestHarnessPlanSchemaAdmitsScreenshotCount(t *testing.T) {
	registry := defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
	schema := harnessToolPlanSchema(registry)
	toolCalls, ok := schema["properties"].(map[string]any)["toolCalls"].(map[string]any)
	if !ok {
		t.Fatalf("plan schema = %#v, want a toolCalls property", schema)
	}
	items, ok := toolCalls["items"].(map[string]any)
	if !ok {
		t.Fatalf("toolCalls schema = %#v, want items", toolCalls)
	}
	properties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("toolCalls items = %#v, want properties", items)
	}
	for _, param := range []string{"at", "count"} {
		if _, ok := properties[param].(map[string]any); !ok {
			t.Errorf("plan grammar toolCalls properties missing %q (has %v)", param, propertiesKeys(properties))
		}
	}
}

func propertiesKeys(properties map[string]any) []string {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestFFmpegScreenshotBatchByCountExecutes drives the equal-interval batch:
// one call, ten frames, the duration read from the (faked) ffprobe report of
// 12.5s — so the seeks land at 0, 1.25, …, 11.25.
func TestFFmpegScreenshotBatchByCountExecutes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{Count: 10})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok || typed.Count != 10 || len(typed.Images) != 10 {
		t.Fatalf("result payload = %+v, want 10 frames", result.Result)
	}
	for i, image := range typed.Images {
		if !strings.HasPrefix(image, "data:image/jpeg;base64,") {
			t.Fatalf("image %d payload = %q", i, image[:40])
		}
	}
	args := fakeFFmpegArgs(t, bin)
	for _, seek := range []string{"-ss 0", "-ss 1.25", "-ss 6.25", "-ss 11.25"} {
		if !strings.Contains(args, seek) {
			t.Fatalf("ffmpeg args = %q, want the %s seek", args, seek)
		}
	}
	if !strings.Contains(result.Summary, "10 of 10 frames at equal intervals") {
		t.Fatalf("summary = %q, want the equal-interval phrase", result.Summary)
	}
}

// TestFFmpegScreenshotBatchByAtListExecutes drives the explicit list form:
// three named timestamps, three frames, order preserved.
func TestFFmpegScreenshotBatchByAtListExecutes(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{At: "0, 6.25, 00:00:12"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok || typed.Count != 3 || len(typed.Images) != 3 {
		t.Fatalf("result payload = %+v, want 3 frames", result.Result)
	}
	if !strings.Contains(typed.Prompt, "3 frames at 0, 6.25, 00:00:12") {
		t.Fatalf("prompt = %q, want the frame list", typed.Prompt)
	}
}

// TestParseVideoRational pins ffprobe's rational aspect grammar: N:D with a
// nonzero denominator, 0:1 read as "unknown" (num 0), everything else false.
func TestParseVideoRational(t *testing.T) {
	cases := []struct {
		value  string
		num    int
		den    int
		wantOK bool
	}{
		{"4:3", 4, 3, true},
		{"1:1", 1, 1, true},
		{"0:1", 0, 1, true},
		{"32:27", 32, 27, true},
		{" 4 : 3 ", 4, 3, true},
		{"", 0, 0, false},
		{"4", 0, 0, false},
		{"4:3:2", 0, 0, false},
		{"a:b", 0, 0, false},
		{"4:0", 0, 0, false},
	}
	for _, tc := range cases {
		num, den, ok := parseVideoRational(tc.value)
		if ok != tc.wantOK || (ok && (num != tc.num || den != tc.den)) {
			t.Errorf("parseVideoRational(%q) = (%d, %d, %v), want (%d, %d, %v)", tc.value, num, den, ok, tc.num, tc.den, tc.wantOK)
		}
	}
}

// TestAnamorphicDisplayDimensions pins the display-size math: width scales by
// the sample aspect, height never moves, and square/unknown/collapsed ratios
// report no stretch.
func TestAnamorphicDisplayDimensions(t *testing.T) {
	cases := []struct {
		name          string
		width         int
		height        int
		num           int
		den           int
		wantWidth     int
		wantHeight    int
		wantStretched bool
	}{
		{"16:9 from 4:3 storage", 960, 720, 4, 3, 1280, 720, true},
		{"NTSC widescreen", 720, 480, 32, 27, 853, 480, true},
		{"narrower display", 960, 720, 3, 4, 720, 720, true},
		{"square pixels", 1920, 1080, 1, 1, 0, 0, false},
		{"unknown", 1920, 1080, 0, 1, 0, 0, false},
		{"degenerate dimensions", 0, 720, 4, 3, 0, 0, false},
		{"rounds back onto the width", 5, 4, 101, 100, 0, 0, false},
	}
	for _, tc := range cases {
		width, height, stretched := anamorphicDisplayDimensions(tc.width, tc.height, tc.num, tc.den)
		if stretched != tc.wantStretched || (stretched && (width != tc.wantWidth || height != tc.wantHeight)) {
			t.Errorf("%s = (%d, %d, %v), want (%d, %d, %v)", tc.name, width, height, stretched, tc.wantWidth, tc.wantHeight, tc.wantStretched)
		}
	}
}

// TestFFmpegScreenshotAnamorphicCorrection pins the squeezed-frame fix: an
// anamorphic source (frames stored 960x720, stretched to 1280x720 on
// playback) is captured at its display size, while square, unknown-aspect,
// and unreadable sources keep the plain frame dump.
func TestFFmpegScreenshotAnamorphicCorrection(t *testing.T) {
	t.Run("resamples to the display size with a notice", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAnamorphicScript)
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
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vf scale=1280:720,setsar=1 ") {
			t.Fatalf("ffmpeg args = %q, want the display-size resample", args)
		}
		if len(typed.Notices) == 0 || !strings.Contains(typed.Notices[0], "960x720") || !strings.Contains(typed.Notices[0], "1280x720") {
			t.Fatalf("notices = %v, want the stored-vs-display explanation", typed.Notices)
		}
	})
	t.Run("square pixels capture untouched", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "screenshot_video", HarnessToolCall{At: "4.5"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolImageResult)
		if len(typed.Notices) != 0 {
			t.Fatalf("notices = %v, a square-pixel capture should carry none", typed.Notices)
		}
		if args := fakeFFmpegArgs(t, bin); strings.Contains(args, "scale=") {
			t.Fatalf("ffmpeg args = %q, square pixels must not resample", args)
		}
	})
	t.Run("unknown sample aspect stays untouched", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeUnknownSARScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "screenshot_video", HarnessToolCall{At: "4.5"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		if args := fakeFFmpegArgs(t, bin); strings.Contains(args, "scale=") {
			t.Fatalf("ffmpeg args = %q, unknown aspect must be treated as square", args)
		}
	})
	t.Run("rotated anamorphic swaps the display axes", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeRotatedAnamorphicScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "screenshot_video", HarnessToolCall{At: "4.5"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		// Stored 960x720 at 4:3 plays as 1280x720, rotated sideways: the
		// display size swaps to 720x1280.
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vf scale=720:1280,setsar=1 ") {
			t.Fatalf("ffmpeg args = %q, want the rotated display-size resample", args)
		}
	})
	t.Run("MP4 sniff corrects without ffprobe", func(t *testing.T) {
		// No ffprobe override and a lookup stub without one (the override is
		// looked up by its own value), so the aspect can only come from the
		// attached MP4's tkhd display size vs the stsd coded size.
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		config := defaultAppConfig()
		ffmpegPath := writeFakeWhisper(t, bin, "ffmpeg", fakeFFmpegScript)
		stubLocalLookup(t, map[string]string{"ffmpeg": ffmpegPath, ffmpegPath: ffmpegPath})
		config.Providers.Local.FFmpeg.Binary = ffmpegPath
		clip := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4FixtureWithSampleEntry(1280, 720, 960, 720))
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{clip},
		}, "screenshot_video", HarnessToolCall{At: "1"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vf scale=1280:720,setsar=1 ") {
			t.Fatalf("ffmpeg args = %q, want the sniffed display-size resample", args)
		}
		typed := result.Result.(ToolImageResult)
		if len(typed.Notices) == 0 || !strings.Contains(typed.Notices[0], "non-square pixels") {
			t.Fatalf("notices = %v, want the sniff-path aspect notice", typed.Notices)
		}
	})
	t.Run("MP4 sniff leaves square storage untouched", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		config := defaultAppConfig()
		ffmpegPath := writeFakeWhisper(t, bin, "ffmpeg", fakeFFmpegScript)
		stubLocalLookup(t, map[string]string{"ffmpeg": ffmpegPath, ffmpegPath: ffmpegPath})
		config.Providers.Local.FFmpeg.Binary = ffmpegPath
		clip := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4FixtureWithSampleEntry(1344, 768, 1344, 768))
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{clip},
		}, "screenshot_video", HarnessToolCall{At: "1"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		if args := fakeFFmpegArgs(t, bin); strings.Contains(args, "scale=") {
			t.Fatalf("ffmpeg args = %q, matching coded and display sizes must not resample", args)
		}
	})
}

// TestFFmpegProbeAnamorphicEvidence pins probe_media's assessment of the
// anomaly: the stored and display sizes, both aspect strings, and a summary
// that says what the clip shows on playback.
func TestFFmpegProbeAnamorphicEvidence(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAnamorphicScript)
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
	if typed.Width != 960 || typed.Height != 720 || typed.DisplayWidth != 1280 || typed.DisplayHeight != 720 {
		t.Fatalf("probe dimensions = %+v, want stored 960x720 and display 1280x720", typed)
	}
	if typed.SampleAspectRatio != "4:3" || typed.DisplayAspectRatio != "16:9" {
		t.Fatalf("probe aspect = %q / %q, want 4:3 / 16:9", typed.SampleAspectRatio, typed.DisplayAspectRatio)
	}
	if !strings.Contains(result.Summary, "960x720 (displays as 1280x720, 16:9)") {
		t.Fatalf("summary = %q, want the display annotation", result.Summary)
	}
}

// TestFFmpegProbeRotationEvidence pins the rotation read: modern ffprobe's
// side_data_list display matrix reaches the evidence and the summary.
func TestFFmpegProbeRotationEvidence(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeRotatedAnamorphicScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "probe_media", HarnessToolCall{})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolProbeResult)
	if !ok || typed.Rotation != -90 {
		t.Fatalf("result payload = %+v, want rotation -90", result.Result)
	}
	if !strings.Contains(result.Summary, "rotation -90°") {
		t.Fatalf("summary = %q, want the rotation fact", result.Summary)
	}
}

// TestFFmpegScreenshotCountWithoutDurationFails pins the count-mode escape
// hatch: with no ffprobe and a clip the MP4 sniff cannot read, the error names
// the explicit-at form so the planner repairs into a timestamp list instead of
// retrying the same call.
func TestFFmpegScreenshotCountWithoutDurationFails(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, "")
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{Count: 4})
	if result.Status == "completed" || !strings.Contains(result.Error, "explicit timestamps") {
		t.Fatalf("result = %+v, want the explicit-timestamps guidance error", result)
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

// TestFFmpegTransformExecutes drives transform_video through the gateway
// against the fake ffmpeg/ffprobe: every branch of the filter resolution,
// including the MP4 tkhd fallback when ffprobe does not resolve.
func TestFFmpegTransformExecutes(t *testing.T) {
	t.Run("exact size cover-crops", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Width: 1280, Height: 720})
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
		if !strings.Contains(args, "-vf scale=1280:720:force_original_aspect_ratio=increase,crop=1280:720 ") || !strings.Contains(args, "libx264") {
			t.Fatalf("transform args = %q, want the cover-crop filter and a re-encode", args)
		}
		if len(typed.Notices) == 0 || !strings.Contains(typed.Notices[0], "re-encoded") {
			t.Fatalf("notices = %v, want the re-encode notice", typed.Notices)
		}
		if !strings.Contains(result.Summary, "resized to 1280x720") {
			t.Fatalf("summary = %q", result.Summary)
		}
	})

	t.Run("aspect crop from the probed dimensions", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{AspectRatio: "4:5"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vf crop=864:1080 ") {
			t.Fatalf("transform args = %q, want the 4:5 crop of the probed 1920x1080", args)
		}
	})

	t.Run("speed 3x rescales video and audio", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 3})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vf setpts=PTS/3 ") || !strings.Contains(args, "-af atempo=2.0,atempo=1.5 ") {
			t.Fatalf("transform args = %q, want the setpts rescale and the chained atempo", args)
		}
		if !strings.Contains(result.Summary, "3x playback speed") {
			t.Fatalf("summary = %q, want the speed op", result.Summary)
		}
	})

	t.Run("slow motion 0.5x", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 0.5})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vf setpts=PTS/0.5 ") || !strings.Contains(args, "-af atempo=0.5 ") {
			t.Fatalf("transform args = %q, want the half-speed setpts and a single atempo", args)
		}
	})

	t.Run("portion 3x between 2 and 4", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 3, Start: "2", End: "4"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		for _, want := range []string{
			"-filter_complex ",
			"trim=start=2:end=4,setpts=(PTS-STARTPTS)/3[v1]",
			"atrim=start=2:end=4,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1]",
			"concat=n=3:v=1:a=1[vout][acat]",
			"-map [vout] -map [acat]",
		} {
			if !strings.Contains(args, want) {
				t.Fatalf("transform args = %q, want %q", args, want)
			}
		}
		if !strings.Contains(result.Summary, "3x playback speed from 2s to 4s") {
			t.Fatalf("summary = %q", result.Summary)
		}
		if !strings.Contains(strings.Join(typed.Notices, " "), "head and tail") {
			t.Fatalf("notices = %v, want the head/tail re-encode notice", typed.Notices)
		}
	})

	t.Run("portion end past the clip's end clamps", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript) // duration 12.5
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 2, Start: "10", End: "30"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "trim=start=10,setpts=") || strings.Contains(args, ":end=30") {
			t.Fatalf("transform args = %q, want an open-ended middle after the clamp", args)
		}
		if !strings.Contains(args, "concat=n=2:v=1:a=1[vout][acat]") {
			t.Fatalf("transform args = %q, want a two-segment concat", args)
		}
		if !strings.Contains(strings.Join(typed.Notices, " "), "at or past the clip's end") {
			t.Fatalf("notices = %v, want the clamp notice", typed.Notices)
		}
	})

	t.Run("portion start past the clip's end fails", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript) // duration 12.5
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 2, Start: "30"})
		if result.Status == "completed" || !strings.Contains(result.Error, "at or past the end of the attached clip") {
			t.Fatalf("result = %+v, want the start-past-end error", result)
		}
	})

	t.Run("portion without audio drops the audio chains", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeNoAudioScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 2, Start: "2", End: "4"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if strings.Contains(args, "[0:a]") || !strings.Contains(args, "concat=n=3:v=1:a=0[vout]") {
			t.Fatalf("transform args = %q, want a video-only graph", args)
		}
		if strings.Contains(args, "-map [acat]") {
			t.Fatalf("transform args = %q, want no audio map", args)
		}
	})

	t.Run("aspect crop without ffprobe reads MP4 bytes", func(t *testing.T) {
		// No ffprobe override and a lookup stub without one (the override is
		// looked up by its own value), so dimensions can only come from the
		// attached MP4's tkhd.
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		config := defaultAppConfig()
		ffmpegPath := writeFakeWhisper(t, bin, "ffmpeg", fakeFFmpegScript)
		stubLocalLookup(t, map[string]string{"ffmpeg": ffmpegPath, ffmpegPath: ffmpegPath})
		config.Providers.Local.FFmpeg.Binary = ffmpegPath
		clip := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4FixtureWithVideoTrack(1344, 768))
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{clip},
		}, "transform_video", HarnessToolCall{AspectRatio: "16:9"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vf crop=1344:756 ") {
			t.Fatalf("transform args = %q, want the 16:9 crop of the tkhd-reported 1344x768", args)
		}
	})

	t.Run("aspect crop fails without readable dimensions", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		config := defaultAppConfig()
		ffmpegPath := writeFakeWhisper(t, bin, "ffmpeg", fakeFFmpegScript)
		stubLocalLookup(t, map[string]string{"ffmpeg": ffmpegPath, ffmpegPath: ffmpegPath})
		config.Providers.Local.FFmpeg.Binary = ffmpegPath
		// The ftyp-only fixture is an MP4 with no readable track box.
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{AspectRatio: "16:9"})
		if result.Status != "failed" || !strings.Contains(result.Error, "dimensions") {
			t.Fatalf("result = %+v (error %s), want the unreadable-dimensions failure", result, result.Error)
		}
	})

	t.Run("rotate and flip", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		for _, tc := range []struct {
			call HarnessToolCall
			want string
		}{
			{HarnessToolCall{Rotate: 90}, "-vf transpose=1 "},
			{HarnessToolCall{Rotate: 180}, "-vf transpose=1,transpose=1 "},
			{HarnessToolCall{Rotate: 270}, "-vf transpose=2 "},
			{HarnessToolCall{Flip: "horizontal"}, "-vf hflip "},
			{HarnessToolCall{Flip: "vertical"}, "-vf vflip "},
		} {
			result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
				AttachedVideos: []string{videoDataURL("CLIP-ONE")},
			}, "transform_video", tc.call)
			if result.Status != "completed" {
				t.Fatalf("call %+v = %+v (error %s)", tc.call, result, result.Error)
			}
			typed, _ := result.Result.(ToolVideoResult)
			os.Remove(typed.Videos[0].TempPath)
			if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, tc.want) {
				t.Fatalf("call %+v args = %q, want %q", tc.call, args, tc.want)
			}
		}
	})
}

// TestFFmpegRegistryGating pins the tool gates: nothing without a binary, the
// six transform tools with ffmpeg alone, plus probe_media only when ffprobe
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
	for _, want := range []string{"screenshot_video", "split_video", "join_videos", "extract_audio", "replace_audio", "transform_video"} {
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
	for _, param := range []string{"at", "start", "end", "mode", "speed"} {
		if _, ok := properties[param].(map[string]any); !ok {
			t.Errorf("plan schema properties missing %q: %+v", param, properties)
		}
	}
}

func TestApplyKwargsFFmpegParams(t *testing.T) {
	var call HarnessToolCall
	applyKwargs(&call, "at='4.5', start='10', end='25', mode='fast', speed=3")
	if call.At != "4.5" || call.Start != "10" || call.End != "25" || call.Mode != "fast" || call.Speed != 3 {
		t.Fatalf("applyKwargs = %+v", call)
	}
	var slowed HarnessToolCall
	applyKwargs(&slowed, "speed='0.5x'")
	if slowed.Speed != 0.5 {
		t.Fatalf("applyKwargs speed='0.5x' = %v, want 0.5", slowed.Speed)
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
