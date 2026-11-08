package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// currentConversationSchemaVersion is bumped whenever HistoryConversation gains
// a field or changes shape. loadForAppend backfills older records up to it.
const currentConversationSchemaVersion = 2

// defaultImageAspectRatio / defaultImageSizePreset are the defaults for the
// image-generation Settings dropdowns. The size preset is a resolution tier
// ("1k", "2k", "4k") — pixels are derived from it at call time (see
// imageSizeForPresetAndRatio in tools_registry.go) and fal models that speak
// tiers natively are told the tier itself (see resolveImageBody), so config
// carries the user's choice rather than raw pixel values.
const (
	defaultImageAspectRatio = "1:1"
	defaultImageSizePreset  = "1k"
)

// resolveTurnWorkspace returns the filesystem-tool root a chat turn must run
// against. The workspace is IMMUTABLE per conversation:
//
//   - Turn 1 (no ConversationID): the request's Workspace wins if set, else
//     the configured default root. The resolved value is what gets persisted
//     onto the new conversation record by writePendingChatConversation.
//   - Turn 2+ (ConversationID set): the value already on the record wins. The
//     request's Workspace is ignored. Legacy SchemaVersion 1 records are
//     backfilled to the configured default.
//
// The caller overrides config.Tools.Filesystem.Root with this value before
// constructing the HarnessEngine, so the registry (cached per engine), the
// filesystem tool layer, the triage prompt, and the planner prompt all see the
// conversation's root through h.config — one override point, no per-callsite
// branching. Returns the empty string only if both the request and the
// configured default are empty, in which case the filesystem layer falls back
// to defaultAppConfig() at resolve time (see fs_tools.go resolvePath).
func resolveTurnWorkspace(config AppConfig, req ChatRequest) (string, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		if ws := strings.TrimSpace(req.Workspace); ws != "" {
			return ws, nil
		}
		return config.Tools.Filesystem.Root, nil
	}
	return readConversationWorkspace(config.Storage, req.ConversationID, config.Tools.Filesystem.Root)
}

// resolveTurnProject validates the project a NEW conversation is being assigned
// to. Like resolveTurnWorkspace it only reads the request on turn 1 — for an
// existing conversation the record's ProjectID is already the truth and the
// request field is ignored — but unlike the workspace there is nothing to
// resolve: an empty projectID means standalone, a non-empty one must reference
// an existing project or the turn fails before anything is persisted. A wrong
// ID is a UI bug (the composer only offers real projects), so failing loudly
// beats silently filing the conversation as standalone.
func resolveTurnProject(config AppConfig, req ChatRequest) (string, error) {
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" || strings.TrimSpace(req.ConversationID) != "" {
		return "", nil
	}
	if _, _, err := findProject(config.Storage, projectID); err != nil {
		return "", err
	}
	return projectID, nil
}

// projectNotesMaxChars bounds the instruction block injected into the planner
// prompt — the same order as plainTranscriptArtifactChars. A longer note is
// capped at injection, never rejected: the project record keeps everything
// the user wrote, the prompt keeps what fits.
const projectNotesMaxChars = 4000

// resolveTurnProjectNotes resolves the instruction block of the project the
// turn's conversation belongs to, so project-level conventions ("render all
// video requests at 9:16 in pixar style") reach the harness prompts through
// req.ProjectNotes. Turn 1 reads the request's ProjectID (already validated by
// resolveTurnProject); turn 2+ ignores it and reads the record's, like every
// other per-conversation fact. Unlike the workspace, the notes are re-read
// LIVE on every turn: the block is editable at the project level and a moved
// conversation must follow its new project (MoveConversationToProject), so
// pinning it at creation would freeze stale guidance. Fail-soft throughout —
// an unreadable record or a missing project yields no note rather than a
// failed turn (deleteProject removes member conversations, so the miss path is
// corruption or a racing delete, not a user state worth failing on).
func resolveTurnProjectNotes(config AppConfig, req ChatRequest) string {
	projectID := strings.TrimSpace(req.ProjectID)
	if strings.TrimSpace(req.ConversationID) != "" {
		projectID = readConversationProjectID(config.Storage, req.ConversationID)
	}
	if projectID == "" {
		return ""
	}
	project, _, err := findProject(config.Storage, projectID)
	if err != nil {
		return ""
	}
	// The no-ellipsis variant: the cap is the whole block's budget, so the
	// injected section never grows a suffix beyond projectNotesMaxChars.
	return truncateRunesEllipsis(strings.TrimSpace(project.Notes), projectNotesMaxChars, "")
}

