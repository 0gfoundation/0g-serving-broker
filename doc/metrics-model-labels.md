# Metrics: series identity, model labels, and renames

> Router-side consumption (canonical folding, public stats, alias upkeep) is documented in the router repo: `docs/model-canonical-rollout.md` ("Renames — scenarios & runbook"). This doc covers the broker side: what the labels are, where their values come from, and what a broker operator must do when a model is renamed.

## Series identity

Every metric carries two **const labels** stamped at startup (`monitor.PrometheusInit`):

| label | value | source |
|---|---|---|
| `server` | ServingURL | `config.Service.ServingURL` |
| `provider_address` | provider's on-chain address | wallet-derived (`contract.ProviderAddress`) |

`(provider_address, server)` mirrors the `(address, endpoint)` identity the router's providers catalog is keyed by, so attribution survives URL reuse/re-pointing (which has corrupted URL-only attribution before).

The label is deliberately NOT named `provider`: deployments already attach a `provider` **external label** carrying the human-readable deployment nickname (`deploy/phala/*/your-prometheus.yml`, e.g. `glm-openrouter`), used by provider-grouped ops dashboards. A series-level label with the same name would silently override it.

Usage metrics additionally carry a **dynamic `model` label**, set per request:

| metric family | model value source |
|---|---|
| `broker_requests_total` | the BOUNDED `CtxKeyMetricModel` (stamped by `PrepareHTTPRequest` via `ctrl.metricModel`; never the raw `CtxKeyResolvedModel`, which carries arbitrary user strings on wildcard deployments). Non-billed proxied endpoints that reach `PrepareHTTPRequest` (e.g. video status polls) carry the configured model; "" remains on paths that never reach it — cache-served signature fetch (all centralized deployments), unsupported-endpoint rejections, rate-limit returns — and on requests rejected before resolution (allowlist rejections land in `model=""` with status>=400) |
| `broker_{input,output}_tokens_total`, `broker_audio_seconds_total`, `broker_tokens_per_second`, whitelist token counters | `ctrl.metricModel`: the validated resolved model (multi-model allowlist hit / single-model rewrite), falling back to the configured `Service.ModelType`. A missing resolved model on a multi-model provider is logged at Error (same broken invariant the billing path reports) |
| whitelist request counters (sync proxy + async submit) | `ctrl.WhitelistMetricLabels` (these sites run before model resolution): the body-extracted model only when it is the configured model or an operator-enumerated pricing entry, `"*"` when only the wildcard admits it, else `Service.ModelType` — a BOUNDED set, never the raw body string. Requests later REJECTED by the allowlist are still counted (folded to the default model); cross-reference `broker_requests_total{model="",status>=400}` |
| `broker_request_duration_seconds` | the same BOUNDED `CtxKeyMetricModel` as `broker_requests_total`, so a dashboard's model selector filters latency and throughput consistently. Cardinality is models x upstreams x paths x 11 default buckets |
| `broker_requests_errors_total` | intentionally NO model label (path/status ops metric). Per-model error rates come from `broker_request_failures_total` / `broker_requests_total{status>=400}` |

Two properties to preserve when touching this code:

- **Label values are on-chain model ids, never slugged.** The router joins them against `providers.model_id` verbatim. The one non-id value is the `"*"` sentinel: on wildcard (serve-all) deployments, user strings admitted by the wildcard collapse to it — in `metricModel` and `WhitelistMetricLabels` both — so callers can never mint unbounded series.
- **Only bounded values reach the label**: enumerated pricing ids, the configured `ModelType`, or `"*"`. Never raw user input, on any path.

## The `provider_identity` label: one model, several upstreams

A single canonical model can be served by several upstreams under one provider —
two `modelPricing` entries with the same `model` and different `providerIdentity`,
each with its own `targetUrl`, secret and prices. The router names the one it wants
per request via the `X-0G-Upstream` header (`config.UpstreamIdentityHeader`), which
`ValidateModelAllowlist` binds into `CtxKeyResolvedIdentity` and every downstream
per-model lookup re-reads.

Without a label for it, every metric aggregates those upstreams together: latency,
error rate and token throughput for a model become an average across upstreams whose
performance is unrelated. Each metric that carries `model` therefore also carries
`provider_identity`, stamped alongside it in `PrepareHTTPRequest`:

| source | value |
|---|---|
| `ctrl.metricUpstream` (memoized under `CtxKeyMetricUpstream`) | `ctrl.UpstreamForModel(resolvedModel, resolvedIdentity)` — the per-model `providerIdentity`, else the service-level one, else the `"self"` sentinel for a decentralized provider with no identity |
| `ctrl.WhitelistMetricLabels` (pre-resolution whitelist counters) | the same resolution, from the FOLDED model label and the request's identity header — deriving both halves from one value is what keeps this counter on the same series as the post-resolution token counters for the request |

