package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	harnessChatMaxSteps    = 3
	harnessChatMaxWallTime = 2 * time.Minute
	harnessPlanNumPredict  = 4096
)

// Context budgeting works in characters with a rough chars-per-token estimate;
// it only needs to be accurate enough to keep requests inside num_ctx so
// Ollama never silently truncates from the front of the conversation.
const (
	contextCharsPerToken      = 4
	minHistoryBudgetChars     = 2048
	toolResultMessageMaxChars = 8 * 1024
	contextOmittedMarker      = "[Earlier conversation was omitted to fit the model's context window.]"
)

// The primary model's system prompt only ever receives these code-authored notes.
// Planner output (briefs, reasons) is telemetry and thinking, never prompt text,
// so a weaker harness model can't cap what the primary model is allowed to know.
const toolEvidenceSystemNote = "Atelier ran workspace tools for this turn. Their observations appear at the end of the conversation. Treat them as evidence: report failures honestly and do not claim an action succeeded unless an observation shows it. You cannot call tools yourself; if the user asked for an action that no observation confirms, say plainly that it was not completed. A tool observation's \"notices\" field holds authoritative caveats that are shown to the user verbatim; account for their meaning (never claim a dropped capability succeeded) but do not quote them."

const invalidPlanSystemNote = "Atelier could not produce a valid tool plan for this turn, so no tools ran. You cannot call tools or execute commands. Do not run commands, paste commands as if executed, or claim any tool action succeeded. If the user asked for a tool action, report plainly that it could not be completed."

const invalidPlanAfterToolsSystemNote = "Atelier ran workspace tools for this turn, but its latest tool plan was invalid, so the most recently requested action did not run. Tool observations appear at the end of the conversation. Treat them as evidence: report failures honestly and do not claim an action succeeded unless an observation shows it. You cannot call tools yourself; if the user asked for an action that no observation confirms, say plainly that it was not completed."

type HarnessEngine struct {
	config AppConfig
	app    *App
	// registry is the tool registry derived from config, built once on first
	// use. Config is immutable for the engine's lifetime, so rebuilding the
	// registry (and its param-schema maps) on every planning round and tool
	// result is wasted work. Guarded by a mutex for the streaming goroutine.
	registryOnce sync.Once
	registry     HarnessToolRegistry
}

type HarnessPreparedTurn struct {
	Completion           ChatCompletionResult
	SkillDecision        *HarnessSkillDecision
	LoadedSkill          *LoadedSkill
	ToolCalls            []HarnessToolCall
	ToolResults          []HarnessToolResult
	PlanValidationErrors []string
	Rounds               []HarnessToolRound
}

type HarnessToolRound struct {
	Iteration            int
	Brief                string
	NeedsTools           bool
	Reason               string
	Completion           ChatCompletionResult
	SkillDecision        *HarnessSkillDecision
	ToolCalls            []HarnessToolCall
	ToolResults          []HarnessToolResult
	PlanValidationErrors []string
}

type HarnessToolPlan struct {
	Brief      string            `json:"brief"`
	NeedsTools bool              `json:"needsTools"`
	Reason     string            `json:"reason"`
	ToolCalls  []HarnessToolCall `json:"toolCalls"`
}

