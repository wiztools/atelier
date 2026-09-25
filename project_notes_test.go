package main

import (
	"strings"
	"testing"
)

// TestSetProjectNotesRoundTrip covers the storage mutator: notes persist
// trimmed onto the project record, surface on the summary the sidebar tree
// renders from, survive a later rename, and clear on an empty set.
func TestSetProjectNotesRoundTrip(t *testing.T) {
	storage := libraryTestStorage(t)
	library, err := createLibrary(storage, "Library")
	if err != nil {
		t.Fatalf("createLibrary returned error: %v", err)
	}
	project, err := createProject(storage, library.ID, "Verticals")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	if project.Notes != "" {
		t.Fatalf("new project should carry no notes, got %q", project.Notes)
	}

	const note = "Render all video requests in this project at 9:16 aspect ratio in pixar style 3D animation."
	updated, err := setProjectNotes(storage, project.ID, "  "+note+"  ")
	if err != nil {
		t.Fatalf("setProjectNotes returned error: %v", err)
	}
	if updated.Notes != note {
		t.Fatalf("summary notes = %q, want the trimmed block", updated.Notes)
	}
	found, _, err := findProject(storage, project.ID)
	if err != nil {
		t.Fatalf("findProject returned error: %v", err)
	}
	if found.Notes != note {
		t.Fatalf("record notes = %q, want the trimmed block", found.Notes)
	}

	// A rename must not disturb the notes — they are separate facets of the
	// same record, and the sidebar refresh after rename re-reads the summary.
	renamed, err := renameProject(storage, project.ID, "Verticals 2")
	if err != nil {
		t.Fatalf("renameProject returned error: %v", err)
	}
	if renamed.Notes != note {
		t.Fatalf("rename dropped the notes: %q", renamed.Notes)
	}

	cleared, err := setProjectNotes(storage, project.ID, "   ")
	if err != nil {
		t.Fatalf("setProjectNotes(clear) returned error: %v", err)
	}
	if cleared.Notes != "" {
		t.Fatalf("whitespace-only notes should clear, got %q", cleared.Notes)
	}

	if _, err := setProjectNotes(storage, "proj_missing", note); err == nil {
		t.Fatalf("setProjectNotes for an unknown project should fail")
	}
}

// TestResolveTurnProjectNotes covers the turn-start resolution: turn 1 reads
// the request's project, turn 2+ reads the record's, standalone and missing
// references resolve to no note (fail-soft — a bad read must never fail a
// turn), and an oversized block is capped at injection, not rejected.
func TestResolveTurnProjectNotes(t *testing.T) {
	storage := libraryTestStorage(t)
	library, err := createLibrary(storage, "Library")
	if err != nil {
		t.Fatalf("createLibrary returned error: %v", err)
	}
	project, err := createProject(storage, library.ID, "Verticals")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	const note = "Render all video requests at 9:16 in pixar style 3D animation."
	if _, err := setProjectNotes(storage, project.ID, note); err != nil {
		t.Fatalf("setProjectNotes returned error: %v", err)
	}
	conversation := libraryConversationFixture("conv_notes", "Notes", "2026-09-01T10:00:00Z", project.ID)
	writeSearchConversation(t, storage, "2026/09/conv_notes", conversation)
	other := libraryConversationFixture("conv_plain", "Plain", "2026-09-01T10:00:00Z", "")
	writeSearchConversation(t, storage, "2026/09/conv_plain", other)

	config := AppConfig{Storage: storage}

	// Turn 1: the request names the project (already validated by
	// resolveTurnProject at this point).
	if got := resolveTurnProjectNotes(config, ChatRequest{ProjectID: project.ID}); got != note {
		t.Fatalf("turn-1 notes = %q, want %q", got, note)
	}
	// Turn 2+: the record's project wins even when the request still carries
	// a (stale or absent) ProjectID.
	if got := resolveTurnProjectNotes(config, ChatRequest{ConversationID: "conv_notes", ProjectID: "proj_other"}); got != note {
		t.Fatalf("turn-2+ notes = %q, want %q", got, note)
	}
	// A standalone conversation has no project and no note.
	if got := resolveTurnProjectNotes(config, ChatRequest{ConversationID: "conv_plain"}); got != "" {
		t.Fatalf("standalone notes = %q, want empty", got)
	}
	// Missing references fail soft to no note — never a failed turn.
	if got := resolveTurnProjectNotes(config, ChatRequest{ProjectID: "proj_missing"}); got != "" {
		t.Fatalf("missing project notes = %q, want empty", got)
	}
	if got := resolveTurnProjectNotes(config, ChatRequest{ConversationID: "conv_missing"}); got != "" {
		t.Fatalf("missing conversation notes = %q, want empty", got)
	}

	// The block is capped at injection, not rejected: the record keeps what
	// the user wrote, the prompt keeps what fits.
	long := strings.Repeat("é", projectNotesMaxChars+500)
	if _, err := setProjectNotes(storage, project.ID, long); err != nil {
		t.Fatalf("setProjectNotes(long) returned error: %v", err)
	}
	capped := resolveTurnProjectNotes(config, ChatRequest{ProjectID: project.ID})
	if got := len([]rune(capped)); got != projectNotesMaxChars {
		t.Fatalf("capped notes rune count = %d, want %d (rune-safe, never mid-sequence)", got, projectNotesMaxChars)
	}
}

