package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSchemaFixture copies a testdata fal schema into the schema-cache
// directory the SchemaCache reads from, simulating a warm disk cache. This
// avoids going to the network for capability detection tests. FetchedAt is set
// to now so readFresh treats the entry as within TTL (a zero FetchedAt would be
// seen as expired and trigger a re-fetch).
func writeSchemaFixture(t *testing.T, cacheDir, modelID, fixtureName string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fal-schemas", fixtureName+".json"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := json.Marshal(cachedSchema{FetchedAt: time.Now(), Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cacheDir, sanitizeModelID(modelID)+".json")
	if err := os.WriteFile(path, entry, 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- Fix #1: capability helper ---

// TestVideoModelSupportsAudio_TrueWhenSchemaHasGenerateAudio seeds the disk
// cache with the Veo 3.1 schema (which declares generate_audio) and asserts the
// helper detects the capability. All three resolved video models are pinned to
// the fixture-backed id so the result is deterministic.
func TestVideoModelSupportsAudio_TrueWhenSchemaHasGenerateAudio(t *testing.T) {
	dir := t.TempDir()
	const model = "veo/test"
	writeSchemaFixture(t, filepath.Join(dir, "schema-cache"), model, "veo3.1")

	app := NewApp()
	config := defaultAppConfig()
	config.Storage.Root = dir
	config.Providers.Fal.VideoModel = model
	config.Providers.Fal.VideoImageModel = model
	config.Providers.Fal.VideoExtendModel = model

	if !videoModelSupportsAudio(context.Background(), config, app) {
		t.Fatal("expected videoModelSupportsAudio=true for a model whose schema has generate_audio")
	}
}

// TestVideoModelSupportsAudio_FalseWhenSchemaLacksGenerateAudio sets ALL three
// resolved video models to a fixture-backed id with the Kling image-to-video
// schema (no generate_audio field), so the result is deterministic regardless of
// whether the test machine has a fal key (which would let the helper fetch real
// schemas for the default model ids).
func TestVideoModelSupportsAudio_FalseWhenSchemaLacksGenerateAudio(t *testing.T) {
	dir := t.TempDir()
	const model = "kling/test"
	writeSchemaFixture(t, filepath.Join(dir, "schema-cache"), model, "kling-image-to-video")

	app := NewApp()
	config := defaultAppConfig()
	config.Storage.Root = dir
	// Pin all three resolved video models so the helper never falls back to a
	// default id whose real schema could be fetched over the network.
	config.Providers.Fal.VideoModel = model
	config.Providers.Fal.VideoImageModel = model
	config.Providers.Fal.VideoExtendModel = model

	if videoModelSupportsAudio(context.Background(), config, app) {
		t.Fatal("expected videoModelSupportsAudio=false for a model without generate_audio")
	}
}

// TestVideoModelSupportsAudio_FalseWhenAppNil asserts the helper degrades
// gracefully (returns false, no panic) when no App is available — the path
// tests and the gateway rebuild take.
func TestVideoModelSupportsAudio_FalseWhenAppNil(t *testing.T) {
	config := defaultAppConfig()
	config.Providers.Fal.VideoModel = "anything"
	if videoModelSupportsAudio(context.Background(), config, nil) {
		t.Fatal("expected false with nil app")
	}
}

// TestVideoModelSupportsAudio_FalseWhenSchemaMissing asserts that when no
// schema is available for any configured model (cold cache, unreachable fetch),
// the helper reports false rather than erroring. All three video models are
// pinned to a non-existent id so no real schema can be fetched — this keeps the
// test deterministic even on a machine with a fal key in the keychain.
func TestVideoModelSupportsAudio_FalseWhenSchemaMissing(t *testing.T) {
	dir := t.TempDir()
	app := NewApp()
	config := defaultAppConfig()
	config.Storage.Root = dir
	const ghost = "fal-ai/test-ghost-model-does-not-exist"
	config.Providers.Fal.VideoModel = ghost
	config.Providers.Fal.VideoImageModel = ghost
	config.Providers.Fal.VideoExtendModel = ghost
	if videoModelSupportsAudio(context.Background(), config, app) {
		t.Fatal("expected false when the schema is unavailable for every configured model")
	}
}

// --- Fix #1: capability-aware descriptions ---

func TestVideoGenerationDescription_AudioCapableSteersToSingleCall(t *testing.T) {
	desc := videoGenerationDescription(true)
	if !strings.Contains(desc, "synchronized audio") {
		t.Fatalf("audio-capable description should mention synchronized audio: %q", desc)
	}
	if !strings.Contains(desc, "prefer a single generate_video") {
		t.Fatalf("audio-capable description should steer away from the chain: %q", desc)
	}
}

func TestVideoGenerationDescription_NotCapableOmitsAudioHint(t *testing.T) {
	desc := videoGenerationDescription(false)
	if strings.Contains(desc, "synchronized audio") {
		t.Fatalf("non-audio description must not claim audio capability: %q", desc)
	}
}

func TestSpeechGenerationDescription_ChainHintOnlyWhenVideoLacksAudio(t *testing.T) {
	withHint := speechGenerationDescription(false)
	withoutHint := speechGenerationDescription(true)
	if !strings.Contains(withHint, "carries forward to lip_sync") {
		t.Fatalf("non-audio-video description should explain the chain: %q", withHint)
	}
	if !strings.Contains(withHint, "generate_speech") {
		t.Fatalf("chain hint should name generate_speech: %q", withHint)
	}
	if strings.Contains(withoutHint, "carries forward") {
		t.Fatalf("audio-capable-video description should not mention the chain: %q", withoutHint)
	}
}

func TestSoundEffectsDescriptionHasNoChainHint(t *testing.T) {
	desc := soundEffectsGenerationDescription()
	if strings.Contains(desc, "lip_sync") {
		t.Fatalf("sound description must not suggest the speech-only lip_sync chain: %q", desc)
	}
	if !strings.Contains(desc, "sound effect") || !strings.Contains(desc, "music") {
		t.Fatalf("sound description should cover both music and sound effects: %q", desc)
	}
}

func TestLipsyncDescription_ChainHintOnlyWhenVideoLacksAudio(t *testing.T) {
	withHint := lipsyncDescription(false)
	withoutHint := lipsyncDescription(true)
	if !strings.Contains(withHint, "carries forward automatically") {
		t.Fatalf("non-audio-video lip_sync description should explain generated-audio carry: %q", withHint)
	}
	if strings.Contains(withoutHint, "carries forward") {
		t.Fatalf("audio-capable-video lip_sync description should not mention carry: %q", withoutHint)
	}
}

// TestRegistryVideoAudioCapableReflectsDescription asserts the registry
// predicate reads the capability from the generate_video description (the
// single source of truth), so triage's conditional hint cannot drift from the
// description the planner sees.
func TestRegistryVideoAudioCapableReflectsDescription(t *testing.T) {
	capable := newHarnessToolRegistry([]HarnessToolDefinition{videoGenerationToolDefinition(true)})
	if !capable.VideoAudioCapable() {
		t.Fatal("VideoAudioCapable should be true when generate_video is audio-capable")
	}
	notCapable := newHarnessToolRegistry([]HarnessToolDefinition{videoGenerationToolDefinition(false)})
	if notCapable.VideoAudioCapable() {
		t.Fatal("VideoAudioCapable should be false when generate_video is not audio-capable")
	}
	// Absent generate_video → false, not a crash.
	empty := newHarnessToolRegistry(nil)
	if empty.VideoAudioCapable() {
		t.Fatal("VideoAudioCapable should be false when generate_video is absent")
	}
}

// --- Fix #2: triage routing hint ---

// TestTriageSystemPromptRoutingHintConditional asserts the narration-routing
// hint appears ONLY when the registry's generate_video is audio-capable. The
// hint itself must reference generate_video / lip_sync (the tool names), not a
// specific model id — checked against the hint sentence in isolation since the
// broader tool description legitimately mentions "Veo extend".
func TestTriageSystemPromptRoutingHintConditional(t *testing.T) {
	capable := newHarnessToolRegistry([]HarnessToolDefinition{videoGenerationToolDefinition(true)})
	with := triageSystemPrompt(capable, nil, "/tmp/ws")
	if !strings.Contains(with, "route to generate_video alone") {
		t.Fatalf("triage prompt should include the narration-routing hint when video is audio-capable")
	}
	// The routing hint is the capability-conditional paragraph; assert it names
	// tools, not models. Isolate just that paragraph — the rest of the prompt
	// (the generate_video tool description) legitimately says "Veo extend".
	hintStart := strings.Index(with, "When the user wants speech")
	if hintStart < 0 {
		t.Fatalf("could not locate routing hint in triage prompt")
	}
	hintEnd := strings.Index(with[hintStart:], "Set needsTools true")
	if hintEnd < 0 {
		hintEnd = len(with[hintStart:])
	}
	hint := with[hintStart : hintStart+hintEnd]
	for _, bad := range []string{"seedance", "veo", "kling"} {
		if strings.Contains(strings.ToLower(hint), bad) {
			t.Fatalf("routing hint must not name model %q: %q", bad, hint)
		}
	}

	notCapable := newHarnessToolRegistry([]HarnessToolDefinition{videoGenerationToolDefinition(false)})
	without := triageSystemPrompt(notCapable, nil, "/tmp/ws")
	if strings.Contains(without, "route to generate_video alone") {
		t.Fatalf("triage prompt must omit the routing hint when video is not audio-capable")
	}
}

// TestTriagePromptListsBothAudioTools asserts that with both audio tools
// registered, triage's catalog and its responseMode "audio" guidance name
// generate_speech AND generate_sound — the planner picks between them, so
// triage must surface both. Also pins that the old combined generate_audio name
// is gone from the catalog.
func TestTriagePromptListsBothAudioTools(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{
		speechGenerationToolDefinition(false),
		soundEffectsGenerationToolDefinition(),
	})
	catalog := registry.PromptCatalog()
	for _, want := range []string{"generate_speech", "generate_sound"} {
		if !strings.Contains(catalog, want) {
			t.Fatalf("tool catalog missing %q:\n%s", want, catalog)
		}
	}
	prompt := triageSystemPrompt(registry, nil, "/tmp/ws")
	if !strings.Contains(prompt, "generate_speech or generate_sound tool is listed as available") {
		t.Fatalf("audio-mode guidance should name both audio tools:\n%s", prompt)
	}
	if strings.Contains(prompt, "generate_audio") {
		t.Fatalf("the retired generate_audio tool name must not appear in the triage prompt:\n%s", prompt)
	}
}

// --- Fix #6 (backstop): findNative nil-schema safety ---

func TestFindNativeNilSchemaReturnsFalse(t *testing.T) {
	if _, _, ok := findNative(nil, builtinFalOverrides(), "video", "any", "generateAudio"); ok {
		t.Fatal("findNative must return false on a nil schema, not panic")
	}
	if _, _, ok := findNative(nil, builtinFalOverrides(), "video", "any", "prompt"); ok {
		t.Fatal("findNative must return false on a nil schema for any canonical param")
	}
}

// --- Fix #3: temp-file → data-URL conversion ---

// TestTempAudioFileAsDataURL_RoundTrip writes a real MP3-frame-sync header to a
// temp file and asserts the helper produces a playable data URL the lip_sync /
// transcribe_audio tools accept. This is the conversion that makes generated
// audio consumable by a later tool in the same turn.
func TestTempAudioFileAsDataURL_RoundTrip(t *testing.T) {
	// Minimal MP3 frame sync header (0xFF 0xE0) so isAudioBytes returns true.
	audio := []byte{0xFF, 0xE0, 0x00, 0x00, 'd', 'u', 'm', 'm', 'y'}
	tmp, err := os.CreateTemp("", "atelier-audio-*.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(audio); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	dataURL, err := tempAudioFileAsDataURL(tmp.Name(), "audio/mpeg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(dataURL, "data:audio/mpeg;base64,") {
		t.Fatalf("expected audio data URL, got %q", dataURL)
	}
}

// TestTempAudioFileAsDataURL_EmptyPathReturnsEmpty asserts the helper yields
// ("", nil) for a missing temp path — the swallow-error contract that keeps a
// missing media result from failing the whole tool batch.
func TestTempAudioFileAsDataURL_EmptyPathReturnsEmpty(t *testing.T) {
	dataURL, err := tempAudioFileAsDataURL("", "audio/mpeg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dataURL != "" {
		t.Fatalf("expected empty data URL for empty path, got %q", dataURL)
	}
}

// TestTempAudioFileAsDataURL_NonAudioReturnsEmpty asserts a temp file that
// isn't audio yields ("", nil) rather than a malformed data URL.
func TestTempAudioFileAsDataURL_NonAudioReturnsEmpty(t *testing.T) {
	tmp, err := os.CreateTemp("", "not-audio-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write([]byte("plain text, not audio"))
	tmp.Close()
	dataURL, err := tempAudioFileAsDataURL(tmp.Name(), "audio/mpeg")
	if err != nil || dataURL != "" {
		t.Fatalf("expected (\"\", nil) for non-audio bytes, got (%q, %v)", dataURL, err)
	}
}

// TestTempVideoFileAsDataURL_RoundTrip is the video sibling: an MP4 ftyp box
// header must produce a video data URL.
func TestTempVideoFileAsDataURL_RoundTrip(t *testing.T) {
	// Minimal MP4: "ftyp" box at offset 4 so isVideoBytes returns true.
	video := []byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	tmp, err := os.CreateTemp("", "atelier-video-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(video); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	dataURL, err := tempVideoFileAsDataURL(tmp.Name(), "video/mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(dataURL, "data:video/mp4;base64,") {
		t.Fatalf("expected video data URL, got %q", dataURL)
	}
}

// --- Fix #3: forwardableMediaFromResults (Part A extraction) ---

// TestForwardableMediaFromAudioResult asserts a completed generate_audio result
// contributes a data URL built from its temp file. This is the exact extraction
// that feeds lip_sync in the conv_047accca scenario.
func TestForwardableMediaFromAudioResult(t *testing.T) {
	audio := []byte{0xFF, 0xE0, 0x00, 0x00, 'x'}
	tmp, err := os.CreateTemp("", "atelier-audio-*.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write(audio)
	tmp.Close()

	results := []HarnessToolResult{{
		Name:   "generate_audio",
		Status: "completed",
		Result: ToolAudioResult{
			Model:  "tts",
			Prompt: "hello",
			Audios: []ToolAudioFile{{TempPath: tmp.Name(), MimeType: "audio/mpeg"}},
		},
	}}
	media := forwardableMediaFromResults(results)
	if media == nil {
		t.Fatal("expected forwardable media from a completed audio result")
	}
	if !strings.HasPrefix(media.Audio, "data:audio/") {
		t.Fatalf("expected audio data URL, got %q", media.Audio)
	}
	if media.Video != "" {
		t.Fatalf("expected no video from an audio result, got %q", media.Video)
	}
}

// TestForwardableMediaSkipsFailedResults asserts a failed generate_audio
// contributes no audio — so lip_sync surfaces its usual attachment error
// rather than consuming partial output.
func TestForwardableMediaSkipsFailedResults(t *testing.T) {
	results := []HarnessToolResult{{
		Name:   "generate_audio",
		Status: "failed",
		Error:  "audio generation failed",
	}}
	if media := forwardableMediaFromResults(results); media != nil {
		t.Fatalf("expected nil media from a failed result, got %+v", media)
	}
}

// TestForwardableMediaFromVideoAndImage asserts video and image results map to
// their respective slots (video temp file → data URL; images already data URLs).
func TestForwardableMediaFromVideoAndImage(t *testing.T) {
	video := []byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	tmp, err := os.CreateTemp("", "atelier-video-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write(video)
	tmp.Close()

	results := []HarnessToolResult{
		{
			Name:   "generate_video",
			Status: "completed",
			Result: ToolVideoResult{
				Model:  "veo",
				Videos: []ToolVideoFile{{TempPath: tmp.Name(), MimeType: "video/mp4"}},
			},
		},
		{
			Name:   "generate_image",
			Status: "completed",
			Result: ToolImageResult{
				Model:  "flux",
				Images: []string{"data:image/png;base64,AAAA"},
			},
		},
	}
	media := forwardableMediaFromResults(results)
	if media == nil {
		t.Fatal("expected forwardable media")
	}
	if !strings.HasPrefix(media.Video, "data:video/") {
		t.Fatalf("expected video data URL, got %q", media.Video)
	}
	if len(media.Images) != 1 || media.Images[0] != "data:image/png;base64,AAAA" {
		t.Fatalf("expected the image data URL, got %v", media.Images)
	}
}

// TestForwardableMediaNewestFirstShadows asserts a later result shadows an
// earlier one within the same batch, matching latestAttached*ForTurn's
// newest-first precedence.
func TestForwardableMediaNewestFirstShadows(t *testing.T) {
	a1 := []byte{0xFF, 0xE0, 0x01}
	a2 := []byte{0xFF, 0xE0, 0x02}
	t1, _ := os.CreateTemp("", "a1-*.mp3")
	defer os.Remove(t1.Name())
	t1.Write(a1)
	t1.Close()
	t2, _ := os.CreateTemp("", "a2-*.mp3")
	defer os.Remove(t2.Name())
	t2.Write(a2)
	t2.Close()

	results := []HarnessToolResult{
		{Name: "generate_audio", Status: "completed", Result: ToolAudioResult{
			Audios: []ToolAudioFile{{TempPath: t1.Name(), MimeType: "audio/mpeg"}}}},
		{Name: "generate_audio", Status: "completed", Result: ToolAudioResult{
			Audios: []ToolAudioFile{{TempPath: t2.Name(), MimeType: "audio/mpeg"}}}},
	}
	media := forwardableMediaFromResults(results)
	if media == nil {
		t.Fatal("expected media")
	}
	// The newest (second) result's audio should win. Assert the data URL carries
	// a2's base64, not a1's.
	wantSuffix := base64.StdEncoding.EncodeToString(a2)
	if !strings.HasSuffix(media.Audio, wantSuffix) {
		t.Fatalf("expected newest audio (a2) to win, got %q", media.Audio)
	}
}

// --- Fix #3 integration: the generate_speech → lip_sync composition ---
//
// This is the headline regression test for conv_047accca33610598408b8cf8: a
// planner requests generate_speech then lip_sync in the same round, and lip_sync
// must see the audio the first call produced. It builds a ToolGateway with fake
// tool functions (same package, so unexported fields are reachable) and applies
// the same per-result forward-feed runHarnessToolCalls does, so the composition
// itself is exercised end to end without a keychain or network.

// TestGenerateSpeechFeedsLipSyncInSameBatch verifies the conv_047accca scenario:
// generate_speech produces audio, then lip_sync consumes it without the
// "requires an attached audio clip" error that previously failed the turn.
func TestGenerateSpeechFeedsLipSyncInSameBatch(t *testing.T) {
	// Fake audio bytes the generate_speech tool will "produce".
	audioBytes := []byte{0xFF, 0xE0, 0x00, 0x00, 'n', 'a', 'r', 'r', 'a', 't', 'e'}
	audioTmp, err := os.CreateTemp("", "atelier-audio-*.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(audioTmp.Name())
	if _, err := audioTmp.Write(audioBytes); err != nil {
		t.Fatal(err)
	}
	audioTmp.Close()

	// Capture what AttachedAudio lip_sync actually saw.
	var lipsyncSawAudio string
	var lipsyncSawFace string

	registry := newHarnessToolRegistry([]HarnessToolDefinition{
		speechGenerationToolDefinition(false),
		lipsyncToolDefinition(false),
	})
	tools := HarnessToolExecutionContext{
		Config: defaultAppConfig(),
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			return GeneratedAudio{Data: audioBytes, MimeType: "audio/mpeg"}, nil
		},
		GenerateLipsync: func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error) {
			lipsyncSawAudio = req.Audio
			lipsyncSawFace = req.Image
			// Minimal MP4 ftyp so the result's writeTempVideo succeeds downstream.
			return GeneratedVideo{Data: []byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, MimeType: "video/mp4"}, nil
		},
	}
	// The face image the user attached this turn — lip_sync should receive it.
	const userFace = "data:image/png;base64,FACE"
	tools.AttachedImages = []string{userFace}

	gateway := ToolGateway{registry: registry, tools: tools}

	// Mirror runHarnessToolCalls's per-call loop + forward-feed.
	calls := []HarnessToolCall{
		{Name: "generate_speech", Content: "narration"},
		{Name: "lip_sync"},
	}
	var results []HarnessToolResult
	for _, call := range calls {
		result := gateway.Execute(context.Background(), ToolExecutionRequest{Name: call.Name, Call: call})
		results = append(results, result)
		if generated := forwardableMediaFromResults(results); generated != nil {
			if strings.TrimSpace(gateway.tools.AttachedAudio) == "" && generated.Audio != "" {
				gateway.tools.AttachedAudio = generated.Audio
			}
			if len(gateway.tools.AttachedImages) == 0 && len(generated.Images) > 0 {
				gateway.tools.AttachedImages = generated.Images
			}
		}
	}

	// generate_speech must have completed.
	if len(results) < 2 || results[0].Status != "completed" {
		t.Fatalf("generate_speech should complete first, got %+v", results)
	}
	// lip_sync must NOT have failed for a missing audio clip — the original bug.
	if results[1].Status != "completed" {
		t.Fatalf("lip_sync should complete after consuming generated audio, got status=%q error=%q",
			results[1].Status, results[1].Error)
	}
	// And it must have seen the generated audio (carried forward) plus the face.
	if !strings.HasPrefix(lipsyncSawAudio, "data:audio/") {
		t.Fatalf("lip_sync should have received the generated audio data URL, got %q", lipsyncSawAudio)
	}
	if lipsyncSawFace != userFace {
		t.Fatalf("lip_sync should have received the user's attached face, got %q", lipsyncSawFace)
	}
}

// TestUserAttachedAudioNotOverwritten asserts the forward-feed precedence rule:
// a user-attached audio clip on the turn is never shadowed by generated audio.
// This protects "the user explicitly attached this clip" intent.
func TestUserAttachedAudioNotOverwritten(t *testing.T) {
	audioBytes := []byte{0xFF, 0xE0, 0x00, 0x00, 'g', 'e', 'n'}
	audioTmp, _ := os.CreateTemp("", "atelier-audio-*.mp3")
	defer os.Remove(audioTmp.Name())
	audioTmp.Write(audioBytes)
	audioTmp.Close()

	var lipsyncSawAudio string
	registry := newHarnessToolRegistry([]HarnessToolDefinition{
		speechGenerationToolDefinition(false),
		lipsyncToolDefinition(false),
	})
	const userAudio = "data:audio/mpeg;base64,USERCLIP"
	tools := HarnessToolExecutionContext{
		Config:        defaultAppConfig(),
		AttachedAudio: userAudio,
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			return GeneratedAudio{Data: audioBytes, MimeType: "audio/mpeg"}, nil
		},
		GenerateLipsync: func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error) {
			lipsyncSawAudio = req.Audio
			return GeneratedVideo{Data: []byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, MimeType: "video/mp4"}, nil
		},
	}
	tools.AttachedImages = []string{"data:image/png;base64,FACE"}
	gateway := ToolGateway{registry: registry, tools: tools}

	calls := []HarnessToolCall{{Name: "generate_speech", Content: "x"}, {Name: "lip_sync"}}
	var results []HarnessToolResult
	for _, call := range calls {
		result := gateway.Execute(context.Background(), ToolExecutionRequest{Name: call.Name, Call: call})
		results = append(results, result)
		// Apply the SAME precedence rule runHarnessToolCalls uses: only fill empty.
		if generated := forwardableMediaFromResults(results); generated != nil {
			if strings.TrimSpace(gateway.tools.AttachedAudio) == "" && generated.Audio != "" {
				gateway.tools.AttachedAudio = generated.Audio
			}
		}
	}
	if lipsyncSawAudio != userAudio {
		t.Fatalf("user-attached audio must win over generated; lip_sync saw %q", lipsyncSawAudio)
	}
}

// TestGeneratedAudioNeverBecomesVoiceReference pins the cloning provenance
// invariant: the forward-feed that hands generated audio to lip_sync must never
// turn that audio into a voice-cloning reference. A second generate_speech in
// the same batch (or a later round) still runs on the plain speech model with
// no reference — otherwise "make a whoosh, then say hello" would clone speech
// from the sound effect.
func TestGeneratedAudioNeverBecomesVoiceReference(t *testing.T) {
	audioBytes := []byte{0xFF, 0xE0, 0x00, 0x00, 'g', 'e', 'n'}
	audioTmp, _ := os.CreateTemp("", "atelier-audio-*.mp3")
	defer os.Remove(audioTmp.Name())
	audioTmp.Write(audioBytes)
	audioTmp.Close()

	config := defaultAppConfig()
	config.Providers.Fal.AudioModel = "fal-ai/speech/model"
	config.Providers.Fal.AudioCloneModel = "fal-ai/clone/model"

	var saw []AudioGenerateRequest
	registry := newHarnessToolRegistry([]HarnessToolDefinition{speechGenerationToolDefinition(false)})
	tools := HarnessToolExecutionContext{
		Config: config,
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			saw = append(saw, req)
			return GeneratedAudio{Data: audioBytes, MimeType: "audio/mpeg"}, nil
		},
	}
	gateway := ToolGateway{registry: registry, tools: tools}

	// Mirror runHarnessToolCalls's per-call loop + forward-feed: generated audio
	// fills the empty AttachedAudio slot but VoiceReference is never written —
	// exactly the real loop, which copies VoiceReference from the turn once and
	// leaves it alone (see runHarnessToolCalls / the round backfill).
	calls := []HarnessToolCall{{Name: "generate_speech", Content: "a"}, {Name: "generate_speech", Content: "b"}}
	var results []HarnessToolResult
	for _, call := range calls {
		result := gateway.Execute(context.Background(), ToolExecutionRequest{Name: call.Name, Call: call})
		results = append(results, result)
		if generated := forwardableMediaFromResults(results); generated != nil {
			if strings.TrimSpace(gateway.tools.AttachedAudio) == "" && generated.Audio != "" {
				gateway.tools.AttachedAudio = generated.Audio
			}
		}
	}
	if len(saw) != 2 {
		t.Fatalf("expected two generate_speech calls, got %d", len(saw))
	}
	for i, req := range saw {
		if req.Model != "fal-ai/speech/model" {
			t.Fatalf("call %d model = %q, want the plain speech model — generated audio must not select the clone path", i, req.Model)
		}
		if req.SourceAudio != "" {
			t.Fatalf("call %d sourceAudio = %q, want empty — generated audio must not become a voice reference", i, req.SourceAudio)
		}
	}
}

// TestUserAttachedClipSelectsCloningPath is the positive counterpart: when the
// user attached a clip (VoiceReference pinned at turn start), generate_speech
// clones from that clip — and keeps cloning from it even after generated audio
// fills the AttachedAudio carry-forward slot, so the reference never drifts.
func TestUserAttachedClipSelectsCloningPath(t *testing.T) {
	audioBytes := []byte{0xFF, 0xE0, 0x00, 0x00, 'g', 'e', 'n'}
	audioTmp, _ := os.CreateTemp("", "atelier-audio-*.mp3")
	defer os.Remove(audioTmp.Name())
	audioTmp.Write(audioBytes)
	audioTmp.Close()

	config := defaultAppConfig()
	config.Providers.Fal.AudioModel = "fal-ai/speech/model"
	config.Providers.Fal.AudioCloneModel = "fal-ai/clone/model"

	const userClip = "data:audio/mpeg;base64,USERCLIP"
	var saw []AudioGenerateRequest
	registry := newHarnessToolRegistry([]HarnessToolDefinition{speechGenerationToolDefinition(false)})
	tools := HarnessToolExecutionContext{
		Config:         config,
		AttachedAudio:  userClip,
		VoiceReference: userClip,
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			saw = append(saw, req)
			return GeneratedAudio{Data: audioBytes, MimeType: "audio/mpeg"}, nil
		},
	}
	gateway := ToolGateway{registry: registry, tools: tools}

	calls := []HarnessToolCall{{Name: "generate_speech", Content: "a"}, {Name: "generate_speech", Content: "b"}}
	var results []HarnessToolResult
	for _, call := range calls {
		result := gateway.Execute(context.Background(), ToolExecutionRequest{Name: call.Name, Call: call})
		results = append(results, result)
		if generated := forwardableMediaFromResults(results); generated != nil {
			if strings.TrimSpace(gateway.tools.AttachedAudio) == "" && generated.Audio != "" {
				gateway.tools.AttachedAudio = generated.Audio
			}
		}
	}
	if len(saw) != 2 {
		t.Fatalf("expected two generate_speech calls, got %d", len(saw))
	}
	for i, req := range saw {
		if req.Model != "fal-ai/clone/model" {
			t.Fatalf("call %d model = %q, want the clone model", i, req.Model)
		}
		if req.SourceAudio != userClip {
			t.Fatalf("call %d sourceAudio = %q, want the user's clip — the reference must not drift to generated audio", i, req.SourceAudio)
		}
	}
}
