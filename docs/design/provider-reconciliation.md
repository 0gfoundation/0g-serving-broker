# Broker ↔ Provider Reconciliation Design

This document describes how the broker reconciles its own billing ledger against the
**upstream provider's** billing statement — the missing edge in the system's accounting
triangle. It exists so that a divergence between "what the broker billed users for" and
"what the upstream actually served / charged us for" becomes a measurable number instead
of an invisible leak.

## The problem it solves

Around inference billing, the system maintains **three independent ledgers that should
agree**:

| Ledger | Kept by | Authoritative on | Status before this design |
|--------|---------|------------------|---------------------------|
| **① Upstream ledger** | The upstream engine / vendor (e.g. MiniMax, Ali, a decentralized TEE GPU) | What was actually computed / what the vendor charges | Not in this repo — obtained out-of-band |
| **② Broker ledger** | This repo: per-request `input_count`/`output_count`, `input_fee`/`output_fee`/`fee`, TEE signatures | What was billed to users, at what price | `request` / `daily_stat` / `user_daily_stat` |
| **③ On-chain ledger** | The settlement contract: 0G actually deducted | What settled on-chain | `pending_settlement` + `TEESettlementResult` events |

The **② ↔ ③** edge is already reconciled: `internal/event/reconciliation_processor.go`
scans on-chain `TEESettlementResult` events and matches them against `pending_settlement`
records, expiring stale ones (see `model/pending_settlement.go`). That is
broker-vs-chain reconciliation.

This design adds the **① ↔ ②** edge: broker-vs-provider. With it, the triangle closes —
any single edge that fails to balance localizes the fault to "the upstream over/under-counted",
"the broker's pricing/counting has a bug", or "billed traffic never reached the chain".

### Why this matters even when token counts look "same-source"

In the TeeTLS scenario the broker derives `input_count`/`output_count` from the `usage`
field the upstream returns in each response (`internal/ctrl/chatbot.go`). One might argue
the upstream's daily statement and the broker's counts share an origin and must be equal.
They do not, for two reasons:

1. **Independent aggregation paths.** The upstream's daily invoice is produced by its own
   billing subsystem, at a different time, from a different pipeline than the per-response
   `usage` field. Divergence between them is a real signal.
2. **The broker drops rows.** `request` rows are **deleted at settlement**
   (`ctrl/settlement_tee.go` → `AccumulateAndDeleteRequests`) and zero-output rows are
   pruned after an hour. If the broker crashes after the upstream call succeeded but before
   the request row is persisted, the broker never bills that request — yet the upstream
   still counts it and charges us. That is a **cost leak** (we paid the vendor, we never
   charged the user), and it is exactly what ① ↔ ② reconciliation surfaces.

## Constraints that shape the design

These are hard constraints from how provider statements actually arrive, not assumptions:

- **Manual and periodic.** The upstream statement is not an API. An operator (Admin)
  periodically requests it from the vendor. Reconciliation is therefore **Admin-driven**,
  not an automatic background poller.
- **Heterogeneous across vendors.** Different upstreams expose different fields. MiniMax
  provides input / output / total tokens. Another vendor may provide only a total cost, or
  only request counts. The design must **not** grow per-vendor parsing code.
- **Coarse granularity.** A vendor statement is typically a **daily** total, not a
  per-request log. Reconciliation cannot rely on matching individual `request_hash`es
  against the upstream.
- **Foreign day boundaries.** The vendor's "day" is defined in the vendor's timezone.
  MiniMax bills on a **China-timezone (UTC+8) daily** boundary. The broker's existing
  `daily_stat` is bucketed in **UTC**, so a naive same-date comparison compares different
  sets of requests at the edges.
- **Multiple upstreams per broker (planned).** Today one broker fronts one upstream, but a
  broker will route to **several vendors at once** (e.g. MiniMax *and* Ali), and **the same
  model may be routed to more than one vendor** (failover / load-balancing). Each vendor
  issues its own statement, so the broker must know, per request, **which vendor actually
  served it**.

## Design principles

### 1. Heterogeneity lives in the input data, not in code

The broker never parses a vendor-specific statement format. Instead, when the Admin
obtains a statement, they transcribe it into one **canonical, sparse input record** with
every field optional; fields the vendor did not provide are left null:

```json
{
  "upstream": "minimax",
  "periodStart": "2026-06-29",
  "periodEnd": "2026-06-29",
  "timezone": "+08:00",
  "inputTokens": 12345678,
  "outputTokens": 2345678,
  "totalTokens": 14691356,
  "requests": null,
  "cost": null
}
```

There is exactly **one** reconciliation code path. It compares **only the fields the input
supplies** (a loop over a fixed dimension set, skipping nulls). Adding a new vendor is a
new row of Admin-entered data, never a code change. The only per-vendor facts that ever
exist are *parameters* — the timezone, and which `upstream` label the statement covers.

### 2. The broker must retain finer granularity than the reconciliation granularity

If the broker only stores UTC *daily* totals, it can never re-slice them into a UTC+8 day,
and every reconciliation against a foreign-timezone statement produces spurious
edge mismatches. The broker therefore records **hourly UTC** aggregates, from which any
whole-hour-offset timezone's daily boundary can be reconstructed exactly (see
[Timezones](#timezones-and-boundary-handling)).

### 3. `upstream` is a first-class, per-request dimension, stamped at routing time

Because the same model may be routed to different vendors, `model` alone cannot attribute
usage to a vendor. Each request records the **billing counterparty that actually served
it**, decided at the moment the broker selects and dispatches to a backend (the proxy
layer) — **not** inferred from static config, which would be wrong under failover or
dynamic routing (the intended target ≠ the served target).

## Data model

Two new server-set columns on `model.Request` (`Upstream` and `Unit`). Both are stamped by
the broker, never client-supplied, so — like the existing `ModelName` — they do not appear
in the generated `Bind()` and require no `model/gen` regeneration.

### `Request.Upstream` (new field)

A new column on `model.Request`, populated in the proxy/dispatch path with the vendor label
of the backend that served the request. Immutable once set. Population by scenario:

| Scenario | `upstream` value |
|----------|------------------|
| TeeML (decentralized GPU) | `self` (no external vendor; reconciled against the engine's own logs) |
| TeeTLS, single upstream (today) | seeded from `service.providerIdentity` (e.g. `minimax`) — required and validated for centralized providers (`config.go`) |
| Multi-upstream (planned) | a per-target vendor label from the routing config, stamped at dispatch |

`providerIdentity` exists only for centralized providers and is empty for decentralized
ones, so it is **not** used as the reconciliation key directly — it merely *seeds*
`upstream` in the single-centralized-upstream case. The reconciliation schema and logic are
identical across all three scenarios; only the field's population source changes when
multi-upstream routing lands. **No schema or query change is needed when that happens.**

### `Request.Unit` (new field)

A new column on `model.Request` recording the authoritative billing unit (`tokens` /
`seconds` / `images`) for that request's `InputCount`/`OutputCount`. Stamped at the point the
response processor decides the unit and sets the counts — in particular the `isDurationBilled`
branch in `ctrl/speech_to_text.go`, since STT splits between seconds (whisper) and tokens
(gpt-4o-transcribe) by response shape, so the unit cannot be inferred from `service_type`
alone. Carried into the `hourly_usage_stat` key at settlement.

### `hourly_usage_stat` (new table)

The retained aggregate that reconciliation reads. It mirrors `daily_stat`'s role (a
non-user-scoped rollup written inside the settlement transaction) but at hourly resolution
and with the `upstream`, `model`, and `unit` dimensions added:

| Field | Type | Notes |
|-------|------|-------|
| `hour` | DATETIME (UTC, truncated to the hour) | Part of primary key; bucketed by the request's `created_at`, **not** settlement time (see below) |
| `upstream` | VARCHAR | Part of primary key; vendor label from `Request.Upstream` |
| `model` | VARCHAR | Part of primary key; the served model |
| `unit` | VARCHAR | Part of primary key; the authoritative billing unit for the count columns (`tokens` / `seconds` / `images`) |
| `service_type` | VARCHAR | Informational context (`chatbot` / `speech-to-text` / `text-to-image` / …) |
| `request_count` | BIGINT | Always recorded (unit-agnostic) |
| `input_count` | BIGINT | Raw, same semantics as `Request.InputCount` for the row's `unit` |
| `output_count` | BIGINT | Raw, same semantics as `Request.OutputCount` for the row's `unit` |

Primary key: `(hour, upstream, model, unit)`. Row cardinality is
`hours × upstreams × models × units`, which is tiny (a day is ≤ a few dozen rows per model),
so a retention pruner analogous to `user_daily_stat`'s trims old rows.

It is written in the same atomic path that already accumulates `daily_stat` /
`user_daily_stat` and deletes settled requests
(`db.AccumulateAndDeleteRequests`), so the hourly rollup survives request deletion and never
double-counts.

**Bucket by `created_at`, not settlement time.** `daily_stat` attributes usage to the
*settlement* day (`UTC_DATE()` at the moment of settlement). Reconciliation must instead
attribute usage to when the request actually happened, because that is how a provider
statement buckets it. So the hourly rollup groups each request by `req.CreatedAt.UTC()`
truncated to the hour — computed per request in Go during the accumulation loop — rather than
by the single settlement date the sibling `daily_stat` upsert uses.

**`unit` is authoritative and per-request, not inferred from `service_type`.** The
`input_count`/`output_count` columns carry whatever unit the request was billed in, and that
unit is **not** a function of `service_type`: within `speech-to-text`, whisper responses
(`{"type":"duration","seconds":N}`) are billed by **seconds** while gpt-4o-transcribe
responses (`{"type":"tokens","input_tokens":N,…}`) are billed by **tokens** — the
`isDurationBilled` branch in `ctrl/speech_to_text.go` decides per response. So the unit is
stamped on the `Request` at the exact point that branch runs and `InputCount`/`OutputCount`
are set, then carried into the rollup key. This keeps the aggregate faithful and avoids the
lossy token-column skip `daily_stat` makes for STT (#530):

| `service_type` | `unit` | `input_count` | `output_count` |
|----------------|--------|---------------|----------------|
| chatbot | `tokens` | input tokens | output tokens |
| speech-to-text (whisper) | `seconds` | seconds | 0 |
| speech-to-text (gpt-4o-transcribe) | `tokens` | input tokens | output tokens |
| text-to-image / image-editing | `images` | 0 | image count |

Reconciliation interprets the counts from `unit`: a `tokens` statement (MiniMax) is compared
token-for-token, an `images` statement against `output_count`, a `seconds` statement against
`input_count` (converted to the vendor's unit, e.g. minutes). The "compare only the fields
present" logic is unchanged; only the unit interpretation is keyed off `unit`.

## Timezones and boundary handling

The core rule: **reconcile from hourly UTC buckets, re-summed into the boundary the vendor
declares.** Three mechanisms compose:

1. **Ask and record the vendor's boundary.** The statement's timezone and period are Admin
   inputs (`timezone`, `periodStart`, `periodEnd`). This is free and mandatory — a wrong
   timezone silently biases every comparison.
2. **Re-bucket, don't pre-commit.** Because the broker keeps hourly UTC buckets, it sums the
   exact set of hours that fall inside the vendor's day. A whole-hour-offset timezone maps
   to a whole number of hour buckets, so the reconstruction is **exact**.
3. **Reconcile over the whole statement period with a tolerance band.** Comparing an entire
   billing period (a week, a month) rather than isolated single days confines boundary
   ambiguity to the two edge hours; over a long period their weight is negligible. A
   tolerance band (e.g. variance `< 0.5%` counts as balanced) absorbs residual clock skew
   and sub-hour zones.

### Worked example: MiniMax (UTC+8, daily)

MiniMax's "2026-06-29" is the UTC half-open interval `[2026-06-28T16:00Z, 2026-06-29T16:00Z)`.
The broker sums its hourly buckets over exactly those 24 hours. China observes **no DST** and
is a **whole-hour** offset, so the reconstruction has **zero boundary error** — this is the
clean case the hourly-UTC store is designed to exploit.

### Edge cases

- **DST-observing vendor timezones.** A local "day" is 23 or 25 hours twice a year; summing
  the correct set of hour buckets still reconstructs it exactly.
- **Half-hour / 45-minute zones** (UTC+5:30, UTC+5:45). Hour buckets cannot split a partial
  hour, leaving **≤ 1 hour** of boundary slippage per period edge. Documented; absorbed by
  the tolerance band. If a vendor bills in such a zone and tighter precision is needed,
  30-minute buckets can be adopted without changing the reconciliation logic.

## Reconciliation algorithm

Given a canonical input record:

1. Convert `[periodStart, periodEnd]` in the input's `timezone` to a UTC hour range.
2. Query `hourly_usage_stat` for rows where `hour` is in that range **and** `upstream`
   matches the input's `upstream`; sum over `model` (keep the per-model breakdown for
   drill-down).
3. For each dimension the input supplies (non-null), compute `brokerValue`,
   `providerValue`, `delta`, and `percentVariance`. Skip null dimensions.
4. When both `inputTokens`/`outputTokens` and `totalTokens` are present, additionally
   cross-check `input + output == total` on both sides.
5. Flag any dimension whose `percentVariance` exceeds the tolerance band.

**Cost / 0G is a soft dimension.** A vendor's `cost` is in the vendor's currency (e.g. USD);
the broker's `fee` is in 0G (A0GI wei) charged to users at the on-chain price. They are not
directly equal. When `cost` is supplied, reconciliation converts via the existing
`pricefeed` and reports it as a **margin check** under a wider tolerance — never as a hard
balance. Token / request dimensions are the high-confidence checks.

## Output

Three artifacts, consistent with the broker's existing observability
(`docs/design/rejection-observability.md`):

- **Diff report** returned by the Admin endpoint: per-dimension `brokerValue`,
  `providerValue`, `delta`, `percentVariance`, `withinTolerance`, plus the per-model
  breakdown for any dimension that is out of tolerance.
- **Prometheus metrics** in the `broker_*` family, labeled by a bounded
  `upstream` and `dimension` (never by user address), e.g.
  `broker_reconciliation_variance_ratio{upstream="minimax",dimension="input_tokens"}`.
- **Bounded summary log**: one line per reconciliation run per upstream, following the
  bounded-log discipline already established for rejections.

## Admin endpoint

```
POST /v1/admin/reconciliation
Authorization: Bearer app-sk-<token>
```

Reuses the same admin authentication and whitelist gate as `/v1/admin/usage/daily`
(`internal/handler/usage_stats.go`). The body is the canonical sparse input record; the
response is the diff report. The endpoint is read-only against `hourly_usage_stat` — it
never mutates billing state.

## Assumptions and limitations

- **Upstream ↔ traffic attribution.** Each vendor statement must correspond exactly to the
  traffic tagged with that `upstream` label. If a vendor's API key is also consumed outside
  this broker, that external usage appears as an irreducible broker-side shortfall — verify
  the 1:1 relationship before trusting a mismatch.
- **Currency mismatch on cost.** As above, `cost` is a margin check, not a hard balance.
- **Coarse granularity.** With daily vendor statements, reconciliation resolves to the
  period, not the individual request. If per-request drill-down is ever required, a retained
  per-request billing log (keyed by `request_hash`, not deleted at settlement) can be added
  as a later phase — the aggregate design does not preclude it.
- **STT dimension.** Reconciled by request count (and, later, duration), not tokens.

## Phased rollout

- **Phase 1 — broker-internal self-check + hourly store.** Add the `hourly_usage_stat`
  table and `Request.Upstream`, populate `upstream` from `providerIdentity` (single-upstream
  today), and ship the `/v1/admin/reconciliation` endpoint. Independently, add a pure
  broker-internal audit that needs no vendor statement: verify `fee == input_count ×
  input_price + output_count × output_price` (catches price-cache drift between `pricefeed`
  and the on-chain price), and that period revenue accrued in `daily_stat` matches the sum
  of `confirmed` `pending_settlement` totals (catches unsettled revenue leak on the ② ↔ ③
  edge). This delivers value with zero dependency on any provider integration.
- **Phase 2 — cross-provider reconciliation at scale.** Persist each run into a
  `reconciliation_report` table for trend analysis, wire the Prometheus metrics and bounded
  summary log, and — when multi-upstream routing lands — stamp `Request.Upstream` from the
  routing layer. No reconciliation-schema change is required at that point, only the change
  to how `upstream` is populated.

## Open questions / future work

- Whether to add a 30-minute bucket resolution if a UTC+5:30/+5:45 vendor is onboarded.
- Whether to add a retained per-request billing log for `request_hash`-level drill-down.
- Whether to auto-ingest common vendor statement formats (CSV) via a generic
  column-mapping config — still no per-vendor code, only per-vendor data mapping.
