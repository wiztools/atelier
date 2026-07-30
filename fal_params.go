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

	// Image-to-image takes the source frame(s). Many edit models (e.g. seedream
	// edit) also accept an explicit image_size alongside the source image, so an
	// aspect-ratio preset is sent when the schema supports it — otherwise the
	// output inherits the source's orientation and a "make this 9:16" request on
	// a landscape source silently stays landscape (see conv_711ebd5f). For models
	// that derive dims purely from the source, no image_size input exists and the
	// preset is correctly omitted. Text-to-image always takes the configured
	// dimensions.
	if len(sourceImages) > 0 {
		path, prop, ok := findNative(schema, ov, "image", req.Model, "sourceImage")
		if !ok {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-image input; the attached image(s) were ignored.",
				req.Model))
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
		// Send the aspect-ratio preset when the edit model accepts image_size.
		// Only the preset enum is sent here — never raw width/height, since the
		// source frame sets the resolution and a pixel object could conflict.
		if preset := falImageSizePreset(req.AspectRatio); preset != "" {
			if sizePath, sizeProp, hasSize := findNative(schema, ov, "image", req.Model, "imageSize"); hasSize {
				if len(sizeProp.Enum) == 0 || valueAllowedByEnum(sizeProp, preset) {
					setBodyPath(schema, body, sizePath, preset)
				}
			}
		}
	} else if req.Width > 0 && req.Height > 0 {
		sendImageSize(schema, ov, body, req)
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

// sendImageSize writes the image_size field onto the fal request body, choosing
// between an aspect-ratio preset enum string and a {width,height} object.
//
// Some fal image models (notably seedream) accept an aspect-ratio preset enum on
// image_size ("landscape_16_9", "portrait_16_9", ...) and either ignore or
// reject a {width,height} object below their minimum pixel area. The derived
// short-edge dims for a portrait ratio routinely fall under that floor, so the
// model falls back to a default landscape size and the requested aspect ratio is
// lost — see conv_b02dc16f: a 9:16 request returned a 2736x1536 landscape image.
// When the requested ratio maps to a known preset, prefer the enum the model
// honors; otherwise send the pixel object (the original behavior).
func sendImageSize(schema *ModelInputSchema, ov Overrides, body map[string]any, req ImageGenerateRequest) {
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
		return
	}
	pixels := map[string]any{"width": req.Width, "height": req.Height}
	if hasSize {
		setBodyPath(schema, body, path, pixels)
	} else {
		body["image_size"] = pixels
	}
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

// resolveVideoBody maps a canonical VideoGenerateRequest onto the model's native
// input schema, returning the fal body, user-facing notices for anything
// dropped, and an error for a hard capability mismatch (multi-image into a
// single-image model). It is the video sibling of resolveImageBody. A nil schema
// (unavailable) yields the legacy hardcoded body — the fields GenerateVideo used
// to build itself before the resolver refactor — plus a notice, so fal models
// without a published schema keep working.
//
// Source media is resolved in priority order: an attached Video (extend) wins,
// then one or more attached Images (image-to-video or multi-image reference-to-
// video); all are absent for text-to-video. fal requires an HTTP(S) URL or a
// data URI and rejects bare base64 with a 422, so falImageURL/falVideoURL
// normalize each. A media field the selected model lacks is dropped with a
// notice rather than sent. Multiple images into a model whose source-image
// field is scalar (or into the no-schema legacy path, which only knows a single
// image_url) is a hard error — the caller fails the tool call so the user
// knows to switch models rather than silently losing all but one image.
func resolveVideoBody(schema *ModelInputSchema, req VideoGenerateRequest, ov Overrides) (map[string]any, []string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	// SourceImages() unifies the new Images slice and the legacy scalar Image.
	sourceImages := make([]string, 0, 4)
	for _, img := range req.SourceImages() {
		if u := falImageURL(strings.TrimSpace(img)); u != "" {
			sourceImages = append(sourceImages, u)
		}
	}
	sourceVideo := falVideoURL(strings.TrimSpace(req.Video))
	if schema == nil {
		// The legacy fallback only knows a scalar image_url; multiple images
		// can't be expressed without a schema to map them onto, so fail rather
		// than silently drop the extras.
		if len(sourceImages) > 1 {
			return nil, nil, fmt.Errorf(
				"model %q could not be queried for its parameter schema and accepts at most one image; %d were attached. Configure a multi-image video model (e.g. bytedance/seedance-2.0/reference-to-video).",
				req.Model, len(sourceImages))
		}
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
		} else if len(sourceImages) == 1 {
			body["image_url"] = sourceImages[0]
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported video input."}, nil
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
			// Guard against values the model's duration enum doesn't accept before
			// sending — "auto" is Seedance-only, while Kling accepts just ["5","10"]
			// and other models have their own fixed sets. Passing an out-of-enum
			// value through would 422 at fal. Drop it with a notice so the request
			// still runs (the model picks its own default) rather than failing.
			if !valueAllowedByEnum(prop, duration) {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not accept duration %q; ignoring it and letting the model choose.",
					req.Model, duration))
			} else {
				setBodyPath(schema, body, path, coerceVideoValue(prop, duration))
			}
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
	case len(sourceImages) > 0:
		path, prop, ok := findNative(schema, ov, "video", req.Model, "sourceImage")
		if !ok {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-image input; the attached image(s) were ignored.",
				req.Model))
			break
		}
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
		setBodyPath(schema, body, path, coerceImages(prop, sourceImages))
	}
	return body, notices, nil
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

// valueAllowedByEnum reports whether value is accepted by prop's enum constraint.
// A property with no enum accepts anything; an enum-constrained property (like
// a video model's duration: Seedance allows "auto" plus "4".."15", Kling allows
// only "5"/"10") accepts only its listed values. Used to gate enum-sensitive
// fields before sending so an out-of-enum value is dropped with a notice rather
// than 422ing at fal. The value is stringified to match how enums are parsed
// from the schema (string literals).
func valueAllowedByEnum(prop SchemaProperty, value string) bool {
	if len(prop.Enum) == 0 {
		return true
	}
	return contains(prop.Enum, value)
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
