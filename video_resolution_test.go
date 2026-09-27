package main

import (
	"context"
	"strings"
	"testing"
)

// === Video resolution param ===
//
// Resolution tiers are model-dependent: Seedance accepts 480p/720p/1080p/4k,
// happy-horse only 720p/1080p, and Kling/Veo have no resolution control at all.
// The resolver must pass a supported tier through, DROP an unsupported one with
// a notice (rather than 422ing at fal), and surface a "no resolution control"
// notice for models that lack the field entirely. These mirror the duration
// enum-guard tests in video_duration_auto_test.go.

// TestResolveVideoBodySeedanceResolutionPassesThrough asserts that against the
// Seedance schema (resolution enum 480p/720p/1080p/4k), a supported tier is sent
// as-is with no notice. Seedance is the model whose "4k" tier most distinguishes
// it, so a 1080p request exercises the clean pass-through path.
func TestResolveVideoBodySeedanceResolutionPassesThrough(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "seedance-2.0-image-to-video"),
		VideoGenerateRequest{
			Model:      "bytedance/seedance-2.0/image-to-video",
			Prompt:     "a drone shot over a misty forest",
			Resolution: "1080p",
		},
		builtinFalOverrides())
	if body["resolution"] != "1080p" {
		t.Fatalf("resolution = %v, want \"1080p\" passed through for Seedance", body["resolution"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices when Seedance accepts the resolution, got %v", notices)
	}
}

// TestResolveVideoBodyResolutionDroppedForHappyHorse is the safety regression:
// when a tier the model's enum doesn't list is sent (4k into happy-horse, whose
// enum is only 720p/1080p), the resolver must drop it with a notice rather than
// passing it through and 422ing at fal. The request still runs — the model picks
// its own default resolution.
func TestResolveVideoBodyResolutionDroppedForHappyHorse(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "happy-horse-image-to-video"),
		VideoGenerateRequest{
			Model:      "alibaba/happy-horse/image-to-video",
			Prompt:     "a parent and child at dawn",
			Resolution: "4k",
			Images:     []string{"data:image/png;base64,AAAA"},
		},
		builtinFalOverrides())
	if _, present := body["resolution"]; present {
		t.Fatalf("resolution must be dropped for happy-horse (no \"4k\" in enum), got %v", body["resolution"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one resolution-dropped notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "does not accept resolution") || !strings.Contains(notices[0], "4k") {
		t.Fatalf("notice should name the rejected resolution value, got %q", notices[0])
	}
}

// TestResolveVideoBodyResolutionNoControlNotice asserts that a model with no
// resolution field in its schema (Kling image-to-video) surfaces a "no resolution
// control" notice and sends nothing — distinct from the enum-rejection case above.
// The user learns the knob doesn't apply rather than that the value was wrong.
func TestResolveVideoBodyResolutionNoControlNotice(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "kling-image-to-video"),
		VideoGenerateRequest{
			Model:      "fal-ai/kling-video/v2/master/image-to-video",
			Prompt:     "a drone shot over a forest",
			Resolution: "1080p",
		},
		builtinFalOverrides())
	if _, present := body["resolution"]; present {
		t.Fatalf("resolution must not be sent when the model has no resolution control, got %v", body["resolution"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one no-resolution-control notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "no resolution control") {
		t.Fatalf("notice should explain the model lacks resolution control, got %q", notices[0])
	}
}

// TestResolveVideoBodyResolutionOmittedSendsNothing pins the default behavior:
// when Resolution is empty (the common case — the planner didn't set it), the
// resolver sends no resolution field and emits no notice, so the model uses its
// own default tier. This is what every video call that doesn't ask for a specific
// resolution must do.
func TestResolveVideoBodyResolutionOmittedSendsNothing(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "seedance-2.0-image-to-video"),
		VideoGenerateRequest{
			Model:  "bytedance/seedance-2.0/image-to-video",
			Prompt: "a drone shot over a misty forest",
		},
		builtinFalOverrides())
	if _, present := body["resolution"]; present {
		t.Fatalf("resolution must not be sent when omitted, got %v", body["resolution"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices when resolution is omitted, got %v", notices)
	}
}

// TestResolveVideoBodyResolutionCaseInsensitiveEnumMatch is the minimax
// regression: the planner-facing tiers are lowercase ("480p", "1080p" — the
// tool description's own examples) while minimax/h3-max's resolution enum is
// uppercase ["480P","768P","1080P"], and the enum guard used to be an
// exact-case match — so every planner-requested tier was dropped as
// out-of-enum and nobody could actually select 1080P on minimax. The match is
// now case-insensitive and sends the enum's own spelling.
func TestResolveVideoBodyResolutionCaseInsensitiveEnumMatch(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "minimax-h3-max-reference-to-video"),
		VideoGenerateRequest{
			Model:      "minimax/h3-max/reference-to-video",
			Prompt:     "the character walks through the house",
			Resolution: "1080p",
		},
		builtinFalOverrides())
	if body["resolution"] != "1080P" {
		t.Fatalf("resolution = %v, want the enum's own spelling \"1080P\"", body["resolution"])
	}
	if len(notices) != 0 {
		t.Fatalf("a case-only mismatch is accepted, want no notices, got %v", notices)
	}
}

