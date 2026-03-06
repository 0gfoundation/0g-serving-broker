# Design: Serving Fine-Tuned Models to End Users

> Status: **Phase 1–3 Implemented (0G Storage download + decryption done), E2E Verified on CPU**  
> Author: Zeyu  
> Date: 2026-03-02  
> Updated: 2026-03-05

## 1. Problem Statement

After a fine-tuning task completes, the LoRA adapter is encrypted and uploaded to 0G Storage. Currently, there is no way for end users to **use** these fine-tuned models for inference. Users must download, decrypt, and self-host the adapter — a poor experience that blocks adoption.

**Goal**: Allow users to send inference requests to their fine-tuned LoRA models through the existing 0G Compute Network, with proper authentication and billing, without requiring users to run their own infrastructure.

### 1.1 Requirements

1. **Shared base model + multiple LoRA adapters**: A single GPU hosts one base model with many user-specific LoRA adapters attached dynamically.
2. **Reuse inference module's contract, auth, and billing**: Fine-tuned model serving MUST use the existing `InferenceServing` contract, session token authentication, per-token billing, and TEE-based settlement — not a parallel system.
3. **Hardware isolation**: Inference GPU(s) must be separate from training GPU(s) to avoid resource contention.
4. **Multi-tier LoRA caching**: Not all adapters fit in GPU memory simultaneously. Inactive adapters should be offloaded (GPU → CPU → Disk → 0G Storage) and restored on demand.
5. **Access control**: Only the user who created a fine-tuning task (or users they authorize) can query their adapter.

---

## 2. Current Architecture Overview

### 2.1 Fine-Tuning Module (`api/fine-tuning/`)

**Task lifecycle**:
```
User creates task → Setup (download data/model) → Training (Docker/LoRA) → Finalize (encrypt + upload to 0G Storage) → Deliver → User acknowledges → Settlement
```

