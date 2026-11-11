package main

// library_assets.go — first-class library asset records: media uploaded into a
// library directly, with no conversation turn behind them. The rest of the
// asset system is derived — an asset exists because some HistoryContent entry
// on a turn points at a file in a conversation's artifacts directory (see
// assets.go). Uploads are the exception: the record below is the stored truth,
// one JSON file beside its media under the library's own directory, so the
// library owns the bytes outright (they survive conversation deletion, and a
// per-asset delete is possible for the first time).
//
// The ast_ id prefix is deliberate, the up_ rationale in reverse: helpers
// throughout the app key conversation-artifact ids by prefix (img_/aud_/vid_/
// md_/trc_), and a distinct prefix keeps a library record from ever being
// mistaken for one — editor opens and conversation-scoped resolvers cannot
// resolve it, which is correct: an upload is referenced by @-mention and
// downloaded, not opened in a conversation-bound editor.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"atelier/internal/mediasniff"
)

// currentLibraryAssetSchemaVersion bumps when the record gains fields. Import
// and the fold read the fields they know; older records keep loading.
const currentLibraryAssetSchemaVersion = 1

// LibraryAsset is one uploaded library asset's persisted record. Kind uses the
// HistoryContent vocabulary ("image", "audio", "video", "transcript",
// "markdown") so the fold, the panel, and mention resolution need no mapping.
// FileName is the media file's name within the library's assets directory —
// records never carry absolute paths, so an archive round trip needs no
// rewriting inside them.
type LibraryAsset struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	LibraryID     string `json:"libraryId"`
	Kind          string `json:"kind"`
	// Name is the uploaded file's original name, display-only: the panel's
	// tooltip and the save dialogs' suggested names. Never used as a filename.
	Name string `json:"name"`
	// FileName is the media file within the library's assets directory
	// ("ast_<hex>.png"). A video's poster sibling follows the house convention
	// (ast_<hex>_poster.jpg) and has no field — it is derived.
	FileName string `json:"fileName"`
	MimeType string `json:"mimeType,omitempty"`
	// Width and Height record an image's pixel dimensions at upload time.
	Width  int   `json:"width,omitempty"`
	Height int   `json:"height,omitempty"`
	Size   int64 `json:"size"`
	// Source provenance: the conversation the upload happened from, when it
	// resolved and belonged to this library. Display-only — the fold drops the
	// link once the conversation is no longer a library member (deleted, or
	// moved out), while the title snapshot keeps the caption honest either way.
	SourceConversationID    string `json:"sourceConversationId,omitempty"`
	SourceConversationTitle string `json:"sourceConversationTitle,omitempty"`
	CreatedAt               string `json:"createdAt"`
}

// libraryAssetsDir is where a library's uploaded assets live — inside the
// library's own directory, so deleteLibrary (which removes the whole tree)
// destroys uploads with the library, and the archive packs them beside the
// records they belong to.
func libraryAssetsDir(storage ConfigStorage, libraryID string) string {
	return filepath.Join(libraryDir(storage, libraryID), "assets")
}

