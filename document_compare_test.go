package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"atelier/internal/document"
)

// Artifact ids in the real md_<24 hex> shape. draft1 and notes share the
// composer's short mention width ("@md_3fa09c1b2"), so that short form is
// ambiguous in the fixture while draft2's is not.
const (
	compareDraft1 = "md_3fa09c1b2d4e5f60718293a4"
	compareDraft2 = "md_77ab01cd9e8f7a6b5c4d3e2f"
	compareNotes  = "md_3fa09c1b2ffff0000aaaabbb"
	compareCopy   = "md_c0c0c0c0c0c0c0c0c0c0c0c0"
)

const compareDraft1Text = `Title: The Roof
Author: Sam

INT. KITCHEN - DAY

JOHN
I never said that.

MARY
You did.

EXT. ROOF - NIGHT

Rain hammers the tiles.
`

const compareDraft2Text = `Title: The Roof
Author: Sam

INT. KITCHEN - DAY

JOHN
I never said it.

MARY
You did.

EXT. ROOF - NIGHT

Rain hammers the tiles.

INT. WAREHOUSE - NIGHT

Crates everywhere.
`

// compareDocument is a document fixture entry: its id, extension, and text.
type compareDocument struct {
	id, ext, text string
}

func (doc compareDocument) content() HistoryContent {
	mime := "text/markdown"
	if doc.ext == ".fountain" {
		mime = "text/x-fountain"
	}
	return HistoryContent{Type: "markdown", ArtifactID: doc.id, Path: "artifacts/" + doc.id + doc.ext, MimeType: mime}
}

var (
	compareDraft1Doc = compareDocument{compareDraft1, ".fountain", compareDraft1Text}
	compareDraft2Doc = compareDocument{compareDraft2, ".fountain", compareDraft2Text}
	compareNotesDoc  = compareDocument{compareNotes, ".md", "# Notes\n\nTighten act two.\n"}
	compareCopyDoc   = compareDocument{compareCopy, ".fountain", compareDraft1Text}
)

// compareConversationFixture writes conv_cmp: the documents saved on earlier
// assistant turns (plus an image), and a NEWEST user turn carrying the given
// references — the record state while the compare ask's tools run, since the
// user turn persists before tools do.
func compareConversationFixture(t *testing.T, storage ConfigStorage, userTurn ...HistoryContent) {
	t.Helper()
	dir := "2026/10/conv_cmp"
	artifacts := filepath.Join(storage.History, "conversations", dir, "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	for _, doc := range []compareDocument{compareDraft1Doc, compareDraft2Doc, compareNotesDoc, compareCopyDoc} {
		if err := os.WriteFile(filepath.Join(artifacts, doc.id+doc.ext), []byte(doc.text), 0644); err != nil {
			t.Fatalf("write %s: %v", doc.id, err)
		}
	}
	writeSearchConversation(t, storage, dir,
		searchConversationFixture("conv_cmp", "Drafts", "2026-10-01T11:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z",
			compareDraft1Doc.content(),
			HistoryContent{Type: "image", ArtifactID: "img_3fa09c1b2aaa", Path: "artifacts/img_3fa09c1b2aaa.png", MimeType: "image/png"}),
		searchTurnFixture("turn_000002", "assistant", "2026-10-01T10:30:00Z",
			compareDraft2Doc.content(), compareNotesDoc.content(), compareCopyDoc.content()),
		searchTurnFixture("turn_000003", "user", "2026-10-01T11:00:00Z", userTurn...),
	)
}

// compareToolContext points a tool execution context at conv_cmp with an
// OpenEditor hook recording every launch as [kind, conversation, artifact,
// seeds...].
func compareToolContext(t *testing.T, config AppConfig) (HarnessToolExecutionContext, *[][]string) {
	t.Helper()
	tools := newHarnessToolExecutionContext(config)
	tools.Storage = config.Storage
	tools.ConversationID = "conv_cmp"
	fired := &[][]string{}
	tools.OpenEditor = func(kind, conversationID, artifactID string, seedArtifactIDs []string) error {
		*fired = append(*fired, append([]string{kind, conversationID, artifactID}, seedArtifactIDs...))
		return nil
	}
	return tools, fired
}

func compareDefinition(t *testing.T, config AppConfig) HarnessToolDefinition {
	t.Helper()
	definition, ok := defaultHarnessToolRegistry(context.Background(), config, nil).Get("compare_documents")
	if !ok {
		t.Fatalf("compare_documents missing from the default tool registry")
	}
	return definition
}

