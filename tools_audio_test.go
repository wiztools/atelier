package main

import (
	"context"
	"testing"
)

// TestGenerateAudioSurfacesNotices verifies the audio tool carries resolver
// notices onto its result via the NoticeProvider interface, so the gateway can
// lift them into the chat reply.
func TestGenerateAudioSurfacesNotices(t *testing.T) {
	tools := HarnessToolExecutionContext{
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg", Notices: []string{"loop ignored"}}, nil
		},
	}
	def := audioGenerationToolDefinition()
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "rain", Loop: true, Model: "m"})
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
