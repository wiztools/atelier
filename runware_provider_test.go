package main

// runware_provider_test.go pins the runware backend's routing, gates,
// resolvers, and stand-downs — the speech_provider_test.go /
// replicate_client_test.go patterns one provider over. The resolver tables
// run against trimmed copies of the real published schemas (the
// runwareFluxSchemaFixture recipe); the gate tests pin live keychain state
// with keyring.MockInit like the replicate gating tests; the poll test
// drives the client against an httptest server standing in for api.runware.ai.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// runwareKlingSchemaFixture trims the real klingai-video-4-0 schema: integer
// duration 3-30, fixed width/height pairs in a oneOf, frameImages and video
// under inputs, no negativePrompt, and the deliveryMethod dial whose default
// is async (the async-ack guard's declared case).
const runwareKlingSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","minimum":3,"maximum":30,"default":10},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "resolution":{"type":"string","enum":["720p","1080p"]},
    "outputFormat":{"type":"string","enum":["MP4","WEBM","MOV"]},
    "deliveryMethod":{"type":"string","enum":["sync","async"],"default":"async"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":10,"items":{"type":"string"}},
      "referenceImages":{"type":"array","maxItems":10,"items":{"type":"string"}},
      "video":{"type":"string"}}}
  },
  "allOf":[{"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
   "then":{"oneOf":[
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":940},"height":{"const":940}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":1460},"height":{"const":624}}},
     {"properties":{"width":{"const":1920},"height":{"const":1080}}},
     {"properties":{"width":{"const":1440},"height":{"const":1440}}},
     {"properties":{"width":{"const":1080},"height":{"const":1920}}},
     {"properties":{"width":{"const":2200},"height":{"const":940}}}
   ]}}]}}}}}`

// runwareSeedanceSchemaFixture trims the real bytedance-seedance-2-0 schema:
// a four-member resolution enum spanning 480p–4k beside the same
// width/height preset pairs (the tier enum and the pixels are published
// together on every Runware video packaging, so the resolver can never
// decide between them by the model's shape alone).
const runwareSeedanceSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","minimum":3,"maximum":12},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "resolution":{"type":"string","enum":["480p","720p","1080p","4k"]},
    "deliveryMethod":{"type":"string","enum":["sync","async"],"default":"async"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":2,"items":{"type":"string"}},
      "video":{"type":"string"}}}
  },
  "allOf":[{"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
   "then":{"oneOf":[
     {"properties":{"width":{"const":864},"height":{"const":496}}},
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":1920},"height":{"const":1080}}},
     {"properties":{"width":{"const":1080},"height":{"const":1920}}},
     {"properties":{"width":{"const":3840},"height":{"const":2160}}},
     {"properties":{"width":{"const":2160},"height":{"const":3840}}}
   ]}}]}}}}}`

// runwareH3FastSchemaFixture trims the real minimax-h3-fast schema: a
// two-member tier enum capping at 768p — no H3 variant offers 1080p — beside
// the same width/height preset pairs, among them a ratio-EXACT 480p preset
// (864x480 = 1.8) that a ratio-first snap at 16:9 would pick over the
// slightly-off 768p one (1344x768 = 1.75) no matter the requested tier. The
// two cross-field rules the shape rule reads ride along as the live schema
// declares them (the property parse keeps them, runware_schema_rules.go
// evaluates them): a width/height is forbidden beside a start frame, and the
// resolution preset is valid only beside one — verified live 2026-11-24,
// where a width beside a square start frame answered "Unsupported use of
// 'height' parameter".
const runwareH3FastSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","minimum":4,"maximum":12},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "resolution":{"type":"string","enum":["480p","768p"]},
    "deliveryMethod":{"type":"string","enum":["sync","async"],"default":"async"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":2,"items":{"type":"string"}},
      "video":{"type":"string"}}}
  },
  "allOf":[{"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
   "then":{"oneOf":[
     {"properties":{"width":{"const":864},"height":{"const":480}}},
     {"properties":{"width":{"const":640},"height":{"const":480}}},
     {"properties":{"width":{"const":480},"height":{"const":480}}},
     {"properties":{"width":{"const":480},"height":{"const":640}}},
     {"properties":{"width":{"const":480},"height":{"const":864}}},
     {"properties":{"width":{"const":1120},"height":{"const":480}}},
     {"properties":{"width":{"const":1344},"height":{"const":768}}},
     {"properties":{"width":{"const":1024},"height":{"const":768}}},
     {"properties":{"width":{"const":768},"height":{"const":768}}},
     {"properties":{"width":{"const":768},"height":{"const":1024}}},
     {"properties":{"width":{"const":768},"height":{"const":1344}}},
     {"properties":{"width":{"const":1792},"height":{"const":768}}}
   ]}},
   {"if":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]},
    "then":{"not":{"anyOf":[{"required":["width"]},{"required":["height"]}]}}},
   {"if":{"required":["resolution"]},
    "then":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]}}
  ]}}}}}`

// runwareVeoSchemaFixture trims the real google-veo-3-1 schema: the extend
// contract — inputs.video is the continuation source, duration is an enum,
// and no width/height beside a source video (the conditional is invisible to
// the parser, so the resolver must withhold them itself).
const runwareVeoSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","enum":[4,6,7,8],"default":4},
    "outputFormat":{"type":"string","enum":["MP4","WEBM","MOV"]},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":2,"items":{"type":"string"}},
      "video":{"type":"string"}}}
  }}}}}}`

// runwareKlingFlashSchemaFixture trims the real klingai-video-4-0-flash
// schema: the Kling 4.0 shape with the one difference that matters to the
// keyframe gate — frameImages caps at maxItems 1 (the schema's own frame
// object reads "This model only supports the first frame") beside
// referenceImages maxItems 10, and no resolution dial (pixels only). It stands
// in for the first-frame-only packagings (Grok Imagine, Hailuo 2.3, Runway
// Gen-4.5, Wan 2.6, ...), every one of them tagged image-to-video and so
// listed in the same Settings picker as Kling 4.0.
const runwareKlingFlashSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","minimum":3,"maximum":20,"default":10},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "outputFormat":{"type":"string","enum":["MP4","WEBM","MOV"]},
    "deliveryMethod":{"type":"string","enum":["sync","async"],"default":"async"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":1,"items":{"type":"string"}},
      "referenceImages":{"type":"array","maxItems":10,"items":{"type":"string"}},
      "video":{"type":"string"}}}
  },
  "allOf":[{"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
   "then":{"oneOf":[
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":960},"height":{"const":960}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":1464},"height":{"const":628}}}
   ]}}]}}}}}`

// runwareSeedance1ProSchemaFixture trims the real bytedance-seedance-1-0-pro
// schema: inputs carries frameImages (maxItems 2) and nothing else — the
// frame-only shape, which animates one image or transitions between two but
// has no reference-image input to guide a clip with.
const runwareSeedance1ProSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"number","minimum":1.2,"maximum":12,"default":5},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "resolution":{"type":"string","enum":["480p","1080p"]},
    "deliveryMethod":{"type":"string","enum":["sync","async"],"default":"async"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":2,"items":{"type":"string"}}}}
  },
  "allOf":[{"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
   "then":{"oneOf":[
     {"properties":{"width":{"const":416},"height":{"const":960}}},
     {"properties":{"width":{"const":480},"height":{"const":864}}},
     {"properties":{"width":{"const":544},"height":{"const":736}}},
     {"properties":{"width":{"const":640},"height":{"const":640}}},
     {"properties":{"width":{"const":736},"height":{"const":544}}},
     {"properties":{"width":{"const":864},"height":{"const":480}}},
     {"properties":{"width":{"const":928},"height":{"const":2176}}},
     {"properties":{"width":{"const":960},"height":{"const":416}}},
     {"properties":{"width":{"const":1088},"height":{"const":1920}}},
     {"properties":{"width":{"const":1248},"height":{"const":1664}}},
     {"properties":{"width":{"const":1440},"height":{"const":1440}}},
     {"properties":{"width":{"const":1664},"height":{"const":1248}}},
     {"properties":{"width":{"const":1920},"height":{"const":1088}}},
     {"properties":{"width":{"const":2176},"height":{"const":928}}}
   ]}}]}}}}}`

// runwarePixVerseV6SchemaFixture trims the real pixverse-v6 schema around the
// rule that matters to the shape rule: ANY input medium (frames or reference
// images) forbids width/height, so the resolution preset is the only size
// signal beside media. PixVerse V6's endpoint accepted a size beside a
// reference image anyway when tried live (2026-11-24) — enforcement is not
// uniform, MiniMax H3 refused one beside a frame — and the resolver follows
// the schema in both cases.
const runwarePixVerseV6SchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","minimum":1,"maximum":15,"default":5},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "resolution":{"type":"string","enum":["360p","540p","720p","1080p"],"default":"720p"},
    "outputFormat":{"type":"string","enum":["MP4","WEBM","MOV"]},
    "deliveryMethod":{"type":"string","enum":["sync","async"],"default":"async"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":2,"items":{"type":"string"}},
      "referenceImages":{"type":"array","maxItems":10,"items":{"type":"string"}}}}
  },
  "allOf":[
   {"dependentRequired":{"width":["height"],"height":["width"]}},
   {"if":{"properties":{"inputs":{"anyOf":[{"required":["frameImages"]},{"required":["referenceImages"]}]}},"required":["inputs"]},"then":{"not":{"anyOf":[{"required":["width"]},{"required":["height"]}]}},"else":{"required":["width","height"]}},
   {"not":{"allOf":[{"required":["resolution"]},{"anyOf":[{"required":["width"]},{"required":["height"]}]}]}},
   {"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
    "then":{"oneOf":[
     {"properties":{"width":{"const":640},"height":{"const":360}}},
     {"properties":{"width":{"const":480},"height":{"const":360}}},
     {"properties":{"width":{"const":360},"height":{"const":360}}},
     {"properties":{"width":{"const":360},"height":{"const":480}}},
     {"properties":{"width":{"const":360},"height":{"const":640}}},
     {"properties":{"width":{"const":540},"height":{"const":540}}},
     {"properties":{"width":{"const":540},"height":{"const":720}}},
     {"properties":{"width":{"const":540},"height":{"const":960}}},
     {"properties":{"width":{"const":720},"height":{"const":540}}},
     {"properties":{"width":{"const":720},"height":{"const":720}}},
     {"properties":{"width":{"const":720},"height":{"const":960}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":960},"height":{"const":720}}},
     {"properties":{"width":{"const":1920},"height":{"const":1080}}},
     {"properties":{"width":{"const":1080},"height":{"const":1920}}}
   ]}}
  ]}}}}}`

// runwareViduQ2ProSchemaFixture trims the real vidu-q2-pro schema (its
// general preset list cut to ten of twenty) around the rule that matters to
// the shape rule: beside a start frame only FIVE sizes are allowed, a subset
// of its presets, and the model has no resolution preset at all — pixels are
// its only size signal.
const runwareViduQ2ProSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","minimum":1,"maximum":8,"default":5},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "outputFormat":{"type":"string","enum":["MP4","WEBM","MOV"]},
    "deliveryMethod":{"type":"string","enum":["sync","async"],"default":"async"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":2,"items":{"type":"string"}},
      "referenceImages":{"type":"array","maxItems":7,"items":{"type":"string"}}}}
  },
  "allOf":[
   {"dependentRequired":{"width":["height"],"height":["width"]}},
   {"if":{"not":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]}},
    "then":{"required":["width","height"]}},
   {"oneOf":[{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"]},
             {"properties":{"inputs":{"required":["referenceImages"]}},"required":["inputs"]}]},
   {"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
    "then":{"oneOf":[
     {"properties":{"width":{"const":640},"height":{"const":352}}},
     {"properties":{"width":{"const":544},"height":{"const":400}}},
     {"properties":{"width":{"const":480},"height":{"const":480}}},
     {"properties":{"width":{"const":400},"height":{"const":544}}},
     {"properties":{"width":{"const":352},"height":{"const":640}}},
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":960},"height":{"const":960}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":1920},"height":{"const":1080}}},
     {"properties":{"width":{"const":1080},"height":{"const":1920}}}
   ]}},
   {"if":{"properties":{"inputs":{"required":["frameImages"]}},"required":["inputs"],
        "anyOf":[{"required":["width"]},{"required":["height"]}]},
    "then":{"oneOf":[
     {"properties":{"width":{"const":720},"height":{"const":720}}},
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":1920},"height":{"const":1080}}},
     {"properties":{"width":{"const":1080},"height":{"const":1920}}}
   ]}}
  ]}}}}}`

