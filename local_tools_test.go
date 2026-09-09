package main

// Tests for the local CLI media tool layer (local_tools.go): binary
// detection, the whisper CLI runner (against fake shell-script binaries), the
// transcription provider resolution that routes transcribe_audio between fal
// and the local whisper, and the Settings-facing detection report.
//
// TestMain (helpers_test.go) pins detection off package-wide; these tests
// restore or stub the lookup per test so outcomes never depend on what is
// installed on the machine running `go test`.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// withRealLocalLookup restores real PATH lookup for one test.
func withRealLocalLookup(t *testing.T) {
	t.Helper()
	previous := localBinaryLookPath
	localBinaryLookPath = exec.LookPath
	t.Cleanup(func() { localBinaryLookPath = previous })
}

// stubLocalLookup replaces PATH lookup with an explicit name→path table.
func stubLocalLookup(t *testing.T, found map[string]string) {
	t.Helper()
	previous := localBinaryLookPath
	localBinaryLookPath = func(name string) (string, error) {
		if path, ok := found[name]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { localBinaryLookPath = previous })
}

// writeFakeWhisper writes an executable shell script under dir, returning its
// absolute path for a config binary override.
func writeFakeWhisper(t *testing.T, dir, name, script string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// openaiWhisperScript fakes the openai-whisper CLI: it writes the invocation's
// args (prefixed with a stable token) to <output_dir>/<input-basename>.txt —
// exactly where the real CLI leaves its transcript — and a well-formed
// --output_format json document (with word timestamps) beside it for the
// timestamped paths.
const openaiWhisperScript = `#!/bin/sh
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--output_dir" ]; then out="$a"; fi
  prev="$a"
done
base=$(basename "$1"); base="${base%.*}"
printf 'TRANSCRIPT-TOKEN %s' "$*" > "$out/$base.txt"
printf '{"text":"hello world","segments":[{"start":0,"end":1,"text":"hello world","words":[{"word":"hello","start":0,"end":0.5},{"word":"world","start":0.5,"end":1}]}]}' > "$out/$base.json"
`

// cppWhisperScript fakes whisper.cpp's whisper-cli: the transcript lands at
// "<-of value>.txt", and a two-cue WebVTT document at "<-of value>.vtt" for
// the timestamped paths.
const cppWhisperScript = `#!/bin/sh
of=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-of" ]; then of="$a"; fi
  prev="$a"
done
printf 'TRANSCRIPT-TOKEN %s' "$*" > "$of.txt"
printf 'WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n\n00:00:01.000 --> 00:00:02.000\nworld\n' > "$of.vtt"
`

func wavDataURL() string {
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(buildWAV(8000))
}

func TestResolveLocalBinary(t *testing.T) {
	const whisperPath = "/opt/test/bin/whisper"
	const cliPath = "/opt/test/bin/whisper-cli"

	t.Run("first candidate wins", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{"whisper": whisperPath, "whisper-cli": cliPath})
		resolved, ok := resolveLocalBinary(whisperBinarySpec, "")
		if !ok {
			t.Fatal("expected whisper to resolve")
		}
		if resolved.flavor != localWhisperFlavorOpenAI || resolved.path != whisperPath || resolved.source != localBinarySourcePath {
			t.Fatalf("resolved = %+v", resolved)
		}
	})
	t.Run("falls back to whisper-cli", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{"whisper-cli": cliPath})
		resolved, ok := resolveLocalBinary(whisperBinarySpec, "")
		if !ok || resolved.flavor != localWhisperFlavorCPP || resolved.path != cliPath {
			t.Fatalf("resolved = %+v, ok = %v", resolved, ok)
		}
	})
	t.Run("not installed", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{})
		if _, ok := resolveLocalBinary(whisperBinarySpec, ""); ok {
			t.Fatal("expected no resolution without any candidate")
		}
	})
	t.Run("override wins over PATH", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{"whisper": whisperPath, "whisper-cli": cliPath})
		resolved, ok := resolveLocalBinary(whisperBinarySpec, "whisper-cli")
		if !ok || resolved.source != localBinarySourceConfig || resolved.flavor != localWhisperFlavorCPP {
			t.Fatalf("resolved = %+v, ok = %v", resolved, ok)
		}
	})
	t.Run("override that does not resolve", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{"whisper": whisperPath})
		if _, ok := resolveLocalBinary(whisperBinarySpec, "/no/such/whisper"); ok {
			t.Fatal("an unresolvable override must fail, not fall back to PATH")
		}
	})
	t.Run("override with an unrecognized name gets the primary flavor", func(t *testing.T) {
		const custom = "/opt/other/whisper-nightly"
		stubLocalLookup(t, map[string]string{"whisper-nightly": custom})
		resolved, ok := resolveLocalBinary(whisperBinarySpec, "whisper-nightly")
		if !ok || resolved.flavor != localWhisperFlavorOpenAI {
			t.Fatalf("resolved = %+v, ok = %v", resolved, ok)
		}
	})
}

