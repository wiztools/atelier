package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Overrides maps category → model-id → canonical → overrideEntry. An entry's
// Path is the native field name (or dot-path) to write to; an empty Path means
// the canonical param is explicitly unsupported (drop-with-notice). Kind, when
// set, forces the property's schemaKind independently of what the model's
// published schema declares — this is the escape hatch for endpoints whose
// runtime has drifted from their own OpenAPI schema (e.g. Kling image-to-video,
// whose schema declares a scalar image_url but whose runtime rejects anything
// but an image_urls array). Missing entries fall through to schema heuristics.
type Overrides struct {
	byCategory map[string]map[string]map[string]overrideEntry
}

// overrideEntry is the leaf value of an Overrides tree. Path is the native
// field name or dot-path; Kind is the OpenAPI-style type string ("array",
// "string", "integer", ...) used only when the field isn't found in the schema
// (see findNative's fabrication branch); Items is the element type for a forced
// array; MaxItems caps a forced array. A bare string override (the legacy
// on-disk form) unmarshals into {Path: <string>}.
type overrideEntry struct {
	Path     string
	Kind     string
	Items    string
	MaxItems int
}

func (o Overrides) lookup(category, model, canon string) (overrideEntry, bool) {
	models, ok := o.byCategory[category]
	if !ok {
		return overrideEntry{}, false
	}
	params, ok := models[model]
	if !ok {
		return overrideEntry{}, false
	}
	entry, ok := params[canon]
	return entry, ok
}

// builtinFalOverrides holds defaults for models the heuristics get wrong —
// primarily endpoints whose published OpenAPI schema disagrees with their
// runtime. Entries are added as such drift is discovered.
func builtinFalOverrides() Overrides {
	return Overrides{byCategory: map[string]map[string]map[string]overrideEntry{
		"audio": {},
		"image": {},
		// fal-ai/kling-video/v2/master/image-to-video: the published schema
		// declares image_url (a single string), but the runtime rejects it with
		// 422 "image_urls: Input should be a valid list" and demands an array.
		// Force the source image onto image_urls as a one-element list. When fal
		// fixes the schema this entry can be removed and pure-schema behavior
		// resumes. See conv_3232dd3836e50aa6402d8f51.
		"video": {
			"fal-ai/kling-video/v2/master/image-to-video": {
				"sourceImage": {Path: "image_urls", Kind: "array", Items: "string"},
			},
		},
		"lipsync": {},
	}}
}

// loadFalOverrides reads <storageRoot>/fal-overrides.json and merges it OVER the
// built-in defaults. A missing or malformed file yields the built-ins. Each leaf
// value accepts two shapes: a legacy bare string (the native field name, or ""
// to disable) or an object {"path","kind","items","maxItems"} that can also
// force the property's schemaKind — see overrideEntry.
func loadFalOverrides(storageRoot string) Overrides {
	ov := builtinFalOverrides()
	data, err := os.ReadFile(filepath.Join(storageRoot, "fal-overrides.json"))
	if err != nil {
		return ov
	}
	// Decode leaves as raw JSON so a bare string and an object both parse.
	var parsed map[string]map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ov // malformed → built-ins
	}
	for category, models := range parsed {
		if ov.byCategory[category] == nil {
			ov.byCategory[category] = map[string]map[string]overrideEntry{}
		}
		for model, params := range models {
			if ov.byCategory[category][model] == nil {
				ov.byCategory[category][model] = map[string]overrideEntry{}
			}
			for canon, raw := range params {
				entry, ok := parseOverrideEntry(raw)
				if !ok {
					continue // unparseable leaf → skip, keep builtin/default
				}
				ov.byCategory[category][model][canon] = entry
			}
		}
	}
	return ov
}

// parseOverrideEntry decodes one override leaf, accepting either a bare JSON
// string (legacy: the native path, or "" to disable) or an object with
// {path, kind, items, maxItems}. Returns ok=false when the leaf is neither.
func parseOverrideEntry(raw json.RawMessage) (overrideEntry, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return overrideEntry{}, false
	}
	// Object form: {"path": "...", "kind": "array", ...}
	if trimmed[0] == '{' {
		var obj struct {
			Path     string `json:"path"`
			Kind     string `json:"kind"`
			Items    string `json:"items"`
			MaxItems int    `json:"maxItems"`
		}
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return overrideEntry{}, false
		}
		return overrideEntry{Path: obj.Path, Kind: obj.Kind, Items: obj.Items, MaxItems: obj.MaxItems}, true
	}
	// Legacy bare-string form: the native path (or "" to disable).
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return overrideEntry{}, false
	}
	return overrideEntry{Path: s}, true
}

// audioSynonyms lists, per canonical param, the native key names to look for in
// a model's schema (scanned in order, top-level then one-level nested).
var audioSynonyms = map[string][]string{
	// "lyrics" covers lyrics-driven music models (fal-ai/diffrhythm) whose only
	// prompt-bearing input is the required lyrics field — see
	// conv_8be630557ba60c15daba8388, which 422ed with "body.lyrics Field
	// required" when the generic prompt/text fallback was sent instead.
	"prompt":   {"prompt", "text", "lyrics"},
	"duration": {"duration_seconds", "duration", "music_length_ms"},
	"loop":     {"loop"},
	"voice":    {"voice", "voice_id", "voice_name", "speaker", "speaker_id"},
	// "sourceAudio" is the voice-cloning reference clip — the audio sibling of
	// the lipsync source inputs, same native keys. Zero-shot cloning models
	// (fal-ai/f5-tts, dia, orpheus) declare audio_url for it; a speech model
	// without a reference input drops it with a notice and uses its own voice.
	"sourceAudio":    {"audio_url", "audio_file_url", "audio"},
	"negativePrompt": {"negative_prompt"},
	// "style_prompt" is required in practice by lyrics-driven music models
	// (fal-ai/diffrhythm: "Either style prompt or reference audio URL must be
	// provided" — a cross-field rule its schema can't express), so the planner
	// gets a canonical style param to fill (conv_3c7d38ba07af8ea2ba60573b).
	"style": {"style_prompt", "style", "genre"},
	// "direction" and "lyrics" are extend_audio canonicals (see
	// resolveAudioExtendBody); sonauto names its side selector "side" and its
	// lyric input "lyrics_prompt".
	"direction": {"direction", "side"},
	"lyrics":    {"lyrics", "lyrics_prompt"},
}

// imageSynonyms lists, per canonical param, the native key names to look for in
// an image model's schema. `sourceImage` is the cross-model abstraction for the
// frame a user attached to transform: flux/dev/image-to-image declares
// `image_url` (scalar), nano-banana-pro declares `image_urls` (array). The
// resolver wraps to a slice when the matched property is schemaArray.
//
// Three canonicals carry an output shape: `imageSize` for models that take a
// pixel-size or a fal preset enum on `image_size`/`size` (seedream),
// `aspectRatio` for models that take a raw ratio string on `aspect_ratio`
// (nano-banana-2/edit, whose enum lists "9:16", "16:9", ...), and `resolution`
// for models that take a tier enum on `resolution` (nano-banana-pro/edit,
// "1K"/"2K"/"4K"). The field names and value spaces don't overlap:
// resolveImageBody forwards the tier first, then tries `aspectRatio` and falls
// back to `imageSize`. We deliberately keep "size" off `aspectRatio` here even
// though the video table includes it — on image models "size" means pixel-size,
// so listing it under both canonicals would be ambiguous.
var imageSynonyms = map[string][]string{
	"prompt":            {"prompt"},
	"sourceImage":       {"image_url", "image_urls"},
	"imageSize":         {"image_size", "size"},
	"aspectRatio":       {"aspect_ratio", "aspectRatio"},
	"resolution":        {"resolution"},
	"numImages":         {"num_images"},
	"numInferenceSteps": {"num_inference_steps"},
}

// videoSynonyms lists, per canonical param, the native key names to look for in
// a video model's schema. `sourceImage` is the image-to-video frame; `sourceVideo`
// is the clip a Veo extend endpoint continues. Both sides list the plural array
// variant too — reference-to-video models (e.g. seedance) declare image_urls AND
// video_urls, and missing the plural video key made resolveVideoBody drop the
// attached video on a model that accepts it (conv_16bf42ce64997fad02f769a9) —
// as well as the reference_*_urls family: minimax/h3-max and bernini-r name
// their reference inputs reference_image_urls / reference_video_urls, and
// missing those made the resolver drop an attached image and fail at fal with
// 422 "At least one reference image, video, or audio must be provided"
// (conv_b1447b28bb48021702444ca4).
// aspectRatio covers Veo's "aspect_ratio" and any camelCase variant; duration is
// model-dependent (Veo wants "8s" strings, Kling wants numbers — coerceVideoValue
// handles both).
var videoSynonyms = map[string][]string{
	"prompt":         {"prompt"},
	"duration":       {"duration"},
	"aspectRatio":    {"aspect_ratio", "aspectRatio", "size"},
	"resolution":     {"resolution"},
	"fps":            {"fps", "frame_rate", "frameRate", "framerate"},
	"negativePrompt": {"negative_prompt"},
	"sourceImage":    {"image_url", "image_urls", "reference_image_urls"},
	// endImage is the closing frame for a start→end keyframe transition. fal
	// names it inconsistently — Seedance image-to-video declares end_image_url,
	// Kling tail_image_url — so both are listed and findNative picks whichever
	// the configured endpoint's schema declares. Populated only in keyframe mode
	// (VideoGenerateRequest.Keyframes); resolveVideoBody sends the last source
	// image here and the first onto image_url.
	"endImage":      {"end_image_url", "tail_image_url"},
	"sourceVideo":   {"video_url", "video_urls", "reference_video_urls"},
	"generateAudio": {"generate_audio"},
	// characterOrientation selects the output's orientation source on
	// motion-control models (Kling v2.6: required enum ["image","video"]). fal's
	// schema docs: "video" matches the motion video — better for complex motions
	// and allows up to 30s; "image" matches the subject image — better for
	// following camera movements, capped at 10s. resolveVideoBody defaults to
	// "video" since complex motion transfer is the dominant use.
	"characterOrientation": {"character_orientation"},
	// task selects the operation on multi-task reference models (seedance-2.5
	// reference-to-video: reference | editing | extension). resolveVideoBody sets
	// "extension" when the tool layer diagnosed the turn as a continuation; the
	// model's "reference" default would use the clip as guidance for a new video
	// instead of continuing it. Models without the param — dedicated extend
	// endpoints, minimax reference-to-video — are untouched.
	"task": {"task"},
	// scale is the upscale factor on video-upscaler endpoints. fal's endpoints
	// name it inconsistently — fal-ai/video-upscaler declares "scale",
	// clarityai/crystal-video-upscaler "scale_factor", topaz "upscale_factor" —
	// so all three are listed and findNative picks whichever the configured
	// endpoint's schema declares. Used by resolveVideoUpscaleBody only.
	"scale": {"scale", "scale_factor", "upscale_factor"},
}

