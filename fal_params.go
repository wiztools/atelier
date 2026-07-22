package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Overrides maps category → model-id → canonical → native path. A native path of
// "" means the canonical param is explicitly unsupported (drop-with-notice).
// Missing entries fall through to schema heuristics.
type Overrides struct {
	byCategory map[string]map[string]map[string]string
}

func (o Overrides) lookup(category, model, canon string) (string, bool) {
	models, ok := o.byCategory[category]
	if !ok {
		return "", false
	}
	params, ok := models[model]
	if !ok {
		return "", false
	}
	native, ok := params[canon]
	return native, ok
}

// builtinFalOverrides holds defaults for models the heuristics get wrong. Empty
// today; entries are added as such models are discovered.
func builtinFalOverrides() Overrides {
	return Overrides{byCategory: map[string]map[string]map[string]string{
		"audio":   {},
		"image":   {},
		"video":   {},
		"lipsync": {},
	}}
}

// loadFalOverrides reads <storageRoot>/fal-overrides.json and merges it OVER the
// built-in defaults. A missing or malformed file yields the built-ins.
func loadFalOverrides(storageRoot string) Overrides {
	ov := builtinFalOverrides()
	data, err := os.ReadFile(filepath.Join(storageRoot, "fal-overrides.json"))
	if err != nil {
		return ov
	}
	var parsed map[string]map[string]map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ov // malformed → built-ins
	}
	for category, models := range parsed {
		if ov.byCategory[category] == nil {
			ov.byCategory[category] = map[string]map[string]string{}
		}
		for model, params := range models {
			if ov.byCategory[category][model] == nil {
				ov.byCategory[category][model] = map[string]string{}
			}
			for canon, native := range params {
				ov.byCategory[category][model][canon] = native
			}
		}
	}
	return ov
}

// audioSynonyms lists, per canonical param, the native key names to look for in
// a model's schema (scanned in order, top-level then one-level nested).
var audioSynonyms = map[string][]string{
	"prompt":         {"prompt", "text"},
	"duration":       {"duration_seconds", "duration", "music_length_ms"},
	"loop":           {"loop"},
	"voice":          {"voice", "voice_id", "voice_name", "speaker", "speaker_id"},
	"negativePrompt": {"negative_prompt"},
}

// imageSynonyms lists, per canonical param, the native key names to look for in
// an image model's schema. `sourceImage` is the cross-model abstraction for the
// frame a user attached to transform: flux/dev/image-to-image declares
// `image_url` (scalar), nano-banana-pro declares `image_urls` (array). The
// resolver wraps to a slice when the matched property is schemaArray.
var imageSynonyms = map[string][]string{
	"prompt":            {"prompt"},
	"sourceImage":       {"image_url", "image_urls"},
	"imageSize":         {"image_size", "size"},
	"numImages":         {"num_images"},
	"numInferenceSteps": {"num_inference_steps"},
}

// videoSynonyms lists, per canonical param, the native key names to look for in
// a video model's schema. `sourceImage` is the image-to-video frame; `sourceVideo`
// is the clip a Veo extend endpoint continues. aspectRatio covers Veo's
// "aspect_ratio" and any camelCase variant; duration is model-dependent (Veo
// wants "8s" strings, Kling wants numbers — coerceVideoValue handles both).
var videoSynonyms = map[string][]string{
	"prompt":         {"prompt"},
	"duration":       {"duration"},
	"aspectRatio":    {"aspect_ratio", "aspectRatio", "size"},
	"negativePrompt": {"negative_prompt"},
	"sourceImage":    {"image_url", "image_urls"},
	"sourceVideo":    {"video_url"},
	"generateAudio":  {"generate_audio"},
}

// lipsyncSynonyms lists, per canonical param, the native key names to look for
// in a lip sync model's schema. sourceAudio is the driving audio track;
// sourceImage is the face for audio-to-video; sourceVideo is the clip for
// video-to-video.
var lipsyncSynonyms = map[string][]string{
	"sourceAudio": {"audio_url", "audio_file_url", "audio"},
	"sourceImage": {"image_url", "image_urls"},
	"sourceVideo": {"video_url"},
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
		{"negativePrompt", strings.TrimSpace(req.NegativePrompt), strings.TrimSpace(req.NegativePrompt) != ""},
	}
}

