package main

// ATELIER_TEST_FFMPEG=1 enables the real editor pipeline, with all history and
// config confined to test temp directories and network calls forbidden.

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRealVideoEditorParent(t *testing.T, config AppConfig, id, sourcePath string) string {
	t.Helper()
	artifactID := randomID("vid")
	conversation := HistoryConversation{SchemaVersion: currentConversationSchemaVersion, ID: id,
		Kind: "chat", Title: "Video editor verification", CreatedAt: "2026-10-08T10:00:00Z", UpdatedAt: "2026-10-08T10:00:00Z",
		Workspace: config.Tools.Filesystem.Root, Stats: HistoryConversationStats{TurnCount: 1, ArtifactCount: 1}}
	turn := HistoryTurn{SchemaVersion: 1, ID: "turn_000001", ConversationID: id, CreatedAt: conversation.CreatedAt,
		Kind: "chat", Role: "user", Content: []HistoryContent{{Type: "video", ArtifactID: artifactID,
			Path: "artifacts/" + artifactID + ".mp4", MimeType: "video/mp4"}}}
	writeSearchConversation(t, config.Storage, "2026/10/"+id, conversation, turn)
	dir := filepath.Join(config.Storage.History, "conversations", "2026", "10", id, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, artifactID+".mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return artifactID
}

func TestVideoEditorRealPipeline(t *testing.T) {
	if os.Getenv("ATELIER_TEST_FFMPEG") != "1" {
		t.Skip("set ATELIER_TEST_FFMPEG=1 to verify real editor normalization/render/persistence")
	}
	withRealLocalLookup(t)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name                       string
		audio, rotated, anamorphic bool
		early, vfr                 bool
	}{
		{"silent", false, false, false, false, false},
		{"delayed-audio", true, false, false, false, false},
		{"early-audio", true, false, false, true, false},
		{"variable-frame-rate", true, false, false, false, true},
		{"rotated", false, true, false, false, false},
		{"anamorphic", false, false, true, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			config := editTestHome(t)
			config.Providers.Local.FFmpeg.Binary = ffmpeg
			config.Providers.Local.FFprobe.Binary = ffprobe
			if err := writeAppConfig(config); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			input := filepath.Join(dir, "source.mp4")
			args := []string{"-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=s=160x96:r=20:d=4"}
			if scenario.audio {
				args = append(args, "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=3.8")
			}
			filter := "setpts=PTS+5/TB"
			if scenario.vfr {
				filter = "select='not(eq(mod(n,3),1))'," + filter
			}
			if scenario.anamorphic {
				filter += ",setsar=4/3"
			}
			args = append(args, "-vf", filter, "-fps_mode", "passthrough", "-c:v", "libx264", "-pix_fmt", "yuv420p")
			if scenario.audio {
				audioOrigin := "5.2"
				if scenario.early {
					audioOrigin = "4.8"
				}
				args = append(args, "-af", "asetpts=PTS+"+audioOrigin+"/TB", "-c:a", "aac")
			}
			args = append(args, "-copyts", input)
			videoReframeRealCommand(t, ffmpeg, args...)
			if scenario.rotated {
				rotated := filepath.Join(dir, "rotated.mp4")
				videoReframeRealCommand(t, ffmpeg, "-v", "error", "-display_rotation:v:0", "90", "-i", input, "-c", "copy", rotated)
				input = rotated
			}
			parentID := randomID("conv")
			artifactID := writeRealVideoEditorParent(t, config, parentID, input)
			app := editTestOfflineApp()
			defer app.shutdown(t.Context())
			info, err := app.ResolveVideoEditSource(parentID, artifactID)
			if err != nil {
				t.Fatal(err)
			}
			defer app.ReleaseVideoEditSourcePreview(info.URL)
			if info.MediaKind != "video" || info.Width <= 0 || info.Height <= 0 || info.DurationSeconds <= 0 || len(info.Thumbnails) > 30 {
				t.Fatalf("invalid editor source: %+v", info)
			}
			previewProbe := videoReframeProbeFile(t, ffprobe, strings.TrimPrefix(info.URL, artifactPrefix), true)
			if first := videoReframeTestSeconds(t, previewProbe.Frames[0].Timestamp); math.Abs(first) > 0.001 {
				t.Fatalf("preview video did not start at zero: %g", first)
			}
			if scenario.rotated && info.Height <= info.Width {
				t.Fatalf("rotation not baked in preview: %dx%d", info.Width, info.Height)
			}
			if scenario.anamorphic && math.Abs(float64(info.Width)/float64(info.Height)-20.0/9) > 0.03 {
				t.Fatalf("anamorphic aspect not corrected: %dx%d", info.Width, info.Height)
			}
			before, err := listConversations(config.Storage)
			if err != nil || len(before) != 1 {
				t.Fatalf("opening created a saved draft: %d, %v", len(before), err)
			}
			crop, err := fitVideoReframeCrop(info.Width, info.Height, "9:16")
			if err != nil {
				t.Fatal(err)
			}
			left, right := crop, crop
			left.X = 0
			right.X = info.Width - crop.Width
			params := VideoReframeParams{Version: 1, Source: VideoReframeSource{Width: info.Width, Height: info.Height, DurationSeconds: info.DurationSeconds},
				AspectRatio: "9:16", Output: VideoReframeOutput{Width: crop.Width, Height: crop.Height}, Markers: []VideoReframeMarker{
					{VideoReframeRect: left, TimeSeconds: 0, InterpolationToNext: "smooth"},
					{VideoReframeRect: right, TimeSeconds: 2, InterpolationToNext: "hold"},
				}}
			state, err := app.SubmitVideoEdit(VideoEditSubmitRequest{ParentConversationID: parentID, SourceArtifactID: artifactID, SourceDigest: info.SourceDigest, Reframe: &params})
			if err != nil {
				t.Fatal(err)
			}
			op := waitForEditTerminal(t, config.Storage, state.SessionConversationID, state.Operation.ID)
			if op.Status != editOperationStatusCompleted {
				t.Fatalf("render %s: %s", op.Status, op.Error)
			}
			deadline := time.Now().Add(3 * time.Second)
			for app.editOpRunning(state.SessionConversationID) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if op.Backend != "ffmpeg" || op.Provider != "" || op.Model != "" || op.CostMicros != 0 || op.CostUnknown || op.Reframe == nil {
				t.Fatalf("incorrect local attribution/instructions: %+v", op)
			}
			reopened, err := app.ListEditSession(state.SessionConversationID)
			if err != nil || reopened.Source.MediaKind != "video" || len(reopened.Operations) != 1 {
				t.Fatalf("reopen failed: %+v, %v", reopened, err)
			}
			resultSource, err := app.ResolveVideoEditInput(state.SessionConversationID, op.ResultArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			defer app.ReleaseVideoEditSourcePreview(resultSource.URL)
			if resultSource.Width != crop.Width || resultSource.Height != crop.Height {
				t.Fatalf("output dimensions %dx%d, want %dx%d", resultSource.Width, resultSource.Height, crop.Width, crop.Height)
			}
			if math.Abs(resultSource.DurationSeconds-info.DurationSeconds) > 0.1 {
				t.Fatalf("duration changed %.4f → %.4f", info.DurationSeconds, resultSource.DurationSeconds)
			}
			probe := videoReframeProbeFile(t, ffprobe, strings.TrimPrefix(resultSource.URL, artifactPrefix), false)
			if scenario.audio {
				found := false
				for _, stream := range probe.Streams {
					if stream.CodecType == "audio" {
						found = true
						expectedOffset := 0.2
						if scenario.early {
							expectedOffset = 0
						}
						if offset := videoReframeTestSeconds(t, stream.StartTime); math.Abs(offset-expectedOffset) > 0.1 {
							t.Fatalf("intentional audio delay lost: %.4f", offset)
						}
					}
				}
				if !found {
					t.Fatal("audio track lost")
				}
			}
			if _, err := app.AddEditResultToConversation(state.SessionConversationID, op.ID); err != nil {
				t.Fatal(err)
			}
			parentDetail, err := getConversation(config.Storage, parentID)
			if err != nil {
				t.Fatal(err)
			}
			adoption := parentDetail.Turns[len(parentDetail.Turns)-1].ProviderResponse["editAdoption"].(map[string]any)
			if adoption["sessionId"] != state.SessionConversationID {
				t.Fatal("adoption lost child-session provenance")
			}
			if _, err := app.AddEditResultToConversation(state.SessionConversationID, op.ID); err == nil {
				t.Fatal("duplicate adoption accepted")
			}
			assets, err := listConversationAssets(config.Storage, parentID)
			if err != nil || len(assets) != 2 || assets[1].Kind != "video" {
				t.Fatalf("adopted video missing: %+v, %v", assets, err)
			}
			if err := app.DeleteConversation(parentID); err != nil {
				t.Fatal(err)
			}
			orphan, err := app.ListEditSession(state.SessionConversationID)
			if err != nil || orphan.ParentAvailable || orphan.Source.URL == "" {
				t.Fatalf("child lost independent source: %+v, %v", orphan, err)
			}
			if _, err := os.Stat(strings.TrimPrefix(orphan.Source.URL, artifactPrefix)); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: preview %dx%d, output %dx%d, duration %.3fs; session/reopen/adoption verified", scenario.name, info.Width, info.Height, crop.Width, crop.Height, resultSource.DurationSeconds)
		})
	}
}
