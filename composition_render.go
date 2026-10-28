package main

// The composition renderer: one ffmpeg filter_complex built from the
// document, assembled from the join tools' vocabulary — per-clip trim +
// conform onto the project frame (the joinTransitionsFilterGraph
// normalization, transform_video's fill modes), junction chaining where a
// cut concats and a dissolve xfades at the merged-timeline offset with an
// equal-power acrossfade (or a trimmed hard cut) on the audio side, lane
// segments through atrim/adelay/volume/afade/duck-enable stages, and a final
// amix that ends at the video sequence boundary (AU-11's default). The plan
// builder is pure and table-tested on full graph strings; the op lifecycle
// mirrors the edit sessions (registry + context cancel + progress through
// runLocalFFmpegProgress) but stays out of the conversation: a PREVIEW's
// output lands in a temp preview directory served by the asset handler, and
// nothing persists until the export path (M3) adopts a saved revision.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	compositionRenderEventName   = "atelier:composition-op"
	compositionPreviewDirName    = "atelier-composition-previews"
	compositionRenderModePreview = "preview"
	compositionRenderModeExport  = "export"
	compositionAudioSampleRate   = 48000
	compositionAudioChannels     = 2
)

// compositionRenderOptions carries the per-render choices the plan builder
// needs: an optional output-timeline range (PV-03's range preview), the lane
// a solo preview isolates (AU-03 — every other lane and the clips' original
// audio drop out of the mix), audio omission (AU-11's export choice), and
// the export-time frame overrides (0 = the revision's own output settings —
// clip order, timing, and lanes are what a saved revision pins; framing is
// an export choice).
type compositionRenderOptions struct {
	StartSeconds *float64
	EndSeconds   *float64
	SoloLaneID   string
	MuteAudio    bool
	Width        int
	Height       int
	FPS          float64
	// InstalledTransitions bounds the junction vocabulary to the resolved
	// ffmpeg's real xfade names (installedXFadeTransitions); a kind outside
	// it renders as a plain dissolve under a notice. Nil skips the check —
	// the pure-builder tests, and any render whose doc has no transitions.
	InstalledTransitions []string
}

// compositionRenderPlan is everything the ffmpeg invocation needs, plus the
// decisions the editor surfaces as notices. InputPaths are the real media
// files in input-index order; ExtraInputArgs the synthetic anullsrc inputs
// that follow them (the join precedent: acrossfade errors on a silent side,
// so a clip without usable audio gets synthesized silence).
type compositionRenderPlan struct {
	FilterComplex  string
	InputPaths     []string
	ExtraInputArgs []string
	Notices        []string
	// SequenceSeconds is the video lane's merged length; OutputSeconds the
	// rendered length after any range trim (what progress divides by).
	SequenceSeconds float64
	OutputSeconds   float64
	Width, Height   int
	FPS             float64
	AudioOnly       bool
	MuteAudio       bool
	RangeStart      *float64
	RangeEnd        *float64
}

// compositionFrame resolves the project's output frame — resolveOutputFrame's
// Go twin: the explicit frame when set, else the first clip's probed display
// size and fps, else the 720p/30 fallback, even-dimensioned.
func compositionFrame(doc CompositionDoc, facts map[string]joinClipFact) (int, int, float64) {
	width, height, fps := doc.Output.Width, doc.Output.Height, doc.Output.FPS
	if width == 0 || height == 0 || fps == 0 {
		if len(doc.Video) > 0 {
			if fact, ok := facts[doc.Video[0].Ref.ArtifactID]; ok {
				if width == 0 {
					width = fact.Width
				}
				if height == 0 {
					height = fact.Height
				}
				if fps == 0 && fact.FPS > 0 {
					fps = fact.FPS
				}
			}
		}
	}
	if width <= 0 {
		width = 1280
	}
	if height <= 0 {
		height = 720
	}
	if fps <= 0 {
		fps = compositionDefaultFPS
	}
	if fps > compositionMaxFPS {
		fps = compositionMaxFPS
	}
	return evenDown(width), evenDown(height), fps
}

// compositionConformChain renders one clip's video chain: the source range,
// then the fit's conform onto the project frame, then the shared
// fps/format/SAR normalization every concat/xfade peer needs. Blur needs its
// own graph statements (fillFilterChain carries internal labels), so the
// chain returns statements rather than one comma-joined string.
func compositionConformStatements(inputIndex int, clip CompositionClip, fact joinClipFact, fit string, width, height int, fpsText string, outLabel string) []string {
	trim := fmt.Sprintf("[%d:v]trim=start=%s:end=%s,setpts=PTS-STARTPTS", inputIndex, formatJoinSeconds(clip.InSeconds), formatJoinSeconds(clip.OutSeconds))
	tail := fmt.Sprintf("setsar=1,fps=%s,format=yuv420p", fpsText)
	switch fit {
	case compositionFitCrop:
		return []string{fmt.Sprintf("%s,scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,%s[%s]",
			trim, width, height, width, height, tail, outLabel)}
	case compositionFitBlur:
		mid := fmt.Sprintf("cvt%d", inputIndex)
		return []string{
			fmt.Sprintf("%s[%s]", trim, mid),
			fmt.Sprintf("[%s]%s,%s[%s]", mid, fillFilterChain(compositionFitBlur, fact.Width, fact.Height, width, height), tail, outLabel),
		}
	default:
		return []string{fmt.Sprintf("%s,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,%s[%s]",
			trim, width, height, width, height, tail, outLabel)}
	}
}

