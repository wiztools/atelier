package main

// Tests for video_poster.go: the poster-path convention, the ffmpeg poster
// args (seek + width cap + anamorphic resample), the seek clamps, the
// qlmanage PNG→JPEG bridge, and the extractor orchestration through the
// localBinaryLookPath seam TestMain pins package-wide (fake shell scripts,
// the model_image_compat.go pattern), so outcomes never depend on the host
// machine's installed tools.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestVideoPosterPath(t *testing.T) {
	cases := []struct{ video, want string }{
		{"/hist/conv/artifacts/vid_ab12.mp4", "/hist/conv/artifacts/vid_ab12_poster.jpg"},
		{"/hist/conv/artifacts/vid_ab12.webm", "/hist/conv/artifacts/vid_ab12_poster.jpg"},
		{"/hist/conv/artifacts/vid_ab12.mov", "/hist/conv/artifacts/vid_ab12_poster.jpg"},
		{"vid_abe.mp4", "vid_abe_poster.jpg"},
	}
	for _, tc := range cases {
		if got := videoPosterPath(tc.video); got != tc.want {
			t.Errorf("videoPosterPath(%q) = %q, want %q", tc.video, got, tc.want)
		}
	}
}

func TestFFmpegPosterArgs(t *testing.T) {
	// Known display size: resampled to ≤512 at the DISPLAY aspect with SAR
	// reset — the anamorphic unsqueeze and the width cap in one filter.
	wide := ffmpegPosterArgs("/in.mp4", "0.500", "/out.jpg", 1920, 1080)
	wantWide := []string{"-ss", "0.500", "-i", "/in.mp4", "-vf", "scale=512:288,setsar=1", "-frames:v", "1", "-q:v", "3", "/out.jpg"}
	if !slices.Equal(wide, wantWide) {
		t.Errorf("ffmpegPosterArgs(1920x1080) = %v, want %v", wide, wantWide)
	}
	// Anamorphic storage 960x720 shown as 1280x720: the poster is sized from
	// the DISPLAY frame, not the squeezed storage frame.
	anamorphic := ffmpegPosterArgs("/in.mp4", "0.500", "/out.jpg", 1280, 720)
	if vf := anamorphic[5]; vf != "scale=512:288,setsar=1" {
		t.Errorf("ffmpegPosterArgs anamorphic vf = %q, want scale=512:288,setsar=1", vf)
	}
	// Below the cap: passthrough size, no upscale.
	small := ffmpegPosterArgs("/in.mp4", "0.500", "/out.jpg", 400, 300)
	if vf := small[5]; vf != "scale=400:300,setsar=1" {
		t.Errorf("ffmpegPosterArgs small vf = %q, want scale=400:300,setsar=1", vf)
	}
	// Unknown dimensions (webm, unparseable container): container-blind cap.
	blind := ffmpegPosterArgs("/in.webm", "1.000", "/out.jpg", 0, 0)
	wantBlind := []string{"-ss", "1.000", "-i", "/in.webm", "-vf", "scale='min(512,iw)':-2", "-frames:v", "1", "-q:v", "3", "/out.jpg"}
	if !slices.Equal(blind, wantBlind) {
		t.Errorf("ffmpegPosterArgs(no dims) = %v, want %v", blind, wantBlind)
	}
}

