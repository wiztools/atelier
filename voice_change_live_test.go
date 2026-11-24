package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"atelier/internal/mediasniff"
)

// TestLiveFalVoiceChangerPricing runs fal's read-only pricing API for the
// voice-changer endpoint with the keychain's key — the
// TestLiveFalPricingAndBilling sibling for the one thing about change_voice no
// mock can confirm: the UNIT fal quotes. The estimate assumes a per-minute
// quote (the model page's "$0.3 per minute") billed in whole minutes
// (falBillsWholeMinutes), and quantityForUnit rounds the source's seconds up
// to the next full minute; any other unit leaves the activity unpriced ("?"),
// which is safe but silent — this check makes it loud. It submits no
// generation and spends nothing. The key is read in-process and never logged.
//
//	ATELIER_LIVE_FAL_PRICING=1 go test -run TestLiveFalVoiceChangerPricing -v .
func TestLiveFalVoiceChangerPricing(t *testing.T) {
	if os.Getenv("ATELIER_LIVE_FAL_PRICING") == "" {
		t.Skip("set ATELIER_LIVE_FAL_PRICING=1 to run fal's read-only pricing read with the keychain key")
	}
	apiKey, err := loadFalAPIKey()
	if err != nil || apiKey == "" {
		t.Fatalf("no fal key in the keychain (err=%v)", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	recorder := &liveFalRecorder{base: http.DefaultTransport}
	client := &http.Client{Transport: recorder, Timeout: 30 * time.Second}
	cache := newFalPricingCache()
	endpoint := defaultFalVoiceChangerModel

	quote, ok := cache.quote(ctx, client, apiKey, endpoint, []string{endpoint})
	if !ok {
		status, body := recorder.last()
		t.Fatalf("the pricing API returned no quote for %s (HTTP %d): %.300s", endpoint, status, body)
	}
	t.Logf("%s: $%v per %q", endpoint, quote.UnitPrice, quote.Unit)
	// What fal sent, field for field: the parser keeps four keys per price, so
	// anything else fal publishes (a minimum, a tier) shows only here.
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
			if raw, _ := json.Marshal(price); strings.Contains(string(raw), endpoint) {
				t.Logf("raw price: %s", raw)
			}
		}
	}
	switch strings.ToLower(quote.Unit) {
	case "minute", "minutes":
	default:
		t.Errorf("quoted per %q, want a per-minute unit: quantityForUnit prices only minute/minutes from the source's seconds, so this quote leaves every change_voice activity at \"?\"", quote.Unit)
	}

	// The estimate end to end at the live rate, through the hints the gateway
	// builds (WholeMinutes from falBillsWholeMinutes): the endpoint bills
	// whole minutes, so a three-second source prices one full minute — the
	// $0.30 fal's dashboard charged for conv_8a298a46840d3419c906c462's 10.1s
	// source, against the $0.05 the fractional minute estimated.
	hints := falBillingHints{Requests: 1, Seconds: 3, WholeMinutes: falBillsWholeMinutes(endpoint)}
	micros := estimateFalCostMicros(ctx, cache, client, apiKey, endpoint, []string{endpoint}, hints)
	t.Logf("estimate for a 3s source: $%.6f (%d micros)", float64(micros)/1e6, micros)
	if want := int64(math.Round(quote.UnitPrice * falWholeMinutes(3) * 1e6)); micros != want {
		t.Errorf("a three-second source estimated %d micros, want %d — one whole billed minute at the live rate (unit %q)", micros, want, quote.Unit)
	}
}

