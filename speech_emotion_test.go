package main

import (
	"context"
	"testing"
)

// TestHarnessPlanSchemaAdmitsEmotion pins that the format-schema planner
// grammar frees generate_speech's emotion like the per-tool param schema does
// — the two are maintained separately, and a grammar without the property
// silently takes the tone control away from every non-native-tools planner.
func TestHarnessPlanSchemaAdmitsEmotion(t *testing.T) {
	registry := defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
	schema := harnessToolPlanSchema(registry)
	toolCalls, ok := schema["properties"].(map[string]any)["toolCalls"].(map[string]any)
	if !ok {
		t.Fatalf("plan schema = %#v, want a toolCalls property", schema)
	}
	items, ok := toolCalls["items"].(map[string]any)
	if !ok {
		t.Fatalf("toolCalls schema = %#v, want items", toolCalls)
	}
	properties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("toolCalls items = %#v, want properties", items)
	}
	if _, ok := properties["emotion"].(map[string]any); !ok {
		t.Errorf("plan grammar toolCalls properties missing emotion (has %v)", propertiesKeys(properties))
	}
}

// TestApplyKwargsMapsSpeechEmotion pins the kwarg-dialect decode: a planner
// that emits its call Python-style still fills the emotion field the JSON plan
// path fills through the struct tag.
func TestApplyKwargsMapsSpeechEmotion(t *testing.T) {
	var call HarnessToolCall
	applyKwargs(&call, "content='Read this sadly.', emotion='sad'")
	if call.Content != "Read this sadly." || call.Emotion != "sad" {
		t.Fatalf("applyKwargs = %+v, want content plus emotion=sad", call)
	}
}
