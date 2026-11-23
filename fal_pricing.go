package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"atelier/internal/mediasniff"
)

// fal media generation has no token meter — fal bills per output unit (an
// image, a second of video, a character of speech, one request, a block of
// video tokens, or a megapixel of generated pixels, depending on the model).
//
// A completed generation's cost has two sources, tried in order:
//
//   - REPORTED: GET {falPlatformBaseURL}/v1/models/billing-events?request_id=…
//     returns fal's own charge for that one request (cost_estimate_nano_usd,
//     plus the quantity and unit price it was measured against). This is the
//     number the fal dashboard shows, and the only one that is right wherever
//     the estimate cannot be: the endpoint prices by a feature the request
//     can't recompute (h3-max/recast's input-video tokens, Kling's voice
//     control), or the pricing API's quote simply doesn't describe the
//     endpoint's real table (conv_b1fbbc85d72ecd1c9bcc1a76: two recast calls
//     estimated $0.05 flat against a documented $0.17/s). It needs a key whose
//     permissions include billing:usage:read (the BILLING or FULL preset) —
//     an inference-only key is refused, and that refusal is remembered.
//
//   - ESTIMATED (the fallback): GET {falPlatformBaseURL}/v1/models/pricing?endpoint_id=a,b,c
//     returns each endpoint's unit price and billing unit, and the tool
//     gateway multiplies by the quantity it knows from the request (image
//     count, requested or rendered seconds, prompt length, or rendered pixels
//     — width × height × frames for video). This is an estimate, not a bill.
//     The quote is a BASE price — fal applies "proportional adjustments for
//     resolution" when it bills — so the endpoints whose pages publish a
//     tier table are scaled onto the tier the request actually rode
//     (falVideoTierMultiplier; conv_3e8e49c2839590e100f5128c: a 1080p
//     h3-max/reference-to-video turn priced at the 480p base, $0.5072 against
//     fal's $1.60), and feature multipliers fal applies server-side that no
//     table here covers may still be missed. Token- and megapixel-billed
//     video models (seedance-2.x per "1000 tokens", the ltx-2.3-22b family
//     per "megapixel") report no usage in the generation response either;
//     their quantity is computed from fal's documented formulas over the
//     request's actual parameters (see falVideoTokenEstimate and
//     falVideoBilledMegapixels).
//
// Everything here is fail-soft by design: no key, no network, an unknown
// billing unit, or an unresolvable quantity yields 0 (no cost shown), never a
// failed or delayed generation — the reported lookup is one bounded request
// with no retry, and anything but a matching event falls through to the
// estimate.

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

	// falBillingDeniedTTL is how long a refusal from the billing-events feed
	// is remembered. 401/403/404 means the configured key cannot read billing
	// (an inference-only preset, or the feed not enabled for the account) —
	// a standing condition no retry this process can fix, so the estimate
	// serves the rest of the day rather than paying a refused request per
	// generation. A key change restarts the app and the clock with it.
	falBillingDeniedTTL = 24 * time.Hour
	// falBillingFailureTTL is the transient-failure window — the pricing
	// cache's negative TTL, for the same reason: an offline machine must not
	// retry on every media turn, and a recovered network is picked up in
	// minutes.
	falBillingFailureTTL = 10 * time.Minute
	// falBillingLookupTimeout bounds the one reported-cost request. Cost is
	// telemetry: a slow billing API may cost the turn a few seconds, never
	// the generation itself.
	falBillingLookupTimeout = 5 * time.Second
	// falBillingLookback widens the feed query's start bound (fal's default
	// is 24h) so a generation recovered long after its submit, or a clock
	// skew, still lands inside the window. The endpoint caps a range at 90
	// days; a week is far beyond any turn.
	falBillingLookback = 7 * 24 * time.Hour
	// falMaxTierMultiplier bounds the resolution-tier scaling of a quote —
	// a sanity cap on the documented tables (the steepest today is 6.4×,
	// h3-max extend-video's 2K over its 480p base).
	falMaxTierMultiplier = 32.0
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
	// billingDeniedAt / billingFailedAt are the billing-events feed's negative
	// windows (falBillingDeniedTTL / falBillingFailureTTL) — zero while the
	// reported-cost lookup is worth attempting.
	billingDeniedAt time.Time
	billingFailedAt time.Time
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
	if err := cache.fetch(ctx, httpClient, apiKey, append(append([]string{}, allIDs...), endpointID), false); err != nil {
		// fal prices a batch all-or-nothing: an unknown or malformed endpoint
		// id fails the request for EVERY id in it, so one bad value — free
		// text typed into a Settings model slot, an endpoint fal has not
		// priced yet (conv_ebcb7dff: a speech turn priced 0 because the batch
		// carried a non-endpoint video-model value) — would keep every quote
		// out of the cache until the slot is fixed. Fall back to the one id
		// this call actually prices, merged in so earlier fallback quotes
		// survive; only BOTH requests failing counts as the outage the
		// negative TTL remembers.
		if err := cache.fetch(ctx, httpClient, apiKey, []string{endpointID}, true); err != nil {
			cache.failedAt = now
			return falPriceQuote{}, false
		}
	}
	quote, ok := cache.quotes[endpointID]
	return quote, ok
}