// TestLiveFalVoiceChange runs change_voice through the real tool gateway
// against fal with the keychain's key — the gated live check, like
// TestLiveRunwareEndToEnd, and the repo's first PAID fal generation test (the
// fal live checks until now were read-only). It spends real money: ONE
// conversion of about three seconds of speech, a whole billed minute — $0.30
// at the model page's $0.3 per minute, since fal rounds a voice change up to
// full minutes (falBillsWholeMinutes) — and a longer source costs no more
// until it passes a minute. What it confirms that no mock can:
//
//   - the gate opens on a real key, and the registry entry's schema read
//     returns the voice list the planner is offered;
//   - fal accepts the hosted upload, the body the resolver builds, and the
//     catalog voice NAME — a rejection here is the first thing fal's
//     2026-12-31 default-voice retirement would produce;
//   - the result is audio about as long as the source.
//
// What it cannot tell: whether the clip SOUNDS like the target voice. That
// needs ears, so the re-voiced clip and its source are written to
// ATELIER_LIVE_FAL_VOICE_CHANGE_OUT (a fresh temp directory when unset) for
// listening. ATELIER_LIVE_FAL_VOICE_CHANGE_IN names a speech clip to convert
// instead of the macOS `say` line this synthesizes; with neither available the
// test skips.
//
// Verified live 2026-11-24: accepted in 11.5s, delivering 2.56s of 128 kbps
// MP3 from a 2.50s source, with the 21-name voice list read from the live
// schema. The key could not read billing (HTTP 403), and the estimate that
// run recorded — $0.0125, 2.5s as a fraction of a minute — understated the
// bill: fal's dashboard charged $0.30 that day for the 10.144s source of
// conv_8a298a46840d3419c906c462, a whole minute — the same minute this
// test's ~3s conversion falls in.
//
// The key is read in-process and never logged: nothing here may route it
// through a shell pipe or print a request's headers.
//
//	ATELIER_LIVE_FAL_VOICE_CHANGE=1 go test -run TestLiveFalVoiceChange -v .
func TestLiveFalVoiceChange(t *testing.T) {
	if os.Getenv("ATELIER_LIVE_FAL_VOICE_CHANGE") == "" {
		t.Skip("set ATELIER_LIVE_FAL_VOICE_CHANGE=1 to run one paid voice change against the live fal API with the keychain key (one whole billed minute, about $0.30)")
	}
	apiKey, err := loadFalAPIKey()
	if err != nil || apiKey == "" {
		t.Fatalf("no fal key in the keychain (err=%v)", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	source := liveVoiceSample(t)
	sourceSeconds, ok := mediasniff.AudioDuration(source)
	if !ok || sourceSeconds <= 0 {
		t.Fatalf("the speech sample's length can't be read (%d bytes) — the per-minute estimate needs it", len(source))
	}
	t.Logf("source: %d bytes, %.2fs", len(source), sourceSeconds)

	app := NewApp()
	submits := &liveFalSubmitRecorder{base: http.DefaultTransport}
	app.client.Transport = submits
	// A fresh storage root keeps the schema cache and the overrides out of the
	// user's ~/.atelier. The audio provider defaults to fal and mergeAppConfig
	// seeds the slot, so this is exactly what a stock install offers.
	config := mergeAppConfig(AppConfig{Storage: ConfigStorage{Root: t.TempDir()}})
	if !voiceChangeConfigured(config) {
		t.Fatal("change_voice is not offered with a fal key on the fal audio route")
	}
	definition, ok := defaultHarnessToolRegistry(ctx, config, app).Get(voiceChangerToolName)
	if !ok {
		t.Fatal("change_voice passes its gate but is missing from the registry")
	}
	voices, fromSchema := voiceChangerVoices(ctx, config, app)
	t.Logf("voice list: %d names, read from the endpoint's schema: %v", len(voices), fromSchema)
	t.Logf("voices: %s", strings.Join(voices, ", "))

	const voice = "Daniel"
	if _, listed := canonicalVoiceName(voices, voice); !listed {
		t.Fatalf("%q is not in the voice list the endpoint publishes today: %v", voice, voices)
	}
	gateway := newToolGateway(app, config)
	gateway.tools.AttachedAudios = []string{"data:audio/wav;base64," + base64.StdEncoding.EncodeToString(source)}

	began := time.Now()
	result, summary, err := definition.Execute(ctx, gateway.tools, HarnessToolCall{Name: voiceChangerToolName, Voice: voice})
	if err != nil {
		t.Fatalf("Execute: %v (%s)", err, submits.describe())
	}
	typed, ok := result.(ToolAudioResult)
	if !ok {
		t.Fatalf("result = %T, want ToolAudioResult", result)
	}
	if typed.Count != 1 || len(typed.Audios) != 1 {
		t.Fatalf("tool result = %+v, want one re-voiced clip", typed)
	}
	// The harness deletes a staged clip once it is saved; nothing saves it here.
	t.Cleanup(func() { _ = os.Remove(typed.Audios[0].TempPath) })
	converted, err := os.ReadFile(typed.Audios[0].TempPath)
	if err != nil {
		t.Fatalf("read the delivered clip: %v", err)
	}
	t.Logf("%s in %s; model=%s mime=%s notices=%v", summary, time.Since(began).Round(time.Millisecond),
		typed.Model, typed.Audios[0].MimeType, typed.Notices)
	convertedSeconds, ok := mediasniff.AudioDuration(converted)
	if !ok {
		t.Errorf("the delivered clip (%d bytes, %s) is not audio the sniffer can read", len(converted), typed.Audios[0].MimeType)
	} else {
		ratio := convertedSeconds / sourceSeconds
		t.Logf("delivered: %d bytes, %.2fs (%.2fx the source's %.2fs)", len(converted), convertedSeconds, ratio, sourceSeconds)
		// Speech-to-speech keeps the delivery's timing; a clip far from the
		// source's length means the endpoint did something other than re-voice.
		if ratio < 0.5 || ratio > 2 {
			t.Errorf("delivered %.2fs against a %.2fs source — not a re-voicing of the clip", convertedSeconds, sourceSeconds)
		}
	}
	if typed.CostMicros <= 0 {
		t.Errorf("no cost was recorded — the activity would show \"?\"")
	} else {
		t.Logf("recorded cost: $%.6f", float64(typed.CostMicros)/1e6)
	}

	// Keep both clips for listening.
	outDir := os.Getenv("ATELIER_LIVE_FAL_VOICE_CHANGE_OUT")
	if outDir == "" {
		if outDir, err = os.MkdirTemp("", "atelier-voice-change-"); err != nil {
			t.Fatalf("create the output directory: %v", err)
		}
	} else if err = os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create the output directory: %v", err)
	}
	_ = os.WriteFile(filepath.Join(outDir, "source.wav"), source, 0o644)
	_ = os.WriteFile(filepath.Join(outDir, "changed-to-"+voice+audioExtensionForMediaType(typed.Audios[0].MimeType)), converted, 0o644)
	t.Logf("clips for listening: %s", outDir)

	// fal's own charge for the conversion: the billing feed is keyed by the
	// queue's request id. One bounded read per attempt, a few attempts apart,
	// because how soon fal posts an event after a generation is unmeasured.
	requestID := submits.lastRequestID()
	if requestID == "" {
		t.Logf("no queue request id was recorded, so fal's reported charge can't be read")
		return
	}
	billing := &liveFalRecorder{base: http.DefaultTransport}
	billingClient := &http.Client{Transport: billing, Timeout: 30 * time.Second}
	micros, outcome := int64(0), falBillingMissing
	for attempt := 1; ; attempt++ {
		micros, outcome = fetchFalBillingCostSince(ctx, billingClient, apiKey, requestID, began.Add(-time.Minute))
		if outcome != falBillingMissing || attempt >= 6 {
			break
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}
	}
	status, body := billing.last()
	switch outcome {
	case falBillingReported:
		t.Logf("fal's reported charge for request %s: $%.6f", requestID, float64(micros)/1e6)
	case falBillingDenied:
		t.Logf("the key cannot read billing (HTTP %d) — the recorded estimate stands; fal's dashboard shows the charge for request %s", status, requestID)
	case falBillingFailed:
		t.Logf("the billing-events lookup failed (HTTP %d)", status)
	default:
		t.Logf("billing had posted no event for request %s after 30s — the recorded estimate stands", requestID)
	}
	logLiveBillingEvent(t, body)
}

// liveVoiceSample returns a few seconds of real speech as WAV bytes: the clip
// ATELIER_LIVE_FAL_VOICE_CHANGE_IN names, or — on macOS — a line the built-in
// `say` synthesizes, which costs nothing and keeps the paid step to the one
// conversion. It skips the test when neither is available.
func liveVoiceSample(t *testing.T) []byte {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv("ATELIER_LIVE_FAL_VOICE_CHANGE_IN")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the speech sample %s: %v", path, err)
		}
		return data
	}
	if runtime.GOOS != "darwin" {
		t.Skip("no speech sample: set ATELIER_LIVE_FAL_VOICE_CHANGE_IN to a clip (the built-in `say` sample needs macOS)")
	}
	path := filepath.Join(t.TempDir(), "sample.wav")
	cmd := exec.Command("say", "-o", path, "--file-format=WAVE", "--data-format=LEI16@22050",
		"The quick brown fox jumps over the lazy dog.")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("no speech sample: macOS `say` could not synthesize one (%v: %s) — set ATELIER_LIVE_FAL_VOICE_CHANGE_IN to a clip", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the synthesized sample: %v", err)
	}
	return data
}