// compositionDuckStages renders a segment's duck ranges as enable-gated
// volume stages, plus the between() terms they cover. A duck sets the
// segment's level ABSOLUTELY inside its range — sequential volume filters
// multiply, so the segment's own gain is gated to the complement of the duck
// ranges rather than stacking under them (a −12 dB duck means −12 dB, not
// −12 dB on top of the segment gain). Times read the segment's local
// timeline (post-atrim, pre-adelay), which is what the ducks are relative
// to; the lane's own gain rides above both as the mixer's bus level.
func compositionDuckStages(segment CompositionSegment) (stages []string, betweenTerms []string) {
	stages = make([]string, 0, len(segment.Ducks))
	for _, duck := range segment.Ducks {
		stages = append(stages, fmt.Sprintf("volume=volume=%sdB:enable='between(t,%s,%s)'",
			formatJoinSeconds(duck.GainDb), formatJoinSeconds(duck.StartSeconds), formatJoinSeconds(duck.EndSeconds)))
		betweenTerms = append(betweenTerms, fmt.Sprintf("between(t,%s,%s)",
			formatJoinSeconds(duck.StartSeconds), formatJoinSeconds(duck.EndSeconds)))
	}
	return stages, betweenTerms
}

// compositionClipAudioUsable reports whether a clip's original audio enters
// the mix: the probed clip must carry sound, and neither the clip's own
// mute/detach state nor a solo preview may silence it.
func compositionClipAudioUsable(clip CompositionClip, facts map[string]joinClipFact, soloLaneID string) bool {
	if clip.Audio.Muted || clip.Audio.Detached || soloLaneID != "" {
		return false
	}
	fact, ok := facts[clip.Ref.ArtifactID]
	return ok && fact.HasAudio
}

