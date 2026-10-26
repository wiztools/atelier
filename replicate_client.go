package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ReplicateClient talks to Replicate's REST API (api.replicate.com) for image
// and video generation. It is deliberately not a ChatProvider — Replicate only
// generates media here — and it mirrors the FalClient shape (value type,
// injected HTTP client, token read from the OS keychain at construction time).
// The transport is Replicate's prediction lifecycle: POST a prediction, poll
// GET /v1/predictions/{id} until a terminal status, then scavenge the
// model-defined output for media URLs.
const (
	replicateAPIBaseURL = "https://api.replicate.com"
	// defaultReplicateImageModel is the text-to-image model used when none is
	// configured — the fastest Flux tier, priced per output image.
	defaultReplicateImageModel = "black-forest-labs/flux-schnell"
	// defaultReplicateImageEditModel is the image-to-image model used when the
	// user attaches a source image to transform — the edit sibling of
	// defaultReplicateImageModel (instruction-based editing with the source
	// frame attached).
	defaultReplicateImageEditModel = "black-forest-labs/flux-kontext-pro"
	// defaultReplicateVideoModel is the text-to-video model used when none is
	// configured; defaultReplicateVideoImageModel is the image-to-video sibling
	// used to animate an attached image (the first frame).
	defaultReplicateVideoModel      = "wan-video/wan-2.5-t2v"
	defaultReplicateVideoImageModel = "wan-video/wan-2.5-i2v"
	// defaultReplicateUpscaleModel is the image upscaler used when none is
	// configured — Real-ESRGAN, the collection's most-run official upscaler
	// (image + scale + optional face enhancement), the same lineage as fal's
	// default esrgan endpoint.
	defaultReplicateUpscaleModel = "nightmareai/real-esrgan"
	// defaultReplicateVideoUpscaleModel is the video upscaler used when none
	// is configured — Topaz's official video upscaler. Unlike the factor-based
	// upscalers it takes a target resolution tier (720p/1080p/4k), which
	// resolveReplicateVideoUpscaleInput derives from the source clip's own
	// frame size × the requested factor.
	defaultReplicateVideoUpscaleModel = "topazlabs/video-upscale"
	// defaultReplicateVideoRestyleModel is the video restyler used when none
	// is configured — Kling 3.0 Omni, edited via natural-language instruction
	// with up to seven reference images: the closest counterpart to fal's
	// Kling o3 video-to-video/edit, the restyle default.
	defaultReplicateVideoRestyleModel = "kwaivgi/kling-v3-omni-video"
	// defaultReplicateVideoReframeModel is the generative video reframer used
	// when none is configured — Luma's official reframe endpoint (720p, clips
	// up to 30s), the same model family fal's reframe roster carries.
	defaultReplicateVideoReframeModel = "luma/reframe-video"
	// defaultReplicateVideoExtendModel is the video-extension model used when
	// none is configured — Grok Imagine's extension endpoint, which continues
	// an attached clip (2–15s source, 2–10s added) and returns the stitched
	// original-plus-extension, the same deliverable fal's Veo extend produces.
	defaultReplicateVideoExtendModel = "xai/grok-imagine-video-extension"
	// defaultReplicateInpaintModel is the image editor's mask-inpainting model
	// when none is configured — the Replicate sibling of defaultFalInpaintModel.
	// FLUX.1 Fill [pro] (official, actively maintained): prompt + image
	// required, mask optional-but-always-sent — "Black areas will be preserved
	// while white areas will be inpainted", the same polarity Atelier's
	// canonical mask paints, so no inversion at the adapter. The mask must
	// match the image's size; the output is a single image URI. Its dev
	// sibling (flux-fill-dev) is picker-selectable but carries two size traps
	// resolveReplicateInpaintInput handles: input dims snap to multiples of 32
	// with a 1440x1440 cap, and its megapixels default is "1" — the adapter
	// sends match_input so the output tracks the source instead.
	defaultReplicateInpaintModel = "black-forest-labs/flux-fill-pro"
	// replicatePollInterval is the delay between prediction status checks,
	// matching fal's cadence.
	replicatePollInterval = 1500 * time.Millisecond
	// replicateVideoMaxBytes caps a downloaded video, matching fal's bound.
	replicateVideoMaxBytes = 256 * 1024 * 1024
	// replicateInlineMediaMaxBytes is the size above which embedded media
	// (base64 data URIs) is uploaded through Replicate's Files API before
	// submission. Replicate recommends data URIs only under ~1MB, so the
	// threshold matches fal's inline limit; larger media gets a hosted URL.
	replicateInlineMediaMaxBytes = 1024 * 1024
	// Replicate collection slugs — the curated buckets the Settings pickers
	// list, mirroring fal's /v1/models categories.
	replicateTextToImageCollection     = "text-to-image"
	replicateImageEditingCollection    = "image-editing"
	replicateTextToVideoCollection     = "text-to-video"
	replicateImageToVideoCollection    = "image-to-video"
	replicateSuperResolutionCollection = "super-resolution"
	// replicateEnhanceVideosCollection is not upscale-only — Replicate files
	// upscalers alongside face restoration, colorization, and frame
	// interpolation — so its lister post-filters for upscaler ids
	// (isReplicateVideoUpscaleModel), the fal pattern of partitioning a broad
	// category.
	replicateEnhanceVideosCollection = "ai-enhance-videos"
	// replicateVideoEditingCollection likewise mixes the restyle/reframe
	// endpoints with audio, lipsync, and utility models; its two listers
	// post-filter by id markers.
	replicateVideoEditingCollection = "video-editing"
	// replicateSpeechToTextCollection is the transcription picker's bucket.
	// It carries speech LLMs (gemini, granite) whose chat-shaped inputs the
	// canonical transcription request cannot fill, so its lister post-filters
	// them out (isReplicateTranscribeModel) the same way.
	replicateSpeechToTextCollection = "speech-to-text"
	// replicateLipsyncCollection is the lip_sync pickers' bucket. It spans
	// both face kinds (re-lip-sync a clip, a talking head from an image)
	// plus text-driven avatar generators, so its listers post-filter by
	// input schema (replicateLipsyncSchemaServes) — a model the canonical
	// request cannot drive (a required text/prompt, a text-driven speech
	// voice) never reaches the dropdown.
	replicateLipsyncCollection = "lipsync"
	// replicateMusicGenerationCollection is the generate_sound picker's
	// bucket when replicate is the AudioProvider. It is music-weighted, but
	// its non-voice entries are exactly the canonical request's domain
	// (stable-audio-2.5 ships sound effects too), so its lister only
	// post-filters voice/tts ids out — the id-marker partition, kept
	// belt-and-braces for future collection merges.
	replicateMusicGenerationCollection = "ai-music-generation"
	// replicateTextToSpeechCollection is the generate_speech picker's bucket
	// when replicate is the SpeechProvider. It mixes TTS endpoints with
	// voice-cloning utilities (minimax/voice-cloning mints a voice id from a
	// clip and speaks nothing) and chat-shaped speech LLMs, so its lister
	// post-filters by input schema (replicateSpeechSchemaServes — a scalar
	// string text input, the lipsync pickers' pattern).
	replicateTextToSpeechCollection = "text-to-speech"
	// defaultReplicateTranscribeModel is the speech-to-text model used when
	// none is configured — openai/whisper, the collection's official
	// whisper: a translate boolean, a two-letter language enum, and a
	// "plain text" | "srt" | "vtt" output-format control a timestamped
	// request sets to vtt so the returned document parses into segments.
	defaultReplicateTranscribeModel = "openai/whisper"
	// defaultReplicateLipsyncImageModel is the audio-to-video lip sync model
	// used when replicate is the LipsyncProvider and none is configured —
	// veed/fabric-1.0, the collection's official talking-head endpoint whose
	// required inputs are exactly the canonical pair (image + audio) and
	// whose only extra is a resolution enum that defaults to 720p.
	defaultReplicateLipsyncImageModel = "veed/fabric-1.0"
	// defaultReplicateLipsyncVideoModel is the video-to-video lip sync model
	// used when replicate is the LipsyncProvider and none is configured —
	// sync/lipsync-2, Sync Labs' official re-sync endpoint (the same family
	// as fal's default): required video + audio, a sync_mode enum, and an
	// expressiveness temperature, all defaulted.
	defaultReplicateLipsyncVideoModel = "sync/lipsync-2"
	// defaultReplicateSoundEffectsModel is the text-to-audio model
	// generate_sound uses when replicate is the AudioProvider and none is
	// configured — stability-ai/stable-audio-2.5, the ai-music-generation
	// collection's official music AND sound-effects endpoint (the only
	// official entry whose contract covers both of generate_sound's kinds),
	// with a ~3-minute ceiling a requested duration clamps against.
	defaultReplicateSoundEffectsModel = "stability-ai/stable-audio-2.5"
	// defaultReplicateSoundEffectsVideoModel is the video-to-sound model
	// add_sound_effects uses when replicate is the AudioProvider and none is
	// configured — zsxkib/mmaudio, the MMAudio V2 packaging: a video in, a
	// synchronized standalone soundtrack out (a GeneratedAudio, which rides
	// the executor's replace_audio chain summary). Community-hosted, so the
	// official-only collection lister seldom surfaces it — the picker falls
	// back to this entry by name, the transcribe lister's rule.
	defaultReplicateSoundEffectsVideoModel = "zsxkib/mmaudio"
	// defaultReplicateAudioExtendModel is the continuation model extend_audio
	// uses when replicate is the AudioProvider and none is configured —
	// meta/musicgen, the collection's official continuation endpoint: an
	// attached clip plus a boolean continuation flag and a TOTAL output
	// duration (source + added, clamped — the model renders ~30s at most).
	// Forward continuations only; a before-direction refuses with the fal
	// remedy, the resolveReplicateAudioExtendInput contract.
	defaultReplicateAudioExtendModel = "meta/musicgen"
	// defaultReplicateSpeechModel is the text-to-speech model generate_speech
	// uses when replicate is the SpeechProvider and none is configured —
	// minimax/speech-2.8-hd, the collection's featured official endpoint and
	// the same MiniMax family fal's speech endpoints drive: a required text
	// input, voice_id (17+ catalog voices or a cloned id), an emotion enum,
	// and a 0.5–2.0 speed, all flat where fal nests them under voice_setting.
	// Quality-first like fal's elevenlabs multilingual default; the turbo
	// sibling is a picker entry away.
	defaultReplicateSpeechModel = "minimax/speech-2.8-hd"
	// maxReplicateTransientRetries is the number of times do() re-issues a
	// request after a transient 5xx, matching the fal client's posture.
	maxReplicateTransientRetries = 1
)

