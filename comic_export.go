package main

// The comic exports: a saved cbc_ revision renders to a multi-page PDF — the
// hand-rolled PDF writer's second tenant, beside the screenplay — and the
// frontend's own SVG rasterization adopts single pages into the conversation
// as image artifacts (AdoptComicPagePNG; the preview IS the export, so the
// browser rasterizes what the editor shows and only the bytes come through).
// The PDF side is Go's because a shareable, printable book deserves vector
// output: panels are polygons as clip paths (`m/l/h W n`), images are
// XObjects (JPEG passthrough via DCTDecode; PNG/GIF decoded to RGB and
// FlateDecode; anything else rides convertImageBytesForModel's basic image
// backend to JPEG, fail-soft to a named refusal), lettering is standard-14
// Helvetica-Bold vector text. The geometry mirrors (placement, wrap, burst,
// tail) restate the frontend model's math in page points — the two must
// agree to the pixel, so each carries its own table tests.

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// comicPDFPageWidthPt is the trim width: 6.625in — the standard US comic
// page — in points; each page's height follows its own aspect ratio.
const comicPDFPageWidthPt = 6.625 * 72

// comicPDFPageSize resolves a page's PDF dimensions from its aspect ratio.
func comicPDFPageSize(ratio float64) (float64, float64) {
	return comicPDFPageWidthPt, comicPDFPageWidthPt / ratio
}

// comicPlacementRect is a placement in page units.
type comicPlacementRect struct {
	x, y, w, h float64
}

// comicImagePlacementRect is the Go mirror of the frontend's
// comicImagePlacement — cover fills the panel's bounding box (× zoom) and
// slides so the focus fraction of the image shows; contain fits and centers.
// Each normalized axis scales by its own page dimension.
func comicImagePlacementRect(points []ComicPoint, imageWidth, imageHeight int, fit string, focusX, focusY, zoom float64, pageWidth, pageHeight float64) comicPlacementRect {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, point := range points {
		minX = math.Min(minX, point.X)
		minY = math.Min(minY, point.Y)
		maxX = math.Max(maxX, point.X)
		maxY = math.Max(maxY, point.Y)
	}
	bw := (maxX - minX) * pageWidth
	bh := (maxY - minY) * pageHeight
	if imageWidth <= 0 {
		imageWidth = 1
	}
	if imageHeight <= 0 {
		imageHeight = 1
	}
	if fit == "contain" {
		scale := math.Min(bw/float64(imageWidth), bh/float64(imageHeight))
		width := float64(imageWidth) * scale
		height := float64(imageHeight) * scale
		return comicPlacementRect{
			x: minX*pageWidth + (bw-width)/2,
			y: minY*pageHeight + (bh-height)/2,
			w: width,
			h: height,
		}
	}
	coverScale := math.Max(bw/float64(imageWidth), bh/float64(imageHeight))
	scale := coverScale * comicClampRange(zoom, comicZoomMin, comicZoomMax, 1)
	width := float64(imageWidth) * scale
	height := float64(imageHeight) * scale
	focus := comicClampRange(focusX, 0, 1, comicFocusDefault)
	focusYClamped := comicClampRange(focusY, 0, 1, comicFocusDefault)
	visibleLeft := focus * math.Max(0, width-bw)
	visibleTop := focusYClamped * math.Max(0, height-bh)
	return comicPlacementRect{x: minX*pageWidth - visibleLeft, y: minY*pageHeight - visibleTop, w: width, h: height}
}

