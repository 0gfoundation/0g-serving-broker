# Controller Design Document

## 1. Overview

### 1.1 Background

In the `0g-serving-broker` deployment environment, the `0g-serving-provider-broker` and `0g-serving-provider-event` containers require frequent start/stop operations, and their configuration files need dynamic updates. Currently, these operations are hard to perform since they are deployed in TEEs.

### 1.2 Objectives

Create a subproject named `controller` that provides HTTP APIs to remotely manage target containers:

- Configuration file updates
- Container start/stop/restart
- Image updates with service synchronization
- Status queries

### 1.3 Target Containers

- `0g-serving-provider-broker` - Main broker service
- `0g-serving-provider-event` - Event processing service
- `broker-ingress` - nginx front end
- `prometheus-init` - Prometheus config init container
- `prometheus` - Prometheus

The names are constants, not configuration — see §3.1.

---

## 2. Project Structure

```text
api/
├── main.go                          # Unified entry point
├── controller/
│   ├── cmd/
│   │   └── server/
│   │       └── main.go              # Standalone entry point
│   ├── internal/
│   │   ├── handler/                 # HTTP routes and handlers
│   │   ├── ctrl/                    # Business controllers
│   │   ├── middleware/              # Middleware (auth, IP whitelist)
│   │   └── docker/                  # Docker API wrapper
```

---

## 3. Configuration Design

Controller configuration is integrated into the existing configuration file as a separate `controller` field.

### 3.1 YAML Configuration Example

```yaml
controller:
  enable: true
  port: 3090
  adminAddresses:                # Admin wallet address whitelist
    - "0x1234567890abcdef1234567890abcdef12345678"
  allowedIPs:                    # IP whitelist, empty list allows all IPs
    - "127.0.0.1"
    - "192.168.1.0/24"           # CIDR format supported
  imageRepo: "ghcr.io/0gfoundation/0g-serving-broker"   # repository only: no tag, no digest
  docker:
    host: "unix:///var/run/docker.sock"
    apiVersion: "1.41"
```

`controller.imageRepo` names a repository and nothing else. A value carrying a
`:tag` or an `@digest` is rejected at startup, because the upgrade path builds
exactly one reference — `imageRepo@<digest>` — and which image runs has to be
decided by the digest in the request, never by a tag the registry can re-point
between two pulls. A port on the registry host is fine (`localhost:5000/broker`);
only the last path segment is checked for a tag.

`controller.image` is **gone**. Nothing reads it. Like `controller.containers`
below, the key is still accepted so a config carrying it boots, and a
`[CONFIG-REMOVED]` line names it at startup — delete it.

### 3.1a `IMAGE_REPO` / `IMAGE_DIGEST`

The broker no longer holds a docker socket, and no longer asks a daemon which
image it is running. It reads two environment variables:

```yaml
  0g-serving-provider-broker:
    image: ghcr.io/0gfoundation/0g-serving-broker@sha256:<digest>
    environment:
      - IMAGE_REPO=ghcr.io/0gfoundation/0g-serving-broker
      - IMAGE_DIGEST=sha256:<the same digest>
```

Their values become `additionalInfo.ImageName` / `ImageDigest` on-chain. Either
one empty reports both as empty — the same answer the removed daemon lookup gave
when no socket was configured.

**Empty is not inert.** `buildAdditionalInfo` has no "unknown" branch: it writes
the empty pair on-chain, and if the previous value was not empty that counts as an
image change, which clears `teeSignerAcknowledged` and needs
`acknowledgeTEESignerByOwner` to restore. The controller's upgrade path cannot
cause this (`RecreateContainer` writes both variables itself), and a
controller-disabled deployment cannot either (it already reports empty). The way to
cause it is a hand-rolled `docker compose up` onto this version with a compose file
that has not added the two variables — so **add them in the same change that moves
the image**.

**The compose file must set them, and must keep them equal to the pinned
`image:`.** Nothing checks the two agree, and nothing can: the broker has no way
left to look at its own container. What makes the pair trustworthy is not the
broker asserting it but the RTMR3 record the controller writes before it
recreates the container — a reader replays that out of a signed quote
(`api/common/attest`). The on-chain fields were never evidence; the provider
writes them.

