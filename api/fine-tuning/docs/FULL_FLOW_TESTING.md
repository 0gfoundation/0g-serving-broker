# Fine-Tuning Full Flow Testing Guide

This guide documents the complete end-to-end testing flow for the 0G Fine-Tuning service.

## Prerequisites

### 1. CVM Environment (Broker Side)

- Docker running with access to `/var/run/docker.sock`
- Pre-trained model available (local path or HuggingFace fallback)
- `qwen-lora:v3` Docker image for training execution
- MySQL database container running

### 2. Client Environment

- Node.js 18+
- `0g-compute-cli` installed and linked
- Private key with sufficient balance for gas fees
- Network access to CVM broker endpoint

### 3. Contract Setup

- Fine-tuning contract deployed (testnetDev or mainnet)
- Provider registered on the contract
- User account created with the provider

## Quick Start (CLI)

### Install CLI

```bash
cd 0g-serving-user-broker
npm install && npm run build
npm link
```

### Complete Flow Commands

```bash
# Set dev mode for testnet
export ZG_DEV_MODE=true

# 1. List available providers
0g-compute-cli fine-tuning list-providers

# 2. List available models
0g-compute-cli fine-tuning list-models

# 3. Create task with dataset upload to TEE
0g-compute-cli fine-tuning create-task \
  --provider <PROVIDER_ADDRESS> \
  --model Qwen2.5-0.5B-Instruct \
  --dataset-path ./dataset.jsonl \
  --config-path ./config.json \
  --data-size 100

# 4. Monitor task progress
0g-compute-cli fine-tuning get-task \
  --provider <PROVIDER_ADDRESS> \
  --task <TASK_ID>

# 5. Download and acknowledge model (after status = Delivered)
0g-compute-cli fine-tuning acknowledge-model \
  --provider <PROVIDER_ADDRESS> \
  --task-id <TASK_ID> \
  --data-path ./output

# 6. Decrypt the model
0g-compute-cli fine-tuning decrypt-model \
  --provider <PROVIDER_ADDRESS> \
  --task-id <TASK_ID> \
  --encrypted-model ./output/lora_model_<TASK_ID>.zip \
  --output ./output/lora_decrypted.zip

# 7. Unzip to get LoRA adapter
unzip ./output/lora_decrypted.zip -d ./output/
```

## Test Data Preparation

### Dataset (dataset.jsonl)

Create a JSONL file with instruction-input-output format:

```json
{"instruction": "Translate to French", "input": "Hello world", "output": "Bonjour le monde"}
{"instruction": "Translate to French", "input": "Good morning", "output": "Bonjour"}
{"instruction": "Translate to French", "input": "Thank you", "output": "Merci"}
{"instruction": "Translate to French", "input": "How are you?", "output": "Comment allez-vous?"}
{"instruction": "Translate to French", "input": "Goodbye", "output": "Au revoir"}
```

Or use chat/messages format:

```json
{"messages": [{"role": "user", "content": "What is 2+2?"}, {"role": "assistant", "content": "2+2 equals 4."}]}
{"messages": [{"role": "user", "content": "Hello"}, {"role": "assistant", "content": "Hi there!"}]}
```

### Training Config (config.json)

```json
{
    "num_train_epochs": 1,
    "per_device_train_batch_size": 1,
    "learning_rate": 0.0001,
    "max_steps": 5,
    "max_seq_length": 128,
    "lora_r": 8,
    "lora_alpha": 16
}
```

## Broker Configuration

### config.yaml (CVM)

```yaml
contract:
  fineTuning:
    address: "0xYourContractAddress"
    chainRpc: "https://evmrpc-testnet.0g.ai"
    privateKey: "your_provider_private_key"

service:
  # Skip 0G Storage upload - users download LoRA directly from TEE
  # Useful for testing or when 0G Storage is unavailable
  skipStorageUpload: true
  
  # Price per token in neuron (1 neuron = 10^-18 A0GI)
  pricePerToken: 1
  
  # Minimum provider balance required
  minBalance: 100000000000000000  # 0.1 A0GI
  
  # Local model paths - maps model name to local file path
  # When set, broker uses local model instead of downloading from HuggingFace
  modelLocalPaths:
    "Qwen2.5-0.5B-Instruct": "/dstack/persistent/models/Qwen2.5-0.5B-Instruct"
    "Qwen3-32B": "/dstack/persistent/models/Qwen3-32B"

database:
  fineTune: root:password@tcp(db-host:3306)/fineTune?parseTime=true
```

