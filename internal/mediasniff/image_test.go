package mediasniff

import (
	"encoding/binary"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

func TestImagePixelDimensions(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   []byte
		wantW  float64
		wantH  float64
		wantOK bool
	}{
		{name: "png", data: mediasnifftest.Image(t, "png", 64, 48), wantW: 64, wantH: 48, wantOK: true},
		{name: "jpeg", data: mediasnifftest.Image(t, "jpeg", 64, 48), wantW: 64, wantH: 48, wantOK: true},
		{name: "gif", data: mediasnifftest.Image(t, "gif", 64, 48), wantW: 64, wantH: 48, wantOK: true},
		{name: "not an image", data: []byte("plain text"), wantOK: false},
		{name: "empty", data: nil, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, ok := ImagePixelDimensions(tt.data)
			if ok != tt.wantOK || (tt.wantOK && (w != tt.wantW || h != tt.wantH)) {
				t.Fatalf("ImagePixelDimensions = (%v, %v, %v), want (%v, %v, %v)", w, h, ok, tt.wantW, tt.wantH, tt.wantOK)
			}
		})
	}
	// WebP's three chunk layouts, hand-built to the container spec (no stdlib
	// encoder): VP8X carries the canvas size, VP8/VP8L their own dimensions.
	vp8x := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00"),
		byte(63), 0, 0, // width-1 = 63 → 64
		byte(47), 0, 0) // height-1 = 47 → 48
	if w, h, ok := ImagePixelDimensions(vp8x); !ok || w != 64 || h != 48 {
		t.Fatalf("VP8X dimensions = (%v, %v, %v), want (64, 48, true)", w, h, ok)
	}
	vp8 := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 \x0a\x00\x00\x00\x00\x00\x00\x9d\x01\x2a"),
		byte(64), 0, // width = 64
		byte(48), 0) // height = 48
	if w, h, ok := ImagePixelDimensions(vp8); !ok || w != 64 || h != 48 {
		t.Fatalf("VP8 dimensions = (%v, %v, %v), want (64, 48, true)", w, h, ok)
	}
	// VP8L packs (width-1)|(height-1)<<14 into 32 bits after its 0x2f marker.
	// Padded past the 30-byte webp guard — real files carry much more.
	vp8l := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x05\x00\x00\x00\x2f"),
		append(binary.LittleEndian.AppendUint32(nil, 63|47<<14), make([]byte, 5)...)...)
	if w, h, ok := ImagePixelDimensions(vp8l); !ok || w != 64 || h != 48 {
		t.Fatalf("VP8L dimensions = (%v, %v, %v), want (64, 48, true)", w, h, ok)
	}
}

func TestJPEGEXIFOrientation(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want int
	}{
		{"exif orientation 6", mediasnifftest.EXIFJPEG(6), 6},
		{"exif orientation 1", mediasnifftest.EXIFJPEG(1), 1},
		{"exif orientation 8", mediasnifftest.EXIFJPEG(8), 8},
		{"orientation out of range fails safe", mediasnifftest.EXIFJPEG(9), 1},
		{"plain JFIF jpeg has none", mediasnifftest.Image(t, "jpeg", 2, 2), 1},
		{"png bytes are not jpeg", mediasnifftest.Image(t, "png", 2, 2), 0},
		{"truncated payload is not a jpeg", []byte{0xFF, 0xD8, 0xFF}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := JPEGEXIFOrientation(tc.data); got != tc.want {
				t.Fatalf("JPEGEXIFOrientation = %d, want %d", got, tc.want)
			}
		})
	}
}