`RecreateContainer` overwrites both variables on every upgrade, from the
reference it is recreating on. Without that the broker would inherit the old
container's environment and come back up announcing the previous digest — the
contract would see no image change and keep the TEE signer acknowledgement that
an image change exists to drop.

The names of the managed containers are **not** configurable. They are constants
in `api/controller/internal/ctrl` (`0g-serving-provider-broker`,
`0g-serving-provider-event`, `broker-ingress`, `prometheus-init`,
`prometheus`). `PUT /v1/config/core` can rewrite the config file, so a name read
from there would be editable through the controller's own API; changing a
constant needs a different controller image.

A `controller.containers` key left over from an earlier release is **accepted and
ignored**, and a `[CONFIG-REMOVED]` line naming it is logged at startup. It is
not rejected on purpose: this config struct is shared with the broker and event
binaries, so refusing the key would stop all three from booting. Delete it anyway
— it steers nothing.

The docker layer additionally refuses to start, stop, restart, remove, recreate
or exec into the controller's own container, identified by matching
`os.Hostname()` against container IDs.

**This requires the controller to run as a container with docker's default
hostname.** Do not set `hostname:` on the controller service and do not use
`network_mode: host`. Where the hostname does not resolve to a container ID, the
operations listed above are refused and the error names the hostname; reads are
unaffected. `PUT /v1/config/core` writes the config file before it restarts
anything, so in that state it returns 500 with the file already rewritten.

### 3.2 Environment Variable Support

Both whitelists can be configured via environment variables, which **replace**
the config file's values rather than adding to them:

```bash
# Multiple entries separated by commas
ADMIN_ADDRESS=0xaddr1,0xaddr2,0xaddr3
ALLOWED_IPS=127.0.0.1,192.168.1.0/24
```

Set these in the compose file. §4.4 covers what that does and does not close.

---

## 4. API Design

### 4.1 Container Management API

| Method | Path                          | Description               |
| ------ | ----------------------------- | ------------------------- |
| GET    | `/v1/containers`              | Get status of all managed containers |
| GET    | `/v1/containers/:name`        | Get status of specific container |
| POST   | `/v1/containers/:name/start`  | Start container           |
| POST   | `/v1/containers/:name/stop`   | Stop container            |
| POST   | `/v1/containers/:name/restart`| Restart container         |

### 4.2 Configuration Management API

| Method | Path                      | Description                    |
| ------ | ------------------------- | ------------------------------ |
| GET    | `/v1/config/core`         | Get the config file shared by broker and event |
| PUT    | `/v1/config/core`         | Write that file and restart broker + event |
| GET    | `/v1/config/ingress`      | Report the ingress container's environment |
| GET    | `/v1/config/prometheus`   | Get the Prometheus config (base64) |
| PUT    | `/v1/config/prometheus`   | Rerun prometheus-init with a new config |

**Breaking change**: `PUT /v1/config/ingress` has been **removed** and now returns
**404**, indistinguishable from an unknown path.

What the ingress routes traffic to is not something a running deployment should be able
to change. It was editable through this API, against a whitelist that included
`TARGET_ENDPOINT`, `DOMAIN` and `GATEWAY_DOMAIN` — so an admin wallet could repoint the
front end at a different upstream — and it left no record anywhere. Those values now come from the compose file only, which means
`compose_hash` covers them: a reader can see what they are, and they cannot change without
the deployment's identity changing.

`GET /v1/config/ingress` stays. `config.IngressAllowedEnvKeys` still narrows what it
reports, but it is a display filter now — it keeps a token or a certificate out of the
response, not a caller out of the container.

Changing an ingress value means editing the compose file and redeploying, exactly as with
the container names (§3.1) and the two whitelists (§4.4).

### 4.3 Image Management API

| Method | Path                | Description                                           |
| ------ | ------------------- | ----------------------------------------------------- |
| POST   | `/v1/images/update` | Pull `imageRepo@<digest>`, rebuild containers, sync service to contract |
| GET    | `/v1/images/info`   | Get information about the image the broker is running |

`POST /v1/images/update` takes the digest and nothing else:

```json
{ "digest": "sha256:<64 lowercase hex characters>" }
```