// runwareSpeechSchemaFixture trims the real minimax-speech-2-8 schema: the
// nested speech object with a voice enum, no emotion or speed dials, dataURI
// output.
const runwareSpeechSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "required":["speech","taskType","taskUUID","model"],
  "properties":{
    "model":{"type":"string"},
    "taskType":{"type":"string"},
    "taskUUID":{"type":"string"},
    "speech":{"type":"object","properties":{
      "text":{"type":"string"},
      "voice":{"type":"string","enum":["English_expressive_narrator","Japanese_ColdQueen"]}}},
    "outputType":{"type":"string","enum":["URL","base64Data","dataURI"]},
    "includeCost":{"type":"boolean"}
  }}}}}}`

// runwareSoundSchemaFixture trims the real mirelo-sfx-1-5 schema: a required
// prompt, a 1-10s duration, no lyrics pipeline.
const runwareSoundSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string","minLength":2},
    "duration":{"type":"number","minimum":1,"maximum":10,"default":10},
    "outputType":{"type":"string","enum":["URL","base64Data","dataURI"]},
    "includeCost":{"type":"boolean"}
  }}}}}}`

// runwareMusicSchemaFixture trims the real minimax-music-2-6 schema: the
// settings.instrumental switch, NO duration control.
const runwareMusicSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "settings":{"type":"object","properties":{
      "instrumental":{"type":"boolean"},
      "lyrics":{"type":"string"}}},
    "includeCost":{"type":"boolean"}
  }}}}}}`

// runwareVideoSFXSchemaFixture adds the inputs.video field to the sound
// schema — the video-soundtrack contract (mirelo with an attached clip; its
// duration dial is mutually exclusive with the video input).
const runwareVideoSFXSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"number","minimum":1,"maximum":10},
    "preserveSpeech":{"type":"boolean"},
    "outputFormat":{"type":"string","enum":["MP3","WAV","MP4"]},
    "inputs":{"type":"object","properties":{
      "video":{"type":"string"}}}
  }}}}}}`

// runwareLipsyncSchemaFixture trims the real klingai-lip-sync schema: audio
// plus a video face, no prompt, no image face.
const runwareLipsyncSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "outputFormat":{"type":"string","enum":["MP4","WEBM","MOV"]},
    "inputs":{"type":"object","properties":{
      "video":{"type":"string"},
      "audio":{"type":"string"}}}
  }}}}}}`

// runwareFillSchemaFixture trims the real bfl-flux-1-fill-pro schema: inputs
// carry exactly seedImage + maskImage (the inpaint contract), and NO
// width/height — the fill models inherit the seed image's dimensions.
const runwareFillSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "required":["positivePrompt","inputs","taskType","taskUUID","model"],
  "properties":{
    "positivePrompt":{"type":"string"},
    "taskType":{"type":"string"},
    "taskUUID":{"type":"string"},
    "model":{"type":"string"},
    "includeCost":{"type":"boolean"},
    "numberResults":{"type":"integer"},
    "outputType":{"type":"string","enum":["URL","base64Data","dataURI"]},
    "outputFormat":{"type":"string","enum":["JPG","PNG","WEBP"]},
    "inputs":{"type":"object","additionalProperties":false,"required":["seedImage","maskImage"],"properties":{
      "seedImage":{"type":"string"},
      "maskImage":{"type":"string"}}}
  }}}}}}`

// runwareUpscaleSchemaFixture trims the real real-esrgan schema: the upscale
// contract — required inputs.image, a fixed [2,4] upscaleFactor enum, dataURI
// output. The other image upscalers share the shape with a numeric factor
// window instead of the enum (clarity: 1–4).
const runwareUpscaleSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "required":["taskType","taskUUID","model","inputs"],
  "properties":{
    "model":{"type":"string"},
    "taskType":{"type":"string"},
    "taskUUID":{"type":"string"},
    "includeCost":{"type":"boolean"},
    "upscaleFactor":{"type":"integer","enum":[2,4]},
    "outputType":{"type":"string","enum":["URL","base64Data","dataURI"]},
    "outputFormat":{"type":"string","enum":["JPG","PNG","WEBP"]},
    "inputs":{"type":"object","properties":{
      "image":{"type":"string"}}}
  }}}}}}`

// runwareClarityUpscaleSchemaFixture trims the real clarity schema: the same
// upscale contract with a numeric factor window (1–4) instead of the enum.
const runwareClarityUpscaleSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "required":["taskType","taskUUID","model","inputs"],
  "properties":{
    "model":{"type":"string"},
    "taskType":{"type":"string"},
    "taskUUID":{"type":"string"},
    "includeCost":{"type":"boolean"},
    "upscaleFactor":{"type":"number","minimum":1,"maximum":4},
    "outputType":{"type":"string","enum":["URL","base64Data","dataURI"]},
    "outputFormat":{"type":"string","enum":["JPG","PNG","WEBP"]},
    "inputs":{"type":"object","properties":{
      "image":{"type":"string"}}}
  }}}}}}`

func TestResolveRunwareImageUpscaleTask(t *testing.T) {
	schema := parseRunwareFixture(t, runwareUpscaleSchemaFixture)
	task, notices, err := resolveRunwareImageUpscaleTask(schema, ImageUpscaleRequest{
		Model: defaultRunwareImageUpscaleModel, Image: "data:image/png;base64,QUJD", Scale: 2,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if task["taskType"] != "upscale" {
		t.Fatalf("taskType = %+v, want upscale", task["taskType"])
	}
	inputs, _ := task["inputs"].(map[string]any)
	if inputs == nil || inputs["image"] != "data:image/png;base64,QUJD" {
		t.Fatalf("the source must ride inputs.image, got %+v", inputs)
	}
	// The [2,4] enum declares integer: the factor goes out as an int.
	if got, ok := task["upscaleFactor"].(int); !ok || got != 2 {
		t.Fatalf("upscaleFactor = %+v, want integer 2", task["upscaleFactor"])
	}
	if task["outputType"] != "dataURI" || task["outputFormat"] != "PNG" {
		t.Fatalf("inline PNG delivery expected, got %+v / %+v", task["outputType"], task["outputFormat"])
	}
	if len(notices) != 0 {
		t.Fatalf("a served 2x upscale should be notice-free, got %v", notices)
	}

	// An off-menu factor snaps to the nearest member, ties toward the
	// smaller — the conservative upscale — under a notice.
	task, notices, err = resolveRunwareImageUpscaleTask(schema, ImageUpscaleRequest{
		Model: defaultRunwareImageUpscaleModel, Image: "data:image/png;base64,QUJD", Scale: 3,
	})
	if err != nil {
		t.Fatalf("resolve snap: %v", err)
	}
	if got := task["upscaleFactor"]; got != 2 {
		t.Fatalf("upscaleFactor = %+v, want the snapped 2 (tie toward smaller)", got)
	}
	if len(notices) == 0 {
		t.Fatal("the snapped factor must say so")
	}
	task, notices, err = resolveRunwareImageUpscaleTask(schema, ImageUpscaleRequest{
		Model: defaultRunwareImageUpscaleModel, Image: "data:image/png;base64,QUJD", Scale: 5,
	})
	if err != nil {
		t.Fatalf("resolve snap high: %v", err)
	}
	if got := task["upscaleFactor"]; got != 4 {
		t.Fatalf("upscaleFactor = %+v, want the snapped 4", got)
	}

	// A numeric window clamps instead of snapping (the video-upscale stance).
	clarity := parseRunwareFixture(t, runwareClarityUpscaleSchemaFixture)
	task, notices, err = resolveRunwareImageUpscaleTask(clarity, ImageUpscaleRequest{
		Model: "runware:500@1", Image: "data:image/png;base64,QUJD", Scale: 8,
	})
	if err != nil {
		t.Fatalf("resolve clamp: %v", err)
	}
	if got, ok := task["upscaleFactor"].(float64); !ok || got != 4 {
		t.Fatalf("upscaleFactor = %+v, want clamped 4", task["upscaleFactor"])
	}
	if len(notices) == 0 {
		t.Fatal("the clamped factor must say so")
	}

	// A direct caller with no factor sends none.
	task, _, err = resolveRunwareImageUpscaleTask(schema, ImageUpscaleRequest{
		Model: defaultRunwareImageUpscaleModel, Image: "data:image/png;base64,QUJD",
	})
	if err != nil {
		t.Fatalf("resolve no scale: %v", err)
	}
	if _, ok := task["upscaleFactor"]; ok {
		t.Fatal("no requested factor must send none")
	}

	// A model whose inputs declare no image refuses before any money moves.
	if _, _, err := resolveRunwareImageUpscaleTask(parseRunwareFixture(t, runwareSoundSchemaFixture), ImageUpscaleRequest{
		Model: "mirelo:1@1", Image: "data:image/png;base64,QUJD", Scale: 2,
	}); err == nil {
		t.Fatal("a model with no image input must refuse the upscale request")
	}

	// Without a schema the universal fields ride with a notice.
	task, notices, err = resolveRunwareImageUpscaleTask(nil, ImageUpscaleRequest{
		Model: defaultRunwareImageUpscaleModel, Image: "data:image/png;base64,QUJD", Scale: 2,
	})
	if err != nil {
		t.Fatalf("resolve nil schema: %v", err)
	}
	if task["inputs"].(map[string]any)["image"] == "" || task["outputType"] != "dataURI" || task["upscaleFactor"] == nil {
		t.Fatalf("nil-schema task must still carry the universal fields, got %+v", task)
	}
	if len(notices) != 1 {
		t.Fatalf("the missing schema must say so, got %v", notices)
	}
}

func TestResolveRunwareVideoTaskTextToVideo(t *testing.T) {
	schema := parseRunwareFixture(t, runwareKlingSchemaFixture)
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model:       "klingai:kling-video@4.0",
		Prompt:      "a lighthouse in a storm",
		Duration:    "5",
		AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if task["taskType"] != "videoInference" || task["positivePrompt"] != "a lighthouse in a storm" {
		t.Fatalf("task core wrong: %+v", task)
	}
	if got, ok := task["duration"].(int); !ok || got != 5 {
		t.Fatalf("duration = %+v, want integer 5 (the schema declares integer)", task["duration"])
	}
	if task["width"] != 1280 || task["height"] != 720 {
		t.Fatalf("size = %vx%v, want the nearest preset 1280x720 for 16:9", task["width"], task["height"])
	}
	if got, ok := task["outputFormat"].(string); !ok || got != "MP4" {
		t.Fatalf("outputFormat = %+v, want MP4", task["outputFormat"])
	}
	if got, ok := task["deliveryMethod"].(string); !ok || got != "sync" {
		t.Fatalf("deliveryMethod = %+v, want sync (the schema declares the dial and its async default would ack)", task["deliveryMethod"])
	}
	if _, ok := task["negativePrompt"]; ok {
		t.Fatal("the schema declares no negativePrompt; it must never be written")
	}
	if len(notices) != 0 {
		t.Fatalf("a plain t2v on a declared shape should be notice-free, got %v", notices)
	}

	// An out-of-range duration clamps to the declared bound under a notice —
	// the model default it used to fall to can sit farther from the request
	// than the bound does.
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p", Duration: "60",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got, ok := task["duration"].(int); !ok || got != 30 {
		t.Fatalf("duration = %+v, want the ceiling 30 (an integer schema takes a JSON number)", task["duration"])
	}
	if len(notices) != 1 || !strings.Contains(strings.Join(notices, " "), "at most 30") {
		t.Fatalf("the clamp must say so, got %v", notices)
	}

	// Image-to-video lands the frame on inputs.frameImages and inherits the
	// source's orientation: no width/height even with a config aspect set.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "animate",
		AspectRatio: "16:9", Images: []string{"data:image/png;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve i2v: %v", err)
	}
	inputs, _ := task["inputs"].(map[string]any)
	if inputs == nil || len(inputs["frameImages"].([]string)) != 1 {
		t.Fatalf("i2v must fill inputs.frameImages, got %+v", inputs)
	}
	if _, ok := task["width"]; ok {
		t.Fatal("i2v inherits the frame's orientation; no pixels may ride")
	}

	// The deliveryMethod dial is written only where the schema declares it —
	// the Veo fixture carries no such property, and additionalProperties:false
	// makes a speculative field a 400.
	task, _, _, err = resolveRunwareVideoTask(parseRunwareFixture(t, runwareVeoSchemaFixture), VideoGenerateRequest{
		Model: "google:3@2", Prompt: "animate", Images: []string{"data:image/png;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve without the dial: %v", err)
	}
	if _, ok := task["deliveryMethod"]; ok {
		t.Fatal("deliveryMethod must never ride when the schema does not declare it")
	}
}

func TestResolveRunwareVideoTaskExtend(t *testing.T) {
	schema := parseRunwareFixture(t, runwareVeoSchemaFixture)
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "google:3@2", Prompt: "the ship sails on",
		Duration: "6", ExtendSource: true, Videos: []string{"data:video/mp4;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	inputs, _ := task["inputs"].(map[string]any)
	if inputs == nil || inputs["video"] != "data:video/mp4;base64,QUJD" {
		t.Fatalf("extend must put the source clip on inputs.video, got %+v", inputs)
	}
	if _, ok := task["duration"]; ok {
		// Veo pins an extension's length itself (duration const 7 under a
		// source video, a conditional invisible to the parser) — the resolver
		// withholds the dial rather than 422ing.
		t.Fatal("an extend turn must never send duration")
	}
	if len(notices) == 0 || !strings.Contains(strings.Join(notices, " "), "length") {
		t.Fatalf("the withheld duration must be explained, got %v", notices)
	}

	// A prompt-less extend still speaks (Veo declares positivePrompt
	// required): a neutral continuation line, not a 422.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "google:3@2", ExtendSource: true, Videos: []string{"data:video/mp4;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve prompt-less extend: %v", err)
	}
	if task["positivePrompt"] == "" {
		t.Fatal("a prompt-less extend needs the neutral continuation prompt")
	}

	// A model with no inputs.video refuses the extend by name (the FLUX fill
	// fixture declares only seed/mask inputs).
	task, _, _, err = resolveRunwareVideoTask(parseRunwareFixture(t, runwareFluxSchemaFixture), VideoGenerateRequest{
		Model: "bfl:1@2", ExtendSource: true, Videos: []string{"data:video/mp4;base64,QUJD"},
	})
	if err == nil || task != nil {
		t.Fatalf("an extend against a model with no source-video input must refuse, got task %+v err %v", task, err)
	}
}

func TestResolveRunwareVideoTaskKeyframes(t *testing.T) {
	// A keyframe turn puts the pair on inputs.frameImages in order — the
	// endpoint anchors the first entry as the opening frame and the last as
	// the closing one — and inherits the frames' orientation like any other
	// image turn: no width/height even with an aspect asked, while the
	// duration still rides (a transition is not an extend).
	schema := parseRunwareFixture(t, runwareKlingSchemaFixture)
	task, _, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "the scene melts between the two",
		Duration: "6", AspectRatio: "16:9", Keyframes: true,
		Images: []string{"data:image/png;base64,QUJD", "data:image/png;base64,REVG"},
	})
	if err != nil {
		t.Fatalf("resolve keyframes: %v", err)
	}
	inputs, _ := task["inputs"].(map[string]any)
	if inputs == nil {
		t.Fatalf("keyframes must fill inputs, got %+v", task)
	}
	frames, ok := inputs["frameImages"].([]string)
	if !ok || len(frames) != 2 || frames[0] != "data:image/png;base64,QUJD" || frames[1] != "data:image/png;base64,REVG" {
		t.Fatalf("inputs.frameImages = %+v, want the ordered pair", inputs["frameImages"])
	}
	if got, ok := task["duration"].(int); !ok || got != 6 {
		t.Fatalf("duration = %+v, want integer 6 on a keyframe turn", task["duration"])
	}
	if _, ok := task["width"]; ok {
		t.Fatal("keyframes inherit the frames' orientation; no pixels may ride")
	}

	// A pinned model whose inputs declare no frameImages refuses the
	// transition up front — the two images would degrade onto a reference or
	// seed input and the clip would not start or end where the user's frames
	// are. The FLUX fill fixture declares only seed/mask inputs.
	task, _, _, err = resolveRunwareVideoTask(parseRunwareFixture(t, runwareFluxSchemaFixture), VideoGenerateRequest{
		Model: "bfl:1@2", Prompt: "transition", Keyframes: true,
		Images: []string{"data:image/png;base64,QUJD", "data:image/png;base64,REVG"},
	})
	if err == nil || task != nil {
		t.Fatalf("a keyframe turn against a model with no frameImages must refuse, got task %+v err %v", task, err)
	}
	if !strings.Contains(err.Error(), "keyframe") {
		t.Fatalf("the refusal must name the keyframe gap, got %v", err)
	}

	// The maxItems clamp still guards a ceiling: the Veo packaging declares
	// frameImages maxItems 2, so three refs would truncate with a notice
	// (the executor sends exactly two; this pins the resolver's own line).
	var clampNotices []string
	task, clampNotices, _, err = resolveRunwareVideoTask(parseRunwareFixture(t, runwareVeoSchemaFixture), VideoGenerateRequest{
		Model: "google:3@2", Prompt: "p", Keyframes: true,
		Images: []string{"data:image/png;base64,QUJD", "data:image/png;base64,REVG", "data:image/png;base64,R0hJ"},
	})
	if err != nil {
		t.Fatalf("resolve clamped keyframes: %v", err)
	}
	inputs, _ = task["inputs"].(map[string]any)
	if frames, _ := inputs["frameImages"].([]string); len(frames) != 2 {
		t.Fatalf("frameImages = %+v, want clamped to the declared maxItems 2", inputs["frameImages"])
	}
	if len(clampNotices) == 0 || !strings.Contains(strings.Join(clampNotices, " "), "frame images") {
		t.Fatalf("the clamp must say so, got %v", clampNotices)
	}
}

