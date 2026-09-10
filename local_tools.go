package main

// Local CLI media tools: detection of binaries installed on the user's machine
// (whisper for transcription; ffmpeg/ffprobe for local media transforms, see
// local_ffmpeg.go) plus the whisper transcription runner the transcribe_audio
// tool routes to when Models.TranscriptionProvider selects the local backend.
// A local binary is a provider like any cloud service — it just resolves via
// exec.LookPath instead of an API key. Everything here is side-effect-free at
// detection time (no process is spawned); process execution lives in
// runLocalWhisperTranscription and the ffmpeg runners below.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Whisper CLI flavors. The flavor names the command-line dialect the runner
// speaks and is reported to Settings so the user knows which whisper they have.
const (
	// localWhisperFlavorOpenAI is the `whisper` CLI from the openai-whisper
	// Python package (pip install openai-whisper). It decodes audio via ffmpeg
	// and downloads named models on first use.
	localWhisperFlavorOpenAI = "openai-whisper"
	// localWhisperFlavorCPP is whisper.cpp's `whisper-cli` (brew whisper-cpp or
	// a source build). It needs an explicit ggml model file path.
	localWhisperFlavorCPP = "whisper-cpp"
)

// Where a resolved local binary came from.
const (
	localBinarySourcePath   = "path"   // found on PATH by candidate name
	localBinarySourceConfig = "config" // the configured override resolved
)

// ImageMagick CLI flavors — the dialect names for imagemagickBinarySpec.
const (
	// localMagickFlavorIM7 is ImageMagick 7's `magick` CLI.
	localMagickFlavorIM7 = "magick7"
	// localMagickFlavorIM6 is the legacy `convert` spelling (a real IM6
	// install, or IM7's compatibility shim — same command shape either way).
	localMagickFlavorIM6 = "convert6"
)

// localBinaryCandidate is one PATH name a tool may be installed as, carrying
// the CLI dialect found under that name.
type localBinaryCandidate struct {
	name   string
	flavor string
}

// localBinarySpec describes a locally installed CLI tool Atelier can drive.
type localBinarySpec struct {
	key        string                 // stable id used in config overrides and the detection report
	label      string                 // Settings-facing name
	candidates []localBinaryCandidate // PATH names in priority order
}

var whisperBinarySpec = localBinarySpec{
	key:   "whisper",
	label: "Whisper",
	candidates: []localBinaryCandidate{
		{name: "whisper", flavor: localWhisperFlavorOpenAI},
		{name: "whisper-cli", flavor: localWhisperFlavorCPP},
	},
}

// ffmpegBinarySpec describes the ffmpeg CLI — the local video/audio transform
// backend (see local_ffmpeg.go). One dialect: the standard ffmpeg CLI every
// distribution ships.
var ffmpegBinarySpec = localBinarySpec{
	key:   "ffmpeg",
	label: "FFmpeg",
	candidates: []localBinaryCandidate{
		{name: "ffmpeg", flavor: "ffmpeg"},
	},
}

// ffprobeBinarySpec describes ffprobe, ffmpeg's metadata sibling — it powers
// probe_media and the codec-aware decisions inside join/extract (copy vs
// re-encode). Detected independently of ffmpeg, with one extra tier: when PATH
// has no ffprobe but ffmpeg resolved, the ffmpeg binary's own directory is
// probed for a sibling ffprobe (they ship together).
var ffprobeBinarySpec = localBinarySpec{
	key:   "ffprobe",
	label: "FFprobe",
	candidates: []localBinaryCandidate{
		{name: "ffprobe", flavor: "ffprobe"},
	},
}

// sipsBinarySpec describes the sips CLI macOS bundles — the basic local image
// tool backend (format conversion incl. HEIC, resize/crop/rotate/flip/pad,
// image facts; see local_images.go). One dialect; the override exists for
// tests and unusual setups since /usr/bin/sips is guaranteed on macOS. Off
// macOS, sips does not exist: the basic image tools ride ImageMagick there
// and this spec is neither consulted nor reported.
var sipsBinarySpec = localBinarySpec{
	key:   "sips",
	label: "sips",
	candidates: []localBinaryCandidate{
		{name: "sips", flavor: "sips"},
	},
}

// imagemagickBinarySpec describes the ImageMagick CLI — the advanced local
// image tool backend (watermarks, collages, color adjustments, metadata
// stripping; see local_images.go). Two dialects: "magick" (ImageMagick 7,
// preferred) and "convert" (the IM6 spelling, also installed by IM7 as a
// compatibility shim). Both run the same single-image command shape.
var imagemagickBinarySpec = localBinarySpec{
	key:   "imagemagick",
	label: "ImageMagick",
	candidates: []localBinaryCandidate{
		{name: "magick", flavor: localMagickFlavorIM7},
		{name: "convert", flavor: localMagickFlavorIM6},
	},
}