// resolveAudioBody maps a canonical AudioGenerateRequest onto model's native
// input schema, returning the fal body and user-facing notices for anything
// dropped. A nil schema (unavailable) yields a generic prompt+text body.
func resolveAudioBody(schema *ModelInputSchema, req AudioGenerateRequest, ov Overrides) (map[string]any, []string) {
	prompt := strings.TrimSpace(req.Prompt)
	if schema == nil {
		return map[string]any{"prompt": prompt, "text": prompt},
			[]string{"Couldn't load the model's parameter schema; generated with defaults and skipped duration/loop/voice."}
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

// resolveImageBody maps a canonical ImageGenerateRequest onto the model's native
// input schema, returning the fal body and user-facing notices for anything
// dropped. A nil schema (unavailable) yields the legacy hardcoded body
// ({prompt, num_images, image_url?|image_size?, num_inference_steps?}) plus a
// notice. This is the image sibling of resolveAudioBody; the only image-specific
// rule is that a source-image field whose schema kind is schemaArray
// (e.g. nano-banana-pro's image_urls) wraps the single URL into a slice.
func resolveImageBody(schema *ModelInputSchema, req ImageGenerateRequest, ov Overrides) (map[string]any, []string) {
	prompt := strings.TrimSpace(req.Prompt)
	// fal requires an HTTP(S) URL or a data URI and rejects bare base64 with a
	// 422; the rest of the app carries attached images as bare base64 (the
	// data: prefix is stripped for Ollama), so normalize here. This was the
	// GenerateImage client's job before the resolver refactor; it moves here
	// because the resolver now owns body construction.
	sourceImage := falImageURL(firstNonEmpty(req.Images))
	if schema == nil {
		body := map[string]any{
			"prompt":     prompt,
			"num_images": 1,
		}
		if sourceImage != "" {
			body["image_url"] = sourceImage
		} else if req.Width > 0 && req.Height > 0 {
			body["image_size"] = map[string]any{"width": req.Width, "height": req.Height}
		}
		if req.Steps > 0 {
			body["num_inference_steps"] = req.Steps
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported image input."}
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

	// Image-to-image takes the source frame; image_size is omitted (fal derives
	// dims from the source). Text-to-image takes the configured dimensions.
	if sourceImage != "" {
		if path, prop, ok := findNative(schema, ov, "image", req.Model, "sourceImage"); ok {
			setBodyPath(schema, body, path, coerceImageValue(prop, sourceImage))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-image input; the attached image was ignored.",
				req.Model))
		}
	} else if req.Width > 0 && req.Height > 0 {
		if path, _, ok := findNative(schema, ov, "image", req.Model, "imageSize"); ok {
			setBodyPath(schema, body, path, map[string]any{"width": req.Width, "height": req.Height})
		} else {
			body["image_size"] = map[string]any{"width": req.Width, "height": req.Height}
		}
	}

	if req.Steps > 0 {
		if path, prop, ok := findNative(schema, ov, "image", req.Model, "numInferenceSteps"); ok {
			setBodyPath(schema, body, path, coerceImageValue(prop, req.Steps))
		} else {
			body["num_inference_steps"] = req.Steps
		}
	}
	return body, notices
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

// resolveVideoBody maps a canonical VideoGenerateRequest onto the model's native
// input schema, returning the fal body and user-facing notices for anything
// dropped. It is the video sibling of resolveImageBody. A nil schema
// (unavailable) yields the legacy hardcoded body — the fields GenerateVideo used
// to build itself before the resolver refactor — plus a notice, so fal models
// without a published schema keep working.
//
// Source media is resolved in priority order: an attached Video (extend) wins,
// then an attached Image (image-to-video); both are absent for text-to-video.
// fal requires an HTTP(S) URL or a data URI and rejects bare base64 with a 422,
// so falImageURL/falVideoURL normalize each. A media field the selected model
// lacks is dropped with a notice rather than sent.
func resolveVideoBody(schema *ModelInputSchema, req VideoGenerateRequest, ov Overrides) (map[string]any, []string) {
	prompt := strings.TrimSpace(req.Prompt)
	sourceImage := falImageURL(strings.TrimSpace(req.Image))
	sourceVideo := falVideoURL(strings.TrimSpace(req.Video))
	if schema == nil {
		body := map[string]any{"prompt": prompt}
		if duration := strings.TrimSpace(req.Duration); duration != "" {
			body["duration"] = duration
		}
		if aspect := strings.TrimSpace(req.AspectRatio); aspect != "" {
			body["aspect_ratio"] = aspect
		}
		if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
			body["negative_prompt"] = negative
		}
		if req.GenerateAudio != nil {
			body["generate_audio"] = *req.GenerateAudio
		}
		if sourceVideo != "" {
			body["video_url"] = sourceVideo
		} else if sourceImage != "" {
			body["image_url"] = sourceImage
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported video input."}
	}

	body := map[string]any{}
	var notices []string

	if path, prop, ok := findNative(schema, ov, "video", req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, coerceVideoValue(prop, prompt))
	} else {
		body["prompt"] = prompt
	}

	if duration := strings.TrimSpace(req.Duration); duration != "" {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "duration"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, duration))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no duration control; ignoring the requested duration.",
				req.Model))
		}
	}
	if aspect := strings.TrimSpace(req.AspectRatio); aspect != "" {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "aspectRatio"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, aspect))
		} else {
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
	if req.GenerateAudio != nil {
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "generateAudio"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, *req.GenerateAudio))
		}
		// Models without a generate_audio field silently ignore it — no notice,
		// matching the "endpoints that never emit audio ignore it" contract on
		// VideoGenerateRequest.GenerateAudio.
	}

	// Source media: extend (video) takes precedence over image-to-video.
	switch {
	case sourceVideo != "":
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceVideo"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, sourceVideo))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-video input; the attached video was ignored.",
				req.Model))
		}
	case sourceImage != "":
		if path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceImage"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, sourceImage))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-image input; the attached image was ignored.",
				req.Model))
		}
	}
	return body, notices
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
	if len(prop.Enum) > 0 {
		s := fmt.Sprintf("%v", value)
		if !contains(prop.Enum, s) {
			return value // caller surfaces the mismatch via a notice if it cares
		}
	}
	return value
}

