package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// EditorLaunchResult names the conversation CreateEditorConversation persisted
// and the artifacts the editors open by. (conversationID, artifactID) is the
// one identity every editor entry point takes — the assets panel, the inline
// Edit buttons, and this launch path alike.
type EditorLaunchResult struct {
	ConversationID      string   `json:"conversationId"`
	ImageArtifactIDs    []string `json:"imageArtifactIds,omitempty"`
	VideoArtifactIDs    []string `json:"videoArtifactIds,omitempty"`
	AudioArtifactIDs    []string `json:"audioArtifactIds,omitempty"`
	SubtitleArtifactIDs []string `json:"subtitleArtifactIds,omitempty"`
	MarkdownArtifactIDs []string `json:"markdownArtifactIds,omitempty"`
}

// editorLaunchEventName is the atelier:editor-launch payload's event: the
// harness's open_editor tool fired and the named conversation's asset should
// open in the matching built-in editor. The frontend opens only when the
// conversation is the active one and no editor is already on screen — a
// mid-stream launch must never steal focus from an edit in progress.
const editorLaunchEventName = "atelier:editor-launch"

type EditorLaunchEvent struct {
	ConversationID string `json:"conversationId"`
	ArtifactID     string `json:"artifactId"`
	Kind           string `json:"kind"`
	// SeedArtifactIDs carries the workspace editors' seeding — the video
	// composer's referenced video and audio, the comic editor's referenced
	// images, in order (empty for an unseeded open). The artifact editors
	// ignore it.
	SeedArtifactIDs []string `json:"seedArtifactIds,omitempty"`
}

// emitEditorLaunchEvent delivers the open_editor tool's UI directive and
// reports whether it was delivered — a false lets the caller fail the tool
// instead of leaving evidence that claims an editor no UI ever showed. Like
// emitChatEvent it no-ops without a Wails context (tests, early startup).
func (a *App) emitEditorLaunchEvent(event EditorLaunchEvent) bool {
	if a.ctx == nil {
		return false
	}
	runtime.EventsEmit(a.ctx, editorLaunchEventName, event)
	return true
}

// ToolOpenEditorResult is the open_editor tool's evidence payload: what was
// opened and where. It carries no media, no cost, and no model — the built-in
// editor is a UI surface, not a generation backend — so it lands as a bare
// tool_call activity (name/status only) in the ledger.
type ToolOpenEditorResult struct {
	Kind           string `json:"kind"`
	ConversationID string `json:"conversationId"`
	ArtifactID     string `json:"artifactId"`
}

// editorOpenKind maps a history content entry onto the editor kind that opens
// it: media map directly, a VTT/SRT transcript artifact opens in the subtitle
// editor, a markdown artifact in the markdown editor. Empty when no built-in
// editor reads the entry (plain .txt transcripts, thinking text).
func editorOpenKind(content HistoryContent) string {
	switch content.Type {
	case "image", "video", "audio":
		return content.Type
	case "transcript":
		if subtitleArtifactExtension(content) != "" {
			return "subtitle"
		}
	case "markdown":
		if markdownArtifactExtension(content) != "" {
			return "markdown"
		}
	case "comic":
		if comicArtifactExtension(content) != "" {
			return "comic"
		}
	}
	return ""
}

