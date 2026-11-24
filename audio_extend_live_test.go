package main

import (
	"context"
	"encoding/base64"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atelier/internal/mediasniff"
)

// TestLiveFalAudioExtend runs ONE PAID audio extension against the live fal API
// with the keychain key — the end-to-end check no mock can give for three
// conversation fixes:
//
//   - conv_5745231eed289e0f2a187570 and conv_1902e78b1c9b1eae182a3d6e: a
//     stable-audio-3 audio-outpainting endpoint receives the prompt it
//     requires and the seconds to add, under its own input names;
//   - conv_78f17db8d3f2125ded13e263: the AAC clip extract_audio produces by
//     copy is converted to WAV before the upload, because the worker decodes
//     with libsndfile.
//
// What it checks: the call succeeds, the AAC→WAV conversion rides a notice,
// and the delivered clip is about four seconds longer than the source. What it
// cannot tell: whether the addition sounds like a continuation. That needs
// ears, so both clips are written to ATELIER_LIVE_FAL_AUDIO_EXTEND_OUT (a
// fresh temp directory when unset) for listening.
//
// ATELIER_LIVE_FAL_AUDIO_EXTEND_MODEL picks the endpoint — one paid call per
// run — defaulting to the small sfx packaging; fal-ai/stable-audio-25/inpaint
// is the conv_78f17db8d3f2125ded13e263 one. It needs an ffmpeg on PATH, both
// to synthesize the AAC source and to convert it. The key is read in-process
// and never logged: nothing here may route it through a shell pipe or print a
// request's headers.
//
//	ATELIER_LIVE_FAL_AUDIO_EXTEND=1 go test -run TestLiveFalAudioExtend -v .
func TestLiveFalAudioExtend(t *testing.T) {
	if os.Getenv("ATELIER_LIVE_FAL_AUDIO_EXTEND") == "" {
		t.Skip("set ATELIER_LIVE_FAL_AUDIO_EXTEND=1 to run one paid audio extension against the live fal API with the keychain key")
	}
	apiKey, err := loadFalAPIKey()
	if err != nil || apiKey == "" {
		t.Fatalf("no fal key in the keychain (err=%v)", err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on PATH: the AAC source needs one to be synthesized and converted")
	}
	model := strings.TrimSpace(os.Getenv("ATELIER_LIVE_FAL_AUDIO_EXTEND_MODEL"))
	if model == "" {
		model = "fal-ai/stable-audio-3/small/sfx/audio-outpainting"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// A four-second AAC clip — the container extract_audio produces when the
	// source video's codec allows a copy.
	dir := t.TempDir()
	m4a := filepath.Join(dir, "source.m4a")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=4", "-c:a", "aac", m4a).CombinedOutput(); err != nil {
		t.Fatalf("synthesize the AAC source: %v (%s)", err, out)
	}
	source, err := os.ReadFile(m4a)
	if err != nil {
		t.Fatalf("read the AAC source: %v", err)
	}
	sourceSeconds, ok := mediasniff.AudioDuration(source)
	if !ok || sourceSeconds <= 0 {
		t.Fatalf("the source's length can't be read (%d bytes)", len(source))
	}
	t.Logf("source: %d bytes, %.2fs AAC; model: %s", len(source), sourceSeconds, model)

	app := NewApp()
	submits := &liveFalSubmitRecorder{base: http.DefaultTransport}
	app.client.Transport = submits
	// A fresh storage root keeps the schema cache and the overrides out of the
	// user's ~/.atelier. The audio provider defaults to fal; the model under
	// test takes the slot a Settings pick would.
	config := mergeAppConfig(AppConfig{Storage: ConfigStorage{Root: t.TempDir()}})
	config.Providers.Fal.AudioExtendModel = model
	config.Providers.Local.FFmpeg.Binary = ffmpeg
	withRealLocalLookup(t)
	definition, ok := defaultHarnessToolRegistry(ctx, config, app).Get("extend_audio")
	if !ok {
		t.Fatal("extend_audio is not offered with a fal key on the fal audio route")
	}
	gateway := newToolGateway(app, config)
	gateway.tools.AttachedAudios = []string{"data:audio/mp4;base64," + base64.StdEncoding.EncodeToString(source)}

	const added = 4.0
	began := time.Now()
	result, summary, err := definition.Execute(ctx, gateway.tools, HarnessToolCall{Name: "extend_audio", Content: "continue in the same style", Duration: "4"})
	if err != nil {
		t.Fatalf("Execute: %v (%s)", err, submits.describe())
	}
	typed, ok := result.(ToolAudioResult)
	if !ok {
		t.Fatalf("result = %T, want ToolAudioResult", result)
	}
	if typed.Count != 1 || len(typed.Audios) != 1 {
		t.Fatalf("tool result = %+v, want one extended clip", typed)
	}
	// The harness deletes a staged clip once it is saved; nothing saves it here.
	t.Cleanup(func() { _ = os.Remove(typed.Audios[0].TempPath) })
	delivered, err := os.ReadFile(typed.Audios[0].TempPath)
	if err != nil {
		t.Fatalf("read the delivered clip: %v", err)
	}
	t.Logf("%s in %s; model=%s mime=%s notices=%v", summary, time.Since(began).Round(time.Millisecond),
		typed.Model, typed.Audios[0].MimeType, typed.Notices)
	t.Logf("delivered: %d bytes, %.2fs against a %.2fs source + %.0fs", len(delivered), typed.DurationSeconds, sourceSeconds, added)

	converted := false
	for _, notice := range typed.Notices {
		if strings.Contains(notice, "AAC/M4A to WAV") {
			converted = true
		}
	}
	if !converted {
		t.Errorf("notices = %v, want the AAC→WAV conversion note: the source went up undecodable", typed.Notices)
	}
	// An extension that ships the source's own length — or the model's default
	// 30 — is the failure both conversations saw: wrong length, said as success.
	if delta := typed.DurationSeconds - (sourceSeconds + added); math.Abs(delta) > 1 {
		t.Errorf("delivered %.2fs, want about %.2fs (source %.2fs + %.0fs)", typed.DurationSeconds, sourceSeconds+added, sourceSeconds, added)
	}
	if typed.CostMicros > 0 {
		t.Logf("recorded cost: $%.6f", float64(typed.CostMicros)/1e6)
	} else {
		t.Log("no cost recorded — the activity would show \"?\"")
	}

	// Keep both clips for listening.
	outDir := os.Getenv("ATELIER_LIVE_FAL_AUDIO_EXTEND_OUT")
	if outDir == "" {
		if outDir, err = os.MkdirTemp("", "atelier-audio-extend-"); err != nil {
			t.Fatalf("create the output directory: %v", err)
		}
	} else if err = os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create the output directory: %v", err)
	}
	_ = os.WriteFile(filepath.Join(outDir, "source.m4a"), source, 0o644)
	_ = os.WriteFile(filepath.Join(outDir, "extended"+audioExtensionForMediaType(typed.Audios[0].MimeType)), delivered, 0o644)
	t.Logf("clips for listening: %s", outDir)
}
