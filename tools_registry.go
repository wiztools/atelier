package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type HarnessToolRisk string

const (
	HarnessToolRiskRead  HarnessToolRisk = "read"
	HarnessToolRiskWrite HarnessToolRisk = "write"
	HarnessToolRiskExec  HarnessToolRisk = "exec"
)

type HarnessToolDefinition struct {
	Name        string
	Title       string
	Description string
	Example     string
	Risk        HarnessToolRisk
	// ParamSchema is the JSON Schema for the tool's arguments, used to declare
	// the tool to Ollama's native function-calling API. It mirrors the rules
	// enforced procedurally by Validate, which stays as a runtime backstop.
	ParamSchema     map[string]any
	Validate        func(prefix string, call HarnessToolCall) []string
	Execute         func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error)
	NeedsPermission func(call HarnessToolCall) bool
	Permission      func(call HarnessToolCall) ToolPermissionRequestEvent
	Activity        func(result HarnessToolResult) HarnessToolActivity
}

type HarnessToolExecutionContext struct {
	Config     AppConfig
	Filesystem *FilesystemToolLayer
	// AttachedImages are the source frames (base64 data URLs) the user attached
	// to the current turn, if any. generate_video uses them to animate or
	// reference-combine via an image-to-video or reference-to-video model; a
	// multi-image request against a single-image model fails the call. Empty for
	// the direct/UI tool path.
	AttachedImages []string
	// AttachedAudio is the audio clip (a data URL) the user attached to the
	// current turn, if any. transcribe_audio consumes it via fal's Whisper/Wizper.
	// Like AttachedImage it is provider-agnostic: the planner decides whether to
	// transcribe it (any provider) or, on OpenRouter, send it as chat input.
	AttachedAudio string
	// AttachedVideo is the video clip (a data URL) the user attached to the
	// current turn, if any. generate_video uses it as the source clip for Veo
	// extend or, alongside AttachedImages, for motion control; the lip sync tool
	// uses it (with AttachedAudio) for video-to-video lip sync. Tool-only: it
	// never reaches a chat model.
	AttachedVideo string
	GenerateImage func(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, []string, error)
	GenerateVideo func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error)
	GenerateAudio func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error)
	// GenerateLipsync runs a lip sync generation — an audio clip drives a face
	// (image for audio-to-video, video for video-to-video). It returns a video
	// (same transport as GenerateVideo) plus resolver notices.
	GenerateLipsync func(ctx context.Context, req LipsyncGenerateRequest) (GeneratedVideo, error)
	TranscribeAudio func(ctx context.Context, model, audioURL, task, language string) (GeneratedTranscript, error)
	UpscaleImage    func(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, error)
	// UpscaleVideo raises an attached clip's resolution via fal's video-upscaler
	// endpoints. It returns a video (same transport as GenerateVideo) plus
	// resolver notices — the video sibling of UpscaleImage.
	UpscaleVideo func(ctx context.Context, req VideoUpscaleRequest) (GeneratedVideo, error)
}

// ToolImageResult carries generated images as data URLs. The Images field is
// stripped before the result is rendered into a tool message so base64 data
// never enters a model context; the harness extracts it for the UI and history.
type ToolImageResult struct {
	Model   string   `json:"model"`
	Prompt  string   `json:"prompt"`
	Count   int      `json:"count"`
	Images  []string `json:"images,omitempty"`
	Notices []string `json:"notices,omitempty"`
}

// ToolVideoResult carries generated videos as on-disk temp-file references, not
// bytes — video is a file-path artifact end to end. The Videos slice is stripped
// before the result is rendered into a tool message (the temp path is not useful
// model evidence); the harness moves each temp file into the conversation's
// artifacts directory when it persists the turn. Notices carries deterministic
// caveats surfaced via NoticeProvider, matching ToolImageResult/ToolAudioResult.
type ToolVideoResult struct {
	Model   string          `json:"model"`
	Prompt  string          `json:"prompt"`
	Count   int             `json:"count"`
	Videos  []ToolVideoFile `json:"videos,omitempty"`
	Notices []string        `json:"notices,omitempty"`
}

type ToolVideoFile struct {
	TempPath  string `json:"tempPath,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
	SourceURL string `json:"sourceUrl,omitempty"`
}

// ToolAudioResult mirrors ToolVideoResult for generated audio: on-disk temp-file
// references, not bytes. The Audios slice is stripped before the result becomes
// a tool message; the harness moves each temp file into the artifacts directory.
type ToolAudioResult struct {
	Model   string          `json:"model"`
	Prompt  string          `json:"prompt"`
	Count   int             `json:"count"`
	Audios  []ToolAudioFile `json:"audios,omitempty"`
	Notices []string        `json:"notices,omitempty"`
}

// ToolTranscribeResult carries the transcript of an audio clip. Unlike the
// media results it holds plain text (the transcript), which rides the standard
// role:"tool" evidence path verbatim — no media slice to strip. Notices carries
// deterministic caveats surfaced via NoticeProvider, matching ToolAudioResult.
type ToolTranscribeResult struct {
	Model      string   `json:"model"`
	Transcript string   `json:"transcript"`
	Notices    []string `json:"notices,omitempty"`
}

// ToolNotices reports deterministic, user-facing caveats produced while
// generating the audio (e.g. a requested loop the model can't honor).
func (r ToolAudioResult) ToolNotices() []string { return r.Notices }

// ToolNotices reports deterministic, user-facing caveats produced while
// resolving the video body (e.g. a model with no source-video input when the
// user attached a video to extend, or an unavailable schema).
func (r ToolVideoResult) ToolNotices() []string { return r.Notices }

// ToolNotices reports deterministic, user-facing caveats produced while
// resolving the image body (e.g. a model with no source-image input when the
// user attached an image, or an unavailable schema).
func (r ToolImageResult) ToolNotices() []string { return r.Notices }

// ToolNotices reports deterministic, user-facing caveats produced while
// transcribing (e.g. an auto-detected language the user may want to confirm).
func (r ToolTranscribeResult) ToolNotices() []string { return r.Notices }

// NoticeProvider lets a tool's output carry deterministic user-facing caveats
// that the harness surfaces verbatim in the chat reply.
type NoticeProvider interface{ ToolNotices() []string }

type ToolAudioFile struct {
	TempPath  string `json:"tempPath,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
	SourceURL string `json:"sourceUrl,omitempty"`
}

// GeneratedVideo and GeneratedAudio are field-identical (Data/MimeType/
// SourceURL), and ToolVideoFile/ToolAudioFile are too (TempPath/MimeType/
// SourceURL). The media-writer helpers below take the fields they need as
// primitives rather than threading the concrete type through, so one helper
// body serves both media kinds without a per-kind copy.

type HarnessToolRegistry struct {
	definitions []HarnessToolDefinition
	byName      map[string]HarnessToolDefinition
}

func newHarnessToolExecutionContext(config AppConfig) HarnessToolExecutionContext {
	return HarnessToolExecutionContext{
		Config:     config,
		Filesystem: newFilesystemToolLayer(config.Tools.Filesystem),
	}
}

// defaultHarnessToolRegistry builds the per-turn tool catalog. ctx and app are
// used only for capability introspection (videoModelSupportsAudio) — a nil app
// or failed schema lookup degrades gracefully to the generic tool wording, so
// callers without an App (tests, the gateway's rebuild path) pass nil safely.
// The registry is built once per engine via sync.Once, so the schema reads happen
// at most once per stream against the disk-backed cache (7-day TTL).
func defaultHarnessToolRegistry(ctx context.Context, config AppConfig, app *App) HarnessToolRegistry {
	definitions := filesystemToolDefinitions(config.Tools.Filesystem)
	if imageGenerationConfigured(config) {
		definitions = append(definitions, imageGenerationToolDefinition())
	}
	videoAudioCapable := false
	if videoGenerationConfigured(config) {
		videoAudioCapable = videoModelSupportsAudio(ctx, config, app)
		definitions = append(definitions, videoGenerationToolDefinition(videoAudioCapable))
	}
	if speechGenerationConfigured(config) {
		definitions = append(definitions, speechGenerationToolDefinition(videoAudioCapable))
	}
	if soundEffectsGenerationConfigured(config) {
		definitions = append(definitions, soundEffectsGenerationToolDefinition())
	}
	if transcribeAudioConfigured(config) {
		definitions = append(definitions, transcribeAudioToolDefinition())
	}
	if lipsyncConfigured(config) {
		definitions = append(definitions, lipsyncToolDefinition(videoAudioCapable))
	}
	if imageUpscaleConfigured(config) {
		definitions = append(definitions, imageUpscaleToolDefinition())
	}
	if videoUpscaleConfigured(config) {
		definitions = append(definitions, videoUpscaleToolDefinition())
	}
	return newHarnessToolRegistry(definitions)
}

