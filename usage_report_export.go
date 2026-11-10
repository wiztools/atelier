package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"atelier/internal/document"
)

// The usage report's exports — a PDF to hand someone and a JSON document for
// whoever wants the numbers. Both re-fold the scope from persisted history at
// export time (the report screen's own rule: nothing is cached), so the file
// reflects the history as it stands when the user clicks, through the same
// fold the screen renders. The PDF prints what the screen shows — summary,
// cost by model, tool calls, the per-child breakdown — on the region's office
// paper (document.DefaultPaperSize) through the documents' page writer
// (internal/document/report_export.go). The JSON carries the UsageReport
// verbatim inside a small versioned envelope: costs stay integer USD
// millionths (costMicros) so a consumer can sum them exactly.

const (
	usageReportExportFormat               = "atelier-usage-report"
	currentUsageReportExportFormatVersion = 1
)

// SaveUsageReportExportRequest names the scope to export — the report
// screen's scope plus its conversation, project, or library id — and the
// format, "pdf" or "json".
type SaveUsageReportExportRequest struct {
	Scope  string `json:"scope"`
	ID     string `json:"id"`
	Format string `json:"format"`
}

// UsageReportExport is the JSON export's envelope. Bump the format version
// when a field's meaning changes; added fields are additive.
type UsageReportExport struct {
	Format        string      `json:"format"`
	FormatVersion int         `json:"formatVersion"`
	AppVersion    string      `json:"appVersion"`
	ExportedAt    string      `json:"exportedAt"`
	CostUnit      string      `json:"costUnit"`
	Report        UsageReport `json:"report"`
}

