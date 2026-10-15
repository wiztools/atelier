package main

// Fountain: the plaintext screenplay format — the "Markdown for scripts"
// dialect of the document editor. fountain.go owns the parser and the review
// checklist, mirroring the frontend's fountainModel.ts line for line (the
// subtitle pairing inverted: here the frontend needs the live parse for the
// preview, the outline, and the Tab/Enter element model, while the Go side
// needs it only when an export renders — the Go parse is the export's source
// of truth, the TS parse the editing session's). The parser is generous by
// contract: every line resolves to SOME element (unmarked lines default to
// action), so a save never fails on content — same posture as
// writeMarkdownAttachment.

import (
	"fmt"
	"strings"
	"unicode"
)

// Element kinds carry Final Draft's paragraph-type names verbatim — the .fdx
// writer maps them identity-wise.
const (
	fountainScene         = "Scene Heading"
	fountainAction        = "Action"
	fountainCharacter     = "Character"
	fountainParenthetical = "Parenthetical"
	fountainDialogue      = "Dialogue"
	fountainTransition    = "Transition"
	fountainLyric         = "Lyric"
	fountainSection       = "Section"
	fountainSynopsis      = "Synopsis"
	fountainPageBreak     = "Page Break"
	fountainCentered      = "Centered"
)

// FountainElement is one parsed line. Line is 1-based and points at the
// source line the element came from (the frontend outline and warnings click
// through to it). Character names the speech block a dialogue or
// parenthetical answers to — the cue with any extension ((CONT'D) etc.)
// stripped.
type FountainElement struct {
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Line      int    `json:"line"`
	Character string `json:"character,omitempty"`
}

// Document formats of the markdown editor's two dialects. The format names
// how a document SAVES (the artifact extension: .md / .fountain) and which
// preview and editing model the editor drives — the content itself is plain
// text either way, and switching format at save time is the conversion path.
const (
	documentFormatMarkdown = "markdown"
	documentFormatFountain = "fountain"
)

