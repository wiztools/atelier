package main

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

// conv_842f67a84cf687ac2f828eff: "Generate 8s duration audio file of underwater
// scuba swiming sound." Triage kept the length but wrote it as prose, the
// format-schema planner could not carry a duration at all and put "duration 8s"
// in the prompt, the configured "auto" sent nothing, and the endpoint rendered
// 3.0s — which the reply then confirmed as an 8-second file.
const (
	conv842TriageTask   = `generate_sound: underwater scuba swimming sound, bubbles, gentle water movement, duration 8s`
	conv842SoundModel   = "fal-ai/elevenlabs/sound-effects/v2"
	conv842ContractTask = `generate_sound: underwater scuba swimming sound, bubbles, gentle water movement, duration:"8"`
)

// planSchemaOmittedCallFields are the HarnessToolCall fields the plan grammar
// deliberately does not free, each with the reason it is safe to leave out.
var planSchemaOmittedCallFields = map[string]string{
	"env": "run_command's free-form variable map — no recorded planner use, and an open-ended object is the shape a constrained grammar handles least predictably",
}

func planSchemaCallProperties(t *testing.T, registry HarnessToolRegistry) map[string]any {
	t.Helper()
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
	return properties
}

// TestHarnessPlanSchemaDeclaresCallFields pins the invariant every past
// grammar gap broke one key at a time (voice, preserveSpeech, now duration):
// a field the call struct can decode is one the grammar can emit. A
// format-schema planner is constrained to the declared properties, so a call
// field missing here is one no such planner can ever set — conv_842f67a84cf687ac2f828eff
// put a user's "8s" into the prompt prose because duration had no slot, and
// across the recorded history format-schema planners carried a duration on 0
// of 94 length-taking calls against 123 of 149 in native mode.
func TestHarnessPlanSchemaDeclaresCallFields(t *testing.T) {
	properties := planSchemaCallProperties(t, defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil))
	typ := reflect.TypeOf(HarnessToolCall{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if _, omitted := planSchemaOmittedCallFields[name]; omitted {
			if _, declared := properties[name]; declared {
				t.Errorf("plan grammar declares %s, listed as deliberately omitted — remove it from planSchemaOmittedCallFields", name)
			}
			continue
		}
		if _, ok := properties[name].(map[string]any); !ok {
			t.Errorf("plan grammar toolCalls properties missing %s (HarnessToolCall.%s; has %v)", name, field.Name, propertiesKeys(properties))
		}
	}
}

