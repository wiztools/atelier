package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Character replacement — the replace_character tool. The category is defined
// by WHICH SCENE SURVIVES: the attached clip's scene, camera, motion, and
// performance are kept while the person in it becomes the character in the
// attached reference images. That separates it from its two neighbors:
//
//   - motion control (generate_video with an image + a video) keeps the IMAGE's
//     scene and borrows the video's motion — DreamActor V2 and the deprecated
//     Wan Motion are that shape ("the reference image to animate"), so they
//     belong in the motion-control slot, not here;
//   - restyle_video re-renders the whole look of the clip (anime, claymation),
//     not one identity inside it.
//
// The verified fal endpoints span three input shapes, which the resolver
// detects from each model's schema rather than a model list:
//
//   - a references ARRAY, one photo per person (minimax/h3-max/recast:
//     reference_image_urls 1–4, mapped left to right, optional prompt);
//   - character ELEMENTS, a frontal image plus extra angles each — the
//     character-sheet shape (the Kling o1/o3 video-to-video edit endpoints:
//     elements[{frontal_image_url, reference_image_urls}], a REQUIRED prompt
//     that must name them @Element1…);
//   - a SCALAR image, one person, no prompt (fal-ai/wan/v2.2-14b/animate/replace).
//
// Fal-only, like generate_voice: the tool does not follow VideoProvider, so a
// Replicate- or Runware-routed video setup still offers it with a fal key.

// characterSynonyms lists, per canonical param, the native key names to look
// for in a character-replacement model's schema (the "character" category —
// resolveCharacterReplaceBody). characterElements is the Kling elements array;
// characterImages the per-person reference photo input (array or scalar).
var characterSynonyms = map[string][]string{
	"sourceVideo":       {"video_url", "video_urls"},
	"prompt":            {"prompt"},
	"characterElements": {"elements"},
	"characterImages":   {"reference_image_urls", "image_urls", "image_url"},
	"resolution":        {"resolution"},
	"keepAudio":         {"keep_audio"},
}

// characterElementExtraAnglesMax is the Kling element's documented extra-angle
// cap ("1-3 images supported") — applied when the schema declares no maxItems.
const characterElementExtraAnglesMax = 3

// characterElementsTotalMax is Kling's documented combined cap on elements +
// reference images when a video rides the call ("Maximum 4 total").
const characterElementsTotalMax = 4

// errCharacterReplaceUnsupported marks a request the configured model cannot
// serve as asked (too many people, no character input). It refuses BEFORE any
// money moves: a clip that swaps one of two requested people is a wrong video
// whether or not a notice admits it — the errFalKeyframesUnsupported stance.
var errCharacterReplaceUnsupported = errors.New("character replacement unsupported by the configured model")

