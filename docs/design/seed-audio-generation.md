# Seed Audio Generation Design

This document describes how the broker should serve and bill **audio generation** — a new
modality whose first vendor is ByteDance **Seed Audio 1.0** (BytePlus Voice). It covers the
client-facing contract, the translator sidecar, the billing shape, the pre-forward reservation,
and the poll-to-completion lifecycle.

It is the audio counterpart of [video-generation-async-billing.md](video-generation-async-billing.md),
and deliberately reuses that design's machinery rather than inventing a parallel one. Read that
document first: everything here about polling, lease-based crash recovery, the `ZG-Res-Key`
signature lifecycle and the job-id contract is *the same mechanism*, and this document only
records where audio differs.

## Why a new modality rather than an existing one

The broker already serves `speech-to-text`. That is the opposite direction and shares nothing
useful: STT consumes audio and bills the INPUT dimension (seconds of uploaded audio, or tokens
for `gpt-4o-transcribe`), while audio generation produces audio and bills the OUTPUT dimension.
`fanOutPrices` in the router (`pkg/contract/provider_lister.go`) sorts service types into exactly
these two buckets and refuses a model that does not fit one — so audio generation needs its own
entry, not a reinterpretation of STT's.

The name is `audio-generation`, not `text-to-speech`. Seed Audio 1.0 generates multi-speaker
dialogue, background music, ambience and foley-style sound effects in a single pass. A model
that produces a scene is not doing text-to-speech, and naming it that way would mislead every
consumer that branches on the service type — including the router's catalog, which renders a
human label per modality.

## The vendor, and the one thing that is NOT shared with Seedance

Seed Audio is a ByteDance model, as Seedance is, and both are reached through BytePlus. It is
tempting to conclude the existing `videotranslator/internal/seedance` client is a starting
point. It is **not**, and the distinction is load-bearing:

| | Seedance 2.5 (video) | Seed Audio 1.0 |
|---|---|---|
| Platform | Ark / ModelArk | BytePlus Voice (openspeech) |
| Host | `ark.cn-beijing.volces.com` | `openspeech.byteoversea.com` |
| Path shape | `/api/v3/contents/generations/tasks` | `/api/v1/...` (voice platform) |
| Auth | `Authorization: Bearer <key>` | app-key / access-key / resource-id headers |

Volcengine's own `ark-cli` confirms the split rather than merely implying it: it hard-excludes
voice models (`doubao-seed-tts-*`, `doubao-seed-asr-*`, `seedasr-*`) from *every* Ark pipeline —
deploy, chat, gen, code-example, usage, pricing — on the explicit grounds that appearing in the
model catalog does not make a voice model callable through Ark. Its pricing skill refuses to
query `--modality Audio` at all.

So the Seedance integration is a **structural** template (create task → poll to terminal →
download asset) and the `translate`/`handler`/`jobid` layers port over almost unchanged. The
vendor client underneath does not: different host, different path constants, and — the one that
changes a function signature — **multi-header auth instead of a single `Authorization` string**.

### Auth plumbing

The broker side already supports this and needs no change: `additionalSecret` is a
`map[string]string` of outbound header name → value, injected at `ctrl/proxy.go` on the
synchronous path and at `ctrl/video_poll.go` on the poller's path. An operator configures the
three openspeech headers there exactly as they configure `Authorization` today.

The **adaptor** side does need a change. `seedance.Client.CreateTask(ctx, authHeader string, …)`
takes one header value, and `handler/video_seedance.go` fills it from
`c.GetHeader("Authorization")`. The audio client must take a header **set** and forward every
configured credential header. A single-string signature here would silently drop two of the
three headers and fail every call with an auth error that looks like a misconfigured key.

## Client-facing contract

### The path is `/v1/audio/generations`, not `/v1/audio/speech`

OpenAI's `POST /v1/audio/speech` promises **raw audio bytes, synchronously**. A client built
against that contract — including every official OpenAI SDK — reads the response body as audio.
Returning `{"id":"…","status":"queued"}` from that path does not fail loudly; it produces a
corrupt audio file, at the client, with no error anywhere in the chain.

