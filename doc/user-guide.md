# 0G Compute Network - User Guide

This guide covers how to fine-tune models, deploy LoRA adapters, and run inference on the 0G Compute Network.

## Prerequisites

- **Node.js** v18+
- **0G Compute CLI** installed globally:
  ```bash
  npm install -g @0glabs/0g-serving-broker
  ```
- A wallet with A0GI tokens on the 0G network

## Quick Start

### 1. Login

```bash
0g-compute-cli login
```

The CLI will interactively prompt for your private key. Verify your login status:

```bash
0g-compute-cli status
```

### 2. Configure Network

```bash
0g-compute-cli setup-network
```

Select testnet or mainnet. You can check the current network with:

```bash
0g-compute-cli show-network
```

If you are using the **dev testnet** environment, enable dev mode so the CLI connects to the correct contracts:

```bash
export ZG_DEV_MODE=true
```

When dev mode is active, the CLI prints `[DEV MODE]` in its output to confirm.

### 3. Deposit Funds

Deposit A0GI into your main account:

```bash
0g-compute-cli deposit --amount 5
```

Transfer funds to a provider sub-account for fine-tuning:

```bash
0g-compute-cli transfer-fund \
  --provider <PROVIDER_ADDRESS> \
  --service fine-tuning \
  --amount 2
```

For inference:

```bash
0g-compute-cli transfer-fund \
  --provider <PROVIDER_ADDRESS> \
  --service inference \
  --amount 2
```

Check your account balance:

```bash
0g-compute-cli get-account
```

## Fine-Tuning

### Step 1: Find a Provider

```bash
0g-compute-cli fine-tuning list-providers
```

This shows available providers, their pricing, and availability status.

### Step 2: Verify Provider TEE

Before submitting tasks, verify the provider's TEE attestation:

```bash
0g-compute-cli fine-tuning verify --provider <PROVIDER_ADDRESS>
```

### Step 3: Prepare Dataset

Create a JSONL file in the chat format:

```json
{"messages": [{"role": "system", "content": "You are a helpful assistant."}, {"role": "user", "content": "What is Python?"}, {"role": "assistant", "content": "Python is a programming language."}]}
{"messages": [{"role": "system", "content": "You are a helpful assistant."}, {"role": "user", "content": "What is Go?"}, {"role": "assistant", "content": "Go is a statically typed, compiled language designed at Google."}]}
```

Each line must be a valid JSON object with a `messages` array following the chat format (system/user/assistant roles).

### Step 4: Create Training Config

Create a JSON file with training hyperparameters:

```json
{
  "neftune_noise_alpha": 5,
  "num_train_epochs": 1,
  "per_device_train_batch_size": 2,
  "learning_rate": 0.0002,
  "max_steps": 3
}
```

Common parameters:

| Parameter | Description | Default |
|---|---|---|
| `num_train_epochs` | Number of training epochs | 1 |
| `per_device_train_batch_size` | Batch size per device | 2 |
| `learning_rate` | Learning rate | 0.0002 |
| `max_steps` | Max training steps (overrides epochs if set) | - |
| `neftune_noise_alpha` | NEFTune noise for regularization (required) | 5 |

> **Note**: Use decimal notation for learning_rate (e.g., `0.0002`), not scientific notation (`2e-4`).

### Step 5: Create Task

**Option A**: Upload dataset to 0G Storage first (recommended):

```bash
# Upload dataset
0g-compute-cli fine-tuning upload --data-path ./dataset.jsonl

# Create task with the returned hash
0g-compute-cli fine-tuning create-task \
  --provider <PROVIDER_ADDRESS> \
  --model "Qwen2.5-0.5B-Instruct" \
  --dataset <DATASET_ROOT_HASH> \
  --config-path ./config.json
```

**Option B**: Upload dataset and create task in one step:

```bash
0g-compute-cli fine-tuning create-task \
  --provider <PROVIDER_ADDRESS> \
  --model "Qwen2.5-0.5B-Instruct" \
  --dataset-path ./dataset.jsonl \
  --config-path ./config.json
```

The CLI will output a **Task ID** — save it for subsequent steps.

> The fee is calculated automatically by the broker based on actual token count.

### Step 6: Monitor Progress

```bash
0g-compute-cli fine-tuning get-task \
  --provider <PROVIDER_ADDRESS> \
  --task <TASK_ID>
```

Task lifecycle:

```
Init → SetUp → Training → Delivering → Delivered → Finished
```

| Status | Description |
|---|---|
| `Init` | Task created, broker is downloading dataset |
| `SetUp` | Dataset processed, environment ready |
| `Training` | GPU training in progress |
| `Delivering` | Encrypting and uploading model to 0G Storage |
| `Delivered` | Model ready for download |
| `Finished` | Settlement complete |

You can also check training logs:

```bash
0g-compute-cli fine-tuning get-log \
  --provider <PROVIDER_ADDRESS> \
  --task <TASK_ID>
```

### Step 7: Download and Acknowledge Model

Once status is `Delivered`, download the model and acknowledge on-chain:

```bash
0g-compute-cli fine-tuning acknowledge-model \
  --provider <PROVIDER_ADDRESS> \
  --task-id <TASK_ID> \
  --data-path ./output \
  --deploy \
  --model "Qwen2.5-0.5B-Instruct"
```

The `--deploy` flag also deploys the LoRA adapter to the inference GPU. The CLI will wait until the adapter is ready.

If the inference provider is different from the fine-tuning provider, specify it separately:

```bash
0g-compute-cli fine-tuning acknowledge-model \
  --provider <FT_PROVIDER_ADDRESS> \
  --inference-provider <INF_PROVIDER_ADDRESS> \
  --task-id <TASK_ID> \
  --data-path ./output \
  --deploy \
  --model "Qwen2.5-0.5B-Instruct"
```

### Step 8: Decrypt Model (Optional)

If you want to keep a local copy of the LoRA adapter:

```bash
0g-compute-cli fine-tuning decrypt-model \
  --provider <PROVIDER_ADDRESS> \
  --task-id <TASK_ID> \
  --encrypted-model ./output/model_<TASK_ID>.bin \
  --output ./decrypted_model.zip
```

The decrypted ZIP contains:
- `output_model/adapter_config.json` — PEFT LoRA configuration
- `output_model/adapter_model.safetensors` — LoRA weights
- `output_model/tokenizer.json` — Tokenizer

### Step 9: Wait for Settlement

The broker automatically settles the fee. Check status until `Finished`:

```bash
0g-compute-cli fine-tuning get-task \
  --provider <PROVIDER_ADDRESS> \
  --task <TASK_ID>
```

## Inference

### Chat with Base Model

Start the local proxy and send requests:

```bash
# Start local inference proxy (foreground)
0g-compute-cli inference serve \
  --provider <PROVIDER_ADDRESS> \
  --port 3000
```

Then use the OpenAI-compatible API:

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen2.5-0.5B-Instruct",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ],
    "max_tokens": 100
  }'
```

### Chat with Fine-Tuned Model

After deploying a LoRA adapter (Step 7), use the adapter name:

```bash
0g-compute-cli fine-tuning chat \
  --provider <PROVIDER_ADDRESS> \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID> \
  --message "Write a hello world in Python"
```

Or use the local proxy with the adapter model name:

```bash
# Get adapter name
0g-compute-cli fine-tuning get-adapter-name \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID>

# Use it in API calls
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "ft-Qwen2-5-0-5B-Instruct-<TASK_ID_PREFIX>",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

### Deploy Adapter Separately

If you didn't use `--deploy` during acknowledge, deploy the adapter later:

```bash
0g-compute-cli fine-tuning deploy-adapter \
  --provider <PROVIDER_ADDRESS> \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID> \
  --wait
```

### List Inference Providers

```bash
0g-compute-cli inference list-providers
```

For detailed health metrics:

```bash
0g-compute-cli inference list-providers-detail
```

### High-Availability Router

For production use with automatic failover across multiple providers:

```bash
0g-compute-cli inference router-serve \
  --port 3000
```

## Account Management

### Check Balance

```bash
0g-compute-cli get-account
```

### Retrieve Funds from Sub-Account

Request to return all funds from a provider sub-account back to your main account:

```bash
0g-compute-cli retrieve-fund \
  --provider <PROVIDER_ADDRESS> \
  --service fine-tuning
```

> **Note**: This requests the return of all funds in the sub-account. There is a lock period before the funds become available in the main account. Use `get-sub-account` to check the remaining lock time.

### Refund from Main Account

```bash
0g-compute-cli refund --amount 1
```

## Available Models

List all supported models:

```bash
0g-compute-cli fine-tuning list-models
```

Common models:
- `Qwen2.5-0.5B-Instruct`
- `Qwen2.5-7B-Instruct`
- `Qwen3-32B`

> Use model names **without** the `Qwen/` prefix.

## API Key Authentication

Generate an API key for programmatic access:

```bash
0g-compute-cli inference get-secret --provider <PROVIDER_ADDRESS>
```

Revoke a specific key:

```bash
0g-compute-cli inference revoke-token \
  --provider <PROVIDER_ADDRESS> \
  --token-id <TOKEN_ID>
```

## Troubleshooting

### "Insufficient balance"

Deposit more funds and transfer to the provider sub-account:

```bash
0g-compute-cli deposit --amount 5
0g-compute-cli transfer-fund --provider <ADDR> --service fine-tuning --amount 2
```

### Task stuck in "Init"

The broker may be downloading the dataset from 0G Storage. Wait a few minutes and check again.

### "User has not acknowledged the provider's TEE signer"

Run the task creation command again — the CLI automatically acknowledges the provider signer. If it persists, explicitly acknowledge:

```bash
0g-compute-cli inference acknowledge-provider --provider <PROVIDER_ADDRESS>
```

### Model download fails

By default, the CLI tries 0G Storage first, then falls back to TEE direct download. You can force a specific method:

```bash
0g-compute-cli fine-tuning acknowledge-model \
  --provider <ADDR> \
  --task-id <ID> \
  --data-path ./output \
  --download-method tee
```
