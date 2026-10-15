package main

// The markdown dialect's print layout: a saved .md artifact renders to PDF
// through the same page writer as the screenplay (fountain_export.go), with
// Markdown's structure mapped onto typographic steps instead of screenplay
// columns — bold headings at larger sizes, hanging-indent lists, verbatim
// code blocks, quoted lines. This is deliberately a "typed page", not full
// typesetting: Courier on the fixed grid, inline emphasis stripped, links
// spelled out — the honest zero-dependency render (the repo's deps stay
// wails + keyring), with the preview remaining where rich styling lives.

import (
	"regexp"
	"strconv"
	"strings"
)

// Block kinds of the markdown print layout.
const (
	markdownBlockParagraph = "paragraph"
	markdownBlockHeading   = "heading"
	markdownBlockCode      = "code"
	markdownBlockQuote     = "quote"
	markdownBlockBullet    = "bullet"
	markdownBlockOrdered   = "ordered"
	markdownBlockRule      = "rule"
	markdownBlockTable     = "table"
)

// markdownBlock is one block-level unit: a paragraph, a heading (level =
// 1-6), a fenced code block, a quote, ONE list item (marker carries its "N."
// number for ordered lists; level the nesting depth), a rule, or a table's
// raw lines.
type markdownBlock struct {
	kind   string
	level  int
	marker string
	lines  []string
}

// parseMarkdownBlocks walks the common block subset — ATX headings, fenced
// code (``` and ~~~), blockquotes, bullet/ordered lists with one indent
// level of nesting, GFM tables (kept as raw lines), rules, paragraphs. The
// same generous contract as the rest of the document surface: anything
// unrecognized becomes paragraph text; nothing fails.
func parseMarkdownBlocks(data []byte) []markdownBlock {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	blocks := []markdownBlock{}
	orderedRun := 0

	fenceAt := func(line string) (string, bool) {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") {
			return "```", true
		}
		if strings.HasPrefix(trimmed, "~~~") {
			return "~~~", true
		}
		return "", false
	}
	isListStart := func(line string) (indent, marker int, ordered bool, ok bool) {
		trimmed := line
		indent = 0
		for indent < len(trimmed) && trimmed[indent] == ' ' {
			indent++
		}
		rest := trimmed[indent:]
		switch {
		case strings.HasPrefix(rest, "- "), strings.HasPrefix(rest, "* "), strings.HasPrefix(rest, "+ "):
			// The marker length covers the bullet and its space, same as the
			// ordered case — the item text starts after it.
			return indent, 2, false, true
		case len(rest) >= 3 && rest[0] >= '0' && rest[0] <= '9':
			dot := strings.IndexAny(rest, ".)")
			if dot > 0 && dot <= 3 && rest[dot+1] == ' ' {
				return indent, dot + 2, true, true
			}
		}
		return 0, 0, false, false
	}
	isTableRow := func(line string) bool {
		return strings.HasPrefix(strings.TrimLeft(line, " \t"), "|")
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			orderedRun = 0
			continue
		}
		// Fenced code: verbatim until the closing fence of the same marker.
		if marker, open := fenceAt(line); open {
			i++
			var code []string
			for ; i < len(lines); i++ {
				if _, close := fenceAt(lines[i]); close && strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), marker) {
					break
				}
				code = append(code, lines[i])
			}
			blocks = append(blocks, markdownBlock{kind: markdownBlockCode, lines: code})
			orderedRun = 0
			continue
		}
		// ATX heading.
		if hashes := matchHashes(trimmed); hashes > 0 {
			blocks = append(blocks, markdownBlock{kind: markdownBlockHeading, level: hashes, lines: []string{strings.TrimSpace(trimmed[hashes:])}})
			orderedRun = 0
			continue
		}
		// Rule: three or more of -, * or _ alone.
		if len(trimmed) >= 3 && (strings.Trim(trimmed, "-") == "" || strings.Trim(trimmed, "*") == "" || strings.Trim(trimmed, "_") == "") {
			blocks = append(blocks, markdownBlock{kind: markdownBlockRule, lines: []string{trimmed}})
			orderedRun = 0
			continue
		}
		// Table: a run of pipe rows (the |---| separator stays out).
		if isTableRow(line) {
			var rows []string
			for ; i < len(lines) && isTableRow(lines[i]); i++ {
				row := strings.TrimSpace(lines[i])
				if row != "" && strings.Trim(row, "|-: ") == "" {
					continue
				}
				rows = append(rows, row)
			}
			i--
			blocks = append(blocks, markdownBlock{kind: markdownBlockTable, lines: rows})
			orderedRun = 0
			continue
		}
		// Blockquote: a run of > lines (one level; deeper markers kept).
		if strings.HasPrefix(trimmed, ">") {
			var quoted []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				quoted = append(quoted, strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"))
			}
			i--
			blocks = append(blocks, markdownBlock{kind: markdownBlockQuote, lines: quoted})
			orderedRun = 0
			continue
		}
		// Lists: one block per item, ordered numbering continuing across a
		// consecutive run and restarting after a blank line.
		if indent, marker, ordered, ok := isListStart(line); ok {
			number := ""
			if ordered {
				orderedRun++
				number = strconv.Itoa(orderedRun) + "."
			} else {
				orderedRun = 0
			}
			itemLines := []string{strings.TrimSpace(line[indent+marker:])}
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) == "" {
					break
				}
				if _, _, _, nextList := isListStart(next); nextList {
					break
				}
				// An indented continuation line joins the item.
				if strings.HasPrefix(next, " ") || strings.HasPrefix(next, "\t") {
					itemLines = append(itemLines, strings.TrimSpace(next))
					i++
					continue
				}
				break
			}
			blocks = append(blocks, markdownBlock{
				kind:   map[bool]string{true: markdownBlockOrdered, false: markdownBlockBullet}[ordered],
				level:  indent / 2,
				marker: number,
				lines:  itemLines,
			})
			continue
		}
		// Paragraph: consecutive plain lines merge into one flowing block.
		var para []string
		for ; i < len(lines); i++ {
			paraLine := strings.TrimSpace(lines[i])
			if paraLine == "" {
				break
			}
			if _, open := fenceAt(lines[i]); open {
				break
			}
			if hashes := matchHashes(paraLine); hashes > 0 && hashes < len(paraLine) {
				break
			}
			if _, _, _, listNext := isListStart(lines[i]); listNext {
				break
			}
			if strings.HasPrefix(paraLine, ">") || isTableRow(paraLine) {
				break
			}
			para = append(para, paraLine)
		}
		i--
		blocks = append(blocks, markdownBlock{kind: markdownBlockParagraph, lines: para})
		orderedRun = 0
	}
	return blocks
}

