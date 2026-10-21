package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const subtitleFixtureVTT = `WEBVTT

NOTE This block is a comment and must be skipped.

meetup
00:00:01.520 --> 00:00:04.080 align:start line:10%
Hello <c.coordinator>world</c>

00:05 --> 00:07.250
Second cue,
two lines

00:00:08.000 --> 00:00:07.000
Backwards times clamp to the start
`

func TestParseSubtitlesVTT(t *testing.T) {
	cues, format, skipped, err := parseSubtitles([]byte(subtitleFixtureVTT))
	if err != nil {
		t.Fatalf("parseSubtitles returned error: %v", err)
	}
	if format != subtitleFormatVTT {
		t.Fatalf("format = %q, want %q", format, subtitleFormatVTT)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(cues) != 3 {
		t.Fatalf("got %d cues, want 3: %+v", len(cues), cues)
	}
	if cues[0].Start != 1.52 || cues[0].End != 4.08 {
		t.Fatalf("cue 0 times = %v/%v, want 1.52/4.08", cues[0].Start, cues[0].End)
	}
	if cues[0].Text != "Hello <c.coordinator>world</c>" {
		t.Fatalf("cue 0 text = %q — parsing must keep tags; rendering strips them", cues[0].Text)
	}
	// Hours-optional clocks and multi-line text.
	if cues[1].Start != 5 || cues[1].End != 7.25 {
		t.Fatalf("cue 1 times = %v/%v, want 5/7.25", cues[1].Start, cues[1].End)
	}
	if cues[1].Text != "Second cue,\ntwo lines" {
		t.Fatalf("cue 1 text = %q", cues[1].Text)
	}
	// An end before its start clamps open (the renderTranscriptVTT convention).
	if cues[2].Start != 8 || cues[2].End != 8 {
		t.Fatalf("cue 2 times = %v/%v, want clamped 8/8", cues[2].Start, cues[2].End)
	}
}

func TestParseSubtitlesSRT(t *testing.T) {
	data := "1\r\n00:00:01,000 --> 00:00:04,080\r\nHello world\r\n\r\n" +
		"2\r\n00:00:05,500 --> 00:00:07,250\r\nSecond cue,\r\ntwo lines\r\n"
	cues, format, skipped, err := parseSubtitles([]byte(data))
	if err != nil {
		t.Fatalf("parseSubtitles returned error: %v", err)
	}
	if format != subtitleFormatSRT {
		t.Fatalf("format = %q, want %q", format, subtitleFormatSRT)
	}
	if skipped != 0 || len(cues) != 2 {
		t.Fatalf("skipped = %d, cues = %d, want 0/2", skipped, len(cues))
	}
	if cues[0].Start != 1 || cues[0].End != 4.08 {
		t.Fatalf("cue 0 times = %v/%v, want 1/4.08", cues[0].Start, cues[0].End)
	}
	if cues[1].Text != "Second cue,\ntwo lines" {
		t.Fatalf("cue 1 text = %q", cues[1].Text)
	}
}

func TestParseSubtitlesLenientMissingIndices(t *testing.T) {
	// SRT files whose numbering drifted (a deleted cue left a gap) still parse:
	// the index line is dropped and the timing line carries the block.
	data := "00:00:01,000 --> 00:00:02,000\nFirst\n\n00:00:03,000 --> 00:00:04,000\nSecond\n"
	cues, _, skipped, err := parseSubtitles([]byte(data))
	if err != nil {
		t.Fatalf("parseSubtitles returned error: %v", err)
	}
	if skipped != 0 || len(cues) != 2 {
		t.Fatalf("skipped = %d, cues = %d, want 0/2", skipped, len(cues))
	}
}

func TestParseSubtitlesCountsMalformedBlocks(t *testing.T) {
	data := "WEBVTT\n\nnot a cue at all\n\n00:00:01.000 --> 00:00:02.000\nGood\n\n00:99:x --> nope\nBad timing\n"
	cues, _, skipped, err := parseSubtitles([]byte(data))
	if err != nil {
		t.Fatalf("parseSubtitles returned error: %v", err)
	}
	if len(cues) != 1 || cues[0].Text != "Good" {
		t.Fatalf("cues = %+v, want the single Good cue", cues)
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}
}

func TestParseSubtitlesRejectsNonSubtitles(t *testing.T) {
	if _, _, _, err := parseSubtitles([]byte("just some prose\nwith no timings\n")); err == nil {
		t.Fatal("parseSubtitles must fail on content with no cue timings")
	}
}

func TestRenderSubtitlesSRT(t *testing.T) {
	rendered := renderSubtitlesSRT([]subtitleCue{
		{Start: 1.52, End: 4.08, Text: "Hello <c.coordinator>world</c> and <i>italics</i>"},
		{Start: 3671.5, End: 3675, Text: "Hour carries"},
	})
	want := "1\n00:00:01,520 --> 00:00:04,080\nHello world and <i>italics</i>\n\n" +
		"2\n01:01:11,500 --> 01:01:15,000\nHour carries\n\n"
	if rendered != want {
		t.Fatalf("rendered =\n%q\nwant\n%q", rendered, want)
	}
}

func TestConvertSubtitlesVTTToSRT(t *testing.T) {
	out, source, cueCount, notices, err := convertSubtitles([]byte(subtitleFixtureVTT), subtitleFormatSRT)
	if err != nil {
		t.Fatalf("convertSubtitles returned error: %v", err)
	}
	if source != subtitleFormatVTT || cueCount != 3 {
		t.Fatalf("source = %q, cueCount = %d, want vtt/3", source, cueCount)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none", notices)
	}
	// The rendered document must parse back to the same cues — the round trip
	// is the converter's contract.
	cues, format, _, err := parseSubtitles(out)
	if err != nil {
		t.Fatalf("converted output does not re-parse: %v", err)
	}
	if format != subtitleFormatSRT {
		t.Fatalf("re-parsed format = %q, want srt", format)
	}
	if len(cues) != 3 || cues[0].Start != 1.52 || cues[0].End != 4.08 {
		t.Fatalf("re-parsed cues = %+v", cues)
	}
	if strings.Contains(string(out), "align:") || strings.Contains(string(out), "<c") {
		t.Fatal("SRT output must not carry VTT cue settings or VTT-only tags")
	}
}

func TestConvertSubtitlesSRTToVTT(t *testing.T) {
	data := "1\n00:00:01,000 --> 00:00:04,080\nHello\n"
	out, source, cueCount, _, err := convertSubtitles([]byte(data), subtitleFormatVTT)
	if err != nil {
		t.Fatalf("convertSubtitles returned error: %v", err)
	}
	if source != subtitleFormatSRT || cueCount != 1 {
		t.Fatalf("source = %q, cueCount = %d, want srt/1", source, cueCount)
	}
	text := string(out)
	if !strings.HasPrefix(text, "WEBVTT") {
		t.Fatalf("VTT output missing the WEBVTT header: %q", text)
	}
	if !strings.Contains(text, "00:00:01.000 --> 00:00:04.080") {
		t.Fatalf("VTT output timestamps wrong: %q", text)
	}
}

func TestConvertSubtitlesRoundTripPreservesCues(t *testing.T) {
	cues := []subtitleCue{
		{Start: 0.5, End: 2.25, Text: "First <i>cue</i>"},
		{Start: 10, End: 12.75, Text: "Second"},
	}
	vtt := renderSubtitlesVTT(cues)
	srt, source, count2, _, err := convertSubtitles([]byte(vtt), subtitleFormatSRT)
	if err != nil || source != subtitleFormatVTT {
		t.Fatalf("vtt→srt: err=%v source=%q", err, source)
	}
	roundTripped, source2, count3, _, err := convertSubtitles(srt, subtitleFormatVTT)
	if err != nil || source2 != subtitleFormatSRT {
		t.Fatalf("srt→vtt: err=%v source=%q", err, source2)
	}
	if count2 != 2 || count3 != 2 {
		t.Fatalf("cue counts = %d/%d, want 2/2", count2, count3)
	}
	parsed, _, _, err := parseSubtitles(roundTripped)
	if err != nil {
		t.Fatalf("round-tripped VTT does not parse: %v", err)
	}
	for i, cue := range parsed {
		if cue.Start != cues[i].Start || cue.End != cues[i].End || cue.Text != cues[i].Text {
			t.Fatalf("cue %d = %+v, want %+v", i, cue, cues[i])
		}
	}
}

func TestConvertSubtitlesNotices(t *testing.T) {
	data := "WEBVTT\n\njunk block\n\n00:00:01.000 --> 00:00:02.000\nOnly cue\n"
	// Same-format conversion normalizes and says so; skipped blocks are named.
	_, _, count, notices, err := convertSubtitles([]byte(data), subtitleFormatVTT)
	if err != nil {
		t.Fatalf("convertSubtitles returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("cueCount = %d, want 1", count)
	}
	if len(notices) != 2 {
		t.Fatalf("notices = %v, want the skip and the same-format notice", notices)
	}
	if !strings.Contains(notices[0], "1 malformed") || !strings.Contains(notices[1], "already WebVTT") {
		t.Fatalf("notices = %v", notices)
	}
}

func TestNormalizeSubtitleTargetFormat(t *testing.T) {
	if got, err := normalizeSubtitleTargetFormat("", subtitleFormatVTT); err != nil || got != subtitleFormatSRT {
		t.Fatalf("empty request on vtt = %q/%v, want srt/nil", got, err)
	}
	if got, err := normalizeSubtitleTargetFormat("", subtitleFormatSRT); err != nil || got != subtitleFormatVTT {
		t.Fatalf("empty request on srt = %q/%v, want vtt/nil", got, err)
	}
	if got, err := normalizeSubtitleTargetFormat(" SRT ", subtitleFormatSRT); err != nil || got != subtitleFormatSRT {
		t.Fatalf("explicit same-format request must normalize, got %q/%v", got, err)
	}
	if _, err := normalizeSubtitleTargetFormat("ass", subtitleFormatVTT); err == nil {
		t.Fatal("unknown target format must fail")
	}
}

func TestSubtitleArtifactExtension(t *testing.T) {
	cases := []struct {
		content HistoryContent
		want    string
	}{
		{HistoryContent{Type: "transcript", Path: "artifacts/trc_a.vtt", MimeType: "text/vtt"}, ".vtt"},
		{HistoryContent{Type: "transcript", Path: "artifacts/trc_b.srt", MimeType: "application/x-subrip"}, ".srt"},
		{HistoryContent{Type: "transcript", Path: "artifacts/trc_c.txt", MimeType: "text/plain"}, ""},
		{HistoryContent{Type: "image", Path: "artifacts/img_a.vtt"}, ""},
		{HistoryContent{Type: "video", Path: "artifacts/vid_a.mp4"}, ""},
	}
	for _, tc := range cases {
		if got := subtitleArtifactExtension(tc.content); got != tc.want {
			t.Fatalf("subtitleArtifactExtension(%+v) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

func subtitleConversationFixture(t *testing.T, storage ConfigStorage) {
	t.Helper()
	artifacts := filepath.Join(storage.History, "conversations", "2026/10/conv_sub", "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nFrom a transcript\n"
	if err := os.WriteFile(filepath.Join(artifacts, "trc_old.vtt"), []byte(vtt), 0644); err != nil {
		t.Fatalf("write vtt artifact: %v", err)
	}
	srt := "1\n00:00:03,000 --> 00:00:04,000\nNewest subtitles\n"
	if err := os.WriteFile(filepath.Join(artifacts, "trc_new.srt"), []byte(srt), 0644); err != nil {
		t.Fatalf("write srt artifact: %v", err)
	}
	writeSearchConversation(t, storage, "2026/10/conv_sub",
		searchConversationFixture("conv_sub", "Subtitles", "2026-10-01T10:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "transcript", ArtifactID: "trc_old", Path: "artifacts/trc_old.vtt", MimeType: "text/vtt"},
			HistoryContent{Type: "transcript", ArtifactID: "trc_plain", Path: "artifacts/trc_plain.txt", MimeType: "text/plain"},
			HistoryContent{Type: "image", ArtifactID: "img_old", Path: "artifacts/img_old.png", MimeType: "image/png"}),
		searchTurnFixture("turn_000002", "assistant", "2026-10-01T10:05:00Z",
			HistoryContent{Type: "transcript", ArtifactID: "trc_new", Path: "artifacts/trc_new.srt", MimeType: "application/x-subrip"}),
	)
}

func TestNewestSubtitleArtifact(t *testing.T) {
	storage := libraryTestStorage(t)
	subtitleConversationFixture(t, storage)

	// Default: the newest turn's first subtitle artifact — the .srt, not the
	// older .vtt, and never the .txt transcript or the image beside it.
	content, extension, err := newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "")
	if err != nil {
		t.Fatalf("newestSubtitleArtifact returned error: %v", err)
	}
	if content.ArtifactID != "trc_new" || extension != ".srt" {
		t.Fatalf("got %s/%s, want trc_new/.srt", content.ArtifactID, extension)
	}

	// An explicit id wins over recency.
	content, extension, err = newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "trc_old")
	if err != nil {
		t.Fatalf("newestSubtitleArtifact(id) returned error: %v", err)
	}
	if content.ArtifactID != "trc_old" || extension != ".vtt" {
		t.Fatalf("got %s/%s, want trc_old/.vtt", content.ArtifactID, extension)
	}

	// An id that exists but is not a subtitle file is a miss, not a fallback.
	if _, _, err := newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "trc_plain"); err == nil {
		t.Fatal("a .txt artifact id must not resolve as a subtitle source")
	}
	if _, _, err := newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "img_old"); err == nil {
		t.Fatal("a non-transcript artifact id must not resolve as a subtitle source")
	}
	if _, _, err := newestSubtitleArtifact(storage, "convert_subtitles", "conv_missing", ""); err == nil {
		t.Fatal("an unknown conversation must fail")
	}

	// The composer's @-mention display form — a leading @ and the truncated
	// short id (conv_84ff8fa9dea9621f290e31e8: the planner passed it verbatim)
	// — resolves through the unique prefix match.
	content, extension, err = newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "@trc_new")
	if err != nil {
		t.Fatalf("newestSubtitleArtifact(@mention) returned error: %v", err)
	}
	if content.ArtifactID != "trc_new" || extension != ".srt" {
		t.Fatalf("got %s/%s, want trc_new/.srt", content.ArtifactID, extension)
	}
	content, _, err = newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "@trc_ol")
	if err != nil || content.ArtifactID != "trc_old" {
		t.Fatalf("@trc_ol = %s/%v, want trc_old", content.ArtifactID, err)
	}

	// A prefix matching more than one subtitle artifact id is ambiguous —
	// named ids never silently resolve to a different file.
	if _, _, err := newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "trc_"); err == nil || !strings.Contains(err.Error(), "starts with") {
		t.Fatalf("ambiguous prefix error = %v, want the ambiguity message", err)
	}
	// A prefix matching nothing keeps the no-match error.
	if _, _, err := newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "trc_zzz"); err == nil {
		t.Fatal("a prefix matching no subtitle artifact must fail")
	}
	// An id that is nothing but the @ normalizes to empty — the default path.
	content, _, err = newestSubtitleArtifact(storage, "convert_subtitles", "conv_sub", "@")
	if err != nil || content.ArtifactID != "trc_new" {
		t.Fatalf("bare @ = %s/%v, want the default newest artifact", content.ArtifactID, err)
	}
}

