# Fine-Tuning Service Testing Guide

This guide describes how to test the 0G Fine-Tuning service end-to-end.

## Prerequisites

1. **CVM Access**: SSH access to the Phala TEE CVM
2. **Broker Binary**: Built broker binary for Linux AMD64
3. **Wallet**: A funded wallet with A0GI tokens on testnetDev
4. **Client SDK**: The `0g-serving-user-broker` package

## Configuration

### Broker Configuration (`config.yaml`)

```yaml
service:
  # Skip 0G Storage upload - users download LoRA directly from TEE
  # Useful for testing or when 0G Storage is unavailable
  skipStorageUpload: true
  
  # Local model paths - maps model hash to local file path
  # When set, broker uses local model instead of downloading from 0G Storage
  modelLocalPaths:
    "0x<model_hash>": "/path/to/model"
  
  # HuggingFace fallback - maps model hash to HuggingFace repo name
  # Used as fallback when local model path doesn't exist
  modelHuggingFaceFallback:
    "0x<model_hash>": "Qwen/Qwen2.5-0.5B-Instruct"
  
  # Local dataset paths - maps dataset hash to local path
  # Useful for testing with pre-prepared datasets
  datasetLocalPaths:
    "0x<dataset_hash>": "/path/to/dataset"
```

### Model/Dataset Fallback Chain

**Model Loading Priority:**
1. Local path from `modelLocalPaths` (symlink + bind mount)
2. HuggingFace download from `modelHuggingFaceFallback`
3. 0G Storage download

**Dataset Loading Priority:**
1. Local path from `datasetLocalPaths` (symlink + bind mount)
2. 0G Storage download

## Step-by-Step Testing

### Step 1: Prepare Test Dataset on CVM

Create a HuggingFace-format dataset on the CVM:

```bash
docker run --rm -v /tmp:/tmp qwen-lora:v3 python3 << 'EOF'
from datasets import Dataset, DatasetDict

data = {
    "instruction": [
        "What is machine learning?",
        "Explain neural networks.",
        "What is deep learning?",
        "Describe reinforcement learning.",
        "What is natural language processing?",
    ],
    "input": ["", "", "", "", ""],
    "output": [
        "Machine learning is a subset of artificial intelligence that enables systems to learn from data.",
        "Neural networks are computing systems inspired by biological neural networks in the brain.",
        "Deep learning is a type of machine learning using multi-layered neural networks.",
        "Reinforcement learning is a type of ML where agents learn by interacting with environments.",
        "NLP is a field of AI focused on interaction between computers and human language.",
    ]
}

ds = DatasetDict({"train": Dataset.from_dict(data)})
ds.save_to_disk("/tmp/test_dataset_hf")
print("Dataset created at /tmp/test_dataset_hf")
EOF
```

### Step 2: Configure the Broker

Create `/tmp/config-testnetdev.yaml` on the CVM:

```yaml
database:
  connectionString: "file:/tmp/fine-tuning.db"

blockchain:
  rpcUrl: "https://evmrpc-testnet.0g.ai"
  chainId: 16601
  signerKey: "${SIGNER_PRIVATE_KEY}"
  fineTuning: "0xae1D93dd9Ccc5FD7ed16785F5D92f7cDbFCbb45f"
  ledger: "0x4BC4A34c2D77fcb54c5D20CF50F9CB03C0Cd63D6"

service:
  providerAddress: "${PROVIDER_ADDRESS}"
  preTrainedModels:
    - name: "Qwen2.5-0.5B-Instruct"
      hash: "0x1234567890..."  # Replace with actual hash
  skipStorageUpload: true
  modelLocalPaths:
    "0x1234567890...": "/dstack/persistent/models/Qwen2.5-0.5B-Instruct"
  modelHuggingFaceFallback:
    "0x1234567890...": "Qwen/Qwen2.5-0.5B-Instruct"
  datasetLocalPaths:
    "0x0000000000000000000000000000000000000000000000000000000000000001": "/tmp/test_dataset_hf"
```

### Step 3: Start the Broker

```bash
docker run -d \
  --name broker \
  --network provider_default \
  --runtime=nvidia \
  --gpus all \
  -p 80:8080 \
  -v /path/to/broker:/usr/bin/broker \
  -v /tmp/config-testnetdev.yaml:/etc/config/config.yaml \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/run/dstack.sock:/var/run/dstack.sock \
  -v /var/run/tappd.sock:/var/run/tappd.sock \
  -v /tmp:/tmp \
  -v /dstack:/dstack \
  ghcr.io/0gfoundation/0g-serving-broker:dev-amd64 \
  0g-fine-tuning-server
```

### Step 4: Verify Broker is Running

```bash
# Check container status
docker ps | grep broker

# Check logs
docker logs broker

# Test API endpoint
curl http://localhost/v1/task/pending
```

### Step 5: Run Test Client

On your local machine:

```bash
cd 0g-serving-user-broker
node test-full-flow.mjs
```

## Expected Task Flow

| Step | Status | Description | Duration |
|------|--------|-------------|----------|
| 1 | `Init` | Task created, broker initializing | ~10s |
| 2 | `SetUp` | Loading model and dataset | ~30-60s |
| 3 | `Training` | LoRA training in progress | ~30-60s |
| 4 | `Trained` | Training complete | instant |
| 5 | `Delivering` | Finalizing (skip if `skipStorageUpload=true`) | instant |
| 6 | `Delivered` | Task finished | - |

## Downloading LoRA from TEE

After task completion, download the trained LoRA adapter directly from TEE:

```bash
# Using curl
curl -o lora.zip \
  "http://<BROKER_URL>/v1/user/<USER_ADDRESS>/task/<TASK_ID>/lora"

# Verify contents
unzip -l lora.zip
```

The downloaded `lora.zip` contains:
- `adapter_config.json` - LoRA configuration
- `adapter_model.safetensors` - Trained weights (~34MB for Qwen2.5-0.5B)

## Troubleshooting

### Task stuck at `Init`

Check broker logs for dataset/model path errors:

```bash
docker logs broker 2>&1 | grep -i error
```

Common causes:
- Dataset path doesn't exist
- Dataset format is incorrect (must be HuggingFace DatasetDict format)

### Task stuck at `SetUp`

Check if model is loading correctly:

```bash
docker logs broker 2>&1 | grep -i model
```

Common causes:
- Model path doesn't exist and no HuggingFace fallback configured
- HuggingFace download failed (network issues)

### Task stuck at `Training`

Check the training container logs:

```bash
# Find training container
docker ps -a | grep qwen-lora

# Check logs
docker logs <container_id>
```

Common causes:
- GPU out of memory
- Training script errors

### Empty LoRA zip

Check training output directory:

```bash
ls -la /tmp/<task_id>/output/
```

Common causes:
- Training failed before saving checkpoint
- Wrong output path configuration

## Test Summary

A successful test produces output like:

```
============================================================
0G Fine-Tuning Full Flow Test
============================================================

1. Setting up wallet...
   Wallet: 0x1F0E...
   Balance: 2609.70 A0GI

2. Checking broker status...
   Pending tasks: 0

3. Creating fine-tuning task...
   Task created! ID: 68ec47d2-...

4. Monitoring task progress...
   Progress: Init → SetUp → Training → Trained → Delivered
   Task completed successfully!

5. Testing LoRA download from TEE...
   LoRA model downloaded: /tmp/lora_model_68ec47d2-....zip
   Size: 34374.84 KB

   SUCCESS! Full flow test completed!
============================================================
```