type HarnessToolCall struct {
	Name        string            `json:"name"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	TimeoutMS   int               `json:"timeoutMs,omitempty"`
	Path        string            `json:"path,omitempty"`
	Content     string            `json:"content,omitempty"`
	Model       string            `json:"model,omitempty"`
	Append      bool              `json:"append,omitempty"`
	Overwrite   bool              `json:"overwrite,omitempty"`
	MaxBytes    int               `json:"maxBytes,omitempty"`
	AllowBinary bool              `json:"allowBinary,omitempty"`
	// NegativePrompt and GenerateAudio are generate_video inputs. GenerateAudio
	// is a pointer so an omitted flag (nil) is distinguishable from an explicit
	// false — see VideoGenerateRequest.GenerateAudio.
	NegativePrompt string `json:"negativePrompt,omitempty"`
	GenerateAudio  *bool  `json:"generateAudio,omitempty"`
	// AspectRatio is an optional generate_image input naming the output shape
	// (e.g. "16:9"). When set, the handler derives width/height from it; when
	// omitted, the configured default dimensions are used. See
	// generateImageParamSchema and imageSizeForAspectRatio.
	AspectRatio string `json:"aspectRatio,omitempty"`
	// Duration is an optional generate_audio input naming the target clip length
	// (a fal enum/seconds string, e.g. "10"). Meaningful for music/sound-effect
	// models; text-to-speech models ignore it, since their length follows the
	// spoken text. Forwarded only when set — see generateAudioParamSchema.
	Duration string `json:"duration,omitempty"`
	// Loop and Voice are optional generate_audio inputs. Loop requests a
	// seamless loop (sound-effect models); Voice selects a text-to-speech voice.
	// Both are resolved against the model's schema and dropped-with-notice when
	// the configured model has no matching parameter.
	Loop  bool   `json:"loop,omitempty"`
	Voice string `json:"voice,omitempty"`
	// Scale is an optional upscale_image input naming the upscale factor
	// ("2x" or "4x"). Omit for the default 2x. See imageUpscaleParamSchema.
	Scale string `json:"scale,omitempty"`
	// Task and Language are optional transcribe_audio inputs. Task selects
	// "transcribe" (default) or "translate"; Language is an optional hint
	// (empty lets the model auto-detect). See transcribeAudioParamSchema.
	Task     string `json:"task,omitempty"`
	Language string `json:"language,omitempty"`
}

type HarnessToolResult struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Result  any      `json:"result,omitempty"`
	Error   string   `json:"error,omitempty"`
	Notices []string `json:"notices,omitempty"`
}

type finalResponseAttempt struct {
	Content  string
	Thinking string
	Model    string
	Reason   string
	Tokens   int
	Emitted  bool
}

func newHarnessEngine(config AppConfig, app ...*App) *HarnessEngine {
	engine := &HarnessEngine{config: config}
	if len(app) > 0 {
		engine.app = app[0]
	}
	return engine
}

// RunChatStream drives one full chat turn through the harness. turnStarted is
// true when the caller has already persisted the user turn (StreamChat does
// this before invoking the stream goroutine); false means RunChatStream must
// start the turn itself (the test helper path).
func (h *HarnessEngine) RunChatStream(ctx context.Context, requestID string, req ChatRequest, turnStarted bool) {
	if h.app == nil {
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		defaultModel, defaultProvider := h.app.resolvedPrimaryModelAndProvider(h.config)
		req.Model = defaultModel
		if strings.TrimSpace(req.Provider) == "" {
			req.Provider = defaultProvider
		}
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if !turnStarted {
		var err error
		conversationID, err = h.StartChatTurn(req)
		if err != nil {
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history start failed: %v", err), Done: true})
			return
		}
	}
	req.ConversationID = conversationID
	h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, ConversationID: conversationID})

	primaryModel := h.primaryModelForRequest(req)
	primaryProvider := resolvedProvider(req)
	harness := h.resolveHarnessTarget(primaryModel, primaryProvider)
	// Resolve the turn's source image once: it feeds the tool execution context
	// (image-to-video, upscale, image-to-image) and is also injected into the
	// final response so a vision-capable primary model can see a previously
	// generated image when the user asks about it. Resolved here rather than
	// inline at each call site so both consume the same value.
	attachedImage := latestAttachedImageForTurn(req, h.config.Storage)
	run := newHarnessRun(requestID, conversationID)
	queued := run.appendStep("queued", 1, "", "", "turn accepted by harness")
	run.completeStep(queued, "completed", "", 0, "")

	// A misconfigured harness provider fails every call for the same reason, so
	// report it now rather than degrading through triage and the planner first.
	if err := h.harnessProviderUnavailable(harness, req.BaseURL); err != nil {
		run.complete("failed", "harness_provider_unavailable")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}

	// Attached audio is a tool-consumable resource on any provider, exactly like
	// an attached image: the planner may run transcribe_audio (fal-ai/wizper,
	// provider-independent) to turn it into text evidence, or — on OpenRouter —
	// send it as chat input via an input_audio content part. There is no
	// provider guard here: Ollama's lack of a native audio input API only
	// matters if the planner chooses not to transcribe, in which case the
	// Ollama adapter simply drops the Audios field (it doesn't recognize it).

	skillIndex, skillIndexErr := loadSkillIndex(skillRootsFor(h.config.Tools.Filesystem.Root))
	var explicitSkill *SkillIndexEntry
	explicitReason := ""
	if skillIndexErr == nil {
		if entry, reason, ok := explicitSkillSelection(skillIndex, lastUserMessage(req.Messages).Content); ok {
			explicitSkill = &entry
			explicitReason = reason
		}
	}

	decision := HarnessTriageDecision{NeedsTools: true, ResponseMode: "text", Reason: "user explicitly referenced a skill"}
	if explicitSkill == nil {
		triage := run.appendStep("triage", 1, harness.provider, harness.model, "harness model deciding response mode and tools")
		var completion ChatCompletionResult
		decision, completion = h.triageChatTurn(ctx, req, harness, skillIndex)
		run.Steps[triage].Decision = triageDecisionLabel(decision)
		status := "completed"
		if decision.Error != "" {
			status = "failed"
		}
		run.completeStep(triage, status, completion.Reason, completion.EvalTokens, decision.Error)
	}
	run.Triage = &decision

	// Image mode requires tools (generate_image). Force it so the planner runs.
	if decision.ResponseMode == "image" && !decision.NeedsTools {
		decision.NeedsTools = true
		decision.ToolTask = "Generate the requested image using the generate_image tool."
	}

	var preparation HarnessPreparedTurn
	preparationThinking := ""
	if decision.NeedsTools {
		// Resolve native tool-calling support. Native tools are an enhancement:
		// any failure to confirm the capability falls back to the format-schema
		// planner path, so a wrong fallback costs latency, never correctness.
		useNativeTools := h.supportsNativeTools(ctx, req.BaseURL, harness)
		toolReq := req
		toolReq.Model = harness.model
		toolReq.Provider = harness.provider
		var err error
		preparation, err = h.prepareChatTurnLoop(ctx, requestID, conversationID, toolReq, harnessTurnContext{
			SkillIndex:      skillIndex,
			SkillIndexErr:   skillIndexErr,
			ExplicitSkill:   explicitSkill,
			ExplicitReason:  explicitReason,
			ToolTask:        decision.ToolTask,
			PrimaryModel:    primaryModel,
			PrimaryProvider: primaryProvider,
			ResponseMode:    decision.ResponseMode,
			UseNativeTools:  useNativeTools,
			Harness:         harness,
			AttachedImage:   attachedImage,
			AttachedAudio:   latestAttachedAudioForTurn(req, h.config.Storage),
			AttachedVideo:   latestAttachedVideoForTurn(req, h.config.Storage),
		}, &run)
		if err != nil {
			run.complete("failed", "harness_prepare_error")
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
			return
		}
		preparationThinking = formatHarnessPreparationThinking(preparation)
		if preparationThinking != "" {
			h.app.emitChatEvent(ChatStreamEvent{
				RequestID:      requestID,
				Thinking:       preparationThinking,
				ConversationID: conversationID,
			})
		}
	}

	// Resolve the response model: when the primary model is an image generation
	// model, it cannot produce text or analyze images, so fall back to the
	// harness model for the final response.
	responseModel := h.responseModelFor(decision.ResponseMode, primaryModel, harness)
	responseProvider := h.responseProviderFor(decision.ResponseMode, primaryProvider, primaryProvider, harness)
	responseReq := h.preparedResponseRequest(req, responseModel, responseProvider, preparation, attachedImage)
	result, err := h.runFinalResponseAttempt(ctx, requestID, conversationID, responseReq, &run)

	// Even if the text response stream failed, deliver any media the tool path
	// produced rather than silently dropping it.
	images, imageReq := imagesFromToolResults(preparation.ToolResults)
	videos, videoReq := videosFromToolResults(preparation.ToolResults)
	audios, audioReq := audiosFromToolResults(preparation.ToolResults)
	// Video/audio temp files are moved into the artifacts directory at persist
	// time; remove any that survive (e.g. a save error) so they don't leak.
	defer cleanupVideoTempFiles(videos)
	defer cleanupAudioTempFiles(audios)
	if err != nil {
		if len(images) > 0 || len(videos) > 0 || len(audios) > 0 {
			result = finalResponseAttempt{
				Content: harnessEmptyResponseNotice(preparation.ToolResults),
				Model:   responseModel,
				Reason:  "response_stream_failed_media_delivered",
			}
			run.complete("completed", "response_stream_failed_media_delivered")
		} else {
			run.complete("failed", result.Reason)
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
			return
		}
	}
	assistantThinking := preparationThinking + result.Thinking
	assistantContent := result.Content
	finalModel := result.Model
	finalReason := result.Reason
	finalTokens := result.Tokens
	finalContentEmitted := result.Emitted

	if strings.TrimSpace(assistantContent) == "" {
		assistantContent = harnessEmptyResponseNotice(preparation.ToolResults)
		finalContentEmitted = false
		if strings.TrimSpace(finalReason) == "" {
			finalReason = "empty_response_notice"
		}
	}
	// Deterministic tool caveats (e.g. a requested loop the model can't honor)
	// are appended verbatim so the user always sees them, regardless of whether
	// the model chose to mention them in its prose.
	toolNotices := collectToolNotices(preparation.ToolResults)
	if toolNotices != "" {
		if strings.TrimSpace(assistantContent) != "" {
			assistantContent += "\n\n" + toolNotices
		} else {
			assistantContent = toolNotices
		}
	}
	if strings.TrimSpace(finalModel) == "" {
		finalModel = responseModel
	}
	h.evaluateChatRun(&run, assistantContent, finalReason)
	// The run is serialized into the saved turn, so the saved step is marked
	// completed optimistically and flipped to failed if the write errors.
	saved := run.appendStep("saved", 1, "", "", "assistant turn and harness run stored in history")
	run.completeStep(saved, "completed", finalReason, finalTokens, "")
	run.complete("completed", "final")
	var saveErr error
	var videoURLs []string
	var audioURLs []string
	switch {
	case len(videos) > 0:
		// The video appender also stores any images produced this turn, so a
		// turn with both media types keeps everything.
		videoURLs, saveErr = appendChatAssistantTurnWithVideos(h.config, conversationID, assistantContent, assistantThinking, finalModel, responseProvider, finalReason, images, imageReq, videos, videoReq, run)
	case len(audios) > 0:
		audioURLs, saveErr = appendChatAssistantTurnWithAudios(h.config, conversationID, assistantContent, assistantThinking, finalModel, responseProvider, finalReason, audios, audioReq, run)
	case len(images) > 0:
		saveErr = appendChatAssistantTurnWithImages(h.config, conversationID, assistantContent, assistantThinking, finalModel, responseProvider, finalReason, images, "", run, imageReq)
	default:
		saveErr = h.SaveAssistantTurn(conversationID, assistantContent, assistantThinking, finalModel, responseProvider, finalReason, finalTokens, run)
	}
	if saveErr != nil {
		run.completeStep(saved, "failed", finalReason, finalTokens, saveErr.Error())
		run.complete("failed", "history_save_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history save failed: %v", saveErr), Done: true})
		return
	}
	terminalContent := assistantContent
	if finalContentEmitted {
		// The model's streamed prose already reached the UI; only the appended
		// notice block (if any) still needs to be delivered live.
		terminalContent = ""
		if toolNotices != "" {
			terminalContent = "\n\n" + toolNotices
		}
	}
	h.app.emitChatEvent(ChatStreamEvent{
		RequestID:      requestID,
		Content:        terminalContent,
		Images:         images,
		Videos:         videoURLs,
		Audios:         audioURLs,
		Done:           true,
		Model:          finalModel,
		Reason:         finalReason,
		Tokens:         finalTokens,
		ConversationID: conversationID,
	})
}

// collectToolNotices gathers deterministic, user-facing caveats across all tool
// results, formatted as blockquote lines for appending to the chat reply.
func collectToolNotices(results []HarnessToolResult) string {
	var lines []string
	for _, r := range results {
		for _, n := range r.Notices {
			if s := strings.TrimSpace(n); s != "" {
				lines = append(lines, "> ⚠️ "+s)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// cleanupVideoTempFiles removes any generated-video temp files that still exist.
// After a successful persist the files have been moved (renamed) away, so this
// is a no-op then; it only matters when the turn errored before persistence.
func cleanupVideoTempFiles(videos []ToolVideoFile) {
	for _, video := range videos {
		if path := strings.TrimSpace(video.TempPath); path != "" {
			os.Remove(path)
		}
	}
}

func cleanupAudioTempFiles(audios []ToolAudioFile) {
	for _, audio := range audios {
		if path := strings.TrimSpace(audio.TempPath); path != "" {
			os.Remove(path)
		}
	}
}

// imagesFromToolResults collects images generated by generate_image tool calls
// this turn, plus the request metadata used to store them as artifacts.
func imagesFromToolResults(results []HarnessToolResult) ([]string, ImageGenerateRequest) {
	var images []string
	var imageReq ImageGenerateRequest
	for _, result := range results {
		typed, ok := result.Result.(ToolImageResult)
		if !ok || result.Status != "completed" {
			continue
		}
		if imageReq.Model == "" {
			imageReq = ImageGenerateRequest{Model: typed.Model, Prompt: typed.Prompt}
		}
		images = append(images, typed.Images...)
	}
	return dedupeStrings(images), imageReq
}

// videosFromToolResults collects videos generated by generate_video tool calls
// this turn, plus the request metadata used to store them as artifacts. Each
// entry references a temp file on disk (not bytes), which the persistence step
// moves into the conversation's artifacts directory.
func videosFromToolResults(results []HarnessToolResult) ([]ToolVideoFile, VideoGenerateRequest) {
	var videos []ToolVideoFile
	var videoReq VideoGenerateRequest
	for _, result := range results {
		typed, ok := result.Result.(ToolVideoResult)
		if !ok || result.Status != "completed" {
			continue
		}
		if videoReq.Model == "" {
			videoReq = VideoGenerateRequest{Model: typed.Model, Prompt: typed.Prompt}
		}
		videos = append(videos, typed.Videos...)
	}
	return videos, videoReq
}

// audiosFromToolResults collects audio produced by generate_audio tool calls
// this turn, plus the request metadata used to store them as artifacts.
func audiosFromToolResults(results []HarnessToolResult) ([]ToolAudioFile, AudioGenerateRequest) {
	var audios []ToolAudioFile
	var audioReq AudioGenerateRequest
	for _, result := range results {
		typed, ok := result.Result.(ToolAudioResult)
		if !ok || result.Status != "completed" {
			continue
		}
		if audioReq.Model == "" {
			audioReq = AudioGenerateRequest{Model: typed.Model, Prompt: typed.Prompt}
		}
		audios = append(audios, typed.Audios...)
	}
	return audios, audioReq
}

// latestUserImage returns the first image attached to the most recent user
// message (a base64 data URL), or "" if the current turn has no attachment. It
// is the source frame generate_video animates for image-to-video.
func latestUserImage(messages []ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		for _, image := range messages[i].Images {
			if trimmed := strings.TrimSpace(image); trimmed != "" {
				return trimmed
			}
		}
		return ""
	}
	return ""
}

// latestUserAudioURL returns the first audio attachment on the most recent user
// message (a data URL), or "" if the current turn has none. It is the audio
// sibling of latestUserImage and the source clip transcribe_audio consumes.
func latestUserAudioURL(messages []ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		for _, audio := range messages[i].Audios {
			if trimmed := strings.TrimSpace(audio); trimmed != "" {
				return trimmed
			}
		}
		return ""
	}
	return ""
}

// latestUserVideoURL returns the first video attachment on the most recent user
// message (a data URL), or "" if the current turn has none. It is the video
// sibling of latestUserAudioURL and the source clip Veo extend / video-to-video
// lip sync consume.
func latestUserVideoURL(messages []ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		for _, video := range messages[i].Videos {
			if trimmed := strings.TrimSpace(video); trimmed != "" {
				return trimmed
			}
		}
		return ""
	}
	return ""
}

// latestAttachedImageForTurn returns the image attached to the current turn if
// present (a base64 data URL), else falls back to the most recent image in
// conversation history, whether user-attached or model-generated. The fallback
// lets image-dependent tools (upscale_image, image-to-image, image-to-video)
// operate across turns without forcing the user to re-attach on every message —
// "the image" is expected to persist across a conversation, including an image
// the model just produced (e.g. generate an image, then "make a video of it"
// in the next turn). History images are persisted as artifacts on disk on both
// user and assistant turns; they're re-read and re-encoded as a data URL to
// match the shape AttachedImage consumers expect. The walk is newest-first and
// role-agnostic, so a later-generated image correctly shadows an earlier
// user-attached one — the most recent image wins. Returns "" if there is no
// current attachment and no image in history (e.g. turn 1 of a brand-new
// conversation, or a conversation with no images). Errors reading history are
// swallowed: a stale history file must not break the current turn — the caller
// falls back to the empty-AttachedImage path and the tool surfaces its usual
// "attach an image" error.
func latestAttachedImageForTurn(req ChatRequest, storage ConfigStorage) string {
	if image := latestUserImage(req.Messages); image != "" {
		return image
	}
	if strings.TrimSpace(req.ConversationID) == "" {
		return ""
	}
	detail, err := getConversation(storage, req.ConversationID)
	if err != nil {
		return ""
	}
	// Walk backwards to find the most recent turn (any role) with an image
	// content entry. Both user-attached and model-generated images land as
	// image artifacts, so no role filter is applied.
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		turn := detail.Turns[i]
		for _, content := range turn.Content {
			if content.Type != "image" || strings.TrimSpace(content.Path) == "" {
				continue
			}
			dataURL, err := readArtifactAsDataURL(storage, req.ConversationID, content)
			if err != nil {
				return "" // don't try older images — one read failure is enough to bail
			}
			return dataURL
		}
	}
	return ""
}

// latestAttachedAudioForTurn is the audio sibling of latestAttachedImageForTurn:
// it returns the current turn's audio attachment (a data URL) if present, else
// falls back to the most recent audio in conversation history, whether
// user-attached or model-generated (e.g. TTS output). The fallback lets
// transcribe_audio operate across turns without forcing the user to re-attach
// on every message. History audio is persisted as artifacts on disk on both
// user and assistant turns; they're re-read and re-encoded as a data URL to
// match the shape AttachedAudio consumers (fal) expect. Errors are swallowed so
// a stale history file can't break the current turn — the caller falls back to
// the empty-AttachedAudio path and transcribe_audio surfaces its "attach an
// audio clip" error.
func latestAttachedAudioForTurn(req ChatRequest, storage ConfigStorage) string {
	if audio := latestUserAudioURL(req.Messages); audio != "" {
		return audio
	}
	if strings.TrimSpace(req.ConversationID) == "" {
		return ""
	}
	detail, err := getConversation(storage, req.ConversationID)
	if err != nil {
		return ""
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		turn := detail.Turns[i]
		for _, content := range turn.Content {
			if content.Type != "audio" || strings.TrimSpace(content.Path) == "" {
				continue
			}
			dataURL, err := readAudioArtifactAsDataURL(storage, req.ConversationID, content)
			if err != nil {
				return ""
			}
			return dataURL
		}
	}
	return ""
}

// latestAttachedVideoForTurn is the video sibling of latestAttachedAudioForTurn:
// it returns the current turn's video attachment (a data URL) if present, else
// falls back to the most recent video in conversation history, whether
// user-attached or model-generated. The fallback lets video-dependent tools
// (Veo extend, video-to-video lip sync) operate across turns without forcing
// the user to re-attach on every message. History video is persisted as
// artifacts on disk on both user and assistant turns; they're re-read and
// re-encoded as a data URL to match the shape AttachedVideo consumers (fal)
// expect. Errors are swallowed so a stale history file can't break the current
// turn — the caller falls back to the empty-AttachedVideo path and the tool
// surfaces its "attach a video" error.
func latestAttachedVideoForTurn(req ChatRequest, storage ConfigStorage) string {
	if video := latestUserVideoURL(req.Messages); video != "" {
		return video
	}
	if strings.TrimSpace(req.ConversationID) == "" {
		return ""
	}
	detail, err := getConversation(storage, req.ConversationID)
	if err != nil {
		return ""
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		turn := detail.Turns[i]
		for _, content := range turn.Content {
			if content.Type != "video" || strings.TrimSpace(content.Path) == "" {
				continue
			}
			dataURL, err := readVideoArtifactAsDataURL(storage, req.ConversationID, content)
			if err != nil {
				return ""
			}
			return dataURL
		}
	}
	return ""
}

func (h *HarnessEngine) primaryModelForRequest(req ChatRequest) string {
	model := strings.TrimSpace(req.SelectedModel)
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	return model
}

func triageDecisionLabel(decision HarnessTriageDecision) string {
	mode := decision.ResponseMode
	if mode == "" {
		mode = "text"
	}
	if decision.NeedsTools {
		return mode + "+tools"
	}
	return mode
}

// completeWithHarnessModel runs one harness call (triage, skill selection, or
// planning) against whichever provider the harness model lives on. All three go
// through the ChatProvider registry rather than reaching for an Ollama client
// directly, so the harness model is not pinned to a single backend.
func (h *HarnessEngine) completeWithHarnessModel(ctx context.Context, harness harnessTarget, req ChatRequest) (ChatCompletionResult, error) {
	provider, err := h.app.providerFor(harness.provider, req.BaseURL)
	if err != nil {
		return ChatCompletionResult{}, err
	}
	return provider.CompleteChat(ctx, req)
}

// harnessProviderUnavailable reports a harness provider that cannot be reached
// for a configuration reason — an OpenRouter harness with no API key, say.
//
// Such a failure is deterministic: it will never succeed on retry. The harness
// fail-safe rails (triage defers to the planner, an invalid plan feeds back a
// correction) exist for probabilistic model failures, and running a whole turn
// through them to arrive at a config error wastes the turn and tells the user
// nothing. Surface it once, up front, instead.
func (h *HarnessEngine) harnessProviderUnavailable(harness harnessTarget, baseURL string) error {
	if _, err := h.app.providerFor(harness.provider, baseURL); err != nil {
		return fmt.Errorf("harness model %q cannot run on %s: %w", harness.model, harness.provider, err)
	}
	return nil
}

// harnessTarget is where the harness model runs. Model and provider travel
// together so they cannot be resolved separately and drift — pairing one
// provider's model name with another provider's endpoint is a silent 404.
type harnessTarget struct {
	model    string
	provider string
}

// resolveHarnessTarget resolves the model and provider for the three harness
// calls (triage, skill selection, planning). An unset harness model falls back
// to the primary model on the primary provider, so a one-model setup still
// works — including a cloud-only one, which an Ollama-pinned fallback could not
// express.
func (h *HarnessEngine) resolveHarnessTarget(primaryModel, primaryProvider string) harnessTarget {
	fallback := harnessTarget{model: primaryModel, provider: primaryProvider}

	// mergeAppConfig normalizes this to "ollama" or "openrouter", but resolve
	// defensively: engines are also constructed straight from test configs.
	if strings.TrimSpace(h.config.Models.HarnessProvider) == "openrouter" {
		if model := strings.TrimSpace(h.config.Providers.OpenRouter.Harness); model != "" {
			return harnessTarget{model: model, provider: "openrouter"}
		}
		return fallback
	}
	if model := strings.TrimSpace(h.config.Providers.Ollama.Models.Harness); model != "" {
		return harnessTarget{model: model, provider: "ollama"}
	}
	return fallback
}

// supportsNativeTools reports whether the harness model advertises native
// function-calling. Any error or absent capability returns false, falling back
// to the format-schema planner path. Native tools are an enhancement, never a
// requirement.
//
// Capability detection is provider-specific: Ollama via /api/show's
// capabilities array, OpenRouter via supported_parameters containing "tools".
// A harness model on any other provider reports false and plans via the format
// schema. Both lookups are single network calls, made once per turn.
func (h *HarnessEngine) supportsNativeTools(ctx context.Context, baseURL string, harness harnessTarget) bool {
	model := strings.TrimSpace(harness.model)
	if model == "" || h.app == nil {
		return false
	}
	switch harness.provider {
	case "ollama":
		show, err := h.app.ollamaClient(baseURL).ShowModel(ctx, model)
		if err != nil {
			return false
		}
		return hasToolsCapability(show.Capabilities)
	case "openrouter":
		client, ok := h.app.openRouterClient()
		if !ok {
			return false
		}
		caps, err := client.ModelCapabilities(ctx, model)
		if err != nil {
			return false
		}
		return caps.supportsTools()
	default:
		return false
	}
}

// responseModelFor resolves which model should produce the final response,
// based on the triage response mode and the configured models.
//
// For responseMode "image": the image was already generated by the tool path,
// so the final response is just a text caption. An image generation model
// cannot produce text, so always use the harness model.
//
// For responseMode "text" and "vision": use the primary model, unless it is
// the configured image generation model (which can't do text or vision), in
// which case fall back to the harness model.
func (h *HarnessEngine) responseModelFor(mode, primaryModel string, harness harnessTarget) string {
	if h.respondsWithHarnessModel(mode, primaryModel) {
		return harness.model
	}
	return primaryModel
}

// responseProviderFor mirrors responseModelFor's fallback logic: whenever the
// final response falls back to the harness model (image captioning, or
// primaryModel being the configured Ollama image model), it must also fall back
// to the provider that model actually lives on.
//
// It takes the resolved harnessTarget rather than reading HarnessProvider from
// config: the two can differ. An unset harness model resolves to the primary
// model on the primary provider, and a raw config read would pair that model
// with the configured harness provider instead — a wrong-model 404.
func (h *HarnessEngine) responseProviderFor(mode, primaryModel, primaryProvider string, harness harnessTarget) string {
	if h.respondsWithHarnessModel(mode, primaryModel) {
		return harness.provider
	}
	return primaryProvider
}

// respondsWithHarnessModel reports whether the final response must fall back to
// the harness model. Image mode captions an already-generated image, and an
// image generation model can produce neither text nor vision.
func (h *HarnessEngine) respondsWithHarnessModel(mode, primaryModel string) bool {
	if mode == "image" {
		return true
	}
	imageModel := strings.TrimSpace(h.config.Providers.Ollama.Models.Image)
	return imageModel != "" && primaryModel == imageModel
}

func (h *HarnessEngine) runFinalResponseAttempt(ctx context.Context, requestID, conversationID string, req ChatRequest, run *HarnessRun) (finalResponseAttempt, error) {
	result := finalResponseAttempt{Model: req.Model}
	providerID := resolvedProvider(req)

	modelCall := run.appendStep("model_call", 1, providerID, req.Model, "provider stream opened")
	provider, err := h.app.providerFor(providerID, req.BaseURL)
	if err != nil {
		run.completeStep(modelCall, "failed", "", 0, err.Error())
		return result, err
	}
	events, err := provider.StreamChat(ctx, req)
	if err != nil {
		run.completeStep(modelCall, "failed", "", 0, err.Error())
		return result, err
	}
	run.completeStep(modelCall, "completed", "", 0, "")
	streaming := run.appendStep("streaming", 1, providerID, req.Model, "assistant response streamed to UI")

	var content strings.Builder
	var thinking strings.Builder
	for event := range events {
		if event.Err != nil {
			run.completeStep(streaming, "failed", result.Reason, result.Tokens, event.Err.Error())
			return result, event.Err
		}

		content.WriteString(event.ContentDelta)
		thinking.WriteString(event.Thinking)
		if event.Model != "" {
			result.Model = event.Model
		}
		if event.DoneReason != "" {
			result.Reason = event.DoneReason
		}
		tokens := 0
		if event.Usage != nil && event.Usage.CompletionTokens > 0 {
			result.Tokens = event.Usage.CompletionTokens
			tokens = event.Usage.CompletionTokens
		}

		h.app.emitChatEvent(ChatStreamEvent{
			RequestID:      requestID,
			Content:        event.ContentDelta,
			Thinking:       event.Thinking,
			Model:          event.Model,
			Provider:       providerID,
			Reason:         event.DoneReason,
			Tokens:         tokens,
			ConversationID: conversationID,
		})
		if event.ContentDelta != "" || event.Thinking != "" {
			result.Emitted = true
		}

		if event.Done {
			result.Content = content.String()
			result.Thinking = thinking.String()
			run.completeStep(streaming, "completed", result.Reason, result.Tokens, "")
			return result, nil
		}
	}
	result.Reason = "stream_ended"
	result.Content = content.String()
	result.Thinking = thinking.String()
	run.completeStep(streaming, "completed", result.Reason, result.Tokens, "")
	return result, nil
}

// toolRegistry returns the tool registry for this engine's config, building it
// once and caching it. The registry is a pure function of config, which is
// immutable for the engine's lifetime, so the param-schema maps are not rebuilt
// on every planning round and tool-result activity lookup.
func (h *HarnessEngine) toolRegistry() HarnessToolRegistry {
	h.registryOnce.Do(func() {
		h.registry = defaultHarnessToolRegistry(h.config)
	})
	return h.registry
}

func ollamaNumCtx(config AppConfig) int {
	if config.Providers.Ollama.NumCtx > 0 {
		return config.Providers.Ollama.NumCtx
	}
	return defaultOllamaNumCtx
}

func (h *HarnessEngine) numCtx() int {
	return ollamaNumCtx(h.config)
}

// withNumCtx returns a copy of options with num_ctx set unless the caller
// already chose one.
func withNumCtx(options map[string]any, numCtx int) map[string]any {
	merged := make(map[string]any, len(options)+1)
	for key, value := range options {
		merged[key] = value
	}
	if _, ok := merged["num_ctx"]; !ok {
		merged["num_ctx"] = numCtx
	}
	return merged
}

// historyBudgetChars is the character budget left for conversation messages
// after reserving room for the system prompt and the model's response.
func historyBudgetChars(numCtx int, system string, reserveTokens int) int {
	budget := numCtx*contextCharsPerToken - len(system) - reserveTokens*contextCharsPerToken
	if budget < minHistoryBudgetChars {
		budget = minHistoryBudgetChars
	}
	return budget
}

// truncateChatHistory drops the oldest messages until the rest fit the budget,
// marking the cut so the model knows earlier turns are missing. The newest
// message is always kept, even when it alone exceeds the budget.
func truncateChatHistory(messages []ChatMessage, budgetChars int) []ChatMessage {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
	}
	if len(messages) == 0 || total <= budgetChars {
		return messages
	}
	start := len(messages) - 1
	used := len(messages[start].Content)
	for start > 0 && used+len(messages[start-1].Content) <= budgetChars {
		start--
		used += len(messages[start].Content)
	}
	if start == 0 {
		return messages
	}
	truncated := append([]ChatMessage{}, messages[start:]...)
	truncated[0] = ChatMessage{
		Role:    truncated[0].Role,
		Content: contextOmittedMarker + "\n\n" + truncated[0].Content,
		Images:  truncated[0].Images,
		Audios:  truncated[0].Audios,
		Videos:  truncated[0].Videos,
	}
	return truncated
}

// harnessTurnContext carries what RunChatStream resolved before entering the
// tool path: the skill index (loaded once per turn), any explicit skill the
// user named, the primary model's triage task for the planner, and whether the
// harness model supports native tool-calling.
type harnessTurnContext struct {
	SkillIndex      []SkillIndexEntry
	SkillIndexErr   error
	ExplicitSkill   *SkillIndexEntry
	ExplicitReason  string
	ToolTask        string
	PrimaryModel    string
	PrimaryProvider string
	ResponseMode    string
	UseNativeTools  bool
	// Harness is the resolved model+provider for the skill-selection and
	// planning calls, carried as a unit so neither can drift from the other.
	Harness harnessTarget
	// AttachedImage is the source frame (base64 data URL) the user attached to
	// this turn, if any — used by generate_video for image-to-video.
	AttachedImage string
	// AttachedAudio is the audio clip (data URL) the user attached to this turn,
	// if any — used by transcribe_audio. Provider-agnostic, like AttachedImage.
	AttachedAudio string
	// AttachedVideo is the video clip (data URL) the user attached to this turn,
	// if any — used by generate_video (Veo extend) and the lip sync tool. Tool-
	// only: unlike audio it never reaches a chat model.
	AttachedVideo string
}

func (h *HarnessEngine) selectSkillForTurn(ctx context.Context, req ChatRequest, turn harnessTurnContext) (*HarnessSkillDecision, *LoadedSkill) {
	if turn.SkillIndexErr != nil {
		return &HarnessSkillDecision{AvailableCount: 0, Error: turn.SkillIndexErr.Error()}, nil
	}
	index := turn.SkillIndex
	if len(index) == 0 {
		return nil, nil
	}

	if turn.ExplicitSkill != nil {
		return h.loadSelectedSkill(*turn.ExplicitSkill, turn.ExplicitReason, len(index))
	}
	if h.app == nil {
		return &HarnessSkillDecision{AvailableCount: len(index), Reason: "skill index loaded; no app client available for selection"}, nil
	}

	indexJSON, _ := json.MarshalIndent(index, "", "  ")
	system := strings.TrimSpace(`You are Atelier's private skill selector. Choose at most one SKILL.md that should guide the current user turn.
Use only the skill index metadata. Do not answer the user.
Respond only with a JSON object matching the response schema:
{
  "skillName": "selected skill name, or empty string when no skill applies",
  "reason": "brief selection reason"
}
Select a skill only when its name or description clearly matches the user's request. If no skill clearly applies, use an empty skillName.`)
	selectionReq := ChatRequest{
		BaseURL: req.BaseURL,
		Model:   req.Model,
		System:  system,
		Messages: []ChatMessage{
			{Role: "user", Content: "Skill index:\n```json\n" + string(indexJSON) + "\n```\n\nCurrent user turn:\n" + lastUserMessage(req.Messages).Content},
		},
		Format: skillSelectionSchema(),
		Options: map[string]any{
			"temperature": 0,
			"num_predict": 160,
			"num_ctx":     h.numCtx(),
		},
	}
	completion, err := h.completeWithHarnessModel(ctx, turn.Harness, selectionReq)
	if err != nil {
		return &HarnessSkillDecision{AvailableCount: len(index), Error: err.Error()}, nil
	}
	plan, err := decodeSkillSelectionPlan(completion.Content)
	if err != nil {
		return &HarnessSkillDecision{AvailableCount: len(index), Error: err.Error()}, nil
	}
	if strings.TrimSpace(plan.SkillName) == "" {
		return &HarnessSkillDecision{AvailableCount: len(index), Reason: strings.TrimSpace(plan.Reason)}, nil
	}
	entry, ok := findSkillByName(index, plan.SkillName)
	if !ok {
		return &HarnessSkillDecision{AvailableCount: len(index), Name: strings.TrimSpace(plan.SkillName), Reason: strings.TrimSpace(plan.Reason), Error: "selected skill was not found in index"}, nil
	}
	return h.loadSelectedSkill(entry, strings.TrimSpace(plan.Reason), len(index))
}

