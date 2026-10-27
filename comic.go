package main

// The comic book workspace's document format — the page-and-panel JSON a
// comic artifact (cbc_<hex>.json) carries: an ordered book of pages, each
// with its own aspect ratio and an ordered list of panels, each panel a
// POLYGON of normalized page coordinates with an optional image clipped
// inside it. Polygon-first is the founding decision: a regular panel is just
// the 4-point case, so rectangular grids cost nothing while irregular panels
// — the comic grammar this editor exists for — are the same schema, not a
// bolt-on. This file owns the schema, the validation/normalization rules,
// seeding, and the artifact writer; the binding layer (comic_editor.go) and
// the frontend's comicModel.ts mirror it, the composition trio's shape. Like
// composition.go the document references media by artifact id + absolute
// path (the cross-conversation mention shape) with a facts snapshot — here
// just the pixel size, measured by stdlib image_dims.go, so no ffprobe, no
// CLI, and no key sit behind this editor at all. Text elements (captions,
// speech balloons) are deliberately deferred: the page/panel structure
// leaves room for a future element array without a schema bump.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	comicSchemaVersion = 1
	// Soft limits the editor enforces client-side; validation refuses beyond
	// them so a runaway paste can't build an unrenderable book.
	comicMaxPages          = 64
	comicMaxPanelsPerPage  = 24
	comicMaxPanelVertices  = 16
	comicMaxUnplacedImages = 50

	// A panel smaller than this fraction of the page area is a degenerate
	// sliver — it cannot hold a readable image or a visible border.
	comicMinPanelArea = 0.001

	comicGutterMin     = 0.0
	comicGutterMax     = 0.15
	comicGutterDefault = 0.02
	comicBorderMin     = 0.0
	comicBorderMax     = 0.03
	comicBorderDefault = 0.004

	comicFocusMin     = 0.0
	comicFocusMax     = 1.0
	comicFocusDefault = 0.5
	comicZoomMin      = 1.0
	comicZoomMax      = 4.0

	comicCustomAspectMin = 0.25
	comicCustomAspectMax = 4.0

	comicFitCover   = "cover"
	comicFitContain = "contain"

	comicDefaultAspect = "comic"
	comicDefaultName   = "Untitled comic"
)

// comicAspectPresets names the page shapes the editor offers. "comic" is the
// standard US comic page (6.625″ × 10.25″); the rest cover manga, the
// webtoon-style tall strip, and the common screens. Values are width ÷
// height.
var comicAspectPresets = map[string]float64{
	"comic":      6.625 / 10.25,
	"manga":      5.0 / 7.5,
	"portrait":   3.0 / 4.0,
	"square":     1.0,
	"landscape":  4.0 / 3.0,
	"widescreen": 16.0 / 9.0,
	"tall":       9.0 / 16.0,
}

// comicAspectPresetNames orders the presets for pickers and error text — a
// map alone would randomize both.
var comicAspectPresetNames = []string{"comic", "manga", "portrait", "square", "landscape", "widescreen", "tall"}

