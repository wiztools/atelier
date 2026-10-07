package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func editTestPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func editSolidImage(width, height int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// exifJPEGBytes crafts a minimal JPEG marker stream carrying an APP1 Exif
// segment whose IFD0 holds exactly one entry: the orientation tag. The parser
// only walks markers and the TIFF header, so no entropy-coded data is needed —
// the SOS marker terminates the walk.
func exifJPEGBytes(orientation uint16) []byte {
	tiff := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	tiff = append(tiff, 0x01, 0x00)             // one IFD entry
	tiff = append(tiff, 0x12, 0x01)             // tag 0x0112 (orientation)
	tiff = append(tiff, 0x03, 0x00)             // type SHORT
	tiff = append(tiff, 0x01, 0x00, 0x00, 0x00) // count 1
	tiff = append(tiff, byte(orientation), 0x00, 0x00, 0x00)
	tiff = append(tiff, 0x00, 0x00, 0x00, 0x00) // next IFD offset
	exif := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xFF, 0xD8}
	out = append(out, 0xFF, 0xE1, 0x00, byte(len(exif)+2))
	out = append(out, exif...)
	return append(out, 0xFF, 0xDA)
}

func TestJPEGEXIFOrientation(t *testing.T) {
	plainJPEG := &bytes.Buffer{}
	if err := jpeg.Encode(plainJPEG, editSolidImage(2, 2, color.RGBA{R: 255, A: 255}), nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	cases := []struct {
		name string
		data []byte
		want int
	}{
		{"exif orientation 6", exifJPEGBytes(6), 6},
		{"exif orientation 1", exifJPEGBytes(1), 1},
		{"exif orientation 8", exifJPEGBytes(8), 8},
		{"orientation out of range fails safe", exifJPEGBytes(9), 1},
		{"plain JFIF jpeg has none", plainJPEG.Bytes(), 1},
		{"png bytes are not jpeg", editTestPNG(t, editSolidImage(2, 2, color.RGBA{R: 255, A: 255})), 0},
		{"truncated payload is not a jpeg", []byte{0xFF, 0xD8, 0xFF}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jpegEXIFOrientation(tc.data); got != tc.want {
				t.Fatalf("jpegEXIFOrientation = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestApplyEXIFOrientation(t *testing.T) {
	// A 2x1 row: red at (0,0), blue at (1,0). Each EXIF transform's expected
	// destination is derived from the mapping in the code's comment.
	red := color.RGBA{R: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, red)
	src.Set(1, 0, blue)
	at := func(img image.Image, x, y int) color.Color { return img.At(x, y) }
	cases := []struct {
		orientation int
		width       int
		height      int
		first       color.Color // (0,0)
		second      color.Color // position depends on shape; 1x2 column below, else (1,0)
		secondX     int
		secondY     int
	}{
		{2, 2, 1, blue, red, 1, 0}, // flip horizontal
		{3, 2, 1, blue, red, 1, 0}, // rotate 180
		{4, 2, 1, red, blue, 1, 0}, // flip vertical (single row unchanged)
		{5, 1, 2, red, blue, 0, 1}, // transpose
		{6, 1, 2, red, blue, 0, 1}, // rotate 90 CW
		{7, 1, 2, blue, red, 0, 1}, // transverse
		{8, 1, 2, blue, red, 0, 1}, // rotate 270 CCW
	}
	for _, tc := range cases {
		t.Run(string(rune('0'+tc.orientation)), func(t *testing.T) {
			out := applyEXIFOrientation(src, tc.orientation)
			bounds := out.Bounds()
			if bounds.Dx() != tc.width || bounds.Dy() != tc.height {
				t.Fatalf("dims %dx%d, want %dx%d", bounds.Dx(), bounds.Dy(), tc.width, tc.height)
			}
			if got := at(out, 0, 0); got != tc.first {
				t.Fatalf("(0,0) = %v, want %v", got, tc.first)
			}
			if got := at(out, tc.secondX, tc.secondY); got != tc.second {
				t.Fatalf("(%d,%d) = %v, want %v", tc.secondX, tc.secondY, got, tc.second)
			}
		})
	}
	// Orientation 1 (and unknown) returns the image unchanged.
	if applyEXIFOrientation(src, 1) != image.Image(src) {
		t.Fatal("orientation 1 should return the input unchanged")
	}
}

func TestValidateInpaintMask(t *testing.T) {
	valid := editTestPNG(t, func() image.Image {
		img := image.NewRGBA(image.Rect(0, 0, 4, 3))
		img.Set(1, 1, color.White)
		return img
	}())
	full := editTestPNG(t, editSolidImage(4, 3, color.White))
	empty := editTestPNG(t, editSolidImage(4, 3, color.Black))
	wrongDims := editTestPNG(t, editSolidImage(5, 3, color.White))
	if _, err := validateInpaintMask(valid, 4, 3); err != nil {
		t.Fatalf("valid partial mask rejected: %v", err)
	}
	if _, err := validateInpaintMask(full, 4, 3); err != nil {
		t.Fatalf("full-image selection must be valid: %v", err)
	}
	if _, err := validateInpaintMask(empty, 4, 3); err == nil {
		t.Fatal("empty mask must be rejected")
	}
	if _, err := validateInpaintMask(wrongDims, 4, 3); err == nil {
		t.Fatal("dimension-mismatched mask must be rejected")
	}
	if _, err := validateInpaintMask([]byte("not a png"), 4, 3); err == nil {
		t.Fatal("non-PNG mask must be rejected")
	}
}

func TestCompositeInpaintResult(t *testing.T) {
	// Source: distinct color per pixel so "bit-exact outside the mask" is
	// provable for every preserved pixel, including the border.
	source := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x * 60), G: uint8(y * 60), B: 9, A: 255})
		}
	}
	result := editSolidImage(4, 4, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	// Mask: fully selected top-left 2x2, a half-strength pixel at (2,0), and
	// everything else preserved.
	mask := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			mask.Set(x, y, color.White)
		}
	}
	mask.Set(2, 0, color.Gray{Y: 128})
	sourceData := editTestPNG(t, source)
	resultData := editTestPNG(t, result)
	maskData := editTestPNG(t, mask)

	out, err := compositeInpaintResult(sourceData, resultData, maskData)
	if err != nil {
		t.Fatalf("compositeInpaintResult: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("composite output is not PNG: %v", err)
	}
	if decoded.Bounds().Dx() != 4 || decoded.Bounds().Dy() != 4 {
		t.Fatalf("composite output dims %v", decoded.Bounds())
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := decoded.At(x, y)
			switch {
			case x < 2 && y < 2:
				if got != result.At(x, y) {
					t.Fatalf("selected pixel (%d,%d) = %v, want result %v", x, y, got, result.At(x, y))
				}
			case x == 2 && y == 0:
				r, _, _, _ := got.RGBA()
				if r8 := r >> 8; r8 <= uint32(x*60) || r8 >= 255 {
					t.Fatalf("edge pixel (%d,%d) is not blended between source (%d) and result (255): %d", x, y, x*60, r8)
				}
			default:
				if got != source.At(x, y) {
					t.Fatalf("preserved pixel (%d,%d) = %v, want source %v", x, y, got, source.At(x, y))
				}
			}
		}
	}
}

