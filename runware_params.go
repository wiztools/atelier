package main

// runware_params.go maps Atelier's canonical media requests onto Runware's
// per-model task bodies — the sibling of fal_params.go and replicate_params.go.
// Runware's vocabulary is standardized across models (positivePrompt,
// inputs.referenceImages / frameImages / video / seedImage / maskImage /
// audio, speech.text, duration), so unlike the synonyms-driven backends these
// resolvers write KNOWN names and let the schema decide only whether a field
// may be written at all: the endpoint rejects undeclared fields
// (additionalProperties:false), so with a schema every optional field is sent
// only where the schema declares it; without one, only the universal fields
// ride, with a notice saying the model's schema couldn't be loaded.
//
// Two contracts differ from the other backends and shape the resolvers:
//
//   - Video tasks carry no width/height alongside an extend source (Veo
//     declares the combination a 400), and an extend's output length is the
//     model's own — a requested duration is dropped there rather than sent.
//   - mirelo's duration control is mutually exclusive with its video input,
//     so the video-soundtrack resolver never sends one; the model matches the
//     attached clip by design, which is what the gateway filled the duration
//     for in the first place.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// errRunwareVideoSourceUnsupported is the deterministic, planner-readable
// refusal for the video turns the runware backend cannot serve — motion
// control, video-reference turns, and keyframes — the
// errReplicateVideoSourceUnsupported shape with runware's own facts (Veo 3.1
// extends an attached clip; Kling's frameImages animates an attached image).
var errRunwareVideoSourceUnsupported = errors.New("the Runware video backend supports text-to-video, image-to-video, and extending an attached clip; it cannot transfer a video's motion onto an image, use attached videos as generation references, or drive start→end keyframe transitions — switch Video Provider to fal.ai in Settings → Models")

// runwareImageRef / runwareVideoRef / runwareAudioRef normalize one source
// reference for an inputs field: an uploaded mediaUUID rides as-is (the
// endpoint accepts it directly, and wrapping it as base64 would corrupt it),
// hosted URLs and data URIs pass through, and bare base64 — the shape
// attachments ride between layers — is wrapped as a data URI with a sniffed
// media type. They are falImageURL/falVideoURL/falAudioURL with a UUID guard.
func runwareImageRef(image string) string {
	if isRunwareUUID(strings.TrimSpace(image)) {
		return strings.TrimSpace(image)
	}
	return falImageURL(image)
}

func runwareVideoRef(video string) string {
	if isRunwareUUID(strings.TrimSpace(video)) {
		return strings.TrimSpace(video)
	}
	return falVideoURL(video)
}

func runwareAudioRef(audio string) string {
	if isRunwareUUID(strings.TrimSpace(audio)) {
		return strings.TrimSpace(audio)
	}
	return falAudioURL(audio)
}

// runwareTaskBase starts every task: the client-minted UUID, one result, and
// the reported cost. The remaining universal fields (output type/format)
// differ per media kind and are written only where the schema declares them.
func runwareTaskBase(taskType, model string) map[string]any {
	return map[string]any{
		"taskType":      taskType,
		"taskUUID":      runwareTaskUUID(),
		"model":         model,
		"numberResults": 1,
		"includeCost":   true,
	}
}

// runwareDeclares reports whether the schema names a property; without a
// schema everything is treated as declared (the resolver then sends only the
// universal fields and the caller's notice already says defaults applied).
func runwareDeclares(schema *ModelInputSchema, name string) bool {
	if schema == nil {
		return true
	}
	_, ok := schema.property(name)
	return ok
}

// runwareInputsNested returns the schema's declared inputs object, if any.
func runwareInputsNested(schema *ModelInputSchema) map[string]SchemaProperty {
	if schema == nil {
		return nil
	}
	if prop, ok := schema.property("inputs"); ok {
		return prop.Nested
	}
	return nil
}

// runwareSetInput writes one media reference into the task's inputs object
// only when the schema declares the field's parent (inputs) and the field
// itself is reachable without a schema; without a schema the field is sent,
// since the endpoint documents it as a universal input name.
func runwareSetInput(schema *ModelInputSchema, task map[string]any, key string, value any) {
	if schema != nil && !hasKey(runwareInputsNested(schema), key) {
		return
	}
	inputs, _ := task["inputs"].(map[string]any)
	if inputs == nil {
		inputs = map[string]any{}
		task["inputs"] = inputs
	}
	inputs[key] = value
}

