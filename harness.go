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
	harnessChatMaxSteps           = 3
	harnessChatMaxWallTime        = 2 * time.Minute
	finalizerToolRequestMaxRounds = 1
)

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
	BlockingToolFailure  *HarnessToolResult
	BlockingPlanFailure  []string
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

type FinalizerToolRequest struct {
	Type      string            `json:"type"`
	Reason    string            `json:"reason"`
	ToolCalls []HarnessToolCall `json:"toolCalls"`
}

type finalResponseAttempt struct {
	Content     string
	Thinking    string
	Model       string
	Reason      string
	Tokens      int
	Emitted     bool
	ToolRequest *FinalizerToolRequest
}

type finalizerHarnessExecution struct {
	Request              FinalizerToolRequest
	Plan                 HarnessPreparedTurn
	Results              []HarnessToolResult
	PlanValidationErrors []string
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
	if preparation.BlockingToolFailure != nil || len(preparation.BlockingPlanFailure) > 0 {
		assistantContent := blockingPreparationUserMessage(preparation)
		finalReason := blockingPreparationDoneReason(preparation)
		h.completeStep(&run, "model_call", "skipped", finalReason, 0, "")
		h.completeStep(&run, "streaming", "skipped", finalReason, 0, "")
		h.startStep(&run, "evaluation")
		for index := range run.Steps {
			if run.Steps[index].Kind == "evaluation" {
				run.Steps[index].Decision = "stop"
				run.Steps[index].Summary = "required harness preparation failed before final response"
				break
			}
		}
		h.completeStep(&run, "evaluation", "completed", finalReason, 0, "")
		h.startStep(&run, "saved")
		h.completeStep(&run, "saved", "completed", finalReason, 0, "")
		h.completeRun(&run, "failed", finalReason)
		if err := h.SaveAssistantTurn(conversationID, assistantContent, preparationThinking, req.Model, finalReason, 0, run); err != nil {
			h.completeStep(&run, "saved", "failed", finalReason, 0, err.Error())
			h.completeRun(&run, "failed", "history_save_error")
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history save failed: %v", err), Done: true})
			return
		}
		h.app.emitChatEvent(ChatStreamEvent{
			RequestID:      requestID,
			Content:        assistantContent,
			Done:           true,
			Model:          req.Model,
			Reason:         finalReason,
			ConversationID: conversationID,
		})
		return
	}

	responseReq := h.preparedResponseRequest(req, responseModel, preparation)
	assistantThinking := preparationThinking
	var assistantContent string
	finalModel := responseModel
	var finalReason string
	var finalTokens int
	finalContentEmitted := false
	for attempt := 0; ; attempt++ {
		result, err := h.runFinalResponseAttempt(ctx, requestID, conversationID, responseReq, &run, attempt > 0)
		if err != nil {
			h.completeRun(&run, "failed", result.Reason)
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
			return
		}
		if strings.TrimSpace(result.Model) != "" {
			finalModel = result.Model
		}
		finalReason = result.Reason
		finalTokens = result.Tokens
		if result.Thinking != "" {
			assistantThinking += result.Thinking
		}
		if result.ToolRequest == nil {
			assistantContent = result.Content
			finalContentEmitted = result.Emitted
			break
		}
		if attempt >= finalizerToolRequestMaxRounds {
			assistantContent = "I noticed I needed one more evidence check, but this turn already used its final-model tool request. Please send the request again and I can continue from here."
			finalReason = "final_tool_request_limit"
			finalContentEmitted = false
			break
		}
		execution, err := h.executeFinalizerToolRequest(ctx, requestID, conversationID, req, &run, preparation, *result.ToolRequest)
		if err != nil {
			h.completeRun(&run, "failed", "final_tool_request_error")
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
			return
		}
		assistantThinking += formatFinalizerToolRequestThinking(execution)
		responseReq = h.responseRequestWithFinalizerToolResults(req, responseModel, preparation, execution)
	}

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

func (h *HarnessEngine) runFinalResponseAttempt(ctx context.Context, requestID, conversationID string, req ChatRequest, run *HarnessRun, retry bool) (finalResponseAttempt, error) {
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
	suppressUntilKnown := true
	flushed := false
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

		if suppressUntilKnown && !isPotentialFinalizerToolRequestPrefix(content.String()) {
			suppressUntilKnown = false
			flushed = true
			h.app.emitChatEvent(ChatStreamEvent{
				RequestID:      requestID,
				Content:        content.String(),
				Thinking:       thinking.String(),
				Model:          chunk.Model,
				Reason:         chunk.DoneReason,
				Tokens:         chunk.EvalCount,
				ConversationID: conversationID,
			})
			result.Emitted = true
		} else if !suppressUntilKnown {
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
		}

		if chunk.Done {
			result.Content = content.String()
			result.Thinking = thinking.String()
			if !flushed {
				if request, ok := parseFinalizerToolRequest(result.Content, h.toolRegistry()); ok {
					result.ToolRequest = &request
					h.completeStep(run, "streaming", "completed", "final_tool_request", result.Tokens, "")
					return result, nil
				}
				h.app.emitChatEvent(ChatStreamEvent{
					RequestID:      requestID,
					Content:        result.Content,
					Thinking:       result.Thinking,
					Model:          result.Model,
					Reason:         result.Reason,
					Tokens:         result.Tokens,
					ConversationID: conversationID,
				})
				result.Emitted = true
			}
			h.completeStep(run, "streaming", "completed", result.Reason, result.Tokens, "")
			return result, nil
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		h.completeStep(run, "streaming", "failed", result.Reason, result.Tokens, err.Error())
		return result, err
	}
	if retry {
		result.Reason = "final_retry_stream_ended"
	} else {
		result.Reason = "stream_ended"
	}
	result.Content = content.String()
	result.Thinking = thinking.String()
	h.completeStep(run, "streaming", "completed", result.Reason, result.Tokens, "")
	return result, nil
}

func isPotentialFinalizerToolRequestPrefix(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "```")
}

