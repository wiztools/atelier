package main

// runware_client.go is the Runware media backend — a third cloud provider for
// generate_image beside fal and Replicate, routed by imageGenerationProvider
// when Models.ImageProvider is "runware". SPIKE SCOPE: text-to-image and
// reference-image edits through one synchronous imageInference task; video,
// audio, upscale, and inpaint stay on the existing providers.
//
// The wire contract differs from both existing backends in three ways that
// shape this file:
//
//   - One endpoint (POST /v1) takes a JSON ARRAY of tasks, each naming its
//     taskType and a client-minted taskUUID (UUIDv4); results come back in a
//     top-level `data` array keyed by that UUID, failures in `errors`.
//   - Cost is REPORTED, not estimated: includeCost:true puts the USD amount
//     actually deducted on each result, so the activity carries real cost —
//     no pricing-API estimate (fal) and no CostUnknown (Replicate).
//   - outputType:"dataURI" returns the bytes inline, so nothing is
//     downloaded afterwards and no output URL can expire under us.
//
// Per-model input schemas live at runware.ai/docs/models/<id>/schema.json as
// OpenAPI 3.1 with the task under components.schemas.RequestBody.items; the
// schema cache rewraps that as a *Input component so parseModelInputSchema
// (fal_schema.go) reads it unchanged. The catalog is the static
// runware.ai/docs/models/index.json, which maps the AIR id the API takes
// (runware:106@1) to the docs id the schema URL needs (bfl-flux-1-kontext-dev).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	runwareAPIURL       = "https://api.runware.ai/v1"
	runwareModelsIndex  = "https://runware.ai/docs/models/index.json"
	runwareTimeout      = 3 * time.Minute
	runwareMaxResponse  = 64 << 20 // inline dataURI images are big
	runwareMaxIndexSize = 8 << 20
	// defaultRunwareImageModel is FLUX.1 Kontext [dev]: one model that does
	// text-to-image AND instruction edits through referenceImages, so the
	// single Runware slot serves both. FLUX.1 [dev] (runware:101@1) was the
	// first default, but its seedImage is plain image-to-image — a live
	// "make it a watercolor" edit came back as a re-rolled photo.
	defaultRunwareImageModel = "runware:106@1"
)

var errRunwareKeyNotConfigured = errors.New("runware api key is not configured")

type RunwareClient struct {
	http    *http.Client
	apiKey  string
	baseURL string
	// indexURL and schemaURL are test seams; production reads the docs site.
	indexURL string
}

func newRunwareClient(httpClient *http.Client, apiKey string) *RunwareClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &RunwareClient{http: httpClient, apiKey: strings.TrimSpace(apiKey), baseURL: runwareAPIURL, indexURL: runwareModelsIndex}
}

// runwareTaskUUID mints the UUIDv4 every task must carry.
func runwareTaskUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type runwareError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Parameter string `json:"parameter"`
	TaskUUID  string `json:"taskUUID"`
}

type runwareImageResult struct {
	TaskType        string   `json:"taskType"`
	TaskUUID        string   `json:"taskUUID"`
	ImageUUID       string   `json:"imageUUID"`
	ImageURL        string   `json:"imageURL"`
	ImageDataURI    string   `json:"imageDataURI"`
	ImageBase64Data string   `json:"imageBase64Data"`
	Seed            *int64   `json:"seed"`
	Cost            *float64 `json:"cost"`
}

type runwareResponse struct {
	Data   []json.RawMessage `json:"data"`
	Errors []runwareError    `json:"errors"`
	// The auth docs describe a singular `error` on REST; accept both shapes.
	Error *runwareError `json:"error"`
}

