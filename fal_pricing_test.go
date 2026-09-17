package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
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
	// fal's compute-time and per-minute vocabulary stays unpriced: the request
	// can never state GPU seconds, and a lipsync clip's minutes ride the
	// driving audio the gateway doesn't probe.
	for _, unit := range []string{"compute seconds", "minutes"} {
		if _, ok := (falBillingHints{Requests: 1, Seconds: 30}).quantityForUnit(unit); ok {
			t.Fatalf("quantityForUnit(%q) should report not-ok", unit)
		}
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

func TestMp4DurationSeconds(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want float64
	}{
		{name: "v0 mvhd", data: mp4FixtureWithDuration(0, 1000, 10500), want: 10.5},
		{name: "v1 mvhd", data: mp4FixtureWithDuration(1, 24000, 240000), want: 10},
		{name: "no moov", data: tinyMP4(), want: 0},
		{name: "not mp4", data: []byte("not a video at all"), want: 0},
		{name: "empty", data: nil, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mp4DurationSeconds(tt.data)
			if tt.want > 0 {
				if !ok || got != tt.want {
					t.Fatalf("mp4DurationSeconds = (%v, %v), want (%v, true)", got, ok, tt.want)
				}
			} else if ok {
				t.Fatalf("mp4DurationSeconds should fail-soft, got (%v, true)", got)
			}
		})
	}
}

// mp4FixtureWithDuration builds a minimal MP4 byte sequence — ftyp + moov +
// mvhd — carrying the given timescale and duration so the duration probe has
// a real container to parse. version selects mvhd box version 0 or 1. The
// header layout follows ISO BMFF: version+flags, then creation+modification
// (4+4 bytes in v0, 8+8 in v1), then timescale, then duration — no reserved
// fields between them.
func mp4FixtureWithDuration(version byte, timescale, duration uint32) []byte {
	var mvhd []byte
	mvhd = append(mvhd, version, 0, 0, 0) // version + flags
	if version == 1 {
		mvhd = append(mvhd, make([]byte, 16)...) // creation + modification (64-bit)
		mvhd = binary.BigEndian.AppendUint32(mvhd, timescale)
		mvhd = binary.BigEndian.AppendUint64(mvhd, uint64(duration))
	} else {
		mvhd = append(mvhd, make([]byte, 8)...) // creation + modification (32-bit)
		mvhd = binary.BigEndian.AppendUint32(mvhd, timescale)
		mvhd = binary.BigEndian.AppendUint32(mvhd, duration)
	}
	// rate + volume + matrix + predefined + next track id pad the payload to
	// the classic 0x6c-byte box; the parser only reads the header fields, but
	// a full-size box keeps the fixture shaped like real output.
	if pad := 0x64 - len(mvhd); pad > 0 {
		mvhd = append(mvhd, make([]byte, pad)...)
	}
	mvhdBox := append(binary.BigEndian.AppendUint32(nil, uint32(len(mvhd)+8)), []byte("mvhd")...)
	mvhdBox = append(mvhdBox, mvhd...)
	moovBox := append(binary.BigEndian.AppendUint32(nil, uint32(len(mvhdBox)+8)), []byte("moov")...)
	moovBox = append(moovBox, mvhdBox...)
	out := tinyMP4()
	return append(out, moovBox...)
}

// mp4TestBox builds one length-prefixed ISO BMFF box around a payload.
func mp4TestBox(boxType string, payload []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(payload)+8))
	out = append(out, boxType...)
	return append(out, payload...)
}

