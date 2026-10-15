package main

// Tests for the staged media upload lifecycle (media_upload.go): the
// begin/append/complete chunk API, the staged-path resolution every consumer
// shares, turn-persistence adoption (move into the conversation's artifacts
// dir), and the artifact-URL branches in the media decode/resolver seams.

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testVideoBytes = append([]byte{0x00, 0x00, 0x00, 0x18}, append([]byte("ftypmp42"), make([]byte, 64)...)...)
var testAudioBytes = append([]byte("ID3"), make([]byte, 64)...)

func newMediaUploadTestApp(t *testing.T) (*App, ConfigStorage) {
	t.Helper()
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	return &App{}, storage
}

func stageTestVideo(t *testing.T, app *App, storage ConfigStorage, data []byte, name string) (string, *StagedMediaUpload) {
	t.Helper()
	uploadID, err := app.beginMediaUpload(storage, name, "video")
	if err != nil {
		t.Fatalf("beginMediaUpload returned error: %v", err)
	}
	if err := app.AppendMediaUploadChunk(uploadID, base64.StdEncoding.EncodeToString(data)); err != nil {
		t.Fatalf("AppendMediaUploadChunk returned error: %v", err)
	}
	staged, err := app.completeMediaUpload(defaultAppConfig(), uploadID)
	if err != nil {
		t.Fatalf("completeMediaUpload returned error: %v", err)
	}
	if _, err := os.Stat(staged.Path); err != nil {
		t.Fatalf("staged file missing: %v", err)
	}
	_ = storage
	return uploadID, staged
}

func TestMediaUploadLifecycle(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	uploadID, err := app.beginMediaUpload(storage, "holiday.mov", "video")
	if err != nil {
		t.Fatalf("beginMediaUpload returned error: %v", err)
	}
	// Two chunks splitting a valid MP4 header across the boundary.
	first := append([]byte{}, testVideoBytes[:11]...)
	second := append([]byte{}, testVideoBytes[11:]...)
	if err := app.AppendMediaUploadChunk(uploadID, base64.StdEncoding.EncodeToString(first)); err != nil {
		t.Fatalf("first chunk returned error: %v", err)
	}
	if err := app.AppendMediaUploadChunk(uploadID, base64.StdEncoding.EncodeToString(second)); err != nil {
		t.Fatalf("second chunk returned error: %v", err)
	}
	staged, err := app.completeMediaUpload(defaultAppConfig(), uploadID)
	if err != nil {
		t.Fatalf("completeMediaUpload returned error: %v", err)
	}
	if got := filepath.Ext(staged.Path); got != ".mov" {
		t.Fatalf("extension = %q, want .mov (from the original name)", got)
	}
	if staged.Size != int64(len(first)+len(second)) {
		t.Fatalf("size = %d, want %d", staged.Size, len(first)+len(second))
	}
	if staged.URL != artifactPrefix+filepath.ToSlash(staged.Path) {
		t.Fatalf("url = %q, want the artifact-prefix form of %q", staged.URL, staged.Path)
	}
	if staged.MediaType != "video/quicktime" {
		t.Fatalf("mediaType = %q, want video/quicktime", staged.MediaType)
	}
	if !isVideoBytes(mustRead(t, staged.Path)) {
		t.Fatal("staged bytes do not sniff as video")
	}
	// The session is consumed: a second complete, or further appends, must fail.
	if _, err := app.completeMediaUpload(defaultAppConfig(), uploadID); err == nil {
		t.Fatal("second complete should have failed — the session was consumed")
	}
	if err := app.AppendMediaUploadChunk(uploadID, "AAAA"); err == nil {
		t.Fatal("append after complete should have failed — the session was consumed")
	}
}

func TestMediaUploadExtensionTable(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	cases := []struct {
		name string
		kind string
		want string
	}{
		{"holiday.mov", "video", ".mov"},
		{"holiday.mkv", "video", ".mkv"},
		{"no-extension", "video", ".mp4"},
		{"mislabeled.mp3", "video", ".mp4"},
		{"voice.wav", "audio", ".wav"},
		{"voice.flac", "audio", ".flac"},
		{"no-extension", "audio", ".mp3"},
		{"mislabeled.mp4", "audio", ".mp3"},
	}
	for _, tc := range cases {
		uploadID, err := app.beginMediaUpload(storage, tc.name, tc.kind)
		if err != nil {
			t.Fatalf("%s/%s: beginMediaUpload returned error: %v", tc.kind, tc.name, err)
		}
		app.mediaUploadsMu.Lock()
		session := app.mediaUploads[uploadID]
		app.mediaUploadsMu.Unlock()
		if got := filepath.Ext(session.path); got != tc.want {
			t.Fatalf("%s/%s: extension = %q, want %q", tc.kind, tc.name, got, tc.want)
		}
		os.Remove(session.path)
	}
}

