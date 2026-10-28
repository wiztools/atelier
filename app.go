package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const defaultOllamaBaseURL = "http://localhost:11434"

// defaultOpenAICompatibleBaseURL is the endpoint of the OpenAI-compatible
// image server (LocalAI's default port; any /v1/images/generations server can
// be configured instead).
const defaultOpenAICompatibleBaseURL = "http://localhost:8080"

// defaultOllamaNumCtx is sent as num_ctx on every chat call so the context
// window is explicit and identical across calls (Ollama reloads the model
// when num_ctx changes between requests). 16K because the planner's prompt
// carries the tool catalog before any conversation or evidence — in native
// tool-calling mode that alone is ~5K tokens (conv_4fe9e638: a replan round
// hit 10,857 prompt tokens against 8,192, and Ollama's overflow behavior is
// to truncate from the FRONT, dropping the planner's instructions).
const defaultOllamaNumCtx = 16384

type App struct {
	ctx          context.Context
	client       *http.Client
	updateClient *http.Client
	baseURL      string
	configMu     sync.Mutex
	streams      map[string]context.CancelFunc
	// streamConversations mirrors streams keyed the same (requestID), holding
	// the conversationID each active stream is writing to — the signal the
	// library/project delete and move guards need, since a stream in flight
	// during a hard delete would resurrect turns into a removed directory.
	streamConversations map[string]string
	streamsMu           sync.Mutex
	permissions         map[string]chan bool
	permissionsMu       sync.Mutex
	toolPermission      func(context.Context, ToolPermissionRequestEvent) ToolPermissionDecision
	updatesMu           sync.Mutex
	lastUpdate          *updateManifest
	updaterQuitting     atomic.Bool
	// falPricing caches fal's per-endpoint unit pricing for media cost
	// estimation (fal_pricing.go). Created once here so every gateway rebuild
	// shares the TTL window; nil on bare &App{} literals, which the estimator
	// treats as "no pricing available" (fail-soft zero).
	falPricing *falPricingCache
	// mediaUploads tracks in-progress staged uploads (media_upload.go) keyed
	// by upload ID — the Begin/Append/Complete chunk lifecycle. A session that
	// never completes (bridge reload, crash) is simply unaddressable; the
	// uploads sweep reclaims the partial file by age.
	mediaUploads   map[string]*MediaUploadSession
	mediaUploadsMu sync.Mutex
	// editOps tracks the editors' in-flight operations keyed by
	// operation ID (edit_session.go): the one-op-per-session guard,
	// CancelImageEdit's handle, and the "is it live?" check orphaned
	// queued/running records consult. editOpsMu guards the map.
	editOps   map[string]*editOpRun
	editOpsMu sync.Mutex
	// compositionRenders tracks the video composer's in-flight render
	// operations keyed by operation ID (composition_render.go): the
	// one-render-per-conversation guard and CancelCompositionRender's
	// handle. compositionRendersMu guards the map.
	compositionRenders       map[string]*compositionRenderRun
	compositionRenderHistory map[string]CompositionRenderOperation
	compositionRendersMu     sync.Mutex
	videoPreviewMu           sync.Mutex
	videoPreviewJobs         map[string]*videoPreviewJob
	videoSourcePreviews      map[string]EditSourceInfo
	// editSubmitMu serializes submit-time validation, persistence, and
	// in-flight registration. Without it, two quick submits into the same
	// session could both pass the running check before either registered.
	editSubmitMu sync.Mutex
	// falCategoryCache caches one fetched fal model category per key (the
	// category id), shared by every Settings lister that reads it — the
	// screen walks ~a dozen categories on load, and re-paging all of them on
	// every open both tripled the request count and, under fal's /v1/models
	// rate limit, emptied the tail listers' pickers (the speech combobox
	// "didn't open"). A cached category also serves a rate-limited re-fetch
	// as the last good copy instead of a failure. Guarded by falCategoryMu;
	// falCategoryCacheTTL bounds freshness. falCategoryFetch is a test seam —
	// nil uses the real fetch.
	falCategoryMu    sync.Mutex
	falCategories    map[string]falCategoryEntry
	falCategoryFetch func(context.Context, string) ([]FalModel, error)
}

func NewApp() *App {
	app := &App{
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
		// The updater gets its own client so a hung check never queues behind
		// a long model stream on the shared one; each call still bounds
		// itself with a context deadline appropriate to its phase (seconds
		// for the manifest, the client timeout for the download).
		updateClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
		baseURL:                  defaultOllamaBaseURL,
		streams:                  map[string]context.CancelFunc{},
		streamConversations:      map[string]string{},
		permissions:              map[string]chan bool{},
		editOps:                  map[string]*editOpRun{},
		compositionRenders:       map[string]*compositionRenderRun{},
		compositionRenderHistory: map[string]CompositionRenderOperation{},
		videoPreviewJobs:         map[string]*videoPreviewJob{},
		videoSourcePreviews:      map[string]EditSourceInfo{},
		falPricing:               newFalPricingCache(),
	}
	app.toolPermission = app.requestToolPermission
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Create the storage directories once at launch from the current config so
	// the first chat/tool/history operation doesn't pay for it. The per-call
	// ensureStorageDirs in loadReadyConfig stays as a no-op-ish backstop for a
	// SaveConfig that changed the storage root mid-session.
	if config, err := loadAppConfig(); err == nil {
		_ = ensureStorageDirs(config.Storage)
		a.baseURL = config.Providers.Ollama.BaseURL
		// A previous process may have died mid-edit: downgrade its orphaned
		// queued/running operations to failed so reopened sessions never show
		// a phantom in-flight edit (edit_session.go).
		sweepInterruptedEditOps(config.Storage)
	}
	cleanupEditSourcePreviews()
	cleanupVideoEditStaging()
	cleanupAudioEditStaging()
	cleanupCompositionPreviews()
	a.startUpdateScheduler(ctx)
}

// beforeClose is wired to Wails' OnBeforeClose hook. It prompts the user to
// confirm before the window closes; returning true prevents the close.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	// The self-updater's own quit: the user already chose to install, and the
	// detached helper is waiting on this process to exit — skip the dialog.
	if a.updaterQuitting.Load() {
		return false
	}
	choice, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         "Quit Atelier?",
		Message:       "Are you sure you want to quit Atelier?",
		Buttons:       []string{"Quit", "Cancel"},
		DefaultButton: "Quit",
		CancelButton:  "Cancel",
	})
	if err != nil {
		// If the dialog fails for any reason, don't trap the user in the app.
		return false
	}
	return choice != "Quit"
}

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

type OllamaStatus struct {
	Online  bool   `json:"online"`
	Version string `json:"version,omitempty"`
	BaseURL string `json:"baseURL"`
	Error   string `json:"error,omitempty"`
}

type OllamaModel struct {
	Name            string   `json:"name"`
	ModifiedAt      string   `json:"modifiedAt,omitempty"`
	Size            int64    `json:"size,omitempty"`
	Family          string   `json:"family,omitempty"`
	Parameter       string   `json:"parameter,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	ImageGeneration bool     `json:"imageGeneration,omitempty"`
	// SpeechGeneration marks a text-to-speech model (Ollama's "speech"
	// capability — phonon and friends), the Settings speech picker's filter
	// the way ImageGeneration is the image picker's.
	SpeechGeneration bool `json:"speechGeneration,omitempty"`
}

type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		ModifiedAt string `json:"modified_at"`
		Size       int64  `json:"size"`
		Details    struct {
			Family        string `json:"family"`
			ParameterSize string `json:"parameter_size"`
		} `json:"details"`
	} `json:"models"`
}

type ollamaShowResponse struct {
	Capabilities []string       `json:"capabilities"`
	ModelInfo    map[string]any `json:"model_info"`
	Details      struct {
		Family        string `json:"family"`
		ParameterSize string `json:"parameter_size"`
	} `json:"details"`
}

type ChatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
	// Audios carries user-attached audio as data URLs (data:audio/<fmt>;base64,...),
	// parallel to Images. Audio input is OpenRouter-only — the harness rejects an
	// audio attachment on any other provider before the turn runs (Ollama has no
	// audio input API). Like Images, the bytes are stripped before triage and the
	// OpenRouter adapter is the sole place they become input_audio content parts.
	Audios []string `json:"audios,omitempty"`
	// Videos carries user-attached video as data URLs (data:video/<fmt>;base64,...),
	// parallel to Audios. Unlike audio, video input is tool-only — it never
	// reaches a chat model: messagesWithoutMedia strips the bytes before triage
	// (and therefore before any model call), and no provider adapter emits a
	// video content part. Tools consume it via AttachedVideos (Veo extend, lip
	// sync). Bytes are validated with isVideoBytes before being persisted.
	Videos []string `json:"videos,omitempty"`
	// Subtitles carries user-attached subtitle files (.vtt/.srt) as data URLs,
	// parallel to Videos. Like video it is tool-only — no provider adapter
	// emits it as model input, and messagesWithoutMedia strips the bytes. The
	// turn persistence writes each file as a trc_<hex>.<ext> transcript
	// artifact, which is the identity convert_subtitles reads and the assets
	// panel and @-mentions list (subtitle.go).
	Subtitles []string `json:"subtitles,omitempty"`
	// Markdowns carries user-attached markdown documents (.md/.markdown) as
	// data URLs, parallel to Subtitles — tool-only, stripped before any model
	// call, each persisted as a md_<hex>.md artifact (markdown_editor.go) that
	// the markdown editor opens and the assets panel and @-mentions list.
	Markdowns []string   `json:"markdowns,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall mirrors the function-call shape a provider returns in
// message.tool_calls (and accepts in assistant message history). Both Ollama
// and OpenRouter use this shape, despite the Ollama-originated field name on
// the wire. Arguments is kept raw so the planner loop can validate before
// unmarshalling. The nested function payload is a named type (not an anonymous
// struct) so the Wails binding generator can mirror it into models.ts.
type ToolCall struct {
	Type     string       `json:"type,omitempty"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function payload of a tool call.
type ToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ChatRequest struct {
	RequestID      string         `json:"requestID,omitempty"`
	ConversationID string         `json:"conversationId,omitempty"`
	BaseURL        string         `json:"baseURL,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model"`
	SelectedModel  string         `json:"selectedModel,omitempty"`
	System         string         `json:"system,omitempty"`
	Messages       []ChatMessage  `json:"messages"`
	Think          any            `json:"think,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
	Format         any            `json:"format,omitempty"`
	// Tools is only set on planner calls when the harness model supports native
	// tool-calling. The final-response request (preparedResponseRequest) never
	// sets it, so the primary model stays tool-free.
	Tools []map[string]any `json:"tools,omitempty"`
	// Workspace selects the filesystem-tool root for a NEW conversation
	// (ConversationID empty). It is read once at creation and persisted onto
	// the conversation record, after which it is immutable. For an existing
	// conversation (turn 2+) this field is ignored — the record's Workspace
	// wins. Empty falls back to the configured default root.
	Workspace string `json:"workspace,omitempty"`
	// ProjectID assigns a NEW conversation (ConversationID empty) to a library
	// project. Read once at creation and persisted onto the conversation
	// record, immutable afterwards — same lifecycle as Workspace. For an
	// existing conversation the field is ignored; the record's ProjectID wins.
	// Must reference an existing project or the turn fails before anything is
	// persisted (resolveTurnProject). Empty means a standalone conversation.
	ProjectID string `json:"projectId,omitempty"`
	// ModelOverrides pins model selections for a NEW conversation
	// (ConversationID empty) — the turn-1 twin of the record's
	// HistoryConversation.ModelOverrides. Applied to the first turn's config
	// (applyConversationModelOverrides) and persisted onto the new record at
	// creation (requestModelOverrides), after which the record is the truth —
	// for an existing conversation this field is ignored. Unlike Workspace it
	// stays mutable afterwards via SetConversationModelOverrides.
	ModelOverrides *ConversationModelOverrides `json:"modelOverrides,omitempty"`
	// ReferencedAssetIDs carries the @-mentioned asset IDs for this turn —
	// ConversationAsset IDs from ListConversationAssets. The harness resolves
	// each ID to its artifact and delivers the media through the tool
	// attachment slots; while set, the latest-wins history fallback for
	// attachments is disabled, so the user's explicit references define the
	// turn's media. IDs only — no media bytes ever ride this field, so
	// nothing enters model context through it.
	ReferencedAssetIDs []string `json:"referencedAssetIds,omitempty"`
	// ProjectNotes carries the instruction block of the project this turn's
	// conversation belongs to, resolved server-side at turn start
	// (resolveTurnProjectNotes) and never sent by the frontend. It rides the
	// request because req already flows unchanged into the triage and planner
	// prompt builders; json:"-" keeps it off the wire and out of the frontend
	// bindings, since a client-set note would bypass the project record (the
	// single source the project editor writes).
	ProjectNotes string `json:"-"`
}

type ChatStreamStart struct {
	RequestID      string `json:"requestID"`
	ConversationID string `json:"conversationId"`
	// UserVideos carries the just-persisted user-turn video attachments'
	// hydrated "/atelier-artifact" URLs in attachment order, so the live
	// transcript can swap its composer data: URLs for artifact srcs the
	// moment the turn starts — the poster frame derives from the artifact
	// path, never a data URL, and the base64 payload leaves the UI state.
	UserVideos []string `json:"userVideos,omitempty"`
	// UserSubtitles carries the just-persisted user-turn subtitle attachments'
	// artifact URLs, positional like UserVideos. The Save binding reads
	// artifact paths only, so the swap is what makes a just-attached subtitle
	// downloadable without a reload.
	UserSubtitles []string `json:"userSubtitles,omitempty"`
	// UserMarkdowns carries the just-persisted user-turn markdown attachments'
	// artifact URLs, positional like UserSubtitles — the same swap, so a
	// just-attached document is downloadable and editable without a reload.
	UserMarkdowns []string `json:"userMarkdowns,omitempty"`
}

type ChatStreamEvent struct {
	RequestID string   `json:"requestID"`
	Content   string   `json:"content,omitempty"`
	Thinking  string   `json:"thinking,omitempty"`
	Images    []string `json:"images,omitempty"`
	Videos    []string `json:"videos,omitempty"`
	Audios    []string `json:"audios,omitempty"`
	// Transcripts carries the "/atelier-artifact" URLs of timestamped
	// transcript artifacts (.vtt) a turn produced, parallel to Videos/Audios.
	Transcripts []string `json:"transcripts,omitempty"`
	// Markdowns carries the "/atelier-artifact" URLs of document artifacts
	// (md_) the document authoring pass produced, parallel to Transcripts —
	// the live UI renders their Edit/Download chips without a reload.
	Markdowns      []string `json:"markdowns,omitempty"`
	Done           bool     `json:"done"`
	Error          string   `json:"error,omitempty"`
	Model          string   `json:"model,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Tokens         int      `json:"tokens,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
}

type ToolPermissionRequestEvent struct {
	ID             string   `json:"id"`
	RequestID      string   `json:"requestID,omitempty"`
	ConversationID string   `json:"conversationId,omitempty"`
	ToolName       string   `json:"toolName"`
	Action         string   `json:"action"`
	Summary        string   `json:"summary"`
	Command        []string `json:"command,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	Path           string   `json:"path,omitempty"`
	ContentPreview string   `json:"contentPreview,omitempty"`
}

type ollamaChatChunk struct {
	Model   string `json:"model"`
	Done    bool   `json:"done"`
	Message struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	DoneReason      string `json:"done_reason"`
	EvalCount       int    `json:"eval_count"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	Error           string `json:"error"`
}

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role      string     `json:"role"`
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	EvalCount       int    `json:"eval_count"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	Error           string `json:"error"`
}

type ImageGenerateRequest struct {
	RequestID      string `json:"requestID,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	BaseURL        string `json:"baseURL,omitempty"`
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	// AspectRatio is the named output shape (e.g. "16:9", "9:16") the caller
	// requested. Width/Height are derived from it, but some fal image models
	// (notably seedream) only honor aspect ratio via a preset enum string
	// ("landscape_16_9", "portrait_16_9") on their image_size param, and reject
	// or ignore width/height objects below a minimum pixel area. Carrying the
	// ratio lets resolveImageBody send the enum the model actually respects.
	AspectRatio string `json:"aspectRatio,omitempty"`
	// Resolution is the effective output resolution tier ("1k", "2k", "4k") —
	// the planner's explicit choice, else the configured default. It is the
	// tier-native sibling of AspectRatio: nano banana models take it directly on
	// a resolution enum, seedream via its auto_1K/auto_2K image_size values, and
	// pixel-only models via Width/Height derived from the tier's long-edge
	// budget. resolveImageBody drops it with a notice where the model can't
	// express it.
	Resolution string `json:"resolution,omitempty"`
	// ResolutionExplicit is true only when Resolution was set by the planner
	// (an explicit request in the prompt), not filled from the configured
	// default tier. resolveImageBody gates its drop notices on it so the
	// configured default never produces a notice on every generation. Tagged
	// json:"-" — internal signal, never serialized, mirroring
	// VideoGenerateRequest.AspectRatioExplicit.
	ResolutionExplicit bool     `json:"-"`
	Steps              int      `json:"steps,omitempty"`
	Images             []string `json:"images,omitempty"`
}

// ImageUpscaleRequest is the input to fal's image upscaler. Scale is a float
// matching fal's native `scale` param (2 or 4); the model-facing tool surface
// uses a "2x"/"4x" enum that Execute converts. fal is the only upscale backend.
type ImageUpscaleRequest struct {
	Model string  `json:"model"`
	Image string  `json:"image"`
	Scale float64 `json:"scale,omitempty"`
}

// VideoUpscaleRequest is the input to fal's video upscaler — the video sibling
// of ImageUpscaleRequest. Video is the source clip (a URL or base64 data URI);
// Scale is a float that resolveVideoUpscaleBody maps onto the endpoint's native
// factor field (scale / scale_factor / upscale_factor). The result is a
// GeneratedVideo, carried through the same artifact pipeline as generate_video.
type VideoUpscaleRequest struct {
	Model string  `json:"model"`
	Video string  `json:"video"`
	Scale float64 `json:"scale,omitempty"`
}

