# AGENTS.md

Guidance for AI agents working in this repository. Atelier is a **desktop AI workshop for agentic chat and image generation, local or cloud** — a macOS-first desktop app (Go + Wails v2 + React/TypeScript) that wraps a model in an agentic loop (triage → planning → tool use → final response) with per-action permission gates. Chat runs against local Ollama or cloud OpenRouter; image generation runs locally (Ollama) or via fal.ai, with cloud keys stored in the OS keychain.

## Commands

```sh
wails dev                          # Run frontend + Go backend together (dev server at :34115)
go test ./...                      # Run all Go tests
npm run build --prefix frontend    # Type-check + build the Vite frontend
wails build                        # Produce build/bin/Atelier.app
./bld.sh                           # One-shot: gofmt -w *.go && go test && npm build && wails build
./release.sh [--dry-run] [--skip-tests] <semver>  # Bump version, build, sign, notarize, tag (macOS)
```

`./bld.sh` runs `gofmt -w *.go` — note the glob is root-level only, so all Go source lives at the repo root in a single `package main`.

Prerequisites: Go 1.24+, Node.js, [Wails CLI v2](https://wails.io) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`), and a running Ollama.

## Repository Layout

Everything is **one flat Go package (`main`) at the repo root**. There are no subpackages. The frontend is a Vite/React app under `frontend/`.

| File | Role |
| --- | --- |
| `main.go` | Wails entry point. Embeds `frontend/dist` via `go:embed`, registers the macOS menu, and mounts a custom asset handler that serves generated image artifacts from disk under the `/atelier-artifact/` URL prefix. |
| `app.go` | The `App` struct — the only type bound to the frontend. Every public method here is a Wails IPC call (auto-generated bindings land in `frontend/wailsjs/go/main/App.js`). Holds config loading/merging (`loadAppConfig`/`mergeAppConfig`/`defaultAppConfig`), streaming lifecycle, permission channels, and provider/keychain wiring. |
| `harness.go` | The core agentic loop (`HarnessEngine`). Largest file — the turn pipeline, planning loop, plan validation, skill selection, tool-result rendering, and harness telemetry (`HarnessRun`/`HarnessStep`). |
| `triage.go` | First-pass routing: the harness model decides `needsTools` + `responseMode` (`text`/`image`/`vision`) before anything else runs. |
| `provider.go` | The `ChatProvider` interface (`ListModels`/`StreamChat`/`CompleteChat`) and `ProviderRegistry` that resolves `"ollama"` or `"openrouter"`. |
| `ollama_client.go` / `ollama_provider.go` | Ollama HTTP client (tags, show, chat stream, generate) and its `ChatProvider` adapter. |
| `openrouter_client.go` / `openrouter_provider.go` | OpenRouter HTTP client and `ChatProvider` adapter. |
| `tools_registry.go` | Tool definitions (`list_files`, `read_file`, `write_file`, `run_command`, `generate_image`) and the JSON schema sent to Ollama for grammar-constrained plan output. |
| `fs_tools.go` | `FilesystemToolLayer` — real file ops and command execution confined to a workspace root, with the command allowlist and path-boundary enforcement. |
| `tool_gateway.go` | `ToolGateway` — permission gating and tool execution, used by both the harness and the direct API methods. |
| `skills.go` | `SKILL.md` discovery (frontmatter parsing), index loading, and the model-driven skill selector. |
| `keychain.go` | OS keyring storage for the OpenRouter API key (`github.com/zalando/go-keyring`). |
| `history_store.go` | Conversation/turn persistence to `~/.atelier/history`. |

Frontend: `frontend/src/App.tsx` is the entire UI (single component, ~React 18 + `react-markdown`). It talks to Go only through the generated `wailsjs/` bindings and Wails runtime events.

## Architecture & Control Flow

A chat turn flows through `App.StreamChat` → `HarnessEngine.RunChatStream`:

1. **Start** — persist the user turn to history, assign a `conversationID`.
2. **Triage** (`triage.go`) — the *harness model* decides `{needsTools, responseMode, toolTask, reason}` with a structured-output JSON schema. Failures **fail safe** to the tool path (`needsTools=true`, `responseMode="text"`) — a wrong fallback costs latency, never correctness.
3. **Planning loop** (`prepareChatTurnLoop`) — only when tools are needed. Up to `harnessChatMaxSteps` (3) rounds, bounded by `harnessChatMaxWallTime` (2 min), at most 3 tool calls per round. The planner emits a JSON plan validated against the tool schema; invalid plans are fed back to the planner as corrections rather than aborting.
4. **Tool execution** (`ToolGateway`) — read-only tools run unattended; `write`/`exec` tools require UI approval via `atelier:tool-permission` events (2-minute timeout, fail-closed).
5. **Final response** — a *different* model (the primary/chat model) streams the user-facing answer. It receives tool observations as **evidence**, never as instructions, and is told (via code-authored system notes in `harness.go`) what actually ran and what failed so it cannot claim success that didn't happen.

### Critical design invariants (don't break these)

- **Planner output is telemetry, never prompt text.** The primary model's system prompt only ever receives code-authored notes (`toolEvidenceSystemNote`, etc.). A brief/reason from a weaker harness model must not cap what the primary model is allowed to know.
- **Tool evidence is delivered as `role:"tool"` messages to the planner** (so it re-plans on evidence), but as a **single `role:"user"` message to the final model** (`toolEvidenceUserMessage`). This is deliberate: the primary model isn't doing native tool-calling, and some providers (Mistral via OpenRouter) reject a bare `tool` role after a `user` role.
- **`num_ctx` is sent explicitly on every call** (`defaultOllamaNumCtx = 8192`). History is trimmed to fit (`truncateChatHistory`) rather than letting Ollama silently truncate from the front; the oldest dropped message gets a `[Earlier conversation was omitted...]` marker.
- **Image base64 never enters model context.** Generated images are stripped from tool-result messages before they reach any model; the harness extracts them separately for the UI and history.
- **An image-generation model cannot produce text/vision.** `responseModelFor` falls back to the harness model for image captions, and `responseProviderFor` forces `"ollama"` in that case (the harness model is always Ollama).
- **Harness model defaults to the primary model** when unset — a one-model setup must still work.

## Conventions

- **Single `package main`, all files at repo root.** No internal subpackages.
- **Config is merged with defaults, never raw.** `mergeAppConfig(config)` fills every unset field from `defaultAppConfig()`. The allowlist (`defaultFilesystemToolAllowedCommands`) and the prompt's command list are read from the same `ConfigFilesystemTool.AllowedCommands`, so they cannot drift.
- **Tool params go directly on the call object, not nested under `args`.** Plan validation produces a specific error (`toolCalls[N].args must be ...; tool parameters like path go directly on the call object`) when a model nests them.
- **All user/storage paths are normalized** via `normalizeStoragePath` (`~` expansion → absolute). The filesystem tool resolves and confines every path to the configured root.
- **IDs are prefixed random hex** (`randomID("conv")`, `"run"`, `"permission"`, `"chat-" + unixnano`).
- **Wails bindings are generated**, not hand-written. `frontend/wailsjs/go/main/App.{js,d.ts}` mirror exported `App` methods; `frontend/wailsjs/go/models.ts` mirrors public structs. Adding a public method to `App` regenerates these on the next `wails dev`/`wails build`.
- **Git commits** use conventional prefixes (`feat:`, `fix:`) in the log.

## Testing

- Standard `go test ./...`. Tests are **table-driven** and live alongside the code (`*_test.go`).
- HTTP is mocked with a `roundTripFunc` (`app_test.go`) injected into an `*http.Client` — no real Ollama/OpenRouter calls in tests.
- `fakeProvider` (`provider_test.go`) implements `ChatProvider` for harness-level tests.
- `app_test.go` is the largest test file (~4000 lines) and covers the full harness pipeline, streaming cleanup, history, tools, and config merging. Helpers like `waitForStreamCleanup` and `harnessStepByKind` exist there.

## Gotchas

- `frontend/dist/` is embedded at compile time (`//go:embed all:frontend/dist` in `main.go`). The frontend must be built (`npm run build --prefix frontend`) before `wails build`; `wails dev` rebuilds it live.
- `version` in `main.go` is `"dev"` unless injected at link time by `release.sh` (`-ldflags "-X main.version=..."`).
- Config lives at `~/.atelier/config.json`; history/artifacts under `~/.atelier/history/`. Skills are loaded from **both** `~/.agents/skills/<name>/SKILL.md` and `~/.atelier/skills/<name>/SKILL.md`.
- The OpenRouter API key is stored in the OS keychain, **not** in config.json. An absent key returns `("", nil)`; callers treat "not configured" and "empty" uniformly.
- `go.mod` has a commented-out `replace` directive for local Wails development — uncomment only when hacking on Wails itself.
- Permission requests time out after 2 minutes and fail closed (denied) if no UI is attached.
