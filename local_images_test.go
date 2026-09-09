package main

// Tests for the local image tools (local_images.go): the pure arg builders,
// the aspect-crop math, sips probe parsing, tool executions against fake
// sips/magick shell scripts (the writeFakeWhisper pattern), the registry
// gates, and one full harness turn that persists a converted image.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeSipsScript fakes the sips CLI: it appends each invocation's args (one
// line per invocation, space-joined) to args.txt next to itself, answers -g
// with a fixed 400x300 PNG property sheet, and copies the input (the argument
// before --out) to the output (the argument after --out) so every transform
// op's staging pipeline stays exercised.
const fakeSipsScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$*" >> "$dir/args.txt"
for a in "$@"; do
  if [ "$a" = "-g" ] || [ "$a" = "--getProperty" ]; then
    echo "/fake/image.png"
    echo "  pixelWidth: 400"
    echo "  pixelHeight: 300"
    echo "  format: png"
    echo "  hasAlpha: no"
    echo "  space: RGB"
    echo "  bitsPerSample: 8"
    exit 0
  fi
done
in=""
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--out" ]; then out="$a"; fi
  if [ "$a" = "--out" ]; then in="$prev"; fi
  prev="$a"
done
if [ -n "$in" ] && [ -n "$out" ]; then cp "$in" "$out"; fi
`

// fakeMagickScript fakes the ImageMagick CLI: it records its args and copies
// the first existing file argument (the base image) to the last argument
// (every builder passes the output path last).
const fakeMagickScript = `#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s\n' "$*" >> "$dir/args.txt"
in=""
out=""
for a in "$@"; do
  if [ -z "$in" ] && [ -f "$a" ]; then in="$a"; fi
  out="$a"
