package main

// Comparing two documents — the compare_documents tool (the AI tier: a chat
// ask like "compare @md_a and @md_b") and the ResolveDocumentCompare binding
// the compare workspace loads through. Both read two markdown artifacts (.md
// or .fountain — the document editor's two dialects) and diff them with
// document.BuildDiff; nothing here writes to the conversation. The workspace
// is read-only, so unlike the editors there is no save path, no streaming
// guard, and nothing to discard.
//
// The tool is the first path that hands document TEXT to the chat model:
// its evidence carries a capped excerpt of the changed lines (plus counts and
// the changed scenes or headings) so the reply can say what changed. The cap
// keeps a long revision from flooding context — the full comparison is what
// the user is looking at.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"atelier/internal/document"
)

// documentCompareLaunchKind is the atelier:editor-launch kind the compare
// workspace opens on; its [before, after] pair rides SeedArtifactIDs.
const documentCompareLaunchKind = "compare"

// The evidence excerpt's caps: changed lines shown, runes per line, and a
// total byte budget that keeps the whole tool result inside
// toolResultMessageMaxChars (an excerpt cut mid-JSON would read as garbage).
const (
	documentCompareExcerptLines     = 40
	documentCompareExcerptLineRunes = 160
	documentCompareExcerptChars     = 4 * 1024
	documentCompareMaxSections      = 20
)

// DocumentCompareRequest names the two documents a compare workspace shows —
// both artifacts of one conversation (a document @-mentioned from another
// conversation rides this one's user turn under its original id) — and
// whether whitespace-only differences count.
type DocumentCompareRequest struct {
	ConversationID   string `json:"conversationId"`
	BeforeArtifactID string `json:"beforeArtifactId"`
	AfterArtifactID  string `json:"afterArtifactId"`
	IgnoreWhitespace bool   `json:"ignoreWhitespace,omitempty"`
}

// DocumentCompareInfo is what the workspace renders: both documents (the
// markdown editor's source shape — identity, format, text) and their diff.
type DocumentCompareInfo struct {
	Before           MarkdownSourceInfo `json:"before"`
	After            MarkdownSourceInfo `json:"after"`
	IgnoreWhitespace bool               `json:"ignoreWhitespace"`
	Diff             document.Diff      `json:"diff"`
}

// ResolveDocumentCompare loads two documents and diffs them for the compare
// workspace. Read-only and session-free: the workspace re-resolves through
// here to swap sides or toggle whitespace, and nothing is ever written.
func (a *App) ResolveDocumentCompare(req DocumentCompareRequest) (DocumentCompareInfo, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return DocumentCompareInfo{}, err
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return DocumentCompareInfo{}, errors.New("comparing documents opens a conversation's documents, and no conversation was given")
	}
	if normalizeSubtitleArtifactID(req.BeforeArtifactID) == "" || normalizeSubtitleArtifactID(req.AfterArtifactID) == "" {
		return DocumentCompareInfo{}, errors.New("comparing documents needs both a before and an after document")
	}
	detail, err := getConversation(config.Storage, conversationID)
	if err != nil {
		return DocumentCompareInfo{}, err
	}
	before, err := findCompareDocument(detail, req.BeforeArtifactID)
	if err != nil {
		return DocumentCompareInfo{}, err
	}
	after, err := findCompareDocument(detail, req.AfterArtifactID)
	if err != nil {
		return DocumentCompareInfo{}, err
	}
	return compareDocumentContents(config.Storage, detail, before, after, req.IgnoreWhitespace)
}

// compareDocumentContents reads both resolved entries and diffs them.
func compareDocumentContents(storage ConfigStorage, detail ConversationDetail, before, after HistoryContent, ignoreWhitespace bool) (DocumentCompareInfo, error) {
	conversationID := detail.Conversation.ID
	beforeSource, err := markdownSourceForContent(storage, conversationID, detail.Conversation.Title, before)
	if err != nil {
		return DocumentCompareInfo{}, err
	}
	afterSource, err := markdownSourceForContent(storage, conversationID, detail.Conversation.Title, after)
	if err != nil {
		return DocumentCompareInfo{}, err
	}
	return DocumentCompareInfo{
		Before:           beforeSource,
		After:            afterSource,
		IgnoreWhitespace: ignoreWhitespace,
		Diff:             document.BuildDiff(beforeSource.Text, afterSource.Text, beforeSource.Format, afterSource.Format, ignoreWhitespace),
	}, nil
}

