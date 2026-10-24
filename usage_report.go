package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The usage report: aggregated cost and tool-call telemetry folded from
// persisted conversation history. Every assistant turn already carries its
// full harness run in ProviderResponse.harnessRun (model-call steps with
// token/cost, tool_call steps with per-call activities) and every edit-session
// operation turn carries its EditOperation in Request.operation — so a report
// at any scope is a pure fold over existing records, with nothing new written.
// The fold mirrors the frontend's per-conversation usage tables
// (summarizeModelUsage/summarizeMediaUsage in App.tsx) so a report for the
// open conversation can never disagree with the live toolbar numbers.

const (
	usageScopeConversation = "conversation"
	usageScopeProject      = "project"
	usageScopeLibrary      = "library"
)

// usageModelCallStepKinds are the run-step kinds that each represent one real
// model invocation: the harness-model calls (triage, skill, planning rounds)
// and the final-response attempt. The streaming step is the body of the same
// final-response call that model_call opened — its tokens and cost fold onto
// the model's row, but it adds no second call. Bookkeeping steps (queued,
// saved, evaluation) are excluded, matching how tokens are summed.
var usageModelCallStepKinds = map[string]bool{
	"triage":     true,
	"skill":      true,
	"planning":   true,
	"model_call": true,
}

// UsageReport is the API-facing aggregated report for one scope. Children
// carries the next level down — per-conversation rows for a project report,
// per-project rows for a library report — so the UI can drill from library →
// project → conversation with the same shape at every stop.
type UsageReport struct {
	Scope          string `json:"scope"`
	ConversationID string `json:"conversationId,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
	LibraryID      string `json:"libraryId,omitempty"`
	Title          string `json:"title"`
	// Conversations counts the records folded into the report (1 for the
	// conversation scope itself).
	Conversations   int             `json:"conversations"`
	Turns           int             `json:"turns"`
	CostMicros      int64           `json:"costMicros"`
	CostUnknown     bool            `json:"costUnknown,omitempty"`
	ModelCalls      int             `json:"modelCalls"`
	ToolCalls       int             `json:"toolCalls"`
	FailedToolCalls int             `json:"failedToolCalls,omitempty"`
	MediaImages     int             `json:"mediaImages,omitempty"`
	MediaVideos     int             `json:"mediaVideos,omitempty"`
	MediaAudios     int             `json:"mediaAudios,omitempty"`
	Models          []UsageModelRow `json:"models"`
	Tools           []UsageToolRow  `json:"tools"`
	Children        []UsageChildRow `json:"children,omitempty"`
}

// UsageModelRow is one row per model that consumed something in scope. Chat
// models fill the token columns (from run steps); media-generation models
// fill the media columns and cost (from tool activities and edit operations).
// The same image model family can run locally or in the cloud, so Provider
// names the backend and joins the row key.
type UsageModelRow struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Calls            int    `json:"calls"`
	PromptTokens     int    `json:"promptTokens,omitempty"`
	CompletionTokens int    `json:"completionTokens,omitempty"`
	MediaImages      int    `json:"mediaImages,omitempty"`
	MediaVideos      int    `json:"mediaVideos,omitempty"`
	MediaAudios      int    `json:"mediaAudios,omitempty"`
	CostMicros       int64  `json:"costMicros,omitempty"`
	CostUnknown      bool   `json:"costUnknown,omitempty"`
}

// UsageToolRow is one row per tool name invoked in scope — harness tools and
// editor operations alike. Failures counts calls whose status was not
// "completed" (denied, failed, timed out) so a scope with burning money and
// nothing to show for it reads as such.
type UsageToolRow struct {
	Name        string `json:"name"`
	Calls       int    `json:"calls"`
	Failures    int    `json:"failures,omitempty"`
	MediaImages int    `json:"mediaImages,omitempty"`
	MediaVideos int    `json:"mediaVideos,omitempty"`
	MediaAudios int    `json:"mediaAudios,omitempty"`
	CostMicros  int64  `json:"costMicros,omitempty"`
	CostUnknown bool   `json:"costUnknown,omitempty"`
}

// UsageChildRow is one breakdown line under a project or library report: the
// next container down with its own rolled-up totals.
type UsageChildRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Turns       int    `json:"turns"`
	ToolCalls   int    `json:"toolCalls"`
	CostMicros  int64  `json:"costMicros"`
	CostUnknown bool   `json:"costUnknown,omitempty"`
}

// usageLedger accumulates the fold. Rows live in maps keyed provider|model
// and tool name, materialized once by rows() in display order.
type usageLedger struct {
	conversations   int
	assistantTurns  int
	costMicros      int64
	costUnknown     bool
	modelCalls      int
	toolCalls       int
	failedToolCalls int
	mediaImages     int
	mediaVideos     int
	mediaAudios     int
	models          map[string]*UsageModelRow
	tools           map[string]*UsageToolRow
}

func newUsageLedger() *usageLedger {
	return &usageLedger{
		models: map[string]*UsageModelRow{},
		tools:  map[string]*UsageToolRow{},
	}
}

func (l *usageLedger) modelRow(provider, model string) *UsageModelRow {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "—"
	}
	key := provider + "|" + model
	row, ok := l.models[key]
	if !ok {
		row = &UsageModelRow{Provider: provider, Model: model}
		l.models[key] = row
	}
	return row
}

func (l *usageLedger) toolRow(name string) *UsageToolRow {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "unknown"
	}
	row, ok := l.tools[name]
	if !ok {
		row = &UsageToolRow{Name: name}
		l.tools[name] = row
	}
	return row
}

// foldRun folds one persisted harness run: model-call steps onto model rows,
// tool activities onto tool rows plus their generation models' media rows.
// Failed model-call steps still count as calls (they were made, and with a
// cloud key often still billed nothing — the cost sum only moves on cost-
// carrying steps); failed tool activities count in the tool row's Failures.
func (l *usageLedger) foldRun(run HarnessRun) {
	for _, step := range run.Steps {
		model := strings.TrimSpace(step.Model)
		// The invocation count: one call per harness-model call (triage,
		// skill selection, each planning round) and per final-response
		// attempt. The streaming step is the body of the same final-response
		// call that model_call opened — no second call.
		if usageModelCallStepKinds[step.Kind] && model != "" {
			row := l.modelRow(step.Provider, model)
			row.Calls++
			l.modelCalls++
		}
		// Token and cost attribution rides every model-carrying pipeline
		// step: the streaming step carries the final response's usage
		// (model_call itself completes before the stream opens), so its cost
		// and tokens fold onto the same model row. Bookkeeping steps (queued,
		// saved, evaluation) carry nothing — a fallback-run's saved step may
		// hold a token count with no model call behind it, excluded exactly
		// like the frontend's fold.
		if (usageModelCallStepKinds[step.Kind] || step.Kind == "streaming") && model != "" {
			row := l.modelRow(step.Provider, model)
			row.PromptTokens += step.PromptTokens
			row.CompletionTokens += step.Tokens
			row.CostMicros += step.CostMicros
			l.costMicros += step.CostMicros
		}
		if step.Kind != "tool_call" {
			continue
		}
		for _, activity := range step.Tools {
			l.foldToolActivity(activity)
		}
	}
}

func (l *usageLedger) foldToolActivity(activity HarnessToolActivity) {
	l.toolCalls++
	row := l.toolRow(activity.Name)
	row.Calls++
	row.CostMicros += activity.CostMicros
	l.costMicros += activity.CostMicros
	if activity.CostUnknown {
		row.CostUnknown = true
		l.costUnknown = true
	}
	if activity.Status != "completed" {
		row.Failures++
		l.failedToolCalls++
	}
	kind := activity.MediaKind
	count := mediaCountForKind(kind, activity.MediaCount)
	if count > 0 {
		addToolMedia(row, kind, count)
		l.addMediaTotal(kind, count)
	}
	// Media attribution onto the model row needs the model the tool actually
	// ran; local CLI tools stamp only a Provider, and failed calls carry
	// neither — both stay off the model table. Cost-bearing activities
	// without a media kind (fal transcription) join too — rows then show a
	// cost with no output counts, keeping the model table reconcilable with
	// the report's money total (the frontend fold's rule).
	if strings.TrimSpace(activity.Model) != "" && (count > 0 || activity.CostMicros > 0 || activity.CostUnknown) {
		model := l.modelRow(activity.Provider, activity.Model)
		model.Calls++
		if count > 0 {
			addToolMediaModel(model, kind, count)
		}
		model.CostMicros += activity.CostMicros
		if activity.CostUnknown {
			model.CostUnknown = true
		}
	}
}

// foldEditOperation folds one editor operation (Request.operation on an edit
// session's op turn): its cost lands beside harness tool calls so a scope's
// total covers the editors too. Local operations (crop, trim) carry a Backend
// and no provider/model, so they bill zero and skip the model table exactly
// like the local CLI tools do.
func (l *usageLedger) foldEditOperation(op EditOperation) {
	l.toolCalls++
	row := l.toolRow(op.Kind)
	row.Calls++
	row.CostMicros += op.CostMicros
	l.costMicros += op.CostMicros
	if op.CostUnknown {
		row.CostUnknown = true
		l.costUnknown = true
	}
	if op.Status != "completed" {
		row.Failures++
		l.failedToolCalls++
		return
	}
	kind := editResultMediaKind(op)
	count := mediaCountForKind(kind, 1)
	if count > 0 {
		addToolMedia(row, kind, count)
		l.addMediaTotal(kind, count)
	}
	if strings.TrimSpace(op.Model) != "" && (count > 0 || op.CostMicros > 0 || op.CostUnknown) {
		model := l.modelRow(op.Provider, op.Model)
		model.Calls++
		if count > 0 {
			addToolMediaModel(model, kind, count)
		}
		model.CostMicros += op.CostMicros
		if op.CostUnknown {
			model.CostUnknown = true
		}
	}
}

func (l *usageLedger) addMediaTotal(kind string, count int) {
	switch kind {
	case "image":
		l.mediaImages += count
	case "video":
		l.mediaVideos += count
	case "audio":
		l.mediaAudios += count
	}
}

// merge folds another ledger into this one — the project/library roll-up over
// per-conversation folds, conversations included: the child fold already
// counted its own record and skipped soft-deleted ones.
func (l *usageLedger) merge(other *usageLedger) {
	l.conversations += other.conversations
	l.assistantTurns += other.assistantTurns
	l.costMicros += other.costMicros
	l.costUnknown = l.costUnknown || other.costUnknown
	l.modelCalls += other.modelCalls
	l.toolCalls += other.toolCalls
	l.failedToolCalls += other.failedToolCalls
	l.mediaImages += other.mediaImages
	l.mediaVideos += other.mediaVideos
	l.mediaAudios += other.mediaAudios
	for key, row := range other.models {
		existing, ok := l.models[key]
		if !ok {
			copied := *row
			l.models[key] = &copied
			continue
		}
		existing.Calls += row.Calls
		existing.PromptTokens += row.PromptTokens
		existing.CompletionTokens += row.CompletionTokens
		existing.MediaImages += row.MediaImages
		existing.MediaVideos += row.MediaVideos
		existing.MediaAudios += row.MediaAudios
		existing.CostMicros += row.CostMicros
		existing.CostUnknown = existing.CostUnknown || row.CostUnknown
	}
	for name, row := range other.tools {
		existing, ok := l.tools[name]
		if !ok {
			copied := *row
			l.tools[name] = &copied
			continue
		}
		existing.Calls += row.Calls
		existing.Failures += row.Failures
		existing.MediaImages += row.MediaImages
		existing.MediaVideos += row.MediaVideos
		existing.MediaAudios += row.MediaAudios
		existing.CostMicros += row.CostMicros
		existing.CostUnknown = existing.CostUnknown || row.CostUnknown
	}
}

// rows materializes the model and tool tables in display order: models by
// cost (then activity, then name), tools by call count (then name).
func (l *usageLedger) rows() ([]UsageModelRow, []UsageToolRow) {
	models := make([]UsageModelRow, 0, len(l.models))
	for _, row := range l.models {
		models = append(models, *row)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].CostMicros != models[j].CostMicros {
			return models[i].CostMicros > models[j].CostMicros
		}
		if models[i].Calls != models[j].Calls {
			return models[i].Calls > models[j].Calls
		}
		ki := strings.ToLower(models[i].Provider + "|" + models[i].Model)
		kj := strings.ToLower(models[j].Provider + "|" + models[j].Model)
		return ki < kj
	})
	tools := make([]UsageToolRow, 0, len(l.tools))
	for _, row := range l.tools {
		tools = append(tools, *row)
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Calls != tools[j].Calls {
			return tools[i].Calls > tools[j].Calls
		}
		return strings.ToLower(tools[i].Name) < strings.ToLower(tools[j].Name)
	})
	return models, tools
}

func mediaCountForKind(kind string, count int) int {
	switch kind {
	case "image", "video", "audio":
		if count > 0 {
			return count
		}
	}
	return 0
}

func addToolMedia(row *UsageToolRow, kind string, count int) {
	switch kind {
	case "image":
		row.MediaImages += count
	case "video":
		row.MediaVideos += count
	case "audio":
		row.MediaAudios += count
	}
}

func addToolMediaModel(row *UsageModelRow, kind string, count int) {
	switch kind {
	case "image":
		row.MediaImages += count
	case "video":
		row.MediaVideos += count
	case "audio":
		row.MediaAudios += count
	}
}

// harnessRunFromProviderResponse decodes the persisted run out of a turn's
// ProviderResponse map (JSON round-trip: the map holds arbitrary decoded
// JSON). Absent or undecodable runs fold as nothing — a legacy turn's totals
// still ride its top-level costMicros, which the run fold duplicates exactly.
func harnessRunFromProviderResponse(providerResponse map[string]any) (HarnessRun, bool) {
	value, ok := providerResponse["harnessRun"]
	if !ok || value == nil {
		return HarnessRun{}, false
	}
	return decodeJSONValue[HarnessRun](value)
}

func editOperationFromRequest(request map[string]any) (EditOperation, bool) {
	if request == nil {
		return EditOperation{}, false
	}
	value, ok := request["operation"]
	if !ok || value == nil {
		return EditOperation{}, false
	}
	return decodeJSONValue[EditOperation](value)
}

func decodeJSONValue[T any](value any) (T, bool) {
	var out T
	raw, err := json.Marshal(value)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, false
	}
	return out, true
}

// foldConversationUsageAt folds one conversation directory's turns into the
// ledger and returns the conversation record (for child-row titles). A
// soft-deleted record folds as nothing — the scope walkers pre-filter, this
// is the belt-and-suspenders re-check. A corrupt turn file aborts the fold:
// the report must not quietly undercount money.
func foldConversationUsageAt(conversationPath string, ledger *usageLedger) (HistoryConversation, error) {
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return HistoryConversation{}, err
	}
	if conversation.DeletedAt != "" {
		return conversation, nil
	}
	ledger.conversations++
	turnsDir := filepath.Join(filepath.Dir(conversationPath), "turns")
	err := filepath.WalkDir(turnsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		var turn HistoryTurn
		if err := readJSONFile(path, &turn); err != nil {
			return err
		}
		if turn.DeletedAt != "" {
			return nil
		}
		if turn.Role != "assistant" {
			return nil
		}
		ledger.assistantTurns++
		if run, ok := harnessRunFromProviderResponse(turn.ProviderResponse); ok {
			ledger.foldRun(run)
		}
		if op, ok := editOperationFromRequest(turn.Request); ok {
			ledger.foldEditOperation(op)
		}
		return nil
	})
	if err != nil {
		return HistoryConversation{}, err
	}
	return conversation, nil
}

// conversationUsageReport folds one conversation (chat or edit session) by ID.
func conversationUsageReport(storage ConfigStorage, conversationID string) (UsageReport, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return UsageReport{}, err
	}
	ledger := newUsageLedger()
	conversation, err := foldConversationUsageAt(conversationPath, ledger)
	if err != nil {
		return UsageReport{}, err
	}
	if conversation.DeletedAt != "" {
		return UsageReport{}, fmt.Errorf("conversation %s is deleted", conversationID)
	}
	return finishUsageReport(UsageReport{
		Scope:          usageScopeConversation,
		ConversationID: conversation.ID,
		Title:          conversation.Title,
	}, ledger), nil
}

// projectUsageReport folds every conversation in a project, with per-
// conversation child rows so the report drills down one level.
func projectUsageReport(storage ConfigStorage, projectID string) (UsageReport, error) {
	project, _, err := findProject(storage, projectID)
	if err != nil {
		return UsageReport{}, err
	}
	ledger := newUsageLedger()
	children := []UsageChildRow{}
	err = forEachProjectConversationRecord(storage, map[string]bool{projectID: true}, false, func(conversationPath string, _ HistoryConversation) error {
		childLedger := newUsageLedger()
		conversation, err := foldConversationUsageAt(conversationPath, childLedger)
		if err != nil {
			return err
		}
		ledger.merge(childLedger)
		if childLedger.conversations == 0 {
			return nil
		}
		children = append(children, UsageChildRow{
			ID:          conversation.ID,
			Title:       conversation.Title,
			Turns:       childLedger.assistantTurns,
			ToolCalls:   childLedger.toolCalls,
			CostMicros:  childLedger.costMicros,
			CostUnknown: childLedger.costUnknown,
		})
		return nil
	})
	if err != nil {
		return UsageReport{}, err
	}
	report := finishUsageReport(UsageReport{
		Scope:     usageScopeProject,
		ProjectID: project.ID,
		Title:     project.Name,
	}, ledger)
	sortUsageChildren(children)
	report.Children = children
	return report, nil
}

// libraryUsageReport folds every conversation under the library's projects,
// with per-project child rows. Each conversation folds into its own project's
// ledger first, so the child rows and the top-level totals come from one walk.
func libraryUsageReport(storage ConfigStorage, libraryID string) (UsageReport, error) {
	library, err := findLibrary(storage, libraryID)
	if err != nil {
		return UsageReport{}, err
	}
	projects, err := listProjectRecords(storage, libraryID)
	if err != nil {
		return UsageReport{}, err
	}
	projectIDs := make(map[string]bool, len(projects))
	projectNames := make(map[string]string, len(projects))
	for _, project := range projects {
		projectIDs[project.ID] = true
		projectNames[project.ID] = project.Name
	}
	ledger := newUsageLedger()
	projectLedgers := make(map[string]*usageLedger, len(projects))
	err = forEachProjectConversationRecord(storage, projectIDs, false, func(conversationPath string, conversation HistoryConversation) error {
		projectLedger, ok := projectLedgers[conversation.ProjectID]
		if !ok {
			projectLedger = newUsageLedger()
			projectLedgers[conversation.ProjectID] = projectLedger
		}
		if _, err := foldConversationUsageAt(conversationPath, projectLedger); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return UsageReport{}, err
	}
	children := []UsageChildRow{}
	for _, project := range projects {
		projectLedger := projectLedgers[project.ID]
		if projectLedger == nil {
			projectLedger = newUsageLedger()
		}
		ledger.merge(projectLedger)
		children = append(children, UsageChildRow{
			ID:          project.ID,
			Title:       project.Name,
			Turns:       projectLedger.assistantTurns,
			ToolCalls:   projectLedger.toolCalls,
			CostMicros:  projectLedger.costMicros,
			CostUnknown: projectLedger.costUnknown,
		})
	}
	report := finishUsageReport(UsageReport{
		Scope:     usageScopeLibrary,
		LibraryID: libraryID,
		Title:     library.Name,
	}, ledger)
	sortUsageChildren(children)
	report.Children = children
	return report, nil
}

// finishUsageReport stamps the folded counters and materialized rows onto the
// scope-shaped shell.
func finishUsageReport(report UsageReport, ledger *usageLedger) UsageReport {
	report.Conversations = ledger.conversations
	report.Turns = ledger.assistantTurns
	report.CostMicros = ledger.costMicros
	report.CostUnknown = ledger.costUnknown
	report.ModelCalls = ledger.modelCalls
	report.ToolCalls = ledger.toolCalls
	report.FailedToolCalls = ledger.failedToolCalls
	report.MediaImages = ledger.mediaImages
	report.MediaVideos = ledger.mediaVideos
	report.MediaAudios = ledger.mediaAudios
	report.Models, report.Tools = ledger.rows()
	return report
}

func sortUsageChildren(children []UsageChildRow) {
	sort.Slice(children, func(i, j int) bool {
		if children[i].CostMicros != children[j].CostMicros {
			return children[i].CostMicros > children[j].CostMicros
		}
		return strings.ToLower(children[i].Title) < strings.ToLower(children[j].Title)
	})
}

// GetConversationUsageReport reports one conversation's cost and tool-call
// totals, folded from its persisted turns. Read-only: no streaming guard.
func (a *App) GetConversationUsageReport(conversationID string) (UsageReport, error) {
	config, err := loadAppConfig()
	if err != nil {
		return UsageReport{}, err
	}
	return conversationUsageReport(config.Storage, conversationID)
}

// GetProjectUsageReport reports one project's totals with per-conversation
// child rows.
func (a *App) GetProjectUsageReport(projectID string) (UsageReport, error) {
	config, err := loadAppConfig()
	if err != nil {
		return UsageReport{}, err
	}
	return projectUsageReport(config.Storage, projectID)
}

// GetLibraryUsageReport reports one library's totals with per-project child
// rows.
func (a *App) GetLibraryUsageReport(libraryID string) (UsageReport, error) {
	config, err := loadAppConfig()
	if err != nil {
		return UsageReport{}, err
	}
	return libraryUsageReport(config.Storage, libraryID)
}
