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
// keys they know in a fixed order and ignore the rest. A value continued on
// indented lines keeps those line breaks ("\n"-joined) — a Contact block is
// an address, one fragment per line, and a title may break deliberately.
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
			value += "\n" + strings.TrimSpace(cont)
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

// fountainSpaceBefore is how many blank lines open an element in the
// formatted script — the PDF prints them as real blank lines and the preview
// as margins, so the screen and the page space a script identically. A scene
// heading takes two (the preview's larger gap above sluglines); lines the
// source keeps together — consecutive action lines, a cue and its
// parentheticals, dialogue and lyrics, consecutive lyric or centered lines —
// take none, being the line breaks of one paragraph; everything else takes
// one. prev is nil at the top of the script, and nothing follows a page
// break on its page.
func fountainSpaceBefore(prev *FountainElement, element FountainElement) int {
	if prev == nil || prev.Kind == fountainPageBreak || element.Kind == fountainPageBreak {
		return 0
	}
	if element.Kind == fountainScene {
		return 2
	}
	if element.Line == prev.Line+1 && fountainSameBlock(prev.Kind, element.Kind) {
		return 0
	}
	return 1
}

// fountainSameBlock names the pairs a paragraph is made of: a speech runs
// from its cue through its parentheticals, dialogue and lyrics; action,
// lyric and centered lines continue their own kind.
func fountainSameBlock(prev, next string) bool {
	switch next {
	case fountainAction, fountainCentered:
		return prev == next
	case fountainLyric:
		return prev == fountainLyric || fountainSpeechKind(prev)
	case fountainParenthetical, fountainDialogue:
		return fountainSpeechKind(prev)
	}
	return false
}

func fountainSpeechKind(kind string) bool {
	return kind == fountainCharacter || kind == fountainParenthetical || kind == fountainDialogue
}

// FountainSpan is one run of element text with the emphasis Fountain marks
// inline — *italic*, **bold**, ***bold italic***, _underline_ — the styling
// the preview draws and the PDF prints.
type FountainSpan struct {
	Text      string
	Bold      bool
	Italic    bool
	Underline bool
}

// fountainInlineSpans splits element text into styled runs, mirrored by the
// frontend model so the screen and the page agree on every marker. A run of
// one to three asterisks (italic, bold, both) or a single underscore
// (underline) is a delimiter: it can open when text follows it and close when
// text precedes it, and an underscore never opens or closes inside a word, so
// snake_case stays literal. A closer pairs with the nearest open delimiter of
// the same kind and length; openers it skips over stay literal, as does every
// delimiter left unpaired, and a backslash escapes a marker. Emphasis never
// spans lines — each element is one line.
func fountainInlineSpans(text string) []FountainSpan {
	type token struct {
		text     string // literal text, or the delimiter's own characters
		delim    rune   // '*' or '_' for a delimiter, 0 for literal text
		count    int
		canOpen  bool
		canClose bool
		pair     int // the partner delimiter's index, -1 when unpaired
	}
	runes := []rune(text)
	tokens := []token{}
	literal := []rune{}
	flush := func() {
		if len(literal) > 0 {
			tokens = append(tokens, token{text: string(literal), pair: -1})
			literal = []rune{}
		}
	}
	for i := 0; i < len(runes); {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) && (runes[i+1] == '*' || runes[i+1] == '_') {
			literal = append(literal, runes[i+1])
			i += 2
			continue
		}
		if r != '*' && r != '_' {
			literal = append(literal, r)
			i++
			continue
		}
		end := i
		for end < len(runes) && runes[end] == r {
			end++
		}
		count := end - i
		if (r == '*' && count > 3) || (r == '_' && count > 1) {
			literal = append(literal, runes[i:end]...)
			i = end
			continue
		}
		before, after := rune(0), rune(0)
		if i > 0 {
			before = runes[i-1]
		}
		if end < len(runes) {
			after = runes[end]
		}
		canOpen := after != 0 && !unicode.IsSpace(after)
		canClose := before != 0 && !unicode.IsSpace(before)
		if r == '_' {
			canOpen = canOpen && !fountainWordRune(before)
			canClose = canClose && !fountainWordRune(after)
		}
		flush()
		tokens = append(tokens, token{text: string(runes[i:end]), delim: r, count: count, canOpen: canOpen, canClose: canClose, pair: -1})
		i = end
	}
	flush()

	openers := []int{}
	for idx := range tokens {
		tok := tokens[idx]
		if tok.delim == 0 {
			continue
		}
		if tok.canClose {
			match := -1
			for s := len(openers) - 1; s >= 0; s-- {
				if open := tokens[openers[s]]; open.delim == tok.delim && open.count == tok.count {
					match = s
					break
				}
			}
			if match >= 0 {
				tokens[openers[match]].pair = idx
				tokens[idx].pair = openers[match]
				openers = openers[:match]
				continue
			}
		}
		if tok.canOpen {
			openers = append(openers, idx)
		}
	}

	spans := []FountainSpan{}
	bold, italic, underline := 0, 0, 0
	for idx, tok := range tokens {
		if tok.delim == 0 || tok.pair < 0 {
			span := FountainSpan{Text: tok.text, Bold: bold > 0, Italic: italic > 0, Underline: underline > 0}
			if n := len(spans); n > 0 && spans[n-1].Bold == span.Bold && spans[n-1].Italic == span.Italic && spans[n-1].Underline == span.Underline {
				spans[n-1].Text += span.Text
			} else {
				spans = append(spans, span)
			}
			continue
		}
		step := 1
		if tok.pair < idx {
			step = -1
		}
		switch {
		case tok.delim == '_':
			underline += step
		case tok.count == 1:
			italic += step
		case tok.count == 2:
			bold += step
		default:
			bold += step
			italic += step
		}
	}
	return spans
}

// fountainWordRune is the "inside a word" test the underscore rule needs.
func fountainWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
