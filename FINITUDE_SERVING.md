# Finitude Serving Technical Documentation

## 1. Project Overview

**Finitude Serving** is a Serverless LoRA inference service system for the 0G Compute Network, enabling users to deploy fine-tuned LoRA adapters to GPUs for real-time inference.

### Core Value Propositions
- **One-click Deployment**: Automatically sync fine-tuned models to inference services
- **Multi-tenant Isolation**: Strict user access control ensuring model security
- **Auto Scaling**: Idle models automatically offloaded, restored on-demand
- **Cost Optimization**: Share base models + dynamically load LoRAs, reducing GPU memory usage

### System Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Finitude Serving                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  ┌──────────────┐      ┌──────────────┐      ┌──────────────┐   │
│  │   User (CLI) │─────▶│ Fine-tuning │─────▶│  Inference   │   │
│  │              │      │   Broker    │      │   Broker     │   │
│  └──────────────┘      └──────┬───────┘      └──────┬───────┘   │
│                               │                     │           │
│                               ▼                     ▼           │
│                        ┌─────────────┐      ┌─────────────┐     │
│                        │ 0G Storage  │      │  vLLM/GPU   │     │
│                        │ (Model Store)│      │  (Inference)│     │
│                        └─────────────┘      └─────────────┘     │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

## 2. Architecture Design

### 2.1 Component Responsibilities

| Component | Responsibility | Key Technologies |
|-----------|----------------|------------------|
| **Fine-tuning Broker** | Manage training tasks, encrypt models, upload to 0G Storage | Go, GORM, Docker SDK |
| **Inference Broker** | Manage LoRA lifecycle, request routing, billing | Go, Gin, gormigrate |
| **SLLM Wrapper** | vLLM interaction layer, dynamic LoRA load/unload | Python, FastAPI |
| **0G Storage** | Decentralized model storage | 0g-storage-client |
| **MySQL** | Task state, adapter metadata | MySQL 8.0 |

### 2.2 Data Flow

```
1. Training Complete
   Fine-tuning Broker → Encrypt LoRA → 0G Storage
                          ↓
                    Store root hash on-chain

2. User Acknowledge
   CLI → Inference Broker → Download from 0G Storage + Decrypt
                              ↓
                         State: Ready (downloaded, not deployed)

3. User Deploy
   CLI → Inference Broker → SLLM Wrapper → vLLM
                              ↓
                         State: Active (loaded in GPU)

4. Inference Request
   User → Inference Broker → SLLM Wrapper → vLLM (base + LoRA)
             ↓
        Verify ownership + Update last access time
```

## 3. Multi-tenant Isolation

### 3.1 User Ownership Verification

Each LoRA adapter is bound to its creator's address:

```go
// CheckLoRAOwnership verifies if user has access to the model
func (c *Ctrl) CheckLoRAOwnership(modelName, userAddress string) error {
    adapter := c.loraManager.GetAdapter(modelName)
    
    // Strict address comparison
    if !strings.EqualFold(adapter.UserAddress, userAddress) {
        return fmt.Errorf("access denied: you are not the owner of model %s", modelName)
    }
    
    // Check adapter state
    switch adapter.State {
    case model.AdapterStateActive:
        c.loraManager.RecordAccess(modelName)  // Update access time
        return nil
    case model.AdapterStateOffloaded:
        return fmt.Errorf("model %s is offloaded, please retry later", modelName)
    // ... other state handling
    }
}
```

### 3.2 Isolation Levels

| Layer | Mechanism | Description |
|-------|-----------|-------------|
| **Request Layer** | Bearer Token + Signature Verification | Each request carries user signature for identity verification |
| **Model Layer** | UserAddress Binding | Adapter metadata records owner address |
| **Storage Layer** | Independent Encryption Keys | Each LoRA uses user's public key to encrypt AES key |
| **Inference Layer** | Adapter Namespace | `ft-{model}-{taskID}` format, globally unique |

## 4. LoRA Lifecycle Management

### 4.1 State Machine

```
┌─────────┐   acknowledge    ┌─────────┐   deploy    ┌─────────┐
│  Init   │ ───────────────▶ │  Ready  │ ─────────▶ │ Active  │
│ (on-chain)│                  │ (downloaded)│      │ (in GPU) │
└─────────┘                  └─────────┘            └────┬────┘
                                                         │
                              ┌──────────────────────────┘
                              │ idle > threshold
                              ▼
                         ┌─────────┐   request    ┌─────────┐
                         │Offloaded│ ◀────────── │ Restoring│
                         │(unloaded)│ ────────▶ │ (restoring)│
                         └─────────┘              └─────────┘
```

### 4.2 Auto Offload Mechanism

