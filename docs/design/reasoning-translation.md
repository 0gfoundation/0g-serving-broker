# Reasoning Parameter Translation Design

This document describes how the broker translates a client's **reasoning intent**
(the OpenAI-portable `reasoning_effort`) into whatever native thinking control the
upstream model actually understands (`enable_thinking`, Anthropic `thinking`,
OpenRouter `reasoning`, …), and why that translation is driven by an explicit
per-model table rather than inferred from the advertised `supported_parameters`
list.

## The problem it solves

Clients want one portable knob. Upstreams expose mutually incompatible ones:

| Upstream / ecosystem | Native control | Value space | Wire placement |
|----------------------|----------------|-------------|----------------|
| OpenAI               | `reasoning_effort` | enum: `minimal`/`low`/`medium`/`high` | top-level |
| Qwen3 on vLLM/SGLang | `enable_thinking`  | bool | **`chat_template_kwargs.enable_thinking`** |
| Anthropic            | `thinking`         | object `{type, budget_tokens}` | top-level |
| OpenRouter           | `reasoning`        | object `{effort \| max_tokens}` | top-level |

A request arriving at the broker carries the portable form; the upstream behind a
given model may need any of the others. The broker is the only place that knows
which upstream a model maps to, so the translation belongs here.

## Normalized intent

The broker reduces every inbound reasoning signal to a single normalized intent
before translation:

```
intent ∈ { On, Off, Unset }
```

`reasoning_effort` maps to intent with **threshold semantics**:

```
none, minimal   → Off      (explicitly disable thinking)
low, medium, high (any other non-empty effort) → On
absent / ""     → Unset    (do not emit any native control; upstream default stands)
```

The decision is deliberately coarse — **any effort level except `none`/`minimal`
turns thinking on**. We do not attempt a faithful effort→budget gradient by default
(see [Effort gradient](#effort-gradient-optional) for the optional extension). The
common case is a binary "think or don't," and a bool is the lowest common
denominator that every listed upstream can satisfy.

`Unset` is distinct from `Off`: when the client says nothing, the broker emits
**nothing**, leaving the model's own default in force. Only an explicit
`none`/`minimal` forces an active disable.

## Why not drive off `supported_parameters`

`supported_parameters` (`config.go` `ModelInfo.SupportedParameters`,
`api/inference/config/config.go:97`) is **static advertisement**, hand-authored by
the operator and surfaced only on `/v1/models` (`models.go:258`, `models.go:375`).
Nothing on the request path reads it. It must NOT become the translation driver,
for three reasons:

1. **A name is not a rule.** The entry is the string `"enable_thinking"` — it does
   not encode the wire placement (top-level vs `chat_template_kwargs`), the value
   type (bool vs enum vs int), or the upstream **default state**. The default state
   is decisive: if a model defaults thinking *on*, honoring an `Off` intent requires
   actively sending `enable_thinking: false`. Presence of the name alone cannot tell
   you whether the disable path must act.

2. **Different ecosystems.** `supported_parameters` is OpenRouter's vocabulary (it
   lists `reasoning`, `include_reasoning`); `enable_thinking` is a Qwen3/vLLM chat
   template kwarg. The backends that actually consume `enable_thinking` typically do
   not publish a `supported_parameters` list at all. Advertising it there is the
   operator hand-bridging two worlds — i.e. config-driven mapping, not detection.

3. **Redundant once the rule exists.** A complete per-parameter rule (placement +
   type + value + default) already answers "send what, where, when." A
   list-membership check on top of it is dead weight.

The advertised list and the translation rule therefore stay **separate concerns**:
the list says what a client *may send*; the table says how the broker *rewrites it*.

## Translation table (per model)

Each model carries an optional reasoning translation rule. Conceptually:

```yaml
reasoningTranslation:
  param: enable_thinking          # native parameter name
  placement: chat_template_kwargs # top_level | chat_template_kwargs | nested
  type: bool                      # bool | enum | int
  defaultOn: false                # upstream default; drives whether Off must emit
```

Resolution per request:

```
intent = normalize(inbound reasoning signals)

switch intent:
  Unset → emit nothing
  On    → emit (param := truthy value)   unless defaultOn already true → emit nothing
  Off   → emit (param := falsy value)    only if defaultOn true; else emit nothing
```

The `defaultOn` short-circuits keep the outgoing body minimal: we never send a
parameter whose value already matches the upstream default.

Models without a `reasoningTranslation` block do not translate — an inbound
`reasoning_effort` is passed through unchanged (relevant for genuine OpenAI-surface
upstreams) or dropped per the existing parameter-forwarding policy.

## `/v1/models` advertisement

`supported_parameters` advertises **both** the portable `reasoning_effort` and the
native `enable_thinking` for models that support thinking. Clients may send either.
This is intentional: the portable knob is the recommended path, while exposing the
native name lets advanced clients address the upstream directly.

## Precedence when both are sent

Because both names are advertised, a client may send `reasoning_effort` AND the
native parameter (e.g. `enable_thinking`) in one request. Rule:

> **An explicitly supplied native parameter wins.** The broker computes the
> normalized intent from `reasoning_effort` only when the native parameter is
> absent. If the native parameter is present, it is passed through untouched and
> the effort value is not translated (to avoid emitting a conflicting duplicate).

This keeps the advanced/explicit path authoritative and prevents the broker from
writing two contradictory controls into one upstream body.

## Effort gradient (optional)

For upstreams whose native control is numeric (`thinking.budget_tokens`,
`reasoning.max_tokens`), a future extension may map effort levels to budgets:

```
low → small budget, medium → mid, high → large
```

This is out of scope for the initial bool-only translation and is noted here so the
table schema (`type: int`, plus a `budgets` map) can accommodate it without a
redesign.

## Open questions

- Whether `none` vs `minimal` should ever differ (today both → `Off`). Some
  upstreams distinguish "no reasoning" from "minimal reasoning"; the bool model
  collapses them.
- Whether to validate an inbound native parameter against the model's declared
  `supported_parameters` and reject mismatches, or forward leniently.