// listLibraryAssetRecords reads every uploaded asset record in a library,
// newest first. A corrupt record is skipped, not fatal — one bad file must not
// empty the panel (the derived fold's fail-soft posture).
func listLibraryAssetRecords(storage ConfigStorage, libraryID string) ([]LibraryAsset, error) {
	entries, err := os.ReadDir(libraryAssetsDir(storage, libraryID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	records := []LibraryAsset{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var record LibraryAsset
		if err := readJSONFile(filepath.Join(libraryAssetsDir(storage, libraryID), entry.Name()), &record); err != nil {
			continue
		}
		if record.ID == "" || record.FileName == "" {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt == records[j].CreatedAt {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt > records[j].CreatedAt
	})
	return records, nil
}

// libraryAssetFromRecord maps a persisted upload record onto the
// ConversationAsset the panel, the mention pool, and the archive all speak.
// memberIDs, when non-nil, gates the source-conversation link: an upload is
// only jump-able while its source chat is still a member of the library.
// URL follows hydration's fail-soft rule — empty when the file is missing on
// disk, the entry stays listed but cannot be previewed or referenced.
func libraryAssetFromRecord(storage ConfigStorage, record LibraryAsset, memberIDs map[string]bool) ConversationAsset {
	url := ""
	mediaPath := filepath.Join(libraryAssetsDir(storage, record.LibraryID), record.FileName)
	if abs, err := filepath.Abs(mediaPath); err == nil {
		if _, err := os.Stat(abs); err == nil {
			url = artifactPrefix + filepath.ToSlash(abs)
		}
	}
	sourceID := ""
	if record.SourceConversationID != "" && (memberIDs == nil || memberIDs[record.SourceConversationID]) {
		sourceID = record.SourceConversationID
	}
	// The title snapshot always rides: an upload whose source chat is gone
	// keeps saying where it came from, it just loses the jump link.
	return ConversationAsset{
		ID:                record.ID,
		Name:              record.Name,
		ConversationID:    sourceID,
		Kind:              record.Kind,
		MimeType:          record.MimeType,
		URL:               url,
		Width:             record.Width,
		Height:            record.Height,
		OriginTurnID:      "",
		Role:              "upload",
		CreatedAt:         record.CreatedAt,
		ConversationTitle: record.SourceConversationTitle,
	}
}

// uploadLibraryAsset persists one uploaded file as a library asset record.
// payload is the same string the composer's attachment path carries — a data
// URL (or, for audio/video past the staging threshold, the staged upload's
// /atelier-artifact URL, adopted rather than re-decoded). conversationID is
// optional provenance; it must resolve and belong to this library or it is
// omitted — a stale id is a display nit, never an upload failure.
func uploadLibraryAsset(config AppConfig, libraryID, conversationID, name, kind, payload string) (ConversationAsset, error) {
	if _, err := findLibrary(config.Storage, libraryID); err != nil {
		return ConversationAsset{}, err
	}
	name = compactString(strings.TrimSpace(name), 120)
	if name == "" {
		name = "Upload"
	}
	assetsDir := libraryAssetsDir(config.Storage, libraryID)
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		return ConversationAsset{}, err
	}

	assetID := randomID("ast")
	assetKind := ""
	extension := ""
	// stagedPath is set when payload points at a staged upload — the media is
	// linked, not decoded, exactly like the turn-persistence path
	// (historyContentForMessage).
	stagedPath := ""
	if kind == "audio" || kind == "video" {
		if resolved, isStaged := stagedUploadPath(payload, mediaUploadsDir(config.Storage)); isStaged {
			stagedPath = resolved
		}
	}

	var data []byte
	switch kind {
	case "image":
		assetKind = "image"
		var err error
		data, extension, err = decodeImagePayload(payload)
		if err != nil {
			return ConversationAsset{}, err
		}
	case "audio", "video":
		assetKind = kind
		kindPrefix, houseExtension := "audio", ".mp3"
		if kind == "video" {
			kindPrefix, houseExtension = "video", ".mp4"
		}
		if stagedPath != "" {
			// The staged extension is kept when it maps to the kind's media
			// type, else the house extension lands — adoptStagedUpload's rule.
			extension = strings.ToLower(filepath.Ext(stagedPath))
			if !strings.HasPrefix(mediaTypeForExtension(extension), kindPrefix+"/") {
				extension = houseExtension
			}
		} else {
			var err error
			if kind == "audio" {
				data, extension, err = decodeAudioPayload(payload)
			} else {
				data, extension, err = decodeVideoPayload(payload)
			}
			if err != nil {
				return ConversationAsset{}, err
			}
		}
	case "subtitle":
		assetKind = "transcript"
		var err error
		data, err = decodeSubtitlePayload(payload)
		if err != nil {
			return ConversationAsset{}, err
		}
		// The bytes must parse as real cue content and the extension follows
		// the content, not the declared mime — writeSubtitleAttachment's
		// contract, replicated here because the record mints an ast_ id.
		_, format, _, err := parseSubtitles(data)
		if err != nil {
			return ConversationAsset{}, fmt.Errorf("attached file is not VTT or SRT subtitle content: %w", err)
		}
		extension = map[string]string{subtitleFormatVTT: ".vtt", subtitleFormatSRT: ".srt"}[format]
	case "markdown":
		assetKind = "markdown"
		data, mime, err := decodeMediaDataURL(payload)
		if err != nil {
			return ConversationAsset{}, err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return ConversationAsset{}, errors.New("markdown document is empty")
		}
		if !utf8.Valid(data) {
			return ConversationAsset{}, errors.New("markdown document is not UTF-8 text")
		}
		extension = markdownExtensionForMime(mime)
	default:
		return ConversationAsset{}, fmt.Errorf("unsupported library asset kind %q (want image, audio, video, subtitle, or markdown)", kind)
	}

	mediaPath := filepath.Join(assetsDir, assetID+extension)
	if stagedPath != "" {
		if err := adoptStagedUploadFile(stagedPath, mediaPath); err != nil {
			return ConversationAsset{}, err
		}
		if kind == "video" {
			posterName := strings.TrimSuffix(assetID+extension, extension) + "_poster.jpg"
			if !adoptStagedUploadPoster(stagedPath, filepath.Join(assetsDir, posterName)) {
				generateVideoPoster(config, mediaPath)
			}
		}
	} else {
		if err := os.WriteFile(mediaPath, data, 0644); err != nil {
			return ConversationAsset{}, err
		}
		if kind == "video" {
			// Poster beside an uploaded clip, best-effort per the
			// video_poster.go contract, same as attached and generated ones.
			generateVideoPoster(config, mediaPath)
		}
	}

	info, err := os.Stat(mediaPath)
	if err != nil {
		return ConversationAsset{}, err
	}
	record := LibraryAsset{
		SchemaVersion: currentLibraryAssetSchemaVersion,
		ID:            assetID,
		LibraryID:     libraryID,
		Kind:          assetKind,
		Name:          name,
		FileName:      assetID + extension,
		MimeType:      mediaTypeForExtension(extension),
		Size:          info.Size(),
		CreatedAt:     info.ModTime().UTC().Format(time.RFC3339),
	}
	if assetKind == "image" && data != nil {
		if width, height, ok := mediasniff.ImagePixelDimensions(data); ok {
			record.Width = int(width)
			record.Height = int(height)
		}
	}
	if sourceID, sourceTitle, ok := libraryAssetSource(config.Storage, libraryID, conversationID); ok {
		record.SourceConversationID = sourceID
		record.SourceConversationTitle = sourceTitle
	}
	if err := writeJSONFile(filepath.Join(assetsDir, assetID+".json"), record); err != nil {
		return ConversationAsset{}, err
	}

	memberIDs := map[string]bool{}
	if record.SourceConversationID != "" {
		memberIDs[record.SourceConversationID] = true
	}
	return libraryAssetFromRecord(config.Storage, record, memberIDs), nil
}

// libraryAssetSource validates upload provenance: the conversation must exist
// and belong to one of the library's projects. The second return is the
// record's title — a snapshot, so the caption survives the conversation's
// later deletion.
func libraryAssetSource(storage ConfigStorage, libraryID, conversationID string) (string, string, bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", "", false
	}
	projectIDs, err := libraryProjectIDs(storage, libraryID)
	if err != nil {
		return "", "", false
	}
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return "", "", false
	}
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return "", "", false
	}
	if conversation.DeletedAt != "" || !projectIDs[conversation.ProjectID] {
		return "", "", false
	}
	return conversation.ID, conversation.Title, true
}

