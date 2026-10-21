package main

import (
	"fmt"
	"os"
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
		"[v0]anull[vout];" +
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
		"[vx0]anull[vout];" +
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
	args := compositionFFmpegArgs(plan, compositionEncodeArgs(true, plan.AudioOnly), "/out.m4a")
	if !strings.Contains(strings.Join(args, " "), "-map [aout]") || strings.Contains(strings.Join(args, " "), "[vout]") {
		t.Fatalf("audio-only args = %v", args)
	}

	if _, err := buildCompositionRenderPlan(renderTestDoc(), map[string]joinClipFact{}, compositionRenderOptions{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty composition must refuse, got %v", err)
	}
}

func TestCompositionFFmpegArgsShape(t *testing.T) {
	clip := renderTestClip("c1", renderTestVideoRef("vid_a", "/v.mp4", 10, true), 0, 10, nil)
	plan, err := buildCompositionRenderPlan(renderTestDoc(clip), renderTestFacts(clip.Ref), compositionRenderOptions{})
	if err != nil {
		t.Fatalf("buildCompositionRenderPlan returned error: %v", err)
	}
	args := compositionFFmpegArgs(plan, compositionEncodeArgs(true, false), "/out.mp4")
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
printf '%%s\\n' "$@" >> "$dir/args.txt"
out=""
mode="%s"
for a in "$@"; do out="$a"; done
case "$mode" in
  fail) echo "fake render failure" >&2; exit 1 ;;
  sleep) sleep 30; exit 0 ;;
esac
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

	// Guard rails first: an unknown mode, an empty request, and an empty
	// document.
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "export", Doc: &CompositionDoc{}}); err == nil || !strings.Contains(err.Error(), "preview") {
		t.Fatalf("the export mode must refuse until its milestone, got %v", err)
	}
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Mode: "preview"}); err == nil || !strings.Contains(err.Error(), "saved revision id or the working document") {
		t.Fatalf("a render with neither doc nor artifact must refuse, got %v", err)
	}
	if _, err := app.SubmitCompositionRender(CompositionRenderRequest{ConversationID: "conv_comp", Doc: &CompositionDoc{}}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty document must refuse, got %v", err)
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