// mp4FixtureWithVideoTrack builds ftyp + moov{video trak, audio trak}. The
// video track's tkhd carries the 16.16 fixed-point presentation size and its
// stbl one stts entry per sampleCounts element (their sum is the frame
// count); the trailing audio trak proves the probes pick the 'vide' handler.
// A call with no sampleCounts omits the sample table entirely — the
// frame-count-unparseable shape.
func mp4FixtureWithVideoTrack(width, height uint32, sampleCounts ...uint32) []byte {
	tkhd := make([]byte, 84) // v0 layout; width/height close the box as 16.16
	binary.BigEndian.PutUint32(tkhd[76:80], width<<16)
	binary.BigEndian.PutUint32(tkhd[80:84], height<<16)

	videoHdlr := make([]byte, 24)
	copy(videoHdlr[8:12], "vide")

	mdiaPayload := mp4TestBox("hdlr", videoHdlr)
	if len(sampleCounts) > 0 {
		stts := make([]byte, 8, 8+len(sampleCounts)*8)
		binary.BigEndian.PutUint32(stts[4:8], uint32(len(sampleCounts)))
		for _, count := range sampleCounts {
			stts = binary.BigEndian.AppendUint32(stts, count)
			stts = binary.BigEndian.AppendUint32(stts, 1) // sample_delta
		}
		mdiaPayload = append(mdiaPayload,
			mp4TestBox("minf", mp4TestBox("stbl", mp4TestBox("stts", stts)))...)
	}
	videoTrak := mp4TestBox("trak", append(mp4TestBox("tkhd", tkhd), mp4TestBox("mdia", mdiaPayload)...))

	audioHdlr := make([]byte, 24)
	copy(audioHdlr[8:12], "soun")
	audioTrak := mp4TestBox("trak", mp4TestBox("mdia", mp4TestBox("hdlr", audioHdlr)))

	moov := mp4TestBox("moov", append(videoTrak, audioTrak...))
	return append(tinyMP4(), moov...)
}

// mp4FixtureAudioOnly builds ftyp + moov{trak} with no video track — the
// shape every video probe must refuse.
func mp4FixtureAudioOnly() []byte {
	audioHdlr := make([]byte, 24)
	copy(audioHdlr[8:12], "soun")
	moov := mp4TestBox("moov", mp4TestBox("trak", mp4TestBox("mdia", mp4TestBox("hdlr", audioHdlr))))
	return append(tinyMP4(), moov...)
}

// mp4FixtureVideoOnly builds ftyp + moov{video trak} with no audio track —
// the silent-clip shape the portion-speed audio sniff must recognize.
func mp4FixtureVideoOnly() []byte {
	tkhd := make([]byte, 84)
	binary.BigEndian.PutUint32(tkhd[76:80], 1344<<16)
	binary.BigEndian.PutUint32(tkhd[80:84], 768<<16)
	videoHdlr := make([]byte, 24)
	copy(videoHdlr[8:12], "vide")
	videoTrak := mp4TestBox("trak", append(mp4TestBox("tkhd", tkhd), mp4TestBox("mdia", mp4TestBox("hdlr", videoHdlr))...))
	return append(tinyMP4(), mp4TestBox("moov", videoTrak)...)
}

