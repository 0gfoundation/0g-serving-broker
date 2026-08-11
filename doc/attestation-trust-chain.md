# Attestation Trust Chain

What a user is told, and why they can believe it, when

- the request passes through a **router that is not a TEE and is not trusted**, and
- the **broker component is upgradeable in place**, without the CVM's `compose_hash` changing.

This document states the finished scheme. It does not argue for it.

---

## The claim

> **This response was produced, inside a TDX CVM, by the broker image whose digest I
> recognise — and it was produced now, not at some earlier time.**

Nothing weaker is useful: a claim about the *deployment* rather than the *running image*
is satisfied by an in-place upgrade to anything, and a claim about *some past moment* is
satisfied by replaying an old attestation.

---

## The chain

Each link names what it establishes and what enforces it. A link marked ⚙ is enforced by
hardware or cryptography; a link marked 👤 is a one-time human check the user performs.

| # | Established | Enforced by |
|---|---|---|
| 1 | The attestation is genuine and unmodified in transit | ⚙ DCAP signature over the quote. The router carries it but cannot alter it. |
| 2 | Which **deployment** this is | ⚙ `compose_hash` is in the signed report body at `mr_config_id[1:33]`, and `sha256(tcb_info.app_compose) == compose_hash`, so the quote carries its own authenticated compose file. |
| 3 | That deployment is the one the user reviewed | 👤 The user compares `compose_hash` against the hash of a compose file they read. |
| 4 | **Only the controller can write the ledger** | 👤 read from that reviewed compose: the broker mounts no `/var/run/dstack.sock`; only the controller does. ⚙ Changing that changes `compose_hash`, which link 3 catches. |
| 5 | Which **image** the broker runs | ⚙ RTMR3 is append-only and covered by the signature. Replay the runtime events; the last `zg-image-update` is the reference. Link 4 is why that record can be believed. |
| 6 | That image is one the user recognises | 👤 The digest is compared against a set that ships with **the user's own software**, never fetched from the provider. |
| 7 | The attestation describes **now**, not the past | ⚙ The signer key is derived per-image: `S = KDF(appKey, running image digest)`. An upgrade changes `S`, so the key an old quote names stops working. A stale quote is self-invalidating. |
| 8 | This **response** came from that image | ⚙ Every response carries a signature by `S` over the exact bytes delivered. The controller holds `S` and signs on request; the private key never leaves the controller, so no broker image can retain it across an upgrade. |
| 9 | The router changed nothing and replayed nothing | ⚙ It does not hold `S`. The signature binds this response's bytes, so it cannot be moved to another request. |

Links 1–2, 5, 7–9 are mechanical. Links 3, 4 and 6 are the user's, and they are the same
discipline throughout: **the trust root travels with software the user installed, never
with the party being verified.**

---

## Why an untrusted router does not weaken this

The router sees ciphertext and signatures. It can drop, delay or garble a response — a
liveness failure the user detects — but it cannot:

- alter a response, because it cannot produce a signature under `S`;
- replay an earlier response, because the signature binds this response's bytes;
- substitute an older attestation, because the `S` named by an older quote no longer
  verifies once the image has changed (link 7).

The router therefore needs no trust at all. It is a transport.

---

## Why an upgradeable broker does not weaken this

An in-place upgrade does not change `compose_hash`, so it cannot be seen in the boot
measurements. It is instead made **visible** and **binding**:

- **Visible.** The controller records the new reference in RTMR3 *before* the change, at a
  moment when no broker is running, so the ledger is true at every instant a quote can be
  taken. Every abort path appends the truth rather than leaving a claim standing; when the
  truth cannot be established the record deliberately names no digest, which the reader
  refuses.
- **Binding.** The upgrade changes `S`, so a user still holding the previous attestation
  stops being able to verify responses and must re-read the ledger. An upgrade cannot be
  slipped past a user who is checking.

A modified broker image gains nothing from running our code differently: it cannot write
the ledger (link 4), cannot obtain the previous `S` (link 8), and cannot derive keys itself
because the controller serves `GetQuote` and `Info` but never `GetKey` or `EmitEvent`.

---

## What each party must be trusted for

| Party | Must be honest? |
|---|---|
| Intel TDX + DCAP | **Yes** — the cryptographic root |
| dstack OS image, KMS key-release policy | **Yes** — covered by `mrtd` / `rtmr0-2` and the on-chain policy |
| **Controller image** | **Yes** — but pinned by `compose_hash`, reviewed by the user, and it cannot upgrade itself |
| **Broker image** | **No** ← the objective |
| **Router** | **No** |
| Provider's host, network, DNS | **No** |
| The user's own SDK and digest allowlist | **Yes** — by construction; this is where the trust root lives |

---

## What the user actually does

1. Once, per release: read the compose file, confirm the broker has no dstack socket,
   record `compose_hash` and the acceptable broker digests. Both go into the user's own
   software.
2. Per session: fetch the quote, verify the DCAP signature, check `compose_hash` against
   the recorded one, replay RTMR3, read the last `zg-image-update`, check it against the
   recorded digests, and take the signer address from `report_data`.
3. Per response: verify the signature against that signer address.

Step 3 failing means the image changed. The user returns to step 2 and decides whether to
accept the new digest — which is the intended behaviour, not an error.

---

## Residual assumptions, stated plainly

- **Step 1 cannot be automated away.** Whether the deployment confines RTMR3 writers is a
  property of the compose file. A verifier cannot derive it from the compose text: the ways
  a container can reach a socket are an open set, and the fields that would identify the
  broker are not the ones the controller uses. It is a document review, and
  `compose_hash` makes that review durable for every later version.
- **Confidentiality is forward-looking.** A malicious image running *now* can retain the
  keys it currently holds. What the per-image derivation guarantees is that it cannot use
  them *after* an upgrade, and cannot obtain the keys of any other version.
- **The controller sits in the response path.** It signs every response over a local
  socket. Availability of the controller becomes availability of the service.
- **An upgrade is not transparent to users.** `Service.teeSignerAcknowledged` resets when
  `teeSignerAddress` or `additionalInfo` changes, and recovery is `onlyOwner`. Every
  in-place upgrade therefore needs the contract owner to re-acknowledge. This is the
  intended cost of link 7: an upgrade a user cannot fail to notice.

---

## Implementation status

| Link | Where | Status |
|---|---|---|
| Pull correctness, digest-only upgrade entry point | `controller/internal/{docker,ctrl}` | merged (#622, #624) |
| Controller cannot be widened at runtime or act on itself | `controller/internal/{ctrl,docker}` | merged (#623) |
| Broker holds no docker socket; image identity from the environment | `inference/internal/contract`, `controller/internal/docker` | #625 |
| Ledger: record before the change, append the truth on abort, serialise | `controller/internal/ctrl` | #626 |
| Reader: RTMR3 replay, quote offsets, running-state resolution | `common/attest` | #627 |
| No unrecorded controller action that changes behaviour | `controller/internal/{ctrl,handler}` | #635 |
| An upgraded container stops claiming to match the compose file | `controller/internal/docker` | #643 |
| Controller serves quotes so the broker can drop the dstack socket | `controller/internal/attestproxy` | #644 |
| **Per-image key derivation and controller-side signing (links 7–8)** | `common/tee`, `controller/internal/attestproxy` | **not started** |

Until the last row lands, links 7 and 8 are not in place: the signer key is derived from
`compose_hash` rather than from the running image, so it survives an in-place upgrade and
an attestation taken before that upgrade continues to verify. Everything above it holds,
which means the ledger is truthful — but a user cannot yet tell whether the copy of it
they are reading is current.