// do posts one task array and returns the decoded envelope. HTTP status maps
// onto the vocabulary the harness's remediation rules already match for fal
// and Replicate ("… authentication failed", "… credit exhausted", "… rate
// limited").
func (c *RunwareClient) do(ctx context.Context, tasks []map[string]any) (runwareResponse, error) {
	if c.apiKey == "" {
		return runwareResponse{}, errRunwareKeyNotConfigured
	}
	payload, err := json.Marshal(tasks)
	if err != nil {
		return runwareResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return runwareResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return runwareResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, runwareMaxResponse))
	if err != nil {
		return runwareResponse{}, err
	}
	var decoded runwareResponse
	decodeErr := json.Unmarshal(body, &decoded)
	message := runwareErrorMessage(decoded)
	if message == "" {
		message = strings.TrimSpace(string(body))
		if len(message) > 300 {
			message = message[:300]
		}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return decoded, fmt.Errorf("runware authentication failed: %s", message)
	case resp.StatusCode == http.StatusPaymentRequired:
		return decoded, fmt.Errorf("runware credit exhausted: %s", message)
	case resp.StatusCode == http.StatusTooManyRequests:
		return decoded, fmt.Errorf("runware rate limited: %s", message)
	case resp.StatusCode >= 300:
		return decoded, fmt.Errorf("runware request failed (%d): %s", resp.StatusCode, message)
	}
	if decodeErr != nil {
		return decoded, fmt.Errorf("runware returned an unreadable response: %w", decodeErr)
	}
	if len(decoded.Errors) > 0 || decoded.Error != nil {
		return decoded, fmt.Errorf("runware request failed: %s", message)
	}
	return decoded, nil
}

func runwareErrorMessage(resp runwareResponse) string {
	errs := resp.Errors
	if resp.Error != nil {
		errs = append(errs, *resp.Error)
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		text := strings.TrimSpace(e.Message)
		if text == "" {
			text = e.Code
		}
		if e.Parameter != "" {
			text += " (parameter " + e.Parameter + ")"
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "; ")
}

// GenerateImage runs one imageInference task built by
// resolveRunwareImageTask and packs the results the way every image backend
// does: data URLs on ollamaGenerateResponse, with the reported cost summed
// into CostMicros.
func (c *RunwareClient) GenerateImage(ctx context.Context, task map[string]any) (ollamaGenerateResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, runwareTimeout)
	defer cancel()
	taskUUID, _ := task["taskUUID"].(string)
	decoded, err := c.do(ctx, []map[string]any{task})
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	model, _ := task["model"].(string)
	out := ollamaGenerateResponse{Model: model, Done: true}
	var costUSD float64
	for _, raw := range decoded.Data {
		var result runwareImageResult
		if json.Unmarshal(raw, &result) != nil {
			continue
		}
		if result.TaskUUID != "" && taskUUID != "" && result.TaskUUID != taskUUID {
			continue
		}
		image := strings.TrimSpace(result.ImageDataURI)
		if image == "" && strings.TrimSpace(result.ImageBase64Data) != "" {
			image = falImageURL(result.ImageBase64Data)
		}
		if image == "" && strings.TrimSpace(result.ImageURL) != "" {
			// Only reached when a model ignores outputType; the URL is
			// fetched now so the artifact never depends on Runware's retention.
			fetched, err := fetchImageAsDataURL(ctx, c.http, result.ImageURL)
			if err != nil {
				return ollamaGenerateResponse{}, fmt.Errorf("runware image download failed: %w", err)
			}
			image = fetched
		}
		if image == "" {
			continue
		}
		out.Images = append(out.Images, image)
		if result.Cost != nil {
			costUSD += *result.Cost
		}
	}
	if len(out.Images) == 0 {
		return ollamaGenerateResponse{}, errors.New("runware returned no images")
	}
	out.Image = out.Images[0]
	out.CostMicros = int64(math.Round(costUSD * 1e6))
	return out, nil
}

// runwareUploadThreshold is the decoded size above which an input image is
// uploaded through mediaStorage and referenced by its mediaUUID instead of
// riding inline. Measured live (2026-11): inline never failed, even a 24 MB
// PNG (33.5 MB of base64), and uploading costs about the same transfer time
// — so the threshold isn't about acceptance. It protects the SYNC request's
// 120s window: inline, the transfer eats into it (the 24 MB edit spent 44s),
// and a request that outlives it comes back "processing", where a resend is a
// second paid generation. Uploading first keeps the paid request small, and a
// failed upload is free to retry.
const runwareUploadThreshold = 2 << 20

// runwareUploadTimeout bounds one upload; it runs before the paid request.
const runwareUploadTimeout = 3 * time.Minute

