# E2EE: the multipart endpoints (SPEC §5.3, §5.3.1)

Companion to [e2ee.md](./e2ee.md), which covers the subsystem: the crypto suite,
the enc key and its attestation binding, request unseal, profile resolution,
response sealing and the §8 signature. Read that first; this file is only the
part that is specific to the endpoints whose payload is **not JSON on the wire**.

Two rules live here, and they point in opposite directions — both apply:

- **§5.3** — a `multipart/form-data` endpoint has no top-level JSON object to
  canonicalize, so the client seals a JSON-ified request and the enclave
  materializes multipart back for the upstream. Everything that conversion has to
  get right is below.
- **§5.3.1** — a body is not "unsealed" merely because it cannot be parsed as an
  envelope. A multipart body must not smuggle an `_e2ee` part, and a JSON body on
  one of these endpoints must be an envelope or be refused.

`/v1/audio/transcriptions` is the only endpoint with a profile today
(`wire.ProfileSpeech`); image-editing is the other multipart endpoint and §5.3
does not cover it yet.

## The speech profile: a JSON-ified request (SPEC §5.3)

The missing top-level JSON object leaves §8's binding with no defined input
either, which is why this endpoint was excluded from sealing rather than merely
unimplemented.

```
caller ──multipart──▶ sidecar ──JSON envelope──▶ router ──▶ enclave ──multipart──▶ upstream
                      seals here                 opaque      opens, re-materializes
```

From §5.2's point of view it is an ordinary request, so nothing in the crypto or
the envelope is multipart-aware. Two properties make this the cheap direction:
the router never sees multipart on a sealed request, and `filename` — a part
header readable by every intermediary today — becomes an ordinary field the
profile seals.

**Request side.** `profileForRequest` resolves speech to `wire.ProfileSpeech`.
The protocol package then enforces §5.3.2's sealed set (`file_base64` always,
plus `filename` / `language` / `prompt` whenever present) and §5.3.3's
conditionally pinned `stream`, compared by *materialized rendering* so the
boolean `false` and the string `"false"` are one value.
`materializeSpeechRequest` converts the opened request back:

- it generates its **own boundary** — none crosses the sealed channel;
- it **forwards the sealed `filename`**, because some backends sniff the audio
  container from the extension, and by then we are inside the TEE;
- it **drops `stream`** rather than rendering it. `OpenRequestFor` has already
  established the only permitted value is the endpoint's own default, and the
  protocol package's renderer is unexported — so "must be `false`" and "how
  `false` is written" cannot be the same code. A field that is not written cannot
  be misread, and the values a form parser reads as true are an open set;
- an array becomes repeated `name[]` (the OpenAI multipart spelling); an
  **object is refused**, because bracket paths, JSON-in-a-field and dotted keys
  are all in use and guessing one would forward a different request than the
  client sealed;
- a field name or `filename` containing **CR, LF, `"` or `;` is refused**, and a
  field named **`file` is refused** — see below;
- a **number is relayed as the literal the client sealed**. The body is decoded
  with `UseNumber`, so nothing goes through `float64` and nothing is re-rendered:
  `1e3` stays `1e3`, and an integer past 2^53 is not silently rounded (measured:
  `12345678901234567890` used to arrive as `12345678901234567168`). No
  `ProfileSpeech` field reaches that range today; the point is that "the upstream
  gets what the client sealed" applies to values as much as to names, and an
  unsealed multipart request relays the client's literal too.

**The transcription is decompressed once, before anything reads it.** The sync
path asks the upstream for `identity`, but an upstream that ignores it sends
compressed bytes, and three readers want plaintext: the #184 leak sanitizer, the
§7.3 seal, and the billing parse. Decoding per-reader broke two of the three —
the seal ran on gzip and returned a 400 after the GPU time was spent, and the
billing parse re-decoded an already-decoded body, where the failure is not clean
(`gzip.NewReader` consumes its 10-byte header probe before erroring, and the
fallback reader is that same drained one, so 81 of 91 bytes survived and the
request billed by word-count estimate). One decode at the top, with
`Content-Encoding` cleared on both the response and the upstream header, is
therefore a correctness property and not a tidy-up. It is wire-visible for
non-E2EE traffic: such a body now reaches the client decompressed, which also
makes the §8 signature bind the bytes the client actually receives.

