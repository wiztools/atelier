package main

import (
	"strings"
	"testing"
)

// === Video duration "auto" option ===
//
// Seedance supports duration "auto" (the model sizes the clip to the prompt),
// but other video models don't — Kling's duration enum is just ["5","10"]. The
// resolver must pass "auto" through to models that accept it and DROP it (with a
// notice) for models that don't, so the UI can offer "auto" unconditionally
// without 422ing on non-Seedance models.

// TestResolveVideoBodySeedanceAutoDurationPassesThrough asserts that against the
// Seedance schema (whose duration enum includes "auto"), the value is sent as-is
// with no notice — letting Seedance size the clip to fit narration.
func TestResolveVideoBodySeedanceAutoDurationPassesThrough(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "seedance-2.0-image-to-video"),
		VideoGenerateRequest{
			Model:    "bytedance/seedance-2.0/image-to-video",
			Prompt:   "a kid narrating a story",
			Duration: "auto",
		},
		builtinFalOverrides())
	if body["duration"] != "auto" {
		t.Fatalf("duration = %v, want \"auto\" passed through for Seedance", body["duration"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices when Seedance accepts the duration, got %v", notices)
	}
}

// TestResolveVideoBodyAutoDurationDroppedForKling is the safety regression: when
// "auto" is sent to a model whose duration enum doesn't include it (Kling:
// ["5","10"]), the resolver must drop it with a notice rather than passing it
// through and 422ing at fal. The request still runs — the model picks its own
// default duration.
func TestResolveVideoBodyAutoDurationDroppedForKling(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "kling-image-to-video"),
		VideoGenerateRequest{
			Model:    "fal-ai/kling-video/v2/master/image-to-video",
			Prompt:   "a drone shot over a forest",
			Duration: "auto",
		},
		builtinFalOverrides())
	if _, present := body["duration"]; present {
		t.Fatalf("duration must be dropped for Kling (no \"auto\" in enum), got %v", body["duration"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one duration-dropped notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "does not accept duration") || !strings.Contains(notices[0], "auto") {
		t.Fatalf("notice should name the rejected duration value, got %q", notices[0])
	}
}

// TestResolveVideoBodyNumericDurationStillWorksForKling asserts the enum-guard
// doesn't break the normal case: a numeric duration that IS in Kling's enum
// passes through unchanged with no notice.
func TestResolveVideoBodyNumericDurationStillWorksForKling(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "kling-image-to-video"),
		VideoGenerateRequest{
			Model:    "fal-ai/kling-video/v2/master/image-to-video",
			Prompt:   "a drone shot over a forest",
			Duration: "10",
		},
		builtinFalOverrides())
	if body["duration"] != "10" {
		t.Fatalf("duration = %v, want \"10\" passed through", body["duration"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for a valid Kling duration, got %v", notices)
	}
}

// TestValueAllowedByEnumDirect covers the helper standalone.
func TestValueAllowedByEnumDirect(t *testing.T) {
	enumProp := SchemaProperty{Enum: []string{"auto", "5", "10"}}
	noEnumProp := SchemaProperty{}
	cases := []struct {
		name string
		prop SchemaProperty
		in   string
		want bool
	}{
		{"auto in enum", enumProp, "auto", true},
		{"5 in enum", enumProp, "5", true},
		{"8 not in enum", enumProp, "8", false},
		{"empty enum accepts all", noEnumProp, "anything", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := valueAllowedByEnum(c.prop, c.in); got != c.want {
				t.Fatalf("valueAllowedByEnum(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