// lipsyncSynonyms lists, per canonical param, the native key names to look for
// in a lip sync model's schema. sourceAudio is the driving audio track;
// sourceImage is the face for audio-to-video; sourceVideo is the clip for
// video-to-video.
var lipsyncSynonyms = map[string][]string{
	"sourceAudio": {"audio_url", "audio_file_url", "audio"},
	"sourceImage": {"image_url", "image_urls"},
	"sourceVideo": {"video_url", "video_urls"},
}

type canonicalValue struct {
	canon   string
	value   any
	present bool
}

// canonicalAudioValues yields the non-prompt canonical params to resolve, in a
// stable order. prompt is handled separately (always required).
func canonicalAudioValues(req AudioGenerateRequest) []canonicalValue {
	return []canonicalValue{
		{"duration", strings.TrimSpace(req.Duration), strings.TrimSpace(req.Duration) != ""},
		{"loop", req.Loop, req.Loop},
		{"voice", strings.TrimSpace(req.Voice), strings.TrimSpace(req.Voice) != ""},
		{"sourceAudio", strings.TrimSpace(req.SourceAudio), strings.TrimSpace(req.SourceAudio) != ""},
		{"negativePrompt", strings.TrimSpace(req.NegativePrompt), strings.TrimSpace(req.NegativePrompt) != ""},
		{"style", strings.TrimSpace(req.Style), strings.TrimSpace(req.Style) != ""},
	}
}

// resolveAudioBody maps a canonical AudioGenerateRequest onto model's native
// input schema, returning the fal body and user-facing notices for anything
// dropped. A nil schema (unavailable) yields a generic prompt+text body.
func resolveAudioBody(schema *ModelInputSchema, req AudioGenerateRequest, ov Overrides) (map[string]any, []string) {
	prompt := strings.TrimSpace(req.Prompt)
	if schema == nil {
		return map[string]any{"prompt": prompt, "text": prompt},
			[]string{"Couldn't load the model's parameter schema; generated with defaults and skipped duration/loop/voice/voice reference."}
	}

	body := map[string]any{}
	var notices []string

	// prompt always maps; hard requirement.
	if path, _, ok := findNative(schema, ov, "audio", req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, prompt)
	} else {
		body["prompt"], body["text"] = prompt, prompt
	}

	for _, item := range canonicalAudioValues(req) {
		if !item.present {
			continue
		}
		path, prop, ok := findNative(schema, ov, "audio", req.Model, item.canon)
		if !ok {
			label := canonLabel(item.canon)
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no %s control; ignoring the requested %s.",
				req.Model, label, label))
			continue
		}
		value, notice := coerceValue(item.canon, prop, item.value, req.Model)
		if notice != "" {
			notices = append(notices, notice)
			continue
		}
		setBodyPath(schema, body, path, value)
	}
	return body, notices
}

// audioExtendFlavor classifies an extend endpoint by id so the resolver can
// apply each one's native extension controls. The three flavors' input names
// are verified against fal's published schemas (OpenAPI for ace-step and
// stable-audio, llms.txt for sonauto); "generic" covers any other endpoint
// chosen via the model override and resolves through the synonym table.
func audioExtendFlavor(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(model, "audio-outpaint"):
		return "ace-step"
	case strings.HasPrefix(model, "sonauto/"):
		return "sonauto"
	case strings.Contains(model, "stable-audio") && strings.Contains(model, "inpaint"):
		return "stable-inpaint"
	}
	return "generic"
}

// stableAudioInpaintMaxSeconds is stable-audio-25/inpaint's hard ceiling on
// seconds_total (and therefore on mask_end).
const stableAudioInpaintMaxSeconds = 190

// aceStepOutpaintMaxSeconds is ace-step's per-side extension cap.
const aceStepOutpaintMaxSeconds = 107

// sonautoExtendDurationRange bounds sonauto's extend_duration input.
const (
	sonautoExtendMinSeconds = 2
	sonautoExtendMaxSeconds = 85
)

