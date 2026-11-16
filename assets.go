package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConversationAsset is one referable media artifact in a conversation's
// history — the unit the assets panel lists and @-mentions resolve to. Assets
// are derived, never stored: every image/audio/video HistoryContent entry
// (and every .vtt/.srt transcript) on a non-deleted turn becomes one. The ID
// is the content entry's ArtifactID —
// globally unique (img_/aud_/vid_<hex>) for artifacts written after that
// scheme landed; legacy records keep per-conversation positional IDs
// (turn_000001_img_000001), which stay valid within the conversation. Entries
// from before ArtifactID existed fall back to their Path, also unique within a
// conversation.
type ConversationAsset struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	// Kind is the HistoryContent type: "image", "audio", "video",
	// "transcript" (the subtitle files — .vtt/.srt only; plain .txt
	// transcripts are not assets), "markdown" (.md documents),
	// "composition" (the video composer's cmp_ project documents), or
	// "comic" (the comic editor's cbc_ project documents).
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
	// ProjectName is the owning conversation's library project's name — display
	// provenance beside ConversationTitle: the editor pickers lead cross-chat
	// rows with it ("My Project · chat title") so media from sibling projects
	// is identifiable in a library fold. Filled post-fold by both listings
	// (stampAssetProjects); empty for standalone conversations and for uploads
	// whose source chat is no longer a library member.
	ProjectName string `json:"projectName,omitempty"`
	// Name is the uploaded file's original filename — uploads only (see
	// library_assets.go); derived assets keep it empty, their filenames never
	// persisted anywhere.
	Name string `json:"name,omitempty"`
	// Alias is the user-set unique search name (asset_aliases.go). Filled by
	// the listing functions after the fold — never by the folds themselves,
	// whose cache holds no alias state (asset_aliases.go stampAssetAliases).
	Alias string `json:"alias,omitempty"`
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
	assets := stampAssetAliases(storage, conversationAssetsFromDetail(detail))
	// The project name rides like the title: a project conversation's assets
	// carry it so pickers can lead cross-chat rows with it. Fail-soft — a
	// project that fails to resolve costs the name, never the listing.
	if projectID := detail.Conversation.ProjectID; projectID != "" {
		if project, _, findErr := findProject(storage, projectID); findErr == nil {
			assets = stampAssetProjects(assets, map[string]string{detail.Conversation.ID: project.Name})
		}
	}
	return assets, nil
}