**Trigger Conditions:**
- Configuration `offloadAfterMinutes` (default 60 minutes)
- Adapter idle in GPU exceeds threshold

**Execution Flow:**

```go
func (m *Manager) offloadLoop(ctx context.Context) {
    ticker := time.NewTicker(interval)
    for {
        <-ticker.C
        
        // 1. Query adapters exceeding idle threshold
        idle, _ := m.db.ListIdleAdapters(threshold)
        
        for _, a := range idle {
            // 2. Delete from vLLM, free GPU memory
            m.sllmClient.DeleteAdapter(ctx, a.AdapterName)
            
            // 3. Update state to Offloaded
            m.setAdapterState(a.AdapterName, model.AdapterStateOffloaded)
        }
    }
}
```

**Features:**
- Only unloads GPU weights, metadata remains in database
- Files remain on disk (can configure `enableColdStorage` to upload to 0G Storage)

### 4.3 Auto Restore Mechanism

When user requests an offloaded model:

```go
func (m *Manager) RestoreAdapter(ctx context.Context, adapterName string) error {
    // Check current state
    if info.State == model.AdapterStateActive || 
       info.State == model.AdapterStateLoading {
        return nil  // Already active
    }
    
    // Async re-download and deploy
    m.setAdapterState(adapterName, model.AdapterStateLoading)
    go m.downloadAdapter(ctx, info)
    return nil
}
```

**Note:** Current implementation returns "please retry later" for user to retry later, avoiding request blocking. Future versions may optimize to synchronous wait or push notification.

## 5. Deployment Guide

### 5.1 Prerequisites

- Phala TEE CVM environment (with GPU)
- MySQL 8.0 database
- 0G Storage client (testnet requires turbo indexer)
- Docker & Docker Compose

### 5.2 Fine-tuning Broker Configuration

```yaml
# config-ft.yaml
contractAddress: "0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569"  # FineTuningServing

database:
  fineTune: "root:password@tcp(mysql-ft:3306)/fineTune?parseTime=true"

service:
  servingUrl: "https://<your-cvm-hash>-3080.dstack-pha-in2.phala.network"
  dataDir: /dstack/persistent/e2e/ft-data
  pricePerToken: 800000000000
  skipStorageUpload: false
  inferenceServiceUrl: "https://<inference-cvm>-3081.dstack-pha-in2.phala.network"
  inferenceServiceSecret: "<shared-secret>"  # Shared with inference broker

storageClient:
  indexerTurbo: "https://indexer-storage-testnet-turbo.0g.ai"
```

### 5.3 Inference Broker Configuration

```yaml
# config-inference.yaml
contractAddress: "0x41bD7Ac5c19000A974D5c192bcd5FB67b56C85c5"  # InferenceServing
skipTEESignerCheck: true  # For dev/testing

database:
  provider: "root:password@tcp(mysql-inf:3306)/provider?parseTime=true"

service:
  targetUrl: "http://sllm-wrapper:8343/v1"
  type: "chatbot"
  model: "Qwen2.5-0.5B-Instruct"
  servingUrl: "https://<your-cvm-hash>-3081.dstack-pha-in2.phala.network"

lora:
  enable: true
  baseModel: "/models/Qwen2.5-0.5B-Instruct"
  loraModulesDir: "/lora-modules"
  sllmUrl: "http://sllm-wrapper:8343"
  offloadAfterMinutes: 60        # Idle time before offload
  enableColdStorage: true          # Enable cold storage
  autoDeploy: false                # Auto-deploy on acknowledge
  internalApiSecret: "<shared-secret>"  # Shared with FT broker
  fineTuningContractAddress: "0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569"
  storageIndexerUrl: "https://indexer-storage-testnet-turbo.0g.ai"
```

### 5.4 Docker Compose Example

```yaml
# docker-compose.yml
services:
  mysql-ft:
    image: mysql:8.0
    environment:
      MYSQL_ROOT_PASSWORD: "123456"
      MYSQL_DATABASE: fineTune
    ports:
      - "33060:3306"
    volumes:
      - mysql-ft-data:/var/lib/mysql

  fine-tuning-broker:
    image: ghcr.io/0gfoundation/0g-serving-broker:latest
    privileged: true
    environment:
      - PORT=3080
      - CONFIG_FILE=/etc/config.yaml
      - NVIDIA_VISIBLE_DEVICES=all
    volumes:
      - /var/run/dstack.sock:/var/run/dstack.sock
      - /var/run/docker.sock:/var/run/docker.sock
      - ./config-ft.yaml:/etc/config.yaml:ro
      - ./broker:/usr/bin/broker  # Custom binary
    command: 0g-fine-tuning-server
    runtime: nvidia
    depends_on:
      - mysql-ft

volumes:
  mysql-ft-data:
```

