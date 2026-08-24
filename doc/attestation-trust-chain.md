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
| 5 | Which **image** the broker runs | ⚙ RTMR3 is append-only and covered by the signature. Replay the runtime events; the last `zg-image-update` names it, as `<repo>@<digest> <0xsigner> <enc_pub>`. Link 4 is why that record can be believed. |
| 6 | That image is one the user recognises | 👤 The digest is compared against a set the user obtained **for themselves** — built from source they read, or taken from a release they have a reason to trust — never fetched from the provider being checked. |
| 7 | **Both keys belong to that image** | ⚙ The record binds the address of `S = KDF(appKey, "/<digest>/sign")` **and** the public half of the enclave encryption key at `"/<digest>/e2ee-enc"`, both derived by the controller before it makes the change. The reader requires the quote's `report_data` to name the same two. Without this they are unconnected: `report_data` is whatever the enclave asked the hardware to sign over, and both keys are derivable only inside the CVM, so a broker could publish keys of its own and a record left over from a change that never completed would be believed. The enc_pub half is not redundant with the signature — the bound **address is public**, so an unrecorded image can publish it beside its own enc_pub, and a client seals its request before any signature exists to contradict it. |
| 8 | The attestation describes **now**, not the past | ⚙ Because `S` follows the image, an upgrade changes it, so the key an old quote names stops working. A stale quote is self-invalidating — no nonce and no freshness protocol. |
| 9 | This **response** came from that image | ⚙ Every response carries a signature by `S` over the exact bytes delivered. The controller holds `S` and signs on request; the private key never leaves the controller, so no broker image can retain it across an upgrade. |
| 10 | The router changed nothing and replayed nothing | ⚙ It does not hold `S`. The signature binds this response's bytes, so it cannot be moved to another request. |

Links 1–2, 5, 7–10 are mechanical. Links 3, 4 and 6 are the user's, and they are the same
discipline throughout: **the trust root comes from somewhere the user chose, never from the
party being verified.** Where it is kept does not matter — an SDK constant, a config file, two
hex strings in a notes file. Only its provenance does.

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
| Whatever the user compares against — an SDK's allowlist, a file, a written-down pair of hashes | **Yes** — by construction; this is where the trust root lives |

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

   Then keep two values: that compose file's `compose_hash`, and the broker digests you accept.
   Keep them however you like — this is a person with two hex strings, not necessarily a program
   with a config. What matters is only that you got them yourself and that you compare against
   them, rather than reading them out of the attestation you are about to check.
2. Per session: fetch the attestation, hand the quote and the event log to **dstack's own
   verifier**, then make the checks it does not: that `tcb_info` belongs to this quote, that
   `compose_hash` is the one you recorded, that the last `zg-image-update` names a digest you
   accept, and that the signer address it binds equals the one in `report_data`.
   `api/common/attest.ResolveRunningState` does everything after the verifier; the verifier
   call and the digest allowlist are the caller's, because both need inputs only the caller
   has.
3. Per response: verify the signature against that signer address.

Step 3 failing means the image changed. The user returns to step 2 and decides whether to
accept the new digest — which is the intended behaviour, not an error.

---

## Verifying by hand

Written out because the first users reach inference through the router and have no verifier
of their own. Two tools: **dstack's verifier**, run by you, for everything about the platform;
and one Python file for the four things that are ours. Anything that automates this later has
to compute the same values from the same inputs, so this doubles as the specification for it.

**Do not reimplement the platform half.** dstack ships the verifier its own KMS runs before
releasing a CVM's keys, and it makes four independent judgements — DCAP collateral, the RTMR
replay, the guest OS image measurement, the ACPI tables. A hand-rolled subset of those is a
downgrade wearing independence as a costume: it verifies fewer things while feeling like it
verifies more. `api/common/attest` used to carry its own replay and quote offsets and no
longer does, for exactly this reason.

What is left over is small, and it is the part nobody else can do, because it is about *our*
records and *our* keys. Read `api/common/attest` if you want it as maintained code —
`ResolveRunningState` does steps 3 to 5 — but do not treat it as a dependency.

### Once, per release

Two values. Neither has to live in code — a notes file is fine — but both must come from you
rather than from the provider:

1. **`compose_hash` of the deployment you reviewed.** `sha256` of the `app_compose` manifest.
   Reviewing it means checking the four things in step 1 above — the sockets the broker does
   not mount, the controller being the only holder of `dstack.sock`, the controller's own
   image digest, and the exact set of services mounting the attestation-socket volume — and
   one more that is about where your plaintext goes rather than who can write the ledger:
   **`TARGET_URL` and `DATABASE_DSN` in the broker's environment.** The broker unseals a
   request, forwards it to the first and persists async request and response bodies to the
   second, so both are values to read rather than merely to pin.

   Read it as a **literal** in `docker_compose_file`. `compose_hash` is
   `sha256(app_compose)`, and that manifest carries the compose text as submitted — so
   `- TARGET_URL=http://0gm-sglang:8000/v1` is covered, while `- TARGET_URL=${TARGET_URL}`
   covers only the reference and leaves the value in the encrypted-environment channel,
   which nothing measures. The two are indistinguishable from inside the CVM, so this is a
   check only you can make. A deployment that leaves the target in the broker's config file
   is the same case: that file arrives the same way, and the manifest holds
   `BROKER_CONFIG=${BROKER_CONFIG:-}` rather than its content.
2. **The broker image digests you accept.** Built from source you read, or taken from a
   release you have a reason to trust. Never read out of the quote you are about to check.

### Establishing the signer — once, not per request

"Once" is bounded by a signal rather than by time: steps 1–5 produce a signer address, and it
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
# 1. Fetch the attestation, keeping the headers. The body's fields are not equally
#    trustworthy on arrival: quote and event_log earn their standing in step 2, and
#    ZG-Quote-Signature is what step 6 uses for the rest.
curl -s -D h.txt "$PROVIDER/v1/quote?legacy=false" > q.json
```

`$PROVIDER` is the broker's own base URL. Through the router, use whichever path the router
maps to that provider — the router carries the quote and cannot alter it (link 1), so where
you fetch it from does not matter to the result.

That last sentence is about the quote. It does not extend to every field beside it:
`nvidia_payload` is appended by this project rather than dstack, and nothing in the quote
covers it. `ZG-Quote-Signature` is a signature by the key `report_data` binds over the exact
body bytes, which is what makes the rest of the response as unalterable in transit as the
quote already was. Step 6 checks it, and a body served without the header is a body whose
`nvidia_payload` says nothing.

```bash
# 2. Hand the quote and the event log to dstack's verifier. Run it yourself — a verifier the
#    provider hosts decides nothing, since the answer is what is being checked.
docker run -d -p 8080:8080 dstacktee/dstack-verifier:0.5.11
curl -s -X POST localhost:8080/verify -H 'Content-Type: application/json' \
     --data-binary @<(jq '{quote, event_log, vm_config}' q.json) > v.json
```

`vm_config` is in that body because the verifier needs it to know which published guest image
to measure against. Omit it and the request still returns 200, but `is_valid`,
`event_log_verified` and `os_image_hash_verified` all come back false, `app_info` is null, and
the reason names a 404 for `os-images/mr_.tar.gz` — a failure that reads as a finding about the
provider while being an incomplete request.

Pick the verifier by what it has to recognise, not by what the CVM runs. It fetches images by
the measurements it knows, so one older than the image it is judging fails to build the
attestation at all. A newer verifier handles older images, so err newer: `0.5.11` verifies a
0.5.9 CVM. Matching the CVM's version exactly is the one thing that will not work, because the
request schema moved between releases — 0.5.9 rejects the body above with 422.

The request schema is the verifier's own, and it moves with its releases — read them rather
than this file. What does not move is the set of fields whose values you must require, because
each one is a link that fails silently if you skip it:

| field | require | what accepting anything else admits |
|---|---|---|
| `quote_verified` | `true` | that the quote is a real TDX quote from genuine Intel hardware — everything else is arithmetic on numbers a provider could have typed |
| `event_log_verified` | `true` | that the log replays onto the quote's registers. The log arrives over plain HTTP from the party being described; this is the only thing that makes it evidence, and steps 3–5 read records out of **exactly the bytes you sent here** |
| `tcb_status` | `UpToDate` | a platform with a published unpatched vulnerability. Any other value comes with `advisory_ids` and is a decision to make deliberately, not a pass to wave through |
| `os_image_hash_verified` | `true` | a guest OS the provider built rather than a published dstack image |
| `app_info.key_provider_info` | the KMS you expect | a CVM whose keys came from somewhere other than the KMS whose on-chain policy you read. Hex-encoded JSON; decode it and read `id` |

Everything above sits under `details` except `is_valid`, and `key_provider_info` is one level
deeper still, under `details.app_info`.

`tcb_status` and `os_image_hash_verified` are two checks this project never made when it
verified quotes itself. Getting them is the reason the recommendation changed.

**Two things the verifier does not tell you, so do not write a check that appears to ask.**
There is no `os_image_is_dev` and no `acpi_tables_verified` in the response — a `.get()` for
either returns a default and passes, which is worse than not looking. For the first, compare
`details.app_info.os_image_hash` against the dstack release whose image you expect: a
development image is a different published hash, not a flag. The second has no substitute
here; `quote_verified` covers the DCAP chain and nothing in the response speaks to the ACPI
tables separately.

```python
# 3-5. verify.py — the checks the verifier does not make, because they are about our records.
import hashlib, json

