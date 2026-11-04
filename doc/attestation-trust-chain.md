# Attestation Trust Chain

What a user is told, and why they can believe it, when

- the request passes through a **router that is not a TEE and is not trusted**, and
- the **broker component is upgradeable in place**, without the CVM's `compose_hash` changing.

## How to read this

Organised by **proposition**, in three layers. Every proposition states the same kind of
thing: *given the premises listed beneath it, it holds.*

Once you finish a layer you never have to revisit its reasoning — you only have to check
that the next layer discharges every premise the previous one listed. **Stopping at any
layer leaves you with a complete conclusion rather than half an argument:**

- **Layer 1** — the three questions a user actually asks, and what each one needs
- **Layer 2** — 14 premises, and which concrete checks each one needs
- **Layer 3** — 40 concrete checks, each runnable as written

Premises are numbered flat, `N0` through `N13`, **independent of which question uses
them** — several are used by two or three questions at once, so grouping them under one
question would mean writing them twice.

There is one rule for choosing premises: **drop any one of them and the rest still hold
while the conclusion no longer does.**

**One discipline runs through the whole document.** Some checks compare a value against
"the value I expect" — the OS version I accept, the image version I accept. Every one of
those obeys the same rule: **the expected value must come from somewhere you chose, never
from the party you are checking.** Where you keep it does not matter (a constant in code, a
line in a notes file). Only its provenance does. An "expected value" fetched from the
provider under examination is not a check; it is a restatement.

**Two kinds of deployment, and which path applies.** What a CVM runs after boot — which
broker image, which keys, where plaintext may go — is not in the manifest, and reaches you one
of two ways:

- **the ledger** (records the controller appends to `RTMR3`, replayed out of the event log) —
  **dstack 0.5 only**.
- **the state document** (`report_data` layout v2: the controller states the current state in
  a short text document, and the 64 bytes the hardware signs carry its hash) — served on
  dstack 0.5 and 0.6.

dstack 0.6 removed `EmitEvent` for applications, so a 0.6 CVM runs the controller with the
compose literal `ZG_CONTROLLER_LEDGER=none` and **has no records at all**. **On dstack 0.6, v2
is the only path that can be verified. On 0.5 both work**, and a reader may check one against
the other. Wherever below "the ledger" carries an argument, the v2 reading is given beside it;
the normative steps are `doc/rtmr3-records.md` §2 (ledger) and §8 (v2).

---

# Layer 1: the three questions a user actually asks

## Five terms, used throughout

- **CVM (confidential virtual machine)** — a VM whose memory is encrypted by the CPU. **The
  physical server it boots on, and the cloud operator's staff, cannot read what is inside.**
  Your request is processed in there.
- **attestation, also called a quote** — a health report the CVM produces, stating what hardware it
  is and what was loaded at boot. The report is **signed by Intel's private key**, so its
  contents cannot be altered by the provider, who does not hold that key.
- **measurement** — every step of boot is hashed and folded into a few registers in the CPU;
  the report carries those hashes. **Change any step and the hash changes.**
- **KMS (key management service)** — the service that issues this machine's keys. A CVM
  cannot conjure keys itself; the KMS issues them, and the release policy is on-chain and
  publicly readable.
- **provider** — the party operating this machine. **This document trusts that party for
  nothing.** Wherever a claim would rest on their word, it counts as no proof at all.

**Three names below are field names you will see in the API response, so they are used as-is
rather than paraphrased:**

| Field | What it holds |
|---|---|
| `quote` | the attestation report above — the API and the code both call it this |
| `event_log` | the **boot log**: a list of what was loaded at boot, which is what explains the hashes in the report |
| `signer` | the **signing address**: the address of the key the program uses to sign responses |

---

## Q1 This response came from trustworthy hardware

> I received some text. Why should I believe it was computed on a genuinely protected
> machine, rather than typed by somebody on their laptop?

**Six premises answer this question:**

| Premise | The situation it rules out |
|---|---|
| **N0** the verifier itself is trustworthy | the judgement was made by the very party under examination |
| **N1** the report comes from real Intel hardware | the report is fabricated; there is no TEE on that machine at all |
| **N2** the OS is a publicly released build | the hardware is real but the provider modified the system |
| **N3** the keys are issued by a KMS I accept, and the provider cannot copy them | hardware and system are both real, but the provider holds a copy of the same keys |
| **N4** the GPU is real and its memory is protected | the CPU side all holds, while the model actually runs on an unprotected card |
| **N8** this response came from that program | it rules out no situation here; it supplies "which key speaks for this machine", and it is established in Q2 |

**Derivation —**

The provider can hand you two things: an assertion, and a string of numbers.

**Before anything else, be clear about one thing: every conclusion below is computed by some
program.** So the first premise is about that program — **it must be run by you, and it must
cover enough checks** (`N0`). Leave this unstated and every step below has a hidden
dependency.

What that program is needed for is **interpreting the measurements**: replaying the boot log
into the registers, and deciding which dstack release a given measurement corresponds to.
**Intel's signature chain is not on that list** — that check has several independent
implementations to choose from, including on-chain ones. See `N0`.

**Step one turns those numbers into testimony.** Intel's certificate chain signed the
attestation, the signing key is Intel's, and the provider cannot forge it. That is `N1`.
Without it, everything downstream is arithmetic on numbers the provider supplied.

**Step two reads the testimony.** The testimony is a set of measurements, and a hash on its
own says nothing — you need to know what a given hash corresponds to. dstack (the vendor of
this CVM stack) publishes every OS image release together with its hash, so you can match
the value in the report against that public list. That is `N2`. After this step, "this
machine" stops being the provider's word and becomes an object you can look up.

**Step three starts from a different place: you do not merely want to read a report, you want
to interact with this machine.** You want to encrypt a request to it, and
verify that what comes back really came from it. Both need keys — a public key to encrypt
to, a private key to sign with.

And **only this protected machine may be able to obtain those keys**. Otherwise anyone else
holding a copy can decrypt what you send in and forge its signature — and the protection that
steps one and two established is worth nothing.

The keys are issued by the KMS. **So the question to ask is: whose KMS is it, and what is
its release policy?** If the provider controls the KMS, it can issue itself a copy of the
same keys at any time. That is `N3`.

**Step four closes a gap nothing above reaches:** everything so far is testimony from the CPU, while the
model runs on the GPU. **A CPU report cannot speak for a GPU** — it has no visibility into
what happens on the card. So the card has to produce its own evidence. That is `N4`.

One check inside that step points ahead: **"this GPU evidence really came from this machine"** — and "this machine" is represented by a key, whose identity is not established
until Q2 (`N8`).

---

## Q2 This response came from trustworthy code and a trustworthy model

> Even granting the hardware, why should I believe what runs inside is the program I
> reviewed and the model I asked for?

**Five premises answer this question:**

| Premise | The situation it rules out |
|---|---|
| **N5** the running manifest is the one I read | you are shown one manifest and the machine runs a different one |
| **N6** the running program is the version I accept | the manifest is unchanged but the image was swapped |
| **N7** the serving model is the version I asked for | the program is right, but a cheaper model is answering |
| **N8** this response came from that program | everything on the machine is fine, and somebody replaced the response in flight |
| **N13** what an old deployment left on disk does not affect this conclusion | this deployment is fine, but it is using what a previous one left on disk |

**Derivation —**

The hardware's testimony says "some program ran under protection". **It does not say which
program.** So Q2 starts over from "which one".

**Step one pins the deployment's identity.** This stack describes a deployment with a
**manifest** (a docker compose file): which containers run, with what arguments. The hash of
that manifest is written into the part of the attestation Intel signs — so **the manifest
you read is the one it actually ran**. That is `N5`.

This step does more than itself: **from here on, anything written in the manifest is a
verifiable fact.** Several later premises build on that.

**Step two: the program version is not in the manifest.** The manifest names an image and a
version, and a version can point at different content — a restart alone can change it. So a
second mechanism is needed, one the hardware also signs. There are two, depending on the
dstack version:

- **dstack 0.5: an append-only, tamper-proof ledger** (really a measurement register in the
  CPU, called `RTMR3` in the spec; "ledger" and `RTMR3` below mean the same thing). The one
  component allowed to change the program — not the program itself — writes a record there
  **before** each upgrade, naming the new content hash; and the ledger's contents enter the
  Intel-signed report.
- **dstack 0.6 (served on 0.5 too): a state document.** 0.6 no longer lets applications write
  that register. Instead the same component computes, from the container actually running, a
  short document naming its content hash and the keys derived for it, and the 64 bytes the
  hardware signs carry that document's hash.

Either way you can read out **which version is running right now**. That is `N6`.

Who may write the ledger, or have a quote taken over a document, is governed by the manifest
— so this step stands on step one.