// newestEditorArtifact finds the artifact an open_editor call should open:
// the newest turn's first content entry of the requested kind carrying an
// artifact id — the same newest-turn-first walk newestVisualMediaKind uses
// for the history fallback, so "the editor" and "the image" resolve to the
// same asset. An empty kind means the newest image, video, or audio of any
// kind — kindless never opens subtitles or markdown on its own, since a bare
// "open the editor" ask most plausibly means visual media and document files
// are opened by name (mode "subtitle" / "markdown") — EXCEPT when the newest
// turn is still a user turn: an open ask riding this turn's own attachments
// resolves against them first, whatever their kind, because the attachment is
// the disambiguator (attach a subtitle file, say "open editor", and the
// subtitle editor is what should open — not the newest visual media). Once
// the assistant turn lands, the bare ask reverts to the visual default. The
// subtitle kind matches transcript artifacts carrying VTT/SRT content
// (subtitleArtifactExtension), never plain .txt transcripts; the markdown
// kind matches md_ artifacts (markdownArtifactExtension). Attachments,
// @-mentions, and generated media all persist as artifacts, so every one of
// them is openable; other text is skipped.
func newestEditorArtifact(storage ConfigStorage, conversationID, kind string) (string, string, error) {
	if kind != "" && kind != "image" && kind != "video" && kind != "audio" && kind != "subtitle" && kind != "markdown" && kind != "comic" {
		return "", "", fmt.Errorf("unknown editor kind %q", kind)
	}
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return "", "", err
	}
	if kind == "" && len(detail.Turns) > 0 && detail.Turns[len(detail.Turns)-1].Role == "user" {
		turn := detail.Turns[len(detail.Turns)-1]
		seen := map[string]bool{}
		names := []string{}
		pick := ""
		for _, content := range turn.Content {
			if content.ArtifactID == "" {
				continue
			}
			openKind := editorOpenKind(content)
			if openKind == "" {
				continue
			}
			// Entries persist in attachment order, so the last openable entry
			// is the most recently attached — the natural pick within a kind.
			pick = content.ArtifactID
			if !seen[openKind] {
				seen[openKind] = true
				names = append(names, openKind)
			}
		}
		switch {
		case len(names) == 1:
			return names[0], pick, nil
		case len(names) > 1:
			return "", "", fmt.Errorf("open_editor found several editor kinds among this turn's media (%s) — name the editor to open, e.g. mode %q", strings.Join(names, ", "), names[0])
		}
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for _, content := range detail.Turns[i].Content {
			if content.ArtifactID == "" {
				continue
			}
			if kind == "" && content.Type != "image" && content.Type != "video" && content.Type != "audio" {
				continue
			}
			if kind == "subtitle" {
				if content.Type != "transcript" || subtitleArtifactExtension(content) == "" {
					continue
				}
			} else if kind == "markdown" {
				if content.Type != "markdown" || markdownArtifactExtension(content) == "" {
					continue
				}
			} else if kind == "comic" {
				if content.Type != "comic" || comicArtifactExtension(content) == "" {
					continue
				}
			} else if kind != "" && content.Type != kind {
				continue
			}
			if kind == "" {
				kind = content.Type
			}
			return kind, content.ArtifactID, nil
		}
	}
	if kind == "" {
		return "", "", errors.New("open_editor found no image, video, or audio in this conversation — ask the user to attach or generate one first")
	}
	return "", "", fmt.Errorf("open_editor found no %s in this conversation — ask the user to attach or generate one first", kind)
}

// compositionSeedIDs gathers the video and audio artifact ids this turn's
// explicit media carries — attachments and @-mentions both land as content
// entries on the user turn, so one walk covers them (cross-conversation
// mentions carry their globally-unique ids). Videos first, then audio:
// cross-kind interleaving is lost at persistence (attachments arrive as
// per-kind arrays), so the deterministic reading of "reference order" is
// within-kind order with clips ahead of music. Empty when the newest turn
// isn't the user's or holds no media: the composer opens unseeded, and its
// picker is right there.
func compositionSeedIDs(storage ConfigStorage, conversationID string) ([]string, error) {
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, err
	}
	if len(detail.Turns) == 0 || detail.Turns[len(detail.Turns)-1].Role != "user" {
		return []string{}, nil
	}
	turn := detail.Turns[len(detail.Turns)-1]
	videos := []string{}
	audios := []string{}
	for _, content := range turn.Content {
		if content.ArtifactID == "" {
			continue
		}
		if content.Type == "video" {
			videos = append(videos, content.ArtifactID)
		} else if content.Type == "audio" {
			audios = append(audios, content.ArtifactID)
		}
	}
	return append(videos, audios...), nil
}

