package main

import "strings"

// Media-generation capability gap (first-run UX, mirroring local_ffmpeg.go and
// local_images.go). When triage routes a turn to a generation mode
// (video/audio) but no matching generate_* tool is configured, the harness has
// no way to produce the media. Left unguided, a small final model fills the
// vacuum with a fabricated "queued / rendering shortly" confirmation for a clip
// that never existed (conv_20a0df2b2db9b4e9ea5a1ad9). These helpers carry the
// same code-authored note + deterministic fallback pattern the local-edit gaps
// use, so the user always learns the capability is absent and how to enable it.

// generationToolNamesByMode maps a triage generation responseMode to the
// generate_* tool names that could satisfy it. A mode is available when the
// registry carries at least one of them. Image is included for completeness;
// in practice image mode is force-routed to the planner before this check (see
// RunChatStream), so only video and audio reach the capability-gap guard.
func generationToolNamesByMode(mode string) []string {
	switch mode {
	case "image":
		return []string{"generate_image"}
	case "video":
		return []string{"generate_video"}
	case "audio":
		return []string{"generate_speech", "generate_sound", "extend_audio"}
	default:
		return nil
	}
}

// generationToolAvailable reports whether the registry can satisfy a generation
// responseMode. It is the capability half of the gap check: triage decides the
// user wants generated media, this decides whether Atelier actually has a tool
// for it. Cross-checking the registry (rather than trusting triage's needsTools
// alone) keeps a triage mistake from injecting a false "not configured" note
// when the tool is in fact present.
func generationToolAvailable(registry HarnessToolRegistry, mode string) bool {
	for _, name := range generationToolNamesByMode(mode) {
		if _, ok := registry.Get(name); ok {
			return true
		}
	}
	return false
}

// generationMediaKindPhrase renders the media noun used in the user- and
// model-facing gap notices. Distinct from generationMediaType ("audio clip"):
// the notices read better with the plain kind ("audio", "video", "images").
func generationMediaKindPhrase(mode string) string {
	switch mode {
	case "video":
		return "video"
	case "audio":
		return "audio"
	case "image":
		return "images"
	default:
		return ""
	}
}

// mediaGenUnavailableNote is the code-authored note delivered to the final
// model — as its own trailing user message, never in the system prompt — when
// triage routed the turn to a generation mode but no matching generate_* tool
// is configured. It states the capability is absent (not broken) and the exact
// remedy to relay, so the model cannot pass off a from-knowledge answer as a
// queued render. Empty for non-generation modes.
func mediaGenUnavailableNote(mode string) string {
	kind := generationMediaKindPhrase(mode)
	if kind == "" {
		return ""
	}
	return "Atelier note: the user's latest request asks Atelier to generate or extend " + kind +
		", but no " + kind + " generation model is configured on this machine, so Atelier has no tool that can do it. " +
		"Do not claim the " + kind + " was created, queued, or is rendering, and do not attempt it through other tools. " +
		"Tell the user plainly that " + kind + " generation needs a one-time setup: add a fal.ai API key and choose a " + kind +
		" model in Settings → Providers, and Atelier enables it automatically on the next message."
}

// mediaGenFallbackNotice returns the deterministic one-line blockquote for the
// chat reply when a generation capability is missing and the final model's
// answer did not already name the remedy — mirroring mediaEditFallbackNotice.
// Empty when the capability is available, the mode is not a generation mode, or
// the answer already covered the remedy.
func mediaGenFallbackNotice(unavailable bool, mode string, assistantContent string) string {
	if !unavailable {
		return ""
	}
	kind := generationMediaKindPhrase(mode)
	if kind == "" {
		return ""
	}
	lower := strings.ToLower(assistantContent)
	if strings.Contains(lower, "fal.ai") || strings.Contains(lower, "settings → providers") {
		return ""
	}
	return "> ⚠️ " + kind + " generation isn't set up yet — add a fal.ai API key and choose a " + kind +
		" model in Settings → Providers, and Atelier picks it up automatically."
}
