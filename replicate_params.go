package main

import (
	"errors"
	"fmt"
	"strings"
)

// The Replicate param layer maps Atelier's canonical image/video requests onto
// a Replicate model's native input schema through the same findNative
// machinery the fal resolvers use (fal_params.go) — the schema walk, enum
// guards, coercion, and guardrails are shared; only the candidate native names
// differ (Replicate models say "image"/"first_frame_image" where fal's say
// "image_url"). Replicate validates inputs against the model's schema and
// rejects unknown fields, so unlike the fal resolvers nothing speculative is
// ever written: a canonical param the model doesn't declare is dropped with a
// notice, never sent under a guessed name.

// replicateImageSynonyms lists, per canonical param, the native input names
// Replicate image models use. The edit models name their source frame "image"
// (nano-banana, seedream) or "input_image" (flux kontext); both are listed and
// the schema picks whichever the configured model declares.
var replicateImageSynonyms = map[string][]string{
	"prompt":            {"prompt"},
	"sourceImage":       {"image", "input_image", "image_url", "image_urls"},
	"aspectRatio":       {"aspect_ratio"},
	"resolution":        {"resolution"},
	"numImages":         {"num_outputs"},
	"numInferenceSteps": {"num_inference_steps"},
}

// replicateVideoSynonyms lists, per canonical param, the native input names
// Replicate video models use. Image-to-video models name the first frame
// "image" (wan i2v, kling) or "first_frame_image" (seedance i2v); both are
// listed. Duration is usually a free number (seconds) rather than fal's enum
// strings, and coerceVideoValue's type-driven numeric coercion handles that.
var replicateVideoSynonyms = map[string][]string{
	"prompt":         {"prompt"},
	"duration":       {"duration"},
	"aspectRatio":    {"aspect_ratio", "aspectRatio"},
	"resolution":     {"resolution"},
	"fps":            {"fps", "frame_rate"},
	"negativePrompt": {"negative_prompt"},
	"sourceImage":    {"image", "first_frame_image", "start_image", "image_url", "image_urls"},
	"sourceVideo":    {"video", "video_url"},
	"generateAudio":  {"generate_audio", "generate_audio_enabled"},
}

// errReplicateVideoSourceUnsupported is the deterministic, planner-readable
// refusal for video-source turns on the Replicate backend: phase one routes
// only text-to-video and image-to-video there, so an extend / motion /
// reference-with-video request fails up front with the remedy in the message
// rather than surfacing as a confusing downstream 422.
var errReplicateVideoSourceUnsupported = errors.New("the Replicate video backend supports text-to-video and image-to-video only; it cannot generate from an attached video (extend, motion transfer, or video reference) — switch Video Provider to fal.ai in Settings → Models")