**Every cleartext field is materialized, including ones the client did not
write.** The request's cleartext half is rewritable in transit *by design* —
that is what `unbound_fields` is for, and the protocol package's own
`DefaultUnboundFields` doc names `x_0g_trace` and `route_options` as fields a
client may declare unbound so the router can inject them. On a JSON endpoint an
injected field is inert; here it reaches the multipart body. Measured, with
`x_0g_trace` declared unbound and injected after sealing:

| injected value | outcome |
| --- | --- |
| `"abc123"` (scalar) | materialized as an ordinary form field — the upstream sees router observability metadata in the transcription request |
| `{"req_id":"abc123"}` (object) | **400**: `field "x_0g_trace" is a composite value, which has no form rendering` |

Neither is a hole, and the object refusal is still right — there is no one
rendering of a nested object in a form. But the second row is a 400 **the client
cannot act on and the provider cannot fix**, so it is a constraint on the
*router*, recorded here rather than discovered from a support ticket: **a router
must not inject an object-valued field into a sealed speech request.** A scalar
is fine. Coordinate with `0g-router` before adding a structured request-side
trace field.

**The sidecar must strip `[]` when JSON-ifying a repeated form field.** An array
materializes as repeated `name[]`, which is the OpenAI multipart spelling — so a
sidecar that JSON-ifies the field under the name it saw *on the wire*
(`timestamp_granularities[]`, which is how the SDKs spell it) produces
`timestamp_granularities[][]` upstream. There is no error: `verbose_json` simply
comes back without word timestamps. The materializer cannot tell the two apart —
a client is entitled to seal a field whose name ends in `[]` — so this is the
sidecar's half of the convention, and the only place it is written down.

**A sealed client should always seal a `filename`.** When it seals none the
materializer writes `audio`, which the part needs to read as a file upload at
all — but with no extension. Since the profile seals no content type and the
part is written `application/octet-stream`, the extension is the *only* container
hint that survives the sealed channel, so a backend that sniffs the container
loses both. Not the materializer's to fix (guessing an extension would be
inventing one); it is a client-side requirement.

**`response_format` is mandatory on a sealed request, not merely restricted.**
The pinned-cleartext check fails on *absence* as well as on a disallowed value —
`sealed request must set "response_format" to "json" or "verbose_json"
explicitly (an absent value takes the server's default, which may not be
permitted)` — so a client that simply omits it, which is legal on the unsealed
endpoint and gets `json` by default, gets a 400 here. That is the right
behaviour and not an oversight: the profile can only seal a response it can find
a `_e2ee` slot in, and *the server* chooses the default, so an omitted format is
the client betting on a value it does not control. But it is a second rule on
top of the value restriction below, and the two are easy to read as one.

The **`Content-Type` and `Content-Length` move with the body**. Everything
downstream reads the boundary out of the header, so a multipart body still
labelled `application/json` reaches the upstream unparseable.