func TestMediaUploadValidation(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	if _, err := app.beginMediaUpload(storage, "clip.mp4", "image"); err == nil {
		t.Fatal("kind \"image\" should be refused — only video/audio stage")
	}
	uploadID, err := app.beginMediaUpload(storage, "clip.mp4", "video")
	if err != nil {
		t.Fatalf("beginMediaUpload returned error: %v", err)
	}
	if err := app.AppendMediaUploadChunk("no-such-upload", "AAAA"); err == nil {
		t.Fatal("append with an unknown upload ID should have failed")
	}
	// Text bytes are not video: complete must refuse, and the session is consumed.
	if err := app.AppendMediaUploadChunk(uploadID, base64.StdEncoding.EncodeToString([]byte("this is not a video"))); err != nil {
		t.Fatalf("append returned error: %v", err)
	}
	if _, err := app.completeMediaUpload(defaultAppConfig(), uploadID); err == nil {
		t.Fatal("complete should have refused non-video bytes")
	}
	if err := app.AppendMediaUploadChunk(uploadID, "AAAA"); err == nil {
		t.Fatal("session should have been consumed by the failed complete")
	}
}

func TestStagedUploadPath(t *testing.T) {
	uploadsDir := filepath.Join(string(filepath.Separator), "Users", "someone", ".atelier", "history", "uploads")
	stagedPath := filepath.Join(uploadsDir, "up_abc123.mp4")
	url := artifactPrefix + filepath.ToSlash(stagedPath)
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"artifact URL", url, stagedPath},
		{"bare path", stagedPath, stagedPath},
		{"artifact URL in another conversation", artifactPrefix + "/Users/someone/.atelier/history/conversations/2026/10/conv_x/artifacts/vid_abc.mp4", ""},
		{"data URL", "data:video/mp4;base64,AAAA", ""},
		{"bare base64", "AAAA", ""},
		{"uploads dir itself", artifactPrefix + filepath.ToSlash(uploadsDir), ""},
		{"wrong prefix", artifactPrefix + filepath.ToSlash(filepath.Join(uploadsDir, "vid_abc.mp4")), ""},
		{"traversal", artifactPrefix + filepath.ToSlash(filepath.Join(uploadsDir, "..", "up_evil.mp4")), ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		if got, _ := stagedUploadPath(tc.payload, uploadsDir); got != tc.want {
			t.Fatalf("%s: stagedUploadPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHistoryContentAdoptsStagedVideoUpload(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	_, staged := stageTestVideo(t, app, storage, testVideoBytes, "clip.mp4")
	// A staged poster sibling (the Complete-time generation wrote one) moves
	// with the file instead of being regenerated.
	posterPath := stagedUploadPosterPath(staged.Path)
	if err := os.WriteFile(posterPath, []byte("poster-bytes"), 0644); err != nil {
		t.Fatalf("writing staged poster returned error: %v", err)
	}

	artifactsDir := filepath.Join(storage.History, "conversations", "2026", "10", "conv_x", "artifacts")
	contents, videoURLs, err := historyContentForMessage(ChatMessage{
		Role:    "user",
		Content: "Extract the audio mp3 from this.",
		Videos:  []string{staged.URL},
	}, artifactsDir, AppConfig{Storage: storage})
	if err != nil {
		t.Fatalf("historyContentForMessage returned error: %v", err)
	}
	if len(videoURLs) != 1 {
		t.Fatalf("videoURLs = %v, want one entry", videoURLs)
	}
	var videoContent *HistoryContent
	for i := range contents {
		if contents[i].Type == "video" {
			videoContent = &contents[i]
		}
	}
	if videoContent == nil {
		t.Fatalf("no video content in %+v", contents)
	}
	if !strings.HasPrefix(videoContent.ArtifactID, "vid_") {
		t.Fatalf("artifactID = %q, want a vid_ prefix", videoContent.ArtifactID)
	}
	if videoContent.MimeType != "video/mp4" {
		t.Fatalf("mimeType = %q, want video/mp4", videoContent.MimeType)
	}
	adoptedPath := filepath.Join(artifactsDir, filepath.Base(videoContent.Path))
	adoptedBytes := mustRead(t, adoptedPath)
	if string(adoptedBytes) != string(testVideoBytes) {
		t.Fatal("adopted artifact bytes differ from the staged bytes")
	}
	// The staged file and its poster REMAIN after adoption (link, not move):
	// the request's own payload keeps resolving for the rest of the turn, and
	// the age sweep reclaims the staged entry later.
	if _, err := os.Stat(staged.Path); err != nil {
		t.Fatalf("staged file should remain after adoption: %v", err)
	}
	if _, err := os.Stat(posterPath); err != nil {
		t.Fatalf("staged poster should remain after adoption: %v", err)
	}
	posterInfo, err := os.Stat(filepath.Join(artifactsDir, strings.TrimSuffix(filepath.Base(adoptedPath), ".mp4")+"_poster.jpg"))
	if err != nil {
		t.Fatalf("adopted poster missing: %v", err)
	}
	if posterInfo.Size() != int64(len("poster-bytes")) {
		t.Fatalf("poster size = %d, want the moved bytes", posterInfo.Size())
	}
	if videoURLs[0] != artifactPrefix+adoptedPath {
		t.Fatalf("videoURLs[0] = %q, want %q", videoURLs[0], artifactPrefix+adoptedPath)
	}
}

func TestHistoryContentAdoptsStagedAudioUpload(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	uploadID, err := app.beginMediaUpload(storage, "interview.wav", "audio")
	if err != nil {
		t.Fatalf("beginMediaUpload returned error: %v", err)
	}
	if err := app.AppendMediaUploadChunk(uploadID, base64.StdEncoding.EncodeToString(testAudioBytes)); err != nil {
		t.Fatalf("append returned error: %v", err)
	}
	staged, err := app.completeMediaUpload(defaultAppConfig(), uploadID)
	if err != nil {
		t.Fatalf("complete returned error: %v", err)
	}

	artifactsDir := filepath.Join(storage.History, "conversations", "2026", "10", "conv_x", "artifacts")
	contents, videoURLs, err := historyContentForMessage(ChatMessage{
		Role:    "user",
		Content: "Transcribe this.",
		Audios:  []string{staged.URL},
	}, artifactsDir, AppConfig{Storage: storage})
	if err != nil {
		t.Fatalf("historyContentForMessage returned error: %v", err)
	}
	if len(videoURLs) != 0 {
		t.Fatalf("videoURLs = %v, want none for an audio attachment", videoURLs)
	}
	var audioContent *HistoryContent
	for i := range contents {
		if contents[i].Type == "audio" {
			audioContent = &contents[i]
		}
	}
	if audioContent == nil {
		t.Fatalf("no audio content in %+v", contents)
	}
	if audioContent.MimeType != "audio/wav" {
		t.Fatalf("mimeType = %q, want audio/wav", audioContent.MimeType)
	}
	if string(mustRead(t, filepath.Join(artifactsDir, filepath.Base(audioContent.Path)))) != string(testAudioBytes) {
		t.Fatal("adopted audio bytes differ from the staged bytes")
	}
	if _, err := os.Stat(staged.Path); err != nil {
		t.Fatalf("staged audio should remain after adoption (link, not move): %v", err)
	}
}

func TestDiscardMediaUpload(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	uploadsDir := mediaUploadsDir(storage)

	// An in-flight session discards by upload ID, removing the partial file.
	uploadID, err := app.beginMediaUpload(storage, "clip.mp4", "video")
	if err != nil {
		t.Fatalf("beginMediaUpload returned error: %v", err)
	}
	app.mediaUploadsMu.Lock()
	partialPath := app.mediaUploads[uploadID].path
	app.mediaUploadsMu.Unlock()
	if err := app.discardMediaUpload(uploadsDir, uploadID); err != nil {
		t.Fatalf("discard by upload ID returned error: %v", err)
	}
	if _, err := os.Stat(partialPath); !os.IsNotExist(err) {
		t.Fatal("partial staged file should have been removed")
	}

	// A completed upload discards by path, poster sibling included.
	_, staged := stageTestVideo(t, app, storage, testVideoBytes, "clip.mp4")
	os.WriteFile(stagedUploadPosterPath(staged.Path), []byte("p"), 0644)
	if err := app.discardMediaUpload(uploadsDir, staged.Path); err != nil {
		t.Fatalf("discard by path returned error: %v", err)
	}
	if _, err := os.Stat(staged.Path); !os.IsNotExist(err) {
		t.Fatal("staged file should have been removed")
	}
	if _, err := os.Stat(stagedUploadPosterPath(staged.Path)); !os.IsNotExist(err) {
		t.Fatal("poster sibling should have been removed")
	}

	// Discard is idempotent — a second call on the same path is a no-op.
	if err := app.discardMediaUpload(uploadsDir, staged.Path); err != nil {
		t.Fatalf("second discard returned error: %v", err)
	}

	// Anything outside the uploads dir is refused — the frontend echoes back
	// values it was handed, but the boundary holds.
	if err := app.discardMediaUpload(uploadsDir, filepath.Join(storage.History, "conversations", "conv_x", "artifacts", "vid_abc.mp4")); err == nil {
		t.Fatal("discarding a conversation artifact should have been refused")
	}
	if err := app.discardMediaUpload(uploadsDir, "data:video/mp4;base64,AAAA"); err == nil {
		t.Fatal("discarding a data URL should have been refused")
	}
}

func TestSweepStaleMediaUploads(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	uploadsDir := mediaUploadsDir(storage)
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		t.Fatalf("mkdir returned error: %v", err)
	}
	freshPath := filepath.Join(uploadsDir, "up_fresh.mp4")
	stalePath := filepath.Join(uploadsDir, "up_stale.mp4")
	stalePosterPath := filepath.Join(uploadsDir, "up_stale_poster.jpg")
	for _, path := range []string{freshPath, stalePath, stalePosterPath} {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatalf("write %s returned error: %v", path, err)
		}
	}
	staleTime := time.Now().Add(-stagedUploadMaxAge - time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes returned error: %v", err)
	}
	if err := os.Chtimes(stalePosterPath, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes returned error: %v", err)
	}
	sweepStaleMediaUploads(uploadsDir)
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatal("fresh upload should have survived the sweep")
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatal("stale upload should have been swept")
	}
	if _, err := os.Stat(stalePosterPath); !os.IsNotExist(err) {
		t.Fatal("stale poster should have been swept")
	}
	_ = app
}

