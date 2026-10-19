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
	"math"
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
for a in "$@"; do
  case "$a" in
    # A help query produces no output file — writing the last arg ("filter=xfade")
    # into the process CWD would stray-file the repo.
    -h|-filters|-version) exit 0 ;;
  esac
done
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

// fakeFFmpegNoMidSeekScript fakes the past-the-end shape: the only plain seek
// that decodes a frame is -ss 0 (a timestamp at/past the stream's end produces
// zero frames) while the end-relative grab always succeeds — driving
// screenshot_video's / contact_sheet_video's final-frame fallback without
// faking it for live timestamps too. Args are recorded like fakeFFmpegScript.
const fakeFFmpegNoMidSeekScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" >> "$dir/args.txt"
prev=""
out=""
mode="seek"
for a in "$@"; do
  if [ "$prev" = "-sseof" ]; then mode="end"; fi
  prev="$a"
  out="$a"
done
if [ "$mode" = "end" ]; then
  printf '\377\330\377FAKE-MEDIA' > "$out"
  exit 0
fi
prev=""
for a in "$@"; do
  if [ "$prev" = "-ss" ] && [ "$a" = "0" ]; then
    printf '\377\330\377FAKE-MEDIA' > "$out"
    exit 0
  fi
  prev="$a"
done
echo "fake ffmpeg: no filtered frames for output stream" >&2
exit 1
`

// fakeFFmpegAlwaysFailsScript fakes a source no capture can read (a corrupt
// file, say). Its two distinct failure messages let a test pin that the
// final-frame fallback surfaces the ORIGINAL seek error, not the fallback's.
const fakeFFmpegAlwaysFailsScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" >> "$dir/args.txt"
for a in "$@"; do
  if [ "$a" = "-sseof" ]; then
    echo "fake ffmpeg: end-relative grab failed" >&2
    exit 1
  fi
done
echo "fake ffmpeg: no filtered frames for output stream" >&2
exit 1
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

// fakeFFprobeLongClipScript fakes ffprobe over a 600-second 1080p clip —
// far beyond reverse_video's decoded-frame memory budget — driving the
// guard's refusal.
const fakeFFprobeLongClipScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"600.000","format_name":"mov,mp4,m4a,3gp,3g2,mj2"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"avg_frame_rate":"30000/1001"},{"codec_type":"audio","codec_name":"aac"}]}
JSON
`

