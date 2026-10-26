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

// generationForceToolTask is the toolTask the harness hands the planner when it
// force-tools a generation mode triage left un-tooled even though the registry
// carries the matching generate_* tool: it names the deliverable so the plan
// emits the call. The video case names extension explicitly because weak triage
// models keep looking for a dedicated extend tool that does not exist —
// generate_video IS the extension path.
func generationForceToolTask(mode string) string {
	switch mode {
	case "video":
		return "Generate the requested video using the generate_video tool; extending or continuing an attached or recent clip is also a generate_video call."
	case "audio":
		return "Generate the requested audio using the generate_speech, generate_sound, generate_voice, extend_audio, or add_sound_effects tool (add_sound_effects when the sound is requested against a video)."
	default:
		return "Generate the requested media using the matching generate_* tool."
	}
}

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
		return []string{"generate_speech", "generate_sound", "generate_voice", "extend_audio", "add_sound_effects"}
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
// queued render. Empty for non-generation modes. The airplane variant names
// airplane mode as the cause — "add a fal.ai key" would be false advice while
// the mode blocks every cloud provider. speechLocal (the Ollama speech route,
// regardless of the mode) appends the local text-to-speech remedy to the audio
// case: the gap firing there means the slot is empty, and choosing one needs
// no key and survives airplane mode.
func mediaGenUnavailableNote(mode string, airplane bool, speechLocal bool) string {
	kind := generationMediaKindPhrase(mode)
	if kind == "" {
		return ""
	}
	localSpeech := ""
	if mode == "audio" && speechLocal {
		localSpeech = " Speech generation can also run locally: choosing an Ollama speech model (e.g. phonon) in Settings → Models, under Speech Provider, enables it with no API key — airplane mode included."
	}
	if airplane {
		return "Atelier note: the user's latest request asks Atelier to generate or extend " + kind +
			", but airplane mode is on, so Atelier will not contact a cloud provider and has no local tool that can do it. " +
			"Do not claim the " + kind + " was created, queued, or is rendering, and do not attempt it through other tools. " +
			"Tell the user plainly that " + kind + " generation needs a cloud provider, which airplane mode blocks: turning it off in Settings → Providers re-enables it." +
			localSpeech
	}
	return "Atelier note: the user's latest request asks Atelier to generate or extend " + kind +
		", but no " + kind + " generation model is configured on this machine, so Atelier has no tool that can do it. " +
		"Do not claim the " + kind + " was created, queued, or is rendering, and do not attempt it through other tools. " +
		"Tell the user plainly that " + kind + " generation needs a one-time setup: add a fal.ai API key and choose a " + kind +
		" model in Settings → Providers, and Atelier enables it automatically on the next message." +
		localSpeech
}

// mediaGenFallbackNotice returns the deterministic one-line blockquote for the
// chat reply when a generation capability is missing and the final model's
// answer did not already name the remedy — mirroring mediaEditFallbackNotice.
// Empty when the capability is available, the mode is not a generation mode, or
// the answer already covered the remedy (fal.ai or, on the local speech route,
// Ollama).
func mediaGenFallbackNotice(unavailable bool, mode string, assistantContent string, airplane bool, speechLocal bool) string {
	if !unavailable {
		return ""
	}
	kind := generationMediaKindPhrase(mode)
	if kind == "" {
		return ""
	}
	lower := strings.ToLower(assistantContent)
	localSpeech := mode == "audio" && speechLocal
	if airplane {
		if strings.Contains(lower, "airplane mode") {
			return ""
		}
		if localSpeech && strings.Contains(lower, "ollama") {
			return ""
		}
		notice := "> ⚠️ " + kind + " generation is unavailable while airplane mode is on — turn it off in Settings → Providers to use cloud " + kind + " models."
		if localSpeech {
			notice += " Speech can also run locally — pick an Ollama speech model in Settings."
		}
		return notice
	}
	if strings.Contains(lower, "fal.ai") || strings.Contains(lower, "settings → providers") {
		return ""
	}
	if localSpeech && strings.Contains(lower, "ollama") {
		return ""
	}
	notice := "> ⚠️ " + kind + " generation isn't set up yet — add a fal.ai API key and choose a " + kind +
		" model in Settings → Providers, and Atelier picks it up automatically."
	if localSpeech {
		notice += " Or set Speech Provider to Ollama and pick a local speech model (e.g. phonon)."
	}
	return notice
}