// comicSeedIDs gathers the image artifact ids the comic editor's draft seeds
// with — the tray's starting palette. The newest user turn's images come
// first in content order (attachments, then @-mention references — the
// deterministic reading of "reference order"); the walk then continues
// BACKWARD through the rest of the conversation, newest artifact first, so
// the art earlier turns produced fills the tray behind them — a
// generate_image output lands on the assistant turn, not the ask, and the
// comic-open ask itself usually carries no images (conv_0d2e8870b27af2acc0441794:
// three images in the conversation, a bare newest user turn, an empty tray).
// Ids dedupe by artifact id (a mention re-references an older artifact at
// its newest position), entries whose files are gone on disk skip (the tray
// stays pickable-and-real; the assets panel is where missing files surface),
// and the list caps at comicMaxUnplacedImages — the tray's own ceiling.
// Empty when the conversation holds no resolvable images at all: the comic
// opens unseeded and the picker is right there.
func comicSeedIDs(storage ConfigStorage, conversationID string) ([]string, error) {
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	candidates := []HistoryContent{}
	collect := func(turn HistoryTurn) {
		for _, content := range turn.Content {
			if content.ArtifactID == "" || content.Type != "image" || seen[content.ArtifactID] {
				continue
			}
			seen[content.ArtifactID] = true
			candidates = append(candidates, content)
		}
	}
	if len(detail.Turns) > 0 && detail.Turns[len(detail.Turns)-1].Role == "user" {
		collect(detail.Turns[len(detail.Turns)-1])
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		if i == len(detail.Turns)-1 && detail.Turns[i].Role == "user" {
			continue // the newest user turn already walked — its refs lead
		}
		collect(detail.Turns[i])
	}
	ids := make([]string, 0, len(candidates))
	for _, content := range candidates {
		if len(ids) >= comicMaxUnplacedImages {
			break
		}
		path, err := contentArtifactPath(storage, conversationID, content)
		if err != nil {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		ids = append(ids, content.ArtifactID)
	}
	return ids, nil
}

// executeOpenEditor resolves the artifact and delivers the launch event. The
// editors open by (conversationID, artifactID) — the same identity every other
// editor entry point takes — and the hook indirection keeps the definition
// testable without a Wails runtime, like every other capability hook on the
// tool execution context. The video composer and the comic editor are the
// exceptions: neither opens an artifact — each opens the workspace seeded
// with the turn's referenced media (the seeded-only contract: media land
// exactly as if picked, in reference order, and nothing is arranged — for
// the comic that means the images wait in the unplaced tray, since arranging
// the frames is the human's half of the loop).
func executeOpenEditor(tools HarnessToolExecutionContext, mode string) (ToolOpenEditorResult, string, error) {
	if strings.TrimSpace(tools.ConversationID) == "" {
		return ToolOpenEditorResult{}, "editor launch is unavailable", errors.New("open_editor opens a conversation asset, and no conversation is attached to this call")
	}
	if tools.OpenEditor == nil {
		return ToolOpenEditorResult{}, "editor launch is unavailable", errors.New("open_editor is not available in this context")
	}
	if mode == "composition" {
		seeds, err := compositionSeedIDs(tools.Storage, tools.ConversationID)
		if err != nil {
			return ToolOpenEditorResult{}, "editor launch failed", err
		}
		if err := tools.OpenEditor("composition", tools.ConversationID, "", seeds); err != nil {
			return ToolOpenEditorResult{}, "editor launch failed", err
		}
		summary := "Opened the video composer on the user's screen"
		if len(seeds) > 0 {
			plural := "s"
			if len(seeds) == 1 {
				plural = ""
			}
			summary += fmt.Sprintf(", seeded with %d referenced clip%s in reference order — nothing was arranged, trimmed, or transitioned; the composition is an unsaved draft until the user saves it", len(seeds), plural)
		} else {
			summary += " (unseeded — this turn referenced no video or audio); the user adds media from the composer's picker"
		}
		return ToolOpenEditorResult{Kind: "composition", ConversationID: tools.ConversationID}, summary, nil
	}
	if mode == "comic" {
		seeds, err := comicSeedIDs(tools.Storage, tools.ConversationID)
		if err != nil {
			return ToolOpenEditorResult{}, "editor launch failed", err
		}
		if err := tools.OpenEditor("comic", tools.ConversationID, "", seeds); err != nil {
			return ToolOpenEditorResult{}, "editor launch failed", err
		}
		summary := "Opened the comic editor on the user's screen"
		if len(seeds) > 0 {
			plural := "s"
			if len(seeds) == 1 {
				plural = ""
			}
			summary += fmt.Sprintf(", seeded with %d of the conversation's image%s (this turn's references first, then newer art first) — nothing was arranged: the images wait in the unplaced tray, and the comic is an unsaved draft until the user saves it", len(seeds), plural)
		} else {
			summary += " (unseeded — this conversation holds no images yet); the user adds art from the comic's picker"
		}
		return ToolOpenEditorResult{Kind: "comic", ConversationID: tools.ConversationID}, summary, nil
	}
	kind, artifactID, err := newestEditorArtifact(tools.Storage, tools.ConversationID, mode)
	if err != nil {
		return ToolOpenEditorResult{}, "editor launch failed", err
	}
	if err := tools.OpenEditor(kind, tools.ConversationID, artifactID, nil); err != nil {
		return ToolOpenEditorResult{}, "editor launch failed", err
	}
	summary := fmt.Sprintf("Opened %s in the built-in %s editor on the user's screen. The asset was opened as-is — nothing was generated or modified; do not describe an edit as done.", artifactID, kind)
	return ToolOpenEditorResult{Kind: kind, ConversationID: tools.ConversationID, ArtifactID: artifactID}, summary, nil
}

// openEditorToolDefinition is the harness's editor-launch capability — the AI
// tier beside the composer's deterministic detection (editorLaunch.ts). It is
// unconditional in the registry: the built-in editors need no keys, no CLI,
// and no model, so an editor-open ask never degrades to an install note or a
// from-knowledge text answer. The side effect is the atelier:editor-launch
// event; the tool's whole job is pointing the UI at the right artifact.
func openEditorToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "open_editor",
		Title:       "Open in editor",
		Description: "Opens one of the conversation's assets in Atelier's built-in editor on the user's screen. mode \"image\" opens the image editor, \"video\" the video editor, \"audio\" the audio editor, \"subtitle\" the subtitle editor (a .vtt or .srt transcript artifact), \"markdown\" the markdown editor (a .md document artifact), \"composition\" the video composer — the multi-clip editing workspace, opened seeded with the turn's referenced video and audio in reference order (nothing is arranged) — and \"comic\" the comic book editor (some users word it the \"comic composer\") — the page-and-panel workspace, opened seeded with the conversation's images (this turn's references first, then newer art first; nothing is arranged — the images wait in the unplaced tray). Omit mode to open whichever editor matches the asset the user means — this turn's attachment when one exists, otherwise the conversation's newest image, video, or audio artifact. The asset is the newest artifact of that kind — this turn's attachment, an @-mentioned asset, or one from conversation history — and it is opened as-is, never modified or regenerated. Call this ONLY when the user asks to open an asset in an editor or workspace (e.g. \"open this in the video editor\", \"open the document in the markdown editor\", \"open the video composer\", \"open the comic editor\"); it does not perform edits — for a change the user described, use the edit or generation tools instead. When the user asks to JOIN or CONCATENATE clips into one rendered file with no editing workspace in the wording, that is join_videos/join_audios, not mode \"composition\" — the composer is for when the user names the composer or wants an editable composition.",
		Example:     `{"name":"open_editor","mode":"video"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"mode": enumParam(`Which editor to open — "image", "video", "audio", "subtitle" (a .vtt/.srt transcript artifact), "markdown" (a .md document artifact), "composition" (the video composer, seeded with this turn's referenced video and audio), or "comic" (the comic book editor, seeded with the conversation's images). Omit when the user attached the asset this turn or the ask names the kind — omitting resolves this turn's attachment first, else the conversation's newest image, video, or audio artifact.`, "image", "video", "audio", "subtitle", "markdown", "composition", "comic"),
			},
			"required": []string{},
		},
		Validate: func(prefix string, call HarnessToolCall) []string {
			switch strings.TrimSpace(call.Mode) {
			case "", "image", "video", "audio", "subtitle", "markdown", "composition", "comic":
				return nil
			default:
				return []string{prefix + `.mode must be "image", "video", "audio", "subtitle", "markdown", "composition", or "comic" for open_editor`}
			}
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			return executeOpenEditor(tools, strings.TrimSpace(call.Mode))
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			// The launch consumes nothing — no media kind, no cost, no model —
			// so the bare projection carries just the verb and what it opened.
			if typed, ok := result.Result.(ToolOpenEditorResult); ok {
				activity.Command = []string{"open_editor", typed.Kind}
			}
			return activity
		},
	}
}

// CreateEditorConversation persists a brand-new conversation whose only turn is
// the user's editor-launch request — the prompt text plus an attached image or
// video — and names the persisted artifacts so the frontend can open the
// matching editor on them. This is StreamChat's turn-1 creation path without
// the stream: no harness engine is built and no model is called, but the record
// keeps the same lifecycle pinning (workspace, project, model overrides) as
// writePendingChatConversation, so a follow-up chat turn in the conversation
// behaves exactly like any other.
func (a *App) CreateEditorConversation(req ChatRequest) (EditorLaunchResult, error) {
	if len(req.Messages) == 0 {
		return EditorLaunchResult{}, errors.New("at least one message is required")
	}
	config, err := loadReadyConfig()
	if err != nil {
		return EditorLaunchResult{}, err
	}
	// Fail an unknown project before anything is persisted — the same
	// fail-early posture as StreamChat.
	if _, err := resolveTurnProject(config, req); err != nil {
		return EditorLaunchResult{}, err
	}
	workspaceRoot, err := resolveTurnWorkspace(config, req)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		config.Tools.Filesystem.Root = workspaceRoot
	}
	// A primary override rewrites req.Model, so the record's Defaults.ChatModel
	// pins the overridden selection — same as every first turn.
	config, req, err = applyConversationModelOverrides(config, req)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	message := lastUserMessage(req.Messages)
	if len(message.Images) == 0 && len(message.Videos) == 0 && len(message.Audios) == 0 && len(message.Subtitles) == 0 && len(message.Markdowns) == 0 {
		return EditorLaunchResult{}, errors.New("an attached image, video, audio, subtitle, or markdown file is required to open an editor")
	}
	// Opening an editor must not require a configured chat model — nothing is
	// called on this path — but stamp the selection when there is one so a
	// later follow-up turn starts from the same defaults as any conversation.
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(config.Providers.Ollama.Models.Primary)
	}
	conversationID, _, _, _, err := writePendingChatConversation(config, req)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	// The editors open by artifact id, which the creation helper does not
	// return — read the turn back and collect the persisted attachments by
	// kind, in attachment order (mention references, if any, come after).
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return EditorLaunchResult{}, err
	}
	result := EditorLaunchResult{ConversationID: conversationID}
	for _, turn := range detail.Turns {
		for _, content := range turn.Content {
			if content.ArtifactID == "" {
				continue
			}
			switch content.Type {
			case "image":
				result.ImageArtifactIDs = append(result.ImageArtifactIDs, content.ArtifactID)
			case "video":
				result.VideoArtifactIDs = append(result.VideoArtifactIDs, content.ArtifactID)
			case "audio":
				result.AudioArtifactIDs = append(result.AudioArtifactIDs, content.ArtifactID)
			case "transcript":
				// Attached subtitle files land as trc_ transcript artifacts;
				// plain .txt transcripts are not editor material.
				if subtitleArtifactExtension(content) != "" {
					result.SubtitleArtifactIDs = append(result.SubtitleArtifactIDs, content.ArtifactID)
				}
			case "markdown":
				if markdownArtifactExtension(content) != "" {
					result.MarkdownArtifactIDs = append(result.MarkdownArtifactIDs, content.ArtifactID)
				}
			}
		}
	}
	return result, nil
}