func TestWhisperArgBuilders(t *testing.T) {
	const input = "/tmp/atelier-whisper-in-123/audio.wav"
	const outDir = "/tmp/atelier-whisper-out-456"

	t.Run("openai-whisper full", func(t *testing.T) {
		args, output := whisperOpenAIArgs(input, outDir, "small", "translate", "fr", "")
		want := []string{input, "--output_format", "txt", "--output_dir", outDir, "--model", "small", "--language", "fr", "--task", "translate"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Fatalf("args = %v, want %v", args, want)
		}
		if output != filepath.Join(outDir, "audio.txt") {
			t.Fatalf("output = %q", output)
		}
	})
	t.Run("openai-whisper minimal lets the CLI default the model", func(t *testing.T) {
		args, _ := whisperOpenAIArgs(input, outDir, "  ", "", "", "")
		for _, arg := range args {
			if arg == "--model" || arg == "--language" || arg == "--task" {
				t.Fatalf("minimal invocation should carry no model/language/task flag: %v", args)
			}
		}
	})
	t.Run("openai-whisper words switch to json + word timestamps", func(t *testing.T) {
		args, output := whisperOpenAIArgs(input, outDir, "", "", "", "words")
		want := []string{input, "--output_format", "json", "--output_dir", outDir, "--word_timestamps", "True"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Fatalf("args = %v, want %v", args, want)
		}
		if output != filepath.Join(outDir, "audio.json") {
			t.Fatalf("output = %q", output)
		}
	})
	t.Run("openai-whisper segments switch to json without the word flag", func(t *testing.T) {
		args, output := whisperOpenAIArgs(input, outDir, "", "", "", "segments")
		for _, arg := range args {
			if arg == "--word_timestamps" {
				t.Fatalf("segments must not request word timestamps: %v", args)
			}
		}
		if !strings.Contains(strings.Join(args, " "), "--output_format json") || output != filepath.Join(outDir, "audio.json") {
			t.Fatalf("args = %v, output = %q", args, output)
		}
	})
	t.Run("whisper-cpp full", func(t *testing.T) {
		args, output := whisperCPPArgs(input, outDir, "/models/ggml-base.en.bin", "translate", "de", "")
		want := []string{"-m", "/models/ggml-base.en.bin", "-f", input, "-otxt", "-of", filepath.Join(outDir, "audio"), "-l", "de", "-tr"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Fatalf("args = %v, want %v", args, want)
		}
		if output != filepath.Join(outDir, "audio.txt") {
			t.Fatalf("output = %q", output)
		}
	})
	t.Run("whisper-cpp words use vtt with one-word cues", func(t *testing.T) {
		args, output := whisperCPPArgs(input, outDir, "/models/ggml-base.en.bin", "", "", "words")
		want := []string{"-m", "/models/ggml-base.en.bin", "-f", input, "-ovtt", "-of", filepath.Join(outDir, "audio"), "-ml", "1", "-sow"}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Fatalf("args = %v, want %v", args, want)
		}
		if output != filepath.Join(outDir, "audio.vtt") {
			t.Fatalf("output = %q", output)
		}
	})
	t.Run("whisper-cpp segments use vtt without the word split", func(t *testing.T) {
		args, output := whisperCPPArgs(input, outDir, "", "", "", "segments")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-ovtt") || strings.Contains(joined, "-ml") || strings.Contains(joined, "-sow") {
			t.Fatalf("segments must use plain vtt cues: %v", args)
		}
		if output != filepath.Join(outDir, "audio.vtt") {
			t.Fatalf("output = %q", output)
		}
	})
	t.Run("whisper-cpp empty model omits -m for the CLI's own default", func(t *testing.T) {
		args, _ := whisperCPPArgs(input, outDir, "", "", "", "")
		for _, arg := range args {
			if arg == "-m" {
				t.Fatalf("empty model must omit -m so whisper-cli applies its own default: %v", args)
			}
		}
		want := []string{"-f", input, "-otxt", "-of", filepath.Join(outDir, "audio")}
		if strings.Join(args, " ") != strings.Join(want, " ") {
			t.Fatalf("args = %v, want %v", args, want)
		}
	})
}

