# 0G Compute Network - Provider Guide

This guide covers how to set up and operate a compute provider node on the 0G Compute Network, offering fine-tuning and inference services.

## Overview

As a provider, you run GPU nodes that serve two functions:
- **Fine-tuning**: Train LoRA adapters on user-supplied datasets
- **Inference**: Serve base models and fine-tuned LoRA adapters via an OpenAI-compatible API

The broker software handles service registration, TEE attestation, settlement, and model management automatically.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  Provider Node (CVM / GPU Server)                            │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐       │
│  │   MySQL      │  │   Broker     │  │   vLLM       │       │
│  │  (state DB)  │  │  (Go binary) │  │  (GPU model) │       │
│  └──────────────┘  └──────────────┘  └──────────────┘       │
│                         │                    │               │
│                    ┌────┴────┐          ┌────┴────┐          │
│                    │  Nginx  │          │  sLLM   │          │
│                    │ (proxy) │          │ Wrapper │          │
│                    └─────────┘          └─────────┘          │
└──────────────────────────────────────────────────────────────┘
            │                                 │
    Smart Contracts                    0G Storage
    (Registration,                     (Datasets,
     Settlement)                        Models)
```

## Prerequisites

- **GPU Server**: NVIDIA GPU with sufficient VRAM (H100/H200 recommended for large models)
- **Docker** and **Docker Compose**
- **A0GI Tokens**: At least 100 A0GI for provider stake (per service)
- **Private Key**: Ethereum-compatible wallet for on-chain operations

## Broker Binary

The broker is distributed as a Docker image:

```
ghcr.io/0gfoundation/0g-serving-broker:latest
```

The single binary supports multiple modes via its first argument:

| Command | Description |
|---|---|
| `0g-inference-server` | Inference HTTP broker |
| `0g-inference-event` | Inference background event/settlement worker |
| `0g-fine-tuning-server` | Fine-tuning broker |
| `0g-controller` | Optional controller service for remote management |

## Setup: Fine-Tuning Provider

### 1. Configuration

Create `config-ft.yaml`:

```yaml
contractAddress: "<FINE_TUNING_SERVING_CONTRACT>"

networks:
  ethereum0g:
    url: "https://evmrpc-testnet.0g.ai"
    chainID: 16602
    privateKeys:
      - "<YOUR_PRIVATE_KEY>"

database:
  fineTune: "root:password@tcp(mysql-ft:3306)/fine_tuning?parseTime=true"

servingUrl: "https://<YOUR_PUBLIC_ENDPOINT>"

service:
  dataDir: "/data/ft-data"
  pricePerToken: 800000000000
  quota:
    cpuCount: 8
    nodeMemory: 187
    gpuCount: 1
    nodeStorage: 900
    gpuType: "H200"
  supportedPredefinedModels:
    - "Qwen2.5-0.5B-Instruct"
  localModelPathMap:
    "Qwen2.5-0.5B-Instruct": "/data/models/Qwen2.5-0.5B-Instruct"
  inferenceServiceUrl: "https://<INFERENCE_BROKER_ENDPOINT>"

storageClient:
  indexerStandard: "https://indexer-storage-testnet-standard.0g.ai"
  indexerTurbo: "https://indexer-storage-testnet-turbo.0g.ai"
```

Key fields:

| Field | Description |
|---|---|
| `contractAddress` | FineTuningServing contract address on the target network |
| `networks.ethereum0g.privateKeys` | Provider wallet private key (used for signing transactions) |
| `servingUrl` | Public HTTPS URL where the broker API is accessible |
| `service.dataDir` | Local directory for datasets, models, and training artifacts |
| `service.pricePerToken` | Price per token in neuron (1 A0GI = 10^18 neuron) |
| `service.quota` | Hardware specs advertised to users |
| `service.localModelPathMap` | Map of model names to local paths (pre-downloaded) |
| `service.inferenceServiceUrl` | URL of the inference broker (for pushing LoRA adapters after training) |
| `storageClient` | 0G Storage indexer endpoints for dataset/model transfer |

### 2. Docker Compose

```yaml
services:
  mysql-ft:
    image: mysql:8.0
    environment:
      MYSQL_ROOT_PASSWORD: password
      MYSQL_DATABASE: fine_tuning
    volumes:
      - mysql-ft-data:/var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "localhost"]
      interval: 10s
      retries: 5

  fine-tuning-broker:
    image: ghcr.io/0gfoundation/0g-serving-broker:latest
    command: 0g-fine-tuning-server
    ports:
      - "3080:3080"
    environment:
      CONFIG_FILE: /etc/config.yaml
      PORT: "3080"
    volumes:
      - ./config-ft.yaml:/etc/config.yaml:ro
      - ./ft-data:/data/ft-data
      - ./models:/data/models:ro
      - /var/run/docker.sock:/var/run/docker.sock
    depends_on:
      mysql-ft:
        condition: service_healthy
    privileged: true
    restart: unless-stopped

volumes:
  mysql-ft-data:
```

> **Note**: `privileged: true` and Docker socket mounting are required because the fine-tuning broker spawns training containers internally.

### 3. Pre-download Models

Download supported models before starting the broker:

```bash
# Using huggingface-cli
pip install huggingface_hub
huggingface-cli download Qwen/Qwen2.5-0.5B-Instruct --local-dir ./models/Qwen2.5-0.5B-Instruct
```

### 4. Start Services

```bash
docker compose up -d
```

The broker will automatically:
1. Connect to the smart contract
2. Register/update the provider service (with 100 A0GI stake on first registration)
3. Start polling for new tasks
4. Build the training executor Docker image (first startup takes several minutes)

### 5. TEE Signer Acknowledgement

After registration, the contract owner must acknowledge your TEE signer before users can submit tasks. Contact the 0G team with:
- Your provider address
- The contract address
- Your TEE signer address (visible in broker logs)

## Setup: Inference Provider

### 1. Configuration

Create `config-inf.yaml`:

```yaml
contractAddress: "<INFERENCE_SERVING_CONTRACT>"

networks:
  ethereum0g:
    url: "https://evmrpc-testnet.0g.ai"
    chainID: 16602
    privateKeys:
      - "<YOUR_PRIVATE_KEY>"

database:
  provider: "root:password@tcp(mysql-inf:3306)/inference?parseTime=true"

service:
  servingUrl: "https://<YOUR_PUBLIC_ENDPOINT>"
  targetUrl: "http://vllm:8000"
  inputPrice: 10000000
  outputPrice: 10000000
  type: "chatbot"
  model: "Qwen2.5-0.5B-Instruct"
  verifiability: "dstack"

lora:
  sllmUrl: "http://sllm-wrapper:8343"
  storageClient:
    indexerStandard: "https://indexer-storage-testnet-standard.0g.ai"
    indexerTurbo: "https://indexer-storage-testnet-turbo.0g.ai"
  loraAdaptersHostPath: "/data/lora-adapters"
  loraAdaptersContainerPath: "/lora-adapters"
  fineTuningContractAddress: "<FINE_TUNING_SERVING_CONTRACT>"
  networks:
    ethereum0g:
      url: "https://evmrpc-testnet.0g.ai"
      chainID: 16602
      privateKeys:
        - "<YOUR_PRIVATE_KEY>"
```

Key fields:

| Field | Description |
|---|---|
| `service.targetUrl` | Internal URL of the vLLM server |
| `service.inputPrice` / `outputPrice` | Price per token in neuron |
| `service.type` | Service type: `chatbot`, `text-to-image`, or `speech-to-text` |
| `service.model` | Model name served by vLLM |
| `lora.sllmUrl` | URL of the ServerlessLLM wrapper for LoRA management |
| `lora.loraAdaptersHostPath` | Host path where LoRA adapters are stored |
| `lora.fineTuningContractAddress` | FineTuningServing contract (to listen for adapter delivery events) |

### 2. Docker Compose

```yaml
services:
  mysql-inf:
    image: mysql:8.0
    environment:
      MYSQL_ROOT_PASSWORD: password
      MYSQL_DATABASE: inference
    volumes:
      - mysql-inf-data:/var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "localhost"]
      interval: 10s
      retries: 5

  vllm:
    image: vllm/vllm-openai:v0.8.5.post1
    command: >
      --model /models/Qwen2.5-0.5B-Instruct
      --served-model-name Qwen2.5-0.5B-Instruct
      --enable-lora
      --lora-modules {}
      --max-lora-rank 64
      --max-loras 4
      --max-cpu-loras 8
      --gpu-memory-utilization 0.85
      --max-model-len 4096
    volumes:
      - ./models:/models:ro
      - ./lora-adapters:/lora-adapters
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
    healthcheck:
      test: ["CMD-SHELL", "curl -sf http://localhost:8000/health || exit 1"]
      interval: 30s
      timeout: 10s
      retries: 10
      start_period: 120s

  sllm-wrapper:
    image: python:3.11-slim
    command: python3 /app/sllm-wrapper.py
    environment:
      VLLM_BASE: "http://vllm:8000"
      LORA_HOST_PREFIX: "/data/lora-adapters"
      LORA_CONTAINER_PREFIX: "/lora-adapters"
    volumes:
      - ./sllm-wrapper.py:/app/sllm-wrapper.py:ro
      - ./lora-adapters:/data/lora-adapters
    depends_on:
      vllm:
        condition: service_healthy
    healthcheck:
      test: ["CMD-SHELL", "curl -sf http://localhost:8343/health || exit 1"]
      interval: 10s
      retries: 5

  inference-broker:
    image: ghcr.io/0gfoundation/0g-serving-broker:latest
    command: 0g-inference-server
    environment:
      CONFIG_FILE: /etc/config.yaml
      PORT: "3080"
    volumes:
      - ./config-inf.yaml:/etc/config.yaml:ro
      - ./lora-adapters:/data/lora-adapters
    depends_on:
      mysql-inf:
        condition: service_healthy
      vllm:
        condition: service_healthy
      sllm-wrapper:
        condition: service_healthy
    restart: unless-stopped

  inference-event:
    image: ghcr.io/0gfoundation/0g-serving-broker:latest
    command: 0g-inference-event
    environment:
      CONFIG_FILE: /etc/config.yaml
    volumes:
      - ./config-inf.yaml:/etc/config.yaml:ro
      - ./lora-adapters:/data/lora-adapters
    depends_on:
      mysql-inf:
        condition: service_healthy
    restart: unless-stopped

  nginx:
    image: nginx:1.27.0
    ports:
      - "3081:80"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    depends_on:
      - inference-broker
    restart: unless-stopped