func TestDecodeMediaDataURLResolvesArtifact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, testVideoBytes, 0644); err != nil {
		t.Fatalf("write returned error: %v", err)
	}
	data, mediaType, err := decodeMediaDataURL(artifactPrefix + path)
	if err != nil {
		t.Fatalf("decodeMediaDataURL returned error: %v", err)
	}
	if string(data) != string(testVideoBytes) {
		t.Fatal("decoded bytes differ from the artifact file")
	}
	if mediaType != "video/mp4" {
		t.Fatalf("mediaType = %q, want video/mp4", mediaType)
	}
	if _, _, err := decodeMediaDataURL(artifactPrefix + filepath.Join(root, "..", "secret.mp4")); err == nil {
		t.Fatal("traversal artifact path should have been refused")
	}
	if _, _, err := decodeMediaDataURL("not-a-url"); err == nil {
		t.Fatal("non-URL payload should still be refused")
	}
}

func TestStageMediaDataURLStreamsArtifact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, testVideoBytes, 0644); err != nil {
		t.Fatalf("write returned error: %v", err)
	}
	stagingDir := t.TempDir()
	staged, err := stageMediaDataURL(stagingDir, "source", artifactPrefix+path)
	if err != nil {
		t.Fatalf("stageMediaDataURL returned error: %v", err)
	}
	if filepath.Ext(staged) != ".mp4" {
		t.Fatalf("staged extension = %q, want .mp4", staged)
	}
	if string(mustRead(t, staged)) != string(testVideoBytes) {
		t.Fatal("staged bytes differ from the artifact file")
	}
}