// matchHashes counts a line's leading ATX hashes (1-6, followed by a space
// or end of line).
func matchHashes(line string) int {
	hashes := 0
	for hashes < len(line) && line[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 {
		return 0
	}
	if hashes < len(line) && line[hashes] != ' ' && line[hashes] != '\t' {
		return 0
	}
	return hashes
}

// markdownLinksReplacer spells links and images the way plain text does:
// [text](url) → "text (url)", images → "[image: alt]".
var (
	markdownImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]*)\)`)
	markdownLinkRe  = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)
)

func markdownLinks(text string) string {
	text = markdownImageRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := markdownImageRe.FindStringSubmatch(match)
		if parts[1] == "" {
			return "[image]"
		}
		return "[image: " + parts[1] + "]"
	})
	return markdownLinkRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := markdownLinkRe.FindStringSubmatch(match)
		if parts[2] == "" || parts[2] == parts[1] {
			return parts[1]
		}
		return parts[1] + " (" + parts[2] + ")"
	})
}

// markdownPlainInline strips the inline syntax a plain Courier page cannot
// carry — bold/italic/strike markers, code spans — and spells links out.
func markdownPlainInline(text string) string {
	text = markdownLinks(text)
	replacer := strings.NewReplacer(
		"`", "",
		"**", "",
		"__", "",
		"~~", "",
		"*", "",
		"_", "",
	)
	return replacer.Replace(text)
}