// TestProjectNotesReachPlannerPrompts pins the injection: both planner prompt
// variants carry the block and its precedence rule when set, and stay free of
// any section when empty — the byte-identical no-op that keeps un-noted
// projects exactly as they were.
func TestProjectNotesReachPlannerPrompts(t *testing.T) {
	engine := newHarnessEngine(defaultAppConfig())
	registry := engine.toolRegistry()
	const note = "Render all video requests in this project at 9:16 aspect ratio in pixar style 3D animation."

	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "a cat surfing"}}, ProjectNotes: note}
	for name, prompt := range map[string]string{
		"json":   engine.plannerSystemPrompt(registry, req, nil, "", "", false),
		"native": engine.plannerSystemPromptNative(registry, req, nil, "", "", false),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, "Project instructions") {
				t.Fatalf("%s planner prompt should carry the project-instructions section", name)
			}
			if !strings.Contains(prompt, note) {
				t.Fatalf("%s planner prompt should carry the note verbatim", name)
			}
			if !strings.Contains(prompt, "always overrides") {
				t.Fatalf("%s planner prompt should state the turn-wins precedence rule", name)
			}
		})
	}

	// Empty notes render no section — the prompt is unchanged for projects
	// without instructions, pinned by the section header's absence.
	plain := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "a cat surfing"}}}
	for name, prompt := range map[string]string{
		"json":   engine.plannerSystemPrompt(registry, plain, nil, "", "", false),
		"native": engine.plannerSystemPromptNative(registry, plain, nil, "", "", false),
	} {
		t.Run(name+"/empty", func(t *testing.T) {
			if strings.Contains(prompt, "Project instructions") {
				t.Fatalf("%s planner prompt must not mention project instructions when none are set", name)
			}
		})
	}
}

// TestProjectInstructionsEvidenceNote pins the final-model note shape: present
// with a bracketed label when set, empty when not, so the trailing-note
// channel in preparedResponseRequest stays silent for un-noted projects.
func TestProjectInstructionsEvidenceNote(t *testing.T) {
	if got := projectInstructionsEvidenceNote(""); got != "" {
		t.Fatalf("empty notes should render no evidence note, got %q", got)
	}
	if got := projectInstructionsEvidenceNote("   "); got != "" {
		t.Fatalf("whitespace notes should render no evidence note, got %q", got)
	}
	got := projectInstructionsEvidenceNote("9:16 pixar style")
	if !strings.HasPrefix(got, "[Project instructions applied to this turn's tool calls]") {
		t.Fatalf("evidence note should carry the bracketed label, got %q", got)
	}
	if !strings.Contains(got, "9:16 pixar style") {
		t.Fatalf("evidence note should carry the note verbatim, got %q", got)
	}
}
