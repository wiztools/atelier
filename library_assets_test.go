package main

// Tests for the uploaded library assets (library_assets.go): per-kind upload
// validation and persistence, staged-upload adoption, source-conversation
// provenance, the library fold's record branch, @-mention resolution against
// the records, and per-asset delete.

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

func libraryAssetTestConfig(t *testing.T, storage ConfigStorage) AppConfig {
	t.Helper()
	config := defaultAppConfig()
	config.Storage = storage
	return config
}

func dataURLFor(mimeType string, data []byte) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func mustUploadLibraryAsset(t *testing.T, config AppConfig, libraryID, conversationID, name, kind, payload string) ConversationAsset {
	t.Helper()
	asset, err := uploadLibraryAsset(config, libraryID, conversationID, name, kind, payload)
	if err != nil {
		t.Fatalf("uploadLibraryAsset(%s) returned error: %v", kind, err)
	}
	return asset
}

func TestUploadLibraryAssetKinds(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)

	image := mediasnifftest.Image(t, "png", 2, 3)
	asset := mustUploadLibraryAsset(t, config, libraryID, "", "hero shot.png", "image", dataURLFor("image/png", image))
	if !strings.HasPrefix(asset.ID, "ast_") {
		t.Fatalf("upload id = %q, want the ast_ prefix", asset.ID)
	}
	if asset.Kind != "image" || asset.Role != "upload" || asset.Name != "hero shot.png" {
		t.Fatalf("uploaded image asset = %+v", asset)
	}
	if asset.MimeType != "image/png" {
		t.Fatalf("uploaded image mime = %q", asset.MimeType)
	}
	if asset.Width != 2 || asset.Height != 3 {
		t.Fatalf("uploaded image dims = %dx%d, want 2x3 (sniffed from the bytes)", asset.Width, asset.Height)
	}
	if asset.URL == "" || !strings.Contains(asset.URL, filepath.ToSlash(libraryAssetsDir(storage, libraryID))) {
		t.Fatalf("uploaded image url = %q, want an artifact URL into the library's assets dir", asset.URL)
	}

	audio := mustUploadLibraryAsset(t, config, libraryID, "", "tone.wav", "audio", dataURLFor("audio/wav", mediasnifftest.WAV(8000)))
	if audio.Kind != "audio" || audio.MimeType != "audio/wav" || !strings.HasSuffix(audio.URL, ".wav") {
		t.Fatalf("uploaded audio asset = %+v", audio)
	}

	video := mustUploadLibraryAsset(t, config, libraryID, "", "clip.mp4", "video", dataURLFor("video/mp4", mediasnifftest.MP4WithDuration(0, 1000, 2000)))
	if video.Kind != "video" || video.MimeType != "video/mp4" || !strings.HasSuffix(video.URL, ".mp4") {
		t.Fatalf("uploaded video asset = %+v", video)
	}

	// The extension follows the CONTENT, not the declared mime — an SRT
	// payload sent under a vtt header lands as .srt (writeSubtitleAttachment's
	// contract).
	srt := "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	subtitle := mustUploadLibraryAsset(t, config, libraryID, "", "caps.srt", "subtitle", dataURLFor("text/vtt", []byte(srt)))
	if subtitle.Kind != "transcript" || subtitle.MimeType != "application/x-subrip" || !strings.HasSuffix(subtitle.URL, ".srt") {
		t.Fatalf("uploaded srt asset = %+v", subtitle)
	}

	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n"
	vttAsset := mustUploadLibraryAsset(t, config, libraryID, "", "caps.vtt", "subtitle", dataURLFor("text/vtt", []byte(vtt)))
	if vttAsset.MimeType != "text/vtt" || !strings.HasSuffix(vttAsset.URL, ".vtt") {
		t.Fatalf("uploaded vtt asset = %+v", vttAsset)
	}

	doc := mustUploadLibraryAsset(t, config, libraryID, "", "notes.markdown", "markdown", dataURLFor("text/markdown", []byte("# Notes\n\nBody text.\n")))
	if doc.Kind != "markdown" || doc.MimeType != "text/markdown" || !strings.HasSuffix(doc.URL, ".md") {
		t.Fatalf("uploaded markdown asset = %+v", doc)
	}
	screenplay := mustUploadLibraryAsset(t, config, libraryID, "", "scene.fountain", "markdown", dataURLFor("text/x-fountain", []byte("INT. ROOM - DAY\n\nAction.\n")))
	if !strings.HasSuffix(screenplay.URL, ".fountain") || screenplay.MimeType != "text/x-fountain" {
		t.Fatalf("uploaded fountain asset = %+v", screenplay)
	}

	records, err := listLibraryAssetRecords(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssetRecords returned error: %v", err)
	}
	if len(records) != 7 {
		t.Fatalf("records = %d, want 7", len(records))
	}
	for _, record := range records {
		if record.LibraryID != libraryID || record.SchemaVersion != currentLibraryAssetSchemaVersion {
			t.Fatalf("record = %+v", record)
		}
	}
}