// parseAudioExtendSeconds reads the planner's duration string as added
// seconds. ok is false when absent or not a positive number (the caller
// notices the invalid case and applies its flavor's default).
func parseAudioExtendSeconds(duration string) (secs float64, ok bool, notice string) {
	trimmed := strings.TrimSpace(duration)
	if trimmed == "" {
		return 0, false, ""
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || parsed <= 0 {
		return 0, false, fmt.Sprintf("Ignored duration %q: not a positive number of seconds; the model default applies.", trimmed)
	}
	return parsed, true, ""
}

// splitCommaTags turns a comma-separated style string into sonauto's tags
// list ("lofi, chill" → ["lofi","chill"]).
func splitCommaTags(style string) []string {
	parts := strings.Split(style, ",")
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		if tag := strings.TrimSpace(part); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// roundSeconds converts a float duration to the integer seconds
// stable-audio's mask fields take, rounding to nearest (a sub-second seam at
// the mask edge beats either regenerating a sliver of the original or leaving
// a generated gap).
func roundSeconds(secs float64) int {
	return int(secs + 0.5)
}

// resolveAudioExtendBody maps a canonical AudioExtendRequest onto the selected
// extend endpoint's native body. Unlike resolveAudioBody the flavors differ
// structurally — ace-step splits before/after durations, sonauto takes a side
// plus one extend_duration, stable-audio places an integer-seconds mask — so
// each flavor builds its body explicitly; only the generic fallback (an
// arbitrary endpoint via the model override) uses the synonym machinery. A nil
// schema still yields a correct literal body for the three known flavors;
// errors are reserved for unfulfillable calls (a source duration the
// mask-based endpoint needs but the probe couldn't determine, or a total past
// the endpoint's cap).
func resolveAudioExtendBody(schema *ModelInputSchema, req AudioExtendRequest, ov Overrides) (map[string]any, []string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	style := strings.TrimSpace(req.Style)
	lyrics := strings.TrimSpace(req.Lyrics)
	extSeconds, extSet, notice := parseAudioExtendSeconds(req.Duration)
	var notices []string
	if notice != "" {
		notices = append(notices, notice)
	}
	before := strings.TrimSpace(req.Direction) == "before"

	switch audioExtendFlavor(req.Model) {
	case "ace-step":
		body := map[string]any{"audio_url": req.SourceAudio}
		// ace-step has no prompt input; tags (required) carries the style, and
		// the description doubles as tags when no style was given.
		tags := style
		if tags == "" {
			tags = prompt
		} else if prompt != "" {
			notices = append(notices, "fal-ai/ace-step/audio-outpaint has no prompt input; the continuation description wasn't sent — style tags and lyrics steer the extension.")
		}
		body["tags"] = tags
		if lyrics != "" {
			body["lyrics"] = lyrics
		}
		if !extSet {
			extSeconds = 30
		}
		if extSeconds > aceStepOutpaintMaxSeconds {
			notices = append(notices, fmt.Sprintf("Capped the extension at %ds: fal-ai/ace-step/audio-outpaint accepts at most %ds per side.", aceStepOutpaintMaxSeconds, aceStepOutpaintMaxSeconds))
			extSeconds = aceStepOutpaintMaxSeconds
		}
		// Both sides are always sent explicitly — fal defaults
		// extend_after_duration to 30, so an omitted zero would silently grow
		// the other end of the clip.
		if before {
			body["extend_before_duration"] = extSeconds
			body["extend_after_duration"] = 0
		} else {
			body["extend_before_duration"] = 0
			body["extend_after_duration"] = extSeconds
		}
		return body, notices, nil

	case "sonauto":
		body := map[string]any{"audio_url": req.SourceAudio}
		if before {
			body["side"] = "left"
		} else {
			body["side"] = "right"
		}
		if prompt != "" {
			body["prompt"] = prompt
		}
		if style != "" {
			body["tags"] = splitCommaTags(style)
		}
		if lyrics != "" {
			body["lyrics_prompt"] = lyrics
		}
		// Omitted duration stays omitted: sonauto auto-determines the
		// extension length, which beats guessing a default for it.
		if extSet {
			if extSeconds < sonautoExtendMinSeconds || extSeconds > sonautoExtendMaxSeconds {
				clamped := math.Min(math.Max(extSeconds, sonautoExtendMinSeconds), sonautoExtendMaxSeconds)
				notices = append(notices, fmt.Sprintf("Adjusted the extension to %ds: sonauto/v2/extend accepts %ds at most (minimum %ds).", int(clamped), sonautoExtendMaxSeconds, sonautoExtendMinSeconds))
				extSeconds = clamped
			}
			body["extend_duration"] = extSeconds
		}
		return body, notices, nil

	case "stable-inpaint":
		if req.SourceDurationSeconds <= 0 {
			return nil, notices, fmt.Errorf("could not determine the source clip's length, which %q needs to place its mask; try \"fal-ai/ace-step/audio-outpaint\" or \"sonauto/v2/extend\", which extend without it", req.Model)
		}
		if !extSet {
			extSeconds = 30
		}
		sourceLen := roundSeconds(req.SourceDurationSeconds)
		ext := roundSeconds(extSeconds)
		// The mask covers only the generated region: [sourceLen, total] when
		// appending, [0, ext] when prepending — the original occupies the rest
		// of seconds_total and is preserved.
		var maskStart, maskEnd, total int
		if before {
			maskStart = 0
			maskEnd = ext
			total = sourceLen + ext
		} else {
			maskStart = sourceLen
			maskEnd = sourceLen + ext
			total = sourceLen + ext
		}
		if total > stableAudioInpaintMaxSeconds {
			return nil, notices, fmt.Errorf("extending the ~%ds clip by %ds reaches %ds, past %q's %ds cap; ask for a shorter extension, or use \"fal-ai/ace-step/audio-outpaint\" for music", sourceLen, ext, total, req.Model, stableAudioInpaintMaxSeconds)
		}
		body := map[string]any{
			"prompt":        prompt,
			"audio_url":     req.SourceAudio,
			"mask_start":    maskStart,
			"mask_end":      maskEnd,
			"seconds_total": total,
		}
		return body, notices, nil
	}

	// Generic: an arbitrary endpoint via the model override — resolve the
	// shared canonicals through the synonym table, drop-with-notice anything
	// the endpoint doesn't declare, and fail when it has no audio input to
	// extend.
	body := map[string]any{}
	if schema == nil {
		return nil, notices, errors.New("couldn't load the selected extend model's parameter schema; only the mapped endpoints (stable-audio-25/inpaint, ace-step/audio-outpaint, sonauto/v2/extend) can run without it")
	}
	path, _, ok := findNative(schema, ov, "audio", req.Model, "sourceAudio")
	if !ok {
		return nil, notices, fmt.Errorf("the selected model %q has no audio input to extend", req.Model)
	}
	setBodyPath(schema, body, path, req.SourceAudio)
	if path, _, ok := findNative(schema, ov, "audio", req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, prompt)
	} else {
		body["prompt"] = prompt
	}
	generic := []canonicalValue{
		{"duration", req.Duration, extSet},
		{"direction", strings.TrimSpace(req.Direction), strings.TrimSpace(req.Direction) != ""},
		{"negativePrompt", strings.TrimSpace(req.NegativePrompt), strings.TrimSpace(req.NegativePrompt) != ""},
		{"style", style, style != ""},
		{"lyrics", lyrics, lyrics != ""},
	}
	for _, item := range generic {
		if !item.present {
			continue
		}
		path, prop, ok := findNative(schema, ov, "audio", req.Model, item.canon)
		if !ok {
			notices = append(notices, fmt.Sprintf("The selected model %q has no %s control; ignoring the requested %s.", req.Model, canonLabel(item.canon), canonLabel(item.canon)))
			continue
		}
		value, notice := coerceValue(item.canon, prop, item.value, req.Model)
		if notice != "" {
			notices = append(notices, notice)
			continue
		}
		setBodyPath(schema, body, path, value)
	}
	return body, notices, nil
}

// resolveImageBody maps a canonical ImageGenerateRequest onto the model's native
// input schema, returning the fal body, user-facing notices for anything
// dropped, and an error for a hard capability mismatch (multi-image into a
// single-image edit model). A nil schema (unavailable) yields the legacy
// hardcoded body ({prompt, num_images, image_url?|image_size?,
// num_inference_steps?}) plus a notice. This is the image sibling of
// resolveVideoBody; the image-specific rules are (1) a source-image field whose
// schema kind is schemaArray (e.g. nano-banana-pro's image_urls) takes the whole
// slice, and (2) multiple images into a scalar-image model is a hard error.
func resolveImageBody(schema *ModelInputSchema, req ImageGenerateRequest, ov Overrides) (map[string]any, []string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	// fal requires an HTTP(S) URL or a data URI and rejects bare base64 with a
	// 422; the rest of the app carries attached images as bare base64 (the
	// data: prefix is stripped for Ollama), so normalize here. This was the
	// GenerateImage client's job before the resolver refactor; it moves here
	// because the resolver now owns body construction.
	sourceImages := make([]string, 0, len(req.Images))
	for _, img := range req.Images {
		if u := falImageURL(strings.TrimSpace(img)); u != "" {
			sourceImages = append(sourceImages, u)
		}
	}
	if schema == nil {
		// The legacy fallback only knows a scalar image_url; multiple images
		// can't be expressed without a schema to map them onto, so fail rather
		// than silently drop the extras.
		if len(sourceImages) > 1 {
			return nil, nil, fmt.Errorf(
				"model %q could not be queried for its parameter schema and accepts at most one image; %d were attached. Configure a multi-image edit model (e.g. one whose schema declares image_urls).",
				req.Model, len(sourceImages))
		}
		body := map[string]any{
			"prompt":     prompt,
			"num_images": 1,
		}
		if len(sourceImages) == 1 {
			body["image_url"] = sourceImages[0]
			// Edit models commonly accept image_size alongside the source frame;
			// send the aspect-ratio preset so an output-orientation request on a
			// differently-shaped source isn't silently dropped.
			if preset := falImageSizePreset(req.AspectRatio); preset != "" {
				body["image_size"] = preset
			}
		} else if req.Width > 0 && req.Height > 0 {
			if preset := falImageSizePreset(req.AspectRatio); preset != "" {
				body["image_size"] = preset
			} else {
				body["image_size"] = map[string]any{"width": req.Width, "height": req.Height}
			}
		}
		if req.Steps > 0 {
			body["num_inference_steps"] = req.Steps
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported image input."}, nil
	}

	body := map[string]any{}
	var notices []string

	if path, prop, ok := findNative(schema, ov, "image", req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, coerceImageValue(prop, prompt))
	} else {
		body["prompt"] = prompt
	}
	if path, prop, ok := findNative(schema, ov, "image", req.Model, "numImages"); ok {
		setBodyPath(schema, body, path, coerceImageValue(prop, 1))
	} else {
		body["num_images"] = 1
	}

	// Resolution tier first: models that speak tiers natively (nano banana's
	// resolution enum) get the effective tier — explicit or configured default
	// — on their own field, independent of the ratio paths below. Pixel-only
	// models never see it here; their dimensions already encode the tier.
	resolutionPlaced, tierNotices := forwardImageResolutionTier(schema, ov, body, req)
	notices = append(notices, tierNotices...)

	// Image-to-image takes the source frame(s). An output-orientation request
	// ("make this 9:16") must override the source's inherited orientation —
	// otherwise a landscape source silently stays landscape (see conv_711ebd5f,
	// and conv_369b3099eed8483b7b6a14bf where the ratio was dropped entirely).
	// Edit models expose this in one of two ways, tried in order: a raw
	// aspect_ratio string enum (nano-banana-2/edit, nano-banana/edit), or an
	// image_size preset enum alongside the source frame (seedream edit). Only
	// the preset is ever sent on image_size — never raw width/height, since the
	// source frame sets the resolution and a pixel object could conflict. For
	// models that derive dims purely from the source (no field of either name),
	// both are correctly omitted. Text-to-image always takes the configured
	// dimensions.
	if len(sourceImages) > 0 {
		path, prop, ok := findNative(schema, ov, "image", req.Model, "sourceImage")
		if !ok {
			// The image is the request's whole purpose — attached images route
			// to the image-to-image slot — and fal accepts the submit without
			// it, so the model would generate from the prompt alone. Refuse
			// instead of delivering an image the attachment never shaped (the
			// wan 2.7 lesson, conv_d5f5177320da74a0ce3caebb); the video
			// resolver holds the same line.
			return nil, notices, fmt.Errorf(
				"the selected model %q has no source-image input to edit; pick a different image edit model in Settings → Models",
				req.Model)
		} else {
			// Guardrail: multiple images into a scalar-image model is a hard
			// error — silently dropping the rest would hide a real capability
			// mismatch.
			if prop.Kind != schemaArray && len(sourceImages) > 1 {
				return nil, notices, fmt.Errorf(
					"model %q accepts a single image; %d were attached. Use a multi-image edit model (one whose schema declares image_urls).",
					req.Model, len(sourceImages))
			}
			if prop.Kind == schemaArray && prop.MaxItems > 0 && len(sourceImages) > prop.MaxItems {
				return nil, notices, fmt.Errorf(
					"model %q accepts at most %d image(s); %d were attached. Attach fewer images or switch to a model with a higher image cap.",
					req.Model, prop.MaxItems, len(sourceImages))
			}
			setBodyPath(schema, body, path, coerceImages(prop, sourceImages))
		}
		// Forward the requested ratio onto the model's native ratio field. Try
		// aspect_ratio (raw "9:16" enum) first; if the model has no aspect_ratio
		// input, fall back to the image_size preset. The two field names don't
		// overlap on any known model, so at most one fires.
		if !sendImageAspectRatio(schema, ov, body, req) {
			if preset := falImageSizePreset(req.AspectRatio); preset != "" {
				if sizePath, sizeProp, hasSize := findNative(schema, ov, "image", req.Model, "imageSize"); hasSize {
					if len(sizeProp.Enum) == 0 || valueAllowedByEnum(sizeProp, preset) {
						setBodyPath(schema, body, sizePath, preset)
					}
				}
			}
		}
		// An explicit resolution the model can't express natively (it has no
		// resolution input — edit resolution follows the source frame by
		// design, so no pixel fallback exists on this path) deserves a notice;
		// the configured default stays silent to avoid one per generation.
		if req.ResolutionExplicit && !resolutionPlaced {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q derives edit resolution from the attached image; ignoring the requested %q resolution.",
				req.Model, normalizeImageResolutionTier(req.Resolution)))
		}
	} else if req.Width > 0 && req.Height > 0 {
		sendImageSize(schema, ov, body, req, resolutionPlaced, &notices)
	}

	if req.Steps > 0 {
		if path, prop, ok := findNative(schema, ov, "image", req.Model, "numInferenceSteps"); ok {
			setBodyPath(schema, body, path, coerceImageValue(prop, req.Steps))
		} else {
			body["num_inference_steps"] = req.Steps
		}
	}
	return body, notices, nil
}

// sendImageAspectRatio writes the raw aspect-ratio string (e.g. "9:16") onto the
// model's native aspect_ratio field, enum-gated, and reports whether it placed a
// value. It is the ratio-native sibling of sendImageSize: many modern edit
// models (nano-banana-2/edit, nano-banana/edit) expose aspect_ratio as a string
// enum ("auto","16:9","9:16",...) rather than a pixel image_size, and send the
// raw ratio — never a fal preset like "portrait_16_9", which those enums don't
// list. A requested ratio the model's enum doesn't accept is dropped (the model
// picks its own default) rather than sent — passing it through would 422 at fal.
//
// Returns false (without writing) when the model has no aspect_ratio input, so
// the caller falls back to sendImageSize's image_size path. This is the fix for
// conv_369b3099eed8483b7b6a14bf: a 9:16 request on nano-banana-2/edit came back
// landscape because the ratio was routed only through image_size, which that
// model doesn't have — see resolveImageBody's sourced branch.
func sendImageAspectRatio(schema *ModelInputSchema, ov Overrides, body map[string]any, req ImageGenerateRequest) bool {
	aspect := strings.TrimSpace(req.AspectRatio)
	if aspect == "" {
		return false
	}
	path, prop, ok := findNative(schema, ov, "image", req.Model, "aspectRatio")
	if !ok {
		return false
	}
	if len(prop.Enum) > 0 && !valueAllowedByEnum(prop, aspect) {
		return false
	}
	setBodyPath(schema, body, path, aspect)
	return true
}