// readConversationProjectID loads a conversation record solely to read its
// project membership — the same second small JSON read pattern as
// readConversationWorkspace (loadForAppend reads the record again during the
// append). Empty on any failure: the caller treats a miss as "no project
// note", never an error.
func readConversationProjectID(storage ConfigStorage, conversationID string) string {
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ""
	}
	var conversation HistoryConversation
	if err := readJSONFile(path, &conversation); err != nil {
		return ""
	}
	return strings.TrimSpace(conversation.ProjectID)
}

// readConversationWorkspace loads a conversation record solely to read its
// workspace root. Legacy SchemaVersion 1 records (no Workspace) are backfilled
// to defaultWorkspace so a conversation resumed for the first time after the
// per-conversation-workspace upgrade resolves consistently. This does a second
// read of conversation.json that loadForAppend will read again during the
// append; both are small JSON reads at turn-start, not per-round.
func readConversationWorkspace(storage ConfigStorage, conversationID, defaultWorkspace string) (string, error) {
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		return "", err
	}
	var conversation HistoryConversation
	if err := readJSONFile(path, &conversation); err != nil {
		return "", err
	}
	if conversation.SchemaVersion < 2 && strings.TrimSpace(conversation.Workspace) == "" {
		return defaultWorkspace, nil
	}
	if ws := strings.TrimSpace(conversation.Workspace); ws != "" {
		return ws, nil
	}
	return defaultWorkspace, nil
}

func writeChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, title string, run HarnessRun) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", err
	}

	userPrompt := lastUserPrompt(req.Messages)
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            workspace.ID,
		Kind:          "chat",
		Title:         normalizeConversationTitle(title, userPrompt),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		Provider: HistoryProvider{
			ID:      "ollama",
			BaseURL: config.Providers.Ollama.BaseURL,
		},
		Defaults: HistoryDefaults{
			ChatModel: req.Model,
			System:    req.System,
		},
		Stats: HistoryConversationStats{
			TurnCount:     2,
			ArtifactCount: countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)}),
		},
		Workspace:      config.Tools.Filesystem.Root,
		ProjectID:      strings.TrimSpace(req.ProjectID),
		ModelOverrides: requestModelOverrides(req),
	}

	userTurn, assistantTurn, _, err := buildChatTurnPair(workspace.ID, 1, nowText, req, assistantContent, assistantThinking, model, provider, reason, tokens, workspace.ArtifactsDir, config.Storage, config, run, strings.TrimSpace(req.ProjectID))
	if err != nil {
		return "", err
	}

	if err := store.writeSnapshot(workspace, conversation, userTurn, assistantTurn); err != nil {
		return "", err
	}
	return workspace.ID, nil
}

// writePendingChatConversation also returns the persisted video attachments'
// hydrated "/atelier-artifact" URLs (in attachment order) so the streaming
// start result can hand the live transcript artifact srcs — the poster-frame
// swap the composer's data: URLs cannot carry.
func writePendingChatConversation(config AppConfig, req ChatRequest) (string, []string, []string, []string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", nil, nil, nil, err
	}

	userPrompt := lastUserPrompt(req.Messages)
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            workspace.ID,
		Kind:          "chat",
		Title:         titleFromPrompt(userPrompt),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		Provider: HistoryProvider{
			ID:      "ollama",
			BaseURL: config.Providers.Ollama.BaseURL,
		},
		Defaults: HistoryDefaults{
			ChatModel: req.Model,
			System:    req.System,
		},
		Stats: HistoryConversationStats{
			TurnCount:     1,
			ArtifactCount: countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)}),
		},
		Workspace:      config.Tools.Filesystem.Root,
		ProjectID:      strings.TrimSpace(req.ProjectID),
		ModelOverrides: requestModelOverrides(req),
	}
	userTurn, videoURLs, subtitleURLs, markdownURLs, err := buildChatUserTurn(workspace.ID, 1, nowText, req, workspace.ArtifactsDir, config.Storage, config, strings.TrimSpace(req.ProjectID))
	if err != nil {
		return "", nil, nil, nil, err
	}

	if err := store.writeSnapshot(workspace, conversation, userTurn); err != nil {
		return "", nil, nil, nil, err
	}
	return workspace.ID, videoURLs, subtitleURLs, markdownURLs, nil
}

func buildChatTurnPair(conversationID string, firstTurnNumber int, createdAt string, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, artifactsDir string, storage ConfigStorage, config AppConfig, run HarnessRun, projectID string) (HistoryTurn, HistoryTurn, []string, error) {
	userTurn, _, _, _, err := buildChatUserTurn(conversationID, firstTurnNumber, createdAt, req, artifactsDir, storage, config, projectID)
	if err != nil {
		return HistoryTurn{}, HistoryTurn{}, nil, err
	}
	assistantTurn := buildChatAssistantTurn(conversationID, firstTurnNumber+1, createdAt, assistantContent, assistantThinking, model, provider, reason, tokens, run, "")
	return userTurn, assistantTurn, nil, nil
}

