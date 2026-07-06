# Request Body Translation & Rewrite Pipeline

This document is the map of what the broker does to a chatbot request body between
receiving it from the client and forwarding it upstream. It ties together the
individual rewrite/translation steps, fixes their **order**, and explains how the
pipeline interacts with TEE signing. For the deep design of a specific step, follow
the links in each section.

## The pipeline

All steps below run inside `PrepareHTTPRequest` (`inference/internal/ctrl/proxy.go`),
gated on `svcType == "chatbot" && len(reqBody) > 0`. Order matters — later steps
depend on earlier ones (notably the resolved model id).

| # | Step | Kind | What it does |
|---|------|------|--------------|
| 1 | `EnsureStreamOptions` | rewrite | Adds `stream_options.include_usage` for streaming requests so usage is billable. |
| 2 | `RewriteLoRARequest` | rewrite | Rewrites a `ft-*` fine-tuned model id to its base model + `lora_adapter_name`. |
| 3 | `ValidateModelAllowlist` (multi-model) / `EnforceConfiguredModel` (single-model) | rewrite | Validates the requested model and rewrites the body's `model` to the **upstream** id. Stamps the **canonical** id into `CtxKeyResolvedModel`. |
| 4 | `enforceRequestFormat` | reject | Rejects a request on an API surface the resolved model doesn't declare in `supportedFormats`. Does not modify the body. |
| 5 | `TranslateMaxTokens` | **translate** | Renames `max_tokens` ⇄ `max_completion_tokens` to whichever the model accepts. See `max_tokens.go`. |
| 6 | `TranslateReasoning` | **translate** | Re-expresses `reasoning_effort` as the model's native thinking control. See [reasoning-translation.md](reasoning-translation.md). |
| 7 | `StripBodyFields` | rewrite | Removes operator-denylisted client params (e.g. `logprobs`) the upstream rejects. |
| 8 | `InjectBodyFields` | rewrite | Injects operator server-side fields. Runs after strip so an injected key can replace a stripped one. |

After step 8 the body is forwarded to the upstream. When the response returns it is
sanitized and — for decentralized in-network and centralized providers — TEE-signed
(see [TEE signing](#interaction-with-tee-signing) below).

## Two kinds of step: translate vs rewrite

**Translations (steps 5–6)** re-express a *portable* OpenAI field as the
*upstream-native* field the target model actually accepts. They share one mechanism:

- **Keyed on `resolvedModelStr`** (the canonical id from `CtxKeyResolvedModel`), **not**
  the body's `model` field — step 3 may already have rewritten that to the upstream id,
  while per-model config is keyed by the canonical id. An empty resolved model selects
  the service-level `ModelInfo` (single-model providers with no rewrite trigger).
- **Driven by `supportedParameters`**, not a per-model table. The broker reads what the
  model advertises and picks the target field/dialect from a small in-code `switch`.
  A model advertising nothing translatable → no-op passthrough.
- **Explicit client value wins.** If the client already set the native field, the
  translation leaves it untouched rather than overriding it from the portable field.
- **Consume-and-replace.** Once translated, the portable field is removed from the
  outgoing body so a strict upstream (e.g. vLLM) doesn't reject an unknown field.

**Rewrites (steps 1–3, 7–8)** are not portable-parameter translations — they adjust
the model id, adapter, streaming options, or apply operator config. They do not follow
the `supportedParameters`/`resolvedModel` translation contract above (though 7–8 are
also keyed on `resolvedModelStr` for per-model operator config).

## Interaction with TEE signing

**Body rewriting does not break signature verification.** Two independent signatures
exist and neither is invalidated by the pipeline:

1. **Inbound request auth** (client → broker, `request.go`) signs the **session token**,
   not the request body. Body rewriting is irrelevant to it.
2. **Outbound TEE proof** (broker → client, `signing.go`) signs
   `sha256(reqBody):sha256(respData)`. The `reqBody` hashed here is the body **read back
   from the forwarded request** (`ProcessHTTPRequest` reads `req.Body`, which
   `PrepareHTTPRequest` populated with the fully-rewritten body). The signature is
   produced at the **end** of the pipeline, so it attests exactly the bytes the broker
   forwarded and the response it got back — it is internally consistent by construction.

Consequence: the TEE proof attests **what the broker processed**, not the client's raw
pre-translation body. A verifier that re-hashed the original client body would not match
the proof's request hash — but this has always been true for every rewrite step, so the
verification model does not rely on it. Adding a new translation step is safe as long as
it stays **before** signing (i.e. inside `PrepareHTTPRequest`), which all steps are.

## Adding a new translation

1. Implement it as a `func (c *Ctrl) TranslateX(body []byte, resolvedModel string) ([]byte, error)`
   following the four shared properties above.
2. Detect the target from `supportedParameters` (via `resolveModelInfo` /
   `nativeReasoningParam`-style lookup), not a per-model table.
3. Wire it into `PrepareHTTPRequest` in the resolved-model block (after step 3), and
   place it relative to strip/inject deliberately — translations generally run before
   `StripBodyFields` so operator denylists can still act on the translated result.
4. Cover it with unit tests including the multi-model (`GetModelPricing`) and
   service-level `ModelInfo` fallback paths.