// TestResolveRunwareVideoTaskKeyframesNeedTwoFrames pins the keyframe gate's
// second arm: a model whose frameImages caps at one entry takes a first frame
// but no last, so a transition refuses before the submit instead of riding the
// maxItems clamp — which kept the opening frame, dropped the closing one, and
// let the paid generation run as plain image-to-video. 23 of the 80 frameImages
// packagings in the 2026-11 catalog are first-frame-only, and every one carries
// the image-to-video tag that lists it in the Settings picker beside Kling 4.0.
func TestResolveRunwareVideoTaskKeyframesNeedTwoFrames(t *testing.T) {
	schema := parseRunwareFixture(t, runwareKlingFlashSchemaFixture)
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0-flash", Prompt: "morph between the two", Keyframes: true,
		Images: []string{"data:image/png;base64,QUJD", "data:image/png;base64,REVG"},
	})
	if err == nil || task != nil {
		t.Fatalf("a transition on a one-frame model must refuse, got task %+v notices %v", task, notices)
	}
	for _, want := range []string{"keyframe", "one frame image", "Settings"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q, got %v", want, err)
		}
	}

	// The same model still animates one image: its ceiling is one, and one is
	// exactly what a plain image-to-video turn sends.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0-flash", Prompt: "animate",
		Images: []string{"data:image/png;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("single-frame i2v on a one-frame model: %v", err)
	}
	inputs, _ := task["inputs"].(map[string]any)
	if frames, _ := inputs["frameImages"].([]string); len(frames) != 1 {
		t.Fatalf("inputs.frameImages = %+v, want the single frame", inputs["frameImages"])
	}
}

// TestResolveRunwareVideoTaskReferenceImages pins the reference route: a turn
// the planner marked useVideoAs:"reference" rides inputs.referenceImages and
// never the opening frame. The resolver read the role nowhere, so one
// reference image landed on frameImages on every model declaring it and a
// character sheet became frame one.
func TestResolveRunwareVideoTaskReferenceImages(t *testing.T) {
	schema := parseRunwareFixture(t, runwareKlingSchemaFixture)
	const (
		first  = "data:image/png;base64,QUJD"
		second = "data:image/png;base64,REVG"
	)

	// One reference image rides referenceImages — the role wins over the
	// one-image-is-the-opening-frame default.
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "this character walking through Paris",
		VideoRole: "reference", Images: []string{first},
	})
	if err != nil {
		t.Fatalf("resolve reference turn: %v", err)
	}
	inputs, _ := task["inputs"].(map[string]any)
	if refs, _ := inputs["referenceImages"].([]string); len(refs) != 1 || refs[0] != first {
		t.Fatalf("inputs.referenceImages = %+v, want the one reference", inputs["referenceImages"])
	}
	if _, ok := inputs["frameImages"]; ok {
		t.Fatalf("a reference turn must not name a frame, got %+v", inputs)
	}
	if len(notices) != 0 {
		t.Fatalf("a reference turn on a declared shape should be notice-free, got %v", notices)
	}

	// Without the role the same image is the opening frame — the default the
	// tool describes (omit imageRole to animate a single attached image).
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "animate", Images: []string{first},
	})
	if err != nil {
		t.Fatalf("resolve plain i2v: %v", err)
	}
	inputs, _ = task["inputs"].(map[string]any)
	if frames, _ := inputs["frameImages"].([]string); len(frames) != 1 || frames[0] != first {
		t.Fatalf("inputs.frameImages = %+v, want the single opening frame", inputs["frameImages"])
	}
	if _, ok := inputs["referenceImages"]; ok {
		t.Fatalf("a plain i2v turn must not name references, got %+v", inputs)
	}

	// A keyframe turn keeps its frames whatever role the call also carries:
	// imageRole:"keyframes" is the specific statement, and the pair rides
	// frameImages in order.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "morph", Keyframes: true, VideoRole: "reference",
		Images: []string{first, second},
	})
	if err != nil {
		t.Fatalf("resolve keyframes beside a reference role: %v", err)
	}
	inputs, _ = task["inputs"].(map[string]any)
	if frames, _ := inputs["frameImages"].([]string); len(frames) != 2 || frames[0] != first || frames[1] != second {
		t.Fatalf("inputs.frameImages = %+v, want the ordered pair", inputs["frameImages"])
	}
	if _, ok := inputs["referenceImages"]; ok {
		t.Fatalf("a keyframe turn must not name references, got %+v", inputs)
	}

	// A frame-only model cannot take references: Seedance 1.0 Pro declares
	// frameImages and nothing else, so the turn refuses naming the gap rather
	// than quietly opening on an image the role said was guidance. One image or
	// two, the refusal is the same.
	frameOnly := parseRunwareFixture(t, runwareSeedance1ProSchemaFixture)
	for _, tc := range []struct {
		name   string
		images []string
		want   string
	}{
		{name: "one image", images: []string{first}, want: "1 attached image(s)"},
		{name: "two images", images: []string{first, second}, want: "2 attached image(s)"},
	} {
		task, _, _, err = resolveRunwareVideoTask(frameOnly, VideoGenerateRequest{
			Model: "bytedance:2@1", Prompt: "p", VideoRole: "reference", Images: tc.images,
		})
		if err == nil || task != nil {
			t.Fatalf("%s: a reference turn on a frame-only model must refuse, got task %+v", tc.name, task)
		}
		for _, want := range []string{"reference", tc.want, "Settings"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: the refusal must name %q, got %v", tc.name, want, err)
			}
		}
	}
}

