# OpenAI SDK Contract Test

This document describes the contract test suite added for
[issue #577](https://github.com/0gfoundation/0g-serving-broker/issues/577): a
test that drives the broker with the **real `openai` npm SDK** over a real TCP
socket, instead of Go structs, to catch wire-level breaks the rest of the test
suite structurally cannot see.

## The problem it solves

The existing integration tests (`api/inference/integration_test/*.go`, build
tag `integration`) drive the broker's `ctrl`/`proxy` layer directly with Go
structs via `httptest.NewRequest` / `engine.ServeHTTP` — in-process, no real
socket. That's enough to test business logic, but it cannot catch a bug that
only manifests when a *real* OpenAI SDK parses the *actual bytes on the wire*:
SSE chunk framing, JSON field shapes, header casing, or which SDK error class
a given HTTP status maps to. The broker rewrites both request and response
bytes on every chatbot call (response sanitization, model-id rewrite,
`max_tokens`/`reasoning_effort` translation, SSE reassembly) — each is a place
where SDK-level compatibility can silently break while the Go-only suite stays
green.

This is a **contract test**, not an e2e test: the upstream is a deterministic,
OpenAI-shaped mock, never a live model.

## Architecture

```
 Node subprocess (run.js, real `openai` npm SDK)
        │  real HTTP over a real TCP socket
        ▼
 httptest.NewServer(env.engine)          <- the broker's actual gin engine,
        │                                   backed by a real MySQL
        │  broker's normal request pipeline    testcontainer
        ▼
 httptest.NewServer (mock upstream)       <- deterministic OpenAI-shaped
                                              mock, driven by request shape
                                              (stream / tools / neither),
                                              never a scenario flag
```

- **Go side** (`api/inference/integration_test/openai_sdk_contract_test.go`,
  build tag `openaicontract`): reuses the same `setupTestEnv`/`createAuthHeader`
  harness as the rest of the `integration_test` package (see
  `helpers_test.go`, whose build tag was widened to `integration ||
  openaicontract` to share it), but wraps `env.engine` in a real
  `httptest.NewServer` instead of calling `ServeHTTP` in-process.
- **Node side** (`api/inference/integration_test/openai_sdk_client/run.js`):
  execs as a subprocess per scenario, using the real `openai` npm package
  (version pinned exactly — no `^` — so an SDK release doesn't cause a false
  failure; bump deliberately). Each scenario performs its own assertions using
  the SDK's own parsing/typing and prints one JSON result line back to the Go
  test.
- **Mock upstream**: behavior is derived entirely from the request the broker
  forwards (the `stream` flag, `tools` presence), never an out-of-band
  scenario flag — so it naturally exercises whatever the request-body
  translation pipeline actually produced. Its default response echoes the
  exact body it received under `_debug_received_body`, giving the test
  visibility into what reached the upstream *after* translation without
  instrumenting production code.

## Coverage

| Scenario | What it proves |
|---|---|
| `TestOpenAISDK_NonStreamChatCompletion` | Non-streaming response shape parses correctly; upstream `id` is rewritten (#184) |
| `TestOpenAISDK_StreamChatCompletion` | SSE chunk framing + `stream_options.include_usage` usage chunk parse correctly |
| `TestOpenAISDK_ToolCalling` | `tool_calls` structured response survives sanitization untouched |
| `TestOpenAISDK_ModelsList` | `client.models.list()` output shape, incl. `reasoning_effort` auto-advertisement |
| `TestOpenAISDK_ErrorMapping_Unauthorized` | Invalid auth surfaces as an SDK error the client can classify |
| `TestOpenAISDK_ErrorMapping_BadRequest` | An unsupported model is rejected as `BadRequestError`/400 (`ctrl.EnforceConfiguredModel`) |
| `TestOpenAISDK_ErrorMapping_RateLimit` | Per-user RPM limiting surfaces as `RateLimitError`/429 |
| `TestOpenAISDK_MaxTokensTranslation` | `max_completion_tokens` → `max_tokens` translation reaches the upstream correctly (see [request-translation.md](request-translation.md)) |
| `TestOpenAISDK_ReasoningEffortTranslation` | `reasoning_effort` → native thinking toggle translation reaches the upstream correctly (see [reasoning-translation.md](reasoning-translation.md)) |
| `TestOpenAISDK_ResponseHeaders_ZGResKey` | The `ZG-Res-Key` header (routing-proof retrieval) is readable via the SDK's raw response (`.withResponse()`) |

Not covered: insufficient-balance (HTTP 402-equivalent) mapping — see
"Known gaps" below.

## Known gaps this test surfaced

Building this suite surfaced three broker-behavior gaps that are documented
(and, where feasible, asserted against) rather than silently fixed — fixing
them is a broker-behavior change, out of scope for a test-only PR:

| Gap | Current behavior | What a real OpenAI API does |
|---|---|---|
| Invalid/missing auth | `BadRequestError` / HTTP 400 (`ctrl.ValidateSession` never wraps its errors with a 401 status) | `AuthenticationError` / HTTP 401 |
| `GetModels` reachability | Registered at bare `GET /v1/models` (`handler.Handler`'s own route group), not under the `/v1/proxy` prefix chat completions use — a real client configured with a provider's serving URL (which includes `/v1/proxy`) cannot reach `client.models.list()` this way today | Same base URL serves both `/chat/completions` and `/models` |
| Insufficient balance | No dedicated 402 mapping; any balance below the fixed 3 0G minimum reserve routes through `ctrl.validateBalanceAdequacy`'s live-contract resync branch, which this suite's harness (no chain client wired up) cannot exercise without a nil-pointer panic — so this suite doesn't test the path at all | `insufficient_quota` / HTTP 402 |

See the top-of-file comment in `openai_sdk_contract_test.go` for the precise
code paths.

## Running locally

```bash
cd api/inference/integration_test/openai_sdk_client && npm ci
cd api && go test -tags openaicontract ./inference/integration_test/... -run TestOpenAISDK -v
```

Requires Docker (a real MySQL testcontainer backs the broker, same as the
`integration`-tagged suite) and Node on `PATH`. The Go test skips itself with
a clear message if `openai_sdk_client/node_modules` hasn't been installed yet.

## CI

`.github/workflows/openai-sdk-contract.yml` runs on every push to `main`
(i.e. every PR merge) and via manual `workflow_dispatch`. It is deliberately
**not** wired into `ci.yml`'s `pull_request` trigger, so it never blocks or
slows down a PR review — but at well under a minute end to end, running it
right after every merge is cheap enough to skip waiting for a nightly cron.
