# Video-Generation Async Billing Design

This document describes how the broker should bill `video-generation` requests when the
upstream (through a translator/gateway or directly) follows the real OpenAI Video API's
asynchronous contract — `POST /videos` returns immediately with a `queued`/`in_progress` job,
and the actual output only becomes known later via `GET /videos/{id}`. It replaces the current
create-time billing in `handleVideoGenerationResponse` with a background poll-to-completion
model, so the fee charged always reflects the provider's actual delivered output, not a guess
made before the video exists.

## The problem it solves

`ctrl/video.go`'s `handleVideoGenerationResponse` bills exactly once, inline, against whatever
the provider returns for the initial `POST /videos` call. This is correct **only** when that
first response already carries the finished result — i.e. when the provider (or a shim in
front of it) blocks until generation completes and returns `status: completed` with the real
`usage.output_video_duration` in one shot.

The broker's own routing layer expects the opposite to be the common case:

- `proxy.go:240-243` defaults the outbound `wait` form field to `false` for video-generation
  requests, "since video generation is typically a long-running operation" — i.e. the broker
  itself asks the provider for real async behavior.
- `const.go:126-128` reserves `AuthRequiredPrefixes = ["/videos/"]` specifically to let a
  client poll `GET /videos/{id}` and fetch `GET /videos/{id}/content` **without being
  billed again** — a design that only makes sense if those calls are expected to be how the
  client eventually learns the job finished.

So the routing/proxy skeleton is built for a genuinely async provider, but the billing code
is not: when a real async provider returns `{"id": "...", "status": "queued"}` from the create
call, `resolveVideoBilling` finds no actual duration, falls back to billing the **client's
requested** duration (`video.go:126-150`, the `videoSourceRequest` path), and the request is
never revisited — `GET /videos/{id}` and `.../content` are hard-coded non-billing
(`proxy.go:472-502`, `charing=false`). The fallback path logs a warning telling the operator to
"configure the upstream/shim to echo seconds" — which is really an admission that today's
billing only works when the provider **disguises a synchronous completion as an async-shaped
response**. Any provider that behaves like real OpenAI/DashScope-style video generation
(create → poll → completed) gets billed on requested duration, permanently, with no
correction — not as a rare degraded case, but as the default outcome of the default (`wait
=false`) code path.

This design closes that gap: the broker itself does the waiting an honest async provider
requires, and only charges once the actual result is known.

## Constraints that shape the design

- **The client-facing contract does not change.** `POST /videos` must keep returning
  immediately with the provider's `id`/`status`, and `GET /videos/{id}` /
  `GET /videos/{id}/content` must keep working as unbilled passthrough — this is required for
  OpenAI-API compatibility and is already implemented. This design only changes what the broker
  does *internally* after create succeeds.
- **The translator (if any) is a pure, stateless protocol translator.** Per the adjacent
  architecture decision for non-OpenAI-native providers (e.g. Alibaba Bailian HappyHorse), the
  translator maps OpenAI-shaped `POST /videos` / `GET /videos/{id}` 1:1 onto the vendor's native
  async API and does not itself wait or hold connections open. The broker is the only party
  that owns the polling loop and the billing decision.
- **Provider job IDs and TTLs are foreign, opaque strings.** DashScope-style `task_id`s are
  valid for roughly 24 hours; some vendors may use different TTLs or ID shapes. The poll
  scheduler must not assume anything about the ID format and must give up (fail, not hang
  forever) once a job has been unresolved for too long. **This applies to the broker's
  INTERNAL handling only** — the id the broker hands to a client is a published contract with a
  format guarantee, because downstream consumers persist and key on it. See
  [Job id contract](#job-id-contract-broker--consumers).
- **Must survive broker restarts without either double-billing or silently dropping revenue.**
  A crash mid-poll must resume polling, not discard the job — unlike a single `POST` create
  call (which cannot be safely retried without risking duplicate generation), a status `GET` is
  a read-only, idempotent operation and is always safe to resume.
- **No dependency on a per-job goroutine or in-memory timer.** With enough concurrent video
  jobs, a goroutine-and-ticker-per-job model (mirroring `async.go`'s worker-pool-per-request
  design) either exhausts goroutines or loses all in-flight timing state on restart. The design
  below keeps all scheduling state in the database instead.
- **Must not let a user hold provider capacity indefinitely without ever being charged.** Since
  billing now happens minutes after the balance was last checked, the broker must reserve
  (or at minimum re-validate) funds at create time, not only at completion time.

## Design principles

### 1. Billing is decided by terminal state, not by which HTTP call happened to return it

Whether the actual output duration arrives in the create response (a shim that blocks) or in a
later poll response (a genuinely async provider), the same billing code path
(`resolveVideoBilling` → `videoOutputUnits` → fee calc, `video.go:295-322`) applies unchanged.
The only thing that changes is **when** that code runs: immediately if the create response is
already terminal, or later — from a background poller — once a poll response is terminal. This
means providers that already work today (shims that block) keep working identically; nothing
regresses.

### 2. Poll state lives in the database, not in memory

A dedicated table records every video job the broker is waiting on, keyed by the provider's job
ID, with a `next_poll_at` due time. A small pool of scheduler workers repeatedly claims due rows,
polls them once, and reschedules or resolves them. Restarting the broker loses no state: due
jobs are just picked up again by the next scan, because "what to do next" was never only in a
goroutine's closure.

### 3. The client-facing async endpoints are untouched; the broker's poller is just another caller of them

The broker's background poller calls the exact same `GET /videos/{id}` path a client would call
directly (through the translator, to the provider) — it does not need a special internal API.
This keeps the translator's contract simple (translate, don't distinguish caller identity) and
means there is only one code path to test for "what does a status response look like," reused by
both the client-facing passthrough and the broker's own poller.

