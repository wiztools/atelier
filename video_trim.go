package main

// The Video Editor's deterministic trim/cut contract. Segments name the kept
// footage on an ALREADY canonical (zero-origin, square-pixel) source; a trim
// shortens the head/tail and a mid-clip delete keeps the ranges on both sides,
// so one shape covers both. Session ownership and probing are separate
// concerns. Nothing here invokes the chat harness or a media provider.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const videoTrimMaxSegments = 50

// Adjacent segments must leave a visible gap: segments closer than one
// millisecond are a degenerate cut that would concatenate contiguous footage.
const videoTrimMinGapSeconds = 0.001

// VideoTrimSegment is one kept range, in source seconds.
type VideoTrimSegment struct {
	StartSeconds float64 `json:"startSeconds"`
	EndSeconds   float64 `json:"endSeconds"`
}

// VideoTrimParams is the trim kind's payload. Source mirrors the reframe
// payload's stale-source guard: the submitter must name the copy it marked
// the cuts against.
type VideoTrimParams struct {
	Version  int                `json:"version"`
	Source   VideoReframeSource `json:"source"`
	Segments []VideoTrimSegment `json:"segments"`
}

// validateVideoTrim returns an owned, chronologically sorted copy of the kept
// segments.
func validateVideoTrim(params VideoTrimParams) (VideoTrimParams, error) {
	if params.Version != 1 {
		return VideoTrimParams{}, errors.New("unsupported trim version")
	}
	source := params.Source
	if !videoReframeDimension(source.Width) || !videoReframeDimension(source.Height) || !videoReframeFinite(source.DurationSeconds) || source.DurationSeconds <= 0 {
		return VideoTrimParams{}, errors.New("trim requires valid source dimensions and a finite positive duration")
	}
	if len(params.Segments) == 0 || len(params.Segments) > videoTrimMaxSegments {
		return VideoTrimParams{}, fmt.Errorf("trim requires between 1 and %d kept segments", videoTrimMaxSegments)
	}
	params.Segments = append([]VideoTrimSegment(nil), params.Segments...)
	// Check finite bounds before sorting: NaN would violate the comparator.
	for _, segment := range params.Segments {
		if !videoReframeFinite(segment.StartSeconds) || !videoReframeFinite(segment.EndSeconds) {
			return VideoTrimParams{}, errors.New("trim segment bounds must be finite")
		}
	}
	sort.Slice(params.Segments, func(i, j int) bool { return params.Segments[i].StartSeconds < params.Segments[j].StartSeconds })
	for i, segment := range params.Segments {
		if segment.StartSeconds < 0 || segment.EndSeconds > source.DurationSeconds || segment.StartSeconds >= segment.EndSeconds {
			return VideoTrimParams{}, errors.New("trim segments must be non-empty ranges inside the source duration")
		}
		if i > 0 && segment.StartSeconds-params.Segments[i-1].EndSeconds < videoTrimMinGapSeconds {
			return VideoTrimParams{}, errors.New("trim segments must not overlap or touch")
		}
	}
	return params, nil
}

// videoTrimKeptSeconds reports the expected render duration.
func videoTrimKeptSeconds(params VideoTrimParams) (float64, error) {
	params, err := validateVideoTrim(params)
	if err != nil {
		return 0, err
	}
	kept := 0.0
	for _, segment := range params.Segments {
		kept += segment.EndSeconds - segment.StartSeconds
	}
	return kept, nil
}

// videoTrimSummary is the human-readable payload digest for adoption notes.
func videoTrimSummary(params VideoTrimParams) string {
	kept, err := videoTrimKeptSeconds(params)
	if err != nil {
		return "trim"
	}
	return fmt.Sprintf("%d segment(s), %.1fs kept", len(params.Segments), kept)
}

// videoTrimFFmpegArgs builds a local render that keeps exactly the validated
// segments: one trim+rebase chain per segment (audio mirrors with atrim), all
// concat'd in one pass. Every boundary re-encodes, so cuts land on the exact
// PTS rather than the nearest keyframe. Inputs are normalized session copies,
// so -noautorotate keeps the decode at the probed dimensions like the reframe
// render; no -copyts — each chain rebases its segment to zero and concat
// relays them into one continuous timeline starting at 0.
func videoTrimFFmpegArgs(params VideoTrimParams, input, output string, hasAudio bool) ([]string, error) {
	params, err := validateVideoTrim(params)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input) == "" || strings.TrimSpace(output) == "" || input == output {
		return nil, errors.New("trim requires distinct input/output paths")
	}
	videoChains := make([]string, 0, len(params.Segments))
	audioChains := make([]string, 0, len(params.Segments))
	concatInputs := make([]string, 0, len(params.Segments)*2)
	for i, segment := range params.Segments {
		start, end := videoReframeNumber(segment.StartSeconds), videoReframeNumber(segment.EndSeconds)
		videoChains = append(videoChains, fmt.Sprintf("[0:v]trim=start=%s:end=%s,setpts=PTS-STARTPTS[v%d]", start, end, i))
		if hasAudio {
			audioChains = append(audioChains, fmt.Sprintf("[0:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS[a%d]", start, end, i))
		}
		concatInputs = append(concatInputs, fmt.Sprintf("[v%d]", i))
		if hasAudio {
			concatInputs = append(concatInputs, fmt.Sprintf("[a%d]", i))
		}
	}
	graph := strings.Join(append(videoChains, audioChains...), ";") +
		";" + strings.Join(concatInputs, "") + fmt.Sprintf("concat=n=%d:v=1:a=%d[outv]", len(params.Segments), boolToInt(hasAudio))
	args := []string{"-nostdin", "-n", "-noautorotate", "-i", input, "-filter_complex", graph, "-map", "[outv]", "-fps_mode", "passthrough", "-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p"}
	if hasAudio {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	} else {
		args = append(args, "-an")
	}
	return append(args, "-movflags", "+faststart", output), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
