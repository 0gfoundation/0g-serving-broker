# Fine-Tuning Service Testing Guide

This guide describes how to test the 0G Fine-Tuning service end-to-end.

## Overview

The fine-tuning service allows users to:
1. Upload datasets to TEE
2. Create fine-tuning tasks using pre-trained models
3. Download encrypted LoRA adapters
4. Decrypt using keys obtained through contract settlement

## Prerequisites

1. **CVM Access**: SSH access to the Phala TEE CVM
2. **Broker Binary**: Built broker binary for Linux AMD64
3. **Wallet**: A funded wallet with A0GI tokens
4. **Client SDK**: The `0g-serving-user-broker` package

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/user/:address/dataset` | POST | Upload dataset to TEE |
| `/v1/user/:address/task` | POST | Create fine-tuning task |
| `/v1/user/:address/task/:id` | GET | Get task status |
| `/v1/user/:address/task/:id/lora` | GET | Download encrypted LoRA |
| `/v1/task/pending` | GET | Get pending task count |

## Configuration

### Broker Configuration (`config.yaml`)

```yaml
service:
  # Skip 0G Storage upload - encrypt locally for TEE download
  skipStorageUpload: true
  
  # File retention (hours) - how long to keep files before cleanup
  fileRetentionHours: 72
  
  # Local model paths - maps model hash to local file path
  modelLocalPaths:
    "0x<model_hash>": "/path/to/model"
  
  # HuggingFace fallback - used when local model doesn't exist
  modelHuggingFaceFallback:
    "0x<model_hash>": "Qwen/Qwen2.5-0.5B-Instruct"
  
  # Local dataset paths (for pre-configured datasets)
  datasetLocalPaths:
    "0x<dataset_hash>": "/path/to/dataset"
```

### Data Loading Priority

**Model Loading:**
1. Local path from `modelLocalPaths`
2. HuggingFace download from `modelHuggingFaceFallback`
3. 0G Storage download

**Dataset Loading:**
1. Config `datasetLocalPaths`
2. User-uploaded dataset (via `/v1/user/:address/dataset`)
3. 0G Storage download

## Complete Flow

### Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Fine-Tuning Complete Flow                        │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  1. User uploads dataset                                            │
│     POST /v1/user/:address/dataset                                  │
│     Returns: datasetHash                                            │
│       ↓                                                             │
│  2. User creates task                                               │
│     POST /v1/user/:address/task                                     │
│     Body: { preTrainedModelHash, datasetHash, trainingParams, ... } │
│       ↓                                                             │
│  3. Broker executes training                                        │
│     Status: Init → SetUp → Training → Trained                       │
│       ↓                                                             │
│  4. Broker encrypts LoRA with AES key                               │
│     - Encrypts user's AES key with user's public key                │
│     - Calls contract.AddDeliverable()                               │
│     Status: Delivered                                               │
│       ↓                                                             │
│  5. User downloads encrypted LoRA                                   │
│     GET /v1/user/:address/task/:id/lora                             │
│       ↓                                                             │
│  6. User calls contract.acknowledge()                               │
│     - Pays for the task                                             │
│     - Gets encryptedSecret from contract                            │
│       ↓                                                             │
│  7. User decrypts                                                   │
│     - Decrypt encryptedSecret with private key → AES key            │
│     - Decrypt LoRA file with AES key                                │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

## Step-by-Step Testing

### Step 1: Upload Dataset

```bash
curl -X POST \
  -F "file=@/path/to/dataset.jsonl" \
  "http://<BROKER_URL>/v1/user/<USER_ADDRESS>/dataset"