// conversationAssetsFromDetail folds a conversation's (sorted) turns into
// assets, preserving turn order and, within a turn, content order. Text and
// thinking entries are skipped; only artifacts with a Path become assets.
func conversationAssetsFromDetail(detail ConversationDetail) []ConversationAsset {
	assets := []ConversationAsset{}
	for _, turn := range detail.Turns {
		for _, content := range turn.Content {
			// Subtitle transcripts (.vtt/.srt — attached or converted), markdown
			// documents (.md), composition documents (cmp_ .json), and comic
			// documents (cbc_ .json) are assets too: referenceable,
			// downloadable, panel-visible. Plain .txt transcripts are text
			// dumps and stay out.
			media := content.Type == "image" || content.Type == "audio" || content.Type == "video"
			if !media && subtitleArtifactExtension(content) == "" && markdownArtifactExtension(content) == "" && compositionArtifactExtension(content) == "" && comicArtifactExtension(content) == "" {
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
		case "transcript":
			// A subtitle mention fills no attachment slot (it is not model
			// input) — the entry below is the whole point: persisted on the
			// user turn, it is what convert_subtitles' record walk reads. The
			// readability check keeps the dangling-reference rule honest.
			_, readErr = readTranscriptArtifact(storage, ownerID, content)
		case "markdown":
			// Same shape as a subtitle mention: no slot, just the persisted
			// entry the markdown editor's record walk reads.
			_, readErr = readTranscriptArtifact(storage, ownerID, content)
		default:
			continue
		}
		if readErr != nil || (dataURL == "" && content.Type != "transcript" && content.Type != "markdown") {
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

// readTranscriptArtifact resolves a transcript content entry to its bytes on
// disk — the existence check a subtitle mention's dangling-reference rule
// needs (the bytes have no consumer here; convert_subtitles re-reads the
// artifact from the record when the turn runs).
func readTranscriptArtifact(storage ConfigStorage, conversationID string, content HistoryContent) ([]byte, error) {
	path, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// libraryArtifactID keeps ArtifactID empty for legacy path-fallback asset IDs —
// those are only meaningful inside their own conversation, so the absolute
// Path key carries the cross-conversation reference instead. Modern IDs
// (img_/aud_/vid_/md_<hex>, and ast_<hex> for uploaded library records) are
// globally unique and pass through.
func libraryArtifactID(id string) string {
	for _, prefix := range []string{"img_", "aud_", "vid_", "md_", "ast_"} {
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
	return libraryAssetIndex(storage, project.LibraryID)
}

// libraryAssetIndex is libraryAssetLookup keyed by the library itself — the
// scope a detached editor draft carries before any conversation (and so any
// project membership on a record) exists.
func libraryAssetIndex(storage ConfigStorage, libraryID string) (map[string]ConversationAsset, error) {
	assets, err := listLibraryAssets(storage, libraryID)
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

// compareRFC3339Instant orders two RFC3339 timestamps by instant, not by
// string (-1 when a is older, 0 when equal, 1 when newer). The asset panel's
// two stamp spellings meet in one sort: derived assets inherit their turn's
// CreatedAt, written local ("…+05:30" — time.Now's RFC3339 in
// conversation_turns.go), while upload records stamp the media file's mod
// time in UTC ("…Z" — library_assets.go). A lexicographic sort pits the two
// wall clocks against each other: on a +05:30 machine a just-uploaded file
// reads five and a half hours older than every asset generated alongside it
// and sinks below half the panel (the "upload not at the top" report). Equal
// instants return 0 so callers keep their own tie-break; a stamp that fails
// to parse degrades to the string comparison — the pre-fix behavior — rather
// than mis-sorting one corrupt stamp into the top slot.
func compareRFC3339Instant(a, b string) int {
	aTime, aErr := time.Parse(time.RFC3339, a)
	bTime, bErr := time.Parse(time.RFC3339, b)
	if aErr != nil || bErr != nil {
		return strings.Compare(a, b)
	}
	switch {
	case aTime.Before(bTime):
		return -1
	case bTime.Before(aTime):
		return 1
	default:
		return 0
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
	// Project names for the rows' provenance ("My Project · chat title"): the
	// members' records carry only the project ID, so the names come off the
	// library's own project records — the same walk libraryProjectIDs just
	// did, read a second time for the one field the ID set drops.
	projects, err := listProjectRecords(storage, libraryID)
	if err != nil {
		return nil, err
	}
	projectNames := make(map[string]string, len(projects))
	for _, project := range projects {
		projectNames[project.ID] = project.Name
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
	memberIDs := make(map[string]bool, len(members))
	for _, member := range members {
		memberIDs[member.conversation.ID] = true
	}
	assets := []ConversationAsset{}
	seen := map[string]bool{}
	// Uploaded records fold in BEFORE the derived walk (library_assets.go):
	// the library owns them outright, and the source-conversation link is
	// kept only while that conversation is still a member here. Records must
	// claim their ast_ ids first — an @-mention persists the id on the
	// mentioning turn, so the derived fold produces an entry with the SAME id
	// and role "user"; letting it win the dedupe demotes the upload to an
	// "attached" card, losing its name snapshot and the panel's delete
	// affordance with it.
	records, err := listLibraryAssetRecords(storage, libraryID)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		seen[record.ID] = true
		assets = append(assets, libraryAssetFromRecord(storage, record, memberIDs))
	}
	for _, member := range members {
		for _, asset := range cachedConversationAssets(member.path, member.conversation) {
			if seen[asset.ID] {
				continue
			}
			seen[asset.ID] = true
			assets = append(assets, asset)
		}
	}
	// Newest-first by instant (compareRFC3339Instant — this list mixes the
	// upload records' UTC stamps with the turns' local ones), ties by ID.
	sort.Slice(assets, func(i, j int) bool {
		if order := compareRFC3339Instant(assets[i].CreatedAt, assets[j].CreatedAt); order != 0 {
			return order > 0
		}
		return assets[i].ID < assets[j].ID
	})
	// Every member carries a ProjectID (membership IS the project), so the
	// conversation→name map covers the whole fold; uploads ride along through
	// their source conversation's membership-gated ConversationID — a source
	// chat that left the library keeps its title snapshot but loses the name,
	// the same split as its jump link.
	chatProjects := make(map[string]string, len(members))
	for _, member := range members {
		if name, ok := projectNames[member.conversation.ProjectID]; ok {
			chatProjects[member.conversation.ID] = name
		}
	}
	assets = stampAssetProjects(assets, chatProjects)
	return stampAssetAliases(storage, assets), nil
}

// stampAssetProjects copies assets with each conversation's project name
// filled in. names maps conversation id → project name; entries without a
// mapping (standalone chats, uploads whose source chat left the library)
// keep the field empty. It runs AFTER the listing folds and outside
// conversationAssetCache on purpose — the stampAssetAliases pattern: cached
// folds must stay project-free so a project move (MoveConversationToProject)
// reads fresh names on the very next listing.
func stampAssetProjects(assets []ConversationAsset, names map[string]string) []ConversationAsset {
	if len(assets) == 0 || len(names) == 0 {
		return assets
	}
	stamped := make([]ConversationAsset, len(assets))
	for i, asset := range assets {
		if name, ok := names[asset.ConversationID]; ok {
			asset.ProjectName = name
		}
		stamped[i] = asset
	}
	return stamped
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
			media := content.Type == "image" || content.Type == "audio" || content.Type == "video"
			if !media && subtitleArtifactExtension(content) == "" && markdownArtifactExtension(content) == "" {
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
