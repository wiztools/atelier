package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"atelier/internal/mediasniff/mediasnifftest"
)

func TestUsdCostMicros(t *testing.T) {
	positive := 0.000243
	negative := -1.5
	tests := []struct {
		name string
		cost *float64
		want int64
	}{
		{name: "nil is unreported", cost: nil, want: 0},
		{name: "zero is free", cost: func() *float64 { v := 0.0; return &v }(), want: 0},
		{name: "negative ignored", cost: &negative, want: 0},
		{name: "nan ignored", cost: func() *float64 { v := math.NaN(); return &v }(), want: 0},
		{name: "typical turn", cost: &positive, want: 243},
		{name: "one dollar", cost: func() *float64 { v := 1.0; return &v }(), want: 1_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usdCostMicros(tt.cost); got != tt.want {
				t.Fatalf("usdCostMicros(%v) = %d, want %d", tt.cost, got, tt.want)
			}
		})
	}
}

func nanValue() float64 { return math.NaN() }

func TestFalBillingHintsQuantityForUnit(t *testing.T) {
	hints := falBillingHints{Images: 4, Seconds: 7.5, Characters: 120, Requests: 1, Tokens: 216000, Megapixels: 124.895232}
	tests := []struct {
		unit    string
		wantQty float64
		wantOK  bool
	}{
		{unit: "image", wantQty: 4, wantOK: true},
		{unit: "Image", wantQty: 4, wantOK: true},
		// The nano-banana-pro family's plural spelling of the same unit —
		// conv_a465108a5f7ec78267191163 priced a nano-banana-pro/edit call "?"
		// before the plural was taught.
		{unit: "images", wantQty: 4, wantOK: true},
		{unit: "second", wantQty: 7.5, wantOK: true},
		{unit: "seconds", wantQty: 7.5, wantOK: true},
		{unit: "character", wantQty: 120, wantOK: true},
		// Character blocks fal's pricing API returns for elevenlabs v3 and
		// f5-tts: the unit names how many characters one billed block covers.
		{unit: "1000 characters", wantQty: 0.12, wantOK: true},
		{unit: "1,000 characters", wantQty: 0.12, wantOK: true},
		{unit: "video", wantQty: 1, wantOK: true},
		{unit: "request", wantQty: 1, wantOK: true},
		// fal's flat per-audio unit (stable-audio inpaint returns "audios").
		{unit: "audios", wantQty: 1, wantOK: true},
		// fal's per-designed-voice unit (minimax/voice-design): one voice
		// per call, so it prices like "request" — the preview's length
		// prices nothing.
		{unit: "cloned voices", wantQty: 1, wantOK: true},
		{unit: "cloned voice", wantQty: 1, wantOK: true},
		// fal's generic per-output unit — seedream v5 returns "units" per
		// generated image (conv_26ef0c4f), so an image call's count is its
		// images while every other call bills one per request.
		{unit: "units", wantQty: 4, wantOK: true},
		// Token vocabulary fal's pricing API returns for seedance-2.x: the
		// unit names how many tokens one billed block covers.
		{unit: "1000 tokens", wantQty: 216, wantOK: true},
		{unit: "1,000 tokens", wantQty: 216, wantOK: true},
		{unit: "tokens", wantQty: 216000, wantOK: true},
		{unit: "Token", wantQty: 216000, wantOK: true},
		// Megapixel vocabulary fal's pricing API returns for the ltx-2.3-22b
		// video family and MP-priced image models.
		{unit: "megapixel", wantQty: 124.895232, wantOK: true},
		{unit: "Megapixels", wantQty: 124.895232, wantOK: true},
		{unit: "1 megapixel", wantQty: 124.895232, wantOK: true},
		// fal's per-minute unit (sync-lipsync v2/v1, billed PRO RATA): sixty
		// seconds of the seconds hint to the unit.
		{unit: "minutes", wantQty: 0.125, wantOK: true},
		{unit: "minute", wantQty: 0.125, wantOK: true},
		{unit: "gpu_second", wantQty: 0, wantOK: false},
		{unit: "", wantQty: 0, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.unit, func(t *testing.T) {
			qty, ok := hints.quantityForUnit(tt.unit)
			if ok != tt.wantOK || qty != tt.wantQty {
				t.Fatalf("quantityForUnit(%q) = (%v, %v), want (%v, %v)", tt.unit, qty, ok, tt.wantQty, tt.wantOK)
			}
		})
	}
	// Missing hints refuse rather than guess: an unprobed duration must not
	// price a per-second model at zero seconds.
	if _, ok := (falBillingHints{Requests: 1}).quantityForUnit("second"); ok {
		t.Fatal("quantityForUnit(second) with no seconds hint should report not-ok")
	}
	// A token-billed model with no computable token count (video inputs the
	// gateway never probed) must not price at zero tokens either.
	if _, ok := (falBillingHints{Requests: 1}).quantityForUnit("1000 tokens"); ok {
		t.Fatal("quantityForUnit(1000 tokens) with no tokens hint should report not-ok")
	}
	// Nor may an MP-billed model price at zero megapixels when no pixel budget
	// was computable.
	if _, ok := (falBillingHints{Requests: 1}).quantityForUnit("megapixel"); ok {
		t.Fatal("quantityForUnit(megapixel) with no megapixels hint should report not-ok")
	}
	// "units" falls back to one per request when no image count applies (a
	// video/lipsync call billed per clip).
	if qty, ok := (falBillingHints{Requests: 1}).quantityForUnit("units"); !ok || qty != 1 {
		t.Fatalf("quantityForUnit(units) with only requests = (%v, %v), want (1, true)", qty, ok)
	}
	// A character-billed model with no text must not price at zero characters.
	if _, ok := (falBillingHints{Requests: 1}).quantityForUnit("1000 characters"); ok {
		t.Fatal("quantityForUnit(1000 characters) with no characters hint should report not-ok")
	}
	// Nor may a per-voice design price at zero voices when no request count
	// rides the hints.
	if _, ok := (falBillingHints{Characters: 91}).quantityForUnit("cloned voices"); ok {
		t.Fatal("quantityForUnit(cloned voices) with no requests hint should report not-ok")
	}
	// fal's compute-time vocabulary stays unpriced: the request can never
	// state GPU seconds. Per-minute endpoints (sync-lipsync) price from the
	// seconds hint now that the lipsync site measures the rendered clip, but
	// a hints value without seconds must still refuse rather than price a
	// zero-minute call.
	for _, unit := range []string{"compute seconds"} {
		if _, ok := (falBillingHints{Requests: 1, Seconds: 30}).quantityForUnit(unit); ok {
			t.Fatalf("quantityForUnit(%q) should report not-ok", unit)
		}
	}
	if _, ok := (falBillingHints{Requests: 1}).quantityForUnit("minutes"); ok {
		t.Fatal("quantityForUnit(minutes) with no seconds hint should report not-ok")
	}
	// The same quote on an endpoint that bills whole minutes (WholeMinutes,
	// the call site's word — see falBillsWholeMinutes) rounds the length up:
	// 7.5s is one full minute, not an eighth of one.
	whole := hints
	whole.WholeMinutes = true
	if qty, ok := whole.quantityForUnit("minutes"); !ok || qty != 1 {
		t.Fatalf("quantityForUnit(minutes) with WholeMinutes = (%v, %v), want (1, true)", qty, ok)
	}
	// The flag is the per-minute unit's alone: it never turns a per-second
	// quote into whole anything.
	if qty, ok := whole.quantityForUnit("seconds"); !ok || qty != 7.5 {
		t.Fatalf("quantityForUnit(seconds) with WholeMinutes = (%v, %v), want (7.5, true)", qty, ok)
	}
}