// videoModelSupportsAudio reports whether any configured video model exposes a
// native generate_audio input — i.e. the model can render a video with
// synchronized audio (speech, music, ambient) from the prompt alone, no
// separate generate_speech + lip_sync chain needed. The check mirrors the
// runtime resolver (findNative "video" "generateAudio") so the planner's tool
// description and the execution-time body building cannot disagree about a
// model's audio capability.
//
// A nil app, an unavailable schema (offline, fetch failed), or a nil schema
// all report false: the description then stays generic and the planner falls
// back to today's behavior. findNative is nil-schema-safe (returns false), so
// the override path still works even when the network is unreachable. The
// lookup is gated by videoGenerationConfigured upstream, so this only runs when
// a fal key is present; the OpenAPI endpoint is public but the shared fetch
// path loads the key the same way generation calls do.
func videoModelSupportsAudio(ctx context.Context, config AppConfig, app *App) bool {
	if app == nil || app.client == nil {
		return false
	}
	cache := newFalSchemaCache(app.client, config.Storage.Root)
	overrides := loadFalOverrides(config.Storage.Root)
	for _, model := range []string{
		resolveDefaultVideoModel(config),
		resolveDefaultVideoImageModel(config),
		resolveDefaultVideoExtendModel(config),
		resolveDefaultVideoMotionModel(config),
	} {
		if strings.TrimSpace(model) == "" {
			continue
		}
		schema := cache.Get(ctx, model)
		if _, _, ok := findNative(schema, overrides, "video", model, "generateAudio"); ok {
			return true
		}
	}
	return false
}

// imageGenerationConfigured reports whether any image-generation backend is
// ready to serve a generate_image call: the Ollama image model is set, or fal.ai
// is the selected image provider with a model configured.
func imageGenerationConfigured(config AppConfig) bool {
	if strings.TrimSpace(config.Providers.Ollama.Models.Image) != "" {
		return true
	}
	return strings.TrimSpace(config.Models.ImageProvider) == "fal" &&
		strings.TrimSpace(config.Providers.Fal.Model) != ""
}

// resolveDefaultImageModel returns the image model the generate_image tool
// should use when the call doesn't override it, taking the configured image
// provider into account.
func resolveDefaultImageModel(config AppConfig) string {
	if strings.TrimSpace(config.Models.ImageProvider) == "fal" {
		if model := strings.TrimSpace(config.Providers.Fal.Model); model != "" {
			return model
		}
		return defaultFalImageModel
	}
	return strings.TrimSpace(config.Providers.Ollama.Models.Image)
}

// resolveDefaultImageEditModel returns the image-to-image model the
// generate_image tool uses when the user attached a source image to transform.
// Mirrors resolveDefaultImageModel: fal exposes image-to-image as a dedicated
// endpoint, while Ollama reuses its single image model (it accepts source images
// inline via the request's images field).
func resolveDefaultImageEditModel(config AppConfig) string {
	if strings.TrimSpace(config.Models.ImageProvider) == "fal" {
		if model := strings.TrimSpace(config.Providers.Fal.ImageEditModel); model != "" {
			return model
		}
		return defaultFalImageEditModel
	}
	return strings.TrimSpace(config.Providers.Ollama.Models.Image)
}

// falKeyConfigured reports whether a fal.ai API key is present in the keychain.
// Used by the fal-only tool gates (upscale, video, audio) so a tool is offered
// only when it can actually run — without this, a keyless user sees the tool
// offered and it fails at call time with errFalKeyNotConfigured. The key is read
// from the keychain rather than the stale Fal.Enabled config flag (which is only
// populated when the frontend re-persists config), so the gate reflects the
// moment a key is saved. The registry is built once per stream, so this is one
// keychain read per turn per gate.
func falKeyConfigured() bool {
	key, err := loadFalAPIKey()
	return err == nil && strings.TrimSpace(key) != ""
}

// imageUpscaleConfigured reports whether the upscale_image tool should be
// offered: fal is the only upscale backend (Ollama has none). The tool is
// available whenever a fal.ai API key is configured, regardless of which
// provider is selected for image generation — upscaling is fal-only and
// independent of generate_image's backend, so an Ollama-configured conversation
// can still upscale via fal.
func imageUpscaleConfigured(config AppConfig) bool {
	return falKeyConfigured()
}

// resolveDefaultImageUpscaleModel returns the upscaler endpoint the upscale_image
// tool uses when the call doesn't override it. fal-only; falls back to the
// const default when the user hasn't picked one in Settings.
func resolveDefaultImageUpscaleModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.UpscaleModel); model != "" {
		return model
	}
	return defaultFalUpscaleModel
}

// videoUpscaleConfigured mirrors imageUpscaleConfigured for the upscale_video
// tool: fal is the only video-upscale backend and the default endpoint always
// applies, so the gate is purely the fal key — like transcribe_audio and
// lip_sync, no model needs to be configured first.
func videoUpscaleConfigured(config AppConfig) bool {
	return falKeyConfigured()
}

// resolveDefaultVideoUpscaleModel returns the video-upscaler endpoint the
// upscale_video tool uses when the call doesn't override it. fal-only; falls
// back to the const default when the user hasn't picked one in Settings.
func resolveDefaultVideoUpscaleModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.VideoUpscaleModel); model != "" {
		return model
	}
	return defaultFalVideoUpscaleModel
}

// videoGenerationConfigured reports whether the generate_video tool should be
// offered: a fal video model must be configured AND a fal.ai key must be
// present. fal is the only video backend (Ollama has no text-to-video models).
// The key check avoids offering a tool that is guaranteed to fail at call time
// with errFalKeyNotConfigured.
func videoGenerationConfigured(config AppConfig) bool {
	if strings.TrimSpace(config.Providers.Fal.VideoModel) == "" &&
		strings.TrimSpace(config.Providers.Fal.VideoImageModel) == "" {
		return false
	}
	return falKeyConfigured()
}

// resolveDefaultVideoModel returns the text-to-video model the generate_video
// tool uses when the call doesn't override it.
func resolveDefaultVideoModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.VideoModel); model != "" {
		return model
	}
	return defaultFalVideoModel
}

// resolveDefaultVideoImageModel returns the image-to-video model used to animate
// an attached image.
func resolveDefaultVideoImageModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.VideoImageModel); model != "" {
		return model
	}
	return defaultFalVideoImageModel
}

// resolveDefaultVideoExtendModel returns the video-extend model used to continue
// an attached video clip (Veo extend). Unlike the text/image video models there
// is no dedicated fal listing category for extend endpoints, so the default is a
// known-good endpoint and the user can override it in config.
func resolveDefaultVideoExtendModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.VideoExtendModel); model != "" {
		return model
	}
	return defaultFalVideoExtendModel
}

// resolveDefaultVideoMotionModel returns the motion-control model used to apply
// an attached video's motion to an attached image's subject, selected when the
// turn carries both an image and a video.
func resolveDefaultVideoMotionModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.VideoMotionModel); model != "" {
		return model
	}
	return defaultFalVideoMotionModel
}

