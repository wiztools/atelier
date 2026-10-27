package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testImageRef(id string) ComicMediaRef {
	return ComicMediaRef{ArtifactID: id, Path: "/tmp/" + id + ".png", Kind: "image", Width: 800, Height: 600}
}

// comicRect builds a clockwise axis-aligned rectangle — the 4-point panel.
func comicRect(x, y, w, h float64) []ComicPoint {
	return []ComicPoint{
		{X: x, Y: y},
		{X: x + w, Y: y},
		{X: x + w, Y: y + h},
		{X: x, Y: y + h},
	}
}

// comicBowtie builds the canonical self-crossing quadrilateral: two triangles
// joined at a pinch, the shape a careless vertex drag produces.
func comicBowtie() []ComicPoint {
	return []ComicPoint{
		{X: 0.1, Y: 0.1},
		{X: 0.9, Y: 0.9},
		{X: 0.9, Y: 0.1},
		{X: 0.1, Y: 0.9},
	}
}

func validComicDoc() ComicDoc {
	return ComicDoc{
		SchemaVersion: comicSchemaVersion,
		Name:          "Issue #1",
		Pages: []ComicPage{{
			ID:     "p1",
			Aspect: "comic",
			Panels: []ComicPanel{
				{ID: "pnl1", Points: comicRect(0.05, 0.05, 0.9, 0.4)},
				{ID: "pnl2", Points: comicRect(0.05, 0.5, 0.44, 0.45), Image: &ComicPanelImage{Ref: testImageRef("img_1")}},
			},
		}},
		Unplaced: []ComicMediaRef{testImageRef("img_2")},
		Style:    ComicStyle{Gutter: comicGutterDefault, Border: comicBorderDefault},
	}
}

