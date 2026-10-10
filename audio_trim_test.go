package main

import (
	"math"
	"strings"
	"testing"
)

func audioTrimTestParams() AudioTrimParams {
	return AudioTrimParams{
		Version: 1,
		Source:  AudioTrimSource{DurationSeconds: 10},
		Segments: []AudioTrimSegment{
			{StartSeconds: 0, EndSeconds: 2},
			{StartSeconds: 5, EndSeconds: 8},
		},
	}
}

func TestAudioTrimValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*AudioTrimParams)
		wantErr string
	}{
		{"valid", func(*AudioTrimParams) {}, ""},
		{"wrong version", func(p *AudioTrimParams) { p.Version = 2 }, "unsupported trim version"},
		{"zero duration", func(p *AudioTrimParams) { p.Source.DurationSeconds = 0 }, "finite positive source duration"},
		{"negative duration", func(p *AudioTrimParams) { p.Source.DurationSeconds = -1 }, "finite positive source duration"},
		{"no segments", func(p *AudioTrimParams) { p.Segments = nil }, "between 1 and"},
		{
			"too many segments",
			func(p *AudioTrimParams) {
				p.Segments = nil
				for i := 0; i <= audioTrimMaxSegments; i++ {
					p.Segments = append(p.Segments, AudioTrimSegment{StartSeconds: float64(i), EndSeconds: float64(i) + 0.5})
				}
			},
			"between 1 and",
		},
		{"empty segment", func(p *AudioTrimParams) { p.Segments[0] = AudioTrimSegment{StartSeconds: 1, EndSeconds: 1} }, "non-empty ranges"},
		{"inverted segment", func(p *AudioTrimParams) { p.Segments[0] = AudioTrimSegment{StartSeconds: 3, EndSeconds: 2} }, "non-empty ranges"},
		{"past the end", func(p *AudioTrimParams) { p.Segments[1].EndSeconds = 11 }, "non-empty ranges"},
		{"negative start", func(p *AudioTrimParams) { p.Segments[0].StartSeconds = -0.5 }, "non-empty ranges"},
		{"nan bound", func(p *AudioTrimParams) { p.Segments[0].EndSeconds = math.NaN() }, "finite"},
		{"overlapping", func(p *AudioTrimParams) { p.Segments[1].StartSeconds = 1.5 }, "must not overlap or touch"},
		{"touching", func(p *AudioTrimParams) { p.Segments[1].StartSeconds = 2 }, "must not overlap or touch"},
		{"sub-millisecond gap", func(p *AudioTrimParams) { p.Segments[1].StartSeconds = 2.0005 }, "must not overlap or touch"},
		{"unsorted input is sorted", func(p *AudioTrimParams) {
			p.Segments[0], p.Segments[1] = p.Segments[1], p.Segments[0]
		}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := audioTrimTestParams()
			test.mutate(&params)
			got, err := validateAudioTrim(params)
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

func TestAudioTrimKeptSeconds(t *testing.T) {
	kept, err := audioTrimKeptSeconds(audioTrimTestParams())
	if err != nil || math.Abs(kept-5) > 1e-9 {
		t.Fatalf("got %g, %v; want 5", kept, err)
	}
	if _, err := audioTrimKeptSeconds(AudioTrimParams{Version: 1}); err == nil {
		t.Fatal("invalid params must be refused")
	}
}

func TestAudioTrimFFmpegArgs(t *testing.T) {
	params := audioTrimTestParams()
	args, err := audioTrimFFmpegArgs(params, "in.mp3", "out.m4a")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"[0:a]atrim=start=0:end=2,asetpts=PTS-STARTPTS[a0]",
		"[0:a]atrim=start=5:end=8,asetpts=PTS-STARTPTS[a1]",
		"[a0][a1]concat=n=2:v=0:a=1[outa]",
		"-map", "[outa]", "-c:a", "aac", "-b:a", "192k",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "trim=") && strings.Contains(joined, "[0:v]") {
		t.Fatalf("audio render must have no video chains: %s", joined)
	}

	if _, err := audioTrimFFmpegArgs(audioTrimTestParams(), "same.mp3", "same.mp3"); err == nil {
		t.Fatal("identical input/output must be refused")
	}
	if _, err := audioTrimFFmpegArgs(AudioTrimParams{Version: 1}, "in.mp3", "out.m4a"); err == nil {
		t.Fatal("invalid params must be refused")
	}
}

func TestAudioTrimSummary(t *testing.T) {
	if got := audioTrimSummary(audioTrimTestParams()); got != "2 segment(s), 5.0s kept" {
		t.Fatalf("got %q", got)
	}
	if got := audioTrimSummary(AudioTrimParams{Version: 1}); got != "trim" {
		t.Fatalf("got %q", got)
	}
}

func TestAudioEditResultKindAndSummary(t *testing.T) {
	op := EditOperation{Kind: editOperationKindAudioTrim, ResultMimeType: "audio/mp4",
		AudioTrim: &AudioTrimParams{Version: 1, Source: AudioTrimSource{DurationSeconds: 10},
			Segments: []AudioTrimSegment{{StartSeconds: 0, EndSeconds: 4}}}}
	if kind := editResultMediaKind(op); kind != "audio" {
		t.Fatalf("editResultMediaKind = %q, want audio", kind)
	}
	if got := editOperationAdoptionSummary(op); got != "trim: 1 segment(s), 4.0s kept" {
		t.Fatalf("adoption summary = %q", got)
	}
	if kind := editResultMediaKind(EditOperation{Kind: editOperationKindAudioTrim}); kind != "audio" {
		t.Fatalf("payload-less audio trim must stay audio, got %q", kind)
	}
}