func TestUploadLibraryAssetRejections(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)

	if _, err := uploadLibraryAsset(config, "lib_unknown", "", "x.png", "image", dataURLFor("image/png", mediasnifftest.Image(t, "png", 1, 1))); err == nil {
		t.Fatalf("unknown library accepted")
	}
	if _, err := uploadLibraryAsset(config, libraryID, "", "junk.png", "image", dataURLFor("image/png", []byte("not an image"))); err == nil {
		t.Fatalf("non-image payload accepted")
	}
	if _, err := uploadLibraryAsset(config, libraryID, "", "junk.wav", "audio", dataURLFor("audio/wav", []byte("not audio"))); err == nil {
		t.Fatalf("non-audio payload accepted")
	}
	if _, err := uploadLibraryAsset(config, libraryID, "", "junk.mp4", "video", dataURLFor("video/mp4", []byte("not video"))); err == nil {
		t.Fatalf("non-video payload accepted")
	}
	if _, err := uploadLibraryAsset(config, libraryID, "", "junk.vtt", "subtitle", dataURLFor("text/vtt", []byte("no cues here"))); err == nil {
		t.Fatalf("cue-less subtitle accepted")
	}
	if _, err := uploadLibraryAsset(config, libraryID, "", "junk.md", "markdown", dataURLFor("text/markdown", []byte("   "))); err == nil {
		t.Fatalf("empty markdown accepted")
	}
	if _, err := uploadLibraryAsset(config, libraryID, "", "junk", "composition", dataURLFor("application/json", []byte("{}"))); err == nil {
		t.Fatalf("unsupported kind accepted")
	}
	if _, err := uploadLibraryAsset(config, libraryID, "", "", "image", ""); err == nil {
		t.Fatalf("empty payload accepted")
	}
}

func TestUploadLibraryAssetStaged(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)

	app := &App{}
	video := append([]byte{0x00, 0x00, 0x00, 0x18}, append([]byte("ftypmp42"), make([]byte, 64)...)...)
	uploadID, err := app.beginMediaUpload(storage, "holiday.mov", "video")
	if err != nil {
		t.Fatalf("beginMediaUpload returned error: %v", err)
	}
	if err := app.AppendMediaUploadChunk(uploadID, base64.StdEncoding.EncodeToString(video)); err != nil {
		t.Fatalf("AppendMediaUploadChunk returned error: %v", err)
	}
	staged, err := app.completeMediaUpload(config, uploadID)
	if err != nil {
		t.Fatalf("completeMediaUpload returned error: %v", err)
	}

	// The staged /atelier-artifact URL is the payload — linked, not decoded.
	asset := mustUploadLibraryAsset(t, config, libraryID, "", "holiday.mov", "video", staged.URL)
	if !strings.HasSuffix(asset.URL, ".mov") {
		t.Fatalf("staged upload kept extension = %q, want .mov", asset.URL)
	}
	if asset.MimeType != "video/quicktime" {
		t.Fatalf("staged upload mime = %q", asset.MimeType)
	}
	entries, err := os.ReadDir(libraryAssetsDir(storage, libraryID))
	if err != nil {
		t.Fatalf("read assets dir: %v", err)
	}
	mediaCount := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			mediaCount++
		}
	}
	if mediaCount != 1 {
		t.Fatalf("assets dir holds %d media files, want 1", mediaCount)
	}
	// The staged source survives adoption (link, not move) — the sweep
	// reclaims it later.
	if _, err := os.Stat(staged.Path); err != nil {
		t.Fatalf("staged source missing after adoption: %v", err)
	}
}

