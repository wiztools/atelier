# Selecting the Harness Model from OpenRouter

**Date:** 2026-07-15
**Status:** Implemented on `feat/openrouter-harness-model`. The one item deferred
to implementation — verifying the strict-mode rejected-keyword list against
OpenRouter's live docs — is still open; see "Structured output on OpenRouter".
**Revision:** 2 — incorporates
`2026-07-15-openrouter-harness-model-design-review.md`. Changes from revision 1
are summarized at the end.

## Goal

Let the user choose the harness model from either local Ollama or OpenRouter, the
same way the primary chat model is already chosen. Ollama remains a peer option
and the default; OpenRouter becomes selectable.

The harness model drives **three** calls, all of which must work on either
provider:

| Call | Site | Schema |
| --- | --- | --- |
| Triage — decides `needsTools` + `responseMode` | `triage.go:75` | `triageResponseSchema()` |
| Skill selection — picks a `SKILL.md` for the turn | `harness.go:679` | `skillSelectionSchema()` |
| Planning loop — emits the validated tool plan | `harness.go:776` | `harnessToolPlanSchema()` |

## Why this is not a settings-page change

The harness is Ollama-only well below the UI. Four things block a provider
choice:

1. **Three harness call sites bypass the provider layer.** All three call
   `h.app.ollamaClient(req.BaseURL).CompleteChat(...)` directly rather than
   resolving through `ProviderRegistry`. The `ChatProvider` interface already
   exists for exactly this; the harness simply never used it.

   The skill selector (`harness.go:679`) is the easiest of the three to miss. It
   runs inside `prepareChatTurnLoop` (`harness.go:740`) *before* the planner
   loop, every time triage concludes tools are needed, on `req.Model` — which is
   already the harness model (`toolReq.Model = harnessModel`, `harness.go:192`).
   Its error path returns `&HarnessSkillDecision{Error: ...}, **nil**`, so a
   failure is invisible: the turn proceeds and no skill is ever selected. Left
   unrouted, an OpenRouter harness keeps hitting Ollama here, and a cloud-only
   setup silently loses skill auto-selection entirely.

2. **The OpenRouter client drops the JSON schema and the sampling options.**
   `openRouterChatBody` (`openrouter_client.go:62`) sends only `model`,
   `messages`, and `stream`. It silently discards `req.Format`, `req.Options`,
   and `req.Tools`. Pointing triage at OpenRouter today would return
   unconstrained prose, `decodeTriageDecision` would fail, and every turn would
   fail-safe into the tool path — degraded, but silently.

3. **The image-caption path hardcodes `"ollama"`.** `responseProviderFor`
   (`harness.go:476`) returns the literal `"ollama"` whenever the response falls
   back to the harness model, justified by the comment "the harness model is
   always Ollama".

4. **Native tool detection is Ollama-specific.** `supportsNativeTools` calls
   Ollama's `ShowModel` to read capabilities.

Adding a dropdown without fixing these produces a feature that appears to work
while quietly breaking the harness. That silent-degradation mode is the main
risk this design exists to avoid, and it is the lens to apply to every decision
below.

## Config

Mirrors the existing primary-model pattern: the provider lives in
`ConfigModels`, the model name lives per-provider.

```go
type ConfigModels struct {
    PrimaryProvider string `json:"primaryProvider,omitempty"`
    HarnessProvider string `json:"harnessProvider,omitempty"`  // new
    ImageProvider   string `json:"imageProvider,omitempty"`
}

type ConfigOpenRouter struct {
    Enabled bool   `json:"enabled"`
    Primary string `json:"primary,omitempty"`
    Harness string `json:"harness,omitempty"`  // new
}
```

Ollama's harness model stays at `Providers.Ollama.Models.Harness`.

**Backward compatibility:** `mergeAppConfig` defaults `HarnessProvider` to
`"ollama"` when unset, and normalizes any unrecognized value to the default —
mirroring how `ImageProvider` is normalized at `app.go:1376`. Every existing
`~/.atelier/config.json` therefore behaves exactly as it does today. This is the
property to protect in review.

`mergeAppConfig` stays a **pure** config transform. It must not consult the
keychain (see "Rejected: key presence in mergeAppConfig" below).

## Harness target resolution

`harnessModelFor(primaryModel) string` is replaced by a resolver returning model
and provider together, so the two can never drift:

```go
type harnessTarget struct {
    model    string
    provider string
}

// resolveHarnessTarget resolves the model and provider for the three harness
// calls (triage, skill selection, planning). An unset harness model falls back
// to the primary model on the primary provider, so a one-model setup still
// works — including a cloud-only one, which the old Ollama-pinned fallback
// could not express.
func (h *HarnessEngine) resolveHarnessTarget(primaryModel, primaryProvider string) harnessTarget
```

