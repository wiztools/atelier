# Model-Aware Audio Parameters — Design Spec

Date: 2026-07-16
Status: Approved (design)
Branch: `feature/model-aware-audio-params`

## Problem

atelier's `generate_audio` tool builds one fixed fal request body — it sends the
prompt as both `prompt` and `text`, plus `duration` and `negative_prompt` — to
whatever audio model is configured. But fal's audio endpoints have **divergent
input schemas**. Verified examples:

| Canonical concept | ElevenLabs SFX v2 | ElevenLabs TTS multilingual-v2 | MiniMax speech-02-hd | ElevenLabs music |
|---|---|---|---|---|
| prompt | `text` | `text` | `text` | `prompt` |
| duration | `duration_seconds` (0.5–22) | — none — | — none — (output only) | `music_length_ms` (ms) |
| loop | `loop` (bool) | — none — | — none — | — none — |
| voice | — none — | `voice` (free string, default "Rachel") | `voice_setting.voice_id` (nested) | — none — |
| output_format | enum: `mp3_44100_128`… | — | enum: `url` / `hex` (delivery, not codec) | enum |

Consequences of the current "send everything" approach:
- `duration` is a silent no-op on any model that names it differently
  (`duration_seconds`, `music_length_ms`) — already broken today.
- Looping (an ElevenLabs SFX v2 capability via the `loop` flag) is unreachable.
- Per-model knobs like `voice` cannot be driven from chat.
- When a requested capability isn't supported, the user gets no signal — the
  request is silently altered.

## Goal

Make `generate_audio` model-aware:

1. The planner emits a small **canonical** parameter vocabulary from chat:
   `prompt`, `duration`, `loop`, `voice`.
2. The tool **resolves** each canonical param to the configured model's real fal
   input schema (fetched from fal, cached), mapping names, transforming values,
   and handling one level of object nesting.
3. Params a model cannot support are **dropped**, and every drop is surfaced to
   the user as a **deterministic caveat** in the chat reply (Route B).

`output_format` is deliberately **excluded** from the canonical vocabulary: its
meaning collides across models (audio codec on ElevenLabs vs. `url`/`hex`
delivery on MiniMax), so a name-based canonical mapping would be wrong.

## Scope (v1)

Full resolver: `prompt`, `duration`, `loop`, `voice`; dynamic schema fetch with
disk cache; one-level nested handling; user-editable override table; Route B
deterministic caveats. Extends the existing `generate_audio` tool (no new tool).

Out of scope: a separate `generate_sound_effect` tool; generating the tool's
param schema dynamically per model; cross-model voice-value translation
(e.g. mapping "Rachel" to an equivalent voice on another model).

## Architecture

Three free-standing, unit-testable components plus targeted edits to existing
wiring. The resolver and schema cache live **outside** `FalClient`, which stays
a thin transport.

### 1. Schema provider — `fal_schema.go`

`SchemaCache` fetches and caches each model's fal input schema.