func (h *HarnessEngine) loadSelectedSkill(entry SkillIndexEntry, reason string, availableCount int) (*HarnessSkillDecision, *LoadedSkill) {
	decision := &HarnessSkillDecision{
		Selected:       true,
		Name:           entry.Name,
		Description:    entry.Description,
		Path:           entry.Path,
		Reason:         strings.TrimSpace(reason),
		AvailableCount: availableCount,
	}
	loaded, err := loadFullSkill(entry)
	if err != nil {
		decision.Error = err.Error()
		return decision, nil
	}
	return decision, &loaded
}

func explicitSkillSelection(index []SkillIndexEntry, prompt string) (SkillIndexEntry, string, bool) {
	lower := strings.ToLower(prompt)
	for _, entry := range index {
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if name == "" {
			continue
		}
		if containsSkillName(prompt, name) {
			return entry, "user mentioned " + entry.Name, true
		}
		candidates := []string{"$" + name, "use " + name, "using " + name}
		for _, candidate := range candidates {
			if strings.Contains(lower, candidate) {
				return entry, "user explicitly referenced " + entry.Name, true
			}
		}
	}
	return SkillIndexEntry{}, "", false
}

// prepareChatTurnLoop is the harness planning loop: the planner model is called
// with the conversation so far, every requested tool call is executed, and each
// result — including failures and denials — is appended back as a tool message
// for the next planning round. The loop is bounded by harnessChatMaxSteps
// planning rounds and harnessChatMaxWallTime of wall time.
func (h *HarnessEngine) prepareChatTurnLoop(ctx context.Context, requestID, conversationID string, req ChatRequest, turn harnessTurnContext, run *HarnessRun) (HarnessPreparedTurn, error) {
	skillDecision, loadedSkill := h.selectSkillForTurn(ctx, req, turn)
	run.Skill = skillDecision
	registry := h.toolRegistry()
	// The planner prompt and plan-parsing differ between the two paths, but the
	// bounded loop, telemetry recording, tool execution, and result feedback
	// are shared.
	var system string
	if turn.UseNativeTools {
		system = h.plannerSystemPromptNative(registry, req, loadedSkill, turn.ToolTask)
	} else {
		system = h.plannerSystemPrompt(registry, req, loadedSkill, turn.ToolTask)
	}
	numCtx := h.numCtx()
	budget := historyBudgetChars(numCtx, system, harnessPlanNumPredict)
	messages := append([]ChatMessage{}, req.Messages...)
	deadline := time.Now().Add(harnessChatMaxWallTime)

	prepared := HarnessPreparedTurn{SkillDecision: skillDecision, LoadedSkill: loadedSkill}
	for iteration := 1; iteration <= harnessChatMaxSteps; iteration++ {
		planning := run.appendStep("planning", iteration, turn.Harness.provider, req.Model, fmt.Sprintf("harness planning round %d", iteration))
		prepReq := ChatRequest{
			BaseURL:  req.BaseURL,
			Model:    req.Model,
			Provider: turn.Harness.provider,
			System:   system,
			Messages: truncateChatHistory(messages, budget),
			Options: map[string]any{
				"temperature": 0,
				"num_predict": harnessPlanNumPredict,
				"num_ctx":     numCtx,
			},
		}
		if turn.UseNativeTools {
			prepReq.Tools = ollamaToolSpecs(registry)
		} else {
			prepReq.Format = harnessToolPlanSchema(registry)
		}
		completion, err := h.completeWithHarnessModel(ctx, turn.Harness, prepReq)
		if err != nil {
			run.completeStep(planning, "failed", "", 0, err.Error())
			return HarnessPreparedTurn{}, err
		}

		// Parse the planner response into a common plan shape. Both paths
		// produce {brief, needsTools, reason, toolCalls, validationErrors}.
		var plan HarnessToolPlan
		var validationErrors []string
		if turn.UseNativeTools {
			plan, validationErrors = parseNativePlannerResponse(completion, registry)
		} else {
			plan, validationErrors = parseHarnessToolPlanWithRegistry(completion.Content, registry)
			if len(validationErrors) > 0 && strings.TrimSpace(completion.Reason) == "length" {
				validationErrors = append([]string{"the plan response hit the output token limit and was cut off; return a shorter plan"}, validationErrors...)
			}
		}
		round := HarnessToolRound{
			Iteration:            iteration,
			Brief:                strings.TrimSpace(plan.Brief),
			NeedsTools:           plan.NeedsTools,
			Reason:               strings.TrimSpace(plan.Reason),
			Completion:           completion,
			PlanValidationErrors: validationErrors,
		}
		prepared.Completion = completion
		prepared.PlanValidationErrors = validationErrors

		if len(validationErrors) > 0 {
			run.Steps[planning].Summary = "plan failed validation; errors fed back to the planner"
			run.completeStep(planning, "completed", completion.Reason, completion.EvalTokens, "")
			prepared.Rounds = append(prepared.Rounds, round)
			prepared.ToolCalls = nil
			messages = append(messages, h.plannerCorrectionMessages(turn.UseNativeTools, completion, validationErrors)...)
			continue
		}
		run.completeStep(planning, "completed", completion.Reason, completion.EvalTokens, "")

		prepared.ToolCalls = plan.ToolCalls
		if !plan.NeedsTools || len(plan.ToolCalls) == 0 {
			prepared.Rounds = append(prepared.Rounds, round)
			break
		}

		toolStep := run.appendStep("tool_call", iteration, "tools", "", "tool calls requested by harness planning")
		results := h.runHarnessToolCalls(ctx, requestID, conversationID, plan.ToolCalls, turn)
		round.ToolResults = results
		prepared.Rounds = append(prepared.Rounds, round)
		prepared.ToolResults = append(prepared.ToolResults, results...)
		run.Steps[toolStep].Tools = h.toolActivities(results, plan.ToolCalls)
		run.completeStep(toolStep, "completed", "tool_call", 0, "")

		if time.Now().After(deadline) {
			break
		}
		messages = append(messages, h.plannerAssistantMessage(turn.UseNativeTools, completion))
		messages = append(messages, toolResultMessages(results)...)
	}
	run.Loop.Iterations = len(prepared.Rounds)
	return prepared, nil
}

