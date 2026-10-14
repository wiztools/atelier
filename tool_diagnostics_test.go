package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The diagnostics channel is the machine-facing twin of notices: where Notices
// carry deterministic caveats the user must see (rendered as the reply's ⚠️
// blockquote), Diagnostics carry resolution decisions that explain how a
// SUCCESSFUL call's sources were mapped onto the model's inputs — the
// multi-reference rebind ("takes multiple references on reference_image_urls").
// Those must never render in the chat reply (conv feedback: the blockquote
// read as a warning although the request worked as intended), but they still
// ride tool evidence and the persisted run so the models and the audit trail
// keep them. These tests pin the seam split: resolver diagnostics → tool
// result payload → gateway Execute → HarnessToolResult, with collectToolNotices
// (the reply renderer) reading only Notices.

const testRebindDiagnostic = `The selected model "minimax/h3-max/reference-to-video" takes multiple references on "reference_image_urls"; the attached images were mapped there.`

// TestGatewayRoutesVideoDiagnosticsBesideNotices drives generate_video through
// a stubbed transport carrying BOTH a user-facing notice and a machine-facing
// rebind diagnostic, and pins that gateway.Execute lands each on its own
// HarnessToolResult channel.
func TestGatewayRoutesVideoDiagnosticsBesideNotices(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{videoGenerationToolDefinition(AppConfig{}, true)})
	tools := HarnessToolExecutionContext{Config: AppConfig{}}
	tools.GenerateVideo = func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
		return GeneratedVideo{
			Data:        []byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'},
			MimeType:    "video/mp4",
			Notices:     []string{"The selected model has no end-frame input; animating from the start frame only."},
			Diagnostics: []string{testRebindDiagnostic},
		}, nil
	}
	gateway := ToolGateway{registry: registry, tools: tools}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "generate_video",
		Call: HarnessToolCall{Name: "generate_video", Model: "minimax/h3-max/reference-to-video", Content: "one shot"},
	})
	if result.Status != "completed" {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}

	// The reply channel keeps only the notice.
	if len(result.Notices) != 1 || !strings.Contains(result.Notices[0], "end-frame input") {
		t.Fatalf("result.Notices = %v, want exactly the user-facing caveat", result.Notices)
	}
	// The diagnostics channel keeps only the rebind.
	if len(result.Diagnostics) != 1 || result.Diagnostics[0] != testRebindDiagnostic {
		t.Fatalf("result.Diagnostics = %v, want exactly the rebind decision", result.Diagnostics)
	}

	// The typed payload carries the same split (toolResultMessageView marshals
	// this struct into planner/final-model evidence and the thinking panel).
	typed, ok := result.Result.(ToolVideoResult)
	if !ok {
		t.Fatalf("result.Result = %T, want ToolVideoResult", result.Result)
	}
	if len(typed.Diagnostics) != 1 || typed.Diagnostics[0] != testRebindDiagnostic {
		t.Fatalf("ToolVideoResult.Diagnostics = %v, want the rebind decision", typed.Diagnostics)
	}

	// The json tags decide where each lands downstream: collectToolNotices
	// (the reply) reads only Notices; the marshaled result (tool evidence,
	// thinking panel, persisted run) carries both under their own keys.
	if reply := collectToolNotices([]HarnessToolResult{result}); strings.Contains(reply, "takes multiple references") {
		t.Fatalf("collectToolNotices leaked the rebind diagnostic into the reply: %s", reply)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(raw), `"diagnostics"`) || !strings.Contains(string(raw), "takes multiple references") {
		t.Fatalf("marshaled result lost the diagnostics channel: %s", raw)
	}
}

// TestGatewayRoutesImageDiagnosticsBesideNotices is the generate_image twin:
// the Replicate image resolver's rebind rides ollamaGenerateResponse.Diagnostics
// (stamped by the gateway beside Notices) through the executor onto
// ToolImageResult and the result's diagnostics channel.
func TestGatewayRoutesImageDiagnosticsBesideNotices(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{imageGenerationToolDefinition(AppConfig{})})
	tools := HarnessToolExecutionContext{Config: AppConfig{}}
	tools.GenerateImage = func(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, []string, error) {
		return ollamaGenerateResponse{
			Images:      []string{"data:image/png;base64," + tinyPNG},
			Diagnostics: []string{testRebindDiagnostic},
		}, nil, nil, nil
	}
	gateway := ToolGateway{registry: registry, tools: tools}

	result := gateway.Execute(context.Background(), ToolExecutionRequest{
		Name: "generate_image",
		Call: HarnessToolCall{Name: "generate_image", Model: "owner/model", Content: "one shot"},
	})
	if result.Status != "completed" {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}
	if len(result.Notices) != 0 {
		t.Fatalf("result.Notices = %v, want none", result.Notices)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0] != testRebindDiagnostic {
		t.Fatalf("result.Diagnostics = %v, want exactly the rebind decision", result.Diagnostics)
	}
	typed, ok := result.Result.(ToolImageResult)
	if !ok {
		t.Fatalf("result.Result = %T, want ToolImageResult", result.Result)
	}
	if len(typed.Diagnostics) != 1 || typed.Diagnostics[0] != testRebindDiagnostic {
		t.Fatalf("ToolImageResult.Diagnostics = %v, want the rebind decision", typed.Diagnostics)
	}
	if reply := collectToolNotices([]HarnessToolResult{result}); strings.Contains(reply, "takes multiple references") {
		t.Fatalf("collectToolNotices leaked the rebind diagnostic into the reply: %s", reply)
	}
}
