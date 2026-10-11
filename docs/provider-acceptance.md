# Provider Acceptance Checklist

Every new model / inference provider added behind `router-api.0g.ai` MUST pass the
checks below before being exposed to integrators. The list exists because real
integration bugs (silent `enable_thinking` no-ops, tool-call payloads stuffed into
`content`, reasoning tokens starving `max_tokens` budgets) have been shipped to
partners and observed in the wild. Catching these locally costs minutes; catching
them after a partner integration costs days.

Run every probe below against the candidate provider. **A provider is accepted
only when all 🔴 checks pass.** 🟡 / 🟢 checks are non-blocking but must be
explicitly waived in the PR description with a reason.

---

## How to run

Set these once per session:

```bash
export ZG_BASE=https://router-api.0g.ai/v1     # or staging URL under test
export ZG_KEY=sk-...                            # test account key
export ZG_MODEL=<model-id-under-test>           # e.g. 0GM-1.0-35B-A3B
```

Each probe below is a copy-paste-runnable `curl`. The "Expect" block lists the
shape of a passing response. Capture the full JSON when filing the PR.

---

## 1. OpenAI Chat Completions surface

### 🔴 1.1 Baseline non-streaming completion works

```bash
curl -s "$ZG_BASE/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{
    \"model\": \"$ZG_MODEL\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Reply with exactly: pong\"}],
    \"max_tokens\": 16
  }"
```

Expect:
- HTTP 200
- `choices[0].message.role == "assistant"`
- `choices[0].message.content` is a non-empty string (NOT `null`)
- `choices[0].finish_reason` is `"stop"` (NOT `"length"` for this prompt)
- `usage.{prompt_tokens, completion_tokens, total_tokens}` are all present and consistent
- `x_0g_trace.request_id` and `x_0g_trace.billing.*` are populated

### 🔴 1.2 Streaming works and emits the OpenAI SSE shape

```bash
curl -sN "$ZG_BASE/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{
    \"model\": \"$ZG_MODEL\",
    \"messages\": [{\"role\": \"user\", \"content\": \"Count: 1, 2, 3.\"}],
    \"max_tokens\": 32,
    \"stream\": true
  }"
```

Expect:
- Each line is `data: {...}` followed by `\n\n`
- First chunk delta includes `"role": "assistant"`
- Subsequent chunks have `delta.content` with incremental text
- Final chunk has `finish_reason != null`
- Stream terminates with `data: [DONE]` (if the upstream framework supports it)

### 🟡 1.3 `max_tokens` is honored, not silently truncated

Submit a prompt that should fit in 64 tokens.

Expect: `finish_reason == "stop"` AND `completion_tokens < max_tokens`. If the
provider hits `length` on a trivial prompt, the budget is being consumed by
hidden reasoning — see §2.

### 🟢 1.4 `temperature`, `top_p`, `stop`, `seed` are accepted (not 400'd)

Send each parameter once with a valid value. Expect HTTP 200 in every case.

---

## 2. Reasoning / thinking behavior

This is the section that previously slipped through review. Read carefully.

### 🔴 2.1 Document the default reasoning shape

Run the baseline probe (§1.1) and record:

- Is `message.reasoning_content` present and non-null by default?
- Is `usage.reasoning_tokens` > 0 by default?
- For streaming (§1.2), does any chunk emit `delta.reasoning_content`?

This must be documented in the provider's entry in the model catalog. Integrators
need to know whether they will see reasoning by default.

### 🔴 2.2 At least ONE documented switch must disable reasoning cleanly

A clean disable means: `reasoning_content` is `null` AND no thinking text
leaks into `content`. Run each probe and record results in a 4-row table.

