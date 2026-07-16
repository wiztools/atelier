# Model-Aware Audio Parameters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `generate_audio` resolve canonical params (prompt, duration, loop, voice, negativePrompt) against each fal model's real input schema, dropping unsupported params and surfacing every drop to the user as a deterministic chat caveat.

**Architecture:** A disk-cached schema provider (`fal_schema.go`) fetches each model's fal OpenAPI input schema. A pure resolver (`fal_params.go`) maps canonical params to native keys (top-level + one-level nested), applies transforms, and returns dropped-param notices. `FalClient.GenerateAudio` becomes a thin transport taking a pre-built native body. Notices ride a generic `NoticeProvider` lift into `HarnessToolResult.Notices` and are appended verbatim to the chat reply.

**Tech Stack:** Go 1.24, standard `testing` (table-driven), `net/http` with injected `roundTripFunc` for network stubs (existing convention in `fal_client_test.go`).

**Spec:** `docs/superpowers/specs/2026-07-16-model-aware-audio-params-design.md`

**Note on `negativePrompt`:** The spec's canonical list is prompt/duration/loop/voice. `negativePrompt` is added to the resolver as a fifth canonical param **to avoid regressing** today's behavior (the current `GenerateAudio` forwards `negative_prompt`). Flagged for reviewer awareness.

---

## File structure

- **Create** `fal_schema.go` — `ModelInputSchema`, OpenAPI parser, `SchemaCache` (disk + TTL + injected clock).
- **Create** `fal_schema_test.go` — parser + cache tests.
- **Create** `fal_params.go` — `Overrides` type + loader/merge, `resolveAudioBody` resolver, synonym sets.
- **Create** `fal_params_test.go` — resolver + override tests (table-driven vs committed fixtures).
- **Create** `testdata/fal-schemas/{sfx-v2,elevenlabs-tts-ml-v2,minimax-speech-02-hd}.json` — real OpenAPI fixtures.
- **Modify** `app.go` — `AudioGenerateRequest` (+Loop, +Voice); `GeneratedAudio` (+Notices).
- **Modify** `fal_client.go` — thin `GenerateAudio(ctx, model, body)`.
- **Modify** `tools_registry.go` — `ToolAudioResult` (+Notices), `NoticeProvider`, tool Execute, `generateAudioParamSchema`.
- **Modify** `tool_gateway.go` — construct cache+overrides; resolve in closure; lift notices.
- **Modify** `harness.go` — `HarnessToolCall` (+Loop, +Voice); `HarnessToolResult` (+Notices); reply-assembly append + streaming fix; system note.

---

## Task 1: Schema type + OpenAPI parser

**Files:**
- Create: `fal_schema.go`
- Test: `fal_schema_test.go`
- Create fixture: `testdata/fal-schemas/minimax-speech-02-hd.json`

- [ ] **Step 1: Add the MiniMax fixture** (trimmed real OpenAPI, enough for tests). Fetch/commit the real schema; minimal viable shape:

```json
{
  "components": {
    "schemas": {
      "MinimaxSpeech02HdInput": {
        "type": "object",
        "required": ["text"],
        "properties": {
          "text": { "type": "string" },
          "voice_setting": {
            "type": "object",
            "default": { "voice_id": "Wise_Woman", "speed": 1, "pitch": 0, "vol": 1 },
            "properties": {
              "voice_id": { "type": "string", "default": "Wise_Woman" },
              "speed": { "type": "number", "default": 1 },
              "pitch": { "type": "integer", "default": 0 },
              "vol": { "type": "number", "default": 1 }
            }
          },
          "output_format": { "type": "string", "enum": ["url", "hex"], "default": "hex" }
        }
      }
    }
  }
}
```

- [ ] **Step 2: Write the failing parser test**

```go
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./... -run TestParseModelInputSchemaNesting`
Expected: FAIL — `parseModelInputSchema` undefined.

- [ ] **Step 4: Implement `fal_schema.go` parser**