// findCompareDocument resolves a document id the way the planner and the
// workspace name one: the full id, or the composer's @-mention form ("@md_"
// plus the short chip width) — the @ drops and a truncated id resolves as
// the UNIQUE prefix of one document artifact (counted by distinct id, since
// a mention re-rides the citing turn under its original id). A prefix that
// matches several documents, an id naming a non-document artifact, or one
// naming nothing at all is an error — a named document never silently
// resolves to a different file (newestSubtitleArtifact's rules).
func findCompareDocument(detail ConversationDetail, wantID string) (HistoryContent, error) {
	wantID = normalizeSubtitleArtifactID(wantID)
	if wantID == "" {
		return HistoryContent{}, errors.New("compare_documents needs a document id")
	}
	var prefixMatch HistoryContent
	prefixIDs := map[string]bool{}
	otherType := ""
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for _, content := range detail.Turns[i].Content {
			if content.ArtifactID == "" {
				continue
			}
			matches := content.ArtifactID == wantID || strings.HasPrefix(content.ArtifactID, wantID)
			if markdownArtifactExtension(content) == "" {
				if matches && otherType == "" {
					otherType = content.Type
				}
				continue
			}
			if content.ArtifactID == wantID {
				return content, nil
			}
			if matches && !prefixIDs[content.ArtifactID] {
				if len(prefixIDs) == 0 {
					prefixMatch = content
				}
				prefixIDs[content.ArtifactID] = true
			}
		}
	}
	switch {
	case len(prefixIDs) == 1:
		return prefixMatch, nil
	case len(prefixIDs) > 1:
		return HistoryContent{}, fmt.Errorf("compare_documents found %d documents whose id starts with %q in this conversation — name the full artifact id", len(prefixIDs), wantID)
	case otherType != "":
		return HistoryContent{}, fmt.Errorf("artifact %q is %s, not a document — compare_documents compares markdown (.md) and Fountain (.fountain) documents", wantID, articleFor(otherType))
	default:
		return HistoryContent{}, fmt.Errorf("compare_documents found no document with id %q in this conversation", wantID)
	}
}

// articleFor renders a content type with its indefinite article for error
// text ("an image", "a video").
func articleFor(noun string) string {
	if noun == "" {
		return "another kind of asset"
	}
	switch noun[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return "an " + noun
	}
	return "a " + noun
}

// compareDocumentCandidates lists the documents this turn references — the
// newest turn's markdown entries when it is the user's, in content order:
// attachments first, then @-mentions in the order the user typed them (the
// persisted order), deduplicated by id. Empty when the newest turn isn't the
// user's — by tool time the asking turn is always persisted, so that means
// there is nothing to default from.
func compareDocumentCandidates(detail ConversationDetail) []HistoryContent {
	candidates := []HistoryContent{}
	if len(detail.Turns) == 0 || detail.Turns[len(detail.Turns)-1].Role != "user" {
		return candidates
	}
	seen := map[string]bool{}
	for _, content := range detail.Turns[len(detail.Turns)-1].Content {
		if content.ArtifactID == "" || markdownArtifactExtension(content) == "" || seen[content.ArtifactID] {
			continue
		}
		seen[content.ArtifactID] = true
		candidates = append(candidates, content)
	}
	return candidates
}