## 6. Key Configuration Notes

### 6.1 Two-CVM Shared Secret

Fine-tuning Broker and Inference Broker exchange encrypted AES keys via internal API, requiring the same shared secret:

```yaml
# FT Broker
service:
  inferenceServiceUrl: "https://inference-broker-url"
  inferenceServiceSecret: "YOUR_SHARED_SECRET"

# Inference Broker
lora:
  internalApiSecret: "YOUR_SHARED_SECRET"  # Must match
```

### 6.2 Auto Deploy Mode

With `lora.autoDeploy: true`, models are immediately loaded to GPU after user acknowledge:

```yaml
lora:
  autoDeploy: true   # Auto-deploy on acknowledge
  offloadAfterMinutes: 30  # Offload after 30 min idle
```

**Use Cases:**
- `autoDeploy: false` (default): User controls deployment timing, cost-sensitive scenarios
- `autoDeploy: true`: Instant availability, suitable for quick demo scenarios

### 6.3 TEE Signer Acknowledgement

Each new CVM deployment generates a new TEE Signer address, requiring contract owner to acknowledge:

```python
# Contract owner executes
fine_tuning_contract.functions.acknowledgeTEESignerByOwner(
    provider_address  # e.g., 0x87a13337F0d4B2b08cce9189DBE9555690828ed4
).transact({'from': owner_address})
```

## 7. CLI Usage Flow

### 7.1 Complete E2E Workflow

```bash
# 1. Environment setup
export ZG_DEV_MODE=true

# 2. Prepare dataset
cat > dataset.jsonl << 'EOF'
{"messages":[{"role":"user","content":"What is 0G?"},{"role":"assistant","content":"0G is decentralized AI infrastructure."}]}
EOF

# 3. Upload dataset
0g-compute-cli fine-tuning upload --data-path ./dataset.jsonl
# Save the output root hash: 0x7be439e4...

# 4. Create training task
0g-compute-cli fine-tuning create-task \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --dataset <ROOT_HASH> \
  --config-path ./train-config.json
# Save task ID: aa276611-ee31-...

# 5. Wait for training (check until status is Delivered)
0g-compute-cli fine-tuning get-task --provider <PROVIDER> --task <TASK_ID>

# 6. Acknowledge model (download locally)
0g-compute-cli fine-tuning acknowledge-model \
  --provider <PROVIDER> \
  --task-id <TASK_ID> \
  --data-path ./my-model

# 7. Deploy to GPU
0g-compute-cli fine-tuning deploy-adapter \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID> \
  --wait

# 8. Chat test
0g-compute-cli fine-tuning chat \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID> \
  --message "Hello! What can you do?"
```

## 8. Troubleshooting

### 8.1 Common Errors

| Error | Cause | Solution |
|-------|-------|----------|
| `TEE signer not acknowledged` | New CVM not acknowledged | Contract owner calls `acknowledgeTEESignerByOwner` |
| `Unknown column 'provider_encrypted_secret'` | Database not migrated | Run `AutoMigrate` or manual ALTER TABLE |
| `403 Forbidden` on push adapter key | Shared secret mismatch | Check FT/Inference broker `internalApiSecret` |
| `model is offloaded, please retry later` | Model offloaded | Wait seconds and retry, or check broker logs |
| `file not found` on 0G Storage | File not synced | Use `--download-method tee` to fallback to TEE download |

### 8.2 Viewing Logs

```bash
# Fine-tuning Broker
docker logs fine-tuning-broker --tail 100

# Inference Broker
docker logs inference-broker --tail 100

# SLLM Wrapper
docker logs sllm-wrapper --tail 100
```

### 8.3 Database Queries

```sql
-- Check task status
SELECT id, progress, user_address, fee FROM task WHERE id = '<task_id>';

-- Check LoRA adapters
SELECT adapter_name, state, user_address, last_access_at FROM lora_adapters;
```

## 9. Security Considerations

1. **Shared Secret**: `internalApiSecret` must be complex and rotated regularly
2. **TEE Signer**: Each CVM is unique, changing CVM requires re-acknowledgement
3. **Model Encryption**: AES-GCM encrypts LoRA files, ECIES encrypts AES keys
4. **Access Control**: Strict `userAddress` verification prevents unauthorized access

## 10. Version History

| Version | Date | Changes |
|---------|------|---------|
| v0.1 | 2025-02 | Initial version, basic LoRA serving |
| v0.2 | 2025-03 | Added 2-CVM architecture, auto offload/restore |

---

**Links:**
- 0G Compute Network: https://docs.0g.ai
- Fine-tuning Guide: `./api/e2e-lora-serving/README.md`
- Contract Addresses: See `ZG_DEV_MODE` configs
