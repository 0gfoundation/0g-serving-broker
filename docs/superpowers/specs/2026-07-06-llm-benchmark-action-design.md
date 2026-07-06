---
title: LLM Service Benchmark & Readiness GitHub Action
author: sidonie
date: 2026-07-06
status: draft
---

# LLM Service Benchmark & Readiness Action — Design

## 1. Summary

Add a GitHub Actions workflow that installs
[aiperf](https://github.com/ai-dynamo/aiperf), benchmarks a broker-exposed
OpenAI-compatible LLM endpoint, compares the measured metrics against
configurable thresholds, and reports a **READY / NOT READY** verdict with a
metrics table. It is triggered by adding a label to a pull request (and also
runnable on demand via manual dispatch). The verdict is published to both the
GitHub Step Summary and a sticky PR comment. The run is **report-only**: a
NOT READY result does not fail the job or block the PR.

## 2. Goals

- Provide an on-demand way to answer "is the LLM service performing well enough
  to be considered ready?" backed by real latency/throughput measurements,
  invoked simply by labeling a PR.
- Keep configuration out of the workflow file: an operator sets the endpoint,
  model, and thresholds once as repo variables/secret, then just applies the
  label.
- Surface results where reviewers already are — a PR comment — and in the
  Actions run summary.

## 3. Non-Goals (YAGNI)

- **Not** standing up the broker or the mock ServerlessLLM
  (`api/e2e-lora-serving/mock_sllm.py`) inside CI. The endpoint is treated as a
  parameter (a broker-exposed URL), not booted by the workflow.
- **No** gating: a NOT READY verdict is informational; it never fails the job or
  blocks merge.
- **No** downloadable artifacts, cron schedules, or push/tag triggers.
- **No** changes to the Go application code, existing workflows, or
  `contract/`, `libs/`, `token-counter/` submodules.

## 4. Context

The repository already has GitHub workflows under `.github/workflows/`
(`ci.yml`, `build.yml`, `docker-publish.yml`, `claude-code-review.yml`). The
inference broker (`api/inference/`) exposes an OpenAI-compatible surface;
`api/e2e-lora-serving/mock_sllm.py` documents that surface (including
`/v1/chat/completions`). aiperf is a `pip`-installable Python CLI that
benchmarks OpenAI-compatible endpoints and exports console/CSV/JSON reports:

```bash
aiperf profile \
  --model "<model>" \
  --streaming \
  --endpoint-type chat \
  --url http://<endpoint> \
  --concurrency 5 \
  --request-count 10
```

This design layers a threshold verdict + PR-comment/Step-Summary report on top
of that CLI.

## 5. Design

### 5.1 New files

| Path | Purpose |
|---|---|
| `.github/workflows/llm-benchmark.yml` | The label/dispatch-triggered workflow |
| `.github/scripts/aiperf_gate.py` | Parses aiperf JSON, applies thresholds, writes the verdict Markdown (Step Summary + comment body) |

Both live under `.github/` because they are CI tooling, not application code
(kept out of `api/`).

### 5.2 Triggers

```yaml
on:
  pull_request:
    types: [labeled]
  workflow_dispatch:
    inputs: { ... }   # see 5.4
```

The job runs only for the intended label or a manual dispatch:

```yaml
if: >
  github.event_name == 'workflow_dispatch' ||
  github.event.label.name == 'run-llm-benchmark'
```

**Trigger label:** `run-llm-benchmark`.

Required permissions:

```yaml
permissions:
  contents: read          # checkout
  pull-requests: write    # post/update the sticky PR comment
```

### 5.3 Configuration resolution

Because a `pull_request: labeled` event carries no dispatch inputs, all
configuration has a resolution order, evaluated per-parameter in an early
"resolve config" step:

1. **`workflow_dispatch` input** (when present) — highest priority.
2. **Repo variable** (`vars.*`) — the normal source for label-triggered runs.
3. **Built-in default** — fallback baked into the workflow.

| Parameter | Dispatch input | Repo variable | Default |
|---|---|---|---|
| Endpoint URL | `endpoint_url` | `vars.LLM_BENCHMARK_ENDPOINT_URL` | *(none — required)* |
| Model | `model` | `vars.LLM_BENCHMARK_MODEL` | *(none — required)* |
| Tokenizer | `tokenizer` | `vars.LLM_BENCHMARK_TOKENIZER` | `""` |
| Concurrency | `concurrency` | `vars.LLM_BENCHMARK_CONCURRENCY` | `5` |
| Request count | `request_count` | `vars.LLM_BENCHMARK_REQUEST_COUNT` | `20` |
| Streaming | `streaming` | `vars.LLM_BENCHMARK_STREAMING` | `true` |
| Max TTFT p99 (ms) | `max_ttft_ms` | `vars.LLM_BENCHMARK_MAX_TTFT_MS` | `2000` |
| Max latency p99 (ms) | `max_latency_ms` | `vars.LLM_BENCHMARK_MAX_LATENCY_MS` | `10000` |
| Min output tok/s | `min_output_tps` | `vars.LLM_BENCHMARK_MIN_OUTPUT_TPS` | `10` |
| Max error rate | `max_error_rate` | `vars.LLM_BENCHMARK_MAX_ERROR_RATE` | `0` |
| Git ref | `ref` | — | PR head / `main` |

Auth: **`secrets.LLM_BENCHMARK_API_KEY`** (optional). When non-empty it is passed
to aiperf as `-H "Authorization: Bearer …"`. Empty ⇒ unauthenticated.

If the endpoint URL or model resolves to empty, the workflow posts a clear
"benchmark not configured" verdict (see 5.6) and stops — it does not error.

### 5.4 `workflow_dispatch` inputs

The same parameters as the variables above are exposed as optional dispatch
inputs so a manual run can override configuration without touching repo
variables. `endpoint_url` and `model` are the only ones an operator typically
must supply if no repo variables are set.

### 5.5 Job steps

Single job on `ubuntu-latest`:

1. **Checkout** — PR head ref for `pull_request` events; `main` (or `ref` input)
   for dispatch. No submodules — this does not touch Go code.
2. **Resolve config** — apply the 5.3 precedence, export resolved values as step
   outputs / env.
3. **Setup Python** via `actions/setup-python` (3.12) and `pip install aiperf`.
4. **Run aiperf** (`continue-on-error: true`):
   ```bash
   aiperf profile \
     --url "$ENDPOINT_URL" \
     --model "$MODEL" \
     --endpoint-type chat \
     ${STREAMING:+--streaming} \
     ${TOKENIZER:+--tokenizer "$TOKENIZER"} \
     ${API_KEY:+-H "Authorization: Bearer $API_KEY"} \
     --concurrency "$CONCURRENCY" \
     --request-count "$REQUEST_COUNT" \
     --output-directory ./aiperf-out
   ```
   The `Authorization` header is only added when the api key is non-empty.
   `continue-on-error` ensures the report step always runs.
5. **Gate + verdict** — run `.github/scripts/aiperf_gate.py`. It writes the
   verdict Markdown to `$GITHUB_STEP_SUMMARY` and to a file
   (`verdict.md`) for the comment step. **Always exits 0** (report-only).
6. **Post PR comment** — only on `pull_request` events: upsert a sticky comment
   using `actions/github-script` (first-party, pinned by SHA). The script finds
   an existing comment containing a hidden marker
   (`<!-- llm-benchmark-verdict -->`) and updates it, or creates a new one.

### 5.6 Gate script (`.github/scripts/aiperf_gate.py`)

- **Pure Python standard library** — no repo dependencies.
- Reads thresholds, run parameters, and the aiperf output directory from
  environment variables.
- Globs the aiperf output directory for the JSON export and extracts:
  - p99 time-to-first-token → compared to max TTFT
  - p99 request latency → compared to max latency
  - average output-token throughput → compared to min output tok/s
  - failed/total request counts → error rate compared to max error rate
- Produces one Markdown block (written to both `$GITHUB_STEP_SUMMARY` and
  `verdict.md`) containing:
  - the hidden marker comment `<!-- llm-benchmark-verdict -->`
  - a bold **✅ READY** or **❌ NOT READY** verdict line
  - a table: Metric | Measured | Threshold | Result
  - the run parameters (endpoint, model, concurrency, request count)
- **Exit code: always 0** — readiness is conveyed only in the verdict text, per
  the report-only decision. A missing/unparseable JSON export yields a
  **NOT READY** verdict with an explanatory note (never a false READY), but the
  job still stays green.

**Implementation note on the aiperf JSON schema.** aiperf descends from
genai-perf; its JSON export uses metric fields such as `time_to_first_token`,
`request_latency`, and `output_token_throughput` with `avg`/`p50`/`p90`/`p99`
sub-fields, plus request success/error counts. The exact filename and field
paths will be pinned during implementation by inspecting one real aiperf run's
output. The gate script performs defensive field lookup so a schema mismatch
produces an explicit NOT READY note rather than a silent false READY.

## 6. Report example (Step Summary & PR comment)

```
<!-- llm-benchmark-verdict -->
## LLM Service Benchmark — ❌ NOT READY

**Endpoint:** https://broker.example/v1  **Model:** llama-3.3-70b-instruct
**Concurrency:** 5  **Requests:** 20

| Metric                       | Measured | Threshold | Result |
|------------------------------|----------|-----------|--------|
| TTFT p99 (ms)                | 3120     | ≤ 2000    | ❌     |
| Request latency p99 (ms)     | 8450     | ≤ 10000   | ✅     |
| Output token throughput (t/s)| 42.1     | ≥ 10      | ✅     |
| Error rate                   | 0.00     | ≤ 0.00    | ✅     |
```

## 7. Testing

- **Gate script:** run `aiperf_gate.py` locally against a sample aiperf JSON
  export (a fixture captured from a real run) with thresholds set both above and
  below the measured values to confirm the READY / NOT READY verdict text (exit
  code always 0).
- **Missing/garbage JSON:** confirm the script emits NOT READY with a note and
  still exits 0.
- **Workflow:** validate YAML with `actionlint`; dry-run via `workflow_dispatch`
  against a reachable OpenAI-compatible endpoint (e.g. a locally run
  `mock_sllm.py`) to confirm end-to-end wiring, Step Summary, and the sticky PR
  comment upsert.

## 8. Security considerations

- The api key comes from `secrets.LLM_BENCHMARK_API_KEY`, passed only as an HTTP
  header to aiperf; it is never logged.
- **Fork PR caveat:** `secrets` are not available to workflows triggered by PRs
  from forks, and `pull-requests: write` is downgraded for forked PRs. The label
  trigger is intended for same-repo (maintainer-labeled) PRs; forked-PR runs will
  simply run unauthenticated and may be unable to post the comment (they still
  render the Step Summary). This is acceptable and documented rather than worked
  around.
- The workflow requests the minimum permissions: `contents: read` and
  `pull-requests: write`.
- No private keys, wallets, or on-chain interaction are involved.

## 9. Open questions

1. **Threshold defaults** — shipped as conservative placeholders; confirm target
   numbers when real broker measurements are available.
2. **Model default** — required with no built-in default; revisit if a single
   model is the standard benchmark target.