// resolveComparePair picks the tool's before and after documents: a named id
// resolves through findCompareDocument; a side left unnamed fills from this
// turn's referenced documents in order, skipping any already named. The
// defaults must be unambiguous — exactly as many unnamed candidates as empty
// sides — and the two sides must be different documents.
func resolveComparePair(detail ConversationDetail, beforeID, afterID string) (HistoryContent, HistoryContent, error) {
	sides := [2]*HistoryContent{}
	for i, id := range [2]string{beforeID, afterID} {
		if normalizeSubtitleArtifactID(id) == "" {
			continue
		}
		content, err := findCompareDocument(detail, id)
		if err != nil {
			return HistoryContent{}, HistoryContent{}, err
		}
		sides[i] = &content
	}
	missing := 0
	for _, side := range sides {
		if side == nil {
			missing++
		}
	}
	if missing > 0 {
		candidates := compareDocumentCandidates(detail)
		pool := []HistoryContent{}
		for _, candidate := range candidates {
			if (sides[0] != nil && sides[0].ArtifactID == candidate.ArtifactID) || (sides[1] != nil && sides[1].ArtifactID == candidate.ArtifactID) {
				continue
			}
			pool = append(pool, candidate)
		}
		switch {
		case len(pool) < missing:
			return HistoryContent{}, HistoryContent{}, fmt.Errorf("compare_documents needs two documents, and this turn references %s — ask the user to @-mention or attach both documents, or name them with artifactId (before) and compareArtifactId (after)", describeCompareCandidates(candidates))
		case len(pool) > missing:
			return HistoryContent{}, HistoryContent{}, fmt.Errorf("this turn references %s — name the two to compare with artifactId (before) and compareArtifactId (after)", describeCompareCandidates(candidates))
		}
		for i := range sides {
			if sides[i] == nil {
				sides[i] = &pool[0]
				pool = pool[1:]
			}
		}
	}
	if sides[0].ArtifactID == sides[1].ArtifactID {
		return HistoryContent{}, HistoryContent{}, fmt.Errorf("both sides name the same document (%s) — compare_documents compares two different documents", sides[0].ArtifactID)
	}
	return *sides[0], *sides[1], nil
}

// describeCompareCandidates renders this turn's documents for an error: the
// count plus their ids, which the planner can pass straight back.
func describeCompareCandidates(candidates []HistoryContent) string {
	if len(candidates) == 0 {
		return "no documents"
	}
	ids := make([]string, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.ArtifactID
	}
	if len(candidates) == 1 {
		return "only one document (" + ids[0] + ")"
	}
	return fmt.Sprintf("%d documents (%s)", len(candidates), strings.Join(ids, ", "))
}

// ToolCompareDocumentsResult is the compare tool's evidence: which documents
// were compared, the change counts, the scenes or headings that changed, and
// a capped excerpt of the changed lines — enough for the reply to say what
// changed. The full diff stays with the workspace on the user's screen.
type ToolCompareDocumentsResult struct {
	ConversationID   string   `json:"conversationId"`
	BeforeArtifactID string   `json:"beforeArtifactId"`
	AfterArtifactID  string   `json:"afterArtifactId"`
	BeforeFormat     string   `json:"beforeFormat"`
	AfterFormat      string   `json:"afterFormat"`
	BeforeLines      int      `json:"beforeLines"`
	AfterLines       int      `json:"afterLines"`
	Added            int      `json:"added"`
	Removed          int      `json:"removed"`
	Changes          int      `json:"changes"`
	Identical        bool     `json:"identical"`
	ChangedSections  []string `json:"changedSections,omitempty"`
	Excerpt          string   `json:"excerpt,omitempty"`
}

