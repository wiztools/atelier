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
)

// fal media generation has no token meter — fal bills per output unit (an
// image, a second of video, a character of speech, or one request, depending
// on the model). Cost is therefore estimated at call time from fal's pricing
// API: GET {falPlatformBaseURL}/v1/models/pricing?endpoint_id=a,b,c returns
// each endpoint's unit price and billing unit, and the tool gateway multiplies
// by the quantity it knows from the request (image count, requested seconds,
// prompt length). This is an estimate, not a bill — feature multipliers fal
// applies server-side (Kling's audio toggle, resolution tiers) may not be
// captured — but it is the only fal source that attributes cost to a specific
// generation: the usage/line-items API aggregates per time bucket with no
// request IDs, so it cannot answer "what did this call cost".
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
	default:
		return 0, false
	}
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
		resolveDefaultVideoUpscaleModel,
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
