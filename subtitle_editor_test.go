package main

import (
	"math"
	"strings"
	"testing"
)

func TestResolveSubtitleEditSource(t *testing.T) {
	// The bound method loads config through loadReadyConfig, so HOME is
	// redirected at a temp dir (editTestHome) and the fixture lives under
	// that same storage.
	config := editTestHome(t)
	subtitleConversationFixture(t, config.Storage)
	app := NewApp()

	source, err := app.ResolveSubtitleEditSource("conv_sub", "trc_new")
	if err != nil {
		t.Fatalf("ResolveSubtitleEditSource returned error: %v", err)
	}
	if source.Format != subtitleFormatSRT || len(source.Cues) != 1 {
		t.Fatalf("source format/cues = %s/%d, want srt/1", source.Format, len(source.Cues))
	}
	if len(source.Cues) != 1 || source.Cues[0].Text != "Newest subtitles" {
		t.Fatalf("cues = %+v", source.Cues)
	}
	if !strings.HasPrefix(source.URL, artifactPrefix) || !strings.HasSuffix(source.URL, "trc_new.srt") {
		t.Fatalf("url = %q", source.URL)
	}
	if source.ConversationTitle != "Subtitles" {
		t.Fatalf("title = %q", source.ConversationTitle)
	}

	// The older .vtt resolves by explicit id and parses as VTT.
	source, err = app.ResolveSubtitleEditSource("conv_sub", "trc_old")
	if err != nil {
		t.Fatalf("ResolveSubtitleEditSource(vtt) returned error: %v", err)
	}
	if source.Format != subtitleFormatVTT || len(source.Cues) != 1 {
		t.Fatalf("vtt source format/cues = %s/%d", source.Format, len(source.Cues))
	}

	// A plain .txt transcript is not subtitle material.
	if _, err := app.ResolveSubtitleEditSource("conv_sub", "trc_plain"); err == nil {
		t.Fatal("a .txt transcript must not resolve as a subtitle source")
	}
	if _, err := app.ResolveSubtitleEditSource("conv_sub", "trc_missing"); err == nil {
		t.Fatal("an unknown artifact id must fail")
	}
	if _, err := app.ResolveSubtitleEditSource("", "trc_old"); err == nil {
		t.Fatal("an empty conversation id must fail")
	}
}

func TestSubtitleCueWarnings(t *testing.T) {
	clean := []SubtitleCue{
		{StartSeconds: 0, EndSeconds: 2, Text: "Perfectly fine"},
		{StartSeconds: 3, EndSeconds: 5, Text: "Also fine"},
	}
	if got := subtitleCueWarnings(clean); len(got) != 0 {
		t.Fatalf("clean doc warned: %v", got)
	}
	dirty := []SubtitleCue{
		{StartSeconds: -1, EndSeconds: 0.2, Text: "hi"},
		{StartSeconds: 0.1, EndSeconds: 3, Text: "overlaps"},
		{StartSeconds: 4, EndSeconds: 4.4, Text: strings.Repeat("x", 40)},
	}
	warnings := subtitleCueWarnings(dirty)
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"negative start", "overlaps the previous cue", "reading speed", "shorter than half a second"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings missing %q: %v", want, warnings)
		}
	}
}

func TestRenderSubtitlesExport(t *testing.T) {
	cues := []SubtitleCue{{StartSeconds: 1.5, EndSeconds: 4, Text: "Hello"}}
	data, extension, err := renderSubtitlesExport(ExportSubtitlesRequest{Format: "srt", Cues: cues})
	if err != nil {
		t.Fatalf("renderSubtitlesExport returned error: %v", err)
	}
	if extension != ".srt" || !strings.Contains(string(data), "00:00:01,500 --> 00:00:04,000") {
		t.Fatalf("srt export wrong: ext=%s data=%q", extension, string(data))
	}

	data, extension, err = renderSubtitlesExport(ExportSubtitlesRequest{Format: "vtt", Cues: cues})
	if err != nil {
		t.Fatalf("vtt export returned error: %v", err)
	}
	if extension != ".vtt" || !strings.HasPrefix(string(data), "WEBVTT") {
		t.Fatalf("vtt export wrong: ext=%s data=%q", extension, string(data))
	}

	if _, _, err := renderSubtitlesExport(ExportSubtitlesRequest{Format: "ass", Cues: cues}); err == nil {
		t.Fatal("unknown format must fail")
	}
	if _, _, err := renderSubtitlesExport(ExportSubtitlesRequest{Format: "vtt"}); err == nil {
		t.Fatal("empty doc must fail")
	}
	if _, _, err := renderSubtitlesExport(ExportSubtitlesRequest{Format: "vtt", Cues: []SubtitleCue{{StartSeconds: 5, EndSeconds: 1, Text: "x"}}}); err == nil {
		t.Fatal("cue ending before its start must fail")
	}
	if _, _, err := renderSubtitlesExport(ExportSubtitlesRequest{Format: "vtt", Cues: []SubtitleCue{{StartSeconds: math.NaN(), EndSeconds: 2, Text: "x"}}}); err == nil {
		t.Fatal("non-finite timestamp must fail")
	}
}

func TestNewestEditorArtifactSubtitleKind(t *testing.T) {
	storage := libraryTestStorage(t)
	subtitleConversationFixture(t, storage)

	// The subtitle kind resolves the newest subtitle artifact — the .srt —
	// and never a plain .txt transcript.
	kind, artifactID, err := newestEditorArtifact(storage, "conv_sub", "subtitle")
	if err != nil {
		t.Fatalf("newestEditorArtifact(subtitle) returned error: %v", err)
	}
	if kind != "subtitle" || artifactID != "trc_new" {
		t.Fatalf("got %s/%s, want subtitle/trc_new", kind, artifactID)
	}

	// Kindless stays visual-media-only: it answers with the conversation's
	// image, never a transcript artifact.
	kind, artifactID, err = newestEditorArtifact(storage, "conv_sub", "")
	if err != nil {
		t.Fatalf("kindless open returned error: %v", err)
	}
	if kind != "image" || artifactID != "img_old" {
		t.Fatalf("kindless got %s/%s, want image/img_old", kind, artifactID)
	}

	// Unknown kinds are still rejected.
	if _, _, err := newestEditorArtifact(storage, "conv_sub", "caption"); err == nil {
		t.Fatal("an unknown editor kind must fail")
	}
}
