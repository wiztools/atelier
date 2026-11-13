package main

// runware_client.go is the Runware media backend — a cloud provider for
// generate_image routed by imageGenerationProvider, and for the video family
// (generate_video, upscale/restyle), lipsync, speech, and sound routed by
// videoGenerationProvider / resolveSpeechProvider / resolveAudioProvider /
// resolveLipsyncProvider when the matching Models.*Provider is "runware".
// extend_audio, generate_voice, and voice cloning stay on the other
// providers: Runware publishes no input-audio continuation and no voice
// design or cloning endpoint.
//
// The wire contract differs from both older backends in three ways that
// shape this file:
//
//   - One endpoint (POST /v1) takes a JSON ARRAY of tasks, each naming its
//     taskType (imageInference / videoInference / audioInference) and a
//     client-minted taskUUID (UUIDv4); results come back in a top-level
//     `data` array keyed by that UUID, failures in `errors`.
//   - Cost is REPORTED, not estimated: includeCost:true puts the USD amount
//     actually deducted on each result, so the activity carries real cost —
//     no pricing-API estimate (fal) and no CostUnknown (Replicate).
//   - Images ride outputType:"dataURI" so the bytes come inline. Video and
//     long audio tasks outlive the sync window (120s): the API then answers
//     with a "processing" row and the task finishes server-side, so the
//     transports poll getResponse by the SAME taskUUID until success —
//     resending would start a second paid generation. There is no cancel
//     API, so CancelMediaJob is a no-op and the taskUUID recorded in the
//     mediaJob before the submit posts is what timeout recovery polls.
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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	runwareAPIURL       = "https://api.runware.ai/v1"
	runwareModelsIndex  = "https://runware.ai/docs/models/index.json"
	runwareDocsBaseURL  = "https://runware.ai/docs/models/"
	runwareTimeout      = 3 * time.Minute
	runwareMaxResponse  = 64 << 20 // inline dataURI images are big
	runwareMaxIndexSize = 8 << 20
	// defaultRunwareImageModel is FLUX.1 [dev]: pure text-to-image (the only
	// tag its catalog entry carries). It held the single image slot until that
	// split — Kontext displaced it as the t2i default because its seedImage is
	// plain image-to-image and a live "make it a watercolor" edit came back as
	// a re-rolled photo — and it returns as the generation default now that
	// edits have their own slot.
	defaultRunwareImageModel = "runware:101@1"
	// defaultRunwareImageEditModel is FLUX.1 Kontext [dev]: the instruction
	// editor the single-slot era defaulted to, kept as the image-to-image
	// default (it edits through referenceImages). The edit slot's catalog
	// filter is edit + image-to-image, and every Runware model declares its
	// own image inputs in its schema, so an attachment rides whichever input
	// the pinned editor declares.
	defaultRunwareImageEditModel = "runware:106@1"
	// The video-family, lipsync, speech, and sound defaults are every one a
	// live model whose schema was read before it was pinned (2026-11): Kling
	// 4.0 serves text-to-video AND image-to-video (inputs.frameImages), Veo
	// 3.1 is the extend model (its inputs.video is the continuation source
	// and the output length is its own), FLUX 3 Upscaler and FLUX Video Edit
	// [fast] are the two video transforms the catalog serves, KlingAI
	// Lip-Sync and VEED Fabric 1.0 are the video-face and image-face lipsync
	// models, MiniMax Speech 2.8 the voice (a ~300-voice catalog enum, no
	// cloning), and Mirelo SFX 1.5 both sound models (text-to-audio, and the
	// video soundtrack that answers MP4 with the sound mixed in).
	defaultRunwareVideoModel        = "klingai:kling-video@4.0"
	defaultRunwareVideoImageModel   = "klingai:kling-video@4.0"
	defaultRunwareVideoExtendModel  = "google:3@2"
	defaultRunwareVideoUpscaleModel = "bfl:flux@video-upscale"
	defaultRunwareVideoRestyleModel = "bfl:flux@video-edit-fast"
	// defaultRunwareImageUpscaleModel is Real-ESRGAN: the same ESRGAN family
	// fal's default upscaler (fal-ai/esrgan) and Replicate's
	// (nightmareai/real-esrgan) speak, so the house default travels with the
	// provider — and its enum [2,4] upscaleFactor matches the tool's 2x/4x
	// vocabulary exactly.
	defaultRunwareImageUpscaleModel      = "runware:504@1"
	defaultRunwareInpaintModel           = "bfl:1@2"
	defaultRunwareLipsyncImageModel      = "veed:fabric@1.0-hosted"
	defaultRunwareLipsyncVideoModel      = "klingai:7@1"
	defaultRunwareSpeechModel            = "minimax:speech@2.8"
	defaultRunwareSoundEffectsModel      = "mirelo:1@1"
	defaultRunwareSoundEffectsVideoModel = "mirelo:1@1"
	// defaultRunwareTranscribeModel is the transcription default — Runware
	// serves speech-to-text through its Gemini text-inference models (audio
	// inputs on a text task), not a dedicated STT engine: plain text comes
	// back, with no timestamp cues, no language dial, and translate folded
	// into the prompt. 3.5 Flash is the newest mid-tier model of the four
	// verified ones (the picker lists all; the ids are deliberately pinned —
	// the family mixes ':' and '@' spellings, so a mechanical derivation
	// would guess wrong).
	defaultRunwareTranscribeModel = "google:gemini@3.5-flash"
)