Resolution:

| `Models.HarnessProvider` | Harness model set? | Result |
| --- | --- | --- |
| `"openrouter"` | yes | `Providers.OpenRouter.Harness` on `"openrouter"` |
| `"ollama"` (or unset/unknown) | yes | `Providers.Ollama.Models.Harness` on `"ollama"` |
| either | no | `primaryModel` on `primaryProvider` |

`RunChatStream` calls `resolveHarnessTarget` **once** and threads the resulting
struct into `triageChatTurn`, `selectSkillForTurn`, `prepareChatTurnLoop`,
`supportsNativeTools`, `responseModelFor`, and `responseProviderFor`.

Threading the struct is not cosmetic — it closes a real bug. Today the sibling
functions take asymmetric arguments:

```go
func (h *HarnessEngine) responseModelFor(mode, primaryModel, harnessModel string) string       // gets the resolved harness model
func (h *HarnessEngine) responseProviderFor(mode, primaryModel, primaryProvider string) string // no harness provider at all
```

`responseModelFor` receives the *resolved* harness model; `responseProviderFor`
does not receive the harness provider and returns a literal. If the implementer
"returns the harness provider" by reading `h.config.Models.HarnessProvider` raw,
it diverges from the table's fallback row: with `HarnessProvider="openrouter"`
and no `OpenRouter.Harness`, `resolveHarnessTarget` yields the **primary** model
on the **primary** provider (say `llama3` on `ollama`), while a raw read yields
`"openrouter"` — sending `llama3` to the OpenRouter endpoint. That is a 404 on
image-caption turns only, i.e. a quiet correctness bug rather than a loud
failure. Passing the resolved struct makes it structurally impossible.

The existing `mergeAppConfig` already defaults `Providers.Ollama.Models.Harness`
to the Ollama primary model (`app.go:1362`), so the Ollama branch is rarely
empty in practice. The fallback stays as the safety net for the OpenRouter
branch and for a genuinely one-model setup.

## Structured output on OpenRouter

This is the load-bearing part of the design.

OpenRouter expects OpenAI-shaped structured outputs:

```json
"response_format": {
  "type": "json_schema",
  "json_schema": {"name": "...", "strict": true, "schema": { ... }}
}
```

Strict mode imposes two constraints the existing schemas do not all satisfy:

- **Every key in `properties` must also appear in `required`.**
- **Only a constrained keyword subset is accepted.** `maxItems`, `minItems`,
  `uniqueItems`, `minimum`, `maximum`, `multipleOf`, `pattern`, `minLength`, and
  `maxLength` are rejected.

Against the three harness schemas:

| Schema | Strict-compatible? |
| --- | --- |
| `triageResponseSchema` (`triage.go:23`) | Yes — `additionalProperties: false`, all four properties required |
| `skillSelectionSchema` (`skills.go:199`) | Yes — both properties required |
| `harnessToolPlanSchema` (`harness.go:988`) | **No** — two separate violations |

`harnessToolPlanSchema` fails twice. Its tool-call item declares eleven
properties but `required: ["name"]`, deliberately, because different tools take
different parameters. And it carries `"maxItems": 3` (`harness.go:999`), a
rejected keyword. Either one gets the schema rejected outright, on **every**
planner call — a guaranteed failure, not a probabilistic one.

### Decision: derive a strict variant in the OpenRouter client

Add a pure function to `openrouter_client.go`:

```go
// strictJSONSchema rewrites an Ollama-shaped JSON Schema into one OpenAI's
// strict mode accepts. Ollama sees the original, byte-identical; only the
// derived variant is sent to OpenRouter.
func strictJSONSchema(schema any) any
```

An ordered pipeline:

1. **Strip** unsupported keywords: `maxItems`, `minItems`, `uniqueItems`,
   `minimum`, `maximum`, `multipleOf`, `pattern`, `minLength`, `maxLength`.
2. **Recurse** into `properties`, `items`, and `anyOf`/`oneOf`.
3. **Promote** every key in `properties` into `required`.
4. **Widen** previously-optional scalars to a nullable union
   (`{"type": "string"}` → `{"type": ["string", "null"]}`).

`openRouterChatBody` applies it when `req.Format != nil`, wrapping the result
under a constant schema name (`"atelier_structured_output"`) with `strict: true`.

Losing `maxItems: 3` for OpenRouter costs nothing real: the cap is advisory, and
`prepareChatTurnLoop` already enforces at most three tool calls per round at
execution time.

Why this over the alternatives:

- **vs. `strict: false`** — degrades to best-effort JSON mode, dropping the
  grammar guarantee the planner depends on. The whole point of `Format` is
  constrained decoding.
- **vs. rewriting `harnessToolPlanSchema` to be strict-native** — changes the
  schema Ollama sees today, risking a regression in the path that currently
  works. The transform keeps Ollama's request byte-identical.

