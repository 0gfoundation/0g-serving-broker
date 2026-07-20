# End-to-End Encryption (E2EE) — provider/enclave side

This documents the broker (provider enclave) side of the 0G Private Computer
end-to-end encryption protocol. The normative wire spec and the reference
implementation live in `0gfoundation/0g-pc` (`protocol/SPEC.md`, `protocol/wire`,
`protocol/crypto`); the broker MUST match it byte-for-byte and does so by
importing that module directly (`github.com/0gfoundation/0g-pc/protocol`) rather
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
- The quote's `report_data` uses the §4.2 layout (64 bytes):

  | offset | size | field |
  |--------|------|-------|
  | 0 | 32 | `enc_pub` (X25519 public key) |
  | 32 | 20 | `signer_addr` (secp256k1 Ethereum address) |
  | 52 | 4 | `version` (uint32 big-endian, = 1) |
  | 56 | 8 | reserved (zero) |

  This is a **breaking change** from the legacy layout (signer address hex),
  gated by the `version` field. The signer binding is preserved (bytes 32:52);
  the change adds `enc_pub` so a client can extract it directly from a verified
  quote — the only trustworthy source of the enc key.
- `key_id = SHA-256(enc_pub)[0:8]` (§4.3). The `TappdClient.TdxQuote` interface
  now takes `reportData []byte` (was a hex string) so the binary layout is passed
  through each backend (`phala`, `gcp`, `mock`, `alicloud`).
- `GET /v1/e2ee/pubkey` advertises `{v, kem_id, enc_pub, key_id, signer_address}`
  as a convenience fetch. A client MUST still verify `enc_pub` against the quote's
  `report_data`; this endpoint is not itself a trust source.

## Request unseal (SPEC §5–§6)

`ctrl/e2ee.go` (`MaybeUnsealRequest`), hooked into `proxy/proxy.go`
(`proxyHTTPRequest`) right after the request body is read, before any
routing/billing:

1. Detect a sealed request (`_e2ee` body field or `X-0G-E2EE` header).
2. Select the enc key by `key_id`; verify `v` / `kem_id` (done by
   `wire.OpenRequest`, plus an explicit `key_id` check for a clear error).
3. Recompute AAD = JCS(envelope minus `_e2ee.ciphertext`) and HPKE-`Open`
   (`info = "0g-pc/v1/seal"`), **fail-closed** on any error.
4. Verify `keys(plaintext) == sealed_fields`, no collision with cleartext
   fields, and `provider_id == this enclave's signer address`.
5. Reconstruct the request = cleartext ∪ decrypted, replace the body, and run the
   normal proxy path on the plaintext.

The reconstructed plaintext (pre-upstream-rewrite) is stashed on the context for
the §8 signature.

## Response seal (SPEC §7)

`ctrl/e2ee.go` seals the sensitive response fields (v1 default: `choices`) to the
request's `client_eph_pub` (`info = "0g-pc/v1/resp"`), leaving `usage`/`model`/
`id` cleartext for router billing.

- Non-streaming (`handleChargingResponse`): one frame via
  `maybeSealNonStreamResponse`, sealed after sanitization and before the write.
- Streaming (`handleChargingStreamResponse`): a per-stream `responseFrameSealer`
  seals each SSE frame under one HPKE context (sequence increments per frame).
  The frame carrying `usage` is marked `final` (the broker forces
  `stream_options.include_usage`, so the last chunk before `[DONE]` carries it);
  if no such frame appears, a synthetic final frame is emitted before `[DONE]` so
  the client can always detect completion.

Billing is unaffected: it reads the raw upstream bytes, not the sealed copy.

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
(`#552`).
