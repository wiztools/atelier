package main

import (
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

func TestProjectNoteAspectRatios(t *testing.T) {
	cases := []struct {
		name         string
		notes        string
		image, video string
	}{
		// conv_357cd8af5943e138ab8a9178's note, verbatim.
		{"video-scoped sentence", "All videos to be generated in pixar 3d style in aspect ratio 9:16.", "", "9:16"},
		{"unscoped binds both", "Everything in 4:3, warm palette.", "4:3", "4:3"},
		{"image-scoped sentence", "Posters should be 2:3. Keep the brand palette.", "2:3", ""},
		{"split scopes", "Images at 1:1.\nVideos at 9:16.", "1:1", "9:16"},
		{"one clause naming both kinds is ambiguous", "Images 1:1 and videos 9:16", "", ""},
		{"contradiction is ambiguous", "Videos in 9:16, never 16:9.", "", ""},
		{"same ratio repeated is fine", "Videos at 9:16. Every clip must be 9:16!", "", "9:16"},
		{"spaced colon", "Render video at 16 : 9", "", "16:9"},
		{"timestamps are not ratios", "Videos should cut at 10:30 and 0:45.", "", ""},
		{"no ratio", "Pixar 3D style, vertical framing.", "", ""},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			image, video := projectNoteAspectRatios(tc.notes)
			if image != tc.image || video != tc.video {
				t.Fatalf("projectNoteAspectRatios(%q) = (%q, %q), want (%q, %q)", tc.notes, image, video, tc.image, tc.video)
			}
		})
	}
}

func TestApplyProjectGenerationDefaults(t *testing.T) {
	base := defaultAppConfig()
	base.Generation.Image.AspectRatio = "16:9"
	base.Generation.Video.AspectRatio = "16:9"

	got := applyProjectGenerationDefaults(base, "All videos to be generated in pixar 3d style in aspect ratio 9:16.")
	if got.Generation.Video.AspectRatio != "9:16" {
		t.Fatalf("video aspect = %q, want the project's 9:16", got.Generation.Video.AspectRatio)
	}
	if got.Generation.Image.AspectRatio != "16:9" {
		t.Fatalf("image aspect = %q, want Settings' 16:9 untouched by a video-only note", got.Generation.Image.AspectRatio)
	}
	if base.Generation.Video.AspectRatio != "16:9" {
		t.Fatal("applyProjectGenerationDefaults mutated the caller's config")
	}

	// The conversation override is applied after the project default and
	// must outrank it.
	overridden, _, err := overlayModelOverrides(got, ChatRequest{}, ConversationModelOverrides{VideoAspectRatio: "1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.Generation.Video.AspectRatio != "1:1" {
		t.Fatalf("video aspect after conversation override = %q, want 1:1", overridden.Generation.Video.AspectRatio)
	}
}

func TestToolVideoResultMeasureVideoShape(t *testing.T) {
	var landscape ToolVideoResult
	landscape.measureVideoShape(mediasnifftest.MP4WithVideoTrack(1920, 1080))
	if landscape.Width != 1920 || landscape.Height != 1080 || landscape.AspectRatio != "16:9" {
		t.Fatalf("landscape shape = %dx%d %q, want 1920x1080 16:9", landscape.Width, landscape.Height, landscape.AspectRatio)
	}
	var portrait ToolVideoResult
	portrait.measureVideoShape(mediasnifftest.MP4WithVideoTrack(720, 1280))
	if portrait.AspectRatio != "9:16" {
		t.Fatalf("portrait aspect = %q, want 9:16", portrait.AspectRatio)
	}
	var unreadable ToolVideoResult
	unreadable.measureVideoShape([]byte("not an mp4"))
	if unreadable.Width != 0 || unreadable.Height != 0 || unreadable.AspectRatio != "" {
		t.Fatalf("unreadable bytes measured %+v, want unset", unreadable)
	}
}
