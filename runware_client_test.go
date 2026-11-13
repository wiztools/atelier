package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// runwareFluxSchemaFixture is a trimmed copy of the real
// runware.ai/docs/models/bfl-flux-1-dev/schema.json shape: the task under
// components.schemas.RequestBody.items, inputs nested one level.
const runwareFluxSchemaFixture = `{
  "openapi": "3.1.0",
  "info": {"x-air-id": "runware:101@1"},
  "components": {"schemas": {
    "RequestBody": {"type": "array", "items": {
      "type": "object", "additionalProperties": false,
      "required": ["positivePrompt", "width", "height", "taskType", "taskUUID", "model"],
      "properties": {
        "model": {"type": "string", "const": "runware:101@1"},
        "steps": {"type": "integer", "minimum": 1, "maximum": 100, "default": 28},
        "inputs": {"type": "object", "properties": {
          "seedImage": {"type": "string"}, "maskImage": {"type": "string"}}},
        "positivePrompt": {"type": "string"},
        "width": {"type": "integer", "minimum": 128, "maximum": 2048, "multipleOf": 64},
        "height": {"type": "integer", "minimum": 128, "maximum": 2048, "multipleOf": 64},
        "taskType": {"type": "string"}, "taskUUID": {"type": "string"},
        "includeCost": {"type": "boolean"},
        "outputType": {"type": "string", "enum": ["URL", "base64Data", "dataURI"]}
      }}},
    "ResponseBody": {"type": "object"},
    "ErrorResponse": {"type": "object"}
  }}
}`

const runwareEditSchemaFixture = `{
  "components": {"schemas": {
    "RequestBody": {"type": "array", "items": {"type": "object", "properties": {
      "positivePrompt": {"type": "string"},
      "width": {"type": "integer", "minimum": 256, "maximum": 16383},
      "height": {"type": "integer", "minimum": 256, "maximum": 16383},
      "resolution": {"type": "string", "enum": ["1K", "1.5K", "2K"]},
      "inputs": {"type": "object", "properties": {
        "referenceImages": {"type": "array", "maxItems": 2, "items": {"type": "string"}}}}
    }}}
  }}
}`

func parseRunwareFixture(t *testing.T, raw string) *ModelInputSchema {
	t.Helper()
	wrapped, err := rewrapRunwareSchema([]byte(raw))
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	schema, err := parseModelInputSchema(wrapped)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return schema
}

func TestRewrapRunwareSchemaParsesTaskProperties(t *testing.T) {
	schema := parseRunwareFixture(t, runwareFluxSchemaFixture)
	for _, name := range []string{"positivePrompt", "width", "height", "steps", "inputs"} {
		if _, ok := schema.property(name); !ok {
			t.Fatalf("missing property %q", name)
		}
	}
	inputs, _ := schema.property("inputs")
	if _, ok := inputs.Nested["seedImage"]; !ok {
		t.Fatalf("inputs.seedImage not captured: %+v", inputs.Nested)
	}
	if w, _ := schema.property("width"); w.Maximum == nil || *w.Maximum != 2048 {
		t.Fatalf("width bounds lost: %+v", w)
	}
}

