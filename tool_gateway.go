package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

type ToolExecutionRequest struct {
	Name           string          `json:"name"`
	Call           HarnessToolCall `json:"call"`
	RequestID      string          `json:"requestId,omitempty"`
	ConversationID string          `json:"conversationId,omitempty"`
	Source         string          `json:"source,omitempty"`
}

// Permission gate outcomes, recorded on ToolPermissionDecision.Outcome.
const (
	permissionOutcomeApproved  = "approved"
	permissionOutcomeDenied    = "denied"
	permissionOutcomeTimeout   = "timeout"
	permissionOutcomeCancelled = "cancelled"
)

// ToolPermissionDecision records how a permission gate resolved — approved or
// denied by the UI, timed out (fail-closed after 2 minutes), cancelled with
// the request context, or denied because no UI was attached — plus how long
// the gate waited. It is telemetry: it rides HarnessToolResult via json:"-"
// and lands on HarnessToolActivity, never in planner evidence.
type ToolPermissionDecision struct {
	Approved bool   `json:"approved"`
	Outcome  string `json:"outcome"`
	WaitMS   int64  `json:"waitMs"`
}

type ToolGateway struct {
	app                 *App
	registry            HarnessToolRegistry
	tools               HarnessToolExecutionContext
	permissionRequester func(context.Context, ToolPermissionRequestEvent) ToolPermissionDecision
}

// imageGenerationProvider names the backend generate_image routes to for the
// given config: the routing truth for both the gateway's GenerateImage wiring
// below and media telemetry attribution (toolActivityFromResult, and the
// generate_image activity's Command). An unset or unrecognized provider means
// Ollama, the local default.
func imageGenerationProvider(config AppConfig) string {
	switch strings.TrimSpace(config.Models.ImageProvider) {
	case "fal":
		return "fal"
	case "replicate":
		return "replicate"
	case "openai-compatible":
		return "openai-compatible"
	default:
		return "ollama"
	}
}

// videoGenerationProvider names the backend generate_video routes to for the
// given config — the video sibling of imageGenerationProvider and likewise the
// routing truth for the gateway's GenerateVideo wiring, media telemetry
// attribution (toolActivityFromResult), and the tool gates/resolvers. An unset
// or unrecognized provider means fal: video was fal-only before the Replicate
// backend existed, so a config written then must keep routing there. The
// non-voice audio tools route by their own Models.AudioProvider
// (resolveAudioProvider), speech by Models.SpeechProvider, lip_sync by
// Models.LipsyncProvider, and the video-source transforms
// (upscale/reframe/restyle) follow this provider.
func videoGenerationProvider(config AppConfig) string {
	switch strings.TrimSpace(config.Models.VideoProvider) {
	case "replicate":
		return "replicate"
	default:
		return "fal"
	}
}

// reframeDurationToleranceSeconds is how far a reframe's rendered clip may
// drift from its source before the mismatch is flagged. Container and keyframe
// rounding move a duration by a frame or two; anything past half a second is
// the model re-timing the footage, not rounding it.
const reframeDurationToleranceSeconds = 0.5

// noteReframeDurationMismatch is the reframe duration guard: generative
// reframers (LTX-2.3, Luma Ray) re-render every frame and decide the output
// length themselves — their schemas carry no duration input to pin it, and
// conv_0d756d03's 15.08s source came back 14.2s from ltx-2.3/reframe. When the
// rendered clip's own container disagrees with the staged source beyond the
// tolerance, a notice rides the result so the reply says so instead of leaving
// it to be discovered on playback. Fail-soft everywhere: an unreadable source
// or an unparseable output container skips the guard, like the pricing
// estimate. The tool description already points frame-exact needs at the local
// transform_video (crop/blur/pad), which the notice names as the alternative.
func noteReframeDurationMismatch(generated *GeneratedVideo, sourceSeconds float64, sourceReadable bool) {
	if !sourceReadable || sourceSeconds <= 0 {
		return
	}
	outputSeconds, ok := mp4DurationSeconds(generated.Data)
	if !ok || outputSeconds <= 0 {
		return
	}
	if math.Abs(outputSeconds-sourceSeconds) <= reframeDurationToleranceSeconds {
		return
	}
	generated.Notices = append(generated.Notices, fmt.Sprintf(
		"The reframed clip runs %.1fs against the source's %.1fs — the reframe model re-rendered the footage and returned a different duration. For a frame-exact shape change, use the local transform_video tool (crop, blur, or pad) instead.",
		outputSeconds, sourceSeconds))
}

// reframeBilledSeconds picks the input duration the fal reframe estimate bills:
// the endpoints bill per second of the INPUT clip, so the staged source's own
// container is the truth. The rendered clip stays the fallback for a source
// that couldn't be parsed — the two can drift (noteReframeDurationMismatch).
func reframeBilledSeconds(sourceSeconds float64, sourceReadable bool, output []byte) (float64, bool) {
	if sourceReadable && sourceSeconds > 0 {
		return sourceSeconds, true
	}
	return falVideoBilledSeconds(output, "")
}

