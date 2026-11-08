package main

import (
	"context"
	"strings"
	"time"
)

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
