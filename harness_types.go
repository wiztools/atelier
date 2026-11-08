package main

type HarnessRun struct {
	ID             string                 `json:"id"`
	Mode           string                 `json:"mode"`
	Status         string                 `json:"status"`
	StartedAt      string                 `json:"startedAt"`
	CompletedAt    string                 `json:"completedAt,omitempty"`
	DurationMS     int64                  `json:"durationMs,omitempty"`
	RequestID      string                 `json:"requestId,omitempty"`
	ConversationID string                 `json:"conversationId,omitempty"`
	Loop           HarnessLoop            `json:"loop"`
	Skill          *HarnessSkillDecision  `json:"skill,omitempty"`
	Triage         *HarnessTriageDecision `json:"triage,omitempty"`
	Steps          []HarnessStep          `json:"steps"`
	// onUpdate, when set, fires after every step transition (appendStep,
	// completeStep, complete) so the UI can render the run live via the
	// atelier:harness-run event. Unexported and never serialized; tests build
	// runs without it.
	onUpdate func()
}

type HarnessLoop struct {
	MaxSteps      int    `json:"maxSteps"`
	MaxWallTimeMS int64  `json:"maxWallTimeMs"`
	Iterations    int    `json:"iterations"`
	StopReason    string `json:"stopReason,omitempty"`
}

type HarnessStep struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Iteration   int    `json:"iteration,omitempty"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Status      string `json:"status"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt,omitempty"`
	DurationMS  int64  `json:"durationMs,omitempty"`
	Decision    string `json:"decision,omitempty"`
	DoneReason  string `json:"doneReason,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	// Tokens is the completion-side count for model-call steps (triage, skill,
	// planning, streaming). Bookkeeping steps (saved/evaluation) carry none —
	// the turn total lives in ProviderResponse.tokens — so summing tokens over
	// steps counts each model call exactly once.
	Tokens       int `json:"tokens,omitempty"`
	PromptTokens int `json:"promptTokens,omitempty"`
	// CostMicros is the server-billed cost of one model call in USD millionths
	// (OpenRouter usage.cost), riding the same steps as the token counts —
	// fal media generation's cost rides the tool_call step's activities
	// instead. Zero when the provider reports no cost (Ollama, local servers).
	CostMicros int64 `json:"costMicros,omitempty"`
	// FirstTokenMS is time-to-first-token for streaming steps: milliseconds
	// between the provider stream opening and the first visible delta.
	FirstTokenMS int64                   `json:"firstTokenMs,omitempty"`
	Request      *HarnessRequestSnapshot `json:"request,omitempty"`
	Tools        []HarnessToolActivity   `json:"tools,omitempty"`
}

// HarnessRequestSnapshot is a compact fingerprint of one model request,
// recorded on every model-call step so post-hoc debugging can see what was
// sent without persisting raw prompts. PromptHash covers the prefix-cache-
// sensitive prefix (system prompt + message #0): consecutive same-model steps
// sharing a PromptHash kept the Ollama prefix cache warm; a change means the
// cache was invalidated. Hashes are sha256 truncated to 12 hex chars.
type HarnessRequestSnapshot struct {
	PromptHash        string `json:"promptHash,omitempty"`
	ToolsHash         string `json:"toolsHash,omitempty"`
	PromptChars       int    `json:"promptChars,omitempty"`
	ToolMode          string `json:"toolMode,omitempty"` // native|format|"" (final responses carry none)
	NumCtx            int    `json:"numCtx,omitempty"`
	TruncatedMessages int    `json:"truncatedMessages,omitempty"`
}

type HarnessToolActivity struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Call is the planner's emitted inputs for this tool call (the plan's
	// per-call params: content, model, path, command, duration, negativePrompt,
	// etc.). Recorded on every tool_call step so the emitted params are
	// inspectable post-hoc — the result struct carries only what the tool
	// produced, not what was requested. Zipped onto the activity at the
	// recording site (harness.go toolActivities), never via HarnessToolResult,
	// so planner params cannot leak into role:"tool" evidence.
	Call          HarnessToolCall `json:"call,omitempty"`
	Path          string          `json:"path,omitempty"`
	Command       []string        `json:"command,omitempty"`
	ExitCode      int             `json:"exitCode,omitempty"`
	StdoutPreview string          `json:"stdoutPreview,omitempty"`
	StderrPreview string          `json:"stderrPreview,omitempty"`
	DurationMS    int64           `json:"durationMs,omitempty"`
	Error         string          `json:"error,omitempty"`
	// Model, MediaKind, and MediaCount record the generation model a media tool
	// actually ran (after defaulting — not the planner's requested Call.Model),
	// what it produced ("video"/"audio"/"image"), and how many. Media models
	// don't consume tokens, so these fields — not token counts — are what makes
	// their consumption visible in telemetry. Set from the tool's own result
	// payload in defaultHarnessToolActivity's type switch; empty for non-media
	// tools and failed calls (no payload).
	// Provider names the backend that rendered the media ("fal", "ollama",
	// "openai-compatible") — attribution for Model, since the same image model
	// family can run locally or in the cloud and the id alone doesn't say which.
	// Filled at the engine layer (toolActivityFromResult) from the same config
	// the tool gateway routes by.
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	MediaKind  string `json:"mediaKind,omitempty"`
	MediaCount int    `json:"mediaCount,omitempty"`
	// CostMicros estimates what this media generation cost in USD millionths,
	// computed at call time from fal's pricing API (unit price × billed units
	// — images, seconds of video, characters, or one request). An estimate,
	// not a bill: feature multipliers fal applies server-side may not be
	// captured. Zero when the backend is local (Ollama, openai-compatible) or
	// pricing was unavailable; failed calls carry none, matching MediaCount.
	CostMicros int64 `json:"costMicros,omitempty"`
	// CostUnknown marks a media generation whose cost could not be determined
	// — the Replicate backend reports no price (no pricing API, no cost in
	// the prediction response), so its rows render "?" in the money column
	// instead of the empty slot a genuinely free local call renders. Absent
	// (false) everywhere else, including turns persisted before the field
	// existed. The turn total still sums only known costs.
	CostUnknown bool `json:"costUnknown,omitempty"`
	// Permission records how the permission gate resolved (approved/denied/
	// timeout/cancelled) and how long it waited, when the call was gated.
	// Zipped from HarnessToolResult's json:"-" side-channel at the same
	// recording site as Call.
	Permission       string `json:"permission,omitempty"`
	PermissionWaitMS int64  `json:"permissionWaitMs,omitempty"`
}