func TestComicArtifactExtension(t *testing.T) {
	cases := []struct {
		content HistoryContent
		want    string
	}{
		{HistoryContent{Type: "comic", Path: "artifacts/cbc_a.json", MimeType: "application/json"}, ".json"},
		{HistoryContent{Type: "comic", MimeType: "application/json"}, ".json"},
		{HistoryContent{Type: "comic", Path: "artifacts/cbc_a.txt", MimeType: "text/plain"}, ""},
		{HistoryContent{Type: "composition", Path: "artifacts/cmp_a.json", MimeType: "application/json"}, ""},
		{HistoryContent{Type: "video", Path: "artifacts/vid_a.json"}, ""},
	}
	for _, tc := range cases {
		if got := comicArtifactExtension(tc.content); got != tc.want {
			t.Fatalf("comicArtifactExtension(%+v) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

func TestValidateComicDocNormalizes(t *testing.T) {
	doc := ComicDoc{
		Name:          "  ",
		SchemaVersion: 0,
		Pages: []ComicPage{{
			ID:     "p1",
			Aspect: "",
			Panels: []ComicPanel{
				// Counterclockwise winding and out-of-range vertices
				// normalize onto the canonical form.
				{ID: "pnl1", Points: []ComicPoint{
					{X: 0, Y: 0.4},
					{X: 0.9, Y: 0.4},
					{X: 0.9, Y: 0.05},
					{X: 0, Y: 0.05},
				}},
				// Overlap with panel 1 is LEGAL — the inset idiom; paint
				// order resolves it.
				{ID: "pnl2", Points: comicRect(0.6, 0.3, 0.35, 0.3), Image: &ComicPanelImage{
					Ref:    testImageRef("img_1"),
					Fit:    "",
					FocusX: -1,
					FocusY: 2,
					Zoom:   9,
				}},
			},
		}},
		Style: ComicStyle{Gutter: -3, Border: 2},
	}
	if err := validateComicDoc(&doc); err != nil {
		t.Fatalf("validateComicDoc returned error: %v", err)
	}
	if doc.Name != comicDefaultName || doc.SchemaVersion != comicSchemaVersion {
		t.Fatalf("name/schema = %q/%d", doc.Name, doc.SchemaVersion)
	}
	if doc.Pages[0].Aspect != comicDefaultAspect {
		t.Fatalf("aspect = %q, want the default preset", doc.Pages[0].Aspect)
	}
	panel1 := doc.Pages[0].Panels[0]
	if got := comicSignedArea(panel1.Points); got <= 0 {
		t.Fatalf("panel winding = %g signed area, want positive (clockwise)", got)
	}
	image := doc.Pages[0].Panels[1].Image
	if image.Fit != comicFitCover || image.FocusX != comicFocusMin || image.FocusY != comicFocusMax || image.Zoom != comicZoomMax {
		t.Fatalf("image = %+v, want cover default and clamped focus/zoom", image)
	}
	if doc.Style.Gutter != comicGutterMin || doc.Style.Border != comicBorderMax {
		t.Fatalf("style = %+v, want clamped gutter/border", doc.Style)
	}

	// Nil slices normalize to empty arrays so the wire never carries null.
	empty := ComicDoc{Pages: []ComicPage{{ID: "p1"}}}
	if err := validateComicDoc(&empty); err != nil {
		t.Fatalf("empty doc returned error: %v", err)
	}
	if empty.Unplaced == nil || empty.Pages[0].Panels == nil {
		t.Fatal("empty doc kept nil slices")
	}
	if empty.Pages[0].Aspect != comicDefaultAspect {
		t.Fatalf("empty doc aspect = %q", empty.Pages[0].Aspect)
	}
}

func TestValidateComicDocRefusals(t *testing.T) {
	page := func(id string, panels ...ComicPanel) ComicPage {
		return ComicPage{ID: id, Panels: panels}
	}
	panel := func(id string, points []ComicPoint) ComicPanel {
		return ComicPanel{ID: id, Points: points}
	}
	imagePanel := func(id string, image *ComicPanelImage) ComicPanel {
		return ComicPanel{ID: id, Points: comicRect(0.1, 0.1, 0.5, 0.5), Image: image}
	}
	tooManyPages := make([]ComicPage, comicMaxPages+1)
	for i := range tooManyPages {
		tooManyPages[i] = page("p" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	tooManyPanels := make([]ComicPanel, comicMaxPanelsPerPage+1)
	for i := range tooManyPanels {
		tooManyPanels[i] = panel("pnl"+string(rune('a'+i%26))+string(rune('a'+i/26)), comicRect(0, 0, 0.2, 0.2))
	}
	tooManyVertices := make([]ComicPoint, comicMaxPanelVertices+1)
	for i := range tooManyVertices {
		tooManyVertices[i] = ComicPoint{X: 0.1, Y: 0.1}
	}
	tooManyUnplaced := make([]ComicMediaRef, comicMaxUnplacedImages+1)
	for i := range tooManyUnplaced {
		tooManyUnplaced[i] = testImageRef("img" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	cases := []struct {
		name string
		doc  ComicDoc
		want string
	}{
		{"no pages", ComicDoc{}, "at least one page"},
		{"too many pages", ComicDoc{Pages: tooManyPages}, "at most 64 pages"},
		{"too many panels", ComicDoc{Pages: []ComicPage{{ID: "p1", Panels: tooManyPanels}}}, "at most 24 panels"},
		{"too many vertices", ComicDoc{Pages: []ComicPage{page("p1", panel("pnl1", tooManyVertices))}}, "at most 16 vertices"},
		{"too few vertices", ComicDoc{Pages: []ComicPage{page("p1", panel("pnl1", comicRect(0, 0, 0.5, 0.5)[:2]))}}, "at least 3 vertices"},
		{"unknown aspect", ComicDoc{Pages: []ComicPage{{ID: "p1", Aspect: "cinemascope"}}}, "unknown aspect"},
		{"zero custom aspect", ComicDoc{Pages: []ComicPage{{ID: "p1", Aspect: "custom"}}}, "positive width ÷ height"},
		{"negative custom aspect", ComicDoc{Pages: []ComicPage{{ID: "p1", Aspect: "custom", CustomAspect: -2}}}, "positive width ÷ height"},
		{"empty page id", ComicDoc{Pages: []ComicPage{{Panels: []ComicPanel{}}}}, "empty id"},
		{"empty panel id", ComicDoc{Pages: []ComicPage{page("p1", panel("", comicRect(0, 0, 0.5, 0.5)))}}, "empty id"},
		{"duplicate page id", ComicDoc{Pages: []ComicPage{page("p1"), page("p1")}}, "reuses id"},
		{"duplicate panel id across pages", ComicDoc{Pages: []ComicPage{page("p1", panel("pnl1", comicRect(0, 0, 0.5, 0.5))), page("p2", panel("pnl1", comicRect(0, 0, 0.5, 0.5)))}}, "reuses id"},
		{"bowtie panel", ComicDoc{Pages: []ComicPage{page("p1", panel("pnl1", comicBowtie()))}}, "crosses itself"},
		{"degenerate sliver", ComicDoc{Pages: []ComicPage{page("p1", panel("pnl1", []ComicPoint{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 0.0001}}))}}, "too small"},
		{"non-finite vertex", ComicDoc{Pages: []ComicPage{page("p1", panel("pnl1", []ComicPoint{{X: 0, Y: 0}, {X: math.NaN(), Y: 0.5}, {X: 0.5, Y: 0.5}}))}}, "non-finite"},
		{"video in panel", ComicDoc{Pages: []ComicPage{page("p1", imagePanel("pnl1", &ComicPanelImage{Ref: ComicMediaRef{ArtifactID: "vid_1", Kind: "video", Path: "/tmp/vid.mp4", Width: 100, Height: 100}}))}}, "comic panels hold images"},
		{"unmeasured image in panel", ComicDoc{Pages: []ComicPage{page("p1", imagePanel("pnl1", &ComicPanelImage{Ref: ComicMediaRef{ArtifactID: "img_1", Kind: "image", Path: "/tmp/img.png"}}))}}, "never measured"},
		{"unknown fit", ComicDoc{Pages: []ComicPage{page("p1", imagePanel("pnl1", &ComicPanelImage{Ref: testImageRef("img_1"), Fit: "stretch"}))}}, "image fit must be"},
		{"video unplaced", ComicDoc{Pages: []ComicPage{page("p1")}, Unplaced: []ComicMediaRef{{ArtifactID: "aud_1", Kind: "audio", Path: "/tmp/a.wav", Width: 1, Height: 1}}}, "stills only"},
		{"unmeasured unplaced", ComicDoc{Pages: []ComicPage{page("p1")}, Unplaced: []ComicMediaRef{{ArtifactID: "img_1", Kind: "image", Path: "/tmp/i.png"}}}, "never measured"},
		{"too many unplaced", ComicDoc{Pages: []ComicPage{page("p1")}, Unplaced: tooManyUnplaced}, "at most 50 unplaced images"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.doc
			err := validateComicDoc(&doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateComicDoc error = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	// Overlapping panels are comic grammar, not an error — an inset over a
	// splash panel validates alongside the plain grid.
	inset := validComicDoc()
	inset.Pages[0].Panels = append(inset.Pages[0].Panels, ComicPanel{ID: "pnl3", Points: comicRect(0.2, 0.2, 0.3, 0.3)})
	if err := validateComicDoc(&inset); err != nil {
		t.Fatalf("overlapping panels must validate, got %v", err)
	}
}

func TestComicSelfIntersects(t *testing.T) {
	cases := []struct {
		name   string
		points []ComicPoint
		want   bool
	}{
		{"rectangle", comicRect(0.1, 0.1, 0.8, 0.8), false},
		{"concave L", []ComicPoint{
			{X: 0.1, Y: 0.1}, {X: 0.9, Y: 0.1}, {X: 0.9, Y: 0.5},
			{X: 0.5, Y: 0.5}, {X: 0.5, Y: 0.9}, {X: 0.1, Y: 0.9},
		}, false},
		{"vertex touching far edge", []ComicPoint{
			{X: 0.1, Y: 0.1}, {X: 0.9, Y: 0.1}, {X: 0.5, Y: 0.5}, {X: 0.9, Y: 0.9}, {X: 0.1, Y: 0.9},
		}, false},
		{"bowtie", comicBowtie(), true},
		{"pentagram", func() []ComicPoint {
			star := make([]ComicPoint, 5)
			for i := range star {
				angle := math.Pi/2 + float64(i)*4*math.Pi/5
				star[i] = ComicPoint{X: 0.5 + 0.4*math.Cos(angle), Y: 0.5 - 0.4*math.Sin(angle)}
			}
			return star
		}(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := comicSelfIntersects(tc.points); got != tc.want {
				t.Fatalf("comicSelfIntersects(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestValidateComicDocTexts(t *testing.T) {
	// Texts normalize: clamped boxes and tail points, defaulted font scale,
	// a caption's stray tail dropped, nil slices filled.
	doc := ComicDoc{Pages: []ComicPage{{
		ID: "p1",
		Texts: []ComicText{
			{ID: "txt1", Kind: comicTextKindBalloon, X: -1, Y: 0.2, Width: 0.2, Height: 0.1, Text: "Hello!", Size: 0,
				Tail: &ComicTail{X: 2, Y: 0.6}},
			{ID: "txt2", Kind: comicTextKindCaption, X: 0.05, Y: 0.02, Width: 0.3, Height: 0.05, Text: "Meanwhile…", Size: 9,
				Tail: &ComicTail{X: 0.5, Y: 0.5}},
			{ID: "txt3", Kind: comicTextKindSFX, X: 0.6, Y: 0.1, Width: 0.15, Height: 0.12, Text: "POW!"},
		},
	}}}
	if err := validateComicDoc(&doc); err != nil {
		t.Fatalf("validateComicDoc returned error: %v", err)
	}
	balloon := doc.Pages[0].Texts[0]
	if balloon.X != 0 || balloon.Size != 1 {
		t.Fatalf("balloon = %+v, want clamped box and defaulted size", balloon)
	}
	if balloon.Tail == nil || balloon.Tail.X != 1 || balloon.Tail.Y != 0.6 {
		t.Fatalf("balloon tail = %+v, want clamped to the page", balloon.Tail)
	}
	caption := doc.Pages[0].Texts[1]
	if caption.Size != comicTextSizeMax {
		t.Fatalf("caption size = %g, want the ceiling", caption.Size)
	}
	if caption.Tail != nil {
		t.Fatalf("caption tail = %+v, want dropped", caption.Tail)
	}
	if doc.Pages[0].Texts[2].Kind != comicTextKindSFX {
		t.Fatalf("sfx = %+v", doc.Pages[0].Texts[2])
	}

	tooMany := make([]ComicText, comicMaxTextsPerPage+1)
	for i := range tooMany {
		tooMany[i] = ComicText{ID: fmt.Sprintf("txt_%02d", i), Kind: comicTextKindCaption, X: 0.1, Y: 0.1, Width: 0.2, Height: 0.05}
	}
	cases := []struct {
		name string
		text ComicText
		want string
	}{
		{"unknown kind", ComicText{ID: "t", Kind: "whisper", X: 0.1, Y: 0.1, Width: 0.2, Height: 0.05}, "kind must be"},
		{"empty id", ComicText{Kind: comicTextKindCaption, X: 0.1, Y: 0.1, Width: 0.2, Height: 0.05}, "empty id"},
		{"duplicate id", ComicText{ID: "px", Kind: comicTextKindCaption, X: 0.1, Y: 0.1, Width: 0.2, Height: 0.05}, "reuses id"},
		{"non-finite box", ComicText{ID: "t", Kind: comicTextKindBalloon, X: math.NaN(), Y: 0.1, Width: 0.2, Height: 0.05}, "non-finite"},
		{"box too small", ComicText{ID: "t", Kind: comicTextKindBalloon, X: 0.1, Y: 0.1, Width: 0.01, Height: 0.05}, "too small"},
		{"non-finite tail", ComicText{ID: "t", Kind: comicTextKindBalloon, X: 0.1, Y: 0.1, Width: 0.2, Height: 0.05, Tail: &ComicTail{X: math.Inf(1), Y: 0.5}}, "non-finite tail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := ComicDoc{Pages: []ComicPage{{ID: "px", Texts: []ComicText{tc.text}}}}
			err := validateComicDoc(&candidate)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateComicDoc error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	overflow := ComicDoc{Pages: []ComicPage{{ID: "px", Texts: tooMany}}}
	if err := validateComicDoc(&overflow); err == nil || !strings.Contains(err.Error(), "at most 20 text elements") {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestComicPageAspect(t *testing.T) {
	if ratio, name := comicPageAspect(ComicPage{Aspect: "manga"}); ratio != 5.0/7.5 || name != "manga" {
		t.Fatalf("manga = %g/%q", ratio, name)
	}
	if ratio, _ := comicPageAspect(ComicPage{Aspect: "custom", CustomAspect: 0.1}); ratio != comicCustomAspectMin {
		t.Fatalf("extreme custom = %g, want clamped to %g", ratio, comicCustomAspectMin)
	}
	if ratio, name := comicPageAspect(ComicPage{Aspect: "hand-edited"}); name != comicDefaultAspect || ratio != comicAspectPresets[comicDefaultAspect] {
		t.Fatalf("unknown aspect = %g/%q, want the default preset", ratio, name)
	}
	if ratio, _ := comicPageAspect(ComicPage{}); ratio != comicAspectPresets[comicDefaultAspect] {
		t.Fatalf("empty aspect = %g, want the default preset", ratio)
	}
}

func TestSeedComicDoc(t *testing.T) {
	doc := seedComicDoc("  ", []ComicMediaRef{testImageRef("img_1"), testImageRef("img_2")})
	if doc.Name != comicDefaultName {
		t.Fatalf("name = %q", doc.Name)
	}
	if len(doc.Pages) != 1 || doc.Pages[0].Aspect != comicDefaultAspect || len(doc.Pages[0].Panels) != 0 {
		t.Fatalf("pages = %+v, want one blank page in the default format", doc.Pages)
	}
	if len(doc.Unplaced) != 2 || doc.Unplaced[0].ArtifactID != "img_1" || doc.Unplaced[1].ArtifactID != "img_2" {
		t.Fatalf("unplaced = %+v, want the refs in reference order", doc.Unplaced)
	}
	if doc.Style.Gutter != comicGutterDefault || doc.Style.Border != comicBorderDefault {
		t.Fatalf("style = %+v, want the house defaults", doc.Style)
	}
	if err := validateComicDoc(&doc); err != nil {
		t.Fatalf("seeded doc must validate: %v", err)
	}
}

func TestWriteComicArtifact(t *testing.T) {
	dir := t.TempDir()
	content, err := writeComicArtifact(dir, validComicDoc())
	if err != nil {
		t.Fatalf("writeComicArtifact: %v", err)
	}
	if content.Type != "comic" || !strings.HasPrefix(content.ArtifactID, "cbc_") || content.MimeType != "application/json" {
		t.Fatalf("content = %+v", content)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.Base(content.Path)))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	parsed, err := parseComicDoc(data)
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if len(parsed.Pages) != 1 || len(parsed.Pages[0].Panels) != 2 || len(parsed.Unplaced) != 1 {
		t.Fatalf("parsed doc = %+v", parsed)
	}
	if _, err := writeComicArtifact(dir, ComicDoc{}); err == nil {
		t.Fatal("a pageless doc must refuse to write")
	}
}

func TestComicMissingRefs(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "img_here.png")
	if err := os.WriteFile(present, []byte("bytes"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ref := func(id, path string) ComicMediaRef {
		ref := testImageRef(id)
		ref.Path = path
		return ref
	}
	doc := validComicDoc()
	doc.Pages[0].Panels[1].Image.Ref = ref("img_here", present)
	// The same absent id appears placed and unplaced — one report entry.
	doc.Unplaced = []ComicMediaRef{ref("img_gone", "/does/not/exist.png")}
	doc.Pages[0].Panels[0].Image = &ComicPanelImage{Ref: ref("img_gone", "/does/not/exist.png")}
	missing := comicMissingRefs(doc)
	if len(missing) != 1 || missing[0] != "img_gone" {
		t.Fatalf("missing = %v, want only img_gone once", missing)
	}
}