// fakeFFprobeFailScript fakes an ffprobe that always fails: it drives the
// probe-failure fallbacks deterministically — an absent ffprobe override
// would let a real ffprobe on PATH probe the fixtures (and two genuine WAVs
// genuinely match, so the copy path would win instead of the fallback).
const fakeFFprobeFailScript = `#!/bin/sh
echo "ffprobe: injected failure" >&2
exit 1
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

// audioDataURL wraps a payload in an audio attachment data URL with the given
// media type — split_audio's container mapping is driven by exactly this
// header (wavDataURL in local_tools_test.go covers the .wav happy path).
func audioDataURL(mimeType, payload string) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString([]byte(payload))
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
		{"last frame square pixels", ffmpegLastFrameArgs("in.mp4", "out.jpg", 0, 0),
			"-sseof -1 -i in.mp4 -vf reverse -frames:v 1 -q:v 2 out.jpg"},
		{"last frame resamples anamorphic", ffmpegLastFrameArgs("in.mp4", "out.jpg", 1280, 720),
			"-sseof -1 -i in.mp4 -vf scale=1280:720,setsar=1,reverse -frames:v 1 -q:v 2 out.jpg"},
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
		{"split audio bounded copy", ffmpegSplitAudioArgs("in.wav", "10", 15, nil, "out.wav"),
			"-i in.wav -ss 10 -t 15 -vn -c:a copy -avoid_negative_ts make_zero out.wav"},
		{"split audio full file copy", ffmpegSplitAudioArgs("in.mp3", "", 0, nil, "out.mp3"),
			"-i in.mp3 -vn -c:a copy -avoid_negative_ts make_zero out.mp3"},
		{"split audio fallback re-encode", ffmpegSplitAudioArgs("in.bin", "00:01:30", 5.5, audioMP3FallbackArgs, "out.mp3"),
			"-i in.bin -ss 00:01:30 -t 5.5 -vn -c:a libmp3lame -b:a 192k -avoid_negative_ts make_zero out.mp3"},
		{"join audios copy", ffmpegJoinAudioArgs("list.txt", "out.wav", true, nil),
			"-f concat -safe 0 -i list.txt -vn -c:a copy out.wav"},
		{"join audios reencode", ffmpegJoinAudioArgs("list.txt", "out.m4a", false, []string{"-c:a", "aac", "-b:a", "192k"}),
			"-f concat -safe 0 -i list.txt -vn -c:a aac -b:a 192k out.m4a"},
		{"extract copy", ffmpegExtractAudioArgs("in.mp4", "out.m4a", true),
			"-i in.mp4 -vn -c:a copy out.m4a"},
		{"extract convert", ffmpegExtractAudioArgs("in.mp4", "out.mp3", false),
			"-i in.mp4 -vn -c:a libmp3lame -b:a 192k out.mp3"},
		{"remove audio stream copy", ffmpegRemoveAudioArgs("in.mp4", "out.mp4"),
			"-i in.mp4 -map 0:v:0 -c:v copy -an -movflags +faststart out.mp4"},
		{"convert audio mp3 default bitrate", ffmpegConvertAudioArgs("in.m4a", "out.mp3", []string{"-c:a", "libmp3lame", "-b:a", "192k"}),
			"-i in.m4a -vn -c:a libmp3lame -b:a 192k out.mp3"},
		{"convert audio lossless", ffmpegConvertAudioArgs("in.mp3", "out.wav", []string{"-c:a", "pcm_s16le"}),
			"-i in.mp3 -vn -c:a pcm_s16le out.wav"},
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
		{"transform fade appended after speed", ffmpegTransformArgs("in.mp4", "setpts=PTS/2,fade=t=out:st=4.25:d=2", "atempo=2,afade=t=out:st=4.25:d=2", "out.mp4"),
			"-i in.mp4 -vf setpts=PTS/2,fade=t=out:st=4.25:d=2 -af atempo=2,afade=t=out:st=4.25:d=2 -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"portion speed with audio", ffmpegPortionSpeedArgs("in.mp4", "FC", true, false, "out.mp4"),
			"-i in.mp4 -filter_complex FC -map [vout] -map [acat] -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"portion speed with faded audio maps [aout]", ffmpegPortionSpeedArgs("in.mp4", "FC", true, true, "out.mp4"),
			"-i in.mp4 -filter_complex FC -map [vout] -map [aout] -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"portion speed without audio", ffmpegPortionSpeedArgs("in.mp4", "FC", false, false, "out.mp4"),
			"-i in.mp4 -filter_complex FC -map [vout] -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"portion speed without audio ignores the faded flag", ffmpegPortionSpeedArgs("in.mp4", "FC", false, true, "out.mp4"),
			"-i in.mp4 -filter_complex FC -map [vout] -c:v libx264 -preset veryfast -crf 18 -c:a aac -b:a 192k -movflags +faststart out.mp4"},
		{"transform audio fades in the recording's codec", ffmpegTransformAudioArgs("in.wav", "afade=t=in:st=0:d=1,afade=t=out:st=27:d=3", []string{"-c:a", "pcm_s16le"}, "out.wav"),
			"-i in.wav -vn -af afade=t=in:st=0:d=1,afade=t=out:st=27:d=3 -c:a pcm_s16le out.wav"},
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
		{"explicit crop mode matches the default", HarnessToolCall{AspectRatio: "4:5", Mode: "crop"}, 1920, 1080,
			"crop=864:1080", "cropped to 4:5 (center 864x1080)"},
		{"blur fill to aspect", HarnessToolCall{AspectRatio: "9:16", Mode: "blur"}, 1920, 1080,
			"split=2[bg][fg];[bg]scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920,gblur=sigma=27[bgf];[fg]scale=1080:606[fgf];[bgf][fgf]overlay=(W-w)/2:(H-h)/2",
			"filled to 9:16 (1080x1920) with a blurred background"},
		{"pad fill to aspect", HarnessToolCall{AspectRatio: "9:16", Mode: "pad"}, 1920, 1080,
			"scale=1080:606,pad=1080:1920:(ow-iw)/2:(oh-ih)/2",
			"padded to 9:16 (1080x1920) (black bars)"},
		{"blur fill widening a portrait clip", HarnessToolCall{AspectRatio: "16:9", Mode: "blur"}, 1080, 1920,
			"split=2[bg][fg];[bg]scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080,gblur=sigma=27[bgf];[fg]scale=606:1080[fgf];[bgf][fgf]overlay=(W-w)/2:(H-h)/2",
			"filled to 16:9 (1920x1080) with a blurred background"},
		{"blur fill to exact pixels", HarnessToolCall{Mode: "blur", Width: 1080, Height: 1920}, 1920, 1080,
			"split=2[bg][fg];[bg]scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920,gblur=sigma=27[bgf];[fg]scale=1080:606[fgf];[bgf][fgf]overlay=(W-w)/2:(H-h)/2",
			"filled to 1080x1920 with a blurred background"},
		{"pad fill to exact pixels", HarnessToolCall{Mode: "pad", Width: 720, Height: 720}, 1920, 1080,
			"scale=720:404,pad=720:720:(ow-iw)/2:(oh-ih)/2",
			"padded to 720x720 (black bars)"},
		{"fill composes with a later resize", HarnessToolCall{AspectRatio: "9:16", Mode: "pad", Width: 720}, 1920, 1080,
			"scale=1080:606,pad=1080:1920:(ow-iw)/2:(oh-ih)/2,scale=720:-2",
			"padded to 9:16 (1080x1920) (black bars), resized to 720 pixels wide"},
		{"fill composes with rotate and speed", HarnessToolCall{AspectRatio: "9:16", Mode: "blur", Rotate: 180, Speed: 2}, 1920, 1080,
			"split=2[bg][fg];[bg]scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920,gblur=sigma=27[bgf];[fg]scale=1080:606[fgf];[bgf][fgf]overlay=(W-w)/2:(H-h)/2,transpose=1,transpose=1,setpts=PTS/2",
			"filled to 9:16 (1080x1920) with a blurred background, rotated 180°, 2x playback speed"},
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
		name         string
		geometry     string
		start        float64
		end          float64
		endBounded   bool
		hasAudio     bool
		speed        float64
		audioFilters string
		want         string
	}{
		{"3x middle with audio", "", 10, 20, true, true, 3, "",
			"[0:v]trim=end=10,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=10,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=10:end=20,setpts=(PTS-STARTPTS)/3[v1];[0:a]atrim=start=10:end=20,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1];" +
				"[0:v]trim=start=20,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=20,asetpts=PTS-STARTPTS[a2];" +
				"[v0][a0][v1][a1][v2][a2]concat=n=3:v=1:a=1[vout][acat]"},
		{"2x middle without audio", "", 4, 8, true, false, 2, "",
			"[0:v]trim=end=4,setpts=PTS-STARTPTS[v0];" +
				"[0:v]trim=start=4:end=8,setpts=(PTS-STARTPTS)/2[v1];" +
				"[0:v]trim=start=8,setpts=PTS-STARTPTS[v2];" +
				"[v0][v1][v2]concat=n=3:v=1:a=0[vout]"},
		{"half-speed opening, no head", "", 0, 4.5, true, true, 0.5, "",
			"[0:v]trim=start=0:end=4.5,setpts=(PTS-STARTPTS)/0.5[v1];[0:a]atrim=start=0:end=4.5,asetpts=(PTS-STARTPTS)/0.5,atempo=0.5[a1];" +
				"[0:v]trim=start=4.5,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=4.5,asetpts=PTS-STARTPTS[a2];" +
				"[v1][a1][v2][a2]concat=n=2:v=1:a=1[vout][acat]"},
		{"3x to the end, no tail", "", 10, 0, false, true, 3, "",
			"[0:v]trim=end=10,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=10,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=10,setpts=(PTS-STARTPTS)/3[v1];[0:a]atrim=start=10,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1];" +
				"[v0][a0][v1][a1]concat=n=2:v=1:a=1[vout][acat]"},
		{"geometry covers the joined output", "crop=864:1080", 10, 20, true, true, 3, "",
			"[0:v]trim=end=10,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=10,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=10:end=20,setpts=(PTS-STARTPTS)/3[v1];[0:a]atrim=start=10:end=20,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1];" +
				"[0:v]trim=start=20,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=20,asetpts=PTS-STARTPTS[a2];" +
				"[v0][a0][v1][a1][v2][a2]concat=n=3:v=1:a=1[vcat][acat];[vcat]crop=864:1080[vout]"},
		// Fades ride the post-concat chains: the video fade routes the concat
		// through [vcat] even without geometry, and the audio fade turns
		// [acat] into [aout] for the args builder to map.
		{"fades on the joined output", "fade=t=out:st=22.333:d=2", 10, 20, true, true, 3, "afade=t=out:st=22.333:d=2",
			"[0:v]trim=end=10,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=10,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=10:end=20,setpts=(PTS-STARTPTS)/3[v1];[0:a]atrim=start=10:end=20,asetpts=(PTS-STARTPTS)/3,atempo=2.0,atempo=1.5[a1];" +
				"[0:v]trim=start=20,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=20,asetpts=PTS-STARTPTS[a2];" +
				"[v0][a0][v1][a1][v2][a2]concat=n=3:v=1:a=1[vcat][acat];[vcat]fade=t=out:st=22.333:d=2[vout];[acat]afade=t=out:st=22.333:d=2[aout]"},
		{"audio fade on a silent clip is dropped", "fade=t=in:st=0:d=1", 4, 8, true, false, 2, "afade=t=in:st=0:d=1",
			"[0:v]trim=end=4,setpts=PTS-STARTPTS[v0];" +
				"[0:v]trim=start=4:end=8,setpts=(PTS-STARTPTS)/2[v1];" +
				"[0:v]trim=start=8,setpts=PTS-STARTPTS[v2];" +
				"[v0][v1][v2]concat=n=3:v=1:a=0[vcat];[vcat]fade=t=in:st=0:d=1[vout]"},
		{"audio-only fade leaves the video straight off the concat", "", 4, 8, true, true, 2, "afade=t=out:st=9:d=1",
			"[0:v]trim=end=4,setpts=PTS-STARTPTS[v0];[0:a]atrim=end=4,asetpts=PTS-STARTPTS[a0];" +
				"[0:v]trim=start=4:end=8,setpts=(PTS-STARTPTS)/2[v1];[0:a]atrim=start=4:end=8,asetpts=(PTS-STARTPTS)/2,atempo=2[a1];" +
				"[0:v]trim=start=8,setpts=PTS-STARTPTS[v2];[0:a]atrim=start=8,asetpts=PTS-STARTPTS[a2];" +
				"[v0][a0][v1][a1][v2][a2]concat=n=3:v=1:a=1[vout][acat];[acat]afade=t=out:st=9:d=1[aout]"},
	}
	for _, tc := range cases {
		if got := videoPortionSpeedFilterComplex(tc.geometry, tc.start, tc.end, tc.endBounded, tc.hasAudio, tc.speed, tc.audioFilters); got != tc.want {
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

// TestPortionOutputDuration pins the rendered length a portion speed change
// produces — what a fade-out on that output is timed against: the head and
// tail at normal speed, the middle divided by the speed.
func TestPortionOutputDuration(t *testing.T) {
	cases := []struct {
		name       string
		start      float64
		end        float64
		endBounded bool
		duration   float64
		speed      float64
		want       float64
	}{
		{"3x middle of a 30s clip", 10, 20, true, 30, 3, 10 + 10.0/3 + 10},
		{"2x from 10s to the end", 10, 0, false, 30, 2, 10 + 10},
		{"half-speed opening", 0, 4, true, 12.5, 0.5, 8 + 8.5},
	}
	for _, tc := range cases {
		if got := portionOutputDuration(tc.start, tc.end, tc.endBounded, tc.duration, tc.speed); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s output duration = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestResolveFadePlan pins the fade clamp and the fade-out's placement: each
// fade is capped at half the output (a notice per shortened fade), the
// fade-out starts at duration minus its length, and an unknown duration
// (zero) passes the fades through untouched — the fade-in-only path with no
// measurable clip.
func TestResolveFadePlan(t *testing.T) {
	plan := resolveFadePlan(1, 2, 12.5)
	if plan.In != 1 || plan.Out != 2 || plan.OutStart != 10.5 || len(plan.Clamped) != 0 {
		t.Fatalf("plain plan = %+v, want in 1, out 2 from 10.5, no clamp", plan)
	}
	clamped := resolveFadePlan(4, 5, 6)
	if clamped.In != 3 || clamped.Out != 3 || clamped.OutStart != 3 {
		t.Fatalf("clamped plan = %+v, want both fades capped at 3 (half of 6)", clamped)
	}
	if len(clamped.Clamped) != 2 || !strings.Contains(clamped.Clamped[0], "4s fade in is longer than half the 6s output, so it was shortened to 3s") || !strings.Contains(clamped.Clamped[1], "5s fade out is longer than half the 6s output, so it was shortened to 3s") {
		t.Fatalf("clamp notices = %v, want one per shortened fade naming the numbers", clamped.Clamped)
	}
	inOnly := resolveFadePlan(2, 0, 0)
	if inOnly.In != 2 || inOnly.Out != 0 || inOnly.OutStart != 0 || len(inOnly.Clamped) != 0 {
		t.Fatalf("unmeasured fade-in plan = %+v, want the fade passed through unclamped", inOnly)
	}
	// Binary dust from a probed decimal duration renders at millisecond
	// precision in the fade-out start.
	dusty := resolveFadePlan(0, 2, 25.708333)
	if got := videoFadeFilters(dusty); len(got) != 1 || got[0] != "fade=t=out:st=23.708:d=2" {
		t.Fatalf("dusty fade-out filter = %v, want a millisecond-rounded start", got)
	}
}

// TestFadeFilterChains pins the fade stages and the target split: "both"
// fills the video and audio chains, "video" leaves audio empty, "audio" leaves
// video empty; the afade stays on the default linear curve (no c= option).
func TestFadeFilterChains(t *testing.T) {
	plan := resolveFadePlan(1, 2, 12.5)
	cases := []struct {
		target    string
		wantVideo string
		wantAudio string
	}{
		{"both", "fade=t=in:st=0:d=1,fade=t=out:st=10.5:d=2", "afade=t=in:st=0:d=1,afade=t=out:st=10.5:d=2"},
		{"video", "fade=t=in:st=0:d=1,fade=t=out:st=10.5:d=2", ""},
		{"audio", "", "afade=t=in:st=0:d=1,afade=t=out:st=10.5:d=2"},
	}
	for _, tc := range cases {
		video, audio := fadeFilterChains(plan, tc.target)
		if video != tc.wantVideo || audio != tc.wantAudio {
			t.Errorf("target %q chains = %q / %q, want %q / %q", tc.target, video, audio, tc.wantVideo, tc.wantAudio)
		}
	}
	video, audio := fadeFilterChains(fadePlan{}, "both")
	if video != "" || audio != "" {
		t.Fatalf("empty plan chains = %q / %q, want both empty", video, audio)
	}
	if got := appendFilterChain("setpts=PTS/2", "fade=t=in:st=0:d=1"); got != "setpts=PTS/2,fade=t=in:st=0:d=1" {
		t.Fatalf("appendFilterChain = %q, want the fade after the rescale", got)
	}
	if got := appendFilterChain("", "afade=t=in:st=0:d=1"); got != "afade=t=in:st=0:d=1" {
		t.Fatalf("appendFilterChain onto empty = %q, want the fade alone", got)
	}
	if got := appendFilterChain("crop=864:1080", ""); got != "crop=864:1080" {
		t.Fatalf("appendFilterChain with empty more = %q, want the chain alone", got)
	}
}

// TestFadeOpPhrases pins the summary ops, which name what the target fades
// from/to so the final model describes the right edit.
func TestFadeOpPhrases(t *testing.T) {
	plan := resolveFadePlan(1, 2.5, 20)
	cases := []struct {
		target string
		want   []string
	}{
		{"both", []string{"faded in from black and silence over 1s", "faded out to black and silence over 2.5s"}},
		{"video", []string{"faded in from black over 1s", "faded out to black over 2.5s"}},
		{"audio", []string{"faded in from silence over 1s", "faded out to silence over 2.5s"}},
	}
	for _, tc := range cases {
		if got := fadeOpPhrases(plan, tc.target); strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("target %q ops = %v, want %v", tc.target, got, tc.want)
		}
	}
	if got := fadeOpPhrases(resolveFadePlan(0, 3, 20), "both"); len(got) != 1 || got[0] != "faded out to black and silence over 3s" {
		t.Fatalf("fade-out-only ops = %v, want the single out phrase", got)
	}
}

// TestNormalizeFadeTarget pins the enum: empty and "both" land on both, the
// two narrowing values pass case-insensitively, anything else is rejected.
func TestNormalizeFadeTarget(t *testing.T) {
	for raw, want := range map[string]string{"": "both", "both": "both", " Video ": "video", "AUDIO": "audio"} {
		got, ok := normalizeFadeTarget(raw)
		if !ok || got != want {
			t.Errorf("normalizeFadeTarget(%q) = %q, %v; want %q, true", raw, got, ok, want)
		}
	}
	if _, ok := normalizeFadeTarget("music"); ok {
		t.Fatalf("normalizeFadeTarget(music) accepted, want rejected")
	}
}

// TestTransformAudioTarget pins the single-clip twin of the join's re-encode
// target: known codecs keep their family with no notice, unknown or absent
// codecs fall back to MP3 192k with the recording-worded notice.
func TestTransformAudioTarget(t *testing.T) {
	ext, mime, args, notice := transformAudioTarget("pcm_s24le")
	if ext != ".wav" || mime != "audio/wav" || strings.Join(args, " ") != "-c:a pcm_s16le" || notice != "" {
		t.Fatalf("pcm target = %s %s %v %q, want WAV with no notice", ext, mime, args, notice)
	}
	ext, mime, args, notice = transformAudioTarget("mp3")
	if ext != ".mp3" || mime != "audio/mpeg" || strings.Join(args, " ") != "-c:a libmp3lame -b:a 192k" || notice != "" {
		t.Fatalf("mp3 target = %s %s %v %q, want MP3 192k with no notice", ext, mime, args, notice)
	}
	ext, _, args, notice = transformAudioTarget("")
	if ext != ".mp3" || strings.Join(args, " ") != strings.Join(audioMP3FallbackArgs, " ") || !strings.Contains(notice, "the recording's codec could not be identified") {
		t.Fatalf("unprobed target = %s %v %q, want the MP3 fallback with the recording notice", ext, args, notice)
	}
	if _, _, _, notice = transformAudioTarget("wmav2"); !strings.Contains(notice, "the recording's codec (wmav2) has no re-encode mapping") {
		t.Fatalf("unmapped target notice = %q, want it to name the codec", notice)
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
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "end"}); len(errors) != 0 {
		t.Errorf("screenshot with at end = %v, want none", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "0,end"}); len(errors) != 0 {
		t.Errorf("screenshot with an at list containing end = %v, want none", errors)
	}
	if errors := screenshot.Validate("toolCalls[0]", HarnessToolCall{At: "endx"}); len(errors) == 0 || !strings.Contains(errors[0], ".at must be a timestamp") {
		t.Errorf("screenshot with endx = %v, want the timestamp error", errors)
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

	splitAudio := splitAudioToolDefinition()
	if errors := splitAudio.Validate("toolCalls[0]", HarnessToolCall{Start: "banana"}); len(errors) == 0 || !strings.Contains(errors[0], ".start must be a timestamp") {
		t.Errorf("split_audio with a bad start = %v", errors)
	}
	if errors := splitAudio.Validate("toolCalls[0]", HarnessToolCall{Start: "10", End: "5"}); len(errors) == 0 || !strings.Contains(errors[0], ".end must be after start") {
		t.Errorf("split_audio with end before start = %v", errors)
	}
	if errors := splitAudio.Validate("toolCalls[0]", HarnessToolCall{Start: "00:00:10", End: "25"}); len(errors) != 0 {
		t.Errorf("valid split_audio (mixed clock and seconds) = %v, want none", errors)
	}
	joinAudios := joinAudiosToolDefinition()
	if errors := joinAudios.Validate("toolCalls[0]", HarnessToolCall{Mode: "turbo"}); len(errors) == 0 || !strings.Contains(errors[0], `.mode must be "auto"`) {
		t.Errorf("join_audios with a bad mode = %v", errors)
	}
	if errors := joinAudios.Validate("toolCalls[0]", HarnessToolCall{Mode: "reencode"}); len(errors) != 0 {
		t.Errorf("join_audios reencode mode = %v, want none", errors)
	}

	convertAudio := convertAudioToolDefinition()
	if errors := convertAudio.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], ".format is required for convert_audio") {
		t.Errorf("convert_audio without a format = %v, want the required-format error", errors)
	}
	if errors := convertAudio.Validate("toolCalls[0]", HarnessToolCall{Format: "aac"}); len(errors) == 0 || !strings.Contains(errors[0], `.format must be one of "mp3"`) {
		t.Errorf("convert_audio with an unsupported format = %v, want the format enum error", errors)
	}
	if errors := convertAudio.Validate("toolCalls[0]", HarnessToolCall{Format: "MP3"}); len(errors) != 0 {
		t.Errorf("convert_audio with an uppercase format = %v, want none (formats normalize)", errors)
	}
	if errors := convertAudio.Validate("toolCalls[0]", HarnessToolCall{Format: "mp3", Quality: 101}); len(errors) == 0 || !strings.Contains(errors[0], ".quality must be 1-100") {
		t.Errorf("convert_audio with an out-of-range quality = %v, want the range error", errors)
	}
	if errors := convertAudio.Validate("toolCalls[0]", HarnessToolCall{Format: "wav", Quality: 85}); len(errors) == 0 || !strings.Contains(errors[0], ".quality applies to mp3, m4a, or ogg output only") {
		t.Errorf("convert_audio with quality on a lossless format = %v, want the lossless-quality error", errors)
	}
	if errors := convertAudio.Validate("toolCalls[0]", HarnessToolCall{Format: "flac"}); len(errors) != 0 {
		t.Errorf("valid convert_audio flac = %v, want none", errors)
	}
	if errors := convertAudio.Validate("toolCalls[0]", HarnessToolCall{Format: "mp3", Quality: 95}); len(errors) != 0 {
		t.Errorf("valid convert_audio mp3 with quality = %v, want none", errors)
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
		t.Errorf("transform with a bad flip = %v, want the flip enum error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Mode: "zoom"}); len(errors) == 0 || !strings.Contains(errors[0], `.mode must be "crop", "blur", or "pad"`) {
		t.Errorf("transform with a bad mode = %v, want the mode enum error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Mode: "blur", Width: 1080}); len(errors) == 0 || !strings.Contains(errors[0], "need a shape target") {
		t.Errorf("transform blur with a lone width = %v, want the shape-target error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Mode: "pad", Rotate: 90}); len(errors) == 0 || !strings.Contains(errors[0], "need a shape target") {
		t.Errorf("transform pad with only rotate = %v, want the shape-target error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Mode: "blur", AspectRatio: "9:16"}); len(errors) != 0 {
		t.Errorf("valid blur transform = %v, want none", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Mode: "pad", Width: 720, Height: 720}); len(errors) != 0 {
		t.Errorf("valid pad both-dims transform = %v, want none", errors)
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
	// Single-clip fades: a fade alone is an operation; the range is
	// 0.1–30s; fadeTarget is an enum that needs a fade to apply to.
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{FadeOut: 2}); len(errors) != 0 {
		t.Errorf("valid fade-out-only transform = %v, want none", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{FadeIn: 1, FadeOut: 2, FadeTarget: "audio", Speed: 2}); len(errors) != 0 {
		t.Errorf("valid fades with target and speed = %v, want none", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{FadeIn: 45}); len(errors) == 0 || !strings.Contains(errors[0], ".fadeIn must be between 0.1 and 30 seconds") {
		t.Errorf("transform with a too-long fade in = %v, want the range error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{FadeOut: 0.05}); len(errors) == 0 || !strings.Contains(errors[0], ".fadeOut must be between 0.1 and 30 seconds") {
		t.Errorf("transform with a too-short fade out = %v, want the range error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{FadeOut: 2, FadeTarget: "music"}); len(errors) == 0 || !strings.Contains(errors[0], `.fadeTarget must be "both", "video", or "audio"`) {
		t.Errorf("transform with a bad fade target = %v, want the enum error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{Width: 1280, FadeTarget: "video"}); len(errors) == 0 || !strings.Contains(errors[0], ".fadeTarget is only valid with fadeIn or fadeOut") {
		t.Errorf("transform with a fade target but no fade = %v, want the needs-a-fade error", errors)
	}
	if errors := transform.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], "fadeIn, or fadeOut") {
		t.Errorf("transform without ops = %v, want the at-least-one error to name the fades", errors)
	}

	transformAudio := transformAudioToolDefinition()
	if errors := transformAudio.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) == 0 || !strings.Contains(errors[0], ".transform_audio needs fadeIn, fadeOut, or both") {
		t.Errorf("transform_audio without fades = %v, want the needs-a-fade error", errors)
	}
	if errors := transformAudio.Validate("toolCalls[0]", HarnessToolCall{FadeOut: 3}); len(errors) != 0 {
		t.Errorf("valid transform_audio fade out = %v, want none", errors)
	}
	if errors := transformAudio.Validate("toolCalls[0]", HarnessToolCall{FadeIn: 0.5, FadeOut: 3}); len(errors) != 0 {
		t.Errorf("valid transform_audio fade pair = %v, want none", errors)
	}
	if errors := transformAudio.Validate("toolCalls[0]", HarnessToolCall{FadeIn: 31}); len(errors) == 0 || !strings.Contains(errors[0], ".fadeIn must be between 0.1 and 30 seconds for transform_audio") {
		t.Errorf("transform_audio with a too-long fade = %v, want the range error", errors)
	}
	if errors := transformAudio.Validate("toolCalls[0]", HarnessToolCall{FadeOut: 2, FadeTarget: "audio"}); len(errors) == 0 || !strings.Contains(errors[0], ".fadeTarget is transform_video's") {
		t.Errorf("transform_audio with a fade target = %v, want the not-here error", errors)
	}

	reverse := reverseVideoToolDefinition()
	if errors := reverse.Validate("toolCalls[0]", HarnessToolCall{}); len(errors) != 0 {
		t.Errorf("valid default reverse = %v, want none", errors)
	}
	if errors := reverse.Validate("toolCalls[0]", HarnessToolCall{Mode: "mute"}); len(errors) != 0 {
		t.Errorf("valid mute reverse = %v, want none", errors)
	}
	if errors := reverse.Validate("toolCalls[0]", HarnessToolCall{Mode: "louder"}); len(errors) == 0 || !strings.Contains(errors[0], `.mode must be "sound" or "mute"`) {
		t.Errorf("reverse with a bad mode = %v, want the mode enum error", errors)
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

// TestScreenshotDescriptionTeachesEndToken pins the planner-facing contract
// conv_defa533b380d7131939a59b4 left on the tool: "last frame" asks route to
// at "end" (the count form's count=1 lands at t=0 — the FIRST frame), and a
// past-end timestamp falls back to the final frame rather than failing.
func TestScreenshotDescriptionTeachesEndToken(t *testing.T) {
	desc := screenshotVideoToolDefinition().Description
	for _, fragment := range []string{`"end"`, "final frame", "last frame"} {
		if !strings.Contains(desc, fragment) {
			t.Fatalf("screenshot_video description = %q, want it to include %q", desc, fragment)
		}
	}
}

// TestContactSheetValidatesEndToken pins that the shared end-token grammar
// reaches the contact sheet's `at` too.
func TestContactSheetValidatesEndToken(t *testing.T) {
	sheet := contactSheetVideoToolDefinition()
	if errors := sheet.Validate("toolCalls[0]", HarnessToolCall{At: "end"}); len(errors) != 0 {
		t.Errorf("contact sheet with at end = %v, want none", errors)
	}
	if errors := sheet.Validate("toolCalls[0]", HarnessToolCall{At: "0,end"}); len(errors) != 0 {
		t.Errorf("contact sheet with an at list containing end = %v, want none", errors)
	}
	if errors := sheet.Validate("toolCalls[0]", HarnessToolCall{At: "endx"}); len(errors) == 0 || !strings.Contains(errors[0], ".at must be a timestamp") {
		t.Errorf("contact sheet with endx = %v, want the timestamp error", errors)
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

// TestFFmpegScreenshotPastEndFallsBackToFinalFrame pins conv_defa533b's fix:
// "capture the frame at 25.71" against a 25.708s clip — the quoted container
// duration sits past the last video frame, so the plain seek decodes nothing —
// must land on the clip's final frame with a substitution notice, not fail.
func TestFFmpegScreenshotPastEndFallsBackToFinalFrame(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegNoMidSeekScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{At: "25.71"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok || typed.Count != 1 || len(typed.Images) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	if len(typed.Notices) != 1 || !strings.Contains(typed.Notices[0], "final frame was captured instead") {
		t.Fatalf("notices = %v, want the final-frame substitution", typed.Notices)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-sseof -1") || !strings.Contains(args, "reverse") {
		t.Fatalf("ffmpeg args = %q, want the end-relative reverse grab", args)
	}
}

// TestFFmpegScreenshotEndToken pins the explicit form: at "end" is the
// planner's affordance for "last frame screenshot" asks (the count form's
// count=1 lands at t=0) — one end-relative grab, the final-frame prompt and
// summary, and no past-end notice, because the substitution IS the requested
// semantics.
func TestFFmpegScreenshotEndToken(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegNoMidSeekScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{At: "end"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok || typed.Count != 1 || len(typed.Images) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	if len(typed.Notices) != 0 {
		t.Fatalf("notices = %v, want none (end is the requested semantics)", typed.Notices)
	}
	if typed.Prompt != "the clip's final frame" {
		t.Fatalf("prompt = %q, want the final-frame wording", typed.Prompt)
	}
	if !strings.Contains(result.Summary, "final frame") {
		t.Fatalf("summary = %q, want the final-frame wording", result.Summary)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-sseof -1") || strings.Contains(args, "-ss ") {
		t.Fatalf("ffmpeg args = %q, want exactly one end-relative grab and no plain seek", args)
	}
}

// TestFFmpegScreenshotBatchMixedTimestamps pins the batch path: a live
// timestamp and a past-end one in one call capture BOTH frames — the past-end
// one via the final-frame substitution, with its notice — rather than
// dropping a frame.
func TestFFmpegScreenshotBatchMixedTimestamps(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegNoMidSeekScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{At: "0,25.71"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok || typed.Count != 2 || len(typed.Images) != 2 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	if len(typed.Notices) != 1 || !strings.Contains(typed.Notices[0], "no frame exists at 25.71") {
		t.Fatalf("notices = %v, want the 25.71 substitution", typed.Notices)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-ss 0") || !strings.Contains(args, "-ss 25.71") || !strings.Contains(args, "-sseof -1") {
		t.Fatalf("ffmpeg args = %q, want the plain seeks and the end-relative grab", args)
	}
}

// TestFFmpegScreenshotFallbackFailureKeepsSeekError pins the error contract:
// when even the end-relative grab fails (a corrupt source, say), the reported
// error is the ORIGINAL seek failure — it names the timestamp behavior the
// user asked about — never the fallback's own.
func TestFFmpegScreenshotFallbackFailureKeepsSeekError(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegAlwaysFailsScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "screenshot_video", HarnessToolCall{At: "25.71"})
	if result.Status == "completed" {
		t.Fatalf("result = %+v, want failure", result)
	}
	if !strings.Contains(result.Error, "no filtered frames for output stream") {
		t.Fatalf("error = %q, want the original seek failure", result.Error)
	}
	if strings.Contains(result.Error, "end-relative grab failed") {
		t.Fatalf("error = %q, the fallback error must not replace the seek error", result.Error)
	}
}

// TestContactSheetGrid pins the montage grid math: an omitted column count
// picks the near-square ceil(√n); an explicit one wins, and rows always cover
// every frame.
func TestContactSheetGrid(t *testing.T) {
	cases := []struct {
		count             int
		columns           int
		wantCols, wantRow int
	}{
		{8, 0, 3, 3},
		{9, 0, 3, 3},
		{10, 0, 4, 3},
		{16, 0, 4, 4},
		{1, 0, 1, 1},
		{7, 2, 2, 4},
		{3, 5, 5, 1},
		{4, -2, 2, 2}, // a negative column count means "derive"
	}
	for _, tc := range cases {
		cols, rows := contactSheetGrid(tc.count, tc.columns)
		if cols != tc.wantCols || rows != tc.wantRow {
			t.Errorf("contactSheetGrid(%d, %d) = %dx%d, want %dx%d", tc.count, tc.columns, cols, rows, tc.wantCols, tc.wantRow)
		}
	}
}

// TestContactSheetTimestamps pins the equal-interval sampling: the first frame
// lands 10% into the clip (AI clips often open on a black frame) and the last
// never at the clip's very end.
func TestContactSheetTimestamps(t *testing.T) {
	timestamps := contactSheetTimestamps(12.5, 8)
	if len(timestamps) != 8 {
		t.Fatalf("timestamps = %v, want 8 entries", timestamps)
	}
	first, err := strconv.ParseFloat(timestamps[0], 64)
	if err != nil || math.Abs(first-1.25) > 1e-9 {
		t.Errorf("first timestamp = %v, want 1.25 (10%% of 12.5)", timestamps[0])
	}
	last, err := strconv.ParseFloat(timestamps[7], 64)
	if err != nil || math.Abs(last-11.09375) > 1e-9 {
		t.Errorf("last timestamp = %v, want 11.09375", timestamps[7])
	}
	if last >= 12.5 {
		t.Errorf("last timestamp %v must stay inside the clip", timestamps[7])
	}
	single := contactSheetTimestamps(12.5, 1)
	if value, _ := strconv.ParseFloat(single[0], 64); math.Abs(value-1.25) > 1e-9 {
		t.Errorf("single-frame timestamp = %v, want 1.25", single[0])
	}
}

// fakeFFmpegSkipOddScript fakes an ffmpeg whose odd-numbered capture outputs
// never land (frame_01.jpg, frame_03.jpg, …): it drives contact_sheet_video's
// missed-frame path — the survivors are renumbered into a gapless sequence and
// the grid is recomputed from them.
const fakeFFmpegSkipOddScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" >> "$dir/args.txt"
prev=""
out=""
for a in "$@"; do
  prev="$a"
  out="$a"
done
case "$out" in
  */frame_01.jpg|*/frame_03.jpg) exit 0 ;;
esac
printf '\377\330\377FAKE-MEDIA' > "$out"
`