// knownLocalBinaries is the registry of local CLI media tools. whisper powers
// local transcription; ffmpeg/ffprobe power the local media transforms; sips
// and ImageMagick power the local image tools — add a spec here and
// detection, the Settings status report, and the per-binary config override
// plumbing all pick it up without further changes. Only the tool-specific
// executor is new code.
var knownLocalBinaries = []localBinarySpec{whisperBinarySpec, ffmpegBinarySpec, ffprobeBinarySpec, sipsBinarySpec, imagemagickBinarySpec}

// localBinaryLookPath is the seam tests stub to keep binary detection
// hermetic: without it, every test that builds a tool registry would register
// transcribe_audio on machines that happen to have a whisper installed.
// helpers_test.go pins it to always-not-found for the whole package;
// local_tools_test.go restores the real lookup per test.
var localBinaryLookPath = exec.LookPath

// runtimeGOOS is the platform seam for macOS-vs-other behavior — backend
// selection for the basic image tools (local_images.go) and the
// platform-aware install copy below read it instead of runtime.GOOS so tests
// can pin either side on any host.
var runtimeGOOS = runtime.GOOS

// resolvedLocalBinary is a detection result: which candidate was found, where,
// and whether that came from PATH or the configured override.
type resolvedLocalBinary struct {
	spec   localBinarySpec
	flavor string
	path   string
	source string
}

// resolveLocalBinary finds spec's binary: a non-empty override wins (an
// absolute path or a bare PATH name — both go through LookPath, which checks
// executability), otherwise the candidates are probed on PATH in priority
// order. ok is false when nothing usable was found.
func resolveLocalBinary(spec localBinarySpec, override string) (resolvedLocalBinary, bool) {
	if override = strings.TrimSpace(override); override != "" {
		if path, err := localBinaryLookPath(override); err == nil {
			return resolvedLocalBinary{spec: spec, flavor: spec.flavorFor(override), path: path, source: localBinarySourceConfig}, true
		}
		return resolvedLocalBinary{}, false
	}
	for _, candidate := range spec.candidates {
		if path, err := localBinaryLookPath(candidate.name); err == nil {
			return resolvedLocalBinary{spec: spec, flavor: candidate.flavor, path: path, source: localBinarySourcePath}, true
		}
	}
	return resolvedLocalBinary{}, false
}

// flavorFor maps an override (path or bare name) onto a CLI dialect by its
// base name; an unrecognized name gets the first candidate's flavor — the
// common case for a versioned or renamed install of the primary CLI.
func (spec localBinarySpec) flavorFor(pathOrName string) string {
	base := filepath.Base(strings.TrimSpace(pathOrName))
	for _, candidate := range spec.candidates {
		if candidate.name == base {
			return candidate.flavor
		}
	}
	if len(spec.candidates) == 0 {
		return ""
	}
	return spec.candidates[0].flavor
}

// resolveLocalWhisperBinary detects the whisper CLI the local transcription
// backend runs: the configured override if set, else "whisper" then
// "whisper-cli" on PATH.
func resolveLocalWhisperBinary(config AppConfig) (resolvedLocalBinary, bool) {
	return resolveLocalBinary(whisperBinarySpec, config.Providers.Local.Whisper.Binary)
}

// resolveLocalFFmpegBinary detects the ffmpeg CLI the local media transforms
// run: the configured override if set, else "ffmpeg" on PATH.
func resolveLocalFFmpegBinary(config AppConfig) (resolvedLocalBinary, bool) {
	return resolveLocalBinary(ffmpegBinarySpec, config.Providers.Local.FFmpeg.Binary)
}

// resolveLocalFFprobeBinary detects the ffprobe CLI. The normal spec tiers run
// first (override, then PATH); when both miss but an ffmpeg resolved, its own
// directory is probed for a sibling ffprobe — they ship in the same package,
// so a PATH-less GUI environment with an explicit ffmpeg override usually has
// the matching ffprobe right next to it.
func resolveLocalFFprobeBinary(config AppConfig) (resolvedLocalBinary, bool) {
	if resolved, ok := resolveLocalBinary(ffprobeBinarySpec, config.Providers.Local.FFprobe.Binary); ok {
		return resolved, true
	}
	ffmpeg, ok := resolveLocalFFmpegBinary(config)
	if !ok {
		return resolvedLocalBinary{}, false
	}
	sibling := filepath.Join(filepath.Dir(ffmpeg.path), "ffprobe")
	if path, err := localBinaryLookPath(sibling); err == nil {
		return resolvedLocalBinary{spec: ffprobeBinarySpec, flavor: "ffprobe", path: path, source: localBinarySourcePath}, true
	}
	return resolvedLocalBinary{}, false
}

