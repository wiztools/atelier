package main

// Tests for the asset alias registry (asset_aliases.go): set/clear round
// trips through both listings, global case-insensitive uniqueness,
// normalization, the corrupt-registry write guard (never clobber), the
// library fold's post-fold stamping (the UpdatedAt-keyed cache must stay
// alias-free), and the upload-delete prune that frees the name.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atelier/internal/mediasniff/mediasnifftest"
)

func aliasTestConversation(t *testing.T, storage ConfigStorage, id string, artifactIDs ...string) {
	t.Helper()
	contents := make([]HistoryContent, 0, len(artifactIDs))
	for _, artifactID := range artifactIDs {
		contents = append(contents, HistoryContent{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       "artifacts/" + artifactID + ".png",
			MimeType:   "image/png",
		})
	}
	writeSearchConversation(t, storage, "2026-08-01/"+id,
		searchConversationFixture(id, "Alias", "2026-08-01T10:00:00Z"),
		searchTurnFixture("turn_000001", "assistant", "2026-08-01T10:00:00Z", contents...),
	)
}

func readAliasRegistryFile(t *testing.T, storage ConfigStorage) assetAliasRegistry {
	t.Helper()
	var registry assetAliasRegistry
	path := filepath.Join(storage.Root, "asset_aliases.json")
	if err := readJSONFile(path, &registry); err != nil {
		t.Fatalf("read alias registry: %v", err)
	}
	return registry
}

func TestSetAssetAliasRoundTripThroughListing(t *testing.T) {
	storage := searchTestStorage(t)
	aliasTestConversation(t, storage, "conv_alias1", "img_aa11")

	if err := setAssetAlias(storage, "img_aa11", "  hero   shot "); err != nil {
		t.Fatalf("setAssetAlias: %v", err)
	}
	assets, err := listConversationAssets(storage, "conv_alias1")
	if err != nil {
		t.Fatalf("listConversationAssets: %v", err)
	}
	if len(assets) != 1 || assets[0].Alias != "hero shot" {
		t.Fatalf("listing alias = %+v, want \"hero shot\" (whitespace collapsed)", assets)
	}
	// The registry file lands at the storage root and carries the normalized
	// alias — the shape saveAssetAliasRegistry writes.
	registry := readAliasRegistryFile(t, storage)
	if registry.SchemaVersion != currentAssetAliasSchemaVersion {
		t.Fatalf("registry schemaVersion = %d, want %d", registry.SchemaVersion, currentAssetAliasSchemaVersion)
	}
	if registry.Aliases["img_aa11"] != "hero shot" {
		t.Fatalf("registry entry = %q, want \"hero shot\"", registry.Aliases["img_aa11"])
	}

	// Empty alias clears, through the same door.
	if err := setAssetAlias(storage, "img_aa11", "   "); err != nil {
		t.Fatalf("clearAssetAlias: %v", err)
	}
	assets, err = listConversationAssets(storage, "conv_alias1")
	if err != nil {
		t.Fatalf("listConversationAssets after clear: %v", err)
	}
	if len(assets) != 1 || assets[0].Alias != "" {
		t.Fatalf("listing after clear = %+v, want no alias", assets)
	}
	registry = readAliasRegistryFile(t, storage)
	if len(registry.Aliases) != 0 {
		t.Fatalf("registry after clear = %+v, want empty", registry.Aliases)
	}
}

