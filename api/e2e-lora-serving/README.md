# Fine-tuning to Serving: Complete E2E Guide

Fine-tune a model with your data, then chat with it — the serving happens automatically.

## How It Works

```
  Upload Dataset ──→ Train ──→ Acknowledge ──→ Chat
       (5 min)      (5–30 min)    (1 min)     (automatic!)
                                     │
                         Broker auto-deploys your
                         adapter to GPU inference
```

Once you acknowledge the trained model, the inference broker **automatically** downloads it, decrypts it, and loads it into the GPU. You just chat — no manual deployment needed.

## Requirements

- Node.js >= 22 (`node --version`)
- `0g-compute-cli` installed:
  ```bash
  npm install -g @0glabs/0g-serving-broker
  ```
- A wallet with 0G testnet tokens (for gas + training fees)

## Step 1 — Set Up Your Wallet

```bash
# Configure the CLI
0g-compute-cli setup-network
0g-compute-cli login

# Deposit funds
0g-compute-cli deposit --amount 10

# Find a provider
0g-compute-cli fine-tuning list-providers

# Fund the provider for training AND inference
0g-compute-cli transfer-fund --provider <PROVIDER> --amount 5 --service fine-tuning
0g-compute-cli transfer-fund --provider <PROVIDER> --amount 2 --service inference

# Acknowledge the provider (required once before first inference)
0g-compute-cli inference acknowledge-provider --provider <PROVIDER>
```

> Replace `<PROVIDER>` with the provider address shown by `list-providers`.

## Step 2 — Prepare and Upload Your Dataset

Create a JSONL file where each line is a training conversation:

```jsonl
{"messages":[{"role":"user","content":"What is 0G?"},{"role":"assistant","content":"0G is a decentralized AI infrastructure."}]}
{"messages":[{"role":"user","content":"How does fine-tuning work?"},{"role":"assistant","content":"Fine-tuning adapts a pre-trained model to your specific data."}]}
```

Upload it:

```bash
0g-compute-cli fine-tuning upload --data-path ./my-dataset.jsonl
```

Save the **root hash** it prints (e.g. `0x128ebb21...`).

## Step 3 — Start Fine-tuning

Check available models:

```bash
0g-compute-cli fine-tuning list-models
```

Create a training config (`train-config.json`):

```json
{
  "neftune_noise_alpha": 5,
  "num_train_epochs": 1,
  "per_device_train_batch_size": 2,
  "learning_rate": 0.0002,
  "max_steps": 3
}
```

> Use `max_steps: 3` for a quick test. Increase for real training.
> Write `0.0002`, not `2e-4` — some parsers reject scientific notation.

Create the task:

```bash
0g-compute-cli fine-tuning create-task \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --dataset <ROOT_HASH> \
  --config-path ./train-config.json
```

Save the **task ID** it prints.

> Use model names without the `Qwen/` prefix.

## Step 4 — Wait for Training

```bash
0g-compute-cli fine-tuning get-task --provider <PROVIDER> --task <TASK_ID>
```

Run this every 30 seconds or so. The status will progress:

```
Init → SettingUp → SetUp → Training → Trained → Delivering → Delivered
```

When it shows **Delivered**, move to the next step.

## Step 5 — Acknowledge the Model

```bash
0g-compute-cli fine-tuning acknowledge-model \
  --provider <PROVIDER> \
  --task-id <TASK_ID> \
  --data-path ./my-model
```

> The flag is `--task-id` here (not `--task`).

This does two things at once:
1. Downloads an encrypted copy of the model to your machine
2. Confirms receipt on-chain, which **triggers the inference broker to auto-deploy your adapter**

Wait about 60 seconds for the task to reach **Finished**:

```bash
0g-compute-cli fine-tuning get-task --provider <PROVIDER> --task <TASK_ID>
```

## Step 6 — Chat with Your Model

Your fine-tuned model is now live. Chat with it:

```bash
0g-compute-cli fine-tuning chat \
  --provider <PROVIDER> \
  --model "Qwen2.5-0.5B-Instruct" \
  --task-id <TASK_ID> \
  --message "Hello! What can you do?"
```

That's it! The broker figured out everything else automatically.

### Alternative: Use an API key

If you want to integrate with your own app (any OpenAI-compatible client works):

