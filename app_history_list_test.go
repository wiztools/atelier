package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// seedListConversation writes a minimal conversation.json under the History
// tree and stamps its file mtime, so listConversations tests can control the
// UpdatedAt field and the on-disk mtime independently.
func seedListConversation(t *testing.T, historyDir, id, updatedAt string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(historyDir, "conversations", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	conv := HistoryConversation{
		SchemaVersion: 1,
		ID:            id,
		Kind:          "chat",
		Title:         "conv " + id,
		CreatedAt:     updatedAt,
		UpdatedAt:     updatedAt,
	}
	path := filepath.Join(dir, "conversation.json")
	if err := writeJSONFile(path, conv); err != nil {
		t.Fatalf("write conversation.json: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// seedDeletedListConversation writes a soft-deleted conversation: DeletedAt set
// plus the sibling tombstone.json that deleteConversation always writes.
func seedDeletedListConversation(t *testing.T, historyDir, id, updatedAt string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(historyDir, "conversations", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	conv := HistoryConversation{
		SchemaVersion: 1,
		ID:            id,
		Kind:          "chat",
		Title:         "conv " + id,
		CreatedAt:     updatedAt,
		UpdatedAt:     updatedAt,
		DeletedAt:     updatedAt,
	}
	path := filepath.Join(dir, "conversation.json")
	if err := writeJSONFile(path, conv); err != nil {
		t.Fatalf("write conversation.json: %v", err)
	}
	if err := writeJSONFile(filepath.Join(dir, "tombstone.json"), map[string]string{
		"conversationId": id,
		"deletedAt":      updatedAt,
	}); err != nil {
		t.Fatalf("write tombstone.json: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestListConversationsEmpty(t *testing.T) {
	storage := ConfigStorage{History: filepath.Join(t.TempDir(), "history")}
	got, err := listConversations(storage)
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 conversations, got %d", len(got))
	}
}

func TestListConversationsSortsByUpdatedAtDesc(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	seedListConversation(t, historyDir, "a", base.Add(1*time.Minute).Format(time.RFC3339), base.Add(1*time.Minute))
	seedListConversation(t, historyDir, "b", base.Add(3*time.Minute).Format(time.RFC3339), base.Add(3*time.Minute))
	seedListConversation(t, historyDir, "c", base.Add(2*time.Minute).Format(time.RFC3339), base.Add(2*time.Minute))

	got, err := listConversations(ConfigStorage{History: historyDir})
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	gotIDs := make([]string, 0, len(got))
	for _, s := range got {
		gotIDs = append(gotIDs, s.ID)
	}
	want := []string{"b", "c", "a"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("order: want %v, got %v", want, gotIDs)
	}
}

func TestListConversationsCapsAt100(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 105; i++ {
		ts := base.Add(time.Duration(-i) * time.Minute)
		id := fmt.Sprintf("conv-%03d", i)
		seedListConversation(t, historyDir, id, ts.Format(time.RFC3339), ts)
	}
	got, err := listConversations(ConfigStorage{History: historyDir})
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("want 100 (capped), got %d", len(got))
	}
	if got[0].ID != "conv-000" {
		t.Fatalf("newest first: want conv-000, got %s", got[0].ID)
	}
	if got[99].ID != "conv-099" {
		t.Fatalf("100th: want conv-099, got %s", got[99].ID)
	}
}

func TestListConversationsShortlistsBeyondOverfetch(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 160; i++ {
		ts := base.Add(time.Duration(-i) * time.Minute)
		id := fmt.Sprintf("conv-%03d", i)
		seedListConversation(t, historyDir, id, ts.Format(time.RFC3339), ts)
	}
	got, err := listConversations(ConfigStorage{History: historyDir})
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("want 100, got %d", len(got))
	}
	if got[0].ID != "conv-000" || got[99].ID != "conv-099" {
		t.Fatalf("want newest 100 conv-000..conv-099, got first=%s last=%s", got[0].ID, got[99].ID)
	}
}

func TestListConversationsOrdersByUpdatedAtNotMtime(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	// updatedAt order (desc): x, y, z. mtime order (desc): y, z, x — deliberately different.
	seedListConversation(t, historyDir, "x", base.Add(10*time.Minute).Format(time.RFC3339), base.Add(1*time.Minute))
	seedListConversation(t, historyDir, "y", base.Add(5*time.Minute).Format(time.RFC3339), base.Add(10*time.Minute))
	seedListConversation(t, historyDir, "z", base.Add(1*time.Minute).Format(time.RFC3339), base.Add(5*time.Minute))

	got, err := listConversations(ConfigStorage{History: historyDir})
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	gotIDs := make([]string, 0, len(got))
	for _, s := range got {
		gotIDs = append(gotIDs, s.ID)
	}
	want := []string{"x", "y", "z"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("final order must follow UpdatedAt not mtime: want %v, got %v", want, gotIDs)
	}
}

// seedDeletedNoTombstone writes a soft-deleted conversation.json (DeletedAt
// set) WITHOUT the sibling tombstone.json, exercising the post-parse
// DeletedAt re-check for legacy records.
func seedDeletedNoTombstone(t *testing.T, historyDir, id, updatedAt string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(historyDir, "conversations", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	conv := HistoryConversation{
		SchemaVersion: 1, ID: id, Kind: "chat", Title: "conv " + id,
		CreatedAt: updatedAt, UpdatedAt: updatedAt, DeletedAt: updatedAt,
	}
	path := filepath.Join(dir, "conversation.json")
	if err := writeJSONFile(path, conv); err != nil {
		t.Fatalf("write conversation.json: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestListConversationsExcludesDeletedWithoutTombstone(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	seedListConversation(t, historyDir, "keep", base.Add(1*time.Minute).Format(time.RFC3339), base.Add(1*time.Minute))
	seedDeletedNoTombstone(t, historyDir, "legacy-gone", base.Add(5*time.Minute).Format(time.RFC3339), base.Add(5*time.Minute))

	got, err := listConversations(ConfigStorage{History: historyDir})
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(got) != 1 || got[0].ID != "keep" {
		t.Fatalf("want [keep], got %+v", got)
	}
}

func TestListConversationsExcludesDeleted(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	base := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	seedListConversation(t, historyDir, "keep", base.Add(1*time.Minute).Format(time.RFC3339), base.Add(1*time.Minute))
	// Deleted conversation has the NEWEST mtime (delete bumps mtime) but must
	// still be excluded.
	seedDeletedListConversation(t, historyDir, "gone", base.Add(5*time.Minute).Format(time.RFC3339), base.Add(5*time.Minute))

	got, err := listConversations(ConfigStorage{History: historyDir})
	if err != nil {
		t.Fatalf("listConversations: %v", err)
	}
	if len(got) != 1 || got[0].ID != "keep" {
		t.Fatalf("want [keep], got %+v", got)
	}
}