```

Response:
```json
{
  "datasetHash": "0x1234...",
  "message": "Dataset uploaded successfully..."
}
```

### Step 2: Create Fine-Tuning Task

```javascript
const taskRequest = {
  userAddress: wallet.address,
  preTrainedModelHash: MODEL_HASH,
  datasetHash: DATASET_HASH,
  trainingParams: JSON.stringify({
    num_train_epochs: 1,
    per_device_train_batch_size: 1,
    learning_rate: 0.0001,
    max_steps: 10,
    lora_r: 8,
    lora_alpha: 16,
  }),
  fee: "1000000000000000000",
  nonce: Date.now().toString(),
  signature: signature,
  userPublicKey: wallet.signingKey.publicKey,
}
```

### Step 3: Monitor Task Progress

```bash
curl "http://<BROKER_URL>/v1/user/<USER_ADDRESS>/task/<TASK_ID>"
```

Expected progress flow:
```
Init → SettingUp → SetUp → Training → Trained → Delivering → Delivered
```

### Step 4: Download Encrypted LoRA

```bash
curl -o lora_encrypted.data \
  "http://<BROKER_URL>/v1/user/<USER_ADDRESS>/task/<TASK_ID>/lora"
```

**Important**: The downloaded file is AES encrypted. You need to:
1. Call `acknowledge()` on the contract
2. Get `encryptedSecret` from the contract
3. Decrypt with your private key to get AES key
4. Decrypt the LoRA file

### Step 5: Decrypt LoRA (Client-Side)

```javascript
// After acknowledge(), get encryptedSecret from contract
const encryptedSecret = await contract.getDeliverable(taskId)

// Decrypt with user's private key (ECIES)
const aesKey = ecies.decrypt(userPrivateKey, encryptedSecret)

// Decrypt the LoRA file with AES
const decryptedLoRA = aesDecrypt(aesKey, encryptedLoraData)
```

## Base Model Download

After fine-tuning, users need the base model to use the LoRA adapter:

**Option 1: Download from 0G Storage**
```bash
# Use 0g-storage-client
0g-storage-client download --root <model_root_hash>
```

**Option 2: Download from HuggingFace**
```bash
# For Qwen models
huggingface-cli download Qwen/Qwen2.5-0.5B-Instruct
huggingface-cli download Qwen/Qwen3-32B
```

## File Cleanup

Files are automatically cleaned up based on `fileRetentionHours` config:
- Task directories (dataset, model, output)
- Encrypted LoRA files (.data)
- Temporary zip files

Default retention: 72 hours (3 days)

## Troubleshooting

### Task stuck at `Init`
- Check broker logs: `docker logs broker`
- Verify dataset exists or can be downloaded

### Task stuck at `SetUp`
- Model download may be in progress (large models take time)
- Check HuggingFace fallback configuration

### Task stuck at `Training`
- Check training container: `docker logs <container_id>`
- Verify GPU memory is sufficient

### Download returns 404
- Task may not be in `Delivered` status
- Encryption may not have completed

### Decryption fails
- Ensure you've called `acknowledge()` first
- Verify the encryptedSecret from contract

## Example Test Script

```bash
#!/bin/bash

BROKER_URL="http://localhost:8080"
USER_ADDRESS="0x1234..."
MODEL_HASH="0xb4f76a886b8655c92bb021922d60b5e4d9271a5c9da98b6cb10937a06c2c75a7"

# 1. Upload dataset
echo "Uploading dataset..."
RESPONSE=$(curl -s -X POST -F "file=@test_data.jsonl" \
  "$BROKER_URL/v1/user/$USER_ADDRESS/dataset")
DATASET_HASH=$(echo $RESPONSE | jq -r '.datasetHash')
echo "Dataset hash: $DATASET_HASH"

# 2. Create task
echo "Creating task..."
# (Use your client SDK to create task with proper signature)

# 3. Monitor progress
echo "Monitoring..."
while true; do
  STATUS=$(curl -s "$BROKER_URL/v1/user/$USER_ADDRESS/task/$TASK_ID" | jq -r '.progress')
  echo "Status: $STATUS"
  if [ "$STATUS" = "Delivered" ]; then
    break
  fi
  sleep 10
done

# 4. Download encrypted LoRA
echo "Downloading..."
curl -o lora_encrypted.data "$BROKER_URL/v1/user/$USER_ADDRESS/task/$TASK_ID/lora"
echo "Done! File: lora_encrypted.data"
```
