package main

// Tests for the forwarded-media feed log (harness.go mediaFeedLog): a chained
// pipeline's intermediate output (extract_audio's .m4a feeding convert_audio,
// generate_speech feeding lip_sync) must not ride the reply as an attachment
// beside the deliverable (conv_34a07011 attached both playbacks). Evidence,
// activities, and user-attached media are never touched.

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestMediaFeedLogChainedIntermediateConsumed(t *testing.T) {
	log := newMediaFeedLog()
	const extracted = "data:audio/mp4;base64,M4A"
	const converted = "data:audio/wav;base64,WAV"

	// Round 1: extract_audio produces the intermediate.
	seq := log.nextExecution()
	log.record(&forwardableMedia{Audio: extracted}, seq)
	// Round 2: convert_audio consumes the seated intermediate and produces
	// the deliverable.
	log.flagConsumed("convert_audio", []string{extracted}, nil, nil)
	seq = log.nextExecution()
	log.record(&forwardableMedia{Audio: converted}, seq)
	// Nothing consumes the deliverable.
	log.flagConsumed("probe_media", []string{converted}, nil, nil)

	results := []HarnessToolResult{
		{Name: "extract_audio", Status: "completed", Result: ToolAudioResult{}},
		{Name: "convert_audio", Status: "completed", Result: ToolAudioResult{}},
	}
	attachable := log.attachableResults(results)
	if len(attachable) != 1 || attachable[0].Name != "convert_audio" {
		t.Fatalf("attachable = %+v, want only the convert_audio deliverable", attachable)
	}
	if audios, _ := audiosFromToolResults(attachable); len(audios) != 0 {
		t.Fatalf("audios collector on attachable results = %d entries, want 0 (no audio result payloads in this fixture)", len(audios))
	}
}

func TestMediaFeedLogLineageConsumesEarlierFeeds(t *testing.T) {
	log := newMediaFeedLog()
	const extracted = "data:audio/mp4;base64,M4A"
	const converted = "data:audio/wav;base64,WAV"

	log.nextExecution()
	log.record(&forwardableMedia{Audio: extracted}, 0)
	log.flagConsumed("convert_audio", []string{extracted}, nil, nil)
	log.nextExecution()
	log.record(&forwardableMedia{Audio: converted}, 1)
	// split_audio consumes the converted deliverable — the extract output fed
	// the same lineage and must be flagged with it.
	log.flagConsumed("split_audio", []string{converted}, nil, nil)

	if !log.isConsumed(0) || !log.isConsumed(1) {
		t.Fatalf("consumed = %v, want both chain links flagged", log.consumed)
	}
}

func TestMediaFeedLogUserAttachmentNeverFlagged(t *testing.T) {
	log := newMediaFeedLog()
	const userClip = "data:audio/mpeg;base64,USER"
	const generated = "data:audio/mpeg;base64,GENERATED"

	seq := log.nextExecution()
	log.record(&forwardableMedia{Audio: generated}, seq)
	// The user's clip holds the slot — forward-feed fills empty slots only, so
	// the generated output never displaced it and a consuming tool consumed
	// the USER's clip, not the feed.
	log.flagConsumed("transcribe_audio", []string{userClip}, nil, nil)

	if log.isConsumed(seq) {
		t.Fatal("a feed whose output never seated the slot must not be flagged")
	}
}

func TestMediaFeedLogNonConsumingToolLeavesFeed(t *testing.T) {
	log := newMediaFeedLog()
	const extracted = "data:audio/mp4;base64,M4A"

	seq := log.nextExecution()
	log.record(&forwardableMedia{Audio: extracted}, seq)
	// probe_media reads the VIDEO slot; a seated audio feed is untouched.
	log.flagConsumed("probe_media", []string{extracted}, nil, nil)
	if log.isConsumed(seq) {
		t.Fatal("probe_media must not flag an audio feed")
	}
	// A video feed, by contrast, is exactly what probe_media consumes.
	log.record(&forwardableMedia{Video: "data:video/mp4;base64,V"}, seq)
	log.flagConsumed("probe_media", nil, []string{"data:video/mp4;base64,V"}, nil)
	if !log.isConsumed(seq) {
		t.Fatal("probe_media seated with the feed's video must flag it")
	}
}

func TestMediaFeedLogNilSafety(t *testing.T) {
	var log *mediaFeedLog
	log.flagConsumed("convert_audio", []string{"data:audio/mp4;base64,X"}, nil, nil)
	if got := log.nextExecution(); got != -1 {
		t.Fatalf("nextExecution on a nil log = %d, want -1", got)
	}
	log.record(&forwardableMedia{Audio: "data:audio/mp4;base64,X"}, 0)
	if log.isConsumed(0) {
		t.Fatal("nil log must not report consumption")
	}
	results := []HarnessToolResult{{Name: "extract_audio", Status: "completed"}}
	if got := log.attachableResults(results); len(got) != 1 {
		t.Fatalf("attachableResults on a nil log = %d entries, want the input unchanged", len(got))
	}
}