type ReplicateClient struct {
	httpClient *http.Client
	apiKey     string
	// jobSink, when set, receives the queue handle of every created
	// prediction — the hook runMediaGeneration uses to diagnose and stop a
	// prediction that outlives the local timeout (media_timeout.go). Callers
	// set it to a *mediaJob they own, right after constructing the client.
	jobSink *mediaJob
}

func newReplicateClient(httpClient *http.Client, apiKey string) ReplicateClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return ReplicateClient{httpClient: httpClient, apiKey: strings.TrimSpace(apiKey)}
}

// replicatePrediction mirrors one prediction object. Output is left raw
// because its shape is model-defined (a URL string, an array of URLs, an
// object, ...) and is scavenged per media kind.
type replicatePrediction struct {
	ID     string          `json:"id"`
	Status string          `json:"status"` // starting | processing | succeeded | failed | canceled
	Output json.RawMessage `json:"output"`
	Error  json.RawMessage `json:"error"`
	Logs   string          `json:"logs"`
}

// GenerateImage submits an already-native input object (built by
// resolveReplicateImageInput against the model's schema), polls until the
// prediction succeeds, and downloads the output images as base64 data URLs
// packed into an ollamaGenerateResponse — the same shape FalClient.
// GenerateImage produces, so generate_image's result normalization is reused.
// Output URLs expire an hour after the prediction completes, so bytes are
// always downloaded inside this call, never lazily.
func (client ReplicateClient) GenerateImage(ctx context.Context, model string, input map[string]any) (ollamaGenerateResponse, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateImageModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	return client.imagesFromPrediction(ctx, prediction, model)
}

// imagesFromPrediction downloads a succeeded image prediction's outputs as
// base64 data URLs packed into a synthetic ollamaGenerateResponse — the same
// shape FalClient.GenerateImage produces, and the shared tail of GenerateImage
// and UpscaleImage plus the timeout policy's RecoverImageJob. Output URLs
// expire an hour after the prediction completes, so this runs inside the
// calling flow, never lazily.
func (client ReplicateClient) imagesFromPrediction(ctx context.Context, prediction replicatePrediction, model string) (ollamaGenerateResponse, error) {
	dataURLs, err := client.downloadImages(ctx, replicateImageURLCandidates(prediction.Output))
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	if len(dataURLs) == 0 {
		return ollamaGenerateResponse{}, errors.New("replicate prediction returned no images")
	}
	return ollamaGenerateResponse{
		Model:  model,
		Image:  dataURLs[0],
		Images: dataURLs,
		Done:   true,
	}, nil
}

// resolveReplicateVideoInput), polls until the prediction succeeds, and
// downloads the output clip as raw bytes — the shared GeneratedVideo shape,
// so every video consumer (artifacts, carry-forward, telemetry) is unchanged.
// Video predictions run for minutes; the caller must pass a context with a
// suitably long deadline. Output URLs expire an hour after the prediction
// completes, so bytes are always downloaded inside this call.
func (client ReplicateClient) GenerateVideo(ctx context.Context, model string, input map[string]any) (GeneratedVideo, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateVideoModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return GeneratedVideo{}, err
	}
	return client.videoFromPrediction(ctx, prediction)
}

