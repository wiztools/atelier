package main

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ConversationAsset is one referable media artifact in a conversation's
// history — the unit the assets panel lists and @-mentions resolve to. Assets
// are derived, never stored: every image/audio/video HistoryContent entry on
// a non-deleted turn becomes one. The ID is the content entry's ArtifactID —
// globally unique (img_/aud_/vid_<hex>) for artifacts written after that
// scheme landed; legacy records keep per-conversation positional IDs
// (turn_000001_img_000001), which stay valid within the conversation. Entries
// from before ArtifactID existed fall back to their Path, also unique within a
// conversation.
type ConversationAsset struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	// Kind is the HistoryContent type: "image", "audio", or "video".
	Kind string `json:"kind"`
	// MimeType is the persisted artifact MIME type, when recorded.
	MimeType string `json:"mimeType,omitempty"`
	// URL is the /atelier-artifact path the panel renders the asset from.
	// Empty when the artifact file is missing on disk — the entry stays listed
	// (it is still part of history) but cannot be previewed or referenced.
	URL string `json:"url,omitempty"`
	// Width and Height are recorded for image assets.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// OriginTurnID and Role record provenance: the turn that persisted the
	// artifact and whether it was user-attached ("user") or generated
	// ("assistant").
	OriginTurnID string `json:"originTurnId"`
	Role         string `json:"role"`
	CreatedAt    string `json:"createdAt,omitempty"`
	// ConversationTitle is the owning conversation's title at fold time. Filled
	// by both the per-conversation and library listings — in a library fold the
	// panel needs it to say which chat each asset came from; per conversation
	// it is constant but harmless.
	ConversationTitle string `json:"conversationTitle,omitempty"`
}

// listConversationAssets derives a conversation's assets from its stored
// history, oldest turn first. The derivation rides getConversation, so
// deleted conversations and deleted turns are already filtered out and
// artifact paths are hydrated to /atelier-artifact URLs.
func listConversationAssets(storage ConfigStorage, conversationID string) ([]ConversationAsset, error) {
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, err
	}
	return conversationAssetsFromDetail(detail), nil
}

// conversationAssetsFromDetail folds a conversation's (sorted) turns into
// assets, preserving turn order and, within a turn, content order. Text and
// thinking entries are skipped; only artifacts with a Path become assets.
func conversationAssetsFromDetail(detail ConversationDetail) []ConversationAsset {
	assets := []ConversationAsset{}
	for _, turn := range detail.Turns {
		for _, content := range turn.Content {
			if content.Type != "image" && content.Type != "audio" && content.Type != "video" {
				continue
			}
			if strings.TrimSpace(content.Path) == "" {
				continue
			}
			id := content.ArtifactID
			if id == "" {
				id = content.Path
			}
			assets = append(assets, ConversationAsset{
				ID:                id,
				ConversationID:    detail.Conversation.ID,
				Kind:              content.Type,
				MimeType:          content.MimeType,
				URL:               content.Text, // hydrated to /atelier-artifact by getConversation when the file exists
				Width:             content.Width,
				Height:            content.Height,
				OriginTurnID:      turn.ID,
				Role:              turn.Role,
				CreatedAt:         turn.CreatedAt,
				ConversationTitle: detail.Conversation.Title,
			})
		}
	}
	return assets
}

// referencedMedia is the outcome of resolving one turn's @-mentioned asset IDs
// against conversation history. The per-kind slices hold data URLs (mention
// order) for the tool attachment slots; entries holds the resolved history
// content entries themselves, for persisting the references on the user turn.
// An entry only appears when its artifact re-read successfully — a dangling
// reference (file missing on disk) yields nothing usable for a tool, nothing
// walkable for the next turn, and nothing renderable in the panel, so it is
// dropped entirely and the user should re-pick.
type referencedMedia struct {
	images  []string
	audios  []string
	videos  []string
	entries []HistoryContent
}