A request that OMITS `X-0G-Upstream` on a model served by several upstreams is
resolved to the entry with the lowest base price — price, `targetUrl`, secret and
this label all come from that one entry, so every counter agrees and the label
names the upstream that actually served it. (It used to be rejected as an
ambiguous upstream; no OpenAI-compatible client knows to send the header, so a
direct caller could never reach such a model.)

A request that SENDS an identity matching no entry is a different case, not the
same fallback: `ResolveRequestedModel` returns not-found, so it is rejected and
trips the invalid-model rate limiter without reaching any upstream. The whitelist
counters still record it first — they run before resolution, and
`UpstreamForModel` ignores the resolve error — so a stale-header whitelist request
is attributed to the cheapest entry's `provider_identity` for a request nothing
served. Cross-reference `broker_requests_total{status>=400}` when reading
whitelist traffic per upstream, the same way the model row above says to.

`provider_identity` is the only signal for which upstream absorbed header-less
traffic: there is deliberately no per-request log for the pick, and the choice is
by price rather than config order, so it does not move when an operator reorders
the yaml.

**The raw header never reaches a label value.** `UpstreamForModel` returns only what
the pricing config holds, so a forged or stale `X-0G-Upstream` folds to a configured
entry rather than minting a series — the same bound `metricModel` gives the model
label. Cardinality is bounded by the number of configured entries.

The label is `provider_identity`, not `upstream`: `broker_request_failures_total`
already uses `upstream` as a *value* of its `source` label (broker / upstream /
client), and `provider` is reserved for the deployment-nickname external label.
`provider_identity` matches both the `providerIdentity` config key and the
`provider_address` const label already on every series.

**Adding this label is a series switch on EVERY deployment, not only
multi-upstream ones.** `metricUpstream` never yields an empty value on a stamped
request: `UpstreamForModel` falls back to the service-level `providerIdentity`,
and then to the `"self"` sentinel, so `broker_requests_total{model="llama"}`
becomes `broker_requests_total{model="llama",provider_identity="self"}` — a
different series that starts at zero. (Prometheus does drop *empty* label values,
but the stamped path never produces one.)

The value is deliberately not left empty for single-upstream deployments:
`ctrl.UpstreamForModel` is the same resolution the reconciliation rollup's
`Request.Upstream` and the TeeTLS routing proof's `providerIdentity` use, and a
metric label that disagreed with those for the same request would be its own
source of bugs.

Treat the rollout exactly like a rename (see below): `increase()`/`rate()`
windows spanning the deploy under-report, and any alert or recording rule
pinning an exact label set stops matching until it is updated. The
`(provider_address, server)` const labels are unchanged, so totals reconcile
across the switch.

## `external_labels` model: legacy shim only

Historically the `model` label came from a static per-deployment Prometheus `external_labels` entry (slugged by convention). Prometheus only applies an external label when the series doesn't already carry one, so the dynamic label always wins where present. Policy:

- **Multi-model providers: never set it.** A static label cannot express N models, and it would falsely stamp the label-less series (gauges, error counters) with one arbitrary model.
- **Single-model providers on a broker version with dynamic labels: remove it at upgrade time.** Keeping it creates a dual convention (dynamic = raw on-chain id vs external = slug) for zero benefit.
- **Not-yet-upgraded brokers: keep it** — it is their only model signal. Remove together with the binary upgrade.

Note the upgrade itself switches single-model deployments' series label value from the external slug (`glm-5-fp8`) to the on-chain id (`zai-org/GLM-5-FP8`). That is a series switch (see below) and is absorbed by the router's resolution layer; no broker-side action needed beyond the config cleanup.

## Renames: what a broker operator must do

**Invariant:** Prometheus data is append-only. A rename never edits history — the old series stops growing, a new one starts at zero. Do not attempt any compatibility tricks at the metrics layer (re-labeling, dual emission); continuity is reconstructed by the router's canonical folding, whose memory of old names is the router registry's aliases.

| scenario | broker-side change | required coordination |
|---|---|---|
| Single-model provider renames `ModelType` (on-chain rename) | config + chain update; metrics switch automatically | Router `models.yaml` must gain the **old name** as an alias of the canonical — ideally merged **before** the rename ships (registry-first = no attribution gap; reversed order costs a temporary `null` slice on dashboards, never miscounting) |
| Multi-model provider renames one allowlist model id | `modelPricing` entry update; metrics switch automatically | Same as above |
| Broker-declared `canonical_id` changes (service-level or per-model) | config update; router's `providers.canonical_id` follows on its next sync | **Must ship together with the matching router `models.yaml` entry.** The broker-declared canonical is preferred over the router's own resolution, so a canonical the router registry doesn't know produces a dangling id with no `/v1/models` metadata |

The `provider_address` const label is what keeps renames auditable: old and new series share `(provider_address, server)`, so token totals can be reconciled across the rename without relying on the name.