// buildCompositionRenderPlan compiles a validated document into the filter
// graph and input list. facts are the render-time probes (probe wins over the
// ref snapshot — a re-encoded file may have gained or lost its audio track).
func buildCompositionRenderPlan(doc CompositionDoc, facts map[string]joinClipFact, opts compositionRenderOptions) (compositionRenderPlan, error) {
	plan := compositionRenderPlan{
		Notices:    []string{},
		InputPaths: []string{},
	}
	if len(doc.Video) == 0 && compositionAudioSegmentCount(doc) == 0 {
		return plan, errors.New("the composition is empty — add clips or audio before rendering")
	}
	if opts.MuteAudio && len(doc.Video) == 0 {
		return plan, errors.New("an audio-only composition cannot export without its audio")
	}
	width, height, fps := compositionFrame(doc, facts)
	if opts.Width > 0 {
		width = evenDown(opts.Width)
	}
	if opts.Height > 0 {
		height = evenDown(opts.Height)
	}
	if opts.FPS > 0 {
		fps = math.Min(compositionMaxFPS, opts.FPS)
	}
	plan.Width, plan.Height, plan.FPS = width, height, fps
	fpsText := formatJoinSeconds(fps)

	// Real inputs: unique refs in first-use order (clips, then segments).
	inputIndex := map[string]int{}
	inputFor := func(ref CompositionMediaRef) int {
		key := ref.ArtifactID
		if key == "" {
			key = ref.Path
		}
		if index, ok := inputIndex[key]; ok {
			return index
		}
		index := len(plan.InputPaths)
		inputIndex[key] = index
		plan.InputPaths = append(plan.InputPaths, ref.Path)
		return index
	}
	for _, clip := range doc.Video {
		inputFor(clip.Ref)
	}
	for _, lane := range doc.AudioLanes {
		for _, segment := range lane.Segments {
			inputFor(segment.Ref)
		}
	}
	syntheticCount := 0
	nextSynthetic := func(duration float64) int {
		index := len(plan.InputPaths) + syntheticCount
		plan.ExtraInputArgs = append(plan.ExtraInputArgs,
			"-f", "lavfi", "-t", formatJoinSeconds(duration), "-i",
			fmt.Sprintf("anullsrc=channel_layout=stereo:sample_rate=%d", compositionAudioSampleRate))
		syntheticCount++
		return index
	}

	starts, sequence := compositionClipLayout(doc)
	plan.SequenceSeconds = sequence
	// The range resolves up front: it applies to each final stream's own
	// section (the video relabel rides the video section, the audio trim the
	// mix), so both need it before either is emitted.
	plan.AudioOnly = len(doc.Video) == 0
	plan.MuteAudio = opts.MuteAudio
	total := sequence
	if plan.AudioOnly {
		total = compositionAudioExtentSeconds(doc)
	}
	rangeStart, rangeEnd := normalizeCompositionRange(opts.StartSeconds, opts.EndSeconds, total)
	plan.RangeStart, plan.RangeEnd = rangeStart, rangeEnd
	plan.OutputSeconds = total
	if rangeStart != nil && rangeEnd != nil {
		plan.OutputSeconds = *rangeEnd - *rangeStart
	}

	statements := []string{}

	// ---- Video lane ----
	videoLabel := ""
	if len(doc.Video) > 0 {
		for i, clip := range doc.Video {
			fact := facts[clip.Ref.ArtifactID]
			statements = append(statements, compositionConformStatements(inputFor(clip.Ref), clip, fact, doc.Output.Fit, width, height, fpsText, fmt.Sprintf("v%d", i))...)
		}
		videoLabel = "[v0]"
		unsupportedXfades := map[string]bool{}
		for j := 0; j+1 < len(doc.Video); j++ {
			next := fmt.Sprintf("[vx%d]", j)
			transition := doc.Video[j].Transition
			if transition != nil {
				// The overlap starts where the incoming clip lands on the
				// merged timeline — compositionClipLayout's starts hold it.
				// The kind names the real xfade ("dissolve" is the composer's
				// word for a plain cross-fade); an install lacking the name
				// falls back to that plain dissolve under a notice.
				xfadeName := compositionXfadeName(transition.Kind)
				if opts.InstalledTransitions != nil {
					known := false
					for _, installed := range opts.InstalledTransitions {
						if installed == xfadeName {
							known = true
							break
						}
					}
					if !known {
						xfadeName = "fade"
						if !unsupportedXfades[transition.Kind] {
							unsupportedXfades[transition.Kind] = true
							plan.Notices = append(plan.Notices, fmt.Sprintf("this ffmpeg lacks the %q transition — that junction uses a plain dissolve instead", transition.Kind))
						}
					}
				}
				statements = append(statements, fmt.Sprintf("%s[v%d]xfade=transition=%s:duration=%s:offset=%s%s",
					videoLabel, j+1, xfadeName, formatJoinSeconds(transition.Seconds), formatJoinSeconds(starts[j+1]), next))
			} else {
				statements = append(statements, fmt.Sprintf("%s[v%d]concat=n=2:v=1:a=0%s", videoLabel, j+1, next))
			}
			videoLabel = next
		}
		// Land the video's final label here — trim to the range or relabel —
		// so the graph reads video-first like every other builder here.
		videoOut := "[vout]"
		if rangeStart != nil && rangeEnd != nil {
			statements = append(statements, fmt.Sprintf("%strim=start=%s:end=%s,setpts=PTS-STARTPTS%s",
				videoLabel, formatJoinSeconds(*rangeStart), formatJoinSeconds(*rangeEnd), videoOut))
		} else if videoLabel != videoOut {
			// null, not anull — anull is the AUDIO pass-through and refuses a
			// video pad ("Media type mismatch … Error linking filters").
			statements = append(statements, fmt.Sprintf("%snull%s", videoLabel, videoOut))
		}
	}

	// ---- Original audio, junction-chained in step with the video ----
	// (skipped entirely when the export omits audio — AU-11's choice)
	audioLabel := ""
	if len(doc.Video) > 0 && !opts.MuteAudio {
		for i, clip := range doc.Video {
			duration := clip.OutSeconds - clip.InSeconds
			label := fmt.Sprintf("[ac%d]", i)
			if compositionClipAudioUsable(clip, facts, opts.SoloLaneID) {
				gain := ""
				if clip.Audio.GainDb != 0 {
					gain = fmt.Sprintf(",volume=%sdB", formatJoinSeconds(clip.Audio.GainDb))
				}
				statements = append(statements, fmt.Sprintf("[%d:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aformat=sample_rates=%d:channel_layouts=stereo%s%s",
					inputFor(clip.Ref), formatJoinSeconds(clip.InSeconds), formatJoinSeconds(clip.OutSeconds), compositionAudioSampleRate, gain, label))
			} else {
				statements = append(statements, fmt.Sprintf("[%d:a]aformat=sample_rates=%d:channel_layouts=stereo%s",
					nextSynthetic(duration), compositionAudioSampleRate, label))
			}
		}
		audioLabel = "[ac0]"
		for j := 0; j+1 < len(doc.Video); j++ {
			next := fmt.Sprintf("[ax%d]", j)
			transition := doc.Video[j].Transition
			if transition != nil && transition.AudioCrossfade && compositionClipAudioUsable(doc.Video[j], facts, opts.SoloLaneID) && compositionClipAudioUsable(doc.Video[j+1], facts, opts.SoloLaneID) {
				// Equal power, the join tools' junction curve.
				statements = append(statements, fmt.Sprintf("%s%s%s%s", audioLabel, fmt.Sprintf("[ac%d]", j+1), acrossfadeFilter(formatJoinSeconds(transition.Seconds)), next))
			} else if transition != nil {
				// A dissolve with cut (or synthesized) audio shortens the
				// merged audio by the overlap too — trim the running chain
				// to the incoming clip's start, then concat.
				trimmed := fmt.Sprintf("[at%d]", j)
				statements = append(statements, fmt.Sprintf("%satrim=start=0:end=%s,asetpts=PTS-STARTPTS%s", audioLabel, formatJoinSeconds(starts[j+1]), trimmed))
				statements = append(statements, fmt.Sprintf("%s%sconcat=n=2:v=0:a=1%s", trimmed, fmt.Sprintf("[ac%d]", j+1), next))
			} else {
				statements = append(statements, fmt.Sprintf("%s%sconcat=n=2:v=0:a=1%s", audioLabel, fmt.Sprintf("[ac%d]", j+1), next))
			}
			audioLabel = next
		}
	}

	// ---- Additional audio lanes ----
	mixLabels := []string{}
	if audioLabel != "" {
		mixLabels = append(mixLabels, audioLabel)
	}
	for _, lane := range doc.AudioLanes {
		if opts.MuteAudio || lane.Muted || (opts.SoloLaneID != "" && lane.ID != opts.SoloLaneID) {
			continue
		}
		for _, segment := range lane.Segments {
			span := segment.OutSeconds - segment.InSeconds
			stages := []string{
				fmt.Sprintf("[%d:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS", inputFor(segment.Ref), formatJoinSeconds(segment.InSeconds), formatJoinSeconds(segment.OutSeconds)),
				fmt.Sprintf("aformat=sample_rates=%d:channel_layouts=stereo", compositionAudioSampleRate),
			}
			if lane.GainDb != 0 {
				stages = append(stages, fmt.Sprintf("volume=%sdB", formatJoinSeconds(lane.GainDb)))
			}
			duckStages, betweenTerms := compositionDuckStages(segment)
			if segment.GainDb != 0 {
				if len(betweenTerms) > 0 {
					// The base gain holds everywhere its ducks do not —
					// absolute levels, not stacked ones.
					stages = append(stages, fmt.Sprintf("volume=volume=%sdB:enable='not(%s)'",
						formatJoinSeconds(segment.GainDb), strings.Join(betweenTerms, "+")))
				} else {
					stages = append(stages, fmt.Sprintf("volume=%sdB", formatJoinSeconds(segment.GainDb)))
				}
			}
			stages = append(stages, duckStages...)
			if segment.FadeInSeconds > 0 {
				stages = append(stages, fmt.Sprintf("afade=t=in:st=0:d=%s", formatJoinSeconds(segment.FadeInSeconds)))
			}
			if segment.FadeOutSeconds > 0 {
				stages = append(stages, fmt.Sprintf("afade=t=out:st=%s:d=%s", formatJoinSeconds(span-segment.FadeOutSeconds), formatJoinSeconds(segment.FadeOutSeconds)))
			}
			delayMS := int64(math.Round(segment.StartSeconds * 1000))
			if delayMS > 0 {
				stages = append(stages, fmt.Sprintf("adelay=%d|%d", delayMS, delayMS))
			}
			label := fmt.Sprintf("[s%d]", len(mixLabels))
			statements = append(statements, strings.Join(stages, ",")+label)
			mixLabels = append(mixLabels, label)
		}
	}

	// ---- Mix ----
	if !opts.MuteAudio {
		if len(mixLabels) == 0 {
			// Only reachable for an audio-only document whose every lane is
			// muted under a solo that matches nothing — refuse rather than
			// emit an empty graph.
			return plan, errors.New("nothing audible to render — every lane is muted")
		}
		mixLabel := mixLabels[0]
		if len(mixLabels) > 1 {
			durationMode := "first"
			if plan.AudioOnly {
				durationMode = "longest"
			}
			mixLabel = "[mix]"
			statements = append(statements, fmt.Sprintf("%samix=inputs=%d:duration=%s:normalize=0%s",
				strings.Join(mixLabels, ""), len(mixLabels), durationMode, mixLabel))
		}
		audioOut := "[aout]"
		if rangeStart != nil && rangeEnd != nil {
			statements = append(statements, fmt.Sprintf("%satrim=start=%s:end=%s,asetpts=PTS-STARTPTS%s",
				mixLabel, formatJoinSeconds(*rangeStart), formatJoinSeconds(*rangeEnd), audioOut))
		} else if mixLabel != audioOut {
			statements = append(statements, fmt.Sprintf("%sanull%s", mixLabel, audioOut))
		}
	}

	if syntheticCount > 0 {
		plural := "s"
		possessive := "their"
		if syntheticCount == 1 {
			plural = ""
			possessive = "its"
		}
		plan.Notices = append(plan.Notices, fmt.Sprintf("%d clip%s carried no usable audio — silence holds %s place in the mix",
			syntheticCount, plural, possessive))
	}
	if opts.SoloLaneID != "" {
		plan.Notices = append(plan.Notices, "solo preview — only the soloed lane is audible")
	}
	plan.FilterComplex = strings.Join(statements, ";")
	return plan, nil
}

