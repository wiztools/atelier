package main

// Tests for model_image_compat.go: the model-boundary conversion of
// sips-readable but model-undecodable image formats (HEIC/HEIF, AVIF, JP2,
// TIFF, BMP) into JPEG for vision chat input and generation source images,
// while attachments, artifacts, and the local CLI tools keep the original
// bytes. sips is faked with a shell script (the localBinaryLookPath seam
// TestMain pins package-wide), so outcomes never depend on the host machine.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSipsJPEGScript fakes the sips CLI for conversion invocations: it writes a
// JPEG-sniffable payload (SOI + APP0 marker) to the --out path. The argument
// shape it parses is sipsConvertArgs': `-s format jpeg <input> --out <output>`.
const fakeSipsJPEGScript = `#!/bin/sh
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--out" ]; then out="$a"; fi
  prev="$a"
done
printf '\377\330\377\340FAKE-JPEG-BYTES' > "$out"
`

// fakeSipsJPEGBytes is what fakeSipsJPEGScript writes — SOI (ff d8) plus an APP0
// marker (ff e0), so imageExtensionForBytes sniffs it as .jpg.
var fakeSipsJPEGBytes = append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("FAKE-JPEG-BYTES")...)

// stubSipsJPEG writes the fake sips script under a temp dir and points the
// local-binary lookup at it for this test, pinning the platform to darwin so
// the sips backend serves regardless of the host running the tests.
func stubSipsJPEG(t *testing.T) {
	t.Helper()
	pinRuntimeGOOS(t, "darwin")
	dir := t.TempDir()
	path := filepath.Join(dir, "sips")
	if err := os.WriteFile(path, []byte(fakeSipsJPEGScript), 0o755); err != nil {
		t.Fatalf("write fake sips: %v", err)
	}
	stubLocalLookup(t, map[string]string{"sips": path})
}

// TestEnsureModelSafeImagePassThrough pins the no-work cases: anything a model
// already decodes — or that normalization can't act on — must come back
// byte-identical, never rewritten or dropped.
func TestEnsureModelSafeImagePassThrough(t *testing.T) {
	// Real magic numbers for the four model-safe formats (data URL and the
	// frontend's bare-base64 shape) plus the non-convertible shapes.
	pngBytes := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("png-body")...)
	jpgBytes := append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("jpeg-body")...)
	webpBytes := append([]byte("RIFF....WEBP"), []byte("webp-body")...)
	gifBytes := append([]byte("GIF89a"), []byte("gif-body")...)

	for name, payload := range map[string]string{
		"png data url":  "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes),
		"png bare":      base64.StdEncoding.EncodeToString(pngBytes),
		"jpeg data url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpgBytes),
		"jpeg bare":     base64.StdEncoding.EncodeToString(jpgBytes),
		"webp data url": "data:image/webp;base64," + base64.StdEncoding.EncodeToString(webpBytes),
		"gif bare":      base64.StdEncoding.EncodeToString(gifBytes),
		"http url":      "https://cdn.fal.ai/media/source-image.png",
		"unsniffable":   base64.StdEncoding.EncodeToString([]byte("not an image")),
		"whitespace":    "   ",
		"artifact path": "/atelier-artifact/conv_abc/img_1.heic",
		"jpeg with lie": "data:image/heic;base64," + base64.StdEncoding.EncodeToString(jpgBytes), // header lies, bytes are model-safe
	} {
		if got := ensureModelSafeImage(context.Background(), defaultAppConfig(), payload); got != payload {
			t.Errorf("%s: got %q, want the payload passed through unchanged", name, got)
		}
	}
	if got := ensureModelSafeImage(context.Background(), defaultAppConfig(), ""); got != "" {
		t.Errorf("empty: got %q, want empty", got)
	}
}

// TestEnsureModelSafeImageConvertsHEIC is the core conversion: a HEIF payload
// — data URL (history/mention paths) or bare base64 (the frontend's attachment
// shape) — becomes a JPEG data URL, and the decision rides the sniffed bytes,
// not the data-URL header (a mislabeled header must not smuggle a HEIF past
// the boundary).
func TestEnsureModelSafeImageConvertsHEIC(t *testing.T) {
	stubSipsJPEG(t)
	heic := heifTestImage("heic")
	encoded := base64.StdEncoding.EncodeToString(heic)
	want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(fakeSipsJPEGBytes)

	for name, payload := range map[string]string{
		"data url":     "data:image/heic;base64," + encoded,
		"bare base64":  encoded,
		"lying header": "data:image/png;base64," + encoded,
	} {
		got := ensureModelSafeImage(context.Background(), defaultAppConfig(), payload)
		if got != want {
			t.Errorf("%s: got %q, want converted JPEG data URL %q", name, got, want)
		}
	}
}