// comicWrapLines mirrors the frontend's comicWrapText: greedy words, the
// 0.6em-per-character advance the wrap assumes, explicit newlines kept, a
// word longer than the box standing alone.
func comicWrapLines(content string, boxWidthPt, fontSizePt float64) []string {
	charWidth := math.Max(1, fontSizePt*0.6)
	maxChars := int(math.Max(4, math.Floor(boxWidthPt/charWidth)))
	lines := []string{}
	for _, paragraph := range strings.Split(content, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := words[0]
		for _, word := range words[1:] {
			if len([]rune(current))+1+len([]rune(word)) <= maxChars {
				current += " " + word
				continue
			}
			lines = append(lines, current)
			current = word
		}
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

// comicSFXPolygon mirrors comicSFXPoints: 12 spikes' worth of alternating
// outer/inner ellipse points around the box.
func comicSFXPolygon(text ComicText) []ComicPoint {
	cx := text.X + text.Width/2
	cy := text.Y + text.Height/2
	rx := text.Width / 2
	ry := text.Height / 2
	const spikes = 12
	points := make([]ComicPoint, 0, spikes*2)
	for i := 0; i < spikes*2; i++ {
		angle := math.Pi*2*float64(i)/(spikes*2) - math.Pi/2
		scale := 1.0
		if i%2 == 1 {
			scale = 0.72
		}
		points = append(points, ComicPoint{X: cx + math.Cos(angle)*rx*scale, Y: cy + math.Sin(angle)*ry*scale})
	}
	return points
}

// comicBalloonTailTriangle mirrors comicBalloonTailBase: the tail's tip plus
// the two bubble-edge base points flanking the direction to it (a quarter
// radian apart).
func comicBalloonTailTriangle(text ComicText) (ComicPoint, ComicPoint, ComicPoint) {
	tip := ComicPoint{X: text.X + text.Width/2, Y: text.Y + text.Height}
	if text.Tail != nil {
		tip = ComicPoint{X: text.Tail.X, Y: text.Tail.Y}
	}
	cx := text.X + text.Width/2
	cy := text.Y + text.Height/2
	rx := text.Width / 2
	ry := text.Height / 2
	if rx <= 0 {
		rx = 1
	}
	if ry <= 0 {
		ry = 1
	}
	angle := math.Atan2((tip.Y-cy)/ry, (tip.X-cx)/rx)
	const spike = 0.125
	pointAt := func(theta float64) ComicPoint {
		return ComicPoint{X: cx + math.Cos(theta)*rx, Y: cy + math.Sin(theta)*ry}
	}
	return tip, pointAt(angle - spike), pointAt(angle + spike)
}

// --- images --------------------------------------------------------------

// comicPDFImage is one embedded page resource: the bytes and the dictionary
// fields the content stream's Do operator needs.
type comicPDFImage struct {
	data           []byte
	filter         string // "/DCTDecode" or "/FlateDecode"
	width, height  int
	colorSpace     string // FlateDecode only
	colorTransform int    // DCTDecode only
}

// comicImageTimeout bounds one image conversion through the basic image
// backend (an HEIC original riding sips) — fail, don't hang the dialog.
const comicImageTimeout = 120 * time.Second

// loadComicPDFImage resolves a ref's file into embeddable form: JPEG passes
// through untouched (DCTDecode), PNG and GIF decode through the stdlib to
// RGB rows (FlateDecode), and anything else — HEIC, AVIF, WebP — rides
// convertImageBytesForModel's basic image backend to JPEG; a ref that
// survives no rung refuses naming convert_image.
func loadComicPDFImage(ctx context.Context, config AppConfig, ref ComicMediaRef) (comicPDFImage, error) {
	data, err := os.ReadFile(ref.Path)
	if err != nil {
		return comicPDFImage{}, fmt.Errorf("reading %s failed: %w", ref.ArtifactID, err)
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 {
		return comicJPEGImage(data)
	}
	if decoded, err := comicDecodedImage(data); err == nil {
		return decoded, nil
	}
	converted, err := convertImageBytesForModel(ctx, config, data, strings.ToLower(filepathExtensionFor(ref)))
	if err == nil && len(converted) >= 3 && converted[0] == 0xff && converted[1] == 0xd8 {
		return comicJPEGImage(converted)
	}
	return comicPDFImage{}, fmt.Errorf("%s could not be embedded — convert it to PNG or JPEG (convert_image) first", ref.ArtifactID)
}

// filepathExtensionFor words a ref's extension for the converter's staging
// input (sips/IM dispatch on it).
func filepathExtensionFor(ref ComicMediaRef) string {
	refPath := ref.Path
	dot := strings.LastIndex(refPath, ".")
	if dot < 0 {
		return ".png"
	}
	return refPath[dot:]
}

// comicJPEGImage passes JPEG bytes through with the dictionary the decoder
// needs: dimensions from the sniffer, the component count off the SOF
// segment deciding the color transform (1-channel grayscale renders wrong
// with the YCbCr transform on).
func comicJPEGImage(data []byte) (comicPDFImage, error) {
	width, height, ok := imagePixelDimensions(data)
	if !ok {
		return comicPDFImage{}, errors.New("the JPEG's dimensions could not be read")
	}
	colorSpace := "/DeviceRGB"
	transform := 1
	if jpegColorComponents(data) == 1 {
		colorSpace = "/DeviceGray"
		transform = 0
	}
	return comicPDFImage{data: data, filter: "/DCTDecode", width: int(width), height: int(height), colorSpace: colorSpace, colorTransform: transform}, nil
}

// jpegColorComponents scans the marker segments for the SOFn and returns its
// component count (0 when the scan fails — the caller defaults the
// transform).
func jpegColorComponents(data []byte) int {
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			offset++
			continue
		}
		marker := data[offset+1]
		if marker == 0xd8 || (0xd0 <= marker && marker <= 0xd7) || marker == 0x01 {
			offset += 2
			continue
		}
		if offset+5 > len(data) {
			return 0
		}
		segmentLen := int(data[offset+2])<<8 | int(data[offset+3])
		if segmentLen < 2 {
			return 0
		}
		if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
			if offset+10 <= len(data) {
				return int(data[offset+9])
			}
			return 0
		}
		offset += 2 + segmentLen
	}
	return 0
}

