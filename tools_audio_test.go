package main

import (
	"context"
	"strings"
	"testing"
)

// TestGenerateSpeechSurfacesNotices verifies the speech tool carries resolver
// notices onto its result via the NoticeProvider interface, so the gateway can
// lift them into the chat reply.
func TestGenerateSpeechSurfacesNotices(t *testing.T) {
	tools := HarnessToolExecutionContext{
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			if req.Voice != "Rachel" {
				t.Errorf("speech tool should forward voice, got %q", req.Voice)
			}
			if req.Duration != "" || req.Loop || req.NegativePrompt != "" {
				t.Errorf("speech tool should not set sound params, got %+v", req)
			}
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg", Notices: []string{"voice ignored"}}, nil
		},
	}
	def := speechGenerationToolDefinition(false)
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "hello there", Voice: "Rachel", Model: "m"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	np, ok := out.(NoticeProvider)
	if !ok {
		t.Fatalf("expected result to implement NoticeProvider, got %T", out)
	}
	notices := np.ToolNotices()
	if len(notices) != 1 || notices[0] != "voice ignored" {
		t.Fatalf("expected [voice ignored], got %v", notices)
	}
}

// TestGenerateSpeechVoiceCloningRouting pins the attachment-driven model
// selection and its cloneVoice override: the user's attached clip
// (VoiceReference) both switches the default to the cloning endpoint and rides
// the request as SourceAudio, cloneVoice forces or suppresses that path
// (nil = auto), and call.Model still overrides either path — the speech
// sibling of the lipsync image/video split.
func TestGenerateSpeechVoiceCloningRouting(t *testing.T) {
	const reference = "data:audio/mpeg;base64,QUJD"
	config := defaultAppConfig()
	config.Providers.Fal.AudioModel = "fal-ai/speech/model"
	config.Providers.Fal.AudioCloneModel = "fal-ai/clone/model"
	no := false
	yes := true

	cases := []struct {
		name       string
		voiceRef   string
		callClone  *bool
		callModel  string
		wantModel  string
		wantSource string
	}{
		{"no reference uses the speech model", "", nil, "", "fal-ai/speech/model", ""},
		{"reference selects the clone model", reference, nil, "", "fal-ai/clone/model", reference},
		{"cloneVoice true forces the clone path", reference, &yes, "", "fal-ai/clone/model", reference},
		{"cloneVoice false opts out with a reference", reference, &no, "", "fal-ai/speech/model", ""},
		{"cloneVoice true without a reference degrades to speech", "", &yes, "", "fal-ai/speech/model", ""},
		{"call model overrides the clone path", reference, nil, "fal-ai/manual", "fal-ai/manual", reference},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotReq AudioGenerateRequest
			tools := HarnessToolExecutionContext{
				Config:         config,
				VoiceReference: tc.voiceRef,
				GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
					gotReq = req
					return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg"}, nil
				},
			}
			def := speechGenerationToolDefinition(false)
			out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "hello", Model: tc.callModel, CloneVoice: tc.callClone})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotReq.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", gotReq.Model, tc.wantModel)
			}
			if gotReq.SourceAudio != tc.wantSource {
				t.Fatalf("sourceAudio = %q, want %q", gotReq.SourceAudio, tc.wantSource)
			}
			// Forced cloning without an attached clip must degrade with a
			// notice, not fail and not clone from nothing.
			np, ok := out.(NoticeProvider)
			if !ok {
				t.Fatalf("expected result to implement NoticeProvider, got %T", out)
			}
			notices := np.ToolNotices()
			wantNotice := tc.voiceRef == "" && tc.callClone != nil && *tc.callClone
			if wantNotice && (len(notices) != 1 || !strings.Contains(notices[0], "regular voice")) {
				t.Fatalf("expected a degrade notice for forced cloning without a clip, got %v", notices)
			}
			if !wantNotice && len(notices) != 0 {
				t.Fatalf("expected no notices, got %v", notices)
			}
		})
	}

	// Unset AudioCloneModel falls back to the built-in cloning default.
	bare := defaultAppConfig()
	bare.Providers.Fal.AudioModel = "fal-ai/speech/model"
	var gotModel string
	tools := HarnessToolExecutionContext{
		Config:         bare,
		VoiceReference: reference,
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			gotModel = req.Model
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg"}, nil
		},
	}
	if _, _, err := speechGenerationToolDefinition(false).Execute(context.Background(), tools, HarnessToolCall{Content: "hello"}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotModel != defaultFalAudioCloneModel {
		t.Fatalf("clone fallback model = %q, want %q", gotModel, defaultFalAudioCloneModel)
	}
}

// TestGenerateSoundSurfacesNotices is the sound-effects sibling: the canonical
// sound params (duration/loop/negativePrompt) flow through and notices ride the
// result the same way.
func TestGenerateSoundSurfacesNotices(t *testing.T) {
	tools := HarnessToolExecutionContext{
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			if req.Duration != "10" || !req.Loop || req.NegativePrompt != "vocals" {
				t.Errorf("sound tool should forward duration/loop/negativePrompt, got %+v", req)
			}
			if req.Style != "jazz" {
				t.Errorf("sound tool should forward style, got %q", req.Style)
			}
			if req.Voice != "" {
				t.Errorf("sound tool should not set voice, got %q", req.Voice)
			}
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg", Notices: []string{"loop ignored"}}, nil
		},
	}
	def := soundEffectsGenerationToolDefinition()
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "rain", Duration: "10", Loop: true, NegativePrompt: "vocals", Style: "jazz", Model: "m"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	np, ok := out.(NoticeProvider)
	if !ok {
		t.Fatalf("expected result to implement NoticeProvider, got %T", out)
	}
	notices := np.ToolNotices()
	if len(notices) != 1 || notices[0] != "loop ignored" {
		t.Fatalf("expected [loop ignored], got %v", notices)
	}
}