func TestResolveRunwareImageTask(t *testing.T) {
	flux := parseRunwareFixture(t, runwareFluxSchemaFixture)
	edit := parseRunwareFixture(t, runwareEditSchemaFixture)
	png := "data:image/png;base64,AAAA"

	tests := []struct {
		name       string
		schema     *ModelInputSchema
		req        ImageGenerateRequest
		check      func(t *testing.T, task map[string]any, notices []string)
		wantErrSub string
	}{
		{
			name:   "text to image snaps size onto the 64 grid and declares outputs",
			schema: flux,
			req:    ImageGenerateRequest{Model: "runware:101@1", Prompt: "a fox", Width: 1000, Height: 3000, Steps: 500},
			check: func(t *testing.T, task map[string]any, notices []string) {
				if task["taskType"] != "imageInference" || task["positivePrompt"] != "a fox" {
					t.Fatalf("task core wrong: %v", task)
				}
				if task["width"] != 1024 || task["height"] != 2048 {
					t.Fatalf("size = %v x %v, want 1024 x 2048", task["width"], task["height"])
				}
				if task["steps"] != 100 {
					t.Fatalf("steps not clamped: %v", task["steps"])
				}
				if task["outputType"] != "dataURI" || task["includeCost"] != true {
					t.Fatalf("output/cost flags missing: %v", task)
				}
				if len(notices) != 1 || !strings.Contains(notices[0], "1024x2048") {
					t.Fatalf("notices = %v", notices)
				}
				if uuid, _ := task["taskUUID"].(string); len(uuid) != 36 || uuid[14] != '4' {
					t.Fatalf("taskUUID not a v4 uuid: %q", uuid)
				}
			},
		},
		{
			name:   "image-to-image model takes the first image as seedImage",
			schema: flux,
			req:    ImageGenerateRequest{Model: "runware:101@1", Prompt: "p", Images: []string{png, png}},
			check: func(t *testing.T, task map[string]any, notices []string) {
				inputs := task["inputs"].(map[string]any)
				if inputs["seedImage"] != png || inputs["referenceImages"] != nil {
					t.Fatalf("inputs = %v", inputs)
				}
				if len(notices) != 1 || !strings.Contains(notices[0], "one source image") {
					t.Fatalf("notices = %v", notices)
				}
			},
		},
		{
			name:   "default tier keeps the pixels (they carry the aspect), never both",
			schema: edit,
			req:    ImageGenerateRequest{Model: "x:1@1", Prompt: "p", Width: 1344, Height: 768, Resolution: "2k"},
			check: func(t *testing.T, task map[string]any, notices []string) {
				if _, ok := task["resolution"]; ok || task["width"] != 1344 || task["height"] != 768 {
					t.Fatalf("task = %v, want pixels without resolution", task)
				}
			},
		},
		{
			name:   "explicit tier wins over pixels; references capped at maxItems",
			schema: edit,
			req:    ImageGenerateRequest{Model: "x:1@1", Prompt: "p", Width: 1344, Height: 768, Resolution: "2k", ResolutionExplicit: true, Images: []string{png, png, png}},
			check: func(t *testing.T, task map[string]any, notices []string) {
				if _, ok := task["width"]; ok {
					t.Fatalf("width sent beside resolution: %v", task)
				}
				if task["resolution"] != "2K" {
					t.Fatalf("resolution = %v", task["resolution"])
				}
				refs := task["inputs"].(map[string]any)["referenceImages"].([]string)
				if len(refs) != 2 {
					t.Fatalf("refs = %d, want 2", len(refs))
				}
			},
		},
		{
			name:       "a model without inputs refuses an edit",
			schema:     parseRunwareFixture(t, `{"components":{"schemas":{"RequestBody":{"items":{"properties":{"positivePrompt":{"type":"string"}}}}}}}`),
			req:        ImageGenerateRequest{Model: "x:2@1", Prompt: "p", Images: []string{png}},
			wantErrSub: "takes no input images",
		},
		{
			name:   "no schema sends the universal fields with a notice",
			schema: nil,
			req:    ImageGenerateRequest{Model: "x:3@1", Prompt: "p", Images: []string{png}},
			check: func(t *testing.T, task map[string]any, notices []string) {
				if task["width"] != 1024 || task["inputs"].(map[string]any)["referenceImages"] == nil {
					t.Fatalf("fallback task = %v", task)
				}
				if len(notices) != 1 {
					t.Fatalf("notices = %v", notices)
				}
			},
		},
		{
			name:       "empty prompt is refused",
			schema:     flux,
			req:        ImageGenerateRequest{Model: "runware:101@1", Prompt: "  "},
			wantErrSub: "needs a prompt",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task, notices, err := resolveRunwareImageTask(tc.schema, tc.req)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("err = %v, want %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tc.check(t, task, notices)
		})
	}
}

func runwareTestClient(fn roundTripFunc) *RunwareClient {
	return newRunwareClient(&http.Client{Transport: fn}, "rw-test-key")
}

