# Reasoning Parameter Translation Design

This document describes how the broker translates a client's **portable reasoning
intent** — the OpenAI `reasoning_effort` field — into whatever native "thinking"
control the target model's upstream actually understands (e.g. Qwen3/vLLM's
`enable_thinking`). The translation is driven by the model's advertised
`supportedParameters` plus a small in-code `switch`, not by a per-model
translation table.

> For the full list of supported translations (this plus `max_tokens`) and where they
> sit in the request-body pipeline, see [request-translation.md](request-translation.md).

## The problem it solves

Clients want one portable knob. Different upstreams expose different ones for the
same on/off concept. The dialects actually seen in the model catalog:

| Upstream / ecosystem   | Advertised param       | Wire form the broker writes                       |
|------------------------|------------------------|---------------------------------------------------|
| OpenAI                 | `reasoning_effort`     | top-level (no translation needed)                 |
| Qwen3 / GLM on vLLM    | `chat_template_kwargs` | `chat_template_kwargs.enable_thinking` = `bool`   |
| DeepSeek / Qwen on DashScope | `enable_thinking`| top-level `enable_thinking` = `bool`              |
| MiniMax                | `thinking`             | `thinking` = `{"type": "enabled"｜"disabled"}`    |
| OpenRouter             | `reasoning`            | `reasoning` = `{"enabled": bool}`                 |

Note the **advertised name is not always the toggle key**: a vLLM model advertises
the container `chat_template_kwargs` and the toggle lives in its nested
`enable_thinking`. DashScope uses a *top-level* `enable_thinking` (an `extra_body`
key) — a different wire location from vLLM's nested one.

**`preserve_thinking` is intentionally NOT a translation target.** DeepSeek/Qwen on
DashScope advertise it, but it is a *multi-turn* flag that keeps prior turns'
reasoning in context — not an on/off switch. The DashScope on/off toggle is the
top-level `enable_thinking` above. Likewise MiniMax's `reasoning_split` only
controls how reasoning is returned (separate field vs inline `<think>` tags), so
it is not a target either.