func TestFFmpegContactSheetExecutes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "contact_sheet_video", HarnessToolCall{Count: 4})
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
	// Four frames at equal intervals over the fake probe's 12.5s, sampling
	// from 10% in: the first seek lands at 1.25.
	if !strings.Contains(args, "-ss 1.25") {
		t.Fatalf("ffmpeg args = %q, want the first -ss seek at 1.25", args)
	}
	if strings.Count(args, "-ss ") != 4 {
		t.Errorf("ffmpeg args = %q, want 4 capture seeks", args)
	}
	if !strings.Contains(args, "scale=480:-2,tile=2x2:margin=8:padding=4:color=black") {
		t.Errorf("ffmpeg args = %q, want the 2x2 tile pass at the cell width", args)
	}
	if !strings.Contains(args, "-start_number 0") || !strings.Contains(args, "tile_%03d.jpg") {
		t.Errorf("ffmpeg args = %q, want the gapless tile sequence input", args)
	}
	if !strings.Contains(result.Summary, "2x2 contact sheet of 4 evenly spaced frames") {
		t.Errorf("summary = %q, want the grid phrase", result.Summary)
	}
}

// TestFFmpegContactSheetDefaultsToEightFrames pins the zero-param form: a bare
// "make a contact sheet" samples 8 frames into a near-square 3x3 grid.
func TestFFmpegContactSheetDefaultsToEightFrames(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "contact_sheet_video", HarnessToolCall{})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if strings.Count(args, "-ss ") != 8 {
		t.Errorf("ffmpeg args = %q, want 8 capture seeks", args)
	}
	if !strings.Contains(args, "tile=3x3") {
		t.Errorf("ffmpeg args = %q, want the default 3x3 grid", args)
	}
}

func TestFFmpegContactSheetExplicitTimestamps(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "contact_sheet_video", HarnessToolCall{At: "2,4,6"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	for _, want := range []string{"-ss 2", "-ss 4", "-ss 6"} {
		if !strings.Contains(args, want) {
			t.Fatalf("ffmpeg args = %q, want %q", args, want)
		}
	}
	// Three explicit frames and no column override: near-square 2x2.
	if !strings.Contains(args, "tile=2x2") {
		t.Errorf("ffmpeg args = %q, want the 2x2 grid", args)
	}
	if !strings.Contains(result.Summary, "frames at 2, 4, 6") {
		t.Errorf("summary = %q, want the timestamp phrase", result.Summary)
	}
}

// TestFFmpegContactSheetColumns pins the explicit column override: 6 frames at
// 2 columns tile into a 2x3 grid.
func TestFFmpegContactSheetColumns(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "contact_sheet_video", HarnessToolCall{Count: 6, Columns: 2})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "tile=2x3") {
		t.Errorf("ffmpeg args = %q, want the 2x3 grid", args)
	}
}

// TestFFmpegContactSheetSkipsMissedFrames pins the fail-soft capture: two of
// four timestamps produce no file, the survivors are renumbered into a gapless
// sequence, the grid is recomputed (2x1), and the misses are noticed.
func TestFFmpegContactSheetSkipsMissedFrames(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegSkipOddScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "contact_sheet_video", HarnessToolCall{Count: 4})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok || typed.Count != 1 || len(typed.Images) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	if len(typed.Notices) != 2 {
		t.Fatalf("notices = %v, want one per missed frame", typed.Notices)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "tile=2x1") {
		t.Errorf("ffmpeg args = %q, want the recomputed 2x1 grid", args)
	}
}

func TestFFmpegContactSheetRequiresVideo(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{}, "contact_sheet_video", HarnessToolCall{Count: 4})
	if result.Status == "completed" || !strings.Contains(result.Error, "requires an attached video clip") {
		t.Fatalf("result = %+v, want the attachment error", result)
	}
}

// TestFFmpegContactSheetDurationUnavailable pins the planner-facing repair
// hint: without a readable duration, the error names contact_sheet_video and
// points at its own explicit-at escape (never screenshot_video's).
func TestFFmpegContactSheetDurationUnavailable(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE")},
	}, "contact_sheet_video", HarnessToolCall{Count: 4})
	if result.Status == "completed" {
		t.Fatalf("result = %+v, want the duration error", result)
	}
	for _, fragment := range []string{"contact_sheet_video could not read the clip's duration", `{"name":"contact_sheet_video","at":"0,30,60"}`} {
		if !strings.Contains(result.Error, fragment) {
			t.Errorf("error = %q, want it to include %q", result.Error, fragment)
		}
	}
}

// TestContactSheetValidation pins contact_sheet_video's per-tool validation:
// the timestamp grammar, the 16-frame cap shared with screenshot_video, and
// the column bounds. A call with neither at nor count is valid (the default
// count applies).
func TestContactSheetValidation(t *testing.T) {
	cases := []struct {
		name     string
		call     HarnessToolCall
		fragment string
	}{
		{"at needs a timestamp", HarnessToolCall{At: "abc"}, ".at must be a timestamp"},
		{"at list validated per token", HarnessToolCall{At: "1,2,abc"}, ".at must be a timestamp"},
		{"at capped", HarnessToolCall{At: "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17"}, ".at lists 17"},
		{"count capped", HarnessToolCall{Count: 17}, ".count must be between 1 and 16"},
		{"count non-negative", HarnessToolCall{Count: -1}, ".count must be between 1 and 16"},
		{"columns capped", HarnessToolCall{Columns: 17}, ".columns must be between 1 and 16"},
		{"columns non-negative", HarnessToolCall{Columns: -3}, ".columns must be between 1 and 16"},
	}
	for _, tc := range cases {
		errors := contactSheetVideoToolDefinition().Validate("toolCalls[0]", tc.call)
		if len(errors) == 0 || !strings.Contains(errors[0], tc.fragment) {
			t.Errorf("%s: validate(%+v) = %v, want %q", tc.name, tc.call, errors, tc.fragment)
		}
	}
	for _, tc := range []struct {
		name string
		call HarnessToolCall
	}{
		{"default count", HarnessToolCall{}},
		{"count at the cap", HarnessToolCall{Count: 16}},
		{"explicit at", HarnessToolCall{At: "4.5"}},
		{"clock at", HarnessToolCall{At: "00:01:30"}},
		{"count plus columns", HarnessToolCall{Count: 4, Columns: 2}},
	} {
		if errors := contactSheetVideoToolDefinition().Validate("toolCalls[0]", tc.call); len(errors) != 0 {
			t.Errorf("%s: validate(%+v) = %v, want none", tc.name, tc.call, errors)
		}
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

// TestFFmpegRemoveAudioExecutes pins the mute path: a pinned stream copy that
// drops the audio (no re-encode), the source container mirrored onto the
// output, the silent-source notice, and the required-video error.
func TestFFmpegRemoveAudioExecutes(t *testing.T) {
	t.Run("stream copy drops the audio", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "remove_audio", HarnessToolCall{})
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
		if len(result.Notices) != 0 {
			t.Fatalf("notices = %v, want none for a clip with audio", result.Notices)
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-map 0:v:0") || !strings.Contains(args, "-c:v copy") || !strings.Contains(args, "-an ") || strings.Contains(args, "libx264") {
			t.Fatalf("remove args = %q, want a pinned stream copy with -an", args)
		}
	})
	t.Run("mirrors a webm source container", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		webm := "data:video/webm;base64," + base64.StdEncoding.EncodeToString([]byte("CLIP-WEBM"))
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{webm},
		}, "remove_audio", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if typed.Videos[0].MimeType != "video/webm" || !strings.HasSuffix(typed.Videos[0].TempPath, ".webm") {
			t.Fatalf("output = %s (%s), want the source's webm container", typed.Videos[0].TempPath, typed.Videos[0].MimeType)
		}
	})
	t.Run("a silent source rides a notice", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeNoAudioScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "remove_audio", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "no audio track") {
			t.Fatalf("notices = %v, want the already-silent notice", result.Notices)
		}
	})
	t.Run("requires an attached video", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{}, "remove_audio", HarnessToolCall{})
		if result.Status == "completed" || !strings.Contains(result.Error, "attached video clip") {
			t.Fatalf("result = %+v, want the attached-video error", result)
		}
	})
}