Why it is safe downstream: Go's `json.Unmarshal` maps a JSON `null` to the
field's zero value, so `"path": null` yields `Path == ""`. Every tool's
`Validate` func already treats an empty string as absent, so
`parseHarnessToolPlanWithRegistry` and plan validation need no changes.

**Accepted risk:** the transform derives a schema shape rather than authoring
one. If a given OpenRouter model handles nullable unions poorly, the planner
emits an invalid plan. That is fail-safe — the existing plan-correction loop
feeds the error back, and `invalidPlanSystemNote` stops the primary model
claiming success — but it costs a round. Revisit if a specific model misbehaves.

**Verify at implementation time:** the exact rejected-keyword list against
OpenRouter's current strict-mode docs. `maxItems` is the one that is certain;
the rest of the list should be confirmed rather than trusted to this document.

## Sampling options

All three harness calls set `temperature: 0` deliberately — stable JSON is the
property the whole design protects — plus a `num_predict` ceiling
(`triage.go:70`, `harness.go:674`, `harness.go:766`).

`openRouterChatBody` maps the two portable ones:

- `Options["temperature"]` → top-level `temperature`
- `Options["num_predict"]` → `max_tokens`

Both are standard OpenAI parameters that OpenRouter normalizes across providers.
Without them an OpenRouter planner runs at the provider's default temperature,
which is exactly the wobble the correction loop then pays a round to fix.

Mapping `num_predict` also repairs an existing safeguard. The planner retries on
truncation by checking `completion.Reason == "length"` (`harness.go:790`).
OpenRouter *does* populate this — it maps `finish_reason` → `Reason`
(`openrouter_client.go:131`) and OpenAI's truncation reason is also the literal
`"length"`, so the path works by vocabulary alignment. But without `max_tokens`
the ceiling is whatever the routed provider defaults to, so the bound the
heuristic exists to enforce would be set by a third party.

`num_ctx` stays **out of scope** — it has no portable OpenAI equivalent, and
context budgeting keeps using the configured `numCtx` as a conservative
character budget for both providers.

## Unpicking the Ollama assumptions

1. **Route all three harness calls through the provider layer.** `triage.go:75`,
   `harness.go:679`, and `harness.go:776` resolve via
   `h.app.providerFor(target.provider, req.BaseURL)` instead of
   `h.app.ollamaClient(...)`.

2. **`responseProviderFor` takes the resolved harness provider** and returns it
   instead of the literal `"ollama"`, mirroring `responseModelFor`. Image
   captions then route to wherever the harness model actually lives.

3. **`supportsNativeTools` returns `false` for non-Ollama providers**, falling
   back to the format-schema planner. This follows the existing rule that native
   tools are "an enhancement, never a requirement" — a wrong fallback costs
   latency, never correctness. `req.Tools` therefore never reaches OpenRouter
   and needs no mapping.

4. **Telemetry stops lying.** Step provider labels are passed the resolved
   provider. Find these by grepping for the literal `"ollama"` passed to
   `appendStep`/`completeStep` rather than trusting a hand-written list;
   `harness.go:166` and `harness.go:759` are known instances but the sweep is
   the source of truth.

## Error surfacing

If `HarnessProvider="openrouter"` with no key set, `providerFor` returns
`"openrouter api key is not configured"` (`openrouter_client.go:138`). Left
alone, triage fails safe to tools and the planner fails safe to the correction
loop — it "works" while doing the wrong thing on every turn.

A missing key is a **configuration** error, not a model failure: deterministic,
and it will never succeed on retry. The fail-safe rails are built for
probabilistic failures and are the wrong response here. Surface it once per turn
as a harness error instead of degrading.

## UI

The Settings → Harness section (`frontend/src/App.tsx:1564`) gains a Provider
select above the model control, mirroring the composer's primary-provider switch
(`App.tsx:1947`):

- **`ollama`** — keeps today's `<select>` over `modelOptions` plus
  `ModelCapabilityLink`.
- **`openrouter`** — a `ModelCombobox` fed by `ListPrimaryModels('openrouter', '')`,
  the full catalog. No capability link, exactly as the primary picker already
  omits it for OpenRouter.

State: `harnessProvider` plus `harnessModels: Record<'ollama' | 'openrouter', string>`,
mirroring the existing `primaryProvider` / `primaryModels` pair so each provider
remembers its own model across switches. Both join the config-save dependency
array (`App.tsx:375`).

**Decision: settings-only, not per-turn.** The primary provider is switchable
per message from the composer; the harness provider is not. The harness is
infrastructure the user configures once, and a per-turn control would add a
second switch to the composer for something that rarely changes turn to turn.
Revisit if it proves annoying in use.