// TestResolveRunwareVideoTaskReferenceAspect pins how a reference turn is
// shaped. A reference image sets no orientation, so the turn takes the
// text-to-video shape rule with its own aspect — the call's explicit ratio,
// else the configured default, never the gateway's image-derived ratio — and
// rides it as width/height presets (Runware documents resolution as unusable
// beside them). With nothing to shape from, or on a model that takes no
// pixels, the tier-alone image-turn rule stands, and the clip takes the
// reference image's own shape.
func TestResolveRunwareVideoTaskReferenceAspect(t *testing.T) {
	schema := parseRunwareFixture(t, runwareKlingSchemaFixture)
	const reference = "data:image/png;base64,QUJD"

	for _, tc := range []struct {
		name         string
		req          VideoGenerateRequest
		images       int
		wantW, wantH int
	}{
		{
			// The gateway derived 16:9 from a landscape reference sheet; the
			// configured 9:16 is the standing preference and wins.
			name:   "the configured default replaces the image-derived ratio",
			req:    VideoGenerateRequest{AspectRatio: "16:9", ConfigAspectRatio: "9:16", Resolution: "1080p", VideoRole: "reference"},
			images: 1, wantW: 1080, wantH: 1920,
		},
		{
			name:   "an explicit ratio wins over the configured default",
			req:    VideoGenerateRequest{AspectRatio: "16:9", AspectRatioExplicit: true, ConfigAspectRatio: "9:16", Resolution: "1080p", VideoRole: "reference"},
			images: 1, wantW: 1920, wantH: 1080,
		},
		{
			name:   "the configured tier steers the preset class",
			req:    VideoGenerateRequest{AspectRatio: "16:9", AspectRatioExplicit: true, ConfigAspectRatio: "9:16", Resolution: "720p", VideoRole: "reference"},
			images: 1, wantW: 1280, wantH: 720,
		},
		{
			// Two images and no role is the executor's reference diagnosis too.
			name:   "two images with no role are a reference turn",
			req:    VideoGenerateRequest{AspectRatio: "16:9", ConfigAspectRatio: "9:16", Resolution: "1080p"},
			images: 2, wantW: 1080, wantH: 1920,
		},
	} {
		req := tc.req
		req.Model, req.Prompt = "klingai:kling-video@4.0", "this character walking through Paris"
		req.Images = make([]string, tc.images)
		for i := range req.Images {
			req.Images[i] = reference
		}
		task, notices, _, err := resolveRunwareVideoTask(schema, req)
		if err != nil {
			t.Fatalf("%s: resolve: %v", tc.name, err)
		}
		if task["width"] != tc.wantW || task["height"] != tc.wantH {
			t.Fatalf("%s: size = %vx%v, want %dx%d from the aspect", tc.name, task["width"], task["height"], tc.wantW, tc.wantH)
		}
		if _, ok := task["resolution"]; ok {
			t.Fatalf("%s: resolution must not ride beside width/height, got %+v", tc.name, task["resolution"])
		}
		if inputs, _ := task["inputs"].(map[string]any); len(inputs["referenceImages"].([]string)) != tc.images {
			t.Fatalf("%s: the images must still ride referenceImages, got %+v", tc.name, task["inputs"])
		}
		if len(notices) != 0 {
			t.Fatalf("%s: a shape the model offers should be notice-free, got %v", tc.name, notices)
		}
	}

	// The config at "auto" leaves nothing to shape from: the tier rides alone
	// as on any image turn, and Runware then matches the reference's shape.
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p", VideoRole: "reference",
		AspectRatio: "16:9", ConfigAspectRatio: "auto", Resolution: "1080p",
		Images: []string{reference},
	})
	if err != nil {
		t.Fatalf("resolve auto reference turn: %v", err)
	}
	if _, ok := task["width"]; ok {
		t.Fatalf("no aspect to shape from; pixels must not ride, got %vx%v", task["width"], task["height"])
	}
	if task["resolution"] != "1080p" {
		t.Fatalf("resolution = %+v, want the tier alone", task["resolution"])
	}
	if len(notices) != 0 {
		t.Fatalf("want no notices, got %v", notices)
	}

	// A model that takes no pixels keeps the tier rule as well: an aspect has
	// nowhere to ride, and dropping the tier too would leave the clip with no
	// size signal at all.
	pixelsless := parseRunwareFixture(t, `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
	  "type":"object","additionalProperties":false,
	  "properties":{
	    "positivePrompt":{"type":"string"},
	    "resolution":{"type":"string","enum":["720p","1080p"]},
	    "inputs":{"type":"object","properties":{
	      "referenceImages":{"type":"array","maxItems":4,"items":{"type":"string"}}}}
	}}}}}}`)
	task, _, _, err = resolveRunwareVideoTask(pixelsless, VideoGenerateRequest{
		Model: "klingai:test@pixelsless", Prompt: "p", VideoRole: "reference",
		ConfigAspectRatio: "9:16", Resolution: "1080p",
		Images: []string{reference},
	})
	if err != nil {
		t.Fatalf("resolve on a model without pixels: %v", err)
	}
	if _, ok := task["width"]; ok {
		t.Fatalf("a model without width/height must not receive pixels, got %vx%v", task["width"], task["height"])
	}
	if task["resolution"] != "1080p" {
		t.Fatalf("resolution = %+v, want the tier alone on a model without pixels", task["resolution"])
	}

	// A model whose schema forbids a size beside reference images keeps the
	// tier-alone rule: the configured default cannot apply there, and only
	// the planner's own ask is worth a notice.
	pixverse := parseRunwareFixture(t, runwarePixVerseV6SchemaFixture)
	task, notices, _, err = resolveRunwareVideoTask(pixverse, VideoGenerateRequest{
		Model: "pixverse:1@8", Prompt: "p", VideoRole: "reference",
		AspectRatio: "16:9", ConfigAspectRatio: "9:16", Resolution: "720p",
		Images: []string{reference},
	})
	if err != nil {
		t.Fatalf("resolve reference turn on a model forbidding sizes: %v", err)
	}
	if _, ok := task["width"]; ok {
		t.Fatalf("the schema forbids a size beside reference images here, got %vx%v", task["width"], task["height"])
	}
	if task["resolution"] != "720p" {
		t.Fatalf("resolution = %+v, want the tier alone", task["resolution"])
	}
	if len(notices) != 0 {
		t.Fatalf("a configured default that cannot apply is not worth a notice, got %v", notices)
	}
	task, notices, _, err = resolveRunwareVideoTask(pixverse, VideoGenerateRequest{
		Model: "pixverse:1@8", Prompt: "p", VideoRole: "reference",
		AspectRatio: "9:16", AspectRatioExplicit: true, ConfigAspectRatio: "16:9", Resolution: "720p",
		Images: []string{reference},
	})
	if err != nil {
		t.Fatalf("resolve explicit reference turn on a model forbidding sizes: %v", err)
	}
	if _, ok := task["width"]; ok {
		t.Fatalf("the schema forbids a size beside reference images here, got %vx%v", task["width"], task["height"])
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "takes the clip's shape from its reference images") || !strings.Contains(notices[0], `explicit "9:16" request was not sent`) {
		t.Fatalf("the planner's own ask must be answered with one notice naming it, got %v", notices)
	}
}

// TestResolveRunwareVideoTaskFrameAspect pins the frame-turn side of the
// shape rule. A frame turn inherits by default; with an explicit ratio it is
// shaped like a reference turn, but only where the model's own schema accepts
// a size beside the frames — the catalog splits on that, and the endpoint
// enforces it for some models and not others. Verified live 2026-11-24 with a
// square start frame asked for as 9:16: Kling 4.0 and Seedance 2.0 delivered
// 720x1280, MiniMax H3 Fast answered "Unsupported use of 'height' parameter"
// exactly as its schema says.
func TestResolveRunwareVideoTaskFrameAspect(t *testing.T) {
	const frame = "data:image/png;base64,QUJD"

	// Kling 4.0 accepts a size beside a frame: the explicit ratio rides as
	// one and the tier yields to it (the two are never written together).
	schema := parseRunwareFixture(t, runwareKlingSchemaFixture)
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "animate",
		AspectRatio: "9:16", AspectRatioExplicit: true, ConfigAspectRatio: "16:9", Resolution: "1080p",
		Images: []string{frame},
	})
	if err != nil {
		t.Fatalf("resolve explicit frame turn: %v", err)
	}
	if task["width"] != 1080 || task["height"] != 1920 {
		t.Fatalf("size = %vx%v, want the 1080x1920 preset for an explicit 9:16", task["width"], task["height"])
	}
	if _, ok := task["resolution"]; ok {
		t.Fatalf("resolution must not ride beside width/height, got %+v", task["resolution"])
	}
	if inputs, _ := task["inputs"].(map[string]any); len(inputs["frameImages"].([]string)) != 1 {
		t.Fatalf("the image must still be the start frame, got %+v", task["inputs"])
	}
	if len(notices) != 0 {
		t.Fatalf("a size the model offers should be notice-free, got %v", notices)
	}

	// A start→end pair is shaped the same way.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "morph", Keyframes: true,
		AspectRatio: "16:9", AspectRatioExplicit: true, Resolution: "720p",
		Images: []string{frame, "data:image/png;base64,REVG"},
	})
	if err != nil {
		t.Fatalf("resolve explicit keyframe turn: %v", err)
	}
	if task["width"] != 1280 || task["height"] != 720 {
		t.Fatalf("size = %vx%v, want 1280x720 for an explicit 16:9 at 720p", task["width"], task["height"])
	}

	// Without an explicit ratio nothing changes: the frame's shape is kept
	// and the tier rides.
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "animate",
		AspectRatio: "16:9", ConfigAspectRatio: "9:16", Resolution: "1080p",
		Images: []string{frame},
	})
	if err != nil {
		t.Fatalf("resolve plain frame turn: %v", err)
	}
	if _, ok := task["width"]; ok {
		t.Fatalf("a frame turn without an explicit ratio keeps its frame's shape, got %vx%v", task["width"], task["height"])
	}
	if task["resolution"] != "1080p" || len(notices) != 0 {
		t.Fatalf("resolution = %+v notices = %v, want the tier alone and no notices", task["resolution"], notices)
	}

	// MiniMax H3 forbids a size beside a frame: the ratio is not sent, the
	// tier still rides, and the reply says why.
	h3 := parseRunwareFixture(t, runwareH3FastSchemaFixture)
	task, notices, _, err = resolveRunwareVideoTask(h3, VideoGenerateRequest{
		Model: "minimax:h3@fast", Prompt: "animate",
		AspectRatio: "9:16", AspectRatioExplicit: true, Resolution: "768p",
		Images: []string{frame},
	})
	if err != nil {
		t.Fatalf("resolve explicit frame turn on a model forbidding sizes: %v", err)
	}
	if _, ok := task["width"]; ok {
		t.Fatalf("the schema forbids a size beside a frame here, got %vx%v", task["width"], task["height"])
	}
	if task["resolution"] != "768p" {
		t.Fatalf("resolution = %+v, want the tier alone", task["resolution"])
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "takes the clip's shape from the start frame") || !strings.Contains(notices[0], `explicit "9:16" request was not sent`) {
		t.Fatalf("the planner's own ask must be answered with one notice naming it, got %v", notices)
	}

	// A pair names both frames.
	_, notices, _, err = resolveRunwareVideoTask(h3, VideoGenerateRequest{
		Model: "minimax:h3@fast", Prompt: "morph", Keyframes: true,
		AspectRatio: "9:16", AspectRatioExplicit: true, Resolution: "768p",
		Images: []string{frame, "data:image/png;base64,REVG"},
	})
	if err != nil {
		t.Fatalf("resolve explicit keyframe turn on a model forbidding sizes: %v", err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "the start and end frames") {
		t.Fatalf("a pair must be named as both frames, got %v", notices)
	}

	// Vidu Q2 Pro whitelists five sizes beside a frame. Both of its common
	// asks already land on one of them — 1080x1920 at 1080p, and 720x1280
	// where the 480p class has no portrait shape to offer — so nothing is
	// substituted and nothing needs saying.
	vidu := parseRunwareFixture(t, runwareViduQ2ProSchemaFixture)
	for _, tc := range []struct {
		tier  string
		wantW int
		wantH int
	}{
		{"1080p", 1080, 1920},
		{"480p", 720, 1280},
	} {
		task, notices, _, err := resolveRunwareVideoTask(vidu, VideoGenerateRequest{
			Model: "vidu:3@1", Prompt: "animate",
			AspectRatio: "9:16", AspectRatioExplicit: true, Resolution: tc.tier,
			Images: []string{frame},
		})
		if err != nil {
			t.Fatalf("vidu %s: resolve: %v", tc.tier, err)
		}
		if task["width"] != tc.wantW || task["height"] != tc.wantH || len(notices) != 0 {
			t.Fatalf("vidu %s: size = %vx%v notices = %v, want %dx%d with no notice", tc.tier, task["width"], task["height"], notices, tc.wantW, tc.wantH)
		}
	}

	// Where the normal pick falls outside what a frame allows, the ask moves
	// to the nearest size that is allowed and says so.
	whitelist := parseRunwareFixture(t, runwareWhitelistSchema)
	task, notices, _, err = resolveRunwareVideoTask(whitelist, VideoGenerateRequest{
		Model: "vidu:3@1", Prompt: "animate",
		AspectRatio: "16:9", AspectRatioExplicit: true, Resolution: "528p",
		Images: []string{frame},
	})
	if err != nil {
		t.Fatalf("resolve on a whitelisting model: %v", err)
	}
	if task["width"] != 1280 || task["height"] != 720 {
		t.Fatalf("size = %vx%v, want the nearest allowed 1280x720", task["width"], task["height"])
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "does not offer 16:9 beside the start frame; generated at its nearest allowed size, 1280x720") {
		t.Fatalf("the substitution must say so, got %v", notices)
	}
}

