package main

// The video composition workspace's document format — the OTIO-shaped JSON a
// composition artifact (cmp_<hex>.json) carries: one sequential video lane of
// clips with source ranges and junction transitions, plus additional audio
// lanes of positioned segments. Media refs point at conversation artifacts by
// id + absolute path (the cross-conversation mention shape, so every reader
// resolves them as-is) and carry a facts snapshot seeded at add time so the
// timeline can lay out without probing. This file owns the schema, the
// validation/normalization rules, seeding, and the artifact writer;
// composition_editor.go is the binding layer, and composition_render.go the
// renderer. The vocabulary deliberately mirrors OpenTimelineIO (tracks, clips
// with source ranges, transitions between neighbors, external references) so
// a future OTIO import/export adapter is a mapping, not a redesign.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	compositionSchemaVersion = 1
	// Soft limits the editor enforces client-side; validation refuses beyond
	// them so a runaway paste can't build an unrenderable document.
	compositionMaxClips      = 50
	compositionMaxAudioLanes = 4

	compositionEpsilon         = 0.001
	compositionMinRangeSeconds = 0.01

	compositionTransitionMinSeconds     = 0.1
	compositionTransitionMaxSeconds     = 10.0
	compositionTransitionDefaultSeconds = 1.0

	compositionGainMinDb = -60.0
	compositionGainMaxDb = 12.0

	compositionDefaultFPS  = 30.0
	compositionMaxFPS      = 60.0
	compositionFitPad      = "pad"
	compositionFitCrop     = "crop"
	compositionFitBlur     = "blur"
	compositionDefaultName = "Untitled composition"
)

