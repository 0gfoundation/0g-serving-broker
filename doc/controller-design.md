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
  image: "ghcr.io/0gfoundation/0g-serving-broker:latest"
  docker:
    host: "unix:///var/run/docker.sock"
    apiVersion: "1.41"
  containers:
    broker:
      name: "0g-serving-provider-broker"
    event:
      name: "0g-serving-provider-event"
```

### 3.2 Environment Variable Support

Admin addresses can be configured via environment variables (takes precedence over config file):

```bash
# Multiple addresses separated by commas
ADMIN_ADDRESS=0xaddr1,0xaddr2,0xaddr3
```

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
| POST   | `/v1/images/update` | Pull latest image, sync service to contract, rebuild containers |
| GET    | `/v1/images/info`   | Get current image information                         |

### 4.4 Admin Whitelist API

| Method | Path | Description |
|--------|------|-------------|
| GET    | `/v1/admin/wallets` | Get current admin wallet address list |
| POST   | `/v1/admin/wallets` | Add new admin wallet address |
| DELETE | `/v1/admin/wallets/:address` | Remove admin wallet address |
| GET    | `/v1/admin/ips` | Get current IP whitelist |
| POST   | `/v1/admin/ips` | Add new IP to whitelist |
| DELETE | `/v1/admin/ips/:ip` | Remove IP from whitelist |

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
                   │  Pull latest     │
                   │  image           │
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
         ┌─────────────┐
         │ Step 2:      │
         │ Sync service │
         │ to contract  │
         │ (update      │
         │ image info)  │
         └─────────────┘
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

1. **Pull Latest Image**: Pull the image with latest tag from the registry, obtain new image digest

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
   - Session Token supports expiration time
   - Nonce prevents replay attacks

### 7.2 Operational Security

- Validate YAML format before config updates
- Keep at least one admin wallet address to prevent lockout
- Use EIP-191 standard personal sign for signature verification

---

## 8. Startup Methods

### 8.1 Start via Unified Entry Point

```bash
./0g-serving-broker 0g-controller
```

### 8.2 Docker Compose Deployment

**Important**: broker, event, and controller all use the same fixed image. The Controller's image update API can uniformly pull the latest image and rebuild containers.

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
- During image update, Controller pulls the latest image, syncs service info to contract, then rebuilds containers in dependency order
