package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atelier/internal/document"
)

var usageExportNow = time.Date(2026, 11, 10, 14, 3, 0, 0, time.UTC)

func seedUsageExportProject(t *testing.T) (ConfigStorage, ProjectSummary) {
	t.Helper()
	base := t.TempDir()
	storage := ConfigStorage{History: filepath.Join(base, "history"), Libraries: filepath.Join(base, "libraries")}
	library, err := createLibrary(storage, "Client work")
	if err != nil {
		t.Fatalf("createLibrary: %v", err)
	}
	project, err := createProject(storage, library.ID, "Launch videos")
	if err != nil {
		t.Fatalf("createProject: %v", err)
	}
	seedUsageConversation(t, storage.History, "conv-a", project.ID, "Expensive chat", []HistoryTurn{
		userTurn(turnID(1), "2026-10-24T10:00:00Z"),
		assistantRunTurn(turnID(2), "2026-10-24T10:00:05Z", HarnessRun{Steps: []HarnessStep{
			{Kind: "model_call", Provider: "openrouter", Model: "anthropic/claude-sonnet", Status: "completed"},
			{Kind: "streaming", Provider: "openrouter", Model: "anthropic/claude-sonnet", Status: "completed", PromptTokens: 12345, Tokens: 678, CostMicros: 3000},
			{Kind: "tool_call", Status: "completed", Tools: []HarnessToolActivity{
				{Name: "generate_video", Status: "completed", Provider: "fal", Model: "fal-ai/kling", MediaKind: "video", MediaCount: 1, CostMicros: 900000},
				{Name: "generate_image", Status: "completed", Provider: "replicate", Model: "black-forest-labs/flux", MediaKind: "image", MediaCount: 2, CostUnknown: true},
				{Name: "generate_image", Status: "failed"},
			}},
		}}),
	})
	return storage, project
}

func TestUsageReportForScope(t *testing.T) {
	storage, project := seedUsageExportProject(t)
	report, err := usageReportForScope(storage, usageScopeProject, project.ID)
	if err != nil {
		t.Fatalf("usageReportForScope: %v", err)
	}
	if report.Scope != usageScopeProject || report.ProjectID != project.ID || report.CostMicros != 903000 {
		t.Fatalf("project fold: %+v", report)
	}
	conversation, err := usageReportForScope(storage, usageScopeConversation, "conv-a")
	if err != nil || conversation.ConversationID != "conv-a" {
		t.Fatalf("conversation fold: %+v, %v", conversation, err)
	}
	if _, err := usageReportForScope(storage, "galaxy", "x"); err == nil {
		t.Fatal("an unknown scope must be refused")
	}
	if _, err := usageReportForScope(storage, usageScopeProject, " "); err == nil {
		t.Fatal("an empty id must be refused")
	}
}