// runwareTextInferenceDocIDs maps the verified text-inference models' API ids
// to the docs id their schema.json lives under. The models index drops these
// entries entirely (their `air` field is null — text tasks are addressed by
// the model id alone), so GetModelInputSchema's AIR scan cannot find them and
// this table is the only honest bridge. Each pair was verified by fetching
// the schema and reading its `model` const (2026-11); a model outside the
// table has no schema and rides the nil-schema path (universal fields only).
var runwareTextInferenceDocIDs = map[string]string{
	"google:gemini:3-flash":        "google-gemini-3-flash",
	"google:gemini@3.1-flash-lite": "google-gemini-3-1-flash-lite",
	"google:gemini@3.1-pro":        "google-gemini-3-1-pro",
	"google:gemini@3.5-flash":      "google-gemini-3-5-flash",
}

var errRunwareKeyNotConfigured = errors.New("runware api key is not configured")

type RunwareClient struct {
	http    *http.Client
	apiKey  string
	baseURL string
	// indexURL and schemaURL are test seams; production reads the docs site.
	indexURL string
	// jobSink mirrors the fal and Replicate clients: the gateway points it at
	// the mediaJob handle it owns, and runTask records the submitted task's
	// UUID before posting — the UUID is client-minted, so recording early
	// means even a timeout inside the submit call itself (the task accepted
	// server-side anyway) can be recovered by polling.
	jobSink *mediaJob
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
	return c.ResolveMediaInputs(ctx, images, "source image")
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
		// The text-inference models are absent from the index (their `air`
		// field is null), so the verified table bridges an API model id to
		// its docs id; anything else is genuinely unknown.
		if docID := runwareTextInferenceDocIDs[air]; docID != "" {
			schemaURL = runwareDocsBaseURL + docID + "/schema.json"
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

// --- video, audio, and lipsync transports ---

const (
	// Video tasks are asynchronous by default on every video model the app
	// drives; they run under the caller's (runMediaGeneration's) deadline,
	// never under the 3-minute sync window image tasks get.
	runwareVideoMaxBytes = 1 << 30
	runwareAudioMaxBytes = 256 << 20
	// runwarePollStart is the first getResponse delay; it grows 1.5× per
	// poll up to runwarePollMax. Runware's own polling guidance says to
	// start at 1–2s and back off; a video that misses the sync window has
	// minutes left, so the curve is unremarkable either way.
	runwarePollStart = 2 * time.Second
	runwarePollMax   = 8 * time.Second
)

// runwareMediaResult is one `data` row from any inference task — image,
// video, or audio — plus the async status fields. A row whose Status is
// "processing" means the task outlived the sync window and keeps running
// server-side; poll getResponse by TaskUUID. Failures arrive in the
// envelope's errors array (which do() turns into an error) but a belt
// status check catches them in-band too.
type runwareMediaResult struct {
	TaskType string `json:"taskType"`
	TaskUUID string `json:"taskUUID"`
	Status   string `json:"status"`
	Progress *int   `json:"progress"`

	ImageUUID       string `json:"imageUUID"`
	ImageURL        string `json:"imageURL"`
	ImageDataURI    string `json:"imageDataURI"`
	ImageBase64Data string `json:"imageBase64Data"`

	VideoUUID       string `json:"videoUUID"`
	VideoURL        string `json:"videoURL"`
	VideoDataURI    string `json:"videoDataURI"`
	VideoBase64Data string `json:"videoBase64Data"`

	AudioUUID       string `json:"audioUUID"`
	AudioURL        string `json:"audioURL"`
	AudioDataURI    string `json:"audioDataURI"`
	AudioBase64Data string `json:"audioBase64Data"`

	// Text/FinishReason are the textInference row's payload — the
	// transcription path reads Text; an empty one means the model answered
	// nothing.
	Text         string `json:"text"`
	FinishReason string `json:"finishReason"`

	Seed *int64   `json:"seed"`
	Cost *float64 `json:"cost"`
}

// runwareRowHasPayload reports whether a data row carries anything
// deliverable: any media field of any kind, or a textInference answer.
func runwareRowHasPayload(row runwareMediaResult) bool {
	return row.VideoURL != "" || row.VideoDataURI != "" || row.VideoBase64Data != "" ||
		row.ImageURL != "" || row.ImageDataURI != "" || row.ImageBase64Data != "" ||
		row.AudioURL != "" || row.AudioDataURI != "" || row.AudioBase64Data != "" ||
		strings.TrimSpace(row.Text) != ""
}

// runwareRowDone reports whether a data row carries a finished result: the
// explicit success status, or a sync row with no status field at all — the
// shape image results have always had — that actually carries a payload. The
// payload requirement is the async-ack guard: deliveryMethod defaults to
// async on Runware's video packagings, and a model that answers the submit
// with a bare acknowledgment row (taskType and taskUUID, no status, no media)
// must read as "still running" and drop into the getResponse poll rather than
// as a finished result whose empty fields become "returned no media"
// (conv_2abf9ca68c1bfb009f303967: MiniMax H3 acked both video submits within
// seconds and the tool reported failure while the generations kept running
// server-side).
func runwareRowDone(row runwareMediaResult) bool {
	if strings.EqualFold(row.Status, "success") {
		return true
	}
	return row.Status == "" && runwareRowHasPayload(row)
}

// runwareNoMediaError names a finished row's own fields. A task that completed
// without delivering media is a provider-side outcome the planner must see —
// a finish reason, a zero-output result — and the old bare "returned no
// media" gave the adjusted-retry guidance nothing to act on.
func runwareNoMediaError(row runwareMediaResult) error {
	detail := fmt.Sprintf("status %q", row.Status)
	if reason := strings.TrimSpace(row.FinishReason); reason != "" {
		detail += ", finishReason " + reason
	}
	if row.Cost != nil {
		detail += fmt.Sprintf(", reported cost $%.4f", *row.Cost)
	}
	return fmt.Errorf("runware task finished without media (%s)", detail)
}

// runwareRowFor finds the data row matching taskUUID. A foreign UUID is
// never harvested — the endpoint returns one row per task in the batch.
func runwareRowFor(decoded runwareResponse, taskUUID string) (runwareMediaResult, bool) {
	for _, raw := range decoded.Data {
		var row runwareMediaResult
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		if row.TaskUUID != "" && taskUUID != "" && row.TaskUUID != taskUUID {
			continue
		}
		return row, true
	}
	return runwareMediaResult{}, false
}

// recordRunwareJob writes the task UUID into the caller's mediaJob handle —
// called before the task posts, since the UUID is minted here and is the
// only thing timeout recovery needs.
func (c *RunwareClient) recordRunwareJob(taskUUID string) {
	if c.jobSink != nil {
		c.jobSink.record(mediaJob{Provider: "runware", ID: taskUUID})
	}
}

// runTask submits one task and delivers its finished row. When the sync
// window closes with the task still running ("processing" row), it polls
// getResponse by the same taskUUID — resending would start a second paid
// generation, so a poll loop is the only correct wait.
func (c *RunwareClient) runTask(ctx context.Context, task map[string]any) (runwareMediaResult, error) {
	taskUUID, _ := task["taskUUID"].(string)
	if taskUUID == "" {
		taskUUID = runwareTaskUUID()
		task["taskUUID"] = taskUUID
	}
	c.recordRunwareJob(taskUUID)
	decoded, err := c.do(ctx, []map[string]any{task})
	if err != nil {
		return runwareMediaResult{}, err
	}
	if row, ok := runwareRowFor(decoded, taskUUID); ok && runwareRowDone(row) {
		return row, nil
	}
	return c.waitForTask(ctx, taskUUID)
}

// waitForTask polls getResponse until the task reports success, moves into
// the errors array (do() surfaces that as an error), or the caller's context
// deadline ends the wait — the runMediaGeneration timeout is the intended
// bound, and its recovery path polls the same UUID.
func (c *RunwareClient) waitForTask(ctx context.Context, taskUUID string) (runwareMediaResult, error) {
	delay := runwarePollStart
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return runwareMediaResult{}, ctx.Err()
		case <-timer.C:
		}
		decoded, err := c.do(ctx, []map[string]any{{"taskType": "getResponse", "taskUUID": taskUUID}})
		if err != nil {
			return runwareMediaResult{}, err
		}
		row, ok := runwareRowFor(decoded, taskUUID)
		if ok && runwareRowDone(row) {
			return row, nil
		}
		if ok && strings.EqualFold(row.Status, "error") {
			return runwareMediaResult{}, fmt.Errorf("runware request failed: task %s reported an error", taskUUID)
		}
		delay = time.Duration(float64(delay) * 1.5)
		if delay > runwarePollMax {
			delay = runwarePollMax
		}
	}
}

