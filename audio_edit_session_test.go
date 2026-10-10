package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRealAudioEditorParent writes a chat conversation holding one user turn
// with one audio artifact (bytes copied from sourcePath) on disk, and returns
// the artifact id — the audio twin of writeRealVideoEditorParent.
func writeRealAudioEditorParent(t *testing.T, config AppConfig, id, sourcePath string) string {
	t.Helper()
	artifactID := randomID("aud")
	conversation := HistoryConversation{SchemaVersion: currentConversationSchemaVersion, ID: id,
		Kind: "chat", Title: "Audio editor verification", CreatedAt: "2026-10-10T10:00:00Z", UpdatedAt: "2026-10-10T10:00:00Z",
		Workspace: config.Tools.Filesystem.Root, Stats: HistoryConversationStats{TurnCount: 1, ArtifactCount: 1}}
	turn := HistoryTurn{SchemaVersion: 1, ID: "turn_000001", ConversationID: id, CreatedAt: conversation.CreatedAt,
		Kind: "chat", Role: "user", Content: []HistoryContent{{Type: "audio", ArtifactID: artifactID,
			Path: "artifacts/" + artifactID + ".m4a", MimeType: "audio/mp4"}}}
	writeSearchConversation(t, config.Storage, "2026/10/"+id, conversation, turn)
	dir := filepath.Join(config.Storage.History, "conversations", "2026", "10", id, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifactID+".m4a"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return artifactID
}

// audioSessionTestApp wires the submit/execute path against fake ffmpeg and
// ffprobe CLIs (the videoSessionTestApp pattern, audio-flavored): ffprobe
// reports a 10s aac source and a 5s render, and ffmpeg's behavior follows
// mode — success copies TRIMMED bytes out, fail injects a render failure,
// cancel stalls until the cancel context fires.
func audioSessionTestApp(t *testing.T, mode string) (*App, AppConfig, string, string, EditSourceInfo, AudioTrimParams) {
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
  case MODE in
    cancel) exec sleep 30 ;;
    fail) printf 'injected render failure\n' >&2; exit 1 ;;
    *) printf TRIMMED > "$out" ;;
  esac
fi
`
	ffmpegScript = strings.Replace(ffmpegScript, "MODE", mode, 1)
	ffprobeScript := `#!/bin/sh
