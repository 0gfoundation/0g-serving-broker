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
| 5 | Which **image** the broker runs | ⚙ RTMR3 is append-only and covered by the signature. Replay the runtime events; the last `zg-image-update` names it, as `<repo>@<digest> <0xsigner>`. Link 4 is why that record can be believed. |
| 6 | That image is one the user recognises | 👤 The digest is compared against a set that ships with **the user's own software**, never fetched from the provider. |
| 7 | The key signing responses **belongs to that image** | ⚙ The record binds the address of `S = KDF(appKey, "/<digest>/sign")`, derived by the controller before it makes the change. The reader requires the quote's `report_data` to name the same address. Without this the two are unconnected: `report_data` is whatever the enclave asked the hardware to sign over, and `S` is derivable only inside the CVM, so a broker could publish an address of its own and a record left over from a change that never completed would be believed. |
| 8 | The attestation describes **now**, not the past | ⚙ Because `S` follows the image, an upgrade changes it, so the key an old quote names stops working. A stale quote is self-invalidating — no nonce and no freshness protocol. |
| 9 | This **response** came from that image | ⚙ Every response carries a signature by `S` over the exact bytes delivered. The controller holds `S` and signs on request; the private key never leaves the controller, so no broker image can retain it across an upgrade. |
| 10 | The router changed nothing and replayed nothing | ⚙ It does not hold `S`. The signature binds this response's bytes, so it cannot be moved to another request. |

Links 1–2, 5, 7–10 are mechanical. Links 3, 4 and 6 are the user's, and they are the same
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

1. **Once, per release: read the compose file.** Four things in it are load-bearing, and
   missing any one of them voids a different link:

   - the **broker** and **event** services mount neither `/var/run/dstack.sock` (link 4 —
     that socket also serves `GetKey`, so holding it means being able to derive any image's
     signing key) nor `/var/run/docker.sock` (without which nothing in the file can say who
     may change what);
   - the **controller** is the only service that mounts `dstack.sock`;
   - the controller's `image:` is a digest the user has also reviewed. It derives the keys
     and holds the docker socket, so an unreviewed controller can run any broker image and
     write a record that is internally consistent with it. Nothing measures the controller's
     own image — the compose file is what pins it;
   - **no service other than the broker and the event service mounts the volume carrying the
     controller's attestation socket** (`zg-tee`). That socket signs any 32-byte hash under
     the broker image's key, so a fourth container mounting it can mint response proofs
     attributed to a reviewed image, and nothing in RTMR3 records that container's existence.

   Then record `compose_hash` and the acceptable broker digests into the user's own software.
2. Per session: fetch the quote, verify the DCAP signature, check `compose_hash` against
   the recorded one, replay RTMR3, read the last `zg-image-update`, check its digest against
   the recorded set, and check that the signer address it binds equals the one in
   `report_data`. `api/common/attest.ResolveRunningState` does the replay and that
   comparison; the DCAP verification and the digest allowlist are the caller's, because both
   need inputs only the caller has.
3. Per response: verify the signature against that signer address.

Step 3 failing means the image changed. The user returns to step 2 and decides whether to
accept the new digest — which is the intended behaviour, not an error.

---

## Verifying by hand

Written out because the first users reach inference through the router and have no verifier
of their own. Nothing below needs a library: one `curl`, one Python file, and Intel's own
quote-verification tool. Anything that automates this later has to compute the same values
from the same three inputs, so this doubles as the specification for it.

Read `api/common/attest` if you want the same logic as maintained code — `ResolveRunningState`
does steps 3 to 6 — but do not treat it as a dependency. The point of this section is that a
user can settle every mechanical link with tools they already trust.

### Once, per release

Two values, and both must come from software you installed rather than from the provider:

1. **`compose_hash` of the deployment you reviewed.** `sha256` of the `app_compose` manifest.
   Reviewing it means checking the four things in step 1 above — the sockets the broker does
   not mount, the controller being the only holder of `dstack.sock`, the controller's own
   image digest, and the exact set of services mounting the attestation-socket volume.
