package main

import (
	"strings"
	"testing"
)

// === Video frame-rate (fps) param ===
//
// Frame rate is model-dependent in the same way resolution is: most video models
// have no fps input at all, and those that do expose it as either a free integer
// or a fixed enum (e.g. [24,30,60]). The resolver must pass a supported value
// through (coerced to an integer when the schema declares one), DROP an
// out-of-enum value with a notice rather than 422ing at fal, and surface a "no
// frame-rate control" notice for models that lack the field entirely. These
// mirror the resolution enum-guard tests in video_resolution_test.go. The
// fps-capable fixture is synthetic — none of the currently-configured fal video
// models expose an fps input — but the resolver path is the same as resolution's.

// TestResolveVideoBodyFpsPassesThrough asserts that against a model whose schema
// declares an integer fps enum [24,30,60], a supported value is coerced to an
// integer and sent as-is with no notice.
func TestResolveVideoBodyFpsPassesThrough(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "fps-capable-video"),
		VideoGenerateRequest{
			Model:  "acme/fps-capable/text-to-video",
			Prompt: "a drone shot over a misty forest",
			FPS:    "30",
		},
		builtinFalOverrides())
	got, present := body["fps"]
	if !present {
		t.Fatalf("fps must be sent when the model accepts it, got body %v", body)
	}
	if n, ok := got.(int); !ok || n != 30 {
		t.Fatalf("fps = %v (%T), want int 30 coerced from \"30\"", got, got)
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices when the model accepts the frame rate, got %v", notices)
	}
}

// TestResolveVideoBodyFpsDroppedOutOfEnum is the safety regression: when a value
// the model's enum doesn't list is sent ("120" into the [24,30,60] model), the
// resolver must drop it with a notice rather than passing it through and 422ing
// at fal. The request still runs — the model picks its own default frame rate.
func TestResolveVideoBodyFpsDroppedOutOfEnum(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "fps-capable-video"),
		VideoGenerateRequest{
			Model:  "acme/fps-capable/text-to-video",
			Prompt: "a drone shot over a misty forest",
			FPS:    "120",
		},
		builtinFalOverrides())
	if _, present := body["fps"]; present {
		t.Fatalf("fps must be dropped when not in the model's enum, got %v", body["fps"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one fps-dropped notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "does not accept frame rate") || !strings.Contains(notices[0], "120") {
		t.Fatalf("notice should name the rejected frame rate value, got %q", notices[0])
	}
}

// TestResolveVideoBodyFpsNoControlNotice asserts that a model with no fps field
// in its schema (Kling image-to-video) surfaces a "no frame-rate control" notice
// and sends nothing — distinct from the enum-rejection case above. The user
// learns the knob doesn't apply rather than that the value was wrong.
func TestResolveVideoBodyFpsNoControlNotice(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "kling-image-to-video"),
		VideoGenerateRequest{
			Model:  "fal-ai/kling-video/v2/master/image-to-video",
			Prompt: "a drone shot over a forest",
			FPS:    "30",
		},
		builtinFalOverrides())
	if _, present := body["fps"]; present {
		t.Fatalf("fps must not be sent when the model has no frame-rate control, got %v", body["fps"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one no-frame-rate-control notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "no frame-rate control") {
		t.Fatalf("notice should explain the model lacks frame-rate control, got %q", notices[0])
	}
}

// TestResolveVideoBodyFpsOmittedSendsNothing pins the default behavior: when FPS
// is empty (the common case — the planner didn't set it), the resolver sends no
// fps field and emits no notice, so the model uses its own default frame rate.
func TestResolveVideoBodyFpsOmittedSendsNothing(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "fps-capable-video"),
		VideoGenerateRequest{
			Model:  "acme/fps-capable/text-to-video",
			Prompt: "a drone shot over a misty forest",
		},
		builtinFalOverrides())
	if _, present := body["fps"]; present {
		t.Fatalf("fps must not be sent when omitted, got %v", body["fps"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices when fps is omitted, got %v", notices)
	}
}
