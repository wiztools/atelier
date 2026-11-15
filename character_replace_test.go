package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// characterFixtureSchema parses a captured fal schema for one of the verified
// character-replacement endpoints (testdata/fal-schemas, fetched from fal's
// queue OpenAPI 2026-11-15).
func characterFixtureSchema(t *testing.T, name string) *ModelInputSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fal-schemas", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := parseModelInputSchema(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return schema
}

func characters(groups ...[]string) []CharacterReference {
	out := make([]CharacterReference, 0, len(groups))
	for _, g := range groups {
		out = append(out, CharacterReference{Images: g})
	}
	return out
}

func joinedNotices(notices []string) string { return strings.Join(notices, "\n") }

// TestResolveCharacterReplaceBodyRecast pins the references-array shape: one
// frontal photo per person, extra angles dropped with a notice, the enum
// resolution respelled, the optional prompt sent, and no soundtrack toggle
// (Recast keeps the source audio by design).
func TestResolveCharacterReplaceBodyRecast(t *testing.T) {
	schema := characterFixtureSchema(t, "minimax-h3-max-recast")
	body, notices, _, err := resolveCharacterReplaceBody(schema, VideoCharacterReplaceRequest{
		Model:      "minimax/h3-max/recast",
		Video:      "https://fal.example/source.mp4",
		Characters: characters([]string{"https://fal.example/a-front.png", "https://fal.example/a-side.png"}, []string{"https://fal.example/b.png"}),
		Prompt:     "the woman becomes A",
		Resolution: "1080p",
	}, builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	refs, _ := body["reference_image_urls"].([]any)
	if len(refs) != 2 || refs[0] != "https://fal.example/a-front.png" || refs[1] != "https://fal.example/b.png" {
		t.Errorf("reference_image_urls = %v, want one frontal per character in order", body["reference_image_urls"])
	}
	if body["video_url"] != "https://fal.example/source.mp4" || body["prompt"] != "the woman becomes A" {
		t.Errorf("body = %v, want the video and prompt forwarded", body)
	}
	if body["resolution"] != "1080P" {
		t.Errorf("resolution = %v, want the schema's own spelling 1080P", body["resolution"])
	}
	if _, ok := body["keep_audio"]; ok {
		t.Error("keep_audio must not be written on a schema that does not declare it")
	}
	if !strings.Contains(joinedNotices(notices), "one reference photo per character") {
		t.Errorf("notices = %v, want the extra-angle drop explained", notices)
	}
}

// TestResolveCharacterReplaceBodyRefusals pins the pre-money refusals: more
// people than the model replaces, and a source clip outside the documented
// window (Recast: 5–30 seconds; Kling o1: 3–10.05s).
func TestResolveCharacterReplaceBodyRefusals(t *testing.T) {
	recast := characterFixtureSchema(t, "minimax-h3-max-recast")
	klingO1 := characterFixtureSchema(t, "kling-o1-video-to-video-edit")
	klingO3 := characterFixtureSchema(t, "kling-o3-pro-video-to-video-edit")
	wan := characterFixtureSchema(t, "wan-2.2-14b-animate-replace")
	five := characters([]string{"a"}, []string{"b"}, []string{"c"}, []string{"d"}, []string{"e"})
	cases := []struct {
		name    string
		schema  *ModelInputSchema
		req     VideoCharacterReplaceRequest
		wantErr string
	}{
		{"recast five people", recast, VideoCharacterReplaceRequest{Model: "minimax/h3-max/recast", Video: "v", Characters: five}, "at most 4 people"},
		{"recast too long", recast, VideoCharacterReplaceRequest{Model: "minimax/h3-max/recast", Video: "v", Characters: characters([]string{"a"}), SourceSeconds: 41}, "split_video"},
		{"recast too short", recast, VideoCharacterReplaceRequest{Model: "minimax/h3-max/recast", Video: "v", Characters: characters([]string{"a"}), SourceSeconds: 3}, "at least 5s"},
		{"kling o1 too long", klingO1, VideoCharacterReplaceRequest{Model: "fal-ai/kling-video/o1/video-to-video/edit", Video: "v", Characters: characters([]string{"a"}), SourceSeconds: 12}, "at most 10.05s"},
		{"kling five elements", klingO3, VideoCharacterReplaceRequest{Model: "fal-ai/kling-video/o3/pro/video-to-video/edit", Video: "v", Characters: five}, "at most 4 characters"},
		{"wan two people", wan, VideoCharacterReplaceRequest{Model: "fal-ai/wan/v2.2-14b/animate/replace", Video: "v", Characters: characters([]string{"a"}, []string{"b"})}, "single person"},
		{"no characters", recast, VideoCharacterReplaceRequest{Model: "minimax/h3-max/recast", Video: "v"}, "at least one character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := resolveCharacterReplaceBody(tc.schema, tc.req, builtinFalOverrides())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
	// An in-window clip passes on o3 (3–15s) where o1 refuses.
	if _, _, _, err := resolveCharacterReplaceBody(klingO3, VideoCharacterReplaceRequest{Model: "fal-ai/kling-video/o3/pro/video-to-video/edit", Video: "v", Characters: characters([]string{"a"}), SourceSeconds: 12}, builtinFalOverrides()); err != nil {
		t.Errorf("o3 12s clip: %v, want it accepted", err)
	}
	// The refusals carry the sentinel so callers can tell them from transport errors.
	_, _, _, err := resolveCharacterReplaceBody(wan, cases[5].req, builtinFalOverrides())
	if !errors.Is(err, errCharacterReplaceUnsupported) {
		t.Errorf("err = %v, want errCharacterReplaceUnsupported", err)
	}
}

// TestResolveCharacterReplaceBodyKlingElements pins the character-sheet shape:
// a frontal image plus at most three extra angles per element, the @Element
// legend authored when the prompt names none (with @Video1 only where the
// model documents it), a user prompt that already names the element kept
// verbatim, a single-image character repeating its frontal view as the
// required extra angle, and the soundtrack kept by default.
func TestResolveCharacterReplaceBodyKlingElements(t *testing.T) {
	o1 := characterFixtureSchema(t, "kling-o1-video-to-video-edit")
	body, notices, _, err := resolveCharacterReplaceBody(o1, VideoCharacterReplaceRequest{
		Model:      "fal-ai/kling-video/o1/video-to-video/edit",
		Video:      "https://fal.example/source.mp4",
		Characters: characters([]string{"https://x/f", "https://x/a1", "https://x/a2", "https://x/a3", "https://x/a4"}),
		Prompt:     "keep the red scarf",
	}, builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	elements, _ := body["elements"].([]any)
	if len(elements) != 1 {
		t.Fatalf("elements = %v, want one element", body["elements"])
	}
	element, _ := elements[0].(map[string]any)
	refs, _ := element["reference_image_urls"].([]any)
	if element["frontal_image_url"] != "https://x/f" || len(refs) != 3 || refs[0] != "https://x/a1" || refs[2] != "https://x/a3" {
		t.Errorf("element = %v, want frontal f and the first three extra angles", element)
	}
	if !strings.Contains(joinedNotices(notices), "at most 3 extra angles") {
		t.Errorf("notices = %v, want the angle trim explained", notices)
	}
	prompt, _ := body["prompt"].(string)
	if !strings.HasPrefix(prompt, "Replace the main character in the video with @Element1") || !strings.HasSuffix(prompt, "keep the red scarf") {
		t.Errorf("prompt = %q, want the legend prepended to the user's steer", prompt)
	}
	if body["keep_audio"] != true {
		t.Errorf("keep_audio = %v, want true by default (o1's own default is false)", body["keep_audio"])
	}

	o3 := characterFixtureSchema(t, "kling-o3-pro-video-to-video-edit")
	keep := false
	body, _, diagnostics, err := resolveCharacterReplaceBody(o3, VideoCharacterReplaceRequest{
		Model:      "fal-ai/kling-video/o3/pro/video-to-video/edit",
		Video:      "v",
		Characters: characters([]string{"https://x/x"}, []string{"https://x/y"}),
		KeepAudio:  &keep,
	}, builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolve o3: %v", err)
	}
	prompt, _ = body["prompt"].(string)
	if !strings.Contains(prompt, "@Video1") || !strings.Contains(prompt, "@Element1, @Element2") {
		t.Errorf("o3 prompt = %q, want the @Video1 legend naming both elements", prompt)
	}
	elements, _ = body["elements"].([]any)
	second, _ := elements[1].(map[string]any)
	if refs, _ := second["reference_image_urls"].([]any); len(refs) != 1 || refs[0] != "https://x/y" {
		t.Errorf("single-image element = %v, want its frontal repeated as the extra angle", second)
	}
	if len(diagnostics) == 0 {
		t.Error("the frontal reuse must ride a diagnostic")
	}
	if body["keep_audio"] != false {
		t.Errorf("keep_audio = %v, want the explicit false honored", body["keep_audio"])
	}

	body, _, _, err = resolveCharacterReplaceBody(o3, VideoCharacterReplaceRequest{
		Model: "fal-ai/kling-video/o3/pro/video-to-video/edit", Video: "v",
		Characters: characters([]string{"x"}), Prompt: "@Element1 replaces the chef in @Video1",
	}, builtinFalOverrides())
	if err != nil || body["prompt"] != "@Element1 replaces the chef in @Video1" {
		t.Errorf("prompt = %v (err %v), want a token-bearing prompt kept verbatim", body["prompt"], err)
	}
}

// TestResolveCharacterReplaceBodyWanReplace pins the scalar shape: one image
// on image_url, no prompt input (a steer drops with a notice), the lowercase
// resolution enum.
func TestResolveCharacterReplaceBodyWanReplace(t *testing.T) {
	wan := characterFixtureSchema(t, "wan-2.2-14b-animate-replace")
	body, notices, _, err := resolveCharacterReplaceBody(wan, VideoCharacterReplaceRequest{
		Model:      "fal-ai/wan/v2.2-14b/animate/replace",
		Video:      "v",
		Characters: characters([]string{"https://x/front", "https://x/side"}),
		Prompt:     "keep the hat",
		Resolution: "720p",
	}, builtinFalOverrides())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if body["image_url"] != "https://x/front" || body["resolution"] != "720p" {
		t.Errorf("body = %v, want the frontal on image_url and 720p", body)
	}
	if _, ok := body["prompt"]; ok {
		t.Error("prompt must not be written on a schema that does not declare it")
	}
	all := joinedNotices(notices)
	if !strings.Contains(all, "takes no prompt") || !strings.Contains(all, "one reference photo per character") {
		t.Errorf("notices = %v, want the prompt and extra-angle drops explained", notices)
	}
}

func TestCharacterSourceDurationWindow(t *testing.T) {
	cases := []struct {
		desc     string
		min, max float64
		ok       bool
	}{
		{"Max file size: 200.0MB, Min duration: 3.0s, Max duration: 10.05s, Min FPS: 24.0", 3, 10.05, true},
		{"The source video, 5 to 30 seconds long, with no single shot longer than 15 seconds.", 5, 30, true},
		{"URL of the input video.", 0, 0, false},
	}
	for _, tc := range cases {
		min, max, ok := characterSourceDurationWindow(tc.desc)
		if ok != tc.ok || min != tc.min || max != tc.max {
			t.Errorf("window(%q) = %g, %g, %v; want %g, %g, %v", tc.desc, min, max, ok, tc.min, tc.max, tc.ok)
		}
	}
}

func TestSplitCharacterReferences(t *testing.T) {
	images := []string{"a", "b", "c"}
	one, err := splitCharacterReferences(images, nil)
	if err != nil || len(one) != 1 || len(one[0].Images) != 3 {
		t.Errorf("default split = %v (%v), want one character sheet of three", one, err)
	}
	two, err := splitCharacterReferences(images, []int{2, 1})
	if err != nil || len(two) != 2 || two[1].Images[0] != "c" {
		t.Errorf("split [2,1] = %v (%v), want a,b then c", two, err)
	}
	if _, err := splitCharacterReferences(images, []int{1, 1}); err == nil {
		t.Error("counts summing short of the images must error")
	}
	if got := parseCharacterImageCounts("[2, 1]"); len(got) != 2 || got[0] != 2 {
		t.Errorf("parse [2, 1] = %v", got)
	}
	if got := parseCharacterImageCounts("2,x"); got != nil {
		t.Errorf("parse 2,x = %v, want nil", got)
	}
}

// TestReplaceCharacterToolExecute pins the executor: the attached video and
// images forwarded, images grouped by characterImageCounts, the default model,
// and the fal attribution on the activity.
func TestReplaceCharacterToolExecute(t *testing.T) {
	var captured VideoCharacterReplaceRequest
	tools := HarnessToolExecutionContext{
		AttachedVideos: []string{"data:video/mp4;base64,AAA"},
		AttachedImages: []string{"img1", "img2", "img3"},
		ReplaceCharacter: func(_ context.Context, req VideoCharacterReplaceRequest) (GeneratedVideo, error) {
			captured = req
			return GeneratedVideo{Data: []byte("fake-mp4"), MimeType: "video/mp4"}, nil
		},
	}
	def := videoCharacterReplaceToolDefinition(AppConfig{})
	result, summary, err := def.Execute(t.Context(), tools, HarnessToolCall{CharacterImageCounts: []int{2, 1}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if captured.Model != defaultFalVideoCharacterModel || captured.Video != "data:video/mp4;base64,AAA" {
		t.Errorf("captured = %+v, want the default model and the attached video", captured)
	}
	if len(captured.Characters) != 2 || len(captured.Characters[0].Images) != 2 {
		t.Errorf("characters = %+v, want the [2,1] grouping", captured.Characters)
	}
	if !strings.Contains(summary, "2 characters") {
		t.Errorf("summary = %q", summary)
	}
	typed, ok := result.(ToolVideoResult)
	if !ok || len(typed.Videos) != 1 {
		t.Fatalf("result = %#v, want one ToolVideoResult video", result)
	}
	t.Cleanup(func() { _ = os.Remove(typed.Videos[0].TempPath) })
	activity := def.Activity(HarnessToolResult{Name: "replace_character", Status: "completed", Result: typed})
	if activity.Provider != "fal" {
		t.Errorf("activity provider = %q, want fal (the tool does not follow VideoProvider)", activity.Provider)
	}

	// Missing sources fail before the hook runs.
	for _, tc := range []HarnessToolExecutionContext{
		{AttachedImages: []string{"img"}, ReplaceCharacter: tools.ReplaceCharacter},
		{AttachedVideos: []string{"vid"}, ReplaceCharacter: tools.ReplaceCharacter},
	} {
		if _, _, err := def.Execute(t.Context(), tc, HarnessToolCall{}); err == nil {
			t.Errorf("Execute(%+v) succeeded, want a missing-source error", tc)
		}
	}
	if errs := def.Validate("toolCalls[0]", HarnessToolCall{CharacterImageCounts: []int{0}}); len(errs) == 0 {
		t.Error("a zero count must draw a correction")
	}
}

func TestVideoCharacterReplaceConfiguredAndResolver(t *testing.T) {
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })
	// Fal-only: a Runware video provider still offers the tool with a fal key.
	if !videoCharacterReplaceConfigured(AppConfig{Models: ConfigModels{VideoProvider: "runware"}}) {
		t.Error("configured(fal key, runware video provider) = false, want true")
	}
	if videoCharacterReplaceConfigured(AppConfig{AirplaneMode: true}) {
		t.Error("airplane mode must drop the tool")
	}
	if got := resolveDefaultVideoCharacterModel(AppConfig{Providers: ConfigProviders{Fal: ConfigFal{VideoCharacterModel: "fal-ai/wan/v2.2-14b/animate/replace"}}}); got != "fal-ai/wan/v2.2-14b/animate/replace" {
		t.Errorf("configured resolver = %q", got)
	}
	if !contains(configuredFalEndpointIDs(AppConfig{}), defaultFalVideoCharacterModel) {
		t.Errorf("configuredFalEndpointIDs missing %q", defaultFalVideoCharacterModel)
	}
	if err := clearFalAPIKey(); err != nil {
		t.Fatal(err)
	}
	if videoCharacterReplaceConfigured(AppConfig{}) {
		t.Error("configured(no key) = true, want false")
	}
}

// TestFalVideoCharacterPartition pins the picker filter against the restyle
// and motion pickers: Recast and Wan Replace are character models only,
// DreamActor and Wan Motion are motion control, and the Kling edit endpoints
// serve both restyle and character replacement.
func TestFalVideoCharacterPartition(t *testing.T) {
	cases := []struct {
		id                         string
		character, motion, restyle bool
	}{
		{"minimax/h3-max/recast", true, false, false},
		{"fal-ai/wan/v2.2-14b/animate/replace", true, false, false},
		{"fal-ai/kling-video/o1/video-to-video/edit", true, false, true},
		{"fal-ai/kling-video/o3/pro/video-to-video/edit", true, false, true},
		{"fal-ai/bytedance/dreamactor/v2", false, true, false},
		{"fal-ai/wan-motion", false, true, false},
		{"fal-ai/ltx-2.3/reframe", false, false, false},
	}
	for _, tc := range cases {
		model := FalModel{ID: tc.id}
		if got := isFalVideoCharacterModel(model); got != tc.character {
			t.Errorf("isFalVideoCharacterModel(%s) = %v, want %v", tc.id, got, tc.character)
		}
		if got := isFalVideoMotionModel(model); got != tc.motion {
			t.Errorf("isFalVideoMotionModel(%s) = %v, want %v", tc.id, got, tc.motion)
		}
		if got := isFalVideoRestyleModel(model); got != tc.restyle {
			t.Errorf("isFalVideoRestyleModel(%s) = %v, want %v", tc.id, got, tc.restyle)
		}
	}
}

func TestTriagePromptRoutesCharacterReplacement(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{videoCharacterReplaceToolDefinition(AppConfig{})})
	prompt := triageSystemPrompt(registry, nil, "/tmp/ws", false)
	for _, want := range []string{"character replacement — the replace_character tool", "not generate_video's motion control"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("triage prompt missing %q", want)
		}
	}
}
