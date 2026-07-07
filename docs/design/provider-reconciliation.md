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
| `is_whitelisted` | BOOL | Part of primary key; whitelisted traffic is counted here (it hits the upstream) but tagged so settlement-side views still exclude it — see [Definitional alignment](#whitelisted-traffic-must-be-counted-even-though-it-is-not-billed) |
| `service_type` | VARCHAR | Informational context (`chatbot` / `speech-to-text` / `text-to-image` / …) |
| `request_count` | BIGINT | Always recorded (unit-agnostic) |
| `input_count` | BIGINT | Raw, same semantics as `Request.InputCount` for the row's `unit` |
| `output_count` | BIGINT | Raw, same semantics as `Request.OutputCount` for the row's `unit` |
| `cached_input_tokens` | BIGINT | Optional sub-category; cache-**read** input tokens (`0` when not reported / not applicable) |
| `cache_write_input_tokens` | BIGINT | Optional sub-category; cache-**write** / cache-creation input tokens (`0` when not applicable) |

Primary key: `(hour, upstream, model, unit, is_whitelisted)`. Row cardinality stays tiny (a
day is ≤ a few dozen rows per model), so a retention pruner analogous to `user_daily_stat`'s
trims old rows. Further sub-category columns (e.g. `reasoning_output_tokens`) are added when
the corresponding usage detail is parsed.

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

## Definitional alignment: what counts as a token / a request

Most reconciliation gaps are not lost data — they are the two sides *counting different
things*. These definitional mismatches are systematic (they recur every period) and must be
handled explicitly, not absorbed as noise.

### Whitelisted traffic must be counted, even though it is not billed

Whitelisted users (internal services, monitoring) bypass billing and **never create a
`request` row** (`proxy.go`; see `user_daily_stat.go`). But that traffic still hits the
upstream and still appears on the upstream's statement. If the rollup only sees billable
requests, every reconciliation shows a **persistent, unexplained shortfall equal to the
whitelisted volume** — and the vendor cannot subtract it for us (it cannot tell which of our
calls were whitelisted).

Therefore the hourly rollup **must count whitelisted traffic too**, tagged with an
`is_whitelisted` flag so reconciliation can include it against the vendor total while
everything else (settlement, `daily_stat`) continues to exclude it. Consequence: the hourly
counter cannot be written *only* inside `AccumulateAndDeleteRequests` (which runs for
billable, settled requests). Whitelisted requests need a separate increment into
`hourly_usage_stat` at response completion, since they are deleted from no table because they
were never inserted into one.

### Token sub-categories: cache, reasoning, audio

`InputCount` collapses what vendors report — and price — as **three disjoint input buckets**.
Anthropic (via the LiteLLM path, `chatbot_litellm.go`) reports `input_tokens` (fresh, excludes
cache), `cache_creation_input_tokens` (cache **write**, itself split by TTL into 5-minute and
1-hour buckets), and `cache_read_input_tokens` (cache **read**). The broker sums all three into
`PromptTokens`/`InputCount`, and surfaces the sub-categories on `Usage`: `PromptTokensDetails
.CachedTokens` (read, earns the discount from #522) and `CacheWriteTokens` / `CacheWrite1hTokens`
(write, billed at a per-TTL premium via #568/#573). Reconciliation records both cache
sub-categories from these fields — but the single `InputCount` still hides them, so both remain
reconciliation-relevant, for two reasons:

- **Token-definition alignment.** A vendor statement itemizes fresh / cache-read / cache-write
  separately. Comparing the broker's collapsed `InputCount` against any single itemized bucket
  leaves a definitional gap — not a bug.
- **Cost / margin.** The three buckets carry three different vendor rates (fresh 1×, read a
  discount, write a premium), so the cost dimension cannot be reconstructed from a single
  input number.

The rollup therefore records `cached_input_tokens` (read) and `cache_write_input_tokens`
(write, the sum of the 5-minute + 1-hour TTL tiers); the fresh bucket is derivable as
`input_count − read − write`. `reasoning_output_tokens` is added likewise once thinking models
are served and `completion_tokens_details` is parsed (see #529).

### What counts as a "request"

Errored / zero-output requests (the broker prunes zero-output rows after an hour), upstream
retries (the broker may count one where the vendor counts the retried attempts), and
client-disconnect (the broker bills the completed response) are all points where "one
request" can mean different things on the two sides. For each, either align the definition
with the vendor or let the tolerance band absorb it — but decide deliberately rather than
discovering the gap during a reconciliation. STT second-rounding (`billableSeconds`) and
broker-vs-vendor timestamp skew are small enough to leave to the tolerance band.

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

- **Reconciliation is per-broker, not fleet-level.** Vendor billing is **per API key, and
  each broker instance uses its own account/key**. So although one vendor is fronted by
  multiple on-chain provider addresses (production `/v1/models` shows `provider_name: "Aliyun"`
  under seven distinct addresses), each of those brokers has its own Aliyun sub-account and its
  own statement — there is no shared account to aggregate. Reconciliation therefore stays on
  each broker: the per-broker `/v1/admin/reconciliation` endpoint reconciles that broker's
  `hourly_usage_stat` against the statement for **its** key. No central aggregation layer is
  needed. Two operational consequences: (1) the Admin must feed each broker the statement for
  *its own* key — pasting another broker's statement will look wildly off; and (2) the
  `upstream` label (e.g. `aliyun`) is not globally unique to a key — it is the broker instance
  that pins the key, so any cross-broker report rollup must distinguish by provider address,
  not by `upstream` name alone. The clean 1:1 (key ↔ broker ↔ statement) holds only while that
  key is not also consumed elsewhere; external use of the same key still shows as an
  irreducible broker-side shortfall.
- **Reconcile on the upstream model id, not `canonical_id` or the user-requested model.** A
  vendor statement itemizes by *its own* model name (OpenRouter bills `zai-org/GLM-5-FP8`,
  Aliyun bills `glm-5`), whereas `canonical_id` collapses distinct upstream models into one
  (`glm-5`) and `Request.ModelName` holds the client-requested id (possibly an alias, and
  rewritten by `UpstreamModel` before dispatch). The rollup's `model` dimension must therefore
  be the identifier actually sent upstream, or per-model lineup against the statement fails and
  same-canonical/different-upstream models collide.
- **Tiered and cache pricing make cost non-linear in tokens.** Many models price by
  input-length tier (`TieredPricingConfig`: the base price is multiplied by the tier whose
  `maxInputTokens ≥ prompt_tokens`, *before* cache billing), and cache read/write carry their
  own rates (including Anthropic's 5-minute vs 1-hour cache-write premiums, `cache_write` /
  `cache_write_1h`). So the cost dimension cannot multiply
  aggregate tokens by a single price — the effective rate is per-request. Either stamp the
  applied effective price/tier on each `Request`, or bucket tokens by tier in the rollup.
- **Currency mismatch on cost.** As above, `cost` is a margin check, not a hard balance.
- **Coarse granularity.** With daily vendor statements, reconciliation resolves to the
  period, not the individual request. If per-request drill-down is ever required, a retained
  per-request billing log (keyed by `request_hash`, not deleted at settlement) can be added
  as a later phase — the aggregate design does not preclude it.
- **STT dimension.** Reconciled by request count (and, later, duration), not tokens.

## Phased rollout

- **Phase 1 — hourly store + Admin reconciliation endpoint.** Add `Request.Upstream` /
  `Request.Unit` / cache sub-category fields, the `hourly_usage_stat` table (written in the
  settlement accumulation path, plus the whitelisted-traffic increment), and the
  `/v1/admin/reconciliation` endpoint that reconciles a vendor statement against the rollup.
  This is the whole value: the Admin-driven ① ↔ ② check.

  > A broker-internal self-check (recompute each request's `fee` from its counts × price;
  > compare accrued revenue to `confirmed` `pending_settlement` totals) was considered as a
  > zero-dependency add-on but **dropped**: the fee recomputation is near-circular (same code,
  > same counts, same price → always equal, unless it re-prices against a different source),
  > and the revenue-vs-settled check overlaps the existing `reconciliation_processor` (② ↔ ③).
  > The only sliver of unique value — a continuous *cached-price vs on-chain-price* consistency
  > check that does not wait for a statement — can be added later as a tiny standalone check if
  > price-cache drift proves to be a real risk.
- **Phase 2 — cross-provider reconciliation at scale.** Persist each run into a
  `reconciliation_report` table for trend analysis, wire the Prometheus metrics and bounded
  summary log, and — when multi-upstream routing lands — stamp `Request.Upstream` from the
  routing layer. No reconciliation-schema change is required at that point, only the change
  to how `upstream` is populated.

## Open questions / future work

- Whether to stamp an explicit per-request effective price/tier column, versus bucketing the
  rollup by tier, to make the cost dimension tier-aware without re-deriving tiers at
  reconciliation time.
- Whether to store the cache-write sub-category split by TTL (`cache_write` vs
  `cache_write_1h`) rather than summed — the broker now bills them at distinct per-TTL
  premiums (#568/#573), so a cost-dimension reconciliation may want them separated.
- Whether to add a 30-minute bucket resolution if a UTC+5:30/+5:45 vendor is onboarded.
- A retained per-request billing log (keeping request-level detail instead of deleting at
  settlement) was considered as an alternative source of truth — it would also give
  arbitrary-range/timezone queries and `request_hash` drill-down. **Deferred**: at ~100k
  requests/day and growing, even a bounded/partitioned raw table is far heavier than the
  hourly rollup, and the raw-detail/drill-down need is currently weak. The hourly aggregate is
  the chosen primary; revisit only if request-level drill-down becomes a real requirement.
- Whether to auto-ingest common vendor statement formats (CSV) via a generic
  column-mapping config — still no per-vendor code, only per-vendor data mapping.