// resolveLocalSipsBinary detects the sips CLI the basic local image tools run:
// the configured override if set, else "sips" on PATH (guaranteed on macOS).
func resolveLocalSipsBinary(config AppConfig) (resolvedLocalBinary, bool) {
	return resolveLocalBinary(sipsBinarySpec, config.Providers.Local.Sips.Binary)
}

// resolveLocalImageMagickBinary detects the ImageMagick CLI the advanced local
// image tools run: the configured override if set, else "magick" (IM7) then
// "convert" (IM6) on PATH.
func resolveLocalImageMagickBinary(config AppConfig) (resolvedLocalBinary, bool) {
	return resolveLocalBinary(imagemagickBinarySpec, config.Providers.Local.Magick.Binary)
}

// LocalToolOverrides lets the Settings UI reflect unsaved edits: binary
// overrides keyed by tool id ("whisper"), each replacing the saved config
// value for detection only.
type LocalToolOverrides struct {
	Binaries map[string]string `json:"binaries,omitempty"`
}

// LocalBinaryStatus is the Settings-facing detection result for one local CLI
// tool.
type LocalBinaryStatus struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	// Flavor names the CLI dialect (whisper: "openai-whisper" | "whisper-cpp").
	Flavor string `json:"flavor,omitempty"`
	// Path is the resolved binary path; Source says whether it came from PATH
	// ("path") or the configured override ("config").
	Path   string `json:"path,omitempty"`
	Source string `json:"source,omitempty"`
	// Detail is a human sentence for the Settings hint line — what was found,
	// or how to install the tool when nothing was.
	Detail string `json:"detail,omitempty"`
}

// LocalToolsReport is the Settings-facing snapshot of every known local CLI
// tool plus the capability routing they influence today.
type LocalToolsReport struct {
	Binaries []LocalBinaryStatus `json:"binaries"`
	// TranscriptionProvider is the backend the current config plus live
	// detection resolve to ("fal" | "local-whisper"), so the Settings dropdown
	// can show the effective default without duplicating the resolution rules
	// in TypeScript.
	TranscriptionProvider string `json:"transcriptionProvider"`
}

// detectLocalTools resolves every known local binary against the config and
// any UI overrides, plus the transcription provider those resolve to. Pure —
// one LookPath per candidate, no state — so it is cheap enough to call on
// every settings render.
func detectLocalTools(config AppConfig, overrides LocalToolOverrides) LocalToolsReport {
	report := LocalToolsReport{TranscriptionProvider: resolveTranscriptionProvider(config)}
	for _, spec := range knownLocalBinaries {
		if spec.key == sipsBinarySpec.key && runtimeGOOS != "darwin" {
			// sips is macOS-only; on other platforms the basic image tools
			// ride ImageMagick, so Settings never sees the unfindable binary.
			continue
		}
		override := configuredLocalBinaryOverride(config, spec.key)
		if value, ok := overrides.Binaries[spec.key]; ok {
			override = value
		}
		status := LocalBinaryStatus{Key: spec.key, Label: spec.label}
		if resolved, ok := resolveLocalBinary(spec, override); ok {
			status.Available = true
			status.Flavor = resolved.flavor
			status.Path = resolved.path
			status.Source = resolved.source
			status.Detail = localBinaryFoundDetail(resolved)
		} else {
			status.Detail = localBinaryMissingDetail(spec)
		}
		report.Binaries = append(report.Binaries, status)
	}
	return report
}

// configuredLocalBinaryOverride returns the Settings-configured binary
// override for a tool id. The switch (not a map) keeps every wired tool
// visible here — adding a spec to knownLocalBinaries without a config section
// is an obvious compile-adjacent gap rather than a silent empty string.
func configuredLocalBinaryOverride(config AppConfig, key string) string {
	switch key {
	case whisperBinarySpec.key:
		return config.Providers.Local.Whisper.Binary
	case ffmpegBinarySpec.key:
		return config.Providers.Local.FFmpeg.Binary
	case ffprobeBinarySpec.key:
		return config.Providers.Local.FFprobe.Binary
	case sipsBinarySpec.key:
		return config.Providers.Local.Sips.Binary
	case imagemagickBinarySpec.key:
		return config.Providers.Local.Magick.Binary
	default:
		return ""
	}
}

