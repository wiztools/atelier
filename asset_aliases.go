package main

// asset_aliases.go — user-set unique aliases for conversation assets. Most
// assets are derived (assets.go): an asset exists because a HistoryContent
// entry on some turn points at a file, so there is no record to hang a name
// on. Aliases therefore live in ONE registry file keyed by asset id, beside
// the config at the storage root — one read stamps every listing, and global
// uniqueness is a single map scan. The assets panel gains the search box that
// matches id or alias; the composer's @-mention pool carries aliases as
// second labels binding to the same asset id.
//
// Uniqueness is global and case-insensitive on purpose: the panel flips
// between conversation and library scope, and a rule that changes with the
// scope would surprise. An alias is display/search metadata — no asset-existence
// check on set (that would walk every conversation per edit; the panel only
// offers the affordance on rows it listed). An empty alias removes the entry.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// currentAssetAliasSchemaVersion bumps when the registry gains fields. Readers
// ignore the version; older files keep loading.
const currentAssetAliasSchemaVersion = 1

// assetAliasMaxRunes caps an alias — short search keys, not titles (container
// names get 72).
const assetAliasMaxRunes = 64

// assetAliasRegistry is the on-disk shape: asset id → alias. Ids are the
// ConversationAsset ids (ArtifactID by precedence, relative Path fallback for
// pre-ID records), so a dangling entry after a conversation delete simply
// never matches a listed asset again.
type assetAliasRegistry struct {
	SchemaVersion int               `json:"schemaVersion"`
	Aliases       map[string]string `json:"aliases"`
}

// assetAliasMu serializes the registry's load-modify-write cycle — Wails
// bindings run on separate goroutines, and two near-simultaneous alias edits
// must not lose one to the other's stale read.
var assetAliasMu sync.Mutex

// assetAliasesPath is the registry file at the storage root. Empty when no
// root is configured (hand-built partial test configs) — callers refuse
// rather than write into the working directory.
func assetAliasesPath(storage ConfigStorage) string {
	if strings.TrimSpace(storage.Root) == "" {
		return ""
	}
	return filepath.Join(storage.Root, "asset_aliases.json")
}

// loadAssetAliasRegistry reads the registry, fail-soft: a missing or corrupt
// file yields an empty registry so a metadata hiccup can never empty the
// panel (the derived fold's posture). The write path is the one that must be
// loud — see saveAssetAliasRegistry.
func loadAssetAliasRegistry(storage ConfigStorage) assetAliasRegistry {
	registry := assetAliasRegistry{SchemaVersion: currentAssetAliasSchemaVersion, Aliases: map[string]string{}}
	path := assetAliasesPath(storage)
	if path == "" {
		return registry
	}
	var stored assetAliasRegistry
	if err := readJSONFile(path, &stored); err != nil {
		return registry
	}
	if len(stored.Aliases) > 0 {
		registry.Aliases = stored.Aliases
	}
	return registry
}

// saveAssetAliasRegistry writes the registry atomically. Unlike the read, a
// corrupt EXISTING file refuses the write: saving over it would silently
// reset every alias the user ever set, and a metadata store that destroys
// user data on write is worse than one that errors.
func saveAssetAliasRegistry(storage ConfigStorage, aliases map[string]string) error {
	path := assetAliasesPath(storage)
	if path == "" {
		return fmt.Errorf("asset aliases: storage root is not configured")
	}
	var existing assetAliasRegistry
	if err := readJSONFile(path, &existing); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("asset alias registry is unreadable: %w", err)
	}
	if aliases == nil {
		aliases = map[string]string{}
	}
	return writeJSONFile(path, assetAliasRegistry{SchemaVersion: currentAssetAliasSchemaVersion, Aliases: aliases})
}

// normalizeAssetAlias is the alias setter's one normalization: trim, collapse
// internal whitespace, cap the length — the container-name idiom without the
// generated-title cleanup (aliases are always user-authored).
func normalizeAssetAlias(alias string) string {
	return compactString(strings.Join(strings.Fields(alias), " "), assetAliasMaxRunes)
}

// setAssetAlias stores (or, with an empty alias, clears) one asset's alias.
// Uniqueness is global and case-insensitive; the conflict error names the
// asset that holds the alias so the user can find and free it.
func setAssetAlias(storage ConfigStorage, assetID, alias string) error {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return fmt.Errorf("asset id is required")
	}
	normalized := normalizeAssetAlias(alias)
	assetAliasMu.Lock()
	defer assetAliasMu.Unlock()
	registry := loadAssetAliasRegistry(storage)
	if normalized == "" {
		delete(registry.Aliases, assetID)
		return saveAssetAliasRegistry(storage, registry.Aliases)
	}
	for id, existing := range registry.Aliases {
		if id != assetID && strings.EqualFold(existing, normalized) {
			return fmt.Errorf("alias %q is already used by %s", normalized, id)
		}
	}
	registry.Aliases[assetID] = normalized
	return saveAssetAliasRegistry(storage, registry.Aliases)
}

// stampAssetAliases copies assets with their registry aliases filled in. It
// runs AFTER the listing folds and outside conversationAssetCache on purpose:
// alias writes don't bump any conversation's UpdatedAt, so cached folds must
// stay alias-free and the stamp must re-read the registry every call. The
// read is fail-soft (loadAssetAliasRegistry), so listings survive a corrupt
// file — they just lose the names.
func stampAssetAliases(storage ConfigStorage, assets []ConversationAsset) []ConversationAsset {
	if len(assets) == 0 {
		return assets
	}
	aliases := loadAssetAliasRegistry(storage).Aliases
	if len(aliases) == 0 {
		return assets
	}
	stamped := make([]ConversationAsset, len(assets))
	for i, asset := range assets {
		if alias, ok := aliases[asset.ID]; ok {
			asset.Alias = alias
		}
		stamped[i] = asset
	}
	return stamped
}

// SetAssetAlias stores or clears (empty alias) one asset's alias.
func (a *App) SetAssetAlias(assetID string, alias string) error {
	return setAssetAlias(loadStorageOrEmpty(), assetID, alias)
}
