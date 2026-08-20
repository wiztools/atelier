package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// triageNumPredict caps the harness model's triage output. It must be large
// enough for the four-field JSON to complete on a wordy model — a length-trim
// here drops the only chance to set responseMode, and the fail-safe lands on
// "text" even when the user attached an image that warranted "vision".
const triageNumPredict = 1024

// HarnessTriageDecision is the harness model's routing decision for a turn. It is
// stored on the HarnessRun for telemetry; Error records a triage failure that
// forced the fail-safe tool path.
type HarnessTriageDecision struct {
	NeedsTools   bool   `json:"needsTools"`
	ResponseMode string `json:"responseMode,omitempty"`
	ToolTask     string `json:"toolTask,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Error        string `json:"error,omitempty"`
}

func triageResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"needsTools", "responseMode", "toolTask", "reason"},
		"properties": map[string]any{
			"needsTools":   map[string]any{"type": "boolean"},
			"responseMode": map[string]any{"type": "string", "enum": []string{"text", "image", "vision", "video", "audio"}},
			"toolTask":     map[string]any{"type": "string"},
			"reason":       map[string]any{"type": "string"},
		},
	}
}

// decodeTriageDecision parses the harness model's triage JSON. It is lenient
// about mis-typed scalar fields the way the planner parser is: a toolTask (or
// reason/responseMode) emitted as an object/array/number/bool is coerced to its
// JSON text representation rather than failing the whole parse. A triage parse
// failure is catastrophic — it drops the responseMode and routing guidance the
// rest of the harness depends on (see conv_4fcc40eb3398a9bb21cb7d00, where an
// object-valued toolTask crashed triage and silently disabled the
// narration-routing hint). Coercion keeps a structurally-sound decision usable;
// needsTools still must be a boolean since the rest of the harness branches on
// its exact value, and a top-level JSON parse error still fails as before.
func decodeTriageDecision(content string) (HarnessTriageDecision, error) {
	candidate := stripJSONFence(content)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &raw); err != nil {
		// A truncation-tolerant fallback: when the model exhausted its output
		// budget mid-JSON (the verbose toolTask field is the usual victim), the
		// whole object fails to parse and triage fail-safes to text mode — which
		// drops responseMode "image"/"video"/"audio" and sinks the entire turn
		// (the planner then correctly concludes it can't generate media). The
		// routing-critical fields (needsTools, responseMode) almost always appear
		// before the truncation point, so salvage them by regex rather than
		// discarding the whole decision. See conv_2d1be19a.
		if salvaged, ok := salvageTriageFromTruncation(candidate); ok {
			raw = salvaged
		} else {
			return HarnessTriageDecision{}, fmt.Errorf("triage decision JSON invalid: %w", err)
		}
	}
	var decision HarnessTriageDecision
	// needsTools is the one field that must decode as a boolean — the harness
	// branches on its exact value, so a mis-typed flag is a real error rather
	// than a coercible scalar.
	if data, ok := raw["needsTools"]; ok {
		if err := json.Unmarshal(data, &decision.NeedsTools); err != nil {
			return HarnessTriageDecision{}, fmt.Errorf("triage decision JSON invalid: %w", err)
		}
	}
	decision.ResponseMode = coerceJSONString(raw["responseMode"])
	decision.ToolTask = coerceJSONString(raw["toolTask"])
	decision.Reason = coerceJSONString(raw["reason"])
	return decision, nil
}

// salvageTriageFromTruncation extracts the routing-critical triage fields from
// a JSON object that was truncated before it closed (the model hit its output
// token limit). It returns the salvaged fields as a RawMessage map and true when
// at least needsTools or responseMode could be recovered; otherwise (false, nil)
// so the caller falls through to the hard parse error. Only complete, well-formed
// field values are kept — a value cut off mid-string is dropped, never guessed.
func salvageTriageFromTruncation(candidate string) (map[string]json.RawMessage, bool) {
	salvaged := map[string]json.RawMessage{}
	// Match "needsTools": <bool> and "responseMode": "<mode>" with complete
	// values. The mode enum is constrained so a partial value (e.g. "im") is not
	// matched as a false positive.
	needsRe := regexp.MustCompile(`"?needsTools"?\s*:\s*(true|false)`)
	if m := needsRe.FindStringSubmatch(candidate); m != nil {
		salvaged["needsTools"] = json.RawMessage(m[1])
	}
	modeRe := regexp.MustCompile(`"?responseMode"?\s*:\s*"(text|image|vision|video|audio)"`)
	if m := modeRe.FindStringSubmatch(candidate); m != nil {
		salvaged["responseMode"] = json.RawMessage(`"` + m[1] + `"`)
	}
	if len(salvaged) == 0 {
		return nil, false
	}
	return salvaged, true
}

// coerceJSONString decodes a JSON value into a string, tolerating non-string
// scalars and containers the way the planner parser tolerates mis-typed fields.
// A plain string is unquoted; any other valid JSON value (object, array,
// number, bool, null) is rendered as its compact JSON text. An empty or
// unparseable RawMessage yields "". This keeps a structurally-sound triage
// decision usable when a small model wraps a string field in an object.
func coerceJSONString(data json.RawMessage) string {
	if len(bytes.TrimSpace(data)) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		return asString
	}
	// Not a string — re-render the raw value as compact JSON text so an object
	// or array becomes a readable (if ugly) string rather than sinking the parse.
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return ""
	}
	rendered, err := json.Marshal(generic)
	if err != nil {
		return ""
	}
	return string(rendered)
}

// messagesWithoutMedia copies messages for text-only side calls such as
// triage, so image, audio, and video payloads never reach a model that only
// routes the turn. Audio and video bytes are stripped for the same reason
// images are: a routing model doesn't need them, and they would bloat the
// triage request for nothing. Stripping video here is also what enforces the
// tool-path-only contract on video input — no adapter emits a video content
// part, so video never reaches a chat model.
func messagesWithoutMedia(messages []ChatMessage) []ChatMessage {
	stripped := make([]ChatMessage, len(messages))
	for index, message := range messages {
		message.Images = nil
		message.Audios = nil
		message.Videos = nil
		stripped[index] = message
	}
	return stripped
}

// messagesWithAttachmentNotes is the triage variant of messagesWithoutMedia:
// it strips the media bytes (so megabytes never reach the routing model) but
// leaves a compact text note on the latest user message describing what was
// attached. Without this, triage sees only bare text and can reason itself out
// of running an attachment-dependent tool — e.g. deciding lip_sync isn't needed
// because it "requires an audio clip and a video" that triage couldn't see were
// attached. Only the latest user turn is annotated: routing cares about what the
// user just sent, and annotating every historical message adds noise.
func messagesWithAttachmentNotes(messages []ChatMessage) []ChatMessage {
	// Build the note from the ORIGINAL latest user message (before stripping),
	// since attachmentNote reads the media counts the strip would nil out.
	var note string
	latestUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			latestUser = i
			note = attachmentNote(messages[i])
			break
		}
	}
	stripped := messagesWithoutMedia(messages)
	if note != "" && latestUser >= 0 {
		stripped[latestUser].Content = note + "\n" + stripped[latestUser].Content
	}
	return stripped
}

// attachmentNote builds the bracketed attachment summary prepended to a user
// message for triage, e.g. "[Attachments: 1 audio clip, 1 video]". Returns an
// empty string when the message carries no media (so no note is added and
// today's behavior is unchanged for text-only turns).
func attachmentNote(message ChatMessage) string {
	var parts []string
	if n := len(message.Images); n > 0 {
		parts = append(parts, pluralize(n, "image"))
	}
	if n := len(message.Audios); n > 0 {
		parts = append(parts, pluralize(n, "audio clip"))
	}
	if n := len(message.Videos); n > 0 {
		parts = append(parts, pluralize(n, "video"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[Attachments: " + strings.Join(parts, ", ") + "]"
}

func pluralize(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// triageChatTurn asks the harness model whether the turn needs tools and what
// response mode the primary model should use. Failures fail safe to the tool
// path: the planner there can still conclude no tools are needed, so a wrong
// fallback costs latency, never correctness. The response mode for the fail-
// safe leans toward "vision" when the latest user turn carries an image, so a
// triage decode failure (e.g. output truncated by num_predict) can't strip the
// only signal that would have kept the primary model's attention on the image.
func (h *HarnessEngine) triageChatTurn(ctx context.Context, req ChatRequest, harness harnessTarget, skillIndex []SkillIndexEntry) (HarnessTriageDecision, ChatCompletionResult, *HarnessRequestSnapshot) {
	system := triageSystemPrompt(h.toolRegistry(), skillIndex, h.config.Tools.Filesystem.Root)
	numCtx := h.numCtx()
	messages := messagesWithAttachmentNotes(req.Messages)
	truncated := truncateChatHistory(messages, historyBudgetChars(numCtx, system, triageNumPredict))
	triageReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    harness.model,
		Provider: harness.provider,
		System:   system,
		Messages: truncated,
		Format:   triageResponseSchema(),
		Options: map[string]any{
			"temperature": 0,
			"num_predict": triageNumPredict,
			"num_ctx":     numCtx,
		},
	}
	snapshot := requestSnapshot(triageReq, numCtx, len(messages)-len(truncated))
	completion, err := h.completeWithHarnessModel(ctx, harness, triageReq)
	if err != nil {
		return triageFailSafe(req, "triage call failed; deferring to the harness model planner", err.Error()), ChatCompletionResult{}, snapshot
	}
	decision, err := decodeTriageDecision(completion.Content)
	if err != nil {
		return triageFailSafe(req, "triage response was not valid JSON; deferring to the harness model planner", err.Error()), completion, snapshot
	}
	if decision.ResponseMode == "" {
		// An empty mode usually means truncation salvage recovered needsTools
		// but not responseMode. Lean the same way the hard fail-safe does: an
		// attached image is the strongest signal the user wanted it seen, so
		// default to vision rather than text.
		decision.ResponseMode = "text"
		if len(latestUserImages(req.Messages)) > 0 {
			decision.ResponseMode = "vision"
		}
	}
	return decision, completion, snapshot
}

// triageFailSafe builds the fail-safe decision for a triage failure: needsTools
// true (the planner can still decline) and responseMode "vision" when the user
// attached an image, otherwise "text". An attached image is the strongest
// signal that the user wanted the image understood; defaulting that case to
// "text" sends the primary model off to look at filesystem evidence instead.
func triageFailSafe(req ChatRequest, reason, errMsg string) HarnessTriageDecision {
	mode := "text"
	if len(latestUserImages(req.Messages)) > 0 {
		mode = "vision"
	}
	return HarnessTriageDecision{NeedsTools: true, ResponseMode: mode, Reason: reason, Error: errMsg}
}

func triageSystemPrompt(registry HarnessToolRegistry, skillIndex []SkillIndexEntry, workspaceRoot string) string {
	skills := "(none)"
	if len(skillIndex) > 0 {
		lines := make([]string, 0, len(skillIndex))
		for _, entry := range skillIndex {
			lines = append(lines, "- "+entry.Name+": "+entry.Description)
		}
		skills = strings.Join(lines, "\n")
	}
	// narrationRouting is a capability-conditional nudge: only when the
	// configured video model can produce audio itself (encoded in the
	// generate_video description by VideoAudioCapable) do we steer narration-
	// over-video requests to a single generate_video call. When the video model
	// has no audio capability this stays empty and the generate_speech +
	// lip_sync chain remains the correct path (made functional by the harness
	// forward-feeding generated media within a turn).
	narrationRouting := ""
	if registry.VideoAudioCapable() {
		narrationRouting = "\nWhen the user wants speech, narration, or a voice over a video, route to generate_video alone — the configured video model can produce the audio in the same call. Do not chain generate_speech + lip_sync for this. Reserve lip_sync for dubbing or re-syncing an existing attached audio clip to a face."
	}
	return strings.TrimSpace(fmt.Sprintf(`You are Atelier's harness model. You decide how the primary model should respond to the latest user turn and whether workspace tools are needed first.
You will not write the user-visible answer. Right now respond only with a JSON object matching the response schema:
{
  "needsTools": false,
  "responseMode": "text",
  "toolTask": "when needsTools is true, the evidence the harness model should gather",
  "reason": "brief decision reason"
}
Set responseMode to one of:
- "text": the user wants a text response (greetings, general knowledge, reasoning, writing, code, conversation).
- "image": the user asks to create, draw, paint, or render an image.
- "vision": the user attached an image and wants it analyzed, described, or understood.
- "video": the user asks to create, animate, or render a video or short clip.
- "audio": the user asks to speak/narrate text, or create music or a sound effect.
When the latest user message begins with "[Attachments: ...]", the user attached that media to the turn — treat it as available to tools that require it (e.g. lip_sync needs an audio clip plus a face image or video, transcribe_audio needs an audio clip, generate_video can animate an attached image or extend an attached video).%s
Set needsTools true only when answering requires acting on the workspace or a listed capability: reading, listing, searching, or writing files, running a command, generating an image, generating a video, generating audio, or following one of the listed skills.
Set needsTools false when your own knowledge is enough: greetings, general knowledge, reasoning, writing, and conversation about content already visible in the chat.
For responseMode "image", set needsTools true so the harness can run the generate_image tool before the primary model responds.
For responseMode "video", set needsTools true so the harness can run the generate_video tool before the primary model responds. Only use "video" when the generate_video tool is listed as available.
For responseMode "audio", set needsTools true so the harness can run the generate_speech or generate_sound tool before the primary model responds. Only use "audio" when the generate_speech or generate_sound tool is listed as available.
Available tools:
%s
Available skills:
%s
Workspace root: %s`, narrationRouting, registry.PromptCatalog(), skills, workspaceRoot))
}