V, Q = json.load(open("v.json")), json.load(open("q.json"))
D = V["details"]                                           # everything but is_valid lives here
EVENT_TYPE = 0x08000001

assert V["is_valid"], V.get("reason")                      # the verdict, not just its parts
assert D["quote_verified"] and D["event_log_verified"]     # plus the rest of the table above

# --- 3. Is tcb_info this quote's, and is this the deployment you reviewed? ---
# The verifier is never handed app_compose — its request carries the quote and the log —
# so this line is the only thing tying the manifest you are about to read to an attested
# hash. Skip it and the compose file degrades to an unauthenticated claim by the provider,
# which is precisely the thing step 1's review was supposed to have settled.
compose_hash = D["app_info"]["compose_hash"]
app_compose = json.loads(Q["tcb_info"])["app_compose"]
assert hashlib.sha256(app_compose.encode()).hexdigest() == compose_hash, "tcb_info is not this quote's"
assert compose_hash == REVIEWED_COMPOSE_HASH, f"unreviewed deployment: {compose_hash}"

# --- 4. Which image, and is it one you accept? ---
# Two filters, and both narrow what may be read as ours. event_log_verified covers all four
# registers, so an entry sitting in IMR 0-2 is just as "verified" as one in 3 — but only 3 is
# extendable after boot, so only 3 can carry a record a container wrote.
events = [e for e in json.loads(Q["event_log"])
          if e["event_type"] == EVENT_TYPE and e["imr"] == 3]
# And only entries after system-ready can have been written by a container; reading the whole
# register would let a record placed among the boot events be taken for ours.
ledger = events[next(i for i, e in enumerate(events) if e["event"] == "system-ready") + 1 :]
records = [e for e in ledger if e["event"] == "zg-image-update"]

if records:
    # Three fields: the reference, the response-signing address, and the enclave encryption
    # public key. Both keys, because report_data carries both — see step 5.
    ref, bound_signer, bound_enc_pub = bytes.fromhex(records[-1]["event_payload"]).decode().split()
    source = "ledger"
else:
    # Nothing recorded since boot, so the broker is on the image compose pins — and that file
    # is trustworthy now, because step 3 anchored it to the quote. RTMR3 resets on every boot,
    # so this is the normal state and not an anomaly.
    import yaml
    compose = yaml.safe_load(json.loads(app_compose)["docker_compose_file"])
    ref = compose["services"]["0g-serving-provider-broker"]["image"]
    bound_signer, bound_enc_pub, source = None, None, "compose"

# A reference naming a tag says which name was asked for, not which image answers. Refuse
# rather than resolve it: a tag is resolved by the provider's daemon, which is the party being
# checked. A deployment whose compose pins `:latest` or `:dev1` therefore cannot be verified
# at all until it either pins a digest or records an upgrade — and that is the state of
# today's production deployments, so expect to stop here on one.
assert "@" in ref, f"the {source} names {ref!r}, which pins no digest — refuse"
digest = ref.split("@", 1)[1]

assert digest in ACCEPTED_DIGESTS, f"unreviewed image: {digest}"

