package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAssetFile puts real bytes at an artifact's relative path inside the
// conversation directory so getConversation's hydration resolves it to a
// /atelier-artifact URL.
func writeAssetFile(t *testing.T, storage ConfigStorage, relDir, relPath, contents string) {
	t.Helper()
	absPath := filepath.Join(storage.History, "conversations", relDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(absPath), err)
	}
	if err := os.WriteFile(absPath, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", absPath, err)
	}
}

func TestListConversationAssetsDerivesAllKindsInOrder(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "2026-08-01/conv_assets1",
		searchConversationFixture("conv_assets1", "Assets", "2026-08-01T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-08-01T10:00:00Z",
			HistoryContent{Type: "text", Text: "here's a reference"},
			HistoryContent{Type: "image", ArtifactID: "img_aa11", Path: "artifacts/img_aa11.png", MimeType: "image/png"},
		),
		searchTurnFixture("turn_000002", "assistant", "2026-08-01T10:00:05Z",
			HistoryContent{Type: "text", Text: "done"},
			HistoryContent{Type: "image", ArtifactID: "img_bb22", Path: "artifacts/img_bb22.png", MimeType: "image/png", Width: 1024, Height: 768},
			HistoryContent{Type: "audio", ArtifactID: "aud_cc33", Path: "artifacts/aud_cc33.mp3", MimeType: "audio/mpeg"},
			HistoryContent{Type: "video", ArtifactID: "vid_dd44", Path: "artifacts/vid_dd44.mp4", MimeType: "video/mp4"},
			HistoryContent{Type: "thinking", Text: "internal"},
		),
	)
	for _, rel := range []string{"artifacts/img_aa11.png", "artifacts/img_bb22.png", "artifacts/aud_cc33.mp3", "artifacts/vid_dd44.mp4"} {
		writeAssetFile(t, storage, "2026-08-01/conv_assets1", rel, "bytes")
	}

	assets, err := listConversationAssets(storage, "conv_assets1")
	if err != nil {
		t.Fatalf("listConversationAssets: %v", err)
	}
	if len(assets) != 4 {
		t.Fatalf("asset count = %d, want 4 (text/thinking skipped): %+v", len(assets), assets)
	}

	want := []struct {
		id, kind, turnID, role string
	}{
		{"img_aa11", "image", "turn_000001", "user"},
		{"img_bb22", "image", "turn_000002", "assistant"},
		{"aud_cc33", "audio", "turn_000002", "assistant"},
		{"vid_dd44", "video", "turn_000002", "assistant"},
	}
	for i, w := range want {
		got := assets[i]
		if got.ID != w.id || got.Kind != w.kind || got.OriginTurnID != w.turnID || got.Role != w.role {
			t.Fatalf("asset[%d] = id:%s kind:%s turn:%s role:%s, want id:%s kind:%s turn:%s role:%s",
				i, got.ID, got.Kind, got.OriginTurnID, got.Role, w.id, w.kind, w.turnID, w.role)
		}
		if got.ConversationID != "conv_assets1" {
			t.Fatalf("asset[%d].ConversationID = %q, want conv_assets1", i, got.ConversationID)
		}
		if !strings.HasPrefix(got.URL, "/atelier-artifact/") {
			t.Fatalf("asset[%d].URL = %q, want a hydrated /atelier-artifact/ URL", i, got.URL)
		}
	}
	if assets[0].Width != 0 || assets[1].Width != 1024 || assets[1].Height != 768 {
		t.Fatalf("image dimensions not carried: [%d]=%dx%d [%d]=%dx%d",
			0, assets[0].Width, assets[0].Height, 1, assets[1].Width, assets[1].Height)
	}
}

func TestListConversationAssetsLegacyAndMissing(t *testing.T) {
	storage := searchTestStorage(t)
	// Deleted turns are filtered by getConversation and contribute nothing.
	deletedTurn := HistoryTurn{
		SchemaVersion: 1, ID: "turn_000003", Kind: "chat", Role: "assistant",
		CreatedAt: "2026-08-02T10:00:10Z", DeletedAt: "2026-08-02T11:00:00Z",
		Content: []HistoryContent{{Type: "image", ArtifactID: "img_dead", Path: "artifacts/img_dead.png"}},
	}
	writeSearchConversation(t, storage, "2026-08-02/conv_assets2",
		searchConversationFixture("conv_assets2", "Legacy", "2026-08-02T10:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-08-02T10:00:00Z",
			// Legacy positional ID — stays as-is, valid within the conversation.
			HistoryContent{Type: "image", ArtifactID: "turn_000001_img_000001", Path: "artifacts/turn_000001_img_000001.png", MimeType: "image/png"},
			// Pre-ArtifactID record — falls back to the path as its ID.
			HistoryContent{Type: "audio", Path: "artifacts/gone.wav", MimeType: "audio/wav"},
		),
		searchTurnFixture("turn_000002", "assistant", "2026-08-02T10:00:05Z",
			HistoryContent{Type: "image", ArtifactID: "img_ee55", Path: "artifacts/img_ee55.png", MimeType: "image/png"},
		),
		deletedTurn,
	)
	// Only the legacy image exists on disk; the path-ID audio and img_ee55 do
	// not — they must stay listed with an empty URL.
	writeAssetFile(t, storage, "2026-08-02/conv_assets2", "artifacts/turn_000001_img_000001.png", "legacy")

	assets, err := listConversationAssets(storage, "conv_assets2")
	if err != nil {
		t.Fatalf("listConversationAssets: %v", err)
	}
	if len(assets) != 3 {
		t.Fatalf("asset count = %d, want 3 (deleted turn excluded): %+v", len(assets), assets)
	}
	if assets[0].ID != "turn_000001_img_000001" || assets[0].URL == "" {
		t.Fatalf("legacy asset = id:%q url:%q, want positional id with hydrated URL", assets[0].ID, assets[0].URL)
	}
	if assets[1].ID != "artifacts/gone.wav" || assets[1].URL != "" {
		t.Fatalf("path-ID asset = id:%q url:%q, want path fallback with empty URL", assets[1].ID, assets[1].URL)
	}
	if assets[2].ID != "img_ee55" || assets[2].URL != "" {
		t.Fatalf("missing-file asset = id:%q url:%q, want id with empty URL", assets[2].ID, assets[2].URL)
	}
}

