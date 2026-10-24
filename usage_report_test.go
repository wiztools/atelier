package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// turnID formats the zero-padded turn-file id the store writes.
func turnID(n int) string {
	return fmt.Sprintf("turn_%06d", n)
}

// seedUsageConversation writes a conversation record plus its turn files under
// the history tree, the shape the usage report folds.
func seedUsageConversation(t *testing.T, historyDir, id, projectID, title string, turns []HistoryTurn) {
	t.Helper()
	dir := filepath.Join(historyDir, "conversations", id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	conversation := HistoryConversation{
		SchemaVersion: 2,
		ID:            id,
		Kind:          "chat",
		Title:         title,
		CreatedAt:     turns[0].CreatedAt,
		UpdatedAt:     turns[len(turns)-1].CreatedAt,
		ProjectID:     projectID,
	}
	if err := writeJSONFile(filepath.Join(dir, "conversation.json"), conversation); err != nil {
		t.Fatalf("write conversation.json: %v", err)
	}
	for i, turn := range turns {
		if turn.ID == "" {
			turn.ID = fmt.Sprintf("turn_%06d", i+1)
		}
		turn.ConversationID = id
		if err := writeJSONFile(filepath.Join(dir, "turns", turn.ID+".json"), turn); err != nil {
			t.Fatalf("write %s: %v", turn.ID, err)
		}
	}
}

// assistantRunTurn builds an assistant turn whose ProviderResponse carries the
// given run, the persisted shape every harness turn uses.
func assistantRunTurn(id, createdAt string, run HarnessRun) HistoryTurn {
	return HistoryTurn{
		SchemaVersion: 1,
		ID:            id,
		CreatedAt:     createdAt,
		Kind:          "chat",
		Role:          "assistant",
		ProviderResponse: map[string]any{
			"harnessRun": run,
			"costMicros": harnessRunCostMicros(run),
		},
	}
}

func userTurn(id, createdAt string) HistoryTurn {
	return HistoryTurn{
		SchemaVersion: 1,
		ID:            id,
		CreatedAt:     createdAt,
		Kind:          "chat",
		Role:          "user",
		Content:       []HistoryContent{{Type: "text", Text: "hi"}},
	}
}

func TestUsageLedgerFoldsRun(t *testing.T) {
	ledger := newUsageLedger()
	ledger.foldRun(HarnessRun{
		Steps: []HarnessStep{
			{Kind: "queued", Status: "completed"},
			{Kind: "triage", Provider: "openrouter", Model: "claude-x", Status: "completed", Tokens: 10, PromptTokens: 100, CostMicros: 1000},
			{Kind: "planning", Provider: "openrouter", Model: "claude-x", Status: "completed", Tokens: 5, PromptTokens: 80, CostMicros: 500},
			{Kind: "tool_call", Provider: "tools", Status: "completed", Tools: []HarnessToolActivity{
				{Name: "generate_video", Status: "completed", Provider: "fal", Model: "fal-ai/kling", MediaKind: "video", MediaCount: 1, CostMicros: 200000},
				{Name: "read_file", Status: "completed"},
				{Name: "run_command", Status: "failed"},
			}},
			{Kind: "model_call", Provider: "openrouter", Model: "claude-x", Status: "completed"},
			{Kind: "streaming", Provider: "openrouter", Model: "claude-x", Status: "completed", Tokens: 50, PromptTokens: 200, CostMicros: 2000},
			{Kind: "saved", Provider: "ollama", Model: "claude-x", Status: "completed", Tokens: 65},
			{Kind: "evaluation", Status: "completed"},
		},
	})

	if ledger.costMicros != 203500 {
		t.Fatalf("costMicros: want 203500, got %d", ledger.costMicros)
	}
	if ledger.modelCalls != 3 {
		t.Fatalf("modelCalls: want 3 (triage, planning, final attempt), got %d", ledger.modelCalls)
	}
	if ledger.toolCalls != 3 || ledger.failedToolCalls != 1 {
		t.Fatalf("toolCalls=%d failedToolCalls=%d, want 3/1", ledger.toolCalls, ledger.failedToolCalls)
	}
	if ledger.mediaVideos != 1 {
		t.Fatalf("mediaVideos: want 1, got %d", ledger.mediaVideos)
	}
	models, tools := ledger.rows()
	if len(models) != 2 {
		t.Fatalf("model rows: want 2, got %d (%v)", len(models), models)
	}
	// Sorted by cost: the video model's $0.20 outranks the chat model's $0.0035.
	if models[0].Model != "fal-ai/kling" {
		t.Fatalf("first model row: want fal-ai/kling, got %s", models[0].Model)
	}
	if models[0].Calls != 1 || models[0].MediaVideos != 1 || models[0].CostMicros != 200000 {
		t.Fatalf("kling row: calls=%d video=%d cost=%d", models[0].Calls, models[0].MediaVideos, models[0].CostMicros)
	}
	// The chat model counts real invocations (triage, planning, the final
	// attempt) — streaming is the body of the same call, and the fallback
	// saved step is bookkeeping.
	if models[1].Calls != 3 || models[1].PromptTokens != 380 || models[1].CompletionTokens != 65 || models[1].CostMicros != 3500 {
		t.Fatalf("claude-x row: %+v", models[1])
	}
	if len(tools) != 3 {
		t.Fatalf("tool rows: want 3, got %d (%v)", len(tools), tools)
	}
	if tools[0].Name != "generate_video" || tools[0].MediaVideos != 1 || tools[0].CostMicros != 200000 {
		t.Fatalf("generate_video row: %+v", tools[0])
	}
	var command *UsageToolRow
	for i := range tools {
		if tools[i].Name == "run_command" {
			command = &tools[i]
		}
	}
	if command == nil || command.Calls != 1 || command.Failures != 1 {
		t.Fatalf("run_command row: %+v", command)
	}
}

func TestUsageLedgerFoldsEditOperation(t *testing.T) {
	ledger := newUsageLedger()
	ledger.foldEditOperation(EditOperation{
		ID:         "op_1",
		Kind:       editOperationKindInpaint,
		Status:     "completed",
		Provider:   "fal",
		Model:      "fal-ai/flux-kontext",
		CostMicros: 42000,
	})
	// A local op carries no provider/model and bills nothing.
	ledger.foldEditOperation(EditOperation{
		ID:      "op_2",
		Kind:    editOperationKindCrop,
		Status:  "completed",
		Backend: "local",
	})
	// A failed cloud op still burns cost but produces no media.
	ledger.foldEditOperation(EditOperation{
		ID:          "op_3",
		Kind:        editOperationKindInpaint,
		Status:      "failed",
		Provider:    "replicate",
		Model:       "black-forest-labs/flux-kontext-pro",
		CostUnknown: true,
	})

	if ledger.costMicros != 42000 {
		t.Fatalf("costMicros: want 42000, got %d", ledger.costMicros)
	}
	if !ledger.costUnknown {
		t.Fatal("costUnknown: want true after a failed replicate op")
	}
	if ledger.mediaImages != 2 {
		t.Fatalf("mediaImages: want 2 (the completed inpaint and the crop's output), got %d", ledger.mediaImages)
	}
	if ledger.toolCalls != 3 || ledger.failedToolCalls != 1 {
		t.Fatalf("toolCalls=%d failedToolCalls=%d, want 3/1", ledger.toolCalls, ledger.failedToolCalls)
	}
	models, tools := ledger.rows()
	if len(models) != 1 {
		t.Fatalf("model rows: want 1 (local crop has no model), got %d", len(models))
	}
	if models[0].Model != "fal-ai/flux-kontext" || models[0].MediaImages != 1 || models[0].CostMicros != 42000 {
		t.Fatalf("flux row: %+v", models[0])
	}
	if len(tools) != 2 {
		t.Fatalf("tool rows: want 2, got %d (%v)", len(tools), tools)
	}
	if tools[0].Name != editOperationKindInpaint || tools[0].Calls != 2 || tools[0].Failures != 1 || tools[0].MediaImages != 1 {
		t.Fatalf("inpaint row: %+v", tools[0])
	}
	if tools[1].Name != editOperationKindCrop || tools[1].CostMicros != 0 || tools[1].MediaImages != 1 {
		t.Fatalf("crop row: %+v", tools[1])
	}
}

func TestUsageLedgerMerge(t *testing.T) {
	first := newUsageLedger()
	first.foldRun(HarnessRun{Steps: []HarnessStep{
		{Kind: "triage", Provider: "openrouter", Model: "m1", Status: "completed", Tokens: 10, CostMicros: 100},
	}})
	second := newUsageLedger()
	second.foldRun(HarnessRun{Steps: []HarnessStep{
		{Kind: "model_call", Provider: "openrouter", Model: "m1", Status: "completed"},
		{Kind: "streaming", Provider: "openrouter", Model: "m1", Status: "completed", Tokens: 30, CostMicros: 250},
	}})
	first.merge(second)
	if first.costMicros != 350 || first.modelCalls != 2 || first.assistantTurns != 0 {
		t.Fatalf("merged ledger: cost=%d calls=%d turns=%d", first.costMicros, first.modelCalls, first.assistantTurns)
	}
	models, _ := first.rows()
	if len(models) != 1 || models[0].Calls != 2 || models[0].CompletionTokens != 40 {
		t.Fatalf("merged model rows: %+v", models)
	}
}

func TestConversationUsageReport(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	run := HarnessRun{Steps: []HarnessStep{
		{Kind: "triage", Provider: "openrouter", Model: "m1", Status: "completed", Tokens: 8, PromptTokens: 90, CostMicros: 300},
		{Kind: "tool_call", Status: "completed", Tools: []HarnessToolActivity{
			{Name: "generate_image", Status: "completed", Provider: "fal", Model: "fal-ai/flux", MediaKind: "image", MediaCount: 2, CostMicros: 50000},
		}},
		{Kind: "model_call", Provider: "openrouter", Model: "m1", Status: "completed"},
		{Kind: "streaming", Provider: "openrouter", Model: "m1", Status: "completed", Tokens: 42, PromptTokens: 210, CostMicros: 900},
	}}
	seedUsageConversation(t, historyDir, "conv1", "", "Reported chat", []HistoryTurn{
		userTurn(turnID(1), "2026-10-24T10:00:00Z"),
		assistantRunTurn(turnID(2), "2026-10-24T10:00:05Z", run),
	})
	// A user turn with no run must not count as an assistant turn.
	seedUsageConversation(t, historyDir, "conv-user-only", "", "No assistant", []HistoryTurn{
		userTurn(turnID(1), "2026-10-24T09:00:00Z"),
	})

	report, err := conversationUsageReport(ConfigStorage{History: historyDir}, "conv1")
	if err != nil {
		t.Fatalf("conversationUsageReport: %v", err)
	}
	if report.Scope != usageScopeConversation || report.ConversationID != "conv1" || report.Title != "Reported chat" {
		t.Fatalf("report shell: %+v", report)
	}
	if report.Conversations != 1 || report.Turns != 1 {
		t.Fatalf("conversations=%d turns=%d, want 1/1", report.Conversations, report.Turns)
	}
	if report.CostMicros != 51200 || report.ModelCalls != 2 || report.ToolCalls != 1 || report.MediaImages != 2 {
		t.Fatalf("totals: cost=%d calls=%d tools=%d images=%d", report.CostMicros, report.ModelCalls, report.ToolCalls, report.MediaImages)
	}
	if len(report.Children) != 0 {
		t.Fatalf("conversation report should have no children, got %v", report.Children)
	}
	if len(report.Models) != 2 || len(report.Tools) != 1 {
		t.Fatalf("rows: %d models, %d tools", len(report.Models), len(report.Tools))
	}

	// A bare assistant turn (no run) folds as an assistant turn with nothing else.
	seedUsageConversation(t, historyDir, "conv-legacy", "", "Legacy", []HistoryTurn{
		{SchemaVersion: 1, ID: turnID(1), Kind: "chat", Role: "assistant", CreatedAt: "2026-10-24T08:00:00Z"},
	})
	report, err = conversationUsageReport(ConfigStorage{History: historyDir}, "conv-legacy")
	if err != nil {
		t.Fatalf("legacy report: %v", err)
	}
	if report.Turns != 1 || report.CostMicros != 0 || len(report.Models) != 0 {
		t.Fatalf("legacy totals: turns=%d cost=%d models=%v", report.Turns, report.CostMicros, report.Models)
	}

	if _, err := conversationUsageReport(ConfigStorage{History: historyDir}, "conv-missing"); err == nil {
		t.Fatal("missing conversation should error")
	}
}

func TestConversationUsageReportFoldsEditOperations(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	opTurn := HistoryTurn{
		SchemaVersion: 1,
		ID:            turnID(2),
		CreatedAt:     "2026-10-24T10:00:05Z",
		Kind:          "edit",
		Role:          "assistant",
		Request: map[string]any{
			"operation": EditOperation{
				ID:         "op_1",
				Kind:       editOperationKindReframe,
				Status:     "completed",
				Provider:   "fal",
				Model:      "fal-ai/ltx-2.3/reframe",
				CostMicros: 120000,
			},
		},
	}
	seedUsageConversation(t, historyDir, "conv-edit", "", "Edit session", []HistoryTurn{
		userTurn(turnID(1), "2026-10-24T10:00:00Z"),
		opTurn,
	})
	report, err := conversationUsageReport(ConfigStorage{History: historyDir}, "conv-edit")
	if err != nil {
		t.Fatalf("conversationUsageReport: %v", err)
	}
	if report.CostMicros != 120000 || report.MediaVideos != 1 || report.ToolCalls != 1 {
		t.Fatalf("totals: %+v", report)
	}
	if len(report.Tools) != 1 || report.Tools[0].Name != editOperationKindReframe {
		t.Fatalf("tool rows: %+v", report.Tools)
	}
}

func TestProjectUsageReport(t *testing.T) {
	base := t.TempDir()
	historyDir := filepath.Join(base, "history")
	librariesDir := filepath.Join(base, "libraries")
	storage := ConfigStorage{History: historyDir, Libraries: librariesDir}

	library, err := createLibrary(storage, "Client work")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}
	project, err := createProject(storage, library.ID, "Launch videos")
	if err != nil {
		t.Fatalf("createProject: %v", err)
	}
	expensive := HarnessRun{Steps: []HarnessStep{
		{Kind: "model_call", Provider: "openrouter", Model: "m1", Status: "completed"},
		{Kind: "streaming", Provider: "openrouter", Model: "m1", Status: "completed", Tokens: 20, CostMicros: 1500},
		{Kind: "tool_call", Status: "completed", Tools: []HarnessToolActivity{
			{Name: "generate_video", Status: "completed", Provider: "fal", Model: "fal-ai/kling", MediaKind: "video", MediaCount: 1, CostMicros: 900000},
		}},
	}}
	cheap := HarnessRun{Steps: []HarnessStep{
		{Kind: "triage", Provider: "ollama", Model: "local-model", Status: "completed", Tokens: 5},
	}}
	seedUsageConversation(t, historyDir, "conv-a", project.ID, "Expensive chat", []HistoryTurn{
		userTurn(turnID(1), "2026-10-24T10:00:00Z"),
		assistantRunTurn(turnID(2), "2026-10-24T10:00:05Z", expensive),
	})
	seedUsageConversation(t, historyDir, "conv-b", project.ID, "Local chat", []HistoryTurn{
		assistantRunTurn(turnID(1), "2026-10-24T11:00:00Z", cheap),
	})
	seedUsageConversation(t, historyDir, "conv-standalone", "", "Not in project", []HistoryTurn{
		assistantRunTurn(turnID(1), "2026-10-24T12:00:00Z", HarnessRun{Steps: []HarnessStep{
			{Kind: "triage", Provider: "ollama", Model: "standalone-model", Status: "completed", Tokens: 5},
		}}),
	})

	report, err := projectUsageReport(storage, project.ID)
	if err != nil {
		t.Fatalf("projectUsageReport: %v", err)
	}
	if report.Scope != usageScopeProject || report.Title != "Launch videos" {
		t.Fatalf("report shell: %+v", report)
	}
	if report.Conversations != 2 || report.Turns != 2 || report.CostMicros != 901500 {
		t.Fatalf("totals: conversations=%d turns=%d cost=%d", report.Conversations, report.Turns, report.CostMicros)
	}
	if len(report.Children) != 2 {
		t.Fatalf("children: want 2, got %v", report.Children)
	}
	// Sorted by cost: the expensive conversation first.
	if report.Children[0].ID != "conv-a" || report.Children[0].CostMicros != 901500 {
		t.Fatalf("first child: %+v", report.Children[0])
	}
	if report.Children[1].ID != "conv-b" || report.Children[1].Turns != 1 {
		t.Fatalf("second child: %+v", report.Children[1])
	}
	// The standalone conversation's turns and cost stay out.
	for _, row := range report.Models {
		if row.Model == "standalone-model" {
			t.Fatalf("standalone conversation leaked into project report: %+v", report.Models)
		}
	}
}