var (
	// "Min duration: 3.0s, Max duration: 10.05s" — fal's standard file
	// constraint suffix on Kling's video_url description.
	characterMinDurationPattern = regexp.MustCompile(`(?i)min duration:\s*([0-9]+(?:\.[0-9]+)?)\s*s`)
	characterMaxDurationPattern = regexp.MustCompile(`(?i)max duration:\s*([0-9]+(?:\.[0-9]+)?)\s*s`)
	// "5 to 30 seconds long" — Recast's prose spelling.
	characterRangeDurationPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(?:to|-|–)\s*([0-9]+(?:\.[0-9]+)?)\s*seconds`)
)

// characterSourceDurationWindow reads the source clip's accepted duration from
// the video input's own documentation. ok is false when the description names
// no window — the resolver then sends the clip and lets fal judge it.
func characterSourceDurationWindow(description string) (minSeconds, maxSeconds float64, ok bool) {
	if m := characterMaxDurationPattern.FindStringSubmatch(description); m != nil {
		maxSeconds, _ = strconv.ParseFloat(m[1], 64)
		if n := characterMinDurationPattern.FindStringSubmatch(description); n != nil {
			minSeconds, _ = strconv.ParseFloat(n[1], 64)
		}
		return minSeconds, maxSeconds, maxSeconds > 0
	}
	if m := characterRangeDurationPattern.FindStringSubmatch(description); m != nil {
		minSeconds, _ = strconv.ParseFloat(m[1], 64)
		maxSeconds, _ = strconv.ParseFloat(m[2], 64)
		return minSeconds, maxSeconds, maxSeconds > minSeconds
	}
	return 0, 0, false
}

// characterElementTokenPattern matches an @ElementN reference in a prompt.
var characterElementTokenPattern = regexp.MustCompile(`@Element[0-9]+`)

// characterElementsLegend authors the sentence that binds Kling's @ElementN
// tokens to the swap. The element endpoints REQUIRE the prompt to name them —
// without the token the model has no way to know an element is the
// replacement rather than an extra prop. videoToken is "@Video1" when the
// model's prompt documentation names that syntax (o3), else plain prose.
func characterElementsLegend(count int, videoToken string) string {
	subject := "the video"
	if videoToken != "" {
		subject = videoToken
	}
	if count == 1 {
		return fmt.Sprintf("Replace the main character in %s with @Element1, keeping the original motion, performance, camera, timing, and setting.", subject)
	}
	tokens := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		tokens = append(tokens, fmt.Sprintf("@Element%d", i))
	}
	return fmt.Sprintf("Replace the main people in %s, from left to right, with %s, keeping the original motion, performance, camera, timing, and setting.", subject, strings.Join(tokens, ", "))
}

// resolveCharacterReplaceBody maps the canonical character-replacement request
// onto the configured endpoint's native fields. Returns the body, the
// user-facing notices, the internal diagnostics, and an error that refuses the
// call before any money moves when the model cannot serve the request.
func resolveCharacterReplaceBody(schema *ModelInputSchema, req VideoCharacterReplaceRequest, ov Overrides) (map[string]any, []string, []string, error) {
	video := falVideoURL(strings.TrimSpace(req.Video))
	prompt := strings.TrimSpace(req.Prompt)
	characters := make([][]string, 0, len(req.Characters))
	for _, character := range req.Characters {
		images := make([]string, 0, len(character.Images))
		for _, img := range character.Images {
			if u := falImageURL(strings.TrimSpace(img)); u != "" {
				images = append(images, u)
			}
		}
		if len(images) > 0 {
			characters = append(characters, images)
		}
	}
	if video == "" {
		return nil, nil, nil, errors.New("replace_character requires a source video")
	}
	if len(characters) == 0 {
		return nil, nil, nil, errors.New("replace_character requires at least one character reference image")
	}

	if schema == nil {
		// The default (Recast) shape: one frontal photo per person.
		frontals := make([]any, 0, len(characters))
		for _, images := range characters {
			frontals = append(frontals, images[0])
		}
		body := map[string]any{"video_url": video, "reference_image_urls": frontals}
		if prompt != "" {
			body["prompt"] = prompt
		}
		return body, []string{"Couldn't load the model's parameter schema; sent the source video and one reference photo per character with default field names."}, nil, nil
	}

	body := map[string]any{}
	var notices, diagnostics []string

	videoPath, videoProp, ok := findNative(schema, ov, "character", req.Model, "sourceVideo")
	if !ok {
		return nil, nil, nil, fmt.Errorf("%w: the model %q has no video input — it cannot replace a character in an attached clip; pick a character-replacement model in Settings", errCharacterReplaceUnsupported, req.Model)
	}
	if req.SourceSeconds > 0 {
		if minSeconds, maxSeconds, ok := characterSourceDurationWindow(videoProp.Description); ok {
			switch {
			case req.SourceSeconds > maxSeconds:
				return nil, nil, nil, fmt.Errorf("%w: the source clip is %.1fs but %q accepts at most %gs — split the clip first (split_video) and replace the character in each part", errCharacterReplaceUnsupported, req.SourceSeconds, req.Model, maxSeconds)
			case minSeconds > 0 && req.SourceSeconds < minSeconds:
				return nil, nil, nil, fmt.Errorf("%w: the source clip is %.1fs but %q needs at least %gs of footage", errCharacterReplaceUnsupported, req.SourceSeconds, req.Model, minSeconds)
			}
		}
	}
	setBodyPath(schema, body, videoPath, coerceVideoValue(videoProp, video))

	promptPath, promptProp, promptDeclared := findNative(schema, ov, "character", req.Model, "prompt")

	elementsPath, elementsProp, hasElements := findNative(schema, ov, "character", req.Model, "characterElements")
	if hasElements && elementsProp.Kind == schemaArray && elementsProp.Items != nil {
		if _, frontal := elementsProp.Items.Nested["frontal_image_url"]; !frontal {
			hasElements = false
		}
	} else {
		hasElements = false
	}

	switch {
	case hasElements:
		total := characterElementsTotalMax
		if elementsProp.MaxItems > 0 && elementsProp.MaxItems < total {
			total = elementsProp.MaxItems
		}
		if len(characters) > total {
			return nil, nil, nil, fmt.Errorf("%w: %q accepts at most %d characters per clip, and %d were requested", errCharacterReplaceUnsupported, req.Model, total, len(characters))
		}
		anglesMax := characterElementExtraAnglesMax
		if refs, ok := elementsProp.Items.Nested["reference_image_urls"]; ok && refs.MaxItems > 0 {
			anglesMax = refs.MaxItems
		}
		elements := make([]any, 0, len(characters))
		trimmed := false
		for _, images := range characters {
			angles := images[1:]
			if len(angles) > anglesMax {
				angles = angles[:anglesMax]
				trimmed = true
			}
			if len(angles) == 0 {
				// The element documents "At least one image is required" for
				// its extra angles; a single-photo character repeats its
				// frontal view there rather than sending an element the
				// endpoint may refuse.
				angles = []string{images[0]}
				diagnostics = append(diagnostics, "A character with a single reference image reused it as the element's extra angle — the endpoint documents at least one.")
			}
			refs := make([]any, 0, len(angles))
			for _, angle := range angles {
				refs = append(refs, angle)
			}
			elements = append(elements, map[string]any{
				"frontal_image_url":    images[0],
				"reference_image_urls": refs,
			})
		}
		if trimmed {
			notices = append(notices, fmt.Sprintf("The selected model %q takes a frontal image plus at most %d extra angles per character; the remaining reference images were not sent.", req.Model, anglesMax))
		}
		setBodyPath(schema, body, elementsPath, elements)
		if !promptDeclared {
			return nil, nil, nil, fmt.Errorf("%w: %q takes character elements but no prompt to name them", errCharacterReplaceUnsupported, req.Model)
		}
		videoToken := ""
		if strings.Contains(promptProp.Description, "@Video1") {
			videoToken = "@Video1"
		}
		if !characterElementTokenPattern.MatchString(prompt) {
			legend := characterElementsLegend(len(elements), videoToken)
			if prompt == "" {
				prompt = legend
			} else {
				prompt = legend + " " + prompt
			}
			diagnostics = append(diagnostics, "The prompt named no @Element tokens, so the replacement sentence binding them was prepended.")
		}
		setBodyPath(schema, body, promptPath, coerceVideoValue(promptProp, prompt))

	default:
		imagesPath, imagesProp, ok := findNative(schema, ov, "character", req.Model, "characterImages")
		if !ok {
			return nil, nil, nil, fmt.Errorf("%w: the model %q has no character reference input; pick a character-replacement model in Settings", errCharacterReplaceUnsupported, req.Model)
		}
		frontals := make([]string, 0, len(characters))
		extraAngles := false
		for _, images := range characters {
			frontals = append(frontals, images[0])
			if len(images) > 1 {
				extraAngles = true
			}
		}
		if imagesProp.Kind == schemaArray {
			if imagesProp.MaxItems > 0 && len(frontals) > imagesProp.MaxItems {
				return nil, nil, nil, fmt.Errorf("%w: %q replaces at most %d people per clip, and %d were requested", errCharacterReplaceUnsupported, req.Model, imagesProp.MaxItems, len(frontals))
			}
			setBodyPath(schema, body, imagesPath, coerceImages(imagesProp, frontals))
		} else {
			if len(frontals) > 1 {
				return nil, nil, nil, fmt.Errorf("%w: %q replaces a single person and %d were requested — replace one character per call, or pick a multi-person model (minimax/h3-max/recast) in Settings", errCharacterReplaceUnsupported, req.Model, len(frontals))
			}
			setBodyPath(schema, body, imagesPath, coerceVideoValue(imagesProp, frontals[0]))
		}
		if extraAngles {
			notices = append(notices, fmt.Sprintf("The selected model %q takes one reference photo per character; only the first image of each character sheet was sent.", req.Model))
		}
		if prompt != "" {
			if promptDeclared {
				setBodyPath(schema, body, promptPath, coerceVideoValue(promptProp, prompt))
			} else {
				notices = append(notices, fmt.Sprintf("The selected model %q takes no prompt; the replacement follows the reference image alone.", req.Model))
			}
		}
	}

	keep := true
	if req.KeepAudio != nil {
		keep = *req.KeepAudio
	}
	if path, prop, ok := findNative(schema, ov, "character", req.Model, "keepAudio"); ok {
		setBodyPath(schema, body, path, coerceVideoValue(prop, keep))
	} else if req.KeepAudio != nil && !keep {
		notices = append(notices, fmt.Sprintf("The selected model %q has no soundtrack control; its own audio behavior applies.", req.Model))
	}

	if res := strings.TrimSpace(req.Resolution); res != "" {
		if path, prop, ok := findNative(schema, ov, "character", req.Model, "resolution"); ok {
			if canonical, allowed := enumValueFor(prop, res); allowed {
				setBodyPath(schema, body, path, coerceVideoValue(prop, canonical))
			} else {
				notices = append(notices, fmt.Sprintf("%q isn't a resolution %q offers (%s); the model's default was used.", res, req.Model, strings.Join(prop.Enum, ", ")))
			}
		} else {
			notices = append(notices, fmt.Sprintf("The selected model %q has no resolution control; ignoring the requested %s.", req.Model, res))
		}
	}
	return body, notices, diagnostics, nil
}

// splitCharacterReferences groups the turn's images into characters. With no
// counts every image belongs to ONE character (a character sheet — the common
// ask); counts split them in order, and must account for every image.
func splitCharacterReferences(images []string, counts []int) ([]CharacterReference, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if len(counts) == 0 {
		return []CharacterReference{{Images: append([]string(nil), images...)}}, nil
	}
	sum := 0
	for _, n := range counts {
		if n <= 0 {
			return nil, fmt.Errorf("characterImageCounts entries must be positive, got %v", counts)
		}
		sum += n
	}
	if sum != len(images) {
		return nil, fmt.Errorf("characterImageCounts %v accounts for %d images but %d are attached", counts, sum, len(images))
	}
	characters := make([]CharacterReference, 0, len(counts))
	next := 0
	for _, n := range counts {
		characters = append(characters, CharacterReference{Images: append([]string(nil), images[next:next+n]...)})
		next += n
	}
	return characters, nil
}

// resolveCharacterProvider is the routing truth for replace_character — the
// gate, the default model, the gateway branch, and the activity stamp all
// read it. Its own dimension (Models.CharacterProvider): the tool does not
// follow VideoProvider.
func resolveCharacterProvider(config AppConfig) string {
	if strings.TrimSpace(config.Models.CharacterProvider) == "runware" {
		return "runware"
	}
	return "fal"
}

// videoCharacterReplaceConfigured gates the replace_character tool on the
// routed provider's key; airplane mode drops it like every cloud tool.
func videoCharacterReplaceConfigured(config AppConfig) bool {
	if airplaneModeActive(config) {
		return false
	}
	if resolveCharacterProvider(config) == "runware" {
		return runwareKeyConfigured()
	}
	return falKeyConfigured()
}

// resolveDefaultVideoCharacterModel returns the model replace_character uses
// when the call doesn't override it, routing by the character provider.
func resolveDefaultVideoCharacterModel(config AppConfig) string {
	if resolveCharacterProvider(config) == "runware" {
		if model := strings.TrimSpace(config.Providers.Runware.VideoCharacterModel); model != "" {
			return model
		}
		return defaultRunwareVideoCharacterModel
	}
	return resolveFalVideoCharacterModel(config)
}

// resolveFalVideoCharacterModel is the fal slot alone — the pricing batch
// reads it so a runware-routed config never sends a Runware AIR id to fal's
// all-or-nothing pricing API.
func resolveFalVideoCharacterModel(config AppConfig) string {
	if model := strings.TrimSpace(config.Providers.Fal.VideoCharacterModel); model != "" {
		return model
	}
	return defaultFalVideoCharacterModel
}

// runwarePerPersonReferencesPattern reads a referenceImages description that
// documents one photo per PERSON (P-Video-2-Pro-Replace: "One image per person
// to replace, matched to the people in the video from left to right"). Without
// that wording the references are angles of ONE character (P-Video-Replace).
var runwarePerPersonReferencesPattern = regexp.MustCompile(`(?i)\bper person\b`)

// resolveRunwareCharacterTask maps the canonical character-replacement request
// onto one Runware videoInference task. Runware's vocabulary is standardized
// (inputs.video + inputs.referenceImages, settings.preserveAudio), so the
// schema only decides whether a field may be written and how the references
// are read — one per person, or one character's angles.
func resolveRunwareCharacterTask(schema *ModelInputSchema, req VideoCharacterReplaceRequest) (map[string]any, []string, []string, error) {
	task := runwareTaskBase("videoInference", req.Model)
	var notices, diagnostics []string
	if schema == nil {
		notices = append(notices, "Couldn't load the Runware model's parameter schema; generated with default parameters.")
	}
	if strings.TrimSpace(req.Video) == "" {
		return nil, nil, nil, errors.New("replace_character requires a source video")
	}
	characters := make([][]string, 0, len(req.Characters))
	for _, character := range req.Characters {
		images := make([]string, 0, len(character.Images))
		for _, img := range character.Images {
			if strings.TrimSpace(img) != "" {
				images = append(images, runwareImageRef(img))
			}
		}
		if len(images) > 0 {
			characters = append(characters, images)
		}
	}
	if len(characters) == 0 {
		return nil, nil, nil, errors.New("replace_character requires at least one character reference image")
	}
	if !runwareDeclaresInput(schema, "video") {
		return nil, nil, nil, fmt.Errorf("%w: Runware model %q takes no source-video input; pick a character-replacement model in Settings", errCharacterReplaceUnsupported, req.Model)
	}
	if !runwareDeclaresInput(schema, "referenceImages") {
		return nil, nil, nil, fmt.Errorf("%w: Runware model %q takes no reference images; pick a character-replacement model in Settings", errCharacterReplaceUnsupported, req.Model)
	}
	refsProp := runwareInputsNested(schema)["referenceImages"]
	maxItems := refsProp.MaxItems
	var refs []string
	if runwarePerPersonReferencesPattern.MatchString(refsProp.Description) {
		extraAngles := false
		for _, images := range characters {
			refs = append(refs, images[0])
			if len(images) > 1 {
				extraAngles = true
			}
		}
		if maxItems > 0 && len(refs) > maxItems {
			return nil, nil, nil, fmt.Errorf("%w: Runware model %q replaces at most %d people per clip, and %d were requested", errCharacterReplaceUnsupported, req.Model, maxItems, len(refs))
		}
		if extraAngles {
			notices = append(notices, fmt.Sprintf("The selected model %q takes one reference photo per person; only the first image of each character sheet was sent.", req.Model))
		}
	} else {
		if len(characters) > 1 {
			return nil, nil, nil, fmt.Errorf("%w: Runware model %q replaces a single character and %d were requested — replace one character per call, or pick a multi-person model (%s) in Settings", errCharacterReplaceUnsupported, req.Model, len(characters), defaultRunwareVideoCharacterModel)
		}
		refs = characters[0]
		if maxItems > 0 && len(refs) > maxItems {
			refs = refs[:maxItems]
			notices = append(notices, fmt.Sprintf("The selected model %q takes at most %d reference images per character; the first %d were sent.", req.Model, maxItems, maxItems))
		}
	}
	runwareSetInput(schema, task, "video", runwareVideoRef(req.Video))
	runwareSetInput(schema, task, "referenceImages", refs)

	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		if runwareDeclares(schema, "positivePrompt") {
			task["positivePrompt"] = prompt
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q takes no prompt; the replacement follows the reference images alone.", req.Model))
		}
	}
	keep := true
	if req.KeepAudio != nil {
		keep = *req.KeepAudio
	}
	if settingsProp, ok := schema.property("settings"); ok && settingsProp.Kind == schemaObject && hasKey(settingsProp.Nested, "preserveAudio") {
		task["settings"] = map[string]any{"preserveAudio": keep}
	} else if req.KeepAudio != nil && !keep {
		notices = append(notices, fmt.Sprintf("Runware model %q has no soundtrack control; its own audio behavior applies.", req.Model))
	}
	if tier := strings.TrimSpace(req.Resolution); tier != "" {
		if prop, ok := schema.property("resolution"); ok {
			if canonical, allowed := enumValueFor(prop, tier); allowed {
				task["resolution"] = canonical
			} else {
				notices = append(notices, fmt.Sprintf("%q isn't a resolution %q offers (%s); the model's default was used.", tier, req.Model, strings.Join(prop.Enum, ", ")))
			}
		} else {
			notices = append(notices, fmt.Sprintf("Runware model %q has no resolution control; ignoring the requested %s.", req.Model, tier))
		}
	}
	if runwareDeclares(schema, "outputFormat") {
		task["outputFormat"] = "MP4"
	}
	// The sync window, the resolveRunwareVideoTask rule: the default "async"
	// answers with a bare acknowledgment row.
	if runwareDeclares(schema, "deliveryMethod") {
		task["deliveryMethod"] = "sync"
	}
	return task, notices, diagnostics, nil
}

// videoCharacterReplaceToolDefinition exposes replace_character. The result
// rides ToolVideoResult like restyle_video, so artifacts, history,
// carry-forward, and the reply's video card work unchanged.
func videoCharacterReplaceToolDefinition(config AppConfig) HarnessToolDefinition {
	return HarnessToolDefinition{
		Name:        "replace_character",
		Title:       "Replace character",
		Risk:        HarnessToolRiskRead,
		Example:     `{"name":"replace_character","content":"the woman in the red coat becomes the character in the reference images"}`,
		Description: "Use this when the user wants the PERSON in an attached video swapped for a character shown in attached images while the video's own scene, camera, motion, performance, and timing are kept — \"replace the actor with my character\", \"put this character in the clip\", \"swap him for @img_…\". Requires an attached video and at least one attached image of the new character. This is not motion control (generate_video with an image + a video keeps the IMAGE's scene and only borrows the motion) and not restyle_video (which re-renders the whole look). By default every attached image is ONE character's sheet (a front view plus extra angles); when several people are being replaced, pass characterImageCounts listing how many of the attached images belong to each person, in left-to-right order of the people in the clip (e.g. [2,1]). content is an optional steer — who becomes whom, what else to keep or change. The clip's soundtrack is kept where the model supports it; pass keepAudio:false only when the user wants it dropped. Runs on the configured character-replacement provider (" + characterProviderLabel(config) + "), takes a minute or more, and source clips are capped by the model (about 10 seconds on Kling o1, 30 on Recast, 2 minutes on Pruna's P-Video replace models).",
		ParamSchema: videoCharacterReplaceParamSchema(),
		Validate: func(prefix string, call HarnessToolCall) []string {
			for _, n := range call.CharacterImageCounts {
				if n <= 0 {
					return []string{prefix + ".characterImageCounts entries must be positive image counts, one per person, e.g. [2,1]"}
				}
			}
			return nil
		},
		Execute: func(ctx context.Context, tools HarnessToolExecutionContext, call HarnessToolCall) (any, string, error) {
			if tools.ReplaceCharacter == nil {
				return nil, "character replacement unavailable", errors.New("character replacement is not available in this context")
			}
			attachedVideo := firstAttachedVideo(tools.AttachedVideos)
			if attachedVideo == "" {
				return nil, "character replacement requires an attached video", errors.New("replace_character requires an attached video — ask the user to attach the clip first")
			}
			if len(tools.AttachedImages) == 0 {
				return nil, "character replacement requires a character image", errors.New("replace_character requires at least one attached image of the new character — ask the user to attach one")
			}
			characters, err := splitCharacterReferences(tools.AttachedImages, call.CharacterImageCounts)
			if err != nil {
				return nil, "character replacement failed", err
			}
			model := strings.TrimSpace(call.Model)
			if model == "" {
				model = resolveDefaultVideoCharacterModel(tools.Config)
			}
			generated, err := tools.ReplaceCharacter(ctx, VideoCharacterReplaceRequest{
				Model:      model,
				Video:      attachedVideo,
				Characters: characters,
				Prompt:     strings.TrimSpace(call.Content),
				Resolution: strings.TrimSpace(call.Resolution),
				KeepAudio:  call.KeepAudio,
			})
			if err != nil {
				return nil, "character replacement failed", err
			}
			if len(generated.Data) == 0 {
				return nil, "character replacement returned no video", errors.New("character replacement model returned no video data")
			}
			tempPath, err := writeTempVideo(generated)
			if err != nil {
				return nil, "character replacement failed", err
			}
			output := ToolVideoResult{
				Model:       model,
				Prompt:      strings.TrimSpace(call.Content),
				Count:       1,
				Videos:      []ToolVideoFile{{TempPath: tempPath, MimeType: generated.MimeType, SourceURL: generated.SourceURL}},
				Notices:     generated.Notices,
				Diagnostics: generated.Diagnostics,
				CostMicros:  generated.CostMicros,
			}
			people := "the character"
			if len(characters) > 1 {
				people = fmt.Sprintf("%d characters", len(characters))
			}
			return output, fmt.Sprintf("replaced the person in the attached video with %s from the reference images using %s, keeping the original scene and performance", people, model), nil
		},
		Activity: func(result HarnessToolResult) HarnessToolActivity {
			activity := defaultHarnessToolActivity(result)
			if typed, ok := result.Result.(ToolVideoResult); ok {
				// The builder stamps the routed provider itself (captured from
				// the config the definition was built against — the lipsync
				// pattern): the engine layer would attribute a video result to
				// VideoProvider, which this tool does not follow.
				provider := resolveCharacterProvider(config)
				activity.Provider = provider
				activity.Command = []string{provider, "replace-character", typed.Model}
			}
			return activity
		},
	}
}

func videoCharacterReplaceParamSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"content": stringParam(`Optional — a steer for the swap: who becomes whom ("the man on the left becomes the character"), or what else to keep or change. Omit when the user gave none.`),
			"characterImageCounts": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "integer"},
				"description": "Optional — when several people are being replaced, how many of the attached images belong to each person, in left-to-right order of the people in the clip (e.g. [2,1]). Omit when all images show one character.",
			},
			"keepAudio":  boolParam("Optional — true (the default) keeps the clip's original soundtrack; false drops it."),
			"resolution": stringParam(`Optional — the output resolution tier, e.g. "720p" or "1080p" (omit for the model's default; ignored with a notice when the model has no such tier).`),
			"model":      stringParam("Optional character-replacement model override."),
		},
	}
}

// parseCharacterImageCounts reads the kwargs spelling of characterImageCounts
// ("2,1", "[2, 1]"). Unreadable entries yield nil, so the default — every
// image is one character — applies rather than a half-parsed split.
func parseCharacterImageCounts(raw string) []int {
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	counts := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil
		}
		counts = append(counts, n)
	}
	return counts
}

// characterProviderLabel names the routed backend for the tool description.
func characterProviderLabel(config AppConfig) string {
	if resolveCharacterProvider(config) == "runware" {
		return "Runware"
	}
	return "fal.ai"
}