## Rejected: key presence in `mergeAppConfig`

The review proposed that `mergeAppConfig` require both `OpenRouter.Harness != ""`
*and* a present key when `HarnessProvider="openrouter"`, falling back to
`"ollama"` otherwise. Rejected on two grounds, recorded so it is not
re-proposed:

- **It would put keychain I/O in a pure function.** `mergeAppConfig` is a pure
  config transform, exercised throughout `app_test.go` with no OS dependencies.
  The key deliberately lives in the keychain rather than config, with
  `ConfigOpenRouter.Enabled` existing precisely to mirror key presence for the
  frontend. Key validation belongs where `Enabled` is computed, not in the merge
  path.
- **Falling back to `"ollama"` contradicts the approved resolution.** The
  approved fallback is the primary model on the primary provider. Falling back
  to `"ollama"` would pin a cloud-only user with no local Ollama to a
  nonexistent Ollama model — a worse failure than the one it prevents, in
  exactly the configuration this design exists to enable. It also contradicts
  the review's own argument against silent degradation.

What survives is the narrow, pure part, adopted above: normalize an
*unrecognized* `HarnessProvider` string to the default, mirroring `ImageProvider`.

## Testing

`fakeProvider` (`provider_test.go`) already implements `ChatProvider`, so
harness tests can assert the three harness calls hit the *configured* provider —
the highest-value new test, since routing is the core change.

New table-driven tests:

- `strictJSONSchema` — `maxItems` and the rest of the rejected keyword set
  stripped; optional properties promoted and widened; nested objects and array
  items recursed. Two named rows deserve to exist explicitly: an already-strict
  schema (triage's) round-tripping unchanged, and the planner's `name` field,
  which carries an `enum` that the widen step must not mangle. The enum case is
  where the first real failure is most likely.
- `openRouterChatBody` — emits `response_format` with `strict: true` when
  `Format` is set and omits it when not; maps `temperature` and `num_predict` →
  `max_tokens`; still omits `num_ctx`.
- `resolveHarnessTarget` — the resolution table above, including the unset-model
  fallback to primary model *and* provider, and unknown-provider normalization.
- `mergeAppConfig` — `HarnessProvider` defaults to `"ollama"` for a legacy
  config with no `models.harnessProvider` key; an unrecognized value normalizes
  to the default; no keychain access.
- Harness routing — an OpenRouter harness with an Ollama primary sends triage,
  skill selection, and planner calls to OpenRouter and the final response to
  Ollama.
- `responseProviderFor` — image mode routes captions to the harness provider;
  the fallback row does not send a primary model name to the wrong endpoint.
- Skill selection on an OpenRouter harness resolves a skill (guards the
  fail-soft hole in blocker 1).

Existing tests to watch: `TestMergeAppConfigDefaultsHarnessModelToPrimaryModel`
(`app_test.go:233`) and `TestHarnessModelPlansKnowledgedPost` (`app_test.go:2402`)
both encode current harness assumptions and will need review.

HTTP stays mocked via `roundTripFunc`; no real OpenRouter calls in tests.

## Documentation

AGENTS.md asserts the dying invariant in two places and must be rewritten, not
left to quietly falsify:

- "An image-generation model cannot produce text/vision. ... `responseProviderFor`
  forces `"ollama"` in that case (the harness model is always Ollama)."
- "**Harness model defaults to the primary model** when unset — a one-model
  setup must still work." — still true, but now also carries the provider.

The architecture section describes triage and planning but not skill selection
as a harness model call; the three-call table above should land there. The
`ConfigOpenRouter` / `ConfigModels` config notes need the new fields.

## Out of scope

- Native tool-calling for OpenRouter harness models (`req.Tools` mapping).
- Mapping `num_ctx` onto OpenRouter.
- A per-turn harness provider switch in the composer.
- Any change to the image or video/audio provider selection.

## Changes from revision 1

- **Skill selection named as a third harness call** (`harness.go:679`). Revision
  1 listed two call sites and presented the list as complete. Its fail-soft
  error path made the omission invisible rather than loud.
- **`strictJSONSchema` gained a strip step.** Revision 1 specified only
  promote-and-widen, which would have emitted `maxItems: 3` and been rejected on
  every planner call. Revision 1's claim that the transform preserved
  constrained decoding was false as written.
- **`temperature` and `max_tokens` are now mapped.** Revision 1 deferred all of
  `Options`, which bundled two trivial parameters with the one hard one and
  silently gave up `temperature: 0`.
- **`resolveHarnessTarget` returns a struct threaded through all consumers**,
  closing the `responseProviderFor` model/provider divergence.
- **Unknown `HarnessProvider` values normalize to the default.**
- **Missing-key errors surface rather than degrade.**
- **Telemetry fix is a grep sweep**, not two named lines.
