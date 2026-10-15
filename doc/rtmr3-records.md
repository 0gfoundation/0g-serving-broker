# RTMR3 records: what the controller writes, and how a verifier reads them

This is the normative description of the records a 0G controller appends to a dstack CVM's
RTMR3, and of the steps a verifier takes to turn a provider's `GET /v1/quote` reply into a
statement about what that CVM is running. It exists so that a verifier can be written
**without importing this repository**: a reader that depends on the code of the party it
checks is not independent of it. `api/common/attest` is the reference implementation;
`api/common/attest/testdata/vectors/` holds real quote replies with the values a reader must
recover from them, and an independent implementation is correct when it agrees with those
files, not when it agrees with the Go code.

Everything below is about the **primary** broker of a CVM. `zg-instance-config-update` (extra
broker instances, `ZG_CONTROLLER_INSTANCES`) is described at the end.

## 1. Inputs, and which of them are trusted

`GET <Service.url>/v1/quote?legacy=false` returns:

| field | what it is | trust |
|---|---|---|
| `quote` | TDX v4 quote, hex | **signed** by the TDX hardware chain (DCAP). The only trust root. |
| `report_data` | the quote's 64 report_data bytes | redundant with `quote[568:632]`; use the quote |
| `event_log` | the CVM's event log in dstack's **GetQuote form** (see §3) | unsigned; worth nothing until replayed into the quote's RTMR3 |
| `tcb_info` | dstack `/Info`: `app_compose`, `compose_hash`, `mrtd`, `rtmr0..3`, `event_log` (full form), `os_image_hash` | unsigned; `app_compose` is worth nothing until it hashes to the compose hash the quote binds |
| `nvidia_payload` | GPU evidence | out of scope here |

The quote body (TD report) starts at byte 48. Within the quote:

| bytes | field |
|---|---|
| 232–280 | `mr_config_id` — byte 0 is a layout tag (`0x01`), bytes 1–33 are **sha256(app_compose)** = `compose_hash` |
| 328–376 / 376–424 / 424–472 | RTMR0 / RTMR1 / RTMR2 (boot chain; compare to an allowlist of published dstack images) |
| **520–568** | **RTMR3** |
| 568–632 | `report_data` (§5) |

## 2. Steps, in order

1. **Verify the quote** (DCAP: certificate chain, TCB status, advisories). Nothing below is
   meaningful on an unverified quote.
2. **Anchor the event log**: replay every RTMR3 entry of `event_log` (§3) and require the
   result to equal `quote[520:568]`. If it does not, stop: this is not the log the CVM wrote.
3. **Anchor app_compose**: require `sha256(tcb_info.app_compose) == compose_hash` and
   `compose_hash == quote[233:265]`. If it does not, stop.
4. **Take the records after `system-ready`** (§4). Entries before it are dstack's own boot
   facts; a `zg-` name among them was not written by a container and must be ignored.
5. **Read the last record of each kind** (§4.1–4.4). Earlier records of the same kind are
   history, not state. An unknown `zg-` name means the reader is older than the writer: refuse,
   do not skip.
6. **Bind the keys**: the signer and enc_pub named by the last `zg-image-update` must equal
   the ones in `report_data` (§5); the signer must equal the provider's on-chain
   `teeSignerAddress`, acknowledged.
7. Apply policy (which digests are released, which images are reviewed, which upstreams are
   inside the CVM) — that is the verifier's own list and never comes from the provider.

## 3. The event log and its replay

Each entry is a JSON object:

```json
{"imr": 3, "event_type": 134217729, "digest": "…", "event": "zg-upstream-set", "event_payload": "…hex…"}
```

- `imr`: register index 0–3. Only `imr == 3` entries can have been written after boot.
- `event_type`: `134217729` = `0x08000001` for every runtime (application-written) entry.
- `event`: the name. `event_payload`: hex of the raw payload bytes.
- `digest`: the SHA-384 dstack extended the register with — **do not use it**, recompute it.

**dstack's GetQuote form omits fields.** In the `event_log` served next to the quote, RTMR3
entries carry `event_payload` and an **empty `digest`**; the firmware's RTMR0–2 entries carry
`digest` and an empty payload. `tcb_info.event_log` is the full form with both. A reader that
folds the declared `digest` therefore replays the full copy and silently fails on the served
one. Recompute:

```
digest(entry) = SHA-384( event_type as 4 bytes little-endian ‖ ":" ‖ event ‖ ":" ‖ payload )
RTMR3         = fold over entries with imr == 3, in log order,
                starting from 48 zero bytes: rtmr = SHA-384( rtmr ‖ digest(entry) )
```

(cc-eventlog writes `event_type.to_ne_bytes()`; every CVM this applies to is x86-64, so
native order is little-endian.) Vector check: `testdata/vectors/*/quote_response.json` →
`expected.json: quote_rtmr3`, from both the served `event_log` and `tcb_info.event_log`.

RTMR0–2 replay with the declared digests (no payloads to recompute from); they are compared
to the boot-chain allowlist, not read for records.

## 4. The records

All names start with `zg-`. Payloads are raw bytes, never JSON (a reader must reproduce them
byte for byte, and JSON leaves key order and escaping free). The writer records **before** it
makes the change the record describes, so a record is a promise about the next state; a
change that aborts after the record is written is visible as the next record (or as a
later `invalidated`/`unknown`, see below).

### 4.1 `zg-image-update`

```
<repo>@sha256:<64 lowercase hex> <0x + 40 lowercase hex signer> <64 lowercase hex enc_pub>
```

