package main

// Local image tools: six deterministic raster transforms on the turn's
// attached images, split across two local CLI backends chosen by capability
// (macOS sips runs what it can; ImageMagick covers what sips cannot):
//
//	sips (ships with macOS)        ImageMagick (brew install imagemagick)
//	  convert_image  — format+quality    compose_images — watermark / collage
//	  transform_image — crop/resize/     adjust_image  — brightness/contrast/
//	    rotate/flip                        saturation, grayscale, sepia
//	  probe_image    — image facts       optimize_image — strip metadata +
//	                                     recompress
//
// They follow the media-tool conventions of local_ffmpeg.go — attachment-
// driven sources (the turn's image slots carry attached, @-mentioned, or
// history-fallback images), outputs staged as temp files wrapped in
// ToolImageResult so the artifact, carry-forward, and telemetry pipelines
// serve them unchanged. Empirical CLI facts this file depends on (verified
// against macOS 25 sips and Homebrew ImageMagick 7): sips cannot WRITE webp
// (reads fine — such inputs are transcoded to PNG with a notice), has no
// aspect-ratio crop (the crop rect is computed from probed dimensions), and
// CHAINING ops in one invocation misbehaves (crop 200x200 + resampleWidth
// 100 on a 400x300 image yields 50x50, not 100x100) — so every sips op runs
// as its own invocation. ImageMagick's `montage` needs a font delegate that
// default Homebrew installs lack, so collages are built with parenthesized
// +append/-append row groups instead.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Model names stamped on results and telemetry, mirroring ffmpegModelName:
// the CLIs are binaries, not models, but the media-result fields and the
// usage ledger key on Model — stable labels keep consumption visible as
// their own rows.
const (
	sipsModelName        = "sips"
	imageMagickModelName = "imagemagick"
)

// sipsToolsConfigured reports whether the sips-backed image tools (convert,
// transform, probe) should be offered.
func sipsToolsConfigured(config AppConfig) bool {
	_, ok := resolveLocalSipsBinary(config)
	return ok
}

// imageMagickToolsConfigured reports whether the ImageMagick-backed image
// tools (compose, adjust, optimize) should be offered.
func imageMagickToolsConfigured(config AppConfig) bool {
	_, ok := resolveLocalImageMagickBinary(config)
	return ok
}

// ---------------------------------------------------------------------------
// Formats
// ---------------------------------------------------------------------------

// sipsWritableFormats are the output formats this macOS's sips can write
// (verified via `sips --formats`): notably NOT webp — sips reads webp but
// refuses to write it, so a webp input to a sips tool is transcoded to PNG
// with a notice.
var sipsWritableFormats = map[string]bool{
	"jpeg": true, "png": true, "heic": true, "tiff": true,
	"gif": true, "bmp": true, "jp2": true, "avif": true,
}

// sipsQualityFormats are the formats sips' -s formatOptions quality setting
// applies to.
var sipsQualityFormats = map[string]bool{"jpeg": true, "heic": true, "jp2": true}

// imagemagickFormats are the output formats the optimize tool accepts from
// ImageMagick — a superset of sips' (webp included).
var imagemagickFormats = map[string]bool{
	"jpeg": true, "png": true, "webp": true, "heic": true, "tiff": true,
	"gif": true, "bmp": true, "jp2": true, "avif": true,
}

// imageFormatExtension maps an output format name onto the staged output
// file's extension.
func imageFormatExtension(format string) string {
	switch format {
	case "jpeg":
		return ".jpg"
	case "tiff":
		return ".tiff"
	default:
		return "." + format
	}
}

// imageFormatMime maps an output format name onto the data-URL media type of
// the ToolImageResult the tools return.
func imageFormatMime(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "heic":
		return "image/heic"
	case "tiff":
		return "image/tiff"
	default:
		return "image/" + format
	}
}

// sipsFormatForMediaType maps an attached image's media type onto the sips
// output format to use for it: the native format when sips can write it, PNG
// otherwise (webp and exotic inputs), with a notice explaining the transcode.
func sipsFormatForMediaType(mediaType string) (format string, notice string) {
	trimmed := strings.TrimSpace(strings.ToLower(mediaType))
	native := map[string]string{
		"image/jpeg": "jpeg", "image/png": "png", "image/heic": "heic",
		"image/heif": "heic", "image/tiff": "tiff", "image/gif": "gif",
		"image/bmp": "bmp", "image/jp2": "jp2", "image/jpx": "jp2",
		"image/jxl": "avif", "image/avif": "avif",
	}[trimmed]
	if native != "" && sipsWritableFormats[native] {
		return native, ""
	}
	return "png", fmt.Sprintf("sips cannot write %s images, so the output was saved as PNG.", strings.TrimPrefix(trimmed, "image/"))
}

// ---------------------------------------------------------------------------
// sips probe: image facts for evidence and crop math
// ---------------------------------------------------------------------------