// VideoReframeRequest is the input to fal's generative video reframer — the
// outpainting sibling of VideoUpscaleRequest. Video is the source clip (a URL
// or base64 data URI), AspectRatio the target shape ("9:16"), Resolution the
// optional output tier ("720p"/"1080p"; empty lets the model apply its own
// default). The result is a GeneratedVideo, carried through the same artifact
// pipeline as generate_video.
type VideoReframeRequest struct {
	Model       string `json:"model"`
	Video       string `json:"video"`
	AspectRatio string `json:"aspectRatio,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
}

// VideoRestyleRequest is the input to fal's video restyler — the style-transfer
// sibling of VideoReframeRequest. Video is the source clip (a URL or base64
// data URI) whose motion is kept, Prompt the new-look instruction ("anime
// style", "claymation", "turn the hero into a rubber duck"), and Images
// optional style/appearance reference frames (a character sheet, a look) that
// ride when the model declares an image input — the "a different character"
// combination. Resolution is the optional output tier; NegativePrompt is
// dropped with a notice on models without one. The result is a GeneratedVideo,
// carried through the same artifact pipeline as generate_video.
type VideoRestyleRequest struct {
	Model          string   `json:"model"`
	Video          string   `json:"video"`
	Prompt         string   `json:"prompt,omitempty"`
	Images         []string `json:"images,omitempty"`
	NegativePrompt string   `json:"negativePrompt,omitempty"`
	Resolution     string   `json:"resolution,omitempty"`
}

// VideoGenerateRequest is the input to a video generation (text-to-video,
// image-to-video, multi-image reference-to-video, or video extend). The
// canonical params here are mapped onto the model's native fields by
// resolveVideoBody; the transport (FalClient.GenerateVideo) receives only the
// resolved body. Images is the source frame(s) for image-to-video or
// reference-to-video; Video is the source clip for a Veo extend endpoint.
// Resolution falls back to the Settings → Generation → Video tier when the
// planner states none; FPS is a planner-only knob — both are dropped with
// a notice when the selected model has no matching input.
type VideoGenerateRequest struct {
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
	Duration    string `json:"duration,omitempty"`
	AspectRatio string `json:"aspectRatio,omitempty"`
	// AspectRatioExplicit is true only when AspectRatio was set by the planner
	// (an explicit request in the prompt), not filled from config or derived
	// from a source image. resolveVideoBody uses it to decide whether to send
	// aspect_ratio for image-to-video: an explicit ratio overrides the source
	// frame's orientation (and is sent), while a config/detected default is
	// dropped so the model inherits the frame. Tagged json:"-" — it is an
	// internal signal, never serialized or surfaced to the frontend.
	AspectRatioExplicit bool `json:"-"`
	// ConfigAspectRatio carries the Settings → Generation → Video aspect
	// ratio, the user's standing orientation preference. On reference-guided
	// turns — where the model's source images are guidance (character sheets,
	// style references) rather than the canvas — the fal resolver withdraws
	// the gateway's image-derived ratio and sends this instead: reference
	// models don't inherit orientation from their reference media
	// (conv_8b8bffd58dcb383ee1cbfeae: a 2752x1536 landscape character sheet
	// produced a 720x1280 portrait clip when no ratio reached fal and
	// seedance-2.5's "auto" default ignored the reference). May be "auto" —
	// the let-the-model-decide sentinel. Tagged json:"-" — internal
	// provenance, like AspectRatioExplicit.
	ConfigAspectRatio string `json:"-"`
	// VideoRole mirrors generate_video's useVideoAs ("" / "motion" default,
	// "reference") so resolveVideoBody can tell reference-video turns from
	// extends and motion control without re-deriving the diagnosis: reference
	// videos are guidance (their shape must not set the output orientation),
	// an extend continues its source clip, and motion control follows the
	// motion video. Tagged json:"-" — internal intent signal, like ExtendSource.
	VideoRole string `json:"-"`
	// ExtendSource is true only when the tool layer diagnosed the turn as a
	// continuation of the attached clip — a video-only, non-reference turn routed
	// to the video-extend model — mirroring how AspectRatioExplicit carries the
	// planner's intent. resolveVideoBody uses it to select the "extension" task
	// on multi-task reference models (bytedance/seedance-2.5/reference-to-video
	// folds continuation into a task enum rather than a dedicated extend
	// endpoint; its "reference" default would silently generate a new clip
	// guided by the source instead of continuing it). Tagged json:"-" — it is
	// an internal intent signal, never serialized or surfaced to the frontend.
	ExtendSource bool `json:"-"`
	// Keyframes is true only when the tool layer diagnosed the turn as a
	// start→end transition (imageRole:"keyframes", exactly two source images).
	// resolveVideoBody uses it to split the images: the first onto image_url,
	// the last onto the model's end-frame field. Tagged json:"-" — an internal
	// intent signal, never serialized or surfaced to the frontend.
	Keyframes      bool   `json:"-"`
	NegativePrompt string `json:"negativePrompt,omitempty"`
	// Resolution is an optional output resolution tier (a fal enum string, e.g.
	// "720p", "1080p", "4k"). Tiers vary by model; resolveVideoBody drops a tier
	// the selected model's enum doesn't list with a notice rather than 422ing at
	// fal. Empty lets the model use its own default. The executor fills it from
	// the Settings → Generation → Video tier when the planner states none.
	Resolution string `json:"resolution,omitempty"`
	// FPS is an optional output frame rate (e.g. "24", "30", "60"). Some video
	// models expose an fps/frame_rate input; resolveVideoBody drops a value the
	// selected model doesn't accept (no such input, or an enum that doesn't list
	// it) with a notice rather than 422ing at fal. Omit to let the model use its
	// own default. Planner-only, like Resolution — there is no persisted config
	// default for it.
	FPS string `json:"fps,omitempty"`
	// Image, when set, is a single source frame for image-to-video generation —
	// a URL or a base64 data URI. It maps to fal's image_url input and requires
	// an image-to-video model. Legacy scalar field; Images is preferred. The two
	// are unified by SourceImages().
	Image string `json:"image,omitempty"`
	// Images, when set, are the source frames for image-to-video or
	// reference-to-video generation — URLs or base64 data URIs. They map to
	// fal's image_url (single) or image_urls (array) input depending on the
	// model's schema. A multi-image request against a scalar-image model fails
	// the tool call with a clear error (see resolveVideoBody).
	Images []string `json:"images,omitempty"`
	// Video, when set, is the source clip a Veo extend endpoint continues — a
	// URL or a base64 data URI. It maps to fal's video_url input and requires an
	// extend-capable model (e.g. fal-ai/veo3.1/extend-video). Legacy scalar
	// field; Videos is preferred. The two are unified by SourceVideos().
	Video string `json:"video,omitempty"`
	// Videos, when set, are the source clips for extend / motion control /
	// reference-to-video generation — URLs or base64 data URIs. They map to
	// fal's video_url (single) or video_urls (array) input depending on the
	// model's schema. A multi-video request against a scalar-video model fails
	// the tool call with a clear error (see resolveVideoBody).
	Videos []string `json:"videos,omitempty"`
	// GenerateAudio maps to fal's generate_audio input on audio-capable video
	// models. A pointer so "unspecified" (nil, let the model default) stays
	// distinct from an explicit false (silent clip) — the latter is what "video
	// without audio" requests. The absence of a generate_audio field does not
	// mean the model is silent: some endpoints emit synchronized audio by
	// default yet expose no toggle. When the caller asks for silence and the
	// model has no generate_audio input, the resolver can't comply and emits a
	// notice instead of silently dropping the request (see resolveVideoBody).
	GenerateAudio *bool `json:"generateAudio,omitempty"`
}

// SourceImages returns the non-empty source images for the request, preferring
// Images (the slice) and falling back to the legacy scalar Image. This is the
// single read point for source images — resolvers and the gateway loop over it
// instead of reading the two fields directly, so the slice plumbing stays in
// one place.
func (req VideoGenerateRequest) SourceImages() []string {
	var raw []string
	if len(req.Images) > 0 {
		raw = req.Images
	} else if s := strings.TrimSpace(req.Image); s != "" {
		raw = []string{s}
	}
	out := make([]string, 0, len(raw))
	for _, img := range raw {
		if s := strings.TrimSpace(img); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SourceVideos returns the non-empty source videos for the request, preferring
// Videos (the slice) and falling back to the legacy scalar Video — the video
// sibling of SourceImages and its single read point.
func (req VideoGenerateRequest) SourceVideos() []string {
	var raw []string
	if len(req.Videos) > 0 {
		raw = req.Videos
	} else if s := strings.TrimSpace(req.Video); s != "" {
		raw = []string{s}
	}
	out := make([]string, 0, len(raw))
	for _, vid := range raw {
		if s := strings.TrimSpace(vid); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// LipsyncGenerateRequest is the input to a lip sync generation. An audio clip
// drives a face: either an image (audio-to-video, a talking head) or an existing
// video (video-to-video, re-lip-synced). The mode is determined by which source
// is set — exactly one of Image or Video. resolveLipsyncBody maps these onto the
// model's native audio_url/image_url/video_url fields.
type LipsyncGenerateRequest struct {
	Model string `json:"model"`
	// Audio is the driving audio track — required. A URL or base64 data URI.
	Audio string `json:"audio,omitempty"`
	// Image is the face source for audio-to-video lip sync. Set for audio-to-video
	// mode (a talking head). Mutually exclusive with Video.
	Image string `json:"image,omitempty"`
	// Video is the face source for video-to-video lip sync. Set for video-to-video
	// mode (re-lip-sync an existing clip). Mutually exclusive with Image.
	Video string `json:"video,omitempty"`
}

// SFXGenerateRequest is the input to a video sound-effects generation: the
// attached clip plus optional steering. resolveSFXBody maps these onto the
// model's native fields (video_url, text_prompt/prompt, negative_prompt,
// preserve_speech, duration).
type SFXGenerateRequest struct {
	Model string `json:"model"`
	// Video is the clip to generate sound for — required. A URL or data URI.
	Video string `json:"video,omitempty"`
	// Prompt steers the soundtrack: name off-screen sources and ambience not
	// shown in the picture. Optional — the model reads the picture itself when
	// empty (endpoints that require a prompt get a generic one).
	Prompt string `json:"prompt,omitempty"`
	// NegativePrompt names sounds to exclude, as a short comma-separated list.
	NegativePrompt string `json:"negativePrompt,omitempty"`
	// Duration is the length of generated sound in seconds. Zero sends nothing
	// (the model's own default); the gateway fills the source clip's measured
	// length when the planner gave none, so the soundtrack covers the video
	// instead of the endpoints' short API default.
	Duration float64 `json:"duration,omitempty"`
	// PreserveSpeech keeps input speech and generates the rest around it (nil =
	// the model's default, on where dialogue exists); false yields sound
	// effects only and suppresses speech.
	PreserveSpeech *bool `json:"preserveSpeech,omitempty"`
}

// SFXGenerateResult carries whichever media kind the sound-effects endpoint
// returned: Video when the endpoint mixed the generated soundtrack into the
// clip (Mirelo SFX 2.0 video-to-video, MMAudio V2), Audio when it delivered
// the soundtrack as a standalone clip (Mirelo SFX 2.0 video-to-audio).
// Exactly one side is populated on success.
type SFXGenerateResult struct {
	Video GeneratedVideo `json:"video,omitempty"`
	Audio GeneratedAudio `json:"audio,omitempty"`
}

type SaveImageRequest struct {
	Image         string `json:"image"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

// SaveVideoRequest asks to copy a generated video artifact to a user-chosen
// location. Path is the on-disk artifact path (not a URL); the frontend passes
// the plain filesystem path the asset handler served the video from.
type SaveVideoRequest struct {
	Path          string `json:"path"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

// AudioGenerateRequest is the input to a text-to-audio generation. The prompt is
// the text to synthesize (speech) or describe (music/sound effects). Duration and
// NegativePrompt are forwarded only when set — text-to-speech models ignore them,
// so callers can leave both empty (see FalClient.GenerateAudio).
type AudioGenerateRequest struct {
	Model string `json:"model"`
	// Kind names the calling tool ("speech" from generate_speech, "sound"
	// from generate_sound): the gateway routes only speech by
	// Models.SpeechProvider — Ollama's local /v1/audio/speech path — while
	// sound generation stays fal-only, even though both share this request
	// shape and one transport hook. Empty keeps the historical fal routing.
	Kind           string `json:"kind,omitempty"`
	Prompt         string `json:"prompt"`
	Duration       string `json:"duration,omitempty"`
	NegativePrompt string `json:"negativePrompt,omitempty"`
	// Loop requests a seamless, gapless loop (sound-effect models). Voice selects
	// a text-to-speech voice. Style names the genre/mood for music models with a
	// style control (fal-ai/diffrhythm's required style_prompt, e.g. "jazz") —
	// it keeps the prompt pure lyrics/description. All three are resolved
	// against the target model's schema and dropped-with-notice when the model
	// has no matching parameter.
	Loop  bool   `json:"loop,omitempty"`
	Voice string `json:"voice,omitempty"`
	Style string `json:"style,omitempty"`
	// Emotion names the delivery tone for speech models that expose an emotion
	// control (the MiniMax speech-2.5/2.8 endpoints declare voice_setting.emotion
	// as a seven-value enum — happy, sad, angry, fearful, disgusted, surprised,
	// neutral). Resolved like the other params — enum-checked against the model's
	// schema and dropped-with-notice when the model has no emotion input, so a
	// non-MiniMax speech model keeps its own default tone.
	Emotion string `json:"emotion,omitempty"`
	// Speed is the speaking-rate multiplier for speech models that expose one
	// (MiniMax's voice_setting.speed: 1 normal, 0.5 half speed, 2 twice as
	// fast). Resolved like the other params — clamped to the model's declared
	// bounds with a notice, dropped-with-notice on models without the control.
	// Zero means unset (any real multiplier is positive).
	Speed float64 `json:"speed,omitempty"`
	// SourceAudio is a reference clip (data URL) for zero-shot voice cloning:
	// generate_speech fills it from the user's attached audio so the speech is
	// synthesized in that voice. Resolved like the other params — mapped onto
	// the model's audio_url-shaped input, dropped-with-notice when the model
	// has no reference input.
	SourceAudio string `json:"sourceAudio,omitempty"`
}

// VoiceDesignRequest is the input to the generate_voice tool: Description is
// the written voice profile ("a sweet, natural voice of an 8-year-old …") and
// PreviewText the line the preview clip speaks — the two required inputs of
// MiniMax's voice-design endpoint (≤2000 and ≤500 characters). An empty
// PreviewText lets the executor supply a default preview line, since the
// endpoint requires one and a voice ask need not carry words to say.
type VoiceDesignRequest struct {
	Model       string `json:"model"`
	Description string `json:"description"`
	PreviewText string `json:"previewText,omitempty"`
}

// AudioExtendRequest is the input to extend_audio: lengthen an existing clip
// by generated audio. Duration is the length of the ADDED audio in seconds
// (a string canonical, like video extend), not the total output length.
// Direction is "after" (default, append) or "before" (prepend). Style and
// Lyrics steer music models (ace-step tags, sonauto tags/lyrics_prompt).
// SourceAudio is the clip to extend (data URL), and SourceDurationSeconds is
// its probed length — required by mask-based endpoints (stable-audio inpaint)
// and computed before the clip is uploaded, when the bytes are still at hand.
type AudioExtendRequest struct {
	Model                 string  `json:"model"`
	Prompt                string  `json:"prompt"`
	Duration              string  `json:"duration,omitempty"`
	Direction             string  `json:"direction,omitempty"`
	NegativePrompt        string  `json:"negativePrompt,omitempty"`
	Style                 string  `json:"style,omitempty"`
	Lyrics                string  `json:"lyrics,omitempty"`
	SourceAudio           string  `json:"sourceAudio,omitempty"`
	SourceDurationSeconds float64 `json:"sourceDurationSeconds,omitempty"`
}

// SaveAudioRequest asks to copy a generated audio artifact to a user-chosen
// location, mirroring SaveVideoRequest.
type SaveAudioRequest struct {
	Path          string `json:"path"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

// SaveTranscriptRequest is the input to SaveTranscript — the transcript
// artifact's path or "/atelier-artifact" URL plus a suggested file name.
type SaveTranscriptRequest struct {
	Path          string `json:"path"`
	SuggestedName string `json:"suggestedName,omitempty"`
}

type ollamaGenerateResponse struct {
	Model    string   `json:"model"`
	Response string   `json:"response"`
	Image    string   `json:"image"`
	Images   []string `json:"images"`
	Done     bool     `json:"done"`
	Error    string   `json:"error"`
	// CostMicros estimates the generation's USD-millionth cost, stamped by the
	// tool gateway on the fal path only (fal_pricing.go); Ollama and the
	// openai-compatible path never set it.
	CostMicros int64 `json:"costMicros,omitempty"`
	// Diagnostics carries the Replicate resolver's machine-facing mapping
	// decisions (the reference rebind), stamped by the gateway beside Notices
	// on the paths that resolve against a schema. Never rendered in the chat
	// reply; rides ToolImageResult.Diagnostics into tool evidence and the
	// persisted run.
	Diagnostics []string `json:"diagnostics,omitempty"`
}

type ConversationSummary struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	DeletedAt     string `json:"deletedAt,omitempty"`
	TurnCount     int    `json:"turnCount"`
	ArtifactCount int    `json:"artifactCount"`
	// Workspace is the immutable filesystem-tool root this conversation runs
	// against. Empty for legacy conversations created before per-conversation
	// workspaces; the UI falls back to the configured default root.
	Workspace string `json:"workspace,omitempty"`
	// ProjectID is the library project this conversation belongs to; empty
	// means standalone. Pinned at creation from ChatRequest.ProjectID and
	// immutable afterwards (MoveConversationToProject is the one sanctioned
	// rewrite). No schema bump: absent and empty both read as standalone.
	ProjectID string `json:"projectId,omitempty"`
	// ModelOverrides mirrors the conversation record's model-selection
	// override (nil when unset) so the UI can badge and hydrate overridden
	// conversations from the list without loading the full record.
	ModelOverrides *ConversationModelOverrides `json:"modelOverrides,omitempty"`
}

type ConversationDetail struct {
	Conversation HistoryConversation `json:"conversation"`
	Turns        []HistoryTurn       `json:"turns"`
}

type PurgeArchivedResult struct {
	DeletedConversations int `json:"deletedConversations"`
	DeletedAssets        int `json:"deletedAssets"`
}

type HistoryConversation struct {
	SchemaVersion int                      `json:"schemaVersion"`
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	Title         string                   `json:"title"`
	CreatedAt     string                   `json:"createdAt"`
	UpdatedAt     string                   `json:"updatedAt"`
	DeletedAt     string                   `json:"deletedAt,omitempty"`
	Provider      HistoryProvider          `json:"provider"`
	Defaults      HistoryDefaults          `json:"defaults"`
	Stats         HistoryConversationStats `json:"stats"`
	// Workspace is the immutable filesystem-tool root this conversation runs
	// against. Set at creation and never changed. SchemaVersion 2+ guarantees
	// this is populated; SchemaVersion 1 records are backfilled on resume.
	Workspace string `json:"workspace,omitempty"`
	// ProjectID is the library project this conversation belongs to; empty
	// means standalone. Pinned at creation from ChatRequest.ProjectID and
	// immutable afterwards (MoveConversationToProject is the one sanctioned
	// rewrite). No schema bump: absent and empty both read as standalone.
	ProjectID string `json:"projectId,omitempty"`
	// ModelOverrides is the per-conversation model-selection override (see
	// ConversationModelOverrides): empty fields inherit the live global
	// Settings, so the struct is stored nil unless at least one field is set.
	// Mutable over the conversation's life via SetConversationModelOverrides,
	// the one sanctioned rewrite. Applied per turn by rewriting the per-stream
	// config copy before the HarnessEngine is built (the resolveTurnWorkspace
	// pattern). No schema bump: absent and nil both read as "follow global
	// config".
	ModelOverrides *ConversationModelOverrides `json:"modelOverrides,omitempty"`
	// EditSession pins an edit-session child conversation (Kind "edit") to
	// the image it edits — see EditSessionMeta (edit_session.go). Nil on chat
	// conversations; no schema bump (absent reads as "not an edit session").
	EditSession *EditSessionMeta `json:"editSession,omitempty"`
}

type HistoryProvider struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseURL"`
}

type HistoryDefaults struct {
	ChatModel  string `json:"chatModel,omitempty"`
	ImageModel string `json:"imageModel,omitempty"`
	System     string `json:"system,omitempty"`
}

type HistoryConversationStats struct {
	TurnCount     int `json:"turnCount"`
	ArtifactCount int `json:"artifactCount"`
}

type HistoryTurn struct {
	SchemaVersion    int              `json:"schemaVersion"`
	ID               string           `json:"id"`
	ConversationID   string           `json:"conversationId"`
	CreatedAt        string           `json:"createdAt"`
	Kind             string           `json:"kind"`
	Role             string           `json:"role"`
	Model            string           `json:"model,omitempty"`
	Provider         string           `json:"provider,omitempty"`
	Content          []HistoryContent `json:"content"`
	Request          map[string]any   `json:"request,omitempty"`
	ProviderResponse map[string]any   `json:"providerResponse,omitempty"`
	DeletedAt        string           `json:"deletedAt,omitempty"`
}