func TestFalResolveMediaURLWithArtifact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, testVideoBytes, 0644); err != nil {
		t.Fatalf("write returned error: %v", err)
	}
	client := FalClient{}
	// Small artifact: read + inline data URI, no HTTP involved.
	resolved, err := client.resolveMediaURL(testContext(), artifactPrefix+path, "video/mp4", "source-video.mp4")
	if err != nil {
		t.Fatalf("resolveMediaURL returned error: %v", err)
	}
	if !strings.HasPrefix(resolved, "data:video/mp4;base64,") {
		t.Fatalf("resolved = %q, want an inline data URI", resolved)
	}
	// Unreadable artifact: fail-soft passthrough of the original reference.
	missing, err := client.resolveMediaURL(testContext(), artifactPrefix+filepath.Join(root, "gone.mp4"), "video/mp4", "source-video.mp4")
	if err != nil {
		t.Fatalf("resolveMediaURL on a missing artifact returned error: %v", err)
	}
	if !strings.HasPrefix(missing, artifactPrefix+"/") {
		t.Fatalf("missing artifact should pass through unchanged, got %q", missing)
	}
}

func TestFalAudioVideoURLWrapArtifacts(t *testing.T) {
	root := t.TempDir()
	audioPath := filepath.Join(root, "voice.wav")
	videoPath := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(audioPath, testAudioBytes, 0644); err != nil {
		t.Fatalf("write returned error: %v", err)
	}
	if err := os.WriteFile(videoPath, testVideoBytes, 0644); err != nil {
		t.Fatalf("write returned error: %v", err)
	}
	audio := falAudioURL(artifactPrefix + audioPath)
	if !strings.HasPrefix(audio, "data:audio/wav;base64,") {
		t.Fatalf("falAudioURL = %q, want an inline data URI", audio[:40])
	}
	video := falVideoURL(artifactPrefix + videoPath)
	if !strings.HasPrefix(video, "data:video/mp4;base64,") {
		t.Fatalf("falVideoURL = %q, want an inline data URI", video[:40])
	}
	// Unreadable artifact: empty (the caller omits the field), not a broken URL.
	if got := falAudioURL(artifactPrefix + filepath.Join(root, "gone.wav")); got != "" {
		t.Fatalf("falAudioURL on a missing artifact = %q, want empty", got)
	}
}