func (h *HarnessEngine) executeFinalizerToolRequest(ctx context.Context, requestID, conversationID string, req ChatRequest, run *HarnessRun, preparation HarnessPreparedTurn, request FinalizerToolRequest) (finalizerHarnessExecution, error) {
	execution := finalizerHarnessExecution{Request: request}
	insertFinalizerToolRequestStep(run)
	h.markStepModel(run, "final_tool_request", "ollama", req.Model)
	h.startStep(run, "final_tool_request")
	plan, err := h.planFinalizerToolRequest(ctx, req, preparation, request)
	if err != nil {
		h.completeStep(run, "final_tool_request", "failed", "", 0, err.Error())
		return execution, err
	}
	execution.Plan = plan
	execution.PlanValidationErrors = plan.PlanValidationErrors
	results := []HarnessToolResult{}
	if len(plan.PlanValidationErrors) == 0 && len(plan.ToolCalls) > 0 {
		results = h.runHarnessToolCalls(ctx, requestID, conversationID, plan.ToolCalls)
	}
	execution.Results = results
	h.attachToolActivitiesToKind(run, "final_tool_request", results)
	h.completeStep(run, "final_tool_request", "completed", plan.Completion.Reason, preparationTokens(plan), "")
	if run.Loop.Iterations < 2 {
		run.Loop.Iterations = 2
	}
	return execution, nil
}