// TestFFmpegReverseVideoExecutes pins the reverse path: a pinned H.264
// re-encode whose picture and audio both play backwards, the mute variant's
// -an, the silent-source notice, the decoded-frame memory refusal, the
// fail-closed unmeasurable-clip error, and the required-video error.
func TestFFmpegReverseVideoExecutes(t *testing.T) {
	t.Run("reverses picture and audio by default", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "reverse_video", HarnessToolCall{})
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
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vf reverse") || !strings.Contains(args, "-af areverse") || !strings.Contains(args, "libx264") || strings.Contains(args, "-an ") {
			t.Fatalf("reverse args = %q, want a pinned re-encode with reversed audio", args)
		}
	})
	t.Run("mute mode strips the audio", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "reverse_video", HarnessToolCall{Mode: "mute"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-an ") || strings.Contains(args, "areverse") {
			t.Fatalf("mute args = %q, want -an and no areverse", args)
		}
		if len(result.Notices) < 2 || !strings.Contains(result.Notices[1], "mode \"mute\"") {
			t.Fatalf("notices = %v, want the mute notice", result.Notices)
		}
	})
	t.Run("a silent source rides a notice", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeNoAudioScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "reverse_video", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if len(result.Notices) < 2 || !strings.Contains(result.Notices[1], "no audio track") {
			t.Fatalf("notices = %v, want the silent-source notice", result.Notices)
		}
	})
	t.Run("a clip beyond the memory budget is refused", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeLongClipScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "reverse_video", HarnessToolCall{})
		if result.Status == "completed" || !strings.Contains(result.Error, "split_video") || !strings.Contains(result.Error, "memory") {
			t.Fatalf("result = %+v, want the memory-budget refusal naming split_video", result)
		}
	})
	t.Run("an unmeasurable clip fails closed", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "reverse_video", HarnessToolCall{})
		if result.Status == "completed" || !strings.Contains(result.Error, "could not measure") {
			t.Fatalf("result = %+v, want the unmeasurable-clip error", result)
		}
	})
	t.Run("requires an attached video", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{}, "reverse_video", HarnessToolCall{})
		if result.Status == "completed" || !strings.Contains(result.Error, "attached video clip") {
			t.Fatalf("result = %+v, want the attached-video error", result)
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

// TestSplitAudioTarget pins the container mapping: a known attachment media
// type stream-copies into its own container (every WAV spelling collapses to
// .wav), and an unrecognized type falls back to MP3 re-encoding with a notice.
func TestSplitAudioTarget(t *testing.T) {
	cases := []struct {
		mediaType string
		wantExt   string
		wantMime  string
		wantCopy  bool
	}{
		{"audio/mpeg", ".mp3", "audio/mpeg", true},
		{"audio/wav", ".wav", "audio/wav", true},
		{"audio/x-wav", ".wav", "audio/wav", true},
		{"audio/wave", ".wav", "audio/wav", true},
		{"audio/vnd.wave", ".wav", "audio/wav", true},
		{"audio/mp4", ".m4a", "audio/mp4", true},
		{"audio/x-m4a", ".m4a", "audio/mp4", true},
		{"audio/aac", ".m4a", "audio/mp4", true},
		{"audio/flac", ".flac", "audio/flac", true},
		{"audio/x-flac", ".flac", "audio/flac", true},
		{"audio/ogg", ".ogg", "audio/ogg", true},
		{"audio/opus", ".ogg", "audio/ogg", true},
		{"audio/vorbis", ".ogg", "audio/ogg", true},
		{"audio/amr", ".mp3", "audio/mpeg", false},
		{"", ".mp3", "audio/mpeg", false},
	}
	for _, tc := range cases {
		ext, mime, codecArgs, notice := splitAudioTarget(tc.mediaType)
		if ext != tc.wantExt || mime != tc.wantMime {
			t.Errorf("splitAudioTarget(%q) = %s/%s, want %s/%s", tc.mediaType, ext, mime, tc.wantExt, tc.wantMime)
		}
		if tc.wantCopy && len(codecArgs) != 0 {
			t.Errorf("splitAudioTarget(%q) re-encodes, want a stream copy", tc.mediaType)
		}
		if !tc.wantCopy && len(codecArgs) == 0 {
			t.Errorf("splitAudioTarget(%q) copies, want the MP3 fallback", tc.mediaType)
		}
		if !tc.wantCopy && !strings.Contains(notice, "converted to MP3") {
			t.Errorf("splitAudioTarget(%q) notice = %q, want the conversion explanation", tc.mediaType, notice)
		}
		if tc.wantCopy && notice != "" {
			t.Errorf("splitAudioTarget(%q) notice = %q, want none", tc.mediaType, notice)
		}
	}
}

// TestConvertAudioTarget pins the format mapping and the quality→bitrate
// tiers: the lossy formats carry their tier's bitrate (omitted quality lands
// on the house 192k default), the lossless ones take no bitrate at all.
func TestConvertAudioTarget(t *testing.T) {
	cases := []struct {
		format   string
		quality  int
		wantExt  string
		wantMime string
		wantArgs string
	}{
		{"mp3", 0, ".mp3", "audio/mpeg", "-c:a libmp3lame -b:a 192k"},
		{"mp3", 95, ".mp3", "audio/mpeg", "-c:a libmp3lame -b:a 320k"},
		{"mp3", 70, ".mp3", "audio/mpeg", "-c:a libmp3lame -b:a 256k"},
		{"mp3", 40, ".mp3", "audio/mpeg", "-c:a libmp3lame -b:a 192k"},
		{"mp3", 10, ".mp3", "audio/mpeg", "-c:a libmp3lame -b:a 128k"},
		{"m4a", 0, ".m4a", "audio/mp4", "-c:a aac -b:a 192k"},
		{"m4a", 95, ".m4a", "audio/mp4", "-c:a aac -b:a 320k"},
		{"ogg", 0, ".ogg", "audio/ogg", "-c:a libvorbis -b:a 192k"},
		{"ogg", 10, ".ogg", "audio/ogg", "-c:a libvorbis -b:a 128k"},
		{"wav", 0, ".wav", "audio/wav", "-c:a pcm_s16le"},
		{"flac", 0, ".flac", "audio/flac", "-c:a flac"},
	}
	for _, tc := range cases {
		ext, mime, codecArgs := convertAudioTarget(tc.format, tc.quality)
		if ext != tc.wantExt || mime != tc.wantMime {
			t.Errorf("convertAudioTarget(%q, %d) = %s/%s, want %s/%s", tc.format, tc.quality, ext, mime, tc.wantExt, tc.wantMime)
		}
		if got := strings.Join(codecArgs, " "); got != tc.wantArgs {
			t.Errorf("convertAudioTarget(%q, %d) args = %q, want %q", tc.format, tc.quality, got, tc.wantArgs)
		}
	}
}

// TestConvertAudioTargetPhrase pins the evidence phrasing: lossy targets name
// their bitrate tier, the lossless ones stay bare.
func TestConvertAudioTargetPhrase(t *testing.T) {
	if got := convertAudioTargetPhrase("mp3", 0); got != "mp3 (192 kbps)" {
		t.Errorf("convertAudioTargetPhrase(mp3, 0) = %q, want \"mp3 (192 kbps)\"", got)
	}
	if got := convertAudioTargetPhrase("mp3", 95); got != "mp3 (320 kbps)" {
		t.Errorf("convertAudioTargetPhrase(mp3, 95) = %q, want \"mp3 (320 kbps)\"", got)
	}
	if got := convertAudioTargetPhrase("wav", 0); got != "wav" {
		t.Errorf("convertAudioTargetPhrase(wav, 0) = %q, want \"wav\"", got)
	}
}

// TestFFmpegConvertAudioExecutes drives convert_audio against the fake ffmpeg:
// the whole file transcodes under the requested format's codec args (-vn
// always, bitrate per the quality tier), the output rides ToolAudioResult with
// the target container's MIME, and the summary names format and bitrate.
func TestFFmpegConvertAudioExecutes(t *testing.T) {
	t.Run("m4a to mp3 at the default bitrate", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/mp4", "MEMO")},
		}, "convert_audio", HarnessToolCall{Format: "mp3"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, ok := result.Result.(ToolAudioResult)
		if !ok || typed.Model != ffmpegModelName || len(typed.Audios) != 1 {
			t.Fatalf("result payload = %+v", result.Result)
		}
		defer os.Remove(typed.Audios[0].TempPath)
		if typed.Audios[0].MimeType != "audio/mpeg" {
			t.Fatalf("mimeType = %q, want audio/mpeg", typed.Audios[0].MimeType)
		}
		data, err := os.ReadFile(typed.Audios[0].TempPath)
		if err != nil || !strings.HasPrefix(string(data), "\xff\xd8\xffFAKE-MEDIA") {
			t.Fatalf("output file = %q (%v), want the fake ffmpeg payload", data, err)
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vn -c:a libmp3lame -b:a 192k") {
			t.Fatalf("convert args = %q, want the default-bitrate MP3 re-encode with -vn", args)
		}
		if !strings.Contains(result.Summary, "converted the attached audio to mp3 (192 kbps)") {
			t.Fatalf("summary = %q, want the format+bitrate phrase", result.Summary)
		}
	})
	t.Run("quality rides as the bitrate tier", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/x-m4a", "MEMO")},
		}, "convert_audio", HarnessToolCall{Format: "MP3", Quality: 95})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-c:a libmp3lame -b:a 320k") {
			t.Fatalf("convert args = %q, want the 320k tier for quality 95", args)
		}
	})
	t.Run("lossless flac takes no bitrate", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/mpeg", "SONG")},
		}, "convert_audio", HarnessToolCall{Format: "flac"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolAudioResult)
		defer os.Remove(typed.Audios[0].TempPath)
		if typed.Audios[0].MimeType != "audio/flac" {
			t.Fatalf("mimeType = %q, want audio/flac", typed.Audios[0].MimeType)
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vn -c:a flac") || strings.Contains(args, "-b:a") {
			t.Fatalf("convert args = %q, want a bare flac encode with no bitrate", args)
		}
	})
	t.Run("needs an attached audio file", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{""},
		}, "convert_audio", HarnessToolCall{Format: "mp3"})
		if result.Status == "completed" || !strings.Contains(result.Error, "requires an attached audio file") {
			t.Fatalf("result = %+v, want the attachment error", result)
		}
	})
}

// TestJoinAudioReencodeTarget pins the first-clip-codec normalization: a known
// codec re-encodes into its own container, any pcm_* variant lands on WAV, and
// an unrecognized or missing codec falls back to MP3 with a notice.
func TestJoinAudioReencodeTarget(t *testing.T) {
	cases := []struct {
		codec    string
		wantExt  string
		wantMime string
		wantArgs string
	}{
		{"pcm_s16le", ".wav", "audio/wav", "-c:a pcm_s16le"},
		{"pcm_f32le", ".wav", "audio/wav", "-c:a pcm_s16le"},
		{"mp3", ".mp3", "audio/mpeg", "-c:a libmp3lame -b:a 192k"},
		{"aac", ".m4a", "audio/mp4", "-c:a aac -b:a 192k"},
		{"flac", ".flac", "audio/flac", "-c:a flac"},
		{"opus", ".ogg", "audio/ogg", "-c:a libopus -b:a 192k"},
		{"vorbis", ".ogg", "audio/ogg", "-c:a libvorbis -b:a 192k"},
	}
	for _, tc := range cases {
		ext, mime, codecArgs, notice := joinAudioReencodeTarget(ToolProbeResult{AudioCodec: tc.codec})
		if ext != tc.wantExt || mime != tc.wantMime {
			t.Errorf("joinAudioReencodeTarget(%q) = %s/%s, want %s/%s", tc.codec, ext, mime, tc.wantExt, tc.wantMime)
		}
		if notice != "" {
			t.Errorf("joinAudioReencodeTarget(%q) notice = %q, want none", tc.codec, notice)
		}
		if got := strings.Join(codecArgs, " "); got != tc.wantArgs {
			t.Errorf("joinAudioReencodeTarget(%q) args = %q, want %q", tc.codec, got, tc.wantArgs)
		}
	}
	for _, codec := range []string{"", "amr", "alac"} {
		ext, mime, codecArgs, notice := joinAudioReencodeTarget(ToolProbeResult{AudioCodec: codec})
		if ext != ".mp3" || mime != "audio/mpeg" {
			t.Errorf("joinAudioReencodeTarget(%q) = %s/%s, want the MP3 fallback", codec, ext, mime)
		}
		if got := strings.Join(codecArgs, " "); got != "-c:a libmp3lame -b:a 192k" {
			t.Errorf("joinAudioReencodeTarget(%q) args = %q, want the MP3 fallback", codec, got)
		}
		if !strings.Contains(notice, "converted to MP3") {
			t.Errorf("joinAudioReencodeTarget(%q) notice = %q, want the conversion explanation", codec, notice)
		}
	}
}

// TestConcatCompatibleAudio pins the copy-join gate: codec, sample rate, and
// channel count must all match, and a probe without an audio stream never
// copies.
func TestConcatCompatibleAudio(t *testing.T) {
	base := ToolProbeResult{AudioCodec: "mp3", SampleRate: 44100, Channels: 2}
	if !concatCompatibleAudio(base, base) {
		t.Errorf("identical probes should be compatible")
	}
	for name, mutate := range map[string]func(*ToolProbeResult){
		"codec differs":       func(p *ToolProbeResult) { p.AudioCodec = "aac" },
		"sample rate differs": func(p *ToolProbeResult) { p.SampleRate = 48000 },
		"channels differ":     func(p *ToolProbeResult) { p.Channels = 1 },
		"no audio stream":     func(p *ToolProbeResult) { p.AudioCodec = "" },
	} {
		mutated := base
		mutate(&mutated)
		if concatCompatibleAudio(base, mutated) {
			t.Errorf("%s should be incompatible", name)
		}
	}
}

func TestFFmpegSplitAudioExecutes(t *testing.T) {
	t.Run("wav copies with no re-encode", func(t *testing.T) {
		// No ffprobe configured on purpose: split_audio never probes.
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, "")
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{wavDataURL()},
		}, "split_audio", HarnessToolCall{Start: "10", End: "25"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, ok := result.Result.(ToolAudioResult)
		if !ok || typed.Model != ffmpegModelName || len(typed.Audios) != 1 {
			t.Fatalf("result payload = %+v", result.Result)
		}
		defer os.Remove(typed.Audios[0].TempPath)
		if !strings.HasSuffix(typed.Audios[0].TempPath, ".wav") || typed.Audios[0].MimeType != "audio/wav" {
			t.Fatalf("audio file = %+v, want a copied .wav", typed.Audios[0])
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-ss 10 -t 15") || !strings.Contains(args, "-vn -c:a copy") || strings.Contains(args, "libmp3lame") {
			t.Fatalf("split args = %q, want an output-seeked stream copy", args)
		}
		if len(result.Notices) != 0 {
			t.Fatalf("notices = %v, want none for a copy", result.Notices)
		}
	})
	t.Run("unbounded call keeps the whole file", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, "")
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{wavDataURL()},
		}, "split_audio", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolAudioResult)
		defer os.Remove(typed.Audios[0].TempPath)
		if args := fakeFFmpegArgs(t, bin); strings.Contains(args, "-ss ") || strings.Contains(args, "-t ") {
			t.Fatalf("split args = %q, want no seek or duration", args)
		}
	})
	t.Run("unidentified format falls back to mp3 with a notice", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, "")
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/amr", "AMR-FAKE")},
		}, "split_audio", HarnessToolCall{Start: "5"})
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
	t.Run("requires an attached audio clip", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, "")
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{}, "split_audio", HarnessToolCall{})
		if result.Status == "completed" || !strings.Contains(result.Error, "requires an attached audio clip") {
			t.Fatalf("result = %+v, want the attachment error", result)
		}
	})
}

