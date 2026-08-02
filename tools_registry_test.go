package main

import (
	"strings"
	"testing"
)

// TestImageSizeForAspectRatio checks that named aspect ratios derive concrete
// width/height from the configured long-edge budget, rounded to a multiple of
// 16, and that an unknown ratio returns (0, 0) so the caller keeps its
// configured defaults.
func TestImageSizeForAspectRatio(t *testing.T) {
	cases := []struct {
		name     string
		baseLong int
		ratio    string
		wantW    int
		wantH    int
	}{
		{"square", 768, "1:1", 768, 768},
		{"landscape16x9", 768, "16:9", 768, 432},
		{"portrait9x16", 768, "9:16", 432, 768},
		{"landscape4x3", 768, "4:3", 768, 576},
		{"portrait3x4", 768, "3:4", 576, 768},
		{"landscape3x2", 768, "3:2", 768, 512},
		{"portrait2x3", 768, "2:3", 512, 768},
		{"cinematic21x9", 768, "21:9", 768, 336},
		{"unknownRatioFallsBack", 768, "totally-bogus", 0, 0},
		{"emptyRatioFallsBack", 768, "", 0, 0},
		{"shortEdgeFlooredAt256", 256, "16:9", 256, 256},
		{"zeroBaseUsesDefault", 0, "1:1", 1024, 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := imageSizeForAspectRatio(tc.baseLong, tc.ratio)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Errorf("imageSizeForAspectRatio(%d, %q) = (%d, %d), want (%d, %d)",
					tc.baseLong, tc.ratio, gotW, gotH, tc.wantW, tc.wantH)
			}
			if tc.wantW != 0 {
				if gotW%16 != 0 || gotH%16 != 0 {
					t.Errorf("dimensions not multiples of 16: (%d, %d)", gotW, gotH)
				}
			}
		})
	}
}

// TestGenerateVideoParamSchemaExposesDuration is the regression for
// conv_484449cf8fe4a13c1ffa6bb4: a request to "extend another 5s" produced a
// ~10s clip because the planner had no duration field to express the 5, so it
// landed only in the prompt text and fal applied its default. The video tool
// must expose a `duration` param (mirroring the audio tool) so the planner can
// carry an explicit clip length / extension length, and its description must
// flag the extend-vs-total distinction (otherwise a "5s" extend reads as total
// length and the same confusion recurs).
func TestGenerateVideoParamSchemaExposesDuration(t *testing.T) {
	schema := generateVideoParamSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", schema["properties"])
	}
	prop, ok := props["duration"].(map[string]any)
	if !ok {
		t.Fatalf("generate_video param schema must expose a `duration` property; got %v", props)
	}
	desc, _ := prop["description"].(string)
	if !strings.Contains(desc, "extension") || !strings.Contains(desc, "total") {
		t.Fatalf("duration description must distinguish extension length from total clip length for extend. got %q", desc)
	}
}
