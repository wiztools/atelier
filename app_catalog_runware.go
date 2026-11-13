package main

// app_catalog_runware.go is the Runware Settings-picker catalog — the sibling
// of app_catalog_fal.go and app_catalog_replicate.go. The source is the same
// public docs index ListRunwareImageModels reads (no key needed to browse);
// the pickers filter it by the index's capability tags, and the audio and
// lipsync lists additionally check each candidate's published input schema —
// Runware's vocabulary is standardized (positivePrompt, inputs.video,
// inputs.audio, the speech object), so a schema check is a handful of
// property reads, the replicateSpeechSchemaServes recipe one dialect over.
// Every picker falls back to its schema-verified default when the filter
// comes back bare, so a dropdown never opens empty.

import (
	"context"
	"time"
)

// listRunwareCapabilityModels fetches the index and keeps the live entries
// whose capabilities include every named tag.
func (a *App) listRunwareCapabilityModels(capabilities ...string) ([]RunwareModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := newRunwareClient(a.client, "").ListModels(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]RunwareModel, 0, len(models))
	for _, m := range models {
		if runwareModelHasAllCapabilities(m, capabilities) {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

// runwareModelHasAllCapabilities requires every named tag (the single-tag
// helper runwareModelHasCapability accepts any-of).
func runwareModelHasAllCapabilities(model RunwareModel, capabilities []string) bool {
	for _, want := range capabilities {
		if !runwareModelHasCapability(model, want) {
			return false
		}
	}
	return true
}

// ListRunwareVideoModels returns the text-to-video models for the Settings
// video picker shown when runware is the VideoProvider.
func (a *App) ListRunwareVideoModels() ([]RunwareModel, error) {
	return a.listRunwareCapabilityModels("text-to-video")
}

// ListRunwareVideoImageModels returns the image-to-video models for the
// picker's second slot (animating an attached frame).
func (a *App) ListRunwareVideoImageModels() ([]RunwareModel, error) {
	return a.listRunwareCapabilityModels("image-to-video")
}

// ListRunwareVideoExtendModels returns the extend-capable video models for
// the extend slot — the index tags the models whose packaging continues an
// attached clip (Veo 3.1's inputs.video).
func (a *App) ListRunwareVideoExtendModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("extend", "text-to-video")
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		models = append(models, RunwareModel{ID: defaultRunwareVideoExtendModel})
	}
	return models, nil
}

// ListRunwareVideoUpscaleModels returns the video upscalers — the index tags
// them video-to-video + upscale (FLUX 3 Upscaler, the ByteDance/Bria
// enhancers).
func (a *App) ListRunwareVideoUpscaleModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("upscale", "video-to-video")
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		models = append(models, RunwareModel{ID: defaultRunwareVideoUpscaleModel})
	}
	return models, nil
}

// ListRunwareVideoRestyleModels returns the video-to-video editing models —
// the edit tag separates restylers (FLUX Video Edit, Kling) from plain
// video-to-video generators.
func (a *App) ListRunwareVideoRestyleModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("video-to-video", "edit")
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		models = append(models, RunwareModel{ID: defaultRunwareVideoRestyleModel})
	}
	return models, nil
}

// runwareSchemaCacheFor builds the picker-side schema cache — the same
// on-disk cache the gateway's generation paths read, so a picker's check
// warms what the call needs.
func (a *App) runwareSchemaCacheFor() (*SchemaCache, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	return newRunwareSchemaCache(a.client, config.Storage.Root), nil
}

// runwareSchemaServesSpeech reports whether a candidate's schema declares the
// nested speech object (speech.text string) the canonical speech request
// fills — Runware's TTS contract. A model without it (a music or SFX model
// tagged the same text-to-audio) is not drivable as speech.
func runwareSchemaServesSpeech(schema *ModelInputSchema) bool {
	if schema == nil {
		return false
	}
	prop, ok := schema.property("speech")
	if !ok || prop.Kind != schemaObject {
		return false
	}
	text, ok := prop.Nested["text"]
	return ok && text.Kind == schemaScalar && text.Type == "string"
}

// runwareSchemaServesVideoSFX reports whether a candidate's inputs declare a
// video field — the video-soundtrack contract (mirelo's inputs.video).
func runwareSchemaServesVideoSFX(schema *ModelInputSchema) bool {
	if schema == nil {
		return false
	}
	prop, ok := schema.property("inputs")
	if !ok || prop.Kind != schemaObject {
		return false
	}
	_, ok = prop.Nested["video"]
	return ok
}

// runwareSchemaServesLipsyncFace reports whether a candidate's inputs declare
// the audio field plus the named face field (image for a talking head, video
// for a re-sync) — both are required by every runware lipsync model.
func runwareSchemaServesLipsyncFace(schema *ModelInputSchema, faceKey string) bool {
	if schema == nil {
		return false
	}
	prop, ok := schema.property("inputs")
	if !ok || prop.Kind != schemaObject {
		return false
	}
	_, hasAudio := prop.Nested["audio"]
	_, hasFace := prop.Nested[faceKey]
	return hasAudio && hasFace
}