func TestMp4HasAudioTrack(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want bool
	}{
		{name: "video and audio tracks", data: mp4FixtureWithVideoTrack(1344, 768, 339), want: true},
		{name: "audio only", data: mp4FixtureAudioOnly(), want: true},
		{name: "video only", data: mp4FixtureVideoOnly(), want: false},
		{name: "no trak at all", data: mp4FixtureWithDuration(0, 1000, 10500), want: false},
		{name: "no moov", data: tinyMP4(), want: false},
		{name: "not mp4", data: []byte("not a video at all"), want: false},
		{name: "empty", data: nil, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mp4HasAudioTrack(tt.data); got != tt.want {
				t.Fatalf("mp4HasAudioTrack = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMp4VideoDimensions(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   []byte
		wantW  float64
		wantH  float64
		wantOK bool
	}{
		{name: "ltx extend output", data: mp4FixtureWithVideoTrack(1344, 768, 339), wantW: 1344, wantH: 768, wantOK: true},
		{name: "portrait", data: mp4FixtureWithVideoTrack(720, 1280), wantW: 720, wantH: 1280, wantOK: true},
		{name: "audio only", data: mp4FixtureAudioOnly(), wantOK: false},
		{name: "no moov", data: tinyMP4(), wantOK: false},
		{name: "not mp4", data: []byte("not a video at all"), wantOK: false},
		{name: "empty", data: nil, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, ok := mp4VideoDimensions(tt.data)
			if ok != tt.wantOK || (tt.wantOK && (w != tt.wantW || h != tt.wantH)) {
				t.Fatalf("mp4VideoDimensions = (%v, %v, %v), want (%v, %v, %v)", w, h, ok, tt.wantW, tt.wantH, tt.wantOK)
			}
		})
	}
}

func TestMp4VideoFrameCount(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   []byte
		want   float64
		wantOK bool
	}{
		{name: "single run", data: mp4FixtureWithVideoTrack(1344, 768, 339), want: 339, wantOK: true},
		{name: "runs sum", data: mp4FixtureWithVideoTrack(1344, 768, 243, 96), want: 339, wantOK: true},
		{name: "no sample table", data: mp4FixtureWithVideoTrack(1344, 768), wantOK: false},
		{name: "audio only", data: mp4FixtureAudioOnly(), wantOK: false},
		{name: "not mp4", data: []byte("not a video at all"), wantOK: false},
		{name: "empty", data: nil, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mp4VideoFrameCount(tt.data)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("mp4VideoFrameCount = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
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
	if got := falVideoTokenEstimate(schema, req, 10); got != 216000 {
		t.Fatalf("token estimate = %v, want 216000", got)
	}
	// Video inputs add their own unprobed duration to fal's formula — no
	// estimate rather than half the bill.
	reqWithVideo := req
	reqWithVideo.Videos = []string{"data:video/mp4;base64,AAAA"}
	if got := falVideoTokenEstimate(schema, reqWithVideo, 10); got != 0 {
		t.Fatalf("video-input turn should estimate 0 tokens, got %v", got)
	}
	// Unknown rendered duration ("auto" and no parseable container): skip.
	if got := falVideoTokenEstimate(schema, req, 0); got != 0 {
		t.Fatalf("unknown duration should estimate 0 tokens, got %v", got)
	}
	// A resolution tier the model's enum rejects bills at the model default.
	rejected := req
	rejected.Resolution = "2160p"
	if got := falVideoTokenEstimate(schema, rejected, 10); got != 216000 {
		t.Fatalf("enum-rejected tier should fall to 720p default (216000 tokens), got %v", got)
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
	if got := falVideoBilledMegapixels(schema, extend, mp4FixtureWithVideoTrack(1344, 768, 339)); got != 124.895232 {
		t.Fatalf("extend megapixels = %v, want 124.895232 (1344×768×121/1e6)", got)
	}
	// Pure generation: every frame in the container was rendered by the model,
	// so the stts count is the billed quantity.
	pure := VideoGenerateRequest{Prompt: "tornado"}
	if got := falVideoBilledMegapixels(schema, pure, mp4FixtureWithVideoTrack(1344, 768, 339)); got != 349.913088 {
		t.Fatalf("pure megapixels = %v, want 349.913088 (1344×768×339/1e6)", got)
	}
	// Pure generation with no parseable frame table falls back to the schema's
	// num_frames default.
	if got := falVideoBilledMegapixels(schema, pure, mp4FixtureWithVideoTrack(1280, 720)); got != 111.5136 {
		t.Fatalf("fallback megapixels = %v, want 111.5136 (1280×720×121/1e6)", got)
	}
	// Extend with no num_frames default on the schema: not computable, and a
	// zero quantity skips the estimate rather than guessing.
	if got := falVideoBilledMegapixels(&ModelInputSchema{}, extend, mp4FixtureWithVideoTrack(1344, 768, 339)); got != 0 {
		t.Fatalf("extend with no num_frames default should estimate 0, got %v", got)
	}
	// No frame size at all: not computable.
	if got := falVideoBilledMegapixels(schema, extend, mp4FixtureAudioOnly()); got != 0 {
		t.Fatalf("audio-only output should estimate 0, got %v", got)
	}
	if got := falVideoBilledMegapixels(schema, extend, nil); got != 0 {
		t.Fatalf("empty output should estimate 0, got %v", got)
	}
}

// testImageBytes encodes a zero-filled image of the given size in one of the
// formats fal returns for image generation.
func testImageBytes(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	default:
		t.Fatalf("unknown fixture format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s fixture: %v", format, err)
	}
	return buf.Bytes()
}

func TestImagePixelDimensions(t *testing.T) {
	for _, tt := range []struct {
		name   string
		data   []byte
		wantW  float64
		wantH  float64
		wantOK bool
	}{
		{name: "png", data: testImageBytes(t, "png", 64, 48), wantW: 64, wantH: 48, wantOK: true},
		{name: "jpeg", data: testImageBytes(t, "jpeg", 64, 48), wantW: 64, wantH: 48, wantOK: true},
		{name: "gif", data: testImageBytes(t, "gif", 64, 48), wantW: 64, wantH: 48, wantOK: true},
		{name: "not an image", data: []byte("plain text"), wantOK: false},
		{name: "empty", data: nil, wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, ok := imagePixelDimensions(tt.data)
			if ok != tt.wantOK || (tt.wantOK && (w != tt.wantW || h != tt.wantH)) {
				t.Fatalf("imagePixelDimensions = (%v, %v, %v), want (%v, %v, %v)", w, h, ok, tt.wantW, tt.wantH, tt.wantOK)
			}
		})
	}
	// WebP's three chunk layouts, hand-built to the container spec (no stdlib
	// encoder): VP8X carries the canvas size, VP8/VP8L their own dimensions.
	vp8x := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00"),
		byte(63), 0, 0, // width-1 = 63 → 64
		byte(47), 0, 0) // height-1 = 47 → 48
	if w, h, ok := imagePixelDimensions(vp8x); !ok || w != 64 || h != 48 {
		t.Fatalf("VP8X dimensions = (%v, %v, %v), want (64, 48, true)", w, h, ok)
	}
	vp8 := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 \x0a\x00\x00\x00\x00\x00\x00\x9d\x01\x2a"),
		byte(64), 0, // width = 64
		byte(48), 0) // height = 48
	if w, h, ok := imagePixelDimensions(vp8); !ok || w != 64 || h != 48 {
		t.Fatalf("VP8 dimensions = (%v, %v, %v), want (64, 48, true)", w, h, ok)
	}
	// VP8L packs (width-1)|(height-1)<<14 into 32 bits after its 0x2f marker.
	// Padded past the 30-byte webp guard — real files carry much more.
	vp8l := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x05\x00\x00\x00\x2f"),
		append(binary.LittleEndian.AppendUint32(nil, 63|47<<14), make([]byte, 5)...)...)
	if w, h, ok := imagePixelDimensions(vp8l); !ok || w != 64 || h != 48 {
		t.Fatalf("VP8L dimensions = (%v, %v, %v), want (64, 48, true)", w, h, ok)
	}
}

