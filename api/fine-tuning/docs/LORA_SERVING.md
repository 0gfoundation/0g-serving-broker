# LoRA Inference Serving & Multi-Tier Caching

This document describes the LoRA inference serving subsystem added to the fine-tuning broker. It enables providers to automatically serve fine-tuned LoRA adapters to end-users through an OpenAI-compatible API, with a multi-tier caching strategy that manages GPU memory, CPU memory, local disk, and 0G decentralized storage.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Multi-Tier Caching](#multi-tier-caching)
- [Components](#components)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Authentication](#authentication)
- [Deployment](#deployment)
- [Testing](#testing)

## Overview

When a fine-tuning task completes, its LoRA adapter output is automatically discovered and registered for inference serving. The system:

1. Runs a single **vLLM** process with one base model and multiple LoRA adapters attached
2. Routes user requests to the correct LoRA adapter based on the model name
3. Manages adapter lifecycle across four storage tiers to optimize GPU utilization
4. Enforces per-model access control via EIP-191 signature authentication

### Key Design Decisions

- **vLLM as the inference engine**: Chosen for its native multi-LoRA support, high throughput, and OpenAI-compatible API
- **Filesystem resolver for dynamic loading**: LoRA adapters are discovered from a directory at runtime — no server restart required
- **Proxy-based access control**: The broker proxy sits in front of vLLM, verifying model ownership before forwarding requests

## Architecture

```
                                    ┌─────────────────────────────┐
                                    │         vLLM Process        │
                                    │                             │
  User Request                      │  Base Model (e.g. Qwen2.5) │
  (model: ft-xxx-yyy)              │         ┌───────────┐       │
        │                           │    ┌────┤  LoRA #1  │       │
        ▼                           │    │    └───────────┘       │
  ┌───────────┐    ┌───────────┐    │    │    ┌───────────┐       │
  │  Auth +   │───▶│  Serving  │───▶│────┼────┤  LoRA #2  │       │
  │  Routing  │    │   Proxy   │    │    │    └───────────┘       │
  └───────────┘    └───────────┘    │    │    ┌───────────┐       │
                                    │    └────┤  LoRA #N  │       │
                                    │         └───────────┘       │
                                    └─────────────────────────────┘
                                                  ▲
                                                  │ symlinks
                                    ┌─────────────────────────────┐
                                    │    /tmp/lora-modules/        │
                                    │    ├── ft-qwen-abc123/       │
                                    │    ├── ft-qwen-def456/       │
                                    │    └── ft-qwen-ghi789/       │
                                    └─────────────────────────────┘
```

### Request Flow

1. User sends a chat completion request with `model: "ft-qwen-abc123"` and an `Authorization: Bearer <signature>` header
2. The **auth middleware** recovers the Ethereum address from the EIP-191 signature
3. The **proxy** verifies the user owns the requested model
4. The proxy checks the model's **cache state**:
   - `active` → forward to vLLM
   - `archived` → trigger async restore from 0G Storage, return HTTP 202
   - `loading` → return HTTP 202 with status message
5. vLLM serves the request using the corresponding LoRA adapter
6. Response is streamed back to the user

## Multi-Tier Caching

The system implements a four-tier storage hierarchy for LoRA adapters:

```
┌──────────────────────────────────────────────────────────┐
│                    Storage Tiers                         │
│                                                          │
│  ┌────────┐    ┌────────┐    ┌────────┐    ┌──────────┐ │
│  │  GPU   │◄──▶│  CPU   │◄──▶│  Disk  │◄──▶│0G Storage│ │
│  │ (Hot)  │    │ (Warm) │    │ (Cool) │    │  (Cold)  │ │
│  └────────┘    └────────┘    └────────┘    └──────────┘ │
│                                                          │
│  ◄── vLLM native LRU ──▶    ◄── Broker managed ──────▶  │
└──────────────────────────────────────────────────────────┘
```

| Tier | Managed By | Capacity Control | Latency |
|------|-----------|-----------------|---------|
| GPU (hot) | vLLM LRU cache | `--max-loras` | ~0ms (already loaded) |
| CPU (warm) | vLLM LRU cache | `--max-cpu-loras` | ~10ms (CPU→GPU transfer) |
| Disk (cool) | Filesystem resolver | Disk space | ~50-75ms (disk read + load) |
| 0G Storage (cold) | Broker offload loop | Unlimited | Seconds-minutes (network download) |

### Tier Transitions

**GPU ↔ CPU ↔ Disk** (handled by vLLM natively):
- vLLM maintains an LRU cache of LoRA adapters on GPU
- When GPU slots are full, least-recently-used adapters are moved to CPU memory
- When CPU slots are full, adapters are evicted entirely and must be reloaded from disk
- The filesystem resolver plugin automatically loads adapters from the `lora-modules` directory

**Disk → 0G Storage** (handled by the broker's offload loop):
- A background goroutine checks every minute for adapters that have not been accessed within `offloadAfterMinutes`
- Inactive adapters are archived: the local symlink and LoRA files are deleted
- The adapter's metadata (model name, task ID, storage hash) is retained in memory
- Only adapters with a valid `OutputRootHash` (uploaded to 0G Storage during fine-tuning finalization) can be offloaded

**0G Storage → Disk** (handled by the broker's restore logic):
- When a user requests an archived adapter, `RestoreModel` is triggered
- The adapter is downloaded from 0G Storage using the stored root hash
- A new symlink is created in the `lora-modules` directory
- The model state transitions: `archived` → `loading` → `active`
- During the download, subsequent requests receive HTTP 202 with a `"status": "loading"` response

### Benchmark Results

Tested on NVIDIA H20 (98GB) with Qwen2.5-0.5B-Instruct base model:

| Scenario | TTFT | Total Time |
|----------|------|------------|
| Hot (GPU cached) | ~15ms | ~50ms |
| Warm (CPU→GPU reload) | ~25ms | ~60ms |
| Cold (disk→GPU load) | ~65ms | ~100ms |
| Concurrent 4 LoRAs | ~35ms avg | ~80ms avg |

These results confirm that GPU↔CPU↔Disk transitions are effectively instantaneous from the user's perspective. The only significant latency comes from the 0G Storage cold tier, which involves network downloads.

## Components

### Manager (`internal/serving/manager.go`)

The central controller that manages:

- **vLLM process lifecycle**: Starts vLLM with multi-LoRA arguments and environment variables, monitors health
- **Auto-discovery**: Polls the database every 30 seconds for completed fine-tuning tasks and registers their LoRA adapters
- **Model registration**: Creates symlinks from the LoRA output directory to the `lora-modules` directory
- **Pruning**: When the number of served models exceeds `MaxLoraModules`, the oldest registered model is removed

### Model Cache (`internal/serving/model_cache.go`)

Handles the Disk ↔ 0G Storage tier:

- **`ModelState`** enum: `active` (on disk), `archived` (cold storage only), `loading` (downloading)
- **`offloadLoop`**: Background goroutine that periodically checks `LastAccessedAt` timestamps
- **`RestoreModel`**: Triggers async download from 0G Storage via the `StorageDownloader` interface
- **`RecordAccess`**: Updates the last-accessed timestamp on every inference request

### Proxy (`internal/serving/proxy.go`)

OpenAI-compatible HTTP endpoints:

- Signature-based authentication
- Model ownership enforcement
- Cache state checks with appropriate HTTP status codes
- Request proxying to vLLM with streaming support

### Registry (`internal/serving/registry.go`)

Placeholder for future on-chain inference service registration. Currently tracks serving state locally.

## API Reference

All serving endpoints are mounted under `/v1/serving/`.

### POST `/v1/serving/v1/chat/completions`

OpenAI-compatible chat completion endpoint.

**Headers:**
- `Authorization: Bearer <EIP-191-signature>` (required)
- `Content-Type: application/json`

**Request Body:**
```json
{
  "model": "ft-qwen2-5-0-5B-I-a1b2c3d4e5f6",
  "messages": [
    {"role": "user", "content": "Hello, how are you?"}
  ],
  "stream": true,
  "max_tokens": 100
}
```

**Responses:**
- `200`: Inference result (same format as OpenAI API)
- `202`: Model is loading from cold storage
  ```json
  {
    "error": "Model is being loaded from cold storage. Please retry in a few moments.",
    "status": "loading",
    "model": "ft-qwen2-5-0-5B-I-a1b2c3d4e5f6"
  }
  ```
- `401`: Invalid or missing signature
- `403`: User does not own the requested model
- `404`: Model not found
- `503`: vLLM not ready

### GET `/v1/serving/v1/models`

List models owned by the authenticated user.

**Headers:**
- `Authorization: Bearer <EIP-191-signature>` (required)

**Response:**
```json
{
  "object": "list",
  "data": [
    {
      "id": "ft-qwen2-5-0-5B-I-a1b2c3d4e5f6",
      "object": "model",
      "owned_by": "0x1234...abcd",
      "task_id": "a1b2c3d4-...",
      "state": "active"
    }
  ]
}
```

### GET `/v1/serving/models`

List all served models (no authentication required, for monitoring).

### GET `/v1/serving/health`

Health check with cache statistics.

**Response:**
```json
{
  "vllm_ready": true,
  "total_models": 10,
  "active_on_disk": 7,
  "archived_cold": 2,
  "loading": 1,
  "cold_storage": true,
  "offload_minutes": 60
}
```

## Configuration

Add the following to your `config.yaml`:

```yaml
serving:
  enable: true
  baseModelPath: "/models/Qwen2.5-0.5B-Instruct"  # Path to base model
  inferenceGpuIds: "0"                              # GPU device IDs for vLLM
  vllmPort: 8000                                    # vLLM server port
  maxLoraRank: 64                                   # Maximum LoRA rank supported
  maxLoraModules: 16                                # Max LoRA adapters on GPU simultaneously
  maxCpuLoras: 32                                   # Max LoRA adapters cached in CPU memory
  loraModulesDir: "/tmp/lora-modules"               # Directory for LoRA symlinks
  inputPrice: "10000000"                            # Price per input token (for contract registration)
  outputPrice: "10000000"                           # Price per output token
  offloadAfterMinutes: 60                           # Minutes of inactivity before cold-storage offload
  enableColdStorage: false                          # Enable 0G Storage offload/restore
```

### Configuration Notes

- **`maxLoraModules`** controls `--max-loras` in vLLM: how many LoRA adapters can be loaded on GPU simultaneously. Higher values use more GPU memory.
- **`maxCpuLoras`** controls `--max-cpu-loras`: how many adapters are cached in CPU memory as a warm tier. Set this higher than `maxLoraModules` for a larger warm cache.
- **`enableColdStorage`** should only be enabled when 0G Storage is properly configured and the fine-tuning service uploads model outputs (i.e., `OutputRootHash` is populated).
- **`offloadAfterMinutes`** sets the inactivity threshold. Models without access within this period are offloaded to save disk space. Set to `0` to disable time-based offloading.

## Authentication

The serving proxy uses **EIP-191 personal message signatures** for authentication:

1. The user signs the message `"0g-serving-inference-auth"` with their Ethereum private key
2. The signature is sent in the `Authorization: Bearer <hex-signature>` header
3. The proxy recovers the signer's address and matches it against the model's `UserAddress`
4. Only the user who created the fine-tuning task can access their LoRA model

This ensures that fine-tuned models remain private to their creators without requiring API keys or session tokens.

## Deployment

### Prerequisites

- NVIDIA GPU with sufficient VRAM for the base model + LoRA adapters
- vLLM installed (`pip install vllm`)
- `lora_filesystem_resolver` plugin (included with vLLM by default since v0.6.x)

### Environment Variables Set by the Manager

The following are automatically set when starting the vLLM subprocess:

```bash
VLLM_ALLOW_RUNTIME_LORA_UPDATING=True       # Enable dynamic LoRA loading
VLLM_PLUGINS=lora_filesystem_resolver        # Use filesystem-based LoRA discovery
VLLM_LORA_RESOLVER_CACHE_DIR=/tmp/lora-modules  # Directory to scan for adapters
CUDA_VISIBLE_DEVICES=0                       # GPU isolation (if configured)
```

### GPU Memory Estimation

As a rough guide for the base model + LoRA overhead:

| Base Model | Base VRAM | Per-LoRA (r=8) | Per-LoRA (r=64) |
|-----------|----------|----------------|-----------------|
| 0.5B params | ~1 GB | ~5 MB | ~40 MB |
| 7B params | ~14 GB | ~50 MB | ~400 MB |
| 13B params | ~26 GB | ~100 MB | ~800 MB |

With a 98GB H20 GPU and a 7B base model, you can comfortably serve 16+ LoRA adapters on GPU simultaneously.

## Testing

### Unit Tests

The serving module includes 17 unit tests covering:

```bash
cd api/fine-tuning && go test ./internal/serving/ -v
```

Test coverage includes:
- Model registration, state tracking, and access recording
- Offload logic (stale models, skip models without storage hash, skip recently accessed)
- Restore logic (async download, idempotent for active/loading states)
- Unregister, prune, ownership checks, deterministic model naming
- Full offload → restore end-to-end cycle

### End-to-End Validation

Verified on NVIDIA H20 (98GB) with Qwen2.5-0.5B-Instruct:

1. **Base model inference** — vLLM serves the base model correctly
2. **Dynamic LoRA loading** — 3 LoRA adapters loaded via filesystem resolver without restart
3. **Multi-LoRA routing** — Concurrent requests to different adapters each route correctly
4. **GPU/CPU caching** — Adapter files deleted from disk, vLLM still serves from memory cache
5. **Disk restore** — Adapter files restored to disk, vLLM resumes serving on next request

### Manual Testing

To test locally without the full broker:

```bash
# 1. Start vLLM with multi-LoRA
export VLLM_ALLOW_RUNTIME_LORA_UPDATING=True
export VLLM_PLUGINS=lora_filesystem_resolver
export VLLM_LORA_RESOLVER_CACHE_DIR=/path/to/lora-modules

vllm serve /path/to/base-model \
  --enable-lora \
  --max-lora-rank 64 \
  --max-loras 16 \
  --max-cpu-loras 32

# 2. Place LoRA adapters in the modules directory
ln -s /path/to/task-output/output_model /path/to/lora-modules/my-adapter

# 3. Send inference request
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "my-adapter", "messages": [{"role": "user", "content": "Hello"}]}'
```
