package main

// document_authoring.go — the responseMode "document" authoring pass. Triage
// routes a pure document ask (a markdown report, a Fountain screenplay) here:
// instead of the planner, the PRIMARY model authors the document in one
// non-streaming side call (the generateConversationTitle pattern), the
// harness parses the fenced document out of the reply, and the turn save
// persists it as an md_ artifact through the markdown attachment identity —
// so the reply chip, the assets panel, the editors, and @-mentions all treat
// it like any other document. The final response still streams normally, told
// via a code-authored note that the document is delivered and must not be
// re-typed (documentAuthoredEvidenceNote, injected by preparedResponseRequest
// through HarnessPreparedTurn.DocumentAuthored).

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// documentAuthorMaxAttempts bounds the authoring call: one initial attempt
// plus a single correction retry when the reply carries no parseable document
// — the triageMaxAttempts idiom. Provider errors are NOT retried (the house
// rule): a failing provider returns nil and the turn degrades to a normal
// text response.
const documentAuthorMaxAttempts = 2

// documentAuthorTimeout bounds one authoring call. Long-form output on a
// local model legitimately takes minutes; the cap only keeps a wedged
// provider from hanging the turn when the request context itself has no
// deadline.
const documentAuthorTimeout = 5 * time.Minute

// authoredChatDocument is one parsed authoring-pass result: the document body
// (what the save path persists via writeMarkdownAttachment), the dialect it
// was fenced as, and the model that wrote it.
type authoredChatDocument struct {
	Format string // documentFormatMarkdown | documentFormatFountain
	Body   string
	Words  int
	Model  string
}

// documentAuthoringSystemPrompt is the authoring call's system prompt. The
// fenced block with a dialect-naming info string is the protocol the parser
// (parseAuthoredDocument) holds the model to; the correction retry names the
// protocol again on a parse miss.
const documentAuthoringSystemPrompt = "You are authoring a document that Atelier will save into the conversation as a file and hand to the user as an editable, downloadable artifact. Respond with the COMPLETE document wrapped in exactly one fenced code block whose info string names the dialect:\n\n" +
	"- ```markdown — a Markdown document (.md): reports, notes, specs, outlines, READMEs, essays.\n" +
	"- ```fountain — a Fountain plaintext screenplay (.fountain): scene headings in caps (INT./EXT.), character cues in caps on their own line above dialogue, action lines, parentheticals, transitions.\n\n" +
	"Write nothing outside the fence — no preamble, no explanation, no closing remarks: the fenced block is saved verbatim as the file. Follow the user's brief and honor the conversation's context (an outline, notes, names, earlier drafts)."

// documentAuthorTaggedFenceRe matches the opening line of the protocol fence:
// ```markdown or ```fountain, tolerating trailing info-string text. Matched
// only at line start so an indented or mid-line mention never opens a block.
var documentAuthorTaggedFenceRe = regexp.MustCompile("(?m)^```[ \\t]*(markdown|fountain)\\b[^\\n]*$")

// documentAuthorBareFenceRe matches an untagged or foreign-tagged opening
// fence (```text, plain ```) — the fallback shape the dialect-from-taskHint
// rule covers.
var documentAuthorBareFenceRe = regexp.MustCompile("(?m)^```[^`\\n]*$")

// documentAuthorClosingFenceRe matches a bare closing-fence line.
var documentAuthorClosingFenceRe = regexp.MustCompile("^```[ \\t]*$")