`/v1/audio/generations` avoids that collision, parallels the existing `/v1/images/generations`,
and leaves `/v1/audio/speech` free for a future synchronous TTS model that can actually honour
it. It is also the honest name for what Seed Audio does (see above).

The *body*, which is the part that matters for client portability, stays OpenAI-standard.

### Request

```jsonc
POST /v1/audio/generations
{
  // OpenAI /v1/audio/speech field names, kept verbatim so a client already
  // speaking OpenAI TTS needs no field renaming:
  "model": "seed-audio-1.0",
  "input": "<the script>",
  "voice": "<preset voice id or cloned voice id>",
  "response_format": "mp3" | "wav" | "pcm" | "opus",
  "speed": 1.0,

  // Seed Audio capabilities with no OpenAI counterpart. Additive and optional:
  // a request that omits all of them is a plain OpenAI TTS request.
  "max_duration": 120,          // seconds, vendor ceiling 120
  "sample_rate": 24000,         // 8000..48000
  "reference_audio": [ … ],     // up to 3 clips, each < 30s — see below
  "reference_image": …,         // 1 image; mutually exclusive with reference_audio
  "pitch": 1.0,
  "loudness": 1.0
}
```

`reference_audio` and `reference_image` are mutually exclusive at the vendor. The translator
rejects a request carrying both **before** calling the vendor, mirroring
`translate.ValidateSeedanceCreateRequest`'s pre-flight rejection of an unsupported
`input_reference` — a local, named failure costs nothing, and a vendor 400 arrives only after
the request has been routed and a slot reserved.

### How reference media reaches us

This needs its own contract rather than "a URL", because **OpenAI has no reference-audio
concept to conform to**: `/v1/audio/speech` takes `voice` as a preset name string and has no
voice-cloning input at all. Whatever we accept is an extension, so the question is which
extension is closest to how OpenAI hands audio around elsewhere.

It does that two ways, and neither is a URL:

- `/v1/audio/transcriptions` takes a multipart **file**.
- chat completions takes inline base64 — `{"type":"input_audio","input_audio":{"data":"…","format":"wav"}}` —
  with an explicit `format` and, unlike `image_url`, **no URL variant**. OpenAI never accepts
  audio by URL anywhere in its API.

So a bare URL is the *least* OpenAI-shaped option here, which is the opposite of the
conclusion an analogy with Seedance's `image_url` would suggest.

The design is therefore structurally what `parseCreateVideoRequest` already does for video —
an OpenAI-native multipart path plus a JSON convenience path — with audio's own shapes:

| Transport | Shape | Standard |
|---|---|---|
| `multipart/form-data` | `reference_audio` file parts | yes — matches `/v1/audio/transcriptions` and the Video API's `input_reference` |
| JSON | `input_audio: {data, format}` objects | yes — OpenAI's own inline-audio shape |
| JSON | a plain `https://…` string | **extension**, documented as such |

The multipart path is what a client with a 2 MB voice sample uses, and is what OpenAI clients
already do for audio; the URL path exists for a client that already hosts the file and wants
to keep the body small. Internally the adaptor normalizes all three into whatever the vendor
takes, exactly as the video handler converts a multipart file part into a `data:` URI so the
vendor mapping stays transport-agnostic.

Two rules carry over from the existing vendors, and one deliberately does not.

**Carried over — the scheme allowlist.** Both Seedance and MiniMax accept only `https://`,
`http://` and a matching `data:` prefix, verbatim. Audio uses the same list with `data:audio/`.

**Carried over — a vendor file handle is never client-addressable.** MiniMax rejects
`mm_file://` appearing in the `image_url` field because "that account is single-tenant
upstream but multi-tenant for us — accepting a client-chosen `mm_file` id in `image_url`
would let one user reference another's uploaded frame." If Seed Audio has an upload-a-sample
endpoint, the same rule applies without modification: a handle may only arrive through a
dedicated field we prefix ourselves, never through a free-form reference string.