// ComicMediaRef pins one image artifact the comic uses — CompositionMediaRef
// scaled to stills: an absolute Path plus the owning conversation (the
// cross-conversation reference shape), and a facts snapshot of the pixel
// size measured when the ref enters the document. The renderer re-reads the
// file at render time, so a stale snapshot costs crop precision, never
// correctness.
type ComicMediaRef struct {
	ArtifactID     string `json:"artifactId"`
	ConversationID string `json:"conversationId,omitempty"`
	// Path is the absolute path of the artifact file on disk.
	Path     string `json:"path"`
	MimeType string `json:"mimeType,omitempty"`
	// Kind is always "image" — the comic takes stills only.
	Kind   string `json:"kind"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ComicPoint is one polygon vertex in normalized page coordinates — [0,1] on
// both axes, y growing downward (SVG space). Normalized points keep the
// document resolution-independent: the page's aspect ratio decides the
// pixel canvas at render time, never the stored geometry.
type ComicPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ComicPanelImage is the image clipped inside a panel. Fit "cover" (the
// default) fills the polygon and crops the overflow around a focal point —
// dragging the image inside the panel moves the focus; "contain" fits the
// whole image inside the polygon and rests it on the panel's empty color.
// Zoom multiplies the cover scale so a panel can push in on a detail. The
// fields are always written (no omitempty): the editor emits explicit values,
// so a document missing them is a hand-written one, and validation clamps the
// zero decode onto the sane reading (fit → cover, zoom → 1, focus stays 0 —
// the top-left corner it names).
type ComicPanelImage struct {
	Ref ComicMediaRef `json:"ref"`
	Fit string        `json:"fit"`
	// FocusX/FocusY pick which part of the image a cover crop shows — the
	// normalized focal point, 0.5/0.5 (dead center) in editor-written docs.
	FocusX float64 `json:"focusX"`
	FocusY float64 `json:"focusY"`
	// Zoom multiplies the cover scale, clamped to [1,4]; 1 = fill exactly.
	Zoom float64 `json:"zoom"`
}

// ComicPanel is one frame on the page: a closed polygon of 3–16 vertices
// with an optional image. The polygon IS the schema — a rectangular panel is
// the 4-point case, so a regular grid and a jagged irregular frame validate
// and render identically. Panels may overlap (comic grammar's insets and
// breakouts): later panels in the page's array paint on top. Panels carry no
// per-panel border in v1 — the book's Style draws every frame the same,
// which keeps the document small and the inspector honest; a per-panel
// override (borderless splash pages) is a deliberate later addition.
type ComicPanel struct {
	ID     string           `json:"id"`
	Points []ComicPoint     `json:"points"`
	Image  *ComicPanelImage `json:"image,omitempty"`
}

// ComicPage is one page of the book: its own aspect ratio and its panels in
// paint order. Aspect names a comicAspectPresets entry, or "custom" to take
// CustomAspect (width ÷ height) directly.
type ComicPage struct {
	ID           string       `json:"id"`
	Aspect       string       `json:"aspect,omitempty"`
	CustomAspect float64      `json:"customAspect,omitempty"`
	Panels       []ComicPanel `json:"panels"`
}

// ComicStyle is the book's chrome: the gutter between panels and the panel
// border stroke, both fractions of page width so the whole book scales with
// the export size untouched.
type ComicStyle struct {
	Gutter float64 `json:"gutter,omitempty"`
	Border float64 `json:"border,omitempty"`
}

// ComicDoc is the whole editable book — what a cbc_ artifact stores and what
// the editor edits. Revisions are immutable snapshots: every save writes a
// fresh artifact (the composition rule). Unplaced holds images picked into
// the document but not yet assigned a panel — the seeded draft's holding
// area ("nothing is arranged"): they persist with the book, so a reopen
// never loses an image the user picked but hadn't placed.
type ComicDoc struct {
	SchemaVersion int             `json:"schemaVersion"`
	Name          string          `json:"name,omitempty"`
	Pages         []ComicPage     `json:"pages"`
	Unplaced      []ComicMediaRef `json:"unplaced"`
	Style         ComicStyle      `json:"style"`
}

// comicArtifactExtension reports ".json" when a history content entry is a
// comic artifact. The Type field is the identity (several things could share
// .json); the extension check keeps a hand-renamed artifact from masquerading
// — the same two-step read as compositionArtifactExtension.
func comicArtifactExtension(content HistoryContent) string {
	if content.Type != "comic" {
		return ""
	}
	if strings.ToLower(filepath.Ext(content.Path)) == ".json" {
		return ".json"
	}
	if strings.EqualFold(content.MimeType, "application/json") {
		return ".json"
	}
	return ""
}

// parseComicDoc decodes artifact bytes into a document. Schema versions above
// the current one refuse — a newer document may carry meanings this build
// would silently drop.
func parseComicDoc(data []byte) (ComicDoc, error) {
	var doc ComicDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return ComicDoc{}, fmt.Errorf("the comic document is not valid JSON: %w", err)
	}
	if doc.SchemaVersion > comicSchemaVersion {
		return ComicDoc{}, fmt.Errorf("comic document schema %d is newer than this build understands (%d)", doc.SchemaVersion, comicSchemaVersion)
	}
	if doc.SchemaVersion < comicSchemaVersion {
		doc.SchemaVersion = comicSchemaVersion
	}
	return doc, nil
}

// comicFinite reports whether every value is finite (NaN and ±Inf must never
// reach a persisted document — they poison every later comparison).
func comicFinite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

// comicClampRange pulls a value into [min, max], sending a non-finite value
// to the fallback.
func comicClampRange(value, min, max, fallback float64) float64 {
	if !comicFinite(value) {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// comicPageAspect resolves a page's width ÷ height: a preset by name, the
// page's own custom ratio clamped to the sane extremes (a 25:1 sliver is a
// mistake, not a format), or the default preset when the name is unknown —
// a hand-edited document still renders.
func comicPageAspect(page ComicPage) (float64, string) {
	if page.Aspect == "custom" && comicFinite(page.CustomAspect) && page.CustomAspect > 0 {
		return comicClampRange(page.CustomAspect, comicCustomAspectMin, comicCustomAspectMax, 1), "custom"
	}
	if ratio, ok := comicAspectPresets[page.Aspect]; ok {
		return ratio, page.Aspect
	}
	return comicAspectPresets[comicDefaultAspect], comicDefaultAspect
}

// comicSignedArea is the shoelace signed area — positive when the polygon
// winds clockwise in y-down space (the editor's SVG screen orientation).
func comicSignedArea(points []ComicPoint) float64 {
	sum := 0.0
	for i := range points {
		a := points[i]
		b := points[(i+1)%len(points)]
		sum += a.X*b.Y - b.X*a.Y
	}
	return sum / 2
}

// comicNormalizeWinding rewrites the ring to clockwise so both spellings of
// the same shape round-trip to the same bytes — a hand-authored
// counterclockwise polygon normalizes on save like any other clamp.
func comicNormalizeWinding(points []ComicPoint) {
	if comicSignedArea(points) < 0 {
		for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
			points[i], points[j] = points[j], points[i]
		}
	}
}

// comicCross is the z component of (b−a) × (p−a): which side of the line
// a→b the point p sits on.
func comicCross(a, b, p ComicPoint) float64 {
	return (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
}

// comicSegmentsCross reports whether segment a1→a2 properly crosses b1→b2 —
// the strict straddle test. Touching at a point or riding collinear does not
// count (a vertex may legitimately sit on a far edge), and adjacent edges
// share a vertex by construction, so they can never read as a crossing.
func comicSegmentsCross(a1, a2, b1, b2 ComicPoint) bool {
	d1 := comicCross(b1, b2, a1)
	d2 := comicCross(b1, b2, a2)
	d3 := comicCross(a1, a2, b1)
	d4 := comicCross(a1, a2, b2)
	return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))
}

// comicSelfIntersects reports whether any two non-adjacent edges of the ring
// properly cross — the bowtie a careless vertex drag can produce and the one
// polygon state the editor refuses to build. n ≤ comicMaxPanelVertices, so
// the quadratic walk is nothing.
func comicSelfIntersects(points []ComicPoint) bool {
	n := len(points)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if j == i+1 || (i == 0 && j == n-1) {
				continue // adjacent edges share a vertex
			}
			if comicSegmentsCross(points[i], points[(i+1)%n], points[j], points[(j+1)%n]) {
				return true
			}
		}
	}
	return false
}

// validateComicDoc checks and normalizes a document in place — the Go half
// of a contract the frontend's comicModel mirrors. Normalizing (canonical
// winding, clamped style and image values, filled defaults) matches what the
// editor does interactively, so a save that reaches an untouched rule still
// round-trips; the errors are for the states the editor refuses to build:
// self-intersecting or degenerate polygons, duplicate ids, unmeasured or
// non-image refs. Overlapping panels are deliberately LEGAL — insets and
// breakouts are comic grammar, and paint order resolves them.
func validateComicDoc(doc *ComicDoc) error {
	doc.Name = strings.TrimSpace(doc.Name)
	if doc.Name == "" {
		doc.Name = comicDefaultName
	}
	doc.SchemaVersion = comicSchemaVersion
	if len(doc.Pages) == 0 {
		return errors.New("a comic needs at least one page")
	}
	if len(doc.Pages) > comicMaxPages {
		return fmt.Errorf("a comic supports at most %d pages, got %d", comicMaxPages, len(doc.Pages))
	}
	if len(doc.Unplaced) > comicMaxUnplacedImages {
		return fmt.Errorf("a comic holds at most %d unplaced images, got %d", comicMaxUnplacedImages, len(doc.Unplaced))
	}
	doc.Style.Gutter = comicClampRange(doc.Style.Gutter, comicGutterMin, comicGutterMax, comicGutterDefault)
	doc.Style.Border = comicClampRange(doc.Style.Border, comicBorderMin, comicBorderMax, comicBorderDefault)
	if doc.Unplaced == nil {
		doc.Unplaced = []ComicMediaRef{}
	}

	seenIDs := map[string]bool{}
	claimID := func(id, label string) error {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s has an empty id", label)
		}
		if seenIDs[id] {
			return fmt.Errorf("%s reuses id %q", label, id)
		}
		seenIDs[id] = true
		return nil
	}

	for pi := range doc.Pages {
		page := &doc.Pages[pi]
		label := fmt.Sprintf("page %d", pi+1)
		if err := claimID(page.ID, label); err != nil {
			return err
		}
		switch page.Aspect {
		case "", "custom":
		default:
			if _, ok := comicAspectPresets[page.Aspect]; !ok {
				return fmt.Errorf("%s has unknown aspect %q — use a preset (%s) or \"custom\"", label, page.Aspect, strings.Join(comicAspectPresetNames, ", "))
			}
		}
		if page.Aspect == "" {
			page.Aspect = comicDefaultAspect
		}
		if page.Aspect == "custom" {
			if !comicFinite(page.CustomAspect) || page.CustomAspect <= 0 {
				return fmt.Errorf("%s's custom aspect must be a positive width ÷ height", label)
			}
			page.CustomAspect = comicClampRange(page.CustomAspect, comicCustomAspectMin, comicCustomAspectMax, 1)
		}
		if page.Panels == nil {
			page.Panels = []ComicPanel{}
		}
		if len(page.Panels) > comicMaxPanelsPerPage {
			return fmt.Errorf("%s supports at most %d panels, got %d", label, comicMaxPanelsPerPage, len(page.Panels))
		}
		for ii := range page.Panels {
			panel := &page.Panels[ii]
			panelLabel := fmt.Sprintf("%s panel %d", label, ii+1)
			if err := claimID(panel.ID, panelLabel); err != nil {
				return err
			}
			if len(panel.Points) < 3 {
				return fmt.Errorf("%s needs at least 3 vertices, got %d", panelLabel, len(panel.Points))
			}
			if len(panel.Points) > comicMaxPanelVertices {
				return fmt.Errorf("%s supports at most %d vertices, got %d", panelLabel, comicMaxPanelVertices, len(panel.Points))
			}
			for vi := range panel.Points {
				point := &panel.Points[vi]
				if !comicFinite(point.X, point.Y) {
					return fmt.Errorf("%s has a non-finite vertex", panelLabel)
				}
				// The editor clamps drags to the page; a hand edit slightly
				// out of range normalizes the same way.
				point.X = comicClampRange(point.X, 0, 1, 0)
				point.Y = comicClampRange(point.Y, 0, 1, 0)
			}
			// Self-intersection before area: a bowtie's two lobes cancel in
			// the shoelace formula, so the shape error must be named before
			// the sliver error can masquerade as one.
			comicNormalizeWinding(panel.Points)
			if comicSelfIntersects(panel.Points) {
				return fmt.Errorf("%s crosses itself — a panel polygon may not bowtie", panelLabel)
			}
			if area := math.Abs(comicSignedArea(panel.Points)); area < comicMinPanelArea {
				return fmt.Errorf("%s is too small to be a panel (area %g of the page; the minimum is %g)", panelLabel, area, comicMinPanelArea)
			}
			if panel.Image != nil {
				image := panel.Image
				if image.Ref.Kind != "image" {
					return fmt.Errorf("%s's image references %s media — comic panels hold images", panelLabel, comicRefKindPhrase(image.Ref.Kind))
				}
				if image.Ref.Width <= 0 || image.Ref.Height <= 0 {
					return fmt.Errorf("%s's image was never measured (no pixel size)", panelLabel)
				}
				switch image.Fit {
				case "", comicFitCover, comicFitContain:
				default:
					return fmt.Errorf("%s's image fit must be %q or %q, got %q", panelLabel, comicFitCover, comicFitContain, image.Fit)
				}
				if image.Fit == "" {
					image.Fit = comicFitCover
				}
				image.FocusX = comicClampRange(image.FocusX, comicFocusMin, comicFocusMax, comicFocusDefault)
				image.FocusY = comicClampRange(image.FocusY, comicFocusMin, comicFocusMax, comicFocusDefault)
				image.Zoom = comicClampRange(image.Zoom, comicZoomMin, comicZoomMax, 1)
			}
		}
	}

	for ui := range doc.Unplaced {
		ref := &doc.Unplaced[ui]
		if ref.Kind != "image" {
			return fmt.Errorf("unplaced image %d references %s media — the comic takes stills only", ui+1, comicRefKindPhrase(ref.Kind))
		}
		if ref.Width <= 0 || ref.Height <= 0 {
			return fmt.Errorf("unplaced image %d was never measured (no pixel size)", ui+1)
		}
	}
	return nil
}

// orPhrase words an unknown media kind for errors.
func comicRefKindPhrase(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "unknown"
	}
	return kind
}

// comicDocSummary words the save turn's one-line record.
func comicDocSummary(doc ComicDoc) string {
	panels := 0
	for _, page := range doc.Pages {
		panels += len(page.Panels)
	}
	aspect := comicDefaultAspect
	if len(doc.Pages) > 0 {
		_, aspect = comicPageAspect(doc.Pages[0])
	}
	return fmt.Sprintf("%s · %s · %s format", comicPlural(len(doc.Pages), "page"), comicPlural(panels, "panel"), aspect)
}

// comicPlural renders "1 page" / "3 pages".
func comicPlural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// seedComicDoc builds a starting book from resolved image refs — the
// seeded-only contract: images land in the unplaced tray exactly as if the
// user had picked them from the picker in that order, and nothing is
// arranged (no panels are built — arranging the frames is the human's half
// of the loop). One blank page in the default format opens ready to draw on.
func seedComicDoc(name string, refs []ComicMediaRef) ComicDoc {
	doc := ComicDoc{
		SchemaVersion: comicSchemaVersion,
		Name:          strings.TrimSpace(name),
		Pages: []ComicPage{{
			ID:     randomID("cpg"),
			Aspect: comicDefaultAspect,
			Panels: []ComicPanel{},
		}},
		Unplaced: []ComicMediaRef{},
		Style:    ComicStyle{Gutter: comicGutterDefault, Border: comicBorderDefault},
	}
	if doc.Name == "" {
		doc.Name = comicDefaultName
	}
	doc.Unplaced = append(doc.Unplaced, refs...)
	return doc
}

// writeComicArtifact persists a document as a fresh cbc_<hex>.json artifact —
// the identity the editor reopens, the assets panel lists, and an export
// pins. The write is verified by parsing it back (the content-detected
// idiom: junk never lands under a cbc_ name).
func writeComicArtifact(artifactsDir string, doc ComicDoc) (HistoryContent, error) {
	if err := validateComicDoc(&doc); err != nil {
		return HistoryContent{}, err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return HistoryContent{}, err
	}
	data = append(data, '\n')
	if _, err := parseComicDoc(data); err != nil {
		return HistoryContent{}, fmt.Errorf("the rendered comic document failed its own parse: %w", err)
	}
	artifactID := randomID("cbc")
	filename := artifactID + ".json"
	if err := os.WriteFile(filepath.Join(artifactsDir, filename), data, 0644); err != nil {
		return HistoryContent{}, err
	}
	return HistoryContent{
		Type:       "comic",
		ArtifactID: artifactID,
		Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
		MimeType:   "application/json",
	}, nil
}

// comicMissingRefs lists the artifact ids whose files are gone on disk — the
// composer's identify-only report. The document keeps the entries (the book
// stays intact); the editor flags them and an export refuses while any
// remain.
func comicMissingRefs(doc ComicDoc) []string {
	seen := map[string]bool{}
	missing := []string{}
	check := func(ref ComicMediaRef) {
		if ref.ArtifactID == "" || seen[ref.ArtifactID] {
			return
		}
		seen[ref.ArtifactID] = true
		if _, err := os.Stat(ref.Path); err != nil {
			missing = append(missing, ref.ArtifactID)
		}
	}
	for _, page := range doc.Pages {
		for _, panel := range page.Panels {
			if panel.Image != nil {
				check(panel.Image.Ref)
			}
		}
	}
	for _, ref := range doc.Unplaced {
		check(ref)
	}
	return missing
}