// TestNewestSubtitleArtifactMentionRecarry mirrors the shape a real
// @-mention produces (conv_84ff8fa9dea9621f290e31e8): the cited artifact
// re-rides the citing user turn under its original id, so the same id
// legitimately appears on two turns. Prefix candidates are counted by
// DISTINCT id — the re-carry must not read as ambiguity.
func TestNewestSubtitleArtifactMentionRecarry(t *testing.T) {
	storage := libraryTestStorage(t)
	artifacts := filepath.Join(storage.History, "conversations", "2026/10/conv_mention", "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	writeSearchConversation(t, storage, "2026/10/conv_mention",
		searchConversationFixture("conv_mention", "Mention recarry", "2026-10-21T17:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-10-21T16:30:00Z",
			HistoryContent{Type: "transcript", ArtifactID: "trc_one", Path: "artifacts/trc_one.srt", MimeType: "application/x-subrip"},
			HistoryContent{Type: "transcript", ArtifactID: "trc_two", Path: "artifacts/trc_two.srt", MimeType: "application/x-subrip"}),
		// The user turn citing trc_one: same id, re-carried entry.
		searchTurnFixture("turn_000002", "user", "2026-10-21T16:59:00Z",
			HistoryContent{Type: "transcript", ArtifactID: "trc_one", Path: "artifacts/trc_one.srt", MimeType: "application/x-subrip"}))

	// The truncated mention form resolves through both re-carried entries.
	content, _, err := newestSubtitleArtifact(storage, "burn_subtitles", "conv_mention", "@trc_on")
	if err != nil || content.ArtifactID != "trc_one" {
		t.Fatalf("@trc_on = %s/%v, want trc_one despite the re-carry", content.ArtifactID, err)
	}
	// Two distinct ids under one prefix are still ambiguous.
	if _, _, err := newestSubtitleArtifact(storage, "burn_subtitles", "conv_mention", "trc_"); err == nil {
		t.Fatal("two distinct subtitle ids under one prefix must stay ambiguous")
	}
	// The newest turn re-carrying the cited artifact makes IT the default —
	// the mention is the strongest recency signal, which is exactly what
	// would have made conv_84ff8fa9's default path burn the right file.
	content, _, err = newestSubtitleArtifact(storage, "burn_subtitles", "conv_mention", "")
	if err != nil || content.ArtifactID != "trc_one" {
		t.Fatalf("default = %s/%v, want trc_one (the cited re-carry on the newest turn)", content.ArtifactID, err)
	}
}