// ToolImageProbeResult is probe_image's evidence payload: the compact facts
// of an attached image. Like ffprobe's ToolProbeResult it rides the standard
// role:"tool" path verbatim — it is already small.
type ToolImageProbeResult struct {
	Format        string `json:"format"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	HasAlpha      bool   `json:"hasAlpha,omitempty"`
	ColorSpace    string `json:"colorSpace,omitempty"`
	BitsPerSample int    `json:"bitsPerSample,omitempty"`
	SizeBytes     int64  `json:"sizeBytes,omitempty"`
}

// parseSipsProperties reads `sips -g all` output: a first line naming the
// file, then one indented "key: value" property per line. Unknown keys are
// kept — callers pick what they need.
func parseSipsProperties(output []byte) map[string]string {
	properties := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " /") {
			// Property keys are single camelCase words; the file-path header
			// line and any diagnostic chatter are skipped this way.
			continue
		}
		properties[key] = strings.TrimSpace(value)
	}
	return properties
}

// probeStagedImage runs sips -g all over a staged image file and parses it
// into the compact ToolImageProbeResult. Size comes from os.Stat, not sips.
func probeStagedImage(ctx context.Context, config AppConfig, path string) (ToolImageProbeResult, error) {
	output, err := runLocalSips(ctx, config, []string{"-g", "all", path})
	if err != nil {
		return ToolImageProbeResult{}, err
	}
	properties := parseSipsProperties(output)
	result := ToolImageProbeResult{
		Format:     properties["format"],
		ColorSpace: properties["space"],
	}
	result.Width, _ = strconv.Atoi(properties["pixelWidth"])
	result.Height, _ = strconv.Atoi(properties["pixelHeight"])
	result.BitsPerSample, _ = strconv.Atoi(properties["bitsPerSample"])
	result.HasAlpha = strings.EqualFold(properties["hasAlpha"], "yes")
	if info, statErr := os.Stat(path); statErr == nil {
		result.SizeBytes = info.Size()
	}
	if result.Width <= 0 || result.Height <= 0 {
		return ToolImageProbeResult{}, fmt.Errorf("sips reported no dimensions for the image: %s", truncateLocalToolOutput(output))
	}
	return result, nil
}

// parseAspectRatio reads a planner-supplied aspect like "16:9" or "1:1" as
// its two positive integer terms.
func parseAspectRatio(token string) (int, int, bool) {
	left, right, found := strings.Cut(strings.TrimSpace(token), ":")
	if !found {
		return 0, 0, false
	}
	width, err := strconv.Atoi(strings.TrimSpace(left))
	if err != nil || width <= 0 {
		return 0, 0, false
	}
	height, err := strconv.Atoi(strings.TrimSpace(right))
	if err != nil || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// aspectCropDimensions computes the largest centered crop rectangle with the
// target aspect inside a width×height image — sips' -c crops from the center,
// so the rect is all that is needed. Terms may exceed the image's own aspect
// (cropping 16:9 out of 9:16 keeps the full width and a shorter height).
func aspectCropDimensions(width, height, aspectWidth, aspectHeight int) (int, int) {
	if width <= 0 || height <= 0 || aspectWidth <= 0 || aspectHeight <= 0 {
		return width, height
	}
	target := float64(aspectWidth) / float64(aspectHeight)
	current := float64(width) / float64(height)
	cropWidth, cropHeight := width, height
	if current > target {
		cropWidth = int(math.Round(float64(height) * target))
	} else {
		cropHeight = int(math.Round(float64(width) / target))
	}
	if cropWidth < 1 {
		cropWidth = 1
	}
	if cropHeight < 1 {
		cropHeight = 1
	}
	if cropWidth > width {
		cropWidth = width
	}
	if cropHeight > height {
		cropHeight = height
	}
	return cropWidth, cropHeight
}

// ---------------------------------------------------------------------------
// Arg builders (pure — pinned by tests)
// ---------------------------------------------------------------------------

// sipsConvertArgs converts format, with an optional quality (a percentage
// sips' formatOptions applies to jpeg/heic/jp2 — validated upstream).
func sipsConvertArgs(input, format, quality, output string) []string {
	args := []string{"-s", "format", format}
	if quality != "" {
		args = append(args, "-s", "formatOptions", quality)
	}
	return append(args, input, "--out", output)
}

// sipsCropArgs center-crops to height×width (sips takes H before W — pinned
// by tests so the classic swap cannot sneak back in).
func sipsCropArgs(input string, height, width int, output string) []string {
	return []string{"-c", strconv.Itoa(height), strconv.Itoa(width), input, "--out", output}
}

// sipsResizeExactArgs resamples to exactly height×width (H before W).
func sipsResizeExactArgs(input string, height, width int, output string) []string {
	return []string{"-z", strconv.Itoa(height), strconv.Itoa(width), input, "--out", output}
}

// sipsResizeWidthArgs resamples so the width is width pixels, preserving
// aspect.
func sipsResizeWidthArgs(input string, width int, output string) []string {
	return []string{"--resampleWidth", strconv.Itoa(width), input, "--out", output}
}

// sipsResizeHeightArgs resamples so the height is height pixels, preserving
// aspect.
func sipsResizeHeightArgs(input string, height int, output string) []string {
	return []string{"--resampleHeight", strconv.Itoa(height), input, "--out", output}
}

// sipsRotateArgs rotates clockwise by degrees (90, 180, or 270 — validated
// upstream).
func sipsRotateArgs(input string, degrees int, output string) []string {
	return []string{"-r", strconv.Itoa(degrees), input, "--out", output}
}

// sipsFlipArgs flips horizontally or vertically.
func sipsFlipArgs(input, direction, output string) []string {
	return []string{"-f", direction, input, "--out", output}
}

// magickGravity maps a planner-facing watermark position onto ImageMagick
// gravity, plus the geometry offset that goes with it (corners get a margin;
// center does not).
func magickGravity(position string) (gravity, offset string) {
	switch position {
	case "center":
		return "Center", "+0+0"
	case "top-left":
		return "NorthWest", "+20+20"
	case "top-right":
		return "NorthEast", "+20+20"
	case "bottom-left":
		return "SouthWest", "+20+20"
	default: // bottom-right
		return "SouthEast", "+20+20"
	}
}

// magickWatermarkArgs composites overlay onto base. scalePercent resizes the
// overlay to a percentage of its own size ("25" → 25%); opacityPercent (0-100)
// multiplies the overlay's alpha channel. The parenthesized overlay group and
// literal "(" ")" args are passed to exec directly — no shell interprets them.
func magickWatermarkArgs(base, overlay, gravity, offset string, scalePercent int, opacityPercent int, hasOpacity bool, output string) []string {
	group := []string{"(", overlay}
	if scalePercent > 0 {
		group = append(group, "-resize", strconv.Itoa(scalePercent)+"%")
	}
	if hasOpacity && opacityPercent >= 0 {
		fraction := float64(opacityPercent) / 100
		group = append(group, "-channel", "A", "-evaluate", "multiply", strconv.FormatFloat(fraction, 'f', 2, 64), "+channel")
	}
	group = append(group, ")")
	args := append([]string{base}, group...)
	return append(args, "-gravity", gravity, "-geometry", offset, "-compose", "over", "-composite", output)
}

// magickCollageArgs tiles inputs into a grid of uniform square cells via
// parenthesized +append row groups joined by -append — deliberately NOT the
// montage tool, whose tile labels need a font delegate default Homebrew
// installs lack. Each image is contained into cellSize with a white
// background (small images are never upscaled into blur).
func magickCollageArgs(inputs []string, columns, cellSize int, output string) []string {
	cell := fmt.Sprintf("%dx%d", cellSize, cellSize)
	args := []string{}
	for rowStart := 0; rowStart < len(inputs); rowStart += columns {
		rowEnd := rowStart + columns
		if rowEnd > len(inputs) {
			rowEnd = len(inputs)
		}
		args = append(args, "(")
		args = append(args, inputs[rowStart:rowEnd]...)
		args = append(args, "-resize", cell, "-background", "white", "-gravity", "center", "-extent", cell, "+append", ")")
	}
	return append(args, "-background", "white", "-append", output)
}

// magickAdjustArgs applies color adjustments: brightness/contrast (each
// -100..100), saturation percentage (0-200, 100 unchanged), then an optional
// grayscale or sepia tone.
func magickAdjustArgs(input string, brightness, contrast int, hasBrightnessContrast bool, saturation int, hasSaturation bool, grayscale, sepia bool, output string) []string {
	args := []string{input}
	if hasBrightnessContrast {
		args = append(args, "-brightness-contrast", fmt.Sprintf("%dx%d", brightness, contrast))
	}
	if hasSaturation {
		args = append(args, "-modulate", fmt.Sprintf("100,%d", saturation))
	}
	switch {
	case grayscale:
		args = append(args, "-colorspace", "Gray")
	case sepia:
		args = append(args, "-sepia-tone", "80%")
	}
	return append(args, output)
}

// magickOptimizeArgs strips metadata and re-encodes with a quality percentage
// (0 keeps ImageMagick's default rather than forcing one).
func magickOptimizeArgs(input string, quality int, output string) []string {
	args := []string{input, "-strip"}
	if quality > 0 {
		args = append(args, "-quality", strconv.Itoa(quality))
	}
	return append(args, output)
}

// ---------------------------------------------------------------------------
// Shared executor plumbing
// ---------------------------------------------------------------------------

// imageResultFromStagedFile reads a staged output image into the ToolImageResult
// the artifact pipeline consumes (data URL + backend attribution).
func imageResultFromStagedFile(stagedPath, format, prompt string, backend string) (ToolImageResult, error) {
	data, err := os.ReadFile(stagedPath)
	if err != nil || len(data) == 0 {
		return ToolImageResult{}, fmt.Errorf("the image tool produced no output file")
	}
	dataURL := "data:" + imageFormatMime(format) + ";base64," + base64.StdEncoding.EncodeToString(data)
	return ToolImageResult{Model: backend, Prompt: prompt, Count: 1, Images: []string{dataURL}}, nil
}

// stageAttachedImage stages one attached image data URL into dir as base+ext,
// returning the path and the media type (for output-format decisions).
func stageAttachedImage(dir, base, dataURL string) (path, mediaType string, err error) {
	data, mediaType, err := decodeMediaDataURL(dataURL)
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, base+stagedMediaExtension(mediaType))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", err
	}
	return path, mediaType, nil
}

// parseScalePercent reads compose_images' Scale input — an overlay size as a
// percentage, "25" or "25%". 0 means unset.
func parseScalePercent(token string) int {
	token = strings.TrimSuffix(strings.TrimSpace(token), "%")
	value, err := strconv.Atoi(token)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// ---------------------------------------------------------------------------
// Unavailable-capability notes (first-run UX, mirroring local_ffmpeg.go)
// ---------------------------------------------------------------------------

// imageEditUnavailableNote composes the code-authored note delivered to the
// final model — as its own trailing user message, never in the system prompt
// — when triage flagged the turn as an image edit and some local image
// backend is missing. Empty when everything is configured. The realistic
// macOS case is sips present (it ships with the OS) and ImageMagick missing;
// the other branches keep non-macOS and override setups honest.
func imageEditUnavailableNote(sipsAvailable, magickAvailable bool) string {
	switch {
	case !sipsAvailable && !magickAvailable:
		return "Atelier note: the user's latest request asks for a local image edit — converting format, resizing or cropping, rotating or flipping, watermarking, building a collage, adjusting colors, or stripping metadata — but no local image tool backend is available on this machine, so Atelier has no tool that can perform it. Do not claim the edit was done and do not attempt it through other tools. Tell the user plainly that local image editing needs macOS's built-in sips (missing here — are you on a non-macOS build?) for basic edits and a one-time `brew install imagemagick` for watermark/collage/color-adjust tools; Atelier detects them automatically on the next message."
	case sipsAvailable && !magickAvailable:
		return "Atelier note: the user's latest request asks for a local image edit. Basic edits (format conversion including HEIC, resize, crop, rotate, flip) are available through macOS's built-in sips, but the watermark, collage, color-adjust, and metadata-strip tools run on ImageMagick, which is not installed on this machine. If the request needs those, do not claim it was done and do not attempt it through other tools — tell the user plainly that a one-time `brew install imagemagick` (or an explicit binary in Settings → Image Tools) enables them, and Atelier detects it automatically on the next message."
	case !sipsAvailable && magickAvailable:
		return "Atelier note: the user's latest request asks for a local image edit. The watermark, collage, color-adjust, and metadata-strip tools are available, but the format-conversion, resize/crop/rotate/flip, and image-facts tools run on macOS's built-in sips CLI, which was not found on this machine — check Settings → Image Tools for a misconfigured override. Do not claim an unavailable edit was done."
	default:
		return ""
	}
}

// imageEditFallbackNotice returns the deterministic one-line blockquote for
// the chat reply when an image-edit capability is missing and the final
// model's answer did not already name the remedy — mirroring
// mediaEditFallbackNotice. Empty when everything is available or the answer
// covered it.
func imageEditFallbackNotice(unavailable bool, assistantContent string) string {
	if !unavailable {
		return ""
	}
	lower := strings.ToLower(assistantContent)
	if strings.Contains(lower, "imagemagick") || strings.Contains(lower, "sips") {
		return ""
	}
	return "> ⚠️ Some local image editing isn't available yet — install ImageMagick (`brew install imagemagick`, or set the binary in Settings → Image Tools) and Atelier picks it up automatically."
}

// ---------------------------------------------------------------------------
// sips tool definitions
// ---------------------------------------------------------------------------

// sipsImageToolDefinitions assembles the sips-backed image tool catalog.
func sipsImageToolDefinitions() []HarnessToolDefinition {
	return []HarnessToolDefinition{
		convertImageToolDefinition(),
		transformImageToolDefinition(),
		probeImageToolDefinition(),
	}
}

// convertImageToolDefinition exposes convert_image: change an attached
// image's format (and quality) using macOS sips.
func convertImageToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "convert_image",
		Title:       "Convert image",
		Description: "Use this when the user asks to convert an attached image to another format — make a JPEG or PNG out of an iPhone HEIC photo, export as HEIC/AVIF/TIFF/GIF/BMP/JP2, or re-encode at a smaller size. Requires an attached image (one attached or @-mentioned this turn, or the conversation's newest image). format is one of \"jpeg\", \"png\", \"heic\", \"avif\", \"tiff\", \"gif\", \"bmp\", \"jp2\" (webp output needs the optimize_image tool); quality is optional, 1-100, and applies to jpeg/heic/jp2. The converted image is attached to the assistant reply and becomes the conversation's newest image.",
		Example:     `{"name":"convert_image","format":"jpeg","quality":85}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: convertImageParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			format := strings.TrimSpace(call.Format)
			if format == "" {
				return []string{prefix + ".format is required for convert_image (\"jpeg\", \"png\", \"heic\", \"avif\", \"tiff\", \"gif\", \"bmp\", or \"jp2\")"}
			}
			if !sipsWritableFormats[format] {
				return []string{prefix + ".format must be one of \"jpeg\", \"png\", \"heic\", \"avif\", \"tiff\", \"gif\", \"bmp\", \"jp2\" for convert_image (webp output is available via optimize_image)"}
			}
			if call.Quality != 0 {
				if call.Quality < 1 || call.Quality > 100 {
					return []string{prefix + ".quality must be 1-100 for convert_image"}
				}
				if !sipsQualityFormats[format] {
					return []string{prefix + ".quality applies to jpeg, heic, or jp2 output only — omit it for " + format}
				}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedImage(tools.AttachedImages)
			if source == "" {
				return nil, "conversion requires an attached image", errors.New("convert_image requires an attached image — ask the user to attach one first")
			}
			format := strings.TrimSpace(call.Format)
			quality := ""
			if call.Quality > 0 && sipsQualityFormats[format] {
				quality = strconv.Itoa(call.Quality)
			}
			staging, err := os.MkdirTemp("", "atelier-sips-*")
			if err != nil {
				return nil, "conversion failed", err
			}
			defer os.RemoveAll(staging)
			input, _, err := stageAttachedImage(staging, "input", source)
			if err != nil {
				return nil, "conversion failed", err
			}
			output := filepath.Join(staging, "converted"+imageFormatExtension(format))
			if _, err := runLocalSips(ctx, tools.Config, sipsConvertArgs(input, format, quality, output)); err != nil {
				return nil, "conversion failed", err
			}
			prompt := "converted to " + format
			if quality != "" {
				prompt += " (quality " + quality + ")"
			}
			result, err := imageResultFromStagedFile(output, format, prompt, sipsModelName)
			if err != nil {
				return nil, "conversion failed", err
			}
			return result, fmt.Sprintf("converted the attached image to %s with sips", format), nil
		},
		Activity: sipsActivity("convert"),
	}
}