```bash
# A. top-level enable_thinking
curl -s "$ZG_BASE/chat/completions" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{\"model\":\"$ZG_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: pong\"}],\"max_tokens\":32,\"enable_thinking\":false}"

# B. extra_body.enable_thinking
curl -s "$ZG_BASE/chat/completions" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{\"model\":\"$ZG_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: pong\"}],\"max_tokens\":32,\"extra_body\":{\"enable_thinking\":false}}"

# C. thinking:{type:disabled}  (Anthropic-style)
curl -s "$ZG_BASE/chat/completions" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{\"model\":\"$ZG_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: pong\"}],\"max_tokens\":32,\"thinking\":{\"type\":\"disabled\"}}"

# D. chat_template_kwargs.enable_thinking  (vLLM/SGLang style)
curl -s "$ZG_BASE/chat/completions" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{\"model\":\"$ZG_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: pong\"}],\"max_tokens\":32,\"chat_template_kwargs\":{\"enable_thinking\":false}}"
```

For each variant record:

| Switch | `reasoning_content` null? | `reasoning_tokens` == 0? | `content` clean of thinking text / `<think>` tags? |
| ------ | ------------------------- | ------------------------ | -------------------------------------------------- |

Pass if **at least one variant is "yes" in all three columns**. If only D works,
the upstream is parsing `chat_template_kwargs` but the model is still emitting
`<think>` blocks into the rendered text — that fails this check.

### 🔴 2.3 No `<think>` / `</think>` tag leakage in `content`

`grep -E '</?think>'` over the responses from §2.2 must return nothing. A stray
closing `</think>` is a sign the chat template strips the opener but not the
closer; integrators will see broken markup.

### 🟡 2.4 Output budget is not starved by reasoning

```bash
curl -s "$ZG_BASE/chat/completions" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{\"model\":\"$ZG_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Write a minimal valid HTML page that says Hello.\"}],\"max_tokens\":900}"
```

Expect: `completion_tokens - reasoning_tokens >= 200` (i.e. at least ~200 tokens
of actual output got produced under a typical webdev budget). If reasoning
consumes >90% of the budget, the model is not safe for app-builder integrations
under default settings and needs either a concise-reasoning mode or guidance in
its catalog entry.

---

## 3. Tool / function calling

### 🔴 3.1 Standard OpenAI `tools` array is parsed into `tool_calls`

```bash
curl -s "$ZG_BASE/chat/completions" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{
    \"model\": \"$ZG_MODEL\",
    \"messages\": [{\"role\":\"user\",\"content\":\"What is the weather in Tokyo?\"}],
    \"max_tokens\": 256,
    \"tools\": [{
      \"type\": \"function\",
      \"function\": {
        \"name\": \"get_weather\",
        \"description\": \"Get current weather for a city\",
        \"parameters\": {\"type\":\"object\",\"properties\":{\"city\":{\"type\":\"string\"}},\"required\":[\"city\"]}
      }
    }],
    \"tool_choice\": \"auto\"
  }"
```

Expect:
- `choices[0].finish_reason == "tool_calls"`
- `choices[0].message.tool_calls` is a **non-empty array** containing
  `{id, type:"function", function:{name, arguments}}`
- `choices[0].message.content` does **NOT** contain a raw `<tool_call>...</tool_call>`
  block. If it does, the upstream framework is missing a tool-call parser
  (e.g. SGLang requires `--tool-call-parser`, vLLM requires `--enable-auto-tool-choice
  --tool-call-parser <name>`).

### 🔴 3.2 Tool-call arguments parse as valid JSON

`jq` the `arguments` field. It must parse and match the declared schema
(`{"city": "Tokyo"}` in the probe above). Stringified-JSON-inside-JSON is acceptable;
broken JSON is not.

### 🟡 3.3 Tool calls work under streaming

Repeat §3.1 with `"stream": true`. Expect at least one chunk to have
`delta.tool_calls[0].function.{name|arguments}`. Concatenating all
`arguments` deltas in order must yield valid JSON.

---

## 4. Error & quota surfaces

### 🔴 4.1 Auth failure returns OpenAI-shaped 401