// resolveRunwareVideoTask maps the canonical video request onto one
// videoInference task. The executor has already refused the turns this
// backend cannot serve (motion control, reference videos, keyframes), so the
// resolver sees exactly three shapes: text-to-video, image-to-video (the
// source frame lands on frameImages — one image auto-distributes to the
// first frame), and an extend (the source clip on inputs.video).
func resolveRunwareVideoTask(schema *ModelInputSchema, req VideoGenerateRequest) (map[string]any, []string, []string, error) {
	task := runwareTaskBase("videoInference", req.Model)
	var notices []string
	var diagnostics []string
	prompt := strings.TrimSpace(req.Prompt)
	sourceVideos := req.SourceVideos()
	sourceImages := req.SourceImages()
	extend := req.ExtendSource && len(sourceVideos) > 0

	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}

	// Prompt. Veo-style extend endpoints declare a required positivePrompt
	// even when the user only asked to continue, so an extend turn without
	// words gets a neutral continuation prompt rather than a 422.
	if prompt == "" && !extend {
		return nil, nil, nil, errors.New("runware video generation needs a prompt")
	}
	if prompt == "" {
		prompt = "Continue the clip seamlessly."
	}
	task["positivePrompt"] = prompt

	// Source media.
	if extend {
		if !runwareDeclaresInput(schema, "video") {
			return nil, nil, nil, fmt.Errorf("Runware model %q takes no source-video input and cannot extend a clip; pick an extend-capable model (Veo 3.1 extends; its input.video names the continuation source)", req.Model)
		}
		runwareSetInput(schema, task, "video", runwareVideoRef(sourceVideos[0]))
		if len(sourceVideos) > 1 {
			notices = append(notices, fmt.Sprintf("Runware model %q extends from one clip; used the first of %d.", req.Model, len(sourceVideos)))
		}
		// The extend's length and shape are the source clip's: Veo's schema
		// declares width/height/seed/resolution a 400 beside inputs.video, and
		// the output duration is the model's own. A requested duration or
		// aspect is dropped here rather than sent.
		if strings.TrimSpace(req.Duration) != "" {
			notices = append(notices, fmt.Sprintf("Runware model %q fixes an extension's length itself; the requested %ss duration was ignored.", req.Model, strings.TrimSpace(req.Duration)))
		}
	} else if len(sourceImages) > 0 {
		refs := make([]string, 0, len(sourceImages))
		for _, img := range sourceImages {
			refs = append(refs, runwareImageRef(img))
		}
		nested := runwareInputsNested(schema)
		switch {
		case schema != nil && hasKey(nested, "frameImages") && (len(refs) == 1 || req.Keyframes):
			// One image auto-distributes to the first frame; the keyframe
			// first/last split is the executor's refusal on this backend.
			if schema != nil {
				if prop := nested["frameImages"]; prop.MaxItems > 0 && len(refs) > prop.MaxItems {
					notices = append(notices, fmt.Sprintf("Runware model %q takes at most %d frame images; used the first %d.", req.Model, prop.MaxItems, prop.MaxItems))
					refs = refs[:prop.MaxItems]
				}
			}
			runwareSetInput(schema, task, "frameImages", refs)
		case schema != nil && hasKey(nested, "referenceImages"):
			if schema != nil {
				if prop := nested["referenceImages"]; prop.MaxItems > 0 && len(refs) > prop.MaxItems {
					notices = append(notices, fmt.Sprintf("Runware model %q takes at most %d reference images; used the first %d.", req.Model, prop.MaxItems, prop.MaxItems))
					refs = refs[:prop.MaxItems]
				}
			}
			runwareSetInput(schema, task, "referenceImages", refs)
		case schema != nil && hasKey(nested, "image"):
			if len(refs) > 1 {
				notices = append(notices, fmt.Sprintf("Runware model %q takes one source image; used the first of %d.", req.Model, len(refs)))
			}
			runwareSetInput(schema, task, "image", refs[0])
		default:
			return nil, nil, nil, fmt.Errorf("Runware model %q declares no image input this app can fill", req.Model)
		}
		diagnostics = append(diagnostics, fmt.Sprintf("runware: %d source image(s) mapped onto the model's declared image input", len(refs)))
	}

	// Duration — never beside an extend source (see above).
	if !extend {
		if duration := strings.TrimSpace(req.Duration); duration != "" && duration != "auto" {
			if prop, ok := schema.property("duration"); ok {
				if !videoDurationSendable(prop, duration) {
					notices = append(notices, videoDurationDroppedNotice(req.Model, duration, prop))
				} else {
					seconds, err := strconv.ParseFloat(duration, 64)
					if err != nil {
						notices = append(notices, fmt.Sprintf("The requested duration %q could not be read as seconds and was ignored.", duration))
					} else {
						task["duration"] = runwareNumberFor(prop, seconds)
					}
				}
			} else {
				notices = append(notices, fmt.Sprintf("The selected model %q has no duration control; ignoring the requested duration.", req.Model))
			}
		}
	}

	// Output shape. An image-to-video or extend inherits the source media's
	// orientation (the fal-side rule), so explicit pixels ride only for
	// text-to-video. A native resolution tier and explicit pixels are
	// mutually exclusive on models that declare both; pixels win because they
	// carry the aspect ratio, and an explicitly requested tier that lost the
	// race says so.
	if !extend && len(sourceImages) == 0 {
		tierSent := false
		if tier := strings.TrimSpace(req.Resolution); tier != "" && schema != nil {
			if prop, ok := schema.property("resolution"); ok {
				if _, hasPixels := schema.property("width"); !hasPixels {
					if canonical, allowed := enumValueFor(prop, tier); allowed {
						task["resolution"] = canonical
						tierSent = true
					} else {
						notices = append(notices, fmt.Sprintf("The selected model %q does not offer resolution %q; using its default.", req.Model, tier))
					}
				}
			}
		}
		if !tierSent {
			if w, h, ok := runwareVideoDimensions(schema, req.AspectRatio); ok {
				task["width"], task["height"] = w, h
			} else if aspect := strings.TrimSpace(req.AspectRatio); aspect != "" && !isAutoAspectRatio(aspect) && req.AspectRatioExplicit && schema != nil {
				if prop, ok := schema.property("resolution"); ok {
					if canonical, allowed := enumValueFor(prop, aspect); allowed {
						task["resolution"] = canonical
					} else {
						notices = append(notices, fmt.Sprintf("The selected model %q has no aspect-ratio control; ignoring the requested aspect ratio.", req.Model))
					}
				} else {
					notices = append(notices, fmt.Sprintf("The selected model %q has no aspect-ratio control; ignoring the requested aspect ratio.", req.Model))
				}
			}
		}
	}

	// Negative prompt, audio toggle, fps — written only where declared.
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" && runwareDeclares(schema, "negativePrompt") {
		task["negativePrompt"] = negative
	}
	if req.GenerateAudio != nil && !*req.GenerateAudio {
		if _, ok := schema.property("generateAudio"); ok {
			task["generateAudio"] = false
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no audio toggle; the clip may include the model's native audio.", req.Model))
		}
	}
	if fps := strings.TrimSpace(req.FPS); fps != "" && !runwareDeclares(schema, "fps") {
		notices = append(notices, fmt.Sprintf("Runware model %q has no frame-rate control; ignoring the requested %s fps.", req.Model, fps))
	}

	// Output delivery: video models answer with URLs (their schemas pin
	// outputType to URL), so only the container is requested where declared.
	if runwareDeclares(schema, "outputFormat") {
		task["outputFormat"] = "MP4"
	}
	return task, notices, diagnostics, nil
}