### Docker Run Command (Broker)

```bash
docker run -d --name broker \
  --network provider_default \
  -p 80:8080 \
  -v /tmp:/tmp \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/run/dstack.sock:/var/run/dstack.sock \
  -v /dstack/persistent:/dstack/persistent \
  -v /path/to/config.yaml:/etc/config.yaml \
  ghcr.io/0gfoundation/0g-serving-broker:latest \
  /dstack/broker --config /etc/config.yaml
```

**Important Volume Mounts:**

| Mount | Purpose |
|-------|---------|
| `/tmp:/tmp` | Share temp directories with training containers |
| `/var/run/docker.sock` | Allow broker to create training containers |
| `/var/run/dstack.sock` | Key derivation for encryption |
| `/dstack/persistent` | Persistent model storage |

#### How Docker Volume Mounts Work

**Why `/tmp:/tmp` is Required:**

1. **Task Directory Creation**: When a task is created, the broker creates a temporary directory at `/tmp/<task-id>` inside the broker container (via `utils.GetDataDir()` which defaults to `os.TempDir()` = `/tmp`).

2. **Training Container Mounts**: When the executor creates a training container, it needs to mount the task directory (`paths.BasePath`, e.g., `/tmp/<task-id>`) into the training container using Docker bind mount.

3. **Docker Bind Mount Requirement**: Docker bind mounts require the **source path to exist on the host machine**, not just inside the container. Without `-v /tmp:/tmp`:
   - Broker creates `/tmp/<task-id>` inside its container filesystem
   - This path doesn't exist on the host
   - Docker cannot create a bind mount from a non-existent host path
   - Result: `bind source path does not exist` error

4. **With `/tmp:/tmp` Mount**:
   - Broker's `/tmp` is directly mapped to host's `/tmp`
   - When broker creates `/tmp/<task-id>`, it's actually created on the host
   - Executor can successfully mount `/tmp/<task-id>` from host to training container
   - Training container can access all task files (dataset, model, config, output)

**Visual Flow:**

```
Host Machine                    Broker Container              Training Container
─────────────────              ──────────────────            ──────────────────
/tmp/                           /tmp/ (mapped)                 
  └─ <task-id>/  ◄───────────────┘                              /app/mnt/ (mounted)
      ├─ data/                                                  ├─ data/
      ├─ model/                                                 ├─ model/
      ├─ config.json                                            ├─ config.json
      └─ output_model/                                          └─ output_model/
```

**Alternative: Using `/dstack/persistent` for Large Models**

For large models like Qwen3-32B, task directories should be stored in `/dstack/persistent` instead of `/tmp`:

```bash
# In broker config.yaml
service:
  dataDir: /dstack/persistent/tasks  # Override default /tmp
```

Then mount `/dstack/persistent`:
```bash
-v /dstack/persistent:/dstack/persistent
```

This ensures:
- Large model files don't fill up `/tmp`
- Persistent storage survives container restarts
- Better disk space management

## Flow Diagram

```
User (CLI)                    Broker (TEE)                    Contract
  │                              │                               │
  │── 1. Upload Dataset ────────▶│                               │
  │     (--dataset-path)         │── Convert JSONL to HF ───────▶│
  │◀── Dataset Hash ────────────│                               │
  │                              │                               │
  │── 2. Create Task ───────────▶│                               │
  │     (create-task cmd)        │                               │
  │◀── Task ID ─────────────────│                               │
  │                              │                               │
  │                              │── Setup (load model/data) ──▶│
  │                              │── Training (LoRA) ──────────▶│
  │                              │── Encrypt LoRA ─────────────▶│
  │                              │                               │
  │                              │  [if skipStorageUpload=false] │
  │                              │── Upload to 0G Storage ─────▶│
  │                              │                               │
  │                              │── Add Deliverable ──────────▶│
  │                              │                               │
  │── 3. Get Task Status ───────▶│                               │
  │◀── Progress: Delivered ─────│                               │
  │                              │                               │
  │── 4. Download LoRA ─────────▶│                               │
  │     (acknowledge-model)      │                               │
  │◀── Encrypted LoRA ──────────│                               │
  │                              │                               │
  │── 5. Acknowledge ──────────────────────────────────────────▶│
  │                              │                               │
  │── 6. Decrypt LoRA ──────────▶│ (local decryption)           │
  │     (decrypt-model)          │                               │
```

## Task Progress States