func TestFFmpegJoinAudiosExecutes(t *testing.T) {
	t.Run("matching clips stream-copy in attachment order", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{wavDataURL(), wavDataURL()},
		}, "join_audios", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, ok := result.Result.(ToolAudioResult)
		if !ok || len(typed.Audios) != 1 || typed.Audios[0].MimeType != "audio/wav" {
			t.Fatalf("result payload = %+v", result.Result)
		}
		defer os.Remove(typed.Audios[0].TempPath)
		if !strings.HasSuffix(typed.Audios[0].TempPath, ".wav") {
			t.Fatalf("audio file = %q, want the first clip's .wav container", typed.Audios[0].TempPath)
		}
		list, err := os.ReadFile(filepath.Join(bin, "concat-list.txt"))
		if err != nil {
			t.Fatalf("the fake ffmpeg never captured a concat list: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(list)), "\n")
		if len(lines) != 2 || !strings.Contains(lines[0], "input-001.wav") || !strings.Contains(lines[1], "input-002.wav") {
			t.Fatalf("concat order = %q, want attachment order (001.wav before 002.wav)", list)
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vn -c:a copy") || strings.Contains(args, "libmp3lame") {
			t.Fatalf("join args = %q, want a stream copy for matching clips", args)
		}
		if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "stream copy") {
			t.Fatalf("notices = %v, want the copy explanation", result.Notices)
		}
	})
	t.Run("needs at least two clips", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{wavDataURL()},
		}, "join_audios", HarnessToolCall{})
		if result.Status == "completed" || !strings.Contains(result.Error, "at least two") {
			t.Fatalf("result = %+v, want the two-clip error", result)
		}
	})
	t.Run("when probing fails re-encodes to the MP3 fallback", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{wavDataURL(), wavDataURL()},
		}, "join_audios", HarnessToolCall{})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed := result.Result.(ToolAudioResult)
		defer os.Remove(typed.Audios[0].TempPath)
		if !strings.HasSuffix(typed.Audios[0].TempPath, ".mp3") || typed.Audios[0].MimeType != "audio/mpeg" {
			t.Fatalf("audio file = %+v, want the MP3 fallback", typed.Audios[0])
		}
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-c:a libmp3lame -b:a 192k") {
			t.Fatalf("join args = %q, want the MP3 re-encode", args)
		}
		if len(result.Notices) == 0 || !strings.Contains(result.Notices[0], "could not be compared") {
			t.Fatalf("notices = %v, want the probe-failure explanation", result.Notices)
		}
	})
}

// TestFFmpegAudioSiblingCrossReferences pins the planner-facing cross-links:
// the video split/join tools point at their audio siblings and vice versa, and
// split_audio carries split_video's one-segment-per-call contract.
func TestFFmpegAudioSiblingCrossReferences(t *testing.T) {
	if desc := splitVideoToolDefinition().Description; !strings.Contains(desc, "split_audio") {
		t.Errorf("split_video description should reference split_audio")
	}
	if desc := joinVideosToolDefinition().Description; !strings.Contains(desc, "join_audios") {
		t.Errorf("join_videos description should reference join_audios")
	}
	splitAudio := splitAudioToolDefinition().Description
	if !strings.Contains(splitAudio, "split_video") {
		t.Errorf("split_audio description should reference split_video")
	}
	for _, fragment := range []string{"ONE segment", "same plan"} {
		if !strings.Contains(splitAudio, fragment) {
			t.Errorf("split_audio description = %q, want it to include %q", splitAudio, fragment)
		}
	}
	if desc := joinAudiosToolDefinition().Description; !strings.Contains(desc, "join_videos") {
		t.Errorf("join_audios description should reference join_videos")
	}
	if desc := joinVideosToolDefinition().Description; !strings.Contains(desc, "list_video_transitions") {
		t.Errorf("join_videos description should reference list_video_transitions")
	}
	if desc := listVideoTransitionsToolDefinition().Description; !strings.Contains(desc, "join_videos") {
		t.Errorf("list_video_transitions description should reference join_videos")
	}
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

// TestFFmpegTransformFadesExecute drives transform_video's single-clip fades
// end to end: the fade stages land LAST on the chains and read the output
// timeline (the probed 12.5s clip, divided by any speed, or the portion
// arithmetic), fadeTarget narrows them, the clamp notices, a silent clip never
// yields a claimed sound fade, and a fade-out with nothing to time it against
// fails closed while a fade-in alone still runs.
func TestFFmpegTransformFadesExecute(t *testing.T) {
	t.Run("fade out to black and silence", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript) // duration 12.5, aac
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{FadeOut: 2})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vf fade=t=out:st=10.5:d=2 ") || !strings.Contains(args, "-af afade=t=out:st=10.5:d=2 ") {
			t.Fatalf("transform args = %q, want the fade-out on both chains starting at 10.5s", args)
		}
		if !strings.Contains(result.Summary, "faded out to black and silence over 2s") {
			t.Fatalf("summary = %q, want the fade op", result.Summary)
		}
		if !strings.Contains(typed.Notices[0], "fades cannot stream-copy") {
			t.Fatalf("notices = %v, want the re-encode notice to name fades", typed.Notices)
		}
	})

	t.Run("fade in the picture alone beside a speed change", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 2, FadeIn: 1, FadeTarget: "video"})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vf setpts=PTS/2,fade=t=in:st=0:d=1 ") || !strings.Contains(args, "-af atempo=2 ") || strings.Contains(args, "afade") {
			t.Fatalf("transform args = %q, want the fade after the rescale and no audio fade", args)
		}
		if !strings.Contains(result.Summary, "2x playback speed, faded in from black over 1s") {
			t.Fatalf("summary = %q, want the speed then the picture-only fade op", result.Summary)
		}
	})

	t.Run("fade out is timed on the sped-up output", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 2, FadeOut: 2})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		// 12.5s at 2x renders 6.25s; a 2s fade-out starts at 4.25s.
		args := fakeFFmpegArgs(t, bin)
		if !strings.Contains(args, "-vf setpts=PTS/2,fade=t=out:st=4.25:d=2 ") || !strings.Contains(args, "-af atempo=2,afade=t=out:st=4.25:d=2 ") {
			t.Fatalf("transform args = %q, want the fade-out placed against the 6.25s output", args)
		}
	})

	t.Run("portion speed with a fade rides the post-concat chains", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{Speed: 3, Start: "2", End: "4", FadeOut: 1})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		// Output = 2s head + 2s/3 middle + 8.5s tail = 11.1667s; the 1s
		// fade-out starts at 10.167s (millisecond precision).
		args := fakeFFmpegArgs(t, bin)
		for _, want := range []string{
			"concat=n=3:v=1:a=1[vcat][acat];[vcat]fade=t=out:st=10.167:d=1[vout];[acat]afade=t=out:st=10.167:d=1[aout]",
			"-map [vout] -map [aout]",
		} {
			if !strings.Contains(args, want) {
				t.Fatalf("transform args = %q, want %q", args, want)
			}
		}
		if !strings.Contains(result.Summary, "3x playback speed from 2s to 4s, faded out to black and silence over 1s") {
			t.Fatalf("summary = %q, want the portion op then the fade op", result.Summary)
		}
	})

	t.Run("a fade longer than half the clip is clamped with a notice", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{FadeOut: 10})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vf fade=t=out:st=6.25:d=6.25 ") {
			t.Fatalf("transform args = %q, want the fade-out clamped to 6.25s", args)
		}
		if !strings.Contains(strings.Join(typed.Notices, " "), "10s fade out is longer than half the 12.5s output, so it was shortened to 6.25s") {
			t.Fatalf("notices = %v, want the clamp notice", typed.Notices)
		}
	})

	t.Run("a silent clip fades the picture alone with a notice", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeNoAudioScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{FadeOut: 2})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vf fade=t=out:st=10.5:d=2 ") || strings.Contains(args, "-af") {
			t.Fatalf("transform args = %q, want the picture fade and no audio chain", args)
		}
		if !strings.Contains(strings.Join(typed.Notices, " "), "carries no audio track, so only the picture was faded") {
			t.Fatalf("notices = %v, want the silent-clip notice", typed.Notices)
		}
		if !strings.Contains(result.Summary, "faded out to black over 2s") || strings.Contains(result.Summary, "silence") {
			t.Fatalf("summary = %q, want a picture-only fade op", result.Summary)
		}
	})

	t.Run("an audio-only fade on a silent clip fails", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeNoAudioScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{FadeOut: 2, FadeTarget: "audio"})
		if result.Status != "failed" || !strings.Contains(result.Error, "has nothing to fade") {
			t.Fatalf("result = %+v (error %s), want the nothing-to-fade failure", result, result.Error)
		}
	})

	t.Run("fade out fails closed without a measurable duration", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		config := defaultAppConfig()
		ffmpegPath := writeFakeWhisper(t, bin, "ffmpeg", fakeFFmpegScript)
		stubLocalLookup(t, map[string]string{"ffmpeg": ffmpegPath, ffmpegPath: ffmpegPath})
		config.Providers.Local.FFmpeg.Binary = ffmpegPath
		// No ffprobe resolves and the ftyp-only fixture has no mvhd.
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{FadeOut: 2})
		if result.Status != "failed" || !strings.Contains(result.Error, "duration for the fade out") {
			t.Fatalf("result = %+v (error %s), want the unreadable-duration failure", result, result.Error)
		}
	})

	t.Run("fade in alone runs without a measurable duration", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		config := defaultAppConfig()
		ffmpegPath := writeFakeWhisper(t, bin, "ffmpeg", fakeFFmpegScript)
		stubLocalLookup(t, map[string]string{"ffmpeg": ffmpegPath, ffmpegPath: ffmpegPath})
		config.Providers.Local.FFmpeg.Binary = ffmpegPath
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedVideos: []string{videoDataURL("CLIP-ONE")},
		}, "transform_video", HarnessToolCall{FadeIn: 1})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolVideoResult)
		defer os.Remove(typed.Videos[0].TempPath)
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vf fade=t=in:st=0:d=1 ") || !strings.Contains(args, "-af afade=t=in:st=0:d=1 ") {
			t.Fatalf("transform args = %q, want the unclamped fade-in on both chains", args)
		}
	})
}

// TestFFmpegTransformAudioExecutes drives transform_audio end to end: the
// fades ride one -af chain timed against the probed 30s recording, the output
// keeps the recording's own codec (pcm → WAV), the clamp notices, an
// unmeasurable fade-out fails closed, and a fade-in alone on an unprobed file
// still runs — into the MP3 fallback with its notice.
func TestFFmpegTransformAudioExecutes(t *testing.T) {
	t.Run("fades a WAV in its own format", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAudioScript) // 30s pcm_s16le
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/wav", "WAV-ONE")},
		}, "transform_audio", HarnessToolCall{FadeIn: 1, FadeOut: 3})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, ok := result.Result.(ToolAudioResult)
		if !ok || typed.Model != ffmpegModelName || len(typed.Audios) != 1 {
			t.Fatalf("result payload = %+v", result.Result)
		}
		defer os.Remove(typed.Audios[0].TempPath)
		if typed.Audios[0].MimeType != "audio/wav" || !strings.HasSuffix(typed.Audios[0].TempPath, ".wav") {
			t.Fatalf("output = %+v, want a WAV", typed.Audios[0])
		}
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-vn -af afade=t=in:st=0:d=1,afade=t=out:st=27:d=3 -c:a pcm_s16le ") {
			t.Fatalf("transform args = %q, want both fades on one chain into PCM", args)
		}
		if !strings.Contains(result.Summary, "faded in from silence over 1s, faded out to silence over 3s") {
			t.Fatalf("summary = %q, want both fade ops", result.Summary)
		}
		if len(typed.Notices) != 0 {
			t.Fatalf("notices = %v, want none for a probed, in-range fade", typed.Notices)
		}
	})

	t.Run("a fade longer than half the recording is clamped", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAudioScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/wav", "WAV-ONE")},
		}, "transform_audio", HarnessToolCall{FadeOut: 20})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolAudioResult)
		defer os.Remove(typed.Audios[0].TempPath)
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-af afade=t=out:st=15:d=15 ") {
			t.Fatalf("transform args = %q, want the fade-out clamped to 15s", args)
		}
		if !strings.Contains(strings.Join(typed.Notices, " "), "20s fade out is longer than half the 30s output, so it was shortened to 15s") {
			t.Fatalf("notices = %v, want the clamp notice", typed.Notices)
		}
	})

	t.Run("fade out fails closed without a measurable duration", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/mpeg", "NOT-REAL-AUDIO")},
		}, "transform_audio", HarnessToolCall{FadeOut: 2})
		if result.Status != "failed" || !strings.Contains(result.Error, "could not read the attached recording's duration for the fade out") {
			t.Fatalf("result = %+v (error %s), want the unreadable-duration failure", result, result.Error)
		}
	})

	t.Run("fade in alone on an unprobed file lands on the MP3 fallback", func(t *testing.T) {
		config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
			AttachedAudios: []string{audioDataURL("audio/mpeg", "NOT-REAL-AUDIO")},
		}, "transform_audio", HarnessToolCall{FadeIn: 1})
		if result.Status != "completed" {
			t.Fatalf("result = %+v (error %s)", result, result.Error)
		}
		typed, _ := result.Result.(ToolAudioResult)
		defer os.Remove(typed.Audios[0].TempPath)
		if typed.Audios[0].MimeType != "audio/mpeg" || !strings.HasSuffix(typed.Audios[0].TempPath, ".mp3") {
			t.Fatalf("output = %+v, want the MP3 fallback", typed.Audios[0])
		}
		if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-af afade=t=in:st=0:d=1 -c:a libmp3lame -b:a 192k ") {
			t.Fatalf("transform args = %q, want the unclamped fade-in into MP3 192k", args)
		}
		if !strings.Contains(strings.Join(typed.Notices, " "), "codec could not be identified, so the faded recording was converted to MP3 192kbps") {
			t.Fatalf("notices = %v, want the fallback notice", typed.Notices)
		}
	})

	t.Run("requires an attached recording", func(t *testing.T) {
		config, _ := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAudioScript)
		result := executeFFmpegTool(t, config, HarnessToolExecutionContext{}, "transform_audio", HarnessToolCall{FadeOut: 2})
		if result.Status != "failed" || !strings.Contains(result.Error, "requires an attached audio file") {
			t.Fatalf("result = %+v (error %s), want the missing-attachment failure", result, result.Error)
		}
	})
}

// TestFFmpegRegistryGating pins the tool gates: nothing without a binary, the
// ffmpeg-backed edit tools (and list_video_transitions) with ffmpeg alone,
// plus probe_media only when ffprobe also resolves.
func TestFFmpegRegistryGating(t *testing.T) {
	newRegistryNames := func(t *testing.T, found map[string]string) []string {
		t.Helper()
		stubLocalLookup(t, found)
		return defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil).Names()
	}
	if names := newRegistryNames(t, map[string]string{}); containsString(names, "split_video") || containsString(names, "list_video_transitions") {
		t.Fatal("ffmpeg tools must be absent without a detected binary")
	}
	names := newRegistryNames(t, map[string]string{"ffmpeg": "/opt/test/bin/ffmpeg"})
	for _, want := range []string{"screenshot_video", "contact_sheet_video", "split_video", "join_videos", "extract_audio", "remove_audio", "replace_audio", "transform_video", "transform_audio", "reverse_video", "list_video_transitions"} {
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
	for _, param := range []string{"at", "count", "start", "end", "mode", "columns", "speed", "fadeIn", "fadeOut", "fadeTarget"} {
		if _, ok := properties[param].(map[string]any); !ok {
			t.Errorf("plan schema properties missing %q: %+v", param, properties)
		}
	}
}

func TestApplyKwargsFFmpegParams(t *testing.T) {
	var call HarnessToolCall
	applyKwargs(&call, "at='4.5', start='10', end='25', mode='fast', speed=3, count=6, columns=2")
	if call.At != "4.5" || call.Start != "10" || call.End != "25" || call.Mode != "fast" || call.Speed != 3 {
		t.Errorf("call = %+v, want the ffmpeg params applied", call)
		t.Fatalf("applyKwargs = %+v", call)
	}
	if call.Count != 6 || call.Columns != 2 {
		t.Errorf("applyKwargs = %+v, want count=6 and columns=2 applied", call)
	}
	var slowed HarnessToolCall
	applyKwargs(&slowed, "speed='0.5x'")
	if slowed.Speed != 0.5 {
		t.Fatalf("applyKwargs speed='0.5x' = %v, want 0.5", slowed.Speed)
	}
	var faded HarnessToolCall
	applyKwargs(&faded, "fadeIn=1.5, fade_out=2, fadeTarget='audio'")
	if faded.FadeIn != 1.5 || faded.FadeOut != 2 || faded.FadeTarget != "audio" {
		t.Fatalf("applyKwargs fades = %+v, want fadeIn=1.5, fadeOut=2, fadeTarget=audio (both spellings)", faded)
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

// ---------------------------------------------------------------------------
// Join transitions
// ---------------------------------------------------------------------------

// fakeFFmpegXFadeScript fakes an ffmpeg whose `-h filter=xfade` reports a
// three-transition vocabulary and whose `-version` reports 7.1.1 — the
// capability check and list_video_transitions both read it. Any other call
// (a real join) writes fake media bytes to its last argument, like
// fakeFFmpegScript.
const fakeFFmpegXFadeScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$@" >> "$dir/args.txt"
for a in "$@"; do
  case "$a" in
    filter=xfade)
      cat <<'HELP'
Filter xfade
  Crossfade one video stream to another.
xfade AVOptions:
  transition         <int>        ..FVA....... set crossfade transition (from 0 to INT_MAX) (default fade)
    fade             0            ..FVA....... fade crossfade
    wipeleft         1            ..FVA....... slide one screen over another from left to right
    fadeblack        2            ..FVA....... fade through black
  duration           <duration>   ..FVA....... set crossfade duration (default 1)
  offset             <duration>   ..FVA....... set crossfade start offset (default 0)
HELP
      exit 0
      ;;
    -version)
      echo "ffmpeg version 7.1.1 Copyright (c) 2000-2025 the FFmpeg developers"
      exit 0
      ;;
  esac
