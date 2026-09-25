package main

import (
	"context"
	"fmt"
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
// video-source transforms (upscale/reframe/restyle), lipsync, and audio stay
// fal-only regardless — they are separate tools with their own fal-key gates.
func videoGenerationProvider(config AppConfig) string {
	switch strings.TrimSpace(config.Models.VideoProvider) {
	case "replicate":
		return "replicate"
	default:
		return "fal"
	}
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
				resp, raw, genErr := client.GenerateImage(ctx, req.Model, body)
				if genErr == nil {
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
				return resp, raw, notices, genErr
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
				input, notices, err := resolveReplicateImageInput(schema, req)
				if err != nil {
					return ollamaGenerateResponse{}, nil, nil, err
				}
				resp, genErr := client.GenerateImage(ctx, req.Model, input)
				return resp, nil, notices, genErr
			}
			resp, raw, err := app.ollamaClient(config.Providers.Ollama.BaseURL).GenerateImage(ctx, req)
			return resp, raw, nil, err
		}
		gateway.tools.GenerateVideo = func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
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
				input, notices, err := resolveReplicateVideoInput(schema, req)
				if err != nil {
					return GeneratedVideo{}, err
				}
				generated, genErr := client.GenerateVideo(ctx, req.Model, input)
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
			// Pre-resolve attached source media: one or more videos for extend,
			// motion control, or reference-to-video, and one or more images for
			// image-to-video / reference-to-video. Oversized payloads upload to
			// fal's CDN so the queue submit stays under the inline size limit.
			// SourceVideos() unifies the legacy scalar Video into the slice, so
			// the resolver and transport below see one list.
			videos := req.SourceVideos()
			for i := range videos {
				if resolved, err := client.resolveMediaURL(ctx, videos[i], "video/mp4", fmt.Sprintf("source-video-%d.mp4", i)); err == nil && resolved != "" {
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
			body, notices, err := resolveVideoBody(schema, req, falOverrides)
			if err != nil {
				return GeneratedVideo{}, err
			}
			generated, genErr := client.GenerateVideo(ctx, req.Model, body)
			if genErr == nil {
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
						hints.Tokens = falVideoTokenEstimate(schema, req, seconds)
					}
				} else if seconds, ok := falDurationSeconds(req.Duration); ok {
					// Video-source turns (extend/motion): the rendered clip
					// contains the source footage, so probing it would
					// overstate what a per-second model bills — Duration
					// names the billed extension length here.
					hints.Seconds = seconds
				}
				// Megapixel-billed models (ltx-2.3-22b) read the rendered
				// frame size from the container plus the generated frame count
				// (container for pure turns, num_frames default when the
				// output embeds source footage).
				hints.Megapixels = falVideoBilledMegapixels(schema, req, generated.Data)
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.GenerateLipsync = func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error) {
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
			body, notices, err := resolveLipsyncBody(schema, req, falOverrides)
			if err != nil {
				return GeneratedVideo{Notices: notices}, err
			}
			// Lip sync returns a video, so it reuses the GenerateVideo transport.
			generated, genErr := client.GenerateVideo(ctx, req.Model, body)
			if genErr == nil {
				// Output length ≈ the driving audio's length, which the
				// gateway never knows — per-second lipsync endpoints get no
				// estimate rather than a guessed duration.
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, falBillingHints{Requests: 1})
			}
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.UpscaleImage = func(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, []string, error) {
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
				resp, genErr := client.UpscaleImage(ctx, req.Model, input)
				return resp, notices, genErr
			}
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return ollamaGenerateResponse{}, nil, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return ollamaGenerateResponse{}, nil, errFalKeyNotConfigured
			}
			resp, err := newFalClient(app.client, apiKey).UpscaleImage(ctx, req)
			if err == nil {
				resp.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, falBillingHints{Images: 1, Requests: 1})
			}
			return resp, nil, err
		}
		gateway.tools.UpscaleVideo = func(ctx context.Context, req VideoUpscaleRequest) (GeneratedVideo, error) {
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
				generated, genErr := client.GenerateVideo(ctx, req.Model, input)
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
			generated, genErr := client.GenerateVideo(ctx, req.Model, body)
			if genErr == nil {
				// Per-second upscalers bill the source clip's length, which the
				// gateway never probed — no estimate rather than a guess.
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, falBillingHints{Requests: 1})
			}
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.ReframeVideo = func(ctx context.Context, req VideoReframeRequest) (GeneratedVideo, error) {
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
				generated, genErr := client.GenerateVideo(ctx, req.Model, input)
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
			generated, genErr := client.GenerateVideo(ctx, req.Model, body)
			if genErr == nil {
				// Reframe endpoints (LTX-2.3) bill per second of the INPUT clip,
				// and a reframe preserves length, so the rendered clip's own
				// container carries the billed duration — measurable here, where
				// upscale's source seconds were not.
				hints := falBillingHints{Requests: 1}
				if seconds, ok := falVideoBilledSeconds(generated.Data, ""); ok {
					hints.Seconds = seconds
				}
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.RestyleVideo = func(ctx context.Context, req VideoRestyleRequest) (GeneratedVideo, error) {
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
				input, notices, err := resolveReplicateVideoRestyleInput(schema, req)
				if err != nil {
					return GeneratedVideo{Notices: notices}, err
				}
				// Restyling returns a video, so it reuses the GenerateVideo
				// transport — the same pattern as the fal path below.
				generated, genErr := client.GenerateVideo(ctx, req.Model, input)
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
			generated, genErr := client.GenerateVideo(ctx, req.Model, body)
			if genErr == nil {
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
			generated, err := client.GenerateAudio(ctx, req.Model, body)
			if err == nil {
				// Character-billed TTS models price the spoken text; per-request
				// models ignore the character count via the unit mapping.
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, falBillingHints{
					Requests:   1,
					Characters: len([]rune(req.Prompt)),
				})
			}
			generated.Notices = notices
			return generated, err
		}
		gateway.tools.GenerateAudioExtend = func(ctx context.Context, req AudioExtendRequest) (GeneratedAudio, error) {
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return GeneratedAudio{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return GeneratedAudio{}, errFalKeyNotConfigured
			}
			client := newFalClient(app.client, apiKey)
			// Probe the source clip's length while its bytes are still at hand:
			// mask-based endpoints (stable-audio inpaint) need it to place the
			// mask, and after the upload below only a URL remains.
			if seconds, ok := dataURLAudioDuration(req.SourceAudio); ok {
				req.SourceDurationSeconds = seconds
			}
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
			generated, err := client.GenerateAudio(ctx, req.Model, body)
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
			generated, err := client.TranscribeAudio(ctx, req)
			if err == nil {
				generated.CostMicros = app.estimateFalGenerationCost(ctx, config, req.Model, hints)
			}
			return generated, err
		}
	}
	// Local whisper transcription needs neither an API key nor an HTTP client,
	// so it wires outside the app block (tests pass a nil app). The runner
	// re-resolves the binary per call, so a Settings change takes effect on the
	// next tool call without rebuilding the gateway.
	if _, ok := resolveLocalWhisperBinary(config); ok {
		gateway.tools.TranscribeAudioLocal = func(ctx context.Context, req TranscribeAudioRequest) (GeneratedTranscript, error) {
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
