package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

// dataURIPNG encodes a real PNG of the given dimensions and returns it as a
// "data:image/png;base64,..." URI. generate_image's aspect-ratio inheritance
// decodes the attached image to read its real pixel dimensions, so the test
// fixture must be a genuinely decodable image (not the placeholder
// "data:image/png;base64,ABC" used elsewhere) for aspectRatioFromImage to
// return a non-empty ratio.
func dataURIPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode %dx%d png: %v", w, h, err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestGenerateImageToolAspectRatioInheritance is the regression test for
// conv_65bc186e6035c885660b369c: a single portrait source image used to come
// back landscape because the configured default ("16:9") was sent to edit
// models that honor image_size. The fix: with no explicit aspectRatio on the
// call and exactly one attached image, the output inherits the source frame's
// orientation.
func TestGenerateImageToolAspectRatioInheritance(t *testing.T) {
	// A 3:4 portrait frame — aspectRatioFromImage maps it to "3:4".
	portrait := dataURIPNG(t, 150, 200)
	// A 16:9 landscape frame.
	landscape := dataURIPNG(t, 160, 90)

	tests := []struct {
		name           string
		call           HarnessToolCall
		attached       []string
		configRatio    string
		wantRatio      string // captured.AspectRatio expectation
		wantInheritLog string // optional substring check, for clarity
	}{
		{
			name: "single portrait image inherits 3:4 over a 16:9 config default",
			call: HarnessToolCall{Content: "make her play beach volleyball"},
			// 150x200 is portrait; defaultAppConfig.Generation.Image.AspectRatio
			// is "1:1", so use a 16:9 config default to prove the source wins.
			attached:    []string{portrait},
			configRatio: "16:9",
			wantRatio:   "3:4",
		},
		{
			name:        "single landscape image inherits 16:9 over a 1:1 config default",
			call:        HarnessToolCall{Content: "widen this shot"},
			attached:    []string{landscape},
			configRatio: "1:1",
			wantRatio:   "16:9",
		},
		{
			name:        "explicit aspectRatio on the call wins over a portrait source",
			call:        HarnessToolCall{Content: "square crop please", AspectRatio: "1:1"},
			attached:    []string{portrait},
			configRatio: "16:9",
			wantRatio:   "1:1",
		},
		{
			name: "two images do not inherit: no authoritative frame, config wins",
			call: HarnessToolCall{Content: "blend these two"},
			// A portrait + a landscape: picking either would be arbitrary, so
			// multi-image falls back to the configured default.
			attached:    []string{portrait, landscape},
			configRatio: "16:9",
			wantRatio:   "16:9",
		},
		{
			name:        "no attached image: text-to-image uses the configured default",
			call:        HarnessToolCall{Content: "a lighthouse at dusk"},
			attached:    nil,
			configRatio: "16:9",
			wantRatio:   "16:9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured ImageGenerateRequest
			tools := HarnessToolExecutionContext{
				Config:         AppConfig{Models: ConfigModels{ImageProvider: "fal"}, Generation: ConfigGeneration{Image: ConfigImageGeneration{AspectRatio: tc.configRatio}}},
				AttachedImages: tc.attached,
				GenerateImage: func(_ context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, []string, error) {
					captured = req
					return ollamaGenerateResponse{Image: "data:image/png;base64,iVBORw0KGgo=", Done: true}, nil, nil, nil
				},
			}

			def := imageGenerationToolDefinition()
			if _, _, err := def.Execute(t.Context(), tools, tc.call); err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if captured.AspectRatio != tc.wantRatio {
				t.Errorf("AspectRatio = %q, want %q\nattached=%d configRatio=%q call.AspectRatio=%q",
					captured.AspectRatio, tc.wantRatio, len(tc.attached), tc.configRatio, tc.call.AspectRatio)
			}
		})
	}
}

// TestGenerateImageToolAspectRatioInheritsSourceNotPixels is a focused check
// that inheritance drives a *portrait* dimension pair for a portrait source —
// the concrete shape that was wrong in conv_65bc186e6035c885660b369c, where a
// portrait input produced 2048x1152 (landscape). imageSizeForPresetAndRatio
// turns the inherited "3:4" ratio into portrait pixels.
func TestGenerateImageToolAspectRatioInheritsSourceNotPixels(t *testing.T) {
	portrait := dataURIPNG(t, 150, 200)
	var captured ImageGenerateRequest
	tools := HarnessToolExecutionContext{
		Config:         AppConfig{Models: ConfigModels{ImageProvider: "fal"}, Generation: ConfigGeneration{Image: ConfigImageGeneration{AspectRatio: "16:9"}}},
		AttachedImages: []string{portrait},
		GenerateImage: func(_ context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, []string, error) {
			captured = req
			return ollamaGenerateResponse{Image: "data:image/png;base64,iVBORw0KGgo=", Done: true}, nil, nil, nil
		},
	}

	if _, _, err := imageGenerationToolDefinition().Execute(t.Context(), tools, HarnessToolCall{Content: "transform"}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if captured.AspectRatio != "3:4" {
		t.Fatalf("AspectRatio = %q, want 3:4 inherited from the portrait source", captured.AspectRatio)
	}
	if captured.Height <= captured.Width {
		t.Errorf("dimensions = %dx%d, want portrait (height > width) for a 3:4 source", captured.Width, captured.Height)
	}
	// Sanity: ensure the harness didn't silently fall back to the configured
	// landscape default — width must not exceed height.
	if strings.HasPrefix(captured.AspectRatio, "16:9") {
		t.Errorf("regression: ratio fell back to configured 16:9 instead of inheriting 3:4")
	}
}
