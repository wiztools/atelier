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
	now := time.Now()
	run := HarnessRun{
		ID:        randomID("run"),
		Mode:      "chat",
		Status:    "running",
		StartedAt: now.Format(time.RFC3339),
		Steps: []HarnessStep{
			{
				ID:        "step_000001",
				Kind:      "model_call",
				Provider:  "ollama",
				Model:     req.Model,
				Status:    "running",
				StartedAt: now.Format(time.RFC3339),
			},
		},
	}
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}
	if req.System != "" {
		body["messages"] = append([]ChatMessage{{Role: "system", Content: req.System}}, req.Messages...)
	}
	if req.Think != nil {
		body["think"] = req.Think
	}
	if req.Options != nil {
		body["options"] = req.Options
	}

	resp, err := h.app.postJSON(ctx, h.app.resolveBaseURL(req.BaseURL)+"/api/chat", body)
	if err != nil {
		h.completeRun(&run, "failed", "", 0)
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var assistantContent strings.Builder
	var assistantThinking strings.Builder
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
			h.completeRun(&run, "failed", finalReason, finalTokens)
			h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
			return
		}
		if chunk.Error != "" {
			h.completeRun(&run, "failed", finalReason, finalTokens)
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

		conversationID := ""
		if chunk.Done {
			var err error
			h.completeRun(&run, "completed", finalReason, finalTokens)
			title := ""
			if strings.TrimSpace(req.ConversationID) == "" {
				title = h.app.generateConversationTitle(h.config, req, assistantContent.String())
			}
			conversationID, err = h.SaveChatTurn(req, assistantContent.String(), assistantThinking.String(), finalModel, finalReason, finalTokens, title, run)
			if err != nil {
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
		h.completeRun(&run, "failed", finalReason, finalTokens)
		h.app.emitChatEvent(ChatStreamEvent{RequestID: requestID, Error: err.Error(), Done: true})
	}
}

func newChatHarnessRun(model, reason string, tokens int) HarnessRun {
	now := time.Now().Format(time.RFC3339)
	run := HarnessRun{
		ID:          randomID("run"),
		Mode:        "chat",
		Status:      "completed",
		StartedAt:   now,
		CompletedAt: now,
		Steps: []HarnessStep{
			{
				ID:          "step_000001",
				Kind:        "model_call",
				Provider:    "ollama",
				Model:       model,
				Status:      "completed",
				StartedAt:   now,
				CompletedAt: now,
				DoneReason:  reason,
				Tokens:      tokens,
			},
		},
	}
	return run
}

func firstHarnessRun(model, reason string, tokens int, runs []HarnessRun) HarnessRun {
	if len(runs) > 0 && strings.TrimSpace(runs[0].ID) != "" {
		return runs[0]
	}
	return newChatHarnessRun(model, reason, tokens)
}

func (h *HarnessEngine) completeRun(run *HarnessRun, status, reason string, tokens int) {
	now := time.Now().Format(time.RFC3339)
	run.Status = status
	run.CompletedAt = now
	if len(run.Steps) == 0 {
		return
	}
	run.Steps[0].Status = status
	run.Steps[0].CompletedAt = now
	run.Steps[0].DoneReason = reason
	run.Steps[0].Tokens = tokens
}

func (h *HarnessEngine) SaveChatTurn(req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, title string, run HarnessRun) (string, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return writeChatConversation(h.config, req, assistantContent, assistantThinking, model, reason, tokens, title, run)
	}
	return appendChatConversation(h.config, req, assistantContent, assistantThinking, model, reason, tokens, run)
}