volumes:
  mysql-inf-data:
```

### 3. Nginx Configuration

```nginx
events { worker_connections 1024; }

http {
    server {
        listen 80;
        client_max_body_size 100M;

        location / {
            proxy_pass http://inference-broker:3080;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_read_timeout 300s;
            proxy_buffering off;
        }
    }
}
```

### 4. sLLM Wrapper

The sLLM wrapper bridges the broker's LoRA management API to vLLM. See `sllm-wrapper.py` in the repository for the reference implementation. It provides:

- `POST /deploy` — Load a LoRA adapter into vLLM
- `POST /remove` — Unload a LoRA adapter
- `GET /adapters` — List loaded adapters
- `GET /health` — Health check

### 5. Start Services

```bash
docker compose up -d
```

Monitor startup:

```bash
docker compose logs -f inference-broker
```

Wait for:
```
[SyncService] Service sync successful
```

## Provider Stake

First-time service registration requires a stake of **100 A0GI** (configurable via `service.providerStake` in the config). The stake is sent automatically during the first `addOrUpdateService` call.

- **Fine-tuning**: 100 A0GI stake on the FineTuningServing contract
- **Inference**: 100 A0GI stake on the InferenceServing contract

If running both services with the same wallet, ensure at least **200 A0GI** balance plus gas fees.

To override the default stake amount, add to your config:

```yaml
service:
  providerStake: "100000000000000000000"  # 100 A0GI in wei
```

## Operations

### Monitoring

Check service status:

```bash
docker compose ps
docker compose logs --tail 50 <service-name>
```

The broker logs service sync status every minute:
```
[GetService] Retrieved service from contract - url=..., model=..., pricePerToken=...
```

### Updating Configuration

1. Edit the config YAML file
2. Restart the affected services:

```bash
docker compose restart inference-broker inference-event
```

### Updating the Broker

```bash
docker compose pull
docker compose up -d
```

### Adding New Models

1. Download the model to the models directory
2. For fine-tuning: Add to `service.localModelPathMap` and `service.supportedPredefinedModels`
3. For inference: Update `service.model` and vLLM command
4. Restart services

### Service Removal

To remove your service from the contract (returns your stake):

```bash
0g-compute-cli fine-tuning remove-service
0g-compute-cli inference remove-service
```

## Contract Addresses

### Testnet (Chain ID: 16602)

| Contract | Address |
|---|---|
| InferenceServing | `0xa79F4c8311FF93C06b8CfB403690cc987c93F91E` |
| FineTuningServing | `0xaC66eBd174435c04F1449BBa08157a707B6fa7b1` |
| LedgerManager | `0xE70830508dAc0A97e6c087c75f402f9Be669E406` |

### Testnet Dev (Chain ID: 16602)

| Contract | Address |
|---|---|
| InferenceServing | `0x41bD7Ac5c19000A974D5c192bcd5FB67b56C85c5` |
| FineTuningServing | `0x4e4158DF35CfdC0ac63264D3E112F5B8E9a5c569` |
| LedgerManager | `0x815B93ab4Ba4BDF530dbF1552649a3c534F8BbF7` |

## Troubleshooting

### "InsufficientStake"

Ensure your wallet has at least 100 A0GI balance. Check with:

```bash
# Using web3
python3 -c "
from web3 import Web3
w3 = Web3(Web3.HTTPProvider('https://evmrpc-testnet.0g.ai'))
print(w3.from_wei(w3.eth.get_balance('<YOUR_ADDRESS>'), 'ether'), 'A0GI')
"
```

### "execution reverted" on addOrUpdateService

Common causes:
1. **Insufficient funds** for stake
2. **Trying to add stake on update** — updates must send 0 value
3. **ABI mismatch** — ensure broker image matches the deployed contract version

### Broker keeps restarting

Check logs for the root cause:

```bash
docker compose logs --tail 100 fine-tuning-broker
```

Common issues:
- MySQL not ready (wait for healthcheck)
- Invalid config YAML (check `yaml.UnmarshalStrict` errors)
- Network connectivity (check RPC endpoint)

### TEE signer not acknowledged

After deploying to a new CVM, the broker generates a new TEE signing key. Contact the 0G team to acknowledge it. Provide:
- Provider address
- Contract address(es)
- TEE signer address (from broker logs: `teeAddress: 0x...`)

### Training executor image build slow

On first startup, the fine-tuning broker builds a PyTorch Docker image (~5-10 minutes). The broker HTTP server won't be available until this completes. This is a one-time operation.