// videoFromPrediction downloads a succeeded video prediction's output clip —
// the shared tail of GenerateVideo and the timeout policy's RecoverVideoJob.
func (client ReplicateClient) videoFromPrediction(ctx context.Context, prediction replicatePrediction) (GeneratedVideo, error) {
	videoURL := replicateVideoURL(prediction.Output)
	if videoURL == "" {
		return GeneratedVideo{}, errors.New("replicate prediction returned no video")
	}
	data, mimeType, err := client.downloadVideo(ctx, videoURL)
	if err != nil {
		return GeneratedVideo{}, err
	}
	return GeneratedVideo{Data: data, MimeType: mimeType, SourceURL: videoURL}, nil
}

// runPrediction creates a prediction for the model and blocks until it reaches
// a terminal status, returning the final prediction object. The create goes to
// the model-scoped route (POST /v1/models/{owner}/{name}/predictions), which
// targets the model's latest version — official models are pinned by their
// owners, so latest is the sanctioned target.
func (client ReplicateClient) runPrediction(ctx context.Context, model string, input map[string]any) (replicatePrediction, error) {
	prediction, err := client.createPrediction(ctx, model, input)
	if err != nil {
		return replicatePrediction{}, err
	}
	if strings.TrimSpace(prediction.ID) == "" {
		return replicatePrediction{}, errors.New("replicate prediction response returned no id")
	}
	if client.jobSink != nil {
		*client.jobSink = mediaJob{
			Provider:  "replicate",
			ID:        prediction.ID,
			StatusURL: "/v1/predictions/" + prediction.ID,
			ResultURL: "/v1/predictions/" + prediction.ID,
			CancelURL: "/v1/predictions/" + prediction.ID + "/cancel",
		}
	}
	if err := client.waitForPrediction(ctx, prediction.ID); err != nil {
		return replicatePrediction{}, err
	}
	return client.fetchPrediction(ctx, prediction.ID)
}

func (client ReplicateClient) createPrediction(ctx context.Context, model string, input map[string]any) (replicatePrediction, error) {
	owner, name, err := replicateModelPath(model)
	if err != nil {
		return replicatePrediction{}, err
	}
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return replicatePrediction{}, err
	}
	resp, err := client.do(ctx, http.MethodPost, "/v1/models/"+owner+"/"+name+"/predictions", body)
	if err != nil {
		return replicatePrediction{}, err
	}
	defer resp.Body.Close()
	var prediction replicatePrediction
	if err := json.NewDecoder(resp.Body).Decode(&prediction); err != nil {
		return replicatePrediction{}, err
	}
	return prediction, nil
}

func (client ReplicateClient) fetchPrediction(ctx context.Context, id string) (replicatePrediction, error) {
	resp, err := client.do(ctx, http.MethodGet, "/v1/predictions/"+strings.TrimSpace(id), nil)
	if err != nil {
		return replicatePrediction{}, err
	}
	defer resp.Body.Close()
	var prediction replicatePrediction
	if err := json.NewDecoder(resp.Body).Decode(&prediction); err != nil {
		return replicatePrediction{}, err
	}
	return prediction, nil
}

// waitForPrediction polls GET /v1/predictions/{id} until the prediction
// reaches a terminal status — succeeded, failed, or canceled — mirroring the
// fal client's waitForCompletion contract (ctx cancellation wins, an empty
// status is an error, unknown non-empty statuses are treated as in-flight).
func (client ReplicateClient) waitForPrediction(ctx context.Context, id string) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("replicate generation cancelled: %w", err)
		}
		prediction, err := client.fetchPrediction(ctx, id)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(prediction.Status)) {
		case "succeeded":
			return nil
		case "failed":
			return errors.New(replicateFailureMessage(prediction))
		case "canceled":
			return errors.New("replicate prediction was canceled")
		case "":
			return errors.New("replicate prediction response was missing a status field")
		default:
			// starting / processing / anything unknown: keep polling.
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("replicate generation cancelled: %w", ctx.Err())
		case <-time.After(replicatePollInterval):
		}
	}
}

// CancelPrediction POSTs the prediction cancel route. Cancellation is
// best-effort on Replicate's side — queued predictions are removed,
// in-progress ones receive a cancellation signal — and a prediction that
// already terminated answers 4xx. Every 4xx means "nothing is running
// anymore" (the outcome the caller wanted), so they are swallowed here; the
// recovery methods re-check the status when the completed case matters.
// Network errors and 5xx are real failures. The request bypasses do()
// deliberately: do() maps every 4xx to an error, and the cancel's own 4xx
// codes carry outcome semantics rather than failures.
func (client ReplicateClient) CancelPrediction(ctx context.Context, id string) error {
	if strings.TrimSpace(client.apiKey) == "" {
		return errReplicateKeyNotConfigured
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, replicateAPIBaseURL+"/v1/predictions/"+strings.TrimSpace(id)+"/cancel", nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+client.apiKey)
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("replicate cancel returned %s", resp.Status)
	}
	return nil
}

// CancelMediaJob adapts CancelPrediction to runMediaGeneration's job-based
// cancel signature, mirroring FalClient.CancelMediaJob.
func (client ReplicateClient) CancelMediaJob(ctx context.Context, job mediaJob) error {
	return client.CancelPrediction(ctx, job.ID)
}

// RecoverVideoJob resolves a prediction that outlived the local timeout: a
// succeeded prediction's clip is fetched and delivered immediately (output
// URLs expire about an hour after completion, so this is the last window the
// asset exists), a failed or canceled one is reported, and a still-running one
// is cancelled and re-checked — a cancel racing a completion yields succeeded,
// and the finished clip is fetched in that case too. Error texts are
// self-contained; runMediaGeneration prefixes only the timeout.
func (client ReplicateClient) RecoverVideoJob(ctx context.Context, job mediaJob) (GeneratedVideo, error) {
	prediction, err := client.fetchPrediction(ctx, job.ID)
	if err == nil {
		switch strings.ToLower(strings.TrimSpace(prediction.Status)) {
		case "succeeded":
			return client.videoFromPrediction(ctx, prediction)
		case "failed":
			return GeneratedVideo{}, fmt.Errorf("the replicate prediction %s then failed: %s", job.ID, replicateFailureMessage(prediction))
		case "canceled":
			return GeneratedVideo{}, fmt.Errorf("the replicate prediction %s was canceled", job.ID)
		}
	}
	if cerr := client.CancelPrediction(ctx, job.ID); cerr != nil {
		return GeneratedVideo{}, fmt.Errorf("the replicate prediction %s could not be canceled (%s) and may still be running", job.ID, cerr)
	}
	if prediction, err := client.fetchPrediction(ctx, job.ID); err == nil && strings.EqualFold(strings.TrimSpace(prediction.Status), "succeeded") {
		return client.videoFromPrediction(ctx, prediction)
	}
	return GeneratedVideo{}, fmt.Errorf("the replicate prediction %s was canceled after the timeout", job.ID)
}

