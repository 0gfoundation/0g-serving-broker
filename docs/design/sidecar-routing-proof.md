# TEE Routing Proof Through a Protocol-Translation Sidecar

## The problem it solves

A `centralized` provider's TEE routing proof is the broker's answer to "how do I know
this response really came from the vendor it claims?". The broker signs, inside its
TEE:

```
sha256(request):sha256(response):providerType:providerIdentity:tlsCertFingerprint
```

The load-bearing field is the last one: the SHA256 of the leaf certificate of the TLS
connection that served the response, read from `resp.TLS` on the broker's own HTTP
call. `signCentralizedRoutingProof` refuses to sign without it, because a TEE-signed
envelope with no TLS evidence gives a verifier false confidence.

That works when the broker talks to the vendor directly. It breaks the moment the
vendor's wire protocol isn't one the broker speaks. For those we run a stateless
protocol-translation sidecar (`api/videotranslator`: DashScope, MiniMax) in the same
CVM, and point `service.targetUrl` at it:

```
broker  --http (in-CVM)-->  translator sidecar  --https-->  api.minimax.io
        ^                                        ^
        no TLS: resp.TLS is nil                  the handshake the proof is about
```

The broker's hop has no certificate to bind, so before this change a translated
provider could not be `centralized` at all — config load rejected the plaintext
`targetUrl`. The only configuration that booted was `providerType: standard`, which
forces `verifiability: standard`: no signature, no proof, no verification. The same
vendor account served as a chatbot (direct HTTPS) stayed `TeeML` while the video
provider in front of it silently became non-verifiable.

## The fix: report the fingerprint from where the handshake happens

The sidecar is in the **same TDX CVM as the broker** and is covered by the same
quote. Its observation of the vendor certificate therefore carries the same weight
as the broker's own would — so the sidecar makes the observation and hands it back.

1. **Sidecar** — `tee.CertCapture` is installed per inbound request by the
   `handler.UpstreamTLSReport()` middleware. Each vendor HTTP call reports its
   `resp.TLS` into it (`internal/{minimax,dashscope}/client.go`), and the middleware
   emits the first observed leaf fingerprint on the response:

   ```
   Zg-Upstream-Cert-Fingerprint: <sha256 hex>
   ```

   First observation wins: one inbound request may also fetch the finished asset
   from the vendor's CDN, and the proof must bind the API endpoint that produced the
   signed body.

2. **Broker** — with `service.targetTLSProxy: true`, `Ctrl.upstreamCertFingerprint`
   takes the fingerprint from that header instead of `resp.TLS`, and config load
   waives the HTTPS requirement on `targetUrl`. Everything downstream — the proof
   format, the cache, `/v1/proxy/signature/{chatID}`, every verifier — is unchanged:
   the fingerprint is still the vendor's real leaf certificate, just witnessed one
   hop earlier.

### The two sources never mix

This is the property that makes the header safe, and it is enforced in one place:

| `targetTLSProxy` | evidence used | ignored |
|---|---|---|
| `false` (default) | `resp.TLS` | the header — an upstream must never be able to dictate its own routing proof |
| `true` | the header | `resp.TLS` — that is the shim's own cert (or nil), which proves nothing about the vendor |

A malformed or absent report yields no fingerprint, and the signer then refuses:
a missing proof is the correct outcome, never a fabricated one.

`targetTLSProxy` is rejected at config load for any provider type other than
`centralized`, and the header is stripped from every client-facing response
(`isUpstreamLeakHeader`) — on a `standard` provider the vendor's certificate
fingerprint would identify the upstream that deployment exists to hide.

### What stops an operator pointing this at something that isn't in-enclave

The header is a plain string; what makes it evidence is the shim being covered by
the same attestation. So the flag is not taken on trust — config load rejects any
target that could not be in-CVM (`validateInEnclaveTarget`):

- **`https://` is rejected.** An in-CVM hop is never TLS, so an HTTPS target means
  the broker has real first-hand `resp.TLS` it would now be ignoring in favour of a
  header the remote host writes — strictly worse than not setting the flag.
