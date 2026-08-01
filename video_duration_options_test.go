package main

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

// These cover the UI-facing duration-enum accessor (videoDurationOptions /
// App.ListFalVideoDurations), the entry point the Settings pickers call to fetch
// a model's accepted durations. The Seedance-vs-Kling "auto" distinction is
// already exercised at the resolver layer in video_duration_auto_test.go; here
// we assert the accessor surfaces the same enums verbatim and degrades safely
// when no schema is available.

// TestVideoDurationOptions_SeedanceIncludesAuto seeds the disk schema cache
// with the Seedance image-to-video schema (duration enum includes "auto" plus
// "4".."15") and asserts the helper returns that enum verbatim. "auto" is the
// value that only some video models accept, so the picker must offer it only
// where the schema actually lists it.
func TestVideoDurationOptions_SeedanceIncludesAuto(t *testing.T) {
	dir := t.TempDir()
	const model = "seedance/test"
	writeSchemaFixture(t, filepath.Join(dir, "schema-cache"), model, "seedance-2.0-image-to-video")

	opts := videoDurationOptions(context.Background(), &http.Client{}, dir, model)
	if opts == nil {
		t.Fatal("expected non-nil duration options for a schema with a duration enum")
	}
	if !contains(opts, "auto") {
		t.Errorf("expected Seedance duration options to include %q, got %v", "auto", opts)
	}
	if !contains(opts, "10") {
		t.Errorf("expected Seedance duration options to include %q, got %v", "10", opts)
	}
}

// TestVideoDurationOptions_KlingEnumVerbatim uses the Kling image-to-video
// schema, whose duration enum is exactly ["5","10"] (no "auto"). The accessor
// must return the schema's enum as-is — the UI reconciles the picker value into
// this set, so a Kling picker never offers "auto".
func TestVideoDurationOptions_KlingEnumVerbatim(t *testing.T) {
	dir := t.TempDir()
	const model = "kling/test"
	writeSchemaFixture(t, filepath.Join(dir, "schema-cache"), model, "kling-image-to-video")

	opts := videoDurationOptions(context.Background(), &http.Client{}, dir, model)
	want := []string{"5", "10"}
	if len(opts) != len(want) {
		t.Fatalf("expected %v, got %v", want, opts)
	}
	for i, v := range want {
		if opts[i] != v {
			t.Errorf("opts[%d] = %q, want %q (full: %v)", i, opts[i], v, opts)
		}
	}
	if contains(opts, "auto") {
		t.Errorf("Kling duration options must not include %q, got %v", "auto", opts)
	}
}

// TestVideoDurationOptions_HappyHorseIntegerEnum seeds the disk schema cache
// with the happy-horse image-to-video schema — whose duration is an INTEGER enum
// [3..15] (no "auto"). This is the model that 422'd in conv_4feb919a, and it
// exercises the enumStrings fix: numeric enum values must be stringified
// (rather than dropped by a failed v.(string) assertion) so the accessor
// returns them and the UI offers the right set.
func TestVideoDurationOptions_HappyHorseIntegerEnum(t *testing.T) {
	dir := t.TempDir()
	const model = "alibaba/happy-horse/image-to-video"
	writeSchemaFixture(t, filepath.Join(dir, "schema-cache"), model, "happy-horse-image-to-video")

	opts := videoDurationOptions(context.Background(), &http.Client{}, dir, model)
	if opts == nil {
		t.Fatal("expected non-nil duration options for an integer-typed duration enum")
	}
	want := []string{"3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15"}
	if len(opts) != len(want) {
		t.Fatalf("expected integer enum stringified to %v, got %v", want, opts)
	}
	for i, v := range want {
		if opts[i] != v {
			t.Errorf("opts[%d] = %q, want %q (full: %v)", i, opts[i], v, opts)
		}
	}
	if contains(opts, "auto") {
		t.Errorf("happy-horse duration options must not include %q, got %v", "auto", opts)
	}
}

// TestVideoDurationOptions_NilWhenSchemaUnavailable asserts the offline
// fallback: a model id with no cached schema and a client whose fetch will fail
// yields nil, never an error — callers (the bound ListFalVideoDurations and the
// UI) treat nil as "use the generic option set" rather than blocking.
func TestVideoDurationOptions_NilWhenSchemaUnavailable(t *testing.T) {
	dir := t.TempDir()
	opts := videoDurationOptions(context.Background(), &http.Client{}, dir, "no/such/model")
	if opts != nil {
		t.Fatalf("expected nil options for an unavailable schema, got %v", opts)
	}
}

// TestVideoDurationOptions_NilForBlankModel guards the TrimSpace check: a blank
// model id short-circuits to nil without touching the cache.
func TestVideoDurationOptions_NilForBlankModel(t *testing.T) {
	dir := t.TempDir()
	for _, blank := range []string{"", "   "} {
		if opts := videoDurationOptions(context.Background(), &http.Client{}, dir, blank); opts != nil {
			t.Errorf("expected nil for blank model %q, got %v", blank, opts)
		}
	}
}