// buildChatUserTurn persists the user's turn. projectID is the conversation's
// project — req.ProjectID on the creation paths (turn 1, before the record
// exists) and the loaded record's ProjectID on appends — so @-mention
// resolution can widen into the library even on the very first turn, when the
// conversation being built is not yet on disk for getConversation to read.
func buildChatUserTurn(conversationID string, turnNumber int, createdAt string, req ChatRequest, artifactsDir string, storage ConfigStorage, config AppConfig, projectID string) (HistoryTurn, []string, []string, []string, error) {
	userContent, videoURLs, err := historyContentForMessage(lastUserMessage(req.Messages), artifactsDir, config)
	if err != nil {
		return HistoryTurn{}, nil, nil, nil, err
	}
	// Attached subtitles' live-render URLs, derived BEFORE the mention append
	// below: they map the composer attachments positionally (what the frontend
	// entry carries), not the whole turn content.
	subtitleURLs := subtitleAttachmentURLs(userContent, artifactsDir)
	// Attached markdown documents' URLs, derived the same way and for the same
	// reason (the Save binding reads artifact paths only).
	markdownURLs := markdownAttachmentURLs(userContent, artifactsDir)
	// @-mentioned assets are persisted as references to the existing artifact
	// entries — same ArtifactID and relative Path, no byte copy — so the next
	// turn's latest-wins history walk sees what the user cited rather than an
	// older artifact. Mentions of library assets from OTHER conversations
	// persist with an absolute Path (see resolveReferencedAssets). On a
	// brand-new conversation there is nothing local to resolve, but a project
	// context can still resolve library-wide mentions.
	if len(req.ReferencedAssetIDs) > 0 {
		userContent = append(userContent, resolveReferencedAssets(storage, conversationID, projectID, req.ReferencedAssetIDs).entries...)
	}
	userTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", turnNumber),
		ConversationID: conversationID,
		CreatedAt:      createdAt,
		Kind:           "chat",
		Role:           "user",
		Content:        userContent,
		Request: map[string]any{
			"model": req.Model,
		},
	}
	if req.Options != nil {
		userTurn.Request["options"] = req.Options
	}
	if req.Think != nil {
		userTurn.Request["think"] = req.Think
	}
	if selectedModel := strings.TrimSpace(req.SelectedModel); selectedModel != "" && selectedModel != req.Model {
		userTurn.Request["selectedModel"] = selectedModel
	}
	return userTurn, videoURLs, subtitleURLs, markdownURLs, nil
}

// subtitleAttachmentURLs maps a user turn's attached-subtitle content entries
// (the trc_<hex>.vtt/.srt artifacts writeSubtitleAttachment persisted) onto
// the "/atelier-artifact" URLs the live UI swaps the just-attached data URLs
// for — the positional counterpart of the returned video URLs. Mentioned
// entries (appended by the caller after this derivation) never ride it: they
// are not composer attachments.
func subtitleAttachmentURLs(contents []HistoryContent, artifactsDir string) []string {
	urls := []string{}
	for _, content := range contents {
		if subtitleArtifactExtension(content) == "" {
			continue
		}
		path := filepath.FromSlash(content.Path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(artifactsDir, filepath.Base(path))
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		urls = append(urls, artifactPrefix+abs)
	}
	return urls
}

// markdownAttachmentURLs is the document twin of subtitleAttachmentURLs: the
// just-persisted md_<hex>.md entries mapped onto their artifact URLs, so the
// live transcript can swap the composer's data URLs the moment the turn
// persists.
func markdownAttachmentURLs(contents []HistoryContent, artifactsDir string) []string {
	urls := []string{}
	for _, content := range contents {
		if markdownArtifactExtension(content) == "" {
			continue
		}
		path := filepath.FromSlash(content.Path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(artifactsDir, filepath.Base(path))
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		urls = append(urls, artifactPrefix+abs)
	}
	return urls
}

// harnessRunCostMicros totals a run's USD-millionth cost: every model-call
// step's server-billed cost plus every media generation's estimate. It
// mirrors how the frontend folds token usage (sum over steps, one row per
// model call) so providerResponse.costMicros and the UI's per-step rows can
// never disagree. Bookkeeping steps carry no cost, same as tokens.
func harnessRunCostMicros(run HarnessRun) int64 {
	var total int64
	for _, step := range run.Steps {
		total += step.CostMicros
		for _, activity := range step.Tools {
			total += activity.CostMicros
		}
	}
	return total
}

func buildChatAssistantTurn(conversationID string, turnNumber int, createdAt string, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun, errorText string) HistoryTurn {
	assistantContents := []HistoryContent{{Type: "text", Text: assistantContent}}
	if strings.TrimSpace(assistantThinking) != "" {
		assistantContents = append(assistantContents, HistoryContent{Type: "thinking", Text: assistantThinking})
	}
	providerResponse := map[string]any{
		"doneReason": reason,
		"harnessRun": run,
		"tokens":     tokens,
	}
	// The turn's money total rides beside the token total, derived from the
	// run (model-call steps + media activities) rather than threaded through
	// the streaming result — the run is the single ledger everything else
	// already renders from, and failed turns keep their burned cost too.
	if costMicros := harnessRunCostMicros(run); costMicros > 0 {
		providerResponse["costMicros"] = costMicros
	}
	if strings.TrimSpace(errorText) != "" {
		providerResponse["error"] = errorText
	}
	if run.Skill != nil {
		providerResponse["skill"] = run.Skill
	}
	return HistoryTurn{
		SchemaVersion:    1,
		ID:               fmt.Sprintf("turn_%06d", turnNumber),
		ConversationID:   conversationID,
		CreatedAt:        createdAt,
		Kind:             "chat",
		Role:             "assistant",
		Model:            model,
		Provider:         provider,
		Content:          assistantContents,
		ProviderResponse: providerResponse,
	}
}

func appendChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun) (string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return "", err
	}
	nowText := time.Now().Format(time.RFC3339)
	userTurn, assistantTurn, _, err := buildChatTurnPair(conversationID, loaded.NextTurnNumber, nowText, req, assistantContent, assistantThinking, model, provider, reason, tokens, loaded.ArtifactsDir, config.Storage, config, run, loaded.Conversation.ProjectID)
	if err != nil {
		return "", err
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount += 2
	loaded.Conversation.Stats.ArtifactCount += countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)})
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, userTurn); err != nil {
		return "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return "", err
	}
	return conversationID, nil
}

