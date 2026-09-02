# End-to-End Encryption (E2EE) — provider/enclave side

This documents the broker (provider enclave) side of the 0G Private Computer
end-to-end encryption protocol. The normative wire spec and the reference
implementation live in `0gfoundation/0g-pc-e2ee` (`protocol/SPEC.md`, `protocol/wire`,
`protocol/crypto`); the broker MUST match it byte-for-byte and does so by
importing that module directly (`github.com/0gfoundation/0g-pc-e2ee/protocol`) rather
than reimplementing the crypto/wire format.

Related issues: broker `#601` (E2EE flow) and `#602` (report_data §4.2 binding),
router acceptance `0g-router#618`, client-side attestation verification
`0g-pc-e2ee#7`, response-signature evolution `#552`.

## Goal

The client seals the sensitive request fields (`messages`, `tools`) into an
`_e2ee` object so the prompt stays encrypted end-to-end to **this broker's
enclave**. The router routes on the cleartext fields (`model`, sampling params,
`stream`) but cannot read the prompt. The enclave decrypts inside the TEE, runs
inference, and seals the response (`choices`) back to a client ephemeral key.

Which fields those are is the **wire profile**'s answer, not a constant: `chat`
seals `messages`/`tools` → `choices`, `image` seals `prompt` → `data`, and
`anthropic` seals `messages` plus a top-level `system` → a field per response
event shape (SPEC §7.2). See "Profile resolution" below.

## Crypto suite (SPEC §3)