// CompositionMediaRef pins one media artifact the composition uses — the
// cross-conversation reference shape: an absolute Path plus the owning
// conversation, so a clip picked from a library sibling survives reloads the
// same way an @-mention does. The facts snapshot (duration, display size, fps,
// audio presence) is measured when the ref enters the composition and travels
// with the document; the renderer re-probes at render time, so a stale
// snapshot costs layout precision, never correctness.
type CompositionMediaRef struct {
	ArtifactID     string `json:"artifactId"`
	ConversationID string `json:"conversationId,omitempty"`
	// Path is the absolute path of the artifact file on disk.
	Path     string `json:"path"`
	MimeType string `json:"mimeType,omitempty"`
	// Kind is "video" or "audio" — the HistoryContent type of the referenced
	// artifact. A video ref on an audio lane is legal (detached audio points
	// back at the clip's own artifact); an audio ref on the video lane is not.
	Kind string `json:"kind"`
	// DurationSeconds is the probed source duration the in/out ranges clamp to.
	DurationSeconds float64 `json:"durationSeconds"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	FPS             float64 `json:"fps,omitempty"`
	HasAudio        bool    `json:"hasAudio,omitempty"`
}

// CompositionTransition is the junction between one clip and the next. Nil
// means a hard cut — the absence IS the default, so a seeded document carries
// no transition objects at all. A dissolve names its length and how the
// original audio crosses the junction (an equal-power crossfade by default,
// the join_audios curve; a hard audio cut when AudioCrossfade is false).
type CompositionTransition struct {
	Kind    string  `json:"kind"` // "dissolve" or a curated xfade name (a "cut" normalizes to nil)
	Seconds float64 `json:"seconds,omitempty"`
	// AudioCrossfade crossfades the outgoing and incoming original audio over
	// the dissolve; false cuts the audio hard at the junction (TR-07).
	AudioCrossfade bool `json:"audioCrossfade,omitempty"`
}

// CompositionOriginalAudio carries the per-clip original-audio mix state —
// volume and muting without detaching (AU-04), and the detached marker that
// says the sound now lives as a segment on an audio lane (AU-07): the
// renderer skips the clip's audio and the segment plays it exactly once.
type CompositionOriginalAudio struct {
	GainDb   float64 `json:"gainDb,omitempty"`
	Muted    bool    `json:"muted,omitempty"`
	Detached bool    `json:"detached,omitempty"`
}

// CompositionClip is one video lane entry: a source range into its ref and
// the junction transition that follows it (the last clip's transition, if
// any, is dropped — there is nothing after it to transition into).
type CompositionClip struct {
	ID         string                   `json:"id"`
	Ref        CompositionMediaRef      `json:"ref"`
	InSeconds  float64                  `json:"inSeconds"`
	OutSeconds float64                  `json:"outSeconds"`
	Transition *CompositionTransition   `json:"transitionToNext,omitempty"`
	Audio      CompositionOriginalAudio `json:"audio"`
}

// CompositionDuck lowers a segment's volume over a time range — the step
// envelope the MVP uses for "lower the music during dialogue" (AU-06). Start
// and End are relative to the SEGMENT's own timeline start, not the
// composition's, so moving the segment moves its ducks with it.
type CompositionDuck struct {
	StartSeconds float64 `json:"startSeconds"`
	EndSeconds   float64 `json:"endSeconds"`
	GainDb       float64 `json:"gainDb"`
}

// CompositionSegment is one audio lane entry: a source range into its ref, a
// timeline start, and the mix controls (gain, edge fades, duck ranges).
// DetachedFromClipID links a segment that detach-audio created back to its
// clip, so reattaching removes exactly that segment and nothing else that
// happens to share the artifact.
type CompositionSegment struct {
	ID                 string              `json:"id"`
	Ref                CompositionMediaRef `json:"ref"`
	InSeconds          float64             `json:"inSeconds"`
	OutSeconds         float64             `json:"outSeconds"`
	StartSeconds       float64             `json:"startSeconds"`
	GainDb             float64             `json:"gainDb,omitempty"`
	FadeInSeconds      float64             `json:"fadeInSeconds,omitempty"`
	FadeOutSeconds     float64             `json:"fadeOutSeconds,omitempty"`
	Ducks              []CompositionDuck   `json:"ducks"`
	DetachedFromClipID string              `json:"detachedFromClipId,omitempty"`
}

// CompositionLane is one additional audio lane — segments are ordered and
// non-overlapping on the lane, and the lane carries its own gain and mute.
type CompositionLane struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	GainDb   float64              `json:"gainDb,omitempty"`
	Muted    bool                 `json:"muted,omitempty"`
	Segments []CompositionSegment `json:"segments"`
}

// CompositionOutput is the project output frame. Zero width/height means
// "the first clip's display size"; zero fps means "the first clip's fps,
// else 30" — the join tools' normalize-onto-the-first-clip policy. Fit picks
// how differently-shaped clips conform: pad letterboxes (the default), crop
// center-crops, blur fills the added area with an enlarged blurred copy —
// transform_video's three modes.
type CompositionOutput struct {
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	FPS    float64 `json:"fps,omitempty"`
	Fit    string  `json:"fit,omitempty"`
}

// CompositionDoc is the whole editable project — what a cmp_ artifact
// stores and what the editor edits. Revisions are immutable snapshots: every
// save writes a fresh artifact, so "revision" is just "the artifact an open
// or an export resolved".
type CompositionDoc struct {
	SchemaVersion int               `json:"schemaVersion"`
	Name          string            `json:"name,omitempty"`
	Output        CompositionOutput `json:"output"`
	Video         []CompositionClip `json:"video"`
	AudioLanes    []CompositionLane `json:"audioLanes"`
}

// compositionArtifactExtension reports ".json" when a history content entry
// is a composition artifact. The Type field is the identity (several things
// could share .json); the extension check keeps a hand-renamed artifact from
// masquerading — the same two-step read as markdownArtifactExtension.
func compositionArtifactExtension(content HistoryContent) string {
	if content.Type != "composition" {
		return ""
	}
	if strings.ToLower(filepath.Ext(content.Path)) == ".json" {
		return ".json"
	}
	if strings.EqualFold(content.MimeType, "application/json") {
		return ".json"
	}
	return ""
}

// parseCompositionDoc decodes artifact bytes into a document. Schema versions
// above the current one refuse — a newer document may carry meanings this
// build would silently drop.
func parseCompositionDoc(data []byte) (CompositionDoc, error) {
	var doc CompositionDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return CompositionDoc{}, fmt.Errorf("the composition document is not valid JSON: %w", err)
	}
	if doc.SchemaVersion > compositionSchemaVersion {
		return CompositionDoc{}, fmt.Errorf("composition document schema %d is newer than this build understands (%d)", doc.SchemaVersion, compositionSchemaVersion)
	}
	if doc.SchemaVersion < compositionSchemaVersion {
		doc.SchemaVersion = compositionSchemaVersion
	}
	return doc, nil
}

// compositionFinite reports whether every value is finite (NaN and ±Inf must
// never reach a persisted document — they poison every later comparison).
func compositionFinite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

// compositionClampGain pulls a gain into the supported dB range.
func compositionClampGain(gainDb float64) float64 {
	if !compositionFinite(gainDb) {
		return 0
	}
	if gainDb < compositionGainMinDb {
		return compositionGainMinDb
	}
	if gainDb > compositionGainMaxDb {
		return compositionGainMaxDb
	}
	return gainDb
}

// compositionNormalizeTransition clamps a junction transition against the
// adjacent clip ranges: into [min, max], and at most half the shorter
// neighboring clip (the join tools' clamp — an overlap longer than half a
// clip would consume the clip entirely). A "cut" normalizes to nil; a
// transition with no length takes the default; a junction whose clips cannot
// hold even the minimum overlap is an error, since no clamp could make it
// fit. An unknown kind is an error.
func compositionNormalizeTransition(transition *CompositionTransition, thisDuration, nextDuration float64, index int) (*CompositionTransition, error) {
	if transition == nil || transition.Kind == "" || transition.Kind == "cut" {
		return nil, nil
	}
	kind, known := compositionTransitionKind(transition.Kind)
	if !known {
		return nil, fmt.Errorf("clip %d's transition kind must be \"cut\", \"dissolve\", or an xfade transition name (fadeblack, wipeleft, circleopen, …), got %q", index+1, transition.Kind)
	}
	upper := math.Min(compositionTransitionMaxSeconds, math.Min(thisDuration, nextDuration)/2)
	if upper < compositionTransitionMinSeconds {
		return nil, fmt.Errorf("clip %d's %s does not fit: the adjacent clips are too short for even the minimum %gs overlap", index+1, kind, compositionTransitionMinSeconds)
	}
	seconds := transition.Seconds
	if !compositionFinite(seconds) || seconds <= 0 {
		seconds = compositionTransitionDefaultSeconds
	}
	if seconds > upper {
		seconds = upper
	}
	if seconds < compositionTransitionMinSeconds {
		seconds = compositionTransitionMinSeconds
	}
	return &CompositionTransition{Kind: kind, Seconds: seconds, AudioCrossfade: transition.AudioCrossfade}, nil
}

// compositionTransitions is the composer's junction vocabulary: "dissolve"
// (the composer's name for a plain cross-fade) plus the join tools' curated
// xfade enum minus "fade" itself — ffmpeg also ships a pixel "dissolve", a
// different effect, so the doc never stores that name.
var compositionTransitions = func() []string {
	names := make([]string, 0, len(xfadeTransitions))
	for _, name := range xfadeTransitions {
		if name != "fade" {
			names = append(names, name)
		}
	}
	return names
}()

// compositionTransitionKind normalizes a doc's transition kind: "fade" (the
// raw xfade name) lands on "dissolve", the join tools' crossfade-alias
// pattern inverted. ok is false for anything outside the vocabulary.
func compositionTransitionKind(kind string) (string, bool) {
	if kind == "fade" {
		return "dissolve", true
	}
	for _, name := range compositionTransitions {
		if kind == name {
			return kind, true
		}
	}
	return "", false
}

// compositionXfadeName maps a doc's transition kind onto its ffmpeg xfade
// name — "dissolve" is the composer's word for xfade "fade"; every other
// kind is already the xfade name.
func compositionXfadeName(kind string) string {
	if kind == "dissolve" {
		return "fade"
	}
	return kind
}

// validateCompositionDoc checks and normalizes a document in place — the
// Go half of a contract the frontend's compositionModel mirrors. Normalizing
// (sorting segments, clamping gains and fades) matches what the editor does
// interactively, so a save that reaches an untouched rule still round-trips;
// the errors are for the states the editor refuses to build: empty or
// non-video clip sources, overlapping lane segments, duplicate ids, unknown
// kinds.
func validateCompositionDoc(doc *CompositionDoc) error {
	doc.Name = strings.TrimSpace(doc.Name)
	if doc.Name == "" {
		doc.Name = compositionDefaultName
	}
	doc.SchemaVersion = compositionSchemaVersion
	// Arrays always marshal as arrays — a nil slice would land as JSON null
	// and every frontend reader would need a null case.
	if doc.Video == nil {
		doc.Video = []CompositionClip{}
	}
	if doc.AudioLanes == nil {
		doc.AudioLanes = []CompositionLane{}
	}
	if len(doc.Video) > compositionMaxClips {
		return fmt.Errorf("a composition supports at most %d clips, got %d", compositionMaxClips, len(doc.Video))
	}
	if len(doc.AudioLanes) > compositionMaxAudioLanes {
		return fmt.Errorf("a composition supports at most %d audio lanes, got %d", compositionMaxAudioLanes, len(doc.AudioLanes))
	}
	switch doc.Output.Fit {
	case "", compositionFitPad, compositionFitCrop, compositionFitBlur:
	default:
		return fmt.Errorf(`output fit must be "pad", "crop", or "blur", got %q`, doc.Output.Fit)
	}
	if doc.Output.Fit == "" {
		doc.Output.Fit = compositionFitPad
	}
	if doc.Output.Width < 0 || doc.Output.Height < 0 {
		return errors.New("output width and height cannot be negative")
	}
	doc.Output.Width = evenDown(doc.Output.Width)
	doc.Output.Height = evenDown(doc.Output.Height)
	if doc.Output.FPS < 0 || doc.Output.FPS > compositionMaxFPS {
		return fmt.Errorf("output fps must be between 0 (inherit the first clip's) and %g, got %g", compositionMaxFPS, doc.Output.FPS)
	}

	seenIDs := map[string]bool{}
	claimID := func(id, label string) error {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s has an empty id", label)
		}
		if seenIDs[id] {
			return fmt.Errorf("%s reuses id %q", label, id)
		}
		seenIDs[id] = true
		return nil
	}

	for i := range doc.Video {
		clip := &doc.Video[i]
		label := fmt.Sprintf("clip %d", i+1)
		if err := claimID(clip.ID, label); err != nil {
			return err
		}
		if clip.Ref.Kind != "video" {
			return fmt.Errorf("%s references %s media — the video lane holds video clips", label, compositionRefKindPhrase(clip.Ref.Kind))
		}
		if clip.Ref.DurationSeconds <= 0 {
			return fmt.Errorf("%s's source was never measured (no duration)", label)
		}
		if err := compositionNormalizeRange(&clip.InSeconds, &clip.OutSeconds, clip.Ref.DurationSeconds, label); err != nil {
			return err
		}
		clip.Audio.GainDb = compositionClampGain(clip.Audio.GainDb)
		if i < len(doc.Video)-1 {
			next := doc.Video[i+1]
			transition, err := compositionNormalizeTransition(clip.Transition, clip.OutSeconds-clip.InSeconds, next.OutSeconds-next.InSeconds, i)
			if err != nil {
				return err
			}
			clip.Transition = transition
		} else if clip.Transition != nil {
			// The final clip has no next neighbor to transition into; the
			// editor never offers one, and a stray one from a hand edit is
			// dropped rather than refused.
			clip.Transition = nil
		}
	}

	for li := range doc.AudioLanes {
		lane := &doc.AudioLanes[li]
		if err := claimID(lane.ID, fmt.Sprintf("audio lane %d", li+1)); err != nil {
			return err
		}
		lane.Name = strings.TrimSpace(lane.Name)
		lane.GainDb = compositionClampGain(lane.GainDb)
		if lane.Segments == nil {
			lane.Segments = []CompositionSegment{}
		}
		sort.SliceStable(lane.Segments, func(a, b int) bool {
			return lane.Segments[a].StartSeconds < lane.Segments[b].StartSeconds
		})
		var laneEnd float64
		for si := range lane.Segments {
			segment := &lane.Segments[si]
			label := fmt.Sprintf("audio lane %d segment %d", li+1, si+1)
			if err := claimID(segment.ID, label); err != nil {
				return err
			}
			if segment.Ref.Kind != "audio" && !(segment.Ref.Kind == "video" && segment.Ref.HasAudio) {
				if segment.Ref.Kind == "video" {
					return fmt.Errorf("%s references a video without an audio track", label)
				}
				return fmt.Errorf("%s references %s media — audio lanes hold audio (or a video's detached audio)", label, compositionRefKindPhrase(segment.Ref.Kind))
			}
			if segment.Ref.DurationSeconds <= 0 {
				return fmt.Errorf("%s's source was never measured (no duration)", label)
			}
			if err := compositionNormalizeRange(&segment.InSeconds, &segment.OutSeconds, segment.Ref.DurationSeconds, label); err != nil {
				return err
			}
			if segment.StartSeconds < 0 || !compositionFinite(segment.StartSeconds) {
				return fmt.Errorf("%s starts before the composition begins", label)
			}
			if segment.StartSeconds < laneEnd-compositionEpsilon {
				return fmt.Errorf("%s overlaps the previous segment on its lane", label)
			}
			laneEnd = segment.StartSeconds + (segment.OutSeconds - segment.InSeconds)
			segment.GainDb = compositionClampGain(segment.GainDb)
			half := (segment.OutSeconds - segment.InSeconds) / 2
			for _, fade := range []*float64{&segment.FadeInSeconds, &segment.FadeOutSeconds} {
				if !compositionFinite(*fade) || *fade < 0 {
					*fade = 0
				}
				if *fade > half {
					*fade = half
				}
			}
			if err := compositionNormalizeDucks(segment, label); err != nil {
				return err
			}
		}
	}
	return nil
}

// compositionRefKindPhrase words an unknown media kind for errors.
func compositionRefKindPhrase(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "unknown"
	}
	return kind
}

// compositionNormalizeRange clamps a source range into [0, duration] with a
// usable length — the editor drags within these rules, so a save that reaches
// them is normalized, not refused.
func compositionNormalizeRange(in, out *float64, duration float64, label string) error {
	if !compositionFinite(*in, *out) {
		return fmt.Errorf("%s has a non-finite source range", label)
	}
	if *in < 0 {
		*in = 0
	}
	if *in > duration {
		*in = duration
	}
	if *out > duration+compositionEpsilon {
		*out = duration
	}
	if *out > duration {
		*out = duration
	}
	if *out-*in < compositionMinRangeSeconds {
		return fmt.Errorf("%s's source range is too short (less than %gs)", label, compositionMinRangeSeconds)
	}
	return nil
}

// compositionNormalizeDucks sorts and bounds a segment's duck ranges. Ducks
// must not overlap — two simultaneous gains are ambiguous on the wire even
// though the renderer would resolve them by filter order.
func compositionNormalizeDucks(segment *CompositionSegment, label string) error {
	if segment.Ducks == nil {
		segment.Ducks = []CompositionDuck{}
	}
	sort.SliceStable(segment.Ducks, func(a, b int) bool {
		return segment.Ducks[a].StartSeconds < segment.Ducks[b].StartSeconds
	})
	span := segment.OutSeconds - segment.InSeconds
	var duckEnd float64
	for di := range segment.Ducks {
		duck := &segment.Ducks[di]
		duckLabel := fmt.Sprintf("%s duck %d", label, di+1)
		if !compositionFinite(duck.StartSeconds, duck.EndSeconds) {
			return fmt.Errorf("%s has a non-finite range", duckLabel)
		}
		if duck.StartSeconds < 0 {
			duck.StartSeconds = 0
		}
		if duck.EndSeconds > span {
			duck.EndSeconds = span
		}
		if duck.EndSeconds-duck.StartSeconds < compositionEpsilon {
			return fmt.Errorf("%s is too short to hear", duckLabel)
		}
		if duck.StartSeconds < duckEnd-compositionEpsilon {
			return fmt.Errorf("%s overlaps the previous duck", duckLabel)
		}
		duckEnd = duck.EndSeconds
		duck.GainDb = compositionClampGain(duck.GainDb)
	}
	return nil
}

// compositionClipLayout computes each video clip's timeline start plus the
// sequence duration — the shared math the frontend timeline renders from and
// the renderer's junction offsets derive from. A dissolve overlaps the two
// sides by its length, so the sequence is the ranges' sum minus one overlap
// per dissolve.
func compositionClipLayout(doc CompositionDoc) (starts []float64, duration float64) {
	starts = make([]float64, len(doc.Video))
	elapsed := 0.0
	for i, clip := range doc.Video {
		starts[i] = elapsed
		elapsed += clip.OutSeconds - clip.InSeconds
		if clip.Transition != nil && i < len(doc.Video)-1 {
			elapsed -= clip.Transition.Seconds
		}
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return starts, elapsed
}

// compositionTimelineDuration is compositionClipLayout's duration half.
func compositionTimelineDuration(doc CompositionDoc) float64 {
	_, duration := compositionClipLayout(doc)
	return duration
}

// compositionDocSummary words the save turn's one-line record.
func compositionDocSummary(doc CompositionDoc) string {
	lanes := len(doc.AudioLanes)
	var lanePhrase string
	switch lanes {
	case 0:
		lanePhrase = "no audio lanes"
	case 1:
		lanePhrase = "1 audio lane"
	default:
		lanePhrase = fmt.Sprintf("%d audio lanes", lanes)
	}
	dissolves := 0
	for _, clip := range doc.Video {
		if clip.Transition != nil {
			dissolves++
		}
	}
	return fmt.Sprintf("%d clips · %s · %s", len(doc.Video), lanePhrase, formatJoinSeconds(compositionTimelineDuration(doc))+"s")
}

// seedCompositionDoc builds a starting document from resolved refs, in
// reference order — the seeded-only rule: media land exactly as if the user
// had picked them from the picker in that order. Video refs become clips at
// their full range with no transitions; audio refs queue sequentially onto a
// single starter lane from the sequence start.
func seedCompositionDoc(name string, refs []CompositionMediaRef) CompositionDoc {
	doc := CompositionDoc{
		SchemaVersion: compositionSchemaVersion,
		Name:          strings.TrimSpace(name),
		Output:        CompositionOutput{Fit: compositionFitPad},
		Video:         []CompositionClip{},
		AudioLanes:    []CompositionLane{},
	}
	if doc.Name == "" {
		doc.Name = compositionDefaultName
	}
	var lane *CompositionLane
	laneEnd := 0.0
	for _, ref := range refs {
		if ref.Kind == "video" {
			doc.Video = append(doc.Video, CompositionClip{
				ID:         randomID("clp"),
				Ref:        ref,
				InSeconds:  0,
				OutSeconds: ref.DurationSeconds,
			})
			continue
		}
		if lane == nil {
			doc.AudioLanes = append(doc.AudioLanes, CompositionLane{ID: randomID("lane"), Name: "Audio 1", Segments: []CompositionSegment{}})
			lane = &doc.AudioLanes[0]
		}
		lane.Segments = append(lane.Segments, CompositionSegment{
			ID:           randomID("seg"),
			Ref:          ref,
			InSeconds:    0,
			OutSeconds:   ref.DurationSeconds,
			StartSeconds: laneEnd,
			Ducks:        []CompositionDuck{},
		})
		laneEnd += ref.DurationSeconds
	}
	return doc
}

// writeCompositionArtifact persists a document as a fresh cmp_<hex>.json
// artifact — the identity the editor reopens, the assets panel lists, and an
// export pins. The write is verified by parsing it back (the
// content-detected idiom: junk never lands under a cmp_ name).
func writeCompositionArtifact(artifactsDir string, doc CompositionDoc) (HistoryContent, error) {
	if err := validateCompositionDoc(&doc); err != nil {
		return HistoryContent{}, err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return HistoryContent{}, err
	}
	data = append(data, '\n')
	if _, err := parseCompositionDoc(data); err != nil {
		return HistoryContent{}, fmt.Errorf("the rendered composition document failed its own parse: %w", err)
	}
	artifactID := randomID("cmp")
	filename := artifactID + ".json"
	if err := os.WriteFile(filepath.Join(artifactsDir, filename), data, 0644); err != nil {
		return HistoryContent{}, err
	}
	return HistoryContent{
		Type:       "composition",
		ArtifactID: artifactID,
		Path:       filepath.ToSlash(filepath.Join("artifacts", filename)),
		MimeType:   "application/json",
	}, nil
}

// compositionMissingRefs lists the artifact ids whose files are gone on disk
// — PR-06's identify-only report. The document keeps the entries (the
// timeline stays intact); the editor flags them and the renderer refuses
// while any remain.
func compositionMissingRefs(doc CompositionDoc) []string {
	seen := map[string]bool{}
	missing := []string{}
	check := func(ref CompositionMediaRef) {
		if ref.ArtifactID == "" || seen[ref.ArtifactID] {
			return
		}
		seen[ref.ArtifactID] = true
		if _, err := os.Stat(ref.Path); err != nil {
			missing = append(missing, ref.ArtifactID)
		}
	}
	for _, clip := range doc.Video {
		check(clip.Ref)
	}
	for _, lane := range doc.AudioLanes {
		for _, segment := range lane.Segments {
			check(segment.Ref)
		}
	}
	return missing
}
