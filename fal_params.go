package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Overrides maps category → model-id → canonical → native path. A native path of
// "" means the canonical param is explicitly unsupported (drop-with-notice).
// Missing entries fall through to schema heuristics.
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

// builtinFalOverrides holds defaults for models the heuristics get wrong. Empty
// today; entries are added as such models are discovered.
func builtinFalOverrides() Overrides {
	return Overrides{byCategory: map[string]map[string]map[string]string{
		"audio": {},
	}}
}

// loadFalOverrides reads <storageRoot>/fal-overrides.json and merges it OVER the
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

// audioSynonyms lists, per canonical param, the native key names to look for in
// a model's schema (scanned in order, top-level then one-level nested).
var audioSynonyms = map[string][]string{
	"prompt":         {"prompt", "text"},
	"duration":       {"duration_seconds", "duration", "music_length_ms"},
	"loop":           {"loop"},
	"voice":          {"voice", "voice_id", "voice_name", "speaker", "speaker_id"},
	"negativePrompt": {"negative_prompt"},
}

type canonicalValue struct {
	canon   string
	value   any
	present bool
}

// canonicalAudioValues yields the non-prompt canonical params to resolve, in a
// stable order. prompt is handled separately (always required).
func canonicalAudioValues(req AudioGenerateRequest) []canonicalValue {
	return []canonicalValue{
		{"duration", strings.TrimSpace(req.Duration), strings.TrimSpace(req.Duration) != ""},
		{"loop", req.Loop, req.Loop},
		{"voice", strings.TrimSpace(req.Voice), strings.TrimSpace(req.Voice) != ""},
		{"negativePrompt", strings.TrimSpace(req.NegativePrompt), strings.TrimSpace(req.NegativePrompt) != ""},
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
		body["prompt"], body["text"] = prompt, prompt
	}

	for _, item := range canonicalAudioValues(req) {
		if !item.present {
			continue
		}
		path, prop, ok := findNative(schema, ov, req.Model, item.canon)
		if !ok {
			label := canonLabel(item.canon)
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no %s control; ignoring the requested %s.",
				req.Model, label, label))
			continue
		}
		value, notice := coerceValue(item.canon, prop, item.value, req.Model)
		if notice != "" {
			notices = append(notices, notice)
			continue
		}
		setBodyPath(schema, body, path, value)
	}
	return body, notices
}

// findNative resolves canon → native dot-path via override, top-level scan, then
// one-level nested scan. Returns the matched leaf property for coercion.
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
func coerceValue(canon string, prop SchemaProperty, value any, model string) (any, string) {
	switch canon {
	case "duration":
		secs, err := strconv.ParseFloat(fmt.Sprintf("%v", value), 64)
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
