package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// === Fix 1: lip_sync force-hosts media ===
//
// conv_b54423f43ab17a060948e74f (and the two before it) failed at lip_sync
// with a deterministic fal 500 "downstream_service_error" because sync-lipsync
// v3 rejects inline data:image URIs at the downstream layer. resolveMediaURL
// kept small images inline (≤1 MB), so the data URI reached fal unmodified.
// resolveMediaURLHosted force-uploads every payload regardless of size. These
// tests mirror the existing resolveMediaURL tests (fal_client_test.go:711+).

// TestResolveMediaURLHostedUploadsSmallPayload is the headline regression: a
// small payload that resolveMediaURL would keep inline is uploaded by the
// hosted variant. This is the exact size tier the lipsync 500 came from.
func TestResolveMediaURLHostedUploadsSmallPayload(t *testing.T) {
	tokenIssued := false
	fileUploaded := false
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(req.URL.String(), "storage/auth/token"):
			tokenIssued = true
			return jsonResp(`{"token":"cdn-test-token","token_type":"Bearer","base_url":"https://v3.test.fal.media"}`), nil
		case strings.HasSuffix(req.URL.Path, "/files/upload"):
			fileUploaded = true
			return jsonResp(`{"access_url":"https://v3.test.fal.media/files/abc/face.png"}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	}))

	// tinyPNG is well under the 1 MB inline threshold; the non-hosted variant
	// would keep it inline. The hosted variant must upload it.
	got, err := client.resolveMediaURLHosted(context.Background(), tinyPNG, "image/png", "face-image.png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://v3.test.fal.media/files/abc/face.png" {
		t.Fatalf("resolveMediaURLHosted(small) = %q, want the CDN access_url", got)
	}
	if !tokenIssued || !fileUploaded {
		t.Fatal("expected a CDN token + file upload for the force-host path")
	}
}

// TestResolveMediaURLHostedPassesThroughHostedURL asserts an already-hosted
// http(s) URL is NOT re-uploaded — the force-host rule only applies to inline
// payloads.
func TestResolveMediaURLHostedPassesThroughHostedURL(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("no HTTP calls expected for a hosted URL: %s %s", req.Method, req.URL)
		return nil, nil
	}))
	got, err := client.resolveMediaURLHosted(context.Background(), "https://v3.fal.media/files/abc/clip.mp4", "video/mp4", "x.mp4")
	if err != nil || got != "https://v3.fal.media/files/abc/clip.mp4" {
		t.Fatalf("resolveMediaURLHosted(hosted) = %q, %v — want pass-through", got, err)
	}
}

// TestResolveMediaURLHostedFallsBackToInlineOnUploadFailure asserts the hosted
// variant preserves the fail-soft contract: a CDN upload failure falls back to
// the inline data URI rather than dropping the media or erroring.
func TestResolveMediaURLHostedFallsBackToInlineOnUploadFailure(t *testing.T) {
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.String(), "storage/auth/token") {
			return jsonResp(`{"token":"t","token_type":"Bearer","base_url":"https://v3.test.fal.media"}`), nil
		}
		return &http.Response{StatusCode: 500, Status: "500 Internal Server Error",
			Body: io.NopCloser(strings.NewReader(`{"detail":"cdn down"}`)), Header: http.Header{}}, nil
	}))
	got, err := client.resolveMediaURLHosted(context.Background(), tinyPNG, "image/png", "face.png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "data:image/") {
		t.Fatalf("expected inline data URI fallback on upload failure, got %q", got[:min(40, len(got))])
	}
}

// === Fix 2 + 3: planner media routing guidance ===

// newPlannerRegistry builds a registry with the audio-capable or non-capable
// generate_video tool so the planner guidance tests can exercise both gates.
func newPlannerRegistry(audioCapable bool) HarnessToolRegistry {
	return newHarnessToolRegistry([]HarnessToolDefinition{videoGenerationToolDefinition(audioCapable)})
}

// TestPlannerMediaRoutingGuidancePresentWhenCapable asserts the routing rule
// appears in BOTH planner prompt variants when the video model is audio-capable
// — the gap behind conv_b54423f43ab17a060948e74f, where triage routed correctly
// but the planner ignored it and ran generate_speech + lip_sync.
func TestPlannerMediaRoutingGuidancePresentWhenCapable(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	registry := newPlannerRegistry(true)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "narrate this"}}}

	for name, prompt := range map[string]string{
		"json":   engine.plannerSystemPrompt(registry, req, nil, "", "", false),
		"native": engine.plannerSystemPromptNative(registry, req, nil, "", "", false),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, "call generate_video ONCE") {
				t.Fatalf("%s planner prompt should include the narration routing rule", name)
			}
			if !strings.Contains(prompt, "Do NOT chain generate_speech + lip_sync") {
				t.Fatalf("%s planner prompt should explicitly forbid the chain for narration", name)
			}
		})
	}
}

// TestPlannerMediaRoutingGuidanceAbsentWhenNotCapable asserts the rule is
// omitted when the video model cannot produce audio — the chain is the correct
// path then, so forbidding it would be wrong.
func TestPlannerMediaRoutingGuidanceAbsentWhenNotCapable(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	registry := newPlannerRegistry(false)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "narrate this"}}}

	for name, prompt := range map[string]string{
		"json":   engine.plannerSystemPrompt(registry, req, nil, "", "", false),
		"native": engine.plannerSystemPromptNative(registry, req, nil, "", "", false),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(prompt, "call generate_video ONCE") {
				t.Fatalf("%s planner prompt must omit the routing rule when video is not audio-capable", name)
			}
		})
	}
}

// TestPlannerMediaRoutingGuidanceIncludesFallbackRule asserts Fix 3: the
// guidance tells the planner to fall back to generate_video if lip_sync fails,
// so a single tool failure does not sink the turn when an alternative exists.
func TestPlannerMediaRoutingGuidanceIncludesFallbackRule(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	registry := newPlannerRegistry(true)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "narrate this"}}}
	prompt := engine.plannerSystemPrompt(registry, req, nil, "", "", false)
	if !strings.Contains(prompt, "If a lip_sync call fails") {
		t.Fatalf("planner guidance should include the lip_sync-failure fallback rule")
	}
	if !strings.Contains(prompt, "single generate_video call") {
		t.Fatalf("planner guidance should name generate_video as the fallback")
	}
}

// TestPlannerMediaRoutingGuidanceHelperDirect covers the helper standalone,
// including the empty-string return when not audio-capable.
func TestPlannerMediaRoutingGuidanceHelperDirect(t *testing.T) {
	if plannerMediaRoutingGuidance(newPlannerRegistry(false)) != "" {
		t.Fatal("guidance must be empty when video is not audio-capable")
	}
	got := plannerMediaRoutingGuidance(newPlannerRegistry(true))
	if !strings.Contains(got, "generate_video ONCE") {
		t.Fatalf("capable guidance should mention generate_video ONCE, got %q", got)
	}
}