// resolveReplicateImageInput maps a canonical ImageGenerateRequest onto the
// Replicate model's native input schema, returning the prediction input,
// user-facing notices for anything dropped, and an error for a hard capability
// mismatch (multi-image into a single-image model). It is the Replicate
// sibling of resolveImageBody; a nil schema (unavailable) yields a minimal
// {prompt, image?} body plus a notice, since Replicate rejects unknown fields
// a richer fallback would invent.
func resolveReplicateImageInput(schema *ModelInputSchema, req ImageGenerateRequest) (map[string]any, []string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	// falImageURL's normalization is provider-neutral: hosted URLs and data
	// URIs pass through and bare base64 (the post-Ollama shape) is wrapped
	// into a data URI — exactly what Replicate accepts inline.
	sourceImages := make([]string, 0, len(req.Images))
	for _, img := range req.Images {
		if u := falImageURL(strings.TrimSpace(img)); u != "" {
			sourceImages = append(sourceImages, u)
		}
	}

	ov := Overrides{}
	if schema == nil {
		if len(sourceImages) > 1 {
			return nil, nil, fmt.Errorf(
				"model %q could not be queried for its parameter schema and accepts at most one image; %d were attached. Configure a multi-image edit model (one whose input declares an image array).",
				req.Model, len(sourceImages))
		}
		body := map[string]any{"prompt": prompt}
		if len(sourceImages) == 1 {
			body["image"] = sourceImages[0]
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported image input."}, nil
	}

	body := map[string]any{}
	var notices []string

	if path, prop, ok := findNative(schema, ov, "replicate-image", req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, coerceImageValue(prop, prompt))
	} else {
		body["prompt"] = prompt
	}
	// num_outputs only when the model declares it — Replicate rejects unknown
	// inputs, so no speculative num_images the way the fal fallback writes one.
	if path, prop, ok := findNative(schema, ov, "replicate-image", req.Model, "numImages"); ok {
		setBodyPath(schema, body, path, coerceImageValue(prop, 1))
	}

	// Resolution tier onto a native resolution enum (nano-banana's "1K"/"2K"),
	// mirroring forwardImageResolutionTier's uppercase + enum gate.
	if tier := normalizeImageResolutionTier(req.Resolution); tier != "" {
		if path, prop, ok := findNative(schema, ov, "replicate-image", req.Model, "resolution"); ok {
			if valueAllowedByEnum(prop, strings.ToUpper(tier)) {
				setBodyPath(schema, body, path, strings.ToUpper(tier))
			} else if req.ResolutionExplicit {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not accept resolution %q; ignoring it and letting the model choose.",
					req.Model, tier))
			}
		}
	}

	// Aspect ratio, enum-gated. Replicate image models expose a raw
	// "16:9"-style aspect_ratio enum; a ratio the enum doesn't list is dropped
	// (the model picks its own / the source frame's shape) rather than sent —
	// passing it through would 422.
	if aspect := strings.TrimSpace(req.AspectRatio); aspect != "" {
		if path, prop, ok := findNative(schema, ov, "replicate-image", req.Model, "aspectRatio"); ok {
			if canonical, allowed := enumValueFor(prop, aspect); allowed {
				setBodyPath(schema, body, path, coerceImageValue(prop, canonical))
			} else {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not accept aspect ratio %q; ignoring it and letting the model choose.",
					req.Model, aspect))
			}
		} else if len(sourceImages) == 0 {
			// Edit models derive their shape from the source frame; a
			// text-to-image model with no aspect_ratio input genuinely can't
			// carry one.
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no aspect-ratio control; ignoring the requested aspect ratio.",
				req.Model))
		}
	}

	if len(sourceImages) > 0 {
		path, prop, ok := findNative(schema, ov, "replicate-image", req.Model, "sourceImage")
		if !ok {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-image input; the attached image(s) were ignored.",
				req.Model))
		} else {
			// Same guardrails as the fal image path: multiple images into a
			// scalar-image model is a hard error (silently dropping the rest
			// would hide a capability mismatch), and a declared cap rejects
			// requests above it.
			if prop.Kind != schemaArray && len(sourceImages) > 1 {
				return nil, notices, fmt.Errorf(
					"model %q accepts a single image; %d were attached. Use a multi-image edit model (one whose input declares an image array).",
					req.Model, len(sourceImages))
			}
			if prop.Kind == schemaArray && prop.MaxItems > 0 && len(sourceImages) > prop.MaxItems {
				return nil, notices, fmt.Errorf(
					"model %q accepts at most %d image(s); %d were attached. Attach fewer images or switch to a model with a higher image cap.",
					req.Model, prop.MaxItems, len(sourceImages))
			}
			setBodyPath(schema, body, path, coerceImages(prop, sourceImages))
		}
	}

	if req.Steps > 0 {
		if path, prop, ok := findNative(schema, ov, "replicate-image", req.Model, "numInferenceSteps"); ok {
			setBodyPath(schema, body, path, coerceImageValue(prop, req.Steps))
		}
	}
	return body, notices, nil
}

