package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
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

func TestExecuteCropOperationCopiesPixelsAndAlpha(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 40), G: uint8(y * 70), B: 11, A: uint8(180 + x + y)})
		}
	}
	out, err := executeCropOperation(editTestPNG(t, source), CropOperationParams{
		X: 1, Y: 1, Width: 2, Height: 2,
		SourceWidth: 4, SourceHeight: 3,
		AspectRatio: "1:1",
	})
	if err != nil {
		t.Fatalf("executeCropOperation: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("crop output is not PNG: %v", err)
	}
	if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("crop output bounds = %v", decoded.Bounds())
	}
	decodedSource, err := png.Decode(bytes.NewReader(editTestPNG(t, source)))
	if err != nil {
		t.Fatalf("decode source fixture: %v", err)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			got := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
			want := color.NRGBAModel.Convert(decodedSource.At(x+1, y+1)).(color.NRGBA)
			if got != want {
				t.Fatalf("pixel (%d,%d) = %v, want source (%d,%d) %v", x, y, got, x+1, y+1, want)
			}
		}
	}
}

func TestExecuteCropOperationPreservesDecoded16BitPNG(t *testing.T) {
	source := image.NewNRGBA64(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			source.SetNRGBA64(x, y, color.NRGBA64{
				R: uint16(1000 + x*1111),
				G: uint16(2000 + y*2222),
				B: uint16(3000 + x*333 + y*444),
				A: uint16(50000 + x*100 + y*10),
			})
		}
	}
	sourceData := editTestPNG(t, source)
	decodedSource, err := png.Decode(bytes.NewReader(sourceData))
	if err != nil {
		t.Fatalf("decode source fixture: %v", err)
	}
	out, err := executeCropOperation(sourceData, CropOperationParams{
		X: 1, Y: 0, Width: 2, Height: 3,
		SourceWidth: 4, SourceHeight: 3,
		AspectRatio: "2:3",
	})
	if err != nil {
		t.Fatalf("executeCropOperation: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("crop output is not PNG: %v", err)
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			gotR, gotG, gotB, gotA := decoded.At(x, y).RGBA()
			wantR, wantG, wantB, wantA := decodedSource.At(x+1, y).RGBA()
			if gotR != wantR || gotG != wantG || gotB != wantB || gotA != wantA {
				t.Fatalf("pixel (%d,%d) RGBA = %v, want decoded source (%d,%d) %v",
					x, y, [4]uint32{gotR, gotG, gotB, gotA}, x+1, y, [4]uint32{wantR, wantG, wantB, wantA})
			}
		}
	}
}

func TestValidateCropParamsRejectsStaleAndOutOfBounds(t *testing.T) {
	cases := []struct {
		name   string
		params CropOperationParams
	}{
		{"stale dimensions", CropOperationParams{X: 0, Y: 0, Width: 2, Height: 2, SourceWidth: 5, SourceHeight: 4}},
		{"negative origin", CropOperationParams{X: -1, Y: 0, Width: 2, Height: 2, SourceWidth: 4, SourceHeight: 3}},
		{"zero size", CropOperationParams{X: 0, Y: 0, Width: 0, Height: 2, SourceWidth: 4, SourceHeight: 3}},
		{"out of bounds", CropOperationParams{X: 3, Y: 0, Width: 2, Height: 2, SourceWidth: 4, SourceHeight: 3}},
		{"unchanged full image", CropOperationParams{X: 0, Y: 0, Width: 4, Height: 3, SourceWidth: 4, SourceHeight: 3}},
		{"ratio mismatch", CropOperationParams{X: 0, Y: 0, Width: 4, Height: 2, SourceWidth: 4, SourceHeight: 3, AspectRatio: "1:1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.params
			if err := validateCropParams(&params, 4, 3); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	params := CropOperationParams{X: 0, Y: 0, Width: 3, Height: 2, SourceWidth: 4, SourceHeight: 3, AspectRatio: "3:2"}
	if err := validateCropParams(&params, 4, 3); err != nil {
		t.Fatalf("valid ratio crop rejected: %v", err)
	}
	params = CropOperationParams{X: 0, Y: 0, Width: 2, Height: 3, SourceWidth: 4, SourceHeight: 3, AspectRatio: "2:3"}
	if err := validateCropParams(&params, 4, 3); err != nil {
		t.Fatalf("swapped ratio crop rejected: %v", err)
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

func TestNormalizeEditSourceBakesEXIFWithoutLocalTools(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			source.SetRGBA(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 60), A: 255})
		}
	}
	encoded := &bytes.Buffer{}
	if err := jpeg.Encode(encoded, source, nil); err != nil {
		t.Fatal(err)
	}
	for orientation := uint16(2); orientation <= 8; orientation++ {
		exif := exifJPEGBytes(orientation)
		raw := append(append([]byte(nil), exif[:len(exif)-2]...), encoded.Bytes()[2:]...)
		normalized, ext, err := normalizeEditSourceBytes(t.Context(), defaultAppConfig(), raw, ".jpg")
		if err != nil {
			t.Fatalf("orientation %d: %v", orientation, err)
		}
		if ext != ".png" {
			t.Fatalf("orientation %d normalized to %s", orientation, ext)
		}
		actual, err := png.Decode(bytes.NewReader(normalized))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		expected := applyEXIFOrientation(decoded, int(orientation))
		if actual.Bounds() != expected.Bounds() {
			t.Fatalf("orientation %d dimensions mismatch", orientation)
		}
		for y := 0; y < actual.Bounds().Dy(); y++ {
			for x := 0; x < actual.Bounds().Dx(); x++ {
				if color.RGBAModel.Convert(actual.At(x, y)) != color.RGBAModel.Convert(expected.At(x, y)) {
					t.Fatalf("orientation %d pixel %d,%d changed", orientation, x, y)
				}
			}
		}
	}
}

func TestNormalizeEditSourceFreezesGIFFirstFrame(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 4, 2), palette)
	second := image.NewPaletted(first.Bounds(), palette)
	for i := range second.Pix {
		second.Pix[i] = 1
	}
	encoded := &bytes.Buffer{}
	if err := gif.EncodeAll(encoded, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	normalized, ext, err := normalizeEditSourceBytes(t.Context(), defaultAppConfig(), encoded.Bytes(), ".gif")
	if err != nil {
		t.Fatal(err)
	}
	if ext != ".png" {
		t.Fatalf("GIF preview format = %s, want a static PNG", ext)
	}
	decoded, err := png.Decode(bytes.NewReader(normalized))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds() != first.Bounds() {
		t.Fatal("GIF preview changed image dimensions")
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			if color.RGBAModel.Convert(decoded.At(x, y)) != color.RGBAModel.Convert(first.At(x, y)) {
				t.Fatal("GIF preview did not use the first frame")
			}
		}
	}
}