// transformImageToolDefinition exposes transform_image: crop to an aspect
// ratio, resize, rotate, or flip an attached image using macOS sips.
func transformImageToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "transform_image",
		Title:       "Transform image",
		Description: "Use this when the user asks to resize, crop, rotate, or flip an attached image — fit it to a social size (crop to 1:1, 4:5, 9:16, 16:9), scale to specific pixel dimensions, straighten a sideways phone photo, or mirror it. Requires an attached image (one attached or @-mentioned this turn, or the conversation's newest image). aspectRatio crops (from the center) to a W:H ratio like \"1:1\" or \"16:9\"; width and/or height resize afterwards — both together resizes to exactly those pixels (set aspectRatio too when the target shape matters), one alone preserves aspect; rotate is 90, 180, or 270 clockwise; flip is \"horizontal\" or \"vertical\". At least one operation is required. The result is attached to the assistant reply and becomes the conversation's newest image.",
		Example:     `{"name":"transform_image","aspectRatio":"4:5","width":1080}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: transformImageParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			aspect := strings.TrimSpace(call.AspectRatio)
			if aspect != "" {
				if _, _, ok := parseAspectRatio(aspect); !ok {
					return []string{prefix + `.aspectRatio must be a W:H ratio like "1:1" or "16:9" for transform_image`}
				}
			}
			if call.Width < 0 || call.Height < 0 {
				return []string{prefix + ".width and .height must be positive pixel counts for transform_image"}
			}
			if call.Width == 0 && call.Height == 0 && aspect == "" && call.Rotate == 0 && strings.TrimSpace(call.Flip) == "" {
				return []string{prefix + ".transform_image needs at least one of aspectRatio, width, height, rotate, or flip"}
			}
			switch call.Rotate {
			case 0, 90, 180, 270:
			default:
				return []string{prefix + ".rotate must be 90, 180, or 270 for transform_image"}
			}
			switch strings.TrimSpace(call.Flip) {
			case "", "horizontal", "vertical":
			default:
				return []string{prefix + `.flip must be "horizontal" or "vertical" for transform_image`}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedImage(tools.AttachedImages)
			if source == "" {
				return nil, "transform requires an attached image", errors.New("transform_image requires an attached image — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-sips-*")
			if err != nil {
				return nil, "transform failed", err
			}
			defer os.RemoveAll(staging)
			current, mediaType, err := stageAttachedImage(staging, "input", source)
			if err != nil {
				return nil, "transform failed", err
			}
			outputFormat, transcodeNotice := sipsFormatForMediaType(mediaType)
			step := 0
			next := func(ext string) string {
				step++
				return filepath.Join(staging, fmt.Sprintf("step-%02d%s", step, ext))
			}
			ops := []string{}
			if aspect := strings.TrimSpace(call.AspectRatio); aspect != "" {
				probe, err := probeStagedImage(ctx, tools.Config, current)
				if err != nil {
					return nil, "transform failed", fmt.Errorf("the image's dimensions could not be read for the %s crop: %w", aspect, err)
				}
				aspectWidth, aspectHeight, _ := parseAspectRatio(aspect)
				cropWidth, cropHeight := aspectCropDimensions(probe.Width, probe.Height, aspectWidth, aspectHeight)
				target := next(imageFormatExtension(outputFormat))
				if _, err := runLocalSips(ctx, tools.Config, sipsCropArgs(current, cropHeight, cropWidth, target)); err != nil {
					return nil, "transform failed", err
				}
				current = target
				ops = append(ops, fmt.Sprintf("cropped to %s (center %dx%d)", aspect, cropWidth, cropHeight))
			}
			if call.Width > 0 || call.Height > 0 {
				target := next(imageFormatExtension(outputFormat))
				var args []string
				switch {
				case call.Width > 0 && call.Height > 0:
					args = sipsResizeExactArgs(current, call.Height, call.Width, target)
					ops = append(ops, fmt.Sprintf("resized to %dx%d", call.Width, call.Height))
				case call.Width > 0:
					args = sipsResizeWidthArgs(current, call.Width, target)
					ops = append(ops, fmt.Sprintf("resized to %d pixels wide", call.Width))
				default:
					args = sipsResizeHeightArgs(current, call.Height, target)
					ops = append(ops, fmt.Sprintf("resized to %d pixels tall", call.Height))
				}
				if _, err := runLocalSips(ctx, tools.Config, args); err != nil {
					return nil, "transform failed", err
				}
				current = target
			}
			if call.Rotate != 0 {
				target := next(imageFormatExtension(outputFormat))
				if _, err := runLocalSips(ctx, tools.Config, sipsRotateArgs(current, call.Rotate, target)); err != nil {
					return nil, "transform failed", err
				}
				current = target
				ops = append(ops, fmt.Sprintf("rotated %d° clockwise", call.Rotate))
			}
			if flip := strings.TrimSpace(call.Flip); flip != "" {
				target := next(imageFormatExtension(outputFormat))
				if _, err := runLocalSips(ctx, tools.Config, sipsFlipArgs(current, flip, target)); err != nil {
					return nil, "transform failed", err
				}
				current = target
				ops = append(ops, "flipped "+flip)
			}
			prompt := "transform: " + strings.Join(ops, ", ")
			result, err := imageResultFromStagedFile(current, outputFormat, prompt, sipsModelName)
			if err != nil {
				return nil, "transform failed", err
			}
			if transcodeNotice != "" {
				result.Notices = append(result.Notices, transcodeNotice)
			}
			return result, fmt.Sprintf("transformed the attached image with sips (%s)", strings.Join(ops, ", ")), nil
		},
		Activity: sipsActivity("transform"),
	}
}

// probeImageToolDefinition exposes probe_image: sips facts of an attached
// image as evidence — dimensions for crop planning, format and size for
// format decisions.
func probeImageToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "probe_image",
		Title:       "Probe image",
		Description: "Use this when the user asks about an attached image's properties (how big is it, what format, does it have an alpha channel, what file size) or when planning crops and conversions needs exact numbers. Runs macOS sips on the newest attached image and returns dimensions, format, color space, bit depth, alpha, and file size as evidence. No parameters.",
		Example:     `{"name":"probe_image"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: probeImageParamSchema(),
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedImage(tools.AttachedImages)
			if source == "" {
				return nil, "probe requires an attached image", errors.New("probe_image requires an attached image — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-sips-*")
			if err != nil {
				return nil, "probe failed", err
			}
			defer os.RemoveAll(staging)
			input, _, err := stageAttachedImage(staging, "input", source)
			if err != nil {
				return nil, "probe failed", err
			}
			result, err := probeStagedImage(ctx, tools.Config, input)
			if err != nil {
				return nil, "probe failed", err
			}
			return result, probeImageSummary(result), nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			activity.Provider = "sips"
			activity.Command = []string{"sips", "probe"}
			return activity
		},
	}
}

