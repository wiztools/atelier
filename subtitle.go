package main

// Pure-Go subtitle conversion: WebVTT ↔ SubRip (SRT), no ffmpeg, CLI, or API
// key behind it. transcribe_audio persists timestamped transcripts as .vtt
// artifacts; convert_subtitles turns those (or an existing .srt) into the
// other format as a new artifact on the reply.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	subtitleFormatVTT = "vtt"
	subtitleFormatSRT = "srt"
)

// subtitleCue is one timed caption block: seconds since the start, and the
// caption text (possibly multi-line, tags included).
type subtitleCue struct {
	Start float64
	End   float64
	Text  string
}

// vttTagPattern strips the VTT-only inline tags an SRT player can't use:
// class/voice/language/ruby tags (with their dotted class suffixes) and the
// karaoke timestamp tags. It deliberately spares <i>/<b>/<u>, which both
// formats tolerate.
var vttTagPattern = regexp.MustCompile(`(?i)</?(?:c|v|lang|ruby|rt)(?:\.[^\s>/]+)*(?:\s[^>]*)?>|<\d{1,2}:\d{2}(?::\d{2})?(?:\.\d{1,3})?>`)

// detectSubtitleFormat sniffs the container from the first non-blank line —
// WebVTT opens with a WEBVTT signature; anything else is treated as SubRip.
func detectSubtitleFormat(data []byte) string {
	text := strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
	return map[bool]string{true: subtitleFormatVTT, false: subtitleFormatSRT}[strings.HasPrefix(text, "WEBVTT")]
}

// parseSubtitles reads either format into cues, leniently: block structure
// comes from blank lines, the cue timing line is located by "-->" (identifier
// lines before it are dropped), and blocks that carry no parseable timing are
// skipped and counted. Cue settings after the end timestamp are ignored by
// parseVTTTimings (local_tools.go), which parses both formats' clock values.
func parseSubtitles(data []byte) (cues []subtitleCue, format string, skipped int, err error) {
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\uFEFF"), "\r\n", "\n")
	format = detectSubtitleFormat(data)
	for _, block := range splitSubtitleBlocks(text) {
		lines := strings.Split(block, "\n")
		first := strings.TrimSpace(lines[0])
		// A WEBVTT header usually sits in its own block; when no blank line
		// follows it, drop just the header line and keep parsing the block.
		if strings.HasPrefix(first, "WEBVTT") && len(lines) > 1 {
			lines = lines[1:]
			first = strings.TrimSpace(lines[0])
		}
		if strings.HasPrefix(first, "WEBVTT") || strings.HasPrefix(first, "NOTE") ||
			strings.HasPrefix(first, "STYLE") || strings.HasPrefix(first, "REGION") {
			continue
		}
		timing := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timing = i
				break
			}
		}
		if timing < 0 {
			if first != "" {
				skipped++
			}
			continue
		}
		timingLine := lines[timing]
		if format == subtitleFormatSRT {
			// SRT writes comma decimals; the clock parser reads dots. An SRT
			// timing line carries nothing but the two timestamps (no VTT cue
			// settings), so the swap is safe.
			timingLine = strings.ReplaceAll(timingLine, ",", ".")
		}
		start, end, ok := parseVTTTimings(timingLine)
		if !ok {
			skipped++
			continue
		}
		cueText := strings.Join(lines[timing+1:], "\n")
		if strings.TrimSpace(cueText) == "" {
			skipped++
			continue
		}
		if end < start {
			end = start
		}
		cues = append(cues, subtitleCue{Start: start, End: end, Text: strings.TrimRight(cueText, " \t")})
	}
	if len(cues) == 0 {
		return nil, format, skipped, errors.New("no subtitle cues found — the file does not look like VTT or SRT subtitle content")
	}
	return cues, format, skipped, nil
}

// splitSubtitleBlocks cuts subtitle text into cue blocks at blank lines.
func splitSubtitleBlocks(text string) []string {
	blocks := make([]string, 0, 16)
	current := make([]string, 0, 8)
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
				current = current[:0]
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	return blocks
}

// formatSRTTimestamp renders seconds as SRT's HH:MM:SS,mmm — comma decimal,
// hours always present.
func formatSRTTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	millis := int64(seconds*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", millis/3600000, (millis/60000)%60, (millis/1000)%60, millis%1000)
}