func TestReplicateResolveMediaURLWithArtifact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "clip.mp4")
	if err := os.WriteFile(path, testVideoBytes, 0644); err != nil {
		t.Fatalf("write returned error: %v", err)
	}
	client := ReplicateClient{}
	resolved, err := client.ResolveMediaURL(testContext(), artifactPrefix+path, "video/mp4", "source-video-0.mp4")
	if err != nil {
		t.Fatalf("ResolveMediaURL returned error: %v", err)
	}
	if !strings.HasPrefix(resolved, "data:video/mp4;base64,") {
		t.Fatalf("resolved = %q, want an inline data URI", resolved[:40])
	}
	missing, err := client.ResolveMediaURL(testContext(), artifactPrefix+filepath.Join(root, "gone.mp4"), "video/mp4", "source-video-0.mp4")
	if err != nil {
		t.Fatalf("ResolveMediaURL on a missing artifact returned error: %v", err)
	}
	if !strings.HasPrefix(missing, artifactPrefix+"/") {
		t.Fatalf("missing artifact should pass through unchanged, got %q", missing)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s returned error: %v", path, err)
	}
	return data
}

func testContext() context.Context { return context.Background() }

func TestWritePendingChatConversationAdoptsStagedUpload(t *testing.T) {
	app, storage := newMediaUploadTestApp(t)
	_, staged := stageTestVideo(t, app, storage, testVideoBytes, "clip.mp4")

	config := defaultAppConfig()
	config.Storage = storage
	conversationID, videoURLs, _, _, err := writePendingChatConversation(config, ChatRequest{
		Model: "chat-model",
		Messages: []ChatMessage{
			{Role: "user", Content: "Extract the audio mp3 from this.", Videos: []string{staged.URL}},
		},
	})
	if err != nil {
		t.Fatalf("writePendingChatConversation returned error: %v", err)
	}
	if len(videoURLs) != 1 {
		t.Fatalf("videoURLs = %v, want the adopted artifact URL", videoURLs)
	}
	// The staged file REMAINS: adoption links the bytes into the conversation's
	// artifacts dir, and the request's own staged URL must keep resolving for
	// the tools that run after persistence (conv_15ecb089: a move stranded
	// extract_audio on the removed staged path).
	if _, err := os.Stat(staged.Path); err != nil {
		t.Fatalf("staged upload should remain resolvable after adoption: %v", err)
	}
	adoptedPath := strings.TrimPrefix(videoURLs[0], artifactPrefix)
	if !strings.Contains(adoptedPath, filepath.Join("conversations", "2026", "10", conversationID, "artifacts")) {
		t.Fatalf("adopted URL %q does not point at the conversation's artifacts dir", videoURLs[0])
	}
	if !isVideoBytes(mustRead(t, adoptedPath)) {
		t.Fatal("adopted artifact does not hold the video bytes")
	}
	// The tool-staging seam the planner's extract_audio call goes through
	// resolves the request's staged URL after the turn persisted.
	stagedSource, err := stageMediaDataURL(t.TempDir(), "source", staged.URL)
	if err != nil {
		t.Fatalf("stageMediaDataURL on the request payload returned error: %v", err)
	}
	if !isVideoBytes(mustRead(t, stagedSource)) {
		t.Fatal("staged source bytes do not sniff as video")
	}
}