// plannerAssistantMessage renders the assistant turn to append back into the
// planner's message history after a tool round. Native tool-calls must be
// echoed as tool_calls on the assistant message so Ollama's native loop keeps
// role ordering valid; the format-schema path echoes only the JSON content.
func (h *HarnessEngine) plannerAssistantMessage(useNativeTools bool, completion ChatCompletionResult) ChatMessage {
	if useNativeTools && len(completion.ToolCalls) > 0 {
		return ChatMessage{Role: "assistant", Content: completion.Content, ToolCalls: completion.ToolCalls}
	}
	return ChatMessage{Role: "assistant", Content: completion.Content}
}

// plannerCorrectionMessages renders the feedback for an invalid plan. The
// format-schema path uses a user-role correction request (the model emits a
// corrected JSON plan). The native path reports the failure as a tool-role
// message — the idiomatic channel for a tool that rejected its arguments — so
// the model's native tool-calling loop can recover in the next round.
func (h *HarnessEngine) plannerCorrectionMessages(useNativeTools bool, completion ChatCompletionResult, validationErrors []string) []ChatMessage {
	if !useNativeTools {
		return []ChatMessage{
			{Role: "assistant", Content: completion.Content},
			{Role: "user", Content: "Your previous response was not a valid tool plan:\n" + validationErrorsMarkdown(validationErrors) + "\n\nReturn a corrected plan that matches the response schema."},
		}
	}
	assistant := ChatMessage{Role: "assistant", Content: completion.Content}
	if len(completion.ToolCalls) > 0 {
		assistant.ToolCalls = completion.ToolCalls
	}
	failed := HarnessToolResult{
		Name:    "planner",
		Status:  "failed",
		Error:   "the tool plan was rejected: " + strings.Join(validationErrors, "; "),
		Summary: "plan validation failed",
	}
	return []ChatMessage{assistant, ChatMessage{Role: "tool", Content: fmt.Sprintf(`{"name":"planner","status":"failed","error":%q}`, failed.Error)}}
}