func TestVideoPosterTargetSize(t *testing.T) {
	cases := []struct {
		width, height int
		wantW, wantH  int
	}{
		{1920, 1080, 512, 288},
		{1280, 720, 512, 288},
		{400, 300, 400, 300},
		{1080, 1920, 512, 910},
		{512, 512, 512, 512},
		{501, 2, 501, 2},
		// Aspect-preserving rounding that lands between pixels rounds to
		// nearest rather than truncating.
		{600, 899, 512, 767},
	}
	for _, tc := range cases {
		gotW, gotH := videoPosterTargetSize(tc.width, tc.height)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("videoPosterTargetSize(%d,%d) = (%d,%d), want (%d,%d)", tc.width, tc.height, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
	if w, h := videoPosterTargetSize(0, 100); w != 0 || h != 0 {
		t.Errorf("videoPosterTargetSize(0,100) = (%d,%d), want (0,0)", w, h)
	}
}

func TestClampVideoPosterSeekSeconds(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0.004, 0.05},
		{0.05, 0.05},
		{1.0, 1.0},
		{45.0, 30.0},
	}
	for _, tc := range cases {
		if got := clampVideoPosterSeekSeconds(tc.in); got != tc.want {
			t.Errorf("clampVideoPosterSeekSeconds(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestVideoPosterSeekAttempts(t *testing.T) {
	if got := videoPosterSeekAttempts(0.5); !slices.Equal(got, []string{"0.500", "0"}) {
		t.Errorf("videoPosterSeekAttempts(0.5) = %v, want [0.500 0]", got)
	}
	if got := videoPosterSeekAttempts(0); !slices.Equal(got, []string{"0"}) {
		t.Errorf("videoPosterSeekAttempts(0) = %v, want [0]", got)
	}
}

// posterTestPNG renders a real 3×2 PNG so the qlmanage-path conversion test
// decodes what QuickLook-shaped output looks like. Renamed from a natural
// tinyPNG because fal_client_test.go already owns that name.
func posterTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestPNGFileToJPEGFile(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "thumb.png")
	if err := os.WriteFile(pngPath, posterTestPNG(t), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	jpgPath := filepath.Join(dir, "vid_x_poster.jpg")
	if err := pngFileToJPEGFile(pngPath, jpgPath); err != nil {
		t.Fatalf("pngFileToJPEGFile: %v", err)
	}
	data, err := os.ReadFile(jpgPath)
	if err != nil {
		t.Fatalf("read poster: %v", err)
	}
	// JPEG SOI magic — the canonical poster is a real JPEG, not a renamed PNG.
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
		t.Fatalf("poster bytes % x, want JPEG SOI (ff d8)", data[:min(2, len(data))])
	}
}

// fakeFFmpegPosterScript fakes the ffmpeg CLI for poster invocations: it
// writes a JPEG-sniffable payload to the LAST argument, which is the staged
// output path in ffmpegPosterArgs' shape.
const fakeFFmpegPosterScript = `#!/bin/sh
out=""
for a in "$@"; do out="$a"; done
printf '\377\330POSTER' > "$out"
`

// stubPosterFFmpeg points local-binary lookup at a fake ffmpeg so poster
// generation runs its real orchestration (staging, rename, idempotence)
// without a host ffmpeg.
func stubPosterFFmpeg(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte(fakeFFmpegPosterScript), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	stubLocalLookup(t, map[string]string{"ffmpeg": path})
}

// writePosterTestVideo stages the clip bytes as a temp file the way the
// tool layer does, returning its path.
func writePosterTestVideo(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "clip.bin")
	// Deliberately not an MP4: duration/dims stay unknown, so the fixed 0.5s
	// seek and container-blind scaling paths are what run.
	if err := os.WriteFile(path, []byte("not-a-video-payload"), 0o644); err != nil {
		t.Fatalf("write clip: %v", err)
	}
	return path
}

func TestGenerateVideoPosterViaFFmpeg(t *testing.T) {
	stubPosterFFmpeg(t)
	dir := t.TempDir()
	clip := writePosterTestVideo(t, dir)
	if !generateVideoPoster(AppConfig{}, clip) {
		t.Fatalf("generateVideoPoster with fake ffmpeg = false, want true")
	}
	poster := videoPosterPath(clip)
	data, err := os.ReadFile(poster)
	if err != nil {
		t.Fatalf("poster missing: %v", err)
	}
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
		t.Fatalf("poster bytes % x, want the fake ffmpeg's JPEG payload", data)
	}
	// No staging litter beside the artifact.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".poster-") {
			t.Errorf("staging file %s left behind", e.Name())
		}
	}
}