# --- 5. Do the keys belong to that image? Both of them. ---
# report_data comes back from the verifier, which read it out of the quote it just verified.
# These 64 bytes are the one part of the quote's layout this file still has to know, because
# their contents are ours (0g-pc SPEC 4.2) rather than dstack's.
rd = bytes.fromhex(D["report_data"].removeprefix("0x"))
if int.from_bytes(rd[52:56], "big") == 1:          # the enc_pub-binding layout
    signer = "0x" + rd[32:52].hex()
else:                                              # the older layout: the ASCII address
    signer = rd.rstrip(b"\x00").decode().lower()

if bound_signer:
    assert signer == bound_signer.lower(), f"the ledger binds {bound_signer}, the quote names {signer}"

    # And the enc_pub, which is the half a signature check cannot rescue. The address the
    # ledger binds is public — it is in the event log you just read — so an image that is not
    # the recorded one can publish that address beside an enc_pub of its own. It could never
    # sign a response, but you would already have sealed your request to its key.
    if int.from_bytes(rd[52:56], "big") == 1:
        assert rd[:32].hex() == bound_enc_pub.lower(), \
            f"the ledger binds enc_pub {bound_enc_pub}, the quote names {rd[:32].hex()}"
        seal_to = rd[:32].hex()
    else:
        # The older layout carries no enc_pub, so there is nothing to check. Do not seal
        # anything using it: fetch the §4.2 quote instead.
        seal_to = None
else:
    seal_to = None

# --- 6. Is the rest of this response the measured image's, and not the path's? ---
# The quote and the event log needed nothing here: DCAP signs one and step 2 replays the
# other into it. nvidia_payload has neither. It is appended by this project beside dstack's
# fields, so a router — the one the trust chain deliberately does not trust — can put a
# different genuine GPU's evidence in its place.
#
# The nonce does not settle that on its own. It is keccak256(report_data), report_data is
# public, and any owner of a confidential-mode GPU can have theirs sign that nonce. What
# ties the evidence to this CVM is that the code collecting it runs here and is measured,
# and this signature is what distinguishes the bytes that code emitted from any others.
from eth_keys import KeyAPI            # any secp256k1 recovery will do
from eth_utils import keccak            # keccak, not sha256

# The personal_sign prefix is applied to the body's keccak DIGEST, not to the body. A
# caller reaching for eth_account.recover_message(encode_defunct(RAW_BODY)) recovers a
# different address and finds nothing wrong with its own code.
sig = bytes.fromhex(HEADERS["ZG-Quote-Signature"].removeprefix("0x"))
signed = keccak(b"\x19Ethereum Signed Message:\n32" + keccak(RAW_BODY))
recovered = KeyAPI().ecdsa_recover(signed, KeyAPI.Signature(sig[:64] + bytes([sig[64] - 27])))
assert recovered.to_address() == signer, "response body is not this signer's"

# Only now does the GPU evidence describe this deployment's GPU. Verifying the evidence
# itself is separate and needs NVIDIA's tools; what this establishes is whose evidence it is.
#
# The nonce is keccak256 of the report_data *handed to* the quote, and the two layouts hand
# over different lengths. §4.2 hands over all 64 bytes. The older layout hands over the
# 42-byte ASCII address and the hardware zero-pads it, so hashing the 64 bytes that come
# back gives an answer that never matches — an honest provider failing a check that was
# computed wrongly.
gpu_nonce = (Q.get("nvidia_payload") or {}).get("nonce")
if gpu_nonce:
    challenged = rd if int.from_bytes(rd[52:56], "big") == 1 else rd[:42]
    assert gpu_nonce == keccak(challenged).hex(), "GPU evidence was raised for another quote"