// appendChatUserTurn also returns the persisted video attachments' hydrated
// "/atelier-artifact" URLs (in attachment order) — see writePendingChatConversation.
func appendChatUserTurn(config AppConfig, req ChatRequest) (string, []string, []string, []string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return "", nil, nil, nil, err
	}
	nowText := time.Now().Format(time.RFC3339)
	userTurn, videoURLs, subtitleURLs, markdownURLs, err := buildChatUserTurn(conversationID, loaded.NextTurnNumber, nowText, req, loaded.ArtifactsDir, config.Storage, config, loaded.Conversation.ProjectID)
	if err != nil {
		return "", nil, nil, nil, err
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)})
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return "", nil, nil, nil, err
	}
	if err := store.writeTurn(loaded.TurnsDir, userTurn); err != nil {
		return "", nil, nil, nil, err
	}
	return conversationID, videoURLs, subtitleURLs, markdownURLs, nil
}

func appendChatAssistantTurn(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun, errorText string) error {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return err
	}
	nowText := time.Now().Format(time.RFC3339)
	assistantTurn := buildChatAssistantTurn(conversationID, loaded.NextTurnNumber, nowText, assistantContent, assistantThinking, model, provider, reason, tokens, run, errorText)

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return err
	}
	return store.writeTurn(loaded.TurnsDir, assistantTurn)
}

// chatAssistantTurnMedia is the per-call piece a media-bearing assistant turn
// needs. build writes that turn's artifacts (any mix of images, videos,
// audios, transcripts) into artifactsDir, and returns the HistoryContent
// entries they become, the per-kind live-render URLs, and the tool metadata
// recorded on the saved turn. The shared append helper calls build after
// loading the conversation, since artifact writing needs the resolved
// ArtifactsDir.
type chatAssistantTurnMedia struct {
	build func(artifactsDir string) (chatAssistantTurnMediaOutput, error)
}

// chatTurnMedia carries every media kind a turn produced into the shared
// appender. Kinds the turn didn't produce stay empty; the builder writes only
// what is present, so any combination persists in one turn — which the local
// ffmpeg tools make reachable (a screenshot plus an extracted audio track in
// one turn, say) and the earlier one-kind-per-switch appenders would have
// dropped.
type chatTurnMedia struct {
	images   []string
	imageReq ImageGenerateRequest
	// imageRaw is the direct image-generation path's raw provider JSON,
	// recorded as rawCompact on the tool metadata.
	imageRaw             string
	videos               []ToolVideoFile
	videoReq             VideoGenerateRequest
	audios               []ToolAudioFile
	audioReq             AudioGenerateRequest
	transcripts          []ToolTranscriptFile
	transcriptModel      string
	transcriptTimestamps string
	// markdowns carries the document authoring pass's documents
	// (document_authoring.go): bytes persisted as md_ artifacts through
	// writeMarkdownAttachment, the same identity user-attached documents and
	// markdown-editor saves ride.
	markdowns []chatTurnMarkdown
}

// chatTurnMarkdown is one model-authored document staged for the turn save.
type chatTurnMarkdown struct {
	data      []byte
	extension string
}

