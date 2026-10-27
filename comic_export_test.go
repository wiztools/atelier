package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// comicExportImageFixture writes a 4×2 solid-color still in the requested
// format and returns the ref pointing at it.
func comicExportImageFixture(t *testing.T, dir, id, format string) ComicMediaRef {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.Set(1, 1, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	var data []byte
	switch format {
	case "png":
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("png.Encode: %v", err)
		}
		data = buf.Bytes()
	case "jpeg":
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, nil); err != nil {
			t.Fatalf("jpeg.Encode: %v", err)
		}
		data = buf.Bytes()
	default:
		t.Fatalf("unknown fixture format %q", format)
	}
	path := filepath.Join(dir, id+"."+format)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
	}
	return ComicMediaRef{ArtifactID: id, Path: path, MimeType: mime, Kind: "image", Width: 4, Height: 2}
}

func comicExportDoc(refs ...ComicMediaRef) ComicDoc {
	doc := seedComicDoc("Export Issue", refs)
	panels := []ComicPanel{{ID: "pnl1", Points: comicRect(0.05, 0.05, 0.9, 0.45)}}
	if len(refs) > 0 {
		panels[0].Image = &ComicPanelImage{Ref: refs[0]}
	}
	doc.Pages[0].Panels = panels
	doc.Pages[0].Texts = []ComicText{
		{ID: "txt1", Kind: comicTextKindCaption, X: 0.05, Y: 0.02, Width: 0.3, Height: 0.05, Text: "Meanwhile…", Size: 1},
		{ID: "txt2", Kind: comicTextKindBalloon, X: 0.55, Y: 0.55, Width: 0.2, Height: 0.12, Text: "Look out!", Size: 1, Tail: &ComicTail{X: 0.6, Y: 0.75}},
		{ID: "txt3", Kind: comicTextKindSFX, X: 0.2, Y: 0.6, Width: 0.16, Height: 0.12, Text: "POW!", Size: 1.8},
	}
	doc.Pages = append(doc.Pages, ComicPage{ID: "p2", Aspect: "square", Panels: []ComicPanel{}, Texts: []ComicText{}})
	return doc
}

func TestComicGeometryMirrors(t *testing.T) {
	// Placement mirrors the frontend's math: center focus centers the crop,
	// contain centers regardless, zoom doubles, focus pins.
	ring := comicRect(0.1, 0.1, 0.8, 0.3)
	cover := comicImagePlacementRect(ring, 800, 600, "cover", 0.5, 0.5, 1, 1000, 1000)
	if cover.w < 800 || cover.h < 300 {
		t.Fatalf("cover = %+v, want the box filled", cover)
	}
	if math.Abs(cover.x+cover.w/2-500) > 1e-6 {
		t.Fatalf("center focus = %g, want the crop centered", cover.x+cover.w/2)
	}
	contained := comicImagePlacementRect(ring, 800, 600, "contain", 0.9, 0.9, 1, 1000, 1000)
	if contained.w > 800+1e-6 || contained.h > 300+1e-6 {
		t.Fatalf("contain = %+v, want the image inside", contained)
	}
	if math.Abs(contained.x+contained.w/2-500) > 1e-6 {
		t.Fatalf("contain centers regardless of focus; got %g", contained.x+contained.w/2)
	}
	zoomed := comicImagePlacementRect(ring, 800, 600, "cover", 0.5, 0.5, 2, 1000, 1000)
	if math.Abs(zoomed.w-cover.w*2) > 1e-6 {
		t.Fatalf("zoom width = %g, want %g", zoomed.w, cover.w*2)
	}
	// A portrait box with a landscape image has horizontal slack — the slide axis.
	tallRing := comicRect(0.1, 0.1, 0.3, 0.8)
	centered := comicImagePlacementRect(tallRing, 800, 600, "cover", 0.5, 0.5, 1, 1000, 1000)
	pinned := comicImagePlacementRect(tallRing, 800, 600, "cover", 0, 0, 1, 1000, 1000)
	if pinned.x <= centered.x {
		t.Fatalf("focus 0 must pin the left edge into view (x %g vs center %g)", pinned.x, centered.x)
	}

	// The wrap is the frontend's: greedy words at 0.6em, explicit newlines
	// kept, a long word whole.
	lines := comicWrapLines("the quick brown fox jumps over the dog", 120, 20)
	want := []string{"the quick", "brown fox", "jumps over", "the dog"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines = %v, want %v", lines, want)
		}
	}
	if got := comicWrapLines("one\ntwo", 500, 20); len(got) != 2 {
		t.Fatalf("explicit newlines = %v", got)
	}
	if got := comicWrapLines("extraordinarily", 60, 20); len(got) != 1 {
		t.Fatalf("a long word must stand alone, got %v", got)
	}

	// The burst fills its box with 24 points; the tail base hugs the edge
	// toward the tip.
	burst := comicSFXPolygon(ComicText{X: 0.4, Y: 0.4, Width: 0.2, Height: 0.1})
	if len(burst) != 24 {
		t.Fatalf("burst points = %d, want 24", len(burst))
	}
	for _, point := range burst {
		if point.X < 0.4-1e-9 || point.X > 0.6+1e-9 || point.Y < 0.4-1e-9 || point.Y > 0.5+1e-9 {
			t.Fatalf("burst point %+v escapes its box", point)
		}
	}
	tailText := ComicText{X: 0.4, Y: 0.4, Width: 0.2, Height: 0.1, Tail: &ComicTail{X: 0.8, Y: 0.9}}
	_, baseA, baseB := comicBalloonTailTriangle(tailText)
	for _, base := range []ComicPoint{baseA, baseB} {
		if base.X < 0.5-1e-9 || base.X > 0.6+1e-9 || base.Y < 0.45-1e-9 || base.Y > 0.5+1e-9 {
			t.Fatalf("tail base %+v misses the bubble edge nearest the tip", base)
		}
	}
}