func TestSetAssetAliasDuplicateRejectedCaseInsensitive(t *testing.T) {
	storage := searchTestStorage(t)
	aliasTestConversation(t, storage, "conv_alias2", "img_aa11", "img_bb22")

	if err := setAssetAlias(storage, "img_aa11", "hero shot"); err != nil {
		t.Fatalf("setAssetAlias: %v", err)
	}
	err := setAssetAlias(storage, "img_bb22", "HERO SHOT")
	if err == nil {
		t.Fatalf("case-variant duplicate accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "img_aa11") {
		t.Fatalf("duplicate error %q does not name the holding asset", err.Error())
	}
	// Overwriting the same asset's own alias is not a conflict.
	if err := setAssetAlias(storage, "img_aa11", "Hero Shot (v2)"); err != nil {
		t.Fatalf("self-overwrite rejected: %v", err)
	}
	// Clearing an unset alias is a no-op, not an error.
	if err := setAssetAlias(storage, "img_bb22", ""); err != nil {
		t.Fatalf("clear of unset alias errored: %v", err)
	}
	if err := setAssetAlias(storage, "", "orphan"); err == nil {
		t.Fatalf("empty asset id accepted")
	}
}

func TestNormalizeAssetAlias(t *testing.T) {
	long := strings.Repeat("x", 70)
	want := strings.Repeat("x", 64) + "..."
	cases := []struct{ in, want string }{
		{"  hero   shot  ", "hero shot"},
		{"\ttabbed\nname\t", "tabbed name"},
		{"", ""},
		{long, want},
	}
	for _, c := range cases {
		if got := normalizeAssetAlias(c.in); got != c.want {
			t.Fatalf("normalizeAssetAlias(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAssetAliasRegistryRefusesClobberOnCorruptFile(t *testing.T) {
	storage := searchTestStorage(t)
	aliasTestConversation(t, storage, "conv_alias3", "img_aa11")
	if err := setAssetAlias(storage, "img_aa11", "hero shot"); err != nil {
		t.Fatalf("setAssetAlias: %v", err)
	}
	path := filepath.Join(storage.Root, "asset_aliases.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatalf("corrupt registry: %v", err)
	}
	if err := setAssetAlias(storage, "img_aa11", "replacement"); err == nil {
		t.Fatalf("write over a corrupt registry accepted, want refusal")
	}
	// The listing path stays fail-soft: a corrupt registry costs the aliases,
	// never the assets.
	assets, err := listConversationAssets(storage, "conv_alias3")
	if err != nil {
		t.Fatalf("listConversationAssets with corrupt registry: %v", err)
	}
	if len(assets) != 1 || assets[0].Alias != "" {
		t.Fatalf("listing with corrupt registry = %+v, want the asset without alias", assets)
	}
}

func TestListLibraryAssetsCarriesFreshAliasesThroughCache(t *testing.T) {
	storage := libraryTestStorage(t)
	libraryID := mustCreateLibrary(t, storage)
	project, err := createProject(storage, libraryID, "Project")
	if err != nil {
		t.Fatalf("createProject returned error: %v", err)
	}
	createdAt := "2025-11-02T10:00:00Z"
	writeArchiveConversation(t, storage, libraryConversationFixture("conv_alias_fold", "Folding Chat", createdAt, project.ID), []HistoryTurn{
		searchTurnFixture("turn_000001", "assistant", createdAt,
			HistoryContent{Type: "image", ArtifactID: "img_aa11", Path: "artifacts/img_aa11.png", MimeType: "image/png"},
		),
	}, nil)

	if err := setAssetAlias(storage, "img_aa11", "first name"); err != nil {
		t.Fatalf("setAssetAlias: %v", err)
	}
	assets, err := listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets: %v", err)
	}
	if len(assets) != 1 || assets[0].ID != "img_aa11" || assets[0].Alias != "first name" {
		t.Fatalf("library fold alias = %+v, want img_aa11 as \"first name\"", assets)
	}

	// Rename without touching the conversation: the fold's UpdatedAt cache key
	// cannot see registry writes, so the stamp must re-read the registry every
	// call or this listing would carry the stale name.
	if err := setAssetAlias(storage, "img_aa11", "second name"); err != nil {
		t.Fatalf("renameAssetAlias: %v", err)
	}
	assets, err = listLibraryAssets(storage, libraryID)
	if err != nil {
		t.Fatalf("listLibraryAssets after rename: %v", err)
	}
	if len(assets) != 1 || assets[0].Alias != "second name" {
		t.Fatalf("library fold after rename = %+v, want \"second name\"", assets)
	}
}

func TestDeleteLibraryAssetPrunesAlias(t *testing.T) {
	storage := libraryTestStorage(t)
	config := libraryAssetTestConfig(t, storage)
	libraryID := mustCreateLibrary(t, storage)
	upload := mustUploadLibraryAsset(t, config, libraryID, "", "deck frame.png", "image",
		dataURLFor("image/png", mediasnifftest.Image(t, "png", 4, 5)))
	if err := setAssetAlias(storage, upload.ID, "deck frame"); err != nil {
		t.Fatalf("setAssetAlias: %v", err)
	}
	if err := deleteLibraryAsset(storage, upload.ID); err != nil {
		t.Fatalf("deleteLibraryAsset: %v", err)
	}
	// The prune frees the name: another asset can take the exact alias.
	aliasTestConversation(t, storage, "conv_alias4", "img_aa11")
	if err := setAssetAlias(storage, "img_aa11", "deck frame"); err != nil {
		t.Fatalf("freed alias refused: %v", err)
	}
	registry := readAliasRegistryFile(t, storage)
	if _, held := registry.Aliases[upload.ID]; held {
		t.Fatalf("deleted upload %s still holds an alias: %+v", upload.ID, registry.Aliases)
	}
}
