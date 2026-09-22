package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fal media generation has no token meter — fal bills per output unit (an
// image, a second of video, a character of speech, one request, a block of
// video tokens, or a megapixel of generated pixels, depending on the model). Cost is therefore estimated at call
// time from fal's pricing API: GET {falPlatformBaseURL}/v1/models/pricing?endpoint_id=a,b,c returns
// each endpoint's unit price and billing unit, and the tool gateway multiplies
// by the quantity it knows from the request (image count, requested or
// rendered seconds, prompt length, or rendered pixels — width × height ×
// frames for video). This is an estimate, not a bill — feature multipliers fal
// applies server-side (Kling's audio toggle, resolution tiers) may not be
// captured — but it is the only fal source that attributes cost to a specific
// generation: the usage/line-items API aggregates per time bucket with no
// request IDs, so it cannot answer "what did this call cost". Token- and
// megapixel-billed video models (seedance-2.x per "1000 tokens", the
// ltx-2.3-22b family per "megapixel") report no usage in the generation
// response; their quantity is computed from fal's documented formulas over the
// request's actual parameters (see falVideoTokenEstimate and
// falVideoBilledMegapixels).
//
// Everything here is fail-soft by design: no key, no network, an unknown
// billing unit, or an unresolvable quantity yields 0 (no cost shown), never a
// failed or delayed generation.

const (
	// falPricingTTL is how long fetched quotes stay fresh. fal prices move on
	// a weeks scale; a day-stale price is fine for a spend display and keeps
	// the pricing endpoint off the per-turn path (mirrors the updater's 24h
	// throttle).
	falPricingTTL = 24 * time.Hour
	// falPricingFailureTTL is the negative cache window: a failed fetch is
	// remembered briefly so an offline machine does not retry the pricing
	// endpoint on every media turn, while a recovered network is picked up
	// within minutes. Only successful fetches refresh falPricingTTL.
	falPricingFailureTTL = 10 * time.Minute
	// falPricingMaxIDs is the endpoint-ID batch ceiling the pricing API
	// accepts per request (its own documented limit).
	falPricingMaxIDs = 50
	// falPricingMaxPages bounds cursor pagination on the pricing response;
	// one batch of configured models fits a single page in practice.
	falPricingMaxPages = 3
)

// falPriceQuote is one endpoint's unit pricing.
type falPriceQuote struct {
	EndpointID string
	// UnitPrice is USD per billing unit (pre-discount; fal's docs note custom
	// account pricing may be reflected here).
	UnitPrice float64
	// Unit names what a unit is: "image", "second", "video", "request",
	// "character", ... — the vocabulary fal's API returns, not a fixed enum.
	Unit string
}

// falPricingCache memoizes pricing quotes process-wide. One instance hangs
// off App (created in NewApp) so every gateway rebuild shares it — a
// per-gateway cache would refetch once per turn.
type falPricingCache struct {
	mu        sync.Mutex
	quotes    map[string]falPriceQuote
	fetchedAt time.Time
	failedAt  time.Time
}

func newFalPricingCache() *falPricingCache {
	return &falPricingCache{quotes: make(map[string]falPriceQuote)}
}

// falPricingResponse mirrors the pricing API payload. Only USD quotes are
// kept — CostMicros is denominated in USD millionths everywhere downstream.
type falPricingResponse struct {
	Prices []struct {
		EndpointID string  `json:"endpoint_id"`
		UnitPrice  float64 `json:"unit_price"`
		Unit       string  `json:"unit"`
		Currency   string  `json:"currency"`
	} `json:"prices"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// quote returns the cached pricing for endpointID, fetching when the cache is
// stale. allIDs is the batch to fetch (the full configured model set plus
// anything else already requested this call) so one refresh covers every
// endpoint the app could call — one pricing GET per TTL window, not per
// model. The mutex is held across the fetch: harness tool calls run
// sequentially within a turn, so this serializes at most the first media tool
// call after launch or a Settings model change.
func (cache *falPricingCache) quote(ctx context.Context, httpClient *http.Client, apiKey, endpointID string, allIDs []string) (falPriceQuote, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := time.Now()
	if quote, ok := cache.quotes[endpointID]; ok && now.Sub(cache.fetchedAt) < falPricingTTL {
		return quote, true
	}
	if now.Sub(cache.failedAt) < falPricingFailureTTL && len(cache.quotes) == 0 {
		return falPriceQuote{}, false
	}
	// The requested endpoint always rides the batch even when the caller
	// passed no wider configured set, so a cold cache can still price a
	// single call.
	if err := cache.fetch(ctx, httpClient, apiKey, append(append([]string{}, allIDs...), endpointID)); err != nil {
		cache.failedAt = now
		return falPriceQuote{}, false
	}
	quote, ok := cache.quotes[endpointID]
	return quote, ok
}

// fetch populates the cache from one batched pricing request (plus cursor
// pages). Caller holds the mutex.
func (cache *falPricingCache) fetch(ctx context.Context, httpClient *http.Client, apiKey string, endpointIDs []string) error {
	ids := make([]string, 0, len(endpointIDs))
	seen := make(map[string]bool, len(endpointIDs))
	for _, id := range endpointIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return fmt.Errorf("no endpoint ids to price")
	}
	if len(ids) > falPricingMaxIDs {
		ids = ids[:falPricingMaxIDs]
	}

	quotes := make(map[string]falPriceQuote)
	cursor := ""
	for page := 0; page < falPricingMaxPages; page++ {
		query := url.Values{}
		query.Set("endpoint_id", strings.Join(ids, ","))
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, falPlatformBaseURL+"/v1/models/pricing?"+query.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Key "+apiKey)
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		var payload falPricingResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("fal pricing api returned %s", resp.Status)
		}
		if decodeErr != nil {
			return decodeErr
		}
		for _, price := range payload.Prices {
			if !strings.EqualFold(strings.TrimSpace(price.Currency), "USD") {
				continue
			}
			quotes[price.EndpointID] = falPriceQuote{
				EndpointID: price.EndpointID,
				UnitPrice:  price.UnitPrice,
				Unit:       strings.ToLower(strings.TrimSpace(price.Unit)),
			}
		}
		if !payload.HasMore || payload.NextCursor == nil || *payload.NextCursor == "" {
			break
		}
		cursor = *payload.NextCursor
	}
	// A successful fetch replaces the table wholesale: prices change, and a
	// removed endpoint must not linger at its old rate. fetchedAt only moves
	// on success (the updater-state pattern), so transient failures retry.
	cache.quotes = quotes
	cache.fetchedAt = time.Now()
	cache.failedAt = time.Time{}
	return nil
}

// falBillingHints carries the quantity inputs a call site knows, before the
// billing unit is known — the pricing quote's unit decides which one applies.
// Zero values mean "not computable here" (e.g. a per-second upscaler whose
// source duration was never probed), and quantityForUnit reports false so the
// cost is skipped rather than guessed.
type falBillingHints struct {
	Images     int
	Seconds    float64
	Characters int
	Requests   int
	// Tokens is the computed token count for a token-billed model (fal's newer
	// video families — seedance 2.x prices per "1000 tokens"). fal does not
	// report usage in the generation response, so the count comes from fal's
	// documented token formula over the request's actual parameters (see
	// falVideoTokenEstimate).
	Tokens float64
	// Megapixels is the generated pixel budget for an MP-billed model — fal's
	// ltx-2.3-22b video family prices per "megapixel of generated video data"
	// (width × height × frames), and some image models (flux essenza) per
	// megapixel of output. Computed from what was actually rendered (see
	// falVideoBilledMegapixels / imageResultMegapixels).
	Megapixels float64
}

// quantityForUnit maps a billing unit onto the hint that prices it.
func (hints falBillingHints) quantityForUnit(unit string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "image":
		return float64(hints.Images), hints.Images > 0
	case "second", "seconds":
		return hints.Seconds, hints.Seconds > 0
	case "character", "characters":
		return float64(hints.Characters), hints.Characters > 0
	case "video", "request":
		// "video" is the flat per-clip unit; "request" the flat per-call one.
		// Both price as one unit per generation.
		return float64(hints.Requests), hints.Requests > 0
	case "audio", "audios":
		// fal's flat per-generated-audio unit (stable-audio inpaint returns
		// "audios"): one per generation, like "request".
		return float64(hints.Requests), hints.Requests > 0
	case "unit", "units":
		// fal's generic per-output unit — seedream v5 prices "units" per
		// generated image, gemini-omni edit per clip (conv_26ef0c4f billed a
		// seedream edit with no dollar row until this unit was taught). Only
		// image-generating calls carry an image count; every other call
		// produced exactly one output.
		if hints.Images > 0 {
			return float64(hints.Images), true
		}
		return float64(hints.Requests), hints.Requests > 0
	default:
		// Token-billed models: the unit names how many tokens one billed unit
		// covers ("1000 tokens" is what fal's pricing API returns for
		// seedance-2.x). The quantity is the token count scaled to that unit.
		if multiplier, isTokens := falTokenUnitMultiplier(unit); isTokens {
			return hints.Tokens / multiplier, hints.Tokens > 0
		}
		// Character-billed TTS models name their block the same way ("1000
		// characters" for elevenlabs v3 and f5-tts): the spoken text's length
		// scaled to that block.
		if multiplier, isCharacters := falCharacterUnitMultiplier(unit); isCharacters {
			return float64(hints.Characters) / multiplier, hints.Characters > 0
		}
		// Megapixel-billed models: the unit names how many megapixels one
		// billed unit covers ("megapixel" in the singular is what fal's pricing
		// API returns for the ltx-2.3-22b family and MP-priced image models).
		if multiplier, isMegapixels := falMegapixelUnitMultiplier(unit); isMegapixels {
			return hints.Megapixels / multiplier, hints.Megapixels > 0
		}
		// Anything else — "compute seconds" (wizper, esrgan: billed GPU time
		// the request can never state), "minutes" (sync-lipsync: output length
		// ≈ the driving audio's, which the gateway doesn't know) — stays
		// unpriced rather than guessed onto a nearby hint.
		return 0, false
	}
}

// falTokenUnitMultiplier parses fal's token billing units — "token",
// "tokens", "1000 tokens", "1,000 tokens" — returning how many tokens one
// billed unit covers (1 for the bare forms). ok is false for any other unit.
func falTokenUnitMultiplier(unit string) (float64, bool) {
	normalized := strings.ToLower(strings.TrimSpace(unit))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, ",", "")
	plural := strings.TrimSuffix(normalized, "tokens")
	if plural == normalized {
		plural = strings.TrimSuffix(normalized, "token")
		if plural == normalized {
			return 0, false
		}
	}
	if plural == "" {
		return 1, true
	}
	multiplier, err := strconv.ParseFloat(plural, 64)
	if err != nil || multiplier <= 0 {
		return 0, false
	}
	return multiplier, true
}

// falCharacterUnitMultiplier parses fal's character billing units —
// "character", "characters", "1000 characters", "1,000 characters" — returning
// how many characters one billed unit covers (1 for the bare forms). ok is
// false for any other unit.
func falCharacterUnitMultiplier(unit string) (float64, bool) {
	normalized := strings.ToLower(strings.TrimSpace(unit))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, ",", "")
	stem := normalized
	plural := strings.TrimSuffix(stem, "characters")
	if plural == stem {
		plural = strings.TrimSuffix(stem, "character")
		if plural == stem {
			return 0, false
		}
	}
	if plural == "" {
		return 1, true
	}
	multiplier, err := strconv.ParseFloat(plural, 64)
	if err != nil || multiplier <= 0 {
		return 0, false
	}
	return multiplier, true
}

// falMegapixelUnitMultiplier parses fal's megapixel billing units —
// "megapixel", "megapixels", "1 megapixel" — returning how many megapixels one
// billed unit covers (1 for the bare forms). ok is false for any other unit.
func falMegapixelUnitMultiplier(unit string) (float64, bool) {
	normalized := strings.ToLower(strings.TrimSpace(unit))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, ",", "")
	stem := normalized
	plural := strings.TrimSuffix(stem, "megapixels")
	if plural == stem {
		plural = strings.TrimSuffix(stem, "megapixel")
		if plural == stem {
			return 0, false
		}
	}
	if plural == "" {
		return 1, true
	}
	multiplier, err := strconv.ParseFloat(plural, 64)
	if err != nil || multiplier <= 0 {
		return 0, false
	}
	return multiplier, true
}

// estimateFalCostMicros prices one fal generation in USD millionths. Fail-soft
// in every direction: missing cache/client/key, unavailable pricing, or an
// unknown billing unit returns 0 so telemetry shows no cost rather than
// blocking or failing the generation.
func estimateFalCostMicros(ctx context.Context, cache *falPricingCache, httpClient *http.Client, apiKey, endpointID string, allIDs []string, hints falBillingHints) int64 {
	if cache == nil || httpClient == nil || strings.TrimSpace(apiKey) == "" || strings.TrimSpace(endpointID) == "" {
		return 0
	}
	quote, ok := cache.quote(ctx, httpClient, apiKey, endpointID, allIDs)
	if !ok || quote.UnitPrice <= 0 {
		return 0
	}
	quantity, ok := hints.quantityForUnit(quote.Unit)
	if !ok || quantity <= 0 {
		return 0
	}
	cost := quote.UnitPrice * quantity * 1e6
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost <= 0 {
		return 0
	}
	return int64(math.Round(cost))
}

// configuredFalEndpointIDs collects every fal endpoint the current config can
// resolve — the batch the pricing cache refreshes in one request, so a turn
// that chains several media tools (speech + lipsync, say) still pays at most
// one pricing GET per TTL window.
func configuredFalEndpointIDs(config AppConfig, extra ...string) []string {
	resolvers := []func(AppConfig) string{
		resolveDefaultImageModel,
		resolveDefaultImageEditModel,
		resolveDefaultImageUpscaleModel,
		resolveDefaultVideoModel,
		resolveDefaultVideoImageModel,
		resolveDefaultVideoExtendModel,
		resolveDefaultVideoMotionModel,
		resolveDefaultVideoKeyframeModel,
		resolveDefaultVideoUpscaleModel,
		resolveDefaultVideoReframeModel,
		resolveDefaultAudioModel,
		resolveDefaultAudioCloneModel,
		resolveDefaultSoundEffectsModel,
		resolveDefaultAudioExtendModel,
		resolveDefaultTranscribeModel,
		resolveDefaultLipsyncImageModel,
		resolveDefaultLipsyncVideoModel,
	}
	ids := make([]string, 0, len(resolvers)+len(extra))
	for _, resolve := range resolvers {
		ids = append(ids, resolve(config))
	}
	ids = append(ids, extra...)
	return ids
}

// falDurationSeconds reads a canonical duration string ("5", "7.5") as billed
// seconds. ok is false when absent or not a positive number — per-second
// models then get no estimate rather than a guessed length.
func falDurationSeconds(duration string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(duration), 64)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

// falVideoTokenFPS is the fixed frame-rate factor in fal's documented token
// formula for token-billed video models (seedance-2.x):
//
//	tokens = (height × width × (input_duration + output_duration) × 24) / 1024
//
// The 24 is part of the formula itself, not the request's fps knob — fal
// counts tokens at a fixed 24 frames per second of output regardless of what
// frame rate the model renders. Verified against fal's published rates:
// 720p (1280×720) works out to 21,600 tokens/second × $0.014 = $0.3024/s,
// exactly the figure on the model page.
const falVideoTokenFPS = 24

// falVideoPixelProduct estimates one output frame's pixel budget (height ×
// width) from the request's resolution tier and aspect ratio. The tier names
// the frame's short side ("720p" → 720, "4k" → 2160); the ratio fixes the
// long side, and only the product matters — portrait and landscape of the
// same ratio bill identically. An "auto" or unrecognized ratio assumes 16:9,
// fal's landscape default; a "w:h" ratio is parsed generically. ok is false
// when the tier is missing or unrecognized: tiers span a 9× cost spread
// (720p vs 4k), so the estimate is skipped rather than pinned to a guess.
func falVideoPixelProduct(resolution, aspect string) (float64, bool) {
	tier := strings.ToLower(strings.TrimSpace(resolution))
	if tier == "" {
		return 0, false
	}
	shortSide := 0.0
	if tier == "4k" {
		shortSide = 2160
	} else {
		parsed, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSuffix(tier, "p"), " "), 64)
		if err != nil || parsed <= 0 {
			return 0, false
		}
		shortSide = parsed
	}
	ratio := 16.0 / 9.0
	trimmed := strings.ToLower(strings.TrimSpace(aspect))
	if parts := strings.Split(trimmed, ":"); len(parts) == 2 {
		w, werr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		h, herr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if werr == nil && herr == nil && w > 0 && h > 0 {
			ratio = w / h
			// The tier names the SHORT side, so the long-side multiplier is
			// the ratio's ≥1 orientation — portrait and landscape of the
			// same ratio bill the same pixel product.
			if ratio < 1 {
				ratio = 1 / ratio
			}
		}
	}
	// Clamp to sane frame ratios so a bogus schema value cannot explode the
	// estimate (the formula only needs long × short).
	if ratio < 1.0/3.0 || ratio > 3.0 {
		ratio = 16.0 / 9.0
	}
	return shortSide * shortSide * ratio, true
}

// falVideoTokenCount applies fal's token formula to one generation. Zero when
// any input is unknown — the caller then omits the estimate entirely.
func falVideoTokenCount(resolution, aspect string, seconds float64) float64 {
	pixels, ok := falVideoPixelProduct(resolution, aspect)
	if !ok || seconds <= 0 {
		return 0
	}
	return pixels * seconds * falVideoTokenFPS / 1024
}

// falVideoTokenEstimate computes a token-billed video generation's token count
// from what the request actually produced. fal reports no usage in the
// response, so the count is derived (per fal's documented formula) from the
// effective resolution tier and the billed seconds:
//
//   - Resolution defaults live in the model's schema ("720p" for seedance),
//     and a tier the model's enum rejects is dropped by resolveVideoBody —
//     schemaStringInput reproduces that resolution either way.
//   - Seconds come from the generated clip's own MP4 container (exact, and the
//     only source when the duration was "auto"), falling back to the requested
//     duration.
//   - Video inputs add their own duration to fal's token formula; the gateway
//     never probes source clips, so a turn with video inputs estimates nothing
//     rather than half the bill. Image inputs contribute no seconds.
//
// Zero means "not computable" — quantityForUnit then skips the cost.
func falVideoTokenEstimate(schema *ModelInputSchema, req VideoGenerateRequest, seconds float64) float64 {
	if len(req.SourceVideos()) > 0 || seconds <= 0 {
		return 0
	}
	resolution := schemaStringInput(schema, "resolution", req.Resolution)
	aspect := schemaStringInput(schema, "aspect_ratio", req.AspectRatio)
	return falVideoTokenCount(resolution, aspect, seconds)
}

// falVideoBilledMegapixels computes the megapixel quantity an MP-billed video
// model is charged for. fal's ltx-2.3-22b family prices per "megapixel of
// generated video data (width × height × frames)":
//
//   - Frame size always comes from the rendered clip's own tkhd — exact, and
//     the only source when the model's video_size is "auto" (extend inherits
//     the source clip's dimensions).
//   - Frames: a pure generation's container carries exactly the frames the
//     model rendered (stts sample count). Video-source turns (extend/motion)
//     must NOT read the container — the output embeds the source footage and
//     would overstate the bill (the same principle as seconds there) — so the
//     generated count is the effective num_frames input, which is the schema's
//     default: Atelier maps no canonical knob onto num_frames, so an omitted
//     input is what the request actually rode.
//
// Zero means "not computable" — quantityForUnit then skips the cost.
func falVideoBilledMegapixels(schema *ModelInputSchema, req VideoGenerateRequest, output []byte) float64 {
	width, height, ok := mp4VideoDimensions(output)
	if !ok {
		return 0
	}
	frames := 0.0
	if count, ok := mp4VideoFrameCount(output); ok && len(req.SourceVideos()) == 0 {
		frames = count
	} else {
		frames = schemaNumberDefault(schema, "num_frames")
	}
	if frames <= 0 {
		return 0
	}
	return width * height * frames / 1e6
}

// imagePixelDimensions sniffs a decoded image payload's pixel size from its
// magic bytes — PNG/GIF/WebP read from fixed headers, JPEG by scanning for a
// SOFn marker segment. No codec imports (x/image is not a dependency): pricing
// only needs the dimensions, and the outputs fal returns are PNG/JPEG/WebP.
func imagePixelDimensions(data []byte) (float64, float64, bool) {
	// PNG: 8-byte signature, IHDR's width/height at offsets 16 and 20.
	if len(data) >= 24 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		width := float64(binary.BigEndian.Uint32(data[16:20]))
		height := float64(binary.BigEndian.Uint32(data[20:24]))
		return width, height, width > 0 && height > 0
	}
	// GIF: 6-byte signature, then the 7-byte logical screen descriptor's
	// little-endian width/height.
	if len(data) >= 10 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		width := float64(binary.LittleEndian.Uint16(data[6:8]))
		height := float64(binary.LittleEndian.Uint16(data[8:10]))
		return width, height, width > 0 && height > 0
	}
	// WebP: RIFF container. VP8X (extended) carries the canvas size minus one
	// as 24-bit little-endian; VP8 (lossy) and VP8L (lossless) carry their own
	// dimensions after their chunk headers.
	if len(data) >= 30 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		switch string(data[12:16]) {
		case "VP8X":
			width := float64(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16) + 1
			height := float64(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16) + 1
			return width, height, width > 0 && height > 0
		case "VP8 ":
			// 3-byte frame tag, then the 10-bit/22-bit start code and
			// little-endian dimensions (14 bits each, stored low 14 bits).
			width := float64(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff)
			height := float64(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
			return width, height, width > 0 && height > 0
		case "VP8L":
			// 5-byte signature, then a 14+14-bit packed little-endian size.
			bits := uint32(binary.LittleEndian.Uint16(data[21:23])) | uint32(data[23])<<16 | uint32(data[24])<<24
			width := float64(bits&0x3fff) + 1
			height := float64((bits>>14)&0x3fff) + 1
			return width, height, width > 0 && height > 0
		}
		return 0, 0, false
	}
	// JPEG: scan the marker segments for SOF0..SOF15 (minus DHT/JPG/DAC) and
	// read the big-endian height/width that follow the segment length.
	if len(data) >= 4 && data[0] == 0xff && data[1] == 0xd8 {
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
			if offset+4 > len(data) {
				break
			}
			segmentLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
			if segmentLen < 2 {
				break
			}
			isSOF := marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc
			if isSOF && offset+9 <= len(data) {
				height := float64(binary.BigEndian.Uint16(data[offset+5 : offset+7]))
				width := float64(binary.BigEndian.Uint16(data[offset+7 : offset+9]))
				return width, height, width > 0 && height > 0
			}
			offset += 2 + segmentLen
		}
	}
	return 0, 0, false
}

// imageResultMegapixels sums the megapixels of a generated-image response's
// decoded payloads — the quantity an MP-billed image model (fal's flux essenza
// prices per megapixel of output) bills. References that are not data URLs or
// that don't decode contribute nothing: an undercount beats a fetch or a guess.
func imageResultMegapixels(images []string, single string) float64 {
	total := 0.0
	add := func(ref string) {
		data, _, err := decodeMediaDataURL(ref)
		if err != nil {
			return
		}
		if width, height, ok := imagePixelDimensions(data); ok {
			total += width * height / 1e6
		}
	}
	for _, ref := range images {
		add(ref)
	}
	if single != "" {
		add(single)
	}
	return total
}

// schemaStringInput resolves the effective value of a native string input:
// the explicit value when the schema accepts it (or doesn't constrain it),
// else the schema's declared default, else "". A value the schema's enum
// rejects is treated as dropped — resolveVideoBody drops it with a notice, so
// the model's server-side default is what actually billed. Acceptance is
// enumValueFor's case-insensitive match, and the accepted spelling is the
// enum's own member, mirroring resolveVideoBody exactly (a "1080p" request on
// minimax's uppercase "1080P" enum is sent as 1080P and bills as 1080P, not
// as dropped-to-default).
func schemaStringInput(schema *ModelInputSchema, name, explicit string) string {
	if schema == nil {
		return strings.TrimSpace(explicit)
	}
	explicit = strings.TrimSpace(explicit)
	if prop, ok := schema.property(name); ok {
		if explicit != "" {
			if canonical, allowed := enumValueFor(prop, explicit); allowed {
				return canonical
			}
		}
		if def, isStr := prop.Default.(string); isStr {
			return strings.TrimSpace(def)
		}
	}
	return explicit
}

// schemaNumberDefault reads a numeric input's declared default from the model's
// schema — the value fal applied server-side when the request omitted the
// input. Zero when the schema is missing, has no such input, or declares no
// numeric default.
func schemaNumberDefault(schema *ModelInputSchema, name string) float64 {
	prop, ok := schema.property(name)
	if !ok {
		return 0
	}
	switch value := prop.Default.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case uint64:
		return float64(value)
	}
	return 0
}

// mp4DurationSeconds reads a generated clip's exact duration from its MP4
// container — the moov/mvhd box's duration ÷ timescale. The gateway already
// holds the downloaded bytes, so this prices token- and per-second models by
// what was actually rendered (a planner-omitted or "auto" duration is
// unknowable from the request). Pure byte parsing, no ffprobe dependency;
// fail-soft on any non-MP4 or malformed payload.
func mp4DurationSeconds(data []byte) (float64, bool) {
	if len(data) < 8 {
		return 0, false
	}
	// ftyp must lead a valid MP4; a different container (MOV variants, webm)
	// is out of scope for the probe.
	if string(data[4:8]) != "ftyp" {
		return 0, false
	}
	moov, ok := mp4ChildBoxPayload(data, "moov")
	if !ok {
		return 0, false
	}
	mvhd, ok := mp4ChildBoxPayload(moov, "mvhd")
	if !ok || len(mvhd) < 4 {
		return 0, false
	}
	// mvhd payload: version(1) flags(3), then v0 [created(4) modified(4)
	// timescale(4) duration(4)] or v1 [created(8) modified(8) timescale(4)
	// duration(8)].
	var timescale, duration float64
	switch mvhd[0] {
	case 1:
		if len(mvhd) < 32 {
			return 0, false
		}
		timescale = float64(binary.BigEndian.Uint32(mvhd[20:24]))
		duration = float64(binary.BigEndian.Uint64(mvhd[24:32]))
	default:
		if len(mvhd) < 20 {
			return 0, false
		}
		timescale = float64(binary.BigEndian.Uint32(mvhd[12:16]))
		duration = float64(binary.BigEndian.Uint32(mvhd[16:20]))
	}
	if timescale <= 0 || duration <= 0 {
		return 0, false
	}
	return duration / timescale, true
}

// mp4ChildBoxPayload scans one container's boxes for the first of the wanted
// type and returns its payload. Handles the 32-bit size form plus size==0
// (box runs to end of data) and size==1 (64-bit largesize).
func mp4ChildBoxPayload(data []byte, boxType string) ([]byte, bool) {
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		headerLen := uint64(8)
		if size == 1 {
			if offset+16 > len(data) {
				return nil, false
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerLen = 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < headerLen || offset+int(size) > len(data) {
			return nil, false
		}
		if string(data[offset+4:offset+8]) == boxType {
			return data[offset+int(headerLen) : offset+int(size)], true
		}
		offset += int(size)
	}
	return nil, false
}

// mp4EachChild walks one container's immediate child boxes, calling visit with
// each box's type and payload. The walk stops early when visit returns false.
// Shares mp4ChildBoxPayload's size-form handling (32-bit, 64-bit largesize,
// size==0 to end of data); a malformed box truncates the walk.
func mp4EachChild(data []byte, visit func(boxType string, payload []byte) bool) {
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		headerLen := uint64(8)
		if size == 1 {
			if offset+16 > len(data) {
				return
			}
			size = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			headerLen = 16
		} else if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < headerLen || offset+int(size) > len(data) {
			return
		}
		if !visit(string(data[offset+4:offset+8]), data[offset+int(headerLen):offset+int(size)]) {
			return
		}
		offset += int(size)
	}
}

// mp4VideoTrak returns the payload of moov's first trak whose mdia/hdlr names
// the 'vide' handler — the video track, whose tkhd/stts carry the frame size
// and count pricing needs. ok is false when no video track parses; an
// audio-only container is not priced.
func mp4VideoTrak(data []byte) ([]byte, bool) {
	moov, ok := mp4ChildBoxPayload(data, "moov")
	if !ok {
		return nil, false
	}
	var videoTrak []byte
	mp4EachChild(moov, func(boxType string, payload []byte) bool {
		if boxType != "trak" {
			return true
		}
		mdia, ok := mp4ChildBoxPayload(payload, "mdia")
		if !ok {
			return true
		}
		hdlr, ok := mp4ChildBoxPayload(mdia, "hdlr")
		// hdlr payload: version+flags(4), pre_defined(4), then the 4-byte
		// handler_type ('vide' for a video track, 'soun' for audio).
		if !ok || len(hdlr) < 12 || string(hdlr[8:12]) != "vide" {
			return true
		}
		videoTrak = payload
		return false
	})
	if videoTrak == nil {
		return nil, false
	}
	return videoTrak, true
}

// mp4HasAudioTrack reports whether the MP4 carries an audio track — any moov
// trak whose mdia/hdlr names the 'soun' handler. Pure byte parsing, fail-soft
// false on malformed payloads; the ffmpeg portion-speed tool reads it when
// ffprobe is unavailable, because a filter_complex must not reference [0:a]
// on a silent clip.
func mp4HasAudioTrack(data []byte) bool {
	moov, ok := mp4ChildBoxPayload(data, "moov")
	if !ok {
		return false
	}
	found := false
	mp4EachChild(moov, func(boxType string, payload []byte) bool {
		if boxType != "trak" {
			return true
		}
		mdia, ok := mp4ChildBoxPayload(payload, "mdia")
		if !ok {
			return true
		}
		hdlr, ok := mp4ChildBoxPayload(mdia, "hdlr")
		if ok && len(hdlr) >= 12 && string(hdlr[8:12]) == "soun" {
			found = true
			return false
		}
		return true
	})
	return found
}

// mp4VideoDimensions reads the video track's presentation size from tkhd —
// width and height ride as 32-bit 16.16 fixed-point values. This is the
// effective render size: a model whose video_size is "auto" inherits the
// source clip's dimensions, and the output carries what was actually rendered.
func mp4VideoDimensions(data []byte) (float64, float64, bool) {
	trak, ok := mp4VideoTrak(data)
	if !ok {
		return 0, 0, false
	}
	tkhd, ok := mp4ChildBoxPayload(trak, "tkhd")
	if !ok || len(tkhd) < 4 {
		return 0, 0, false
	}
	// tkhd payload: version(1) flags(3), then v0 [created(4) modified(4)
	// trackID(4) reserved(4) duration(4) reserved(8) layer(2) altGroup(2)
	// volume(2) reserved(2) matrix(36)] or v1 [created(8) modified(8)
	// trackID(4) reserved(4) duration(8) + the same trailer], with width and
	// height's 16.16 fixed-point pair closing the box.
	widthOffset := 76
	if tkhd[0] == 1 {
		widthOffset = 88
	}
	if len(tkhd) < widthOffset+8 {
		return 0, 0, false
	}
	width := float64(binary.BigEndian.Uint32(tkhd[widthOffset:widthOffset+4])) / 65536
	height := float64(binary.BigEndian.Uint32(tkhd[widthOffset+4:widthOffset+8])) / 65536
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// mp4CodedVideoDimensions reads the video track's coded frame size from the
// first stsd sample entry — the VisualSampleEntry fixed header's uint16
// width/height pair, the size the decoder produces, versus tkhd's
// presentation size. The ffmpeg screenshot tool compares the two: a
// difference is an anamorphic container, whose players stretch the coded
// frames to the tkhd size. Pure byte parsing, fail-soft on anything
// unparseable; an MP4 without a readable sample table reports no coded size.
func mp4CodedVideoDimensions(data []byte) (int, int, bool) {
	trak, ok := mp4VideoTrak(data)
	if !ok {
		return 0, 0, false
	}
	mdia, ok := mp4ChildBoxPayload(trak, "mdia")
	if !ok {
		return 0, 0, false
	}
	minf, ok := mp4ChildBoxPayload(mdia, "minf")
	if !ok {
		return 0, 0, false
	}
	stbl, ok := mp4ChildBoxPayload(minf, "stbl")
	if !ok {
		return 0, 0, false
	}
	stsd, ok := mp4ChildBoxPayload(stbl, "stsd")
	if !ok || len(stsd) < 8 {
		return 0, 0, false
	}
	// stsd payload: version+flags(4) entry_count(4), then the sample entries;
	// a VisualSampleEntry's fixed header carries width(2)/height(2) at its
	// payload offsets 24/26.
	width, height := 0, 0
	mp4EachChild(stsd[8:], func(boxType string, payload []byte) bool {
		if len(payload) < 28 {
			return true
		}
		encodedWidth := int(binary.BigEndian.Uint16(payload[24:26]))
		encodedHeight := int(binary.BigEndian.Uint16(payload[26:28]))
		if encodedWidth > 0 && encodedHeight > 0 {
			width, height = encodedWidth, encodedHeight
			return false
		}
		return true
	})
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// mp4VideoFrameCount sums the video track's stts sample counts — every frame
// the container actually carries. Exact for a purely generated clip, where the
// output holds exactly the frames the model rendered.
func mp4VideoFrameCount(data []byte) (float64, bool) {
	trak, ok := mp4VideoTrak(data)
	if !ok {
		return 0, false
	}
	mdia, ok := mp4ChildBoxPayload(trak, "mdia")
	if !ok {
		return 0, false
	}
	minf, ok := mp4ChildBoxPayload(mdia, "minf")
	if !ok {
		return 0, false
	}
	stbl, ok := mp4ChildBoxPayload(minf, "stbl")
	if !ok {
		return 0, false
	}
	stts, ok := mp4ChildBoxPayload(stbl, "stts")
	if !ok || len(stts) < 8 {
		return 0, false
	}
	// stts payload: version+flags(4), entry_count(4), then run-length entries
	// of {sample_count(4), sample_delta(4)}.
	entries := int(binary.BigEndian.Uint32(stts[4:8]))
	if entries < 0 || 8+entries*8 > len(stts) {
		return 0, false
	}
	total := 0.0
	for i := 0; i < entries; i++ {
		offset := 8 + i*8
		total += float64(binary.BigEndian.Uint32(stts[offset : offset+4]))
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// falVideoBilledSeconds resolves the output duration for pricing: the
// generated clip's own container is exact (including "auto" durations the
// request never stated); the requested duration is the fallback when the
// container can't be parsed.
func falVideoBilledSeconds(output []byte, requested string) (float64, bool) {
	if seconds, ok := mp4DurationSeconds(output); ok {
		return seconds, true
	}
	return falDurationSeconds(requested)
}

// estimateFalGenerationCost is the App-level entry the tool gateway calls
// after a successful fal generation. Nil-receiver safe (tests build bare App
// values) and returns 0 whenever pricing cannot be resolved.
func (a *App) estimateFalGenerationCost(ctx context.Context, config AppConfig, endpointID string, hints falBillingHints) int64 {
	if a == nil || a.client == nil || a.falPricing == nil {
		return 0
	}
	apiKey, err := loadFalAPIKey()
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return 0
	}
	return estimateFalCostMicros(ctx, a.falPricing, a.client, apiKey, endpointID, configuredFalEndpointIDs(config, endpointID), hints)
}