```bash
# Get an API key
0g-compute-cli inference get-secret --provider <PROVIDER> --duration 0

# Get your adapter's model name
0g-compute-cli fine-tuning get-adapter-name \
  --model "Qwen2.5-0.5B-Instruct" --task-id <TASK_ID>
# Example output: ft-Qwen2-5-0-5B-Instruct-1f6fef2b-e0f6
```

Then use any OpenAI client:

```bash
curl -X POST https://<BROKER_URL>/v1/proxy/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <API_KEY>" \
  -d '{
    "model": "ft-Qwen2-5-0-5B-Instruct-1f6fef2b-e0f6",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

## Optional: Get a Local Copy of the Adapter

If you want the adapter files on your machine (e.g. for local use or backup), decrypt the model you downloaded in Step 5:

```bash
# Wait for task status to be "Finished" first
0g-compute-cli fine-tuning decrypt-model \
  --provider <PROVIDER> \
  --task-id <TASK_ID> \
  --encrypted-model ./my-model \
  --output ./my-model-decrypted.zip

unzip ./my-model-decrypted.zip -d ./my-adapter
ls ./my-adapter/
# adapter_config.json  adapter_model.safetensors
```

---

## Commands Cheat Sheet

| What | Command |
|------|---------|
| Deposit | `0g-compute-cli deposit --amount 10` |
| Fund provider (training) | `0g-compute-cli transfer-fund --provider <ADDR> --amount 5 --service fine-tuning` |
| Fund provider (inference) | `0g-compute-cli transfer-fund --provider <ADDR> --amount 2 --service inference` |
| Acknowledge provider | `0g-compute-cli inference acknowledge-provider --provider <ADDR>` |
| Upload dataset | `0g-compute-cli fine-tuning upload --data-path ./data.jsonl` |
| Create task | `0g-compute-cli fine-tuning create-task --provider <ADDR> --model <MODEL> --dataset <HASH> --config-path ./config.json` |
| Check status | `0g-compute-cli fine-tuning get-task --provider <ADDR> --task <ID>` |
| View training logs | `0g-compute-cli fine-tuning get-log --provider <ADDR> --task <ID>` |
| Acknowledge model | `0g-compute-cli fine-tuning acknowledge-model --provider <ADDR> --task-id <ID> --data-path ./model` |
| Chat | `0g-compute-cli fine-tuning chat --provider <ADDR> --model <MODEL> --task-id <ID> --message "Hi"` |
| Get API key | `0g-compute-cli inference get-secret --provider <ADDR> --duration 0` |
| Get adapter name | `0g-compute-cli fine-tuning get-adapter-name --model <MODEL> --task-id <ID>` |

## Common Gotchas

| Gotcha | Correct | Wrong |
|--------|---------|-------|
| Task flag varies by command | `get-task` uses `--task`; `acknowledge-model` uses `--task-id` | Mixing them up |
| Model names have no prefix | `Qwen2.5-0.5B-Instruct` | `Qwen/Qwen2.5-0.5B-Instruct` |
| Learning rate format | `0.0002` | `2e-4` |
| Provider flag name | `--provider` | `--provider-address` |

---

## Troubleshooting

### "Insufficient balance" when creating a task

Deposit more and transfer to the provider:

```bash
0g-compute-cli get-account
0g-compute-cli deposit --amount 10
0g-compute-cli transfer-fund --provider <ADDR> --amount 5 --service fine-tuning
```

### Task stuck at "Init" or "SettingUp"

Providers process one task at a time. If another task is running, yours will wait in the queue. Check back in a few minutes.

### "File not found" when acknowledging model

The model may not be on 0G Storage yet. Try the TEE fallback:

```bash
0g-compute-cli fine-tuning acknowledge-model \
  --provider <ADDR> --task-id <ID> --data-path ./model \
  --download-method tee
```

### Decrypt fails

The decryption key is only available after the task reaches **Finished**. Check the status and wait.

### Chat returns "adapter not found"

The broker auto-deploys the adapter when you acknowledge, but it can take up to 30 seconds. Wait and try again.

If it persists, the provider might not have LoRA serving configured. Ask your team to confirm.

---

## Automated E2E Test (for Developers)

There is a script that automates the serving half of this flow (adapter encryption → 0G Storage upload → on-chain events → broker auto-deploy → GPU inference) in about 45 seconds:

```bash
cd api/e2e-lora-serving
pip install web3 eciespy cryptography eth_keys requests
cp .env.example .env    # edit with your values
set -a && source .env && set +a
python3 e2e_real_0g_storage_test.py
```

See `.env.example` for configuration options.
