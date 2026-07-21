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
	// AttachedImage is the source frame (a base64 data URL) the user attached to
	// the current turn, if any. generate_video uses it to animate the image via
	// an image-to-video model. Empty for the direct/UI tool path.
	AttachedImage string
	// AttachedAudio is the audio clip (a data URL) the user attached to the
	// current turn, if any. transcribe_audio consumes it via fal's Whisper/Wizper.
	// Like AttachedImage it is provider-agnostic: the planner decides whether to
	// transcribe it (any provider) or, on OpenRouter, send it as chat input.
	AttachedAudio   string
	GenerateImage   func(ctx context.Context, req ImageGenerateRequest) (ollamaGenerateResponse, []byte, error)
	GenerateVideo   func(ctx context.Context, req VideoGenerateRequest) (GeneratedVideo, error)
	GenerateAudio   func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error)
	TranscribeAudio func(ctx context.Context, model, audioURL, task, language string) (GeneratedTranscript, error)
	UpscaleImage    func(ctx context.Context, req ImageUpscaleRequest) (ollamaGenerateResponse, error)
}

// ToolImageResult carries generated images as data URLs. The Images field is
// stripped before the result is rendered into a tool message so base64 data
// never enters a model context; the harness extracts it for the UI and history.
type ToolImageResult struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Count  int      `json:"count"`
	Images []string `json:"images,omitempty"`
}