// fetch populates the cache from one batched pricing request (plus cursor
// pages). merge appends the fetched quotes onto the existing table instead of
// replacing it — the solo fallback after a failed batch keeps the quotes that
// earlier fallbacks fetched, since the batch that would refresh them keeps
// failing. Caller holds the mutex.
func (cache *falPricingCache) fetch(ctx context.Context, httpClient *http.Client, apiKey string, endpointIDs []string, merge bool) error {
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
	// removed endpoint must not linger at its old rate. The merge form is the
	// solo-fallback exception — it owns no refresh. fetchedAt only moves on
	// success (the updater-state pattern), so transient failures retry.
	if merge {
		for id, quote := range quotes {
			cache.quotes[id] = quote
		}
	} else {
		cache.quotes = quotes
	}
	cache.fetchedAt = time.Now()
	cache.failedAt = time.Time{}
	return nil
}

// falBillingEvent mirrors one record of the billing-events feed. Only the
// request's own charge is read: cost_estimate_nano_usd is the precision-
// preserving form of cost_total (an integer count, where summing float dollars
// drifts), so it wins and cost_total stands in when absent.
type falBillingEvent struct {
	RequestID   string   `json:"request_id"`
	CostTotal   *float64 `json:"cost_total"`
	CostNanoUSD *float64 `json:"cost_estimate_nano_usd"`
}

// falBillingEventsResponse mirrors the feed's payload. A request_id filter
// returns at most a handful of rows, so one page is read — the matching ids
// are all that matter, and fal's own limit is far above that.
type falBillingEventsResponse struct {
	BillingEvents []falBillingEvent `json:"billing_events"`
}

// falBillingLookup is the outcome of one reported-cost request.
type falBillingLookup int

const (
	// falBillingMissing: the feed answered and carries no event for the
	// request — most often because billing posts a moment after the result
	// does. Nothing to cache; the next generation asks again.
	falBillingMissing falBillingLookup = iota
	falBillingReported
	// falBillingDenied: the key cannot read billing. Standing, so it is
	// remembered for falBillingDeniedTTL.
	falBillingDenied
	// falBillingFailed: transport, status, or decode failure. Transient, so
	// it is remembered for falBillingFailureTTL only.
	falBillingFailed
)

// fetchFalBillingCost makes the one reported-cost request for requestID and
// returns fal's charge in USD millionths. A request can bill as several lines
// (an output plus a reference surcharge), so every event carrying the id is
// summed; an id appearing on no event is falBillingMissing, never $0 — a zero
// here means "not reported", so the caller's estimate still has its say.
func fetchFalBillingCost(ctx context.Context, httpClient *http.Client, apiKey, requestID string) (int64, falBillingLookup) {
	return fetchFalBillingCostSince(ctx, httpClient, apiKey, requestID, time.Now().Add(-falBillingLookback))
}