```go
package main

import (
	"encoding/json"
	"errors"
	"strings"
)

type schemaKind int

const (
	schemaScalar schemaKind = iota
	schemaObject
)

// SchemaProperty is a simplified view of one OpenAPI input property.
type SchemaProperty struct {
	Name    string
	Kind    schemaKind
	Enum    []string
	Default any                       // object default (for nested merge) or scalar default
	Nested  map[string]SchemaProperty // populated when Kind == schemaObject
}

// ModelInputSchema is the parsed input model for one fal endpoint.
type ModelInputSchema struct {
	Properties map[string]SchemaProperty
	order      []string
}

func (s *ModelInputSchema) property(name string) (SchemaProperty, bool) {
	if s == nil {
		return SchemaProperty{}, false
	}
	p, ok := s.Properties[name]
	return p, ok
}

// objectProps returns object-typed properties in declared order (stable scan).
func (s *ModelInputSchema) objectProps() []SchemaProperty {
	out := make([]SchemaProperty, 0)
	for _, name := range s.order {
		if p := s.Properties[name]; p.Kind == schemaObject {
			out = append(out, p)
		}
	}
	return out
}

type openAPIDoc struct {
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

type openAPIProp struct {
	Type       string                     `json:"type"`
	Enum       []any                      `json:"enum"`
	Default    json.RawMessage            `json:"default"`
	Properties map[string]json.RawMessage `json:"properties"`
}

type openAPIModel struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	order      []string
}

// parseModelInputSchema finds the single `*Input` schema in the doc and
// simplifies it. One level of object nesting is captured; deeper nesting is
// flattened to an opaque object (its scalar sub-props still parse).
func parseModelInputSchema(raw []byte) (*ModelInputSchema, error) {
	var doc openAPIDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var inputRaw json.RawMessage
	for name, body := range doc.Components.Schemas {
		if strings.HasSuffix(name, "Input") {
			inputRaw = body
			break
		}
	}
	if len(inputRaw) == 0 {
		return nil, errors.New("no *Input schema found in openapi doc")
	}
	props, order, err := orderedProps(inputRaw)
	if err != nil {
		return nil, err
	}
	schema := &ModelInputSchema{Properties: map[string]SchemaProperty{}, order: order}
	for _, name := range order {
		schema.Properties[name] = toSchemaProperty(name, props[name])
	}
	return schema, nil
}

// orderedProps preserves declaration order using a raw pass over the JSON.
func orderedProps(inputRaw json.RawMessage) (map[string]json.RawMessage, []string, error) {
	var model openAPIModel
	if err := json.Unmarshal(inputRaw, &model); err != nil {
		return nil, nil, err
	}
	order := jsonKeyOrder(inputRaw, "properties")
	if len(order) == 0 {
		for name := range model.Properties {
			order = append(order, name)
		}
	}
	return model.Properties, order, nil
}

func toSchemaProperty(name string, raw json.RawMessage) SchemaProperty {
	var p openAPIProp
	_ = json.Unmarshal(raw, &p)
	sp := SchemaProperty{Name: name, Kind: schemaScalar, Enum: enumStrings(p.Enum)}
	if len(p.Default) > 0 {
		var d any
		if err := json.Unmarshal(p.Default, &d); err == nil {
			sp.Default = d
		}
	}
	if p.Type == "object" || len(p.Properties) > 0 {
		sp.Kind = schemaObject
		sp.Nested = map[string]SchemaProperty{}
		for subName, subRaw := range p.Properties {
			sp.Nested[subName] = toSchemaProperty(subName, subRaw)
		}
	}
	return sp
}

func enumStrings(vals []any) []string {
	if len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 5: Add the `jsonKeyOrder` helper** (declaration-order of a nested object's keys via a streaming decoder):

```go
// jsonKeyOrder returns the keys of obj[field] in source order. Best-effort:
// returns nil if the shape is unexpected, and callers fall back to map order.
func jsonKeyOrder(raw json.RawMessage, field string) []string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil
	}
	body, ok := top[field]
	if !ok {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return keys
		}
		key, _ := keyTok.(string)
		keys = append(keys, key)
		// skip the value
		if err := skipJSONValue(dec); err != nil {
			return keys
		}
	}
	return keys
}

func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'), json.Delim('['):
		depth := 1
		for depth > 0 {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			switch t {
			case json.Delim('{'), json.Delim('['):
				depth++
			case json.Delim('}'), json.Delim(']'):
				depth--
			}
		}
	}
	return nil
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./... -run TestParseModelInputSchemaNesting`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add fal_schema.go fal_schema_test.go testdata/fal-schemas/minimax-speech-02-hd.json
git commit -m "feat: parse fal model OpenAPI input schemas"
```

---

## Task 2: Disk-cached schema provider with TTL

**Files:**
- Modify: `fal_schema.go`
- Test: `fal_schema_test.go`

- [ ] **Step 1: Write failing cache tests** (fresh-hit does no fetch; expired refetches; fetch-fail returns nil; corrupt file is a miss):

```go
func TestSchemaCacheFreshHitNoFetch(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	fetches := 0
	fetch := func(ctx context.Context, model string) ([]byte, error) {
		fetches++
		return os.ReadFile(filepath.Join("testdata", "fal-schemas", "minimax-speech-02-hd.json"))
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
		return os.ReadFile(filepath.Join("testdata", "fal-schemas", "minimax-speech-02-hd.json"))
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
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./... -run TestSchemaCache`
Expected: FAIL — `SchemaCache` undefined.

- [ ] **Step 3: Implement `SchemaCache` in `fal_schema.go`**