// chatTurnMediaURLs holds the "/atelier-artifact" links per media kind; the
// live UI renders each kind in its own player, so they cannot share one list.
type chatTurnMediaURLs struct {
	videos      []string
	audios      []string
	transcripts []string
	markdowns   []string
}

// chatAssistantTurnMediaOutput is what a media build closure produced: the
// history content entries (all kinds, in order), the per-kind artifact URLs,
// and the providerResponse.tool metadata.
type chatAssistantTurnMediaOutput struct {
	contents []HistoryContent
	urls     chatTurnMediaURLs
	tool     map[string]any
}

// appendChatAssistantTurnWithMedia is the shared skeleton for persisting an
// assistant turn that produced media. It loads the conversation, delegates
// artifact writing + metadata assembly to media.build, then writes a single
// assistant turn whose ProviderResponse carries the tool metadata.
func appendChatAssistantTurnWithMedia(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, run HarnessRun, media chatAssistantTurnMedia) (chatTurnMediaURLs, error) {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return chatTurnMediaURLs{}, err
	}
	nowText := time.Now().Format(time.RFC3339)

	output, err := media.build(loaded.ArtifactsDir)
	if err != nil {
		return chatTurnMediaURLs{}, err
	}

	contents := []HistoryContent{{Type: "text", Text: assistantContent}}
	if strings.TrimSpace(assistantThinking) != "" {
		contents = append(contents, HistoryContent{Type: "thinking", Text: assistantThinking})
	}
	contents = append(contents, output.contents...)
	providerResponse := map[string]any{
		"doneReason": reason,
		"harnessRun": run,
		"tool":       output.tool,
	}
	// Same turn-total derivation as buildChatAssistantTurn: the run is the
	// ledger, and a media turn's dollar total rides beside the run just like a
	// text turn's (the frontend folds the run itself, but history stays
	// self-describing for inspection).
	if costMicros := harnessRunCostMicros(run); costMicros > 0 {
		providerResponse["costMicros"] = costMicros
	}
	assistantTurn := HistoryTurn{
		SchemaVersion:    1,
		ID:               fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID:   conversationID,
		CreatedAt:        nowText,
		Kind:             "chat",
		Role:             "assistant",
		Model:            model,
		Provider:         provider,
		Content:          contents,
		ProviderResponse: providerResponse,
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(output.contents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return chatTurnMediaURLs{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return chatTurnMediaURLs{}, err
	}
	return output.urls, nil
}

// appendChatAssistantTurnWithTurnMedia persists an assistant turn that
// produced any combination of media kinds. Every kind present is written into
// the one turn — images, videos, audios, transcripts — with the tool metadata
// naming the dominant kind (video > audio > image > transcript) plus every
// kind's count. The single-kind appenders below are thin conveniences over
// this one.
func appendChatAssistantTurnWithTurnMedia(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, media chatTurnMedia, run HarnessRun) (chatTurnMediaURLs, error) {
	return appendChatAssistantTurnWithMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, run, chatAssistantTurnMedia{
		build: func(artifactsDir string) (chatAssistantTurnMediaOutput, error) {
			var contents []HistoryContent
			urls := chatTurnMediaURLs{}
			tool := map[string]any{}
			if len(media.videos) > 0 {
				videoContents, videoURLs, err := writeChatVideoArtifacts(config, artifactsDir, media.videos)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, videoContents...)
				urls.videos = videoURLs
				tool["name"] = "video_generation"
				tool["model"] = media.videoReq.Model
				tool["videoCount"] = len(videoContents)
			}
			if len(media.audios) > 0 {
				audioContents, audioURLs, err := writeChatAudioArtifacts(config, artifactsDir, media.audios)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, audioContents...)
				urls.audios = audioURLs
				if tool["name"] == nil {
					tool["name"] = "audio_generation"
					tool["model"] = media.audioReq.Model
				}
				tool["audioCount"] = len(audioContents)
			}
			if len(media.images) > 0 {
				imageContents, err := writeChatImageArtifacts(artifactsDir, media.imageReq, media.images)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, imageContents...)
				if tool["name"] == nil {
					tool["name"] = "image_generation"
					tool["model"] = media.imageReq.Model
					if media.imageRaw != "" {
						tool["rawCompact"] = media.imageRaw
					}
				}
				tool["imageCount"] = len(imageContents)
			}
			if len(media.transcripts) > 0 {
				transcriptContents, transcriptURLs, err := writeChatTranscriptArtifacts(config, artifactsDir, media.transcripts)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, transcriptContents...)
				urls.transcripts = transcriptURLs
				if tool["name"] == nil {
					tool["name"] = "audio_transcription"
					tool["model"] = media.transcriptModel
					tool["timestamps"] = media.transcriptTimestamps
				}
				tool["transcripts"] = len(transcriptContents)
			}
			if len(media.markdowns) > 0 {
				if err := os.MkdirAll(artifactsDir, 0755); err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				absArtifactsDir, err := filepath.Abs(artifactsDir)
				if err != nil {
					absArtifactsDir = artifactsDir
				}
				for _, doc := range media.markdowns {
					content, err := writeMarkdownAttachment(artifactsDir, doc.data, doc.extension)
					if err != nil {
						return chatAssistantTurnMediaOutput{}, err
					}
					contents = append(contents, content)
					urls.markdowns = append(urls.markdowns, artifactPrefix+filepath.Join(absArtifactsDir, content.ArtifactID+doc.extension))
				}
				if tool["name"] == nil {
					tool["name"] = "document_authoring"
				}
				tool["documentCount"] = len(media.markdowns)
			}
			return chatAssistantTurnMediaOutput{contents: contents, urls: urls, tool: tool}, nil
		},
	})
}

