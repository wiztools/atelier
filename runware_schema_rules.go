package main

// runware_schema_rules.go evaluates a candidate task against a Runware
// model's own cross-field rules. The packagings publish their real contracts
// as JSON Schema `allOf` beside the properties — "when inputs.frameImages is
// present, width/height must not be sent", "a size beside frames must be one
// of these five", "resolution requires input media" — and an endpoint that
// enforces them answers a 400 before anything is generated (MiniMax H3 Fast
// on a width beside a start frame: "Unsupported use of 'height' parameter.
// This parameter is not supported for the selected model."). A property-level
// parse cannot see those rules, so the resolvers used to learn them only by
// asking; this is the offline answer. Enforcement is not uniform — PixVerse
// V6 accepted a size beside reference images its schema forbids — so the
// schema is read as the contract, the way every other field already is.
//
// runwareTaskValid is a small evaluator for exactly the keywords the
// catalog's rules use (628 rules across 88 video models as of 2026-11:
// if/then/else, not, anyOf, allOf, oneOf, required, properties, const, enum,
// type, dependentRequired, minItems, maxItems, contains, items, and the
// numeric and length bounds) and nothing else. A rule using an unknown
// keyword is read as placing no constraint, so a gap here can only ever mean
// "send it and let the endpoint judge", never a false refusal.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// runwareTaskValid reports whether task violates none of the schema's allOf
// rules. A schema that could not be loaded, or one without rules, is valid.
func runwareTaskValid(schema *ModelInputSchema, task map[string]any) bool {
	if schema == nil {
		return true
	}
	for _, rule := range schema.AllOf {
		var node any
		if json.Unmarshal(rule, &node) != nil {
			continue
		}
		if !runwareRuleHolds(node, task) {
			return false
		}
	}
	return true
}

// runwareSizeValid reports whether the schema's rules allow an output size
// beside everything else the task already carries. The size is written only
// for the evaluation and taken back out, so the caller's task is untouched.
func runwareSizeValid(schema *ModelInputSchema, task map[string]any, width, height int) bool {
	prevWidth, hadWidth := task["width"]
	prevHeight, hadHeight := task["height"]
	task["width"], task["height"] = width, height
	valid := runwareTaskValid(schema, task)
	if hadWidth {
		task["width"] = prevWidth
	} else {
		delete(task, "width")
	}
	if hadHeight {
		task["height"] = prevHeight
	} else {
		delete(task, "height")
	}
	return valid
}

// runwareValidSize picks the output size for an aspect that the model's rules
// allow beside the task's inputs: the normal pick (runwareVideoDimensions —
// the requested aspect at the tier's class) where it is allowed, else the
// nearest preset that is (Vidu Q2 whitelists five sizes beside a frame, so a
// 21:9 ask there lands on the nearest of the five). substituted reports that
// the second path was taken. False when no size is allowed beside these
// inputs — MiniMax H3 beside a start frame — or when the model takes no
// pixels at all.
func runwareValidSize(schema *ModelInputSchema, task map[string]any, aspect, tier string) (width, height int, substituted, ok bool) {
	if schema == nil {
		return 0, 0, false, false
	}
	width, height, ok = runwareVideoDimensions(schema, aspect, tier)
	if ok && runwareSizeValid(schema, task, width, height) {
		return width, height, false, true
	}
	presetProp, hasPresets := schema.property(runwareSizePresetsProperty)
	aw, ah, hasAspect := parseAspectRatio(strings.TrimSpace(aspect))
	if !hasPresets || !hasAspect || isAutoAspectRatio(strings.TrimSpace(aspect)) {
		return 0, 0, false, false
	}
	want := math.Log(float64(aw) / float64(ah))
	// The area of the pick that was asked for, kept before the loop reuses
	// width/height for the candidate that is winning.
	targetArea := float64(width) * float64(height)
	best, bestArea := math.MaxFloat64, 0.0
	found := false
	for _, preset := range presetProp.Enum {
		var pw, ph int
		if _, err := fmt.Sscanf(preset, "%dx%d", &pw, &ph); err != nil || pw <= 0 || ph <= 0 {
			continue
		}
		if !runwareSizeValid(schema, task, pw, ph) {
			continue
		}
		dist := math.Abs(math.Log(float64(pw)/float64(ph)) - want)
		area := float64(pw) * float64(ph)
		// Nearest aspect wins; equal distances (two exact 16:9 shapes at
		// different tiers) go to the preset closest in area to the pick that
		// was asked for, so the tier still decides the size.
		if !found || dist < best-1e-9 || (math.Abs(dist-best) <= 1e-9 && closer(area, bestArea, targetArea)) {
			best, bestArea, found = dist, area, true
			width, height = pw, ph
		}
	}
	return width, height, found, found
}

// closer reports whether a sits nearer target than b does.
func closer(a, b, target float64) bool {
	return math.Abs(a-target) < math.Abs(b-target)
}

// runwareShapeSource names what fixes an image turn's shape, for notices.
func runwareShapeSource(referenceGuided, keyframes bool) string {
	switch {
	case referenceGuided:
		return "its reference images"
	case keyframes:
		return "the start and end frames"
	default:
		return "the start frame"
	}
}