// TestExtendAudioRoutingAndValidation pins the extend_audio tool's contract:
// validation (content required, direction enum), the fail-fast on a missing
// source clip, the first-attachment source selection, param forwarding, and
// the default-model resolution chain (call override → config → built-in
// default).
func TestExtendAudioRoutingAndValidation(t *testing.T) {
	const clip = "data:audio/mpeg;base64,QUJD"
	const second = "data:audio/mpeg;base64,REVG"
	def := extendAudioToolDefinition()

	if errs := def.Validate("toolCalls[0]", HarnessToolCall{}); len(errs) != 1 || !strings.Contains(errs[0], "content is required") {
		t.Fatalf("content validation = %v", errs)
	}
	if errs := def.Validate("toolCalls[0]", HarnessToolCall{Content: "x", Direction: "middle"}); len(errs) != 1 || !strings.Contains(errs[0], "direction") {
		t.Fatalf("direction validation = %v", errs)
	}
	for _, direction := range []string{"", "after", "before"} {
		if errs := def.Validate("toolCalls[0]", HarnessToolCall{Content: "x", Direction: direction}); len(errs) != 0 {
			t.Fatalf("direction %q should validate, got %v", direction, errs)
		}
	}

	// A missing source fails the call rather than generating brand-new audio.
	tools := HarnessToolExecutionContext{
		GenerateAudioExtend: func(ctx context.Context, req AudioExtendRequest) (GeneratedAudio, error) {
			t.Fatal("extend must not run without a source clip")
			return GeneratedAudio{}, nil
		},
	}
	if _, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "more rain"}); err == nil || !strings.Contains(err.Error(), "needs an audio clip") {
		t.Fatalf("missing source error = %v", err)
	}

	// Unavailable backend fails, mirroring audioGenerationExecute.
	if _, _, err := def.Execute(context.Background(), HarnessToolExecutionContext{}, HarnessToolCall{Content: "more rain"}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unavailable backend error = %v", err)
	}

	config := defaultAppConfig()
	config.Providers.Fal.AudioExtendModel = "fal-ai/configured/extend"
	cases := []struct {
		name      string
		config    AppConfig
		callModel string
		wantModel string
	}{
		{"configured model wins over the default", config, "", "fal-ai/configured/extend"},
		{"built-in default when unset", defaultAppConfig(), "", defaultFalAudioExtendModel},
		{"call model overrides the config", config, "fal-ai/manual", "fal-ai/manual"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotReq AudioExtendRequest
			tools := HarnessToolExecutionContext{
				Config:         tc.config,
				AttachedAudios: []string{clip, second},
				GenerateAudioExtend: func(ctx context.Context, req AudioExtendRequest) (GeneratedAudio, error) {
					gotReq = req
					return GeneratedAudio{Data: []byte("x"), MimeType: "audio/wav", Notices: []string{"capped"}}, nil
				},
			}
			out, summary, err := def.Execute(context.Background(), tools, HarnessToolCall{
				Content: "more rain", Duration: "20", Direction: "before",
				Style: "lofi", Lyrics: "la", NegativePrompt: "vocals", Model: tc.callModel,
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotReq.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", gotReq.Model, tc.wantModel)
			}
			// The FIRST attached clip is the source — fal endpoints take one
			// audio_url.
			if gotReq.SourceAudio != clip {
				t.Fatalf("source = %q, want the first attachment", gotReq.SourceAudio)
			}
			if gotReq.Prompt != "more rain" || gotReq.Duration != "20" || gotReq.Direction != "before" ||
				gotReq.Style != "lofi" || gotReq.Lyrics != "la" || gotReq.NegativePrompt != "vocals" {
				t.Fatalf("params not forwarded: %+v", gotReq)
			}
			audio, ok := out.(ToolAudioResult)
			if !ok {
				t.Fatalf("expected ToolAudioResult, got %T", out)
			}
			if audio.Model != tc.wantModel || audio.Count != 1 || len(audio.Audios) != 1 {
				t.Fatalf("result = %+v", audio)
			}
			if !strings.Contains(summary, tc.wantModel) {
				t.Fatalf("summary = %q, want it to name the model", summary)
			}
			np, ok := out.(NoticeProvider)
			if !ok || len(np.ToolNotices()) != 1 || np.ToolNotices()[0] != "capped" {
				t.Fatalf("notices must ride the result, got %T %+v", out, out)
			}
		})
	}
}

// TestExtendAudioDescriptionBlessesBareLength pins the catalog half of the
// conv_a1990c38b8ee9269525a4c0a lesson: a bare "extend this clip by 20s" with
// no description is a valid extend_audio request. The tool description must
// teach the same-style default and forbid asking — the small harness model
// read "content required" as "uncallable without a description" and routed
// the turn to text.
func TestExtendAudioDescriptionBlessesBareLength(t *testing.T) {
	description := extendAudioDescription()
	if !strings.Contains(description, "continue in the same style") {
		t.Fatalf("description should teach the bare-length default:\n%s", description)
	}
	if !strings.Contains(description, "do not ask the user") {
		t.Fatalf("description should forbid asking instead of extending:\n%s", description)
	}
	schema := extendAudioParamSchema()
	content, ok := schema["properties"].(map[string]any)["content"].(map[string]any)
	if !ok {
		t.Fatalf("content property missing from schema: %#v", schema)
	}
	if !strings.Contains(content["description"].(string), "continue in the same style") {
		t.Fatalf("content param should carry the bare-length default too: %#v", content)
	}
}