// resolveReferencedAssets maps ReferencedAssetIDs to their artifacts. IDs are
// ConversationAsset IDs — ArtifactID by precedence, with the relative Path as
// a fallback key for pre-ID records. Resolution is conversation-local first;
// IDs that don't resolve locally are looked up across the conversation's
// LIBRARY when it belongs to a project, so an @-mention can cite any asset in
// the library (the FCP model: media uploaded in one chat is referenceable from
// its sibling chats).
//
// fallbackProjectID carries the project on the creation paths, where the
// conversation record is not on disk yet (buildChatUserTurn runs before the
// snapshot is written); once loaded, the record's own ProjectID wins.
//
// Unknown IDs, and artifacts that cannot be re-read as data URLs, are skipped
// rather than failing the turn: the composer only offers resolvable IDs, so
// anything else is a stale reference the user should re-pick, not an error
// worth burning the turn on.
func resolveReferencedAssets(storage ConfigStorage, conversationID, fallbackProjectID string, ids []string) referencedMedia {
	out := referencedMedia{}
	if len(ids) == 0 {
		return out
	}
	localIndex := map[string]HistoryContent{}
	projectID := strings.TrimSpace(fallbackProjectID)
	if strings.TrimSpace(conversationID) != "" {
		if detail, err := getConversation(storage, conversationID); err == nil {
			localIndex = assetContentIndex(detail)
			if detail.Conversation.ProjectID != "" {
				projectID = detail.Conversation.ProjectID
			}
		}
	}
	var libraryIndex map[string]ConversationAsset
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		content, ok := localIndex[id]
		ownerID := conversationID
		if !ok {
			if projectID == "" {
				continue
			}
			if libraryIndex == nil {
				// A failed lookup leaves the empty sentinel in place so the
				// remaining IDs don't each retry the walk.
				if lookup, err := libraryAssetLookup(storage, projectID); err == nil {
					libraryIndex = lookup
				} else {
					libraryIndex = map[string]ConversationAsset{}
				}
			}
			asset, found := libraryIndex[id]
			if !found || asset.URL == "" {
				continue // unknown, or file missing on disk
			}
			ownerID = asset.ConversationID
			// Cross-conversation references persist with an ABSOLUTE Path —
			// the entry lives on this conversation's user turn but points at
			// another conversation's artifacts dir, which a relative path
			// cannot express. Every reader (hydration, the read*Artifact
			// helpers, the history walkers) resolves absolute paths as-is.
			content = HistoryContent{
				Type:       asset.Kind,
				ArtifactID: libraryArtifactID(asset.ID),
				Path:       strings.TrimPrefix(asset.URL, artifactPrefix),
				MimeType:   asset.MimeType,
				Width:      asset.Width,
				Height:     asset.Height,
			}
		}
		var dataURL string
		var readErr error
		switch content.Type {
		case "image":
			dataURL, readErr = readArtifactAsDataURL(storage, ownerID, content)
		case "audio":
			dataURL, readErr = readAudioArtifactAsDataURL(storage, ownerID, content)
		case "video":
			dataURL, readErr = readVideoArtifactAsDataURL(storage, ownerID, content)
		default:
			continue
		}
		if readErr != nil || dataURL == "" {
			continue
		}
		switch content.Type {
		case "image":
			out.images = append(out.images, dataURL)
		case "audio":
			out.audios = append(out.audios, dataURL)
		case "video":
			out.videos = append(out.videos, dataURL)
		}
		out.entries = append(out.entries, content)
	}
	return out
}

// libraryArtifactID keeps ArtifactID empty for legacy path-fallback asset IDs —
// those are only meaningful inside their own conversation, so the absolute
// Path key carries the cross-conversation reference instead. Modern IDs
// (img_/aud_/vid_<hex>) are globally unique and pass through.
func libraryArtifactID(id string) string {
	for _, prefix := range []string{"img_", "aud_", "vid_"} {
		if strings.HasPrefix(id, prefix) {
			return id
		}
	}
	return ""
}

// libraryAssetLookup indexes a whole library's assets by ID — the resolution
// table for @-mentions that cite another conversation's media. Rides the
// cached listLibraryAssets fold, so the first mention in a turn pays the walk
// and everything after (including the same turn's tool-slot resolution) hits
// the cache.
func libraryAssetLookup(storage ConfigStorage, projectID string) (map[string]ConversationAsset, error) {
	project, _, err := findProject(storage, projectID)
	if err != nil {
		return nil, err
	}
	assets, err := listLibraryAssets(storage, project.LibraryID)
	if err != nil {
		return nil, err
	}
	index := make(map[string]ConversationAsset, len(assets))
	for _, asset := range assets {
		if _, seen := index[asset.ID]; !seen {
			index[asset.ID] = asset
		}
	}
	return index, nil
}

// conversationAssetCacheEntry memoizes one conversation's asset fold, valid as
// long as its UpdatedAt is unchanged (every append rewrites conversation.json
// with a new UpdatedAt, so the key self-invalidates).
type conversationAssetCacheEntry struct {
	updatedAt string
	assets    []ConversationAsset
}

