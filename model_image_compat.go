package main

// Model-boundary image-format normalization. Atelier accepts as attachments
// every image container the local image backends read (the HEIF family,
// AVIF, JP2, TIFF, BMP — imageExtensionForBytes), but the models that
// consume image bytes are far narrower: vision chat models and image/video
// generation source inputs decode only the web-native formats. The helpers
// here convert anything else to JPEG on the resolved basic image backend —
// sips on macOS, ImageMagick elsewhere — at the two model boundaries: the
// final-response message stream (preparedResponseRequest) and the generation
// gateways (tool_gateway). Attachments, artifacts, and the local CLI tools
// keep the original bytes (the backend edits a .heic natively; convert_image
// is the user-facing path for explicit format changes).

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
)

// modelSafeImageExtensions lists the image formats models that consume image
// bytes accept: vision chat models (OpenAI-compatible image_url parts, Ollama
// vision) and generation source inputs (fal image_url/image_urls, Ollama
// /api/generate) decode PNG, JPEG, WebP, and GIF — and, in practice, nothing
// else. Every other sips-readable extension (heic/heif/avif/jp2/tiff/bmp)
// must be converted before the bytes cross a model boundary.
var modelSafeImageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
}

// ensureModelSafeImages is the slice form of ensureModelSafeImage, preserving
// order and length. Entries are normalized independently; a payload that
// cannot be converted passes through untouched so the provider's own error —
// the pre-existing behavior — still surfaces.
func ensureModelSafeImages(ctx context.Context, config AppConfig, images []string) []string {
	if len(images) == 0 {
		return images
	}
	out := make([]string, len(images))
	for i, image := range images {
		out[i] = ensureModelSafeImage(ctx, config, image)
	}
	return out
}

// ensureModelSafeImage converts one image payload — a base64 data URL or bare
// base64 — into a form models decode, returning a JPEG data URL when the
// sniffed format is outside modelSafeImageExtensions. Everything else passes
// through unchanged: already-safe formats (no work), http(s) URLs (already
// hosted; fal fetches them), and anything undecodable (the adapters' own
// fail-closed normalization drops or rejects those, as before). Conversion
// runs on the resolved basic image backend and is fail-soft: when no backend
// resolved or the conversion errors, the original payload is returned so the
// turn proceeds exactly as it would have without this normalization.
// Decisions are made on the sniffed bytes, never the data-URL header — a
// mislabeled header must not smuggle an unsupported container past the
// boundary.
func ensureModelSafeImage(ctx context.Context, config AppConfig, payload string) string {
	dataURL := normalizeAttachedImage(payload)
	if dataURL == "" {
		return payload
	}
	data, _, err := decodeMediaDataURL(dataURL)
	if err != nil {
		return payload
	}
	extension := imageExtensionForBytes(data)
	if extension == "" || modelSafeImageExtensions[extension] {
		return payload
	}
	converted, err := convertImageBytesForModel(ctx, config, data, extension)
	if err != nil || len(converted) == 0 {
		return payload
	}
	// Trust the output's bytes, not the command's success: a backend that
	// wrote something unexpected must not ship HEIC bytes under a JPEG label.
	if !modelSafeImageExtensions[imageExtensionForBytes(converted)] {
		return payload
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(converted)
}

// convertImageBytesForModel re-encodes image bytes (whose sniffed extension
// rode in as `extension`, so the staged input gets the container the backend
// trusts) as JPEG in a scratch directory, on whichever basic image backend
// resolved. It is the silent sibling of the convert_image tool: same
// invocation shapes, no quality knob, no ToolImageResult — the output is raw
// bytes for a model-boundary data URL.
func convertImageBytesForModel(ctx context.Context, config AppConfig, data []byte, extension string) ([]byte, error) {
	backend, ok := resolveBasicImageBackend(config)
	if !ok {
		return nil, errors.New("no local image backend found — sips ships with macOS; ImageMagick serves other platforms")
	}
	staging, err := os.MkdirTemp("", "atelier-model-image-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	input := filepath.Join(staging, "input"+extension)
	if err := os.WriteFile(input, data, 0o644); err != nil {
		return nil, err
	}
	output := filepath.Join(staging, "model-safe.jpg")
	dialect := basicImageDialectFor(backend)
	if _, err := dialect.run(ctx, config, dialect.convertArgs(input, "jpeg", "", output)); err != nil {
		return nil, err
	}
	return os.ReadFile(output)
}