func TestRenderComicPDF(t *testing.T) {
	config := editTestHome(t)
	dir := t.TempDir()
	pngRef := comicExportImageFixture(t, dir, "img_png", "png")
	jpegRef := comicExportImageFixture(t, dir, "img_jpg", "jpeg")

	doc := comicExportDoc(pngRef, jpegRef)
	doc.Pages[0].Panels = append(doc.Pages[0].Panels,
		ComicPanel{ID: "pnl2", Points: comicRect(0.05, 0.55, 0.44, 0.4), Image: &ComicPanelImage{Ref: pngRef}},
		ComicPanel{ID: "pnl3", Points: comicRect(0.5, 0.55, 0.45, 0.4), Image: &ComicPanelImage{Ref: jpegRef, Fit: "contain"}},
	)
	// The same art in two panels must embed ONCE.
	rendered, err := renderComicPDF(context.Background(), config, doc)
	if err != nil {
		t.Fatalf("renderComicPDF returned error: %v", err)
	}
	if !bytes.HasPrefix(rendered, []byte("%PDF-1.4")) || !bytes.Contains(rendered, []byte("%%EOF")) {
		t.Fatal("the render is not a PDF")
	}
	if !strings.Contains(string(rendered), "/Count 2") {
		t.Fatal("the two-page book rendered one page")
	}
	if !strings.Contains(string(rendered), "/Subtype /Image /Width 4 /Height 2 /BitsPerComponent 8 /Filter /FlateDecode /ColorSpace /DeviceRGB") {
		t.Fatal("the PNG did not embed as FlateDecode RGB")
	}
	if strings.Count(string(rendered), "/DCTDecode") != 1 {
		t.Fatal("the JPEG did not pass through once")
	}
	// 477pt trim width; the square second page is 477 tall.
	if !strings.Contains(string(rendered), "/MediaBox [0 0 477.00 477.00]") {
		t.Fatal("the square page's MediaBox is wrong")
	}
	// Panels clip, images draw, lettering shows.
	for _, marker := range []string{" W n", "/Im0 Do", "/Im1 Do", "(Meanwhile...)", "(Look out!)", "(POW!)", "Helvetica-Bold"} {
		if !strings.Contains(string(rendered), marker) {
			t.Fatalf("the render misses %q", marker)
		}
	}

	// A missing image refuses the export outright.
	broken := doc
	broken.Pages[0].Panels[1].Image.Ref.Path = "/nonexistent/img.png"
	if _, err := renderComicPDF(context.Background(), config, broken); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing-image error = %v", err)
	}

	// The xref table is walked for real: every offset lands on its numbered
	// object, and every Kids target parses as a page dict — a bare
	// /Resources value once read /Font as the dict and broke every parser
	// while string greps stayed green.
	comicAssertPDFStructure(t, rendered, 2)
}

