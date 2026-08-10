# Video request authoring and the in-flight reserve

Issue: [#628](https://github.com/0gfoundation/0g-serving-broker/issues/628).
Companion to [video-generation-async-billing.md](video-generation-async-billing.md),
which covers what happens *after* the create is forwarded.

## The problem

A video create must pass a balance check **before** it is forwarded — you cannot ask
"can this wallet afford it" after the GPU time is spent. So the broker used to parse
the client's request and predict what the upstream would bill.

It cannot do that reliably. The upstream (videotranslator + vendor) parses the *same*
request to decide what to render, and the broker's reading was systematically stricter
— "cannot read it" means falling back to a smaller number, so **every divergence cost
money in the same direction**. Five were found and fixed one at a time, each on a
different axis, and each was found in the fix or the test written for the previous one:
the URL query on a transport where the upstream never reads it; a value past the
broker's sizing cap counting as "the upstream named a duration"; size read from the body
while the upstream read the query; strict `json.Unmarshal` over a wide struct vs the
upstream's `json.Decoder` over a narrow one; and a 1024-byte form-field cap returning a
truncated prefix as a usable number while `r.FormValue` read the whole value.

The fifth could only be handled by *refusing* rather than aligning. There is no reason
to believe a sixth does not exist.

Measured on mainnet before this landed: a wallet with exactly 1.0 0G locked passed the
gate and was billed **6.698 0G** for one 5-second 2K clip, because the reserve passed
for video was literally `"0"` and the only barrier was `MinimumLockedBalance`.

## The fix: write the inputs, don't predict them

`Ctrl.AuthorVideoRequest` (`inference/internal/ctrl/video_request.go`) rewrites the
create request before it is forwarded. Three rules, no prediction:

```
seconds unreadable -> 400 (ErrVideoSecondsUnreadable)
seconds absent     -> the model's configured default is WRITTEN, and priced
seconds present    -> normalised into the model's range, WRITTEN BACK, and priced
```

One parser that writes the field cannot diverge from itself. The whole variant family
is removed, not narrowed.

Resolution is authored the same way, through a dedicated `resolution` field the
translator honours over anything it would otherwise derive:

- a `size` that is already a resolution token (`"2K"`, `"1080P"`) is honoured — both
  supported vendors accept one;
- a **pixel-dimension** `size` (`"1280x720"`) is an *aspect ratio* to every supported
  vendor, never a tier. MiniMax renders `MINIMAX_RESOLUTION` from the translator's own
  environment; DashScope snaps to its own two-tier enum. Neither is knowable at gate
  time, so the model's `billing.defaultResolution` is written explicitly alongside the
  size, which keeps the aspect ratio working.

A client-supplied `resolution` is dropped, never forwarded: it would select a tier the
reserve did not price.

Multipart bodies are rewritten with a real MIME reader/writer under the original
boundary, not a substring scan — a scan cannot tell a form field from the same bytes
inside a prompt value, and these two fields decide the fee.

### Where the values come from

`billing.defaultSeconds` / `minSeconds` / `maxSeconds` / `defaultResolution`, per model.
They must describe the **vendor's** accepted set, and the design is self-checking:
because the broker now writes the value, a wrong one is rejected by the vendor as a 4xx
on the create — loud and unbilled — instead of silently rendering a tier that was never
priced. `minSeconds` is the one that must be right for the money: a vendor that clamps a
shorter request *up* to its own floor renders more than was reserved. Clamping down can
only over-reserve.

Whitelisted traffic branches out before the billing switch and never reaches any of
this, by design.

## The in-flight reserve

The reserve also has to survive until the job settles. The `requests` row is created
with `fee = "0"` and unsettled is `SUM(fee) WHERE processed = false`, so an async video
job contributed **0** for the minutes until the poller settled it — N concurrent creates
from one wallet all read the same balance and all passed. The gate guaranteed "this
wallet can afford ONE clip", not "this wallet can afford what it has in flight".

The hazard here is worse than the problem: some jobs are never billed (poll times out,
provider reports failed, no usable duration). An estimate written and never cleared
would remove that amount from the wallet's available balance **permanently**, with no
recovery path. So the whole thing rests on one checkable invariant:

> a non-zero reserve exists on a `requests` row **iff** an unresolved poll job exists

- **One write site**: `ctrl.reserveInFlightVideoFee`, called only after
  `CreateVideoPollJob` succeeds. A path that never creates a poll job never writes a
  reserve, so there is nothing for it to leak — which is why the several
  `RecordVideoBillingSkipped` sites need no release call. (Adding one at each would mean
  two places deciding the same thing, and missing one strands a balance forever.)
- **Release sites**, all single transactional DB methods in `internal/db/video_poll_job.go`:

  | terminal method | handling |
  |---|---|
  | `CompleteVideoPollJobWithBilling` | already overwrites `fee` with the real amount |
  | `FailVideoPollJob` | resets to `"0"` in the same transaction as the status write |
  | `TimeOutVideoPollJob` | same |
  | `CompleteVideoPollJobWhitelisted` | nothing to release — whitelisted traffic has no `requests` row |

`output_count` stays 0 while a reserve is held, so `ListRequest`'s `ExcludeZeroOutput`
keeps a reserve out of on-chain settlement: it is a hold on the balance, never something
to settle.

The invariant is tested at both layers: `TestPollVideoJob_TerminalWithoutBilling_ReleasesReserve`
(ctrl, asserts every terminal-without-billing path hands the request hash down) and
`TestVideoPollJob_InFlightReserveLifecycle` (db, integration-tagged — removing either
`releaseRequestReserve` call turns it red).

## Known residue

The routing proof binds `sha256(forwarded request body)`, and the forwarded body now
differs from the client's by the authored `seconds`/`resolution` as well as the
pre-existing appended `wait=false` part. A client cannot reproduce that hash from its
own request. This gap predates this change (see `ensureMultipartWaitField`); the image
path solves it by stashing `clientReqBody` for signing, and video would need the same
plus a second body on the poll job, since the poll's billing fallback must keep reading
the *authored* duration.
