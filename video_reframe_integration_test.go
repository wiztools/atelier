package main

// Opt-in proof against real binaries, never the local-tool TestMain seams:
// ATELIER_TEST_FFMPEG=1 go test -run TestVideoReframeFFmpegIntegration -v

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type videoReframeProbe struct {
	Frames []struct {
		Timestamp string `json:"best_effort_timestamp_time"`
	} `json:"frames"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		StartTime string `json:"start_time"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func videoReframeRealCommand(t *testing.T, executable string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	var stderr limitedReframeTestBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", filepath.Base(executable), err, stderr.data)
	}
	return out
}

// A failing binary cannot flood a test log; raw frames still go to stdout.
type limitedReframeTestBuffer struct{ data []byte }

func (b *limitedReframeTestBuffer) Write(p []byte) (int, error) {
	length := len(p)
	if remaining := 4096 - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(remaining, len(p))]...)
	}
	return length, nil
}

func videoReframeProbeFile(t *testing.T, ffprobe, path string, frames bool) videoReframeProbe {
	t.Helper()
	args := []string{"-v", "error", "-show_streams", "-show_format", "-of", "json"}
	if frames {
		args = append(args, "-select_streams", "v:0", "-show_frames", "-show_entries", "frame=best_effort_timestamp_time:stream=codec_type,width,height,start_time:format=duration")
	}
	data := videoReframeRealCommand(t, ffprobe, append(args, path)...)
	var probe videoReframeProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatal(err)
	}
	return probe
}

func videoReframeTestSeconds(t *testing.T, value string) float64 {
	t.Helper()
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("invalid probed timestamp %q", value)
	}
	return seconds
}