// TestResolveVideoBodyResolutionCaseMismatchStillDropped pins the boundary:
// case-insensitivity cannot invent a member. minimax has no 720p tier (its
// middle tier is 768P), so "720p" must still be dropped with a notice rather
// than silently coerced onto a tier the user never asked for.
func TestResolveVideoBodyResolutionCaseMismatchStillDropped(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "minimax-h3-max-reference-to-video"),
		VideoGenerateRequest{
			Model:      "minimax/h3-max/reference-to-video",
			Prompt:     "the character walks through the house",
			Resolution: "720p",
		},
		builtinFalOverrides())
	if _, present := body["resolution"]; present {
		t.Fatalf("resolution must stay dropped when no enum member matches, got %v", body["resolution"])
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "does not accept resolution") || !strings.Contains(notices[0], "720p") {
		t.Fatalf("expected one resolution-dropped notice naming 720p, got %v", notices)
	}
}

// === Config default precedence (Settings → Generation → Video) ===
//
// Resolution now has a persisted config default beside the planner's per-call
// knob, mirroring the image tool's tier rule: an explicit resolution on the
// call wins; otherwise the configured tier applies; an unset (empty) config
// keeps the pre-setting behavior — the model's own default. The config value
// is canonicalized at the executor because per-conversation overrides and
// tests build configs without mergeAppConfig's normalization pass.

// TestNormalizeVideoResolutionTier pins the canonical set: the four tiers the
// Settings picker offers, case-insensitive and trimmed; anything else —
// including tiers other models might speak ("1440p") and image vocabulary
// ("2k") — normalizes to "" (let the model choose) rather than riding a
// request the resolvers would notice about every turn.
func TestNormalizeVideoResolutionTier(t *testing.T) {
	cases := map[string]string{
		"480p":    "480p",
		"1080p":   "1080p",
		"  4K ":   "4k",
		"1080P":   "1080p",
		"":        "",
		"2k":      "",
		"1440p":   "",
		"auto":    "",
		"1080 px": "",
	}
	for input, want := range cases {
		if got := normalizeVideoResolutionTier(input); got != want {
			t.Fatalf("normalizeVideoResolutionTier(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestGenerateVideoResolutionPrecedence drives the generate_video executor with
// a stubbed gateway to pin the request-building precedence: explicit call tier
// beats config, config fills an omitted tier, and a junk config value
// (normalize-empty) leaves the request unset instead of forwarding garbage.
func TestGenerateVideoResolutionPrecedence(t *testing.T) {
	cases := []struct {
		name          string
		callTier      string
		configTier    string
		wantRequested string
	}{
		{name: "explicit call tier beats config", callTier: "4k", configTier: "1080p", wantRequested: "4k"},
		{name: "config tier fills an omitted call tier", callTier: "", configTier: "1080p", wantRequested: "1080p"},
		{name: "unset config keeps the model default", callTier: "", configTier: "", wantRequested: ""},
		{name: "junk config tier is dropped, not forwarded", callTier: "", configTier: "2k", wantRequested: ""},
		{name: "uppercase config tier is canonicalized", callTier: "", configTier: " 720P ", wantRequested: "720p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := defaultAppConfig()
			config.Providers.Fal.VideoModel = "bytedance/seedance-2.0/text-to-video"
			config.Generation.Video.Resolution = tc.configTier

			var gotReq VideoGenerateRequest
			def := videoGenerationToolDefinition(AppConfig{}, false)
			exec := HarnessToolExecutionContext{
				Config: config,
				GenerateVideo: func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
					gotReq = req
					return GeneratedVideo{Data: []byte("mp4"), MimeType: "video/mp4", SourceURL: "https://example.com/v.mp4"}, nil
				},
			}
			_, _, err := def.Execute(context.Background(), exec, HarnessToolCall{
				Name:       "generate_video",
				Content:    "a drone shot over a misty forest",
				Resolution: tc.callTier,
			})
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if gotReq.Resolution != tc.wantRequested {
				t.Fatalf("request resolution = %q, want %q", gotReq.Resolution, tc.wantRequested)
			}
		})
	}
}

// TestMergeAppConfigVideoResolutionCanonicalized pins the merge-side pass: a
// hand-edited config.json tier is lowercased onto the canonical set, and an
// unknown value becomes empty — empty means "let the model choose", so there
// is no defaults-fill the way Duration/AspectRatio have one.
func TestMergeAppConfigVideoResolutionCanonicalized(t *testing.T) {
	cases := map[string]string{
		"1080P": "1080p",
		"4k":    "4k",
		"2k":    "",
		"hi":    "",
		"":      "",
	}
	for input, want := range cases {
		config := mergeAppConfig(AppConfig{Generation: ConfigGeneration{Video: ConfigVideoGeneration{Resolution: input}}})
		if got := config.Generation.Video.Resolution; got != want {
			t.Fatalf("mergeAppConfig resolution %q = %q, want %q", input, got, want)
		}
	}
}