func TestResolveRunwareVideoTaskResolutionTier(t *testing.T) {
	schema := parseRunwareFixture(t, runwareKlingSchemaFixture)

	// The configured tier steers the preset snap beside an aspect: 16:9 at
	// 1080p lands on the 1920x1080 preset — every Runware packaging publishes
	// the tier enum BESIDE width/height, so the old pixels-win drop silenced
	// the tier and the aspect tie then broke toward the smallest same-ratio
	// size (the preset was scored against the ratio's own numbers, 16x9).
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "a lighthouse in a storm",
		AspectRatio: "16:9", Resolution: "1080p",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if task["width"] != 1920 || task["height"] != 1080 {
		t.Fatalf("size = %vx%v, want the 1080p preset 1920x1080 for 16:9", task["width"], task["height"])
	}
	if _, ok := task["resolution"]; ok {
		t.Fatal("pixels carry the shape; the tier enum must not ride beside them")
	}
	if len(notices) != 0 {
		t.Fatalf("an offered tier beside an aspect should be notice-free, got %v", notices)
	}

	// The lower tier picks the smaller preset on the same shape, and a
	// portrait aspect renders its own pair.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p",
		AspectRatio: "16:9", Resolution: "720p",
	})
	if err != nil {
		t.Fatalf("resolve 720p: %v", err)
	}
	if task["width"] != 1280 || task["height"] != 720 {
		t.Fatalf("size = %vx%v, want 1280x720 at the 720p tier", task["width"], task["height"])
	}
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p",
		AspectRatio: "9:16", Resolution: "1080p",
	})
	if err != nil {
		t.Fatalf("resolve portrait: %v", err)
	}
	if task["width"] != 1080 || task["height"] != 1920 {
		t.Fatalf("size = %vx%v, want the portrait 1080x1920 preset", task["width"], task["height"])
	}

	// A bare aspect without a tier keeps the 720p-class default — the
	// let-the-model-decide behavior before the setting existed is unchanged.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p", AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("resolve bare aspect: %v", err)
	}
	if task["width"] != 1280 || task["height"] != 720 {
		t.Fatalf("size = %vx%v, want the bare-aspect default 1280x720", task["width"], task["height"])
	}

	// An exotic planner tier outside the canonical Settings set still steers
	// — "1440p" reads as a 2560x1440 target, and Kling's closest available
	// 16:9 size is its 1080p preset.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p",
		AspectRatio: "16:9", Resolution: "1440p",
	})
	if err != nil {
		t.Fatalf("resolve exotic tier: %v", err)
	}
	if task["width"] != 1920 || task["height"] != 1080 {
		t.Fatalf("size = %vx%v, want the closest 16:9 preset to 1440p", task["width"], task["height"])
	}

	// A tier the enum lacks, beside an aspect: the snap lands on the closest
	// available size and the mismatch says so.
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p",
		AspectRatio: "16:9", Resolution: "4k",
	})
	if err != nil {
		t.Fatalf("resolve 4k on Kling: %v", err)
	}
	if task["width"] != 1920 || task["height"] != 1080 {
		t.Fatalf("size = %vx%v, want the model's largest 16:9 preset", task["width"], task["height"])
	}
	if len(notices) != 1 || !strings.Contains(strings.Join(notices, " "), "4k") {
		t.Fatalf("the unoffered tier must say so, got %v", notices)
	}

	// A tier without an aspect rides the enum alone — the shape is the
	// model's own default. A tier the enum lacks substitutes to the nearest
	// offered class under a notice (an honest one-step downgrade, never a
	// silent fall to a lower default), and a genuinely unreadable tier says
	// so and sends nothing.
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p", Resolution: "1080p",
	})
	if err != nil {
		t.Fatalf("resolve tier alone: %v", err)
	}
	if task["resolution"] != "1080p" {
		t.Fatalf("resolution = %+v, want the enum tier", task["resolution"])
	}
	if _, ok := task["width"]; ok {
		t.Fatal("a tier without an aspect must not invent pixels")
	}
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p", Resolution: "8k",
	})
	if err != nil {
		t.Fatalf("resolve 8k tier: %v", err)
	}
	if task["resolution"] != "1080p" {
		t.Fatalf("resolution = %+v, want the nearest offered class 1080p", task["resolution"])
	}
	if len(notices) != 1 || !strings.Contains(strings.Join(notices, " "), "its closest, 1080p") {
		t.Fatalf("the substitution must say so, got %v", notices)
	}
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p", Resolution: "cinematic",
	})
	if err != nil {
		t.Fatalf("resolve unreadable tier: %v", err)
	}
	if _, ok := task["resolution"]; ok {
		t.Fatal("an unreadable tier must never ride")
	}
	if len(notices) != 1 {
		t.Fatalf("the unreadable tier must say so, got %v", notices)
	}

	// Seedance publishes the full 480p–4k ladder beside its presets: 4k at
	// 16:9 lands on the 3840x2160 preset, 1080p portrait on 1080x1920.
	seedance := parseRunwareFixture(t, runwareSeedanceSchemaFixture)
	task, _, _, err = resolveRunwareVideoTask(seedance, VideoGenerateRequest{
		Model: "bytedance:seedance@2.0", Prompt: "p",
		AspectRatio: "16:9", Resolution: "4k",
	})
	if err != nil {
		t.Fatalf("resolve seedance 4k: %v", err)
	}
	if task["width"] != 3840 || task["height"] != 2160 {
		t.Fatalf("size = %vx%v, want the 4k preset 3840x2160", task["width"], task["height"])
	}
	task, _, _, err = resolveRunwareVideoTask(seedance, VideoGenerateRequest{
		Model: "bytedance:seedance@2.0", Prompt: "p",
		AspectRatio: "9:16", Resolution: "1080p",
	})
	if err != nil {
		t.Fatalf("resolve seedance portrait: %v", err)
	}
	if task["width"] != 1080 || task["height"] != 1920 {
		t.Fatalf("size = %vx%v, want the portrait 1080p preset", task["width"], task["height"])
	}

	// A shape the snapped tier class doesn't carry falls to the class-less
	// snap: Kling's 1080p class is 16:9-only, so an ultrawide ask renders at
	// the model's own ultrawide size — the near-tied 720p-class ultrawide
	// (1460x624 measures 0.2% closer to 21:9 than the 1080p-class 2200x940)
	// must not overrule the tier the area tie-break exists to serve.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p",
		AspectRatio: "21:9", Resolution: "1080p",
	})
	if err != nil {
		t.Fatalf("resolve ultrawide: %v", err)
	}
	if task["width"] != 2200 || task["height"] != 940 {
		t.Fatalf("size = %vx%v, want the 1080p-class ultrawide preset", task["width"], task["height"])
	}
}

func TestResolveRunwareVideoTaskTierBelowModelCeiling(t *testing.T) {
	// minimax:h3@fast publishes tiers ["480p","768p"] only — no H3 variant
	// offers 1080p — and its preset family carries a ratio-EXACT 480p shape
	// (864x480 = 1.8) beside the slightly-off 768p one (1344x768 = 1.75).
	// conv_9639d9f6dbc3c6be9c09bc05: a 1080p-configured image turn answered
	// the model's 480p DEFAULT (832x480) because a tier mismatch meant
	// "send nothing", and a ratio-first snap would have landed the same 480p
	// class in pixel form. The enum snap substitutes one honest step instead.
	schema := parseRunwareFixture(t, runwareH3FastSchemaFixture)

	// Image-to-video: the closest offered tier rides resolution.
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "minimax:h3@fast", Prompt: "animate",
		AspectRatio: "16:9", Resolution: "1080p",
		Images: []string{"data:image/png;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve i2v: %v", err)
	}
	if task["resolution"] != "768p" {
		t.Fatalf("resolution = %+v, want the nearest offered tier 768p", task["resolution"])
	}
	if _, ok := task["width"]; ok {
		t.Fatal("i2v inherits the frame's orientation; no pixels may ride")
	}
	if len(notices) != 1 || !strings.Contains(strings.Join(notices, " "), "its closest, 768p") {
		t.Fatalf("the substitution must say so, got %v", notices)
	}

	// Text-to-video with an aspect: the pixels land in the snapped tier's
	// class — the ratio-exact 480p preset must not win on ratio alone.
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "minimax:h3@fast", Prompt: "p",
		AspectRatio: "16:9", Resolution: "1080p",
	})
	if err != nil {
		t.Fatalf("resolve t2v: %v", err)
	}
	if task["width"] != 1344 || task["height"] != 768 {
		t.Fatalf("size = %vx%v, want the 768p class 1344x768", task["width"], task["height"])
	}
	if len(notices) != 1 || !strings.Contains(strings.Join(notices, " "), "1080p") {
		t.Fatalf("the unoffered tier must say so, got %v", notices)
	}

	// Text-to-video without an aspect: the enum substitution rides alone.
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "minimax:h3@fast", Prompt: "p", Resolution: "1080p",
	})
	if err != nil {
		t.Fatalf("resolve tier alone: %v", err)
	}
	if task["resolution"] != "768p" {
		t.Fatalf("resolution = %+v, want the nearest offered tier 768p", task["resolution"])
	}
	if _, ok := task["width"]; ok {
		t.Fatal("a tier without an aspect must not invent pixels")
	}

	// An offered tier rides exactly: 480p picks the 480p class even though
	// the ratio-exact shape at 768p would score better against a 16:9 want.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "minimax:h3@fast", Prompt: "p",
		AspectRatio: "16:9", Resolution: "480p",
	})
	if err != nil {
		t.Fatalf("resolve 480p: %v", err)
	}
	if task["width"] != 864 || task["height"] != 480 {
		t.Fatalf("size = %vx%v, want the 480p class 864x480", task["width"], task["height"])
	}

	// A bare aspect (no tier) keeps the class-less snap: ratios within the
	// tie window count as one shape, and the area tie-break serves the
	// 720p-class default — H3's widescreen 768p preset, not its 480p one.
	task, _, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "minimax:h3@fast", Prompt: "p", AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("resolve bare aspect: %v", err)
	}
	if task["width"] != 1344 || task["height"] != 768 {
		t.Fatalf("size = %vx%v, want the 720p-class default 1344x768", task["width"], task["height"])
	}
}

func TestResolveRunwareVideoTaskTierImageToVideo(t *testing.T) {
	schema := parseRunwareFixture(t, runwareKlingSchemaFixture)

	// An image turn keeps inheriting the frame's orientation — no pixels —
	// but the tier now rides resolution: the packagings publish it beside
	// input media with the documented contract "automatically matches the
	// aspect ratio from the input", so the configured 1080p scales the clip
	// instead of silently landing on the model's default (Seedance 2.x
	// defaults 720p).
	task, notices, _, err := resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "animate",
		AspectRatio: "16:9", Resolution: "1080p",
		Images: []string{"data:image/png;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve i2v: %v", err)
	}
	if task["resolution"] != "1080p" {
		t.Fatalf("resolution = %+v, want the tier on an image turn", task["resolution"])
	}
	if _, ok := task["width"]; ok {
		t.Fatal("i2v inherits the frame's orientation; no pixels may ride")
	}
	inputs, _ := task["inputs"].(map[string]any)
	if inputs == nil || len(inputs["frameImages"].([]string)) != 1 {
		t.Fatalf("i2v must still fill inputs.frameImages, got %+v", inputs)
	}
	if len(notices) != 0 {
		t.Fatalf("an offered tier should be notice-free, got %v", notices)
	}

	// A tier the model lacks substitutes to the nearest offered class under
	// a notice — the honest one-step downgrade (conv_9639d9f6dbc3c6be9c09bc05
	// answered its 480p default here before the snap existed).
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "animate", Resolution: "4k",
		Images: []string{"data:image/png;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve i2v 4k: %v", err)
	}
	if task["resolution"] != "1080p" {
		t.Fatalf("resolution = %+v, want the nearest offered class 1080p", task["resolution"])
	}
	if len(notices) != 1 || !strings.Contains(strings.Join(notices, " "), "its closest, 1080p") {
		t.Fatalf("the substitution must say so, got %v", notices)
	}

	// A model without the control says so too.
	task, notices, _, err = resolveRunwareVideoTask(parseRunwareFixture(t, runwareVeoSchemaFixture), VideoGenerateRequest{
		Model: "google:3@2", Prompt: "animate", Resolution: "1080p",
		Images: []string{"data:image/png;base64,QUJD"},
	})
	if err != nil {
		t.Fatalf("resolve i2v without the control: %v", err)
	}
	if _, ok := task["resolution"]; ok {
		t.Fatal("no resolution control on the schema; the tier must never ride")
	}
	if len(notices) != 1 || !strings.Contains(strings.Join(notices, " "), "no resolution control") {
		t.Fatalf("the missing control must say so, got %v", notices)
	}
}