// sendImageSize writes the image_size field onto the fal request body, choosing
// between an aspect-ratio preset enum string, a tier auto-preset, and a
// {width,height} object.
//
// The model's native ratio field is tried first via sendImageAspectRatio — a
// text-to-image model that exposes aspect_ratio (e.g. nano-banana-2) takes the
// raw "9:16" string there and has no image_size input at all, so routing it
// through image_size would silently drop the ratio (the image-edit sibling of
// conv_369b3099eed8483b7b6a14bf). Only when the model has no aspect_ratio input
// does this fall through to the image_size paths below.
//
// Some fal image models (notably seedream) accept an aspect-ratio preset enum on
// image_size ("landscape_16_9", "portrait_16_9", ...) and either ignore or
// reject a {width,height} object below their minimum pixel area. The derived
// short-edge dims for a portrait ratio routinely fall under that floor, so the
// model falls back to a default landscape size and the requested aspect ratio is
// lost — see conv_b02dc16f: a 9:16 request returned a 2736x1536 landscape image.
// When the requested ratio maps to a known preset, prefer the enum the model
// honors; a mapped preset also fixes the resolution, so an explicit tier that
// couldn't be expressed anywhere is dropped with a notice. Tier-auto models
// (seedream's auto_1K/auto_2K) get the tier there when no preset fired —
// 1k pixels fall under seedream's area floor and 4k pixels exceed its ceiling,
// so the auto value is the only safe carrier; 2k pixels carry both the ratio
// and the tier and stay pixels. Otherwise send the pixel object (the original
// behavior).
func sendImageSize(schema *ModelInputSchema, ov Overrides, body map[string]any, req ImageGenerateRequest, resolutionPlaced bool, notices *[]string) {
	if sendImageAspectRatio(schema, ov, body, req) {
		return
	}
	preset := falImageSizePreset(req.AspectRatio)
	path, prop, hasSize := findNative(schema, ov, "image", req.Model, "imageSize")
	if preset != "" && (!hasSize || valueAllowedByEnum(prop, preset) || len(prop.Enum) == 0) {
		// The property either has no enum constraint (seedream's anyOf leaves
		// Enum empty in our parsed view) or explicitly accepts the preset —
		// either way the preset is safe to send.
		if hasSize {
			setBodyPath(schema, body, path, preset)
		} else {
			body["image_size"] = preset
		}
		// A mapped preset pins the resolution to whatever the model defines for
		// that shape. An explicit tier that no native field took is dropped —
		// say so; the configured default stays silent (it fires every call).
		if req.ResolutionExplicit && !resolutionPlaced && normalizeImageResolutionTier(req.Resolution) != "" {
			*notices = append(*notices, fmt.Sprintf(
				"The selected model %q fixes its own resolution for the %s shape; ignoring the requested %q resolution.",
				req.Model, req.AspectRatio, normalizeImageResolutionTier(req.Resolution)))
		}
		return
	}
	if auto := imageResolutionAutoPreset(req.Resolution); auto != "" &&
		hasSize && (valueAllowedByEnum(prop, auto) || len(prop.Enum) == 0) {
		// Unlike the preset branch, auto values are only sent when the model
		// declares image_size: they are seedream-family values, not a fal-wide
		// convention, so an undeclared field can't be assumed to accept them.
		// An empty parsed enum is seedream's anyOf shape — same posture as the
		// preset branch takes there.
		setBodyPath(schema, body, path, auto)
		// 4k maps to auto_2K: pixels would exceed the tier-auto model's area
		// ceiling and 422, so the cap is a real downgrade worth surfacing.
		if normalizeImageResolutionTier(req.Resolution) == "4k" {
			*notices = append(*notices, fmt.Sprintf(
				"The selected model %q tops out at its 2K tier; using it instead of the requested 4k.",
				req.Model))
		}
		return
	}
	pixels := map[string]any{"width": req.Width, "height": req.Height}
	if hasSize {
		setBodyPath(schema, body, path, pixels)
	} else {
		body["image_size"] = pixels
	}
}

// forwardImageResolutionTier writes the effective resolution tier onto the
// model's native resolution input when it has one (nano banana exposes
// resolution as a "1K"/"2K"/"4K" enum). fal's enums are uppercase, so the tier
// is uppercased to match. Returns whether the tier was placed; drop notices are
// emitted for a tier the model rejects — always for an explicit request, and
// for the configured default too, since a Settings choice that silently does
// nothing is exactly the dishonesty the tier rework exists to fix.
func forwardImageResolutionTier(schema *ModelInputSchema, ov Overrides, body map[string]any, req ImageGenerateRequest) (bool, []string) {
	tier := normalizeImageResolutionTier(req.Resolution)
	if tier == "" {
		return false, nil
	}
	native := strings.ToUpper(tier)
	if path, prop, ok := findNative(schema, ov, "image", req.Model, "resolution"); ok {
		if valueAllowedByEnum(prop, native) {
			setBodyPath(schema, body, path, native)
			return true, nil
		}
		return false, []string{fmt.Sprintf(
			"The selected model %q does not accept resolution %q; ignoring it and letting the model choose.",
			req.Model, tier)}
	}
	// No native field: the tier may still ride pixels or an image_size auto
	// value below, so nothing is reported here. sendImageSize and the edit
	// branch own the explicit-request notices for those outcomes.
	return false, nil
}

// imageResolutionAutoPreset maps a resolution tier onto fal's image_size auto
// values where a model's enum lists them (seedream: auto_1K/auto_2K). 2k
// returns "" on purpose: 2k-derived pixels sit inside the area window
// tier-auto models accept, and pixels carry the aspect ratio the auto value
// would drop. 4k maps down to auto_2K — pixels would exceed the model's area
// ceiling — and the caller notices the cap.
func imageResolutionAutoPreset(tier string) string {
	switch normalizeImageResolutionTier(tier) {
	case "1k":
		return "auto_1K"
	case "4k":
		return "auto_2K"
	}
	return ""
}

// falImageSizePreset maps a named aspect ratio to fal's image_size preset enum
// where one exists. Returns "" for ratios with no preset (1:1 maps to
// "square_hd"); callers fall back to the pixel object.
func falImageSizePreset(ratio string) string {
	switch strings.TrimSpace(ratio) {
	case "1:1":
		return "square_hd"
	case "16:9":
		return "landscape_16_9"
	case "9:16":
		return "portrait_16_9"
	case "4:3":
		return "landscape_4_3"
	case "3:4":
		return "portrait_4_3"
	}
	return ""
}

// coerceImageValue adapts a canonical image value to the native property's type:
// a schemaArray property (e.g. nano-banana-pro's image_urls) wraps a scalar into
// a single-element slice. Scalars pass through unchanged. Unlike audio's
// coerceValue there's no enum or unit conversion in the image path today.
func coerceImageValue(prop SchemaProperty, value any) any {
	if prop.Kind == schemaArray {
		return []any{value}
	}
	return value
}

// coerceImages adapts a slice of source images to the native property's type
// for multi-image requests (image-to-video reference, multi-image edit). It is
// the slice sibling of coerceImageValue/coerceVideoValue: a schemaArray
// property (e.g. seedance reference-to-video's image_urls) takes the whole
// slice; a scalar property (e.g. image-to-video's image_url) takes only the
// first — the caller's guardrail already rejected multi-image-into-scalar
// before reaching here, so the truncation is a defensive backstop, not the
// primary behavior.
func coerceImages(prop SchemaProperty, images []string) any {
	if len(images) == 0 {
		return nil
	}
	if prop.Kind == schemaArray {
		out := make([]any, 0, len(images))
		for _, img := range images {
			out = append(out, img)
		}
		return out
	}
	return images[0]
}

// coerceVideos is the video sibling of coerceImages: a schemaArray property
// (e.g. a reference-to-video model declaring video_urls) takes the whole list,
// a scalar property takes the first clip. The scalar branch only ever sees one
// video — resolveVideoBody's guardrails reject a multi-video request against a
// scalar-video model before this runs.
func coerceVideos(prop SchemaProperty, videos []string) any {
	if len(videos) == 0 {
		return nil
	}
	if prop.Kind == schemaArray {
		out := make([]any, 0, len(videos))
		for _, vid := range videos {
			out = append(out, vid)
		}
		return out
	}
	return videos[0]
}