// comicDecodedImage decodes a stdlib-decodable still (PNG, GIF's first
// frame — the decoders register via the blank imports) to top-down RGB rows
// with alpha composited over white. TOP-DOWN, not flipped: unlike the page's
// y-up coordinate space, a PDF image's sample matrix stores its FIRST row as
// the displayed TOP (the same orientation as PNG/GIF and DCTDecode's JPEG —
// which is exactly why passthrough needs no flip), so flipping here stands
// every decoded panel on its head.
func comicDecodedImage(data []byte) (comicPDFImage, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return comicPDFImage{}, err
	}
	frame, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return comicPDFImage{}, err
	}
	bounds := frame.Bounds()
	rows := make([]byte, bounds.Dy()*bounds.Dx()*3)
	for y := 0; y < bounds.Dy(); y++ {
		destRow := y * bounds.Dx() * 3
		for x := 0; x < bounds.Dx(); x++ {
			c := color.NRGBAModel.Convert(frame.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			// Alpha composited over the page's white paper, for real — the
			// raw RGB of a transparent pixel is arbitrary and would smear.
			alpha := uint32(c.A)
			base := destRow + x*3
			rows[base] = uint8((uint32(c.R)*alpha + 255*(255-alpha)) / 255)
			rows[base+1] = uint8((uint32(c.G)*alpha + 255*(255-alpha)) / 255)
			rows[base+2] = uint8((uint32(c.B)*alpha + 255*(255-alpha)) / 255)
		}
	}
	var z bytes.Buffer
	writer := zlib.NewWriter(&z)
	if _, err := writer.Write(rows); err != nil {
		return comicPDFImage{}, err
	}
	if err := writer.Close(); err != nil {
		return comicPDFImage{}, err
	}
	return comicPDFImage{data: z.Bytes(), filter: "/FlateDecode", width: cfg.Width, height: cfg.Height, colorSpace: "/DeviceRGB"}, nil
}

// --- PDF assembly --------------------------------------------------------

// comicPDFBuilder accumulates objects and their xref offsets — pdfWrite's
// shape, parameterized because a comic's object count varies with its image
// count.
type comicPDFBuilder struct {
	w       *pdfBuf
	offsets []int
}

func newComicPDFBuilder() *comicPDFBuilder {
	b := &comicPDFBuilder{w: &pdfBuf{}}
	b.w.writeString("%PDF-1.4\n")
	return b
}