// conversationAssetCache keeps library folds cheap: listLibraryAssets walks
// every member conversation's turns, and the panel refreshes after every
// stream, so an uncached fold would re-parse the whole library each turn.
// Bounded by the number of conversations ever listed in this process.
var conversationAssetCache = struct {
	sync.Mutex
	entries map[string]conversationAssetCacheEntry
}{entries: map[string]conversationAssetCacheEntry{}}

// invalidateConversationAssetCache drops entries for conversations removed
// from disk (hard project/library deletes), so a stale fold can't resurrect
// deleted media. dirs are conversation directories; the ID is the base name.
func invalidateConversationAssetCache(dirs []string) {
	conversationAssetCache.Lock()
	defer conversationAssetCache.Unlock()
	for _, dir := range dirs {
		delete(conversationAssetCache.entries, filepath.Base(dir))
	}
}

// listLibraryAssets folds every conversation in a library's projects into one
// asset list — the library-level view for the assets panel and the @-mention
// picker. Assets stay physically in their owning conversation's artifacts
// dir; nothing is copied. Dedupe is by ID, newest occurrence wins (same rule
// as assetContentIndex): an asset referenced by several conversations appears
// once, from the conversation that most recently touched it. Output is
// newest-first for display.
func listLibraryAssets(storage ConfigStorage, libraryID string) ([]ConversationAsset, error) {
	projectIDs, err := libraryProjectIDs(storage, libraryID)
	if err != nil {
		return nil, err
	}
	type libraryMember struct {
		path         string
		conversation HistoryConversation
	}
	members := []libraryMember{}
	err = forEachProjectConversationRecord(storage, projectIDs, false, func(path string, conversation HistoryConversation) error {
		members = append(members, libraryMember{path: path, conversation: conversation})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Newest conversation first so the dedupe below keeps the newest copy.
	sort.Slice(members, func(i, j int) bool {
		if members[i].conversation.UpdatedAt == members[j].conversation.UpdatedAt {
			return members[i].conversation.ID < members[j].conversation.ID
		}
		return members[i].conversation.UpdatedAt > members[j].conversation.UpdatedAt
	})
	assets := []ConversationAsset{}
	seen := map[string]bool{}
	for _, member := range members {
		for _, asset := range cachedConversationAssets(member.path, member.conversation) {
			if seen[asset.ID] {
				continue
			}
			seen[asset.ID] = true
			assets = append(assets, asset)
		}
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].CreatedAt == assets[j].CreatedAt {
			return assets[i].ID < assets[j].ID
		}
		return assets[i].CreatedAt > assets[j].CreatedAt
	})
	return assets, nil
}

// cachedConversationAssets returns a conversation's asset fold through the
// UpdatedAt-keyed cache, folding (getConversationAt + conversationAssetsFromDetail)
// only on a miss or a changed UpdatedAt. path is the conversation.json path the
// tree walk already produced.
func cachedConversationAssets(conversationPath string, conversation HistoryConversation) []ConversationAsset {
	conversationAssetCache.Lock()
	cached, ok := conversationAssetCache.entries[conversation.ID]
	conversationAssetCache.Unlock()
	if ok && cached.updatedAt == conversation.UpdatedAt {
		return cached.assets
	}
	detail, err := getConversationAt(conversationPath)
	if err != nil {
		return nil
	}
	assets := conversationAssetsFromDetail(detail)
	conversationAssetCache.Lock()
	conversationAssetCache.entries[conversation.ID] = conversationAssetCacheEntry{updatedAt: conversation.UpdatedAt, assets: assets}
	conversationAssetCache.Unlock()
	return assets
}

// assetContentIndex maps every referable asset key in a conversation to its
// history content entry. Two keys point at each entry — its ArtifactID and
// its relative Path — so both the modern scheme (img_/aud_/vid_<hex>) and the
// path-fallback IDs of pre-ID records resolve. The walk is newest-first, so
// when a persisted mention re-references an older artifact (same ArtifactID,
// another entry), the newest entry wins.
func assetContentIndex(detail ConversationDetail) map[string]HistoryContent {
	index := map[string]HistoryContent{}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for _, content := range detail.Turns[i].Content {
			if content.Type != "image" && content.Type != "audio" && content.Type != "video" {
				continue
			}
			if strings.TrimSpace(content.Path) == "" {
				continue
			}
			if content.ArtifactID != "" {
				if _, seen := index[content.ArtifactID]; !seen {
					index[content.ArtifactID] = content
				}
			}
			if _, seen := index[content.Path]; !seen {
				index[content.Path] = content
			}
		}
	}
	return index
}
