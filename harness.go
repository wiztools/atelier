package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	harnessChatMaxSteps    = 3
	harnessChatMaxWallTime = 2 * time.Minute
)

const harnessInvalidPlanBrief = "The harness could not produce a valid executable tool plan, so no tools ran for the latest plan. The final response model cannot call tools or execute commands, so it must not run commands, paste commands as if executed, or claim any tool action succeeded. If the user asked for a tool action, report plainly that it could not be completed."

type HarnessEngine struct {
	config AppConfig
	app    *App
}

type HarnessPreparedTurn struct {
	Brief                string
	NeedsTools           bool
	Reason               string
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
	Append      bool              `json:"append,omitempty"`
	Overwrite   bool              `json:"overwrite,omitempty"`
	MaxBytes    int               `json:"maxBytes,omitempty"`
	AllowBinary bool              `json:"allowBinary,omitempty"`
}

type HarnessToolResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Result  any    `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
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

func (h *HarnessEngine) RunChatStream(ctx context.Context, requestID string, req ChatRequest) {
	if h.app == nil {
		return
	}
	req = h.chatRequestForHarness(req)
	conversationID := strings.TrimSpace(req.ConversationID)
	if !req.turnStarted {
		var err error
		conversationID, err = h.StartChatTurn(req)
		if err != nil {
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history start failed: %v", err), Done: true})
			return
		}
	}
	req.ConversationID = conversationID
	h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, ConversationID: conversationID})
	if h.shouldUseImageTool(req) {
		h.runImageTool(ctx, requestID, conversationID, req)
		return
	}

	responseModel := h.responseModelForChatSelection(req)
	run := newChatHarnessRun(responseModel, "", 0)
	run.Status = "running"
	run.CompletedAt = ""
	run.DurationMS = 0
	run.RequestID = requestID
	run.ConversationID = conversationID
	run.Loop.StopReason = ""
	for index := range run.Steps {
		run.Steps[index].Status = "pending"
		run.Steps[index].CompletedAt = ""
		run.Steps[index].DurationMS = 0
		run.Steps[index].Decision = ""
		run.Steps[index].DoneReason = ""
		run.Steps[index].Error = ""
		run.Steps[index].Tokens = 0
	}
	h.completeStep(&run, "queued", "completed", "", 0, "")
	h.markStepModel(&run, "preparing", "ollama", req.Model)
	h.startStep(&run, "preparing")
	preparation, err := h.prepareChatTurnLoop(ctx, requestID, conversationID, req, &run)
	if err != nil {
		h.completeStep(&run, "preparing", "failed", "", 0, err.Error())
		h.completeRun(&run, "failed", "harness_prepare_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	h.completeStep(&run, "preparing", "completed", preparation.Completion.Reason, preparationTokens(preparation), "")
	preparationThinking := formatHarnessPreparationThinking(preparation)
	if preparationThinking != "" {
		h.app.emitChatEvent(ChatStreamEvent{
			RequestID:      requestID,
			Thinking:       preparationThinking,
			ConversationID: conversationID,
		})
	}

	responseReq := h.preparedResponseRequest(req, responseModel, preparation)
	result, err := h.runFinalResponseAttempt(ctx, requestID, conversationID, responseReq, &run)
	if err != nil {
		h.completeRun(&run, "failed", result.Reason)
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	assistantThinking := preparationThinking + result.Thinking
	assistantContent := result.Content
	finalModel := result.Model
	finalReason := result.Reason
	finalTokens := result.Tokens
	finalContentEmitted := result.Emitted

	if strings.TrimSpace(assistantContent) == "" {
		if fallback := fallbackFinalResponseFromToolResults(preparation.ToolResults); fallback != "" {
			assistantContent = fallback
			finalContentEmitted = false
			if strings.TrimSpace(finalReason) == "" {
				finalReason = "tool_result_fallback"
			}
		}
	}
	if strings.TrimSpace(finalModel) == "" {
		finalModel = responseModel
	}
	h.evaluateChatRun(&run, assistantContent, finalReason)
	h.startStep(&run, "saved")
	h.completeStep(&run, "saved", "completed", finalReason, finalTokens, "")
	h.completeRun(&run, "completed", "final")
	if err := h.SaveAssistantTurn(conversationID, assistantContent, assistantThinking, finalModel, finalReason, finalTokens, run); err != nil {
		h.completeStep(&run, "saved", "failed", finalReason, finalTokens, err.Error())
		h.completeRun(&run, "failed", "history_save_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history save failed: %v", err), Done: true})
		return
	}
	terminalContent := assistantContent
	if finalContentEmitted {
		terminalContent = ""
	}
	h.app.emitChatEvent(ChatStreamEvent{
		RequestID:      requestID,
		Content:        terminalContent,
		Done:           true,
		Model:          finalModel,
		Reason:         finalReason,
		Tokens:         finalTokens,
		ConversationID: conversationID,
	})
}

func (h *HarnessEngine) chatRequestForHarness(req ChatRequest) ChatRequest {
	if strings.TrimSpace(req.SelectedModel) == "" {
		req.SelectedModel = strings.TrimSpace(req.Model)
	}
	model := strings.TrimSpace(h.config.Providers.Ollama.Models.Harness)
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	req.Model = model
	return req
}

func (h *HarnessEngine) responseModelForChatSelection(req ChatRequest) string {
	model := strings.TrimSpace(req.SelectedModel)
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	return model
}

func (h *HarnessEngine) runFinalResponseAttempt(ctx context.Context, requestID, conversationID string, req ChatRequest, run *HarnessRun) (finalResponseAttempt, error) {
	result := finalResponseAttempt{Model: req.Model}
	h.startStep(run, "model_call")
	resp, err := h.app.ollamaClient(req.BaseURL).OpenChatStream(ctx, req)
	if err != nil {
		h.completeStep(run, "model_call", "failed", "", 0, err.Error())
		return result, err
	}
	defer resp.Body.Close()
	h.completeStep(run, "model_call", "completed", "", 0, "")
	h.startStep(run, "streaming")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var content strings.Builder
	var thinking strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var chunk ollamaChatChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			h.completeStep(run, "streaming", "failed", result.Reason, result.Tokens, err.Error())
			return result, err
		}
		if chunk.Error != "" {
			h.completeStep(run, "streaming", "failed", result.Reason, result.Tokens, chunk.Error)
			return result, errors.New(chunk.Error)
		}

		content.WriteString(chunk.Message.Content)
		thinking.WriteString(chunk.Message.Thinking)
		if chunk.Model != "" {
			result.Model = chunk.Model
		}
		if chunk.DoneReason != "" {
			result.Reason = chunk.DoneReason
		}
		if chunk.EvalCount > 0 {
			result.Tokens = chunk.EvalCount
		}

		h.app.emitChatEvent(ChatStreamEvent{
			RequestID:      requestID,
			Content:        chunk.Message.Content,
			Thinking:       chunk.Message.Thinking,
			Model:          chunk.Model,
			Reason:         chunk.DoneReason,
			Tokens:         chunk.EvalCount,
			ConversationID: conversationID,
		})
		if chunk.Message.Content != "" || chunk.Message.Thinking != "" {
			result.Emitted = true
		}

		if chunk.Done {
			result.Content = content.String()
			result.Thinking = thinking.String()
			h.completeStep(run, "streaming", "completed", result.Reason, result.Tokens, "")
			return result, nil
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		h.completeStep(run, "streaming", "failed", result.Reason, result.Tokens, err.Error())
		return result, err
	}
	result.Reason = "stream_ended"
	result.Content = content.String()
	result.Thinking = thinking.String()
	h.completeStep(run, "streaming", "completed", result.Reason, result.Tokens, "")
	return result, nil
}

func (h *HarnessEngine) toolRegistry() HarnessToolRegistry {
	return defaultHarnessToolRegistry(h.config)
}

func (h *HarnessEngine) selectSkillForTurn(ctx context.Context, req ChatRequest) (*HarnessSkillDecision, *LoadedSkill) {
	index, err := loadSkillIndex(defaultSkillRoots())
	if err != nil {
		return &HarnessSkillDecision{AvailableCount: 0, Error: err.Error()}, nil
	}
	if len(index) == 0 {
		return nil, nil
	}

	if entry, reason, ok := explicitSkillSelection(index, lastUserMessage(req.Messages).Content); ok {
		return h.loadSelectedSkill(entry, reason, len(index))
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
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, selectionReq)
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
func (h *HarnessEngine) prepareChatTurnLoop(ctx context.Context, requestID, conversationID string, req ChatRequest, run *HarnessRun) (HarnessPreparedTurn, error) {
	skillDecision, loadedSkill := h.selectSkillForTurn(ctx, req)
	run.Skill = skillDecision
	registry := h.toolRegistry()
	system := h.plannerSystemPrompt(registry, req, loadedSkill)
	messages := append([]ChatMessage{}, req.Messages...)
	deadline := time.Now().Add(harnessChatMaxWallTime)

	prepared := HarnessPreparedTurn{SkillDecision: skillDecision, LoadedSkill: loadedSkill}
	for iteration := 1; iteration <= harnessChatMaxSteps; iteration++ {
		prepReq := ChatRequest{
			BaseURL:  req.BaseURL,
			Model:    req.Model,
			System:   system,
			Messages: messages,
			Format:   harnessToolPlanSchema(registry),
			Options: map[string]any{
				"temperature": 0,
				"num_predict": 1024,
			},
		}
		completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
		if err != nil {
			return HarnessPreparedTurn{}, err
		}
		plan, validationErrors := parseHarnessToolPlanWithRegistry(completion.Content, registry)
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
			prepared.Rounds = append(prepared.Rounds, round)
			prepared.Brief = harnessInvalidPlanBrief
			prepared.NeedsTools = false
			prepared.Reason = "The harness plan was invalid, so the final response must not claim any tool action ran."
			prepared.ToolCalls = nil
			messages = append(messages,
				ChatMessage{Role: "assistant", Content: completion.Content},
				ChatMessage{Role: "user", Content: "Your previous response was not a valid tool plan:\n" + validationErrorsMarkdown(validationErrors) + "\n\nReturn a corrected plan that matches the response schema."},
			)
			continue
		}

		prepared.Brief = strings.TrimSpace(plan.Brief)
		prepared.NeedsTools = plan.NeedsTools
		prepared.Reason = strings.TrimSpace(plan.Reason)
		prepared.ToolCalls = plan.ToolCalls
		if !plan.NeedsTools || len(plan.ToolCalls) == 0 {
			prepared.Rounds = append(prepared.Rounds, round)
			break
		}

		insertToolCallStep(run)
		h.startStep(run, "tool_call")
		results := h.runHarnessToolCalls(ctx, requestID, conversationID, plan.ToolCalls)
		round.ToolResults = results
		prepared.Rounds = append(prepared.Rounds, round)
		prepared.ToolResults = append(prepared.ToolResults, results...)
		h.attachToolActivities(run, prepared.ToolResults)
		h.completeStep(run, "tool_call", "completed", "tool_call", 0, "")

		if time.Now().After(deadline) {
			break
		}
		messages = append(messages, ChatMessage{Role: "assistant", Content: completion.Content})
		messages = append(messages, toolResultMessages(results)...)
	}
	run.Loop.Iterations = len(prepared.Rounds)
	return prepared, nil
}

func (h *HarnessEngine) plannerSystemPrompt(registry HarnessToolRegistry, req ChatRequest, loadedSkill *LoadedSkill) string {
	system := strings.TrimSpace(fmt.Sprintf(`You are Atelier's private harness model. You gather evidence for the final model that will answer the user.