last=""
for arg in "$@"; do last="$arg"; done
duration=10.0
if [ "$(cat "$last")" = "TRIMMED" ]; then duration=5.0; fi
cat <<JSON
{"format":{"duration":"$duration","format_name":"mov,mp4"},"streams":[{"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2}]}
JSON
`
	config.Providers.Local.FFmpeg.Binary = writeFakeWhisper(t, bin, "ffmpeg", ffmpegScript)
	config.Providers.Local.FFprobe.Binary = writeFakeWhisper(t, bin, "ffprobe", ffprobeScript)
	if err := writeAppConfig(config); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.m4a")
	if err := os.WriteFile(source, []byte("SOURCE"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := randomID("conv")
	artifact := writeRealAudioEditorParent(t, config, parent, source)
	app := editTestOfflineApp()
	t.Cleanup(func() { app.shutdown(context.Background()) })
	info, err := app.ResolveAudioEditSource(parent, artifact)
	if err != nil {
		t.Fatal(err)
	}
	params := AudioTrimParams{Version: 1, Source: AudioTrimSource{DurationSeconds: info.DurationSeconds},
		Segments: []AudioTrimSegment{{StartSeconds: 0, EndSeconds: 2}, {StartSeconds: 5, EndSeconds: 8}}}
	return app, config, parent, artifact, info, params
}

func TestAudioEditSessionCompletionAndAdoption(t *testing.T) {
	app, config, parent, artifact, info, params := audioSessionTestApp(t, "success")
	state, err := app.SubmitAudioEdit(AudioEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Trim: &params})
	if err != nil {
		t.Fatal(err)
	}
	if !state.CreatedSession || state.SourceURL == "" {
		t.Fatalf("session creation did not report its source: %+v", state)
	}
	op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
	if op.Status != editOperationStatusCompleted {
		t.Fatalf("render %s: %s", op.Status, op.Error)
	}
	if op.Backend != "ffmpeg" || op.Provider != "" || op.Model != "" || op.CostMicros != 0 || op.CostUnknown || op.AudioTrim == nil {
		t.Fatalf("incorrect local attribution/payload: %+v", op)
	}
	if absDurationDelta(op.ResultDurationSeconds, 5) > 1e-9 {
		t.Fatalf("result duration %g, want 5", op.ResultDurationSeconds)
	}
	if op.ResultMimeType != "audio/mp4" || !strings.HasPrefix(op.ResultArtifactID, "aud_") {
		t.Fatalf("result artifact not audio: %+v", op)
	}
	reopened, err := app.ListEditSession(state.SessionConversationID)
	if err != nil || reopened.Source.MediaKind != "audio" || len(reopened.Operations) != 1 || reopened.Operations[0].AudioTrim == nil {
		t.Fatalf("reopen failed: %+v, %v", reopened, err)
	}
	if _, err := app.AddEditResultToConversation(state.SessionConversationID, op.ID); err != nil {
		t.Fatal(err)
	}
	parentDetail, err := getConversation(config.Storage, parent)
	if err != nil {
		t.Fatal(err)
	}
	adoption := parentDetail.Turns[len(parentDetail.Turns)-1].ProviderResponse["editAdoption"].(map[string]any)
	if adoption["kind"] != editOperationKindAudioTrim || !strings.Contains(editOperationAdoptionSummary(op), "trim: 2 segment(s), 5.0s kept") {
		t.Fatalf("adoption lost audio provenance/summary: %v / %q", adoption["kind"], editOperationAdoptionSummary(op))
	}
	assets, err := listConversationAssets(config.Storage, parent)
	if err != nil || len(assets) != 2 || assets[1].Kind != "audio" {
		t.Fatalf("adopted audio missing: %+v, %v", assets, err)
	}
	// A deleted parent must not break the child: delete for real and reopen.
	if err := app.DeleteConversation(parent); err != nil {
		t.Fatal(err)
	}
	orphan, err := app.ListEditSession(state.SessionConversationID)
	if err != nil || orphan.ParentAvailable || orphan.Source.URL == "" {
		t.Fatalf("child lost independent source: %+v, %v", orphan, err)
	}
	if _, err := os.Stat(strings.TrimPrefix(orphan.Source.URL, artifactPrefix)); err != nil {
		t.Fatal(err)
	}
}

func TestAudioEditSessionCancelAndFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "fail"} {
		t.Run(mode, func(t *testing.T) {
			app, config, parent, artifact, info, params := audioSessionTestApp(t, mode)
			state, err := app.SubmitAudioEdit(AudioEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Trim: &params})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.SubmitAudioEdit(AudioEditSubmitRequest{SessionConversationID: state.SessionConversationID, Trim: &params}); err == nil {
				t.Fatal("accepted concurrent session render")
			}
			if mode == "cancel" {
				if err := app.CancelAudioEdit("unrelated", state.Operation.ID); err == nil {
					t.Fatal("cancel accepted wrong session ownership")
				}
				if err := app.CancelAudioEdit(state.SessionConversationID, state.Operation.ID); err != nil {
					t.Fatal(err)
				}
			}
			op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
			wantStatus := editOperationStatusCancelled
			if mode == "fail" {
				wantStatus = editOperationStatusFailed
			}
			if op.Status != wantStatus || op.Error == "" || op.ResultArtifactID != "" {
				t.Fatalf("invalid %s operation: %+v", mode, op)
			}
			assets, err := listConversationAssets(config.Storage, state.SessionConversationID)
			if err != nil || len(assets) != 1 {
				t.Fatalf("unrendered output became an asset: %+v, %v", assets, err)
			}
		})
	}
}

func TestAudioEditRejectsStaleSourceBeforeSessionCreation(t *testing.T) {
	app, config, parent, artifact, info, params := audioSessionTestApp(t, "success")
	if _, err := app.SubmitAudioEdit(AudioEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: "stale", Trim: &params}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("accepted stale source: %v", err)
	}
	bad := params
	bad.Segments = []AudioTrimSegment{{StartSeconds: 3, EndSeconds: 2}}
	if _, err := app.SubmitAudioEdit(AudioEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact, SourceDigest: info.SourceDigest, Trim: &bad}); err == nil {
		t.Fatal("accepted invalid cut set")
	}
	noTrim := AudioEditSubmitRequest{ParentConversationID: parent, SourceArtifactID: artifact}
	if _, err := app.SubmitAudioEdit(noTrim); err == nil {
		t.Fatal("accepted a submit without trim parameters")
	}
	conversations, err := listConversations(config.Storage)
	if err != nil || len(conversations) != 1 {
		t.Fatal("invalid submit created an edit session")
	}
}

func TestAudioEditSessionKindGuard(t *testing.T) {
	app, config, parent, _, info, _ := audioSessionTestApp(t, "success")
	if _, err := app.ResolveAudioEditInput(parent, info.ArtifactID); err == nil || !strings.Contains(err.Error(), "not an audio edit session") {
		t.Fatalf("ResolveAudioEditInput accepted a chat conversation: %v", err)
	}
	detail, err := getConversation(config.Storage, parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := findAudioContent(detail, info.ArtifactID); !ok {
		t.Fatal("findAudioContent missed the parent artifact")
	}
}
