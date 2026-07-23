# End-to-End Encryption (E2EE) — provider/enclave side

This documents the broker (provider enclave) side of the 0G Private Computer
end-to-end encryption protocol. The normative wire spec and the reference
implementation live in `0gfoundation/0g-pc` (`protocol/SPEC.md`, `protocol/wire`,
`protocol/crypto`); the broker MUST match it byte-for-byte and does so by
importing that module directly (`github.com/0gfoundation/0g-pc-e2ee/protocol`) rather
than reimplementing the crypto/wire format.

Related issues: broker `#601` (this work), router acceptance `0g-router#618`,
client-side attestation verification `0g-pc#7`, response-signature evolution
`#552`.

## Goal

The client seals the sensitive request fields (`messages`, `tools`) into an
`_e2ee` object so the prompt stays encrypted end-to-end to **this broker's
enclave**. The router routes on the cleartext fields (`model`, sampling params,
`stream`) but cannot read the prompt. The enclave decrypts inside the TEE, runs
inference, and seals the response (`choices`) back to a client ephemeral key.

## Crypto suite (SPEC §3)

HPKE (RFC 9180), base mode: DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 /
ChaCha20-Poly1305. All AAD and content hashes are over JCS-canonical JSON
(RFC 8785). Response signature stays ECDSA secp256k1 over an EIP-191 digest.

## Enclave enc key + attestation binding (SPEC §4)

- The enclave derives an **X25519 enc key inside the TEE** from a key-derivation
  path (`/e2ee-enc`) distinct from the secp256k1 signer key. The private key
  never leaves the enclave. See `common/tee/enckey.go` (`deriveEncKey`) and
  `common/tee/tee.go` (`SyncQuote` → `getEncKey`).
- `key_id = SHA-256(enc_pub)[0:8]` (§4.3).
- `GET /v1/e2ee/pubkey` advertises `{v, kem_id, enc_pub, key_id, signer_address}`.

### report_data binding — deferred (TODO)

The §4.2 layout binds `enc_pub` into the quote's `report_data`
(`enc_pub(32) ‖ signer_addr(20) ‖ version(4) ‖ reserved(8)`) so a client can
extract and verify the enc key straight out of a verified attestation. This is a
**breaking change** to `report_data` (today it is the legacy signer-address-hex
layout) and is **deferred** to get the E2EE flow working end-to-end first.

Until it lands, `enc_pub` is published only via `GET /v1/e2ee/pubkey` and is
**not yet attestation-bound** — a client cannot yet prove the enc key belongs to
the attested enclave. Tracked as a follow-up (see the TODO in
`common/tee/enckey.go` / `SyncQuote`).

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
3. Recompute AAD = JCS(envelope minus `_e2ee.ciphertext`) and HPKE-`Open`
   (`info = "0g-pc/v1/seal"`), **fail-closed** on any error.
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

## Response seal (SPEC §7)

`ctrl/e2ee.go` seals the sensitive response fields (v1 default: `choices`) to the
request's `client_eph_pub` (`info = "0g-pc/v1/resp"`), leaving `usage`/`model`/
`id` cleartext for router billing.

- Non-streaming (`handleChargingResponse`): one frame via
  `maybeSealNonStreamResponse`, sealed after sanitization and before the write.
- Streaming (`handleChargingStreamResponse`): a per-stream `responseFrameSealer`
  seals each SSE frame under one HPKE context (sequence increments per frame).
  Every data frame is sealed as NON-final and exactly one synthetic final frame is
  emitted at stream end — before `[DONE]`, or on EOF-without-`[DONE]` — so the
  client always gets exactly one completion marker. `final` is deliberately NOT
  derived from per-frame `usage` (empty `usage:{}` chunks and vLLM
  `continuous_usage_stats` would otherwise mark a non-terminal frame final and
  truncate the stream). Each sealed frame is a self-contained SSE event (`\n\n`
  terminator) so it never merges with the next frame or `[DONE]` in the client's
  SSE reader.

Billing is unaffected: it reads the raw upstream bytes, not the sealed copy.

### Unbound field: `x_0g_trace` (SPEC §5.2)

Every sealed response declares `unbound_fields: ["x_0g_trace"]` — a cleartext
field EXCLUDED from the seal AAD. The response travels broker → router → client,
and the **router** attaches `x_0g_trace` (an observability trace) to the sealed
response on the way back. Because it is unbound, the router's injection does not
break the client's `Open`, while every bound field (`choices` sealed, `usage`/
`model`/`id` cleartext) stays tamper-evident. Per the §8 corollary a
router-injected value is not cryptographically trusted (trust comes from on-chain
settlement), so a trace object MUST be unbound, never a bound/signed field.
`x_0g_trace` is response-only — it is not on the request path and never reaches
any upstream. Declared via the seal APIs' trailing `unboundFields` variadic
(`SealResponse` / `NewResponseSealer`).

## Response signature (SPEC §8)

For a sealed request the client verifies the TEE signature over the **decrypted**
content, so the signed `text` binds the JCS-canonical reconstructed request and
the JCS-canonical decrypted response (`ctrl/signing.go` → `signChatE2EE`), not
the sealed bytes on the wire. Non-E2EE requests keep the existing `signChatWithKey`
behaviour.

### Streaming signature — TODO (tracked)

A streaming response has no single canonical JSON object, and the client-side
verify implementation (`0g-pc#7`) is not yet in place. As a **provisional**
binding the broker hashes the ordered concatenation of the delivered plaintext
frames as-is (whole-frame plaintext concatenation). The exact canonicalization
MUST be reconciled with the client verify implementation before streaming E2EE
signatures are relied upon. Tracked in `#552` and `0g-pc#7`.

## Out of scope (tracked elsewhere)

Client-side attestation verify (`0g-pc#7`); router acceptance of sealed requests
(`0g-router#618`); candidate scoring; finalized streaming signature format
(`#552`); §4.2 `report_data` binding of `enc_pub` (deferred follow-up, see above).