type HistoryContent struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
	Path       string `json:"path,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

type HarnessRun struct {
	ID             string                 `json:"id"`
	Mode           string                 `json:"mode"`
	Status         string                 `json:"status"`
	StartedAt      string                 `json:"startedAt"`
	CompletedAt    string                 `json:"completedAt,omitempty"`
	DurationMS     int64                  `json:"durationMs,omitempty"`
	RequestID      string                 `json:"requestId,omitempty"`
	ConversationID string                 `json:"conversationId,omitempty"`
	Loop           HarnessLoop            `json:"loop"`
	Skill          *HarnessSkillDecision  `json:"skill,omitempty"`
	Triage         *HarnessTriageDecision `json:"triage,omitempty"`
	Steps          []HarnessStep          `json:"steps"`
	// onUpdate, when set, fires after every step transition (appendStep,
	// completeStep, complete) so the UI can render the run live via the
	// atelier:harness-run event. Unexported and never serialized; tests build
	// runs without it.
	onUpdate func()
}

type HarnessLoop struct {
	MaxSteps      int    `json:"maxSteps"`
	MaxWallTimeMS int64  `json:"maxWallTimeMs"`
	Iterations    int    `json:"iterations"`
	StopReason    string `json:"stopReason,omitempty"`
}

type HarnessStep struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Iteration   int    `json:"iteration,omitempty"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Status      string `json:"status"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt,omitempty"`
	DurationMS  int64  `json:"durationMs,omitempty"`
	Decision    string `json:"decision,omitempty"`
	DoneReason  string `json:"doneReason,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	// Tokens is the completion-side count for model-call steps (triage, skill,
	// planning, streaming). Bookkeeping steps (saved/evaluation) carry none —
	// the turn total lives in ProviderResponse.tokens — so summing tokens over
	// steps counts each model call exactly once.
	Tokens       int `json:"tokens,omitempty"`
	PromptTokens int `json:"promptTokens,omitempty"`
	// CostMicros is the server-billed cost of one model call in USD millionths
	// (OpenRouter usage.cost), riding the same steps as the token counts —
	// fal media generation's cost rides the tool_call step's activities
	// instead. Zero when the provider reports no cost (Ollama, local servers).
	CostMicros int64 `json:"costMicros,omitempty"`
	// FirstTokenMS is time-to-first-token for streaming steps: milliseconds
	// between the provider stream opening and the first visible delta.
	FirstTokenMS int64                   `json:"firstTokenMs,omitempty"`
	Request      *HarnessRequestSnapshot `json:"request,omitempty"`
	Tools        []HarnessToolActivity   `json:"tools,omitempty"`
}

// HarnessRequestSnapshot is a compact fingerprint of one model request,
// recorded on every model-call step so post-hoc debugging can see what was
// sent without persisting raw prompts. PromptHash covers the prefix-cache-
// sensitive prefix (system prompt + message #0): consecutive same-model steps
// sharing a PromptHash kept the Ollama prefix cache warm; a change means the
// cache was invalidated. Hashes are sha256 truncated to 12 hex chars.
type HarnessRequestSnapshot struct {
	PromptHash        string `json:"promptHash,omitempty"`
	ToolsHash         string `json:"toolsHash,omitempty"`
	PromptChars       int    `json:"promptChars,omitempty"`
	ToolMode          string `json:"toolMode,omitempty"` // native|format|"" (final responses carry none)
	NumCtx            int    `json:"numCtx,omitempty"`
	TruncatedMessages int    `json:"truncatedMessages,omitempty"`
}

type HarnessToolActivity struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Call is the planner's emitted inputs for this tool call (the plan's
	// per-call params: content, model, path, command, duration, negativePrompt,
	// etc.). Recorded on every tool_call step so the emitted params are
	// inspectable post-hoc — the result struct carries only what the tool
	// produced, not what was requested. Zipped onto the activity at the
	// recording site (harness.go toolActivities), never via HarnessToolResult,
	// so planner params cannot leak into role:"tool" evidence.
	Call          HarnessToolCall `json:"call,omitempty"`
	Path          string          `json:"path,omitempty"`
	Command       []string        `json:"command,omitempty"`
	ExitCode      int             `json:"exitCode,omitempty"`
	StdoutPreview string          `json:"stdoutPreview,omitempty"`
	StderrPreview string          `json:"stderrPreview,omitempty"`
	DurationMS    int64           `json:"durationMs,omitempty"`
	Error         string          `json:"error,omitempty"`
	// Model, MediaKind, and MediaCount record the generation model a media tool
	// actually ran (after defaulting — not the planner's requested Call.Model),
	// what it produced ("video"/"audio"/"image"), and how many. Media models
	// don't consume tokens, so these fields — not token counts — are what makes
	// their consumption visible in telemetry. Set from the tool's own result
	// payload in defaultHarnessToolActivity's type switch; empty for non-media
	// tools and failed calls (no payload).
	// Provider names the backend that rendered the media ("fal", "ollama",
	// "openai-compatible") — attribution for Model, since the same image model
	// family can run locally or in the cloud and the id alone doesn't say which.
	// Filled at the engine layer (toolActivityFromResult) from the same config
	// the tool gateway routes by.
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	MediaKind  string `json:"mediaKind,omitempty"`
	MediaCount int    `json:"mediaCount,omitempty"`
	// CostMicros estimates what this media generation cost in USD millionths,
	// computed at call time from fal's pricing API (unit price × billed units
	// — images, seconds of video, characters, or one request). An estimate,
	// not a bill: feature multipliers fal applies server-side may not be
	// captured. Zero when the backend is local (Ollama, openai-compatible) or
	// pricing was unavailable; failed calls carry none, matching MediaCount.
	CostMicros int64 `json:"costMicros,omitempty"`
	// CostUnknown marks a media generation whose cost could not be determined
	// — the Replicate backend reports no price (no pricing API, no cost in
	// the prediction response), so its rows render "?" in the money column
	// instead of the empty slot a genuinely free local call renders. Absent
	// (false) everywhere else, including turns persisted before the field
	// existed. The turn total still sums only known costs.
	CostUnknown bool `json:"costUnknown,omitempty"`
	// Permission records how the permission gate resolved (approved/denied/
	// timeout/cancelled) and how long it waited, when the call was gated.
	// Zipped from HarnessToolResult's json:"-" side-channel at the same
	// recording site as Call.
	Permission       string `json:"permission,omitempty"`
	PermissionWaitMS int64  `json:"permissionWaitMs,omitempty"`
}

// EmptyStatePrompt is a heading/subtext pair shown on the empty chat screen.
type EmptyStatePrompt struct {
	Heading string `json:"heading"`
	Sub     string `json:"sub"`
}

// emptyStatePrompts is the pool of greetings for the empty chat screen; one is
// picked at random each time the transcript is empty.
var emptyStatePrompts = []EmptyStatePrompt{
	{"What are we making today?", "Describe an image, video, or sound to begin."},
	{"Start something.", "Pick a model, or just describe what you want to create."},
	{"The studio's open.", "Type a prompt to generate images, video, audio, and more."},
	{"Describe what you want to create.", "Works across image, video, audio, and motion models."},
	{"What do you want to generate?", "Enter a prompt and choose a model to get started."},
	{"Type a prompt to begin.", "Or paste a reference to guide the generation."},
	{"Begin with a blank canvas.", "Describe your idea and pick a model."},
	{"Imagine it here.", "Turn a prompt into images, video, or sound."},
	{"A blank page, endless models.", "Start typing to bring an idea to life."},
	{"Conjure something.", "Describe it — we'll find the right model."},
	{"Feed the machines.", "A prompt is all it takes."},
}

// RandomEmptyStatePrompt returns a random greeting for the empty chat screen.
func (a *App) RandomEmptyStatePrompt() EmptyStatePrompt {
	return emptyStatePrompts[mrand.IntN(len(emptyStatePrompts))]
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

func (a *App) ListConversations() ([]ConversationSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listConversations(config.Storage)
}

func (a *App) GetConversation(conversationID string) (ConversationDetail, error) {
	config, err := loadAppConfig()
	if err != nil {
		return ConversationDetail{}, err
	}
	return getConversation(config.Storage, conversationID)
}

// ListConversationAssets derives the conversation's referable media assets —
// every image/audio/video artifact in its history, oldest first — for the
// assets panel and the @-mention composer.
func (a *App) ListConversationAssets(conversationID string) ([]ConversationAsset, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	return listConversationAssets(config.Storage, conversationID)
}

// SearchConversations greps stored conversation history — turn text and
// titles — beyond what the sidebar list shows, returning per-conversation
// matches with context snippets.
func (a *App) SearchConversations(query string, options SearchOptions) (SearchResponse, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return SearchResponse{}, err
	}
	return searchHistory(config.Storage, query, options)
}

func (a *App) DeleteConversation(conversationID string) error {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if a.editOpRunning(conversationID) {
		return errors.New("an edit is still running in this conversation; finish or cancel it before deleting")
	}
	config, err := loadAppConfig()
	if err != nil {
		return err
	}
	return deleteConversation(config.Storage, conversationID)
}

func (a *App) PurgeArchivedConversations() (PurgeArchivedResult, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if a.anyEditOperationActive() {
		return PurgeArchivedResult{}, errors.New("an edit is still running; finish or cancel it before purging archived conversations")
	}
	config, err := loadAppConfig()
	if err != nil {
		return PurgeArchivedResult{}, err
	}
	return purgeArchivedConversations(config.Storage)
}

func (a *App) UpdateConversationTitle(conversationID string, title string) (ConversationSummary, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	if a.editOpRunning(conversationID) {
		return ConversationSummary{}, errors.New("an edit is still running in this conversation; finish or cancel it before renaming")
	}
	config, err := loadAppConfig()
	if err != nil {
		return ConversationSummary{}, err
	}
	return updateConversationTitle(config.Storage, conversationID, title)
}

// CreateLibrary creates a top-level library container (see libraries.go).
func (a *App) CreateLibrary(name string) (LibrarySummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibrarySummary{}, err
	}
	return createLibrary(config.Storage, name)
}

// ListLibraries returns every library with its projects, most-recently-active
// first, for the sidebar's Libraries tree. Activity is derived from the member
// conversations (see newestConversationActivity), not stored on the records.
func (a *App) ListLibraries() ([]LibrarySummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listLibraries(config.Storage)
}

func (a *App) RenameLibrary(libraryID string, name string) (LibrarySummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibrarySummary{}, err
	}
	return renameLibrary(config.Storage, libraryID, name)
}

// DeleteLibrary hard-deletes a library, its projects, and every conversation
// in them — Final Cut's trash, no recovery. Refused while any member
// conversation is streaming: an in-flight append would write turns into a
// directory this call just removed.
func (a *App) DeleteLibrary(libraryID string) (DeleteLibraryResult, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	config, err := loadReadyConfig()
	if err != nil {
		return DeleteLibraryResult{}, err
	}
	projectIDs, err := libraryProjectIDs(config.Storage, libraryID)
	if err != nil {
		return DeleteLibraryResult{}, err
	}
	if a.conversationsStreamingInProjects(config.Storage, projectIDs) {
		return DeleteLibraryResult{}, errors.New("a conversation in this library is still running; wait for it to finish before deleting")
	}
	return deleteLibrary(config.Storage, libraryID)
}

func (a *App) CreateProject(libraryID string, name string) (ProjectSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ProjectSummary{}, err
	}
	return createProject(config.Storage, libraryID, name)
}

func (a *App) RenameProject(projectID string, name string) (ProjectSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ProjectSummary{}, err
	}
	return renameProject(config.Storage, projectID, name)
}

// SetProjectNotes replaces a project's instruction block — standing guidance
// the harness injects into its planner prompt for every conversation in the
// project (resolveTurnProjectNotes). An empty string clears it.
func (a *App) SetProjectNotes(projectID string, notes string) (ProjectSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ProjectSummary{}, err
	}
	return setProjectNotes(config.Storage, projectID, notes)
}

// DeleteProject hard-deletes a project and every conversation in it, with the
// same streaming refusal as DeleteLibrary.
func (a *App) DeleteProject(projectID string) (DeleteProjectResult, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	config, err := loadReadyConfig()
	if err != nil {
		return DeleteProjectResult{}, err
	}
	if a.conversationsStreamingInProjects(config.Storage, map[string]bool{projectID: true}) {
		return DeleteProjectResult{}, errors.New("a conversation in this project is still running; wait for it to finish before deleting")
	}
	return deleteProject(config.Storage, projectID)
}

// ListProjectConversations returns the project's conversations, newest-first,
// for the sidebar's project rows. Not recency-capped — these conversations are
// filtered out of the standalone list, so this is their only listing.
func (a *App) ListProjectConversations(projectID string) ([]ConversationSummary, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listProjectConversations(config.Storage, projectID)
}

// ListLibraryAssets folds every conversation in the library's projects into
// one asset list — uploads and generated media alike — for the library assets
// panel and the @-mention picker.
func (a *App) ListLibraryAssets(libraryID string) ([]ConversationAsset, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	return listLibraryAssets(config.Storage, libraryID)
}

// MoveConversationToProject reassigns a conversation to a project, or detaches
// it to standalone when projectID is empty. Refused while the conversation is
// streaming — the append path rewrites conversation.json from its own load,
// and a concurrent move would clobber one of the two writes.
func (a *App) MoveConversationToProject(conversationID string, projectID string) (ConversationSummary, error) {
	a.editSubmitMu.Lock()
	defer a.editSubmitMu.Unlock()
	config, err := loadReadyConfig()
	if err != nil {
		return ConversationSummary{}, err
	}
	if a.conversationStreaming(conversationID) || a.editOpRunning(conversationID) {
		return ConversationSummary{}, errors.New("this conversation is still running; wait for it to finish before moving it")
	}
	return moveConversationToProject(config.Storage, conversationID, projectID)
}

// InspectLibraryExport is the export pre-flight: what an archive of this
// library would contain, how big it is, and which referenced assets are
// missing on disk. No dialog, no writes — the confirm UI runs it first, and
// missing assets never block (the manifest records them).
func (a *App) InspectLibraryExport(libraryID string) (LibraryExportPlan, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibraryExportPlan{}, err
	}
	return inspectLibraryExport(config.Storage, libraryID)
}

// ExportLibrary archives a library — projects, member conversations (soft-
// deleted included), and all artifacts — into a user-chosen .atelierlib file.
// Refused while any member conversation is streaming: the archive must
// snapshot settled turns, not a directory an in-flight append is still
// writing.
func (a *App) ExportLibrary(libraryID string) (LibraryExportResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibraryExportResult{}, err
	}
	projectIDs, err := libraryProjectIDs(config.Storage, libraryID)
	if err != nil {
		return LibraryExportResult{}, err
	}
	if a.conversationsStreamingInProjects(config.Storage, projectIDs) {
		return LibraryExportResult{}, errors.New("a conversation in this library is still running; wait for it to finish before exporting")
	}
	plan, err := inspectLibraryExport(config.Storage, libraryID)
	if err != nil {
		return LibraryExportResult{}, err
	}
	filename := sanitizeFilename(plan.LibraryName)
	if filename == "" {
		filename = libraryID
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export Library",
		DefaultFilename: filename + libraryArchiveExtension,
		Filters:         []runtime.FileFilter{{DisplayName: "Atelier Library Archive (*.atelierlib)", Pattern: "*.atelierlib"}},
	})
	if err != nil {
		return LibraryExportResult{}, err
	}
	if path == "" {
		return LibraryExportResult{}, nil
	}
	if !strings.HasSuffix(strings.ToLower(path), libraryArchiveExtension) {
		path += libraryArchiveExtension
	}
	return exportLibrary(config.Storage, libraryID, path, func(phase string, done, total int) {
		a.emitLibraryArchiveEvent(LibraryArchiveEvent{Op: "export", LibraryID: libraryID, Phase: phase, Done: done, Total: total})
	})
}

// ImportLibrary restores a .atelierlib archive as a new library in storage.
// The archive is verified whole (per-file SHA-256 against its manifest) before
// anything is written, and an archive whose library or conversations already
// exist is refused — import never merges. Model names in the archived history
// are provenance, not capabilities: nothing checks model availability.
func (a *App) ImportLibrary() (LibraryImportResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return LibraryImportResult{}, err
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import Library",
		Filters: []runtime.FileFilter{
			{DisplayName: "Atelier Library Archive (*.atelierlib)", Pattern: "*.atelierlib"},
			{DisplayName: "Zip Archive (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil {
		return LibraryImportResult{}, err
	}
	if path == "" {
		return LibraryImportResult{}, nil
	}
	return importLibraryArchive(config.Storage, config.Tools.Filesystem.Root, path, func(phase string, done, total int) {
		a.emitLibraryArchiveEvent(LibraryArchiveEvent{Op: "import", Phase: phase, Done: done, Total: total})
	})
}

// conversationsStreamingInProjects reports whether any conversation belonging
// to one of projectIDs has an active stream or edit. On a walk error it checks
// all active work — deletion is destructive, so uncertainty blocks it.
func (a *App) conversationsStreamingInProjects(storage ConfigStorage, projectIDs map[string]bool) bool {
	ids, err := projectConversationIDs(storage, projectIDs)
	if err != nil {
		return a.anyStreamActive() || a.anyEditOperationActive()
	}
	if len(ids) == 0 {
		return false
	}
	a.streamsMu.Lock()
	active := make(map[string]bool, len(a.streamConversations))
	for _, conversationID := range a.streamConversations {
		active[conversationID] = true
	}
	a.streamsMu.Unlock()
	a.editOpsMu.Lock()
	for _, operation := range a.editOps {
		active[operation.conversationID] = true
	}
	a.editOpsMu.Unlock()
	for _, id := range ids {
		if active[id] {
			return true
		}
	}
	return false
}

func (a *App) anyEditOperationActive() bool {
	a.editOpsMu.Lock()
	defer a.editOpsMu.Unlock()
	return len(a.editOps) > 0
}

// Stop local child processes before Wails exits. If persistence cannot finish
// before shutdown, the existing startup sweep marks the operation interrupted.
func (a *App) shutdown(_ context.Context) {
	a.videoPreviewMu.Lock()
	for _, job := range a.videoPreviewJobs {
		job.cancel()
	}
	a.videoPreviewMu.Unlock()
	a.editOpsMu.Lock()
	for _, operation := range a.editOps {
		operation.cancel()
	}
	a.editOpsMu.Unlock()
	a.compositionRendersMu.Lock()
	for _, render := range a.compositionRenders {
		render.cancel()
	}
	a.compositionRendersMu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for a.anyEditOperationActive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// conversationStreaming reports whether this one conversation has an active
// stream.
func (a *App) conversationStreaming(conversationID string) bool {
	a.streamsMu.Lock()
	defer a.streamsMu.Unlock()
	for _, id := range a.streamConversations {
		if id == conversationID {
			return true
		}
	}
	return false
}

func (a *App) SetOllamaBaseURL(baseURL string) error {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}
	a.baseURL = normalized
	return nil
}

func (a *App) CheckOllama(baseURL string) OllamaStatus {
	return a.ollamaClient(baseURL).Check(context.Background())
}

// CheckForUpdates fetches the release manifest and compares it with the
// running version. Manual by design — the automatic startup/ticker path runs
// the same core behind the 24h throttle — and raises the banner event when an
// update is found so the Settings status line and the banner agree.
func (a *App) CheckForUpdates() UpdateStatus {
	return a.checkForUpdates(a.ctx, true)
}

// menuCheckForUpdates is the app-menu entry point; the emitted event raises
// the same banner the automatic check does.
func (a *App) menuCheckForUpdates() {
	_ = a.checkForUpdates(a.ctx, true)
}

// The File-menu creation actions are pure frontend navigation: the menu click
// lands in Go (Wails menu handlers), so each emits a dedicated event and the
// UI drives its inline creation flow from it — no Go-side dialogs, no new
// bindings. menuNewConversation is context-aware on the frontend side: a
// pending in-project chat when the user is working inside a project (FCP's
// ⌘N-acts-on-the-selected-container), else a standalone chat.
func (a *App) menuNewConversation() { a.emitMenuAction("atelier:menu-new-conversation") }

// menuNewLibrary routes File → New Library… to the sidebar's inline input.
func (a *App) menuNewLibrary() { a.emitMenuAction("atelier:menu-new-library") }

// menuNewProject routes File → New Project… to the inline input under the
// currently expanded (or first) library; the frontend picks the target.
func (a *App) menuNewProject() { a.emitMenuAction("atelier:menu-new-project") }

func (a *App) emitMenuAction(event string) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, event)
	}
}

// InstallUpdate installs the update found by the last check: download,
// verify, stage the swap, hand off to the detached helper, then quit so the
// bundle can be replaced and relaunched. Refuses while a turn is streaming —
// the frontend queues and retries when idle; this gate stays authoritative —
// and refuses cleanly in development, where there is no bundle to swap.
func (a *App) InstallUpdate() error {
	if a.anyStreamActive() {
		return errors.New("a conversation is still running; wait for it to finish before updating")
	}
	manifest := a.cachedUpdateManifest()
	if manifest == nil {
		return errors.New("no update is available yet; check for updates first")
	}
	if err := applySelfUpdate(a.updateClient, manifest); err != nil {
		return err
	}
	// The helper is live and waiting on this PID: quit now, dialog-free.
	a.updaterQuitting.Store(true)
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
	return nil
}

// anyStreamActive reports whether any chat turn is in flight. StreamChat
// registers the cancel func synchronously and the goroutine removes it only
// on completion, so a non-empty map is the reliable "turn running" signal.
func (a *App) anyStreamActive() bool {
	a.streamsMu.Lock()
	defer a.streamsMu.Unlock()
	return len(a.streams) > 0
}

func (a *App) ListModels(baseURL string) ([]OllamaModel, error) {
	return a.ollamaClient(baseURL).ListModels(context.Background())
}

func (a *App) ChooseToolWorkspace(current string) (string, error) {
	current = normalizeStoragePath(current)
	if current == "" {
		current = defaultAppConfig().Tools.Filesystem.Root
	}
	if err := os.MkdirAll(current, 0755); err != nil {
		return "", err
	}
	path, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "Choose Atelier workspace",
		DefaultDirectory:     current,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	return normalizeStoragePath(path), nil
}

func (a *App) ResolveToolPermission(permissionID string, approved bool) error {
	permissionID = strings.TrimSpace(permissionID)
	if permissionID == "" {
		return errors.New("permission id is required")
	}
	a.permissionsMu.Lock()
	response, ok := a.permissions[permissionID]
	if ok {
		delete(a.permissions, permissionID)
	}
	a.permissionsMu.Unlock()
	if !ok {
		return fmt.Errorf("permission request %q is not pending", permissionID)
	}
	select {
	case response <- approved:
	default:
	}
	return nil
}

func (a *App) RunToolCommand(req ToolCommandRequest) (ToolCommandResult, error) {
	result, err := a.ExecuteTool(ToolExecutionRequest{
		Name: "run_command",
		Call: HarnessToolCall{
			Name:      "run_command",
			Command:   req.Command,
			Args:      req.Args,
			Cwd:       req.Cwd,
			Env:       req.Env,
			TimeoutMS: req.TimeoutMS,
		},
		Source: "api",
	})
	if err != nil {
		return ToolCommandResult{}, err
	}
	if result.Status == "denied" {
		return ToolCommandResult{}, errors.New(result.Error)
	}
	output, ok := result.Result.(ToolCommandResult)
	if !ok {
		return ToolCommandResult{}, fmt.Errorf("run_command returned %T", result.Result)
	}
	if result.Status == "failed" {
		return output, errors.New(result.Error)
	}
	return output, nil
}

func (a *App) ListToolFiles(req ToolFileListRequest) (ToolFileListResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ToolFileListResult{}, err
	}
	return newFilesystemToolLayer(config.Tools.Filesystem).ListFiles(req)
}

func (a *App) ReadToolFile(req ToolFileReadRequest) (ToolFileReadResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return ToolFileReadResult{}, err
	}
	return newFilesystemToolLayer(config.Tools.Filesystem).ReadFile(req)
}

func (a *App) WriteToolFile(req ToolFileWriteRequest) (ToolFileWriteResult, error) {
	result, err := a.ExecuteTool(ToolExecutionRequest{
		Name: "write_file",
		Call: HarnessToolCall{
			Name:      "write_file",
			Path:      req.Path,
			Content:   req.Content,
			Append:    req.Append,
			Overwrite: req.Overwrite,
		},
		Source: "api",
	})
	if err != nil {
		return ToolFileWriteResult{}, err
	}
	if result.Status == "denied" {
		return ToolFileWriteResult{}, errors.New(result.Error)
	}
	output, ok := result.Result.(ToolFileWriteResult)
	if !ok {
		return ToolFileWriteResult{}, fmt.Errorf("write_file returned %T", result.Result)
	}
	if result.Status == "failed" {
		return output, errors.New(result.Error)
	}
	return output, nil
}

func (a *App) ExecuteTool(req ToolExecutionRequest) (HarnessToolResult, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return HarnessToolResult{}, err
	}
	result := newToolGateway(a, config).Execute(context.Background(), req)
	return result, nil
}

func (a *App) StreamChat(req ChatRequest) (*ChatStreamStart, error) {
	if len(req.Messages) == 0 {
		return nil, errors.New("at least one message is required")
	}

	config, err := loadReadyConfig()
	if err != nil {
		return nil, err
	}
	// Fail an unknown project before anything is persisted (see
	// resolveTurnProject) — same fail-early posture as the provider check.
	if _, err := resolveTurnProject(config, req); err != nil {
		return nil, err
	}
	// Resolve the conversation's immutable workspace and make it the engine's
	// filesystem-tool root before construction. The registry (cached per
	// engine), the filesystem layer, and the harness/triage prompts all read it
	// through h.config, so this single override scopes the whole turn. See
	// resolveTurnWorkspace for the turn-1-vs-turn-2+ rules.
	workspaceRoot, err := resolveTurnWorkspace(config, req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		config.Tools.Filesystem.Root = workspaceRoot
	}
	// Apply the conversation's model-selection overrides the same way — one
	// rewrite of the per-stream config copy before engine construction scopes
	// harness routing, tool-registry gating, and every tool's default model
	// through h.config (see applyConversationModelOverrides). A primary
	// override also rewrites the request's model fields: the record wins,
	// like the workspace.
	config, req, err = applyConversationModelOverrides(config, req)
	if err != nil {
		return nil, err
	}
	// Resolve the project's instruction block onto the request — the same
	// seam as the workspace/model overrides, but read live every turn (see
	// resolveTurnProjectNotes). From here req.ProjectNotes reaches the triage
	// and planner prompts unchanged, so no further plumbing is needed.
	req.ProjectNotes = resolveTurnProjectNotes(config, req)
	engine := newHarnessEngine(config, a)
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(config.Providers.Ollama.Models.Primary)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("model is required")
	}

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = fmt.Sprintf("chat-%d", time.Now().UnixNano())
	}

	conversationID, userVideoURLs, userSubtitleURLs, userMarkdownURLs, err := engine.StartChatTurn(req)
	if err != nil {
		return nil, err
	}
	req.ConversationID = conversationID

	streamCtx, cancel := context.WithCancel(context.Background())
	a.streamsMu.Lock()
	a.streams[requestID] = cancel
	a.streamConversations[requestID] = conversationID
	a.streamsMu.Unlock()

	go func() {
		defer func() {
			a.streamsMu.Lock()
			delete(a.streams, requestID)
			delete(a.streamConversations, requestID)
			a.streamsMu.Unlock()
		}()
		engine.RunChatStream(streamCtx, requestID, req, true)
	}()

	return &ChatStreamStart{RequestID: requestID, ConversationID: conversationID, UserVideos: userVideoURLs, UserSubtitles: userSubtitleURLs, UserMarkdowns: userMarkdownURLs}, nil
}

func (a *App) CancelStream(requestID string) {
	a.streamsMu.Lock()
	cancel := a.streams[requestID]
	a.streamsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) SaveImage(req SaveImageRequest) (string, error) {
	data, extension, err := decodeImagePayload(req.Image)
	if err != nil {
		return "", err
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-image", extension, "Save generated image", []runtime.FileFilter{
		{DisplayName: "Image Files", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif;*.heic;*.avif;*.tiff;*.bmp;*.jp2"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveVideo copies a generated video artifact to a user-chosen location. The
// frontend passes the path the asset handler served the video from (either a
// bare filesystem path or the "/atelier-artifact"-prefixed URL); this strips the
// prefix, confirms the bytes are a playable video, and streams a Save dialog
// copy. Returns the chosen path, or "" if the dialog was cancelled.
func (a *App) SaveVideo(req SaveVideoRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("video path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if !isVideoBytes(data) {
		return "", errors.New("artifact is not a supported video")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension == "" {
		extension = ".mp4"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-video", extension, "Save generated video", []runtime.FileFilter{
		{DisplayName: "Video Files", Pattern: "*.mp4;*.webm;*.mov;*.m4v"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveAudio copies a generated audio artifact to a user-chosen location,
// mirroring SaveVideo.
func (a *App) SaveAudio(req SaveAudioRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("audio path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if !isAudioBytes(data) {
		return "", errors.New("artifact is not a supported audio clip")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension == "" {
		extension = ".mp3"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-audio", extension, "Save generated audio", []runtime.FileFilter{
		{DisplayName: "Audio Files", Pattern: "*.mp3;*.wav;*.ogg;*.flac;*.m4a;*.aac;*.opus"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// SaveTranscript copies a transcript artifact (.vtt timestamped or .txt plain)
// to a user-chosen location, mirroring SaveVideo/SaveAudio. The guard is a
// non-empty-text check rather than the media savers' byte sniffing — there is
// no container magic to verify.
func (a *App) SaveTranscript(req SaveTranscriptRequest) (string, error) {
	sourcePath := strings.TrimSpace(req.Path)
	sourcePath = strings.TrimPrefix(sourcePath, artifactPrefix)
	if sourcePath == "" {
		return "", errors.New("transcript path is empty")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", errors.New("transcript artifact is empty")
	}

	extension := strings.ToLower(filepath.Ext(sourcePath))
	if extension != ".vtt" && extension != ".txt" && extension != ".srt" {
		extension = ".txt"
	}
	return a.saveArtifactDialog(data, req.SuggestedName, "atelier-transcript", extension, "Save transcript", []runtime.FileFilter{
		{DisplayName: "Transcript & Subtitle Files", Pattern: "*.vtt;*.txt;*.srt"},
		{DisplayName: "All Files", Pattern: "*.*"},
	})
}

// saveArtifactDialog is the shared body for SaveImage/SaveVideo/SaveAudio: it
// builds a filename (defaultName + extension when the user gave none, ensuring
// the extension is present), opens a Save dialog with the given title/filters,
// and writes data to the chosen path. Each caller keeps its own source-loading
// and byte-sniffing; this owns the filename-guard → dialog → write ritual they
// all shared. Returns the chosen path, or ("", nil) if the dialog was cancelled.
func (a *App) saveArtifactDialog(data []byte, suggestedName, defaultName, extension, title string, filters []runtime.FileFilter) (string, error) {
	filename := sanitizeFilename(suggestedName)
	if filename == "" {
		filename = defaultName + extension
	}
	if !strings.HasSuffix(strings.ToLower(filename), extension) {
		filename += extension
	}

	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: filename,
		Filters:         filters,
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func decodeImagePayload(image string) ([]byte, string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, "", errors.New("image data is empty")
	}

	// A conversation loaded from history renders its images as "/atelier-artifact"
	// URLs served from disk rather than inline data URLs (hydrateHistoryContent),
	// so resolve those back to bytes on disk — mirroring SaveVideo/SaveAudio.
	if sourcePath := strings.TrimPrefix(image, artifactPrefix); sourcePath != image {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, "", err
		}
		if !isImageBytes(data) {
			return nil, "", errors.New("artifact is not a supported image")
		}
		extension := strings.ToLower(filepath.Ext(sourcePath))
		if extension == "" {
			extension = ".png"
		}
		return data, extension, nil
	}

	extension := ".png"
	if strings.HasPrefix(image, "data:image/") {
		headerEnd := strings.Index(image, ",")
		if headerEnd < 0 {
			return nil, "", errors.New("image data URL is missing payload")
		}
		mediaType := image[len("data:"):headerEnd]
		if semicolon := strings.Index(mediaType, ";"); semicolon >= 0 {
			mediaType = mediaType[:semicolon]
		}
		extension = extensionForMediaType(mediaType)
		image = image[headerEnd+1:]
	}

	data, err := base64.StdEncoding.DecodeString(image)
	if err != nil {
		return nil, "", err
	}
	if !isImageBytes(data) {
		return nil, "", errors.New("payload is not a supported image")
	}
	// Bare base64 (the frontend's attachment shape — no data: header carrying
	// the media type) falls through with the .png default, so derive the real
	// extension from the sniffed bytes. A HEIF attachment must persist as .heic,
	// and a JPEG must not be mislabeled .png: the artifact's extension becomes
	// its MIME on later turns (readArtifactAsDataURL) and the staged file name
	// the local CLI tools hand to sips/ffmpeg.
	if sniffed := imageExtensionForBytes(data); sniffed != "" {
		extension = sniffed
	}
	return data, extension, nil
}

// decodeAudioPayload mirrors decodeImagePayload for user-attached audio. It
// accepts a data:audio/<fmt>;base64,... data URL (what the frontend always
// sends) or a hydrated /atelier-artifact/ path (referencing a persisted audio
// artifact), and returns the raw bytes plus a file extension. Bytes are
// validated with isAudioBytes so a non-audio payload is rejected rather than
// written as a broken artifact. Unlike images there is no bare-base64 fallback:
// audio input is OpenRouter-only and openRouterInputAudio handles its own
// normalization at the adapter boundary.
func decodeAudioPayload(audio string) ([]byte, string, error) {
	audio = strings.TrimSpace(audio)
	if audio == "" {
		return nil, "", errors.New("audio data is empty")
	}

	// A conversation reloaded from history references audio artifacts via the
	// /atelier-artifact/ URL prefix (hydrateHistoryContent); resolve those back
	// to bytes on disk, mirroring decodeImagePayload / SaveAudio.
	if sourcePath := strings.TrimPrefix(audio, artifactPrefix); sourcePath != audio {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, "", err
		}
		if !isAudioBytes(data) {
			return nil, "", errors.New("artifact is not a supported audio file")
		}
		extension := strings.ToLower(filepath.Ext(sourcePath))
		if extension == "" {
			extension = ".mp3"
		}
		return data, extension, nil
	}

	extension := ".mp3"
	if strings.HasPrefix(audio, "data:audio/") {
		headerEnd := strings.Index(audio, ",")
		if headerEnd < 0 {
			return nil, "", errors.New("audio data URL is missing payload")
		}
		mediaType := audio[len("data:"):headerEnd]
		if semicolon := strings.Index(mediaType, ";"); semicolon >= 0 {
			mediaType = mediaType[:semicolon]
		}
		extension = audioExtensionForMediaType(mediaType)
		audio = audio[headerEnd+1:]
	}

	data, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		return nil, "", err
	}
	if !isAudioBytes(data) {
		return nil, "", errors.New("payload is not a supported audio file")
	}
	return data, extension, nil
}

// decodeVideoPayload mirrors decodeAudioPayload for user-attached video. It
// accepts a data:video/<fmt>;base64,... data URL (what the frontend always
// sends) or a hydrated /atelier-artifact/ path (referencing a persisted video
// artifact), and returns the raw bytes plus a file extension. Bytes are
// validated with isVideoBytes so a non-video payload is rejected rather than
// written as a broken artifact. Like audio, there is no bare-base64 fallback:
// video input is tool-only and the harness resolves AttachedVideos from the data
// URL the frontend sends (or a re-encoded artifact on history fallback).
func decodeVideoPayload(video string) ([]byte, string, error) {
	video = strings.TrimSpace(video)
	if video == "" {
		return nil, "", errors.New("video data is empty")
	}

	// A conversation reloaded from history references video artifacts via the
	// /atelier-artifact/ URL prefix (hydrateHistoryContent); resolve those back
	// to bytes on disk, mirroring decodeAudioPayload.
	if sourcePath := strings.TrimPrefix(video, artifactPrefix); sourcePath != video {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, "", err
		}
		if !isVideoBytes(data) {
			return nil, "", errors.New("artifact is not a supported video file")
		}
		extension := strings.ToLower(filepath.Ext(sourcePath))
		if extension == "" {
			extension = ".mp4"
		}
		return data, extension, nil
	}

	extension := ".mp4"
	if strings.HasPrefix(video, "data:video/") {
		headerEnd := strings.Index(video, ",")
		if headerEnd < 0 {
			return nil, "", errors.New("video data URL is missing payload")
		}
		mediaType := video[len("data:"):headerEnd]
		if semicolon := strings.Index(mediaType, ";"); semicolon >= 0 {
			mediaType = mediaType[:semicolon]
		}
		extension = videoExtensionForMediaType(mediaType)
		video = video[headerEnd+1:]
	}

	data, err := base64.StdEncoding.DecodeString(video)
	if err != nil {
		return nil, "", err
	}
	if !isVideoBytes(data) {
		return nil, "", errors.New("payload is not a supported video file")
	}
	return data, extension, nil
}

// contentArtifactPath resolves a HistoryContent entry's artifact path to an
// absolute filesystem path. Entries written by their own conversation carry a
// path relative to that conversation's directory; entries persisted as
// references to OTHER conversations' library assets carry an absolute path
// (see resolveReferencedAssets) and skip the conversation lookup entirely.
func contentArtifactPath(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	resolved := filepath.FromSlash(strings.TrimSpace(content.Path))
	if filepath.IsAbs(resolved) {
		return resolved, nil
	}
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(conversationPath), resolved), nil
}

// readArtifactAsDataURL resolves a persisted image artifact (referenced by
// relative Path in a HistoryContent entry) to its bytes on disk and re-encodes
// them as a base64 data URL — the shape AttachedImage consumers (fal/Ollama)
// expect. Used by the harness to re-hydrate a prior turn's attached image when
// the current turn has no attachment, so image-dependent tools (upscale_image,
// image-to-image, image-to-video) work across turns without forcing the user
// to re-attach on every message. Path resolution mirrors hydrateHistoryContent;
// bytes are read directly rather than going through getConversation's hydration.
func readArtifactAsDataURL(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	absPath, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	if !isImageBytes(data) {
		return "", fmt.Errorf("artifact %s is not a supported image", content.Path)
	}
	mediaType := strings.TrimSpace(content.MimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// readAudioArtifactAsDataURL resolves a persisted audio artifact (referenced by
// relative Path in a HistoryContent entry) to its bytes on disk and re-encodes
// them as a base64 data URL — the shape AttachedAudios consumers (fal's
// transcribe_audio) expect. Sibling of readArtifactAsDataURL for the audio
// path: used by the harness to re-hydrate a prior turn's attached audio when
// the current turn has no attachment, so transcribe_audio works across turns
// without forcing the user to re-attach on every message.
func readAudioArtifactAsDataURL(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	absPath, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	if !isAudioBytes(data) {
		return "", fmt.Errorf("artifact %s is not a supported audio file", content.Path)
	}
	mediaType := strings.TrimSpace(content.MimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "audio/") {
		// DetectContentType can return "application/octet-stream" for some
		// audio containers; fall back to mpeg so the data URL is well-formed.
		mediaType = "audio/mpeg"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (a *App) writeChatConversation(req ChatRequest, assistantContent, assistantThinking, model, reason string, tokens int) (string, error) {
	if strings.TrimSpace(assistantContent) == "" && strings.TrimSpace(assistantThinking) == "" {
		return "", nil
	}
	config, err := loadReadyConfig()
	if err != nil {
		return "", err
	}
	// Same fail-early project validation as StreamChat (see resolveTurnProject).
	if _, err := resolveTurnProject(config, req); err != nil {
		return "", err
	}
	// Apply the same per-conversation workspace override as StreamChat so this
	// non-streaming write path is consistent (see resolveTurnWorkspace).
	workspaceRoot, err := resolveTurnWorkspace(config, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		config.Tools.Filesystem.Root = workspaceRoot
	}
	// Same per-conversation model overrides as StreamChat so this
	// non-streaming write path is consistent (see
	// applyConversationModelOverrides).
	config, req, err = applyConversationModelOverrides(config, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		model = req.Model
	}
	title := ""
	if strings.TrimSpace(req.ConversationID) == "" {
		title = a.generateConversationTitle(config, req, assistantContent)
	}
	run := fallbackHarnessRun(req.Model, reason, tokens)
	return newHarnessEngine(config).SaveChatTurn(req, assistantContent, assistantThinking, model, resolvedProvider(req), reason, tokens, title, run)
}

// generateConversationTitle names a new conversation with the primary model,
// falling back to the truncated prompt whenever the call can't be made or
// comes back empty. It resolves through the provider layer rather than
// reaching for ollamaClient: req.Model is the *primary* chat model, so an
// OpenRouter primary would otherwise send an OpenRouter model name to the
// local Ollama endpoint and silently degrade every title to the fallback.
func (a *App) generateConversationTitle(config AppConfig, req ChatRequest, assistantContent string) string {
	userPrompt := lastUserPrompt(req.Messages)
	fallback := titleFromPrompt(userPrompt)
	if strings.TrimSpace(req.Model) == "" || strings.TrimSpace(userPrompt) == "" {
		return fallback
	}
	// Airplane mode: the title call would leave the machine whenever the
	// primary model is a cloud one — skip straight to the prompt-derived
	// fallback. Resolve would refuse the provider anyway; this keeps the skip
	// deliberate instead of relying on an error shaped like a failure.
	if airplaneModeActive(config) {
		return fallback
	}

	provider, err := a.providerFor(resolvedProvider(req), req.BaseURL)
	if err != nil {
		return fallback
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	completion, err := provider.CompleteChat(ctx, conversationTitleRequest(config, req, userPrompt, assistantContent))
	if err != nil {
		return fallback
	}
	return normalizeConversationTitle(completion.Content, userPrompt)
}

// conversationTitleRequest builds the title call for a finished turn. The
// options are Ollama tuning knobs; openRouterChatBody drops options entirely,
// so the same request is valid on either provider.
func conversationTitleRequest(config AppConfig, req ChatRequest, userPrompt, assistantContent string) ChatRequest {
	titlePrompt := "Generate a concise title for this chat conversation. Return only the title, no quotes, no punctuation wrapper, no explanation. Keep it under 8 words.\n\nUser:\n" +
		compactString(userPrompt, 1600) +
		"\n\nAssistant:\n" +
		compactString(assistantContent, 1600)
	options := map[string]any{
		"temperature": 0,
		"num_predict": 24,
	}
	for key, value := range req.Options {
		if _, exists := options[key]; !exists {
			options[key] = value
		}
	}
	return ChatRequest{
		BaseURL:  req.BaseURL,
		Provider: resolvedProvider(req),
		Model:    req.Model,
		System:   "You create short, specific conversation titles.",
		Messages: []ChatMessage{{Role: "user", Content: titlePrompt}},
		Options:  withNumCtx(options, ollamaNumCtx(config)),
	}
}

func (a *App) emitChatEvent(event ChatStreamEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "chat:chunk", event)
	}
}

// HarnessRunEvent is the atelier:harness-run payload: the full run snapshot
// after every step transition. The live panel and the persisted record render
// from the same shape, so the UI never fabricates in-flight state.
type HarnessRunEvent struct {
	RequestID      string     `json:"requestId"`
	ConversationID string     `json:"conversationId,omitempty"`
	Run            HarnessRun `json:"run"`
}

func (a *App) emitHarnessRunEvent(event HarnessRunEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "atelier:harness-run", event)
	}
}

// LibraryArchiveEvent is the atelier:library-archive payload: coarse progress
// for a library export or import — one tick per written conversation (export)
// and per verified entry / written conversation (import).
type LibraryArchiveEvent struct {
	Op        string `json:"op"` // "export" | "import"
	LibraryID string `json:"libraryId,omitempty"`
	Phase     string `json:"phase"` // "verify" | "write"
	Done      int    `json:"done"`
	Total     int    `json:"total"`
}

func (a *App) emitLibraryArchiveEvent(event LibraryArchiveEvent) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "atelier:library-archive", event)
	}
}

func (a *App) requestToolPermission(ctx context.Context, event ToolPermissionRequestEvent) ToolPermissionDecision {
	if a.ctx == nil {
		// No UI is attached, so nobody can approve: fail closed.
		return ToolPermissionDecision{Outcome: permissionOutcomeDenied}
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = randomID("permission")
	}
	requestedAt := time.Now()
	decisionAt := func(approved bool, outcome string) ToolPermissionDecision {
		return ToolPermissionDecision{Approved: approved, Outcome: outcome, WaitMS: time.Since(requestedAt).Milliseconds()}
	}
	response := make(chan bool, 1)
	a.permissionsMu.Lock()
	a.permissions[event.ID] = response
	a.permissionsMu.Unlock()
	runtime.EventsEmit(a.ctx, "atelier:tool-permission", event)

	select {
	case approved := <-response:
		if approved {
			return decisionAt(true, permissionOutcomeApproved)
		}
		return decisionAt(false, permissionOutcomeDenied)
	case <-ctx.Done():
		a.permissionsMu.Lock()
		delete(a.permissions, event.ID)
		a.permissionsMu.Unlock()
		return decisionAt(false, permissionOutcomeCancelled)
	case <-time.After(2 * time.Minute):
		a.permissionsMu.Lock()
		delete(a.permissions, event.ID)
		a.permissionsMu.Unlock()
		return decisionAt(false, permissionOutcomeTimeout)
	}
}

func (a *App) resolveBaseURL(baseURL string) string {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return a.cachedBaseURL()
	}
	if normalized == "" {
		return a.cachedBaseURL()
	}
	return normalized
}

// cachedBaseURL reads the runtime Ollama base URL set by GetConfig, SaveConfig,
// or SetOllamaBaseURL. Those writers hold configMu; this read takes the same
// lock so a config save during a concurrent chat stream can't race the field.
func (a *App) cachedBaseURL() string {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	return a.baseURL
}

func (a *App) ollamaClient(baseURL string) OllamaClient {
	return newOllamaClient(a.client, a.resolveBaseURL(baseURL))
}

// openRouterClient builds an OpenRouterClient from the saved API key, mirroring
// ollamaClient's role as a direct client accessor for harness capability checks
// (ModelCapabilities). Returns ok=false when no key is configured so callers can
// skip capability detection without a key-load error — matching how Resolve
// treats an absent key as a config state rather than a hard failure.
func (a *App) openRouterClient() (OpenRouterClient, bool) {
	apiKey, err := loadOpenRouterAPIKey()
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return OpenRouterClient{}, false
	}
	return newOpenRouterClient(a.client, apiKey), true
}

func (a *App) providerFor(providerID, baseURL string) (ChatProvider, error) {
	return newProviderRegistry(a).Resolve(providerID, baseURL)
}

// ListPrimaryModels lists models available for the primary chat role from
// the given provider. Harness/image model lists still go through the
// existing Ollama-only ListModels method — this is deliberately separate so
// the primary-role picker doesn't disturb the harness/image UI's capability
// detection (image-generation flags, family/parameter metadata), which only
// OllamaModel carries today.
func (a *App) ListPrimaryModels(provider, baseURL string) ([]ModelInfo, error) {
	resolved, err := a.providerFor(provider, baseURL)
	if err != nil {
		return nil, err
	}
	return resolved.ListModels(context.Background())
}

func (a *App) SaveOpenRouterAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearOpenRouterAPIKey()
	}
	return saveOpenRouterAPIKey(apiKey)
}

func (a *App) HasOpenRouterAPIKey() (bool, error) {
	key, err := loadOpenRouterAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckOpenRouterConnection validates the stored OpenRouter API key with a
// cheap authenticated ping (GET /api/v1/key, no tokens spent), mirroring
// CheckFalConnection. The public /models list cannot serve as this check — it
// returns 200 even for an invalid key, so a green model-list fetch proved
// reachability, never the key.
func (a *App) CheckOpenRouterConnection() error {
	key, err := loadOpenRouterAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errOpenRouterKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newOpenRouterClient(a.client, key).VerifyKey(ctx)
}

func (a *App) SaveFalAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearFalAPIKey()
	}
	return saveFalAPIKey(apiKey)
}

func (a *App) HasFalAPIKey() (bool, error) {
	key, err := loadFalAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckFalConnection validates the stored fal.ai API key with a cheap
// authenticated ping (no generation). Returns an error describing why the key
// is rejected, or nil when it resolves. Used by the Settings "Check Connection"
// button and the startup key check, like its OpenRouter and Replicate
// counterparts — the model catalogs are discovery aids and never double as
// key checks.
func (a *App) CheckFalConnection() error {
	key, err := loadFalAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errFalKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newFalClient(a.client, key).VerifyKey(ctx)
}

func (a *App) SaveReplicateAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearReplicateAPIKey()
	}
	return saveReplicateAPIKey(apiKey)
}

func (a *App) HasReplicateAPIKey() (bool, error) {
	key, err := loadReplicateAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// CheckReplicateConnection validates the stored Replicate API token with a
// cheap authenticated ping (GET /v1/account, no generation). Returns an error
// describing why the token is rejected, or nil when it resolves. Used by the
// Settings "Check Connection" button, mirroring CheckFalConnection.
func (a *App) CheckReplicateConnection() error {
	key, err := loadReplicateAPIKey()
	if err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errReplicateKeyNotConfigured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newReplicateClient(a.client, key).VerifyKey(ctx)
}

// ListReplicateModels returns the official models of Replicate's
// text-to-image collection for the Settings image-model picker — the
// Replicate counterpart of ListFalModels. Requires a stored token (the client
// attaches it to every request), so call it after the key is saved.
func (a *App) ListReplicateModels() ([]ReplicateModel, error) {
	return a.listReplicateCollection(replicateTextToImageCollection)
}

// ListReplicateImageEditModels returns the official models of Replicate's
// image-editing collection for the Settings image-edit-model picker.
func (a *App) ListReplicateImageEditModels() ([]ReplicateModel, error) {
	return a.listReplicateCollection(replicateImageEditingCollection)
}

// ListReplicateUpscaleModels returns the official models of Replicate's
// super-resolution collection for the Settings image-upscale-model picker,
// shown when replicate is the image provider (upscale follows it).
func (a *App) ListReplicateUpscaleModels() ([]ReplicateModel, error) {
	return a.listReplicateCollection(replicateSuperResolutionCollection)
}

// ListReplicateVideoUpscaleModels returns the video upscalers of Replicate's
// ai-enhance-videos collection for the Settings video-upscale-model picker,
// shown when replicate is the video provider (video upscale follows it). The
// collection mixes upscalers with face restoration, colorization, and frame
// interpolation, so entries are post-filtered by id markers — the fal pattern
// of partitioning a broad category for a picker.
func (a *App) ListReplicateVideoUpscaleModels() ([]ReplicateModel, error) {
	models, err := a.listReplicateCollection(replicateEnhanceVideosCollection)
	if err != nil {
		return nil, err
	}
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if isReplicateVideoUpscaleModel(model) {
			filtered = append(filtered, model)
		}
	}
	return filtered, nil
}

// isReplicateVideoUpscaleModel reports whether a collection entry looks like a
// video upscaler: the id (owner/name, lowercased) carries one of the upscale
// family markers. Matches topazlabs/video-upscale, the crystal and esrgan
// video upscalers, and the super-resolution (animesr/basicvsr) models; skips
// the collection's face restoration, colorization, and interpolation entries.
func isReplicateVideoUpscaleModel(model ReplicateModel) bool {
	lower := strings.ToLower(model.ID)
	for _, marker := range []string{"upscale", "esrgan", "animesr", "superresolution", "super-resolution", "basicvsr"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ListReplicateVideoRestyleModels returns the restyle-class (video-to-video
// edit) models of Replicate's video-editing collection for the Settings
// restyle-model picker, shown when replicate is the video provider (restyle
// follows it). The collection mixes in audio, lipsync, and utility models, so
// entries are post-filtered by id markers.
func (a *App) ListReplicateVideoRestyleModels() ([]ReplicateModel, error) {
	return a.listReplicateVideoEditingCollection(isReplicateVideoRestyleModel)
}

// ListReplicateVideoReframeModels returns the generative reframe models of
// the same collection for the Settings reframe-model picker, shown when
// replicate is the video provider (reframe follows it).
func (a *App) ListReplicateVideoReframeModels() ([]ReplicateModel, error) {
	return a.listReplicateVideoEditingCollection(isReplicateVideoReframeModel)
}

// ListReplicateVideoExtendModels returns the video-extension models of the
// same collection for the Settings video-extend-model picker, shown when
// replicate is the video provider (the extend turn routes there like the
// transforms, rather than failing with a provider-switch remedy).
func (a *App) ListReplicateVideoExtendModels() ([]ReplicateModel, error) {
	return a.listReplicateVideoEditingCollection(isReplicateVideoExtendModel)
}

// listReplicateVideoEditingCollection fetches the video-editing collection and
// keeps the entries matching keep — the shared partitioned-picker body behind
// the restyle and reframe listers.
func (a *App) listReplicateVideoEditingCollection(keep func(ReplicateModel) bool) ([]ReplicateModel, error) {
	models, err := a.listReplicateCollection(replicateVideoEditingCollection)
	if err != nil {
		return nil, err
	}
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if keep(model) {
			filtered = append(filtered, model)
		}
	}
	return filtered, nil
}

// isReplicateVideoRestyleModel reports whether a collection entry looks like a
// restyle-class editor: the id carries one of the edit/modify markers. Matches
// kwaivgi/kling-v3-omni-video (natural-language editing with reference
// images), wan-video/wan-2.7-videoedit, and luma/modify-video; skips the
// collection's reframe, extend, audio, and utility entries.
func isReplicateVideoRestyleModel(model ReplicateModel) bool {
	lower := strings.ToLower(model.ID)
	for _, marker := range []string{"videoedit", "video-edit", "modify-video", "omni-video"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// isReplicateVideoReframeModel reports whether a collection entry is a
// generative reframer — luma/reframe-video and siblings.
func isReplicateVideoReframeModel(model ReplicateModel) bool {
	return strings.Contains(strings.ToLower(model.ID), "reframe")
}

// isReplicateVideoExtendModel reports whether a collection entry is a
// video-extension endpoint — one that continues an attached clip into a longer
// one and returns the stitched result (xai/grok-imagine-video-extension). The
// id is checked for "extend" and "continue", the same naming pair the fal
// partition (isFalVideoExtendModel) recognizes; the video-editing collection
// holds no audio-extension entries, so the id markers alone partition it.
func isReplicateVideoExtendModel(model ReplicateModel) bool {
	lower := strings.ToLower(model.ID)
	return strings.Contains(lower, "extend") || strings.Contains(lower, "continue")
}

// ListReplicateVideoModels returns the official models of Replicate's
// text-to-video collection for the Settings video-model picker.
func (a *App) ListReplicateVideoModels() ([]ReplicateModel, error) {
	return a.listReplicateCollection(replicateTextToVideoCollection)
}

// ListReplicateVideoImageModels returns the official models of Replicate's
// image-to-video collection for the Settings image-to-video model picker.
func (a *App) ListReplicateVideoImageModels() ([]ReplicateModel, error) {
	return a.listReplicateCollection(replicateImageToVideoCollection)
}

// ListReplicateTranscribeModels returns the official models of Replicate's
// speech-to-text collection for the Settings transcription-model picker,
// shown when replicate is the transcription provider. The collection mixes
// in speech LLMs (gemini, granite) whose chat-shaped inputs the canonical
// transcription request cannot fill, so entries are post-filtered by id
// markers — the fal pattern of partitioning a broad category for a picker.
// An empty result (a wholly unexpected collection shape) falls back to the
// default model so the dropdown never goes bare.
func (a *App) ListReplicateTranscribeModels() ([]ReplicateModel, error) {
	models, err := a.listReplicateCollection(replicateSpeechToTextCollection)
	if err != nil {
		return nil, err
	}
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if isReplicateTranscribeModel(model) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, ReplicateModel{ID: defaultReplicateTranscribeModel})
	}
	return filtered, nil
}

// isReplicateTranscribeModel reports whether a speech-to-text collection
// entry looks like a transcriber the canonical request can drive: it must
// not carry a speech-LLM marker (the gemini and granite families take
// prompt/messages arrays instead of an audio URI). Everything else —
// whisper-class, gpt-4o-transcribe, parakeet — stays: the resolver's
// audio-input requirement fails models that slip through, with a
// provider-switch remedy.
func isReplicateTranscribeModel(model ReplicateModel) bool {
	lower := strings.ToLower(model.ID)
	for _, marker := range []string{"gemini", "granite"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// listReplicateCollection is the shared body of the Replicate catalog listers:
// load the token, fetch one collection with a bounded timeout. Collections are
// pre-bucketed server-side, so there is no pagination to walk.
func (a *App) listReplicateCollection(slug string) ([]ReplicateModel, error) {
	key, err := loadReplicateAPIKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return newReplicateClient(a.client, key).ListCollectionModels(ctx, slug)
}

// ListReplicateLipsyncImageModels returns the official lipsync collection
// models whose input schemas serve the canonical image-face pair — an image
// plus the driving audio — for the Settings talking-head picker, shown when
// replicate is the lipsync provider. Text-driven avatar generators
// (kling-lip-sync's text + voice_id, wan-2.2-s2v's required prompt,
// p-video-avatar's voice_script) and every video-face re-sync endpoint drop
// out: the canonical request cannot drive them. An empty result falls back
// to the default model so the dropdown never goes bare.
func (a *App) ListReplicateLipsyncImageModels() ([]ReplicateModel, error) {
	return a.listReplicateLipsyncModels("sourceImage", defaultReplicateLipsyncImageModel)
}

// ListReplicateLipsyncVideoModels is the video-face sibling: re-sync
// endpoints whose schemas accept a video plus the driving audio
// (sync/lipsync-2, the HeyGen pair, pixverse/lipsync).
func (a *App) ListReplicateLipsyncVideoModels() ([]ReplicateModel, error) {
	return a.listReplicateLipsyncModels("sourceVideo", defaultReplicateLipsyncVideoModel)
}

// listReplicateLipsyncModels is the shared body of the two lipsync pickers:
// the collection walk, then a per-model input-schema check through the
// replicate schema cache (which warms the same cache the gateway's lip_sync
// wiring reads). The fal listers' two-category fetch is the sibling here;
// the schema check replaces their id-marker partition because the face kinds
// are only visible in the schemas — no id vocabulary separates a talking
// head from a re-sync endpoint.
func (a *App) listReplicateLipsyncModels(faceKind, fallback string) ([]ReplicateModel, error) {
	models, err := a.listReplicateCollection(replicateLipsyncCollection)
	if err != nil {
		return nil, err
	}
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cache := newReplicateSchemaCache(a.client, config.Storage.Root)
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if replicateLipsyncSchemaServes(cache.Get(ctx, model.ID), faceKind) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, ReplicateModel{ID: fallback})
	}
	return filtered, nil
}

// ListReplicateSoundEffectModels returns the non-voice models of Replicate's
// ai-music-generation collection for the Settings music/sound-effects picker,
// shown when replicate is the AudioProvider (generate_sound follows it). The
// collection is music-weighted but its non-voice entries are exactly the
// canonical request's domain (stable-audio-2.5 ships sound effects too), so
// only voice/tts ids are filtered out. An empty result (a wholly unexpected
// collection shape) falls back to the default model so the dropdown never
// goes bare.
func (a *App) ListReplicateSoundEffectModels() ([]ReplicateModel, error) {
	models, err := a.listReplicateCollection(replicateMusicGenerationCollection)
	if err != nil {
		return nil, err
	}
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if isReplicateSoundModel(model) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, ReplicateModel{ID: defaultReplicateSoundEffectsModel})
	}
	return filtered, nil
}

// isReplicateSoundModel reports whether an ai-music-generation collection
// entry serves generate_sound's canonical request: everything except the
// voice/tts ids (none in the collection today — the filter is the
// belt-and-braces partition that keeps a future speech entry out).
func isReplicateSoundModel(model ReplicateModel) bool {
	lower := strings.ToLower(model.ID)
	for _, marker := range []string{"speech", "tts", "voice"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// ListReplicateSpeechModels returns the text-to-speech collection models
// whose input schemas serve the canonical generate_speech request — a
// scalar-string text input — for the Settings speech-model picker, shown when
// replicate is the SpeechProvider. The collection mixes TTS endpoints with
// the voice-cloning utility (minimax/voice-cloning takes a clip and mints an
// id, speaking nothing), and only the schemas tell them apart — the id
// vocabulary ("voice", "speech") is shared — so each entry's input schema is
// checked through the replicate schema cache (warming the same cache the
// gateway reads, the extend/lipsync pickers' pattern). An empty result falls
// back to the default model so the dropdown never goes bare.
func (a *App) ListReplicateSpeechModels() ([]ReplicateModel, error) {
	models, err := a.listReplicateCollection(replicateTextToSpeechCollection)
	if err != nil {
		return nil, err
	}
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cache := newReplicateSchemaCache(a.client, config.Storage.Root)
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if replicateSpeechSchemaServes(cache.Get(ctx, model.ID)) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, ReplicateModel{ID: defaultReplicateSpeechModel})
	}
	return filtered, nil
}

// ListReplicateSoundEffectsVideoModels returns the video-to-sound models of
// Replicate's video-editing collection for the Settings video-sound-effects
// picker, shown when replicate is the AudioProvider (add_sound_effects
// follows it). The collection mixes in restyle, reframe, extend, and utility
// models, so entries are id-marker pre-filtered AND input-schema checked
// (replicateSFXSchemaServes — a scalar-string video input through the
// replicate-sfx synonyms, the lipsync/extend pickers' precise filter), which
// keeps utility entries like extract-audio/video-audio-merge out. The whole
// class is community-hosted today and the collection lists official models
// only, so an empty result is the EXPECTED shape: it falls back to the
// verified drivable packagings by name (the transcribe lister's rule,
// widened to the family — MMAudio's L40S default, its T4 hardware variant,
// and ThinkSound, steered through its caption input), and the field stays
// free-text editable either way.
func (a *App) ListReplicateSoundEffectsVideoModels() ([]ReplicateModel, error) {
	models, err := a.listReplicateVideoEditingCollection(isReplicateVideoSFXModel)
	if err != nil {
		return nil, err
	}
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cache := newReplicateSchemaCache(a.client, config.Storage.Root)
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if replicateSFXSchemaServes(cache.Get(ctx, model.ID)) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		for _, id := range replicateVideoSFXFallbacks {
			filtered = append(filtered, ReplicateModel{ID: id})
		}
	}
	return filtered, nil
}

// replicateVideoSFXFallbacks names the video-to-sound packagings verified
// drivable against the canonical request — the picker's bare-dropdown
// fallback, since the official-only collection listing never surfaces this
// community-hosted class. The first entry is the default model.
var replicateVideoSFXFallbacks = []string{
	defaultReplicateSoundEffectsVideoModel,
	"zsxkib/mmaudio-t4",
	"zsxkib/thinksound",
}

// isReplicateVideoSFXModel reports whether a video-editing collection entry
// is a video-to-sound GENERATOR — the id carries one of the mmaudio/sfx/
// sound/foley markers. Plain "audio" is deliberately not a marker: the
// collection carries audio utilities (lucataco/extract-audio strips the
// track, lucataco/video-audio-merge muxes one in) that no sound description
// can drive; the schema check behind this pre-filter rejects them too, but
// the vocabulary shouldn't invite them. Skips the collection's restyle,
// reframe, and extend entries, which share no marker.
func isReplicateVideoSFXModel(model ReplicateModel) bool {
	lower := strings.ToLower(model.ID)
	for _, marker := range []string{"mmaudio", "sfx", "sound", "foley"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ListReplicateAudioExtendModels returns the ai-music-generation collection
// models whose input schemas serve the canonical extend_audio request — a
// scalar-string source-audio input plus a scalar-boolean continuation flag —
// for the Settings audio-extend picker, shown when replicate is the
// AudioProvider (extend follows it). The capability is only visible in the
// schemas, so the lister checks each model's input schema through the
// replicate schema cache (warming the same cache the gateway reads — the
// lipsync pickers' pattern); MusicGen's text-to-music siblings without the
// continuation packaging drop out. An empty result falls back to the default
// model so the dropdown never goes bare.
func (a *App) ListReplicateAudioExtendModels() ([]ReplicateModel, error) {
	models, err := a.listReplicateCollection(replicateMusicGenerationCollection)
	if err != nil {
		return nil, err
	}
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cache := newReplicateSchemaCache(a.client, config.Storage.Root)
	filtered := make([]ReplicateModel, 0, len(models))
	for _, model := range models {
		if replicateContinuationSchemaServes(cache.Get(ctx, model.ID)) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, ReplicateModel{ID: defaultReplicateAudioExtendModel})
	}
	return filtered, nil
}

// ListReplicateVideoDurations returns the duration values the given Replicate
// video model accepts, read from its input schema — the Replicate counterpart
// of ListFalVideoDurations. Returns an empty slice (not an error) when the
// schema is unavailable or the model has no enum-constrained duration control
// (most Replicate video models take a free number of seconds), so the UI falls
// back to a generic option set.
func (a *App) ListReplicateVideoDurations(model string) ([]string, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	opts := replicateVideoDurationOptions(ctx, a.client, config.Storage.Root, model)
	if opts == nil {
		return []string{}, nil
	}
	return opts, nil
}

// ListOpenAICompatibleModels returns the model ids advertised by the local
// OpenAI-compatible image server (GET /v1/models) for the Settings model
// picker. Takes the endpoint explicitly because the user may still be editing
// the baseURL field; the optional bearer key is attached when one is stored.
// The picker also accepts free text, so a server without /v1/models only loses
// the suggestions, not the field.
func (a *App) ListOpenAICompatibleModels(baseURL string) ([]string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultOpenAICompatibleBaseURL
	}
	apiKey, err := loadOpenAICompatibleAPIKey()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := newOpenAICompatibleClient(a.client, baseURL, apiKey)
	return client.ListModels(ctx)
}

// SaveOpenAICompatibleAPIKey stores the optional bearer key for the local
// OpenAI-compatible image server. Saving an empty key clears it — a local
// server with no auth is the common case and must send no Authorization header.
func (a *App) SaveOpenAICompatibleAPIKey(apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return clearOpenAICompatibleAPIKey()
	}
	return saveOpenAICompatibleAPIKey(apiKey)
}

func (a *App) HasOpenAICompatibleAPIKey() (bool, error) {
	key, err := loadOpenAICompatibleAPIKey()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(key) != "", nil
}

// ClearOpenAICompatibleAPIKey removes the optional bearer key so requests to
// the local image server go out unauthenticated again.
func (a *App) ClearOpenAICompatibleAPIKey() error {
	return clearOpenAICompatibleAPIKey()
}

// ListFalModels returns fal's text-to-image catalog for the Settings image-model
// picker, replacing the free-text fal model field. Unlike the chat primary
// picker (ListPrimaryModels), fal is not a ChatProvider — this hits fal's
// /v1/models catalog directly. Requires a stored fal key (the underlying client
// attaches it to every request), so call it after the key is saved.
func (a *App) ListFalModels() ([]FalModel, error) {
	return a.cachedFalCategoryModels(falTextToImageCategory)
}

// ListFalVideoModels returns fal's text-to-video catalog for the Settings
// video-model picker. It mirrors ListFalModels but filters to the text-to-video
// category; fal is the only video backend (Ollama has no video models).
func (a *App) ListFalVideoModels() ([]FalModel, error) {
	return a.cachedFalCategoryModels(falTextToVideoCategory)
}

// ListFalVideoDurations returns the duration values the given fal video model
// accepts (e.g. ["auto","4",...,"15"] for Seedance, ["5","10"] for Kling), read
// from the model's published input schema. Used by the Settings duration
// pickers so each model only offers durations it won't 422 on — the failure in
// conv_4feb919a came from offering Seedance-only "auto" to a model that wanted
// an integer. Returns an empty slice (not an error) when the schema is
// unavailable — offline, no key, or a model with no duration control — so the
// UI falls back to a generic option set. The OpenAPI endpoint is public, but
// the fetch path loads the key the same way generation calls do.
func (a *App) ListFalVideoDurations(model string) ([]string, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	opts := videoDurationOptions(ctx, a.client, config.Storage.Root, model)
	if opts == nil {
		return []string{}, nil
	}
	return opts, nil
}

// ListFalSoundEffectDurations returns the duration values the given fal sound
// model accepts, for the Settings audio duration picker — the audio sibling of
// ListFalVideoDurations, mirroring resolveAudioBody's duration lookup so the
// picker lists only values the model won't 422 on (elevenlabs declares a
// numeric 0.5–22 range, so the synthesized ladder is 1–22).
func (a *App) ListFalSoundEffectDurations(model string) ([]string, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	opts := modelDurationOptions(ctx, a.client, config.Storage.Root, model, "audio")
	if opts == nil {
		return []string{}, nil
	}
	return opts, nil
}

// ListFalVideoImageModels returns fal's image-to-video catalog for the Settings
// image-to-video model picker (used to animate an attached image).
func (a *App) ListFalVideoImageModels() ([]FalModel, error) {
	return a.cachedFalCategoryModels(falImageToVideoCategory)
}

// ListFalVideoKeyframeModels returns fal's image-to-video catalog for the
// Settings keyframe-model picker. Keyframe (start→end transition) endpoints are
// image-to-video models with an end-frame input, so they share the
// image-to-video category with ListFalVideoImageModels.
func (a *App) ListFalVideoKeyframeModels() ([]FalModel, error) {
	return a.cachedFalCategoryModels(falImageToVideoCategory)
}

// falCategoryCacheTTL bounds how long a fetched fal model category is reused
// before a re-fetch. Long enough that the listers on one Settings open share a
// single fetch per category (the six video-to-video pickers used to page the
// same category six times); short enough that a newly added endpoint shows up
// on a later visit.
const falCategoryCacheTTL = 2 * time.Minute

// falCategoryEntry is one cached category walk.
type falCategoryEntry struct {
	models    []FalModel
	fetchedAt time.Time
}

// fetchFalCategoryModels pages a fal model category. falCategoryFetch overrides
// it in tests; the real path loads the key and calls the fal client.
func (a *App) fetchFalCategoryModels(ctx context.Context, category string) ([]FalModel, error) {
	if a.falCategoryFetch != nil {
		return a.falCategoryFetch(ctx, category)
	}
	key, err := loadFalAPIKey()
	if err != nil {
		return nil, err
	}
	return newFalClient(a.client, key).ListModels(ctx, category, 0)
}

// cachedFalCategoryModels returns fal's catalog for one category, fetching it
// at most once per falCategoryCacheTTL across every lister that reads it. The
// Settings screen walks ~a dozen categories on load; without the cache each
// open re-paged all of them, and under fal's /v1/models rate limit the tail of
// that burst (the audio listers) failed and left those pickers with zero
// options — the speech combobox rendered no dropdown at all. A category with a
// stale cached copy also serves that copy when the re-fetch fails, so a
// rate-limited refresh degrades to slightly-old data instead of an empty
// picker.
func (a *App) cachedFalCategoryModels(category string) ([]FalModel, error) {
	a.falCategoryMu.Lock()
	defer a.falCategoryMu.Unlock()
	if entry, ok := a.falCategories[category]; ok && time.Since(entry.fetchedAt) < falCategoryCacheTTL {
		return entry.models, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	models, err := a.fetchFalCategoryModels(ctx, category)
	if err != nil {
		if entry, ok := a.falCategories[category]; ok {
			return entry.models, nil
		}
		return nil, err
	}
	if a.falCategories == nil {
		a.falCategories = map[string]falCategoryEntry{}
	}
	a.falCategories[category] = falCategoryEntry{models: models, fetchedAt: time.Now()}
	return models, nil
}

// cachedImageToImageModels returns fal's image-to-image catalog through the
// shared category cache. The upscale and edit listers both derive their picks
// from this one bucket.
func (a *App) cachedImageToImageModels() ([]FalModel, error) {
	return a.cachedFalCategoryModels(falImageUpscalingCategory)
}

// ListFalUpscaleModels returns fal's image-upscaler catalog for the Settings
// upscale-model picker. fal is the only upscale backend (Ollama has none). fal
// files upscalers under the broader image-to-image category (alongside
// inpainting, background removal, etc.), so we fetch that (shared, cached) and
// keep only the models whose id or tags mention upscaling.
func (a *App) ListFalUpscaleModels() ([]FalModel, error) {
	models, err := a.cachedImageToImageModels()
	if err != nil {
		return nil, err
	}
	upscale := make([]FalModel, 0, len(models))
	for _, model := range models {
		if isFalUpscaleModel(model) {
			upscale = append(upscale, model)
		}
	}
	return upscale, nil
}

// isFalUpscaleModel reports whether a fal catalog entry is an image upscaler,
// by id or tag. fal tags upscalers inconsistently ("upscaling" vs "upscale",
// sometimes neither), so the id is checked too.
func isFalUpscaleModel(model FalModel) bool {
	if strings.Contains(strings.ToLower(model.ID), "upscal") {
		return true
	}
	for _, tag := range model.Tags {
		if strings.Contains(strings.ToLower(tag), "upscal") {
			return true
		}
	}
	return false
}

// ListFalImageEditModels returns fal's image-editing catalog for the Settings
// image-to-image-model picker — the endpoints eligible to transform an attached
// source image. fal files these under the same broad image-to-image bucket as
// upscalers (inpainting, background removal, style transfer, etc.), so we fetch
// that category and keep everything that is NOT an upscaler — the inverse of
// ListFalUpscaleModels. The two listers share falImageUpscalingCategory.
func (a *App) ListFalImageEditModels() ([]FalModel, error) {
	models, err := a.cachedImageToImageModels()
	if err != nil {
		return nil, err
	}
	edits := make([]FalModel, 0, len(models))
	for _, model := range models {
		if !isFalUpscaleModel(model) {
			edits = append(edits, model)
		}
	}
	return edits, nil
}

// isFalLipsyncModel reports whether a fal catalog entry is a lip-sync endpoint,
// by id or tag. fal tags these inconsistently ("lipsync" vs "lip sync", and some
// carry only one), so both the id and every tag are checked. Mirrors
// isFalUpscaleModel's defensive matching.
//
// Kling's ai-avatar endpoints are the untagged exception: they are audio-driven
// talking-head generators whose published schemas require exactly audio_url +
// image_url — the audio-to-video picker's contract, mapped as-is by
// lipsyncSynonyms — but fal files them under image-to-video with no lipsync tag,
// so "kling" + "avatar" in the id also matches. Avatar matching stays
// Kling-scoped on purpose: other avatar endpoints do not take an audio+image
// pair (argil/avatars/audio-to-video takes an avatar id with no image input,
// fal-ai/ai-avatar/single-text and friends are text-driven), and selecting one
// for an audio+image turn fails at generation time via resolveLipsyncBody's
// face-source rule.
func isFalLipsyncModel(model FalModel) bool {
	id := strings.ToLower(model.ID)
	if strings.Contains(id, "lipsync") || strings.Contains(id, "lip-sync") {
		return true
	}
	if strings.Contains(id, "kling") && strings.Contains(id, "avatar") {
		return true
	}
	for _, tag := range model.Tags {
		lower := strings.ToLower(tag)
		if strings.Contains(lower, "lipsync") || strings.Contains(lower, "lip sync") {
			return true
		}
	}
	return false
}

// ListFalLipsyncImageModels returns fal's audio-to-video lip sync catalog for
// the Settings audio-to-video-model picker — endpoints that drive a face image
// with an audio clip (a talking head). fal files these across the audio-to-video
// and image-to-video categories (sync-lipsync/v3/image-to-video is
// image-to-video; kling's ai-avatar endpoints sit in image-to-video with no
// lipsync tag at all), so both are fetched, merged, deduped by endpoint id, and
// kept only when isFalLipsyncModel matches. Kling's lipsync/* endpoints are
// deliberately absent: fal files them under text-to-video, they take no image
// input, and an audio+image pick would fail resolveLipsyncBody's face-source
// rule. The category split mirrors how ListFalAudioModels merges two
// categories; the filter mirrors ListFalUpscaleModels.
func (a *App) ListFalLipsyncImageModels() ([]FalModel, error) {
	merged := []FalModel{}
	seen := map[string]bool{}
	for _, category := range []string{falAudioToVideoCategory, falImageToVideoCategory} {
		models, err := a.cachedFalCategoryModels(category)
		if err != nil {
			// One category failing (a rate-limited request) keeps the
			// other's matches; only a total failure degrades, below.
			continue
		}
		for _, model := range models {
			if model.ID == "" || seen[model.ID] || !isFalLipsyncModel(model) {
				continue
			}
			seen[model.ID] = true
			merged = append(merged, model)
		}
	}
	if len(merged) == 0 {
		merged = append(merged, FalModel{ID: defaultFalLipsyncImageModel})
	}
	return merged, nil
}

// falTextDrivenLipsyncModel reports whether a lip sync endpoint drives speech
// from TTS text rather than an audio file — fal tags its text-driven variants
// "text to video" (fal-ai/kling-video/lipsync/text-to-video takes
// audio_input.tts_audio and no audio_url), and the id suffix carries the same
// signal. lip_sync always carries an audio clip, so a text-driven endpoint
// cannot serve it: listing one would invite a pick that 422s on the tool's
// contract. Consulted only for entries isFalLipsyncModel already matched.
func falTextDrivenLipsyncModel(model FalModel) bool {
	if strings.Contains(strings.ToLower(model.ID), "text-to-video") {
		return true
	}
	for _, tag := range model.Tags {
		if strings.EqualFold(strings.TrimSpace(tag), "text to video") {
			return true
		}
	}
	return false
}

// ListFalLipsyncVideoModels returns fal's video-to-video lip sync catalog for
// the Settings video-to-video-model picker — endpoints that re-lip-sync an
// existing video with an audio clip (sync-lipsync, latentsync, veed, heygen,
// pixverse). fal files the sync-lipsync family under the video-to-video
// category but kling's lipsync endpoints (video_url + audio_url, e.g.
// lipsync/audio-to-video) under text-to-video, so both are fetched, merged,
// and deduped by endpoint id — the audio-to-video lister's two-category
// pattern — kept when isFalLipsyncModel matches, minus the text-driven entries
// the tool's required audio clip rules out.
func (a *App) ListFalLipsyncVideoModels() ([]FalModel, error) {
	merged := []FalModel{}
	seen := map[string]bool{}
	for _, category := range []string{falVideoToVideoCategory, falTextToVideoCategory} {
		models, err := a.cachedFalCategoryModels(category)
		if err != nil {
			// One category failing (a rate-limited request) keeps the
			// other's matches; only a total failure degrades, below.
			continue
		}
		for _, model := range models {
			if model.ID == "" || seen[model.ID] || !isFalLipsyncModel(model) || falTextDrivenLipsyncModel(model) {
				continue
			}
			seen[model.ID] = true
			merged = append(merged, model)
		}
	}
	if len(merged) == 0 {
		// A failed (rate-limited) or reshuffled catalog keeps the mapped
		// default — the picker stays openable.
		merged = append(merged, FalModel{ID: defaultFalLipsyncVideoModel})
	}
	return merged, nil
}

// isFalVideoSFXModel reports whether a fal catalog entry is a video
// sound-effects endpoint — one that watches an attached clip and generates a
// synchronized soundtrack for it (Mirelo SFX, MMAudio, CassetteAI's and
// PixVerse's video sound-effects generators). fal files these across the
// video-to-video and video-to-audio categories, so the id and tags are checked
// for the sfx markers rather than trusting one category. Mirrors
// isFalLipsyncModel / isFalVideoExtendModel. The text-only SFX generators
// (cassetteai/sound-effects-generator, mirelo sfx2.0 text-to-audio) live in
// the text-to-audio category this predicate never sees — they are
// generate_sound's domain.
func isFalVideoSFXModel(model FalModel) bool {
	id := strings.ToLower(model.ID)
	for _, marker := range []string{"sfx", "sound-effect", "sound effect", "foley", "mmaudio"} {
		if strings.Contains(id, marker) {
			return true
		}
	}
	for _, tag := range model.Tags {
		tag = strings.ToLower(tag)
		for _, marker := range []string{"sfx", "sound-effect", "sound effect", "foley"} {
			if strings.Contains(tag, marker) {
				return true
			}
		}
	}
	return false
}

// ListFalSoundEffectsVideoModels returns fal's video sound-effects catalog for
// the Settings video-sound-effects-model picker (the add_sound_effects tool's
// endpoint) — models that watch an attached clip and generate a synchronized
// soundtrack for it. Mirelo's sfx2.0 family spans two categories (the
// video-to-video endpoint returns the clip with the sound mixed in, the
// video-to-audio one the standalone soundtrack), so both are fetched, merged,
// and deduped by endpoint id — the lipsync listers' two-category pattern —
// kept only when isFalVideoSFXModel matches.
func (a *App) ListFalSoundEffectsVideoModels() ([]FalModel, error) {
	merged := []FalModel{}
	seen := map[string]bool{}
	for _, category := range []string{falVideoToVideoCategory, falVideoToAudioCategory} {
		models, err := a.cachedFalCategoryModels(category)
		if err != nil {
			// One category failing (a rate-limited request) keeps the
			// other's matches; only a total failure degrades, below.
			continue
		}
		for _, model := range models {
			if model.ID == "" || seen[model.ID] || !isFalVideoSFXModel(model) {
				continue
			}
			seen[model.ID] = true
			merged = append(merged, model)
		}
	}
	if len(merged) == 0 {
		// A failed (rate-limited) or reshuffled catalog keeps the mapped
		// default — the picker stays openable.
		merged = append(merged, FalModel{ID: defaultFalSoundEffectsVideoModel})
	}
	return merged, nil
}

// isFalVideoExtendModel reports whether a fal catalog entry is a video-extend
// endpoint — one that continues an existing clip into a longer one (Veo extend,
// ltx extend, pixverse extend, etc.). fal files these under the broad
// video-to-video category alongside lip-sync and other transforms, so the id and
// tags are checked for "extend" — and for "continue", the same capability under
// the naming MiniMax's H3 continuation endpoints use (continue-video rather than
// extend-video). Mirrors isFalLipsyncModel / isFalUpscaleModel.
func isFalVideoExtendModel(model FalModel) bool {
	id := strings.ToLower(model.ID)
	if strings.Contains(id, "extend") || strings.Contains(id, "continue") {
		return true
	}
	for _, tag := range model.Tags {
		tag = strings.ToLower(tag)
		if strings.Contains(tag, "extend") || strings.Contains(tag, "continue") {
			return true
		}
	}
	return false
}

// ListFalVideoExtendModels returns fal's video-extend catalog for the Settings
// video-extend-model picker — endpoints that continue an attached video clip
// into a longer one. fal files these under the video-to-video category, fetched
// and kept only when isFalVideoExtendModel matches. Shares its category with
// ListFalLipsyncVideoModels and ListFalVideoMotionModels; the three filters
// (extend vs lipsync vs motion) partition it.
func (a *App) ListFalVideoExtendModels() ([]FalModel, error) {
	models, err := a.cachedFalCategoryModels(falVideoToVideoCategory)
	if err != nil {
		return nil, err
	}
	extend := make([]FalModel, 0, len(models))
	for _, model := range models {
		if isFalVideoExtendModel(model) {
			extend = append(extend, model)
		}
	}
	return extend, nil
}

// isFalVideoMotionModel reports whether a fal catalog entry is a motion-control
// endpoint — one that transfers an attached video's motion onto an attached
// image's subject (Kling motion-control). fal files these under the broad
// video-to-video category alongside extend and lip-sync, so the id and tags are
// checked for "motion". Mirrors isFalVideoExtendModel / isFalLipsyncModel.
func isFalVideoMotionModel(model FalModel) bool {
	if strings.Contains(strings.ToLower(model.ID), "motion") {
		return true
	}
	for _, tag := range model.Tags {
		if strings.Contains(strings.ToLower(tag), "motion") {
			return true
		}
	}
	return false
}

// ListFalVideoMotionModels returns fal's motion-control catalog for the
// Settings motion-control-model picker — endpoints that apply an attached
// video's motion to an attached image's subject, selected when the user attaches
// both. fal files these under the video-to-video category, fetched and kept only
// when isFalVideoMotionModel matches.
func (a *App) ListFalVideoMotionModels() ([]FalModel, error) {
	models, err := a.cachedFalCategoryModels(falVideoToVideoCategory)
	if err != nil {
		return nil, err
	}
	motion := make([]FalModel, 0, len(models))
	for _, model := range models {
		if isFalVideoMotionModel(model) {
			motion = append(motion, model)
		}
	}
	return motion, nil
}

// isFalVideoUpscaleModel reports whether a fal catalog entry is a video
// upscaler — one that raises an attached clip's resolution (RealESRGAN
// per-frame, Topaz, Crystal, SeedVR). fal files these under the broad
// video-to-video category alongside extend, lipsync, motion, and reframe, so
// the id and tags are checked for "upscal" — the same marker isFalUpscaleModel
// uses on the image side. Together the six predicates partition video-to-video.
func isFalVideoUpscaleModel(model FalModel) bool {
	if strings.Contains(strings.ToLower(model.ID), "upscal") {
		return true
	}
	for _, tag := range model.Tags {
		if strings.Contains(strings.ToLower(tag), "upscal") {
			return true
		}
	}
	return false
}

// ListFalVideoUpscaleModels returns fal's video-upscaler catalog for the
// Settings video-upscale-model picker (the upscale_video tool's endpoint) —
// endpoints that raise an attached video's resolution. fal files these under the
// video-to-video category, fetched and kept only when isFalVideoUpscaleModel
// matches. Shares its category with the extend/lipsync/motion/reframe/restyle
// pickers; the six filters partition it.
func (a *App) ListFalVideoUpscaleModels() ([]FalModel, error) {
	models, err := a.cachedFalCategoryModels(falVideoToVideoCategory)
	if err != nil {
		return nil, err
	}
	upscale := make([]FalModel, 0, len(models))
	for _, model := range models {
		if isFalVideoUpscaleModel(model) {
			upscale = append(upscale, model)
		}
	}
	return upscale, nil
}

// isFalVideoReframeModel reports whether a fal catalog entry is a generative
// video reframer — one that converts an attached clip to a new aspect ratio by
// outpainting the added canvas (LTX-2.3 Reframe, Luma Ray 2 / Ray 3.2 reframe)
// rather than cropping or upscaling it. fal files these under the broad
// video-to-video category, so the id and tags are checked for "reframe" —
// alongside the extend/lipsync/motion/upscale markers, the six predicates
// partition video-to-video.
func isFalVideoReframeModel(model FalModel) bool {
	if strings.Contains(strings.ToLower(model.ID), "reframe") {
		return true
	}
	for _, tag := range model.Tags {
		if strings.Contains(strings.ToLower(tag), "reframe") {
			return true
		}
	}
	return false
}

// ListFalVideoReframeModels returns fal's video-reframe catalog for the
// Settings video-reframe-model picker (the reframe_video tool's endpoint) —
// endpoints that generatively convert an attached clip to a new aspect ratio.
// fal files these under the video-to-video category, fetched and kept only when
// isFalVideoReframeModel matches. Shares its category with the
// extend/lipsync/motion/upscale/restyle pickers; the six filters partition it.
func (a *App) ListFalVideoReframeModels() ([]FalModel, error) {
	models, err := a.cachedFalCategoryModels(falVideoToVideoCategory)
	if err != nil {
		return nil, err
	}
	reframe := make([]FalModel, 0, len(models))
	for _, model := range models {
		if isFalVideoReframeModel(model) {
			reframe = append(reframe, model)
		}
	}
	return reframe, nil
}

// falVideoRestyleMarkers are the id-only markers of a general prompt-driven
// restyle endpoint — one that re-renders an attached clip under a text
// instruction (Kling o3 video-to-video/edit, Wan edit-video, Ray modify, Sora
// remix, the open-weight */video-to-video family). Matched on the id, never
// the tags: fal's catalog tags are noisy across this category (unrelated
// entries share "stylized, transform"), while the ids are precise.
var falVideoRestyleMarkers = []string{
	"video-to-video", "v2v", "edit-video", "video-edit", "restyle", "stylize", "style-transfer", "modify", "remix",
}

// falVideoOtherFamilyMarkers are the id/tags markers of the video-to-video
// category entries that are NOT general restyle endpoints: the five families
// the other pickers partition (extend, lipsync, motion, upscale, reframe) plus
// the utility transforms (background removal, inpainting, depth/pose control,
// interpolation, relighting, sound...). A restyle candidate carrying any of
// these belongs to that family instead.
var falVideoOtherFamilyMarkers = []string{
	"extend", "lipsync", "motion", "upscal", "reframe",
	"audio", "sound", "sfx", "music", "talk",
	"background", "removal", "erase", "subtitle", "caption",
	"inpaint", "outpaint", "depth", "pose", "interpolat", "rife",
	"denoise", "deblur", "colorize", "colorization", "hdr", "relight", "recamera",
	"sam-", "segment", "mask", "ffmpeg", "trim", "reverse", "blend",
	"vton", "dreamactor", "realtime", "webrtc", "green-screen",
	"clean-plate", "day-to-night", "water-simulation", "decompress", "sdr",
}

// isFalVideoRestyleModel reports whether a fal catalog entry is a general
// video restyler — one that re-renders an attached clip under a prompt while
// keeping its motion, rather than one of the five partitioned families
// (extend, lipsync, motion, upscale, reframe) or a utility transform. The
// positive markers are id-only (catalog tags are noisy here); the exclusion
// markers check id and tags, mirroring the five sibling predicates so together
// the six filters partition fal's video-to-video category.
func isFalVideoRestyleModel(model FalModel) bool {
	id := strings.ToLower(model.ID)
	for _, marker := range falVideoOtherFamilyMarkers {
		if strings.Contains(id, marker) {
			return false
		}
	}
	for _, tag := range model.Tags {
		lower := strings.ToLower(tag)
		for _, marker := range falVideoOtherFamilyMarkers {
			if strings.Contains(lower, marker) {
				return false
			}
		}
	}
	for _, marker := range falVideoRestyleMarkers {
		if strings.Contains(id, marker) {
			return true
		}
	}
	return false
}

// ListFalVideoRestyleModels returns fal's video-restyle catalog for the
// Settings video-restyle-model picker (the restyle_video tool's endpoint) —
// endpoints that re-render an attached clip under a prompt. fal files these
// under the video-to-video category, fetched and kept only when
// isFalVideoRestyleModel matches. Shares its category with the
// extend/lipsync/motion/upscale/reframe pickers; the six filters partition it.
func (a *App) ListFalVideoRestyleModels() ([]FalModel, error) {
	models, err := a.cachedFalCategoryModels(falVideoToVideoCategory)
	if err != nil {
		return nil, err
	}
	restyle := make([]FalModel, 0, len(models))
	for _, model := range models {
		if isFalVideoRestyleModel(model) {
			restyle = append(restyle, model)
		}
	}
	return restyle, nil
}

// isFalSpeechModel reports whether a fal catalog entry is a text-to-speech
// endpoint even though fal files it under the broad text-to-audio category —
// fal's text-to-speech and text-to-audio categories are disjoint, and
// endpoints like elevenlabs/tts/*, kokoro/*, gemini-tts, f5-tts, csm, zonos,
// and elevenlabs text-to-dialogue land in text-to-audio with at best a
// "speech"-family tag. Both audio pickers consult this predicate: the speech
// lister merges matches in (the default elevenlabs TTS model is itself filed
// only under text-to-audio), and the sound-effects lister filters them out.
// Mirrors isFalLipsyncModel / isFalUpscaleModel's defensive id+tags matching.
func isFalSpeechModel(model FalModel) bool {
	id := strings.ToLower(model.ID)
	for _, marker := range []string{"tts", "speech", "dialogue"} {
		if strings.Contains(id, marker) {
			return true
		}
	}
	for _, tag := range model.Tags {
		lower := strings.ToLower(tag)
		if strings.Contains(lower, "speech") || strings.Contains(lower, "voice cloning") || strings.Contains(lower, "voice-clone") || strings.Contains(lower, "conversational") {
			return true
		}
	}
	return false
}

// ListFalSpeechModels returns fal's text-to-speech catalog for the Settings
// speech-model picker (the generate_speech tool's endpoint). fal files some
// TTS endpoints only under the broader text-to-audio category (elevenlabs/tts,
// kokoro, zonos, ...), so that category is fetched too and the
// isFalSpeechModel matches are merged in — without it, the default speech
// model would be missing from its own picker. Deduped by endpoint id.
func (a *App) ListFalSpeechModels() ([]FalModel, error) {
	speech, err := a.cachedFalCategoryModels(falTextToSpeechCategory)
	if err != nil {
		// Catalog failure keeps the mapped defaults: the speech and
		// voice-clone pickers (which share this list) open with a short
		// static pair instead of rendering no dropdown at all — fal's
		// /v1/models endpoint rate-limits the settings screen's catalog
		// burst, and the audio listers sit at the tail of it.
		return falAudioFallback(), nil
	}
	audio, err := a.cachedFalCategoryModels(falTextToAudioCategory)
	if err != nil {
		// The speech category may still have served (cache or stale copy) —
		// merge the mapped defaults into it rather than discarding the walk.
		audio = falAudioFallback()
	}

	merged := make([]FalModel, 0, len(speech)+len(audio))
	seen := map[string]bool{}
	for _, model := range speech {
		if model.ID == "" || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		merged = append(merged, model)
	}
	for _, model := range audio {
		if model.ID == "" || seen[model.ID] || !isFalSpeechModel(model) {
			continue
		}
		seen[model.ID] = true
		merged = append(merged, model)
	}
	return merged, nil
}

// falAudioFallback is the static pair ListFalSpeechModels degrades to when a
// category fetch fails: the speech and voice-clone pickers share this list,
// and each needs its own mapped default in it to stay openable.
func falAudioFallback() []FalModel {
	return []FalModel{
		{ID: defaultFalAudioModel},
		{ID: defaultFalAudioCloneModel},
	}
}

// isFalVoiceDesignModel reports whether a fal catalog entry is a voice-design
// endpoint — one that synthesizes a NEW voice from a written description
// (MiniMax voice-design, Qwen 3 TTS voice-design, Kling create-voice), rather
// than speaking with a preset or cloned voice. fal files them under the
// text-to-speech category (Kling's under audio-to-audio), so the id is checked
// for the design/create-voice markers. Mirrors isFalAudioExtendModel's
// defensive id+tags matching.
func isFalVoiceDesignModel(model FalModel) bool {
	id := strings.ToLower(model.ID)
	for _, marker := range []string{"voice-design", "voice_design", "create-voice"} {
		if strings.Contains(id, marker) {
			return true
		}
	}
	return false
}

// ListFalVoiceDesignModels returns fal's voice-design catalog for the Settings
// voice-design-model picker (the generate_voice tool's endpoint), fetched from
// the text-to-speech category and kept only when isFalVoiceDesignModel
// matches. The mapped default is always included even when the catalog fetch
// or filter misses it (a catalog re-shuffle then degrades to a short static
// list instead of an empty picker).
func (a *App) ListFalVoiceDesignModels() ([]FalModel, error) {
	design := []FalModel{{ID: defaultFalVoiceDesignModel}}
	seen := map[string]bool{defaultFalVoiceDesignModel: true}
	models, err := a.cachedFalCategoryModels(falTextToSpeechCategory)
	if err != nil {
		// Catalog failure keeps the static default — the picker stays usable.
		return design, nil
	}
	for _, model := range models {
		if model.ID == "" || seen[model.ID] || !isFalVoiceDesignModel(model) {
			continue
		}
		seen[model.ID] = true
		design = append(design, model)
	}
	return design, nil
}

// ListFalSoundEffectModels returns fal's text-to-audio catalog (music and
// sound effects) for the Settings sound-effects-model picker (the
// generate_sound tool's endpoint), with speech endpoints filtered out — fal
// files TTS models like elevenlabs/tts/* and kokoro/* under this same
// category, and they don't belong behind a "music & sound effects" label.
func (a *App) ListFalSoundEffectModels() ([]FalModel, error) {
	models, err := a.cachedFalCategoryModels(falTextToAudioCategory)
	if err != nil {
		// Catalog failure keeps the mapped default — the picker stays
		// openable (the speech-list failure mode: fal's rate limit lands on
		// the audio listers last).
		return []FalModel{{ID: defaultFalSoundEffectsModel}}, nil
	}
	sound := make([]FalModel, 0, len(models))
	for _, model := range models {
		if !isFalSpeechModel(model) {
			sound = append(sound, model)
		}
	}
	return sound, nil
}

// ListFalTranscribeModels returns fal's speech-to-text catalog for the Settings
// transcription-model picker. Single category (speech-to-text), like the other
// audio-family pickers.
func (a *App) ListFalTranscribeModels() ([]FalModel, error) {
	models, err := a.cachedFalCategoryModels(falSpeechToTextCategory)
	if err != nil {
		// Catalog failure keeps the mapped default — the picker stays
		// openable (the audio listers sit at the tail of fal's rate-limited
		// catalog burst).
		return []FalModel{{ID: defaultFalTranscribeModel}}, nil
	}
	return models, nil
}

// DetectLocalTools reports the locally installed CLI media tools (whisper
// today — see local_tools.go) for the Settings Transcription section: which
// binaries were found, where, and which transcription provider the current
// config resolves to. overrides carries unsaved binary overrides keyed by
// tool id ("whisper") so the UI can reflect an edit before it is persisted.
// Detection is LookPath-only — no process is spawned — so this is cheap to
// call on every settings render.
func (a *App) DetectLocalTools(overrides LocalToolOverrides) (LocalToolsReport, error) {
	config, err := loadAppConfig()
	if err != nil {
		return LocalToolsReport{}, err
	}
	return detectLocalTools(config, overrides), nil
}

// isFalAudioExtendModel reports whether a fal catalog entry is an audio-extend
// endpoint — one that continues an existing clip with generated audio
// (stable-audio inpaint/outpaint, ace-step audio-outpaint, sonauto extend).
// fal files them under the audio-to-audio category alongside unrelated
// transforms, so the id is checked for the extend/outpaint/inpaint keywords.
// Mirrors isFalVideoExtendModel.
func isFalAudioExtendModel(model FalModel) bool {
	id := strings.ToLower(model.ID)
	for _, keyword := range []string{"extend", "outpaint", "inpaint"} {
		if strings.Contains(id, keyword) {
			return true
		}
	}
	for _, tag := range model.Tags {
		lower := strings.ToLower(tag)
		for _, keyword := range []string{"extend", "outpaint", "inpaint"} {
			if strings.Contains(lower, keyword) {
				return true
			}
		}
	}
	return false
}

// ListFalAudioExtendModels returns fal's audio-extend catalog for the Settings
// audio-extend-model picker (the extend_audio tool's default endpoint),
// fetched from the audio-to-audio category and kept only when
// isFalAudioExtendModel matches. The three mapped endpoints are always
// included even when the catalog fetch or filter misses them (a catalog
// re-shuffle then degrades to a short static list instead of an empty picker).
func (a *App) ListFalAudioExtendModels() ([]FalModel, error) {
	extend := []FalModel{
		{ID: defaultFalAudioExtendModel},
		{ID: "fal-ai/ace-step/audio-outpaint"},
		{ID: "sonauto/v2/extend"},
	}
	seen := map[string]bool{}
	for _, model := range extend {
		seen[model.ID] = true
	}
	models, err := a.cachedFalCategoryModels(falAudioToAudioCategory)
	if err != nil {
		// Catalog failure keeps the static three — the picker stays usable.
		return extend, nil
	}
	for _, model := range models {
		if model.ID == "" || seen[model.ID] || !isFalAudioExtendModel(model) {
			continue
		}
		seen[model.ID] = true
		extend = append(extend, model)
	}
	return extend, nil
}

// resolvedPrimaryModelAndProvider returns which model/provider the primary
// chat role should use when a request doesn't specify one explicitly.
func (a *App) resolvedPrimaryModelAndProvider(config AppConfig) (model, provider string) {
	provider = strings.TrimSpace(config.Models.PrimaryProvider)
	if provider != "openrouter" && provider != "openai-compatible" {
		provider = "ollama"
	}
	if provider == "openrouter" {
		return strings.TrimSpace(config.Providers.OpenRouter.Primary), provider
	}
	if provider == "openai-compatible" {
		return strings.TrimSpace(config.Providers.OpenAICompatible.Primary), provider
	}
	return strings.TrimSpace(config.Providers.Ollama.Models.Primary), provider
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

// currentConversationSchemaVersion is bumped whenever HistoryConversation gains
// a field or changes shape. loadForAppend backfills older records up to it.
const currentConversationSchemaVersion = 2

// defaultImageAspectRatio / defaultImageSizePreset are the defaults for the
// image-generation Settings dropdowns. The size preset is a resolution tier
// ("1k", "2k", "4k") — pixels are derived from it at call time (see
// imageSizeForPresetAndRatio in tools_registry.go) and fal models that speak
// tiers natively are told the tier itself (see resolveImageBody), so config
// carries the user's choice rather than raw pixel values.
const (
	defaultImageAspectRatio = "1:1"
	defaultImageSizePreset  = "1k"
)

// resolveTurnWorkspace returns the filesystem-tool root a chat turn must run
// against. The workspace is IMMUTABLE per conversation:
//
//   - Turn 1 (no ConversationID): the request's Workspace wins if set, else
//     the configured default root. The resolved value is what gets persisted
//     onto the new conversation record by writePendingChatConversation.
//   - Turn 2+ (ConversationID set): the value already on the record wins. The
//     request's Workspace is ignored. Legacy SchemaVersion 1 records are
//     backfilled to the configured default.
//
// The caller overrides config.Tools.Filesystem.Root with this value before
// constructing the HarnessEngine, so the registry (cached per engine), the
// filesystem tool layer, the triage prompt, and the planner prompt all see the
// conversation's root through h.config — one override point, no per-callsite
// branching. Returns the empty string only if both the request and the
// configured default are empty, in which case the filesystem layer falls back
// to defaultAppConfig() at resolve time (see fs_tools.go resolvePath).
func resolveTurnWorkspace(config AppConfig, req ChatRequest) (string, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		if ws := strings.TrimSpace(req.Workspace); ws != "" {
			return ws, nil
		}
		return config.Tools.Filesystem.Root, nil
	}
	return readConversationWorkspace(config.Storage, req.ConversationID, config.Tools.Filesystem.Root)
}

// resolveTurnProject validates the project a NEW conversation is being assigned
// to. Like resolveTurnWorkspace it only reads the request on turn 1 — for an
// existing conversation the record's ProjectID is already the truth and the
// request field is ignored — but unlike the workspace there is nothing to
// resolve: an empty projectID means standalone, a non-empty one must reference
// an existing project or the turn fails before anything is persisted. A wrong
// ID is a UI bug (the composer only offers real projects), so failing loudly
// beats silently filing the conversation as standalone.
func resolveTurnProject(config AppConfig, req ChatRequest) (string, error) {
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" || strings.TrimSpace(req.ConversationID) != "" {
		return "", nil
	}
	if _, _, err := findProject(config.Storage, projectID); err != nil {
		return "", err
	}
	return projectID, nil
}

// projectNotesMaxChars bounds the instruction block injected into the planner
// prompt — the same order as plainTranscriptArtifactChars. A longer note is
// capped at injection, never rejected: the project record keeps everything
// the user wrote, the prompt keeps what fits.
const projectNotesMaxChars = 4000

// resolveTurnProjectNotes resolves the instruction block of the project the
// turn's conversation belongs to, so project-level conventions ("render all
// video requests at 9:16 in pixar style") reach the harness prompts through
// req.ProjectNotes. Turn 1 reads the request's ProjectID (already validated by
// resolveTurnProject); turn 2+ ignores it and reads the record's, like every
// other per-conversation fact. Unlike the workspace, the notes are re-read
// LIVE on every turn: the block is editable at the project level and a moved
// conversation must follow its new project (MoveConversationToProject), so
// pinning it at creation would freeze stale guidance. Fail-soft throughout —
// an unreadable record or a missing project yields no note rather than a
// failed turn (deleteProject removes member conversations, so the miss path is
// corruption or a racing delete, not a user state worth failing on).
func resolveTurnProjectNotes(config AppConfig, req ChatRequest) string {
	projectID := strings.TrimSpace(req.ProjectID)
	if strings.TrimSpace(req.ConversationID) != "" {
		projectID = readConversationProjectID(config.Storage, req.ConversationID)
	}
	if projectID == "" {
		return ""
	}
	project, _, err := findProject(config.Storage, projectID)
	if err != nil {
		return ""
	}
	// The no-ellipsis variant: the cap is the whole block's budget, so the
	// injected section never grows a suffix beyond projectNotesMaxChars.
	return truncateRunesEllipsis(strings.TrimSpace(project.Notes), projectNotesMaxChars, "")
}

// readConversationProjectID loads a conversation record solely to read its
// project membership — the same second small JSON read pattern as
// readConversationWorkspace (loadForAppend reads the record again during the
// append). Empty on any failure: the caller treats a miss as "no project
// note", never an error.
func readConversationProjectID(storage ConfigStorage, conversationID string) string {
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ""
	}
	var conversation HistoryConversation
	if err := readJSONFile(path, &conversation); err != nil {
		return ""
	}
	return strings.TrimSpace(conversation.ProjectID)
}

// readConversationWorkspace loads a conversation record solely to read its
// workspace root. Legacy SchemaVersion 1 records (no Workspace) are backfilled
// to defaultWorkspace so a conversation resumed for the first time after the
// per-conversation-workspace upgrade resolves consistently. This does a second
// read of conversation.json that loadForAppend will read again during the
// append; both are small JSON reads at turn-start, not per-round.
func readConversationWorkspace(storage ConfigStorage, conversationID, defaultWorkspace string) (string, error) {
	path, err := findConversationPath(storage, conversationID)
	if err != nil {
		return "", err
	}
	var conversation HistoryConversation
	if err := readJSONFile(path, &conversation); err != nil {
		return "", err
	}
	if conversation.SchemaVersion < 2 && strings.TrimSpace(conversation.Workspace) == "" {
		return defaultWorkspace, nil
	}
	if ws := strings.TrimSpace(conversation.Workspace); ws != "" {
		return ws, nil
	}
	return defaultWorkspace, nil
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

func writeChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, title string, run HarnessRun) (string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", err
	}

	userPrompt := lastUserPrompt(req.Messages)
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            workspace.ID,
		Kind:          "chat",
		Title:         normalizeConversationTitle(title, userPrompt),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		Provider: HistoryProvider{
			ID:      "ollama",
			BaseURL: config.Providers.Ollama.BaseURL,
		},
		Defaults: HistoryDefaults{
			ChatModel: req.Model,
			System:    req.System,
		},
		Stats: HistoryConversationStats{
			TurnCount:     2,
			ArtifactCount: countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)}),
		},
		Workspace:      config.Tools.Filesystem.Root,
		ProjectID:      strings.TrimSpace(req.ProjectID),
		ModelOverrides: requestModelOverrides(req),
	}

	userTurn, assistantTurn, _, err := buildChatTurnPair(workspace.ID, 1, nowText, req, assistantContent, assistantThinking, model, provider, reason, tokens, workspace.ArtifactsDir, config.Storage, config, run, strings.TrimSpace(req.ProjectID))
	if err != nil {
		return "", err
	}

	if err := store.writeSnapshot(workspace, conversation, userTurn, assistantTurn); err != nil {
		return "", err
	}
	return workspace.ID, nil
}

// writePendingChatConversation also returns the persisted video attachments'
// hydrated "/atelier-artifact" URLs (in attachment order) so the streaming
// start result can hand the live transcript artifact srcs — the poster-frame
// swap the composer's data: URLs cannot carry.
func writePendingChatConversation(config AppConfig, req ChatRequest) (string, []string, []string, []string, error) {
	now := time.Now()
	nowText := now.Format(time.RFC3339)
	store := newHistoryStore(config.Storage)
	workspace, err := store.newWorkspace(now)
	if err != nil {
		return "", nil, nil, nil, err
	}

	userPrompt := lastUserPrompt(req.Messages)
	conversation := HistoryConversation{
		SchemaVersion: currentConversationSchemaVersion,
		ID:            workspace.ID,
		Kind:          "chat",
		Title:         titleFromPrompt(userPrompt),
		CreatedAt:     nowText,
		UpdatedAt:     nowText,
		Provider: HistoryProvider{
			ID:      "ollama",
			BaseURL: config.Providers.Ollama.BaseURL,
		},
		Defaults: HistoryDefaults{
			ChatModel: req.Model,
			System:    req.System,
		},
		Stats: HistoryConversationStats{
			TurnCount:     1,
			ArtifactCount: countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)}),
		},
		Workspace:      config.Tools.Filesystem.Root,
		ProjectID:      strings.TrimSpace(req.ProjectID),
		ModelOverrides: requestModelOverrides(req),
	}
	userTurn, videoURLs, subtitleURLs, markdownURLs, err := buildChatUserTurn(workspace.ID, 1, nowText, req, workspace.ArtifactsDir, config.Storage, config, strings.TrimSpace(req.ProjectID))
	if err != nil {
		return "", nil, nil, nil, err
	}

	if err := store.writeSnapshot(workspace, conversation, userTurn); err != nil {
		return "", nil, nil, nil, err
	}
	return workspace.ID, videoURLs, subtitleURLs, markdownURLs, nil
}

func buildChatTurnPair(conversationID string, firstTurnNumber int, createdAt string, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, artifactsDir string, storage ConfigStorage, config AppConfig, run HarnessRun, projectID string) (HistoryTurn, HistoryTurn, []string, error) {
	userTurn, _, _, _, err := buildChatUserTurn(conversationID, firstTurnNumber, createdAt, req, artifactsDir, storage, config, projectID)
	if err != nil {
		return HistoryTurn{}, HistoryTurn{}, nil, err
	}
	assistantTurn := buildChatAssistantTurn(conversationID, firstTurnNumber+1, createdAt, assistantContent, assistantThinking, model, provider, reason, tokens, run, "")
	return userTurn, assistantTurn, nil, nil
}

// buildChatUserTurn persists the user's turn. projectID is the conversation's
// project — req.ProjectID on the creation paths (turn 1, before the record
// exists) and the loaded record's ProjectID on appends — so @-mention
// resolution can widen into the library even on the very first turn, when the
// conversation being built is not yet on disk for getConversation to read.
func buildChatUserTurn(conversationID string, turnNumber int, createdAt string, req ChatRequest, artifactsDir string, storage ConfigStorage, config AppConfig, projectID string) (HistoryTurn, []string, []string, []string, error) {
	userContent, videoURLs, err := historyContentForMessage(lastUserMessage(req.Messages), artifactsDir, config)
	if err != nil {
		return HistoryTurn{}, nil, nil, nil, err
	}
	// Attached subtitles' live-render URLs, derived BEFORE the mention append
	// below: they map the composer attachments positionally (what the frontend
	// entry carries), not the whole turn content.
	subtitleURLs := subtitleAttachmentURLs(userContent, artifactsDir)
	// Attached markdown documents' URLs, derived the same way and for the same
	// reason (the Save binding reads artifact paths only).
	markdownURLs := markdownAttachmentURLs(userContent, artifactsDir)
	// @-mentioned assets are persisted as references to the existing artifact
	// entries — same ArtifactID and relative Path, no byte copy — so the next
	// turn's latest-wins history walk sees what the user cited rather than an
	// older artifact. Mentions of library assets from OTHER conversations
	// persist with an absolute Path (see resolveReferencedAssets). On a
	// brand-new conversation there is nothing local to resolve, but a project
	// context can still resolve library-wide mentions.
	if len(req.ReferencedAssetIDs) > 0 {
		userContent = append(userContent, resolveReferencedAssets(storage, conversationID, projectID, req.ReferencedAssetIDs).entries...)
	}
	userTurn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", turnNumber),
		ConversationID: conversationID,
		CreatedAt:      createdAt,
		Kind:           "chat",
		Role:           "user",
		Content:        userContent,
		Request: map[string]any{
			"model": req.Model,
		},
	}
	if req.Options != nil {
		userTurn.Request["options"] = req.Options
	}
	if req.Think != nil {
		userTurn.Request["think"] = req.Think
	}
	if selectedModel := strings.TrimSpace(req.SelectedModel); selectedModel != "" && selectedModel != req.Model {
		userTurn.Request["selectedModel"] = selectedModel
	}
	return userTurn, videoURLs, subtitleURLs, markdownURLs, nil
}

// subtitleAttachmentURLs maps a user turn's attached-subtitle content entries
// (the trc_<hex>.vtt/.srt artifacts writeSubtitleAttachment persisted) onto
// the "/atelier-artifact" URLs the live UI swaps the just-attached data URLs
// for — the positional counterpart of the returned video URLs. Mentioned
// entries (appended by the caller after this derivation) never ride it: they
// are not composer attachments.
func subtitleAttachmentURLs(contents []HistoryContent, artifactsDir string) []string {
	urls := []string{}
	for _, content := range contents {
		if subtitleArtifactExtension(content) == "" {
			continue
		}
		path := filepath.FromSlash(content.Path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(artifactsDir, filepath.Base(path))
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		urls = append(urls, artifactPrefix+abs)
	}
	return urls
}

// markdownAttachmentURLs is the document twin of subtitleAttachmentURLs: the
// just-persisted md_<hex>.md entries mapped onto their artifact URLs, so the
// live transcript can swap the composer's data URLs the moment the turn
// persists.
func markdownAttachmentURLs(contents []HistoryContent, artifactsDir string) []string {
	urls := []string{}
	for _, content := range contents {
		if markdownArtifactExtension(content) == "" {
			continue
		}
		path := filepath.FromSlash(content.Path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(artifactsDir, filepath.Base(path))
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		urls = append(urls, artifactPrefix+abs)
	}
	return urls
}

// harnessRunCostMicros totals a run's USD-millionth cost: every model-call
// step's server-billed cost plus every media generation's estimate. It
// mirrors how the frontend folds token usage (sum over steps, one row per
// model call) so providerResponse.costMicros and the UI's per-step rows can
// never disagree. Bookkeeping steps carry no cost, same as tokens.
func harnessRunCostMicros(run HarnessRun) int64 {
	var total int64
	for _, step := range run.Steps {
		total += step.CostMicros
		for _, activity := range step.Tools {
			total += activity.CostMicros
		}
	}
	return total
}

func buildChatAssistantTurn(conversationID string, turnNumber int, createdAt string, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun, errorText string) HistoryTurn {
	assistantContents := []HistoryContent{{Type: "text", Text: assistantContent}}
	if strings.TrimSpace(assistantThinking) != "" {
		assistantContents = append(assistantContents, HistoryContent{Type: "thinking", Text: assistantThinking})
	}
	providerResponse := map[string]any{
		"doneReason": reason,
		"harnessRun": run,
		"tokens":     tokens,
	}
	// The turn's money total rides beside the token total, derived from the
	// run (model-call steps + media activities) rather than threaded through
	// the streaming result — the run is the single ledger everything else
	// already renders from, and failed turns keep their burned cost too.
	if costMicros := harnessRunCostMicros(run); costMicros > 0 {
		providerResponse["costMicros"] = costMicros
	}
	if strings.TrimSpace(errorText) != "" {
		providerResponse["error"] = errorText
	}
	if run.Skill != nil {
		providerResponse["skill"] = run.Skill
	}
	return HistoryTurn{
		SchemaVersion:    1,
		ID:               fmt.Sprintf("turn_%06d", turnNumber),
		ConversationID:   conversationID,
		CreatedAt:        createdAt,
		Kind:             "chat",
		Role:             "assistant",
		Model:            model,
		Provider:         provider,
		Content:          assistantContents,
		ProviderResponse: providerResponse,
	}
}

func appendChatConversation(config AppConfig, req ChatRequest, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun) (string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return "", err
	}
	nowText := time.Now().Format(time.RFC3339)
	userTurn, assistantTurn, _, err := buildChatTurnPair(conversationID, loaded.NextTurnNumber, nowText, req, assistantContent, assistantThinking, model, provider, reason, tokens, loaded.ArtifactsDir, config.Storage, config, run, loaded.Conversation.ProjectID)
	if err != nil {
		return "", err
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount += 2
	loaded.Conversation.Stats.ArtifactCount += countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)})
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, userTurn); err != nil {
		return "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return "", err
	}
	return conversationID, nil
}

// appendChatUserTurn also returns the persisted video attachments' hydrated
// "/atelier-artifact" URLs (in attachment order) — see writePendingChatConversation.
func appendChatUserTurn(config AppConfig, req ChatRequest) (string, []string, []string, []string, error) {
	conversationID := strings.TrimSpace(req.ConversationID)
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return "", nil, nil, nil, err
	}
	nowText := time.Now().Format(time.RFC3339)
	userTurn, videoURLs, subtitleURLs, markdownURLs, err := buildChatUserTurn(conversationID, loaded.NextTurnNumber, nowText, req, loaded.ArtifactsDir, config.Storage, config, loaded.Conversation.ProjectID)
	if err != nil {
		return "", nil, nil, nil, err
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += countMessageAttachments([]ChatMessage{lastUserMessage(req.Messages)})
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return "", nil, nil, nil, err
	}
	if err := store.writeTurn(loaded.TurnsDir, userTurn); err != nil {
		return "", nil, nil, nil, err
	}
	return conversationID, videoURLs, subtitleURLs, markdownURLs, nil
}

func appendChatAssistantTurn(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, tokens int, run HarnessRun, errorText string) error {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return err
	}
	nowText := time.Now().Format(time.RFC3339)
	assistantTurn := buildChatAssistantTurn(conversationID, loaded.NextTurnNumber, nowText, assistantContent, assistantThinking, model, provider, reason, tokens, run, errorText)

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return err
	}
	return store.writeTurn(loaded.TurnsDir, assistantTurn)
}

// chatAssistantTurnMedia is the per-call piece a media-bearing assistant turn
// needs. build writes that turn's artifacts (any mix of images, videos,
// audios, transcripts) into artifactsDir, and returns the HistoryContent
// entries they become, the per-kind live-render URLs, and the tool metadata
// recorded on the saved turn. The shared append helper calls build after
// loading the conversation, since artifact writing needs the resolved
// ArtifactsDir.
type chatAssistantTurnMedia struct {
	build func(artifactsDir string) (chatAssistantTurnMediaOutput, error)
}

// chatTurnMedia carries every media kind a turn produced into the shared
// appender. Kinds the turn didn't produce stay empty; the builder writes only
// what is present, so any combination persists in one turn — which the local
// ffmpeg tools make reachable (a screenshot plus an extracted audio track in
// one turn, say) and the earlier one-kind-per-switch appenders would have
// dropped.
type chatTurnMedia struct {
	images   []string
	imageReq ImageGenerateRequest
	// imageRaw is the direct image-generation path's raw provider JSON,
	// recorded as rawCompact on the tool metadata.
	imageRaw             string
	videos               []ToolVideoFile
	videoReq             VideoGenerateRequest
	audios               []ToolAudioFile
	audioReq             AudioGenerateRequest
	transcripts          []ToolTranscriptFile
	transcriptModel      string
	transcriptTimestamps string
	// markdowns carries the document authoring pass's documents
	// (document_authoring.go): bytes persisted as md_ artifacts through
	// writeMarkdownAttachment, the same identity user-attached documents and
	// markdown-editor saves ride.
	markdowns []chatTurnMarkdown
}

// chatTurnMarkdown is one model-authored document staged for the turn save.
type chatTurnMarkdown struct {
	data      []byte
	extension string
}

// chatTurnMediaURLs holds the "/atelier-artifact" links per media kind; the
// live UI renders each kind in its own player, so they cannot share one list.
type chatTurnMediaURLs struct {
	videos      []string
	audios      []string
	transcripts []string
	markdowns   []string
}

// chatAssistantTurnMediaOutput is what a media build closure produced: the
// history content entries (all kinds, in order), the per-kind artifact URLs,
// and the providerResponse.tool metadata.
type chatAssistantTurnMediaOutput struct {
	contents []HistoryContent
	urls     chatTurnMediaURLs
	tool     map[string]any
}

// appendChatAssistantTurnWithMedia is the shared skeleton for persisting an
// assistant turn that produced media. It loads the conversation, delegates
// artifact writing + metadata assembly to media.build, then writes a single
// assistant turn whose ProviderResponse carries the tool metadata.
func appendChatAssistantTurnWithMedia(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, run HarnessRun, media chatAssistantTurnMedia) (chatTurnMediaURLs, error) {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return chatTurnMediaURLs{}, err
	}
	nowText := time.Now().Format(time.RFC3339)

	output, err := media.build(loaded.ArtifactsDir)
	if err != nil {
		return chatTurnMediaURLs{}, err
	}

	contents := []HistoryContent{{Type: "text", Text: assistantContent}}
	if strings.TrimSpace(assistantThinking) != "" {
		contents = append(contents, HistoryContent{Type: "thinking", Text: assistantThinking})
	}
	contents = append(contents, output.contents...)
	providerResponse := map[string]any{
		"doneReason": reason,
		"harnessRun": run,
		"tool":       output.tool,
	}
	// Same turn-total derivation as buildChatAssistantTurn: the run is the
	// ledger, and a media turn's dollar total rides beside the run just like a
	// text turn's (the frontend folds the run itself, but history stays
	// self-describing for inspection).
	if costMicros := harnessRunCostMicros(run); costMicros > 0 {
		providerResponse["costMicros"] = costMicros
	}
	assistantTurn := HistoryTurn{
		SchemaVersion:    1,
		ID:               fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID:   conversationID,
		CreatedAt:        nowText,
		Kind:             "chat",
		Role:             "assistant",
		Model:            model,
		Provider:         provider,
		Content:          contents,
		ProviderResponse: providerResponse,
	}

	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(output.contents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return chatTurnMediaURLs{}, err
	}
	if err := store.writeTurn(loaded.TurnsDir, assistantTurn); err != nil {
		return chatTurnMediaURLs{}, err
	}
	return output.urls, nil
}

// appendChatAssistantTurnWithTurnMedia persists an assistant turn that
// produced any combination of media kinds. Every kind present is written into
// the one turn — images, videos, audios, transcripts — with the tool metadata
// naming the dominant kind (video > audio > image > transcript) plus every
// kind's count. The single-kind appenders below are thin conveniences over
// this one.
func appendChatAssistantTurnWithTurnMedia(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, media chatTurnMedia, run HarnessRun) (chatTurnMediaURLs, error) {
	return appendChatAssistantTurnWithMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, run, chatAssistantTurnMedia{
		build: func(artifactsDir string) (chatAssistantTurnMediaOutput, error) {
			var contents []HistoryContent
			urls := chatTurnMediaURLs{}
			tool := map[string]any{}
			if len(media.videos) > 0 {
				videoContents, videoURLs, err := writeChatVideoArtifacts(config, artifactsDir, media.videos)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, videoContents...)
				urls.videos = videoURLs
				tool["name"] = "video_generation"
				tool["model"] = media.videoReq.Model
				tool["videoCount"] = len(videoContents)
			}
			if len(media.audios) > 0 {
				audioContents, audioURLs, err := writeChatAudioArtifacts(config, artifactsDir, media.audios)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, audioContents...)
				urls.audios = audioURLs
				if tool["name"] == nil {
					tool["name"] = "audio_generation"
					tool["model"] = media.audioReq.Model
				}
				tool["audioCount"] = len(audioContents)
			}
			if len(media.images) > 0 {
				imageContents, err := writeChatImageArtifacts(artifactsDir, media.imageReq, media.images)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, imageContents...)
				if tool["name"] == nil {
					tool["name"] = "image_generation"
					tool["model"] = media.imageReq.Model
					if media.imageRaw != "" {
						tool["rawCompact"] = media.imageRaw
					}
				}
				tool["imageCount"] = len(imageContents)
			}
			if len(media.transcripts) > 0 {
				transcriptContents, transcriptURLs, err := writeChatTranscriptArtifacts(config, artifactsDir, media.transcripts)
				if err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				contents = append(contents, transcriptContents...)
				urls.transcripts = transcriptURLs
				if tool["name"] == nil {
					tool["name"] = "audio_transcription"
					tool["model"] = media.transcriptModel
					tool["timestamps"] = media.transcriptTimestamps
				}
				tool["transcripts"] = len(transcriptContents)
			}
			if len(media.markdowns) > 0 {
				if err := os.MkdirAll(artifactsDir, 0755); err != nil {
					return chatAssistantTurnMediaOutput{}, err
				}
				absArtifactsDir, err := filepath.Abs(artifactsDir)
				if err != nil {
					absArtifactsDir = artifactsDir
				}
				for _, doc := range media.markdowns {
					content, err := writeMarkdownAttachment(artifactsDir, doc.data, doc.extension)
					if err != nil {
						return chatAssistantTurnMediaOutput{}, err
					}
					contents = append(contents, content)
					urls.markdowns = append(urls.markdowns, artifactPrefix+filepath.Join(absArtifactsDir, content.ArtifactID+doc.extension))
				}
				if tool["name"] == nil {
					tool["name"] = "document_authoring"
				}
				tool["documentCount"] = len(media.markdowns)
			}
			return chatAssistantTurnMediaOutput{contents: contents, urls: urls, tool: tool}, nil
		},
	})
}

func appendChatAssistantTurnWithImages(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, images []string, raw string, run HarnessRun, imageReq ImageGenerateRequest) error {
	_, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{images: images, imageReq: imageReq, imageRaw: raw}, run)
	return err
}

// appendChatAssistantTurnWithVideos persists an assistant turn that produced one
// or more generated videos (and, in the rare case a turn produced both, any
// images too). Video temp files are moved into the conversation's artifacts
// directory; the returned URLs are the "/atelier-artifact" links the live UI
// renders before the turn is reloaded from history.
func appendChatAssistantTurnWithVideos(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, images []string, imageReq ImageGenerateRequest, videos []ToolVideoFile, videoReq VideoGenerateRequest, run HarnessRun) ([]string, error) {
	urls, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{
		images: images, imageReq: imageReq, videos: videos, videoReq: videoReq,
	}, run)
	return urls.videos, err
}

// writeChatVideoArtifacts moves each generated video's temp file into the
// artifacts directory as vid_<hex>.<ext> and returns the history content
// entries plus the "/atelier-artifact" URLs the live UI renders. A temp file
// that can't be resolved to a video is skipped rather than aborting the whole
// turn save. Thin wrapper over the shared media-artifact writer.
func writeChatVideoArtifacts(config AppConfig, artifactsDir string, videos []ToolVideoFile) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(videos))
	for i, v := range videos {
		files[i] = mediaArtifactEntry{tempPath: v.TempPath, mimeType: v.MimeType}
	}
	return writeChatMediaArtifacts(config, artifactsDir, files, "vid", "video", videoExtensionForMediaType)
}

// writeChatMediaArtifacts is the shared body for moving generated video/audio
// temp files into a conversation's artifacts directory. Each file is named
// <kindTag>_<hex>.<ext> — a globally unique randomID, because artifact IDs
// double as filenames and asset references that must not collide across
// conversations (legacy records used per-conversation positional IDs like
// turn_000001_aud_000001). It returns the HistoryContent entries and the
// "/atelier-artifact" URLs the live UI renders before reload. A file with
// no temp path is skipped rather than aborting the whole turn save. The
// contentType ("video"/"audio") and extensionFn differ per media kind.
type mediaArtifactEntry struct {
	tempPath string
	mimeType string
}

func writeChatMediaArtifacts(config AppConfig, artifactsDir string, files []mediaArtifactEntry, kindTag, contentType string, extensionFn func(string) string) ([]HistoryContent, []string, error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, nil, err
	}
	absArtifactsDir, err := filepath.Abs(artifactsDir)
	if err != nil {
		return nil, nil, err
	}
	contents := make([]HistoryContent, 0, len(files))
	urls := make([]string, 0, len(files))
	for _, file := range files {
		tempPath := strings.TrimSpace(file.tempPath)
		if tempPath == "" {
			continue
		}
		extension := extensionFn(file.mimeType)
		artifactID := randomID(kindTag)
		filename := artifactID + extension
		destPath := filepath.Join(absArtifactsDir, filename)
		if err := moveFile(tempPath, destPath); err != nil {
			return nil, nil, err
		}
		if contentType == "video" {
			// Poster frame for the <video> poster/panel placeholder
			// (video_poster.go). Best-effort: a missing poster degrades the
			// thumbnail, never the save.
			generateVideoPoster(config, destPath)
		}
		contents = append(contents, HistoryContent{
			Type:       contentType,
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
		urls = append(urls, artifactPrefix+destPath)
	}
	return contents, urls, nil
}

// appendChatAssistantTurnWithAudios persists an assistant turn that produced one
// or more generated audio clips. Audio temp files are moved into the
// conversation's artifacts directory; the returned URLs are the
// "/atelier-artifact" links the live UI renders before reload.
func appendChatAssistantTurnWithAudios(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, audios []ToolAudioFile, audioReq AudioGenerateRequest, run HarnessRun) ([]string, error) {
	urls, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{
		audios: audios, audioReq: audioReq,
	}, run)
	return urls.audios, err
}

// writeChatAudioArtifacts moves each generated audio's temp file into the
// artifacts directory as aud_<hex>.<ext> and returns the history
// content entries plus the "/atelier-artifact" URLs the live UI renders. Thin
// wrapper over the shared media-artifact writer, mirroring writeChatVideoArtifacts.
func writeChatAudioArtifacts(config AppConfig, artifactsDir string, audios []ToolAudioFile) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(audios))
	for i, a := range audios {
		files[i] = mediaArtifactEntry{tempPath: a.TempPath, mimeType: a.MimeType}
	}
	return writeChatMediaArtifacts(config, artifactsDir, files, "aud", "audio", audioExtensionForMediaType)
}

// writeChatTranscriptArtifacts moves each transcript's temp file into the
// artifacts directory as trc_<hex>.<ext> and returns the history content
// entries plus the "/atelier-artifact" URLs the live UI renders. The extension
// follows the staged file's mime type — .vtt for the WebVTT rendering of a
// timestamped transcript, .txt for an oversized plain transcript. Thin wrapper
// over the shared media-artifact writer, mirroring writeChatAudioArtifacts.
func writeChatTranscriptArtifacts(config AppConfig, artifactsDir string, transcripts []ToolTranscriptFile) ([]HistoryContent, []string, error) {
	files := make([]mediaArtifactEntry, len(transcripts))
	for i, t := range transcripts {
		files[i] = mediaArtifactEntry{tempPath: t.TempPath, mimeType: t.MimeType}
	}
	return writeChatMediaArtifacts(config, artifactsDir, files, "trc", "transcript", transcriptExtensionForMimeType)
}

// transcriptExtensionForMimeType maps a staged transcript's mime type onto its
// artifact extension — .vtt for WebVTT, .srt for SubRip (convert_subtitles'
// output), .txt for anything else (the tool layer stages plain text with
// text/plain).
func transcriptExtensionForMimeType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "text/vtt":
		return ".vtt"
	case "application/x-subrip":
		return ".srt"
	default:
		return ".txt"
	}
}

// appendChatAssistantTurnWithTranscripts persists an assistant turn that
// produced one or more transcript artifacts (.vtt timestamped or .txt plain).
// Transcript temp files are moved into the conversation's artifacts directory
// as trc_<hex>.<ext>; the returned URLs are the "/atelier-artifact" links the
// live UI renders before the turn is reloaded from history.
func appendChatAssistantTurnWithTranscripts(config AppConfig, conversationID, assistantContent, assistantThinking, model, provider, reason string, transcripts []ToolTranscriptFile, transcriptModel, timestamps string, run HarnessRun) ([]string, error) {
	urls, err := appendChatAssistantTurnWithTurnMedia(config, conversationID, assistantContent, assistantThinking, model, provider, reason, chatTurnMedia{
		transcripts: transcripts, transcriptModel: transcriptModel, transcriptTimestamps: timestamps,
	}, run)
	return urls.transcripts, err
}

// moveFile relocates src to dst, falling back to a copy-and-delete when the two
// live on different filesystems (os.Rename fails with a cross-device error).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

func writeChatImageArtifacts(artifactsDir string, req ImageGenerateRequest, images []string) ([]HistoryContent, error) {
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return nil, err
	}
	imageContents := make([]HistoryContent, 0, len(images))
	for _, image := range images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			// Skip entries that aren't decodable image bytes (e.g. a stray http
			// URL that slipped past the harvest step) rather than aborting the
			// whole turn save and orphaning any artifacts already written.
			continue
		}
		artifactID := randomID("img")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, err
		}
		imageContents = append(imageContents, HistoryContent{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
			Width:      req.Width,
			Height:     req.Height,
		})
	}
	return imageContents, nil
}

const (
	// maxConversationList is the hard cap on how many conversations the sidebar
	// list returns. We intentionally never page beyond the most recent 100.
	maxConversationList = 100
	// listCandidateOverfetch is how many conversation.json files we shortlist by
	// file mtime (a cheap, no-parse proxy for recency) before parsing. The 50
	// slots of slack above maxConversationList absorb minor mtime/UpdatedAt skew
	// so the exact UpdatedAt re-sort below is not starved.
	listCandidateOverfetch = 150
)

type conversationCandidate struct {
	path  string
	mtime time.Time
}

// listConversations returns the most-recently-updated conversations, capped at
// maxConversationList. It avoids parsing every conversation.json on disk: it
// walks the tree collecting only (path, mtime) per conversation, shortlists the
// newest listCandidateOverfetch by mtime, parses just those, then orders the
// survivors by the exact UpdatedAt field and applies the cap.
func listConversations(storage ConfigStorage) ([]ConversationSummary, error) {
	root := filepath.Join(storage.History, "conversations")
	candidates := []conversationCandidate{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		// Skip soft-deleted conversations without parsing: deleteConversation
		// always writes a sibling tombstone.json. This keeps freshly-deleted
		// conversations (whose mtime is bumped to "now") from crowding real
		// entries out of the mtime shortlist below.
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), "tombstone.json")); statErr == nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		candidates = append(candidates, conversationCandidate{path: path, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ConversationSummary{}, nil
		}
		return nil, err
	}

	// Shortlist the newest candidates by mtime, then parse only those.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].mtime.Equal(candidates[j].mtime) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].mtime.After(candidates[j].mtime)
	})
	if len(candidates) > listCandidateOverfetch {
		candidates = candidates[:listCandidateOverfetch]
	}

	summaries := make([]ConversationSummary, 0, len(candidates))
	for _, candidate := range candidates {
		var conversation HistoryConversation
		if err := readJSONFile(candidate.path, &conversation); err != nil {
			return nil, err
		}
		// Belt-and-suspenders: a conversation may carry DeletedAt without a
		// tombstone (e.g. legacy records), so re-check after parsing.
		if conversation.DeletedAt != "" {
			continue
		}
		summaries = append(summaries, conversationSummaryFrom(conversation))
	}

	// Exact ordering by UpdatedAt (RFC3339 strings sort chronologically), then
	// apply the hard cap.
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt == summaries[j].UpdatedAt {
			return summaries[i].ID < summaries[j].ID
		}
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	if len(summaries) > maxConversationList {
		summaries = summaries[:maxConversationList]
	}
	return summaries, nil
}

func getConversation(storage ConfigStorage, conversationID string) (ConversationDetail, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ConversationDetail{}, err
	}
	return getConversationAt(conversationPath)
}

// getConversationAt is getConversation for a caller that already knows the
// conversation.json path (the library asset fold walks the tree and hands
// paths straight through, skipping the per-conversation find walk).
func getConversationAt(conversationPath string) (ConversationDetail, error) {
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return ConversationDetail{}, err
	}
	if conversation.DeletedAt != "" {
		return ConversationDetail{}, fmt.Errorf("conversation %s is deleted", conversation.ID)
	}

	turnsDir := filepath.Join(filepath.Dir(conversationPath), "turns")
	turns := []HistoryTurn{}
	err := filepath.WalkDir(turnsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		var turn HistoryTurn
		if err := readJSONFile(path, &turn); err != nil {
			return err
		}
		if turn.DeletedAt != "" {
			return nil
		}
		turn.Content = hydrateHistoryContent(filepath.Dir(conversationPath), turn.Content)
		turns = append(turns, turn)
		return nil
	})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ConversationDetail{}, err
		}
	}
	sort.Slice(turns, func(i, j int) bool {
		return turns[i].ID < turns[j].ID
	})
	return ConversationDetail{Conversation: conversation, Turns: turns}, nil
}

func deleteConversation(storage ConfigStorage, conversationID string) error {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return err
	}
	now := time.Now().Format(time.RFC3339)
	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return err
	}
	conversation.DeletedAt = now
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return err
	}
	tombstone := map[string]any{
		"schemaVersion":  1,
		"conversationId": conversationID,
		"deletedAt":      now,
		"reason":         "user_deleted",
	}
	return writeJSONFile(filepath.Join(filepath.Dir(conversationPath), "tombstone.json"), tombstone)
}

func purgeArchivedConversations(storage ConfigStorage) (PurgeArchivedResult, error) {
	root := filepath.Join(storage.History, "conversations")
	var archivedDirs []string
	deletedAssets := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		var conversation HistoryConversation
		if err := readJSONFile(path, &conversation); err != nil {
			return err
		}
		if conversation.DeletedAt != "" {
			archivedDirs = append(archivedDirs, filepath.Dir(path))
			deletedAssets += countFiles(filepath.Join(filepath.Dir(path), "artifacts"))
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PurgeArchivedResult{}, nil
		}
		return PurgeArchivedResult{}, err
	}

	for _, dir := range archivedDirs {
		if err := os.RemoveAll(dir); err != nil {
			return PurgeArchivedResult{}, err
		}
	}
	return PurgeArchivedResult{DeletedConversations: len(archivedDirs), DeletedAssets: deletedAssets}, nil
}

func countFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		count++
		return nil
	})
	return count
}

func updateConversationTitle(storage ConfigStorage, conversationID string, title string) (ConversationSummary, error) {
	conversationPath, err := findConversationPath(storage, conversationID)
	if err != nil {
		return ConversationSummary{}, err
	}

	var conversation HistoryConversation
	if err := readJSONFile(conversationPath, &conversation); err != nil {
		return ConversationSummary{}, err
	}
	if conversation.DeletedAt != "" {
		return ConversationSummary{}, fmt.Errorf("conversation %s is deleted", conversationID)
	}
	conversation.Title = normalizeConversationTitle(title, conversation.Title)
	conversation.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeJSONFile(conversationPath, conversation); err != nil {
		return ConversationSummary{}, err
	}
	return conversationSummaryFrom(conversation), nil
}

func findConversationPath(storage ConfigStorage, conversationID string) (string, error) {
	root := filepath.Join(storage.History, "conversations")
	// Conversation directories are named by ID (conversationDir), so the
	// lookup normally resolves with one glob + one file read. The tree walk
	// below stays as the fallback for records whose directory name is not
	// their ID and for IDs containing glob metacharacters.
	if matches, globErr := filepath.Glob(filepath.Join(root, "*", "*", conversationID, "conversation.json")); globErr == nil && len(matches) > 0 {
		var conversation HistoryConversation
		if readErr := readJSONFile(matches[0], &conversation); readErr == nil && conversation.ID == conversationID {
			return matches[0], nil
		}
	}
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(path) != "conversation.json" {
			return nil
		}
		var conversation HistoryConversation
		if err := readJSONFile(path, &conversation); err != nil {
			return err
		}
		if conversation.ID == conversationID {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("conversation %s not found", conversationID)
	}
	return found, nil
}

func conversationDir(storage ConfigStorage, createdAt time.Time, conversationID string) string {
	return filepath.Join(
		storage.History,
		"conversations",
		createdAt.Format("2006"),
		createdAt.Format("01"),
		conversationID,
	)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
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

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// hydrateHistoryContent resolves image content items to URLs the frontend can
// render. Instead of embedding the full base64 data URL (which can be several
// MB for generated images), it sets a relative URL that the Wails asset
// handler serves from disk. This avoids pushing large payloads through the
// JSON IPC boundary.
func hydrateHistoryContent(conversationDir string, contents []HistoryContent) []HistoryContent {
	hydrated := make([]HistoryContent, 0, len(contents))
	for _, content := range contents {
		// Image, video, audio, transcript, markdown, composition, and comic
		// artifacts are all stored on disk and served by the asset handler;
		// resolve their path to an /atelier-artifact URL. Library-asset
		// references carry absolute paths; local artifacts are relative to the
		// conversation directory.
		if (content.Type == "image" || content.Type == "video" || content.Type == "audio" || content.Type == "transcript" || content.Type == "markdown" || content.Type == "composition" || content.Type == "comic") && content.Path != "" && !strings.HasPrefix(content.Path, "data:") {
			resolved := filepath.FromSlash(content.Path)
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(conversationDir, resolved)
			}
			if _, err := os.Stat(resolved); err == nil {
				content.Text = "/atelier-artifact" + resolved
			}
		}
		hydrated = append(hydrated, content)
	}
	return hydrated
}

func randomID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func titleFromPrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	if prompt == "" {
		return "Untitled"
	}
	return compactString(prompt, 72)
}

func cleanGeneratedTitle(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "#")
	title = strings.TrimSpace(title)
	for _, prefix := range []string{"Title:", "title:", "Conversation title:", "conversation title:"} {
		title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
	}
	title = strings.Trim(title, "\"'`“”‘’")
	title = strings.Join(strings.Fields(title), " ")
	return compactString(title, 72)
}