done
out=""
for a in "$@"; do
  out="$a"
done
case "$out" in
  -*) exit 0 ;;
esac
printf '\377\330\377FAKE-MEDIA' > "$out"
`

func TestNormalizeXFadeTransition(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"fade", "fade", true},
		{" CrossFade ", "fade", true},
		{"DISSOLVE", "dissolve", true},
		{"wipeleft", "wipeleft", true},
		{"zoomin", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeXFadeTransition(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeXFadeTransition(%q) = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// TestJoinTransitionsFilterGraph pins the whole transitioned graph: junction
// offsets run on the merged timeline (each xfade shortens the total by the
// overlap), a silent clip gets a synthesized anullsrc input after the real
// clips, and the transition clamps to half the shortest clip.
func TestJoinTransitionsFilterGraph(t *testing.T) {
	videoChain := "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p"
	audioChain := "aformat=sample_rates=48000:channel_layouts=stereo"
	cases := []struct {
		name         string
		clips        []joinClipFact
		transition   string
		requested    float64
		wantGraph    string
		wantInputs   []string
		wantSeconds  float64
		wantOutput   float64
		wantClamped  bool
		wantNormal   bool
		wantSynthOne bool
	}{
		{
			name:       "two audio clips, default one-second fade",
			clips:      []joinClipFact{{Duration: 12.5, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}, {Duration: 12.5, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}},
			transition: "fade",
			requested:  0,
			wantGraph: "[0:v]" + videoChain + "[v0];[1:v]" + videoChain + "[v1];" +
				"[v0][v1]xfade=transition=fade:duration=1:offset=11.5[vx1];[vx1]format=yuv420p[vout];" +
				"[0:a]" + audioChain + "[ain0];[1:a]" + audioChain + "[ain1];" +
				"[ain0][ain1]acrossfade=d=1:c1=qsin:c2=qsin[ax1];[ax1]" + audioChain + "[aout]",
			wantSeconds: 1,
			wantOutput:  24,
		},
		{
			name:       "three clips with a silent middle, dissolve",
			clips:      []joinClipFact{{Duration: 10, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}, {Duration: 8, Width: 1920, Height: 1080, FPS: 30}, {Duration: 12, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}},
			transition: "dissolve",
			requested:  2,
			wantGraph: "[0:v]" + videoChain + "[v0];[1:v]" + videoChain + "[v1];[2:v]" + videoChain + "[v2];" +
				"[v0][v1]xfade=transition=dissolve:duration=2:offset=8[vx1];" +
				"[vx1][v2]xfade=transition=dissolve:duration=2:offset=14[vx2];[vx2]format=yuv420p[vout];" +
				"[0:a]" + audioChain + "[ain0];[3:a]" + audioChain + "[ain1];[2:a]" + audioChain + "[ain2];" +
				"[ain0][ain1]acrossfade=d=2:c1=qsin:c2=qsin[ax1];[ax1][ain2]acrossfade=d=2:c1=qsin:c2=qsin[ax2];[ax2]" + audioChain + "[aout]",
			wantInputs:   []string{"-f", "lavfi", "-t", "8", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000"},
			wantSeconds:  2,
			wantOutput:   26,
			wantSynthOne: true,
		},
		{
			name:       "clamped to half the shortest clip",
			clips:      []joinClipFact{{Duration: 2, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}, {Duration: 5, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}},
			transition: "wipeleft",
			requested:  3,
			wantGraph: "[0:v]" + videoChain + "[v0];[1:v]" + videoChain + "[v1];" +
				"[v0][v1]xfade=transition=wipeleft:duration=1:offset=1[vx1];[vx1]format=yuv420p[vout];" +
				"[0:a]" + audioChain + "[ain0];[1:a]" + audioChain + "[ain1];" +
				"[ain0][ain1]acrossfade=d=1:c1=qsin:c2=qsin[ax1];[ax1]" + audioChain + "[aout]",
			wantSeconds: 1,
			wantOutput:  6,
			wantClamped: true,
		},
		{
			name:       "differing clips normalize to the first clip's frame",
			clips:      []joinClipFact{{Duration: 10, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}, {Duration: 10, Width: 1280, Height: 720, FPS: 25, HasAudio: true}},
			transition: "slideleft",
			requested:  2,
			wantGraph: "[0:v]" + videoChain + "[v0];[1:v]" + videoChain + "[v1];" +
				"[v0][v1]xfade=transition=slideleft:duration=2:offset=8[vx1];[vx1]format=yuv420p[vout];" +
				"[0:a]" + audioChain + "[ain0];[1:a]" + audioChain + "[ain1];" +
				"[ain0][ain1]acrossfade=d=2:c1=qsin:c2=qsin[ax1];[ax1]" + audioChain + "[aout]",
			wantSeconds: 2,
			wantOutput:  18,
			wantNormal:  true,
		},
		{
			name:       "unprobed clips stand in 30fps without counting as normalized",
			clips:      []joinClipFact{{Duration: 6, Width: 1920, Height: 1080, HasAudio: true}, {Duration: 6, Width: 1920, Height: 1080, HasAudio: true}},
			transition: "radial",
			requested:  1,
			wantGraph: "[0:v]" + videoChain + "[v0];[1:v]" + videoChain + "[v1];" +
				"[v0][v1]xfade=transition=radial:duration=1:offset=5[vx1];[vx1]format=yuv420p[vout];" +
				"[0:a]" + audioChain + "[ain0];[1:a]" + audioChain + "[ain1];" +
				"[ain0][ain1]acrossfade=d=1:c1=qsin:c2=qsin[ax1];[ax1]" + audioChain + "[aout]",
			wantSeconds: 1,
			wantOutput:  11,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := joinTransitionsFilterGraph(tc.clips, tc.transition, tc.requested)
			if plan.FilterComplex != tc.wantGraph {
				t.Fatalf("filter_complex =\n%s\nwant\n%s", plan.FilterComplex, tc.wantGraph)
			}
			if plan.Seconds != tc.wantSeconds {
				t.Errorf("seconds = %v, want %v", plan.Seconds, tc.wantSeconds)
			}
			if plan.OutputSeconds != tc.wantOutput {
				t.Errorf("output seconds = %v, want %v", plan.OutputSeconds, tc.wantOutput)
			}
			if plan.Clamped != tc.wantClamped {
				t.Errorf("clamped = %v, want %v", plan.Clamped, tc.wantClamped)
			}
			if plan.Normalized != tc.wantNormal {
				t.Errorf("normalized = %v, want %v", plan.Normalized, tc.wantNormal)
			}
			if (len(plan.Synthesized) > 0) != tc.wantSynthOne {
				t.Errorf("synthesized = %v, want presence %v", plan.Synthesized, tc.wantSynthOne)
			}
			if strings.Join(plan.InputArgs, " ") != strings.Join(tc.wantInputs, " ") {
				t.Errorf("input args = %v, want %v", plan.InputArgs, tc.wantInputs)
			}
		})
	}
}

func TestFFmpegJoinTransitionArgs(t *testing.T) {
	plan := joinTransitionPlan{
		FilterComplex: "FC",
		InputArgs:     []string{"-f", "lavfi", "-t", "5", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000"},
	}
	got := strings.Join(ffmpegJoinTransitionArgs([]string{"a.mp4", "b.mp4"}, plan, "out.mp4"), " ")
	want := "-i a.mp4 -i b.mp4 -f lavfi -t 5 -i anullsrc=channel_layout=stereo:sample_rate=48000 " +
		"-filter_complex FC -map [vout] -map [aout] -c:v libx264 -preset veryfast -crf 18 " +
		"-c:a aac -b:a 192k -movflags +faststart out.mp4"
	if got != want {
		t.Fatalf("args = %s, want %s", got, want)
	}
}

func TestParseXFadeTransitions(t *testing.T) {
	help := `Filter xfade
  Crossfade one video stream to another.
xfade AVOptions:
  transition         <int>        ..FVA....... set crossfade transition (from 0 to INT_MAX) (default fade)
    fade             0            ..FVA....... fade crossfade
    wipeleft         1            ..FVA....... slide one screen over another from left to right
    fadeblack        2            ..FVA....... fade through black
  duration           <duration>   ..FVA....... set crossfade duration (default 1)
  offset             <duration>   ..FVA....... set crossfade start offset (default 0)
`
	got := parseXFadeTransitions([]byte(help))
	want := []string{"fade", "wipeleft", "fadeblack"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	for _, broken := range []string{"", "no filter help here", "xfade AVOptions:\n  duration <duration> bad"} {
		if got := parseXFadeTransitions([]byte(broken)); len(got) != 0 {
			t.Errorf("parseXFadeTransitions(%q) = %v, want empty", broken, got)
		}
	}
}

func TestParseFFmpegVersion(t *testing.T) {
	cases := []struct {
		output string
		want   string
	}{
		{"ffmpeg version 7.1.1 Copyright (c) 2000-2025 the FFmpeg developers\n", "7.1.1"},
		{"ffmpeg version n7.0.2-13-g8b8dd0bf76\n", "7.0.2-13-g8b8dd0bf76"},
		{"garbage", ""},
	}
	for _, tc := range cases {
		if got := parseFFmpegVersion([]byte(tc.output)); got != tc.want {
			t.Errorf("parseFFmpegVersion(%q) = %q, want %q", tc.output, got, tc.want)
		}
	}
}

// TestFFmpegJoinTransitionExecutes drives the transitioned join end to end:
// the graph replaces the concat demuxer, the junction offsets land at the
// merged timeline, and the evidence says what ran.
func TestFFmpegJoinTransitionExecutes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE"), videoDataURL("CLIP-TWO")},
	}, "join_videos", HarnessToolCall{Transition: "dissolve", TransitionDuration: 2})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-filter_complex") || strings.Contains(args, "-f concat") {
		t.Fatalf("args = %s, want a filter_complex join without the concat demuxer", args)
	}
	for _, fragment := range []string{
		"xfade=transition=dissolve:duration=2:offset=10.5",
		"acrossfade=d=2:c1=qsin:c2=qsin",
	} {
		if !strings.Contains(args, fragment) {
			t.Errorf("args = %s, want it to include %q", args, fragment)
		}
	}
	if !strings.Contains(result.Summary, "dissolve transition, 2s at each junction") ||
		!strings.Contains(result.Summary, "23s output") {
		t.Errorf("summary = %q, want the transition phrase and the shortened output length", result.Summary)
	}
	joined := strings.Join(result.Notices, "\n")
	if !strings.Contains(joined, "overlaps the clips by 2s") || !strings.Contains(joined, "re-encoded") {
		t.Errorf("notices = %v, want the overlap and re-encode explanation", result.Notices)
	}
	typed, ok := result.Result.(ToolVideoResult)
	if !ok || len(typed.Videos) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	os.Remove(typed.Videos[0].TempPath)
}

// TestFFmpegJoinTransitionSubstitutesUnsupported pins the capability check:
// a transition the installed ffmpeg lacks degrades to "fade" under a notice
// that names the request and points at list_video_transitions.
func TestFFmpegJoinTransitionSubstitutesUnsupported(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegXFadeScript, fakeFFprobeScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE"), videoDataURL("CLIP-TWO")},
	}, "join_videos", HarnessToolCall{Transition: "dissolve"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "xfade=transition=fade:") {
		t.Fatalf("args = %s, want the substituted fade transition", args)
	}
	joined := strings.Join(result.Notices, "\n")
	if !strings.Contains(joined, `does not support the "dissolve" transition`) ||
		!strings.Contains(joined, "list_video_transitions") {
		t.Errorf("notices = %v, want the substitution notice naming the request", result.Notices)
	}
	typed, ok := result.Result.(ToolVideoResult)
	if !ok || len(typed.Videos) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	os.Remove(typed.Videos[0].TempPath)
}

// TestFFmpegJoinTransitionUnmeasurableDegrades pins the degradation: a clip
// whose length cannot be measured (ffprobe failing, the fixture carrying no
// MP4 boxes to sniff) joins plainly under a substitution notice rather than
// failing the call.
func TestFFmpegJoinTransitionUnmeasurableDegrades(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedVideos: []string{videoDataURL("CLIP-ONE"), videoDataURL("CLIP-TWO")},
	}, "join_videos", HarnessToolCall{Transition: "dissolve"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-f concat") || strings.Contains(args, "xfade=transition=") {
		t.Fatalf("args = %s, want the plain concat join without xfade", args)
	}
	joined := strings.Join(result.Notices, "\n")
	if !strings.Contains(joined, "without the requested transition") {
		t.Errorf("notices = %v, want the substitution notice", result.Notices)
	}
	typed, ok := result.Result.(ToolVideoResult)
	if !ok || len(typed.Videos) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	os.Remove(typed.Videos[0].TempPath)
}

func TestListVideoTransitionsExecutes(t *testing.T) {
	config, _ := ffmpegTestConfig(t, fakeFFmpegXFadeScript, "")
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{}, "list_video_transitions", HarnessToolCall{})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	typed, ok := result.Result.(ToolTransitionListResult)
	if !ok {
		t.Fatalf("result payload = %+v", result.Result)
	}
	if typed.Version != "7.1.1" || typed.Default != "fade" {
		t.Errorf("payload = %+v, want version 7.1.1 and default fade", typed)
	}
	if strings.Join(typed.Transitions, ",") != "fade,wipeleft,fadeblack" {
		t.Errorf("transitions = %v, want the fixture's three", typed.Transitions)
	}
	if !strings.Contains(result.Summary, "ffmpeg 7.1.1 supports 3 video transitions") {
		t.Errorf("summary = %q", result.Summary)
	}
}

func TestJoinVideosTransitionValidate(t *testing.T) {
	validate := func(call HarnessToolCall) []string {
		return joinVideosToolDefinition().Validate("toolCalls[0]", call)
	}
	if errs := validate(HarnessToolCall{Mode: "copy", Transition: "fade"}); len(errs) != 1 ||
		!strings.Contains(errs[0], `mode "copy" cannot carry a transition`) {
		t.Errorf("copy + transition = %v, want the mode conflict", errs)
	}
	if errs := validate(HarnessToolCall{Transition: "zoomin"}); len(errs) != 1 ||
		!strings.Contains(errs[0], "list_video_transitions") {
		t.Errorf("unknown transition = %v, want the vocabulary correction", errs)
	}
	for _, bad := range []float64{-1, 0.05, 15} {
		if errs := validate(HarnessToolCall{Transition: "fade", TransitionDuration: bad}); len(errs) != 1 {
			t.Errorf("transitionDuration %v = %v, want the bounds correction", bad, errs)
		}
	}
	for _, good := range []HarnessToolCall{
		{Transition: "crossfade"},
		{Transition: "DISSOLVE", TransitionDuration: 2.5},
		{Mode: "reencode", Transition: "wipeleft"},
	} {
		if errs := validate(good); len(errs) != 0 {
			t.Errorf("validate(%+v) = %v, want none", good, errs)
		}
	}
}

// ---------------------------------------------------------------------------
// Audio join transitions
// ---------------------------------------------------------------------------

// fakeFFprobeAudioScript fakes ffprobe over a 30-second 44.1 kHz stereo PCM
// WAV — the audio-only report (codec, sample rate, channels, duration) the
// crossfaded join normalizes onto and derives its re-encode target from.
const fakeFFprobeAudioScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"30.000","format_name":"wav","bit_rate":"1411200","size":"5292044"},"streams":[{"codec_type":"audio","codec_name":"pcm_s16le","sample_rate":"44100","channels":2}]}
JSON
`