// videoGenerationDescription assembles the generate_video tool description.
// audioCapable is the resolved capability of the configured video models
// (videoModelSupportsAudio); when true the description steers the planner toward
// a single generate_video call for narration rather than a generate_speech +
// lip_sync chain. The wording is generic — it never names the specific model —
// so it survives a model swap without rewording.
func videoGenerationDescription(audioCapable bool) string {
	base := "Use this when the user asks to create, animate, extend, or render a video or short clip. Works from a text description; when the user attached an image, animates that image (image-to-video); when the user attached a video, extends it into a longer clip (Veo extend); when the user attached both an image and a video, transfers the video's motion onto the image's subject (motion control) — describe the desired result in the prompt and do not ask the user which source to use. The clip is attached to the assistant reply. Generation runs for a minute or more. Pass negativePrompt to steer content away from unwanted elements, and generateAudio:false when the user wants a silent clip."
	if audioCapable {
		return base + " This video model can also generate synchronized audio (speech, music, ambient sound) from the prompt. For narration, a voice-over, or a speaking character, prefer a single generate_video call with the spoken text in the prompt over chaining generate_speech + lip_sync."
	}
	return base
}

func videoGenerationToolDefinition(audioCapable bool) HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "generate_video",
		Title:       "Generate video",
		Description: videoGenerationDescription(audioCapable),
		Example:     `{"name":"generate_video","content":"a drone shot flying over a misty pine forest at sunrise"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: generateVideoParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			if strings.TrimSpace(call.Content) == "" {
				return []string{prefix + ".content is required for generate_video (the video prompt)"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.GenerateVideo == nil {
				return nil, "video generation unavailable", errors.New("video generation is not available in this context")
			}
			// Attached media picks the generation mode and model, in priority order:
			// an attached video switches to a Veo extend endpoint (continues the
			// clip), an attached image switches to image-to-video (animates the
			// frame; multiple images switch to reference-to-video when the model
			// supports it), otherwise text-to-video. An image AND a video together
			// switch to motion control (the video's motion applied to the image's
			// subject). The planner may still override the model per call.
			attachedImages := tools.AttachedImages
			attachedVideo := strings.TrimSpace(tools.AttachedVideo)
			model := strings.TrimSpace(call.Model)
			if model == "" {
				switch {
				case attachedVideo != "" && len(attachedImages) > 0:
					model = resolveDefaultVideoMotionModel(tools.Config)
				case attachedVideo != "":
					model = resolveDefaultVideoExtendModel(tools.Config)
				case len(attachedImages) > 0:
					model = resolveDefaultVideoImageModel(tools.Config)
				default:
					model = resolveDefaultVideoModel(tools.Config)
				}
			}
			if model == "" {
				return nil, "video generation unavailable", errors.New("no video model is configured")
			}
			// Aspect-ratio precedence for video mirrors the image tool's rule
			// (tools_registry.go generate_image): an explicit aspectRatio on the
			// tool call wins over everything. With no explicit ratio, image-to-
			// video inherits the source frame's orientation rather than getting
			// the configured default stamped on it — a 9:16 portrait image used
			// to come back 16:9 because the config default ("16:9") was sent
			// unconditionally (see conv_26cc3f515d6d645b316763cb). Video-extend
			// (attachedVideo) and text-to-video fall through to the configured
			// default, as detection only applies to a source image.
			ratio := strings.TrimSpace(call.AspectRatio)
			explicit := ratio != ""
			if ratio == "" && len(attachedImages) > 0 && attachedVideo == "" {
				ratio = aspectRatioFromImage(attachedImages[0])
			}
			if ratio == "" {
				ratio = tools.Config.Generation.Video.AspectRatio
			}
			// Duration precedence mirrors aspectRatio: an explicit duration on the
			// call wins; otherwise the configured default applies. For an extend
			// (attachedVideo), the value is the length of the extension, not the
			// total clip — resolveVideoBody surfaces that distinction to the user
			// via a notice so the planner's intent isn't misread as total length.
			duration := strings.TrimSpace(call.Duration)
			if duration == "" {
				duration = tools.Config.Generation.Video.Duration
			}
			videoReq := VideoGenerateRequest{
				Model:               model,
				Prompt:              strings.TrimSpace(call.Content),
				Duration:            duration,
				AspectRatio:         ratio,
				AspectRatioExplicit: explicit,
				NegativePrompt:      strings.TrimSpace(call.NegativePrompt),
				Resolution:          strings.TrimSpace(call.Resolution),
				FPS:                 strings.TrimSpace(call.FPS),
				Images:              attachedImages,
				Video:               attachedVideo,
				GenerateAudio:       call.GenerateAudio,
			}
			generated, err := tools.GenerateVideo(ctx, videoReq)
			if err != nil {
				return nil, "video generation failed", err
			}
			if len(generated.Data) == 0 {
				return nil, "video generation returned no video", errors.New("video model returned no video data")
			}
			tempPath, err := writeTempVideo(generated)
			if err != nil {
				return nil, "video generation failed", err
			}
			output := ToolVideoResult{
				Model:   model,
				Prompt:  videoReq.Prompt,
				Count:   1,
				Videos:  []ToolVideoFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
				Notices: generated.Notices,
			}
			summary := fmt.Sprintf("generated a video with %s", model)
			if attachedVideo != "" && len(attachedImages) > 0 {
				summary = fmt.Sprintf("transferred the attached video's motion onto the attached image with %s", model)
			} else if attachedVideo != "" {
				summary = fmt.Sprintf("extended the attached video into a longer clip with %s", model)
			} else if imageCount := len(attachedImages); imageCount > 1 {
				summary = fmt.Sprintf("combined %d attached images into a video with %s", imageCount, model)
			} else if imageCount == 1 {
				summary = fmt.Sprintf("animated the attached image into a video with %s", model)
			}
			return output, summary, nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolVideoResult); ok {
				activity.Command = []string{"fal", "generate", typed.Model}
			}
			return activity
		},
	}
}

// writeTempMediaBytes writes downloaded media bytes to a temp file named
// prefix+ext and returns its path. The harness moves this file into the
// conversation's artifacts directory when it persists the turn; carrying a path
// (not bytes) keeps multi-MB media out of tool-result telemetry and the JSON
// IPC boundary. Shared by the video and audio temp writers.
func writeTempMediaBytes(data []byte, prefix, ext string) (string, error) {
	file, err := os.CreateTemp("", prefix+ext)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

// writeTempVideo / writeTempAudio keep each media kind's ext derivation next to
// its caller while sharing one writer body.
func writeTempVideo(video GeneratedVideo) (string, error) {
	return writeTempMediaBytes(video.Data, "atelier-video-*", videoExtensionForMediaType(video.MimeType))
}

// speechGenerationConfigured reports whether the generate_speech tool should be
// offered: a fal speech model must be configured AND a fal.ai key must be
// present. fal is the only audio backend. The key check avoids offering a tool
// that is guaranteed to fail at call time with errFalKeyNotConfigured.
func speechGenerationConfigured(config AppConfig) bool {
	if strings.TrimSpace(config.Providers.Fal.AudioModel) == "" {
		return false
	}
	return falKeyConfigured()
}

// soundEffectsGenerationConfigured mirrors speechGenerationConfigured for the
// generate_sound tool (music and sound effects). mergeAppConfig seeds the
// default once AudioModel is set, so anyone with speech configured gets both.
func soundEffectsGenerationConfigured(config AppConfig) bool {
	if strings.TrimSpace(config.Providers.Fal.SoundEffectsModel) == "" {
		return false
	}
	return falKeyConfigured()
}

// resolveDefaultAudioModel returns the model the generate_speech tool uses when
// the call doesn't override it.
func resolveDefaultAudioModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.AudioModel); model != "" {
		return model
	}
	return defaultFalAudioModel
}

// resolveDefaultSoundEffectsModel returns the model the generate_sound tool
// uses when the call doesn't override it.
func resolveDefaultSoundEffectsModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.SoundEffectsModel); model != "" {
		return model
	}
	return defaultFalSoundEffectsModel
}

// transcribeAudioConfigured reports whether the transcribe_audio tool should be
// offered: fal is the only transcription backend, and the default model
// (fal-ai/wizper) always applies, so the gate is purely the fal key — unlike
// generate_speech/generate_sound/generate_video, no model needs to be
// configured first.
func transcribeAudioConfigured(config AppConfig) bool {
	return falKeyConfigured()
}

// resolveDefaultTranscribeModel returns the speech-to-text model the
// transcribe_audio tool uses when the call doesn't override it.
func resolveDefaultTranscribeModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.TranscribeModel); model != "" {
		return model
	}
	return defaultFalTranscribeModel
}

// lipsyncConfigured reports whether the lip_sync tool should be offered: fal is
// the only lip sync backend, and the default models always apply (the audio-to-
// video and video-to-video endpoints are known-good), so the gate is purely the
// fal key — like transcribe_audio, no model needs to be configured first.
func lipsyncConfigured(config AppConfig) bool {
	return falKeyConfigured()
}

// resolveDefaultLipsyncImageModel returns the audio-to-video lip sync model
// used when the user attaches an audio clip plus an image (a talking head).
func resolveDefaultLipsyncImageModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.LipsyncImageModel); model != "" {
		return model
	}
	return defaultFalLipsyncImageModel
}

// resolveDefaultLipsyncVideoModel returns the video-to-video lip sync model
// used when the user attaches an audio clip plus a video (re-lip-sync a clip).
func resolveDefaultLipsyncVideoModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.LipsyncVideoModel); model != "" {
		return model
	}
	return defaultFalLipsyncVideoModel
}

// speechGenerationDescription assembles the generate_speech tool description.
// videoAudioCapable reflects whether the configured video model can produce
// audio on its own; when it cannot, the description tells the planner that
// generated speech carries forward to a later lip_sync in the same turn — the
// correct path for putting narration behind a video on a non-audio video model.
func speechGenerationDescription(videoAudioCapable bool) string {
	base := "Use this when the user asks to speak, narrate, or read text aloud (text-to-speech). The configured fal.ai speech model generates the clip and it is attached to the assistant reply."
	if !videoAudioCapable {
		return base + " To put generated speech behind a video, call generate_speech first then lip_sync in the same turn — the generated speech carries forward to lip_sync automatically."
	}
	return base
}

// soundEffectsGenerationDescription assembles the generate_sound tool
// description. It takes no videoAudioCapable hint: lip_sync is a face tool, so
// the generate_speech + lip_sync chain never applies to music or sound effects.
func soundEffectsGenerationDescription() string {
	return "Use this when the user asks to create music, ambience, or a sound effect from a description. The configured fal.ai sound-effects model generates the clip and it is attached to the assistant reply. When the request names a genre or mood, pass it as style and keep content as the pure lyrics or sound description."
}

// audioGenerationExecute is the shared Execute body for generate_speech and
// generate_sound: both resolve the model (call override, then the tool's
// configured default), run the same fal audio backend, and stage the clip as a
// ToolAudioResult — carry-forward, artifacts, and notice surfacing are keyed on
// that result type, so both tools share the machinery. The caller supplies the
// canonical request with its tool-specific params already set.
func audioGenerationExecute(ctx context.Context, tools HarnessToolExecutionContext, audioReq AudioGenerateRequest, defaultModel func(AppConfig) string) (any, string, error) {
	if tools.GenerateAudio == nil {
		return nil, "audio generation unavailable", errors.New("audio generation is not available in this context")
	}
	if strings.TrimSpace(audioReq.Model) == "" {
		audioReq.Model = defaultModel(tools.Config)
	}
	if strings.TrimSpace(audioReq.Model) == "" {
		return nil, "audio generation unavailable", errors.New("no audio model is configured")
	}
	audioReq.Prompt = strings.TrimSpace(audioReq.Prompt)
	generated, err := tools.GenerateAudio(ctx, audioReq)
	if err != nil {
		return nil, "audio generation failed", err
	}
	if len(generated.Data) == 0 {
		return nil, "audio generation returned no audio", errors.New("audio model returned no audio data")
	}
	tempPath, err := writeTempAudio(generated)
	if err != nil {
		return nil, "audio generation failed", err
	}
	output := ToolAudioResult{
		Model:   audioReq.Model,
		Prompt:  audioReq.Prompt,
		Count:   1,
		Audios:  []ToolAudioFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
		Notices: generated.Notices,
	}
	return output, fmt.Sprintf("generated audio with %s", audioReq.Model), nil
}

// audioGenerationActivity renders the shared fal generate command for any tool
// that returns a ToolAudioResult.
func audioGenerationActivity(result HarnessToolResult) HarnessToolActivity {
	activity := defaultHarnessToolActivity(result)
	if typed, ok := result.Result.(ToolAudioResult); ok {
		activity.Command = []string{"fal", "generate", typed.Model}
	}
	return activity
}

func speechGenerationToolDefinition(videoAudioCapable bool) HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "generate_speech",
		Title:       "Generate speech",
		Description: speechGenerationDescription(videoAudioCapable),
		Example:     `{"name":"generate_speech","content":"The quick brown fox jumps over the lazy dog."}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: generateSpeechParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			if strings.TrimSpace(call.Content) == "" {
				return []string{prefix + ".content is required for generate_speech (the text to speak)"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			return audioGenerationExecute(ctx, tools, AudioGenerateRequest{
				Model:  strings.TrimSpace(call.Model),
				Prompt: call.Content,
				Voice:  strings.TrimSpace(call.Voice),
			}, resolveDefaultAudioModel)
		},
		Activity: audioGenerationActivity,
	}
}

func soundEffectsGenerationToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "generate_sound",
		Title:       "Generate sound",
		Description: soundEffectsGenerationDescription(),
		Example:     `{"name":"generate_sound","content":"a calm lo-fi piano loop with soft rain in the background"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: generateSoundParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			if strings.TrimSpace(call.Content) == "" {
				return []string{prefix + ".content is required for generate_sound (the description of the music or sound)"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			return audioGenerationExecute(ctx, tools, AudioGenerateRequest{
				Model:          strings.TrimSpace(call.Model),
				Prompt:         call.Content,
				Duration:       strings.TrimSpace(call.Duration),
				NegativePrompt: strings.TrimSpace(call.NegativePrompt),
				Loop:           call.Loop,
				Style:          strings.TrimSpace(call.Style),
			}, resolveDefaultSoundEffectsModel)
		},
		Activity: audioGenerationActivity,
	}
}

// writeTempAudio writes downloaded audio bytes to a temp file, mirroring the
// video path. Thin wrapper over the shared writer.
func writeTempAudio(audio GeneratedAudio) (string, error) {
	return writeTempMediaBytes(audio.Data, "atelier-audio-*", audioExtensionForMediaType(audio.MimeType))
}

// transcribeAudioToolDefinition exposes the transcribe_audio tool. It consumes
// the user's attached audio clip (AttachedAudio) and returns the transcript via
// fal's speech-to-text endpoint (fal-ai/wizper by default). The transcript flows
// as normal tool evidence — the primary model weaves it into its reply. Requires
// an attached audio clip, mirroring how upscale_image requires an attached image.
func transcribeAudioToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "transcribe_audio",
		Title:       "Transcribe audio",
		Description: "Use this when the user asks to transcribe, caption, or get a text version of an attached audio clip (a voice memo, recording, interview, etc.). Requires an attached audio clip. Runs the configured fal.ai speech-to-text model and returns the transcript as evidence. Set task to \"translate\" to translate the audio's speech to English text instead of transcribing it.",
		Example:     `{"name":"transcribe_audio"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: transcribeAudioParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.TranscribeAudio == nil {
				return nil, "audio transcription unavailable", errors.New("audio transcription is not available in this context")
			}
			attachedAudio := strings.TrimSpace(tools.AttachedAudio)
			if attachedAudio == "" {
				return nil, "audio transcription requires an attached audio clip", errors.New("transcribe_audio requires an attached audio clip — ask the user to attach one first")
			}
			model := strings.TrimSpace(call.Model)
			if model == "" {
				model = resolveDefaultTranscribeModel(tools.Config)
			}
			transcript, err := tools.TranscribeAudio(ctx, model, attachedAudio, strings.TrimSpace(call.Task), strings.TrimSpace(call.Language))
			if err != nil {
				return nil, "audio transcription failed", err
			}
			output := ToolTranscribeResult{
				Model:      model,
				Transcript: transcript.Text,
				Notices:    transcript.Notices,
			}
			return output, fmt.Sprintf("transcribed audio with %s", model), nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolTranscribeResult); ok {
				activity.Command = []string{"fal", "transcribe", typed.Model}
			}
			return activity
		},
	}
}

