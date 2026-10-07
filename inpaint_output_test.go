package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestInpaintOperationKeepsOutsideMaskModelChanges(t *testing.T) {
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	config := editTestHome(t)
	sourceID, _, _ := writeEditSessionFixture(t, config, "conv_inpaint_outside_parent", "conv_inpaint_outside")
	sourceData := editTestFixturePNG(t, 8, 8, color.RGBA{G: 100, A: 255})
	maskData := inpaintOutputMaskPNG(t, 8, 8)
	modelOutput := inpaintOutputPNG(t, 8, 8, func(img *image.RGBA) {
		fillImage(img, color.RGBA{G: 100, A: 255})
		img.Set(0, 0, color.RGBA{B: 255, A: 255})
		img.Set(7, 7, color.RGBA{R: 255, A: 255})
	})

	app := inpaintOutputMockApp(t, "fal-ai/qwen-image-edit/inpaint", modelOutput)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	app.executeImageEditOperation(ctx, cancel, config, "conv_inpaint_outside", "turn_000003", EditOperation{
		ID:              "editop_outside",
		Kind:            editOperationKindInpaint,
		Status:          editOperationStatusQueued,
		CreatedAt:       "2026-10-07T01:00:00Z",
		InputArtifactID: sourceID,
		Provider:        "fal",
		Model:           "fal-ai/qwen-image-edit/inpaint",
		Inpaint: &InpaintOperationParams{
			Prompt:    "replace the selected pixels",
			MaskWidth: 8, MaskHeight: 8,
		},
	}, sourceData, maskData)

	op := waitForEditTerminal(t, config.Storage, "conv_inpaint_outside", "editop_outside")
	if op.Status != editOperationStatusCompleted {
		t.Fatalf("inpaint status = %s: %s", op.Status, op.Error)
	}
	if op.ResultWidth != 8 || op.ResultHeight != 8 {
		t.Fatalf("result dimensions = %dx%d, want provider dimensions 8x8", op.ResultWidth, op.ResultHeight)
	}
	if !containsNotice(op.Notices, "The model changed pixels outside the selection. These changes were kept in the result.") {
		t.Fatalf("outside-mask notice missing: %v", op.Notices)
	}
	if joined := strings.Join(op.Notices, "\n"); strings.Contains(joined, "discard") || strings.Contains(joined, "inverted polarity") {
		t.Fatalf("notice should be neutral and accepting, got %v", op.Notices)
	}
	if got := readEditResultBytes(t, config.Storage, "conv_inpaint_outside", op); !bytes.Equal(got, modelOutput) {
		t.Fatal("saved result bytes differ from provider output")
	}
}

func TestInpaintOperationKeepsProviderDimensionsAndBytes(t *testing.T) {
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	config := editTestHome(t)
	sourceID, _, _ := writeEditSessionFixture(t, config, "conv_inpaint_size_parent", "conv_inpaint_size")
	sourceData := editTestFixturePNG(t, 8, 8, color.RGBA{G: 100, A: 255})
	maskData := inpaintOutputMaskPNG(t, 8, 8)
	modelOutput := inpaintOutputPNG(t, 4, 4, func(img *image.RGBA) {
		fillImage(img, color.RGBA{R: 220, A: 255})
	})

	app := inpaintOutputMockApp(t, "fal-ai/qwen-image-edit/inpaint", modelOutput)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	app.executeImageEditOperation(ctx, cancel, config, "conv_inpaint_size", "turn_000003", EditOperation{
		ID:              "editop_size",
		Kind:            editOperationKindInpaint,
		Status:          editOperationStatusQueued,
		CreatedAt:       "2026-10-07T01:05:00Z",
		InputArtifactID: sourceID,
		Provider:        "fal",
		Model:           "fal-ai/qwen-image-edit/inpaint",
		Inpaint: &InpaintOperationParams{
			Prompt:    "replace the selected pixels",
			MaskWidth: 8, MaskHeight: 8,
		},
	}, sourceData, maskData)

	op := waitForEditTerminal(t, config.Storage, "conv_inpaint_size", "editop_size")
	if op.Status != editOperationStatusCompleted {
		t.Fatalf("inpaint status = %s: %s", op.Status, op.Error)
	}
	if op.ResultWidth != 4 || op.ResultHeight != 4 {
		t.Fatalf("result dimensions = %dx%d, want provider dimensions 4x4", op.ResultWidth, op.ResultHeight)
	}
	if !containsNotice(op.Notices, "The model returned 4x4 for the 8x8 source. The result keeps the model's original dimensions.") {
		t.Fatalf("dimension notice missing: %v", op.Notices)
	}
	if !containsNotice(op.Notices, "The model output differs outside the selection after aligning dimensions for comparison; resizing may contribute to these differences. The full model output was kept.") {
		t.Fatalf("resized outside-mask notice missing: %v", op.Notices)
	}
	if got := readEditResultBytes(t, config.Storage, "conv_inpaint_size", op); !bytes.Equal(got, modelOutput) {
		t.Fatal("saved resized-result bytes differ from provider output")
	}
}

func inpaintOutputMockApp(t *testing.T, model string, output []byte) *App {
	t.Helper()
	app := NewApp()
	app.client.Transport = falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1/models/pricing":
			return jsonResp(`{"prices":[]}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/api/openapi/queue/openapi.json":
			return jsonResp(`{"components":{"schemas":{"QwenInpaintInput":{"type":"object","properties":{"prompt":{"type":"string"},"image_url":{"type":"string"},"mask_url":{"type":"string"},"image_size":{"type":"object","properties":{"width":{"type":"integer"},"height":{"type":"integer"}}},"output_format":{"type":"string","enum":["png","jpeg"]}}}}}}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/"+model:
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode fal submit body: %v", err)
			}
			if body["prompt"] == "" || body["image_url"] == "" || body["mask_url"] == "" {
				t.Fatalf("submit body missing inpaint fields: %v", body)
			}
			return jsonResp(`{"request_id":"req-inpaint-1"}`), nil
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/status"):
			return jsonResp(`{"status":"COMPLETED"}`), nil
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/requests/req-inpaint-1"):
			return jsonResp(`{"images":[{"url":"https://falcdn.example/inpaint-output.png"}]}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/inpaint-output.png":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewReader(output)),
				Header:     http.Header{"Content-Type": []string{"image/png"}},
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	})
	return app
}

func inpaintOutputPNG(t *testing.T, width, height int, draw func(*image.RGBA)) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw(img)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode output: %v", err)
	}
	return buf.Bytes()
}

func inpaintOutputMaskPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	maskURL := editTestMaskDataURL(t, width, height)
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(maskURL, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("decode mask: %v", err)
	}
	return data
}

func fillImage(img *image.RGBA, c color.Color) {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			img.Set(x, y, c)
		}
	}
}

func containsNotice(notices []string, want string) bool {
	for _, notice := range notices {
		if notice == want {
			return true
		}
	}
	return false
}

func readEditResultBytes(t *testing.T, storage ConfigStorage, conversationID string, op EditOperation) []byte {
	t.Helper()
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		t.Fatalf("findConversationPath: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), filepath.FromSlash(op.ResultPath)))
	if err != nil {
		t.Fatalf("read result artifact: %v", err)
	}
	return data
}
