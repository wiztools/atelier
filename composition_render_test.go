package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- Pure plan-builder tests (full-string and structural, the
// TestJoinTransitionsFilterGraph pattern) ----

func renderTestVideoRef(id, path string, seconds float64, hasAudio bool) CompositionMediaRef {
	return CompositionMediaRef{ArtifactID: id, Path: path, Kind: "video", DurationSeconds: seconds, Width: 1280, Height: 720, FPS: 30, HasAudio: hasAudio}
}

func renderTestAudioRef(id, path string, seconds float64) CompositionMediaRef {
	return CompositionMediaRef{ArtifactID: id, Path: path, Kind: "audio", DurationSeconds: seconds, HasAudio: true}
}

func renderTestDoc(clips ...CompositionClip) CompositionDoc {
	return CompositionDoc{SchemaVersion: compositionSchemaVersion, Name: "Test", Output: CompositionOutput{Fit: compositionFitPad}, Video: clips, AudioLanes: []CompositionLane{}}
}

func renderTestClip(id string, ref CompositionMediaRef, in, out float64, transition *CompositionTransition) CompositionClip {
	return CompositionClip{ID: id, Ref: ref, InSeconds: in, OutSeconds: out, Transition: transition}
}

func renderTestFacts(refs ...CompositionMediaRef) map[string]joinClipFact {
	facts := map[string]joinClipFact{}
	for _, ref := range refs {
		facts[ref.ArtifactID] = joinClipFact{Duration: ref.DurationSeconds, Width: ref.Width, Height: ref.Height, FPS: ref.FPS, HasAudio: ref.HasAudio}
	}
	return facts
}

func renderTestStillRef(id, path string, width, height int) CompositionMediaRef {
	return CompositionMediaRef{ArtifactID: id, Path: path, Kind: "image", Width: width, Height: height}
}

func TestBuildCompositionRenderPlanStillThenClip(t *testing.T) {
	// A portrait phone photo (stored landscape, EXIF 6) opens the sequence
	// and dissolves into a 720p clip: the clip sets the frame, the still is
	// read once and held for exactly 4s worth of frames, and its silence
	// holds the audio junction without a "no usable audio" notice.
	still := renderTestStillRef("img_a", "/a.jpg", 1080, 1920)
	clip := renderTestVideoRef("vid_b", "/b.mp4", 10, true)
	doc := renderTestDoc(
		renderTestClip("c1", still, 0, 4, &CompositionTransition{Kind: "dissolve", Seconds: 1, AudioCrossfade: true}),
		renderTestClip("c2", clip, 0, 10, nil),
	)
	plan, err := buildCompositionRenderPlan(doc, renderTestFacts(still, clip), compositionRenderOptions{
		Stills: map[string]compositionStillFact{"img_a": {Width: 1080, Height: 1920, Decoder: "mjpeg", Orientation: 6}},
	})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	want := "[0:v]sidedata=mode=delete:type=DISPLAYMATRIX,transpose=1,scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2," +
		"loop=loop=-1:size=1:start=0,trim=end_frame=120,setsar=1,fps=30,format=yuv420p,settb=1/1000000[v0];" +
		"[1:v]trim=start=0:end=10,setpts=PTS-STARTPTS," +
		"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p,settb=1/1000000[v1];" +
		"[v0][v1]xfade=transition=fade:duration=1:offset=3[vx0];" +
		"[vx0]null[vout];" +
		"[2:a]aformat=sample_rates=48000:channel_layouts=stereo,asettb=1/1000000[ac0];" +
		"[1:a]atrim=start=0:end=10,asetpts=PTS-STARTPTS,aformat=sample_rates=48000:channel_layouts=stereo,asettb=1/1000000[ac1];" +
		"[ac0]atrim=start=0:end=3,asetpts=PTS-STARTPTS[at0];" +
		"[at0][ac1]concat=n=2:v=0:a=1[ax0];" +
		"[ax0]anull[aout]"
	if plan.FilterComplex != want {
		t.Fatalf("FilterComplex =\n%s\nwant\n%s", plan.FilterComplex, want)
	}
	if plan.Width != 1280 || plan.Height != 720 || plan.FPS != 30 || plan.SequenceSeconds != 13 {
		t.Fatalf("plan scalars = %dx%d @%g, %gs; want the clip's 1280x720 @30 and 13s", plan.Width, plan.Height, plan.FPS, plan.SequenceSeconds)
	}
	if strings.Join(plan.InputPaths, " ") != "/a.jpg /b.mp4" || len(plan.InputOptions) != 2 ||
		strings.Join(plan.InputOptions[0], " ") != "-noautorotate -f image2 -pattern_type none -framerate 30 -c:v mjpeg" || plan.InputOptions[1] != nil {
		t.Fatalf("inputs = %v / %q", plan.InputPaths, plan.InputOptions)
	}
	if strings.Join(plan.ExtraInputArgs, " ") != "-f lavfi -t 4 -i anullsrc=channel_layout=stereo:sample_rate=48000" {
		t.Fatalf("ExtraInputArgs = %+v", plan.ExtraInputArgs)
	}
	if len(plan.Notices) != 0 {
		t.Fatalf("notices = %v, want none — a still is silent by nature", plan.Notices)
	}
}

func TestBuildCompositionRenderPlanStillFramesAndFits(t *testing.T) {
	photo := renderTestStillRef("img_p", "/p.png", 4032, 3024)
	portrait := renderTestStillRef("img_q", "/q.webp", 1080, 1920)
	stills := map[string]compositionStillFact{
		"img_p": {Width: 4032, Height: 3024, Decoder: "png"},
		"img_q": {Width: 1080, Height: 1920, Decoder: "webp"},
	}

	// Stills only: the frame inherits the first still's shape, fitted to the
	// 1920 long edge; no fps to inherit, so 30. The same photo used twice is
	// read twice (each use holds its own frame), and the blur fit composites
	// each still once by its own display size.
	slideshow := renderTestDoc(
		renderTestClip("c1", photo, 0, 2.5, nil),
		renderTestClip("c2", portrait, 0, 3, nil),
		renderTestClip("c3", photo, 0, 1, nil),
	)
	slideshow.Output.Fit = compositionFitBlur
	plan, err := buildCompositionRenderPlan(slideshow, renderTestFacts(photo, portrait), compositionRenderOptions{Stills: stills})
	if err != nil {
		t.Fatalf("slideshow plan returned error: %v", err)
	}
	if plan.Width != 1920 || plan.Height != 1440 || plan.FPS != 30 {
		t.Fatalf("slideshow frame = %dx%d @%g, want the photo fitted to 1920x1440 @30", plan.Width, plan.Height, plan.FPS)
	}
	if strings.Join(plan.InputPaths, " ") != "/p.png /q.webp /p.png" {
		t.Fatalf("inputs = %v, want one read per still use", plan.InputPaths)
	}
	for _, want := range []string{
		"[0:v]sidedata=mode=delete:type=DISPLAYMATRIX," + fillFilterChain(compositionFitBlur, 4032, 3024, 1920, 1440) + ",loop=loop=-1:size=1:start=0,trim=end_frame=75,",
		"[1:v]sidedata=mode=delete:type=DISPLAYMATRIX," + fillFilterChain(compositionFitBlur, 1080, 1920, 1920, 1440) + ",loop=loop=-1:size=1:start=0,trim=end_frame=90,",
		"[2:v]sidedata=mode=delete:type=DISPLAYMATRIX," + fillFilterChain(compositionFitBlur, 4032, 3024, 1920, 1440) + ",loop=loop=-1:size=1:start=0,trim=end_frame=30,",
		"[v0][v1]concat=n=2:v=1:a=0[vx0]",
		"[vx0][v2]concat=n=2:v=1:a=0[vx1]",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Fatalf("slideshow graph missing %q:\n%s", want, plan.FilterComplex)
		}
	}
	if strings.Contains(plan.FilterComplex, "transpose") || len(plan.Notices) != 0 {
		t.Fatalf("an upright still needs no turn and raises no notice:\n%s\n%v", plan.FilterComplex, plan.Notices)
	}
	if strings.Join(plan.InputOptions[1], " ") != "-noautorotate -f image2 -pattern_type none -framerate 30 -c:v webp" {
		t.Fatalf("webp read options = %q", plan.InputOptions[1])
	}

	// A still opening the sequence never decides the format: the first VIDEO
	// clip does, fps included.
	clip := CompositionMediaRef{ArtifactID: "vid_c", Path: "/c.mp4", Kind: "video", DurationSeconds: 5, Width: 1280, Height: 720, FPS: 24, HasAudio: true}
	titled := renderTestDoc(renderTestClip("c1", photo, 0, 2, nil), renderTestClip("c2", clip, 0, 5, nil))
	titled.Output.Fit = compositionFitCrop
	plan, err = buildCompositionRenderPlan(titled, renderTestFacts(photo, clip), compositionRenderOptions{Stills: stills})
	if err != nil {
		t.Fatalf("titled plan returned error: %v", err)
	}
	if plan.Width != 1280 || plan.Height != 720 || plan.FPS != 24 {
		t.Fatalf("titled frame = %dx%d @%g, want the clip's 1280x720 @24", plan.Width, plan.Height, plan.FPS)
	}
	if !strings.Contains(plan.FilterComplex, "[0:v]sidedata=mode=delete:type=DISPLAYMATRIX,scale=1280:720:force_original_aspect_ratio=increase,crop=1280:720,loop=loop=-1:size=1:start=0,trim=end_frame=48,setsar=1,fps=24,") {
		t.Fatalf("the cropped still must hold 2s at the clip's 24fps:\n%s", plan.FilterComplex)
	}
	if !strings.Contains(strings.Join(plan.InputOptions[0], " "), "-framerate 24") {
		t.Fatalf("the still must read at the project rate: %q", plan.InputOptions[0])
	}

	// A still too small to frame falls back to the 720p default.
	speck := renderTestStillRef("img_s", "/s.gif", 1, 1)
	plan, err = buildCompositionRenderPlan(renderTestDoc(renderTestClip("c1", speck, 0, 1, nil)), renderTestFacts(speck), compositionRenderOptions{})
	if err != nil {
		t.Fatalf("speck plan returned error: %v", err)
	}
	if plan.Width != 1280 || plan.Height != 720 {
		t.Fatalf("speck frame = %dx%d, want the 1280x720 fallback", plan.Width, plan.Height)
	}
	// Without read details the still reads by image2's extension guess.
	if strings.Join(plan.InputOptions[0], " ") != "-noautorotate -f image2 -pattern_type none -framerate 30" {
		t.Fatalf("detail-less read options = %q", plan.InputOptions[0])
	}
}