func TestResolveRunwareSpeechTask(t *testing.T) {
	schema := parseRunwareFixture(t, runwareSpeechSchemaFixture)
	task, notices, err := resolveRunwareSpeechTask(schema, AudioGenerateRequest{
		Model: "minimax:speech@2.8", Kind: audioKindSpeech,
		Prompt: "hello there", Voice: "japanese_coldqueen", Emotion: "happy", Speed: 1.5,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	speech, _ := task["speech"].(map[string]any)
	if speech == nil || speech["text"] != "hello there" {
		t.Fatalf("speech.text must carry the words, got %+v", speech)
	}
	// The enum match is case-insensitive and sends the schema's own spelling.
	if speech["voice"] != "Japanese_ColdQueen" {
		t.Fatalf("voice = %+v, want the schema's spelling", speech["voice"])
	}
	// Runware's speech models publish no emotion/speed dials: dropped with
	// notices, not sent.
	if _, ok := speech["emotion"]; ok {
		t.Fatal("emotion must drop on a model without the control")
	}
	if _, ok := speech["speed"]; ok {
		t.Fatal("speed must drop on a model without the control")
	}
	if len(notices) != 2 {
		t.Fatalf("both dropped controls must say so, got %v", notices)
	}
	if task["outputType"] != "dataURI" {
		t.Fatalf("outputType = %+v, want inline delivery on a declared enum", task["outputType"])
	}

	// An unknown voice name drops with a notice; the model default speaks.
	task, notices, err = resolveRunwareSpeechTask(schema, AudioGenerateRequest{
		Model: "minimax:speech@2.8", Prompt: "hi", Voice: "MysteryVoice",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	speech = task["speech"].(map[string]any)
	if _, ok := speech["voice"]; ok {
		t.Fatal("an unknown voice must be dropped, not sent")
	}
	if len(notices) == 0 {
		t.Fatal("the dropped voice must say so")
	}

	// A model with no speech object refuses before any money moves.
	if _, _, err := resolveRunwareSpeechTask(parseRunwareFixture(t, runwareKlingSchemaFixture), AudioGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "hi",
	}); err == nil {
		t.Fatal("a non-speech model must refuse the speech request")
	}
}

func TestResolveRunwareSoundTask(t *testing.T) {
	schema := parseRunwareFixture(t, runwareSoundSchemaFixture)
	task, notices, err := resolveRunwareSoundTask(schema, AudioGenerateRequest{
		Model: "mirelo:1@1", Kind: audioKindSound, Prompt: "rain on a tin roof", Duration: "30",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if task["positivePrompt"] != "rain on a tin roof" {
		t.Fatalf("prompt = %+v", task["positivePrompt"])
	}
	// The clamp is the notice-carrying clamp: send 10 (the ceiling), say so.
	if got, ok := task["duration"].(float64); !ok || got != 10 {
		t.Fatalf("duration = %+v, want clamped 10", task["duration"])
	}
	if len(notices) == 0 {
		t.Fatal("the clamped duration must say so")
	}
	if task["outputType"] != "dataURI" {
		t.Fatalf("outputType = %+v, want inline delivery", task["outputType"])
	}

	// A music model's settings.instrumental is satisfied (the canonical
	// request carries no lyrics), and a duration that cannot ride drops.
	music := parseRunwareFixture(t, runwareMusicSchemaFixture)
	task, notices, err = resolveRunwareSoundTask(music, AudioGenerateRequest{
		Model: "minimax:music@2.6", Kind: audioKindSound, Prompt: "lo-fi piano", Duration: "30",
	})
	if err != nil {
		t.Fatalf("resolve music: %v", err)
	}
	settings, _ := task["settings"].(map[string]any)
	if settings == nil || settings["instrumental"] != true {
		t.Fatalf("music model must be told instrumental, got %+v", settings)
	}
	if _, ok := task["duration"]; ok {
		t.Fatal("a model with no duration control must never receive one")
	}
	if len(notices) == 0 {
		t.Fatal("the dropped duration must say so")
	}
}

func TestResolveRunwareSFXTask(t *testing.T) {
	schema := parseRunwareFixture(t, runwareVideoSFXSchemaFixture)
	task, notices, err := resolveRunwareSFXTask(schema, SFXGenerateRequest{
		Model: "mirelo:1@1", Video: "data:video/mp4;base64,QUJD",
		Prompt: "distant thunder", Duration: 8, PreserveSpeech: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	inputs, _ := task["inputs"].(map[string]any)
	if inputs == nil || inputs["video"] != "data:video/mp4;base64,QUJD" {
		t.Fatalf("the clip must ride inputs.video, got %+v", inputs)
	}
	// mirelo's duration dial is mutually exclusive with its video input —
	// never sent, whatever the gateway probed.
	if _, ok := task["duration"]; ok {
		t.Fatal("a video-soundtrack task must never carry a duration")
	}
	// preserveSpeech is declared on this fixture, so it rides.
	if task["preserveSpeech"] != false {
		t.Fatalf("preserveSpeech = %+v, want the explicit false", task["preserveSpeech"])
	}
	if task["outputFormat"] != "MP4" {
		t.Fatalf("outputFormat = %+v, want MP4 beside a video input", task["outputFormat"])
	}
	if len(notices) != 0 {
		t.Fatalf("a fully-served sfx request should be notice-free, got %v", notices)
	}

	// A prompt-less ask gets the generic steering line (the fal rule).
	task, _, err = resolveRunwareSFXTask(schema, SFXGenerateRequest{Model: "mirelo:1@1", Video: "data:video/mp4;base64,QUJD"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if task["positivePrompt"] == "" {
		t.Fatal("the endpoint requires a prompt; the generic line must fill in")
	}

	// A text-only model refuses the video request by name.
	if _, _, err := resolveRunwareSFXTask(parseRunwareFixture(t, runwareSoundSchemaFixture), SFXGenerateRequest{
		Model: "mirelo:1@1", Video: "data:video/mp4;base64,QUJD",
	}); err == nil {
		t.Fatal("a text-only sound model must refuse the video-soundtrack request")
	}
}

func TestResolveRunwareLipsyncTask(t *testing.T) {
	schema := parseRunwareFixture(t, runwareLipsyncSchemaFixture)
	task, _, err := resolveRunwareLipsyncTask(schema, LipsyncGenerateRequest{
		Model: "klingai:7@1", Audio: "data:audio/mpeg;base64,QUJD", Video: "data:video/mp4;base64,QUJD",
	})
	if err != nil {
		t.Fatalf("resolve video face: %v", err)
	}
	inputs := task["inputs"].(map[string]any)
	if inputs["audio"] == "" || inputs["video"] == "" {
		t.Fatalf("audio and video face must both ride inputs, got %+v", inputs)
	}
	// An image face on a video-face model refuses by name.
	if _, _, err := resolveRunwareLipsyncTask(schema, LipsyncGenerateRequest{
		Model: "klingai:7@1", Audio: "data:audio/mpeg;base64,QUJD", Image: "data:image/png;base64,QUJD",
	}); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("an image face against a video-face model must refuse naming the kind, got %v", err)
	}
}

func TestResolveRunwareInpaintTask(t *testing.T) {
	fill := parseRunwareFixture(t, runwareFillSchemaFixture)
	task, notices, err := resolveRunwareInpaintTask(fill, ImageInpaintRequest{
		Model: "bfl:1@2", Prompt: "a wall of bookshelves",
		SourceImage: "data:image/jpeg;base64,QUJD", MaskImage: "data:image/png;base64,QUJD",
		Width: 1024, Height: 768,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	inputs := task["inputs"].(map[string]any)
	if inputs["seedImage"] == "" || inputs["maskImage"] == "" {
		t.Fatalf("source and mask must both ride inputs, got %+v", inputs)
	}
	// The FLUX fill fixture declares no width/height: dimensions inherit the
	// source image, so nothing rides.
	if _, ok := task["width"]; ok {
		t.Fatal("a fill model inherits the source dimensions; no width may ride")
	}
	if task["outputType"] != "dataURI" || task["outputFormat"] != "PNG" {
		t.Fatalf("inline PNG delivery expected, got %+v / %+v", task["outputType"], task["outputFormat"])
	}
	if len(notices) != 0 {
		t.Fatalf("a served inpaint should be notice-free, got %v", notices)
	}

	// A schema whose inputs declare no maskImage refuses before any money
	// moves — the fal/Replicate contract.
	if _, _, err := resolveRunwareInpaintTask(parseRunwareFixture(t, runwareKlingSchemaFixture), ImageInpaintRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p",
		SourceImage: "data:image/jpeg;base64,QUJD", MaskImage: "data:image/png;base64,QUJD",
	}); err == nil {
		t.Fatal("a model with no mask input must refuse the inpaint request")
	}
}

func TestRunwareVideoAudioProviderRoutingAndGates(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearRunwareAPIKey() })
	if err := saveRunwareAPIKey("rw-live-key"); err != nil {
		t.Fatalf("save key: %v", err)
	}

	// mergeAppConfig seeds every runware family slot on provider selection —
	// the provider-selection way — so picking runware opens the gates
	// without a Settings visit.
	config := mergeAppConfig(AppConfig{Models: ConfigModels{
		VideoProvider: "runware", InpaintProvider: "runware",
		SpeechProvider: "runware", AudioProvider: "runware", LipsyncProvider: "runware",
	}})
	if !videoGenerationConfigured(config) || !videoUpscaleConfigured(config) || !videoRestyleConfigured(config) {
		t.Fatal("the runware video family should be gated on with the seeded slots and a key")
	}
	if videoReframeConfigured(config) {
		t.Fatal("reframe_video has no runware model; its gate must close")
	}
	if !lipsyncConfigured(config) {
		t.Fatal("lipsync should be gated on under a runware LipsyncProvider with a key")
	}
	if !speechGenerationConfigured(config) || !soundEffectsGenerationConfigured(config) || !soundEffectsVideoConfigured(config) {
		t.Fatal("the runware speech and sound pair should be gated on")
	}
	if audioExtendConfigured(config) {
		t.Fatal("extend_audio has no runware backend; its gate must close")
	}
	if resolveDefaultVideoModel(config) != defaultRunwareVideoModel ||
		resolveDefaultVideoExtendModel(config) != defaultRunwareVideoExtendModel ||
		resolveDefaultVideoUpscaleModel(config) != defaultRunwareVideoUpscaleModel ||
		resolveDefaultVideoRestyleModel(config) != defaultRunwareVideoRestyleModel {
		t.Fatal("the video resolvers should route to the runware slots")
	}
	if resolveDefaultSpeechModel(config) != defaultRunwareSpeechModel ||
		resolveDefaultSoundEffectsModel(config) != defaultRunwareSoundEffectsModel ||
		resolveDefaultSoundEffectsVideoModel(config) != defaultRunwareSoundEffectsVideoModel {
		t.Fatal("the audio resolvers should route to the runware slots")
	}
	if got := resolveDefaultLipsyncVideoModel(config, resolveLipsyncProvider(config)); got != defaultRunwareLipsyncVideoModel {
		t.Fatalf("lipsync video resolver = %q, want the runware slot", got)
	}

	// Airplane mode drops every runware route.
	config.AirplaneMode = true
	for name, gate := range map[string]func(AppConfig) bool{
		"generate_video":    videoGenerationConfigured,
		"upscale_video":     videoUpscaleConfigured,
		"restyle_video":     videoRestyleConfigured,
		"lip_sync":          lipsyncConfigured,
		"generate_speech":   speechGenerationConfigured,
		"generate_sound":    soundEffectsGenerationConfigured,
		"add_sound_effects": soundEffectsVideoConfigured,
	} {
		if gate(config) {
			t.Fatalf("%s must drop under airplane mode", name)
		}
	}
	if err := airplaneMediaError(config, "generate_video"); err == nil {
		t.Fatal("airplane mode must refuse the runware video call")
	}

	// Without a key the cloud gates close.
	if err := clearRunwareAPIKey(); err != nil {
		t.Fatalf("clear key: %v", err)
	}
	config.AirplaneMode = false
	if videoGenerationConfigured(config) || speechGenerationConfigured(config) {
		t.Fatal("the runware gates need the key, not just the slots")
	}
}

func TestRunwareImageUpscaleRoutingAndGates(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearRunwareAPIKey() })

	// Pinning runware as the image provider seeds the upscale slot the
	// provider-selection way, so picking runware opens the gate without a
	// Settings visit.
	merged := mergeAppConfig(AppConfig{Models: ConfigModels{ImageProvider: "runware"}})
	if merged.Providers.Runware.UpscaleModel != defaultRunwareImageUpscaleModel {
		t.Fatalf("pinning runware should seed the image-upscale default, got %q", merged.Providers.Runware.UpscaleModel)
	}
	if err := saveRunwareAPIKey("rw-live-key"); err != nil {
		t.Fatalf("save key: %v", err)
	}
	if !imageUpscaleConfigured(merged) {
		t.Fatal("the gate should open with the seeded slot and a key")
	}
	if got := resolveDefaultImageUpscaleModel(merged); got != defaultRunwareImageUpscaleModel {
		t.Fatalf("resolver = %q, want the runware slot", got)
	}
	merged.Providers.Runware.UpscaleModel = "runware:500@1"
	if got := resolveDefaultImageUpscaleModel(merged); got != "runware:500@1" {
		t.Fatalf("resolver = %q, want the configured runware slot", got)
	}

	// Airplane mode drops the cloud route like every other one.
	merged.AirplaneMode = true
	if imageUpscaleConfigured(merged) {
		t.Fatal("upscale_image must drop under airplane mode")
	}
	merged.AirplaneMode = false

	// Without a key the gate closes even with the slot seeded.
	if err := clearRunwareAPIKey(); err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if imageUpscaleConfigured(merged) {
		t.Fatal("the runware upscale gate needs the key, not just the slot")
	}

	// The executor dispatches to the runware slot and the activity names the
	// routed backend.
	merged.Providers.Runware.UpscaleModel = defaultRunwareImageUpscaleModel
	var captured ImageUpscaleRequest
	tools := HarnessToolExecutionContext{
		Config:         merged,
		AttachedImages: []string{"data:image/png;base64,QUJD"},
		UpscaleImage: func(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, []string, error) {
			captured = req
			return ollamaGenerateResponse{Image: "data:image/png;base64,iVBORw0KGgo=", Done: true}, nil, nil
		},
	}
	def := imageUpscaleToolDefinition(merged)
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if captured.Model != defaultRunwareImageUpscaleModel {
		t.Fatalf("model = %q, want the runware slot", captured.Model)
	}
	if _, ok := out.(ToolImageResult); !ok {
		t.Fatalf("expected ToolImageResult, got %T", out)
	}
	activity := def.Activity(HarnessToolResult{Result: out})
	if len(activity.Command) != 3 || activity.Command[0] != "runware" || activity.Command[2] != defaultRunwareImageUpscaleModel {
		t.Fatalf("activity.Command = %v, want {runware upscale model}", activity.Command)
	}
}

func TestGenerateSpeechRunwareRouteStandsDownCloning(t *testing.T) {
	config := defaultAppConfig()
	config.Models.SpeechProvider = "runware"
	config.Providers.Runware.SpeechModel = "minimax:speech@2.8"

	var captured AudioGenerateRequest
	tools := HarnessToolExecutionContext{
		Config:         config,
		VoiceReference: "data:audio/wav;base64,QUJD",
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			captured = req
			return GeneratedAudio{Data: []byte("x"), MimeType: "audio/mpeg"}, nil
		},
	}
	def := speechGenerationToolDefinition(false, "runware")
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "say hi in my voice"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if captured.Model != "minimax:speech@2.8" {
		t.Fatalf("model = %q, want the runware slot", captured.Model)
	}
	if captured.SourceAudio != "" {
		t.Fatalf("SourceAudio must stand down on the runware route, got %q", captured.SourceAudio)
	}
	audio, ok := out.(ToolAudioResult)
	if !ok {
		t.Fatalf("expected ToolAudioResult, got %T", out)
	}
	found := false
	for _, notice := range audio.Notices {
		if strings.Contains(notice, "Runware speech route doesn't clone voices") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a runware cloning stand-down notice, got %v", audio.Notices)
	}

	// The activity stamps runware with the captured-provider pattern.
	activity := def.Activity(HarnessToolResult{Result: audio})
	if activity.Provider != "runware" {
		t.Fatalf("activity.Provider = %q, want runware", activity.Provider)
	}
	if len(activity.Command) != 3 || activity.Command[0] != "runware" || activity.Command[2] != "minimax:speech@2.8" {
		t.Fatalf("activity.Command = %v, want {runware generate model}", activity.Command)
	}
}

// TestRunwareWaitForTaskPollsThenDownloads drives GenerateVideo through the
// async contract: the submit answers "processing", the first getResponse
// says so too, the second reports success with a URL, and the URL's bytes
// come home inside the same call. The task UUID lands in the jobSink before
// the submit even posts, so a timeout inside the submit itself is pollable.
func TestRunwareWaitForTaskPollsThenDownloads(t *testing.T) {
	taskUUID := ""
	polls := 0
	var resultURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The result download (no task envelope on a GET): a minimal
			// MP4-shaped header the sniff reads (an ftyp box at offset 4).
			w.Write([]byte("\x00\x00\x00\x20ftypisom\x00\x00\x02\x00isomiso2avc1mp41" + strings.Repeat("x", 256)))
			return
		}
		var tasks []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&tasks)
		first := tasks[0]
		w.Header().Set("Content-Type", "application/json")
		switch first["taskType"] {
		case "getResponse":
			polls++
			status := "processing"
			if polls >= 2 {
				status = "success"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"taskType": "videoInference", "taskUUID": first["taskUUID"],
					"status": status, "videoUUID": "b7db282d-2943-4f12-992f-77df3ad3ec71",
					"videoURL": resultURL, "cost": 0.18,
				}},
			})
		case "videoInference":
			taskUUID, _ = first["taskUUID"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"taskType": "videoInference", "taskUUID": taskUUID,
					"status": "processing", "progress": 12,
				}},
			})
		default:
			return
		}
	}))
	defer server.Close()
	resultURL = server.URL + "/result.mp4"

	client := newRunwareClient(server.Client(), "rw-key")
	client.baseURL = server.URL
	job := &mediaJob{}
	client.jobSink = job

	generated, err := client.GenerateVideo(context.Background(), map[string]any{
		"taskType": "videoInference", "taskUUID": runwareTaskUUID(), "model": "google:3@2",
		"positivePrompt": "continue",
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if polls < 2 {
		t.Fatalf("expected two getResponse polls, got %d", polls)
	}
	if job.Provider != "runware" || job.ID == "" {
		t.Fatalf("jobSink must record the task UUID, got %+v", job)
	}
	if job.ID != taskUUID {
		t.Fatalf("recorded job id %q != submitted taskUUID %q", job.ID, taskUUID)
	}
	if len(generated.Data) == 0 || generated.CostMicros != 180000 {
		t.Fatalf("generated = %d bytes, cost %d; want bytes and the reported $0.18", len(generated.Data), generated.CostMicros)
	}

	// Recovery: one getResponse on the recorded job id delivers the finished
	// bytes without resubmitting. polls starts at 1 so the stub's first
	// recovery poll already reports success — the task finished remotely
	// during the timeout window, which is the case recovery exists for.
	polls = 1
	recovered, err := client.RecoverVideoJob(context.Background(), *job)
	if err != nil {
		t.Fatalf("RecoverVideoJob: %v", err)
	}
	if polls != 2 || len(recovered.Data) == 0 {
		t.Fatalf("recovery should poll once and deliver, got polls=%d bytes=%d", polls, len(recovered.Data))
	}
}

// TestRunwareUpscaleImagePollsThenPacks drives UpscaleImage through the async
// contract the upscale task shares with video: the submit answers
// "processing", a getResponse poll reports success with an inline data URI,
// and the image packs onto ollamaGenerateResponse with the reported cost. The
// task UUID lands in the jobSink before the submit posts, so RecoverImageJob
// delivers the same row with one poll after a caller-side timeout.
func TestRunwareUpscaleImagePollsThenPacks(t *testing.T) {
	taskUUID := ""
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tasks []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&tasks)
		first := tasks[0]
		w.Header().Set("Content-Type", "application/json")
		switch first["taskType"] {
		case "getResponse":
			polls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"taskType": "upscale", "taskUUID": first["taskUUID"], "status": "success",
					"imageUUID":    "b7db282d-2943-4f12-992f-77df3ad3ec71",
					"imageDataURI": "data:image/png;base64,QUJD", "cost": 0.0013,
				}},
			})
		case "upscale":
			taskUUID, _ = first["taskUUID"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"taskType": "upscale", "taskUUID": taskUUID,
					"status": "processing", "progress": 40,
				}},
			})
		default:
			return
		}
	}))
	defer server.Close()

	client := newRunwareClient(server.Client(), "rw-key")
	client.baseURL = server.URL
	job := &mediaJob{}
	client.jobSink = job

	resp, err := client.UpscaleImage(context.Background(), map[string]any{
		"taskType": "upscale", "taskUUID": runwareTaskUUID(), "model": defaultRunwareImageUpscaleModel,
	})
	if err != nil {
		t.Fatalf("UpscaleImage: %v", err)
	}
	if polls != 1 {
		t.Fatalf("expected one getResponse poll, got %d", polls)
	}
	if job.Provider != "runware" || job.ID == "" || job.ID != taskUUID {
		t.Fatalf("jobSink must record the submitted task UUID, got %+v (submitted %q)", job, taskUUID)
	}
	if len(resp.Images) != 1 || resp.Images[0] != "data:image/png;base64,QUJD" {
		t.Fatalf("images = %v, want the inline data URI", resp.Images)
	}
	if resp.CostMicros != 1300 {
		t.Fatalf("cost = %d, want the reported $0.0013", resp.CostMicros)
	}

	// Recovery polls the recorded job id once and delivers without
	// resubmitting — the task finished remotely during the timeout window.
	recovered, err := client.RecoverImageJob(context.Background(), *job)
	if err != nil {
		t.Fatalf("RecoverImageJob: %v", err)
	}
	if polls != 2 || len(recovered.Images) != 1 {
		t.Fatalf("recovery should poll once and deliver, got polls=%d images=%d", polls, len(recovered.Images))
	}
}

