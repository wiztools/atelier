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

// TestHarnessGeneratesVideoViaFal runs the full chat turn (triage → planner →
// generate_video tool → fal queue API → download → final response) and confirms
// the generated clip is persisted as a file-path "video" history artifact — the
// mp4 lands on disk under artifacts/, history references it by path (never
// base64), and the provider tool metadata records the video generation.
func TestHarnessGeneratesVideoViaFal(t *testing.T) {
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
	config.Providers.Fal.VideoModel = defaultFalVideoModel
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	falCalls := 0
	statusPolls := 0
	nonStreamCount := 0
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "fal.run") {
			falCalls++
			if req.Method == http.MethodPost {
				return jsonResponse(`{"request_id":"req-vid-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				statusPolls++
				if statusPolls == 1 {
					return jsonResponse(`{"status":"IN_PROGRESS"}`), nil
				}
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-vid-1") {
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/generated.mp4","content_type":"video/mp4"}}`), nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(string(tinyMP4()))),
				Header: http.Header{"Content-Type": []string{"video/mp4"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					decision := `{"needsTools":true,"responseMode":"video","toolTask":"Generate a video of a forest flyover.","reason":"The user asked for a video."}`
					return chatCompletion("harness-model", decision), nil
				}
				prepCalls++
				body := `{"brief":"Generate the requested video.","needsTools":true,"reason":"video","toolCalls":[{"name":"generate_video","content":"a drone shot over a misty forest"}]}`
				if prepCalls > 1 {
					body = `{"brief":"The video was generated.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("harness-model", body), nil
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the clip."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-fal-video", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Create a video of a forest flyover"},
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
		t.Fatalf("turn count = %d, want user + assistant", len(detail.Turns))
	}
	if detail.Conversation.Stats.ArtifactCount != 1 {
		t.Fatalf("artifactCount = %d, want 1", detail.Conversation.Stats.ArtifactCount)
	}
	assistant := detail.Turns[1]
	var video *HistoryContent
	for i := range assistant.Content {
		if assistant.Content[i].Type == "video" {
			video = &assistant.Content[i]
		}
	}
	if video == nil {
		t.Fatalf("assistant content has no video artifact: %+v", assistant.Content)
	}
	if video.MimeType != "video/mp4" {
		t.Errorf("video mime = %q, want video/mp4", video.MimeType)
	}
	if !strings.HasPrefix(video.Path, "artifacts/") || !strings.HasSuffix(video.Path, ".mp4") {
		t.Errorf("video path = %q, want artifacts/*.mp4", video.Path)
	}
	// getConversation hydrates disk-backed artifacts to an /atelier-artifact URL;
	// its presence confirms the file exists on disk.
	if !strings.HasPrefix(video.Text, "/atelier-artifact/") {
		t.Errorf("hydrated video text = %q, want /atelier-artifact/ URL (file missing on disk?)", video.Text)
	}
	tool, ok := assistant.ProviderResponse["tool"].(map[string]any)
	if !ok || tool["name"] != "video_generation" || tool["model"] != defaultFalVideoModel {
		t.Fatalf("assistant provider tool = %+v, want video_generation via %s", assistant.ProviderResponse["tool"], defaultFalVideoModel)
	}
}

// TestHarnessAnimatesAttachedImageViaFal is the regression test for
// conv_a89e35615db14bdeec29cfff ("Animate this image..."). When the user
// attaches an image, generate_video must switch to image-to-video: use the
// image-to-video model and pass the attached frame to fal as image_url, rather
// than silently dropping the image and running text-to-video.
func TestHarnessAnimatesAttachedImageViaFal(t *testing.T) {
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
	// Only the text-to-video model is configured; the image-to-video model falls
	// back to its default, so an attached image works out of the box.
	config.Providers.Fal.VideoModel = defaultFalVideoModel
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	submittedModelPath := ""
	sawImageURL := false
	nonStreamCount := 0
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "fal.run") {
			if req.Method == http.MethodPost {
				submittedModelPath = req.URL.Path
				body, _ := io.ReadAll(req.Body)
				// fal rejects bare base64 with a 422; the frontend sends bare
				// base64, so the client must wrap it as a data URI.
				sawImageURL = strings.Contains(string(body), `"image_url":"data:image/`)
				return jsonResponse(`{"request_id":"req-i2v-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-i2v-1") {
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/generated.mp4","content_type":"video/mp4"}}`), nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(string(tinyMP4()))),
				Header: http.Header{"Content-Type": []string{"video/mp4"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					decision := `{"needsTools":true,"responseMode":"video","toolTask":"Animate the attached image.","reason":"The user asked to animate an image."}`
					return chatCompletion("harness-model", decision), nil
				}
				prepCalls++
				body := `{"brief":"Animate the attached image.","needsTools":true,"reason":"video","toolCalls":[{"name":"generate_video","content":"make the character talk"}]}`
				if prepCalls > 1 {
					body = `{"brief":"The video was generated.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("harness-model", body), nil
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the animation."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-fal-animate", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			// Bare base64 — exactly what the frontend sends (the data: prefix is
			// stripped for Ollama). fal needs it re-wrapped as a data URI.
			{Role: "user", Content: "Animate this image as if the character is talking", Images: []string{tinyPNG}},
		},
	})

	if !sawImageURL {
		t.Fatal("fal submit did not include image_url as a data URI — bare base64 is rejected by fal with 422")
	}
	if submittedModelPath != "/"+defaultFalVideoImageModel {
		t.Fatalf("submitted to %q, want the image-to-video model %q", submittedModelPath, "/"+defaultFalVideoImageModel)
	}

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	assistant := detail.Turns[len(detail.Turns)-1]
	hasVideo := false
	for _, c := range assistant.Content {
		if c.Type == "video" {
			hasVideo = true
		}
	}
	if !hasVideo {
		t.Fatalf("assistant turn has no video artifact: %+v", assistant.Content)
	}
}

// TestHarnessGeneratesAudioViaFal runs the full chat turn (triage → planner →
// generate_audio tool → fal queue → download → final response) and confirms the
// clip is persisted as a file-path "audio" history artifact.
func TestHarnessGeneratesAudioViaFal(t *testing.T) {
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
	config.Providers.Fal.AudioModel = defaultFalAudioModel
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	nonStreamCount := 0
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/api/openapi/") {
			return jsonResponse(`{"components":{"schemas":{"TtsInput":{"type":"object","required":["text"],"properties":{"text":{"type":"string"},"voice":{"type":"string","default":"Rachel"}}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			if req.Method == http.MethodPost {
				return jsonResponse(`{"request_id":"req-aud-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-aud-1") {
				return jsonResponse(`{"audio":{"url":"https://queue.fal.run/generated.mp3","content_type":"audio/mpeg"}}`), nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(string(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), 0xFF, 0xFB, 0x90, 0x00)))),
				Header: http.Header{"Content-Type": []string{"audio/mpeg"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"audio","toolTask":"Narrate the line.","reason":"The user asked for speech."}`), nil
				}
				prepCalls++
				body := `{"brief":"Narrate the line.","needsTools":true,"reason":"audio","toolCalls":[{"name":"generate_audio","content":"The happiness of your life depends upon the quality of your thoughts."}]}`
				if prepCalls > 1 {
					body = `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("harness-model", body), nil
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the narration."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-fal-audio", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Read this out loud: The happiness of your life depends upon the quality of your thoughts."},
		},
	})

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	assistant := detail.Turns[len(detail.Turns)-1]
	var audio *HistoryContent
	for i := range assistant.Content {
		if assistant.Content[i].Type == "audio" {
			audio = &assistant.Content[i]
		}
	}
	if audio == nil {
		t.Fatalf("assistant content has no audio artifact: %+v", assistant.Content)
	}
	if !strings.HasPrefix(audio.Path, "artifacts/") || !strings.HasSuffix(audio.Path, ".mp3") {
		t.Errorf("audio path = %q, want artifacts/*.mp3", audio.Path)
	}
	if !strings.HasPrefix(audio.Text, "/atelier-artifact/") {
		t.Errorf("hydrated audio text = %q, want /atelier-artifact/ URL (file missing?)", audio.Text)
	}
	tool, ok := assistant.ProviderResponse["tool"].(map[string]any)
	if !ok || tool["name"] != "audio_generation" {
		t.Fatalf("assistant provider tool = %+v, want audio_generation", assistant.ProviderResponse["tool"])
	}
}

// TestHarnessAppendsLoopNoticeForUnsupportedModel drives a "looping sound"
// request against a text-to-speech model that has no loop parameter, and
// confirms the deterministic caveat is appended to the persisted assistant turn
// (Route B), not silently dropped.
func TestHarnessAppendsLoopNoticeForUnsupportedModel(t *testing.T) {
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
	config.Providers.Fal.AudioModel = defaultFalAudioModel // TTS: no loop support
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	nonStreamCount := 0
	prepCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/api/openapi/") {
			// TTS schema — text + voice only, no loop parameter.
			return jsonResponse(`{"components":{"schemas":{"TtsInput":{"type":"object","required":["text"],"properties":{"text":{"type":"string"},"voice":{"type":"string","default":"Rachel"}}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			if req.Method == http.MethodPost {
				return jsonResponse(`{"request_id":"req-loop-1"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/status") {
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/requests/req-loop-1") {
				return jsonResponse(`{"audio":{"url":"https://queue.fal.run/generated.mp3","content_type":"audio/mpeg"}}`), nil
			}
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(string(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), 0xFF, 0xFB, 0x90, 0x00)))),
				Header: http.Header{"Content-Type": []string{"audio/mpeg"}}}, nil
		}
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				nonStreamCount++
				if nonStreamCount == 1 {
					return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"audio","toolTask":"Make a looping ambient sound.","reason":"The user asked for a loop."}`), nil
				}
				prepCalls++
				body := `{"brief":"Make a looping ambient sound.","needsTools":true,"reason":"audio","toolCalls":[{"name":"generate_audio","content":"gentle ambient rain","loop":true}]}`
				if prepCalls > 1 {
					body = `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`
				}
				return chatCompletion("harness-model", body), nil
			}
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is your ambient sound."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-loop-notice", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Make a looping ambient rain sound."},
		},
	})

	conversations, err := listConversations(config.Storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	detail, err := getConversation(config.Storage, conversations[0].ID)
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	assistant := detail.Turns[len(detail.Turns)-1]
	raw, _ := json.Marshal(assistant)
	if !strings.Contains(string(raw), "loop") || !strings.Contains(string(raw), "⚠️") {
		t.Fatalf("expected a loop caveat in the saved assistant turn, got: %s", raw)
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
