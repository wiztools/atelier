package main

// runware_schema_rules_test.go pins the cross-field rule evaluator
// (runware_schema_rules.go): every keyword the catalog's rules use, the
// fail-open reading of anything else, and the size picker built on it. The
// rule shapes below are the ones the 2026-11 video catalog publishes (628
// rules across 88 models); two outcomes were verified live on 2026-11-24 —
// MiniMax H3 Fast refused a width beside a start frame exactly as its rule
// says, and Kling 4.0 accepted one exactly as the absence of a rule says.

import (
	"encoding/json"
	"testing"
)

// ruleHolds decodes one rule and evaluates it against value.
func ruleHolds(t *testing.T, rule string, value any) bool {
	t.Helper()
	var node any
	if err := json.Unmarshal([]byte(rule), &node); err != nil {
		t.Fatalf("rule %s does not decode: %v", rule, err)
	}
	return runwareRuleHolds(node, value)
}

func TestRunwareRuleHoldsKeywords(t *testing.T) {
	const (
		h3FrameRule   = `{"if":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]},"then":{"not":{"anyOf":[{"required":["width"]},{"required":["height"]}]}}}`
		notAllOfRule  = `{"not":{"allOf":[{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]},{"anyOf":[{"required":["width"]},{"required":["height"]}]}]}}`
		pairOnlyRule  = `{"if":{"properties":{"inputs":{"properties":{"frameImages":{"minItems":2}},"required":["frameImages"]}},"required":["inputs"]},"then":{"not":{"anyOf":[{"required":["width"]},{"required":["height"]}]}}}`
		resolutionXOR = `{"not":{"allOf":[{"required":["resolution"]},{"anyOf":[{"required":["width"]},{"required":["height"]}]}]}}`
	)
	frames := func(n int) map[string]any {
		list := make([]string, n)
		for i := range list {
			list[i] = "data:image/png;base64,QUJD"
		}
		return map[string]any{"inputs": map[string]any{"frameImages": list}}
	}
	with := func(task map[string]any, key string, value any) map[string]any {
		out := map[string]any{}
		for k, v := range task {
			out[k] = v
		}
		out[key] = value
		return out
	}

	for _, tc := range []struct {
		name  string
		rule  string
		value any
		want  bool
	}{
		// required
		{"required present", `{"required":["a"]}`, map[string]any{"a": 1}, true},
		{"required absent", `{"required":["a"]}`, map[string]any{}, false},
		{"required ignores a non-object", `{"required":["a"]}`, "text", true},
		// properties apply only to present keys
		{"nested required satisfied", `{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]}`, frames(1), true},
		{"nested required missing", `{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]}`, map[string]any{"inputs": map[string]any{}}, false},
		{"parent itself required", `{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]}`, map[string]any{}, false},
		{"absent key is not judged", `{"properties":{"width":{"const":720}}}`, map[string]any{}, true},
		// not
		{"not over a satisfied schema", `{"not":{"required":["a"]}}`, map[string]any{"a": 1}, false},
		{"not over an unsatisfied schema", `{"not":{"required":["a"]}}`, map[string]any{}, true},
		// if / then / else
		{"if-then satisfied", `{"if":{"required":["a"]},"then":{"required":["b"]},"else":{"required":["c"]}}`, map[string]any{"a": 1, "b": 2}, true},
		{"if-then violated", `{"if":{"required":["a"]},"then":{"required":["b"]},"else":{"required":["c"]}}`, map[string]any{"a": 1}, false},
		{"else satisfied", `{"if":{"required":["a"]},"then":{"required":["b"]},"else":{"required":["c"]}}`, map[string]any{"c": 3}, true},
		{"else violated", `{"if":{"required":["a"]},"then":{"required":["b"]},"else":{"required":["c"]}}`, map[string]any{}, false},
		// anyOf / allOf / oneOf
		{"anyOf one branch", `{"anyOf":[{"required":["a"]},{"required":["b"]}]}`, map[string]any{"b": 1}, true},
		{"anyOf no branch", `{"anyOf":[{"required":["a"]},{"required":["b"]}]}`, map[string]any{}, false},
		{"allOf every branch", `{"allOf":[{"required":["a"]},{"required":["b"]}]}`, map[string]any{"a": 1, "b": 2}, true},
		{"allOf one missing", `{"allOf":[{"required":["a"]},{"required":["b"]}]}`, map[string]any{"a": 1}, false},
		{"oneOf exactly one", `{"oneOf":[{"required":["a"]},{"required":["b"]}]}`, map[string]any{"a": 1}, true},
		{"oneOf two branches", `{"oneOf":[{"required":["a"]},{"required":["b"]}]}`, map[string]any{"a": 1, "b": 2}, false},
		// const / enum — a task's Go int equals a rule's decoded float
		{"const int equals float", `{"const":1920}`, 1920, true},
		{"const same number as float", `{"const":1920}`, 1920.0, true},
		{"const other number", `{"const":1920}`, 1080, false},
		{"const is typed", `{"const":1920}`, "1920", false},
		{"enum member", `{"enum":["720p","1080p"]}`, "1080p", true},
		{"enum outsider", `{"enum":["720p","1080p"]}`, "4k", false},
		// arrays — resolvers build []string
		{"minItems met", `{"minItems":2}`, []string{"a", "b"}, true},
		{"minItems unmet", `{"minItems":2}`, []string{"a"}, false},
		{"maxItems met", `{"maxItems":1}`, []string{"a"}, true},
		{"maxItems exceeded", `{"maxItems":1}`, []string{"a", "b"}, false},
		{"items judges every element", `{"items":{"type":"string"}}`, []any{"a", 1}, false},
		{"contains wants one", `{"contains":{"const":"x"}}`, []any{"a", "x"}, true},
		{"contains none", `{"contains":{"const":"x"}}`, []any{"a", "b"}, false},
		// dependentRequired
		{"dependentRequired met", `{"dependentRequired":{"width":["height"]}}`, map[string]any{"width": 1, "height": 2}, true},
		{"dependentRequired unmet", `{"dependentRequired":{"width":["height"]}}`, map[string]any{"width": 1}, false},
		{"dependentRequired not triggered", `{"dependentRequired":{"width":["height"]}}`, map[string]any{"height": 2}, true},
		// numbers
		{"minimum inclusive", `{"minimum":4}`, 4, true},
		{"minimum below", `{"minimum":4}`, 3, false},
		{"maximum above", `{"maximum":12}`, 13, false},
		{"exclusiveMinimum as a number", `{"exclusiveMinimum":4}`, 4, false},
		{"exclusiveMinimum as a flag", `{"minimum":4,"exclusiveMinimum":true}`, 4, false},
		{"multipleOf met", `{"multipleOf":64}`, 704, true},
		{"multipleOf unmet", `{"multipleOf":64}`, 720, false},
		{"length bounds on a string", `{"maxLength":2}`, "abc", false},
		// type
		{"integer", `{"type":"integer"}`, 5, true},
		{"integer rejects a fraction", `{"type":"integer"}`, 5.5, false},
		{"integer rejects a string", `{"type":"integer"}`, "5", false},
		{"array", `{"type":"array"}`, []string{"a"}, true},
		{"object", `{"type":"object"}`, map[string]any{}, true},
		// A keyword the evaluator does not read places no constraint.
		{"unknown keywords pass", `{"format":"uuid","pattern":"^x","$comment":"y"}`, "plain", true},
		{"a bare true rule holds", `true`, map[string]any{}, true},
		{"a bare false rule fails", `false`, map[string]any{}, false},
		{"a non-schema node passes", `"required"`, map[string]any{}, true},
		// The catalog's real shapes.
		{"h3: a size beside one frame is forbidden", h3FrameRule, with(frames(1), "width", 480), false},
		{"h3: a size beside two frames is forbidden", h3FrameRule, with(frames(2), "height", 864), false},
		{"h3: a frame without a size passes", h3FrameRule, frames(1), true},
		{"h3: references are not frames", h3FrameRule, map[string]any{"inputs": map[string]any{"referenceImages": []string{"x"}}, "width": 480}, true},
		{"not-allOf form forbids the same", notAllOfRule, with(frames(1), "width", 480), false},
		{"not-allOf form allows a frame alone", notAllOfRule, frames(1), true},
		{"pair-only rule allows a size beside one frame", pairOnlyRule, with(frames(1), "width", 480), true},
		{"pair-only rule forbids it beside two", pairOnlyRule, with(frames(2), "width", 480), false},
		{"resolution and pixels never together", resolutionXOR, map[string]any{"resolution": "720p", "width": 1280}, false},
		{"resolution alone", resolutionXOR, map[string]any{"resolution": "720p"}, true},
	} {
		if got := ruleHolds(t, tc.rule, tc.value); got != tc.want {
			t.Errorf("%s: runwareRuleHolds = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRunwareTaskValid(t *testing.T) {
	task := map[string]any{"inputs": map[string]any{"frameImages": []string{"x"}}}

	if !runwareTaskValid(nil, task) {
		t.Fatal("a schema that could not be loaded must read as valid")
	}
	if !runwareTaskValid(&ModelInputSchema{}, task) {
		t.Fatal("a schema without rules must read as valid")
	}
	forbidden := `{"if":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]},"then":{"not":{"anyOf":[{"required":["width"]},{"required":["height"]}]}}}`
	sized := map[string]any{"inputs": map[string]any{"frameImages": []string{"x"}}, "width": 480, "height": 864}
	if runwareTaskValid(&ModelInputSchema{AllOf: []json.RawMessage{json.RawMessage(forbidden)}}, sized) {
		t.Fatal("a size beside a frame on a model forbidding it must read as invalid")
	}
	// A rule that does not decode teaches nothing and is skipped.
	skipped := &ModelInputSchema{AllOf: []json.RawMessage{json.RawMessage(`{"not":`), json.RawMessage(forbidden)}}
	if runwareTaskValid(skipped, sized) {
		t.Fatal("an unreadable rule must not hide the readable one after it")
	}
	if !runwareTaskValid(&ModelInputSchema{AllOf: []json.RawMessage{json.RawMessage(`{"not":`)}}, sized) {
		t.Fatal("an unreadable rule on its own places no constraint")
	}
}

func TestRunwareSizeValidLeavesTheTaskAlone(t *testing.T) {
	schema := &ModelInputSchema{AllOf: []json.RawMessage{json.RawMessage(`{"not":{"required":["width"]}}`)}}
	bare := map[string]any{"duration": 5}
	if runwareSizeValid(schema, bare, 480, 864) {
		t.Fatal("a rule forbidding width must reject the size")
	}
	if _, present := bare["width"]; present {
		t.Fatal("the evaluated size must be taken back out of a task that had none")
	}
	if _, present := bare["height"]; present {
		t.Fatal("the evaluated size must be taken back out of a task that had none")
	}
	preset := map[string]any{"width": 1280, "height": 720}
	runwareSizeValid(schema, preset, 480, 864)
	if preset["width"] != 1280 || preset["height"] != 720 {
		t.Fatalf("a size the task already carried must be restored, got %vx%v", preset["width"], preset["height"])
	}
}

func TestParseModelInputSchemaKeepsAllOf(t *testing.T) {
	withRules, err := parseModelInputSchema([]byte(`{"components":{"schemas":{"FooInput":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"allOf":[{"dependentRequired":{"a":["b"]}},{"required":["c"]}]}}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(withRules.AllOf) != 2 || withRules.Required[0] != "a" {
		t.Fatalf("AllOf = %d rule(s), Required = %v; want both rules kept beside the required list", len(withRules.AllOf), withRules.Required)
	}
	if runwareTaskValid(withRules, map[string]any{"a": "x"}) {
		t.Fatal("a task missing the rule's `c` must read as invalid")
	}
	without, err := parseModelInputSchema([]byte(`{"components":{"schemas":{"BarInput":{"type":"object","properties":{"a":{"type":"string"}}}}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if without.AllOf != nil {
		t.Fatalf("AllOf = %v, want nil for a schema without rules", without.AllOf)
	}
	if !runwareTaskValid(without, map[string]any{}) {
		t.Fatal("a schema without rules accepts any task")
	}
}

// runwareWhitelistSchema trims Vidu Q2 Pro's two size rules onto the smallest
// schema the picker needs: five sizes are allowed beside a frame, two of the
// wider presets are not.
const runwareWhitelistSchema = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object",
  "properties":{
    "positivePrompt":{"type":"string"},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "inputs":{"type":"object","properties":{"frameImages":{"type":"array","maxItems":2,"items":{"type":"string"}}}}
  },
  "allOf":[
   {"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
    "then":{"oneOf":[
     {"properties":{"width":{"const":1920},"height":{"const":1080}}},
     {"properties":{"width":{"const":1080},"height":{"const":1920}}},
     {"properties":{"width":{"const":720},"height":{"const":720}}},
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":960},"height":{"const":528}}},
     {"properties":{"width":{"const":640},"height":{"const":640}}}
   ]}},
   {"if":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]},
    "then":{"oneOf":[
     {"properties":{"width":{"const":1920},"height":{"const":1080}}},
     {"properties":{"width":{"const":1080},"height":{"const":1920}}},
     {"properties":{"width":{"const":720},"height":{"const":720}}},
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}}
   ]}}
  ]}}}}}`

func TestRunwareValidSize(t *testing.T) {
	schema := parseRunwareFixture(t, runwareWhitelistSchema)
	frame := map[string]any{"inputs": map[string]any{"frameImages": []string{"data:image/png;base64,QUJD"}}}

	// Where the rules allow the normal pick, it is returned untouched.
	width, height, substituted, ok := runwareValidSize(schema, frame, "16:9", "1080p")
	if !ok || substituted || width != 1920 || height != 1080 {
		t.Fatalf("allowed pick = %dx%d substituted=%v ok=%v, want 1920x1080 straight", width, height, substituted, ok)
	}
	if _, present := frame["width"]; present {
		t.Fatal("the picker must not leave a size in the caller's task")
	}

	// A pick outside what a frame allows moves to the nearest size that is:
	// the 528p class holds one landscape preset, and it is not one of the
	// five. Between the two allowed 16:9 shapes the one nearer the asked-for
	// area wins.
	width, height, substituted, ok = runwareValidSize(schema, frame, "16:9", "528p")
	if !ok || !substituted || width != 1280 || height != 720 {
		t.Fatalf("whitelisted pick = %dx%d substituted=%v ok=%v, want the allowed 1280x720 flagged as substituted", width, height, substituted, ok)
	}

	// Without frames the wider list applies, so the same ask is not
	// substituted.
	width, height, substituted, ok = runwareValidSize(schema, map[string]any{}, "16:9", "528p")
	if !ok || substituted || width != 960 || height != 528 {
		t.Fatalf("unframed pick = %dx%d substituted=%v ok=%v, want 960x528 straight", width, height, substituted, ok)
	}

	// Nothing to shape from, or no schema, is not a size.
	if _, _, _, ok := runwareValidSize(schema, frame, "auto", "1080p"); ok {
		t.Fatal(`"auto" asks for no shape`)
	}
	if _, _, _, ok := runwareValidSize(nil, frame, "16:9", "1080p"); ok {
		t.Fatal("without a schema no size can be known to be allowed")
	}

	// A model forbidding every size beside a frame has none to offer.
	forbidden := parseRunwareFixture(t, `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
	  "type":"object",
	  "properties":{
	    "width":{"type":"integer"},"height":{"type":"integer"},
	    "inputs":{"type":"object","properties":{"frameImages":{"type":"array","items":{"type":"string"}}}}
	  },
	  "allOf":[
	   {"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
	    "then":{"oneOf":[{"properties":{"width":{"const":1280},"height":{"const":720}}}]}},
	   {"if":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]},
	    "then":{"not":{"anyOf":[{"required":["width"]},{"required":["height"]}]}}}
	  ]}}}}}`)
	if _, _, _, ok := runwareValidSize(forbidden, frame, "16:9", "720p"); ok {
		t.Fatal("a size the rules forbid beside a frame must not be offered")
	}
}