// transcribeAudioParamSchema describes transcribe_audio's optional inputs. There
// is no "content" param — the audio comes from the user's attachment, not a
// prompt. task and language are the only fal-ai/wizper inputs the planner can
// steer; both are optional with sensible defaults.
func transcribeAudioParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"model": stringParam("Optional fal.ai speech-to-text model override."),
			"task": stringParam("Optional — \"transcribe\" (default) to transcribe the audio in its " +
				"original language, or \"translate\" to translate the speech to English text."),
			"language": stringParam("Optional — the spoken language as a two-letter code (e.g. \"fr\") " +
				"to guide transcription. Omit to let the model auto-detect."),
		},
		"required": []string{},
	}
}

// lipsyncDescription assembles the lip_sync tool description. When the
// configured video model cannot produce audio itself, the description notes
// that lip_sync can consume audio generated earlier in the same turn — the
// correct path for a non-audio video model. The attachment requirement stands
// either way: lip_sync always needs an audio clip plus a face.
func lipsyncDescription(videoAudioCapable bool) string {
	base := "Use this when the user asks to lip sync, dub, or sync audio to a face. Requires an attached audio clip AND an attached face: an image produces a talking-head video (audio-to-video), a video re-lip-syncs the existing clip (video-to-video). The synced video is attached to the assistant reply. Generation runs for a minute or more."
	if !videoAudioCapable {
		return base + " The audio clip may be one the user attached or one generate_speech produced earlier in this turn — generated speech carries forward automatically."
	}
	return base
}

// lipsyncToolDefinition exposes the lip_sync tool. It consumes an attached audio
// clip plus an attached face (image or video) and produces a lip-synced video.
// The mode is determined by which face attachment is present: image → audio-to-
// video (a talking head), video → video-to-video (re-lip-sync an existing clip).
// The result is a video, so it rides the existing ToolVideoResult → artifact
// pipeline (videosFromToolResults / writeChatVideoArtifacts) — no new
// persistence code. Mirrors transcribe_audio's attachment-guard pattern.
func lipsyncToolDefinition(videoAudioCapable bool) HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "lip_sync",
		Title:       "Lip sync video",
		Description: lipsyncDescription(videoAudioCapable),
		Example:     `{"name":"lip_sync"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: lipsyncParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.GenerateLipsync == nil {
				return nil, "lip sync unavailable", errors.New("lip sync is not available in this context")
			}
			attachedAudio := strings.TrimSpace(tools.AttachedAudio)
			if attachedAudio == "" {
				return nil, "lip sync requires an attached audio clip", errors.New("lip_sync requires an attached audio clip — ask the user to attach one first")
			}
			// sync-lipsync v3 takes a single face source (image or video), so
			// only the first attached image is used even when several are
			// attached; multi-image faces aren't part of this tool.
			attachedImage := ""
			if len(tools.AttachedImages) > 0 {
				attachedImage = strings.TrimSpace(tools.AttachedImages[0])
			}
			attachedVideo := strings.TrimSpace(tools.AttachedVideo)
			if attachedImage == "" && attachedVideo == "" {
				return nil, "lip sync requires an attached face", errors.New("lip_sync requires an attached image or video to lip sync — ask the user to attach a face alongside the audio")
			}
			model := strings.TrimSpace(call.Model)
			if model == "" {
				// Pick the mode by which face is attached: video → video-to-video,
				// image → audio-to-video. A video takes precedence if both are set.
				if attachedVideo != "" {
					model = resolveDefaultLipsyncVideoModel(tools.Config)
				} else {
					model = resolveDefaultLipsyncImageModel(tools.Config)
				}
			}
			lipsyncReq := LipsyncGenerateRequest{
				Model: model,
				Audio: attachedAudio,
				Image: attachedImage,
				Video: attachedVideo,
			}
			generated, err := tools.GenerateLipsync(ctx, lipsyncReq)
			if err != nil {
				return nil, "lip sync failed", err
			}
			if len(generated.Data) == 0 {
				return nil, "lip sync returned no video", errors.New("lip sync model returned no video data")
			}
			tempPath, err := writeTempVideo(generated)
			if err != nil {
				return nil, "lip sync failed", err
			}
			output := ToolVideoResult{
				Model:   model,
				Count:   1,
				Videos:  []ToolVideoFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
				Notices: generated.Notices,
			}
			summary := fmt.Sprintf("lip-synced the attached audio to a %s with %s", map[bool]string{true: "video", false: "image"}[attachedVideo != ""], model)
			return output, summary, nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolVideoResult); ok {
				activity.Command = []string{"fal", "lipsync", typed.Model}
			}
			return activity
		},
	}
}

// lipsyncParamSchema describes lip_sync's inputs. There is no "content" param —
// the audio and face come from the user's attachments, not a prompt. model is
// the only knob the planner can steer; everything else is attachment-driven.
func lipsyncParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"model": stringParam("Optional fal.ai lip sync model override."),
		},
		"required": []string{},
	}
}

func imageGenerationToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "generate_image",
		Title:       "Generate image",
		Description: "Use this when the user asks to create, draw, paint, or render an image. Works from a text description, and when the user attached an image, transforms that image in the requested style (image-to-image). The configured image model generates it and the image is attached to the assistant reply.",
		Example:     `{"name":"generate_image","content":"a watercolor of a lighthouse at dusk"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: generateImageParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			if strings.TrimSpace(call.Content) == "" {
				return []string{prefix + ".content is required for generate_image (the image prompt)"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.GenerateImage == nil {
				return nil, "image generation unavailable", errors.New("image generation is not available in this context")
			}
			// Attached images switch to image-to-image (or multi-reference edit
			// for models that declare an array source field): use the image-to-image
			// model and pass the source frames to the generator to transform.
			attachedImages := tools.AttachedImages
			model := strings.TrimSpace(call.Model)
			if model == "" {
				if len(attachedImages) > 0 {
					model = resolveDefaultImageEditModel(tools.Config)
				} else {
					model = resolveDefaultImageModel(tools.Config)
				}
			}
			if model == "" {
				return nil, "image generation unavailable", errors.New("no image model is configured")
			}
			// Aspect-ratio precedence: an explicit aspectRatio on the tool call
			// wins over everything. With no explicit ratio and exactly one source
			// image, the output inherits that frame's orientation rather than
			// getting the configured default stamped on — a portrait source used
			// to come back landscape because the config default ("16:9") was sent
			// to edit models that honor image_size (see
			// conv_65bc186e6035c885660b369c). With zero or multiple attached
			// images there is no authoritative frame (text-to-image has none;
			// multi-image edit is a composite of references), so the configured
			// default applies. This mirrors generate_video's rule below.
			ratio := strings.TrimSpace(call.AspectRatio)
			if ratio == "" && len(attachedImages) == 1 {
				ratio = aspectRatioFromImage(attachedImages[0])
			}
			if ratio == "" {
				ratio = strings.TrimSpace(tools.Config.Generation.Image.AspectRatio)
			}
			width, height := imageSizeForPresetAndRatio(tools.Config.Generation.Image.SizePreset, ratio)
			imageReq := ImageGenerateRequest{
				Model:       model,
				Prompt:      strings.TrimSpace(call.Content),
				Width:       width,
				Height:      height,
				AspectRatio: ratio,
				Steps:       tools.Config.Generation.Image.Steps,
			}
			if len(attachedImages) > 0 {
				imageReq.Images = attachedImages
			}
			payload, raw, notices, err := tools.GenerateImage(ctx, imageReq)
			if err != nil {
				return nil, "image generation failed", err
			}
			images := normalizeImagePayloads(payload.Images)
			if maybeImage := normalizeImagePayload(payload.Image); maybeImage != "" {
				images = append(images, maybeImage)
			}
			if maybeImage := normalizeImagePayload(payload.Response); maybeImage != "" {
				images = append(images, maybeImage)
			}
			images = append(images, collectImagesFromJSON(raw)...)
			images = dedupeStrings(images)
			if len(images) == 0 {
				return nil, "image generation returned no image", errors.New("image model returned no image data")
			}
			output := ToolImageResult{Model: model, Prompt: imageReq.Prompt, Count: len(images), Images: images, Notices: notices}
			summary := fmt.Sprintf("generated %d image%s with %s", len(images), pluralSuffix(len(images)), model)
			if imageCount := len(attachedImages); imageCount > 1 {
				summary = fmt.Sprintf("combined %d attached images into %d image%s with %s", imageCount, len(images), pluralSuffix(len(images)), model)
			} else if imageCount == 1 {
				summary = fmt.Sprintf("transformed the attached image into %d image%s with %s", len(images), pluralSuffix(len(images)), model)
			}
			return output, summary, nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolImageResult); ok {
				// fal model ids are namespaced under "fal-ai/..."; Ollama tags
				// never use that prefix (they look like "x/z-image-turbo:latest").
				provider := "ollama"
				if strings.HasPrefix(typed.Model, "fal-ai/") {
					provider = "fal"
				}
				activity.Command = []string{provider, "generate", typed.Model}
			}
			return activity
		},
	}
}