func normalizeConversationTitle(title string, fallback string) string {
	title = cleanGeneratedTitle(title)
	if title != "" {
		return title
	}
	return titleFromPrompt(fallback)
}

func lastUserMessage(messages []ChatMessage) ChatMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return messages[index]
		}
	}
	if len(messages) == 0 {
		return ChatMessage{}
	}
	return messages[len(messages)-1]
}

// lastUserMessageIndex returns the index of the most recent user message, or -1
// if there is none. It is the index-returning counterpart to lastUserMessage
// for callers that need to mutate the message in place (e.g. attaching an
// image resolved from history onto the last user message before the final
// response).
func lastUserMessageIndex(messages []ChatMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return i
		}
	}
	return -1
}

func lastUserPrompt(messages []ChatMessage) string {
	return lastUserMessage(messages).Content
}

// readVideoArtifactAsDataURL resolves a persisted video artifact (referenced by
// relative Path in a HistoryContent entry) to its bytes on disk and re-encodes
// them as a base64 data URL — the shape AttachedVideos consumers (Veo extend,
// lip sync) expect. Sibling of readAudioArtifactAsDataURL: used by the harness
// to re-hydrate a prior turn's attached video when the current turn has no
// attachment, so video-dependent tools work across turns without forcing the
// user to re-attach on every message.
func readVideoArtifactAsDataURL(storage ConfigStorage, conversationID string, content HistoryContent) (string, error) {
	absPath, err := contentArtifactPath(storage, conversationID, content)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	if !isVideoBytes(data) {
		return "", fmt.Errorf("artifact %s is not a supported video file", content.Path)
	}
	mediaType := strings.TrimSpace(content.MimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "video/") {
		// DetectContentType can return "application/octet-stream" for some
		// video containers; fall back to mp4 so the data URL is well-formed.
		mediaType = "video/mp4"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// tempAudioFileAsDataURL reads a freshly-generated audio temp file (the
// TempPath/MimeType on a ToolAudioFile produced by generate_speech/generate_sound) and
// re-encodes it as a base64 data URL — the shape AttachedAudios consumers
// (transcribe_audio, lip_sync) expect. Sibling of readAudioArtifactAsDataURL
// for the in-turn path: the file is a temp file written by writeTempAudio, not
// a persisted conversation artifact, so there is no conversation-path
// resolution. A missing/unreadable or non-audio file returns ("", nil): an
// error here must not fail the tool batch — the caller simply leaves
// AttachedAudios empty and the downstream tool surfaces its own attachment
// error, matching today's behavior.
func tempAudioFileAsDataURL(tempPath, mimeType string) (string, error) {
	path := strings.TrimSpace(tempPath)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil // unreadable temp file — not fatal, see comment above
	}
	if !isAudioBytes(data) {
		return "", nil
	}
	mediaType := strings.TrimSpace(mimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "audio/") {
		mediaType = "audio/mpeg"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// tempVideoFileAsDataURL is the video sibling of tempAudioFileAsDataURL: reads
// a freshly-generated video temp file and re-encodes it as a data URL for
// AttachedVideos consumers (Veo extend, video-to-video lip sync). Same
// swallow-error contract: a bad temp file yields ("", nil), not a failure.
func tempVideoFileAsDataURL(tempPath, mimeType string) (string, error) {
	path := strings.TrimSpace(tempPath)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	if !isVideoBytes(data) {
		return "", nil
	}
	mediaType := strings.TrimSpace(mimeType)
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mediaType, "video/") {
		mediaType = "video/mp4"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// historyContentForMessage persists a message's attachments as artifacts and
// returns the content entries plus, for video attachments, their hydrated
// "/atelier-artifact" URLs in attachment order — the swap-in srcs the live
// transcript uses to replace the composer's data: URLs the moment the turn
// persists (a data URL can carry no poster; the artifact URL can, and it
// drops the multi-MB base64 payload from the UI state).
func historyContentForMessage(message ChatMessage, artifactsDir string, config AppConfig) ([]HistoryContent, []string, error) {
	contents := []HistoryContent{}
	videoURLs := []string{}
	if strings.TrimSpace(message.Content) != "" {
		contents = append(contents, HistoryContent{Type: "text", Text: message.Content})
	}
	if len(message.Images) > 0 || len(message.Audios) > 0 || len(message.Videos) > 0 || len(message.Subtitles) > 0 || len(message.Markdowns) > 0 {
		if err := os.MkdirAll(artifactsDir, 0755); err != nil {
			return nil, nil, err
		}
	}
	for _, image := range message.Images {
		data, extension, err := decodeImagePayload(image)
		if err != nil {
			return nil, nil, err
		}
		artifactID := randomID("img")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, nil, err
		}
		contents = append(contents, HistoryContent{
			Type:       "image",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
	}
	for _, audio := range message.Audios {
		// A staged upload is linked into the artifacts dir — no decode, no
		// second copy of a potentially very large payload; the staged entry
		// stays so the request's own payload keeps resolving for the tools
		// (media_upload.go).
		if stagedPath, isStaged := stagedUploadPath(audio, mediaUploadsDir(config.Storage)); isStaged {
			content, _, err := adoptStagedUpload(config, stagedPath, artifactsDir, "aud")
			if err != nil {
				return nil, nil, err
			}
			contents = append(contents, content)
			continue
		}
		data, extension, err := decodeAudioPayload(audio)
		if err != nil {
			return nil, nil, err
		}
		artifactID := randomID("aud")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, nil, err
		}
		contents = append(contents, HistoryContent{
			Type:       "audio",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
	}
	for _, video := range message.Videos {
		// Staged-upload adoption, same as audio above.
		if stagedPath, isStaged := stagedUploadPath(video, mediaUploadsDir(config.Storage)); isStaged {
			content, artifactPath, err := adoptStagedUpload(config, stagedPath, artifactsDir, "vid")
			if err != nil {
				return nil, nil, err
			}
			contents = append(contents, content)
			absArtifactPath, err := filepath.Abs(artifactPath)
			if err != nil {
				absArtifactPath = artifactPath
			}
			videoURLs = append(videoURLs, artifactPrefix+absArtifactPath)
			continue
		}
		data, extension, err := decodeVideoPayload(video)
		if err != nil {
			return nil, nil, err
		}
		artifactID := randomID("vid")
		filename := artifactID + extension
		artifactPath := filepath.Join(artifactsDir, filename)
		if err := os.WriteFile(artifactPath, data, 0644); err != nil {
			return nil, nil, err
		}
		// Poster frame beside an attached clip, same as generated ones
		// (video_poster.go). Best-effort — attachment persistence never
		// depends on a thumbnail extractor resolving.
		generateVideoPoster(config, artifactPath)
		absArtifactPath, err := filepath.Abs(artifactPath)
		if err != nil {
			absArtifactPath = artifactPath
		}
		videoURLs = append(videoURLs, artifactPrefix+absArtifactPath)
		contents = append(contents, HistoryContent{
			Type:       "video",
			ArtifactID: artifactID,
			Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
			MimeType:   mediaTypeForExtension(extension),
		})
	}
	for _, subtitle := range message.Subtitles {
		data, err := decodeSubtitlePayload(subtitle)
		if err != nil {
			return nil, nil, err
		}
		content, err := writeSubtitleAttachment(artifactsDir, data)
		if err != nil {
			return nil, nil, err
		}
		contents = append(contents, content)
	}
	for _, markdown := range message.Markdowns {
		data, mime, err := decodeMediaDataURL(markdown)
		if err != nil {
			return nil, nil, err
		}
		content, err := writeMarkdownAttachment(artifactsDir, data, markdownExtensionForMime(mime))
		if err != nil {
			return nil, nil, err
		}
		contents = append(contents, content)
	}
	if len(contents) == 0 {
		return []HistoryContent{{Type: "text", Text: ""}}, nil, nil
	}
	return contents, videoURLs, nil
}

// decodeSubtitlePayload reads an attached subtitle file's bytes from a data
// URL (or an /atelier-artifact path, the hydrated form a later turn can
// re-carry). decodeMediaDataURL is the shared decoder.
func decodeSubtitlePayload(subtitle string) ([]byte, error) {
	data, _, err := decodeMediaDataURL(subtitle)
	return data, err
}

// writeSubtitleAttachment persists subtitle bytes as a trc_<hex>.<ext>
// transcript artifact — the identity convert_subtitles reads, the chat chips
// render, and the assets panel lists. The bytes must parse as actual cue
// content (detectSubtitleFormat alone would accept any non-WebVTT bytes as
// SRT), and the extension follows the CONTENT, not the declared mime: a
// mislabeled upload still lands as the format its bytes actually are.
func writeSubtitleAttachment(artifactsDir string, data []byte) (HistoryContent, error) {
	_, format, _, err := parseSubtitles(data)
	if err != nil {
		return HistoryContent{}, fmt.Errorf("attached file is not VTT or SRT subtitle content: %w", err)
	}
	extension := map[string]string{subtitleFormatVTT: ".vtt", subtitleFormatSRT: ".srt"}[format]
	artifactID := randomID("trc")
	filename := artifactID + extension
	if err := os.WriteFile(filepath.Join(artifactsDir, filename), data, 0644); err != nil {
		return HistoryContent{}, err
	}
	return HistoryContent{
		Type:       "transcript",
		ArtifactID: artifactID,
		Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
		MimeType:   mediaTypeForExtension(extension),
	}, nil
}

// countMessageAttachments counts image, audio, video, subtitle, and markdown
// attachments across messages for conversation artifact stats. Audio and video
// are included so the artifact count reflects user-attached media, not just images.
func countMessageAttachments(messages []ChatMessage) int {
	count := 0
	for _, message := range messages {
		count += len(message.Images)
		count += len(message.Audios)
		count += len(message.Videos)
		count += len(message.Subtitles)
		count += len(message.Markdowns)
	}
	return count
}

func normalizeImagePayloads(images []string) []string {
	normalized := make([]string, 0, len(images))
	for _, image := range images {
		if payload := normalizeImagePayload(image); payload != "" {
			normalized = append(normalized, payload)
		}
	}
	return normalized
}

func normalizeImagePayload(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "data:image/") {
		if _, _, err := decodeImagePayload(value); err == nil {
			return value
		}
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	if strings.HasPrefix(value, "![") {
		start := strings.Index(value, "](")
		end := strings.LastIndex(value, ")")
		if start >= 0 && end > start+2 {
			return normalizeImagePayload(value[start+2 : end])
		}
	}
	if strings.Contains(value, "data:image/") {
		start := strings.Index(value, "data:image/")
		end := start
		for end < len(value) && !strings.ContainsRune(" \n\r\t\"')]", rune(value[end])) {
			end++
		}
		return normalizeImagePayload(value[start:end])
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err == nil && isImageBytes(data) {
		return "data:" + mediaTypeForExtension(imageExtensionForBytes(data)) + ";base64," + value
	}
	return ""
}

// imageExtensionForBytes sniffs image bytes and returns their file extension —
// the single magic-number table behind isImageBytes, bare-base64 payload
// persistence, and attachment normalization. Beyond the web formats it covers
// every container macOS sips reads (local_images.go): the HEIF family, AVIF,
// JPEG 2000, TIFF, and BMP. An ISO BMFF container (bytes 4-8 "ftyp") counts
// only when its major or a compatible brand is an image brand, so an attached
// mp4/mov is never mistaken for a HEIF photo.
func imageExtensionForBytes(data []byte) string {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return ".png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return ".jpg"
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return ".webp"
	}
	if len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))) {
		return ".gif"
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		// The brand table: HEIC/HEIF image and sequence brands, AVIF, and
		// JPEG 2000. The major brand sits at 8:12; compatible brands follow
		// the minor-version field at 16, four bytes apart.
		heifBrands := map[string]string{
			"heic": ".heic", "heix": ".heic", "heim": ".heic", "heis": ".heic",
			"hevc": ".heic", "hevx": ".heic", "hevm": ".heic", "hevs": ".heic",
			"mif1": ".heic", "mif2": ".heic", "msf1": ".heic",
			"avif": ".avif", "avis": ".avif",
			"jp2 ": ".jp2",
		}
		if extension, ok := heifBrands[string(data[8:12])]; ok {
			return extension
		}
		for offset := 16; offset+4 <= len(data); offset += 4 {
			if extension, ok := heifBrands[string(data[offset:offset+4])]; ok {
				return extension
			}
		}
	}
	if len(data) >= 4 && ((data[0] == 'I' && data[1] == 'I' && data[2] == 0x2a && data[3] == 0x00) ||
		(data[0] == 'M' && data[1] == 'M' && data[2] == 0x00 && data[3] == 0x2a)) {
		return ".tiff"
	}
	if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
		return ".bmp"
	}
	return ""
}

func isImageBytes(data []byte) bool {
	return imageExtensionForBytes(data) != ""
}

func collectImagesFromJSON(raw []byte) []string {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	var images []string
	walkJSONStrings(payload, func(value string) {
		if image := normalizeImagePayload(value); image != "" {
			images = append(images, image)
		}
	})
	return images
}

func walkJSONStrings(value any, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case []any:
		for _, item := range typed {
			walkJSONStrings(item, visit)
		}
	case map[string]any:
		for _, item := range typed {
			walkJSONStrings(item, visit)
		}
	}
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		deduped = append(deduped, value)
	}
	return deduped
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func compactRawResponse(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return compactString(string(raw), 1200)
	}
	redactLargeImageStrings(payload)
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return compactString(string(raw), 1200)
	}
	return compactString(string(data), 2000)
}