```go
import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type cachedSchema struct {
	FetchedAt time.Time       `json:"fetchedAt"`
	Raw       json.RawMessage `json:"raw"`
}

// SchemaCache fetches and disk-caches fal model input schemas with a TTL.
type SchemaCache struct {
	dir   string
	ttl   time.Duration
	now   func() time.Time
	fetch func(ctx context.Context, model string) ([]byte, error)
}

func newFalSchemaCache(httpClient *http.Client, storageRoot string) *SchemaCache {
	client := FalClient{httpClient: httpClient} // fetch needs no API key (public openapi)
	return &SchemaCache{
		dir: filepath.Join(storageRoot, "schema-cache"),
		ttl: 7 * 24 * time.Hour,
		now: time.Now,
		fetch: func(ctx context.Context, model string) ([]byte, error) {
			return client.fetchOpenAPISchema(ctx, model)
		},
	}
}

// Get returns the parsed schema for model, or nil when unavailable (offline and
// no fresh cache). A fresh disk copy (within TTL) is served without a fetch; an
// expired or missing copy triggers a fetch, and a failed fetch returns nil
// (does NOT fall back to the stale copy — "generic immediately").
func (c *SchemaCache) Get(ctx context.Context, model string) *ModelInputSchema {
	path := c.pathFor(model)
	if raw, ok := c.readFresh(path); ok {
		if schema, err := parseModelInputSchema(raw); err == nil {
			return schema
		}
	}
	raw, err := c.fetch(ctx, model)
	if err != nil || len(raw) == 0 {
		return nil
	}
	c.write(path, raw)
	schema, err := parseModelInputSchema(raw)
	if err != nil {
		return nil
	}
	return schema
}

func (c *SchemaCache) pathFor(model string) string {
	return filepath.Join(c.dir, sanitizeModelID(model)+".json")
}

func (c *SchemaCache) readFresh(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var entry cachedSchema
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false // corrupt → miss
	}
	if c.now().Sub(entry.FetchedAt) > c.ttl {
		return nil, false
	}
	return entry.Raw, true
}

func (c *SchemaCache) write(path string, raw []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	entry := cachedSchema{FetchedAt: c.now(), Raw: json.RawMessage(raw)}
	if data, err := json.Marshal(entry); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}

func sanitizeModelID(model string) string {
	return strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(model)
}
```

- [ ] **Step 4: Add `fetchOpenAPISchema` to `fal_client.go`** (uses the queue openapi endpoint; goes through the shared `do` helper without auth):

```go
const falOpenAPIBaseURL = "https://fal.ai"

// fetchOpenAPISchema returns the raw OpenAPI JSON for a fal endpoint's input
// schema. Public endpoint; no Authorization required.
func (client FalClient) fetchOpenAPISchema(ctx context.Context, model string) ([]byte, error) {
	path := "/api/openapi/queue/openapi.json?endpoint_id=" + url.QueryEscape(model)
	resp, err := client.do(ctx, falOpenAPIBaseURL, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
```

(Confirm `net/url` and `io` are imported in `fal_client.go`; add if missing.)

- [ ] **Step 5: Run cache tests**

