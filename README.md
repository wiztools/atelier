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