func TestCompareDocumentsToolDefaultsToTheTurnsDocuments(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	compareConversationFixture(t, config.Storage, compareDraft1Doc.content(), compareDraft2Doc.content())
	tools, fired := compareToolContext(t, config)
	definition := compareDefinition(t, config)

	output, summary, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents"})
	if err != nil {
		t.Fatalf("compare_documents returned error: %v", err)
	}
	if want := [][]string{{"compare", "conv_cmp", "", compareDraft1, compareDraft2}}; !reflect.DeepEqual(*fired, want) {
		t.Fatalf("launch hook fired %v, want %v", *fired, want)
	}
	result, ok := output.(ToolCompareDocumentsResult)
	if !ok {
		t.Fatalf("output type = %T, want ToolCompareDocumentsResult", output)
	}
	if result.BeforeArtifactID != compareDraft1 || result.AfterArtifactID != compareDraft2 || result.BeforeFormat != document.FormatFountain || result.AfterFormat != document.FormatFountain {
		t.Fatalf("result identity = %+v", result)
	}
	// One changed dialogue line (1+/1-) and a new scene: its heading, its
	// action, and the two blanks that separate them (4+).
	if result.Added != 5 || result.Removed != 1 || result.Changes != 2 || result.Identical {
		t.Fatalf("result counts = +%d -%d in %d changes (identical %v), want +5 -1 in 2", result.Added, result.Removed, result.Changes, result.Identical)
	}
	// The new scene files under its own heading, not the scene its leading
	// blank line still sits in.
	if want := []string{"INT. KITCHEN - DAY", "INT. WAREHOUSE - NIGHT"}; !reflect.DeepEqual(result.ChangedSections, want) {
		t.Fatalf("changed sections = %q, want %q", result.ChangedSections, want)
	}
	for _, want := range []string{"@@ INT. KITCHEN - DAY · before 7 · after 7", "- I never said that.", "+ I never said it.", "@@ INT. WAREHOUSE - NIGHT · before — · after 15–18", "+ INT. WAREHOUSE - NIGHT", "+ Crates everywhere."} {
		if !strings.Contains(result.Excerpt, want) {
			t.Fatalf("excerpt missing %q:\n%s", want, result.Excerpt)
		}
	}
	if strings.Contains(result.Excerpt, "not shown") {
		t.Fatalf("a short diff must not carry the truncation note:\n%s", result.Excerpt)
	}
	for _, want := range []string{compareDraft1 + " (before)", compareDraft2 + " (after)", "5 lines added and 1 removed across 2 changes", "INT. KITCHEN - DAY; INT. WAREHOUSE - NIGHT", "do not paste it back", "Nothing was modified."} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %s", want, summary)
		}
	}
}

func TestCompareDocumentsToolNamedSides(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	compareConversationFixture(t, config.Storage, compareDraft1Doc.content(), compareDraft2Doc.content())
	tools, fired := compareToolContext(t, config)
	definition := compareDefinition(t, config)

	// The short @-mention form resolves as a unique prefix, and naming both
	// sides reverses the default order.
	output, _, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents", ArtifactID: "@md_77ab01cd9", CompareArtifactID: compareDraft1})
	if err != nil {
		t.Fatalf("named compare returned error: %v", err)
	}
	if result := output.(ToolCompareDocumentsResult); result.BeforeArtifactID != compareDraft2 || result.AfterArtifactID != compareDraft1 || result.Added != 1 || result.Removed != 5 {
		t.Fatalf("named result = %+v, want draft2 before draft1", result)
	}

	// One named side: the other fills from the turn's remaining document.
	output, _, err = definition.Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents", ArtifactID: compareDraft2})
	if err != nil {
		t.Fatalf("half-named compare returned error: %v", err)
	}
	if result := output.(ToolCompareDocumentsResult); result.BeforeArtifactID != compareDraft2 || result.AfterArtifactID != compareDraft1 {
		t.Fatalf("half-named result = %+v, want draft2 before, the turn's other document after", result)
	}

	// Documents from earlier in the conversation are nameable even when the
	// turn references none of them.
	output, _, err = definition.Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents", ArtifactID: compareNotes, CompareArtifactID: compareDraft2})
	if err != nil {
		t.Fatalf("history compare returned error: %v", err)
	}
	if result := output.(ToolCompareDocumentsResult); result.BeforeFormat != document.FormatMarkdown || result.AfterFormat != document.FormatFountain {
		t.Fatalf("mixed-format result = %+v", result)
	}
	if len(*fired) != 3 {
		t.Fatalf("launch hook fired %d times, want 3", len(*fired))
	}
}