2. **The broker image digests you accept.** Built from source you read, or taken from a
   release you have a reason to trust. Never read out of the quote you are about to check.

### Establishing the signer — once, not per request

"Once" is bounded by a signal rather than by time: steps 1–6 produce a signer address, and it
stays good until a response signature fails to verify against it. There is no session object to
hold and no interval to pick, which matters because the first callers are plain API clients
with neither.

Reusing the address is not a cost/safety trade — it is safe, and for a reason worth stating
because everything above exists to produce it. Suppose the provider upgrades an hour after you
verified. The controller derives from the image that is now running, so it signs with a
different key; the upgrade restarted the broker, so its quote names that key too. Your very
next response therefore fails to verify. **The window in which you are unaware the image
changed is zero responses**, so re-fetching a quote per request buys nothing but a DCAP
verification you did not need.

That holds only because the key follows the image. If one key served every version — which is
what a deployment without per-image derivation has — a reused address would keep verifying
across an upgrade indefinitely, and re-fetching the quote would not help either, since the
address in it would not have changed.

What this is NOT sensitive to: a config change. `zg-config-update` does not move the signer
address, so a change recorded while you are reusing one goes unnoticed. Re-reading the quote is
the only way to see it, and that is the one thing per-request verification would actually buy.

```bash
# 1. Fetch the attestation. Three values arrive together; only the first is trustworthy on
#    its own, and steps 3 and 4 are what earn the other two.
curl -s "$PROVIDER/v1/quote?legacy=true" > q.json     # {quote, event_log, tcb_info}
```

`$PROVIDER` is the broker's own base URL. Through the router, use whichever path the router
maps to that provider — the router carries the quote and cannot alter it (link 1), so where
you fetch it from does not matter to the result.

```bash
# 2. Verify the DCAP signature. Use Intel's tooling or dcap-qvl; do not reimplement it.
#    Everything after this treats the quote's bytes as hardware-attested.
jq -r .quote q.json | xxd -r -p > quote.bin
dcap-qvl verify quote.bin        # or Intel's QVL / a service you trust
```

