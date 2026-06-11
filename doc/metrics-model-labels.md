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
| whitelist request counters (sync proxy + async submit) | `ctrl.WhitelistMetricModel` (these sites run before model resolution): the body-extracted model only when it is the configured model or an operator-enumerated pricing entry, `"*"` when only the wildcard admits it, else `Service.ModelType` — a BOUNDED set, never the raw body string. Requests later REJECTED by the allowlist are still counted (folded to the default model); cross-reference `broker_requests_total{model="",status>=400}` |
| `broker_requests_errors_total`, `broker_request_duration_seconds` | intentionally NO model label (path/status ops metrics; histogram cardinality). Per-model error rates can be derived from `broker_requests_total{status>=400}` |

Two properties to preserve when touching this code:

- **Label values are on-chain model ids, never slugged.** The router joins them against `providers.model_id` verbatim. The one non-id value is the `"*"` sentinel: on wildcard (serve-all) deployments, user strings admitted by the wildcard collapse to it — in `metricModel` and `WhitelistMetricModel` both — so callers can never mint unbounded series.
- **Only bounded values reach the label**: enumerated pricing ids, the configured `ModelType`, or `"*"`. Never raw user input, on any path.

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