// TestChainedToolIntermediateNotAttached drives the real gateway through the
// conv_047accca composition (generate_speech → lip_sync) the way
// runHarnessToolCalls does — flag, execute, record — and asserts the speech
// clip is dropped from the attachment collection while the lipsynced video
// stays. The user's turn shows one player, not a stepping stone plus the
// deliverable.
func TestChainedToolIntermediateNotAttached(t *testing.T) {
	audioBytes := []byte{0xFF, 0xE0, 0x00, 0x00, 'n', 'a', 'r', 'r', 'a', 't', 'e'}
	registry := newHarnessToolRegistry([]HarnessToolDefinition{
		speechGenerationToolDefinition(false, "fal"),
		lipsyncToolDefinition(defaultAppConfig(), false),
	})
	tools := HarnessToolExecutionContext{
		Config: defaultAppConfig(),
		GenerateAudio: func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			return GeneratedAudio{Data: audioBytes, MimeType: "audio/mpeg"}, nil
		},
		GenerateLipsync: func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error) {
			return GeneratedVideo{Data: []byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, MimeType: "video/mp4"}, nil
		},
	}
	const userFace = "data:image/png;base64,FACE"
	tools.AttachedImages = []string{userFace}
	gateway := ToolGateway{registry: registry, tools: tools}

	log := newMediaFeedLog()
	calls := []HarnessToolCall{
		{Name: "generate_speech", Content: "narration"},
		{Name: "lip_sync"},
	}
	var results []HarnessToolResult
	for _, call := range calls {
		log.flagConsumed(call.Name, gateway.tools.AttachedAudios, gateway.tools.AttachedVideos, gateway.tools.AttachedImages)
		seq := log.nextExecution()
		result := gateway.Execute(context.Background(), ToolExecutionRequest{Name: call.Name, Call: call})
		results = append(results, result)
		batchResults := []HarnessToolResult{result}
		if generated := forwardableMediaFromResults(batchResults); generated != nil {
			log.record(generated, seq)
			if len(gateway.tools.AttachedAudios) == 0 && strings.TrimSpace(generated.Audio) != "" {
				gateway.tools.AttachedAudios = []string{generated.Audio}
			}
			if len(gateway.tools.AttachedVideos) == 0 && generated.Video != "" {
				gateway.tools.AttachedVideos = []string{generated.Video}
			}
			if len(gateway.tools.AttachedImages) == 0 && len(generated.Images) > 0 {
				gateway.tools.AttachedImages = generated.Images
			}
		}
	}
	if results[0].Status != "completed" || results[1].Status != "completed" {
		t.Fatalf("both calls should complete, got %q and %q", results[0].Status, results[1].Status)
	}

	attachable := log.attachableResults(results)
	if len(attachable) != 1 || attachable[0].Name != "lip_sync" {
		t.Fatalf("attachable = %+v, want only the lip_sync deliverable", attachable)
	}
	if videos, _ := videosFromToolResults(attachable); len(videos) != 1 {
		t.Fatalf("videos collector = %d entries, want the lipsynced video", len(videos))
	}
	if audios, _ := audiosFromToolResults(attachable); len(audios) != 0 {
		t.Fatalf("audios collector = %d entries, want the consumed speech clip dropped", len(audios))
	}

	// The consumed intermediate's temp file is cleaned up; the deliverable's
	// is left for the persist-or-cleanup lifecycle.
	cleanupConsumedMediaTempFiles(results, log)
	if audios, _ := audiosFromToolResults(results[:1]); len(audios) == 1 {
		if _, err := os.Stat(audios[0].TempPath); !os.IsNotExist(err) {
			t.Fatalf("consumed intermediate temp should have been removed: %v", err)
		}
	}
	defer cleanupAudioTempFiles(func() []ToolAudioFile {
		if audios, _ := audiosFromToolResults(attachable); len(audios) > 0 {
			return audios
		}
		if videos, _ := videosFromToolResults(attachable); len(videos) > 0 {
			return nil
		}
		return nil
	}())
}

// TestToolConsumedMediaKindsMatchRegistry pins the consumption table against
// the full tool-definition universe (constructors called directly — the
// assembled registry filters by configured backends and CLIs, so it is
// environment-dependent). A tool rename would otherwise silently stop
// flagging (the intermediate would reappear beside its deliverable), and a
// table entry for a tool that no longer exists would be dead weight.
func TestToolConsumedMediaKindsMatchRegistry(t *testing.T) {
	config := defaultAppConfig()
	definitions := filesystemToolDefinitions(config.Tools.Filesystem)
	definitions = append(definitions, openEditorToolDefinition())
	definitions = append(definitions, imageGenerationToolDefinition(config))
	definitions = append(definitions, videoGenerationToolDefinition(config, false))
	definitions = append(definitions, speechGenerationToolDefinition(false, "fal"))
	definitions = append(definitions, soundEffectsGenerationToolDefinition("fal"))
	definitions = append(definitions, extendAudioToolDefinition())
	definitions = append(definitions, transcribeAudioToolDefinition(config))
	definitions = append(definitions, ffmpegToolDefinitions(config)...)
	// The image tool constructors are backend-gated (they return empty when
	// no CLI resolves, and tests pin binary detection off), so their
	// definitions are built directly per backend dialect.
	dialect := basicImageDialectFor("sips")
	definitions = append(definitions,
		convertImageToolDefinition(dialect),
		transformImageToolDefinition(dialect),
		probeImageToolDefinition(dialect),
	)
	definitions = append(definitions, imagemagickImageToolDefinitions()...)
	// probe_media is gated on ffprobe resolving (pinned off in tests).
	definitions = append(definitions, probeMediaToolDefinition())
	definitions = append(definitions, lipsyncToolDefinition(defaultAppConfig(), false))
	definitions = append(definitions, imageUpscaleToolDefinition(config))
	definitions = append(definitions, videoUpscaleToolDefinition(config))
	definitions = append(definitions, videoReframeToolDefinition(config))
	definitions = append(definitions, videoRestyleToolDefinition(config))
	known := map[string]bool{}
	for _, definition := range definitions {
		known[definition.Name] = true
	}
	for name := range toolConsumedMediaKinds {
		if !known[name] {
			t.Fatalf("toolConsumedMediaKinds names %q, which no tool definition produces", name)
		}
	}
}
