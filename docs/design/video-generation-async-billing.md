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
`async.go:188` already uses for the text-to-image async queue) and persists a `Request` row with
that estimated fee. Completion — whether immediate or via the poller — recomputes the fee from
the actual delivered duration and overwrites it, exactly as `UpdateRequestFeesAndCount` does
today. If actual and requested diverge, the corrected fee is what settles; nothing settles before
the actual duration is known.

The reserve is `EstimateVideoCreateFee` (`video.go`): `videoCreateReserveUnits × outputPrice`,
where the units are the requested `seconds` weighted by the **service-level** size ratio
(`GetVideoSizeRatio`, baseline 1.0 for an unrecognized size) and floored at 1. Two properties are
deliberate and worth not "fixing" by accident:

- **It does not reuse `videoOutputUnits`**, which is what settlement bills on. The two read `size`
  from different vocabularies: settlement sees the response's rendered resolution tier (`"2K"`),
  which is what a `per_unit_table` list is keyed on, while a create request carries the client's
  free-text size — pixel dimensions (`"1280x720"`) for an OpenAI-conforming caller. Routing the
  request value through the bucket lookup misses every row, and `videoOutputUnits` answers a miss
  with the table **maximum**; as a reserve that rejects callers who can afford the real bill, and
  it would double the `per_unit_table_miss` operator signal that settlement already emits for the
  same request.
- **It prices off `GetCachedService`, not `GetBillingPrices`.** `CtxKeyResolvedModel` is set by
  `PrepareHTTPRequest`, which runs *after* the balance check, so per-model pricing is not
  available at gate time and asking for it would log a spurious "resolvedModel missing" ERROR on
  every video create. The service price is the configured ceiling over all models (USD-denominated
  services get the live max wei price overlaid), so the reserve stays at or above the per-model fee
  for these units.

Known residuals, both bounded and both strictly smaller than the "reserve nothing" they replace:
a model whose per-model block prices a tier above the service baseline (a `per_video_second`
multiplier > 1, or a `per_unit_table` bucket) reserves below its eventual bill by that tier factor;
and a create that names no usable `seconds` reserves the 1-unit floor while the upstream applies
its own default duration (H3's is 4s) and bills that. Closing either needs the rendered tier /
applied duration at request time, which only the upstream knows — the fix is the create response
echoing them, not the broker guessing.

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