**Key artifacts after training**:
- LoRA adapter directory on disk (`output_model/`)
- Encrypted LoRA uploaded to 0G Storage (`OutputRootHash`)
- `EncryptedSecret` (AES key encrypted with user's public key)

**Contract**: `FineTuningServing` — methods: `addOrUpdateService`, `addDeliverable`, `acknowledgeDeliverable`, `settleFees`.

### 2.2 Inference Module (`api/inference/`)

**Request flow**:
```
User → POST /v1/proxy/chat/completions
     → Session token validation (app-sk-<base64(token|sig)>)
     → Balance check (inputFee + unsettledFee + reservation)
     → Create DB Request record
     → Forward to provider's target URL
     → Extract usage (prompt_tokens, completion_tokens)
     → Compute fee (inputPrice × inputTokens + outputPrice × outputTokens)
     → Update DB Request with fee
     → [Later] TEE signs batch → settleFees on-chain
```

**Key mechanisms**:
- **Contract registration**: `InferenceServing.addOrUpdateService(ServiceParams{URL, model, inputPrice, outputPrice, ...})`
- **Auth**: Session token with expiry, nonce, on-chain revocation check, address recovery
- **Billing**: Per-token pricing, DB Request records, unsettled fee tracking
- **Settlement**: TEE signs EIP-712 digest of request batch → `settleFees` on-chain

### 2.3 Gap Analysis (Why PR #374 Was Insufficient)

| Capability | Inference Module | PR #374 (fine-tuning/serving) |
|------------|-----------------|-------------------------------|
| Contract registration | `addOrUpdateService()` | No-op stub |
| Auth | Session token + on-chain verification | Static message signature, replayable |
| Billing | Per-token, DB records, unsettled tracking | None |
| Settlement | TEE-signed batch → on-chain | None |
| Service discovery | On-chain, public | Not discoverable |

**Conclusion**: PR #374 built an isolated inference server without connecting to the economic model. The correct approach is to register fine-tuned models as inference services and route traffic through the inference proxy.

---

## 3. Proposed Architecture

### 3.1 Current Inference Broker Limitation

**Critical constraint**: The current inference broker only supports **one service per instance**.

```go
// config/config.go — singular Service, not Services[]
type Config struct {
    Service  Service  `yaml:"service"`   // ONE service only
    ...
}

// proxy/proxy.go — singular target
type Proxy struct {
    serviceTarget  string   // ONE target URL
    serviceType    string   // ONE service type
    ...
}
```

Each inference broker instance = one vLLM backend = one model. To serve fine-tuned LoRA adapters (which are multiple models on a shared base), the inference broker **must be extended to support multiple services**.

### 3.2 High-Level Flow

```
┌──────────────────────────────────┐   ┌──────────────────────────────────────┐
│   Fine-Tuning Node (TEE, GPU 0)  │   │   Inference Node (TEE, GPU 1)        │
│                                  │   │                                      │
│   Fine-Tuning Broker             │   │   Inference Broker                   │
│   ┌────────────────────────────┐ │   │   ┌────────────────────────────────┐ │
│   │ Task lifecycle:            │ │   │   │ Proxy (existing + LoRA ext.)   │ │
│   │ Create → Train → Finalize  │ │   │   │  • Auth (session token)        │ │
│   │ → Deliver → Settlement     │ │   │   │  • ft-* model rewrite          │ │
│   │                            │ │   │   │  • Owner check                 │ │
│   │ Upload adapter to          │ │   │   │  • Billing (per-token)         │ │
│   │ 0G Storage (encrypted)     │ │   │   │  • TEE settlement              │ │
│   └────────────────────────────┘ │   │   └──────────────┬─────────────────┘ │
│                                  │   │                  │ forward            │
│   Contract: FineTuningServing    │   │   ┌──────────────▼─────────────────┐ │
│   • addDeliverable()             │   │   │ ServerlessLLM Cluster          │ │
│   • acknowledgeDeliverable()  ───┼───┼─► │  Base model + LoRA adapters    │ │
│   • settleFees()                 │   │   │  GPU multiplexing, scale-zero  │ │
│                                  │   │   └──────────────▲─────────────────┘ │
└──────────────────────────────────┘   │                  │                    │
                                       │   ┌──────────────┴─────────────────┐ │
   On-chain events                     │   │ LoRA Manager (new)             │ │
   (acknowledgeDeliverable) ──────────►│   │  • Watch chain for new tasks   │ │
                                       │   │  • Download from 0G Storage    │ │
                                       │   │  • Decrypt → sllm deploy       │ │
                                       │   │  • Offload idle adapters       │ │
                                       │   └────────────────────────────────┘ │
                                       │                                      │
                                       │   Contract: InferenceServing         │
                                       │   • addOrUpdateService() (base model)│
                                       │   • settleFeesWithTEE()              │
                                       └──────────────────────────────────────┘
```

**Key**: The two modules run on **separate TEE nodes** with separate databases. They communicate via **on-chain events** (not shared DB). The fine-tuning contract emits events when tasks are delivered/acknowledged; the inference module's LoRA Manager watches these events to discover new adapters.

### 3.3 Contract Constraint: One Provider = One Service

The `InferenceServing` contract uses `keccak256(provider_address)` as the service key:

```solidity
function _key(address provider) internal pure returns (bytes32) {
    return keccak256(abi.encode(provider));
}
```

This means **one wallet address can only register one service**. We cannot register each LoRA adapter as a separate on-chain service.

**Solution**: Register the **base model** as the single on-chain service. All LoRA adapters share the same `inputPrice` and `outputPrice`. The broker handles LoRA routing internally — no contract changes required.

**Unified pricing rationale**: During inference, the compute cost for different LoRA adapters is essentially the same (same base model, same GPU, same token generation). The difference is in training cost (different dataset sizes, LoRA ranks), which is already billed separately via the `FineTuningServing` contract.

### 3.4 Key Design Decisions

**Decision 1: Inference module manages vLLM/ServerlessLLM and LoRA; fine-tuning module only trains.**

The inference module already owns the vLLM process and the proxy. It should also manage LoRA adapters, since adapters are part of the serving pipeline. The fine-tuning module produces LoRA adapters and notifies inference when a new one is ready.

**Decision 2: One on-chain service, LoRA routing inside broker.**

The broker registers one service with the base model name (e.g., `Qwen2.5-7B`). All LoRA adapters share the same `inputPrice` and `outputPrice`. The broker internally maps the `model` field in user requests to the correct LoRA adapter and forwards to ServerlessLLM with the right `lora_adapter_name`.

```
On-chain: ONE service registered
  model: "Qwen2.5-7B"
  inputPrice: X, outputPrice: Y
  url: "http://broker:3080"

User request: { "model": "ft-Qwen2-5-7B-abc123", ... }
                       │
                 Broker maps internally
                       │
                       ▼
Forward to ServerlessLLM: { "model": "Qwen2.5-7B",
                             "lora_adapter_name": "ft-Qwen2-5-7B-abc123", ... }
```

**Decision 3: Access control via owner check in broker.**

For requests with `model` starting with `ft-`, the broker checks that the requester's address matches the fine-tuning task owner. This is a lightweight DB lookup before forwarding.

**Decision 4: Billing uses the registered service's per-token pricing.**

All LoRA adapters bill at the same rate as the base model. The existing billing pipeline (token counting → fee computation → TEE settlement) works unchanged.

### 3.5 Component Responsibilities

| Component | Where | Responsibility |
|-----------|-------|----------------|
| **Fine-Tuning Worker** | fine-tuning node (TEE) | Train LoRA adapter, encrypt + upload to 0G Storage, `addDeliverable()` on-chain |
| **LoRA Manager** | inference node (new component) | Watch chain events, download/decrypt adapters from 0G Storage, manage adapters in ServerlessLLM (`sllm deploy`/`sllm delete`), offload idle adapters |
| **Proxy** | inference node (minimal changes) | Map `ft-*` model names to `lora_adapter_name`, owner check, forward to ServerlessLLM |
| **ServerlessLLM** | inference node (new dependency) | Base model + LoRA inference, GPU multiplexing, scale-to-zero |
| **FineTuningServing contract** | on-chain | Task lifecycle, deliverable tracking, training fee settlement |
| **InferenceServing contract** | on-chain | Service registration (base model), inference fee settlement |

### 3.6 Required Inference Module Changes

| Area | Current | Required Change |
|------|---------|----------------|
| Proxy | Forwards to single vLLM | Add model name mapping: `ft-*` → rewrite request body with `lora_adapter_name` |
| Config | Single `targetUrl` to vLLM | Point to ServerlessLLM endpoint instead; add `lora` config section |
| Owner check | None | New middleware: for `ft-*` models, verify requester == task owner (from LoRA Manager's in-memory map) |
| LoRA Manager | None | **New component**: watch FineTuningServing contract events, download/decrypt adapters from 0G Storage, manage ServerlessLLM lifecycle |
| Local DB | Request records only | Add `lora_adapters` table for crash recovery (adapter metadata) |
| Contract | Registers one service | **No change** — still one service, base model name |
| Billing | Per-token with one price | **No change** — all LoRA adapters use same price |
| Settlement | TEE batch per provider | **No change** |
| vLLM management | Broker manages vLLM directly | Replace with ServerlessLLM (managed separately via Docker Compose) |

### 3.7 Inter-Module Communication

**Constraint**: Fine-tuning and inference run on **separate TEE nodes** with separate databases. They cannot share a MySQL instance. Therefore, inter-module communication uses **on-chain events**.

The flow:

1. Fine-tuning task completes → fine-tuning broker calls `FineTuningServing.addDeliverable()` on-chain
2. User acknowledges result → calls `acknowledgeDeliverable()` on-chain
3. Inference module's LoRA Manager watches the `FineTuningServing` contract for `DeliverableAcknowledged` events
4. When detected: extract task info (adapter 0G Storage root hash, user address) → download from 0G Storage → decrypt → deploy to ServerlessLLM

**Why trigger on acknowledge (not deliver)?** The user must explicitly confirm they want the fine-tuned result before the provider starts serving it. If the user never acknowledges (e.g., unhappy with quality), the adapter is NOT deployed for inference — no wasted GPU resources.

**Adapter file transfer**: The inference node downloads the encrypted adapter from **0G Storage** (not from the fine-tuning node's disk). This avoids any direct file sharing between TEE nodes. The provider holds the decryption key (or re-derives it) to decrypt the adapter locally.

---

## 4. Detailed Design

### 4.1 Config Changes (Minimal)

The broker config only needs a small addition — a `lora` section for the LoRA Manager:

```yaml
# Existing service config — unchanged
service:
  targetUrl: "http://sllm:8343"     # point to ServerlessLLM instead of vLLM
  type: "chatbot"
  model: "Qwen2.5-7B"              # base model name (registered on-chain)
  inputPrice: "10000000"
  outputPrice: "10000000"

# New: LoRA serving config
lora:
  enable: true
  baseModel: "Qwen2.5-7B"
  loraModulesDir: "/data/lora-modules"
  sllmUrl: "http://sllm:8343"          # ServerlessLLM HTTP endpoint (configurable)
  offloadAfterMinutes: 60
  enableColdStorage: true
  fineTuningContractAddress: "0x1234..."   # FineTuningServing contract to watch events
  chainRpcUrl: "https://evmrpc-testnet.0g.ai"
  pollBlockIntervalSeconds: 5
  mockDeploy: false                     # Set true for E2E testing without 0G Storage
```

**Key changes from previous version**:
- `fineTuningDbProvider` removed — no shared DB between TEE nodes
- `fineTuningContractAddress` + `chainRpcUrl` added — LoRA Manager watches on-chain events instead
- `service.targetUrl` now points to **ServerlessLLM** (port 8343) instead of vLLM (port 8000)
- `sllmUrl` added — configurable SLLM endpoint (previously hardcoded)
- `mockDeploy` added — when `true`, creates placeholder adapter files instead of downloading from 0G Storage (for CPU-only E2E testing)

Everything else stays the same — same model name on-chain, same prices, same contract registration.

### 4.2 LoRA Manager (Implemented in Inference Module)

Lives in `api/inference/internal/lora/`. Manages the LoRA adapter lifecycle via ServerlessLLM HTTP API + on-chain event watching.

**Implementation status**: Core manager, SLLM client, event watcher, and DB persistence are **implemented and E2E tested**. 0G Storage download + decryption is a **TODO** (see Section 9).

**Responsibilities**:
1. Watch the `FineTuningServing` contract for `DeliverableAcknowledged` events (new completed + acknowledged tasks)
2. Download encrypted adapter from 0G Storage → decrypt with provider key → place in `loraModulesDir` (**TODO**)
3. Call ServerlessLLM HTTP API to register adapter (`POST /v1/models/deploy`)
4. Maintain an in-memory map of `model name → adapter info (owner address, state, last access, 0G root hash)`
5. Persist adapter metadata to local DB (inference node's own MySQL) for crash recovery
6. Track access times, offload idle adapters (`DELETE /v1/models/<name>`)
7. Restore from 0G Storage on demand (download → decrypt → deploy)

**Startup sequence**:
```
1. ServerlessLLM cluster is already running (Docker Compose)
2. Load adapter metadata from local DB (inference node's own MySQL)
3. For each known adapter: check if files exist on disk → sllm deploy
4. For missing files: re-download from 0G Storage → decrypt → sllm deploy
5. Start event watcher (subscribe to FineTuningServing contract events)
6. Start offload loop (check idle adapters every N minutes)
```

**Event watching detail**:
```go
// LoRA Manager subscribes to FineTuningServing contract events
func (m *LoRAManager) watchNewAdapters(ctx context.Context) {
    // Watch for DeliverableAcknowledged(taskID, userAddress, rootHash)
    // On event:
    //   1. Download encrypted adapter from 0G Storage by rootHash
    //   2. Decrypt using provider's key
    //   3. Place in loraModulesDir/ft-<base>-<taskID>/
    //   4. sllm deploy --lora-adapters "ft-<base>-<taskID>=<path>"
    //   5. Add to in-memory map + persist to local DB
}
```

### 4.3 Proxy: Model Name Mapping

The only proxy change: intercept requests with `ft-*` model names, rewrite the body for ServerlessLLM.

```go
func (p *Proxy) rewriteLoRARequest(reqBody []byte) ([]byte, string, error) {
    var body map[string]interface{}
    json.Unmarshal(reqBody, &body)
    
    modelName, _ := body["model"].(string)
    if !strings.HasPrefix(modelName, "ft-") {
        return reqBody, modelName, nil  // base model: no rewrite
    }
    
    // Rewrite for ServerlessLLM:
    //   "model": "ft-Qwen2-5-7B-abc123"
    // becomes:
    //   "model": "Qwen2.5-7B",
    //   "lora_adapter_name": "ft-Qwen2-5-7B-abc123"
    body["model"] = p.ctrl.Config.Service.ModelType  // base model name
    body["lora_adapter_name"] = modelName
    
    modified, _ := json.Marshal(body)
    return modified, modelName, nil
}
```

### 4.4 Complete User Journey (End to End)

The full lifecycle from account creation to inference settlement:

```
Phase A: Setup (user ↔ FineTuningServing contract)
──────────────────────────────────────────────────
A1. User creates account with fine-tuning provider
    → FineTuningServing.addAccount(providerAddress)
    → Deposits funds (for training fee)

A2. User creates fine-tuning task
    → POST /fine-tuning/tasks (to fine-tuning broker)
    → Uploads dataset to 0G Storage
    → Task enters queue: Pending → Setup → Training → Finalize → Delivered

A3. Fine-tuning broker delivers result
    → Encrypted LoRA adapter uploaded to 0G Storage
    → FineTuningServing.addDeliverable(taskID, rootHash, encryptedSecret)

A4. User acknowledges the result
    → FineTuningServing.acknowledgeDeliverable(taskID)
    → Training fee settled via FineTuningServing.settleFees()

    Edge cases:
    • User CANCELS task before completion → task stopped, no adapter, no inference
    • User NEVER acknowledges → adapter stays in 0G Storage but is NOT deployed
      for inference (LoRA Manager only triggers on acknowledge event)
    • User acknowledges but adapter is corrupted → download fails, not deployed


Phase B: Inference Setup (user ↔ InferenceServing contract)
───────────────────────────────────────────────────────────
B1. [Automatic] LoRA Manager on inference node detects DeliverableAcknowledged event
    → Downloads encrypted adapter from 0G Storage
    → Decrypts with provider's key
    → Calls `sllm deploy` to load adapter into ServerlessLLM
    → Adapter is now servable as "ft-<base>-<taskID>"

B2. User creates account with inference provider (SAME provider, but InferenceServing contract)
    → InferenceServing: user calls addAccount(providerAddress)
    → User deposits funds (for inference fees)
    (Note: this is the SAME provider address, but a DIFFERENT contract.
     The provider runs both fine-tuning and inference nodes.)

B3. User creates session token (done on USER's side, via SDK/CLI)
    → User signs a message with their private key:
      { providerAddress, nonce, expiry }
    → Encodes as: app-sk-<base64(message|signature)>
    → This token is sent as Authorization header in all inference requests
    (Where: user's local machine, using 0G inference SDK. NOT on the broker.)


Phase C: Inference Request (user ↔ inference broker ↔ ServerlessLLM)
────────────────────────────────────────────────────────────────────
C1. User sends inference request:
    POST /v1/proxy/chat/completions
    Authorization: Bearer app-sk-<token>
    Body: { "model": "ft-Qwen2-5-7B-abc123", "messages": [...] }

C2. Inference broker proxy:
    a. Validate session token → recover userAddress from signature
    b. Detect "ft-*" model → owner check:
       LoRA Manager lookup: is userAddress the task owner? If not → 403
    c. Rewrite request body for ServerlessLLM:
       { "model": "Qwen2.5-7B", "lora_adapter_name": "ft-Qwen2-5-7B-abc123", ... }
    d. Check balance ≥ estimatedFee + unsettledFees
       (uses InferenceServing contract: account(user, provider).balance)
    e. Create Request record in broker's local DB
    f. Forward to http://sllm:8343/v1/chat/completions
    g. ServerlessLLM routes to correct LoRA adapter, returns response
    h. Extract usage: prompt_tokens, completion_tokens
    i. Compute fee = inputPrice × promptTokens + outputPrice × completionTokens
    j. Update Request record with fee

C3. Settlement (periodic, by inference broker's event service):
    a. Batch unprocessed Request records
    b. TEE signs EIP-712 digest of the batch
    c. Call InferenceServing.settleFeesWithTEE(settlements[])
    d. Contract deducts from user's account balance → transfers to provider


Phase D: Adapter Lifecycle
──────────────────────────
D1. Adapter goes idle (no requests for offloadAfterMinutes)
    → LoRA Manager calls `sllm delete` to unload from ServerlessLLM
    → Adapter files may remain on disk or be cleaned up
    → Inference requests for this adapter now return 503

D2. User sends request for offloaded adapter
    → LoRA Manager detects adapter not loaded
    → Downloads from 0G Storage → decrypts → `sllm deploy`
    → Returns 503 with Retry-After (or blocks if wait_for_model=true)

D3. Provider stops serving (removes inference service)
    → InferenceServing.removeService() → stake returned
    → All adapters become inaccessible
```

### 4.5 Access Control

**Where**: Access control for fine-tuned models is added to the **existing inference broker** (not a new broker). Specifically, it is a new middleware step in the existing proxy handler chain (`api/inference/internal/proxy/`).

**Why not a separate broker?** The existing inference broker already handles authentication (session tokens), billing (per-token), and settlement (TEE). Building a separate "fine-tune inference broker" would duplicate all of this. Instead, we extend the existing proxy with one additional check: for `ft-*` model names, verify the requester is the task owner.

**Data source**: The LoRA Manager maintains an in-memory map of adapter → owner, populated from on-chain events (DeliverableAcknowledged events contain the user address and task ID). No shared DB needed.

```go
func (p *Proxy) checkLoRAOwnership(modelName, userAddress string) error {
    if !strings.HasPrefix(modelName, "ft-") {
        return nil  // base model: no owner check, standard access
    }
    adapter := p.loraManager.GetAdapter(modelName)
    if adapter == nil {
        return errors.New("model not found: " + modelName)
    }
    if !strings.EqualFold(adapter.OwnerAddress, userAddress) {
        return errors.New("access denied: you are not the owner of this fine-tuned model")
    }
    return nil
}
```

**Integration point**: This check runs AFTER session token validation (which recovers `userAddress`) and BEFORE the request is forwarded to ServerlessLLM. The middleware chain in the proxy becomes:

```
Session token validation → Balance check → [NEW: LoRA owner check] → Request rewrite → Forward
```

### 4.6 Multi-Tier LoRA Caching

Managed by the LoRA Manager, using ServerlessLLM's built-in caching + our 0G Storage cold tier:

```
Active (ServerlessLLM manages GPU/CPU/Disk) ←──→ Archived (0G Storage)
     ↑                                              │
     │    offloadAfterMinutes of inactivity          │
     │    (LoRA Manager calls sllm delete)           │
     └──────────────────────────────────────────────┘
          restore on request
          (download from 0G → decrypt → sllm deploy)
```

ServerlessLLM handles the fast tier (GPU ↔ CPU ↔ Disk) automatically. The LoRA Manager only manages the cold tier (Disk ↔ 0G Storage) for adapters that need to be fully offloaded.

### 4.7 Provider Key Management (Plan B — No Contract Change)

The fine-tuning broker encrypts the LoRA adapter with a random AES key. The user gets the AES key encrypted with their ECIES public key (`EncryptedSecret`). But the inference broker (on a separate node) also needs the AES key to download and decrypt the adapter from 0G Storage.

**Solution**: Embed the provider-encrypted AES key directly in the on-chain `modelRootHash`:

```
Fine-tuning broker (finalizer):
  1. Generate random AES key → encrypt adapter → upload to 0G Storage → storageHash (32 bytes)
  2. Encrypt AES key with user's ECIES public key → EncryptedSecret (for user CLI download)
  3. Encrypt AES key with provider wallet's ECIES public key → providerEncKey (~81 bytes)
  4. Store in DB:  OutputRootHash = hex(storageHash)  (32 bytes, for client CLI compatibility)
  5. Store in contract:  modelRootHash = storageHash + providerEncKey  (~113 bytes)

Inference broker (LoRA Manager):
  1. Event watcher detects DeliverableAcknowledged → calls GetDeliverable() → gets modelRootHash bytes
  2. Parse: storageHash = modelRootHash[:32], providerEncKey = modelRootHash[32:]
  3. Decrypt providerEncKey with provider wallet's ECIES private key → AES key
  4. Download encrypted adapter from 0G Storage using storageHash
  5. Decrypt with AES-GCM using AES key → unzip → deploy to ServerlessLLM
```

**Why this works**:
- Both brokers share the same provider wallet (`networks.privateKeys` in config)
- The `modelRootHash` is `bytes memory` in Solidity — variable length, no contract change needed
- The client CLI reads `OutputRootHash` from the broker API (32-byte hash), not from the contract
- On-chain gas cost increase is negligible (~81 extra bytes ≈ 5,500 gas)

### 4.8 Serving Lifecycle

```
Fine-tuning task completes (Delivered state)
        │
        ▼
Fine-tuning broker calls addDeliverable() on FineTuningServing contract
        │
        ▼
User reviews result and calls acknowledgeDeliverable() on-chain
        │
        ▼
LoRA Manager (inference node) detects DeliverableAcknowledged event
        │
        ▼
Download encrypted adapter from 0G Storage → decrypt with provider key
        │
        ▼
Place adapter in loraModulesDir/ft-<base>-<taskID>/
        │
        ▼
sllm deploy --lora-adapters "ft-<base>-<taskID>=<adapter-path>"
        │
        ▼
Add to in-memory adapter map + persist to local DB
(owner, state, last_access, rootHash)
        │
        ▼
User can now query via inference proxy (using session token)
        │
        ▼  (after offloadAfterMinutes of inactivity)
        │
Offload: sllm delete → mark as "offloaded" in local DB
         (adapter files may remain on disk or be cleaned up)
        │
        ▼  (user sends new request for offloaded adapter)
        │
Restore: download from 0G Storage → decrypt → sllm deploy → update state
```

---

## 5. ServerlessLLM Analysis

### 5.1 What ServerlessLLM Offers

[ServerlessLLM](https://github.com/ServerlessLLM/ServerlessLLM) (OSDI'24) is an open-source system for multi-model GPU multiplexing with 659 stars, Apache 2.0 license:

| Feature | Description |
|---------|-------------|
| **Fast checkpoint loading** | Custom binary format + O_DIRECT I/O → 6-10x faster than SafeTensors |
| **GPU multiplexing** | Run 10+ models on 1 GPU with fast switching and scale-to-zero |
| **LoRA fine-tuning** | PEFT LoRA backend (`ft_backend`) with job management, priority queue, timeout |
| **LoRA serving** | `sllm deploy --enable-lora --lora-adapters` → OpenAI-compatible inference |
| **Storage-aware scheduling** | Locality-driven server allocation minimizes loading latency |
| **Live migration** | Move running models between nodes with zero downtime |
| **OpenAI-compatible API** | Drop-in replacement for `/v1/chat/completions`, `/v1/embeddings` |
| **Docker/K8s ready** | Docker Compose cluster (head + workers), Ray-based |

### 5.2 Coverage Matrix

| Requirement | ServerlessLLM | 0G Must Build | Notes |
|-------------|:------------:|:-------------:|-------|
| LoRA fine-tuning | ✅ | — | PEFT backend, HuggingFace datasets, configurable LoRA rank/alpha |
| LoRA serving (base + adapters) | ✅ | — | vLLM/Transformers backend, named adapters |
| Multi-tier caching (GPU→CPU→Disk) | ✅ | — | Built-in, more mature than custom Go |
| Fast model loading (6-10x) | ✅ | — | `sllm-store`, O_DIRECT I/O, pinned memory DMA |
| Scale to zero | ✅ | — | Auto-scale per model, idle reclamation |
| GPU multiplexing | ✅ | — | Intelligent scheduling across models |
| On-chain contract registration | ❌ | ✅ | `InferenceServing.addOrUpdateService()` |
| Session token authentication | ❌ | ✅ | `app-sk-<base64(token\|sig)>`, expiry, revocation |
| Per-token billing | ❌ | ✅ | `inputPrice × tokens + outputPrice × tokens` |
| TEE-signed settlement | ❌ | ✅ | EIP-712 batch settlement |
| 0G Storage (cold tier) | ❌ | ✅ | Encrypted LoRA upload/download |
| Encrypted model delivery | ❌ | ✅ | AES-GCM + ECIES |
| Per-user access control | ❌ | ✅ | Task owner verification |

### 5.3 Integration Approach: ServerlessLLM as Serving Engine Only

Keep the existing fine-tuning pipeline (Docker-based training, 0G Storage, TEE) unchanged. Replace direct vLLM management with ServerlessLLM **for serving only**. The inference broker handles all traffic (auth, billing, settlement).

ServerlessLLM is NOT used for fine-tuning because: (1) it's not designed for TEE environments, (2) its `ft_backend` doesn't support encrypted 0G Storage datasets, and (3) patching 0G-specific features into its Python codebase would create a maintenance burden.

**How it works**:
1. Fine-tuning task completes → encrypted LoRA uploaded to 0G Storage → `addDeliverable()` on-chain
2. User acknowledges → `acknowledgeDeliverable()` on-chain
3. LoRA Manager (inference node) watches chain events → downloads from 0G Storage → decrypts → `sllm deploy`
4. User sends request → inference proxy (auth, owner check, billing) → rewrite ft-* model → forward to ServerlessLLM → response → fee computation
5. On idle timeout: `sllm delete` the adapter (on-chain service registration unchanged — it's the base model)
6. On restore: download from 0G Storage → decrypt → `sllm deploy`

### 5.4 Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Ray dependency (ServerlessLLM is Ray-based) | Deployment complexity increases | Containerize ServerlessLLM cluster; fine-tuning broker connects via HTTP API |
| ServerlessLLM upstream breaking changes | Adapter management API may change | Pin version, maintain thin Go client wrapper |
| TEE environment compatibility | ServerlessLLM workers may not run in TEE | Run ServerlessLLM outside TEE; only fine-tuning training runs in TEE |
| Cold restore latency | Downloading from 0G Storage + decryption adds time | Use `sllm-store` for fast disk→GPU loading after download; pre-warm popular adapters |
| Single point of failure | ServerlessLLM cluster down = no serving | Health checks + auto-restart; fallback to direct vLLM if needed |

---

## 6. Implementation Plan

### Phase 1: ServerlessLLM Setup + Proxy LoRA Routing — DONE

**Goal**: Deploy ServerlessLLM, add model name mapping and owner check to broker proxy.

1. **~~Deploy ServerlessLLM cluster~~** ✅
   - ServerlessLLM requires GPU (compute capability 7.0+); for CPU-only E2E testing, a mock SLLM server (`mock_sllm.py`) was created that implements the same HTTP API
   - The real ServerlessLLM deployment is deferred to GPU environment testing
   - `sllmUrl` is now configurable so the broker can point to either real SLLM or the mock

2. **~~Point broker `targetUrl` to ServerlessLLM~~** ✅
   - `service.targetUrl` now configurable to SLLM endpoint
   - Verified with mock SLLM in E2E test

3. **~~Add LoRA request rewriting in proxy~~** ✅
   - `ft-*` model detection and rewrite implemented in `api/inference/internal/ctrl/proxy.go`
   - Rewrites `model` → base model name, adds `lora_adapter_name`

4. **~~Add owner-check middleware~~** ✅
   - Owner check for `ft-*` models implemented in proxy
   - Looks up adapter owner from LoRA Manager's in-memory map

### Phase 2: LoRA Manager + On-Chain Event Watching — DONE

**Goal**: Automate adapter lifecycle — discover via chain events, deploy, offload, restore.

1. **~~Build Go HTTP client for ServerlessLLM~~** ✅
   - `api/inference/internal/lora/sllm_client.go` — HTTP client (not CLI-based)
   - `DeployAdapter(baseModel, adapterName, adapterPath)` → `POST /v1/models/deploy`
   - `DeleteAdapter(adapterName)` → `DELETE /v1/models/<name>`
   - `ListModels()` → `GET /v1/models`
   - `HealthCheck()` → `GET /health`

2. **~~Build LoRA Manager in inference module~~** ✅
   - `api/inference/internal/lora/manager.go` — full adapter lifecycle management
   - In-memory adapter map with state tracking (Pending → Active → Offloaded → Failed)
   - DB persistence for crash recovery (`lora_adapters` table via GORM)
   - Offload loop with configurable idle timeout
   - `mockDeploy` mode for CPU-only testing (creates placeholder files)
   - 0G Storage download + AES-GCM decryption now implemented (Phase 3), `mockDeploy` mode still available for CPU-only testing

3. **~~Build Event Watcher~~** ✅
   - `api/inference/internal/lora/event_watcher.go` — polls `FineTuningServing` contract
   - Watches for `DeliverableAcknowledged` events from a checkpoint block number
   - On event: extracts task ID, user address, model root hash → calls LoRA Manager `RegisterAdapter()`
   - Configurable poll interval (`pollBlockIntervalSeconds`)

4. **~~Local adapter metadata table~~** ✅
   - `api/inference/internal/lora/model/adapter.go` — GORM model
   - Fields: adapterName, taskID, userAddress, baseModel, adapterPath, storageRootHash, state, lastAccessedAt
   - Crash recovery: on restart, loads adapters from DB → re-deploys to SLLM

5. **~~End-to-end test~~** ✅
   - `api/e2e-lora-serving/e2e_test.py` — 14/14 tests passed (see Section 9)
   - Full chain: register service → deposit → deliver → acknowledge → event detect → adapter deploy → inference request

### Phase 3: 0G Storage Integration + Optimization — PARTIALLY DONE

1. **~~0G Storage download + decryption in LoRA Manager~~** ✅
   - `StorageDownloader` (`api/inference/internal/lora/storage_downloader.go`) — downloads encrypted adapter from 0G Storage via indexer client
   - `AesDecryptLargeFile` (`api/common/util/crypto.go`) — streaming AES-GCM decryption matching the chunk-based encryption format
   - `ProviderECIESEncrypt/Decrypt` (`api/common/util/crypto.go`) — encrypt/decrypt AES key with provider wallet's ECIES key pair
   - `ParseCombinedModelRootHash` (`api/common/util/crypto.go`) — splits `[32-byte storage hash][N-byte provider-encrypted AES key]` from contract data
   - **Key management approach (Plan B)**: No contract change needed. The fine-tuning broker encrypts the AES key with the provider wallet's ECIES public key and appends it to `modelRootHash` in the contract. The inference broker (same provider wallet) extracts and decrypts it. The DB `OutputRootHash` keeps only the 32-byte storage hash for client CLI compatibility.

2. **Real ServerlessLLM deployment (GPU required)** — TODO
   - ServerlessLLM requires GPU with compute capability >= 7.0
   - Set up ServerlessLLM head + worker(s) via Docker Compose on GPU nodes
   - Deploy base model with `--enable-lora`
   - Point broker `sllmUrl` to real SLLM cluster
   - Alternative: use vLLM directly with `--enable-lora` (simpler, no Ray dependency)

3. **`sllm-store` for faster cold restore** — TODO
   - Convert adapters to ServerlessLLM's optimized format
   - Benchmark loading speedup

4. **Streaming billing** — TODO
   - Token counting from SSE chunks

5. **Adapter sharing** — TODO
   - Allow task owners to grant access to other users

### Phase 4: Production Hardening — TODO

1. **Monitoring**: Prometheus metrics for active adapters, loading latencies, cache hit rates
2. **Rate limiting**: Per-user request limits
3. **Quota management**: Max adapters per user
4. **Fault tolerance**: Auto-restart ServerlessLLM, re-deploy adapters on recovery

---

## 7. Implementation Status (as of 2026-03-05)

### 7.1 Implemented Components

| Component | File(s) | Status |
|-----------|---------|--------|
| **LoRA Manager** | `api/inference/internal/lora/manager.go` | Implemented — adapter lifecycle, in-memory map, DB persistence, mock deploy mode, 0G Storage download+decrypt |
| **SLLM HTTP Client** | `api/inference/internal/lora/sllm_client.go` | Implemented — deploy, delete, list, health check via HTTP API |
| **Event Watcher** | `api/inference/internal/lora/event_watcher.go` | Implemented — polls FineTuningServing contract for DeliverableAcknowledged events, handles combined modelRootHash |
| **Storage Downloader** | `api/inference/internal/lora/storage_downloader.go` | Implemented — 0G Storage download → ECIES decrypt AES key → AES-GCM decrypt adapter → unzip |
| **DB Model** | `api/inference/internal/lora/model/adapter.go` | Implemented — GORM model for crash recovery |
| **Proxy LoRA routing** | `api/inference/internal/ctrl/proxy.go` | Implemented — ft-* model name rewrite + owner check |
| **Config** | `api/inference/config/config.go` | Implemented — LoRAConfig with sllmUrl, mockDeploy, storageIndexerUrl fields |
| **Server init** | `api/inference/cmd/server/main.go` | Implemented — initializes LoRA Manager + Event Watcher on startup when `lora.enable: true` |
| **CPU-only executor** | `api/fine-tuning/internal/services/executor.go` | Implemented — skips nvidia runtime when gpuCount=0 |
| **Provider ECIES key sharing** | `api/fine-tuning/internal/services/finalizer.go` | Implemented — encrypts AES key with provider wallet ECIES pubkey, embeds in contract modelRootHash |
| **AES large file decrypt** | `api/common/util/crypto.go` | Implemented — `AesDecryptLargeFile` (streaming, matches `AesEncryptLargeFile` chunk format) |
| **Provider ECIES encrypt/decrypt** | `api/common/util/crypto.go` | Implemented — `ProviderECIESEncrypt`, `ProviderECIESDecrypt`, `ParseCombinedModelRootHash` |
| **Provider key helper** | `api/common/config/network.go` | Implemented — `GetProviderPrivateKey` extracts wallet key from config for both brokers |
| **Mock SLLM** | `api/e2e-lora-serving/mock_sllm.py` | Implemented — Python HTTP server mimicking ServerlessLLM API |
| **E2E test script** | `api/e2e-lora-serving/e2e_test.py` | Implemented — 14 test assertions covering full lifecycle |

### 7.2 Not Yet Implemented

| Component | Blocker | Notes |
|-----------|---------|-------|
| **Real ServerlessLLM deployment** | Requires GPU (compute capability >= 7.0) | Can use vLLM with `--enable-lora` as lighter alternative |
| **Adapter offload to 0G Storage** | Depends on cold storage design | Currently offload only calls `sllm delete`, does not re-upload |
| **Full E2E with real 0G Storage** | Needs 0G Storage testnet connectivity from E2E env | Code path implemented, needs integration test on testnet |

---

## 8. E2E Test Results (2026-03-05)

### 8.1 Test Environment

Two **CPU-only** machines (no GPU), all components running locally:

| Component | Implementation | Port |
|-----------|---------------|------|
| **Hardhat local chain** | Docker container, auto-deploys LedgerManager + InferenceServing + FineTuningServing | 8545 |
| **Inference Broker** | Real Go binary from `feat/serverless-llm-serving` branch | 3081 |
| **Mock ServerlessLLM** | Python HTTP server (`mock_sllm.py`) | 8343 |
| **MySQL** | Docker container for inference broker DB | 3308 |

Fine-tuning broker was **not** started — the test script simulates the fine-tuning side by directly calling contract functions via `web3.py`.

### 8.2 Test Flow and Results

```
14 passed / 0 failed / 14 total
```

| Step | Test | Result | Detail |
|------|------|--------|--------|
| 0 | Hardhat chain connectivity | PASS | Block height > 0 |
| 0 | Inference broker health | PASS | `GET /v1/quote` → 200 |
| 0 | Mock SLLM health | PASS | `GET /health` → 200 |
| 1 | Provider registers FT service | PASS | `addOrUpdateService()` with 100 ETH stake |
| 2 | User creates ledger | PASS | `addLedger()` with 5 ETH deposit |
| 2 | User transfers to FT provider | PASS | `transferFund("fine-tuning-v1.0", 1 ETH)` |
| 3 | Provider adds deliverable | PASS | `addDeliverable(taskID, modelRootHash)` |
| 3 | Deliverable readable on-chain | PASS | `getDeliverable()` returns data |
| 4 | User acknowledges deliverable | PASS | `acknowledgeDeliverable()` tx confirmed |
| 4 | DeliverableAcknowledged event | PASS | Event emitted in block |
| 5 | Event Watcher detects adapter | PASS | Adapter appeared in mock SLLM within 3s |
| 6 | Inference request to mock SLLM | PASS | `POST /v1/chat/completions` → 200 |
| 6 | Response content correct | PASS | Echo response with adapter name |
| 6 | Token usage reported | PASS | `{prompt_tokens: 4, completion_tokens: 8, total_tokens: 12}` |

### 8.3 Key Findings During Testing

1. **LedgerManager service name** — `transferFund()` requires the full registered name including version (e.g., `fine-tuning-v1.0`), not just the base name (`fine-tuning`). Using the wrong name causes `ServiceNotFound("0x0000...")`.

2. **Minimum deposit** — `addLedger()` requires at least 3 ETH; `transferFund()` requires at least 1 ETH. Below these thresholds, the contract reverts with `MinimumDepositRequired`.

3. **Broker auto-registration** — The inference broker auto-registers its service on-chain at startup. The E2E script handles this idempotently (catches `CannotAddStakeWhenUpdating` and `LedgerExists` errors gracefully).

4. **Event Watcher latency** — From `acknowledgeDeliverable()` tx to adapter appearing in mock SLLM: ~3 seconds (1 poll cycle at `pollBlockIntervalSeconds: 3`).

5. **CPU-only executor** — Added `gpuCount=0` check in `executor.go` to skip nvidia runtime request. Without this, the executor would fail on CPU-only machines even for mock training.

### 8.4 What the E2E Test Validates vs. What It Mocks

| Layer | Real or Mock? | Detail |
|-------|--------------|--------|
| Smart contracts (LedgerManager, FT, INF) | **Real** | Actual Solidity contracts deployed on Hardhat |
| On-chain transactions | **Real** | Signed and executed via web3.py |
| Inference Broker (Go binary) | **Real** | Full binary from feat/serverless-llm-serving branch |
| Event Watcher | **Real** | Polls Hardhat chain, processes events |
| LoRA Manager | **Real** | Registers adapter, calls SLLM client, persists to DB |
| SLLM Client (Go HTTP calls) | **Real** | Makes actual HTTP requests to SLLM endpoint |
| ServerlessLLM | **Mock** | Python server, no real model loading or inference |
| Fine-tuning training | **Mock** | Test script calls `addDeliverable()` directly |
| 0G Storage download | **Mock** | `mockDeploy: true` creates placeholder files |
| Model inference | **Mock** | Returns echo response, no real LLM computation |

The test validates the **control plane** (contract interactions, event-driven adapter discovery, adapter lifecycle management) but not the **data plane** (real model loading, GPU inference, 0G Storage transfer). Validating the data plane requires GPU machines + real ServerlessLLM.

---

## 9. Open Questions

1. **LoRA decryption key management** (critical, needs TEE team input)  
   The fine-tuning node encrypts the LoRA adapter with the user's public key before uploading to 0G Storage. The inference node (a separate TEE) needs the decrypted version. How does the inference node obtain the decryption key? This likely requires a provider-level key scheme: the fine-tuning node re-encrypts with a provider key so any of the provider's TEE nodes can decrypt.

2. **On-chain event reliability**  
   The LoRA Manager watches `FineTuningServing` contract events. It must handle: missed events during downtime (replay from checkpoint block number), chain reorgs (sufficient confirmation depth), and event indexing at scale.

3. **User account across two contracts**  
   A user must deposit to BOTH `FineTuningServing` (training fees) and `InferenceServing` (inference fees). The SDK/CLI should guide users through both deposits in a single workflow to avoid confusion.

4. **Future: per-adapter pricing**  
   Currently all LoRA adapters share the base model's price. If per-adapter pricing is needed later, the `InferenceServing` contract must be upgraded to support multi-service per provider (`keccak256(provider, serviceName)` as key).

5. **ServerlessLLM version pinning**  
   ServerlessLLM is pre-1.0 and actively developed. Pin to a specific commit and maintain a thin Go client wrapper to isolate API changes.

6. **GPU resource isolation**  
   GPU 0 for training, GPU 1 for inference, enforced via `CUDA_VISIBLE_DEVICES`. Need to define behavior for providers with variable GPU counts.

7. **Provider restart recovery**  
   On restart, LoRA Manager re-loads adapter metadata from local DB → checks adapter files on disk → `sllm deploy` for existing → downloads from 0G Storage for missing. Need to handle partial failures and race conditions with incoming requests.

---

## 10. References

- [ServerlessLLM (OSDI'24)](https://github.com/ServerlessLLM/ServerlessLLM) — Fast multi-model serving with GPU multiplexing
- [ServerlessLLM Paper](https://www.usenix.org/system/files/osdi24-fu.pdf) — Architecture details, checkpoint loading optimization
- [ServerlessLLM Documentation](https://serverlessllm.github.io/docs/stable/) — Deployment and API reference
- [vLLM LoRA support](https://docs.vllm.ai/en/latest/features/lora.html) — Dynamic LoRA loading (used by ServerlessLLM internally)
- [0G Inference Module](../../../inference/) — Contract, auth, billing, settlement code
- [0G Fine-Tuning Module](./) — Task lifecycle, 0G Storage, TEE
- PR #373 — Fine-tuning monitoring (merged)
- PR #374 — Previous serving implementation (on hold, to be superseded by this design)
- PR #379 — ServerlessLLM-based LoRA serving implementation + E2E test (this design)
- `api/e2e-lora-serving/` — E2E test script and mock SLLM server