// comicAssertPDFStructure walks a rendered PDF the way a parser does: the
// startxref pointer lands on the table, the table's offsets resolve to their
// numbered objects, and the Pages tree's kids are page dictionaries — a bare
// /Resources value once read /Font as the dict and broke every parser while
// string greps stayed green.
func comicAssertPDFStructure(t *testing.T, rendered []byte, wantPages int) {
	t.Helper()
	data := string(rendered)
	xrefStart := strings.LastIndex(data, "\nxref\n") + 1
	if xrefStart == 0 {
		t.Fatal("no xref table")
	}
	fields := strings.Fields(data[xrefStart:])
	if len(fields) < 3 || fields[0] != "xref" || fields[1] != "0" {
		t.Fatalf("xref header = %v", fields[:min(3, len(fields))])
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		t.Fatalf("xref count %q: %v", fields[2], err)
	}
	entries := fields[3:]
	if len(entries) < count*3 {
		t.Fatalf("xref carries %d fields for %d objects", len(entries), count)
	}
	for object := 1; object < count; object++ {
		// Entry 0 is the free object; each entry is offset, generation, kind.
		offset, err := strconv.Atoi(entries[3+(object-1)*3])
		if err != nil {
			t.Fatalf("xref offset for object %d: %v", object, err)
		}
		head := data[offset:]
		if !strings.HasPrefix(head, fmt.Sprintf("%d 0 obj", object)) {
			t.Fatalf("xref offset %d lands on %q, want object %d", offset, strings.SplitN(head, "\n", 2)[0], object)
		}
	}
	tail := strings.Fields(data[strings.LastIndex(data, "startxref"):])
	if len(tail) < 2 || tail[0] != "startxref" {
		t.Fatal("no startxref")
	}
	pointer, err := strconv.Atoi(tail[1])
	if err != nil || pointer != xrefStart {
		t.Fatalf("startxref = %d, want the table at %d", pointer, xrefStart)
	}
	if !strings.Contains(data, fmt.Sprintf("/Count %d >>", wantPages)) {
		t.Fatalf("pages count ≠ %d", wantPages)
	}
	for _, match := range regexp.MustCompile(`/Type /Pages /Kids \[([^\]]+)\]`).FindAllStringSubmatch(data, -1) {
		references := strings.Fields(match[1])
		for i := 0; i+2 < len(references); i += 3 {
			if references[i+1] != "0" || references[i+2] != "R" {
				t.Fatalf("kid group %q unreadable", references[i:i+3])
			}
			id, err := strconv.Atoi(references[i])
			if err != nil {
				t.Fatalf("kid %q unreadable", references[i])
			}
			at := strings.Index(data, fmt.Sprintf("\n%d 0 obj\n", id))
			if at < 0 || !strings.HasPrefix(data[at:], fmt.Sprintf("\n%d 0 obj\n<< /Type /Page ", id)) {
				t.Fatalf("kid %d is not a page dict", id)
			}
		}
	}
}

