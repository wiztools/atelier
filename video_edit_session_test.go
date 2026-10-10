package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func videoSessionTestApp(t *testing.T, mode string) (*App, AppConfig, string, string, EditSourceInfo, VideoReframeParams) {
	t.Helper()
	withRealLocalLookup(t)
	config := editTestHome(t)
	bin := t.TempDir()
	ffmpegScript := `#!/bin/sh
out=""
render=0
for arg in "$@"; do
  out="$arg"
  if [ "$arg" = "-progress" ]; then render=1; fi
done
if [ "$render" = 1 ]; then
  printf 'out_time_ms=1000000\nprogress=continue\n'
  printf PARTIAL > "$out"
  case MODE in
    cancel) exec sleep 30 ;;
    fail) printf 'injected render failure\n' >&2; exit 1 ;;
    invalid) printf INVALID > "$out" ;;
    *) printf REFRAMED > "$out" ;;
  esac
else
  printf THUMB > "$out"
fi
`
	ffmpegScript = strings.Replace(ffmpegScript, "MODE", mode, 1)
	ffprobeScript := `#!/bin/sh
last=""
for arg in "$@"; do last="$arg"; done
w=160
if [ "$(cat "$last")" = "REFRAMED" ]; then w=54; fi
cat <<JSON
{"format":{"duration":"4.0","format_name":"mov,mp4"},"streams":[{"codec_type":"video","codec_name":"h264","width":$w,"height":96,"start_time":"0.0","sample_aspect_ratio":"1:1","avg_frame_rate":"20/1"}]}
JSON
`
	config.Providers.Local.FFmpeg.Binary = writeFakeWhisper(t, bin, "ffmpeg", ffmpegScript)
	config.Providers.Local.FFprobe.Binary = writeFakeWhisper(t, bin, "ffprobe", ffprobeScript)
	if err := writeAppConfig(config); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(source, []byte("SOURCE"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := randomID("conv")
	artifact := writeRealVideoEditorParent(t, config, parent, source)
	app := editTestOfflineApp()
	t.Cleanup(func() { app.shutdown(context.Background()) })
	info, err := app.ResolveVideoEditSource(parent, artifact)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = app.ReleaseVideoEditSourcePreview(info.URL)
		for _, url := range info.Thumbnails {
			_ = app.ReleaseVideoEditSourcePreview(url)
		}
	})
	crop, err := fitVideoReframeCrop(info.Width, info.Height, "9:16")
	if err != nil {
		t.Fatal(err)
	}
	params := VideoReframeParams{Version: 1, Source: VideoReframeSource{Width: info.Width, Height: info.Height, DurationSeconds: info.DurationSeconds},
		AspectRatio: "9:16", Output: VideoReframeOutput{Width: crop.Width, Height: crop.Height},
		Markers: []VideoReframeMarker{{VideoReframeRect: crop, TimeSeconds: 0, InterpolationToNext: "smooth"}}}
	return app, config, parent, artifact, info, params
}

func TestVideoEditSessionCancelAndConcurrency(t *testing.T) {
	app, config, parent, artifact, info, params := videoSessionTestApp(t, "cancel")
	state, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Reframe: &params})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SubmitVideoEdit(VideoEditSubmitRequest{SessionConversationID: state.SessionConversationID, Reframe: &params}); err == nil {
		t.Fatal("accepted concurrent session render")
	}
	if err := app.DeleteConversation(state.SessionConversationID); err == nil {
		t.Fatal("allowed deletion during render")
	}
	if err := app.CancelVideoEdit("unrelated", state.Operation.ID); err == nil {
		t.Fatal("cancel accepted wrong session ownership")
	}
	if err := app.CancelVideoEdit(state.SessionConversationID, state.Operation.ID); err != nil {
		t.Fatal(err)
	}
	op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
	if op.Status != editOperationStatusCancelled || op.ResultArtifactID != "" {
		t.Fatalf("cancelled job has result: %+v", op)
	}
	assets, err := listConversationAssets(config.Storage, state.SessionConversationID)
	if err != nil || len(assets) != 1 {
		t.Fatalf("partial render became an asset: %+v, %v", assets, err)
	}
}

