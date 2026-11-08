package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type AppConfig struct {
	Version int `json:"version"`
	// AirplaneMode is the offline guarantee: while on, Atelier itself never
	// contacts a cloud service. Chat runs on local Ollama/OpenAI-compatible
	// endpoints (loopback-verified), media generation and transcription
	// reduce to their local backends, and update checks stand down. The
	// enforcement seams live in airplane_mode.go.
	AirplaneMode bool             `json:"airplaneMode"`
	Storage      ConfigStorage    `json:"storage"`
	Providers    ConfigProviders  `json:"providers"`
	Models       ConfigModels     `json:"models"`
	Prompts      ConfigPrompts    `json:"prompts"`
	Generation   ConfigGeneration `json:"generation"`
	Tools        ConfigTools      `json:"tools"`
	UI           ConfigUI         `json:"ui"`
	Updates      ConfigUpdates    `json:"updates"`
}

// ConfigUpdates configures the self-updater. ManifestURL is overridable so the
// flow can be exercised against a local server. AutoCheck is a pointer so an
// absent field — a config written before the updater existed — still means
// the default "enabled" rather than silently disabling update checks.
type ConfigUpdates struct {
	ManifestURL string `json:"manifestUrl"`
	AutoCheck   *bool  `json:"autoCheck,omitempty"`
}

type ConfigStorage struct {
	Root      string `json:"root"`
	History   string `json:"history"`
	Artifacts string `json:"artifacts"`
	// Libraries is where library/project records live (see libraries.go).
	// Media never moves into it — assets stay in conversation dirs and
	// library listings fold across conversations.
	Libraries string `json:"libraries"`
}

type ConfigProviders struct {
	Ollama           ConfigOllama           `json:"ollama"`
	OpenRouter       ConfigOpenRouter       `json:"openrouter"`
	Fal              ConfigFal              `json:"fal"`
	Replicate        ConfigReplicate        `json:"replicate"`
	OpenAICompatible ConfigOpenAICompatible `json:"openaiCompatible"`
	Local            ConfigLocalProviders   `json:"local"`
}

type ConfigOllama struct {
	BaseURL string             `json:"baseURL"`
	Models  ConfigOllamaModels `json:"models"`
	NumCtx  int                `json:"numCtx"`
}

type ConfigOllamaModels struct {
	Primary string `json:"primary"`
	Harness string `json:"harness"`
	Image   string `json:"image"`
	// Speech is the local speech model generate_speech runs on when
	// Models.SpeechProvider is "ollama" (e.g. "phonon"). The gate requires
	// it — like the fal AudioModel slot, the tool is opt-in per model.
	Speech string `json:"speech,omitempty"`
}

type ConfigOpenRouter struct {
	Enabled bool   `json:"enabled"`
	Primary string `json:"primary,omitempty"`
	Harness string `json:"harness,omitempty"`
}