// RecoverImageJob is RecoverVideoJob for image results (generate_image and
// upscale_image share the same ollamaGenerateResponse shape). model labels the
// recovered response — the job handle alone doesn't carry it, and the tool
// layer attributes activities by the result's model.
func (client ReplicateClient) RecoverImageJob(ctx context.Context, job mediaJob, model string) (ollamaGenerateResponse, error) {
	prediction, err := client.fetchPrediction(ctx, job.ID)
	if err == nil {
		switch strings.ToLower(strings.TrimSpace(prediction.Status)) {
		case "succeeded":
			return client.imagesFromPrediction(ctx, prediction, model)
		case "failed":
			return ollamaGenerateResponse{}, fmt.Errorf("the replicate prediction %s then failed: %s", job.ID, replicateFailureMessage(prediction))
		case "canceled":
			return ollamaGenerateResponse{}, fmt.Errorf("the replicate prediction %s was canceled", job.ID)
		}
	}
	if cerr := client.CancelPrediction(ctx, job.ID); cerr != nil {
		return ollamaGenerateResponse{}, fmt.Errorf("the replicate prediction %s could not be canceled (%s) and may still be running", job.ID, cerr)
	}
	if prediction, err := client.fetchPrediction(ctx, job.ID); err == nil && strings.EqualFold(strings.TrimSpace(prediction.Status), "succeeded") {
		return client.imagesFromPrediction(ctx, prediction, model)
	}
	return ollamaGenerateResponse{}, fmt.Errorf("the replicate prediction %s was canceled after the timeout", job.ID)
}

// GenerateAudio submits an already-native input object (built by
// resolveReplicateAudioInput against the model's schema), polls until the
// prediction succeeds, and downloads the output clip as raw bytes — the
// shared GeneratedAudio shape, so the audio tools' result normalization is
// unchanged. It is the Replicate sibling of FalClient.GenerateAudio: a thin
// transport that knows nothing about canonical params or notices. Output URLs
// expire an hour after the prediction completes, so bytes are always
// downloaded inside this call, never lazily.
func (client ReplicateClient) GenerateAudio(ctx context.Context, model string, input map[string]any) (GeneratedAudio, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateSoundEffectsModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return GeneratedAudio{}, err
	}
	return client.audioFromPrediction(ctx, prediction)
}

// audioFromPrediction downloads a succeeded audio prediction's output — the
// shared tail of GenerateAudio and the timeout policy's RecoverAudioJob.
func (client ReplicateClient) audioFromPrediction(ctx context.Context, prediction replicatePrediction) (GeneratedAudio, error) {
	audioURL := replicateAudioURL(prediction.Output)
	if audioURL == "" {
		return GeneratedAudio{}, errors.New("replicate prediction returned no audio")
	}
	data, mimeType, err := client.downloadAudio(ctx, audioURL)
	if err != nil {
		return GeneratedAudio{}, err
	}
	return GeneratedAudio{Data: data, MimeType: mimeType, SourceURL: audioURL}, nil
}

// RecoverAudioJob is RecoverVideoJob for audio results (generate_sound on the
// replicate route). A succeeded prediction's clip is fetched and delivered
// immediately, a failed or canceled one is reported, and a still-running one
// is cancelled and re-checked — a cancel racing a completion yields
// succeeded, and the finished clip is fetched in that case too.
func (client ReplicateClient) RecoverAudioJob(ctx context.Context, job mediaJob) (GeneratedAudio, error) {
	prediction, err := client.fetchPrediction(ctx, job.ID)
	if err == nil {
		switch strings.ToLower(strings.TrimSpace(prediction.Status)) {
		case "succeeded":
			return client.audioFromPrediction(ctx, prediction)
		case "failed":
			return GeneratedAudio{}, fmt.Errorf("the replicate prediction %s then failed: %s", job.ID, replicateFailureMessage(prediction))
		case "canceled":
			return GeneratedAudio{}, fmt.Errorf("the replicate prediction %s was canceled", job.ID)
		}
	}
	if cerr := client.CancelPrediction(ctx, job.ID); cerr != nil {
		return GeneratedAudio{}, fmt.Errorf("the replicate prediction %s could not be canceled (%s) and may still be running", job.ID, cerr)
	}
	if prediction, err := client.fetchPrediction(ctx, job.ID); err == nil && strings.EqualFold(strings.TrimSpace(prediction.Status), "succeeded") {
		return client.audioFromPrediction(ctx, prediction)
	}
	return GeneratedAudio{}, fmt.Errorf("the replicate prediction %s was canceled after the timeout", job.ID)
}

// RecoverSFXJob is the kind-agnostic recovery twin the add_sound_effects
// replicate route shares with fal's: the endpoint family spans two output
// kinds (a soundtrack-mixed video, a standalone soundtrack), so recovery
// sniffs the same ladder sfxFromPrediction runs — video first, then audio.
func (client ReplicateClient) RecoverSFXJob(ctx context.Context, job mediaJob) (SFXGenerateResult, error) {
	prediction, err := client.fetchPrediction(ctx, job.ID)
	if err == nil {
		switch strings.ToLower(strings.TrimSpace(prediction.Status)) {
		case "succeeded":
			return client.sfxFromPrediction(ctx, prediction)
		case "failed":
			return SFXGenerateResult{}, fmt.Errorf("the replicate prediction %s then failed: %s", job.ID, replicateFailureMessage(prediction))
		case "canceled":
			return SFXGenerateResult{}, fmt.Errorf("the replicate prediction %s was canceled", job.ID)
		}
	}
	if cerr := client.CancelPrediction(ctx, job.ID); cerr != nil {
		return SFXGenerateResult{}, fmt.Errorf("the replicate prediction %s could not be canceled (%s) and may still be running", job.ID, cerr)
	}
	if prediction, err := client.fetchPrediction(ctx, job.ID); err == nil && strings.EqualFold(strings.TrimSpace(prediction.Status), "succeeded") {
		return client.sfxFromPrediction(ctx, prediction)
	}
	return SFXGenerateResult{}, fmt.Errorf("the replicate prediction %s was canceled after the timeout", job.ID)
}

// GenerateSFX submits an already-native input object (built by resolveSFXBody
// against the model's schema), polls until the prediction succeeds, and
// downloads whichever media kind the model produced — a soundtrack-mixed
// video (GeneratedVideo) or a standalone soundtrack (GeneratedAudio), the
// same dual-kind SFXGenerateResult fal's queue path fills. Output URLs expire
// an hour after the prediction completes, so bytes are always downloaded
// inside this call.
func (client ReplicateClient) GenerateSFX(ctx context.Context, model string, input map[string]any) (SFXGenerateResult, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateSoundEffectsVideoModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return SFXGenerateResult{}, err
	}
	return client.sfxFromPrediction(ctx, prediction)
}