Run: `go test ./... -run TestSchemaCache`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add fal_schema.go fal_schema_test.go fal_client.go
git commit -m "feat: disk-cached fal schema provider with TTL"
```

---

## Task 3: Overrides loader + merge

**Files:**
- Create: `fal_params.go`
- Test: `fal_params_test.go`

- [ ] **Step 1: Write failing override test**

```go
func TestLoadFalOverridesMergesUserOverBuiltin(t *testing.T) {
	dir := t.TempDir()
	// user file marks minimax voice unsupported and remaps a custom model
	user := `{"audio":{"fal-ai/minimax/speech-02-hd":{"voice":""},"acme/tts":{"voice":"speaker_id"}}}`
	if err := os.WriteFile(filepath.Join(dir, "fal-overrides.json"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	ov := loadFalOverrides(dir)
	if got, ok := ov.lookup("audio", "fal-ai/minimax/speech-02-hd", "voice"); !ok || got != "" {
		t.Fatalf("expected explicit-unsupported voice override, got %q ok=%v", got, ok)
	}
	if got, _ := ov.lookup("audio", "acme/tts", "voice"); got != "speaker_id" {
		t.Fatalf("expected user voice remap, got %q", got)
	}
}

func TestLoadFalOverridesMalformedIgnored(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "fal-overrides.json"), []byte("{ not json"), 0o644)
	ov := loadFalOverrides(dir) // must not panic; returns built-in defaults
	if ov.byCategory == nil {
		t.Fatal("expected non-nil overrides even on malformed file")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./... -run TestLoadFalOverrides`
Expected: FAIL — `loadFalOverrides` undefined.

- [ ] **Step 3: Implement overrides in `fal_params.go`**

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Overrides maps category → model-id → canonical → native path.
// A native path of "" means the canonical param is explicitly unsupported
// (drop-with-notice). Missing entries fall through to schema heuristics.
type Overrides struct {
	byCategory map[string]map[string]map[string]string
}

func (o Overrides) lookup(category, model, canon string) (string, bool) {
	models, ok := o.byCategory[category]
	if !ok {
		return "", false
	}
	params, ok := models[model]
	if !ok {
		return "", false
	}
	native, ok := params[canon]
	return native, ok
}

// builtinFalOverrides holds defaults for models the heuristics get wrong.
// Empty today; entries are added as such models are discovered.
func builtinFalOverrides() Overrides {
	return Overrides{byCategory: map[string]map[string]map[string]string{
		"audio": {},
	}}
}

// loadFalOverrides reads ~/.atelier/fal-overrides.json and merges it OVER the
// built-in defaults. A missing or malformed file yields the built-ins.
func loadFalOverrides(storageRoot string) Overrides {
	ov := builtinFalOverrides()
	data, err := os.ReadFile(filepath.Join(storageRoot, "fal-overrides.json"))
	if err != nil {
		return ov
	}
	var parsed map[string]map[string]map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ov // malformed → built-ins
	}
	for category, models := range parsed {
		if ov.byCategory[category] == nil {
			ov.byCategory[category] = map[string]map[string]string{}
		}
		for model, params := range models {
			if ov.byCategory[category][model] == nil {
				ov.byCategory[category][model] = map[string]string{}
			}
			for canon, native := range params {
				ov.byCategory[category][model][canon] = native
			}
		}
	}
	return ov
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./... -run TestLoadFalOverrides`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add fal_params.go fal_params_test.go
git commit -m "feat: fal override table with user-file merge"
```

---

## Task 4: The resolver

**Files:**
- Modify: `fal_params.go`
- Test: `fal_params_test.go`
- Create fixtures: `testdata/fal-schemas/sfx-v2.json`, `testdata/fal-schemas/elevenlabs-tts-ml-v2.json`

- [ ] **Step 1: Add the two remaining fixtures.** `sfx-v2.json` Input props: `text` (string, required), `duration_seconds` (number), `prompt_influence` (number), `output_format` (enum), `loop` (boolean). `elevenlabs-tts-ml-v2.json` Input props: `text` (string, required), `voice` (string, default "Rachel"), plus `stability`, `similarity_boost`. Follow the same OpenAPI shape as Task 1's fixture (schema name ends in `Input`).

- [ ] **Step 2: Write failing resolver tests** (one per branch, using real fixtures):

```go
func loadSchema(t *testing.T, name string) *ModelInputSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fal-schemas", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseModelInputSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolveSFXLoopAndDuration(t *testing.T) {
	body, notices := resolveAudioBody(loadSchema(t, "sfx-v2"),
		AudioGenerateRequest{Model: "fal-ai/elevenlabs/sound-effects/v2", Prompt: "rain", Duration: "10", Loop: true},
		builtinFalOverrides())
	if body["text"] != "rain" {
		t.Fatalf("expected text=rain, got %v", body["text"])
	}
	if body["duration_seconds"] != 10.0 {
		t.Fatalf("expected duration_seconds=10, got %v", body["duration_seconds"])
	}
	if body["loop"] != true {
		t.Fatalf("expected loop=true, got %v", body["loop"])
	}
	if len(notices) != 0 {
		t.Fatalf("expected no notices, got %v", notices)
	}
}

func TestResolveVoiceNestedMerge(t *testing.T) {
	body, notices := resolveAudioBody(loadSchema(t, "minimax-speech-02-hd"),
		AudioGenerateRequest{Model: "fal-ai/minimax/speech-02-hd", Prompt: "hello", Voice: "Grandma"},
		builtinFalOverrides())
	vs, ok := body["voice_setting"].(map[string]any)
	if !ok {
		t.Fatalf("expected voice_setting object, got %T", body["voice_setting"])
	}
	if vs["voice_id"] != "Grandma" {
		t.Fatalf("expected voice_id=Grandma, got %v", vs["voice_id"])
	}
	if vs["speed"] != 1.0 { // sibling default preserved by merge
		t.Fatalf("expected merged default speed=1, got %v", vs["speed"])
	}
	if len(notices) != 0 {
		t.Fatalf("unexpected notices: %v", notices)
	}
}

func TestResolveDropsUnsupportedLoop(t *testing.T) {
	_, notices := resolveAudioBody(loadSchema(t, "elevenlabs-tts-ml-v2"),
		AudioGenerateRequest{Model: "fal-ai/elevenlabs/tts/multilingual-v2", Prompt: "hi", Loop: true},
		builtinFalOverrides())
	if len(notices) != 1 || !strings.Contains(notices[0], "loop") {
		t.Fatalf("expected one loop-drop notice, got %v", notices)
	}
}

func TestResolveVoiceOnSFXDropped(t *testing.T) {
	_, notices := resolveAudioBody(loadSchema(t, "sfx-v2"),
		AudioGenerateRequest{Model: "sfx", Prompt: "wind", Voice: "Rachel"},
		builtinFalOverrides())
	if len(notices) != 1 || !strings.Contains(notices[0], "voice") {
		t.Fatalf("expected voice-drop notice, got %v", notices)
	}
}

func TestResolveSchemaUnavailableGeneric(t *testing.T) {
	body, notices := resolveAudioBody(nil,
		AudioGenerateRequest{Model: "x", Prompt: "hi", Loop: true, Voice: "Rachel"},
		builtinFalOverrides())
	if body["prompt"] != "hi" || body["text"] != "hi" {
		t.Fatalf("expected generic prompt+text body, got %v", body)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "schema") {
		t.Fatalf("expected schema-unavailable notice, got %v", notices)
	}
}
```

- [ ] **Step 3: Run to verify fail**

Run: `go test ./... -run TestResolve`
Expected: FAIL — `resolveAudioBody` undefined.

- [ ] **Step 4: Implement the resolver in `fal_params.go`**

```go
import (
	"fmt"
	"strconv"
	"strings"
)

var audioSynonyms = map[string][]string{
	"prompt":         {"prompt", "text"},
	"duration":       {"duration_seconds", "duration", "music_length_ms"},
	"loop":           {"loop"},
	"voice":          {"voice", "voice_id", "voice_name", "speaker", "speaker_id"},
	"negativePrompt": {"negative_prompt"},
}

// canonicalAudioValues yields the (canon, value, present) tuples to resolve,
// in a stable order. prompt is handled separately (always required).
func canonicalAudioValues(req AudioGenerateRequest) []struct {
	canon   string
	value   any
	present bool
} {
	return []struct {
		canon   string
		value   any
		present bool
	}{
		{"duration", req.Duration, strings.TrimSpace(req.Duration) != ""},
		{"loop", req.Loop, req.Loop},
		{"voice", req.Voice, strings.TrimSpace(req.Voice) != ""},
		{"negativePrompt", req.NegativePrompt, strings.TrimSpace(req.NegativePrompt) != ""},
	}
}

// resolveAudioBody maps a canonical AudioGenerateRequest onto model's native
// input schema, returning the fal body and user-facing notices for anything
// dropped. A nil schema (unavailable) yields a generic prompt+text body.
func resolveAudioBody(schema *ModelInputSchema, req AudioGenerateRequest, ov Overrides) (map[string]any, []string) {
	prompt := strings.TrimSpace(req.Prompt)
	if schema == nil {
		return map[string]any{"prompt": prompt, "text": prompt},
			[]string{"Couldn't load the model's parameter schema; generated with defaults and skipped duration/loop/voice."}
	}

	body := map[string]any{}
	var notices []string

	// prompt always maps; hard requirement.
	if path, _, ok := findNative(schema, ov, req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, prompt)
	} else {
		// Neither text nor prompt: still send both so submit isn't empty.
		body["prompt"], body["text"] = prompt, prompt
	}

	for _, item := range canonicalAudioValues(req) {
		if !item.present {
			continue
		}
		path, prop, ok := findNative(schema, ov, req.Model, item.canon)
		if !ok {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no %s control; ignoring the requested %s.",
				req.Model, canonLabel(item.canon), canonLabel(item.canon)))
			continue
		}
		value, noticed := coerceValue(item.canon, path, prop, item.value, req.Model)
		if noticed != "" {
			notices = append(notices, noticed)
			continue
		}
		setBodyPath(schema, body, path, value)
	}
	return body, notices
}

