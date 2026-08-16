package main

import (
	"context"
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
