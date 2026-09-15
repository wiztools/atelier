package main

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
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
	hints := falBillingHints{Images: 4, Seconds: 7.5, Characters: 120, Requests: 1}
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
		{unit: "video", wantQty: 1, wantOK: true},
		{unit: "request", wantQty: 1, wantOK: true},
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
