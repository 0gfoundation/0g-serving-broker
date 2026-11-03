# RTMR3 records: what the controller writes, and how a verifier reads them

This is the normative description of the records a 0G controller appends to a dstack CVM's
RTMR3, and of the steps a verifier takes to turn a provider's `GET /v1/quote` reply into a
statement about what that CVM is running. It exists so that a verifier can be written
**without importing this repository**: a reader that depends on the code of the party it
checks is not independent of it. `api/common/attest` is the reference implementation;
`api/common/attest/testdata/vectors/` holds real quote replies with the values a reader must
recover from them, and an independent implementation is correct when it agrees with those
files, not when it agrees with the Go code.

On dstack 0.6 there are no records; the same state is committed in the quote's `report_data`
instead (layout version 2, §8).

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
<name> <base URL> provider=<address>
<name> <base URL> <identity> provider=<address>
…                               (exactly n member lines)
```

or the single word

```
invalidated
```

The **whole** set of destinations the primary broker may forward unsealed plaintext to:
`service.targetUrl` plus every `modelPricing[].targetUrl`, deduplicated by name — of every
in-process instance's config too when there are any, each member tagged with its provider. A
snapshot, not a delta: the last record is the set.

The set's hash (which enters the signing and encryption key derivation) is SHA-256 over
`zg-upstream-set-v1\n` followed by one line per member, `<name> <base URL> <identity>\n`
(identity empty when absent), with ` provider=<address>` before the newline when the member
has one, lines sorted. A set without providers hashes exactly as before the field existed.
Because the instances share one signer, a change to **any** instance's upstreams rotates it,
and every instance's provider address needs its signer acknowledged again on chain.

- `count=<n>` is the first line; the number of member lines must equal `n` (a tally mismatch
  makes the record unreadable).
- `<name>`: `[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?` — lowercase, 63 bytes max. Derived from the
  URL's host unless the config gives `providerIdentity`, in which case that.
- `<base URL>`: `http` or `https`, lowercase scheme and host, ASCII host, no userinfo, no
  query, no fragment, no default port spelled out, no trailing slash, no bare `/` path, no
  dot segments, no percent-encoding in the path. (The writer refuses to record anything else,
  so a reader may refuse too.)
- `<identity>`: optional third field, the vendor identity the config names for that upstream.
- `provider=<address>`: optional last field, present when the CVM serves several providers
  from the same processes (in-process instances, controller-design.md §3.3a). `<address>` is
  the provider's on-chain address as `0x` + 40 **lowercase** hex; the member is a destination
  that provider's config permits. Tagged, so it cannot be read as an identity (which never
  contains `=`). Either every member carries it or none does — a mixed record is unreadable.
  Names are unique per provider: two providers using one upstream are two members.
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
(`ZG_CONTROLLER_INSTANCES`), or to one of the in-process instances the primary broker
serves from `CONFIG_DIR` (controller-design.md §3.3a) — the record is the same either way. `unknown` is what an aborted change writes; a reader refuses the
whole CVM until a later record for that instance succeeds. Instances share the primary's
signing and encryption keys (one trust domain). For **container** instances
(`ZG_CONTROLLER_INSTANCES`) the upstream set (§4.3) is derived from the primary config only,
which is why the controller refuses `recordUpstreamSet` together with them; a verifier must not
accept a privacy claim for a CVM that declares them until the record covers their upstreams
too. **In-process** instances are covered: their members carry `provider=`.

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

## 8. report_data v2: the state committed in the quote

dstack 0.6 removed `EmitEvent` for applications ("runtime RTMR3 events are system-owned"), so
on 0.6 RTMR3 holds no `zg-` records at all and §2 steps 2 and 4–6 have nothing to read. Layout
version 2 commits the same state in the quote instead. Design and rationale:
`doc/design/report-data-v2.md`. It is served on 0.5 too; there the records of §4 also exist.

### 8.1 Where

`GET <Service.url>/v1/quote?report_data_version=2` returns the reply of §1 with one more member,
`state_document` (a JSON string), and is signed like the other layouts (`ZG-Quote-Signature`
over the body, by the signer the document's `image` line names). It answers **404** when the
deployment serves no v2 quote — no attestation proxy, a controller that predates it, or a
controller that could not build the document when the broker started (the v2 quote is taken
once, at broker start, and is best effort) — never another layout. Any other
`report_data_version` is 400.

### 8.2 Layout

| bytes | field |
|---|---|
| 0–32 | `enc_pub`, X25519 public key — the key a client seals to |
| 32–64 | `SHA-256(state_document)`, over the exact UTF-8 bytes of the JSON string's value |

Telling the layouts apart without a version byte: v1 has `report_data[52:56] == 00000001` and
`[56:64]` zero; legacy is printable ASCII then zeros; anything else is v2 — and a reader that
asked for v2 checks the hash, which a v1 or legacy value does not satisfy (2^-64 / not ASCII).
Read `report_data` from the quote (`quote[568:632]`), not from the reply's `report_data` member
(§1).

### 8.3 The state document

UTF-8, lines joined by LF, **no final LF**, fixed order:

```
zg-state-v2
image <b64url(zg-image-update payload)>
config <b64url(zg-config-update payload)>
upstreams <b64url(zg-upstream-set payload)>   |   upstreams unrecorded
engines <b64url(zg-engine-set payload)>
instance <name> <b64url(zg-instance-config-update payload)>     (0..n lines, sorted by name)
ledger rtmr3   |   ledger unavailable
```

`b64url` is base64url without padding. Each payload is exactly the bytes the RTMR3 record of
that kind carries (§4.1–4.5), so it is parsed with the same grammar. What each line states,
**as of when the quote was taken** — every line is computed from the running deployment, not
taken from the last record written:

| line | states |
|---|---|
| `image` | `<repo>@sha256:<digest> <signer> <enc_pub>`: the digest of the image the broker container runs, and both keys derived for it and for the bound upstream set (§4.1). `<repo>` is the controller's configured repository and is not attested — judge the digest. |
| `config` | the hex SHA-256 of the primary config file on disk. |
| `upstreams` | the set the keys are **bound** to (§4.3). `unrecorded`: the deployment records no set (only possible with a ledger and `recordUpstreamSet` off). The payload `invalidated`: the set is recorded but no set is bound — it could not be stated, or its binding failed. Either is "nothing bounds where plaintext goes". |
| `engines` | the containers the controller created (§4.4); `count=0` when none. |
| `instance` | per extra instance, its name and its config file's hash (§4.5). |
| `ledger` | `rtmr3`: this CVM has an RTMR3 the controller appends to on every change (dstack 0.5) — the records of §4 exist for changes since boot, but a deployment that changed nothing has none, so their absence is not a mismatch. `unavailable`: no ledger (dstack 0.6, `ZG_CONTROLLER_LEDGER=none`). |

### 8.4 Steps

Replace §2 steps 2 and 4–6 with:

1. Verify the quote (§2 step 1) and anchor `app_compose` (§2 step 3).
2. `SHA-256(state_document) == report_data[32:64]`, else stop.
3. Parse the lines (§8.3). `image`'s `enc_pub == report_data[0:32]`, else stop.
4. `image`'s signer equals the provider's acknowledged on-chain `teeSignerAddress`.
5. Policy, as §2 step 7: the digest is a release; every `upstreams` member is inside the CVM
   (and `unrecorded` / `invalidated` are not private); the engines and instances are reviewed;
   the compose baseline is unchanged.

On `ledger rtmr3` a reader may additionally replay RTMR3 (§3) and require the last record of
each kind, where one exists, to equal the document's line — a consistency check, not a second
source.

### 8.5 What it does and does not bound

- **Who chooses the bytes.** The controller's attestation proxy quotes a v2 `report_data` only
  for the document it builds itself at that moment and the `enc_pub` it derives for that
  document's key identity; legacy and v1 only for the current keys; the ingress's socket only
  values whose second half is zero. No container holding either socket can obtain a quote in
  any layout a reader accepts over a document or key of its choosing (a zero second half is
  none of them).
- **The set is a bound on destinations by origin.** It is `service.targetUrl` (after the
  broker's `TARGET_URL`, which overrides it) plus every `modelPricing[].targetUrl` — of every
  in-process instance's config too, when there are any (§4.3); the
  broker's forwarding client follows redirects only to the same origin and answers any other
  as 502. Every outbound request carrying request content targets a member.
- **Freshness.** A quote is as fresh as the state it commits to: the broker takes all its
  quotes once, at start, and serves them until it restarts. An image change and a primary
  config change restart it; a change of a **bound** set also rotates the keys (a new signer to
  acknowledge on chain) — an unrecorded set rotates nothing. Two changes do not restart the
  primary broker, so their lines can lag the running deployment: engines, and the config of a
  container instance (`ZG_CONTROLLER_INSTANCES`). Both exist only with a ledger (without one,
  engine changes and container instances are refused), and there the `zg-engine-set` /
  `zg-instance-config-update` records are appended to RTMR3 — visible in a quote the broker
  takes after its next restart, not in the one it serves now.
- **Snapshot, not history.** On 0.6 nothing records the states between two quotes; changes
  that rotate the keys leave an on-chain acknowledgement, config-only changes leave nothing.