// sfxFromPrediction downloads a succeeded SFX prediction's output — video
// first (the soundtrack-mixed clip is the richer deliverable), then audio —
// the shared tail of GenerateSFX and RecoverSFXJob.
func (client ReplicateClient) sfxFromPrediction(ctx context.Context, prediction replicatePrediction) (SFXGenerateResult, error) {
	if videoURL := replicateVideoURL(prediction.Output); videoURL != "" {
		data, mimeType, err := client.downloadVideo(ctx, videoURL)
		if err != nil {
			return SFXGenerateResult{}, err
		}
		return SFXGenerateResult{Video: GeneratedVideo{Data: data, MimeType: mimeType, SourceURL: videoURL}}, nil
	}
	audioURL := replicateAudioURL(prediction.Output)
	if audioURL == "" {
		return SFXGenerateResult{}, errors.New("replicate prediction returned no video or audio")
	}
	data, mimeType, err := client.downloadAudio(ctx, audioURL)
	if err != nil {
		return SFXGenerateResult{}, err
	}
	return SFXGenerateResult{Audio: GeneratedAudio{Data: data, MimeType: mimeType, SourceURL: audioURL}}, nil
}

// replicateAudioURL finds the clip URL in an audio prediction's output. Audio
// models return a single URL string (or a one-element array); the fallback
// walk matches http(s) URLs with audio extensions the way the fal client's
// firstFalAudioURL does.
func replicateAudioURL(raw json.RawMessage) string {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.HasPrefix(single, "http") {
			return strings.TrimSpace(single)
		}
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, entry := range list {
			if s, ok := entry.(string); ok && strings.HasPrefix(s, "http") {
				return strings.TrimSpace(s)
			}
		}
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	found := ""
	walkJSONStrings(payload, func(value string) {
		if found != "" {
			return
		}
		trimmed := strings.TrimSpace(value)
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			return
		}
		lower := strings.ToLower(trimmed)
		for _, ext := range []string{".mp3", ".wav", ".ogg", ".flac", ".m4a", ".aac", ".opus"} {
			if strings.Contains(lower, ext) {
				found = trimmed
				return
			}
		}
	})
	return found
}

// downloadAudio fetches a generated audio URL and returns its raw bytes and
// MIME type — the fal client's downloadAudio contract, shared here because
// audio bytes are handed back for the caller to write straight to a file-path
// artifact. The byte sniff OR the declared audio/ content type is accepted:
// Replicate delivery URLs may carry neither a sniffable container header nor
// a set Content-Type (raw PCM WAV payloads), so failing closed on both would
// drop valid clips.
func (client ReplicateClient) downloadAudio(ctx context.Context, audioURL string) ([]byte, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("audio download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, falAudioMaxBytes))
	if err != nil {
		return nil, "", err
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !isAudioBytes(data) && !strings.HasPrefix(mimeType, "audio/") {
		return nil, "", errors.New("downloaded replicate result is not a supported audio clip")
	}
	if !strings.HasPrefix(mimeType, "audio/") {
		if detected := http.DetectContentType(data); strings.HasPrefix(detected, "audio/") {
			mimeType = detected
		} else {
			mimeType = "audio/mpeg"
		}
	}
	return data, mimeType, nil
}

// replicateFailureMessage renders a failed prediction's error field, which may
// be a plain string or an object, falling back to the log tail.
func replicateFailureMessage(prediction replicatePrediction) string {
	if msg := replicateRawMessageText(prediction.Error); msg != "" {
		return msg
	}
	if logs := strings.TrimSpace(prediction.Logs); logs != "" {
		lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
		return lines[len(lines)-1]
	}
	return "replicate reported a failed prediction"
}

// replicateRawMessageText stringifies a raw JSON field that may be a string or
// an object with a message/detail/error member.
func replicateRawMessageText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		for _, key := range []string{"message", "detail", "error"} {
			if text := replicateRawMessageText(obj[key]); text != "" {
				return text
			}
		}
	}
	return strings.TrimSpace(string(trimmed))
}

// replicateModelPath splits an owner/name slug into escaped path segments.
// Replicate model ids are always two segments; a malformed id fails here
// rather than as a confusing 404 from the API.
func replicateModelPath(model string) (owner, name string, err error) {
	parts := strings.SplitN(strings.TrimSpace(model), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid replicate model id %q: expected owner/name", model)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// replicateImageURLCandidates collects output URLs that may hold images, in
// priority order: direct shapes first (a URL string, an array of strings or
// {url} objects — the common image-model output), then any http(s) URL whose
// path ends in an image extension, then every http(s) URL (the download step
// validates content and drops non-images). Replicate delivery URLs usually
// carry the output's extension, but not always; the layered walk keeps a
// well-shaped model working either way.
func replicateImageURLCandidates(raw json.RawMessage) []string {
	var urls []string
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			urls = append(urls, value)
		}
	}
	collectHTTPURLs := func(extensionFilter []string) {
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return
		}
		walkJSONStrings(payload, func(value string) {
			trimmed := strings.TrimSpace(value)
			if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
				return
			}
			if len(extensionFilter) > 0 {
				lower := strings.ToLower(trimmed)
				for _, ext := range extensionFilter {
					if strings.Contains(lower, ext) {
						add(trimmed)
						return
					}
				}
				return
			}
			add(trimmed)
		})
	}

	// Direct shapes.
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && strings.HasPrefix(single, "http") {
		add(single)
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, entry := range list {
			switch typed := entry.(type) {
			case string:
				if strings.HasPrefix(typed, "http") {
					add(typed)
				}
			case map[string]any:
				if url, ok := typed["url"].(string); ok {
					add(url)
				}
			}
		}
	}
	// Extension-anchored walk, then the any-URL fallback.
	collectHTTPURLs([]string{".png", ".jpg", ".jpeg", ".webp", ".gif"})
	collectHTTPURLs(nil)
	return urls
}

// replicateVideoURL finds the clip URL in a video prediction's output. Video
// models return a single URL string (or a one-element array); the fallback
// walk matches http(s) URLs with video extensions the way the fal client's
// firstFalVideoURL does.
func replicateVideoURL(raw json.RawMessage) string {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.HasPrefix(single, "http") {
			return strings.TrimSpace(single)
		}
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, entry := range list {
			if s, ok := entry.(string); ok && strings.HasPrefix(s, "http") {
				return strings.TrimSpace(s)
			}
		}
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	found := ""
	walkJSONStrings(payload, func(value string) {
		if found != "" {
			return
		}
		trimmed := strings.TrimSpace(value)
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			return
		}
		lower := strings.ToLower(trimmed)
		for _, ext := range []string{".mp4", ".webm", ".mov", ".m4v"} {
			if strings.Contains(lower, ext) {
				found = trimmed
				return
			}
		}
	})
	return found
}