// TestEnsureModelSafeImageConvertsOtherSipsFormats pins that every
// sips-readable, model-unsafe extension converts, not just HEIC — AVIF, JP2,
// TIFF, and BMP attachments are equally undecodable at a model.
func TestEnsureModelSafeImageConvertsOtherSipsFormats(t *testing.T) {
	stubSipsJPEG(t)
	want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(fakeSipsJPEGBytes)
	tiffBytes := []byte{'I', 'I', 0x2a, 0x00, 0, 0, 0, 8}
	bmpBytes := append([]byte{'B', 'M'}, make([]byte, 16)...)

	payloads := map[string][]byte{
		"avif": heifTestImage("avif"),
		"jp2":  heifTestImage("jp2 "),
		"tiff": tiffBytes,
		"bmp":  bmpBytes,
	}
	for name, data := range payloads {
		got := ensureModelSafeImage(context.Background(), defaultAppConfig(), "data:image/*;base64,"+base64.StdEncoding.EncodeToString(data))
		if got != want {
			t.Errorf("%s: got %q, want converted JPEG data URL %q", name, got, want)
		}
	}
}

// TestEnsureModelSafeImageFailSoftWithoutSips pins the fail-soft contract: on
// a machine without sips (the TestMain-pinned lookup — non-macOS, or a broken
// override), the original payload is returned untouched so the turn proceeds
// exactly as it would have without normalization and the provider's own error
// surfaces, as before.
func TestEnsureModelSafeImageFailSoftWithoutSips(t *testing.T) {
	heic := heifTestImage("heic")
	payload := "data:image/heic;base64," + base64.StdEncoding.EncodeToString(heic)
	got := ensureModelSafeImage(context.Background(), defaultAppConfig(), payload)
	if got != payload {
		t.Errorf("got %q, want the original payload (no sips → fail-soft)", got)
	}
}

// TestEnsureModelSafeImageFailSoftOnBadConversion pins the output guard: a
// sips that exits 0 but doesn't actually convert (here: local_images_test.go's
// copy-input-to-output fake) must not ship HEIC bytes under a JPEG label — the
// original payload comes back instead.
func TestEnsureModelSafeImageFailSoftOnBadConversion(t *testing.T) {
	config, _ := sipsTestConfig(t) // the cp-based fake sips
	payload := "data:image/heic;base64," + base64.StdEncoding.EncodeToString(heifTestImage("heic"))
	got := ensureModelSafeImage(context.Background(), config, payload)
	if got != payload {
		t.Errorf("got %q, want the original payload (non-JPEG sips output → fail-soft)", got)
	}
}

// TestEnsureModelSafeImageConvertsViaImageMagickOnLinux pins the non-macOS
// boundary: with the platform pinned off darwin, the same HEIC conversion
// routes through ImageMagick — sips is not even consulted (the stub lookup
// has no entry for it).
func TestEnsureModelSafeImageConvertsViaImageMagickOnLinux(t *testing.T) {
	pinRuntimeGOOS(t, "linux")
	// The fake magick writes JPEG-sniffable bytes to its LAST argument (every
	// convert-style invocation passes the output path last).
	dir := t.TempDir()
	path := filepath.Join(dir, "magick")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nout=\"\"\nfor a in \"$@\"; do out=\"$a\"; done\nprintf '\\377\\330\\377\\340FAKE-JPEG-BYTES' > \"$out\"\n"), 0o755); err != nil {
		t.Fatalf("write fake magick: %v", err)
	}
	stubLocalLookup(t, map[string]string{"magick": path})

	payload := "data:image/heic;base64," + base64.StdEncoding.EncodeToString(heifTestImage("heic"))
	want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(fakeSipsJPEGBytes)
	if got := ensureModelSafeImage(context.Background(), defaultAppConfig(), payload); got != want {
		t.Fatalf("got %q, want converted JPEG data URL %q", got, want)
	}
}

