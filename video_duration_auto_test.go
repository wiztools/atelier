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

// === Integer-typed duration ===
//
// happy-horse (alibaba/happy-horse/image-to-video) declares duration as an
// INTEGER enum [3..15], unlike Kling/Veo/Seedance which declare it as a string.
// Two bugs in conv_4feb919a stemmed from this: (1) "auto" was offered because
// numeric enum values were silently dropped during parsing, leaving the enum
// empty and bypassing the guard; (2) a valid "10" was sent as a JSON STRING and
// 422'd because fal wanted integer 10. These tests pin both fixes: numeric
// enums survive parsing (so the guard catches "auto"), and an integer-typed
// duration is coerced to a JSON number on the wire.

// TestResolveVideoBodyIntegerDurationSentAsNumber asserts that against the
// happy-horse schema (duration type: integer, enum [3..15]), a valid numeric
// duration string is coerced to a JSON int — not left as the string "10" that
// fal rejects. No notice, since 10 is in the enum.
func TestResolveVideoBodyIntegerDurationSentAsNumber(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "happy-horse-image-to-video"),
		VideoGenerateRequest{
			Model:    "alibaba/happy-horse/image-to-video",
			Prompt:   "a parent and child at dawn",
			Duration: "10",
			Images:   []string{"data:image/png;base64,AAAA"},
		},
		builtinFalOverrides())
	got, ok := body["duration"]
	if !ok {
		t.Fatalf("expected duration in body, got %v", body)
	}
	// Must be an int (JSON number), NOT the string "10". A string here is the
	// exact regression that 422'd at fal in conv_4feb919a.
	n, isInt := got.(int)
	if !isInt {
		t.Fatalf("duration must be int for an integer-typed schema, got %T (%v)", got, got)
	}
	if n != 10 {
		t.Fatalf("duration = %d, want 10", n)
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices for a valid integer duration, got %v", notices)
	}
}

// TestResolveVideoBodyAutoDroppedForIntegerEnum asserts that once numeric enum
// values survive parsing, the enum guard catches a non-numeric "auto" against
// an integer enum and drops it with a notice (rather than sending it and 422ing
// or, worse, coercing "auto" to a number). This is bug (1) from conv_4feb919a.
func TestResolveVideoBodyAutoDroppedForIntegerEnum(t *testing.T) {
	body, notices, _ := resolveVideoBody(loadSchema(t, "happy-horse-image-to-video"),
		VideoGenerateRequest{
			Model:    "alibaba/happy-horse/image-to-video",
			Prompt:   "a parent and child at dawn",
			Duration: "auto",
			Images:   []string{"data:image/png;base64,AAAA"},
		},
		builtinFalOverrides())
	if _, present := body["duration"]; present {
		t.Fatalf("duration must be dropped for an integer enum that doesn't list %q, got %v", "auto", body["duration"])
	}
	if len(notices) != 1 {
		t.Fatalf("expected one duration-dropped notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "does not accept duration") || !strings.Contains(notices[0], "auto") {
		t.Fatalf("notice should name the rejected duration value, got %q", notices[0])
	}
}

// TestNumericEnumSurvivesParsing pins the enumStrings fix standalone: a schema
// whose enum is JSON numbers [3,4,5] (decoded by encoding/json as float64)
// must parse to the string slice ["3","4","5"], not be silently dropped. This
// is the foundation that makes both the enum guard and the UI duration picker
// work for integer-typed models.
func TestNumericEnumSurvivesParsing(t *testing.T) {
	schema := loadSchema(t, "happy-horse-image-to-video")
	prop, ok := schema.property("duration")
	if !ok {
		t.Fatal("expected a duration property in the happy-horse schema")
	}
	if prop.Type != "integer" {
		t.Errorf("duration type = %q, want %q", prop.Type, "integer")
	}
	want := []string{"3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15"}
	if len(prop.Enum) != len(want) {
		t.Fatalf("numeric enum was not stringified: got %v (len %d), want %v", prop.Enum, len(prop.Enum), want)
	}
	for i, v := range want {
		if prop.Enum[i] != v {
			t.Errorf("enum[%d] = %q, want %q (full: %v)", i, prop.Enum[i], v, prop.Enum)
		}
	}
}