func (h *HarnessEngine) responseRequestWithFinalizerToolResults(req ChatRequest, responseModel string, preparation HarnessPreparedTurn, execution finalizerHarnessExecution) ChatRequest {
	responseReq := h.preparedResponseRequest(req, responseModel, preparation)
	responseReq.System = appendFinalizerToolResultsToSystem(responseReq.System, execution)
	return responseReq
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
Return exactly one fenced JSON object. No prose outside the JSON block.
Schema:
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

func (h *HarnessEngine) prepareChatTurnLoop(ctx context.Context, requestID, conversationID string, req ChatRequest, run *HarnessRun) (HarnessPreparedTurn, error) {
	first, err := h.prepareChatTurn(ctx, req)
	if err != nil {
		return HarnessPreparedTurn{}, err
	}
	run.Skill = first.SkillDecision
	rounds := []HarnessToolRound{preparedTurnToRound(1, first)}
	if len(first.ToolCalls) > 0 {
		insertToolCallStep(run)
		h.startStep(run, "tool_call")
		first.ToolResults = h.runHarnessToolCalls(ctx, requestID, conversationID, first.ToolCalls)
		rounds[0].ToolResults = first.ToolResults
		h.attachToolActivities(run, first.ToolResults)
		if failure, ok := blockingToolFailure(first.ToolResults); ok {
			h.completeStep(run, "tool_call", "failed", toolFailureDoneReason(failure), 0, toolFailureDetail(failure))
			first.BlockingToolFailure = &failure
			first.Brief = toolFailureBrief(failure)
			first.Reason = toolFailureDecision(failure)
			first.Rounds = rounds
			run.Loop.StopReason = toolFailureDoneReason(failure)
			return first, nil
		}
		h.completeStep(run, "tool_call", "completed", "tool_call", 0, "")
	}
	final := first
	if len(first.ToolResults) > 0 && len(first.PlanValidationErrors) == 0 {
		inspection, err := h.inspectToolResults(ctx, req, first)
		if err != nil {
			return HarnessPreparedTurn{}, err
		}
		rounds = append(rounds, preparedTurnToRound(2, inspection))
		final = inspection
		final.SkillDecision = first.SkillDecision
		final.LoadedSkill = first.LoadedSkill
		final.ToolResults = append([]HarnessToolResult{}, first.ToolResults...)
		if len(inspection.ToolCalls) > 0 {
			insertToolCallStep(run)
			h.startStep(run, "tool_call")
			inspection.ToolResults = h.runHarnessToolCalls(ctx, requestID, conversationID, inspection.ToolCalls)
			rounds[1].ToolResults = inspection.ToolResults
			final.ToolResults = append(final.ToolResults, inspection.ToolResults...)
			h.attachToolActivities(run, final.ToolResults)
			if failure, ok := blockingToolFailure(inspection.ToolResults); ok {
				h.completeStep(run, "tool_call", "failed", toolFailureDoneReason(failure), 0, toolFailureDetail(failure))
				final.BlockingToolFailure = &failure
				final.Brief = toolFailureBrief(failure)
				final.Reason = toolFailureDecision(failure)
				final.Rounds = rounds
				run.Loop.StopReason = toolFailureDoneReason(failure)
				return final, nil
			}
			h.completeStep(run, "tool_call", "completed", "tool_call", 0, "")
		} else {
			final.ToolResults = append(final.ToolResults, inspection.ToolResults...)
		}
		final.ToolCalls = inspection.ToolCalls
		final.PlanValidationErrors = inspection.PlanValidationErrors
	}
	final.Rounds = rounds
	if len(rounds) > 1 {
		run.Loop.Iterations = len(rounds)
	}
	return final, nil
}

func preparedTurnToRound(iteration int, turn HarnessPreparedTurn) HarnessToolRound {
	return HarnessToolRound{
		Iteration:            iteration,
		Brief:                turn.Brief,
		NeedsTools:           turn.NeedsTools,
		Reason:               turn.Reason,
		Completion:           turn.Completion,
		SkillDecision:        turn.SkillDecision,
		ToolCalls:            turn.ToolCalls,
		ToolResults:          turn.ToolResults,
		PlanValidationErrors: turn.PlanValidationErrors,
	}
}

func (h *HarnessEngine) prepareChatTurn(ctx context.Context, req ChatRequest) (HarnessPreparedTurn, error) {
	skillDecision, loadedSkill := h.selectSkillForTurn(ctx, req)
	toolCatalog := h.toolRegistry().PromptCatalog()
	system := strings.TrimSpace(`You are Atelier's private harness model. Prepare a concise markdown brief for the next model that will answer the user.
Do not answer the user directly. Do not include hidden chain-of-thought. Capture only useful answer guidance: intent, constraints, relevant context, response shape, and cautions.
Return exactly one fenced JSON object. No prose outside the JSON block.
Schema:
{
  "brief": "concise guidance for the final model",
  "needsTools": false,
  "reason": "why tools are or are not needed",
  "toolCalls": []
}
If workspace context, a user-requested command, or a skill-specified command would materially improve the answer, set "needsTools": true and include at most 3 tool calls.
The final response model cannot call tools or execute commands. If a user request or active SKILL.md requires a command, this harness must include the command as a tool call now. Do not put instructions like "run this command" in the brief for the final model.
Allowed tool calls:
` + toolCatalog + `
When "needsTools" is false, "toolCalls" must be [].
Prefer read-only calls unless the user clearly asks to modify files or run a specific write-capable command. Filesystem paths and command working directories are scoped to Atelier's configured filesystem tool root.`)
	if strings.TrimSpace(req.System) != "" {
		system += "\n\nUser-facing system prompt to preserve:\n" + strings.TrimSpace(req.System)
	}
	if loadedSkill != nil {
		system += "\n\nActive SKILL.md selected for this turn. Follow these instructions when preparing the brief, including any workflow or command guidance that applies. Do not quote the skill unless the user asks about process.\n\n" + loadedSkill.Body
	}
	prepReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    req.Model,
		System:   system,
		Messages: req.Messages,
		Options: map[string]any{
			"temperature": 0,
			"num_predict": 1024,
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
	if err != nil {
		return HarnessPreparedTurn{}, err
	}
	plan, validationErrors := h.parseHarnessToolPlan(completion.Content)
	if len(validationErrors) > 0 && shouldRepairHarnessToolPlan(completion.Content, loadedSkill) {
		repairedPlan, repairedCompletion, repairedErrors, err := h.repairHarnessToolPlan(ctx, req, system, completion.Content, validationErrors)
		if err != nil {
			return HarnessPreparedTurn{}, err
		}
		if len(repairedErrors) == 0 {
			plan = repairedPlan
			completion = repairedCompletion
			validationErrors = nil
		} else if recoveredPlan, ok := recoverCommandToolPlanFromHarnessText(req, repairedCompletion.Content); ok {
			plan = recoveredPlan
			completion = repairedCompletion
			validationErrors = nil
		} else if recoveredPlan, ok := recoverCommandToolPlanFromHarnessText(req, completion.Content); ok {
			plan = recoveredPlan
			validationErrors = nil
		}
	}
	brief := harnessBriefForPlan(plan, completion.Content, validationErrors)
	toolCalls := []HarnessToolCall{}
	if len(validationErrors) == 0 && plan.NeedsTools {
		toolCalls = plan.ToolCalls
	}
	prepared := HarnessPreparedTurn{
		Brief:                brief,
		NeedsTools:           len(validationErrors) == 0 && plan.NeedsTools,
		Reason:               strings.TrimSpace(plan.Reason),
		Completion:           completion,
		SkillDecision:        skillDecision,
		LoadedSkill:          loadedSkill,
		ToolCalls:            toolCalls,
		PlanValidationErrors: validationErrors,
	}
	if shouldBlockInvalidHarnessPlan(prepared) {
		prepared.BlockingPlanFailure = append([]string{}, validationErrors...)
	}
	return applyDeterministicToolFallback(req, prepared), nil
}

func (h *HarnessEngine) inspectToolResults(ctx context.Context, req ChatRequest, prior HarnessPreparedTurn) (HarnessPreparedTurn, error) {
	resultsJSON, _ := json.MarshalIndent(prior.ToolResults, "", "  ")
	system := strings.TrimSpace(`You are Atelier's private harness model. Inspect tool results and decide whether one final tool round is needed before handing off to the answer model.
Do not answer the user directly. Do not include hidden chain-of-thought.
Return exactly one fenced JSON object. No prose outside the JSON block.
Schema:
{
  "brief": "updated concise guidance for the final model, incorporating useful tool observations",
  "needsTools": false,
  "reason": "why one more tool round is or is not needed",
  "toolCalls": []
}
You may request at most one more batch of up to 3 tool calls. If the existing results are sufficient, set needsTools false and toolCalls [].`)
	if prior.LoadedSkill != nil {
		system += "\n\nActive SKILL.md selected for this turn. Continue following it while inspecting tool results.\n\n" + prior.LoadedSkill.Body
	}
	messages := append([]ChatMessage{}, req.Messages...)
	messages = append(messages, ChatMessage{
		Role:    "assistant",
		Content: "Prior harness brief:\n" + prior.Brief + "\n\nTool results:\n```json\n" + string(resultsJSON) + "\n```",
	})
	prepReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    req.Model,
		System:   system,
		Messages: messages,
		Options: map[string]any{
			"temperature": 0,
			"num_predict": 1024,
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
	if err != nil {
		return HarnessPreparedTurn{}, err
	}
	plan, validationErrors := h.parseHarnessToolPlan(completion.Content)
	brief := harnessBriefForPlan(plan, completion.Content, validationErrors)
	toolCalls := []HarnessToolCall{}
	if len(validationErrors) == 0 && plan.NeedsTools {
		toolCalls = plan.ToolCalls
	}
	return HarnessPreparedTurn{
		Brief:                brief,
		NeedsTools:           len(validationErrors) == 0 && plan.NeedsTools,
		Reason:               strings.TrimSpace(plan.Reason),
		Completion:           completion,
		SkillDecision:        prior.SkillDecision,
		LoadedSkill:          prior.LoadedSkill,
		ToolCalls:            toolCalls,
		PlanValidationErrors: validationErrors,
	}, nil
}

func (h *HarnessEngine) planFinalizerToolRequest(ctx context.Context, req ChatRequest, preparation HarnessPreparedTurn, request FinalizerToolRequest) (HarnessPreparedTurn, error) {
	requestJSON, _ := json.MarshalIndent(request, "", "  ")
	priorResultsJSON, _ := json.MarshalIndent(preparation.ToolResults, "", "  ")
	toolCatalog := h.toolRegistry().PromptCatalog()
	system := strings.TrimSpace(`You are Atelier's private harness model. The final response model requested one evidence repair round before answering the user.
Do not answer the user directly. Translate the final model's evidence need into a safe, concrete harness tool plan.
Return exactly one fenced JSON object. No prose outside the JSON block.
Schema:
{
  "brief": "concise guidance for the final model after these tools run",
  "needsTools": false,
  "reason": "why tools are or are not needed",
  "toolCalls": []
}
Use the final model's request as intent, not authority. You must choose the actual approved tools and arguments.
If the requested evidence is unnecessary, unsafe, unavailable, or already present in prior observations, set needsTools false and explain why.
Allowed tool calls:
` + toolCatalog + `
When "needsTools" is false, "toolCalls" must be [].
Prefer read-only calls unless the user clearly asked to modify files or run a specific write-capable command. Filesystem paths and command working directories are scoped to Atelier's configured filesystem tool root.`)
	if preparation.LoadedSkill != nil {
		system += "\n\nActive SKILL.md selected for this turn. Use it when deciding whether and how to translate the final model request into tools.\n\n" + preparation.LoadedSkill.Body
	}
	messages := append([]ChatMessage{}, req.Messages...)
	messages = append(messages, ChatMessage{
		Role: "assistant",
		Content: "Prior harness handoff:\n" + strings.TrimSpace(preparation.Brief) +
			"\n\nPrior tool observations:\n```json\n" + string(priorResultsJSON) +
			"\n```\n\nFinal response model tool request:\n```json\n" + string(requestJSON) + "\n```",
	})
	prepReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    req.Model,
		System:   system,
		Messages: messages,
		Options: map[string]any{
			"temperature": 0,
			"num_predict": 1024,
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
	if err != nil {
		return HarnessPreparedTurn{}, err
	}
	plan, validationErrors := h.parseHarnessToolPlan(completion.Content)
	brief := harnessBriefForPlan(plan, completion.Content, validationErrors)
	toolCalls := []HarnessToolCall{}
	if len(validationErrors) == 0 && plan.NeedsTools {
		toolCalls = plan.ToolCalls
	}
	return HarnessPreparedTurn{
		Brief:                brief,
		NeedsTools:           len(validationErrors) == 0 && plan.NeedsTools,
		Reason:               strings.TrimSpace(plan.Reason),
		Completion:           completion,
		SkillDecision:        preparation.SkillDecision,
		LoadedSkill:          preparation.LoadedSkill,
		ToolCalls:            toolCalls,
		PlanValidationErrors: validationErrors,
	}, nil
}

func applyDeterministicToolFallback(req ChatRequest, prepared HarnessPreparedTurn) HarnessPreparedTurn {
	if len(prepared.ToolCalls) > 0 {
		return prepared
	}
	if fileName, content, ok := forcedWriteFileRequest(req); ok {
		prepared.NeedsTools = true
		prepared.Reason = appendHarnessOverride(prepared.Reason, "the user explicitly asked Atelier to create a file, so a write_file tool call is required.")
		if strings.TrimSpace(prepared.Brief) == "" {
			prepared.Brief = "Create the requested file in the configured workspace and report the result from the tool output."
		}
		prepared.ToolCalls = []HarnessToolCall{{
			Name:      "write_file",
			Path:      fileName,
			Content:   content,
			Overwrite: true,
		}}
		prepared.PlanValidationErrors = nil
		return prepared
	}
	if !shouldForceWorkspaceList(req) {
		return prepared
	}
	prepared.NeedsTools = true
	prepared.Reason = appendHarnessOverride(prepared.Reason, "the user asked about workspace contents, so a workspace listing is required.")
	if strings.TrimSpace(prepared.Brief) == "" {
		prepared.Brief = "List the configured workspace, then answer from the actual filesystem results."
	}
	prepared.ToolCalls = []HarnessToolCall{{Name: "list_files", Path: "."}}
	return prepared
}

func shouldRepairHarnessToolPlan(content string, loadedSkill *LoadedSkill) bool {
	return loadedSkill != nil || looksLikeToolDelegation(content)
}

func (h *HarnessEngine) repairHarnessToolPlan(ctx context.Context, req ChatRequest, system, invalidContent string, validationErrors []string) (HarnessToolPlan, ChatCompletionResult, []string, error) {
	errorsMarkdown := validationErrorsMarkdown(validationErrors)
	repairSystem := system + "\n\n" + strings.TrimSpace(`

You are repairing your previous invalid harness response.
Return exactly one fenced JSON object matching the schema. No prose outside the JSON block.
If your previous response described a command, convert it into a run_command tool call now.
If an active SKILL.md requires a command for the user request, include that command as a run_command tool call now.
Do not tell the final model to run commands; the final model cannot call tools.`)
	messages := append([]ChatMessage{}, req.Messages...)
	messages = append(messages, ChatMessage{
		Role: "assistant",
		Content: "Invalid harness response:\n" + strings.TrimSpace(invalidContent) +
			"\n\nValidation errors:\n" + errorsMarkdown,
	})
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: "Repair the invalid harness response into the exact fenced JSON tool plan schema.",
	})
	repairReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    req.Model,
		System:   repairSystem,
		Messages: messages,
		Options: map[string]any{
			"temperature": 0,
			"num_predict": 1024,
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, repairReq)
	if err != nil {
		return HarnessToolPlan{}, ChatCompletionResult{}, nil, err
	}
	plan, errors := h.parseHarnessToolPlan(completion.Content)
	return plan, completion, errors, nil
}

func recoverCommandToolPlanFromHarnessText(req ChatRequest, content string) (HarnessToolPlan, bool) {
	for _, commandLine := range commandLineCandidatesFromHarnessText(content) {
		fields, ok := splitShellFields(commandLine)
		if !ok || !commandLineFieldsLookExecutable(fields) {
			continue
		}
		fields = fillCommandPlaceholders(fields, previousAssistantContent(req.Messages))
		call := HarnessToolCall{
			Name:    "run_command",
			Command: fields[0],
			Args:    append([]string{}, fields[1:]...),
		}
		plan := HarnessToolPlan{
			Brief:      "Execute the command recovered from the selected skill instructions, then report the tool result. Do not claim success unless the command succeeds.",
			NeedsTools: true,
			Reason:     "The harness response was not valid JSON, but it contained a concrete command line that must be executed by the harness before the final model responds.",
			ToolCalls:  []HarnessToolCall{call},
		}
		if len(validateHarnessToolPlan(plan, filesystemToolRegistry())) == 0 {
			return plan, true
		}
	}
	return HarnessToolPlan{}, false
}

func commandLineCandidatesFromHarnessText(content string) []string {
	candidates := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "`")
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		candidates = append(candidates, value)
	}
	for _, span := range inlineCodeSpans(content) {
		add(span)
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "-")
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)
		if before, after, ok := strings.Cut(line, ":"); ok {
			label := strings.ToLower(strings.TrimSpace(before))
			if strings.Contains(label, "command") || strings.Contains(label, "tool call") || strings.Contains(label, "structure") {
				add(after)
				continue
			}
		}
		add(line)
	}
	return candidates
}