func TestCompareDocumentsToolErrors(t *testing.T) {
	cases := []struct {
		name     string
		userTurn []HistoryContent
		call     HarnessToolCall
		want     string
	}{
		{"no documents", nil, HarnessToolCall{}, "this turn references no documents"},
		{"one document", []HistoryContent{compareDraft1Doc.content()}, HarnessToolCall{}, "only one document (" + compareDraft1 + ")"},
		{"three unnamed", []HistoryContent{compareDraft1Doc.content(), compareDraft2Doc.content(), compareNotesDoc.content()}, HarnessToolCall{}, "this turn references 3 documents"},
		{"three, one named", []HistoryContent{compareDraft1Doc.content(), compareDraft2Doc.content(), compareNotesDoc.content()}, HarnessToolCall{ArtifactID: compareNotes}, "name the two to compare"},
		{"same document twice", []HistoryContent{compareDraft1Doc.content(), compareDraft2Doc.content()}, HarnessToolCall{ArtifactID: compareDraft1, CompareArtifactID: "@" + compareDraft1}, "same document"},
		{"ambiguous prefix", []HistoryContent{compareDraft1Doc.content(), compareDraft2Doc.content()}, HarnessToolCall{ArtifactID: "@md_3fa09c1b2"}, "2 documents whose id starts with"},
		{"not a document", []HistoryContent{compareDraft1Doc.content(), compareDraft2Doc.content()}, HarnessToolCall{ArtifactID: "img_3fa09c1b2aaa"}, "is an image, not a document"},
		{"unknown id", []HistoryContent{compareDraft1Doc.content(), compareDraft2Doc.content()}, HarnessToolCall{CompareArtifactID: "md_nope"}, "no document with id \"md_nope\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := newEditorLaunchTestConfig(t)
			compareConversationFixture(t, config.Storage, tc.userTurn...)
			tools, fired := compareToolContext(t, config)
			tc.call.Name = "compare_documents"
			_, summary, err := compareDefinition(t, config).Execute(t.Context(), tools, tc.call)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
			if summary != "document comparison failed" {
				t.Fatalf("summary = %q", summary)
			}
			if len(*fired) != 0 {
				t.Fatalf("a failed comparison must not open the workspace, fired %v", *fired)
			}
		})
	}
}

func TestCompareDocumentsToolRequiresConversationAndHook(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	compareConversationFixture(t, config.Storage, compareDraft1Doc.content(), compareDraft2Doc.content())
	definition := compareDefinition(t, config)

	tools, _ := compareToolContext(t, config)
	tools.ConversationID = ""
	if _, summary, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents"}); err == nil || summary != "document comparison is unavailable" {
		t.Fatalf("no conversation: err %v summary %q", err, summary)
	}
	tools, _ = compareToolContext(t, config)
	tools.OpenEditor = nil
	if _, summary, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents"}); err == nil || summary != "document comparison is unavailable" {
		t.Fatalf("no hook: err %v summary %q", err, summary)
	}
	// The launch is the execution: a hook that cannot deliver fails the call.
	tools, _ = compareToolContext(t, config)
	tools.OpenEditor = func(string, string, string, []string) error { return errors.New("no UI is attached") }
	if _, _, err := definition.Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents"}); err == nil || !strings.Contains(err.Error(), "no UI is attached") {
		t.Fatalf("undeliverable launch: err %v", err)
	}
}

func TestCompareDocumentsGatewayDeliveryFailsWithoutUI(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	compareConversationFixture(t, config.Storage, compareDraft1Doc.content(), compareDraft2Doc.content())
	gateway := newToolGateway(NewApp(), config, defaultHarnessToolRegistry(context.Background(), config, nil))
	gateway.tools.Storage = config.Storage
	gateway.tools.ConversationID = "conv_cmp"
	outcome := gateway.Execute(t.Context(), ToolExecutionRequest{Name: "compare_documents", Call: HarnessToolCall{Name: "compare_documents"}, ConversationID: "conv_cmp"})
	if outcome.Status != "failed" || !strings.Contains(outcome.Error, "could not be delivered") {
		t.Fatalf("gateway compare_documents outcome = %+v, want a delivery failure", outcome)
	}
}