func TestLoadComicPDFImageLadder(t *testing.T) {
	config := editTestHome(t)
	dir := t.TempDir()

	// PNG decodes to FlateDecode RGB.
	pngRef := comicExportImageFixture(t, dir, "img_a", "png")
	loaded, err := loadComicPDFImage(context.Background(), config, pngRef)
	if err != nil {
		t.Fatalf("png load: %v", err)
	}
	if loaded.filter != "/FlateDecode" || loaded.colorSpace != "/DeviceRGB" || loaded.width != 4 || loaded.height != 2 {
		t.Fatalf("png = %+v", loaded)
	}

	// JPEG passes through with its real dimensions and the YCbCr transform.
	jpegRef := comicExportImageFixture(t, dir, "img_b", "jpeg")
	loaded, err = loadComicPDFImage(context.Background(), config, jpegRef)
	if err != nil {
		t.Fatalf("jpeg load: %v", err)
	}
	if loaded.filter != "/DCTDecode" || loaded.width != 4 || loaded.height != 2 || loaded.colorTransform != 1 {
		t.Fatalf("jpeg = %+v", loaded)
	}

	// Junk refuses naming convert_image.
	junk := ComicMediaRef{ArtifactID: "img_bad", Path: filepath.Join(dir, "img_bad.png"), Kind: "image", Width: 1, Height: 1}
	if err := os.WriteFile(junk.Path, []byte("not an image"), 0644); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	if _, err := loadComicPDFImage(context.Background(), config, junk); err == nil || !strings.Contains(err.Error(), "convert_image") {
		t.Fatalf("junk error = %v", err)
	}
}

func TestAdoptComicPagePNG(t *testing.T) {
	config := editTestHome(t)
	comicConversationFixture(t, config.Storage)
	app := NewApp()

	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	adopted, err := app.AdoptComicPagePNG(AdoptComicPagePNGRequest{
		ConversationID:     "conv_comic",
		Name:               "Issue #1",
		RevisionArtifactID: "cbc_src",
		PageLabel:          "1",
		PNGBase64:          encoded,
	})
	if err != nil {
		t.Fatalf("AdoptComicPagePNG returned error: %v", err)
	}
	if !strings.HasPrefix(adopted.ArtifactID, "img_") || !strings.HasPrefix(adopted.URL, artifactPrefix) || !strings.Contains(adopted.URL, adopted.ArtifactID) {
		t.Fatalf("adopted = %+v", adopted)
	}
	detail, err := getConversation(config.Storage, "conv_comic")
	if err != nil {
		t.Fatalf("getConversation: %v", err)
	}
	last := detail.Turns[len(detail.Turns)-1]
	if last.Role != "assistant" || !strings.Contains(last.Content[0].Text, "page 1") {
		t.Fatalf("adoption turn = %+v", last)
	}
	found := false
	for _, content := range last.Content {
		if content.Type == "image" && content.ArtifactID == adopted.ArtifactID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the adopted artifact is not on the turn: %+v", last.Content)
	}

	// Junk base64 refuses before anything is written.
	if _, err := app.AdoptComicPagePNG(AdoptComicPagePNGRequest{ConversationID: "conv_comic", PNGBase64: "not base64 %%%"}); err == nil {
		t.Fatal("junk PNG bytes must refuse")
	}
}

func TestComicDecodedImageRowOrder(t *testing.T) {
	// PDF image samples are TOP-DOWN: the first row of the FlateDecode
	// output must be the image's displayed top — the flip here is what stood
	// every decoded panel on its head.
	img := image.NewRGBA(image.Rect(0, 0, 2, 4))
	for y := 0; y < 2; y++ {
		img.Set(0, y, color.NRGBA{R: 255, A: 255})
		img.Set(1, y, color.NRGBA{R: 255, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	loaded, err := comicDecodedImage(buf.Bytes())
	if err != nil {
		t.Fatalf("comicDecodedImage: %v", err)
	}
	if loaded.colorSpace != "/DeviceRGB" || loaded.filter != "/FlateDecode" {
		t.Fatalf("loaded = %+v", loaded)
	}
	reader, err := zlib.NewReader(bytes.NewReader(loaded.data))
	if err != nil {
		t.Fatalf("zlib reader: %v", err)
	}
	rows, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("zlib read: %v", err)
	}
	// Row 0 (top): red. The bottom rows: transparent black composites white.
	if rows[0] != 255 || rows[1] != 0 || rows[2] != 0 {
		t.Fatalf("first output row = %v, want the image's top (red)", rows[:3])
	}
	last := rows[len(rows)-3:]
	if last[0] != 255 || last[1] != 255 || last[2] != 255 {
		t.Fatalf("last output row = %v, want white (transparent black over paper)", last)
	}
}