func TestFalTokenUnitMultiplier(t *testing.T) {
	for _, tt := range []struct {
		unit   string
		want   float64
		wantOK bool
	}{
		{unit: "1000 tokens", want: 1000, wantOK: true},
		{unit: " 1,000 TOKENS ", want: 1000, wantOK: true},
		{unit: "1000tokens", want: 1000, wantOK: true},
		{unit: "tokens", want: 1, wantOK: true},
		{unit: "token", want: 1, wantOK: true},
		{unit: "10 tokens", want: 10, wantOK: true},
		{unit: "1k tokens", want: 0, wantOK: false},
		{unit: "image", want: 0, wantOK: false},
		{unit: "", want: 0, wantOK: false},
	} {
		got, ok := falTokenUnitMultiplier(tt.unit)
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("falTokenUnitMultiplier(%q) = (%v, %v), want (%v, %v)", tt.unit, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestFalCharacterUnitMultiplier(t *testing.T) {
	for _, tt := range []struct {
		unit   string
		want   float64
		wantOK bool
	}{
		{unit: "1000 characters", want: 1000, wantOK: true},
		{unit: " 1,000 CHARACTERS ", want: 1000, wantOK: true},
		{unit: "1000characters", want: 1000, wantOK: true},
		{unit: "characters", want: 1, wantOK: true},
		{unit: "character", want: 1, wantOK: true},
		{unit: "1k characters", want: 0, wantOK: false},
		{unit: "1000 tokens", want: 0, wantOK: false},
		{unit: "", want: 0, wantOK: false},
	} {
		got, ok := falCharacterUnitMultiplier(tt.unit)
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("falCharacterUnitMultiplier(%q) = (%v, %v), want (%v, %v)", tt.unit, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestFalMegapixelUnitMultiplier(t *testing.T) {
	for _, tt := range []struct {
		unit   string
		want   float64
		wantOK bool
	}{
		{unit: "megapixel", want: 1, wantOK: true},
		{unit: "Megapixels", want: 1, wantOK: true},
		{unit: "1 megapixel", want: 1, wantOK: true},
		{unit: "1000 megapixels", want: 1000, wantOK: true},
		{unit: "1,000 megapixels", want: 1000, wantOK: true},
		{unit: "1000megapixels", want: 1000, wantOK: true},
		// The token vocabulary must not leak into the megapixel parser.
		{unit: "1000 tokens", want: 0, wantOK: false},
		{unit: "mp", want: 0, wantOK: false},
		{unit: "image", want: 0, wantOK: false},
		{unit: "", want: 0, wantOK: false},
	} {
		got, ok := falMegapixelUnitMultiplier(tt.unit)
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("falMegapixelUnitMultiplier(%q) = (%v, %v), want (%v, %v)", tt.unit, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestFalVideoPixelProduct(t *testing.T) {
	for _, tt := range []struct {
		name       string
		resolution string
		aspect     string
		want       float64
		wantOK     bool
	}{
		// 720p landscape: 1280×720 — fal's own $0.3024/s seedance rate is
		// 21,600 tokens/s against this product.
		{name: "720p 16:9", resolution: "720p", aspect: "16:9", want: 921600, wantOK: true},
		{name: "720p portrait same product", resolution: "720p", aspect: "9:16", want: 921600, wantOK: true},
		{name: "720p auto assumes landscape", resolution: "720p", aspect: "auto", want: 921600, wantOK: true},
		{name: "720p blank aspect", resolution: "720p", aspect: "", want: 921600, wantOK: true},
		{name: "1080p 16:9", resolution: "1080p", aspect: "16:9", want: 2073600, wantOK: true},
		{name: "4k tier", resolution: "4k", aspect: "16:9", want: 8294400, wantOK: true},
		{name: "480p square", resolution: "480p", aspect: "1:1", want: 230400, wantOK: true},
		{name: "720p 4:3", resolution: "720p", aspect: "4:3", want: 691200, wantOK: true},
		{name: "missing tier", resolution: "", aspect: "16:9", want: 0, wantOK: false},
		{name: "unrecognized tier", resolution: "ultra", aspect: "16:9", want: 0, wantOK: false},
		{name: "bogus ratio clamps to landscape", resolution: "720p", aspect: "9:1", want: 921600, wantOK: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := falVideoPixelProduct(tt.resolution, tt.aspect)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("falVideoPixelProduct(%q, %q) = (%v, %v), want (%v, %v)", tt.resolution, tt.aspect, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestFalVideoTokenCount(t *testing.T) {
	// fal's published math: a 10s 720p seedance-2.0 clip is ~$3.02 at
	// $0.014/1000 tokens — 216,000 tokens.
	if got := falVideoTokenCount("720p", "16:9", 10); got != 216000 {
		t.Fatalf("falVideoTokenCount(720p, 16:9, 10s) = %v, want 216000", got)
	}
	if got := falVideoTokenCount("", "16:9", 10); got != 0 {
		t.Fatalf("unknown resolution should yield 0 tokens, got %v", got)
	}
	if got := falVideoTokenCount("720p", "16:9", 0); got != 0 {
		t.Fatalf("unknown duration should yield 0 tokens, got %v", got)
	}
}

func TestSchemaStringInput(t *testing.T) {
	schema := &ModelInputSchema{Properties: map[string]SchemaProperty{
		"resolution":   {Name: "resolution", Enum: []string{"480p", "720p", "1080p", "4k"}, Default: "720p"},
		"aspect_ratio": {Name: "aspect_ratio", Enum: []string{"auto", "16:9", "9:16", "1:1"}, Default: "auto"},
		"prompt":       {Name: "prompt"},
	}}
	for _, tt := range []struct {
		name     string
		input    string
		explicit string
		want     string
	}{
		{name: "explicit accepted", input: "resolution", explicit: "1080p", want: "1080p"},
		{name: "blank falls to default", input: "resolution", explicit: "", want: "720p"},
		{name: "enum-rejected falls to default", input: "resolution", explicit: "2160p", want: "720p"},
		{name: "unconstrained keeps explicit", input: "prompt", explicit: "a tornado", want: "a tornado"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := schemaStringInput(schema, tt.input, tt.explicit); got != tt.want {
				t.Fatalf("schemaStringInput(%q, %q) = %q, want %q", tt.input, tt.explicit, got, tt.want)
			}
		})
	}
	if got := schemaStringInput(nil, "resolution", "1080p"); got != "1080p" {
		t.Fatalf("nil schema should keep explicit value, got %q", got)
	}
}

func TestFalVideoTokenEstimate(t *testing.T) {
	schema := &ModelInputSchema{Properties: map[string]SchemaProperty{
		"resolution":   {Name: "resolution", Enum: []string{"480p", "720p", "1080p", "4k"}, Default: "720p"},
		"aspect_ratio": {Name: "aspect_ratio", Enum: []string{"auto", "16:9", "9:16", "1:1"}, Default: "auto"},
	}}
	// The motivating turn: image-to-video, no resolution/duration from the
	// planner — schema default 720p, 10s rendered → 216,000 tokens.
	req := VideoGenerateRequest{Prompt: "tornado", Images: []string{"data:image/png;base64,AAAA"}}
	if got := falVideoTokenEstimate(schema, req, 0, 10); got != 216000 {
		t.Fatalf("token estimate = %v, want 216000", got)
	}
	// The conv_859908bf turn: seedance-2.5 reference-to-video over a 2.25s
	// source clip, 10.030998s rendered at the 720p default. fal's formula
	// bills input + output duration: 900 × (2.25 + 10.030998) × 24 =
	// 265,269.5568 tokens — the estimate this turn used to skip entirely.
	reqWithVideo := req
	reqWithVideo.Videos = []string{"data:video/mp4;base64,AAAA"}
	if got := falVideoTokenEstimate(schema, reqWithVideo, 2.25, 10.030998); got != 265269.5568 {
		t.Fatalf("video-source token estimate = %v, want 265269.5568", got)
	}
	// A video-input turn whose source durations are unknown (bytes never
	// local) skips rather than billing only the output half.
	if got := falVideoTokenEstimate(schema, reqWithVideo, 0, 10); got != 0 {
		t.Fatalf("video-input turn with unknown source seconds should estimate 0 tokens, got %v", got)
	}
	// Unknown rendered duration ("auto" and no parseable container): skip.
	if got := falVideoTokenEstimate(schema, req, 0, 0); got != 0 {
		t.Fatalf("unknown duration should estimate 0 tokens, got %v", got)
	}
	// A resolution tier the model's enum rejects bills at the model default.
	rejected := req
	rejected.Resolution = "2160p"
	if got := falVideoTokenEstimate(schema, rejected, 0, 10); got != 216000 {
		t.Fatalf("enum-rejected tier should fall to 720p default (216000 tokens), got %v", got)
	}
}

func TestFalVideoSourceSeconds(t *testing.T) {
	dataURL := func(data []byte) string {
		return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(data)
	}
	// Two parseable clips sum: 8s + 2.25s.
	got, ok := falVideoSourceSeconds([]string{
		dataURL(mediasnifftest.MP4WithDuration(0, 1000, 8000)),
		dataURL(mediasnifftest.MP4WithDuration(0, 1000, 2250)),
	})
	if !ok || got != 10.25 {
		t.Fatalf("source seconds = (%v, %v), want (10.25, true)", got, ok)
	}
	for name, videos := range map[string][]string{
		"hosted url":     {"https://v3.fal.media/files/x.mp4"},
		"non-mp4 bytes":  {dataURL([]byte("not a video at all"))},
		"unparseable":    {dataURL(tinyMP4())},
		"one of two":     {dataURL(mediasnifftest.MP4WithDuration(0, 1000, 8000)), "https://v3.fal.media/files/x.mp4"},
		"empty list":     nil,
		"empty data url": {""},
	} {
		if seconds, ok := falVideoSourceSeconds(videos); ok || seconds != 0 {
			t.Fatalf("%s: source seconds = (%v, %v), want (0, false) — a partial sum must not bill", name, seconds, ok)
		}
	}
}

func TestFalVideoReferenceDiscount(t *testing.T) {
	for model, want := range map[string]float64{
		"bytedance/seedance-2.0/reference-to-video":  0.6,
		"bytedance/seedance-2.5/reference-to-video":  0.6,
		" bytedance/seedance-2.5/reference-to-video": 0.6,
		"bytedance/seedance-1.5/reference-to-video":  0,
		"fal-ai/kling-video/v2.6/pro/motion-control": 0,
		"fal-ai/ltx-2.3-22b/extend-video":            0,
		"":                                           0,
	} {
		if got := falVideoReferenceDiscount(model); got != want {
			t.Fatalf("falVideoReferenceDiscount(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestFalVideoBilledMegapixels(t *testing.T) {
	schema := &ModelInputSchema{Properties: map[string]SchemaProperty{
		"num_frames": {Name: "num_frames", Default: float64(121)},
	}}
	// The motivating turn (conv_801b5619): ltx-2.3-22b extend over a 1344×768
	// clip. The rendered output carries 339 frames (243 source + 96 extension),
	// but the bill is the 121 generated frames — the schema default the request
	// rode when the planner set no duration knob.
	extend := VideoGenerateRequest{Prompt: "earthquake", Videos: []string{"data:video/mp4;base64,AAAA"}}
	if got := falVideoBilledMegapixels(schema, extend, mediasnifftest.MP4WithVideoTrack(1344, 768, 339)); got != 124.895232 {
		t.Fatalf("extend megapixels = %v, want 124.895232 (1344×768×121/1e6)", got)
	}
	// Pure generation: every frame in the container was rendered by the model,
	// so the stts count is the billed quantity.
	pure := VideoGenerateRequest{Prompt: "tornado"}
	if got := falVideoBilledMegapixels(schema, pure, mediasnifftest.MP4WithVideoTrack(1344, 768, 339)); got != 349.913088 {
		t.Fatalf("pure megapixels = %v, want 349.913088 (1344×768×339/1e6)", got)
	}
	// Pure generation with no parseable frame table falls back to the schema's
	// num_frames default.
	if got := falVideoBilledMegapixels(schema, pure, mediasnifftest.MP4WithVideoTrack(1280, 720)); got != 111.5136 {
		t.Fatalf("fallback megapixels = %v, want 111.5136 (1280×720×121/1e6)", got)
	}
	// Extend with no num_frames default on the schema: not computable, and a
	// zero quantity skips the estimate rather than guessing.
	if got := falVideoBilledMegapixels(&ModelInputSchema{}, extend, mediasnifftest.MP4WithVideoTrack(1344, 768, 339)); got != 0 {
		t.Fatalf("extend with no num_frames default should estimate 0, got %v", got)
	}
	// No frame size at all: not computable.
	if got := falVideoBilledMegapixels(schema, extend, mediasnifftest.MP4AudioOnly()); got != 0 {
		t.Fatalf("audio-only output should estimate 0, got %v", got)
	}
	if got := falVideoBilledMegapixels(schema, extend, nil); got != 0 {
		t.Fatalf("empty output should estimate 0, got %v", got)
	}
}

func TestImageResultMegapixels(t *testing.T) {
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(mediasnifftest.Image(t, "png", 64, 48))
	// Two 64×48 images + one legacy scalar: (3 × 3072 px) / 1e6.
	got := imageResultMegapixels([]string{dataURL, dataURL}, dataURL)
	if got != 3*64*48/1e6 {
		t.Fatalf("imageResultMegapixels = %v, want %v", got, 3*64*48/1e6)
	}
	// References that aren't data URLs are not fetched; undecodable payloads
	// contribute nothing.
	got = imageResultMegapixels([]string{"https://fal.media/far-away.png", "data:image/png;base64,!!!"}, "")
	if got != 0 {
		t.Fatalf("non-decodable references should contribute 0, got %v", got)
	}
}

func TestFalDurationSeconds(t *testing.T) {
	for _, tt := range []struct {
		duration string
		want     float64
		wantOK   bool
	}{
		{duration: "5", want: 5, wantOK: true},
		{duration: "7.5", want: 7.5, wantOK: true},
		{duration: "", wantOK: false},
		{duration: "10s", wantOK: false},
		{duration: "0", wantOK: false},
		{duration: "-3", wantOK: false},
	} {
		got, ok := falDurationSeconds(tt.duration)
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("falDurationSeconds(%q) = (%v, %v), want (%v, %v)", tt.duration, got, ok, tt.want, tt.wantOK)
		}
	}
}

// TestAudioBillingSeconds pins the per-second audio billing quantity: the
// rendered clip's own duration (what fal actually charged) wins over the
// requested length, the request stands in when the bytes don't probe, and
// neither known means not-ok — the cost is skipped rather than guessed.
func TestAudioBillingSeconds(t *testing.T) {
	rendered := 10 * 1152.0 / 44100 // mediasnifftest.MP3(10): ten 1152-sample frames at 44.1kHz
	for _, tt := range []struct {
		name      string
		requested string
		rendered  []byte
		want      float64
		wantOK    bool
	}{
		{"rendered wins over requested", "5", mediasnifftest.MP3(10), rendered, true},
		{"requested stands in for junk bytes", "7.5", []byte("not audio"), 7.5, true},
		{"requested with no bytes", "5", nil, 5, true},
		{"neither known", "", nil, 0, false},
		{"auto duration with no bytes", "auto", nil, 0, false},
	} {
		got, ok := audioBillingSeconds(tt.requested, tt.rendered)
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("audioBillingSeconds(%q, %d bytes) [%s] = (%v, %v), want (%v, %v)",
				tt.requested, len(tt.rendered), tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}

// falPricingTransport serves one pricing payload and counts requests. When
// failBatchOn is set, any request whose endpoint_id query carries that
// substring fails with 400 — fal validates a batch all-or-nothing, so one bad
// id fails the request for every id in it — while requests without it serve
// the body.
type falPricingTransport struct {
	requests    int
	lastQuery   string
	lastAuth    string
	body        string
	status      int
	failBatchOn string
}

func (transport *falPricingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.requests++
	transport.lastQuery = req.URL.RawQuery
	transport.lastAuth = req.Header.Get("Authorization")
	if transport.failBatchOn != "" && strings.Contains(req.URL.RawQuery, transport.failBatchOn) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"validation_error","message":"Bad request"}}`)),
			Header:     http.Header{},
		}, nil
	}
	body := transport.body
	if body == "" {
		body = `{"prices":[` +
			`{"endpoint_id":"fal-ai/flux/schnell","unit_price":0.003,"unit":"image","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/nano-banana-pro/edit","unit_price":0.15,"unit":"images","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/kling-video/v2/master/text-to-video","unit_price":0.07,"unit":"second","currency":"USD"},` +
			`{"endpoint_id":"bytedance/seedance-2.0/reference-to-video","unit_price":0.014,"unit":"1000 tokens","currency":"USD"},` +
			`{"endpoint_id":"bytedance/seedance-2.5/reference-to-video","unit_price":0.0214,"unit":"1000 tokens","currency":"USD"},` +
			`{"endpoint_id":"bytedance/seedream/v5/pro/edit","unit_price":0.0675,"unit":"units","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/f5-tts","unit_price":0.05,"unit":"1000 characters","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/minimax/voice-design","unit_price":3,"unit":"cloned voices","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/elevenlabs/sound-effects/v2","unit_price":0.002,"unit":"second","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/sync-lipsync/v2","unit_price":3,"unit":"minutes","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/nonusd/model","unit_price":1,"unit":"image","currency":"EUR"}` +
			`],"next_cursor":null,"has_more":false}`
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

func TestFalPricingCacheQuoteFetchesOnceAndCaches(t *testing.T) {
	transport := &falPricingTransport{}
	client := &http.Client{Transport: transport}
	cache := newFalPricingCache()
	ctx := context.Background()

	quote, ok := cache.quote(ctx, client, "key", "fal-ai/flux/schnell", []string{"fal-ai/flux/schnell", "fal-ai/kling-video/v2/master/text-to-video"})
	if !ok {
		t.Fatal("first quote missed")
	}
	if quote.Unit != "image" || quote.UnitPrice != 0.003 {
		t.Fatalf("quote = %+v, want unit image at 0.003", quote)
	}
	if transport.requests != 1 {
		t.Fatalf("pricing requests = %d after first quote, want 1", transport.requests)
	}
	if !strings.Contains(transport.lastQuery, "endpoint_id=fal-ai%2Fflux%2Fschnell") {
		t.Fatalf("query %q does not batch the endpoint ids", transport.lastQuery)
	}
	if transport.lastAuth != "Key key" {
		t.Fatalf("authorization = %q, want \"Key key\"", transport.lastAuth)
	}

	// Second quote within the TTL hits the cache only — the batched fetch
	// already covered the sibling model.
	quote, ok = cache.quote(ctx, client, "key", "fal-ai/kling-video/v2/master/text-to-video", []string{"fal-ai/flux/schnell"})
	if !ok || quote.Unit != "second" || quote.UnitPrice != 0.07 {
		t.Fatalf("sibling quote = %+v ok=%v, want kling second at 0.07", quote, ok)
	}
	if transport.requests != 1 {
		t.Fatalf("pricing requests = %d after sibling quote, want 1 (cache)", transport.requests)
	}

	// Non-USD quotes are skipped: the EUR endpoint is unknown.
	if _, ok := cache.quotes["fal-ai/nonusd/model"]; ok {
		t.Fatal("non-USD quote should not be cached")
	}
}

func TestFalPricingCacheStaleRefetches(t *testing.T) {
	transport := &falPricingTransport{}
	cache := newFalPricingCache()
	cache.fetchedAt = time.Now().Add(-falPricingTTL - time.Minute)
	cache.quotes = map[string]falPriceQuote{"fal-ai/flux/schnell": {UnitPrice: 0.003, Unit: "image"}}
	if _, ok := cache.quote(context.Background(), &http.Client{Transport: transport}, "key", "fal-ai/flux/schnell", nil); !ok {
		t.Fatal("stale quote refetch failed")
	}
	if transport.requests != 1 {
		t.Fatalf("pricing requests = %d, want 1 (stale cache refetches)", transport.requests)
	}
}

func TestFalPricingCacheFailureNegativeTTL(t *testing.T) {
	transport := &falPricingTransport{status: http.StatusInternalServerError}
	cache := newFalPricingCache()
	ctx := context.Background()
	if _, ok := cache.quote(ctx, &http.Client{Transport: transport}, "key", "fal-ai/flux/schnell", nil); ok {
		t.Fatal("failing fetch should not produce a quote")
	}
	// Batch and solo fallback both fail — 2 requests — and only then does the
	// negative window open.
	if transport.requests != 2 {
		t.Fatalf("pricing requests = %d, want 2 (batch + solo fallback)", transport.requests)
	}
	// Within the negative window no retry: an offline machine must not hit
	// the pricing endpoint on every media turn.
	if _, ok := cache.quote(ctx, &http.Client{Transport: transport}, "key", "fal-ai/flux/schnell", nil); ok {
		t.Fatal("negative-cached quote should miss")
	}
	if transport.requests != 2 {
		t.Fatalf("pricing requests = %d within negative TTL, want 2", transport.requests)
	}
}

// TestFalPricingCachePoisonedBatchFallsBackToSolo is the regression shape of
// conv_ebcb7dff: a non-endpoint value typed into a Settings model slot rides
// the configured batch, fal rejects the WHOLE batched pricing request for it,
// and the speech generation under test priced to zero even though its own
// endpoint had a good quote ($0.1 per 1000 characters). The cache must fall
// back to the one id the call prices, merge it in, and serve later calls from
// that merge without re-fetching.
func TestFalPricingCachePoisonedBatchFallsBackToSolo(t *testing.T) {
	transport := &falPricingTransport{
		failBatchOn: "Kling",
		body:        `{"prices":[{"endpoint_id":"fal-ai/minimax/speech-2.8-hd","unit_price":0.1,"unit":"1000 characters","currency":"USD"}],"next_cursor":null,"has_more":false}`,
	}
	client := &http.Client{Transport: transport}
	cache := newFalPricingCache()
	ctx := context.Background()

	// The batch the gateway builds carries the junk Settings value; the
	// requested speech endpoint prices anyway. 13 characters × $0.1/1000.
	got := estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/minimax/speech-2.8-hd", []string{"Kling", "fal-ai/esrgan"}, falBillingHints{Requests: 1, Characters: 13})
	if got != 1300 {
		t.Fatalf("speech estimate = %d, want 1300 (13 characters at $0.1/1000)", got)
	}
	if transport.requests != 2 {
		t.Fatalf("pricing requests = %d, want 2 (poisoned batch + solo fallback)", transport.requests)
	}
	if !strings.Contains(transport.lastQuery, "endpoint_id=fal-ai%2Fminimax%2Fspeech-2.8-hd") || strings.Contains(transport.lastQuery, "Kling") {
		t.Fatalf("fallback query %q should carry the speech id alone", transport.lastQuery)
	}

	// The merged quote serves later calls without another fetch.
	if got := estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/minimax/speech-2.8-hd", []string{"Kling"}, falBillingHints{Requests: 1, Characters: 13}); got != 1300 {
		t.Fatalf("cached speech estimate = %d, want 1300", got)
	}
	if transport.requests != 2 {
		t.Fatalf("pricing requests = %d after cached quote, want 2 (merged quote serves)", transport.requests)
	}
}

func TestEstimateFalCostMicros(t *testing.T) {
	transport := &falPricingTransport{}
	client := &http.Client{Transport: transport}
	cache := newFalPricingCache()
	ctx := context.Background()

	// Per-image: 4 images × $0.003 = $0.012 = 12000 micros.
	got := estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/flux/schnell", nil, falBillingHints{Images: 4, Requests: 1})
	if got != 12000 {
		t.Fatalf("image estimate = %d, want 12000", got)
	}
	// The nano-banana-pro family's plural "images" unit prices identically —
	// conv_a465108a5f7ec78267191163's turn stamped costUnknown because the
	// plural fell through every case. 1 image × $0.15.
	got = estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/nano-banana-pro/edit", nil, falBillingHints{Images: 1, Requests: 1})
	if got != 150000 {
		t.Fatalf("nano-banana estimate = %d, want 150000", got)
	}
	// Per-second: 5s × $0.07 = $0.35.
	got = estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/kling-video/v2/master/text-to-video", nil, falBillingHints{Seconds: 5, Requests: 1})
	if got != 350000 {
		t.Fatalf("video estimate = %d, want 350000", got)
	}
	// Token-billed: 216,000 tokens (a 10s 720p seedance-2.0 clip) at
	// $0.014/1000 = $3.024 — fal's published "~$3.02 for a 10-sec clip".
	got = estimateFalCostMicros(ctx, cache, client, "key", "bytedance/seedance-2.0/reference-to-video", nil, falBillingHints{Tokens: 216000, Requests: 1})
	if got != 3024000 {
		t.Fatalf("token-billed estimate = %d, want 3024000", got)
	}
	// The video-reference discount multiplies the price after quantity ×
	// unit price: $3.024 × 0.6 = $1.8144.
	got = estimateFalCostMicros(ctx, cache, client, "key", "bytedance/seedance-2.0/reference-to-video", nil, falBillingHints{Tokens: 216000, Requests: 1, PriceMultiplier: 0.6})
	if got != 1814400 {
		t.Fatalf("discounted token estimate = %d, want 1814400", got)
	}
	// The conv_859908bf bill, end to end: a seedance-2.5 reference-to-video
	// turn over a 2.25s source clip that rendered 10.030998s at the 720p
	// default — 265,269.5568 tokens at $0.0214/1000, ×0.6 video-reference
	// discount = $3.406061. Before this pricing path existed the turn reported
	// only the text-model's $0.000067.
	schema := &ModelInputSchema{Properties: map[string]SchemaProperty{
		"resolution":   {Name: "resolution", Enum: []string{"480p", "720p", "1080p"}, Default: "720p"},
		"aspect_ratio": {Name: "aspect_ratio", Enum: []string{"auto", "16:9", "9:16"}, Default: "auto"},
	}}
	videoReq := VideoGenerateRequest{Prompt: "scissor-land", Videos: []string{"data:video/mp4;base64,AAAA"}}
	got = estimateFalCostMicros(ctx, cache, client, "key", "bytedance/seedance-2.5/reference-to-video", nil, falBillingHints{
		Requests:        1,
		Tokens:          falVideoTokenEstimate(schema, videoReq, 2.25, 10.030998),
		PriceMultiplier: falVideoReferenceDiscount("bytedance/seedance-2.5/reference-to-video"),
	})
	if got != 3406061 {
		t.Fatalf("conv_859908bf estimate = %d, want 3406061", got)
	}
	// Unit-billed: seedream v5 prices "units" per generated image
	// (conv_26ef0c4f) — 1 unit × $0.0675 = $0.0675.
	got = estimateFalCostMicros(ctx, cache, client, "key", "bytedance/seedream/v5/pro/edit", nil, falBillingHints{Images: 1, Requests: 1})
	if got != 67500 {
		t.Fatalf("unit-billed estimate = %d, want 67500", got)
	}
	// Character-block TTS: 120 characters at $0.05/1000 = $0.006.
	got = estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/f5-tts", nil, falBillingHints{Characters: 120, Requests: 1})
	if got != 6000 {
		t.Fatalf("character-billed estimate = %d, want 6000", got)
	}
	// Voice design (the conv_1a989138830e7eaa29f6c429 bill): fal prices the
	// endpoint per "cloned voices" — $3 per designed voice, one per call —
	// so a design with the 91-character default preview prices a flat $3.00
	// whatever the preview says. While the unit was unknown the turn stamped
	// costUnknown and rendered "est. ?".
	got = estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/minimax/voice-design", nil, falBillingHints{Requests: 1, Characters: 91})
	if got != 3000000 {
		t.Fatalf("voice-design estimate = %d, want 3000000", got)
	}
	// Per-second sound generation (the conv_a3fecc876e6360a50ed6e8b5 bill): the
	// elevenlabs sound-effects model rendered a 3s clip at $0.002/s = $0.006 —
	// before the audio path carried a Seconds hint the turn read "?" for it.
	got = estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/elevenlabs/sound-effects/v2", nil, falBillingHints{Seconds: 3, Requests: 1})
	if got != 6000 {
		t.Fatalf("per-second audio estimate = %d, want 6000", got)
	}
	// Per-minute lipsync (the conv_d5a03dfe20e5225352212c2b bill): sync-lipsync
	// v2 prices per minute of output video — the turn's 0.875s rendered clip
	// is 0.875/60 min × $3 = $0.04375. Before the minutes unit was taught the
	// estimate priced to zero and the activity stamped costUnknown.
	got = estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/sync-lipsync/v2", nil, falBillingHints{Requests: 1, Seconds: 0.875})
	if got != 43750 {
		t.Fatalf("per-minute lipsync estimate = %d, want 43750", got)
	}
	// Fail-soft: nil cache, empty key, unknown endpoint, and an unpriceable
	// unit (per-second model with no duration hint) all yield 0.
	if estimateFalCostMicros(ctx, nil, client, "key", "x", nil, falBillingHints{}) != 0 {
		t.Fatal("nil cache should estimate 0")
	}
	if estimateFalCostMicros(ctx, cache, client, "  ", "x", nil, falBillingHints{Images: 1}) != 0 {
		t.Fatal("empty key should estimate 0")
	}
	if estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/unknown", nil, falBillingHints{Images: 1}) != 0 {
		t.Fatal("unknown endpoint should estimate 0")
	}
	if estimateFalCostMicros(ctx, cache, client, "key", "fal-ai/kling-video/v2/master/text-to-video", nil, falBillingHints{Requests: 1}) != 0 {
		t.Fatal("per-second model with no seconds hint should estimate 0, not guess")
	}
	// A token-billed model with no computable token count (video inputs the
	// gateway never probed) estimates 0, never zero tokens.
	if estimateFalCostMicros(ctx, cache, client, "key", "bytedance/seedance-2.0/reference-to-video", nil, falBillingHints{Requests: 1}) != 0 {
		t.Fatal("token-billed model with no token count should estimate 0, not guess")
	}
}

func TestFalVideoBilledSeconds(t *testing.T) {
	// The rendered container wins: it prices what was actually produced,
	// including "auto" durations the request never stated.
	if got, ok := falVideoBilledSeconds(mediasnifftest.MP4WithDuration(0, 1000, 8000), "5"); !ok || got != 8 {
		t.Fatalf("billed seconds = (%v, %v), want (8, true) from the container", got, ok)
	}
	// Unparseable container falls back to the requested duration.
	if got, ok := falVideoBilledSeconds(tinyMP4(), "5"); !ok || got != 5 {
		t.Fatalf("billed seconds = (%v, %v), want (5, true) from the request", got, ok)
	}
	// Neither source knows: not-ok, never a guess.
	if _, ok := falVideoBilledSeconds(tinyMP4(), ""); ok {
		t.Fatal("billed seconds should be not-ok with no container and no requested duration")
	}
}

// TestFalLipsyncBillingSeconds pins the per-minute lipsync billing quantity:
// the rendered clip's own container (what fal charged for) wins, the driving
// audio's data-URL duration stands in when the container won't parse, and
// neither measurable means not-ok — the cost is skipped rather than guessed.
func TestFalLipsyncBillingSeconds(t *testing.T) {
	// Container wins: the conv_d5a03dfe turn's 0.875s output.
	if got, ok := falLipsyncBillingSeconds(mediasnifftest.MP4WithDuration(0, 1000, 875), "data:audio/mp4;base64,AAAA"); !ok || got != 0.875 {
		t.Fatalf("lipsync billed seconds = (%v, %v), want (0.875, true) from the container", got, ok)
	}
	// Unparseable output falls back to the driving audio: a 1s WAV (8000
	// payload bytes at the fixture's 8kHz byte rate).
	wavURL := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(mediasnifftest.WAV(8000))
	if got, ok := falLipsyncBillingSeconds(tinyMP4(), wavURL); !ok || got != 1 {
		t.Fatalf("lipsync billed seconds = (%v, %v), want (1, true) from the audio", got, ok)
	}
	// A hosted (non-data-URL) audio reference is not fetched — not-ok.
	if _, ok := falLipsyncBillingSeconds(tinyMP4(), "https://v3.fal.media/files/x.m4a"); ok {
		t.Fatal("hosted audio reference should not price")
	}
	// Neither source measurable: not-ok.
	if _, ok := falLipsyncBillingSeconds(tinyMP4(), ""); ok {
		t.Fatal("lipsync billed seconds should be not-ok with no container and no audio")
	}
}

// TestFalBillsWholeMinutes pins the whole-minute rule the elevenlabs voice
// changer bills by. conv_8a298a46840d3419c906c462: two change_voice turns over
// a 10.144s source recorded $0.05072 each — 10.144/60 of fal's $0.30/min —
// while fal's dashboard charged $0.30 for the request, one full minute. The
// flag rides the endpoint alone (lipsync's per-minute quote stays pro rata),
// the length rounds the way fal documents its per-minute audio billing, and
// the estimate lands on the whole minute.
func TestFalBillsWholeMinutes(t *testing.T) {
	// The rule is the endpoint's, not the unit's: fal's own comparison
	// prices a 10s Lipsync 2 Pro clip at roughly $0.83 against $5/min.
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{defaultFalVoiceChangerModel, true},
		{"  " + defaultFalVoiceChangerModel + "  ", true},
		{"fal-ai/sync-lipsync/v2", false},
		{"fal-ai/sync-lipsync/v2/pro", false},
		{"fal-ai/sync-lipsync", false},
		{"", false},
	} {
		if got := falBillsWholeMinutes(tc.model); got != tc.want {
			t.Errorf("falBillsWholeMinutes(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}

	// fal's documented order — whole seconds, then up to the next full
	// minute, one minute at minimum — in its own worked examples.
	for _, tc := range []struct {
		seconds float64
		want    float64
	}{
		{10.144, 1},        // the conv's source
		{0.25, 1},          // any part of a minute bills the minute
		{59.9, 1},          // it rounds to 60 whole seconds: still one minute
		{60, 1},            // exactly one minute is one minute
		{61, 2},            // one second over is the next full minute
		{300.011, 5},       // fal: "A 5:00.011 file is 5 units"
		{301, 6},           // fal: "a 5:01 file is 6"
		{3600.0000001, 60}, // probe dust never bills a second minute
	} {
		if got := falWholeMinutes(tc.seconds); got != tc.want {
			t.Errorf("falWholeMinutes(%v) = %v, want %v", tc.seconds, got, tc.want)
		}
	}

	// End to end at fal's live quote ($0.3 per minutes, 2026-11-24).
	transport := &falPricingTransport{body: `{"prices":[` +
		`{"endpoint_id":"` + defaultFalVoiceChangerModel + `","unit_price":0.3,"unit":"minutes","currency":"USD"},` +
		`{"endpoint_id":"fal-ai/sync-lipsync/v2","unit_price":3,"unit":"minutes","currency":"USD"}` +
		`],"next_cursor":null,"has_more":false}`}
	client := &http.Client{Transport: transport}
	cache := newFalPricingCache()
	ctx := context.Background()
	hints := falBillingHints{Requests: 1, Seconds: 10.144, WholeMinutes: falBillsWholeMinutes(defaultFalVoiceChangerModel)}
	if got := estimateFalCostMicros(ctx, cache, client, "key", defaultFalVoiceChangerModel, nil, hints); got != 300000 {
		t.Errorf("a 10.144s voice change estimated %d micros, want 300000 — the $0.30 fal charged", got)
	}
	// What the same turn recorded before the rule: the fractional minute.
	hints.WholeMinutes = false
	if got := estimateFalCostMicros(ctx, cache, client, "key", defaultFalVoiceChangerModel, nil, hints); got != 50720 {
		t.Errorf("a 10.144s voice change at a fractional minute = %d micros, want the 50720 conv_8a298a46840d3419c906c462 recorded", got)
	}
	// Lipsync keeps its pro-rata minute: the call site's own expression
	// (falBillsWholeMinutes on the model it prices) leaves the quote alone.
	const lipsync = "fal-ai/sync-lipsync/v2"
	lipsyncHints := falBillingHints{Requests: 1, Seconds: 10, WholeMinutes: falBillsWholeMinutes(lipsync)}
	if got := estimateFalCostMicros(ctx, cache, client, "key", lipsync, nil, lipsyncHints); got != 500000 {
		t.Errorf("a 10s lipsync estimated %d micros, want 500000 at 10/60 of $3 — pro rata, not a whole minute", got)
	}
}

func TestConfiguredFalEndpointIDs(t *testing.T) {
	config := defaultAppConfig()
	ids := configuredFalEndpointIDs(config, "custom/extra")
	// The list may repeat (two categories sharing one configured model);
	// fetch() dedupes — here only coverage matters.
	found := make(map[string]bool)
	for _, id := range ids {
		if id == "" {
			t.Fatal("empty endpoint id in batch")
		}
		found[id] = true
	}
	if !found["custom/extra"] {
		t.Fatal("extra endpoint id missing from batch")
	}
	if !found[defaultFalVideoModel] || !found[defaultFalTranscribeModel] {
		t.Fatal("default video/transcribe models missing from batch")
	}
	// A configured override rides the batch so the pricing refresh covers it.
	config.Providers.Fal.VideoModel = "fal-ai/veo3.1/text-to-video"
	ids = configuredFalEndpointIDs(config)
	found = make(map[string]bool)
	for _, id := range ids {
		found[id] = true
	}
	if !found["fal-ai/veo3.1/text-to-video"] {
		t.Fatal("configured override missing from batch")
	}
}

// TestGatewayGenerateVideoPricesTokenBilledModel drives the real GenerateVideo
// gateway closure against a mocked fal (schema + queue + download + pricing)
// and pins the stamped CostMicros for a token-billed model. This is the
// regression shape of conv_c9a17b4c860500ddff15984f: a seedance-2.0
// image-to-video turn where the planner emitted neither duration nor
// resolution — the estimate must now come from the schema-default tier
// ("720p") and the rendered clip's own container duration.
func TestGatewayGenerateVideoPricesTokenBilledModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/v1/models/billing-events"):
			// No billing event posted: the estimate these tests pin is the fallback.
			return jsonResponse(`{"billing_events":[],"next_cursor":null,"has_more":false}`), nil
		case strings.HasPrefix(req.URL.Path, "/v1/models/pricing"):
			return jsonResponse(`{"prices":[` +
				`{"endpoint_id":"bytedance/seedance-2.0/reference-to-video","unit_price":0.014,"unit":"1000 tokens","currency":"USD"}` +
				`],"next_cursor":null,"has_more":false}`), nil
		case strings.Contains(req.URL.Path, "/api/openapi/"):
			// Minimal seedance-2.0-shaped input schema: prompt + scalar
			// image_url, with the resolution/aspect_ratio defaults the token
			// estimate must fall back to.
			return jsonResponse(`{"components":{"schemas":{"SeedanceInput":{"type":"object","required":["prompt"],` +
				`"properties":{"prompt":{"type":"string"},"image_url":{"type":"string"},` +
				`"resolution":{"type":"string","enum":["480p","720p","1080p","4k"],"default":"720p"},` +
				`"aspect_ratio":{"type":"string","enum":["auto","16:9","9:16","1:1"],"default":"auto"}}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			switch {
			case req.Method == http.MethodPost:
				return jsonResponse(`{"request_id":"req-tok-1"}`), nil
			case strings.HasSuffix(req.URL.Path, "/status"):
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			case strings.HasSuffix(req.URL.Path, "/requests/req-tok-1"):
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/dl/token-video.mp4","content_type":"video/mp4"}}`), nil
			default:
				// The clip download: a 10-second container, priced instead of
				// the request's absent "auto" duration.
				return mp4Resp(mediasnifftest.MP4WithDuration(0, 1000, 10000), "video/mp4"), nil
			}
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		return nil, nil
	})

	gateway := newToolGateway(app, config)
	generated, err := gateway.tools.GenerateVideo(context.Background(), VideoGenerateRequest{
		Model:  "bytedance/seedance-2.0/reference-to-video",
		Prompt: "A huge tornado lifts and spins the house.",
		Images: []string{tinyPNG},
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	// 10s rendered at the schema-default 720p (auto aspect → 16:9) =
	// 216,000 tokens × $0.014/1000 = $3.024 — fal's published rate card.
	if generated.CostMicros != 3024000 {
		t.Fatalf("CostMicros = %d, want 3024000 (216000 tokens at $0.014/1000)", generated.CostMicros)
	}
}

// TestGatewayGenerateVideoPricesTokenBilledVideoSourceTurn is the regression
// shape of conv_859908bf: a seedance-2.5 reference-to-video turn over an
// attached 2.25s source clip that rendered 10.030998s at the 720p default.
// fal's token formula bills input_video_duration + output_duration, and the
// model page multiplies the price by 0.6 when video references ride: 900 ×
// 12.280998 × 24 = 265,269.5568 tokens at $0.0214/1000 × 0.6 = $3.406061.
// Before the source-duration capture existed this turn priced to zero and the
// ledger showed only the text model's $0.000067.
func TestGatewayGenerateVideoPricesTokenBilledVideoSourceTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/v1/models/billing-events"):
			// No billing event posted: the estimate these tests pin is the fallback.
			return jsonResponse(`{"billing_events":[],"next_cursor":null,"has_more":false}`), nil
		case strings.HasPrefix(req.URL.Path, "/v1/models/pricing"):
			return jsonResponse(`{"prices":[` +
				`{"endpoint_id":"bytedance/seedance-2.5/reference-to-video","unit_price":0.0214,"unit":"1000 tokens","currency":"USD"}` +
				`],"next_cursor":null,"has_more":false}`), nil
		case strings.Contains(req.URL.String(), "storage/auth/token"):
			// Video sources force-host to fal's CDN before the submit.
			return jsonResponse(`{"token":"cdn-test-token","token_type":"Bearer","base_url":"https://v3.test.fal.media"}`), nil
		case strings.HasSuffix(req.URL.Path, "/files/upload"):
			return jsonResponse(`{"access_url":"https://v3.test.fal.media/files/abc/source.mp4","uploaded":true}`), nil
		case strings.Contains(req.URL.Path, "/api/openapi/"):
			// Minimal seedance-2.5 reference-to-video-shaped schema: prompt +
			// the video_url source slot, with the resolution/aspect_ratio
			// defaults the token estimate falls back to.
			return jsonResponse(`{"components":{"schemas":{"Seedance25Input":{"type":"object","required":["prompt"],` +
				`"properties":{"prompt":{"type":"string"},"video_url":{"type":"string"},` +
				`"resolution":{"type":"string","enum":["480p","720p","1080p"],"default":"720p"},` +
				`"aspect_ratio":{"type":"string","enum":["auto","16:9","9:16"],"default":"auto"},` +
				`"task":{"type":"string","enum":["reference","editing","extension"],"default":"reference"}}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			switch {
			case req.Method == http.MethodPost:
				return jsonResponse(`{"request_id":"req-tok-2"}`), nil
			case strings.HasSuffix(req.URL.Path, "/status"):
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			case strings.HasSuffix(req.URL.Path, "/requests/req-tok-2"):
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/dl/token-video-source.mp4","content_type":"video/mp4"}}`), nil
			default:
				// The clip download: a 10.030998-second container — the
				// output_duration term of the formula.
				return mp4Resp(mediasnifftest.MP4WithDuration(0, 1000000, 10030998), "video/mp4"), nil
			}
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		return nil, nil
	})

	gateway := newToolGateway(app, config)
	generated, err := gateway.tools.GenerateVideo(context.Background(), VideoGenerateRequest{
		Model:  "bytedance/seedance-2.5/reference-to-video",
		Prompt: "The girl stops walking and looks around Scissor-land.",
		Videos: []string{"data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mediasnifftest.MP4WithDuration(0, 1000, 2250))},
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if generated.CostMicros != 3406061 {
		t.Fatalf("CostMicros = %d, want 3406061 (265269.5568 tokens at $0.0214/1000, ×0.6 video-reference discount)", generated.CostMicros)
	}
}

func TestGatewayGenerateVideoPricesMegapixelBilledModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/v1/models/billing-events"):
			// No billing event posted: the estimate these tests pin is the fallback.
			return jsonResponse(`{"billing_events":[],"next_cursor":null,"has_more":false}`), nil
		case strings.HasPrefix(req.URL.Path, "/v1/models/pricing"):
			return jsonResponse(`{"prices":[` +
				`{"endpoint_id":"fal-ai/ltx-2.3-22b/extend-video","unit_price":0.001605,"unit":"megapixel","currency":"USD"}` +
				`],"next_cursor":null,"has_more":false}`), nil
		case strings.Contains(req.URL.String(), "storage/auth/token"):
			// Video sources force-host to fal's CDN before the submit.
			return jsonResponse(`{"token":"cdn-test-token","token_type":"Bearer","base_url":"https://v3.test.fal.media"}`), nil
		case strings.HasSuffix(req.URL.Path, "/files/upload"):
			return jsonResponse(`{"access_url":"https://v3.test.fal.media/files/abc/source.mp4","uploaded":true}`), nil
		case strings.Contains(req.URL.Path, "/api/openapi/"):
			// Minimal ltx-2.3-22b extend-shaped input schema: prompt + the
			// video_url source slot, with the num_frames default the megapixel
			// estimate must read (Atelier maps no canonical knob onto it).
			return jsonResponse(`{"components":{"schemas":{"LtxExtendInput":{"type":"object","required":["prompt"],` +
				`"properties":{"prompt":{"type":"string"},"video_url":{"type":"string"},` +
				`"num_frames":{"type":"integer","minimum":9,"maximum":481,"default":121}}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			switch {
			case req.Method == http.MethodPost:
				return jsonResponse(`{"request_id":"req-mp-1"}`), nil
			case strings.HasSuffix(req.URL.Path, "/status"):
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			case strings.HasSuffix(req.URL.Path, "/requests/req-mp-1"):
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/dl/megapixel-video.mp4","content_type":"video/mp4"}}`), nil
			default:
				// The clip download: a 1344×768 container carrying 339 frames
				// (243 source + 96 extension). The extend turn must bill the
				// 121 generated frames, not the container's total.
				return mp4Resp(mediasnifftest.MP4WithVideoTrack(1344, 768, 339), "video/mp4"), nil
			}
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		return nil, nil
	})

	gateway := newToolGateway(app, config)
	generated, err := gateway.tools.GenerateVideo(context.Background(), VideoGenerateRequest{
		Model:  "fal-ai/ltx-2.3-22b/extend-video",
		Prompt: "The girl experiences an earthquake, with a rumble noise.",
		Video:  "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(tinyMP4()),
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	// 1344×768 × 121 schema-default frames = 124.895232 MP × $0.001605/MP =
	// $0.200457 — fal's published rate card for the 22B family.
	if generated.CostMicros != 200457 {
		t.Fatalf("CostMicros = %d, want 200457 (124.895232 MP at $0.001605)", generated.CostMicros)
	}
}

// TestGatewayGenerateLipsyncPricesMinuteBilledModel drives the real
// GenerateLipsync gateway closure against a mocked fal (schema + CDN upload +
// queue + download + pricing) and pins the stamped CostMicros for a
// per-minute-billed model. This is the regression shape of
// conv_d5a03dfe20e5225352212c2b: a sync-lipsync/v2 call over a 0.84s audio
// clip that rendered 0.875s of video — the estimate must come from the
// rendered clip's own container duration, and before the minutes unit was
// taught the turn stamped costUnknown.
func TestGatewayGenerateLipsyncPricesMinuteBilledModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/v1/models/billing-events"):
			// No billing event posted: the estimate these tests pin is the fallback.
			return jsonResponse(`{"billing_events":[],"next_cursor":null,"has_more":false}`), nil
		case strings.HasPrefix(req.URL.Path, "/v1/models/pricing"):
			return jsonResponse(`{"prices":[` +
				`{"endpoint_id":"fal-ai/sync-lipsync/v2","unit_price":3,"unit":"minutes","currency":"USD"}` +
				`],"next_cursor":null,"has_more":false}`), nil
		case strings.Contains(req.URL.String(), "storage/auth/token"):
			// The driving audio force-hosts to fal's CDN before the submit.
			return jsonResponse(`{"token":"cdn-test-token","token_type":"Bearer","base_url":"https://v3.test.fal.media"}`), nil
		case strings.HasSuffix(req.URL.Path, "/files/upload"):
			return jsonResponse(`{"access_url":"https://v3.test.fal.media/files/abc/audio.mp3","uploaded":true}`), nil
		case strings.Contains(req.URL.Path, "/api/openapi/"):
			// Minimal sync-lipsync-shaped input schema: the driving audio_url
			// plus the video_url face slot.
			return jsonResponse(`{"components":{"schemas":{"SyncLipsyncInput":{"type":"object","required":["audio_url","video_url"],` +
				`"properties":{"audio_url":{"type":"string"},"video_url":{"type":"string"},` +
				`"sync_mode":{"type":"string","enum":["cut_off","loop","bounce","silence","remap"],"default":"cut_off"}}}}}}`), nil
		}
		if strings.Contains(req.URL.Host, "fal.run") {
			switch {
			case req.Method == http.MethodPost:
				return jsonResponse(`{"request_id":"req-lip-1"}`), nil
			case strings.HasSuffix(req.URL.Path, "/status"):
				return jsonResponse(`{"status":"COMPLETED"}`), nil
			case strings.HasSuffix(req.URL.Path, "/requests/req-lip-1"):
				return jsonResponse(`{"video":{"url":"https://queue.fal.run/dl/lipsync-video.mp4","content_type":"video/mp4"}}`), nil
			default:
				// The clip download: a 0.875-second container — the rendered
				// length the per-minute bill tracks (the conv's output shape).
				return mp4Resp(mediasnifftest.MP4WithDuration(0, 1000, 875), "video/mp4"), nil
			}
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		return nil, nil
	})

	gateway := newToolGateway(app, config)
	generated, err := gateway.tools.GenerateLipsync(context.Background(), LipsyncGenerateRequest{
		Model: "fal-ai/sync-lipsync/v2",
		Audio: "data:audio/mpeg;base64," + base64.StdEncoding.EncodeToString(mediasnifftest.MP3(4)),
		Video: "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(tinyMP4()),
	})
	if err != nil {
		t.Fatalf("GenerateLipsync: %v", err)
	}
	// 0.875s rendered = 0.875/60 min × $3/min = $0.04375 — fal's rate card
	// for the endpoint, priced from the rendered container.
	if generated.CostMicros != 43750 {
		t.Fatalf("CostMicros = %d, want 43750 (0.875s at $3/minute)", generated.CostMicros)
	}
}

// falBillingTransport serves one billing-events payload (or a failure) and
// records the request, so the reported-cost lookup's shape can be pinned.
type falBillingTransport struct {
	requests int
	path     string
	query    url.Values
	auth     string
	status   int
	body     string
	err      error
}

func (transport *falBillingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.requests++
	transport.path = req.URL.Path
	transport.query = req.URL.Query()
	transport.auth = req.Header.Get("Authorization")
	if transport.err != nil {
		return nil, transport.err
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Header:     http.Header{},
	}, nil
}

// TestFetchFalBillingCost pins the one reported-cost request: what it asks
// fal for, and how each answer maps onto a lookup outcome. The event shape is
// the billing-events feed's documented record.
func TestFetchFalBillingCost(t *testing.T) {
	const reported = `{"request_id":"req-1","endpoint_id":"minimax/h3-max/reference-to-video","timestamp":"2026-11-23T18:09:00Z",` +
		`"quantity":10,"output_units":10,"unit":"second","unit_price":0.16,"percent_discount":null,` +
		`"cost_subtotal":1.6,"cost_discount":0,"cost_total":1.6,"cost_estimate_nano_usd":1600000000}`
	const surcharge = `{"request_id":"req-1","endpoint_id":"x","timestamp":"2026-11-23T18:09:00Z","quantity":1024,"output_units":1024,` +
		`"unit":"tokens","unit_price":0.02,"percent_discount":null,"cost_subtotal":0.02048,"cost_discount":0,` +
		`"cost_total":0.02048,"cost_estimate_nano_usd":20480000}`
	tests := []struct {
		name       string
		status     int
		body       string
		err        error
		wantMicros int64
		want       falBillingLookup
	}{
		{
			name:       "the nano-USD field is the charge",
			body:       `{"billing_events":[` + reported + `],"next_cursor":null,"has_more":false}`,
			wantMicros: 1600000,
			want:       falBillingReported,
		},
		{
			name:       "cost_total stands in when the nano field is absent",
			body:       `{"billing_events":[{"request_id":"req-1","cost_total":1.62048}],"next_cursor":null,"has_more":false}`,
			wantMicros: 1620480,
			want:       falBillingReported,
		},
		{
			// A request can bill as several lines — fal's own combined
			// examples are an output plus a reference-token surcharge.
			name:       "every line carrying the request id is summed",
			body:       `{"billing_events":[` + reported + `,` + surcharge + `],"next_cursor":null,"has_more":false}`,
			wantMicros: 1620480,
			want:       falBillingReported,
		},
		{
			name: "another request's event is never harvested",
			body: `{"billing_events":[{"request_id":"req-other","cost_total":9,"cost_estimate_nano_usd":9000000000}]}`,
			want: falBillingMissing,
		},
		{
			name: "the feed answers before billing has posted the event",
			body: `{"billing_events":[],"next_cursor":null,"has_more":false}`,
			want: falBillingMissing,
		},
		{
			// "Not reported" and "reported as free" both read zero here; the
			// estimate then has its say, and a free endpoint quotes zero
			// anyway.
			name: "a zero-cost event is not a report",
			body: `{"billing_events":[{"request_id":"req-1","cost_total":0,"cost_estimate_nano_usd":0}]}`,
			want: falBillingMissing,
		},
		{
			name: "a sub-micro charge rounds away to not-reported",
			body: `{"billing_events":[{"request_id":"req-1","cost_estimate_nano_usd":400}]}`,
			want: falBillingMissing,
		},
		{name: "403 is a refusal", status: http.StatusForbidden, body: `{"error":{"type":"authorization_error","message":"Access denied"}}`, want: falBillingDenied},
		{name: "401 is a refusal", status: http.StatusUnauthorized, body: `{"error":{"type":"authorization_error","message":"Invalid API key"}}`, want: falBillingDenied},
		{name: "404 is a refusal (the feed is not enabled for the account)", status: http.StatusNotFound, want: falBillingDenied},
		{name: "500 is a transient failure", status: http.StatusInternalServerError, want: falBillingFailed},
		{name: "429 is a transient failure", status: http.StatusTooManyRequests, want: falBillingFailed},
		{name: "a transport failure is transient", err: errors.New("connection reset"), want: falBillingFailed},
		{name: "an undecodable payload is transient", body: `{not json`, want: falBillingFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &falBillingTransport{status: tc.status, body: tc.body, err: tc.err}
			gotMicros, got := fetchFalBillingCost(context.Background(), &http.Client{Transport: transport}, "key", "req-1")
			if got != tc.want || gotMicros != tc.wantMicros {
				t.Fatalf("fetchFalBillingCost = (%d, %d), want (%d, %d)", gotMicros, got, tc.wantMicros, tc.want)
			}
			if transport.requests != 1 {
				t.Fatalf("billing requests = %d, want exactly 1 (no retry)", transport.requests)
			}
			// The request's shape, on every row: one keyed lookup of the one
			// request id, with the account's key.
			if transport.path != "/v1/models/billing-events" {
				t.Fatalf("path = %q, want /v1/models/billing-events", transport.path)
			}
			if got := transport.query.Get("request_id"); got != "req-1" {
				t.Fatalf("request_id filter = %q, want \"req-1\"", got)
			}
			if transport.auth != "Key key" {
				t.Fatalf("authorization = %q, want \"Key key\"", transport.auth)
			}
			start, err := time.Parse(time.RFC3339, transport.query.Get("start"))
			if err != nil {
				t.Fatalf("start filter %q is not RFC3339: %v", transport.query.Get("start"), err)
			}
			// The window is widened past fal's 24h default but stays inside
			// the feed's 90-day cap.
			if age := time.Since(start); age < falBillingLookback-time.Hour || age > falBillingLookback+time.Hour {
				t.Fatalf("start filter is %v old, want about %v", age, falBillingLookback)
			}
		})
	}
}

// TestFalReportedCostMicrosNegativeCaches pins when a failed lookup is
// remembered and when the next generation simply asks again. The estimate
// serves every one of these cases — what is being tested is that a standing
// refusal costs nothing per turn, a transient failure retries soon, and a feed
// that merely has not posted yet is never assumed to stay that way.
func TestFalReportedCostMicrosNegativeCaches(t *testing.T) {
	ctx := context.Background()
	const empty = `{"billing_events":[],"next_cursor":null,"has_more":false}`

	t.Run("refusal is remembered for the day", func(t *testing.T) {
		transport := &falBillingTransport{status: http.StatusForbidden}
		cache := newFalPricingCache()
		if _, ok := cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", "req-1"); ok {
			t.Fatal("a refused lookup must not report a cost")
		}
		if _, ok := cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", "req-2"); ok {
			t.Fatal("a lookup inside the denial window must not report a cost")
		}
		if transport.requests != 1 {
			t.Fatalf("billing requests = %d, want 1 (the second call stayed inside the denial window)", transport.requests)
		}
		// Once the window lapses the key is asked again — a preset may have
		// been widened on fal's side.
		cache.billingDeniedAt = time.Now().Add(-falBillingDeniedTTL - time.Minute)
		if _, ok := cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", "req-3"); ok {
			t.Fatal("still refused: must not report a cost")
		}
		if transport.requests != 2 {
			t.Fatalf("billing requests = %d, want 2 after the denial window lapsed", transport.requests)
		}
	})

	t.Run("transient failure is remembered for minutes", func(t *testing.T) {
		transport := &falBillingTransport{status: http.StatusInternalServerError}
		cache := newFalPricingCache()
		cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", "req-1")
		cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", "req-2")
		if transport.requests != 1 {
			t.Fatalf("billing requests = %d, want 1 inside the failure window", transport.requests)
		}
		cache.billingFailedAt = time.Now().Add(-falBillingFailureTTL - time.Second)
		transport.status, transport.body = http.StatusOK, `{"billing_events":[{"request_id":"req-3","cost_estimate_nano_usd":1600000000}]}`
		cost, ok := cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", "req-3")
		if !ok || cost != 1600000 {
			t.Fatalf("reported cost = (%d, %v) after the window lapsed, want (1600000, true)", cost, ok)
		}
		if transport.requests != 2 {
			t.Fatalf("billing requests = %d, want 2 after the failure window lapsed", transport.requests)
		}
	})

	t.Run("a missing event is never cached", func(t *testing.T) {
		transport := &falBillingTransport{body: empty}
		cache := newFalPricingCache()
		for _, id := range []string{"req-1", "req-2", "req-3"} {
			if _, ok := cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", id); ok {
				t.Fatalf("request %s: an empty feed must not report a cost", id)
			}
		}
		if transport.requests != 3 {
			t.Fatalf("billing requests = %d, want 3 (each generation asks once)", transport.requests)
		}
	})

	t.Run("nothing to look up", func(t *testing.T) {
		transport := &falBillingTransport{body: empty}
		cache := newFalPricingCache()
		for _, id := range []string{"", "   "} {
			if _, ok := cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "key", id); ok {
				t.Fatalf("request id %q: must not report a cost", id)
			}
		}
		if _, ok := cache.reportedCostMicros(ctx, &http.Client{Transport: transport}, "  ", "req-1"); ok {
			t.Fatal("a blank key must not report a cost")
		}
		if _, ok := cache.reportedCostMicros(ctx, nil, "key", "req-1"); ok {
			t.Fatal("a nil client must not report a cost")
		}
		if transport.requests != 0 {
			t.Fatalf("billing requests = %d, want 0", transport.requests)
		}
	})
}

// TestEstimateFalGenerationCostPrefersReportedCharge drives the App-level
// entry the gateway calls: fal's own charge for the request wins, the
// estimate serves when the feed has no event, and a call that knows no
// request id never asks the feed at all.
func TestEstimateFalGenerationCostPrefersReportedCharge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	keyring.MockInit()
	if err := saveFalAPIKey("fal-test-key"); err != nil {
		t.Fatalf("saveFalAPIKey: %v", err)
	}
	t.Cleanup(func() { _ = clearFalAPIKey() })

	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	billing := `{"billing_events":[],"next_cursor":null,"has_more":false}`
	billingRequests, pricingRequests := 0, 0
	app := NewApp()
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/v1/models/billing-events"):
			billingRequests++
			return jsonResponse(billing), nil
		case strings.HasPrefix(req.URL.Path, "/v1/models/pricing"):
			pricingRequests++
			return jsonResponse(`{"prices":[` +
				`{"endpoint_id":"minimax/h3-max/reference-to-video","unit_price":0.05,"unit":"seconds","currency":"USD"}` +
				`],"next_cursor":null,"has_more":false}`), nil
		}
		t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		return nil, nil
	})
	const model = "minimax/h3-max/reference-to-video"
	ctx := context.Background()
	// A 10s 1080p turn: the hints the gateway builds, whose estimate is
	// $0.05 × 10 × 3.2 = $1.60.
	turn := falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 3.2}

	// fal's charge is $1.62048 — the documented 10-second, five-square-image
	// 1080p example, whose reference surcharge the estimate cannot see. The
	// report wins, and no pricing quote is even fetched.
	billing = `{"billing_events":[{"request_id":"req-1","cost_total":1.62048,"cost_estimate_nano_usd":1620480000}],"next_cursor":null,"has_more":false}`
	if got := app.estimateFalGenerationCost(ctx, config, model, falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 3.2, RequestID: "req-1"}); got != 1620480 {
		t.Fatalf("cost = %d, want 1620480 from the reported charge", got)
	}
	if pricingRequests != 0 || billingRequests != 1 {
		t.Fatalf("requests = %d pricing / %d billing, want 0 / 1 (the report answered)", pricingRequests, billingRequests)
	}

	// No event posted: the estimate serves — $1.60.
	billing = `{"billing_events":[],"next_cursor":null,"has_more":false}`
	if got := app.estimateFalGenerationCost(ctx, config, model, falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 3.2, RequestID: "req-2"}); got != 1600000 {
		t.Fatalf("cost = %d, want 1600000 from the estimate fallback", got)
	}
	if pricingRequests != 1 || billingRequests != 2 {
		t.Fatalf("requests = %d pricing / %d billing, want 1 / 2", pricingRequests, billingRequests)
	}

	// A call site with no request id (no queue handle) skips the feed.
	if got := app.estimateFalGenerationCost(ctx, config, model, turn); got != 1600000 {
		t.Fatalf("cost = %d, want 1600000 from the estimate", got)
	}
	if billingRequests != 2 {
		t.Fatalf("billing requests = %d, want 2 (no request id, no lookup)", billingRequests)
	}
}