func TestRunLocalWhisperTranscription(t *testing.T) {
	withRealLocalLookup(t)
	dir := t.TempDir()

	newConfig := func() AppConfig {
		config := defaultAppConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "bin"), "whisper", openaiWhisperScript)
		return config
	}

	t.Run("openai-whisper flavor", func(t *testing.T) {
		config := newConfig()
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Model: "small", Audio: wavDataURL(), Task: "translate", Language: "fr"})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		for _, want := range []string{"TRANSCRIPT-TOKEN", "--output_format txt", "--model small", "--language fr", "--task translate", ".wav"} {
			if !strings.Contains(transcript.Text, want) {
				t.Errorf("transcript %q missing %q", transcript.Text, want)
			}
		}
	})
	t.Run("whisper-cpp flavor", func(t *testing.T) {
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp"), "whisper-cli", cppWhisperScript)
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Model: "/models/ggml-base.en.bin", Audio: wavDataURL()})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		for _, want := range []string{"TRANSCRIPT-TOKEN", "-otxt", "-m /models/ggml-base.en.bin"} {
			if !strings.Contains(transcript.Text, want) {
				t.Errorf("transcript %q missing %q", transcript.Text, want)
			}
		}
	})
	t.Run("openai-whisper words parse the json output", func(t *testing.T) {
		config := newConfig()
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL(), Timestamps: "words"})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if transcript.Text != "hello world" {
			t.Errorf("plain transcript = %q", transcript.Text)
		}
		if transcript.Timestamps != "words" {
			t.Errorf("Timestamps = %q, want words", transcript.Timestamps)
		}
		if !strings.Contains(transcript.TimestampedText, "[00:00:00.000 --> 00:00:00.500] hello") ||
			!strings.Contains(transcript.TimestampedText, "[00:00:00.500 --> 00:00:01.000] world") {
			t.Errorf("TimestampedText = %q, want per-word lines", transcript.TimestampedText)
		}
	})
	t.Run("whisper-cpp words parse the vtt output", func(t *testing.T) {
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp-ts"), "whisper-cli", cppWhisperScript)
		config.Providers.Local.Whisper.Model = "/models/ggml-base.en.bin"
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL(), Timestamps: "words"})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if transcript.Timestamps != "words" {
			t.Fatalf("Timestamps = %q, want words", transcript.Timestamps)
		}
		if !strings.Contains(transcript.TimestampedText, "[00:00:00.000 --> 00:00:01.000] hello") ||
			!strings.Contains(transcript.TimestampedText, "[00:00:01.000 --> 00:00:02.000] world") {
			t.Fatalf("TimestampedText = %q, want per-cue lines", transcript.TimestampedText)
		}
	})
	t.Run("whisper-cpp empty model uses the cached default when present", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("WHISPER_MODEL", "") // pin the env tier off — hermetic on any machine
		cacheDir := filepath.Join(home, ".cache", "whisper.cpp")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		cached := filepath.Join(cacheDir, "ggml-base.en.bin")
		if err := os.WriteFile(cached, []byte("fake-model"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp-cache"), "whisper-cli", cppWhisperScript)
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if !strings.Contains(transcript.Text, "-m "+cached) {
			t.Fatalf("transcript %q should pass the cached model path", transcript.Text)
		}
	})
	t.Run("whisper-cpp empty model auto-fills ~/.whisper-base.en.bin", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("WHISPER_MODEL", "") // pin the env tier off — hermetic on any machine
		homeRoot := filepath.Join(home, ".whisper-base.en.bin")
		if err := os.WriteFile(homeRoot, []byte("fake-model"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp-homeroot"), "whisper-cli", cppWhisperScript)
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if !strings.Contains(transcript.Text, "-m "+homeRoot) {
			t.Fatalf("transcript %q should pass the home-root model path", transcript.Text)
		}
	})
	t.Run("whisper-cpp home-root model outranks the cache", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("WHISPER_MODEL", "")
		homeRoot := filepath.Join(home, ".whisper-base.en.bin")
		if err := os.WriteFile(homeRoot, []byte("fake-model"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		cacheDir := filepath.Join(home, ".cache", "whisper.cpp")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cacheDir, "ggml-base.en.bin"), []byte("fake-model"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp-homeroot-cache"), "whisper-cli", cppWhisperScript)
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if !strings.Contains(transcript.Text, "-m "+homeRoot) {
			t.Fatalf("transcript %q should prefer the home-root model over the cache", transcript.Text)
		}
	})
	t.Run("whisper-cpp empty model honors WHISPER_MODEL over the cache", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		cacheDir := filepath.Join(home, ".cache", "whisper.cpp")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cacheDir, "ggml-base.en.bin"), []byte("fake-model"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		const envModel = "/env/models/ggml-small.en.bin"
		t.Setenv("WHISPER_MODEL", envModel)
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp-env"), "whisper-cli", cppWhisperScript)
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if !strings.Contains(transcript.Text, "-m "+envModel) {
			t.Fatalf("transcript %q should pass the WHISPER_MODEL path", transcript.Text)
		}
		if strings.Contains(transcript.Text, "ggml-base.en.bin") {
			t.Fatalf("WHISPER_MODEL must outrank the cache default: %q", transcript.Text)
		}
	})
	t.Run("whisper-cpp explicit Settings model beats WHISPER_MODEL", func(t *testing.T) {
		t.Setenv("WHISPER_MODEL", "/env/models/should-not-win.bin")
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp-explicit"), "whisper-cli", cppWhisperScript)
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Model: "/models/ggml-base.en.bin", Audio: wavDataURL()})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if !strings.Contains(transcript.Text, "-m /models/ggml-base.en.bin") {
			t.Fatalf("transcript %q should pass the explicit model", transcript.Text)
		}
		if strings.Contains(transcript.Text, "should-not-win") {
			t.Fatalf("an explicit model must outrank WHISPER_MODEL: %q", transcript.Text)
		}
	})
	t.Run("whisper-cpp empty model with no cache invokes the CLI model-less", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("WHISPER_MODEL", "") // pin the env tier off — hermetic on any machine
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "cpp-bare"), "whisper-cli", cppWhisperScript)
		transcript, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()})
		if err != nil {
			t.Fatalf("runLocalWhisperTranscription: %v", err)
		}
		if strings.Contains(transcript.Text, " -m ") || strings.HasPrefix(transcript.Text, "TRANSCRIPT-TOKEN -m") {
			t.Fatalf("transcript %q must not carry -m — whisper-cli applies its own default", transcript.Text)
		}
	})
	t.Run("missing binary", func(t *testing.T) {
		config := newConfig()
		config.Providers.Local.Whisper.Binary = filepath.Join(dir, "no-such-whisper") // deterministic miss
		if _, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()}); err == nil || !strings.Contains(err.Error(), "no local whisper CLI found") {
			t.Fatalf("err = %v, want the install-hint error", err)
		}
	})
	t.Run("non-data-URL input", func(t *testing.T) {
		config := newConfig()
		if _, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: "https://example.com/a.wav"}); err == nil || !strings.Contains(err.Error(), "not a data URL") {
			t.Fatalf("err = %v, want the data-URL error", err)
		}
	})
	t.Run("cli failure surfaces output", func(t *testing.T) {
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "fail"), "whisper", "#!/bin/sh\necho 'boom: bad audio' >&2\nexit 1\n")
		if _, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()}); err == nil || !strings.Contains(err.Error(), "boom: bad audio") {
			t.Fatalf("err = %v, want the CLI output in the error", err)
		}
	})
	t.Run("empty transcript is an error", func(t *testing.T) {
		config := newConfig()
		config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, filepath.Join(dir, "empty"), "whisper", "#!/bin/sh\n: > /dev/null\nout=\"\"\nprev=\"\"\nfor a in \"$@\"; do\n  if [ \"$prev\" = \"--output_dir\" ]; then out=\"$a\"; fi\n  prev=\"$a\"\ndone\nbase=$(basename \"$1\"); base=\"${base%.*}\"\n: > \"$out/$base.txt\"\n")
		if _, err := runLocalWhisperTranscription(context.Background(), config, TranscribeAudioRequest{Audio: wavDataURL()}); err == nil || !strings.Contains(err.Error(), "empty transcript") {
			t.Fatalf("err = %v, want the empty-transcript error", err)
		}
	})
}

