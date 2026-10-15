package main

import (
	"encoding/base64"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
)

func kinds(elements []FountainElement) []string {
	result := make([]string, len(elements))
	for i, element := range elements {
		result[i] = element.Kind
	}
	return result
}

func TestFountainParseElements(t *testing.T) {
	script := "Title: The Draft\nAuthor: Subhash\n\nINT. WRITING ROOM - NIGHT\n\nA laptop glows in the dark.\n\nSUBHASH\nTyping furiously.\n\nMEERA (V.O.)\nFrom the next room.\n\nCUT TO:\n\n> FADE OUT.\n\n= a synopsis\n\n# Act Two\n\n===\n\n~ singing in the rain\n\n> the end <\n"
	elements, titlePage := fountainParse([]byte(script))

	got := kinds(elements)
	want := []string{
		fountainScene,
		fountainAction,
		fountainCharacter,
		fountainDialogue,
		fountainCharacter,
		fountainDialogue,
		fountainTransition,
		fountainTransition,
		fountainSynopsis,
		fountainSection,
		fountainPageBreak,
		fountainLyric,
		fountainCentered,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if elements[0].Text != "INT. WRITING ROOM - NIGHT" || elements[3].Character != "SUBHASH" {
		t.Fatalf("first scene/dialogue = %+v / %+v", elements[0], elements[3])
	}
	if elements[4].Text != "MEERA (V.O.)" || elements[5].Character != "MEERA" {
		t.Fatalf("cue keeps its extension but attributes the stripped name: %+v / %+v", elements[4], elements[5])
	}
	if elements[11].Text != "singing in the rain" || elements[12].Text != "the end" {
		t.Fatalf("lyric/centered strip their markers: %+v / %+v", elements[11], elements[12])
	}
	if titlePage["Title"] != "The Draft" || titlePage["Author"] != "Subhash" {
		t.Fatalf("titlePage = %v", titlePage)
	}
}

func TestFountainParseForces(t *testing.T) {
	script := ".A MONTAGE — forced\n\n@iago (whispering)\nlowercase cues need the force\n\n> and suddenly a transition\n\n!Not a scene heading.\n"
	elements, _ := fountainParse([]byte(script))
	got := kinds(elements)
	want := []string{fountainScene, fountainCharacter, fountainDialogue, fountainTransition, fountainAction}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if elements[1].Text != "iago (whispering)" {
		t.Fatalf("forced cue = %+v", elements[1])
	}
}

func TestFountainParseSpeechContext(t *testing.T) {
	// An uppercase shout inside a speech block stays dialogue; the same line
	// after a blank line becomes a character cue (followed by speech).
	script := "MAYA\nWAIT. Don't move.\n\nWAIT. Don't move.\n"
	elements, _ := fountainParse([]byte(script))
	got := kinds(elements)
	want := []string{fountainCharacter, fountainDialogue, fountainAction}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestFountainParseNoTitlePage(t *testing.T) {
	// A script that opens with prose has no title page — the first line goes
	// to the body parse untouched.
	elements, titlePage := fountainParse([]byte("Just some action to open.\n\nINT. LATER\n"))
	if len(titlePage) != 0 {
		t.Fatalf("titlePage = %v, want none", titlePage)
	}
	if len(elements) != 2 || elements[0].Kind != fountainAction {
		t.Fatalf("elements = %v", kinds(elements))
	}
}

func TestFountainWarnings(t *testing.T) {
	// Consecutive cues need the @ force (a pattern-rule cue is always
	// followed by its speech), and so does a lowercase spelling.
	script := "INT. HOUSE - DAY\n\n@AMELIA\n@MAYA\nHello.\n\n@amelia\nIs here.\n"
	elements, _ := fountainParse([]byte(script))
	warnings := fountainWarnings(elements)
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, `the cue "AMELIA" has no dialogue after it`) {
		t.Fatalf("warnings = %v, want the empty-dialogue entry", warnings)
	}
	if !strings.Contains(joined, `also spelled`) {
		t.Fatalf("warnings = %v, want the duplicate-spelling entry", warnings)
	}

	clean, _ := fountainParse([]byte("INT. HOUSE - DAY\n\nMAYA\nHello.\n"))
	if got := fountainWarnings(clean); len(got) != 0 {
		t.Fatalf("clean script warnings = %v", got)
	}

	headless, _ := fountainParse([]byte("Just prose.\n"))
	if got := fountainWarnings(headless); len(got) != 1 || !strings.Contains(got[0], "no scene headings") {
		t.Fatalf("headless warnings = %v", got)
	}
}

func TestFountainPDFShape(t *testing.T) {
	script := "Title: The Draft\nAuthor: Subhash\n\nINT. ROOM - DAY\n\nAction text.\n\nMAYA\nDialogue here.\n"
	elements, titlePage := fountainParse([]byte(script))
	pdf, err := fountainPDF(elements, titlePage)
	if err != nil {
		t.Fatalf("fountainPDF returned error: %v", err)
	}
	content := string(pdf)
	if !strings.HasPrefix(content, "%PDF-1.4\n") {
		t.Fatal("the PDF must open with its magic header")
	}
	if !strings.Contains(content, "/Type /Catalog") || !strings.Contains(content, "/BaseFont /Courier") {
		t.Fatal("the PDF must carry a catalog and the Courier fonts")
	}
	// Two pages: title + body. The body page is numbered from the second
	// script page, so neither page carries a number here.
	if !strings.Contains(content, "/Count 2") {
		t.Fatalf("page count missing: %q", content[len(content)-400:])
	}
	if strings.Contains(content, "(2.) Tj") {
		t.Fatal("the script's first body page must not carry a page number")
	}
	// Title underlined: a stroke path rides the title page stream.
	if !strings.Contains(content, " l S\n") {
		t.Fatal("the title page must underline the title")
	}
	// Longer scripts paginate and number from the second body page.
	var long strings.Builder
	long.WriteString("INT. ROOM - DAY\n\n")
	for i := 0; i < 200; i++ {
		long.WriteString("Action line of the long script to force pagination.\n\n")
	}
	elements2, _ := fountainParse([]byte(long.String()))
	pdf2, err := fountainPDF(elements2, map[string]string{})
	if err != nil {
		t.Fatalf("fountainPDF(long) returned error: %v", err)
	}
	if !strings.Contains(string(pdf2), "(2.) Tj") {
		t.Fatal("the script's second body page must carry the 2. page number")
	}
}

func TestFountainFDXRoundTrip(t *testing.T) {
	script := "Title: The Draft\nAuthor: Subhash\n\nINT. ROOM - DAY\n\nAction text.\n\nMAYA\n Dialogue with a <tag> and & ampersand.\n"
	elements, titlePage := fountainParse([]byte(script))
	fdx, err := fountainFDX(elements, titlePage)
	if err != nil {
		t.Fatalf("fountainFDX returned error: %v", err)
	}
	var doc fdxDocument
	if err := xml.Unmarshal(fdx, &doc); err != nil {
		t.Fatalf("the output must parse back as FDX: %v", err)
	}
	if doc.DocumentType != "Script" || len(doc.Content.Paragraphs) != 4 {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Content.Paragraphs[0].Type != "Scene Heading" || doc.Content.Paragraphs[2].Type != "Character" || doc.Content.Paragraphs[3].Type != "Dialogue" {
		t.Fatalf("paragraphs = %+v", doc.Content.Paragraphs)
	}
	if doc.Content.Paragraphs[3].Text != "Dialogue with a <tag> and & ampersand." {
		t.Fatalf("text escaping = %q", doc.Content.Paragraphs[3].Text)
	}
	// Sections and synopses never reach the body.
	withExtras, _ := fountainParse([]byte("= note\n\n# ACT ONE\n\nINT. ROOM\n"))
	fdx2, _ := fountainFDX(withExtras, map[string]string{})
	var doc2 fdxDocument
	xml.Unmarshal(fdx2, &doc2)
	if len(doc2.Content.Paragraphs) != 1 || doc2.Content.Paragraphs[0].Type != "Scene Heading" {
		t.Fatalf("extras leaked into the body: %+v", doc2.Content.Paragraphs)
	}
}

func TestMarkdownFountainPersistence(t *testing.T) {
	config := defaultAppConfig()
	artifactsDir := filepath.Join(t.TempDir(), "artifacts")

	// A .fountain attachment persists with the fountain dialect's extension
	// and wire mime; a text/markdown one stays .md.
	fountain := "data:text/x-fountain;base64," + base64.StdEncoding.EncodeToString([]byte("INT. ROOM - DAY\n"))
	contents, _, err := historyContentForMessage(ChatMessage{Markdowns: []string{fountain, markdownDataURL(t, "# note\n")}}, artifactsDir, config)
	if err != nil {
		t.Fatalf("historyContentForMessage returned error: %v", err)
	}
	var documents []HistoryContent
	for _, content := range contents {
		if content.Type == "markdown" {
			documents = append(documents, content)
		}
	}
	if len(documents) != 2 || !strings.HasSuffix(documents[0].Path, ".fountain") || !strings.HasSuffix(documents[1].Path, ".md") {
		t.Fatalf("documents = %+v, want a .fountain and a .md artifact", documents)
	}
	if markdownArtifactExtension(documents[0]) != ".fountain" {
		t.Fatalf("fountain artifact extension = %q", markdownArtifactExtension(documents[0]))
	}
}

func TestSaveMarkdownEditFountainFormat(t *testing.T) {
	config := editTestHome(t)
	markdownConversationFixture(t, config.Storage)
	app := NewApp()

	script := "INT. ROOM - DAY\n\nMAYA\nHello.\n"
	result, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{
		ConversationID:   "conv_doc",
		SourceArtifactID: "md_old",
		Format:           "fountain",
		Text:             script,
	})
	if err != nil {
		t.Fatalf("SaveMarkdownEdit(fountain) returned error: %v", err)
	}
	if !strings.HasPrefix(result.ArtifactID, "md_") || result.Format != "fountain" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.HasSuffix(result.URL, ".fountain") {
		t.Fatalf("url = %q, want a .fountain artifact", result.URL)
	}
	detail, err := getConversation(config.Storage, "conv_doc")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	var saved *HistoryContent
	for i := len(detail.Turns) - 1; i >= 0 && saved == nil; i-- {
		for j, content := range detail.Turns[i].Content {
			if content.ArtifactID == result.ArtifactID {
				saved = &detail.Turns[i].Content[j]
			}
		}
	}
	if saved == nil || saved.MimeType != "text/x-fountain" {
		t.Fatalf("saved entry = %+v", saved)
	}
	if !strings.Contains(detail.Turns[len(detail.Turns)-1].Content[0].Text, "saved as Fountain") {
		t.Fatalf("summary = %q", detail.Turns[len(detail.Turns)-1].Content[0].Text)
	}

	// The resolve hands the dialect back; an invalid format is refused.
	info, err := app.ResolveMarkdownEditSource("conv_doc", result.ArtifactID)
	if err != nil || info.Format != "fountain" {
		t.Fatalf("resolve format = %q, err %v", info.Format, err)
	}
	if _, err := app.SaveMarkdownEdit(SaveMarkdownEditRequest{ConversationID: "conv_doc", Format: "fdx", Text: script}); err == nil {
		t.Fatal("an unknown format must fail")
	}
}