// markdownPDFLines lays a parsed document onto the page grid: bold headings
// at stepped sizes, hanging-indent lists, verbatim clipped code, quoted
// lines, rules of underscores.
func markdownPDFLines(blocks []markdownBlock) [][]fountainPDFLine {
	const (
		headingLarge  = 16.0
		headingMedium = 14.0
		codeSize      = 11.0
		tableSize     = 10.0
	)
	wrap := fountainWrapText
	blank := fountainPDFLine{}
	out := [][]fountainPDFLine{}
	appendLine := func(line fountainPDFLine) { out = append(out, []fountainPDFLine{line}) }
	appendBlank := func() { out = append(out, []fountainPDFLine{blank}) }

	for _, block := range blocks {
		switch block.kind {
		case markdownBlockHeading:
			appendBlank()
			size := 12.0
			switch {
			case block.level <= 2:
				size = headingLarge
			case block.level == 3:
				size = headingMedium
			}
			text := markdownPlainInline(strings.Join(block.lines, " "))
			for _, piece := range wrap(text, fountainWidthAction) {
				appendLine(fountainPDFLine{text: piece, font: "/F2", size: size})
			}
			appendBlank()
		case markdownBlockParagraph:
			text := markdownPlainInline(strings.Join(block.lines, " "))
			for _, piece := range wrap(text, fountainWidthAction) {
				appendLine(fountainPDFLine{text: piece})
			}
			appendBlank()
		case markdownBlockCode:
			for _, raw := range block.lines {
				line := raw
				if len(line) > 63 {
					line = line[:63]
				}
				appendLine(fountainPDFLine{text: line, indent: 2, size: codeSize})
			}
			appendBlank()
		case markdownBlockQuote:
			for _, raw := range block.lines {
				for _, piece := range wrap(markdownPlainInline(raw), fountainWidthAction-4) {
					appendLine(fountainPDFLine{text: "> " + piece, indent: 2})
				}
			}
			appendBlank()
		case markdownBlockBullet:
			hang := strings.Repeat("  ", block.level)
			text := markdownPlainInline(strings.Join(block.lines, " "))
			pieces := wrap(text, fountainWidthAction-len(hang)-2)
			for i, piece := range pieces {
				if i == 0 {
					appendLine(fountainPDFLine{text: hang + "- " + piece, indent: len(hang)})
					continue
				}
				appendLine(fountainPDFLine{text: hang + "  " + piece, indent: len(hang) + 2})
			}
			appendBlank()
		case markdownBlockOrdered:
			hang := strings.Repeat("  ", block.level)
			text := markdownPlainInline(strings.Join(block.lines, " "))
			pieces := wrap(text, fountainWidthAction-len(hang)-3)
			for i, piece := range pieces {
				if i == 0 {
					appendLine(fountainPDFLine{text: hang + block.marker + " " + piece, indent: len(hang)})
					continue
				}
				appendLine(fountainPDFLine{text: hang + "   " + piece, indent: len(hang) + 3})
			}
			appendBlank()
		case markdownBlockRule:
			appendLine(fountainPDFLine{text: strings.Repeat("_", fountainWidthAction)})
			appendBlank()
		case markdownBlockTable:
			for _, raw := range block.lines {
				line := raw
				if len(line) > 72 {
					line = line[:72]
				}
				appendLine(fountainPDFLine{text: line, size: tableSize})
			}
			appendBlank()
		}
	}
	return out
}

// markdownPDF renders a parsed document to the page writer: the layout is
// paginated with the markdown widow rule (a heading keeps a body line below
// it) and no title page — the document's own H1 opens page 1, which numbers
// from page 2 like every rendered document.
func markdownPDF(blocks []markdownBlock) ([]byte, error) {
	lines := markdownPDFLines(blocks)
	pages := pdfPaginate(lines, markdownKeepRoom)
	return pdfWrite(pages, nil, -1), nil
}

// markdownKeepRoom is the print layout's widow rule: a heading needs a body
// line beneath it.
func markdownKeepRoom(blk []fountainPDFLine) int {
	if len(blk) == 1 && blk[0].pdfFont() == "/F2" && blk[0].pdfSize() > 12 {
		return 2
	}
	return len(blk)
}
