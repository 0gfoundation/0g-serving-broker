# Request Body Translation

The broker accepts a **portable OpenAI-schema** chatbot request and, before
forwarding it upstream, rewrites certain fields into the **third-party schema** the
target model actually understands. This lets a client speak plain OpenAI while the
broker adapts to whatever the routed upstream (vLLM, DashScope, MiniMax, …) expects.

This document lists **what is translated today**. Each translation is driven by the
model's advertised `supportedParameters`, not a per-model table: the broker only
translates a field when the resolved model advertises the corresponding upstream
parameter. If it advertises nothing translatable, the body passes through untouched.

## Supported translations

### 1. Output token cap — `TranslateMaxTokens`

Same value, different field name. `max_tokens` (original) and `max_completion_tokens`
(its replacement) mean the same thing, but some upstreams accept only one.

| OpenAI schema (client sends) | Third-party schema (broker writes) | Applies when the model advertises |
|------------------------------|-------------------------------------|-----------------------------------|
| `"max_completion_tokens": 256` | `"max_tokens": 256` | `max_tokens` only (e.g. DeepSeek-on-vLLM) |
| `"max_tokens": 256` | `"max_completion_tokens": 256` | `max_completion_tokens` only |

Details: `inference/internal/ctrl/max_tokens.go`.

### 2. Reasoning / thinking control — `TranslateReasoning`

The OpenAI input is `reasoning_effort`. It is normalized to a binary on/off intent:
`none` / `minimal` → **off**; any other non-empty value (`low` / `medium` / `high`)
→ **on**. That intent is then written in the upstream's dialect:

| OpenAI schema (client sends) | Third-party schema (broker writes) | Upstream dialect (advertised param) |
|------------------------------|-------------------------------------|-------------------------------------|
| `"reasoning_effort": "high"` | `"chat_template_kwargs": {"enable_thinking": true}` | Qwen3 / GLM on vLLM (`chat_template_kwargs`) |
| `"reasoning_effort": "none"` | `"chat_template_kwargs": {"enable_thinking": false}` | Qwen3 / GLM on vLLM (`chat_template_kwargs`) |
| `"reasoning_effort": "high"` | `"enable_thinking": true` | DashScope / Aliyun (top-level `enable_thinking`) |
| `"reasoning_effort": "high"` | `"thinking": {"type": "enabled"}` | MiniMax (`thinking`) |
| `"reasoning_effort": "none"` | `"thinking": {"type": "disabled"}` | MiniMax (`thinking`) |

**Not translated:** `preserve_thinking` (a DashScope *multi-turn* context flag, not an
on/off toggle) and MiniMax's `reasoning_split` (output-format control). Advertising
either does not trigger reasoning translation.

Full design: [reasoning-translation.md](reasoning-translation.md).

## Shared rules

All translations above follow the same contract:

- **Keyed on the resolved canonical model id** (`CtxKeyResolvedModel`), not the body's
  `model` field — model validation may already have rewritten that to the upstream id,
  while per-model config is keyed by the canonical id. An empty resolved model selects
  the service-level `ModelInfo`.
- **Driven by `supportedParameters`** — the broker picks the target field/dialect from
  what the model advertises, via a small in-code `switch`, not a per-model table.
- **Explicit client value wins** — if the client already set the third-party field, the
  broker leaves it untouched instead of overriding it from the portable field.
- **Consume-and-replace** — once translated, the portable OpenAI field is removed from
  the outgoing body so a strict upstream (e.g. vLLM) doesn't reject an unknown field.

## Where it runs

Both translations run inside `PrepareHTTPRequest`
(`inference/internal/ctrl/proxy.go`), after model validation has stamped the resolved
model id, and before the operator strip/inject steps. Full chatbot rewrite order:

```
EnsureStreamOptions → RewriteLoRARequest → ValidateModelAllowlist / EnforceConfiguredModel
  → enforceRequestFormat → TranslateMaxTokens → TranslateReasoning
  → StripBodyFields → InjectBodyFields → forward upstream
```

## Adding a new translation

1. Implement `func (c *Ctrl) TranslateX(body []byte, resolvedModel string) ([]byte, error)`
   following the four shared rules above.
2. Detect the target from `supportedParameters`, not a per-model table.
3. Wire it into `PrepareHTTPRequest` in the resolved-model block; place it before
   `StripBodyFields` so operator denylists can still act on the translated result.
4. Add a row to the relevant table above, and cover it with unit tests including the
   multi-model (`GetModelPricing`) and service-level `ModelInfo` fallback paths.