// TestRunwareAsyncAckRowIsNotAResult is the conv_2abf9ca68c1bfb009f303967
// regression: MiniMax H3 answers a video submit with a bare acknowledgment
// row — taskType and taskUUID only, no status, no media — and the old
// row-done check read that as a finished result whose empty fields became
// "runware returned no media" seconds after submit, while the generation kept
// running server-side. The submit's ack must drop into the getResponse poll
// and the clip must be delivered from it.
func TestRunwareAsyncAckRowIsNotAResult(t *testing.T) {
	polls := 0
	var resultURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte("\x00\x00\x00\x20ftypisom\x00\x00\x02\x00isomiso2avc1mp41" + strings.Repeat("x", 256)))
			return
		}
		var tasks []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&tasks)
		first := tasks[0]
		w.Header().Set("Content-Type", "application/json")
		if first["taskType"] == "getResponse" {
			polls++
			status := "processing"
			if polls >= 2 {
				status = "success"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"taskType": "videoInference", "taskUUID": first["taskUUID"],
					"status": status, "videoURL": resultURL, "cost": 0.18,
				}},
			})
			return
		}
		// The acknowledgment: no status field at all.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"taskType": "videoInference", "taskUUID": first["taskUUID"]}},
		})
	}))
	defer server.Close()
	resultURL = server.URL + "/result.mp4"

	client := newRunwareClient(server.Client(), "rw-key")
	client.baseURL = server.URL

	generated, err := client.GenerateVideo(context.Background(), map[string]any{
		"taskType": "videoInference", "taskUUID": runwareTaskUUID(), "model": "minimax:h3@0",
		"positivePrompt": "dolly in",
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if polls < 2 {
		t.Fatalf("the ack must be polled to delivery, got %d getResponse polls", polls)
	}
	if len(generated.Data) == 0 {
		t.Fatalf("no bytes delivered")
	}
}

// TestRunwareFinishedRowWithoutMediaNamesTheRow pins the other half: a row
// that IS finished but carries nothing must not surface as the bare "returned
// no media" — the error names the row's status (and cost) so the planner's
// adjusted-retry guidance has something to act on. The direct GenerateVideo
// path and the job-recovery path (pollTaskOnce) both get the named error.
func TestRunwareFinishedRowWithoutMediaNamesTheRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tasks []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&tasks)
		first := tasks[0]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"taskType": first["taskType"], "taskUUID": first["taskUUID"],
				"status": "success", "cost": 0.09,
			}},
		})
	}))
	defer server.Close()

	client := newRunwareClient(server.Client(), "rw-key")
	client.baseURL = server.URL

	_, err := client.GenerateVideo(context.Background(), map[string]any{
		"taskType": "videoInference", "taskUUID": runwareTaskUUID(), "model": "minimax:h3@0",
		"positivePrompt": "dolly in",
	})
	if err == nil || !strings.Contains(err.Error(), "finished without media") || !strings.Contains(err.Error(), `status "success"`) || !strings.Contains(err.Error(), "$0.0900") {
		t.Fatalf("GenerateVideo err = %v, want the named row", err)
	}
	if strings.Contains(err.Error(), "returned no media") {
		t.Fatalf("err = %v, must not be the bare no-media miss", err)
	}

	_, err = client.pollTaskOnce(context.Background(), mediaJob{Provider: "runware", ID: runwareTaskUUID()})
	if err == nil || !strings.Contains(err.Error(), "finished without media") {
		t.Fatalf("pollTaskOnce err = %v, want the named row", err)
	}
}