// fetchFalBillingCostSince is fetchFalBillingCost with the feed window's start
// given instead of derived (falBillingLookback back from now). The live check
// names a window that covers a request recorded days ago, so it keeps
// exercising this exact path as that request ages (fal_pricing_live_test.go).
func fetchFalBillingCostSince(ctx context.Context, httpClient *http.Client, apiKey, requestID string, start time.Time) (int64, falBillingLookup) {
	query := url.Values{}
	query.Set("request_id", requestID)
	query.Set("start", start.UTC().Format(time.RFC3339))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, falPlatformBaseURL+"/v1/models/billing-events?"+query.Encode(), nil)
	if err != nil {
		return 0, falBillingFailed
	}
	req.Header.Set("Authorization", "Key "+apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, falBillingFailed
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return 0, falBillingDenied
	case http.StatusOK:
	default:
		return 0, falBillingFailed
	}
	var payload falBillingEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, falBillingFailed
	}
	nanoUSD := 0.0
	for _, event := range payload.BillingEvents {
		if strings.TrimSpace(event.RequestID) != requestID {
			continue
		}
		switch {
		case event.CostNanoUSD != nil && *event.CostNanoUSD > 0:
			nanoUSD += *event.CostNanoUSD
		case event.CostTotal != nil && *event.CostTotal > 0:
			nanoUSD += *event.CostTotal * 1e9
		}
	}
	if math.IsNaN(nanoUSD) || math.IsInf(nanoUSD, 0) || nanoUSD <= 0 {
		return 0, falBillingMissing
	}
	// 1 micro = 1,000 nano. A charge below half a micro rounds away to
	// nothing, which is the same "not reported" the event's absence is.
	micros := int64(math.Round(nanoUSD / 1000))
	if micros <= 0 {
		return 0, falBillingMissing
	}
	return micros, falBillingReported
}