func TestCompositionFitStillFrame(t *testing.T) {
	cases := []struct {
		width, height int
		wantW, wantH  int
	}{
		{4032, 3024, 1920, 1440},
		{3024, 4032, 1440, 1920},
		{1024, 1024, 1024, 1024}, // never enlarged
		{1081, 721, 1080, 720},   // even-dimensioned
		{6000, 1000, 1920, 320},
		{1, 1, 0, 0}, // too small to frame
		{0, 600, 0, 0},
	}
	for _, tc := range cases {
		if w, h := compositionFitStillFrame(tc.width, tc.height); w != tc.wantW || h != tc.wantH {
			t.Fatalf("compositionFitStillFrame(%d, %d) = %dx%d, want %dx%d", tc.width, tc.height, w, h, tc.wantW, tc.wantH)
		}
	}
}

func TestCompositionOrientationStages(t *testing.T) {
	want := map[int]string{0: "", 1: "", 2: "hflip", 3: "hflip,vflip", 4: "vflip", 5: "transpose=0", 6: "transpose=1", 7: "transpose=3", 8: "transpose=2", 9: ""}
	for orientation, stages := range want {
		if got := strings.Join(compositionOrientationStages(orientation), ","); got != stages {
			t.Fatalf("compositionOrientationStages(%d) = %q, want %q", orientation, got, stages)
		}
	}
}

func TestBuildCompositionRenderPlanSingleClip(t *testing.T) {
	clip := renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 10, true), 0, 10, nil)
	plan, err := buildCompositionRenderPlan(renderTestDoc(clip), renderTestFacts(clip.Ref), compositionRenderOptions{})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	want := "[0:v]trim=start=0:end=10,setpts=PTS-STARTPTS," +
		"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p,settb=1/1000000[v0];" +
		"[v0]null[vout];" +
		"[0:a]atrim=start=0:end=10,asetpts=PTS-STARTPTS,aformat=sample_rates=48000:channel_layouts=stereo,asettb=1/1000000[ac0];" +
		"[ac0]anull[aout]"
	if plan.FilterComplex != want {
		t.Fatalf("FilterComplex =\n%s\nwant\n%s", plan.FilterComplex, want)
	}
	if plan.SequenceSeconds != 10 || plan.OutputSeconds != 10 || plan.Width != 1280 || plan.Height != 720 || plan.FPS != 30 || plan.AudioOnly {
		t.Fatalf("plan scalars = %+v", plan)
	}
	if len(plan.InputPaths) != 1 || plan.InputPaths[0] != "/v.mp4" || len(plan.ExtraInputArgs) != 0 || len(plan.Notices) != 0 {
		t.Fatalf("plan inputs/notices = %+v / %+v", plan.InputPaths, plan.Notices)
	}
}

func TestBuildCompositionRenderPlanDissolveWithAudioCut(t *testing.T) {
	a := renderTestVideoRef("vid_a", "/a.mp4", 4, true)
	b := renderTestVideoRef("vid_b", "/b.mp4", 3, false)
	dissolve := &CompositionTransition{Kind: "dissolve", Seconds: 1, AudioCrossfade: false}
	doc := renderTestDoc(
		renderTestClip("c1", a, 0, 4, dissolve),
		renderTestClip("c2", b, 0, 3, nil),
	)
	plan, err := buildCompositionRenderPlan(doc, renderTestFacts(a, b), compositionRenderOptions{})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	want := "[0:v]trim=start=0:end=4,setpts=PTS-STARTPTS," +
		"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p,settb=1/1000000[v0];" +
		"[1:v]trim=start=0:end=3,setpts=PTS-STARTPTS," +
		"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p,settb=1/1000000[v1];" +
		"[v0][v1]xfade=transition=fade:duration=1:offset=3[vx0];" +
		"[vx0]null[vout];" +
		"[0:a]atrim=start=0:end=4,asetpts=PTS-STARTPTS,aformat=sample_rates=48000:channel_layouts=stereo,asettb=1/1000000[ac0];" +
		"[2:a]aformat=sample_rates=48000:channel_layouts=stereo,asettb=1/1000000[ac1];" +
		"[ac0]atrim=start=0:end=3,asetpts=PTS-STARTPTS[at0];" +
		"[at0][ac1]concat=n=2:v=0:a=1[ax0];" +
		"[ax0]anull[aout]"
	if plan.FilterComplex != want {
		t.Fatalf("FilterComplex =\n%s\nwant\n%s", plan.FilterComplex, want)
	}
	if plan.SequenceSeconds != 6 {
		t.Fatalf("SequenceSeconds = %g, want 6 (4+3 minus the 1s dissolve)", plan.SequenceSeconds)
	}
	if strings.Join(plan.ExtraInputArgs, " ") != "-f lavfi -t 3 -i anullsrc=channel_layout=stereo:sample_rate=48000" {
		t.Fatalf("ExtraInputArgs = %+v", plan.ExtraInputArgs)
	}
	if len(plan.Notices) != 1 || !strings.Contains(plan.Notices[0], "1 clip carried no usable audio") {
		t.Fatalf("notices = %+v", plan.Notices)
	}
}