- **Routable hosts are rejected** (public IPs, dotted DNS names). Only loopback,
  private/link-local addresses, and bare compose service names pass. A shim outside
  the enclave is not covered by the CVM's quote, and the broker would also be
  sending the injected vendor API key across the public network in cleartext.

What deliberately remains is that the operator can declare a *fake* shim as a
service in their own compose. No config check can catch that, and none needs to:
the compose is hashed into the CVM measurement, so cheating that way changes the
quote. That is the same trust boundary the rest of the deployment already rests on
— and it is why the sidecar must be declared in the **same measured compose** as
the broker, not started separately.

### On an end-to-end-encrypted request the proof travels nested

A sealed (`_e2ee`) request already has a signature of its own — the 0g-pc SPEC §8
ciphertext binding — and `/v1/proxy/signature/{chatID}` has room for exactly one
top-level signed statement. On a sealed request that statement has to stay §8:
it is what the E2EE client verifies, and it is the signature the client refuses
the response without.

So the routing proof is served **alongside** it, under `routing_proof`, carrying
its own `text` and `signature`:

```jsonc
{
  "text": "zg-sig-v1/e2ee-ct:<reqHash>:<respHash>",   // §8, unchanged
  "signature": "0x…",
  "signing_address": "0x…",
  "signing_algo": "ecdsa",
  "routing_proof": {                                   // present only when sealed AND centralized
    "text": "<req>:<resp>:centralized:<identity>:<fingerprint>",
    "signature": "0x…",                                // its OWN signature, over its own text
    "signing_address": "0x…",
    "provider_type": "centralized",
    "provider_identity": "api.vendor.example",
    "tls_cert_fingerprint": "…"
  }
}
```

Three properties are deliberate:

- **Nested, not merged.** Every field inside `routing_proof` is covered by
  `routing_proof.signature`. Hoisting the fingerprint next to a §8 signature that
  does not cover it would hand verifiers a value with nothing behind it — the same
  false confidence the signer refuses to create when the fingerprint is missing.
- **The same text format** as an unsealed proof, so no verifier needs new code for
  the nested case. On a sealed request its two hashes are over the on-wire
  ciphertext, so the pair chains end to end: the routing proof says which vendor's
  TLS connection produced exactly those bytes, and §8 says those bytes are the
  ciphertext the client decrypted.
- **Unsealed responses are untouched.** There the routing proof *is* the whole
  signature and stays flat at the top level, `routing_proof` absent. Both shapes
  are asserted, so neither can drift into the other.

Why it needed doing: `signChatResponse` returns from its E2EE branch before the
centralized one, so before this a sealed request to a centralized provider got
**no routing proof at all** — the vendor attestation that is the primary trust
artifact of a centralized route, dropped on exactly the traffic that asked for
the most confidentiality, and (per the section below) not counted as a skip
either. Assembly failure is non-fatal: §8 still gets cached, because losing the
load-bearing signature over missing vendor evidence would turn an absent extra
into a failed request.

### When a proof is not produced

Every path that cannot produce a proof — no TLS, no sidecar report, a malformed
report, a signing failure — refuses to sign and increments
`broker_routing_proof_skipped_total{reason}`. This matters because `verifiability`
is static config: nothing else notices when proof production stops. A sidecar
rolled back to an image that doesn't report the certificate would otherwise keep
serving a `TeeML`-advertised service with zero proofs behind it, visible only as
one log line per response. **Alert on any sustained non-zero rate.**