**Breaking change**: the body used to be empty and the image came from
`controller.image`, so the endpoint pulled whatever the configured tag pointed at.
A request with no `digest`, or one that is not `sha256:` followed by 64 lowercase
hex characters, now returns **400** and touches no container. The repository still
comes from config (`controller.imageRepo`); only the digest is caller-supplied,
so there is no request shape that pulls by tag.

The digest is validated before anything is stopped, removed or created — a
malformed one cannot leave the deployment half-upgraded with the event container
already down. It is then recorded in RTMR3 before the pull (§5.1a); if the record
fails the request returns **500** and nothing was touched, and if another change
holds the controller it returns **409** (`PUT /v1/config/core` shares that lock).

`GET /v1/images/info` reports the image the **broker container** is running,
read off the container rather than off config. It used to inspect the configured
reference; that stopped being answerable once the config became a bare
repository, since docker resolves a bare name to `:latest` and pulling
`repo@digest` never creates that tag.

### 4.4 Admin Whitelist API

| Method | Path | Description |
|--------|------|-------------|
| GET    | `/v1/admin/wallets` | Get current admin wallet address list |
| GET    | `/v1/admin/ips` | Get current IP whitelist |

Read-only. Both whitelists are read once at startup — from config, or from
`ADMIN_ADDRESS` / `ALLOWED_IPS` where those are set (§3.2) — and there is no
longer any API that edits them: the wallet list is the authorisation boundary for
every other route here, so a route that widened it would be a way to escalate
past it. Changing either one means restarting the controller with a new value;
if the environment variable is set, editing the config file alone will not do it.

**This closes the direct route only.** `PUT /v1/config/core` validates YAML syntax
and nothing else, and it writes the same file the controller loads. A caller
holding one admin wallet can write `controller.adminAddresses`,
`controller.allowedIPs`, `controller.imageRepo` or `controller.docker.host` there,
and the controller reads them at its next start. (`PUT /v1/config/ingress` used to be a
second such route, reaching the ingress upstream; it is gone — see §4.2.) `ADMIN_ADDRESS` / `ALLOWED_IPS`
override the first two (§3.2); nothing overrides the other two. **Set those env
vars in the compose file** — and note that no code path enforces a floor on the
number of remaining admins.

Revocation is offline. Nothing invalidates a leaked admin session token while the
controller runs, and `SessionToken` treats `ExpiresAt: 0` as never expiring.
Bounding token lifetime is tracked separately.

Read the container guard as a safety interlock, not containment. What a
`PUT /v1/config/core` write reaches is now narrower than it was: the broker reads
neither `controller.imageRepo` nor `controller.docker.host` any more — its image
identity comes from `IMAGE_REPO` / `IMAGE_DIGEST`, which only the compose file and
the controller's own recreate path set, and neither is reachable through this
config file. The controller's copy of `controller.imageRepo` waits for the
controller to restart, which no route here triggers.

Writing `controller.imageRepo` does not by itself choose an image: the digest
still comes from the request, so the reachable outcome is an upgrade to the same
digest out of a different repository — a mirror the caller controls.

**Breaking change**: `POST /v1/admin/wallets`, `DELETE /v1/admin/wallets/:address`,
`POST /v1/admin/ips` and `DELETE /v1/admin/ips/:ip` have been removed and now
return 404, indistinguishable from an unknown path. Neither `/v1/admin/ips`
write route ever affected traffic: enforcement
uses the startup snapshot held by `IPWhitelistMiddleware`, so both only ever
edited what `GET /v1/admin/ips` reported.

---

## 5. Core Workflows

### 5.1 Container Startup Order

Based on container dependencies:

1. `0g-serving-provider-broker` depends on `mysql` healthy
2. `0g-serving-provider-event` depends on `0g-serving-provider-broker` healthy

Therefore, restart order should be:

- **Stop**: event → broker
- **Start**: broker → event (wait for broker healthy)

### 5.1a RTMR3 accounting

The controller records the broker's image and the shared config file in **RTMR3
before changing either**. RTMR3 is append-only hardware state: an event folded into it cannot be
edited or removed, and it is covered by the signature over any quote taken
afterwards — so the record outlives the process that wrote it and does not depend
on that process being honest later.