// renderSubtitlesSRT lays cues out in SubRip: sequential numbering (SRT
// requires it; VTT identifiers are dropped), comma-decimal timestamps, and
// VTT-only inline tags stripped.
func renderSubtitlesSRT(cues []subtitleCue) string {
	var builder strings.Builder
	for i, cue := range cues {
		text := vttTagPattern.ReplaceAllString(cue.Text, "")
		text = strings.TrimRight(text, " \t")
		if text == "" {
			text = strings.TrimSpace(cue.Text)
		}
		fmt.Fprintf(&builder, "%d\n%s --> %s\n%s\n\n", i+1, formatSRTTimestamp(cue.Start), formatSRTTimestamp(cue.End), text)
	}
	return builder.String()
}

// renderSubtitlesVTT lays cues out as WebVTT. renderTranscriptVTT already
// writes exactly this shape (header, dot-decimal clocks, no identifiers) from
// transcript chunks, so the cues ride it through the chunk shape.
func renderSubtitlesVTT(cues []subtitleCue) string {
	chunks := make([]transcriptChunk, len(cues))
	for i, cue := range cues {
		end := cue.End
		chunks[i] = transcriptChunk{Start: cue.Start, End: &end, Text: cue.Text}
	}
	return renderTranscriptVTT(chunks)
}

// convertSubtitles parses subtitle bytes and re-renders them in the target
// format. Converting a file to its own format is allowed and normalizes it
// (strict timestamps, sequential numbering), with a notice saying so.
func convertSubtitles(data []byte, target string) (out []byte, source string, cueCount int, notices []string, err error) {
	cues, source, skipped, err := parseSubtitles(data)
	if err != nil {
		return nil, source, 0, nil, err
	}
	if skipped > 0 {
		notices = append(notices, fmt.Sprintf("%d malformed or empty subtitle block%s skipped.", skipped, map[bool]string{true: "s were", false: " was"}[skipped != 1]))
	}
	if source == target {
		notices = append(notices, fmt.Sprintf("The source was already %s; it was re-emitted with normalized timestamps and numbering.", subtitleFormatLabel(target)))
	}
	var rendered string
	if target == subtitleFormatSRT {
		rendered = renderSubtitlesSRT(cues)
	} else {
		rendered = renderSubtitlesVTT(cues)
	}
	return []byte(rendered), source, len(cues), notices, nil
}

// subtitleFormatLabel names a format identifier for evidence and notices.
func subtitleFormatLabel(format string) string {
	return map[string]string{subtitleFormatVTT: "WebVTT", subtitleFormatSRT: "SubRip (SRT)"}[format]
}

// ToolConvertSubtitlesResult carries a subtitle conversion: what was read,
// what was written, and the converted file as a temp-file artifact. The file
// rides the Transcripts slice deliberately — the transcript-artifact plumbing
// (persistence as trc_<hex>.<ext>, reply attachment chips, temp cleanup) is
// exactly what a converted subtitle file needs, and the transcript slot is
// where transcriptsFromToolResults collects it from.
type ToolConvertSubtitlesResult struct {
	SourceFormat string               `json:"sourceFormat"`
	TargetFormat string               `json:"targetFormat"`
	CueCount     int                  `json:"cueCount"`
	Transcripts  []ToolTranscriptFile `json:"transcripts,omitempty"`
	Notices      []string             `json:"notices,omitempty"`
}

// ToolNotices reports deterministic, user-facing caveats produced while
// converting (e.g. malformed cue blocks that were skipped).
func (r ToolConvertSubtitlesResult) ToolNotices() []string { return r.Notices }

