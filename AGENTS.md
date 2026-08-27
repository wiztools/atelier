# AGENTS.md

Guidance for AI agents working in this repository. Atelier is a **desktop AI workshop for agentic chat and image generation, local or cloud** — a macOS-first desktop app (Go + Wails v2 + React/TypeScript) that wraps a model in an agentic loop (triage → planning → tool use → final response) with per-action permission gates. Chat runs against local Ollama or cloud OpenRouter; image generation runs locally (Ollama) or via fal.ai, with cloud keys stored in the OS keychain.

## Commands

```sh
wails dev                          # Run frontend + Go backend together (dev server at :34115)
go test ./...                      # Run all Go tests
npm run build --prefix frontend    # Type-check + build the Vite frontend
wails build                        # Produce build/bin/Atelier.app
./bld.sh                           # One-shot: gofmt -w *.go && npm build && go test && wails build
./release.sh [--dry-run] [--skip-tests] <semver>  # Bump version, build, sign, notarize, tag (macOS)
```

`./bld.sh` runs `gofmt -w *.go` — note the glob is root-level only, so all Go source lives at the repo root in a single `package main`.

Prerequisites: Go 1.27+, Node.js, [Wails CLI v2](https://wails.io) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`), and a running Ollama.

## Repository Layout

Everything is **one flat Go package (`main`) at the repo root**. There are no subpackages. The frontend is a Vite/React app under `frontend/`.

| File | Role |
| --- | --- |
| `main.go` | Wails entry point. Embeds `frontend/dist` via `go:embed`, registers the macOS menu, and mounts a custom asset handler that serves generated image artifacts from disk under the `/atelier-artifact/` URL prefix. |
| `app.go` | The `App` struct — the only type bound to the frontend. Every public method here is a Wails IPC call (auto-generated bindings land in `frontend/wailsjs/go/main/App.js`). Holds config loading/merging (`loadAppConfig`/`mergeAppConfig`/`defaultAppConfig`), streaming lifecycle, permission channels, and provider/keychain wiring. |
| `harness.go` | The core agentic loop (`HarnessEngine`). Largest file — the turn pipeline, planning loop, plan validation, skill selection, tool-result rendering, and harness telemetry (`HarnessRun`/`HarnessStep`). |
| `triage.go` | First-pass routing: the harness model decides `needsTools` + `responseMode` (`text`/`image`/`vision`) before anything else runs. |
| `provider.go` | The `ChatProvider` interface (`ListModels`/`StreamChat`/`CompleteChat`) and `ProviderRegistry` that resolves `"ollama"`, `"openrouter"`, or `"openai-compatible"`. |
| `ollama_client.go` / `ollama_provider.go` | Ollama HTTP client (tags, show, chat stream, generate) and its `ChatProvider` adapter. |
| `openrouter_client.go` / `openrouter_provider.go` | OpenRouter HTTP client and `ChatProvider` adapter. `strictJSONSchema` derives an OpenAI strict-mode variant of the harness's Ollama-shaped `Format` schema (strip rejected keywords like `maxItems`, promote every property into `required`, widen optionals to nullable unions); Ollama still receives the original. |
| `openai_compatible_client.go` | HTTP client for a local server speaking OpenAI's `/v1/images/generations` shape (LocalAI, a diffusers shim, ...) — the image side of the `"openai-compatible"` provider. Packs results into `ollamaGenerateResponse` data URLs with nil raw JSON; shares `fetchImageAsDataURL` with the fal client. |
| `openai_compatible_chat_provider.go` | `ChatProvider` adapter for the same local server's `/v1/chat/completions` (streaming SSE + non-stream), usable as primary and harness provider. Reuses the OpenAI wire adapters (`openRouterChatBody`/`openRouterMessages`/`openRouterWireMessages`) — they are protocol-level, not openrouter.ai-specific — and maps `delta.reasoning`/`reasoning_content` to `ChatEvent.Thinking`. The bearer key is optional: Resolve never fails on its absence. |
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
2. **Triage** (`triage.go`) — the *harness model* decides `{needsTools, responseMode, toolTask, reason}` with a structured-output JSON schema. Failures **fail safe** to the tool path (`needsTools=true`, `responseMode="text"`) — a wrong fallback costs latency, never correctness. An unreachable harness *provider* (e.g. OpenRouter with no key) is a config error, not a model failure: `harnessProviderUnavailable` reports it before triage rather than burning the turn on the fail-safe rails.
3. **Planning loop** (`prepareChatTurnLoop`) — only when tools are needed. Up to `harnessChatMaxSteps` (3) rounds, bounded by `harnessChatMaxWallTime` (2 min), at most 3 tool calls per round. The planner emits a JSON plan validated against the tool schema; invalid plans are fed back to the planner as corrections rather than aborting.
4. **Tool execution** (`ToolGateway`) — read-only tools run unattended; `write`/`exec` tools require UI approval via `atelier:tool-permission` events (2-minute timeout, fail-closed).
5. **Final response** — a *different* model (the primary/chat model) streams the user-facing answer. It receives tool observations as **evidence**, never as instructions, and is told (via code-authored notes in the message stream — see `toolEvidenceNote`/`toolEvidenceSystemNote` in `harness.go`) what actually ran and what failed so it cannot claim success that didn't happen.

### Critical design invariants (don't break these)

- **Planner output is telemetry, never prompt text.** The primary model's system prompt is never mutated per-turn — no tool-evidence notes, no `toolTask`, nothing. Code-authored notes (`toolEvidenceSystemNote`, etc., via `toolEvidenceNote`) ride in the **message stream** (prepended to the tool-evidence user message, or a trailing user message when no tools ran), so message #0 stays byte-identical across tooled and un-tooled turns and the prefix cache survives. A brief/reason from a weaker harness model must not cap what the primary model is allowed to know.
- **Tool evidence is delivered as `role:"tool"` messages to the planner** (so it re-plans on evidence), but as a **single `role:"user"` message to the final model** (`toolEvidenceUserMessage`). This is deliberate: the primary model isn't doing native tool-calling, and some providers (Mistral via OpenRouter) reject a bare `tool` role after a `user` role.
- **`role:"tool"` is the harness's canonical evidence shape; adapters translate it.** The OpenAI wire format only accepts a tool message when the preceding assistant message carries `tool_calls`, which the format-schema planner never emits. Ollama is lenient; OpenRouter returns `400 tool message has no preceding assistant tool_calls`. `openRouterMessages` rewrites unbacked tool messages into a `role:"user"` observation (shared `toolObservationsPrefix`). Keep this in the **adapter**, not the harness — the harness must not learn which providers are strict.
- **`num_ctx` is sent explicitly on every call** (`defaultOllamaNumCtx = 8192`). History is trimmed to fit (`truncateChatHistory`) rather than letting Ollama silently truncate from the front; the oldest dropped message gets a `[Earlier conversation was omitted...]` marker.
- **Image base64 never enters model context.** Generated images are stripped from tool-result messages before they reach any model; the harness extracts them separately for the UI and history.
- **An image-generation model cannot produce text/vision.** `responseModelFor` falls back to the harness model for image captions, and `responseProviderFor` falls back to that model's provider. Both take the resolved `harnessTarget` rather than reading config: an unset harness model resolves to the *primary* model on the *primary* provider, so a raw `Models.HarnessProvider` read would pair that model with the wrong endpoint.
- **The harness model runs on either provider.** `resolveHarnessTarget` returns model and provider as one `harnessTarget`; never resolve them separately. All three harness calls — triage, skill selection (`selectSkillForTurn`), and planning — go through `completeWithHarnessModel`, never `ollamaClient` directly. `supportsNativeTools` is Ollama-only capability detection, so a non-Ollama harness plans via the format schema.
- **Harness model defaults to the primary model *and* its provider** when unset — a one-model setup must still work, including a cloud-only one with no local Ollama.
- **The workspace root is per-conversation and immutable.** Each conversation pins a `ConfigFilesystemTool.Root` at creation (`HistoryConversation.Workspace`); turn 2+ reads it from the record and ignores `ChatRequest.Workspace`. The single override point is `resolveTurnWorkspace`, called in `StreamChat`/`writeChatConversation` *before* `newHarnessEngine` — it rewrites `config.Tools.Filesystem.Root`, and because the tool registry is cached per-`HarnessEngine` (`toolRegistry()` via `registryOnce`) with each stream building a fresh engine, that one override scopes the registry descriptions, the filesystem layer, and the harness/triage prompts through `h.config`. Do **not** refactor the root out of `workspaceRootPhrase` or the prompt templates — the override makes that unnecessary. Legacy SchemaVersion 1 records are backfilled to the default in `loadForAppend`/`readConversationWorkspace`. The command allowlist stays global; the global `Tools.Filesystem.Root` is the default for new conversations and the fallback for the manual tool methods.
- **Every provider call is a step; every step transition is live.** Each model call (triage, skill, planning, model_call/streaming) records a `HarnessStep` with provider, model, prompt+completion tokens, and a `HarnessRequestSnapshot` (hashes/sizes only — raw prompts are never persisted). `HarnessRun.onUpdate` fires on every `appendStep`/`completeStep`/`complete` and emits the full run snapshot as the `atelier:harness-run` Wails event; the frontend renders these directly and never fabricates in-flight state. `TestHarnessTelemetryCoversEveryModelCall` (in `harness_telemetry_test.go`) pins the convention: one step per intercepted provider HTTP call. Conversation title generation (`generateConversationTitle`, the non-streaming path) is the one sanctioned exception — it runs after the turn and is not part of the run.
- **Token usage lives on model-call steps only.** Bookkeeping steps (`queued`, `saved`, `evaluation`, `tool_call`) carry no token counts — the turn total rides in `providerResponse.tokens` — so summing steps counts each model call exactly once. The frontend's per-model usage fold (`summarizeModelUsage`) filters to `triage`/`skill`/`planning`/`model_call`/`streaming`; keep that set and the Go recording sites in sync.
- **Media consumption is recorded as activity fields, never token rows.** Media models (fal video/audio/image) burn no tokens, so their consumption rides `HarnessToolActivity.Provider`/`Model`/`MediaKind`/`MediaCount`, populated in `defaultHarnessToolActivity`'s type switch from the tool's own result payload — the resolved model after defaulting, not the planner's `Call.Model`. `Provider` (backend attribution: "fal"/"ollama"/"openai-compatible") is filled one layer up in `toolActivityFromResult`, because video/audio generation is fal-only while `generate_image` routes by `config.Models.ImageProvider` — the same field the tool gateway reads via `imageGenerationProvider` — which the per-tool activity builders don't receive. The frontend's media fold (`summarizeRunMediaUsage`) reads `tool_call` activities only and renders a sibling "media generation" block, never a row in the token table; failed calls carry no payload and drop out. Turns saved before these fields existed (e.g. conv_a27d6008) fall back to the `providerResponse.tool` block via `mediaUsageFromToolSummary` — per turn, run activities take precedence so nothing is double-counted, and the legacy provider is derived best-effort (fal for video/audio, `fal-ai/` prefix for images).
- **Failed turns persist their run.** The early-return failure paths in `RunChatStream` call `saveFailedAssistantTurn` (best-effort) so an errored turn still leaves its ledger — and the tokens it really burned — in history, with the error text as the turn's content and `providerResponse.error`.
- **Internal telemetry rides `json:"-"` side channels, zipped at the recording site.** `HarnessToolResult.Permission` (`*ToolPermissionDecision`, carrying outcome approved/denied/timeout/cancelled + wait time) is invisible to planner evidence and bindings; `toolActivities` copies it onto the persisted `HarnessToolActivity` fields, the same pattern as planner `Call` params. Never deliver internal diagnostics via `Notices` — those render to the user verbatim.

## Conventions

- **Single `package main`, all files at repo root.** No internal subpackages.
- **Config is merged with defaults, never raw.** `mergeAppConfig(config)` fills every unset field from `defaultAppConfig()`. The allowlist (`defaultFilesystemToolAllowedCommands`) and the prompt's command list are read from the same `ConfigFilesystemTool.AllowedCommands`, so they cannot drift.
- **Tool params go directly on the call object, not nested under `args`.** Plan validation produces a specific error (`toolCalls[N].args must be ...; tool parameters like path go directly on the call object`) when a model nests them.
- **All user/storage paths are normalized** via `normalizeStoragePath` (`~` expansion → absolute). The filesystem tool resolves and confines every path to the configured root.
- **IDs are prefixed random hex** (`randomID("conv")`, `"run"`, `"permission"`, `"chat-" + unixnano`).
- **Wails bindings are generated, and must be committed in the same change.** `frontend/wailsjs/go/main/App.{js,d.ts}` mirror exported `App` methods; `frontend/wailsjs/go/models.ts` mirrors the public structs reachable from bound methods. **Any change to a bound `App` method _or_ to a field on a struct it exposes** (e.g. adding `Loop`/`Voice` to `HarnessToolCall`, `Notices` to `HarnessToolResult`) must be followed by regenerating **and committing** these files — run `wails generate module` (or `wails dev`/`wails build`, which regenerate as a side effect). A Go-only change that adds struct fields without the regenerated `models.ts` merges cleanly but leaves the frontend types stale on `main`; the frontend then can't see the new fields. Commit the bindings alongside the Go change, and discard any incidental mode-only churn the generator makes to `frontend/wailsjs/runtime/`.
- **Git commits** use conventional prefixes (`feat:`, `fix:`) in the log.

## Testing

- Standard `go test ./...`. Tests are **table-driven** and live alongside the code (`*_test.go`).
- HTTP is mocked with a `roundTripFunc` (`app_test.go`) injected into an `*http.Client` — no real Ollama/OpenRouter calls in tests.
- `fakeProvider` (`provider_test.go`) implements `ChatProvider` for harness-level tests.
- `app_test.go` is the largest test file (~4000 lines) and covers the full harness pipeline, streaming cleanup, history, tools, and config merging. Helpers like `waitForStreamCleanup` and `harnessStepByKind` exist there.
- `harness_telemetry_test.go` covers harness telemetry: token accounting (prompt + completion per model-call step), TTFT, failed-turn persistence, permission-decision recording, request snapshots, and the instrumentation-coverage rule (one step per provider call). Its `telemetryTestConfig`/`persistedHarnessRun` helpers drive a full turn against a mocked Ollama and read the run back from history.

## Gotchas

- `frontend/dist/` is embedded at compile time (`//go:embed all:frontend/dist` in `main.go`). The frontend must be built (`npm run build --prefix frontend`) before `wails build`; `wails dev` rebuilds it live.
- `version` in `main.go` is `"dev"` unless injected at link time by `release.sh` (`-ldflags "-X main.version=..."`).
- Config lives at `~/.atelier/config.json`; history/artifacts under `~/.atelier/history/`. Skills are loaded from **both** `~/.agents/skills/<name>/SKILL.md` and `~/.atelier/skills/<name>/SKILL.md`.
- The OpenRouter API key is stored in the OS keychain, **not** in config.json. An absent key returns `("", nil)`; callers treat "not configured" and "empty" uniformly.
- `go.mod` has a commented-out `replace` directive for local Wails development — uncomment only when hacking on Wails itself.
- Permission requests time out after 2 minutes and fail closed (denied) if no UI is attached.
