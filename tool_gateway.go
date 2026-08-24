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
	case "openai-compatible":
		return "openai-compatible"
	default:
		return "ollama"
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
		schemaCache := newFalSchemaCache(app.client, config.Storage.Root)
		falOverrides := loadFalOverrides(config.Storage.Root)
		gateway.tools.GenerateImage = func(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, []string, error) {
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
			resp, raw, err := app.ollamaClient(config.Providers.Ollama.BaseURL).GenerateImage(ctx, req)
			return resp, raw, nil, err
		}
		gateway.tools.GenerateVideo = func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error) {
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
			generated.Notices = notices
			return generated, genErr
		}
		gateway.tools.UpscaleImage = func(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, error) {
			apiKey, err := loadFalAPIKey()
			if err != nil {
				return ollamaGenerateResponse{}, err
			}
			if strings.TrimSpace(apiKey) == "" {
				return ollamaGenerateResponse{}, errFalKeyNotConfigured
			}
			return newFalClient(app.client, apiKey).UpscaleImage(ctx, req)
		}
		gateway.tools.UpscaleVideo = func(ctx context.Context, req VideoUpscaleRequest) (GeneratedVideo, error) {
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
			schema := schemaCache.Get(ctx, req.Model)
			body, notices := resolveAudioBody(schema, req, falOverrides)
			generated, err := client.GenerateAudio(ctx, req.Model, body)
			generated.Notices = notices
			return generated, err
		}
		gateway.tools.TranscribeAudio = func(ctx context.Context, model, audioURL, task, language string) (GeneratedTranscript, error) {
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
			if resolved, err := client.resolveMediaURL(ctx, audioURL, "audio/mpeg", "audio.mp3"); err == nil {
				audioURL = resolved
			}
			return client.TranscribeAudio(ctx, model, audioURL, task, language)
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
