package main

import (
	"context"
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
	t.Cleanup(func() { _ = app.ReleaseVideoEditSourcePreview(info.URL) })
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
	for _, url := range append([]string{info.URL}, info.Thumbnails...) {
		if _, err := os.Stat(strings.TrimPrefix(url, artifactPrefix)); !os.IsNotExist(err) {
			t.Fatalf("preview resource leaked: %s, %v", url, err)
		}
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