// convertSubtitlesToolDefinition exposes the convert_subtitles tool: pure-Go
// WebVTT ↔ SubRip conversion over the conversation's subtitle artifacts. Like
// open_editor it is unconditional — no keys, CLI, or model behind it — and it
// is deliberately NOT a mediaEdit: it runs with no ffmpeg installed, so the
// media-edit install note would be false beside it.
func convertSubtitlesToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "convert_subtitles",
		Title:       "Convert subtitles",
		Description: convertSubtitlesDescription(),
		Example:     `{"name":"convert_subtitles","format":"srt"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: convertSubtitlesParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			switch strings.ToLower(strings.TrimSpace(call.Format)) {
			case "", subtitleFormatSRT, subtitleFormatVTT:
				return nil
			default:
				return []string{prefix + `.format must be "srt" or "vtt"`}
			}
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			return executeConvertSubtitles(tools, call)
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			// The conversion consumes nothing — no media kind, no cost, no
			// model — so the bare projection carries just the direction.
			if typed, ok := result.Result.(ToolConvertSubtitlesResult); ok {
				activity.Command = []string{"convert_subtitles", typed.SourceFormat + "->" + typed.TargetFormat}
			}
			return activity
		},
	}
}

func convertSubtitlesDescription() string {
	return "Converts a subtitle file between WebVTT (.vtt) and SubRip (.srt) — for example turning the timestamped transcript a previous transcribe_audio turn produced into an SRT file. Runs entirely locally (no ffmpeg or API key). The source is the conversation's newest .vtt/.srt artifact, the subtitle file an earlier call in this same turn produced (e.g. a transcribe_audio in the same plan), or the artifact named by artifactId. The converted file is attached to the reply as a downloadable artifact."
}

// convertSubtitlesParamSchema mirrors the Validate rules for the native
// function-calling declaration.
func convertSubtitlesParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"format":     enumParam(`Target subtitle format — "srt" (SubRip) or "vtt" (WebVTT). Defaults to the opposite of the source file's format.`, subtitleFormatSRT, subtitleFormatVTT),
			"artifactId": stringParam(`Optional — the id of a specific transcript artifact in this conversation to convert (e.g. "trc_ab12cd34"). Defaults to the newest .vtt/.srt artifact, or the subtitle file an earlier call in this turn produced.`),
		},
		"required": []string{},
	}
}

// executeConvertSubtitles resolves the source subtitle file (this turn's
// in-flight artifacts first, then the persisted record), converts it, and
// stages the output as a transcript-file artifact the harness persists.
func executeConvertSubtitles(tools HarnessToolExecutionContext, call HarnessToolCall) (ToolConvertSubtitlesResult, string, error) {
	data, sourceFormat, sourceLabel, err := resolveSubtitleSource(tools, "convert_subtitles", strings.TrimSpace(call.ArtifactID))
	if err != nil {
		return ToolConvertSubtitlesResult{}, "subtitle conversion failed", err
	}
	target, err := normalizeSubtitleTargetFormat(call.Format, sourceFormat)
	if err != nil {
		return ToolConvertSubtitlesResult{}, "subtitle conversion failed", err
	}
	out, _, cueCount, notices, err := convertSubtitles(data, target)
	if err != nil {
		return ToolConvertSubtitlesResult{}, "subtitle conversion failed", err
	}
	tempPath, err := writeTempMediaBytes(out, "atelier-subtitles-*", "."+target)
	if err != nil {
		return ToolConvertSubtitlesResult{}, "subtitle conversion failed", fmt.Errorf("staging the converted subtitles failed: %w", err)
	}
	mimeType := map[string]string{subtitleFormatVTT: "text/vtt", subtitleFormatSRT: "application/x-subrip"}[target]
	extension := transcriptExtensionForMimeType(mimeType)
	return ToolConvertSubtitlesResult{
		SourceFormat: sourceFormat,
		TargetFormat: target,
		CueCount:     cueCount,
		Transcripts:  []ToolTranscriptFile{{TempPath: tempPath, MimeType: mimeType}},
		Notices:      notices,
	}, fmt.Sprintf("Converted %d cues from %s to %s; the %s file (%s) is attached to the reply.", cueCount, subtitleFormatLabel(sourceFormat), subtitleFormatLabel(target), strings.TrimPrefix(extension, "."), sourceLabel), nil
}

// normalizeSubtitleTargetFormat resolves the requested target against the
// source: an empty request flips the source format, an explicit request must
// name a known format (same-format conversion normalizes, not errors).
func normalizeSubtitleTargetFormat(requested, sourceFormat string) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "":
		if sourceFormat == subtitleFormatSRT {
			return subtitleFormatVTT, nil
		}
		return subtitleFormatSRT, nil
	case subtitleFormatSRT, subtitleFormatVTT:
		return requested, nil
	default:
		return "", fmt.Errorf(`convert_subtitles format must be "srt" or "vtt", got %q`, requested)
	}
}

