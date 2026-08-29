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