func localBinaryFoundDetail(resolved resolvedLocalBinary) string {
	dialect := resolved.flavor
	switch resolved.flavor {
	case localWhisperFlavorOpenAI:
		dialect = "openai-whisper CLI"
	case localWhisperFlavorCPP:
		dialect = "whisper.cpp CLI"
	case "ffmpeg":
		dialect = "ffmpeg CLI"
	case "ffprobe":
		dialect = "ffprobe CLI"
	case "sips":
		dialect = "sips CLI (macOS built-in)"
	case localMagickFlavorIM7:
		dialect = "ImageMagick 7 `magick` CLI"
	case localMagickFlavorIM6:
		dialect = "ImageMagick 6 `convert` CLI"
	}
	source := "detected on PATH"
	if resolved.source == localBinarySourceConfig {
		source = "configured binary"
	}
	return fmt.Sprintf("%s — %s (%s, %s)", resolved.spec.label, resolved.path, dialect, source)
}

// packageInstallHint names how to install a formula on this platform: brew
// on macOS, the mainstream Linux package managers otherwise (dnf spells some
// formulas differently — ImageMagick capitalizes — so it takes its own
// argument).
func packageInstallHint(formula, dnfFormula string) string {
	if runtimeGOOS == "darwin" {
		return "`brew install " + formula + "`"
	}
	return "`apt install " + formula + "` (Debian/Ubuntu) or `dnf install " + dnfFormula + "` (Fedora)"
}

func localBinaryMissingDetail(spec localBinarySpec) string {
	switch spec.key {
	case whisperBinarySpec.key:
		return "No whisper CLI found on the PATH. Install one to enable local transcription: `pip install -U openai-whisper` or `brew install whisper-cpp`."
	case ffmpegBinarySpec.key:
		return "No ffmpeg CLI found on the PATH. Install one to enable local video tools (screenshot, split, join, extract audio): " + packageInstallHint("ffmpeg", "ffmpeg") + "."
	case ffprobeBinarySpec.key:
		return "No ffprobe CLI found. It ships with ffmpeg (" + packageInstallHint("ffmpeg", "ffmpeg") + "); without it the video tools still run but always re-encode and extract audio as MP3."
	case sipsBinarySpec.key:
		return "No sips CLI found — it ships with macOS, so a miss here means the configured override in Settings → Image Tools points at the wrong path."
	case imagemagickBinarySpec.key:
		detail := "Install one to enable the advanced image tools (watermark, collage, color adjust, strip metadata)"
		if runtimeGOOS != "darwin" {
			detail = "Install one to enable local image editing — every image tool rides ImageMagick on this platform"
		}
		return "No ImageMagick CLI found on the PATH. " + detail + ": " + packageInstallHint("imagemagick", "ImageMagick") + "."
	default:
		return fmt.Sprintf("%s was not found on the PATH.", spec.label)
	}
}

// localWhisperTimeout bounds one local whisper invocation. Transcription is
// CPU-bound and a large model on a long clip can legitimately run for minutes,
// so this exists to reap a wedged process — not to enforce latency. Context
// cancellation (a cancelled turn) still propagates immediately.
const localWhisperTimeout = 10 * time.Minute

