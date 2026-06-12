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
	harnessPlanNumPredict  = 1024
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
	run := newHarnessRun(requestID, conversationID)
	queued := run.appendStep("queued", 1, "", "", "turn accepted by harness")
	run.completeStep(queued, "completed", "", 0, "")
	preparation, err := h.prepareChatTurnLoop(ctx, requestID, conversationID, req, &run)
	if err != nil {
		run.complete("failed", "harness_prepare_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
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
		run.complete("failed", result.Reason)
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
	// The run is serialized into the saved turn, so the saved step is marked
	// completed optimistically and flipped to failed if the write errors.
	saved := run.appendStep("saved", 1, "", "", "assistant turn and harness run stored in history")
	run.completeStep(saved, "completed", finalReason, finalTokens, "")
	run.complete("completed", "final")
	if err := h.SaveAssistantTurn(conversationID, assistantContent, assistantThinking, finalModel, finalReason, finalTokens, run); err != nil {
		run.completeStep(saved, "failed", finalReason, finalTokens, err.Error())
		run.complete("failed", "history_save_error")
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
	modelCall := run.appendStep("model_call", 1, "ollama", req.Model, "provider stream opened")
	resp, err := h.app.ollamaClient(req.BaseURL).OpenChatStream(ctx, req)
	if err != nil {
		run.completeStep(modelCall, "failed", "", 0, err.Error())
		return result, err
	}
	defer resp.Body.Close()
	run.completeStep(modelCall, "completed", "", 0, "")
	streaming := run.appendStep("streaming", 1, "ollama", req.Model, "assistant response streamed to UI")

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
			run.completeStep(streaming, "failed", result.Reason, result.Tokens, err.Error())
			return result, err
		}
		if chunk.Error != "" {
			run.completeStep(streaming, "failed", result.Reason, result.Tokens, chunk.Error)
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
			run.completeStep(streaming, "completed", result.Reason, result.Tokens, "")
			return result, nil
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		run.completeStep(streaming, "failed", result.Reason, result.Tokens, err.Error())
		return result, err
	}
	result.Reason = "stream_ended"
	result.Content = content.String()
	result.Thinking = thinking.String()
	run.completeStep(streaming, "completed", result.Reason, result.Tokens, "")
	return result, nil
}

func (h *HarnessEngine) toolRegistry() HarnessToolRegistry {
	return defaultHarnessToolRegistry(h.config)
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
	}
	return truncated
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
			"num_ctx":     h.numCtx(),
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
	numCtx := h.numCtx()
	budget := historyBudgetChars(numCtx, system, harnessPlanNumPredict)
	messages := append([]ChatMessage{}, req.Messages...)
	deadline := time.Now().Add(harnessChatMaxWallTime)

	prepared := HarnessPreparedTurn{SkillDecision: skillDecision, LoadedSkill: loadedSkill}
	for iteration := 1; iteration <= harnessChatMaxSteps; iteration++ {
		planning := run.appendStep("planning", iteration, "ollama", req.Model, fmt.Sprintf("harness planning round %d", iteration))
		prepReq := ChatRequest{
			BaseURL:  req.BaseURL,
			Model:    req.Model,
			System:   system,
			Messages: truncateChatHistory(messages, budget),
			Format:   harnessToolPlanSchema(registry),
			Options: map[string]any{
				"temperature": 0,
				"num_predict": harnessPlanNumPredict,
				"num_ctx":     numCtx,
			},
		}
		completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
		if err != nil {
			run.completeStep(planning, "failed", "", 0, err.Error())
			return HarnessPreparedTurn{}, err
		}
		plan, validationErrors := parseHarnessToolPlanWithRegistry(completion.Content, registry)
		if len(validationErrors) > 0 && strings.TrimSpace(completion.Reason) == "length" {
			validationErrors = append([]string{"the plan response hit the output token limit and was cut off; return a shorter plan"}, validationErrors...)
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
		run.completeStep(planning, "completed", completion.Reason, completion.EvalTokens, "")

		prepared.Brief = strings.TrimSpace(plan.Brief)
		prepared.NeedsTools = plan.NeedsTools
		prepared.Reason = strings.TrimSpace(plan.Reason)
		prepared.ToolCalls = plan.ToolCalls
		if !plan.NeedsTools || len(plan.ToolCalls) == 0 {
			prepared.Rounds = append(prepared.Rounds, round)
			break
		}

		toolStep := run.appendStep("tool_call", iteration, "tools", "", "tool calls requested by harness planning")
		results := h.runHarnessToolCalls(ctx, requestID, conversationID, plan.ToolCalls)
		round.ToolResults = results
		prepared.Rounds = append(prepared.Rounds, round)
		prepared.ToolResults = append(prepared.ToolResults, results...)
		run.Steps[toolStep].Tools = h.toolActivities(results)
		run.completeStep(toolStep, "completed", "tool_call", 0, "")

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
	messages := append([]ChatMessage{}, req.Messages...)
	if len(preparation.ToolResults) > 0 {
		messages = append(messages, toolResultMessages(preparation.ToolResults)...)
	}
	numCtx := h.numCtx()
	responseReq.Messages = truncateChatHistory(messages, historyBudgetChars(numCtx, responseReq.System, numCtx/4))
	responseReq.Options = withNumCtx(req.Options, numCtx)
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
// Oversized results are cut down for the message only; history and telemetry
// keep the full result.
func toolResultMessages(results []HarnessToolResult) []ChatMessage {
	messages := make([]ChatMessage, 0, len(results))
	for _, result := range results {
		content, err := json.Marshal(result)
		if err != nil {
			content = []byte(fmt.Sprintf(`{"name":%q,"status":"failed","error":"tool result could not be serialized"}`, result.Name))
		}
		if len(content) > toolResultMessageMaxChars {
			content = compactToolResultMessage(result, string(content))
		}
		messages = append(messages, ChatMessage{Role: "tool", Content: string(content)})
	}
	return messages
}

func compactToolResultMessage(result HarnessToolResult, fullJSON string) []byte {
	preview := fullJSON
	if len(preview) > toolResultMessageMaxChars-512 {
		preview = preview[:toolResultMessageMaxChars-512] + "..."
	}
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

func (h *HarnessEngine) toolActivities(results []HarnessToolResult) []HarnessToolActivity {
	activities := make([]HarnessToolActivity, 0, len(results))
	for _, result := range results {
		activities = append(activities, h.toolActivityFromResult(result))
	}
	return activities
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
	run := newHarnessRun(requestID, conversationID)
	queued := run.appendStep("queued", 1, "", "", "turn accepted by harness")
	run.completeStep(queued, "completed", "", 0, "")
	preparing := run.appendStep("preparing", 1, "", "", "request classified as an image generation tool call")
	run.completeStep(preparing, "completed", "", 0, "")
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

	toolStep := run.appendStep("tool_call", 1, "ollama", imageModel, "configured image model invoked from chat")
	payload, raw, err := h.app.ollamaClient(req.BaseURL).GenerateImage(ctx, imageReq)
	if err != nil {
		run.completeStep(toolStep, "failed", "", 0, err.Error())
		run.complete("failed", "tool_error")
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
		run.completeStep(toolStep, "failed", "", 0, errText)
		run.complete("failed", "tool_empty")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, ConversationID: conversationID, Error: errText, Done: true})
		return
	}
	doneReason := "tool_call"
	run.Steps[toolStep].Tools = h.toolActivities([]HarnessToolResult{{
		Name:    "image_generation",
		Status:  "completed",
		Summary: fmt.Sprintf("generated %d image%s", len(images), pluralSuffix(len(images))),
		Result: ToolCommandResult{
			Command:    []string{"ollama", "generate", imageModel},
			ExitCode:   0,
			DurationMS: 0,
		},
	}})
	run.completeStep(toolStep, "completed", doneReason, 0, "")
	h.evaluateImageToolRun(&run, len(images), doneReason)
	saved := run.appendStep("saved", 1, "", "", "assistant image turn stored in chat history")
	run.completeStep(saved, "completed", doneReason, 0, "")
	run.complete("completed", "final")

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
			Iterations:    1,
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

func firstHarnessRun(model, reason string, tokens int, runs []HarnessRun) HarnessRun {
	if len(runs) > 0 && strings.TrimSpace(runs[0].ID) != "" {
		return runs[0]
	}
	return fallbackHarnessRun(model, reason, tokens)
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

func (h *HarnessEngine) evaluateImageToolRun(run *HarnessRun, imageCount int, doneReason string) {
	decision := "final"
	summary := fmt.Sprintf("image tool generated %d image%s", imageCount, pluralSuffix(imageCount))
	if imageCount == 0 {
		decision = "stop"
		summary = "image tool returned no images"
	}
	index := run.appendStep("evaluation", 1, "", "", summary)
	run.Steps[index].Decision = decision
	run.completeStep(index, "completed", doneReason, 0, "")
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