// TestResolveTranscriptionProvider pins the routing rules: explicit choice
// wins, auto prefers fal when a key exists (existing behavior), else a
// detected local whisper so a fal-less machine still transcribes.
func TestResolveTranscriptionProvider(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	t.Run("explicit fal survives without a key", func(t *testing.T) {
		config := defaultAppConfig()
		config.Models.TranscriptionProvider = transcriptionProviderFal
		if got := resolveTranscriptionProvider(config); got != transcriptionProviderFal {
			t.Fatalf("provider = %q", got)
		}
	})
	t.Run("explicit local survives without a binary", func(t *testing.T) {
		config := defaultAppConfig()
		config.Models.TranscriptionProvider = transcriptionProviderLocalWhisper
		if got := resolveTranscriptionProvider(config); got != transcriptionProviderLocalWhisper {
			t.Fatalf("provider = %q", got)
		}
	})
	t.Run("auto prefers fal when a key exists", func(t *testing.T) {
		if err := saveFalAPIKey("fal-test-key"); err != nil {
			t.Fatalf("saveFalAPIKey: %v", err)
		}
		stubLocalLookup(t, map[string]string{"whisper": "/opt/test/bin/whisper"})
		if got := resolveTranscriptionProvider(defaultAppConfig()); got != transcriptionProviderFal {
			t.Fatalf("provider = %q, want fal", got)
		}
	})
	t.Run("auto falls back to a detected local whisper", func(t *testing.T) {
		if err := clearFalAPIKey(); err != nil {
			t.Fatalf("clearFalAPIKey: %v", err)
		}
		stubLocalLookup(t, map[string]string{"whisper": "/opt/test/bin/whisper"})
		if got := resolveTranscriptionProvider(defaultAppConfig()); got != transcriptionProviderLocalWhisper {
			t.Fatalf("provider = %q, want local-whisper", got)
		}
	})
	t.Run("auto with neither falls back to fal", func(t *testing.T) {
		if err := clearFalAPIKey(); err != nil {
			t.Fatalf("clearFalAPIKey: %v", err)
		}
		stubLocalLookup(t, map[string]string{})
		if got := resolveTranscriptionProvider(defaultAppConfig()); got != transcriptionProviderFal {
			t.Fatalf("provider = %q, want fal", got)
		}
	})
	t.Run("unknown value behaves like auto", func(t *testing.T) {
		if err := clearFalAPIKey(); err != nil {
			t.Fatalf("clearFalAPIKey: %v", err)
		}
		config := defaultAppConfig()
		config.Models.TranscriptionProvider = "bogus"
		stubLocalLookup(t, map[string]string{"whisper": "/opt/test/bin/whisper"})
		if got := resolveTranscriptionProvider(config); got != transcriptionProviderLocalWhisper {
			t.Fatalf("provider = %q, want local-whisper", got)
		}
	})
}

