package main

import (
	"math"
	"strings"
	"testing"
)

func trimTestParams() VideoTrimParams {
	return VideoTrimParams{
		Version: 1,
		Source:  VideoReframeSource{Width: 1920, Height: 1080, DurationSeconds: 10},
		Segments: []VideoTrimSegment{
			{StartSeconds: 0, EndSeconds: 2},
			{StartSeconds: 5, EndSeconds: 8},
		},
	}
}

func TestVideoTrimValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*VideoTrimParams)
		wantErr string
	}{
		{"valid", func(*VideoTrimParams) {}, ""},
		{"wrong version", func(p *VideoTrimParams) { p.Version = 2 }, "unsupported trim version"},
		{"zero dimensions", func(p *VideoTrimParams) { p.Source.Width = 0 }, "valid source dimensions"},
		{"zero duration", func(p *VideoTrimParams) { p.Source.DurationSeconds = 0 }, "valid source dimensions"},
		{"no segments", func(p *VideoTrimParams) { p.Segments = nil }, "between 1 and"},
		{
			"too many segments",
			func(p *VideoTrimParams) {
				p.Segments = nil
				for i := 0; i <= videoTrimMaxSegments; i++ {
					p.Segments = append(p.Segments, VideoTrimSegment{StartSeconds: float64(i), EndSeconds: float64(i) + 0.5})
				}
			},
			"between 1 and",
		},
		{"empty segment", func(p *VideoTrimParams) { p.Segments[0] = VideoTrimSegment{StartSeconds: 1, EndSeconds: 1} }, "non-empty ranges"},
		{"inverted segment", func(p *VideoTrimParams) { p.Segments[0] = VideoTrimSegment{StartSeconds: 3, EndSeconds: 2} }, "non-empty ranges"},
		{"past the end", func(p *VideoTrimParams) { p.Segments[1].EndSeconds = 11 }, "non-empty ranges"},
		{"negative start", func(p *VideoTrimParams) { p.Segments[0].StartSeconds = -0.5 }, "non-empty ranges"},
		{"nan bound", func(p *VideoTrimParams) { p.Segments[0].EndSeconds = math.NaN() }, "finite"},
		{"overlapping", func(p *VideoTrimParams) { p.Segments[1].StartSeconds = 1.5 }, "must not overlap or touch"},
		{"touching", func(p *VideoTrimParams) { p.Segments[1].StartSeconds = 2 }, "must not overlap or touch"},
		{"sub-millisecond gap", func(p *VideoTrimParams) { p.Segments[1].StartSeconds = 2.0005 }, "must not overlap or touch"},
		{"unsorted input is sorted", func(p *VideoTrimParams) {
			p.Segments[0], p.Segments[1] = p.Segments[1], p.Segments[0]
		}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := trimTestParams()
			test.mutate(&params)
			got, err := validateVideoTrim(params)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got.Segments[0].StartSeconds != 0 {
					t.Fatalf("segments not sorted: %+v", got.Segments)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestVideoTrimKeptSeconds(t *testing.T) {
	kept, err := videoTrimKeptSeconds(trimTestParams())
	if err != nil || math.Abs(kept-5) > 1e-9 {
		t.Fatalf("got %g, %v; want 5", kept, err)
	}
	if _, err := videoTrimKeptSeconds(VideoTrimParams{Version: 1}); err == nil {
		t.Fatal("invalid params must be refused")
	}
}

func TestVideoTrimFFmpegArgs(t *testing.T) {
	params := trimTestParams()

	t.Run("silent clip", func(t *testing.T) {
		args, err := videoTrimFFmpegArgs(params, "in.mp4", "out.mp4", false)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"[0:v]trim=start=0:end=2,setpts=PTS-STARTPTS[v0]",
			"[0:v]trim=start=5:end=8,setpts=PTS-STARTPTS[v1]",
			"[v0][v1]concat=n=2:v=1:a=0[outv]",
			"-noautorotate", "-map", "[outv]", "-an", "-c:v", "libx264", "-crf", "18",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("args missing %q: %s", want, joined)
			}
		}
		if strings.Contains(joined, "atrim") {
			t.Fatalf("silent clip must have no audio chains: %s", joined)
		}
	})

	t.Run("audio clip", func(t *testing.T) {
		args, err := videoTrimFFmpegArgs(params, "in.mp4", "out.mp4", true)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"[0:a]atrim=start=0:end=2,asetpts=PTS-STARTPTS[a0]",
			"[0:a]atrim=start=5:end=8,asetpts=PTS-STARTPTS[a1]",
			"[v0][a0][v1][a1]concat=n=2:v=1:a=1[outv]",
			"-c:a", "aac", "-b:a", "192k",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("args missing %q: %s", want, joined)
			}
		}
	})

	t.Run("guards", func(t *testing.T) {
		if _, err := videoTrimFFmpegArgs(trimTestParams(), "same.mp4", "same.mp4", false); err == nil {
			t.Fatal("identical input/output must be refused")
		}
		if _, err := videoTrimFFmpegArgs(VideoTrimParams{Version: 1}, "in.mp4", "out.mp4", false); err == nil {
			t.Fatal("invalid params must be refused")
		}
	})
}

func TestVideoTrimSummary(t *testing.T) {
	if got := videoTrimSummary(trimTestParams()); got != "2 segment(s), 5.0s kept" {
		t.Fatalf("got %q", got)
	}
	if got := videoTrimSummary(VideoTrimParams{Version: 1}); got != "trim" {
		t.Fatalf("got %q", got)
	}
}
