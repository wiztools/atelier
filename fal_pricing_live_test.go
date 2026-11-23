package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveFalPricingAndBilling runs fal's read-only Platform API reads for real
// with the keychain's key — the gated live check, like TestLiveRunwareEndToEnd.
// It submits no generation and spends nothing. It is the check to run after
// touching fal_pricing.go, because two things there rest on how fal's API
// actually behaves and no mock can confirm them:
//
//   - the pricing API quotes each tiered endpoint at its CHEAPEST tier — the
//     documented tables (falVideoTiers) are ratios over that base, so a quote
//     above the cheapest rate means the ratio over-scales that endpoint;
//   - the billing-events feed answers a key with billing permission with the
//     charge the fal dashboard shows, which is what replaces the estimate.
//
// What a passing run cannot tell: how soon after a generation fal posts its
// event. The lookup makes one request with no retry, so a feed that posts
// minutes later always misses and falls back to the estimate — only a fresh
// paid generation shows that, and this check deliberately submits none.
//
// The key is read in-process and never logged: nothing here may route it
// through a shell pipe or print a request's headers.
//
//	ATELIER_LIVE_FAL_PRICING=1 go test -run TestLiveFalPricingAndBilling -v .
func TestLiveFalPricingAndBilling(t *testing.T) {
	if os.Getenv("ATELIER_LIVE_FAL_PRICING") == "" {
		t.Skip("set ATELIER_LIVE_FAL_PRICING=1 to run fal's read-only pricing and billing reads with the keychain key")
	}
	apiKey, err := loadFalAPIKey()
	if err != nil || apiKey == "" {
		t.Fatalf("no fal key in the keychain (err=%v)", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// liveFalPricedEndpoints are quoted one by one through the production
	// cache. The batch fal receives is all-or-nothing, so one id fal has not
	// priced fails it and every endpoint then falls back to a solo quote — the
	// production path (fal_pricing.go, fetch's merge form), exercised as is.
	priced := []string{
		"minimax/h3-max/reference-to-video",
		"minimax/h3-max/image-to-video",
		"minimax/h3-max/text-to-video",
		"minimax/h3-max/extend-video",
		"minimax/h3/text-to-video",
		"minimax/h3/image-to-video",
		"minimax/h3/reference-to-video",
		// No tier table covers these, so falVideoTiers leaves them unscaled:
		// recast and reframe-video publish one, but the pricing API quoted both
		// FLAT — $0.05 per "units" (2026-11-24) — and Kling v3's quote matched
		// none of its page's audio rates. Quoted here with no assertion, so a
		// run shows what fal makes of them today; should one come back as a
		// per-second rate, it earns a table (falVideoTiers).
		"minimax/h3-max/reframe-video",
		"minimax/h3-max/recast",
		"fal-ai/kling-video/v3/standard/text-to-video",
	}

	t.Run("pricing quotes", func(t *testing.T) {
		recorder := &liveFalRecorder{base: http.DefaultTransport}
		client := &http.Client{Transport: recorder, Timeout: 30 * time.Second}
		cache := newFalPricingCache()
		for _, endpoint := range priced {
			quote, ok := cache.quote(ctx, client, apiKey, endpoint, priced)
			if !ok {
				t.Errorf("%s: the pricing API returned no quote", endpoint)
				continue
			}
			note := ""
			if tiers := falVideoTiers(endpoint); tiers != nil {
				base := tiers[0].Rate
				switch {
				case quote.Unit != "second" && quote.Unit != "seconds":
					t.Errorf("%s: quoted $%v per %q, want a per-second rate — the tier table does not apply to this quote", endpoint, quote.UnitPrice, quote.Unit)
				case quote.UnitPrice > base+1e-9:
					t.Errorf("%s: quoted $%v/s, above the cheapest documented tier ($%v/s) — falVideoTiers' ratio over-scales this endpoint", endpoint, quote.UnitPrice, base)
				case quote.UnitPrice < base-1e-9:
					note = fmt.Sprintf("  (under the $%v/s table rate: a proportional discount or promo leaves the ratios intact)", base)
				}
			}
			t.Logf("%-44s $%v / %s%s", endpoint, quote.UnitPrice, quote.Unit, note)
		}
		// What fal sent, field for field: the parser keeps four keys per
		// price, so anything else fal publishes shows only here.
		for _, response := range recorder.responses {
			if response.status != http.StatusOK {
				continue
			}
			var payload struct {
				Prices []map[string]any `json:"prices"`
			}
			if err := json.Unmarshal(response.body, &payload); err != nil {
				continue
			}
			for _, price := range payload.Prices {
				raw, _ := json.Marshal(price)
				t.Logf("raw price: %s", raw)
			}
		}
	})

	t.Run("billing event for a request with a verified charge", func(t *testing.T) {
		// The feed caps one query at 90 days. A window 80 days back keeps the
		// pinned request inside that cap for as long as it can be reached at
		// all; past it the check needs a newer request.
		start := time.Now().Add(-80 * 24 * time.Hour)
		if start.After(liveFalKnownRequestAt) {
			t.Skip("the pinned request is past the feed's 90-day query cap — point this test at a newer request whose dashboard charge is verified")
		}
		recorder := &liveFalRecorder{base: http.DefaultTransport}
		client := &http.Client{Transport: recorder, Timeout: 30 * time.Second}
		began := time.Now()
		micros, outcome := fetchFalBillingCostSince(ctx, client, apiKey, liveFalKnownRequestID, start)
		status, body := recorder.last()

		var payload struct {
			BillingEvents []struct {
				RequestID       string   `json:"request_id"`
				EndpointID      string   `json:"endpoint_id"`
				Timestamp       string   `json:"timestamp"`
				Quantity        *float64 `json:"quantity"`
				Unit            *string  `json:"unit"`
				UnitPrice       *float64 `json:"unit_price"`
				PercentDiscount *float64 `json:"percent_discount"`
				CostSubtotal    *float64 `json:"cost_subtotal"`
				CostDiscount    *float64 `json:"cost_discount"`
				CostTotal       *float64 `json:"cost_total"`
				CostNanoUSD     *float64 `json:"cost_estimate_nano_usd"`
			} `json:"billing_events"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Error != nil {
			t.Logf("fal answered HTTP %d: %s — %s", status, payload.Error.Type, payload.Error.Message)
		}
		for _, event := range payload.BillingEvents {
			t.Logf("event: request=%s endpoint=%s at=%s quantity=%v %s at $%v/%s, discount=%v%%, subtotal=$%v discount=$%v total=$%v nano=%v",
				event.RequestID, event.EndpointID, event.Timestamp,
				derefFloat(event.Quantity), derefString(event.Unit), derefFloat(event.UnitPrice), derefString(event.Unit),
				derefFloat(event.PercentDiscount), derefFloat(event.CostSubtotal), derefFloat(event.CostDiscount),
				derefFloat(event.CostTotal), derefFloat(event.CostNanoUSD))
		}
		t.Logf("lookup: outcome=%d in %s", outcome, time.Since(began).Round(time.Millisecond))

		switch {
		case status == http.StatusUnauthorized:
			t.Fatalf("fal rejected the stored key (HTTP 401) — the key in the keychain is not valid; was the new one saved in Settings?")
		case outcome == falBillingDenied:
			t.Skipf("the key cannot read billing (HTTP %d) — the BILLING or FULL preset is required; until then the estimate serves", status)
		case outcome == falBillingFailed:
			t.Fatalf("the billing-events lookup failed (HTTP %d)", status)
		case outcome == falBillingMissing:
			t.Fatalf("the feed answered (HTTP %d) but carries no event for %s: billing has not posted it, or fal no longer retains it", status, liveFalKnownRequestID)
		}
		if micros != liveFalKnownRequestMicros {
			t.Fatalf("reported charge = %d micros, want %d — the dashboard's $%.2f for this request", micros, liveFalKnownRequestMicros, float64(liveFalKnownRequestMicros)/1e6)
		}
	})
}

// The pinned request is the conv_3e8e49c2839590e100f5128c generation, whose
// fal dashboard page showed Cost $1.60 — a 10s 1080p
// minimax/h3-max/reference-to-video turn, the turn this pricing work began
// with. fal's page is the authority on the charge; if the event ever ages out
// of the feed, replace all three with a newer request verified the same way.
const (
	liveFalKnownRequestID     = "01a20998-4f58-77f0-8aea-b1e68169e836"
	liveFalKnownRequestMicros = int64(1600000)
)

// liveFalKnownRequestAt is when fal created the pinned request — the
// dashboard's "Date Created" (2026/11/23 23:38, UTC+05:30).
var liveFalKnownRequestAt = time.Date(2026, time.November, 23, 18, 8, 0, 0, time.UTC)

// liveFalRecorder wraps a transport and keeps every response's status and
// body, so the check can report what fal actually answered without a second
// request or any change to the production path. The bodies are the pricing
// and billing payloads — public prices and this account's own charges. The key
// rides only in a request header, which this never reads.
type liveFalRecorder struct {
	base      http.RoundTripper
	responses []liveFalResponse
}

type liveFalResponse struct {
	status int
	body   []byte
}

func (recorder *liveFalRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := recorder.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	recorder.responses = append(recorder.responses, liveFalResponse{status: resp.StatusCode, body: body})
	return resp, nil
}

// last reports the most recent response — the empty response when the
// transport itself failed before one arrived.
func (recorder *liveFalRecorder) last() (int, []byte) {
	if len(recorder.responses) == 0 {
		return 0, nil
	}
	last := recorder.responses[len(recorder.responses)-1]
	return last.status, last.body
}

func derefFloat(value *float64) any {
	if value == nil {
		return "—"
	}
	return *value
}

func derefString(value *string) any {
	if value == nil {
		return "—"
	}
	return *value
}