func redactLargeImageStrings(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok && normalizeImagePayload(text) != "" {
				typed[key] = fmt.Sprintf("<image data: %d chars>", len(text))
				continue
			}
			redactLargeImageStrings(item)
		}
	case []any:
		for index, item := range typed {
			if text, ok := item.(string); ok && normalizeImagePayload(text) != "" {
				typed[index] = fmt.Sprintf("<image data: %d chars>", len(text))
				continue
			}
			redactLargeImageStrings(item)
		}
	}
}

// compactString truncates value to at most maxLength runes, appending "..." when
// it shortens. It works in runes (not bytes) so a multi-byte UTF-8 sequence —
// common in non-ASCII titles and prompts — is never split, which would leave
// invalid UTF-8 in history and the model's input.
func compactString(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	return string(runes[:maxLength]) + "..."
}

func extensionForMediaType(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/heic", "image/heif":
		return ".heic"
	case "image/avif":
		return ".avif"
	case "image/jp2":
		return ".jp2"
	case "image/tiff", "image/tif":
		return ".tiff"
	case "image/bmp":
		return ".bmp"
	default:
		return ".png"
	}
}

func mediaTypeForExtension(extension string) string {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".heic", ".heif":
		return "image/heic"
	case ".avif":
		return "image/avif"
	case ".jp2":
		return "image/jp2"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".bmp":
		return "image/bmp"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		// Matroska: the staged-upload container allowlist keeps the .mkv
		// extension (media_upload.go), so the adopt path reads the same table —
		// an unknown mapping here would rename the artifact to .mp4 and stamp
		// the staged upload "image/png" (the table's default).
		return "video/x-matroska"
	case ".3gp":
		return "video/3gpp"
	case ".avi":
		return "video/x-msvideo"
	case ".ts":
		return "video/mp2t"
	case ".mpg", ".mpeg":
		return "video/mpeg"
	case ".flv":
		return "video/x-flv"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".m4a", ".aac":
		return "audio/mp4"
	case ".md":
		return "text/markdown"
	case ".fountain":
		return "text/x-fountain"
	case ".vtt":
		return "text/vtt"
	case ".srt":
		return "application/x-subrip"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	default:
		return "image/png"
	}
}