// findNative resolves canon → native dot-path via override, top-level scan,
// then one-level nested scan. Returns the matched leaf property for coercion.
func findNative(schema *ModelInputSchema, ov Overrides, model, canon string) (string, SchemaProperty, bool) {
	if native, ok := ov.lookup("audio", model, canon); ok {
		if native == "" {
			return "", SchemaProperty{}, false // explicitly unsupported
		}
		if prop, ok := propAtPath(schema, native); ok {
			return native, prop, true
		}
		return native, SchemaProperty{Name: native, Kind: schemaScalar}, true
	}
	syns := audioSynonyms[canon]
	for _, name := range syns {
		if prop, ok := schema.property(name); ok {
			return name, prop, true
		}
	}
	for _, obj := range schema.objectProps() {
		for _, name := range syns {
			if sub, ok := obj.Nested[name]; ok {
				return obj.Name + "." + name, sub, true
			}
		}
	}
	return "", SchemaProperty{}, false
}

func propAtPath(schema *ModelInputSchema, path string) (SchemaProperty, bool) {
	parts := strings.SplitN(path, ".", 2)
	top, ok := schema.property(parts[0])
	if !ok {
		return SchemaProperty{}, false
	}
	if len(parts) == 1 {
		return top, true
	}
	sub, ok := top.Nested[parts[1]]
	return sub, ok
}