// resolveVideoBody maps a canonical VideoGenerateRequest onto the model's native
// input schema, returning the fal body, user-facing notices for anything
// dropped, and an error for a hard capability mismatch (multi-image into a
// single-image model). It is the video sibling of resolveImageBody. A nil schema
// (unavailable) yields the legacy hardcoded body — the fields GenerateVideo used
// to build itself before the resolver refactor — plus a notice, so fal models
// without a published schema keep working.
//
// Source media is resolved per side, not exclusively: an attached Video maps to
// the model's video input (extend / motion source) and attached Images map to
// its image input (image-to-video / reference / motion subject), so a model
// that takes both — motion control — receives both; all are absent for
// text-to-video. fal requires an HTTP(S) URL or a data URI and rejects bare
// base64 with a 422, so falImageURL/falVideoURL normalize each. A media field
// the selected model lacks is dropped with a notice rather than sent. Multiple
// images into a model whose source-image field is scalar (or into the no-schema
// legacy path, which only knows a single image_url) is a hard error — the
// caller fails the tool call so the user knows to switch models rather than
// silently losing all but one image. Multiple videos follow the same rule on
// the source-video side (a scalar video_url vs the video_urls arrays reference
// models declare).
func resolveVideoBody(schema *ModelInputSchema, req VideoGenerateRequest, ov Overrides) (map[string]any, []string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	// SourceImages() unifies the new Images slice and the legacy scalar Image.
	sourceImages := make([]string, 0, 4)
	for _, img := range req.SourceImages() {
		if u := falImageURL(strings.TrimSpace(img)); u != "" {
			sourceImages = append(sourceImages, u)
		}
	}
	// SourceVideos() unifies the new Videos slice and the legacy scalar Video.
	// sourceVideo stays as the first clip for the scalar uses below (the legacy
	// body's video_url, the extend aspect-ratio notice, sourcePresent gating).
	sourceVideos := make([]string, 0, 4)
	for _, vid := range req.SourceVideos() {
		if u := falVideoURL(strings.TrimSpace(vid)); u != "" {
			sourceVideos = append(sourceVideos, u)
		}
	}
	sourceVideo := ""
	if len(sourceVideos) > 0 {
		sourceVideo = sourceVideos[0]
	}
	if schema == nil {
		// The legacy fallback only knows a scalar image_url and video_url;
		// multiples of either can't be expressed without a schema to map them
		// onto, so fail rather than silently drop the extras.
		if len(sourceImages) > 1 {
			return nil, nil, fmt.Errorf(
				"model %q could not be queried for its parameter schema and accepts at most one image; %d were attached. Configure a multi-image video model (e.g. bytedance/seedance-2.0/reference-to-video).",
				req.Model, len(sourceImages))
		}
		if len(sourceVideos) > 1 {
			return nil, nil, fmt.Errorf(
				"model %q could not be queried for its parameter schema and accepts at most one video; %d were attached. Configure a multi-video reference model (e.g. bytedance/seedance-2.5/reference-to-video).",
				req.Model, len(sourceVideos))
		}
		body := map[string]any{"prompt": prompt}
		if duration := strings.TrimSpace(req.Duration); duration != "" {
			body["duration"] = duration
		}
		if aspect := strings.TrimSpace(req.AspectRatio); aspect != "" && !isAutoAspectRatio(aspect) {
			// Same gate as the schema-driven branch: for image-to-video and
			// extend, only send aspect_ratio when the planner explicitly set it;
			// otherwise let the model inherit the source media's orientation.
			// "auto" is withheld too — without a schema there is no enum to
			// check it against, and fal 422s on an unknown enum member, while
			// the model's own default already delivers the defer the sentinel
			// asks for.
			if (len(sourceImages) == 0 && sourceVideo == "") || req.AspectRatioExplicit {
				body["aspect_ratio"] = aspect
			}
		}
		if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
			body["negative_prompt"] = negative
		}
		if res := strings.TrimSpace(req.Resolution); res != "" {
			body["resolution"] = res
		}
		if fps := strings.TrimSpace(req.FPS); fps != "" {
			body["fps"] = fps
		}
		if req.GenerateAudio != nil {
			body["generate_audio"] = *req.GenerateAudio
		}
		if sourceVideo != "" {
			body["video_url"] = sourceVideo
		} else if len(sourceImages) == 1 {
			body["image_url"] = sourceImages[0]
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported video input."}, nil
	}

	body := map[string]any{}
	var notices []string

	// Capture the prompt's native path/prop: the reference-token legend below
	// re-sets the same slot with an augmented prompt after the source sides
	// resolve.
	promptPath, promptProp := "", SchemaProperty{}
	if path, prop, ok := findNative(schema, ov, "video", req.Model, "prompt"); ok {
		promptPath, promptProp = path, prop
		setBodyPath(schema, body, path, coerceVideoValue(prop, prompt))
	} else {
		body["prompt"] = prompt
	}

	if duration := strings.TrimSpace(req.Duration); duration != "" {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "duration"); ok {
			// Guard against unsendable durations before sending — "auto" is
			// Seedance-only, Kling accepts just ["5","10"], and an enum-less
			// numeric field (minimax/h3's integer 5–15) has no "auto" member at
			// all. Passing any of those through would 422 at fal. Drop "auto"
			// with a notice that names the model's own default so the request
			// still runs; anything else unsendable drops the same way it always
			// did.
			if !videoDurationSendable(prop, duration) {
				notices = append(notices, videoDurationDroppedNotice(req.Model, duration, prop))
			} else {
				setBodyPath(schema, body, path, coerceVideoValue(prop, duration))
			}
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no duration control; ignoring the requested duration.",
				req.Model))
		}
	}
	// Reference-guided turns attach guidance media, not the canvas: the
	// model's schema-declared source input is reference-style — a plural,
	// prompt-addressed array (image_urls, reference_image_urls, video_urls)
	// whose own description documents @ImageN/@VideoN tokens or guidance — so
	// the attached images are character sheets and style references whose
	// shape must not set the output orientation. On those turns the
	// configured default replaces the gateway's image-derived ratio AND is
	// sent, because reference models don't inherit orientation from their
	// reference media (conv_8b8bffd58dcb383ee1cbfeae: a 2752x1536 landscape
	// character sheet produced a 720x1280 portrait clip when no ratio reached
	// fal and seedance-2.5's aspect_ratio "auto" ignored the reference).
	// Frame turns (image-to-video, keyframes), extends, and motion control
	// keep the inherit rule — their output continues the source frame or clip
	// (conv_26cc3f515d6d645b316763cb). Classification reads the schema's own
	// declared input, not the override-resolved wire path: builtin overrides
	// fix wire-format drift (kling v2 i2v routes image_url bytes onto
	// image_urls) without changing what the media means.
	referenceGuided := false
	if len(sourceImages) > 0 && len(sourceVideos) == 0 && !req.Keyframes {
		if name, prop, ok := findNative(schema, Overrides{}, "video", req.Model, "sourceImage"); ok {
			referenceGuided = referenceStyleMediaInput(name, prop)
		}
	}
	if len(sourceVideos) > 0 && req.VideoRole == "reference" && !req.ExtendSource {
		if name, prop, ok := findNative(schema, Overrides{}, "video", req.Model, "sourceVideo"); ok {
			referenceGuided = referenceStyleMediaInput(name, prop)
		}
	}
	aspect := strings.TrimSpace(req.AspectRatio)
	if !req.AspectRatioExplicit && referenceGuided {
		// The gateway's image-derived ratio describes the reference media,
		// not the requested canvas — withdraw it in favor of the configured
		// default, the user's standing orientation preference.
		aspect = strings.TrimSpace(req.ConfigAspectRatio)
	}
	if aspect != "" {
		// Image-to-video and extend-video models derive orientation from their
		// source media — the source frame for image-to-video, the source clip
		// for extend — so an aspect_ratio derived from config (or a detected
		// source ratio echoed back) is both redundant and a footgun: it
		// conflicts with the source and can force a re-orient (see
		// conv_26cc3f515d6d645b316763cb: a 9:16 portrait image came back 16:9
		// because the config default was sent). Only send aspect_ratio for a
		// sourced request when it was explicitly requested by the planner,
		// which legitimately overrides the source — or when the source media
		// is reference guidance, which sets no orientation of its own to
		// conflict with. Text-to-video always sends it (the configured default
		// is the only signal there). This mirrors resolveImageBody's parity
		// rule for image-to-image (fal_params.go:248-290).
		sourcePresent := len(sourceImages) > 0 || sourceVideo != ""
		if sourcePresent && !req.AspectRatioExplicit && !referenceGuided {
			// skip — inherit the source media's orientation
		} else if path, prop, ok := findNative(schema, ov, "video", req.Model, "aspectRatio"); ok {
			// Enum guard, same as duration/resolution/fps: the planner's ratio
			// enum offers values not every model takes (3:2 and 2:3, while
			// minimax accepts only adaptive/21:9/16:9/4:3/1:1/3:4/9:16), and
			// passing an out-of-enum ratio through would 422 at fal. Drop it
			// with a notice so the request still runs on the model's own
			// default (minimax: "adaptive", which follows the source media) —
			// except "auto", the defer sentinel: a model whose enum lacks it
			// falls back to its own default, which is exactly the request, so
			// that drop stays silent.
			if canonical, allowed := enumValueFor(prop, aspect); allowed {
				setBodyPath(schema, body, path, coerceVideoValue(prop, canonical))
			} else if !isAutoAspectRatio(aspect) {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not accept aspect ratio %q; ignoring it and letting the model choose.",
					req.Model, aspect))
			}
		} else if isAutoAspectRatio(aspect) {
			// "auto" asked the model to decide; one with no aspect_ratio input
			// does exactly that with its own default — the request, not a
			// mismatch worth a notice.
		} else if sourceVideo != "" && !referenceGuided {
			// Extend with an explicit ratio, but the model has no aspect_ratio
			// input. Its output ratio is NOT uncontrolled — it is inherited
			// from the source clip — so "no aspect-ratio control" is wrong here
			// (see conv_484449cf8fe4a13c1ffa6bb4, where this notice misstated a
			// grok-imagine-video extend request). Atelier doesn't reshape the
			// video, so say that honestly rather than claiming the ratio was
			// ignored.
			notices = append(notices, fmt.Sprintf(
				"The selected model %q derives the output aspect ratio from the source video and has no aspect_ratio input, so the explicit %q request is only honored if the source video already matches; Atelier did not reshape the video.",
				req.Model, aspect))
		} else if len(sourceImages) > 0 && !referenceGuided {
			// Image-to-video with an explicit ratio, but the model has no
			// aspect_ratio input. Its output ratio is NOT uncontrolled — it is
			// inherited from the source frame — so "no aspect-ratio control" is
			// wrong here. The model could honor the request through the frame's
			// shape; Atelier doesn't reshape the image, so say that honestly
			// rather than claiming the ratio was ignored. (See
			// conv_e30f67cc834d4e98e1a49631, where this notice misstated a
			// happy-horse image-to-video request.)
			notices = append(notices, fmt.Sprintf(
				"The selected model %q derives the output aspect ratio from the source image and has no aspect_ratio input, so the explicit %q request is only honored if the source image already matches; Atelier did not reshape the image.",
				req.Model, aspect))
		} else {
			// Text-to-video with no aspect_ratio input — and reference-guided
			// turns, whose guidance media sets no orientation of its own: in
			// both the model picks its own default orientation.
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no aspect-ratio control; ignoring the requested aspect ratio.",
				req.Model))
		}
	}
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "negativePrompt"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, negative))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no negative-prompt control; ignoring the requested negative prompt.",
				req.Model))
		}
	}
	if res := strings.TrimSpace(req.Resolution); res != "" {
		// Resolution tiers are model-dependent: Seedance accepts
		// 480p/720p/1080p/4k, happy-horse only 720p/1080p. Guard against values
		// the model's enum doesn't list before sending — passing an out-of-enum
		// tier through would 422 at fal. Drop it with a notice so the request
		// still runs (the model picks its own default), mirroring how duration is
		// handled above. enumValueFor matches case-insensitively and returns the
		// enum's own spelling, so the planner's lowercase "1080p" reaches
		// minimax's uppercase "1080P" enum member instead of being dropped
		// (a tier that merely differs in case is accepted, not rejected).
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "resolution"); ok {
			if canonical, allowed := enumValueFor(prop, res); allowed {
				setBodyPath(schema, body, path, coerceVideoValue(prop, canonical))
			} else {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not accept resolution %q; ignoring it and letting the model choose.",
					req.Model, res))
			}
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no resolution control; ignoring the requested resolution.",
				req.Model))
		}
	}
	if fps := strings.TrimSpace(req.FPS); fps != "" {
		// Frame rate is model-dependent: most video models have no fps input at
		// all, and those that do expose it as either a free integer or a fixed
		// enum (e.g. ["24","30","60"]). Guard against both before sending — an
		// out-of-enum value would 422 at fal. Drop it with a notice so the
		// request still runs (the model picks its own default), mirroring how
		// resolution and duration are handled above. coerceVideoValue handles
		// integer-typed fps inputs (the common case) by parsing the string.
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "fps"); ok {
			if !valueAllowedByEnum(prop, fps) {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not accept frame rate %q; ignoring it and letting the model choose.",
					req.Model, fps))
			} else {
				setBodyPath(schema, body, path, coerceVideoValue(prop, fps))
			}
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no frame-rate control; ignoring the requested frame rate.",
				req.Model))
		}
	}
	if req.GenerateAudio != nil {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "generateAudio"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, *req.GenerateAudio))
		} else if !*req.GenerateAudio {
			// An explicit silent request (generateAudio:false) against a model
			// with no generate_audio toggle cannot be honored. The absence of a
			// toggle does NOT mean the model is silent — some endpoints emit
			// synchronized audio by default (e.g. alibaba/happy-horse, which
			// does joint audio-video generation) yet expose no way to disable
			// it. Silent dropping here would let audio through after the user
			// explicitly asked for none, so surface a notice instead. A true
			// (or nil) value is left to the model's default — no notice needed.
			notices = append(notices, fmt.Sprintf(
				"The selected model %q generates audio by default and exposes no generate_audio input; an explicit silent request cannot be honored, so the video will contain audio.",
				req.Model))
		}
	}

	// Source media: a video alone is an extend (video-to-video), images alone are
	// image-to-video / reference-to-video, and BOTH together are motion control —
	// the model animates the image's subject with the video's motion, so each
	// side maps onto its own native field. A side the model's schema can't map
	// is a hard error, not a graceful drop: fal accepts the submit without it,
	// so the model would fail demanding its field or generate media the
	// attachments never shaped. The resolved props feed the reference-token
	// legend below.
	var sourceVideoProp, sourceImageProp SchemaProperty
	if len(sourceVideos) > 0 {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceVideo"); ok {
			// Guardrails mirroring the image side: multiple videos into a
			// scalar-video model is a hard error (the model only accepts one
			// clip; silently dropping the rest would hide a capability
			// mismatch), and a declared maxItems cap rejects requests above it.
			if prop.Kind != schemaArray && len(sourceVideos) > 1 {
				return nil, notices, fmt.Errorf(
					"model %q accepts a single video; %d were attached. Use a multi-video reference model (e.g. bytedance/seedance-2.5/reference-to-video).",
					req.Model, len(sourceVideos))
			}
			if prop.Kind == schemaArray && prop.MaxItems > 0 && len(sourceVideos) > prop.MaxItems {
				return nil, notices, fmt.Errorf(
					"model %q accepts at most %d video(s); %d were attached. Attach fewer videos or switch to a model with a higher video cap.",
					req.Model, prop.MaxItems, len(sourceVideos))
			}
			sourceVideoProp = prop
			setBodyPath(schema, body, path, coerceVideos(prop, sourceVideos))
		} else {
			// The video is the request's whole purpose on an extend/motion/
			// reference turn — the gateway routes the matching model slot — so
			// submitting without it either fails server-side or extends
			// nothing. The replicate sibling resolver holds the same line.
			return nil, notices, fmt.Errorf(
				"the selected model %q has no source-video input to extend or reference; pick a different video model in Settings → Models",
				req.Model)
		}
	}
	// Keyframe mode: the first source image is the opening frame (image_url via
	// sourceImage), the last is the closing frame (the model's end-frame field).
	// The tool layer already guaranteed exactly two images. If the model has no
	// end-frame input, drop the closing frame with a notice and let the request
	// run as plain image-to-video from the opening frame — a partial loss that
	// still honors the opening frame, unlike a fully unmapped source side,
	// which refuses above.
	if req.Keyframes && len(sourceImages) >= 2 {
		endFrame := sourceImages[len(sourceImages)-1]
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "endImage"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, endFrame))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no end-frame input; animating from the start frame only.",
				req.Model))
		}
		sourceImages = sourceImages[:1]
	}
	if len(sourceImages) > 0 {
		path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceImage")
		if !ok {
			// Same refusal as an unmapped video side: the image is the whole
			// purpose of an image-to-video / motion / reference turn, and fal
			// accepts the submit without it — the model then fails demanding
			// its field or animates nothing of the user's frame (the wan 2.7
			// lesson, conv_d5f5177320da74a0ce3caebb).
			return nil, notices, fmt.Errorf(
				"the selected model %q has no source-image input to animate from; pick a different image-to-video model in Settings → Models",
				req.Model)
		} else {
			// Guardrail: multiple images into a scalar-image model is a hard error.
			// The model only accepts one frame; silently dropping the rest would
			// hide a real capability mismatch. The error names the model so the
			// user knows what to change.
			if prop.Kind != schemaArray && len(sourceImages) > 1 {
				return nil, notices, fmt.Errorf(
					"model %q accepts a single image; %d were attached. Use a multi-image model (e.g. bytedance/seedance-2.0/reference-to-video).",
					req.Model, len(sourceImages))
			}
			// Guardrail: a model declaring maxItems rejects requests above the cap.
			if prop.Kind == schemaArray && prop.MaxItems > 0 && len(sourceImages) > prop.MaxItems {
				return nil, notices, fmt.Errorf(
					"model %q accepts at most %d image(s); %d were attached. Attach fewer images or switch to a model with a higher image cap.",
					req.Model, prop.MaxItems, len(sourceImages))
			}
			sourceImageProp = prop
			setBodyPath(schema, body, path, coerceImages(prop, sourceImages))
		}
	}

	// Reference-token legend: some reference-input models (seedance, kling
	// o3/pro reference-to-video) address their sources in the prompt with
	// @ImageN/@VideoN tokens, per their own schema descriptions — but the
	// planner wrote the prompt before a model was chosen and may reference
	// attachments by filename, which the video model can't see
	// (conv_16bf42ce64997fad02f769a9). Append a legend mapping attachment order
	// onto the tokens the model's schema documents. It rides the outgoing body
	// only — ToolVideoResult.Prompt keeps the planner's original text, so
	// history and telemetry stay clean.
	var legend []string
	if len(sourceImages) > 0 && advertisesReferenceTokens(sourceImageProp) {
		tokens := make([]string, len(sourceImages))
		for i := range sourceImages {
			tokens[i] = fmt.Sprintf("@Image%d", i+1)
		}
		legend = append(legend, "the attached images, in attachment order, are "+strings.Join(tokens, ", "))
	}
	if len(sourceVideos) > 0 && advertisesReferenceTokens(sourceVideoProp) {
		tokens := make([]string, len(sourceVideos))
		for i := range sourceVideos {
			tokens[i] = fmt.Sprintf("@Video%d", i+1)
		}
		legend = append(legend, "the attached videos, in attachment order, are "+strings.Join(tokens, ", "))
	}
	if len(legend) > 0 {
		augmented := prompt
		if augmented != "" {
			augmented += "\n\n"
		}
		augmented += "Reference media for this request: " + strings.Join(legend, "; ") + "."
		if promptPath != "" {
			setBodyPath(schema, body, promptPath, coerceVideoValue(promptProp, augmented))
		} else {
			body["prompt"] = augmented
		}
	}

	// Motion-control models require character_orientation — the output's
	// orientation source (Kling v2.6: enum ["image","video"]; the request 422s
	// without it). Per fal's schema docs "video" (follow the motion video) suits
	// complex motion transfer — the dominant use — and allows up to 30s, while
	// "image" (follow the subject image) suits camera-following and caps at 10s.
	// Default the field to "video" whenever the model declares it; a model whose
	// enum lists other values is left to its own default rather than sent one it
	// would reject.
	if path, prop, ok := findNative(schema, ov, "video", req.Model, "characterOrientation"); ok {
		if valueAllowedByEnum(prop, "video") {
			setBodyPath(schema, body, path, coerceVideoValue(prop, "video"))
		}
	}
	// Multi-task reference models fold continuation into a `task` enum instead of
	// a dedicated extend endpoint: seedance-2.5 reference-to-video defaults to
	// "reference" (the clip guides a new video), so an extend-shaped turn sent
	// there without task:"extension" silently generates a new clip rather than
	// continuing the source. When the tool layer diagnosed the turn as an
	// extension, set the task wherever the model's schema declares one that lists
	// "extension". A model without the param (veo/ltx extend endpoints, minimax
	// reference-to-video) or with an enum lacking "extension" is left to its own
	// default rather than sent a value it would reject — the same enum-gated
	// defaulting as characterOrientation above.
	if req.ExtendSource {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "task"); ok {
			if valueAllowedByEnum(prop, "extension") {
				setBodyPath(schema, body, path, coerceVideoValue(prop, "extension"))
			}
		}
	}
	return body, notices, nil
}