func TestTranscribeAudioConfiguredLocalWhisper(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	t.Run("local whisper lights the tool without a fal key", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{"whisper": "/opt/test/bin/whisper"})
		config := defaultAppConfig()
		if !transcribeAudioConfigured(config) {
			t.Fatal("transcribe should be configured when a local whisper is detected")
		}
		definition, ok := defaultHarnessToolRegistry(context.Background(), config, nil).Get("transcribe_audio")
		if !ok {
			t.Fatal("transcribe_audio should be registered via the local whisper")
		}
		if !strings.Contains(definition.Description, "locally installed whisper CLI") {
			t.Fatalf("description should name the local backend:\n%s", definition.Description)
		}
	})
	t.Run("explicit local selection without a binary keeps the tool off", func(t *testing.T) {
		config := defaultAppConfig()
		config.Models.TranscriptionProvider = transcriptionProviderLocalWhisper
		if transcribeAudioConfigured(config) {
			t.Fatal("transcribe must not be offered when the local backend cannot run")
		}
		if _, ok := defaultHarnessToolRegistry(context.Background(), config, nil).Get("transcribe_audio"); ok {
			t.Fatal("transcribe_audio should be absent without a whisper binary")
		}
	})
	t.Run("local activity command", func(t *testing.T) {
		config := defaultAppConfig()
		config.Models.TranscriptionProvider = transcriptionProviderLocalWhisper
		definition := transcribeAudioToolDefinition(config)
		result := HarnessToolResult{Result: ToolTranscribeResult{Model: "small", Transcript: "hi"}}
		if got := definition.Activity(result).Command; strings.Join(got, " ") != "whisper transcribe small" {
			t.Fatalf("command = %v", got)
		}
		result.Result = ToolTranscribeResult{Transcript: "hi"} // empty model: the CLI default
		if got := definition.Activity(result).Command; strings.Join(got, " ") != "whisper transcribe" {
			t.Fatalf("command = %v, want no empty model tail", got)
		}
	})
}