| State | Description |
|-------|-------------|
| `Init` | Task created, waiting for setup |
| `SettingUp` | Loading model and dataset |
| `SetUp` | Ready for training |
| `Training` | LoRA training in progress |
| `Trained` | Training complete, encrypting |
| `Delivered` | Deliverable registered on contract |
| `UserAcknowledged` | User confirmed receipt |
| `Finished` | Complete |
| `Failed` | Error occurred |

## Model Configuration

| Model | Local Path | HuggingFace Fallback |
|-------|------------|---------------------|
| Qwen2.5-0.5B-Instruct | `/dstack/persistent/models/Qwen2.5-0.5B-Instruct` | `Qwen/Qwen2.5-0.5B-Instruct` |
| Qwen3-32B | `/dstack/persistent/models/Qwen3-32B` | `Qwen/Qwen3-32B` |

## Timing Reference

### Qwen2.5-0.5B-Instruct (5 training steps)

- Setup: ~1 minute
- Training: ~30 seconds
- Finalize: ~30 seconds
- **Total: ~2-3 minutes**

### Qwen3-32B (5 training steps)

- Setup: ~5-10 minutes (model loading)
- Training: ~10-20 minutes
- **Total: ~15-30 minutes**

## Troubleshooting

### "previous deliverable not acknowledged"

**Cause:** A previous task's deliverable exists but hasn't been acknowledged.

**Solution:** Call `acknowledge-model` for the previous task before creating a new one.

### "signature required" when downloading

**Cause:** The LoRA download endpoint requires authentication.

**Solution:** Use the CLI command which handles signature automatically:
```bash
0g-compute-cli fine-tuning acknowledge-model --provider ... --task-id ...
```

### Task stuck in "SettingUp"

**Cause:** Model or dataset loading issues.

**Check:**
- Model exists at configured local path or HuggingFace fallback is accessible
- Dataset was successfully converted to HuggingFace format
- Sufficient disk space on CVM

### "bind source path does not exist"

**Cause:** Docker volume mount issue - broker's `/tmp` not shared with host.

**Solution:** Ensure broker container has `-v /tmp:/tmp` mount.

### "dial unix /var/run/dstack.sock: connect: no such file or directory"

**Cause:** dstack socket not mounted in broker container.

**Solution:** Add `-v /var/run/dstack.sock:/var/run/dstack.sock` to broker container.

### "insufficient provider balance"

**Cause:** Provider wallet balance below minimum required.

**Solution:** Transfer funds to provider wallet address.

## Test Success Criteria

- [ ] Dataset uploaded to TEE and hash returned
- [ ] Task created successfully with Task ID
- [ ] Task progresses: Init → SetUp → Training → Trained → Delivered
- [ ] Encrypted LoRA downloaded (non-empty file, ~50-100MB)
- [ ] Deliverable acknowledged on contract
- [ ] Model decrypted successfully
- [ ] LoRA adapter files present (adapter_config.json, adapter_model.safetensors)

## Example Complete Test Session

```bash
# Prepare test files
mkdir -p /tmp/finetuning-test
cat > /tmp/finetuning-test/dataset.jsonl << 'EOF'
{"instruction": "Translate to French", "input": "Hello world", "output": "Bonjour le monde"}
{"instruction": "Translate to French", "input": "Good morning", "output": "Bonjour"}
{"instruction": "Translate to French", "input": "Thank you", "output": "Merci"}
{"instruction": "Translate to French", "input": "How are you?", "output": "Comment allez-vous?"}
{"instruction": "Translate to French", "input": "Goodbye", "output": "Au revoir"}
EOF

cat > /tmp/finetuning-test/config.json << 'EOF'
{
    "num_train_epochs": 1,
    "per_device_train_batch_size": 1,
    "learning_rate": 0.0001,
    "max_steps": 5,
    "max_seq_length": 128,
    "lora_r": 8,
    "lora_alpha": 16
}
EOF

# Run test
export ZG_DEV_MODE=true
PROVIDER=0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC

# Create task
TASK_ID=$(0g-compute-cli fine-tuning create-task \
  --provider $PROVIDER \
  --model Qwen2.5-0.5B-Instruct \
  --dataset-path /tmp/finetuning-test/dataset.jsonl \
  --config-path /tmp/finetuning-test/config.json \
  --data-size 100 2>&1 | grep "Created Task ID" | awk '{print $NF}')

echo "Task ID: $TASK_ID"

# Poll until delivered
while true; do
  STATUS=$(0g-compute-cli fine-tuning get-task --provider $PROVIDER --task $TASK_ID 2>&1 | grep -o 'Progress: [^,]*' | cut -d' ' -f2)
  echo "Status: $STATUS"
  if [ "$STATUS" = "Delivered" ] || [ "$STATUS" = "Failed" ]; then
    break
  fi
  sleep 15
done

# Download and decrypt
if [ "$STATUS" = "Delivered" ]; then
  0g-compute-cli fine-tuning acknowledge-model \
    --provider $PROVIDER \
    --task-id $TASK_ID \
    --data-path /tmp/finetuning-test/output
    
  0g-compute-cli fine-tuning decrypt-model \
    --provider $PROVIDER \
    --task-id $TASK_ID \
    --encrypted-model /tmp/finetuning-test/output/lora_model_${TASK_ID}.zip \
    --output /tmp/finetuning-test/output/lora_decrypted.zip
    
  unzip /tmp/finetuning-test/output/lora_decrypted.zip -d /tmp/finetuning-test/output/
  
  echo "LoRA files:"
  ls -la /tmp/finetuning-test/output/output_model/
fi
```