func (b *comicPDFBuilder) raw(format string, args ...any) int {
	id := len(b.offsets) + 1
	b.offsets = append(b.offsets, b.w.len())
	fmt.Fprintf(b.w.raw(), "%d 0 obj\n%s\nendobj\n", id, fmt.Sprintf(format, args...))
	return id
}

func (b *comicPDFBuilder) stream(dict string, payload []byte) int {
	id := len(b.offsets) + 1
	b.offsets = append(b.offsets, b.w.len())
	fmt.Fprintf(b.w.raw(), "%d 0 obj\n<< %s /Length %d >>\nstream\n", id, dict, len(payload))
	b.w.raw().Write(payload)
	b.w.raw().WriteString("\nendstream\nendobj\n")
	return id
}

func (b *comicPDFBuilder) finish() []byte {
	xrefHead := b.w.len()
	fmt.Fprintf(b.w.raw(), "xref\n0 %d\n", len(b.offsets)+1)
	b.w.writeString("0000000000 65535 f \n")
	for _, head := range b.offsets {
		fmt.Fprintf(b.w.raw(), "%010d 00000 n \n", head)
	}
	fmt.Fprintf(b.w.raw(), "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(b.offsets)+1, xrefHead)
	return b.w.bytes()
}

// comicTextStrokePt is the lettering outline's weight, the editor's 3-unit
// stroke at the 1000-unit page scale.
const comicTextStrokeUnits = 3.0

// renderComicPDF lays the whole book out: white pages at their own aspect,
// panels clipped and border-stroked, images cover/contain-placed under the
// clip, lettering as vector shapes and text. Refuses while any referenced
// file is missing on disk (the composer's render rule) — an export that
// silently dropped art would lie.
func renderComicPDF(ctx context.Context, config AppConfig, doc ComicDoc) ([]byte, error) {
	if missing := comicMissingRefs(doc); len(missing) > 0 {
		return nil, fmt.Errorf("the comic's export needs every image on disk — missing: %s", strings.Join(missing, ", "))
	}
	if err := validateComicDoc(&doc); err != nil {
		return nil, err
	}

	// One XObject per unique artifact — the same art in N panels embeds once.
	images := map[string]int{}
	imageList := []comicPDFImage{}
	loadRef := func(ref ComicMediaRef) (int, error) {
		if id, ok := images[ref.ArtifactID]; ok {
			return id, nil
		}
		loaded, err := loadComicPDFImage(ctx, config, ref)
		if err != nil {
			return 0, err
		}
		images[ref.ArtifactID] = 4 + len(imageList)
		imageList = append(imageList, loaded)
		return images[ref.ArtifactID], nil
	}
	// Preflight every ref so a refusal lands before any bytes are written.
	for _, page := range doc.Pages {
		for _, panel := range page.Panels {
			if panel.Image != nil {
				if _, err := loadRef(panel.Image.Ref); err != nil {
					return nil, err
				}
			}
		}
	}

	builder := newComicPDFBuilder()
	builder.raw("<< /Type /Catalog /Pages 2 0 R >>")
	// Objects interleave: content stream i takes 4+N+2i, its page dict
	// 5+N+2i (after the 3 header objects and N image XObjects).
	kids := []string{}
	for i := range doc.Pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 5+len(imageList)+2*i))
	}
	builder.raw("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(doc.Pages))
	builder.raw("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	for _, embedded := range imageList {
		dict := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /BitsPerComponent 8 /Filter %s /ColorSpace %s", embedded.width, embedded.height, embedded.filter, embedded.colorSpace)
		if embedded.filter == "/DCTDecode" {
			dict += fmt.Sprintf(" /ColorTransform %d", embedded.colorTransform)
		}
		builder.stream(dict, embedded.data)
	}

	contentBase := 4 + len(imageList)
	xrefResources := func() string {
		entries := []string{"/Font << /F1 3 0 R >>"}
		if len(imageList) > 0 {
			xobjects := []string{}
			for i := range imageList {
				xobjects = append(xobjects, fmt.Sprintf("/Im%d %d 0 R", i, 4+i))
			}
			entries = append(entries, fmt.Sprintf("/XObject << %s >>", strings.Join(xobjects, " ")))
		}
		// The enclosing dict is the whole point — /Resources is a DICTIONARY;
		// its entries bare would read /Font as the value and break every parser.
		return "<< " + strings.Join(entries, " ") + " >>"
	}

	// The page dicts name their content streams, so the streams write FIRST
	// (ids contentBase+i) and the dicts after — the ids the Pages tree's
	// kids already named.
	for pageIndex, page := range doc.Pages {
		ratio, _ := comicPageAspect(page)
		pageW, pageH := comicPDFPageSize(ratio)
		var stream bytes.Buffer
		fmt.Fprintf(&stream, "1 1 1 rg 0 0 %.2f %.2f re f 0 0 0 rg\n", pageW, pageH)

		// Panels: fill, clipped image, then the border stroke OUTSIDE the
		// clip (the editor's stroke centers on the polygon — clipping it
		// would shave half).
		for _, panel := range page.Panels {
			path := comicPolygonPath(panel.Points, pageW, pageH)
			fmt.Fprintf(&stream, "q %s h W n 0.957 0.957 0.941 rg 0 0 %.2f %.2f re f Q\n", path, pageW, pageH)
			if panel.Image != nil {
				placement := comicImagePlacementRect(panel.Points, panel.Image.Ref.Width, panel.Image.Ref.Height, panel.Image.Fit, panel.Image.FocusX, panel.Image.FocusY, panel.Image.Zoom, pageW, pageH)
				fmt.Fprintf(&stream, "q %s h W n q %.2f 0 0 %.2f %.2f %.2f cm /Im%d Do Q Q\n",
					path, placement.w, placement.h, placement.x, pageH-placement.y-placement.h, images[panel.Image.Ref.ArtifactID]-4)
			}
			strokeWidth := doc.Style.Border * pageW
			fmt.Fprintf(&stream, "%.3f w %s h S\n", strokeWidth, path)
		}

		// Lettering: tail (under), shape, wrapped centered text.
		textStroke := comicTextStrokeUnits * pageW / 1000
		fmt.Fprintf(&stream, "1 j 1 J\n")
		for _, text := range page.Texts {
			bx := text.X * pageW
			byTop := text.Y * pageH
			bw := text.Width * pageW
			bh := text.Height * pageH
			// PDF y-up: the box's TOP edge in pdf space is H - topY; the box
			// spans [H - topY - bh, H - topY].
			byPDF := pageH - byTop - bh
			fontSize := text.Size * pageW * 0.022
			lineHeight := fontSize * 1.15
			lines := comicWrapLines(text.Text, bw*0.82, fontSize)
			if text.Kind == comicTextKindBalloon && text.Tail != nil {
				tip, baseA, baseB := comicBalloonTailTriangle(text)
				fmt.Fprintf(&stream, "1 1 1 rg 0 0 0 RG %.3f w %s B\n", textStroke, comicTrianglePath(tip, baseA, baseB, pageW, pageH))
			}
			switch text.Kind {
			case comicTextKindBalloon:
				fmt.Fprintf(&stream, "0 0 0 RG 1 1 1 rg %.3f w %s B\n", textStroke, comicEllipsePath(bx+bw/2, byPDF+bh/2, bw/2, bh/2))
			case comicTextKindCaption:
				fmt.Fprintf(&stream, "0 0 0 RG 1 1 1 rg %.3f w %.2f %.2f %.2f %.2f re B\n", textStroke, bx, byPDF, bw, bh)
			case comicTextKindSFX:
				var path strings.Builder
				for i, point := range comicSFXPolygon(text) {
					if i == 0 {
						fmt.Fprintf(&path, "%.2f %.2f m ", point.X*pageW, pageH-point.Y*pageH)
					} else {
						fmt.Fprintf(&path, "%.2f %.2f l ", point.X*pageW, pageH-point.Y*pageH)
					}
				}
				fmt.Fprintf(&stream, "0 0 0 RG 1 1 1 rg %.3f w %sh B\n", textStroke, path.String())
			}
			cx := bx + bw/2
			cyPDF := byPDF + bh/2
			total := float64(len(lines)) * lineHeight
			for i, line := range lines {
				if line == "" {
					continue
				}
				baseline := cyPDF + total/2 - (float64(i)+0.5)*lineHeight + fontSize*0.35
				width := float64(len([]rune(line))) * fontSize * 0.6
				x := cx - width/2
				fmt.Fprintf(&stream, "BT /F1 %.2f Tf 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", fontSize, x, baseline, pdfEscape(line))
			}
		}

		contentID := builder.stream("", stream.Bytes())
		if contentID != contentBase+2*pageIndex {
			return nil, fmt.Errorf("the comic's PDF assembly drifted (content %d landed at id %d)", contentBase+2*pageIndex, contentID)
		}
		builder.raw("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources %s /Contents %d 0 R >>", pageW, pageH, xrefResources(), contentID)
	}

	return builder.finish(), nil
}

