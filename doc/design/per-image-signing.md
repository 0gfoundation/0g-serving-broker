# Per-image signing (trust-chain links 7 and 8)

Implementation spec for the one unimplemented row of
[`doc/attestation-trust-chain.md`](../attestation-trust-chain.md). Everything here follows
from that document; this file only says *how*.

## Why it is one slice and not two

Link 7 (a stale attestation stops verifying) is delivered by deriving the signer key from
the running image. Link 8 (the key never sits in the broker) is what makes link 7 hold: a
broker image that can read the key can exfiltrate it and keep signing with it after the
upgrade, which restores exactly the hole link 7 closes.

So per-image derivation without controller-side custody is worth nothing, and the two ship
together.

## What changes

### 1. Controller: two narrow operations replace `/GetKey`

`controller/internal/attestproxy` currently forwards `/GetQuote`, `/Info`, `/GetKey`.
Forwarding `/GetKey` lets the broker derive any path, including the previous image's — so
it must go, and be replaced by operations that never hand over a signing key:

```
POST /GetQuote        forwarded to dstack unchanged
POST /Info            forwarded to dstack unchanged
POST /Sign            {hash: hex}  → {signature: hex}   signs with S_current
POST /GetEncKey       {}           → {key: hex}         the current image's enc key
POST /GetKey          REMOVED
POST /EmitEvent       never forwarded
```

`S_current` is derived as `dstack GetKey(path = "/" + <current broker image digest>)`, and
the current digest is read the way `restoreImageRecord` reads it: `GetContainerStatus` on
`containerBroker`, requiring an exact name match, taking the part after `@`. Refuse when the
digest cannot be established — a signature under an unknown key is worse than no signature.

The private key is derived on demand and never written to a response. Only `/GetEncKey`
returns key material, because the broker must decrypt requests itself; it is per-image too,
so an upgraded image cannot decrypt what was sealed to its predecessor.

### 2. Broker: `TeeService` gains a signing seam

`common/tee/tee.go` holds `ProviderSigner *ecdsa.PrivateKey` and callers sign with it
directly. Replace the field with a method:

```go
func (s *TeeService) Sign(hash []byte) ([]byte, error)
```

Two implementations behind it:

- **local** — the current behaviour, `crypto.Sign(hash, key)`, for every deployment that
  has not moved to the controller (`TEE_SOCKET` unset). Keeps invariant 1.
- **remote** — POST `/Sign` to the controller socket.

`TeeService.Address` must become the address of whichever key is in play, so it is read from
the controller in the remote case (add `/SignerAddress`, or return it from `/GetEncKey`;
prefer a separate call so the two are independently cacheable).

Call sites to convert — all of them pass a hash and want 65 bytes back, so the change is
mechanical:

| File | Count |
|---|---|
| `inference/internal/ctrl/signing.go` | 4 |
| `common/tee/tee.go` | 2 |
| `fine-tuning/internal/services/finalizer.go` | 1 |

The recovery-id fixup (`if sig[64] == 0 \|\| sig[64] == 1 { sig[64] += 27 }`) currently sits
at each call site; keep it there rather than inside `Sign`, so the remote and local paths
return identical bytes and the existing signature format is untouched.

### 3. `report_data` is unchanged

It already carries `signer_addr` and `enc_pub`. Only the values change, because the
derivation changed. No layout change, no version bump, no client-side format work — a client
that already extracts the signer address from a verified quote keeps working.

## What to verify, and what to attack

Tests that must exist:

- `/GetKey` and `/EmitEvent` are refused; `/Sign` and `/GetEncKey` reach the controller's own
  handlers and never dstack's `/GetKey`.
- `/Sign` refuses when the broker container's digest cannot be established exactly (no
  container, substring-only name match, tag-only reference).
- Two different current digests produce two different signer addresses.
- Local and remote paths produce byte-identical signatures for the same hash and key.
- `TEE_SOCKET` unset ⇒ every byte of today's behaviour, including the address.

Attack angles a review must cover, because each of them silently voids the slice:

1. Can the broker reach dstack's `/GetKey` by any route — a path that normalises, a second
   socket, an env var pointing back at dstack?
2. Can the broker ask `/Sign` for a digest other than the current one? (It must not be able
   to name one at all.)
3. Can a stale `S` still verify — i.e. is the address the client reads from `report_data`
   really the per-image one, on both the quote path and the response path?
4. Does anything log or return the derived signing key?
5. Streaming: what is the signature granularity? If `StreamBinder` signs per chunk, each
   chunk becomes a controller round trip. Measure before shipping; if it is per chunk,
   either batch or keep the local path for streams and say so explicitly.

## Operational consequences to write into the runbook

- Every in-place upgrade changes `teeSignerAddress`, which resets
  `Service.teeSignerAcknowledged`. Recovery is `acknowledgeTEESignerByOwner`, which is
  `onlyOwner` — **the provider cannot self-heal**. An upgrade therefore needs the contract
  owner in the loop. This is the intended cost of link 7, not a defect.
- The controller enters the response path. Its availability becomes the service's
  availability.
- Deployment order: the controller must serve `/Sign` before any broker is pointed at it.

## Status

Not started. `#644` currently forwards `/GetKey`; narrowing it is part of this slice, not a
separate change, because the broker has no other way to obtain keys until `/Sign` and
`/GetEncKey` exist.
