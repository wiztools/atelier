package main

// The image-edit operation model: the kind-discriminated record every
// submitted edit persists (the plan's "operation record"), plus the pure-Go
// media core the inpaint operation needs — source normalization (orientation
// baked, decodable bytes) and mask validation. Session
// persistence and the bound methods live in edit_session.go; the provider
// adapters live beside their siblings (fal_params.go, replicate_params.go).
//
// The envelope is deliberately media- and kind-agnostic: Kind names the
// operation, exactly one per-kind payload struct carries its params, and
// unknown kinds (from future versions) degrade to opaque entries. Deterministic
// kinds (crop next) add their own payload struct and executor — the envelope,
// the session lineage, and adoption never change.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // register the GIF decoder for decodeComposableImage
	"image/jpeg"
	"image/png"
	"math"
	"strings"
)

// Edit operation kinds. Inpaint is provider-backed; crop is deterministic and
// runs locally against the session-owned source pixels.
const (
	editOperationKindInpaint = "inpaint"
	editOperationKindCrop    = "crop"
	editOperationKindReframe = "video-reframe"
)

// Edit operation statuses. queued/running are transient (the in-flight edit);
// completed/failed/cancelled are terminal. A queued/running op found without
// a live run (app restart) is downgraded to failed with an interruption
// notice — see sweepInterruptedEditOps.
const (
	editOperationStatusQueued    = "queued"
	editOperationStatusRunning   = "running"
	editOperationStatusCompleted = "completed"
	editOperationStatusFailed    = "failed"
	editOperationStatusCancelled = "cancelled"
)

// Inpaint mask polarity, normalized at the adapter boundary from the verified
// provider contract: Atelier's canonical mask paints the EDITABLE region white
// (white = may change, black = preserved), and an adapter for a model that
// speaks the opposite polarity inverts before sending — never the reverse.
const (
	inpaintMaskWhiteEdits     = "white-edits"
	inpaintMaskWhitePreserves = "white-preserves"
)