// SaveUsageReportExport folds the scope fresh, renders it, and opens the save
// dialog. Returns the chosen path, or "" when the dialog was cancelled.
// Read-only over history: no streaming guard, like the report bindings.
func (a *App) SaveUsageReportExport(req SaveUsageReportExportRequest) (string, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "pdf" && format != "json" {
		return "", fmt.Errorf(`usage report export format must be "pdf" or "json", got %q`, req.Format)
	}
	config, err := loadAppConfig()
	if err != nil {
		return "", err
	}
	report, err := usageReportForScope(config.Storage, req.Scope, req.ID)
	if err != nil {
		return "", err
	}
	paper, err := document.PaperFor(document.DefaultPaperSize())
	if err != nil {
		return "", err
	}
	now := time.Now()
	export, err := renderUsageReportExport(report, format, now, paper)
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(export.Data, usageReportExportName(report, now), "atelier-usage-report", export.Extension, "Export usage report", []runtime.FileFilter{
		{DisplayName: export.FilterName, Pattern: export.Pattern},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// usageReportForScope dispatches to the scope's fold — the export's twin of
// the three Get*UsageReport bindings.
func usageReportForScope(storage ConfigStorage, scope, id string) (UsageReport, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return UsageReport{}, fmt.Errorf("usage report export needs a %s id", scope)
	}
	switch strings.TrimSpace(scope) {
	case usageScopeConversation:
		return conversationUsageReport(storage, id)
	case usageScopeProject:
		return projectUsageReport(storage, id)
	case usageScopeLibrary:
		return libraryUsageReport(storage, id)
	}
	return UsageReport{}, fmt.Errorf(`usage report scope must be %q, %q, or %q, got %q`, usageScopeConversation, usageScopeProject, usageScopeLibrary, scope)
}

// renderUsageReportExport is the save dialog's testable half.
func renderUsageReportExport(report UsageReport, format string, now time.Time, paper document.Paper) (document.Export, error) {
	switch format {
	case "pdf":
		data := document.RenderReportPDF(usageReportDocument(report, now), paper)
		return document.Export{Data: data, Extension: ".pdf", FilterName: "PDF Files", Pattern: "*.pdf"}, nil
	case "json":
		data, err := json.MarshalIndent(UsageReportExport{
			Format:        usageReportExportFormat,
			FormatVersion: currentUsageReportExportFormatVersion,
			AppVersion:    strings.TrimSpace(version),
			ExportedAt:    now.Format(time.RFC3339),
			CostUnit:      "usd_micros",
			Report:        usageReportForJSON(report),
		}, "", "  ")
		if err != nil {
			return document.Export{}, err
		}
		return document.Export{Data: append(data, '\n'), Extension: ".json", FilterName: "JSON Files", Pattern: "*.json"}, nil
	}
	return document.Export{}, fmt.Errorf(`usage report export format must be "pdf" or "json", got %q`, format)
}

// usageReportForJSON normalizes nil tables to empty arrays, so a consumer
// always finds "models" and "tools" as arrays (an empty fold has none).
func usageReportForJSON(report UsageReport) UsageReport {
	if report.Models == nil {
		report.Models = []UsageModelRow{}
	}
	if report.Tools == nil {
		report.Tools = []UsageToolRow{}
	}
	return report
}

// usageReportExportName is the save dialog's suggested file name:
// "<Title> usage report <date>", sanitized by saveArtifactDialog's guard.
func usageReportExportName(report UsageReport, now time.Time) string {
	title := strings.TrimSpace(report.Title)
	if title == "" {
		title = "Atelier"
	}
	return fmt.Sprintf("%s usage report %s", title, now.Format("2006-01-02"))
}

func usageScopeNoun(scope string) string {
	switch scope {
	case usageScopeLibrary:
		return "Library"
	case usageScopeProject:
		return "Project"
	}
	return "Conversation"
}

// usageReportDocument lays the report out the way the report screen shows
// it: the summary stats, "Cost by model", "Tool calls", and the child
// breakdown, with the screen's cost marks — "~" on a total that includes
// unpriceable media, "?" on a row whose only cost was unpriceable.
func usageReportDocument(report UsageReport, now time.Time) document.Report {
	noun := usageScopeNoun(report.Scope)
	title := noun + " usage report"
	if t := strings.TrimSpace(report.Title); t != "" {
		title += " — " + t
	}
	exported := "Exported " + now.Format("2 January 2006, 15:04 MST")
	if v := strings.TrimSpace(version); v != "" {
		exported += " · Atelier " + v
	}
	doc := document.Report{Title: title, Subtitle: []string{exported}}

	failed := ""
	if report.FailedToolCalls > 0 {
		failed = fmt.Sprintf(" (%d failed)", report.FailedToolCalls)
	}
	facts := []document.ReportFact{
		{Label: "Cost", Value: usageTotalCostLabel(report.CostMicros, report.CostUnknown)},
		// The screen's "Model calls" explains itself in a tooltip; on paper
		// the label must say it counts chat-model invocations only, or it
		// reads as a mistake beside the model table's Calls column (which
		// also counts media generations).
		{Label: "Chat model calls", Value: fmt.Sprint(report.ModelCalls)},
		{Label: "Tool calls", Value: fmt.Sprintf("%d%s", report.ToolCalls, failed)},
		{Label: "Turns", Value: fmt.Sprint(report.Turns)},
	}
	if report.Scope != usageScopeConversation {
		facts = append(facts, document.ReportFact{Label: "Chats", Value: fmt.Sprint(report.Conversations)})
	}
	if media := usageMediaLabel(report.MediaVideos, report.MediaAudios, report.MediaImages); media != "" {
		facts = append(facts, document.ReportFact{Label: "Media", Value: media})
	}
	doc.Sections = append(doc.Sections, document.ReportSection{Heading: "Summary", Facts: facts})

	if len(report.Models) > 0 {
		table := &document.ReportTable{Columns: []document.ReportColumn{
			{Header: "Model"},
			{Header: "Calls", Right: true},
			{Header: "Tokens in", Right: true},
			{Header: "Tokens out", Right: true},
			{Header: "Media"},
			{Header: "Cost", Decimal: true},
		}}
		for _, row := range report.Models {
			name := row.Model
			if row.Provider != "" && row.Provider != "—" {
				name += "\n" + row.Provider
			}
			tokensIn, tokensOut := "—", "—"
			if row.PromptTokens > 0 || row.CompletionTokens > 0 {
				tokensIn, tokensOut = usageTokenLabel(row.PromptTokens), usageTokenLabel(row.CompletionTokens)
			}
			table.Rows = append(table.Rows, []string{
				name,
				fmt.Sprint(row.Calls),
				tokensIn,
				tokensOut,
				orDash(usageMediaLabel(row.MediaVideos, row.MediaAudios, row.MediaImages)),
				usageRowCostLabel(row.CostMicros, row.CostUnknown),
			})
		}
		doc.Sections = append(doc.Sections, document.ReportSection{Heading: "Cost by model", Table: table})
	}

	if len(report.Tools) > 0 {
		table := &document.ReportTable{Columns: []document.ReportColumn{
			{Header: "Tool"},
			{Header: "Calls", Right: true},
			{Header: "Failed", Right: true},
			{Header: "Media produced"},
			{Header: "Cost", Decimal: true},
		}}
		for _, row := range report.Tools {
			failures := "—"
			if row.Failures > 0 {
				failures = fmt.Sprint(row.Failures)
			}
			table.Rows = append(table.Rows, []string{
				row.Name,
				fmt.Sprint(row.Calls),
				failures,
				orDash(usageMediaLabel(row.MediaVideos, row.MediaAudios, row.MediaImages)),
				usageRowCostLabel(row.CostMicros, row.CostUnknown),
			})
		}
		doc.Sections = append(doc.Sections, document.ReportSection{Heading: "Tool calls", Table: table})
	}

	if len(report.Children) > 0 {
		heading, column := "Conversations", "Chat"
		if report.Scope == usageScopeLibrary {
			heading, column = "Projects", "Project"
		}
		table := &document.ReportTable{Columns: []document.ReportColumn{
			{Header: column},
			{Header: "Turns", Right: true},
			{Header: "Tool calls", Right: true},
			{Header: "Cost", Decimal: true},
		}}
		for _, child := range report.Children {
			name := strings.TrimSpace(child.Title)
			if name == "" {
				name = child.ID
			}
			cost := usageCostMicrosLabel(child.CostMicros)
			if cost == "" {
				cost = "—"
			}
			if child.CostUnknown {
				cost = "~" + cost
			}
			table.Rows = append(table.Rows, []string{name, fmt.Sprint(child.Turns), fmt.Sprint(child.ToolCalls), cost})
		}
		doc.Sections = append(doc.Sections, document.ReportSection{Heading: heading, Table: table})
	}

	if len(report.Models) == 0 && len(report.Tools) == 0 {
		doc.Sections = append(doc.Sections, document.ReportSection{
			Notes: []string{fmt.Sprintf("No usage recorded in this %s yet.", strings.ToLower(noun))},
		})
	}
	doc.Footer = []string{
		"Totals cover every chat model call, media generation, and editor operation in the " + strings.ToLower(noun) + "'s saved history. " +
			"Media costs are estimates from each provider's own pricing. \"~\" marks a figure that includes media whose cost could not be determined; \"?\" marks one with no determinable cost at all.",
	}
	return doc
}

// usageCostMicrosLabel follows the frontend's formatCostMicros — dollars
// with decimals scaled to the magnitude, so a $0.004 turn does not print
// "$0.00" — then drops trailing zeros past the cents: on paper "$0.960"
// claims a precision the figure doesn't have. Empty for nothing.
func usageCostMicrosLabel(micros int64) string {
	if micros <= 0 {
		return ""
	}
	dollars := float64(micros) / 1e6
	decimals := 5
	switch {
	case dollars >= 1:
		decimals = 2
	case dollars >= 0.01:
		decimals = 3
	}
	label := fmt.Sprintf("$%.*f", decimals, dollars)
	for strings.HasSuffix(label, "0") && len(label)-strings.Index(label, ".")-1 > 2 {
		label = label[:len(label)-1]
	}
	return label
}

// usageTotalCostLabel is the summary's total: "~" when unpriceable media is
// in it. A known-free scope prints "$0.00" — a report handed to someone else
// should say free, not "?".
func usageTotalCostLabel(micros int64, unknown bool) string {
	label := usageCostMicrosLabel(micros)
	switch {
	case label == "" && unknown:
		return "?"
	case label == "":
		return "$0.00"
	case unknown:
		return "~" + label
	}
	return label
}

// usageRowCostLabel is a table row's cost: "?" when the only cost was
// unpriceable, "—" for none, and — beyond the screen, which leaves the mark
// to totals — "~" on a priced row that also carries unpriceable media, so
// the page's footnote holds for every figure on it.
func usageRowCostLabel(micros int64, unknown bool) string {
	label := usageCostMicrosLabel(micros)
	switch {
	case label == "" && unknown:
		return "?"
	case label == "":
		return "—"
	case unknown:
		return "~" + label
	}
	return label
}

// usageTokenLabel mirrors the frontend's formatTokenCount.
func usageTokenLabel(count int) string {
	if count >= 1000 {
		return fmt.Sprintf("%.1fk", float64(count)/1000)
	}
	return fmt.Sprint(count)
}

// usageMediaLabel mirrors the frontend's mediaCountsLabel.
func usageMediaLabel(videos, audios, images int) string {
	parts := []string{}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	if videos > 0 {
		parts = append(parts, plural(videos, "video", "videos"))
	}
	if audios > 0 {
		parts = append(parts, plural(audios, "audio clip", "audio clips"))
	}
	if images > 0 {
		parts = append(parts, plural(images, "image", "images"))
	}
	return strings.Join(parts, " · ")
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}