// resolveReplicateVideoInput maps a canonical VideoGenerateRequest onto the
// Replicate model's native input schema — the Replicate sibling of
// resolveVideoBody. Video-source turns (extend, motion transfer, video
// reference) and keyframe transitions fail up front with
// errReplicateVideoSourceUnsupported: the Replicate backend routes only
// text-to-video and image-to-video in this phase, and a deterministic error
// with the remedy in the message beats a downstream 422. A nil schema yields a
// minimal {prompt, image?} body plus a notice.
func resolveReplicateVideoInput(schema *ModelInputSchema, req VideoGenerateRequest) (map[string]any, []string, error) {
	if len(req.SourceVideos()) > 0 {
		return nil, nil, errReplicateVideoSourceUnsupported
	}
	if req.Keyframes {
		return nil, nil, errors.New("start→end keyframe transitions are not supported on the Replicate video backend; switch Video Provider to fal.ai in Settings → Models")
	}

	prompt := strings.TrimSpace(req.Prompt)
	sourceImages := make([]string, 0, 4)
	for _, img := range req.SourceImages() {
		if u := falImageURL(strings.TrimSpace(img)); u != "" {
			sourceImages = append(sourceImages, u)
		}
	}

	ov := Overrides{}
	if schema == nil {
		if len(sourceImages) > 1 {
			return nil, nil, fmt.Errorf(
				"model %q could not be queried for its parameter schema and accepts at most one image; %d were attached.",
				req.Model, len(sourceImages))
		}
		body := map[string]any{"prompt": prompt}
		if len(sourceImages) == 1 {
			body["image"] = sourceImages[0]
		}
		return body, []string{"Couldn't load the model's parameter schema; generated with defaults and may have dropped an unsupported video input."}, nil
	}

	body := map[string]any{}
	var notices []string

	if path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "prompt"); ok {
		setBodyPath(schema, body, path, coerceVideoValue(prop, prompt))
	} else {
		body["prompt"] = prompt
	}
	if duration := strings.TrimSpace(req.Duration); duration != "" {
		// Enum guard first (some models list fixed durations), then the shared
		// type-driven coercion — Replicate durations are usually free numbers,
		// so coerceVideoValue turns "5" into 5 against a number-typed field.
		if path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "duration"); ok {
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
		// Image-to-video derives orientation from the source frame, so a
		// config/detected default is redundant and conflicting (the fal-side
		// rule, conv_26cc3f515d6d645b316763cb); only an explicit planner
		// request overrides it. Text-to-video always sends the ratio.
		if len(sourceImages) > 0 && !req.AspectRatioExplicit {
			// skip — inherit the source frame's orientation
		} else if path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "aspectRatio"); ok {
			if canonical, allowed := enumValueFor(prop, aspect); allowed {
				setBodyPath(schema, body, path, coerceVideoValue(prop, canonical))
			} else {
				notices = append(notices, fmt.Sprintf(
					"The selected model %q does not accept aspect ratio %q; ignoring it and letting the model choose.",
					req.Model, aspect))
			}
		} else if len(sourceImages) > 0 {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q derives the output aspect ratio from the source image and has no aspect_ratio input, so the explicit %q request is only honored if the source image already matches; Atelier did not reshape the image.",
				req.Model, aspect))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no aspect-ratio control; ignoring the requested aspect ratio.",
				req.Model))
		}
	}
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
		if path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "negativePrompt"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, negative))
		} else {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no negative-prompt control; ignoring the requested negative prompt.",
				req.Model))
		}
	}
	if res := strings.TrimSpace(req.Resolution); res != "" {
		if path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "resolution"); ok {
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
		if path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "fps"); ok {
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
		if path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "generateAudio"); ok {
			setBodyPath(schema, body, path, coerceVideoValue(prop, *req.GenerateAudio))
		} else if !*req.GenerateAudio {
			// Same honesty rule as the fal resolver: no toggle does not mean
			// silent — some models emit synchronized audio by default — so an
			// explicit silent request says so instead of dropping quietly.
			notices = append(notices, fmt.Sprintf(
				"The selected model %q exposes no generate_audio input; an explicit silent request cannot be honored, so the video may contain audio.",
				req.Model))
		}
	}
	if len(sourceImages) > 0 {
		path, prop, ok := findNative(schema, ov, "replicate-video", req.Model, "sourceImage")
		if !ok {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q has no source-image input; the attached image(s) were ignored.",
				req.Model))
		} else {
			if prop.Kind != schemaArray && len(sourceImages) > 1 {
				return nil, notices, fmt.Errorf(
					"model %q accepts a single image; %d were attached. Use a multi-image reference model.",
					req.Model, len(sourceImages))
			}
			if prop.Kind == schemaArray && prop.MaxItems > 0 && len(sourceImages) > prop.MaxItems {
				return nil, notices, fmt.Errorf(
					"model %q accepts at most %d image(s); %d were attached. Attach fewer images or switch to a model with a higher image cap.",
					req.Model, prop.MaxItems, len(sourceImages))
			}
			setBodyPath(schema, body, path, coerceImages(prop, sourceImages))
		}
	}
	return body, notices, nil
}

// replicateUpscaleSynonyms lists, per canonical param, the native input names
// Replicate upscaler models use — the image-upscale sibling of the video
// table's scale entry (scale / scale_factor / upscale_factor, the names fal's
// upscalers split across endpoints too).
var replicateUpscaleSynonyms = map[string][]string{
	"sourceImage": {"image", "input_image", "image_url"},
	"scale":       {"scale", "scale_factor", "upscale_factor"},
}

// resolveReplicateUpscaleInput maps a canonical ImageUpscaleRequest onto the
// Replicate upscaler model's native input schema — the replicate sibling of
// FalClient.UpscaleImage's hand-built body, with the enum/type guards the
// schema makes possible. A nil schema yields the minimal {image, scale}
// fallback every upscaler accepts.
func resolveReplicateUpscaleInput(schema *ModelInputSchema, req ImageUpscaleRequest) (map[string]any, []string, error) {
	image := falImageURL(strings.TrimSpace(req.Image))
	scale := req.Scale
	if scale <= 0 {
		scale = 2
	}
	ov := Overrides{}
	if schema == nil {
		return map[string]any{"image": image, "scale": scale}, nil, nil
	}
	body := map[string]any{}
	var notices []string
	// The source image is the tool's entire purpose: an unmapped image input
	// is a hard error, not a graceful drop (the resolveLipsyncBody rule).
	path, prop, ok := findNative(schema, ov, "replicate-upscale", req.Model, "sourceImage")
	if !ok {
		return nil, notices, fmt.Errorf("the selected model %q has no source-image input to upscale", req.Model)
	}
	setBodyPath(schema, body, path, coerceImageValue(prop, image))
	if path, prop, ok := findNative(schema, ov, "replicate-upscale", req.Model, "scale"); ok {
		// Enum guard before the shared type-driven numeric coercion — a factor
		// the model's enum doesn't list would 422, so drop it with a notice
		// and let the model use its own default.
		if value := fmt.Sprintf("%v", scale); !valueAllowedByEnum(prop, value) {
			notices = append(notices, fmt.Sprintf(
				"The selected model %q does not accept scale %v; using the model's default factor.",
				req.Model, scale))
		} else {
			setBodyPath(schema, body, path, coerceVideoValue(prop, scale))
		}
	}
	return body, notices, nil
}
