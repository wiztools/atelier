package main

import (
	"encoding/json"
	"errors"
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
		return HarnessTriageDecision{}, errors.New("no valid triage decision JSON found")
	}
	return decision, nil
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