// coerceValue converts a canonical value to the native property's type and
// applies transforms (seconds→ms for *_ms fields; enum validation). Returns a
// notice string instead of a value when the value is invalid for the property.
func coerceValue(canon, path string, prop SchemaProperty, value any, model string) (any, string) {
	switch canon {
	case "duration":
		secs, err := strconv.ParseFloat(strings.TrimSpace(value.(string)), 64)
		if err != nil {
			return nil, fmt.Sprintf("Ignored duration %q for %q: not a number.", value, model)
		}
		if strings.HasSuffix(prop.Name, "_ms") {
			return secs * 1000, ""
		}
		return secs, ""
	case "loop":
		return true, ""
	default: // voice, negativePrompt — string values, enum-checked
		s := fmt.Sprintf("%v", value)
		if len(prop.Enum) > 0 && !contains(prop.Enum, s) {
			return nil, fmt.Sprintf("%q isn't a valid %s for %q; valid: %s.",
				s, canonLabel(canon), model, strings.Join(prop.Enum, ", "))
		}
		return s, ""
	}
}

// setBodyPath writes value at a dot-path, seeding a nested object from the
// schema's default so sibling defaults survive the merge.
func setBodyPath(schema *ModelInputSchema, body map[string]any, path string, value any) {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		body[parts[0]] = value
		return
	}
	obj, ok := body[parts[0]].(map[string]any)
	if !ok {
		obj = map[string]any{}
		if top, ok := schema.property(parts[0]); ok {
			if def, ok := top.Default.(map[string]any); ok {
				for k, v := range def {
					obj[k] = v
				}
			}
		}
	}
	obj[parts[1]] = value
	body[parts[0]] = obj
}