func TestDetectLocalToolsReport(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	t.Run("detected whisper without a fal key resolves local", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{"whisper": "/opt/test/bin/whisper"})
		report := detectLocalTools(defaultAppConfig(), LocalToolOverrides{})
		if report.TranscriptionProvider != transcriptionProviderLocalWhisper {
			t.Fatalf("provider = %q, want local-whisper", report.TranscriptionProvider)
		}
		if len(report.Binaries) != 1 || report.Binaries[0].Key != "whisper" || !report.Binaries[0].Available {
			t.Fatalf("binaries = %+v", report.Binaries)
		}
		if report.Binaries[0].Path != "/opt/test/bin/whisper" || report.Binaries[0].Flavor != localWhisperFlavorOpenAI {
			t.Fatalf("status = %+v", report.Binaries[0])
		}
		if !strings.Contains(report.Binaries[0].Detail, "/opt/test/bin/whisper") {
			t.Fatalf("detail should name the path: %q", report.Binaries[0].Detail)
		}
	})
	t.Run("fal key resolves fal", func(t *testing.T) {
		if err := saveFalAPIKey("fal-test-key"); err != nil {
			t.Fatalf("saveFalAPIKey: %v", err)
		}
		stubLocalLookup(t, map[string]string{"whisper": "/opt/test/bin/whisper"})
		report := detectLocalTools(defaultAppConfig(), LocalToolOverrides{})
		if report.TranscriptionProvider != transcriptionProviderFal {
			t.Fatalf("provider = %q, want fal", report.TranscriptionProvider)
		}
	})
	t.Run("missing whisper carries an install hint", func(t *testing.T) {
		stubLocalLookup(t, map[string]string{})
		report := detectLocalTools(defaultAppConfig(), LocalToolOverrides{})
		if report.Binaries[0].Available {
			t.Fatal("whisper should be unavailable")
		}
		if !strings.Contains(report.Binaries[0].Detail, "pip install") {
			t.Fatalf("detail should tell the user how to install: %q", report.Binaries[0].Detail)
		}
	})
	t.Run("unsaved UI override is reflected", func(t *testing.T) {
		// PATH has no candidate; only the override name resolves, so the report
		// must show the config source and the whisper-cli dialect.
		stubLocalLookup(t, map[string]string{"whisper-cli": "/opt/test/bin/whisper-cli"})
		report := detectLocalTools(defaultAppConfig(), LocalToolOverrides{Binaries: map[string]string{"whisper": "whisper-cli"}})
		status := report.Binaries[0]
		if !status.Available || status.Source != localBinarySourceConfig || status.Flavor != localWhisperFlavorCPP {
			t.Fatalf("status = %+v", status)
		}
		if !strings.Contains(status.Detail, "configured binary") {
			t.Fatalf("detail should name the configured source: %q", status.Detail)
		}
	})
}

func TestToolGatewayWiresLocalWhisper(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })
	withRealLocalLookup(t)

	dir := t.TempDir()
	config := defaultAppConfig()
	config.Models.TranscriptionProvider = transcriptionProviderLocalWhisper
	config.Providers.Local.Whisper.Binary = writeFakeWhisper(t, dir, "whisper", openaiWhisperScript)

	gateway := newToolGateway(nil, config)
	if gateway.tools.TranscribeAudioLocal == nil {
		t.Fatal("gateway should wire local whisper transcription")
	}
	if gateway.tools.TranscribeAudio != nil {
		t.Fatal("fal transcription must stay unwired without a key (and app)")
	}
	transcript, err := gateway.tools.TranscribeAudioLocal(context.Background(), TranscribeAudioRequest{Audio: wavDataURL()})
	if err != nil {
		t.Fatalf("TranscribeAudioLocal: %v", err)
	}
	if !strings.Contains(transcript.Text, "TRANSCRIPT-TOKEN") {
		t.Fatalf("transcript = %q", transcript.Text)
	}

	notFound := defaultAppConfig()
	notFound.Models.TranscriptionProvider = transcriptionProviderLocalWhisper
	notFound.Providers.Local.Whisper.Binary = filepath.Join(dir, "no-such-whisper") // deterministic miss
	if newToolGateway(nil, notFound).tools.TranscribeAudioLocal != nil {
		t.Fatal("gateway must not wire local transcription without a resolvable binary")
	}
}