```python
# 3-6. verify.py — the four checks that need the quote's own bytes.
import hashlib, json, sys

Q, EVENT_TYPE = json.load(open("q.json")), 0x08000001
quote = bytes.fromhex(Q["quote"].removeprefix("0x"))

# Byte offsets into a TDX v4 quote. Fixed by the format; a v5 quote fails step 3 rather
# than being misread, because nothing else lands on these boundaries.
MR_CONFIG_ID, RTMR0, RTMR_STRIDE, REPORT_DATA = 232, 376, 48, 568

# --- 3. Which deployment is this, and is it the one you reviewed? ---
# mr_config_id is 0x01 followed by the compose hash, so the quote carries its own
# authenticated compose file once the next line checks the manifest against it.
compose_hash = quote[MR_CONFIG_ID + 1 : MR_CONFIG_ID + 33].hex()
app_compose = json.loads(Q["tcb_info"])["app_compose"]
assert hashlib.sha256(app_compose.encode()).hexdigest() == compose_hash, "tcb_info is not this quote's"
assert compose_hash == REVIEWED_COMPOSE_HASH, f"unreviewed deployment: {compose_hash}"

# --- 4. Replay RTMR3 and require the quote's value. ---
# This is what makes the event log believable: it arrives over plain HTTP from the party
# being described, and only a replay that lands on a hardware register redeems it.
mr = bytes(48)
events = [e for e in json.loads(Q["event_log"]) if e["event_type"] == EVENT_TYPE]
for e in events:
    payload = bytes.fromhex(e["event_payload"])
    digest = hashlib.sha384(
        EVENT_TYPE.to_bytes(4, "little") + b":" + e["event"].encode() + b":" + payload
    ).digest()
    mr = hashlib.sha384(mr + digest).digest()
assert mr == quote[RTMR0 + 3 * RTMR_STRIDE : RTMR0 + 4 * RTMR_STRIDE], "the event log is not this quote's"

# --- 5. Which image, and is it one you accept? ---
# Only entries after system-ready can have been written by a container; reading the whole
# log would let a record placed among the boot events be taken for ours.
ledger = events[next(i for i, e in enumerate(events) if e["event"] == "system-ready") + 1 :]
records = [e for e in ledger if e["event"] == "zg-image-update"]

if records:
    ref, bound_signer = bytes.fromhex(records[-1]["event_payload"]).decode().split()
    source = "ledger"
else:
    # Nothing recorded since boot, so the broker is on the image compose pins — and that file
    # is trustworthy now, because step 3 anchored it to the quote. RTMR3 resets on every boot,
    # so this is the normal state and not an anomaly.
    import yaml
    compose = yaml.safe_load(json.loads(app_compose)["docker_compose_file"])
    ref = compose["services"]["0g-serving-provider-broker"]["image"]
    bound_signer, source = None, "compose"

# A reference naming a tag says which name was asked for, not which image answers. Refuse
# rather than resolve it: a tag is resolved by the provider's daemon, which is the party being
# checked. A deployment whose compose pins `:latest` or `:dev1` therefore cannot be verified
# at all until it either pins a digest or records an upgrade — and that is the state of
# today's production deployments, so expect to stop here on one.
assert "@" in ref, f"the {source} names {ref!r}, which pins no digest — refuse"
digest = ref.split("@", 1)[1]

assert digest in ACCEPTED_DIGESTS, f"unreviewed image: {digest}"

# --- 6. Does the key signing responses belong to that image? ---
rd = quote[REPORT_DATA : REPORT_DATA + 64]
if int.from_bytes(rd[52:56], "big") == 1:          # the enc_pub-binding layout
    signer = "0x" + rd[32:52].hex()
else:                                              # the older layout: the ASCII address
    signer = rd.rstrip(b"\x00").decode().lower()

if bound_signer:
    assert signer == bound_signer.lower(), f"the ledger binds {bound_signer}, the quote names {signer}"
print(f"image {digest} (from the {source}), responses signed by {signer}")
```

Step 6 is the one that makes the digest a statement about the running process rather than
about an installation. Skipping it leaves `report_data` and the ledger unconnected, and a
divergence between them — a broker publishing an address of its own, or a record left over
from a change that never finished — accepted rather than refused.

When there is no record, there is no bound address to compare, and the property is weaker by
exactly that much: the image is pinned by hardware through `compose_hash`, but nothing vouches
separately for the key. That is sound because becoming a different image requires a change,
and a change cannot happen unrecorded — see the residual assumption about the compose pin.

### Per response

One signature recovery against the address step 6 established. No quote, no replay, no DCAP.

A failure here is not an error to retry through. It is the notification that the image changed,
and the only thing to do with it is go back and establish the signer again — which puts the
question link 6 exists for, *is this a digest I accept*, in front of a person at the moment it
becomes live.

```bash
# 7. Make the request and keep the handle the broker returns.
KEY=$(curl -si "$PROVIDER/v1/proxy/$PROVIDER_ADDR/chat/completions" \
        -H 'Content-Type: application/json' -d @req.json \
        -D >(grep -i '^ZG-Res-Key:' | cut -d' ' -f2 | tr -d '\r' >&2) -o resp.json 2>&1 >/dev/null)

# 8. Fetch the TEE signature over that exchange.
curl -s "$PROVIDER/v1/proxy/signature/$KEY" > sig.json   # {text, signature, signing_address}
```