// EditOperation is the persisted record of one submitted edit iteration —
// the envelope every kind shares. It rides the operation turn's Request map
// under the "operation" key (edit_session.go) and is delivered to the frontend
// verbatim through the bound methods and the atelier:edit-op event. Executor
// attribution is flat: AI ops carry Provider+Model (fal/replicate), local ops
// carry Backend (the sips/imagemagick/ffmpeg label convention). Results are
// ordinary image content on the same turn; masks are operation-payload
// artifacts (msk_<hex>.png) referenced from the params only — never
// HistoryContent, so asset enumeration and model context never see them.
type EditOperation struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// CreatedAt/CompletedAt bookend the operation; CompletedAt is empty while
	// queued/running.
	CreatedAt   string `json:"createdAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	// InputArtifactID is the canvas state this operation consumed — the
	// session's source copy for the first op, an earlier result after
	// "Use as source". ResultArtifactID/ResultPath locate the output (empty
	// while queued/running/failed). ResultURL is response-only, hydrated by
	// the readers (ListEditSession, the persist site) — never persisted.
	InputArtifactID       string  `json:"inputArtifactId,omitempty"`
	ResultArtifactID      string  `json:"resultArtifactId,omitempty"`
	ResultPath            string  `json:"resultPath,omitempty"`
	ResultMimeType        string  `json:"resultMimeType,omitempty"`
	ResultWidth           int     `json:"resultWidth,omitempty"`
	ResultHeight          int     `json:"resultHeight,omitempty"`
	ResultDurationSeconds float64 `json:"resultDurationSeconds,omitempty"`
	ResultURL             string  `json:"resultUrl,omitempty"`
	// Executor attribution. Provider/Model record the effective pair for AI
	// ops (whether inherited or pinned); Backend names the local CLI label for
	// deterministic ops (the basicImageActivity convention). CostMicros is the
	// fal estimate; CostUnknown marks Replicate media (renders "?"), and both
	// stay zero for local ops.
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Backend     string `json:"backend,omitempty"`
	CostMicros  int64  `json:"costMicros,omitempty"`
	CostUnknown bool   `json:"costUnknown,omitempty"`
	// Error/Notices carry failure text and deterministic caveats verbatim.
	Error    string   `json:"error,omitempty"`
	Notices  []string `json:"notices,omitempty"`
	Progress float64  `json:"progress,omitempty"`
	// Kind payloads — exactly one is non-nil. A nil payload with an unknown
	// Kind is rendered as an opaque entry (forward compatibility).
	Inpaint *InpaintOperationParams `json:"inpaint,omitempty"`
	Crop    *CropOperationParams    `json:"crop,omitempty"`
	Reframe *VideoReframeParams     `json:"reframe,omitempty"`
	// AdoptedAt records when this operation's result was added to the parent
	// conversation (AddEditResultToConversation) — the idempotency mark that
	// keeps repeated clicks from duplicating parent entries.
	AdoptedAt string `json:"adoptedAt,omitempty"`
}

// InpaintOperationParams is the inpaint kind's payload: the prompt, the
// selection mask (an msk_<hex>.png artifact in the session's artifacts dir,
// white = editable), and the mask's pixel dimensions — which must match the
// normalized source the op ran against.
type InpaintOperationParams struct {
	Prompt         string `json:"prompt,omitempty"`
	MaskArtifactID string `json:"maskArtifactId,omitempty"`
	MaskPath       string `json:"maskPath,omitempty"`
	MaskWidth      int    `json:"maskWidth,omitempty"`
	MaskHeight     int    `json:"maskHeight,omitempty"`
}

// CropOperationParams is the deterministic crop payload. Coordinates are
// integer upright image pixels in the input artifact's coordinate space.
// SourceWidth/SourceHeight are a stale-preview guard: the submitter must name
// the dimensions it used to derive the rectangle.
type CropOperationParams struct {
	X            int    `json:"x"`
	Y            int    `json:"y"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	SourceWidth  int    `json:"sourceWidth"`
	SourceHeight int    `json:"sourceHeight"`
	AspectRatio  string `json:"aspectRatio,omitempty"`
}

// operationPayload returns the op's kind payload for validation and adapter
// dispatch.
func (op EditOperation) operationPayload() (*InpaintOperationParams, bool) {
	if op.Kind == editOperationKindInpaint && op.Inpaint != nil {
		return op.Inpaint, true
	}
	return nil, false
}

// editComposableExtensions lists the image containers the pure-Go decoder
// decodes (stdlib image/png, image/jpeg, image/gif — go.mod carries no image
// libraries). Sources outside this set — WebP and every CLI-decoded container
// (HEIC/AVIF/TIFF/BMP/JP2) — are baked to JPEG by normalizeEditSourceBytes so
// mask alignment and diagnostics always see the same pixels.
var editComposableExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
}

