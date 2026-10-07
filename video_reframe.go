package main

// The Video Editor's deterministic framing contract. Coordinates refer to an
// upright, square-pixel source; probing/normalization and session ownership are
// separate concerns. Nothing here invokes the chat harness or a media provider.

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const videoReframeMaxMarkers = 100
const videoReframeMaxDimension = 32768

type VideoReframeSource struct {
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	DurationSeconds float64 `json:"durationSeconds"`
}

type VideoReframeRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type VideoReframeMarker struct {
	VideoReframeRect
	TimeSeconds         float64 `json:"timeSeconds"`
	InterpolationToNext string  `json:"interpolationToNext"`
}

type VideoReframeOutput struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type VideoReframeParams struct {
	Version     int                  `json:"version"`
	Source      VideoReframeSource   `json:"source"`
	AspectRatio string               `json:"aspectRatio"`
	Output      VideoReframeOutput   `json:"output"`
	Markers     []VideoReframeMarker `json:"markers"`
}

func videoReframeRatio(aspect string) (int, int, error) {
	switch aspect {
	case "9:16":
		return 9, 16, nil
	case "1:1":
		return 1, 1, nil
	case "16:9":
		return 16, 9, nil
	default:
		return 0, 0, errors.New("reframe aspect ratio must be 9:16, 1:1, or 16:9")
	}
}

func videoReframeFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func videoReframeDimension(value int) bool {
	return value >= 2 && value <= videoReframeMaxDimension
}

// fitVideoReframeCrop chooses the largest exact-ratio crop with even dimensions.
// Rounding the width and height independently would change the aspect ratio.
func fitVideoReframeCrop(width, height int, aspect string) (VideoReframeRect, error) {
	if !videoReframeDimension(width) || !videoReframeDimension(height) {
		return VideoReframeRect{}, errors.New("reframe source dimensions must be between 2 and 32768 pixels")
	}
	p, q, err := videoReframeRatio(aspect)
	if err != nil {
		return VideoReframeRect{}, err
	}
	multiple := min(width/p, height/q) / 2 * 2
	if multiple == 0 {
		return VideoReframeRect{}, errors.New("source is too small for an even crop of this aspect ratio")
	}
	w, h := p*multiple, q*multiple
	return VideoReframeRect{X: (width - w + 1) / 2, Y: (height - h + 1) / 2, Width: w, Height: h}, nil
}