func TestLibraryUsageReport(t *testing.T) {
	base := t.TempDir()
	historyDir := filepath.Join(base, "history")
	librariesDir := filepath.Join(base, "libraries")
	storage := ConfigStorage{History: historyDir, Libraries: librariesDir}

	library, err := createLibrary(storage, "Client work")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}
	projectA, err := createProject(storage, library.ID, "Launch")
	if err != nil {
		t.Fatalf("createProject: %v", err)
	}
	projectB, err := createProject(storage, library.ID, "Socials")
	if err != nil {
		t.Fatalf("createProject: %v", err)
	}
	otherLibrary, err := createLibrary(storage, "Personal")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}

	seedUsageConversation(t, historyDir, "conv-a1", projectA.ID, "A1", []HistoryTurn{
		assistantRunTurn(turnID(1), "2026-10-24T10:00:00Z", HarnessRun{Steps: []HarnessStep{
			{Kind: "tool_call", Status: "completed", Tools: []HarnessToolActivity{
				{Name: "generate_video", Status: "completed", Provider: "fal", Model: "fal-ai/kling", MediaKind: "video", MediaCount: 1, CostMicros: 900000},
			}},
		}}),
	})
	seedUsageConversation(t, historyDir, "conv-b1", projectB.ID, "B1", []HistoryTurn{
		assistantRunTurn(turnID(1), "2026-10-24T11:00:00Z", HarnessRun{Steps: []HarnessStep{
			{Kind: "triage", Provider: "openrouter", Model: "m1", Status: "completed", CostMicros: 700},
		}}),
	})
	seedUsageConversation(t, historyDir, "conv-other", otherLibrary.ID, "Other library", []HistoryTurn{
		assistantRunTurn(turnID(1), "2026-10-24T12:00:00Z", HarnessRun{Steps: []HarnessStep{
			{Kind: "triage", Provider: "openrouter", Model: "m2", Status: "completed", CostMicros: 999000},
		}}),
	})
	seedUsageConversation(t, historyDir, "conv-free", projectA.ID, "No usage", []HistoryTurn{
		userTurn(turnID(1), "2026-10-24T09:00:00Z"),
	})

	report, err := libraryUsageReport(storage, library.ID)
	if err != nil {
		t.Fatalf("libraryUsageReport: %v", err)
	}
	if report.Scope != usageScopeLibrary || report.Title != "Client work" {
		t.Fatalf("report shell: %+v", report)
	}
	if report.Conversations != 3 || report.Turns != 2 || report.CostMicros != 900700 {
		t.Fatalf("totals: conversations=%d turns=%d cost=%d", report.Conversations, report.Turns, report.CostMicros)
	}
	// The other library's conversation stays out entirely.
	for _, row := range report.Models {
		if row.Model == "m2" {
			t.Fatalf("other library leaked: %+v", report.Models)
		}
	}
	if len(report.Children) != 2 {
		t.Fatalf("children: want 2, got %v", report.Children)
	}
	if report.Children[0].ID != projectA.ID || report.Children[0].CostMicros != 900000 || report.Children[0].Turns != 1 {
		t.Fatalf("first child: %+v", report.Children[0])
	}
	if report.Children[1].ID != projectB.ID || report.Children[1].CostMicros != 700 {
		t.Fatalf("second child: %+v", report.Children[1])
	}

	// An empty library reports zeroed totals with per-project rows.
	emptyLibrary, err := createLibrary(storage, "Empty")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}
	report, err = libraryUsageReport(storage, emptyLibrary.ID)
	if err != nil {
		t.Fatalf("empty library report: %v", err)
	}
	if report.CostMicros != 0 || report.Turns != 0 || len(report.Children) != 0 {
		t.Fatalf("empty library totals: %+v", report)
	}
}