func TestUploadLibraryAssetProvenance(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Project")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	writeArchiveConversation(t, storage, libraryConversationFixture("conv_src", "Source Chat", "2025-11-02T10:00:00Z", project.ID), nil, nil)
	// A conversation that belongs to no library must not stamp provenance.
	writeArchiveConversation(t, storage, searchConversationFixture("conv_loose", "Loose Chat", "2025-11-02T10:00:00Z"), nil, nil)

	payload := dataURLFor("image/png", mediasnifftest.Image(t, "png", 1, 1))
	linked := mustUploadLibraryAsset(t, config, libraryID, "conv_src", "from-chat.png", "image", payload)
	if linked.ConversationID != "conv_src" || linked.ConversationTitle != "Source Chat" {
		t.Fatalf("linked upload = %+v", linked)
	}

	loose := mustUploadLibraryAsset(t, config, libraryID, "conv_loose", "loose.png", "image", payload)
	if loose.ConversationID != "" || loose.ConversationTitle != "" {
		t.Fatalf("out-of-library provenance stamped: %+v", loose)
	}

	unknown := mustUploadLibraryAsset(t, config, libraryID, "conv_missing", "gone.png", "image", payload)
	if unknown.ConversationID != "" {
		t.Fatalf("unknown provenance stamped: %+v", unknown)
	}

	records, err := listLibraryAssetRecords(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssetRecords returned error: %v", err)
	}
	stamped := 0
	for _, record := range records {
		if record.SourceConversationID != "" {
			stamped++
			if record.SourceConversationTitle != "Source Chat" {
				t.Fatalf("provenance title = %q", record.SourceConversationTitle)
			}
		}
	}
	if stamped != 1 {
		t.Fatalf("stamped records = %d, want 1", stamped)
	}
}

func TestListLibraryAssetsIncludesUploads(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Project")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	createdAt := "2025-11-02T10:00:00Z"
	writeArchiveConversation(t, storage, libraryConversationFixture("conv_aaa", "Chat", createdAt, project.ID), []HistoryTurn{
		searchTurnFixture("turn_000001", "assistant", createdAt,
			HistoryContent{Type: "image", Path: "artifacts/img_derived.png", MimeType: "image/png", ArtifactID: "img_derived"},
		),
	}, map[string][]byte{"img_derived.png": []byte("png-bytes")})

	upload := mustUploadLibraryAsset(t, config, libraryID, "conv_aaa", "uploaded.png", "image",
		dataURLFor("image/png", mediasnifftest.Image(t, "png", 4, 5)))

	assets, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("fold = %d assets, want 2", len(assets))
	}
	var uploaded *ConversationAsset
	for i := range assets {
		if assets[i].Role == "upload" {
			uploaded = &assets[i]
		}
	}
	if uploaded == nil {
		t.Fatalf("fold missing the upload: %+v", assets)
	}
	if uploaded.ID != upload.ID || uploaded.Name != "uploaded.png" || uploaded.ConversationID != "conv_aaa" || uploaded.ConversationTitle != "Chat" {
		t.Fatalf("folded upload = %+v", uploaded)
	}
	if uploaded.OriginTurnID != "" {
		t.Fatalf("folded upload carries origin turn %q", uploaded.OriginTurnID)
	}

	// The source link is member-gated: deleting the conversation drops the
	// link but keeps the title snapshot and the asset itself.
	if err := deleteConversation(storage, "conv_aaa"); err != nil {
		t.Fatalf("deleteConversation returned error: %v", err)
	}
	invalidateConversationAssetCache([]string{findConversationDirForTest(t, storage, "conv_aaa")})
	assets, err = listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets after delete returned error: %v", err)
	}
	uploaded = nil
	for i := range assets {
		if assets[i].Role == "upload" {
			uploaded = &assets[i]
		}
	}
	if uploaded == nil {
		t.Fatalf("upload vanished from the fold after its source chat was deleted: %+v", assets)
	}
	if uploaded.ConversationID != "" || uploaded.ConversationTitle != "Chat" {
		t.Fatalf("folded upload after source delete = %+v", uploaded)
	}
}

// findConversationDirForTest locates a conversation directory for cache
// invalidation in tests — the fold's cache keys on conversation ID.
func findConversationDirForTest(t *testing.T, storage ConfigStorage, conversationID string) string {
	t.Helper()
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		t.Fatalf("findConversationPath(%s) returned error: %v", conversationID, err)
	}
	return filepath.Dir(path)
}