// parseNativePlannerResponse converts a native tool-calling completion into the
// common plan shape. The model's content becomes the brief (telemetry only),
// needsTools is inferred from whether any tool calls were emitted, and each
// call is validated against the registry the same way the format-schema path
// validates its JSON plan — minus the envelope constraints (required brief/
// reason, needsTools consistency) that only make sense for the schema envelope.
func parseNativePlannerResponse(completion ChatCompletionResult, registry HarnessToolRegistry) (HarnessToolPlan, []string) {
	calls, validationErrors := mapNativeToolCalls(completion.ToolCalls)
	if len(calls) > 3 {
		validationErrors = append(validationErrors, "toolCalls may contain at most 3 calls")
	}
	for index, call := range calls {
		validationErrors = append(validationErrors, validateHarnessToolCall(index, call, registry)...)
	}
	// Truncation guard: a native response that hit the output limit with no
	// surviving tool calls is indistinguishable from "decided no tools are
	// needed", but here it almost always means the model spent its token budget
	// on thinking and the tool_calls were cut off. Treat that as a validation
	// error so the loop retries (mirroring the format-schema path's length
	// handling) instead of silently concluding the turn needs no tools.
	if strings.TrimSpace(completion.Reason) == "length" && len(calls) == 0 {
		validationErrors = append([]string{"the tool plan hit the output token limit before any tool call was emitted; emit tool calls first and keep reasoning short"}, validationErrors...)
	}
	return HarnessToolPlan{
		Brief:      completion.Content,
		NeedsTools: len(calls) > 0,
		Reason:     "",
		ToolCalls:  calls,
	}, validationErrors
}

// mapNativeToolCalls converts Ollama's native tool_calls into the flat
// HarnessToolCall shape the gateway expects. Each call's arguments JSON object
// is unmarshalled directly onto a HarnessToolCall, whose fields match the tool
// parameter names. A per-call decode error is reported rather than failing the
// whole round, mirroring decodeHarnessToolCalls.
func mapNativeToolCalls(calls []ToolCall) ([]HarnessToolCall, []string) {
	if len(calls) == 0 {
		return nil, nil
	}
	mapped := make([]HarnessToolCall, 0, len(calls))
	var problems []string
	for index, call := range calls {
		name := strings.TrimSpace(call.Function.Name)
		var harnessCall HarnessToolCall
		harnessCall.Name = name
		args := bytes.TrimSpace(call.Function.Arguments)
		if len(args) > 0 && string(args) != "null" {
			if err := json.Unmarshal(args, &harnessCall); err != nil {
				problems = append(problems, fmt.Sprintf("toolCalls[%d].arguments could not be parsed: %v", index, err))
				continue
			}
		}
		harnessCall.Name = name
		mapped = append(mapped, harnessCall)
	}
	return mapped, problems
}