func TestBuildCompositionRenderPlanLanesRangeSolo(t *testing.T) {
	a := renderTestVideoRef("vid_a", "/a.mp4", 10, true)
	b := renderTestVideoRef("vid_b", "/b.mp4", 5, true)
	c := renderTestVideoRef("vid_c", "/c.mp4", 8, true)
	music := renderTestAudioRef("aud_m", "/m.wav", 6)
	doc := renderTestDoc(
		renderTestClip("c1", a, 0, 10, &CompositionTransition{Kind: "dissolve", Seconds: 1, AudioCrossfade: true}),
		renderTestClip("c2", b, 0, 5, nil),
		renderTestClip("c3", c, 0, 8, nil),
	)
	doc.AudioLanes = []CompositionLane{{
		ID: "l1", Name: "Music",
		Segments: []CompositionSegment{{
			ID: "s1", Ref: music, InSeconds: 0, OutSeconds: 6, StartSeconds: 2,
			GainDb: -6, FadeInSeconds: 1, FadeOutSeconds: 1,
			Ducks: []CompositionDuck{{StartSeconds: 1, EndSeconds: 3, GainDb: -12}},
		}},
	}}
	start := float64(9)
	end := float64(14)
	plan, err := buildCompositionRenderPlan(doc, renderTestFacts(a, b, c), compositionRenderOptions{StartSeconds: &start, EndSeconds: &end})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	for _, want := range []string{
		"[v0][v1]xfade=transition=fade:duration=1:offset=9[vx0]",
		"[vx0][v2]concat=n=2:v=1:a=0[vx1]",
		acrossfadeFilter("1"),
		"[ax0][ac2]concat=n=2:v=0:a=1[ax1]",
		"[3:a]atrim=start=0:end=6,asetpts=PTS-STARTPTS,aformat=sample_rates=48000:channel_layouts=stereo," +
			"volume=volume=-6dB:enable='not(between(t,1,3))'," +
			"volume=volume=-12dB:enable='between(t,1,3)'," +
			"afade=t=in:st=0:d=1,afade=t=out:st=5:d=1,adelay=2000|2000[s1]",
		"[ax1][s1]amix=inputs=2:duration=first:normalize=0[mix]",
		"[mix]atrim=start=9:end=14,asetpts=PTS-STARTPTS[aout]",
		"[vx1]trim=start=9:end=14,setpts=PTS-STARTPTS[vout]",
	} {
		if !strings.Contains(plan.FilterComplex, want) {
			t.Fatalf("FilterComplex missing %q:\n%s", want, plan.FilterComplex)
		}
	}
	if plan.OutputSeconds != 5 || plan.SequenceSeconds != 22 {
		t.Fatalf("seconds = output %g sequence %g, want 5 / 22", plan.OutputSeconds, plan.SequenceSeconds)
	}
	if plan.RangeStart == nil || *plan.RangeStart != 9 || plan.RangeEnd == nil || *plan.RangeEnd != 14 {
		t.Fatalf("range = %+v %+v", plan.RangeStart, plan.RangeEnd)
	}

	// A solo preview keeps only the soloed lane's segment in the mix and
	// silences every clip's original audio (synthesized silence holds the
	// timeline shape).
	soloPlan, err := buildCompositionRenderPlan(doc, renderTestFacts(a, b, c), compositionRenderOptions{SoloLaneID: "l1"})
	if err != nil {
		t.Fatalf("solo buildCompositionRenderPlan returned error: %v", err)
	}
	if !strings.Contains(strings.Join(soloPlan.ExtraInputArgs, " "), "anullsrc=channel_layout=stereo:sample_rate=48000") || !strings.Contains(soloPlan.FilterComplex, "[s1]") {
		t.Fatalf("solo graph lost its lane or synthesized the clip audio wrong:\n%s\nargs %+v", soloPlan.FilterComplex, soloPlan.ExtraInputArgs)
	}
	if strings.Contains(soloPlan.FilterComplex, acrossfadeFilter("1")) {
		t.Fatalf("solo graph must not crossfade synthesized silence — found an acrossfade:\n%s", soloPlan.FilterComplex)
	}
	foundSolo := false
	for _, notice := range soloPlan.Notices {
		if strings.Contains(notice, "solo preview") {
			foundSolo = true
		}
	}
	if !foundSolo {
		t.Fatalf("solo notices = %+v", soloPlan.Notices)
	}
}

func TestBuildCompositionRenderPlanAudioOnly(t *testing.T) {
	music := renderTestAudioRef("aud_m", "/m.wav", 6)
	doc := renderTestDoc()
	doc.AudioLanes = []CompositionLane{{ID: "l1", Name: "Music", Segments: []CompositionSegment{{
		ID: "s1", Ref: music, InSeconds: 0, OutSeconds: 6, StartSeconds: 0, Ducks: []CompositionDuck{},
	}}}}
	plan, err := buildCompositionRenderPlan(doc, map[string]joinClipFact{}, compositionRenderOptions{})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	if !plan.AudioOnly || plan.OutputSeconds != 6 {
		t.Fatalf("plan = audioOnly %v output %g", plan.AudioOnly, plan.OutputSeconds)
	}
	if strings.Contains(plan.FilterComplex, "[v") || !strings.Contains(plan.FilterComplex, "[0:a]atrim=start=0:end=6") {
		t.Fatalf("audio-only graph =\n%s", plan.FilterComplex)
	}
	args := compositionFFmpegArgs(plan, compositionEncodeArgs(true, 0, plan.AudioOnly, true), "/out.m4a")
	if !strings.Contains(strings.Join(args, " "), "-map [aout]") || strings.Contains(strings.Join(args, " "), "[vout]") {
		t.Fatalf("audio-only args = %v", args)
	}

	if _, err := buildCompositionRenderPlan(renderTestDoc(), map[string]joinClipFact{}, compositionRenderOptions{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty composition must refuse, got %v", err)
	}
}

func TestBuildCompositionRenderPlanMuteAudioAndOverrides(t *testing.T) {
	clip := renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 10, true), 0, 10, nil)
	doc := renderTestDoc(clip)
	facts := renderTestFacts(clip.Ref)

	muted, err := buildCompositionRenderPlan(doc, facts, compositionRenderOptions{MuteAudio: true})
	if err != nil {
		t.Fatalf("mute buildCompositionRenderPlan returned error: %v", err)
	}
	if strings.Contains(muted.FilterComplex, "[aout]") || strings.Contains(muted.FilterComplex, "amix") || strings.Contains(muted.FilterComplex, "[0:a]") {
		t.Fatalf("a muted render must carry no audio graph:\n%s", muted.FilterComplex)
	}
	args := compositionFFmpegArgs(muted, compositionEncodeArgs(false, 0, false, false), "/out.mp4")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map [vout]") || strings.Contains(joined, "[aout]") || !strings.Contains(joined, "-an") {
		t.Fatalf("muted args = %v", args)
	}

	// Frame overrides win over the first clip's facts (even-dimensioned).
	overridden, err := buildCompositionRenderPlan(doc, facts, compositionRenderOptions{Width: 1081, Height: 1921, FPS: 24})
	if err != nil {
		t.Fatalf("override buildCompositionRenderPlan returned error: %v", err)
	}
	if overridden.Width != 1080 || overridden.Height != 1920 || overridden.FPS != 24 {
		t.Fatalf("overrides = %dx%d @%g", overridden.Width, overridden.Height, overridden.FPS)
	}
	if !strings.Contains(overridden.FilterComplex, "scale=1080:1920") || !strings.Contains(overridden.FilterComplex, "fps=24") {
		t.Fatalf("the conform chain did not follow the overrides:\n%s", overridden.FilterComplex)
	}

	// A muted audio-only composition is a contradiction and refuses.
	audioOnly := renderTestDoc()
	audioOnly.AudioLanes = []CompositionLane{{ID: "l1", Name: "Music", Segments: []CompositionSegment{{
		ID: "s1", Ref: renderTestAudioRef("aud_m", "/m.wav", 6), InSeconds: 0, OutSeconds: 6, Ducks: []CompositionDuck{},
	}}}}
	if _, err := buildCompositionRenderPlan(audioOnly, map[string]joinClipFact{}, compositionRenderOptions{MuteAudio: true}); err == nil || !strings.Contains(err.Error(), "cannot export without its audio") {
		t.Fatalf("a muted audio-only composition must refuse, got %v", err)
	}
}

func TestCompositionFFmpegArgsShape(t *testing.T) {
	clip := renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 10, true), 0, 10, nil)
	plan, err := buildCompositionRenderPlan(renderTestDoc(clip), renderTestFacts(clip.Ref), compositionRenderOptions{})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	args := compositionFFmpegArgs(plan, compositionEncodeArgs(true, 0, false, true), "/out.mp4")
	want := "-y -i /v.mp4 -filter_complex " + plan.FilterComplex +
		" -map [vout] -map [aout]" +
		" -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p -c:a aac -b:a 128k -movflags +faststart /out.mp4"
	if strings.Join(args, " ") != want {
		t.Fatalf("args =\n%v\nwant\n%s", args, want)
	}

	// A still's read options sit right before its own -i; the synthetic
	// silence holding its audio place follows the real inputs.
	still := renderTestStillRef("img_a", "/s.png", 640, 480)
	plan, err = buildCompositionRenderPlan(renderTestDoc(renderTestClip("c1", still, 0, 4, nil)), renderTestFacts(still), compositionRenderOptions{
		Stills: map[string]compositionStillFact{"img_a": {Width: 640, Height: 480, Decoder: "png"}},
	})
	if err != nil {
		t.Fatalf("still buildCompositionRenderPlan returned error: %v", err)
	}
	args = compositionFFmpegArgs(plan, compositionEncodeArgs(true, 0, false, true), "/out.mp4")
	want = "-y -noautorotate -f image2 -pattern_type none -framerate 30 -c:v png -i /s.png" +
		" -f lavfi -t 4 -i anullsrc=channel_layout=stereo:sample_rate=48000" +
		" -filter_complex " + plan.FilterComplex +
		" -map [vout] -map [aout]" +
		" -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p -c:a aac -b:a 128k -movflags +faststart /out.mp4"
	if strings.Join(args, " ") != want {
		t.Fatalf("still args =\n%v\nwant\n%s", args, want)
	}
}

