package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type schemaKind int

const (
	schemaScalar schemaKind = iota
	schemaObject
	schemaArray
)

// SchemaProperty is a simplified view of one OpenAPI input property. One level
// of object nesting is captured in Nested; Default holds the property's default
// (used to seed nested-object merges so sibling defaults survive).
type SchemaProperty struct {
	Name    string
	Kind    schemaKind
	Enum    []string
	Default any
	Nested  map[string]SchemaProperty // populated when Kind == schemaObject
	Items   *SchemaProperty           // populated when Kind == schemaArray (may be nil)
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
	if s == nil {
		return nil
	}
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
	Items      json.RawMessage            `json:"items"`
}

type openAPIModel struct {
	Properties map[string]json.RawMessage `json:"properties"`
}

// parseModelInputSchema finds the single `*Input` schema in the doc and
// simplifies it. One level of object nesting is captured.
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
	var model openAPIModel
	if err := json.Unmarshal(inputRaw, &model); err != nil {
		return nil, err
	}
	order := jsonKeyOrder(inputRaw, "properties")
	if len(order) == 0 {
		for name := range model.Properties {
			order = append(order, name)
		}
	}
	schema := &ModelInputSchema{Properties: map[string]SchemaProperty{}, order: order}
	for _, name := range order {
		schema.Properties[name] = toSchemaProperty(name, model.Properties[name])
	}
	return schema, nil
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
	// Arrays (e.g. fal-ai/nano-banana/edit's image_urls) get their own kind so
	// the resolver can wrap a scalar value into a slice at body-build time.
	if p.Type == "array" {
		sp.Kind = schemaArray
		if len(p.Items) > 0 {
			item := toSchemaProperty(name, p.Items)
			sp.Items = &item
		}
		return sp
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
		if key, ok := keyTok.(string); ok {
			keys = append(keys, key)
		}
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

// --- Disk-cached schema provider ---

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
	return &SchemaCache{
		dir: filepath.Join(storageRoot, "schema-cache"),
		ttl: 7 * 24 * time.Hour,
		now: time.Now,
		fetch: func(ctx context.Context, model string) ([]byte, error) {
			// The OpenAPI endpoint is public, but the shared do() helper requires
			// a key, so load it the same way the generation calls do. A missing
			// key surfaces as an unavailable schema (generic body + notice).
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return nil, err
			}
			return newFalClient(httpClient, apiKey).fetchOpenAPISchema(ctx, model)
		},
	}
}

// Get returns the parsed schema for model, or nil when unavailable (offline and
// no fresh cache). A fresh disk copy (within TTL) is served without a fetch; an
// expired or missing copy triggers a fetch, and a failed fetch returns nil (does
// NOT fall back to a stale copy — "generic immediately").
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
