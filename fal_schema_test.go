package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestParseModelInputSchemaNesting(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fal-schemas", "minimax-speech-02-hd.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	schema, err := parseModelInputSchema(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	text, ok := schema.property("text")
	if !ok || text.Kind != schemaScalar {
		t.Fatalf("expected scalar text property, got %+v (ok=%v)", text, ok)
	}
	vs, ok := schema.property("voice_setting")
	if !ok || vs.Kind != schemaObject {
		t.Fatalf("expected object voice_setting, got %+v (ok=%v)", vs, ok)
	}
	if _, ok := vs.Nested["voice_id"]; !ok {
		t.Fatalf("expected nested voice_id, got %+v", vs.Nested)
	}
	of, _ := schema.property("output_format")
	if len(of.Enum) != 2 || of.Enum[0] != "url" {
		t.Fatalf("expected output_format enum [url hex], got %+v", of.Enum)
	}
}

func minimaxFetch(ctx context.Context, model string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", "fal-schemas", "minimax-speech-02-hd.json"))
}

func TestSchemaCacheFreshHitNoFetch(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	fetches := 0
	fetch := func(ctx context.Context, model string) ([]byte, error) {
		fetches++
		return minimaxFetch(ctx, model)
	}
	cache := &SchemaCache{dir: dir, ttl: 7 * 24 * time.Hour, now: func() time.Time { return now }, fetch: fetch}
	if s := cache.Get(context.Background(), "fal-ai/minimax/speech-02-hd"); s == nil {
		t.Fatal("expected schema on first get")
	}
	now = now.Add(24 * time.Hour) // within TTL
	if s := cache.Get(context.Background(), "fal-ai/minimax/speech-02-hd"); s == nil {
		t.Fatal("expected cached schema")
	}
	if fetches != 1 {
		t.Fatalf("expected 1 fetch (second served from disk), got %d", fetches)
	}
}

func TestSchemaCacheExpiredRefetches(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	fetches := 0
	fetch := func(ctx context.Context, model string) ([]byte, error) {
		fetches++
		return minimaxFetch(ctx, model)
	}
	cache := &SchemaCache{dir: dir, ttl: time.Hour, now: func() time.Time { return now }, fetch: fetch}
	cache.Get(context.Background(), "m")
	now = now.Add(2 * time.Hour) // past TTL
	cache.Get(context.Background(), "m")
	if fetches != 2 {
		t.Fatalf("expected refetch after expiry, got %d fetches", fetches)
	}
}

func TestSchemaCacheFetchFailUnavailable(t *testing.T) {
	dir := t.TempDir()
	cache := &SchemaCache{dir: dir, ttl: time.Hour, now: time.Now,
		fetch: func(ctx context.Context, model string) ([]byte, error) { return nil, errors.New("offline") }}
	if s := cache.Get(context.Background(), "m"); s != nil {
		t.Fatalf("expected nil schema when fetch fails and no cache, got %+v", s)
	}
}

func TestSchemaCacheCorruptFileIsMiss(t *testing.T) {
	dir := t.TempDir()
	fetches := 0
	cache := &SchemaCache{dir: dir, ttl: time.Hour, now: time.Now,
		fetch: func(ctx context.Context, model string) ([]byte, error) {
			fetches++
			return minimaxFetch(ctx, model)
		}}
	// Seed a corrupt cache file at the expected path.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.pathFor("m"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := cache.Get(context.Background(), "m"); s == nil {
		t.Fatal("expected schema after refetching over corrupt file")
	}
	if fetches != 1 {
		t.Fatalf("expected corrupt file to force one fetch, got %d", fetches)
	}
}

// TestSchemaCacheFetchUsesConfiguredKey guards the regression where
// newFalSchemaCache built a keyless FalClient, so do()'s empty-key guard
// rejected every schema fetch before any network call — silently forcing the
// resolver's generic fallback and dropping loop/voice/duration on every request.
func TestSchemaCacheFetchUsesConfiguredKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	var gotAuth string
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotAuth = req.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Status: "200 OK",
			Body:   io.NopCloser(strings.NewReader(`{"components":{"schemas":{"SfxInput":{"type":"object","required":["text"],"properties":{"text":{"type":"string"},"loop":{"type":"boolean"}}}}}}`)),
			Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}

	cache := newFalSchemaCache(httpClient, t.TempDir())
	schema := cache.Get(context.Background(), "fal-ai/elevenlabs/sound-effects/v2")
	if schema == nil {
		t.Fatal("expected a schema; fetch must succeed when a fal key is configured")
	}
	if _, ok := schema.property("loop"); !ok {
		t.Fatal("expected loop property parsed from the fetched schema")
	}
	if gotAuth != "Key fal-test-key" {
		t.Fatalf("expected request to carry the configured key, got Authorization=%q", gotAuth)
	}
}