func TestCompositionDocForRenderOwnsItsCopy(t *testing.T) {
	// A render normalizes its own deep copy: the caller's document keeps its
	// values and shares no memory with what the executor reads. A struct
	// copy once aliased the caller's slices, so normalizing wrote through and
	// a reused doc raced the previous render's executor.
	callerDoc := renderTestDoc(renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 5, true), 0, 5, nil))
	callerDoc.Video[0].Audio.GainDb = 40 // past the +12 dB cap: normalization clamps it
	rendered, err := compositionDocForRender(AppConfig{}, "conv_comp", "", &callerDoc)
	if err != nil {
		t.Fatalf("compositionDocForRender returned %v", err)
	}
	if rendered.Video[0].Audio.GainDb != compositionGainMaxDb {
		t.Fatalf("rendered gain = %g, want the clamped %g", rendered.Video[0].Audio.GainDb, compositionGainMaxDb)
	}
	if callerDoc.Video[0].Audio.GainDb != 40 {
		t.Fatalf("normalizing wrote through to the caller's doc: gain = %g", callerDoc.Video[0].Audio.GainDb)
	}
	if &rendered.Video[0] == &callerDoc.Video[0] {
		t.Fatal("the rendered doc shares its clip slice with the caller's")
	}
}

func TestNormalizeCompositionRange(t *testing.T) {
	full := 22.0
	nine := 9.0
	five := 5.0
	if start, end := normalizeCompositionRange(nil, nil, full); start != nil || end != nil {
		t.Fatalf("no range = %+v %+v", start, end)
	}
	start, end := normalizeCompositionRange(&nine, nil, full)
	if *start != 9 || *end != 22 {
		t.Fatalf("open-ended range = %g %g", *start, *end)
	}
	start, end = normalizeCompositionRange(nil, &five, full)
	if *start != 0 || *end != 5 {
		t.Fatalf("startless range = %g %g", *start, *end)
	}
	if start, end = normalizeCompositionRange(&nine, &nine, full); start != nil || end != nil {
		t.Fatalf("a degenerate range renders the full timeline, got %g %g", *start, *end)
	}
}

// ---- Lifecycle tests (the fake-ffmpeg pattern) ----

// writeFakeCompositionFFmpeg fakes the render CLI: argv lands in args.txt,
// and the OUTPUT call (any argv with -progress, the render's signature — the
// poster call carries none) copies the sniffable MP4 template to the last
// argument. Modes: ok, sleep (hangs until cancelled), fail.
func writeFakeCompositionFFmpeg(t *testing.T, dir, mode string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "template.mp4"), compositionMP4Fixture(8, 1280, 720, true), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%%s\n' "$@" >> "$dir/args.txt"
out=""
mode="%s"
for a in "$@"; do out="$a"; done
case "$mode" in
  fail) echo "fake render failure" >&2; exit 1 ;;
  sleep) sleep 30; exit 0 ;;
esac
# The volumedetect pass writes to the null muxer ("-") — copying the template
# there would stray-file the process CWD (the repo, under go test).
if [ "$out" = "-" ]; then
  echo "[Parsed_volumedetect_0] max_volume: -3.2 dB" >&2
  exit 0