**A sealed envelope is only opened on the route its profile serves.** Profile
resolution answers from the service type alone, so without this a sealed speech
envelope POSTed to a free route — `/signature/{chatID}`, `/attestation/report` —
was opened, materialized into multipart and answered in the clear on a route that
serves no inference at all. Scoped to the speech profile, because that is what
made it reachable: before, the arm returned `("", false)` for the service type
and the envelope was refused. `ProfileImage` is route-blind in the same way and
predates this — measured on a text-to-image provider, a sealed image envelope is
opened on *every* route including the free ones, with the sealed `prompt`
restored and the context marked sealed. Widening the rule wants a per-profile
route set rather than a second profile-specific condition, so it is tracked as
[#734](https://github.com/0gfoundation/0g-serving-broker/issues/734).

**Sealed audio tops out well below what an unsealed upload gets.** `file_base64`
inflates the payload ~4/3 before it is sealed, and the 32MB body cap applies to
the *envelope*, so the largest audio that fits a sealed request is around 23MB
against 32MB unsealed. At the peak of such a request several copies are live —
the envelope, the opened request, the decoded audio and the materialized
multipart — which is why the reconstructed plaintext is **no longer stashed on
the request context**: nothing in production ever read it back (the §8 binding
travels as a 32-byte hash), so it was a retained copy proportional to the audio
with no reader. The accessor went with it.

**Names go into part headers, and `multipart.Writer` does not validate them.**
Its escaper handles `\` and `"` and writes everything else — CR and LF
included — verbatim, and both the field names and the `filename` come straight
out of the opened envelope, i.e. from the client. Reproduced end to end: a sealed
field named `zz\r\nContent-Disposition: form-data; name=model\r\n\r\nexpensive-model\r\nX`
materializes a part whose header block carries a **second**
`Content-Disposition … name=model`, and a filename of `a.mp3\r\nX-Injected: yes`
adds a header line inside the audio part.

The boundary is generated here and never leaves, so a whole extra part cannot be
injected. The reachable damage is a **parser differential**: Go's reader is
first-header-wins and reads the broker's `model`, while a reader that takes the
last `Content-Disposition` reads the injected one — the broker and the upstream
disagreeing about which model was requested, on a body the broker itself built.
That is exactly what this profile exists to prevent, so such a name is **refused,
not escaped**: a rewritten name is not the name the client sealed.

The quote is in the refused set for the same differential, measured rather than
assumed: Go writes `a.mp3"; name="model` as `filename="a.mp3\"; name=\"model"`,
which Go's own `ParseMediaType` resolves back to one parameter — but a parser
that does not process backslash escapes reads a second `name` out of it. A
**backslash is deliberately not refused**: escaped, it yields a literal backslash
in the value rather than a parameter break, and refusing it would reject an
ordinary Windows-style filename for no gain.

The **semicolon is a third mechanism, not a variation on the other two.** CR and
LF need no escaping because the writer emits them verbatim; the quote needs it
and gets it. A semicolon needs *neither*: inside a quoted parameter it is
RFC-legal and ordinary, so the writer emits it as-is and Go's own reader is right
to keep it. What breaks is a parser that splits the disposition on `;` before
honouring the quotes — and those exist. Both halves of the damage the quote is
refused for, measured the same way:

| sealed field name | RFC-compliant reader | `;`-splitting reader |
| --- | --- | --- |
| `zz; name=model` | one field literally named `zz; name=model` | a **second `name=model`**, so last-wins reads the injected model |
| `zz; name=file; filename=decoy.mp3` | one oddly-named field | a **second `file` part** named `decoy.mp3` — walking straight past the `file` reservation below, because the sealed field is not itself named `file` |

An `=` is **not** refused, measured for the same reason the backslash is not:
`zz=model` is written `name="zz=model"` and a `;`-splitter still sees one
segment, so there is no second parameter to read.

**A `filename` is a name, not a path.** One containing `/`, or equal to `.` or
`..`, is refused. Go's `ReadForm` never uses the client filename as a disk path,
but a backend that joins it onto an upload directory (Werkzeug without
`secure_filename`, several faster-whisper wrappers) does, and on the sealed path
the broker is the one writing the part header — so it owns what goes in it.
Refused rather than reduced to a base name, per this file's standing rule: a
rewritten name is not the name the client sealed. Only the forward slash: on the
POSIX upstreams this runs against a backslash is an ordinary filename character,
which is why `C:\recordings\a.mp3` is accepted and pinned by a test. This is
stricter than the unsealed multipart path, which relays whatever filename the
client sends — deliberately, because there the broker did not write the header.

**`file` is reserved.** It is the name the audio part is written under, so a
sealed field of the same name materializes two parts called `file`. Measured:
Go's `ReadForm` keeps both, sorting them by kind, while a backend reading
`form["file"]` or taking the last match gets the decoy and transcribes nothing.
Refused rather than skipped — nothing in the profile legitimately seals `file`
(the audio travels in `file_base64`), and silently dropping a field the client
sealed is the one outcome a profile whose whole claim is "the upstream gets what
the client sealed" must not produce.

**Response side (§7.3).** The sealed set is not a constant: `text` always, plus
each of `segments` / `words` / `language` the frame carries — a profile-wide
constant would reject a plain `json` transcription or leak a `verbose_json` one.
That resolution lives in the wire package and the speech handler reaches it
through the same `prepareFrameForSealing` the chat and image paths use. The
billable quantity stays **cleartext**, as either `usage.seconds` or the top-level
`duration`, so billing and the router both still read it.

**A sealed response is signed BEFORE the frame is flushed**, as on the chat and
image paths (issue #619). The client learns the chatID from `ZG-Res-Key`, which
goes out with the flushed headers, so signing afterwards let a sealed client that
immediately fetched `GET /v1/proxy/signature/{chatID}` race the cache write — and,
worse, made a signing failure unrecoverable: the frame was already on the wire, so
the failure could only be logged while the client held a response it could never
verify. Signing first lets it **fail closed**. Only the sealed path is reordered;
the plaintext path keeps its existing sign-after-write order, and a signing
failure there is still non-fatal, because an unsealed client can read its
transcript without a signature and a sealed one cannot.

**The signing gate is now the same three-way condition chat and image carry** —
`!TargetSeparated || IsCentralized() || e2eeSealed` — on *both* the streaming and
the non-streaming branch, and both route through `signChatResponse`. That is a
**wire-visible change for unsealed traffic too**, and worth stating plainly
rather than leaving to be discovered: a centralized STT provider now emits
`ZG-Res-Key` and caches a **routing proof** where before it emitted nothing, and
an in-network one reaches the same `signChatWithKey` it always did. Holding the
two branches to one condition is the point — before, an unsealed centralized
provider got the proof on a non-streaming transcription and nothing on a
streaming one, a difference in the client's evidence chain that the streaming
flag has no business making. The §8 signature routes
through `signChatResponse` rather than `signChatWithKey`, because on a sealed turn
it must bind the on-wire aad‖ciphertext rather than a plaintext the client never
received.

**Sealing demands more of the upstream than proxying does, and that is the
profile's sharpest operational edge.** §7.3 requires the billable duration as a
**JSON number** at `usage.seconds` or top-level `duration`. Measured against the
pinned protocol package, three shapes the broker bills happily when proxying are
refused when sealing:

| upstream body | who sends it |
|---|---|
| `{"text":"…"}` | `whisper-1` and most self-hosted faster-whisper / vLLM builds, on `response_format=json` |
| `{"text":"…","usage":{"type":"tokens",…}}` | `gpt-4o-transcribe`, which bills in tokens rather than seconds |
| `{"text":"…","duration":"3.2"}` | a whisper backend observed in the wild — `flexFloat64` exists for it |

`null` in either locator is refused too: the protocol package draws "null is the
absence of a number, not a zero", while a genuine `0` is accepted.

The transcription has already been produced when this is discovered, so the
request fails **after** the compute was spent. That is the right trade rather
than a defect: synthesizing the number would have the §8 signature attest a
duration the model never produced, which is precisely what
`updateSpeechToTextFallback`'s word-count estimate is. The same answer the image
profile already gives a provider that returns a 200 with no verifiable image
count.

The attribution is taken **at the seal failure**, not by a pre-check restating
§7.3. There was such a pre-check and it was a strict subset of what the sealer
enforces — measured, a negative `duration`, two locators that disagree, and a
null `usage.seconds` beside a valid `duration` all walked past it and were then
refused with no attribution at all, landing back in the bucket the attribution
exists to empty. A second copy of a rule is a subset of it by default, and the
sealer's own messages are more precise than the pre-check's were. The rule now: a
sealed turn whose profile is in hand has everything the broker owes, so what is
left to fail is the upstream's response; a missing profile is broker state and
keeps the default bucket.

Two consequences are wired deliberately:

- **Attributed upstream**, not to the broker. The provider's response *shape* is
  what makes the result unusable, and a provider must not be able to move a
  degradation into the broker's alert bucket by emitting an unbillable 200. The
  wasted compute lands on the provider, which is also who can fix it. Unlike the
  image path this is **not** marked `ignoreError`, so it stays logged — an
  operator wants to see it, and the attribution override already keeps it out of
  the client bucket.
- **Named**, with its own error text rather than a generic seal failure, so the
  log and the response distinguish it from a real broker fault.

The check is in the handler rather than left to the sealer only because the
sealer's refusal carries no distinguishable error type. The sealer remains the
only **gate** — if the two ever disagree it still fails closed behind the check.

Not pre-rejected at admission: `response_format` cannot predict this. The
profile permits both `json` and `verbose_json`, only `verbose_json` reliably
carries `duration`, and refusing `json` up front would reject every upstream that
*does* report seconds on it. The condition is a property of the response, so it
can only be discovered from the response.

The quoted-number case is the one worth fixing rather than documenting, and it
belongs **upstream in `0g-pc-e2ee`**: `flexFloat64`'s existence is evidence the
shape is real, and a broker-side normalization would be working around the
protocol package instead of correcting it.

**An unsealed `file_base64` JSON body is refused, deliberately.** The router's
OpenAPI spec documents such a shape on this endpoint; the broker has never
implemented it, and the customer-facing docs say the opposite ("this endpoint
uses `multipart/form-data` **instead of a JSON body**"). Refusing it is therefore
consistent with what is actually promoted, and replaces a silent failure at the
upstream with a clear 400 here. Supporting it later is small — the
materialization is profile-independent — but it is a product decision, not a
protocol one.

**Not covered:** streaming (§5.3.3 — the profile defines no streaming frames);
`response_format` outside `{json, verbose_json}` (§5.3.2 — `text`/`srt`/`vtt`
return a body with nowhere to put `_e2ee`, so they are inexpressible under
sealing rather than merely leaky); and upstreams that report no numeric duration,
per the section above — sealed traffic to them fails rather than being billed on
an estimate.

## An envelope smuggled into a multipart body (SPEC §5.3.1)

Step 1 detects an envelope in a JSON body. A client can also put one in a
`multipart/form-data` part — the speech-to-text and image-editing shape — where
step 1 never looks, because the body is not JSON. §5.3.1 is the rule against
that: *a body that cannot be parsed as an envelope is not thereby an unsealed
body*, and a body that **is** an envelope is one whatever `Content-Type` carries
it. So `multipartNamesE2EEPart` enumerates the parts and refuses a request
declaring one named `_e2ee`, on both entry points (`MaybeUnsealRequest` for the
sync proxy, `RefuseAsync` for the async submit routes, which never reach the
proxy and were the same hole one request shape over).

Both halves of §5.3.1 now hold, and they are separate rules: a **multipart** body
must not contain an `_e2ee` part (above), and on an endpoint with a JSON-ified
profile a **JSON** body must be a valid envelope or be refused — never forwarded
as an unsealed JSON request "just in case".

The predicate's **operand order is load-bearing**. All four are pure, so `&&`
short-circuits left to right and the cheap ones lead: service type, then route,
then anything that touches the body. Written the other way round — as it briefly
was — every request on every service type pays a full unmarshal before the
service-type check rules it out, which also defeats `hasE2EEMarker`, the
substring scan that exists to keep the parse off the non-sealed majority.
Measured on a 1 MiB chat body: **4.69 ms and 1,057,463 B per request against
20 µs and 48 B**. Two tests pin it by allocated bytes — one on chatbot, one on a
speech provider's free routes, because on chatbot the service-type check
short-circuits first and hides any ordering of the rest.

That second half is keyed on the **body's shape**, not on the declared media
type. Leading with a `Content-Type` check made the rule hold for a body
*labelled* JSON rather than for a JSON body: the same
`{"model":…,"file_base64":…}` reached the multipart-only upstream verbatim under
`text/plain` or no `Content-Type` at all, which is the fall-through the rule
exists to prevent. Only a JSON **object** counts — a bare array, string, number
or empty body is not a request shape this endpoint has ever accepted, sealed or
not, and the upstream's own refusal is the clearer error for it. The asymmetry
with the multipart half is deliberate and stays: that one *must* read the header,
because the boundary lives there and there is no other way to find the parts.

Image-editing is the other multipart
endpoint and §5.3 does not cover it yet, so it has no profile and the rule is not
applied to it.

**The second half is scoped to the ENDPOINT, not just the service type**, and
getting that wrong was a live break rather than a loose edge. `MaybeUnsealRequest`
runs in `proxyHTTPRequest` *before* the route is classified, and
`serviceGroup.Any("*any", …)` puts every method and path under `/v1/proxy`
through it. A service-type-only predicate therefore refused a JSON content type
on every path a speech provider serves — measured, on both GET and POST:

| path | consequence |
|---|---|
| `GET /v1/proxy/signature/{chatID}` | the endpoint a sealed client **must** call to fetch the §8 signature this profile emits |
| `GET /v1/proxy/attestation/report` | no attestation |
| `GET /v1/proxy/models` | no model list |

Clients that set `Content-Type: application/json` on every request — the OpenAI
SDKs among them — would have lost the signature fetch to the very change that
started producing signatures.

**The route is the dispatcher's own normalized path, compared for equality** —
handed to `MaybeUnsealRequest` by the proxy, derived once in
`constant.SplitTargetRoute` beside the tables it is matched against.

Equality against the dispatcher's string, not a suffix over `ctx.Request.URL.Path`
— that was a bypass, since any path plus the route ends with the route, and
`/v1/proxy/signature/audio/transcriptions` was a free route to the dispatcher and
this endpoint to the guard. The derivation therefore exists **once**, in
`constant.SplitTargetRoute`: the proxy matches billing keys against it and passes
it in, so there is no second copy to diverge and the compiler requires the caller
to supply it. Pinned at both levels, because the *wiring* — which of the two paths
in scope gets passed — exists only in `proxyHTTPRequest`, where passing the wrong
one closes the bypass and breaks sealed speech completely with every `ctrl` test
still green.

The lesson is in the guard's own comment rather than here: the suffix was
defended as "a spelling this misses loses only the diagnostic refusal, never a
protection", which was true while the §5.3.1 diagnostic was its only caller and
false once this profile's route guard made it load-bearing. *A predicate whose
safety argument names its callers has to be re-read when a caller is added.*

The rule is on part **names**, never on the raw bytes: `prompt` carries arbitrary
caller text, so a substring rule would 400 a legitimate transcription for
mentioning the protocol.

**Scope, deliberately.** It reads what Go's `mime/multipart` reads and nothing
more. A malformed body, an unreadable boundary, a nested part, or a name spelled
in an encoding Go declines to decode is **forwarded**. That is not an oversight:
a client evading the check has forwarded its own ciphertext upstream and gets
garbage back — there is no adversary with a motive here, only an honest client
with a bug, and `_e2ee` is plain ASCII that such a client has no reason to
encode. The forwarded shapes are asserted by
`TestMalformedAndExoticBodiesAreForwarded` so the limit is a decision on record.

Note this rule and the speech profile above are about **opposite** directions and
both apply: a sealed speech request arrives as JSON and is opened; a multipart
body carrying an `_e2ee` part is a client error and is refused. Sealed data
legitimately reaching a multipart endpoint does so as an envelope, never as a
form part.