### 4. Fee reservation at create time, true-up at completion

Create-time validates the user has sufficient balance for the **requested** duration
(reusing the existing `ValidateRequestWithEstimatedFee` balance check, the same mechanism
`async.go:188` already uses for the text-to-image async queue). The reserve is used for that
comparison and then **thrown away**: `proxyHTTPRequest` zeroes `InputFee`/`Fee` before
`CreateRequest`, so the row is a zero-fee placeholder, not a persisted estimate. Completion —
whether immediate or via the poller — computes the fee from the actual delivered duration and
writes it, exactly as `UpdateRequestFeesAndCount` does today. Nothing settles before the actual
duration is known.

The reserve is `VideoCreateReserveFee` (`video.go`): `videoReserveUnitsFromRequest × outputPrice`.

### Why the unit basis is guarded rather than trusted

Both inputs — `seconds` and `size` — are client-controlled, while the bill is computed from the
**response**. Every bypass found while building this was the same shape: a request the gate read
differently from the upstream, resolving to the cheapest legal reserve. So the classifier answers
one question per create — *did the client name a duration, and can this gate price it the way the
upstream will?* — with three states (`videoReserveDuration`):

- **priced** — reserve what was named.
- **absent** — the create named no duration. This is a **funded** state: it prices what the model
  publishes, because the upstream applies its own default and bills that.
- **unpriceable** — refused (400). This covers a duration that is non-numeric, non-positive or out
  of `ceilSeconds`' range, **and** every failure to read the request itself.

That last part is the load-bearing one. `absent` being funded means any read failure routed into it
is a fixed discount, so these are all `unpriceable`:

| Shape | Read as before | Actually rendered / billed |
|---|---|---|
| one byte appended after the JSON object | absent (`json.Unmarshal` validates the whole input and populates nothing) | 15s — the translator decodes with `json.Decoder`, which ignores trailing data |
| `{"seconds":"abc"}`, `" 6 "`, `"+6"`, `true`, `[15]`, `{...}` | absent (a wrong JSON type is a hard decode failure for `json.Number`) | whatever a laxer upstream coerces |
| a multipart value padded past the field reader's 1024-byte cap | absent | read in full by the upstream's form parser |
| a multipart field sent twice (`seconds`, `size` or `model`) | first value | Starlette/FastAPI return the **last** |
| a multipart body that cannot be walked to the end | absent | repaired or sniffed downstream |
| a body that is not a JSON object at all | absent | — |
| a billing field in the URL **query** (`?seconds=15`) | the body's value | the query's — `r.FormValue` resolves the query before the body, and the gate is handed only the body |
| `{"seconds":1,"Seconds":15}` | 1 | 15 — Go matches keys onto struct fields case-insensitively and resolves competing variants by document order, so an unordered map cannot know which the upstream took |

Keys are matched case-**insensitively**, which is the price of decoding key-wise: the upstream
decodes into a struct, and `encoding/json` matches object keys onto struct fields regardless of
case. Folding the lookup makes a `{"Seconds":15}` read the same on both sides; more than one
variant of a billing field is refused, because Go resolves competing variants by document order
and a map has none.

`seconds`, `size` and `model` are refused outright in the URL query, plus `model` for
`speech-to-text` and `n` for `image-editing` — the other multipart modalities where a body field the
gate reads sets the fee. The scoping property is "does a body field move the fee?", not "does the gate
resolve a per-model price?": the second was the original rationale here and it let `image-editing`'s
`n` through, where a body of `n=1` plus `?n=10` billed one image and rendered ten. `text-to-image` and
chatbot post JSON, whose decoders read the body only. The check sits above the whitelist branch: a
whitelisted create is unbilled, but it still writes the reconciliation rollup, which would otherwise
name the body's values while the upstream served the query's. The broker forwards the query
verbatim and the upstream reads the create with `r.FormValue`, whose `ParseMultipartForm` populates
`r.Form` from the query *before* appending the body — so the query wins, and the gate is handed only
the body. The OpenAI Video API puts none of them in the query, so refusing costs no legitimate
traffic; merging them here would mean re-implementing Go's precedence as a second reader of one
request, which is the shape that produced every bypass in this list.

Transport is chosen by **Content-Type**, the way `ExtractModelName` chooses it — not by "did this
parse as JSON". `ExtractModelName` itself decodes with a `json.Decoder`, key-wise and case-folded, for the same
reasons the duration does: it used to use `json.Unmarshal` with an exact-key lookup, so one
appended byte — or spelling the key `"Model"` — made it read no model at all and fall back to the
configured default — the reserve priced the default model, the allowlist in
`ResolveModelForBilling` passed a model the caller never named, settlement billed the default's
price, and the upstream rendered the one that was asked for. Falling back from a failed JSON parse into the multipart reader was the mechanism
behind the trailing-byte bypass: on a JSON content type that reader finds no boundary and reports
the field absent. `multipartFormFields` answers value, repetition, truncation and walk-completion
in **one** walk, because an image-to-video create can carry megabytes of reference image and
advancing a multipart reader streams every part it skips.

### What an omitted field is priced at

Omitting `seconds` or `size` is legal — OpenAI's Video API defaults both — so refusing would break
a conforming client, and flooring would hand over the difference. Both fall back to what the model
**publishes** in `GET /v1/models` (`defaultParameters`, via `Service.DefaultVideoSecondsFor` /
`DefaultVideoSizeFor`, resolved per-model exactly as `EffectiveModelInfo` does). That is the number
a caller reading the model card would expect to be charged, and it is what the upstream will apply:

- an omitted `seconds` reserved 1 unit against H3's 4s default;
- an omitted `size` reserved the 1.0 baseline while the vendor rendered — and settlement billed —
  its configured tier. Note this was **not** upstream misbehaviour: the translator's poll response
  reports `Size` as the *rendered* tier, so on a provider that renders exactly one tier the two
  sides simply never spoke the same vocabulary.

This makes a field that used to be pure `/v1/models` documentation load-bearing for billing, and
the two failure modes get different treatment because they are differently visible:

- **Present but unusable** fails the boot (`ModelInfo.Validate`). A YAML `size: 1080` decodes as
  an int, `seconds: .nan` passes every ordered comparison, and both then report "unpublished" at
  runtime — indistinguishable from the operator having published nothing, so the reserve moves
  away from the bill with no error, log or metric. That is a typo the operator cannot otherwise
  see, so it is worth refusing to start over.
- **Absent** is warned at boot (per model, so a per-model `modelInfo` satisfies it and inheritance
  from the service block still works) and refused at request time as a broker-attributed 503. Not
  a boot failure: it would refuse to start every existing video deployment that has not added the
  field, and the warning plus the 503's attribution already put it in front of the operator.

`config.video-standard.example.yaml` publishes both fields.

One non-obvious consequence: `constant.TargetRoute` is keyed on path only, so a bodyless
`GET /videos` (the OpenAI Video API's list operation) reaches the same billing arm. It reserves
nothing — pricing the published default duration for it would demand balance for a video nobody
asked for, and refusing would 503 a read.

### The size ratio, and the tier a model prices by name

The basis is the requested duration weighted by the larger of two answers:

- **the service-level size ratio** (`GetVideoSizeRatio`), clamped to a 1.0 floor.
  `DefaultVideoSizeRatios` prices small frames *below* baseline (`"832x480"` = 0.5), so unclamped a
  caller names a cheap size, reserves half, and is billed for what the upstream actually renders
  (`seconds:15, size:"832x480"` reserved 8 units against a 15-unit bill). The clamp is coherent with
  what these ratios mean — multipliers "relative to the baseline 720x1280" — so a map with no entry
  at or above 1.0 is a misconfiguration, not a cheaper service. The comparison is `!(ratio >= 1)`
  so a `NaN` ratio cannot slip through into `videoOutputCount`'s NaN floor.
- **the resolved model's own billing block**, when it prices that resolution
  (`BillingConfig.HasResolution`). A **duration** the block does not tabulate is answered the way
  settlement answers it: rounded UP to the covering bucket (`NextBucketUnits`), or the table
  **maximum** when nothing covers it. Falling through to the seconds basis on either was an
  under-reserve on one legal integer — `seconds:7` against rows at 6 and 10 reserved 7 units
  against a 40-unit bill (5.7×), and `seconds:12` above every bucket reserved 12 against 40
  (3.3×). `GetVideoSizeRatio` knows only pixel keys, so on a tiered model a
  caller could name `"1080P"` and reserve the baseline against a 2× bucket. Exactness is checked
  separately rather than trusted from `OutputUnits`, because `resolutionMultiplier` answers a
  `per_video_second` miss with the 1.0 baseline *and a nil error* — indistinguishable from a tier
  that genuinely costs 1.0.

A **`per_unit_table`** model prices in units that bear no relation to seconds — a 6s clip at 2K can
be 60 units — so the service-ratio basis is not a conservative fallback for one, it is a different
scale. The published `defaultParameters.size` is therefore used not only when the request names no
size but also when it names one **this model prices nowhere** — including `"1280x720"`, the OpenAI
Video API's documented shape. Both are the same situation from the gate's side: the upstream renders
its configured tier and settlement bills from the response's tier either way. Only when neither the
request nor the published default names a tier the model prices is the reserve refused
(`ErrVideoDefaultSizeUnpublished`, broker-attributed) rather than expressed on the wrong scale.

Scoped to `per_unit_table` deliberately. A `per_video_second` block's `resolutionMultipliers` *are*
seconds multipliers and answer a miss with the 1.0 baseline, which is directly comparable to the
reserve's own clamped basis — treating it the same way refused creates that were priceable and raised
bills the previous behaviour charged less for (`{"seconds":5,"size":"1280x720"}` against
`{720p:1.0, 1080p:1.5}` with a published `1080p` default went 5 units → 8). The same scoping applies
to the settlement-side substitution and the boot cross-check.
The per-model `videoSizeRatios` map is also consulted, taking the larger of it and the
service-level map: it is a per-model-capable field that `GET /v1/models` advertises per model, and
the reserve read only the service scope, so a published per-model ratio was used by nothing.

Two further properties are deliberate and worth not "fixing" by accident:

- **It does not call `videoOutputUnits`**, which is what settlement bills on, and the two are not
  interchangeable in either direction. They read `size` from different vocabularies: settlement sees
  the response's rendered tier (`"2K"`), which is what a `per_unit_table` list is keyed on, while a
  create carries the client's free-text size. A resolution the table carries no rows for at all
  finds no covering bucket, so `videoOutputUnits` falls to the table **maximum** — as a reserve that
  refuses callers who can afford the real bill, and it double-counts the `per_unit_table_miss`
  operator signal settlement already emits for the same request. (A duration-only miss rounds up to
  the next bucket instead; it is the absent-resolution case that reaches the maximum.) Swapping the
  other way — settling on the reserve's basis — would bill the requested duration instead of the
  delivered one and silently under-bill. The names carry the basis for that reason.
- **It prices off `GetCachedService`, not `GetBillingPrices`.** `CtxKeyResolvedModel` is set by
  `PrepareHTTPRequest`, which runs *after* the balance check, so per-model pricing is not available
  at gate time and asking for it would log a spurious "resolvedModel missing" ERROR on every video
  create. The service price is the configured ceiling over all models (USD-denominated services get
  the live max wei price overlaid), so the reserve's per-unit price is a true ceiling. Note that
  ceiling is over per-*unit* prices, so it does not compensate for a unit count the reserve
  under-counts — see the residuals.

### Failure classes and how they are attributed

`VideoCreateReserveFee` returns three distinguishable classes, and `proxyHTTPRequest` attributes
each one differently — the split is the point, because folding them blamed the caller for operator
and broker faults:

| Class | HTTP | Attribution | Rejection reason |
|---|---|---|---|
| `ErrVideoSecondsUnpriceable` | 400 | client | `invalid_request` |
| `ErrVideoModelNotServed` | 400 | client | `model_mismatch` |
| `ErrVideoBillingFieldInQuery` | 400 | client | `invalid_request` |
| `ErrVideoDefaultDurationUnpublished` | 503 | broker | `pricing_unavailable` |
| `ErrVideoDefaultSizeUnpublished` | 503 | broker | `pricing_unavailable` |
| `ErrPricingUnavailable` (stale USD snapshot) | 503 | broker | `pricing_unavailable` |
| anything else (contract RPC failure, unparseable configured price) | 503 | broker | `upstream_error` |

Every row is recorded, including the broker-caused ones. The argument for classifying the
client-caused rejections — a request refused at the billing gate must not die unclassified — applies
at least as strongly to a fault entirely on this side: an operator whose config publishes no default
duration refuses *every* conforming create, and a contract-RPC outage refuses all of them, both with
nothing but a status label to see it by. `upstream_error` is the documented catch-all for a
server-side failure with no more specific classification.

Only `ErrPricingUnavailable`'s message is replaced before it reaches the client: it carries the
internal wrap, that pricing is USD-denominated, the feed's staleness threshold and the age of the
last update — and `errors.Response` sanitizes only at *exactly* 500, so a 503 ships whatever it is
handed. The two `Unpublished` sentinels are passed through: their text is curated and tells the
caller what to do.

`ErrVideoModelNotServed` exists because the allowlist's own check runs in `PrepareHTTPRequest`,
*after* this gate: without it, a caller enumerating model names on the video path was told their
`seconds` was invalid. Refusing here also short-circuits the only other caller of
`recordModelMismatch`, which is what feeds the per-user limiter that BLOCKS such a caller — the
rejection counter alone would have left the video path with no enumeration throttle at all — so
this arm calls `Ctrl.RecordModelMismatch` itself. 

The final row is explicit for two separate reasons. Retryability: `errors.Response` defaults an
unclassified error to 400, which told the client a broker outage was their fault — and this path is
a **new dependency**, since `GetBillingPrices` took the per-model branch and never read the contract
at gate time, so an RPC blip has to be retryable rather than terminal. Disclosure:
`errors.Response` replaces the message only at EXACTLY 500, so a 503 body ships whatever it was
handed — a contract-read failure put `dial tcp …: connection refused` in front of the caller. The
cause is logged at the call site and the client gets a generic message.

The same `ErrPricingUnavailable` → 503 mapping was added to `proxy.handleBrokerError` itself, which
its two siblings (`ctrl.handleBrokerError`, the `/v1/service` handler) already had. That also fixes
the two pre-existing `GetBillingPrices` call sites on this path — including the whitelist one — so
during a rate-feed outage `/chat/completions`, `/images/*` and `/audio/*` now answer 503 instead of
a terminal 400 as well.

### Known residuals

Named because they are the difference between "the gap is closed" and "the gap is smaller". The
reserve-vs-bill delta itself IS measured now (entry 4); what these describe is the shapes where the
delta is expected to be non-zero, and why.

1. **The reserve bounds the size of one request, not the number of them.** An async create writes
   its `Request` row with `Fee = "0"` and it stays there until the poller resolves it minutes
   later, so `CalculateUnsettledFee` (`SUM(fee) WHERE processed = false`) sees nothing for in-flight
   jobs and the gate re-admits at the same balance: N concurrent creates all pass on a balance sized
   for one. There is no broker-side in-flight cap on video creates. Not fixed here on purpose:
   carrying reserves into `unsettled` needs `FailVideoPollJob` / `TimeOutVideoPollJob` to clear them,
   or a job that never delivers settles as real revenue and the caller pays for a video they never
   received. That is a poll-lifecycle change, not a reserve change.
2. **The tier is decided by a config the gate cannot read.** This is one root cause, not two
   residuals, and naming it that way is the point: for a pixel-dimension `size` the rendered
   resolution comes from `MINIMAX_RESOLUTION` in the **translator's** environment — a separate
   process, defaulting to `"2K"` — and the broker's only proxy for it is the model's published
   `defaultParameters.size`. Two independent configs that must agree, with nothing enforcing
   coherence and no signal when they diverge. "The vendor picks the tier" is really "a second config
   file picks it", which is fixable directly (validate the two against each other, or have the
   translator echo its configured resolution in the create response) rather than paid for with a
   blanket over-reserve.

   One half of this is closed rather than accepted: a pixel `size` whose height names a tier the model
   DOES price is now spelled as that tier before any lookup (`PricedResolutionAlias`), so
   `"1920x1080"` on a card keyed `{720p, 1080p}` reserves exactly what it settles. The residual below
   is only the genuinely unnameable case — `"1920x1080"` against a card keyed `{2K, 4K}`, which is the
   live MiniMax shape. Before that, comparing pixel dimensions against tier names as opaque strings
   produced BOTH failures depending on which side the fallback picked: the 1.0 baseline (reserve 5,
   bill 8) or the dearest row (reserve 40, bill 5 — an 8× refusal of solvent callers).

   Until then, two shapes:

   (a) **The published default disagrees with what the translator renders.** The reserve prices the
   published tier and settlement bills what the response reports, so the gap is the distance between
   the two configs. On the DashScope path the translator derives the tier from the pixel size (max
   side ≤ 1280 → 720P, above → 1080P) and reports no `size` at all, so a mismatch is invisible on
   both sides — a broker-vs-vendor gap rather than a reserve-vs-bill one.

   (b) **A reported tier the model prices, above the tier the reserve priced.** Note the condition:
   not "above both the requested and the published one". With a DEAR published default,
   `{"seconds":6,"size":"768P"}` against a table whose `1080P@6` row is 60 units reserves 6 and
   settles 60 — **10×** — as soon as the upstream reports the published tier. Every fixture in this
   repo publishes the cheap tier, which is why that shape needs saying out loud. It is the upstream's
   own statement, so it is billed as stated rather than repriced, and closing it would mean reserving
   the model's dearest tier for every create — taxing the common case to cover the uncommon one.
   Entry 4's counter fires on exactly this.

   An UNTABLED reported tier is in neither: it is substituted for the price and metered as a
   `per_unit_table` miss.