// runLocalWhisperTranscription runs the locally installed whisper CLI over an
// attached audio clip (a data URL) and returns its transcript — the local
// sibling of FalClient.TranscribeAudio, taking the same canonical request. The
// clip is decoded to a temp file (whisper CLIs read real files, not data URLs),
// the CLI writes its transcript into a temp dir, and both are cleaned up
// afterwards. Plain turns read a .txt; req.Timestamps set switches the output
// format (json for the pip CLI, VTT for whisper.cpp) and parses it into a
// timestamped transcript — words via --word_timestamps / -ml 1 -sow, segments
// via each CLI's default segmentation.
func runLocalWhisperTranscription(ctx context.Context, config AppConfig, req TranscribeAudioRequest) (GeneratedTranscript, error) {
	resolved, ok := resolveLocalWhisperBinary(config)
	if !ok {
		return GeneratedTranscript{}, errors.New("no local whisper CLI found — install openai-whisper (pip) or whisper.cpp (brew), or clear the configured binary override in Settings → Transcription")
	}
	data, mediaType, err := decodeAudioDataURL(req.Audio)
	if err != nil {
		return GeneratedTranscript{}, err
	}
	input, err := os.CreateTemp("", "atelier-whisper-in-*"+audioExtensionForMediaType(mediaType))
	if err != nil {
		return GeneratedTranscript{}, err
	}
	defer os.Remove(input.Name())
	if _, err := input.Write(data); err != nil {
		input.Close()
		return GeneratedTranscript{}, err
	}
	if err := input.Close(); err != nil {
		return GeneratedTranscript{}, err
	}
	outDir, err := os.MkdirTemp("", "atelier-whisper-out-*")
	if err != nil {
		return GeneratedTranscript{}, err
	}
	defer os.RemoveAll(outDir)

	model := strings.TrimSpace(req.Model)
	timestamps := strings.TrimSpace(req.Timestamps)
	var args []string
	var outputPath string
	runFromHome := false
	switch resolved.flavor {
	case localWhisperFlavorCPP:
		// An empty model is valid for whisper.cpp and resolves in tiers —
		// WHISPER_MODEL, then the conventional on-disk locations, then
		// whisper-cli's own default. See resolveDefaultWhisperCPPModel.
		if model == "" {
			model = resolveDefaultWhisperCPPModel()
		}
		model = expandTildePath(model)
		args, outputPath = whisperCPPArgs(input.Name(), outDir, model, req.Task, req.Language, timestamps)
		runFromHome = true
	default:
		args, outputPath = whisperOpenAIArgs(input.Name(), outDir, model, req.Task, req.Language, timestamps)
	}

	runCtx, cancel := context.WithTimeout(ctx, localWhisperTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, resolved.path, args...)
	if runFromHome {
		// whisper-cli resolves its default model ("models/ggml-base.en.bin")
		// against the process working directory; anchor that to the user's
		// home so the documented ~/models location works from a GUI app whose
		// own CWD is "/". Input and output paths above are absolute, so this
		// changes nothing else.
		if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
			cmd.Dir = home
		}
	}
	// whisper prints progress and diagnostics on stderr; combining it keeps a
	// failure message useful without a second pipe to drain.
	output, err := cmd.CombinedOutput()
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return GeneratedTranscript{}, fmt.Errorf("whisper timed out after %s: %s", localWhisperTimeout, truncateLocalToolOutput(output))
		}
		return GeneratedTranscript{}, fmt.Errorf("whisper failed: %v: %s", err, truncateLocalToolOutput(output))
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return GeneratedTranscript{}, fmt.Errorf("whisper produced no transcript at %s: %s", outputPath, truncateLocalToolOutput(output))
	}
	if timestamps == "" {
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return GeneratedTranscript{}, fmt.Errorf("whisper returned an empty transcript: %s", truncateLocalToolOutput(output))
		}
		return GeneratedTranscript{Text: text}, nil
	}
	// Timestamped modes parse the structured output back into chunks, and the
	// plain text always comes FROM the chunks — the raw file is a VTT/JSON
	// document, not prose (conv_4fe9e638: feeding the raw .vtt through made
	// the plain transcript a 6.9KB WebVTT dump). Both local flavors serve
	// word- and segment-level natively, so the produced level is the requested
	// one.
	var chunks []transcriptChunk
	plain := ""
	if resolved.flavor == localWhisperFlavorCPP {
		chunks = parseWhisperVTT(raw)
	} else {
		plain, chunks = parseWhisperOpenAIJSON(raw, timestamps == "words")
	}
	if plain == "" {
		plain = joinTranscriptChunkText(chunks)
	}
	if plain == "" {
		return GeneratedTranscript{}, fmt.Errorf("whisper returned an empty transcript: %s", truncateLocalToolOutput(output))
	}
	transcript := GeneratedTranscript{Text: plain, Timestamps: timestamps}
	if rendered := renderTimestampedChunks(chunks); rendered != "" {
		transcript.TimestampedText = rendered
		transcript.Chunks = chunks
	} else {
		transcript.Timestamps = ""
	}
	return transcript, nil
}

// truncateLocalToolOutput bounds a CLI's combined output inside an error
// message so a chatty failure can't flood the chat reply.
func truncateLocalToolOutput(output []byte) string {
	const limit = 2000
	text := strings.TrimSpace(string(output))
	if len(text) > limit {
		return text[:limit] + "…"
	}
	if text == "" {
		return "(no output)"
	}
	return text
}