func canonLabel(canon string) string {
	if canon == "negativePrompt" {
		return "negative prompt"
	}
	return canon
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run resolver tests**

Run: `go test ./... -run TestResolve`
Expected: PASS (all five).

- [ ] **Step 6: Commit**

```bash
git add fal_params.go fal_params_test.go testdata/fal-schemas/sfx-v2.json testdata/fal-schemas/elevenlabs-tts-ml-v2.json
git commit -m "feat: model-aware audio param resolver"
```

---

## Task 5: Thin FalClient.GenerateAudio + request/result fields

**Files:**
- Modify: `app.go` (`AudioGenerateRequest`, `GeneratedAudio`)
- Modify: `fal_client.go` (`GenerateAudio` signature)
- Modify: `fal_client_test.go` (existing GenerateAudio test call sites)

- [ ] **Step 1: Update `AudioGenerateRequest` and `GeneratedAudio` in `app.go`**

```go
type AudioGenerateRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	Duration       string `json:"duration,omitempty"`
	NegativePrompt string `json:"negativePrompt,omitempty"`
	Loop           bool   `json:"loop,omitempty"`
	Voice          string `json:"voice,omitempty"`
}
```

Find the `GeneratedAudio` struct (grep `type GeneratedAudio struct`) and add:

```go
	Notices []string `json:"notices,omitempty"`
```

- [ ] **Step 2: Make `GenerateAudio` thin in `fal_client.go`** — replace lines 300-355 body-building with a signature that accepts a pre-built body:

```go
func (client FalClient) GenerateAudio(ctx context.Context, model string, body map[string]any) (GeneratedAudio, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultFalAudioModel
	}
	if body == nil {
		body = map[string]any{}
	}
	submit, err := client.submit(ctx, model, body)
	if err != nil {
		return GeneratedAudio{}, err
	}
	requestID := strings.TrimSpace(submit.RequestID)
	if requestID == "" {
		return GeneratedAudio{}, errors.New("fal submit returned no request id")
	}
	statusURL, resultURL := falQueueURLs(submit, model, requestID)
	if err := client.waitForCompletion(ctx, statusURL); err != nil {
		return GeneratedAudio{}, err
	}
	result, raw, err := client.fetchResult(ctx, resultURL)
	if err != nil {
		return GeneratedAudio{}, err
	}
	audioURL := ""
	for _, file := range []*falVideoFile{result.Audio, result.AudioFile} {
		if file != nil && strings.TrimSpace(file.URL) != "" {
			audioURL = strings.TrimSpace(file.URL)
			break
		}
	}
	if audioURL == "" {
		audioURL = firstFalAudioURL(raw)
	}
	if audioURL == "" {
		return GeneratedAudio{}, errors.New("fal result returned no audio")
	}
	data, mimeType, err := client.downloadAudio(ctx, audioURL)
	if err != nil {
		return GeneratedAudio{}, err
	}
	return GeneratedAudio{Data: data, MimeType: mimeType, SourceURL: audioURL}, nil
}
```

- [ ] **Step 3: Fix `fal_client_test.go` call sites.** Grep `GenerateAudio(` in the test; update each to the new signature, e.g. `client.GenerateAudio(ctx, "fal-ai/elevenlabs/tts/multilingual-v2", map[string]any{"text": "hi"})`. Assert the submitted body matches (the test's roundTripFunc can capture the request body).

- [ ] **Step 4: Run**

Run: `go test ./... -run TestFal`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app.go fal_client.go fal_client_test.go
git commit -m "refactor: thin FalClient.GenerateAudio taking a native body"
```

---

## Task 6: Tool wiring — param schema, Execute, NoticeProvider

**Files:**
- Modify: `tools_registry.go`
- Test: `tools_registry_test.go`

- [ ] **Step 1: Add `loop`/`voice` to `generateAudioParamSchema()`** (tools_registry.go:612):

```go
			"loop":  boolParam("Optional — set true for a seamless, gapless loop (ambient beds, backgrounds). Only some sound-effect models support it; ignored otherwise with a note."),
			"voice": stringParam("Optional — the voice to use for text-to-speech (e.g. \"Rachel\"). Only text-to-speech models support it; ignored otherwise with a note."),
```

- [ ] **Step 2: Add `Notices` to `ToolAudioResult` and implement `NoticeProvider`** (tools_registry.go:80):

```go
type ToolAudioResult struct {
	Model   string          `json:"model"`
	Prompt  string          `json:"prompt"`
	Count   int             `json:"count"`
	Audios  []ToolAudioFile `json:"audios,omitempty"`
	Notices []string        `json:"notices,omitempty"`
}

func (r ToolAudioResult) ToolNotices() []string { return r.Notices }

// NoticeProvider lets a tool's output surface deterministic user-facing caveats.
type NoticeProvider interface { ToolNotices() []string }
```

- [ ] **Step 3: Map Loop/Voice and copy notices in Execute** (tools_registry.go:330-353):

```go
			audioReq := AudioGenerateRequest{
				Model:          model,
				Prompt:         strings.TrimSpace(call.Content),
				Duration:       strings.TrimSpace(call.Duration),
				NegativePrompt: strings.TrimSpace(call.NegativePrompt),
				Loop:           call.Loop,
				Voice:          strings.TrimSpace(call.Voice),
			}
			// ... after generated, err := tools.GenerateAudio(ctx, audioReq):
			output := ToolAudioResult{
				Model:   model,
				Prompt:  audioReq.Prompt,
				Count:   1,
				Audios:  []ToolAudioFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
				Notices: generated.Notices,
			}
```

- [ ] **Step 4: Write failing test** that the tool result carries notices:

```go
func TestGenerateAudioSurfacesNotices(t *testing.T) {
	tools := HarnessToolExecutionContext{
		Config: AppConfig{ /* audio model set so resolveDefaultAudioModel returns non-empty */ },
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg", Notices: []string{"loop ignored"}}, nil
		},
	}
	def := audioGenerationToolDefinition()
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "rain", Loop: true, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	np, ok := out.(NoticeProvider)
	if !ok || len(np.ToolNotices()) != 1 || np.ToolNotices()[0] != "loop ignored" {
		t.Fatalf("expected notice carried on result, got %+v", out)
	}
}
```

(Set the `Config` audio model using whatever `resolveDefaultAudioModel` reads — grep it; e.g. `AppConfig{Providers: ConfigProviders{Fal: ConfigFal{AudioModel: "m"}}}`.)

- [ ] **Step 5: Run**

Run: `go test ./... -run TestGenerateAudioSurfacesNotices`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add tools_registry.go tools_registry_test.go
git commit -m "feat: thread audio param notices through the tool result"
```

---

## Task 7: HarnessToolCall + HarnessToolResult + gateway lift

**Files:**
- Modify: `harness.go` (`HarnessToolCall`, `HarnessToolResult`)
- Modify: `tool_gateway.go` (resolve closure + notice lift)

- [ ] **Step 1: Add planner fields to `HarnessToolCall`** (harness.go:108):

```go
	Duration string `json:"duration,omitempty"`
	// Loop and Voice are optional generate_audio inputs. Loop requests a
	// seamless loop (sound-effect models); Voice selects a TTS voice. Both are
	// resolved against the model's schema and dropped-with-notice if unsupported.
	Loop  bool   `json:"loop,omitempty"`
	Voice string `json:"voice,omitempty"`
```

- [ ] **Step 2: Add `Notices` to `HarnessToolResult`** (harness.go:111):

```go
type HarnessToolResult struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Result  any      `json:"result,omitempty"`
	Error   string   `json:"error,omitempty"`
	Notices []string `json:"notices,omitempty"`
}
```

- [ ] **Step 3: Lift notices in the gateway** (tool_gateway.go, after line 103 `result.Summary = summary`):

```go
	if np, ok := output.(NoticeProvider); ok {
		result.Notices = np.ToolNotices()
	}
```

- [ ] **Step 4: Construct cache+overrides and resolve in the audio closure** (tool_gateway.go). Before the closures (after line 36), build once:

```go
		schemaCache := newFalSchemaCache(app.client, config.Storage.Root)
		audioOverrides := loadFalOverrides(config.Storage.Root)
```

Replace the audio closure body (lines 60-69) so it resolves before calling the now-thin client:

```go
		gateway.tools.GenerateAudio = func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedAudio{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedAudio{}, errFalKeyNotConfigured
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices := resolveAudioBody(schema, req, audioOverrides)
			ga, err := newFalClient(app.client, apiKey).GenerateAudio(ctx, req.Model, body)
			ga.Notices = notices
			return ga, err
		}
```

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add harness.go tool_gateway.go
git commit -m "feat: resolve audio params in gateway and lift notices"
```

---

## Task 8: Route B — append notices to the chat reply

**Files:**
- Modify: `harness.go` (reply-assembly + streaming fix + system note)
- Test: `fal_harness_test.go`

- [ ] **Step 1: Add a helper to collect + format notices** (harness.go, near reply-assembly):

```go
// collectToolNotices gathers deterministic caveats across all tool results,
// formatted as blockquote lines for the chat reply.
func collectToolNotices(results []HarnessToolResult) string {
	var lines []string
	for _, r := range results {
		for _, n := range r.Notices {
			if s := strings.TrimSpace(n); s != "" {
				lines = append(lines, "> ⚠️ "+s)
			}
		}
	}
	return strings.Join(lines, "\n")
}
```

- [ ] **Step 2: Append notices to assistantContent and fix the live emit** (harness.go:277-336). After `assistantContent := result.Content` and the empty-notice block, insert:

```go
	toolNotices := collectToolNotices(preparation.ToolResults)
	if toolNotices != "" {
		if strings.TrimSpace(assistantContent) != "" {
			assistantContent += "\n\n" + toolNotices
		} else {
			assistantContent = toolNotices
		}
	}
```

Then change the terminal-emit content so notices render live even when the
model's prose was already streamed. Replace the `terminalContent` block
(harness.go:321-324):

```go
	terminalContent := assistantContent
	if finalContentEmitted {
		// The model's streamed prose already reached the UI; only the appended
		// notice block (if any) still needs to be delivered.
		terminalContent = toolNotices
		if toolNotices != "" {
			terminalContent = "\n\n" + toolNotices
		}
	}
```

- [ ] **Step 3: Add the model-instruction line to `toolEvidenceSystemNote`.** Grep `toolEvidenceSystemNote =` and append to its string:

```
 A tool result's "notices" field holds authoritative caveats shown to the user verbatim; account for their meaning (do not claim a dropped capability succeeded) but do not quote them.
```

- [ ] **Step 4: Write failing test** for the append (drive a harness turn whose tool result carries a notice; assert the persisted/emitted assistant content contains the caveat). Model this on existing `fal_harness_test.go` turn tests — reuse their fake provider + gateway setup, returning a `HarnessToolResult{Notices: []string{"loop ignored"}}`, and assert the final `ChatStreamEvent.Content` or saved turn contains `⚠️ loop ignored`.

```go
func TestHarnessAppendsToolNotices(t *testing.T) {
	// (mirror the setup of the nearest existing harness turn test)
	// ... arrange a generate_audio turn whose gateway returns Notices:["loop ignored"]
	// ... run RunChatStream, capture emitted events
	if !strings.Contains(finalContent, "loop ignored") {
		t.Fatalf("expected notice in reply, got %q", finalContent)
	}
}
```

- [ ] **Step 5: Run**

Run: `go test ./... -run TestHarnessAppendsToolNotices`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add harness.go fal_harness_test.go
git commit -m "feat: append tool notices to chat reply (Route B)"
```

---

## Task 9: Full build, format, vet, and manual verify

- [ ] **Step 1: Format and vet**

Run: `gofmt -w *.go && go vet ./...`
Expected: no diagnostics.

- [ ] **Step 2: Full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 3: Manual end-to-end (verify skill).** With a fal key configured and `audioModel` set to `fal-ai/elevenlabs/sound-effects/v2`, run the app and ask for "a looping ambient rain sound." Confirm: audio attaches, no caveat (loop supported). Then set `audioModel` to `fal-ai/elevenlabs/tts/multilingual-v2`, ask again for a "looping" sound, and confirm the reply contains the loop-not-supported caveat. Live fal call — run only with user go-ahead.

- [ ] **Step 4: Final commit (if any format/vet fixes)**

```bash
git add -A && git commit -m "chore: gofmt and vet fixes for model-aware audio params"
```

---

## Self-review notes

- **Spec coverage:** schema fetch+cache+TTL (T2), resolver with synonyms/nesting/enum/generic (T1,T4), overrides user-merge (T3), canonical params incl. loop/voice on the tool (T6), thin client (T5), generic Notices + lift (T6,T7), Route B append + streaming fix + system note (T8), tests+fixtures throughout, manual verify (T9).
- **Deviation:** `negativePrompt` added as a fifth canonical param to avoid regressing current behavior — flagged above and in the plan header.
- **Type consistency:** `ModelInputSchema`, `SchemaProperty`, `SchemaCache`, `Overrides`, `resolveAudioBody`, `NoticeProvider`, `GeneratedAudio.Notices`, `HarnessToolResult.Notices` are used identically across tasks.
- **Open confirm during impl:** exact `AppConfig` path for the audio model in the Task 6 test (`resolveDefaultAudioModel`), and the nearest existing harness turn test to mirror in Task 8.