// resolveSubtitleSource reads the source subtitle bytes. In-turn artifacts
// come first (a transcribe_audio or convert_subtitles call earlier this turn —
// its temp file is not persisted, and thus invisible to the record walk, until
// the turn saves); the persisted record walk covers every earlier turn. The
// returned label names the source for the evidence summary (an artifact id, or
// the temp file's base name for in-flight files). toolName names the calling
// tool in the error messages (convert_subtitles and burn_subtitles share the
// ladder).
func resolveSubtitleSource(tools HarnessToolExecutionContext, toolName, wantArtifactID string) ([]byte, string, string, error) {
	if wantArtifactID == "" {
		// Newest first — and subtitle files only: a timestamped transcription
		// can stage BOTH a .vtt and an oversized plain .txt (see
		// transcribeAudioOutput), and only the .vtt converts.
		for i := len(tools.TranscriptFiles) - 1; i >= 0; i-- {
			file := tools.TranscriptFiles[i]
			extension := transcriptExtensionForMimeType(file.MimeType)
			if extension != ".vtt" && extension != ".srt" {
				continue
			}
			data, err := os.ReadFile(file.TempPath)
			if err != nil {
				return nil, "", "", fmt.Errorf("reading the turn's subtitle file failed: %w", err)
			}
			return data, subtitleFormatForExtension(extension), filepath.Base(file.TempPath), nil
		}
	}
	if strings.TrimSpace(tools.ConversationID) == "" {
		return nil, "", "", errors.New(toolName + " reads a conversation's subtitle artifacts, and no conversation is attached to this call")
	}
	content, extension, err := newestSubtitleArtifact(tools.Storage, toolName, tools.ConversationID, wantArtifactID)
	if err != nil {
		return nil, "", "", err
	}
	path, err := contentArtifactPath(tools.Storage, tools.ConversationID, *content)
	if err != nil {
		return nil, "", "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", "", fmt.Errorf("reading the subtitle artifact failed: %w", err)
	}
	return data, subtitleFormatForExtension(extension), content.ArtifactID, nil
}

// subtitleFormatForExtension maps .vtt/.srt onto the format identifiers; an
// empty string for anything else (the caller never passes one).
func subtitleFormatForExtension(extension string) string {
	return map[string]string{".vtt": subtitleFormatVTT, ".srt": subtitleFormatSRT}[extension]
}

// newestSubtitleArtifact finds the artifact a subtitle-consuming call should
// read: the newest turn's first transcript content entry carrying a VTT or SRT
// artifact — the same newest-turn-first walk newestEditorArtifact uses, so
// "the subtitles" and "the transcript" resolve to the same asset. Text (.txt)
// transcripts are not subtitle files and never match. A non-empty wantID
// matches that artifact id exactly. toolName names the calling tool in the
// error messages.
func newestSubtitleArtifact(storage ConfigStorage, toolName, conversationID, wantID string) (*HistoryContent, string, error) {
	detail, err := getConversation(storage, conversationID)
	if err != nil {
		return nil, "", err
	}
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		for j := range detail.Turns[i].Content {
			content := detail.Turns[i].Content[j]
			extension := subtitleArtifactExtension(content)
			if extension == "" {
				continue
			}
			if wantID != "" && content.ArtifactID != wantID {
				continue
			}
			return &content, extension, nil
		}
	}
	if wantID != "" {
		return nil, "", fmt.Errorf("%s found no VTT or SRT artifact with id %q in this conversation — transcript artifacts carrying subtitle content are what it can read", toolName, wantID)
	}
	return nil, "", errors.New(toolName + " found no subtitle artifact (.vtt or .srt) in this conversation — a transcribe_audio call with timestamps produces one, or the user can attach a subtitle file first")
}

// subtitleArtifactExtension reports ".vtt"/".srt" when a history content entry
// is a transcript artifact holding subtitle content. The path extension is the
// direct identity (writeChatMediaArtifacts derives it from the staged mime);
// the mime mapping covers any entry whose path is missing.
func subtitleArtifactExtension(content HistoryContent) string {
	if content.Type != "transcript" {
		return ""
	}
	if extension := strings.ToLower(filepath.Ext(content.Path)); extension == ".vtt" || extension == ".srt" {
		return extension
	}
	extension := transcriptExtensionForMimeType(content.MimeType)
	if extension == ".vtt" || extension == ".srt" {
		return extension
	}
	return ""
}
