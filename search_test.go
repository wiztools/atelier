package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func searchTestStorage(t *testing.T) ConfigStorage {
	t.Helper()
	root := t.TempDir()
	return ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
}

func writeSearchConversation(t *testing.T, storage ConfigStorage, relDir string, conversation HistoryConversation, turns ...HistoryTurn) {
	t.Helper()
	conversationDir := filepath.Join(storage.History, "conversations", relDir)
	turnsDir := filepath.Join(conversationDir, "turns")
	if err := os.MkdirAll(turnsDir, 0755); err != nil {
		t.Fatalf("MkdirAll %s: %v", turnsDir, err)
	}
	if err := writeJSONFile(filepath.Join(conversationDir, "conversation.json"), conversation); err != nil {
		t.Fatalf("write conversation.json: %v", err)
	}
	for _, turn := range turns {
		if err := writeJSONFile(filepath.Join(turnsDir, turn.ID+".json"), turn); err != nil {
			t.Fatalf("write %s: %v", turn.ID, err)
		}
	}
}

func searchConversationFixture(id, title, updatedAt string) HistoryConversation {
	return HistoryConversation{
		SchemaVersion: 2,
		ID:            id,
		Kind:          "chat",
		Title:         title,
		CreatedAt:     updatedAt,
		UpdatedAt:     updatedAt,
	}
}

func searchTurnFixture(id, role, createdAt string, contents ...HistoryContent) HistoryTurn {
	return HistoryTurn{
		SchemaVersion: 1,
		ID:            id,
		Kind:          "chat",
		Role:          role,
		CreatedAt:     createdAt,
		Content:       contents,
	}
}

func TestSearchHistoryLiteral(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "2026/01/conv-a",
		searchConversationFixture("conv-a", "Trip planning", "2026-01-02T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-01-02T10:00:00Z",
			HistoryContent{Type: "text", Text: "What is the capital of France?"}),
		searchTurnFixture("turn_000002", "assistant", "2026-01-02T10:00:05Z",
			HistoryContent{Type: "text", Text: "The capital of France is Paris."}),
	)
	writeSearchConversation(t, storage, "2026/01/conv-b",
		searchConversationFixture("conv-b", "Unrelated", "2026-01-01T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-01-01T10:00:00Z",
			HistoryContent{Type: "text", Text: "Tell me about goats."}),
	)

	response, err := searchHistory(storage, "france", SearchOptions{})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("result count = %d, want 1", len(response.Results))
	}
	result := response.Results[0]
	if result.Conversation.ID != "conv-a" {
		t.Fatalf("conversation id = %q, want conv-a", result.Conversation.ID)
	}
	if result.TitleMatched {
		t.Fatalf("titleMatched = true, want false for turn-only match")
	}
	if len(result.Matches) != 2 {
		t.Fatalf("match count = %d, want 2", len(result.Matches))
	}
	first, second := result.Matches[0], result.Matches[1]
	if first.TurnID != "turn_000001" || first.Role != "user" {
		t.Fatalf("first match = %s/%s, want turn_000001/user", first.TurnID, first.Role)
	}
	if first.Match != "France" {
		t.Fatalf("first match text = %q, want %q", first.Match, "France")
	}
	if first.Before != "What is the capital of " || first.After != "?" {
		t.Fatalf("first snippet = %q|%q|%q", first.Before, first.Match, first.After)
	}
	if second.TurnID != "turn_000002" || second.Role != "assistant" {
		t.Fatalf("second match = %s/%s, want turn_000002/assistant", second.TurnID, second.Role)
	}
	if response.Truncated {
		t.Fatalf("truncated = true, want false")
	}
}