// UploadLibraryAsset persists one uploaded file into a library. See
// uploadLibraryAsset for the payload contract.
func (a *App) UploadLibraryAsset(libraryID string, conversationID string, name string, kind string, payload string) (ConversationAsset, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ConversationAsset{}, err
	}
	return uploadLibraryAsset(config, libraryID, conversationID, name, kind, payload)
}

// deleteLibraryAsset removes an uploaded asset's record, media, and poster
// sibling. Conversations holding @-mentions of it keep their entries — the
// existing dangling-reference semantics (unresolvable mentions are dropped
// silently at resolution, and the archive's missing set records the gap).
func deleteLibraryAsset(storage ConfigStorage, assetID string) error {
	assetID = strings.TrimSpace(assetID)
	if !strings.HasPrefix(assetID, "ast_") {
		return fmt.Errorf("refusing to delete %q: not a library asset", assetID)
	}
	libraries, err := listLibraryRecords(storage)
	if err != nil {
		return err
	}
	for _, library := range libraries {
		recordPath := filepath.Join(libraryAssetsDir(storage, library.ID), assetID+".json")
		if _, err := os.Stat(recordPath); err != nil {
			continue
		}
		var record LibraryAsset
		if err := readJSONFile(recordPath, &record); err == nil && record.FileName != "" {
			mediaPath := filepath.Join(libraryAssetsDir(storage, library.ID), record.FileName)
			if err := os.Remove(mediaPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			posterPath := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath)) + "_poster.jpg"
			if err := os.Remove(posterPath); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := os.Remove(recordPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return fmt.Errorf("no library asset %s", assetID)
}

// DeleteLibraryAsset removes an uploaded library asset.
func (a *App) DeleteLibraryAsset(assetID string) error {
	return deleteLibraryAsset(loadStorageOrEmpty(), assetID)
}
