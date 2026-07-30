package main

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// imageDimensions returns the pixel dimensions of a decoded image. It accepts
// PNG, JPEG, and GIF bytes (the formats the rest of the app treats as images —
// see isImageBytes). ok is false on any decode error; callers must treat that as
// "unknown" and fall back to a default rather than guessing.
//
// This is the inverse of imageSizeForAspectRatio, which goes ratio → pixels:
// here we read the actual frame so an image-to-video request can inherit the
// source's orientation instead of getting the configured ratio stamped on it
// (see conv_26cc3f515d6d645b316763cb, a 9:16 portrait image that came back 16:9).
func imageDimensions(data []byte) (w, h int, ok bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// aspectRatioFromImage decodes a single image — given as a data URI, a bare
// base64 string, or an "/atelier-artifact" path — and maps its pixel dimensions
// to the canonical ratio set the rest of the app uses
// (1:1, 16:9, 9:16, 4:3, 3:4, 3:2, 2:3, 21:9). It returns "" when the image
// can't be decoded or its ratio doesn't match a known preset within tolerance —
// it never guesses, so callers fall back to the configured default rather than
// sending an arbitrary ratio.
//
// Normalization (data-URI / base64 / artifact path → bytes) reuses
// decodeImagePayload so every form AttachedImages can carry is handled.
func aspectRatioFromImage(imageStr string) string {
	data, _, err := decodeImagePayload(imageStr)
	if err != nil {
		return ""
	}
	w, h, ok := imageDimensions(data)
	if !ok {
		return ""
	}
	return aspectRatioForPixels(w, h)
}

// aspectRatioForPixels maps (w, h) to the closest canonical ratio string,
// matching by the target's w/h ratio within a 3% tolerance. A near-exact
// tolerance (not just "closest") avoids mislabeling a 1280x720 frame as 4:3
// territory; anything outside the band returns "" so the caller keeps its
// configured default. Square is matched at exactly 1.0.
func aspectRatioForPixels(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	target := float64(w) / float64(h)
	type preset struct {
		name  string
		w, hr int
	}
	presets := []preset{
		{"1:1", 1, 1},
		{"16:9", 16, 9},
		{"9:16", 9, 16},
		{"4:3", 4, 3},
		{"3:4", 3, 4},
		{"3:2", 3, 2},
		{"2:3", 2, 3},
		{"21:9", 21, 9},
	}
	const tolerance = 0.03
	for _, p := range presets {
		r := float64(p.w) / float64(p.hr)
		if r == target {
			return p.name
		}
		lo, hi := r*(1-tolerance), r*(1+tolerance)
		if target >= lo && target <= hi {
			return p.name
		}
	}
	return ""
}