func TestVideoReframeFFmpegIntegration(t *testing.T) {
	if os.Getenv("ATELIER_TEST_FFMPEG") != "1" {
		t.Skip("set ATELIER_TEST_FFMPEG=1 to run real ffmpeg geometry/timing proof")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("integration requested but ffmpeg is unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal("integration requested but ffprobe is unavailable")
	}
	params := loadVideoReframeFixtures(t).Timelines[0].Params
	for _, test := range []struct {
		name       string
		start      float64
		audio, vfr bool
		dense      bool
	}{
		{"silent", 0, false, false, false},
		{"nonzero-start-delayed-audio", 5, true, false, false},
		{"variable-frame-rate", 5, true, true, false},
		{"maximum-marker-count", 0, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			params := params
			if test.dense {
				params.Markers = make([]VideoReframeMarker, videoReframeMaxMarkers)
				for i := range params.Markers {
					params.Markers[i] = VideoReframeMarker{
						VideoReframeRect: VideoReframeRect{X: i, Y: i % 32, Width: 36, Height: 64},
						TimeSeconds:      float64(i) / 25, InterpolationToNext: "smooth",
					}
				}
			}
			dir := t.TempDir()
			source, output := filepath.Join(dir, "source.mkv"), filepath.Join(dir, "output.mp4")
			// Top half encodes absolute X in luma; bottom half encodes Y.
			// This lets us recover the actual crop origin from exported pixels.
			args := []string{"-v", "error", "-nostdin", "-f", "lavfi", "-i", "nullsrc=s=160x96:r=20:d=4,geq=lum='if(lt(Y,48),20+X,20+2*Y)':cb=128:cr=128,format=yuv444p"}
			if test.audio {
				args = append(args, "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=3.8")
			}
			filter := "setpts=PTS+" + videoReframeNumber(test.start) + "/TB"
			if test.vfr {
				filter = "select='not(eq(mod(n,3),1))'," + filter
			}
			args = append(args, "-vf", filter, "-fps_mode", "passthrough", "-c:v", "ffv1")
			if test.audio {
				args = append(args, "-af", "asetpts=PTS+"+videoReframeNumber(test.start+0.2)+"/TB", "-c:a", "pcm_s16le")
			}
			args = append(args, "-copyts", source)
			videoReframeRealCommand(t, ffmpeg, args...)
			sourceFrames := videoReframeProbeFile(t, ffprobe, source, true)
			if len(sourceFrames.Frames) == 0 {
				t.Fatal("generated source has no frames")
			}
			start := videoReframeTestSeconds(t, sourceFrames.Frames[0].Timestamp)
			renderArgs, err := videoReframeFFmpegArgs(params, source, output, start, test.audio)
			if err != nil {
				t.Fatal(err)
			}
			videoReframeRealCommand(t, ffmpeg, append([]string{"-v", "error"}, renderArgs...)...)
			result := videoReframeProbeFile(t, ffprobe, output, true)
			if len(result.Frames) != len(sourceFrames.Frames) {
				t.Fatalf("frame count changed: source %d, output %d", len(sourceFrames.Frames), len(result.Frames))
			}
			if len(result.Streams) != 1 || result.Streams[0].Width != params.Output.Width || result.Streams[0].Height != params.Output.Height {
				t.Fatalf("unexpected output geometry: %+v", result.Streams)
			}
			pixels := videoReframeRealCommand(t, ffmpeg, "-v", "error", "-i", output, "-map", "0:v:0", "-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1")
			w, h := params.Output.Width, params.Output.Height
			frameSize := w * h * 3 / 2
			if len(pixels) != len(result.Frames)*frameSize {
				t.Fatalf("unexpected decoded frame bytes: %d", len(pixels))
			}
			maxPixelDrift, maxTimeDrift := 0.0, 0.0
			for i, frame := range result.Frames {
				at := videoReframeTestSeconds(t, sourceFrames.Frames[i].Timestamp) - start
				gotTime := videoReframeTestSeconds(t, frame.Timestamp)
				maxTimeDrift = math.Max(maxTimeDrift, math.Abs(gotTime-at))
				want, err := evaluateVideoReframe(params, at)
				if err != nil {
					t.Fatal(err)
				}
				luma := pixels[i*frameSize : i*frameSize+w*h]
				// Average a 3x3 patch to tolerate H.264's small quantization error.
				average := func(cx, cy int) float64 {
					sum := 0
					for y := cy - 1; y <= cy+1; y++ {
						for x := cx - 1; x <= cx+1; x++ {
							sum += int(luma[y*w+x])
						}
					}
					return float64(sum) / 9
				}
				gotX := average(4, 4) - 20 - 4
				gotY := (average(4, h-8)-20)/2 - float64(h-8)
				drift := math.Max(math.Abs(gotX-float64(want.X)), math.Abs(gotY-float64(want.Y)))
				maxPixelDrift = math.Max(maxPixelDrift, drift)
				if drift > 2 {
					t.Fatalf("frame %d at %.3fs crop drift %.3fpx: decoded (%.2f, %.2f), expected (%d, %d)", i, at, drift, gotX, gotY, want.X, want.Y)
				}
			}
			if maxTimeDrift > 0.1 {
				t.Fatalf("timestamp drift %.3fs", maxTimeDrift)
			}
			allStreams := videoReframeProbeFile(t, ffprobe, output, false)
			duration := videoReframeTestSeconds(t, allStreams.Format.Duration)
			if math.Abs(duration-params.Source.DurationSeconds) > 0.1 {
				t.Fatalf("duration %.3fs differs from source duration %.3fs", duration, params.Source.DurationSeconds)
			}
			audioFound := false
			for _, stream := range allStreams.Streams {
				if stream.CodecType == "audio" {
					audioFound = true
					audioStart := videoReframeTestSeconds(t, stream.StartTime)
					if math.Abs(audioStart-0.2) > 0.1 {
						t.Fatalf("lost intentional audio offset: %.3fs", audioStart)
					}
				}
			}
			if audioFound != test.audio {
				t.Fatalf("audio presence %v, want %v", audioFound, test.audio)
			}
			t.Log(fmt.Sprintf("%d frames, max crop drift %.3fpx, max timestamp drift %.4fs, duration %.3fs; audio=%v", len(result.Frames), maxPixelDrift, maxTimeDrift, duration, audioFound))
		})
	}
}
