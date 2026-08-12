package main

import (
	"context"
	"encoding/json"
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

// TestParseModelInputSchemaArray verifies arrays (e.g. nano-banana/edit's
// image_urls) parse to Kind == schemaArray so resolveImageBody can wrap a
// scalar source-image value into a single-element slice. Without this the
// resolver cannot tell image_url (string) from image_urls (array).
func TestParseModelInputSchemaArray(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fal-schemas", "nano-banana-edit.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	schema, err := parseModelInputSchema(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	urls, ok := schema.property("image_urls")
	if !ok {
		t.Fatalf("expected image_urls property, got ok=%v", ok)
	}
	if urls.Kind != schemaArray {
		t.Fatalf("image_urls.Kind = %v, want schemaArray", urls.Kind)
	}
	if urls.Items == nil {
		t.Fatalf("expected Items to be populated for array, got nil")
	}
	if urls.Items.Kind != schemaScalar {
		t.Fatalf("image_urls.Items.Kind = %v, want schemaScalar (string elements)", urls.Items.Kind)
	}
	// Sanity: the scalar sibling stays scalar.
	if prompt, _ := schema.property("prompt"); prompt.Kind != schemaScalar {
		t.Errorf("prompt.Kind = %v, want schemaScalar", prompt.Kind)
	}
}

// TestParseModelInputSchemaMaxItems verifies maxItems is captured on array
// properties so resolveVideoBody/resolveImageBody can reject requests that
// exceed a model's declared image cap. Properties without maxItems parse as 0
// (unset/unknown) — the guardrail treats 0 as "no cap declared".
func TestParseModelInputSchemaMaxItems(t *testing.T) {
	doc := `{"components":{"schemas":{"XInput":{"type":"object","properties":{
		"image_urls":{"type":"array","items":{"type":"string"},"maxItems":9},
		"unbounded_urls":{"type":"array","items":{"type":"string"}},
		"prompt":{"type":"string"}
	}}}}}`
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	capped, ok := schema.property("image_urls")
	if !ok {
		t.Fatal("expected image_urls property")
	}
	if capped.MaxItems != 9 {
		t.Errorf("image_urls.MaxItems = %d, want 9", capped.MaxItems)
	}
	unbounded, _ := schema.property("unbounded_urls")
	if unbounded.MaxItems != 0 {
		t.Errorf("unbounded_urls.MaxItems = %d, want 0 (unset)", unbounded.MaxItems)
	}
	// Non-array properties don't carry maxItems.
	if prompt, _ := schema.property("prompt"); prompt.MaxItems != 0 {
		t.Errorf("prompt.MaxItems = %d, want 0 on a scalar", prompt.MaxItems)
	}
}

// TestToSchemaPropertyUnwrapsNullableArrayUnion is the regression for
// conv_9bbf4d6894859debe3430fdb: fal declares optional array fields as
// anyOf:[{type:array, items:{...}}, {type:null}] with no top-level "type". Before
// the unwrap, the property parsed as an empty scalar and the resolver dropped the
// attached image with "has no source-image input". The concrete array branch
// must surface so coerceImages builds the list fal's runtime requires.
func TestToSchemaPropertyUnwrapsNullableArrayUnion(t *testing.T) {
	doc := `{"components":{"schemas":{"XInput":{"type":"object","properties":{
		"image_urls":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]}
	}}}}}`
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	urls, ok := schema.property("image_urls")
	if !ok {
		t.Fatal("expected image_urls property")
	}
	if urls.Kind != schemaArray {
		t.Fatalf("image_urls.Kind = %v, want schemaArray (unwrapped from nullable union)", urls.Kind)
	}
	if urls.Type != "array" {
		t.Errorf("image_urls.Type = %q, want \"array\"", urls.Type)
	}
	if urls.Items == nil || urls.Items.Type != "string" {
		t.Errorf("image_urls.Items = %+v, want string element type", urls.Items)
	}
}

// TestToSchemaPropertyUnwrapsNullableScalarUnion covers the nullable scalar
// shape (start_image_url, end_image_url, prompt on the Kling o3/pro schema):
// anyOf:[{type:string}, {type:null}] must surface as a string scalar.
func TestToSchemaPropertyUnwrapsNullableScalarUnion(t *testing.T) {
	doc := `{"components":{"schemas":{"XInput":{"type":"object","properties":{
		"start_image_url":{"anyOf":[{"type":"string"},{"type":"null"}]}
	}}}}}`
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := schema.property("start_image_url")
	if !ok {
		t.Fatal("expected start_image_url property")
	}
	if p.Kind != schemaScalar {
		t.Fatalf("start_image_url.Kind = %v, want schemaScalar", p.Kind)
	}
	if p.Type != "string" {
		t.Errorf("start_image_url.Type = %q, want \"string\"", p.Type)
	}
}

// TestToSchemaPropertyUnwrapsMultiTypeUnionPrefersConcrete covers the seedream /
// ideogram image_size shape: anyOf:[{$ref:...}, {type:string, enum:[...]}]. The
// $ref branch can't be resolved without ref-following, so the parser must pick
// the concrete enum-string branch — surfacing both the type and the enum so the
// resolver's enum guard can validate presets it currently sends blind.
func TestToSchemaPropertyUnwrapsMultiTypeUnionPrefersConcrete(t *testing.T) {
	doc := `{"components":{"schemas":{"XInput":{"type":"object","properties":{
		"image_size":{"anyOf":[{"$ref":"#/components/schemas/ImageSize"},{"type":"string","enum":["square_hd","landscape_16_9","portrait_16_9"]}]}
	}}}}}`
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := schema.property("image_size")
	if !ok {
		t.Fatal("expected image_size property")
	}
	if p.Type != "string" {
		t.Errorf("image_size.Type = %q, want \"string\" (concrete branch)", p.Type)
	}
	if len(p.Enum) != 3 || p.Enum[0] != "square_hd" {
		t.Errorf("image_size.Enum = %v, want [square_hd landscape_16_9 portrait_16_9]", p.Enum)
	}
}

// TestToSchemaPropertyFallsBackOnUnionWithoutConcreteType pins the contract
// that a union with no usable branch (only null / $ref) parses as the old
// default — a scalar with empty type — rather than panicking or mis-routing.
// No surveyed fal schema hits this, but the parser must stay safe on unknown
// shapes.
func TestToSchemaPropertyFallsBackOnUnionWithoutConcreteType(t *testing.T) {
	doc := `{"components":{"schemas":{"XInput":{"type":"object","properties":{
		"weird":{"anyOf":[{"type":"null"},{"$ref":"#/components/schemas/X"}]}
	}}}}}`
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := schema.property("weird")
	if !ok {
		t.Fatal("expected weird property")
	}
	if p.Kind != schemaScalar {
		t.Errorf("weird.Kind = %v, want schemaScalar (fallback)", p.Kind)
	}
	if p.Type != "" {
		t.Errorf("weird.Type = %q, want empty (no concrete branch)", p.Type)
	}
}

// TestToSchemaPropertyUnwrapsOneOf confirms oneOf is treated identically to
// anyOf — both are OpenAPI union markers and fal uses anyOf, but the parser
// must not silently ignore oneOf if a schema uses it.
func TestToSchemaPropertyUnwrapsOneOf(t *testing.T) {
	doc := `{"components":{"schemas":{"XInput":{"type":"object","properties":{
		"ratio":{"oneOf":[{"type":"string","enum":["a","b"]},{"type":"null"}]}
	}}}}}`
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := schema.property("ratio")
	if !ok {
		t.Fatal("expected ratio property")
	}
	if p.Type != "string" {
		t.Errorf("ratio.Type = %q, want \"string\"", p.Type)
	}
	if len(p.Enum) != 2 || p.Enum[0] != "a" {
		t.Errorf("ratio.Enum = %v, want [a b]", p.Enum)
	}
}

// TestParseModelInputSchemaPicksPrimaryInput verifies the *Input selection is
// deterministic when a doc declares more than one. The Kling o3/pro doc also
// declares KlingV3ComboElementInput (a nested-element sub-schema with no
// image_urls); a plain first-match map scan picked either nondeterministically,
// so conv_9bbf4d6894859debe3430fdb intermittently resolved against the helper
// schema and dropped the attached image. The parser must pick the *Input with
// the most properties (the top-level request input).
func TestParseModelInputSchemaPicksPrimaryInput(t *testing.T) {
	doc := `{"components":{"schemas":{
		"KlingV3ComboElementInput":{"type":"object","properties":{"frontal_image_url":{"type":"string"},"voice_id":{"type":"string"}}},
		"KlingVideoO3ProReferenceToVideoInput":{"type":"object","properties":{"prompt":{"type":"string"},"image_urls":{"type":"array","items":{"type":"string"}},"start_image_url":{"type":"string"}}}
	}}}`
	schema, err := parseModelInputSchema([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The primary input (3 props) must win over the element sub-schema (2 props).
	if _, ok := schema.property("image_urls"); !ok {
		t.Fatal("expected image_urls from the primary *Input; the element sub-schema was picked instead")
	}
	if _, ok := schema.property("prompt"); !ok {
		t.Fatal("expected prompt from the primary *Input")
	}
}

// TestParseModelInputSchemaKlingO3ProImageUrlsIsArray is the end-to-end
// regression for conv_9bbf4d6894859debe3430fdb against the real cached fal
// schema: image_urls must parse as schemaArray so resolveVideoBody sends a list,
// not the scalar string fal rejects. Skips when the live cache is absent (CI
// without a fal key), since the synthetic unit tests above cover the shape.
func TestParseModelInputSchemaKlingO3ProImageUrlsIsArray(t *testing.T) {
	path := filepath.Join(os.Getenv("HOME"), ".atelier", "schema-cache", "fal-ai_kling-video_o3_pro_reference-to-video.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("live schema cache not present at %s; skipping end-to-end parse", path)
	}
	var cached struct {
		Raw json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(raw, &cached); err != nil {
		t.Fatalf("decode cache wrapper: %v", err)
	}
	schema, err := parseModelInputSchema(cached.Raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	urls, ok := schema.property("image_urls")
	if !ok {
		t.Fatal("expected image_urls property on Kling o3/pro reference-to-video")
	}
	if urls.Kind != schemaArray {
		t.Fatalf("image_urls.Kind = %v, want schemaArray (the runtime requires a list)", urls.Kind)
	}
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