// GenerateVideo submits an already-native videoInference task (built by
// resolveRunwareVideoTask and its upscale/restyle/lipsync siblings) and
// downloads the result. Inline delivery is never requested on video — the
// models declare URL output — so the URL is fetched immediately; nothing
// may depend on Runware's retention window.
func (c *RunwareClient) GenerateVideo(ctx context.Context, task map[string]any) (GeneratedVideo, error) {
	row, err := c.runTask(ctx, task)
	if err != nil {
		return GeneratedVideo{}, err
	}
	if row.VideoDataURI == "" && row.VideoBase64Data == "" && row.VideoURL == "" {
		return GeneratedVideo{}, runwareNoMediaError(row)
	}
	data, err := c.mediaBytes(ctx, row.VideoDataURI, row.VideoBase64Data, row.VideoURL, runwareVideoMaxBytes)
	if err != nil {
		return GeneratedVideo{}, err
	}
	if !isVideoBytes(data) {
		return GeneratedVideo{}, errors.New("runware returned an unsupported video")
	}
	return GeneratedVideo{
		Data:       data,
		MimeType:   runwareMediaMimeType(data, row.VideoURL, "video/mp4"),
		SourceURL:  strings.TrimSpace(row.VideoURL),
		CostMicros: runwareCostMicros(row.Cost),
	}, nil
}

