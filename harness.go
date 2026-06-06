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
	preparation, err := h.prepareChatTurn(ctx, req)
	if err != nil {
		h.completeStep(&run, "preparing", "failed", "", 0, err.Error())
		h.completeRun(&run, "failed", "harness_prepare_error")
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	preparationThinking := formatHarnessPreparationThinking(preparation)
	h.completeStep(&run, "preparing", "completed", preparation.Reason, preparation.EvalTokens, "")
	if preparationThinking != "" {
		h.app.emitChatEvent(ChatStreamEvent{
			RequestID:      requestID,
			Thinking:       preparationThinking,
			ConversationID: conversationID,
		})
	}

	responseReq := h.preparedResponseRequest(req, responseModel, preparation.Content)
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

func (h *HarnessEngine) prepareChatTurn(ctx context.Context, req ChatRequest) (ChatCompletionResult, error) {
	system := strings.TrimSpace(`You are Atelier's private harness model. Prepare a concise markdown brief for the next model that will answer the user.
Do not answer the user directly. Do not include hidden chain-of-thought. Capture only useful answer guidance: intent, constraints, relevant context, response shape, and cautions.
Keep it compact.`)
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
	return h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, prepReq)
}

func (h *HarnessEngine) preparedResponseRequest(req ChatRequest, responseModel, preparation string) ChatRequest {
	responseReq := req
	responseReq.Model = responseModel
	responseReq.System = appendHarnessPreparationToSystem(req.System, preparation)
	return responseReq
}

func appendHarnessPreparationToSystem(system, preparation string) string {
	preparation = strings.TrimSpace(preparation)
	if preparation == "" {
		return system
	}
	handoff := "Atelier harness-prepared brief for this turn. Use it as private guidance for the final response; do not quote or mention it unless the user asks about process.\n\n" + preparation
	if strings.TrimSpace(system) == "" {
		return handoff
	}
	return strings.TrimSpace(system) + "\n\n" + handoff
}

func formatHarnessPreparationThinking(preparation ChatCompletionResult) string {
	var parts []string
	if text := strings.TrimSpace(preparation.Content); text != "" {
		parts = append(parts, "### Harness preparation\n\n"+text)
	}
	if text := strings.TrimSpace(preparation.Thinking); text != "" {
		parts = append(parts, "### Harness model thinking\n\n"+text)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
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
