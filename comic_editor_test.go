package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// comicPNG builds a real 4×3 PNG — bytes the measurement sniff reads without
// any CLI.
func comicPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// comicConversationFixture writes conv_comic with a measurable image, an
// image whose bytes defeat the sniff, a video (the wrong kind for a comic),
// and one saved comic artifact.
func comicConversationFixture(t *testing.T, storage ConfigStorage) string {
	t.Helper()
	artifacts := filepath.Join(storage.History, "conversations", "2026/10/conv_comic", "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "img_a.png"), comicPNG(t), 0644); err != nil {
		t.Fatalf("write img_a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "img_broken.png"), []byte("not an image at all"), 0644); err != nil {
		t.Fatalf("write img_broken: %v", err)
	}
	content, err := writeComicArtifact(artifacts, validComicDoc())
	if err != nil {
		t.Fatalf("writeComicArtifact: %v", err)
	}
	writeSearchConversation(t, storage, "2026/10/conv_comic",
		searchConversationFixture("conv_comic", "Comics", "2026-10-01T10:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_a", Path: "artifacts/img_a.png", MimeType: "image/png"},
			HistoryContent{Type: "image", ArtifactID: "img_broken", Path: "artifacts/img_broken.png", MimeType: "image/png"},
			HistoryContent{Type: "video", ArtifactID: "vid_a", Path: "artifacts/vid_a.mp4", MimeType: "video/mp4"}),
		searchTurnFixture("turn_000002", "assistant", "2026-10-01T10:05:00Z", content),
	)
	return artifacts
}

