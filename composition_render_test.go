package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestBuildCompositionRenderPlanSingleClip(t *testing.T) {
	clip := renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 10, true), 0, 10, nil)
	plan, err := buildCompositionRenderPlan(renderTestDoc(clip), renderTestFacts(clip.Ref), compositionRenderOptions{})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	want := "[0:v]trim=start=0:end=10,setpts=PTS-STARTPTS," +
		"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p[v0];" +
		"[v0]null[vout];" +
		"[0:a]atrim=start=0:end=10,asetpts=PTS-STARTPTS,aformat=sample_rates=48000:channel_layouts=stereo[ac0];" +
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
		"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p[v0];" +
		"[1:v]trim=start=0:end=3,setpts=PTS-STARTPTS," +
		"scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=30,format=yuv420p[v1];" +
		"[v0][v1]xfade=transition=fade:duration=1:offset=3[vx0];" +
		"[vx0]null[vout];" +
		"[0:a]atrim=start=0:end=4,asetpts=PTS-STARTPTS,aformat=sample_rates=48000:channel_layouts=stereo[ac0];" +
		"[2:a]aformat=sample_rates=48000:channel_layouts=stereo[ac1];" +
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
	op, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	terminal := waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "completed" {
		t.Fatalf("terminal = %+v, want completed", terminal)
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
	op, err = app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc})
	if err != nil {
		t.Fatalf("SubmitCompositionRender returned error: %v", err)
	}
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &seed.Doc}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a concurrent render must refuse, got %v", err)
	}
	waitCompositionRenderRunning(t, app, op.ID)
	if err := app.CancelCompositionRender(op.ID); err != nil {
		t.Fatalf("CancelCompositionRender returned error: %v", err)
	}
	terminal = waitCompositionRenderTerminal(t, app, op.ID)
	if terminal.Status != "cancelled" || terminal.ResultURL != "" {
		t.Fatalf("cancelled op = %+v", terminal)
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
		{"lanes with ducks", withLanes(), compositionRenderOptions{}, true, 0, true},
		{"audio only", CompositionDoc{SchemaVersion: compositionSchemaVersion, Name: "Test", Output: CompositionOutput{Fit: compositionFitPad}, AudioLanes: []CompositionLane{musicLane}}, compositionRenderOptions{}, true, 0, true},
		{"range preview", cutWithSilent(), compositionRenderOptions{StartSeconds: &one, EndSeconds: &four}, true, 0, true},
		{"export muted", dissolveDoc(), compositionRenderOptions{MuteAudio: true}, false, 20, false},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			plan, err := buildCompositionRenderPlan(scenario.doc, facts, scenario.opts)
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
}
