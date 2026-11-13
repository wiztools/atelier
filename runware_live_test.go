package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"image"
	"image/png"
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

	// A large source (~12 MB noise PNG) must upload through mediaStorage and
	// be deleted afterwards. FLUX dev at 512px keeps this step ~$0.0013.
	large := "data:image/png;base64," + base64.StdEncoding.EncodeToString(runwareLiveNoisePNG(t, 2048, 2048))
	start := time.Now()
	resp, _, _, err := gateway.tools.GenerateImage(ctx, ImageGenerateRequest{
		Model: "runware:101@1", Prompt: "abstract colorful painting", Width: 512, Height: 512, Images: []string{large},
	})
	if err != nil {
		t.Fatalf("large-source edit: %v", err)
	}
	var mediaUUID string
	for _, d := range resp.Diagnostics {
		if i := strings.Index(d, "uploaded as media "); i >= 0 {
			mediaUUID = strings.TrimSpace(d[i+len("uploaded as media "):])
		}
	}
	if mediaUUID == "" || len(resp.Images) != 1 {
		t.Fatalf("large-source edit did not upload: images=%d diagnostics=%v", len(resp.Images), resp.Diagnostics)
	}
	t.Logf("large-source edit: %d MB source uploaded as %s, %s, $%.6f", len(large)*3/4>>20, mediaUUID, time.Since(start).Round(time.Millisecond), float64(resp.CostMicros)/1e6)
	// The cleanup must have deleted it: referencing it again has to fail.
	_, err = newRunwareClient(app.client, key).GenerateImage(ctx, map[string]any{
		"taskType": "imageInference", "taskUUID": runwareTaskUUID(), "model": "runware:101@1",
		"positivePrompt": "abstract painting", "width": 512, "height": 512, "outputType": "dataURI", "includeCost": true,
		"inputs": map[string]any{"seedImage": mediaUUID},
	})
	if err == nil {
		t.Fatalf("uploaded media %s still usable after cleanup", mediaUUID)
	}
	// Only an error about the image input proves the delete; any other
	// rejection (a bad prompt, say) would pass this check vacuously.
	if msg := strings.ToLower(err.Error()); !strings.Contains(msg, "seedimage") && !strings.Contains(msg, "image") && !strings.Contains(msg, "media") {
		t.Fatalf("reference to deleted media failed for an unrelated reason: %v", err)
	}
	t.Logf("deleted upload rejected as expected: %v", err)
}

