# Fine-Tuning Full Flow Testing Guide

This guide documents the complete end-to-end testing flow for the 0G Fine-Tuning service.

## Prerequisites

1. **CVM Environment**
   - Broker service running in Docker
   - Pre-trained model available (local path or HuggingFace fallback configured)
   - `qwen-lora:v3` Docker image for training execution

2. **Client Environment**
   - Node.js with ethers.js installed
   - Private key with sufficient balance for gas fees
   - Network access to CVM broker endpoint

3. **Contract Setup**
   - Fine-tuning contract deployed
   - Provider registered on the contract
   - User account created with the provider

## Test Flow Overview

```
User                          Broker (TEE)                    Contract
  │                              │                               │
  │──── 1. Upload Dataset ──────▶│                               │
  │◀─── Dataset Hash ───────────│                               │
  │                              │                               │
  │──── 2. Create Task ─────────▶│                               │
  │◀─── Task ID ────────────────│                               │
  │                              │                               │
  │                              │── Setup (load model/data) ───▶│
  │                              │── Training (LoRA) ───────────▶│
  │                              │── Encrypt LoRA ──────────────▶│
  │                              │── 3. Add Deliverable ────────▶│
  │                              │                               │
  │──── 4. Download LoRA ───────▶│                               │
  │◀─── Encrypted LoRA ─────────│                               │
  │                              │                               │
  │──── 5. Acknowledge ─────────────────────────────────────────▶│
  │                              │                               │
```

## Step-by-Step Testing

### Step 1: Prepare Test Dataset

Create a JSONL file with instruction-input-output format:

```json
{"instruction": "Translate to French", "input": "Hello world", "output": "Bonjour le monde"}
{"instruction": "Translate to French", "input": "Good morning", "output": "Bonjour"}
{"instruction": "Translate to French", "input": "Thank you", "output": "Merci"}
```

### Step 2: Upload Dataset

```javascript
// Calculate dataset hash
const datasetContent = fs.readFileSync('dataset.jsonl');
const datasetHash = ethers.keccak256(datasetContent);

// Upload to broker
const response = await fetch(`${BROKER_URL}/v1/user/${userAddress}/dataset`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream' },
    body: datasetContent
});

const result = await response.json();
console.log('Dataset hash:', result.hash);
```

**Expected Response:**
```json
{
  "hash": "0x88f241037ce5c842906598cd43bb42e27e21f6f755ea7ea71e8e62ff3a2c7a2e",
  "size": 515
}
```

### Step 3: Create Fine-Tuning Task

```javascript
// Task parameters
const taskRequest = {
    preTrainedModelHash: MODEL_HASH,  // e.g., Qwen2.5-0.5B hash
    datasetHash: datasetHash,
    trainingParams: JSON.stringify({
        num_train_epochs: 1,
        per_device_train_batch_size: 1,
        learning_rate: 0.0001,
        max_steps: 5,
        max_seq_length: 128,
        lora_r: 8,
        lora_alpha: 16
    }),
    fee: ethers.parseEther("1").toString(),
    nonce: Date.now().toString()
};

// Sign the request (must match broker's verification logic)
const message = ethers.concat([
    ethers.zeroPadValue(userAddress, 20),
    ethers.zeroPadValue(ethers.toBeHex(BigInt(taskRequest.nonce)), 32),
    ethers.toUtf8Bytes(taskRequest.datasetHash),
    ethers.zeroPadValue(ethers.toBeHex(BigInt(taskRequest.fee)), 32)
]);
const messageHash = ethers.keccak256(message);
const signature = await wallet.signMessage(ethers.getBytes(messageHash));

taskRequest.signature = signature;

// Create task
const response = await fetch(`${BROKER_URL}/v1/user/${userAddress}/task`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(taskRequest)
});

const task = await response.json();
console.log('Task ID:', task.id);
```

**Expected Response:**
```json
{
  "id": "9d04f438-b672-4bc9-b506-95f66b8cfa19"
}
```

### Step 4: Monitor Task Progress