// compositionAudioSegmentCount counts every lane segment.
func compositionAudioSegmentCount(doc CompositionDoc) int {
	count := 0
	for _, lane := range doc.AudioLanes {
		count += len(lane.Segments)
	}
	return count
}

// compositionAudioExtentSeconds is compositionAudioExtent's Go twin — where
// the last audio ends, the audio-only render's length.
func compositionAudioExtentSeconds(doc CompositionDoc) float64 {
	extent := 0.0
	for _, lane := range doc.AudioLanes {
		for _, segment := range lane.Segments {
			end := segment.StartSeconds + (segment.OutSeconds - segment.InSeconds)
			if end > extent {
				extent = end
			}
		}
	}
	return extent
}

// normalizeCompositionRange clamps a requested range into the timeline: a
// start without an end runs to the end; an end without a start runs from the
// beginning; anything degenerate renders the whole timeline. A nil pair
// means full render.
func normalizeCompositionRange(start, end *float64, total float64) (*float64, *float64) {
	if start == nil && end == nil {
		return nil, nil
	}
	from := 0.0
	if start != nil && *start > 0 {
		from = *start
	}
	to := total
	if end != nil && *end < to {
		to = *end
	}
	if from >= to-compositionMinRangeSeconds {
		return nil, nil
	}
	return &from, &to
}

// compositionEncodeArgs renders the output block. A preview trades quality
// for latency (ultrafast, CRF 23, 128k audio); an export takes the house
// encode with the requested CRF (0 = the house 18), and an export that
// omits audio replaces the audio block with -an (AU-11).
func compositionEncodeArgs(preview bool, crf int, audioOnly, includeAudio bool) []string {
	if audioOnly {
		bitrate := "192k"
		if preview {
			bitrate = "128k"
		}
		return []string{"-c:a", "aac", "-b:a", bitrate}
	}
	if crf <= 0 || crf > 34 {
		crf = 18
	}
	videoBlock := func(preset string, quality int) []string {
		return []string{"-c:v", "libx264", "-preset", preset, "-crf", strconv.Itoa(quality), "-pix_fmt", "yuv420p"}
	}
	if preview {
		return append(videoBlock("ultrafast", 23), "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart")
	}
	args := videoBlock("veryfast", crf)
	if !includeAudio {
		return append(args, "-an")
	}
	return append(args, "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart")
}