func appendChatAssistantTurnWithImages(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, images []string, raw string, run HarnessRun, imageReq ImageGenerateRequest) error {
	_, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{images: images, imageReq: imageReq, imageRaw: raw}, run)
	return err
}

// appendChatAssistantTurnWithVideos persists an assistant turn that produced one
// or more generated videos (and, in the rare case a turn produced both, any
// images too). Video temp files are moved into the conversation's artifacts
// directory; the returned URLs are the "/atelier-artifact" links the live UI
// renders before the turn is reloaded from history.
func appendChatAssistantTurnWithVideos(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, images []string, imageReq ImageGenerateRequest, videos []ToolVideoFile, videoReq VideoGenerateRequest, run HarnessRun) ([]string, error) {
	urls, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{
		images: images, imageReq: imageReq, videos: videos, videoReq: videoReq,
	}, run)
	return urls.videos, err
}

// writeChatVideoArtifacts moves each generated video's temp file into the
// artifacts directory as vid_<hex>.<ext> and returns the history content
// entries plus the "/atelier-artifact" URLs the live UI renders. A temp file
// that can't be resolved to a video is skipped rather than aborting the whole
// turn save. Thin wrapper over the shared media-artifact writer.
func writeChatVideoArtifacts(config AppConfig, artifactsDir string, videos []ToolVideoFile) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(videos))
	for i, v := range videos {
		files[i] = mediaArtifactEntry{tempPath: v.TempPath, mimeType: v.MimeType}
	}
	return writeChatMediaArtifacts(config, artifactsDir, files, "vid", "video", videoExtensionForMediaType)
}

// writeChatMediaArtifacts is the shared body for moving generated video/audio
// temp files into a conversation's artifacts directory. Each file is named
// <kindTag>_<hex>.<ext> — a globally unique randomID, because artifact IDs
// double as filenames and asset references that must not collide across
// conversations (legacy records used per-conversation positional IDs like
// turn_000001_aud_000001). It returns the HistoryContent entries and the
// "/atelier-artifact" URLs the live UI renders before reload. A file with
// no temp path is skipped rather than aborting the whole turn save. The
// contentType ("video"/"audio") and extensionFn differ per media kind.
type mediaArtifactEntry struct {
	tempPath string
	mimeType string
}

func writeChatMediaArtifacts(config AppConfig, artifactsDir string, files []mediaArtifactEntry, kindTag, contentType string, extensionFn func(string) string) ([]HistoryContent, []string, error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, nil, err
	}
	absArtifactsDir, err := filepath.Abs(artifactsDir)
	if err != nil {
		return nil, nil, err
	}
	contents := make([]HistoryContent, 0, len(files))
	urls := make([]string, 0, len(files))
	for _, file := range files {
		tempPath := strings.TrimSpace(file.tempPath)
		if tempPath == "" {
			continue
		}
		extension := extensionFn(file.mimeType)
		artifactID := randomID(kindTag)
		filename := artifactID + extension
		destPath := filepath.Join(absArtifactsDir, filename)
		if err := moveFile(tempPath, destPath); err != nil {
			return nil, nil, err
		}
		if contentType == "video" {
			// Poster frame for the <video> poster/panel placeholder
			// (video_poster.go). Best-effort: a missing poster degrades the
			// thumbnail, never the save.
			generateVideoPoster(config, destPath)
		}
		contents = append(contents, HistoryContent{
			Type:       contentType,
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
		urls = append(urls, artifactPrefix+destPath)
	}
	return contents, urls, nil
}

// appendChatAssistantTurnWithAudios persists an assistant turn that produced one
// or more generated audio clips. Audio temp files are moved into the
// conversation's artifacts directory; the returned URLs are the
// "/atelier-artifact" links the live UI renders before reload.
func appendChatAssistantTurnWithAudios(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, audios []ToolAudioFile, audioReq AudioGenerateRequest, run HarnessRun) ([]string, error) {
	urls, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{
		audios: audios, audioReq: audioReq,
	}, run)
	return urls.audios, err
}