done
if [ -n "$in" ] && [ -n "$out" ]; then cp "$in" "$out"; fi
`

func imageDataURL(payload string) string {
	// A minimal PNG signature (all isImageBytes checks) so the fixture
	// survives attachment persistence like a real image.
	frame := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte(payload)...)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(frame)
}

// sipsTestConfig wires a fake sips binary and returns the config plus the
// directory holding its args.txt.
func sipsTestConfig(t *testing.T) (AppConfig, string) {
	t.Helper()
	withRealLocalLookup(t)
	dir := t.TempDir()
	config := defaultAppConfig()
	config.Providers.Local.Sips.Binary = writeFakeWhisper(t, filepath.Join(dir, "bin"), "sips", fakeSipsScript)
	return config, filepath.Join(dir, "bin")
}

// magickTestConfig wires a fake ImageMagick binary (and a fake sips for the
// basic tools that share the registry).
func magickTestConfig(t *testing.T) (AppConfig, string) {
	t.Helper()
	withRealLocalLookup(t)
	dir := t.TempDir()
	config := defaultAppConfig()
	config.Providers.Local.Sips.Binary = writeFakeWhisper(t, filepath.Join(dir, "bin"), "sips", fakeSipsScript)
	config.Providers.Local.Magick.Binary = writeFakeWhisper(t, filepath.Join(dir, "bin"), "magick", fakeMagickScript)
	return config, filepath.Join(dir, "bin")
}

// fakeCLIArgs reads the args a fake CLI recorded, one line per invocation.
func fakeCLIArgs(t *testing.T, bin string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bin, "args.txt"))
	if err != nil {
		t.Fatalf("the fake CLI never ran: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// executeImageTool builds a registry + gateway over the config, sets the
// image attachment slots, and executes one call.
func executeImageTool(t *testing.T, config AppConfig, attachments HarnessToolExecutionContext, name string, call HarnessToolCall) HarnessToolResult {
	t.Helper()
	registry := defaultHarnessToolRegistry(context.Background(), config, nil)
	if _, ok := registry.Get(name); !ok {
		t.Fatalf("tool %q is not in the registry (gating or wiring bug)", name)
	}
	gateway := newToolGateway(nil, config, registry)
	gateway.tools.AttachedImages = attachments.AttachedImages
	return gateway.Execute(context.Background(), ToolExecutionRequest{Name: name, Call: call})
}

// TestSipsArgBuilders pins every sips builder's exact argument shape —
// especially the H-before-W order of -c and -z, the classic swap this file
// guards against.
func TestSipsArgBuilders(t *testing.T) {
	if got := sipsConvertArgs("in.png", "jpeg", "85", "out.jpg"); strings.Join(got, " ") != "-s format jpeg -s formatOptions 85 in.png --out out.jpg" {
		t.Errorf("convert args = %v", got)
	}
	if got := sipsConvertArgs("in.png", "png", "", "out.png"); strings.Join(got, " ") != "-s format png in.png --out out.png" {
		t.Errorf("convert args without quality = %v", got)
	}
	if got := sipsCropArgs("in.png", 300, 240, "out.png"); strings.Join(got, " ") != "-c 300 240 in.png --out out.png" {
		t.Errorf("crop args = %v (H must precede W)", got)
	}
	if got := sipsResizeExactArgs("in.png", 1350, 1080, "out.png"); strings.Join(got, " ") != "-z 1350 1080 in.png --out out.png" {
		t.Errorf("exact resize args = %v (H must precede W)", got)
	}
	if got := sipsResizeWidthArgs("in.png", 1080, "out.png"); strings.Join(got, " ") != "--resampleWidth 1080 in.png --out out.png" {
		t.Errorf("width resize args = %v", got)
	}
	if got := sipsResizeHeightArgs("in.png", 1350, "out.png"); strings.Join(got, " ") != "--resampleHeight 1350 in.png --out out.png" {
		t.Errorf("height resize args = %v", got)
	}
	if got := sipsRotateArgs("in.png", 90, "out.png"); strings.Join(got, " ") != "-r 90 in.png --out out.png" {
		t.Errorf("rotate args = %v", got)
	}
	if got := sipsFlipArgs("in.png", "horizontal", "out.png"); strings.Join(got, " ") != "-f horizontal in.png --out out.png" {
		t.Errorf("flip args = %v", got)
	}
}

// TestMagickArgBuilders pins the ImageMagick builders: the parenthesized
// watermark overlay group, the append-grid collage rows, and the adjustment
// chain.
func TestMagickArgBuilders(t *testing.T) {
	watermark := magickWatermarkArgs("base.png", "logo.png", "SouthEast", "+20+20", 25, 70, true, "out.png")
	want := "base.png ( logo.png -resize 25% -channel A -evaluate multiply 0.70 +channel ) -gravity SouthEast -geometry +20+20 -compose over -composite out.png"
	if strings.Join(watermark, " ") != want {
		t.Errorf("watermark args = %v\nwant %v", watermark, strings.Split(want, " "))
	}
	bare := magickWatermarkArgs("base.png", "logo.png", "Center", "+0+0", 0, 0, false, "out.png")
	if got := strings.Join(bare, " "); got != "base.png ( logo.png ) -gravity Center -geometry +0+0 -compose over -composite out.png" {
		t.Errorf("bare watermark args = %s", got)
	}
	collage := magickCollageArgs([]string{"a.png", "b.png", "c.png"}, 2, 512, "out.png")
	wantCollage := "( a.png b.png -resize 512x512 -background white -gravity center -extent 512x512 +append ) ( c.png -resize 512x512 -background white -gravity center -extent 512x512 +append ) -background white -append out.png"
	if strings.Join(collage, " ") != wantCollage {
		t.Errorf("collage args = %v", collage)
	}
	adjust := magickAdjustArgs("in.png", 10, -5, true, 130, true, false, false, "out.png")
	if got := strings.Join(adjust, " "); got != "in.png -brightness-contrast 10x-5 -modulate 100,130 out.png" {
		t.Errorf("adjust args = %s", got)
	}
	gray := magickAdjustArgs("in.png", 0, 0, false, 0, false, true, false, "out.png")
	if got := strings.Join(gray, " "); got != "in.png -colorspace Gray out.png" {
		t.Errorf("grayscale args = %s", got)
	}
	optimize := magickOptimizeArgs("in.jpg", 80, "out.jpg")
	if got := strings.Join(optimize, " "); got != "in.jpg -strip -quality 80 out.jpg" {
		t.Errorf("optimize args = %s", got)
	}
}

func TestAspectCropDimensions(t *testing.T) {
	cases := []struct {
		name                            string
		width, height, aspectW, aspectH int
		wantW, wantH                    int
	}{
		{"4:5 out of 400x300", 400, 300, 4, 5, 240, 300},
		{"1:1 out of 400x300", 400, 300, 1, 1, 300, 300},
		{"16:9 out of 400x300", 400, 300, 16, 9, 400, 225},
		{"9:16 out of 400x300", 400, 300, 9, 16, 169, 300},
		{"same aspect unchanged", 400, 300, 4, 3, 400, 300},
		{"clamps degenerate", 0, 0, 4, 5, 0, 0},
	}
	for _, tc := range cases {
		gotW, gotH := aspectCropDimensions(tc.width, tc.height, tc.aspectW, tc.aspectH)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("%s: aspectCropDimensions = %dx%d, want %dx%d", tc.name, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
	// A rounding case: 1000x333 cropped to 1:1 must stay inside the image.
	gotW, gotH := aspectCropDimensions(1000, 333, 1, 1)
	if gotW != 333 || gotH != 333 {
		t.Errorf("rounding: aspectCropDimensions = %dx%d, want 333x333", gotW, gotH)
	}
}

func TestParseAspectRatioAndScale(t *testing.T) {
	for token, ok := range map[string]bool{"16:9": true, "1:1": true, "4:5": true, "0:9": false, "9": false, "16:9:2": false, "": false, "-1:2": false, "x:y": false} {
		if _, _, got := parseAspectRatio(token); got != ok {
			t.Errorf("parseAspectRatio(%q) ok = %v, want %v", token, got, ok)
		}
	}
	if w, h, _ := parseAspectRatio(" 16:9 "); w != 16 || h != 9 {
		t.Errorf("parseAspectRatio trims = %dx%d, want 16x9", w, h)
	}
	for token, want := range map[string]int{"25": 25, "25%": 25, "0": 0, "": 0, "-5": 0, "big": 0} {
		if got := parseScalePercent(token); got != want {
			t.Errorf("parseScalePercent(%q) = %d, want %d", token, got, want)
		}
	}
}

func TestParseSipsProperties(t *testing.T) {
	output := []byte("/tmp/img.png\n  pixelWidth: 400\n  pixelHeight: 300\n  format: png\n  hasAlpha: yes\n  space: RGB\n  bitsPerSample: 16\n  formatOptions: default\n")
	properties := parseSipsProperties(output)
	if properties["pixelWidth"] != "400" || properties["format"] != "png" || properties["hasAlpha"] != "yes" {
		t.Fatalf("properties = %+v", properties)
	}
	if _, ok := properties["/tmp/img.png"]; ok {
		t.Error("the file-path header line must not become a property")
	}
}

// TestSipsFormatForMediaType pins the webp transcode: sips reads webp but
// cannot write it, so webp inputs ride the sips tools as PNG with a notice.
func TestSipsFormatForMediaType(t *testing.T) {
	if format, notice := sipsFormatForMediaType("image/jpeg"); format != "jpeg" || notice != "" {
		t.Errorf("jpeg = %q (%q), want jpeg with no notice", format, notice)
	}
	if format, notice := sipsFormatForMediaType("image/webp"); format != "png" || notice == "" {
		t.Errorf("webp = %q (%q), want png with a transcode notice", format, notice)
	}
	if format, notice := sipsFormatForMediaType("image/heic"); format != "heic" || notice != "" {
		t.Errorf("heic = %q (%q), want heic with no notice", format, notice)
	}
	if format, _ := sipsFormatForMediaType("image/x-exotic"); format != "png" {
		t.Errorf("unknown = %q, want png", format)
	}
}

func TestImageToolValidation(t *testing.T) {
	cases := []struct {
		name string
		call HarnessToolCall
		want string
	}{
		{"convert needs format", HarnessToolCall{Name: "convert_image"}, "format is required"},
		{"convert rejects webp", HarnessToolCall{Name: "convert_image", Format: "webp"}, "optimize_image"},
		{"convert quality range", HarnessToolCall{Name: "convert_image", Format: "jpeg", Quality: 150}, "quality must be 1-100"},
		{"convert quality format mismatch", HarnessToolCall{Name: "convert_image", Format: "png", Quality: 80}, "quality applies to"},
		{"transform needs an op", HarnessToolCall{Name: "transform_image"}, "at least one"},
		{"transform aspect shape", HarnessToolCall{Name: "transform_image", AspectRatio: "wide"}, "W:H ratio"},
		{"transform rotate value", HarnessToolCall{Name: "transform_image", Rotate: 45}, "rotate must be"},
		{"transform flip value", HarnessToolCall{Name: "transform_image", Flip: "diagonal"}, "flip must be"},
		{"compose mode value", HarnessToolCall{Name: "compose_images", Mode: "blend"}, "mode must be"},
		{"compose position value", HarnessToolCall{Name: "compose_images", Mode: "watermark", Position: "middle"}, "position must be"},
		{"compose opacity range", HarnessToolCall{Name: "compose_images", Opacity: 150}, "opacity must be 0-100"},
		{"compose scale range", HarnessToolCall{Name: "compose_images", Scale: "150"}, "at most 100"},
		{"adjust needs an op", HarnessToolCall{Name: "adjust_image"}, "at least one"},
		{"adjust brightness range", HarnessToolCall{Name: "adjust_image", Brightness: 150}, "brightness must be"},
		{"adjust saturation range", HarnessToolCall{Name: "adjust_image", Saturation: 250}, "saturation must be"},
		{"adjust tone exclusivity", HarnessToolCall{Name: "adjust_image", Grayscale: true, Sepia: true}, "cannot be combined"},
		{"optimize format value", HarnessToolCall{Name: "optimize_image", Format: "psd"}, "format must be"},
	}
	for _, tc := range cases {
		registry := newHarnessToolRegistry([]HarnessToolDefinition{
			convertImageToolDefinition(), transformImageToolDefinition(), composeImagesToolDefinition(),
			adjustImageToolDefinition(), optimizeImageToolDefinition(), probeImageToolDefinition(),
		})
		definition, ok := registry.Get(tc.call.Name)
		if !ok {
			t.Fatalf("%s: tool missing from test registry", tc.call.Name)
		}
		errors := definition.Validate(fmt.Sprintf("toolCalls[%d]", 0), tc.call)
		if len(errors) == 0 || !strings.Contains(errors[0], tc.want) {
			t.Errorf("%s: errors = %v, want one containing %q", tc.name, errors, tc.want)
		}
	}
	// A valid call produces no errors.
	registry := newHarnessToolRegistry([]HarnessToolDefinition{convertImageToolDefinition()})
	definition, _ := registry.Get("convert_image")
	if errors := definition.Validate("toolCalls[0]", HarnessToolCall{Name: "convert_image", Format: "jpeg", Quality: 85}); len(errors) != 0 {
		t.Errorf("valid convert errors = %v", errors)
	}
}

func TestSipsConvertExecutes(t *testing.T) {
	config, bin := sipsTestConfig(t)
	result := executeImageTool(t, config, HarnessToolExecutionContext{AttachedImages: []string{imageDataURL("PHOTO")}}, "convert_image", HarnessToolCall{Format: "jpeg", Quality: 85})
	if result.Status != "completed" {
		t.Fatalf("status = %s (%s)", result.Status, result.Error)
	}
	args := fakeCLIArgs(t, bin)
	if len(args) != 1 || !strings.Contains(args[0], "-s format jpeg -s formatOptions 85") {
		t.Fatalf("sips invocations = %v", args)
	}
	if !strings.HasPrefix(result.Result.(ToolImageResult).Images[0], "data:image/jpeg;base64,") {
		t.Errorf("result image is not a jpeg data URL")
	}
}

// TestSipsTransformExecutes pins the one-op-per-invocation contract: an
// aspect crop + resize + rotate runs four separate sips calls (chaining them
// in one invocation misbehaves — see the local_images.go header).
func TestSipsTransformExecutes(t *testing.T) {
	config, bin := sipsTestConfig(t)
	result := executeImageTool(t, config, HarnessToolExecutionContext{AttachedImages: []string{imageDataURL("PHOTO")}}, "transform_image", HarnessToolCall{AspectRatio: "4:5", Width: 1080, Height: 1350, Rotate: 90})
	if result.Status != "completed" {
		t.Fatalf("status = %s (%s)", result.Status, result.Error)
	}
	args := fakeCLIArgs(t, bin)
	if len(args) != 4 {
		t.Fatalf("sips invocations = %d (%v), want 4 (probe, crop, resize, rotate)", len(args), args)
	}
	if !strings.Contains(args[0], "-g all") {
		t.Errorf("probe invocation = %q, want -g all for the crop dimensions", args[0])
	}
	if !strings.Contains(args[1], "-c 300 240") {
		t.Errorf("crop invocation = %q, want the 4:5 rect of the probed 400x300", args[1])
	}
	if !strings.Contains(args[2], "-z 1350 1080") {
		t.Errorf("resize invocation = %q, want -z H W", args[2])
	}
	if !strings.Contains(args[3], "-r 90") {
		t.Errorf("rotate invocation = %q", args[3])
	}
	if !strings.Contains(result.Summary, "cropped") {
		t.Errorf("summary = %q, want the ops named", result.Summary)
	}
}

func TestProbeImageExecutes(t *testing.T) {
	config, bin := sipsTestConfig(t)
	result := executeImageTool(t, config, HarnessToolExecutionContext{AttachedImages: []string{imageDataURL("PHOTO")}}, "probe_image", HarnessToolCall{})
	if result.Status != "completed" {
		t.Fatalf("status = %s (%s)", result.Status, result.Error)
	}
	args := fakeCLIArgs(t, bin)
	if len(args) != 1 || !strings.Contains(args[0], "-g all") {
		t.Fatalf("sips invocations = %v, want one -g all probe", args)
	}
	probe, ok := result.Result.(ToolImageProbeResult)
	if !ok || probe.Width != 400 || probe.Height != 300 || probe.Format != "png" {
		t.Fatalf("probe output = %+v", result.Result)
	}
}

func TestMagickWatermarkExecutes(t *testing.T) {
	config, bin := magickTestConfig(t)
	result := executeImageTool(t, config, HarnessToolExecutionContext{AttachedImages: []string{imageDataURL("BASE"), imageDataURL("LOGO")}}, "compose_images", HarnessToolCall{Mode: "watermark", Position: "bottom-right", Scale: "25", Opacity: 70})
	if result.Status != "completed" {
		t.Fatalf("status = %s (%s)", result.Status, result.Error)
	}
	args := fakeCLIArgs(t, bin)
	if len(args) != 1 || !strings.Contains(args[0], "-gravity SouthEast") || !strings.Contains(args[0], "-resize 25%") {
		t.Fatalf("magick invocation = %v", args)
	}
	if !strings.HasPrefix(result.Result.(ToolImageResult).Images[0], "data:image/png;base64,") {
		t.Errorf("watermark output is not a png data URL")
	}
}

func TestMagickCollageExecutes(t *testing.T) {
	config, bin := magickTestConfig(t)
	images := []string{imageDataURL("A"), imageDataURL("B"), imageDataURL("C")}
	result := executeImageTool(t, config, HarnessToolExecutionContext{AttachedImages: images}, "compose_images", HarnessToolCall{Mode: "collage"})
	if result.Status != "completed" {
		t.Fatalf("status = %s (%s)", result.Status, result.Error)
	}
	args := fakeCLIArgs(t, bin)
	if len(args) != 1 || !strings.Contains(args[0], "+append") || !strings.Contains(args[0], "-append") {
		t.Fatalf("magick invocation = %v", args)
	}
	// 3 images → 2 columns → row one holds two images, row two holds one.
	if strings.Count(args[0], "( ") != 2 {
		t.Errorf("collage invocation = %q, want two row groups", args[0])
	}
}

func TestMagickAdjustAndOptimizeExecutes(t *testing.T) {
	config, bin := magickTestConfig(t)
	result := executeImageTool(t, config, HarnessToolExecutionContext{AttachedImages: []string{imageDataURL("PHOTO")}}, "adjust_image", HarnessToolCall{Saturation: 130, Grayscale: false})
	if result.Status != "completed" {
		t.Fatalf("adjust status = %s (%s)", result.Status, result.Error)
	}
	if args := fakeCLIArgs(t, bin); !strings.Contains(args[len(args)-1], "-modulate 100,130") {
		t.Fatalf("adjust invocation = %v", args)
	}
	result = executeImageTool(t, config, HarnessToolExecutionContext{AttachedImages: []string{imageDataURL("PHOTO")}}, "optimize_image", HarnessToolCall{Format: "webp", Quality: 80})
	if result.Status != "completed" {
		t.Fatalf("optimize status = %s (%s)", result.Status, result.Error)
	}
	if args := fakeCLIArgs(t, bin); !strings.Contains(args[len(args)-1], "-strip -quality 80") {
		t.Fatalf("optimize invocation = %v", args)
	}
	if !strings.HasPrefix(result.Result.(ToolImageResult).Images[0], "data:image/webp;base64,") {
		t.Errorf("optimize output is not a webp data URL")
	}
}

func TestImageToolsRequireAttachment(t *testing.T) {
	config, _ := sipsTestConfig(t)
	for _, name := range []string{"convert_image", "transform_image", "probe_image"} {
		result := executeImageTool(t, config, HarnessToolExecutionContext{}, name, HarnessToolCall{Format: "jpeg"})
		if result.Status == "ok" {
			t.Errorf("%s executed with no attached image", name)
		}
	}
}

// TestImageToolsRegistryGating pins the two gates: sips tools appear only
// with a sips binary, ImageMagick tools only with a magick/convert binary.
func TestImageToolsRegistryGating(t *testing.T) {
	// TestMain's pin keeps every local binary undetected on the default path.
	bare := defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
	for _, name := range []string{"convert_image", "transform_image", "probe_image", "compose_images", "adjust_image", "optimize_image"} {
		if _, ok := bare.Get(name); ok {
			t.Errorf("%s offered with no local binaries configured", name)
		}
	}
	sipsConfig, _ := sipsTestConfig(t)
	// Stub the lookup so the host machine's real ImageMagick (present on dev
	// machines) cannot leak into the sips-only assertion.
	stubLocalLookup(t, map[string]string{sipsConfig.Providers.Local.Sips.Binary: sipsConfig.Providers.Local.Sips.Binary})
	sipsOnly := defaultHarnessToolRegistry(context.Background(), sipsConfig, nil)
	for _, name := range []string{"convert_image", "transform_image", "probe_image"} {
		if _, ok := sipsOnly.Get(name); !ok {
			t.Errorf("%s missing with sips configured", name)
		}
	}
	for _, name := range []string{"compose_images", "adjust_image", "optimize_image"} {
		if _, ok := sipsOnly.Get(name); ok {
			t.Errorf("%s offered with no ImageMagick configured", name)
		}
	}
	magickConfig, _ := magickTestConfig(t)
	stubLocalLookup(t, map[string]string{
		magickConfig.Providers.Local.Sips.Binary:   magickConfig.Providers.Local.Sips.Binary,
		magickConfig.Providers.Local.Magick.Binary: magickConfig.Providers.Local.Magick.Binary,
	})
	both := defaultHarnessToolRegistry(context.Background(), magickConfig, nil)
	for _, name := range []string{"compose_images", "adjust_image", "optimize_image"} {
		if _, ok := both.Get(name); !ok {
			t.Errorf("%s missing with ImageMagick configured", name)
		}
	}
}

func TestHarnessToolPlanSchemaHasImageParams(t *testing.T) {
	schema := harnessToolPlanSchema(filesystemToolRegistry())
	items := schema["properties"].(map[string]any)["toolCalls"].(map[string]any)["items"].(map[string]any)
	properties := items["properties"].(map[string]any)
	for _, param := range []string{"format", "quality", "aspectRatio", "scale", "width", "height", "rotate", "flip", "position", "opacity", "brightness", "contrast", "saturation", "grayscale", "sepia"} {
		if _, ok := properties[param].(map[string]any); !ok {
			t.Errorf("plan schema properties missing %q", param)
		}
	}
}

func TestApplyKwargsImageParams(t *testing.T) {
	var call HarnessToolCall
	applyKwargs(&call, "format='jpeg', quality=85, width=1080, height=1350, rotate=90, flip='horizontal', position='bottom-right', opacity=70, brightness=10, contrast=-5, saturation=130, grayscale=true, sepia=false, scale='25'")
	if call.Format != "jpeg" || call.Quality != 85 || call.Width != 1080 || call.Rotate != 90 || call.Flip != "horizontal" ||
		call.Position != "bottom-right" || call.Opacity != 70 || call.Brightness != 10 || call.Contrast != -5 ||
		call.Saturation != 130 || !call.Grayscale || call.Sepia || call.Scale != "25" {
		t.Fatalf("applyKwargs = %+v", call)
	}
	// Unparseable ints degrade to 0 so per-tool validation produces the fix.
	var bad HarnessToolCall
	applyKwargs(&bad, "quality='lots'")
	if bad.Quality != 0 {
		t.Fatalf("unparseable quality = %d, want 0", bad.Quality)
	}
}

func TestDecodeTriageImageEdit(t *testing.T) {
	decision, err := decodeTriageDecision(`{"needsTools":false,"responseMode":"text","toolTask":"","reason":"edit","mediaEdit":false,"imageEdit":true}`)
	if err != nil || !decision.ImageEdit {
		t.Fatalf("decision = %+v (err %v), want imageEdit true", decision, err)
	}
	decision, err = decodeTriageDecision(`{"needsTools":false,"responseMode":"text","toolTask":"","reason":"no edit","mediaEdit":false}`)
	if err != nil || decision.ImageEdit {
		t.Fatalf("decision = %+v (err %v), want imageEdit false when absent", decision, err)
	}
	decision, err = decodeTriageDecision(`{"needsTools":false,"responseMode":"text","toolTask":"","reason":"edit","mediaEdit":false,"imageEdit":"true"}`)
	if err != nil || decision.ImageEdit {
		t.Fatalf("decision = %+v (err %v), a mis-typed imageEdit must stay false", decision, err)
	}
}

func TestImageEditUnavailableNote(t *testing.T) {
	if got := imageEditUnavailableNote(true, true); got != "" {
		t.Fatalf("note with both backends = %q, want none", got)
	}
	if got := imageEditUnavailableNote(false, false); !strings.Contains(got, "brew install imagemagick") {
		t.Fatalf("both-missing note = %q, want the install remedy", got)
	}
	got := imageEditUnavailableNote(true, false)
	if !strings.Contains(got, "brew install imagemagick") || !strings.Contains(got, "sips") {
		t.Fatalf("sips-only note = %q, want both the available basic edits and the imagemagick remedy", got)
	}
	if got := imageEditUnavailableNote(false, true); !strings.Contains(got, "sips") {
		t.Fatalf("magick-only note = %q, want the sips gap named", got)
	}
}

func TestImageEditFallbackNotice(t *testing.T) {
	if got := imageEditFallbackNotice(false, "Nope."); got != "" {
		t.Fatalf("notice with flag off = %q, want none", got)
	}
	if got := imageEditFallbackNotice(true, "Install ImageMagick with brew first."); got != "" {
		t.Fatalf("notice when the answer covers it = %q, want none", got)
	}
	got := imageEditFallbackNotice(true, "I cannot do that right now.")
	if !strings.Contains(got, "brew install imagemagick") {
		t.Fatalf("notice = %q, want the install remedy", got)
	}
}

// imageEditTurnConfig mirrors mediaEditTurnConfig for an image-edit turn: the
// user attaches an image and asks for a watermark, triage flags imageEdit,
// and the final model answers finalText. Returns the captured final request
// bodies.
func imageEditTurnConfig(t *testing.T, config AppConfig, triageJSON, finalText string) []string {
	t.Helper()
	var finalBodies []string
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				switch payload["model"] {
				case "harness-model":
					harnessCalls++
					if harnessCalls == 1 {
						return chatCompletion("harness-model", triageJSON), nil
					}
					t.Fatalf("unexpected harness call #%d (needsTools false must skip planning)", harnessCalls)
					return nil, nil
				default:
					return chatCompletion("chat-box-model", `"Edited"`), nil
				}
			}
			encoded, _ := json.Marshal(payload)
			finalBodies = append(finalBodies, string(encoded))
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":`+strconv.Quote(finalText)+`},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}},
			}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})
	app.runChatStream(context.Background(), "request-image-edit", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Put my logo in the bottom right corner of this image.",
			Images:  []string{imageDataURL("PHOTO")},
		}},
	})
	return finalBodies
}

// TestHarnessImageEditWithoutMagickNotifies pins the first-run image-edit
// experience when sips is present but ImageMagick is not: the final model
// receives the scoped note and the reply carries the deterministic fallback.
func TestHarnessImageEditWithoutMagickNotifies(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	home := t.TempDir()
	t.Setenv("HOME", home)
	// A fake sips override plus a stubbed lookup: sips configured, and the
	// host machine's real ImageMagick cannot leak into the detection.
	config, _ := sipsTestConfig(t)
	stubLocalLookup(t, map[string]string{config.Providers.Local.Sips.Binary: config.Providers.Local.Sips.Binary})
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}
	const triageJSON = `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"watermark needs imagemagick","mediaEdit":false,"imageEdit":true}`

	finalBodies := imageEditTurnConfig(t, config, triageJSON, "I can't add watermarks on this machine yet.")
	if len(finalBodies) == 0 {
		t.Fatal("the final model was never called")
	}
	joined := strings.Join(finalBodies, "\n")
	if !strings.Contains(joined, "brew install imagemagick") {
		t.Fatal("the install note never reached the final model's messages")
	}
	if reply := savedAssistantContent(t, config); !strings.Contains(reply, "brew install imagemagick") {
		t.Fatalf("reply = %q, want the deterministic install fallback", reply)
	}
}

// TestHarnessImageToolsAvailableNoNote pins the both-configured path: with
// sips and ImageMagick both detected, an imageEdit-flagged turn gets no
// install note and no fallback.
func TestHarnessImageToolsAvailableNoNote(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	home := t.TempDir()
	t.Setenv("HOME", home)
	config, _ := magickTestConfig(t)
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}
	const triageJSON = `{"needsTools":false,"responseMode":"text","toolTask":"","reason":"asked for basic edits","mediaEdit":false,"imageEdit":true}`

	finalBodies := imageEditTurnConfig(t, config, triageJSON, "Sure — attach the image and I'll resize it.")
	joined := strings.Join(finalBodies, "\n")
	if strings.Contains(joined, "Atelier note:") {
		t.Fatal("the note must not appear when both image backends are configured")
	}
	if reply := savedAssistantContent(t, config); strings.Contains(reply, "⚠️") {
		t.Fatalf("reply = %q, no fallback is warranted with both backends configured", reply)
	}
}

// TestHarnessImageTurnPersistsMedia drives a full planned turn whose plan
// calls convert_image on the attached image and pins that the converted
// artifact persists on the saved turn.
func TestHarnessImageTurnPersistsMedia(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withRealLocalLookup(t)

	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config, _ := sipsTestConfig(t)
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	var finalBodies []string
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				switch payload["model"] {
				case "harness-model":
					harnessCalls++
					switch harnessCalls {
					case 1: // triage
						return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"text","toolTask":"Convert the HEIC photo to JPEG.","reason":"The user asked for a format conversion.","mediaEdit":false,"imageEdit":true}`), nil
					case 2: // plan
						return chatCompletion("harness-model", `{"brief":"Convert the attached image to jpeg at quality 85.","needsTools":true,"reason":"format conversion","toolCalls":[{"name":"convert_image","format":"jpeg","quality":85}]}`), nil
					case 3:
						return chatCompletion("harness-model", `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`), nil
					}
					t.Fatalf("unexpected harness call #%d", harnessCalls)
					return nil, nil
				default:
					return chatCompletion("chat-box-model", `"Converted"`), nil
				}
			}
			encoded, _ := json.Marshal(payload)
			finalBodies = append(finalBodies, string(encoded))
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the JPEG."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}},
			}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-image-turn", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Convert this photo to a JPEG.",
			Images:  []string{imageDataURL("HEIC-PHOTO")},
		}},
	})

	if len(finalBodies) == 0 {
		t.Fatal("the final model was never called")
	}

	conversations, err := listConversations(config.Storage)
	if err != nil || len(conversations) != 1 {
		t.Fatalf("listConversations = %v (%d conversations)", err, len(conversations))
	}
	loaded, err := newHistoryStore(config.Storage).loadForAppend(conversations[0].ID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		t.Fatalf("loadForAppend: %v", err)
	}
	turnData, err := os.ReadFile(filepath.Join(loaded.TurnsDir, "turn_000002.json"))
	if err != nil {
		t.Fatalf("ReadFile assistant turn: %v", err)
	}
	var savedTurn HistoryTurn
	if err := json.Unmarshal(turnData, &savedTurn); err != nil {
		t.Fatalf("Unmarshal turn: %v", err)
	}
	var imagePath string
	for _, content := range savedTurn.Content {
		if content.Type == "image" {
			imagePath = content.Path
		}
	}
	if !strings.Contains(imagePath, "img_") {
		t.Fatalf("saved turn content = %+v, want a persisted image artifact", savedTurn.Content)
	}
	if _, err := os.Stat(filepath.Join(loaded.ArtifactsDir, filepath.Base(imagePath))); err != nil {
		t.Fatalf("image artifact unreadable: %v", err)
	}
}

