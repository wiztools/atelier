package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	harnessChatMaxSteps    = 3
	harnessChatMaxWallTime = 2 * time.Minute
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
	h.startStep(&run, "model_call")
	resp, err := h.app.ollamaClient(req.BaseURL).OpenChatStream(ctx, responseReq)
	if err != nil {
		h.completeStep(&run, "model_call", "failed", "", 0, err.Error())
		h.completeRun(&run, "failed", "provider_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	defer resp.Body.Close()
	h.completeStep(&run, "model_call", "completed", "", 0, "")
	h.startStep(&run, "streaming")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var assistantContent strings.Builder
	var assistantThinking strings.Builder
	assistantThinking.WriteString(preparationThinking)
	var finalModel string
	var finalReason string
	var finalTokens int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var chunk ollamaChatChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			h.completeStep(&run, "streaming", "failed", finalReason, finalTokens, err.Error())
			h.completeRun(&run, "failed", "decode_error")
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
			return
		}
		if chunk.Error != "" {
			h.completeStep(&run, "streaming", "failed", finalReason, finalTokens, chunk.Error)
			h.completeRun(&run, "failed", "provider_error")
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: chunk.Error, Done: true})
			return
		}

		assistantContent.WriteString(chunk.Message.Content)
		assistantThinking.WriteString(chunk.Message.Thinking)
		if chunk.Model != "" {
			finalModel = chunk.Model
		}
		if chunk.DoneReason != "" {
			finalReason = chunk.DoneReason
		}
		if chunk.EvalCount > 0 {
			finalTokens = chunk.EvalCount
		}

		if chunk.Done {
			var err error
			if strings.TrimSpace(finalModel) == "" {
				finalModel = responseModel
			}
			h.completeStep(&run, "streaming", "completed", finalReason, finalTokens, "")
			h.evaluateChatRun(&run, assistantContent.String(), finalReason)
			h.startStep(&run, "saved")
			h.completeStep(&run, "saved", "completed", finalReason, finalTokens, "")
			h.completeRun(&run, "completed", "final")
			err = h.SaveAssistantTurn(conversationID, assistantContent.String(), assistantThinking.String(), finalModel, finalReason, finalTokens, run)
			if err != nil {
				h.completeStep(&run, "saved", "failed", finalReason, finalTokens, err.Error())
				h.completeRun(&run, "failed", "history_save_error")
				h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: fmt.Sprintf("history save failed: %v", err), Done: true})
				return
			}
		}

		h.app.emitChatEvent(ChatStreamEvent{
			RequestID:      requestID,
			Content:        chunk.Message.Content,
			Thinking:       chunk.Message.Thinking,
			Done:           chunk.Done,
			Model:          chunk.Model,
			Reason:         chunk.DoneReason,
			Tokens:         chunk.EvalCount,
			ConversationID: conversationID,
		})
		if chunk.Done {
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		h.completeStep(&run, "streaming", "failed", finalReason, finalTokens, err.Error())
		h.completeRun(&run, "failed", "stream_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
	}
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

func (h *HarnessEngine) toolRegistry() HarnessToolRegistry {
	return defaultHarnessToolRegistry(h.config)
}

func (h *HarnessEngine) prepareChatTurnLoop(ctx context.Context, requestID, conversationID string, req ChatRequest, run *HarnessRun) (HarnessPreparedTurn, error) {
	first, err := h.prepareChatTurn(ctx, req)
	if err != nil {
		return HarnessPreparedTurn{}, err
	}
	rounds := []HarnessToolRound{preparedTurnToRound(1, first)}
	if len(first.ToolCalls) > 0 {
		insertToolCallStep(run)
		h.startStep(run, "tool_call")
		first.ToolResults = h.runHarnessToolCalls(ctx, requestID, conversationID, first.ToolCalls)
		rounds[0].ToolResults = first.ToolResults
		h.attachToolActivities(run, first.ToolResults)
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
		final.ToolResults = append([]HarnessToolResult{}, first.ToolResults...)
		if len(inspection.ToolCalls) > 0 {
			insertToolCallStep(run)
			h.startStep(run, "tool_call")
			inspection.ToolResults = h.runHarnessToolCalls(ctx, requestID, conversationID, inspection.ToolCalls)
			rounds[1].ToolResults = inspection.ToolResults
			final.ToolResults = append(final.ToolResults, inspection.ToolResults...)
			h.attachToolActivities(run, final.ToolResults)
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
		ToolCalls:            turn.ToolCalls,
		ToolResults:          turn.ToolResults,
		PlanValidationErrors: turn.PlanValidationErrors,
	}
}

func (h *HarnessEngine) prepareChatTurn(ctx context.Context, req ChatRequest) (HarnessPreparedTurn, error) {
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
Allowed tool calls:
` + toolCatalog + `
When "needsTools" is false, "toolCalls" must be [].
Prefer read-only calls unless the user clearly asks to modify files or run a specific write-capable command. Filesystem paths and command working directories are scoped to Atelier's configured filesystem tool root.`)
	if strings.TrimSpace(req.System) != "" {
		system += "\n\nUser-facing system prompt to preserve:\n" + strings.TrimSpace(req.System)
	}
	prepReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    req.Model,
		System:   system,
		Messages: req.Messages,
		Options: map[string]any{
			"temperature": 0,
			"num_predict": 512,
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
	if err != nil {
		return HarnessPreparedTurn{}, err
	}
	plan, validationErrors := h.parseHarnessToolPlan(completion.Content)
	brief := strings.TrimSpace(plan.Brief)
	if brief == "" {
		brief = strings.TrimSpace(completion.Content)
	}
	toolCalls := []HarnessToolCall{}
	if len(validationErrors) == 0 && plan.NeedsTools {
		toolCalls = plan.ToolCalls
	}
	prepared := HarnessPreparedTurn{
		Brief:                brief,
		NeedsTools:           len(validationErrors) == 0 && plan.NeedsTools,
		Reason:               strings.TrimSpace(plan.Reason),
		Completion:           completion,
		ToolCalls:            toolCalls,
		PlanValidationErrors: validationErrors,
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
			"num_predict": 512,
		},
	}
	completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
	if err != nil {
		return HarnessPreparedTurn{}, err
	}
	plan, validationErrors := h.parseHarnessToolPlan(completion.Content)
	brief := strings.TrimSpace(plan.Brief)
	if brief == "" {
		brief = strings.TrimSpace(completion.Content)
	}
	toolCalls := []HarnessToolCall{}
	if len(validationErrors) == 0 && plan.NeedsTools {
		toolCalls = plan.ToolCalls
	}
	return HarnessPreparedTurn{
		Brief:                brief,
		NeedsTools:           len(validationErrors) == 0 && plan.NeedsTools,
		Reason:               strings.TrimSpace(plan.Reason),
		Completion:           completion,
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
	handoff := "Atelier harness-prepared brief for this turn. Use it as private guidance for the final response; do not quote or mention it unless the user asks about process.\n\n" + handoffContent
	if strings.TrimSpace(system) == "" {
		return handoff
	}
	return strings.TrimSpace(system) + "\n\n" + handoff
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
			candidates = append(candidates, strings.TrimSpace(afterHeader[:end]))
		}
		search = afterHeader[end+3:]
	}
	candidates = append(candidates, content)
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
	activities := make([]HarnessToolActivity, 0, len(results))
	for _, result := range results {
		activities = append(activities, h.toolActivityFromResult(result))
	}
	for index := range run.Steps {
		if run.Steps[index].Kind == "tool_call" {
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