// TestFormatPlanCarriesDuration pins both transport halves of the fix: a plan
// that names a duration decodes onto the call through the JSON plan path, and
// the strict variant OpenRouter receives still declares it (as a nullable
// string, since strict mode promotes every property to required).
func TestFormatPlanCarriesDuration(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{soundEffectsGenerationToolDefinition("fal")})
	plan, errors := parseHarnessToolPlanWithRegistry(`{"brief":"sound","needsTools":true,"reason":"asked for","toolCalls":[{`+
		`"name":"generate_sound","content":"underwater scuba swimming sound","duration":"8"}]}`, registry)
	if len(errors) != 0 {
		t.Fatalf("plan errors = %v", errors)
	}
	if len(plan.ToolCalls) != 1 || plan.ToolCalls[0].Duration != "8" {
		t.Fatalf("plan toolCalls = %+v, want one generate_sound call carrying duration 8", plan.ToolCalls)
	}

	body := openRouterChatBody(ChatRequest{Model: "m", Format: harnessToolPlanSchema(registry)}, false)
	sent := body["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"].(map[string]any)
	items := sent["properties"].(map[string]any)["toolCalls"].(map[string]any)["items"].(map[string]any)
	properties := items["properties"].(map[string]any)
	encoded, err := json.Marshal(properties["duration"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"string"`) || !strings.Contains(string(encoded), `"null"`) {
		t.Errorf("strict variant duration = %s, want a nullable string", encoded)
	}
	required, _ := json.Marshal(items["required"])
	if !strings.Contains(string(required), `"duration"`) {
		t.Errorf("strict variant required = %s, want duration promoted to required", required)
	}
}

// runSound executes generate_sound against a fake backend that answers clip,
// returning the request the executor built, the typed result, and the summary.
func runSound(t *testing.T, call HarnessToolCall, exec HarnessToolExecutionContext, clip []byte) (AudioGenerateRequest, ToolAudioResult, string) {
	t.Helper()
	var captured AudioGenerateRequest
	exec.GenerateAudio = func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
		captured = req
		return GeneratedAudio{Data: clip, MimeType: "audio/wav"}, nil
	}
	result, summary, err := soundEffectsGenerationToolDefinition("fal").Execute(context.Background(), exec, call)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	typed, ok := result.(ToolAudioResult)
	if !ok {
		t.Fatalf("result = %T, want ToolAudioResult", result)
	}
	return captured, typed, summary
}

// TestGenerateSoundDurationRecoveredFromTriage is generate_sound's sibling of
// TestGenerateVideoDurationRecoveredFromTriage: triage's stated length sits
// between the call and the configured default.
func TestGenerateSoundDurationRecoveredFromTriage(t *testing.T) {
	dropped := HarnessToolCall{Name: "generate_sound", Content: "underwater scuba swimming sound, bubbles, duration 8s", Model: conv842SoundModel}
	threeSeconds := mediasnifftest.WAV(24000) // 8 kHz mono 8-bit: 24000 bytes = 3.0s

	t.Run("a dropped duration binds triage's stated length", func(t *testing.T) {
		config := defaultAppConfig()
		config.Generation.Audio.Duration = "auto"
		req, out, _ := runSound(t, dropped, HarnessToolExecutionContext{Config: config, TriageTask: conv842ContractTask}, threeSeconds)
		if req.Duration != "8" {
			t.Fatalf("captured.Duration = %q, want the triaged 8 (not the configured auto)", req.Duration)
		}
		if !hasDiagnostic(out.Diagnostics, "omitted duration") {
			t.Fatalf("diagnostics %v, want one naming the recovered duration", out.Diagnostics)
		}
	})

	t.Run("the planner's own duration wins", func(t *testing.T) {
		call := dropped
		call.Duration = "12"
		req, out, _ := runSound(t, call, HarnessToolExecutionContext{Config: defaultAppConfig(), TriageTask: conv842ContractTask}, threeSeconds)
		if req.Duration != "12" {
			t.Fatalf("captured.Duration = %q, want the planner's 12", req.Duration)
		}
		if hasDiagnostic(out.Diagnostics, "omitted duration") {
			t.Fatalf("diagnostics %v, want no recovery when the call carries a duration", out.Diagnostics)
		}
	})

	t.Run("the conv's prose note is not recovered", func(t *testing.T) {
		// triageDurationSeconds reads the contract spelling only — the rule
		// that keeps a user's beat timings from being misread — which is why
		// the audio bullet now teaches it. Prose leaves the length to the
		// model, and the result then states what the model chose.
		config := defaultAppConfig()
		config.Generation.Audio.Duration = "auto"
		req, out, summary := runSound(t, dropped, HarnessToolExecutionContext{Config: config, TriageTask: conv842TriageTask}, threeSeconds)
		if req.Duration != "" {
			t.Fatalf("captured.Duration = %q, want none for a prose note under a configured auto", req.Duration)
		}
		if hasDiagnostic(out.Diagnostics, "omitted duration") {
			t.Fatalf("diagnostics %v, want no recovery from prose", out.Diagnostics)
		}
		if out.DurationSeconds != 3 || summary != "generated 3.0s of audio with "+conv842SoundModel {
			t.Fatalf("delivered length = %v, summary = %q; want the measured 3.0s stated", out.DurationSeconds, summary)
		}
	})

	t.Run("no triage length keeps the configured default", func(t *testing.T) {
		config := defaultAppConfig()
		config.Generation.Audio.Duration = "30"
		req, out, _ := runSound(t, dropped, HarnessToolExecutionContext{Config: config, TriageTask: "generate_sound: rain on a tin roof"}, threeSeconds)
		if req.Duration != "30" {
			t.Fatalf("captured.Duration = %q, want the configured 30", req.Duration)
		}
		if len(out.Diagnostics) != 0 {
			t.Fatalf("diagnostics %v, want none", out.Diagnostics)
		}
	})
}

// TestExtendAudioDurationRecoveredFromTriage: the extension length gets the
// same floor — "add 30 seconds" must not fall to the default because the
// planner dropped it.
func TestExtendAudioDurationRecoveredFromTriage(t *testing.T) {
	const clip = "data:audio/mpeg;base64,QUJD"
	run := func(t *testing.T, call HarnessToolCall, exec HarnessToolExecutionContext) (AudioExtendRequest, ToolAudioResult) {
		t.Helper()
		var captured AudioExtendRequest
		exec.AttachedAudios = []string{clip}
		exec.GenerateAudioExtend = func(ctx context.Context, req AudioExtendRequest) (GeneratedAudio, error) {
			captured = req
			return GeneratedAudio{Data: mediasnifftest.WAV(24000), MimeType: "audio/wav"}, nil
		}
		result, _, err := extendAudioToolDefinition("fal").Execute(context.Background(), exec, call)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		typed, ok := result.(ToolAudioResult)
		if !ok {
			t.Fatalf("result = %T, want ToolAudioResult", result)
		}
		return captured, typed
	}
	task := `extend_audio: continue in the same style, duration:"30"`
	dropped := HarnessToolCall{Name: "extend_audio", Content: "continue in the same style", Model: "fal-ai/stable-audio-25/inpaint"}

	req, out := run(t, dropped, HarnessToolExecutionContext{Config: defaultAppConfig(), TriageTask: task})
	if req.Duration != "30" {
		t.Fatalf("captured.Duration = %q, want the triaged 30", req.Duration)
	}
	if !hasDiagnostic(out.Diagnostics, "omitted duration") {
		t.Fatalf("diagnostics %v, want one naming the recovered duration", out.Diagnostics)
	}

	own := dropped
	own.Duration = "45"
	req, out = run(t, own, HarnessToolExecutionContext{Config: defaultAppConfig(), TriageTask: task})
	if req.Duration != "45" || hasDiagnostic(out.Diagnostics, "omitted duration") {
		t.Fatalf("captured.Duration = %q diagnostics %v, want the planner's 45 and no recovery", req.Duration, out.Diagnostics)
	}

	config := defaultAppConfig()
	config.Generation.Audio.ExtendDuration = "20"
	req, _ = run(t, dropped, HarnessToolExecutionContext{Config: config, TriageTask: "extend_audio: continue in the same style, add 30 seconds"})
	if req.Duration != "20" {
		t.Fatalf("captured.Duration = %q, want the configured 20 for a prose note", req.Duration)
	}
}

// TestGenerateSoundExampleCarriesDuration pins the slot a format-schema
// planner actually reads: the catalog renders only each tool's Example and
// Description, never the per-tool param schema.
func TestGenerateSoundExampleCarriesDuration(t *testing.T) {
	def := soundEffectsGenerationToolDefinition("fal")
	var call HarnessToolCall
	if err := json.Unmarshal([]byte(def.Example), &call); err != nil {
		t.Fatalf("example %s does not decode: %v", def.Example, err)
	}
	if call.Name != "generate_sound" || call.Duration == "" {
		t.Fatalf("example = %+v, want a generate_sound call carrying a duration", call)
	}
	if errors := def.Validate("toolCalls[0]", call); len(errors) != 0 {
		t.Fatalf("example does not validate: %v", errors)
	}
	description := def.Description
	for _, want := range []string{`duration:"8"`, "prompt prose cannot set the audio's length"} {
		if !strings.Contains(description, want) {
			t.Errorf("description lacks %q: %q", want, description)
		}
	}
}

// TestTriageAudioBulletTeachesDurationSpelling: the executor's recovery reads
// the contract spelling only, so the audio bullet must teach it the way the
// video bullet does — conv_842f67a84cf687ac2f828eff's triage wrote
// "duration 8s", which no recovery could read.
func TestTriageAudioBulletTeachesDurationSpelling(t *testing.T) {
	prompt := triageSystemPrompt(HarnessToolRegistry{}, nil, "/tmp/workspace", false)
	var bullet string
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, `- "audio":`) {
			bullet = line
		}
	}
	if bullet == "" {
		t.Fatal("triage prompt has no audio bullet")
	}
	if !strings.Contains(bullet, `duration:"N"`) {
		t.Fatalf("audio bullet lacks the duration:\"N\" toolTask spelling: %q", bullet)
	}
	if triageDurationSeconds(conv842ContractTask) != "8" {
		t.Fatal("the taught spelling does not parse")
	}
}

// fakeFFprobeThreeSecondScript fakes ffprobe over a 3.000-second MP3 — the
// exact figure the pure-Go frame walk cannot produce on its own.
const fakeFFprobeThreeSecondScript = `#!/bin/sh
cat <<'JSON'
{"format":{"duration":"3.000","format_name":"mp3","bit_rate":"128000"},"streams":[{"codec_type":"audio","codec_name":"mp3","sample_rate":"44100","channels":2}]}
JSON
`

// TestGeneratedAudioStatesDeliveredLength pins that the result and summary
// describe the clip that shipped: measured from its own file (ffprobe when
// configured, the byte sniff otherwise), and unstated when unmeasurable.
func TestGeneratedAudioStatesDeliveredLength(t *testing.T) {
	call := HarnessToolCall{Name: "generate_sound", Content: "rain", Model: conv842SoundModel}

	t.Run("the sniff measures a measurable clip", func(t *testing.T) {
		req, out, summary := runSound(t, call, HarnessToolExecutionContext{Config: defaultAppConfig()}, mediasnifftest.WAV(24000))
		if req.Kind != audioKindSound || out.DurationSeconds != 3 || summary != "generated 3.0s of audio with "+conv842SoundModel {
			t.Fatalf("kind = %q, delivered length = %v, summary = %q; want sound, 3.0s stated", req.Kind, out.DurationSeconds, summary)
		}
	})

	t.Run("ffprobe answers exactly where it is configured", func(t *testing.T) {
		withRealLocalLookup(t) // TestMain pins lookup off; the override needs it
		config := defaultAppConfig()
		config.Providers.Local.FFprobe.Binary = writeFakeWhisper(t, filepath.Join(t.TempDir(), "bin"), "ffprobe", fakeFFprobeThreeSecondScript)
		// 117 frames at 1152 samples / 44.1 kHz: the frame walk reads 3.056s
		// where ffprobe's gapless answer is 3.000 — conv_842f67a84cf687ac2f828eff's
		// own clip.
		_, out, summary := runSound(t, call, HarnessToolExecutionContext{Config: config}, mediasnifftest.MP3(117))
		if out.DurationSeconds != 3 || summary != "generated 3.0s of audio with "+conv842SoundModel {
			t.Fatalf("delivered length = %v, summary = %q; want ffprobe's 3.0s", out.DurationSeconds, summary)
		}
	})

	t.Run("without ffprobe the sniff covers the length", func(t *testing.T) {
		_, out, summary := runSound(t, call, HarnessToolExecutionContext{Config: defaultAppConfig()}, mediasnifftest.MP3(117))
		want := 117 * 1152.0 / 44100
		if math.Abs(out.DurationSeconds-want) > 1e-9 || summary != "generated 3.1s of audio with "+conv842SoundModel {
			t.Fatalf("delivered length = %v, summary = %q; want the sniffed %v (3.1s)", out.DurationSeconds, summary, want)
		}
	})

	t.Run("an unmeasurable clip leaves the length unstated", func(t *testing.T) {
		_, out, summary := runSound(t, call, HarnessToolExecutionContext{Config: defaultAppConfig()}, []byte("x"))
		if out.DurationSeconds != 0 || summary != "generated audio with "+conv842SoundModel {
			t.Fatalf("delivered length = %v, summary = %q; want no length claimed", out.DurationSeconds, summary)
		}
	})
}

// TestGenerateSoundNoticesLengthDisagreement: a clip whose measured length
// disagrees with the length that was sent says so, so the reply cannot confirm
// a length the model did not deliver.
func TestGenerateSoundNoticesLengthDisagreement(t *testing.T) {
	const resolverDrop = `The selected model "fal-ai/x" has no duration control; ignoring the requested duration.`
	cases := []struct {
		name      string
		requested string
		delivered float64
		notices   []string
		want      string
	}{
		{"the conv: 3s delivered for an 8s request", "8", 3.0, nil,
			"Requested 8s of audio, but " + conv842SoundModel + " delivered 3.0s. Atelier did not adjust the clip's length."},
		{"an exact delivery is silent", "8", 8.0, nil, ""},
		{"a tenth is tolerated", "8", 7.6, nil, ""},
		{"past a tenth it is named", "8", 7.1, nil,
			"Requested 8s of audio, but " + conv842SoundModel + " delivered 7.1s. Atelier did not adjust the clip's length."},
		{"short clips get half a second of slack", "2", 2.4, nil, ""},
		{"short clips past the slack are named", "2", 2.6, nil,
			"Requested 2s of audio, but " + conv842SoundModel + " delivered 2.6s. Atelier did not adjust the clip's length."},
		{"a longer delivery is named too", "8", 10.0, nil,
			"Requested 8s of audio, but " + conv842SoundModel + " delivered 10.0s. Atelier did not adjust the clip's length."},
		{"a resolver notice about the duration owns the story", "8", 3.0, []string{resolverDrop}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ToolAudioResult{Model: conv842SoundModel, DurationSeconds: tc.delivered, Notices: tc.notices}
			if got := soundLengthNotice(result, tc.requested); got != tc.want {
				t.Fatalf("soundLengthNotice = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("no requested length leaves nothing to compare", func(t *testing.T) {
		if got := soundLengthNotice(ToolAudioResult{Model: conv842SoundModel, DurationSeconds: 3}, ""); got != "" {
			t.Fatalf("soundLengthNotice = %q, want none", got)
		}
	})
	t.Run("an unmeasurable delivery is not a dispute", func(t *testing.T) {
		if got := soundLengthNotice(ToolAudioResult{Model: conv842SoundModel}, "8"); got != "" {
			t.Fatalf("soundLengthNotice = %q, want none", got)
		}
	})

	// The wiring: the notice rides the executor's result as a user-facing
	// notice, so the gateway surfaces it verbatim in the reply.
	call := HarnessToolCall{Name: "generate_sound", Content: "underwater scuba swimming sound", Duration: "8", Model: conv842SoundModel}
	_, out, _ := runSound(t, call, HarnessToolExecutionContext{Config: defaultAppConfig()}, mediasnifftest.WAV(24000))
	notices := out.ToolNotices()
	if len(notices) != 1 || !strings.Contains(notices[0], "delivered 3.0s") {
		t.Fatalf("notices = %v, want the length disagreement", notices)
	}
}