func TestExecuteConvertSubtitlesFromTurnFiles(t *testing.T) {
	// In-turn chaining: the .vtt a transcribe_audio produced earlier THIS turn
	// converts without a persisted record (and without a conversation at all).
	temp, err := writeTempMediaBytes([]byte(subtitleFixtureVTT), "atelier-test-transcript-*", ".vtt")
	if err != nil {
		t.Fatalf("writeTempMediaBytes returned error: %v", err)
	}
	defer os.Remove(temp)

	// A plain .txt staged beside the .vtt (an oversized transcription) is not
	// a subtitle source — the newest SUBTITLE file wins, not the newest file.
	plain, err := writeTempMediaBytes([]byte("a plain transcript without timings"), "atelier-test-transcript-*", ".txt")
	if err != nil {
		t.Fatalf("writeTempMediaBytes returned error: %v", err)
	}
	defer os.Remove(plain)

	tools := HarnessToolExecutionContext{
		TranscriptFiles: []ToolTranscriptFile{
			{TempPath: temp, MimeType: "text/vtt"},
			{TempPath: plain, MimeType: "text/plain"},
		},
	}
	result, summary, err := executeConvertSubtitles(tools, HarnessToolCall{})
	if err != nil {
		t.Fatalf("executeConvertSubtitles returned error: %v", err)
	}
	if result.SourceFormat != subtitleFormatVTT || result.TargetFormat != subtitleFormatSRT || result.CueCount != 3 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Transcripts) != 1 || !strings.HasSuffix(result.Transcripts[0].TempPath, ".srt") {
		t.Fatalf("staged = %+v, want one .srt temp file", result.Transcripts)
	}
	if result.Transcripts[0].MimeType != "application/x-subrip" {
		t.Fatalf("mime = %q, want application/x-subrip", result.Transcripts[0].MimeType)
	}
	defer os.Remove(result.Transcripts[0].TempPath)
	if !strings.Contains(summary, "3 cues") || !strings.Contains(summary, "SubRip") {
		t.Fatalf("summary = %q", summary)
	}
	converted, err := os.ReadFile(result.Transcripts[0].TempPath)
	if err != nil {
		t.Fatalf("read converted: %v", err)
	}
	if !strings.Contains(string(converted), "00:00:01,520 --> 00:00:04,080") {
		t.Fatalf("converted = %q", string(converted))
	}
}