func newToolGateway(app *App, config AppConfig, registry ...HarnessToolRegistry) ToolGateway {
	gw := ToolGateway{
		app:   app,
		tools: newHarnessToolExecutionContext(config),
	}
	if len(registry) > 0 {
		gw.registry = registry[0]
	} else {
		// The gateway rebuild path is rare (only when a caller doesn't pass a
		// cached registry); use Background like the engine's toolRegistry() so a
		// cancelled request context can't poison a registry that may outlive it.
		gw.registry = defaultHarnessToolRegistry(context.Background(), config, app)
	}
	gateway := gw
	if app != nil {
		gateway.permissionRequester = app.toolPermission
		// schemaCache is category-agnostic (keyed by model id, used by both
		// resolveAudioBody and resolveImageBody); falOverrides carries the
		// per-model escape-hatch map for every category (audio, image, ...).
		// replicateSchemaCache serves the same role for the Replicate backend's
		// per-model input schemas, namespaced on its own disk directory.
		schemaCache := newFalSchemaCache(app.client, config.Storage.Root)
		falOverrides := loadFalOverrides(config.Storage.Root)
		replicateSchemaCache := newReplicateSchemaCache(app.client, config.Storage.Root)
		gateway.tools.GenerateImage = func(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, []string, error) {
			// Airplane mode refuses the cloud-routed backends (fal, Replicate)
			// before any source normalization; a local backend falls through.
			if err := airplaneMediaError(config, "generate_image"); err != nil {
				return ollamaGenerateResponse{}, nil, nil, err
			}
			// Source images must decode at the model: an attached HEIC/AVIF/
			// TIFF/BMP/JP2 becomes JPEG here (model_image_compat.go) for every
			// backend below — fal, openai-compatible, and Ollama alike.
			req.Images = ensureModelSafeImages(ctx, config, req.Images)
			provider := imageGenerationProvider(config)
			if provider == "fal" {
				apiKey, err := loadFalAPIKey()
				if err != nil {
					return ollamaGenerateResponse{}, nil, nil, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return ollamaGenerateResponse{}, nil, nil, errFalKeyNotConfigured
				}
				client := newFalClient(app.client, apiKey)
				// Pre-resolve attached source images: oversized payloads upload to
				// fal's CDN so the queue submit stays under the inline size limit.
				for i, img := range req.Images {
					if resolved, err := client.resolveMediaURL(ctx, img, "image/png", ""); err == nil && resolved != "" {
						req.Images[i] = resolved
					}
				}
				schema := schemaCache.Get(ctx, req.Model)
				body, notices, err := resolveImageBody(schema, req, falOverrides)
				if err != nil {
					return ollamaGenerateResponse{}, nil, nil, err
				}
				job := &mediaJob{}
				client.jobSink = job
				resp, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (ollamaGenerateResponse, error) {
						r, _, err := client.GenerateImage(jctx, req.Model, body)
						return r, err
					},
					client.CancelMediaJob,
					func(jctx context.Context, j mediaJob) (ollamaGenerateResponse, error) {
						return client.RecoverImageJob(jctx, j, req.Model)
					},
				)
				if genErr == nil {
					if recovered {
						notices = append(notices, mediaGenerationRecoveredNotice)
					}
					// Cost is priced from what the response actually delivered
					// (image count, rendered megapixels for MP-billed models),
					// not what the plan asked for.
					imageCount := len(resp.Images)
					if imageCount == 0 && resp.Image != "" {
						imageCount = 1
					}
					resp.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, falBillingHints{
						Images:     imageCount,
						Requests:   1,
						Megapixels: imageResultMegapixels(resp.Images, resp.Image),
					})
				}
				return resp, nil, notices, genErr
			}
			if provider == "openai-compatible" {
				apiKey, err := loadOpenAICompatibleAPIKey()
				if err != nil {
					return ollamaGenerateResponse{}, nil, nil, err
				}
				client := newOpenAICompatibleClient(app.client, config.Providers.OpenAICompatible.BaseURL, apiKey)
				resp, _, err := client.GenerateImage(ctx, req)
				// The client already normalized every result into data URLs, so
				// raw stays nil like the fal path (collectImagesFromJSON must not
				// re-harvest source URLs from a response it doesn't see).
				return resp, nil, nil, err
			}
			if provider == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return ollamaGenerateResponse{}, nil, nil, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return ollamaGenerateResponse{}, nil, nil, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				// Pre-resolve attached source images: oversized payloads upload
				// through Replicate's Files API so the prediction input stays
				// within the inline data-URI budget.
				for i, img := range req.Images {
					if resolved, err := client.ResolveMediaURL(ctx, img, "image/png", ""); err == nil && resolved != "" {
						req.Images[i] = resolved
					}
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				input, notices, diagnostics, err := resolveReplicateImageInput(schema, req)
				if err != nil {
					return ollamaGenerateResponse{}, nil, nil, err
				}
				job := &mediaJob{}
				client.jobSink = job
				resp, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (ollamaGenerateResponse, error) {
						return client.GenerateImage(jctx, req.Model, input)
					},
					client.CancelMediaJob,
					func(jctx context.Context, j mediaJob) (ollamaGenerateResponse, error) {
						return client.RecoverImageJob(jctx, j, req.Model)
					},
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				resp.Diagnostics = diagnostics
				return resp, nil, notices, genErr
			}
			resp, raw, err := app.ollamaClient(config.Providers.Ollama.BaseURL).GenerateImage(ctx, req)
			return resp, raw, nil, err
		}
		gateway.tools.GenerateVideo = func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
			// Cloud-only (fal or Replicate): airplane mode refuses the call.
			if err := airplaneMediaError(config, "generate_video"); err != nil {
				return GeneratedVideo{}, err
			}
			// Source media is normalized to a model-decodable format for every
			// backend (model_image_compat.go) before the provider branch — the
			// same rule GenerateImage applies at the top of its closure.
			req.Images = ensureModelSafeImages(ctx, config, req.Images)
			req.Image = ensureModelSafeImage(ctx, config, req.Image)
			if videoGenerationProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedVideo{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedVideo{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				// Pre-resolve attached source media: oversized payloads upload
				// through Replicate's Files API so the prediction input stays
				// within the inline data-URI budget. Only extend turns arrive
				// with a source video (motion control and video-reference
				// turns are refused up front by the executor and the
				// resolver), so the videos resolve exactly like the images.
				for i, img := range req.Images {
					if resolved, err := client.ResolveMediaURL(ctx, img, "image/png", fmt.Sprintf("source-image-%d.png", i)); err == nil && resolved != "" {
						req.Images[i] = resolved
					}
				}
				if resolved, err := client.ResolveMediaURL(ctx, req.Image, "image/png", "source-image.png"); err == nil {
					req.Image = resolved
				}
				videos := req.SourceVideos()
				for i := range videos {
					if resolved, err := client.ResolveMediaURL(ctx, videos[i], "video/mp4", fmt.Sprintf("source-video-%d.mp4", i)); err == nil && resolved != "" {
						videos[i] = resolved
					}
				}
				req.Videos = videos
				req.Video = ""
				schema := replicateSchemaCache.Get(ctx, req.Model)
				input, notices, diagnostics, err := resolveReplicateVideoInput(schema, req)
				if err != nil {
					return GeneratedVideo{}, err
				}
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedVideo, error) {
						return client.GenerateVideo(jctx, req.Model, input)
					},
					client.CancelMediaJob,
					client.RecoverVideoJob,
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				generated.Notices = notices
				generated.Diagnostics = diagnostics
				return generated, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedVideo{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedVideo{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Pre-resolve attached source media: one or more videos for extend,
			// motion control, or reference-to-video, and one or more images for
			// image-to-video / reference-to-video. Video sources upload to
			// fal's CDN unconditionally (the upscale/reframe/restyle/lipsync
			// rule): extend and video-to-video endpoints fetch video_url
			// server-side and cannot read an inline data URI — pixverse extend
			// failed with file_download_error on a 528KB clip that stayed
			// inline under the 1 MB threshold (conv_9543bb0fbd8a7026a9a15776).
			// Image sources keep the threshold-gated resolveMediaURL: image
			// endpoints accept data URIs broadly, and only oversized payloads
			// must leave the request. SourceVideos() unifies the legacy scalar
			// Video into the slice, so the resolver and transport below see
			// one list.
			videos := req.SourceVideos()
			// The source clips' billed input duration is parsed from the staged
			// data URLs BEFORE hosting replaces them with fal CDN URLs whose
			// bytes are no longer local (fal's token formula bills
			// input_video_duration alongside output_duration).
			sourceSeconds, _ := falVideoSourceSeconds(videos)
			for i := range videos {
				if resolved, err := client.resolveMediaURLHosted(ctx, videos[i], "video/mp4", fmt.Sprintf("source-video-%d.mp4", i)); err == nil && resolved != "" {
					videos[i] = resolved
				}
			}
			req.Videos = videos
			req.Video = ""
			for i, img := range req.Images {
				if resolved, err := client.resolveMediaURL(ctx, img, "image/png", fmt.Sprintf("source-image-%d.png", i)); err == nil && resolved != "" {
					req.Images[i] = resolved
				}
			}
			// Legacy scalar Image still flows through SourceImages(); resolve it too
			// for callers that populate the old field.
			if resolved, err := client.resolveMediaURL(ctx, req.Image, "image/png", "source-image.png"); err == nil {
				req.Image = resolved
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices, diagnostics, err := resolveVideoBody(schema, req, falOverrides)
			if err != nil {
				return GeneratedVideo{}, err
			}
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedVideo, error) {
					return client.GenerateVideo(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverVideoJob,
			)
			if genErr == nil {
				if recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				hints := falBillingHints{Requests: 1}
				if len(req.SourceVideos()) == 0 {
					// Pure generation: the rendered clip's own container is
					// the exact billed length (a planner-omitted or "auto"
					// duration is unknowable from the request), falling back
					// to the requested duration. Per-second models bill those
					// seconds directly; token-billed ones feed them into
					// fal's token formula with the effective resolution tier.
					if seconds, ok := falVideoBilledSeconds(generated.Data, req.Duration); ok {
						hints.Seconds = seconds
						hints.Tokens = falVideoTokenEstimate(schema, req, 0, seconds)
					}
				} else {
					// Video-source turns (extend/motion/reference): per-second
					// models read Duration, the billed extension length — the
					// rendered clip contains the source footage, so probing it
					// would overstate the bill. Token-billed models (seedance)
					// bill input_video_duration + output_duration per fal's
					// formula: the parsed source seconds plus the rendered
					// clip's own container, both measurable, so the estimate
					// covers the whole bill (zero source seconds — bytes never
					// local — skips it inside falVideoTokenEstimate).
					if seconds, ok := falDurationSeconds(req.Duration); ok {
						hints.Seconds = seconds
					}
					if seconds, ok := falVideoBilledSeconds(generated.Data, req.Duration); ok {
						hints.Tokens = falVideoTokenEstimate(schema, req, sourceSeconds, seconds)
					}
					// fal's seedance reference-to-video pages multiply the
					// price by 0.6 whenever the call carries video inputs.
					hints.PriceMultiplier = falVideoReferenceDiscount(req.Model)
				}
				// Megapixel-billed models (ltx-2.3-22b) read the rendered
				// frame size from the container plus the generated frame count
				// (container for pure turns, num_frames default when the
				// output embeds source footage).
				hints.Megapixels = falVideoBilledMegapixels(schema, req, generated.Data)
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			generated.Diagnostics = diagnostics
			return generated, genErr
		}
		gateway.tools.GenerateLipsync = func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error) {
			// Cloud-only (fal or Replicate): airplane mode refuses the call.
			if err := airplaneMediaError(config, "lip_sync"); err != nil {
				return GeneratedVideo{}, err
			}
			if resolveLipsyncProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedVideo{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedVideo{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				// The image face is normalized to a model-decodable format
				// first (model_image_compat.go) — a HEIC face source can't
				// ride a Replicate image input any better than a generation
				// source can.
				req.Image = ensureModelSafeImage(ctx, config, req.Image)
				// Pre-resolve the media references: oversized payloads upload
				// through Replicate's Files API so the prediction input stays
				// within the inline data-URI budget — lipsync media (a clip
				// plus its soundtrack) is always oversized in practice, the
				// generate_video path's rule.
				if resolved, err := client.ResolveMediaURL(ctx, req.Audio, "audio/mpeg", "audio.mp3"); err == nil && resolved != "" {
					req.Audio = resolved
				}
				if resolved, err := client.ResolveMediaURL(ctx, req.Video, "video/mp4", "face-video.mp4"); err == nil && resolved != "" {
					req.Video = resolved
				}
				if resolved, err := client.ResolveMediaURL(ctx, req.Image, "image/png", "face-image.png"); err == nil && resolved != "" {
					req.Image = resolved
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				body, notices, err := resolveLipsyncBody(schema, req, Overrides{}, "replicate-lipsync")
				if err != nil {
					return GeneratedVideo{Notices: notices}, err
				}
				// Lip sync returns a video, so it reuses the GenerateVideo
				// transport — the prediction lifecycle, cancellation, and
				// output scavenging are the video backend's. Replicate
				// reports no per-generation cost, so CostMicros stays unset
				// and the activity renders the CostUnknown "?" (the
				// dollar-cost invariant).
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedVideo, error) {
						return client.GenerateVideo(jctx, req.Model, body)
					},
					client.CancelMediaJob,
					client.RecoverVideoJob,
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				generated.Notices = notices
				return generated, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedVideo{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedVideo{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Pre-resolve all three media references with the force-host variant:
			// the driving audio and the face source (image or video). sync-lipsync
			// v3 rejects inline data URIs at the downstream layer (500
			// "downstream_service_error" on a data:image/...;base64 image_url — see
			// conv_b54423f43ab17a060948e74f), even though fal's own queue accepts
			// them and Seedance consumes the same inline payload fine. Hosting every
			// media reference sidesteps the downstream rejection; resolveMediaURLHosted
			// uploads regardless of size and falls back to an inline data URI on
			// upload failure so the request still goes through.
			//
			// The image face is normalized to a model-decodable format first
			// (model_image_compat.go) — a HEIC face source can't ride fal's
			// image_url any better than a generation source can.
			req.Image = ensureModelSafeImage(ctx, config, req.Image)
			// The audio's data URL is the only locally readable form of the
			// driving audio — capture it before the hosted swap below, for
			// the per-minute billing quantity (falLipsyncBillingSeconds).
			audioData := req.Audio
			if resolved, err := client.resolveMediaURLHosted(ctx, req.Audio, "audio/mpeg", "audio.mp3"); err == nil {
				req.Audio = resolved
			}
			if resolved, err := client.resolveMediaURLHosted(ctx, req.Video, "video/mp4", "face-video.mp4"); err == nil {
				req.Video = resolved
			}
			if resolved, err := client.resolveMediaURLHosted(ctx, req.Image, "image/png", "face-image.png"); err == nil {
				req.Image = resolved
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices, err := resolveLipsyncBody(schema, req, falOverrides, "lipsync")
			if err != nil {
				return GeneratedVideo{Notices: notices}, err
			}
			// Lip sync returns a video, so it reuses the GenerateVideo transport.
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedVideo, error) {
					return client.GenerateVideo(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverVideoJob,
			)
			if genErr == nil {
				if recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				// Sync-lipsync bills per minute of output video ($3/min on
				// v2): the rendered clip's own container prices it, with the
				// driving audio's duration as the stand-in. Before the
				// minutes unit was taught, the estimate priced to zero and
				// the activity stamped costUnknown
				// (conv_d5a03dfe20e5225352212c2b).
				hints := falBillingHints{Requests: 1}
				if seconds, ok := falLipsyncBillingSeconds(generated.Data, audioData); ok {
					hints.Seconds = seconds
				}
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.GenerateSFX = func(ctx context.Context, req SFXGenerateRequest) (SFXGenerateResult, error) {
			// Cloud-only (fal or Replicate, by Models.AudioProvider): airplane
			// mode refuses the call.
			if err := airplaneMediaError(config, "add_sound_effects"); err != nil {
				return SFXGenerateResult{}, err
			}
			// The source clip rides fal-hosted like every fal media reference (a
			// multi-MB video sent inline trips the queue's request-size cap). Its
			// data URL is the only locally readable form of the length — probe it
			// before the swap, so a call whose plan named no length soundtracks the
			// whole clip instead of the endpoints' short API default (10s on
			// mirelo). WebM/MOV sources the MP4 sniff can't read fall back to the
			// model's own default, the shared fail-soft rule. The probe is
			// provider-shared: the Replicate SFX models take the same duration
			// hint (mirelo clamps it to the video, mmaudio renders that much
			// sound), and its schema resolver carries the same bounds guard.
			if req.Duration <= 0 {
				if data, _, decErr := decodeMediaDataURL(req.Video); decErr == nil {
					if seconds, ok := mp4DurationSeconds(data); ok {
						req.Duration = seconds
					}
				}
			}
			if resolveAudioProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return SFXGenerateResult{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return SFXGenerateResult{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				if resolved, err := client.ResolveMediaURL(ctx, req.Video, "video/mp4", "sfx-source.mp4"); err == nil {
					req.Video = resolved
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				body, notices, err := resolveSFXBodyForCategory(schema, req, Overrides{}, "replicate-sfx")
				if err != nil {
					return SFXGenerateResult{}, err
				}
				// The SFX endpoints return a video with the soundtrack mixed in
				// or a standalone audio clip, so both the transport and the
				// recovery are kind-agnostic (RecoverSFXJob sniffs the same
				// ladder sfxFromPrediction runs). CostMicros stays unset:
				// Replicate reports no cost — the engine layer stamps
				// CostUnknown for the "?" render.
				job := &mediaJob{}
				client.jobSink = job
				result, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (SFXGenerateResult, error) {
						return client.GenerateSFX(jctx, req.Model, body)
					},
					client.CancelMediaJob,
					client.RecoverSFXJob,
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				if result.Video.Data != nil {
					result.Video.Notices = notices
				}
				if result.Audio.Data != nil {
					result.Audio.Notices = notices
				}
				return result, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return SFXGenerateResult{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return SFXGenerateResult{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			if resolved, err := client.resolveMediaURLHosted(ctx, req.Video, "video/mp4", "sfx-source.mp4"); err == nil {
				req.Video = resolved
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices, err := resolveSFXBody(schema, req, falOverrides)
			if err != nil {
				return SFXGenerateResult{}, err
			}
			// The SFX endpoints return a video with the soundtrack mixed in or a
			// standalone audio clip, so both the transport and the recovery are
			// kind-agnostic (RecoverSFXJob sniffs the same ladder sfxFromJob runs).
			job := &mediaJob{}
			client.jobSink = job
			result, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (SFXGenerateResult, error) {
					return client.GenerateSFX(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverSFXJob,
			)
			if genErr == nil {
				if recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				// The family bills per second of generated audio (mirelo, per
				// sample — the resolver pins one), so the length the body carried
				// prices it. When the probe never measured the source (a WebM
				// sniff miss), the rendered output's own container stands in —
				// the generated audio tracks the clip either way.
				hints := falBillingHints{Requests: 1}
				seconds, priced := req.Duration, req.Duration > 0
				if !priced {
					if result.Video.Data != nil {
						seconds, priced = mp4DurationSeconds(result.Video.Data)
					} else if result.Audio.Data != nil {
						seconds, priced = probeAudioDuration(result.Audio.Data)
					}
				}
				if priced {
					hints.Seconds = seconds
				}
				cost := app.estimateFalGenerationCost(ctx, config, req.Model, hints)
				if result.Video.Data != nil {
					result.Video.CostMicros = cost
					result.Video.Notices = notices
				}
				if result.Audio.Data != nil {
					result.Audio.CostMicros = cost
					result.Audio.Notices = notices
				}
			}
			return result, genErr
		}
		gateway.tools.UpscaleImage = func(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, []string, error) {
			// Cloud-only (Ollama has no upscaler): airplane mode refuses the call.
			if err := airplaneMediaError(config, "upscale_image"); err != nil {
				return ollamaGenerateResponse{}, nil, err
			}
			// The source image is normalized to a model-decodable format for
			// every backend (model_image_compat.go) before the provider branch.
			req.Image = ensureModelSafeImage(ctx, config, req.Image)
			// Upscale follows the image provider: replicate routes there (the
			// same seam generate_image reads), every other provider stays on
			// fal — the only cloud upscaler otherwise (Ollama has none).
			if imageGenerationProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return ollamaGenerateResponse{}, nil, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return ollamaGenerateResponse{}, nil, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				if resolved, err := client.ResolveMediaURL(ctx, req.Image, "image/png", "source-image.png"); err == nil {
					req.Image = resolved
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				input, notices, err := resolveReplicateUpscaleInput(schema, req)
				if err != nil {
					return ollamaGenerateResponse{}, notices, err
				}
				job := &mediaJob{}
				client.jobSink = job
				resp, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (ollamaGenerateResponse, error) {
						return client.UpscaleImage(jctx, req.Model, input)
					},
					client.CancelMediaJob,
					func(jctx context.Context, j mediaJob) (ollamaGenerateResponse, error) {
						return client.RecoverImageJob(jctx, j, req.Model)
					},
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				return resp, notices, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return ollamaGenerateResponse{}, nil, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return ollamaGenerateResponse{}, nil, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			job := &mediaJob{}
			client.jobSink = job
			resp, recovered, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (ollamaGenerateResponse, error) {
					return client.UpscaleImage(jctx, req)
				},
				client.CancelMediaJob,
				func(jctx context.Context, j mediaJob) (ollamaGenerateResponse, error) {
					return client.RecoverImageJob(jctx, j, req.Model)
				},
			)
			var notices []string
			if err == nil && recovered {
				notices = append(notices, mediaGenerationRecoveredNotice)
			}
			if err == nil {
				resp.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, falBillingHints{Images: 1, Requests: 1})
			}
			return resp, notices, err
		}
		gateway.tools.UpscaleVideo = func(ctx context.Context, req VideoUpscaleRequest) (GeneratedVideo, error) {
			// Cloud-only (fal or Replicate): airplane mode refuses the call.
			if err := airplaneMediaError(config, "upscale_video"); err != nil {
				return GeneratedVideo{}, err
			}
			// Video upscale follows the video provider: replicate routes there,
			// every other provider stays on fal — the same seam generate_video
			// reads.
			if videoGenerationProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedVideo{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedVideo{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				schema := replicateSchemaCache.Get(ctx, req.Model)
				// The resolver probes the source clip's frame size from the
				// inline data URL for tier-based models, so it runs BEFORE the
				// media upload swaps the value for a hosted URL.
				input, sourceKey, notices, err := resolveReplicateVideoUpscaleInput(schema, req)
				if err != nil {
					return GeneratedVideo{Notices: notices}, err
				}
				if sourceKey != "" {
					if resolved, err := client.ResolveMediaURL(ctx, req.Video, "video/mp4", "source-video.mp4"); err == nil && resolved != "" {
						input[sourceKey] = resolved
					}
				}
				// Upscaling returns a video, so it reuses the GenerateVideo
				// transport — the same pattern as the fal path below.
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedVideo, error) {
						return client.GenerateVideo(jctx, req.Model, input)
					},
					client.CancelMediaJob,
					client.RecoverVideoJob,
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				generated.Notices = notices
				return generated, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedVideo{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedVideo{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Pre-resolve the source clip with the force-host variant: an attached
			// video almost always exceeds fal's inline base64 limit, and the upscale
			// endpoints sit in the data-URI-rejecting camp downstream (the same
			// failure mode sync-lipsync v3 showed), so host it on fal's CDN
			// regardless of size. Fail-soft: on upload failure the inline data URI
			// is sent and fal's error surfaces verbatim.
			if resolved, err := client.resolveMediaURLHosted(ctx, req.Video, "video/mp4", "source-video.mp4"); err == nil {
				req.Video = resolved
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices, err := resolveVideoUpscaleBody(schema, req, falOverrides)
			if err != nil {
				return GeneratedVideo{Notices: notices}, err
			}
			// Upscaling returns a video, so it reuses the GenerateVideo transport
			// (the same pattern as GenerateLipsync).
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedVideo, error) {
					return client.GenerateVideo(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverVideoJob,
			)
			if genErr == nil {
				if recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				// Per-second upscalers bill the source clip's length, which the
				// gateway never probed — no estimate rather than a guess.
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, falBillingHints{Requests: 1})
			}
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.ReframeVideo = func(ctx context.Context, req VideoReframeRequest) (GeneratedVideo, error) {
			// Cloud-only (fal or Replicate): airplane mode refuses the call —
			// the local transform_video covers shape changes by crop/blur/pad.
			if err := airplaneMediaError(config, "reframe_video"); err != nil {
				return GeneratedVideo{}, err
			}
			// The duration guard (and the input-seconds billing) need the
			// source clip's own length, readable only while it is staged
			// inline — the branches below replace req.Video with a hosted URL
			// whose bytes are no longer local. Fail-soft: an unparseable
			// source disables the guard and falls back to the rendered clip
			// for billing, the same rule as pricing.
			sourceSeconds, sourceReadable := falVideoSourceSeconds([]string{req.Video})
			// Reframe follows the video provider: replicate routes there, every
			// other provider stays on fal — the same seam generate_video reads.
			if videoGenerationProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedVideo{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedVideo{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				if resolved, err := client.ResolveMediaURL(ctx, req.Video, "video/mp4", "source-video.mp4"); err == nil {
					req.Video = resolved
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				input, notices, err := resolveReplicateVideoReframeInput(schema, req)
				if err != nil {
					return GeneratedVideo{Notices: notices}, err
				}
				// Reframing returns a video, so it reuses the GenerateVideo
				// transport — the same pattern as the fal path below.
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedVideo, error) {
						return client.GenerateVideo(jctx, req.Model, input)
					},
					client.CancelMediaJob,
					client.RecoverVideoJob,
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				generated.Notices = notices
				if genErr == nil {
					noteReframeDurationMismatch(&generated, sourceSeconds, sourceReadable)
				}
				return generated, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedVideo{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedVideo{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Pre-resolve the source clip with the force-host variant, the same
			// rule as upscale: an attached video almost always exceeds fal's
			// inline base64 limit, and the reframe endpoints sit in the same
			// data-URI-rejecting camp downstream. Fail-soft: on upload failure
			// the inline data URI is sent and fal's error surfaces verbatim.
			if resolved, err := client.resolveMediaURLHosted(ctx, req.Video, "video/mp4", "source-video.mp4"); err == nil {
				req.Video = resolved
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices, err := resolveVideoReframeBody(schema, req, falOverrides)
			if err != nil {
				return GeneratedVideo{Notices: notices}, err
			}
			// Reframing returns a video, so it reuses the GenerateVideo transport
			// (the same pattern as UpscaleVideo / GenerateLipsync).
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedVideo, error) {
					return client.GenerateVideo(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverVideoJob,
			)
			if genErr == nil {
				if recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				// Reframe endpoints (LTX-2.3) bill per second of the INPUT
				// clip, so the staged source's own container is the billed
				// quantity — the rendered clip was long the proxy on the
				// preserve-length assumption, but the model can drift
				// (conv_0d756d03: 15.08s in, 14.2s out, ~6% underbilled).
				hints := falBillingHints{Requests: 1}
				if seconds, ok := reframeBilledSeconds(sourceSeconds, sourceReadable, generated.Data); ok {
					hints.Seconds = seconds
				}
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			if genErr == nil {
				noteReframeDurationMismatch(&generated, sourceSeconds, sourceReadable)
			}
			return generated, genErr
		}
		gateway.tools.RestyleVideo = func(ctx context.Context, req VideoRestyleRequest) (GeneratedVideo, error) {
			// Cloud-only (fal or Replicate): airplane mode refuses the call.
			if err := airplaneMediaError(config, "restyle_video"); err != nil {
				return GeneratedVideo{}, err
			}
			// Restyle follows the video provider: replicate routes there, every
			// other provider stays on fal — the same seam generate_video reads.
			if videoGenerationProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedVideo{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedVideo{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				if resolved, err := client.ResolveMediaURL(ctx, req.Video, "video/mp4", "source-video.mp4"); err == nil {
					req.Video = resolved
				}
				// Reference images are normalized to a model-decodable format
				// first (model_image_compat.go) — a HEIC character sheet can't
				// ride a Replicate reference input any better than fal's.
				req.Images = ensureModelSafeImages(ctx, config, req.Images)
				for i, img := range req.Images {
					if resolved, err := client.ResolveMediaURL(ctx, img, "image/png", fmt.Sprintf("style-reference-%d.png", i)); err == nil {
						req.Images[i] = resolved
					}
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				input, notices, diagnostics, err := resolveReplicateVideoRestyleInput(schema, req)
				if err != nil {
					return GeneratedVideo{Notices: notices}, err
				}
				// Restyling returns a video, so it reuses the GenerateVideo
				// transport — the same pattern as the fal path below.
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedVideo, error) {
						return client.GenerateVideo(jctx, req.Model, input)
					},
					client.CancelMediaJob,
					client.RecoverVideoJob,
				)
				if genErr == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				generated.Notices = notices
				generated.Diagnostics = diagnostics
				return generated, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedVideo{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedVideo{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Pre-resolve the source clip with the force-host variant, the same
			// rule as reframe: an attached video almost always exceeds fal's
			// inline base64 limit, and the restyle endpoints sit in the same
			// data-URI-rejecting camp downstream. Fail-soft: on upload failure
			// the inline data URI is sent and fal's error surfaces verbatim.
			if resolved, err := client.resolveMediaURLHosted(ctx, req.Video, "video/mp4", "source-video.mp4"); err == nil {
				req.Video = resolved
			}
			// Reference images are normalized to a model-decodable format first
			// (model_image_compat.go) — a HEIC character sheet can't ride fal's
			// image_urls any better than a face source can — and always hosted,
			// the lipsync rule: restyle endpoints reject inline data URIs
			// downstream too, and a reference is cheap to upload.
			req.Images = ensureModelSafeImages(ctx, config, req.Images)
			for i, img := range req.Images {
				if resolved, err := client.resolveMediaURLHosted(ctx, img, "image/png", fmt.Sprintf("style-reference-%d.png", i)); err == nil {
					req.Images[i] = resolved
				}
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices, err := resolveVideoRestyleBody(schema, req, falOverrides)
			if err != nil {
				return GeneratedVideo{Notices: notices}, err
			}
			// Restyling returns a video, so it reuses the GenerateVideo transport
			// (the same pattern as ReframeVideo / UpscaleVideo).
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, genErr := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedVideo, error) {
					return client.GenerateVideo(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverVideoJob,
			)
			if genErr == nil {
				if recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				// Restyle preserves length (Kling o3 and Wan re-render the
				// input's duration), so the rendered clip's own container
				// carries the billed duration — the same rule as reframe.
				// Flat per-video and per-request models price off Requests.
				hints := falBillingHints{Requests: 1}
				if seconds, ok := falVideoBilledSeconds(generated.Data, ""); ok {
					hints.Seconds = seconds
				}
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.GenerateAudio = func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
			// Speech routes by Models.SpeechProvider: the ollama branch runs
			// the local model over loopback — no key to load, and airplane
			// mode never applies. Sound generation (and speech on the fal or
			// replicate routes) falls through to the cloud paths below, which
			// is why the airplane refusal lives there and not here.
			if req.Kind == audioKindSpeech && resolveSpeechProvider(config) == "ollama" {
				generated, err := app.ollamaClient(config.Providers.Ollama.BaseURL).GenerateSpeech(ctx, req.Model, req.Voice, req.Prompt)
				if err == nil {
					// Emotion and speed are fal voice controls the local
					// speech model has no input for (the endpoint 400s on
					// speed) — dropped with a notice so the reply doesn't
					// imply they shaped the clip. Cost stays unset: a local
					// generation is free by nature, not unknown.
					if strings.TrimSpace(req.Emotion) != "" {
						generated.Notices = append(generated.Notices, "The local speech model has no emotion control, so the requested tone was not applied.")
					}
					if req.Speed != 0 {
						generated.Notices = append(generated.Notices, "The local speech model has no speed control, so the requested pace was not applied.")
					}
				}
				return generated, err
			}
			// Cloud-only below (Replicate for speech on the SpeechProvider
			// route and for sound on the AudioProvider route, fal for
			// everything else): airplane mode refuses the call.
			if err := airplaneMediaError(config, "audio generation"); err != nil {
				return GeneratedAudio{}, err
			}
			// Each kind routes by its own provider — speech by
			// Models.SpeechProvider, sound by Models.AudioProvider — and the
			// replicate branches run the prediction lifecycle and download
			// the clip. CostMicros stays unset (Replicate reports no cost; the
			// engine layer stamps CostUnknown for the "?" render). The speech
			// branch never carries a SourceAudio to resolve: cloning stays
			// fal-only and the executor stands the reference down.
			if req.Kind == audioKindSpeech && resolveSpeechProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedAudio{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedAudio{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				schema := replicateSchemaCache.Get(ctx, req.Model)
				body, notices, err := resolveReplicateSpeechInput(schema, req)
				if err != nil {
					return GeneratedAudio{Notices: notices}, err
				}
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedAudio, error) {
						return client.GenerateAudio(jctx, req.Model, body)
					},
					client.CancelMediaJob,
					client.RecoverAudioJob,
				)
				if err == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				generated.Notices = notices
				return generated, err
			}
			if req.Kind == audioKindSound && resolveAudioProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedAudio{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedAudio{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				schema := replicateSchemaCache.Get(ctx, req.Model)
				body, notices, err := resolveReplicateAudioInput(schema, req)
				if err != nil {
					return GeneratedAudio{Notices: notices}, err
				}
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedAudio, error) {
						return client.GenerateAudio(jctx, req.Model, body)
					},
					client.CancelMediaJob,
					client.RecoverAudioJob,
				)
				if err == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				generated.Notices = notices
				return generated, err
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedAudio{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedAudio{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Pre-resolve a voice-cloning reference clip the way transcribe does:
			// a 10+ second reference can exceed fal's inline data-URI limit, so
			// upload it to CDN first when oversized. Fail-soft — fal's own error
			// surfaces if the inline form is also rejected.
			if strings.TrimSpace(req.SourceAudio) != "" {
				if resolved, err := client.resolveMediaURL(ctx, req.SourceAudio, "audio/mpeg", "voice-reference.mp3"); err == nil {
					req.SourceAudio = resolved
				}
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices := resolveAudioBody(schema, req, falOverrides)
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedAudio, error) {
					return client.GenerateAudio(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverAudioJob,
			)
			if err == nil {
				if recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				// Character-billed TTS models price the spoken text; per-second
				// sound models (elevenlabs) the rendered clip's own length;
				// flat per-request models ignore both via the unit mapping.
				hints := falBillingHints{
					Requests:   1,
					Characters: len([]rune(req.Prompt)),
				}
				if seconds, ok := audioBillingSeconds(req.Duration, generated.Data); ok {
					hints.Seconds = seconds
				}
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			return generated, err
		}
		gateway.tools.GenerateVoiceDesign = func(ctx context.Context, req VoiceDesignRequest) (GeneratedAudio, error) {
			// fal-only: airplane mode refuses the call.
			if err := airplaneMediaError(config, "voice design"); err != nil {
				return GeneratedAudio{}, err
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedAudio{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedAudio{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// The voice-design endpoints take the two plain strings directly —
			// prompt + preview_text — so there is no schema resolution here,
			// only the transport, the timeout policy, and the estimate.
			body := map[string]any{
				"prompt":       req.Description,
				"preview_text": req.PreviewText,
			}
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedAudio, error) {
					return client.GenerateVoiceDesign(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverAudioJob,
			)
			if err == nil && recovered {
				generated.Notices = append(generated.Notices, mediaGenerationRecoveredNotice)
			}
			if err == nil {
				// fal prices the default voice-design endpoint per "cloned
				// voices" ($3 per designed voice — one per call), so
				// Requests carries the estimate. The character hint stays
				// for alternate endpoints in the slot priced per preview
				// character.
				hints := falBillingHints{
					Requests:   1,
					Characters: len([]rune(req.PreviewText)),
				}
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			return generated, err
		}
		gateway.tools.GenerateAudioExtend = func(ctx context.Context, req AudioExtendRequest) (GeneratedAudio, error) {
			// Cloud-only (fal or Replicate, by Models.AudioProvider): airplane
			// mode refuses the call.
			if err := airplaneMediaError(config, "extend_audio"); err != nil {
				return GeneratedAudio{}, err
			}
			// Probe the source clip's length while its bytes are still at hand:
			// mask-based endpoints (stable-audio inpaint) need it to place the
			// mask, the replicate continuation total is source plus added, and
			// after the upload below only a URL remains.
			if seconds, ok := dataURLAudioDuration(req.SourceAudio); ok {
				req.SourceDurationSeconds = seconds
			}
			if resolveAudioProvider(config) == "replicate" {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedAudio{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedAudio{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				// The continuation models take a URI input, so the source rides
				// the Files API above the inline threshold — the lipsync
				// pattern. Fail-soft; the model's own error surfaces otherwise.
				if strings.TrimSpace(req.SourceAudio) != "" {
					if resolved, err := client.ResolveMediaURL(ctx, req.SourceAudio, "audio/mpeg", "source-audio.mp3"); err == nil {
						req.SourceAudio = resolved
					}
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				body, notices, err := resolveReplicateAudioExtendInput(schema, req)
				if err != nil {
					return GeneratedAudio{Notices: notices}, err
				}
				job := &mediaJob{}
				client.jobSink = job
				generated, recovered, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedAudio, error) {
						return client.GenerateAudio(jctx, req.Model, body)
					},
					client.CancelMediaJob,
					client.RecoverAudioJob,
				)
				if err == nil && recovered {
					notices = append(notices, mediaGenerationRecoveredNotice)
				}
				// CostMicros stays unset: Replicate reports no cost — the
				// engine layer stamps CostUnknown for the "?" render.
				generated.Notices = notices
				return generated, err
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedAudio{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedAudio{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// sonauto/v2/extend rejects inline data URIs ("must be a valid
			// publicly accessible URL"), so the source always goes through fal's
			// CDN storage — the lipsync pattern — instead of the ≤1MB inline
			// path the voice reference uses. Fail-soft; fal's own error surfaces
			// if the inline form is also rejected.
			if strings.TrimSpace(req.SourceAudio) != "" {
				if resolved, err := client.resolveMediaURLHosted(ctx, req.SourceAudio, "audio/mpeg", "source-audio.mp3"); err == nil {
					req.SourceAudio = resolved
				}
			}
			schema := schemaCache.Get(ctx, req.Model)
			body, notices, err := resolveAudioExtendBody(schema, req, falOverrides)
			if err != nil {
				return GeneratedAudio{Notices: notices}, err
			}
			job := &mediaJob{}
			client.jobSink = job
			generated, recovered, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedAudio, error) {
					return client.GenerateAudio(jctx, req.Model, body)
				},
				client.CancelMediaJob,
				client.RecoverAudioJob,
			)
			if err == nil && recovered {
				notices = append(notices, mediaGenerationRecoveredNotice)
			}
			if err == nil {
				// Duration is the ADDED length — what a per-second model bills.
				hints := falBillingHints{Requests: 1}
				if seconds, ok := falDurationSeconds(req.Duration); ok {
					hints.Seconds = seconds
				}
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			return generated, err
		}
		gateway.tools.TranscribeAudio = func(ctx context.Context, req TranscribeAudioRequest) (GeneratedTranscript, error) {
			// Airplane mode refuses the call here (the resolver routes to the
			// local whisper backend in that mode, so this wrapper is the belt
			// behind the executor's dispatch). The cloud dispatch below routes
			// by the resolved transcription provider: replicate when pinned,
			// fal otherwise — the auto chain never resolves replicate.
			if err := airplaneMediaError(config, "transcribe_audio"); err != nil {
				return GeneratedTranscript{}, err
			}
			if resolveTranscriptionProvider(config) == transcriptionProviderReplicate {
				apiKey, err := loadReplicateAPIKey()
				if err != nil {
					return GeneratedTranscript{}, err
				}
				if strings.TrimSpace(apiKey) == "" {
					return GeneratedTranscript{}, errReplicateKeyNotConfigured
				}
				client := newReplicateClient(app.client, apiKey)
				// Pre-resolve the audio clip: a long voice memo can exceed the
				// inline data-URI budget, so it uploads through the Files API
				// first when oversized — the fal resolveMediaURL pattern, on
				// Replicate's own storage.
				if resolved, err := client.ResolveMediaURL(ctx, req.Audio, "audio/mpeg", "audio.mp3"); err == nil && resolved != "" {
					req.Audio = resolved
				}
				schema := replicateSchemaCache.Get(ctx, req.Model)
				input, notices, err := resolveReplicateTranscribeInput(schema, req)
				if err != nil {
					return GeneratedTranscript{Notices: notices}, err
				}
				job := &mediaJob{}
				client.jobSink = job
				// Transcription rides the timeout policy cancel-only on both
				// cloud backends: an abandoned prediction is stopped, but
				// there is no recover step — a transcript is cheap to redo.
				generated, _, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
					func(jctx context.Context) (GeneratedTranscript, error) {
						return client.TranscribeAudio(jctx, req, input)
					},
					client.CancelMediaJob,
					nil,
				)
				if err == nil {
					// Replicate reports no per-generation cost — the image and
					// video backends' CostUnknown stance.
					generated.Notices = append(generated.Notices, notices...)
				}
				return generated, err
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedTranscript{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedTranscript{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Pre-resolve the audio clip: a long voice memo can exceed fal's inline
			// size limit, so upload it to CDN first when oversized.
			// Duration for the per-second billing hint is probed while the clip
			// is still inline — after resolution only a URL remains.
			hints := falBillingHints{Requests: 1}
			if seconds, ok := dataURLAudioDuration(req.Audio); ok {
				hints.Seconds = seconds
			}
			if resolved, err := client.resolveMediaURL(ctx, req.Audio, "audio/mpeg", "audio.mp3"); err == nil {
				req.Audio = resolved
			}
			// Transcription rides the timeout policy cancel-only: an abandoned
			// wizper job is stopped, but there is no recover step — a
			// transcript is cheap to redo.
			job := &mediaJob{}
			client.jobSink = job
			generated, _, err := runMediaGeneration(ctx, mediaGenerationTimeout(config), job,
				func(jctx context.Context) (GeneratedTranscript, error) {
					return client.TranscribeAudio(jctx, req)
				},
				client.CancelMediaJob,
				nil,
			)
			if err == nil {
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			return generated, err
		}
		// open_editor's side effect is the UI directive, not local work — the
		// event IS the execution, so an undeliverable launch fails the call
		// rather than letting evidence claim an editor that never opened.
		gateway.tools.OpenEditor = func(kind, conversationID, artifactID string, seedArtifactIDs []string) error {
			if !app.emitEditorLaunchEvent(EditorLaunchEvent{Kind: kind, ConversationID: conversationID, ArtifactID: artifactID, SeedArtifactIDs: seedArtifactIDs}) {
				return errors.New("the editor launch could not be delivered — no UI is attached")
			}
			return nil
		}
	}
	// Local whisper transcription needs neither an API key nor an HTTP client,
	// so it wires outside the app block (tests pass a nil app). The runner
	// re-resolves the binary per call, so a Settings change takes effect on the
	// next tool call without rebuilding the gateway.
	if _, ok := resolveLocalWhisperBinary(config); ok {
		gateway.tools.TranscribeAudioLocal = func(ctx context.Context, req TranscribeAudioRequest) (GeneratedTranscript, []string, error) {
			return runLocalWhisperTranscription(ctx, config, req)
		}
	}
	return gateway
}

func (g ToolGateway) Execute(ctx context.Context, req ToolExecutionRequest) HarnessToolResult {
	call := req.Call
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(call.Name)
	}
	if name == "" {
		return HarnessToolResult{Status: "failed", Summary: "tool not recognized", Error: "tool name is required"}
	}
	call.Name = name

	result := HarnessToolResult{Name: name, Status: "completed"}
	definition, ok := g.registry.Get(name)
	if !ok {
		result.Status = "failed"
		result.Error = fmt.Sprintf("unknown tool %q", name)
		result.Summary = "tool not recognized"
		return result
	}
	requiresPermission := definition.RequiresPermissionFor(call) || g.requiresUnlistedCommandPermission(call)
	var permission *ToolPermissionDecision
	if requiresPermission {
		decision := g.requestPermission(ctx, req, definition, call)
		if !decision.Approved {
			return HarnessToolResult{Name: name, Status: "denied", Summary: definition.Title + " was not approved", Error: "permission denied", Permission: &decision}
		}
		permission = &decision
	}
	tools := g.tools
	if g.requiresUnlistedCommandPermission(call) {
		tools.Filesystem = tools.Filesystem.withApprovedUnlistedCommand(call.Command)
	}
	output, summary, err := definition.Execute(ctx, tools, call)
	result.Result = output
	result.Summary = summary
	result.Permission = permission
	if np, ok := output.(NoticeProvider); ok {
		result.Notices = np.ToolNotices()
	}
	if dp, ok := output.(DiagnosticProvider); ok {
		result.Diagnostics = dp.ToolDiagnostics()
	}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Summary = name + " failed"
	} else if toolError := harnessToolOutputError(output); toolError != "" {
		result.Status = "failed"
		result.Error = toolError
	}
	return result
}

func (g ToolGateway) requiresUnlistedCommandPermission(call HarnessToolCall) bool {
	if strings.TrimSpace(call.Name) != "run_command" {
		return false
	}
	if g.tools.Filesystem == nil {
		return false
	}
	name := normalizedCommandName(call.Command)
	return name != "" && !commandAllowed(name, g.tools.Filesystem.config.AllowedCommands)
}

func (g ToolGateway) requestPermission(ctx context.Context, req ToolExecutionRequest, definition HarnessToolDefinition, call HarnessToolCall) ToolPermissionDecision {
	if g.permissionRequester == nil {
		// Nobody can approve: fail closed.
		return ToolPermissionDecision{Outcome: permissionOutcomeDenied}
	}
	event := ToolPermissionRequestEvent{}
	if definition.Permission != nil {
		event = definition.Permission(call)
	}
	if strings.TrimSpace(event.Summary) == "" {
		event.Summary = definition.Title
	}
	event.ID = randomID("permission")
	event.RequestID = req.RequestID
	event.ConversationID = req.ConversationID
	event.ToolName = call.Name
	event.Action = call.Name
	return g.permissionRequester(ctx, event)
}