// ---------------------------------------------------------------------------
// ImageMagick tool definitions
// ---------------------------------------------------------------------------

// imagemagickImageToolDefinitions assembles the ImageMagick-backed image
// tool catalog.
func imagemagickImageToolDefinitions() []HarnessToolDefinition {
	return []HarnessToolDefinition{
		composeImagesToolDefinition(),
		adjustImageToolDefinition(),
		optimizeImageToolDefinition(),
	}
}

// composeImagesToolDefinition exposes compose_images: watermark an image
// with an overlay, or build a collage grid of several images.
func composeImagesToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "compose_images",
		Title:       "Compose images",
		Description: "Use this when the user asks to put a logo or watermark on an image, overlay one image onto another, or combine several images into one grid/collage/contact sheet. Requires at least two images attached or @-mentioned this turn. mode \"watermark\" composites the SECOND attached image over the first (the base) — position is \"bottom-right\" (default), \"bottom-left\", \"top-right\", \"top-left\", or \"center\"; scale sizes the overlay as a percentage of its own size (\"25\" for a small corner logo); opacity is 0-100 (a subtle watermark is often 50-70). mode \"collage\" tiles every attached image in attachment order into a square-celled grid. The result is attached to the assistant reply and becomes the conversation's newest image.",
		Example:     `{"name":"compose_images","mode":"watermark","position":"bottom-right","scale":25,"opacity":70}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: composeImagesParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			mode := strings.TrimSpace(call.Mode)
			if mode == "" {
				mode = "watermark"
			}
			switch mode {
			case "watermark", "collage":
			default:
				return []string{prefix + `.mode must be "watermark" or "collage" for compose_images`}
			}
			switch strings.TrimSpace(call.Position) {
			case "", "center", "top-left", "top-right", "bottom-left", "bottom-right":
			default:
				return []string{prefix + `.position must be "center", "top-left", "top-right", "bottom-left", or "bottom-right" for compose_images`}
			}
			if call.Opacity < 0 || call.Opacity > 100 {
				return []string{prefix + ".opacity must be 0-100 for compose_images"}
			}
			if scale := parseScalePercent(call.Scale); scale > 100 {
				return []string{prefix + ".scale must be a percentage of the overlay's size, at most 100, for compose_images"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			images := nonEmptyImages(tools.AttachedImages)
			if len(images) < 2 {
				return nil, "compose needs at least two images", errors.New("compose_images needs at least two images — for a watermark, attach the base image first and the logo/overlay second; for a collage, attach every image in order")
			}
			staging, err := os.MkdirTemp("", "atelier-magick-*")
			if err != nil {
				return nil, "compose failed", err
			}
			defer os.RemoveAll(staging)
			mode := strings.TrimSpace(call.Mode)
			if mode == "" {
				mode = "watermark"
			}
			if mode == "collage" {
				inputs := make([]string, 0, len(images))
				for i, image := range images {
					input, _, err := stageAttachedImage(staging, fmt.Sprintf("input-%03d", i+1), image)
					if err != nil {
						return nil, "collage failed", err
					}
					inputs = append(inputs, input)
				}
				columns := int(math.Ceil(math.Sqrt(float64(len(inputs)))))
				output := filepath.Join(staging, "collage.png")
				if _, err := runLocalImageMagick(ctx, tools.Config, magickCollageArgs(inputs, columns, 1024, output)); err != nil {
					return nil, "collage failed", err
				}
				result, err := imageResultFromStagedFile(output, "png", fmt.Sprintf("collage of %d images", len(inputs)), imageMagickModelName)
				if err != nil {
					return nil, "collage failed", err
				}
				return result, fmt.Sprintf("combined %d attached images into a %d-column grid with ImageMagick", len(inputs), columns), nil
			}
			base, _, err := stageAttachedImage(staging, "base", images[0])
			if err != nil {
				return nil, "watermark failed", err
			}
			overlay, _, err := stageAttachedImage(staging, "overlay", images[1])
			if err != nil {
				return nil, "watermark failed", err
			}
			position := strings.TrimSpace(call.Position)
			if position == "" {
				position = "bottom-right"
			}
			gravity, offset := magickGravity(position)
			scale := parseScalePercent(call.Scale)
			output := filepath.Join(staging, "watermarked.png")
			if _, err := runLocalImageMagick(ctx, tools.Config, magickWatermarkArgs(base, overlay, gravity, offset, scale, call.Opacity, call.Opacity > 0, output)); err != nil {
				return nil, "watermark failed", err
			}
			prompt := "watermark at " + position
			if scale > 0 {
				prompt += fmt.Sprintf(", overlay at %d%%", scale)
			}
			if call.Opacity > 0 {
				prompt += fmt.Sprintf(", %d%% opacity", call.Opacity)
			}
			result, err := imageResultFromStagedFile(output, "png", prompt, imageMagickModelName)
			if err != nil {
				return nil, "watermark failed", err
			}
			return result, fmt.Sprintf("composited the second attached image over the first (%s) with ImageMagick", position), nil
		},
		Activity: magickActivity("compose"),
	}
}

// adjustImageToolDefinition exposes adjust_image: brightness, contrast,
// saturation, grayscale, or sepia on an attached image.
func adjustImageToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "adjust_image",
		Title:       "Adjust image",
		Description: "Use this when the user asks to adjust an attached image's look — brighter/darker, more/less contrast, more/less saturated or vivid, black & white (grayscale), or a vintage sepia tone. Requires an attached image (one attached or @-mentioned this turn, or the conversation's newest image). brightness and contrast are -100 to 100 (0 unchanged); saturation is 0-200 percent (100 unchanged, 0 fully desaturated); grayscale and sepia are true/false shortcuts that cannot be combined. At least one adjustment is required. The adjusted image is attached to the assistant reply and becomes the conversation's newest image.",
		Example:     `{"name":"adjust_image","brightness":10,"saturation":130}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: adjustImageParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			if call.Brightness < -100 || call.Brightness > 100 {
				return []string{prefix + ".brightness must be -100 to 100 for adjust_image"}
			}
			if call.Contrast < -100 || call.Contrast > 100 {
				return []string{prefix + ".contrast must be -100 to 100 for adjust_image"}
			}
			if call.Saturation < 0 || call.Saturation > 200 {
				return []string{prefix + ".saturation must be 0-200 for adjust_image (100 is unchanged)"}
			}
			if call.Grayscale && call.Sepia {
				return []string{prefix + ".grayscale and .sepia cannot be combined — pick one look for adjust_image"}
			}
			if call.Brightness == 0 && call.Contrast == 0 && call.Saturation == 0 && !call.Grayscale && !call.Sepia {
				return []string{prefix + ".adjust_image needs at least one of brightness, contrast, saturation, grayscale, or sepia"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedImage(tools.AttachedImages)
			if source == "" {
				return nil, "adjust requires an attached image", errors.New("adjust_image requires an attached image — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-magick-*")
			if err != nil {
				return nil, "adjust failed", err
			}
			defer os.RemoveAll(staging)
			input, mediaType, err := stageAttachedImage(staging, "input", source)
			if err != nil {
				return nil, "adjust failed", err
			}
			format, _ := sipsFormatForMediaType(mediaType)
			// ImageMagick writes webp fine, so a webp input keeps its format.
			if strings.Contains(strings.ToLower(mediaType), "webp") {
				format = "webp"
			}
			output := filepath.Join(staging, "adjusted"+imageFormatExtension(format))
			hasBrightnessContrast := call.Brightness != 0 || call.Contrast != 0
			hasSaturation := call.Saturation != 0 && call.Saturation != 100
			if _, err := runLocalImageMagick(ctx, tools.Config, magickAdjustArgs(input, call.Brightness, call.Contrast, hasBrightnessContrast, call.Saturation, hasSaturation, call.Grayscale, call.Sepia, output)); err != nil {
				return nil, "adjust failed", err
			}
			var ops []string
			if hasBrightnessContrast {
				ops = append(ops, fmt.Sprintf("brightness %d, contrast %d", call.Brightness, call.Contrast))
			}
			if hasSaturation {
				ops = append(ops, fmt.Sprintf("saturation %d%%", call.Saturation))
			}
			if call.Grayscale {
				ops = append(ops, "grayscale")
			}
			if call.Sepia {
				ops = append(ops, "sepia")
			}
			prompt := "adjust: " + strings.Join(ops, ", ")
			result, err := imageResultFromStagedFile(output, format, prompt, imageMagickModelName)
			if err != nil {
				return nil, "adjust failed", err
			}
			return result, fmt.Sprintf("adjusted the attached image with ImageMagick (%s)", strings.Join(ops, ", ")), nil
		},
		Activity: magickActivity("adjust"),
	}
}