// compositionFFmpegArgs assembles the invocation: real inputs, synthetic
// inputs, the graph, the maps, the encode block, and the output path.
func compositionFFmpegArgs(plan compositionRenderPlan, encodeArgs []string, output string) []string {
	args := []string{"-y"}
	for _, path := range plan.InputPaths {
		args = append(args, "-i", path)
	}
	args = append(args, plan.ExtraInputArgs...)
	args = append(args, "-filter_complex", plan.FilterComplex)
	switch {
	case plan.AudioOnly:
		args = append(args, "-map", "[aout]")
	case plan.MuteAudio:
		args = append(args, "-map", "[vout]")
	default:
		args = append(args, "-map", "[vout]", "-map", "[aout]")
	}
	args = append(args, encodeArgs...)
	args = append(args, output)
	return args
}

// ---- The render-op lifecycle (the edit-op shape, conversation-free) ----

// CompositionRenderRequest names what to render: a SAVED revision by
// artifact id, or the working document itself (the preview path — no forced
// save before looking). A preview lands in the temp preview directory; an
// EXPORT always pins a saved revision (EX-04) and adopts its result into the
// conversation as a new artifact, so it refuses a working document and a
// streaming conversation up front. The optional fields are export choices:
// audio omission (AU-11), frame overrides (0 = the revision's own), and CRF.
type CompositionRenderRequest struct {
	ConversationID string          `json:"conversationId"`
	ArtifactID     string          `json:"artifactId,omitempty"`
	Doc            *CompositionDoc `json:"doc,omitempty"`
	Mode           string          `json:"mode"`
	StartSeconds   *float64        `json:"startSeconds,omitempty"`
	EndSeconds     *float64        `json:"endSeconds,omitempty"`
	SoloLaneID     string          `json:"soloLaneId,omitempty"`
	IncludeAudio   *bool           `json:"includeAudio,omitempty"`
	Width          int             `json:"width,omitempty"`
	Height         int             `json:"height,omitempty"`
	FPS            float64         `json:"fps,omitempty"`
	CRF            int             `json:"crf,omitempty"`
}

// CompositionRenderOperation is the live op the editor tracks; every
// transition rides the atelier:composition-op event.
type CompositionRenderOperation struct {
	ID        string   `json:"id"`
	Mode      string   `json:"mode"`
	Status    string   `json:"status"`
	Progress  float64  `json:"progress"`
	Error     string   `json:"error,omitempty"`
	Notices   []string `json:"notices"`
	ResultURL string   `json:"resultUrl,omitempty"`
	// ResultPosterURL rides the _poster.jpg convention the assets panel and
	// players already mirror — WKWebView paints a black rectangle until
	// playback, so the preview needs a still.
	ResultPosterURL string  `json:"resultPosterUrl,omitempty"`
	OutputSeconds   float64 `json:"outputSeconds"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	FPS             float64 `json:"fps,omitempty"`
	AudioOnly       bool    `json:"audioOnly"`
	IncludeAudio    bool    `json:"includeAudio"`
	// ExportArtifactID names the vid_/aud_ artifact an export adopted into
	// the conversation (previews leave it empty — their result is ephemeral).
	ExportArtifactID string   `json:"exportArtifactId,omitempty"`
	RangeStart       *float64 `json:"rangeStartSeconds,omitempty"`
	RangeEnd         *float64 `json:"rangeEndSeconds,omitempty"`
	SoloLaneID       string   `json:"soloLaneId,omitempty"`
	StartedAt        string   `json:"startedAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

type compositionRenderRun struct {
	conversationID string
	cancel         context.CancelFunc
	// op is the live operation snapshot — tests poll it, and the terminal
	// transition moves it into the history map before the run deregisters.
	op CompositionRenderOperation
	// The export choices captured at submit: the pinned revision, audio
	// omission, and the frame/CRF overrides (0 = inherit).
	artifactID    string
	muteAudio     bool
	width, height int
	fps           float64
	crf           int
}

// compositionPreviewDir is where preview outputs live, served through the
// asset handler by absolute path. Swept at launch; nothing here is data.
func compositionPreviewDir() string {
	return filepath.Join(os.TempDir(), compositionPreviewDirName)
}

// cleanupCompositionPreviews removes preview outputs older than a day — the
// edit staging sweeper's twin.
func cleanupCompositionPreviews() {
	entries, err := os.ReadDir(compositionPreviewDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(compositionPreviewDir(), entry.Name()))
		}
	}
}

func (a *App) emitCompositionRender(op CompositionRenderOperation) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, compositionRenderEventName, op)
}