func TestImageResultMegapixels(t *testing.T) {
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testImageBytes(t, "png", 64, 48))
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

// falPricingTransport serves one pricing payload and counts requests.
type falPricingTransport struct {
	requests  int
	lastQuery string
	lastAuth  string
	body      string
	status    int
}

func (transport *falPricingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.requests++
	transport.lastQuery = req.URL.RawQuery
	transport.lastAuth = req.Header.Get("Authorization")
	body := transport.body
	if body == "" {
		body = `{"prices":[` +
			`{"endpoint_id":"fal-ai/flux/schnell","unit_price":0.003,"unit":"image","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/kling-video/v2/master/text-to-video","unit_price":0.07,"unit":"second","currency":"USD"},` +
			`{"endpoint_id":"bytedance/seedance-2.0/reference-to-video","unit_price":0.014,"unit":"1000 tokens","currency":"USD"},` +
			`{"endpoint_id":"bytedance/seedream/v5/pro/edit","unit_price":0.0675,"unit":"units","currency":"USD"},` +
			`{"endpoint_id":"fal-ai/f5-tts","unit_price":0.05,"unit":"1000 characters","currency":"USD"},` +
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
	if transport.requests != 1 {
		t.Fatalf("pricing requests = %d, want 1", transport.requests)
	}
	// Within the negative window no retry: an offline machine must not hit
	// the pricing endpoint on every media turn.
	if _, ok := cache.quote(ctx, &http.Client{Transport: transport}, "key", "fal-ai/flux/schnell", nil); ok {
		t.Fatal("negative-cached quote should miss")
	}
	if transport.requests != 1 {
		t.Fatalf("pricing requests = %d within negative TTL, want 1", transport.requests)
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
	if got, ok := falVideoBilledSeconds(mp4FixtureWithDuration(0, 1000, 8000), "5"); !ok || got != 8 {
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
				return mp4Resp(mp4FixtureWithDuration(0, 1000, 10000), "video/mp4"), nil
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
		case strings.HasPrefix(req.URL.Path, "/v1/models/pricing"):
			return jsonResponse(`{"prices":[` +
				`{"endpoint_id":"fal-ai/ltx-2.3-22b/extend-video","unit_price":0.001605,"unit":"megapixel","currency":"USD"}` +
				`],"next_cursor":null,"has_more":false}`), nil
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
				return mp4Resp(mp4FixtureWithVideoTrack(1344, 768, 339), "video/mp4"), nil
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