func TestCompareDocumentsToolIdenticalAndMixed(t *testing.T) {
	config := newEditorLaunchTestConfig(t)
	compareConversationFixture(t, config.Storage, compareDraft1Doc.content(), compareCopyDoc.content())
	tools, fired := compareToolContext(t, config)
	output, summary, err := compareDefinition(t, config).Execute(t.Context(), tools, HarnessToolCall{Name: "compare_documents"})
	if err != nil {
		t.Fatalf("identical compare returned error: %v", err)
	}
	result := output.(ToolCompareDocumentsResult)
	if !result.Identical || result.Excerpt != "" || len(result.ChangedSections) != 0 {
		t.Fatalf("identical result = %+v", result)
	}
	if !strings.Contains(summary, "identical") || !strings.Contains(summary, "no differences to describe") {
		t.Fatalf("identical summary = %q", summary)
	}
	if len(*fired) != 1 {
		t.Fatalf("identical documents still open the workspace, fired %v", *fired)
	}

	mixed := documentCompareSummary(ToolCompareDocumentsResult{BeforeArtifactID: "md_a", AfterArtifactID: "md_b", BeforeFormat: document.FormatMarkdown, AfterFormat: document.FormatFountain, Added: 1, Removed: 1, Changes: 1})
	if !strings.Contains(mixed, "One document is Markdown and the other Fountain") || !strings.Contains(mixed, "1 line added and 1 removed across 1 change") {
		t.Fatalf("mixed summary = %q", mixed)
	}
}

func TestDocumentCompareExcerptCaps(t *testing.T) {
	before, after := []string{}, []string{}
	for i := 0; i < 60; i++ {
		before = append(before, "keep "+strings.Repeat("x", i%3))
		before = append(before, "old line "+string(rune('a'+i%26))+strings.Repeat(" long", 60))
		after = append(after, "keep "+strings.Repeat("x", i%3))
		after = append(after, "new line "+string(rune('a'+i%26))+strings.Repeat(" long", 60))
	}
	diff := document.BuildDiff(strings.Join(before, "\n"), strings.Join(after, "\n"), document.FormatMarkdown, document.FormatMarkdown, false)
	excerpt := documentCompareExcerpt(diff)
	changed := 0
	for _, line := range strings.Split(excerpt, "\n") {
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "+ ") {
			changed++
			if len([]rune(line)) > 2+documentCompareExcerptLineRunes+len("...") {
				t.Fatalf("excerpt line not truncated (%d runes): %q", len([]rune(line)), line)
			}
		}
	}
	if changed > documentCompareExcerptLines || changed == 0 {
		t.Fatalf("excerpt shows %d changed lines, want 1..%d", changed, documentCompareExcerptLines)
	}
	if len(excerpt) > documentCompareExcerptChars+200 {
		t.Fatalf("excerpt is %d bytes, want it near the %d budget", len(excerpt), documentCompareExcerptChars)
	}
	if !strings.Contains(excerpt, "more changed lines not shown — the full comparison is on the user's screen") {
		t.Fatalf("a capped excerpt must say what it left out:\n%s", excerpt)
	}
}

