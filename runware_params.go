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
// control and video-reference turns, the errReplicateVideoSourceUnsupported
// shape with runware's own facts (Veo 3.1 extends an attached clip; Kling's
// frameImages animates an attached image or drives a start→end keyframe
// transition).
var errRunwareVideoSourceUnsupported = errors.New("the Runware video backend supports text-to-video, image-to-video (two attached images make a start→end keyframe transition), and extending an attached clip; it cannot transfer a video's motion onto an image or use attached videos as generation references — switch Video Provider to fal.ai in Settings → Models")

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
// backend cannot serve (motion control, reference videos), so the resolver
// sees exactly five shapes: text-to-video, image-to-video (the source frame
// lands on frameImages — one image auto-distributes to the first frame), a
// keyframe transition (two images on frameImages, auto-distributed first and
// last, schema-gated below on a model that holds the pair), a reference turn
// (useVideoAs:"reference" — the images ride referenceImages, never the
// opening frame, and the clip is shaped from the call's or the configured
// aspect rather than the reference image's), and an extend (the source
// clip on inputs.video). A frame turn carrying an explicit ratio is shaped
// the same way wherever the model's rules accept a size beside its frames.
func resolveRunwareVideoTask(schema *ModelInputSchema, req VideoGenerateRequest) (map[string]any, []string, []string, error) {
	task := runwareTaskBase("videoInference", req.Model)
	var notices []string
	var diagnostics []string
	prompt := strings.TrimSpace(req.Prompt)
	sourceVideos := req.SourceVideos()
	sourceImages := req.SourceImages()
	extend := req.ExtendSource && len(sourceVideos) > 0
	// referenceGuided is set once the images land on referenceImages — they
	// are guidance with no frame to inherit, which the output-shape rule
	// below reads (a start frame, or a start→end pair, fixes orientation).
	referenceGuided := false

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
		// A reference turn (useVideoAs:"reference") sends its images as
		// guidance — characters, style, scenes — never as the clip's opening
		// frame. The role went unread here, so one reference image landed on
		// frameImages on every model declaring it and a character sheet became
		// frame one.
		reference := req.VideoRole == "reference"
		// A keyframe transition is only honest on a model that anchors BOTH of
		// the user's frames. Without frameImages the two images would degrade
		// onto referenceImages or image and the output would not start or end
		// where the user's frames are. With a one-entry ceiling the clamp
		// below kept the opening frame and dropped the closing one on a paid
		// generation — the first-frame-only packagings (Kling 4.0 Flash, Grok
		// Imagine, Hailuo 2.3, Runway Gen-4.5, ...) sit in the same
		// image-to-video picker. Either way, fail before the submit — nothing
		// has been paid yet.
		if req.Keyframes && schema != nil {
			if prop, ok := nested["frameImages"]; !ok || prop.MaxItems == 1 {
				gap := "declares no start/end frame input (inputs.frameImages)"
				if ok {
					gap = "takes only one frame image (inputs.frameImages caps at one entry — the first frame, never a last)"
				}
				return nil, nil, nil, fmt.Errorf("Runware model %q %s and cannot drive a keyframe transition; pick a keyframe-capable model (Kling 4.0 and the MiniMax H3 family both take two) or switch Video Provider to fal.ai in Settings → Models", req.Model, gap)
			}
		}
		switch {
		case schema != nil && hasKey(nested, "frameImages") && (req.Keyframes || (len(refs) == 1 && !reference)):
			// One image auto-distributes to the first frame; two ride in order
			// — the endpoint anchors refs[0] as the first frame and refs[1] as
			// the last (the auto-distribution contract every frameImages
			// packaging publishes), so the executor's openingFrame swap already
			// oriented the pair, and plain strings need no model-specific
			// {image, frame} tagging. A single image lands here only when the
			// call names no reference role — that turn falls through to
			// referenceImages — while a keyframe turn keeps its frames whatever
			// role the call also carries: the flag is the specific statement.
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
			referenceGuided = true
		case schema != nil && hasKey(nested, "image"):
			if len(refs) > 1 {
				notices = append(notices, fmt.Sprintf("Runware model %q takes one source image; used the first of %d.", req.Model, len(refs)))
			}
			runwareSetInput(schema, task, "image", refs[0])
		default:
			// A reference turn on a frame-only model has nowhere honest to go:
			// its frameImages IS the opening frame, exactly what the role said
			// these images are not. Name that gap over the generic line.
			if reference && schema != nil && hasKey(nested, "frameImages") {
				return nil, nil, nil, fmt.Errorf("Runware model %q takes start frames only (inputs.frameImages) and declares no reference-image input (inputs.referenceImages), so it cannot use %d attached image(s) as references; pick a model that declares inputs.referenceImages (Kling 4.0, Seedance 2.0, and the MiniMax H3 family do) or switch Video Provider to fal.ai in Settings → Models", req.Model, len(refs))
			}
			return nil, nil, nil, fmt.Errorf("Runware model %q declares no image input this app can fill", req.Model)
		}
		diagnostics = append(diagnostics, fmt.Sprintf("runware: %d source image(s) mapped onto the model's declared image input", len(refs)))
	}

	// Duration — never beside an extend source (see above).
	if !extend {
		if duration := strings.TrimSpace(req.Duration); duration != "" && duration != "auto" {
			if prop, ok := schema.property("duration"); ok {
				// Bind, don't drop: an in-range duration rides, an over-ask or
				// under-ask clamps to the violated bound (or snaps to the
				// nearest enum member) under a notice naming the length the
				// clip actually gets; only an unreadable value drops
				// (videoDurationClamped, the fal-side fix).
				value, clampNotice, sendable := videoDurationClamped(req.Model, prop, duration)
				if !sendable {
					notices = append(notices, videoDurationDroppedNotice(req.Model, duration, prop))
				} else {
					if clampNotice != "" {
						notices = append(notices, clampNotice)
					}
					task["duration"] = value
				}
			} else {
				notices = append(notices, fmt.Sprintf("The selected model %q has no duration control; ignoring the requested duration.", req.Model))
			}
		}
	}

	// Output shape. An extend inherits the source clip's shape entirely —
	// Veo's schema 400s width/height/seed/resolution beside inputs.video —
	// and was fully resolved above, so nothing here writes to it. An
	// image-to-video inherits the source frame's orientation too (the
	// fal-side rule; no pixels ride beside input media), but the tier still
	// does: the packagings publish resolution beside input media with the
	// documented contract "automatically matches the aspect ratio from the
	// input", so the configured tier scales the output instead of silently
	// landing on the model's own default (Seedance 2.x defaults 720p). The
	// restyle resolver has always sent it this way.
	//
	// An image turn is otherwise shaped from an aspect only where the model's
	// own rules accept a size beside the images it carries (runwareValidSize —
	// the packagings publish that as an allOf rule, and MiniMax H3 enforces it
	// with a 400). Two turns ask for one: a reference turn, which has no frame
	// to inherit and so takes the call's explicit ratio or else the configured
	// default — never the gateway's image-derived ratio, which describes the
	// reference media (a landscape character sheet) rather than the canvas,
	// the fal rule (resolveVideoBody's referenceGuided) — and a frame turn
	// with an EXPLICIT ratio, where the planner's ask legitimately overrides
	// the frame (the fal inherit rule's one exception). The shape rides as
	// width/height presets, because Runware documents resolution as unusable
	// beside them. With no aspect to shape from (the config at "auto" or
	// unset), or where the rules leave no size the model takes, the turn keeps
	// the image-turn rule above: the tier alone rides, and under the
	// documented contract the clip then takes the input's own shape.
	shapeAspect, shapeExplicit := req.AspectRatio, req.AspectRatioExplicit
	if referenceGuided && !req.AspectRatioExplicit {
		shapeAspect = strings.TrimSpace(req.ConfigAspectRatio)
		shapeExplicit = shapeAspect != "" && !isAutoAspectRatio(shapeAspect)
	}
	tier := strings.TrimSpace(req.Resolution)
	sizeW, sizeH, sizeSubstituted, sizeOK := 0, 0, false, false
	if len(sourceImages) > 0 && shapeExplicit && (referenceGuided || req.AspectRatioExplicit) {
		sizeW, sizeH, sizeSubstituted, sizeOK = runwareValidSize(schema, task, shapeAspect, tier)
	}
	aspectDriven := len(sourceImages) == 0 || sizeOK
	if sizeSubstituted {
		notices = append(notices, fmt.Sprintf("Runware model %q does not offer %s beside %s; generated at its nearest allowed size, %dx%d.", req.Model, shapeAspect, runwareShapeSource(referenceGuided, req.Keyframes), sizeW, sizeH))
	}
	if !extend {
		if len(sourceImages) > 0 && !aspectDriven {
			// The turn asked for a shape this model will not take beside these
			// images. Say so when the ask was the planner's own; a configured
			// default that cannot apply is not worth a notice on every call.
			if req.AspectRatioExplicit {
				notices = append(notices, fmt.Sprintf(
					"Runware model %q takes the clip's shape from %s and does not accept an output size beside it, so the explicit %q request was not sent.",
					req.Model, runwareShapeSource(referenceGuided, req.Keyframes), req.AspectRatio))
			}
			if tier != "" {
				if prop, ok := schema.property("resolution"); ok {
					if canonical, allowed := enumValueFor(prop, tier); allowed {
						task["resolution"] = canonical
					} else if snapped, ok := runwareNearestTierEnum(prop.Enum, tier); ok {
						// The requested tier is beyond this model: one
						// substitution to its nearest class beats a silent fall
						// to a lower default (conv_9639d9f6dbc3c6be9c09bc05: an
						// h3@fast image turn configured 1080p — a tier that
						// variant's enum lacks — answered its 480p default,
						// 832x480, when its 768p class was one honest step
						// away).
						task["resolution"] = snapped
						notices = append(notices, fmt.Sprintf("The selected model %q does not offer resolution %q; generated at its closest, %s.", req.Model, tier, snapped))
					} else {
						notices = append(notices, fmt.Sprintf("The selected model %q does not offer resolution %q; using its default.", req.Model, tier))
					}
				} else {
					follows := "the source frame"
					if referenceGuided {
						follows = "the reference media"
					}
					notices = append(notices, fmt.Sprintf("Runware model %q has no resolution control; the clip follows %s and the model's own resolution default.", req.Model, follows))
				}
			}
		} else {
			// Text-to-video — and a reference turn with an aspect to shape
			// from. The packagings publish the tier enum BESIDE
			// width/height (Kling 4.0: ["720p","1080p"] next to its fixed size
			// presets), so the old pixels-win rule — send the tier only when
			// the model takes no pixels — dropped the configured tier on every
			// Runware model, and the preset snap then broke its aspect tie
			// toward the smallest same-ratio size because it scored area
			// against the raw ratio numbers (16x9) instead of pixels. Pixels
			// still win when an aspect is requested — they carry the shape —
			// but the tier now steers WHICH same-shape preset wins: the aspect
			// is rendered at the tier's long edge and the preset scored
			// against that target, so 16:9 at 1080p lands 1920x1080 instead
			// of 1280x720. A tier without an aspect rides the enum alone (the
			// model's default shape at the requested tier); the two fields
			// are never written together.
			tierSent := false
			aspectToken := strings.TrimSpace(shapeAspect)
			_, _, aspectParsed := parseAspectRatio(aspectToken)
			aspectRequested := aspectToken != "" && !isAutoAspectRatio(aspectToken) && aspectParsed
			if tier != "" && schema != nil {
				if prop, ok := schema.property("resolution"); ok {
					canonical, allowed := enumValueFor(prop, tier)
					snapped, snapOK := runwareNearestTierEnum(prop.Enum, tier)
					switch {
					case allowed && !aspectRequested:
						task["resolution"] = canonical
						tierSent = true
					case snapOK && !aspectRequested:
						// The requested tier is beyond this model; one
						// substitution to its nearest class — an honest
						// downgrade, never a silent fall to a lower default.
						task["resolution"] = snapped
						tierSent = true
						notices = append(notices, fmt.Sprintf("The selected model %q does not offer resolution %q; generated at its closest, %s.", req.Model, tier, snapped))
					case !allowed && aspectRequested:
						notices = append(notices, fmt.Sprintf("The selected model %q does not offer resolution %q; generated at its closest available size.", req.Model, tier))
					case !allowed:
						notices = append(notices, fmt.Sprintf("The selected model %q does not offer resolution %q; using its default.", req.Model, tier))
					}
				} else if !aspectRequested {
					notices = append(notices, fmt.Sprintf("Runware model %q has no resolution control; ignoring the requested tier.", req.Model))
				}
			}
			if !tierSent {
				if sizeOK {
					// An image turn shaped from an aspect: a size the model's
					// own rules allow beside its inputs.
					task["width"], task["height"] = sizeW, sizeH
				} else if w, h, ok := runwareVideoDimensions(schema, shapeAspect, tier); ok {
					task["width"], task["height"] = w, h
				} else if aspect := aspectToken; aspect != "" && !isAutoAspectRatio(aspect) && shapeExplicit && schema != nil {
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
	// Delivery: request the documented sync window where the schema declares
	// the dial — every video packaging does, and its default is "async", under
	// which the submit answers with a bare acknowledgment row and the result
	// must be polled (conv_2abf9ca68c1bfb009f303967). With "sync" a task that
	// finishes inside the 120s window delivers in the submit's own response;
	// one that outlives it still comes back "processing" and drops into the
	// same getResponse poll the async path uses.
	if runwareDeclares(schema, "deliveryMethod") {
		task["deliveryMethod"] = "sync"
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
// is rendered onto the declared bounds at the tier's long edge, snapped to
// the 64-px grid most Runware models require. The tier steers WHICH preset
// wins: when the schema also declares a resolution enum (every Runware video
// packaging does), the requested tier snaps to the enum's nearest member and
// the preset is picked from THAT class by aspect — MiniMax H3 publishes a
// ratio-exact 480p preset beside its slightly-off 768p one, so a
// ratio-first snap at a 1080p target landed the 480p class. A class with no
// shape-plausible preset (a 21:9 ask against a 16:9-only class) falls to the
// class-less snap: the aspect rendered at the tier's long edge, scored by
// ratio then area — the raw ratio numbers' tiny product used to tie-break
// toward the smallest same-ratio preset (16:9 at a configured 1080p landed
// 1280x720 instead of 1920x1080). Without a requestable aspect nothing is
// sent and the model uses its own default.
func runwareVideoDimensions(schema *ModelInputSchema, aspect, tier string) (int, int, bool) {
	aw, ah, hasAspect := 1, 1, false
	if token := strings.TrimSpace(aspect); token != "" && !isAutoAspectRatio(token) {
		if w, h, ok := parseAspectRatio(token); ok {
			aw, ah, hasAspect = w, h, true
		}
	}
	if !hasAspect {
		return 0, 0, false
	}
	longEdge := runwareTierLongEdge(tier)
	w, h := longEdge, longEdge*ah/aw
	if aw < ah {
		w, h = longEdge*aw/ah, longEdge
	}
	if schema != nil {
		if prop, ok := schema.property(runwareSizePresetsProperty); ok && len(prop.Enum) > 0 {
			lines, hasClass := runwareTierScanLines(tier)
			if resProp, ok := schema.property("resolution"); ok && len(resProp.Enum) > 0 {
				if snapped, ok := runwareNearestTierEnum(resProp.Enum, tier); ok {
					if memberLines, ok := runwareTierScanLines(snapped); ok {
						lines, hasClass = memberLines, true
					}
				}
			}
			if hasClass {
				if pw, ph, ok := runwarePresetForTier(prop.Enum, lines, aw, ah); ok {
					return pw, ph, true
				}
			}
			return runwareNearestPreset(prop.Enum, w, h)
		}
	}
	if !runwareDeclares(schema, "width") || !runwareDeclares(schema, "height") {
		return 0, 0, false
	}
	return runwareSnapDimension(schema, "width", w), runwareSnapDimension(schema, "height", h), true
}

// runwareTierLongEdge renders a resolution tier as a target long edge in
// pixels — what "1080p" means when scoring preset sizes (1920) and the long
// edge a free-size model gets snapped toward. The canonical Settings tiers
// land on their standard edges; an exotic planner tier ("768p", "1440p")
// reads its number as the short edge of a 16:9 frame; anything unreadable
// keeps the 720p-class default bare aspects have always rendered at.
func runwareTierLongEdge(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "480p":
		return 854
	case "720p":
		return 1280
	case "1080p":
		return 1920
	case "4k":
		return 3840
	}
	if token := strings.ToLower(strings.TrimSpace(tier)); strings.HasSuffix(token, "p") {
		if n, err := strconv.Atoi(strings.TrimSuffix(token, "p")); err == nil && n >= 240 && n <= 4320 {
			return n * 16 / 9
		}
	}
	return 1280
}

// runwareTierScanLines reads a resolution tier as its scan-line count — the
// short edge a preset of that tier renders ("1080p" → 1080, "4k" → 2160 on
// the 16:9 UHD convention, an exotic "Np" planner tier at face value).
// Strictly: an unreadable tier reports not-ok rather than a default, so a
// junk tier can never masquerade as a real class.
func runwareTierScanLines(tier string) (int, bool) {
	token := strings.ToLower(strings.TrimSpace(tier))
	if strings.HasSuffix(token, "p") {
		if n, err := strconv.Atoi(strings.TrimSuffix(token, "p")); err == nil && n >= 144 && n <= 4320 {
			return n, true
		}
	}
	if strings.HasSuffix(token, "k") {
		if n, err := strconv.Atoi(strings.TrimSuffix(token, "k")); err == nil && n >= 1 && n <= 8 {
			return n * 540, true
		}
	}
	return 0, false
}

// runwareNearestTierEnum snaps a requested tier onto the model's own tier
// vocabulary — the enum member whose scan-line count is numerically nearest
// the request's, ties toward the smaller (the conservative cost). A 1080p
// ask on a 480p/768p model degrades one honest step instead of silently
// landing on a lower default (conv_9639d9f6dbc3c6be9c09bc05: an h3@fast
// image turn configured 1080p answered its 480p default — 832x480 — when
// its 768p class was one substitution away).
func runwareNearestTierEnum(members []string, tier string) (string, bool) {
	want, ok := runwareTierScanLines(tier)
	if !ok {
		return "", false
	}
	best, bestDist, bestLines, found := "", math.Inf(1), 0, false
	for _, member := range members {
		lines, ok := runwareTierScanLines(member)
		if !ok {
			continue
		}
		dist := math.Abs(float64(lines - want))
		if dist < bestDist-1e-9 || (dist <= bestDist+1e-9 && lines < bestLines) {
			best, bestDist, bestLines, found = member, dist, lines, true
		}
	}
	return best, found
}

// runwarePresetForTier picks from the preset class the scan-line count names
// — presets whose short edge equals it — the one whose aspect is nearest the
// request's (log-distance), provided that nearest stays shape-plausible
// (within runwareTierClassRatioTolerance): a tier class never carries every
// aspect, and forcing a 21:9 ask into the class's 16:9 preset would distort
// the clip where the class-less snap would have found the model's own
// ultrawide size. Not-ok sends the caller to that class-less snap.
func runwarePresetForTier(presets []string, lines, aw, ah int) (int, int, bool) {
	want := math.Log(float64(aw) / float64(ah))
	bestW, bestH := 0, 0
	bestRatio := math.Inf(1)
	for _, p := range presets {
		var w, h int
		if _, err := fmt.Sscanf(p, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			continue
		}
		short := w
		if h < short {
			short = h
		}
		if short != lines {
			continue
		}
		if dist := math.Abs(math.Log(float64(w)/float64(h)) - want); dist < bestRatio-1e-9 {
			bestW, bestH, bestRatio = w, h, dist
		}
	}
	if bestW == 0 || bestRatio > runwareTierClassRatioTolerance {
		return 0, 0, false
	}
	return bestW, bestH, true
}

// runwareTierClassRatioTolerance is how far (natural-log ratio) a tier
// class's nearest preset may sit from the requested aspect before the class
// counts as empty for that shape (16:9's common neighbors — 1.75:1
// widescreen — sit at ~0.016; a 21:9 ask against a 16:9 class sits at 0.27).
const runwareTierClassRatioTolerance = 0.1

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
