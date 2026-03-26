# Finitude Serving: Complete Technical Documentation

**Version:** 3.0  
**Last Updated:** March 2026  
**Status:** Production Ready

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Problem Statement](#2-problem-statement)
3. [System Architecture](#3-system-architecture)
4. [Detailed Design](#4-detailed-design)
5. [Multi-Tenant Isolation](#5-multi-tenant-isolation)
6. [LoRA Lifecycle Management](#6-lora-lifecycle-management)
7. [Implementation Guide](#7-implementation-guide)
8. [Deployment & Operations](#8-deployment--operations)
9. [API Reference](#9-api-reference)
10. [Troubleshooting](#10-troubleshooting)
11. [Security Considerations](#11-security-considerations)
12. [Appendix](#12-appendix)

---

## 1. Executive Summary

**Finitude Serving** is a Serverless LoRA inference service system for the 0G Compute Network, enabling users to deploy fine-tuned LoRA adapters to GPUs for real-time inference without managing their own infrastructure.

### 1.1 Core Value Propositions

| Feature | Benefit |
|---------|---------|
| **One-click Deployment** | Automatically sync fine-tuned models to inference services |
| **Multi-tenant Isolation** | Strict user access control ensuring model security |
| **Auto Scaling** | Idle models automatically offloaded, restored on-demand |
| **Cost Optimization** | Share base models + dynamically load LoRAs, reducing GPU memory usage by 80%+ |
| **Unified Billing** | Reuse existing inference billing pipeline, per-token pricing |

### 1.2 Key Metrics

- **Cold start time:** ~30 seconds (offloaded → active)
- **GPU memory efficiency:** One base model + up to 8 LoRA adapters per GPU
- **Throughput:** 20+ tokens/second per request
- **Cost reduction:** 90% vs centralized AI services

---

## 2. Problem Statement

### 2.1 The Challenge

After a fine-tuning task completes, the LoRA adapter is encrypted and uploaded to 0G Storage. **Currently, there is no way for end users to use these fine-tuned models for inference.**

**User Pain Points:**
1. Must download, decrypt, and self-host the adapter
2. Complex infrastructure setup (vLLM, GPU management)
3. No integration with 0G Compute Network's billing/settlement
4. Poor adoption due to operational overhead

### 2.2 Goals

1. **Shared base model + multiple LoRA adapters:** A single GPU hosts one base model with many user-specific LoRA adapters attached dynamically.

2. **Reuse inference module's contract, auth, and billing:** Fine-tuned model serving MUST use the existing `InferenceServing` contract, session token authentication, per-token billing, and TEE-based settlement — not a parallel system.

3. **Hardware isolation:** Inference GPU(s) must be separate from training GPU(s) to avoid resource contention.

4. **Multi-tier LoRA caching:** Not all adapters fit in GPU memory simultaneously. Inactive adapters should be offloaded (GPU → CPU → Disk → 0G Storage) and restored on demand.

5. **Access control:** Only the user who created a fine-tuning task (or users they authorize) can query their adapter.

---

## 3. System Architecture

### 3.1 High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Finitude Serving Architecture                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                               │
│  ┌─────────────────────┐         ┌─────────────────────┐                      │
│  │   User (CLI/SDK)    │────────▶│  Fine-Tuning Node   │                      │
│  │                     │         │  (TEE, GPU 0)       │                      │
│  └─────────────────────┘         │                     │                      │
│                                  │  • Train LoRA       │                      │
│                                  │  • Encrypt + Upload   │                      │
│                                  │  • Add Deliverable   │                      │
│                                  └──────────┬──────────┘                      │
│                                             │                                 │
│                                             ▼                                 │
│                                  ┌─────────────────────┐                      │
│                                  │   0G Storage        │                      │
│                                  │   (Decentralized)   │                      │
│                                  └──────────┬──────────┘                      │
│                                             │                                 │
│                                             ▼                                 │
│                                  ┌─────────────────────┐                      │
│                                  │   Inference Node    │◀──── User Inference   │
│                                  │   (TEE, GPU 1)     │       Requests        │
│                                  │                     │                      │
│                                  │  • LoRA Manager     │                      │
│                                  │  • Event Watcher    │                      │
│                                  │  • Proxy + Billing  │                      │
│                                  │  • vLLM/Serverless  │                      │
│                                  └─────────────────────┘                      │
│                                                                               │
│  Communication: On-chain events (FineTuningServing → InferenceServing)         │
│  Contracts: FineTuningServing (task mgmt) + InferenceServing (billing)        │
│                                                                               │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Two-Node Architecture

**Fine-Tuning Node (Training GPU):**
- Runs fine-tuning tasks in TEE
- Encrypts LoRA adapters with user + provider keys
- Uploads to 0G Storage
- Emits `DeliverableAcknowledged` events on-chain

**Inference Node (Inference GPU):**
- Runs LoRA Manager and Proxy
- Watches chain events for new adapters
- Downloads/decrypts adapters from 0G Storage
- Manages vLLM/ServerlessLLM lifecycle
- Handles user requests with auth + billing

**Key Design:** Two independent TEE nodes with separate databases communicate via **on-chain events**, not shared storage.

### 3.3 Contract Architecture

| Contract | Purpose | Key Methods |
|----------|---------|-------------|
| **FineTuningServing** | Task lifecycle, training settlement | `addDeliverable()`, `acknowledgeDeliverable()`, `settleFees()` |
| **InferenceServing** | Service registration, inference billing | `addOrUpdateService()`, `settleFeesWithTEE()` |
| **Ledger** | Account management, token tracking | `getUserAccount()`, `transferFund()` |

**Constraint:** One provider wallet = one service (contract uses `keccak256(provider_address)` as service key).

**Solution:** Register the **base model** as the single on-chain service. All LoRA adapters share the same `inputPrice` and `outputPrice`. The broker handles LoRA routing internally.

---

## 4. Detailed Design

### 4.1 Key Design Decisions

#### Decision 1: Inference Module Manages Serving

The inference module already owns the vLLM process and the proxy. It should also manage LoRA adapters since adapters are part of the serving pipeline.

**Responsibility Split:**
- **Fine-tuning module:** Produces LoRA adapters and notifies inference when ready
- **Inference module:** Manages adapter lifecycle, serving, and billing

#### Decision 2: One On-Chain Service, Internal LoRA Routing

**On-chain Registration:**
```yaml
model: "Qwen2.5-0.5B-Instruct"
inputPrice: X
outputPrice: Y
url: "http://broker:3080"
```

**User Request:**
```json
{ "model": "ft-Qwen2-5-0-5B-Instruct-abc123", ... }
```

**Internal Mapping:**
```
Broker maps internally:
ft-Qwen2-5-0-5B-Instruct-abc123
↓
Forward to ServerlessLLM:
{ "model": "Qwen2.5-0.5B-Instruct",
  "lora_adapter_name": "ft-Qwen2-5-0-5B-Instruct-abc123", ... }
```

#### Decision 3: Unified Pricing

All LoRA adapters under the same base model share identical per-token pricing.

**Rationale:** During inference, compute cost is the same (same base model, same GPU, same token generation). Training cost differences are handled separately via `FineTuningServing` contract.

#### Decision 4: Access Control via Broker

For requests with model starting with `ft-`, the broker verifies:
1. Requester's address matches fine-tuning task owner
2. Lightweight DB lookup before forwarding

### 4.2 Component Responsibilities

| Component | Location | Responsibility |
|-----------|----------|----------------|
| **Fine-Tuning Worker** | Fine-tuning node (TEE) | Train LoRA, encrypt + upload to 0G Storage, `addDeliverable()` on-chain |
| **LoRA Manager** | Inference node (new) | Watch chain events, download/decrypt adapters, manage ServerlessLLM, offload idle |
| **Proxy** | Inference node (modified) | Map `ft-*` model names, owner check, forward to ServerlessLLM |
| **ServerlessLLM** | Inference node (new) | Base model + LoRA inference, GPU multiplexing, scale-to-zero |
| **Event Watcher** | Inference node (new) | Poll `FineTuningServing` for `DeliverableAcknowledged` events |

### 4.3 Required Changes Summary

**New Components:**
- `LoRA Manager` - Lifecycle management
- `Event Watcher` - Chain event monitoring
- `Storage Downloader` - 0G Storage integration
- `SLLM Client` - ServerlessLLM HTTP API client

**Modified Components:**
- `Proxy` - Model name mapping, owner check
- `Config` - LoRA configuration section
- `DB Schema` - `lora_adapters` table

---

## 5. Multi-Tenant Isolation

### 5.1 Isolation Levels

| Layer | Mechanism | Implementation |
|-------|-----------|----------------|
| **Network** | TLS + TEE | All inter-node communication via HTTPS |
| **Authentication** | Session Token | `app-sk-<base64(token|sig)>` with expiry, nonce, revocation check |
| **Authorization** | Ownership Check | `userAddress` must match task creator |
| **Model** | User Binding | Adapter metadata records owner address |
| **Storage** | Encryption Keys | Each LoRA uses user's public key + provider key to encrypt AES key |
| **Inference** | Namespace Isolation | `ft-{model}-{taskID}` format, globally unique |

### 5.2 Ownership Verification

```go
// CheckLoRAOwnership verifies if user has access to the model
func (c *Ctrl) CheckLoRAOwnership(modelName, userAddress string) error {
    if !lora.IsLoRAModel(modelName) {
        return nil  // Not a LoRA model, skip
    }

    adapter := c.loraManager.GetAdapter(modelName)
    if adapter == nil {
        return fmt.Errorf("fine-tuned model not found: %s", modelName)
    }

    // Strict address comparison
    if !strings.EqualFold(adapter.UserAddress, userAddress) {
        return fmt.Errorf("access denied: you are not the owner of model %s", modelName)
    }

    // Check adapter state and trigger restore if needed
    switch adapter.State {
    case model.AdapterStateActive:
        c.loraManager.RecordAccess(modelName)
        return nil
    case model.AdapterStateOffloaded, model.AdapterStateArchived:
        go c.loraManager.RestoreAdapter(context.Background(), modelName)
        return fmt.Errorf("model %s is restoring, please retry in 30 seconds", modelName)
    // ... other states
    }
}
```

---

## 6. LoRA Lifecycle Management

### 6.1 State Machine

```
┌─────────┐    Training      ┌─────────┐   Acknowledge    ┌─────────┐
│  Init   │ ───────────────▶│ Delivered│ ──────────────▶│  Ready  │
│(on-chain)│                 │(on-chain)│                │(local)  │
└─────────┘                 └─────────┘                └────┬────┘
                                                          │
                              ┌───────────────────────────┘
                              │ Deploy
                              ▼
                         ┌─────────┐   Inference   ┌─────────┐
                         │ Active  │◀──────────────│  Idle   │
                         │ (GPU)   │ ────────────▶│ (track) │
                         └────┬────┘              └────┬────┘
                              │                         │
                              │          Idle > threshold
                              │                         │
                              │                         ▼
                              │ Offload            ┌─────────┐
                              └──────────────────▶│Offloaded│
                                                  │ (disk)  │
                                                  └────┬────┘
                                                       │
                              ┌────────────────────────┘
                              │ Restore Request
                              ▼
                         ┌─────────┐
                         │ Loading │
                         │(async)  │
                         └─────────┘
```

**States:**
- `Init` - Task created, training in progress
- `Delivered` - Training complete, on-chain deliverable added
- `Ready` - Downloaded locally, not deployed to GPU
- `Active` - Loaded in GPU, ready for inference
- `Offloaded` - Unloaded from GPU, files remain on disk
- `Archived` - Cold storage (optional 0G Storage backup)
- `Loading` - Async restore in progress
- `Failed` - Deployment/download failed

### 6.2 Auto-Offload Mechanism

**Trigger:**
- Config `offloadAfterMinutes` (default: 60)
- Last access time exceeds threshold

**Process:**
```go
func (m *Manager) offloadLoop(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    for {
        <-ticker.C
        
        // 1. Query idle adapters
        idle, _ := m.db.ListIdleAdapters(time.Duration(m.config.OffloadAfterMinutes) * time.Minute)
        
        for _, a := range idle {
            // 2. Unload from GPU
            if err := m.sllmClient.DeleteAdapter(ctx, a.AdapterName); err != nil {
                m.logger.Errorf("Failed to delete adapter %s: %v", a.AdapterName, err)
                continue
            }
            
            // 3. Update state
            m.setAdapterState(a.AdapterName, model.AdapterStateOffloaded)
            
            // 4. Optional: Cold storage upload
            if m.config.EnableColdStorage {
                go m.archiveToColdStorage(a)
            }
        }
    }
}
```

### 6.3 Auto-Restore Mechanism

When user requests an offloaded/archived adapter:

```go
func (c *Ctrl) CheckLoRAOwnership(modelName, userAddress string) error {
    // ... ownership check ...
    
    switch adapter.State {
    case model.AdapterStateActive:
        c.loraManager.RecordAccess(modelName)
        return nil
    case model.AdapterStateOffloaded, model.AdapterStateArchived:
        // Trigger async restore
        go c.loraManager.RestoreAdapter(context.Background(), modelName)
        return fmt.Errorf("model %s is restoring, please retry in 30 seconds", modelName)
    }
}

func (m *Manager) RestoreAdapter(ctx context.Context, adapterName string) error {
    m.setAdapterState(adapterName, model.AdapterStateLoading)
    
    // Download from disk or 0G Storage
    go m.downloadAndDeploy(ctx, adapterName)
    return nil
}
```

---

## 7. Implementation Guide

### 7.1 File Structure

```
api/inference/
├── internal/
│   ├── lora/
│   │   ├── manager.go           # LoRA lifecycle management
│   │   ├── sllm_client.go     # ServerlessLLM HTTP client
│   │   ├── event_watcher.go     # On-chain event monitoring
│   │   ├── storage_downloader.go # 0G Storage download + decrypt
│   │   └── lora_test.go         # Unit tests
│   ├── ctrl/
│   │   ├── lora.go              # Owner check, request rewriting
│   │   └── lora_test.go
│   ├── db/
│   │   ├── lora.go              # GORM CRUD operations
│   └── handler/
│       └── handler.go           # HTTP handlers
├── model/
│   └── lora.go                  # AdapterState enum
├── config/
│   └── config.go                # LoRAConfig struct
└── cmd/server/
    └── main.go                    # Initialization
```

### 7.2 Key Code Patterns

**Snapshot Copy for Thread Safety:**
```go
func (m *Manager) GetAdapter(adapterName string) *AdapterInfo {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    a, ok := m.adapters[adapterName]
    if !ok {
        return nil
    }
    
    // Return copy to prevent data race
    cp := *a
    return &cp
}
```

**Request Rewriting:**
```go
func (c *Ctrl) RewriteLoRARequest(body []byte) ([]byte, string, error) {
    var bodyMap map[string]interface{}
    if err := json.Unmarshal(body, &bodyMap); err != nil {
        return body, "", nil
    }
    
    modelName, _ := bodyMap["model"].(string)
    if !lora.IsLoRAModel(modelName) {
        return body, modelName, nil
    }
    
    // Extract base model and adapter name
    baseModel := extractBaseModel(modelName)
    adapterName := modelName
    
    // Rewrite request for ServerlessLLM
    bodyMap["model"] = baseModel
    bodyMap["lora_adapter_name"] = adapterName
    
    newBody, _ := json.Marshal(bodyMap)
    return newBody, modelName, nil
}
```

---

## 8. Deployment & Operations

### 8.1 Prerequisites

| Requirement | Version/Spec | Notes |
|-------------|--------------|-------|
| Phala TEE CVM | Latest | Two separate CVMs (FT + Inference) |
| GPU | NVIDIA H100/A100 | Training: H100, Inference: H100 |
| MySQL | 8.0+ | Separate databases for FT/Inference |
| 0G Storage Client | Latest | Testnet requires turbo indexer |
| Docker | 20.10+ | With NVIDIA runtime |
| Docker Compose | 2.0+ | For multi-service orchestration |

### 8.2 Fine-Tuning Broker Configuration

```yaml
# config-ft.yaml
contractAddress: "0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569"  # FineTuningServing
skipTEESignerCheck: false

database:
  fineTune: "root:password@tcp(mysql-ft:3306)/fineTune?parseTime=true"

networks:
  ethereum0g:
    url: "https://evmrpc-testnet.0g.ai"
    chainID: 16602
    privateKeys:
      - "your_private_key_here"

service:
  servingUrl: "https://<your-cvm-hash>-3080.dstack-pha-in2.phala.network"
  dataDir: /dstack/persistent/e2e/ft-data
  pricePerToken: 800000000000
  skipStorageUpload: false
  inferenceServiceUrl: "https://<inference-cvm>-3081.dstack-pha-in2.phala.network"
  inferenceServiceSecret: "YOUR_SHARED_SECRET_MIN_32_CHARS_LONG"

storageClient:
  indexerTurbo: "https://indexer-storage-testnet-turbo.0g.ai"
  logLevel: "info"

logger:
  format: "text"
  level: "debug"
```

### 8.3 Inference Broker Configuration

```yaml
# config-inference.yaml
contractAddress: "0x41bD7Ac5c19000A974D5c192bcd5FB67b56C85c5"  # InferenceServing
skipTEESignerCheck: true  # Set false for production

database:
  provider: "root:password@tcp(mysql-inf:3306)/provider?parseTime=true"

networks:
  ethereum0g:
    url: "https://evmrpc-testnet.0g.ai"
    chainID: 16602
    privateKeys:
      - "your_private_key_here"

service:
  targetUrl: "http://sllm-wrapper:8343/v1"
  type: "chatbot"
  model: "Qwen2.5-0.5B-Instruct"
  inputPrice: "10000000"
  outputPrice: "10000000"
  servingUrl: "https://<your-cvm-hash>-3081.dstack-pha-in2.phala.network"

lora:
  enable: true
  baseModel: "/models/Qwen2.5-0.5B-Instruct"
  loraModulesDir: "/lora-modules"
  sllmUrl: "http://sllm-wrapper:8343"
  offloadAfterMinutes: 60
  enableColdStorage: false
  autoDeploy: false
  internalApiSecret: "YOUR_SHARED_SECRET_MIN_32_CHARS_LONG"
  fineTuningContractAddress: "0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569"
  chainRpcUrl: "https://evmrpc-testnet.0g.ai"
  pollBlockIntervalSeconds: 5
  storageIndexerUrl: "https://indexer-storage-testnet-turbo.0g.ai"
  mockDeploy: false

event:
  providerAddr: ":8089"

logger:
  format: "text"
  level: "debug"
```

### 8.4 Docker Compose Deployment

```yaml
# docker-compose.yml for Inference Node
version: '3.8'

services:
  mysql-inf:
    image: mysql:8.0
    container_name: mysql-inf
    environment:
      MYSQL_ROOT_PASSWORD: "123456"
      MYSQL_DATABASE: provider
    ports:
      - "33061:3306"
    volumes:
      - mysql-inf-data:/var/lib/mysql
    networks:
      - e2e-net

  vllm:
    image: vllm/vllm-openai:v0.8.5.post1
    container_name: vllm
    ipc: host
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
    volumes:
      - /dstack/persistent/e2e/models:/models:ro
      - /dstack/persistent/e2e/lora-modules:/lora-modules
    environment:
      - VLLM_ALLOW_RUNTIME_LORA_UPDATING=1
      - HF_ENDPOINT=https://hf-mirror.com
    command: >
      --model /models/Qwen2.5-0.5B-Instruct
      --enable-lora
      --max-lora-rank 64
      --max-loras 8
      --gpu-memory-utilization 0.3
      --host 0.0.0.0
      --port 8000
    networks:
      - e2e-net

  sllm-wrapper:
    image: python:3.11-slim
    container_name: sllm-wrapper
    volumes:
      - ./sllm-wrapper.py:/app/sllm-wrapper.py:ro
    environment:
      - VLLM_BASE=http://vllm:8000
      - LORA_HOST_PREFIX=/lora-modules/
      - LORA_CONTAINER_PREFIX=/lora-modules/
    command: python3 /app/sllm-wrapper.py
    depends_on:
      - vllm
    networks:
      - e2e-net

  inference-broker:
    image: ghcr.io/0gfoundation/0g-serving-broker:latest
    container_name: inference-broker
    environment:
      - PORT=3080
      - CONFIG_FILE=/etc/config.yaml
      - NETWORK=phala
    volumes:
      - ./broker:/usr/bin/broker
      - ./config-inf-docker.yaml:/etc/config.yaml:ro
      - /dstack/persistent/e2e/lora-modules:/lora-modules
      - /var/run/dstack.sock:/var/run/dstack.sock
    command: 0g-inference-server
    depends_on:
      - mysql-inf
      - sllm-wrapper
    networks:
      - e2e-net

  nginx:
    image: nginx:1.27.0
    container_name: nginx-inference
    ports:
      - "3081:80"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    depends_on:
      - inference-broker
    networks:
      - e2e-net

volumes:
  mysql-inf-data:

networks:
  e2e-net:
    name: e2e-net
    driver: bridge
```

### 8.5 Critical Configuration Notes

#### Shared Secret Configuration

Fine-tuning and Inference brokers must share the same secret for internal API authentication:

```yaml
# Fine-tuning Broker
service:
  inferenceServiceSecret: "YOUR_SHARED_SECRET_MIN_32_CHARS_LONG"

# Inference Broker
lora:
  internalApiSecret: "YOUR_SHARED_SECRET_MIN_32_CHARS_LONG"  # Must match!
```

**Security Requirements:**
- Minimum 32 characters
- Use `openssl rand -hex 32` to generate
- Rotate regularly
- Store in secure environment variables

#### TEE Signer Acknowledgement

Each new CVM deployment generates a unique TEE Signer address requiring contract owner acknowledgement:

```python
from web3 import Web3

w3 = Web3(Web3.HTTPProvider("https://evmrpc-testnet.0g.ai"))

# Contract ABI (simplified)
abi = [{
    "inputs": [{"name": "provider", "type": "address"}],
    "name": "acknowledgeTEESignerByOwner",
    "type": "function"
}]

contract = w3.eth.contract(address="0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569", abi=abi)

# Owner executes this
tx = contract.functions.acknowledgeTEESignerByOwner(
    "0x87a13337F0d4B2b08cce9189DBE9555690828ed4"  # Provider address
).transact({'from': owner_address})
```

---

## 9. API Reference

### 9.1 Fine-Tuning CLI Commands

| Command | Purpose | Example |
|---------|---------|---------|
| `fine-tuning upload` | Upload dataset to 0G Storage | `0g-compute-cli ft upload --data-path ./data.jsonl` |
| `fine-tuning create-task` | Start training | `0g-compute-cli ft create-task --provider <ADDR> --model <MODEL> --dataset <HASH>` |
| `fine-tuning get-task` | Check task status | `0g-compute-cli ft get-task --provider <ADDR> --task <ID>` |
| `fine-tuning acknowledge-model` | Download model locally | `0g-compute-cli ft acknowledge-model --provider <ADDR> --task-id <ID>` |
| `fine-tuning deploy-adapter` | Deploy to GPU | `0g-compute-cli ft deploy-adapter --provider <ADDR> --model <MODEL> --task-id <ID>` |
| `fine-tuning chat` | Send inference request | `0g-compute-cli ft chat --provider <ADDR> --model <MODEL> --task-id <ID> --message "Hi"` |

### 9.2 Inference Broker Endpoints

#### Internal API (Fine-tuning → Inference)

```http
POST /internal/v1/adapter-keys
Content-Type: application/json
Authorization: Bearer <internalApiSecret>

{
  "taskId": "uuid-string",
  "storageHash": "0x...",
  "providerEncKey": "0x..."
}
```

#### User API (Inference)

```http
POST /v1/proxy/chat/completions
Content-Type: application/json
Authorization: Bearer <session-token>

{
  "model": "ft-Qwen2-5-0-5B-Instruct-abc123",
  "messages": [{"role": "user", "content": "Hello!"}],
  "max_tokens": 100
}
```

#### Adapter Management

```http
GET /v1/lora/adapters                           # List all adapters
GET /v1/lora/adapters/:name                     # Get adapter status
POST /v1/lora/adapters/deploy                   # Deploy adapter (internal)
```

### 9.3 Session Token Format

```
Authorization: Bearer app-sk-<base64(json-token|signature)>

Token JSON:
{
  "appId": "my-app",
  "tokenId": 0,           // 0-254 = persistent, 255 = ephemeral
  "generation": 1,
  "timestamp": 1774320000000,  // Unix ms
  "expiresAt": 1774323600000,  // Unix ms, 0 = never
  "nonce": "random-string",
  "address": "0x...",       // User wallet address
  "provider": "0x..."       // Provider address
}

Signature: ECDSA sign of token JSON, recoverable to user address
```

---

## 10. Troubleshooting

### 10.1 Common Errors

| Error | Cause | Solution |
|-------|-------|----------|
| `TEE signer not acknowledged` | New CVM not acknowledged | Contract owner calls `acknowledgeTEESignerByOwner()` |
| `Unknown column 'provider_encrypted_secret'` | DB not migrated | Run `AutoMigrate` or manual `ALTER TABLE` |
| `403 Forbidden` on push adapter key | Shared secret mismatch | Verify `inferenceServiceSecret` == `internalApiSecret` |
| `model is restoring, please retry` | Model offloaded, restore in progress | Wait 30 seconds and retry |
| `file not found` on 0G Storage | File not synced | Use `--download-method tee` to fallback |
| `signature verification failed` | Clock skew or wrong key | Sync system time, verify private key |
| `insufficient balance` | Low funds | Deposit more tokens and transfer to provider |

### 10.2 Debug Commands

```bash
# Check task status
0g-compute-cli fine-tuning get-task --provider <ADDR> --task <ID>

# View broker logs
docker logs inference-broker --tail 100 -f
docker logs fine-tuning-broker --tail 100 -f

# Check adapter state
curl https://<broker>/v1/lora/adapters | jq

# Database queries
docker exec mysql-inf mysql -uroot -p123456 -e "SELECT id, progress, fee FROM provider.task WHERE id = '<task_id>';"
docker exec mysql-inf mysql -uroot -p123456 -e "SELECT adapter_name, state, user_address FROM provider.lo_ra_adapter;"

# GPU status
nvidia-smi
docker exec vllm nvidia-smi
```

### 10.3 Log Analysis

**Fine-tuning Broker Key Logs:**
```
INFO  "uploading output model to 0G Storage"
INFO  "pushing adapter key to inference broker"
INFO  "addDeliverable on-chain success"
```

**Inference Broker Key Logs:**
```
INFO  "Discovered new adapter via chain event"
INFO  "downloaded and decrypted adapter"
INFO  "deployed LoRA adapter to ServerlessLLM"
INFO  "LoRA rewrite: ft-... → lora_adapter_name"
WARN  "adapter offloaded due to idle timeout"
INFO  "Restoring offloaded adapter"
```

---

## 11. Security Considerations

### 11.1 Threat Model

| Threat | Mitigation |
|--------|------------|
| Unauthorized model access | `CheckLoRAOwnership()` verifies `userAddress` |
| Replay attacks | Session tokens include nonce and expiry |
| Model theft in transit | AES-GCM encryption + TLS |
| Key compromise | ECIES dual-key encryption (user + provider keys) |
| DoS on inference | Rate limiting (15 req/s) + concurrency limits |
| Billing fraud | TEE-signed settlement, on-chain verification |

### 11.2 Security Checklist

- [ ] Shared secret > 32 characters, rotated regularly
- [ ] TEE Signer acknowledged for each CVM deployment
- [ ] Database credentials in environment variables (not config files)
- [ ] Private keys never logged or exposed in error messages
- [ ] HTTPS only for all external endpoints
- [ ] Rate limiting enabled on proxy endpoints
- [ ] Adapter encryption keys properly managed

### 11.3 Encryption Details

**LoRA Adapter Encryption:**
1. Generate random 32-byte AES key
2. Encrypt LoRA files with AES-GCM
3. Encrypt AES key with user's ECIES public key
4. Encrypt AES key with provider's ECIES public key (for serving)
5. Upload encrypted file + encrypted keys to 0G Storage

**Key Hierarchy:**
```
User Private Key ──┐
                  ├──▶ ECIES ──▶ Encrypted AES Key ──▶ Decrypt LoRA
Provider Key ─────┘                    │
                                       ▼
                              AES-GCM ──▶ LoRA Adapter
```

---

## 12. Appendix

### A. Contract Addresses

| Network | FineTuningServing | InferenceServing | Ledger |
|---------|-------------------|------------------|--------|
| 0G Testnet | `0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569` | `0x41bD7Ac5c19000A974D5c192bcd5FB67b56C85c5` | `0x815B93ab4Ba4BDF530dbF1552649a3c534F8BbF7` |
| 0G Mainnet | (TBD) | (TBD) | (TBD) |

### B. Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `ZG_DEV_MODE` | Use dev contract addresses | `true` |
| `CONFIG_FILE` | Path to YAML config | `/etc/config.yaml` |
| `NETWORK` | Network type | `phala` |
| `PORT` | HTTP server port | `3080` |

### C. Model Naming Convention

```
ft-{base-model}-{task-id-short}

Examples:
ft-Qwen2-5-0-5B-Instruct-abe55ea4-707
ft-Llama-3-1-8B-4d0e115e-76b
```

### D. References

- 0G Compute Network Docs: https://docs.0g.ai
- Fine-tuning E2E Guide: `./api/e2e-lora-serving/README.md`
- Design Document: `./fine-tuning/docs/DESIGN_SERVING_FINETUNED_MODELS.md`
- Code Review Standards: `./CLAUDE.md`

---

**Document Version:** 3.0  
**Last Updated:** March 24, 2026  
**Maintained by:** 0G Compute Team