// comicPolygonPath renders the polygon path operators in PDF space (y-up).
func comicPolygonPath(points []ComicPoint, pageW, pageH float64) string {
	var b strings.Builder
	for i, point := range points {
		operator := "l"
		if i == 0 {
			operator = "m"
		}
		fmt.Fprintf(&b, "%.2f %.2f %s ", point.X*pageW, pageH-point.Y*pageH, operator)
	}
	return b.String()
}

// comicTrianglePath renders the closed tail triangle.
func comicTrianglePath(a, b, c ComicPoint, pageW, pageH float64) string {
	return fmt.Sprintf("%.2f %.2f m %.2f %.2f l %.2f %.2f l h ", a.X*pageW, pageH-a.Y*pageH, b.X*pageW, pageH-b.Y*pageH, c.X*pageW, pageH-c.Y*pageH)
}

// comicEllipsePath renders an axis-aligned ellipse as four Bézier arcs with
// the circle's kappa handle ratio.
func comicEllipsePath(cx, cy, rx, ry float64) string {
	const kappa = 0.5522847498
	kx := rx * kappa
	ky := ry * kappa
	return fmt.Sprintf(
		"%.2f %.2f m %.2f %.2f %.2f %.2f %.2f %.2f c %.2f %.2f %.2f %.2f %.2f %.2f c %.2f %.2f %.2f %.2f %.2f %.2f c %.2f %.2f %.2f %.2f %.2f %.2f c ",
		cx+rx, cy,
		cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry,
		cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy,
		cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry,
		cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy,
	)
}