// isRunwareUUID reports whether s is a canonical UUID — the shape a
// mediaUUID/imageUUID takes, which Runware accepts as an image input.
func isRunwareUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// UploadMedia stores one payload (data URI, bare base64, or public URL)
// through the mediaStorage task and returns its mediaUUID.
func (c *RunwareClient) UploadMedia(ctx context.Context, media string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runwareUploadTimeout)
	defer cancel()
	decoded, err := c.do(ctx, []map[string]any{{
		"taskType": "mediaStorage", "taskUUID": runwareTaskUUID(), "operation": "upload", "media": media,
	}})
	if err != nil {
		return "", err
	}
	for _, raw := range decoded.Data {
		var result struct {
			MediaUUID string `json:"mediaUUID"`
		}
		if json.Unmarshal(raw, &result) == nil && isRunwareUUID(result.MediaUUID) {
			return result.MediaUUID, nil
		}
	}
	return "", errors.New("runware media upload returned no mediaUUID")
}

// DeleteMedia removes an uploaded payload. Best-effort cleanup after a
// generation: the source has done its job, and nothing else references it.
func (c *RunwareClient) DeleteMedia(ctx context.Context, mediaUUID string) error {
	_, err := c.do(ctx, []map[string]any{{
		"taskType": "mediaStorage", "taskUUID": runwareTaskUUID(), "operation": "delete", "media": mediaUUID,
	}})
	return err
}

// CleanupUploads deletes uploaded sources once their generation is over,
// best-effort on a fresh context: the request's may already be cancelled,
// and a failed delete costs only storage.
func (c *RunwareClient) CleanupUploads(uploads []string) {
	if len(uploads) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, id := range uploads {
		_ = c.DeleteMedia(ctx, id)
	}
}

// ResolveImageInputs uploads every source image whose decoded payload exceeds
// runwareUploadThreshold and swaps it for its mediaUUID; smaller payloads,
// hosted URLs, and UUIDs pass through. An upload failure keeps that image
// inline — the generation can still succeed — and is reported as a
// diagnostic (a mapping decision, not something the reply should announce).
// The returned uploads are the mediaUUIDs to clean up afterwards.
func (c *RunwareClient) ResolveImageInputs(ctx context.Context, images []string) (resolved, uploads, diagnostics []string) {
	resolved = make([]string, len(images))
	for i, img := range images {
		resolved[i] = img
		if runwareInlineSize(img) <= runwareUploadThreshold {
			continue
		}
		mediaUUID, err := c.UploadMedia(ctx, img)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("runware: upload of source image %d failed (%v); sent it inline", i+1, err))
			continue
		}
		resolved[i] = mediaUUID
		uploads = append(uploads, mediaUUID)
		diagnostics = append(diagnostics, fmt.Sprintf("runware: source image %d (%d bytes) uploaded as media %s", i+1, runwareInlineSize(img), mediaUUID))
	}
	return resolved, uploads, diagnostics
}

// runwareInlineSize estimates the decoded byte size of an inline payload
// (data URI or bare base64) from its base64 length; URLs and UUIDs are 0.
func runwareInlineSize(img string) int {
	img = strings.TrimSpace(img)
	if img == "" || isRunwareUUID(img) || strings.HasPrefix(img, "http://") || strings.HasPrefix(img, "https://") {
		return 0
	}
	if strings.HasPrefix(img, "data:") {
		comma := strings.IndexByte(img, ',')
		if comma < 0 {
			return 0
		}
		img = img[comma+1:]
	}
	return len(img) * 3 / 4
}

// VerifyKey is the Settings "Check Connection" ping: an authentication task
// alone, which validates the key without running inference.
func (c *RunwareClient) VerifyKey(ctx context.Context) error {
	_, err := c.do(ctx, []map[string]any{{"taskType": "authentication", "apiKey": c.apiKey}})
	return err
}

// RunwareModel is one Settings picker entry from the static models index.
type RunwareModel struct {
	ID           string   `json:"id"` // the AIR id the API takes
	DocID        string   `json:"docId"`
	DisplayName  string   `json:"displayName"`
	Creator      string   `json:"creator"`
	Capabilities []string `json:"capabilities"`
	SchemaURL    string   `json:"-"`
}