// TestEnsureModelSafeImagesPreservesOrder pins the slice contract: order and
// length are preserved, and only the model-unsafe entries are rewritten.
func TestEnsureModelSafeImagesPreservesOrder(t *testing.T) {
	stubSipsJPEG(t)
	converted := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(fakeSipsJPEGBytes)
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString(append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("body")...))
	heic := "data:image/heic;base64," + base64.StdEncoding.EncodeToString(heifTestImage("heic"))

	got := ensureModelSafeImages(context.Background(), defaultAppConfig(), []string{png, heic, png})
	want := []string{png, converted, png}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if images := ensureModelSafeImages(context.Background(), defaultAppConfig(), nil); images != nil {
		t.Errorf("nil input: got %+v, want nil", images)
	}
}

// TestPreparedResponseRequestConvertsVisionHEIC pins the vision boundary: the
// final-response request's message images are converted, whether the HEIC
// arrived on the last user message (the frontend's bare-base64 attachment
// shape) or through the history/mention fallback injection (data URLs).
func TestPreparedResponseRequestConvertsVisionHEIC(t *testing.T) {
	stubSipsJPEG(t)
	engine := newHarnessEngine(defaultAppConfig(), nil) // nil app → permissive strip (images always kept)
	converted := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(fakeSipsJPEGBytes)
	lastUserImages := func(messages []ChatMessage) []string {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				return messages[i].Images
			}
		}
		t.Fatal("no user message in result")
		return nil
	}

	// Current-turn attachment: bare base64 HEIC on the last user message.
	req := ChatRequest{
		Model: "primary-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "what is in this photo?", Images: []string{
				base64.StdEncoding.EncodeToString(heifTestImage("heic")),
			}},
		},
	}
	result, _ := engine.preparedResponseRequest(context.Background(), req, "primary-model", "openrouter", HarnessPreparedTurn{}, nil)
	if images := lastUserImages(result.Messages); len(images) != 1 || images[0] != converted {
		t.Errorf("attachment branch: last user Images = %+v, want [%q]", images, converted)
	}

	// History-fallback injection: a resolved HEIC data URL lands on the last
	// user message and converts there too.
	req = ChatRequest{
		Model: "primary-model",
		Messages: []ChatMessage{
			{Role: "assistant", Content: "Here is the photo."},
			{Role: "user", Content: "describe the photo again"},
		},
	}
	result, _ = engine.preparedResponseRequest(context.Background(), req, "primary-model", "openrouter", HarnessPreparedTurn{}, []string{
		"data:image/heic;base64," + base64.StdEncoding.EncodeToString(heifTestImage("mif1")),
	})
	if images := lastUserImages(result.Messages); len(images) != 1 || images[0] != converted {
		t.Errorf("injection branch: last user Images = %+v, want [%q]", images, converted)
	}
}

// TestGatewayGenerateImageNormalizesHEICSource pins the generation boundary
// end to end through the Ollama image provider: a HEIC source image on an
// image-to-image call reaches /api/generate as bare base64 of the CONVERTED
// JPEG, never the HEIC bytes Ollama's diffusion models cannot decode.
func TestGatewayGenerateImageNormalizesHEICSource(t *testing.T) {
	stubSipsJPEG(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	var wireImages []string
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/generate" {
			t.Fatalf("unexpected provider path %q", req.URL.Path)
		}
		data, _ := io.ReadAll(req.Body)
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("image request body is not JSON: %v", err)
		}
		for _, image := range payload["images"].([]any) {
			if text, ok := image.(string); ok {
				wireImages = append(wireImages, text)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"model":"image-model","image":"iVBORw0KGgo=","done":true}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	// defaultAppConfig routes generate_image to Ollama (no key needed), and
	// the normalization runs before the provider dispatch in the wrapper.
	gateway := newToolGateway(app, defaultAppConfig())
	if _, _, _, err := gateway.tools.GenerateImage(t.Context(), ImageGenerateRequest{
		Model:  "image-model",
		Prompt: "repaint this photo in watercolor",
		Images: []string{"data:image/heic;base64," + base64.StdEncoding.EncodeToString(heifTestImage("heic"))},
	}); err != nil {
		t.Fatalf("GenerateImage returned error: %v", err)
	}

	want := base64.StdEncoding.EncodeToString(fakeSipsJPEGBytes)
	if len(wireImages) != 1 || wireImages[0] != want {
		t.Fatalf("wire images = %+v, want the converted JPEG as bare base64 [%q]", wireImages, want)
	}
	if decoded, err := base64.StdEncoding.DecodeString(wireImages[0]); err != nil || !bytes.HasPrefix(decoded, []byte{0xff, 0xd8}) {
		t.Fatalf("wire image is not the converted JPEG: %v", err)
	}
}