// reportedCostMicros reads fal's own charge for one completed request from the
// billing-events feed. ok is false — and nothing is guessed — when the lookup
// is not worth making (no id, a negative window open), is refused, fails, or
// finds no event yet; the caller then falls back to the estimate. The mutex
// is held across the request for the same reason quote holds it: harness tool
// calls run sequentially, so this serializes nothing that was concurrent.
func (cache *falPricingCache) reportedCostMicros(ctx context.Context, httpClient *http.Client, apiKey, requestID string) (int64, bool) {
	requestID = strings.TrimSpace(requestID)
	if httpClient == nil || requestID == "" || strings.TrimSpace(apiKey) == "" {
		return 0, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := time.Now()
	if now.Sub(cache.billingDeniedAt) < falBillingDeniedTTL || now.Sub(cache.billingFailedAt) < falBillingFailureTTL {
		return 0, false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, falBillingLookupTimeout)
	defer cancel()
	cost, outcome := fetchFalBillingCost(lookupCtx, httpClient, apiKey, requestID)
	switch outcome {
	case falBillingDenied:
		cache.billingDeniedAt = now
	case falBillingFailed:
		cache.billingFailedAt = now
	}
	return cost, outcome == falBillingReported
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
	// PriceMultiplier scales the computed price after quantity × unit price,
	// for fal-documented conditional discounts. The seedance reference-to-video
	// family multiplies by 0.6 whenever the call carries video inputs (see
	// falVideoReferenceDiscount); 0 means no multiplier applies.
	PriceMultiplier float64
	// TierMultiplier scales the quote the other way — up from the base price
	// onto the resolution tier the request actually rode, for the endpoints
	// whose pages publish a per-tier table (see falVideoTierMultiplier). 0 or
	// 1 means the request rode the base tier or the endpoint has no table.
	TierMultiplier float64
	// RequestID is the fal queue request id of the completed generation this
	// call prices. When set, fal's own charge for that request is read first
	// (falPricingCache.reportedCostMicros) and the estimate serves only when
	// no event is available.
	RequestID string
}

// quantityForUnit maps a billing unit onto the hint that prices it.
func (hints falBillingHints) quantityForUnit(unit string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "image", "images":
		// fal's flat per-generated-image unit. The nano-banana-pro family
		// (text-to-image and /edit alike) returns the PLURAL "images" —
		// conv_a465108a5f7ec78267191163 priced a nano-banana-pro/edit call "?"
		// until the plural was taught (verified against the live pricing API,
		// 2026-11: unit_price 0.15, unit "images") — while "image" singular
		// stays for the endpoints that still spell it that way.
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
	case "cloned voice", "cloned voices":
		// fal's per-designed-voice unit — minimax/voice-design returns
		// "cloned voices" ($3 per designed voice, verified 2026-10): each
		// call designs exactly one voice whatever the preview's length, so
		// it prices like "request" (conv_1a989138830e7eaa29f6c429: the unit
		// fell through to the unpriced default and the design stamped
		// costUnknown).
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
	case "minute", "minutes":
		// fal's per-minute video unit — sync-lipsync bills per minute of
		// output video ($3/min on v2, $0.70/min on v1, verified 2026-10).
		// A minute is exactly sixty seconds of the seconds hint, which the
		// lipsync call site fills from the rendered clip's own container
		// (see falLipsyncBillingSeconds).
		return hints.Seconds / 60, hints.Seconds > 0
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
		// the request can never state) — stays unpriced rather than guessed
		// onto a nearby hint.
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
	if hints.PriceMultiplier > 0 && hints.PriceMultiplier <= 1 {
		cost *= hints.PriceMultiplier
	}
	// The tier multiplier is a surcharge, so its guard is the discount's
	// mirror: only a value above 1 (and under the sanity cap) scales — 0 and 1
	// both mean the base tier, and a bogus value never inflates a bill. The
	// documented tables are per-second rates, so they scale only a per-second
	// quote; a flat per-clip quote on such an endpoint describes something
	// else and is left alone.
	perSecond := quote.Unit == "second" || quote.Unit == "seconds"
	if perSecond && hints.TierMultiplier > 1 && hints.TierMultiplier <= falMaxTierMultiplier {
		cost *= hints.TierMultiplier
	}
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
		resolveDefaultVideoRestyleModel,
		resolveFalVideoCharacterModel,
		resolveDefaultAudioModel,
		resolveDefaultAudioCloneModel,
		resolveDefaultSoundEffectsModel,
		resolveDefaultAudioExtendModel,
		resolveDefaultTranscribeModel,
	}
	ids := make([]string, 0, len(resolvers)+len(extra))
	for _, resolve := range resolvers {
		ids = append(ids, resolve(config))
	}
	// The lipsync slots route by Models.LipsyncProvider, so their fal
	// endpoints only join the batch when fal actually serves lip_sync — a
	// replicate-pinned config's fal lipsync slots are dead config.
	if resolveLipsyncProvider(config) == "fal" {
		ids = append(ids,
			resolveDefaultLipsyncImageModel(config, "fal"),
			resolveDefaultLipsyncVideoModel(config, "fal"))
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

// audioBillingSeconds resolves the length a per-second audio model bills (the
// elevenlabs sound-effects family prices per second of generated audio): the
// rendered clip's own duration first — fal charges the output it actually
// produced, which is the only source when the model decided the length itself
// (an "auto" duration turn, conv_a3fecc876e6360a50ed6e8b5 billed "?" until
// this existed) — then the requested duration as a stand-in when the bytes
// don't probe. ok is false when neither is known: the cost is skipped rather
// than guessed, the shared fail-soft rule.
func audioBillingSeconds(requested string, rendered []byte) (float64, bool) {
	if seconds, ok := mediasniff.AudioDuration(rendered); ok && seconds > 0 {
		return seconds, true
	}
	return falDurationSeconds(requested)
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
//   - Output seconds come from the generated clip's own MP4 container (exact,
//     and the only source when the duration was "auto"), falling back to the
//     requested duration — the caller resolves them via falVideoBilledSeconds.
//   - Video inputs add their own duration to fal's token formula
//     (input_video_duration + output_duration, billed alongside the output per
//     fal's model pages); their seconds are parsed from the staged data URLs
//     before the gateway hosts them (falVideoSourceSeconds). With no local
//     bytes the input half is unknowable, so the estimate is skipped rather
//     than covering only the output. Image inputs contribute no seconds.
//
// Zero means "not computable" — quantityForUnit then skips the cost.
func falVideoTokenEstimate(schema *ModelInputSchema, req VideoGenerateRequest, sourceSeconds, outputSeconds float64) float64 {
	if outputSeconds <= 0 {
		return 0
	}
	seconds := outputSeconds
	if len(req.SourceVideos()) > 0 {
		if sourceSeconds <= 0 {
			return 0
		}
		seconds += sourceSeconds
	}
	resolution := schemaStringInput(schema, "resolution", req.Resolution)
	aspect := schemaStringInput(schema, "aspect_ratio", req.AspectRatio)
	return falVideoTokenCount(resolution, aspect, seconds)
}

// falVideoSourceSeconds sums the billed input duration across a turn's source
// clips — the input_video_duration term of fal's token formula. The harness
// stages source media as base64 data URLs, so the gateway captures this BEFORE
// resolveMediaURL hosts oversized payloads on fal's CDN; a hosted URL's bytes
// are no longer local, and pricing never fetches. ok is false when any clip's
// duration can't be read: a partial sum would bill only the parsed clips, so
// the whole estimate is skipped (the activity then carries CostUnknown).
func falVideoSourceSeconds(videos []string) (float64, bool) {
	if len(videos) == 0 {
		return 0, false
	}
	total := 0.0
	for _, video := range videos {
		data, _, err := decodeMediaDataURL(video)
		if err != nil {
			return 0, false
		}
		seconds, ok := mediasniff.MP4Duration(data)
		if !ok || seconds <= 0 {
			return 0, false
		}
		total += seconds
	}
	return total, true
}

// falVideoReferenceDiscount returns the price multiplier fal applies when a
// call carries video inputs, documented on the seedance reference-to-video
// model pages: seedance-2.0 ("If video inputs are provided the price is
// multiplied by 0.6") and seedance-2.5 ("If any video references are provided,
// the price is multiplied by 0.6", applied to input and output seconds alike).
// Scoped to the family whose pages state it — a discount is never guessed onto
// another model's quote. Zero means none.
func falVideoReferenceDiscount(model string) float64 {
	if strings.HasPrefix(strings.TrimSpace(model), "bytedance/seedance-2.") {
		return 0.6
	}
	return 0
}

// falVideoTier is one rung of an endpoint's per-second resolution table.
type falVideoTier struct {
	// Name is the tier as its schema spells it, uppercased — the h3-max
	// endpoints spell the same enum "1080P" and "1080p", so lookup is
	// case-insensitive.
	Name string
	// Rate is fal's documented USD per second at the tier.
	Rate float64
}

// falVideoTiers returns fal's documented per-second rates by resolution tier
// for the endpoints whose model pages publish a table, cheapest tier first
// (read off fal.ai/models/<id>, 2026-11). The pricing API quotes each of
// these endpoints at its FIRST tier only — its own docs call the quote a
// "Base price" that billing adjusts "for resolution". Verified two ways. A
// live read of all seven (2026-11-24, fal_pricing_live_test.go) returned a
// per-second quote at or under the table's $0.05 — h3-max image-to-video and
// text-to-video at $0.0375, a proportional discount that leaves the ratios
// intact. And 130 priced turns in history — every minimax turn whose billed
// length could be re-measured (115 on h3-max/reference-to-video, 14 on
// h3-max/extend-video, one on h3/text-to-video) — imply exactly the $0.05
// 480p rate, conv_3e8e49c2839590e100f5128c's 1080p turn among them. So a
// request on a higher tier is under-reported by the tier's ratio until it is
// scaled (that turn recorded $0.5072 against fal's $1.60). Nil for every
// other endpoint: its quote is used as is, and no ratio is guessed onto a
// model whose page states none.
//
// Deliberately NOT listed, though their pages also price by tier or feature:
//   - minimax/h3-max/recast ($0.085/s at 768p, $0.17/s at 1080p, plus input
//     video tokens) and minimax/h3-max/reframe-video (the reference table's
//     three tiers) — the pricing API quotes both FLAT: $0.05 per "units",
//     not a per-second rate (live 2026-11-24). There is no base to scale, and
//     estimateFalCostMicros leaves a non-per-second quote alone, so a table
//     entry could never apply. Both estimate $0.05 a call whatever their
//     length or tier (conv_b1fbbc85d72ecd1c9bcc1a76: two 6.6s recast calls
//     at the 1080p default recorded $0.05 each against roughly $2 each).
//   - Kling v3's audio toggle (its page: $0.084/s off, $0.126/s on) — the
//     pricing API quotes $0.14/s (live 2026-11-24, and the same figure
//     implied by the one priced turn in history), matching none of the page's
//     three rates, so which configuration the base describes is unknown.
//
// All three are served by the reported charge instead (reportedCostMicros).
func falVideoTiers(model string) []falVideoTier {
	switch strings.TrimSpace(model) {
	case "minimax/h3-max/text-to-video",
		"minimax/h3-max/image-to-video",
		"minimax/h3-max/reference-to-video":
		return []falVideoTier{{"480P", 0.05}, {"768P", 0.08}, {"1080P", 0.16}}
	case "minimax/h3-max/extend-video":
		return []falVideoTier{{"480P", 0.05}, {"768P", 0.08}, {"1080P", 0.16}, {"2K", 0.32}}
	case "minimax/h3/text-to-video",
		"minimax/h3/image-to-video",
		"minimax/h3/reference-to-video":
		return []falVideoTier{{"480P", 0.05}, {"768P", 0.06}, {"2K", 0.13}, {"4K", 0.16}}
	}
	return nil
}

// falVideoTierMultiplier returns how many times the base quote a request that
// rode resolution costs on a tiered endpoint — the tier's documented rate over
// the cheapest tier's, so the pricing API's live quote (and any proportional
// account discount already folded into it) is scaled rather than replaced by
// a hardcoded price. Zero when the endpoint has no table, the tier is the
// base one, or the tier is not in the table (a value the resolver would have
// dropped for the model's own default — the caller passes the EFFECTIVE tier
// from schemaStringInput, so that default is what gets priced).
func falVideoTierMultiplier(model, resolution string) float64 {
	tiers := falVideoTiers(model)
	if len(tiers) < 2 || tiers[0].Rate <= 0 {
		return 0
	}
	want := strings.ToUpper(strings.TrimSpace(resolution))
	for _, tier := range tiers[1:] {
		if tier.Name == want {
			if tier.Rate <= tiers[0].Rate {
				return 0
			}
			return tier.Rate / tiers[0].Rate
		}
	}
	return 0
}

// falBillsRequestedDuration reports the endpoints whose page bills the
// duration the REQUEST asked for rather than the clip the model rendered.
// Only minimax/h3-max/reference-to-video states it ("Billing uses the
// requested output duration") — and its schema warns that "the output can run
// up to about 0.7 s longer than requested", which is exactly the gap the
// container was measuring (conv_3e8e49c2839590e100f5128c: a 10s request
// rendered a 10.144s container). The sibling pages say only "per second of
// video generated", so they keep the rendered clip.
func falBillsRequestedDuration(model string) bool {
	return strings.TrimSpace(model) == "minimax/h3-max/reference-to-video"
}

// falVideoRequestedSeconds resolves the duration a request-duration-billed
// endpoint charges: the value the resolver actually put on the wire — after
// its enum snap and range clamp, which is why the body is read rather than
// the canonical request — falling back to the model's declared default when
// the call sent none (fal then bills that default). ok is false when neither
// is known, and the caller keeps the rendered clip's own seconds.
func falVideoRequestedSeconds(schema *ModelInputSchema, body map[string]any, ov Overrides, model string) (float64, bool) {
	if schema != nil && body != nil {
		if path, _, ok := findNative(schema, ov, "video", model, "duration"); ok {
			if seconds, ok := bodyNumberAtPath(body, path); ok && seconds > 0 {
				return seconds, true
			}
		}
	}
	if seconds := schemaNumberDefault(schema, "duration"); seconds > 0 {
		return seconds, true
	}
	return 0, false
}

// bodyNumberAtPath reads a numeric value from a resolved request body at a
// dot-path (setBodyPath's write shape): a JSON number, a Go integer, or a
// numeric string. Absent and non-numeric values are not-ok.
func bodyNumberAtPath(body map[string]any, path string) (float64, bool) {
	parts := strings.SplitN(path, ".", 2)
	value, ok := body[parts[0]]
	if !ok {
		return 0, false
	}
	if len(parts) == 2 {
		nested, ok := value.(map[string]any)
		if !ok {
			return 0, false
		}
		if value, ok = nested[parts[1]]; !ok {
			return 0, false
		}
	}
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		return parsed, err == nil
	}
	return 0, false
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
	width, height, ok := mediasniff.MP4VideoDimensions(output)
	if !ok {
		return 0
	}
	frames := 0.0
	if count, ok := mediasniff.MP4VideoFrameCount(output); ok && len(req.SourceVideos()) == 0 {
		frames = count
	} else {
		frames = schemaNumberDefault(schema, "num_frames")
	}
	if frames <= 0 {
		return 0
	}
	return width * height * frames / 1e6
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
		if width, height, ok := mediasniff.ImagePixelDimensions(data); ok {
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

// falVideoBilledSeconds resolves the output duration for pricing: the
// generated clip's own container is exact (including "auto" durations the
// request never stated); the requested duration is the fallback when the
// container can't be parsed.
func falVideoBilledSeconds(output []byte, requested string) (float64, bool) {
	if seconds, ok := mediasniff.MP4Duration(output); ok {
		return seconds, true
	}
	return falDurationSeconds(requested)
}

// falLipsyncBillingSeconds resolves the quantity a per-minute lipsync model
// bills (sync-lipsync prices per minute of output video): the generated
// clip's own container is exact — fal charges the video it actually rendered —
// and the driving audio's duration stands in when the container won't parse,
// since lipsync output tracks the audio's length. The audio rides as a data
// URL when the gateway holds the request, so the sniff happens before
// resolveMediaURLHosted swaps it for a hosted URL whose bytes are no longer
// local. ok is false when neither source is measurable: the cost is skipped
// rather than guessed, the shared fail-soft rule
// (conv_d5a03dfe20e5225352212c2b priced a sync-lipsync v2 call "?" until the
// unit and this quantity existed).
func falLipsyncBillingSeconds(output []byte, audio string) (float64, bool) {
	if seconds, ok := mediasniff.MP4Duration(output); ok {
		return seconds, true
	}
	return mediasniff.DataURLAudioDuration(audio)
}

// estimateFalGenerationCost is the App-level entry the tool gateway calls
// after a successful fal generation. Nil-receiver safe (tests build bare App
// values) and returns 0 whenever pricing cannot be resolved.
//
// fal's own charge for the request wins when the call site knows its request
// id (hints.RequestID) and the configured key can read billing; the estimate
// serves every other case, so a refused lookup changes nothing but accuracy.
func (a *App) estimateFalGenerationCost(ctx context.Context, config AppConfig, endpointID string, hints falBillingHints) int64 {
	if a == nil || a.client == nil || a.falPricing == nil {
		return 0
	}
	apiKey, err := loadFalAPIKey()
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return 0
	}
	if cost, ok := a.falPricing.reportedCostMicros(ctx, a.client, apiKey, hints.RequestID); ok {
		return cost
	}
	return estimateFalCostMicros(ctx, a.falPricing, a.client, apiKey, endpointID, configuredFalEndpointIDs(config, endpointID), hints)
}