// An @-mention persists the upload's ast_ id on the mentioning turn, so the
// derived fold produces an entry with the same id and role "user". The record
// must win that dedupe: the asset keeps its upload identity (role, name
// snapshot, the panel's delete affordance), not the mentioning turn's.
func TestListLibraryAssetsMentionKeepsUploadIdentity(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Project")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	upload := mustUploadLibraryAsset(t, config, libraryID, "", "uploaded.png", "image",
		dataURLFor("image/png", mediasnifftest.Image(t, "png", 4, 5)))

	// A member conversation @-mentioned the upload: the persisted entry
	// carries the ast_ id and the absolute media path (the cross-conversation
	// mention shape), exactly as resolveReferencedAssets writes it.
	createdAt := "2025-11-02T10:00:00Z"
	writeArchiveConversation(t, storage, libraryConversationFixture("conv_mention", "Mentioning Chat", createdAt, project.ID), []HistoryTurn{
		searchTurnFixture("turn_000001", "user", createdAt,
			HistoryContent{
				Type:       "image",
				Path:       filepath.Join(libraryAssetsDir(storage, libraryID), upload.ID+".png"),
				MimeType:   "image/png",
				ArtifactID: upload.ID,
			},
		),
	}, nil)

	assets, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets returned error: %v", err)
	}
	var folded *ConversationAsset
	matches := 0
	for i := range assets {
		if assets[i].ID == upload.ID {
			matches++
			folded = &assets[i]
		}
	}
	if matches != 1 || folded == nil {
		t.Fatalf("fold holds %d entries for %s, want 1: %+v", matches, upload.ID, assets)
	}
	if folded.Role != "upload" || folded.Name != "uploaded.png" {
		t.Fatalf("mention demoted the upload: %+v", folded)
	}
}

func TestDeleteLibraryAsset(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)

	asset := mustUploadLibraryAsset(t, config, libraryID, "", "clip.mp4", "video", dataURLFor("video/mp4", mediasnifftest.MP4WithDuration(0, 1000, 2000)))
	mediaPath := filepath.Join(libraryAssetsDir(storage, libraryID), "x.mp4")
	entries, err := os.ReadDir(libraryAssetsDir(storage, libraryID))
	if err != nil {
		t.Fatalf("read assets dir: %v", err)
	}
	if len(entries) != 2 { // record + media (poster generation is a no-op with binaries pinned off)
		t.Fatalf("assets dir holds %d files, want 2", len(entries))
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".mp4") {
			mediaPath = filepath.Join(libraryAssetsDir(storage, libraryID), entry.Name())
		}
	}

	if err := deleteLibraryAsset(storage, asset.ID); err != nil {
		t.Fatalf("deleteLibraryAsset returned error: %v", err)
	}
	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Fatalf("media survived delete: %v", err)
	}
	remaining, err := listLibraryAssetRecords(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssetRecords returned error: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("record survived delete: %+v", remaining)
	}
	if err := deleteLibraryAsset(storage, asset.ID); err == nil {
		t.Fatalf("second delete accepted")
	}
	if err := deleteLibraryAsset(storage, "img_notanupload"); err == nil {
		t.Fatalf("non-upload id accepted")
	}
	if err := deleteLibraryAsset(storage, ""); err == nil {
		t.Fatalf("empty id accepted")
	}

	// A missing media file must not block the record's removal.
	doc := mustUploadLibraryAsset(t, config, libraryID, "", "notes.md", "markdown", dataURLFor("text/markdown", []byte("# Notes\n")))
	entries, err = os.ReadDir(libraryAssetsDir(storage, libraryID))
	if err != nil {
		t.Fatalf("read assets dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".md") {
			if err := os.Remove(filepath.Join(libraryAssetsDir(storage, libraryID), entry.Name())); err != nil {
				t.Fatalf("remove media: %v", err)
			}
		}
	}
	if err := deleteLibraryAsset(storage, doc.ID); err != nil {
		t.Fatalf("delete with missing media returned error: %v", err)
	}
}

func TestLibraryAssetMentionResolution(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Project")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}

	image := mediasnifftest.Image(t, "png", 2, 3)
	asset := mustUploadLibraryAsset(t, config, libraryID, "", "hero.png", "image", dataURLFor("image/png", image))

	// Resolution rides the library widening (empty conversation, project
	// fallback): the record resolves to a data URL for the tool slots and a
	// persistable entry with its ast_ id and an absolute path.
	media := resolveReferencedAssets(storage, "", project.ID, []string{asset.ID, "ast_missing"})
	if len(media.images) != 1 {
		t.Fatalf("resolved images = %d, want 1", len(media.images))
	}
	if !strings.HasPrefix(media.images[0], "data:image/png;base64,") {
		t.Fatalf("resolved image payload = %.40q", media.images[0])
	}
	if len(media.entries) != 1 {
		t.Fatalf("resolved entries = %d, want 1", len(media.entries))
	}
	entry := media.entries[0]
	if entry.ArtifactID != asset.ID || entry.Type != "image" {
		t.Fatalf("resolved entry = %+v", entry)
	}
	if !filepath.IsAbs(filepath.FromSlash(entry.Path)) {
		t.Fatalf("resolved entry path = %q, want absolute", entry.Path)
	}
	if !strings.Contains(entry.Path, filepath.ToSlash(libraryAssetsDir(storage, libraryID))) {
		t.Fatalf("resolved entry path = %q, want it inside the library's assets dir", entry.Path)
	}
}