// optimizeImageToolDefinition exposes optimize_image: strip metadata (EXIF,
// GPS) and recompress an attached image for sharing, optionally converting
// format.
func optimizeImageToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "optimize_image",
		Title:       "Optimize image",
		Description: "Use this when the user asks to remove an image's metadata (EXIF, GPS location, camera info) before sharing, shrink a file for the web, or save a smaller copy — and for webp output, which convert_image cannot produce. Requires an attached image (one attached or @-mentioned this turn, or the conversation's newest image). Always strips metadata; quality is optional 1-100 (85 when omitted for jpeg/webp/heic); format optionally converts at the same time — \"webp\", \"jpeg\", \"png\", \"heic\", \"avif\", \"tiff\", \"gif\", \"bmp\", \"jp2\" (omit to keep the input format). The optimized image is attached to the assistant reply and becomes the conversation's newest image.",
		Example:     `{"name":"optimize_image","quality":80}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: optimizeImageParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			if format := strings.TrimSpace(call.Format); format != "" && !imagemagickFormats[format] {
				return []string{prefix + `.format must be one of "webp", "jpeg", "png", "heic", "avif", "tiff", "gif", "bmp", "jp2" for optimize_image`}
			}
			if call.Quality < 0 || call.Quality > 100 {
				return []string{prefix + ".quality must be 1-100 for optimize_image"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			source := firstAttachedImage(tools.AttachedImages)
			if source == "" {
				return nil, "optimize requires an attached image", errors.New("optimize_image requires an attached image — ask the user to attach one first")
			}
			staging, err := os.MkdirTemp("", "atelier-magick-*")
			if err != nil {
				return nil, "optimize failed", err
			}
			defer os.RemoveAll(staging)
			input, mediaType, err := stageAttachedImage(staging, "input", source)
			if err != nil {
				return nil, "optimize failed", err
			}
			format := strings.TrimSpace(call.Format)
			if format == "" {
				if format, _ = sipsFormatForMediaType(mediaType); strings.Contains(strings.ToLower(mediaType), "webp") {
					format = "webp"
				}
			}
			quality := call.Quality
			switch format {
			case "jpeg", "webp", "heic":
				if quality == 0 {
					quality = 85
				}
			}
			output := filepath.Join(staging, "optimized"+imageFormatExtension(format))
			if _, err := runLocalImageMagick(ctx, tools.Config, magickOptimizeArgs(input, quality, output)); err != nil {
				return nil, "optimize failed", err
			}
			prompt := "metadata stripped"
			if quality > 0 {
				prompt += fmt.Sprintf(", quality %d", quality)
			}
			result, err := imageResultFromStagedFile(output, format, prompt, imageMagickModelName)
			if err != nil {
				return nil, "optimize failed", err
			}
			return result, fmt.Sprintf("stripped metadata and re-encoded the attached image as %s with ImageMagick", format), nil
		},
		Activity: magickActivity("optimize"),
	}
}

// ---------------------------------------------------------------------------
// Activities and summaries
// ---------------------------------------------------------------------------

// sipsActivity is the shared activity builder for the sips image tools:
// default media fields (kind/count/model ride the result type) plus the
// provider attribution. Provider is pre-filled so the engine layer's
// fal/ollama attribution (toolActivityFromResult) skips these.
func sipsActivity(verb string) func(result HarnessToolResult) HarnessToolActivity {
	return func(result HarnessToolResult) HarnessToolActivity {
		activity := defaultHarnessToolActivity(result)
		activity.Provider = "sips"
		activity.Command = []string{"sips", verb}
		return activity
	}
}

// magickActivity is the ImageMagick counterpart of sipsActivity.
func magickActivity(verb string) func(result HarnessToolResult) HarnessToolActivity {
	return func(result HarnessToolResult) HarnessToolActivity {
		activity := defaultHarnessToolActivity(result)
		activity.Provider = "imagemagick"
		activity.Command = []string{"magick", verb}
		return activity
	}
}

func probeImageSummary(result ToolImageProbeResult) string {
	parts := []string{fmt.Sprintf("%dx%d", result.Width, result.Height)}
	if result.Format != "" {
		parts = append(parts, result.Format)
	}
	if result.BitsPerSample > 0 {
		parts = append(parts, fmt.Sprintf("%d-bit", result.BitsPerSample))
	}
	if result.ColorSpace != "" {
		parts = append(parts, result.ColorSpace)
	}
	if result.HasAlpha {
		parts = append(parts, "alpha")
	}
	if result.SizeBytes > 0 {
		parts = append(parts, fmt.Sprintf("%.1fKB", float64(result.SizeBytes)/1024))
	}
	return fmt.Sprintf("probed the attached image with sips: %s", strings.Join(parts, ", "))
}

// ---------------------------------------------------------------------------
// Param schemas
// ---------------------------------------------------------------------------

func convertImageParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"format":  stringParam(`The output format — "jpeg", "png", "heic", "avif", "tiff", "gif", "bmp", or "jp2". Required.`),
			"quality": intParam(`Optional — 1-100 encoding quality, for jpeg/heic/jp2 output.`),
		},
		"required": []string{"format"},
	}
}

func transformImageParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"aspectRatio": stringParam(`Optional — crop to a W:H ratio like "1:1", "4:5", "9:16", "16:9" (center crop), applied before any resize.`),
			"width":       intParam("Optional — resize to this pixel width (with height, exact pixels; alone, aspect-preserving)."),
			"height":      intParam("Optional — resize to this pixel height (with width, exact pixels; alone, aspect-preserving)."),
			"rotate":      intParam(`Optional — rotate clockwise: 90, 180, or 270.`),
			"flip":        stringParam(`Optional — "horizontal" or "vertical".`),
		},
		"required": []string{},
	}
}

func probeImageParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             []string{},
	}
}

func composeImagesParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode":     enumParam(`"watermark" (default) composites the second attached image over the first; "collage" grids every attached image in attachment order.`, "watermark", "collage"),
			"position": enumParam(`Watermark position of the overlay — "bottom-right" (default), "bottom-left", "top-right", "top-left", or "center".`, "center", "top-left", "top-right", "bottom-left", "bottom-right"),
			"scale":    stringParam(`Optional — overlay size as a percentage of its own size ("25" for a small corner logo).`),
			"opacity":  intParam("Optional — overlay opacity 0-100 (50-70 reads as a subtle watermark)."),
		},
		"required": []string{},
	}
}

func adjustImageParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"brightness": intParam("Optional — -100 to 100 (0 unchanged)."),
			"contrast":   intParam("Optional — -100 to 100 (0 unchanged)."),
			"saturation": intParam("Optional — 0-200 percent (100 unchanged, 0 desaturated)."),
			"grayscale":  boolParam("Optional — true for black & white."),
			"sepia":      boolParam("Optional — true for a vintage sepia tone."),
		},
		"required": []string{},
	}
}

func optimizeImageParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"quality": intParam("Optional — 1-100 re-encode quality (85 when omitted for jpeg/webp/heic)."),
			"format":  stringParam(`Optional — convert at the same time: "webp", "jpeg", "png", "heic", "avif", "tiff", "gif", "bmp", "jp2". Omit to keep the input format.`),
		},
		"required": []string{},
	}
}