Do not answer the user directly. Do not include hidden chain-of-thought. Respond only with a JSON tool plan matching the response schema:
{
  "brief": "concise guidance for the final model",
  "needsTools": false,
  "reason": "why tools are or are not needed",
  "toolCalls": []
}
You plan in rounds, at most %d in total. Each round may request up to 3 tool calls. The harness executes them and returns each result, including failures, as a tool message; read the results and plan the next round.
When you have enough evidence, or none is needed, set "needsTools" false with empty "toolCalls" and write the brief: intent, constraints, relevant evidence, response shape, and cautions for the final model.
A failed or denied tool call is information, not a dead end: adapt the plan or tell the final model to report the failure plainly. Never claim an action succeeded unless a tool result shows it.
The final response model cannot call tools or execute commands. If a user request or active SKILL.md requires a command, include it as a tool call now. Do not put instructions like "run this command" in the brief.
Allowed tool calls:
%s
When "needsTools" is false, "toolCalls" must be []. Prefer read-only calls unless the user clearly asks to modify files or run a specific write-capable command. Filesystem paths and command working directories are scoped to Atelier's configured filesystem tool root.`, harnessChatMaxSteps, registry.PromptCatalog()))
	if strings.TrimSpace(req.System) != "" {
		system += "\n\nUser-facing system prompt to preserve:\n" + strings.TrimSpace(req.System)
	}
	if loadedSkill != nil {
		system += "\n\nActive SKILL.md selected for this turn. Follow these instructions when planning tools and writing the brief, including any workflow or command guidance that applies. Do not quote the skill unless the user asks about process.\n\n" + loadedSkill.Body
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

func (h *HarnessEngine) preparedResponseRequest(req ChatRequest, responseModel string, preparation HarnessPreparedTurn) ChatRequest {
	responseReq := req
	responseReq.Model = responseModel
	responseReq.System = appendHarnessPreparationToSystem(req.System, preparation)
	if len(preparation.ToolResults) > 0 {
		messages := append([]ChatMessage{}, req.Messages...)
		responseReq.Messages = append(messages, toolResultMessages(preparation.ToolResults)...)
	}
	return responseReq
}

func appendHarnessPreparationToSystem(system string, preparation HarnessPreparedTurn) string {
	brief := strings.TrimSpace(preparation.Brief)
	if brief == "" && len(preparation.ToolResults) == 0 {
		return system
	}
	handoff := "Atelier harness-prepared brief for this turn. Use it as private guidance for the final response; do not quote or mention it unless the user asks about process.\n\n" + brief
	if len(preparation.ToolResults) > 0 {
		handoff += "\n\nHarness tool observations for this turn appear as tool messages at the end of the conversation. Treat them as evidence: report failures honestly and do not claim an action succeeded unless an observation shows it. You cannot call tools yourself."
	}
	if strings.TrimSpace(system) == "" {
		return handoff
	}
	return strings.TrimSpace(system) + "\n\n" + handoff
}

// toolResultMessages renders tool results as role:"tool" messages so models
// receive observations in the message stream rather than the system prompt.
func toolResultMessages(results []HarnessToolResult) []ChatMessage {
	messages := make([]ChatMessage, 0, len(results))
	for _, result := range results {
		content, err := json.Marshal(result)
		if err != nil {
			content = []byte(fmt.Sprintf(`{"name":%q,"status":"failed","error":"tool result could not be serialized"}`, result.Name))
		}
		messages = append(messages, ChatMessage{Role: "tool", Content: string(content)})
	}
	return messages
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

func preparationTokens(preparation HarnessPreparedTurn) int {
	if len(preparation.Rounds) == 0 {
		return preparation.Completion.EvalTokens
	}
	total := 0
	for _, round := range preparation.Rounds {
		total += round.Completion.EvalTokens
	}
	return total
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

func parseHarnessToolPlan(content string) (HarnessToolPlan, []string) {
	return parseHarnessToolPlanWithRegistry(content, filesystemToolRegistry())
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
		if err := json.Unmarshal(data, &plan.ToolCalls); err != nil {
			errors = append(errors, "toolCalls must be an array of tool call objects")
		}
	}
	errors = append(errors, validateHarnessToolPlan(plan, registry)...)
	return plan, errors
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

func (h *HarnessEngine) runHarnessToolCalls(ctx context.Context, requestID, conversationID string, calls []HarnessToolCall) []HarnessToolResult {
	gateway := newToolGateway(h.app, h.config)
	results := make([]HarnessToolResult, 0, len(calls))
	for _, call := range calls {
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

func (h *HarnessEngine) attachToolActivities(run *HarnessRun, results []HarnessToolResult) {
	h.attachToolActivitiesToKind(run, "tool_call", results)
}

func (h *HarnessEngine) attachToolActivitiesToKind(run *HarnessRun, kind string, results []HarnessToolResult) {
	activities := make([]HarnessToolActivity, 0, len(results))
	for _, result := range results {
		activities = append(activities, h.toolActivityFromResult(result))
	}
	for index := range run.Steps {
		if run.Steps[index].Kind == kind {
			run.Steps[index].Tools = activities
			return
		}
	}
}

func (h *HarnessEngine) toolActivityFromResult(result HarnessToolResult) HarnessToolActivity {
	if definition, ok := h.toolRegistry().Get(result.Name); ok && definition.Activity != nil {
		return definition.Activity(result)
	}
	return defaultHarnessToolActivity(result)
}

func fallbackFinalResponseFromToolResults(results []HarnessToolResult) string {
	if len(results) == 0 {
		return ""
	}
	var lines []string
	for _, result := range results {
		if result.Status != "completed" {
			continue
		}
		name := strings.TrimSpace(result.Name)
		if name == "" {
			name = "tool"
		}
		line := fmt.Sprintf("Completed `%s` successfully.", name)
		if detail := fallbackToolResultDetail(result); detail != "" {
			line += " " + detail
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n\n")
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
	if len(text) <= 220 {
		return text
	}
	return text[:220] + "..."
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
	if len(content) <= 500 {
		return content
	}
	return content[:500] + "\n..."
}

func (h *HarnessEngine) shouldUseImageTool(req ChatRequest) bool {
	if strings.TrimSpace(h.config.Providers.Ollama.Models.Image) == "" {
		return false
	}
	prompt := strings.ToLower(strings.TrimSpace(lastUserMessage(req.Messages).Content))
	if prompt == "" {
		return false
	}
	actionWords := []string{"generate", "create", "draw", "paint", "render", "make", "design", "illustrate"}
	imageWords := []string{"image", "photo", "picture", "illustration", "poster", "logo", "artwork", "wallpaper"}
	hasAction := false
	for _, word := range actionWords {
		if strings.Contains(prompt, word) {
			hasAction = true
			break
		}
	}
	if !hasAction {
		return false
	}
	for _, word := range imageWords {
		if strings.Contains(prompt, word) {
			return true
		}
	}
	return strings.HasPrefix(prompt, "draw ") || strings.HasPrefix(prompt, "paint ") || strings.HasPrefix(prompt, "render ")
}

func (h *HarnessEngine) runImageTool(ctx context.Context, requestID, conversationID string, req ChatRequest) {
	imageModel := h.imageModelForChatSelection(ctx, req)
	prompt := strings.TrimSpace(lastUserMessage(req.Messages).Content)
	run := newImageToolHarnessRun(req.Model, imageModel, requestID, conversationID)
	h.app.emitChatEvent(ChatStreamEvent{
		RequestID:      requestID,
		ConversationID: conversationID,
		Content:        fmt.Sprintf("Creating image with %s...\n\n", imageModel),
	})

	imageReq := ImageGenerateRequest{
		BaseURL: req.BaseURL,
		Model:   imageModel,
		Prompt:  prompt,
		Width:   h.config.Generation.Image.Width,
		Height:  h.config.Generation.Image.Height,
		Steps:   h.config.Generation.Image.Steps,
	}

	h.startStep(&run, "tool_call")
	payload, raw, err := h.app.ollamaClient(req.BaseURL).GenerateImage(ctx, imageReq)
	if err != nil {
		h.completeStep(&run, "tool_call", "failed", "", 0, err.Error())
		h.completeRun(&run, "failed", "tool_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, ConversationID: conversationID, Error: err.Error(), Done: true})
		return
	}
	images := normalizeImagePayloads(payload.Images)
	if maybeImage := normalizeImagePayload(payload.Image); maybeImage != "" {
		images = append(images, maybeImage)
	}
	if maybeImage := normalizeImagePayload(payload.Response); maybeImage != "" {
		images = append(images, maybeImage)
	}
	images = append(images, collectImagesFromJSON(raw)...)
	images = dedupeStrings(images)
	if len(images) == 0 {
		errText := "image model returned no image data"
		h.completeStep(&run, "tool_call", "failed", "", 0, errText)
		h.completeRun(&run, "failed", "tool_empty")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, ConversationID: conversationID, Error: errText, Done: true})
		return
	}
	doneReason := "tool_call"
	h.attachToolActivities(&run, []HarnessToolResult{{
		Name:    "image_generation",
		Status:  "completed",
		Summary: fmt.Sprintf("generated %d image%s", len(images), pluralSuffix(len(images))),
		Result: ToolCommandResult{
			Command:    []string{"ollama", "generate", imageModel},
			ExitCode:   0,
			DurationMS: 0,
		},
	}})
	h.completeStep(&run, "tool_call", "completed", doneReason, 0, "")
	h.evaluateImageToolRun(&run, len(images), doneReason)
	h.startStep(&run, "saved")
	h.completeStep(&run, "saved", "completed", doneReason, 0, "")
	h.completeRun(&run, "completed", "final")

	assistantContent := fmt.Sprintf("Generated %d image%s with %s.", len(images), pluralSuffix(len(images)), imageModel)
	if strings.TrimSpace(payload.Response) != "" && normalizeImagePayload(payload.Response) == "" {
		assistantContent = strings.TrimSpace(payload.Response)
	}
	if err := appendChatAssistantTurnWithImages(h.config, conversationID, assistantContent, imageModel, doneReason, images, compactRawResponse(raw), run, imageReq); err != nil {
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, ConversationID: conversationID, Error: fmt.Sprintf("history save failed: %v", err), Done: true})
		return
	}
	h.app.emitChatEvent(ChatStreamEvent{
		RequestID:      requestID,
		ConversationID: conversationID,
		Content:        assistantContent,
		Images:         images,
		Model:          imageModel,
		Reason:         doneReason,
		Done:           true,
	})
}

func (h *HarnessEngine) imageModelForChatSelection(ctx context.Context, req ChatRequest) string {
	selectedModel := strings.TrimSpace(req.SelectedModel)
	if selectedModel != "" && h.app.ollamaClient(req.BaseURL).IsImageGenerationModel(ctx, selectedModel) {
		return selectedModel
	}
	return strings.TrimSpace(h.config.Providers.Ollama.Models.Image)
}

func newChatHarnessRun(model, reason string, tokens int) HarnessRun {
	now := time.Now().Format(time.RFC3339)
	startedAt := time.Now()
	run := HarnessRun{
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
				Kind:        "queued",
				Iteration:   1,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				Summary:     "turn accepted by harness",
			},
			{
				ID:          "step_000002",
				Kind:        "preparing",
				Iteration:   1,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				Summary:     "request normalized and history turn prepared",
			},
			{
				ID:          "step_000003",
				Kind:        "model_call",
				Iteration:   1,
				Provider:    "ollama",
				Model:       model,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				Summary:     "provider stream opened",
			},
			{
				ID:          "step_000004",
				Kind:        "streaming",
				Iteration:   1,
				Provider:    "ollama",
				Model:       model,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				DoneReason:  reason,
				Summary:     "assistant response streamed to UI",
				Tokens:      tokens,
			},
			{
				ID:          "step_000005",
				Kind:        "evaluation",
				Iteration:   1,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				Decision:    "final",
				DoneReason:  reason,
				Summary:     "assistant response is user-visible final output",
			},
			{
				ID:          "step_000006",
				Kind:        "saved",
				Iteration:   1,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				Summary:     "assistant turn and harness run stored in history",
			},
		},
	}
	run.DurationMS = time.Since(startedAt).Milliseconds()
	return run
}

func newImageToolHarnessRun(chatModel, imageModel, requestID, conversationID string) HarnessRun {
	now := time.Now().Format(time.RFC3339)
	return HarnessRun{
		ID:             randomID("run"),
		Mode:           "chat",
		Status:         "running",
		StartedAt:      now,
		RequestID:      requestID,
		ConversationID: conversationID,
		Loop: HarnessLoop{
			MaxSteps:      harnessChatMaxSteps,
			MaxWallTimeMS: harnessChatMaxWallTime.Milliseconds(),
			Iterations:    1,
		},
		Steps: []HarnessStep{
			{
				ID:          "step_000001",
				Kind:        "queued",
				Iteration:   1,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				Summary:     "turn accepted by harness",
			},
			{
				ID:          "step_000002",
				Kind:        "preparing",
				Iteration:   1,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				Summary:     "request classified as an image generation tool call",
			},
			{
				ID:        "step_000003",
				Kind:      "tool_call",
				Iteration: 1,
				Provider:  "ollama",
				Model:     imageModel,
				Status:    "pending",
				StartedAt: now,
				Summary:   "configured image model invoked from chat",
			},
			{
				ID:        "step_000004",
				Kind:      "evaluation",
				Iteration: 1,
				Status:    "pending",
				StartedAt: now,
			},
			{
				ID:        "step_000005",
				Kind:      "saved",
				Iteration: 1,
				Status:    "pending",
				StartedAt: now,
				Summary:   "assistant image turn stored in chat history",
			},
		},
	}
}

func insertToolCallStep(run *HarnessRun) {
	for _, step := range run.Steps {
		if step.Kind == "tool_call" {
			return
		}
	}
	now := time.Now().Format(time.RFC3339)
	step := HarnessStep{
		ID:        "step_000003_tool",
		Kind:      "tool_call",
		Iteration: 1,
		Provider:  "tools",
		Status:    "pending",
		StartedAt: now,
		Summary:   "tool calls requested by harness preparation",
	}
	insertAt := len(run.Steps)
	for index, existing := range run.Steps {
		if existing.Kind == "model_call" {
			insertAt = index
			break
		}
	}
	run.Steps = append(run.Steps, HarnessStep{})
	copy(run.Steps[insertAt+1:], run.Steps[insertAt:])
	run.Steps[insertAt] = step
}

func firstHarnessRun(model, reason string, tokens int, runs []HarnessRun) HarnessRun {
	if len(runs) > 0 && strings.TrimSpace(runs[0].ID) != "" {
		return runs[0]
	}
	return newChatHarnessRun(model, reason, tokens)
}

func (h *HarnessEngine) startStep(run *HarnessRun, kind string) {
	now := time.Now().Format(time.RFC3339)
	for index := range run.Steps {
		if run.Steps[index].Kind != kind {
			continue
		}
		run.Steps[index].Status = "running"
		run.Steps[index].StartedAt = now
		run.Steps[index].CompletedAt = ""
		run.Steps[index].DurationMS = 0
		run.Steps[index].Error = ""
		return
	}
}

func (h *HarnessEngine) markStepModel(run *HarnessRun, kind, provider, model string) {
	for index := range run.Steps {
		if run.Steps[index].Kind != kind {
			continue
		}
		run.Steps[index].Provider = provider
		run.Steps[index].Model = model
		return
	}
}

func (h *HarnessEngine) completeStep(run *HarnessRun, kind, status, reason string, tokens int, errorText string) {
	now := time.Now().Format(time.RFC3339)
	for index := range run.Steps {
		if run.Steps[index].Kind != kind {
			continue
		}
		run.Steps[index].Status = status
		run.Steps[index].CompletedAt = now
		run.Steps[index].DoneReason = reason
		run.Steps[index].Tokens = tokens
		run.Steps[index].Error = errorText
		if startedAt, err := time.Parse(time.RFC3339, run.Steps[index].StartedAt); err == nil {
			run.Steps[index].DurationMS = time.Since(startedAt).Milliseconds()
		}
		return
	}
}

func (h *HarnessEngine) completeRun(run *HarnessRun, status, stopReason string) {
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
	h.startStep(run, "evaluation")
	for index := range run.Steps {
		if run.Steps[index].Kind == "evaluation" {
			run.Steps[index].Decision = decision
			run.Steps[index].Summary = summary
			break
		}
	}
	h.completeStep(run, "evaluation", "completed", doneReason, 0, "")
	run.Loop.StopReason = decision
}

func (h *HarnessEngine) evaluateImageToolRun(run *HarnessRun, imageCount int, doneReason string) {
	decision := "final"
	summary := fmt.Sprintf("image tool generated %d image%s", imageCount, pluralSuffix(imageCount))
	if imageCount == 0 {
		decision = "stop"
		summary = "image tool returned no images"
	}
	h.startStep(run, "evaluation")
	for index := range run.Steps {
		if run.Steps[index].Kind == "evaluation" {
			run.Steps[index].Decision = decision
			run.Steps[index].Summary = summary
			break
		}
	}
	h.completeStep(run, "evaluation", "completed", doneReason, 0, "")
	run.Loop.StopReason = decision
}

func (h *HarnessEngine) SaveChatTurn(req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, title string, run HarnessRun) (string, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return writeChatConversation(h.config, req, assistantContent, assistantThinking, model, reason, tokens, title, run)
	}
	return appendChatConversation(h.config, req, assistantContent, assistantThinking, model, reason, tokens, run)
}

func (h *HarnessEngine) StartChatTurn(req ChatRequest) (string, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return writePendingChatConversation(h.config, req)
	}
	return appendChatUserTurn(h.config, req)
}

func (h *HarnessEngine) SaveAssistantTurn(conversationID, assistantContent, assistantThinking, model, reason string, tokens int, run HarnessRun) error {
	if strings.TrimSpace(assistantContent) == "" && strings.TrimSpace(assistantThinking) == "" {
		return nil
	}
	return appendChatAssistantTurn(h.config, conversationID, assistantContent, assistantThinking, model, reason, tokens, run)
}