func TestVideoEditSessionFailureDoesNotPublishResult(t *testing.T) {
	for _, mode := range []string{"fail", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			app, config, parent, artifact, info, params := videoSessionTestApp(t, mode)
			state, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Reframe: &params})
			if err != nil {
				t.Fatal(err)
			}
			op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
			if op.Status != editOperationStatusFailed || op.Error == "" || op.ResultArtifactID != "" {
				t.Fatalf("invalid failed operation: %+v", op)
			}
			assets, err := listConversationAssets(config.Storage, state.SessionConversationID)
			if err != nil || len(assets) != 1 {
				t.Fatal("failed output was published")
			}
		})
	}
}

func TestVideoEditRejectsStaleSourceBeforeSessionCreation(t *testing.T) {
	app, config, parent, artifact, info, params := videoSessionTestApp(t, "success")
	_, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: "stale", Reframe: &params})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("accepted stale source: %v", err)
	}
	params.Markers[0].X = -1
	if _, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Reframe: &params}); err == nil {
		t.Fatal("accepted invalid framing")
	}
	conversations, err := listConversations(config.Storage)
	if err != nil || len(conversations) != 1 {
		t.Fatal("invalid submit created an edit session")
	}
	if err := app.ReleaseVideoEditSourcePreview(info.URL); err != nil {
		t.Fatal(err)
	}
	for _, url := range info.Thumbnails {
		if err := app.ReleaseVideoEditSourcePreview(url); err != nil {
			t.Fatal(err)
		}
	}
	for _, url := range append([]string{info.URL}, info.Thumbnails...) {
		if _, err := os.Stat(strings.TrimPrefix(url, artifactPrefix)); !os.IsNotExist(err) {
			t.Fatalf("preview resource leaked: %s, %v", url, err)
		}
	}
}

// TestVideoEditSourceReleaseIsFileScoped pins the release contract: one
// released URL deletes exactly its own file. A live editor may still be
// rendering the generation's siblings (React's dev StrictMode unmount replay
// fires releases while the component survives), so the old whole-generation
// cascade must not come back.
func TestVideoEditSourceReleaseIsFileScoped(t *testing.T) {
	app, _, _, _, info, _ := videoSessionTestApp(t, "success")
	if err := app.ReleaseVideoEditSourcePreview(info.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimPrefix(info.URL, artifactPrefix)); !os.IsNotExist(err) {
		t.Fatalf("released preview survived: %v", err)
	}
	if len(info.Thumbnails) == 0 {
		t.Fatal("resolve produced no thumbnails to scope-test")
	}
	for _, url := range info.Thumbnails {
		if _, err := os.Stat(strings.TrimPrefix(url, artifactPrefix)); err != nil {
			t.Fatalf("sibling tile was cascaded away: %s, %v", url, err)
		}
		if err := app.ReleaseVideoEditSourcePreview(url); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(strings.TrimPrefix(url, artifactPrefix)); !os.IsNotExist(err) {
			t.Fatalf("released tile survived: %s, %v", url, err)
		}
	}
}

// TestVideoTrimSubmitGuards covers the trim kind's submit-side contract with
// the fake ffmpeg harness: exactly one payload, the stale-source guard, and a
// queued trim that cancels like a reframe render.
func TestVideoTrimSubmitGuards(t *testing.T) {
	app, config, parent, artifact, info, params := videoSessionTestApp(t, "cancel")
	trim := func(start, end float64, sourceWidth, sourceHeight int, duration float64) *VideoTrimParams {
		return &VideoTrimParams{
			Version: 1,
			Source:  VideoReframeSource{Width: sourceWidth, Height: sourceHeight, DurationSeconds: duration},
			Segments: []VideoTrimSegment{
				{StartSeconds: start, EndSeconds: end},
			},
		}
	}
	if _, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, Reframe: &params, Trim: trim(0, 1, info.Width, info.Height, info.DurationSeconds)}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("accepted both payloads: %v", err)
	}
	stale := trim(0, 1, info.Width+2, info.Height, info.DurationSeconds)
	if _, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Trim: stale}); err == nil || !strings.Contains(err.Error(), "trim targets") {
		t.Fatalf("accepted stale trim source: %v", err)
	}
	if _, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Trim: trim(0, 9, info.Width, info.Height, info.DurationSeconds)}); err == nil {
		t.Fatal("accepted out-of-range trim segment")
	}
	state, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Trim: trim(0.5, 1.5, info.Width, info.Height, info.DurationSeconds)})
	if err != nil {
		t.Fatal(err)
	}
	if state.Operation.Kind != editOperationKindTrim || state.Operation.Trim == nil || state.Operation.Reframe != nil || state.Operation.Backend != "ffmpeg" {
		t.Fatalf("incorrect trim operation attribution: %+v", state.Operation)
	}
	if err := app.CancelVideoEdit(state.SessionConversationID, state.Operation.ID); err != nil {
		t.Fatal(err)
	}
	op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
	if op.Status != editOperationStatusCancelled {
		t.Fatalf("trim render status %s: %s", op.Status, op.Error)
	}
}