```javascript
async function pollTaskStatus(taskId) {
    while (true) {
        const response = await fetch(
            `${BROKER_URL}/v1/user/${userAddress}/task/${taskId}`
        );
        const task = await response.json();
        
        console.log(`Status: ${task.progress}`);
        
        if (task.progress === 'Delivered') {
            return task;
        }
        if (task.progress === 'Failed') {
            throw new Error('Task failed');
        }
        
        await sleep(30000); // Poll every 30 seconds
    }
}
```

**Progress States:**
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

### Step 5: Download Encrypted LoRA

```javascript
const response = await fetch(
    `${BROKER_URL}/v1/user/${userAddress}/task/${taskId}/lora`
);

if (response.ok) {
    const loraData = await response.arrayBuffer();
    fs.writeFileSync('lora_encrypted.data', Buffer.from(loraData));
    console.log(`Downloaded: ${loraData.byteLength} bytes`);
}
```

**Expected:** Binary file ~50-100MB depending on model size.

### Step 6: Acknowledge Deliverable (Contract Call)

```javascript
const FINE_TUNING_ABI = [
    "function acknowledgeDeliverable(address provider, string id) external"
];

const contract = new ethers.Contract(FINE_TUNING_CA, FINE_TUNING_ABI, wallet);
const tx = await contract.acknowledgeDeliverable(PROVIDER_ADDRESS, taskId);
await tx.wait();

console.log('Deliverable acknowledged');
```

**Important:** You must acknowledge the current deliverable before creating a new task. The contract enforces this to ensure users confirm receipt of each delivery.

## Model Hashes

| Model | Hash |
|-------|------|
| Qwen2.5-0.5B-Instruct | `0xb4f76a886b8655c92bb021922d60b5e4d9271a5c9da98b6cb10937a06c2c75a7` |
| Qwen3-32B | `0x2e6f9620c35bdcb2b753cc7aa34e78077a8ed133e36fa36008fd6bdfd29af3a5` |

## Timing Reference

For Qwen2.5-0.5B-Instruct with 5 training steps:
- Setup: ~1 minute
- Training: ~30 seconds
- Encryption & Contract: ~30 seconds
- **Total: ~2-3 minutes**

For Qwen3-32B:
- Setup: ~5-10 minutes (model loading)
- Training: ~10-20 minutes
- **Total: ~15-30 minutes**

## Troubleshooting

### "previous deliverable not acknowledged"

**Cause:** A previous task's deliverable exists but hasn't been acknowledged.

**Solution:** Call `acknowledgeDeliverable()` for the previous task before creating a new one.

```javascript
// Check existing deliverables
const deliverables = await contract.getDeliverables(userAddress, providerAddress);
for (const d of deliverables) {
    if (!d.acknowledged) {
        await contract.acknowledgeDeliverable(providerAddress, d.id);
    }
}
```

### "signature verification failed"

**Cause:** Signature format doesn't match broker's expected format.

**Solution:** Ensure you're using the correct signing method:
1. Concatenate: address (20 bytes) + nonce (32 bytes) + datasetHash (UTF8) + fee (32 bytes)
2. Hash with keccak256
3. Sign the hash bytes with `signMessage()`

### Task stuck in "SettingUp"

**Cause:** Model or dataset loading issues.

**Check:**
- Model exists at configured local path or HuggingFace fallback is correct
- Dataset was successfully converted to HuggingFace format
- Sufficient disk space on CVM

## Contract ABI Reference

```solidity
// Provider calls
function addDeliverable(address user, string id, bytes modelRootHash) external;

// User calls
function acknowledgeDeliverable(address provider, string id) external;

// View functions
function getDeliverable(address user, address provider, string id) 
    external view returns (Deliverable);
function getDeliverables(address user, address provider) 
    external view returns (Deliverable[]);
```

## Test Success Criteria

- [ ] Dataset uploaded and hash returned
- [ ] Task created successfully
- [ ] Task progresses through all states to `Delivered`
- [ ] Encrypted LoRA downloaded (non-empty file)
- [ ] Deliverable acknowledged on contract
- [ ] Task status reaches `Finished`