func TestComicEditorLifecycle(t *testing.T) {
	config := editTestHome(t)
	comicConversationFixture(t, config.Storage)
	app := NewApp()

	// Seeding measures what it can and skips the rest with warnings — the
	// picker is right there to add a refusable id by hand.
	seed, err := app.SeedComic(SeedComicRequest{ConversationID: "conv_comic", Name: "Issue #1", ArtifactIDs: []string{"img_a", "img_broken", "vid_a", "img_missing"}})
	if err != nil {
		t.Fatalf("SeedComic returned error: %v", err)
	}
	if len(seed.Doc.Unplaced) != 1 || seed.Doc.Unplaced[0].ArtifactID != "img_a" {
		t.Fatalf("unplaced = %+v, want only img_a", seed.Doc.Unplaced)
	}
	if seed.Doc.Unplaced[0].Width != 4 || seed.Doc.Unplaced[0].Height != 3 {
		t.Fatalf("img_a ref = %+v, want the sniffed 4×3 size", seed.Doc.Unplaced[0])
	}
	if len(seed.Warnings) != 3 {
		t.Fatalf("warnings = %v, want one per skipped id", seed.Warnings)
	}
	for _, want := range []string{"img_broken", "vid_a", "img_missing"} {
		found := false
		for _, warning := range seed.Warnings {
			if strings.Contains(warning, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("warnings %v miss %q", seed.Warnings, want)
		}
	}
	if len(seed.Doc.Pages) != 1 || seed.Doc.Pages[0].Aspect != comicDefaultAspect {
		t.Fatalf("seeded pages = %+v, want one blank page in the default format", seed.Doc.Pages)
	}

	// A video id straight through the picker's add refuses — the comic takes
	// images.
	if _, err := app.ResolveComicMedia("conv_comic", "vid_a"); err == nil || !strings.Contains(err.Error(), "comic takes images") {
		t.Fatalf("ResolveComicMedia(video) = %v", err)
	}

	// Saving lands a fresh cbc_ artifact plus an assistant turn; reopening
	// resolves it with no missing refs.
	seed.Doc.Pages[0].Panels = []ComicPanel{{ID: "pnl1", Points: comicRect(0.05, 0.05, 0.9, 0.4), Image: &ComicPanelImage{Ref: seed.Doc.Unplaced[0]}}}
	saved, err := app.SaveComicEdit(SaveComicEditRequest{ConversationID: "conv_comic", Doc: seed.Doc})
	if err != nil {
		t.Fatalf("SaveComicEdit returned error: %v", err)
	}
	if !strings.HasPrefix(saved.ArtifactID, "cbc_") || !strings.Contains(saved.Summary, "1 page · 1 panel") {
		t.Fatalf("saved = %+v", saved)
	}
	if !strings.Contains(saved.URL, "cbc_") {
		t.Fatalf("url = %q", saved.URL)
	}
	reopened, err := app.ResolveComicEditSource("conv_comic", saved.ArtifactID)
	if err != nil {
		t.Fatalf("ResolveComicEditSource returned error: %v", err)
	}
	if len(reopened.Doc.Pages[0].Panels) != 1 || reopened.Doc.Pages[0].Panels[0].Image == nil || reopened.Doc.Pages[0].Panels[0].Image.Ref.ArtifactID != "img_a" {
		t.Fatalf("reopened doc = %+v", reopened.Doc)
	}
	if len(reopened.MissingRefs) != 0 {
		t.Fatalf("reopened missing refs = %v", reopened.MissingRefs)
	}
	if reopened.ConversationTitle != "Comics" {
		t.Fatalf("conversation title = %q", reopened.ConversationTitle)
	}

	// An unknown artifact id, and an id that names another kind, refuse.
	if _, err := app.ResolveComicEditSource("conv_comic", "cbc_absent"); err == nil || !strings.Contains(err.Error(), "no comic artifact") {
		t.Fatalf("ResolveComicEditSource(unknown) = %v", err)
	}

	// A panel whose image file is gone reopens flagged (identify-only).
	if err := os.Remove(filepath.Join(config.Storage.History, "conversations", "2026/10/conv_comic", "artifacts", "img_a.png")); err != nil {
		t.Fatalf("remove img_a: %v", err)
	}
	flagged, err := app.ResolveComicEditSource("conv_comic", saved.ArtifactID)
	if err != nil {
		t.Fatalf("ResolveComicEditSource after remove returned error: %v", err)
	}
	if len(flagged.MissingRefs) != 1 || flagged.MissingRefs[0] != "img_a" {
		t.Fatalf("missing refs = %v, want img_a", flagged.MissingRefs)
	}
}

func TestNewestEditorArtifactComicKind(t *testing.T) {
	storage := libraryTestStorage(t)
	comicConversationFixture(t, storage)

	// The comic kind resolves the newest comic artifact.
	kind, artifactID, err := newestEditorArtifact(storage, "conv_comic", "comic")
	if err != nil {
		t.Fatalf("newestEditorArtifact(comic) returned error: %v", err)
	}
	if kind != "comic" || !strings.HasPrefix(artifactID, "cbc_") {
		t.Fatalf("got %s/%s, want comic/cbc_*", kind, artifactID)
	}

	// Kindless stays visual-media-only — a saved comic never hijacks a bare
	// "open the editor" ask (it resolves the fixture's newest image).
	kind, artifactID, err = newestEditorArtifact(storage, "conv_comic", "")
	if err != nil {
		t.Fatalf("kindless open returned error: %v", err)
	}
	if kind != "image" || artifactID != "img_a" {
		t.Fatalf("kindless got %s/%s, want image/img_a", kind, artifactID)
	}
}

func TestComicSeedIDs(t *testing.T) {
	storage := libraryTestStorage(t)

	// The tray seeds from the WHOLE conversation: the newest user turn's
	// images lead in content order (the ask's explicit references), then the
	// rest of the conversation walks backward newest-artifact-first — a
	// generate_image output lands on the assistant turn, not the ask, and the
	// comic-open ask itself usually carries nothing. Missing files skip.
	artifacts := filepath.Join(storage.History, "conversations", "2026/10/conv_seed", "artifacts")
	if err := os.MkdirAll(artifacts, 0755); err != nil {
		t.Fatalf("MkdirAll artifacts: %v", err)
	}
	for _, id := range []string{"img_att1", "img_att2", "img_gen_old", "img_gen_new"} {
		if err := os.WriteFile(filepath.Join(artifacts, id+".png"), []byte("bytes"), 0644); err != nil {
			t.Fatalf("write %s: %v", id, err)
		}
	}
	writeSearchConversation(t, storage, "2026/10/conv_seed",
		searchConversationFixture("conv_seed", "Seeds", "2026-10-01T12:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_gen_old", Path: "artifacts/img_gen_old.png", MimeType: "image/png"}),
		searchTurnFixture("turn_000002", "user", "2026-10-01T10:30:00Z",
			HistoryContent{Type: "text", Text: "make a character sheet"}),
		searchTurnFixture("turn_000003", "assistant", "2026-10-01T11:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_gen_new", Path: "artifacts/img_gen_new.png", MimeType: "image/png"},
			HistoryContent{Type: "image", ArtifactID: "img_gone", Path: "artifacts/img_gone.png", MimeType: "image/png"}),
		searchTurnFixture("turn_000004", "user", "2026-10-01T12:00:00Z",
			HistoryContent{Type: "image", ArtifactID: "img_att1", Path: "artifacts/img_att1.png", MimeType: "image/png"},
			HistoryContent{Type: "video", ArtifactID: "vid_1", Path: "artifacts/vid_1.mp4", MimeType: "video/mp4"},
			HistoryContent{Type: "image", ArtifactID: "img_att2", Path: "artifacts/img_att2.png", MimeType: "image/png"}),
	)
	ids, err := comicSeedIDs(storage, "conv_seed")
	if err != nil {
		t.Fatalf("comicSeedIDs returned error: %v", err)
	}
	want := []string{"img_att1", "img_att2", "img_gen_new", "img_gen_old"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}

	// The tray's ceiling caps the walk — the newest images win.
	bulkArtifacts := filepath.Join(storage.History, "conversations", "2026/10/conv_seed_bulk", "artifacts")
	if err := os.MkdirAll(bulkArtifacts, 0755); err != nil {
		t.Fatalf("MkdirAll bulk artifacts: %v", err)
	}
	for i := 0; i < 55; i++ {
		id := fmt.Sprintf("img_bulk_%02d", i)
		if err := os.WriteFile(filepath.Join(bulkArtifacts, id+".png"), []byte("bytes"), 0644); err != nil {
			t.Fatalf("write %s: %v", id, err)
		}
	}
	bulk := []HistoryContent{}
	for i := 0; i < 55; i++ {
		bulk = append(bulk, HistoryContent{Type: "image", ArtifactID: fmt.Sprintf("img_bulk_%02d", i), Path: fmt.Sprintf("artifacts/img_bulk_%02d.png", i), MimeType: "image/png"})
	}
	writeSearchConversation(t, storage, "2026/10/conv_seed_bulk",
		searchConversationFixture("conv_seed_bulk", "Bulk", "2026-10-01T12:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-10-01T10:00:00Z", bulk...),
	)
	ids, err = comicSeedIDs(storage, "conv_seed_bulk")
	if err != nil {
		t.Fatalf("comicSeedIDs(bulk) returned error: %v", err)
	}
	if len(ids) != comicMaxUnplacedImages {
		t.Fatalf("bulk ids = %d, want the %d ceiling", len(ids), comicMaxUnplacedImages)
	}
	// Within a turn, content order IS reference order (the walk reverses
	// turns, not entries), so the cap trims the candidate tail.
	if ids[0] != "img_bulk_00" || ids[49] != "img_bulk_49" {
		t.Fatalf("bulk ids[0..49] = %q..%q, want content order 00..49", ids[0], ids[49])
	}

	// A conversation with no images at all still opens unseeded.
	writeSearchConversation(t, storage, "2026/10/conv_empty",
		searchConversationFixture("conv_empty", "Empty", "2026-10-01T12:00:00Z"),
		searchTurnFixture("turn_000001", "user", "2026-10-01T10:00:00Z",
			HistoryContent{Type: "text", Text: "hello"}),
	)
	ids, err = comicSeedIDs(storage, "conv_empty")
	if err != nil {
		t.Fatalf("comicSeedIDs(empty) returned error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty on an image-less conversation", ids)
	}
}