func TestCompositeInpaintResultRejectsMismatchedOutput(t *testing.T) {
	source := editTestPNG(t, editSolidImage(4, 4, color.Black))
	result := editTestPNG(t, editSolidImage(2, 2, color.White))
	mask := editTestPNG(t, editSolidImage(4, 4, color.White))
	if _, err := compositeInpaintResult(source, result, mask); err == nil {
		t.Fatal("a size-changing result must be rejected, not silently resized")
	}
}

func TestNormalizeEditSourceBytesPassthrough(t *testing.T) {
	pngData := editTestPNG(t, editSolidImage(3, 2, color.RGBA{G: 255, A: 255}))
	got, ext, err := normalizeEditSourceBytes(t.Context(), defaultAppConfig(), pngData, ".png")
	if err != nil {
		t.Fatalf("png passthrough: %v", err)
	}
	if ext != ".png" || !bytes.Equal(got, pngData) {
		t.Fatalf("composable unrotated source must pass through byte-identical (ext %q, %d bytes vs %d)", ext, len(got), len(pngData))
	}
	jpegData := &bytes.Buffer{}
	if err := jpeg.Encode(jpegData, editSolidImage(3, 2, color.RGBA{B: 255, A: 255}), nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	got, ext, err = normalizeEditSourceBytes(t.Context(), defaultAppConfig(), jpegData.Bytes(), ".jpg")
	if err != nil {
		t.Fatalf("jpeg passthrough: %v", err)
	}
	if ext != ".jpg" || !bytes.Equal(got, jpegData.Bytes()) {
		t.Fatalf("unrotated jpeg must pass through byte-identical (ext %q)", ext)
	}
}

func TestAlignInpaintOutputScale(t *testing.T) {
	// A 1536x1024 source with a 1440x960 (uniformly scaled, same 3:2 shape)
	// output is rescaled back to the source's dims with a notice — the live
	// flux-pro fill behavior on large sources.
	source := editTestPNG(t, editSolidImage(48, 32, color.RGBA{R: 200, G: 100, A: 255}))
	output := editTestPNG(t, editSolidImage(45, 30, color.RGBA{B: 90, A: 255}))
	aligned, notice, scaled := alignInpaintOutputScale(source, output)
	if !scaled {
		t.Fatal("uniformly scaled output should be aligned")
	}
	if notice == "" {
		t.Fatal("alignment must carry an explanatory notice")
	}
	w, h, ok := editImageDimensions(aligned)
	if !ok || w != 48 || h != 32 {
		t.Fatalf("aligned dims = %dx%d (ok=%v), want 48x32", w, h, ok)
	}
	// The aligned result composites cleanly against the full-res mask.
	mask := editTestPNG(t, editSolidImage(48, 32, color.White))
	if _, err := compositeInpaintResult(source, aligned, mask); err != nil {
		t.Fatalf("composite after alignment: %v", err)
	}

	// A differently-shaped output is passed through untouched — the composite
	// refuses it, as before.
	distorted := editTestPNG(t, editSolidImage(30, 30, color.Black))
	passthrough, notice, scaled := alignInpaintOutputScale(source, distorted)
	if scaled || notice != "" {
		t.Fatal("aspect-mismatched output must not be rescaled")
	}
	if _, err := compositeInpaintResult(source, passthrough, mask); err == nil {
		t.Fatal("composite must still refuse a differently-shaped output")
	}

	// Matching dims are a no-op.
	same := editTestPNG(t, editSolidImage(48, 32, color.RGBA{B: 90, A: 255}))
	if _, notice, scaled := alignInpaintOutputScale(source, same); scaled || notice != "" {
		t.Fatal("matching dims need no alignment")
	}
}