func inlineCodeSpans(content string) []string {
	spans := []string{}
	inFence := false
	for len(content) > 0 {
		index := strings.Index(content, "`")
		if index < 0 {
			break
		}
		content = content[index:]
		count := 0
		for count < len(content) && content[count] == '`' {
			count++
		}
		content = content[count:]
		if count >= 3 {
			inFence = !inFence
			continue
		}
		if inFence || count != 1 {
			continue
		}
		end := strings.Index(content, "`")
		if end < 0 {
			break
		}
		spans = append(spans, content[:end])
		content = content[end+1:]
	}
	return spans
}

func splitShellFields(commandLine string) ([]string, bool) {
	fields := []string{}
	var current strings.Builder
	var quote rune
	escaped := false
	for _, char := range strings.TrimSpace(commandLine) {
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		if char == '"' || char == '\'' {
			quote = char
			continue
		}
		if char == ' ' || char == '\t' || char == '\n' || char == '\r' {
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(char)
	}
	if escaped || quote != 0 {
		return nil, false
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields, len(fields) > 0
}

func commandLineFieldsLookExecutable(fields []string) bool {
	if len(fields) < 2 || strings.HasPrefix(fields[0], "-") {
		return false
	}
	if strings.ContainsAny(fields[0], " \t\n\r:") {
		return false
	}
	hasFlag := false
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			hasFlag = true
			break
		}
	}
	return hasFlag
}

