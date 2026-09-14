# Audio Generation: Request and Response Shapes

What 0G Compute's request/response contract looked like before audio generation,
what audio adds, and — the part with no precedent — where its billable quantity
lives when the response body cannot carry one.

Companion to [seed-audio-generation.md](seed-audio-generation.md), which covers the
billing design. This document is only about wire shapes.

## The rule audio breaks

Every modality the broker served before audio answers with **JSON**, and two things
ride in that JSON:

- a `usage` block (or a countable array) carrying the billable quantity, and
- `x_0g_trace`, which the 0G Router injects into the same body to report cost and
  TEE verification.

Audio generation answers with **raw audio bytes**. OpenAI's `/v1/audio/speech`
returns the audio as the body — an SDK writes it straight to a file — so anything
added to that body corrupts it. Neither `usage` nor `x_0g_trace` can exist.

That is the whole of what is new. Everything else about audio follows the
established shapes.

## What was already there

| Modality | Endpoint | Response body | Billable quantity |
|---|---|---|---|
| chatbot | `POST /chat/completions` | JSON | `usage.prompt_tokens` / `completion_tokens` |
| chatbot (Anthropic) | `POST /messages` | JSON | `usage.input_tokens` / `output_tokens` |
| embedding | `POST /embeddings` | JSON | `usage.prompt_tokens` (input only) |
| speech-to-text | `POST /audio/transcriptions` | JSON | `usage` — seconds (whisper) or tokens (gpt-4o-transcribe) |
| text-to-image | `POST /images/generations` | JSON | `data[]` length |
| image-editing | `POST /images/edits` | JSON | `data[]` length |
| video-generation | `POST /videos` → poll → `/content` | JSON, then bytes | `usage.output_video_duration`, or Seedance's `usage.completion_tokens` |

A representative one — chat:

```jsonc
{
  "id": "chatcmpl-...", "object": "chat.completion",
  "choices": [ /* ... */ ],
  "usage": { "prompt_tokens": 12, "completion_tokens": 40, "total_tokens": 52 },
  "x_0g_trace": {                       // injected by the router
    "request_id": "req_abc123",
    "provider": "0x1234...",
    "billing": { "input_cost": "...", "output_cost": "...", "total_cost": "...", "currency": "0g" },
    "tee_verified": true
  }
}
```

Note video already has a half-exception: `GET /videos/{id}/content` streams bytes
and carries no trace either. Audio is the first modality where the **primary**
response is bytes.

## What audio adds

### Request — OpenAI-standard, with additive extensions

```jsonc
POST /v1/audio/speech
Content-Type: application/json

{
  // OpenAI /v1/audio/speech field names, unchanged, so a client already speaking
  // OpenAI TTS needs no renaming:
  "model": "seed-audio-1.0",
  "input": "<the script>",
  "voice": "<preset or cloned voice id>",
  "response_format": "mp3" | "wav" | "pcm" | "opus",
  "speed": 1.0,

  // Additive. A request omitting all of these is a plain OpenAI TTS request.
  "max_duration": 120,          // seconds; vendor ceiling is 120
  "sample_rate": 24000,
  "reference_audio": [ … ],     // up to 3 clips — voice cloning
  "reference_image": …,         // 1 image; mutually exclusive with reference_audio
  "pitch": 1.0,
  "loudness": 1.0
}
```

`multipart/form-data` is also accepted, which is how a client supplies reference
audio as file parts — the OpenAI-native shape for audio input (`/v1/audio/
transcriptions` takes a multipart `file`).

### Response — bytes, with the quantity in headers

```
HTTP/1.1 200 OK
Content-Type: audio/mpeg
X-0G-Audio-Duration-Seconds: 47.2     ← billable quantity
X-0G-Fee: 47200000                    ← what was charged, in neuron
Provider: 0x1234...
Access-Control-Expose-Headers: Provider, content-encoding, ZG-Res-Key, X-0G-Audio-Duration-Seconds, X-0G-Fee

<raw audio bytes>
```

**There is no `usage` key, and there cannot be one.** The two headers carry what
`usage` and `x_0g_trace.billing.total_cost` carry everywhere else.

They are in `Access-Control-Expose-Headers` because otherwise a browser client
receives them and still cannot read them — which for this modality means no
quantity and no cost at all.

## Where the numbers come from

The broker never speaks the vendor's protocol; the adaptor translates. Reading
right to left:

```
BytePlus                          adaptor                        broker → client
────────                          ───────                        ───────────────
POST /api/v3/tts/create           POST /v1/audio/speech          POST /v1/audio/speech
  X-Api-Key                         (OpenAI body)                  (OpenAI body)
  X-Api-Request-Id

{ "code": 0,                      base64-decode `audio`          <raw bytes>
  "audio": "<base64>",        →   → raw bytes              →     X-0G-Audio-Duration-Seconds
  "url": "…",  // 2h expiry                                      X-0G-Fee
  "duration": 47.2,               original_duration
  "original_duration": 48.0,  →   → X-0G-Audio-Duration-Seconds
  "subtitle": {…} }
```

### Why `original_duration` and not `duration`

The vendor returns both, and its reference is explicit twice over:

> `original_duration` — *"This is also the duration used for billing, with a
> maximum of 120s."*
>
> `duration` — *"It may differ from original_duration when speed adjustment or
> post-processing is applied; billing is based on original_duration."*

An adaptor populating the header from `duration` would bill a `speed: 2.0` request
for the clip the listener hears rather than the audio the model produced —
roughly half. This is the single most important field mapping in the integration.

### Consequences worth knowing

- **`speed` cannot move the bill.** Billing reads the pre-processing figure either
  way.
- **Billing is output-only, capped at 120s.** Reference audio and images are not
  charged, which is what makes the pre-forward reserve a true upper bound rather
  than an estimate.
- **The `url` field is unused.** It expires in 2 hours; the broker streams the bytes
  instead so the vendor's asset host is never exposed to a client.

## Billing, end to end

```
gate        AudioCreateReserve → min(max_duration, 120) × price   → balance check
serve       vendor → adaptor → broker → client                    (bytes stream through)
bill        ceil(X-0G-Audio-Duration-Seconds) × price             → the request row
```

Unit is `seconds`, stamped when the request row is created. Rate class is empty —
audio has one flat per-second rate and no tier axis.

If the header is absent or unusable the reserved ceiling is billed instead, metered
as `broker_audio_billing_fallback_total{source="reserve"}`. That **over-bills by
construction**, so any rate on that series means the adaptor stopped populating the
header — not a tuning problem.

## What is deliberately absent

| | Why |
|---|---|
| `usage` in the body | the body is audio; a JSON wrapper corrupts it |
| `x_0g_trace` | same — cost moves to `X-0G-Fee` |
| `ZG-Res-Key` | the signature lifecycle is not built. Advertising a handle that can only 404 is worse than offering none: a client that checks reads it as a FAILED attestation rather than an absent one |
| E2EE | `profileForRequest` returns `sealable=false`; no wire profile has been analyzed for this shape |
| streaming | Seed Audio is non-streaming by design. `stream_format` on this endpoint should be refused rather than faked — buffering then chunking gives no latency benefit. Tracked separately for models that do stream |
| an async job shape | Seed Audio is synchronous. A future async audio vendor would need that built; nothing speculative is carried for it |