// downloadImages fetches each candidate URL through the shared
// fetchImageAsDataURL (which rejects non-image bytes), keeping the ones that
// decode. Ordering is preserved, so the first successfully downloaded image is
// the output's own first entry.
func (client ReplicateClient) downloadImages(ctx context.Context, urls []string) ([]string, error) {
	dataURLs := make([]string, 0, len(urls))
	var lastErr error
	for _, url := range urls {
		dataURL, err := fetchImageAsDataURL(ctx, client.httpClient, url)
		if err != nil {
			lastErr = err
			continue
		}
		dataURLs = append(dataURLs, dataURL)
	}
	if len(dataURLs) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return dataURLs, nil
}

// downloadVideo fetches a generated video URL and returns its raw bytes and
// MIME type — the fal client's downloadVideo contract, shared here because
// video bytes are handed back for the caller to write straight to a
// file-path artifact.
func (client ReplicateClient) downloadVideo(ctx context.Context, videoURL string) ([]byte, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("video download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, replicateVideoMaxBytes))
	if err != nil {
		return nil, "", err
	}
	if !isVideoBytes(data) {
		return nil, "", errors.New("downloaded replicate result is not a supported video")
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if mimeType == "" || !strings.HasPrefix(mimeType, "video/") {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "video/") {
		mimeType = "video/mp4"
	}
	return data, mimeType, nil
}

// UpscaleImage submits an attached image to a Replicate upscaler model
// (input built by resolveReplicateUpscaleInput against the model's schema),
// polls until the prediction succeeds, and returns the upscaled image as a
// base64 data URL packed into a synthetic ollamaGenerateResponse — the same
// shape FalClient.UpscaleImage produces, so the tool's result normalization
// is reused. Routed to when the image provider is replicate
// (imageGenerationProvider); fal remains the backend otherwise.
func (client ReplicateClient) UpscaleImage(ctx context.Context, model string, input map[string]any) (ollamaGenerateResponse, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultReplicateUpscaleModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return ollamaGenerateResponse{}, err
	}
	return client.imagesFromPrediction(ctx, prediction, model)
}

// TranscribeAudio submits an already-native input object (built by
// resolveReplicateTranscribeInput against the model's schema), polls until the
// prediction succeeds, and normalizes the output onto GeneratedTranscript —
// the Replicate sibling of FalClient.TranscribeAudio. There is no recover
// step: transcription rides the timeout policy's cancel-only path, a
// transcript being cheap to redo.
func (client ReplicateClient) TranscribeAudio(ctx context.Context, req TranscribeAudioRequest, input map[string]any) (GeneratedTranscript, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = defaultReplicateTranscribeModel
	}
	if input == nil {
		input = map[string]any{}
	}
	prediction, err := client.runPrediction(ctx, model, input)
	if err != nil {
		return GeneratedTranscript{}, err
	}
	return replicateTranscriptFromPrediction(prediction, req)
}

// replicateTranscriptFromPrediction normalizes a succeeded prediction's output
// onto GeneratedTranscript. The speech-to-text collection's outputs are
// heterogeneous — openai/whisper returns an object carrying the transcript,
// an optional English translation, whisper segment objects, and hosted
// srt/txt files; gpt-4o-transcribe and parakeet return a plain string;
// whisperx-class models return segments with word arrays — so the ladder
// reads whichever shape arrives. The requested granularity is honored when
// the output provides it: a "words" request stays words only when word
// chunks materialized, degrades to segments with the wizper-style notice
// when segment chunks did, and comes back plain (fal's silent downgrade)
// when the model produced no chunks at all.
func replicateTranscriptFromPrediction(prediction replicatePrediction, req TranscribeAudioRequest) (GeneratedTranscript, error) {
	var output any
	if len(prediction.Output) > 0 {
		if err := json.Unmarshal(prediction.Output, &output); err != nil {
			return GeneratedTranscript{}, fmt.Errorf("replicate transcription returned undecodable output: %w", err)
		}
	}
	text, chunks, sawWords := replicateTranscriptOutput(output, strings.TrimSpace(req.Task))
	if text == "" && len(chunks) > 0 {
		// A chunk-only payload still yields a usable plain transcript —
		// the fal chunk-only path.
		parts := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			if trimmed := strings.TrimSpace(chunk.Text); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
		text = strings.Join(parts, " ")
	}
	if strings.TrimSpace(text) == "" {
		return GeneratedTranscript{}, errors.New("replicate transcription returned no text")
	}

	transcript := GeneratedTranscript{Text: strings.TrimSpace(text)}
	requested := strings.TrimSpace(req.Timestamps)
	if requested == "" {
		// A plain request stays plain even when the model volunteered
		// segments — the fal chunk_level contract.
		return transcript, nil
	}
	if len(chunks) == 0 {
		// The model produced no timestamped units — report plain text
		// rather than claiming a timestamped transcript (fal's stance).
		return transcript, nil
	}
	if requested == "words" && !sawWords {
		// No word-level units in the output — the model served segments
		// (or the request's word control was absent), so say so instead of
		// labeling segments as words.
		transcript.Notices = append(transcript.Notices, fmt.Sprintf("%s provides segment-level timestamps only — returning segments", strings.TrimSpace(req.Model)))
		requested = "segments"
	}
	transcript.Timestamps = requested
	transcript.Chunks = chunks
	transcript.TimestampedText = renderTimestampedChunks(chunks)
	return transcript, nil
}

// replicateTranscriptOutput scavenges one speech-to-text output for the
// transcript text and timestamped chunks. Object outputs are read field by
// field: a translate task prefers the English "translation" field (the
// transcription field holds the original-language text beside it), plain
// requests take "transcription" then "text"; chunks come from the "segments"
// array (the whisper and whisperx shapes alike, word arrays flattened when
// present) and fall back to subtitle-parsing the text itself when it is a
// VTT/SRT document (openai/whisper's vtt output format). String outputs are
// the whole transcript — chunked only when the text itself is a subtitle
// document.
func replicateTranscriptOutput(output any, task string) (string, []transcriptChunk, bool) {
	switch typed := output.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return "", nil, false
		}
		if chunks, ok := subtitleChunksForTranscript(text); ok {
			return subtitleTextFromChunks(chunks), chunks, false
		}
		return text, nil, false
	case map[string]any:
		var chunks []transcriptChunk
		sawWords := false
		if segments, ok := typed["segments"].([]any); ok {
			chunks, sawWords = replicateSegmentsToChunks(segments)
		}
		fields := []string{"transcription", "text"}
		if task == "translate" {
			fields = []string{"translation", "transcription", "text"}
		}
		for _, field := range fields {
			if s, ok := typed[field].(string); ok && strings.TrimSpace(s) != "" {
				text := strings.TrimSpace(s)
				if len(chunks) == 0 {
					if parsed, ok := subtitleChunksForTranscript(text); ok {
						return subtitleTextFromChunks(parsed), parsed, false
					}
				}
				return text, chunks, sawWords
			}
		}
		return "", chunks, sawWords
	}
	return "", nil, false
}