// TestFalVideoTierMultiplier pins the documented tier ratios: the tier the
// request rode over the cheapest one the pricing quote covers. Case-insensitive
// because the h3-max schemas spell the same enum both ways.
func TestFalVideoTierMultiplier(t *testing.T) {
	tests := []struct {
		model      string
		resolution string
		want       float64
	}{
		{"minimax/h3-max/reference-to-video", "480P", 0},
		{"minimax/h3-max/reference-to-video", "768P", 1.6},
		{"minimax/h3-max/reference-to-video", "1080P", 3.2},
		{"minimax/h3-max/reference-to-video", "1080p", 3.2},
		{"minimax/h3-max/reference-to-video", " 1080P ", 3.2},
		{"minimax/h3-max/reference-to-video", "", 0},
		// Not in this endpoint's table: the resolver drops such a value for
		// the model's own default, which the caller prices instead.
		{"minimax/h3-max/reference-to-video", "2K", 0},
		{" minimax/h3-max/reference-to-video", "1080P", 3.2},
		{"minimax/h3-max/image-to-video", "1080P", 3.2},
		{"minimax/h3-max/text-to-video", "768p", 1.6},
		{"minimax/h3-max/extend-video", "1080P", 3.2},
		{"minimax/h3-max/extend-video", "2K", 6.4},
		{"minimax/h3-max/extend-video", "2k", 6.4},
		{"minimax/h3/text-to-video", "768P", 1.2},
		{"minimax/h3/text-to-video", "2K", 2.6},
		{"minimax/h3/reference-to-video", "4K", 3.2},
		{"minimax/h3/image-to-video", "2K", 2.6},
		// No table to scale from. recast and reframe-video publish one, but the
		// pricing API quotes both flat ($0.05 per "units", live 2026-11-24), so
		// there is no per-second base to scale; the rest never stated a table.
		{"minimax/h3-max/reframe-video", "1080p", 0},
		{"minimax/h3-max/recast", "1080P", 0},
		{"fal-ai/kling-video/v3/standard/text-to-video", "1080p", 0},
		{"fal-ai/kling-video/o1/standard/image-to-video", "1080p", 0},
		{"bytedance/seedance-2.5/reference-to-video", "1080p", 0},
		{"minimax/h3-max/reference-to-video-pro", "1080P", 0},
	}
	for _, tc := range tests {
		if got := falVideoTierMultiplier(tc.model, tc.resolution); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("falVideoTierMultiplier(%q, %q) = %v, want %v", tc.model, tc.resolution, got, tc.want)
		}
	}
}