func TestMergeAppConfigLocalTools(t *testing.T) {
	t.Run("provider normalization", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{"local-whisper", transcriptionProviderLocalWhisper},
			{"fal", transcriptionProviderFal},
			{"  fal  ", transcriptionProviderFal},
			{"", ""},
			{"bogus", ""},
		} {
			config := defaultAppConfig()
			config.Models.TranscriptionProvider = tc.in
			if got := mergeAppConfig(config).Models.TranscriptionProvider; got != tc.want {
				t.Errorf("merge(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})
	t.Run("whisper fields are trimmed, never defaulted", func(t *testing.T) {
		config := defaultAppConfig()
		config.Providers.Local.Whisper.Binary = "  /opt/bin/whisper  "
		config.Providers.Local.Whisper.Model = " small.en "
		merged := mergeAppConfig(config)
		if merged.Providers.Local.Whisper.Binary != "/opt/bin/whisper" || merged.Providers.Local.Whisper.Model != "small.en" {
			t.Fatalf("merged whisper config = %+v", merged.Providers.Local.Whisper)
		}
		empty := mergeAppConfig(defaultAppConfig())
		if empty.Providers.Local.Whisper.Binary != "" || empty.Providers.Local.Whisper.Model != "" {
			t.Fatalf("empty whisper config must stay empty, got %+v", empty.Providers.Local.Whisper)
		}
	})
}

// TestHarnessTranscribesViaLocalWhisper drives a full turn with transcribe_audio
// routed to a fake local whisper — no fal key anywhere. Pins the whole chain:
// provider-aware registry gate, gateway wiring, the runner, and the transcript
// reaching the final model as tool evidence.
func TestHarnessTranscribesViaLocalWhisper(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withRealLocalLookup(t)

	keyring.MockInit()
	t.Cleanup(func() { _ = clearFalAPIKey() })

	whisperBin := writeFakeWhisper(t, filepath.Join(home, "bin"), "whisper", openaiWhisperScript)
	config := defaultAppConfig()
	config.Storage = ConfigStorage{
		Root:      filepath.Join(home, ".atelier"),
		History:   filepath.Join(home, ".atelier", "history"),
		Artifacts: filepath.Join(home, ".atelier", "history"),
	}
	config.Providers.Ollama.BaseURL = "http://ollama.test"
	config.Providers.Ollama.Models.Primary = "chat-box-model"
	config.Providers.Ollama.Models.Harness = "harness-model"
	config.Models.TranscriptionProvider = transcriptionProviderLocalWhisper
	config.Providers.Local.Whisper.Binary = whisperBin
	if err := writeAppConfig(config); err != nil {
		t.Fatalf("writeAppConfig: %v", err)
	}

	var finalBodies []string
	app := NewApp()
	harnessCalls := 0
	app.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/show":
			return jsonResponse(`{"capabilities":[],"model_info":{},"details":{"family":"test","parameter_size":"1B"}}`), nil
		case "/api/chat":
			payload := chatPayload(t, req)
			if payload["stream"] == false {
				switch payload["model"] {
				case "harness-model":
					harnessCalls++
					switch harnessCalls {
					case 1: // triage
						return chatCompletion("harness-model", `{"needsTools":true,"responseMode":"text","toolTask":"Transcribe the attached clip.","reason":"The user attached audio and asked for a transcript."}`), nil
					case 2: // plan
						return chatCompletion("harness-model", `{"brief":"Transcribe the clip with word timestamps.","needsTools":true,"reason":"transcribe","toolCalls":[{"name":"transcribe_audio","timestamps":"words"}]}`), nil
					case 3:
						return chatCompletion("harness-model", `{"brief":"Done.","needsTools":false,"reason":"done","toolCalls":[]}`), nil
					}
					t.Fatalf("unexpected harness call #%d", harnessCalls)
					return nil, nil
				default:
					// Title generation runs once, on the primary model, after
					// turn 1.
					return chatCompletion("chat-box-model", `"Transcript"`), nil
				}
			}
			encoded, _ := json.Marshal(payload)
			finalBodies = append(finalBodies, string(encoded))
			body := fmt.Sprintln(`{"model":"chat-box-model","message":{"role":"assistant","content":"Here is the transcript."},"done":false}`) +
				fmt.Sprintln(`{"model":"chat-box-model","done":true,"done_reason":"stop","eval_count":3}`)
			return &http.Response{StatusCode: 200, Status: "200 OK",
				Body:   io.NopCloser(strings.NewReader(body)),
				Header: http.Header{"Content-Type": []string{"application/x-ndjson"}},
			}, nil
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
			return nil, nil
		}
	})

	app.runChatStream(context.Background(), "request-transcribe-local", ChatRequest{
		BaseURL: "http://ollama.test",
		Model:   "chat-box-model",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: "Transcribe this voice memo.",
			Audios:  []string{wavDataURL()},
		}},
	})

	if len(finalBodies) == 0 {
		t.Fatal("the final model was never called")
	}
	joined := strings.Join(finalBodies, "\n")
	if !strings.Contains(joined, "hello world") {
		t.Fatal("the local whisper transcript never reached the final model as tool evidence")
	}
	// The evidence rides a JSON wire body, where Go's encoder HTML-escapes
	// ">" (\u003e) — assert on escape-free fragments of the rendered line.
	if !strings.Contains(joined, "timestampedTranscript") ||
		!strings.Contains(joined, "[00:00:00.000 --") ||
		!strings.Contains(joined, "00:00:00.500] hello") {
		t.Fatal("the word-timestamped transcript never reached the final model as tool evidence")
	}
	if strings.Contains(joined, "fal-ai/wizper") {
		t.Fatal("a fal model id leaked into a local-whisper turn")
	}
}