fi
cp "$dir/template.mp4" "$out"
echo "out_time_ms=4000000"
`, mode)
	path := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func waitCompositionRenderTerminal(t *testing.T, app *App, operationID string) CompositionRenderOperation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		app.compositionRendersMu.Lock()
		op, done := app.compositionRenderHistory[operationID]
		app.compositionRendersMu.Unlock()
		if done {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("render %s never reached a terminal state", operationID)
	return CompositionRenderOperation{}
}

func waitCompositionRenderRunning(t *testing.T, app *App, operationID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		app.compositionRendersMu.Lock()
		run, live := app.compositionRenders[operationID]
		status := ""
		if live {
			status = run.op.Status
		}
		app.compositionRendersMu.Unlock()
		if status == "running" {
			return
		}
		if status == "failed" || status == "cancelled" {
			t.Fatalf("render went %s before it could be cancelled", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("render %s never started running", operationID)
}

func TestCompositionRenderLifecycle(t *testing.T) {
	config := editTestHome(t)
	compositionConversationFixture(t, config.Storage)
	app := NewApp()

	// Guard rails first: an unknown mode, an empty request, an empty
	// document, and an export that tries to render a working document.
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "burn", Doc: &CompositionDoc{}}); err == nil || !strings.Contains(err.Error(), "preview") {
		t.Fatalf("an unknown mode must refuse, got %v", err)
	}
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "preview"}); err == nil || !strings.Contains(err.Error(), "saved revision id or the working document") {
		t.Fatalf("a render with neither doc nor artifact must refuse, got %v", err)
	}
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &CompositionDoc{}}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty document must refuse, got %v", err)
	}
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "export", Doc: &CompositionDoc{Video: []CompositionClip{{ID: "c", Ref: renderTestVideoRef("vid_a", "/v.mp4", 5, true), OutSeconds: 5}}}}); err == nil || !strings.Contains(err.Error(), "save the composition first") {
		t.Fatalf("an export without a pinned revision must refuse, got %v", err)
	}

	// The happy path renders the working document (the preview contract —
	// no forced save) and lands a playable file in the preview directory.
	seed, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", ArtifactIDs: []string{"vid_a", "aud_a"}})
	if err != nil {
		t.Fatalf("SeedComposition returned error: %v", err)
	}
	okConfig := config
	okConfig.Providers.Local.FFmpeg.Binary = writeFakeCompositionFFmpeg(t, filepath.Join(t.TempDir(), "ok"), "ok")
	if err := writeAppConfig(okConfig); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}
	// TestMain pins binary lookup off for the whole package; the edit-session
	// tests re-stub it per phase, and so does this one.
	stubLocalLookup(t, map[string]string{okConfig.Providers.Local.FFmpeg.Binary: okConfig.Providers.Local.FFmpeg.Binary})
	op, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc, PreviewKey: " key_timeline.key_settings "})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	// The op names its conversation from submit through every transition:
	// the event is app-global, so listeners filter and attribute by it. It
	// echoes the editor's preview key the same way — opaque, trimmed — so
	// the stage can tell whether a finished render matches the timeline.
	if op.ConversationID != "conv_comp" {
		t.Fatalf("submitted op conversationId = %q, want conv_comp", op.ConversationID)
	}
	if op.PreviewKey != "key_timeline.key_settings" {
		t.Fatalf("submitted op previewKey = %q, want the request's key", op.PreviewKey)
	}
	terminal := waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "completed" {
		t.Fatalf("terminal = %+v, want completed", terminal)
	}
	if terminal.ConversationID != "conv_comp" {
		t.Fatalf("terminal op conversationId = %q, want conv_comp", terminal.ConversationID)
	}
	if terminal.PreviewKey != "key_timeline.key_settings" {
		t.Fatalf("terminal op previewKey = %q, want the request's key", terminal.PreviewKey)
	}
	if terminal.Error != "" || terminal.ResultURL == "" || terminal.OutputSeconds != 8 || terminal.AudioOnly || terminal.Width != 1280 {
		t.Fatalf("completed op = %+v", terminal)
	}
	previewPath := strings.TrimPrefix(terminal.ResultURL, artifactPrefix)
	if !strings.Contains(previewPath, compositionPreviewDirName) {
		t.Fatalf("preview path %q is not under the preview directory", previewPath)
	}
	if _, err := os.Stat(previewPath); err != nil {
		t.Fatalf("preview file missing: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(filepath.Dir(okConfig.Providers.Local.FFmpeg.Binary), "args.txt"))
	if err != nil || !strings.Contains(string(args), "-progress") || !strings.Contains(string(args), "-filter_complex") {
		t.Fatalf("the render never invoked ffmpeg with its graph: %v", err)
	}

	// A missing source refuses up front, naming the artifact (PR-06). The
	// doc is copied clip-wise — seed.Doc's slices are shared, and this
	// mutation must not leak into the later phases.
	broken := seed.Doc
	brokenClips := append([]CompositionClip{}, seed.Doc.Video...)
	brokenClips[0].Ref.Path = "/nonexistent/vid.mp4"
	broken.Video = brokenClips
	op, err = app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &broken})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	terminal = waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "failed" || !strings.Contains(terminal.Error, "vid_a") || !strings.Contains(terminal.Error, "missing source file") {
		t.Fatalf("missing-source op = %+v", terminal)
	}

	// One render per conversation: a second submit while one runs refuses,
	// and cancellation records the cancelled state without a result.
	sleepConfig := config
	sleepConfig.Providers.Local.FFmpeg.Binary = writeFakeCompositionFFmpeg(t, filepath.Join(t.TempDir(), "sleep"), "sleep")
	if err := writeAppConfig(sleepConfig); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}
	stubLocalLookup(t, map[string]string{sleepConfig.Providers.Local.FFmpeg.Binary: sleepConfig.Providers.Local.FFmpeg.Binary})
	op, err = app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc, PreviewKey: "key_running"})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a concurrent render must refuse, got %v", err)
	}
	waitCompositionRenderRunning(t, app, op.ID)
	// A render outlives its editor: the lister hands a reopened composer the
	// in-flight op — its own conversation's only — with the key it was built
	// from, so the reopened stage can judge it when it lands.
	if listed := app.ListCompositionRenders("conv_comp"); len(listed) != 1 || listed[0].ID != op.ID || listed[0].Status != "running" || listed[0].ConversationID != "conv_comp" || listed[0].PreviewKey != "key_running" {
		t.Fatalf("ListCompositionRenders(conv_comp) = %+v, want the running op", listed)
	}
	if listed := app.ListCompositionRenders("conv_other"); listed == nil || len(listed) != 0 {
		t.Fatalf("ListCompositionRenders(conv_other) = %#v, want an empty non-nil list", listed)
	}
	if err := app.CancelCompositionRender(op.ID); err != nil {
		t.Fatalf("CancelCompositionRender returned error: %v", err)
	}
	terminal = waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "cancelled" || terminal.ResultURL != "" {
		t.Fatalf("cancelled op = %+v", terminal)
	}
	// The terminal op files into history in the same locked step that
	// deregisters it, so a finished render is never listed.
	if listed := app.ListCompositionRenders("conv_comp"); len(listed) != 0 {
		t.Fatalf("a finished render must not be listed, got %+v", listed)
	}

	// A failing ffmpeg surfaces its stderr as the op's error.
	failConfig := config
	failConfig.Providers.Local.FFmpeg.Binary = writeFakeCompositionFFmpeg(t, filepath.Join(t.TempDir(), "fail"), "fail")
	if err := writeAppConfig(failConfig); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}
	stubLocalLookup(t, map[string]string{failConfig.Providers.Local.FFmpeg.Binary: failConfig.Providers.Local.FFmpeg.Binary})
	op, err = app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	terminal = waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "failed" || !strings.Contains(terminal.Error, "fake render failure") {
		t.Fatalf("failed op = %+v", terminal)
	}
}

// TestCompositionRenderReplace pins the auto-preview contract: a running
// preview yields to a Replace request and reports itself superseded (never
// failed, though its killed ffmpeg errored), an export may replace a preview
// but nothing replaces an export, and a finished preview's files can be
// discarded by id — previews only, never while registered.
func TestCompositionRenderReplace(t *testing.T) {
	config := editTestHome(t)
	compositionConversationFixture(t, config.Storage)
	app := NewApp()
	seed, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", ArtifactIDs: []string{"vid_a", "aud_a"}})
	if err != nil {
		t.Fatalf("SeedComposition returned error: %v", err)
	}
	saved, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: "conv_comp", Doc: seed.Doc})
	if err != nil {
		t.Fatalf("SaveCompositionEdit returned error: %v", err)
	}
	useFake := func(mode string) {
		phase := config
		phase.Providers.Local.FFmpeg.Binary = writeFakeCompositionFFmpeg(t, filepath.Join(t.TempDir(), mode), mode)
		if err := writeAppConfig(phase); err != nil {
			t.Fatalf("writeAppConfig: %v", err)
		}
		stubLocalLookup(t, map[string]string{phase.Providers.Local.FFmpeg.Binary: phase.Providers.Local.FFmpeg.Binary})
	}
	previewDoc := func() *CompositionDoc {
		doc := seed.Doc
		return &doc
	}

	useFake("sleep")
	first, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: previewDoc(), PreviewKey: "key_first", Auto: true})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	if !first.Auto || first.PreviewKey != "key_first" {
		t.Fatalf("submitted op = %+v, want auto with its key", first)
	}
	waitCompositionRenderRunning(t, app, first.ID)
	second, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: previewDoc(), PreviewKey: "key_second", Auto: true, Replace: true})
	if err != nil {
		t.Fatalf("a Replace preview must make room, got %v", err)
	}
	// The retired run may still be winding down, but it is no longer the
	// conversation's render: only its successor is listed.
	if listed := app.ListCompositionRenders("conv_comp"); len(listed) != 1 || listed[0].ID != second.ID {
		t.Fatalf("ListCompositionRenders = %+v, want only the replacing render", listed)
	}
	retired := waitCompositionRenderTerminal(t, app, first.ID)
	if retired.Status != "cancelled" || retired.SupersededBy != second.ID || retired.Error != "" || retired.ResultURL != "" {
		t.Fatalf("replaced op = %+v, want cancelled, superseded by %s, no error, no result", retired, second.ID)
	}
	waitCompositionRenderRunning(t, app, second.ID)
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: previewDoc()}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("without Replace a second render must still refuse, got %v", err)
	}

	// An export makes room the same way — and then nothing replaces it.
	export, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "export", ArtifactID: saved.ArtifactID, Replace: true})
	if err != nil {
		t.Fatalf("an export must replace a running preview, got %v", err)
	}
	if export.Auto {
		t.Fatalf("an export submitted without Auto echoed auto: %+v", export)
	}
	retired = waitCompositionRenderTerminal(t, app, second.ID)
	if retired.Status != "cancelled" || retired.SupersededBy != export.ID {
		t.Fatalf("preview replaced by the export = %+v", retired)
	}
	waitCompositionRenderRunning(t, app, export.ID)
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: previewDoc(), Replace: true}); err == nil || !strings.Contains(err.Error(), "export is rendering") {
		t.Fatalf("a preview must never replace an export, got %v", err)
	}
	if err := app.DiscardCompositionPreview(export.ID); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("discarding a registered render must refuse, got %v", err)
	}
	// A hand cancel names no successor, and an export cancelled mid-encode
	// adopts nothing.
	if err := app.CancelCompositionRender(export.ID); err != nil {
		t.Fatalf("CancelCompositionRender returned error: %v", err)
	}
	stopped := waitCompositionRenderTerminal(t, app, export.ID)
	if stopped.Status != "cancelled" || stopped.SupersededBy != "" || stopped.Error != "" || stopped.ExportArtifactID != "" {
		t.Fatalf("hand-cancelled export = %+v", stopped)
	}

	// Discard removes a finished preview's file and poster.
	useFake("ok")
	done, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: previewDoc(), Replace: true})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	finished := waitCompositionRenderTerminal(t, app, done.ID)
	if finished.Status != "completed" || finished.ResultPosterURL == "" {
		t.Fatalf("finished preview = %+v, want completed with a poster", finished)
	}
	previewPath := strings.TrimPrefix(finished.ResultURL, artifactPrefix)
	posterPath := strings.TrimPrefix(finished.ResultPosterURL, artifactPrefix)
	for _, path := range []string{previewPath, posterPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preview output %s missing before the discard: %v", path, err)
		}
	}
	if err := app.DiscardCompositionPreview(done.ID); err != nil {
		t.Fatalf("DiscardCompositionPreview returned error: %v", err)
	}
	for _, path := range []string{previewPath, posterPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preview output %s survived the discard (%v)", path, err)
		}
	}
	// Only render ids build a path — nothing else reaches the filesystem.
	for _, id := range []string{"", "../etc/passwd", "crend_../../x", "vid_a", "crend_XYZ"} {
		if err := app.DiscardCompositionPreview(id); err == nil {
			t.Fatalf("DiscardCompositionPreview(%q) must refuse", id)
		}
	}
}

func TestCompositionRenderReadsStills(t *testing.T) {
	config := editTestHome(t)
	artifacts := compositionConversationFixture(t, config.Storage)
	app := NewApp()
	okConfig := config
	okConfig.Providers.Local.FFmpeg.Binary = writeFakeCompositionFFmpeg(t, filepath.Join(t.TempDir(), "ok"), "ok")
	if err := writeAppConfig(okConfig); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}
	stubLocalLookup(t, map[string]string{okConfig.Providers.Local.FFmpeg.Binary: okConfig.Providers.Local.FFmpeg.Binary})

	// A portrait EXIF still opens a sequence before a 720p clip: the render
	// measures it from its bytes (no ffprobe — TestMain pins lookups off),
	// reads it once under the decoder its magic named, turns it upright in
	// the graph, and lets the clip set the frame.
	seed, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", ArtifactIDs: []string{"img_r", "vid_a"}})
	if err != nil {
		t.Fatalf("SeedComposition returned error: %v", err)
	}
	op, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	terminal := waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "completed" || terminal.Width != 1280 || terminal.Height != 720 {
		t.Fatalf("still render = %+v, want completed at the clip's 1280x720", terminal)
	}
	for _, notice := range terminal.Notices {
		if strings.Contains(notice, "no usable audio") {
			t.Fatalf("a still's silence must not raise the silent-clip notice: %v", terminal.Notices)
		}
	}
	recorded, err := os.ReadFile(filepath.Join(filepath.Dir(okConfig.Providers.Local.FFmpeg.Binary), "args.txt"))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	for _, want := range []string{
		"-noautorotate\n-f\nimage2\n-pattern_type\nnone\n-framerate\n",
		"\n-c:v\nmjpeg\n-i\n" + filepath.Join(artifacts, "img_r.jpg") + "\n",
		"[0:v]sidedata=mode=delete:type=DISPLAYMATRIX,transpose=1,scale=1280:720:",
		"loop=loop=-1:size=1:start=0,trim=end_frame=",
	} {
		if !strings.Contains(string(recorded), want) {
			t.Fatalf("render argv missing %q:\n%s", want, recorded)
		}
	}

	// A still whose bytes are not an image fails the render naming it and
	// the way out, before ffmpeg ever runs on it.
	junkPath := filepath.Join(artifacts, "img_junk.png")
	if err := os.WriteFile(junkPath, []byte("not an image"), 0644); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	junk := renderTestDoc(renderTestClip("c1", CompositionMediaRef{ArtifactID: "img_junk", Path: junkPath, Kind: "image", Width: 10, Height: 10}, 0, 2, nil))
	op, err = app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &junk})
	if err != nil {
		t.Fatalf("junk SubmitCompositionRender returned error: %v", err)
	}
	terminal = waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "failed" || !strings.Contains(terminal.Error, "could not read the still img_junk") || !strings.Contains(terminal.Error, "convert_image") {
		t.Fatalf("junk still render = %+v, want a named failure", terminal)
	}
}

func TestCompositionExportLifecycle(t *testing.T) {
	config := editTestHome(t)
	artifacts := compositionConversationFixture(t, config.Storage)
	app := NewApp()
	fakeDir := filepath.Join(t.TempDir(), "ffmpeg")
	fake := writeFakeCompositionFFmpeg(t, fakeDir, "ok")
	exportConfig := config
	exportConfig.Providers.Local.FFmpeg.Binary = fake
	if err := writeAppConfig(exportConfig); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}
	stubLocalLookup(t, map[string]string{fake: fake})

	// Save a revision first — the export's pinned input (EX-04).
	seed, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", ArtifactIDs: []string{"vid_a"}})
	if err != nil {
		t.Fatalf("SeedComposition returned error: %v", err)
	}
	saved, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: "conv_comp", Doc: seed.Doc})
	if err != nil {
		t.Fatalf("SaveCompositionEdit returned error: %v", err)
	}

	// An export while the conversation streams refuses up front.
	app.streamsMu.Lock()
	app.streamConversations["req"] = "conv_comp"
	app.streamsMu.Unlock()
	_, err = app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "export", ArtifactID: saved.ArtifactID})
	if err == nil || !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("a streaming conversation must refuse the export, got %v", err)
	}
	app.streamsMu.Lock()
	delete(app.streamConversations, "req")
	app.streamsMu.Unlock()

	// The export: frame override, CRF, and audio omitted (the seeded clip
	// has audio, so this exercises the video-only graph and -an).
	noAudio := false
	op, err := app.SubmitCompositionRender(CompositionRenderRequest{
		ConversationID: "conv_comp",
		Mode:           "export",
		ArtifactID:     saved.ArtifactID,
		IncludeAudio:   &noAudio,
		Width:          1920,
		Height:         1080,
		CRF:            23,
	})
	if err != nil {
		t.Fatalf("SubmitCompositionRender(export) returned error: %v", err)
	}
	terminal := waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "completed" {
		t.Fatalf("export terminal = %+v", terminal)
	}
	if terminal.Width != 1920 || terminal.Height != 1080 || terminal.IncludeAudio || terminal.ExportArtifactID == "" || !strings.HasPrefix(terminal.ExportArtifactID, "vid_") {
		t.Fatalf("export op fields = %+v", terminal)
	}
	if !strings.HasPrefix(terminal.ResultURL, artifactPrefix) || !strings.Contains(terminal.ResultURL, "conv_comp") {
		t.Fatalf("export result URL = %q, want the conversation artifact", terminal.ResultURL)
	}

	// The adopted artifact and its linked turn landed in the conversation.
	detail, err := getConversation(config.Storage, "conv_comp")
	if err != nil {
		t.Fatalf("getConversation returned error: %v", err)
	}
	var exportTurn *HistoryTurn
	for i := len(detail.Turns) - 1; i >= 0; i-- {
		if detail.Turns[i].ProviderResponse != nil {
			if _, ok := detail.Turns[i].ProviderResponse["compositionExport"]; ok {
				exportTurn = &detail.Turns[i]
			}
		}
	}
	if exportTurn == nil {
		t.Fatal("the export's linked turn is missing from history")
	}
	link := exportTurn.ProviderResponse["compositionExport"].(map[string]any)
	if link["artifactId"] != terminal.ExportArtifactID || link["revisionArtifactId"] != saved.ArtifactID || link["kind"] != "video" {
		t.Fatalf("compositionExport link = %+v", link)
	}
	var exported *HistoryContent
	for _, content := range exportTurn.Content {
		if content.ArtifactID == terminal.ExportArtifactID {
			exported = &content
		}
	}
	if exported == nil || exported.Type != "video" {
		t.Fatalf("the export turn carries no video artifact: %+v", exportTurn.Content)
	}
	if _, err := os.Stat(filepath.Join(artifacts, filepath.Base(exported.Path))); err != nil {
		t.Fatalf("the exported file never landed in the artifacts dir: %v", err)
	}
	if detail.Conversation.Stats.TurnCount != 2 || detail.Conversation.Stats.ArtifactCount != 2 {
		t.Fatalf("stats = %+v, want the save and the export counted", detail.Conversation.Stats)
	}

	// The rendered argv carried the export block: the house preset, the
	// requested CRF, the overridden frame's scale, and -an for the omitted
	// mix.
	recorded, err := os.ReadFile(filepath.Join(fakeDir, "args.txt"))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	joined := strings.ReplaceAll(string(recorded), "\n", " ")
	for _, want := range []string{"-preset veryfast", "-crf 23", "-an", "scale=1920:1080"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("export argv missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "amix") || strings.Contains(joined, "acrossfade") {
		t.Fatalf("an audio-omitted export must carry no audio graph:\n%s", joined)
	}

	// An audio-only composition exports its mix as an aud_ artifact (AU-08).
	audioSeed, err := app.SeedComposition(SeedCompositionRequest{ConversationID: "conv_comp", ArtifactIDs: []string{"aud_a"}})
	if err != nil {
		t.Fatalf("audio SeedComposition returned error: %v", err)
	}
	audioSaved, err := app.SaveCompositionEdit(SaveCompositionEditRequest{ConversationID: "conv_comp", Doc: audioSeed.Doc})
	if err != nil {
		t.Fatalf("audio SaveCompositionEdit returned error: %v", err)
	}
	op, err = app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "export", ArtifactID: audioSaved.ArtifactID})
	if err != nil {
		t.Fatalf("audio export submit returned error: %v", err)
	}
	terminal = waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "completed" || !terminal.AudioOnly || !strings.HasPrefix(terminal.ExportArtifactID, "aud_") {
		t.Fatalf("audio export = %+v", terminal)
	}
	// The fake's volumedetect pass reports a −3.2 dB peak — comfortably under
	// the ceiling, so no clipping notice rides the export.
	for _, notice := range terminal.Notices {
		if strings.Contains(notice, "clips") {
			t.Fatalf("a −3.2 dB peak must not raise the clipping notice: %v", terminal.Notices)
		}
	}
	if _, err := os.Stat("-"); err == nil {
		t.Fatal("the fake ffmpeg stray-filed the null output into the working directory")
	}
}

// ---- Real-ffmpeg graph verification (opt-in, the updater_e2e pattern) ----
//
// The plan tests above pin the graph TEXT; only ffmpeg can say whether the
// graph LINKS — the composer's first real render failed on an audio
// pass-through (anull) wired into the video chain, a media-type mismatch no
// string comparison sees and the fake-ffmpeg lifecycle tests never parse.
// Set ATELIER_COMPOSITION_FFMPEG=1 (ffmpeg on PATH) to render every
// representative shape for real; plain `go test` stays machine-independent.
func TestCompositionRenderGraphsOnRealFFmpeg(t *testing.T) {
	if os.Getenv("ATELIER_COMPOSITION_FFMPEG") == "" {
		t.Skip("set ATELIER_COMPOSITION_FFMPEG=1 to run the composition graphs through a real ffmpeg")
	}
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on PATH")
	}
	dir := t.TempDir()
	encode := func(name string, args ...string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		cmd := exec.Command(binary, append([]string{"-y", "-v", "error"}, append(args, path)...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg fixture %s: %v\n%s", name, err, out)
		}
		return path
	}
	// An audio-carrying 1280x720 clip, a second audio-carrying 960x540 clip
	// (the pad conform runs between them), a silent 640x360 clip, and a
	// stereo WAV for the lanes.
	withAudio := encode("a.mp4",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
	withAudioB := encode("b.mp4",
		"-f", "lavfi", "-i", "testsrc=size=960x540:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=550:sample_rate=48000:duration=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
	silent := encode("c.mp4",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	wav := encode("m.wav",
		"-f", "lavfi", "-i", "sine=frequency=220:sample_rate=48000:duration=3",
		"-ac", "2")

	refA := CompositionMediaRef{ArtifactID: "vid_a", Path: withAudio, Kind: "video", DurationSeconds: 3, Width: 1280, Height: 720, FPS: 30, HasAudio: true}
	refB := CompositionMediaRef{ArtifactID: "vid_b", Path: withAudioB, Kind: "video", DurationSeconds: 3, Width: 960, Height: 540, FPS: 24, HasAudio: true}
	refC := CompositionMediaRef{ArtifactID: "vid_c", Path: silent, Kind: "video", DurationSeconds: 3, Width: 640, Height: 360, FPS: 24, HasAudio: false}
	refM := CompositionMediaRef{ArtifactID: "aud_m", Path: wav, Kind: "audio", DurationSeconds: 3, HasAudio: true}
	facts := renderTestFacts(refA, refB, refC, refM)

	// Stills, each measured the executor's way: an 800x600 PNG, a GIF, a
	// WebP, and a JPEG stored 64x32 — left half red, right half blue — whose
	// EXIF 6 turns it portrait with red on top.
	pngStill := encode("s.png", "-f", "lavfi", "-i", "testsrc2=size=800x600:rate=1:duration=1", "-frames:v", "1")
	gifStill := encode("s.gif", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=5:duration=1")
	webpStill := encode("s.webp", "-f", "lavfi", "-i", "testsrc2=size=640x480:rate=1:duration=1", "-frames:v", "1")
	split := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			if x < 32 {
				split.Set(x, y, color.RGBA{R: 230, A: 255})
			} else {
				split.Set(x, y, color.RGBA{B: 230, A: 255})
			}
		}
	}
	rotatedStill := filepath.Join(dir, "rot.jpg")
	if err := os.WriteFile(rotatedStill, compositionEXIFJPEGOf(t, split, 6), 0644); err != nil {
		t.Fatalf("write rot.jpg: %v", err)
	}
	stills := map[string]compositionStillFact{}
	stillRef := func(id, path string) CompositionMediaRef {
		t.Helper()
		fact, err := measureCompositionStillFile(path)
		if err != nil {
			t.Fatalf("measure %s: %v", path, err)
		}
		stills[id] = fact
		facts[id] = joinClipFact{Width: fact.Width, Height: fact.Height}
		return CompositionMediaRef{ArtifactID: id, Path: path, Kind: "image", Width: fact.Width, Height: fact.Height}
	}
	refP := stillRef("img_p", pngStill)
	refG := stillRef("img_g", gifStill)
	refW := stillRef("img_w", webpStill)
	refR := stillRef("img_r", rotatedStill)
	if refR.Width != 32 || refR.Height != 64 {
		t.Fatalf("the EXIF-6 still measured %dx%d, want its portrait display size 32x64", refR.Width, refR.Height)
	}
	musicLane := CompositionLane{ID: "lane1", Name: "Music", Segments: []CompositionSegment{{
		ID: "seg1", Ref: refM, InSeconds: 0, OutSeconds: 3, StartSeconds: 1,
		GainDb: -6, FadeInSeconds: 0.5, FadeOutSeconds: 0.5,
		Ducks: []CompositionDuck{{StartSeconds: 1, EndSeconds: 2, GainDb: -12}},
	}}}
	dissolve := &CompositionTransition{Kind: "dissolve", Seconds: 1, AudioCrossfade: true}
	cutWithSilent := func() CompositionDoc {
		return renderTestDoc(renderTestClip("c1", refA, 0, 3, nil), renderTestClip("c2", refC, 0, 3, nil))
	}
	dissolveDoc := func() CompositionDoc {
		return renderTestDoc(renderTestClip("c1", refA, 0, 3, dissolve), renderTestClip("c2", refB, 0, 3, nil))
	}
	withLanes := func() CompositionDoc {
		doc := renderTestDoc(renderTestClip("c1", refA, 0, 3, nil))
		doc.AudioLanes = []CompositionLane{musicLane}
		return doc
	}
	one := 1.0
	four := 4.0
	// Stills: a slideshow that wipes and dissolves between three image
	// formats under music, a portrait EXIF still beside footage under the
	// blur fit (two blur composites in one graph), and a still alone (its
	// frame inherits the still's portrait display size).
	slideshow := func() CompositionDoc {
		doc := renderTestDoc(
			renderTestClip("c1", refP, 0, 2, &CompositionTransition{Kind: "wipeleft", Seconds: 0.5}),
			renderTestClip("c2", refG, 0, 1.5, &CompositionTransition{Kind: "dissolve", Seconds: 0.5, AudioCrossfade: true}),
			renderTestClip("c3", refW, 0, 2, nil),
		)
		doc.AudioLanes = []CompositionLane{musicLane}
		return doc
	}
	portraitBlur := func() CompositionDoc {
		doc := renderTestDoc(renderTestClip("c1", refR, 0, 2, dissolve), renderTestClip("c2", refB, 0, 3, nil))
		doc.Output.Fit = compositionFitBlur
		return doc
	}
	croppedStills := func() CompositionDoc {
		doc := renderTestDoc(renderTestClip("c1", refA, 0, 3, nil), renderTestClip("c2", refG, 0, 1.5, nil), renderTestClip("c3", refR, 0, 1, nil))
		doc.Output.Fit = compositionFitCrop
		return doc
	}

	scenarios := []struct {
		name         string
		doc          CompositionDoc
		opts         compositionRenderOptions
		preview      bool
		crf          int
		includeAudio bool
	}{
		{"single clip", renderTestDoc(renderTestClip("c1", refA, 0, 3, nil)), compositionRenderOptions{}, true, 0, true},
		{"cut with silent peer", cutWithSilent(), compositionRenderOptions{}, true, 0, true},
		{"dissolve with crossfade", dissolveDoc(), compositionRenderOptions{}, true, 0, true},
		// The conv_0ffd6212 shape: a cut junction (concat) ahead of a dissolve
		// junction (xfade). ffmpeg 9's concat junction outputs a 1/1000000
		// timebase link while the incoming clip chain runs at 1/fps, and xfade
		// refuses the mismatch — the settb stages on every junction-bound
		// chain are what let this graph configure.
		{"cut before dissolve", renderTestDoc(renderTestClip("c1", refA, 0, 3, nil), renderTestClip("c2", refB, 0, 3, dissolve), renderTestClip("c3", refA, 0, 3, nil)), compositionRenderOptions{}, true, 0, true},
		{"wipe transition", renderTestDoc(renderTestClip("c1", refA, 0, 3, &CompositionTransition{Kind: "wipeleft", Seconds: 1}), renderTestClip("c2", refB, 0, 3, nil)), compositionRenderOptions{}, true, 0, true},
		{"lanes with ducks", withLanes(), compositionRenderOptions{}, true, 0, true},
		{"audio only", CompositionDoc{SchemaVersion: compositionSchemaVersion, Name: "Test", Output: CompositionOutput{Fit: compositionFitPad}, AudioLanes: []CompositionLane{musicLane}}, compositionRenderOptions{}, true, 0, true},
		{"range preview", cutWithSilent(), compositionRenderOptions{StartSeconds: &one, EndSeconds: &four}, true, 0, true},
		{"export muted", dissolveDoc(), compositionRenderOptions{MuteAudio: true}, false, 20, false},
		{"still dissolves into clip", renderTestDoc(renderTestClip("c1", refP, 0, 2, dissolve), renderTestClip("c2", refA, 0, 3, nil)), compositionRenderOptions{}, true, 0, true},
		{"stills slideshow", slideshow(), compositionRenderOptions{}, true, 0, true},
		{"portrait still blur", portraitBlur(), compositionRenderOptions{}, true, 0, true},
		{"cropped stills export", croppedStills(), compositionRenderOptions{}, false, 20, true},
		{"still range preview", renderTestDoc(renderTestClip("c1", refP, 0, 3, nil), renderTestClip("c2", refC, 0, 3, nil)), compositionRenderOptions{StartSeconds: &one, EndSeconds: &four}, true, 0, true},
		{"portrait still alone", renderTestDoc(renderTestClip("c1", refR, 0, 1, nil)), compositionRenderOptions{}, true, 0, true},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			opts := scenario.opts
			opts.Stills = stills
			plan, err := buildCompositionRenderPlan(scenario.doc, facts, opts)
			if err != nil {
				t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
			}
			output := filepath.Join(dir, strings.ReplaceAll(scenario.name, " ", "-")+".mp4")
			args := compositionFFmpegArgs(plan, compositionEncodeArgs(scenario.preview, scenario.crf, plan.AudioOnly, scenario.includeAudio), output)
			cmd := exec.Command(binary, append([]string{"-v", "error"}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg refused the graph: %v\nfilter_complex: %s\n%s", err, plan.FilterComplex, out)
			}
		})
	}

	// A still holds exactly its length: the slideshow renders the layout's
	// frame count (2 + 1.5 + 2s less two 0.5s overlaps, at the PNG-inherited
	// default 30fps — 135 frames).
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe on PATH — the still length and orientation checks need it")
	}
	countOut, err := exec.Command(ffprobe, "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames,width,height", "-of", "csv=p=0", filepath.Join(dir, "stills-slideshow.mp4")).Output()
	if err != nil {
		t.Fatalf("ffprobe slideshow: %v", err)
	}
	fields := strings.Split(strings.TrimSpace(string(countOut)), ",")
	_, sequence := compositionClipLayout(slideshow())
	wantFrames := int(math.Round(sequence * 30))
	if len(fields) != 3 || fields[0] != "800" || fields[1] != "600" || fields[2] != strconv.Itoa(wantFrames) {
		t.Fatalf("slideshow = %v, want 800x600 at exactly %d frames", fields, wantFrames)
	}

	// The EXIF-6 photo alone renders portrait (32x64) and upright: its stored
	// left-red/right-blue halves turn red-on-top. The file must carry NO
	// display matrix — the decoder attaches the EXIF turn to every frame even
	// under -noautorotate, and a leaked matrix makes players turn the
	// upright picture again — and the byte count cannot tell 32x64 from
	// 64x32, so the coded size is checked separately. Both portrait outputs
	// are checked: the still alone, and the still opening a blur sequence
	// (where a leaked matrix would turn the whole composition).
	for _, name := range []string{"portrait-still-alone.mp4", "portrait-still-blur.mp4"} {
		sideData, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream_side_data=side_data_type", "-of", "csv=p=0", filepath.Join(dir, name)).Output()
		if err != nil {
			t.Fatalf("ffprobe %s side data: %v", name, err)
		}
		if strings.Contains(string(sideData), "Display Matrix") {
			t.Fatalf("%s carries a display matrix — players would turn it again: %s", name, sideData)
		}
	}
	sizeOut, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", filepath.Join(dir, "portrait-still-alone.mp4")).Output()
	if err != nil || strings.TrimSpace(string(sizeOut)) != "32,64" {
		t.Fatalf("portrait still coded size = %q (err %v), want 32,64", sizeOut, err)
	}
	frame, err := exec.Command(binary, "-v", "error", "-i", filepath.Join(dir, "portrait-still-alone.mp4"),
		"-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil {
		t.Fatalf("decode portrait frame: %v", err)
	}
	if len(frame) != 32*64*3 {
		t.Fatalf("portrait frame is %d bytes, want a 32x64 rgb24 frame (%d)", len(frame), 32*64*3)
	}
	pixel := func(x, y int) (int, int, int) {
		offset := (y*32 + x) * 3
		return int(frame[offset]), int(frame[offset+1]), int(frame[offset+2])
	}
	if r, _, b := pixel(16, 10); r < 150 || b > 100 {
		t.Fatalf("top of the portrait still = rgb(%d,_,%d), want red — the EXIF turn is wrong", r, b)
	}
	if r, _, b := pixel(16, 54); b < 150 || r > 100 {
		t.Fatalf("bottom of the portrait still = rgb(%d,_,%d), want blue — the EXIF turn is wrong", r, b)
	}
}

func TestBuildCompositionRenderPlanTransitionVocabulary(t *testing.T) {
	wipe := &CompositionTransition{Kind: "wipeleft", Seconds: 1}
	clipA := renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 10, true), 0, 10, wipe)
	clipB := renderTestClip("c2", renderTestVideoRef("vid_b", "/w.mp4", 10, true), 0, 10, nil)
	doc := renderTestDoc(clipA, clipB)
	facts := renderTestFacts(clipA.Ref, clipB.Ref)

	plan, err := buildCompositionRenderPlan(doc, facts, compositionRenderOptions{})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	if !strings.Contains(plan.FilterComplex, "xfade=transition=wipeleft:duration=1:") {
		t.Fatalf("a named xfade kind must reach the graph verbatim: %s", plan.FilterComplex)
	}

	// An install lacking the name renders the junction as a plain dissolve
	// under one deduped notice; the audio branches are untouched.
	plan, err = buildCompositionRenderPlan(doc, facts, compositionRenderOptions{InstalledTransitions: []string{"fade"}})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan with a bounded install returned error: %v", err)
	}
	if strings.Contains(plan.FilterComplex, "wipeleft") || !strings.Contains(plan.FilterComplex, "xfade=transition=fade:duration=1:") {
		t.Fatalf("an unsupported kind must degrade to a plain dissolve: %s", plan.FilterComplex)
	}
	if len(plan.Notices) != 1 || !strings.Contains(plan.Notices[0], "wipeleft") {
		t.Fatalf("exactly one substitution notice naming the kind: %v", plan.Notices)
	}

	// "dissolve" remains the composer's word for xfade "fade".
	dissolveDoc := renderTestDoc(renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 10, true), 0, 10, &CompositionTransition{Kind: "dissolve", Seconds: 1}), renderTestClip("c2", renderTestVideoRef("vid_b", "/w.mp4", 10, true), 0, 10, nil))
	plan, err = buildCompositionRenderPlan(dissolveDoc, renderTestFacts(clipA.Ref, clipB.Ref), compositionRenderOptions{InstalledTransitions: []string{"fade"}})
	if err != nil {
		t.Fatalf("dissolve plan returned error: %v", err)
	}
	if !strings.Contains(plan.FilterComplex, "xfade=transition=fade:") || len(plan.Notices) != 0 {
		t.Fatalf("dissolve must render as fade without a notice: %s %v", plan.FilterComplex, plan.Notices)
	}
}