func TestExecuteConvertSubtitlesFromPersistedArtifact(t *testing.T) {
	storage := libraryTestStorage(t)
	subtitleConversationFixture(t, storage)

	tools := HarnessToolExecutionContext{Storage: storage, ConversationID: "conv_sub"}
	result, _, err := executeConvertSubtitles(tools, HarnessToolCall{})
	if err != nil {
		t.Fatalf("executeConvertSubtitles returned error: %v", err)
	}
	defer os.Remove(result.Transcripts[0].TempPath)
	// The newest artifact is the .srt, so the default target flips to VTT.
	if result.SourceFormat != subtitleFormatSRT || result.TargetFormat != subtitleFormatVTT {
		t.Fatalf("result = %+v", result)
	}
	converted, err := os.ReadFile(result.Transcripts[0].TempPath)
	if err != nil {
		t.Fatalf("read converted: %v", err)
	}
	if !strings.HasPrefix(string(converted), "WEBVTT") || !strings.Contains(string(converted), "00:00:03.000 --> 00:00:04.000") {
		t.Fatalf("converted = %q", string(converted))
	}

	// An explicit artifactId reads that artifact instead of the newest one.
	result, _, err = executeConvertSubtitles(tools, HarnessToolCall{ArtifactID: "trc_old"})
	if err != nil {
		t.Fatalf("executeConvertSubtitles(id) returned error: %v", err)
	}
	defer os.Remove(result.Transcripts[0].TempPath)
	if result.SourceFormat != subtitleFormatVTT || result.TargetFormat != subtitleFormatSRT {
		t.Fatalf("id result = %+v", result)
	}

	// The composer's @-mention short form names the same artifact.
	result, _, err = executeConvertSubtitles(tools, HarnessToolCall{ArtifactID: "@trc_ol"})
	if err != nil {
		t.Fatalf("executeConvertSubtitles(@mention) returned error: %v", err)
	}
	defer os.Remove(result.Transcripts[0].TempPath)
	if result.SourceFormat != subtitleFormatVTT {
		t.Fatalf("mention result = %+v, want the .vtt source", result)
	}
}