func (h *HarnessEngine) plannerSystemPrompt(registry HarnessToolRegistry, req ChatRequest, loadedSkill *LoadedSkill, toolTask string) string {
	system := strings.TrimSpace(fmt.Sprintf(`You are Atelier's private harness model. You gather evidence for the final model that will answer the user.
Do not answer the user directly. Do not include hidden chain-of-thought. Respond only with a JSON tool plan matching the response schema:
{
  "brief": "concise guidance for the primary model",
  "needsTools": false,
  "reason": "why tools are or are not needed",
  "toolCalls": []
}
You plan in rounds, at most %d in total. Each round may request up to 3 tool calls. The harness executes them and returns each result, including failures, as a tool message; read the results and plan the next round.
When you have enough evidence, or none is needed, set "needsTools" false with empty "toolCalls" and write the brief: intent, constraints, relevant evidence, response shape, and cautions for the final model.
A failed or denied tool call is information, not a dead end: adapt the plan or tell the final model to report the failure plainly. Never claim an action succeeded unless a tool result shows it.
The primary model that answers the user cannot call tools or execute commands. If a user request or active SKILL.md requires a command, include it as a tool call now. Do not put instructions like "run this command" in the brief.
Allowed tool calls:
%s
When "needsTools" is false, "toolCalls" must be []. Prefer read-only calls unless the user clearly asks to modify files or run a specific write-capable command. The filesystem tools and run_command operate on real files on this machine; paths and command working directories are confined to (but fully real within) %s.`, harnessChatMaxSteps, registry.PromptCatalog(), workspaceRootPhrase(h.config.Tools.Filesystem)))
	if strings.TrimSpace(req.System) != "" {
		system += "\n\nUser-facing system prompt to preserve:\n" + strings.TrimSpace(req.System)
	}
	if loadedSkill != nil {
		system += "\n\nActive SKILL.md selected for this turn. Follow these instructions when planning tools and writing the brief, including any workflow or command guidance that applies. Do not quote the skill unless the user asks about process.\n\n" + loadedSkill.Body
	}
	if strings.TrimSpace(toolTask) != "" {
		system += "\n\nThe primary model triaged this turn and requested tool evidence:\n" + strings.TrimSpace(toolTask)
	}
	return system
}

// plannerSystemPromptNative is the native tool-calling variant: it keeps the
// role, skill, workspace-root, and tool-task guidance, but drops the JSON
// envelope description and instead instructs the model to call its tools for
// evidence and, when done, write a one-line plan summary in content with no
// tool calls. That content becomes the round's brief (telemetry only).
func (h *HarnessEngine) plannerSystemPromptNative(registry HarnessToolRegistry, req ChatRequest, loadedSkill *LoadedSkill, toolTask string) string {
	system := strings.TrimSpace(fmt.Sprintf(`You are Atelier's private harness model. You gather evidence for the final model that will answer the user.
Do not answer the user directly. Do not include hidden chain-of-thought.
You have tools available. Call them to gather evidence for the final model. You plan in rounds, at most %d in total; each round may request up to 3 tool calls. The harness executes them and returns each result, including failures, as a tool message; read the results and plan the next round.
When you have enough evidence, or none is needed, make no tool calls and write a one-line summary of your plan in your content: intent, relevant evidence, and cautions for the final model. The final model cannot call tools, so include any required command as a tool call now, not in your summary.
A failed or denied tool call is information, not a dead end: adapt the plan or tell the final model to report the failure plainly. Never claim an action succeeded unless a tool result shows it.
The filesystem tools and run_command operate on real files on this machine; paths and command working directories are confined to (but fully real within) %s. Prefer read-only calls unless the user clearly asks to modify files or run a specific write-capable command.`, harnessChatMaxSteps, workspaceRootPhrase(h.config.Tools.Filesystem)))
	if strings.TrimSpace(req.System) != "" {
		system += "\n\nUser-facing system prompt to preserve:\n" + strings.TrimSpace(req.System)
	}
	if loadedSkill != nil {
		system += "\n\nActive SKILL.md selected for this turn. Follow these instructions when planning tools and writing the summary, including any workflow or command guidance that applies. Do not quote the skill unless the user asks about process.\n\n" + loadedSkill.Body
	}
	if strings.TrimSpace(toolTask) != "" {
		system += "\n\nThe primary model triaged this turn and requested tool evidence:\n" + strings.TrimSpace(toolTask)
	}
	return system
}

// harnessToolPlanSchema is sent as the Ollama structured-output format for
// planner calls, so plan responses are grammar-constrained to valid JSON.
func harnessToolPlanSchema(registry HarnessToolRegistry) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"brief", "needsTools", "reason", "toolCalls"},
		"properties": map[string]any{
			"brief":      map[string]any{"type": "string"},
			"needsTools": map[string]any{"type": "boolean"},
			"reason":     map[string]any{"type": "string"},
			"toolCalls": map[string]any{
				"type":     "array",
				"maxItems": 3,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"name"},
					"properties": map[string]any{
						"name":        map[string]any{"type": "string", "enum": registry.Names()},
						"command":     map[string]any{"type": "string"},
						"args":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"cwd":         map[string]any{"type": "string"},
						"timeoutMs":   map[string]any{"type": "integer"},
						"path":        map[string]any{"type": "string"},
						"content":     map[string]any{"type": "string"},
						"append":      map[string]any{"type": "boolean"},
						"overwrite":   map[string]any{"type": "boolean"},
						"maxBytes":    map[string]any{"type": "integer"},
						"allowBinary": map[string]any{"type": "boolean"},
					},
				},
			},
		},
	}
}

// preparedResponseRequest builds the ChatRequest sent to the final response
// model. attachedImage is the turn's resolved source image (current-turn
// attachment or the most recent image in history, including model-generated
// ones); when the last user message carries no image of its own, it is injected
// there so a vision-capable primary model can see — and be asked about — an
// image the model produced in an earlier turn (e.g. "describe the image you
// just generated"). It is injected as an Images entry on the last user message
// to match the shape adapters expect, and before stripUnsupportedMedia so the
// same capability logic that governs user-attached images governs it.
func (h *HarnessEngine) preparedResponseRequest(req ChatRequest, responseModel, responseProvider string, preparation HarnessPreparedTurn, attachedImage string) ChatRequest {
	responseReq := req
	responseReq.Model = responseModel
	responseReq.Provider = responseProvider
	responseReq.System = appendToolEvidenceToSystem(req.System, preparation)
	messages := append([]ChatMessage{}, req.Messages...)
	// If the turn resolved a source image from history (e.g. a model-generated
	// image from an earlier turn) but the last user message has none, attach it
	// so a vision-capable response model can answer questions about it. A
	// genuinely-attached current-turn image already rides on the last user
	// message, so the len()==0 guard avoids duplicating it.
	if attachedImage = strings.TrimSpace(attachedImage); attachedImage != "" {
		if i := lastUserMessageIndex(messages); i >= 0 && len(messages[i].Images) == 0 {
			messages[i].Images = append(messages[i].Images, attachedImage)
		}
	}
	// Strip user-attached media the response model cannot accept, so a request
	// is never rejected with a modality 404 (e.g. sending input_audio to
	// mistral-large, whose input_modalities are text+image+file). The check is
	// capability-based for OpenRouter (architecture.input_modalities) and
	// permissive for Ollama (which silently drops media it can't handle) and for
	// an unknown provider/model (avoid breaking a working path on a lookup miss).
	// Images are left intact unless explicitly unsupported: nearly all chat models
	// are multimodal, and an unnecessary strip would break vision.
	h.stripUnsupportedMedia(messages, responseModel, responseProvider)
	if len(preparation.ToolResults) > 0 {
		messages = append(messages, toolEvidenceUserMessage(preparation.ToolResults))
	}
	numCtx := h.numCtx()
	responseReq.Messages = truncateChatHistory(messages, historyBudgetChars(numCtx, responseReq.System, numCtx/4))
	responseReq.Options = withNumCtx(req.Options, numCtx)
	return responseReq
}

// stripUnsupportedMedia removes user-attached audio and video bytes from
// messages in place when the response model's published input modalities do not
// include them. It is a no-op when the model accepts the modality, when the
// provider isn't OpenRouter (Ollama drops unknown fields silently), or when the
// capability lookup fails (permissive — prefer a possible 404 over dropping
// media a model could have handled). Images are never stripped here: vision
// support is near-universal among chat models, and a false-negative strip would
// silently break image-input turns.
func (h *HarnessEngine) stripUnsupportedMedia(messages []ChatMessage, model, provider string) {
	if provider != "openrouter" || h.app == nil {
		return
	}
	client, ok := h.app.openRouterClient()
	if !ok {
		return
	}
	caps, err := client.ModelCapabilities(context.Background(), strings.TrimSpace(model))
	if err != nil {
		return // lookup miss — leave media intact (permissive)
	}
	if !caps.acceptsInputModality("audio") {
		for i := range messages {
			messages[i].Audios = nil
		}
	}
	if !caps.acceptsInputModality("video") {
		for i := range messages {
			messages[i].Videos = nil
		}
	}
}

// toolObservationsPrefix labels tool results carried in a user-role message.
// Shared with the OpenRouter adapter (openRouterMessages), which rewrites
// tool-role messages the OpenAI wire format cannot express, so both paths
// present observations to a model in the same vocabulary.
const toolObservationsPrefix = "[Tool observations]\n"

// toolEvidenceUserMessage renders tool results as a single user-role message
// so that providers enforcing strict role ordering (e.g. Mistral via
// OpenRouter) never see a bare "tool" role after a "user" role. The primary
// model is not doing native tool-calling — it receives observations as
// evidence — so a user message is the semantically correct container.
func toolEvidenceUserMessage(results []HarnessToolResult) ChatMessage {
	observations := toolResultMessages(results)
	parts := make([]string, 0, len(observations))
	for _, msg := range observations {
		parts = append(parts, msg.Content)
	}
	return ChatMessage{
		Role:    "user",
		Content: toolObservationsPrefix + strings.Join(parts, "\n\n"),
	}
}

func appendToolEvidenceToSystem(system string, preparation HarnessPreparedTurn) string {
	var note string
	switch {
	case len(preparation.PlanValidationErrors) > 0 && len(preparation.ToolResults) > 0:
		note = invalidPlanAfterToolsSystemNote
	case len(preparation.PlanValidationErrors) > 0:
		note = invalidPlanSystemNote
	case len(preparation.ToolResults) > 0:
		note = toolEvidenceSystemNote
	default:
		return system
	}
	if strings.TrimSpace(system) == "" {
		return note
	}
	return strings.TrimSpace(system) + "\n\n" + note
}