// TestParseWhisperVTT pins the VTT cue parser: headers are skipped, cue
// settings after the end timestamp are tolerated, and multi-line cue text
// joins with single spaces.
func TestParseWhisperVTT(t *testing.T) {
	vtt := "WEBVTT\n\n" +
		"NOTE this is a comment\n\n" +
		"00:00:00.000 --> 00:00:01.500\nhello\n\n" +
		"00:00:01.500 --> 00:00:03.000 align:start line:0%\nbig\nbeautiful\nworld\n\n"
	chunks := parseWhisperVTT([]byte(vtt))
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v, want 2", chunks)
	}
	if chunks[0].Start != 0 || chunks[0].End == nil || *chunks[0].End != 1.5 || chunks[0].Text != "hello" {
		t.Errorf("chunk 0 = %+v", chunks[0])
	}
	if chunks[1].Start != 1.5 || chunks[1].End == nil || *chunks[1].End != 3 {
		t.Errorf("chunk 1 timing = %+v", chunks[1])
	}
	if chunks[1].Text != "big beautiful world" {
		t.Errorf("multi-line cue text = %q", chunks[1].Text)
	}
}

// TestParseWhisperOpenAIJSON pins the pip CLI's json parser in both modes:
// words prefers segments[].words, segments falls back to the segment entries,
// and the top-level text rides along as the plain transcript.
func TestParseWhisperOpenAIJSON(t *testing.T) {
	doc := []byte(`{"text":"hello big world","segments":[` +
		`{"start":0,"end":1,"text":"hello big world","words":[` +
		`{"word":"hello","start":0,"end":0.4},{"word":"big","start":0.4,"end":0.7},{"word":"world","start":0.7,"end":1}]},` +
		`{"start":1,"end":2,"text":"second segment"}]}`)

	plain, chunks := parseWhisperOpenAIJSON(doc, true)
	if plain != "hello big world" {
		t.Errorf("plain = %q", plain)
	}
	// Words mode takes per-word entries where the payload has them; a segment
	// without word data (the flag was ignored) still renders at segment level
	// so no text is lost.
	if len(chunks) != 4 || chunks[0].Text != "hello" || chunks[2].Text != "world" || chunks[2].Start != 0.7 {
		t.Fatalf("word chunks = %+v", chunks)
	}
	if chunks[3].Text != "second segment" || chunks[3].Start != 1 {
		t.Fatalf("wordless segment should fall back to segment level: %+v", chunks[3])
	}

	plain, chunks = parseWhisperOpenAIJSON(doc, false)
	if len(chunks) != 2 || chunks[1].Text != "second segment" || chunks[1].Start != 1 || *chunks[1].End != 2 {
		t.Fatalf("segment chunks = %+v", chunks)
	}
	if plain != "hello big world" {
		t.Errorf("plain = %q", plain)
	}
}

// TestFormatTranscriptSeconds pins the shared clock rendering, including the
// rounding guard that keeps fractional milliseconds from producing 1.000.
func TestFormatTranscriptSeconds(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "00:00:00.000"},
		{1.5, "00:00:01.500"},
		{61.25, "00:01:01.250"},
		{3675.005, "01:01:15.005"},
		{59.9995, "00:00:59.999"}, // must not round up into an invalid millisecond field
		{-3, "00:00:00.000"},
	}
	for _, tc := range cases {
		if got := formatTranscriptSeconds(tc.in); got != tc.want {
			t.Errorf("formatTranscriptSeconds(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTranscribeAudioTimestampsValidation pins the plan-validation rule: the
// timestamps param accepts only "words" and "segments" (or absence).
func TestTranscribeAudioTimestampsValidation(t *testing.T) {
	definition := transcribeAudioToolDefinition(defaultAppConfig())
	for _, value := range []string{"", "words", "segments", "  words  "} {
		call := HarnessToolCall{Name: "transcribe_audio", Timestamps: value}
		if problems := definition.Validate("toolCalls[0]", call); len(problems) != 0 {
			t.Errorf("timestamps %q should validate, got %v", value, problems)
		}
	}
	call := HarnessToolCall{Name: "transcribe_audio", Timestamps: "phrases"}
	problems := definition.Validate("toolCalls[0]", call)
	if len(problems) != 1 || !strings.Contains(problems[0], `timestamps must be "words" or "segments"`) {
		t.Fatalf("problems = %v", problems)
	}
}
