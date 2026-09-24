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

// triageMaxAttempts bounds the triage call: one initial attempt plus a single
// correction retry when the response doesn't parse, the same bound the skill
// selector allows itself.
const triageMaxAttempts = 2

// HarnessTriageDecision is the harness model's routing decision for a turn. It is
// stored on the HarnessRun for telemetry; Error records a triage failure that
// forced the fail-safe tool path.
type HarnessTriageDecision struct {
	NeedsTools   bool   `json:"needsTools"`
	ResponseMode string `json:"responseMode,omitempty"`
	ToolTask     string `json:"toolTask,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Error        string `json:"error,omitempty"`
	// MediaEdit records that triage judged the request to be a local edit of
	// EXISTING media rather than generation — grab a frame/screenshot,
	// split/trim a segment, join clips, crop/resize/rotate a clip, extract the
	// audio track, or put different audio under a video: the operations
	// Atelier's local ffmpeg tools serve (see local_ffmpeg.go). When no ffmpeg
	// CLI is configured, the harness turns this flag into a code-authored note
	// telling the final model to direct the user to install ffmpeg, instead of
	// letting the request silently fall through to a from-knowledge text
	// answer. Advisory: a false or missing flag only loses that notice, never
	// routing.
	MediaEdit bool `json:"mediaEdit,omitempty"`
	// ImageEdit is the image counterpart of MediaEdit: the request edits an
	// EXISTING image (convert format, resize/crop, rotate/flip, watermark,
	// collage, color adjust, strip metadata) rather than generating one — the
	// operations Atelier's local sips/ImageMagick tools serve (see
	// local_images.go). When a backend is missing, the harness turns this flag
	// into a code-authored install note for the final model. Advisory in the
	// same way: a false or missing flag only loses that notice.
	ImageEdit bool `json:"imageEdit,omitempty"`
}

func triageResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		// Every property required — the same all-required shape that keeps
		// this schema strict-clean for OpenRouter (see strictJSONSchema): an
		// optional property would be widened to a nullable union there. Decode
		// stays lenient (an absent mediaEdit/imageEdit is false) so the
		// truncation-salvage path still works.
		"required": []string{"needsTools", "responseMode", "toolTask", "reason", "mediaEdit", "imageEdit"},
		"properties": map[string]any{
			"needsTools":   map[string]any{"type": "boolean"},
			"responseMode": map[string]any{"type": "string", "enum": []string{"text", "image", "vision", "video", "audio"}},
			"toolTask":     map[string]any{"type": "string"},
			"reason":       map[string]any{"type": "string"},
			"mediaEdit":    map[string]any{"type": "boolean"},
			"imageEdit":    map[string]any{"type": "boolean"},
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
	// mediaEdit is advisory: a mis-typed value leaves it false rather than
	// sinking the routing decision the way a mis-typed needsTools would.
	// imageEdit is advisory the same way.
	if data, ok := raw["mediaEdit"]; ok {
		_ = json.Unmarshal(data, &decision.MediaEdit)
	}
	if data, ok := raw["imageEdit"]; ok {
		_ = json.Unmarshal(data, &decision.ImageEdit)
	}
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

// turnMediaSlots carries the tool-facing media resolved for the turn — explicit
// attachments plus @-mentioned assets plus the conversation-history fallback —
// exactly what resolveTurnMedia computed once in RunChatStream. Triage renders
// its grounding note from these slots instead of re-resolving: the fallback
// re-reads and re-encodes multi-megabyte artifacts as data URLs, which a
// routing decision that only needs counts must not pay for twice.
type turnMediaSlots struct {
	images []string
	videos []string
	audios []string
}

// messagesWithAttachmentNotes is the triage variant of messagesWithoutMedia:
// it strips the media bytes (so megabytes never reach the routing model) but
// leaves compact text notes on the latest user message describing the media
// the turn has access to. Two notes, both computed from the ORIGINAL messages
// (before stripping), since they read the media counts the strip would nil
// out:
//
//   - "[Attachments: ...]" — media the user attached to this message. Without
//     it, triage sees only bare text and can reason itself out of running an
//     attachment-dependent tool (deciding lip_sync isn't needed because it
//     "requires an audio clip and a video" triage couldn't see were attached).
//   - "[Available media: ...]" — media beyond the attachments: @-mentioned
//     assets and the newest artifacts the history fallback resolved. Without
//     it, triage cannot tell that "extend this video" refers to something
//     real (conv_a90d8a8fec4a33e58e635b37: assistant media is stripped from
//     history, the frontend never echoes generated artifacts back as
//     attachments, so a bare extend follow-up looked like text-only feedback
//     and the primary model then claimed in prose that the clip had been
//     extended).
//
// Only the latest user turn is annotated: routing cares about what the user
// just sent, and annotating every historical message adds noise.
func messagesWithAttachmentNotes(messages []ChatMessage, slots turnMediaSlots) []ChatMessage {
	var notes []string
	latestUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			latestUser = i
			if note := attachmentNote(messages[i]); note != "" {
				notes = append(notes, note)
			}
			break
		}
	}
	if note := availableMediaNote(slots, messages); note != "" {
		notes = append(notes, note)
	}
	stripped := messagesWithoutMedia(messages)
	if len(notes) > 0 && latestUser >= 0 {
		stripped[latestUser].Content = strings.Join(notes, "\n") + "\n" + stripped[latestUser].Content
	}
	return stripped
}

// availableMediaNote summarizes the media tools can reach beyond the latest
// user turn's explicit attachments: @-mentioned assets plus whatever the
// resolveTurnMedia history fallback backfilled (the newest image-or-video
// artifact when neither kind is explicit, the newest audio). Counts only —
// the routing model never needs the bytes, and the fallback's data URLs can
// be tens of megabytes. Empty when every resolved slot is explained by the
// message's own attachments, so an ordinary attached-media turn renders no
// second note.
func availableMediaNote(slots turnMediaSlots, messages []ChatMessage) string {
	var parts []string
	if n := len(slots.images) - len(latestUserImages(messages)); n > 0 {
		parts = append(parts, pluralize(n, "image"))
	}
	if n := len(slots.videos) - len(latestUserVideoURLs(messages)); n > 0 {
		parts = append(parts, pluralize(n, "video"))
	}
	if n := len(slots.audios) - len(latestUserAudioURLs(messages)); n > 0 {
		parts = append(parts, pluralize(n, "audio clip"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[Available media: " + strings.Join(parts, ", ") + "]"
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
// response mode the primary model should use. A response that doesn't decode
// gets one correction retry with the parse error fed back — the same repair
// idiom the planner uses for invalid plans — so a model that answered in prose
// instead of routing (conv_b8581c45e97098773e5bd238: "invalid character 'T'")
// gets a chance to emit the JSON object before the fail-safe engages. Failures
// still fail safe to the tool path: the planner there can still conclude no
// tools are needed, so a wrong fallback costs latency, never correctness. The
// response mode for the fail-safe leans toward "vision" when the latest user
// turn carries an image, so a triage decode failure (e.g. output truncated by
// num_predict) can't strip the only signal that would have kept the primary
// model's attention on the image. One "triage" step is recorded per attempt
// (nil run is tolerated for direct/unit callers), keeping the
// one-step-per-provider-call telemetry convention. slots is the turn's
// resolved media (RunChatStream's resolveTurnMedia result): it grounds the
// available-media note so a bare "extend this video" follow-up can be routed
// against media the message itself doesn't carry.
func (h *HarnessEngine) triageChatTurn(ctx context.Context, req ChatRequest, harness harnessTarget, skillIndex []SkillIndexEntry, run *HarnessRun, slots turnMediaSlots) (HarnessTriageDecision, ChatCompletionResult, *HarnessRequestSnapshot) {
	system := triageSystemPrompt(h.toolRegistry(), skillIndex, h.config.Tools.Filesystem.Root)
	numCtx := h.numCtx()
	messages := messagesWithAttachmentNotes(req.Messages, slots)
	budget := historyBudgetChars(numCtx, system, triageNumPredict)
	triageReq := ChatRequest{
		BaseURL:  req.BaseURL,
		Model:    harness.model,
		Provider: harness.provider,
		System:   system,
		Messages: truncateChatHistory(messages, budget),
		Format:   triageResponseSchema(),
		Options: map[string]any{
			"temperature": 0,
			"num_predict": triageNumPredict,
			"num_ctx":     numCtx,
		},
	}
	triageStep := -1
	for attempt := 1; attempt <= triageMaxAttempts; attempt++ {
		snapshot := requestSnapshot(triageReq, numCtx, len(messages)-len(triageReq.Messages))
		if run != nil {
			triageStep = run.appendStep("triage", attempt, harness.provider, harness.model, "harness model deciding response mode and tools")
			run.Steps[triageStep].Request = snapshot
		}
		completion, err := h.completeWithHarnessModel(ctx, harness, triageReq)
		if err != nil {
			decision := triageFailSafe(req, "triage call failed; deferring to the harness model planner", err.Error())
			if run != nil {
				run.Steps[triageStep].Decision = triageDecisionLabel(decision)
				run.completeStep(triageStep, "failed", completion.Reason, completion.EvalTokens, decision.Error)
			}
			return decision, ChatCompletionResult{}, snapshot
		}
		if run != nil {
			run.Steps[triageStep].PromptTokens = completion.PromptTokens
			run.Steps[triageStep].CostMicros = completion.CostMicros
		}
		decision, decodeErr := decodeTriageDecision(completion.Content)
		if decodeErr == nil {
			// An empty mode usually means truncation salvage recovered needsTools
			// but not responseMode. Lean the same way the hard fail-safe does: an
			// attached image is the strongest signal the user wanted it seen, so
			// default to vision rather than text.
			if decision.ResponseMode == "" {
				decision.ResponseMode = "text"
				if len(latestUserImages(req.Messages)) > 0 {
					decision.ResponseMode = "vision"
				}
			}
			if run != nil {
				run.Steps[triageStep].Decision = triageDecisionLabel(decision)
				run.completeStep(triageStep, "completed", completion.Reason, completion.EvalTokens, "")
			}
			return decision, completion, snapshot
		}
		if attempt < triageMaxAttempts {
			// One correction retry: the call itself succeeded, so record the
			// attempt as a correction round (like the planner's invalid-plan
			// rounds) rather than a failure, then feed the parse error back.
			if run != nil {
				run.Steps[triageStep].Summary = "invalid triage decision, correction requested: " + decodeErr.Error()
				run.completeStep(triageStep, "completed", completion.Reason, completion.EvalTokens, "")
			}
			messages = append(messages,
				ChatMessage{Role: "assistant", Content: completion.Content},
				ChatMessage{Role: "user", Content: triageCorrectionPrompt(decodeErr.Error())})
			triageReq.Messages = truncateChatHistory(messages, budget)
			continue
		}
		decision = triageFailSafe(req, "triage response was not valid JSON after the correction retry; deferring to the harness model planner", decodeErr.Error())
		if run != nil {
			run.Steps[triageStep].Decision = triageDecisionLabel(decision)
			run.completeStep(triageStep, "failed", completion.Reason, completion.EvalTokens, decision.Error)
		}
		return decision, completion, snapshot
	}
	// Unreachable — every attempt returns — but the loop shape needs a fall-through.
	return triageFailSafe(req, "triage exhausted its attempts; deferring to the harness model planner", ""), ChatCompletionResult{}, nil
}

// triageCorrectionPrompt renders the feedback for a triage response that didn't
// decode, modeled on the planner's parse-failure correction: a vague "not
// valid JSON" leaves a weak model free to repeat the prose it just wrote, so
// name the observed failure and demand only the JSON object. See
// conv_b8581c45e97098773e5bd238: the harness model began answering the user
// in prose, the fail-safe sent the turn down the tool path, and the run ended
// in a spurious kc permission prompt for a poem question.
func triageCorrectionPrompt(parseErr string) string {
	return "Your previous response was not JSON, so no routing decision could be read: " + parseErr + ". " +
		"You are not answering the user. Do NOT emit code, markdown, or prose. " +
		"Respond with ONLY the JSON object matching the response schema " +
		`({"needsTools":..., "responseMode":..., "toolTask":..., "reason":..., "mediaEdit":..., "imageEdit":...}).`
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
  "reason": "brief decision reason",
  "mediaEdit": false,
  "imageEdit": false
}
Set responseMode to one of:
- "text": the user wants a text response (greetings, general knowledge, reasoning, writing, code, conversation). A transcript is a text deliverable too: transcribing, captioning, or timestamping an attached audio clip routes here, with needsTools true so the transcribe_audio tool gathers it.
- "image": the user asks to create, draw, paint, or render an image — or to change what an existing one shows (a generative edit of it).
- "vision": the user attached an image and wants it analyzed, described, or understood.
- "video": the user asks to create, animate, extend, restyle, or render a video or short clip.
- "audio": the user asks to GENERATE a new audio clip — speak/narrate text, create music or a sound effect, or extend an audio clip.
Set mediaEdit true when the user asks to EDIT an existing clip instead of generating new media: grab a frame/screenshot of a video, split/trim/cut a segment, join/concatenate clips, crop/resize/rotate a clip (e.g. to a 16:9 shape or to fit a size limit), convert a clip between aspect ratios (e.g. 16:9 to 9:16 vertical for Reels/Shorts — transform_video reaches the new shape locally by cropping or by filling the added canvas with a blurred copy or black bars, while reframe_video generates new scene content for the added area), change a clip's playback speed (speed it up, slow motion, or speed up just a portion of it), extract the audio track, or put different audio under a video. The responseMode for these stays "text" — the edited clip is attached to the reply, not generated. When one of the edit tools (screenshot_video, split_video, join_videos, transform_video, reframe_video, extract_audio, replace_audio) is listed under Available tools, set needsTools true and describe the edit in toolTask; when none is listed, set needsTools false — the harness itself tells the user how to enable editing. RESTYLING is not one of these: changing a clip's look — its art style (anime, claymation, watercolor), its characters' appearance, its visual treatment — keeps the motion but re-renders every frame, which is generation, so choose responseMode "video" (with needsTools true when restyle_video is listed), never mediaEdit.
Set imageEdit true when the user asks to EDIT an existing image instead of generating new ones: convert its format (including iPhone HEIC photos), resize or crop it (e.g. to a 1:1/4:5/9:16/16:9 shape), rotate or flip it, add a watermark or logo overlay, combine several images into a collage/grid, adjust colors (brightness, contrast, saturation, grayscale, sepia), or strip metadata / shrink it for sharing. The responseMode stays "text" — the edited image is attached to the reply. When one of the image tools (convert_image, transform_image, compose_images, adjust_image, optimize_image) is listed under Available tools, set needsTools true and describe the edit in toolTask; when none is listed, set needsTools false — the harness itself tells the user how to enable local image editing. CONTENT CHANGES are not one of these: altering what the image shows — retouching a subject, changing its proportions (a smaller head, a longer neck), removing or adding an element, switching the outfit, hairstyle, or art style — re-renders the image, which is generation, so choose responseMode "image" with needsTools true (generate_image takes the attached image as its edit reference), never imageEdit and never "text" because the local tools cannot do it (conv_d53a86bd51bd5740ed30e683: "make the head and neck a bit smaller" was routed to text on the theory that image tools only transform whole images, and the reply then asked for the image the user had attached).
When the latest user message begins with "[Attachments: ...]", the user attached that media to the turn — treat it as available to tools that require it (e.g. lip_sync needs an audio clip plus a face image or video, transcribe_audio needs an audio clip, extend_audio can extend an attached audio clip, generate_image can edit an attached image, generate_video can animate an attached image or extend an attached video). When the message carries an "[Available media: ...]" note, tools can additionally reach that media even though nothing is attached — it was resolved from @-mentioned assets or the conversation's recent artifacts, so a bare "extend this video", "continue the clip", or "animate it" refers to it and belongs in the matching media mode with needsTools true, never in "text" as feedback or description (conv_a90d8a8fec4a33e58e635b37: "Extend this video" after an earlier generation was routed to text because the routing model could not see that any video existed). The note only reports that the media exists and tools can reach it — it does not by itself mean the user wants new media.%s
Set needsTools true only when answering requires acting on the workspace or a listed capability: reading, listing, searching, or writing files, running a command, generating an image, generating a video, generating audio, or following one of the listed skills.
Set needsTools false when your own knowledge is enough: greetings, general knowledge, reasoning, writing, and conversation about content already visible in the chat.
For responseMode "image", set needsTools true so the harness can run the generate_image tool before the primary model responds.
For responseMode "video", set needsTools true when the generate_video tool is listed so the harness can run it before the primary model responds. A restyle of an attached or recent clip — changing its look: art style, characters' appearance, visual treatment — is the restyle_video tool, so set needsTools true when restyle_video is listed instead; the deliverable is the restyled clip, not prose. When the user wants a video but generate_video is NOT listed, still set responseMode "video" and set needsTools false — Atelier itself tells the user how to enable video generation. Never collapse a video request to "text" because the tool is missing: describing the clip as if it were made, or claiming it was queued or is rendering, is the failure this prevents (conv_20a0df2b2db9b4e9ea5a1ad9). An attached or recent video plus a request to extend, continue, or lengthen it is video mode too — generate_video continues the clip (there is no separate extend_video tool), fetching the source from conversation history when it is not attached; do not route it to "text" and describe the extension as if it happened (conv_c9a17b4c860500ddff15984f: a small harness model invented a missing "extend_video" tool, skipped tools entirely, and the primary model then claimed in prose that the clip had been extended).
For responseMode "audio", set needsTools true when the generate_speech, generate_sound, or extend_audio tool is listed so the harness can run it before the primary model responds. When the user wants generated audio but none of those tools is listed, still set responseMode "audio" and set needsTools false — Atelier itself tells the user how to enable audio generation; do not fall back to "text" and describe the audio as if it were produced. An attached or recent audio clip plus a request to extend, continue, or lengthen it is audio mode even when the user describes nothing about the addition — extend_audio works from just a length; do not route it to "text" to ask what the added audio should sound like. Transcribing, captioning, or timestamping an EXISTING clip is never "audio" mode — the deliverable is text, so choose "text" and let transcribe_audio produce it (conv_f70468e4 and successors: small models kept misrouting "transcribe with timestamps" here because the request is about audio).
Available tools:
%s
Available skills:
%s
Workspace root: %s`, narrationRouting, registry.PromptCatalog(), skills, workspaceRoot))
}