// ListRunwareSpeechModels returns the text-to-speech models for the
// generate_speech picker shown when runware is the SpeechProvider. The
// text-to-audio capability tag mixes in music and SFX models, so every
// candidate is schema-checked for the speech object — the
// replicateSpeechSchemaServes stance that only the schema can tell a speech
// model from a same-tagged neighbor. Bare falls back to the verified default.
func (a *App) ListRunwareSpeechModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("text-to-audio")
	if err != nil {
		return nil, err
	}
	cache, err := a.runwareSchemaCacheFor()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filtered := make([]RunwareModel, 0, len(models))
	for _, model := range models {
		if runwareSchemaServesSpeech(cache.Get(ctx, model.ID)) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, RunwareModel{ID: defaultRunwareSpeechModel})
	}
	return filtered, nil
}

// ListRunwareSoundEffectModels returns the text-to-audio models MINUS the
// speech ones for the generate_sound picker (music and sound effects). The
// schema check is the inverse of the speech picker's: a candidate serves
// when its schema declares a positive prompt and NOT the speech object.
func (a *App) ListRunwareSoundEffectModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("text-to-audio")
	if err != nil {
		return nil, err
	}
	cache, err := a.runwareSchemaCacheFor()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filtered := make([]RunwareModel, 0, len(models))
	for _, model := range models {
		schema := cache.Get(ctx, model.ID)
		if !runwareSchemaServesSpeech(schema) && schemaHasPositivePrompt(schema) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, RunwareModel{ID: defaultRunwareSoundEffectsModel})
	}
	return filtered, nil
}

// schemaHasPositivePrompt reports whether the parsed schema declares the
// top-level positivePrompt string — the text-driven contract every runware
// generation model carries.
func schemaHasPositivePrompt(schema *ModelInputSchema) bool {
	if schema == nil {
		return false
	}
	prop, ok := schema.property("positivePrompt")
	return ok && prop.Kind == schemaScalar && prop.Type == "string"
}

// ListRunwareSoundEffectsVideoModels returns the video-soundtrack models for
// the add_sound_effects picker shown when runware is the AudioProvider — the
// video-to-audio capability tag, schema-checked for the inputs.video field
// the canonical request fills.
func (a *App) ListRunwareSoundEffectsVideoModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("video-to-audio")
	if err != nil {
		return nil, err
	}
	cache, err := a.runwareSchemaCacheFor()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filtered := make([]RunwareModel, 0, len(models))
	for _, model := range models {
		if runwareSchemaServesVideoSFX(cache.Get(ctx, model.ID)) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, RunwareModel{ID: defaultRunwareSoundEffectsVideoModel})
	}
	return filtered, nil
}

// ListRunwareLipsyncImageModels returns the image-face lipsync models (a
// talking head from an attached image) — audio-to-video tagged, schema-checked
// for inputs.image + inputs.audio. Runware's image-face class is thin (VEED
// Fabric), so bare falls back to the verified default by name.
func (a *App) ListRunwareLipsyncImageModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("audio-to-video")
	if err != nil {
		return nil, err
	}
	return a.filterRunwareLipsyncModels(models, "image", defaultRunwareLipsyncImageModel), nil
}

// ListRunwareLipsyncVideoModels returns the video-face lipsync models
// (re-sync an attached clip) — the same tag, schema-checked for
// inputs.video + inputs.audio (KlingAI Lip-Sync, PixVerse).
func (a *App) ListRunwareLipsyncVideoModels() ([]RunwareModel, error) {
	models, err := a.listRunwareCapabilityModels("audio-to-video")
	if err != nil {
		return nil, err
	}
	return a.filterRunwareLipsyncModels(models, "video", defaultRunwareLipsyncVideoModel), nil
}

// filterRunwareLipsyncModels schema-checks a tagged candidate list for the
// named face input and falls back to the verified default when bare — the
// ListReplicateLipsyncModels recipe.
func (a *App) filterRunwareLipsyncModels(models []RunwareModel, faceKey, fallback string) []RunwareModel {
	cache, err := a.runwareSchemaCacheFor()
	if err != nil {
		return []RunwareModel{{ID: fallback}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	filtered := make([]RunwareModel, 0, len(models))
	for _, model := range models {
		if runwareSchemaServesLipsyncFace(cache.Get(ctx, model.ID), faceKey) {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, RunwareModel{ID: fallback})
	}
	return filtered
}

// ListRunwareVideoDurations returns the duration values the given Runware
// video model accepts, read straight from its schema's duration property —
// the ListFalVideoDurations contract with Runware's standardized names (no
// synonym walk: the field is literally "duration"). Empty (not an error) when
// the schema is unavailable or the model has no duration control, so the UI
// falls back to a generic option set.
func (a *App) ListRunwareVideoDurations(model string) ([]string, error) {
	config, err := loadAppConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	schema := newRunwareSchemaCache(a.client, config.Storage.Root).Get(ctx, model)
	if prop, ok := schema.property("duration"); ok {
		return durationOptionsForProperty(prop), nil
	}
	return []string{}, nil
}

// ListRunwareSoundEffectDurations is the audio sibling — the generate_sound
// duration picker under a runware AudioProvider (mirelo's 1–10s window).
func (a *App) ListRunwareSoundEffectDurations(model string) ([]string, error) {
	return a.ListRunwareVideoDurations(model)
}
