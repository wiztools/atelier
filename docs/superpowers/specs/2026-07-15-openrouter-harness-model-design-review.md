# Review: Selecting the Harness Model from OpenRouter

**Date:** 2026-07-15
**Status:** Followup — caveats and design refinements on
`2026-07-15-openrouter-harness-model-design.md`

## Scope

This is a code-verified review of the approved design. Every line reference in
the original plan was checked against the repo and holds. The plan's backbone is
correct — the routing decision, the derive-a-strict-variant decision, the
fail-safe framing, and the backward-compatibility property are all the right
calls. What follows are the gaps that will bite during implementation, and a set
of low-effort refinements that make the design safer to land.

## Caveats (things the plan gets wrong or omits)

### 1. There is a fifth Ollama bypass point: the skill selector

The "Why this is not a settings-page change" section enumerates **four**
blockers and treats the list as complete. It is not. The skill selector is a
third harness call, distinct from triage and the planner:

```
harness.go:679   completion, err := h.app.ollamaClient(req.BaseURL).CompleteChat(ctx, selectionReq)
```

`selectSkillForTurn` runs **first** inside `prepareChatTurnLoop`
(`harness.go:740`), before the planner loop, every time triage concludes tools
are needed. It uses `Format: skillSelectionSchema()` (`harness.go:672`) and runs
on `req.Model`, which is already the harness model (`toolReq.Model = harnessModel`
at `harness.go:192`).

**Consequence of the omission:** when the harness is OpenRouter, skill
auto-selection silently keeps hitting Ollama. In the cloud-only setup the plan
explicitly wants to enable (no local Ollama), the selector errors on every turn,
`decodeSkillSelectionPlan` never runs, and **no skill ever auto-selects**. This
is fail-*soft*, not fail-safe: it does not crash the turn, but it permanently
disables the skill-selection feature in exactly the configuration the design
exists to support.

**Fix:** route it through `providerFor(harnessProvider, ...)` like the other two
call sites. Note `skillSelectionSchema()` (`skills.go:199`) is already
strict-compatible — both properties required, `additionalProperties: false` — so
unlike the planner it needs no schema work, only routing.

The "Unpicking the Ollama assumptions" section should therefore name **three**
call sites, not two: `triage.go:75`, `harness.go:679`, `harness.go:776`.

### 2. `responseProviderFor` has a latent model/provider mismatch

This is the most likely place to introduce a real correctness bug. Today the two
sibling functions take asymmetric arguments:

```go
func (h *HarnessEngine) responseModelFor(mode, primaryModel, harnessModel string) string      // gets resolved harness model
func (h *HarnessEngine) responseProviderFor(mode, primaryModel, primaryProvider string) string // NO harnessProvider param
```

`responseModelFor` receives the *resolved* harness model as an argument.
`responseProviderFor` does **not** receive the harness provider — it returns the
literal `"ollama"`. The plan (point 2) changes the body to "return the harness
provider" but never changes the signature, so the implementer's only options are
to read `h.config.Models.HarnessProvider` raw or add the parameter.

Reading it raw diverges from the resolution table. Consider the table's fallback
row — "`either`, model not set → `primaryModel` on `primaryProvider`": there
`harnessTarget` returns the **primary** model on the **primary** provider.
`responseModelFor` returns `primaryModel` correctly (it is passed the resolved
value). But `responseProviderFor` reading `config.Models.HarnessProvider ==
"openrouter"` would return `"openrouter"` — sending the primary model's name to
the OpenRouter endpoint. On an image-caption turn that is a wrong-model error /
404.

**Fix:** make `responseProviderFor` take the *resolved* harness provider as a
parameter, mirroring `responseModelFor`, and resolve both values from the *same*
`harnessTarget` call in `RunChatStream`. The cleaner form is to return a single
struct so model and provider cannot drift — see design refinement A below.

### 3. `strictJSONSchema` as specified is insufficient — it must also strip keywords

The plan describes the transform as "promote optional props into `required`,
widen to nullable union." That is necessary but not sufficient. The planner
schema carries:

```go
harness.go:999    "maxItems": 3,
```

OpenAI-style strict structured outputs (which OpenRouter forwards) accept a
**constrained keyword subset** and explicitly reject `maxItems`, `minItems`,
`minimum`, `maximum`, `pattern`, `maxLength`, `minLength`, and others. As
specified, `strictJSONSchema` would emit `maxItems: 3` and OpenRouter would
reject the schema on **every** planner call.

This is not the "accepted risk" the plan describes (a model mishandling a
nullable union, which is probabilistic). This is a **guaranteed** rejection,
which turns the entire planner path into the fail-safe correction loop on every
turn — silent degradation in the exact mode the plan's intro says it exists to
avoid.

**Fix:** the transform needs a second pass — strip the unsupported keyword set
(`maxItems`, `minItems`, `minimum`, `maximum`, `pattern`, `maxLength`,
`minLength`, `multipleOf`, `uniqueItems`) recursively, then promote and widen.
Keeping `maxItems` for Ollama is still correct because Ollama sees the
byte-identical original; only the derived variant strips it. This is consistent
with the plan's "leave Ollama untouched" principle. Verify the full reject list
against OpenRouter's current strict-mode docs during implementation, but
`maxItems` removal is the one that is certain.