- Source: `https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=<model>`.
- Parses the OpenAPI `Input` model into a simplified `ModelInputSchema`:
  for each property — name, type, enum values (if any), and **one level** of
  nested object properties (with the nested object's default, so we can merge).
- Persists to `~/.atelier/schema-cache/<sanitized-model>.json` with a
  `fetchedAt` timestamp. **TTL: 7 days.**
- Lookup semantics (`Get(ctx, model)`):
  - Fresh cache hit (within TTL) → return cached, **no network**.
  - Missing or expired → fetch; on success cache + return; on failure return
    **unavailable** (do NOT use a stale/expired copy — "generic immediately").
  - Corrupt/partial cache file → treat as a miss and re-fetch.
- Clock is injected so the TTL is testable without wall-clock dependence.
- Owned at the App level (needs the `~/.atelier` dir); captured by the gateway
  closure. Not injected into `FalClient`.

### 2. Resolver — `fal_params.go`

Pure function:

```go
func resolveAudioBody(schema *ModelInputSchema, req AudioGenerateRequest, ov Overrides)
    (body map[string]any, notices []string)
```

Synonym sets (canonical → candidate native keys, scanned in order):

| Canonical | Synonyms | Value transform |
|---|---|---|
| `prompt` | `prompt`, `text` | none |
| `duration` | `duration_seconds`, `duration`, `music_length_ms` | seconds→ms when native key ends in `_ms` |
| `loop` | `loop` | none (emitted only when `true`) |
| `voice` | `voice`, `voice_id`, `voice_name`, `speaker`, `speaker_id` | none (free-form string, forwarded as-is) |

Resolution per canonical param:
1. **Override wins.** If `ov[model][canon]` exists → use its native path, or
   drop-with-notice if the override value is `""` (explicitly unsupported).
2. **Top-level scan.** First schema property whose name is in the synonym set →
   map there.
3. **One-level nested scan.** Else, for each object-typed property, scan its
   sub-properties (catches MiniMax `voice_setting.voice_id`). On a hit, write via
   dot-path and **merge into the schema's default object** so sibling defaults
   (`speed`/`pitch`/`vol`) survive.
4. **Enum check.** If the matched property is an enum and the value isn't in the
   allowed set → drop-with-notice listing valid values. (Voice fields are
   free-form strings, so this rarely fires for voice — an accepted limitation.)
5. **No match** → drop-with-notice:
   *"The selected model `<model>` has no `<canon>` control; ignoring it."*

`prompt` always maps (`text`/`prompt` exist on every audio model). If neither
exists, that is a hard error, not a drop.

**Schema unavailable** → generic body (`prompt` + `text` only) plus one notice:
*"Couldn't load the model's parameter schema; generated with defaults and
skipped duration/loop/voice."*

### 3. Override table

Built-in Go defaults merged **under** a user-editable
`~/.atelier/fal-overrides.json`. Shape:

```json
{ "audio": { "<model-id>": { "voice": "voice_setting.voice_id", "duration": "" } } }
```

`""` = explicitly unsupported (drop-with-notice). Malformed file → log + ignore,
fall back to built-in defaults (never crash generation on bad config).

## Data model & plumbing

Struct changes:

- `AudioGenerateRequest` (app.go) — add `Loop bool`, `Voice string`.
  `Duration` stays `string` (planner emits it that way; resolver parses).
- `HarnessToolCall` (harness.go) — add `Loop bool`, `Voice string` so
  planner-emitted JSON deserializes them.
- `GeneratedAudio` — add `Notices []string`.
- `HarnessToolResult` (harness.go) — add `Notices []string` (generic, reusable
  caveat channel).
- `ToolAudioResult` (tools_registry.go) — add `Notices []string`; implement
  `NoticeProvider`.

Generic notice lift (no per-tool signature churn):

```go
type NoticeProvider interface { ToolNotices() []string }
// in tool_gateway.go, right after Execute:
if np, ok := output.(NoticeProvider); ok { result.Notices = np.ToolNotices() }
```

`FalClient.GenerateAudio` becomes thin:

```go
func (client FalClient) GenerateAudio(ctx context.Context, model string, body map[string]any)
    (GeneratedAudio, error)
```

It submits the already-native body and does submit/poll/download only.
`AudioGenerateRequest` never reaches it.

Gateway audio closure (tool_gateway.go) owns the resolve step:

```go
tools.GenerateAudio = func(ctx context.Context, req AudioGenerateRequest) (GeneratedAudio, error) {
    schema := schemaCache.Get(ctx, req.Model)          // fresh | unavailable
    body, notices := resolveAudioBody(schema, req, overrides)
    ga, err := falClient.GenerateAudio(ctx, req.Model, body)
    ga.Notices = notices
    return ga, err
}
```

Tool Execute (tools_registry.go) — build `AudioGenerateRequest` with the new
`Loop`/`Voice`; copy `generated.Notices → ToolAudioResult.Notices`. Add `loop`
and `voice` to `generateAudioParamSchema()` so the planner can emit them.

Data flow:

```
planner → HarnessToolCall{Loop,Voice} → AudioGenerateRequest
       → gateway closure: schemaCache.Get + resolveAudioBody → (body, notices)
       → FalClient.GenerateAudio(model, body) → GeneratedAudio{Notices}
       → ToolAudioResult{Notices} → NoticeProvider lift → HarnessToolResult.Notices
```

## Route B — surfacing caveats in chat

`HarnessToolResult.Notices` does double duty:

1. **Informs the model.** `toolResultMessages` already marshals the whole
   `HarnessToolResult` into the model's tool-observation context, so `Notices`
   flow there automatically — the model won't falsely claim a dropped capability
   applied. A one-line addition to `toolEvidenceSystemNote` states these are
   authoritative user-facing caveats: convey their meaning, don't quote verbatim.
2. **Deterministic append.** In the reply-assembly block (harness.go ~277-337),
   collect notices from `preparation.ToolResults` and append them to
   `assistantContent` (trailing `\n\n> ⚠️ <notice>` line) before persist.

**Streaming fix.** The model's prose is streamed incrementally, so
`finalContentEmitted` is true and the terminal `ChatStreamEvent` sends
`terminalContent = ""` to avoid re-sending (harness.go ~321-324). A notice
appended after streaming would be persisted but not render live. When notices
exist, include them in the terminal event's `Content` even when
`finalContentEmitted` is true, so the caveat renders live and matches the
persisted turn.

## Error handling

- Schema unavailable → generic body + notice; generation still succeeds.
- Param dropped (unsupported/invalid) → omitted from body + notice; never a hard
  failure.
- Prompt unmappable (no `text`/`prompt`) → hard error via existing `error` return.
- Malformed overrides file → log + ignore; use built-in defaults.
- Corrupt cache file → miss + re-fetch.

## Testing

- **Resolver unit tests** (table-driven) against committed fixtures of the three
  real schemas (SFX v2, ElevenLabs TTS ml-v2, MiniMax): assert native body +
  notices for every branch — top-level map, nested merge, enum reject, no-match
  drop, unit transform, generic fallback.
- **Schema cache tests:** fresh-hit (no fetch), expired → refetch, fetch-fail →
  unavailable, corrupt file → miss. Injected clock for TTL.
- **Override merge tests:** user file overrides built-in; `""` = unsupported;
  malformed file ignored.
- **Notice plumbing test:** a resolver drop propagates through `GeneratedAudio`
  → `ToolAudioResult` → `NoticeProvider` lift → `HarnessToolResult.Notices`.
- **Schema parser test:** fixture OpenAPI JSON → `ModelInputSchema` (enums +
  one-level nesting extracted correctly).
- **End-to-end (verify skill):** drive a "make a looping ambient sound" request;
  confirm audio attaches and the loop caveat (or success) renders in chat. Live
  fal call — gated on user go-ahead.

Schema fixtures are committed so unit tests never hit the network.

## Files touched

- New: `fal_schema.go`, `fal_params.go` (+ `_test.go` for each), test fixtures.
- New: `~/.atelier/fal-overrides.json` support (loader in config path).
- Edit: `fal_client.go` (thin `GenerateAudio`), `app.go`
  (`AudioGenerateRequest`, `GeneratedAudio`), `tools_registry.go`
  (`ToolAudioResult`, tool Execute, param schema, `NoticeProvider`),
  `tool_gateway.go` (resolve closure + notice lift), `harness.go`
  (`HarnessToolCall`, `HarnessToolResult.Notices`, reply-assembly append +
  streaming fix, system note).