// normalizeEditSourceBytes prepares one source image for the editor pipeline:
// orientation baked into the pixels and the bytes decodable by Go.
// Upright PNG/JPEG sources pass through byte-identical. GIFs and oriented
// JPEGs become PNGs in Go; other containers (WebP/HEIC/AVIF/TIFF/BMP/JP2)
// use the resolved basic image backend to become JPEG. Because
// ImageMagick does not reliably bake EXIF orientation on conversion, the bake
// re-checks the result's own orientation flag and, if it survived, applies the
// rotation in pure Go (bakeJPEGOrientation) — the final bytes always read
// upright, so the mask the user painted on the upright preview aligns with the
// pixels every later stage (provider submit, diagnostics) sees.
func normalizeEditSourceBytes(ctx context.Context, config AppConfig, data []byte, extension string) ([]byte, string, error) {
	extension = strings.ToLower(strings.TrimSpace(extension))
	if extension == "" {
		extension = imageExtensionForBytes(data)
	}
	if extension == "" {
		return nil, "", errors.New("unsupported image payload")
	}
	orientation := jpegEXIFOrientation(data)
	if extension == ".gif" || orientation > 1 {
		// Freeze GIFs on their first frame and bake JPEG orientation before
		// previewing. PNG keeps the decoded pixels without another lossy encode.
		decoded, err := decodeComposableImage(data)
		if err != nil {
			return nil, "", fmt.Errorf("could not decode the source image: %w", err)
		}
		out := &bytes.Buffer{}
		if err := png.Encode(out, applyEXIFOrientation(decoded, orientation)); err != nil {
			return nil, "", err
		}
		return out.Bytes(), ".png", nil
	}
	if editComposableExtensions[extension] && orientation <= 1 {
		return data, extension, nil
	}
	converted, err := convertImageBytesForModel(ctx, config, data, extension)
	if err != nil || len(converted) == 0 {
		return nil, "", fmt.Errorf("could not normalize the source image on the local image backend: %w", err)
	}
	// Trust the output's bytes, not the command's success (the
	// convertImageBytesForModel rule): a backend that wrote something
	// undecodable must not ship as a "normalized" source.
	if sniffed := imageExtensionForBytes(converted); sniffed != ".jpg" && sniffed != ".jpeg" {
		return nil, "", errors.New("the local image backend returned an unexpected format while normalizing the source")
	}
	if orientation := jpegEXIFOrientation(converted); orientation > 1 {
		baked, err := bakeJPEGOrientation(converted, orientation)
		if err != nil {
			return nil, "", fmt.Errorf("could not bake the source image's EXIF orientation: %w", err)
		}
		converted = baked
	}
	return converted, ".jpg", nil
}

// jpegEXIFOrientation extracts the EXIF orientation tag (0x0112) from a JPEG's
// APP1 Exif segment: 1 when absent or upright, 2–8 for the flip/rotate values,
// 0 when the payload is not a JPEG (callers treat 0 like 1). Only the segment
// walk and IFD0 are parsed — no dependencies, no full EXIF decode.
func jpegEXIFOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0
	}
	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xFF {
			return 1
		}
		marker := data[pos+1]
		// Standalone markers carry no length segment.
		if marker == 0xFF || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			pos += 2
			continue
		}
		// Start of scan: the EXIF segment always precedes the entropy-coded data.
		if marker == 0xDA {
			break
		}
		segmentLength := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		if segmentLength < 2 {
			return 1
		}
		if marker == 0xE1 && pos+10 <= len(data) && string(data[pos+4:pos+10]) == "Exif\x00\x00" {
			segmentEnd := pos + 2 + segmentLength
			if segmentEnd > len(data) {
				segmentEnd = len(data)
			}
			return tiffOrientation(data[pos+10 : segmentEnd])
		}
		pos += 2 + segmentLength
	}
	return 1
}

// tiffOrientation reads the orientation entry (tag 0x0112) from a TIFF header
// (the body of the Exif APP1 segment, after its 6-byte signature). Anything
// unreadable or out of range reports 1 — the fail-safe is "no rotation".
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder = binary.BigEndian
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
	default:
		return 1
	}
	ifdOffset := int(order.Uint32(tiff[4:8]))
	if ifdOffset < 2 || ifdOffset+2 > len(tiff) {
		return 1
	}
	entryCount := int(order.Uint16(tiff[ifdOffset : ifdOffset+2]))
	for i := 0; i < entryCount; i++ {
		entryStart := ifdOffset + 2 + i*12
		if entryStart+12 > len(tiff) {
			break
		}
		entry := tiff[entryStart : entryStart+12]
		if order.Uint16(entry[0:2]) != 0x0112 {
			continue
		}
		value := int(order.Uint16(entry[8:10]))
		if value >= 1 && value <= 8 {
			return value
		}
		return 1
	}
	return 1
}

