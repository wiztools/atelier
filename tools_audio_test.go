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