// runwareDeclaresInput reports whether the schema's inputs object names a
// field; without a schema every documented input name is treated as present.
func runwareDeclaresInput(schema *ModelInputSchema, key string) bool {
	if schema == nil {
		return true
	}
	return hasKey(runwareInputsNested(schema), key)
}

// runwareVideoDimensions derives an output size for text-to-video on the
// model's own terms: a declared preset list wins — Kling and Veo publish
// fixed width/height pairs and an off-list size is a 400 — else the aspect
// is rendered onto the declared bounds at a 720p-class long edge, snapped to
// the 64-px grid most Runware models require. Without a requestable aspect
// nothing is sent and the model uses its own default.
func runwareVideoDimensions(schema *ModelInputSchema, aspect string) (int, int, bool) {
	aw, ah, hasAspect := 1, 1, false
	if token := strings.TrimSpace(aspect); token != "" && !isAutoAspectRatio(token) {
		if w, h, ok := parseAspectRatio(token); ok {
			aw, ah, hasAspect = w, h, true
		}
	}
	if !hasAspect {
		return 0, 0, false
	}
	if schema != nil {
		if prop, ok := schema.property(runwareSizePresetsProperty); ok && len(prop.Enum) > 0 {
			if w, h, ok := runwareNearestPreset(prop.Enum, aw, ah); ok {
				return w, h, true
			}
		}
	}
	if !runwareDeclares(schema, "width") || !runwareDeclares(schema, "height") {
		return 0, 0, false
	}
	const longEdge = 1280
	w, h := longEdge, longEdge*ah/aw
	if aw < ah {
		w, h = longEdge*aw/ah, longEdge
	}
	return runwareSnapDimension(schema, "width", w), runwareSnapDimension(schema, "height", h), true
}