// bakeJPEGOrientation applies a JPEG's EXIF orientation (2–8) to its pixels and
// re-encodes without the flag, so the stored bytes read upright everywhere —
// the backend-independent guarantee the bake step in normalizeEditSourceBytes
// needs. A pixel-domain transpose of an already-decoded image is lossless for
// the transform itself; the re-encode is the JPEG generation the bake costs
// (sources with no rotation never pay it).
func bakeJPEGOrientation(data []byte, orientation int) ([]byte, error) {
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	baked := applyEXIFOrientation(decoded, orientation)
	out := &bytes.Buffer{}
	if err := jpeg.Encode(out, baked, &jpeg.Options{Quality: 95}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// applyEXIFOrientation maps EXIF orientation values 2–8 onto their pixel
// transforms (flip horizontal/vertical, transpose, rotate 90/180/270). Value 1
// and unknown values return the image unchanged.
func applyEXIFOrientation(img image.Image, orientation int) image.Image {
	switch orientation {
	case 2:
		return imageFlipHorizontal(img)
	case 3:
		return imageRotate180(img)
	case 4:
		return imageFlipVertical(img)
	case 5:
		return imageTranspose(img)
	case 6:
		return imageRotate90(img)
	case 7:
		return imageTransverse(img)
	case 8:
		return imageRotate270(img)
	}
	return img
}

// imageFlipHorizontal mirrors the image left↔right.
func imageFlipHorizontal(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(bounds.Max.X-1-x, y, img.At(x, y))
		}
	}
	return out
}

// imageFlipVertical mirrors the image top↔bottom.
func imageFlipVertical(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(x, bounds.Max.Y-1-y, img.At(x, y))
		}
	}
	return out
}

// imageRotate180 rotates the image a half turn.
func imageRotate180(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(bounds.Max.X-1-x, bounds.Max.Y-1-y, img.At(x, y))
		}
	}
	return out
}

// imageRotate90 rotates the image 90° clockwise (the EXIF 6 case).
func imageRotate90(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(bounds.Max.Y-1-y, x, img.At(x, y))
		}
	}
	return out
}

// imageRotate270 rotates the image 90° counter-clockwise (the EXIF 8 case):
// source (x, y) lands at (y, W-1-x).
func imageRotate270(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(y, bounds.Max.X-1-x, img.At(x, y))
		}
	}
	return out
}

// imageTranspose flips along the main diagonal (EXIF 5).
func imageTranspose(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(y, x, img.At(x, y))
		}
	}
	return out
}

// imageTransverse flips along the anti-diagonal (EXIF 7).
func imageTransverse(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(bounds.Max.Y-1-y, bounds.Max.X-1-x, img.At(x, y))
		}
	}
	return out
}

// decodeComposableImage decodes PNG/JPEG/GIF bytes — exactly the containers
// normalizeEditSourceBytes emits. WebP and the CLI-decoded containers are
// rejected here (they should never reach this decoder; the bake step owns
// them).
func decodeComposableImage(data []byte) (image.Image, error) {
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

// editImageDimensions reports an image payload's pixel dimensions through the
// magic-byte sniffer (fal_pricing.go) — no full decode, works for every
// container imageExtensionForBytes recognizes.
func editImageDimensions(data []byte) (width, height int, ok bool) {
	w, h, sniffed := imagePixelDimensions(data)
	return int(w), int(h), sniffed
}

// validateInpaintMask decodes the submitted selection PNG and checks it against
// the normalized source: PNG only (the editor's canvas export), exactly the
// source's pixel dimensions (the one coordinate system — display, encoding,
// and validation share it), and a nonempty selection (at least one pixel with
// any selection strength; a full-image selection is valid). It returns the
// decoded mask image for diagnostics. Zero-remote-call rule: every check
// here runs before any provider submit.
func validateInpaintMask(maskData []byte, sourceWidth, sourceHeight int) (image.Image, error) {
	maskImg, err := png.Decode(bytes.NewReader(maskData))
	if err != nil {
		return nil, fmt.Errorf("the selection mask must be a PNG image (%s)", maskErrReason(err))
	}
	bounds := maskImg.Bounds()
	if bounds.Dx() != sourceWidth || bounds.Dy() != sourceHeight {
		return nil, fmt.Errorf("the selection mask is %dx%d but the source image is %dx%d — open the editor again so the mask is painted against the current image",
			bounds.Dx(), bounds.Dy(), sourceWidth, sourceHeight)
	}
	if !maskHasSelection(maskImg) {
		return nil, errors.New("the selection is empty — paint at least a few pixels with the brush before generating")
	}
	return maskImg, nil
}

// maskHasSelection reports whether any pixel of the mask carries selection
// strength: the luminance of its color multiplied by its own alpha. The
// translucent overlay and the anti-aliased brush edge both produce partial
// values; only a fully black-and-opaque mask counts as empty.
func maskHasSelection(mask image.Image) bool {
	bounds := mask.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if maskPixelStrength(mask.At(x, y)) > 0 {
				return true
			}
		}
	}
	return false
}