// liveFalSubmitRecorder keeps the queue's submit answers — the request id the
// billing feed is keyed by, and the body of a refusal — and touches no other
// response. It never reads a request, so the key and the storage token stay
// wherever they are.
type liveFalSubmitRecorder struct {
	base    http.RoundTripper
	submits []liveFalSubmit
}

type liveFalSubmit struct {
	path      string
	requestID string
	status    int
	body      string
}

func (recorder *liveFalSubmitRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := recorder.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Host, ".fal.run") {
		return resp, nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var decoded struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(body, &decoded)
	recorder.submits = append(recorder.submits, liveFalSubmit{
		path:      req.URL.Path,
		requestID: decoded.RequestID,
		status:    resp.StatusCode,
		body:      string(body),
	})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// lastRequestID is the queue's id for the most recent submit, empty when none
// was recorded or the submit was refused before one was issued.
func (recorder *liveFalSubmitRecorder) lastRequestID() string {
	if len(recorder.submits) == 0 {
		return ""
	}
	return recorder.submits[len(recorder.submits)-1].requestID
}

// describe reports the latest submit for a failure message. The submit answer
// carries the request id and the queue's URLs, never a credential.
func (recorder *liveFalSubmitRecorder) describe() string {
	if len(recorder.submits) == 0 {
		return "no request reached fal's queue"
	}
	last := recorder.submits[len(recorder.submits)-1]
	return fmt.Sprintf("POST %s answered HTTP %d: %.300s", last.path, last.status, last.body)
}

// logLiveBillingEvent prints the feed's one event for the request — the
// quantity and unit fal actually billed, which is the only place a minimum or
// rounding rule shows.
func logLiveBillingEvent(t *testing.T, body []byte) {
	t.Helper()
	var payload struct {
		BillingEvents []struct {
			EndpointID      string   `json:"endpoint_id"`
			Timestamp       string   `json:"timestamp"`
			Quantity        *float64 `json:"quantity"`
			Unit            *string  `json:"unit"`
			UnitPrice       *float64 `json:"unit_price"`
			PercentDiscount *float64 `json:"percent_discount"`
			CostTotal       *float64 `json:"cost_total"`
			CostNanoUSD     *float64 `json:"cost_estimate_nano_usd"`
		} `json:"billing_events"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	for _, event := range payload.BillingEvents {
		t.Logf("billing event: endpoint=%s at=%s quantity=%v %s at $%v/%s, discount=%v%%, total=$%v nano=%v",
			event.EndpointID, event.Timestamp,
			derefFloat(event.Quantity), derefString(event.Unit), derefFloat(event.UnitPrice), derefString(event.Unit),
			derefFloat(event.PercentDiscount), derefFloat(event.CostTotal), derefFloat(event.CostNanoUSD))
	}
}