func TestExecuteConvertSubtitlesWithoutSource(t *testing.T) {
	storage := libraryTestStorage(t)
	tools := HarnessToolExecutionContext{Storage: storage, ConversationID: "conv_empty"}
	if _, _, err := executeConvertSubtitles(tools, HarnessToolCall{}); err == nil {
		t.Fatal("a conversation with no subtitle artifacts must fail the call")
	}
	if _, _, err := executeConvertSubtitles(HarnessToolExecutionContext{}, HarnessToolCall{}); err == nil {
		t.Fatal("no conversation and no in-turn files must fail the call")
	}
	// A plain .txt in turn files is not a subtitle source either — with no
	// conversation behind it there is nothing to fall back to.
	txtOnly := HarnessToolExecutionContext{
		TranscriptFiles: []ToolTranscriptFile{{TempPath: "/tmp/plain.txt", MimeType: "text/plain"}},
	}
	if _, _, err := executeConvertSubtitles(txtOnly, HarnessToolCall{}); err == nil {
		t.Fatal("in-turn files carrying only plain text must not resolve as a subtitle source")
	}
	storage2 := libraryTestStorage(t)
	subtitleConversationFixture(t, storage2)
	if _, _, err := executeConvertSubtitles(HarnessToolExecutionContext{Storage: storage2, ConversationID: "conv_sub"}, HarnessToolCall{Format: "ass"}); err == nil {
		t.Fatal("an unknown target format must fail the call")
	}
}