func TestSearchHistoryOrdersConversationsByUpdatedAt(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "conv-old",
		searchConversationFixture("conv-old", "Old needle", "2026-01-01T10:00:00Z"))
	writeSearchConversation(t, storage, "conv-new",
		searchConversationFixture("conv-new", "New needle", "2026-02-01T10:00:00Z"))
	writeSearchConversation(t, storage, "conv-mid",
		searchConversationFixture("conv-mid", "Mid needle", "2026-01-15T10:00:00Z"))

	response, err := searchHistory(storage, "needle", SearchOptions{})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(response.Results) != 3 {
		t.Fatalf("result count = %d, want 3", len(response.Results))
	}
	wantOrder := []string{"conv-new", "conv-mid", "conv-old"}
	for i, want := range wantOrder {
		if response.Results[i].Conversation.ID != want {
			t.Fatalf("result %d = %q, want %q", i, response.Results[i].Conversation.ID, want)
		}
		if !response.Results[i].TitleMatched {
			t.Fatalf("result %d titleMatched = false, want true", i)
		}
	}
}

func TestSearchHistoryCaseSensitivity(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "conv-a",
		searchConversationFixture("conv-a", "Mixed", "2026-01-02T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-01-02T10:00:00Z",
			HistoryContent{Type: "text", Text: "Grep the logs"}),
	)

	tests := []struct {
		name          string
		query         string
		caseSensitive bool
		wantMatches   int
	}{
		{name: "folded by default", query: "grep", wantMatches: 1},
		{name: "folded uppercase query", query: "GREP", wantMatches: 1},
		{name: "sensitive exact", query: "Grep", caseSensitive: true, wantMatches: 1},
		{name: "sensitive miss", query: "grep", caseSensitive: true, wantMatches: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := searchHistory(storage, test.query, SearchOptions{CaseSensitive: test.caseSensitive})
			if err != nil {
				t.Fatalf("searchHistory returned error: %v", err)
			}
			got := 0
			for _, result := range response.Results {
				got += len(result.Matches)
			}
			if got != test.wantMatches {
				t.Fatalf("match count = %d, want %d", got, test.wantMatches)
			}
		})
	}
}

func TestSearchHistoryRegex(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "conv-a",
		searchConversationFixture("conv-a", "Palette", "2026-01-02T10:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-01-02T10:00:00Z",
			HistoryContent{Type: "text", Text: "Pick a colour scheme"}),
	)

	response, err := searchHistory(storage, "colou?r", SearchOptions{Regex: true})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(response.Results) != 1 || len(response.Results[0].Matches) != 1 {
		t.Fatalf("results = %+v, want one match", response.Results)
	}
	if response.Results[0].Matches[0].Match != "colour" {
		t.Fatalf("match = %q, want %q", response.Results[0].Matches[0].Match, "colour")
	}

	// The same query as a literal must not match.
	literal, err := searchHistory(storage, "colou?r", SearchOptions{})
	if err != nil {
		t.Fatalf("literal searchHistory returned error: %v", err)
	}
	if len(literal.Results) != 0 {
		t.Fatalf("literal result count = %d, want 0", len(literal.Results))
	}

	if _, err := searchHistory(storage, "[", SearchOptions{Regex: true}); err == nil {
		t.Fatalf("invalid pattern returned nil error, want error")
	}
}

func TestSearchHistoryThinkingScope(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "conv-a",
		searchConversationFixture("conv-a", "Deep work", "2026-01-02T10:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-01-02T10:00:00Z",
			HistoryContent{Type: "thinking", Text: "pondering the grep problem"},
			HistoryContent{Type: "text", Text: "Here is a grep approach."}),
	)

	defaultResponse, err := searchHistory(storage, "grep", SearchOptions{})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(defaultResponse.Results) != 1 || len(defaultResponse.Results[0].Matches) != 1 {
		t.Fatalf("default matches = %+v, want visible text only", defaultResponse.Results)
	}

	withThinking, err := searchHistory(storage, "pondering", SearchOptions{IncludeThinking: true})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(withThinking.Results) != 1 || len(withThinking.Results[0].Matches) != 1 {
		t.Fatalf("thinking matches = %+v, want one", withThinking.Results)
	}

	withoutThinking, err := searchHistory(storage, "pondering", SearchOptions{})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(withoutThinking.Results) != 0 {
		t.Fatalf("thinking hidden by default: results = %+v", withoutThinking.Results)
	}
}