type runwareIndexEntry struct {
	ID           string   `json:"id"`
	AIR          string   `json:"air"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Creator      string   `json:"creator"`
	Capabilities []string `json:"capabilities"`
	Schema       string   `json:"schema"`
}

// runwareIndexCache holds the parsed index for the process: the schema cache
// needs it to turn an AIR id into a schema URL on every generation, and the
// index is a ~400-entry static file.
var runwareIndexCache struct {
	sync.Mutex
	models    []RunwareModel
	fetchedAt time.Time
}

const runwareIndexTTL = 12 * time.Hour

// ListModels returns the live, AIR-addressable models of the index.
func (c *RunwareClient) ListModels(ctx context.Context) ([]RunwareModel, error) {
	runwareIndexCache.Lock()
	defer runwareIndexCache.Unlock()
	if runwareIndexCache.models != nil && time.Since(runwareIndexCache.fetchedAt) < runwareIndexTTL {
		return runwareIndexCache.models, nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.indexURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("runware model index request failed (%d)", resp.StatusCode)
	}
	var entries []runwareIndexEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, runwareMaxIndexSize)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("runware model index unreadable: %w", err)
	}
	models := make([]RunwareModel, 0, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.AIR) == "" || (e.Status != "" && e.Status != "live") {
			continue
		}
		models = append(models, RunwareModel{
			ID: e.AIR, DocID: e.ID, DisplayName: e.Name, Creator: e.Creator,
			Capabilities: e.Capabilities, SchemaURL: e.Schema,
		})
	}
	sort.SliceStable(models, func(i, j int) bool { return models[i].DisplayName < models[j].DisplayName })
	runwareIndexCache.models = models
	runwareIndexCache.fetchedAt = time.Now()
	return models, nil
}

func runwareModelHasCapability(model RunwareModel, capabilities ...string) bool {
	for _, have := range model.Capabilities {
		for _, want := range capabilities {
			if have == want {
				return true
			}
		}
	}
	return false
}

// GetModelInputSchema fetches the model's schema.json and rewraps the task
// schema (components.schemas.RequestBody.items) as a "RunwareTaskInput"
// component, keeping the doc's other components for $ref resolution, so the
// shared parser's *Input lookup finds it.
func (c *RunwareClient) GetModelInputSchema(ctx context.Context, air string) ([]byte, error) {
	models, err := c.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	schemaURL := ""
	for _, m := range models {
		if m.ID == air {
			schemaURL = m.SchemaURL
			break
		}
	}
	if schemaURL == "" {
		return nil, fmt.Errorf("runware model %q is not in the models index", air)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, schemaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("runware schema request failed (%d)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, runwareMaxIndexSize))
	if err != nil {
		return nil, err
	}
	return rewrapRunwareSchema(raw)
}

func rewrapRunwareSchema(raw []byte) ([]byte, error) {
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var body struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(doc.Components.Schemas["RequestBody"], &body); err != nil || len(body.Items) == 0 {
		return nil, errors.New("runware schema has no RequestBody.items task schema")
	}
	// Keep every component for $ref resolution, but drop any whose name ends
	// in "Input": the parser picks the largest *Input, and only the task
	// schema may compete.
	schemas := map[string]json.RawMessage{}
	for name, s := range doc.Components.Schemas {
		if !strings.HasSuffix(name, "Input") {
			schemas[name] = s
		}
	}
	task := json.RawMessage(body.Items)
	if presets := runwareSizePresets(body.Items); len(presets) > 0 {
		injected, err := injectRunwareSizePresets(body.Items, presets)
		if err != nil {
			return nil, err
		}
		task = injected
	}
	schemas["RunwareTaskInput"] = task
	return json.Marshal(map[string]any{"components": map[string]any{"schemas": schemas}})
}

// runwareSizePresetsProperty is a synthetic property the rewrap injects to
// carry a model's fixed output sizes through the shared parser, which keeps
// only properties. FLUX.1 Kontext accepts exactly a list of width/height
// pairs, declared as allOf → if(width|height) → then.oneOf[{width:{const},
// height:{const}}]; a 64-grid size outside that list is a 400. The resolver
// never writes this key into a task — it only reads it.
const runwareSizePresetsProperty = "x-atelier-size-presets"

// runwareSizePresets collects every width/height const pair from oneOf/anyOf
// arrays anywhere in the task schema whose every branch pins both, as
// "WxH" strings in schema order.
func runwareSizePresets(task json.RawMessage) []string {
	var presets []string
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			for _, key := range []string{"oneOf", "anyOf"} {
				branches, ok := node[key].([]any)
				if !ok || len(branches) == 0 {
					continue
				}
				pairs := make([]string, 0, len(branches))
				for _, b := range branches {
					w, h, ok := runwareConstSize(b)
					if !ok {
						pairs = nil
						break
					}
					pairs = append(pairs, fmt.Sprintf("%dx%d", w, h))
				}
				for _, p := range pairs {
					if !seen[p] {
						seen[p] = true
						presets = append(presets, p)
					}
				}
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	var root any
	if json.Unmarshal(task, &root) == nil {
		walk(root)
	}
	return presets
}

func runwareConstSize(branch any) (int, int, bool) {
	node, _ := branch.(map[string]any)
	props, _ := node["properties"].(map[string]any)
	read := func(name string) (int, bool) {
		p, _ := props[name].(map[string]any)
		f, ok := p["const"].(float64)
		return int(f), ok && f > 0
	}
	w, okW := read("width")
	h, okH := read("height")
	return w, h, okW && okH
}

func injectRunwareSizePresets(task json.RawMessage, presets []string) (json.RawMessage, error) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(task, &node); err != nil {
		return nil, err
	}
	var props map[string]json.RawMessage
	if err := json.Unmarshal(node["properties"], &props); err != nil || props == nil {
		return task, nil
	}
	enc, err := json.Marshal(map[string]any{"type": "string", "enum": presets})
	if err != nil {
		return nil, err
	}
	props[runwareSizePresetsProperty] = enc
	if node["properties"], err = json.Marshal(props); err != nil {
		return nil, err
	}
	out, err := json.Marshal(node)
	return out, err
}

// runwareNearestPreset picks the preset whose aspect ratio is closest to the
// request's (log-distance, so 2:1 and 1:2 are equally far from 1:1), breaking
// ties toward the area closest to the requested one.
func runwareNearestPreset(presets []string, width, height int) (int, int, bool) {
	want := math.Log(float64(width) / float64(height))
	bestW, bestH := 0, 0
	bestRatio, bestArea := math.Inf(1), math.Inf(1)
	for _, p := range presets {
		var w, h int
		if _, err := fmt.Sscanf(p, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			continue
		}
		ratio := math.Abs(math.Log(float64(w)/float64(h)) - want)
		area := math.Abs(float64(w*h - width*height))
		if ratio < bestRatio-1e-9 || (math.Abs(ratio-bestRatio) <= 1e-9 && area < bestArea) {
			bestW, bestH, bestRatio, bestArea = w, h, ratio, area
		}
	}
	return bestW, bestH, bestW > 0
}

func newRunwareSchemaCache(httpClient *http.Client, storageRoot string) *SchemaCache {
	return &SchemaCache{
		dir: filepath.Join(storageRoot, "schema-cache", "runware"),
		ttl: 7 * 24 * time.Hour,
		now: time.Now,
		fetch: func(ctx context.Context, model string) ([]byte, error) {
			// The docs are public; the key is not needed for the schema.
			return newRunwareClient(httpClient, "").GetModelInputSchema(ctx, model)
		},
	}
}

// resolveRunwareImageTask maps the canonical image request onto an
// imageInference task. Runware rejects undeclared fields
// (additionalProperties:false), so with a schema every optional field is
// written only where the schema declares it; without one, only the fields
// the API reference marks universal are sent.
func resolveRunwareImageTask(schema *ModelInputSchema, req ImageGenerateRequest) (map[string]any, []string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, nil, errors.New("runware image generation needs a prompt")
	}
	task := map[string]any{
		"taskType":       "imageInference",
		"taskUUID":       runwareTaskUUID(),
		"model":          req.Model,
		"positivePrompt": prompt,
		"numberResults":  1,
		"outputType":     "dataURI",
		"outputFormat":   "PNG",
		"includeCost":    true,
	}
	var notices []string
	declares := func(name string) bool {
		if schema == nil {
			return true
		}
		_, ok := schema.property(name)
		return ok
	}
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}

	// A native resolution tier and explicit pixels are mutually exclusive on
	// the models that declare both (Seedream's schema: not(resolution AND
	// (width OR height))). An explicitly requested tier wins; otherwise the
	// pixels ride, since they carry the aspect ratio. The configured default
	// tier alone rides only when the model takes no pixels at all.
	tierSent := false
	if tier := normalizeImageResolutionTier(req.Resolution); tier != "" && schema != nil {
		_, hasPixels := schema.property("width")
		if prop, ok := schema.property("resolution"); ok && (req.ResolutionExplicit || !hasPixels) {
			if valueAllowedByEnum(prop, strings.ToUpper(tier)) {
				task["resolution"] = strings.ToUpper(tier)
				tierSent = true
			}
		}
	}

	width, height := req.Width, req.Height
	if width <= 0 || height <= 0 {
		width, height = 1024, 1024
	}
	if !tierSent && declares("width") && declares("height") {
		w := runwareSnapDimension(schema, "width", width)
		h := runwareSnapDimension(schema, "height", height)
		if prop, ok := schema.property(runwareSizePresetsProperty); ok && len(prop.Enum) > 0 {
			if pw, ph, ok := runwareNearestPreset(prop.Enum, width, height); ok {
				w, h = pw, ph
			}
		}
		task["width"], task["height"] = w, h
		// Speak up only when the SHAPE changed: a preset or grid size a little
		// larger or smaller than asked is the model's normal rounding, and a
		// notice on every call would teach the reader to ignore them.
		aspectDrift := math.Abs(math.Log(float64(w)/float64(h)) - math.Log(float64(width)/float64(height)))
		if aspectDrift > runwareAspectNoticeTolerance && (req.Width > 0 || req.Height > 0) {
			notices = append(notices, fmt.Sprintf("Runware model %q only renders certain sizes; generated at %dx%d instead of %dx%d.", req.Model, w, h, width, height))
		}
	}
	if req.Steps > 0 && schema != nil {
		if prop, ok := schema.property("steps"); ok {
			steps := req.Steps
			if prop.Maximum != nil && float64(steps) > *prop.Maximum {
				steps = int(*prop.Maximum)
			}
			task["steps"] = steps
		}
	}

	// Source images ride the model's declared inputs object: a reference
	// array for edit models (Kontext, Seedream, Qwen edit), else a single
	// seedImage for image-to-image (FLUX dev). Runware accepts data URIs
	// inline, so nothing is uploaded first.
	images := make([]string, 0, len(req.Images))
	for _, img := range req.Images {
		img = strings.TrimSpace(img)
		if isRunwareUUID(img) {
			// An uploaded mediaUUID rides as-is; falImageURL would wrap it as
			// base64.
			images = append(images, img)
			continue
		}
		if u := falImageURL(img); u != "" {
			images = append(images, u)
		}
	}
	if len(images) > 0 {
		inputs := map[string]any{}
		var nested map[string]SchemaProperty
		if schema != nil {
			if prop, ok := schema.property("inputs"); ok {
				nested = prop.Nested
			}
		}
		switch {
		case nested == nil && schema != nil:
			return nil, nil, fmt.Errorf("Runware model %q takes no input images; pick an edit-capable Runware model for image edits", req.Model)
		case nested == nil || hasKey(nested, "referenceImages"):
			if prop, ok := nested["referenceImages"]; ok && prop.MaxItems > 0 && len(images) > prop.MaxItems {
				notices = append(notices, fmt.Sprintf("Runware model %q takes at most %d reference images; used the first %d.", req.Model, prop.MaxItems, prop.MaxItems))
				images = images[:prop.MaxItems]
			}
			inputs["referenceImages"] = images
		case hasKey(nested, "seedImage"):
			if len(images) > 1 {
				notices = append(notices, fmt.Sprintf("Runware model %q takes one source image; used the first of %d.", req.Model, len(images)))
			}
			inputs["seedImage"] = images[0]
		case hasKey(nested, "image"):
			inputs["image"] = images[0]
		default:
			return nil, nil, fmt.Errorf("Runware model %q declares no image input this app can fill", req.Model)
		}
		task["inputs"] = inputs
	}
	return task, notices, nil
}

func hasKey(m map[string]SchemaProperty, key string) bool {
	_, ok := m[key]
	return ok
}

// runwareAspectNoticeTolerance is how far (natural-log ratio, ~3%) a sent
// size's aspect may drift from the requested one before a notice says so.
const runwareAspectNoticeTolerance = 0.03

// runwareSnapDimension clamps a pixel size into the property's declared
// bounds and onto the 64-px grid most Runware diffusion models require
// (the parser doesn't carry multipleOf; 64 is the documented common grid,
// and a multiple of 64 also satisfies any finer grid).
func runwareSnapDimension(schema *ModelInputSchema, name string, value int) int {
	v := int(math.Round(float64(value)/64)) * 64
	if v < 64 {
		v = 64
	}
	if schema != nil {
		if prop, ok := schema.property(name); ok {
			if prop.Minimum != nil && float64(v) < *prop.Minimum {
				v = int(math.Ceil(*prop.Minimum/64)) * 64
			}
			if prop.Maximum != nil && float64(v) > *prop.Maximum {
				v = int(math.Floor(*prop.Maximum/64)) * 64
			}
		}
	}
	return v
}