// resolveRunwareVideoUpscaleTask maps the canonical video-upscale request
// onto Runware's upscale task. The factor is clamped into the declared
// bounds (FLUX 3 Upscaler takes 1.5–3) — an over-ask scales by what the
// model can do, the transform tools' clamp stance, under a notice when the
// request lost more than a rounding step.
func resolveRunwareVideoUpscaleTask(schema *ModelInputSchema, req VideoUpscaleRequest) (map[string]any, []string, error) {
	task := runwareTaskBase("upscale", req.Model)
	var notices []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}
	runwareSetInput(schema, task, "video", runwareVideoRef(req.Video))
	if runwareDeclares(schema, "positivePrompt") {
		// Optional on the upscaler; sent only where the schema asks for it —
		// an upscale ask carries no new elements to describe.
		task["positivePrompt"] = "Preserve the source clip and sharpen its detail."
	}
	if req.Scale > 0 && runwareDeclares(schema, "upscaleFactor") {
		factor := req.Scale
		if schema != nil {
			if prop, ok := schema.property("upscaleFactor"); ok {
				if prop.Minimum != nil && factor < *prop.Minimum {
					notices = append(notices, fmt.Sprintf("Runware model %q scales by at least %gx; using %gx instead of the requested %gx.", req.Model, *prop.Minimum, *prop.Minimum, req.Scale))
					factor = *prop.Minimum
				}
				if prop.Maximum != nil && factor > *prop.Maximum {
					notices = append(notices, fmt.Sprintf("Runware model %q scales by at most %gx; using %gx instead of the requested %gx.", req.Model, *prop.Maximum, *prop.Maximum, req.Scale))
					factor = *prop.Maximum
				}
			}
		}
		task["upscaleFactor"] = factor
	}
	if runwareDeclares(schema, "outputFormat") {
		task["outputFormat"] = "MP4"
	}
	return task, notices, nil
}

// resolveRunwareImageUpscaleTask maps the canonical image-upscale request
// onto one upscale task: the source on inputs.image (the field every catalog
// image upscaler declares — Real-ESRGAN, Clarity, SwinIR, Bria, Topaz Wonder,
// CCSR, SD Latent, P-Image) and the factor bound to the model's declared
// shape — a fixed enum (Real-ESRGAN's [2,4]) snaps to the nearest legal
// value, a numeric window (Clarity 1–4, Wonder 1–6, P-Image 2–16) gets the
// notice-carrying clamp, the transform tools' over-ask stance either way.
// Inline PNG delivery is requested like the image and inpaint resolvers'.
func resolveRunwareImageUpscaleTask(schema *ModelInputSchema, req ImageUpscaleRequest) (map[string]any, []string, error) {
	if strings.TrimSpace(req.Image) == "" {
		return nil, nil, errors.New("runware image upscaling needs the attached image")
	}
	task := runwareTaskBase("upscale", req.Model)
	var notices []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	} else if !hasKey(runwareInputsNested(schema), "image") {
		return nil, nil, fmt.Errorf("Runware model %q takes no source-image input and cannot upscale; pick an image upscaler (the catalog tags them image-to-image + upscale)", req.Model)
	}
	runwareSetInput(schema, task, "image", runwareImageRef(req.Image))
	if req.Scale > 0 && runwareDeclares(schema, "upscaleFactor") {
		factor := req.Scale
		if schema != nil {
			if prop, ok := schema.property("upscaleFactor"); ok {
				if len(prop.Enum) > 0 {
					// A fixed factor menu (Real-ESRGAN's [2,4]) snaps to the
					// nearest member, ties toward the smaller — the
					// conservative upscale.
					if snap, ok := runwareNearestEnumNumber(prop.Enum, factor); ok && snap != factor {
						notices = append(notices, fmt.Sprintf("Runware model %q scales in %gx steps; using %gx instead of the requested %gx.", req.Model, snap, snap, req.Scale))
						factor = snap
					}
				} else {
					if prop.Minimum != nil && factor < *prop.Minimum {
						notices = append(notices, fmt.Sprintf("Runware model %q scales by at least %gx; using %gx instead of the requested %gx.", req.Model, *prop.Minimum, *prop.Minimum, req.Scale))
						factor = *prop.Minimum
					}
					if prop.Maximum != nil && factor > *prop.Maximum {
						notices = append(notices, fmt.Sprintf("Runware model %q scales by at most %gx; using %gx instead of the requested %gx.", req.Model, *prop.Maximum, *prop.Maximum, req.Scale))
						factor = *prop.Maximum
					}
				}
				task["upscaleFactor"] = runwareNumberFor(prop, factor)
			} else {
				task["upscaleFactor"] = factor
			}
		} else {
			task["upscaleFactor"] = factor
		}
	}
	// Output delivery: inline data URI (the URL is only fetched when a model
	// ignores the request) and lossless PNG, the image and inpaint resolvers'
	// stance — with no schema the universal fields ride blind.
	if schema == nil {
		task["outputType"] = "dataURI"
		task["outputFormat"] = "PNG"
	} else {
		if prop, ok := schema.property("outputType"); ok {
			if _, allowed := enumValueFor(prop, "dataURI"); allowed {
				task["outputType"] = "dataURI"
			}
		}
		if _, ok := schema.property("outputFormat"); ok {
			task["outputFormat"] = "PNG"
		}
	}
	return task, notices, nil
}