// --- bindings ------------------------------------------------------------

// AdoptComicPagePNGRequest carries one frontend-rasterized page: the PNG
// bytes (base64 — the Wails IPC bridge carries strings), the exporting
// conversation, and the revision the export pinned (provenance for the turn).
type AdoptComicPagePNGRequest struct {
	ConversationID     string `json:"conversationId"`
	Name               string `json:"name,omitempty"`
	RevisionArtifactID string `json:"revisionArtifactId,omitempty"`
	PageLabel          string `json:"pageLabel"`
	PNGBase64          string `json:"pngBase64"`
}

// AdoptComicPagePNGResult names what the adoption produced: the artifact's
// identity (the reply chip and the assets panel read it) and its hydrated
// URL — the Edits panel's Download rides it.
type AdoptComicPagePNGResult struct {
	ArtifactID string `json:"artifactId"`
	URL        string `json:"url"`
}

// AdoptComicPagePNG writes the rasterized page into the conversation as a
// fresh img_ artifact plus an assistant turn linking the export to its
// revision — the composition-export adoption shape (this copy generated
// nothing, so no model attribution rides the artifact). Refused while the
// conversation streams, like every sanctioned write.
func (a *App) AdoptComicPagePNG(req AdoptComicPagePNGRequest) (AdoptComicPagePNGResult, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return AdoptComicPagePNGResult{}, errors.New("adopting a comic page requires a conversation id")
	}
	pngBytes, err := base64.StdEncoding.DecodeString(req.PNGBase64)
	if err != nil || len(pngBytes) == 0 {
		return AdoptComicPagePNGResult{}, errors.New("the page's PNG bytes could not be decoded")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return AdoptComicPagePNGResult{}, fmt.Errorf("the page's PNG could not be read: %w", err)
	}
	a.streamsMu.Lock()
	for _, active := range a.streamConversations {
		if active == conversationID {
			a.streamsMu.Unlock()
			return AdoptComicPagePNGResult{}, errors.New("the conversation is still streaming — wait for the turn to finish before exporting")
		}
	}
	a.streamsMu.Unlock()

	config, err := loadReadyConfig()
	if err != nil {
		return AdoptComicPagePNGResult{}, err
	}
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return AdoptComicPagePNGResult{}, err
	}
	contents, err := writeChatImageArtifacts(loaded.ArtifactsDir, ImageGenerateRequest{Width: cfg.Width, Height: cfg.Height}, []string{"data:image/png;base64," + req.PNGBase64})
	if err != nil || len(contents) == 0 {
		if err == nil {
			err = errors.New("the page could not be copied into the conversation")
		}
		return AdoptComicPagePNGResult{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = comicDefaultName
	}
	summary := fmt.Sprintf("Exported comic “%s” · page %s as an image artifact", name, strings.TrimSpace(req.PageLabel))
	nowText := time.Now().Format(time.RFC3339)
	turn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: conversationID,
		CreatedAt:      nowText,
		Kind:           "chat",
		Role:           "assistant",
		Content:        append([]HistoryContent{{Type: "text", Text: summary}}, contents...),
		ProviderResponse: map[string]any{
			"comicExport": map[string]any{
				"artifactId":         contents[0].ArtifactID,
				"revisionArtifactId": strings.TrimSpace(req.RevisionArtifactID),
				"kind":               "png",
				"backend":            "svg",
				"page":               strings.TrimSpace(req.PageLabel),
			},
		},
	}
	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(contents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return AdoptComicPagePNGResult{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, turn); err != nil {
		return AdoptComicPagePNGResult{}, err
	}
	absArtifactsDir, err := filepath.Abs(loaded.ArtifactsDir)
	if err != nil {
		absArtifactsDir = loaded.ArtifactsDir
	}
	return AdoptComicPagePNGResult{
		ArtifactID: contents[0].ArtifactID,
		URL:        artifactPrefix + filepath.Join(absArtifactsDir, filepath.Base(contents[0].Path)),
	}, nil
}

