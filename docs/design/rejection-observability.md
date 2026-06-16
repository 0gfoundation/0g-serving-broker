# Rejection Observability Design

This document describes how the broker makes **rejected requests** observable — the
classified Prometheus counter, the bounded rejection summary log, and the recommended
production log level. It exists because of a real incident where a provider saw
sustained 15–30 RPS with near-zero revenue and there was **no way to diagnose it from
logs or dashboards**: requests were dying silently at the billing gate.

## The problem it solves

A request can be rejected at several points before it ever reaches the upstream model:

```
POST /chat/completions
  │
  ├─ per-user RPM limit            → reject (rate_limit)
  ├─ per-user TPM limit            → reject (tpm_limit)
  ├─ per-user IPM limit            → reject (ipm_limit)
  ├─ per-user concurrency limit    → reject (concurrency)
  ├─ model-mismatch block          → reject (model_mismatch)
  ├─ billing / balance gate ───────┐
  │     ├─ account not on contract → reject (account_not_exist)
  │     ├─ provider not acknowledged → reject (not_acknowledged)
  │     └─ locked balance below reserve → reject (insufficient_balance)
  │
  ▼
  upstream model (billable)
```

Before this change, the billing-gate rejections were **completely silent**:
`ValidateRequestWithEstimatedFee` sets `ignoreError` on the user-caused paths, and
`handleBrokerError` skips logging whenever `ignoreError` is set. So a flood of
underfunded requests produced **no log line and no metric** — the "high RPS, zero
revenue" funnel was invisible. Meanwhile the rate-limit gate had the opposite problem:
it logged **one warning per rejected request**, which under a sustained flood is an
unbounded, disk-filling log-amplification vector (one incident produced a 191 MB /
1.17 M-line log for a single provider in one day).

## Metric: `broker_requests_rejected_total`

A single counter, labeled only by a **bounded** `reason`:

```
broker_requests_rejected_total{reason="rate_limit"}
broker_requests_rejected_total{reason="tpm_limit"}
broker_requests_rejected_total{reason="ipm_limit"}
broker_requests_rejected_total{reason="concurrency"}
broker_requests_rejected_total{reason="model_mismatch"}
broker_requests_rejected_total{reason="insufficient_balance"}
broker_requests_rejected_total{reason="not_acknowledged"}
broker_requests_rejected_total{reason="account_not_exist"}
broker_requests_rejected_total{reason="upstream_error"}   # unclassified server-side validation failure
```

> `account_not_exist` is recorded whenever the contract account lookup
> (`GetUserAccount`) fails. That lookup logs the underlying error at `error`
> level in the contract layer regardless of `ignoreError`, so a genuine
> RPC/node outage is never silent — but it does count under `account_not_exist`
> rather than a transport-error reason. A sustained spike there with healthy
> chain RPC means real not-yet-funded accounts; correlate with the contract-layer
> error log to rule out an RPC outage.

The counter is incremented **even when `ignoreError` suppresses the per-request log**,
so observability is decoupled from HTTP-error verbosity. With it, "high RPS + zero
revenue" becomes immediately obvious as `reason="insufficient_balance"` climbing while
`broker_requests_total` stays flat:

```promql
sum by (reason) (rate(broker_requests_rejected_total[5m]))
```

> **Naming.** The metric follows the existing `broker_*` family
> (`broker_requests_total`, `broker_requests_errors_total`) so it sits alongside them
> on existing dashboards. Issue #542 referenced it as `inference_request_rejected_total`;
> the `broker_` prefix was chosen for consistency.

### Why `reason` is the only label

`reason` is sourced exclusively from a fixed constant set (`monitor.Rejection*`), so
cardinality is bounded. A per-user label is deliberately **not** used — user addresses
are unbounded and would explode series count. Top offenders are surfaced through the
aggregated log instead (below).

## Bounded rejection summary log

Per-event rejection logging is replaced by a periodic aggregator
(`api/inference/internal/proxy/rejection.go`). Every `rejectionFlushInterval` (60s) it
emits **at most one line per reason** that saw activity:

```
request rejections [insufficient_balance]: 189231 in last 1m0s across 3 user(s); top: 0x4870…a4E9=189000, ...
```

Properties:

- **Bounded volume** — one line per reason per interval, independent of request rate.
  A flood can no longer fill the disk through the log path.
- **Bounded memory** — distinct addresses tracked per reason are capped
  (`maxRejectionUsersPerReason`); the excess is folded into a `(+N from untracked addrs)`
  overflow tally rather than growing the map.
- **Truncated addresses** — full addresses never reach the log (per CLAUDE.md guidance).
- **Final flush on shutdown** — `Proxy.Close()` stops the aggregator after one last flush.

## Recommended production log level

Run mainnet at **`level: info`** (the built-in default). At `level: debug` the proxy
emits ~3+ verbose lines per request (`Proxy: method=...`, `ReadAll success`, full
`request saved` struct, signature lines) — even for silently-rejected requests — which
is what amplified the incident log to 191 MB/day. The classified counter plus the
aggregated summary above provide the diagnosis that `debug` was being abused for, at a
tiny fraction of the volume. Reserve `debug` for short, targeted investigations.