// runwareNearestEnumNumber picks the numerically nearest member of a numeric
// enum, ties toward the smaller value. False when no member parses as a
// number, in which case the caller sends the request as-is.
func runwareNearestEnumNumber(enum []string, value float64) (float64, bool) {
	best, bestDist, found := 0.0, math.Inf(1), false
	for _, member := range enum {
		f, err := strconv.ParseFloat(strings.TrimSpace(member), 64)
		if err != nil {
			continue
		}
		if dist := math.Abs(f - value); dist < bestDist-1e-9 || (dist <= bestDist+1e-9 && f < best) {
			best, bestDist, found = f, dist, true
		}
	}
	return best, found
}

// resolveRunwareVideoRestyleTask maps the canonical restyle request onto one
// videoInference task (FLUX Video Edit's shape: the source clip on
// inputs.video, the new-look instruction on positivePrompt). Style reference
// images ride inputs.referenceImages only where the model declares them; a
// prompt-less restyle is refused — the prompt IS the restyle.
func resolveRunwareVideoRestyleTask(schema *ModelInputSchema, req VideoRestyleRequest) (map[string]any, []string, []string, error) {
	task := runwareTaskBase("videoInference", req.Model)
	var notices []string
	var diagnostics []string
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, nil, nil, fmt.Errorf("Runware model %q needs a prompt describing the new look; a restyle without words would re-render the clip unchanged", req.Model)
	}
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}
	if !runwareDeclaresInput(schema, "video") {
		return nil, nil, nil, fmt.Errorf("Runware model %q takes no source-video input and cannot restyle a clip", req.Model)
	}
	runwareSetInput(schema, task, "video", runwareVideoRef(req.Video))
	task["positivePrompt"] = prompt
	for _, img := range req.Images {
		if !runwareDeclaresInput(schema, "referenceImages") {
			notices = append(notices, fmt.Sprintf("Runware model %q takes no style reference images; ignoring the attached look reference.", req.Model))
			break
		}
		refs, _ := task["inputs"].(map[string]any)["referenceImages"].([]string)
		refs = append(refs, runwareImageRef(img))
		runwareSetInput(schema, task, "referenceImages", refs)
	}
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
		if runwareDeclares(schema, "negativePrompt") {
			task["negativePrompt"] = negative
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no negative-prompt control; ignoring it.", req.Model))
		}
	}
	if tier := strings.TrimSpace(req.Resolution); tier != "" {
		if prop, ok := schema.property("resolution"); ok {
			if canonical, allowed := enumValueFor(prop, tier); allowed {
				task["resolution"] = canonical
			} else {
				notices = append(notices, fmt.Sprintf("The selected model %q does not offer resolution %q; using its default.", req.Model, tier))
			}
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no resolution control; ignoring the requested tier.", req.Model))
		}
	}
	if runwareDeclares(schema, "outputFormat") {
		task["outputFormat"] = "MP4"
	}
	return task, notices, diagnostics, nil
}

// resolveRunwareLipsyncTask maps the canonical lipsync request onto one
// videoInference task: the driving audio on inputs.audio and the face on
// the input the model declares — KlingAI Lip-Sync takes a video face,
// VEED Fabric an image face. The executor picks the model by face kind, so
// a mismatch is a belt error naming the fix.
func resolveRunwareLipsyncTask(schema *ModelInputSchema, req LipsyncGenerateRequest) (map[string]any, []string, error) {
	task := runwareTaskBase("videoInference", req.Model)
	var notices []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}
	faceKey, faceRef := "", ""
	if strings.TrimSpace(req.Video) != "" {
		faceKey, faceRef = "video", req.Video
	} else if strings.TrimSpace(req.Image) != "" {
		faceKey, faceRef = "image", req.Image
	} else {
		return nil, nil, errors.New("lip sync needs a face: attach a video or an image of the person to animate")
	}
	if !runwareDeclaresInput(schema, "audio") {
		return nil, nil, fmt.Errorf("Runware model %q takes no audio input and cannot lip-sync", req.Model)
	}
	if !runwareDeclaresInput(schema, faceKey) {
		kind := "video"
		if faceKey == "image" {
			kind = "image"
		}
		return nil, nil, fmt.Errorf("Runware model %q takes no %s face; pick a lipsync model that drives that kind of face, or switch the Lip Sync Provider to fal.ai or Replicate", req.Model, kind)
	}
	runwareSetInput(schema, task, "audio", runwareAudioRef(req.Audio))
	if faceKey == "image" {
		runwareSetInput(schema, task, "image", runwareImageRef(faceRef))
	} else {
		runwareSetInput(schema, task, "video", runwareVideoRef(faceRef))
	}
	if runwareDeclares(schema, "outputFormat") {
		task["outputFormat"] = "MP4"
	}
	return task, notices, nil
}