// TestHarnessHEIFAttachmentConverts pins the user-facing HEIF flow end to end:
// an iPhone HEIC photo attached with "convert this to jpg" — sent as bare
// base64, the frontend's real payload shape — must persist the conversation
// (it used to die at StartChatTurn with "payload is not a supported image",
// so the chat never reached the sidebar), stage the .heic bytes for sips, and
// attach the converted artifact to the reply.
func TestHarnessHEIFAttachmentConverts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withRealLocalLookup(t)

	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config, bin := sipsTestConfig(t)
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	// The frontend strips the data: URL header before sending (Ollama's wire
	// shape), so the current turn's images arrive as bare base64.
	bareHEIF := base64.StdEncoding.EncodeToString(heifTestImage("heic"))

	var finalBodies []string
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				switch payload["model"] {
				case "harness-model":
					harnessCalls++
					switch harnessCalls {
					case 1: // triage
						return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"text","toolTask":"Convert the HEIF photo to JPEG.","reason":"The user asked for a format conversion.","mediaEdit":false,"imageEdit":true}`), nil
					case 2: // plan
						return chatCompletion("harness-model", `{"brief":"Convert the attached image to jpeg.","needsTools":true,"reason":"format conversion","toolCalls":[{"name":"convert_image","format":"jpeg"}]}`), nil
					case 3:
						return chatCompletion("harness-model", `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`), nil
					}
					t.Fatalf("unexpected harness call #%d", harnessCalls)
					return nil, nil
				default:
					return chatCompletion("chat-box-model", `"Converted"`), nil
				}
			}
			encoded, _ := json.Marshal(payload)
			finalBodies = append(finalBodies, string(encoded))
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the JPEG."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}},
			}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-heif-turn", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Convert this to jpg.",
			Images:  []string{bareHEIF},
		}},
	})

	if len(finalBodies) == 0 {
		t.Fatal("the final model was never called")
	}
	// The turn must survive to history — the reported bug lost the whole
	// conversation at attachment validation, so it never hit the sidebar.
	conversations, err := listConversations(config.Storage)
	if err != nil || len(conversations) != 1 {
		t.Fatalf("listConversations = %v (%d conversations), want exactly 1", err, len(conversations))
	}
	loaded, err := newHistoryStore(config.Storage).loadForAppend(conversations[0].ID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		t.Fatalf("loadForAppend: %v", err)
	}
	// The staged input carries the .heic extension so sips trusts the format.
	invocations := fakeCLIArgs(t, bin)
	if len(invocations) == 0 {
		t.Fatal("the fake sips never ran — convert_image never executed")
	}
	stagedHEIC := false
	for _, args := range invocations {
		if strings.Contains(args, "input.heic") {
			stagedHEIC = true
		}
	}
	if !stagedHEIC {
		t.Fatalf("sips args = %v, want the staged input named input.heic", invocations)
	}
	// The user turn persists the HEIF bytes as a .heic artifact; the assistant
	// turn carries the converted image.
	userData, err := os.ReadFile(filepath.Join(loaded.TurnsDir, "turn_000001.json"))
	if err != nil {
		t.Fatalf("ReadFile user turn: %v", err)
	}
	var userTurn HistoryTurn
	if err := json.Unmarshal(userData, &userTurn); err != nil {
		t.Fatalf("Unmarshal user turn: %v", err)
	}
	heicArtifact := false
	for _, content := range userTurn.Content {
		if content.Type == "image" && strings.HasSuffix(content.Path, ".heic") && content.MimeType == "image/heic" {
			heicArtifact = true
		}
	}
	if !heicArtifact {
		t.Fatalf("user turn content = %+v, want a persisted .heic image artifact", userTurn.Content)
	}
	if reply := savedAssistantContent(t, config); !strings.Contains(reply, "Here is the JPEG.") {
		t.Fatalf("assistant reply = %q, want the completed conversion reply", reply)
	}
}