// whisperOpenAIArgs builds the openai-whisper CLI invocation: it loads audio
// via ffmpeg, downloads the named model on first use, and writes the
// transcript into --output_dir. The model is optional — the CLI defaults to
// "small". A plain turn writes <input-basename>.txt; a timestamped turn
// switches to json (segments carry start/end, and --word_timestamps True adds
// per-word entries). Returns the args and the expected transcript path.
func whisperOpenAIArgs(input, outDir, model, task, language, timestamps string) ([]string, string) {
	format, ext := "txt", ".txt"
	if timestamps != "" {
		format, ext = "json", ".json"
	}
	args := []string{input, "--output_format", format, "--output_dir", outDir}
	if timestamps == "words" {
		args = append(args, "--word_timestamps", "True")
	}
	if model = strings.TrimSpace(model); model != "" {
		args = append(args, "--model", model)
	}
	if language = strings.TrimSpace(language); language != "" {
		args = append(args, "--language", language)
	}
	if strings.TrimSpace(task) == "translate" {
		args = append(args, "--task", "translate")
	}
	return args, transcriptPathForInput(outDir, input, ext)
}

// whisperCPPArgs builds the whisper.cpp (`whisper-cli`) invocation; -otxt plus
// -of place the transcript at <output-filebase>.txt. A model is passed via -m
// only when set: whisper-cli never runs model-less, but an empty value here
// deliberately omits -m so whisper-cli applies its own default resolution —
// "models/ggml-base.en.bin" relative to its working directory (the runner
// anchors that to the user's home, so ~/models/ggml-base.en.bin works). This
// mirrors how the CLI behaves when invoked bare, which is the behavior users
// of an existing whisper.cpp checkout expect; its own error surfaces verbatim
// when no default resolves.
func whisperCPPArgs(input, outDir, model, task, language, timestamps string) ([]string, string) {
	base := strings.TrimSuffix(transcriptPathForInput(outDir, input, ".txt"), ".txt")
	outputFlag, ext := "-otxt", ".txt"
	if timestamps != "" {
		outputFlag, ext = "-ovtt", ".vtt"
	}
	var args []string
	if model = strings.TrimSpace(model); model != "" {
		args = append(args, "-m", model)
	}
	args = append(args, "-f", input, outputFlag, "-of", base)
	if timestamps == "words" {
		// One word per cue — whisper.cpp's word-timestamp recipe: cap the
		// segment length at one character and split on word boundaries.
		args = append(args, "-ml", "1", "-sow")
	}
	if language = strings.TrimSpace(language); language != "" {
		args = append(args, "-l", language)
	}
	if strings.TrimSpace(task) == "translate" {
		args = append(args, "-tr")
	}
	return args, base + ext
}