// validateVideoReframe returns an owned, chronologically sorted copy. The v1
// contract reserves size per marker, but deliberately rejects animated zoom.
func validateVideoReframe(params VideoReframeParams) (VideoReframeParams, error) {
	if params.Version != 1 {
		return VideoReframeParams{}, errors.New("unsupported reframe version")
	}
	source := params.Source
	if !videoReframeDimension(source.Width) || !videoReframeDimension(source.Height) || !videoReframeFinite(source.DurationSeconds) || source.DurationSeconds <= 0 {
		return VideoReframeParams{}, errors.New("reframe requires valid source dimensions and a finite positive duration")
	}
	p, q, err := videoReframeRatio(params.AspectRatio)
	if err != nil {
		return VideoReframeParams{}, err
	}
	validSize := func(w, h int) bool {
		return videoReframeDimension(w) && videoReframeDimension(h) && w%2 == 0 && h%2 == 0 && w*q == h*p
	}
	if !validSize(params.Output.Width, params.Output.Height) {
		return VideoReframeParams{}, errors.New("reframe output must have even dimensions and the selected aspect ratio")
	}
	if len(params.Markers) == 0 || len(params.Markers) > videoReframeMaxMarkers {
		return VideoReframeParams{}, errors.New("reframe requires between 1 and 100 framing markers")
	}
	params.Markers = append([]VideoReframeMarker(nil), params.Markers...)
	// Check finite times before sorting: NaN would violate the comparator.
	for _, marker := range params.Markers {
		if !videoReframeFinite(marker.TimeSeconds) || marker.TimeSeconds < 0 || marker.TimeSeconds > source.DurationSeconds {
			return VideoReframeParams{}, errors.New("reframe marker time must be within the source duration")
		}
	}
	sort.Slice(params.Markers, func(i, j int) bool { return params.Markers[i].TimeSeconds < params.Markers[j].TimeSeconds })
	if params.Markers[0].TimeSeconds != 0 {
		return VideoReframeParams{}, errors.New("reframe requires a marker at time zero")
	}
	first := params.Markers[0]
	for i, marker := range params.Markers {
		if !validSize(marker.Width, marker.Height) || marker.Width != first.Width || marker.Height != first.Height {
			return VideoReframeParams{}, errors.New("reframe v1 requires a constant even crop size with the selected aspect ratio")
		}
		if marker.Width > source.Width || marker.Height > source.Height || marker.X < 0 || marker.Y < 0 || marker.X > source.Width-marker.Width || marker.Y > source.Height-marker.Height {
			return VideoReframeParams{}, errors.New("reframe crop must stay inside the source")
		}
		if i > 0 && marker.TimeSeconds == params.Markers[i-1].TimeSeconds {
			return VideoReframeParams{}, errors.New("reframe marker timestamps must be unique")
		}
		switch marker.InterpolationToNext {
		case "linear", "smooth", "hold":
		default:
			return VideoReframeParams{}, errors.New("reframe interpolation must be linear, smooth, or hold")
		}
	}
	return params, nil
}

// evaluateVideoReframe is the preview reference implementation. At an exact
// marker timestamp the new framing wins, including a hold-then-cut boundary.
func evaluateVideoReframe(params VideoReframeParams, timeSeconds float64) (VideoReframeRect, error) {
	params, err := validateVideoReframe(params)
	if err != nil {
		return VideoReframeRect{}, err
	}
	if !videoReframeFinite(timeSeconds) {
		return VideoReframeRect{}, errors.New("reframe preview time must be finite")
	}
	t := max(0, min(timeSeconds, params.Source.DurationSeconds))
	i := sort.Search(len(params.Markers), func(i int) bool { return params.Markers[i].TimeSeconds > t }) - 1
	a := params.Markers[i]
	if i == len(params.Markers)-1 || a.InterpolationToNext == "hold" {
		return a.VideoReframeRect, nil
	}
	b := params.Markers[i+1]
	u := (t - a.TimeSeconds) / (b.TimeSeconds - a.TimeSeconds)
	if a.InterpolationToNext == "smooth" {
		u = u * u * (3 - 2*u)
	}
	return VideoReframeRect{
		X:     videoReframeRound(float64(a.X)+float64(b.X-a.X)*u, params.Source.Width-a.Width),
		Y:     videoReframeRound(float64(a.Y)+float64(b.Y-a.Y)*u, params.Source.Height-a.Height),
		Width: a.Width, Height: a.Height,
	}, nil
}

func videoReframeRound(value float64, limit int) int {
	return max(0, min(int(math.Floor(value+0.5)), limit))
}

// changeVideoReframeAspect preserves marker centers where the new bounds allow
// it. It returns new data, so a future editor can treat this as one undo action.
func changeVideoReframeAspect(params VideoReframeParams, aspect string) (VideoReframeParams, error) {
	params, err := validateVideoReframe(params)
	if err != nil {
		return VideoReframeParams{}, err
	}
	crop, err := fitVideoReframeCrop(params.Source.Width, params.Source.Height, aspect)
	if err != nil {
		return VideoReframeParams{}, err
	}
	params.AspectRatio = aspect
	params.Output = VideoReframeOutput{Width: crop.Width, Height: crop.Height}
	for i, marker := range params.Markers {
		params.Markers[i].VideoReframeRect = VideoReframeRect{
			X:     videoReframeRound(float64(marker.X)+float64(marker.Width-crop.Width)/2, params.Source.Width-crop.Width),
			Y:     videoReframeRound(float64(marker.Y)+float64(marker.Height-crop.Height)/2, params.Source.Height-crop.Height),
			Width: crop.Width, Height: crop.Height,
		}
	}
	return params, nil
}