func TestSearchHistoryCapsMatchesPerTurn(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "conv-a",
		searchConversationFixture("conv-a", "Chatty", "2026-01-02T10:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-01-02T10:00:00Z",
			HistoryContent{Type: "text", Text: "grep one"},
			HistoryContent{Type: "text", Text: "grep two"},
			HistoryContent{Type: "text", Text: "grep three"},
			HistoryContent{Type: "text", Text: "grep four"}),
	)

	response, err := searchHistory(storage, "grep", SearchOptions{})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(response.Results) != 1 || len(response.Results[0].Matches) != maxSearchMatchesPerTurn {
		t.Fatalf("match count = %d, want %d", len(response.Results[0].Matches), maxSearchMatchesPerTurn)
	}
}

func TestSearchHistoryExclusions(t *testing.T) {
	storage := searchTestStorage(t)
	deletedTurn := searchTurnFixture("turn_000003", "assistant", "2026-01-03T10:00:10Z",
		HistoryContent{Type: "text", Text: "needle in deleted turn"})
	deletedTurn.DeletedAt = "2026-01-03T11:00:00Z"
	writeSearchConversation(t, storage, "conv-live",
		searchConversationFixture("conv-live", "Live", "2026-01-03T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-01-03T10:00:00Z",
			HistoryContent{Type: "text", Text: "needle here"}),
		searchTurnFixture("turn_000002", "assistant", "2026-01-03T10:00:05Z",
			HistoryContent{Type: "text", Text: "needle also here"}),
		deletedTurn)

	writeSearchConversation(t, storage, "conv-tomb",
		searchConversationFixture("conv-tomb", "Tombstoned", "2026-01-02T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-01-02T10:00:00Z",
			HistoryContent{Type: "text", Text: "needle in tombstone"}))
	if err := os.WriteFile(filepath.Join(storage.History, "conversations", "conv-tomb", "tombstone.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write tombstone: %v", err)
	}

	deletedConversation := searchConversationFixture("conv-del", "Deleted", "2026-01-01T10:00:00Z")
	deletedConversation.DeletedAt = "2026-01-01T11:00:00Z"
	writeSearchConversation(t, storage, "conv-del", deletedConversation,
		searchTurnFixture("turn_000001", "user", "2026-01-01T10:00:00Z",
			HistoryContent{Type: "text", Text: "needle in deleted conversation"}))

	response, err := searchHistory(storage, "needle", SearchOptions{})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("result count = %d, want 1 (only conv-live)", len(response.Results))
	}
	if response.Results[0].Conversation.ID != "conv-live" {
		t.Fatalf("conversation id = %q, want conv-live", response.Results[0].Conversation.ID)
	}
	if len(response.Results[0].Matches) != 2 {
		t.Fatalf("match count = %d, want 2 (deleted turn skipped)", len(response.Results[0].Matches))
	}
}

func TestSearchHistoryLimitAndTruncation(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "conv-a",
		searchConversationFixture("conv-a", "Needle A", "2026-01-01T10:00:00Z"))
	writeSearchConversation(t, storage, "conv-b",
		searchConversationFixture("conv-b", "Needle B", "2026-01-02T10:00:00Z"))
	writeSearchConversation(t, storage, "conv-c",
		searchConversationFixture("conv-c", "Needle C", "2026-01-03T10:00:00Z"))

	response, err := searchHistory(storage, "needle", SearchOptions{Limit: 2})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("result count = %d, want 2", len(response.Results))
	}
	if !response.Truncated {
		t.Fatalf("truncated = false, want true")
	}
	if response.Results[0].Conversation.ID != "conv-c" || response.Results[1].Conversation.ID != "conv-b" {
		t.Fatalf("results = %q,%q want conv-c,conv-b (newest first)",
			response.Results[0].Conversation.ID, response.Results[1].Conversation.ID)
	}

	unlimited, err := searchHistory(storage, "needle", SearchOptions{})
	if err != nil {
		t.Fatalf("searchHistory returned error: %v", err)
	}
	if len(unlimited.Results) != 3 || unlimited.Truncated {
		t.Fatalf("default limit results = %d truncated = %v, want 3/false", len(unlimited.Results), unlimited.Truncated)
	}
}