**NOT carried over — silent degradation.** Seedance drops an unusable reference and falls back
to text-to-video; MiniMax does the same. That is safe there because the result is visibly
different and the prompt still drove it. It is not safe here: dropping one of three
`@AudioN` references yields a full-length, fully-billed generation **in the wrong voice** —
which is precisely the harm Seedance's own `asset://` / `file_id` 400 path exists to prevent
("so a client doesn't get silently billed for a different video than they asked for"). Every
unusable `reference_audio` entry is therefore a 400, not a degrade.

The remaining shape difference is that `reference_audio` is a **list of at most 3**, where
both existing vendors take a scalar `input_reference`. Validation is list-shaped: length,
per-entry scheme, and the `reference_audio` XOR `reference_image` rule.

### Response

```jsonc
// POST → immediately, non-terminal
{ "id": "…", "status": "queued", "model": "seed-audio-1.0", "created_at": 1757... }

// GET /v1/audio/generations/{id}  → once terminal
{ "id": "…", "status": "completed", "model": "…",
  "audio": { "duration_seconds": 47.2, "format": "mp3", "sample_rate": 24000 },
  "usage": { "output_audio_seconds": 48 } }

// GET /v1/audio/generations/{id}/content → the audio bytes
```

`usage.output_audio_seconds` is the billable quantity, ceil-rounded from the real duration. It
is reported in the STATUS response rather than only in the content response for the same reason
video reports it in the status body: the broker's poller sees the status response, and making
billing depend on a body the poller never fetches would mean downloading every generated asset
just to bill it.

### Routing table entries

- `/v1/audio/generations` joins `constant.TargetRoute` — this is what makes the route billable.
- `/v1/audio/generations/` joins `constant.AuthRequiredPrefixes` — status and content are
  authenticated but **unbilled** passthrough, exactly as `/videos/` is. The create call is the
  only billable one; a client polling its own job must not be charged per poll.

### E2EE: this modality is not sealable

`ctrl/e2ee.go`'s `profileForRequest` maps (service type, API surface) onto a wire profile and
returns `sealable=false` for anything with no profile of its own. Its comment already anticipates
this case by name — *"video-generation, and whatever service type is added next"* — and spells
out why a default arm must not guess `ProfileChat`: it would apply chat's sealing rules to a
request shape nobody analyzed.

`audio-generation` therefore takes the `sealable=false` path, deliberately and not by oversight.
This matches the async image endpoints, which are likewise unsealable because a job that is
enqueued, forwarded later and served from a store has no point at which a response could be
sealed to the client's ephemeral key.

## Why async, and why the job lives at the vendor

Seed Audio generates up to 120 seconds of audio and takes roughly 10–30 seconds to do it. The
BytePlus voice platform exposes this as submit-then-poll; the same is true of its audio-file
recognition APIs. Holding a synchronous connection open across router → broker → translator →
vendor for that long is hostile to every timeout in the chain.

Given async, there are two shapes already in this codebase:

**A. Vendor-owned job + broker poller** — the `/videos` pattern. The translator creates a vendor
task and returns its id; the broker records a poll job and polls to terminal state; billing
happens on the result.

**B. Broker-owned queue** — the `/v1/async/images/*` pattern. The broker enqueues the request and
a worker goroutine holds the whole downstream call.

**A is the right shape here, and the reason is crash recovery.** B's recovery path
(`MarkProcessingAsyncJobsAsFailed`) marks in-flight jobs *failed* on restart — correct for its
own case, because a single in-flight `POST` cannot be safely retried without risking a duplicate
provider call. But under B the vendor has *already accepted and will bill for* the generation,
so failing the job discards revenue for work we are paying for. A's recovery is a lease timeout
on an idempotent `GET`: a restart resumes polling, which is the behaviour
video-generation-async-billing.md's §3 argues for at length. A also holds no goroutine for the
render, and reuses `ctrl/video_poll.go` and `db/video_poll_job.go` nearly wholesale, so it is
less new code than it first appears.

## Billing

### Mode and unit