3. **`seconds` the upstream clamps, in either direction.** `{"seconds":1}` prices at 1 unit while the
   translator raises it to the model floor (H3: 4s) and bills that — an under-reserve. Above the
   ceiling it is the mirror image and costs the *caller*: `{"seconds":60}` is priced at 60 units
   though the translator clamps to 15 and bills 15, so a solvent caller can be refused for a request
   the upstream would have served (the 0G Router accepts up to 3600). On a `per_unit_table` model the
   over-reserve does not scale with the requested duration — it jumps to the table maximum, because
   the reserve must mirror what settlement bills when no bucket covers the observation. Both
   directions come from one gap: the model's priceable duration range is published nowhere the gate
   can read it. `defaultParameters` closed the omitted cases the same way a published min/max would
   close these — and would also let the broker reject an out-of-range duration outright instead of
   having it silently clamped upstream, which is the better contract anyway.
4. **It is now observed.** `broker_video_reserve_shortfall_total` counts settlements that billed more
   units than the reserve had gated. The reserve is not persisted, so `checkVideoReserveCoverage`
   RECOMPUTES it from the request body settlement already holds — one pure parse, no new column, no
   carrying it on the poll job.

   This is the signal that catches the class rather than an instance of it. Every under-reserve this
   path has had was the reserve (reading the REQUEST) and settlement (reading the RESPONSE)
   disagreeing, and each fix for one instance opened its mirror — three rounds running, on the same
   axis. A non-zero value is expected (residual 2b is exactly that shape); a moving RATE means the
   reserve's model of the upstream has drifted, which is when someone should look. It cannot refuse
   anything: by settlement the video exists, and refusing to bill would serve it free.


