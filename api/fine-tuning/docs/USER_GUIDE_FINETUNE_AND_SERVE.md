# User Guide: Fine-Tune and Serve Models via CLI

> **CLI**: `0g-compute-cli` | **Network**: 0G Testnet (Chain ID 16602) | **Date**: 2026-03-12

Full workflow: fine-tune a model → download result → send inference requests to your fine-tuned model.

---

## Workflow Overview

```
  FINE-TUNING                                  INFERENCE
  ──────────                                   ─────────
  deposit                                      transfer-fund (inference)
     ↓                                              ↓
  transfer-fund (fine-tuning)                  acknowledge-provider
     ↓                                              ↓
  upload dataset                               get-secret (API Key)
     ↓                                              ↓
  create-task                                  chat / curl
     ↓
  monitor (get-task / get-log)
     ↓
  acknowledge-model  ──── on-chain event ────→  adapter auto-deployed
     ↓
  decrypt-model (optional, for self-hosting)
```

---

## Prerequisites

```bash
# Install
npm install -g 0g-compute-cli

# Configure (interactive)
0g-compute-cli setup
```

---

## Step 1: Account Setup

```bash
# Find providers
0g-compute-cli fine-tuning list-providers
0g-compute-cli fine-tuning list-models

# Deposit funds to main account
0g-compute-cli deposit --amount 5

# Transfer to fine-tuning sub-account
0g-compute-cli transfer-fund \
  --provider <PROVIDER> --amount 2 --service fine-tuning
```

---

## Step 2: Upload Dataset

Prepare JSONL (one JSON per line, chat format):

```json
{"messages": [{"role": "system", "content": "You are a helpful assistant."}, {"role": "user", "content": "What is 0G?"}, {"role": "assistant", "content": "0G is a decentralized AI infrastructure."}]}
```

```bash
0g-compute-cli fine-tuning upload --data-path ./my_dataset.jsonl
# Output: Root hash: 0x128ebb...
```

Save the **root hash**.

---

## Step 3: Create Fine-Tuning Task

Prepare `config.json`:

```json
{
  "neftune_noise_alpha": 5,
  "num_train_epochs": 1,
  "per_device_train_batch_size": 2,
  "learning_rate": 0.0002,
  "max_steps": 100
}
```

> Use `0.0002` not `2e-4` for learning_rate.

**Option A** — with pre-uploaded dataset hash:

```bash
0g-compute-cli fine-tuning create-task \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --dataset <ROOT_HASH> \
  --config-path ./config.json
```

**Option B** — upload + create in one step:

```bash
0g-compute-cli fine-tuning create-task \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --dataset-path ./my_dataset.jsonl \
  --config-path ./config.json
```

> Use model names **without** the `Qwen/` prefix.

Save the output **Task ID** (e.g., `1f6fef2b-e0f6-4a28-9ebe-86d5370b1934`).

---

## Step 4: Monitor Progress

```bash
0g-compute-cli fine-tuning get-task --provider <PROVIDER> --task <TASK_ID>
0g-compute-cli fine-tuning get-log  --provider <PROVIDER> --task <TASK_ID>
```

States: `Init → SetUp → Training → Delivering → Delivered → Finished`

Wait for **Delivered** before proceeding.

---

## Step 5: Download and Acknowledge Model

```bash
0g-compute-cli fine-tuning acknowledge-model \
  --provider <PROVIDER> \
  --task-id <TASK_ID> \
  --data-path ./output/
```

Downloads encrypted model from 0G Storage and acknowledges on-chain.

---

## Step 6: Decrypt Model (Optional)

For local self-hosting:

```bash
0g-compute-cli fine-tuning decrypt-model \
  --provider <PROVIDER> \
  --task-id <TASK_ID> \
  --encrypted-model ./output/model_<TASK_ID>.bin \
  --output ./decrypted_model/
```

Output: ZIP with LoRA adapter files (`adapter_model.safetensors`, `adapter_config.json`).

---

## Step 7: Use Fine-Tuned Model for Inference

After acknowledging (Step 5), the provider's inference broker auto-deploys your LoRA adapter.

### 7.1 Prepare Inference Account

```bash
# Transfer funds for inference
0g-compute-cli transfer-fund \
  --provider <PROVIDER> --amount 1 --service inference

# Acknowledge inference provider
0g-compute-cli inference acknowledge-provider --provider <PROVIDER>
```

### 7.2 Chat Directly (Easiest)

```bash
0g-compute-cli fine-tuning chat \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID> \
  --message "Hello! Tell me about 0G."
```

Output:
```
Adapter model name: ft-Qwen2-5-0-5B-Instruct-1f6fef2b-e0f6
Assistant: Hi! 0G is a modular, infinitely scalable data availability layer...

Tokens: 12 prompt + 25 completion = 37 total
```

You can also specify a custom system prompt:

```bash
0g-compute-cli fine-tuning chat \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID> \
  --system "You are a blockchain expert." \
  --message "Explain consensus mechanisms."
```

### 7.3 Use API Key with curl (Advanced)

Generate a persistent API key:

```bash
0g-compute-cli inference get-secret \
  --provider <PROVIDER> \
  --duration 0
```

Use the returned Bearer token with curl:

```bash
PROVIDER_URL="https://provider-endpoint.example.com"
ADAPTER="ft-Qwen2-5-0-5B-Instruct-1f6fef2b-e0f6"

curl $PROVIDER_URL/v1/proxy/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <YOUR_API_KEY>" \
  -d '{
    "model": "'$ADAPTER'",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

### 7.4 Get Adapter Name (Reference)

```bash
0g-compute-cli fine-tuning get-adapter-name \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID>
# Output: ft-Qwen2-5-0-5B-Instruct-1f6fef2b-e0f6
```

---

## Command Reference

### Fine-Tuning Commands

| Command | Description |
|---------|-------------|
| `fine-tuning list-providers` | List available fine-tuning providers |
| `fine-tuning list-models` | List available base models |
| `fine-tuning upload --data-path <path>` | Upload dataset to 0G Storage |
| `fine-tuning create-task` | Create a fine-tuning task |
| `fine-tuning get-task --provider <addr> [--task <id>]` | Check task status |
| `fine-tuning get-log --provider <addr> [--task <id>]` | View training logs |
| `fine-tuning list-tasks --provider <addr>` | List all tasks |
| `fine-tuning acknowledge-model` | Download and acknowledge model |
| `fine-tuning decrypt-model` | Decrypt model for local use |
| `fine-tuning cancel-task` | Cancel a running task |
| `fine-tuning get-adapter-name` | Get LoRA adapter name for inference |
| `fine-tuning chat` | Chat with fine-tuned model |

### Inference Commands

| Command | Description |
|---------|-------------|
| `inference list-providers` | List inference providers |
| `inference acknowledge-provider --provider <addr>` | Acknowledge provider signer |
| `inference get-secret --provider <addr>` | Generate API key |
| `inference revoke-token --provider <addr> --token-id <id>` | Revoke an API key |

### Account Commands

| Command | Description |
|---------|-------------|
| `get-account` | View account balance |
| `add-account --amount <0G>` | Create account with initial deposit |
| `deposit --amount <0G>` | Deposit to existing account |
| `transfer-fund --provider <addr> --amount <0G> --service <type>` | Transfer to sub-account |
| `refund --amount <0G>` | Withdraw from main account |

---

## Troubleshooting

### "Insufficient balance" when creating task

Deposit more funds and transfer to the fine-tuning sub-account:

```bash
0g-compute-cli deposit --amount 5
0g-compute-cli transfer-fund --provider <PROVIDER> --amount 3 --service fine-tuning
```

### Task stuck at "SetUp"

The provider is downloading the dataset and model. Large models (e.g., Qwen3-32B) may take several minutes. Check logs:

```bash
0g-compute-cli fine-tuning get-log --provider <PROVIDER> --task <TASK_ID>
```

### "Model not found" when using chat

The inference broker may not have deployed the adapter yet. This happens automatically after `acknowledge-model`, but may take up to 30 seconds. Wait and retry.

### "Service not acknowledge the tee signer"

Run the acknowledge command:

```bash
0g-compute-cli inference acknowledge-provider --provider <PROVIDER>
```

### API Key expired or invalid

Generate a new one:

```bash
0g-compute-cli inference get-secret --provider <PROVIDER> --duration 0
```

Or revoke all and regenerate:

```bash
0g-compute-cli inference revoke-all-tokens --provider <PROVIDER>
0g-compute-cli inference get-secret --provider <PROVIDER> --duration 0
```

---

## Complete Example

```bash
# 1. Setup
0g-compute-cli deposit --amount 5
0g-compute-cli transfer-fund --provider 0x87a1...8ed4 --amount 2 --service fine-tuning
0g-compute-cli transfer-fund --provider 0x87a1...8ed4 --amount 2 --service inference

# 2. Fine-tune
0g-compute-cli fine-tuning create-task \
  --provider 0x87a1...8ed4 \
  --model "Qwen2.5-0.5B-Instruct" \
  --dataset-path ./data.jsonl \
  --config-path ./config.json
# => Task ID: abc123-def456

# 3. Wait for completion, then download
0g-compute-cli fine-tuning get-task --provider 0x87a1...8ed4 --task abc123-def456
0g-compute-cli fine-tuning acknowledge-model \
  --provider 0x87a1...8ed4 --task-id abc123-def456 --data-path ./output/

# 4. Use for inference
0g-compute-cli inference acknowledge-provider --provider 0x87a1...8ed4
0g-compute-cli fine-tuning chat \
  --provider 0x87a1...8ed4 \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id abc123-def456 \
  --message "Hello from my fine-tuned model!"
```