// documentFenceBody returns the lines after an opening fence's end, up to the
// LAST bare closing-fence line: a Markdown document's own internal ``` blocks
// pair up inside the outer fence, so the outer close is the last bare ``` in
// the reply. When no closing fence exists the remainder is taken whole — an
// unterminated fence still delivers the body it carried, and the save path's
// own validation is the backstop.
func documentFenceBody(content string, openingEnd int) string {
	lines := strings.Split(content[openingEnd:], "\n")
	lastClose := -1
	for i, line := range lines {
		if documentAuthorClosingFenceRe.MatchString(line) {
			lastClose = i
		}
	}
	if lastClose >= 0 {
		lines = lines[:lastClose]
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// parseAuthoredDocument extracts the document from an authoring reply. The
// tagged fence is authoritative for the dialect; an untagged fence falls back
// to the dialect triage's toolTask names (documentFormatFromTask). Prose
// around the fence is tolerated (models add it despite the prompt) — the
// fenced block is still saved verbatim.
func parseAuthoredDocument(content, taskHint string) (authoredChatDocument, error) {
	if m := documentAuthorTaggedFenceRe.FindStringSubmatchIndex(content); m != nil {
		return buildAuthoredDocument(documentFenceBody(content, m[1]), content[m[2]:m[3]])
	}
	if m := documentAuthorBareFenceRe.FindStringIndex(content); m != nil {
		return buildAuthoredDocument(documentFenceBody(content, m[1]), documentFormatFromTask(taskHint))
	}
	return authoredChatDocument{}, fmt.Errorf("no fenced document block (```markdown or ```fountain) found in the reply")
}

// buildAuthoredDocument validates the extracted body (non-empty —
// writeMarkdownAttachment would refuse it anyway, but failing here is what
// feeds the correction retry) and records the word count the evidence note
// states.
func buildAuthoredDocument(body, format string) (authoredChatDocument, error) {
	body = strings.Trim(body, "\n")
	if strings.TrimSpace(body) == "" {
		return authoredChatDocument{}, fmt.Errorf("the fenced document block is empty")
	}
	if format != documentFormatMarkdown && format != documentFormatFountain {
		return authoredChatDocument{}, fmt.Errorf("unknown document dialect %q", format)
	}
	return authoredChatDocument{Format: format, Body: body, Words: markdownWordCount(body)}, nil
}

// documentFormatFromTask resolves the fallback dialect from triage's toolTask
// wording when the reply's fence carried no recognized tag.
func documentFormatFromTask(task string) string {
	normalized := strings.ToLower(task)
	if strings.Contains(normalized, "fountain") || strings.Contains(normalized, "screenplay") || strings.Contains(normalized, "script") {
		return documentFormatFountain
	}
	return documentFormatMarkdown
}

// documentAuthorCorrectionPrompt is the parse-failure feedback, modeled on
// triageCorrectionPrompt: name the observed failure and demand only the
// protocol shape, so the model cannot repeat the prose it just wrote.
func documentAuthorCorrectionPrompt(parseErr string) string {
	return "Your previous reply could not be parsed as a document: " + parseErr +
		". Respond again with the COMPLETE document inside exactly one fenced code block — ```markdown or ```fountain — and nothing outside the fence."
}

// documentAuthoringRequest builds the authoring call: the authoring system
// prompt over the conversation's messages with every media payload stripped
// (the messagesWithoutMedia rule — documents are authored from words, and the
// bytes would bloat the call), the project's instruction block riding the
// system prompt when one is set, and the router's toolTask wording appended
// so the format hint and brief stay attached. The history budget reserves a
// larger response share than the final response's: the model's output here is
// the whole document.
func (h *HarnessEngine) documentAuthoringRequest(req ChatRequest, responseModel, responseProvider, toolTask string) ChatRequest {
	system := documentAuthoringSystemPrompt
	if evidenceNote := projectInstructionsEvidenceNote(req.ProjectNotes); evidenceNote != "" {
		system += "\n\n" + evidenceNote
	}
	if task := strings.TrimSpace(toolTask); task != "" {
		system += "\n\nThe user's ask, as the harness router understood it: " + task
	}
	numCtx := h.numCtx()
	authorReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    responseModel,
		Provider: responseProvider,
		System:   system,
		Messages: truncateChatHistory(messagesWithoutMedia(req.Messages), historyBudgetChars(numCtx, system, numCtx/2)),
		Options:  withNumCtx(req.Options, numCtx),
	}
	return authorReq
}

// authorChatDocument runs the document-mode authoring pass: one primary-model
// side call (plus the parse-failure correction retry) that returns the
// document to persist. A provider error, or two unparseable replies, returns
// nil and the turn degrades to a normal text response — a prose answer is
// always the safe floor for a document ask, unlike media where a
// from-knowledge narration would fabricate the artifact. Each attempt records
// one "document" run step (the one-step-per-provider-call convention; the
// kind rides usageModelCallStepKinds so the authoring call bills into usage
// reports beside triage and the final response).
func (h *HarnessEngine) authorChatDocument(ctx context.Context, req ChatRequest, responseModel, responseProvider, toolTask string, run *HarnessRun) *authoredChatDocument {
	authorCtx, cancel := context.WithTimeout(ctx, documentAuthorTimeout)
	defer cancel()
	authorReq := h.documentAuthoringRequest(req, responseModel, responseProvider, toolTask)
	providerID := resolvedProvider(authorReq)
	provider, err := h.app.providerFor(providerID, req.BaseURL)
	if err != nil {
		return nil
	}
	numCtx := h.numCtx()
	budget := historyBudgetChars(numCtx, authorReq.System, numCtx/2)
	messages := append([]ChatMessage{}, authorReq.Messages...)
	for attempt := 1; attempt <= documentAuthorMaxAttempts; attempt++ {
		step := run.appendStep("document", attempt, providerID, responseModel, "primary model authoring the requested document")
		run.Steps[step].Request = requestSnapshot(authorReq, numCtx, len(messages)-len(authorReq.Messages))
		completion, err := provider.CompleteChat(authorCtx, authorReq)
		if err != nil {
			run.completeStep(step, "failed", "", 0, err.Error())
			return nil
		}
		run.Steps[step].PromptTokens = completion.PromptTokens
		run.Steps[step].CostMicros = completion.CostMicros
		doc, parseErr := parseAuthoredDocument(completion.Content, toolTask)
		if parseErr == nil {
			doc.Model = responseModel
			run.Steps[step].Summary = fmt.Sprintf("authored a %s document (%d words)", doc.Format, doc.Words)
			run.completeStep(step, "completed", completion.Reason, completion.EvalTokens, "")
			return &doc
		}
		if attempt < documentAuthorMaxAttempts {
			// The call itself succeeded, so the attempt completes as a
			// correction round (the triage convention) and the parse error is
			// fed back for the retry.
			run.Steps[step].Summary = "no parseable document, correction requested: " + parseErr.Error()
			run.completeStep(step, "completed", completion.Reason, completion.EvalTokens, "")
			messages = append(messages,
				ChatMessage{Role: "assistant", Content: completion.Content},
				ChatMessage{Role: "user", Content: documentAuthorCorrectionPrompt(parseErr.Error())})
			authorReq.Messages = truncateChatHistory(messages, budget)
			continue
		}
		run.completeStep(step, "failed", completion.Reason, completion.EvalTokens, parseErr.Error())
	}
	return nil
}

// documentAuthoredEvidenceNote is the code-authored note the final model
// receives after the authoring pass produced the document: the artifact is
// delivered beneath the reply with edit and download controls, so the reply
// summarizes rather than re-types (the tool-evidence posture, except here the
// model IS the author — the note frames what it already did).
func documentAuthoredEvidenceNote(doc *authoredChatDocument) string {
	if doc == nil {
		return ""
	}
	return fmt.Sprintf("Atelier note: your document-authoring pass already wrote the requested %s document (%d words) and Atelier saved it into this conversation as an editable artifact, delivered below your reply with edit and download controls. Do not repeat the document's text in your reply. Answer with a brief summary of what you wrote — a sentence or two — and offer a natural next step, such as opening it in the editor or revising a section.", markdownFormatLabel(doc.Format), doc.Words)
}