// writeChatAudioArtifacts moves each generated audio's temp file into the
// artifacts directory as aud_<hex>.<ext> and returns the history
// content entries plus the "/atelier-artifact" URLs the live UI renders. Thin
// wrapper over the shared media-artifact writer, mirroring writeChatVideoArtifacts.
func writeChatAudioArtifacts(config AppConfig, artifactsDir string, audios []ToolAudioFile) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(audios))
	for i, a := range audios {
		files[i] = mediaArtifactEntry{tempPath: a.TempPath, mimeType: a.MimeType}
	}
	return writeChatMediaArtifacts(config, artifactsDir, files, "aud", "audio", audioExtensionForMediaType)
}

// writeChatTranscriptArtifacts moves each transcript's temp file into the
// artifacts directory as trc_<hex>.<ext> and returns the history content
// entries plus the "/atelier-artifact" URLs the live UI renders. The extension
// follows the staged file's mime type — .vtt for the WebVTT rendering of a
// timestamped transcript, .txt for an oversized plain transcript. Thin wrapper
// over the shared media-artifact writer, mirroring writeChatAudioArtifacts.
func writeChatTranscriptArtifacts(config AppConfig, artifactsDir string, transcripts []ToolTranscriptFile) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(transcripts))
	for i, t := range transcripts {
		files[i] = mediaArtifactEntry{tempPath: t.TempPath, mimeType: t.MimeType}
	}
	return writeChatMediaArtifacts(config, artifactsDir, files, "trc", "transcript", transcriptExtensionForMimeType)
}

// transcriptExtensionForMimeType maps a staged transcript's mime type onto its
// artifact extension — .vtt for WebVTT, .srt for SubRip (convert_subtitles'
// output), .txt for anything else (the tool layer stages plain text with
// text/plain).
func transcriptExtensionForMimeType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "text/vtt":
		return ".vtt"
	case "application/x-subrip":
		return ".srt"
	default:
		return ".txt"
	}
}

// appendChatAssistantTurnWithTranscripts persists an assistant turn that
// produced one or more transcript artifacts (.vtt timestamped or .txt plain).
// Transcript temp files are moved into the conversation's artifacts directory
// as trc_<hex>.<ext>; the returned URLs are the "/atelier-artifact" links the
// live UI renders before the turn is reloaded from history.
func appendChatAssistantTurnWithTranscripts(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, transcripts []ToolTranscriptFile, transcriptModel, timestamps string, run HarnessRun) ([]string, error) {
	urls, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{
		transcripts: transcripts, transcriptModel: transcriptModel, transcriptTimestamps: timestamps,
	}, run)
	return urls.transcripts, err
}

// moveFile relocates src to dst, falling back to a copy-and-delete when the two
// live on different filesystems (os.Rename fails with a cross-device error).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

func writeChatImageArtifacts(artifactsDir string, req ImageGenerateRequest, images []string) ([]HistoryContent, error) {
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, err
	}
	imageContents := make([]HistoryContent, 0, len(images))
	for _, image := range images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			// Skip entries that aren't decodable image bytes (e.g. a stray http
			// URL that slipped past the harvest step) rather than aborting the
			// whole turn save and orphaning any artifacts already written.
			continue
		}
		artifactID := randomID("img")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, err
		}
		imageContents = append(imageContents, HistoryContent{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
			Width:      req.Width,
			Height:     req.Height,
		})
	}
	return imageContents, nil
}