| event | payload (bare bytes, not JSON) | emitted before |
|---|---|---|
| `zg-image-update` | `<repo>@sha256:<64hex>` — the reference the upgrade runs on | `POST /v1/images/update` touches any container |
| `zg-config-update` | `hex(sha256(<config file content>))` | `PUT /v1/config/core` writes the file |

**Not everything is covered.** `PUT /v1/config/ingress` and
`PUT /v1/config/prometheus` change in-TEE state with no record at all, and
`TARGET_ENDPOINT` is on the ingress whitelist — so the ingress upstream can be
repointed without a trace. That gap predates this accounting and closing it needs a
third event, i.e. a wire-contract change; tracked separately. Read the guarantee
below as being about the broker's image and its core config, nothing wider.

Three properties this rests on, none of them visible from the call site:

- **Payloads are bare bytes.** They go into the event's SHA-384 digest, and a
  verifier has to reproduce them exactly. JSON leaves key order, spacing and
  escaping free; a bare string does not. Changing a name or an encoding changes
  the measurement and breaks every existing reader.
- **`zg-` is a namespace.** dstack already writes `app-id`, `compose-hash`,
  `os-image-hash` and `system-ready` into RTMR3, and other components may add
  their own.
- **A quote is sealed when a broker process starts.** The broker takes its quote
  once at startup and caches the whole quote / event-log / tcb_info triple for its
  lifetime, so an event emitted while it runs reaches no reader until it restarts.
  This cuts both ways: it is why both paths above recreate or restart the broker, and
  it is why the ledger has to be true *at those instants* rather than merely
  eventually.

#### What this does not prove, and why

**Any container holding `/var/run/dstack.sock` can write RTMR3 directly.** dstack's
guest agent serves `EmitEvent` on that socket from the same unmediated handler as
`GetQuote` and `GetKey`, and binds it `0777` — "Allow any user to connect to the
socket" (`guest-agent/src/main.rs`, `rpc_service.rs`). There is no authorization and no
restriction on the event name or payload. The broker mounts that socket because it
needs `GetQuote` and `DeriveKey`.

So a provider running a modified broker image can append its own `zg-image-update`
naming any digest and then take a quote over it. Payloads are unauthenticated bare
bytes, and `report_data` carries no nonce, so such a quote replays forever. **No
placement of the controller's record changes that**, and neither does locking the
container routes: the writer and the adversary are the same process.

The quote it produces is genuine — the replay matches, the compose hash matches, the
signature verifies. Nothing on the reader side can tell it from a real upgrade, because
the payloads are unauthenticated bytes and any container on the socket can derive the
same keys, so there is no signature to check.

Neither a signature on the record nor a challenge nonce fixes that. There is no key to
sign with that the broker cannot also derive (`GetKey` derives by path and any container may
ask for any path), and freshness binding defeats *replay* of an old quote, not forgery of a
new one — the modified image forges the record and then takes a fresh quote carrying the
verifier's own nonce.

**Whether the ledger is confined is a property of the compose file, and the caller is the
one who can settle it.** `attest.ResolveRunningState` returns `ComposeHash`, the hash of the
compose the CVM actually booted, bound to the quote by hardware. Compare it against the hash
of a compose you published and reviewed, and you know — by reading a document you control —
whether anything upgradeable can reach that socket. Same discipline as the expected-digest
set (§D3): the answer comes from software the user installed, never from the party being
checked.

> **Tried and reverted: having `attest` derive this from `app_compose`.** It looks like it
> should work, since `compose_hash` authenticates the compose text. It does not. The mount
> shapes that reach a socket are an open set — a whole-directory bind (`/var/run:/var/run`),
> a named volume with a bind `driver_opts`, a YAML alias, `extends:`, or an interpolation
> that splits the path, since `app_compose` stores the compose text *uninterpolated*. And the
> fields that would identify the broker are the wrong ones: the controller locates containers
> by `container_name`, while a compose `services:` key is free to differ, and in this
> project's own compose the controller shares the broker's **image string** and differs only
> by `command:` — so the naive sibling test refuses the very shape it was meant to bless. A
> parser that answers "probably not" here is worse than none, because it reads as a
> guarantee. Do not re-add it.