func TestSearchHistoryEmptyQueryAndMissingRoot(t *testing.T) {
	storage := searchTestStorage(t)
	response, err := searchHistory(storage, "   ", SearchOptions{})
	if err != nil {
		t.Fatalf("blank query returned error: %v", err)
	}
	if len(response.Results) != 0 || response.Results == nil {
		t.Fatalf("blank query results = %+v, want empty non-nil", response.Results)
	}

	missing := searchTestStorage(t)
	missing.History = filepath.Join(missing.History, "does-not-exist")
	response, err = searchHistory(missing, "anything", SearchOptions{})
	if err != nil {
		t.Fatalf("missing root returned error: %v", err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("missing root results = %d, want 0", len(response.Results))
	}
}

func TestSearchSnippet(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		start      int
		end        int
		wantBefore string
		wantMatch  string
		wantAfter  string
	}{
		{
			name:       "short text is shown whole",
			text:       "What is the capital of France?",
			start:      strings.Index("What is the capital of France?", "France"),
			end:        strings.Index("What is the capital of France?", "France") + len("France"),
			wantBefore: "What is the capital of ",
			wantMatch:  "France",
			wantAfter:  "?",
		},
		{
			name:       "window edges snap to whole words",
			text:       strings.Repeat("alpha ", 30) + "needle" + strings.Repeat(" beta", 30),
			start:      180,
			end:        186,
			wantBefore: "…alpha alpha alpha alpha alpha alpha alpha alpha ",
			wantMatch:  "needle",
			wantAfter:  " beta beta beta beta beta beta beta beta beta…",
		},
		{
			name:       "single-token context collapses to bare ellipses",
			text:       strings.Repeat("x", 120) + "needle" + strings.Repeat("y", 120),
			start:      120,
			end:        126,
			wantBefore: "…",
			wantMatch:  "needle",
			wantAfter:  "…",
		},
		{
			name:       "whitespace runs collapse but boundary spaces survive",
			text:       "hello   world\n\nneedle\ttab",
			start:      15,
			end:        21,
			wantBefore: "hello world ",
			wantMatch:  "needle",
			wantAfter:  " tab",
		},
		{
			name:       "no space added against punctuation",
			text:       "cartoon-character sheet",
			start:      8,
			end:        17,
			wantBefore: "cartoon-",
			wantMatch:  "character",
			wantAfter:  " sheet",
		},
		{
			name:       "match at text start has no leading space",
			text:       "needle in a haystack",
			start:      0,
			end:        6,
			wantBefore: "",
			wantMatch:  "needle",
			wantAfter:  " in a haystack",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before, match, after := searchSnippet(test.text, test.start, test.end)
			if before != test.wantBefore || match != test.wantMatch || after != test.wantAfter {
				t.Fatalf("snippet = %q|%q|%q, want %q|%q|%q",
					before, match, after, test.wantBefore, test.wantMatch, test.wantAfter)
			}
		})
	}
}

func TestIndexFold(t *testing.T) {
	tests := []struct {
		name      string
		haystack  string
		needle    string
		wantFound bool
		wantStart int
	}{
		{name: "ascii fold", haystack: "Hello World", needle: "wORLD", wantFound: true, wantStart: 6},
		{name: "no match", haystack: "Hello", needle: "grep", wantFound: false},
		{name: "multibyte context offsets", haystack: "π≈3.14 needle", needle: "NEEDLE", wantFound: true, wantStart: 10},
		{name: "multibyte needle", haystack: "aöb", needle: "Ö", wantFound: true, wantStart: 1},
		{name: "empty needle never matches", haystack: "anything", needle: "", wantFound: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end := indexFold(test.haystack, test.needle)
			if !test.wantFound {
				if start != -1 || end != -1 {
					t.Fatalf("indexFold = (%d,%d), want (-1,-1)", start, end)
				}
				return
			}
			if start != test.wantStart {
				t.Fatalf("start = %d, want %d", start, test.wantStart)
			}
			if end-start != len(test.needle) {
				t.Fatalf("match length = %d, want %d", end-start, len(test.needle))
			}
			if test.haystack[start:end] != test.haystack[test.wantStart:test.wantStart+len(test.needle)] {
				t.Fatalf("offsets %d:%d do not isolate the match", start, end)
			}
		})
	}
}