// SaveComicPDF copies an exported comic PDF to a user-chosen location — the
// Edits panel's Download for an export whose save dialog already filed it
// once (re-file it without re-rendering). The bytes must be a PDF.
func (a *App) SaveComicPDF(req SaveTranscriptRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("pdf path is empty")
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return "", errors.New("the file is not a PDF")
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-comic", ".pdf", "Save comic PDF", []runtime.FileFilter{
		{DisplayName: "PDF Files", Pattern: "*.pdf"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// ExportComicPDF renders a SAVED comic revision to PDF and opens the save
// dialog — the composer's pinned-revision rule: the editor's Save & export
// writes the revision first, so what the user approved is what renders.
func (a *App) ExportComicPDF(conversationID, artifactID string) (string, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return "", err
	}
	content, _, err := resolveComicContent(config.Storage, strings.TrimSpace(conversationID), strings.TrimSpace(artifactID))
	if err != nil {
		return "", err
	}
	path, err := contentArtifactPath(config.Storage, conversationID, *content)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the comic artifact failed: %w", err)
	}
	doc, err := parseComicDoc(data)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), comicImageTimeout)
	defer cancel()
	rendered, err := renderComicPDF(ctx, config, doc)
	if err != nil {
		return "", err
	}
	suggested := strings.TrimSpace(doc.Name)
	if suggested == "" {
		suggested = "comic"
	}
	return a.saveArtifactDialog(rendered, suggested+".pdf", "atelier-comic", ".pdf", "Export comic", []runtime.FileFilter{
		{DisplayName: "PDF Files", Pattern: "*.pdf"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}