So: **until a deployment keeps that socket away from the broker, treat an event-sourced
digest as an audit record of what the controller did, not as proof of what is running.** The
way to make it proof is to route the broker's quotes through the controller — its image is
pinned by `compose_hash` and it cannot upgrade itself (§4.4), so a ledger only it can write
is worth reading. That spans this repo and `deploy` and makes the broker depend on the
controller at startup, so it is a decision rather than a detail.

What the accounting below buys in the meantime is not nothing, and it is what motivated the
work: an in-TEE upgrade can no longer be performed silently, or misreport itself by accident.

#### The invariant, stated properly

> The **last** image record names the image the broker will be running when it next
> serves a quote, and the last config record names the file it will read.

Three things hold it up, and each one is load-bearing.

**1. The record comes before the change.** That is what makes an unrecorded change
impossible — the silent-upgrade failure. A failure to record aborts with every
container untouched.

**2. The record comes as late as it can — once no broker is running.** For the image
path that means *after both containers are stopped*, immediately before the create.

A reader believes the last image record, so the ledger has to be true at every instant a
quote can be taken — and a quote can be taken by whatever image is currently running.
Against a *modified* broker that is unwinnable (see above); against an unmodified one it
means the record must not exist while a broker is alive that is not on the new
reference, because an ordinary restart in that window — a crash, an OOM kill, docker's
restart policy — would seal a quote around a ledger that does not describe it.

So the record must not exist while a broker is alive that is not on the new reference.
At the point it is written, none is: both containers were just stopped, docker's
restart policy does not fire on a deliberate stop, and the routes that could start one
are behind the **same lock** the upgrade holds. The next broker to exist is the one
created on the new reference. (That lock is the only reason those routes have one; they
record nothing themselves. A caller arriving mid-change gets **409**, nothing touched.)

Earlier placements all failed that test — at the top of the call, or after the pull,
the still-running old image could collect a quote naming an image it was not running,
and with the pull unbounded and failable on demand that was a window a caller could
hold open. A record that cannot be written restarts the broker rather than leaving it
stopped: nothing was recorded, so the ledger is still truthful, and a dstack hiccup
must not become an outage.

An upgrade also **refuses to start** unless both managed containers resolve by their
exact names. Container lookup otherwise falls back to a shortest-substring match, which
is fine for a status endpoint and destructive here: once an attempt has removed the
broker container and failed to create a replacement, the shortest remaining name
containing `0g-serving-provider-broker` is `0g-serving-provider-broker-db` — the
database in this project's own compose file. A retry would stop it, record an image
change, and recreate the database container running the broker image, reporting success.
**Set `container_name:` on both services**; the error names the missing one.

Both recorded paths also run on a **detached, bounded** context. Detached so a client
disconnect cannot abort a change half way, or make the record and the restore fail
selectively — that would be the caller choosing which half of the accounting happens.
Bounded because this lock now gates start/stop/restart too, and `PullImage` has no
timeout of its own; without a ceiling a registry that never answers would leave an
operator unable to so much as restart the broker to recover.

**3. A record must not outlive the change it describes.** RTMR3 cannot be rewound, so
the abort paths *append the truth*: the two stops and the recreate re-read the
reference off the broker container and record it; a config write that fails re-reads
the file and records its actual hash. A reader takes the last record, so appending
restores the answer.

Leaving a stale record instead is **not** the conservative direction. It names the
digest the caller asked for — for an attacker, exactly the digest a verifier is
looking for — while the broker keeps running whatever it ran before. Install an older
*published* image, then fail an "upgrade" to the current one, and the ledger names the
current release while a superseded build serves traffic. "A reader would reject it"
only holds when the stale record names something the reader was **not** looking for.

For the same reason the restore **fails closed**: every outcome emits something, and
when the truth cannot be established — no broker container, an unreadable file, a
name that resolved to a neighbouring container — the payload deliberately names no
digest or no hash, which `attest.ResolveRunningState` refuses. Refusing beats
believing the record being replaced. Only the emit itself failing can leave the ledger
overstating, and then the API error says so; an operator has to resolve it.