// advertisesReferenceTokens reports whether a source field's own schema
// description tells the model to reference its entries in the prompt as
// @ImageN/@VideoN. Seedance and kling o3/pro reference-to-video document this;
// happy-horse and bernini-r take reference media but define no token syntax, so
// injecting tokens into their prompts would be literal noise. Gating on the
// description rather than a model list lets new models that document the same
// convention light up with no code change.
func advertisesReferenceTokens(prop SchemaProperty) bool {
	return strings.Contains(prop.Description, "@Image") || strings.Contains(prop.Description, "@Video")
}

// referenceStyleMediaInput reports whether a model's schema-declared source
// input treats its media as reference guidance rather than the canvas the
// output continues. Frame inputs are singular and say so (image_url, video_url
// — "the URL of the starting frame image"); reference inputs are plural,
// prompt-addressed arrays (image_urls, reference_image_urls, video_urls)
// whose descriptions either document the @ImageN/@VideoN token syntax or
// describe guidance. A plural-named input whose description talks about frames
// still counts as a frame input — the name is a naming convention, the
// description is the model's own semantics.
func referenceStyleMediaInput(name string, prop SchemaProperty) bool {
	if advertisesReferenceTokens(prop) {
		return true
	}
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	switch name {
	case "image_urls", "video_urls", "reference_image_urls", "reference_video_urls":
		return !strings.Contains(strings.ToLower(prop.Description), "frame")
	}
	return false
}

// isAutoAspectRatio reports whether a requested aspect ratio is the
// "let the model decide" sentinel rather than a concrete shape — seedance's
// own aspect_ratio enum vocabulary. On models that can't take it the request
// is satisfied by the model's own default, so an unsendable "auto" drops
// silently where a concrete ratio drops with a notice.
func isAutoAspectRatio(aspect string) bool {
	return strings.EqualFold(strings.TrimSpace(aspect), "auto")
}

// coerceVideoValue adapts a canonical video value to the native property's type.
// It is the video sibling of coerceImageValue: a schemaArray property (e.g. a
// model declaring image_urls rather than image_url) wraps a scalar into a slice.
// Strings and bools pass through unchanged; enum-valued properties are checked
// against their allowed values, dropping an invalid one with a notice rather
// than sending a value fal will reject. Unlike audio's coerceValue there is no
// unit conversion (duration stays in the caller's form; Veo takes "8s" strings,
// Kling takes numbers, and the caller configures whichever its model expects).
func coerceVideoValue(prop SchemaProperty, value any) any {
	if prop.Kind == schemaArray {
		return []any{value}
	}
	// Type-driven numeric coercion: a canonical duration arrives as a string
	// (from config/UI), but a model whose schema declares the field as integer
	// or number must receive a JSON number, not a string — happy-horse 422'd on
	// "10" because it expects integer 10 (conv_4feb919a). String-typed fields
	// (Veo "8s", Kling "5", Seedance "auto") are untouched. A non-numeric value
	// against a numeric field is returned as-is; the caller's enum guard drops
	// it with a notice rather than sending a malformed number.
	if prop.Type == "integer" || prop.Type == "number" {
		s := strings.TrimSpace(fmt.Sprintf("%v", value))
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			if prop.Type == "integer" {
				return int(n)
			}
			return n
		}
		return value
	}
	if len(prop.Enum) > 0 {
		s := fmt.Sprintf("%v", value)
		if !contains(prop.Enum, s) {
			return value // caller surfaces the mismatch via a notice if it cares
		}
	}
	return value
}