The video path additionally *evicts* a create-time signature whenever the final body
will never be signed, so `ZG-Res-Key` 404s rather than resolving to a proof over the
queued placeholder. That rule is not specific to a sidecar — it applies to every
video provider and is documented with the rest of the async-video signature
lifecycle in
[video-generation-async-billing.md](video-generation-async-billing.md#signature-lifecycle-zg-res-key).

### What `ZG-Res-Key` covers on an async video job

The key issued with the create response is re-signed by the poller over the FINAL
polled body (pre-existing behaviour, unchanged here — it is what makes verification
of the delivered video possible at all). A client must therefore verify against the
finished job, not against the `{"status":"queued"}` envelope it first received.

Scope notes, both deliberate:

- `targetTLSProxy` is accepted only for `type: video-generation`. It makes "a 200
  with no fingerprint" a routine outcome, and video is the only modality that
  decides whether to advertise `ZG-Res-Key` from whether the proof can actually be
  produced. Wiring another modality's advertise predicate is the prerequisite for
  widening it.
- `serving_domain` changes SOURCE under this flag rather than disappearing.
  `targetUrl` is then an in-CVM container name — wrong to publish, and a violation
  of that field's contract (it is documented as matching the upstream TLS SNI/SAN,
  a connection the broker no longer makes itself). Publishing nothing would be
  worse: a verifier would hold the proof's certificate fingerprint with no host to
  fetch a certificate from and compare, and `provider_identity` alone does not
  close that (MiniMax fronts both `api.minimax.io` and `api.minimaxi.com`, with
  different certificates). So `service.upstreamDomain` is **required** under the
  flag and published in its place.

  Taking that on the operator's word is safe, and the contrast with
  `targetTLSProxy` is the reason: a wrong domain is self-defeating. The verifier
  fetches that host's real certificate and compares it against the signed
  fingerprint, so naming a host the translator does not talk to makes verification
  FAIL rather than falsely succeed. It buys verifiability without buying trust —
  where the flag itself, unconstrained, would buy the operator a forged proof.

  **But failing at the verifier is not good enough**, because the broker would not
  know. `upstreamDomain` and the host the translator actually dials are the same
  fact stored twice, in two files in two containers — the translator picks its own
  via `MINIMAX_BASE_URL` / `DASHSCOPE_BASE_URL`. Nothing couples them, and the
  shipped compose example hardcodes one while telling a domestic-site operator to
  change the other. So the translator reports the SNI it dialed alongside the
  fingerprint (`Zg-Upstream-Cert-Host`) and the broker **refuses to sign on a
  mismatch**, with reason `domain_mismatch`. A missing proof is checkable; one
  bound to a host nobody was told to check is indistinguishable from tampering.

  A translator dialing a bare IP reports no SNI (TLS sends none for an IP literal)
  and therefore produces no proof — correct, since `upstreamDomain` must be a
  hostname and could never have matched.

## Video generation also had no proof path at all

Independent of the sidecar, `video-generation` advertised `ZG-Res-Key` for a
centralized provider but never signed anything for one: the only signer on the video
path was the decentralized content signer, gated on `!TargetSeparated`, and
`centralized` forces `TargetSeparated`. A centralized video provider handed clients
a lookup key that could only 404, so relaxing the config check alone would not have
produced a working proof.

`signVideoResponse` (sync create, `video.go`) and `signVideoPollResult` (background
poller, `video_poll.go`) now dispatch on the trust model like every other modality:
routing proof for centralized, content signature for an in-network model. The poll
re-signs under the same `chatKey` the create response returned, binding the
certificate observed on **that** poll — the connection that actually delivered the
finished video, not the one used at create time possibly hours earlier.

## Configuring a translated provider

```yaml
service:
  type: video-generation
  providerType: centralized
  providerIdentity: minimax
  verifiability: TeeML
  targetUrl: http://0g-minimax-video-translator:8090   # the in-CVM sidecar
  targetTLSProxy: true                                 # it reports the vendor cert
  upstreamDomain: api.minimax.io                       # what a verifier checks that cert against
  additionalSecret:
    Authorization: "Bearer <vendor-api-key>"
```

Run the sidecar in the same compose file / CVM as the broker (see
`api/videotranslator/docker-compose.minimax.example.yml`) — the compose is what the
CVM measurement covers, so a sidecar started outside it is not attested. If the
sidecar is ever moved outside the enclave, drop `targetTLSProxy` — and with it the
claim of verifiability. Config load will refuse the combination anyway.

Sidecar entrypoints must build their engine with `handler.NewEngine()`, which
installs the reporting middleware; registering it by hand is the one step a new
vendor's `cmd/server` can silently omit.