// ConfigFal configures the fal.ai image-generation backend. The API key lives
// in the OS keychain (see keychain.go), not in config — Enabled mirrors the
// key's presence for the frontend, like ConfigOpenRouter.
type ConfigFal struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`
	// ImageEditModel is the image-to-image endpoint used when the user attaches a
	// source image to transform; Model is the text-to-image endpoint.
	ImageEditModel string `json:"imageEditModel,omitempty"`
	// VideoModel is the text-to-video endpoint; VideoImageModel is the
	// image-to-video endpoint used when the user attaches an image to animate;
	// VideoExtendModel is the video-extend endpoint used when the user attaches a
	// video to continue (Veo extend); VideoMotionModel is the motion-control
	// endpoint used when the user attaches both an image and a video (the video's
	// motion is applied to the image's subject).
	VideoModel       string `json:"videoModel,omitempty"`
	VideoImageModel  string `json:"videoImageModel,omitempty"`
	VideoExtendModel string `json:"videoExtendModel,omitempty"`
	VideoMotionModel string `json:"videoMotionModel,omitempty"`
	// VideoKeyframeModel is the start→end keyframe endpoint used when the planner
	// sets imageRole:"keyframes" and two images are attached — the first is the
	// opening frame, the last the closing frame, and the model generates the
	// transition between them.
	VideoKeyframeModel string `json:"videoKeyframeModel,omitempty"`
	// AudioModel is the text-to-speech endpoint used by the generate_speech
	// tool; SoundEffectsModel is the text-to-audio endpoint (music and sound
	// effects) used by the generate_sound tool. AudioCloneModel is the
	// voice-cloning endpoint generate_speech switches to when the
	// user attached a reference clip — selected by attachment, like the
	// lipsync image/video split. AudioExtendModel is the audio-extend endpoint
	// used by the extend_audio tool (stable-audio-25/inpaint by default — the
	// audio sibling of VideoExtendModel). VoiceDesignModel is the voice-design
	// endpoint used by the generate_voice tool (MiniMax voice-design by
	// default): a written description of a voice becomes a preview clip plus a
	// reusable voice id.
	AudioModel        string `json:"audioModel,omitempty"`
	SoundEffectsModel string `json:"soundEffectsModel,omitempty"`
	AudioCloneModel   string `json:"audioCloneModel,omitempty"`
	AudioExtendModel  string `json:"audioExtendModel,omitempty"`
	VoiceDesignModel  string `json:"voiceDesignModel,omitempty"`
	// TranscribeModel is the speech-to-text endpoint used by transcribe_audio
	// when the transcription provider is fal (fal-ai/wizper by default). The
	// local whisper CLI is the provider-independent alternative — see
	// Models.TranscriptionProvider and local_tools.go.
	TranscribeModel string `json:"transcribeModel,omitempty"`
	// UpscaleModel is the image upscaler endpoint (fal-only; Ollama has none).
	UpscaleModel string `json:"upscaleModel,omitempty"`
	// InpaintModel is the mask-inpainting endpoint the image editor uses when
	// fal is the InpaintProvider — a model whose verified contract takes a
	// source image plus a selection mask (see image_edit.go).
	InpaintModel string `json:"inpaintModel,omitempty"`
	// VideoUpscaleModel is the video upscaler endpoint used by the upscale_video
	// tool (fal-only, like UpscaleModel) — a video-to-video transform, so the
	// picker partitions fal's video-to-video category by "upscal".
	VideoUpscaleModel string `json:"videoUpscaleModel,omitempty"`
	// VideoReframeModel is the generative video-reframe endpoint used by the
	// reframe_video tool (fal-only, like VideoUpscaleModel) — a video-to-video
	// transform that changes an attached clip's aspect ratio by outpainting the
	// added canvas (16:9 → 9:16 for Reels/Shorts) instead of cropping, so the
	// picker partitions fal's video-to-video category by "reframe".
	VideoReframeModel string `json:"videoReframeModel,omitempty"`
	// VideoRestyleModel is the video-restyle endpoint used by the restyle_video
	// tool (fal-only, like VideoReframeModel) — a video-to-video transform that
	// re-renders an attached clip under a prompt while keeping its motion (anime,
	// claymation, a different look or characters), so the picker partitions fal's
	// video-to-video category by the general edit/style markers minus the other
	// five families.
	VideoRestyleModel string `json:"videoRestyleModel,omitempty"`
	// LipsyncImageModel is the audio-to-video lip sync endpoint used when the
	// user attaches an audio clip plus an image (a talking head).
	// LipsyncVideoModel is the video-to-video lip sync endpoint used when the
	// user attaches an audio clip plus a video (re-lip-sync an existing clip).
	// Both serve when fal is the LipsyncProvider; the Replicate slots mirror
	// them for a replicate-pinned config.
	LipsyncImageModel string `json:"lipsyncImageModel,omitempty"`
	LipsyncVideoModel string `json:"lipsyncVideoModel,omitempty"`
	// SoundEffectsVideoModel is the video-to-sound-effects endpoint used by the
	// add_sound_effects tool (Mirelo SFX 2.0 video-to-video by default): the
	// model watches an attached clip and generates a synchronized soundtrack
	// for it, returning the video with the sound mixed in (or, on the
	// video-to-audio family, the standalone soundtrack).
	SoundEffectsVideoModel string `json:"soundEffectsVideoModel,omitempty"`
}

// ConfigReplicate configures the Replicate image/video-generation backend —
// the cloud sibling of ConfigFal for generate_image, upscale_image (which
// follow ImageProvider), generate_video, and upscale_video/restyle_video/
// reframe_video (which follow VideoProvider). The API token lives in the OS
// keychain (see keychain.go), not in config — Enabled mirrors the key's
// presence for the frontend, like ConfigFal. The non-voice audio tools
// (generate_sound, add_sound_effects, extend_audio) route by
// Models.AudioProvider, so the sound slots below serve when it picks
// replicate; generate_speech routes by Models.SpeechProvider, so SpeechModel
// serves when IT picks replicate; voice design and voice cloning stay
// fal-only, and lip_sync routes by Models.LipsyncProvider.
type ConfigReplicate struct {
	Enabled bool `json:"enabled"`
	// Model is the text-to-image model (an owner/name slug, e.g.
	// "black-forest-labs/flux-schnell"); ImageEditModel is the image-to-image
	// model used when the user attaches a source image to transform.
	Model          string `json:"model,omitempty"`
	ImageEditModel string `json:"imageEditModel,omitempty"`
	// VideoModel is the text-to-video model; VideoImageModel is the
	// image-to-video model used when the user attaches an image to animate;
	// VideoExtendModel is the video-extension model used when the user
	// attaches a video to continue (the extend turn routes here like the
	// transforms below rather than failing with a provider-switch remedy).
	VideoModel       string `json:"videoModel,omitempty"`
	VideoImageModel  string `json:"videoImageModel,omitempty"`
	VideoExtendModel string `json:"videoExtendModel,omitempty"`
	// UpscaleModel is the image upscaler upscale_image uses when replicate is
	// the image provider — upscale follows ImageProvider, so fal's
	// UpscaleModel serves on every other provider.
	UpscaleModel string `json:"upscaleModel,omitempty"`
	// InpaintModel is the mask-inpainting model the image editor uses when
	// replicate is the InpaintProvider — the Replicate sibling of
	// ConfigFal.InpaintModel.
	InpaintModel string `json:"inpaintModel,omitempty"`
	// VideoUpscaleModel is the video upscaler upscale_video uses when
	// replicate is the video provider — video upscale follows VideoProvider,
	// so fal's VideoUpscaleModel serves on fal.
	VideoUpscaleModel string `json:"videoUpscaleModel,omitempty"`
	// VideoRestyleModel is the video restyler restyle_video uses, and
	// VideoReframeModel the generative reframer reframe_video uses — both
	// follow VideoProvider like VideoUpscaleModel, with fal's slots serving
	// on fal.
	VideoRestyleModel string `json:"videoRestyleModel,omitempty"`
	VideoReframeModel string `json:"videoReframeModel,omitempty"`
	// TranscribeModel is the speech-to-text model transcribe_audio uses when
	// replicate is the transcription provider (openai/whisper by default) —
	// the Replicate sibling of ConfigFal.TranscribeModel. The picker lists
	// Replicate's speech-to-text collection.
	TranscribeModel string `json:"transcribeModel,omitempty"`
	// LipsyncImageModel is the audio-to-video lip sync model lip_sync uses
	// (a talking head from the attached image) and LipsyncVideoModel the
	// video-to-video model (re-lip-sync an attached clip) when replicate is
	// the LipsyncProvider — the Replicate siblings of ConfigFal's lipsync
	// pair. The pickers list Replicate's lipsync collection, post-filtered
	// by input schema.
	LipsyncImageModel string `json:"lipsyncImageModel,omitempty"`
	LipsyncVideoModel string `json:"lipsyncVideoModel,omitempty"`
	// SoundEffectsModel is the text-to-audio model generate_sound uses when
	// replicate is the AudioProvider (music and sound effects —
	// stable-audio-2.5 by default), SoundEffectsVideoModel the
	// video-to-sound model add_sound_effects uses (MMAudio by default,
	// whose soundtrack arrives as a standalone clip the planner chains into
	// replace_audio), and AudioExtendModel the continuation model
	// extend_audio uses (meta/musicgen by default — an attached clip is
	// continued forward, total output capped around 30s) — the Replicate
	// siblings of ConfigFal's audio slots. The pickers list Replicate's
	// ai-music-generation collection post-filtered per picker.
	SoundEffectsModel      string `json:"soundEffectsModel,omitempty"`
	SoundEffectsVideoModel string `json:"soundEffectsVideoModel,omitempty"`
	AudioExtendModel       string `json:"audioExtendModel,omitempty"`
	// SpeechModel is the text-to-speech model generate_speech uses when
	// replicate is the SpeechProvider (minimax/speech-2.8-turbo by default —
	// the MiniMax family fal's speech endpoints already drive, flat voice_id/
	// emotion/speed inputs) — the Replicate sibling of ConfigFal.AudioModel.
	// Voice design and cloning have no Replicate slot: an attached reference
	// stands down on this route with a notice, the Ollama route's contract.
	// The picker lists Replicate's text-to-speech collection, input-schema
	// checked.
	SpeechModel string `json:"speechModel,omitempty"`
}

// ConfigOpenAICompatible addresses a local server that speaks OpenAI's API
// shape (LocalAI, a diffusers shim, ...): chat via /v1/chat/completions and
// images via /v1/images/generations. Primary/Harness are chat models for the
// primary/harness roles; Model is the image model used by generate_image. The
// optional bearer key lives in the OS keychain (see keychain.go), not in
// config.
type ConfigOpenAICompatible struct {
	BaseURL string `json:"baseURL"`
	Primary string `json:"primary,omitempty"`
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
}

// ConfigLocalProviders configures locally installed CLI media tools — binaries
// on the user's machine rather than cloud services (see local_tools.go for
// detection and the registry). whisper powers local transcription; ffmpeg and
// ffprobe power the local media transforms (local_ffmpeg.go). Adding a tool
// here, a spec in knownLocalBinaries, and a config override case in
// configuredLocalBinaryOverride is the whole wiring for detection + Settings.
type ConfigLocalProviders struct {
	Whisper ConfigLocalWhisper     `json:"whisper"`
	FFmpeg  ConfigLocalFFmpeg      `json:"ffmpeg"`
	FFprobe ConfigLocalFFprobe     `json:"ffprobe"`
	Sips    ConfigLocalSips        `json:"sips"`
	Magick  ConfigLocalImageMagick `json:"magick"`
}

// ConfigLocalWhisper configures the local whisper CLI. Binary overrides
// detection (an absolute path or a bare PATH name; empty auto-detects
// "whisper" then "whisper-cli" on PATH). Model is the whisper model — an
// openai-whisper size ("small", "large-v3", ...) or, for whisper.cpp, a path
// to a ggml .bin file (a leading "~/" is expanded before it reaches the CLI;
// whisper-cli's fopen does no shell expansion). Empty resolves in tiers (see
// local_tools.go): WHISPER_MODEL from Atelier's environment, then
// ~/.whisper-base.en.bin, then ~/.cache/whisper.cpp/ggml-base.en.bin when
// present, else whisper-cli's own models/ default anchored to the user's home.
// VADModel is an optional voice-activity-detection model (whisper.cpp's
// ggml-silero): when set, whisper.cpp transcriptions run with --vad so
// long silences are skipped instead of transcribed as hallucinated text.
// whisper.cpp only — the openai-whisper CLI has no VAD flags, so a set value
// there rides a notice rather than the command line.
// ChunkMinutes is the optional long-clip chunk size for whisper.cpp (0
// disables): when the clip measures longer than this many minutes, the runner
// transcodes it to 16 kHz mono once, cuts it into that-many-minute segments
// with one ffmpeg pass, transcribes each chunk under its own timeout, and
// stitches the cues back together with offset timestamps — the full-file run's
// VAD behaves differently on long inputs (conv_863887f9cfcfc746ee9b7a4c: it
// decodes the silent gaps and hallucinates instead of the quiet speech), and
// short clips do not. Needs local ffmpeg; without it, or when the clip can't
// be measured, the runner falls back to the single pass.
type ConfigLocalWhisper struct {
	Binary       string `json:"binary,omitempty"`
	Model        string `json:"model,omitempty"`
	VADModel     string `json:"vadModel,omitempty"`
	ChunkMinutes int    `json:"chunkMinutes,omitempty"`
}

// ConfigLocalFFmpeg configures the local ffmpeg CLI behind the video tools
// (screenshot, split, join, extract audio, replace audio). Binary overrides
// detection (an absolute path or a bare PATH name; empty auto-detects
// "ffmpeg" on PATH). No model — one ffmpeg dialect.
type ConfigLocalFFmpeg struct {
	Binary string `json:"binary,omitempty"`
}

// ConfigLocalFFprobe configures the local ffprobe CLI (metadata for
// probe_media and the copy-vs-reencode decisions). Empty auto-detects
// "ffprobe" on PATH and then the directory of the resolved ffmpeg binary —
// they ship together.
type ConfigLocalFFprobe struct {
	Binary string `json:"binary,omitempty"`
}

// ConfigLocalSips configures the macOS-bundled sips CLI behind the basic
// local image tools (convert_image, transform_image, probe_image — see
// local_images.go). sips ships with every macOS, so detection almost always
// succeeds; the override exists for non-standard setups and tests.
type ConfigLocalSips struct {
	Binary string `json:"binary,omitempty"`
}

// ConfigLocalImageMagick configures the ImageMagick CLI behind the advanced
// local image tools (compose_images, adjust_image, optimize_image — sips
// cannot watermark, build collages, adjust colors, or strip metadata). Empty
// auto-detects "magick" (ImageMagick 7) then "convert" (legacy ImageMagick 6)
// on PATH.
type ConfigLocalImageMagick struct {
	Binary string `json:"binary,omitempty"`
}

type ConfigModels struct {
	PrimaryProvider string `json:"primaryProvider,omitempty"`
	// HarnessProvider selects where the harness model runs (triage, skill
	// selection, planning). Absent in configs written before harness provider
	// selection existed, so it normalizes to "ollama" — see mergeAppConfig.
	HarnessProvider string `json:"harnessProvider,omitempty"`
	ImageProvider   string `json:"imageProvider,omitempty"`
	// VideoProvider selects the generate_video backend: "fal" (the historical
	// only backend) or "replicate". Absent in configs written before the
	// second video provider existed, so it normalizes to "fal" — every
	// existing setup keeps its behavior exactly. Normalized in mergeAppConfig.
	VideoProvider string `json:"videoProvider,omitempty"`
	// TranscriptionProvider selects the transcribe_audio backend: "fal"
	// (fal.ai speech-to-text) or "local-whisper" (the locally installed
	// whisper CLI — see local_tools.go). Empty auto-resolves at use time:
	// fal when its API key is configured, else a detected local whisper —
	// so a config written before this setting existed keeps its old
	// behaviour exactly while a fal-less machine with whisper installed
	// still lights up. Normalized in mergeAppConfig.
	TranscriptionProvider string `json:"transcriptionProvider,omitempty"`
	// InpaintProvider selects the image editor's mask-inpainting backend:
	// "fal" (the default) or "replicate". Independent of ImageProvider on
	// purpose — a user's ordinary image generator and their verified
	// mask-capable editor model need not share a backend. Normalized in
	// mergeAppConfig like VideoProvider.
	InpaintProvider string `json:"inpaintProvider,omitempty"`
	// LipsyncProvider selects the lip_sync backend: "fal" (the default) or
	// "replicate". Independent of VideoProvider the way InpaintProvider is
	// independent of ImageProvider — lip_sync spans all three media kinds
	// (audio plus an image or video face), so tying it to the video backend
	// would surprise. Normalized in mergeAppConfig like VideoProvider.
	LipsyncProvider string `json:"lipsyncProvider,omitempty"`
	// SpeechProvider selects the generate_speech backend: "fal" (the
	// default), "ollama" — Ollama's speech models (phonon and friends,
	// the "speech" capability) served through its OpenAI-compatible
	// /v1/audio/speech endpoint — or "replicate", the text-to-speech
	// collection's models over the prediction lifecycle. The scope is the
	// voice models alone: music, sound effects, and audio extension route
	// by AudioProvider, and voice design and voice cloning stay fal-only
	// regardless (the ollama and replicate routes stand a cloning reference
	// down with a notice). The model slots are Providers.Ollama.Models.Speech
	// and Providers.Replicate.SpeechModel. Normalized in mergeAppConfig like
	// VideoProvider.
	SpeechProvider string `json:"speechProvider,omitempty"`
	// AudioProvider selects the NON-voice audio backend — generate_sound
	// and add_sound_effects: "fal" (the default) or "replicate". Its own
	// dimension, the LipsyncProvider shape: speech routes by SpeechProvider,
	// and voice design, cloning, and extend_audio stay fal-only regardless,
	// so tying the non-voice family to either the speech or the video
	// backend would surprise. The model slots are Providers.Fal /
	// Providers.Replicate's SoundEffectsModel + SoundEffectsVideoModel.
	// Normalized in mergeAppConfig like VideoProvider.
	AudioProvider string `json:"audioProvider,omitempty"`
}

type ConfigPrompts struct {
	System string `json:"system"`
}

type ConfigGeneration struct {
	Image ConfigImageGeneration `json:"image"`
	Video ConfigVideoGeneration `json:"video"`
	Audio ConfigAudioGeneration `json:"audio"`
	// MediaTimeoutSeconds bounds one queued media generation (fal / Replicate —
	// image, video, audio, upscale, and the video transforms) from submit to
	// delivered bytes. On timeout the shared policy (media_timeout.go) recovers
	// a job that completed anyway and cancels one still running; it never
	// bounds the synchronous backends or the local CLI tools. Zero inherits the
	// default — normalized in mergeAppConfig.
	MediaTimeoutSeconds int `json:"mediaTimeoutSeconds,omitempty"`
}

type ConfigImageGeneration struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
	// SizePreset is the default output resolution tier ("1k", "2k", "4k") —
	// the fallback when a generate_image call states no resolution. Pre-tier
	// values (draft/standard/high/high+ and raw pixels) are migrated onto
	// tiers by mergeAppConfig; fal models that speak tiers natively are told
	// the tier itself, pixel-only models get dimensions derived from it.
	SizePreset string `json:"sizePreset,omitempty"`
	Steps      int    `json:"steps"`
	// Width/Height are legacy fields, retained so mergeAppConfig can migrate
	// pre-preset config.json values into SizePreset. New configs leave them
	// unset; pixels are derived from AspectRatio + SizePreset at call time.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

// ConfigVideoGeneration holds the user-facing text-to-video knobs. Duration
// and AspectRatio are fal enum strings ("5"/"10", "16:9"/"9:16"/"1:1");
// Resolution is a tier ("480p"/"720p"/"1080p"/"4k", empty = let the model
// choose); other fal parameters (negative prompt, cfg scale) use the model's
// own defaults.
type ConfigVideoGeneration struct {
	Duration    string `json:"duration"`
	AspectRatio string `json:"aspectRatio"`
	// Resolution is the default output resolution tier for generate_video —
	// the fallback when a call states no resolution, mirroring
	// ConfigImageGeneration.SizePreset. Empty (the default) lets the model
	// choose, the pre-setting behavior. Tiers vary by model; the provider
	// resolvers drop a tier the selected model's enum doesn't list with a
	// notice rather than 422ing.
	Resolution string `json:"resolution,omitempty"`
}

// ConfigAudioGeneration holds the user-facing audio generation knobs. Duration
// is seconds as a string ("10"), or "auto" (the default) to let the model
// decide the clip length from the prompt — the generate_sound fallback when a
// call states no duration, mirroring ConfigVideoGeneration.Duration. Speech
// (generate_speech) deliberately has no duration: its length follows the
// spoken text. ExtendDuration is extend_audio's default ADDED length — "auto"
// (the default) keeps each extend model's own behavior (the inpaint/outpaint
// flavors' built-in 30s; sonauto decides from the audio).
type ConfigAudioGeneration struct {
	Duration       string `json:"duration"`
	ExtendDuration string `json:"extendDuration"`
}

type ConfigTools struct {
	Filesystem ConfigFilesystemTool `json:"filesystem"`
}

type ConfigFilesystemTool struct {
	Root            string   `json:"root"`
	MaxOutputBytes  int      `json:"maxOutputBytes"`
	TimeoutMS       int      `json:"timeoutMs"`
	AllowedCommands []string `json:"allowedCommands"`
}

type ConfigUI struct {
	Mode string `json:"mode"`
}

func (a *App) GetConfig() (AppConfig, error) {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	config, err := loadAppConfig()
	if err != nil {
		return AppConfig{}, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return AppConfig{}, err
	}
	a.baseURL = config.Providers.Ollama.BaseURL
	return config, nil
}

func (a *App) SaveConfig(config AppConfig) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	merged := mergeAppConfig(config)
	if err := writeAppConfig(merged); err != nil {
		return err
	}
	if err := ensureStorageDirs(merged.Storage); err != nil {
		return err
	}
	a.baseURL = merged.Providers.Ollama.BaseURL
	return nil
}

func normalizeBaseURL(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", nil
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", errors.New("base URL must include a host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".atelier", "config.json"), nil
}

func loadAppConfig() (AppConfig, error) {
	path, err := configPath()
	if err != nil {
		return AppConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultAppConfig(), nil
		}
		return AppConfig{}, err
	}

	var config AppConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return AppConfig{}, err
	}
	return mergeAppConfig(config), nil
}

// loadReadyConfig loads the merged config and ensures every storage directory
// it references exists. This is the boilerplate every read-side App method
// needs before touching history or artifacts, factored out so each caller is a
// single line instead of the load → ensure → error ritual. Read-only: unlike
// GetConfig, it never writes config back to disk.
func loadReadyConfig() (AppConfig, error) {
	config, err := loadAppConfig()
	if err != nil {
		return AppConfig{}, err
	}
	if err := ensureStorageDirs(config.Storage); err != nil {
		return AppConfig{}, err
	}
	return config, nil
}

func writeAppConfig(config AppConfig) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(mergeAppConfig(config), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func defaultAppConfig() AppConfig {
	root := defaultStorageRoot()
	history := filepath.Join(root, "history")
	autoCheckDefault := true
	return AppConfig{
		Version: 1,
		Storage: ConfigStorage{
			Root:      root,
			History:   history,
			Artifacts: history,
			Libraries: filepath.Join(root, "libraries"),
		},
		Providers: ConfigProviders{
			Ollama: ConfigOllama{
				BaseURL: defaultOllamaBaseURL,
				Models: ConfigOllamaModels{
					Primary: "mistral-small3.1:latest",
					Harness: "mistral-small3.1:latest",
					Image:   "x/z-image-turbo:latest",
				},
				NumCtx: defaultOllamaNumCtx,
			},
			OpenAICompatible: ConfigOpenAICompatible{
				BaseURL: defaultOpenAICompatibleBaseURL,
			},
		},
		Models: ConfigModels{
			PrimaryProvider: "ollama",
			HarnessProvider: "ollama",
			ImageProvider:   "ollama",
			// InpaintProvider defaults to fal: the editor's first verified
			// mask-capable catalog lives there (image_edit.go).
			InpaintProvider: "fal",
		},
		Prompts: ConfigPrompts{
			System: "You are Atelier, a precise local AI collaborator.",
		},
		Generation: ConfigGeneration{
			Image: ConfigImageGeneration{
				AspectRatio: defaultImageAspectRatio,
				SizePreset:  defaultImageSizePreset,
				Steps:       24,
			},
			Video: ConfigVideoGeneration{
				Duration:    defaultFalVideoDuration,
				AspectRatio: defaultFalVideoAspectRatio,
			},
			Audio: ConfigAudioGeneration{
				Duration:       defaultFalSoundDuration,
				ExtendDuration: defaultFalAudioExtendDuration,
			},
			MediaTimeoutSeconds: defaultMediaGenerationTimeoutSeconds,
		},
		Tools: ConfigTools{
			Filesystem: ConfigFilesystemTool{
				Root:            defaultDocumentsRoot(),
				MaxOutputBytes:  64 * 1024,
				TimeoutMS:       defaultToolTimeoutMS,
				AllowedCommands: defaultFilesystemToolAllowedCommands(),
			},
		},
		UI: ConfigUI{
			Mode: "chat",
		},
		Updates: ConfigUpdates{
			ManifestURL: defaultUpdateManifestURL,
			AutoCheck:   &autoCheckDefault,
		},
	}
}

func mergeAppConfig(config AppConfig) AppConfig {
	defaults := defaultAppConfig()
	if config.Version <= 0 {
		config.Version = defaults.Version
	}
	config.Storage = mergeStorageConfig(config.Storage, defaults.Storage)
	if normalized, err := normalizeBaseURL(config.Providers.Ollama.BaseURL); err == nil && normalized != "" {
		config.Providers.Ollama.BaseURL = normalized
	} else {
		config.Providers.Ollama.BaseURL = defaults.Providers.Ollama.BaseURL
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Primary) == "" {
		config.Providers.Ollama.Models.Primary = defaults.Providers.Ollama.Models.Primary
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Harness) == "" {
		config.Providers.Ollama.Models.Harness = config.Providers.Ollama.Models.Primary
	}
	if strings.TrimSpace(config.Providers.Ollama.Models.Image) == "" {
		config.Providers.Ollama.Models.Image = defaults.Providers.Ollama.Models.Image
	}
	if config.Providers.Ollama.NumCtx <= 0 {
		config.Providers.Ollama.NumCtx = defaults.Providers.Ollama.NumCtx
	}
	// The OpenAI-compatible image server has no canonical model id (unlike the
	// fal catalog), so only the endpoint defaults; the model stays unset until
	// the user picks or types one.
	if normalized, err := normalizeBaseURL(config.Providers.OpenAICompatible.BaseURL); err == nil && normalized != "" {
		config.Providers.OpenAICompatible.BaseURL = normalized
	} else {
		config.Providers.OpenAICompatible.BaseURL = defaults.Providers.OpenAICompatible.BaseURL
	}
	if strings.TrimSpace(config.Models.PrimaryProvider) == "" {
		config.Models.PrimaryProvider = defaults.Models.PrimaryProvider
	}
	// HarnessProvider selects where triage, skill selection, and planning run.
	// Normalize unknown or empty values to the Ollama default: an absent field
	// means a config written before this setting existed, which must keep its
	// old behaviour exactly.
	switch strings.TrimSpace(config.Models.HarnessProvider) {
	case "ollama", "openrouter", "openai-compatible":
		config.Models.HarnessProvider = strings.TrimSpace(config.Models.HarnessProvider)
	default:
		config.Models.HarnessProvider = defaults.Models.HarnessProvider
	}
	// ImageProvider selects the generate_image backend. Normalize unknown or
	// empty values to the Ollama default so the tool path never sees a stray id.
	switch strings.TrimSpace(config.Models.ImageProvider) {
	case "ollama", "fal", "replicate", "openai-compatible":
		config.Models.ImageProvider = strings.TrimSpace(config.Models.ImageProvider)
	default:
		config.Models.ImageProvider = defaults.Models.ImageProvider
	}
	// VideoProvider selects the generate_video backend ("fal" | "replicate").
	// Normalize unknown or empty values to "fal": an absent field means a
	// config written before the second video provider existed, which must keep
	// routing to fal exactly as before.
	switch strings.TrimSpace(config.Models.VideoProvider) {
	case "fal", "replicate":
		config.Models.VideoProvider = strings.TrimSpace(config.Models.VideoProvider)
	default:
		config.Models.VideoProvider = "fal"
	}
	// TranscriptionProvider selects the transcribe_audio backend ("fal" |
	// "replicate" | "local-whisper"). Unknown or empty normalizes to "" —
	// empty auto-resolves at use time (fal when its key is configured, else
	// a detected local whisper; replicate only serves when pinned), so a
	// config written before this setting existed keeps its old behaviour
	// exactly. Deliberately not seeded from defaults: the effective provider
	// depends on live key/detection state.
	switch strings.TrimSpace(config.Models.TranscriptionProvider) {
	case transcriptionProviderFal, transcriptionProviderLocalWhisper, transcriptionProviderReplicate:
		config.Models.TranscriptionProvider = strings.TrimSpace(config.Models.TranscriptionProvider)
	default:
		config.Models.TranscriptionProvider = ""
	}
	// InpaintProvider selects the image editor's mask-inpainting backend
	// ("fal" | "replicate"). Unknown or empty normalizes to "fal", the
	// default: an absent field means a config written before the editor
	// existed, which must keep routing there.
	switch strings.TrimSpace(config.Models.InpaintProvider) {
	case "fal", "replicate":
		config.Models.InpaintProvider = strings.TrimSpace(config.Models.InpaintProvider)
	default:
		config.Models.InpaintProvider = "fal"
	}
	// LipsyncProvider selects the lip_sync backend ("fal" | "replicate").
	// Unknown or empty normalizes to "fal", the default: an absent field
	// means a config written before Replicate served lipsync, which must
	// keep routing there. The model slots carry no forced defaults — the
	// resolvers fall back to known-good constants on both providers, the
	// transcription slots' contract.
	switch strings.TrimSpace(config.Models.LipsyncProvider) {
	case "fal", "replicate":
		config.Models.LipsyncProvider = strings.TrimSpace(config.Models.LipsyncProvider)
	default:
		config.Models.LipsyncProvider = "fal"
	}
	// SpeechProvider selects the generate_speech backend ("fal" | "ollama" |
	// "replicate"). Unknown or empty normalizes to "fal": speech was
	// fal-only before Ollama's speech models existed, so a config written
	// then must keep routing there. The Ollama model slot carries no forced
	// default — the gate requires it and the resolver's constant is only a
	// belt; the Replicate slot seeds below the provider-selection way.
	switch strings.TrimSpace(config.Models.SpeechProvider) {
	case "fal", "ollama", "replicate":
		config.Models.SpeechProvider = strings.TrimSpace(config.Models.SpeechProvider)
	default:
		config.Models.SpeechProvider = "fal"
	}
	// AudioProvider selects the non-voice audio backend ("fal" |
	// "replicate") for generate_sound and add_sound_effects. Unknown or
	// empty normalizes to "fal": audio was fal-only before Replicate served
	// it, so a config written then must keep routing there. Like the lipsync
	// slots, the model slots carry no forced defaults beyond the seeds below.
	switch strings.TrimSpace(config.Models.AudioProvider) {
	case "fal", "replicate":
		config.Models.AudioProvider = strings.TrimSpace(config.Models.AudioProvider)
	default:
		config.Models.AudioProvider = "fal"
	}
	// Local CLI tool settings carry no forced defaults: an empty binary means
	// PATH auto-detection and an empty model means the CLI's own default.
	config.Providers.Local.Whisper.Binary = strings.TrimSpace(config.Providers.Local.Whisper.Binary)
	config.Providers.Local.Whisper.Model = strings.TrimSpace(config.Providers.Local.Whisper.Model)
	config.Providers.Local.Whisper.VADModel = strings.TrimSpace(config.Providers.Local.Whisper.VADModel)
	// The chunk size is minutes; a non-positive value disables chunking and a
	// nonsense-large one would make single-invocation runs pointless, so it
	// clamps to [0, 120].
	if config.Providers.Local.Whisper.ChunkMinutes < 0 || config.Providers.Local.Whisper.ChunkMinutes > 120 {
		config.Providers.Local.Whisper.ChunkMinutes = 0
	}
	config.Providers.Local.FFmpeg.Binary = strings.TrimSpace(config.Providers.Local.FFmpeg.Binary)
	config.Providers.Local.FFprobe.Binary = strings.TrimSpace(config.Providers.Local.FFprobe.Binary)
	if config.Models.ImageProvider == "fal" && strings.TrimSpace(config.Providers.Fal.Model) == "" {
		config.Providers.Fal.Model = defaultFalImageModel
	}
	// Same seed for the Replicate image path: selecting it as the image
	// provider without a model leaves the picker empty, so pin the default.
	if config.Models.ImageProvider == "replicate" && strings.TrimSpace(config.Providers.Replicate.Model) == "" {
		config.Providers.Replicate.Model = defaultReplicateImageModel
	}
	// Seed the editor's inpaint endpoint the same way: a provider selected
	// without a model would leave the editor's Generate permanently disabled.
	if config.Models.InpaintProvider == "fal" && strings.TrimSpace(config.Providers.Fal.InpaintModel) == "" {
		config.Providers.Fal.InpaintModel = defaultFalInpaintModel
	}
	if config.Models.InpaintProvider == "replicate" && strings.TrimSpace(config.Providers.Replicate.InpaintModel) == "" {
		config.Providers.Replicate.InpaintModel = defaultReplicateInpaintModel
	}
	// Seed the sound-effects endpoint for configs written before the
	// generate_speech/generate_sound split: an AudioModel already configured for
	// the old combined generate_audio tool implies audio generation is in use, so
	// the new generate_sound tool lights up without a Settings visit.
	if strings.TrimSpace(config.Providers.Fal.AudioModel) != "" && strings.TrimSpace(config.Providers.Fal.SoundEffectsModel) == "" {
		config.Providers.Fal.SoundEffectsModel = defaultFalSoundEffectsModel
	}
	// Seed the voice-clone endpoint the same way: anyone with speech configured
	// gets cloning when they attach a reference clip, without a Settings visit.
	if strings.TrimSpace(config.Providers.Fal.AudioModel) != "" && strings.TrimSpace(config.Providers.Fal.AudioCloneModel) == "" {
		config.Providers.Fal.AudioCloneModel = defaultFalAudioCloneModel
	}
	// Seed the voice-design endpoint the same way: anyone with speech configured
	// gets generate_voice, so a "create a voice" ask routes to it without a
	// Settings visit.
	if strings.TrimSpace(config.Providers.Fal.AudioModel) != "" && strings.TrimSpace(config.Providers.Fal.VoiceDesignModel) == "" {
		config.Providers.Fal.VoiceDesignModel = defaultFalVoiceDesignModel
	}
	// Seed the video sound-effects endpoint the same way: anyone with speech
	// configured gets add_sound_effects, so an "add sound effects to this video"
	// ask routes to it without a Settings visit.
	if strings.TrimSpace(config.Providers.Fal.AudioModel) != "" && strings.TrimSpace(config.Providers.Fal.SoundEffectsVideoModel) == "" {
		config.Providers.Fal.SoundEffectsVideoModel = defaultFalSoundEffectsVideoModel
	}
	// Seed the Replicate sound pair the provider-selection way (the image and
	// inpaint seeds above): selecting replicate as the AudioProvider without a
	// model would leave the pickers empty and the gates closed, so pin the
	// defaults once. The extend slot seeds the same way — the audio family
	// routes as one dimension.
	if config.Models.AudioProvider == "replicate" {
		if strings.TrimSpace(config.Providers.Replicate.SoundEffectsModel) == "" {
			config.Providers.Replicate.SoundEffectsModel = defaultReplicateSoundEffectsModel
		}
		if strings.TrimSpace(config.Providers.Replicate.SoundEffectsVideoModel) == "" {
			config.Providers.Replicate.SoundEffectsVideoModel = defaultReplicateSoundEffectsVideoModel
		}
		if strings.TrimSpace(config.Providers.Replicate.AudioExtendModel) == "" {
			config.Providers.Replicate.AudioExtendModel = defaultReplicateAudioExtendModel
		}
	}
	// Seed the Replicate speech slot the same way: selecting replicate as the
	// SpeechProvider without a model would leave the picker empty and the
	// generate_speech gate closed.
	if config.Models.SpeechProvider == "replicate" && strings.TrimSpace(config.Providers.Replicate.SpeechModel) == "" {
		config.Providers.Replicate.SpeechModel = defaultReplicateSpeechModel
	}
	if strings.TrimSpace(config.Prompts.System) == "" {
		config.Prompts.System = defaults.Prompts.System
	}
	if strings.TrimSpace(config.Generation.Image.AspectRatio) == "" {
		config.Generation.Image.AspectRatio = defaults.Generation.Image.AspectRatio
	}
	if strings.TrimSpace(config.Generation.Image.SizePreset) == "" {
		// One-time migration: legacy config.json carried width/height in pixels.
		// Fold the old long edge into the closest resolution tier.
		if migrated := legacyImageSizePreset(config.Generation.Image.Width, config.Generation.Image.Height); migrated != "" {
			config.Generation.Image.SizePreset = migrated
		} else {
			config.Generation.Image.SizePreset = defaults.Generation.Image.SizePreset
		}
	}
	// Vocabulary migration: presets written before the tier rework
	// (draft/standard/high/high+) map onto the nearest resolution tier so an
	// existing config keeps working without a Settings visit.
	config.Generation.Image.SizePreset = migrateImageSizePresetTier(config.Generation.Image.SizePreset)
	if config.Generation.Image.Steps <= 0 {
		config.Generation.Image.Steps = defaults.Generation.Image.Steps
	}
	if strings.TrimSpace(config.Generation.Video.Duration) == "" {
		config.Generation.Video.Duration = defaults.Generation.Video.Duration
	}
	if strings.TrimSpace(config.Generation.Video.AspectRatio) == "" {
		config.Generation.Video.AspectRatio = defaults.Generation.Video.AspectRatio
	}
	// "auto" is a meaningful value (the model decides), so only an absent field
	// backfills — the pre-setting behavior for configs written before the
	// audio group existed.
	config.Generation.Audio.Duration = strings.TrimSpace(config.Generation.Audio.Duration)
	if config.Generation.Audio.Duration == "" {
		config.Generation.Audio.Duration = defaults.Generation.Audio.Duration
	}
	config.Generation.Audio.ExtendDuration = strings.TrimSpace(config.Generation.Audio.ExtendDuration)
	if config.Generation.Audio.ExtendDuration == "" {
		config.Generation.Audio.ExtendDuration = defaults.Generation.Audio.ExtendDuration
	}
	// A non-positive timeout is not "unbounded" — an absent field (config
	// written before the knob existed) and a junk value both land on the
	// default, so no queued generation is ever left without a bound.
	if config.Generation.MediaTimeoutSeconds <= 0 {
		config.Generation.MediaTimeoutSeconds = defaults.Generation.MediaTimeoutSeconds
	}
	// Canonicalize the tier (trim + lowercase) and drop unknown values: empty
	// means "let the model choose", so a junk hand-edited config.json value
	// must become empty rather than ride a request the resolvers would notice
	// about on every video turn.
	config.Generation.Video.Resolution = normalizeVideoResolutionTier(config.Generation.Video.Resolution)
	config.Tools = mergeToolsConfig(config.Tools, defaults.Tools)
	config.UI.Mode = defaults.UI.Mode
	if strings.TrimSpace(config.Updates.ManifestURL) == "" {
		config.Updates.ManifestURL = defaults.Updates.ManifestURL
	}
	if config.Updates.AutoCheck == nil {
		config.Updates.AutoCheck = defaults.Updates.AutoCheck
	}
	return config
}

// legacyImageSizePreset maps a legacy config.json width/height pair (raw pixels)
// onto the closest resolution tier by long edge. Returns "" when neither
// dimension is set (0/missing), so the caller falls back to the default tier.
// Used once during mergeAppConfig to migrate pre-preset configs.
func legacyImageSizePreset(width, height int) string {
	longEdge := width
	if height > longEdge {
		longEdge = height
	}
	if longEdge <= 0 {
		return ""
	}
	switch {
	case longEdge <= 1536:
		return "1k"
	case longEdge <= 3072:
		return "2k"
	default:
		return "4k"
	}
}

// migrateImageSizePresetTier maps the pre-rework preset vocabulary onto
// resolution tiers and passes tier values through. Ties round to the cheaper
// tier (standard's 1536-long-edge lands on 1k, preserving the cheap default;
// high+'s 2560 lands on 2k rather than jumping to 4k). Unknown values fall
// back to the default tier.
func migrateImageSizePresetTier(preset string) string {
	switch strings.TrimSpace(preset) {
	case "1k", "2k", "4k":
		return strings.TrimSpace(preset)
	case "draft", "standard":
		return "1k"
	case "high", "high+":
		return "2k"
	}
	return defaultImageSizePreset
}

func mergeToolsConfig(tools ConfigTools, defaults ConfigTools) ConfigTools {
	tools.Filesystem.Root = normalizeStoragePath(tools.Filesystem.Root)
	if tools.Filesystem.Root == "" {
		tools.Filesystem.Root = defaults.Filesystem.Root
	}
	if tools.Filesystem.MaxOutputBytes <= 0 {
		tools.Filesystem.MaxOutputBytes = defaults.Filesystem.MaxOutputBytes
	}
	if tools.Filesystem.TimeoutMS <= 0 {
		tools.Filesystem.TimeoutMS = defaults.Filesystem.TimeoutMS
	}
	if len(tools.Filesystem.AllowedCommands) == 0 {
		tools.Filesystem.AllowedCommands = append([]string{}, defaults.Filesystem.AllowedCommands...)
	}
	return tools
}

func defaultFilesystemToolAllowedCommands() []string {
	return []string{
		"cat",
		"df",
		"du",
		"echo",
		"find",
		"grep",
		"head",
		"ls",
		"pwd",
		"rg",
		"tail",
		"wc",
	}
}

func defaultStorageRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".atelier")
	}
	return filepath.Join(home, ".atelier")
}

func defaultDocumentsRoot() string {
	// ~/Documents is the default workspace root for new conversations. It's
	// user-editable in Settings; this is just the seed value, intentionally
	// not influenced by XDG_DOCUMENTS_DIR so the default is predictable.
	return normalizeStoragePath(filepath.Join(userHomeFallback(), "Documents"))
}

func userHomeFallback() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return home
	}
	if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
		return home
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	return "."
}

func mergeStorageConfig(storage ConfigStorage, defaults ConfigStorage) ConfigStorage {
	storage.Root = normalizeStoragePath(storage.Root)
	storage.History = normalizeStoragePath(storage.History)
	storage.Artifacts = normalizeStoragePath(storage.Artifacts)
	storage.Libraries = normalizeStoragePath(storage.Libraries)
	if storage.Root == "" {
		storage.Root = defaults.Root
	}
	if storage.History == "" {
		storage.History = filepath.Join(storage.Root, "history")
	}
	if storage.Artifacts == "" {
		storage.Artifacts = storage.History
	}
	if storage.Libraries == "" {
		storage.Libraries = filepath.Join(storage.Root, "libraries")
	}
	return storage
}

func normalizeStoragePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		path = filepath.Join(home, path[2:])
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func ensureStorageDirs(storage ConfigStorage) error {
	for _, path := range []string{
		storage.Root,
		storage.History,
		storage.Artifacts,
		storage.Libraries,
		filepath.Join(storage.History, "conversations"),
		filepath.Join(storage.History, "indexes"),
	} {
		// An empty entry is an unconfigured path, not one to create — merged
		// configs always fill these (see mergeStorageConfig); hand-built
		// partial configs (tests) may set only the dirs they use.
		if path == "" {
			continue
		}
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	return nil
}