// valueAllowedByEnum reports whether value is accepted by prop's enum constraint.
// A property with no enum accepts anything; an enum-constrained property (like
// a video model's duration: Seedance allows "auto" plus "4".."15", Kling allows
// only "5"/"10") accepts only its listed values — case-insensitively, via
// enumValueFor, because planner-facing values and model enums don't always
// agree on casing (the resolution tiers are lowercase "480p"/"1080p" while
// minimax/h3-max lists "480P"/"768P"/"1080P"; an exact-match guard dropped
// every planner request as out-of-enum). Used to gate enum-sensitive
// fields before sending so an out-of-enum value is dropped with a notice rather
// than 422ing at fal. The value is stringified to match how enums are parsed
// from the schema (string literals).
func valueAllowedByEnum(prop SchemaProperty, value string) bool {
	_, ok := enumValueFor(prop, value)
	return ok
}

// enumValueFor matches value against a property's enum and returns the enum's
// own spelling for the body. The match is exact first, then case-insensitive:
// callers send the canonical member (not the caller's casing) so fal receives a
// value the model's schema documents. A property with no enum accepts anything
// and echoes the value unchanged; ok is false only when an enum is declared and
// the value matches no member under either comparison.
func enumValueFor(prop SchemaProperty, value string) (canonical string, ok bool) {
	if len(prop.Enum) == 0 {
		return value, true
	}
	for _, member := range prop.Enum {
		if member == value {
			return member, true
		}
	}
	for _, member := range prop.Enum {
		if strings.EqualFold(member, value) {
			return member, true
		}
	}
	return "", false
}

// videoDurationSendable reports whether a canonical duration value can be sent
// on the model's native duration property: it must pass the property's enum
// when one is declared, and parse as a number within the declared bounds when
// the property is integer/number-typed. The type leg is the fix for enum-less
// numeric durations — minimax/h3 declares duration as a bare integer (5–15,
// default 5), so Seedance-style "auto" passed the enum guard vacuously, rode
// coerceVideoValue through unchanged, and reached fal as a string that 422'd
// int_parsing (conv_2c4fa3a2fb515a792869ca1c, conv_c72f1ce6470a957fb4df62eb).
// The bounds leg keeps the picker's widened 30s ladder from 422ing on
// bounded-numeric models (wan-2.7-i2v's integer 2–15): an out-of-range value
// drops with a notice like an out-of-enum one, instead of failing the
// generation server-side. String-typed durations (Veo "8s", Kling "5",
// Seedance's own "auto" enum member) keep flowing through the enum leg
// untouched.
func videoDurationSendable(prop SchemaProperty, duration string) bool {
	if !valueAllowedByEnum(prop, duration) {
		return false
	}
	if prop.Type == "integer" || prop.Type == "number" {
		v, err := strconv.ParseFloat(strings.TrimSpace(duration), 64)
		if err != nil {
			return false
		}
		if prop.Minimum != nil && v < *prop.Minimum {
			return false
		}
		if prop.Maximum != nil && v > *prop.Maximum {
			return false
		}
		return true
	}
	return true
}

// videoDurationDroppedNotice explains a dropped duration. "auto" is a
// resolution rather than a rejection — the model picks its own length when the
// field is omitted, and the schema's declared default is named so the user
// knows what the clip will get — while any other value reads as the plain
// refusal the enum guard has always emitted.
func videoDurationDroppedNotice(model, duration string, prop SchemaProperty) string {
	if duration == "auto" {
		if def := schemaDefaultSeconds(prop); def != "" {
			return fmt.Sprintf("The selected model %q has no auto duration; using its default of %s seconds.", model, def)
		}
		return fmt.Sprintf("The selected model %q has no auto duration; letting the model choose the clip length.", model)
	}
	return fmt.Sprintf("The selected model %q does not accept duration %q; ignoring it and letting the model choose.", model, duration)
}

// schemaDefaultSeconds renders a duration property's declared default as
// notice-facing seconds text ("5"), or "" when none is declared. JSON decoding
// lands schema numbers as float64; FormatFloat's shortest form prints integral
// values without a trailing .0.
func schemaDefaultSeconds(prop SchemaProperty) string {
	switch v := prop.Default.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// resolveLipsyncBody maps a LipsyncGenerateRequest onto the model's native input
// schema, returning the fal body and user-facing notices. The driving audio is
// always required; the face source is either an image (audio-to-video) or a
// video (video-to-video) — the tool guarantees exactly one is set before this
// runs. fal requires HTTP(S) URLs or data URIs (it rejects bare base64 with a
// 422), so falAudioURL/falImageURL/falVideoURL normalize each. A nil schema
// yields a generic body with the audio + whichever source is present, plus a
// notice.
//
// The face source is the tool's entire purpose, so an unmapped face input is a
// fatal error, not a graceful drop: without it the model can't do lip sync and
// the request would 422 downstream with a confusing "field required" message
// (see conv_ff1caffa123d39a9fd98f2ac, where a video-only Kling endpoint was
// selected for an audio+image turn and the dropped image became a 422). The
// audio input, by contrast, stays a notice — some endpoints can drive a face
// from text, so a missing audio field is degradable.
func resolveLipsyncBody(schema *ModelInputSchema, req LipsyncGenerateRequest, ov Overrides) (map[string]any, []string, error) {
	audio := falAudioURL(strings.TrimSpace(req.Audio))
	image := falImageURL(strings.TrimSpace(req.Image))
	video := falVideoURL(strings.TrimSpace(req.Video))

	if schema == nil {
		body := map[string]any{}
		if audio != "" {
			body["audio_url"] = audio
		}
		if video != "" {
			body["video_url"] = video
		} else if image != "" {
			body["image_url"] = image
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported input."}, nil
	}

	body := map[string]any{}
	var notices []string

	if audio != "" {
		if path, prop, ok := findNative(schema, ov, "lipsync", req.Model, "sourceAudio"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, audio))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no audio input; the attached audio was ignored.",
				req.Model))
		}
	}

	// The face source: video wins over image (the tool sets exactly one, but
	// resolve defensively in case both are present). A face input the model
	// cannot accept is fatal — the tool cannot produce a lip sync without a
	// face, and sending the request anyway yields a downstream 422 that reads
	// as a confusing "field required" rather than a model-mismatch.
	switch {
	case video != "":
		if path, prop, ok := findNative(schema, ov, "lipsync", req.Model, "sourceVideo"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, video))
		} else {
			return nil, notices, fmt.Errorf(
				"the lip sync model %q has no video input — it cannot lip-sync an attached video; attach an image instead or pick a video-capable model in Settings",
				req.Model)
		}
	case image != "":
		if path, prop, ok := findNative(schema, ov, "lipsync", req.Model, "sourceImage"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, image))
		} else {
			return nil, notices, fmt.Errorf(
				"the lip sync model %q has no image input — it cannot lip-sync an attached image; attach a video instead or pick an image-capable model in Settings",
				req.Model)
		}
	}
	return body, notices, nil
}

// resolveVideoUpscaleBody maps a VideoUpscaleRequest onto the model's native
// input schema, returning the fal body and user-facing notices. The source clip
// is the tool's entire purpose, so an unmapped video input is fatal (mirroring
// resolveLipsyncBody's face-source rule): sending anyway would 422 downstream
// with a confusing "field required". A missing scale input is degradable — the
// model applies its own default factor — so that stays a notice. A nil schema
// yields the legacy {video_url, scale} body plus a notice. The scale synonym
// table maps the canonical factor onto whichever name the endpoint declares
// (scale / scale_factor / upscale_factor).
func resolveVideoUpscaleBody(schema *ModelInputSchema, req VideoUpscaleRequest, ov Overrides) (map[string]any, []string, error) {
	video := falVideoURL(strings.TrimSpace(req.Video))

	if schema == nil {
		body := map[string]any{"video_url": video}
		if req.Scale > 0 {
			body["scale"] = req.Scale
		}
		return body, []string{"Couldn't load the model's parameter schema; sent the source video and scale with default field names."}, nil
	}

	body := map[string]any{}
	var notices []string

	if path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceVideo"); ok {
		setBodyPath(schema, body, path, coerceVideoValue(prop, video))
	} else {
		return nil, notices, fmt.Errorf(
			"the upscale model %q has no video input — it cannot upscale an attached video; pick a video upscaler in Settings",
			req.Model)
	}
	if req.Scale > 0 {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "scale"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, req.Scale))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no scale control; using the model's default upscale factor.",
				req.Model))
		}
	}
	return body, notices, nil
}

// resolveVideoReframeBody maps a VideoReframeRequest onto the model's native
// input schema, returning the fal body and user-facing notices. Both the
// source clip and the target aspect ratio are the tool's entire purpose, so an
// unmapped video input OR aspect-ratio input is fatal (mirroring
// resolveVideoUpscaleBody's source rule): a reframe endpoint that can't be
// told the target shape can't reframe, and sending anyway would 422 downstream
// or run with the model's default ratio — the wrong deliverable. An
// out-of-enum ratio is fatal too, naming the model's supported set: unlike
// generate_video's drop-with-notice guard (where the model's own default is a
// legitimate fallback), a reframe that silently picks a different ratio is
// not what the user asked for. Resolution is degradable — endpoints apply
// their own default tier — so a missing or unsupported tier stays a notice.
// A nil schema yields the legacy {video_url, aspect_ratio} body plus a notice.
func resolveVideoReframeBody(schema *ModelInputSchema, req VideoReframeRequest, ov Overrides) (map[string]any, []string, error) {
	video := falVideoURL(strings.TrimSpace(req.Video))
	aspect := strings.TrimSpace(req.AspectRatio)

	if schema == nil {
		body := map[string]any{"video_url": video, "aspect_ratio": aspect}
		if res := strings.TrimSpace(req.Resolution); res != "" {
			body["resolution"] = res
		}
		return body, []string{"Couldn't load the model's parameter schema; sent the source video and aspect ratio with default field names."}, nil
	}

	body := map[string]any{}
	var notices []string

	if path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceVideo"); ok {
		setBodyPath(schema, body, path, coerceVideoValue(prop, video))
	} else {
		return nil, notices, fmt.Errorf(
			"the reframe model %q has no video input — it cannot reframe an attached video; pick a reframe model in Settings",
			req.Model)
	}
	if path, prop, ok := findNative(schema, ov, "video", req.Model, "aspectRatio"); ok {
		if canonical, allowed := enumValueFor(prop, aspect); allowed {
			setBodyPath(schema, body, path, coerceVideoValue(prop, canonical))
		} else {
			return nil, notices, fmt.Errorf(
				"the reframe model %q does not accept aspect ratio %q — it supports %s; pick a supported ratio or a different reframe model in Settings",
				req.Model, aspect, strings.Join(prop.Enum, ", "))
		}
	} else {
		return nil, notices, fmt.Errorf(
			"the reframe model %q has no aspect-ratio input — it cannot be told the target shape; pick a reframe model in Settings",
			req.Model)
	}
	if res := strings.TrimSpace(req.Resolution); res != "" {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "resolution"); ok {
			if canonical, allowed := enumValueFor(prop, res); allowed {
				setBodyPath(schema, body, path, coerceVideoValue(prop, canonical))
			} else {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not support resolution %q; using the model's default tier.",
					req.Model, res))
			}
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no resolution control; using the model's default tier.",
				req.Model))
		}
	}
	return body, notices, nil
}