// ToolVideoResult carries generated videos as on-disk temp-file references, not
// bytes — video is a file-path artifact end to end. The Videos slice is stripped
// before the result is rendered into a tool message (the temp path is not useful
// model evidence); the harness moves each temp file into the conversation's
// artifacts directory when it persists the turn.
type ToolVideoResult struct {
	Model  string          `json:"model"`
	Prompt string          `json:"prompt"`
	Count  int             `json:"count"`
	Videos []ToolVideoFile `json:"videos,omitempty"`
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

func defaultHarnessToolRegistry(config AppConfig) HarnessToolRegistry {
	definitions := filesystemToolDefinitions(config.Tools.Filesystem)
	if imageGenerationConfigured(config) {
		definitions = append(definitions, imageGenerationToolDefinition())
	}
	if videoGenerationConfigured(config) {
		definitions = append(definitions, videoGenerationToolDefinition())
	}
	if audioGenerationConfigured(config) {
		definitions = append(definitions, audioGenerationToolDefinition())
	}
	if transcribeAudioConfigured(config) {
		definitions = append(definitions, transcribeAudioToolDefinition())
	}
	if imageUpscaleConfigured(config) {
		definitions = append(definitions, imageUpscaleToolDefinition())
	}
	return newHarnessToolRegistry(definitions)
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

func videoGenerationToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "generate_video",
		Title:       "Generate video",
		Description: "Use this when the user asks to create, animate, or render a video or short clip. Works from a text description, and when the user attached an image, animates that image (image-to-video). The clip is attached to the assistant reply. Generation runs for a minute or more. Pass negativePrompt to steer content away from unwanted elements, and generateAudio:false when the user wants a silent clip.",
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
			// An attached image switches to image-to-video: use the image-to-video
			// model and pass the image to fal as the source frame.
			attachedImage := strings.TrimSpace(tools.AttachedImage)
			model := strings.TrimSpace(call.Model)
			if model == "" {
				if attachedImage != "" {
					model = resolveDefaultVideoImageModel(tools.Config)
				} else {
					model = resolveDefaultVideoModel(tools.Config)
				}
			}
			if model == "" {
				return nil, "video generation unavailable", errors.New("no video model is configured")
			}
			videoReq := VideoGenerateRequest{
				Model:          model,
				Prompt:         strings.TrimSpace(call.Content),
				Duration:       tools.Config.Generation.Video.Duration,
				AspectRatio:    tools.Config.Generation.Video.AspectRatio,
				NegativePrompt: strings.TrimSpace(call.NegativePrompt),
				Image:          attachedImage,
				GenerateAudio:  call.GenerateAudio,
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
				Model:  model,
				Prompt: videoReq.Prompt,
				Count:  1,
				Videos: []ToolVideoFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
			}
			summary := fmt.Sprintf("generated a video with %s", model)
			if attachedImage != "" {
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

// audioGenerationConfigured reports whether the generate_audio tool should be
// offered: a fal audio model must be configured AND a fal.ai key must be
// present. fal is the only audio backend. The key check avoids offering a tool
// that is guaranteed to fail at call time with errFalKeyNotConfigured.
func audioGenerationConfigured(config AppConfig) bool {
	if strings.TrimSpace(config.Providers.Fal.AudioModel) == "" {
		return false
	}
	return falKeyConfigured()
}

// resolveDefaultAudioModel returns the model the generate_audio tool uses when
// the call doesn't override it.
func resolveDefaultAudioModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.AudioModel); model != "" {
		return model
	}
	return defaultFalAudioModel
}

// transcribeAudioConfigured reports whether the transcribe_audio tool should be
// offered: fal is the only transcription backend, and the default model
// (fal-ai/wizper) always applies, so the gate is purely the fal key — unlike
// generate_audio/video, no model needs to be configured first.
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

func audioGenerationToolDefinition() HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "generate_audio",
		Title:       "Generate audio",
		Description: "Use this when the user asks to generate audio: speak or narrate text (text-to-speech), or create music or a sound effect from a description. The configured fal.ai audio model generates it and the clip is attached to the assistant reply.",
		Example:     `{"name":"generate_audio","content":"a calm lo-fi piano loop with soft rain in the background"}`,
		Risk:        HarnessToolRiskRead,
		ParamSchema: generateAudioParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			if strings.TrimSpace(call.Content) == "" {
				return []string{prefix + ".content is required for generate_audio (the text or audio prompt)"}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.GenerateAudio == nil {
				return nil, "audio generation unavailable", errors.New("audio generation is not available in this context")
			}
			model := strings.TrimSpace(call.Model)
			if model == "" {
				model = resolveDefaultAudioModel(tools.Config)
			}
			if model == "" {
				return nil, "audio generation unavailable", errors.New("no audio model is configured")
			}
			audioReq := AudioGenerateRequest{
				Model:          model,
				Prompt:         strings.TrimSpace(call.Content),
				Duration:       strings.TrimSpace(call.Duration),
				NegativePrompt: strings.TrimSpace(call.NegativePrompt),
				Loop:           call.Loop,
				Voice:          strings.TrimSpace(call.Voice),
			}
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
				Model:   model,
				Prompt:  audioReq.Prompt,
				Count:   1,
				Audios:  []ToolAudioFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
				Notices: generated.Notices,
			}
			return output, fmt.Sprintf("generated audio with %s", model), nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolAudioResult); ok {
				activity.Command = []string{"fal", "generate", typed.Model}
			}
			return activity
		},
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
			// An attached image switches to image-to-image: use the image-to-image
			// model and pass the source frame to the generator to transform.
			attachedImage := strings.TrimSpace(tools.AttachedImage)
			model := strings.TrimSpace(call.Model)
			if model == "" {
				if attachedImage != "" {
					model = resolveDefaultImageEditModel(tools.Config)
				} else {
					model = resolveDefaultImageModel(tools.Config)
				}
			}
			if model == "" {
				return nil, "image generation unavailable", errors.New("no image model is configured")
			}
			imageReq := ImageGenerateRequest{
				Model:  model,
				Prompt: strings.TrimSpace(call.Content),
				Width:  tools.Config.Generation.Image.Width,
				Height: tools.Config.Generation.Image.Height,
				Steps:  tools.Config.Generation.Image.Steps,
			}
			// An explicit aspectRatio from the tool call overrides the configured
			// dimensions. The configured long edge sets the resolution budget;
			// width/height are derived from the ratio. Image-to-image ignores these
			// (fal derives dims from the source frame), so this is moot there.
			if ratio := strings.TrimSpace(call.AspectRatio); ratio != "" {
				baseLong := imageReq.Width
				if imageReq.Height > baseLong {
					baseLong = imageReq.Height
				}
				if w, h := imageSizeForAspectRatio(baseLong, ratio); w > 0 && h > 0 {
					imageReq.Width, imageReq.Height = w, h
				}
			}
			if attachedImage != "" {
				imageReq.Images = []string{attachedImage}
			}
			payload, raw, err := tools.GenerateImage(ctx, imageReq)
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
			output := ToolImageResult{Model: model, Prompt: imageReq.Prompt, Count: len(images), Images: images}
			summary := fmt.Sprintf("generated %d image%s with %s", len(images), pluralSuffix(len(images)), model)
			if attachedImage != "" {
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
	return defaultHarnessToolRegistry(defaultAppConfig())
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
			"aspectRatio": enumParam("Optional — the output image shape. Omit to use the configured default size; ignored when transforming an attached image.", "1:1", "16:9", "9:16", "4:3", "3:4"),
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
			attachedImage := strings.TrimSpace(tools.AttachedImage)
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

func generateVideoParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content":        stringParam("The video prompt — describe the clip to create."),
			"model":          stringParam("Optional fal.ai video model override."),
			"negativePrompt": stringParam("Optional — describe what to keep out of the clip (e.g. \"blurry, text, watermark\")."),
			"generateAudio":  boolParam("Optional — set false to render a silent clip on models that would otherwise add audio. Ignored by models that never produce audio."),
		},
		"required": []string{"content"},
	}
}

func generateAudioParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content": stringParam("The text to speak, or a description of the music/sound to create."),
			"model":   stringParam("Optional fal.ai audio model override."),
			"duration": stringParam("Optional — target clip length for music/sound-effect models (e.g. \"10\"). " +
				"Ignored by text-to-speech models, whose length follows the spoken text."),
			"negativePrompt": stringParam("Optional — describe what to keep out of the audio (e.g. \"vocals, percussion\"). " +
				"Ignored by text-to-speech models."),
			"loop": boolParam("Optional — set true for a seamless, gapless loop (ambient beds, backgrounds). " +
				"Only some sound-effect models support it; ignored otherwise with a note to the user."),
			"voice": stringParam("Optional — the voice for text-to-speech (e.g. \"Rachel\"). " +
				"Only text-to-speech models support it; ignored otherwise with a note to the user."),
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
