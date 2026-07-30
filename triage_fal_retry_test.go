package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- Triage: mis-typed field coercion ---
//
// conv_4fcc40eb3398a9bb21cb7d00 failed end-to-end because the triage model
// emitted toolTask as an object, json.Unmarshal rejected it, and triage fell
// back to vision+tools — silently disabling the narration-routing hint. These
// tests assert a mis-typed scalar field is coerced to its JSON text rather than
// sinking the whole decision.

// TestDecodeTriageDecisionCoercesObjectToolTask is the exact regression: a
// toolTask emitted as a JSON object must not fail the parse. It becomes the
// object's compact JSON text, and the rest of the decision stays usable.
func TestDecodeTriageDecisionCoercesObjectToolTask(t *testing.T) {
	decision, err := decodeTriageDecision(`{"needsTools":true,"responseMode":"video","toolTask":{"task":"generate_video","mode":"audio"},"reason":"narration over video"}`)
	if err != nil {
		t.Fatalf("object-valued toolTask must not fail the parse: %v", err)
	}
	if !decision.NeedsTools {
		t.Fatal("needsTools should still decode")
	}
	if decision.ResponseMode != "video" {
		t.Fatalf("responseMode should still decode, got %q", decision.ResponseMode)
	}
	if decision.ToolTask == "" {
		t.Fatal("toolTask should be coerced to non-empty JSON text, not dropped")
	}
	if !strings.Contains(decision.ToolTask, "generate_video") {
		t.Fatalf("coerced toolTask should preserve the object's content, got %q", decision.ToolTask)
	}
	if decision.Reason != "narration over video" {
		t.Fatalf("reason should still decode, got %q", decision.Reason)
	}
}