// toolResultMessages renders tool results as role:"tool" messages so models
// receive observations in the message stream rather than the system prompt.
// Oversized results are cut down for the message only; history and telemetry
// keep the full result.
func toolResultMessages(results []HarnessToolResult) []ChatMessage {
	messages := make([]ChatMessage, 0, len(results))
	for _, result := range results {
		messageResult := result
		if typed, ok := result.Result.(ToolImageResult); ok {
			typed.Images = nil
			messageResult.Result = typed
		}
		if typed, ok := result.Result.(ToolVideoResult); ok {
			typed.Videos = nil
			messageResult.Result = typed
		}
		if typed, ok := result.Result.(ToolAudioResult); ok {
			typed.Audios = nil
			messageResult.Result = typed
		}
		content, err := json.Marshal(messageResult)
		if err != nil {
			content = []byte(fmt.Sprintf(`{"name":%q,"status":"failed","error":"tool result could not be serialized"}`, result.Name))
		}
		if len(content) > toolResultMessageMaxChars {
			content = compactToolResultMessage(messageResult, string(content))
		}
		messages = append(messages, ChatMessage{Role: "tool", Content: string(content)})
	}
	return messages
}

func compactToolResultMessage(result HarnessToolResult, fullJSON string) []byte {
	preview := truncateRunes(fullJSON, toolResultMessageMaxChars-512)
	compact := HarnessToolResult{
		Name:    result.Name,
		Status:  result.Status,
		Summary: strings.TrimSpace(result.Summary + " (result truncated to fit the model context)"),
		Result:  preview,
		Error:   result.Error,
	}
	content, err := json.Marshal(compact)
	if err != nil {
		return []byte(fmt.Sprintf(`{"name":%q,"status":%q,"summary":"tool result was too large to include"}`, result.Name, result.Status))
	}
	return content
}

func formatHarnessPreparationThinking(preparation HarnessPreparedTurn) string {
	var parts []string
	for _, round := range preparation.Rounds {
		prefix := fmt.Sprintf("Harness preparation %d", round.Iteration)
		if text := strings.TrimSpace(round.Brief); text != "" {
			parts = append(parts, "### "+prefix+"\n\n"+text)
		}
		if strings.TrimSpace(round.Reason) != "" {
			parts = append(parts, fmt.Sprintf("### Tool decision %d\n\n%s", round.Iteration, round.Reason))
		}
		if len(round.PlanValidationErrors) > 0 {
			parts = append(parts, fmt.Sprintf("### Harness plan validation %d\n\n%s", round.Iteration, validationErrorsMarkdown(round.PlanValidationErrors)))
		}
		if len(round.ToolCalls) > 0 {
			if data, err := json.MarshalIndent(round.ToolCalls, "", "  "); err == nil {
				parts = append(parts, fmt.Sprintf("### Tool plan %d\n\n```json\n%s\n```", round.Iteration, string(data)))
			}
		}
		if len(round.ToolResults) > 0 {
			if data, err := json.MarshalIndent(round.ToolResults, "", "  "); err == nil {
				parts = append(parts, fmt.Sprintf("### Tool results %d\n\n```json\n%s\n```", round.Iteration, string(data)))
			}
		}
		if text := strings.TrimSpace(round.Completion.Thinking); text != "" {
			parts = append(parts, fmt.Sprintf("### Harness model thinking %d\n\n%s", round.Iteration, text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func validationErrorsMarkdown(errors []string) string {
	lines := make([]string, 0, len(errors))
	for _, err := range errors {
		if text := strings.TrimSpace(err); text != "" {
			lines = append(lines, "- "+text)
		}
	}
	return strings.Join(lines, "\n")
}

func parseHarnessToolPlanWithRegistry(content string, registry HarnessToolRegistry) (HarnessToolPlan, []string) {
	return decodeAndValidateHarnessToolPlan(stripJSONFence(content), registry)
}

// stripJSONFence tolerates a single markdown code fence around an otherwise
// structured-output JSON response.
func stripJSONFence(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	withoutOpen := trimmed[3:]
	if newline := strings.Index(withoutOpen, "\n"); newline >= 0 {
		withoutOpen = withoutOpen[newline+1:]
	}
	if end := strings.LastIndex(withoutOpen, "```"); end >= 0 {
		withoutOpen = withoutOpen[:end]
	}
	return strings.TrimSpace(withoutOpen)
}

func decodeAndValidateHarnessToolPlan(candidate string, registry HarnessToolRegistry) (HarnessToolPlan, []string) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &raw); err != nil {
		return HarnessToolPlan{}, []string{"plan JSON could not be parsed: " + err.Error()}
	}
	errors := validateHarnessPlanKeys(raw)
	var plan HarnessToolPlan
	if data, ok := raw["brief"]; ok {
		if err := json.Unmarshal(data, &plan.Brief); err != nil {
			errors = append(errors, "brief must be a string")
		}
	}
	if data, ok := raw["needsTools"]; ok {
		if err := json.Unmarshal(data, &plan.NeedsTools); err != nil {
			errors = append(errors, "needsTools must be a boolean")
		}
	}
	if data, ok := raw["reason"]; ok {
		if err := json.Unmarshal(data, &plan.Reason); err != nil {
			errors = append(errors, "reason must be a string")
		}
	}
	if data, ok := raw["toolCalls"]; ok {
		errors = append(errors, decodeHarnessToolCalls(data, &plan.ToolCalls)...)
	}
	errors = append(errors, validateHarnessToolPlan(plan, registry)...)
	return plan, errors
}

// decodeHarnessToolCalls reports per-element errors when toolCalls is a valid
// array whose elements fail to decode, so the planner model learns which field
// was wrong instead of a blanket "must be an array" message.
func decodeHarnessToolCalls(data json.RawMessage, calls *[]HarnessToolCall) []string {
	if err := json.Unmarshal(data, calls); err == nil {
		return nil
	}
	var elements []json.RawMessage
	if json.Unmarshal(data, &elements) != nil {
		return []string{"toolCalls must be an array of tool call objects"}
	}
	var problems []string
	// json fills the fields it can before reporting a type error, so
	// partially-decoded calls stay in the plan and downstream validation
	// keeps the original indexes.
	decoded := make([]HarnessToolCall, len(elements))
	for index, element := range elements {
		if err := json.Unmarshal(element, &decoded[index]); err != nil {
			problems = append(problems, describeHarnessToolCallDecodeError(index, err))
		}
	}
	*calls = decoded
	return problems
}

func describeHarnessToolCallDecodeError(index int, err error) string {
	prefix := fmt.Sprintf("toolCalls[%d]", index)
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return prefix + " must be a tool call object"
	}
	message := fmt.Sprintf("%s.%s must be %s, got %s", prefix, typeErr.Field, friendlyJSONFieldType(typeErr.Type.String()), typeErr.Value)
	if typeErr.Field == "args" && typeErr.Value == "object" {
		message += "; tool parameters like path go directly on the call object, not nested under args"
	}
	return message
}

func friendlyJSONFieldType(goType string) string {
	switch goType {
	case "string":
		return "a string"
	case "bool":
		return "a boolean"
	case "int":
		return "a number"
	case "[]string":
		return "an array of strings"
	case "map[string]string":
		return "an object with string values"
	}
	return "a " + goType + " value"
}

func validateHarnessPlanKeys(raw map[string]json.RawMessage) []string {
	allowed := map[string]bool{
		"brief":      true,
		"needsTools": true,
		"reason":     true,
		"toolCalls":  true,
	}
	required := []string{"brief", "needsTools", "reason", "toolCalls"}
	var errors []string
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			errors = append(errors, key+" is required")
		}
	}
	for key := range raw {
		if !allowed[key] {
			errors = append(errors, "unknown field "+key)
		}
	}
	return errors
}

func validateHarnessToolPlan(plan HarnessToolPlan, registry HarnessToolRegistry) []string {
	var errors []string
	if strings.TrimSpace(plan.Brief) == "" {
		errors = append(errors, "brief is required")
	}
	if strings.TrimSpace(plan.Reason) == "" {
		errors = append(errors, "reason is required")
	}
	if !plan.NeedsTools && len(plan.ToolCalls) > 0 {
		errors = append(errors, "toolCalls must be empty when needsTools is false")
	}
	if plan.NeedsTools && len(plan.ToolCalls) == 0 {
		errors = append(errors, "toolCalls must include at least one call when needsTools is true")
	}
	if len(plan.ToolCalls) > 3 {
		errors = append(errors, "toolCalls may contain at most 3 calls")
	}
	for index, call := range plan.ToolCalls {
		errors = append(errors, validateHarnessToolCall(index, call, registry)...)
	}
	return errors
}

func validateHarnessToolCall(index int, call HarnessToolCall, registry HarnessToolRegistry) []string {
	prefix := fmt.Sprintf("toolCalls[%d]", index)
	name := strings.TrimSpace(call.Name)
	if name == "" {
		return []string{prefix + ".name is required"}
	}
	definition, ok := registry.Get(name)
	if !ok {
		return []string{prefix + ".name must be one of " + registry.NamesCSV()}
	}
	if definition.Validate == nil {
		return nil
	}
	return definition.Validate(prefix, call)
}

