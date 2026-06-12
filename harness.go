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

// The chat model's system prompt only ever receives these code-authored notes.
// Planner output (briefs, reasons) is telemetry and thinking, never prompt text,
// so a weaker tool model can't cap what the chat model is allowed to know.
const toolEvidenceSystemNote = "Atelier ran workspace tools for this turn. Their observations appear as tool messages at the end of the conversation. Treat them as evidence: report failures honestly and do not claim an action succeeded unless an observation shows it. You cannot call tools yourself; if the user asked for an action that no observation confirms, say plainly that it was not completed."

const invalidPlanSystemNote = "Atelier could not produce a valid tool plan for this turn, so no tools ran. You cannot call tools or execute commands. Do not run commands, paste commands as if executed, or claim any tool action succeeded. If the user asked for a tool action, report plainly that it could not be completed."

const invalidPlanAfterToolsSystemNote = "Atelier ran workspace tools for this turn, but its latest tool plan was invalid, so the most recently requested action did not run. Tool observations appear as tool messages at the end of the conversation. Treat them as evidence: report failures honestly and do not claim an action succeeded unless an observation shows it. You cannot call tools yourself; if the user asked for an action that no observation confirms, say plainly that it was not completed."

type HarnessEngine struct {
	config AppConfig
	app    *App
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
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(h.config.Providers.Ollama.Models.Chat)
	}
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

	chatModel := h.chatModelForRequest(req)
	toolModel := h.toolModelFor(chatModel)
	run := newHarnessRun(requestID, conversationID)
	queued := run.appendStep("queued", 1, "", "", "turn accepted by harness")
	run.completeStep(queued, "completed", "", 0, "")

	skillIndex, skillIndexErr := loadSkillIndex(defaultSkillRoots())
	var explicitSkill *SkillIndexEntry
	explicitReason := ""
	if skillIndexErr == nil {
		if entry, reason, ok := explicitSkillSelection(skillIndex, lastUserMessage(req.Messages).Content); ok {
			explicitSkill = &entry
			explicitReason = reason
		}
	}

	decision := HarnessTriageDecision{NeedsTools: true, Reason: "user explicitly referenced a skill"}
	if explicitSkill == nil {
		triage := run.appendStep("triage", 1, "ollama", chatModel, "chat model deciding whether tools are needed")
		var completion ChatCompletionResult
		decision, completion = h.triageChatTurn(ctx, req, chatModel, skillIndex)
		run.Steps[triage].Decision = triageDecisionLabel(decision)
		status := "completed"
		if decision.Error != "" {
			status = "failed"
		}
		run.completeStep(triage, status, completion.Reason, completion.EvalTokens, decision.Error)
	}
	run.Triage = &decision

	var preparation HarnessPreparedTurn
	preparationThinking := ""
	if decision.NeedsTools {
		toolReq := req
		toolReq.Model = toolModel
		var err error
		preparation, err = h.prepareChatTurnLoop(ctx, requestID, conversationID, toolReq, harnessTurnContext{
			SkillIndex:     skillIndex,
			SkillIndexErr:  skillIndexErr,
			ExplicitSkill:  explicitSkill,
			ExplicitReason: explicitReason,
			ToolTask:       decision.ToolTask,
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

	responseReq := h.preparedResponseRequest(req, chatModel, preparation)
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
		assistantContent = harnessEmptyResponseNotice(preparation.ToolResults)
		finalContentEmitted = false
		if strings.TrimSpace(finalReason) == "" {
			finalReason = "empty_response_notice"
		}
	}
	if strings.TrimSpace(finalModel) == "" {
		finalModel = chatModel
	}
	images, imageReq := imagesFromToolResults(preparation.ToolResults)
	h.evaluateChatRun(&run, assistantContent, finalReason)
	// The run is serialized into the saved turn, so the saved step is marked
	// completed optimistically and flipped to failed if the write errors.
	saved := run.appendStep("saved", 1, "", "", "assistant turn and harness run stored in history")
	run.completeStep(saved, "completed", finalReason, finalTokens, "")
	run.complete("completed", "final")
	var saveErr error
	if len(images) > 0 {
		saveErr = appendChatAssistantTurnWithImages(h.config, conversationID, assistantContent, assistantThinking, finalModel, finalReason, images, "", run, imageReq)
	} else {
		saveErr = h.SaveAssistantTurn(conversationID, assistantContent, assistantThinking, finalModel, finalReason, finalTokens, run)
	}
	if saveErr != nil {
		run.completeStep(saved, "failed", finalReason, finalTokens, saveErr.Error())
		run.complete("failed", "history_save_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history save failed: %v", saveErr), Done: true})
		return
	}
	terminalContent := assistantContent
	if finalContentEmitted {
		terminalContent = ""
	}
	h.app.emitChatEvent(ChatStreamEvent{
		RequestID:      requestID,
		Content:        terminalContent,
		Images:         images,
		Done:           true,
		Model:          finalModel,
		Reason:         finalReason,
		Tokens:         finalTokens,
		ConversationID: conversationID,
	})
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