Such a record is fatal to the reader only when it is the **last** one of its kind. An
earlier one is history that a later correct record supersedes — otherwise a single
transient docker error would make a CVM unverifiable for the rest of its boot, since
RTMR3 only resets on a reboot.

Boundary: once the broker has been recreated on the new image the record is already
correct, so the remaining failures (health wait, ingress reload, event container,
contract sync) must *not* restore — that would replace a true record with a false one.

`controller/internal/ctrl/rtmr3_test.go` covers all three, the boundary, and the
fail-closed path.

`zg-config-update` records **behaviour, not just code**. Pricing, verifiability
and `targetUrl` all live in that config file; an image digest alone would leave
them changeable without a trace.

Both recorded paths are serialized against each other **and against
`POST /v1/containers/:name/{start,stop,restart}`**; a caller that arrives mid-change
gets **409** rather than being queued, with nothing touched. RTMR3 is a ledger — the
last `zg-image-update` in it is what a reader believes is running — so two interleaved
upgrades would leave the last event and the last container created coming from
different requests, and a bare start slipped into the middle would seal a quote around
a ledger that does not describe the container it just started.

This requires `/var/run/dstack.sock` mounted into the controller. It is not
dialled at startup, so the read-only endpoints keep working without it; an upgrade
attempted without it fails at the record, before anything is touched.

The reader for this ledger is `api/common/attest`, in this repository so that the
format and the reader cannot drift apart —
`attest.ResolveRunningState(quote, event_log, tcb_info, brokerService)` replays
RTMR3, anchors the event log and the compose file back to the signed quote, and
answers which image and config are running. Both sides take the event names from
`attest`. It stops short of verifying the quote's DCAP signature and of judging
whether the answer is acceptable; the digest list for the latter has to come from
software the *user* installed, never from the provider being checked.

### 5.2 Image Update Workflow

Step 1 below is preceded by the `zg-image-update` record described in §5.1a.

```text
┌─────────────────────────────────────────────────────────────┐
│                     Image Update Workflow                    │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
                   ┌──────────────────┐
                   │  Step 1:         │
                   │  Docker Pull     │
                   │  imageRepo@      │
                   │  <digest>        │
                   └──────────────────┘
                              │
                  ┌───────────┴───────────┐
                  │                       │
                  ▼                       ▼
           ┌──────────┐            ┌──────────┐
           │  Pull    │            │  Pull    │
           │  Success │            │  Failed  │
           └──────────┘            └──────────┘
                  │                       │
                  │                       ▼
                  │                ┌───────────┐
                  │                │  Return   │
                  │                │  Error    │
                  │                └───────────┘
                  ▼
         ┌──────────────┐
         │ Step 2:      │
         │ Sync service │
         │ to contract  │
         │ (update      │
         │ image info)  │
         └──────────────┘
                  │
                  ▼
         ┌─────────────┐
         │ Step 3:     │
         │ Stop event  │
         │ container   │
         └─────────────┘
                  │
                  ▼
         ┌─────────────┐
         │ Stop broker │
         │ container   │
         └─────────────┘
                  │
                  ▼
         ┌─────────────┐
         │ Step 4:     │
         │ Rebuild     │
         │ broker      │
         │ (preserve   │
         │ config)     │
         └─────────────┘
                  │
                  ▼
         ┌─────────────┐
         │ Wait for    │
         │ broker      │
         │ healthy     │
         └─────────────┘
                  │
                  ▼
         ┌─────────────┐
         │ Rebuild     │
         │ event       │
         │ (preserve   │
         │ config)     │
         └─────────────┘
                  │
                  ▼
         ┌─────────────┐
         │ Return      │
         │ update      │
         │ result      │
         └─────────────┘
```

**Key Steps Explained**:

1. **Pull the requested image**: Pull `imageRepo@<digest>`, the digest coming from
   the request body. Nothing resolves a tag, so the image that gets run is the one
   that was asked for, and a failed pull is reported as a failure rather than read
   as a successful one.

2. **Sync Service to Contract**:
   - Retrieve existing service information from contract
   - Only update `ImageName` and `ImageDigest` fields in `additionalInfo`
   - All other fields (ServiceType, Url, Model, Price, TeeSignerAddress, etc.) remain unchanged
   - Call contract's `addOrUpdateService` method to update service information.
   - By updating the image information, the corresponding provider in contract will be set as not acknowledged. Only after contract owner acknowledges the service, it becomes active again. It protects against dishonest providers pushing malicious images.

