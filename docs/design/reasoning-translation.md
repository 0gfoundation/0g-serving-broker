# Reasoning Parameter Translation Design

This document describes how the broker translates a client's **portable reasoning
intent** — the OpenAI `reasoning_effort` field — into whatever native "thinking"
control the target model's upstream actually understands (e.g. Qwen3/vLLM's
`enable_thinking`). The translation is driven by the model's advertised
`supportedParameters` plus a small in-code `switch`, not by a per-model
translation table.

## The problem it solves

Clients want one portable knob. Different upstreams expose different ones for the
same on/off concept:

| Upstream / ecosystem | Native control     | Wire placement                  |
|----------------------|--------------------|---------------------------------|
| OpenAI               | `reasoning_effort` | top-level (no translation needed)|
| Qwen3 on vLLM/SGLang | `enable_thinking`  | `chat_template_kwargs.enable_thinking` (bool) |

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

Then a `switch` on the native parameter name writes the value in the wire location
that dialect expects:

```go
switch nativeParam {
case "enable_thinking":
    // Qwen3/vLLM: bool nested under chat_template_kwargs
    bodyMap["chat_template_kwargs"]["enable_thinking"] = on
// add a case per ecosystem as upstreams are onboarded
}
```

The shape of each native control (bool here) lives inline in its `switch` arm — so
there is no separate `type` / on-value / off-value data to store. A future
object-shaped control (e.g. an Anthropic-style `{type: "enabled"}`) is simply
another `case` that builds its own shape; the registry of names to recognize in
step 1 stays in sync with the `switch` cases by construction (same vocabulary).

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

- Whether `none` vs `minimal` should ever differ (today both → `Off`).
- Whether to validate an inbound native parameter against the model's declared
  `supportedParameters` and reject mismatches, or forward leniently (today:
  lenient — an explicit native parameter is forwarded untouched).
