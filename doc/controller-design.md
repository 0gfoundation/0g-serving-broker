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
  image: "ghcr.io/0gfoundation/0g-serving-broker:latest" # deprecated, see below — still needed by the broker
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

`controller.image` is **deprecated and no longer read by the controller**, but
must stay set for now: the broker still reads it (`provider_contract.go`) to
report `additionalInfo.ImageName` on-chain and to resolve that name's digest
against the local daemon. Deleting it early empties both image fields, and the
contract reads an image change as grounds to un-acknowledge the provider. It can
be removed once the broker takes `IMAGE_REPO` / `IMAGE_DIGEST` from its
environment instead.

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
| GET    | `/v1/configs/:name`       | Get current config of specific container |
| PUT    | `/v1/configs/:name`       | Update config of specific container |
| POST   | `/v1/configs/:name/apply` | Update config and restart container |

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
already down.

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
and the controller reads them at its next start. `ADMIN_ADDRESS` / `ALLOWED_IPS`
override the first two (§3.2); nothing overrides the other two. **Set those env
vars in the compose file** — and note that no code path enforces a floor on the
number of remaining admins.

Revocation is offline. Nothing invalidates a leaked admin session token while the
controller runs, and `SessionToken` treats `ExpiresAt: 0` as never expiring.
Bounding token lifetime is tracked separately.

Read the container guard as a safety interlock, not containment. `controller.image`
and `controller.docker.host` are read by the **broker** too
(`inference/internal/contract/provider_contract.go`), and `PUT /v1/config/core`
restarts the broker in the same call — so a write to either lands there within
seconds, and `controller.image` is what the broker reports on-chain as
`ImageName`, with the digest read from whatever daemon `controller.docker.host`
names. The controller's own copy of `controller.imageRepo` waits for the
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

### 5.2 Image Update Workflow

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
- Controller requires Docker socket mount to manage other containers
- During image update, Controller pulls `imageRepo@<digest>`, rebuilds containers in dependency order, then syncs service info to contract