// TestLiveRunwareMediaFamilies runs one gated live call per new media family
// against the real API — speech, sound, inpaint, and a short text-to-video
// — through the same gateway paths chat uses, asserting honest artifacts:
// audio/image/video bytes that sniff as their kind, the reported cost, and
// the poll machinery for video (async submit → getResponse → download).
// It spends real (small) money, around $0.10 total.
//
//	ATELIER_LIVE_RUNWARE=1 go test -run TestLiveRunwareMediaFamilies -v .
func TestLiveRunwareMediaFamilies(t *testing.T) {
	if os.Getenv("ATELIER_LIVE_RUNWARE") == "" {
		t.Skip("set ATELIER_LIVE_RUNWARE=1 to run against the live Runware API with the keychain key")
	}
	key, err := loadRunwareAPIKey()
	if err != nil || key == "" {
		t.Fatalf("no Runware key in the keychain (err=%v)", err)
	}
	app := NewApp()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Speech: the MiniMax default speaks one short line; the reported cost
	// must ride and the bytes must sniff as audio.
	config := mergeAppConfig(AppConfig{
		Models: ConfigModels{
			ImageProvider: "runware", VideoProvider: "runware", InpaintProvider: "runware",
			SpeechProvider: "runware", AudioProvider: "runware", TranscriptionProvider: "runware",
		},
		Storage: ConfigStorage{Root: t.TempDir()},
	})
	if !speechGenerationConfigured(config) {
		t.Fatal("generate_speech not offered under the runware provider")
	}
	gateway := newToolGateway(app, config)
	speech, err := gateway.tools.GenerateAudio(ctx, AudioGenerateRequest{
		Model: defaultRunwareSpeechModel, Kind: audioKindSpeech,
		Prompt: "Runware media backends are live.", Voice: "Japanese_ColdQueen",
	})
	if err != nil {
		t.Fatalf("GenerateAudio: %v", err)
	}
	if len(speech.Data) == 0 || !isAudioBytes(speech.Data) {
		t.Fatalf("speech bytes do not sniff as audio (%d bytes)", len(speech.Data))
	}
	if speech.CostMicros <= 0 {
		t.Fatal("speech must report its cost")
	}
	t.Logf("speech: %d bytes, $%.6f", len(speech.Data), float64(speech.CostMicros)/1e6)

	// Transcription (self-contained): the just-generated speech clip goes
	// through the same gateway path chat uses — unconditional mediaStorage
	// upload (inputs.audios takes no inline bytes), the textInference task,
	// and the plain text back. The spoken words must survive the round trip.
	if !transcribeAudioConfigured(config) {
		t.Fatal("transcribe_audio not offered under the runware transcription provider")
	}
	transcript, err := gateway.tools.TranscribeAudio(ctx, TranscribeAudioRequest{
		Model: defaultRunwareTranscribeModel, Audio: "data:audio/mpeg;base64," + base64.StdEncoding.EncodeToString(speech.Data),
		Timestamps: "segments",
	})
	if err != nil {
		t.Fatalf("TranscribeAudio: %v", err)
	}
	lower := strings.ToLower(transcript.Text)
	if !strings.Contains(lower, "runware") || !strings.Contains(lower, "live") {
		t.Fatalf("the round-trip transcript lost the spoken words: %q (notices=%v)", transcript.Text, transcript.Notices)
	}
	if transcript.Timestamps != "" {
		t.Fatal("the runware backend must degrade a timestamps request to plain text")
	}
	degraded := false
	for _, notice := range transcript.Notices {
		if strings.Contains(notice, "no cue track") {
			degraded = true
		}
	}
	if !degraded {
		t.Fatalf("the timestamps request must carry the degrade notice, got %v", transcript.Notices)
	}
	if transcript.CostMicros <= 0 {
		t.Fatal("transcription must report its cost")
	}
	t.Logf("transcript: %q, $%.6f, notices=%v", transcript.Text, float64(transcript.CostMicros)/1e6, transcript.Notices)

	// Sound: mirelo text-to-audio, clamped into its 1–10s window.
	sound, err := gateway.tools.GenerateAudio(ctx, AudioGenerateRequest{
		Model: defaultRunwareSoundEffectsModel, Kind: audioKindSound,
		Prompt: "rain on a tin roof", Duration: "30",
	})
	if err != nil {
		t.Fatalf("GenerateAudio sound: %v", err)
	}
	if len(sound.Data) == 0 || !isAudioBytes(sound.Data) {
		t.Fatalf("sound bytes do not sniff as audio (%d bytes)", len(sound.Data))
	}
	clamped := false
	for _, notice := range sound.Notices {
		if strings.Contains(notice, "instead of the requested 30") {
			clamped = true
		}
	}
	if !clamped {
		t.Fatalf("the 30s request must carry the clamp notice, got %v", sound.Notices)
	}
	t.Logf("sound: %d bytes, $%.6f, notices=%v", len(sound.Data), float64(sound.CostMicros)/1e6, sound.Notices)

	// Inpaint: a small Kontext image, masked at its center, through the same
	// executeInpaintProvider path the editor drives (run inline like images).
	base, _, _, err := gateway.tools.GenerateImage(ctx, ImageGenerateRequest{
		Model: defaultRunwareImageModel, Prompt: "a plain white wall, photograph", Width: 1248, Height: 832,
	})
	if err != nil {
		t.Fatalf("inpaint source: %v", err)
	}
	mask := runwareLiveMaskPNG(t, 1248, 832)
	inpaintConfig := config
	inpaintConfig.Models.InpaintProvider = "runware"
	sourceURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(mustDecodeDataURL(t, base.Image))
	resultData, costMicros, _, inpaintNotices, err := app.executeInpaintProvider(ctx, inpaintConfig, EditOperation{
		ID: "live-inpaint", Kind: editOperationKindInpaint, Provider: "runware", Model: defaultRunwareInpaintModel,
		Inpaint: &InpaintOperationParams{Prompt: "a wall of bookshelves", MaskWidth: 1248, MaskHeight: 832},
	}, mustDecodeDataURL(t, sourceURL), mask)
	if err != nil {
		t.Fatalf("inpaint: %v (notices=%v)", err, inpaintNotices)
	}
	if !isImageBytes(resultData) {
		t.Fatal("inpaint result does not sniff as an image")
	}
	t.Logf("inpaint: %d bytes, $%.6f", len(resultData), float64(costMicros)/1e6)

	// Video: one short Kling t2v clip through the runware gateway branch —
	// the async submit, the getResponse poll loop, the download, and the
	// reported cost all ride this one call.
	if !videoGenerationConfigured(config) {
		t.Fatal("generate_video not offered under the runware provider")
	}
	start := time.Now()
	video, err := gateway.tools.GenerateVideo(ctx, VideoGenerateRequest{
		Model: defaultRunwareVideoModel, Prompt: "a red fox trotting through fresh snow",
		Duration: "5", AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if !isVideoBytes(video.Data) {
		t.Fatalf("video bytes do not sniff as a video (%d bytes)", len(video.Data))
	}
	if video.CostMicros <= 0 {
		t.Fatal("video must report its cost")
	}
	if _, ok := mediasniff.MP4Duration(video.Data); !ok {
		t.Fatal("the rendered clip's MP4 duration must be measurable")
	}
	t.Logf("video: %d bytes, $%.6f, %s, notices=%v", len(video.Data), float64(video.CostMicros)/1e6, time.Since(start).Round(time.Second), video.Notices)
	if out := os.Getenv("ATELIER_LIVE_RUNWARE_OUT"); out != "" {
		_ = os.WriteFile(filepath.Join(out, "live-speech.mp3"), speech.Data, 0o644)
		_ = os.WriteFile(filepath.Join(out, "live-sound.mp3"), sound.Data, 0o644)
		_ = os.WriteFile(filepath.Join(out, "live-inpaint.png"), resultData, 0o644)
		_ = os.WriteFile(filepath.Join(out, "live-video.mp4"), video.Data, 0o644)
	}
}

// runwareLiveMaskPNG renders the canonical polarity mask: a white center
// rectangle (the area to edit) on black (preserved).
func runwareLiveMaskPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			if x > w/4 && x < 3*w/4 && y > h/4 && y < 3*h/4 {
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 255, 255, 255, 255
			}
		}
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustDecodeDataURL(t *testing.T, dataURL string) []byte {
	t.Helper()
	data, _, err := decodeMediaDataURL(dataURL)
	if err != nil {
		t.Fatalf("decode data URL: %v", err)
	}
	return data
}

// runwareLiveNoisePNG encodes random RGB noise, which PNG cannot compress, so
// the payload is reliably past runwareUploadThreshold.
func runwareLiveNoisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if _, err := rand.Read(img.Pix); err != nil {
		t.Fatal(err)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