// runwareRuleHolds reports whether value satisfies one rule node.
func runwareRuleHolds(rule, value any) bool {
	node, ok := rule.(map[string]any)
	if !ok {
		// JSON Schema allows bare boolean schemas; these packagings never
		// use them, and a non-schema node places no constraint.
		if holds, ok := rule.(bool); ok {
			return holds
		}
		return true
	}
	if list, ok := node["allOf"].([]any); ok {
		for _, sub := range list {
			if !runwareRuleHolds(sub, value) {
				return false
			}
		}
	}
	if list, ok := node["anyOf"].([]any); ok && len(list) > 0 {
		any := false
		for _, sub := range list {
			if runwareRuleHolds(sub, value) {
				any = true
				break
			}
		}
		if !any {
			return false
		}
	}
	if list, ok := node["oneOf"].([]any); ok && len(list) > 0 {
		count := 0
		for _, sub := range list {
			if runwareRuleHolds(sub, value) {
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	if sub, ok := node["not"]; ok && runwareRuleHolds(sub, value) {
		return false
	}
	if cond, ok := node["if"]; ok {
		branch, hasBranch := node["else"]
		if runwareRuleHolds(cond, value) {
			branch, hasBranch = node["then"]
		}
		if hasBranch && !runwareRuleHolds(branch, value) {
			return false
		}
	}
	return runwareRuleApplies(node, value)
}

// runwareRuleApplies applies the node's own keywords to the value. Following
// JSON Schema, an applicator ignores a value of the wrong kind: `required`
// says nothing about a string.
func runwareRuleApplies(node map[string]any, value any) bool {
	if obj, ok := value.(map[string]any); ok {
		if names, ok := node["required"].([]any); ok {
			for _, name := range names {
				if key, ok := name.(string); ok {
					if _, present := obj[key]; !present {
						return false
					}
				}
			}
		}
		if props, ok := node["properties"].(map[string]any); ok {
			for name, sub := range props {
				if child, present := obj[name]; present && !runwareRuleHolds(sub, child) {
					return false
				}
			}
		}
		if deps, ok := node["dependentRequired"].(map[string]any); ok {
			for name, needs := range deps {
				if _, present := obj[name]; !present {
					continue
				}
				list, ok := needs.([]any)
				if !ok {
					continue
				}
				for _, need := range list {
					if key, ok := need.(string); ok {
						if _, present := obj[key]; !present {
							return false
						}
					}
				}
			}
		}
	}
	if want, ok := node["const"]; ok && !runwareJSONEqual(want, value) {
		return false
	}
	if list, ok := node["enum"].([]any); ok && len(list) > 0 {
		match := false
		for _, want := range list {
			if runwareJSONEqual(want, value) {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	if name, ok := node["type"].(string); ok && !runwareJSONTypeIs(name, value) {
		return false
	}
	if n, ok := runwareJSONNumber(value); ok {
		if bound, ok := runwareJSONNumber(node["minimum"]); ok {
			if exclusive, _ := node["exclusiveMinimum"].(bool); (exclusive && n <= bound) || (!exclusive && n < bound) {
				return false
			}
		}
		if bound, ok := runwareJSONNumber(node["maximum"]); ok {
			if exclusive, _ := node["exclusiveMaximum"].(bool); (exclusive && n >= bound) || (!exclusive && n > bound) {
				return false
			}
		}
		// OpenAPI 3.1 writes an exclusive bound as a number of its own.
		if bound, ok := runwareJSONNumber(node["exclusiveMinimum"]); ok && n <= bound {
			return false
		}
		if bound, ok := runwareJSONNumber(node["exclusiveMaximum"]); ok && n >= bound {
			return false
		}
		if step, ok := runwareJSONNumber(node["multipleOf"]); ok && step > 0 {
			if math.Abs(n/step-math.Round(n/step)) > 1e-6 {
				return false
			}
		}
	}
	if text, ok := value.(string); ok {
		if limit, ok := runwareJSONNumber(node["maxLength"]); ok && float64(len([]rune(text))) > limit {
			return false
		}
		if limit, ok := runwareJSONNumber(node["minLength"]); ok && float64(len([]rune(text))) < limit {
			return false
		}
	}
	if items, ok := runwareJSONSlice(value); ok {
		if limit, ok := runwareJSONNumber(node["minItems"]); ok && float64(len(items)) < limit {
			return false
		}
		if limit, ok := runwareJSONNumber(node["maxItems"]); ok && float64(len(items)) > limit {
			return false
		}
		if itemRule, ok := node["items"].(map[string]any); ok {
			for _, item := range items {
				if !runwareRuleHolds(itemRule, item) {
					return false
				}
			}
		}
		if contains, ok := node["contains"]; ok {
			some := false
			for _, item := range items {
				if runwareRuleHolds(contains, item) {
					some = true
					break
				}
			}
			if !some {
				return false
			}
		}
	}
	return true
}

// runwareJSONEqual compares a rule's constant with a task value. Numbers
// compare numerically: tasks carry Go ints where a decoded rule has float64.
func runwareJSONEqual(want, value any) bool {
	if w, ok := runwareJSONNumber(want); ok {
		v, ok := runwareJSONNumber(value)
		return ok && w == v
	}
	if w, ok := want.(string); ok {
		v, ok := value.(string)
		return ok && w == v
	}
	return want == value
}

// runwareJSONNumber reads a JSON or Go numeric value.
func runwareJSONNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// runwareJSONSlice reads a JSON or Go slice: resolvers build some task lists
// as []string, decoded rules only ever see []any.
func runwareJSONSlice(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	case []string:
		out := make([]any, len(s))
		for i, item := range s {
			out[i] = item
		}
		return out, true
	}
	return nil, false
}

// runwareJSONTypeIs checks a value against one JSON Schema type name.
func runwareJSONTypeIs(name string, value any) bool {
	switch name {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := runwareJSONSlice(value)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := runwareJSONNumber(value)
		return ok
	case "integer":
		n, ok := runwareJSONNumber(value)
		return ok && n == math.Trunc(n)
	}
	return true
}