## Data model

### `VideoPollJob` (new table)

Tracks a single provider video-generation job the broker is waiting on to reach a terminal
state. One row per `POST /videos` call that returned a non-terminal status.

| Field | Type | Notes |
|-------|------|-------|
| `id` | Model (standard) | |
| `ProviderJobID` | `varchar(255)`, indexed | The `id` the provider/translator returned from create. Opaque, vendor-defined. |
| `RequestHash` | `varchar(255)`, indexed, unique | Links back to the `Request` row created at create time (estimated fee, user address). One video job, one request. |
| `PollURL` | `text` | Fully-resolved `GET` URL to poll (translator/provider base + path), captured at create time so the poller does not need to reconstruct routing decisions later. |
| `RequestBody` | `mediumblob` | The original client request bytes — needed by `resolveVideoBilling`'s request-duration fallback and by TEE signing at completion. |
| `RequestedSeconds` | `bigint` | Parsed once at create time, so a poll response that (like real OpenAI create responses) merely echoes the request never needs re-parsing the original body just to get a fallback number. |
| `Status` | `varchar(32)`, indexed | `pending` (queued, not yet polled) / `polling` (claimed by a worker, in flight) / `completed` / `failed` / `timed_out` |
| `Attempts` | `int` | Poll attempts so far; informational + used for backoff (see below). |
| `NextPollAt` | `datetime`, indexed | When a scheduler worker is next allowed to poll this row. The scan query is `status IN (pending) AND next_poll_at <= now()`. |
| `ExpiresAt` | `datetime`, indexed | Hard ceiling — `created_at + MaxPollDuration`. Past this, the job is marked `timed_out` regardless of provider state. |
| `ErrorMessage` | `text` | Set on `failed`/`timed_out`. |
| `IsWhitelisted` | `tinyint(1)` | Set when the originating request was whitelisted (unbilled). See [Whitelisted (unbilled) traffic](#whitelisted-unbilled-traffic). |

`status='polling'` additionally has an implicit lease: a row claimed by a worker is only
considered "stuck" (eligible for another worker to reclaim) if `next_poll_at` has passed without
a status change — the claim itself sets `next_poll_at` far enough in the future to cover one HTTP
round trip, so a crashed worker's claimed rows become claimable again automatically once that
window elapses, with no separate crash-recovery pass needed (see
[Crash recovery](#crash-recovery-is-the-normal-path-not-a-special-case)).

## Lifecycle

### 1. Create (`POST /videos`)

`handleVideoGenerationResponse` is split at the point it currently always bills
(`video.go:295`):

1. Validate balance against the **requested** duration (`ValidateRequestWithEstimatedFee`,
   the same call `SubmitAsyncJob` already makes) before creating the `Request` row, so an
   under-funded user is rejected at create time as today, not minutes later when the job
   would otherwise complete for free.
2. Write the provider's create response to the client unchanged (`video.go:266` — no change,
   this already returns immediately without waiting on billing).
3. Parse the create response's `status`/terminal fields (already available via
   `videoResponseFields`, `video.go:56-73` — no new parsing code, just branching on it):
   - **Terminal** (`completed`, or any response `resolveVideoBilling` can already extract an
     actual duration from): bill immediately, exactly as today. No `VideoPollJob` row is
     created. This is the existing behavior for shims that block — unchanged.
   - **Non-terminal** (`queued`/`in_progress`, no actual duration available): create the
     `Request` row with the estimated fee from step 1, and insert a `VideoPollJob` row with
     `NextPollAt = now() + initial poll interval`.

### 2. Background poll scheduler

A small fixed pool of workers (config: `VideoPoll.MaxConcurrentPolls`) repeatedly:

1. Claim a batch of due rows: `UPDATE video_poll_job SET status='polling', next_poll_at=now()+leaseWindow WHERE status='pending' AND next_poll_at<=now() LIMIT batchSize` (single atomic claim, same pattern as any competing-consumers queue — no separate locking needed).
2. For each claimed row, issue one `GET` to `PollURL` (through the same HTTP client/timeout
   settings as the rest of the proxy layer).
3. On a terminal `completed` response: run the existing billing path
   (`resolveVideoBilling` → `videoOutputUnits` → fee calc → `UpdateRequestFeesAndCount`) against
   the poll response body instead of the create response body, sign the response for TEE
   verification exactly as the sync path does today (`signChatWithKey`, `video.go:271-276`) so
   `GET /videos/{id}/content`'s `ZG-Res-Key` continues to work, and mark the row `completed`.
4. On a terminal `failed` response: mark `failed`, bill nothing (the fee reservation from create
   is released, not charged — see [Open questions](#open-questions) on how reservation release
   is implemented).
5. On a non-terminal response: increment `Attempts`, set
   `NextPollAt = now() + pollInterval` (a fixed interval is sufficient given providers already
   recommend one, e.g. DashScope's 5-15s; exponential backoff is unnecessary complexity for a
   bounded-attempts poll), set `status` back to `pending`.
6. If `now() > ExpiresAt`: mark `timed_out` regardless of the poll result, log loudly (this is a
   real accounting gap — the provider may have delivered a video the broker never billed for —
   and should be surfaced the same way `video.go:301`'s "billing indeterminate" case already is,
   not buried at `Warn`).

### 3. Crash recovery is the normal path, not a special case

Because all scheduling state (`status`, `NextPollAt`) lives in the database and a worker's claim
is a lease with a timeout rather than an in-memory lock, a broker restart requires **no explicit
recovery step**: any row left in `polling` past its leased `NextPollAt` is picked up by the next
scan exactly like a fresh `pending` row. This is deliberately different from `async.go`'s
`MarkProcessingAsyncJobsAsFailed` (`db/async_job.go:56-70`), which must fail in-flight jobs on
restart because their state (a single in-flight `POST`) cannot be safely resumed without risking
a duplicate provider call. Polling has no such hazard — `GET` is idempotent — so resuming is
strictly better than failing, and failing here would recreate the exact billing gap this design
exists to close.

### 4. Client-facing polling is unaffected

`GET /videos/{id}` and `GET /videos/{id}/content` keep working exactly as today — unbilled
passthrough to the translator/provider (`AuthRequiredPrefixes`, `proxy.go:472-502`, unchanged).
A client can see `completed` and download the video before the broker's own poller happens to
observe the same terminal state; this is fine and not a race that needs closing, for the same
reason it is already fine that chatbot/image billing finalizes after the response is written to
the client (`video.go:266` writes before `video.go:295` bills) — content delivery has never been
gated on billing completion in this codebase, and gating it here would reintroduce the latency
this design exists to avoid.

## `per_unit_table` and durations a vendor can actually emit

A `per_unit_table` prices exact `(resolution, duration)` buckets. A duration the
table does not list is a **miss**, and a miss does not fall back to the per-second
formula — that would underbill. It rounds up to the **next bucket**: the row for that
resolution with the smallest duration that is still ≥ the observed one. Selection is
by duration, never by price — choosing the cheapest covering row would assume the
table is monotonic, and an operator who discounts long clips would have a short clip
billed below the bucket that neighbours it. Only when NOTHING covers the observation
does it fall to the table maximum.

That rounding-up rule exists because the previous behaviour — always the table
maximum, across every resolution — turns an untabulated duration into a charge for
the most expensive clip the operator ever priced. It is reachable whenever a vendor's
minimum shifts: MiniMax H3's floor moved 5 → 4, which is also its default request
shape, so the most common request became a miss overnight.

**Operator rule:** tabulate every duration the vendor can emit **for every resolution
it can emit**, starting at the vendor's minimum — not just the minimum. A conforming
OpenAI client sends `seconds` from {4, 8, 12}, and any value with no bucket at or
above it falls to the table maximum across every resolution. The resolution half
matters just as much and is easier to miss: a resolution with *no rows at all* has no
covering bucket however short the clip, so it takes that same table-max path — a
4-second clip at an untabulated size is billed as the longest 4K one in the table.
Otherwise clients are billed a price `GET /v1/models` does not advertise for their
request — it publishes one variant per configured bucket, so an untabulated
`(resolution, duration)` has no visible price at all.

Misses are metered by **`broker_video_table_miss_total{reason}`**, which is how an
operator finds out a row is missing without reading logs. `reason="next_bucket"` means
the observation was rounded up to a covering row; `reason="uncovered"` means nothing
covered it and the table maximum was charged — the expensive one, and the one to alert
on. This is deliberately NOT `broker_video_billing_skipped_total`, which means the
opposite: a video served *without being billed at all*. The log line names the
offending `(seconds, size)`, throttled to a few lines an hour per bucket.

## Signature lifecycle (`ZG-Res-Key`)

Billing is not the only thing an async job defers — so is the TEE signature, and the
two have the same shape: the create response is not the final answer.

`ZG-Res-Key` is issued with the create response and signed over the
`{"status":"queued"}` envelope, then **re-signed by the poller over the FINAL body**
under the same key. So the contract is:

> the key covers the job's final state, not the envelope the client first received.

That contract only holds if every path keeps it. Two rules follow:

1. **A response is only advertised as signed if it will actually be signed.** One
   predicate (`signs` in `handleVideoGenerationResponse`) drives the `ZG-Res-Key`
   header, the create-time signing call, and whether a `chatKey` is handed to the
   poll job — they cannot disagree. A centralized provider previously advertised the
   header while only the decentralized signer existed, so the key could only 404.

2. **The create-time signature is evicted whenever a final body the client can
   obtain exists but was never signed.** Timeout, provider-reported `failed`,
   completed-with-no-resolvable-duration, a linked request row that vanished, a
   failed re-sign, and two of the three no-poll-job exits (scheduler disabled,
   insert failed) — in all of these the vendor job id exists, and `GET /videos/{id}`
   proxies straight to the upstream regardless of any poll job, so the client can
   fetch a body the cached signature does not describe. Leaving it would hand the
   client a *valid* TEE signature whose response hash does not match what it
   fetched — indistinguishable from tampering, and worse than the 404 it gets
   instead.

   The exception is a create response with **no job id**: the client cannot build
   `GET /videos/{id}`, so no final body is obtainable and the cached signature still
   describes exactly the response it holds. Evicting there would break a lookup that
   was never in doubt. The rule is "an obtainable final body exists", not "the
   poller did not run".

   Note this is a client-visible change for providers that predate it: such a job's
   `ZG-Res-Key` used to stay resolvable (pointing at the queued envelope) and now
   404s.

Lost signatures are metered by `broker_routing_proof_skipped_total{reason}` for
centralized providers, except where a sibling counter already covers the outcome
(`VideoPollTimedOutTotal`, `VideoGenerationFailedTotal`, `VideoBillingSkippedTotal`)
— double-metering a routine vendor failure would put a permanent baseline under an
alert whose instruction is "any sustained rate is a problem".

## Job id contract (broker → consumers)

The `id` in the `POST /videos` response originates upstream, not in the broker: the
broker reads it back out of the upstream body (`videoRespFields.ID`) and never mints
one. Before this change the translator passed the vendor's `task_id` through verbatim,
which made the vendor's id-shaping decision a **published API contract** — because
consumers do not merely echo the id back, they persist it and key on it:

> **Guarantee:** the `id` returned by `POST /videos`, and accepted by
> `GET /videos/{id}` and `GET /videos/{id}/content`, is at most **36 characters**
> from `[A-Za-z0-9_-]`.
>
> A vendor whose `task_id` does not satisfy this is **mapped by the translator**.

**This is now enforced, not merely documented.** Shaping the id is protocol
translation, so it happens where protocol translation happens:

- `translate.EncodeJobID` (api/videotranslator) maps every vendor `task_id` into the
  contract on create, and `DecodeJobID` maps it back on every `GET /videos/{id}` and
  `/content`. The mapping is a self-describing tag plus payload, so it is reversible
  **without state** — the translator holds no cross-request state and must not start.
  `v0_` passes a compliant id through, `v1_` compacts a canonical UUID by dropping
  its hyphens (DashScope's ids are exactly 36 characters and would not otherwise
  survive the tag), `v2_` base64url-encodes anything else.
- A vendor id that no encoding can carry — a stateless reversible mapping into 33
  payload characters holds at most 24 arbitrary bytes — fails the create call
  loudly, naming the id. Note what that does NOT buy: encoding runs after the
  vendor's create call returned, so the vendor has already accepted the job and will
  bill for it, and the id survives only in the log. The win is a local, immediate,
  named failure — not a saved clip. Nor is it guaranteed to surface in staging: a
  vendor whose id shape varies by model, region, or API version can fail first in
  production.
- **An id with no tag is passed through** as a pre-tagging vendor id. The translator
  shipped before tagging existed, so ids already in flight carry none, and rejecting
  them would strand every such job (the poller treats the 4xx as retryable, so the
  job spins to `MaxPollDuration`, never bills, and drops the signature its client
  holds a key for). An id carrying a `vN_` tag this build does not know is a
  different thing and IS rejected — it came from a newer replica, and forwarding it
  would hand the vendor an id it never issued.
- Adding a tag is a **two-phase deploy**: ship the decode case everywhere first,
  enable it in the encoder only once every replica can decode it, and never remove
  or reuse a tag. A rollback otherwise strands every id the new tag issued.
- The broker asserts the contract independently (`isContractJobID`,
  inference/internal/ctrl/video.go). That path has no translator to rely on: it
  catches a vendor spoken to directly.

### Why a bound exists at all, and why it is this tight

The 0G Router (the main consumer today) stores the id as part of the primary key of
its `async_jobs` table (`varchar(36)`) and validates it at submit. But the binding
constraint is one step further downstream: the id is folded into the router's
billing idempotency key,

```
"async-" + <last 8 of provider address> + "-" + <job id>   →   usage_logs.request_id
```

and `usage_logs.request_id` is `varchar(64)` with a UNIQUE index — **the key that
makes async billing exactly-once, shared by video and image alike**. That leaves a
hard ceiling of 49 characters for the id, and raising it means rebuilding a unique
index on the largest, hottest table in that system. So "just widen the column" is
not available as a cheap escape hatch; 36 is the value in force today and 49 is the
absolute ceiling under the current key format.

### What happens if the guarantee is broken

Not a clean rejection. The router validates the id **after** the create call has
already succeeded, so an over-long id means:

- the vendor has generated (and charged us for) a clip,
- the router returns an error to the client and marks the provider failed —
  degrading routing for a provider that is actually healthy,
- and no `async_jobs` row is written, so nothing can ever bill or deliver that clip.

The failure is silent from the vendor's side and looks like a provider fault from
the router's. That asymmetry — cheap to guarantee here, expensive and misattributed
there — is why the bound belongs at this boundary.

### Enforcement options

1. **Validate at onboarding (cheap, recommended first).** Reject a provider config
   whose vendor issues non-conforming ids, turning a runtime, per-request failure
   into an explicit deployment-time one. Zero runtime cost.
2. **Mint and map (the full fix).** Have the broker issue its own short id and store
   the vendor `task_id` beside it — `video_job_owner` is already keyed by this id, so
   it is one extra column. This removes the ceiling entirely and makes the id shape
   the broker's own decision rather than each vendor's, which is what the "opaque
   internally, guaranteed externally" split above actually requires.

Note the current margin is thin, not comfortable: a DashScope UUID is exactly 36
characters, and an OpenAI-shaped `vid_`/`video_` prefix on a UUID or 32-byte hex
(40 and 38 characters) is already over. This is worth settling before the first
non-MiniMax video vendor is onboarded.

## Whitelisted (unbilled) traffic

Whitelisted requests bypass billing entirely and create no `Request` row (see `proxy.go`), but
the broker still needs to count them for reconciliation against a provider's vendor statement
(`hourly_usage_stat`, see `docs/design/provider-reconciliation.md`) — otherwise every whitelisted
hit on a genuinely async provider would be invisible, understating usage. A whitelisted request
against a non-terminal (`queued`/`in_progress`) create response gets a `VideoPollJob` row exactly
like a paying user's, with `IsWhitelisted=true`, and the poll scheduler resolves it the same way.

The one deliberate difference from the paying-user path is **when** the reconciliation row is
written. A paying user's `Request` row already exists (with an estimated fee) the moment create
returns, and completion simply updates it in place. `hourly_usage_stat` cannot be updated the same
way: it is a pre-aggregated rollup whose primary key includes `RateClass`, so an eager "unresolved"
write at create time followed by a "corrected" write at completion would mean moving a unit of
count from one aggregate row to another (decrement one, increment a different one) rather than
updating a value in place — the *destination* row is only known once the real `RateClass` is.

Whitelisted jobs sidestep this entirely by writing to `hourly_usage_stat` exactly once, only at
resolution time (`completed`/`failed`/`timed_out`), when the final `RateClass` is already known.
Nothing is written at create time; every terminal outcome — including every early-return failure
before a `VideoPollJob` even exists (no provider job id, a transient `CreateVideoPollJob` error) —
records a usage row (zero seconds on failure/timeout, the real resolved seconds on completion), so
a whitelisted request that hit the upstream is never simply invisible to reconciliation, and never
needs correcting after the fact.

## Configuration

New `VideoPoll` config block, mirroring `AsyncConfig` (`config.go:861-879`):

| Field | Meaning | Suggested default |
|-------|---------|--------------------|
| `MaxConcurrentPolls` | Worker pool size for the poll scheduler | 10 |
| `PollInterval` | Fixed delay between poll attempts for a given job | 10s |
| `MaxPollDuration` | Ceiling from job creation to forced `timed_out` | 20 minutes (comfortably above the ~1-5 minute generation time HappyHorse/DashScope-style vendors report; tune per-provider if durations vary widely) |
| `ScanInterval` | How often the scheduler queries for due rows | 5s |

## Phased rollout

- **Phase 1 — the poller + create-time branching.** Add `VideoPollJob`, split
  `handleVideoGenerationResponse` at the terminal/non-terminal check, add the scheduler and its
  config. Fee reservation reuses the existing `ValidateRequestWithEstimatedFee` check as a
  pre-create gate (no hold/release semantics yet — under-provisioning risk is bounded by
  `MaxPollDuration` and the fact that a request already exists once a job is created, so a
  balance draining to zero mid-poll surfaces as a normal insufficient-balance case at the next
  unrelated request, not as a silent loss on this one).
- **Phase 2 — real fund holding.** If Phase 1's re-validate-only approach proves insufficient
  (e.g. observed abuse of the gap between create-time validation and completion-time charge),
  add an explicit reservation that decrements available balance at create and releases/settles
  it at completion, rather than only checking balance was sufficient at a point in time.

## Open questions

- **Fund reservation mechanics.** Phase 1 assumes re-validating balance at create time is
  sufficient. Whether a true hold/release is needed depends on how much balance a single user
  can tie up across concurrently-pending video jobs, which needs real usage data to size.
- **Per-provider `MaxPollDuration` / `PollInterval` overrides.** A single global default may not
  fit every vendor (HappyHorse's ~1-5 minutes vs. a hypothetical vendor with much longer render
  times). Whether this belongs in `config.Service` per-model config or stays a single global
  knob is deferred until a second real async video vendor is onboarded.
- **Alerting on `timed_out` jobs.** A `timed_out` row is a genuine reconciliation gap candidate
  (the `provider-reconciliation.md` ① ↔ ② edge should catch it eventually, but a synchronous
  alert would surface it faster). Left for when Prometheus metrics for this path are wired up.