// resolveRunwareSpeechTask maps the canonical speech request onto one
// audioInference task with the model's nested speech object (speech.text is
// the spoken words, speech.voice the catalog voice). Runware's speech models
// publish voice catalogs as enums and no emotion/speed dials (fal's MiniMax
// endpoints nest those under voice_setting — the Runware packaging does
// not), so a requested control drops with a notice unless THIS model's
// schema declares it.
func resolveRunwareSpeechTask(schema *ModelInputSchema, req AudioGenerateRequest) (map[string]any, []string, error) {
	text := strings.TrimSpace(req.Prompt)
	if text == "" {
		return nil, nil, errors.New("runware speech generation needs words to speak")
	}
	task := runwareTaskBase("audioInference", req.Model)
	var notices []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}
	// The spoken text is the hard requirement: without a declared speech
	// object there is no contract this request can fill.
	speechProp, hasSpeech := schema.property("speech")
	if schema != nil && (!hasSpeech || speechProp.Kind != schemaObject) {
		return nil, nil, fmt.Errorf("Runware model %q does not declare the speech object (speech.text) the app speaks; pick a Runware text-to-speech model", req.Model)
	}
	speech := map[string]any{"text": text}
	if voice := strings.TrimSpace(req.Voice); voice != "" {
		voiceProp, hasVoice := speechProp.Nested["voice"]
		if schema != nil && !hasVoice {
			notices = append(notices, fmt.Sprintf("Runware model %q has no voice selection; using its default voice.", req.Model))
		} else if canonical, ok := enumValueFor(voiceProp, voice); ok {
			speech["voice"] = canonical
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q doesn't offer a voice named %q; using its default voice.", req.Model, voice))
		}
	}
	if emotion := strings.TrimSpace(req.Emotion); emotion != "" {
		if emotionProp, ok := speechProp.Nested["emotion"]; ok && schema != nil {
			if canonical, ok := enumValueFor(emotionProp, emotion); ok {
				speech["emotion"] = canonical
			} else {
				notices = append(notices, fmt.Sprintf("Runware model %q doesn't offer the emotion %q; using its default delivery.", req.Model, emotion))
			}
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no emotion control; using its default delivery.", req.Model))
		}
	}
	if req.Speed > 0 {
		if speedProp, ok := speechProp.Nested["speed"]; ok && schema != nil {
			speed := req.Speed
			if speedProp.Minimum != nil && speed < *speedProp.Minimum {
				notices = append(notices, fmt.Sprintf("Runware model %q speaks at most %gx speed at the slow end; using %g.", req.Model, *speedProp.Minimum, *speedProp.Minimum))
				speed = *speedProp.Minimum
			}
			if speedProp.Maximum != nil && speed > *speedProp.Maximum {
				notices = append(notices, fmt.Sprintf("Runware model %q speaks at most %gx speed; using %g instead of the requested %gx.", req.Model, *speedProp.Maximum, *speedProp.Maximum, req.Speed))
				speed = *speedProp.Maximum
			}
			speech["speed"] = speed
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no speed control; speaking at its default pace.", req.Model))
		}
	}
	task["speech"] = speech
	if runwareDeclares(schema, "outputType") {
		// Inline delivery: the bytes ride the response, so nothing depends on
		// the ttl window (GenerateAudio still fetches a URL when a model
		// ignores the request).
		if prop, ok := schema.property("outputType"); ok {
			if _, allowed := enumValueFor(prop, "dataURI"); allowed {
				task["outputType"] = "dataURI"
			}
		}
	}
	return task, notices, nil
}

// resolveRunwareSoundTask maps the canonical sound request (music or sound
// effects from text) onto one audioInference task. Music models that carry a
// lyrics pipeline (MiniMax Music's settings.instrumental) are told the piece
// is instrumental — the canonical request has no lyrics to send, and
// declaring instrumental is what makes the prompt-only shape schema-legal.
func resolveRunwareSoundTask(schema *ModelInputSchema, req AudioGenerateRequest) (map[string]any, []string, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, nil, errors.New("runware sound generation needs a description of the sound or music")
	}
	task := runwareTaskBase("audioInference", req.Model)
	var notices []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}
	task["positivePrompt"] = prompt
	if settingsProp, ok := schema.property("settings"); ok && schema != nil && settingsProp.Kind == schemaObject && hasKey(settingsProp.Nested, "instrumental") {
		task["settings"] = map[string]any{"instrumental": true}
	}
	if duration := strings.TrimSpace(req.Duration); duration != "" && duration != "auto" {
		if prop, ok := schema.property("duration"); ok {
			seconds, err := strconv.ParseFloat(duration, 64)
			if err != nil {
				notices = append(notices, fmt.Sprintf("The requested duration %q could not be read as seconds and was ignored.", duration))
			} else {
				if schema != nil {
					clamped, clampedNotice := runwareClampNumber(prop, seconds, fmt.Sprintf("Runware model %q generates sound between", req.Model))
					if clampedNotice != "" {
						notices = append(notices, clampedNotice)
					}
					seconds = clamped
				}
				task["duration"] = runwareNumberFor(prop, seconds)
			}
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no duration control; the piece runs its own length.", req.Model))
		}
	}
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
		if runwareDeclares(schema, "negativePrompt") {
			task["negativePrompt"] = negative
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no negative-prompt control; ignoring it.", req.Model))
		}
	}
	if style := strings.TrimSpace(req.Style); style != "" {
		if prop, ok := schema.property("style"); ok && schema != nil {
			task["style"] = coerceRunwareScalar(prop, style)
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no style control; ignoring the requested style.", req.Model))
		}
	}
	if req.Loop && !runwareDeclares(schema, "loop") {
		notices = append(notices, fmt.Sprintf("Runware model %q has no loop control; the clip may not loop seamlessly.", req.Model))
	}
	if runwareDeclares(schema, "outputType") {
		if prop, ok := schema.property("outputType"); ok {
			if _, allowed := enumValueFor(prop, "dataURI"); allowed {
				task["outputType"] = "dataURI"
			}
		}
	}
	return task, notices, nil
}

