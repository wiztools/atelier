package main

type ConversationSummary struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	DeletedAt     string `json:"deletedAt,omitempty"`
	TurnCount     int    `json:"turnCount"`
	ArtifactCount int    `json:"artifactCount"`
	// Workspace is the immutable filesystem-tool root this conversation runs
	// against. Empty for legacy conversations created before per-conversation
	// workspaces; the UI falls back to the configured default root.
	Workspace string `json:"workspace,omitempty"`
	// ProjectID is the library project this conversation belongs to; empty
	// means standalone. Pinned at creation from ChatRequest.ProjectID and
	// immutable afterwards (MoveConversationToProject is the one sanctioned
	// rewrite). No schema bump: absent and empty both read as standalone.
	ProjectID string `json:"projectId,omitempty"`
	// ModelOverrides mirrors the conversation record's model-selection
	// override (nil when unset) so the UI can badge and hydrate overridden
	// conversations from the list without loading the full record.
	ModelOverrides *ConversationModelOverrides `json:"modelOverrides,omitempty"`
}

type ConversationDetail struct {
	Conversation HistoryConversation `json:"conversation"`
	Turns        []HistoryTurn       `json:"turns"`
}

type PurgeArchivedResult struct {
	DeletedConversations int `json:"deletedConversations"`
	DeletedAssets        int `json:"deletedAssets"`
}

type HistoryConversation struct {
	SchemaVersion int                      `json:"schemaVersion"`
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	Title         string                   `json:"title"`
	CreatedAt     string                   `json:"createdAt"`
	UpdatedAt     string                   `json:"updatedAt"`
	DeletedAt     string                   `json:"deletedAt,omitempty"`
	Provider      HistoryProvider          `json:"provider"`
	Defaults      HistoryDefaults          `json:"defaults"`
	Stats         HistoryConversationStats `json:"stats"`
	// Workspace is the immutable filesystem-tool root this conversation runs
	// against. Set at creation and never changed. SchemaVersion 2+ guarantees
	// this is populated; SchemaVersion 1 records are backfilled on resume.
	Workspace string `json:"workspace,omitempty"`
	// ProjectID is the library project this conversation belongs to; empty
	// means standalone. Pinned at creation from ChatRequest.ProjectID and
	// immutable afterwards (MoveConversationToProject is the one sanctioned
	// rewrite). No schema bump: absent and empty both read as standalone.
	ProjectID string `json:"projectId,omitempty"`
	// ModelOverrides is the per-conversation model-selection override (see
	// ConversationModelOverrides): empty fields inherit the live global
	// Settings, so the struct is stored nil unless at least one field is set.
	// Mutable over the conversation's life via SetConversationModelOverrides,
	// the one sanctioned rewrite. Applied per turn by rewriting the per-stream
	// config copy before the HarnessEngine is built (the resolveTurnWorkspace
	// pattern). No schema bump: absent and nil both read as "follow global
	// config".
	ModelOverrides *ConversationModelOverrides `json:"modelOverrides,omitempty"`
	// EditSession pins an edit-session child conversation (Kind "edit") to
	// the image it edits — see EditSessionMeta (edit_session.go). Nil on chat
	// conversations; no schema bump (absent reads as "not an edit session").
	EditSession *EditSessionMeta `json:"editSession,omitempty"`
}

type HistoryProvider struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseURL"`
}

type HistoryDefaults struct {
	ChatModel  string `json:"chatModel,omitempty"`
	ImageModel string `json:"imageModel,omitempty"`
	System     string `json:"system,omitempty"`
}

type HistoryConversationStats struct {
	TurnCount     int `json:"turnCount"`
	ArtifactCount int `json:"artifactCount"`
}

type HistoryTurn struct {
	SchemaVersion    int              `json:"schemaVersion"`
	ID               string           `json:"id"`
	ConversationID   string           `json:"conversationId"`
	CreatedAt        string           `json:"createdAt"`
	Kind             string           `json:"kind"`
	Role             string           `json:"role"`
	Model            string           `json:"model,omitempty"`
	Provider         string           `json:"provider,omitempty"`
	Content          []HistoryContent `json:"content"`
	Request          map[string]any   `json:"request,omitempty"`
	ProviderResponse map[string]any   `json:"providerResponse,omitempty"`
	DeletedAt        string           `json:"deletedAt,omitempty"`
}

type HistoryContent struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
	Path       string `json:"path,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}