// resolveLipsyncBody maps a LipsyncGenerateRequest onto the model's native input
// schema, returning the fal body and user-facing notices. The driving audio is
// always required; the face source is either an image (audio-to-video) or a
// video (video-to-video) — the tool guarantees exactly one is set before this
// runs. fal requires HTTP(S) URLs or data URIs (it rejects bare base64 with a
// 422), so falAudioURL/falImageURL/falVideoURL normalize each. A nil schema
// yields a generic body with the audio + whichever source is present, plus a
// notice. A media field the selected model lacks is dropped with a notice.
func resolveLipsyncBody(schema *ModelInputSchema, req LipsyncGenerateRequest, ov Overrides) (map[string]any, []string) {
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
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported input."}
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
	// resolve defensively in case both are present).
	switch {
	case video != "":
		if path, prop, ok := findNative(schema, ov, "lipsync", req.Model, "sourceVideo"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, video))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-video input; the attached video was ignored.",
				req.Model))
		}
	case image != "":
		if path, prop, ok := findNative(schema, ov, "lipsync", req.Model, "sourceImage"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, image))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-image input; the attached image was ignored.",
				req.Model))
		}
	}
	return body, notices
}

// findNative resolves canon → native dot-path via override, top-level scan, then
// one-level nested scan. Returns the matched leaf property for coercion.
// category selects the synonym table and override namespace ("audio", "image",
// "video", or "lipsync").
func findNative(schema *ModelInputSchema, ov Overrides, category, model, canon string) (string, SchemaProperty, bool) {
	if native, ok := ov.lookup(category, model, canon); ok {
		if native == "" {
			return "", SchemaProperty{}, false // explicitly unsupported
		}
		if prop, ok := propAtPath(schema, native); ok {
			return native, prop, true
		}
		return native, SchemaProperty{Name: native, Kind: schemaScalar}, true
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

// synonymsFor returns the native-name candidates for (category, canon), or nil
// when the pair isn't in any synonym table.
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