// resolveRunwareSFXTask maps the canonical video-soundtrack request onto one
// audioInference task: the attached clip on inputs.video and a steering
// prompt. mirelo's duration dial is mutually exclusive with its video input
// (the model matches the clip by design — the very thing the gateway's
// duration fill exists for on the fal path), so no duration is ever sent
// here. The prompt is required by the endpoint; a prompt-less ask gets a
// generic steering line, the fal resolver's rule.
func resolveRunwareSFXTask(schema *ModelInputSchema, req SFXGenerateRequest) (map[string]any, []string, error) {
	if strings.TrimSpace(req.Video) == "" {
		return nil, nil, errors.New("add_sound_effects needs the attached clip to generate a soundtrack for")
	}
	task := runwareTaskBase("audioInference", req.Model)
	var notices []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}
	if !runwareDeclaresInput(schema, "video") {
		return nil, nil, fmt.Errorf("Runware model %q takes no video input; it generates sound from text only — use generate_sound instead", req.Model)
	}
	runwareSetInput(schema, task, "video", runwareVideoRef(req.Video))
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "Sound effects and ambience matching the scene."
	}
	task["positivePrompt"] = prompt
	if negative := strings.TrimSpace(req.NegativePrompt); negative != "" {
		if runwareDeclares(schema, "negativePrompt") {
			task["negativePrompt"] = negative
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no negative-prompt control; ignoring it.", req.Model))
		}
	}
	if req.PreserveSpeech != nil {
		if runwareDeclares(schema, "preserveSpeech") {
			task["preserveSpeech"] = *req.PreserveSpeech
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no preserve-speech control; speech in the clip may be regenerated.", req.Model))
		}
	}
	// The soundtrack container follows the endpoint: a video input forces
	// MP4/MOV/WEBM (sound mixed into the picture), audio-only output when a
	// model serves both shapes.
	if runwareDeclares(schema, "outputFormat") {
		task["outputFormat"] = "MP4"
	}
	if runwareDeclares(schema, "outputType") {
		if prop, ok := schema.property("outputType"); ok {
			if _, allowed := enumValueFor(prop, "dataURI"); allowed {
				task["outputType"] = "dataURI"
			}
		}
	}
	return task, notices, nil
}

// resolveRunwareInpaintTask maps the canonical inpaint request onto one
// imageInference task: the source on inputs.seedImage and the mask on
// inputs.maskImage — Runware's polarity matches the canonical one (white is
// the area to edit), so the mask rides unchanged. Dedicated inpainting
// models (FLUX.1 Fill) inherit the seed image's dimensions and take no
// width/height; where a model declares them and the caller pinned some,
// they ride snapped. A schema whose inputs declare no mask refuses before
// any money moves — the fal/Replicate contract.
func resolveRunwareInpaintTask(schema *ModelInputSchema, req ImageInpaintRequest) (map[string]any, []string, error) {
	if strings.TrimSpace(req.SourceImage) == "" || strings.TrimSpace(req.MaskImage) == "" {
		return nil, nil, errors.New("runware inpainting needs the source image and the mask")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, nil, errors.New("runware inpainting needs a prompt describing the masked area")
	}
	task := runwareTaskBase("imageInference", req.Model)
	var notices []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	} else {
		nested := runwareInputsNested(schema)
		if !hasKey(nested, "maskImage") {
			return nil, nil, fmt.Errorf("Runware model %q takes no mask input; pick a mask-capable model (the verified Runware inpaint models are FLUX.1 Fill)", req.Model)
		}
	}
	runwareSetInput(schema, task, "seedImage", runwareImageRef(req.SourceImage))
	runwareSetInput(schema, task, "maskImage", runwareImageRef(req.MaskImage))
	task["positivePrompt"] = prompt
	if schema == nil || (runwareDeclares(schema, "width") && runwareDeclares(schema, "height")) {
		if req.Width > 0 && req.Height > 0 {
			task["width"] = runwareSnapDimension(schema, "width", req.Width)
			task["height"] = runwareSnapDimension(schema, "height", req.Height)
		}
	}
	if runwareDeclares(schema, "outputType") {
		task["outputType"] = "dataURI"
	}
	if runwareDeclares(schema, "outputFormat") {
		task["outputFormat"] = "PNG"
	}
	return task, notices, nil
}