// GenerateAudio submits an already-native audioInference task (speech or
// sound; the SFX sibling reuses it) and delivers the bytes. Speech and
// sound models declare dataURI output, so the bytes usually ride inline —
// mirelo's video-soundtrack branch answers with MP4 bytes on the same
// audio* fields, which the SFX caller separates by sniffing.
func (c *RunwareClient) GenerateAudio(ctx context.Context, task map[string]any) (GeneratedAudio, error) {
	row, err := c.runTask(ctx, task)
	if err != nil {
		return GeneratedAudio{}, err
	}
	if row.AudioDataURI == "" && row.AudioBase64Data == "" && row.AudioURL == "" {
		return GeneratedAudio{}, runwareNoMediaError(row)
	}
	data, err := c.mediaBytes(ctx, row.AudioDataURI, row.AudioBase64Data, row.AudioURL, runwareAudioMaxBytes)
	if err != nil {
		return GeneratedAudio{}, err
	}
	if !isAudioBytes(data) && !isVideoBytes(data) {
		return GeneratedAudio{}, errors.New("runware returned an unsupported audio clip")
	}
	return GeneratedAudio{
		Data:       data,
		MimeType:   runwareMediaMimeType(data, row.AudioURL, "audio/mpeg"),
		SourceURL:  strings.TrimSpace(row.AudioURL),
		CostMicros: runwareCostMicros(row.Cost),
	}, nil
}