// executeCompareDocuments resolves the pair, diffs it, and opens the compare
// workspace. The diff is computed BEFORE the launch so an unreadable document
// fails the call instead of opening an empty view, and the launch is the
// execution: an undeliverable event fails the call rather than leaving
// evidence that claims a comparison no UI showed (the open_editor rule).
func executeCompareDocuments(tools HarnessToolExecutionContext, beforeID, afterID string) (ToolCompareDocumentsResult, string, error) {
	if strings.TrimSpace(tools.ConversationID) == "" {
		return ToolCompareDocumentsResult{}, "document comparison is unavailable", errors.New("compare_documents compares a conversation's documents, and no conversation is attached to this call")
	}
	if tools.OpenEditor == nil {
		return ToolCompareDocumentsResult{}, "document comparison is unavailable", errors.New("compare_documents is not available in this context")
	}
	detail, err := getConversation(tools.Storage, tools.ConversationID)
	if err != nil {
		return ToolCompareDocumentsResult{}, "document comparison failed", err
	}
	before, after, err := resolveComparePair(detail, beforeID, afterID)
	if err != nil {
		return ToolCompareDocumentsResult{}, "document comparison failed", err
	}
	info, err := compareDocumentContents(tools.Storage, detail, before, after, false)
	if err != nil {
		return ToolCompareDocumentsResult{}, "document comparison failed", err
	}
	if err := tools.OpenEditor(documentCompareLaunchKind, tools.ConversationID, "", []string{before.ArtifactID, after.ArtifactID}); err != nil {
		return ToolCompareDocumentsResult{}, "document comparison failed", err
	}
	diff := info.Diff
	result := ToolCompareDocumentsResult{
		ConversationID:   tools.ConversationID,
		BeforeArtifactID: before.ArtifactID,
		AfterArtifactID:  after.ArtifactID,
		BeforeFormat:     info.Before.Format,
		AfterFormat:      info.After.Format,
		BeforeLines:      len(document.SplitLines(info.Before.Text)),
		AfterLines:       len(document.SplitLines(info.After.Text)),
		Added:            diff.Stats.Added,
		Removed:          diff.Stats.Removed,
		Changes:          diff.Stats.Changes,
		Identical:        diff.Stats.Identical,
		ChangedSections:  changedSectionNames(diff),
		Excerpt:          documentCompareExcerpt(diff),
	}
	return result, documentCompareSummary(result), nil
}

// documentSectionLabel names a hunk's place for evidence and the change list.
func documentSectionLabel(section string) string {
	if section == "" {
		return "start of the document"
	}
	return section
}

// changedSectionNames lists the distinct places the changes sit, in document
// order, capped at documentCompareMaxSections.
func changedSectionNames(diff document.Diff) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, hunk := range diff.Hunks {
		label := documentSectionLabel(hunk.Section)
		if seen[label] {
			continue
		}
		seen[label] = true
		if len(names) == documentCompareMaxSections {
			break
		}
		names = append(names, label)
	}
	return names
}

// hunkSideRange renders one side's lines for an excerpt header: "42",
// "42–44", or "—" when the side has no lines in the change.
func hunkSideRange(line, count int) string {
	switch count {
	case 0:
		return "—"
	case 1:
		return fmt.Sprint(line)
	}
	return fmt.Sprintf("%d–%d", line, line+count-1)
}

// documentCompareExcerpt renders the changes unified-style — a header per
// change naming its scene or heading and line ranges, then its removed lines
// ("- ") and added lines ("+ ") — until documentCompareExcerptLines changed
// lines or documentCompareExcerptChars bytes, closing with a count of what
// was left out. Empty for identical documents.
func documentCompareExcerpt(diff document.Diff) string {
	if diff.Stats.Identical {
		return ""
	}
	var excerpt strings.Builder
	shown := 0
	full := false
	for _, hunk := range diff.Hunks {
		header := fmt.Sprintf("@@ %s · before %s · after %s\n", documentSectionLabel(hunk.Section), hunkSideRange(hunk.BeforeLine, hunk.BeforeCount), hunkSideRange(hunk.AfterLine, hunk.AfterCount))
		if excerpt.Len()+len(header) > documentCompareExcerptChars {
			break
		}
		excerpt.WriteString(header)
		lines := []string{}
		for r := hunk.FirstRow; r <= hunk.LastRow; r++ {
			if line := diff.Rows[r].Before; line != nil {
				lines = append(lines, "- "+truncateRunes(line.Text, documentCompareExcerptLineRunes))
			}
		}
		for r := hunk.FirstRow; r <= hunk.LastRow; r++ {
			if line := diff.Rows[r].After; line != nil {
				lines = append(lines, "+ "+truncateRunes(line.Text, documentCompareExcerptLineRunes))
			}
		}
		for _, line := range lines {
			if shown == documentCompareExcerptLines || excerpt.Len()+len(line)+1 > documentCompareExcerptChars {
				full = true
				break
			}
			excerpt.WriteString(line + "\n")
			shown++
		}
		if full {
			break
		}
	}
	if remaining := diff.Stats.Added + diff.Stats.Removed - shown; remaining > 0 {
		fmt.Fprintf(&excerpt, "… %d more changed %s not shown — the full comparison is on the user's screen\n", remaining, pluralNoun(remaining, "line", "lines"))
	}
	return strings.TrimRight(excerpt.String(), "\n")
}

