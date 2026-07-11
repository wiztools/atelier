# Atelier

**Your desktop AI workshop — agentic chat and image generation, local or cloud.** Atelier wraps a model in a real agent loop — triage, planning, tool use, and per-action permission gates — so it can read files, run commands, and generate images. Run chat against local Ollama or cloud OpenRouter, and generate images locally (Ollama) or via fal.ai. Cloud API keys are kept in the OS keychain; local-only stays fully offline.

It's a desktop app (Go + Wails + React), MIT licensed, and runs against your own Ollama endpoint or the cloud providers you configure.

<!-- TODO: add a hero screenshot showing a tool-use turn or the permission-approval dialog -->
<!-- ![Atelier](docs/screenshot.png) -->

> Status: early and macOS-first. Cross-platform builds (Windows/Linux) are supported by Wails and on the roadmap. Contributions welcome — see [Contributing](#contributing).

## How the harness works

Every turn runs through an agentic loop rather than a single model call:

- **Chat-model-first triage** — the chat model first decides whether the turn even needs tools (`{needsTools, toolTask, reason}`). Knowledge questions are answered directly; only real work spins up the planner. This inverts the usual "always-tool-first" pattern and keeps simple turns fast.
- **Bounded planning loop** — when tools are needed, a separate tool model plans and executes actions for up to 3 rounds, within a 2-minute wall-clock budget and at most 3 tool calls per round. Tool results are fed back as `role:"tool"` messages so the planner re-plans on evidence instead of firing once and stopping.
- **Per-action permission gates** — write and exec actions require explicit approval in the UI. Read-only tools run without prompting.
- **Evidence-aware responses** — the final response model is told (in code, not by the planner) which tools actually ran and what failed, so it can't silently claim success that didn't happen.
- **Context management** — an explicit `num_ctx` (default 8192) is sent on every call, and the oldest history is trimmed to fit rather than letting Ollama truncate silently.

### Built-in tools

| Tool | Access | Notes |
| --- | --- | --- |
| `list_files` | read-only | Inspect files and directories within the workspace root. |
| `read_file` | read-only | Read text files, with a configurable size limit. |
| `run_command` | gated | Allowlisted commands (`cat`, `echo`, `find`, `grep`, `head`, `ls`, `pwd`, `rg`, `tail`, `wc`); write/exec operations require permission. |
| `generate_image` | read-only | Invokes the configured image model; registered only when an image model is set. |

All file and command operations are scoped to a configurable workspace root (default `~/Documents`).

### Skills

Drop a `SKILL.md` file into `~/.agents/skills/<name>/` or `~/.atelier/skills/<name>/` and the harness can auto-select it and inject its instructions into the planner — domain-specific workflows without hardcoding them.

## Capabilities

- Connects to a configurable Ollama endpoint, defaulting to `http://localhost:11434`.
- Reads local models from `/api/tags` and detects multimodal / image-gen capability via `/api/show`.
- Streams chat from `/api/chat` into the UI through Wails runtime events.
- Sends base64 image attachments for vision-capable chat models.
- Calls `/api/generate` for image generation with configurable width, height, and steps, exposed to the harness as the `generate_image` tool.
- Stores conversations and generated artifacts under `~/.atelier/history`, with full per-turn telemetry (triage decision, plans, tool calls, results).

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

## Configuration

Atelier stores local preferences in:

```sh
~/.atelier/config.json
```

The file is versioned and hierarchical so more providers, model profiles, generation defaults, and UI preferences can be added without flattening the schema. The `models.tools` key names the model that plans and executes tools.

```json
{
  "version": 1,
  "storage": {
    "root": "~/.atelier",
    "history": "~/.atelier/history",
    "artifacts": "~/.atelier/history"
  },
  "providers": {
    "ollama": {
      "baseURL": "http://localhost:11434",
      "models": {
        "chat": "mistral-small3.1:latest",
        "tools": "mistral-small3.1:latest",
        "image": "x/z-image-turbo:latest"
      },
      "numCtx": 8192
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

Image generations are stored as conversation folders with `conversation.json`, turn JSON files, and generated image artifacts.

## Contributing

Atelier is early and single-author — contributions and feedback are welcome. Good areas to jump in:

- Windows/Linux builds and testing (the Wails stack already supports them).
- New harness tools and `SKILL.md` workflows.
- Frontend polish and UX.

Open an issue to discuss a change before a large PR, and run `go test ./...` before submitting.

## License

[MIT](LICENSE) © 2026 WizTools.org