// fakeFFprobeAudioDriftScript fakes an ffprobe whose SECOND and later calls
// report a 48 kHz mono clip after a 44.1 kHz stereo first — drives the
// crossfaded join's normalize-onto-the-first-clip branch.
const fakeFFprobeAudioDriftScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -e "$dir/probed" ]; then
cat <<'JSON'
{"format":{"duration":"20.000","format_name":"wav"},"streams":[{"codec_type":"audio","codec_name":"pcm_s16le","sample_rate":"48000","channels":1}]}
JSON
else
  : > "$dir/probed"
cat <<'JSON'
{"format":{"duration":"30.000","format_name":"wav"},"streams":[{"codec_type":"audio","codec_name":"pcm_s16le","sample_rate":"44100","channels":2}]}
JSON
fi
`

func TestNormalizeAudioTransition(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"crossfade", "crossfade", true},
		{" CrossFade ", "crossfade", true},
		{"fade", "crossfade", true},
		{"FADE", "crossfade", true},
		{"dip", "dip", true},
		{" Dip ", "dip", true},
		{"fadeblack", "dip", true},
		{"FadeBlack", "dip", true},
		{"dissolve", "", false},
		{"wipeleft", "", false},
		{"fadewhite", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeAudioTransition(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeAudioTransition(%q) = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFormatJoinSeconds(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{2, "2"},
		{0.5, "0.5"},
		{12.527 + 8.013 - (12.527 + 8.013 - 2), "2"},
		{19.540000000000003, "19.54"},
		{6.2635, "6.264"},
	}
	for _, tc := range cases {
		if got := formatJoinSeconds(tc.seconds); got != tc.want {
			t.Errorf("formatJoinSeconds(%v) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// TestJoinAudioTransitionsFilterGraph pins the crossfaded graph: every input
// normalized onto the first clip's shape, one equal-power acrossfade per junction chained
// through the previous junction's output, the output pinned to the same
// shape, the overlap clamped to half the shortest clip, and 48 kHz stereo
// standing in when no shape was measured.
func TestJoinAudioTransitionsFilterGraph(t *testing.T) {
	stereo44 := "aformat=sample_rates=44100:channel_layouts=stereo"
	mono48 := "aformat=sample_rates=48000:channel_layouts=mono"
	stereo48 := "aformat=sample_rates=48000:channel_layouts=stereo"
	cases := []struct {
		name          string
		clips         []joinAudioClipFact
		requested     float64
		wantGraph     string
		wantSeconds   float64
		wantOutput    float64
		wantShape     [2]int
		wantClamped   bool
		wantNormal    bool
		wantDefaulted bool
	}{
		{
			name:        "two clips, default one-second crossfade",
			clips:       []joinAudioClipFact{{Duration: 30, SampleRate: 44100, Channels: 2, Codec: "pcm_s16le"}, {Duration: 30, SampleRate: 44100, Channels: 2, Codec: "pcm_s16le"}},
			requested:   0,
			wantGraph:   "[0:a]" + stereo44 + "[ain0];[1:a]" + stereo44 + "[ain1];[ain0][ain1]acrossfade=d=1:c1=qsin:c2=qsin[ax1];[ax1]" + stereo44 + "[aout]",
			wantSeconds: 1,
			wantOutput:  59,
			wantShape:   [2]int{44100, 2},
		},
		{
			name:      "three clips chain through the previous junction",
			clips:     []joinAudioClipFact{{Duration: 10, SampleRate: 48000, Channels: 1}, {Duration: 8, SampleRate: 48000, Channels: 1}, {Duration: 12, SampleRate: 48000, Channels: 1}},
			requested: 2,
			wantGraph: "[0:a]" + mono48 + "[ain0];[1:a]" + mono48 + "[ain1];[2:a]" + mono48 + "[ain2];" +
				"[ain0][ain1]acrossfade=d=2:c1=qsin:c2=qsin[ax1];[ax1][ain2]acrossfade=d=2:c1=qsin:c2=qsin[ax2];[ax2]" + mono48 + "[aout]",
			wantSeconds: 2,
			wantOutput:  26,
			wantShape:   [2]int{48000, 1},
		},
		{
			name:        "clamped to half the shortest clip",
			clips:       []joinAudioClipFact{{Duration: 2, SampleRate: 44100, Channels: 2}, {Duration: 5, SampleRate: 44100, Channels: 2}},
			requested:   3,
			wantGraph:   "[0:a]" + stereo44 + "[ain0];[1:a]" + stereo44 + "[ain1];[ain0][ain1]acrossfade=d=1:c1=qsin:c2=qsin[ax1];[ax1]" + stereo44 + "[aout]",
			wantSeconds: 1,
			wantOutput:  6,
			wantShape:   [2]int{44100, 2},
			wantClamped: true,
		},
		{
			name:        "differing clips normalize to the first clip's shape",
			clips:       []joinAudioClipFact{{Duration: 30, SampleRate: 44100, Channels: 2}, {Duration: 20, SampleRate: 48000, Channels: 1}},
			requested:   2,
			wantGraph:   "[0:a]" + stereo44 + "[ain0];[1:a]" + stereo44 + "[ain1];[ain0][ain1]acrossfade=d=2:c1=qsin:c2=qsin[ax1];[ax1]" + stereo44 + "[aout]",
			wantSeconds: 2,
			wantOutput:  48,
			wantShape:   [2]int{44100, 2},
			wantNormal:  true,
		},
		{
			name:          "unmeasured shape defaults to 48 kHz stereo",
			clips:         []joinAudioClipFact{{Duration: 1}, {Duration: 1}},
			requested:     0,
			wantGraph:     "[0:a]" + stereo48 + "[ain0];[1:a]" + stereo48 + "[ain1];[ain0][ain1]acrossfade=d=0.5:c1=qsin:c2=qsin[ax1];[ax1]" + stereo48 + "[aout]",
			wantSeconds:   0.5,
			wantOutput:    1.5,
			wantShape:     [2]int{48000, 2},
			wantClamped:   true,
			wantDefaulted: true,
		},
		{
			name:        "a sniffed second clip never counts as normalized",
			clips:       []joinAudioClipFact{{Duration: 30, SampleRate: 44100, Channels: 2}, {Duration: 30}},
			requested:   1,
			wantGraph:   "[0:a]" + stereo44 + "[ain0];[1:a]" + stereo44 + "[ain1];[ain0][ain1]acrossfade=d=1:c1=qsin:c2=qsin[ax1];[ax1]" + stereo44 + "[aout]",
			wantSeconds: 1,
			wantOutput:  59,
			wantShape:   [2]int{44100, 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := joinAudioTransitionsFilterGraph(tc.clips, tc.requested)
			if plan.FilterComplex != tc.wantGraph {
				t.Fatalf("filter_complex =\n%s\nwant\n%s", plan.FilterComplex, tc.wantGraph)
			}
			if plan.Seconds != tc.wantSeconds {
				t.Errorf("seconds = %v, want %v", plan.Seconds, tc.wantSeconds)
			}
			if plan.OutputSeconds != tc.wantOutput {
				t.Errorf("output seconds = %v, want %v", plan.OutputSeconds, tc.wantOutput)
			}
			if plan.SampleRate != tc.wantShape[0] || plan.Channels != tc.wantShape[1] {
				t.Errorf("shape = %d Hz × %d, want %d Hz × %d", plan.SampleRate, plan.Channels, tc.wantShape[0], tc.wantShape[1])
			}
			if plan.Clamped != tc.wantClamped {
				t.Errorf("clamped = %v, want %v", plan.Clamped, tc.wantClamped)
			}
			if plan.Normalized != tc.wantNormal {
				t.Errorf("normalized = %v, want %v", plan.Normalized, tc.wantNormal)
			}
			if plan.Defaulted != tc.wantDefaulted {
				t.Errorf("defaulted = %v, want %v", plan.Defaulted, tc.wantDefaulted)
			}
		})
	}
}

// TestJoinAudioDipFilterGraph pins the dip-to-silence graph: every input
// normalized onto the first clip's shape, each clip's edges carrying only the
// half-fades its junctions need (the first fades out alone, the last fades in
// alone, a middle clip both), the concat filter with no overlap so the output
// is the clips' sum, the transition clamped to the shortest clip, and the
// shape/default decisions shared with the crossfade graph.
func TestJoinAudioDipFilterGraph(t *testing.T) {
	stereo44 := "aformat=sample_rates=44100:channel_layouts=stereo"
	mono48 := "aformat=sample_rates=48000:channel_layouts=mono"
	stereo48 := "aformat=sample_rates=48000:channel_layouts=stereo"
	cases := []struct {
		name          string
		clips         []joinAudioClipFact
		requested     float64
		wantGraph     string
		wantSeconds   float64
		wantOutput    float64
		wantShape     [2]int
		wantClamped   bool
		wantNormal    bool
		wantDefaulted bool
	}{
		{
			name:        "two clips, default one-second dip",
			clips:       []joinAudioClipFact{{Duration: 30, SampleRate: 44100, Channels: 2, Codec: "pcm_s16le"}, {Duration: 30, SampleRate: 44100, Channels: 2, Codec: "pcm_s16le"}},
			requested:   0,
			wantGraph:   "[0:a]" + stereo44 + ",afade=t=out:st=29.5:d=0.5[ain0];[1:a]" + stereo44 + ",afade=t=in:st=0:d=0.5[ain1];[ain0][ain1]concat=n=2:v=0:a=1[aout]",
			wantSeconds: 1,
			wantOutput:  60,
			wantShape:   [2]int{44100, 2},
		},
		{
			name:      "a middle clip fades in and out",
			clips:     []joinAudioClipFact{{Duration: 10, SampleRate: 48000, Channels: 1}, {Duration: 8, SampleRate: 48000, Channels: 1}, {Duration: 12, SampleRate: 48000, Channels: 1}},
			requested: 2,
			wantGraph: "[0:a]" + mono48 + ",afade=t=out:st=9:d=1[ain0];" +
				"[1:a]" + mono48 + ",afade=t=in:st=0:d=1,afade=t=out:st=7:d=1[ain1];" +
				"[2:a]" + mono48 + ",afade=t=in:st=0:d=1[ain2];" +
				"[ain0][ain1][ain2]concat=n=3:v=0:a=1[aout]",
			wantSeconds: 2,
			wantOutput:  30,
			wantShape:   [2]int{48000, 1},
		},
		{
			name:        "clamped to the shortest clip",
			clips:       []joinAudioClipFact{{Duration: 2, SampleRate: 44100, Channels: 2}, {Duration: 5, SampleRate: 44100, Channels: 2}},
			requested:   3,
			wantGraph:   "[0:a]" + stereo44 + ",afade=t=out:st=1:d=1[ain0];[1:a]" + stereo44 + ",afade=t=in:st=0:d=1[ain1];[ain0][ain1]concat=n=2:v=0:a=1[aout]",
			wantSeconds: 2,
			wantOutput:  7,
			wantShape:   [2]int{44100, 2},
			wantClamped: true,
		},
		{
			name:        "differing clips normalize to the first clip's shape",
			clips:       []joinAudioClipFact{{Duration: 30, SampleRate: 44100, Channels: 2}, {Duration: 20, SampleRate: 48000, Channels: 1}},
			requested:   2,
			wantGraph:   "[0:a]" + stereo44 + ",afade=t=out:st=29:d=1[ain0];[1:a]" + stereo44 + ",afade=t=in:st=0:d=1[ain1];[ain0][ain1]concat=n=2:v=0:a=1[aout]",
			wantSeconds: 2,
			wantOutput:  50,
			wantShape:   [2]int{44100, 2},
			wantNormal:  true,
		},
		{
			name:          "unmeasured shape defaults to 48 kHz stereo",
			clips:         []joinAudioClipFact{{Duration: 1}, {Duration: 1}},
			requested:     0,
			wantGraph:     "[0:a]" + stereo48 + ",afade=t=out:st=0.5:d=0.5[ain0];[1:a]" + stereo48 + ",afade=t=in:st=0:d=0.5[ain1];[ain0][ain1]concat=n=2:v=0:a=1[aout]",
			wantSeconds:   1,
			wantOutput:    2,
			wantShape:     [2]int{48000, 2},
			wantDefaulted: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := joinAudioDipFilterGraph(tc.clips, tc.requested)
			if plan.FilterComplex != tc.wantGraph {
				t.Fatalf("filter_complex =\n%s\nwant\n%s", plan.FilterComplex, tc.wantGraph)
			}
			if strings.Contains(plan.FilterComplex, "acrossfade") || strings.Contains(plan.FilterComplex, "qsin") {
				t.Errorf("filter_complex = %s, want no crossfade stage and no equal-power curve in a dip", plan.FilterComplex)
			}
			if plan.Seconds != tc.wantSeconds {
				t.Errorf("seconds = %v, want %v", plan.Seconds, tc.wantSeconds)
			}
			if plan.OutputSeconds != tc.wantOutput {
				t.Errorf("output seconds = %v, want %v", plan.OutputSeconds, tc.wantOutput)
			}
			if plan.SampleRate != tc.wantShape[0] || plan.Channels != tc.wantShape[1] {
				t.Errorf("shape = %d Hz × %d, want %d Hz × %d", plan.SampleRate, plan.Channels, tc.wantShape[0], tc.wantShape[1])
			}
			if plan.Clamped != tc.wantClamped {
				t.Errorf("clamped = %v, want %v", plan.Clamped, tc.wantClamped)
			}
			if plan.Normalized != tc.wantNormal {
				t.Errorf("normalized = %v, want %v", plan.Normalized, tc.wantNormal)
			}
			if plan.Defaulted != tc.wantDefaulted {
				t.Errorf("defaulted = %v, want %v", plan.Defaulted, tc.wantDefaulted)
			}
		})
	}
}

func TestAudioChannelLayoutAndShapePhrase(t *testing.T) {
	for channels, want := range map[int]string{1: "mono", 2: "stereo", 6: "6c"} {
		if got := audioChannelLayout(channels); got != want {
			t.Errorf("audioChannelLayout(%d) = %q, want %q", channels, got, want)
		}
	}
	cases := []struct {
		rate, channels int
		want           string
	}{
		{44100, 2, "44.1 kHz stereo"},
		{48000, 1, "48 kHz mono"},
		{22050, 6, "22.05 kHz 6-channel"},
	}
	for _, tc := range cases {
		if got := audioShapePhrase(tc.rate, tc.channels); got != tc.want {
			t.Errorf("audioShapePhrase(%d, %d) = %q, want %q", tc.rate, tc.channels, got, tc.want)
		}
	}
}

func TestFFmpegJoinAudioTransitionArgs(t *testing.T) {
	plan := joinAudioTransitionPlan{FilterComplex: "FC"}
	got := strings.Join(ffmpegJoinAudioTransitionArgs([]string{"a.wav", "b.wav"}, plan, []string{"-c:a", "pcm_s16le"}, "out.wav"), " ")
	want := "-i a.wav -i b.wav -filter_complex FC -map [aout] -c:a pcm_s16le out.wav"
	if got != want {
		t.Fatalf("args = %s, want %s", got, want)
	}
}

func TestJoinAudiosTransitionValidate(t *testing.T) {
	validate := func(call HarnessToolCall) []string {
		return joinAudiosToolDefinition().Validate("toolCalls[0]", call)
	}
	if errs := validate(HarnessToolCall{Mode: "copy", Transition: "crossfade"}); len(errs) != 1 ||
		!strings.Contains(errs[0], `mode "copy" cannot carry a transition`) {
		t.Errorf("copy + transition = %v, want the mode conflict", errs)
	}
	if errs := validate(HarnessToolCall{Mode: "copy", Transition: "dip"}); len(errs) != 1 ||
		!strings.Contains(errs[0], `mode "copy" cannot carry a transition`) {
		t.Errorf("copy + dip = %v, want the mode conflict", errs)
	}
	if errs := validate(HarnessToolCall{Transition: "dissolve"}); len(errs) != 1 ||
		!strings.Contains(errs[0], `must be "crossfade"`) || !strings.Contains(errs[0], `"dip"`) || !strings.Contains(errs[0], "join_videos") {
		t.Errorf("video transition = %v, want the vocabulary correction naming both audio transitions and pointing at join_videos", errs)
	}
	for _, bad := range []float64{-1, 0.05, 15} {
		if errs := validate(HarnessToolCall{Transition: "crossfade", TransitionDuration: bad}); len(errs) != 1 {
			t.Errorf("transitionDuration %v = %v, want the bounds correction", bad, errs)
		}
	}
	if errs := validate(HarnessToolCall{Mode: "turbo", Transition: "dissolve"}); len(errs) != 2 {
		t.Errorf("bad mode + bad transition = %v, want both corrections", errs)
	}
	for _, good := range []HarnessToolCall{
		{Transition: "crossfade"},
		{Transition: "FADE", TransitionDuration: 2.5},
		{Mode: "reencode", Transition: "crossfade"},
		{Mode: "auto", Transition: "fade", TransitionDuration: 10},
		{Transition: "dip"},
		{Transition: "fadeblack", TransitionDuration: 2},
		{Mode: "reencode", Transition: "dip", TransitionDuration: 0.5},
		{Mode: "copy"},
	} {
		if errs := validate(good); len(errs) != 0 {
			t.Errorf("validate(%+v) = %v, want none", good, errs)
		}
	}
	schema := joinAudiosParamSchema()["properties"].(map[string]any)
	for _, param := range []string{"transition", "transitionDuration"} {
		if _, ok := schema[param]; !ok {
			t.Errorf("join_audios param schema lacks %q", param)
		}
	}
	if enum := schema["transition"].(map[string]any)["enum"].([]string); strings.Join(enum, ",") != "crossfade,fade,dip,fadeblack" {
		t.Errorf("transition enum = %v, want crossfade,fade,dip,fadeblack", enum)
	}
	if desc := joinAudiosToolDefinition().Description; !strings.Contains(desc, "crossfade") || !strings.Contains(desc, "transitionDuration") ||
		!strings.Contains(desc, `"dip"`) || !strings.Contains(desc, "NO overlap") || !strings.Contains(desc, "transform_audio") {
		t.Errorf("join_audios description should explain both transitions and point the outer edges at transform_audio: %q", desc)
	}
}

// TestFFmpegJoinAudioTransitionExecutes drives the crossfaded join end to end:
// the graph replaces the concat demuxer, every input lands on the first clip's
// shape, the output keeps the first clip's codec family (two WAVs crossfade
// into a WAV), and the evidence says what ran.
func TestFFmpegJoinAudioTransitionExecutes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAudioScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedAudios: []string{wavDataURL(), wavDataURL()},
	}, "join_audios", HarnessToolCall{Transition: "fade", TransitionDuration: 2})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-filter_complex") || strings.Contains(args, "-f concat") {
		t.Fatalf("args = %s, want a filter_complex join without the concat demuxer", args)
	}
	for _, fragment := range []string{
		"[0:a]aformat=sample_rates=44100:channel_layouts=stereo[ain0]",
		"[ain0][ain1]acrossfade=d=2:c1=qsin:c2=qsin[ax1]",
		"[ax1]aformat=sample_rates=44100:channel_layouts=stereo[aout]",
		"-map [aout] -c:a pcm_s16le",
	} {
		if !strings.Contains(args, fragment) {
			t.Errorf("args = %s, want it to include %q", args, fragment)
		}
	}
	if first, second := strings.Index(args, "input-001.wav"), strings.Index(args, "input-002.wav"); first < 0 || second < 0 || first > second {
		t.Errorf("args = %s, want the inputs in attachment order", args)
	}
	if !strings.Contains(result.Summary, "crossfade, 2s at each junction") || !strings.Contains(result.Summary, "58s output") {
		t.Errorf("summary = %q, want the crossfade phrase and the shortened output length", result.Summary)
	}
	if len(result.Notices) != 1 || !strings.Contains(result.Notices[0], "overlaps the clips by 2s") ||
		!strings.Contains(result.Notices[0], "2s shorter") || !strings.Contains(result.Notices[0], "re-encoded") {
		t.Errorf("notices = %v, want only the overlap and re-encode explanation", result.Notices)
	}
	typed, ok := result.Result.(ToolAudioResult)
	if !ok || len(typed.Audios) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	defer os.Remove(typed.Audios[0].TempPath)
	if typed.Audios[0].MimeType != "audio/wav" || !strings.HasSuffix(typed.Audios[0].TempPath, ".wav") {
		t.Errorf("audio file = %+v, want the first clip's WAV family", typed.Audios[0])
	}
}

// TestFFmpegJoinAudioTransitionNormalizesShape pins the normalize notice: a
// second clip at another sample rate and channel count is converted onto the
// first clip's shape, and the notice names that shape.
func TestFFmpegJoinAudioTransitionNormalizesShape(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAudioDriftScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedAudios: []string{wavDataURL(), wavDataURL()},
	}, "join_audios", HarnessToolCall{Transition: "crossfade"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "[1:a]aformat=sample_rates=44100:channel_layouts=stereo[ain1]") || strings.Contains(args, "48000") {
		t.Fatalf("args = %s, want the second clip normalized onto the first clip's 44.1 kHz stereo", args)
	}
	joined := strings.Join(result.Notices, "\n")
	if !strings.Contains(joined, "converted to the first clip's (44.1 kHz stereo)") {
		t.Errorf("notices = %v, want the normalize notice naming the shape", result.Notices)
	}
	if !strings.Contains(result.Summary, "49s output") {
		t.Errorf("summary = %q, want 30 + 20 - 1 = 49s output", result.Summary)
	}
	typed := result.Result.(ToolAudioResult)
	os.Remove(typed.Audios[0].TempPath)
}

// TestFFmpegJoinAudioTransitionSniffsWithoutFFprobe pins the no-ffprobe path:
// the pure-Go byte sniff measures the WAVs (one second each), so the crossfade
// still runs — clamped to half the shortest clip, rendered at the default
// shape, re-encoded to the MP3 fallback because no codec was identified — and
// every one of those decisions rides a notice.
func TestFFmpegJoinAudioTransitionSniffsWithoutFFprobe(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedAudios: []string{wavDataURL(), wavDataURL()},
	}, "join_audios", HarnessToolCall{Transition: "crossfade"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	for _, fragment := range []string{
		"acrossfade=d=0.5:c1=qsin:c2=qsin",
		"aformat=sample_rates=48000:channel_layouts=stereo",
		"-c:a libmp3lame -b:a 192k",
	} {
		if !strings.Contains(args, fragment) {
			t.Errorf("args = %s, want it to include %q", args, fragment)
		}
	}
	if strings.Contains(args, "-f concat") {
		t.Errorf("args = %s, want the crossfade graph, not the concat degrade", args)
	}
	joined := strings.Join(result.Notices, "\n")
	for _, fragment := range []string{
		"shortened to 0.5s",
		"rendered at 48 kHz stereo",
		"could not be identified",
		"overlaps the clips by 0.5s",
	} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("notices = %v, want them to include %q", result.Notices, fragment)
		}
	}
	if !strings.Contains(result.Summary, "1.5s output") {
		t.Errorf("summary = %q, want 1 + 1 - 0.5 = 1.5s output", result.Summary)
	}
	typed := result.Result.(ToolAudioResult)
	defer os.Remove(typed.Audios[0].TempPath)
	if typed.Audios[0].MimeType != "audio/mpeg" || !strings.HasSuffix(typed.Audios[0].TempPath, ".mp3") {
		t.Errorf("audio file = %+v, want the MP3 fallback", typed.Audios[0])
	}
}

// TestFFmpegJoinAudioTransitionUnmeasurableDegrades pins the degradation: a
// clip whose length neither ffprobe nor the byte sniff can read joins plainly
// under a substitution notice rather than failing the call — and the plain
// join's own notice follows it.
func TestFFmpegJoinAudioTransitionUnmeasurableDegrades(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedAudios: []string{audioDataURL("audio/mpeg", "not-an-mp3"), audioDataURL("audio/mpeg", "still-not-an-mp3")},
	}, "join_audios", HarnessToolCall{Transition: "crossfade"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-f concat") || strings.Contains(args, "acrossfade") {
		t.Fatalf("args = %s, want the plain concat join without acrossfade", args)
	}
	if len(result.Notices) < 2 || !strings.Contains(result.Notices[0], "without the requested crossfade") ||
		!strings.Contains(result.Notices[1], "could not be compared") {
		t.Errorf("notices = %v, want the substitution notice first, then the plain join's probe-failure notice", result.Notices)
	}
	if !strings.Contains(result.Summary, "re-encoded") || strings.Contains(result.Summary, "crossfade") {
		t.Errorf("summary = %q, want the plain join's phrase", result.Summary)
	}
	typed, ok := result.Result.(ToolAudioResult)
	if !ok || len(typed.Audios) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	os.Remove(typed.Audios[0].TempPath)
}

// TestFFmpegJoinAudioDipExecutes drives the dip-to-silence join end to end:
// the per-clip afade edges feed the concat filter (no acrossfade, no concat
// demuxer), the output keeps the first clip's codec family and the clips'
// combined length, and the evidence says what ran. The two 30s WAVs with a 2s
// dip fade out from 29s and in over the first second of the second clip.
func TestFFmpegJoinAudioDipExecutes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeAudioScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedAudios: []string{wavDataURL(), wavDataURL()},
	}, "join_audios", HarnessToolCall{Transition: "fadeblack", TransitionDuration: 2})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	if !strings.Contains(args, "-filter_complex") || strings.Contains(args, "-f concat") || strings.Contains(args, "acrossfade") {
		t.Fatalf("args = %s, want a filter_complex dip without the concat demuxer or a crossfade", args)
	}
	for _, fragment := range []string{
		"[0:a]aformat=sample_rates=44100:channel_layouts=stereo,afade=t=out:st=29:d=1[ain0]",
		"[1:a]aformat=sample_rates=44100:channel_layouts=stereo,afade=t=in:st=0:d=1[ain1]",
		"[ain0][ain1]concat=n=2:v=0:a=1[aout]",
		"-map [aout] -c:a pcm_s16le",
	} {
		if !strings.Contains(args, fragment) {
			t.Errorf("args = %s, want it to include %q", args, fragment)
		}
	}
	if !strings.Contains(result.Summary, "dip, 2s at each junction") || !strings.Contains(result.Summary, "60s output") {
		t.Errorf("summary = %q, want the dip phrase and the unchanged combined length", result.Summary)
	}
	if len(result.Notices) != 1 || !strings.Contains(result.Notices[0], "no overlap") ||
		!strings.Contains(result.Notices[0], "combined length (60s)") || !strings.Contains(result.Notices[0], "re-encoded") {
		t.Errorf("notices = %v, want only the no-overlap and re-encode explanation", result.Notices)
	}
	typed, ok := result.Result.(ToolAudioResult)
	if !ok || len(typed.Audios) != 1 {
		t.Fatalf("result payload = %+v", result.Result)
	}
	defer os.Remove(typed.Audios[0].TempPath)
	if typed.Audios[0].MimeType != "audio/wav" || !strings.HasSuffix(typed.Audios[0].TempPath, ".wav") {
		t.Errorf("audio file = %+v, want the first clip's WAV family", typed.Audios[0])
	}
	if typed.Prompt != "joined 2 audio clips with a dip" {
		t.Errorf("prompt = %q, want the dip named", typed.Prompt)
	}
}

// TestFFmpegJoinAudioDipClampsAndNormalizes pins the dip's own clamp notice
// (the two half-fades must fit inside the shortest clip — a 1s sniffed WAV
// caps a requested 3s dip at 1s) beside the shared shape notices, with the
// unprobed path landing on the MP3 fallback like the crossfade's.
func TestFFmpegJoinAudioDipClampsAndNormalizes(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedAudios: []string{wavDataURL(), wavDataURL()},
	}, "join_audios", HarnessToolCall{Transition: "dip", TransitionDuration: 3})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	args := fakeFFmpegArgs(t, bin)
	for _, fragment := range []string{
		"aformat=sample_rates=48000:channel_layouts=stereo,afade=t=out:st=0.5:d=0.5[ain0]",
		"aformat=sample_rates=48000:channel_layouts=stereo,afade=t=in:st=0:d=0.5[ain1]",
		"-c:a libmp3lame -b:a 192k",
	} {
		if !strings.Contains(args, fragment) {
			t.Errorf("args = %s, want it to include %q", args, fragment)
		}
	}
	joined := strings.Join(result.Notices, "\n")
	for _, fragment := range []string{
		"the dip was shortened to 1s",
		"must fit inside the shortest clip",
		"the dip join was rendered at 48 kHz stereo",
		"could not be identified",
		"combined length (2s)",
	} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("notices = %v, want them to include %q", result.Notices, fragment)
		}
	}
	if !strings.Contains(result.Summary, "dip, 1s at each junction") || !strings.Contains(result.Summary, "2s output") {
		t.Errorf("summary = %q, want the clamped dip and 1 + 1 = 2s output", result.Summary)
	}
	typed := result.Result.(ToolAudioResult)
	os.Remove(typed.Audios[0].TempPath)
}

// TestFFmpegJoinAudioDipUnmeasurableDegrades pins that the dip degrades the
// way the crossfade does: an unmeasurable clip joins plainly under a
// substitution notice that names the requested transition.
func TestFFmpegJoinAudioDipUnmeasurableDegrades(t *testing.T) {
	config, bin := ffmpegTestConfig(t, fakeFFmpegScript, fakeFFprobeFailScript)
	result := executeFFmpegTool(t, config, HarnessToolExecutionContext{
		AttachedAudios: []string{audioDataURL("audio/mpeg", "not-an-mp3"), audioDataURL("audio/mpeg", "still-not-an-mp3")},
	}, "join_audios", HarnessToolCall{Transition: "dip"})
	if result.Status != "completed" {
		t.Fatalf("result = %+v (error %s)", result, result.Error)
	}
	if args := fakeFFmpegArgs(t, bin); !strings.Contains(args, "-f concat") || strings.Contains(args, "afade") {
		t.Fatalf("args = %s, want the plain concat join without any fade", args)
	}
	if len(result.Notices) < 2 || !strings.Contains(result.Notices[0], "without the requested dip") {
		t.Errorf("notices = %v, want the substitution notice naming the dip first", result.Notices)
	}
	if strings.Contains(result.Summary, "dip") {
		t.Errorf("summary = %q, want the plain join's phrase", result.Summary)
	}
	typed := result.Result.(ToolAudioResult)
	os.Remove(typed.Audios[0].TempPath)
}

// TestAcrossfadeFilterIsEqualPower pins the junction stage both join tools
// share: quarter-sine curves on both sides, never acrossfade's default linear
// ramps (which dip ~3 dB at the midpoint on unrelated material).
func TestAcrossfadeFilterIsEqualPower(t *testing.T) {
	if got := acrossfadeFilter("2.5"); got != "acrossfade=d=2.5:c1=qsin:c2=qsin" {
		t.Fatalf("acrossfadeFilter(2.5) = %q", got)
	}
	for _, graph := range []string{
		joinTransitionsFilterGraph([]joinClipFact{{Duration: 10, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}, {Duration: 10, Width: 1920, Height: 1080, FPS: 30, HasAudio: true}}, "fade", 1).FilterComplex,
		joinAudioTransitionsFilterGraph([]joinAudioClipFact{{Duration: 10, SampleRate: 48000, Channels: 2}, {Duration: 10, SampleRate: 48000, Channels: 2}}, 1).FilterComplex,
	} {
		if !strings.Contains(graph, "acrossfade=d=1:c1=qsin:c2=qsin") || strings.Contains(graph, "acrossfade=d=1[") {
			t.Errorf("graph = %s, want every acrossfade stage equal-power", graph)
		}
	}
}