Three space-separated fields: the image reference the broker and event service run on
(always a digest, never a tag, always in the controller's configured repository), then the
response-signing address and the X25519 encryption public key derived for that image (and,
since the upstream set is recorded, for that image **and** the current upstream set — §4.3).

- Last record decides `broker_digest`, `broker_signer`, `broker_enc_pub`.
- No record at all (a CVM never upgraded or re-bound): the broker digest is the one
  `app_compose` pins for the broker service, and there is no recorded signer to bind;
  `digest_source` is `compose` instead of `event`.
- The controller also writes one at boot, when it binds keys to the upstream set, which is
  why a fresh CVM with `recordUpstreamSet` on already carries one.

### 4.2 `zg-config-update`

```
<64 lowercase hex> = sha256(bytes of the new broker config file)
```

Written before the controller replaces the primary broker's config file. The content is not
measured anywhere else; a verifier can only compare this hash to a config it has reviewed.
Where the plaintext goes is **not** read from here but from §4.3.

### 4.3 `zg-upstream-set`

```
count=<n>
<name> <base URL>
<name> <base URL> <identity>
…                               (exactly n member lines)
```

or the single word

```
invalidated
```

The **whole** set of destinations the primary broker may forward unsealed plaintext to:
`service.targetUrl` plus every `modelPricing[].targetUrl`, deduplicated by name. A snapshot,
not a delta: the last record is the set.

- `count=<n>` is the first line; the number of member lines must equal `n` (a tally mismatch
  makes the record unreadable).
- `<name>`: `[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?` — lowercase, 63 bytes max. Derived from the
  URL's host unless the config gives `providerIdentity`, in which case that.
- `<base URL>`: `http` or `https`, lowercase scheme and host, ASCII host, no userinfo, no
  query, no fragment, no default port spelled out, no trailing slash, no bare `/` path, no
  dot segments, no percent-encoding in the path. (The writer refuses to record anything else,
  so a reader may refuse too.)
- `<identity>`: optional third field, the vendor identity the config names for that upstream.
- Line cap 4096 bytes; member cap 1024.
- `invalidated`: the writer could not state the set (config unreadable, grammar it cannot
  express). A reader treats the state as **unknown**, which for any privacy claim means
  "not private".

An upstream is **inside the CVM** when its URL's host has no dot and equals a service name in
`app_compose`; the reader then also knows the image that serves it (`app_compose` pins it by
digest). Anything else is a destination outside the CVM.

Ordering on a config change through the controller: `zg-config-update` → `zg-upstream-set`
→ `zg-image-update` (the keys are re-derived with the new set's hash, so a change to the set
changes the signer, and the on-chain acknowledgement of the old signer no longer applies).

### 4.4 `zg-engine-set`

```
count=<n>
<name>\t<image@sha256:…>\t<gpus>\t<args>
```

Containers the controller created after launch (`POST /v1/engines`). Only possible when the
controller's compose literal has `ZG_CONTROLLER_ENGINES=on`; a verifier that requires that
variable to be absent will never see this record and may refuse it if it does.

### 4.5 `zg-instance-config-update`

```
<instance> <64 lowercase hex>
<instance> unknown
```

A config change to one of the extra broker instances the controller manages
(`ZG_CONTROLLER_INSTANCES`). `unknown` is what an aborted change writes; a reader refuses the
whole CVM until a later record for that instance succeeds. Instances share the primary's
signing and encryption keys (one trust domain). **The upstream set (§4.3) is derived from the
primary config only**, which is why the controller refuses `recordUpstreamSet` together with
instances today; a verifier must not accept a privacy claim for a CVM that declares
instances until the record covers their upstreams too.

## 5. report_data (64 bytes at quote[568:632])

Layout version 1 (`0g-pc` SPEC §4.2):

| bytes | field |
|---|---|
| 0–32 | `enc_pub`, X25519 public key |
| 32–52 | `signer`, 20-byte address |
| 52–56 | version, big-endian `0x00000001` |
| 56–64 | zero |

Legacy layout (version field zero): the signer as ASCII hex text, zero-padded; no `enc_pub`.
A reader seals requests to `enc_pub` **from the quote**, never from `/v1/e2ee/pubkey`, and
only after step 6 of §2 has bound it to the recorded image.

## 6. What a verifier still has to bring itself

- the DCAP verification and the boot-chain allowlist (published dstack image hashes);
- the list of released broker digests (GitHub Releases of this repository, cosign-signed) —
  `broker_digest` and the controller's compose-pinned digest must be on it;
- the list of reviewed third-party image digests for everything on the plaintext path;
- the rule that only the controller mounts `/var/run/dstack.sock` (anyone holding it can
  append records; the records are only as good as that confinement) and
  `/var/run/docker.sock`;
- re-running all of this whenever the on-chain signer changes, not only at acknowledgement
  time.

## 7. Vectors

`api/common/attest/testdata/vectors/<name>/quote_response.json` — `quote` (hex), `event_log`
(GetQuote form, as served), `tcb_info` (full) from a real CVM, NVIDIA payload removed.
`expected.json` — `quote_rtmr3`, `quote_mr_config_id`, `compose_hash`, `sha256_app_compose`,
`report_data_enc_pub`, `report_data_signer`, `report_data_version`,
`runtime_events_after_system_ready`, `broker_digest`, `broker_digest_source`,
`broker_signer`, `broker_enc_pub`, `upstreams`. `vector_test.go` is the reference reader
run against them; an independent reader should run the same files.
