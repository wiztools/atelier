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
// under inputs, no negativePrompt.
const runwareKlingSchemaFixture = `{"components":{"schemas":{"RequestBody":{"type":"array","items":{
  "type":"object","additionalProperties":false,
  "properties":{
    "positivePrompt":{"type":"string"},
    "duration":{"type":"integer","minimum":3,"maximum":30,"default":10},
    "width":{"type":"integer"},"height":{"type":"integer"},
    "resolution":{"type":"string","enum":["720p","1080p"]},
    "outputFormat":{"type":"string","enum":["MP4","WEBM","MOV"]},
    "includeCost":{"type":"boolean"},
    "inputs":{"type":"object","properties":{
      "frameImages":{"type":"array","maxItems":10,"items":{"type":"string"}},
      "referenceImages":{"type":"array","maxItems":10,"items":{"type":"string"}},
      "video":{"type":"string"}}}
  },
  "allOf":[{"if":{"anyOf":[{"required":["width"]},{"required":["height"]}]},
   "then":{"oneOf":[
     {"properties":{"width":{"const":1280},"height":{"const":720}}},
     {"properties":{"width":{"const":720},"height":{"const":1280}}},
     {"properties":{"width":{"const":1920},"height":{"const":1080}}}
   ]}}]}}}}}`

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
	if _, ok := task["negativePrompt"]; ok {
		t.Fatal("the schema declares no negativePrompt; it must never be written")
	}
	if len(notices) != 0 {
		t.Fatalf("a plain t2v on a declared shape should be notice-free, got %v", notices)
	}

	// An out-of-range duration drops with a notice, never rides.
	task, notices, _, err = resolveRunwareVideoTask(schema, VideoGenerateRequest{
		Model: "klingai:kling-video@4.0", Prompt: "p", Duration: "60",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := task["duration"]; ok {
		t.Fatal("a 60s duration must be dropped against the 30s ceiling")
	}
	if len(notices) == 0 {
		t.Fatal("the dropped duration must say so")
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