func TestVideoEditProgressRunnerCancellation(t *testing.T) {
	app, config, _, _, _, _ := videoSessionTestApp(t, "cancel")
	_ = app
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	updates := 0
	err := runLocalFFmpegProgress(ctx, config, 4, []string{"-nostdin", "-i", "unused", filepath.Join(t.TempDir(), "partial.mp4")}, func(progress float64) {
		updates++
		if progress >= 1 {
			t.Error("reported completion before persistence")
		}
		cancel()
	})
	if err == nil || updates == 0 || time.Since(start) > 3*time.Second {
		t.Fatalf("runner did not promptly cancel: updates=%d, err=%v", updates, err)
	}
}

func TestVideoEditThumbnailURLsPersistBesideSource(t *testing.T) {
	withRealLocalLookup(t)
	config := editTestHome(t)
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "thumb-calls")
	t.Setenv("THUMB_CALLS", calls)
	ffmpeg := writeFakeWhisper(t, bin, "ffmpeg", `#!/bin/sh
echo x >> "$THUMB_CALLS"
out=""
for arg in "$@"; do out="$arg"; done
printf THUMB > "$out"
`)
	config.Providers.Local.FFmpeg.Binary = ffmpeg
	if err := writeAppConfig(config); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "vid_test.mp4")
	if err := os.WriteFile(source, []byte("SOURCE"), 0o644); err != nil {
		t.Fatal(err)
	}

	generated := videoEditThumbnailURLs(context.Background(), config, source, 4, 12)
	if len(generated) != 4 {
		t.Fatalf("short strip: %v", generated)
	}
	for i, url := range generated {
		want := filepath.Join(dir, fmt.Sprintf("vid_test_thumb_%02d.jpg", i+1))
		if url != artifactPrefix+want {
			t.Fatalf("tile %d named %q, want %q", i, url, want)
		}
		if info, err := os.Stat(want); err != nil || info.Size() == 0 {
			t.Fatalf("tile %d missing on disk: %v", i+1, err)
		}
	}

	// A repeat resolve is a stat check: even a broken ffmpeg cannot hurt it.
	config.Providers.Local.FFmpeg.Binary = filepath.Join(bin, "missing-ffmpeg")
	cached := videoEditThumbnailURLs(context.Background(), config, source, 4, 12)
	if strings.Join(cached, "\n") != strings.Join(generated, "\n") {
		t.Fatalf("cache hit changed the strip: %v", cached)
	}

	// Missing and zero-byte tiles regenerate; present ones are not re-spawned.
	config.Providers.Local.FFmpeg.Binary = ffmpeg
	if err := os.Remove(filepath.Join(dir, "vid_test_thumb_02.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vid_test_thumb_03.jpg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(calls)
	if resumed := videoEditThumbnailURLs(context.Background(), config, source, 4, 12); len(resumed) != 4 {
		t.Fatalf("resumed strip short: %v", resumed)
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(invocations), "x"); got != 2 {
		t.Fatalf("resume regenerated %d tiles, want 2", got)
	}

	// A stray tile from an earlier wider strip is pruned.
	stray := filepath.Join(dir, "vid_test_thumb_09.jpg")
	if err := os.WriteFile(stray, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	videoEditThumbnailURLs(context.Background(), config, source, 4, 12)
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stale tile survived: %v", err)
	}

	for _, invalid := range [][2]float64{{0, 12}, {4, 0}, {-1, -1}} {
		if got := videoEditThumbnailURLs(context.Background(), config, source, invalid[0], int(invalid[1])); got != nil {
			t.Fatalf("invalid input %v produced %v", invalid, got)
		}
	}
}
