package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"strconv"
	"strings"
)

// validateCropParams checks that a crop is both inside the decoded source and
// derived from the same preview dimensions the editor showed. The rectangle is
// already integer-canonicalized by the frontend; this function is the durable
// guard before the operation is persisted.
func validateCropParams(params *CropOperationParams, sourceWidth, sourceHeight int) error {
	if params == nil {
		return errors.New("crop parameters are required")
	}
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return errors.New("could not read the image's dimensions")
	}
	if params.SourceWidth != sourceWidth || params.SourceHeight != sourceHeight {
		return fmt.Errorf("the crop was prepared for a %dx%d source, but the current image is %dx%d — reopen the editor and try again",
			params.SourceWidth, params.SourceHeight, sourceWidth, sourceHeight)
	}
	if params.X < 0 || params.Y < 0 {
		return errors.New("the crop origin must stay inside the source image")
	}
	if params.Width <= 0 || params.Height <= 0 {
		return errors.New("the crop must be at least 1 pixel wide and 1 pixel tall")
	}
	if params.X > sourceWidth-params.Width || params.Y > sourceHeight-params.Height {
		return fmt.Errorf("the crop rectangle %d,%d %dx%d exceeds the %dx%d source image",
			params.X, params.Y, params.Width, params.Height, sourceWidth, sourceHeight)
	}
	ratio := strings.ToLower(strings.TrimSpace(params.AspectRatio))
	if ratio != "" && ratio != "free" && ratio != "original" {
		rw, rh, ok := parseCropAspectRatio(ratio)
		if !ok {
			return fmt.Errorf("unsupported crop aspect ratio %q", params.AspectRatio)
		}
		target := float64(rw) / float64(rh)
		// Raster dimensions can only approximate many ratios when constrained
		// by integer pixels. Accept the documented one-pixel bound.
		widthIfHeightLocked := target * float64(params.Height)
		heightIfWidthLocked := float64(params.Width) / target
		if math.Abs(float64(params.Width)-widthIfHeightLocked) > 1.01 &&
			math.Abs(float64(params.Height)-heightIfWidthLocked) > 1.01 {
			return fmt.Errorf("the crop size %dx%d does not match the locked %s ratio", params.Width, params.Height, ratio)
		}
	}
	if ratio == "original" {
		target := float64(sourceWidth) / float64(sourceHeight)
		widthIfHeightLocked := target * float64(params.Height)
		heightIfWidthLocked := float64(params.Width) / target
		if math.Abs(float64(params.Width)-widthIfHeightLocked) > 1.01 &&
			math.Abs(float64(params.Height)-heightIfWidthLocked) > 1.01 {
			return fmt.Errorf("the crop size %dx%d does not match the original image ratio", params.Width, params.Height)
		}
	}
	if params.X == 0 && params.Y == 0 && params.Width == sourceWidth && params.Height == sourceHeight {
		return errors.New("the crop is unchanged — choose a smaller region before applying")
	}
	params.AspectRatio = ratio
	return nil
}

func parseCropAspectRatio(ratio string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(ratio), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, false
	}
	return w, h, w > 0 && h > 0
}

// executeCropOperation copies the requested rectangle from the decoded source
// into a fresh PNG. No resampling or provider call occurs.
func executeCropOperation(sourceData []byte, params CropOperationParams) ([]byte, error) {
	source, err := decodeComposableImage(sourceData)
	if err != nil {
		return nil, fmt.Errorf("could not decode the source image for cropping: %w", err)
	}
	bounds := source.Bounds()
	if err := validateCropParams(&params, bounds.Dx(), bounds.Dy()); err != nil {
		return nil, err
	}
	// The standard PNG/JPEG/GIF decoders return images with SubImage. Keep
	// their native pixel storage so alpha and 16-bit samples are not converted.
	sub, ok := source.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, errors.New("the source image does not support rectangular cropping")
	}
	rect := image.Rect(params.X, params.Y, params.X+params.Width, params.Y+params.Height).Add(bounds.Min)
	out := sub.SubImage(rect)
	encoded := &bytes.Buffer{}
	if err := png.Encode(encoded, out); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