## Qwen3-32B Full Flow Test

Qwen3-32B is a large 32B parameter model that requires special handling:

### Requirements

- **GPU Memory**: H200 (141GB) or similar high-memory GPU
- **Disk Space**: ~70GB for model files
- **transformers**: >= 4.51.0 (for Qwen3 architecture support)
- **4-bit Quantization**: Automatically enabled for large models

### Model Setup on CVM

```bash
# Download Qwen3-32B to CVM (if not already present)
docker run --rm -v /dstack/persistent/models:/models python:3.11-slim bash -c "
pip install -q huggingface_hub
python3 -c \"
from huggingface_hub import snapshot_download
snapshot_download(
    repo_id='Qwen/Qwen3-32B',
    local_dir='/models/Qwen3-32B',
    local_dir_use_symlinks=False,
    resume_download=True
)
\"
"

# Verify model files (~61GB)
ls -lh /dstack/persistent/models/Qwen3-32B/
```

### Broker config.yaml

Ensure Qwen3-32B is configured in `modelLocalPaths`:

```yaml
service:
  modelLocalPaths:
    "0x2e6f9620c35bdcb2b753cc7aa34e78077a8ed133e36fa36008fd6bdfd29af3a5": "/dstack/persistent/models/Qwen3-32B"
  modelHuggingFaceFallback:
    "0x2e6f9620c35bdcb2b753cc7aa34e78077a8ed133e36fa36008fd6bdfd29af3a5": "Qwen/Qwen3-32B"
```

### Complete Test Script

```bash
#!/bin/bash
# Qwen3-32B Fine-tuning Full Flow Test

# Setup
export ZG_DEV_MODE=true
export ZEROG_PRIVATE_KEY="your_private_key"
PROVIDER="0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"

# Prepare test data
mkdir -p /tmp/qwen3-test
cat > /tmp/qwen3-test/dataset.jsonl << 'EOF'
{"instruction": "Translate to French", "input": "Hello world", "output": "Bonjour le monde"}
{"instruction": "Translate to French", "input": "Good morning", "output": "Bonjour"}
{"instruction": "Translate to French", "input": "Thank you", "output": "Merci"}
{"instruction": "Translate to French", "input": "How are you?", "output": "Comment allez-vous?"}
EOF

cat > /tmp/qwen3-test/config.json << 'EOF'
{
    "num_train_epochs": 1,
    "per_device_train_batch_size": 1,
    "learning_rate": 0.0001,
    "max_steps": 5,
    "max_seq_length": 128,
    "lora_r": 8,
    "lora_alpha": 16
}
EOF

# Step 1: Create Task
echo "=== Creating Qwen3-32B fine-tuning task ==="
RESULT=$(0g-compute-cli fine-tuning create-task \
  --provider $PROVIDER \
  --model Qwen3-32B \
  --dataset-path /tmp/qwen3-test/dataset.jsonl \
  --config-path /tmp/qwen3-test/config.json \
  --data-size 100 2>&1)
echo "$RESULT"
TASK_ID=$(echo "$RESULT" | grep "Created Task ID" | awk '{print $NF}')
echo "Task ID: $TASK_ID"

# Step 2: Monitor Progress (expect ~5-10 minutes)
echo "=== Monitoring task (Qwen3-32B takes longer due to model size) ==="
while true; do
  STATUS=$(0g-compute-cli fine-tuning get-task --provider $PROVIDER --task $TASK_ID 2>&1 | grep "Progress" | awk '{print $4}')
  echo "$(date +%H:%M:%S) - Status: $STATUS"
  if [ "$STATUS" = "Delivered" ] || [ "$STATUS" = "Failed" ]; then
    break
  fi
  sleep 30
done

# Step 3: Download and Acknowledge
if [ "$STATUS" = "Delivered" ]; then
  echo "=== Downloading model ==="
  0g-compute-cli fine-tuning acknowledge-model \
    --provider $PROVIDER \
    --task-id $TASK_ID \
    --data-path /tmp/qwen3-test/output
  
  # Wait for Finished status
  sleep 30
  
  # Step 4: Decrypt
  echo "=== Decrypting model ==="
  0g-compute-cli fine-tuning decrypt-model \
    --provider $PROVIDER \
    --task-id $TASK_ID \
    --encrypted-model /tmp/qwen3-test/output/lora_model_${TASK_ID}.zip \
    --output /tmp/qwen3-test/output/lora_decrypted.zip
  
  # Step 5: Extract
  echo "=== Extracting LoRA adapter ==="
  unzip -o /tmp/qwen3-test/output/lora_decrypted.zip -d /tmp/qwen3-test/output/
  
  # Verify output
  echo "=== Final Output ==="
  ls -lh /tmp/qwen3-test/output/output_model/
  cat /tmp/qwen3-test/output/output_model/adapter_config.json
fi
```