print(f"image {digest} (from the {source}), responses signed by {signer}, seal to {seal_to}")
```

`RAW_BODY` is the bytes of `q.json` as received, not a re-serialisation of the parsed object:
the signature is over what was sent. `HEADERS` is `h.txt` from step 1.

A response with no `ZG-Quote-Signature` is not a failure of the platform half — the quote,
the event log, the compose hash and both keys all still verify, because each of those carries
its own proof. What is lost is `nvidia_payload`, and with it any statement about the GPU.

Step 5 is the one that makes the digest a statement about the running process rather than
about an installation, and the two halves fail in different directions. The signer check is
about **authenticity**, and it can be made after the fact: a bad signature refuses a response
you have already read. The enc_pub check is about **confidentiality**, and it cannot — you seal
the request first, so a wrong key means the plaintext is gone before anything could object. That
is why a record binding only one key is refused rather than half-checked.

Skipping either leaves `report_data` and the ledger unconnected, and a
divergence between them — a broker publishing keys of its own, or a record left over
from a change that never finished — accepted rather than refused.

When there is no record, there is no bound address to compare, and the property is weaker by
exactly that much: the image is pinned by hardware through `compose_hash`, but nothing vouches
separately for the key. That is sound because becoming a different image requires a change,
and a change cannot happen unrecorded — see the residual assumption about the compose pin.

### Per response

One signature recovery against the address step 5 established. No quote, no replay, no DCAP.

A failure here is not an error to retry through. It is the notification that the image changed,
and the only thing to do with it is go back and establish the signer again — which puts the
question link 6 exists for, *is this a digest I accept*, in front of a person at the moment it
becomes live.

```bash
# 6. Make the request and keep the handle the broker returns.
KEY=$(curl -si "$PROVIDER/v1/proxy/$PROVIDER_ADDR/chat/completions" \
        -H 'Content-Type: application/json' -d @req.json \
        -D >(grep -i '^ZG-Res-Key:' | cut -d' ' -f2 | tr -d '\r' >&2) -o resp.json 2>&1 >/dev/null)

# 7. Fetch the TEE signature over that exchange.
curl -s "$PROVIDER/v1/proxy/signature/$KEY" > sig.json   # {text, signature, signing_address}
```

```python
# 8. Two checks, and both matter.
from eth_account.messages import encode_defunct
from eth_account import Account
import hashlib, json

sig = json.load(open("sig.json"))

# a. The signature is by the key step 5 established — not by whatever address the response
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

Run against a current production CVM's attestation, this gets through step 3 —
`sha256(app_compose)` matches the compose hash in the signed report body, and the event log
replays onto the quote's RTMR3 exactly — and then **refuses at step 4**, because that
deployment's compose names `ghcr.io/0gfoundation/0g-serving-broker:dev1` and a tag is resolved
by the provider's own daemon.

Both of those were checked against a real report, by hand, before the verifier became the
recommendation: the anchor holds and the replay lands. What has not been run end to end is
`POST /verify` against that same report, so treat the platform half's four extra judgements as
the reason to use it rather than as something this file has confirmed for our deployments.

The refusal point is the shortest description of what this whole series changes. The mechanical
parts already work today; what is missing is a deployment that pins what it runs. After it
regenerates its compose with a controller, step 4 answers from the ledger and step 5 has an
address to compare.

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
| Broker holds no docker socket; image identity from the environment | `inference/internal/contract`, `controller/internal/docker` | merged (#625) |
| Ledger: record before the change, append the truth on abort, serialise | `controller/internal/ctrl` | merged (#626) |
| Reader: running-state resolution on top of a verified quote | `common/attest` | merged (#627) |
| No unrecorded controller action that changes behaviour | `controller/internal/{ctrl,handler}` | merged (#635) |
| Controller serves quotes so the broker can drop the dstack socket | `controller/internal/attestproxy` | merged (#644) |
| Per-image key derivation and controller-side signing (links 8–9) | `common/tee`, `controller/internal/attestproxy` | merged (#648) |
| The record binds the signer address; the reader requires the quote to match (link 7) | `common/attest`, `controller/internal/ctrl` | merged (#649) |
| The generated deployment actually withholds both sockets from the broker | `inference/integration/config` | merged (#650) |
| An upgraded container stops claiming to match the compose file | `controller/internal/docker` | merged (#652) |

Every link now has code. Two things the code cannot supply:

- **Nothing in this repository calls `ResolveRunningState`.** It is a library; the chain
  terminates in the client, and a client that skips step 2 gets none of links 5–8. Whatever
  ships as the verifier has to call dstack's verifier and require every field in the table
  above, then make the binding comparison, and must not cache a signer address across
  sessions — caching one is exactly how a stale attestation stops self-invalidating.
- **The KMS's `compose_hash` authorization policy is outside this repository.** The app key
  follows a persisted `app_id`, not `compose_hash`, so redeploying with a different compose
  file re-derives the same app key. Only the KMS's check at CVM registration gates that;
  `S` alone cannot detect it.