```python
# 9. Two checks, and both matter.
from eth_account.messages import encode_defunct
from eth_account import Account
import hashlib, json

sig = json.load(open("sig.json"))

# a. The signature is by the key step 6 established — not by whatever address the response
#    happens to carry. Reading signing_address and verifying against it proves nothing.
assert Account.recover_message(encode_defunct(text=sig["text"]), signature=sig["signature"]).lower() == signer

# b. The signed text is over the bytes you actually exchanged, so the signature cannot be
#    moved to another request. For a plain chat completion it is
#    sha256(request):sha256(response); an E2EE response signs the sealed bytes instead, and
#    the client that decrypted them already holds exactly those.
expected = hashlib.sha256(open("req.json","rb").read()).hexdigest() + ":" + \
           hashlib.sha256(open("resp.json","rb").read()).hexdigest()
assert sig["text"] == expected, f"signed {sig['text']}, exchanged {expected}"
```

### What this produces on a deployment today

Run against a current production CVM's attestation, the script above gets through steps 3 and
4 — `sha256(app_compose)` matches the compose hash in the signed report body, and replaying the
event log reproduces the quote's RTMR3 exactly — and then **refuses at step 5**, because that
deployment's compose names `ghcr.io/0gfoundation/0g-serving-broker:dev1` and a tag is resolved
by the provider's own daemon.

That is the correct outcome, and it is the shortest description of what this whole series
changes. The mechanical parts already work today; what is missing is a deployment that pins
what it runs. After it regenerates its compose with a controller, step 5 answers from the
ledger and step 6 has an address to compare.

### Two things a manual verifier must not do

- **Do not write a signer address into configuration.** Holding one in memory and reusing it
  is correct and expected. Baking it into a config file, a constant or a deploy artefact is
  not, because then a verification failure gets "fixed" by updating the constant — and at that
  point nothing ever fetches a quote again, the address stops being something a quote
  established, and the one mechanism that makes a stale attestation self-invalidating is gone.
  The address is a cached derivation, not a setting.
- **Do not read the digest, the signer address, or the compose hash from anywhere but the
  quote.** The on-chain `additionalInfo.ImageDigest` and `teeSignerAddress` are written by the
  provider. They are useful for discovery, and they are not evidence.

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
- **With no record at all, the answer rests on the compose pin alone.** RTMR3 resets on
  every CVM boot, so a deployment that has not been upgraded since it booted has an empty
  ledger, and the reader answers with the digest the compose file pins. That path binds no
  signer address — there is none recorded — so it is only sound if the pinned image really
  is the running one. What makes that true is the controller invalidating the recreated
  container's `com.docker.compose.config-hash` label: without it, `docker compose up` after
  a reboot would leave an in-band upgrade in place while the ledger was empty, and the
  reader would report the reviewed digest while unreviewed code answered. That is why the
  label invalidation is part of this chain and not a tidiness fix.
- **A reboot reverts an in-place upgrade.** It follows from the point above: after a reboot
  the container comes back on the image the compose file pins. So an in-place upgrade is a
  way to change what runs *until the next boot*, not a way to change the deployment. Making
  one persist means a new compose file, which changes `compose_hash` and is therefore a
  change the user sees at link 3. The revert also changes the signer address back, which
  resets the acknowledgement a second time.
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
| Per-image key derivation and controller-side signing (links 8–9) | `common/tee`, `controller/internal/attestproxy` | #648 |
| The record binds the signer address; the reader requires the quote to match (link 7) | `common/attest`, `controller/internal/ctrl` | #649 |
| The generated deployment actually withholds both sockets from the broker | `inference/integration/config` | #650 |

Every link now has code. Two things the code cannot supply:

- **Nothing in this repository calls `ResolveRunningState`.** It is a library; the chain
  terminates in the client, and a client that skips step 2 gets none of links 5–8. Whatever
  ships as the verifier has to run the replay and the binding comparison, and must not cache
  a signer address across sessions — caching one is exactly how a stale attestation stops
  self-invalidating.
- **The KMS's `compose_hash` authorization policy is outside this repository.** The app key
  follows a persisted `app_id`, not `compose_hash`, so redeploying with a different compose
  file re-derives the same app key. Only the KMS's check at CVM registration gates that;
  `S` alone cannot detect it.