// TestDecodeTriageDecisionCoercesEachNonStringType asserts every non-string
// JSON type coerces to a non-empty string rather than failing.
func TestDecodeTriageDecisionCoercesEachNonStringType(t *testing.T) {
	cases := map[string]string{
		"array":  `{"needsTools":true,"responseMode":"text","toolTask":["a","b"],"reason":"x"}`,
		"number": `{"needsTools":true,"responseMode":"text","toolTask":42,"reason":"x"}`,
		"bool":   `{"needsTools":true,"responseMode":"text","toolTask":true,"reason":"x"}`,
		"null":   `{"needsTools":true,"responseMode":"text","toolTask":null,"reason":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			decision, err := decodeTriageDecision(body)
			if err != nil {
				t.Fatalf("%s toolTask must not fail the parse: %v", name, err)
			}
			// null coerces to "" (a genuine absence); everything else is non-empty.
			if name != "null" && decision.ToolTask == "" {
				t.Fatalf("%s toolTask should coerce to non-empty text", name)
			}
		})
	}
}

// TestDecodeTriageDecisionStillRequiresValidJSON asserts a top-level parse
// failure (prose, empty) still fails — coercion only applies to structurally-
// sound JSON with a mis-typed field, not to garbage. A truncated object whose
// routing fields are complete is salvaged (see TestDecodeTriageSalvagesTruncated).
func TestDecodeTriageDecisionStillRequiresValidJSON(t *testing.T) {
	for _, bad := range []string{
		"not json at all",
		``,
	} {
		if _, err := decodeTriageDecision(bad); err == nil {
			t.Fatalf("expected parse error for %q", bad)
		}
	}
}

// TestDecodeTriageSalvagesTruncated covers the conv_2d1be19a fix: when triage
// exhausted its output budget mid-JSON (the verbose toolTask is the usual
// victim), the routing-critical fields that appeared before the truncation are
// salvaged rather than discarding the whole decision and fail-safeing to text —
// which would drop responseMode "image"/"video"/"audio" and sink the turn.
func TestDecodeTriageSalvagesTruncated(t *testing.T) {
	// Truncated mid-toolTask: needsTools and responseMode are complete.
	truncated := `{"needsTools":true,"reason":"generate two images","responseMode":"image","toolTask":"The user wants to generate two images,`
	decision, err := decodeTriageDecision(truncated)
	if err != nil {
		t.Fatalf("truncated JSON with complete routing fields should be salvaged, got error: %v", err)
	}
	if !decision.NeedsTools {
		t.Errorf("needsTools = false, want true salvaged before truncation")
	}
	if decision.ResponseMode != "image" {
		t.Errorf("responseMode = %q, want image salvaged before truncation", decision.ResponseMode)
	}
}

// TestDecodeTriageDecisionStillRequiresBooleanNeedsTools asserts that while
// string fields are coerced, a mis-typed needsTools (the field the harness
// branches on) is still a hard error — a wrong boolean value is worse than a
// fail-safe fallback.
func TestDecodeTriageDecisionStillRequiresBooleanNeedsTools(t *testing.T) {
	if _, err := decodeTriageDecision(`{"needsTools":"yes","responseMode":"text","toolTask":"x","reason":"x"}`); err == nil {
		t.Fatal("a string-valued needsTools must still fail the parse")
	}
}

// TestCoerceJSONStringDirect covers the helper directly.
func TestCoerceJSONStringDirect(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain string", `"hello"`, "hello"},
		{"number", `42`, "42"},
		{"bool true", `true`, "true"},
		{"bool false", `false`, "false"},
		{"object", `{"a":"b"}`, `{"a":"b"}`},
		{"array", `[1,2,3]`, "[1,2,3]"},
		{"empty raw", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := coerceJSONString(json.RawMessage(c.in))
			if got != c.want {
				t.Fatalf("coerceJSONString(%s) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- fal: transient-5xx retry ---
//
// conv_4fcc40eb failed at lip_sync because fal's result fetch returned a 500
// "downstream_service_error". These tests assert FalClient.do retries a
// transient 5xx once before surfacing it, and surfaces it after the retry is
// exhausted.

// errResp builds a fal-shaped JSON error response with the given status/body.
func errResp(status int, statusText, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     statusText,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

const downstreamSvcErrBody = `{"detail":[{"msg":"Downstream service error","type":"downstream_service_error"}]}`
const okResultBody = `{"video":{"url":"https://example.com/out.mp4"},"audio":{"url":"https://example.com/out.mp3"}}`

// TestFalDoRetriesTransient500OnResultFetch asserts a single 500 on the result
// fetch is retried and the overall call succeeds.
func TestFalDoRetriesTransient500OnResultFetch(t *testing.T) {
	hits := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		// Only count the result GET (no /status suffix); give everything else OK.
		if !(req.Method == http.MethodGet && !strings.HasSuffix(req.URL.Path, "/status")) {
			return jsonResp(`{"request_id":"req","status":"COMPLETED"}`), nil
		}
		hits++
		if hits == 1 {
			return errResp(http.StatusInternalServerError, "500 Internal Server Error", downstreamSvcErrBody), nil
		}
		return jsonResp(okResultBody), nil
	}))
	resp, err := client.do(context.Background(), falQueueBaseURL, http.MethodGet, "/some-model/requests/abc", nil)
	if err != nil {
		t.Fatalf("a single transient 500 should be retried, got error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d", resp.StatusCode)
	}
}

// TestFalDoSurfaces500AfterRetryExhausted asserts that two consecutive 500s (the
// original failure plus the one retry) surface as an error rather than looping.
func TestFalDoSurfaces500AfterRetryExhausted(t *testing.T) {
	hits := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		hits++
		return errResp(http.StatusInternalServerError, "500 Internal Server Error", downstreamSvcErrBody), nil
	}))
	_, err := client.do(context.Background(), falQueueBaseURL, http.MethodGet, "/some-model/requests/abc", nil)
	if err == nil {
		t.Fatal("expected the 500 to surface after the retry is exhausted")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should mention the 500, got %v", err)
	}
	if !strings.Contains(err.Error(), "downstream_service_error") {
		t.Fatalf("error should preserve the fal error body, got %v", err)
	}
	if hits != 2 {
		t.Fatalf("expected exactly 2 attempts (1 original + 1 retry), got %d", hits)
	}
}

// TestFalDoDoesNotRetry4xx asserts non-transient errors (4xx) are NOT retried —
// an auth or validation failure should surface immediately.
func TestFalDoDoesNotRetry4xx(t *testing.T) {
	hits := 0
	client := newFalTestClient(t, falHandler(func(req *http.Request) (*http.Response, error) {
		hits++
		return errResp(http.StatusUnauthorized, "401 Unauthorized", `{"detail":"bad key"}`), nil
	}))
	_, err := client.do(context.Background(), falQueueBaseURL, http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected the 401 to surface")
	}
	if hits != 1 {
		t.Fatalf("4xx must not be retried, got %d hits", hits)
	}
}
