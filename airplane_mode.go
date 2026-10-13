package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Airplane mode (AppConfig.AirplaneMode) is the offline guarantee: while it
// is on, Atelier itself never contacts a cloud service. Chat runs on local
// providers only (Ollama, an OpenAI-compatible server on loopback), media
// generation reduces to its local backends (ffmpeg, sips, ImageMagick, local
// whisper), and the self-updater stands down. Enforcement lives at the seams
// every external call already funnels through — ProviderRegistry.Resolve for
// chat, the tool gates in tools_registry.go for media, resolveTranscription
// Provider for speech-to-text, and checkForUpdates for the updater — so a
// per-conversation model override or a stale Settings panel cannot route
// around it. The policy is never to silently reroute: a cloud-pinned role or
// tool is refused with an error (or simply not offered) that names the fix.

// errAirplaneModeActive is the sentinel every airplane refusal wraps, so
// callers and tests can tell "blocked by airplane mode" apart from other
// config errors without string matching.
var errAirplaneModeActive = errors.New("airplane mode is on")

// airplaneModeActive reports whether the given config has airplane mode
// enabled. Config already flows to every enforcement site (the engine, the
// gateway, the tool gates, the editor sessions), so this stays a pure read —
// the only disk-reading path is airplaneDiskConfig below.
func airplaneModeActive(config AppConfig) bool {
	return config.AirplaneMode
}

// airplaneDiskConfig loads the on-disk config for the two enforcement sites
// that hold no config in hand (ProviderRegistry.Resolve and the updater's
// check path). A variable so TestMain can pin it, keeping provider resolution
// and update behavior machine-independent in tests (the localBinaryLookPath
// pattern). A config that cannot be read fails open — the same posture as
// loadAppConfig itself, which falls back to defaults (airplane off) on a
// missing file.
var airplaneDiskConfig = func() (AppConfig, bool) {
	config, err := loadAppConfig()
	if err != nil {
		return AppConfig{}, false
	}
	return config, config.AirplaneMode
}

// airplaneChatProviderError refuses a chat provider airplane mode cannot
// allow: OpenRouter is a cloud service, and the local providers are only
// local if their endpoint is. ollamaBaseURL is the request's base URL (empty
// means the configured Ollama endpoint); the OpenAI-compatible endpoint
// always comes from config. RunChatStream runs this up front for the primary
// and harness roles so a blocked turn fails before triage burns anything,
// and Resolve runs it again as the outer belt for every direct call path.
func airplaneChatProviderError(config AppConfig, providerID, ollamaBaseURL string) error {
	if !airplaneModeActive(config) {
		return nil
	}
	switch providerID {
	case "openrouter":
		return fmt.Errorf("%w: the %s model runs on OpenRouter, a cloud service — switch it to Ollama or a local OpenAI-compatible server in Settings → Models, or turn airplane mode off in Settings → Providers", errAirplaneModeActive, providerID)
	case "openai-compatible":
		if !isLoopbackEndpoint(config.Providers.OpenAICompatible.BaseURL) {
			return fmt.Errorf("%w: the OpenAI-compatible endpoint %q is not on this machine — point it at a local server in Settings → Providers, or turn airplane mode off", errAirplaneModeActive, config.Providers.OpenAICompatible.BaseURL)
		}
	case "ollama":
		endpoint := ollamaBaseURL
		if strings.TrimSpace(endpoint) == "" {
			endpoint = config.Providers.Ollama.BaseURL
		}
		if !isLoopbackEndpoint(endpoint) {
			return fmt.Errorf("%w: the Ollama endpoint %q is not on this machine — point it at a local server in Settings → Providers, or turn airplane mode off", errAirplaneModeActive, endpoint)
		}
	}
	return nil
}

// isLoopbackEndpoint reports whether the URL's host is this machine: loopback
// by name (localhost) or by address (IPv4 127/8, IPv6 ::1). Empty means the
// caller's configured default, which airplane mode treats as local — the
// defaults are loopback endpoints and mergeAppConfig normalizes to them.
func isLoopbackEndpoint(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// airplaneMediaError refuses a media-generation tool call airplane mode
// cannot serve. Every backend except generate_image's is cloud-only (fal.ai
// or Replicate), so those refuse outright; generate_image refuses only when
// its routed backend is a cloud one — an Ollama or local OpenAI-compatible
// image model keeps working. The tool gates keep cloud tools out of the
// registry so the planner cannot plan them; this is the belt for calls that
// reach the gateway wrappers without a plan.
func airplaneMediaError(config AppConfig, tool string) error {
	if !airplaneModeActive(config) {
		return nil
	}
	if tool == "generate_image" {
		switch imageGenerationProvider(config) {
		case "ollama", "openai-compatible":
			return nil
		}
	}
	return fmt.Errorf("%w: %s runs on a cloud media provider — turn airplane mode off in Settings → Providers to use it", errAirplaneModeActive, tool)
}

// airplaneInpaintError refuses the image editor's mask-inpainting operation:
// it is provider-backed (fal.ai or Replicate) with no local backend, so
// airplane mode has nothing to route it to. The editors' other operations
// (crop, trim, reframe, audio trim) are local ffmpeg/sips work and stay.
func airplaneInpaintError(config AppConfig) error {
	if !airplaneModeActive(config) {
		return nil
	}
	return fmt.Errorf("%w: inpainting runs on a cloud provider — turn airplane mode off in Settings → Providers to use it, or use the editor's local crop tool", errAirplaneModeActive)
}