func filesystemToolRegistry() HarnessToolRegistry {
	return defaultHarnessToolRegistry(context.Background(), defaultAppConfig(), nil)
}

// jsonSchema helpers describe tool parameters to Ollama's native tool-calling
// API. They mirror the rules enforced by each tool's Validate func, which stays
// as a runtime backstop for the format-schema planner path.

func stringParam(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func intParam(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func boolParam(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func enumParam(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func listFilesParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": stringParam("Optional relative directory under the workspace root to list."),
		},
	}
}

func readFileParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":        stringParam("Relative path of a text file under the workspace root."),
			"maxBytes":    intParam("Optional cap on bytes read."),
			"allowBinary": boolParam("When true, do not reject binary file content."),
		},
		"required": []string{"path"},
	}
}

func runCommandParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"command":   stringParam("The allowlisted command to run."),
			"args":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Arguments to pass to the command."},
			"cwd":       stringParam("Optional relative working directory under the workspace root."),
			"timeoutMs": intParam("Optional timeout in milliseconds."),
			"env":       map[string]any{"type": "object", "description": "Optional environment variables."},
		},
		"required": []string{"command"},
	}
}

func writeFileParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path":      stringParam("Relative path of the file to create or modify, under the workspace root."),
			"content":   stringParam("The text content to write."),
			"append":    boolParam("When true, append to the file instead of replacing it."),
			"overwrite": boolParam("When true, overwrite an existing file."),
		},
		"required": []string{"path", "content"},
	}
}

func generateImageParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content":     stringParam("The image prompt — describe the image to create."),
			"model":       stringParam("Optional image generation model override."),
			"aspectRatio": enumParam("Optional — the output image shape. Omit to inherit a single attached image's orientation, else use the configured default.", "1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "21:9"),
		},
		"required": []string{"content"},
	}
}

// imageUpscaleToolDefinition exposes the upscale_image tool. It takes an
// attached image and returns a higher-resolution version via the configured
// fal upscaler. fal-only (no Ollama path); the attached-image requirement is
// enforced in Execute because Validate only sees the call, not tools.
func imageUpscaleToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "upscale_image",
		Title:       "Upscale image",
		Description: "Use this when the user asks to upscale, increase the resolution of, or make a higher-resolution version of an attached image. Requires an attached image. fal.ai only — runs unattended like image generation.",
		Example:     `{"name":"upscale_image","scale":"2x"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: imageUpscaleParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.UpscaleImage == nil {
				return nil, "image upscaling unavailable", errors.New("image upscaling is not available in this context")
			}
			// Upscaling is single-image: take the first attached image. The rest,
			// if any, are ignored — upscaling a montage isn't meaningful.
			attachedImage := ""
			if len(tools.AttachedImages) > 0 {
				attachedImage = strings.TrimSpace(tools.AttachedImages[0])
			}
			if attachedImage == "" {
				return nil, "image upscaling requires an attached image", errors.New("upscale_image requires an attached image — ask the user to attach one first")
			}
			model := strings.TrimSpace(call.Model)
			if model == "" {
				model = resolveDefaultImageUpscaleModel(tools.Config)
			}
			if model == "" {
				return nil, "image upscaling unavailable", errors.New("no upscale model is configured")
			}
			scale := 2.0
			if strings.TrimSpace(call.Scale) == "4x" {
				scale = 4.0
			}
			payload, err := tools.UpscaleImage(ctx, ImageUpscaleRequest{
				Model: model,
				Image: attachedImage,
				Scale: scale,
			})
			if err != nil {
				return nil, "image upscaling failed", err
			}
			images := normalizeImagePayloads(payload.Images)
			if maybeImage := normalizeImagePayload(payload.Image); maybeImage != "" {
				images = append(images, maybeImage)
			}
			if maybeImage := normalizeImagePayload(payload.Response); maybeImage != "" {
				images = append(images, maybeImage)
			}
			images = dedupeStrings(images)
			if len(images) == 0 {
				return nil, "image upscaling returned no image", errors.New("upscale model returned no image data")
			}
			output := ToolImageResult{Model: model, Count: len(images), Images: images}
			summary := fmt.Sprintf("upscaled the attached image to %dx with %s", int(scale), model)
			return output, summary, nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolImageResult); ok {
				activity.Command = []string{"fal", "upscale", typed.Model}
			}
			return activity
		},
	}
}

func imageUpscaleParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"scale": enumParam("Optional — the upscale factor. Omit for 2x.", "2x", "4x"),
			"model": stringParam("Optional upscale model override."),
		},
		"required": []string{},
	}
}

// videoUpscaleToolDefinition exposes the upscale_video tool — the video sibling
// of upscale_image. It takes an attached clip and returns a
// higher-resolution version via the configured fal video upscaler (a
// video-to-video transform). fal-only; the attached-video requirement is
// enforced in Execute because Validate only sees the call, not tools. The result
// rides the same ToolVideoResult pipeline as generate_video, so artifacts,
// history, and the chat reply's video card all work unchanged.
func videoUpscaleToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "upscale_video",
		Title:       "Upscale video",
		Description: "Use this when the user asks to upscale, increase the resolution of, or make a higher-resolution or 4K version of an attached video clip. Requires an attached video. fal.ai only — runs unattended like video generation. Upscaling runs for a minute or more on longer clips.",
		Example:     `{"name":"upscale_video","scale":"2x"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: videoUpscaleParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.UpscaleVideo == nil {
				return nil, "video upscaling unavailable", errors.New("video upscaling is not available in this context")
			}
			attachedVideo := strings.TrimSpace(tools.AttachedVideo)
			if attachedVideo == "" {
				return nil, "video upscaling requires an attached video", errors.New("upscale_video requires an attached video — ask the user to attach one first")
			}
			model := strings.TrimSpace(call.Model)
			if model == "" {
				model = resolveDefaultVideoUpscaleModel(tools.Config)
			}
			if model == "" {
				return nil, "video upscaling unavailable", errors.New("no video upscale model is configured")
			}
			scale := 2.0
			if strings.TrimSpace(call.Scale) == "4x" {
				scale = 4.0
			}
			generated, err := tools.UpscaleVideo(ctx, VideoUpscaleRequest{
				Model: model,
				Video: attachedVideo,
				Scale: scale,
			})
			if err != nil {
				return nil, "video upscaling failed", err
			}
			if len(generated.Data) == 0 {
				return nil, "video upscaling returned no video", errors.New("upscale model returned no video data")
			}
			tempPath, err := writeTempVideo(generated)
			if err != nil {
				return nil, "video upscaling failed", err
			}
			output := ToolVideoResult{
				Model:   model,
				Count:   1,
				Videos:  []ToolVideoFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
				Notices: generated.Notices,
			}
			return output, fmt.Sprintf("upscaled the attached video to %dx with %s", int(scale), model), nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolVideoResult); ok {
				activity.Command = []string{"fal", "upscale", typed.Model}
			}
			return activity
		},
	}
}

func videoUpscaleParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"scale": enumParam("Optional — the upscale factor. Omit for 2x.", "2x", "4x"),
			"model": stringParam("Optional video upscale model override."),
		},
		"required": []string{},
	}
}