```bash
curl -s -o /dev/null -w "%{http_code}\n" "$ZG_BASE/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-invalid-key-test" \
  -d "{\"model\":\"$ZG_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
```

Expect: `401`, body has `error.{message, type, code}`.

### 🔴 4.2 Unknown model returns 404 / 400 with a descriptive error

```bash
curl -s "$ZG_BASE/chat/completions" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ZG_KEY" \
  -d "{\"model\":\"does-not-exist-xyz\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
```

Expect: 4xx with `error.message` naming the model. Not a 500. Not a hang.

### 🟡 4.3 Upstream provider error body is decoded before logging

Trigger an obvious upstream error (e.g. malformed `messages`) and confirm the
broker logs surface the readable upstream message, not a hex/base64 blob.
(Regression coverage for #480.)

### 🟢 4.4 Rate-limit headers are emitted when applicable

When the test account is rate-limited, `X-RateLimit-*` headers should be set.
See `docs/design/rate-limiting.md`.

---

## 5. Settlement & verification (broker-side)

These are non-negotiable for any provider, regardless of model behavior.

### 🔴 5.1 `x_0g_trace.billing` populated and consistent with `usage`

`input_cost + output_cost == total_cost` and both scale with token counts.

### 🔴 5.2 `x_0g_trace.provider` matches the on-chain provider address

The address in the response trace must be the same as the one registered in the
service registry contract for this model.

### 🔴 5.3 TEE response signature verifies (when provider advertises TEE)

For providers that declare a TEE backend, the broker's `processResponse` must
succeed on a real response — i.e. signature verification against the attested
public key passes end-to-end. Run the integration suite:

```bash
cd api && go test -tags integration ./inference/... -timeout 600s
```

against a deployment that points at the candidate provider.

---

## 6. /v1/models metadata

### 🔴 6.1 The model is listed and uses snake_case fields

```bash
curl -s "$ZG_BASE/models" -H "Authorization: Bearer $ZG_KEY" | jq '.data[] | select(.id|test(env.ZG_MODEL))'
```

Expect:
- The model appears in `.data`
- All JSON keys are snake_case (regression coverage for #482)
- Required fields present: `id`, `object`, `owned_by`, plus any catalog-specific
  fields (`context_length`, `max_completion_tokens`, capability flags)

---

## Sign-off

Open the acceptance PR with this table filled in. Paste the curl outputs into a
collapsed `<details>` block so reviewers can spot-check.

| Section | Result | Evidence (request_id or notes) |
| ------- | ------ | ------------------------------ |
| 1.1 Baseline completion | ☐ pass / ☐ fail | |
| 1.2 Streaming           | ☐ pass / ☐ fail | |
| 1.3 max_tokens honored  | ☐ pass / ☐ fail | |
| 2.1 Default reasoning documented | ☐ pass / ☐ fail | |
| 2.2 At least one clean disable switch | ☐ pass / ☐ fail | working switch: ____ |
| 2.3 No `<think>` tag leakage | ☐ pass / ☐ fail | |
| 2.4 Output budget healthy | ☐ pass / ☐ fail / ☐ waived | |
| 3.1 tool_calls parsed | ☐ pass / ☐ fail | |
| 3.2 Arguments valid JSON | ☐ pass / ☐ fail | |
| 3.3 Streaming tool calls | ☐ pass / ☐ fail / ☐ waived | |
| 4.1 401 shape | ☐ pass / ☐ fail | |
| 4.2 Unknown model 4xx | ☐ pass / ☐ fail | |
| 5.1 Billing consistent | ☐ pass / ☐ fail | |
| 5.2 Provider address matches | ☐ pass / ☐ fail | |
| 5.3 TEE signature verifies | ☐ pass / ☐ fail / ☐ N/A | |
| 6.1 /v1/models entry | ☐ pass / ☐ fail | |

A 🔴 fail blocks the provider from being added. A 🟡 fail requires a written
mitigation (e.g. integrator-facing caveat in the catalog) before merge.
