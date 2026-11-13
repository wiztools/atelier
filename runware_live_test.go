package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atelier/internal/mediasniff"
)

// TestLiveRunwareEndToEnd runs generate_image through the real tool gateway
// against Runware with the keychain's key — the gated live check, like
// TestLiveOllamaSpeechEndToEnd. It spends real (small) money: a FLUX.1
// Kontext [dev] generation and instruction edit, and one Seedream 5.5
// reference edit, around $0.05.
// ATELIER_LIVE_RUNWARE_OUT names a directory to keep the images in.
//
//	ATELIER_LIVE_RUNWARE=1 go test -run TestLiveRunwareEndToEnd -v .
func TestLiveRunwareEndToEnd(t *testing.T) {
	if os.Getenv("ATELIER_LIVE_RUNWARE") == "" {
		t.Skip("set ATELIER_LIVE_RUNWARE=1 to run against the live Runware API with the keychain key")
	}
	key, err := loadRunwareAPIKey()
	if err != nil || key == "" {
		t.Fatalf("no Runware key in the keychain (err=%v)", err)
	}
	app := NewApp()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := newRunwareClient(app.client, key).VerifyKey(ctx); err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	models, err := app.ListRunwareImageModels()
	if err != nil || len(models) == 0 {
		t.Fatalf("ListRunwareImageModels: %d models, err=%v", len(models), err)
	}
	t.Logf("catalog: %d image models", len(models))

	config := mergeAppConfig(AppConfig{
		Models:  ConfigModels{ImageProvider: "runware"},
		Storage: ConfigStorage{Root: t.TempDir()},
	})
	if !imageGenerationConfigured(config) {
		t.Fatal("generate_image not offered under the runware provider")
	}
	gateway := newToolGateway(app, config)
	outDir := os.Getenv("ATELIER_LIVE_RUNWARE_OUT")

	generate := func(name string, req ImageGenerateRequest, wantW, wantH int) string {
		t.Helper()
		start := time.Now()
		resp, _, notices, err := gateway.tools.GenerateImage(ctx, req)
		if err != nil {
			t.Fatalf("%s: GenerateImage: %v", name, err)
		}
		if len(resp.Images) != 1 || !strings.HasPrefix(resp.Image, "data:image/") {
			t.Fatalf("%s: images = %d, image prefix %.30q", name, len(resp.Images), resp.Image)
		}
		comma := strings.IndexByte(resp.Image, ',')
		data, err := base64.StdEncoding.DecodeString(resp.Image[comma+1:])
		if err != nil {
			t.Fatalf("%s: undecodable image: %v", name, err)
		}
		w, h, ok := mediasniff.ImagePixelDimensions(data)
		if !ok || (wantW > 0 && (int(w) != wantW || int(h) != wantH)) {
			t.Fatalf("%s: dimensions %.0fx%.0f ok=%v, want %dx%d", name, w, h, ok, wantW, wantH)
		}
		if resp.CostMicros <= 0 {
			t.Fatalf("%s: no reported cost", name)
		}
		t.Logf("%s: %.0fx%.0f, %d bytes, $%.6f, %s, notices=%v", name, w, h, len(data), float64(resp.CostMicros)/1e6, time.Since(start).Round(time.Millisecond), notices)
		if outDir != "" {
			_ = os.WriteFile(filepath.Join(outDir, name+".png"), data, 0o644)
		}
		return resp.Image
	}

	// The default (FLUX.1 Kontext dev) only renders its preset sizes: a 3:2
	// request lands on the 1248x832 preset.
	base := generate("kontext-text-to-image", ImageGenerateRequest{
		Model: defaultRunwareImageModel, Prompt: "a red fox sitting in fresh snow, golden hour, photograph",
		Width: 1536, Height: 1024,
	}, 1248, 832)
	generate("kontext-instruction-edit", ImageGenerateRequest{
		Model: defaultRunwareImageModel, Prompt: "turn this photo into a loose watercolor painting, keep the fox's pose",
		Width: 1536, Height: 1024, Images: []string{base},
	}, 1248, 832)
	generate("seedream-reference-edit", ImageGenerateRequest{
		Model: "bytedance:seedream@5.5", Prompt: "turn this fox photo into a 16:9 cinematic winter scene",
		Width: 1344, Height: 768, Resolution: "1k", Images: []string{base},
	}, 0, 0)
}