func runwareResponseOf(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRunwareGenerateImageReportsCostAndInlineImages(t *testing.T) {
	var sent []map[string]any
	client := runwareTestClient(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer rw-test-key" {
			t.Fatalf("auth header = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatalf("body: %v", err)
		}
		uuid := sent[0]["taskUUID"]
		return runwareResponseOf(200, `{"data":[
			{"taskType":"imageInference","taskUUID":"`+uuid.(string)+`","imageDataURI":"data:image/png;base64,QUJD","cost":0.0038},
			{"taskType":"imageInference","taskUUID":"someone-else","imageDataURI":"data:image/png;base64,WFla","cost":9}
		]}`), nil
	})
	task, _, err := resolveRunwareImageTask(nil, ImageGenerateRequest{Model: "runware:101@1", Prompt: "fox"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.GenerateImage(context.Background(), task)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(sent) != 1 || sent[0]["taskType"] != "imageInference" {
		t.Fatalf("request = %v", sent)
	}
	if len(resp.Images) != 1 || resp.Image != "data:image/png;base64,QUJD" {
		t.Fatalf("images = %v (a foreign taskUUID must not be harvested)", resp.Images)
	}
	if resp.CostMicros != 3800 {
		t.Fatalf("cost = %d micros, want 3800", resp.CostMicros)
	}
}

func TestRunwareErrorsMapOntoRemediationVocabulary(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"errors":[{"code":"invalidApiKey","message":"Invalid API key"}]}`, "runware authentication failed: Invalid API key"},
		{402, `{"errors":[{"code":"insufficientCredits","message":"Your account balance is insufficient"}]}`, "runware credit exhausted"},
		{429, `{"errors":[{"code":"inferenceRateLimitExceeded","message":"slow down"}]}`, "runware rate limited: slow down"},
		{400, `{"errors":[{"code":"invalidParameter","message":"bad width","parameter":"width"}]}`, "bad width (parameter width)"},
		{200, `{"errors":[{"code":"x","message":"model failed"}]}`, "runware request failed: model failed"},
		{200, `{"data":[]}`, "runware returned no images"},
	}
	for _, tc := range tests {
		client := runwareTestClient(func(*http.Request) (*http.Response, error) {
			return runwareResponseOf(tc.status, tc.body), nil
		})
		_, err := client.GenerateImage(context.Background(), map[string]any{"taskUUID": "u", "model": "m"})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("status %d: err = %v, want %q", tc.status, err, tc.want)
		}
	}
}

func TestRunwareListModelsKeepsLiveAIRAddressableEntries(t *testing.T) {
	runwareIndexCache.Lock()
	runwareIndexCache.models, runwareIndexCache.fetchedAt = nil, time.Time{}
	runwareIndexCache.Unlock()
	t.Cleanup(func() {
		runwareIndexCache.Lock()
		runwareIndexCache.models = nil
		runwareIndexCache.Unlock()
	})
	client := runwareTestClient(func(*http.Request) (*http.Response, error) {
		return runwareResponseOf(200, `[
			{"id":"bfl-flux-1-dev","air":"runware:101@1","name":"FLUX.1 [dev]","status":"live","capabilities":["text-to-image"],"schema":"https://x/s.json"},
			{"id":"no-air","name":"Docs only","status":"live","capabilities":["text-to-image"]},
			{"id":"retired","air":"r:1@1","name":"Old","status":"deprecated","capabilities":["text-to-image"]},
			{"id":"ace","air":"runware:ace@1","name":"ACE","status":"live","capabilities":["text-to-audio"]}
		]`), nil
	})
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "runware:ace@1" || models[1].ID != "runware:101@1" {
		t.Fatalf("models = %+v", models)
	}
	if !runwareModelHasCapability(models[1], "edit", "text-to-image") || runwareModelHasCapability(models[0], "text-to-image") {
		t.Fatal("capability filter wrong")
	}
}

func TestRunwareImageProviderRouting(t *testing.T) {
	config := mergeAppConfig(AppConfig{Models: ConfigModels{ImageProvider: "runware"}})
	if got := imageGenerationProvider(config); got != "runware" {
		t.Fatalf("provider = %q", got)
	}
	if config.Providers.Runware.Model != defaultRunwareImageModel {
		t.Fatalf("model not seeded: %q", config.Providers.Runware.Model)
	}
	if resolveDefaultImageModel(config) != defaultRunwareImageModel || resolveDefaultImageEditModel(config) != defaultRunwareImageModel {
		t.Fatal("default models should both resolve to the one Runware slot")
	}
	if !imageGenerationConfigured(config) {
		t.Fatal("generate_image should be offered")
	}
	config.AirplaneMode = true
	if imageGenerationConfigured(config) || airplaneMediaError(config, "generate_image") == nil {
		t.Fatal("airplane mode must drop the cloud Runware route")
	}
}

// runwareKontextSchemaFixture trims the real bfl-flux-1-kontext-dev schema:
// free-integer width/height whose legal pairs live only in allOf's
// if(width|height) → then.oneOf of const pairs.
const runwareKontextSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object",
  "properties":{
    "positivePrompt":{"type":"string"},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "inputs":{"type":"object","properties":{"referenceImages":{"type":"array","minItems":1,"items":{"type":"string"}}}}
  },
  "allOf":[
    {"dependentRequired":{"width":["height"],"height":["width"]}},
    {"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
     "then":{"oneOf":[
       {"title":"1K (~21:9)","properties":{"width":{"const":1568},"height":{"const":672}}},
       {"title":"1K (5:3)","properties":{"width":{"const":1328},"height":{"const":800}}},
       {"title":"1K (3:2)","properties":{"width":{"const":1248},"height":{"const":832}}},
       {"title":"1K (1:1)","properties":{"width":{"const":1024},"height":{"const":1024}}},
       {"title":"1K (2:3)","properties":{"width":{"const":832},"height":{"const":1248}}},
       {"title":"1K (~9:21)","properties":{"width":{"const":672},"height":{"const":1568}}}
     ]}}
  ]}}}}}`

func TestRunwareSizePresetsFromSchema(t *testing.T) {
	schema := parseRunwareFixture(t, runwareKontextSchemaFixture)
	prop, ok := schema.property(runwareSizePresetsProperty)
	if !ok || strings.Join(prop.Enum, ",") != "1568x672,1328x800,1248x832,1024x1024,832x1248,672x1568" {
		t.Fatalf("presets = %v (ok=%v)", prop.Enum, ok)
	}
	// A schema without const pairs carries no synthetic property.
	if _, ok := parseRunwareFixture(t, runwareFluxSchemaFixture).property(runwareSizePresetsProperty); ok {
		t.Fatal("FLUX dev schema should carry no presets")
	}

	tests := []struct {
		name         string
		w, h         int
		wantW, wantH int
		wantNotice   bool
	}{
		{"square lands on the square preset", 1024, 1024, 1024, 1024, false},
		{"16:9 lands on 5:3, a shape change worth a notice", 1344, 768, 1328, 800, true},
		{"3:2 source keeps its shape at a different size, quietly", 1536, 1024, 1248, 832, false},
		{"tall portrait picks the tall preset", 720, 1680, 672, 1568, false},
		{"21:9 is matched exactly", 2560, 1097, 1568, 672, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task, notices, err := resolveRunwareImageTask(schema, ImageGenerateRequest{Model: "runware:106@1", Prompt: "p", Width: tc.w, Height: tc.h})
			if err != nil {
				t.Fatal(err)
			}
			if task["width"] != tc.wantW || task["height"] != tc.wantH {
				t.Fatalf("size = %vx%v, want %dx%d", task["width"], task["height"], tc.wantW, tc.wantH)
			}
			if (len(notices) > 0) != tc.wantNotice {
				t.Fatalf("notices = %v, want notice=%v", notices, tc.wantNotice)
			}
			if _, leaked := task[runwareSizePresetsProperty]; leaked {
				t.Fatal("the synthetic presets property must never reach the task")
			}
		})
	}
}

func TestRunwareResolveImageInputs(t *testing.T) {
	small := "data:image/png;base64," + strings.Repeat("A", 1024)
	large := "data:image/png;base64," + strings.Repeat("B", runwareUploadThreshold*4/3+400)
	const uuid = "25a9717a-f382-4f4c-ab1d-fa0e062e0d01"

	t.Run("only payloads over the threshold upload", func(t *testing.T) {
		var tasks []map[string]any
		client := runwareTestClient(func(r *http.Request) (*http.Response, error) {
			var sent []map[string]any
			_ = json.NewDecoder(r.Body).Decode(&sent)
			tasks = append(tasks, sent...)
			return runwareResponseOf(200, `{"data":[{"taskType":"mediaStorage","mediaUUID":"`+uuid+`","mediaURL":"https://mm.runware.ai/x.png"}]}`), nil
		})
		resolved, uploads, diags := client.ResolveImageInputs(context.Background(), []string{small, large, "https://example.com/a.png", uuid})
		if len(tasks) != 1 || tasks[0]["taskType"] != "mediaStorage" || tasks[0]["operation"] != "upload" || tasks[0]["media"] != large {
			t.Fatalf("upload tasks = %d %v", len(tasks), tasks)
		}
		if resolved[0] != small || resolved[1] != uuid || resolved[2] != "https://example.com/a.png" || resolved[3] != uuid {
			t.Fatalf("resolved = %v", resolved)
		}
		if len(uploads) != 1 || uploads[0] != uuid || len(diags) != 1 || !strings.Contains(diags[0], "uploaded as media") {
			t.Fatalf("uploads=%v diags=%v", uploads, diags)
		}
	})

	t.Run("a failed upload keeps the image inline with a diagnostic", func(t *testing.T) {
		client := runwareTestClient(func(*http.Request) (*http.Response, error) {
			return runwareResponseOf(500, `{"errors":[{"code":"x","message":"storage down"}]}`), nil
		})
		resolved, uploads, diags := client.ResolveImageInputs(context.Background(), []string{large})
		if resolved[0] != large || len(uploads) != 0 || len(diags) != 1 || !strings.Contains(diags[0], "storage down") {
			t.Fatalf("resolved=%.40q uploads=%v diags=%v", resolved[0], uploads, diags)
		}
	})

	t.Run("cleanup deletes each upload", func(t *testing.T) {
		var deleted []any
		client := runwareTestClient(func(r *http.Request) (*http.Response, error) {
			var sent []map[string]any
			_ = json.NewDecoder(r.Body).Decode(&sent)
			if sent[0]["operation"] == "delete" {
				deleted = append(deleted, sent[0]["media"])
			}
			return runwareResponseOf(200, `{"data":[{"taskType":"mediaStorage"}]}`), nil
		})
		client.CleanupUploads([]string{uuid})
		client.CleanupUploads(nil)
		if len(deleted) != 1 || deleted[0] != uuid {
			t.Fatalf("deleted = %v", deleted)
		}
	})

	t.Run("an uploaded UUID rides the task unwrapped", func(t *testing.T) {
		task, _, err := resolveRunwareImageTask(parseRunwareFixture(t, runwareFluxSchemaFixture), ImageGenerateRequest{Model: "runware:101@1", Prompt: "p", Width: 512, Height: 512, Images: []string{uuid}})
		if err != nil {
			t.Fatal(err)
		}
		if got := task["inputs"].(map[string]any)["seedImage"]; got != uuid {
			t.Fatalf("seedImage = %v, want the bare UUID", got)
		}
	})
}

func TestIsRunwareUUID(t *testing.T) {
	for in, want := range map[string]bool{
		"25a9717a-f382-4f4c-ab1d-fa0e062e0d01": true,
		"25A9717A-F382-4F4C-AB1D-FA0E062E0D01": true,
		"25a9717af3824f4cab1dfa0e062e0d01":     false,
		"25a9717a-f382-4f4c-ab1d-fa0e062e0d0g": false,
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYA": false,
	} {
		if got := isRunwareUUID(in); got != want {
			t.Errorf("isRunwareUUID(%q) = %v, want %v", in, got, want)
		}
	}
}