func TestGenerateVideoPosterIdempotent(t *testing.T) {
	// No ffmpeg stubbed: with the existing poster file present, the
	// short-circuit must win before any extractor is consulted.
	dir := t.TempDir()
	clip := writePosterTestVideo(t, dir)
	if err := os.WriteFile(videoPosterPath(clip), []byte("existing poster"), 0o644); err != nil {
		t.Fatalf("seed poster: %v", err)
	}
	if !generateVideoPoster(AppConfig{}, clip) {
		t.Fatalf("generateVideoPoster with existing poster = false, want true")
	}
	data, _ := os.ReadFile(videoPosterPath(clip))
	if string(data) != "existing poster" {
		t.Fatalf("existing poster was overwritten: %q", string(data))
	}
}

func TestGenerateVideoPosterSkipsWithoutTools(t *testing.T) {
	// TestMain pins localBinaryLookPath to not-found, so neither extractor
	// resolves; off darwin even qlmanage's gate is closed. The clip must come
	// through untouched with no poster beside it.
	pinRuntimeGOOS(t, "linux")
	dir := t.TempDir()
	clip := writePosterTestVideo(t, dir)
	if generateVideoPoster(AppConfig{}, clip) {
		t.Fatalf("generateVideoPoster without tools = true, want false")
	}
	if _, err := os.Stat(videoPosterPath(clip)); !os.IsNotExist(err) {
		t.Fatalf("poster should not exist, stat err = %v", err)
	}
}

func TestGenerateVideoPosterViaQuickLook(t *testing.T) {
	// ffmpeg stays unresolved (only qlmanage is stubbed), runtimeGOOS is
	// pinned to darwin, and the fake qlmanage writes a real PNG named the
	// way the real one does (<input basename>.png in -o's dir) — so the
	// PNG→JPEG bridge and the canonical naming are both exercised.
	pinRuntimeGOOS(t, "darwin")
	dir := t.TempDir()
	clip := writePosterTestVideo(t, dir)
	pngB64 := base64.StdEncoding.EncodeToString(posterTestPNG(t))
	script := fmt.Sprintf(`#!/bin/sh
out=""
video=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  video="$a"
  prev="$a"
done
printf '%%s' '%s' | base64 -d > "$out/$(basename "$video").png"
`, pngB64)
	path := filepath.Join(dir, "qlmanage")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake qlmanage: %v", err)
	}
	stubLocalLookup(t, map[string]string{"qlmanage": path})

	if !generateVideoPoster(AppConfig{}, clip) {
		t.Fatalf("generateVideoPoster with fake qlmanage = false, want true")
	}
	data, err := os.ReadFile(videoPosterPath(clip))
	if err != nil {
		t.Fatalf("poster missing: %v", err)
	}
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xd8 {
		t.Fatalf("poster bytes % x, want re-encoded JPEG SOI (ff d8)", data[:min(2, len(data))])
	}
}