// imageSizeForAspectRatio derives concrete width/height from a named aspect
// ratio, using baseLong as the long-edge budget. Both edges are rounded to a
// multiple of 16 (a common constraint for diffusion image models) and floored
// at 256. An unrecognized ratio returns (0, 0) so the caller keeps the
// configured default dimensions.
func imageSizeForAspectRatio(baseLong int, ratio string) (int, int) {
	if baseLong <= 0 {
		baseLong = 1024
	}
	var wr, hr int
	switch strings.TrimSpace(ratio) {
	case "1:1":
		wr, hr = 1, 1
	case "16:9":
		wr, hr = 16, 9
	case "9:16":
		wr, hr = 9, 16
	case "4:3":
		wr, hr = 4, 3
	case "3:4":
		wr, hr = 3, 4
	case "3:2":
		wr, hr = 3, 2
	case "2:3":
		wr, hr = 2, 3
	case "21:9":
		wr, hr = 21, 9
	default:
		return 0, 0
	}
	longEdge := roundToMultipleOf16(baseLong)
	shortRatio, longRatio := wr, hr
	if shortRatio > longRatio {
		shortRatio, longRatio = longRatio, shortRatio
	}
	shortEdge := roundToMultipleOf16(baseLong * shortRatio / longRatio)
	if wr >= hr {
		return longEdge, shortEdge
	}
	return shortEdge, longEdge
}

func roundToMultipleOf16(n int) int {
	rounded := (n + 8) / 16 * 16
	if rounded < 256 {
		return 256
	}
	return rounded
}

// imageSizePresetLongEdge returns the long-edge pixel budget for a named size
// preset. These are vetted values (the Settings Size dropdown lists them) so
// neither the model nor the user can request an out-of-budget generation by
// accident. An unknown preset falls back to the standard long edge, matching
// defaultImageSizePreset.
func imageSizePresetLongEdge(preset string) int {
	switch strings.TrimSpace(preset) {
	case "draft":
		return 1024
	case "standard":
		return 1536
	case "high":
		return 2048
	case "high+":
		return 2560
	default:
		return 1536
	}
}

// imageSizeForPresetAndRatio derives concrete width/height from a size preset
// (long-edge budget) and an aspect ratio, composing imageSizeForAspectRatio.
// An unknown ratio falls back to a square at the preset's long edge so a bad
// value never produces a zero-dimension request.
func imageSizeForPresetAndRatio(preset, ratio string) (int, int) {
	baseLong := imageSizePresetLongEdge(preset)
	ratio = strings.TrimSpace(ratio)
	if ratio == "" {
		ratio = defaultImageAspectRatio
	}
	width, height := imageSizeForAspectRatio(baseLong, ratio)
	if width == 0 || height == 0 {
		return baseLong, baseLong
	}
	return width, height
}

func generateVideoParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content":        stringParam("The video prompt — describe the clip to create."),
			"model":          stringParam("Optional fal.ai video model override."),
			"negativePrompt": stringParam("Optional — describe what to keep out of the clip (e.g. \"blurry, text, watermark\")."),
			"aspectRatio":    enumParam("Optional — the output video shape. When the model exposes an aspect_ratio input this is sent directly; otherwise image-to-video models inherit the ratio from the source image, so the explicit ratio is only honored if that image already matches (the image is not reshaped). Omit to inherit the attached image's orientation (image-to-video) or the configured default (text-to-video).", "1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "21:9"),
			"duration":       stringParam("Optional — the target clip length in seconds (e.g. \"5\", \"8\"). Forwarded only when the model exposes a duration input; an unsupported value is dropped with a notice and the model's default is used. For an extend (an attached video) the value is the length of the *extension*, not the total output length — so \"5\" adds 5s to the source clip. Omit to use the configured default length (or the model's default when extending)."),
			"resolution":     stringParam("Optional — the output video resolution tier (e.g. \"480p\", \"720p\", \"1080p\", \"4k\"). Tiers vary by model, so an unsupported value is ignored with a notice and the model's default is used. Omit to let the model choose."),
			"fps":            stringParam("Optional — the output frame rate in frames per second (e.g. \"24\", \"30\", \"60\"). Only some video models expose an fps/frame_rate input; an unsupported value (or a model with no such input) is ignored with a notice and the model's default is used. Omit to let the model choose."),
			"generateAudio":  boolParam("Optional — set false to render a silent clip on models that would otherwise add audio. Some models generate audio by default yet expose no way to disable it; on those, a false value cannot be honored and the user is notified."),
		},
		"required": []string{"content"},
	}
}

func generateSpeechParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content": stringParam("The text to speak."),
			"model":   stringParam("Optional fal.ai speech model override."),
			"voice": stringParam("Optional — the voice for the speech (e.g. \"Rachel\"). " +
				"Only some speech models support it; ignored otherwise with a note to the user."),
		},
		"required": []string{"content"},
	}
}

func generateSoundParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content": stringParam("A description of the music or sound effect to create."),
			"model":   stringParam("Optional fal.ai sound model override."),
			"duration": stringParam("Optional — target clip length in seconds (e.g. \"10\"). " +
				"Ignored by models that don't expose a duration control."),
			"negativePrompt": stringParam("Optional — describe what to keep out of the audio (e.g. \"vocals, percussion\"). " +
				"Ignored by models without a negative-prompt control."),
			"loop": boolParam("Optional — set true for a seamless, gapless loop (ambient beds, backgrounds). " +
				"Only some models support it; ignored otherwise with a note to the user."),
			"style": stringParam("Optional — the genre or mood of the music (e.g. \"jazz\", \"lo-fi\", \"orchestral\"). " +
				"Keep content as the pure lyrics or sound description and put the style here; some music models require a style."),
		},
		"required": []string{"content"},
	}
}

// ollamaToolSpecs maps the registry to Ollama's native tools array shape:
// [{ "type": "function", "function": { "name", "description", "parameters" } }].
func ollamaToolSpecs(registry HarnessToolRegistry) []map[string]any {
	specs := make([]map[string]any, 0, len(registry.definitions))
	for _, definition := range registry.definitions {
		parameters := definition.ParamSchema
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		specs = append(specs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        definition.Name,
				"description": definition.Description,
				"parameters":  parameters,
			},
		})
	}
	return specs
}

// workspaceRootPhrase describes the filesystem boundary in concrete terms.
// The tools operate on real files on the host machine, confined to a real
// directory — not an abstract or simulated "workspace". Naming the actual
// root keeps a planning model from concluding it cannot observe the machine.
func workspaceRootPhrase(fsConfig ConfigFilesystemTool) string {
	if root := strings.TrimSpace(fsConfig.Root); root != "" {
		return "the Atelier filesystem root (" + root + ")"
	}
	return "the Atelier filesystem root"
}

// runCommandDescription builds the run_command tool description from the live
// filesystem config so the model is told exactly which commands it may run.
// The command list is read from the same ConfigFilesystemTool.AllowedCommands
// that fs_tools.go enforces, so the prompt and the allowlist cannot drift.
func runCommandDescription(fsConfig ConfigFilesystemTool) string {
	base := "Use this to run an allowlisted command on this machine. Commands run for real; the working directory is confined to " + workspaceRootPhrase(fsConfig) + " and its subdirectories. Use it when the user or a skill provides a command, or when a command is the direct way to gather evidence such as searching text, listing with filters, counting, or checking status."
	allowed := make([]string, 0, len(fsConfig.AllowedCommands))
	for _, cmd := range fsConfig.AllowedCommands {
		if trimmed := strings.TrimSpace(cmd); trimmed != "" {
			allowed = append(allowed, trimmed)
		}
	}
	if len(allowed) == 0 {
		return base + " No commands are currently permitted by the allowlist."
	}
	return base + " Allowed commands (nothing else will run): " + strings.Join(allowed, ", ") + "."
}

