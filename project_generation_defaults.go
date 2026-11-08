package main

import (
	"regexp"
	"strings"
)

// Project notes are prose for the planner (projectInstructionsSection), and
// prose alone proved too weak for the one convention with a structural home:
// the output shape. conv_357cd8af5943e138ab8a9178's project said "All videos
// to be generated in pixar 3d style in aspect ratio 9:16"; the planner read
// that 9:16 as the tool's existing default, omitted aspectRatio, and the
// reference-guided call shipped the Settings default — a 1920x1080 clip the
// final reply then narrated as 9:16. applyProjectGenerationDefaults closes the
// gap in code: an explicit ratio a note scopes to videos (or images) becomes
// that project's generation default, so it holds whether or not the planner
// copies it into the call.

// projectNoteRatioPattern matches a ratio written with a colon ("9:16",
// "16 : 9"). Only the canonical set aspectRatioForPixels names is accepted —
// a timestamp like "10:30" never matches a preset, and other separators
// ("2x3 grid", "9/16") read as too many other things to trust.
var projectNoteRatioPattern = regexp.MustCompile(`\b(\d{1,2})\s*:\s*(\d{1,2})\b`)

var projectNoteCanonicalRatios = map[string]bool{
	"1:1": true, "16:9": true, "9:16": true, "4:3": true,
	"3:4": true, "3:2": true, "2:3": true, "21:9": true,
}

// projectNoteSentenceBreak splits a note into the clauses a ratio's scope is
// read from: lines, semicolons, and sentence-ending punctuation followed by
// whitespace (a ratio's own colon never splits).
var projectNoteSentenceBreak = regexp.MustCompile(`[\n;]+|[.!?]+(\s+|$)`)

var (
	projectNoteVideoWords = regexp.MustCompile(`(?i)\b(videos?|clips?|footage|films?|movies?|reels?)\b`)
	projectNoteImageWords = regexp.MustCompile(`(?i)\b(images?|pictures?|photos?|photographs?|posters?|illustrations?|thumbnails?|artworks?|stills)\b`)
)

// projectNoteAspectRatios reads the image and video aspect ratios a project
// note asks for. Each clause's ratios apply to the media kinds it names — a
// clause naming videos binds only video, one naming images only image, one
// naming both or neither binds both. A kind resolves only when exactly one
// distinct ratio binds it: "images 1:1, videos 9:16" in one clause, or
// "use 9:16, never 16:9", is ambiguous and yields "" — the planner, which
// reads the whole note, keeps that call rather than code guessing.
func projectNoteAspectRatios(notes string) (image, video string) {
	imageRatios := map[string]bool{}
	videoRatios := map[string]bool{}
	for _, clause := range projectNoteSentenceBreak.Split(notes, -1) {
		var ratios []string
		for _, m := range projectNoteRatioPattern.FindAllStringSubmatch(clause, -1) {
			ratio := m[1] + ":" + m[2]
			if projectNoteCanonicalRatios[ratio] {
				ratios = append(ratios, ratio)
			}
		}
		if len(ratios) == 0 {
			continue
		}
		namesVideo := projectNoteVideoWords.MatchString(clause)
		namesImage := projectNoteImageWords.MatchString(clause)
		bindVideo := namesVideo || !namesImage
		bindImage := namesImage || !namesVideo
		for _, ratio := range ratios {
			if bindVideo {
				videoRatios[ratio] = true
			}
			if bindImage {
				imageRatios[ratio] = true
			}
		}
	}
	return soleRatio(imageRatios), soleRatio(videoRatios)
}

func soleRatio(ratios map[string]bool) string {
	if len(ratios) != 1 {
		return ""
	}
	for ratio := range ratios {
		return ratio
	}
	return ""
}

// applyProjectGenerationDefaults overlays the project note's aspect ratios
// onto the per-turn config copy's generation defaults. It runs between the
// global config and the conversation's model overrides, so the precedence is
// Settings < project note < conversation override < an explicit aspectRatio
// on the tool call. Being a DEFAULT (not an explicit request) keeps every
// source-media rule intact: image-to-video still inherits its frame and an
// extend its clip, while text-to-video and reference-guided turns — whose
// guidance media sets no orientation — take the project's shape.
func applyProjectGenerationDefaults(config AppConfig, notes string) AppConfig {
	if strings.TrimSpace(notes) == "" {
		return config
	}
	image, video := projectNoteAspectRatios(notes)
	if image != "" {
		config.Generation.Image.AspectRatio = image
	}
	if video != "" {
		config.Generation.Video.AspectRatio = video
	}
	return config
}
