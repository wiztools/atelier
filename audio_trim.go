package main

// The Audio Editor's deterministic cut/trim contract. Segments name the kept
// audio on the source timeline — the same one-shape-covers-head-tail-trims-
// and-mid-deletes shape the video trim uses, minus the geometry a waveform
// never had. Session ownership and probing are separate concerns. Nothing
// here invokes the chat harness or a media provider.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const audioTrimMaxSegments = 50

// Adjacent segments must leave an audible gap: segments closer than one
// millisecond are a degenerate cut that would concatenate contiguous audio.
const audioTrimMinGapSeconds = 0.001

// AudioTrimSource is the stale-source guard: the submitter must name the
// duration it marked the cuts against.
type AudioTrimSource struct {
	DurationSeconds float64 `json:"durationSeconds"`
}

// AudioTrimSegment is one kept range, in source seconds.
type AudioTrimSegment struct {
	StartSeconds float64 `json:"startSeconds"`
	EndSeconds   float64 `json:"endSeconds"`
}

// AudioTrimParams is the audio trim kind's payload.
type AudioTrimParams struct {
	Version  int                `json:"version"`
	Source   AudioTrimSource    `json:"source"`
	Segments []AudioTrimSegment `json:"segments"`
}

// validateAudioTrim returns an owned, chronologically sorted copy of the kept
// segments.
func validateAudioTrim(params AudioTrimParams) (AudioTrimParams, error) {
	if params.Version != 1 {
		return AudioTrimParams{}, errors.New("unsupported trim version")
	}
	if !videoReframeFinite(params.Source.DurationSeconds) || params.Source.DurationSeconds <= 0 {
		return AudioTrimParams{}, errors.New("trim requires a finite positive source duration")
	}
	if len(params.Segments) == 0 || len(params.Segments) > audioTrimMaxSegments {
		return AudioTrimParams{}, fmt.Errorf("trim requires between 1 and %d kept segments", audioTrimMaxSegments)
	}
	params.Segments = append([]AudioTrimSegment(nil), params.Segments...)
	// Check finite bounds before sorting: NaN would violate the comparator.
	for _, segment := range params.Segments {
		if !videoReframeFinite(segment.StartSeconds) || !videoReframeFinite(segment.EndSeconds) {
			return AudioTrimParams{}, errors.New("trim segment bounds must be finite")
		}
	}
	sort.Slice(params.Segments, func(i, j int) bool { return params.Segments[i].StartSeconds < params.Segments[j].StartSeconds })
	for i, segment := range params.Segments {
		if segment.StartSeconds < 0 || segment.EndSeconds > params.Source.DurationSeconds || segment.StartSeconds >= segment.EndSeconds {
			return AudioTrimParams{}, errors.New("trim segments must be non-empty ranges inside the source duration")
		}
		if i > 0 && segment.StartSeconds-params.Segments[i-1].EndSeconds < audioTrimMinGapSeconds {
			return AudioTrimParams{}, errors.New("trim segments must not overlap or touch")
		}
	}
	return params, nil
}

// audioTrimKeptSeconds reports the expected render duration.
func audioTrimKeptSeconds(params AudioTrimParams) (float64, error) {
	params, err := validateAudioTrim(params)
	if err != nil {
		return 0, err
	}
	kept := 0.0
	for _, segment := range params.Segments {
		kept += segment.EndSeconds - segment.StartSeconds
	}
	return kept, nil
}

// audioTrimSummary is the human-readable payload digest for adoption notes.
func audioTrimSummary(params AudioTrimParams) string {
	kept, err := audioTrimKeptSeconds(params)
	if err != nil {
		return "trim"
	}
	return fmt.Sprintf("%d segment(s), %.1fs kept", len(params.Segments), kept)
}

// audioTrimFFmpegArgs builds a local render that keeps exactly the validated
// segments: one atrim+rebase chain per segment, all concat'd in one pass.
// Every boundary re-encodes, so cuts land on the exact sample rather than a
// packet boundary, and the output is AAC 192k in an m4a container — the same
// always-re-encode contract the video trim renders under (H.264 CRF 18).
func audioTrimFFmpegArgs(params AudioTrimParams, input, output string) ([]string, error) {
	params, err := validateAudioTrim(params)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input) == "" || strings.TrimSpace(output) == "" || input == output {
		return nil, errors.New("trim requires distinct input/output paths")
	}
	chains := make([]string, 0, len(params.Segments))
	concatInputs := make([]string, 0, len(params.Segments))
	for i, segment := range params.Segments {
		start, end := videoReframeNumber(segment.StartSeconds), videoReframeNumber(segment.EndSeconds)
		chains = append(chains, fmt.Sprintf("[0:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS[a%d]", start, end, i))
		concatInputs = append(concatInputs, fmt.Sprintf("[a%d]", i))
	}
	graph := strings.Join(chains, ";") + ";" + strings.Join(concatInputs, "") +
		fmt.Sprintf("concat=n=%d:v=0:a=1[outa]", len(params.Segments))
	return []string{"-nostdin", "-n", "-i", input, "-filter_complex", graph, "-map", "[outa]",
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", output}, nil
}