// fountainTitlePage is the raw key/value block a script may open with. Keys
// keep their source spelling ("Draft date"); the PDF/FDX writers render the
// keys they know in a fixed order and ignore the rest.
func fountainParse(data []byte) ([]FountainElement, map[string]string) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	elements := []FountainElement{}
	titlePage := map[string]string{}

	i := 0
	// Title page: `Key: value` lines from the very top, values continuing on
	// indented lines, the whole block ended by the first blank line. A
	// document that starts with dialogue text simply fails the key pattern
	// and the body parse begins untouched.
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			i++
			break
		}
		key, value, ok := fountainTitleKey(line)
		if !ok {
			break
		}
		i++
		for j := i; j < len(lines); j++ {
			cont := lines[j]
			if strings.TrimSpace(cont) == "" || (cont[0] != ' ' && cont[0] != '\t') {
				break
			}
			value += " " + strings.TrimSpace(cont)
			i = j + 1
		}
		titlePage[key] = strings.TrimSpace(value)
		i--
	}

	expectSpeech := false
	speaker := ""
	for ; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		lineNo := i + 1
		if trimmed == "" {
			expectSpeech = false
			continue
		}
		// Forced and marker forms first — they win over the pattern rules
		// regardless of context.
		switch {
		case trimmed == "===" || (strings.Trim(trimmed, "=") == "" && len(trimmed) >= 3):
			elements = append(elements, FountainElement{Kind: fountainPageBreak, Text: trimmed, Line: lineNo})
			expectSpeech = false
		case strings.HasPrefix(trimmed, ">") && strings.HasSuffix(trimmed, "<") && len(trimmed) > 1:
			elements = append(elements, FountainElement{Kind: fountainCentered, Text: strings.TrimSpace(trimmed[1 : len(trimmed)-1]), Line: lineNo})
			expectSpeech = false
		case strings.HasPrefix(trimmed, ">"):
			elements = append(elements, FountainElement{Kind: fountainTransition, Text: strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), Line: lineNo})
			expectSpeech = false
		case strings.HasPrefix(trimmed, ".") && len(trimmed) > 1 && trimmed[1] != '.':
			elements = append(elements, FountainElement{Kind: fountainScene, Text: strings.TrimSpace(strings.TrimPrefix(trimmed, ".")), Line: lineNo})
			expectSpeech = false
		case strings.HasPrefix(trimmed, "@"):
			cue := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
			elements = append(elements, FountainElement{Kind: fountainCharacter, Text: cue, Line: lineNo})
			expectSpeech = true
			speaker = fountainCharacterName(cue)
		case strings.HasPrefix(trimmed, "!"):
			elements = append(elements, FountainElement{Kind: fountainAction, Text: strings.TrimSpace(strings.TrimPrefix(trimmed, "!")), Line: lineNo})
			expectSpeech = false
		case strings.HasPrefix(trimmed, "#") && fountainIsSection(trimmed):
			elements = append(elements, FountainElement{Kind: fountainSection, Text: strings.TrimSpace(strings.TrimLeft(trimmed, "# ")), Line: lineNo})
			expectSpeech = false
		case strings.HasPrefix(trimmed, "="):
			elements = append(elements, FountainElement{Kind: fountainSynopsis, Text: strings.TrimSpace(strings.TrimPrefix(trimmed, "=")), Line: lineNo})
			expectSpeech = false
		case strings.HasPrefix(trimmed, "~"):
			elements = append(elements, FountainElement{Kind: fountainLyric, Text: strings.TrimSpace(strings.TrimPrefix(trimmed, "~")), Line: lineNo})
			expectSpeech = false
		// Pattern rules apply only to blank-preceded lines: a speech block
		// keeps consuming whatever the screenwriter types as speech, so an
		// uppercase shout inside dialogue stays dialogue.
		case !expectSpeech && fountainIsSceneHeading(trimmed):
			elements = append(elements, FountainElement{Kind: fountainScene, Text: trimmed, Line: lineNo})
		case !expectSpeech && fountainIsTransitionCue(trimmed):
			elements = append(elements, FountainElement{Kind: fountainTransition, Text: trimmed, Line: lineNo})
		case !expectSpeech && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" && fountainIsCharacterCue(trimmed):
			elements = append(elements, FountainElement{Kind: fountainCharacter, Text: trimmed, Line: lineNo})
			expectSpeech = true
			speaker = fountainCharacterName(trimmed)
		case expectSpeech && strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")") && len(trimmed) > 1:
			elements = append(elements, FountainElement{Kind: fountainParenthetical, Text: trimmed, Line: lineNo, Character: speaker})
		case expectSpeech:
			elements = append(elements, FountainElement{Kind: fountainDialogue, Text: trimmed, Line: lineNo, Character: speaker})
		default:
			elements = append(elements, FountainElement{Kind: fountainAction, Text: trimmed, Line: lineNo})
			expectSpeech = false
		}
	}
	return elements, titlePage
}

// fountainTitleKeys are the standard title-page keys (compared
// case-insensitively) — the guard that keeps "CUT TO:" (or any uppercase
// colon-ending line a screenplay legitimately contains) from being eaten as
// a title page when it opens a script.
var fountainTitleKeys = map[string]bool{
	"title": true, "credit": true, "author": true, "authors": true,
	"source": true, "draft date": true, "contact": true, "copyright": true, "notes": true,
}

// fountainTitleKey splits a `Key: value` title-page line. Keys are letters,
// spaces, and digits up to the first colon, and must be one of the standard
// title-page keys; anything else is body text.
func fountainTitleKey(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	colon := strings.Index(trimmed, ":")
	if colon <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(trimmed[:colon])
	for _, r := range key {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' {
			return "", "", false
		}
	}
	if !fountainTitleKeys[strings.ToLower(key)] {
		return "", "", false
	}
	return key, strings.TrimSpace(trimmed[colon+1:]), true
}