func TestTranscriptsFromToolResultsCollectsConvertedSubtitles(t *testing.T) {
	converted := ToolTranscriptFile{TempPath: "/tmp/sub.srt", MimeType: "application/x-subrip"}
	results := []HarnessToolResult{
		{Status: "completed", Result: ToolConvertSubtitlesResult{
			SourceFormat: subtitleFormatVTT, TargetFormat: subtitleFormatSRT, CueCount: 2,
			Transcripts: []ToolTranscriptFile{converted},
		}},
		// A failed call contributes nothing, mirroring the transcribe collector.
		{Status: "failed", Result: ToolConvertSubtitlesResult{Transcripts: []ToolTranscriptFile{{TempPath: "/tmp/lost.srt"}}}},
	}
	files, model, timestamps := transcriptsFromToolResults(results)
	if len(files) != 1 || files[0].TempPath != "/tmp/sub.srt" {
		t.Fatalf("files = %+v, want the completed call's file only", files)
	}
	if model != "" || timestamps != "" {
		t.Fatalf("model/timestamps = %q/%q, want empty — subtitle conversions carry no transcription metadata", model, timestamps)
	}
}

func TestToolResultMessageViewStripsConvertedSubtitles(t *testing.T) {
	result := HarnessToolResult{Status: "completed", Result: ToolConvertSubtitlesResult{
		SourceFormat: subtitleFormatVTT, TargetFormat: subtitleFormatSRT,
		Transcripts: []ToolTranscriptFile{{TempPath: "/tmp/sub.srt"}},
	}}
	view := toolResultMessageView(result)
	typed, ok := view.Result.(ToolConvertSubtitlesResult)
	if !ok {
		t.Fatalf("view result type = %T", view.Result)
	}
	if len(typed.Transcripts) != 0 {
		t.Fatalf("evidence must not carry the temp-file refs, got %+v", typed.Transcripts)
	}
}

