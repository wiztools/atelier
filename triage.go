package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const triageNumPredict = 256

// HarnessTriageDecision is the chat model's routing decision for a turn. It is
// stored on the HarnessRun for telemetry; Error records a triage failure that
// forced the fail-safe tool path.
type HarnessTriageDecision struct {
	NeedsTools bool   `json:"needsTools"`
	ToolTask   string `json:"toolTask,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

func triageResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"needsTools", "toolTask", "reason"},
		"properties": map[string]any{
			"needsTools": map[string]any{"type": "boolean"},
			"toolTask":   map[string]any{"type": "string"},
			"reason":     map[string]any{"type": "string"},
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

// messagesWithoutImages copies messages for text-only side calls such as
// triage, so image payloads never reach a model that only routes the turn.
func messagesWithoutImages(messages []ChatMessage) []ChatMessage {
	stripped := make([]ChatMessage, len(messages))
	for index, message := range messages {
		message.Images = nil
		stripped[index] = message
	}
	return stripped
}

// triageChatTurn asks the chat model whether the turn needs tools. Failures
// fail safe to the tool path: the planner there can still conclude no tools
// are needed, so a wrong fallback costs latency, never correctness.
func (h *HarnessEngine) triageChatTurn(ctx context.Context, req ChatRequest, chatModel string, skillIndex []SkillIndexEntry) (HarnessTriageDecision, ChatCompletionResult) {
	system := triageSystemPrompt(h.toolRegistry(), skillIndex, h.config.Tools.Filesystem.Root)
	numCtx := h.numCtx()
	triageReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    chatModel,
		System:   system,
		Messages: truncateChatHistory(messagesWithoutImages(req.Messages), historyBudgetChars(numCtx, system, triageNumPredict)),
		Format:   triageResponseSchema(),
		Options: map[string]any{
			"temperature": 0,
			"num_predict": triageNumPredict,
			"num_ctx":     numCtx,
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, triageReq)
	if err != nil {
		return HarnessTriageDecision{NeedsTools: true, Reason: "triage call failed; deferring to the tool model planner", Error: err.Error()}, ChatCompletionResult{}
	}
	decision, err := decodeTriageDecision(completion.Content)
	if err != nil {
		return HarnessTriageDecision{NeedsTools: true, Reason: "triage response was not valid JSON; deferring to the tool model planner", Error: err.Error()}, completion
	}
	return decision, completion
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
	return strings.TrimSpace(fmt.Sprintf(`You are Atelier's chat model deciding whether the latest user turn needs workspace tools before you answer.
You will write the user-visible answer in a separate call. Right now respond only with a JSON object matching the response schema:
{
  "needsTools": false,
  "toolTask": "when needsTools is true, the evidence the tool model should gather",
  "reason": "brief decision reason"
}
Set needsTools true only when answering requires acting on the workspace or a listed capability: reading, listing, searching, or writing files, running a command, generating an image, or following one of the listed skills.
Set needsTools false when your own knowledge is enough: greetings, general knowledge, reasoning, writing, and conversation about content already visible in the chat.
Available tools:
%s
Available skills:
%s
Workspace root: %s`, registry.PromptCatalog(), skills, workspaceRoot))
}