func fillCommandPlaceholders(fields []string, replacement string) []string {
	replacement = strings.TrimSpace(replacement)
	if replacement == "" {
		return fields
	}
	filled := append([]string{}, fields...)
	for index, field := range filled {
		if isCommandPlaceholder(field) {
			filled[index] = replacement
		}
	}
	return filled
}

func isCommandPlaceholder(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, `"'`)
	if value == "" {
		return false
	}
	return value == "..." ||
		value == "…" ||
		strings.Contains(value, "<") && strings.Contains(value, ">") ||
		strings.Contains(value, "previous") && strings.Contains(value, "content")
}

func previousAssistantContent(messages []ChatMessage) string {
	for index := len(messages) - 2; index >= 0; index-- {
		message := messages[index]
		if message.Role == "assistant" && strings.TrimSpace(message.Content) != "" {
			return message.Content
		}
	}
	return ""
}

func appendHarnessOverride(reason, override string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "Atelier override: " + override
	}
	return reason + "\n\nAtelier override: " + override
}

func forcedWriteFileRequest(req ChatRequest) (string, string, bool) {
	prompt := strings.TrimSpace(lastUserMessage(req.Messages).Content)
	lower := strings.ToLower(prompt)
	if prompt == "" || !strings.Contains(lower, "file") || !strings.Contains(lower, "workspace") {
		return "", "", false
	}
	if !containsAny(lower, []string{"create", "write", "make"}) {
		return "", "", false
	}
	fileName, ok := extractRequestedFileName(prompt)
	if !ok {
		return "", "", false
	}
	content, ok := extractRequestedFileContent(prompt)
	if !ok {
		return "", "", false
	}
	return fileName, content, true
}