func TestResolveDocumentCompare(t *testing.T) {
	config := editTestHome(t)
	compareConversationFixture(t, config.Storage, compareDraft1Doc.content(), compareDraft2Doc.content())
	app := NewApp()

	info, err := app.ResolveDocumentCompare(DocumentCompareRequest{ConversationID: "conv_cmp", BeforeArtifactID: compareDraft1, AfterArtifactID: compareDraft2})
	if err != nil {
		t.Fatalf("ResolveDocumentCompare returned error: %v", err)
	}
	if info.Before.ArtifactID != compareDraft1 || info.After.ArtifactID != compareDraft2 || info.Before.Text != compareDraft1Text || info.After.Format != document.FormatFountain {
		t.Fatalf("sides = %+v / %+v", info.Before, info.After)
	}
	if info.Before.ConversationTitle != "Drafts" || !strings.HasPrefix(info.After.URL, artifactPrefix) {
		t.Fatalf("source identity = %+v", info.After)
	}
	if info.Diff.Stats.Changes != 2 || len(info.Diff.Rows) == 0 || len(info.Diff.Hunks) != 2 {
		t.Fatalf("diff = %+v", info.Diff.Stats)
	}

	// Whitespace-only differences vanish under ignoreWhitespace.
	spaced := compareDocument{"md_5pace5pace5pace5pace5pac", ".fountain", strings.ReplaceAll(compareDraft1Text, "JOHN\n", "JOHN   \n")}
	if err := os.WriteFile(filepath.Join(config.Storage.History, "conversations", "2026/10/conv_cmp", "artifacts", spaced.id+spaced.ext), []byte(spaced.text), 0644); err != nil {
		t.Fatalf("write spaced doc: %v", err)
	}
	compareConversationFixture(t, config.Storage, compareDraft1Doc.content(), spaced.content())
	exact, err := app.ResolveDocumentCompare(DocumentCompareRequest{ConversationID: "conv_cmp", BeforeArtifactID: compareDraft1, AfterArtifactID: spaced.id})
	if err != nil || exact.Diff.Stats.Identical {
		t.Fatalf("exact compare = %+v, %v; want a whitespace difference", exact.Diff.Stats, err)
	}
	loose, err := app.ResolveDocumentCompare(DocumentCompareRequest{ConversationID: "conv_cmp", BeforeArtifactID: compareDraft1, AfterArtifactID: spaced.id, IgnoreWhitespace: true})
	if err != nil || !loose.Diff.Stats.Identical || !loose.IgnoreWhitespace {
		t.Fatalf("ignore-whitespace compare = %+v, %v; want identical", loose.Diff.Stats, err)
	}

	for name, req := range map[string]DocumentCompareRequest{
		"no conversation": {BeforeArtifactID: compareDraft1, AfterArtifactID: compareDraft2},
		"no after":        {ConversationID: "conv_cmp", BeforeArtifactID: compareDraft1},
		"unknown":         {ConversationID: "conv_cmp", BeforeArtifactID: compareDraft1, AfterArtifactID: "md_nope"},
		"image":           {ConversationID: "conv_cmp", BeforeArtifactID: "img_3fa09c1b2aaa", AfterArtifactID: compareDraft2},
	} {
		if _, err := app.ResolveDocumentCompare(req); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestCompareDocumentsActivityAndKwargs(t *testing.T) {
	definition := compareDefinition(t, defaultAppConfig())
	activity := definition.Activity(HarnessToolResult{Name: "compare_documents", Status: "completed", Result: ToolCompareDocumentsResult{BeforeArtifactID: "md_a", AfterArtifactID: "md_b"}})
	if !reflect.DeepEqual(activity.Command, []string{"compare_documents", "md_a", "md_b"}) {
		t.Fatalf("activity command = %v", activity.Command)
	}

	// The kwarg dialects fill both ids under either spelling.
	call := HarnessToolCall{Name: "compare_documents"}
	applyKwargs(&call, "artifact_id='@md_3fa09c1b2', compareArtifactId='md_77ab01cd9'")
	if call.ArtifactID != "@md_3fa09c1b2" || call.CompareArtifactID != "md_77ab01cd9" {
		t.Fatalf("kwargs call = %+v", call)
	}
}

// TestTriagePromptRoutesDocumentCompareToTools pins the routing paragraph: a
// compare ask is a tool turn with responseMode "text" — never the "document"
// authoring mode (which would write a NEW document), and never a toolless
// text reply that would invent differences it cannot see. Airplane mode
// keeps it: the comparison is local.
func TestTriagePromptRoutesDocumentCompareToTools(t *testing.T) {
	registry := newHarnessToolRegistry([]HarnessToolDefinition{compareDocumentsToolDefinition()})
	prompt := triageSystemPrompt(registry, nil, "/tmp/ws", false)
	for _, want := range []string{
		"A request to COMPARE or DIFF two existing documents",
		"When compare_documents is listed under Available tools (it always is), set needsTools true with responseMode \"text\"",
		"Never route a compare ask to \"document\"",
		"Comparing two documents the user already has is not authoring",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("triage prompt missing %q", want)
		}
	}
	if airplane := triageSystemPrompt(registry, nil, "/tmp/ws", true); !strings.Contains(airplane, "open_editor, and compare_documents still work") {
		t.Error("the airplane paragraph should say compare_documents still works")
	}
}