A new `BillingModePerAudioSecond` joins the `BillingMode` enum in `config/model_pricing.go`.
`BillingObservables` gains an `AudioSeconds` field and `OutputUnits` gains the corresponding
case. The fee is the same shape every non-token modality already uses: `OutputUnits ×
OutputPrice`.

The reconciliation unit is the **existing** `constant.BillingUnitSeconds`. No new unit is
introduced: whisper STT and video generation already record seconds, and audio's seconds mean
the same thing to a reconciliation query. `DefaultBillingUnitForService` gains an
`audio-generation` case returning it.

### No published unit vocabulary either

An earlier draft of this design gave the router a `UnitAudioSecond = "audio_second"`
alongside its `video_second` / `video_clip` / `video_token`. It does not get one, and the
reason follows directly from the section below: that vocabulary types `ModelPriceVariant.Unit`,
i.e. rows in a `variants` table, and a modality with no tier axis publishes no variants. The
constant would be read by nothing. Billing computes `pricing.audio × seconds` directly, the
same way the router's no-variants fallback already prices a flat video model.

The broker likewise publishes no `audio_unit` beside `pricing.audio`, unlike `video_unit`.
`video_unit` exists because video's flat scalar is genuinely ambiguous — it can price a second,
a table unit, or a completion token — and `videoPriceUnit`'s own doc argues against naming a
unit the consumer already assumes correctly. Audio has one mode and one unit.

Both become wrong the moment a second audio billing shape appears. That, not a future vendor,
is the trigger to revisit.

### No tier axis, deliberately

Seed Audio publishes one flat per-second rate (~$0.0025/second direct; $0.002–$0.00325 across
resellers). There is no resolution analogue, and sample rate and output format do not move the
vendor's price.

So `BillingConfig` gains **no** new tier table for this mode. This follows the reasoning already
recorded on `VideoTokenPriceTier`: a configurable price for a cell nothing can reach gets
published in `GET /v1/models` and never charged, and a consumer quoting it quotes a price the
broker does not honour. If a real tiered rate card ever appears, the axis is added then — in the
config, in `audiospec`, and in the router's variant resolver together, which is the same
three-places rule that doc states.

`Request.RateClass` is therefore empty for audio, exactly as it is for whisper seconds and
per-image billing.

### The reservation is a true bound, not an estimate

A new `common/audiospec` package is the sibling of `common/videospec`, and exists for the same
reason: the translator and the broker must not derive independently what the vendor will
produce, because a create is billed asynchronously and a gate that guesses low has already let
the audio be generated by the time the real bill arrives.

Audio's version of that answer is stronger than video's. The reserve is:

```
reserve = min(requested max_duration, 120) × per-second price
```

The vendor's hard 120-second ceiling makes this an actual **upper bound** on what can be billed,
not an estimate. Compare `videospec.TokenEstimator`, whose doc is explicit that it returns an
estimate the gate tolerates drift on, and which needs `ctrl.WarnVideoTokenEstimateDrift` watching
it. Audio needs no such drift warning: the bill cannot exceed the hold. A request that omits
`max_duration` reserves the full 120-second ceiling.

This matters beyond correctness of a single request. Because the hold is a true bound,
concurrent creates from one wallet see each other exactly, rather than approximately — which is
the property the minimum-locked-balance floor exists to paper over when it does not hold.

### Where the billable quantity comes from

Note the framing: the **vendor's own reported billable quantity**, not "the duration we
measured". This follows Seedance, whose `usage.completion_tokens` is authoritative precisely
because it already bakes in both the output and any billable *input* reference media (a
reference video, there). If Seed Audio likewise charges for the reference clips a cloning
request supplies, its reported quantity includes them and passing it through keeps our charge
equal to the invoice. Deriving our own output-only number would silently under-bill exactly
the requests that use the feature the modality exists for.