func TestRenderUsageReportExportJSON(t *testing.T) {
	storage, project := seedUsageExportProject(t)
	report, err := projectUsageReport(storage, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	export, err := renderUsageReportExport(report, "json", usageExportNow, mustPaper(t))
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	if export.Extension != ".json" || export.Pattern != "*.json" {
		t.Fatalf("export shape: %+v", export)
	}
	var decoded UsageReportExport
	if err := json.Unmarshal(export.Data, &decoded); err != nil {
		t.Fatalf("the export must be valid JSON: %v", err)
	}
	if decoded.Format != usageReportExportFormat || decoded.FormatVersion != currentUsageReportExportFormatVersion || decoded.CostUnit != "usd_micros" {
		t.Fatalf("envelope: %+v", decoded)
	}
	if decoded.ExportedAt != "2026-11-10T14:03:00Z" {
		t.Fatalf("exportedAt = %q", decoded.ExportedAt)
	}
	// The report rides verbatim: exact micros, rows, children.
	if decoded.Report.CostMicros != 903000 || !decoded.Report.CostUnknown || len(decoded.Report.Children) != 1 || len(decoded.Report.Models) != len(report.Models) {
		t.Fatalf("report payload: %+v", decoded.Report)
	}

	// An empty fold still exports its tables as arrays.
	empty, err := renderUsageReportExport(UsageReport{Scope: usageScopeConversation}, "json", usageExportNow, mustPaper(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty.Data), `"models": []`) || !strings.Contains(string(empty.Data), `"tools": []`) {
		t.Fatalf("empty tables must export as arrays:\n%s", empty.Data)
	}
}

func TestRenderUsageReportExportPDF(t *testing.T) {
	storage, project := seedUsageExportProject(t)
	report, err := projectUsageReport(storage, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	export, err := renderUsageReportExport(report, "pdf", usageExportNow, mustPaper(t))
	if err != nil {
		t.Fatalf("render pdf: %v", err)
	}
	content := string(export.Data)
	if export.Extension != ".pdf" || !strings.HasPrefix(content, "%PDF-1.4\n") {
		t.Fatalf("export shape: %+v", export.Extension)
	}
	for _, want := range []string{
		"(Project usage report -- Launch videos) Tj",
		"(~$0.903) Tj", // the summary total carries the unknown-cost mark
		"(Cost by model) Tj",
		"(Tool calls) Tj",
		"(Conversations) Tj",
		"Expensive chat",
		"anthropic/claude-sonnet",
		"12.3k",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("the PDF must print %q", want)
		}
	}

	if _, err := renderUsageReportExport(report, "csv", usageExportNow, mustPaper(t)); err == nil {
		t.Fatal("an unknown format must be refused")
	}
}

func TestUsageReportDocumentMirrorsTheScreen(t *testing.T) {
	storage, project := seedUsageExportProject(t)
	report, err := projectUsageReport(storage, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc := usageReportDocument(report, usageExportNow)
	if doc.Title != "Project usage report — Launch videos" {
		t.Fatalf("title = %q", doc.Title)
	}
	facts := map[string]string{}
	for _, fact := range doc.Sections[0].Facts {
		facts[fact.Label] = fact.Value
	}
	if facts["Cost"] != "~$0.903" || facts["Tool calls"] != "3 (1 failed)" || facts["Chats"] != "1" || facts["Media"] != "1 video · 2 images" {
		t.Fatalf("summary facts: %v", facts)
	}
	var models *document.ReportTable
	for _, section := range doc.Sections {
		if section.Heading == "Cost by model" {
			models = section.Table
		}
	}
	if models == nil {
		t.Fatal("missing the model table")
	}
	costs := map[string]string{}
	for _, row := range models.Rows {
		costs[strings.SplitN(row[0], "\n", 2)[0]] = row[5]
	}
	// An unpriceable model with no reported cost prints "?", a priced one its
	// dollars at the screen's magnitude-scaled precision.
	if costs["black-forest-labs/flux"] != "?" || costs["fal-ai/kling"] != "$0.90" || costs["anthropic/claude-sonnet"] != "$0.003" {
		t.Fatalf("model costs: %v", costs)
	}
}

func TestUsageCostLabels(t *testing.T) {
	cases := []struct {
		micros  int64
		unknown bool
		total   string
		row     string
	}{
		{0, false, "$0.00", "—"},
		{0, true, "?", "?"},
		{4000, false, "$0.004", "$0.004"},
		{960000, false, "$0.96", "$0.96"},
		{25000, true, "~$0.025", "~$0.025"},
		{1234567, false, "$1.23", "$1.23"},
	}
	for _, c := range cases {
		if got := usageTotalCostLabel(c.micros, c.unknown); got != c.total {
			t.Errorf("usageTotalCostLabel(%d, %v) = %q, want %q", c.micros, c.unknown, got, c.total)
		}
		if got := usageRowCostLabel(c.micros, c.unknown); got != c.row {
			t.Errorf("usageRowCostLabel(%d, %v) = %q, want %q", c.micros, c.unknown, got, c.row)
		}
	}
	if got := usageMediaLabel(1, 2, 1); got != "1 video · 2 audio clips · 1 image" {
		t.Errorf("usageMediaLabel = %q", got)
	}
	if got := usageReportExportName(UsageReport{Title: "Launch videos"}, usageExportNow); got != "Launch videos usage report 2026-11-10" {
		t.Errorf("usageReportExportName = %q", got)
	}
}

func mustPaper(t *testing.T) document.Paper {
	t.Helper()
	paper, err := document.PaperFor("letter")
	if err != nil {
		t.Fatal(err)
	}
	return paper
}