// audioExtensionForMediaType maps an audio MIME type to a file extension,
// defaulting to .mp3 (the format most fal audio endpoints return).
func audioExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/mp4", "audio/aac", "audio/x-m4a":
		return ".m4a"
	default:
		return ".mp3"
	}
}

// isAudioBytes reports whether data looks like a container an <audio> tag can
// play: MP3 (ID3 tag or MPEG frame sync), WAV/RIFF, OGG, FLAC, or MP4/M4A
// (ISO base media "ftyp"). Returns false otherwise so a non-audio download is
// rejected rather than written as a broken artifact.
func isAudioBytes(data []byte) bool {
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")) {
		return true
	}
	if len(data) >= 2 && data[0] == 0xFF && (data[1]&0xE0) == 0xE0 {
		return true // MPEG audio frame sync (MP3/AAC-ADTS)
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("fLaC")) {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return true // MP4/M4A audio container
	}
	return false
}

// videoExtensionForMediaType maps a video MIME type to a file extension. Unlike
// extensionForMediaType (image-oriented, defaults to .png), an unrecognized
// video type falls back to .mp4 — the format every fal video endpoint returns.
func videoExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "video/x-matroska", "video/mkv":
		// "video/mkv" is the MIME tag third-party Matroska UTIs declare on
		// macOS; x-matroska is the standard spelling.
		return ".mkv"
	case "video/x-msvideo":
		return ".avi"
	case "video/mp2t":
		return ".ts"
	case "video/mpeg":
		return ".mpg"
	case "video/x-flv":
		return ".flv"
	default:
		return ".mp4"
	}
}