That also bounds how wrong the reservation can be. The reserve is output-only, so a vendor
that bills reference input can exceed it — see [Open questions](#open-questions). Billing on
the vendor's number means the *charge* is still right; it is only the *hold* that would need
widening.

Resolution order, and every step is a real possibility rather than defensive padding:

1. **The vendor's reported billable quantity** (seconds, or whatever unit it reports).
   Expected to be the normal path: the vendor bills per second of output itself, so it knows
   the number and has reason to report it. Authoritative when present, even if it exceeds the
   output duration we would have measured.
2. **Derived from the returned container.** For `wav` and `pcm` this is exact arithmetic from
   byte count, sample rate, channel count and sample width — no decoder. For `mp3` and `opus` it
   needs a small header walk (frame count; granule position) — still not a decoder, but real
   code with real edge cases.
   **`pcm` is the exception to step 2 and must not take it.** Raw PCM has no container at
   all — the bytes are the samples — so the same 480,000 bytes is 10.0s at 24kHz/16-bit/mono,
   5.0s at 24kHz/16-bit/stereo, or 6.7s at 24kHz/24-bit/mono. The request carries
   `sample_rate`, but nothing carries bit depth or channel count, so deriving a duration means
   assuming vendor defaults. That makes `pcm` the ONLY format where this fallback can be
   confidently WRONG rather than failing: a malformed mp3 fails to parse and is noticed, while
   a wrong channel assumption bills exactly 2x with no error and a perfectly plausible number
   — on a money path, and by enough to exceed the hold. So `pcm` with no vendor-reported
   duration skips step 2 entirely and goes straight to step 3.

3. **The reserved ceiling.** Over-bills by construction, so it is a last resort and must be
   loudly metered rather than logged at `Warn`, in the same spirit as
   `broker_video_billing_skipped_total`.

Modelling all three rather than assuming (1) follows `seedance/types.go`, which carries
`Frames`/`FramesPerSecond` defensively beside `Duration` precisely because a vendor's response
shape is not ours to promise.

## Lifecycle

Identical in structure to video's, so this section records only what is audio-specific. For the
claim/lease mechanics, the terminal/non-terminal create-time branch, the whitelisted-traffic
rule and the `ZG-Res-Key` re-signing contract, see
[video-generation-async-billing.md](video-generation-async-billing.md).

### `AudioPollJob`

A new table mirroring `VideoPollJob` field for field, with `RequestedSeconds` carrying the
reserved ceiling rather than a requested clip length.

It is a **separate table**, not a `modality` column on `VideoPollJob`. The two have different
poll cadences and different timeout ceilings (below), the scan query is the hot path, and
widening a table whose every row is claimed by an atomic `UPDATE … WHERE status='pending' AND
next_poll_at<=now()` puts audio and video jobs in contention for the same rows for no benefit.

### Poll cadence must not inherit video's

Video's defaults are `PollInterval` 10s and `MaxPollDuration` 20 minutes, sized against a 1–5
minute render. Audio renders in 10–30 seconds. Reusing video's numbers would:

- poll roughly three times for a job that is usually done on the first check, and
- hold a reservation for **forty times** the job's actual lifetime in the timeout case.

`AudioPoll` therefore gets its own config block. Suggested: `PollInterval` 3s,
`MaxPollDuration` 5 minutes, `ScanInterval` 2s, `MaxConcurrentPolls` 10.

### Job-id contract

Unchanged and non-negotiable: the id returned by create, and accepted by the status and content
routes, is at most **36 characters** from `[A-Za-z0-9_-]`. The binding constraint is downstream —
the router folds it into `usage_logs.request_id` (`varchar(64)`, UNIQUE), the key that makes
async billing exactly-once.

`translate.EncodeJobID` / `DecodeJobID` port over unchanged. They already absorb an arbitrary
vendor id shape through the `v0_`/`v1_`/`v2_` tags and fail loudly, naming the id, on one that no
encoding can carry. Openspeech's task-id shape is not yet confirmed, and that is fine — this is
exactly the uncertainty that machinery exists to absorb.

## Components and where they live

```
0g-serving-broker/
  api/common/audiospec/              NEW — vendor rules; sibling of videospec
  api/audiotranslator/               NEW — the adaptor sidecar
    cmd/server/seedaudio.go
    internal/seedaudio/{client,types}.go
    internal/translate/{translate,seedaudio,jobid}.go
    internal/handler/audio_seedaudio.go
  api/inference/
    const/const.go                   service type, TargetRoute, AuthRequiredPrefixes, billing unit
    config/model_pricing.go          BillingModePerAudioSecond, BillingObservables, OutputUnits
    config/config.go                 AudioPoll config block
    internal/ctrl/audio.go           NEW — create-time branch + billing
    internal/ctrl/audio_poll.go      NEW — poll scheduler
    internal/ctrl/audio_reserve.go   NEW — pre-forward hold
    internal/ctrl/proxy.go           dispatch case
    internal/db/audio_poll_job.go    NEW
    internal/handler/models.go       publish the audio price + variants

0g-router/backend/
  pkg/contract/provider_lister.go    ModelInfoPricing.Audio (ingestion), fanOutPrices arm
  pkg/inference/handler.go           ServiceTypeAudioGeneration
  pkg/inference/audio_handler.go     NEW
  pkg/inference/async.go             audio branch in the async biller
  pkg/modelregistry/registry.go      validServiceTypes
  pkg/contract/provider_lister.go    fanOutPrices output-only branch
  pkg/metrics/{metrics,collector}.go per-modality unit + counters
  internal/router/router.go          routes
  internal/handler/async_inference.go  submit / poll / content
  internal/model/models.go           AsyncJob service type
  frontend/src/pages/*.tsx           modality labels
```

The translator sidecar is a **separate module from `videotranslator`**, not a fourth vendor
inside it. `videotranslator`'s three vendors all serve the OpenAI *Video* API shape and share
`translate.CreateVideoRequest` / `VideoResponse`; an audio vendor shares neither the request
type, the response type, nor the routes. Adding it there would mean a package whose name is
wrong and whose shared types apply to half its contents.

## `fanOutPrices` — the failure this must avoid

The router admits a fanned-out model only if it is priced, and the generic rule requires a
strictly positive prompt **and** completion price. `pkg/contract/provider_lister.go` already
documents two shapes where that rule is wrong, and audio is a third.

Audio generation is **output-only**: its input price is 0 (nothing is charged for the script)
and its whole per-unit cost lives on the output side. Without an explicit branch, an audio
provider is rejected at admission, logged as `invalid_price` — and the operator-facing symptom
is not "misconfigured price" but "the modality is broken": the model is neither listed nor
routable, with a log line blaming the wrong thing. That is precisely the failure that function's
doc records having already happened twice, for embedding and for video.

Audio is the mirror of the embedding/STT branch rather than a member of it: those are input-only,
audio is output-only. It needs its own arm, gating admission on the **output** price alone.

## Metrics

`router_usage_quantity_by_modality_cumulative` deliberately omits video-generation because video
logs 0 tokens and would contribute an empty series. Audio has the same shape and needs the same
decision made explicitly rather than by omission — either it reports seconds into that counter,
or it gets a sibling counter the way video got `router_video_billed_seconds_total`.

The `unit` label must distinguish audio's seconds from STT's. Both are seconds, but they measure
opposite dimensions — input consumed vs output produced — and summing them is meaningless.

## Phasing

1. **Pricing primitives.** `common/audiospec`, `BillingModePerAudioSecond`, config validation,
   `GET /v1/models` publishing, router ingestion + `fanOutPrices` arm. Pure, unit-testable,
   wired to nothing — the same "engine first, request paths later" order the multimodal billing
   work already used.
2. **The adaptor**, against a recorded test double.
3. **Broker request path**: route, reserve, create-time terminal/non-terminal branch,
   `AudioPollJob` + scheduler.
4. **Router request path**: submit / poll / content, async billing, usage recording.
5. **Frontend and metrics.**

## Open questions

- **The vendor wire schema is not yet confirmed.** Field names and the task-status enum are taken
  from BytePlus platform conventions and third-party listings, not the official reference — the
  model is enterprise-gated. Phase 1 depends on none of it. Phase 2 needs it to write the client,
  but it is ordinary integration work once the reference is in hand, not a design question.

  Two things it is worth being precise about, because an earlier draft of this section overstated
  both as blockers:

  **Duration is never actually missing.** The adaptor is ours and defines the response envelope,
  so it can always emit `usage.output_audio_seconds` — if the vendor does not report a duration,
  the adaptor measures one. The open question is only how cheaply. A vendor-reported duration is
  free; otherwise the adaptor must fetch the asset on the TERMINAL status call and read it from
  the container, which is exact arithmetic for `wav`/`pcm` from a 44-byte header, a Xing/Info
  frame or frame walk for `mp3`, and — the awkward one — a range request to the **tail** for
  `ogg`/`opus`, whose granule position lives on the last page. So the answer decides whether the
  adaptor needs an asset fetch in the status path at all, not whether billing can work.

  **The task-id shape is very unlikely to be a problem.** `EncodeJobID` already absorbs a vendor
  id that is ≤33 characters of `[A-Za-z0-9_-]` (passthrough), a canonical UUID (hyphens dropped),
  or anything else up to 24 bytes (base64url). Only an id that is simultaneously longer than 24
  bytes, not a UUID, and outside the contract charset fails — and it fails loudly at that vendor's
  first request with the id named. This was flagged at all only because Seed Audio is on
  openspeech rather than Ark, so its ids are issued by a different service than Seedance's and
  cannot simply be assumed to match. It is a one-line check on the first live call, not a gate.
- **Whether the reserve needs to cover reference-media INPUT.** The hold is output-only
  (`min(max_duration, 120) × price`). Seedance's vendor charges for a reference video, and Seed
  Audio may likewise charge for the up-to-three reference clips a cloning request supplies — in
  which case the vendor's billable quantity exceeds the hold. Note this does NOT make the charge
  wrong: billing is on the vendor's reported quantity (see above), so only the gate under-holds.
  Confirmed against the real rate card, this is either nothing or a `+ 3 × 30s` term in the
  reserve.
- **Whether `speed`, `pitch` and `loudness` change the billed duration.** If the vendor applies
  `speed` before generating, the ceiling still bounds the output and nothing changes. If it
  applies it as post-processing, a `speed < 1` request stretches output past a ceiling computed
  from `max_duration`. Same shape as the question above and the same consequence — the charge
  stays correct, the hold does not.

### Settled, recorded so they are not reopened
- **`supported_parameters` extends to cover audio.** `GET /v1/models` advertises a
  parameter vocabulary built for chat, which has no `voice`, `reference_audio`, `sample_rate`
  or `loudness`. Audio models must not advertise an empty or chat-shaped set — a client
  discovers what it can send from this field. Extending the vocabulary is Phase 4 work, but
  the decision is made: extend it, do not leave audio models under-describing themselves.
- **No streaming, accepted for now.** OpenAI TTS offers `stream_format: "sse"` so a client can
  start playing before generation finishes; this design is async-only, so a caller waits the
  full 10-30s before hearing anything. Fine for voiceover and batch work, wrong for anything
  interactive. Accepted as a known limitation rather than designed around — a synchronous
  streaming path would live at `/v1/audio/speech`, which this design deliberately left free
  for exactly that.

- **Voice-cloning consent.** Raised and explicitly set aside for now. There is no consent,
  attestation or provenance mechanism for a voice reference anywhere in the broker or router, and
  this design adds none.
- **Asset expiry.** Vendor asset URLs are time-limited (~24h) and `/content` re-fetches from the
  vendor, so a client that comes back after expiry gets nothing. Accepted as-is — same property
  video already has. A broker-side asset store and cache is a later optimization, not a
  prerequisite.
- **E2EE.** `sealable=false`, inherited from the video/async precedent, accepted deliberately.
- **The over-hold on short scripts.** A request with no `max_duration` holds the full 120s even
  when it generates four seconds of speech. Accepted: Seed Audio's ceiling is the analogue of
  Seedance's [4, 30] clamp, and a script-length heuristic is not safe for a model that also
  generates music and ambience, where a three-word prompt can produce two minutes of output.
- **Whether a second audio vendor is coming.** `audiospec` is built as a registry from the start
  because `videospec` had to become one, but the per-vendor split only pays off with a second
  vendor. It costs little to keep.
