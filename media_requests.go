package main

import (
	"strings"
)

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