### Expected Output

```
output/output_model/
├── adapter_config.json          # LoRA configuration
├── adapter_model.safetensors    # LoRA weights (~257MB for 32B model)
├── tokenizer.json               # Tokenizer (~11MB)
├── tokenizer_config.json
├── chat_template.jinja
├── README.md
└── checkpoint-5/                # Training checkpoint
```

### Timing Reference (Qwen3-32B, 5 steps)

| Stage | Time |
|-------|------|
| Setup (model loading with 4-bit) | ~2 minutes |
| Training (5 steps) | ~2-3 minutes |
| Finalize (encryption) | ~1 minute |
| **Total** | **~5-6 minutes** |

### Qwen3-32B Troubleshooting

#### "Transformers does not recognize architecture qwen3"

**Cause:** transformers version < 4.51.0

**Solution:** Update Docker image with newer transformers:
```dockerfile
RUN pip install "transformers>=4.51.0" "accelerate>=0.30.0" "peft>=0.11.0"
```

#### "TypeError: Trainer.__init__() got an unexpected keyword argument 'tokenizer'"

**Cause:** transformers 5.x API change - `tokenizer` parameter deprecated

**Solution:** Use `data_collator` instead:
```python
# Before (deprecated)
trainer = Trainer(model=model, tokenizer=tokenizer, ...)

# After (transformers 5.x compatible)
data_collator = DataCollatorForLanguageModeling(tokenizer=tokenizer, mlm=False)
trainer = Trainer(model=model, data_collator=data_collator, ...)
```

#### Model loading crashes silently

**Cause:** OOM or 4-bit quantization not triggering

**Solution:** Ensure training script auto-detects large models:
```python
# Auto-enable 4-bit for large models (hidden_size * num_layers > 100000)
if hidden_size * num_layers > 100000:
    use_4bit = True
```

#### "Using a device_map requires accelerate"

**Cause:** accelerate version incompatible with transformers 5.x

**Solution:** Update accelerate >= 0.30.0

## Output Structure

After successful decryption:

```
output/
├── lora_model_<task-id>.zip      # Encrypted model from TEE
├── lora_decrypted.zip            # Decrypted zip
└── output_model/                 # Extracted LoRA adapter
    ├── adapter_config.json       # LoRA configuration
    ├── adapter_model.safetensors # LoRA weights (see sizes below)
    ├── tokenizer.json            # Tokenizer
    ├── tokenizer_config.json
    ├── chat_template.jinja       # Chat template (for Qwen models)
    ├── README.md
    └── checkpoint-N/             # Training checkpoint
        ├── adapter_model.safetensors
        ├── optimizer.pt
        ├── scheduler.pt
        └── trainer_state.json
```

### LoRA Adapter Sizes by Model

| Model | adapter_model.safetensors | Total Output |
|-------|--------------------------|--------------|
| Qwen2.5-0.5B-Instruct | ~8 MB | ~20 MB |
| Qwen3-32B | ~257 MB | ~670 MB |
