# report_data v2: the running state committed in the quote (dstack 0.6 and after)

Status: proposal, 2026-10-26. Owners: broker/controller (Jiahao), gateway/pcverify (Hongji).

## 1. Why

Everything a verifier learns about a controller-mode CVM **after boot** — which broker
build runs, where plaintext may be forwarded, which config file is installed — is read
from RTMR3 records the controller appends through dstack's `EmitEvent`
(`doc/rtmr3-records.md`). dstack 0.6.0 removed that: `EmitEvent` always fails with
*"runtime RTMR3 events are system-owned and cannot be extended by apps"* (0.6 CHANGELOG,
Removed, #1116), and the replacement dstack names is one line in `guest-api-v1.md`:
*"Applications bind their data through `report_data` instead."*

Today's effect: a controller on 0.6 logs an error at boot and carries on with nothing in
RTMR3 (`controller/cmd/server/main.go:75`); the gateway's runtime check and the upstream
rule then read "unknown", which is "not private". Nothing is wrong on 0.5.9, where every
production controller-mode CVM runs, and 0.5.x stays supported by the frozen v0 surface
(`GetQuote`, `Info`, `GetKey` on `/` are unchanged in 0.6 — only `EmitEvent` is gone). So
this is not urgent for what runs now; it is a prerequisite for any OS upgrade, and it
closes one hole that exists today regardless (§4.1).

## 2. What changes, in one paragraph

The controller already decides the running state (it is the only writer of RTMR3 records
and the only holder of `dstack.sock`). Instead of appending that state to a ledger, it
commits to it inside the quote: `report_data` becomes `enc_pub ‖ SHA-256(state document)`,
the broker serves the state document next to the quote, and a verifier recomputes the
hash. The records inside the document keep the exact grammars `rtmr3-records.md` already
defines, so a reader reuses its parsers; what changes is where the bytes come from and
the one new step of hashing them. On 0.5 the controller keeps writing RTMR3 as well, so
every reader in use today keeps working unchanged.

## 3. The v2 layout

```
report_data[0:32]   enc_pub            X25519 recipient key, as in v1
report_data[32:64]  SHA-256(stateDoc)  the commitment
```

v1 is `enc_pub(32) ‖ signer(20) ‖ 0x00000001 ‖ 0×8` and legacy is the ASCII signer
address zero-padded. A reader tells them apart without a version byte: v1 has
`report_data[52:56] == 00000001` and `report_data[56:64] == 0`; legacy is printable ASCII;
v2 is neither (its last 32 bytes are a hash, so the chance of matching either pattern is
2^-64). A reader that receives a state document checks v2 first and falls back.

### 3.1 The state document

A text document, UTF-8, LF line endings, one record per line, fixed order, no trailing
blank line. Field payloads are the **exact bytes** the RTMR3 records already carry,
base64url-encoded without padding, so the grammars in `rtmr3-records.md` §4 apply
unchanged:

```
zg-state-v2
image <b64url(zg-image-update payload)>
config <b64url(zg-config-update payload)>
upstreams <b64url(zg-upstream-set payload)>
engines <b64url(zg-engine-set payload)>
instance <name> <b64url(zg-instance-config-update payload)>   (0..n lines, sorted by name)
ledger rtmr3|unavailable
```

- `image`: `<repo>@sha256:<digest> <signer> <enc_pub>` — the running broker build and
  both keys derived for it. **`enc_pub` here must equal `report_data[0:32]`**; that is the
  binding between the commitment and the key a client seals to.
- `config`: sha256 of the installed primary config file. Written as `none` (literal, not
  encoded) only on a CVM whose controller has never installed a config — in practice
  never, since the boot path installs one.
- `upstreams`: the whole permitted set, or `invalidated`, exactly as §4.3. A reader treats
  anything but a readable set as "not private".
- `engines`: `count=0` on every deployment without `ZG_CONTROLLER_ENGINES=on`.
- `ledger`: `rtmr3` when every record in this document was also appended to RTMR3 (0.5),
  `unavailable` when `EmitEvent` refused (0.6). It tells a reader whether the RTMR3 copy
  exists to cross-check; it changes no rule.

The document is the controller's; the broker never composes it (§4.2).

### 3.2 What a verifier does

Replaces §2 steps 2 and 4–6 of `rtmr3-records.md` when a state document is present:

1. Verify the quote (DCAP). Anchor `app_compose` to `mr_config_id` as before.
2. `SHA-256(stateDoc) == report_data[32:64]`, else stop.
3. Parse the lines with the §4 grammars. `image.enc_pub == report_data[0:32]`, else stop.
4. `image.signer` equals the provider's acknowledged on-chain `teeSignerAddress`.
5. Apply the same policy as today: `image` digest on the release list; `upstreams` all
   inside the CVM; `engines` empty; the compose baseline unchanged.

Nothing is replayed. On 0.5 a reader may additionally replay RTMR3 and require the last
record of each kind to equal the document's — a consistency check, not a second source.

## 4. Who may obtain a quote over what

This is the part that makes or breaks the scheme, and it is also the part that fixes a
hole we have today.

### 4.1 The hole that exists now

The attestation proxy forwards `/GetQuote` with **the caller's** `report_data` on both
sockets. The broker's socket (`zg-tee`) has to, because the broker builds its own v1
report_data; the ingress's read-only socket (`zg-tee-ro`, #763) does too, because
dstack-ingress fetches evidence as `GET /GetQuote?report_data=<sha256 ‖ zeros>`.

So a modified broker image, or the ingress image itself (Phala's, not ours, and on the
TLS path), can obtain a **genuine** quote over `attacker_enc_pub ‖ real_signer ‖ 1 ‖ 0`.
The signer matches the chain, DCAP passes, and a client that reads enc_pub from that
quote seals its prompt to the attacker's key. What catches it today is the gateway's
runtime check comparing the quote's enc_pub with the one in the last `zg-image-update`
— only under enforce, and not at all on 0.6, where there is no record. The ingress had
this ability before #763 as well (it mounted `dstack.sock` directly), so this is not new;
it is just the moment to close it.

### 4.2 The rule: the proxy validates every report_data it forwards

`attestproxy` stops being a pass-through for report_data. For each socket:

| socket | accepted `report_data` | why |
|---|---|---|
| `zg-tee` (broker, event, instances) | exactly one of: the **legacy** layout for the current signer; the **v1** layout for the current (enc_pub, signer); the **v2** layout for the current state document | the three things an honest broker ever asks for, computed by the controller from its own view — nothing a caller composes is forwarded |
| `zg-tee-ro` (ingress) | `report_data[32:64] == 0` | the shape of ingress evidence (`sha256(sha256sum.txt)` padded); none of the three layouts above has a zero second half, so the ingress can produce none of them |

Anything else is refused with 403 and logged at warning, like `/EmitEvent` is today.

The "current state document" is owned by the controller: it already holds the digest, the
bound upstream-set hash (`CurrentKeyIdentity`), the installed config's sha256, the engine
set and the instance configs — the same values it writes to RTMR3 today. Two new local
methods on `zg-tee`, answered by the proxy like `/Sign` is:

- `GET /State` → the current document (text/plain). The broker serves it to clients.
- `POST /GetQuote` with v2 report_data → validated against `SHA-256(current document)`
  and the current enc_pub, then forwarded. (The broker computes report_data from the
  document it fetched; a race with a concurrent state change simply fails validation and
  the broker retries, which is the same ordering the key rebind already imposes.)

No caller gets to choose bytes that end up in a quote, except the ingress's own evidence
hash, which by construction cannot look like any of our layouts.

### 4.3 What this does not change

- **Key derivation.** signer and enc_pub stay `GetKey` at the path
  `(digest, upstream-set hash)` (`doc/design/per-image-signing.md`). v2 is another
  commitment over the same keys, so no re-acknowledgement is caused by adopting it.
  (dstack 0.6's *v1* `GetKey` uses a different KDF; we stay on the frozen v0 `GetKey`,
  which 0.6 serves unchanged. Moving to the v1 KDF would rotate every signer and is a
  separate decision.)
- **Freshness.** A quote is as fresh as the state it commits to, no more — exactly as a
  RTMR3 log is. What bounds staleness today keeps bounding it: a change to the image or
  the upstream set rotates the keys, so a request sealed to an old enc_pub does not open
  after the change, and the new signer has to be acknowledged on-chain before billable
  traffic resumes. A config-only change rotates nothing, so a client holding an old quote
  may carry an old `config` hash until it refreshes; `config` carries no privacy weight
  (the upstream set is what does), so that is accepted, as it is today.
- **Quote lifecycle.** The broker produces and caches its quotes at key bind (boot, and
  after every controller-driven change) and serves the cached one, as it does for v1.
  A per-request nonce would need per-request quote generation and is left out; if a
  verifier wants it later, the document gains a `nonce` line the proxy accepts verbatim
  (≤ 32 bytes) and nothing else here changes.

## 5. What is lost, stated plainly

RTMR3 is an append-only history: a reader could see that the upstream set was changed to
an external host and changed back between two quotes. report_data commits to a snapshot.
Two things remain: every change that rotates the keys leaves an acknowledgement on-chain,
which is public and append-only; and on 0.5 the controller keeps writing RTMR3, so the
history is intact there (`ledger rtmr3`). What is genuinely gone on 0.6 is the history of
config-only changes, which the controller records today as `zg-config-update`. We accept
that: the config hash was never compared against anything by a verifier.

## 6. Compatibility and rollout

Everything below is additive. No existing caller, client or verifier sees a difference
until it asks for v2.

| component | change | default behaviour |
|---|---|---|
| controller (`attestproxy`) | validate report_data per §4.2; serve `/State`; on `EmitEvent` failure write `ledger unavailable` and continue; on **any other** RTMR3 write failure on 0.5 fail closed (today: log and continue) | unchanged for legacy and v1 callers |
| broker | `GET /v1/quote?report_data_version=2` → v2 quote + `state` document in the reply; `?legacy=` unchanged | unchanged |
| event, instance brokers | nothing (same image, same proxy) | — |
| `rtmr3-records.md` | add §8 "report_data v2" (= this §3); add a 0.6 golden vector under `testdata/vectors/` once a 0.6 CVM exists; the v1/RTMR3 vector stays | — |
| gateway / pcverify | request v2; verify per §3.2; **fall back** to v1 + RTMR3 replay when the broker returns no state document (pre-v2 broker, or v2 not requested); OS allowlist may then admit a 0.6 image | enforce semantics unchanged: the same three judgements, read from the document instead of the log |
| compose (19, 20, rehearsal, template) | none — the sockets and volumes are already in the #763 shape | — |

Order:

1. Broker PR: proxy validation (§4.2, including the ingress rule — this part is worth
   shipping even if nothing else is), `/State`, v2 quote, fail-closed, spec §8, tests
   (vector: the rehearsal CVM on 0.5.9 with `ledger rtmr3`; a 0.6 vector when available).
   Release as **v0.6.0** of the broker.
2. Deploy 19/20 on it — the redeploy that was planned anyway for the controller
   release; one restart carries both.
3. Gateway: v2 reader with v1 fallback, behind the existing enforce switch.
4. Only then: trial a 0.6 guest image on the rehearsal CVM, add it to the OS allowlist.

## 7. Open points for the gateway side

- Whether to keep requiring the RTMR3 replay on 0.5 as a cross-check (cheap, and it keeps
  the history argument in §5 for 0.5) or to read only the document everywhere.
- `pcverify -provider` output: show which source answered (`document` vs `rtmr3`) so a
  report says what it compared.
- The seal rule reads enc_pub from report_data today (SPEC §4.2); for v2 the layout is
  the same for bytes 0–32, so the client side needs no change unless it also reads the
  signer from bytes 32–52 — which it must stop doing once v2 is in play, and read from the
  document (or the chain) instead.