// TestEstimateFalCostMicrosTierMultiplier pins how the tier ratio meets the the
// quote: it scales a per-second price after quantity × unit price, composes
// with the discount multiplier, and leaves everything else alone.
func TestEstimateFalCostMicrosTierMultiplier(t *testing.T) {
	// fal's real spellings, as the live check read them (2026-11-24): the
	// minimax family is quoted per "seconds" in the plural, the flat shape of
	// recast and reframe-video is $0.05 per "units".
	pricing := &falPricingTransport{body: `{"prices":[` +
		`{"endpoint_id":"minimax/h3-max/reference-to-video","unit_price":0.05,"unit":"seconds","currency":"USD"},` +
		`{"endpoint_id":"minimax/h3-max/reference-to-video-singular","unit_price":0.05,"unit":"second","currency":"USD"},` +
		`{"endpoint_id":"minimax/h3-max/reframe-video","unit_price":0.05,"unit":"units","currency":"USD"}` +
		`],"next_cursor":null,"has_more":false}`}
	cache := newFalPricingCache()
	ctx := context.Background()
	client := &http.Client{Transport: pricing}
	tests := []struct {
		name     string
		endpoint string
		hints    falBillingHints
		want     int64
	}{
		{
			name:     "1080p scales the 480p base",
			endpoint: "minimax/h3-max/reference-to-video",
			hints:    falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 3.2},
			want:     1600000,
		},
		{
			// The plural is what fal returns for this family today; the
			// singular is still accepted, since older quotes used it.
			name:     "the singular unit spells the same rate",
			endpoint: "minimax/h3-max/reference-to-video-singular",
			hints:    falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 3.2},
			want:     1600000,
		},
		{
			name:     "the base tier is unscaled",
			endpoint: "minimax/h3-max/reference-to-video",
			hints:    falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 1},
			want:     500000,
		},
		{
			name:     "no table means no scaling",
			endpoint: "minimax/h3-max/reference-to-video",
			hints:    falBillingHints{Requests: 1, Seconds: 10},
			want:     500000,
		},
		{
			name:     "a value under 1 is ignored",
			endpoint: "minimax/h3-max/reference-to-video",
			hints:    falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 0.5},
			want:     500000,
		},
		{
			name:     "a value over the sanity cap is ignored",
			endpoint: "minimax/h3-max/reference-to-video",
			hints:    falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 100},
			want:     500000,
		},
		{
			// recast and reframe-video are really quoted this way: a flat $0.05
			// per "units". The documented tables are per-second rates, so a
			// tier ratio must never touch this quote — it would bill a flat
			// price three times over.
			name:     "a flat quote is never scaled",
			endpoint: "minimax/h3-max/reframe-video",
			hints:    falBillingHints{Requests: 1, TierMultiplier: 3.2},
			want:     50000,
		},
		{
			name:     "the tier composes with the reference discount",
			endpoint: "minimax/h3-max/reference-to-video",
			hints:    falBillingHints{Requests: 1, Seconds: 10, TierMultiplier: 3.2, PriceMultiplier: 0.6},
			want:     960000,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimateFalCostMicros(ctx, cache, client, "key", tc.endpoint, nil, tc.hints); got != tc.want {
				t.Fatalf("estimate = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestFalVideoRequestedSeconds pins the duration a request-duration-billed
// endpoint charges: what the resolver put on the wire, else the model's
// declared default, else nothing (the caller keeps the rendered clip).
func TestFalVideoRequestedSeconds(t *testing.T) {
	plain := &ModelInputSchema{Properties: map[string]SchemaProperty{
		"duration": {Name: "duration", Type: "number", Default: float64(5)},
	}}
	nested := &ModelInputSchema{
		Properties: map[string]SchemaProperty{
			"settings": {Name: "settings", Kind: schemaObject, Nested: map[string]SchemaProperty{
				"duration": {Name: "duration", Type: "number", Default: float64(3)},
			}},
		},
		order: []string{"settings"},
	}
	nodefault := &ModelInputSchema{Properties: map[string]SchemaProperty{
		"duration": {Name: "duration", Type: "number"},
	}}
	tests := []struct {
		name   string
		schema *ModelInputSchema
		body   map[string]any
		want   float64
		ok     bool
	}{
		// The wire value is the billed one: this is post-clamp, so a 20s ask
		// the resolver clamped to the model's 15s maximum reads 15 here.
		{name: "the wire value wins", schema: plain, body: map[string]any{"duration": float64(15)}, want: 15, ok: true},
		{name: "a Go integer on the wire", schema: plain, body: map[string]any{"duration": 10}, want: 10, ok: true},
		{name: "a string-typed dial", schema: plain, body: map[string]any{"duration": "7"}, want: 7, ok: true},
		{name: "a nested dial", schema: nested, body: map[string]any{"settings": map[string]any{"duration": float64(4)}}, want: 4, ok: true},
		{name: "nothing on the wire bills the model default", schema: plain, body: map[string]any{}, want: 5, ok: true},
		{name: "an unreadable wire value falls to the default", schema: plain, body: map[string]any{"duration": "auto"}, want: 5, ok: true},
		{name: "a nil body falls to the default", schema: plain, body: nil, want: 5, ok: true},
		{name: "no wire value and no default is not-ok", schema: nodefault, body: map[string]any{}, ok: false},
		{name: "no schema and no body is not-ok", schema: nil, body: nil, ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := falVideoRequestedSeconds(tc.schema, tc.body, Overrides{}, "minimax/h3-max/reference-to-video")
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("falVideoRequestedSeconds = (%v, %v), want (%v, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestFalBillsRequestedDuration scopes the requested-duration rule to the one
// page that states it; every sibling keeps its rendered clip.
func TestFalBillsRequestedDuration(t *testing.T) {
	for model, want := range map[string]bool{
		"minimax/h3-max/reference-to-video":            true,
		" minimax/h3-max/reference-to-video":           true,
		"minimax/h3-max/image-to-video":                false,
		"minimax/h3-max/extend-video":                  false,
		"minimax/h3-max/text-to-video":                 false,
		"minimax/h3/reference-to-video":                false,
		"fal-ai/kling-video/v3/standard/text-to-video": false,
	} {
		if got := falBillsRequestedDuration(model); got != want {
			t.Fatalf("falBillsRequestedDuration(%q) = %v, want %v", model, got, want)
		}
	}
}

// TestGatewayGenerateVideoPricesTieredReferenceToVideo is the regression shape
// of conv_3e8e49c2839590e100f5128c: a minimax/h3-max/reference-to-video turn
// the planner sent with neither duration nor resolution, so the configured
// defaults rode — 10s at 1080p — and the model rendered a 10.144s container.
// fal bills that turn $0.16/s × the REQUESTED 10s = $1.60; the ledger recorded
// $0.5072, the pricing API's 480p base rate × the rendered container, because
// the quote is a base price that billing scales by resolution. The estimate
// now rides the tier the request actually sent and the duration it asked for,
// and fal's own charge replaces it as soon as billing has posted the event.
func TestGatewayGenerateVideoPricesTieredReferenceToVideo(t *testing.T) {
	// Minimal h3-max reference-to-video-shaped input schema: the dials the
	// resolver maps (duration, resolution with fal's uppercase enum, the
	// scalar start-frame image) plus the required prompt expansion switch.
	const schema = `{"components":{"schemas":{"H3MaxReferenceToVideoInput":{"type":"object","required":["prompt","prompt_expansion_mode"],` +
		`"properties":{"prompt":{"type":"string","minLength":1},` +
		`"prompt_expansion_mode":{"type":"string","default":"balanced"},` +
		`"duration":{"type":"number","minimum":0.92,"maximum":15,"default":5},` +
		`"resolution":{"type":"string","enum":["480P","768P","1080P"],"default":"768P"},` +
		`"aspect_ratio":{"type":"string","enum":["adaptive","21:9","16:9","4:3","1:1","3:4","9:16"],"default":"adaptive"},` +
		`"image_url":{"anyOf":[{"type":"string","minLength":1},{"type":"null"}]},` +
		`"reference_image_urls":{"type":"array","items":{"type":"string"},"maxItems":9}}}}}}`
	tests := []struct {
		name       string
		resolution string
		billing    string
		want       int64
	}{
		{
			// The reported turn: 10s × $0.16 — fal's dashboard charge to the cent.
			name:       "1080p estimate",
			resolution: "1080p",
			billing:    `{"billing_events":[],"next_cursor":null,"has_more":false}`,
			want:       1600000,
		},
		{
			name:       "768p estimate",
			resolution: "768p",
			billing:    `{"billing_events":[],"next_cursor":null,"has_more":false}`,
			want:       800000,
		},
		{
			name:       "480p is the base tier",
			resolution: "480p",
			billing:    `{"billing_events":[],"next_cursor":null,"has_more":false}`,
			want:       500000,
		},
		{
			// No tier on the call: the resolver sends none and fal applies the
			// schema's 768P default, which is what must be priced.
			name:       "no tier on the call bills the model default",
			resolution: "",
			billing:    `{"billing_events":[],"next_cursor":null,"has_more":false}`,
			want:       800000,
		},
		{
			// Billing has posted: fal's charge replaces the estimate. This is
			// fal's documented ten-second, five-square-image 1080p example —
			// the fifth image's reference surcharge is exactly what no
			// estimate from the request's parameters can see.
			name:       "the reported charge wins",
			resolution: "1080p",
			billing:    `{"billing_events":[{"request_id":"req-tier-1","quantity":10,"unit":"second","unit_price":0.16,"cost_total":1.62048,"cost_estimate_nano_usd":1620480000}],"next_cursor":null,"has_more":false}`,
			want:       1620480,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			keyring.MockInit()
			if err := saveFalAPIKey("fal-test-key"); err != nil {
				t.Fatalf("saveFalAPIKey: %v", err)
			}
			t.Cleanup(func() { _ = clearFalAPIKey() })

			config := defaultAppConfig()
			config.Storage = ConfigStorage{
				Root:      filepath.Join(home, ".atelier"),
				History:   filepath.Join(home, ".atelier", "history"),
				Artifacts: filepath.Join(home, ".atelier", "history"),
			}
			if err := writeAppConfig(config); err != nil {
				t.Fatalf("writeAppConfig: %v", err)
			}

			var submitted map[string]any
			var billingQuery url.Values
			app := NewApp()
			app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasPrefix(req.URL.Path, "/v1/models/billing-events"):
					billingQuery = req.URL.Query()
					return jsonResponse(tc.billing), nil
				case strings.HasPrefix(req.URL.Path, "/v1/models/pricing"):
					return jsonResponse(`{"prices":[` +
						`{"endpoint_id":"minimax/h3-max/reference-to-video","unit_price":0.05,"unit":"seconds","currency":"USD"}` +
						`],"next_cursor":null,"has_more":false}`), nil
				case strings.Contains(req.URL.Path, "/api/openapi/"):
					return jsonResponse(schema), nil
				}
				if strings.Contains(req.URL.Host, "fal.run") {
					switch {
					case req.Method == http.MethodPost:
						if err := json.NewDecoder(req.Body).Decode(&submitted); err != nil {
							t.Fatalf("decode submit body: %v", err)
						}
						return jsonResponse(`{"request_id":"req-tier-1"}`), nil
					case strings.HasSuffix(req.URL.Path, "/status"):
						return jsonResponse(`{"status":"COMPLETED"}`), nil
					case strings.HasSuffix(req.URL.Path, "/requests/req-tier-1"):
						return jsonResponse(`{"video":{"url":"https://queue.fal.run/dl/tier-video.mp4","content_type":"video/mp4"}}`), nil
					default:
						// The clip download: the turn's real 10.144s container
						// — longer than the 10s fal billed.
						return mp4Resp(mediasnifftest.MP4WithDuration(0, 1000, 10144), "video/mp4"), nil
					}
				}
				t.Fatalf("unexpected request %s %s", req.Method, req.URL)
				return nil, nil
			})

			gateway := newToolGateway(app, config)
			generated, err := gateway.tools.GenerateVideo(context.Background(), VideoGenerateRequest{
				// What the executor hands the gateway once the configured
				// defaults fill a call that named neither: 10s, the tier.
				Model:       "minimax/h3-max/reference-to-video",
				Prompt:      "A 9-second continuous Pixar-style 3D underwater shot in the bright Central Plaza.",
				Duration:    "10",
				Resolution:  tc.resolution,
				AspectRatio: "16:9",
				Images:      []string{tinyPNG},
			})
			if err != nil {
				t.Fatalf("GenerateVideo: %v", err)
			}
			if generated.CostMicros != tc.want {
				t.Fatalf("CostMicros = %d, want %d", generated.CostMicros, tc.want)
			}
			// The estimate keys off what was actually sent, so pin the wire:
			// the duration, and the tier in fal's own enum spelling (or none).
			if got := submitted["duration"]; got != float64(10) {
				t.Fatalf("submitted duration = %#v, want the number 10", got)
			}
			wantTier := "1080P"
			switch tc.resolution {
			case "768p":
				wantTier = "768P"
			case "480p":
				wantTier = "480P"
			case "":
				wantTier = ""
			}
			if got, _ := submitted["resolution"].(string); got != wantTier {
				t.Fatalf("submitted resolution = %q, want %q", got, wantTier)
			}
			// One lookup, keyed on the request the queue returned.
			if got := billingQuery.Get("request_id"); got != "req-tier-1" {
				t.Fatalf("billing lookup request_id = %q, want \"req-tier-1\"", got)
			}
		})
	}
}
