# 0G SLLM Wrapper

A small ServerlessLLM-compatible API shim over vLLM, used by the inference
broker's LoRA serving path.

The broker speaks a "ServerlessLLM" subset (deploy / chat / list / delete)
to a target service. This wrapper translates that surface into native vLLM
endpoints (`/v1/load_lora_adapter`, `/v1/chat/completions`, …) and resolves
model-name aliases so a request for `/models/Qwen2.5-0.5B-Instruct` is
forwarded as the canonical id vLLM exposes in `/v1/models`.

## Why a separate image

In Phala/dstack TEE deployments the host operator can mount the broker
config file but **cannot** pre-stage other files on the host filesystem
(see issue #469 and `api/fine-tuning/docs/DESIGN_SERVING_FINETUNED_MODELS.md`,
"TEE 环境约束"). Earlier examples bind-mounted `sllm-wrapper.py`, which
made them undeployable to a real TEE. This image bakes the script so the
compose file only needs `image:` and (optionally) env vars.

## Build / publish

```sh
cd api/inference/integration/sllm-wrapper
docker build -t ghcr.io/0gfoundation/0g-sllm-wrapper:dev .
docker push ghcr.io/0gfoundation/0g-sllm-wrapper:dev
```

## Usage

```yaml
services:
  sllm-wrapper:
    image: ghcr.io/0gfoundation/0g-sllm-wrapper:dev
    environment:
      - VLLM_BASE=http://vllm:8000
      - LORA_HOST_PREFIX=/lora-modules/
      - LORA_CONTAINER_PREFIX=/lora-modules/
    healthcheck:
      test: ["CMD-SHELL", "python3 -c \"import urllib.request; urllib.request.urlopen('http://localhost:8343/health')\""]
```

## Configuration

| Var | Default | Notes |
|---|---|---|
| `PORT` | `8343` | Listen port |
| `VLLM_BASE` | `http://vllm:8000` | vLLM HTTP base URL |
| `LORA_HOST_PREFIX` | `/lora-modules/` | Path prefix the broker writes in deploy bodies |
| `LORA_CONTAINER_PREFIX` | `/lora-modules/` | Path prefix vLLM expects (use the same value when the broker and vLLM share a named volume) |
