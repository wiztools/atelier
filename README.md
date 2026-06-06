# Atelier

Atelier is a Wails + Go desktop harness for local AI work through Ollama.

The first slice focuses on the gap in Ollama Desktop: image-generation models can be driven directly through the Ollama API while the app also supports streamed chat and multimodal image input.

## Current Capabilities

- Connects to a configurable Ollama endpoint, defaulting to `http://localhost:11434`.
- Reads local models from `/api/tags`.
- Streams chat from `/api/chat` into the UI through Wails runtime events.
- Sends base64 image attachments for vision-capable chat models.
- Calls `/api/generate` for experimental image generation with width, height, and steps.
- Normalizes generated base64 image responses into browser-renderable image data URLs.
- Stores image-generation conversations and generated artifacts under `~/.atelier/history`.

## Development

```sh
wails dev
```

Wails will run the Vite frontend and the Go desktop backend together. In browser development mode, Wails exposes a dev server at `http://localhost:34115`.

## Verification

```sh
go test ./...
npm run build --prefix frontend
wails build
```

The packaged macOS app is produced at:

```sh
build/bin/Atelier.app
```

## Ollama Models

Recommended local starting points:

- Chat: `gpt-oss:20b`, `mistral-small3.1:latest`, or another general model.
- Vision input: `llava:7b` or another multimodal model.
- Image generation: `x/z-image-turbo:latest` or `x/flux2-klein:4b`.

## Configuration

Atelier stores local preferences in:

```sh
~/.atelier/config.json
```

The file is versioned and hierarchical so more providers, model profiles, generation defaults, and UI preferences can be added without flattening the schema:

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
        "harness": "mistral-small3.1:latest",
        "image": "x/z-image-turbo:latest"
      }
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
