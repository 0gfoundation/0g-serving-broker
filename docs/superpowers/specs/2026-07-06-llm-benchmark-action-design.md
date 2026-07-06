---
title: LLM Service Benchmark & Readiness GitHub Action
author: sidonie
date: 2026-07-06
status: draft
---

# LLM Service Benchmark & Readiness Action — Design

## 1. Summary

Add a manually-triggered GitHub Actions workflow that installs
[aiperf](https://github.com/ai-dynamo/aiperf), benchmarks a broker-exposed
OpenAI-compatible LLM endpoint, compares the measured metrics against
configurable thresholds, and renders a **READY / NOT READY** verdict plus a
metrics table into the GitHub Step Summary. The job **fails** when the service
is not ready, so the run's red/green status is itself the readiness signal.

## 2. Goals

- Provide an on-demand way to answer "is the LLM service performing well enough
  to be considered ready?" backed by real latency/throughput measurements.
- Keep the action self-explanatory and parameterized: an operator supplies the
  broker endpoint URL and (optionally) auth + thresholds, and gets a verdict.
- Surface results where they are immediately visible — the Actions run summary —
  without requiring artifact downloads or external tooling.

## 3. Non-Goals (YAGNI)

- **Not** standing up the broker or the mock ServerlessLLM
  (`api/e2e-lora-serving/mock_sllm.py`) inside CI. The endpoint is treated as a
  parameter (a broker-exposed URL), not booted by the workflow.
- **No** downloadable artifacts, PR comments, cron schedules, or PR/tag
  triggers. The action is manual-dispatch-only and reports to the Step Summary
  only.
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

This design layers a threshold gate + Step-Summary report on top of that CLI.

## 5. Design

### 5.1 New files

| Path | Purpose |
|---|---|
| `.github/workflows/llm-benchmark.yml` | The manual workflow |
| `.github/scripts/aiperf_gate.py` | Parses aiperf JSON, applies thresholds, writes the Step Summary, sets exit code |

Both live under `.github/` because they are CI tooling, not application code
(kept out of `api/`).

### 5.2 Workflow trigger & inputs

Trigger: `workflow_dispatch` only.

| Input | Type | Default | Purpose |
|---|---|---|---|
| `endpoint_url` | string | *(required)* | Broker's OpenAI-compatible base URL |
| `model` | string | *(required)* | Model name sent in requests |
| `tokenizer` | string | `""` | Optional HF tokenizer id for accurate token metrics |
| `api_key` | string | `""` | Optional bearer token → `-H "Authorization: Bearer …"` |
| `concurrency` | string | `5` | Concurrent requests |
| `request_count` | string | `20` | Total requests |
| `streaming` | boolean | `true` | Stream responses (enables TTFT measurement) |
| `max_ttft_ms` | string | `2000` | Threshold: p99 time-to-first-token (ms) |
| `max_latency_ms` | string | `10000` | Threshold: p99 request latency (ms) |
| `min_output_tps` | string | `10` | Threshold: min avg output-token throughput (tokens/s) |
| `max_error_rate` | string | `0` | Threshold: max fraction of failed requests (0–1) |
| `ref` | string | `main` | Git ref to check out |

Threshold defaults are conservative placeholders and can be overridden per-run.
`model` is required (no baked-in default) so the action is explicit about what
it is benchmarking.

### 5.3 Job steps

Single job on `ubuntu-latest`:

1. **Checkout** at `ref` (default `main`). No submodules — this does not touch
   Go code.
2. **Setup Python** via `actions/setup-python` (3.12) and `pip install aiperf`.
3. **Run aiperf** (`continue-on-error: true`):
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
   The `Authorization` header is only added when `api_key` is non-empty.
   `continue-on-error` ensures a run that completes but misses thresholds still
   reaches the gate step (rather than being masked by a nonzero aiperf exit).
4. **Gate + report**: run `.github/scripts/aiperf_gate.py`, passing thresholds
   and the aiperf output directory. The script writes the verdict/table to
   `$GITHUB_STEP_SUMMARY` and exits nonzero on NOT READY, which fails the job.

### 5.4 Gate script (`.github/scripts/aiperf_gate.py`)

- **Pure Python standard library** — no repo dependencies.
- Reads thresholds and the aiperf output directory from environment variables /
  CLI args.
- Globs the aiperf output directory for the JSON export and extracts:
  - p99 time-to-first-token → compared to `max_ttft_ms`
  - p99 request latency → compared to `max_latency_ms`
  - average output-token throughput → compared to `min_output_tps`
  - failed/total request counts → error rate compared to `max_error_rate`
- Writes a Markdown summary to `$GITHUB_STEP_SUMMARY`:
  - A bold **✅ READY** or **❌ NOT READY** verdict line.
  - A table with columns: Metric | Measured | Threshold | Pass/Fail.
  - The run parameters (endpoint, model, concurrency, request count).
- Exit code: `0` when every threshold passes, `1` otherwise (including when the
  JSON export is missing or unparseable — a missing report must never be
  reported as READY).

**Implementation note on the aiperf JSON schema.** aiperf descends from
genai-perf; its JSON export uses metric fields such as `time_to_first_token`,
`request_latency`, and `output_token_throughput` with `avg`/`p50`/`p90`/`p99`
sub-fields, plus request success/error counts. The exact filename and field
paths will be pinned during implementation by inspecting one real aiperf run's
output. The gate script performs defensive field lookup so a schema mismatch
produces an explicit error (and a NOT READY / failed job) rather than a silent
false READY.

## 6. Report example (Step Summary)

```
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
  below the measured values to confirm READY / NOT READY and exit codes.
- **Missing/garbage JSON:** confirm the script fails closed (NOT READY, exit 1).
- **Workflow:** validate YAML with `actionlint`; dry-run via `workflow_dispatch`
  against a reachable OpenAI-compatible endpoint (e.g. a locally run
  `mock_sllm.py` or any test endpoint) to confirm end-to-end wiring and Step
  Summary rendering.

## 8. Security considerations

- `api_key` is a run-time input, passed only as an HTTP header to aiperf; it is
  not logged. (Operators who need it hidden from run logs can wire it to a repo
  secret in a follow-up; the initial version keeps it as an input for the
  "pretend there is a URL" framing.)
- The workflow uses read-only default permissions (`permissions: contents:
  read`), consistent with the existing `ci.yml`.
- No private keys, wallets, or on-chain interaction are involved.

## 9. Open questions

1. **Threshold defaults** — shipped as conservative placeholders; confirm target
   numbers when real broker measurements are available.
2. **Model default** — currently required with no default; revisit if a single
   model is the standard benchmark target.
