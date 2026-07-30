package main

import (
	"encoding/base64"
	"testing"
)

// Tiny real PNGs (8-bit grayscale) generated so dimension/aspect detection can
// be tested without binary fixtures. Each decodes to the listed pixel size.
const (
	// 9x16 portrait
	png9x16B64 = "iVBORw0KGgoAAAANSUhEUgAAAAkAAAAQCAAAAADhIwqfAAAADElEQVR4nGNgGNwAAACgAAGwBmIYAAAAAElFTkSuQmCC"
	// 16x9 landscape
	png16x9B64 = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAJCAAAAAAeQfPuAAAADElEQVR4nGNgGKQAAACZAAFEOyJLAAAAAElFTkSuQmCC"
	// 1x1 square
	png1x1B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGNgAAAAAgABSK+kcQAAAABJRU5ErkJggg=="
	// 4x3
	png4x3B64 = "iVBORw0KGgoAAAANSUhEUgAAAAQAAAADCAAAAACRn/EaAAAAC0lEQVR4nGNgQAEAAA8AAbVWKT4AAAAASUVORK5CYII="
	// 3x4
	png3x4B64 = "iVBORw0KGgoAAAANSUhEUgAAAAMAAAAECAAAAABuRtrbAAAAC0lEQVR4nGNgQAUAABAAATm9j2UAAAAASUVORK5CYII="
)

func TestImageDimensions(t *testing.T) {
	cases := []struct {
		name   string
		b64    string
		wantW  int
		wantH  int
		wantOK bool
	}{
		{"portrait 9x16", png9x16B64, 9, 16, true},
		{"landscape 16x9", png16x9B64, 16, 9, true},
		{"square 1x1", png1x1B64, 1, 1, true},
		{"4x3", png4x3B64, 4, 3, true},
		{"3x4", png3x4B64, 3, 4, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := base64.StdEncoding.DecodeString(tc.b64)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			w, h, ok := imageDimensions(data)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if w != tc.wantW || h != tc.wantH {
				t.Fatalf("dims = (%d,%d), want (%d,%d)", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestImageDimensionsInvalid(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte("not an image"), []byte{0x89, 0x50}} {
		if _, _, ok := imageDimensions(data); ok {
			t.Fatalf("imageDimensions(%v) = ok, want false", data)
		}
	}
}

func TestAspectRatioForPixels(t *testing.T) {
	cases := []struct {
		w, h int
		want string
	}{
		{9, 16, "9:16"},
		{1152, 2048, "9:16"}, // the conv_26cc3f515d6d645b316763cb source frame
		{16, 9, "16:9"},
		{1920, 1080, "16:9"},
		{1, 1, "1:1"},
		{4, 3, "4:3"},
		{3, 4, "3:4"},
		{3, 2, "3:2"},
		{2, 3, "2:3"},
		{21, 9, "21:9"},
		{7, 3, "21:9"}, // exactly 21:9 (7/3 == 21/9)
		// Within 3% tolerance.
		{160, 90, "16:9"}, // exactly 16:9
		{161, 90, "16:9"}, // ~0.6% off, within band
		// Outside any preset → "".
		{5, 1, ""},
		{0, 0, ""},
	}
	for _, tc := range cases {
		got := aspectRatioForPixels(tc.w, tc.h)
		if got != tc.want {
			t.Errorf("aspectRatioForPixels(%d,%d) = %q, want %q", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestAspectRatioFromImage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare base64 portrait", png9x16B64, "9:16"},
		{"data-URI portrait", "data:image/png;base64," + png9x16B64, "9:16"},
		{"bare base64 landscape", png16x9B64, "16:9"},
		{"bare base64 square", png1x1B64, "1:1"},
		{"empty", "", ""},
		{"garbage", "not-an-image", ""},
		{"unparseable data uri", "data:image/png;base64,!!!", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aspectRatioFromImage(tc.in); got != tc.want {
				t.Fatalf("aspectRatioFromImage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
