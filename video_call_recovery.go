package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Recovering what a weak planner drops from a generate_video call. The
// planner authors the paid call, but it is not the only record of what the
// user asked for: triage's toolTask is the harness's own routing note, and
// the user's message is the request itself. conv_efa4b078cd9a24df88d76203
// lost both the stated length and the whole brief in one call — triage wrote
// duration:"6", the planner (google/gemma-4-31b-it) sent no duration and a
// generic "seamless transition" sentence in place of a beat-by-beat brief,
// the executor filled the length with the configured 10s, and the reply
// described beats the paid clip was never asked for. The keyframe-role
// recovery (triageNamesKeyframes) is the same family.

// triageDurationPattern reads the contract spelling the triage prompt teaches
// ("duration:\"6\"", "duration: 8", "\"duration\":\"5s\"", "duration=4.5").
// Prose like "a 6 second clip" deliberately does not match: the user's own
// message is full of beat timings ("0–1 sec.") a free-text sniff would
// misread, and triage's note names the parameter when it means it.
var triageDurationPattern = regexp.MustCompile(`(?i)\bduration"?\s*[:=]\s*["']?(\d+(?:\.\d+)?)\s*s?\b`)

// triageDurationSeconds returns the clip length triage's toolTask names, as
// the decimal string generate_video's duration takes, or "" when the note
// names none (or a non-positive one).
func triageDurationSeconds(task string) string {
	match := triageDurationPattern.FindStringSubmatch(task)
	if match == nil {
		return ""
	}
	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || seconds <= 0 {
		return ""
	}
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}

const (
	// briefRestoreMinWords is the size of request worth protecting: below it
	// the user's words are an idea the planner is expected to elaborate ("make
	// a video of a cat jumping"), and low overlap is the elaboration, not a loss.
	briefRestoreMinWords = 25
	// briefRestoreMaxRecall is the share of the request's distinctive words a
	// prompt must carry to count as the user's brief. A faithful condensation
	// keeps the subjects and directions (well over half); the conv's generic
	// sentence carried about one word in twenty.
	briefRestoreMaxRecall = 0.2
	// briefRestoreMaxChars bounds what the restore may send: Kling o1's
	// 2500-character prompt cap is the smallest known (conv_51b85fd58d6818d38b3fe5aa),
	// and a brief past it is one the planner was right to condense.
	briefRestoreMaxChars = 2500
)

var (
	briefMentionPattern = regexp.MustCompile(`@[A-Za-z0-9_]+`)
	briefWordPattern    = regexp.MustCompile(`[a-z]+`)
	briefStopwords      = map[string]bool{
		"about": true, "after": true, "again": true, "also": true, "back": true, "been": true,
		"before": true, "both": true, "each": true, "from": true, "have": true, "into": true,
		"just": true, "keep": true, "like": true, "make": true, "more": true, "only": true,
		"over": true, "same": true, "should": true, "some": true, "such": true, "than": true,
		"that": true, "their": true, "them": true, "then": true, "there": true, "these": true,
		"they": true, "this": true, "those": true, "through": true, "very": true, "want": true,
		"were": true, "what": true, "when": true, "where": true, "while": true, "will": true,
		"with": true, "your": true, "please": true, "create": true, "video": true, "clip": true,
	}
)

// briefWords returns the distinctive words of text: lowercase, four letters
// or more, stopwords out, each counted once.
func briefWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range briefWordPattern.FindAllString(strings.ToLower(text), -1) {
		if len(word) >= 4 && !briefStopwords[word] {
			words[word] = true
		}
	}
	return words
}

// cleanUserBrief readies the user's message to ride as a generation prompt:
// @-mention tokens resolve to attached media the generation model already
// receives and cannot read as text, so they drop, and the whitespace they
// leave collapses line by line.
func cleanUserBrief(text string) string {
	lines := strings.Split(briefMentionPattern.ReplaceAllString(text, ""), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	cleaned := strings.Join(lines, "\n")
	for strings.Contains(cleaned, "\n\n\n") {
		cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(cleaned)
}

// restoredUserBrief returns the user's own request to send in place of the
// planner's prompt when that prompt dropped the brief — a substantial request
// (briefRestoreMinWords) whose distinctive words the prompt mostly lacks
// (briefRestoreMaxRecall) — or "" when the prompt should stand. A brief past
// briefRestoreMaxChars never restores. The planner's prompt is the target of
// the prompt-completeness rule (plannerMediaRoutingGuidance); this is its
// executor-side floor, since the paid model sees only the call.
func restoredUserBrief(prompt, userRequest string) string {
	brief := cleanUserBrief(userRequest)
	if brief == "" || len(brief) > briefRestoreMaxChars {
		return ""
	}
	wanted := briefWords(brief)
	if len(wanted) < briefRestoreMinWords {
		return ""
	}
	carried := briefWords(prompt)
	kept := 0
	for word := range wanted {
		if carried[word] {
			kept++
		}
	}
	if float64(kept)/float64(len(wanted)) >= briefRestoreMaxRecall {
		return ""
	}
	return brief
}