**`thinking` on the Anthropic surface is intentionally NOT a translation
target**, even though the same name is a target on the OpenAI surface (MiniMax /
Zhipu GLM). Anthropic's own `/v1/messages` `thinking` control is `{"type":
"enabled", "budget_tokens": N}` — `budget_tokens` is mandatory (≥1024, must be
less than the request's `max_tokens`) and the broker has no basis to compute it.
Writing `{"type": "enabled"}` without it would be rejected by the upstream. The
broker distinguishes the two dialects by the model's `supportedFormats`: a
`thinking` advertisement is only treated as a target when `supportedFormats`
does not declare the Anthropic surface (`requiresAnthropicBudgetTokens` in
`reasoning.go`). Affected models (e.g. `claude-opus-4-8`, `claude-sonnet-5`)
neither get `reasoning_effort` translated nor advertised — a client on the
Anthropic surface sets `thinking` (with `budget_tokens`) directly, as normal.

Two edge cases in that check are deliberate, not oversights:

- **A model that omits `supportedFormats` entirely is treated as NOT requiring
  `budget_tokens`.** `supportedFormats` is documented elsewhere as "omitted ⇒
  unconstrained, accepts every surface," which would argue for the opposite
  default. But most `thinking`-advertising models today are MiniMax/Zhipu-style
  and legitimately omit `supportedFormats`; defaulting the other way would
  silently stop translating `reasoning_effort` for all of them. A genuinely
  Anthropic-native model must set `supportedFormats: ["anthropic"]` explicitly —
  which it needs anyway for `enforceRequestFormat` to reject stray
  `/chat/completions` requests against it.
- **The check is model-wide, not scoped to the current request's surface.** A
  model declaring *both* `openai` and `anthropic` in `supportedFormats`, with
  `thinking` meant only as the OpenAI-surface (type-only) toggle, would have
  `thinking` excluded as a translation target on both surfaces. This is a real
  gap, not one forced by the `/v1/models` advertisement limitation below:
  `AdvertisedSupportedParameters` genuinely can't be surface-scoped (it feeds
  one static list into `GET /v1/models` regardless of surface), but
  *translation* runs at request time, where the actual surface is already
  known (`apiFormatForPath` in `proxy.go`, computed one call before
  `TranslateReasoning` runs) — a surface-aware fix is possible without touching
  advertisement. It was deliberately not built: no shipped config combines
  `thinking` with a dual-surface declaration today, and the fix would mean
  threading the surface through `TranslateReasoning` → `nativeReasoningParam`
  and updating every call site (including most of `reasoning_test.go`) to
  guard against a case nothing currently exercises. Revisit if a real
  dual-surface + `thinking` config is ever needed.

A request reaching the broker carries the portable `reasoning_effort`. The broker
is the only component that knows which upstream a given model maps to, so the
translation belongs here.

## How it works

The mechanism is two pieces — a startup-known set of native parameter names, and a
request-time `switch` — exactly mirroring how the rest of the request pipeline
rewrites bodies (`EnsureStreamOptions`, `EnforceConfiguredModel` in
`internal/ctrl/proxy.go`).

### 1. Which native parameter (driven by `supportedParameters`)

`ModelInfo.SupportedParameters` (`config/config.go`) is parsed once at startup. A
model that needs translation advertises **both** the portable `reasoning_effort`
and its native control, e.g.:

```yaml
supportedParameters: [temperature, top_p, reasoning_effort, enable_thinking]
```

At request time the broker scans the resolved model's `supportedParameters` for a
name it recognizes as a native thinking control (`enable_thinking`, …) and picks
that as the translation **target**. `reasoning_effort` is never a target — it is
the translation *input*. A model that advertises no native control (a genuine
OpenAI-surface upstream) gets no translation: `reasoning_effort` passes through
untouched.

### 2. The `switch` (input → output)

The client's `reasoning_effort` is first normalized to a binary intent:

```
none, minimal   → Off
low, medium, high (any other non-empty value) → On
absent / ""     → Unset  → emit nothing, upstream default stands
```

That binary intent is what every dialect below receives. `chat_template_kwargs`
additionally carries graded depth when the model opts in — see
[Graded depth on `chat_template_kwargs`](#graded-depth-on-chat_template_kwargs).

Then a `switch` on the native parameter name writes the value in the wire location
that dialect expects:

```go
switch nativeParam {
case "chat_template_kwargs": // Qwen3/GLM on vLLM: nested bool
    bodyMap["chat_template_kwargs"]["enable_thinking"] = on
case "enable_thinking":      // DashScope: top-level bool
    bodyMap["enable_thinking"] = on
case "thinking":             // MiniMax: object
    bodyMap["thinking"] = map[string]any{"type": onOff(on)} // "enabled"｜"disabled"
case "reasoning":            // OpenRouter: object
    bodyMap["reasoning"]["enabled"] = on
}
```

The shape of each native control (bool, or MiniMax's object) lives inline in its
`switch` arm — so there is no separate `type` / on-value / off-value data to store.
A new object-shaped control is simply another `case` that builds its own shape; the
set of names recognized in step 1 stays in sync with the `switch` cases by
construction (same vocabulary). The apply step returns whether it wrote anything,
so a recognized-but-unhandled name never causes `reasoning_effort` to be stripped
without a replacement.

## Graded depth on `chat_template_kwargs`

The binary intent above throws away everything OpenAI's ladder says beyond
on/off: `low`, `medium`, `high` and `max` all produce the identical
`enable_thinking: true`, so a client asking to think *less* is billed for full
depth with no signal that the request was ignored. GLM-5.2/5.3-style templates
do expose graded depth — as a nested `chat_template_kwargs.reasoning_effort` —
so on that dialect the depth can be preserved.

It is preserved **only for a model that declares the levels its template
accepts**:

```yaml
supportedParameters: [temperature, reasoning_effort, chat_template_kwargs]
reasoningEffortLevels: [low, high, max]   # GLM-5.3
```

The opt-in is not bureaucracy — there is no value the broker could write
blindly. The accepted set differs per model family (GLM-5.2 takes `high`/`max`,
GLM-5.3 adds `low`, Qwen3.8-style templates take `low`/`medium`/`xhigh`), and a
template that *validates* its input rejects anything outside its own set with a
Jinja error that surfaces as HTTP 400. A model declaring no levels is therefore
translated exactly as it was before this path existed: the bool alone.
`ModelInfo.Validate` rejects an unknown level, and rejects levels declared
without `chat_template_kwargs` in `supportedParameters`, so both mistakes fail at
load time rather than on a live request.

When levels are declared, `resolveChatTemplateEffort` maps the request onto them:
the **deepest declared level that does not exceed the request**, or the
shallowest declared level when the request is shallower than everything
declared. Both keys are then written — the bool as the gate, the level as the
depth — since a template that reads only `enable_thinking` is unaffected by the
extra kwarg.

Two properties motivate that rule over forwarding the client's value verbatim:

- **Monotone.** A template resolves a value it does not recognize to its
  *maximum* depth. Forwarding verbatim therefore inverts OpenAI's ordering: on
  GLM-5.3 (`low`/`high`/`max`) a verbatim `medium` would mean more thinking than
  `high`. Resolution maps `medium` → `low` instead, keeping the ladder ordered.
- **Never rounds up.** An unrepresentable request is honored by the nearest
  level below, not above. The client asked to think less, and reasoning tokens
  are billed as output, so rounding up would charge for depth nobody asked for.

Depth applies only to a thinking-*on* request: `none` (and `minimal`, see below)
is fully expressed by `enable_thinking: false`, and pairing that with a level
would be contradictory. The precedence rule below still comes first — a client
that wrote its own nested `reasoning_effort` gets neither key derived for it.

### Why not a per-model translation table

An earlier draft proposed a per-model `{param, placement, type, on, off}` config
block. That is redundant with the `switch`: placement/type/values are properties of
the *named* parameter, not of the model, so they belong in one shared `switch`,
not duplicated into every model's config. The model only needs to declare *which*
native name its upstream speaks — which it already does via `supportedParameters`.

## Precedence: explicit native parameter wins

Because `supportedParameters` advertises both names, a client may send
`reasoning_effort` **and** the native parameter in one request. Rule:

> If the client already set the native parameter (in its wire location), it is
> left untouched and `reasoning_effort` is not translated. The broker only derives
> the native value from `reasoning_effort` when the native parameter is absent.

This keeps the explicit/advanced path authoritative and prevents the broker from
writing two conflicting controls into one upstream body.

For an object-shaped native control, "set" means one of a specific set of
sub-fields is present, not merely that the container key exists — so a client's
*other* kwargs in that container don't block translation. Two containers carry
more than one such sub-field, because the dialect addresses the same on/off
concept in more than one way:

- `chat_template_kwargs` counts `enable_thinking` (the bool the broker itself
  writes) **and** `reasoning_effort` (the graded `"low"`/`"high"` depth key
  GLM-5.2/5.3-style templates read, anything else meaning maximum). Counting
  only `enable_thinking` would let the broker write `enable_thinking: false`
  into the same map as a client's `reasoning_effort: "high"`; the template
  reads both and thinking ends up off — silently inverting an explicit client
  request. Note the nested `reasoning_effort` is a *chat-template variable*,
  unrelated to the top-level OpenAI field of the same name that the broker
  translates from; the broker recognizes the nested one but never writes it,
  since its own intent is binary.
- OpenRouter's `reasoning` counts `enabled` (the one the broker writes),
  `effort` (OpenRouter's own low/medium/high control), and `max_tokens` (which
  itself implies reasoning is on) — so the broker never layers a derived
  `enabled: false` next to a client's own `effort: "high"` in the same object.

When translation does occur, the broker removes `reasoning_effort` from the
outgoing body: it has been consumed and re-expressed natively, and a Qwen/vLLM
upstream that needs `enable_thinking` may reject the unknown OpenAI field.

## `/v1/models` advertisement

`supportedParameters` advertises **both** `reasoning_effort` (the recommended
portable knob) and the native name (e.g. `enable_thinking`) for models that
support thinking. Clients may send either; the precedence rule above resolves the
both-sent case.

## No per-model default tracking

The broker does not record whether a model defaults thinking on or off. When the
client expresses no intent (`Unset`) the broker emits nothing and the upstream's
own default stands; only an explicit intent causes the broker to write an explicit
value. This is why no `defaultOn` flag is needed.

## Open questions

- Whether `none` vs `minimal` should ever differ (today both → `Off`). In OpenAI's
  semantics `minimal` means "very little reasoning" and `none` is the off switch,
  so `Off` is an approximation for `minimal`. Mapping it to the shallowest
  declared level *only on models that declare levels* was considered and
  rejected: the same request would then mean "off" on one provider and "shallow"
  on another for one canonical model id, which is the class of inconsistency this
  work is trying to remove. Changing it uniformly is a behaviour change for every
  dialect (DashScope, MiniMax, OpenRouter included) and belongs in its own
  decision.
- What `reasoning_effort: none` should do on an upstream that *cannot* disable
  thinking. Zhipu/Tencent-hosted GLM-5.3 rejects it outright (`该模型始终思考，
  不支持关闭思考；请使用 low、high 或 max`, HTTP 400) while a self-hosted vLLM
  deployment of the same weights honours `enable_thinking: false` — so for one
  canonical model id the portable request either works or 400s depending on which
  provider the router picked. Options: reject broker-side with a clear error, or
  downgrade to the shallowest supported level. Needs a config field expressing
  "this upstream cannot turn thinking off".
- Whether to validate an inbound native parameter against the model's declared
  `supportedParameters` and reject mismatches, or forward leniently (today:
  lenient — an explicit native parameter is forwarded untouched).