func pluralNoun(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

// documentCompareSummary is the tool's one-line account for the planner and
// the final model: what opened, the counts, where the changes sit, and the
// instruction to describe — not re-type — the excerpt.
func documentCompareSummary(result ToolCompareDocumentsResult) string {
	opened := fmt.Sprintf("Opened %s (before) and %s (after) side by side in the compare view on the user's screen", result.BeforeArtifactID, result.AfterArtifactID)
	mixed := ""
	if result.BeforeFormat != result.AfterFormat {
		mixed = " One document is Markdown and the other Fountain — they were compared line by line as text."
	}
	if result.Identical {
		return opened + ": the two documents are identical — there are no differences to describe. Nothing was modified." + mixed
	}
	where := ""
	if len(result.ChangedSections) > 0 {
		names := result.ChangedSections
		more := ""
		if len(names) > 5 {
			more = fmt.Sprintf(", and %d more", len(names)-5)
			names = names[:5]
		}
		where = " (in: " + strings.Join(names, "; ") + more + ")"
	}
	return fmt.Sprintf("%s: %d %s added and %d removed across %d %s%s. Describe what changed in your own words from the excerpt — do not paste it back; the user is looking at the full comparison. Nothing was modified.%s",
		opened, result.Added, pluralNoun(result.Added, "line", "lines"), result.Removed, result.Changes, pluralNoun(result.Changes, "change", "changes"), where, mixed)
}

// compareDocumentsToolDefinition is the harness's document-compare
// capability. Unconditional in the registry, like open_editor: it needs no
// keys, no CLI, and no model, so a compare ask never degrades to a
// from-knowledge answer — and it still works in airplane mode.
func compareDocumentsToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "compare_documents",
		Title:       "Compare documents",
		Description: "Compares two of the conversation's documents — markdown (.md) or Fountain screenplay (.fountain) — and opens Atelier's side-by-side compare view on the user's screen: lines added, removed, and changed, with the changed words highlighted and a list of the changes by scene (Fountain) or heading (Markdown). The result carries the change counts, the scenes or headings that changed, and an excerpt of the changed lines — describe the differences from it. By default it compares the two documents this turn attaches or @-mentions, in the order the user gave them: the first is the BEFORE (older) side, the second the AFTER (newer) side. Name them with artifactId (before) and compareArtifactId (after) when the turn references more than two documents, when the wording reverses the order (\"what changed in @md_b since @md_a\" — before is md_a), or to compare documents from earlier in the conversation. Call this whenever the user asks to compare or diff two documents, drafts, or scripts, or asks what changed between them. It only reads the two documents: it never edits either one or writes a new document.",
		Example:     `{"name":"compare_documents"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"artifactId":        stringParam(`Optional — the BEFORE document (the older draft, shown on the left): a document artifact id from this conversation, full ("md_3fa09c1b2d4e5f60718293a4") or the short @-mention form the user typed ("@md_3fa09c1b2"). Omit to use the first document this turn attaches or @-mentions.`),
				"compareArtifactId": stringParam(`Optional — the AFTER document (the newer draft, shown on the right), in the same forms. Omit to use the second document this turn attaches or @-mentions.`),
			},
			"required": []string{},
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			return executeCompareDocuments(tools, call.ArtifactID, call.CompareArtifactID)
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			// Like open_editor, the comparison consumes nothing — no media
			// kind, cost, or model — so the bare projection carries the verb
			// and the pair it compared.
			if typed, ok := result.Result.(ToolCompareDocumentsResult); ok {
				activity.Command = []string{"compare_documents", typed.BeforeArtifactID, typed.AfterArtifactID}
			}
			return activity
		},
	}
}
