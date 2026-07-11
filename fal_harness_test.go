package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestHarnessGeneratesImageViaFal is the fal.ai counterpart of
// TestHarnessGeneratesImageViaPlannedTool. It runs the full chat turn
// (triage → planner → generate_image tool → fal queue API → final response) and
// confirms the generated image is persisted as a history artifact.
//
// This is a regression test for a bug where the fal client returned the raw fal
// result JSON alongside the downloaded image data URL. The tool's
// collectImagesFromJSON backstop then re-harvested the source https URL from
// that raw JSON, so appendChatAssistantTurnWithImages received two images: the
// decodable data URL and a bare URL that decodeImagePayload could not handle.
// writeChatImageArtifacts wrote the first artifact and then errored on the
// second, aborting the whole turn save and orphaning the artifact on disk — the
// assistant turn never appeared in history.
func TestHarnessGeneratesImageViaFal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "chat-box-model"
	config.Models.ImageProvider = "fal"
	config.Providers.Fal.Model = "fal-ai/flux/schnell"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	// Minimal valid PNG the fal result "hosts" for the client to download.
	pngBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x60, 0x00, 0x00, 0x00, 0x00, 0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC, 0x33,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}

	app := NewApp()
	falCalls := 0
	statusPolls := 0
	prepCalls := 0
	nonStreamCount := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "fal.run") {
			falCalls++
			if req.Method == http.MethodPost {
				return jsonResponse(`{"request_id":"req-fal-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				statusPolls++
				if statusPolls == 1 {
					return jsonResponse(`{"status":"IN_PROGRESS"}`), nil
				}
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-fal-1") {
				return jsonResponse(`{"images":[{"url":"https://queue.fal.run/generated.png"}]}`), nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(string(pngBytes))),
				Header: http.Header{"Content-Type": []string{"image/png"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					decision := `{"needsTools":true,"responseMode":"image","toolTask":"Generate an image of a small house.","reason":"The user asked for an image."}`
					return chatCompletion("harness-model", decision), nil
				}
				prepCalls++
				body := `{"brief":"Generate the requested image.","needsTools":true,"reason":"image","toolCalls":[{"name":"generate_image","content":"a small house with a red roof"}]}`
				if prepCalls > 1 {
					body = `{"brief":"The image was generated.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("harness-model", body), nil
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the small house."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-fal-image", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create an image of a small house"},
		},
	})

	if falCalls == 0 || statusPolls == 0 {
		t.Fatalf("fal queue was never called: falCalls=%d statusPolls=%d", falCalls, statusPolls)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(conversations))
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("turn count = %d, want user + assistant (the regression: the assistant turn was not saved)", len(detail.Turns))
	}
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifactCount = %d, want 1", detail.Conversation.Stats.ArtifactCount)
	}
	assistant := detail.Turns[1]
	images := historyImagesForTest(assistant.Content)
	if len(images) != 1 {
		t.Fatalf("assistant image content = %+v, want one image artifact", assistant.Content)
	}
	tool, ok := assistant.ProviderResponse["tool"].(map[string]any)
	if !ok || tool["name"] != "image_generation" || tool["model"] != "fal-ai/flux/schnell" {
		t.Fatalf("assistant provider tool = %+v, want image_generation via fal-ai/flux/schnell", assistant.ProviderResponse["tool"])
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Status: "200 OK",
		Body:   io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": []string{"application/json"}}}
}

func chatCompletion(model, content string) *http.Response {
	return jsonResponse(`{"model":"` + model + `","message":{"role":"assistant","content":` + strconv.Quote(content) + `},"done":true,"done_reason":"stop","eval_count":2}`)
}

func chatPayload(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	data, _ := io.ReadAll(req.Body)
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("provider request body is not JSON: %v", err)
	}
	return payload
}