// subtitleChunksForTranscript parses a transcript that is itself a VTT or SRT
// document into timestamped chunks. openai/whisper's vtt output format makes
// the transcription field carry a WebVTT document; this recovers the timing
// the plain-text output would lose. Only a parse that yields cues counts —
// ordinary prose never parses as subtitles.
func subtitleChunksForTranscript(text string) ([]transcriptChunk, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "WEBVTT") && !looksLikeSRTPattern(trimmed) {
		return nil, false
	}
	cues, _, _, err := parseSubtitles([]byte(trimmed))
	if err != nil || len(cues) == 0 {
		return nil, false
	}
	chunks := make([]transcriptChunk, 0, len(cues))
	for _, cue := range cues {
		end := cue.End
		chunks = append(chunks, transcriptChunk{Start: cue.Start, End: &end, Text: cue.Text})
	}
	return chunks, true
}

// looksLikeSRTPattern reports whether text opens like an SRT document — a
// numeric cue counter followed by a timing line — so the SRT side of
// parseSubtitles is reachable for string outputs (openai/whisper's srt
// format is delivered as a hosted file, but some models inline SRT text).
func looksLikeSRTPattern(text string) bool {
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) < 2 {
		return false
	}
	counter := strings.TrimSpace(lines[0])
	if counter == "" {
		return false
	}
	for _, r := range counter {
		if r < '0' || r > '9' {
			return false
		}
	}
	return strings.Contains(lines[1], "-->")
}