// resolveVideoRestyleBody maps a VideoRestyleRequest onto the model's native
// input schema, returning the fal body and user-facing notices. The source clip
// and the prompt are the tool's entire purpose, so an unmapped video input OR
// prompt input is fatal (mirroring resolveVideoReframeBody's rules): a restyle
// endpoint that can't be told the new look can't restyle, and sending anyway
// would 422 downstream or re-render with the model's default style — the wrong
// deliverable. Reference images, resolution, and the negative prompt are
// degradable — they refine the look rather than define it — so a model without
// the matching input drops that side with a notice. A nil schema yields the
// legacy {video_url, prompt} body plus a notice.
func resolveVideoRestyleBody(schema *ModelInputSchema, req VideoRestyleRequest, ov Overrides) (map[string]any, []string, error) {
	video := falVideoURL(strings.TrimSpace(req.Video))
	prompt := strings.TrimSpace(req.Prompt)
	images := make([]string, 0, 4)
	for _, img := range req.Images {
		if u := falImageURL(strings.TrimSpace(img)); u != "" {
			images = append(images, u)
		}
	}

	if schema == nil {
		body := map[string]any{"video_url": video, "prompt": prompt}
		if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
			body["negative_prompt"] = negative
		}
		if res := strings.TrimSpace(req.Resolution); res != "" {
			body["resolution"] = res
		}
		return body, []string{"Couldn't load the model's parameter schema; sent the source video and style prompt with default field names."}, nil
	}

	body := map[string]any{}
	var notices []string

	if path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceVideo"); ok {
		setBodyPath(schema, body, path, coerceVideoValue(prop, video))
	} else {
		return nil, notices, fmt.Errorf(
			"the restyle model %q has no video input — it cannot restyle an attached video; pick a restyle model in Settings",
			req.Model)
	}
	if path, prop, ok := findNative(schema, ov, "video", req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, coerceVideoValue(prop, prompt))
	} else {
		return nil, notices, fmt.Errorf(
			"the restyle model %q has no prompt input — it cannot be told the new look; pick a restyle model in Settings",
			req.Model)
	}
	// Reference images refine the look (a character sheet, a style frame) rather
	// than define it, so a model without an image input drops them with a
	// notice and the prompt carries the style alone. A scalar image input takes
	// the first image only; a maxItems-capped array is trimmed to the cap —
	// notices rather than the hard errors resolveVideoBody raises for its
	// primary sources.
	if len(images) > 0 {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceImage"); ok {
			switch {
			case prop.Kind != schemaArray && len(images) > 1:
				setBodyPath(schema, body, path, coerceVideoValue(prop, images[0]))
				notices = append(notices, fmt.Sprintf(
					"The selected model %q accepts a single reference image; the first of %d was sent.",
					req.Model, len(images)))
			case prop.Kind == schemaArray && prop.MaxItems > 0 && len(images) > prop.MaxItems:
				setBodyPath(schema, body, path, coerceImages(prop, images[:prop.MaxItems]))
				notices = append(notices, fmt.Sprintf(
					"The selected model %q accepts at most %d reference image(s); the first %d were sent.",
					req.Model, prop.MaxItems, prop.MaxItems))
			default:
				setBodyPath(schema, body, path, coerceImages(prop, images))
			}
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no reference-image input; the attached image(s) were ignored and the style comes from the prompt alone.",
				req.Model))
		}
	}
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "negativePrompt"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, negative))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no negative-prompt control; ignoring the requested negative prompt.",
				req.Model))
		}
	}
	if res := strings.TrimSpace(req.Resolution); res != "" {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "resolution"); ok {
			if canonical, allowed := enumValueFor(prop, res); allowed {
				setBodyPath(schema, body, path, coerceVideoValue(prop, canonical))
			} else {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not support resolution %q; using the model's default tier.",
					req.Model, res))
			}
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no resolution control; using the model's default tier.",
				req.Model))
		}
	}
	return body, notices, nil
}

// category selects the synonym table and override namespace ("audio", "image",
// findNative resolves canon → native dot-path via override, top-level scan, then
// one-level nested scan. Returns the matched leaf property for coercion.
// category selects the synonym table and override namespace ("audio", "image",
// "video", or "lipsync").
//
// When an override names a field the schema does not declare, the returned
// property is fabricated from the override's Kind hint (defaulting to scalar):
// an override with Kind:"array" yields a schemaArray property so the existing
// array coercion/guardrails downstream apply unchanged. This is how an override
// compensates for a model whose runtime disagrees with its published schema
// (e.g. Kling image-to-video demanding image_urls as a list while its schema
// declares a scalar image_url).
func findNative(schema *ModelInputSchema, ov Overrides, category, model, canon string) (string, SchemaProperty, bool) {
	if entry, ok := ov.lookup(category, model, canon); ok {
		if entry.Path == "" {
			return "", SchemaProperty{}, false // explicitly unsupported
		}
		if prop, ok := propAtPath(schema, entry.Path); ok {
			return entry.Path, prop, true
		}
		return entry.Path, fabricateOverrideProperty(entry), true
	}
	syns := synonymsFor(category, canon)
	for _, name := range syns {
		if prop, ok := schema.property(name); ok {
			return name, prop, true
		}
	}
	for _, obj := range schema.objectProps() {
		for _, name := range syns {
			if sub, ok := obj.Nested[name]; ok {
				return obj.Name + "." + name, sub, true
			}
		}
	}
	return "", SchemaProperty{}, false
}

// fabricateOverrideProperty builds a SchemaProperty for an override path that
// isn't present in the model's schema, using the override's Kind hint. An
// "array" kind yields schemaArray (with string elements unless Items overrides),
// so the existing array coercion and multi-image guardrails apply as if the
// schema had declared it. Any other/empty kind yields a plain scalar — the
// pre-override behavior.
func fabricateOverrideProperty(entry overrideEntry) SchemaProperty {
	if entry.Kind == "array" {
		itemsType := entry.Items
		if itemsType == "" {
			itemsType = "string"
		}
		return SchemaProperty{
			Name:     entry.Path,
			Kind:     schemaArray,
			Type:     "array",
			Items:    &SchemaProperty{Name: entry.Path, Kind: schemaScalar, Type: itemsType},
			MaxItems: entry.MaxItems,
		}
	}
	return SchemaProperty{Name: entry.Path, Kind: schemaScalar, Type: entry.Kind}
}

// synonymsFor returns the native-name candidates for (category, canon), or nil
// when the pair isn't in any synonym table. The replicate categories route to
// the Replicate synonym tables (replicate_params.go) through the same
// findNative machinery — the schema walk, guardrails, and coercion are shared;
// only the candidate native names differ.
func synonymsFor(category, canon string) []string {
	switch category {
	case "audio":
		return audioSynonyms[canon]
	case "image":
		return imageSynonyms[canon]
	case "video":
		return videoSynonyms[canon]
	case "lipsync":
		return lipsyncSynonyms[canon]
	case "replicate-image":
		return replicateImageSynonyms[canon]
	case "replicate-video":
		return replicateVideoSynonyms[canon]
	case "replicate-upscale":
		return replicateUpscaleSynonyms[canon]
	case "replicate-video-upscale":
		return replicateVideoUpscaleSynonyms[canon]
	case "replicate-video-restyle":
		return replicateVideoRestyleSynonyms[canon]
	case "replicate-video-reframe":
		return replicateVideoReframeSynonyms[canon]
	}
	return nil
}

func propAtPath(schema *ModelInputSchema, path string) (SchemaProperty, bool) {
	parts := strings.SplitN(path, ".", 2)
	top, ok := schema.property(parts[0])
	if !ok {
		return SchemaProperty{}, false
	}
	if len(parts) == 1 {
		return top, true
	}
	sub, ok := top.Nested[parts[1]]
	return sub, ok
}

// coerceValue converts a canonical value to the native property's type and
// applies transforms (seconds→ms for *_ms fields; enum validation). Returns a
// notice string instead of a value when the value is invalid for the property.
func coerceValue(canon string, prop SchemaProperty, value any, model string) (any, string) {
	switch canon {
	case "duration":
		secs, err := strconv.ParseFloat(fmt.Sprintf("%v", value), 64)
		if err != nil {
			return nil, fmt.Sprintf("Ignored duration %q for %q: not a number.", value, model)
		}
		if strings.HasSuffix(prop.Name, "_ms") {
			return secs * 1000, ""
		}
		return secs, ""
	case "loop":
		return true, ""
	default: // voice, negativePrompt — string values, enum-checked
		s := fmt.Sprintf("%v", value)
		if len(prop.Enum) > 0 && !contains(prop.Enum, s) {
			return nil, fmt.Sprintf("%q isn't a valid %s for %q; valid: %s.",
				s, canonLabel(canon), model, strings.Join(prop.Enum, ", "))
		}
		return s, ""
	}
}

// setBodyPath writes value at a dot-path, seeding a nested object from the
// schema's default so sibling defaults survive the merge.
func setBodyPath(schema *ModelInputSchema, body map[string]any, path string, value any) {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		body[parts[0]] = value
		return
	}
	obj, ok := body[parts[0]].(map[string]any)
	if !ok {
		obj = map[string]any{}
		if top, ok := schema.property(parts[0]); ok {
			if def, ok := top.Default.(map[string]any); ok {
				for k, v := range def {
					obj[k] = v
				}
			}
		}
	}
	obj[parts[1]] = value
	body[parts[0]] = obj
}

func canonLabel(canon string) string {
	if canon == "negativePrompt" {
		return "negative prompt"
	}
	if canon == "sourceAudio" {
		return "voice reference"
	}
	return canon
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