// maskPixelStrength reduces one mask pixel to its 0–255 selection strength.
// color.Color's RGBA() reports alpha-PREMULTIPLIED 16-bit channels, and the
// premultiplied luminance already folds the pixel's alpha in once — white at
// half opacity reads as half strength — so no second alpha multiply happens
// here. An opaque white pixel is full strength; black or transparent is none.
func maskPixelStrength(c color.Color) uint32 {
	r, g, b, _ := c.RGBA()
	return (299*uint32(r>>8) + 587*uint32(g>>8) + 114*uint32(b>>8)) / 1000
}

// maskErrReason shortens stdlib PNG decode errors for the user-facing
// validation message.
func maskErrReason(err error) string {
	if err == nil {
		return "undecodable"
	}
	msg := err.Error()
	if idx := strings.Index(msg, ": "); idx >= 0 {
		msg = msg[idx+2:]
	}
	return msg
}

// resizeImageBilinear rescales an image to width×height with bilinear
// interpolation in premultiplied space (go.mod carries no image scaling
// dependency, and the kernel is small enough to own). Sample points sit at
// pixel centers so uniform upscale/downscale stays symmetric.
func resizeImageBilinear(src image.Image, width, height int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	if sw == 0 || sh == 0 {
		return out
	}
	xRatio := float64(sw) / float64(width)
	yRatio := float64(sh) / float64(height)
	sample := func(x, y int) (float64, float64, float64, float64) {
		if x < 0 {
			x = 0
		}
		if x > sw-1 {
			x = sw - 1
		}
		if y < 0 {
			y = 0
		}
		if y > sh-1 {
			y = sh - 1
		}
		r, g, b, a := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
		return float64(r >> 8), float64(g >> 8), float64(b >> 8), float64(a >> 8)
	}
	for y := 0; y < height; y++ {
		sy := (float64(y)+0.5)*yRatio - 0.5
		y0 := int(math.Floor(sy))
		fy := sy - float64(y0)
		for x := 0; x < width; x++ {
			sx := (float64(x)+0.5)*xRatio - 0.5
			x0 := int(math.Floor(sx))
			fx := sx - float64(x0)
			var blended [4]float64
			for row, yy := range [2]int{y0, y0 + 1} {
				r0, g0, b0, a0 := sample(x0, yy)
				r1, g1, b1, a1 := sample(x0+1, yy)
				wx := [2]float64{1 - fx, fx}
				wy := 1 - fy
				if row == 1 {
					wy = fy
				}
				blended[0] += wy * (wx[0]*r0 + wx[1]*r1)
				blended[1] += wy * (wx[0]*g0 + wx[1]*g1)
				blended[2] += wy * (wx[0]*b0 + wx[1]*b1)
				blended[3] += wy * (wx[0]*a0 + wx[1]*a1)
			}
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(math.Round(math.Min(255, blended[0]))),
				G: uint8(math.Round(math.Min(255, blended[1]))),
				B: uint8(math.Round(math.Min(255, blended[2]))),
				A: uint8(math.Round(math.Min(255, blended[3]))),
			})
		}
	}
	return out
}