HPKE (RFC 9180), base mode: DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 /
ChaCha20-Poly1305. The AAD (the cleartext manifest) is over JCS-canonical JSON
(RFC 8785); the sealed body itself is NOT canonicalized — its exact bytes are
bound by the AEAD (0g-pc-e2ee#16 dropped the sealed-body JCS pass). Response
signature is ECDSA secp256k1 over an EIP-191 digest.

## Enclave enc key + attestation binding (SPEC §4)

- The enclave derives an **X25519 enc key inside the TEE** from a key-derivation
  path (`/e2ee-enc`) distinct from the secp256k1 signer key. The private key
  never leaves the enclave. See `common/tee/enckey.go` (`deriveEncKey`) and
  `common/tee/tee.go` (`SyncQuote` → `getEncKey`).
- `key_id = SHA-256(enc_pub)[0:8]` (§4.3).
- `GET /v1/e2ee/pubkey` advertises `{v, kem_id, enc_pub, key_id, signer_address}`.

### report_data binding (SPEC §4.2)

`SyncQuote` can bind `enc_pub` and `signer_addr` into the quote's `report_data` so
a client can extract and verify the enc key straight out of a verified attestation
rather than trusting `GET /v1/e2ee/pubkey`. See `buildReportData` in
`common/tee/enckey.go`. The fixed 64-byte layout:

```
offset  size  field
0       32    enc_pub      X25519 recipient key (RFC 7748 u-coord, little-endian)
32      20    signer_addr  secp256k1 Ethereum address, raw bytes
52      4     version      uint32, big-endian; = 1 (reportDataVersion)
56      8     reserved     MUST be zero
```

`report_data` is a fixed 64-byte hardware field. `TappdClient.TdxQuote` takes the
already-built 64 bytes (`[]byte`) and every backend embeds the same payload; only
the transport encoding differs per SDK (phala hex, gcp `[64]byte`, alicloud proto
bytes, mock base64).

A client MUST verify the quote's hardware signature, then read `report_data`,
check `version`, confirm `signer_addr` matches the on-chain `teeSignerAddress`,
and only then trust `enc_pub`. `GET /v1/e2ee/pubkey` stays as a convenience fetch
but is **not** a trust source — the client MUST compare its advertised `enc_pub`
against the one bound in `report_data`.

This is a **breaking change** to `report_data` (previously the legacy
signer-address-hex layout): consumers that read it as the ASCII signer address
must switch to `report_data[32:52]` and gate on `version`. Landed in `#602`,
coordinated in lockstep with client verify `0g-pc-e2ee#7` and router
`0g-router#618`.

Because the SDK/CLI attestation verifiers still parse the legacy layout, the
broker serves **both** quotes so clients migrate independently without a
fleet-wide flip. `SyncQuote` always builds the legacy ASCII signer-address quote
and, when the §4.2 binding is enabled (the default, see `bindEncPubEnvVar` in
`common/tee/tee.go`), additionally builds the §4.2 quote. `GET /quote` selects
between them:

- `GET /quote` or `GET /quote?legacy=true` (the **default**, for backward
  compatibility) → the legacy quote, for clients that predate §4.2.
- `GET /quote?legacy=false` → the §4.2 quote binding `enc_pub` (falls back to the
  legacy quote when the §4.2 quote was not generated).

The §4.2 quote is gated by the `TEE_REPORT_DATA_BIND_ENC_PUB` env var, which
**defaults to on**; setting it to a falsey value (`0`/`false`/`no`/`off`) is a
kill switch that stops emitting the §4.2 quote, so every `GET /quote` then falls
back to the legacy layout. With the §4.2 quote absent, `enc_pub` is **not**
attestation-bound — it is still published via `GET /v1/e2ee/pubkey` and E2EE
sealing works, it just cannot be verified straight out of a quote. A client that
requires the binding MUST request `legacy=false`, check `report_data[52:56] ==
version`, and reject a legacy fallback, so the fallback is safe against downgrade.
The dual-serving is a migration aid; drop the legacy quote once all clients
understand the §4.2 layout.

> The `version` in `report_data` (§4.2, `reportDataVersion` in
> `common/tee/enckey.go`) is a **separate** version from the `_e2ee` envelope `v`
> (§5, `wire.Version`) advertised by `GET /v1/e2ee/pubkey`. Both are `1` today but
> version independent SPEC layers and are not required to track together.

## Request unseal (SPEC §5–§6)

`ctrl/e2ee.go` (`MaybeUnsealRequest`), hooked into `proxy/proxy.go`
(`proxyHTTPRequest`) right after the request body is read, before any
routing/billing:

1. Detect a sealed request by a genuine top-level `_e2ee` object in the body —
   matching the router, which routes on the body field, not a header. A body that
   merely contains the substring `_e2ee` inside its content is passed through
   unchanged (not rejected). (A header signal may be added later.)
2. Select the enc key by `key_id`; verify `v` / `kem_id` (done by
   `wire.OpenRequest`, plus an explicit `key_id` check for a clear error).
3. Recompute AAD = JCS(envelope minus `_e2ee.ciphertext` and any `unbound_fields`)
   and HPKE-`Open` (`info = "0g-pc/v1/seal"`), **fail-closed** on any error.
4. Verify `keys(plaintext) == sealed_fields`, no collision with cleartext
   fields, and `signer_addr == this enclave's signer address` (the pin; renamed
   from `provider_id` in 0g-pc-e2ee #17).
5. Reconstruct the request = cleartext ∪ decrypted, replace the body, and run the
   normal proxy path on the plaintext.

The reconstructed plaintext (pre-upstream-rewrite) is stashed on the context for
the §8 signature.

### Stale enc key self-heal (409)

The enc key is measurement-tied, so a provider upgrade can rotate it while the
router/client still hold the old `key_id`. A sealed request that selects a
`key_id` which is not the enclave's current key is rejected with **HTTP 409** and
a body whose message begins with the token `e2ee_key_mismatch` (carrying the
current `key_id` as a non-authoritative hint). This is a retriable, self-healing
signal: the router/gateway should re-fetch and re-verify the enc key (via the
quote once §4.2 lands — never trust a forwarded key blindly) and re-seal to this
provider, rather than treating it as a generic client error. It is detected
pre-inference in `MaybeUnsealRequest`, so nothing is billed. All *other* unseal
failures (tampered AAD, malformed envelope, unusable ephemeral key, `signer_addr`
mismatch) stay **400** fail-closed — re-fetching a key would not help. Detection
uses the `ctrl.ErrE2EEKeyMismatch` sentinel; the router must key off the
`e2ee_key_mismatch` token (coordinate with `0g-router#618`).

## Profile resolution (SPEC §5.1)

`profileForRequest(serviceType, surface)` in `ctrl/e2ee.go` is an **allowlist**
keyed on the service type AND the API surface the request arrived on
(`apiFormatForPath`); anything absent is refused rather than guessed, since a
request shape nobody has analyzed would otherwise get some other profile's rule
applied silently.

| Service type | Surface | Profile |
|---|---|---|
| `chatbot` | `/chat/completions` | `chat` |
| `chatbot` | `/messages`, `/v1/messages` | `anthropic` |
| `chatbot` | an unrecognized path | **refused** |
| `text-to-image` | any (not a chat surface) | `image` |
| everything else | any | refused |

**The surface is half the key, not decoration.** One chatbot service answers on
both chat paths, so keyed on the service type alone an Anthropic sealed request
resolved to `chat`: the response path then sealed an injected empty `choices`
while the real `content`/`delta` rode in the frame's cleartext half — no error
anywhere, since the wire format is identical and the frames look plausible.

An **unrecognized** chatbot path is refused for the same reason, and it is the
likelier way in: adding a chat route means adding to `constant.TargetRoute`,
while teaching `apiFormatForPath` about it is a separate edit nothing forces — so
a surface that fell through to `chat` would apply chat's rules to an unanalyzed
request shape, silently. It costs nothing today (every chatbot route in
`TargetRoute` is matched, pinned by
`TestEveryChatRouteHasARecognizedSurface`), and an unsealed request never
reaches this resolution at all.

`MaybeUnsealRequest` resolves it once and stashes it on the gin context
(`CtxKeyE2EEProfile`); every seal path reads it back rather than re-deriving or
taking a constant from its call site, so a response cannot be sealed under rules
that were never applied to its request.

## Response seal (SPEC §7)

`ctrl/e2ee.go` seals the response's sensitive fields to the request's
`client_eph_pub` (`info = "0g-pc/v1/resp"`), leaving `usage`/`model`/`id`
cleartext for router billing. What to seal comes from
`wire.ResponseSealedFieldsForFrame(profile, frame)` — resolved per frame, since a
frame-typed profile's answer is a property of the frame.

- Non-streaming (`handleChargingResponse`): one frame via
  `maybeSealNonStreamResponse`, sealed after sanitization and before the write.
- Streaming (`handleChargingStreamResponse`): a per-stream `responseFrameSealer`
  seals each SSE frame under one HPKE context (sequence increments per frame).
  Each sealed frame is a self-contained SSE event (`\n\n` terminator) so it never
  merges with the next frame or `[DONE]` in the client's SSE reader.
- **What a sealed stream may emit is an ALLOWLIST**: the blank line that separates
  SSE events, the `[DONE]` sentinel *where the profile's own grammar has one*, and
  `data:` frames (sealed). Every other line
  — `event:`, `id:`, `retry:`, an unknown field — is dropped, and a `data:`
  payload that is not a JSON object fails the stream closed. They all sit outside
  the frame JSON and so outside the AAD and the §8 binding, and while everything a
  sealed frame's cleartext half may hold is checked by the per-shape taxonomy,
  these lines are checked by nothing (`sanitizeStreamLine`'s leak-field stripping
  only inspects `data:` JSON too). Forwarding one hands an upstream a channel for
  arbitrary text to the client and to every intermediary on an otherwise sealed
  turn.
- **The `event:` line is REBUILT from each frame's bound `type`**, which is why
  dropping the upstream's costs nothing: §7.2 already has a receiver ignore the
  received line and rebuild it from the bound discriminator, and `sealFrame` does
  the same derivation for forwarded and synthesized frames alike.

  It is built **only for a profile that has a discriminator**, and that gate is
  the load-bearing half. On such a profile the value is already validated —
  `ResponseSealedFieldsForFrame` refuses any shape outside the taxonomy, so it can
  only be one of a fixed set of identifiers. On a single-shape profile nothing
  validates it: `type` is an ordinary cleartext field the wire package has no rule
  about, so an upstream could put anything there **including a newline**, and a
  line built from it would end and start a fresh SSE line — an attacker-chosen,
  unsealed, unbound `data:` frame written into a sealed stream ahead of the real
  one. That is the very channel this section closes, so a chat or image stream
  gets no event line (which is also what their API sends), and a discriminator
  carrying a line break fails the frame closed rather than being quietly dropped.
- **Chat streams** (no terminal event of their own): every data frame is sealed as
  NON-final and exactly one synthetic final frame is emitted at stream end —
  before `[DONE]`, or on EOF-without-`[DONE]`. `final` is deliberately NOT derived
  from per-frame `usage` (empty `usage:{}` chunks and vLLM
  `continuous_usage_stats` would otherwise mark a non-terminal frame final and
  truncate the stream).
- **Anthropic streams** end with a terminal EVENT, which is sealed AS the final
  frame so nothing synthetic follows it. There are **two**: `message_stop` closes
  a completed turn and `error` closes one that failed partway, sending no
  `message_stop` at all — so which shapes are terminal is asked of
  `wire.IsTerminalResponseFrame` rather than hardcoded here. Recognizing only
  `message_stop` would mark an error-terminated stream non-final and then append a
  `message_stop` after the `error`, a sequence no Anthropic stream produces and
  one that reads to a client as a turn that completed normally.
  `newResponseFrameSealer` proves at stream setup that the capping frame is one
  the profile can actually SEAL, by sealing it through a throwaway HPKE context
  and discarding the result. Resolving its sealed set proves less than it looks:
  `SealFrame` also requires every declared field to be present and runs the
  profile's final-frame cleartext checks — the image profile passed a resolve-only
  probe and would then have failed at EOF on its §7.1 `usage.output_images`
  requirement, which a synthesized placeholder cannot carry. (That is the honest
  answer for image: it has no legal way to cap a truncated stream. Unreachable
  today, since only the chatbot path streams.)

  On an upstream that drops off before either, `finalFrameLine` synthesizes a
  `message_stop` — a full `event: message_stop` + sealed data event, a legal frame
  of the API (it seals nothing per §7.2) rather than a bare placeholder. It never
  synthesizes an `error`: there is no failure to report, only a truncation, and
  inventing one would attribute to the model something it did not produce.

  > **The capped turn is INCOMPLETE, and a client integrator will see that.**
  > Anthropic's grammar ends a turn `content_block_stop` → `message_delta`
  > (carrying `stop_reason` and `usage.output_tokens`) → `message_stop`. A stream
  > truncated mid-`content_block_delta` and capped here skips the first two, so an
  > SDK accumulating it yields a `Message` with `stop_reason: null` and possibly an
  > unclosed content block, and the router never sees an output-token count for
  > that turn. The frame is legal; the SEQUENCE is one no complete turn produces —
  > which is the honest signal, since the turn did not complete. Synthesizing a
  > `message_delta` to fill the gap is deliberately NOT done: `stop_reason` and the
  > token count would both be invented, and §8 signs whatever is sent, so the
  > broker would be attesting numbers the model never produced. Treat a turn whose
  > `stop_reason` is null as truncated. (The chat path is symmetric — its synthetic
  > final frame carries empty `choices` and no `finish_reason` — it is only more
  > visible here, because the Messages SDKs run a stricter state machine.
  > Synthesizing the `content_block_stop` too, which seals nothing and would be
  > safe, needs the sealer to track whether a block is open; not worth the state
  > until something actually trips on it.)
  What to synthesize is the one per-profile literal left in `ctrl/e2ee.go`
  (`synthFinalFrameFor`), because "which event should a broker invent when the
  upstream sent none" is a serving decision rather than a wire rule. So
  `newResponseFrameSealer` proves the entry is one the profile can seal *before*
  the first frame goes out: a frame-typed profile added without one fails the
  request up front instead of emitting a stream with no final frame, which is a
  truncation the client rejects wholesale and which EOF is too late to report.
  The EOF path logs a synthesis failure for the same reason.
- **A data frame arriving BEHIND the final frame is never sealed.** §7 puts the
  final frame last, and with a frame-typed profile a terminal event can land
  mid-stream, so an upstream can send one (a proxy appending `message_stop` after
  `error`, or duplicating it). It matters mostly for §8: `sealFrame` folds every
  frame it seals into the streaming binding, and a client stops consuming at the
  frame marked `final` — so a trailing frame would leave the client recomputing
  the binding over N frames while the broker signed N+1, failing verification on
  a turn that otherwise succeeded. Handling it before the seal is what keeps the
  binding equal to what the client received.

  What happens then depends on whether the frame carries an answer
  (`handleFrameAfterFinal`):

  - **Dropped, with a `Warn`**, when it carries none — a duplicate or trailing
    terminal event, or a frame holding none of the fields its shape would seal,
    where EMPTY counts as absent (`choices: []`, `{}`, `null`). That last part is
    load-bearing rather than pedantic: OpenAI's trailing usage-only chunk carries
    `"choices": []`, so a presence-only test failed the very frame this branch
    exists for — and `[]` is how this file itself writes "nothing here", since
    `ensureSealedFieldsPresent` manufactures exactly that as its placeholder. That is the case actually
    seen in the wild, and the client is unharmed: it already has a complete final
    frame. Failing instead would be worse than the quirk, because the stream is
    already committed and flushed — `handleBrokerError` ends in
    `ctx.JSON(400, …)`, which appends a JSON error body behind the sealed final
    frame and reports a fully delivered turn as a broker error.
  - **Fails the stream**, when the frame does carry one of them, when it reports
    a FAILURE (a non-empty `error`), or when its shape is unknown and so might
    carry either. The failure check deliberately does NOT go through the sealed
    set: `error` is Anthropic's content field for its `error` shape, but chat's
    sealed set is only `choices`, so an OpenAI-style `{"error": …}` chunk behind
    `[DONE]` would otherwise be dropped and leave the client believing the turn
    completed — the same swallowed downstream failure, missed only because the
    two APIs name it differently. That is the one case where dropping loses data
    — something the client would never see — and stopping is all the broker can
    do about it, since the frame cannot be sealed without breaking the binding.
    Being TERMINAL is not an exemption: Anthropic's `error` is both terminal and
    content-bearing, so a trailing one reports a real downstream failure and must
    not be swallowed.

  Blank lines still pass through — a real stream ends its last event with a blank
  line. An empty `data:` line carries no frame and no content, so it is dropped
  rather than failed.

  **`[DONE]` passes through only on a profile whose grammar has it.** It is a
  sentinel rather than content, and an OpenAI-shaped chat client requires it, so
  the chat and image profiles forward it. A Messages stream has no such sentinel —
  it ends with a terminal EVENT — so on a frame-typed profile the line is DROPPED
  (at `Debug`, since an upstream emitting it is a quirk, not a fault: LiteLLM's
  Anthropic passthrough does on some versions). Forwarding it there was
  inconsistent three ways at once: it is unsealed, unbound cleartext on a stream
  whose whole point is that every byte is sealed or accounted for; it contradicts
  the allowlist's own justification, which admits the sentinel because the profile's
  clients need it; and it arrives *behind* a terminal event that a Messages SDK has
  already used to close the turn, so at best it is ignored and at worst it trips a
  stricter state machine. The synthetic final frame (if the sentinel is what ends
  the stream) is still emitted first in both cases — only the sentinel line itself
  differs.
  The sentinel is matched on the PARSED payload, because SSE makes the space after
  the colon optional: `data:[DONE]` is the same sentinel as `data: [DONE]`, and
  matching the raw line meant the spaceless form fell through to the fail-closed
  branch and destroyed a turn that had already delivered every content frame.

**A failure report inside a 200 response is sealed even where the profile's own
set does not name it.** (An HTTP-level failure — a non-200 from the upstream — is
a different path and is NOT sealed; see "An upstream HTTP error body is not
sealed" below. This rule is about the `error` field of a response the upstream
returned 200 for, which is how both APIs report a mid-turn failure.)
An upstream error message can quote the request that produced it, so `error` is
content on either surface — but only the Anthropic taxonomy says so: chat's
sealed set is `["choices"]` whatever the frame holds, so an OpenAI-style
`{"error": …}` chunk mid-stream used to carry its message in the frame's
cleartext half. `prepareFrameForSealing` adds `error` to the sealed set for a
profile with no discriminator (a sealed SUPERSET is legal — only the profile's
required set is mandated — and it opens on a conforming client). A frame-typed
profile is left alone, because it already governs the field and disagrees about
where it belongs: its `error` shape seals `error`, carrying it on any other shape
is refused outright, and adding it to a shape that seals nothing is refused too.

> ⚠️ **This is a behavior change on the chat path, and the router is the party it
> lands on.** On a sealed chat turn — streaming and non-streaming alike — a
> top-level `error` object USED TO travel in the cleartext half and now does not.
> Any router-side logic that reads `error` out of a sealed chat frame to classify a
> failure, decide a retry, or fail over to another provider stops seeing it: the
> router holds no response key, so the field is opaque to it. It is still
> DETECTABLE without a key — the frame's cleartext `sealed_fields` names `error`,
> which is exactly "this turn reported a failure" and enough to drive
> classification, retry or failover — but the message text and any error code are
> not. HTTP status, `ZG-Failure-Source` and the usual headers are unaffected. If
> the router needs more than the presence signal on a sealed turn, that has to come
> from a cleartext channel outside the frame. Coordinate with `0g-router`.

`ensureSealedFieldsPresent(profile, frame, sealedFields)` injects an empty
placeholder only for the sealed fields a frame of THAT PROFILE may legitimately
**omit** — `choices` (chat) and `data` (image), both arrays, so an empty one
merges to nothing on the client. That is what lets a trailing usage-only chat
chunk with no `choices` still seal. The permission is keyed on the profile, not
the bare field name: the name alone does not carry the invariant, so a
frame-typed profile with a content field that happened to be called `data` would
otherwise inherit the image profile's permission and get a placeholder on a frame
obliged to carry content.

**Anthropic has no such field**, for two separate reasons. Its per-shape stream
fields are OBJECTS (`delta`, `content_block`, `error`), where `[]` would be a type
error shipped to the client rather than a placeholder. And its non-streaming
`content` is an array but is never legitimately absent — the Messages API always
returns it on a `message` response, an empty array at worst — so a placeholder
there could only fire on a broken upstream, and would then seal, sign and mark
final a frame carrying an empty answer while the router bills the output tokens
that same response reported, with nothing reporting a problem. So a frame whose
shape declares a field it does not carry fails closed.

Billing is unaffected: it reads the raw upstream bytes, not the sealed copy.

### Unbound fields: `model`, `x_0g_trace` (SPEC §5.2)

Every sealed response declares `unbound_fields: ["model", "x_0g_trace"]` —
cleartext fields EXCLUDED from the seal AAD. The response travels
broker → router → client, and the **router** rewrites `model` (substituting the
served model back to the alias the client requested) and attaches `x_0g_trace`
(an observability trace) to the sealed response on the way back. Because they are
unbound, the router's rewrite/injection does not break the client's `Open`, while
every bound field (`choices` sealed, `usage`/`id` cleartext) stays tamper-evident.

> ⚠️ On an **Anthropic stream** the `model` alias has nowhere to be rewritten:
> there is no top-level `model` on those frames — it lives at `message.model`
> inside message_start, which is BOUND (it has to be: the router's input token
> count is in the same object, and §7.2 keeps `message` cleartext-and-bound so
> that count is authenticated). Declaring `model` unbound is therefore a no-op
> there. The router's alias substitution has to skip an Anthropic sealed stream,
> or accept that the served model name is what appears; making `message` unbound
> to allow it is NOT an option — it would unauthenticate the token count and void
> the §7.2 `message.content` check. Non-streaming Anthropic responses do carry a
> top-level `model` and are unaffected. Coordinate with `0g-router`.

Per the §8 corollary a router-injected value is not cryptographically trusted
(trust comes from on-chain settlement), so these MUST be unbound, never
bound/signed fields. Both are response-only — they are not on the request path and
never reach any upstream. Declared via the seal APIs' trailing `unboundFields`
variadic (`SealResponse` / `NewResponseSealer`).

## Response signature (SPEC §8)

Today `ctrl/signing.go` → `signChatE2EE` binds the JCS-canonical reconstructed
request and the JCS-canonical decrypted response. Non-E2EE requests keep the
existing `signChatWithKey` behaviour.

> ⚠️ **Divergence from the current SPEC §8.** 0g-pc-e2ee#16 redefined §8 to bind
> the on-wire bytes — `sha256(request aad ‖ ciphertext) : sha256(response aad ‖
> ciphertext)` — instead of `JCS(plaintext)`, so that no canonicalization of the
> sealed content is needed and the signature covers exactly the non-`unbound`
> content. Our `signChatE2EE` has not been reworked to this yet; it is a known,
> tracked gap (not urgent — client-side verification `0g-pc-e2ee#7` is not yet
> implemented, and the rework is blocked on the wire package exporting an
> `aad ‖ ciphertext` signing-bytes helper). Tracked with `#552`.

### Streaming signature — TODO (tracked)

A streaming response has no single canonical JSON object, and the client-side
verify implementation (`0g-pc-e2ee#7`) is not yet in place. As a **provisional**
binding the broker hashes the ordered concatenation of the delivered plaintext
frames as-is (whole-frame plaintext concatenation). The exact canonicalization
MUST be reconciled with the client verify implementation before streaming E2EE
signatures are relied upon. Tracked in `#552` and `0g-pc-e2ee#7`.

### An upstream HTTP error body is NOT sealed

Everything above is about a response the upstream returned **200** for. When the
upstream returns a **non-200**, `ProcessHTTPRequest` hands off to
`handleServiceError` and returns *before* the charging handlers run — so no seal
path is reached, and the upstream's error body goes to the client, through the
router, **in the clear**, on a request whose payload was sealed.

This is a real and deliberate gap, not an oversight to read past:

- **It is the same content class the rule above seals.** The argument for sealing
  a 200 response's `error` field — an upstream error message can quote the
  request that produced it — applies verbatim to a 4xx/5xx body, which is usually
  a richer quote of the request than any in-band `error` frame.
- **Why it is not closed here.** The seal path seals a *conforming response frame
  of a profile*: `wire.ResponseSealedFieldsForFrame` asks what shape this frame
  is and what that shape must hide. An upstream 500 with an arbitrary body is not
  a frame of any profile, so there is nothing for the taxonomy to answer.
  Sealing it needs a wire-level shape for "the request failed at the HTTP layer"
  (a sealed error envelope with its own §7 rules), which is a `0g-pc-e2ee`
  protocol addition and a cross-repo change — not something to improvise inside
  the broker, because a client that cannot recognize the shape gets a body it
  cannot open on the one path where it most needs to read the reason.
- **What does apply today.** For forwarder services the body is leak-sanitized
  before re-emission (`sanitizeResponseBody`, #184) so it cannot name the
  upstream, and the broker's own log of it is secret-redacted and truncated
  (`redactUpstreamSecrets`). Neither is confidentiality: both are aimed at a
  different threat, and the vendor's message text — request quote included —
  survives both by design.

So: **a sealed turn hides the request and the answer, and does not hide why an
HTTP-level failure happened.** Treat that as the current boundary. Closing it is
protocol work; tracked below.

## Out of scope (tracked elsewhere)

Client-side attestation verify (`0g-pc-e2ee#7`); router acceptance of sealed requests
(`0g-router#618`); candidate scoring; finalized streaming signature format
(`#552`); §4.2 `report_data` binding of `enc_pub` (deferred follow-up, see above);
a sealed shape for an upstream **HTTP error body** (see the section directly
above — needs a `0g-pc-e2ee` wire shape first, so today a non-200 upstream body
reaches the client in the clear on a sealed turn).
