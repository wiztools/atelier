package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Name string
	Kind schemaKind
	// Description is the property's OpenAPI description. Reference-input
	// models advertise their in-prompt @ImageN/@VideoN token syntax here
	// (e.g. seedance's image_urls), so the resolver gates its prompt legend on
	// the model's own documentation rather than a hardcoded model list. On
	// nullable-union fields the description lives on the parent property next
	// to anyOf — toSchemaProperty preserves it through the unwrap.
	Description string
	// Type is the raw OpenAPI scalar type ("string", "integer", "number",
	// "boolean") for scalars; the resolver uses it to coerce the canonical
	// (always-string) duration into the type fal's schema declares — an integer
	// duration must be sent as a JSON number, not a string (see conv_4feb919a:
	// happy-horse 422'd on "10" because it expects integer 10). Empty for the
	// array/object kinds, which carry their structure in Items/Nested instead.
	Type     string
	Enum     []string
	Default  any
	Nested   map[string]SchemaProperty // populated when Kind == schemaObject
	Items    *SchemaProperty           // populated when Kind == schemaArray (may be nil)
	MaxItems int                       // populated when Kind == schemaArray; 0 = unset
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
	MaxItems   int                        `json:"maxItems"`
	AnyOf      []json.RawMessage          `json:"anyOf"`
	OneOf      []json.RawMessage          `json:"oneOf"`
	// Description is documented at the property level (not inside union
	// branches), so it must survive the union unwrap to stay usable.
	Description string `json:"description"`
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
	// Find the primary *Input schema. Some fal OpenAPI docs declare more than
	// one (e.g. Kling o3/pro reference-to-video also declares
	// KlingV3ComboElementInput for a nested element shape). A plain map scan +
	// first-match break is nondeterministic across map iteration order, which
	// caused conv_9bbf4d6894859debe3430fdb to intermittently select the helper
	// schema (no image_urls) and drop the attached image. Pick the *Input with
	// the most properties — the top-level request input carries all the
	// user-facing fields, while nested element/item sub-schemas are smaller —
	// and tie-break by name for stability.
	var inputRaw json.RawMessage
	var inputName string
	var inputPropCount int
	for name, body := range doc.Components.Schemas {
		if !strings.HasSuffix(name, "Input") {
			continue
		}
		// Count top-level properties without a full recursive parse: decode just
		// the property map's length. Cheap and order-independent.
		var probe struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		n := 0
		if json.Unmarshal(body, &probe) == nil {
			n = len(probe.Properties)
		}
		if n > inputPropCount || (n == inputPropCount && (inputName == "" || name < inputName)) {
			inputRaw = body
			inputName = name
			inputPropCount = n
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
	// Unwrap anyOf/oneOf before parsing. fal declares optional fields as
	// anyOf:[{<concrete type>}, {type:"null"}] (nullable unions); without this
	// unwrap, the property has no top-level "type" and parses as an empty
	// scalar, losing its array-ness/items/enum. See conv_9bbf4d6894859debe3430fdb
	// (Kling o3/pro image_urls dropped the attached image this way). Recursion
	// terminates because the chosen branch has a concrete type.
	if p.Type == "" {
		if branch := unwrapUnion(p); branch != nil {
			unwrapped := toSchemaProperty(name, branch)
			if unwrapped.Description == "" {
				unwrapped.Description = p.Description
			}
			return unwrapped
		}
	}
	sp := SchemaProperty{Name: name, Kind: schemaScalar, Type: p.Type, Description: p.Description, Enum: enumStrings(p.Enum)}
	if len(p.Default) > 0 {
		var d any
		if err := json.Unmarshal(p.Default, &d); err == nil {
			sp.Default = d
		}
	}
	// Arrays (e.g. fal-ai/nano-banana/edit's image_urls) get their own kind so
	// the resolver can wrap a scalar value into a slice at body-build time.
	// MaxItems is captured so a multi-image guardrail can reject requests that
	// exceed the model's declared cap; 0 means the model didn't declare one.
	if p.Type == "array" {
		sp.Kind = schemaArray
		sp.MaxItems = p.MaxItems
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

// unwrapUnion picks the concrete branch from an anyOf/oneOf union on p so the
// property's real type/array-ness/items/enum survive parsing. Returns nil when
// p has no union or no branch qualifies, so callers behave as before.
//
// fal declares optional fields two ways, both handled here:
//
//  1. Nullable union (the common shape): anyOf:[{<concrete type>}, {type:"null"}].
//     Example: image_urls: anyOf:[{type:array, items:{type:string}}, {type:null}].
//  2. Multi-type union: anyOf:[{$ref:...}, {type:"string", enum:[...]}] (e.g.
//     seedream's image_size). No null branch; the $ref branch can't be resolved
//     without ref-following, so the enum-string branch is the usable one.
//
// In both shapes the right branch is the first carrying a concrete top-level
// "type" other than "null" — null-type and $ref-only (type-less) branches are
// skipped. Every fal union surveyed (17 live schemas + test fixtures) has at
// least one such branch.
func unwrapUnion(p openAPIProp) json.RawMessage {
	branches := p.AnyOf
	if len(branches) == 0 {
		branches = p.OneOf
	}
	for _, branch := range branches {
		var sub openAPIProp
		if json.Unmarshal(branch, &sub) != nil {
			continue
		}
		// Skip branches with no concrete type: the nullable {type:"null"} marker
		// and $ref-only branches both qualify (a $ref adds no top-level type).
		if sub.Type == "" || sub.Type == "null" {
			continue
		}
		return branch
	}
	return nil
}

// enumStrings renders an OpenAPI enum (which may be strings OR numbers — fal's
// happy-horse declares duration as an integer enum [3..15]) into string form.
// Stringifying with %v rather than a type-assertion matters: encoding/json
// decodes JSON numbers into []any as float64, so v.(string) would silently drop
// every numeric enum value, leaving the enum empty and bypassing the
// valueAllowedByEnum guard. "%v" renders both "5" (from string) and "5" (from
// float64 5.0) identically, so callers compare against the canonical string
// form uniformly.
func enumStrings(vals []any) []string {
	if len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, fmt.Sprintf("%v", v))
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

// videoDurationOptions returns the duration values the given fal video model
// accepts, drawn from its published input schema's duration enum (e.g.
// ["auto","4",...,"15"] for Seedance, ["5","10"] for Kling). It mirrors the
// lookup resolveVideoBody performs at submit time, so the Settings duration
// picker can show exactly the values the selected model won't 422 on.
//
// Returns nil when the schema is unavailable (offline, fetch failed, no key)
// or the model has no duration control — callers fall back to a generic option
// set rather than blocking the UI. Nil-safe throughout: a nil schema, nil app,
// or empty model all yield nil. findNative is nil-schema-safe (returns false).
func videoDurationOptions(ctx context.Context, client *http.Client, storageRoot, model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	cache := newFalSchemaCache(client, storageRoot)
	overrides := loadFalOverrides(storageRoot)
	schema := cache.Get(ctx, model)
	if _, prop, ok := findNative(schema, overrides, "video", model, "duration"); ok {
		return prop.Enum
	}
	return nil
}