// GenerateSFX submits a video-soundtrack audioInference task and carries the
// result on whichever side filled: mirelo mixes the sound INTO the clip and
// answers MP4 bytes on the audio* fields (Video side), while the same model
// without a video input returns a standalone soundtrack (Audio side).
func (c *RunwareClient) GenerateSFX(ctx context.Context, task map[string]any) (SFXGenerateResult, error) {
	audio, err := c.GenerateAudio(ctx, task)
	if err != nil {
		return SFXGenerateResult{}, err
	}
	if isVideoBytes(audio.Data) {
		return SFXGenerateResult{Video: GeneratedVideo{
			Data:       audio.Data,
			MimeType:   audio.MimeType,
			SourceURL:  audio.SourceURL,
			CostMicros: audio.CostMicros,
		}}, nil
	}
	return SFXGenerateResult{Audio: audio}, nil
}

// UpscaleImage submits an already-native upscale task (built by
// resolveRunwareImageUpscaleTask) and packs the result the way GenerateImage
// does: a data URL on ollamaGenerateResponse with the reported cost in
// CostMicros. The transport is runTask rather than GenerateImage's bare
// submit because an upscale of a large source can outlive the sync window
// like a video task — the "processing" row then needs the getResponse poll,
// and resending would start a second paid generation.
func (c *RunwareClient) UpscaleImage(ctx context.Context, task map[string]any) (ollamaGenerateResponse, error) {
	row, err := c.runTask(ctx, task)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	return runwareImageResponse(ctx, c, row)
}

// runwareImageResponse packs a finished row's image payload the way every
// image backend delivers: inline data URI first, bare base64 wrapped, else
// the output URL fetched now so the artifact never depends on Runware's
// retention window.
func runwareImageResponse(ctx context.Context, c *RunwareClient, row runwareMediaResult) (ollamaGenerateResponse, error) {
	image := strings.TrimSpace(row.ImageDataURI)
	if image == "" && strings.TrimSpace(row.ImageBase64Data) != "" {
		image = falImageURL(row.ImageBase64Data)
	}
	if image == "" && strings.TrimSpace(row.ImageURL) != "" {
		fetched, err := fetchImageAsDataURL(ctx, c.http, row.ImageURL)
		if err != nil {
			return ollamaGenerateResponse{}, fmt.Errorf("runware image download failed: %w", err)
		}
		image = fetched
	}
	if image == "" {
		return ollamaGenerateResponse{}, errors.New("runware returned no image")
	}
	return ollamaGenerateResponse{
		Done:       true,
		Images:     []string{image},
		Image:      image,
		CostMicros: runwareCostMicros(row.Cost),
	}, nil
}

// TranscribeAudio submits an already-native textInference task (built by
// resolveRunwareTranscribeTask) and returns the plain transcript. Runware's
// speech-to-text rides its Gemini text models: the answer is `text` and a
// reported cost — no cue track exists to parse.
func (c *RunwareClient) TranscribeAudio(ctx context.Context, task map[string]any) (GeneratedTranscript, error) {
	row, err := c.runTask(ctx, task)
	if err != nil {
		return GeneratedTranscript{}, err
	}
	if strings.TrimSpace(row.Text) == "" {
		return GeneratedTranscript{}, errors.New("runware returned an empty transcript")
	}
	return GeneratedTranscript{
		Text:       row.Text,
		CostMicros: runwareCostMicros(row.Cost),
	}, nil
}

// mediaBytes materializes a result row's payload: inline data URI / bare
// base64 when the model honored them, else the output URL fetched now —
// results are retained only for the ttl window, so nothing may wait.
func (c *RunwareClient) mediaBytes(ctx context.Context, dataURI, base64Data, url string, maxBytes int64) ([]byte, error) {
	if s := strings.TrimSpace(dataURI); s != "" {
		decoded, _, err := decodeMediaDataURL(s)
		return decoded, err
	}
	if s := strings.TrimSpace(base64Data); s != "" {
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("runware result base64 unreadable: %w", err)
		}
		return decoded, nil
	}
	if s := strings.TrimSpace(url); s != "" {
		return c.downloadMedia(ctx, s, maxBytes)
	}
	return nil, errors.New("runware returned no media")
}