// subtitleTextFromChunks joins chunk text into the plain transcript — the
// text half of a subtitle-parsed transcript.
func subtitleTextFromChunks(chunks []transcriptChunk) string {
	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if trimmed := strings.TrimSpace(chunk.Text); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

// replicateSegmentsToChunks walks a model's "segments" array — the whisper
// segment shape {start, end, text} or the whisperx one that adds a nested
// "words" array of {start, end, word} — into canonical chunks. Word objects
// replace their parent segment only when every word carries a usable start
// (a half-aligned word list would drop the timing precision the segments
// themselves carry); unusable segments are skipped, never fatal.
func replicateSegmentsToChunks(segments []any) ([]transcriptChunk, bool) {
	chunks := make([]transcriptChunk, 0, len(segments))
	sawWords := false
	for _, segment := range segments {
		obj, ok := segment.(map[string]any)
		if !ok {
			continue
		}
		if words, ok := obj["words"].([]any); ok && len(words) > 0 {
			if wordChunks := replicateWordsToChunks(words); len(wordChunks) > 0 {
				chunks = append(chunks, wordChunks...)
				sawWords = true
				continue
			}
		}
		chunk := transcriptChunk{Text: stringField(obj, "text")}
		start, ok := numberField(obj, "start")
		if !ok {
			continue // no timing — the unit cannot join a timestamped transcript
		}
		chunk.Start = start
		if end, ok := numberField(obj, "end"); ok {
			chunk.End = &end
		}
		chunks = append(chunks, chunk)
	}
	return chunks, sawWords
}

// replicateWordsToChunks walks a whisperx word array ({start, end, word})
// into word-level chunks. A word missing timing or text drops out; an array
// with no usable words yields nothing so the caller falls back to its
// segment.
func replicateWordsToChunks(words []any) []transcriptChunk {
	chunks := make([]transcriptChunk, 0, len(words))
	for _, word := range words {
		obj, ok := word.(map[string]any)
		if !ok {
			continue
		}
		start, okStart := numberField(obj, "start")
		if !okStart {
			continue
		}
		text := stringField(obj, "word")
		if text == "" {
			text = stringField(obj, "text")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		chunk := transcriptChunk{Start: start, Text: text}
		if end, ok := numberField(obj, "end"); ok {
			chunk.End = &end
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

// stringField reads a string field from a decoded JSON object, trimmed.
func stringField(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return strings.TrimSpace(s)
}

// numberField reads a numeric field (JSON number, or a string holding one —
// some models serialize timestamps as strings) as float64.
func numberField(obj map[string]any, key string) (float64, bool) {
	switch v := obj[key].(type) {
	case float64:
		return v, true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f, true
		}
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// ResolveMediaURL normalizes a media reference for Replicate, uploading it
// through the Files API when the decoded payload exceeds the inline limit —
// the fal resolveMediaURL pattern. Small payloads stay inline as data URIs
// (Replicate accepts them under ~1MB); large ones — typically attached source
// images and clips — get a hosted URL. On any upload failure the original
// data URI is returned so the request still goes through and Replicate's own
// error surfaces. defaultMediaType is the fallback when the payload's MIME
// can't be detected.
func (client ReplicateClient) ResolveMediaURL(ctx context.Context, reference, defaultMediaType, filename string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", nil
	}
	if strings.HasPrefix(reference, "http://") || strings.HasPrefix(reference, "https://") {
		return reference, nil
	}
	var data []byte
	var mediaType string
	if path, ok := artifactMediaPath(reference); ok {
		// A local artifact (hydrated history, or a large staged upload the
		// request carries instead of a data URL). Read the bytes and flow into
		// the same inline-vs-upload decision below.
		mediaType = mediaTypeForExtension(strings.ToLower(filepath.Ext(path)))
		if mediaType == "" {
			mediaType = defaultMediaType
		}
		fileData, err := os.ReadFile(path)
		if err != nil {
			return reference, nil // unreadable — pass through, let Replicate reject
		}
		data = fileData
		if detected := http.DetectContentType(data); strings.HasPrefix(detected, strings.SplitN(defaultMediaType, "/", 2)[0]+"/") {
			mediaType = detected
		}
	} else if strings.HasPrefix(reference, "data:") {
		comma := strings.Index(reference, ",")
		if comma < 0 {
			return reference, nil // malformed data URI — let Replicate reject it
		}
		header := reference[len("data:"):comma]
		mediaType = header
		if semicolon := strings.Index(header, ";"); semicolon >= 0 {
			mediaType = header[:semicolon]
		}
		if mediaType == "" || mediaType == "base64" {
			mediaType = defaultMediaType
		}
		decoded, err := base64.StdEncoding.DecodeString(reference[comma+1:])
		if err != nil {
			return reference, nil // undecodable — pass through, let Replicate reject
		}
		data = decoded
	} else {
		decoded, err := base64.StdEncoding.DecodeString(reference)
		if err != nil {
			return reference, nil
		}
		data = decoded
		mediaType = defaultMediaType
		if detected := http.DetectContentType(data); strings.HasPrefix(detected, strings.SplitN(defaultMediaType, "/", 2)[0]+"/") {
			mediaType = detected
		}
	}
	if len(data) <= replicateInlineMediaMaxBytes {
		return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	accessURL, err := client.UploadFile(ctx, data, filename, mediaType)
	if err != nil {
		return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	return accessURL, nil
}

// UploadFile uploads raw media bytes through Replicate's Files API
// (multipart POST /v1/files, "content" field) and returns the hosted access
// URL — the input form video/image models accept for large media. The URL is
// what gets passed as the model input value.
func (client ReplicateClient) UploadFile(ctx context.Context, data []byte, filename, mediaType string) (string, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return "", errReplicateKeyNotConfigured
	}
	if filename == "" {
		filename = "upload"
		if parts := strings.Split(mediaType, "/"); len(parts) == 2 {
			filename += "." + parts[1]
		}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("content", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, replicateAPIBaseURL+"/v1/files", &body)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("replicate file upload returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		URLs struct {
			Get string `json:"get"`
		} `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.URLs.Get) == "" {
		return "", errors.New("replicate file upload returned no access url")
	}
	return result.URLs.Get, nil
}

// ReplicateModel is one entry from a Replicate collection, flattened into the
// same field names FalModel uses (id, displayName, ...) so the frontend model
// pickers render both providers' catalogs identically.
type ReplicateModel struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"displayName"`
	Category     string   `json:"category"`
	Description  string   `json:"description"`
	Status       string   `json:"status"`
	Tags         []string `json:"tags"`
	ThumbnailURL string   `json:"thumbnailUrl"`
}

// replicateCollectionResponse mirrors GET /v1/collections/{slug}.
type replicateCollectionResponse struct {
	Name   string                     `json:"name"`
	Slug   string                     `json:"slug"`
	Models []replicateCollectionModel `json:"models"`
}

type replicateCollectionModel struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Visibility    string `json:"visibility"`
	IsOfficial    bool   `json:"is_official"`
	RunCount      int64  `json:"run_count"`
	CoverImageURL string `json:"cover_image_url"`
}

// ListCollectionModels returns the models of a Replicate collection
// (GET /v1/collections/{slug}), filtered to official models. Official models
// are per-output priced and always on — the curated list a Settings picker
// should offer — while community models bill GPU time, can go offline with
// their authors, and their input schemas drift. The fal listers' category
// walk is the sibling here; collections are pre-bucketed so no pagination is
// needed.
func (client ReplicateClient) ListCollectionModels(ctx context.Context, slug string) ([]ReplicateModel, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, errors.New("replicate collection slug is required")
	}
	resp, err := client.do(ctx, http.MethodGet, "/v1/collections/"+slug, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var collection replicateCollectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&collection); err != nil {
		return nil, err
	}
	models := make([]ReplicateModel, 0, len(collection.Models))
	for _, entry := range collection.Models {
		owner := strings.TrimSpace(entry.Owner)
		name := strings.TrimSpace(entry.Name)
		if owner == "" || name == "" || !entry.IsOfficial {
			continue
		}
		models = append(models, ReplicateModel{
			ID:           owner + "/" + name,
			DisplayName:  name,
			Category:     slug,
			Description:  entry.Description,
			Status:       "official",
			Tags:         []string{owner},
			ThumbnailURL: entry.CoverImageURL,
		})
	}
	return models, nil
}

// GetModelInputSchema returns the raw OpenAPI JSON describing a model's input
// (latest_version.openapi_schema) — the same dialect fal's per-endpoint
// schemas speak, so parseModelInputSchema consumes either unchanged.
func (client ReplicateClient) GetModelInputSchema(ctx context.Context, model string) ([]byte, error) {
	owner, name, err := replicateModelPath(model)
	if err != nil {
		return nil, err
	}
	resp, err := client.do(ctx, http.MethodGet, "/v1/models/"+owner+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload struct {
		LatestVersion *struct {
			OpenAPISchema json.RawMessage `json:"openapi_schema"`
		} `json:"latest_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.LatestVersion == nil || len(payload.LatestVersion.OpenAPISchema) == 0 {
		return nil, fmt.Errorf("model %q has no published input schema", model)
	}
	return payload.LatestVersion.OpenAPISchema, nil
}

// VerifyKey confirms the API token is accepted by Replicate without starting
// a prediction — a cheap authenticated GET (the account endpoint) that
// returns 200 for a valid token and 401 for a bad one. Used by the Settings
// "Check Connection" button so a bad token fails fast and cheaply.
func (client ReplicateClient) VerifyKey(ctx context.Context) error {
	resp, err := client.do(ctx, http.MethodGet, "/v1/account", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// do issues one API request with Bearer auth, retrying once on a transient
// 5xx. Error bodies carry {title, detail}; the mapped prefixes
// ("replicate authentication failed", "replicate rate limited",
// "replicate credit exhausted") are what the harness's error-remediation
// rules match on, mirroring the fal client's error vocabulary.
func (client ReplicateClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if strings.TrimSpace(client.apiKey) == "" {
		return nil, errReplicateKeyNotConfigured
	}
	target := replicateAPIBaseURL + path
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		httpReq, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+client.apiKey)
		if body != nil {
			httpReq.Header.Set("Content-Type", "application/json")
		}
		resp, err = client.httpClient.Do(httpReq)
		if err != nil {
			return nil, err
		}
		if !isReplicateTransientStatus(resp.StatusCode) || attempt >= maxReplicateTransientRetries {
			break
		}
		// Drain before retry so the connection can be reused.
		resp.Body.Close()
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		trimmed := strings.TrimSpace(string(message))
		if detail := replicateErrorDetail(message); detail != "" {
			trimmed = detail
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("replicate authentication failed: %s", trimmed)
		case http.StatusPaymentRequired:
			return nil, fmt.Errorf("replicate credit exhausted: %s", trimmed)
		case http.StatusTooManyRequests:
			return nil, fmt.Errorf("replicate rate limited: %s", trimmed)
		default:
			// Name the method and endpoint: a bare "replicate returned 422"
			// is opaque when several models are in play.
			return nil, fmt.Errorf("replicate %s %s returned %s: %s", method, path, resp.Status, trimmed)
		}
	}
	return resp, nil
}

// replicateErrorDetail extracts the {title, detail} pair Replicate's error
// bodies carry (detail may itself be a string or an array of validation
// errors), so a 401/422 message reads "Unauthenticated: ..." instead of raw
// JSON.
func replicateErrorDetail(body []byte) string {
	var payload struct {
		Title  string          `json:"title"`
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	title := strings.TrimSpace(payload.Title)
	detail := replicateRawMessageText(payload.Detail)
	switch {
	case title != "" && detail != "":
		return title + ": " + detail
	case title != "":
		return title
	default:
		return detail
	}
}

// isReplicateTransientStatus reports whether a Replicate HTTP status is worth
// retrying — the same posture as the fal client: 5xx transient, 429 excluded
// (it has a dedicated rate-limit message), 4xx deterministic.
func isReplicateTransientStatus(code int) bool {
	switch code {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}
