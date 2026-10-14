package main

import (
	"math"
	"os"
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

func TestRenderSubtitlesForSave(t *testing.T) {
	cues := []SubtitleCue{{StartSeconds: 1.5, EndSeconds: 4, Text: "Hello"}}
	data, extension, err := renderSubtitlesForSave("srt", cues)
	if err != nil {
		t.Fatalf("renderSubtitlesForSave returned error: %v", err)
	}
	if extension != ".srt" || !strings.Contains(string(data), "00:00:01,500 --> 00:00:04,000") {
		t.Fatalf("srt render wrong: ext=%s data=%q", extension, string(data))
	}

	data, extension, err = renderSubtitlesForSave("vtt", cues)
	if err != nil {
		t.Fatalf("vtt render returned error: %v", err)
	}
	if extension != ".vtt" || !strings.HasPrefix(string(data), "WEBVTT") {
		t.Fatalf("vtt render wrong: ext=%s data=%q", extension, string(data))
	}

	if _, _, err := renderSubtitlesForSave("ass", cues); err == nil {
		t.Fatal("unknown format must fail")
	}
	if _, _, err := renderSubtitlesForSave("vtt", nil); err == nil {
		t.Fatal("empty doc must fail")
	}
	if _, _, err := renderSubtitlesForSave("vtt", []SubtitleCue{{StartSeconds: 5, EndSeconds: 1, Text: "x"}}); err == nil {
		t.Fatal("cue ending before its start must fail")
	}
	if _, _, err := renderSubtitlesForSave("vtt", []SubtitleCue{{StartSeconds: math.NaN(), EndSeconds: 2, Text: "x"}}); err == nil {
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

func TestSaveSubtitleEdit(t *testing.T) {
	config := editTestHome(t)
	subtitleConversationFixture(t, config.Storage)
	app := NewApp()

	result, err := app.SaveSubtitleEdit(SaveSubtitleEditRequest{
		ConversationID:   "conv_sub",
		SourceArtifactID: "trc_old",
		Format:           "srt",
		Cues: []SubtitleCue{
			{StartSeconds: 1, EndSeconds: 2.5, Text: "Edited first"},
			{StartSeconds: 3, EndSeconds: 4, Text: "Edited second"},
		},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleEdit returned error: %v", err)
	}
	if !strings.HasPrefix(result.ArtifactID, "trc_") || result.Format != "srt" || result.CueCount != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.HasPrefix(result.URL, artifactPrefix) || !strings.HasSuffix(result.URL, ".srt") {
		t.Fatalf("url = %q", result.URL)
	}

	// The saved artifact is a parseable SRT file on disk with the edited cues.
	detail, err := getConversation(config.Storage, "conv_sub")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	// The fixture writes its turns without record stats, so the save's
	// increment is the first one the record carries.
	if detail.Conversation.Stats.TurnCount != 1 {
		t.Fatalf("turn count = %d, want 1 (the save turn; the fixture's turns carry no stats)", detail.Conversation.Stats.TurnCount)
	}
	var saved *HistoryContent
	var saveTurn *HistoryTurn
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for j, content := range detail.Turns[i].Content {
			if content.ArtifactID == result.ArtifactID {
				saved = &detail.Turns[i].Content[j]
				saveTurn = &detail.Turns[i]
			}
		}
	}
	if saved == nil || saveTurn == nil {
		t.Fatal("the save turn with the new artifact is missing from history")
	}
	if saved.Type != "transcript" || !strings.HasSuffix(saved.Path, ".srt") {
		t.Fatalf("saved entry = %+v, want a .srt transcript", saved)
	}
	if saveTurn.Role != "assistant" || len(saveTurn.Content) != 2 {
		t.Fatalf("save turn = role %q with %d entries, want an assistant turn with text + transcript", saveTurn.Role, len(saveTurn.Content))
	}
	if !strings.Contains(saveTurn.Content[0].Text, "trc_old") || !strings.Contains(saveTurn.Content[0].Text, "2 cues") {
		t.Fatalf("save summary = %q", saveTurn.Content[0].Text)
	}
	path, err := contentArtifactPath(config.Storage, "conv_sub", *saved)
	if err != nil {
		t.Fatalf("contentArtifactPath returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved artifact: %v", err)
	}
	cues, format, _, err := parseSubtitles(data)
	if err != nil || format != subtitleFormatSRT || len(cues) != 2 || cues[0].Text != "Edited first" {
		t.Fatalf("saved file parse = format %s, %d cues, err %v", format, len(cues), err)
	}

	// Refusals: unknown conversation, and a conversation that is streaming.
	if _, err := app.SaveSubtitleEdit(SaveSubtitleEditRequest{ConversationID: "conv_missing", Format: "vtt", Cues: []SubtitleCue{{StartSeconds: 0, EndSeconds: 1, Text: "x"}}}); err == nil {
		t.Fatal("an unknown conversation must fail")
	}
	app.streamsMu.Lock()
	app.streamConversations["req_live"] = "conv_sub"
	app.streamsMu.Unlock()
	defer func() {
		app.streamsMu.Lock()
		delete(app.streamConversations, "req_live")
		app.streamsMu.Unlock()
	}()
	if _, err := app.SaveSubtitleEdit(SaveSubtitleEditRequest{ConversationID: "conv_sub", Format: "vtt", Cues: []SubtitleCue{{StartSeconds: 0, EndSeconds: 1, Text: "x"}}}); err == nil {
		t.Fatal("a streaming conversation must refuse the save")
	}
	// Validation still applies (borrowed from the export half).
	if _, err := app.SaveSubtitleEdit(SaveSubtitleEditRequest{ConversationID: "conv_sub", Format: "ass", Cues: []SubtitleCue{{StartSeconds: 0, EndSeconds: 1, Text: "x"}}}); err == nil {
		t.Fatal("an unknown format must fail")
	}
}