// downloadMedia fetches a generated result URL and returns its raw bytes —
// video and audio alike, since the caller validates the kind it expects.
func (c *RunwareClient) downloadMedia(ctx context.Context, mediaURL string, maxBytes int64) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("runware result download failed: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

// runwareMediaMimeType names a downloaded payload: the response Content-Type
// when it is media-shaped, else a magic-byte detection, else the caller's
// kind default. The download path drops headers, so the sniff matters.
func runwareMediaMimeType(data []byte, url, fallback string) string {
	mimeType := strings.TrimSpace(mediaTypeForURL(url))
	if !strings.Contains(mimeType, "/") {
		mimeType = ""
	}
	if detected := http.DetectContentType(data); strings.HasPrefix(detected, "video/") || strings.HasPrefix(detected, "audio/") {
		if mimeType == "" {
			mimeType = detected
		}
	}
	if mimeType == "" {
		mimeType = fallback
	}
	return mimeType
}

// mediaTypeForURL reads an extension off a result URL — vm.runware.ai paths
// end in the container (.mp4/.mp3/...), which is more specific than a sniff.
func mediaTypeForURL(url string) string {
	if url == "" {
		return ""
	}
	ext := strings.ToLower(path.Ext(url))
	return mediaTypeForExtension(ext)
}

// runwareCostMicros rounds a reported USD cost into millionths.
func runwareCostMicros(cost *float64) int64 {
	if cost == nil {
		return 0
	}
	return int64(math.Round(*cost * 1e6))
}

// CancelMediaJob is a no-op: Runware publishes no cancel API, so an
// abandoned task keeps running remotely and the cost is already committed.
// Returning nil (success) keeps runMediaGeneration's recovery shape
// identical to the fal and Replicate siblings.
func (c *RunwareClient) CancelMediaJob(ctx context.Context, job mediaJob) error {
	return nil
}

// RecoverVideoJob resolves a timed-out generation with one getResponse poll:
// success delivers the finished bytes, failure reports the task's end, and
// "still processing" errors — runMediaGeneration then reports that the job
// could not be confirmed (its no-op cancel can't stop it either).
func (c *RunwareClient) RecoverVideoJob(ctx context.Context, job mediaJob) (GeneratedVideo, error) {
	row, err := c.pollTaskOnce(ctx, job)
	if err != nil {
		return GeneratedVideo{}, err
	}
	data, err := c.mediaBytes(ctx, row.VideoDataURI, row.VideoBase64Data, row.VideoURL, runwareVideoMaxBytes)
	if err != nil {
		return GeneratedVideo{}, err
	}
	return GeneratedVideo{
		Data:       data,
		MimeType:   runwareMediaMimeType(data, row.VideoURL, "video/mp4"),
		SourceURL:  strings.TrimSpace(row.VideoURL),
		CostMicros: runwareCostMicros(row.Cost),
	}, nil
}

// RecoverAudioJob is the audio sibling of RecoverVideoJob.
func (c *RunwareClient) RecoverAudioJob(ctx context.Context, job mediaJob) (GeneratedAudio, error) {
	row, err := c.pollTaskOnce(ctx, job)
	if err != nil {
		return GeneratedAudio{}, err
	}
	data, err := c.mediaBytes(ctx, row.AudioDataURI, row.AudioBase64Data, row.AudioURL, runwareAudioMaxBytes)
	if err != nil {
		return GeneratedAudio{}, err
	}
	return GeneratedAudio{
		Data:       data,
		MimeType:   runwareMediaMimeType(data, row.AudioURL, "audio/mpeg"),
		SourceURL:  strings.TrimSpace(row.AudioURL),
		CostMicros: runwareCostMicros(row.Cost),
	}, nil
}

// RecoverImageJob is the image sibling: one getResponse poll on the recorded
// task delivers the finished upscale when the caller's window closed while
// the task kept running server-side.
func (c *RunwareClient) RecoverImageJob(ctx context.Context, job mediaJob) (ollamaGenerateResponse, error) {
	row, err := c.pollTaskOnce(ctx, job)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	return runwareImageResponse(ctx, c, row)
}

// RecoverSFXJob reuses the audio recovery and re-splits the sides the way
// GenerateSFX does.
func (c *RunwareClient) RecoverSFXJob(ctx context.Context, job mediaJob) (SFXGenerateResult, error) {
	row, err := c.pollTaskOnce(ctx, job)
	if err != nil {
		return SFXGenerateResult{}, err
	}
	audio, err := c.mediaBytes(ctx, row.AudioDataURI, row.AudioBase64Data, row.AudioURL, runwareAudioMaxBytes)
	if err != nil {
		return SFXGenerateResult{}, err
	}
	if isVideoBytes(audio) {
		return SFXGenerateResult{Video: GeneratedVideo{
			Data:       audio,
			MimeType:   runwareMediaMimeType(audio, row.AudioURL, "video/mp4"),
			SourceURL:  strings.TrimSpace(row.AudioURL),
			CostMicros: runwareCostMicros(row.Cost),
		}}, nil
	}
	return SFXGenerateResult{Audio: GeneratedAudio{
		Data:       audio,
		MimeType:   runwareMediaMimeType(audio, row.AudioURL, "audio/mpeg"),
		SourceURL:  strings.TrimSpace(row.AudioURL),
		CostMicros: runwareCostMicros(row.Cost),
	}}, nil
}

// pollTaskOnce reads a timed-out task's current state by the UUID the
// jobSink recorded at submit time.
func (c *RunwareClient) pollTaskOnce(ctx context.Context, job mediaJob) (runwareMediaResult, error) {
	if job.Provider != "runware" || strings.TrimSpace(job.ID) == "" {
		return runwareMediaResult{}, errors.New("no runware task was recorded for this generation")
	}
	decoded, err := c.do(ctx, []map[string]any{{"taskType": "getResponse", "taskUUID": job.ID}})
	if err != nil {
		return runwareMediaResult{}, err
	}
	row, ok := runwareRowFor(decoded, job.ID)
	if !ok {
		return runwareMediaResult{}, fmt.Errorf("runware task %s is unknown to the API", job.ID)
	}
	if !runwareRowDone(row) && !strings.EqualFold(row.Status, "error") {
		return runwareMediaResult{}, fmt.Errorf("runware task %s is still processing", job.ID)
	}
	if !runwareRowHasPayload(row) {
		// A finished row with nothing in it — an explicit success that
		// delivered no media, or an error row whose fields live outside our
		// struct — names itself instead of surfacing as a bare download miss.
		return runwareMediaResult{}, runwareNoMediaError(row)
	}
	return row, nil
}

// ResolveMediaInputs uploads every source payload whose decoded size exceeds
// runwareUploadThreshold and swaps it for its mediaUUID; smaller payloads,
// hosted URLs, and UUIDs pass through. Video and audio attachments are
// routinely megabytes, so the upload-first path is the norm for them — the
// same 120s sync-window protection the image path documents. An upload
// failure keeps that source inline (the generation can still succeed) and
// is reported as a diagnostic. The returned uploads are the mediaUUIDs to
// clean up afterwards.
func (c *RunwareClient) ResolveMediaInputs(ctx context.Context, media []string, label string) (resolved, uploads, diagnostics []string) {
	resolved = make([]string, len(media))
	for i, src := range media {
		resolved[i] = src
		if runwareInlineSize(src) <= runwareUploadThreshold {
			continue
		}
		mediaUUID, err := c.UploadMedia(ctx, src)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("runware: upload of %s %d failed (%v); sent it inline", label, i+1, err))
			continue
		}
		resolved[i] = mediaUUID
		uploads = append(uploads, mediaUUID)
		diagnostics = append(diagnostics, fmt.Sprintf("runware: %s %d (%d bytes) uploaded as media %s", label, i+1, runwareInlineSize(src), mediaUUID))
	}
	return resolved, uploads, diagnostics
}