func TestListConversationAssetsUnknownConversation(t *testing.T) {
	storage := searchTestStorage(t)
	if _, err := listConversationAssets(storage, "conv_nope"); err == nil {
		t.Fatal("expected an error for an unknown conversation")
	}
}

// minimalMentionWAV is the smallest payload isAudioBytes accepts, so the audio
// artifact reader re-reads it as a data URL.
var minimalMentionWAV = []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x40\x1f\x00\x00\x40\x1f\x00\x00\x01\x00\x08\x00data\x00\x00\x00\x00")

func TestResolveReferencedAssetsKindsOrderAndUnknown(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "2026/08/conv_ref1",
		searchConversationFixture("conv_ref1", "Refs", "2026-08-03T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-08-03T10:00:00Z",
			HistoryContent{Type: "text", Text: "hi"},
		),
		searchTurnFixture("turn_000002", "assistant", "2026-08-03T10:00:05Z",
			HistoryContent{Type: "image", ArtifactID: "img_r1", Path: "artifacts/img_r1.png", MimeType: "image/png"},
			HistoryContent{Type: "audio", ArtifactID: "aud_r2", Path: "artifacts/aud_r2.wav", MimeType: "audio/wav"},
			// Pre-ID record: resolvable by its path only.
			HistoryContent{Type: "audio", Path: "artifacts/legacy_r4.wav", MimeType: "audio/wav"},
			HistoryContent{Type: "video", ArtifactID: "vid_r3", Path: "artifacts/vid_r3.mp4", MimeType: "video/mp4"},
		),
	)
	writeAssetFile(t, storage, "2026/08/conv_ref1", "artifacts/img_r1.png", string(minimalPNG))
	writeAssetFile(t, storage, "2026/08/conv_ref1", "artifacts/aud_r2.wav", string(minimalMentionWAV))
	writeAssetFile(t, storage, "2026/08/conv_ref1", "artifacts/legacy_r4.wav", string(minimalMentionWAV))
	writeAssetFile(t, storage, "2026/08/conv_ref1", "artifacts/vid_r3.mp4", string([]byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}))

	got := resolveReferencedAssets(storage, "conv_ref1", []string{"aud_r2", "img_r1", "nope", "artifacts/legacy_r4.wav", "", "vid_r3"})
	if len(got.audios) != 2 || len(got.images) != 1 || len(got.videos) != 1 {
		t.Fatalf("per-kind counts = img:%d aud:%d vid:%d, want img:1 aud:2 vid:1", len(got.images), len(got.audios), len(got.videos))
	}
	if !strings.HasPrefix(got.audios[0], "data:audio/wav") || !strings.HasPrefix(got.images[0], "data:image/") || !strings.HasPrefix(got.videos[0], "data:video/") {
		t.Fatalf("resolved media not data URLs: aud:%q img:%q vid:%q", got.audios[0], got.images[0], got.videos[0])
	}
	// Entries follow mention order with unknown and empty IDs skipped.
	wantIDs := []string{"aud_r2", "img_r1", "artifacts/legacy_r4.wav", "vid_r3"}
	if len(got.entries) != len(wantIDs) {
		t.Fatalf("entry count = %d, want %d: %+v", len(got.entries), len(wantIDs), got.entries)
	}
	for i, want := range wantIDs {
		key := got.entries[i].ArtifactID
		if key == "" {
			key = got.entries[i].Path
		}
		if key != want {
			t.Fatalf("entries[%d] = %q, want %q", i, key, want)
		}
	}
}

func TestResolveReferencedAssetsSkipsMissingAndEmpty(t *testing.T) {
	storage := searchTestStorage(t)
	writeSearchConversation(t, storage, "2026/08/conv_ref2",
		searchConversationFixture("conv_ref2", "Refs", "2026-08-03T11:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-08-03T11:00:00Z",
			// Listed in history but the file is gone: nothing usable to resolve.
			HistoryContent{Type: "image", ArtifactID: "img_gone", Path: "artifacts/img_gone.png", MimeType: "image/png"},
		),
	)

	got := resolveReferencedAssets(storage, "conv_ref2", []string{"img_gone"})
	if len(got.images) != 0 || len(got.entries) != 0 {
		t.Fatalf("missing-file mention resolved to img:%d entries:%d, want none", len(got.images), len(got.entries))
	}
	if other := resolveReferencedAssets(storage, "conv_ref2", nil); len(other.entries) != 0 {
		t.Fatal("no IDs must resolve nothing")
	}
	if other := resolveReferencedAssets(storage, "", []string{"img_gone"}); len(other.entries) != 0 {
		t.Fatal("empty conversation ID must resolve nothing")
	}
	if other := resolveReferencedAssets(storage, "conv_nope", []string{"img_gone"}); len(other.entries) != 0 {
		t.Fatal("unknown conversation must resolve nothing, not fail")
	}
}