---

## 6. Authentication Design

### 6.1 Authentication Mechanism Overview

Controller uses Ethereum signature-based authentication, consistent with the existing `0g-serving-broker` authentication method. Administrators must sign a session token with their private key, and the server verifies the signature to confirm the requester's identity.

### 6.2 Authentication Flow

```text
┌─────────────────────────────────────────────────────────────┐
│                     Authentication Flow                     │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
                   ┌──────────────────┐
                   │  Client generates│
                   │  Session Token   │
                   └──────────────────┘
                              │
                              ▼
                   ┌──────────────────┐
                   │  Sign with       │
                   │  private key     │
                   │  keccak256(token)│
                   └──────────────────┘
                              │
                              ▼
                   ┌──────────────────┐
                   │  Send request    │
                   │  Authorization:  │
                   │  Bearer app-sk-  │
                   │  <base64>        │
                   └──────────────────┘
                              │
                              ▼
                   ┌──────────────────┐
                   │  Controller      │
                   │  validates:      │
                   │  1. Parse token  │
                   │  2. Recover addr │
                   │  3. Check auth   │
                   │     list         │
                   └──────────────────┘
                              │
                  ┌───────────┴───────────┐
                  │                       │
                  ▼                       ▼
           ┌──────────┐            ┌──────────┐
           │  Auth    │            │  Access  │
           │  Passed  │            │  Denied  │
           └──────────┘            └──────────┘
```

### 6.3 Request Header Format

```text
Authorization: Bearer app-sk-<base64(rawMessage|signature)>
```

Where:

- `rawMessage`: JSON-formatted SessionToken string containing address, timestamp, expiresAt, nonce
- `signature`: Ethereum signature of `keccak256(rawMessage)` (EIP-191 personal sign)
- `|`: Delimiter

---

## 7. Security Considerations

### 7.1 Access Control (Dual Whitelist)

1. **IP Whitelist** (First Layer) - Intercepts before authentication, reduces server load
   - Single IP: `192.168.1.100`
   - CIDR: `192.168.1.0/24`
   - Empty list allows all IPs

2. **Wallet Address Whitelist** (Second Layer) - Identity verification based on Ethereum signatures
   - Ethereum address format: `0x1234...`
   - Session Token carries an expiry, and `ExpiresAt: 0` means it never expires
   - Nonce is carried in the token but never checked — see §4.4

### 7.2 Operational Security

- Validate YAML format before config updates
- Keep at least one admin wallet address to prevent lockout — no code enforces this
- Use EIP-191 standard personal sign for signature verification

---

## 8. Startup Methods

### 8.1 Start via Unified Entry Point

```bash
./0g-serving-broker 0g-controller
```

This is also what §8.2's compose runs. What decides whether container management
works is the deployment shape, not the entry point: run directly on a host, the
controller serves reads and refuses every container write. See §3.1.

### 8.2 Docker Compose Deployment

**Important**: broker, event, and controller all use the same fixed image. The Controller's image update API pulls the requested digest and rebuilds the containers on it.

```yaml
services:
  # Controller service
  0g-controller:
    image: ghcr.io/0gfoundation/0g-serving-broker:latest
    ports:
      - "3090:3090"
    environment:
      - PORT=3090
      - CONFIG_FILE=/etc/config.yaml
      - ADMIN_ADDRESS=0x1234...
    volumes:
      - ./config.yaml:/etc/config.yaml
      - /var/run/docker.sock:/var/run/docker.sock  # Important: requires Docker socket
    command: 0g-controller
```

**Notes**:

- Controller uses the same configuration file as broker/event
- Controller requires Docker socket mount to manage other containers. **The broker
  and event services must not have one** — see §3.1a; a broker holding a socket can
  change any container in the CVM, which is the premise the deployment's
  `compose_hash` has to pin for an upgrade to be provable at all
- During image update, Controller pulls `imageRepo@<digest>`, rebuilds containers in dependency order, then syncs service info to contract