func filesystemToolDefinitions(fsConfig ConfigFilesystemTool) []HarnessToolDefinition {
	definitions := []HarnessToolDefinition{
		{
			Name:        "list_files",
			Title:       "List files",
			Description: "Use this to inspect real files and directories under " + workspaceRootPhrase(fsConfig) + " on this machine.",
			Example:     `{"name":"list_files","path":"optional relative directory"}`,
			Risk:        HarnessToolRiskRead,
			ParamSchema: listFilesParamSchema(),
			Execute: func(_ context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.ListFiles(ToolFileListRequest{Path: call.Path})
				return output, fmt.Sprintf("listed %d entries", len(output.Entries)), err
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolFileListResult); ok {
					activity.Path = typed.Path
				}
				return activity
			},
		},
		{
			Name:        "read_file",
			Title:       "Read file",
			Description: "Use this to read a real text file from under " + workspaceRootPhrase(fsConfig) + " on this machine.",
			Example:     `{"name":"read_file","path":"relative/path.txt","maxBytes":20000}`,
			Risk:        HarnessToolRiskRead,
			ParamSchema: readFileParamSchema(),
			Validate: func(prefix string, call HarnessToolCall) []string {
				if strings.TrimSpace(call.Path) == "" {
					return []string{prefix + ".path is required for read_file"}
				}
				return nil
			},
			Execute: func(_ context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.ReadFile(ToolFileReadRequest{
					Path:        call.Path,
					MaxBytes:    call.MaxBytes,
					AllowBinary: call.AllowBinary,
				})
				return output, fmt.Sprintf("read %d bytes", output.Bytes), err
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolFileReadResult); ok {
					activity.Path = typed.Path
				}
				return activity
			},
		},
		{
			Name:        "run_command",
			Title:       "Run command",
			Description: runCommandDescription(fsConfig),
			Example:     `{"name":"run_command","command":"rg","args":["-n","Atelier","."],"cwd":"optional relative directory"}`,
			Risk:        HarnessToolRiskExec,
			ParamSchema: runCommandParamSchema(),
			NeedsPermission: func(call HarnessToolCall) bool {
				return !isReadOnlyCommandCall(call)
			},
			Validate: func(prefix string, call HarnessToolCall) []string {
				if strings.TrimSpace(call.Command) == "" {
					return []string{prefix + ".command is required for run_command"}
				}
				return nil
			},
			Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.RunCommand(ctx, ToolCommandRequest{
					Command:   call.Command,
					Args:      call.Args,
					Cwd:       call.Cwd,
					Env:       call.Env,
					TimeoutMS: call.TimeoutMS,
				})
				return output, commandResultSummary(output), err
			},
			Permission: func(call HarnessToolCall) ToolPermissionRequestEvent {
				command := append([]string{call.Command}, call.Args...)
				summary := formatCommandSummary(command)
				if summary == "" {
					summary = "Run command"
				}
				return ToolPermissionRequestEvent{
					Command: command,
					Cwd:     call.Cwd,
					Summary: summary,
				}
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolCommandResult); ok {
					activity.Command = typed.Command
					activity.Path = typed.Cwd
					activity.ExitCode = typed.ExitCode
					activity.StdoutPreview = previewToolContent(typed.Stdout)
					activity.StderrPreview = previewToolContent(typed.Stderr)
					activity.DurationMS = typed.DurationMS
				}
				return activity
			},
		},
		{
			Name:        "write_file",
			Title:       "Write file",
			Description: "Use this only when the user clearly asks to create or modify a real file under " + workspaceRootPhrase(fsConfig) + " on this machine.",
			Example:     `{"name":"write_file","path":"relative/path.txt","content":"text","overwrite":false,"append":false}`,
			Risk:        HarnessToolRiskWrite,
			ParamSchema: writeFileParamSchema(),
			Validate: func(prefix string, call HarnessToolCall) []string {
				var errors []string
				if strings.TrimSpace(call.Path) == "" {
					errors = append(errors, prefix+".path is required for write_file")
				}
				if call.Content == "" {
					errors = append(errors, prefix+".content is required for write_file")
				}
				return errors
			},
			Execute: func(_ context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
				output, err := tools.Filesystem.WriteFile(ToolFileWriteRequest{
					Path:      call.Path,
					Content:   call.Content,
					Append:    call.Append,
					Overwrite: call.Overwrite,
				})
				return output, fmt.Sprintf("wrote %d bytes", output.Bytes), err
			},
			Permission: func(call HarnessToolCall) ToolPermissionRequestEvent {
				summary := "Write file"
				if strings.TrimSpace(call.Path) != "" {
					summary = "Write " + call.Path
				}
				return ToolPermissionRequestEvent{
					Path:           call.Path,
					ContentPreview: previewToolContent(call.Content),
					Summary:        summary,
				}
			},
			Activity: func(result HarnessToolResult) HarnessToolActivity {
				activity := defaultHarnessToolActivity(result)
				if typed, ok := result.Result.(ToolFileWriteResult); ok {
					activity.Path = typed.Path
				}
				return activity
			},
		},
	}
	return definitions
}

func newHarnessToolRegistry(definitions []HarnessToolDefinition) HarnessToolRegistry {
	byName := make(map[string]HarnessToolDefinition, len(definitions))
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	return HarnessToolRegistry{definitions: definitions, byName: byName}
}

func (r HarnessToolRegistry) Get(name string) (HarnessToolDefinition, bool) {
	definition, ok := r.byName[strings.TrimSpace(name)]
	return definition, ok
}

func (r HarnessToolRegistry) Names() []string {
	names := make([]string, 0, len(r.definitions))
	for _, definition := range r.definitions {
		names = append(names, definition.Name)
	}
	return names
}

func (r HarnessToolRegistry) NamesCSV() string {
	return strings.Join(r.Names(), ", ")
}

func (r HarnessToolRegistry) PromptCatalog() string {
	lines := make([]string, 0, len(r.definitions))
	for _, definition := range r.definitions {
		line := "- " + definition.Example
		if strings.TrimSpace(definition.Description) != "" {
			line += " - " + definition.Description
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// videoAudioCapabilityMarker is the phrase videoGenerationDescription appends
// when the configured video model can produce audio. VideoAudioCapable probes
// for it rather than re-reading config so the registry stays the single source
// of truth for capability — the description and this predicate derive from the
// same flag set at build time and cannot drift.
const videoAudioCapabilityMarker = "can also generate synchronized audio"

// VideoAudioCapable reports whether the generate_video tool's description
// advertises native audio capability. It returns false when generate_video is
// not in the registry. Used by triage to bias narration requests toward a
// single generate_video call instead of a generate_speech + lip_sync chain.
func (r HarnessToolRegistry) VideoAudioCapable() bool {
	def, ok := r.Get("generate_video")
	if !ok {
		return false
	}
	return strings.Contains(def.Description, videoAudioCapabilityMarker)
}

func (definition HarnessToolDefinition) RequiresPermission() bool {
	return definition.Risk == HarnessToolRiskWrite || definition.Risk == HarnessToolRiskExec
}

func (definition HarnessToolDefinition) RequiresPermissionFor(call HarnessToolCall) bool {
	if definition.NeedsPermission != nil {
		return definition.NeedsPermission(call)
	}
	return definition.RequiresPermission()
}

func defaultHarnessToolActivity(result HarnessToolResult) HarnessToolActivity {
	return HarnessToolActivity{
		Name:   result.Name,
		Status: result.Status,
		Error:  result.Error,
	}
}

func formatCommandSummary(command []string) string {
	parts := make([]string, 0, len(command))
	for _, arg := range command {
		if strings.TrimSpace(arg) == "" {
			continue
		}
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func commandResultSummary(result ToolCommandResult) string {
	return fmt.Sprintf("command exited with code %d", result.ExitCode)
}

func isReadOnlyCommandCall(call HarnessToolCall) bool {
	if len(reqEnvWithoutBlanks(call.Env)) > 0 {
		return false
	}
	name := normalizedCommandName(call.Command)
	// The read-only set is the default allowlist — every command it ships is
	// inherently read-only. Reading from one source prevents the two lists from
	// drifting: a command allowed by default that isn't recognized here would
	// needlessly prompt for permission. Commands a user adds to their own
	// configured allowlist are not read-only by default (this can't know that).
	if !isDefaultReadOnlyCommand(name) {
		return false
	}
	for _, arg := range call.Args {
		if commandFlagDenied(name, commandFlagName(strings.TrimSpace(arg))) {
			return false
		}
	}
	return true
}

// isDefaultReadOnlyCommand reports whether name is one of the commands the
// default allowlist ships with. It is the single source of truth for which
// commands skip permission gating, so that list and the read-only check
// cannot drift apart.
func isDefaultReadOnlyCommand(name string) bool {
	for _, allowed := range defaultFilesystemToolAllowedCommands() {
		if normalizedCommandName(allowed) == name {
			return true
		}
	}
	return false
}