func videoReframeNumber(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// videoReframeAxisExpression matches evaluateVideoReframe, with bounded numeric
// inputs only. Quoting is for the filtergraph parser, not a shell. exact=1 in
// the crop filter prevents chroma subsampling from rounding an odd x/y down.
func videoReframeAxisExpression(markers []VideoReframeMarker, horizontal bool) string {
	coordinate := func(marker VideoReframeMarker) int {
		if horizontal {
			return marker.X
		}
		return marker.Y
	}
	segmentExpression := func(i int) string {
		a := markers[i]
		segment := strconv.Itoa(coordinate(a))
		if i == len(markers)-1 {
			return segment
		}
		b := markers[i+1]
		if a.InterpolationToNext != "hold" && coordinate(a) != coordinate(b) {
			u := fmt.Sprintf("clip((t-%s)/%s,0,1)", videoReframeNumber(a.TimeSeconds), videoReframeNumber(b.TimeSeconds-a.TimeSeconds))
			weight := u
			if a.InterpolationToNext == "smooth" {
				weight = fmt.Sprintf("(%s*%s*(3-2*%s))", u, u, u)
			}
			segment = fmt.Sprintf("(%d+(%d)*%s)", coordinate(a), coordinate(b)-coordinate(a), weight)
		}
		return segment
	}
	// A balanced decision tree avoids ffmpeg's expression parser depth limit
	// at the supported 100-marker cap, and evaluates only the active segment.
	var expression func(int, int) string
	expression = func(start, end int) string {
		if end-start == 1 {
			return segmentExpression(start)
		}
		middle := (start + end) / 2
		return fmt.Sprintf("if(lt(t,%s),%s,%s)", videoReframeNumber(markers[middle].TimeSeconds), expression(start, middle), expression(middle, end))
	}
	return "floor((" + expression(0, len(markers)) + ")+0.5)"
}

// videoReframeFFmpegArgs builds a local render for an ALREADY canonical source.
// sourceStartSeconds is the first video PTS (from probing), used as the common
// video/audio origin. Resetting each stream to its own STARTPTS would erase an
// intentional audio delay. Normalization of rotation/SAR belongs upstream.
func videoReframeFFmpegArgs(params VideoReframeParams, input, output string, sourceStartSeconds float64, hasAudio bool) ([]string, error) {
	params, err := validateVideoReframe(params)
	if err != nil {
		return nil, err
	}
	if !videoReframeFinite(sourceStartSeconds) || strings.TrimSpace(input) == "" || strings.TrimSpace(output) == "" || input == output {
		return nil, errors.New("reframe requires distinct input/output paths and a finite source presentation start")
	}
	first := params.Markers[0]
	origin := videoReframeNumber(sourceStartSeconds)
	filters := fmt.Sprintf("setpts=PTS-(%s)/TB,crop=w=%d:h=%d:x='%s':y='%s':exact=1,scale=%d:%d,setsar=1",
		origin, first.Width, first.Height, videoReframeAxisExpression(params.Markers, true), videoReframeAxisExpression(params.Markers, false), params.Output.Width, params.Output.Height)
	args := []string{"-nostdin", "-n", "-copyts", "-noautorotate", "-i", input, "-map", "0:v:0", "-vf", filters, "-fps_mode", "passthrough", "-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p"}
	if hasAudio {
		args = append(args, "-map", "0:a:0", "-af", "asetpts=PTS-("+origin+")/TB", "-c:a", "aac", "-b:a", "192k")
	} else {
		args = append(args, "-an")
	}
	return append(args, "-movflags", "+faststart", output), nil
}