// historyContentForMessage persists a message's attachments as artifacts and
// returns the content entries plus, for video attachments, their hydrated
// "/atelier-artifact" URLs in attachment order — the swap-in srcs the live
// transcript uses to replace the composer's data: URLs the moment the turn
// persists (a data URL can carry no poster; the artifact URL can, and it
// drops the multi-MB base64 payload from the UI state).
func historyContentForMessage(message ChatMessage, artifactsDir string, config AppConfig) ([]HistoryContent, []string, error) {
	contents := []HistoryContent{}
	videoURLs := []string{}
	if strings.TrimSpace(message.Content) != "" {
		contents = append(contents, HistoryContent{Type: "text", Text: message.Content})
	}
	if len(message.Images) > 0 || len(message.Audios) > 0 || len(message.Videos) > 0 || len(message.Subtitles) > 0 || len(message.Markdowns) > 0 {
		if err := os.MkdirAll(artifactsDir, 0755); err != nil {
			return nil, nil, err
		}
	}
	for _, image := range message.Images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			return nil, nil, err
		}
		artifactID := randomID("img")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, nil, err
		}
		contents = append(contents, HistoryContent{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
	}
	for _, audio := range message.Audios {
		// A staged upload is linked into the artifacts dir — no decode, no
		// second copy of a potentially very large payload; the staged entry
		// stays so the request's own payload keeps resolving for the tools
		// (media_upload.go).
		if stagedPath, isStaged := stagedUploadPath(audio, mediaUploadsDir(config.Storage)); isStaged {
			content, _, err := adoptStagedUpload(config, stagedPath, artifactsDir, "aud")
			if err != nil {
				return nil, nil, err
			}
			contents = append(contents, content)
			continue
		}
		data, extension, err := decodeAudioPayload(audio)
		if err != nil {
			return nil, nil, err
		}
		artifactID := randomID("aud")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, nil, err
		}
		contents = append(contents, HistoryContent{
			Type:       "audio",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
	}
	for _, video := range message.Videos {
		// Staged-upload adoption, same as audio above.
		if stagedPath, isStaged := stagedUploadPath(video, mediaUploadsDir(config.Storage)); isStaged {
			content, artifactPath, err := adoptStagedUpload(config, stagedPath, artifactsDir, "vid")
			if err != nil {
				return nil, nil, err
			}
			contents = append(contents, content)
			absArtifactPath, err := filepath.Abs(artifactPath)
			if err != nil {
				absArtifactPath = artifactPath
			}
			videoURLs = append(videoURLs, artifactPrefix+absArtifactPath)
			continue
		}
		data, extension, err := decodeVideoPayload(video)
		if err != nil {
			return nil, nil, err
		}
		artifactID := randomID("vid")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, nil, err
		}
		// Poster frame beside an attached clip, same as generated ones
		// (video_poster.go). Best-effort — attachment persistence never
		// depends on a thumbnail extractor resolving.
		generateVideoPoster(config, artifactPath)
		absArtifactPath, err := filepath.Abs(artifactPath)
		if err != nil {
			absArtifactPath = artifactPath
		}
		videoURLs = append(videoURLs, artifactPrefix+absArtifactPath)
		contents = append(contents, HistoryContent{
			Type:       "video",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
	}
	for _, subtitle := range message.Subtitles {
		data, err := decodeSubtitlePayload(subtitle)
		if err != nil {
			return nil, nil, err
		}
		content, err := writeSubtitleAttachment(artifactsDir, data)
		if err != nil {
			return nil, nil, err
		}
		contents = append(contents, content)
	}
	for _, markdown := range message.Markdowns {
		data, mime, err := decodeMediaDataURL(markdown)
		if err != nil {
			return nil, nil, err
		}
		content, err := writeMarkdownAttachment(artifactsDir, data, markdownExtensionForMime(mime))
		if err != nil {
			return nil, nil, err
		}
		contents = append(contents, content)
	}
	if len(contents) == 0 {
		return []HistoryContent{{Type: "text", Text: ""}}, nil, nil
	}
	return contents, videoURLs, nil
}

// decodeSubtitlePayload reads an attached subtitle file's bytes from a data
// URL (or an /atelier-artifact path, the hydrated form a later turn can
// re-carry). decodeMediaDataURL is the shared decoder.
func decodeSubtitlePayload(subtitle string) ([]byte, error) {
	data, _, err := decodeMediaDataURL(subtitle)
	return data, err
}

// writeSubtitleAttachment persists subtitle bytes as a trc_<hex>.<ext>
// transcript artifact — the identity convert_subtitles reads, the chat chips
// render, and the assets panel lists. The bytes must parse as actual cue
// content (detectSubtitleFormat alone would accept any non-WebVTT bytes as
// SRT), and the extension follows the CONTENT, not the declared mime: a
// mislabeled upload still lands as the format its bytes actually are.
func writeSubtitleAttachment(artifactsDir string, data []byte) (HistoryContent, error) {
	_, format, _, err := parseSubtitles(data)
	if err != nil {
		return HistoryContent{}, fmt.Errorf("attached file is not VTT or SRT subtitle content: %w", err)
	}
	extension := map[string]string{subtitleFormatVTT: ".vtt", subtitleFormatSRT: ".srt"}[format]
	artifactID := randomID("trc")
	filename := artifactID + extension
	if err := os.WriteFile(filepath.Join(artifactsDir, filename), data, 0644); err != nil {
		return HistoryContent{}, err
	}
	return HistoryContent{
		Type:       "transcript",
		ArtifactID: artifactID,
		Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
		MimeType:   mediaTypeForExtension(extension),
	}, nil
}

// countMessageAttachments counts image, audio, video, subtitle, and markdown
// attachments across messages for conversation artifact stats. Audio and video
// are included so the artifact count reflects user-attached media, not just images.
func countMessageAttachments(messages []ChatMessage) int {
	count := 0
	for _, message := range messages {
		count += len(message.Images)
		count += len(message.Audios)
		count += len(message.Videos)
		count += len(message.Subtitles)
		count += len(message.Markdowns)
	}
	return count
}