// fountainIsSceneHeading names the INT/EXT family: a slug line prefix
// followed by a separator (space, period, dash, colon) or end of line, so
// "INTERRUPTED" stays action.
func fountainIsSceneHeading(line string) bool {
	upper := strings.ToUpper(line)
	for _, prefix := range []string{"INT.", "EXT.", "INT ", "EXT ", "EST ", "INT./EXT.", "EXT./INT.", "INT/EXT", "EXT/INT", "I/E ", "I/E."} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return upper == "INT" || upper == "EXT" || upper == "EST" || upper == "I/E"
}

// fountainIsTransitionCue matches the uppercase TO: convention ("CUT TO:",
// "SMASH CUT TO:"). Anything else uppercase-but-not-a-transition needs the
// `> ` force.
func fountainIsTransitionCue(line string) bool {
	if !strings.HasSuffix(line, "TO:") || len(line) < 4 {
		return false
	}
	return fountainIsUppercaseLine(strings.TrimSuffix(line, "TO:"))
}

// fountainIsCharacterCue matches the uppercase-followed-by-speech rule: the
// line's letters are all uppercase (digits and punctuation ride along —
// "INT. EXT." was already claimed by the scene rule, "2ND MAN" is legal) and
// the caller has checked a non-blank line follows.
func fountainIsCharacterCue(line string) bool {
	return fountainIsUppercaseLine(line)
}

func fountainIsUppercaseLine(line string) bool {
	letters := false
	for _, r := range line {
		if unicode.IsLetter(r) {
			letters = true
			if !unicode.IsUpper(r) {
				return false
			}
		}
	}
	return letters
}

// fountainIsSection checks the `#` marker forms: one to six hashes followed
// by a space (or bare hashes at line end), so "#tag" stays action.
func fountainIsSection(line string) bool {
	hashes := 0
	for _, r := range line {
		if r == '#' {
			hashes++
			continue
		}
		break
	}
	if hashes == 0 || hashes > 6 || hashes >= len(line) {
		return false
	}
	return line[hashes] == ' ' || line[hashes] == '\t'
}

// fountainCharacterName strips a cue's extension — (CONT'D), (V.O.), (O.S.)
// and friends — so "SUBHASH (V.O.)" and "SUBHASH" resolve to one character
// for the duplicate-name warning and the speech attribution.
func fountainCharacterName(cue string) string {
	name := strings.TrimSpace(cue)
	for {
		open := strings.LastIndex(name, "(")
		if open < 0 {
			break
		}
		close := strings.LastIndex(name, ")")
		if close < open {
			break
		}
		extension := strings.ToUpper(strings.TrimSpace(name[open+1 : close]))
		extension = strings.ReplaceAll(extension, "’", "'")
		switch extension {
		case "CONT'D", "CONT’D", "V.O.", "O.S.", "O.C.", "PRE-LAP", "FILTERED", "ON SCREEN", "OFF SCREEN":
			name = strings.TrimSpace(name[:open])
		default:
			// A parenthetical that is not a known extension is part of the
			// cue (legal, rare) — keep it.
			return name
		}
	}
	return name
}

// fountainWarnings is the review checklist — the Go twin of the frontend
// model's checklist, computed from the parsed elements rather than raw lines
// so the context rules (what counts as a speech block) are the parser's own.
func fountainWarnings(elements []FountainElement) []string {
	warnings := []string{}
	scenes := 0
	names := map[string]string{}
	dupes := map[string]bool{}
	for i, element := range elements {
		if element.Kind == fountainScene {
			scenes++
			continue
		}
		if element.Kind != fountainCharacter {
			continue
		}
		name := fountainCharacterName(element.Text)
		key := strings.ToLower(name)
		if first, ok := names[key]; ok && first != name && !dupes[key] {
			warnings = append(warnings, fmt.Sprintf("line %d: character %q is also spelled %q — pick one spelling", element.Line, name, first))
			dupes[key] = true
		} else if !ok {
			names[key] = name
		}
		if i+1 < len(elements) && elements[i+1].Kind == fountainCharacter {
			warnings = append(warnings, fmt.Sprintf("line %d: the cue %q has no dialogue after it", element.Line, element.Text))
		}
	}
	if scenes == 0 && len(elements) > 0 {
		warnings = append(warnings, "no scene headings — an INT. or EXT. line marks where each scene starts")
	}
	return warnings
}