func extractRequestedFileName(prompt string) (string, bool) {
	lower := strings.ToLower(prompt)
	markers := []string{"named ", "called "}
	for _, marker := range markers {
		index := strings.Index(lower, marker)
		if index < 0 {
			continue
		}
		rest := strings.TrimSpace(prompt[index+len(marker):])
		name := firstPromptToken(rest)
		if name != "" {
			return strings.Trim(name, `"'.,;:!?`), true
		}
	}
	return "", false
}

func extractRequestedFileContent(prompt string) (string, bool) {
	lower := strings.ToLower(prompt)
	markers := []string{"that says ", "says ", "containing ", "contains ", "with content "}
	for _, marker := range markers {
		index := strings.Index(lower, marker)
		if index < 0 {
			continue
		}
		content := strings.TrimSpace(prompt[index+len(marker):])
		content = strings.Trim(content, `"'`)
		content = strings.TrimRight(content, ".")
		if strings.TrimSpace(content) != "" {
			return content, true
		}
	}
	return "", false
}

func firstPromptToken(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, `"`) || strings.HasPrefix(text, `'`) {
		quote := text[:1]
		rest := text[1:]
		if end := strings.Index(rest, quote); end >= 0 {
			return rest[:end]
		}
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func containsAny(text string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}

func shouldForceWorkspaceList(req ChatRequest) bool {
	prompt := strings.ToLower(strings.TrimSpace(lastUserMessage(req.Messages).Content))
	if prompt == "" || !strings.Contains(prompt, "workspace") {
		return false
	}
	triggers := []string{
		"what",
		"present",
		"contain",
		"contains",
		"inside",
		"list",
		"files",
		"directories",
		"folders",
	}
	for _, trigger := range triggers {
		if strings.Contains(prompt, trigger) {
			return true
		}
	}
	return false
}

func (h *HarnessEngine) preparedResponseRequest(req ChatRequest, responseModel string, preparation HarnessPreparedTurn) ChatRequest {
	responseReq := req
	responseReq.Model = responseModel
	responseReq.System = appendHarnessPreparationToSystem(req.System, preparation)
	return responseReq
}

func appendHarnessPreparationToSystem(system string, preparation HarnessPreparedTurn) string {
	handoffContent := strings.TrimSpace(preparation.Brief)
	if len(preparation.ToolResults) > 0 {
		if data, err := json.MarshalIndent(preparation.ToolResults, "", "  "); err == nil {
			handoffContent = strings.TrimSpace(handoffContent + "\n\nTool observations:\n```json\n" + string(data) + "\n```")
		}
	}
	if handoffContent == "" {
		return system
	}
	handoff := "Atelier harness-prepared brief for this turn. Use it as private guidance for the final response; do not quote or mention it unless the user asks about process.\n\n" + handoffContent + "\n\n" + finalizerToolRequestContract()
	if strings.TrimSpace(system) == "" {
		return handoff
	}
	return strings.TrimSpace(system) + "\n\n" + handoff
}

func finalizerToolRequestContract() string {
	return strings.TrimSpace(`If this private brief is missing evidence that materially changes the answer, you may request ONE harness evidence repair round instead of answering. To do that, make your entire response exactly one fenced JSON object, with no prose before or after it:

` + "```json" + `
{
  "type": "tool_request",
  "reason": "what evidence is missing and why it matters",
  "toolCalls": [
    {"name": "read_file", "path": "relative/path.txt"}
  ]
}
` + "```" + `

The toolCalls array is optional guidance; use [] when you know what evidence is missing but not the exact approved tool call. Use this only when the answer would otherwise be materially weaker or ungrounded. The harness model reviews your request, plans approved tools, the harness executes them, and then asks you to answer again with the new observations. Do not claim the tool request ran unless the harness returns observations.`)
}

func appendFinalizerToolResultsToSystem(system string, execution finalizerHarnessExecution) string {
	requestJSON, _ := json.MarshalIndent(execution.Request, "", "  ")
	planJSON, _ := json.MarshalIndent(execution.Plan.ToolCalls, "", "  ")
	resultsJSON, _ := json.MarshalIndent(execution.Results, "", "  ")
	validation := ""
	if len(execution.PlanValidationErrors) > 0 {
		validation = "\n\nHarness plan validation:\n" + validationErrorsMarkdown(execution.PlanValidationErrors)
	}
	block := strings.TrimSpace(`The final response model requested one additional harness evidence round. The harness model reviewed that request, planned the approved tool work, and the harness executed the approved plan. Use these observations to answer the user now. Do not request another tool round unless the prior request clearly failed and the answer would be unsafe to provide.

Finalizer tool request:
` + "```json\n" + string(requestJSON) + "\n```" + `

Harness finalizer decision:
` + strings.TrimSpace(execution.Plan.Reason) + `

Harness finalizer brief:
` + strings.TrimSpace(execution.Plan.Brief) + `

Harness approved tool plan:
` + "```json\n" + string(planJSON) + "\n```" + validation + `

Finalizer tool observations:
` + "```json\n" + string(resultsJSON) + "\n```")
	if strings.TrimSpace(system) == "" {
		return block
	}
	return strings.TrimSpace(system) + "\n\n" + block
}

func formatFinalizerToolRequestThinking(execution finalizerHarnessExecution) string {
	requestJSON, _ := json.MarshalIndent(execution.Request, "", "  ")
	planJSON, _ := json.MarshalIndent(execution.Plan.ToolCalls, "", "  ")
	resultsJSON, _ := json.MarshalIndent(execution.Results, "", "  ")
	var parts []string
	parts = append(parts, "### Final model evidence request\n\n```json\n"+string(requestJSON)+"\n```")
	if text := strings.TrimSpace(execution.Plan.Brief); text != "" {
		parts = append(parts, "### Harness finalizer preparation\n\n"+text)
	}
	if strings.TrimSpace(execution.Plan.Reason) != "" {
		parts = append(parts, "### Harness finalizer decision\n\n"+execution.Plan.Reason)
	}
	if len(execution.PlanValidationErrors) > 0 {
		parts = append(parts, "### Harness finalizer validation\n\n"+validationErrorsMarkdown(execution.PlanValidationErrors))
	}
	if len(execution.Plan.ToolCalls) > 0 {
		parts = append(parts, "### Harness finalizer tool plan\n\n```json\n"+string(planJSON)+"\n```")
	}
	if len(execution.Results) > 0 {
		parts = append(parts, "### Harness finalizer tool results\n\n```json\n"+string(resultsJSON)+"\n```")
	}
	if text := strings.TrimSpace(execution.Plan.Completion.Thinking); text != "" {
		parts = append(parts, "### Harness finalizer model thinking\n\n"+text)
	}
	return "\n\n" + strings.Join(parts, "\n\n")
}

func formatHarnessPreparationThinking(preparation HarnessPreparedTurn) string {
	if len(preparation.Rounds) > 0 {
		return formatHarnessRoundThinking(preparation)
	}
	var parts []string
	if text := strings.TrimSpace(preparation.Brief); text != "" {
		parts = append(parts, "### Harness preparation\n\n"+text)
	}
	if strings.TrimSpace(preparation.Reason) != "" {
		parts = append(parts, "### Tool decision\n\n"+preparation.Reason)
	}
	if len(preparation.PlanValidationErrors) > 0 {
		parts = append(parts, "### Harness plan validation\n\n"+validationErrorsMarkdown(preparation.PlanValidationErrors))
	}
	if len(preparation.ToolCalls) > 0 {
		if data, err := json.MarshalIndent(preparation.ToolCalls, "", "  "); err == nil {
			parts = append(parts, "### Tool plan\n\n```json\n"+string(data)+"\n```")
		}
	}
	if len(preparation.ToolResults) > 0 {
		if data, err := json.MarshalIndent(preparation.ToolResults, "", "  "); err == nil {
			parts = append(parts, "### Tool results\n\n```json\n"+string(data)+"\n```")
		}
	}
	if text := strings.TrimSpace(preparation.Completion.Thinking); text != "" {
		parts = append(parts, "### Harness model thinking\n\n"+text)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

func formatHarnessRoundThinking(preparation HarnessPreparedTurn) string {
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

func (h *HarnessEngine) parseHarnessToolPlan(content string) (HarnessToolPlan, []string) {
	return parseHarnessToolPlanWithRegistry(content, h.toolRegistry())
}

func parseFinalizerToolRequest(content string, registry HarnessToolRegistry) (FinalizerToolRequest, bool) {
	for _, candidate := range harnessJSONCandidates(content) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(candidate), &raw); err != nil {
			continue
		}
		typeData, ok := raw["type"]
		if !ok {
			continue
		}
		var requestType string
		if err := json.Unmarshal(typeData, &requestType); err != nil || strings.TrimSpace(requestType) != "tool_request" {
			continue
		}
		var request FinalizerToolRequest
		if err := json.Unmarshal([]byte(candidate), &request); err != nil {
			continue
		}
		if len(validateFinalizerToolRequest(request, registry)) == 0 {
			return request, true
		}
	}
	return FinalizerToolRequest{}, false
}

func validateFinalizerToolRequest(request FinalizerToolRequest, registry HarnessToolRegistry) []string {
	var errors []string
	if strings.TrimSpace(request.Type) != "tool_request" {
		errors = append(errors, `type must be "tool_request"`)
	}
	if strings.TrimSpace(request.Reason) == "" {
		errors = append(errors, "reason is required")
	}
	if len(request.ToolCalls) > 3 {
		errors = append(errors, "toolCalls may contain at most 3 calls")
	}
	for index, call := range request.ToolCalls {
		errors = append(errors, validateHarnessToolCall(index, call, registry)...)
	}
	return errors
}

func parseHarnessToolPlanWithRegistry(content string, registry HarnessToolRegistry) (HarnessToolPlan, []string) {
	var parseErrors []string
	for _, candidate := range harnessJSONCandidates(content) {
		plan, errors := decodeAndValidateHarnessToolPlan(candidate, registry)
		if len(errors) == 0 {
			return plan, nil
		}
		parseErrors = errors
		if strings.TrimSpace(plan.Brief) != "" {
			return plan, errors
		}
	}
	if len(parseErrors) == 0 {
		parseErrors = []string{"no valid JSON plan found; expected a fenced JSON object with brief, needsTools, reason, and toolCalls"}
	}
	return HarnessToolPlan{}, parseErrors
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

func harnessBriefForPlan(plan HarnessToolPlan, content string, validationErrors []string) string {
	brief := strings.TrimSpace(plan.Brief)
	if brief != "" {
		return brief
	}
	content = strings.TrimSpace(content)
	if len(validationErrors) > 0 && looksLikeToolDelegation(content) {
		return "The harness could not produce a valid executable tool plan. The final response model cannot call tools or execute commands, so it must not run commands, paste commands as if executed, or claim any tool action succeeded. It should report that the requested tool action could not be completed because the harness plan was invalid."
	}
	return content
}

func looksLikeToolDelegation(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, `"toolcalls"`) ||
		strings.Contains(lower, `"command"`) ||
		strings.Contains(lower, "run_command") ||
		strings.Contains(lower, "tool required") ||
		strings.Contains(lower, "call ") && strings.Contains(lower, " command") ||
		strings.Contains(lower, "execute") && strings.Contains(lower, "command") ||
		strings.Contains(lower, "call ") && strings.Contains(lower, " --")
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

func harnessJSONCandidates(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	candidates := []string{}
	seen := map[string]bool{}
	addCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		candidates = append(candidates, candidate)
	}
	search := content
	for {
		start := strings.Index(search, "```")
		if start < 0 {
			break
		}
		afterFence := search[start+3:]
		lineEnd := strings.Index(afterFence, "\n")
		if lineEnd < 0 {
			break
		}
		fenceInfo := strings.ToLower(strings.TrimSpace(afterFence[:lineEnd]))
		afterHeader := afterFence[lineEnd+1:]
		end := strings.Index(afterHeader, "```")
		if end < 0 {
			break
		}
		if fenceInfo == "" || strings.Contains(fenceInfo, "json") {
			addCandidate(afterHeader[:end])
		}
		search = afterHeader[end+3:]
	}
	for _, candidate := range embeddedJSONObjectCandidates(content) {
		addCandidate(candidate)
	}
	addCandidate(content)
	return candidates
}

func embeddedJSONObjectCandidates(content string) []string {
	candidates := []string{}
	inString := false
	escaped := false
	depth := 0
	start := -1
	for index, char := range content {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = index
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				candidate := strings.TrimSpace(content[start : index+1])
				if strings.Contains(candidate, `"toolCalls"`) &&
					(strings.Contains(candidate, `"brief"`) || strings.Contains(candidate, `"type"`)) {
					candidates = append(candidates, candidate)
				}
				start = -1
			}
		}
	}
	return candidates
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

func shouldBlockInvalidHarnessPlan(prepared HarnessPreparedTurn) bool {
	if len(prepared.PlanValidationErrors) == 0 || len(prepared.ToolCalls) > 0 {
		return false
	}
	if prepared.LoadedSkill != nil || prepared.SkillDecision != nil && prepared.SkillDecision.Selected {
		return true
	}
	return looksLikeToolDelegation(prepared.Completion.Content)
}

func blockingPreparationDoneReason(preparation HarnessPreparedTurn) string {
	if preparation.BlockingToolFailure != nil {
		return toolFailureDoneReason(*preparation.BlockingToolFailure)
	}
	if len(preparation.BlockingPlanFailure) > 0 {
		return "harness_plan_invalid"
	}
	return "harness_blocked"
}

func blockingPreparationUserMessage(preparation HarnessPreparedTurn) string {
	if preparation.BlockingToolFailure != nil {
		return toolFailureUserMessage(*preparation.BlockingToolFailure)
	}
	if len(preparation.BlockingPlanFailure) > 0 {
		detail := strings.Join(preparation.BlockingPlanFailure, "; ")
		if strings.TrimSpace(detail) == "" {
			detail = "the harness plan was invalid"
		}
		return "I couldn't complete the requested action because the harness could not produce a valid executable tool plan: " + detail + "."
	}
	return "I couldn't complete the requested action because the harness could not prepare it safely."
}

func blockingToolFailure(results []HarnessToolResult) (HarnessToolResult, bool) {
	for _, result := range results {
		if strings.TrimSpace(result.Status) != "completed" {
			return result, true
		}
		if strings.TrimSpace(result.Error) != "" {
			return result, true
		}
	}
	return HarnessToolResult{}, false
}

func toolFailureDoneReason(result HarnessToolResult) string {
	if strings.TrimSpace(result.Status) == "denied" {
		return "tool_denied"
	}
	return "tool_failed"
}

func toolFailureDetail(result HarnessToolResult) string {
	if errorText := strings.TrimSpace(result.Error); errorText != "" {
		return errorText
	}
	if summary := strings.TrimSpace(result.Summary); summary != "" {
		return summary
	}
	if name := strings.TrimSpace(result.Name); name != "" {
		return name + " did not complete"
	}
	return "tool did not complete"
}

func toolFailureDecision(result HarnessToolResult) string {
	return "A required harness tool did not complete, so the final response must report the failure instead of claiming success."
}

func toolFailureBrief(result HarnessToolResult) string {
	return "The requested action was not completed because a required tool failed. Tell the user plainly that the action did not finish, include the failure reason, and do not claim success.\n\nFailure: " + toolFailureDetail(result)
}

func toolFailureUserMessage(result HarnessToolResult) string {
	name := strings.TrimSpace(result.Name)
	if name == "" {
		name = "tool"
	}
	detail := toolFailureDetail(result)
	if strings.TrimSpace(result.Status) == "denied" {
		return fmt.Sprintf("I couldn't complete the requested action because `%s` was denied: %s.", name, detail)
	}
	return fmt.Sprintf("I couldn't complete the requested action because `%s` failed: %s.", name, detail)
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

func insertFinalizerToolRequestStep(run *HarnessRun) {
	for _, step := range run.Steps {
		if step.Kind == "final_tool_request" {
			return
		}
	}
	now := time.Now().Format(time.RFC3339)
	step := HarnessStep{
		ID:        "step_final_tool_request",
		Kind:      "final_tool_request",
		Iteration: 2,
		Provider:  "tools",
		Status:    "pending",
		StartedAt: now,
		Summary:   "harness planning for final response model tool request",
	}
	insertAt := len(run.Steps)
	for index, existing := range run.Steps {
		if existing.Kind == "evaluation" {
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
