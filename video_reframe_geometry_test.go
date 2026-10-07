package main

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

type videoReframeFixtures struct {
	Fit []struct {
		Name                      string
		SourceWidth, SourceHeight int
		AspectRatio               string
		Expected                  VideoReframeRect
	}
	Timelines []struct {
		Name    string
		Params  VideoReframeParams
		Samples []struct {
			TimeSeconds float64
			Expected    VideoReframeRect
		}
	}
	RatioChanges []struct {
		Name        string
		Params      VideoReframeParams
		AspectRatio string
		Expected    VideoReframeParams
	}
}

func loadVideoReframeFixtures(t *testing.T) videoReframeFixtures {
	t.Helper()
	data, err := os.ReadFile("frontend/test/fixtures/videoReframe.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures videoReframeFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func TestVideoReframeSharedFixtures(t *testing.T) {
	fixtures := loadVideoReframeFixtures(t)
	for _, test := range fixtures.Fit {
		t.Run(test.Name, func(t *testing.T) {
			got, err := fitVideoReframeCrop(test.SourceWidth, test.SourceHeight, test.AspectRatio)
			if err != nil || got != test.Expected {
				t.Fatalf("got %+v, %v; want %+v", got, err, test.Expected)
			}
		})
	}
	for _, test := range fixtures.Timelines {
		t.Run(test.Name, func(t *testing.T) {
			for _, sample := range test.Samples {
				got, err := evaluateVideoReframe(test.Params, sample.TimeSeconds)
				if err != nil || got != sample.Expected {
					t.Fatalf("at %g got %+v, %v; want %+v", sample.TimeSeconds, got, err, sample.Expected)
				}
			}
		})
	}
	for _, test := range fixtures.RatioChanges {
		t.Run(test.Name, func(t *testing.T) {
			got, err := changeVideoReframeAspect(test.Params, test.AspectRatio)
			if err != nil || !reflect.DeepEqual(got, test.Expected) {
				t.Fatalf("got %+v, %v; want %+v", got, err, test.Expected)
			}
		})
	}
}

func TestVideoReframeValidation(t *testing.T) {
	base := loadVideoReframeFixtures(t).Timelines[0].Params
	tests := []struct {
		name   string
		mutate func(*VideoReframeParams)
	}{
		{"version", func(p *VideoReframeParams) { p.Version = 2 }},
		{"ratio", func(p *VideoReframeParams) { p.AspectRatio = "4:3" }},
		{"source-size", func(p *VideoReframeParams) { p.Source.Width = 1 }},
		{"huge-source", func(p *VideoReframeParams) { p.Source.Width = 32769 }},
		{"nan-duration", func(p *VideoReframeParams) { p.Source.DurationSeconds = math.NaN() }},
		{"infinite-duration", func(p *VideoReframeParams) { p.Source.DurationSeconds = math.Inf(1) }},
		{"zero-duration", func(p *VideoReframeParams) { p.Source.DurationSeconds = 0 }},
		{"odd-output", func(p *VideoReframeParams) { p.Output.Width = 35 }},
		{"wrong-output-ratio", func(p *VideoReframeParams) { p.Output.Width = 38 }},
		{"empty", func(p *VideoReframeParams) { p.Markers = nil }},
		{"too-many", func(p *VideoReframeParams) { p.Markers = make([]VideoReframeMarker, 101) }},
		{"missing-zero", func(p *VideoReframeParams) { p.Markers[0].TimeSeconds = 1 }},
		{"nan-time", func(p *VideoReframeParams) { p.Markers[0].TimeSeconds = math.NaN() }},
		{"infinite-time", func(p *VideoReframeParams) { p.Markers[0].TimeSeconds = math.Inf(1) }},
		{"negative-time", func(p *VideoReframeParams) { p.Markers[0].TimeSeconds = -1 }},
		{"past-end", func(p *VideoReframeParams) { p.Markers[1].TimeSeconds = 5 }},
		{"duplicate", func(p *VideoReframeParams) { p.Markers[1].TimeSeconds = 0 }},
		{"resize", func(p *VideoReframeParams) { p.Markers[1].Width = 18; p.Markers[1].Height = 32 }},
		{"odd-crop", func(p *VideoReframeParams) { p.Markers[0].Width = 27; p.Markers[0].Height = 48 }},
		{"negative-position", func(p *VideoReframeParams) { p.Markers[0].X = -1 }},
		{"out-of-bounds", func(p *VideoReframeParams) { p.Markers[0].Y = 33 }},
		{"huge-crop", func(p *VideoReframeParams) { p.Markers[0].Width = 360; p.Markers[0].Height = 640 }},
		{"mode", func(p *VideoReframeParams) { p.Markers[0].InterpolationToNext = "t);evil" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := base
			p.Markers = append([]VideoReframeMarker(nil), base.Markers...)
			test.mutate(&p)
			if _, err := validateVideoReframe(p); err == nil {
				t.Fatal("accepted invalid parameters")
			}
			if _, err := videoReframeFFmpegArgs(p, "in.mp4", "out.mp4", 0, false); err == nil {
				t.Fatal("built render for invalid parameters")
			}
		})
	}
	if _, err := evaluateVideoReframe(base, math.NaN()); err == nil {
		t.Fatal("accepted NaN preview time")
	}
	if _, err := evaluateVideoReframe(base, math.Inf(1)); err == nil {
		t.Fatal("accepted infinite preview time")
	}
	if _, err := fitVideoReframeCrop(10, 10, "9:16"); err == nil {
		t.Fatal("accepted too-small source")
	}
	// Integer JSON fields cannot silently accept fractions at the IPC boundary.
	var fractional VideoReframeParams
	if err := json.Unmarshal([]byte(`{"source":{"width":160.5}}`), &fractional); err == nil {
		t.Fatal("accepted fractional dimension")
	}
	if err := json.Unmarshal([]byte(`{"markers":[{"x":1.5}]}`), &fractional); err == nil {
		t.Fatal("accepted fractional coordinate")
	}
}

func TestVideoReframeValidationOwnsMarkers(t *testing.T) {
	params := loadVideoReframeFixtures(t).Timelines[1].Params
	canonical, err := validateVideoReframe(params)
	if err != nil {
		t.Fatal(err)
	}
	if params.Markers[0].TimeSeconds != 1 || canonical.Markers[0].TimeSeconds != 0 {
		t.Fatal("sort mutated input or did not sort")
	}
	canonical.Markers[0].X = 5
	if params.Markers[1].X != 101 {
		t.Fatal("canonical markers alias input")
	}
	changed, err := changeVideoReframeAspect(params, "1:1")
	if err != nil {
		t.Fatal(err)
	}
	changed.Markers[0].X = 2
	if params.Markers[1].Width != 36 {
		t.Fatal("ratio change mutated input")
	}
}

func TestVideoReframeFFmpegArgs(t *testing.T) {
	p := loadVideoReframeFixtures(t).Timelines[0].Params
	args, err := videoReframeFFmpegArgs(p, "source with spaces.mp4", "output.mp4", 5, true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-copyts", "setpts=PTS-(5)/TB", "asetpts=PTS-(5)/TB", "exact=1", "scale=36:64", "-fps_mode passthrough", "-map 0:a:0", "-n"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if strings.Contains(joined, "STARTPTS") || strings.Contains(joined, "-r ") {
		t.Fatal("independent timestamp reset or forced frame rate")
	}
	args, err = videoReframeFFmpegArgs(p, "in.mp4", "out.mp4", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "-an") {
		t.Fatal("silent input did not disable audio")
	}
	for _, start := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := videoReframeFFmpegArgs(p, "in", "out", start, false); err == nil {
			t.Fatal("accepted nonfinite start")
		}
	}
	if _, err := videoReframeFFmpegArgs(p, "in", "in", 0, false); err == nil {
		t.Fatal("accepted overwriting source")
	}
}