// SubmitCompositionRender validates the request, registers the op, and runs
// it on a goroutine — the returned operation is the queued snapshot; later
// states arrive as atelier:composition-op events. One render per
// conversation at a time; a preview never writes to the conversation, so no
// streaming guard applies (the export path, which adopts its result, will
// carry one).
func (a *App) SubmitCompositionRender(req CompositionRenderRequest) (CompositionRenderOperation, error) {
	config, err := loadReadyConfig()
	if err != nil {
		return CompositionRenderOperation{}, err
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = compositionRenderModePreview
	}
	if mode != compositionRenderModePreview && mode != compositionRenderModeExport {
		return CompositionRenderOperation{}, fmt.Errorf(`render mode must be "preview" or "export", got %q`, mode)
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return CompositionRenderOperation{}, errors.New("rendering a composition requires its conversation id")
	}
	// An export pins a saved revision (EX-04) and adopts its result into the
	// conversation — both write-facing, so a working document and a
	// streaming conversation are refused up front.
	if mode == compositionRenderModeExport {
		if strings.TrimSpace(req.ArtifactID) == "" {
			return CompositionRenderOperation{}, errors.New("an export renders a saved revision — save the composition first")
		}
		if a.conversationStreaming(conversationID) {
			return CompositionRenderOperation{}, errors.New("the conversation is still streaming — wait for the turn to finish before exporting")
		}
	}
	doc, err := compositionDocForRender(config, conversationID, req.ArtifactID, req.Doc)
	if err != nil {
		return CompositionRenderOperation{}, err
	}
	includeAudio := true
	if req.IncludeAudio != nil {
		includeAudio = *req.IncludeAudio
	}
	muteAudio := mode == compositionRenderModeExport && !includeAudio

	nowText := time.Now().Format(time.RFC3339)
	op := CompositionRenderOperation{
		ID:           randomID("crend"),
		Mode:         mode,
		Status:       "queued",
		Notices:      []string{},
		StartedAt:    nowText,
		UpdatedAt:    nowText,
		RangeStart:   req.StartSeconds,
		RangeEnd:     req.EndSeconds,
		SoloLaneID:   strings.TrimSpace(req.SoloLaneID),
		IncludeAudio: includeAudio,
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &compositionRenderRun{conversationID: conversationID, cancel: cancel, op: op,
		artifactID: strings.TrimSpace(req.ArtifactID), muteAudio: muteAudio, width: req.Width, height: req.Height, fps: req.FPS, crf: req.CRF}
	a.compositionRendersMu.Lock()
	for _, existing := range a.compositionRenders {
		if existing.conversationID == conversationID {
			a.compositionRendersMu.Unlock()
			cancel()
			return CompositionRenderOperation{}, errors.New("a render is already running for this composition — cancel it first")
		}
	}
	a.compositionRenders[op.ID] = run
	a.compositionRendersMu.Unlock()
	a.emitCompositionRender(op)
	go a.executeCompositionRender(ctx, config, run, doc)
	return op, nil
}

// CancelCompositionRender stops a running render; the executor records the
// cancelled state and emits it.
func (a *App) CancelCompositionRender(operationID string) error {
	a.compositionRendersMu.Lock()
	run, ok := a.compositionRenders[strings.TrimSpace(operationID)]
	a.compositionRendersMu.Unlock()
	if !ok {
		return fmt.Errorf("no render operation with id %q", operationID)
	}
	run.cancel()
	return nil
}

// compositionDocForRender resolves the document a render targets: a saved
// revision (the export discipline — the artifact is the immutable input), or
// the caller's working draft for a preview.
func compositionDocForRender(config AppConfig, conversationID, artifactID string, doc *CompositionDoc) (CompositionDoc, error) {
	if id := strings.TrimSpace(artifactID); id != "" {
		info, err := resolveCompositionRevision(config.Storage, conversationID, id)
		if err != nil {
			return CompositionDoc{}, err
		}
		return info.Doc, nil
	}
	if doc == nil {
		return CompositionDoc{}, errors.New("a render needs a saved revision id or the working document")
	}
	normalized := *doc
	if err := validateCompositionDoc(&normalized); err != nil {
		return CompositionDoc{}, err
	}
	if len(normalized.Video) == 0 && compositionAudioSegmentCount(normalized) == 0 {
		return CompositionDoc{}, errors.New("the composition is empty — add clips or audio before rendering")
	}
	return normalized, nil
}

// resolveCompositionRevision is ResolveCompositionEditSource's pure half —
// the render path reuses the same read/parse/validate ladder.
func resolveCompositionRevision(storage ConfigStorage, conversationID, artifactID string) (CompositionSourceInfo, error) {
	content, title, err := resolveCompositionContent(storage, conversationID, artifactID)
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	path, err := contentArtifactPath(storage, conversationID, *content)
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CompositionSourceInfo{}, fmt.Errorf("reading the composition artifact failed: %w", err)
	}
	doc, err := parseCompositionDoc(data)
	if err != nil {
		return CompositionSourceInfo{}, err
	}
	if err := validateCompositionDoc(&doc); err != nil {
		return CompositionSourceInfo{}, fmt.Errorf("the saved composition is not valid: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	return CompositionSourceInfo{
		ConversationID:    conversationID,
		ConversationTitle: title,
		ArtifactID:        content.ArtifactID,
		URL:               artifactPrefix + absPath,
		Doc:               doc,
		MissingRefs:       compositionMissingRefs(doc),
		Warnings:          []string{},
	}, nil
}

// executeCompositionRender runs the op: probe, plan, encode, verify,
// promote into the preview directory, poster, emit. The op value lives on
// the run (mutex-guarded) so tests can poll it; the terminal transition
// files it into the history map before the run deregisters.
func (a *App) executeCompositionRender(ctx context.Context, config AppConfig, run *compositionRenderRun, doc CompositionDoc) {
	op := run.op
	defer func() {
		a.compositionRendersMu.Lock()
		delete(a.compositionRenders, op.ID)
		a.compositionRenderHistory[op.ID] = op
		// Cap the history — it exists for tests and a future lister, not as
		// a ledger; previews are ephemeral by design.
		if len(a.compositionRenderHistory) > 32 {
			for id := range a.compositionRenderHistory {
				delete(a.compositionRenderHistory, id)
				if len(a.compositionRenderHistory) <= 32 {
					break
				}
			}
		}
		a.compositionRendersMu.Unlock()
	}()
	update := func(mutate func(*CompositionRenderOperation)) {
		mutate(&op)
		op.UpdatedAt = time.Now().Format(time.RFC3339)
		a.compositionRendersMu.Lock()
		if live, ok := a.compositionRenders[op.ID]; ok {
			live.op = op
		}
		a.compositionRendersMu.Unlock()
		a.emitCompositionRender(op)
	}
	update(func(o *CompositionRenderOperation) { o.Status = "running" })

	staging, err := os.MkdirTemp("", "atelier-composition-render-")
	if err != nil {
		update(func(o *CompositionRenderOperation) { o.Status = "failed"; o.Error = err.Error() })
		return
	}
	defer os.RemoveAll(staging)

	// Probe the video refs (existence for every ref — PR-06's render-time
	// refusal — and facts for the video lane).
	if missing := compositionMissingRefs(doc); len(missing) > 0 {
		plural := "s"
		pronoun := "them"
		if len(missing) == 1 {
			plural = ""
			pronoun = "it"
		}
		update(func(o *CompositionRenderOperation) {
			o.Status = "failed"
			o.Error = fmt.Sprintf("missing source file%s: %s — relink or remove %s from the composition before rendering",
				plural, strings.Join(missing, ", "), pronoun)
		})
		return
	}
	facts := map[string]joinClipFact{}
	for _, clip := range doc.Video {
		if _, done := facts[clip.Ref.ArtifactID]; done {
			continue
		}
		fact, ok := joinClipFactFor(ctx, config, clip.Ref.Path, artifactPrefix+clip.Ref.Path)
		if !ok {
			update(func(o *CompositionRenderOperation) {
				o.Status = "failed"
				o.Error = fmt.Sprintf("could not measure %s (duration and frame size) — install ffmpeg (ffprobe) to render this composition", clip.Ref.ArtifactID)
			})
			return
		}
		facts[clip.Ref.ArtifactID] = fact
	}

	// The junction vocabulary is bounded by the resolved ffmpeg only when the
	// doc uses one — no capability probe for transition-free compositions.
	var installed []string
	for _, clip := range doc.Video {
		if clip.Transition != nil {
			if names, _, _, ok := installedXFadeTransitions(ctx, config); ok {
				installed = names
			}
			break
		}
	}

	plan, err := buildCompositionRenderPlan(doc, facts, compositionRenderOptions{
		StartSeconds:         op.RangeStart,
		EndSeconds:           op.RangeEnd,
		SoloLaneID:           op.SoloLaneID,
		MuteAudio:            run.muteAudio,
		Width:                run.width,
		Height:               run.height,
		FPS:                  run.fps,
		InstalledTransitions: installed,
	})
	if err != nil {
		update(func(o *CompositionRenderOperation) { o.Status = "failed"; o.Error = err.Error() })
		return
	}
	update(func(o *CompositionRenderOperation) {
		o.Notices = plan.Notices
		o.OutputSeconds = plan.OutputSeconds
		o.Width, o.Height, o.FPS = plan.Width, plan.Height, plan.FPS
		o.AudioOnly = plan.AudioOnly
		o.RangeStart, o.RangeEnd = plan.RangeStart, plan.RangeEnd
	})

	extension := ".mp4"
	if plan.AudioOnly {
		extension = ".m4a"
	}
	stagedOutput := filepath.Join(staging, "output"+extension)
	encodeArgs := compositionEncodeArgs(op.Mode == compositionRenderModePreview, run.crf, plan.AudioOnly, !plan.MuteAudio)
	args := compositionFFmpegArgs(plan, encodeArgs, stagedOutput)
	err = runLocalFFmpegProgress(ctx, config, plan.OutputSeconds, args, func(progress float64) {
		update(func(o *CompositionRenderOperation) { o.Progress = progress })
	})
	if err != nil {
		status, message := "failed", err.Error()
		if ctx.Err() != nil {
			status = "cancelled"
			message = ""
		}
		update(func(o *CompositionRenderOperation) { o.Status = status; o.Error = message })
		return
	}
	probeKind := "video"
	if plan.AudioOnly {
		probeKind = "audio"
	}
	seconds, ok := verifyCompositionOutput(ctx, config, stagedOutput, probeKind)
	if !ok {
		update(func(o *CompositionRenderOperation) {
			o.Status = "failed"
			o.Error = "the render produced no playable output — try again, or report the composition if it repeats"
		})
		return
	}

	// An export adopts its result into the conversation as a new artifact
	// plus a linked turn (EX-07); a preview lands in the swept temp
	// directory. The AU-10-lite clipping check reads the export's own mix.
	if op.Mode == compositionRenderModeExport {
		if a.conversationStreaming(run.conversationID) {
			update(func(o *CompositionRenderOperation) {
				o.Status = "failed"
				o.Error = "the conversation started streaming while the export rendered — retry once the turn finishes (nothing was written)"
			})
			return
		}
		notices := plan.Notices
		if !plan.MuteAudio {
			if clipping := compositionClippingNotice(ctx, config, stagedOutput); clipping != "" {
				notices = append(notices, clipping)
			}
		}
		artifactID, resultURL, adoptErr := adoptCompositionExport(config, run.conversationID, doc, run.artifactID, stagedOutput, extension, op.ID)
		if adoptErr != nil {
			update(func(o *CompositionRenderOperation) { o.Status = "failed"; o.Error = adoptErr.Error() })
			return
		}
		update(func(o *CompositionRenderOperation) {
			o.Status = "completed"
			o.Progress = 1
			o.Notices = notices
			o.ExportArtifactID = artifactID
			o.ResultURL = resultURL
			o.OutputSeconds = seconds
		})
		return
	}

	if err := os.MkdirAll(compositionPreviewDir(), 0755); err != nil {
		update(func(o *CompositionRenderOperation) { o.Status = "failed"; o.Error = err.Error() })
		return
	}
	destination := filepath.Join(compositionPreviewDir(), op.ID+extension)
	if err := os.Rename(stagedOutput, destination); err != nil {
		update(func(o *CompositionRenderOperation) { o.Status = "failed"; o.Error = err.Error() })
		return
	}
	resultURL := artifactPrefix + destination
	posterURL := ""
	if !plan.AudioOnly {
		if generateVideoPoster(config, destination) {
			posterURL = artifactPrefix + videoPosterPath(destination)
		}
	}
	update(func(o *CompositionRenderOperation) {
		o.Status = "completed"
		o.Progress = 1
		o.ResultURL = resultURL
		o.ResultPosterURL = posterURL
		o.OutputSeconds = seconds
	})
}

// adoptCompositionExport writes the rendered file into the conversation as a
// fresh vid_/aud_ artifact plus an assistant turn whose ProviderResponse
// links the export to its pinned revision — the appendEditAdoptionTurn
// shape (deliberately not the media-ledger helpers: this copy generated
// nothing). Returns the artifact id and its hydrated URL.
func adoptCompositionExport(config AppConfig, conversationID string, doc CompositionDoc, revisionArtifactID, stagedOutput, extension, operationID string) (string, string, error) {
	store := newHistoryStore(config.Storage)
	loaded, err := store.loadForAppend(conversationID, "chat", "a chat", config.Tools.Filesystem.Root)
	if err != nil {
		return "", "", err
	}
	tempCopy, err := copyEditResultToTemp(stagedOutput, extension)
	if err != nil {
		return "", "", err
	}
	defer os.Remove(tempCopy)
	var contents []HistoryContent
	var urls []string
	if extension == ".m4a" {
		contents, urls, err = writeChatAudioArtifacts(config, loaded.ArtifactsDir, []ToolAudioFile{{TempPath: tempCopy, MimeType: "audio/mp4"}})
	} else {
		contents, urls, err = writeChatVideoArtifacts(config, loaded.ArtifactsDir, []ToolVideoFile{{TempPath: tempCopy, MimeType: "video/mp4"}})
	}
	if err != nil || len(contents) == 0 || len(urls) == 0 {
		if err == nil {
			err = errors.New("the export could not be copied into the conversation")
		}
		return "", "", err
	}
	nowText := time.Now().Format(time.RFC3339)
	kind := "video"
	if extension == ".m4a" {
		kind = "audio"
	}
	summary := fmt.Sprintf("Exported composition “%s” · %s · %s", doc.Name, compositionDocSummary(doc), kind)
	turn := HistoryTurn{
		SchemaVersion:  1,
		ID:             fmt.Sprintf("turn_%06d", loaded.NextTurnNumber),
		ConversationID: conversationID,
		CreatedAt:      nowText,
		Kind:           "chat",
		Role:           "assistant",
		Content:        append([]HistoryContent{{Type: "text", Text: summary}}, contents...),
		ProviderResponse: map[string]any{
			"compositionExport": map[string]any{
				"artifactId":         contents[0].ArtifactID,
				"revisionArtifactId": revisionArtifactID,
				"operationId":        operationID,
				"kind":               kind,
				"backend":            "ffmpeg",
			},
		},
	}
	loaded.Conversation.UpdatedAt = nowText
	loaded.Conversation.Stats.TurnCount++
	loaded.Conversation.Stats.ArtifactCount += len(contents)
	if err := store.writeConversation(loaded.Path, loaded.Conversation); err != nil {
		return "", "", err
	}
	if err := store.writeTurn(loaded.TurnsDir, turn); err != nil {
		return "", "", err
	}
	return contents[0].ArtifactID, urls[0], nil
}

// compositionClippingNotice is AU-10's export-time check: a volumedetect
// pass over the rendered file, reported when the peak sits at the ceiling —
// the mix is clipping and gains should come down. Fail-soft: no ffmpeg or
// an unparsable report returns no notice.
func compositionClippingNotice(ctx context.Context, config AppConfig, path string) string {
	resolved, ok := resolveLocalFFmpegBinary(config)
	if !ok {
		return ""
	}
	output, _, err := runLocalMediaCLI(ctx, resolved, "ffmpeg", localFFmpegTimeout, []string{"-i", path, "-af", "volumedetect", "-f", "null", "-"})
	if err != nil {
		return ""
	}
	match := regexp.MustCompile(`max_volume:\s*(-?[0-9.]+)\s*dB`).FindStringSubmatch(string(output))
	if match == nil {
		return ""
	}
	peak, err := strconv.ParseFloat(match[1], 64)
	if err != nil || peak <= -0.5 {
		return ""
	}
	return fmt.Sprintf("the mix clips (peak %s dB) — lower the gains and re-export", match[1])
}

// verifyCompositionOutput checks the rendered file is playable, using the
// probe ladder: ffprobe when it resolves, else the pure-Go container sniffs —
// a machine without ffprobe can still render (the facts ladder already
// sniffs its inputs), so verification must not be the one hard dependency.
func verifyCompositionOutput(ctx context.Context, config AppConfig, path, kind string) (float64, bool) {
	if _, ok := resolveLocalFFprobeBinary(config); ok {
		if probe, err := probeStagedMedia(ctx, config, path, kind); err == nil && probe.Duration > 0 {
			return probe.Duration, true
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if kind == "audio" {
		if seconds, ok := probeAudioDuration(data); ok && seconds > 0 {
			return seconds, true
		}
		return 0, false
	}
	seconds, durationOK := mp4DurationSeconds(data)
	_, _, sizeOK := mp4VideoDimensions(data)
	return seconds, durationOK && sizeOK
}