// subtitleDataURL renders subtitle text as the data URL the frontend composer
// sends for an attached subtitle file.
func subtitleDataURL(t *testing.T, mime, text string) string {
	t.Helper()
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString([]byte(text))
}

func TestHistoryContentForMessagePersistsSubtitles(t *testing.T) {
	config := defaultAppConfig()
	artifactsDir := filepath.Join(t.TempDir(), "artifacts")
	message := ChatMessage{
		Role:    "user",
		Content: "convert this please",
		Subtitles: []string{
			subtitleDataURL(t, "text/vtt", "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n"),
			subtitleDataURL(t, "application/x-subrip", "1\n00:00:03,000 --> 00:00:04,000\nWorld\n"),
		},
	}
	contents, _, err := historyContentForMessage(message, artifactsDir, config)
	if err != nil {
		t.Fatalf("historyContentForMessage returned error: %v", err)
	}
	var subtitles []HistoryContent
	for _, content := range contents {
		if content.Type == "transcript" {
			subtitles = append(subtitles, content)
		}
	}
	if len(subtitles) != 2 {
		t.Fatalf("persisted %d transcript entries, want 2: %+v", len(subtitles), contents)
	}
	if !strings.HasSuffix(subtitles[0].Path, ".vtt") || !strings.HasSuffix(subtitles[1].Path, ".srt") {
		t.Fatalf("extensions = %s/%s, want .vtt/.srt", subtitles[0].Path, subtitles[1].Path)
	}
	if subtitles[0].MimeType != "text/vtt" || subtitles[1].MimeType != "application/x-subrip" {
		t.Fatalf("mimes = %s/%s", subtitles[0].MimeType, subtitles[1].MimeType)
	}

	// A mislabeled upload persists as the format its CONTENT is: the .vtt data
	// under an srt mime still lands as .vtt.
	mislabeled := ChatMessage{Subtitles: []string{subtitleDataURL(t, "application/x-subrip", "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n")}}
	contents, _, err = historyContentForMessage(mislabeled, artifactsDir, config)
	if err != nil {
		t.Fatalf("mislabeled attach returned error: %v", err)
	}
	if len(contents) != 1 || !strings.HasSuffix(contents[0].Path, ".vtt") {
		t.Fatalf("mislabeled mime must not decide the extension: %+v", contents)
	}

	// Non-subtitle content fails the turn's persistence, not silently passes.
	junk := ChatMessage{Subtitles: []string{subtitleDataURL(t, "text/vtt", "just some prose without cues\n")}}
	if _, _, err := historyContentForMessage(junk, artifactsDir, config); err == nil {
		t.Fatal("content with no cues must fail persistence")
	}
}