**The last premise of `N6` is where this chain closes, and it deserves its own note.** The
first three give you a content hash — and **a hash does not tell you which source it came
from**. Closing that gap is not a matter of trusting anyone: the image is built in GitHub
Actions, and the build signs it with cosign (recorded in Sigstore's public transparency log)
and emits a provenance attestation naming the commit it was built from. **Two `cosign
verify` invocations take you from a content hash to a specific commit, and that commit's
source is public.** Commands in `N6.4`.

**Step three is the model.** The model's version is written directly in the manifest, and the
manifest is already covered by the signature, so reading it is enough — no extra mechanism.
That is `N7`.

**Step four.** With the first three in hand, what you know is "**the machine has** the
program I reviewed and the model I asked for". **They do not say the text in your hands was
produced by that machine** — any hop in between could replace the response wholesale while
all three proofs remain correct.

Hence `N8`: every response carries a signature, under a key **only that program version, with
that set of permitted upstream destinations, can derive** — change the program or widen where
it may forward and the key changes, so the signature stops verifying immediately.

The signature covers `sha256(request):sha256(response)` —
**the request's hash is inside the signature** — so an old response replayed at you will not
match the request you actually sent. **But that requires you to compare the request hash as
well**; verifying only that the signature is valid is not enough. See `N8.1`.

**One gap is left open:** all of the above covers "this boot". And this machine inherits a
**disk that survives across deployments** — what a previous deployment wrote is still there.
Model weights live there, and they are not re-checked on load. That class of question is
answered in `N13`.

---

## Q3 My information did not leak along the way

> Could what I send be seen by somebody else — the provider, the cloud operator, or some hop
> along the way?

**This question is cut along the path the plaintext travels: four segments, end to end, so
that any instance of "somebody else read the content" lands in one of them:**

| Premise | The segment it holds |
|---|---|
| **N9** nothing in transit can read the content | from your machine to the CVM |
| **N10** only the verified program can open my request | the moment of entry — is the recipient the one you meant |
| **N11** nothing else inside the boundary can read the plaintext | after the plaintext is inside, who else can see it |
| **N12** the program inside does not write plaintext outside | whether the program itself sends it back out |

**It also uses conclusions from the first two questions:**

| Borrowed | What for |
|---|---|
| `N1` + `N2` | proving the memory protection is actually in force |
| `N5` | being able to read what is inside the boundary out of the manifest |
| `N6` | confirming which program a key belongs to |
| `N13` | old logs on disk |

**Derivation —**

**Segment one is transit.** HTTPS protects only up to the point of decryption, and **where
that point sits determines who can read the plaintext** — decrypt at a gateway and the
gateway sees everything. In this deployment **decryption happens inside the CVM**, and the
certificate's private key is generated inside it too, so the gateway and the host see only
ciphertext. That is `N9`, and "the decryption point is inside" is something you read out of
the manifest anchored by `N5`.

**Segment two is entry.** Segment one leans on "the channel is safe"; this one is stronger:
**the request is encrypted with a public key before it leaves your machine**, so how many
hops there are and who they belong to stops mattering. The price is that you must confirm
**that public key belongs to the verified program**, which needs `N6`. That is `N10`.

**Segment three is after the plaintext is inside.** There is no clever mechanism here — the
only approach is to **enumerate everything that could read it**: the physical host (cannot,
by `N1` + `N2`), the GPU side (by `N4`), anyone who could log in, and **the containers inside
the boundary other than the main program** (a database, monitoring — they share the same
boundary). That is `N11`.

**Enumerating completely is the whole point: miss one party and every proof above still
holds while the conclusion does not.**

**Segment four is the program inside writing outward.** Once the plaintext is inside, it can
be **written to logs**, **forwarded upstream**, or **stored in a database**. Each of those
destinations has to be a measured value — a literal in the manifest, or a member of the
upstream set the keys are bound to and the quote states — not a setting the provider can
quietly repoint. That is `N12`.

**Why these three segments lean on the first two questions:** confidentiality is ultimately a
claim about a **boundary** — safe inside, unsafe outside. The boundary is drawn by hardware
(Q1's business), and what is inside it and where plaintext goes is decided by the manifest
(Q2's business). **Q3's own work is enumeration: confirming there is no fifth segment.**

**One gap here too:** the disk still holds **logs written by older program versions**. Also
answered in `N13`.

---

## The order to verify in

The fourteen premises are not parallel; later ones use earlier results. Do them in this
order:

```
Step 0   Get the verification tool ready
         N0   the verifier is run by me, not called as the provider's endpoint

Step 1   Establish that the machine itself is trustworthy
         N1   the report comes from real Intel hardware      <- judged by N0's program
          |-  N2   the OS is a publicly released build         <- needs N1 first
          \-  N3   keys issued by a KMS I accept               <- needs N1 first

Step 2   Establish what is running inside
         N5   the manifest is the one I read                 <- its hash is in N1's report
          |-  N6   the program is the version I accept         <- needs N5 first
          |-  N7   the model is the version I asked for        <- needs N5 first
          \-  N8   this response came from that program        <- needs N6 first

Step 3   The GPU, separately, and it reaches back into step 2
         N4   the GPU is real and its memory is protected     <- needs N8 first

Step 4   Confidentiality: four segments of the plaintext's path, end to end

         you --N9--> reaches CVM --N10--> handed to program --N11--> inside --N12--> stays in

              N9   nothing in transit can read it
              N10  only the verified program can open it
              N11  no other process in the boundary can read it
              N12  the program does not write plaintext outside

Last     N13  what an old deployment left on disk does not affect this conclusion
         It cuts across steps 2 and 4: the model weights from step 2 and the historical
         logs from step 4 live on the same disk, so both carry a caveat from one cause.
```

**Every `<-` is a dependency:** the item on its right must hold first.

**Note the direction of the arrow in step 3** — it points from "hardware" into "code": the
GPU evidence needs a key to prove it came from this machine, and that key's identity is
settled by step 2's `N8`. **So even a reader who only cares about the hardware has to read
step 2.**

## Who, in the end, do I have to trust

**This is the question worth asking after Layer 1.** The point of the scheme is not "trust
nothing" — it is to **shrink the set of parties you must trust to a minimum, and make each
one something you can check yourself**.

| Party | Must be honest? |
|---|---|
| Intel's TDX hardware and its certificate chain | **Yes** — the cryptographic root; without it none of this exists |
| dstack's published OS image and the KMS release policy | **Yes** — but both are measured and the policy is on-chain, so you can check them |
| **The one component inside the CVM that can derive keys and can change which program runs** | **Yes** — but pinned by content hash in the manifest, and **it cannot change itself** |
| **The program that handles your request** | **No** ← this is the objective |
| **The router** | **No** |
| The provider's host, network, DNS | **No** |
| Whatever you compare against (your own list of hashes) | **Yes** — but it is a place you chose; **this is where the trust root lives** |

**How to read that table:** only three parties must be honest — **Intel, dstack, and the
component on the third row**. And **the party you most need to defend against (the program
handling your request) sits in the "No" column** — what code it runs is governed by the ledger
(or the state document) and by key derivation, not by its own word.

**Why the third row has to be trusted:** some component in a CVM has to be able to derive keys
and to change which program runs — otherwise neither upgrades nor signing could happen. And
that capability means: if the component itself has been swapped, it can run any program and
then write a record — or build a state document — internally consistent with it, and **the
whole chain looks perfectly normal**.

**And its own image is covered by no measurement — the manifest is the only thing pinning
it.**

**Which container this row corresponds to is something you determine when you read the
manifest** (`N5.3`). Different deployments can place this role differently, so Layer 1 names
only the role: **while reading the manifest, find who holds that capability, then confirm it is
pinned by content hash and that the capability is not also held by the program handling your
request.**

## Which premises are shared between questions

Five of the fourteen are used by more than one question. They are proved once, in Layer 2:

| Premise | Used by |
|---|---|
| **N6** the ledger / state document and image identity | Q1 · Q2 · Q3 |
| **N1** + **N2** hardware and OS | Q1 · Q3 |
| **N5** the anchored manifest | Q2 · Q3 |
| **N8** the signer binding | Q1 · Q2 |
| **N13** the persistent disk | Q2 · Q3 |

---

# Layer 2: discharging Layer 1's fourteen premises

`N1` and `N3` are each a single check and are not decomposed — go straight to Layer 3 for
them. The other twelve are broken down below.

## N0 the program I verify with is itself trustworthy

> Every conclusion above was computed by some program. **Why is that program trustworthy?**

| Premise | Why it is needed |
|---|---|
| **N0.1 I run the verifier myself** | otherwise the judgement comes from the party being judged |
| **N0.2 its checks are the ones dstack's KMS makes before releasing keys** | and the source is public, so you can confirm that |

**Derivation —**

**First, separate out which part actually needs a program to render a verdict.**

Intel's signature chain — `N1` — **does not**: Intel publishes the certificates and revocation
collateral the check needs, and there are several independent implementations of it, including
ones that run on-chain. **For DCAP you need not rely on any single party's verdict at all.**

**What does require a verifier are three other things, none of which are about Intel:**

| The task | Why hand-rolling it is a bad idea |
|---|---|
| replaying the boot log into the measurement registers | the mechanism is not hard, but it has to be implemented entry by entry, and **missing a step raises no error** |
| **deciding which dstack release a measurement corresponds to** | this needs a **reference table of known measurements**, and that table lives in dstack's tool — this is the main reason |
| Intel's TCB status and advisory list | requires querying Intel's firmware-rating database, and keeping up with its updates |

**The verdict on those three comes from some program, and whose program it is and how much it
covers determines how solid your conclusion is.** So it has to be a stated premise rather than
an assumption.

**The first requirement is hard: that program must be run by you.** If it is the provider's
and you merely call their endpoint, the chain is circular — you are asking the party under
examination whether to believe them. That is `N0.1`. **Note this holds regardless of which
implementation you pick** — even if you take DCAP to an on-chain verifier, the verdict on the
other three must not come from the provider.

**The second is how much it covers.** There is a ready answer: **dstack's own KMS runs the same
flow before releasing a CVM's keys.** Using it applies exactly the standard that decides
whether this machine gets keys at all — it cannot be more lenient toward this machine than the
KMS is. And it is open source, so you can confirm that by reading it. That is `N0.2`.

**Do not assemble a simplified version yourself.** Checking fewer things feels more
independent and is in fact weaker — the second and third rows above are what a hand-rolled
implementation will almost certainly miss, **and missing them produces no warning:** you will
just see a "pass".

## N2 the machine's OS is a publicly released build

> Those few hashes in the report — why do they show this machine was not assembled by the
> provider?

| Premise | Why it is needed |
|---|---|
| **N2.1 the boot log recomputes to the hashes in the report** | without this step the boot log is just a text file the provider handed you |
| **N2.2 that hash corresponds to a publicly released version** | a hash on its own says nothing; you need to be able to look it up |
| **N2.3 the machine's firmware is not on the known-defect list** | the first two can both hold while the protection is bypassed by a published flaw |

**Derivation —** The registers in the report are only a few hashes; they do not tell you what
was loaded at boot. What explains the contents is a **boot log**: each step, what it loaded,
what its hash was.

The problem is that this log arrives over plain HTTP **from the party you are examining** —
so to start with it is only an assertion. `N2.1` turns it into evidence: recompute each entry's
hash, fold them in order, and see whether the result equals the Intel-signed value in the
report. **If it matches, the log cannot have been written after the fact** — forging one that
produces the same hash means breaking SHA-256.

With a trustworthy log you can then ask "which OS release does this value correspond to" —
`N2.2`. Because dstack publishes each version's hash, you can look it up independently
instead of being told.

Finally `N2.3`: the first two can both hold while the machine still carries a published,
exploitable firmware defect. That is not forgery, it is **bypass** — the hardware protection
itself fails while every signature stays correct.

**`N2.1` is also a premise of `N6` on the ledger path** — the ledger read there is trustworthy
for exactly this reason. (The v2 path reads nothing from the event log; it anchors its document
in `report_data` instead.)

## N4 the GPU is real and its memory is protected

> A CPU report cannot speak for a graphics card. So why is the card trustworthy?

| Premise | Why it is needed |
|---|---|
| **N4.1 a change to the GPU count would be noticed** | the three below all assume the hardware configuration was not swapped |
| **N4.2 this GPU evidence came from this machine** | |
| **N4.3 this GPU evidence was generated for this attestation** | |
| **N4.4 this GPU evidence passes NVIDIA's own verification** | |
| **N4.5 ❌ the GPU was only allowed into service after passing verification** | no evidence exists for this one — but you do not need it; see below |

**Derivation —** The key background: the GPU's evidence is **appended** beside the CPU
report, and **it carries no Intel signature chain of its own**. So three substitutions have to
be blocked separately:

- **swap the source** — someone pastes in evidence from a real card on another machine.
  Blocked by `N4.2`: the whole response body carries a signature under a key belonging to the
  verified program.
- **swap the time** — replay last month's evidence from this machine. Blocked by `N4.3`: the
  evidence carries a nonce derived from this report's contents.
- **swap the content** — the evidence is forged outright. Blocked by `N4.4`: hand it to
  NVIDIA's verification and follow the chain to their root.

All three assume the configuration itself was not altered, and the GPU count participates in
the expected measurement — that is `N4.1`.

**`N4.5` is the one ❌ in the document.** By NVIDIA's design a card in CC mode boots
not-ready and refuses to compute until something explicitly marks it ready, but **no evidence
says who did that on this machine, or whether anything was verified first.**

**It does not block your conclusion.** Doing `N4.4` yourself gives you, directly, what you
wanted: the card's firmware measurements and CC state at the moment you asked. **The only
readers affected are those who rely on "the platform already verified it" and skip `N4.4`.**

## N5 the running manifest is the one I read

> What does "this deployment" even mean, and why is it the one I read?

| Premise | Why it is needed |
|---|---|
| **N5.1 the manifest hash is inside the hardware-signed report** | turns "this deployment" from an assertion into a value the hardware signed |
| **N5.2 the manifest text in my hands matches that hash** | the provider showed you this file, not a different one |
| **N5.3 this manifest is one I read and accepted** | the first two only prove "some attested deployment", not the one you want |

**Derivation —** Three links in a chain; **break one and you fall back to "some attested
deployment", which is not what the user asked for.**

The hardware signed a hash (`N5.1`) → that hash corresponds to the text in your hands
(`N5.2`) → that text is one you read and accepted (`N5.3`).

**Only the third needs a human**, and it cannot be automated — whether a manifest leaves a
back door is something you read out of it; a program cannot decide it. (There are other `👤`
steps in this document, but this one carries the most reading and the most downstream
dependencies.)

**What that one review produces is used repeatedly.** Reading the manifest also tells you:
who may write the ledger or obtain a quote, which ledger mode the controller runs in, whether
any login channel exists, how many containers are inside the boundary, and whether the
plaintext exits are literals or variables. **Most later 👤 checks draw their evidence from this
single reading** — `N5.3` in Layer 3 carries the full table.

**So do that reading against the table, item by item.** Miss one item and the corresponding
check quietly loses its evidence, **and you will get no warning.**

**And reviewing once covers every later version** — the manifest's hash is pinned by the
hardware, so if the manifest changes you find out immediately.

## N6 the running program is the version I accept

> The manifest is pinned, but the program version can change at any time. Which one is
> running right now?

| Premise | Why it is needed |
|---|---|
| **N6.1 only the controller can write the ledger, or choose what a quote commits to** | a ledger anyone may write, or a quote over anyone's bytes, may be stating an attacker's words |
| **N6.2 what you read is what the hardware signed** — ledger: records are append-only; v2: the document hashes to `report_data[32:64]` | |
| **N6.3 the state names the version running now** — ledger: the last record; v2: the document's `image` line | |
| **N6.4 the version read out is on my own accepted list** | a hash does not say which source it came from; this premise connects it to a commit |

**Derivation — these four have a strict order; reverse it and you are reading an attacker's
words.**

The background is a specific hazard: dstack puts "append a record" and "fetch a report"
behind **the same unauthenticated endpoint**, and access to it is open to every container.
Which means **any container can append a record describing itself and then take a genuine
report** — the replay matches, the manifest hash matches, the signature verifies. **The whole
chain looks entirely normal while the content is self-authored.** (This one is 0.5-only: on
0.6 nobody can append.)

**And that socket does more than write records — it also hands out keys.** `GetKey` sits
behind the same socket: **whoever holds it can derive the signing key of any image version.**

**And a third thing: whoever holds it chooses what a quote says.** `GetQuote` signs whatever 64
bytes the caller hands it — an `enc_pub` of the caller's own beside the real signer, or a state
document of the caller's own. DCAP passes; nothing in the quote says who chose the bytes. On 0.6,
with no record to compare against, nothing would catch it.

So the requirement "the broker does not mount this socket" blocks three things: writing false
records, taking keys that are not its own, and obtaining quotes over bytes of its choosing. The
broker instead gets a socket served by the controller's attestation proxy, which forwards no
`EmitEvent` and no `GetKey`, and **since #778 quotes only `report_data` it computed itself**:
the legacy and v1 layouts for the current keys, and v2 only for the document the controller
builds at that moment (`api/controller/internal/attestproxy/reportdata.go`, `state.go`).

So the order must be: first restrict the writer — only the controller may write or choose the
bytes, the broker may not (`N6.1`, read out of the `N5.3` review); then confirm what you read is
what the hardware signed (`N6.2`). **Only after those two is what the ledger or the document says
worth reading.** Then read the current version (`N6.3`).

**The last premise, `N6.4`, is where this chain closes.** The first three hand you a content
hash — and **a hash does not tell you which source it came from**. Closing that gap does not
require trusting anyone: the image is built in GitHub Actions, and the build signs it with
cosign (recorded in Sigstore's public transparency log) and emits a provenance attestation
naming the commit it was built from. **Two `cosign verify` invocations take you from a content
hash to a specific commit, whose source is public.** Commands in `N6.4`.

## N7 the serving model is the version I asked for

> I asked for this model at this version. Is that what is running?

| Premise | Why it is needed |
|---|---|
| **N7.1 the model version is pinned in the signed manifest** | without a pinned version, the same model name may be different weights next time |
| **N7.2 ⚠️ the weights loaded into VRAM are the version the manifest pins** | |

**Derivation —** This splits "which one is **declared**" from "which one actually **runs**" —
**and it is that split which lets the second half carry a caveat on its own.**

The first follows from `N5`: the version is written in the manifest, and the manifest is
covered by the hardware signature, so reading it is enough.

**⚠️ The second is not covered by this attestation.**

Note the wording: **not "the weights were swapped", but "this proof cannot speak to it".** The
version argument governs *which bytes to download*; and the weight files are large (tens of
GB), far too large to re-download every boot, so they live on a persistent disk — **and disk
contents enter no measurement.**

The library that loads them **only checks the file name on a cache hit; it does not recompute
the content hash.** So a file with the right name and replaced content would be read into VRAM
while the manifest, every signature, and the ledger **all stay unchanged**.

**But very few parties are in a position to do it:** that disk is encrypted whole, with the key
only inside the CVM — the host, the cloud operator, and anyone on the network cannot touch it. **The only
party that can write there is some deployment that was approved in the past.** That history is
fully queryable on-chain, so this caveat **can be closed**; it just costs reading more
manifests. See `N13`.

## N8 the response I received came from that verified program

> Everything so far was about what the machine has. What about the text in my hands?

| Premise | Why it is needed |
|---|---|
| **N8.1 every response carries a signature** | the premises above describe the machine, not this one response |
| **N8.2 the signing key belongs to the version in the record (or `image` line)** | a valid signature still leaves open who signed |
| **N8.3 change the program and an old conclusion self-invalidates** | |

**Derivation —** There is a signature (`N8.1`), the signer is the right one (`N8.2`), and
changing the signer invalidates old conclusions (`N8.3`).

**The middle one is the crux, and it needs two things at once.** Keys can only be derived
inside the CVM, so: the ledger record binds **an address** to **the program version it
names**; and the 64 bytes the hardware signed must point at the **same address**.

**Neither half suffices alone:**

- only `report_data` — the program can write an address of its own into it and the hardware
  will sign it regardless.
- only the ledger — the address in the ledger is public; anyone can copy and publish it.

**Both must name the same address**, and that cross-check cannot be forged: the key derives
only inside the CVM, so whoever filled `report_data` and whoever wrote the record must have
held the same one.

**v2 moves the address out of `report_data`.** The 64 bytes carry `enc_pub` and the hash of
the state document; the signer is named only by the document's `image` line. What makes that
line trustworthy is `N6.1`: the controller's proxy quotes a v2 `report_data` only over the
document it built itself, with keys it derived itself. The cross-check is then two equalities:
the `image` line's `enc_pub` equals `report_data[0:32]`, and its signer equals the provider's
on-chain, acknowledged `teeSignerAddress` (`doc/rtmr3-records.md` §8.4).

`N8.3` is the entire basis for "verify once and reuse": the key follows the program version
and the upstream set it is bound to, so the moment either changes, the address you hold stops
verifying anything. **So you do not need to re-run the full verification per request — a
failing signature is the signal.** What does not rotate the key — a config change that leaves
the upstream set alone — you see only by fetching a new quote; see `N8.3`.

## N9 nothing in transit can read the content

> From my machine to that CVM, what can each hop see?

| Premise | Why it is needed |
|---|---|
| **N9.1 the transport encryption is only opened inside the CVM** | |
| **N9.2 what the router hop can see is stated honestly** | saying only "transport is encrypted" leaves the impression nobody sees anything |

**Derivation —** HTTPS protects only up to the point of decryption, and **where that point
sits determines who sees the plaintext.** A common deployment decrypts at a gateway, and the
gateway then sees everything.

Here decryption happens inside the CVM, and the certificate's private key is generated inside
it, so the host cannot obtain it. That is `N9.1`, and "the decryption point is inside" is what
you read out of the manifest anchored by `N5`.

`N9.2` is not a guarantee but a **scope statement**: if requests pass through a router, state
honestly what the router can see. It sits at the same level as the guarantees because **the
reader needs to know where the protection stops.**

## N10 only the verified program can open my request

> Granting a safe channel, why is the other end of it the program I meant?

| Premise | Why it is needed |
|---|---|
| **N10.1 the request is encrypted before it leaves my machine** | |
| **N10.2 the public key I encrypted to belongs to the verified program** | a public key can be anybody's |
| **N10.3 that check completed before I sent anything** | the order is part of the guarantee |

**Derivation —** `N10.1` moves the security from "the channel" to "the recipient": the
request is encrypted before it leaves your machine, so the number of hops and who owns them
stops affecting the conclusion.

But the recipient is a public key, and **a public key can be anybody's**. So `N10.2` requires
that key to satisfy two things at once: it appears in the hardware-signed report, and it
matches the key bound in the ledger record or the state document's `image` line — which needs
`N6` and `N8.2`.

**`N10.3` looks like a triviality and is not.** Send first and verify after, and **by the time
you begin checking "should I hand this over", the plaintext is already handed over.** Sending
is irreversible; there is no remedy afterwards.

So the ordering here is not operational advice but part of the guarantee itself — it has to be
written as its own premise, or an implementer will put the verification wherever is
convenient.

## N11 nothing else inside the boundary can read the plaintext

> The plaintext is inside now. Inside, who can read it?

| Premise | Why it is needed |
|---|---|
| **N11.1 the host cannot read the CVM's memory** | |
| **N11.2 the host cannot read the GPU's memory either** | |
| **N11.3 this machine has no login channel** | |
| **N11.4 the containers that can see plaintext are fully enumerated** | |

**Derivation —** This section works differently from the others: it introduces no new
cryptography, it **enumerates every party that could read the plaintext** and hands each one
to a conclusion already established.

- the physical host and the cloud operator — cannot, and the proof is `N1` + `N2`, i.e. that
  CPU memory encryption really is in force.
- the GPU side — cannot, and the proof is `N4`.
- anyone who could log in — every login channel must be confirmed absent, read out of the
  `N5` review.
- **containers inside the boundary other than the main program** — a database, log
  collection, monitoring; they are inside the same boundary, **so they have to be counted.**
- **plaintext handlers the manifest does not spell out** — containers the controller may
  create after launch, and extra provider instances with their own configs; see `N11.4`.

**Completeness is the whole point: miss one party and every proof above is still correct
while the conclusion does not hold.** The last item is the one most often missed — attention
goes to the main program, while a database and a monitoring container sit in the same boundary
and see the same plaintext.

## N12 the program inside does not write plaintext outside

> Will the program inside the boundary send the plaintext back out itself?

| Premise | Why it is needed |
|---|---|
| **N12.1 ⚠️ no log level can print plaintext content** — with three stated exceptions | once inside, the first destination is a log |
| **N12.2 every plaintext exit is measured** — a signed literal, or a member of the bound upstream set | |
| **N12.3 the remaining config keys do not decide where plaintext goes** | |

**Derivation —** Once the plaintext is inside, it has **only three destinations**; enumerate
them and the segment closes:

**One: written down.** Logs used to print request bodies at debug level, and "we only print at
debug" is a promise **you cannot verify** — the log level lives in a config file, and config
file contents are not covered by the hardware signature. So the fix is not a promise but
**structural impossibility**: everywhere content would be printed now emits only a length and
a sha256 fingerprint. Three kinds of log line still carry bounded fragments of what a client or an
upstream sent; `N12.1` names them. `N12.1`

**Two: sent out.** The plaintext is forwarded to upstream model services, and written to a
database (unsealed async jobs store request and response bodies verbatim). If those addresses
live only in a config file, the provider can quietly repoint them at an external host —
plaintext leaves the boundary while **every hash, every signature and the ledger stay
unchanged**. The default upstream and the database are fixed **as literals in the manifest**
(`TARGET_URL`, `DATABASE_DSN`). But the config file can also name an upstream per model, and
those have no manifest literal; what bounds them is the **upstream set** — every destination
the running configs name, computed by the controller, bound into the signing key, and stated
in the quote. `N12.2`

**Three: decided by a switch the signature does not cover.** The remaining config keys
(prices, allowlists, concurrency limits) are indeed uncovered, but **each has been argued not
to affect anybody else's confidentiality.** `N12.3`

**That last one carries no caveat marker, and the reason matters:** it is not "proved", it is
"argued to be irrelevant to what is being proved". Listing irrelevant things as gaps would
dilute the real ones.

**⚠️ `N12.1`'s caveat has two parts:** the version you accepted prints no request or response
body at any level, but it does print three kinds of bounded fragment (a rejected parameter value; a video
request's `size`; a redacted upstream error body); **and it cannot govern bytes it did not write** — old logs on
disk.

## N13 what an old deployment left on disk does not affect this conclusion

> Every proof so far was about "this boot". What about what a previous deployment left on
> disk?

| Premise | Why it is needed |
|---|---|
| **N13.1 a channel from the old deployment to this one really exists** | |
| **N13.2 everything on that channel that gets reused is enumerated** | |
| **N13.3 widening the review to historical manifests closes the channel** | the channel can be closed, but it costs more reading |
| **N13.4 whatever cannot be closed is stated honestly** | anything unclosable must be visible, or the reader overestimates the conclusion |

**Derivation —** dstack **deliberately** decouples the disk encryption key from
`compose_hash`; the README states the purpose: "Supports application upgrades". **The
consequence is that redeploying with a new manifest reads the disk the previous deployment
left behind.**

**The evidence for this needs nothing from us:** dstack is open source, and the conditions
under which its KMS issues the same disk key are in its code — read it and you will see that
**what determines the disk key is the application's identity, not the manifest's hash**.

**Only processes inside the CVM can write to that disk** (it is encrypted, the key is inside,
the host cannot write to it). So "processes inside" means **the containers of some
deployment**. The question therefore becomes: **among the manifests that have been approved in
the past, is there one that could have left something on the disk?**

That is what `N13.3` does: widen the review from "the current manifest" to **every manifest
ever approved on-chain**. And you must read the **historical approval events**, not the
current allow-list — entries that were revoked are no longer visible, and they really did run.

**One crossing point deserves its own note, because it no longer holds.** The config file also
lives on this disk, so by the same logic an old deployment could leave its own config file for
this one to read — and the config file happens to hold "where plaintext is forwarded" and
"which database plaintext is written to". If that worked, it would **bypass all of `N12`.**

It does not work, for two reasons. **A literal in the manifest takes precedence over the same
key in the config file** (`TARGET_URL`, `DATABASE_DSN`), and the manifest's hash is in the
hardware signature (`N5`). And every other destination the file can name — a per-model
`targetUrl` — is a member of the **upstream set**, which the controller computes from the
config actually in use and which the quote states (`N12.2`); a destination left behind by an
old deployment shows there like any other. What the file can still change unseen is limited to
the keys already argued not to affect confidentiality.

**So the one crossing point that remains is logs.** The disk still holds logs written by older
program versions, and older versions did print request bodies at debug level. `N12.1` proves
"**the version you accepted** cannot print plaintext"; **it cannot govern bytes it did not
write.** Closing it is again `N13.3`: check whether any version that printed request bodies
appears in the history of approved manifests.

---

## You can stop here

**40 concrete checks: two ⚠️ and one ❌.** The shape of the chain and its weak points are now
clear; all that is left is running it.

| | Which | ① why the proof cannot cover it | ② who can actually do it | ③ how to check whether it happened |
|---|---|---|---|---|
| ⚠️ | `N7.2` weight contents | disk contents enter no measurement | **only a deployment approved in the past** — the host, the cloud operator and the network cannot touch that disk | read the on-chain approved manifests (append-only, enumerable) and confirm none could write that directory |
| ⚠️ | `N12.1` logs | the invariant governs code, not bytes already on disk; and three kinds of log line carry bounded fragments | **depends on who can read container logs** — if logs are readable, no "exploit" is needed | old logs: check whether any version that printed request bodies appears in the history; fragments: read the lines `N12.1` names |
| ❌ | `N4.5` GPU release | nothing measured records who set it, or whether anything was verified first | — | **no need to check** — doing `N4.4` yourself gives you the fact directly |

**Read the marks as applying to column ① only.** ⚠️ and ❌ mean "this attestation has nothing
to say about it"; **they do not mean "someone can attack you" (②), still less "it happened"
(③).** Columns ② and ③ exist so that reading does not happen.

**The last row differs in kind from the first two:** those are "not enough evidence, read more
and you can close it"; `N4.5` is "this proposition cannot be proved, **but your conclusion
does not pass through it**".

**Of the three, `N12.1` is the only one where ② may actually hold** — if container logs are
readable and some version really did run at debug level, those request bodies are sitting on
the disk now. **Check that one first.**

Two of the three point at the disk. That is why `N13` is written as a premise of its own: one
cause, stated once.

---

# Layer 3: how to check each one

Ordered by premise number, for lookup — you do not have to read it end to end; turn to
whichever one you are about to run.

Each entry has the same three rows:

| | |
|---|---|
| **Establishes** | what you know once this holds |
| **On what grounds** | the nature of the evidence: ⚙ hardware or cryptography · 👤 a check you make once, by hand · ❌ no evidence today |
| **How to check** | the command or assertion, runnable as written |

**The marks that matter are ⚠️ and ❌.** A chain is only as strong as its weakest link, so weak
links have to be stated in the open — a document that hides them is worse than none, because
the reader assumes a guarantee they did not get.

Both marks say "this attestation cannot cover it", not "somebody can attack you". Each entry
below states who actually could, and how to check.

## N0 the program I verify with is itself trustworthy

### N0.1 I run the verifier myself

| | |
|---|---|
| Establishes | the program judging the report's authenticity is not controlled by the party under examination |
| On what grounds | 👤 you start the container and make the request yourself |
| How to check | the commands below — the provider only supplies the report, it takes no part in the judgement |

```bash
# Your own verifier, on your own machine — 0.5.11 for dstack 0.5 CVMs; see below for 0.6
docker run -d -p 8080:8080 dstacktee/dstack-verifier:0.5.11

# From the provider, take only the report itself
curl -s -D h.txt "$PROVIDER/v1/quote?legacy=false" > q.json               # v1 layout, dstack 0.5
curl -s -D h.txt "$PROVIDER/v1/quote?report_data_version=2" > q.json      # v2 layout, the only one on 0.6

# Hand it to your copy to judge
curl -s -X POST localhost:8080/verify -H 'Content-Type: application/json' \
     --data-binary @<(jq '{quote, event_log, vm_config}' q.json) > v.json
```

`$PROVIDER` is the broker's own base URL. Through the router, use whichever path the router
maps to that provider — the router carries the quote and cannot alter it (`N1`), so where you
fetch it from does not affect the result.

**The provider appears exactly once in this flow: supplying `q.json`.** The judgement happens
on your machine.

**Two places where you will misread a failure:**

- **The request body must carry `vm_config`.** Without it you get **HTTP 200 with `is_valid`
  false** — the verifier does not know which OS image to download to compute the expected
  measurement. **The failure hides in the response body and looks like a provider problem**,
  when in fact your request was missing a field.
- **The verifier must not be older than the OS image of the machine being checked.** It looks
  the image up in a table of known measurements it ships with. **Note that using a verifier of
  the same version as the machine is exactly what does not work** — the older request format
  differs.

  **By that rule `0.5.11` cannot verify a dstack 0.6 CVM** (0.6 GPU machines run
  `dstack-0.6.1`). ⚠️ As of this writing Docker Hub carries no released 0.6 tag of
  `dstacktee/dstack-verifier` — only `0.6.0-rc0`, `0.6.0-rc3` and `latest` — and an `rc` of
  0.6.0 is older than 0.6.1. Use a verifier whose version is **at least the OS image's**; until
  one is published that means building it yourself from the dstack release matching the
  machine. This document has not run the 0.6 verifier, so the output shape below is 0.5.11's.

**Its output looks like this** (0.5.11):

```
{is_valid, reason, details:{
    quote_verified, event_log_verified, os_image_hash_verified,
    tcb_status, advisory_ids, report_data,
    app_info:{app_id, compose_hash, device_id, instance_id,
              key_provider_info, mr_aggregated, mr_system, os_image_hash}}}
```

**Everything except `is_valid` is nested under `details`.** Where the assertions below write
`D`, they mean that `V["details"]` level.

### N0.2 its checks are the ones dstack's KMS makes before releasing keys

| | |
|---|---|
| Establishes | this program's checks are complete, not a subset that merely feels more independent |
| On what grounds | 👤 read its source; it runs the same flow as dstack's KMS key release |
| How to check | source at `dstacktee/dstack-verifier`, the `/verify` path |

**Why this matters:** you might ask whether a tool from the provider's own ecosystem counts as
independent. What matters is not who wrote it, **but what it checks and whether you can verify
that.**

**And be clear about which part actually depends on it:** the DCAP signature chain has several
independent implementations to choose from, including on-chain ones — you are not confined to
this one. **What does depend on it is "which dstack release does this measurement correspond
to"** — that reference table of known measurements is inside it, and any other tool means
maintaining a copy yourself.

This verifier satisfies both:

- **Its standard is the key-release standard.** When dstack's KMS decides whether to give a
  machine its keys, it runs the same flow. So it cannot be more lenient toward this machine
  than the KMS is.
- **It is open source.** You do not have to take the previous sentence on trust.

**It does four things a hand-rolled version would almost certainly miss:** the OS image hash,
the ACPI tables, Intel's TCB status and advisory list, and the development-build
determination.

---

## A field used repeatedly: `report_data`

**This is not a proposition; it is a field reference** — `N4.2`, `N4.3`, `N6.2`, `N8.2` and
`N10.2` all take something out of these 64 bytes.

`report_data` is **the 64 bytes the hardware signature covers**. Its contents are handed to the
hardware by the program inside the CVM; the hardware signs them and does not check them (who
chooses them is `N6.1`'s business). There are three layouts:

```
v1:      enc_pub(32) || signer_addr(20) || version(4 BE) = 1 || reserved(8) = 0
legacy:  the signer address as ASCII text (42 bytes), zero-padded to 64
v2:      enc_pub(32) || SHA-256(state_document)(32)          -- no version bytes
```

v2 has no version field, so the layouts are told apart by shape (`doc/rtmr3-records.md` §8.2):

```python
rd = bytes.fromhex(D["report_data"].removeprefix("0x"))   # from the quote, not the reply's member
v1 = rd[52:56] == b"\x00\x00\x00\x01" and rd[56:64] == bytes(8)
text = rd.rstrip(b"\x00")
legacy = not v1 and len(text) > 0 and all(0x20 <= b < 0x7f for b in text)
v2 = not v1 and not legacy          # every v1 / legacy / v2 below means these
```

A v2 value lands in the v1 or legacy branch only by accident of its hash (2^-64 / not ASCII),
and a reader that asked for v2 then fails the document-hash check in `N6.2` rather than
misreading it. **Do not test `rd[52:56] == 1` alone** — on v2 those four bytes are part of a hash.

| Field | Layout | Who uses it | For what |
|---|---|---|---|
| `rd[0:32]` enc_pub | v1, v2 | `N10.2` | the recipient public key for an encrypted request |
| `rd[32:52]` signer | v1 | `N4.2`, `N8.2` | the address responses are verified against |
| ASCII signer | legacy | `N4.2`, `N8.2` | the same, as text |
| `rd[32:64]` document hash | v2 | `N6.2` | commits to the state document; the signer is in its `image` line |
| the bytes handed to the quote | all | `N4.3` | the input to the nonce in the GPU evidence |

**Which endpoint returns which layout** (`api/inference/internal/handler/quote.go`):

| Request | Layout | Notes |
|---|---|---|
| `GET /v1/quote` | legacy | the default, for clients that have not migrated |
| `GET /v1/quote?legacy=false` | v1 | falls back to the legacy quote if the v1 one was not generated — so assert `v1` |
| `GET /v1/quote?report_data_version=2` | v2 | takes precedence over `legacy`; the reply carries one more member, `state_document` (a JSON string). **404** when the deployment serves no v2 quote, and it **never falls back** to another layout; any other value is **400** |

**The legacy layout has no enc_pub field at all** — so `N10.2` requires explicitly requesting
v1 or v2, and **requires asserting the layout you asked for and refusing to fall back**.

**On dstack 0.6 request v2**: a v1 quote still carries the keys, but nothing on 0.6 names the
image they were derived for — there is no record to bind them to (`N6.3`), and only the v2
document says it. On 0.5 either works.

A v2 404 has a narrow set of causes: no attestation proxy, a controller that predates v2, a
controller that could not build the document when the broker started, or a state change landing
between the broker's fetch of the document and its quote request (the proxy then refuses the
quote). The v2 quote is taken once, at broker start, and is best effort, so a 404 lasts until
the broker next restarts (`api/common/tee/tee.go`, `syncStateQuote`).

---

## N1 the report comes from real Intel hardware

| | |
|---|---|
| Establishes | this quote comes from real Intel TDX hardware (TDX is the name of Intel's memory-isolation technology), not a string of numbers the provider typed |
| On what grounds | ⚙ the signature chain over the quote (Intel calls this system DCAP), verifying up to Intel's root |
| How to check | on the response from `N0.1`: `assert V["is_valid"], V.get("reason")` and `assert D["quote_verified"]` |

Without this, everything downstream is arithmetic on numbers the provider supplied.

**`is_valid` must be asserted on its own** — it is the verifier's overall verdict, and the
individual field checks do not cover a rejection it made for some other reason.

## N2 the machine's OS is a publicly released build

### N2.1 the boot log recomputes to the hashes in the report

| | |
|---|---|
| Establishes | the entries in `event_log` are not a text file somebody wrote, but a log that recomputes to the register values in the quote |
| On what grounds | ⚙ the verifier recomputes each entry's digest, folds it into the registers, and compares against the signed quote |
| How to check | on the response from `N0.1`: `assert D["event_log_verified"]` |

**This is a premise of `N6` on the ledger path.** `event_log` arrives over plain HTTP from the
party being described; only this step turns it from an assertion into evidence — and every record
read in `N6` comes out of **the bytes you sent to be verified**. (The v2 path reads its state from
`state_document`, anchored by `report_data` instead; `N6.2`.)

### N2.2 that hash corresponds to a publicly released version

| | |
|---|---|
| Establishes | the guest OS running is one dstack published, not one the provider modified |
| On what grounds | ⚙ `os_image_hash_verified`; 👤 you compare `os_image_hash` against the release you expect |
| How to check | on the response from `N0.1`: `assert D["os_image_hash_verified"]`, then compare `D["app_info"]["os_image_hash"]` against the release hash you recorded yourself |

**The verifier has no `os_image_is_dev` field.** This document once required checking it, which
was wrong — it does not exist in 0.5.11, and `.get("os_image_is_dev", False)` defaults to
passing, **which is worse than not checking.**

The replacement: a development build is a **different published hash**, not a boolean. The name
in `vm_config.image` carries it — e.g. `dstack-nvidia-dev-0.5.9-0e09f2bc`, with `-dev-` written
in plainly — and `vm_config` participates in the measurement computation. (On dstack 0.6 the
GPU machines run the single `dstack-0.6.1` image rather than a separate `-nvidia-` build; the
rule is the same — a `-dev-` name, or a hash you did not record, is a different build.)

**`acpi_tables_verified` does not exist either.** There is no replacement for that one today:
`quote_verified` covers the DCAP chain, and no field in the response speaks to the ACPI tables
separately.

### N2.3 the machine's firmware is not on the known-defect list

| | |
|---|---|
| Establishes | this machine's TCB is current, with no published exploitable defect |
| On what grounds | ⚙ Intel's published TCB status and advisories |
| How to check | on the response from `N0.1`: `assert D["tcb_status"] == "UpToDate" and not D["advisory_ids"]` |

**Where these two fields come from:** both are in the `N0.1` response, under `details`.
`tcb_status` is Intel's rating of this machine's firmware version; `advisory_ids` is a list of
specific advisory identifiers.

**`advisory_ids` is populated whenever `tcb_status` is anything but `UpToDate`.**

**Do not wave it through.** Each identifier corresponds to a published defect; you need to look
each one up, understand what it affects, and then decide for yourself whether to keep using
this machine. **This is a point that needs your judgement, not a warning to ignore.**

## N3 the machine's keys are issued by a KMS I accept, and the provider cannot copy them

| | |
|---|---|
| Establishes | this CVM's keys come from the KMS whose on-chain policy you read, not from somewhere else |
| On what grounds | ⚙ the verifier reads the issuing party's identity out of the signed report; 👤 you compare it against the KMS you accept |
| How to check | decode `D["app_info"]["key_provider_info"]` (**hex-encoded JSON**) and read the `id` inside |

**Two things that are easy to get wrong:** the field is `key_provider_info` (not
`key_provider`); and it is nested inside `app_info`, one level deeper than the other checks.
Its value is **hex-encoded JSON** — decode it first, then read `id`.

## N4 the GPU is real and its memory is protected

Model weights and the KV cache live in VRAM. **This whole section answers "can the host read
my VRAM", and `N1`–`N3` say nothing about it. You can confirm that yourself:** search the
verifier's response for `gpu`, `nvidia`, `cc_mode` — not one hit; the ledger's records are all
dstack's own boot events. **The CPU report simply has no field describing a graphics card.**

### N4.1 a change to the GPU count would be noticed

| | |
|---|---|
| Establishes | this machine really has N GPUs passed through, and that configuration was not altered |
| On what grounds | ⚙ `vm_config` participates in computing the expected measurement, and it carries `num_gpus` |
| How to check | read `vm_config`, e.g. `{"num_gpus":1,"num_nvswitches":0,"image":"dstack-nvidia-dev-0.5.9-…"}` (0.5); 0.6 GPU machines name `dstack-0.6.1` |

**You can confirm `vm_config` really participates:** drop it from the request and
`os_image_hash_verified` turns false. That shows the expected measurement is computed together
with it, so the `num_gpus` inside cannot be quietly changed.

**But it only establishes "a GPU was passed through"** — not that the GPU refuses host reads of
its memory.

### N4.2 this GPU evidence came from this machine

| | |
|---|---|
| Establishes | `nvidia_payload` is bytes emitted by the measured image, not something swapped in along the path |
| On what grounds | ⚙ `ZG-Quote-Signature`: the key bound by `report_data` signs **the entire response body** |
| How to check | the code below; the recovered address must equal the signer `N8.2` establishes |

**Why this premise has to exist at all:** the `quote` carries Intel's signature chain, and
`event_log` can be replayed against it (`N2.1`) — both are self-proving. `nvidia_payload` is a
blob appended beside them, and **it carries nothing of its own to show it came from this
machine.**

The result: any party on the path — including the router this document explicitly does not
trust — can substitute evidence from another genuine GPU. That evidence passes NVIDIA's
verification; it just describes a different machine. **So the whole response body gets a
signature, binding that blob to this machine too.**

```python
from eth_keys import KeyAPI
from eth_utils import keccak

# personal_sign's prefix wraps the keccak digest of the body, not the body. Reaching for
# eth_account.recover_message(encode_defunct(RAW_BODY)) recovers a different address, with
# nothing to indicate why.
sig = bytes.fromhex(HEADERS["ZG-Quote-Signature"].removeprefix("0x"))
signed = keccak(b"\x19Ethereum Signed Message:\n32" + keccak(RAW_BODY))
recovered = KeyAPI().ecdsa_recover(signed, KeyAPI.Signature(sig[:64] + bytes([sig[64] - 27])))
assert recovered.to_address() == signer
```

`RAW_BODY` must be **the bytes as received**, not a reparse-and-reserialise — the signature is
over the bytes that were sent.

**If the response has no signature header, that is not a failure of the whole verification.**
The quote, the boot log, the manifest hash and both keys all remain checkable — each carries
its own proof. **What is lost is `nvidia_payload`, and with it every conclusion about the GPU.**
You still know which program is running; you do not know what card it runs on.

### N4.3 this GPU evidence was generated for this attestation

| | |
|---|---|
| Establishes | the GPU evidence is not older evidence brought in from elsewhere |
| On what grounds | ⚙ nonce = `keccak256(the report_data handed to the quote)`, computed by measured code inside the CVM |
| How to check | the two layouts use different formulas — see below |

```python
challenged = rd[:42] if legacy else rd              # layouts from the field reference above
assert Q["nvidia_payload"]["nonce"] == keccak(challenged).hex()
```

The broker computes the nonce as `keccak256` of exactly the `report_data` it handed to the
quote (`api/common/tee/phala.go`, `TdxQuote`). **The layouts hand the quote different
lengths:** v1 and v2 hand over all 64 bytes; legacy hands over a 42-byte ASCII address which
the hardware then zero-pads to 64. **Take the 64 bytes you got back and hash them directly, and
the legacy layout never matches** — a completely honest provider fails a check you computed
wrongly, and you conclude you found something. So use the formula for the layout you actually
have.

**This blocks replay, not substitution.** The nonce is a hash of `report_data`, which is public
— anyone with a CC-mode GPU can have their card sign fresh evidence over that nonce. **What
actually pins the evidence to this machine is `N4.2`.**

### N4.4 this GPU evidence passes NVIDIA's own verification

| | |
|---|---|
| Establishes | that card is genuine NVIDIA hardware, its firmware measurements match expectations, and it is in confidential-computing mode |
| On what grounds | ⚙ the GPU attestation report's signature, chaining to NVIDIA's root |
| How to check | hand it to NVIDIA's hosted verification service (NRAS), or a locally run verifier |

```python
from nv_attestation_sdk import attestation
c = attestation.Attestation(); c.set_name("relying-party")
c.set_nonce(payload["nonce"])
c.add_verifier(attestation.Devices.GPU, attestation.Environment.REMOTE, "", "")
ok = c.attest(payload["evidence_list"])     # True on success, prints **** Attestation Successful ****
```

**This step is eight lines, far simpler than the CPU side.** The price is sending the evidence
to NVIDIA's hosted service, i.e. trusting one more party.

**That price is smaller than it looks.** What you are checking is "is this card genuine NVIDIA
hardware", and the ultimate basis for that is NVIDIA's root certificate anyway — **they are the
hardware vendor, already inside your trust boundary**; distrust them and there is nothing left
to check against.

This differs from the CPU side: the verifier for the CPU report **must not be the provider's
copy**, because the provider is the object of the check.

To stay fully local, use `Environment.LOCAL` with `nv-local-gpu-verifier`, at the cost of
maintaining an RIM cache and OCSP checking yourself.

**Posting `nvidia_payload` to NRAS as-is failed on Blackwell (B300) before #785.** NRAS checks
the certificate chain against the payload's top-level `arch`, and the broker wrote a fixed
`HOPPER` there; every evidence item then failed with `INVALID_CERTIFICATE_CHAIN`. Since #785 the
top-level `arch` follows the architecture the evidence items carry
(`api/common/tee/payload.go`, `parseGpuPayload`). A quote taken by an older broker image still
carries `HOPPER`: on a Blackwell machine, a failure there is that bug, not forged evidence. The
broker's `POST /v1/quote/verify/gpu` forwards the body to NRAS unchanged, so it behaves the same.

**⚠️ `nv-attestation-sdk` has passed its announced end-of-support date (2026-09-15)**; the SDK
warns about this itself, and the migration target is the C++ SDK.

### N4.5 ❌ the GPU was only allowed into service after passing verification

| | |
|---|---|
| Establishes | the GPU on this machine was permitted to compute only after passing attestation |
| On what grounds | **❌ no evidence** — but see below: doing `N4.4` yourself goes around it |
| How to check | cannot be checked |

NVIDIA's design: in CC mode the GPU boots not-ready and refuses to compute; something must
explicitly mark it ready. Their documentation states it plainly:

> the GPU is not automatically marked Ready after attestation. This means the user or control
> plane must explicitly set the GPU ready state.

**The driver does not "release after verifying"**, and `nvidia-smi conf-compute -srs 1`
succeeds with no attestation whatsoever.

The GPU on this machine reports `CC status: ON` / `Ready state: ready` / `Unprotected memory:
0 KiB` — states you can see for yourself by fetching GPU evidence. **The problem is not the
state, it is that no public material says who set it, or whether anything was verified
first:**

Everywhere that could be checked was, and none of it mentions the GPU:

```
all boot-time services on the system     none mentions nvidia / gpu / attest
the GPU state daemon                     not running
anywhere calling that toggle command     none
dstack's startup script                  zero mentions of GPU
the pre-launch script Phala injects      zero mentions of GPU
every record in the ledger (RTMR3)       no GPU-related entry
```

**Four of those you can read yourself** (the startup script and the pre-launch script are both
in the manifest, covered by its hash; the ledger's records are in the quote). So the conclusion
is: **setting the ready state happens outside the CVM, or at an earlier stage of the image, and
leaves no trace anywhere measured.**

**But this does not block the conclusion you want.** Think about what that gate is for: it is a
process constraint NVIDIA offers the machine's owner — verify first, then release. **You do not
need to pass through the gate to get the conclusion.**

Once you have done `N4.2`, `N4.3` and `N4.4` yourself, you hold: **at the moment you sent your
request, what this card's firmware measurements were and whether encryption was on.** That is
precisely what you wanted to know. The gate is a step somebody else performs on your behalf,
and you just performed it.

**One thing genuinely remains:** before you verified, the card may have run somebody else's
work while unverified. That affects those people, **not the confidentiality of this request** —
because what you verified is the state as of your request.

**So the only readers affected by this one are those who choose to believe "the platform
already verified it" and skip `N4.4`.**

**If Phala verified once inside the CVM and the result entered a measurement, this premise would
become checkable** — that is the smallest change that would turn it from ❌ into ⚙. On 0.5 that
could be a ledger record; on 0.6 applications can no longer append to RTMR3 ("runtime RTMR3
events are system-owned"), so it would have to be written by dstack itself, or carried in what
`report_data` commits to.

---

## N5 the running manifest is the one I read

### N5.1 the manifest hash is inside the hardware-signed report

| | |
|---|---|
| Establishes | the deployment identity this CVM claims is signed by the hardware, not self-asserted |
| On what grounds | ⚙ `compose_hash` sits in the signed report body at `mr_config_id[1:33]` |
| How to check | locate it in the quote |

In the quote's bytes, `compose_hash` sits at offset **0xb9** past the 48-byte header.
`mr_config_id`'s first byte is the prefix `0x01` and the last 16 bytes are zero padding, so the
hash itself is `mr_config_id[1:33]` — **matching what dstack's spec says, and you can locate it
in the bytes once to confirm.**

### N5.2 the manifest text in my hands matches that hash

| | |
|---|---|
| Establishes | the manifest you are about to review belongs to this quote, not a different one the provider handed over |
| On what grounds | ⚙ hash equality |
| How to check | `assert sha256(json.loads(Q["tcb_info"])["app_compose"]) == D["app_info"]["compose_hash"]` |

**This step cannot be skipped.** The verifier's request carries `quote / event_log / vm_config`
and **never `app_compose`** — so either `tcb_info` gets anchored back to the quote here, or
nothing anchors it and that manifest degrades into an unsupported claim by the provider.

These three values must be equal, and **all three are in your hands, so compute it yourself**:
`sha256(the manifest text you received)` == the `compose-hash` record in the boot log == the
quote's `mr_config_id[1:33]`.

### N5.3 this manifest is one I read and accepted

| | |
|---|---|
| Establishes | not merely "some attested deployment", but **the one you read and accepted** |
| On what grounds | 👤 compare against the hash you kept yourself |
| How to check | `assert compose_hash == REVIEWED_COMPOSE_HASH` |

**First, connect the roles to actual container names.** Layer 1 named two roles without naming
containers, because a deployment may place them anywhere. Reading the manifest is where you find
out which is which, and in this deployment they are:

| Role from Layer 1 | The container here |
|---|---|
| the program that handles your request | `0g-serving-provider-broker` — called **the broker** below |
| the one component that can derive keys and change which program runs | `0g-controller` — called **the controller** below |

**Do not take those names on faith: the point of the review is to work out which container holds
which capability**, by looking at what each one mounts. A deployment could name them anything.

**Every row below is followed by what happens if you skip it** — because skipping any row
silently removes the evidence under the corresponding premise.

| What to look at | Supports | What skipping it costs |
|---|---|---|
| the broker mounts no `dstack.sock`; only the controller does | N6.1 | the broker could both write false records **and derive the signing key of any version** |
| the controller's own image is pinned by content hash | N6.1 | **nothing measures the controller's own image** — the manifest is the only thing pinning it. An unreviewed controller can run any broker image and write a record consistent with it |
| **no container other than the broker and the event service mounts the attestation socket** (`zg-tee`) | N6.1 | that socket **signs any 32-byte hash under the broker image's key**. A fourth container mounting it can mint response proofs attributed to a reviewed image — **and the ledger records nothing about that container's existence** |
| if the ingress mounts dstack's socket at all, it is the controller's **read-only** proxy socket (`ATTEST_PROXY_READONLY_SOCKET`), in a volume that does not also hold the broker's signing socket (`ATTEST_PROXY_SOCKET`) — reachability is the volume mount | N6.1, N9.1 | `dstack.sock` lets it append records and quote any bytes; the signing socket lets it sign as the broker. An ingress still mounting `dstack.sock` (deployments from before #763) holds every capability of the controller's socket |
| the controller's `ZG_CONTROLLER_LEDGER`: absent or `rtmr3` on dstack 0.5, `none` on 0.6 | N6.3 | it decides whether records exist at all. On 0.6 without `none` every recorded change is refused, and with `recordUpstreamSet` on the controller does not start; `none` on 0.5 throws the ledger away and leaves only v2 (`api/controller/internal/ctrl/ledger.go`) |
| the controller's `ZG_CONTROLLER_ENGINES` is absent (anything but `on`) | N11.4 | with `on` the controller may create containers the manifest does not list (`POST /v1/engines`); they appear only in `zg-engine-set` / the `engines` line |
| whether the controller declares `ZG_CONTROLLER_INSTANCES`, and the broker `CONFIG_DIR` | N11.4, N12.2 | each is another plaintext handler whose config the manifest does not measure |
| no service mounts docker's data root | N6.2 | mounting it means swapping containers without going through the ledger |
| all three login channels are absent | N11.3 | somebody can log in and read memory and files directly |
| `TARGET_URL` and `DATABASE_DSN` are **literals**, not `${...}` | N12.2 | the default upstream and the database server can be quietly repointed outside the CVM |
| every container inside the boundary is pinned by content hash, not a tag | N11.4 | tags are re-resolved at every boot, so one `compose_hash` can run different content |
| whether `allowed_envs` (the allowlist of injectable environment variables) carries the two SSH variables | N11.3 | the variables themselves are unmeasured, so the allowlist is the only side you can see |
| whether the OS image name contains `-dev-` | N2.2 | a development build ships sshd, which voids the login-channel row |

**This review has to be done by a person; it cannot be handed to a program.** To judge "does
this deployment restrict who may write the ledger or obtain a quote", you have to see whether
each container in the manifest can reach that socket. There are many ways for it to do so — mounting it directly,
mounting a parent directory, sharing another container's namespace — and **that list has no
end**. Nor is there any reliable test for "which container is the main program".

The result is that a program can only ever answer "doesn't look like it". **And a check that
can only answer "probably not" is more dangerous than no check, because it reads like a
guarantee.**

**The good news is that this reading is needed once.** The manifest's hash is pinned by the
hardware, so as long as the hash is unchanged this reading's conclusions hold for every later
request; when it changes you find out immediately and read the new manifest then.

**But "once" means forward, not backward.** This deployment inherits an encrypted disk that
survives across deployments, so the review's scope also includes **every manifest ever
approved** — see `N13`.

## N6 the running program is the version I accept

### N6.1 only the controller can write the ledger, or choose what a quote commits to

| | |
|---|---|
| Establishes | the records in the ledger, and the bytes any quote commits to, can only have been chosen by the controller |
| On what grounds | 👤 read out of the reviewed manifest: the broker mounts no `dstack.sock`, only the controller's proxy socket; ⚙ changing that changes `compose_hash`, which `N5.3` catches |
| How to check | it is the conclusion of the `N5.3` review |

**Everything in `N6` rests on this one, for a concrete reason:** dstack puts `EmitEvent` and `GetQuote`
behind the same unauthenticated handler, with socket permissions 0777. So any container holding
that socket can append arbitrary records to the ledger — **including one describing the image it
is itself running** — and then obtain a genuine quote covering it: the replay matches, the manifest
hash matches, the signature verifies. **The whole chain looks entirely normal while the content
is self-authored.** (dstack 0.5 only: on 0.6 `EmitEvent` fails for every application.)

**And that socket does more than write records — it also hands out keys.** `GetKey` sits behind
the same socket: **whoever holds it can derive the signing key of any image version.**

**And it quotes whatever `report_data` it is handed.** A holder can obtain a genuine quote over
`its own enc_pub ‖ the real signer ‖ 1 ‖ 0`, or over `enc_pub ‖ SHA-256(a document of its own)`.
DCAP passes and the signer matches the chain; a client that seals to the `enc_pub` in a verified
quote seals to the holder. On 0.5 the image record's `enc_pub` catches the first; on 0.6 there is
no record, and only the confinement below stops either.

So this requirement blocks three things: writing false records, obtaining keys that belong to a
different image, and obtaining quotes over bytes of the holder's choosing.

**What the broker holds instead is the controller's attestation proxy socket**
(`api/controller/internal/attestproxy/proxy.go`). It forwards `GetQuote` and `Info` and answers
`/Sign`, `/SignerAddress`, `/GetEncKey` and `/State` itself; it never forwards `EmitEvent` or
`GetKey`. **Since #778 it does not pass `GetQuote`'s argument through**
(`attestproxy/reportdata.go`): on the broker's socket it quotes only the legacy and v1 layouts
for the keys it derives for the current identity, and v2 only for the state document the
controller builds at that moment with the `enc_pub` derived for that document's identity; on the
ingress's read-only socket, only values whose second half is zero, which no accepted layout has.
Anything else is refused with 403, and the request dstack receives is rebuilt from the validated
bytes.

**A deployment that gives `dstack.sock` only to the controller does not have this problem**, and
only then do the records — and the state document — carry real weight: each binds the addresses
of **two keys derived from the program version it names** (see `N8.2` below).

**Which way the causality runs matters here:** that binding is trustworthy only if **the main
program cannot derive keys itself**. If the main program could reach the socket, it
could write a record saying "I am version X" while filling in its own key addresses — the
binding would be internally consistent and the content false. **So it is the isolation that
makes the binding meaningful, not the binding on its own.**

### N6.2 what you read is what the hardware signed

| | |
|---|---|
| Establishes | ledger: records cannot be deleted or edited, nor forged in. v2: the state document is byte for byte the one the quote commits to |
| On what grounds | ⚙ ledger: RTMR3 only supports extension and its value enters the signed quote; `N2.1` guarantees the boot log replays to that value. v2: `report_data[32:64]` is SHA-256 of the document |
| How to check | v2: one hash; ledger: two filters, neither optional |

**v2** (the reply of `?report_data_version=2`):

```python
import hashlib
doc = Q["state_document"]                                  # a JSON string in the reply
assert v2 and hashlib.sha256(doc.encode()).digest() == rd[32:64]
```

The hash is over the exact UTF-8 bytes of the string's value (no final LF). A mismatch means the
document is not the one the quote commits to: stop.

**Ledger** (dstack 0.5):

```python
events = [e for e in json.loads(Q["event_log"])
          if e["event_type"] == 0x08000001 and e["imr"] == 3]
ledger = events[next(i for i, e in enumerate(events) if e["event"] == "system-ready") + 1:]
```

**`imr == 3` is not optional:** `event_log_verified` covers all four registers, so an entry
sitting in IMR 0–2 with a runtime type is **just as "verified" as one on RTMR3**. But only RTMR3
can be extended after boot, so only RTMR3 can carry a record a container wrote.

**Taking only entries after `system-ready` is not optional either:** before that event the
ledger holds dstack's own boot events; only entries after it can have been written by a
container.

**Without that split you get it wrong:** a forged record placed among the boot events would be
read as a normal program-version record. **Cutting at `system-ready` separates "what the system
wrote" from "what a container wrote".**

### N6.3 the state names the version running now

| | |
|---|---|
| Establishes | you can read the current image's content hash, and the two keys derived for it, out of the state document or the ledger |
| On what grounds | ⚙ v2: the `image` line, computed from the broker container actually running when the quote was taken; ledger: the last `zg-image-update` record |
| How to check | below |

```python
import base64
def unb64(s): return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4)).decode()

if v2:                                                     # dstack 0.6, or 0.5
    lines = doc.split("\n")
    assert lines[0] == "zg-state-v2"
    state = dict(l.split(" ", 1) for l in lines[1:] if not l.startswith("instance "))
    ref, bound_signer, bound_enc_pub = unb64(state["image"]).split()
    source = "state document"
else:                                                      # dstack 0.5 only
    records = [e for e in ledger if e["event"] == "zg-image-update"]
    if records:
        ref, bound_signer, bound_enc_pub = bytes.fromhex(records[-1]["event_payload"]).decode().split()
        source = "ledger"
    else:                                                  # no v2 quote, no image record since boot
        ref = compose["services"]["0g-serving-provider-broker"]["image"]
        bound_signer = bound_enc_pub = None; source = "compose"   # weaker: see N8.2
assert "@sha256:" in ref, f"{ref} from {source} does not pin a digest — refuse"
```

**The state document** (`api/controller/internal/ctrl/state.go`; grammar in
`doc/rtmr3-records.md` §8.3) has one line per kind, in fixed order, each payload base64url
without padding and in exactly the grammar of the RTMR3 record of the same kind:

| Line | States, as of when the quote was taken |
|---|---|
| `image` | `<repo>@sha256:<digest> <signer> <enc_pub>` — the digest the broker container runs, and both keys derived for it **and** the bound upstream set. `<repo>` is the controller's configured repository and is not attested; judge the digest |
| `config` | the SHA-256 of the primary config file on disk |
| `upstreams` | the upstream set the keys are bound to (`N12.2`); `unrecorded` or the payload `invalidated` means nothing bounds where plaintext goes |
| `engines` | the containers the controller created (`N11.4`); `count=0` when none |
| `instance <name>` | one per extra instance, the hash of its config file |
| `ledger` | `rtmr3` (dstack 0.5: records exist too) or `unavailable` (`ZG_CONTROLLER_LEDGER=none`, dstack 0.6) |

Every line is computed from the running deployment, not copied from the last record, so a
deployment that has changed nothing since boot still has a complete document. On `ledger rtmr3`
you may additionally replay the records and require the last record of each kind (per instance
name for `zg-instance-config-update`) to equal the document's line — a consistency check, not a
second source. A kind with no record is not a mismatch (below).

**On the ledger path, reading zero image records is normal, not an error.** RTMR3 starts empty
at every boot, and the controller writes a `zg-image-update` at boot **only when the deployment
records its upstream set** (the binding of keys to the set writes one); otherwise it writes one
only on an upgrade (`api/controller/cmd/server/main.go`, `ctrl/upstream_set.go`). So a 0.5
machine with the set unrecorded and no upgrade since boot has no image record, and the answer
is in the v2 document. Only a deployment that serves no v2 quote (the request 404s; see the
`report_data` field reference) leaves the version written in the manifest as the sole answer —
trustworthy as a manifest, because `N5.2` anchored it, but it binds no signer; see the residual
assumptions.

**You can tell the cases apart by reading:** a document, use its `image` line; records present,
use the last one. You do not need anyone to tell you which case you are in.

**If what you read is a tag (`:latest`, `:dev1`) rather than a content hash, refuse outright —
do not try to resolve it.** Resolving a tag into concrete content is done by docker on the
provider's machine, and the provider is the party you are checking; what they resolve is
unverifiable to you.

So: a deployment using a tag rather than a content hash **cannot be verified on this premise**
until it either pins a hash or records an upgrade. (The v2 `image` line always carries a
digest — the controller refuses to build a document for a broker whose digest it cannot pin.)

**After two successive upgrades the records look like this** (ledger path):

```
#1  sha256:<content hash of version A>   signer=0x<address A>   encPub=<pubkey A>
#2  sha256:<content hash of version B>   signer=0x<address B>   encPub=<pubkey B>
```

Records are appended in order. And **key derivation is deterministic** — roll back to an earlier
version (with the same upstream set) and both key addresses return exactly to that earlier pair.
The key's identity is the image digest **plus** the hash of the upstream set it is bound to
(`api/controller/internal/attestproxy/signing.go`, `KeyIdentity`; #717/#718); `N8.3`.

**But "deterministic" holds only within one machine.** The keys derive from "this machine's root
key + the program version", and the root key differs per machine. So **moving to a new machine
changes every key address even when the program version is identical** — the address you
recorded stops working and you have to verify again. That is not a malfunction; the keys are
meant to follow the machine.

### N6.4 the version read out is on my own accepted list

| | |
|---|---|
| Establishes | what runs is not just "some recorded image" but the one **you accepted** |
| On what grounds | 👤 compare against a list you kept yourself; ⚙ and every entry on that list traces back to a public build |
| How to check | `assert digest in ACCEPTED_DIGESTS`, with the list's provenance below |

**Here the chain has one blank left: you hold a content hash, and what you want to know is
"which source is this".** A hash does not answer that.

**Filling that blank is not a matter of trusting anyone — it is a matter of reading the build
record.** The image is built in GitHub Actions, and the build does two further things:

- **signs it with cosign**, recording the signature in Sigstore's public transparency log
  (anyone can query it, and entries cannot be removed after the fact)
- **emits a build provenance attestation** (SLSA) stating **which repository, which commit and
  which workflow** produced that content hash

So you can check it yourself:

```bash
# 1. This content hash really was signed by that repository's workflow
cosign verify \
  --certificate-identity-regexp="https://github.com/0gfoundation/0g-serving-broker/.*" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/0gfoundation/0g-serving-broker@sha256:<content hash>

# 2. Which commit it was built from
cosign verify-attestation --type slsaprovenance \
  --certificate-identity-regexp="https://github.com/0gfoundation/0g-serving-broker/.*" \
  --certificate-oidc-issuer="https://token.actions.githubusercontent.com" \
  ghcr.io/0gfoundation/0g-serving-broker@sha256:<content hash>
```

**The output of step 2 contains a commit, and that commit's source is public.** So the chain
connects:

```
content hash in the ledger
or the image line           ->  cosign signature (Sigstore public log)
                            ->  the commit in the provenance attestation
                            ->  source you can read
```

**Be clear about what this buys and what it does not.** It proves "this image was built from
that source, by that public workflow". **It does not prove the source is free of problems** —
that takes reading the code, and what this step does is **make "which code do I read" a question
with a definite answer.**

One honest boundary: the signature proves the build **happened** and its origin is traceable; it
does not rule out the build environment itself having been tampered with. Reproducible builds
would shrink that; they are not in place today.

---

## N7 the serving model is the version I asked for

### N7.1 the model version is pinned in the signed manifest

| | |
|---|---|
| Establishes | which model and version is served is decided by the manifest anchored by `N5` |
| On what grounds | ⚙ the manifest pins the model version (e.g. `--revision <commit sha>`), and the manifest itself enters `compose_hash` |
| How to check | read the inference engine container's arguments in `app_compose` |

**Why the version has to be pinned:** a model repository's name is a moving reference — today's
`main` and tomorrow's `main` may be two different sets of weights. Pinning the version fixes it
at a specific commit.

And the identifier a model host uses for large files is the file content's sha256, so **fixing
the commit fixes the bytes of the weights** — at least at the level of "which bytes should be
downloaded". (Whether those are the bytes actually loaded is `N7.2`'s question.)

### N7.2 ⚠️ the weights loaded into VRAM are the version the manifest pins

| | |
|---|---|
| Establishes | the weights loaded in the process are the bytes of the version the manifest pins |
| On what grounds | ⚙ the version argument and the inference engine's content hash are both measured, but **the load path does not re-check content**; closing it needs `N13`'s historical review |
| How to check | below |

**First, what this premise is not:** it is **not** "the weights have been swapped", and it is
**not** a vulnerability anyone outside can exploit. It is "**this attestation alone cannot speak
to it**" — and both the reason and the remedy are specific.

The version argument governs **download behaviour**: which version's bytes to fetch. But weight
files are large, far too large to re-download every boot, so they land on a persistent volume
(`/dstack/persistent/hf-cache`), and **the contents of a persistent volume enter no
measurement**. Therefore:

- weights already on the disk, from a different origin, may be loaded directly without
  triggering a download
- no mechanism records "which bytes were loaded" into the ledger

**You can read this behaviour yourself** — the loading library is open source; the relevant code
is `huggingface_hub` 1.27.0, `file_download.py` line 1233:

```python
if not force_download and os.path.exists(blob_path):
    return pointer_path        # exists, return it, no re-hash
```

The file name is the expected sha256, **and the content is never re-hashed**. Which means a file
with a correct name and replaced content would be loaded into VRAM as the correct weights.

**So the strongest conclusion available from reading the source is "reuse whatever is in the
cache", not "the loaded bytes hash correctly".** The gap between those two sentences is the
entire content of this ⚠️.

**Next is the most important part of this premise: who can actually do it.** That disk is
encrypted whole (Linux dm-crypt) with the key only inside the CVM. So the set of parties who
could write to that directory is much smaller than it first appears:

| Who | Can they corrupt that file |
|---|---|
| the host / cloud operator | **No** — the disk is encrypted and the key is only inside the CVM |
| anyone on the network | **No** — they cannot reach the disk at all |
| a container of this deployment | **only the inference container mounts that directory**, and its image is pinned by content hash and auditable |
| **a container of some previous deployment** | **Yes** — the encryption key survives manifest changes under the KMS mode |

**And even that last row is auditable:** `allowedComposeHashes` is on-chain and append-only
(`addComposeHash` is `onlyOwner`), so **the set of manifests ever permitted to run is complete
on-chain and enumerable**. You can request each one's text (self-proving via its hash) and review
it, confirming none could write that cache directory.

**So this is not a gap specific to weights but one instance of the persistent-disk class** — the
full account, and how to audit it, is in `N13`. Weights are worth calling out inside that class
only because they are the one persistent object whose **contents** a user cares about directly
("which model am I actually getting").

---

## N8 the response I received came from that verified program

### N8.1 every response carries a signature

| | |
|---|---|
| Establishes | **this one** response came from that image, not merely "that image runs on that machine" |
| On what grounds | ⚙ every response carries a signature by the signer over the bytes delivered |
| How to check | one signature recovery against the address `N8.2` establishes |

**Each response needs only that recovery — no quote, no replay, no DCAP.** The private key is
held by the controller, which signs on request; it never leaves the controller, so **no broker
image can carry it across an upgrade.**

**This premise also delimits what the router can and cannot do.**

**It cannot forge.** It cannot alter the quote in transit (`N1`'s Intel signature blocks that);
change one byte of a response and the signature fails. **It does not hold the signing key, so
it cannot produce a new response that verifies.**

**It cannot replay either.** The signature covers not just the response but **the request and
response hashes concatenated**:

```
signed content = sha256(request body) : sha256(response body)
```

So a genuinely-produced earlier response, offered as the answer to your current request, **has a
request hash inside it that does not match the request you sent** — you notice immediately.

**But that guarantee has a precondition the verifier must meet:** you cannot merely check that
the signature is valid; **you must also recompute the request hash from the request you actually
sent and compare it.** Verify the signature without comparing the request hash and replay works
again — because that old response's signature really is valid.

**So the only harm the router can do is not forwarding** — you get nothing. And "getting
nothing" you notice at once, which is a different kind of problem from "getting something false".

### N8.2 the signing key belongs to the version in the record (or `image` line)

| | |
|---|---|
| Establishes | the key that signed the response derives from the content hash `N6.3` read out |
| On what grounds | ⚙ v1 / legacy: the ledger record binds an address, and the quote's `report_data` must name the same one. v2: `report_data` names no address; the `image` line of the document it commits to does, and the chain must name the same one |
| How to check | below |

```python
if v2:
    signer = bound_signer.lower()                   # report_data carries no signer; the image line does
elif v1:
    signer = "0x" + rd[32:52].hex()
else:
    signer = rd.rstrip(b"\x00").decode().lower()
if bound_signer:
    assert signer == bound_signer.lower()
# The provider's on-chain service: its teeSignerAddress, acknowledged by the contract owner.
# Required on every path; on v2 it is the only thing outside the CVM the signer is checked against.
assert signer == onchain_tee_signer.lower() and onchain_acknowledged
```

**On v2, why the `image` line can be trusted for the address at all:** the controller's proxy
quotes `enc_pub ‖ SHA-256(document)` only for the document it builds itself and the `enc_pub` it
derives for that document's key identity (`N6.1`). So the signer in a document the quote commits
to was derived by the controller, for the digest on the same line. Comparing it with the on-chain
acknowledged `teeSignerAddress` is what ties "the key this CVM's controller derived" to "the key
this provider registered" (`doc/rtmr3-records.md` §8.4 step 4).

**Why both sides must be checked:** the 64 bytes of `report_data` are handed to the hardware by
the program itself — the hardware only signs them, it does not validate their contents. And the
record in the ledger was written by the controller.

**Neither half suffices alone:**

- only `report_data` — the program can write an address of its own into it, and the hardware
  signs it regardless.
- only the ledger — the address in the ledger is public; anyone can copy it and publish it.

**Both must name the same address**, and that crossing cannot be forged: keys derive only inside
the CVM, so whoever filled `report_data` and whoever wrote the record must have held the same
one.

**With neither a document nor an image record (`N6.3`'s last case: a 0.5 deployment that
serves no v2 quote and has not recorded an image since boot) this premise weakens by exactly
one notch:** the program version is still pinned by the manifest's hash, but **nothing
separately vouches for that key.**

How solid the conclusion is then depends on one thing: **whether "the version the manifest
names" really is "the one running".** The mechanism that makes that true is the controller
invalidating, during an upgrade, the label on the replaced container used for comparing against
the manifest. **Without that step**, a reboot after an upgrade has docker start the upgraded
container as before while the ledger has been cleared — so you read the reviewed version from
the manifest while **unreviewed code answers you.**

### N8.3 change the program and an old conclusion self-invalidates

| | |
|---|---|
| Establishes | having verified once, you may reuse that address rather than re-verifying per request |
| On what grounds | ⚙ the key follows the image and the bound upstream set |
| How to check | nothing extra — a failing signature is the signal |

Suppose the provider upgrades an hour after you verified. The controller derives from the image
**now** running, so it signs with a different key; the upgrade restarted the broker, so its
quote names that key too. **Your very next response fails to verify.**

**So the window in which "the program changed and you do not know" is zero responses long** —
the moment it changes, the next response stops verifying. Fetching a fresh quote per request
buys nothing extra.

**This holds entirely because the key derives per image.** If one key served every version —
which is what a deployment without per-image derivation has — a reused address would keep
verifying across upgrades indefinitely, and re-fetching the quote would not help either, since
the address in it would not have changed.

**The same holds for where plaintext may go.** A key's identity is the image digest **plus**
the hash of the upstream set it is bound to (`api/controller/internal/attestproxy/signing.go`,
`KeyIdentity`; #717/#718), so a config change that adds, removes or repoints an upstream
rotates the signer just as an upgrade does — and needs the new signer acknowledged on chain.
Binding is on whenever the set is recorded: always with `ZG_CONTROLLER_LEDGER=none`
(`ctrl/ledger.go`, `recordsUpstreamSet`), and with `controller.recordUpstreamSet` on a 0.5
deployment. A 0.5 deployment with it off binds nothing — its `upstreams` line reads
`unrecorded` — and an upstream change rotates no key there.

**But there is one thing it cannot detect: a config change that leaves the set alone.** The
program version and the set are unchanged, so the key address is unchanged and signatures keep
passing. **If such a change happens while you are reusing an address, you get no signal.**
Seeing it requires a fresh quote: on 0.5 a `zg-config-update` record, in v2 the `config` line.
A primary-config change goes through the controller's `ApplyCoreConfig`, which restarts the
broker, and the broker takes its quotes afresh at start — so the new `config` line is in the
next quote it serves. That is the one thing per-request verification actually buys.

**One staleness to know about** (`doc/rtmr3-records.md` §8.5): the broker takes its v2 quote
once, at start, and serves it until it restarts. Two changes do not restart the primary broker
— the controller creating an engine, and a config change to a container instance
(`ZG_CONTROLLER_INSTANCES`) — so the `engines` and container-`instance` lines can lag the
running deployment until the next broker restart. Both exist only with a ledger (without one
the controller refuses them). Their RTMR3 records are appended at once, but they reach you only
in a quote's event log, and that too is the one taken at start — so the ledger lags the same
way. After an engine or instance change, ask for a restart (or wait for one) before relying on
either.

**Last point: when a signature fails, do not treat it as a retryable network error.** It is a
definite notification: **the program changed.** What to do is return to `N6.4` and ask again
whether you accept this new version — and that question needs a person.

---

## N9 nothing in transit can read the content

### N9.1 the transport encryption is only opened inside the CVM

| | |
|---|---|
| Establishes | the TLS private key is inside the CVM; neither the host nor the gateway sees plaintext |
| On what grounds | ⚙ the ingress container runs inside the CVM and terminates TLS there; 👤 confirmed from the reviewed manifest |
| How to check | confirmed during the `N5.3` review |

**One cost has to be stated here:** the container that terminates TLS sees plaintext. **So it
sits inside the trust boundary, like the controller.** It also asks dstack for two things — `Info`
(the app id it publishes in a DNS TXT record) and `GetQuote` over its certificate evidence; its
certificate itself comes from Let's Encrypt, not from `GetTlsKey` (`doc/controller-design.md`).

Mounted the usual way it holds `dstack.sock`, with every capability `N6.1` lists. **Since #763
it can be given the controller's read-only proxy socket instead**
(`api/controller/internal/attestproxy/proxy.go`, `NewReadOnly`): that socket forwards only `Info`
and `GetQuote` — restricted to `report_data` whose second half is zero — and answers nothing
itself: no `EmitEvent`, no `GetKey`, no `GetTlsKey`, no signing. Which one this deployment does
is an `N5.3` row.

So its image **must be pinned by content hash, not a tag.** With a tag, the image can be swapped
while the manifest's hash stays the same — and you cannot detect it.

### N9.2 what the router hop can see is stated honestly

| | |
|---|---|
| Establishes | the visibility of the router hop is made explicit |
| On what grounds | depends on whether `N10`'s sealing is used |
| How to check | below |

**Without sealing: the router sees plaintext.** It is an ordinary HTTP proxy — TLS terminates
there once and is re-established to the CVM. `N8` guarantees it **cannot alter** a response; it
does not guarantee it **cannot see** one.

**With sealing: the router sees only ciphertext.** Only the program inside the CVM holds the
opening key.

**So `N9` alone does not answer "my information did not leak"; that requires `N10`.**

`N9.2` is not a guarantee but a **scope statement**. It sits beside the guarantees because **the
reader needs to know where the protection stops.** Saying only "transport is encrypted" without
naming the middle hop leaves the impression nobody along the way can see anything.

---

## N10 only the verified program can open my request

### N10.1 the request is encrypted before it leaves my machine

| | |
|---|---|
| Establishes | the sensitive content of the request is encrypted before it leaves your machine |
| On what grounds | ⚙ encrypted to a public key; only the holder of the matching private key can open it |
| How to check | take `rd[0:32]` from `report_data` (see the field reference above) as the recipient public key and encrypt to it |

**First, what this step is doing.** Ordinary HTTPS protects a **channel**: the content is
ciphertext along the way and is restored to plaintext at the far end — **so who the far end is
matters, and so does how many hops there are.**

This step changes the approach: **on your own machine, encrypt the request content to a public
key first.** After that, how many hops it passes through and who each one is stops mattering —
because **only the holder of the matching private key can open it**, and that private key is
derived only inside the CVM and never leaves.

The scheme used is a standard public-key encapsulation (HPKE); the specification details are in
0g-pc SPEC §5 and §6.

**This step is the easy half; the hard half is the next one:** where did that public key come
from? **If it is a value the provider simply handed you, this whole step is wasted** — you have
merely encrypted the request to the provider. Hence `N10.2`.

### N10.2 the public key I encrypted to belongs to the verified program

| | |
|---|---|
| Establishes | the key you sealed to derives from the content hash `N6.3` read out |
| On what grounds | ⚙ the enc_pub in `report_data` plus the binding in the ledger record or the `image` line |
| How to check | below |

```python
if bound_enc_pub:
    assert v1 or v2                                 # legacy has no such field
    assert rd[:32].hex() == bound_enc_pub.lower()   # v2: the image line's enc_pub
    seal_to = rd[:32].hex()
```

On v2 this is `doc/rtmr3-records.md` §8.4 step 3: `rd[0:32]` must equal the `image` line's
`enc_pub`, else stop. With neither a document nor a record there is no `bound_enc_pub`, and
nothing here lets you seal.

**Why checking only the signer is not enough:** the address bound in the ledger or the document
is **public** — it is right there in what you just read. So a process that is not the recorded image can
publish that address alongside **an enc_pub of its own**. It will never produce a valid response
signature, **but you have already sealed your request to its key.**

That is why a record binding only one of the two keys must be rejected rather than half-checked.

`/v1/e2ee/pubkey` is a convenience endpoint, **not a source of trust**. Take `enc_pub` from the
verified quote, then confirm what that endpoint returns equals it.

**These three values must be equal; compare them yourself:** the quote's `report_data[0:32]` ==
the `enc_pub` returned by `/v1/e2ee/pubkey` == the third field in the ledger record or the
`image` line. Any
mismatch means somebody is receiving your request under a key that does not belong to that
program.

### N10.3 that check completed before I sent anything

| | |
|---|---|
| Establishes | the ordering itself is part of the guarantee |
| On what grounds | logical irreversibility |
| How to check | perform `N10.2` before the first request is sent |

**If you send the request first and verify the recipient after, then by the time you begin
checking "should I hand this over", the plaintext is already handed over.** Sending is
irreversible; there is no remedy afterwards.

So the ordering here is not operational advice but part of the guarantee itself — it has to be
written as its own premise, or an implementer will place the verification wherever is
convenient.

**The two halves fail in different directions:**

| | About what | Recoverable after the fact |
|---|---|---|
| the signer check | **authenticity** | **Yes** — reject a response you have already read |
| the enc_pub check | **confidentiality** | **No** — you seal, then send; wrong key means the plaintext is already out |

**Doing both is what lets you notice a mismatch between the two sides.** Do only one and
`report_data` and the ledger (or document) become two unrelated statements — and it is precisely their
disagreement that exposes a problem: a program publishing keys of its own, or the half-record
left by an upgrade that never completed. **Check only one side and all of those pass as normal.**

---

## N11 nothing else inside the boundary can read the plaintext

### N11.1 the host cannot read the CVM's memory

| | |
|---|---|
| Establishes | the physical server, the virtualisation layer and the cloud operator's staff cannot read the CVM's memory |
| On what grounds | ⚙ Intel TDX memory encryption and access control |
| How to check | nothing extra — `N1` and `N2` together are its proof |

**This premise adds no new verification action, but it has to be listed on its own.** It is the
foundation of the whole confidentiality argument: memory encryption is done by the CPU, and "this
machine really is a genuine TDX machine with that feature on" is exactly what `N1` and `N2`
prove.

**Put differently: if Q1 holds, this holds automatically; if Q1 does not, this is moot.**

### N11.2 the host cannot read the GPU's memory either

See `N4` in full, and especially the ❌ on `N4.5`.

**Model weights and intermediate inference state both live in VRAM**, so this is not a repeat of
`N4` — it is where confidentiality (Q3) genuinely draws on the hardware side (Q1).

**It is also the weakest link in Q3 today** — because the one ❌ (`N4.5`) sits on this chain. But
as elsewhere: **do `N4.4` yourself and you do not depend on that premise.**

### N11.3 this machine has no login channel

| | |
|---|---|
| Establishes | nobody can log into this CVM to read memory or files |
| On what grounds | 👤 review the manifest and the deployment parameters; ⚙ the pre-launch script is itself covered by `compose_hash` |
| How to check | confirm all three channels are absent |

Phala **injects** a pre-launch script of its own, **and passing no `--pre-launch-script` does not
remove it**. **That script is itself in `app-compose.json`, so it is covered by `compose_hash` —
you can read it line by line while reviewing the manifest, rather than taking anyone's
description of it.** It does three things to root access:

```bash
334  echo "$DSTACK_ROOT_PUBLIC_KEY" > /home/root/.ssh/authorized_keys
338  if [[ -n "$DSTACK_AUTHORIZED_KEYS" ]]; then …
345  if [[ $(jq 'has("ssh_authorized_keys")' /dstack/user_config) == "true" ]]; then …
313  DSTACK_ROOT_PASSWORD=$(…)      # it also sets a root password
```

**Three channels, all of which have to be closed:**

```
DSTACK_AUTHORIZED_KEYS      unset
DSTACK_ROOT_PUBLIC_KEY      unset
/dstack/user_config         contains no ssh_authorized_keys
the OS image                a non-dev build (non-dev images ship no sshd)
```

**But here is a crucial asymmetry: the script is covered by the hash, while the environment
variables it reads are not.** They travel through dstack's encrypted-environment channel, filtered
by `allowed_envs` — and **a variable not on that allowlist is dropped silently** (one warning
line). So "was `DSTACK_AUTHORIZED_KEYS` set" is something **the user cannot verify from the
quote**; it can only be settled by the choice of OS image (a non-dev image ships no sshd, so a
key set there does nothing).

**That is the real basis for `N11.3`: not "no key was planted", but "a non-dev image has no
sshd" — and that is covered by `N2.2`.**

### N11.4 the containers that can see plaintext are fully enumerated

| | |
|---|---|
| Establishes | the complete set of processes that can see plaintext |
| On what grounds | 👤 read out of the reviewed manifest; ⚙ each one pinned by content hash |
| How to check | confirm in `app_compose` that every row below carries `@sha256:` |

**This premise is easily missed, because intuition only reaches for the broker.** Actually inside
the confidentiality boundary:

| Container | Why it is inside |
|---|---|
| broker | it unseals the request |
| **the inference engine container** | it receives the unsealed plaintext, and it is the code the version pinning governs |
| **the database container** (here `mysql`) | the async-job table stores unsealed request and response bodies verbatim — **not just billing metadata** |
| **the config-init container** | it holds `PRIVATE_KEY` and sits on a network with egress |
| controller | it holds dstack.sock and docker.sock |
| ingress | it terminates TLS; it holds dstack.sock, or since #763 the read-only proxy socket (`N9.1`) |

**Three more kinds of plaintext handler are not read off the manifest's service list**, and each
has to be accounted for separately:

| Handler | Where it is stated | What bounds it |
|---|---|---|
| **engines the controller creates** after launch (`POST /v1/engines`) | not in `app_compose`; in `zg-engine-set` (0.5) and the `engines` line | possible only with `ZG_CONTROLLER_ENGINES=on` in the controller's compose literal, and refused outright with `ZG_CONTROLLER_LEDGER=none` (`api/controller/internal/ctrl/engine.go`). Absent the literal, `engines` reads `count=0` |
| **container instances** (`ZG_CONTROLLER_INSTANCES`) — extra brokers, each with its own config | their containers are in the compose; their configs are not measured, and only their hashes reach `zg-instance-config-update` / the `instance` lines | refused whenever the upstream set is recorded, so always without a ledger (`ctrl/instances.go`, `parseInstances`): **their upstreams are not in the set**, so a privacy claim for a CVM declaring them does not hold |
| **in-process instances** — extra providers the broker serves from `CONFIG_DIR` | inside the broker container; one config file each, hashed into an `instance` line | their upstreams **are** in the set, tagged `provider=<address>` (`ctrl/upstream_set.go`); their database server is the `DATABASE_DSN` literal's (`api/inference/config/instances.go`) |

**So every container inside the boundary must be pinned by content hash, not a tag.** On dstack,
tags are re-resolved at every boot (`app-compose.sh` runs `docker compose pull` and then
`docker image prune -af`), so **one `compose_hash` can run different content across boots** —
which is why a tag is no constraint at all. (An engine's image is digest-pinned by the
controller's own allowlist, `controller.engines`, and named by digest in its record.)

**The database row is the one most often judged wrongly:** looking only at the billing table
suggests it holds nothing but addresses, token counts and fees, like pure metadata — whereas **the
async-job table is the one storing unsealed request and response bodies verbatim**. When judging "is the
database inside the boundary", judge by the table that stores the bodies.

---

## N12 the program inside does not write plaintext outside

### N12.1 ⚠️ no log level can print plaintext content

| | |
|---|---|
| Establishes | no log level can print the user's request or response body; the two bounded exceptions below are stated |
| On what grounds | 👤 code review — but what you are reviewing is **impossibility**, not correct configuration |
| How to check | read the source for the version you accepted; the fingerprint helper is the invariant |

**Why this cannot rest on "we promise not to print it":** the natural approach is "only print
request bodies at debug level, and production does not run debug". But the log level lives in a
config file, and **config file contents are not covered by the hardware signature** (see
`N12.3`) — so "we did not enable debug" is something you cannot verify.

**So the approach has to become "structurally cannot print":** every place in the code that would
emit request or response content emits only a **length plus a sha256 fingerprint**. Turn the level
all the way up and no plaintext appears, and **the log level falls back to being a pure
operational parameter** — it decides how much log there is, not whether plaintext is in it.

**How you confirm this:** read the source for the version you accepted and look at the function
that emits the fingerprint — it carries a test asserting that no 4-byte slice of the input appears
in the output. **What you are reviewing is impossibility, not whether a setting is right.**

**There is one more subtle leak:** error bodies from upstream services frequently echo credentials
back (e.g. `Incorrect API key provided: sk-proj-…`), and the log line recording an upstream error
is at **Error level, printed by default**.

The approach now is redaction **by value**: substitute every secret from the local configuration
out of the text. **Not by pattern matching** — "strings that look like a key" can never be
enumerated completely, whereas substitution by value misses no form of it.

**⚠️ Three kinds of log line are exceptions to "only a length and a fingerprint", and they are
stated here rather than hidden:**

| Line | Level | What it carries |
|---|---|---|
| a request profile dropping a parameter the upstream engine rejects (`api/inference/internal/ctrl/request_profile.go`) | **Info** | the **value the client chose** for that field (e.g. `reasoning_effort`, `thinking.display`), truncated to 64 bytes, with its path and the model |
| an upstream error response (`api/inference/internal/ctrl/proxy.go`, `logsafe.go`) | **Error**, printed by default | the upstream's **error body**, up to 512 bytes, with only this deployment's configured secrets redacted — and some vendors echo the request in it |
| a video job whose billing proof is skipped (`api/inference/internal/ctrl/video.go`, `video_reserve.go`, via `logProofSkip`) | **Error**, throttled | the request's free-text **`size`** value, truncated to 80 bytes |

None is a request body, and all are bounded; but the error body can carry a fragment of what you
sent, and the other two carry values you chose. Whoever can read the container logs (`GET /v1/logs` authenticates with the provider's own key)
can read these.

**This premise has a tail:** it proves "**the version you accepted** cannot print content", and
the logs on the disk were not all written by it — **bytes written by older versions are still
there, and earlier versions did print request bodies at debug level.** The invariant is about
code; it cannot govern content already on disk. **See `N13`.**

### N12.2 every plaintext exit is measured — a signed literal, or a member of the bound upstream set

| | |
|---|---|
| Establishes | who the broker forwards the unsealed plaintext to, and where it writes it, are values the user can read and that are measured |
| On what grounds | ⚙ the default upstream and the database server are **literals** in the manifest, so they enter `compose_hash`; every other upstream is a member of the upstream set, which is bound into the signing key and stated in the quote |
| How to check | find both literals in `app_compose`'s `docker_compose_file`; then read the upstream set and require every member to be inside the CVM |

| Exit | Where it is set | What goes out |
|---|---|---|
| forwarded upstream, by default | `TARGET_URL` (manifest literal; overrides `service.targetUrl`) | the entire unsealed request |
| forwarded upstream, per model | every `modelPricing[].targetUrl` in the config file — **no env override**, unmeasured by `compose_hash` (`api/inference/config/config.go`) | the entire unsealed request, for that model |
| forwarded upstream, per in-process instance | every `targetUrl` in each `CONFIG_DIR` file — `TARGET_URL` does not apply to them (`api/inference/config/instances.go`) | the same, for that provider |
| written to a database | `DATABASE_DSN` (manifest literal; also fixes every in-process instance's database **server** — only the database name may come from its file) | unsealed async jobs' request and response bodies |

**Only a literal counts** for the two manifest exits — the rule is identical for both:

```yaml
- TARGET_URL=http://<inference container name>:8000/v1                ✅ covered
- DATABASE_DSN=root:…@tcp(mysql:3306)/provider?parseTime=true         ✅ covered
- TARGET_URL=${TARGET_URL}                                            ❌ only the reference
```

**Why `${VAR}` does not count:** what enters the hash is only the fact that a variable is
referenced there; **the variable's value travels through a separate, unmeasured encrypted
channel.** And from inside the CVM the two forms are indistinguishable — the container receives
the expanded string either way.

**So this is a check only you, reading the manifest from outside, can make; the program inside
cannot make it for you.**

**The implementation point is the same for both: the environment variable's value takes
precedence over the same key in the config file.** Reverse that ordering and the unverifiable half
gets the last word. The database address needs one more step: the older legacy config key must
lose too, and it must be cleared after the override so no second, contradictory answer remains.

**Why the database address counts as a plaintext exit** (`TARGET_URL` is obvious; this one is
not):

```go
RequestBody     []byte  `gorm:"type:mediumblob"`
ResponseBody    []byte  `gorm:"type:mediumblob"`
```

The async-job table stores **verbatim bodies**, not metadata. So "where is the database" and
"where is upstream" are the same class of question.

**What happens if the address lives only in the config file:** you can confirm from the manifest
that "this deployment declares a database container running inside the CVM", **but you cannot
confirm the program connects to it.** Point the address at an external host and the plaintext
leaves the boundary while the manifest hash, `report_data`, every signature and the ledger **all
stay unchanged.**

**Changed, and invisible from outside: that is precisely why it had to be fixed** — it
overturns `N10`: you seal your request to a proven key precisely so the plaintext stays inside the
boundary.

One scope limit: the async job table is the **asynchronous path** (images, video). Synchronous
chat completions do not persist bodies; the database receives the billing table's metadata
(addresses, token counts, fees). And a **sealed** async text-to-image job stores the request
envelope and the sealed result, not plaintext (commit `1cc96ca7`; 0g-pc SPEC §7.5). **Metadata
exists on every path; plaintext only on the unsealed asynchronous one.**

**The per-model upstreams have no literal, and that is what the upstream set is for.** The
config file can send any model to its own `targetUrl`, and the file is outside `compose_hash`.
The controller therefore computes the **whole** set of destinations the broker may forward to —
the broker container's actual `TARGET_URL`, plus every `modelPricing[].targetUrl`, plus every
in-process instance's, each tagged with its provider — from the configs in use
(`api/controller/internal/ctrl/upstream_set.go`; #783 made it read the broker's own `TARGET_URL`
rather than the file's `targetUrl`). It is stated:

- on 0.5, as a `zg-upstream-set` record when `controller.recordUpstreamSet` is on;
- in v2, as the `upstreams` line — always bound with `ZG_CONTROLLER_LEDGER=none`.

**The check** (`doc/rtmr3-records.md` §4.3, §8.4 step 5): every member must be **inside the
CVM** — its URL's host has no dot and equals a service name in `app_compose`, which also tells
you the image serving it. `upstreams unrecorded`, a payload of `invalidated`, or no record at
all mean **nothing bounds where plaintext goes: not private.** A member outside the CVM is a
plaintext exit you either accept by name or refuse.

**Why a set of base URLs is a bound by origin:** the broker's forwarding client follows a
redirect only to the same scheme and host:port it first sent to; any other `3xx` is handed back
and answered as a `502` with a fixed body, so neither the body (Go replays it on 307/308) nor a
vendor credential follows it elsewhere, and the `Location` is not revealed
(`api/inference/internal/ctrl/redirect.go`; #780).

**And the set is bound into the key** (`N8.3`): changing it changes the signer, which needs the
new signer acknowledged on chain. A provider cannot widen where plaintext goes without that
visible event.

### N12.3 the remaining config keys do not decide where plaintext goes

| | |
|---|---|
| Establishes | the remaining config keys, which the hardware signature does not cover, do not affect where plaintext goes |
| On what grounds | 👤 argued item by item — note that this is not "proved", it is "argued to be irrelevant" |
| How to check | against the table below, confirm each key falls in the "does not decide where plaintext goes" class |

These keys are likewise not covered by the hardware signature. **But they do not constitute a
gap, because none of them decides where plaintext goes:**

| Key | Why it does not matter |
|---|---|
| `concurrencyLimit.*` | quality of service, nothing about where content goes |
| `inputPrice` / `outputPrice` | **the authoritative copy is on-chain**; compare against the chain, not the config |
| `whitelist.userAddresses` | who gets free usage — affects billing fairness, not anyone else's confidentiality |
| `chatCacheExpiration`, `cacheTokenBilling` | billing accounting |
| `revenueTransfer.targetAddress` | where the provider's own revenue goes, not the user's money |
| `logger.level` / `logPaths` | **no level can print a body** (see `N12.1`, and the two lines it names, both at Info or above) |
| `priceFeed.coinGeckoApiKey` | the provider's own credential |

**This one carries no caveat marker, and the reason has to be stated:** it is not "proved", it is
"argued item by item to be unrelated to what is being proved". That distinction matters — listing
irrelevant things as gaps costs the real gaps their weight.

**Not in this table: `modelPricing[].targetUrl`.** It decides where plaintext goes, so it is an
exit, handled in `N12.2` through the upstream set.

**Whether they changed behind your back is now visible**, without reading the values. For these
keys you only need to know **they were not changed**, and the v2 `config` line is the SHA-256 of
the primary config file on disk when the quote was taken; each `instance` line is the same for
one extra instance's file (`api/controller/internal/ctrl/state.go`). Compare it with the hash of
a config you reviewed, or with the previous quote. On 0.5 a change through the controller is
also a `zg-config-update` / `zg-instance-config-update` record; on 0.6 nothing records changes
between two quotes — config-only changes leave no trace except the next quote's `config` line.

---

## N13 what an old deployment left on disk does not affect this conclusion

Every premise so far is about **this** deployment: this program version, this manifest, this pair
of keys. And this machine inherits an **encrypted disk that survives across deployments**, which
raises one more class of question: **will what a previous deployment left on that disk be used
again by this one.**

**It is not asking the same thing as `N12`, though the two are easily conflated:**

- `N12` asks about **space**: where does the plaintext go (upstream, database, logs). The answer
  is entirely inside this one manifest.
- `N13` asks about **time**: does what the previous manifest left behind still count.

**The two intersect in exactly two places** — the config file and the logs, both of which live on
this disk. **The config-file intersection no longer holds** (reason below); the log one remains.

### N13.1 a channel from the old deployment to this one really exists

| | |
|---|---|
| Establishes | after redeploying with a new manifest, what you read is the disk the previous deployment left |
| On what grounds | ⚙ dstack's key derivation rule: what determines the disk key is the application's identity, not the manifest's hash |
| How to check | read the section of dstack's open source where the KMS issues disk keys — no observational data needed |

dstack's KMS mode **deliberately** decouples the disk encryption key from `compose_hash`; the
README states the purpose: "Supports application upgrades". Redeploy with a new manifest and the
key is unchanged, so **the new deployment reads the disk the old one left**.

**The basis for this is public and you can check it yourself:** dstack is open source, and the
conditions under which its KMS issues keys are in its code; that `compose_hash` does not
participate in disk-key derivation is confirmable by reading. **No observational data from us is
required.**

The disk itself is dm-crypt encrypted (`/dev/mapper/dstack_data_disk`) with the key only inside
the CVM — so **the host and the cloud operator cannot write to it.** The only things that can
write there are processes inside the CVM: the containers of some deployment, or (if a login
channel exists) somebody who logged in.

**Those two facts together are the point of this section:** nothing outside can modify this disk,
so what has to be audited is not "did somebody tamper from outside" but **whether any manifest
ever approved to run could have left something on the disk.**

### N13.2 everything on that channel that gets reused is enumerated

| | |
|---|---|
| Establishes | the things on the disk that this deployment will use as-is are fully listed |
| On what grounds | 👤 go through each persistent volume: who mounts it, and what for |
| How to check | read every volume declaration out of the manifest and check against the table below |

| Persistent object | What one old deployment could do | Affects which premise |
|---|---|---|
| the model weight cache | write a file with the right name and wrong content | `N7.2` |
| **the `config.yaml` in the config volume** | leave a config of its own — the init container's `if [ ! -f ]` guard **preserves** it | `N12.3`; its per-model upstreams show in the upstream set (`N12.2`) — see below |
| container logs (under `/var/lib/docker`) | leave logs **written by an older version**, and earlier versions printed request bodies at debug level | `N12.1` |
| the database's data directory | alter historical request records, settlement state | billing; not confidentiality |
| image layers under `/var/lib/docker` | leave a layer for some tag to resolve to | `N11.4` (this is why content-hash pinning is required) |

**The config-file row needs its own explanation. It used to be the most dangerous of the five,
and it no longer is.**

The config file also lives on this disk. So by the logic above, an old deployment could leave a
config file of its own for this deployment to read as-is — and the config file happens to hold
"who plaintext is forwarded to" and "which database plaintext is written to". If that worked, it
would **bypass all of `N12`.**

It does not work, because of the order in which config is read:

```
read config file -> migrate legacy keys -> apply TARGET_URL / DATABASE_DSN env override   <- this order
```

**The environment variable is applied last, and it is a literal in the manifest, covered by
`compose_hash`.** So a `config.yaml` left on the disk has no say even if it points
`service.targetUrl` somewhere else, nor over the database server.

**It can still name per-model upstreams** (`modelPricing[].targetUrl`), which no literal
overrides. Those are members of the upstream set, computed from the config actually in use and
stated in the quote (`N12.2`) — a destination an old deployment left behind shows there like any
other, and fails the "every member inside the CVM" check if it is outside. What the file can
still change unseen is limited to the keys in `N12.3`'s table (prices, allowlists, concurrency,
`logger.level`), **all of which have already been argued not to affect confidentiality** — and
the v2 `config` line shows that the file differs from the one you reviewed.

In other words: **`N12.2`'s fix closed `N13`'s config channel as a side effect.** That is not a
coincidence — "make the unverifiable half have no say" solves both the space and the time
direction at once.

**The genuinely remaining intersection is the log row:** the disk still holds logs written by
older versions, and earlier versions printed request bodies at debug level. `N12.1` proves "the
version you accepted cannot print content"; **it cannot govern bytes it did not write.** Closing
that is `N13.3`: check whether any version that printed request bodies appears in the history of
approved manifests.

### N13.3 widening the review to historical manifests closes the channel

| | |
|---|---|
| Establishes | this channel can be closed, by reviewing every manifest ever approved |
| On what grounds | ⚙ only processes inside the CVM can write to the disk, and those processes all come from some approved manifest |
| How to check | read the **historical** `addComposeHash` events on-chain, not the current allow-list |

Three things have to be right:

**① Read the events, not the current state**

```solidity
function addComposeHash(bytes32) external onlyOwner
function removeComposeHash(bytes32) external onlyOwner
```

**`removeComposeHash` exists.** So the current allow-list on-chain is **not** the historical
set — an owner can permit a manifest, run it once, and remove it again. Getting the complete
history means enumerating the `addComposeHash` **event log** (append-only); querying the mapping
misses exactly those.

**This is what decides whether the audit is feasible at all:** via the event log it is; via
current state it is not.

**② Two more things in the manifest are load-bearing, beyond `services:`**

```
allowed_envs contains DSTACK_AUTHORIZED_KEYS / DSTACK_ROOT_PUBLIC_KEY  -> SSH was once allowed
the OS image name contains -dev- (e.g. dstack-nvidia-dev-0.5.9)        -> that image ships sshd
```

Both are covered by `compose_hash`, so they are inside the "read the history" scope — but **a
reviewer has to know to look at them**, not only at `services:`. Either line being true for
some historical manifest means somebody could have logged into that deployment, and from there
written anything to the disk.

**③ What you are looking for is narrow**

Not "is this manifest safe in general", but the specific question: **could any container in it
have written to one of the persistent volumes in `N13.2`'s table.** That is a much smaller
question than a full review, which is what makes reading many manifests feasible at all.

### N13.4 whatever cannot be closed is stated honestly

| | |
|---|---|
| Establishes | what remains after the historical review has been done |
| On what grounds | 👤 stated plainly |
| How to check | — no runnable check; this is an explanation |

| | Cost |
|---|---|
| image content hash, manifest contents, key binding, `report_data` | **constant** — read one manifest, compare one hash |
| everything in `N13.2`'s table | **proportional to deployment history** — read every manifest ever approved |

**So the honest statement is: this class of question is closable, but its cost grows with the
deployment's history.** A record in the ledger, or a line in the state document, naming the
loaded weights would compress the weight row back to constant; that is not in place today.

---

## Residual assumptions, stated plainly

- **One step must be done by a person and cannot be automated.** Judging "does this deployment
  restrict who may write the ledger or obtain a quote" requires a human reading the manifest — the reason a program
  cannot is in `N5.3`. The good news is that this reading is needed once: the manifest's hash is
  pinned by the hardware, so if it changes you find out immediately.

- **Confidentiality is only guaranteed forward.** An image that is malicious **now** can retain
  the keys it currently holds. What per-image derivation guarantees is that **after an upgrade** it
  can no longer use them, and cannot obtain any other version's keys.

- **The v2 document is the answer on dstack 0.6, and the fallback for an empty ledger on 0.5.**
  On 0.6 there are no records at all (`ZG_CONTROLLER_LEDGER=none`). On 0.5 RTMR3 starts empty at
  every boot, and the controller writes a `zg-image-update` at boot only when the deployment
  records its upstream set; otherwise only on an upgrade. Either way the `image` line answers,
  computed from the broker container actually running, with both keys.

- **On dstack 0.6 the state is a snapshot, not a history.** Nothing records the states between
  two quotes: changes that rotate the keys leave an on-chain acknowledgement; config-only changes
  leave nothing but a different `config` line in the next quote. And a quote is only as fresh as
  the broker's last start (`N8.3`; `doc/rtmr3-records.md` §8.5).

- **With neither a v2 document nor an image record, the answer rests on the version written in
  the manifest.** That happens only on a 0.5 deployment whose controller serves no v2 quote and
  has recorded no image since boot. **That path binds no signing address** (there is nothing to
  carry one), so it holds on the condition that **"the version the manifest names" really is "the
  one running"**.

  The mechanism making that true: during an upgrade the controller invalidates the label on the
  replaced container that is used for comparing against the manifest. **Without that step**, a
  reboot after an upgrade has docker start the upgraded container as before while the ledger has
  been cleared — so you read the reviewed version from the manifest while **unreviewed code
  answers you.** That invalidation is therefore part of the chain, not incidental tidying. (The v2
  `image` line does not depend on it: it is read off the running container.)

- **A reboot rolls an in-place upgrade back.** A corollary of the above: after a reboot the
  container returns to the image pinned in the manifest.

  **So an in-place upgrade can only change "what runs until the next boot"; it cannot change the
  deployment itself.** Making an upgrade durable requires a new manifest — which changes
  `compose_hash`, i.e. a change you can see in `N5.3`.

  The rollback also moves the signing address back. **So the on-chain "the user has acknowledged
  this signing address" marker is reset a second time** — once on upgrade, once on rollback. Each
  reset needs the contract owner to acknowledge again.

- **Both halves of the response signature must be verified.** The signature covers
  `sha256(request):sha256(response)`, so it **does** block replay — provided the verifier **also
  compares the request-hash half**. Accepting on a valid signature alone gives that class back.

- **The controller is in the response path.** Every response has to pass through it for signing.
  **So the controller's availability equals the service's availability** — if it is down, the
  service cannot answer. That is the price of isolating the signing key from the main program.

- **Upgrades are not transparent to the user, deliberately.** The chain records a marker meaning
  "the user has acknowledged this signing address"; change the address and the marker is cleared,
  and setting it again requires the contract owner.

  **So every in-place upgrade necessarily requires somebody to acknowledge again on-chain.** That
  is `N8.2`'s expected cost, not a defect — **an upgrade the user cannot fail to notice is exactly
  the intended effect.**

  **The same goes for a change to the upstream set.** The key is derived for the image digest and
  the bound set's hash (`N8.3`), so adding, removing or repointing an upstream rotates the signer
  and clears the marker too — with in-process instances, for every provider address the CVM
  serves, since they share one signer. Widening where plaintext may go cannot happen without that
  on-chain re-acknowledgement. (A 0.5 deployment that does not record its set binds nothing, and
  its upstream changes rotate nothing.)

- **Who holds the on-chain allow-list decides whether that gate stops the provider.** For this
  machine to obtain keys, its manifest hash must already be on an allow-list in an on-chain
  contract — redeploy with an unapproved manifest and the machine will not start. The contract is
  public:

  ```solidity
  if !allowedComposeHashes[bootInfo.composeHash]  -> reject
  function addComposeHash(bytes32) external onlyOwner
  ```

  **But adding to the allow-list is `onlyOwner`.** So the strength of this gate depends entirely on
  who the owner is:

  | If the owner is | Who the gate stops |
  |---|---|
  | a multisig or governance contract | **the provider** — changing the manifest must be approved first |
  | the provider itself | only third parties and accidents, not the provider |

  **You can check this yourself:** read that contract instance's `owner()` on-chain, plus the
  `requireTcbUpToDate` and `allowAnyDevice` switches. **Once those three values are known, this
  assumption becomes a definite statement.** Until you check, it is a genuine residual assumption.

- **Keys surviving upgrades is by design, not a defect.** dstack has three boot modes; two of them
  require "change one byte of the manifest and get a whole new key set", and therefore **do not
  support upgrades at all**. This deployment uses the third, which deliberately decouples the keys
  from the manifest hash. **The constraint therefore moves from "hardware measurement" to "the
  on-chain allow-list"** — i.e. the assumption above.

- **The client SDK does not walk this chain yet.** Today's SDK verifies signatures against the
  address recorded on-chain and reads neither the ledger nor the state document. **Which means the guarantees `N6` and
  `N8` provide are, today, only obtainable by someone verifying by hand against this document.**
  Whoever wires it into the SDK later **must not persist the address across sessions in a config
  file** — doing so is precisely what would remove the "an old conclusion self-invalidates"
  property.
