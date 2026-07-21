# Atelier

**Your desktop AI workshop — agentic chat and media generation, local or cloud.** Atelier wraps a model in a real agent loop — triage, planning, tool use, and per-action permission gates — so it can read files, run commands, and generate or transcribe media. Chat runs against local Ollama or cloud OpenRouter (with image and audio input attachments); images, video, audio, and transcription run locally (Ollama, images only) or via fal.ai. Cloud API keys are kept in the OS keychain; local-only stays fully offline.

It's a desktop app (Go + Wails + React), MIT licensed, and runs against your own Ollama endpoint or the cloud providers you configure.

<!-- TODO: add a hero screenshot showing a tool-use turn or the permission-approval dialog -->
<!-- ![Atelier](docs/screenshot.png) -->

> Status: early and macOS-first. Cross-platform builds (Windows/Linux) are supported by Wails and on the roadmap. Contributions welcome — see [Contributing](#contributing).

## How the harness works

Every turn runs through an agentic loop rather than a single model call:

- **Harness-model-first triage** — a configurable harness model first decides whether the turn even needs tools (`{needsTools, toolTask, reason}`). Knowledge questions are answered directly; only real work spins up the planner. This inverts the usual "always-tool-first" pattern and keeps simple turns fast. (The harness model defaults to the primary chat model and runs on either provider.)
- **Bounded planning loop** — when tools are needed, a separate tool model plans and executes actions for up to 3 rounds, within a 2-minute wall-clock budget and at most 3 tool calls per round. Tool results are fed back as `role:"tool"` messages so the planner re-plans on evidence instead of firing once and stopping.
- **Per-action permission gates** — write and exec actions require explicit approval in the UI. Read-only tools run without prompting.
- **Evidence-aware responses** — the final response model is told (in code, not by the planner) which tools actually ran and what failed, so it can't silently claim success that didn't happen.
- **Context management** — an explicit `num_ctx` (default 8192) is sent on every call, and the oldest history is trimmed to fit rather than letting Ollama truncate silently.

### Built-in tools

| Tool | Access | Notes |
| --- | --- | --- |
| `list_files` | read-only | Inspect files and directories within the workspace root. |
| `read_file` | read-only | Read text files, with a configurable size limit. |
| `write_file` | gated | Write or overwrite files within the workspace root; requires approval. |
| `run_command` | gated | Allowlisted commands (`cat`, `echo`, `find`, `grep`, `head`, `ls`, `pwd`, `rg`, `tail`, `wc`); unlisted or write/exec operations require permission. |
| `generate_image` | read-only | Invokes the configured image model (Ollama or fal.ai); registered only when an image model is set. |
| `generate_video` | read-only | fal.ai text-to-video or image-to-video (animates an attached image); registered only when a fal video model and key are configured. |
| `generate_audio` | read-only | fal.ai text-to-speech, music, or sound-effect generation; registered only when a fal audio model and key are configured. |
| `transcribe_audio` | read-only | fal.ai speech-to-text (fal-ai/wizper by default) on an attached audio clip; registered only when a fal key is configured. The transcript flows back as tool evidence. |
| `upscale_image` | read-only | fal.ai image upscaling on an attached image; registered only when a fal key is configured. |

All file and command operations are scoped to a configurable, per-conversation workspace root (default `~/Documents`), pinned when the conversation is created.

### Skills

Drop a `SKILL.md` file into `~/.agents/skills/<name>/` or `~/.atelier/skills/<name>/` and the harness can auto-select it and inject its instructions into the planner — domain-specific workflows without hardcoding them.

## Capabilities

**Chat providers**
- **Ollama** (local) — configurable endpoint, defaulting to `http://localhost:11434`. Reads models from `/api/tags`, detects vision and image-generation capability via `/api/show`, and streams chat from `/api/chat` into the UI through Wails runtime events.
- **OpenRouter** (cloud) — any OpenAI-compatible chat model. Send vision and audio input as OpenAI content parts (`image_url`, `input_audio`); the OpenRouter adapter translates the harness's canonical message shape into the wire format both providers accept.

**Multimodal input**
- Attach **images** (file picker, paste, or drag-and-drop) — sent as `image_url` parts on OpenRouter and base64 `images` arrays on Ollama. Vision-capable models receive them directly.
- Attach **audio** — sent as `input_audio` parts on OpenRouter, or transcribed via the `transcribe_audio` tool on any provider. The same attach button handles both kinds.

**Media generation (via fal.ai or Ollama)**
- **Images** — locally via Ollama (`/api/generate`) or via fal.ai, with configurable width, height, and steps.
- **Video** — fal.ai text-to-video and image-to-video (animates an attached image).
- **Audio** — fal.ai text-to-speech, music, and sound effects.
- **Transcription** — fal.ai speech-to-text (`fal-ai/wizper`) on attached audio.
- **Upscaling** — fal.ai image upscaling on attached images.

**Agent loop & tooling**
- Skills — see [Skills](#skills) below.
- Per-conversation workspace root, pinned at creation and immutable thereafter.

**Persistence**
- Conversations and generated artifacts under `~/.atelier/history`, with full per-turn telemetry (triage decision, plans, tool calls, results, and tool notices).
- Cloud API keys (OpenRouter, fal.ai) stored in the OS keychain — never written to config.json.

## Prerequisites

- [Ollama](https://ollama.com) running locally with at least one chat model pulled.
- [Go 1.24+](https://go.dev/dl/)
- [Node.js](https://nodejs.org) (for the Vite frontend)
- [Wails CLI v2](https://wails.io/docs/gettingstarted/installation): `go install github.com/wailsapp/wails/v2/cmd/wails@latest`

## Development

```sh
wails dev
```

Wails runs the Vite frontend and the Go desktop backend together. In browser development mode, Wails exposes a dev server at `http://localhost:34115`.

## Build

```sh
go test ./...
npm run build --prefix frontend
wails build
```

The packaged macOS app is produced at:

```sh
build/bin/Atelier.app
```

## Ollama models

Recommended local starting points:

- Chat: `gpt-oss:20b`, `mistral-small3.1:latest`, or another general model.
- Vision input: `llava:7b` or another multimodal model.
- Image generation: `x/z-image-turbo:latest` or `x/flux2-klein:4b`.

Video generation, audio generation, transcription, and image upscaling run via fal.ai (there are no local Ollama endpoints for these) — configure a fal API key in Settings to enable them.

## Configuration

Atelier stores local preferences in:

```sh
~/.atelier/config.json
```

The file is versioned and hierarchical so more providers, model profiles, generation defaults, and UI preferences can be added without flattening the schema. The `models` block selects which provider runs the primary chat model, the harness model (triage, skill selection, planning), and image generation; the per-provider `models` blocks name the concrete model IDs. Cloud API keys live in the OS keychain, not here.

```json
{
  "version": 1,
  "storage": {
    "root": "~/.atelier",
    "history": "~/.atelier/history",
    "artifacts": "~/.atelier/history"
  },
  "models": {
    "primaryProvider": "ollama",
    "harnessProvider": "ollama",
    "imageProvider": "ollama"
  },
  "providers": {
    "ollama": {
      "baseURL": "http://localhost:11434",
      "models": {
        "primary": "mistral-small3.1:latest",
        "harness": "mistral-small3.1:latest",
        "image": "x/z-image-turbo:latest"
      },
      "numCtx": 8192
    },
    "openrouter": {},
    "fal": {
      "videoModel": "fal-ai/kling-video/v2/master/text-to-video",
      "audioModel": "fal-ai/elevenlabs/tts/multilingual-v2",
      "transcribeModel": "fal-ai/wizper",
      "upscaleModel": "fal-ai/esrgan"
    }
  },
  "prompts": {
    "system": "You are Atelier, a precise local AI collaborator."
  },
  "generation": {
    "image": {
      "width": 768,
      "height": 768,
      "steps": 24
    },
    "video": {
      "duration": "5",
      "aspectRatio": "16:9"
    }
  },
  "ui": {
    "mode": "chat"
  }
}
```

On startup, Atelier creates the storage root and history scaffold:

```text
~/.atelier/
  config.json
  history/
    conversations/
    indexes/
```

Generated media (images, video, audio) and attached inputs (audio attachments) are stored as conversation folders with `conversation.json`, turn JSON files, and artifact files under `artifacts/`.

## Contributing

Atelier is early and single-author — contributions and feedback are welcome. Good areas to jump in:

- Windows/Linux builds and testing (the Wails stack already supports them).
- New harness tools and `SKILL.md` workflows.
- Frontend polish and UX.

Open an issue to discuss a change before a large PR, and run `go test ./...` before submitting.

## License

[MIT](LICENSE) © 2026 WizTools.org