// expandTildePath expands a leading "~" or "~/" to the user's home directory
// and leaves every other string untouched — "~user/...", relative paths
// (whisper-cli resolves those against its working directory), absolute paths,
// openai-whisper sizes like "large-v3". whisper-cli opens its -m model with
// plain fopen, which does no shell-style expansion, so a configured "~/..."
// path must be absolutized before it reaches the CLI (conv_6bce6645: a
// literal "~/.cache/whisper.cpp/ggml-large-v3.bin" failed with exit status 3
// even though the file existed).
func expandTildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// resolveDefaultWhisperCPPModel fills in an empty whisper.cpp model in
// priority order:
//
//	WHISPER_MODEL from Atelier's environment
//	~/.whisper-base.en.bin                  (user-placed model in the home root)
//	~/.cache/whisper.cpp/ggml-base.en.bin   (the whisper.cpp cache convention)
//
// "" means nothing resolved — the runner then omits -m and lets whisper-cli
// apply its own default (models/ggml-base.en.bin, anchored to the home
// working directory the runner sets).
func resolveDefaultWhisperCPPModel() string {
	if model := whisperModelFromEnv(); model != "" {
		return model
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	for _, candidate := range []string{
		filepath.Join(home, ".whisper-base.en.bin"),
		filepath.Join(home, ".cache", "whisper.cpp", "ggml-base.en.bin"),
	} {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// whisperModelEnvVar is the conventional whisper.cpp model-path environment
// variable. whisper-cli itself does not read it (verified against the Homebrew
// build — its only env hook is WHISPER_ARG_DEVICE), but Atelier honors it on
// whisper-cli's behalf as the first tier of empty-model resolution (see
// resolveDefaultWhisperCPPModel). Only meaningful when Atelier was launched
// with the variable set: a terminal or `wails dev` launch inherits shell
// exports, a Dock/Finder launch does not (use
// `launchctl setenv WHISPER_MODEL <path>` to reach GUI launches).
const whisperModelEnvVar = "WHISPER_MODEL"

// whisperModelFromEnv returns the WHISPER_MODEL value when it holds a usable
// path, else "".
func whisperModelFromEnv() string {
	return strings.TrimSpace(os.Getenv(whisperModelEnvVar))
}

// transcriptPathForInput mirrors both CLIs' naming: the transcript lands at
// <outDir>/<input basename without extension><ext> — both CLIs derive the
// transcript file name from the input's, changing only the extension.
func transcriptPathForInput(outDir, input, ext string) string {
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	return filepath.Join(outDir, base+ext)
}

// parseWhisperVTT extracts the cue entries of a whisper.cpp WebVTT transcript:
// a timing line ("00:00:00.000 --> 00:00:01.520") followed by the cue text
// until a blank line. Headers ("WEBVTT", "NOTE", metadata) carry no "-->" and
// are skipped naturally.
func parseWhisperVTT(data []byte) []transcriptChunk {
	var chunks []transcriptChunk
	var current *transcriptChunk
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.Contains(line, "-->") {
			start, end, ok := parseVTTTimings(line)
			if !ok {
				current = nil
				continue
			}
			chunks = append(chunks, transcriptChunk{Start: start, End: &end})
			// Points at the slice's last element; the next append may
			// reallocate, but by then `current` is already detached.
			current = &chunks[len(chunks)-1]
			continue
		}
		if current == nil {
			continue
		}
		if strings.TrimSpace(line) == "" {
			current = nil
			continue
		}
		if current.Text != "" {
			current.Text += " "
		}
		current.Text += strings.TrimSpace(line)
	}
	return chunks
}

// parseVTTTimings splits a VTT cue timing line into its start and end seconds.
// Cue settings after the end timestamp are ignored.
func parseVTTTimings(line string) (float64, float64, bool) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, ok := parseClockTimestamp(strings.TrimSpace(parts[0]))
	if !ok {
		return 0, 0, false
	}
	endToken := strings.TrimSpace(parts[1])
	if idx := strings.IndexAny(endToken, " \t"); idx >= 0 {
		endToken = endToken[:idx]
	}
	end, ok := parseClockTimestamp(endToken)
	if !ok {
		return 0, 0, false
	}
	return start, end, true
}

// parseClockTimestamp reads VTT/SRT-style clock values — HH:MM:SS.mmm with the
// hours segment optional — as seconds.
func parseClockTimestamp(token string) (float64, bool) {
	if token == "" {
		return 0, false
	}
	negative := strings.HasPrefix(token, "-")
	segments := strings.Split(strings.TrimPrefix(token, "-"), ":")
	if len(segments) < 2 || len(segments) > 3 {
		return 0, false
	}
	seconds := 0.0
	for _, segment := range segments {
		value, err := strconv.ParseFloat(segment, 64)
		if err != nil || value < 0 {
			return 0, false
		}
		seconds = seconds*60 + value
	}
	if negative {
		seconds = -seconds
	}
	return seconds, true
}

// parseWhisperOpenAIJSON reads the pip `whisper` CLI's --output_format json
// document. Words mode prefers segments[].words (emitted alongside
// --word_timestamps True); segments mode uses segments[].{start,end,text}.
// The document's top-level text is the plain transcript.
func parseWhisperOpenAIJSON(data []byte, words bool) (string, []transcriptChunk) {
	var payload struct {
		Text     string `json:"text"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
			Words []struct {
				Word  string  `json:"word"`
				Start float64 `json:"start"`
				End   float64 `json:"end"`
			} `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", nil
	}
	var chunks []transcriptChunk
	for _, segment := range payload.Segments {
		if words && len(segment.Words) > 0 {
			for _, word := range segment.Words {
				end := word.End
				chunks = append(chunks, transcriptChunk{Start: word.Start, End: &end, Text: word.Word})
			}
			continue
		}
		end := segment.End
		chunks = append(chunks, transcriptChunk{Start: segment.Start, End: &end, Text: segment.Text})
	}
	return strings.TrimSpace(payload.Text), chunks
}

// joinTranscriptChunkText concatenates chunk texts with single spaces — the
// plain-transcript fallback when a structured output carries chunks but no
// top-level text.
func joinTranscriptChunkText(chunks []transcriptChunk) string {
	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if trimmed := strings.TrimSpace(chunk.Text); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

// decodeAudioDataURL splits a data:audio/... URL into its bytes and media
// type, delegating to the generic media decoder. Parameters after the media
// type (";codecs=opus", ";base64") are ignored; the payload must be standard
// base64.
func decodeAudioDataURL(dataURL string) ([]byte, string, error) {
	return decodeMediaDataURL(dataURL)
}

// decodeMediaDataURL splits any data:... URL into its bytes and media type —
// the shared decoder for every local CLI tool's attached media (whisper audio,
// ffmpeg video/audio). Parameters after the media type (";codecs=opus",
// ";base64") are ignored; the payload must be standard base64.
func decodeMediaDataURL(dataURL string) ([]byte, string, error) {
	trimmed := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(trimmed, "data:") {
		return nil, "", errors.New("attached media is not a data URL")
	}
	comma := strings.Index(trimmed, ",")
	if comma < 0 {
		return nil, "", errors.New("attached media data URL has no payload")
	}
	mediaType := strings.TrimSpace(trimmed[:comma][len("data:"):])
	if idx := strings.Index(mediaType, ";"); idx >= 0 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}
	data, err := base64.StdEncoding.DecodeString(trimmed[comma+1:])
	if err != nil {
		return nil, "", fmt.Errorf("attached media payload is not valid base64: %w", err)
	}
	return data, mediaType, nil
}

// localFFmpegTimeout bounds one local ffmpeg/ffprobe invocation. Local
// transforms are CPU-bound and a re-encode of a long clip can legitimately run
// for minutes, so this exists to reap a wedged process — not to enforce
// latency. Context cancellation (a cancelled turn) propagates immediately, and
// the harness's own wall-time budget bounds the planned-tool path.
const localFFmpegTimeout = 10 * time.Minute

// runLocalFFmpeg executes the locally installed ffmpeg CLI with args. The
// binary is re-resolved per call, so a Settings change takes effect on the next
// tool call without rebuilding the gateway. ffmpeg's stderr carries progress
// and diagnostics, so the combined output rides any error message (capped).
func runLocalFFmpeg(ctx context.Context, config AppConfig, args []string) error {
	resolved, ok := resolveLocalFFmpegBinary(config)
	if !ok {
		return errors.New("no local ffmpeg CLI found — install ffmpeg (brew install ffmpeg), or clear the configured binary override in Settings → Video Tools")
	}
	_, err := runLocalMediaCLI(ctx, resolved, "ffmpeg", localFFmpegTimeout, args)
	return err
}

// runLocalFFprobe executes the locally installed ffprobe CLI with args and
// returns its combined output — the metadata sibling the ffmpeg tools use for
// codec/container decisions (probe_media, join copy-vs-reencode, extract
// copy-vs-convert). JSON output lands on stdout; diagnostics on stderr.
func runLocalFFprobe(ctx context.Context, config AppConfig, args []string) ([]byte, error) {
	resolved, ok := resolveLocalFFprobeBinary(config)
	if !ok {
		return nil, errors.New("no local ffprobe CLI found — it ships with ffmpeg (brew install ffmpeg)")
	}
	return runLocalMediaCLI(ctx, resolved, "ffprobe", localFFmpegTimeout, args)
}

// localImageToolTimeout bounds one sips/ImageMagick invocation. Image
// transforms are fast; like the other local CLIs this exists to reap a wedged
// process, not to enforce latency.
const localImageToolTimeout = 5 * time.Minute

// runLocalSips executes the macOS sips CLI with args, re-resolved per call so
// a Settings change applies without rebuilding the gateway.
func runLocalSips(ctx context.Context, config AppConfig, args []string) ([]byte, error) {
	resolved, ok := resolveLocalSipsBinary(config)
	if !ok {
		return nil, errors.New("no sips CLI found — it ships with macOS; check Settings → Image Tools for a configured override")
	}
	return runLocalMediaCLI(ctx, resolved, "sips", localImageToolTimeout, args)
}

// runLocalImageMagick executes the detected ImageMagick CLI (magick or
// convert) with args, re-resolved per call so a Settings change applies
// without rebuilding the gateway.
func runLocalImageMagick(ctx context.Context, config AppConfig, args []string) ([]byte, error) {
	resolved, ok := resolveLocalImageMagickBinary(config)
	if !ok {
		return nil, errors.New("no ImageMagick CLI found — install one (brew install imagemagick), or clear the configured binary override in Settings → Image Tools")
	}
	return runLocalMediaCLI(ctx, resolved, "imagemagick", localImageToolTimeout, args)
}

// runLocalMediaCLI is the shared exec body for the local media CLIs: a bounded
// context, combined output, and an error that embeds the (capped) output so a
// chatty ffmpeg failure is still useful in chat.
func runLocalMediaCLI(ctx context.Context, resolved resolvedLocalBinary, label string, timeout time.Duration, args []string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := exec.CommandContext(runCtx, resolved.path, args...).CombinedOutput()
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("%s timed out after %s: %s", label, timeout, truncateLocalToolOutput(output))
		}
		return output, fmt.Errorf("%s failed: %v: %s", label, err, truncateLocalToolOutput(output))
	}
	return output, nil
}
