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
| `chatbot` | `/v1/chat/completions` (or an unrecognized path) | `chat` |
| `chatbot` | `/v1/messages` | `anthropic` |
| `text-to-image` | any | `image` |
| everything else | any | refused |

**The surface is half the key, not decoration.** One chatbot service answers on
both chat paths, so keyed on the service type alone an Anthropic sealed request
resolved to `chat`: the response path then sealed an injected empty `choices`
while the real `content`/`delta` rode in the frame's cleartext half — no error
anywhere, since the wire format is identical and the frames look plausible.

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
  merges with the next frame or `[DONE]` in the client's SSE reader. Non-`data:`
  lines pass through, which is what carries an Anthropic `event: <type>` line
  through beside its sealed data line.
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
  On an upstream that drops off before either, `finalFrameLine` synthesizes a
  `message_stop` — a full `event: message_stop` + sealed data event, a legal frame
  of the API (it seals nothing per §7.2) rather than a bare placeholder. It never
  synthesizes an `error`: there is no failure to report, only a truncation, and
  inventing one would attribute to the model something it did not produce.

`ensureSealedFieldsPresent` injects an empty placeholder only for the sealed
fields a frame may legitimately **omit** — `choices` (chat) and `data` (image),
both arrays, so an empty one merges to nothing on the client. That is what lets a
trailing usage-only chat chunk with no `choices` still seal.

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

## Out of scope (tracked elsewhere)

Client-side attestation verify (`0g-pc-e2ee#7`); router acceptance of sealed requests
(`0g-router#618`); candidate scoring; finalized streaming signature format
(`#552`); §4.2 `report_data` binding of `enc_pub` (deferred follow-up, see above).