// isVideoBytes reports whether data looks like one of the video containers the
// attachment pipeline admits (the staged-upload allowlist in media_upload.go
// and the frontend's videoExtensions mirror each other): MP4/MOV and 3GP (ISO
// base media, "ftyp" box at offset 4), WebM/Matroska (EBML magic), FLV ("FLV"
// magic), AVI (RIFF with "AVI " form type), MPEG program stream (.mpg/.mpeg,
// pack header), and MPEG transport stream (.ts, 0x47 packet sync — the M2TS
// variant shifts it four bytes in). Returns false otherwise so a stray
// non-video download is rejected rather than written as a broken artifact.
func isVideoBytes(data []byte) bool {
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return true
	}
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("FLV")) {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("AVI ")) {
		return true
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xBA}) {
		return true // MPEG program stream pack header
	}
	// Transport stream: 188-byte packets carry 0x47 at every header, so two
	// consecutive syncs identify it where a lone 0x47 could be anything.
	if len(data) >= 189 && data[0] == 0x47 && data[188] == 0x47 {
		return true
	}
	if len(data) >= 197 && data[4] == 0x47 && data[196] == 0x47 {
		return true // M2TS: 4-byte TP_extra_header before each packet
	}
	return false
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, invalid := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"} {
		value = strings.ReplaceAll(value, invalid, "-")
	}
	value = strings.Trim(value, ". ")
	return compactString(value, 80)
}