func (h *HarnessEngine) chatModelForRequest(req ChatRequest) string {
	model := strings.TrimSpace(req.SelectedModel)
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	return model
}

func triageDecisionLabel(decision HarnessTriageDecision) string {
	if decision.NeedsTools {
		return "tools"
	}
	return "direct_answer"
}

// toolModelFor resolves the planning model for the tool path; an unset config
// falls back to the chat model so a one-model setup still works.
func (h *HarnessEngine) toolModelFor(chatModel string) string {
	model := strings.TrimSpace(h.config.Providers.Ollama.Models.Tools)
	if model == "" {
		return chatModel
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

// harnessTurnContext carries what RunChatStream resolved before entering the
// tool path: the skill index (loaded once per turn), any explicit skill the
// user named, and the chat model's triage task for the planner.
type harnessTurnContext struct {
	SkillIndex     []SkillIndexEntry
	SkillIndexErr  error
	ExplicitSkill  *SkillIndexEntry
	ExplicitReason string
	ToolTask       string
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
func (h *HarnessEngine) prepareChatTurnLoop(ctx context.Context, requestID, conversationID string, req ChatRequest, turn harnessTurnContext, run *HarnessRun) (HarnessPreparedTurn, error) {
	skillDecision, loadedSkill := h.selectSkillForTurn(ctx, req, turn)
	run.Skill = skillDecision
	registry := h.toolRegistry()
	system := h.plannerSystemPrompt(registry, req, loadedSkill, turn.ToolTask)
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
			prepared.ToolCalls = nil
			messages = append(messages,
				ChatMessage{Role: "assistant", Content: completion.Content},
				ChatMessage{Role: "user", Content: "Your previous response was not a valid tool plan:\n" + validationErrorsMarkdown(validationErrors) + "\n\nReturn a corrected plan that matches the response schema."},
			)
			continue
		}
		run.completeStep(planning, "completed", completion.Reason, completion.EvalTokens, "")

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

func (h *HarnessEngine) plannerSystemPrompt(registry HarnessToolRegistry, req ChatRequest, loadedSkill *LoadedSkill, toolTask string) string {
	system := strings.TrimSpace(fmt.Sprintf(`You are Atelier's private harness model. You gather evidence for the final model that will answer the user.
Do not answer the user directly. Do not include hidden chain-of-thought. Respond only with a JSON tool plan matching the response schema:
{
  "brief": "concise guidance for the chat model",
  "needsTools": false,
  "reason": "why tools are or are not needed",
  "toolCalls": []
}
You plan in rounds, at most %d in total. Each round may request up to 3 tool calls. The harness executes them and returns each result, including failures, as a tool message; read the results and plan the next round.
When you have enough evidence, or none is needed, set "needsTools" false with empty "toolCalls" and write the brief: intent, constraints, relevant evidence, response shape, and cautions for the final model.
A failed or denied tool call is information, not a dead end: adapt the plan or tell the final model to report the failure plainly. Never claim an action succeeded unless a tool result shows it.
The chat model that answers the user cannot call tools or execute commands. If a user request or active SKILL.md requires a command, include it as a tool call now. Do not put instructions like "run this command" in the brief.
Allowed tool calls:
%s
When "needsTools" is false, "toolCalls" must be []. Prefer read-only calls unless the user clearly asks to modify files or run a specific write-capable command. Filesystem paths and command working directories are scoped to Atelier's configured filesystem tool root.`, harnessChatMaxSteps, registry.PromptCatalog()))
	if strings.TrimSpace(req.System) != "" {
		system += "\n\nUser-facing system prompt to preserve:\n" + strings.TrimSpace(req.System)
	}
	if loadedSkill != nil {
		system += "\n\nActive SKILL.md selected for this turn. Follow these instructions when planning tools and writing the brief, including any workflow or command guidance that applies. Do not quote the skill unless the user asks about process.\n\n" + loadedSkill.Body
	}
	if strings.TrimSpace(toolTask) != "" {
		system += "\n\nThe chat model triaged this turn and requested tool evidence:\n" + strings.TrimSpace(toolTask)
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
	responseReq.System = appendToolEvidenceToSystem(req.System, preparation)
	messages := append([]ChatMessage{}, req.Messages...)
	if len(preparation.ToolResults) > 0 {
		messages = append(messages, toolResultMessages(preparation.ToolResults)...)
	}
	numCtx := h.numCtx()
	responseReq.Messages = truncateChatHistory(messages, historyBudgetChars(numCtx, responseReq.System, numCtx/4))
	responseReq.Options = withNumCtx(req.Options, numCtx)
	return responseReq
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