func TestRunwareMediaGenGapNotices(t *testing.T) {
	// A runware-pinned audio setup learns about the Runware key, not a fal
	// key that would light up nothing.
	note := mediaGenUnavailableNote("audio", false, "fal", "runware")
	if !strings.Contains(note, "Runware API key") || strings.Contains(note, "fal.ai API key") {
		t.Fatalf("runware-pinned audio gap should name the runware key, got %q", note)
	}
	// The runware-routed speech half gets its own suffix on the fal sound
	// gap.
	note = mediaGenUnavailableNote("audio", false, "runware", "fal")
	if !strings.Contains(note, "Speech generation is set to run on Runware") {
		t.Fatalf("fal audio gap under a runware speech route should name it, got %q", note)
	}
	// An answer already naming runware suppresses the fallback notice.
	if got := mediaGenFallbackNotice(true, "audio", "Add a Runware key in Settings.", false, "fal", "runware"); got != "" {
		t.Fatalf("a runware-remedied answer should suppress the notice, got %q", got)
	}
	notice := mediaGenFallbackNotice(true, "audio", "Sure, generating now.", false, "fal", "runware")
	if !strings.Contains(notice, "Runware API key") {
		t.Fatalf("runware fallback notice should name the key, got %q", notice)
	}
}

func TestRunwareConversationOverrides(t *testing.T) {
	if err := validateConversationModelOverrides(ConversationModelOverrides{
		VideoProvider: "runware", InpaintProvider: "runware", SpeechProvider: "runware",
		AudioProvider: "runware", LipsyncProvider: "runware", ImageProvider: "runware",
	}); err != nil {
		t.Fatalf("runware must be legal on every overridden dimension: %v", err)
	}

	base := defaultAppConfig()
	base.Models.VideoProvider = "fal"
	base.Models.InpaintProvider = "runware"
	overlaid, _, err := overlayModelOverrides(base, ChatRequest{}, ConversationModelOverrides{
		VideoProvider: "runware", VideoModel: "google:3@2", InpaintModel: "bfl:1@2",
	})
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if overlaid.Models.VideoProvider != "runware" {
		t.Fatalf("video provider = %q, want runware", overlaid.Models.VideoProvider)
	}
	if overlaid.Providers.Runware.VideoModel != "google:3@2" {
		t.Fatalf("runware VideoModel = %q, want the override", overlaid.Providers.Runware.VideoModel)
	}
	if overlaid.Providers.Runware.InpaintModel != "bfl:1@2" {
		t.Fatalf("runware InpaintModel = %q, want the override on the runware slot", overlaid.Providers.Runware.InpaintModel)
	}
	if overlaid.Providers.Fal.VideoModel != "" {
		t.Fatalf("the override must not leak onto the fal slot, got %q", overlaid.Providers.Fal.VideoModel)
	}

	// The upscale-model override follows the effective image provider — a
	// runware-pinned conversation upscales on the runware slot.
	imageBase := defaultAppConfig()
	imageBase.Models.ImageProvider = "fal"
	overlaid, _, err = overlayModelOverrides(imageBase, ChatRequest{}, ConversationModelOverrides{
		ImageProvider: "runware", UpscaleModel: "runware:500@1",
	})
	if err != nil {
		t.Fatalf("overlay upscale: %v", err)
	}
	if overlaid.Providers.Runware.UpscaleModel != "runware:500@1" {
		t.Fatalf("runware UpscaleModel = %q, want the override", overlaid.Providers.Runware.UpscaleModel)
	}
	if overlaid.Providers.Fal.UpscaleModel != "" {
		t.Fatalf("the upscale override must not leak onto the fal slot, got %q", overlaid.Providers.Fal.UpscaleModel)
	}
}

func TestVerifiedInpaintModelsRunware(t *testing.T) {
	options := verifiedInpaintModels("runware")
	if len(options) == 0 {
		t.Fatal("the runware inpaint catalog must not be empty")
	}
	foundDefault := false
	for _, opt := range options {
		if opt.ID == defaultRunwareInpaintModel {
			foundDefault = true
		}
	}
	if !foundDefault {
		t.Fatalf("the verified catalog must carry the default %q", defaultRunwareInpaintModel)
	}
}

// boolPtr is the *bool literal the SFX canonical request carries.
func boolPtr(v bool) *bool { return &v }

var _ = time.Second // keep the time import honest if fixtures change

// runwareTranscribeSchemaFixture trims the real google-gemini-3-5-flash
// schema: the messages contract plus inputs.audios (uuid/uri items — no data
// URIs), the transcription backend's shape.
const runwareTranscribeSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "required":["messages","taskType","taskUUID","model"],
  "properties":{
    "messages":{"type":"array","items":{"type":"object","properties":{"role":{"type":"string"},"content":{"type":"string"}}}},
    "systemPrompt":{"type":"string"},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "audios":{"type":"array","minItems":1,"items":{"type":"string"}},
      "images":{"type":"array","items":{"type":"string"}}}}
  }}}}}}`

func TestResolveRunwareTranscribeTask(t *testing.T) {
	schema := parseRunwareFixture(t, runwareTranscribeSchemaFixture)
	task, notices, err := resolveRunwareTranscribeTask(schema, TranscribeAudioRequest{
		Model: defaultRunwareTranscribeModel, Audio: "0f4a44e1-1a2b-4c3d-8e5f-9a8b7c6d5e4f", Task: "transcribe",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if task["taskType"] != "textInference" {
		t.Fatalf("taskType = %+v, want textInference", task["taskType"])
	}
	messages := task["messages"].([]map[string]any)
	if len(messages) != 1 || messages[0]["role"] != "user" {
		t.Fatalf("messages = %+v, want one user turn", task["messages"])
	}
	content, _ := messages[0]["content"].(string)
	if !strings.Contains(content, "Transcribe") {
		t.Fatalf("the instruction must ask for a transcription, got %q", content)
	}
	inputs := task["inputs"].(map[string]any)
	if inputs["audios"].([]string)[0] != "0f4a44e1-1a2b-4c3d-8e5f-9a8b7c6d5e4f" {
		t.Fatalf("audios = %+v, want the uploaded mediaUUID verbatim", inputs["audios"])
	}
	if len(notices) != 0 {
		t.Fatalf("a plain transcribe should be notice-free, got %v", notices)
	}

	// Translate folds into the instruction; the language hint rides beside it.
	task, _, err = resolveRunwareTranscribeTask(schema, TranscribeAudioRequest{
		Model: defaultRunwareTranscribeModel, Audio: "uuid", Task: "translate", Language: "ja",
	})
	if err != nil {
		t.Fatalf("resolve translate: %v", err)
	}
	content = task["messages"].([]map[string]any)[0]["content"].(string)
	if !strings.Contains(content, "translate it to English") || !strings.Contains(content, `"ja"`) {
		t.Fatalf("translate + language must fold into the instruction, got %q", content)
	}

	// A timestamps request degrades to plain under a notice — there is no
	// cue-capable output on the text models to fake.
	task, notices, err = resolveRunwareTranscribeTask(schema, TranscribeAudioRequest{
		Model: defaultRunwareTranscribeModel, Audio: "uuid", Timestamps: "segments",
	})
	if err != nil {
		t.Fatalf("resolve timestamps: %v", err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "no cue track") {
		t.Fatalf("the timestamps degrade must say so, got %v", notices)
	}

	// A model without the audio contract refuses by name.
	if _, _, err := resolveRunwareTranscribeTask(parseRunwareFixture(t, runwareSoundSchemaFixture), TranscribeAudioRequest{
		Model: "mirelo:1@1", Audio: "uuid",
	}); err == nil {
		t.Fatal("a model with no audios input must refuse the transcription request")
	}
}

func TestRunwareTranscriptionRoutingAndGates(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearRunwareAPIKey() })

	// Pinned runware routes there — auto never resolves it (fal has no key
	// and no whisper here, but auto must still fall back to fal, never
	// runware).
	config := defaultAppConfig()
	config.Models.TranscriptionProvider = "none"
	if got := resolveTranscriptionProvider(config); got != transcriptionProviderFal {
		t.Fatalf("auto with nothing configured = %q, want fal", got)
	}
	config.Models.TranscriptionProvider = transcriptionProviderRunware
	if got := resolveTranscriptionProvider(config); got != transcriptionProviderRunware {
		t.Fatalf("pinned = %q, want runware", got)
	}
	if transcribeAudioConfigured(config) {
		t.Fatal("the gate needs the key AND the slot")
	}
	if err := saveRunwareAPIKey("rw-key"); err != nil {
		t.Fatalf("save key: %v", err)
	}
	if transcribeAudioConfigured(config) {
		t.Fatal("the gate needs the slot even with a key")
	}
	merged := mergeAppConfig(config)
	if merged.Providers.Runware.TranscribeModel != defaultRunwareTranscribeModel {
		t.Fatalf("pinning runware should seed the verified default, got %q", merged.Providers.Runware.TranscribeModel)
	}
	if !transcribeAudioConfigured(merged) {
		t.Fatal("the gate should open with the seeded slot and a key")
	}
	if resolveDefaultRunwareTranscribeModel(merged) != defaultRunwareTranscribeModel {
		t.Fatal("the resolver should route to the runware slot")
	}
	merged.AirplaneMode = true
	if got := resolveTranscriptionProvider(merged); got != transcriptionProviderLocalWhisper {
		t.Fatalf("airplane mode must route transcription to local whisper, got %q", got)
	}

	// The executor dispatches to the runware slot and stamps the activity.
	merged.AirplaneMode = false
	var captured TranscribeAudioRequest
	tools := HarnessToolExecutionContext{
		Config:         merged,
		AttachedAudios: []string{"data:audio/mpeg;base64,QUJD"},
		TranscribeAudio: func(ctx context.Context, req TranscribeAudioRequest) (GeneratedTranscript, error) {
			captured = req
			return GeneratedTranscript{Text: "hello there"}, nil
		},
	}
	def := transcribeAudioToolDefinition(merged)
	out, _, err := def.Execute(context.Background(), tools, HarnessToolCall{Content: "transcribe"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if captured.Model != defaultRunwareTranscribeModel {
		t.Fatalf("model = %q, want the runware slot", captured.Model)
	}
	typed, ok := out.(ToolTranscribeResult)
	if !ok {
		t.Fatalf("expected ToolTranscribeResult, got %T", out)
	}
	if typed.Model != defaultRunwareTranscribeModel || typed.Transcript != "hello there" {
		t.Fatalf("result = %+v", typed)
	}
	activity := def.Activity(HarnessToolResult{Result: typed})
	if activity.Provider != "runware" {
		t.Fatalf("activity.Provider = %q, want runware", activity.Provider)
	}

	// A per-conversation transcription override lands on the runware slot.
	overlaid, _, err := overlayModelOverrides(merged, ChatRequest{}, ConversationModelOverrides{
		TranscriptionProvider: transcriptionProviderRunware, TranscribeModel: "google:gemini@3.1-pro",
	})
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if overlaid.Providers.Runware.TranscribeModel != "google:gemini@3.1-pro" {
		t.Fatalf("runware TranscribeModel = %q, want the override", overlaid.Providers.Runware.TranscribeModel)
	}
	if err := validateConversationModelOverrides(ConversationModelOverrides{TranscriptionProvider: "none"}); err == nil {
		t.Fatal("an unknown transcription provider must be rejected")
	}
}