// runwareNumberFor sends a numeric canonical value in the property's declared
// JSON type — an integer duration must go out as 10, not "10" or 10.5 (the
// conv_4feb919a lesson, shared with the fal resolver).
func runwareNumberFor(prop SchemaProperty, value float64) any {
	if prop.Type == "integer" {
		return int(value)
	}
	return value
}

// coerceRunwareScalar coerces a canonical string into a property's declared
// JSON type; runware's scalar canon is mostly string-typed, so this is a
// pass-through with the number leg for safety.
func coerceRunwareScalar(prop SchemaProperty, value string) any {
	if prop.Type == "integer" || prop.Type == "number" {
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return runwareNumberFor(prop, f)
		}
	}
	return value
}

// runwareClampNumber clamps a numeric request into the property's declared
// bounds and returns the notice-carrying-clamp contract's second half: the
// caller sends the clamped value and surfaces the notice regardless.
func runwareClampNumber(prop SchemaProperty, value float64, model string) (float64, string) {
	if prop.Minimum != nil && value < *prop.Minimum {
		return *prop.Minimum, fmt.Sprintf("%s %g and %g seconds of audio; generating %g instead of the requested %g.", model, *prop.Minimum, *prop.Maximum, *prop.Minimum, value)
	}
	if prop.Maximum != nil && value > *prop.Maximum {
		return *prop.Maximum, fmt.Sprintf("%s %g and %g seconds of audio; generating %g instead of the requested %g.", model, *prop.Minimum, *prop.Maximum, *prop.Maximum, value)
	}
	return value, ""
}

// resolveRunwareTranscribeTask maps the canonical transcription request onto
// one textInference task. Runware's speech-to-text rides its Gemini text
// models — an audio array on a chat-shaped task — so the contract differs
// from the whisper-shaped backends in three ways the resolver makes honest:
// the transcribe/translate task folds into the instruction (there is no task
// enum), the optional two-letter language hint folds in beside it (no
// language dial), and a timestamps request degrades to a plain transcript
// under a notice (the schemas carry no cue parameters — there is no VTT-able
// output to fake). The audio must arrive as a mediaUUID or hosted URL: the
// audios input validates uuid/uri only, no data URIs, so the gateway uploads
// unconditionally before this resolver runs.
func resolveRunwareTranscribeTask(schema *ModelInputSchema, req TranscribeAudioRequest) (map[string]any, []string, error) {
	if strings.TrimSpace(req.Audio) == "" {
		return nil, nil, errors.New("runware transcription needs the attached audio clip")
	}
	var notices []string
	instruction := ""
	if req.Task == "translate" {
		instruction = "Listen to the speech in this audio recording and translate it to English. Output only the English translation as plain text, with no commentary."
	} else {
		instruction = "Transcribe this audio recording exactly as spoken, including every word. Output only the transcript as plain text, with no commentary."
	}
	if lang := strings.TrimSpace(req.Language); lang != "" && lang != "auto" {
		instruction += fmt.Sprintf(" The speech is expected to be in language %q; transcribe it faithfully in its original words.", lang)
	}
	task := map[string]any{
		"taskType":      "textInference",
		"taskUUID":      runwareTaskUUID(),
		"model":         req.Model,
		"messages":      []map[string]any{{"role": "user", "content": instruction}},
		"numberResults": 1,
		"includeCost":   true,
	}
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; sent the universal text-task shape.")
	} else {
		if !runwareDeclares(schema, "messages") {
			return nil, nil, fmt.Errorf("Runware model %q does not declare the messages contract the app speaks; pick a Runware text-inference model (the verified Gemini speech-to-text models)", req.Model)
		}
		if prop, ok := schema.property("inputs"); !ok || prop.Kind != schemaObject || !hasKey(prop.Nested, "audios") {
			return nil, nil, fmt.Errorf("Runware model %q takes no audio input; pick an audio-to-text-capable Runware model", req.Model)
		}
	}
	inputs := map[string]any{"audios": []string{runwareAudioRef(req.Audio)}}
	task["inputs"] = inputs
	if req.Timestamps != "" {
		notices = append(notices, fmt.Sprintf("Runware's speech-to-text runs on text models that return plain text only, so the requested %s timestamps are not available — the transcript has no cue track (fal.ai, Replicate, or local whisper produce timestamped transcripts).", req.Timestamps))
	}
	return task, notices, nil
}
