package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// triageNumPredict caps the harness model's triage output. It must be large
// enough for the four-field JSON to complete on a wordy model — a length-trim
// here drops the only chance to set responseMode, and the fail-safe lands on
// "text" even when the user attached an image that warranted "vision".
const triageNumPredict = 1024

// HarnessTriageDecision is the harness model's routing decision for a turn. It is
// stored on the HarnessRun for telemetry; Error records a triage failure that
// forced the fail-safe tool path.
type HarnessTriageDecision struct {
	NeedsTools   bool   `json:"needsTools"`
	ResponseMode string `json:"responseMode,omitempty"`
	ToolTask     string `json:"toolTask,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Error        string `json:"error,omitempty"`
}

func triageResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"needsTools", "responseMode", "toolTask", "reason"},
		"properties": map[string]any{
			"needsTools":   map[string]any{"type": "boolean"},
			"responseMode": map[string]any{"type": "string", "enum": []string{"text", "image", "vision", "video", "audio"}},
			"toolTask":     map[string]any{"type": "string"},
			"reason":       map[string]any{"type": "string"},
		},
	}
}

func decodeTriageDecision(content string) (HarnessTriageDecision, error) {
	var decision HarnessTriageDecision
	if err := json.Unmarshal([]byte(stripJSONFence(content)), &decision); err != nil {
		return HarnessTriageDecision{}, fmt.Errorf("triage decision JSON invalid: %w", err)
	}
	return decision, nil
}

// messagesWithoutMedia copies messages for text-only side calls such as
// triage, so image, audio, and video payloads never reach a model that only
// routes the turn. Audio and video bytes are stripped for the same reason
// images are: a routing model doesn't need them, and they would bloat the
// triage request for nothing. Stripping video here is also what enforces the
// tool-path-only contract on video input — no adapter emits a video content
// part, so video never reaches a chat model.
func messagesWithoutMedia(messages []ChatMessage) []ChatMessage {
	stripped := make([]ChatMessage, len(messages))
	for index, message := range messages {
		message.Images = nil
		message.Audios = nil
		message.Videos = nil
		stripped[index] = message
	}
	return stripped
}

// triageChatTurn asks the harness model whether the turn needs tools and what
// response mode the primary model should use. Failures fail safe to the tool
// path: the planner there can still conclude no tools are needed, so a wrong
// fallback costs latency, never correctness. The response mode for the fail-
// safe leans toward "vision" when the latest user turn carries an image, so a
// triage decode failure (e.g. output truncated by num_predict) can't strip the
// only signal that would have kept the primary model's attention on the image.
func (h *HarnessEngine) triageChatTurn(ctx context.Context, req ChatRequest, harness harnessTarget, skillIndex []SkillIndexEntry) (HarnessTriageDecision, ChatCompletionResult) {
	system := triageSystemPrompt(h.toolRegistry(), skillIndex, h.config.Tools.Filesystem.Root)
	numCtx := h.numCtx()
	triageReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    harness.model,
		Provider: harness.provider,
		System:   system,
		Messages: truncateChatHistory(messagesWithoutMedia(req.Messages), historyBudgetChars(numCtx, system, triageNumPredict)),
		Format:   triageResponseSchema(),
		Options: map[string]any{
			"temperature": 0,
			"num_predict": triageNumPredict,
			"num_ctx":     numCtx,
		},
	}
	completion, err := h.completeWithHarnessModel(ctx, harness, triageReq)
	if err != nil {
		return triageFailSafe(req, "triage call failed; deferring to the harness model planner", err.Error()), ChatCompletionResult{}
	}
	decision, err := decodeTriageDecision(completion.Content)
	if err != nil {
		return triageFailSafe(req, "triage response was not valid JSON; deferring to the harness model planner", err.Error()), completion
	}
	if decision.ResponseMode == "" {
		decision.ResponseMode = "text"
	}
	return decision, completion
}

// triageFailSafe builds the fail-safe decision for a triage failure: needsTools
// true (the planner can still decline) and responseMode "vision" when the user
// attached an image, otherwise "text". An attached image is the strongest
// signal that the user wanted the image understood; defaulting that case to
// "text" sends the primary model off to look at filesystem evidence instead.
func triageFailSafe(req ChatRequest, reason, errMsg string) HarnessTriageDecision {
	mode := "text"
	if latestUserImage(req.Messages) != "" {
		mode = "vision"
	}
	return HarnessTriageDecision{NeedsTools: true, ResponseMode: mode, Reason: reason, Error: errMsg}
}

func triageSystemPrompt(registry HarnessToolRegistry, skillIndex []SkillIndexEntry, workspaceRoot string) string {
	skills := "(none)"
	if len(skillIndex) > 0 {
		lines := make([]string, 0, len(skillIndex))
		for _, entry := range skillIndex {
			lines = append(lines, "- "+entry.Name+": "+entry.Description)
		}
		skills = strings.Join(lines, "\n")
	}
	return strings.TrimSpace(fmt.Sprintf(`You are Atelier's harness model. You decide how the primary model should respond to the latest user turn and whether workspace tools are needed first.
You will not write the user-visible answer. Right now respond only with a JSON object matching the response schema:
{
  "needsTools": false,
  "responseMode": "text",
  "toolTask": "when needsTools is true, the evidence the harness model should gather",
  "reason": "brief decision reason"
}
Set responseMode to one of:
- "text": the user wants a text response (greetings, general knowledge, reasoning, writing, code, conversation).
- "image": the user asks to create, draw, paint, or render an image.
- "vision": the user attached an image and wants it analyzed, described, or understood.
- "video": the user asks to create, animate, or render a video or short clip.
- "audio": the user asks to speak/narrate text, or create music or a sound effect.
Set needsTools true only when answering requires acting on the workspace or a listed capability: reading, listing, searching, or writing files, running a command, generating an image, generating a video, generating audio, or following one of the listed skills.
Set needsTools false when your own knowledge is enough: greetings, general knowledge, reasoning, writing, and conversation about content already visible in the chat.
For responseMode "image", set needsTools true so the harness can run the generate_image tool before the primary model responds.
For responseMode "video", set needsTools true so the harness can run the generate_video tool before the primary model responds. Only use "video" when the generate_video tool is listed as available.
For responseMode "audio", set needsTools true so the harness can run the generate_audio tool before the primary model responds. Only use "audio" when the generate_audio tool is listed as available.
Available tools:
%s
Available skills:
%s
Workspace root: %s`, registry.PromptCatalog(), skills, workspaceRoot))
}
