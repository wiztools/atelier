package main

import (
	"strings"
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
				ID:             id,
				ConversationID: detail.Conversation.ID,
				Kind:           content.Type,
				MimeType:       content.MimeType,
				URL:            content.Text, // hydrated to /atelier-artifact by getConversation when the file exists
				Width:          content.Width,
				Height:         content.Height,
				OriginTurnID:   turn.ID,
				Role:           turn.Role,
				CreatedAt:      turn.CreatedAt,
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
// a fallback key for pre-ID records. Unknown IDs, and artifacts that cannot be
// re-read as data URLs, are skipped rather than failing the turn: the composer
// only offers resolvable IDs, so anything else is a stale reference the user
// should re-pick, not an error worth burning the turn on.
func resolveReferencedAssets(storage ConfigStorage, conversationID string, ids []string) referencedMedia {
	out := referencedMedia{}
	if len(ids) == 0 || strings.TrimSpace(conversationID) == "" {
		return out
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return out
	}
	index := assetContentIndex(detail)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		content, ok := index[id]
		if !ok {
			continue
		}
		var dataURL string
		var readErr error
		switch content.Type {
		case "image":
			dataURL, readErr = readArtifactAsDataURL(storage, conversationID, content)
		case "audio":
			dataURL, readErr = readAudioArtifactAsDataURL(storage, conversationID, content)
		case "video":
			dataURL, readErr = readVideoArtifactAsDataURL(storage, conversationID, content)
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