func TestSubtitleAttachmentURLs(t *testing.T) {
	artifactsDir := t.TempDir()
	contents := []HistoryContent{
		{Type: "text", Text: "hi"},
		{Type: "transcript", ArtifactID: "trc_a", Path: "artifacts/trc_a.vtt", MimeType: "text/vtt"},
		{Type: "transcript", ArtifactID: "trc_b", Path: "artifacts/trc_b.txt", MimeType: "text/plain"},
		{Type: "image", ArtifactID: "img_a", Path: "artifacts/img_a.png", MimeType: "image/png"},
	}
	urls := subtitleAttachmentURLs(contents, artifactsDir)
	if len(urls) != 1 || !strings.HasPrefix(urls[0], artifactPrefix) || !strings.HasSuffix(urls[0], "trc_a.vtt") {
		t.Fatalf("urls = %v, want exactly the vtt artifact URL", urls)
	}
	if !strings.HasPrefix(urls[0], artifactPrefix) {
		t.Fatalf("url must carry the artifact prefix: %q", urls[0])
	}
}

func TestConversationAssetsIncludeSubtitles(t *testing.T) {
	detail := ConversationDetail{
		Conversation: HistoryConversation{ID: "conv_assets"},
		Turns: []HistoryTurn{{
			ID:   "turn_000001",
			Role: "assistant",
			Content: []HistoryContent{
				{Type: "transcript", ArtifactID: "trc_vtt", Path: "artifacts/trc_vtt.vtt", MimeType: "text/vtt"},
				{Type: "transcript", ArtifactID: "trc_srt", Path: "artifacts/trc_srt.srt", MimeType: "application/x-subrip"},
				{Type: "transcript", ArtifactID: "trc_txt", Path: "artifacts/trc_txt.txt", MimeType: "text/plain"},
				{Type: "image", ArtifactID: "img_a", Path: "artifacts/img_a.png", MimeType: "image/png"},
			},
		}},
	}
	assets := conversationAssetsFromDetail(detail)
	kinds := map[string]string{}
	for _, asset := range assets {
		kinds[asset.ID] = asset.Kind
	}
	if kinds["trc_vtt"] != "transcript" || kinds["trc_srt"] != "transcript" {
		t.Fatalf("subtitle assets missing: %+v", kinds)
	}
	if kinds["trc_txt"] != "" {
		t.Fatalf("plain .txt transcripts must not be assets: %+v", kinds)
	}
	if kinds["img_a"] != "image" {
		t.Fatalf("image asset regressed: %+v", kinds)
	}

	// The mention index sees the same set.
	index := assetContentIndex(detail)
	if _, ok := index["trc_srt"]; !ok {
		t.Fatal("asset index must resolve subtitle artifacts")
	}
	if _, ok := index["trc_txt"]; ok {
		t.Fatal("asset index must not resolve plain transcripts")
	}
}

func TestResolveReferencedAssetsResolvesSubtitles(t *testing.T) {
	storage := libraryTestStorage(t)
	subtitleConversationFixture(t, storage)

	referenced := resolveReferencedAssets(storage, "conv_sub", "", []string{"trc_old"})
	if len(referenced.entries) != 1 || referenced.entries[0].ArtifactID != "trc_old" {
		t.Fatalf("entries = %+v, want the mentioned vtt artifact", referenced.entries)
	}
	// Subtitles fill no media slot: they are not model input anywhere.
	if len(referenced.images) != 0 || len(referenced.audios) != 0 || len(referenced.videos) != 0 {
		t.Fatalf("slots = %+v, want empty", referenced)
	}

	// A dangling id (artifact file gone) drops out entirely.
	if got := resolveReferencedAssets(storage, "conv_sub", "", []string{"trc_missing"}); len(got.entries) != 0 {
		t.Fatalf("dangling id must resolve to nothing, got %+v", got.entries)
	}
}

func TestMessagesWithoutMediaStripsSubtitles(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: "hi", Subtitles: []string{"data:text/vtt;base64,V0VCVlRU"}}}
	stripped := messagesWithoutMedia(messages)
	if len(stripped[0].Subtitles) != 0 {
		t.Fatal("subtitles must be stripped before triage like the other media bytes")
	}
	if messages[0].Subtitles == nil {
		t.Fatal("the original messages must be untouched (copy, not in-place)")
	}
}
