package main

import (
	"encoding/json"
)

type OllamaStatus struct {
	Online  bool   `json:"online"`
	Version string `json:"version,omitempty"`
	BaseURL string `json:"baseURL"`
	Error   string `json:"error,omitempty"`
}

type OllamaModel struct {
	Name            string   `json:"name"`
	ModifiedAt      string   `json:"modifiedAt,omitempty"`
	Size            int64    `json:"size,omitempty"`
	Family          string   `json:"family,omitempty"`
	Parameter       string   `json:"parameter,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	ImageGeneration bool     `json:"imageGeneration,omitempty"`
	// SpeechGeneration marks a text-to-speech model (Ollama's "speech"
	// capability — phonon and friends), the Settings speech picker's filter
	// the way ImageGeneration is the image picker's.
	SpeechGeneration bool `json:"speechGeneration,omitempty"`
}

type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		ModifiedAt string `json:"modified_at"`
		Size       int64  `json:"size"`
		Details    struct {
			Family        string `json:"family"`
			ParameterSize string `json:"parameter_size"`
		} `json:"details"`
	} `json:"models"`
}

type ollamaShowResponse struct {
	Capabilities []string       `json:"capabilities"`
	ModelInfo    map[string]any `json:"model_info"`
	Details      struct {
		Family        string `json:"family"`
		ParameterSize string `json:"parameter_size"`
	} `json:"details"`
}

type ChatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
	// Audios carries user-attached audio as data URLs (data:audio/<fmt>;base64,...),
	// parallel to Images. Audio input is OpenRouter-only — the harness rejects an
	// audio attachment on any other provider before the turn runs (Ollama has no
	// audio input API). Like Images, the bytes are stripped before triage and the
	// OpenRouter adapter is the sole place they become input_audio content parts.
	Audios []string `json:"audios,omitempty"`
	// Videos carries user-attached video as data URLs (data:video/<fmt>;base64,...),
	// parallel to Audios. Unlike audio, video input is tool-only — it never
	// reaches a chat model: messagesWithoutMedia strips the bytes before triage
	// (and therefore before any model call), and no provider adapter emits a
	// video content part. Tools consume it via AttachedVideos (Veo extend, lip
	// sync). Bytes are validated with isVideoBytes before being persisted.
	Videos []string `json:"videos,omitempty"`
	// Subtitles carries user-attached subtitle files (.vtt/.srt) as data URLs,
	// parallel to Videos. Like video it is tool-only — no provider adapter
	// emits it as model input, and messagesWithoutMedia strips the bytes. The
	// turn persistence writes each file as a trc_<hex>.<ext> transcript
	// artifact, which is the identity convert_subtitles reads and the assets
	// panel and @-mentions list (subtitle.go).
	Subtitles []string `json:"subtitles,omitempty"`
	// Markdowns carries user-attached markdown documents (.md/.markdown) as
	// data URLs, parallel to Subtitles — tool-only, stripped before any model
	// call, each persisted as a md_<hex>.md artifact (markdown_editor.go) that
	// the markdown editor opens and the assets panel and @-mentions list.
	Markdowns []string   `json:"markdowns,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall mirrors the function-call shape a provider returns in
// message.tool_calls (and accepts in assistant message history). Both Ollama
// and OpenRouter use this shape, despite the Ollama-originated field name on
// the wire. Arguments is kept raw so the planner loop can validate before
// unmarshalling. The nested function payload is a named type (not an anonymous
// struct) so the Wails binding generator can mirror it into models.ts.
type ToolCall struct {
	Type     string       `json:"type,omitempty"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function payload of a tool call.
type ToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ChatRequest struct {
	RequestID      string         `json:"requestID,omitempty"`
	ConversationID string         `json:"conversationId,omitempty"`
	BaseURL        string         `json:"baseURL,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model"`
	SelectedModel  string         `json:"selectedModel,omitempty"`
	System         string         `json:"system,omitempty"`
	Messages       []ChatMessage  `json:"messages"`
	Think          any            `json:"think,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
	Format         any            `json:"format,omitempty"`
	// Tools is only set on planner calls when the harness model supports native
	// tool-calling. The final-response request (preparedResponseRequest) never
	// sets it, so the primary model stays tool-free.
	Tools []map[string]any `json:"tools,omitempty"`
	// Workspace selects the filesystem-tool root for a NEW conversation
	// (ConversationID empty). It is read once at creation and persisted onto
	// the conversation record, after which it is immutable. For an existing
	// conversation (turn 2+) this field is ignored — the record's Workspace
	// wins. Empty falls back to the configured default root.
	Workspace string `json:"workspace,omitempty"`
	// ProjectID assigns a NEW conversation (ConversationID empty) to a library
	// project. Read once at creation and persisted onto the conversation
	// record, immutable afterwards — same lifecycle as Workspace. For an
	// existing conversation the field is ignored; the record's ProjectID wins.
	// Must reference an existing project or the turn fails before anything is
	// persisted (resolveTurnProject). Empty means a standalone conversation.
	ProjectID string `json:"projectId,omitempty"`
	// ModelOverrides pins model selections for a NEW conversation
	// (ConversationID empty) — the turn-1 twin of the record's
	// HistoryConversation.ModelOverrides. Applied to the first turn's config
	// (applyConversationModelOverrides) and persisted onto the new record at
	// creation (requestModelOverrides), after which the record is the truth —
	// for an existing conversation this field is ignored. Unlike Workspace it
	// stays mutable afterwards via SetConversationModelOverrides.
	ModelOverrides *ConversationModelOverrides `json:"modelOverrides,omitempty"`
	// ReferencedAssetIDs carries the @-mentioned asset IDs for this turn —
	// ConversationAsset IDs from ListConversationAssets. The harness resolves
	// each ID to its artifact and delivers the media through the tool
	// attachment slots; while set, the latest-wins history fallback for
	// attachments is disabled, so the user's explicit references define the
	// turn's media. IDs only — no media bytes ever ride this field, so
	// nothing enters model context through it.
	ReferencedAssetIDs []string `json:"referencedAssetIds,omitempty"`
	// ProjectNotes carries the instruction block of the project this turn's
	// conversation belongs to, resolved server-side at turn start
	// (resolveTurnProjectNotes) and never sent by the frontend. It rides the
	// request because req already flows unchanged into the triage and planner
	// prompt builders; json:"-" keeps it off the wire and out of the frontend
	// bindings, since a client-set note would bypass the project record (the
	// single source the project editor writes).
	ProjectNotes string `json:"-"`
}

type ChatStreamStart struct {
	RequestID      string `json:"requestID"`
	ConversationID string `json:"conversationId"`
	// UserVideos carries the just-persisted user-turn video attachments'
	// hydrated "/atelier-artifact" URLs in attachment order, so the live
	// transcript can swap its composer data: URLs for artifact srcs the
	// moment the turn starts — the poster frame derives from the artifact
	// path, never a data URL, and the base64 payload leaves the UI state.
	UserVideos []string `json:"userVideos,omitempty"`
	// UserSubtitles carries the just-persisted user-turn subtitle attachments'
	// artifact URLs, positional like UserVideos. The Save binding reads
	// artifact paths only, so the swap is what makes a just-attached subtitle
	// downloadable without a reload.
	UserSubtitles []string `json:"userSubtitles,omitempty"`
	// UserMarkdowns carries the just-persisted user-turn markdown attachments'
	// artifact URLs, positional like UserSubtitles — the same swap, so a
	// just-attached document is downloadable and editable without a reload.
	UserMarkdowns []string `json:"userMarkdowns,omitempty"`
}

type ChatStreamEvent struct {
	RequestID string   `json:"requestID"`
	Content   string   `json:"content,omitempty"`
	Thinking  string   `json:"thinking,omitempty"`
	Images    []string `json:"images,omitempty"`
	Videos    []string `json:"videos,omitempty"`
	Audios    []string `json:"audios,omitempty"`
	// Transcripts carries the "/atelier-artifact" URLs of timestamped
	// transcript artifacts (.vtt) a turn produced, parallel to Videos/Audios.
	Transcripts []string `json:"transcripts,omitempty"`
	// Markdowns carries the "/atelier-artifact" URLs of document artifacts
	// (md_) the document authoring pass produced, parallel to Transcripts —
	// the live UI renders their Edit/Download chips without a reload.
	Markdowns      []string `json:"markdowns,omitempty"`
	Done           bool     `json:"done"`
	Error          string   `json:"error,omitempty"`
	Model          string   `json:"model,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Tokens         int      `json:"tokens,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
}

type ToolPermissionRequestEvent struct {
	ID             string   `json:"id"`
	RequestID      string   `json:"requestID,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	ToolName       string   `json:"toolName"`
	Action         string   `json:"action"`
	Summary        string   `json:"summary"`
	Command        []string `json:"command,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	Path           string   `json:"path,omitempty"`
	ContentPreview string   `json:"contentPreview,omitempty"`
}

type ollamaChatChunk struct {
	Model   string `json:"model"`
	Done    bool   `json:"done"`
	Message struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	DoneReason      string `json:"done_reason"`
	EvalCount       int    `json:"eval_count"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	Error           string `json:"error"`
}

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	EvalCount       int    `json:"eval_count"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	Error           string `json:"error"`
}

func lastUserMessage(messages []ChatMessage) ChatMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return messages[index]
		}
	}
	if len(messages) == 0 {
		return ChatMessage{}
	}
	return messages[len(messages)-1]
}

// lastUserMessageIndex returns the index of the most recent user message, or -1
// if there is none. It is the index-returning counterpart to lastUserMessage
// for callers that need to mutate the message in place (e.g. attaching an
// image resolved from history onto the last user message before the final
// response).
func lastUserMessageIndex(messages []ChatMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return i
		}
	}
	return -1
}

func lastUserPrompt(messages []ChatMessage) string {
	return lastUserMessage(messages).Content
}