// TestWriteChatVideoArtifactsGeneratesPoster pins the persist-time wiring:
// the shared media-artifact writer calls the poster generator for video
// artifacts (and only video — the audio sibling writes none).
func TestWriteChatVideoArtifactsGeneratesPoster(t *testing.T) {
	stubPosterFFmpeg(t)
	dir := t.TempDir()
	clip := writePosterTestVideo(t, dir)

	contents, _, err := writeChatVideoArtifacts(AppConfig{}, dir, []ToolVideoFile{{TempPath: clip, MimeType: "video/mp4"}})
	if err != nil {
		t.Fatalf("writeChatVideoArtifacts: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(contents))
	}
	artifactPath := filepath.Join(dir, filepath.Base(contents[0].Path))
	if _, err := os.Stat(videoPosterPath(artifactPath)); err != nil {
		t.Fatalf("poster beside video artifact missing: %v", err)
	}
}

func TestWriteChatAudioArtifactsWritesNoPoster(t *testing.T) {
	stubPosterFFmpeg(t)
	dir := t.TempDir()
	audio := filepath.Join(dir, "tone.bin")
	if err := os.WriteFile(audio, []byte("RIFF\x24\x00\x00\x00WAVEfmt "), 0o644); err != nil {
		t.Fatalf("write audio temp: %v", err)
	}
	if _, _, err := writeChatAudioArtifacts(AppConfig{}, dir, []ToolAudioFile{{TempPath: audio, MimeType: "audio/wav"}}); err != nil {
		t.Fatalf("writeChatAudioArtifacts: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "_poster") {
			t.Fatalf("audio artifact wrote a poster: %s", e.Name())
		}
	}
}

// mp4ishPayload is a byte payload that sniffs as an MP4 container (ftyp box at
// offset 4) so decodeVideoPayload accepts it as a video attachment.
func mp4ishPayload() []byte {
	payload := append([]byte{0x00, 0x00, 0x00, 0x20}, []byte("ftypisom00000000isommp42fake-payload")...)
	return payload
}

// TestHistoryContentForMessageReturnsVideoURLs pins the upload path's URL
// contract: a data-URL video attachment persists as an artifact WITH its
// poster, and the returned hydrated URL resolves to that artifact — the src
// swap ChatStreamStart.UserVideos carries to the live transcript.
func TestHistoryContentForMessageReturnsVideoURLs(t *testing.T) {
	stubPosterFFmpeg(t)
	artifactsDir := t.TempDir()
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4ishPayload())

	contents, videoURLs, err := historyContentForMessage(ChatMessage{
		Role:    "user",
		Content: "extend this",
		Videos:  []string{dataURL},
	}, artifactsDir, AppConfig{})
	if err != nil {
		t.Fatalf("historyContentForMessage returned error: %v", err)
	}
	if len(contents) != 2 {
		t.Fatalf("contents = %d entries, want 2 (text + video)", len(contents))
	}
	if len(videoURLs) != 1 {
		t.Fatalf("videoURLs = %d, want 1", len(videoURLs))
	}
	url := videoURLs[0]
	if !strings.HasPrefix(url, "/atelier-artifact/") {
		t.Fatalf("videoURL = %q, want /atelier-artifact/ prefix", url)
	}
	// The URL resolves to the persisted artifact, and the poster landed
	// beside it (the fake ffmpeg's JPEG).
	artifactPath := strings.TrimPrefix(url, artifactPrefix)
	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("video URL does not resolve to the persisted artifact: %v", err)
	}
	if _, err := os.Stat(videoPosterPath(artifactPath)); err != nil {
		t.Fatalf("poster beside uploaded artifact missing: %v", err)
	}
}

// TestAppendChatUserTurnReturnsVideoURLs walks the real turn-1 → append flow:
// both streaming persistence paths must hand back the persisted upload URLs
// in attachment order.
func TestAppendChatUserTurnReturnsVideoURLs(t *testing.T) {
	stubPosterFFmpeg(t)
	root := t.TempDir()
	storage := ConfigStorage{
		Root:      filepath.Join(root, ".atelier"),
		History:   filepath.Join(root, ".atelier", "history"),
		Artifacts: filepath.Join(root, ".atelier", "history"),
	}
	config := defaultAppConfig()
	config.Storage = storage
	if err := ensureStorageDirs(storage); err != nil {
		t.Fatalf("ensureStorageDirs returned error: %v", err)
	}
	upload := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4ishPayload())

	conversationID, firstURLs, err := writePendingChatConversation(config, ChatRequest{
		Model:    "chat-model",
		Messages: []ChatMessage{{Role: "user", Content: "turn one", Videos: []string{upload}}},
	})
	if err != nil {
		t.Fatalf("writePendingChatConversation returned error: %v", err)
	}
	if len(firstURLs) != 1 || !strings.HasPrefix(firstURLs[0], "/atelier-artifact/") {
		t.Fatalf("turn-1 videoURLs = %v, want one hydrated artifact URL", firstURLs)
	}

	appendedID, secondURLs, err := appendChatUserTurn(config, ChatRequest{
		ConversationID: conversationID,
		Model:          "chat-model",
		Messages:       []ChatMessage{{Role: "user", Content: "turn two", Videos: []string{upload}}},
	})
	if err != nil {
		t.Fatalf("appendChatUserTurn returned error: %v", err)
	}
	if appendedID != conversationID {
		t.Fatalf("appendChatUserTurn conversation = %q, want %q", appendedID, conversationID)
	}
	if len(secondURLs) != 1 || !strings.HasPrefix(secondURLs[0], "/atelier-artifact/") {
		t.Fatalf("turn-2 videoURLs = %v, want one hydrated artifact URL", secondURLs)
	}
}
