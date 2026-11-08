package main

import (
	"context"
	"strings"
	"time"
)

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
