package main

import (
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