### 4. Determinism is quietly lost — `temperature: 0` is dropped for OpenRouter

Triage (`triage.go:70`) and the planner (`harness.go:766`) deliberately set
`temperature: 0` for stable JSON, plus `num_predict`. The plan defers **all**
`Options` mapping (`"req.Options stays Ollama-only"`). The consequence is that an
OpenRouter planner runs at the provider's default temperature rather than zero —
and plan stability is precisely the property the whole design exists to protect.

The fail-safe correction loop catches bad plans, but every wobble costs a round
and wall-clock time.

**Fix:** the plan's "all-or-nothing Options mapping" framing obscures a cheap
middle path. Map just `Options["temperature"]` → top-level `temperature` and
`Options["num_predict"]` → `max_tokens` in `openRouterChatBody`. These two are
standard OpenAI parameters, low-risk, and they recover the determinism the
harness relies on. `num_ctx` (the genuinely hard one) can stay deferred.

### 5. Telemetry audit should be exhaustive, not two named lines

The plan's point 4 names `harness.go:166` and `harness.go:759`. But the skill
selector path (caveat 1) and any other `appendStep(..., "ollama", ...)` should
be swept too. A single grep for the literal `"ollama"` passed to `appendStep` /
`completeStep`, replacing all of them with the resolved provider, is more
reliable than trusting the two named lines are the complete set.

### 6. Minor: unconfigured-key UX silently degrades

If `HarnessProvider="openrouter"` but no key is set, `providerFor` returns
`"openrouter api key is not configured"` (`openrouter_client.go:138`). Triage
then fails safe to tools, the planner fails safe to the correction loop. It
*works*, but it is exactly the silent-degradation mode the plan's own intro says
it exists to avoid.

**Fix:** surface "OpenRouter key not set for harness" once per turn instead of
degrading, at least for the harness role. See also refinement D, which prevents
the unconfigured state from ever being a configured one.

## Better design (low-effort, high-value refinements)

These sit inside the plan's own framework — same decisions, sharper edges.

### A. Make `harnessTarget` return a struct, and thread it everywhere

The plan already renames `harnessModelFor` → `harnessTarget`. Go one step
further:

```go
type harnessTarget struct { model, provider string }
func (h *HarnessEngine) harnessTarget(primaryModel, primaryProvider string) harnessTarget
```

Pass `harnessTarget` into `triageChatTurn`, `selectSkillForTurn`,
`prepareChatTurnLoop`, `supportsNativeTools`, `responseModelFor`, and
`responseProviderFor`. This makes caveat 2 (model/provider divergence)
structurally impossible — the two values can never come from different
resolutions. It also makes the "don't break the one-model setup" invariant
self-evident rather than something to guard in review.

### B. Expand `strictJSONSchema` to strip + promote + widen

A clear, ordered pipeline:

1. Strip unsupported keywords (`maxItems`, `minItems`, `minimum`, `maximum`,
   `pattern`, `maxLength`, `minLength`, `multipleOf`, `uniqueItems`).
2. Recurse into `properties`, `items`, and `anyOf`/`oneOf`.
3. Promote every key into `required`.
4. Widen optional scalars to `[T, "null"]`.

Add the `enum` + nullable case as an explicit test: the planner's `name` field
has an `enum`, and `strictJSONSchema` must not mangle it. This is where the first
real test failure is most likely, so it deserves a named row in the table-driven
tests.

### C. Map `temperature` + `max_tokens` in `openRouterChatBody`

Two lines, recovers planner determinism, keeps `num_ctx` deferred. See caveat 4.

### D. Settings validity check on save

If `HarnessProvider="openrouter"`, require `OpenRouter.Harness != ""` and a
present key; otherwise fall back to `"ollama"` in `mergeAppConfig`. This mirrors
how `ImageProvider` is normalized at `app.go:1376`. It prevents the
unconfigured-key silent-degrade from ever being a configured state.

## What the plan gets right (keep these)

- **Routing through `ProviderRegistry`** instead of bypassing it — correct, and
  the `ChatProvider` interface already exists for exactly this.
- **Deriving a strict variant** rather than rewriting `harnessToolPlanSchema` —
  keeps the Ollama path byte-identical; the right call.
- **`supportsNativeTools → false`** for non-Ollama, treating native tools as
  enhancement-only — consistent with the existing invariant that a wrong
  fallback costs latency, never correctness.
- **The fail-safe framing throughout** (triage failure → tool path; invalid plan
  → correction loop; `invalidPlanSystemNote` stops the primary model lying) —
  these are exactly the right rails, *provided* the schema transform actually
  produces an acceptable schema (caveat 3).
- **Defaulting `HarnessProvider="ollama"` in `mergeAppConfig`** — the
  backward-compat property is correctly identified as the thing to protect.

## Net assessment

The plan is a solid backbone. The two findings that will actually break it in
practice are **caveat 1 (the skill selector)** and **caveat 3 (`maxItems` in
strict mode)**. Both are omissions rather than wrong decisions, and both have
clean fixes inside the plan's own framework. Caveat 2 is the one most likely to
ship a correctness bug rather than a loud failure, and refinement A makes it
structurally impossible. None of the above argues against the design's direction;
it argues for tightening it before implementation begins.