func (h *HarnessEngine) runHarnessToolCalls(ctx context.Context, requestID, conversationID string, calls []HarnessToolCall, turn harnessTurnContext) []HarnessToolResult {
	// Pass the engine's cached registry so the gateway doesn't rebuild the
	// param-schema maps on every planning round.
	gateway := newToolGateway(h.app, h.config, h.toolRegistry())
	// Hand the turn's attached image and audio to the tool context so generate_video
	// can animate the image (image-to-video) and transcribe_audio can transcribe the
	// audio. Empty for turns without the corresponding attachment.
	gateway.tools.AttachedImage = turn.AttachedImage
	gateway.tools.AttachedAudio = turn.AttachedAudio
	gateway.tools.AttachedVideo = turn.AttachedVideo
	results := make([]HarnessToolResult, 0, len(calls))
	for _, call := range calls {
		// When the user selected a model that is not the harness model as the
		// primary model and the turn is in image mode, use that model for
		// generate_image instead of the configured default. This lets the user
		// pick a different image model per turn. When the primary model IS the
		// harness model (a text model), the configured image model is correct.
		// This override only applies when the primary model lives on the same
		// provider as the image backend: a single-provider (all-Ollama) setup
		// where the user picked a different Ollama model for the turn. When the
		// chat provider differs from the image provider (e.g. OpenRouter chat
		// with Ollama images), the primary model is unrelated to image
		// generation — sending it to the image endpoint is a wrong-model 404.
		// fal.ai as the image provider is likewise excluded: the primary (chat)
		// model is unrelated to fal's image endpoints.
		imageProvider := strings.TrimSpace(h.config.Models.ImageProvider)
		primaryOnImageProvider := turn.PrimaryProvider != "" && turn.PrimaryProvider == imageProvider
		if call.Name == "generate_image" && turn.ResponseMode == "image" &&
			imageProvider != "fal" && primaryOnImageProvider &&
			turn.PrimaryModel != "" && turn.PrimaryModel != h.config.Providers.Ollama.Models.Harness {
			if strings.TrimSpace(call.Model) == "" {
				call.Model = turn.PrimaryModel
			}
		}
		results = append(results, gateway.Execute(ctx, ToolExecutionRequest{
			Name:           call.Name,
			Call:           call,
			RequestID:      requestID,
			ConversationID: conversationID,
			Source:         "harness",
		}))
	}
	return results
}

func (h *HarnessEngine) toolActivities(results []HarnessToolResult, calls []HarnessToolCall) []HarnessToolActivity {
	activities := make([]HarnessToolActivity, 0, len(results))
	// runHarnessToolCalls emits one result per call, so results and calls are
	// 1:1. Bound the zip by the shorter slice defensively — a future caller that
	// splits or drops results should not panic, it should simply leave the
	// activity's Call at its zero value.
	for i, result := range results {
		activity := h.toolActivityFromResult(result)
		if i < len(calls) {
			activity.Call = calls[i]
		}
		activities = append(activities, activity)
	}
	return activities
}

func (h *HarnessEngine) toolActivityFromResult(result HarnessToolResult) HarnessToolActivity {
	if definition, ok := h.toolRegistry().Get(result.Name); ok && definition.Activity != nil {
		return definition.Activity(result)
	}
	return defaultHarnessToolActivity(result)
}

// harnessEmptyResponseNotice speaks in the harness's own voice when the
// response model produced no content. It reports tool outcomes verbatim and
// never phrases anything as if the model had answered.
func harnessEmptyResponseNotice(results []HarnessToolResult) string {
	lines := []string{"Atelier harness notice: the response model returned no content for this turn."}
	if len(results) > 0 {
		lines = append(lines, "Tool activity this turn:")
		for _, result := range results {
			name := strings.TrimSpace(result.Name)
			if name == "" {
				name = "tool"
			}
			var line string
			switch result.Status {
			case "completed":
				line = fmt.Sprintf("- `%s` completed.", name)
				if detail := fallbackToolResultDetail(result); detail != "" {
					line += " " + detail
				}
			case "denied":
				line = fmt.Sprintf("- `%s` was denied: %s", name, toolResultErrorDetail(result))
			default:
				line = fmt.Sprintf("- `%s` failed: %s", name, toolResultErrorDetail(result))
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func toolResultErrorDetail(result HarnessToolResult) string {
	if errorText := strings.TrimSpace(result.Error); errorText != "" {
		return errorText
	}
	if summary := strings.TrimSpace(result.Summary); summary != "" {
		return summary
	}
	return "no detail available"
}

func fallbackToolResultDetail(result HarnessToolResult) string {
	switch typed := result.Result.(type) {
	case ToolCommandResult:
		return fallbackCommandResultDetail(typed)
	case ToolFileReadResult:
		if typed.Path != "" {
			return fmt.Sprintf("Read `%s`.", shortenLocalPath(typed.Path))
		}
	case ToolFileListResult:
		if typed.Path != "" {
			return fmt.Sprintf("Listed `%s` with %d entr%s.", shortenLocalPath(typed.Path), len(typed.Entries), pluralY(len(typed.Entries)))
		}
	case ToolFileWriteResult:
		if typed.Path != "" {
			return fmt.Sprintf("Wrote `%s`.", shortenLocalPath(typed.Path))
		}
	}
	summary := strings.TrimSpace(result.Summary)
	if summary == "" {
		return ""
	}
	return summary + "."
}

func fallbackCommandResultDetail(result ToolCommandResult) string {
	var parts []string
	if len(result.Command) > 0 {
		parts = append(parts, "Command: `"+formatCommandSummary(result.Command)+"`.")
	}
	if stdout := strings.TrimSpace(result.Stdout); stdout != "" {
		parts = append(parts, "Output: `"+previewInline(stdout)+"`.")
	}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		parts = append(parts, "Details: `"+previewInline(stderr)+"`.")
	}
	return strings.Join(parts, " ")
}

func previewInline(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return truncateRunes(text, 220)
}

func shortenLocalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func pluralY(count int) string {
	if count == 1 {
		return "y"
	}
	return "ies"
}

func harnessToolOutputError(output any) string {
	switch typed := output.(type) {
	case ToolCommandResult:
		return typed.Error
	}
	return ""
}

func previewToolContent(content string) string {
	content = strings.TrimSpace(content)
	return truncateRunesEllipsis(content, 500, "\n...")
}

// truncateRunes returns s truncated to at most max runes, appending the given
// ellipsis when it shortens. It works in runes (not bytes) so a multi-byte
// UTF-8 sequence is never split, which would leave invalid UTF-8 in tool-result
// messages and stdout previews shown to models. A non-positive max returns s
// unchanged.
func truncateRunes(s string, max int) string {
	return truncateRunesEllipsis(s, max, "...")
}

func truncateRunesEllipsis(s string, max int, ellipsis string) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + ellipsis
}

func newHarnessRun(requestID, conversationID string) HarnessRun {
	return HarnessRun{
		ID:             randomID("run"),
		Mode:           "chat",
		Status:         "running",
		StartedAt:      time.Now().Format(time.RFC3339),
		RequestID:      requestID,
		ConversationID: conversationID,
		Loop: HarnessLoop{
			MaxSteps:      harnessChatMaxSteps,
			MaxWallTimeMS: harnessChatMaxWallTime.Milliseconds(),
			Iterations:    0,
		},
	}
}

// fallbackHarnessRun records a turn that was written to history without live
// harness telemetry. It claims only what actually happened: the turn was saved.
func fallbackHarnessRun(model, reason string, tokens int) HarnessRun {
	now := time.Now().Format(time.RFC3339)
	return HarnessRun{
		ID:          randomID("run"),
		Mode:        "chat",
		Status:      "completed",
		StartedAt:   now,
		CompletedAt: now,
		Loop: HarnessLoop{
			MaxSteps:      harnessChatMaxSteps,
			MaxWallTimeMS: harnessChatMaxWallTime.Milliseconds(),
			Iterations:    1,
			StopReason:    "final",
		},
		Steps: []HarnessStep{
			{
				ID:          "step_000001",
				Kind:        "saved",
				Iteration:   1,
				Provider:    "ollama",
				Model:       model,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				DoneReason:  reason,
				Tokens:      tokens,
				Summary:     "assistant turn stored without live harness telemetry",
			},
		},
	}
}

// appendStep records a step the moment it starts and returns its index for
// completion. Steps are only ever appended, in the order they actually happen.
func (run *HarnessRun) appendStep(kind string, iteration int, provider, model, summary string) int {
	run.Steps = append(run.Steps, HarnessStep{
		ID:        fmt.Sprintf("step_%06d", len(run.Steps)+1),
		Kind:      kind,
		Iteration: iteration,
		Provider:  provider,
		Model:     model,
		Status:    "running",
		StartedAt: time.Now().Format(time.RFC3339),
		Summary:   summary,
	})
	return len(run.Steps) - 1
}

func (run *HarnessRun) completeStep(index int, status, reason string, tokens int, errorText string) {
	if index < 0 || index >= len(run.Steps) {
		return
	}
	step := &run.Steps[index]
	step.Status = status
	step.CompletedAt = time.Now().Format(time.RFC3339)
	step.DoneReason = reason
	step.Tokens = tokens
	step.Error = errorText
	if startedAt, err := time.Parse(time.RFC3339, step.StartedAt); err == nil {
		step.DurationMS = time.Since(startedAt).Milliseconds()
	}
}

func (run *HarnessRun) complete(status, stopReason string) {
	run.Status = status
	completedAt := time.Now()
	run.CompletedAt = completedAt.Format(time.RFC3339)
	if startedAt, err := time.Parse(time.RFC3339, run.StartedAt); err == nil {
		run.DurationMS = completedAt.Sub(startedAt).Milliseconds()
	}
	run.Loop.StopReason = stopReason
}

func (h *HarnessEngine) evaluateChatRun(run *HarnessRun, assistantContent, doneReason string) {
	decision := "final"
	summary := "assistant response is user-visible final output"
	if strings.TrimSpace(assistantContent) == "" && strings.TrimSpace(doneReason) == "" {
		decision = "stop"
		summary = "no assistant content to continue from"
	}
	index := run.appendStep("evaluation", 1, "", "", summary)
	run.Steps[index].Decision = decision
	run.completeStep(index, "completed", doneReason, 0, "")
	run.Loop.StopReason = decision
}

func (h *HarnessEngine) SaveChatTurn(req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, title string, run HarnessRun) (string, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return writeChatConversation(h.config, req, assistantContent, assistantThinking, model, provider, reason, tokens, title, run)
	}
	return appendChatConversation(h.config, req, assistantContent, assistantThinking, model, provider, reason, tokens, run)
}

func (h *HarnessEngine) StartChatTurn(req ChatRequest) (string, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return writePendingChatConversation(h.config, req)
	}
	return appendChatUserTurn(h.config, req)
}

func (h *HarnessEngine) SaveAssistantTurn(conversationID, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun) error {
	if strings.TrimSpace(assistantContent) == "" && strings.TrimSpace(assistantThinking) == "" {
		return nil
	}
	return appendChatAssistantTurn(h.config, conversationID, assistantContent, assistantThinking, model, provider, reason, tokens, run)
}