func TestProjectUsageReportSkipsDeleted(t *testing.T) {
	base := t.TempDir()
	historyDir := filepath.Join(base, "history")
	librariesDir := filepath.Join(base, "libraries")
	storage := ConfigStorage{History: historyDir, Libraries: librariesDir}

	library, err := createLibrary(storage, "Client work")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}
	project, err := createProject(storage, library.ID, "Launch")
	if err != nil {
		t.Fatalf("createProject: %v", err)
	}
	run := HarnessRun{Steps: []HarnessStep{
		{Kind: "triage", Provider: "openrouter", Model: "m1", Status: "completed", CostMicros: 500},
	}}
	seedUsageConversation(t, historyDir, "conv-live", project.ID, "Live", []HistoryTurn{
		assistantRunTurn(turnID(1), "2026-10-24T10:00:00Z", run),
	})
	seedUsageConversation(t, historyDir, "conv-deleted", project.ID, "Deleted", []HistoryTurn{
		assistantRunTurn(turnID(1), "2026-10-24T11:00:00Z", run),
	})
	// Mark the second conversation deleted, the soft-delete shape.
	deletedPath := filepath.Join(historyDir, "conversations", "conv-deleted", "conversation.json")
	var deleted HistoryConversation
	if err := readJSONFile(deletedPath, &deleted); err != nil {
		t.Fatalf("read deleted record: %v", err)
	}
	deleted.DeletedAt = "2026-10-24T12:00:00Z"
	if err := writeJSONFile(deletedPath, deleted); err != nil {
		t.Fatalf("write deleted record: %v", err)
	}

	report, err := projectUsageReport(storage, project.ID)
	if err != nil {
		t.Fatalf("projectUsageReport: %v", err)
	}
	if report.Conversations != 1 || report.Turns != 1 || report.CostMicros != 500 {
		t.Fatalf("deleted conversation counted: %+v", report)
	}

	// The conversation scope refuses a deleted conversation outright.
	if _, err := conversationUsageReport(storage, "conv-deleted"); err == nil {
		t.Fatal("deleted conversation report should error")
	}
}
