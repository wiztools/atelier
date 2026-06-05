package main

import "strings"

type HarnessEngine struct {
	config AppConfig
}

func newHarnessEngine(config AppConfig) HarnessEngine {
	return HarnessEngine{config: config}
}

func (engine HarnessEngine) SaveChatTurn(req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int, title string) (string, error) {
	if strings.TrimSpace(req.ConversationID) != "" {
		return appendChatConversation(engine.config, req, assistantContent, assistantThinking, model, reason, tokens)
	}
	return writeChatConversation(engine.config, req, assistantContent, assistantThinking, model, reason, tokens, title)
}
