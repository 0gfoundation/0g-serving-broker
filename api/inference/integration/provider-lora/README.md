# Inference broker + LoRA serving (TEE-deployable)

Reference deployment for an inference broker that serves both base-model
inference **and** fine-tune-derived LoRA adapters. Designed to run inside
a Phala/dstack TEE without any host-side file pre-staging (issue #469),
and to advertise its LoRA capability so off-chain routers can find it
(issue #468), with quota & admission control protecting the GPU pool
(issue #470).

## What's different from `provider/`

| Aspect | `provider/` example | `provider-lora/` (this folder) |
|---|---|---|
| Host bind-mounts | model dir, nginx.conf, init.sql | **only** `config.yaml` |
| vLLM model source | host-mounted `/models/...` | HF auto-download into named volume |
| sllm-wrapper | bind-mount `sllm-wrapper.py` | published image `ghcr.io/0gfoundation/0g-sllm-wrapper:latest` |
| nginx | front-proxies the broker | dropped, broker listens on host port directly |
| LoRA | not configured | `lora.enable: true` + `autoDeploy: true` |

If you don't need LoRA, keep using `provider/`. If you do, prefer this
example for any TEE deployment.

## Topology

```
        ┌──────────────── inf-net ────────────────┐
inbound │                                         │
  ─────►│ inference-broker:3080  ── reads ───────►│ MySQL
        │     │                                   │
        │     ├─ /v1/chat/completions ───────────►│ sllm-wrapper:8343 ── /v1/* ──► vllm:8000 (Qwen base + LoRA)
        │     ├─ /v1/lora/admin/evict (provider)  │
        │     ├─ /v1/capabilities (public)        │
        │     ├─ /v1/models (public)              │
        │     └─ /v1/proxy/... (auth)             │
        │                                         │
        │ inference-event ── reads chain ────────►│ ethereum0g RPC
        │     │                                   │
        │     └─ on DeliverableAcknowledged ─────►│ inference-broker (admission check) ──► download ──► deploy
        └─────────────────────────────────────────┘

  Named volumes:
    hf-cache        → vLLM HF model cache (auto-populated)
    lora-modules    → adapter files (shared between vLLM, sllm-wrapper, broker)
    mysql-data      → broker DB
```

The only file mounted from the host is `./config.yaml`, which dstack lets
you deliver via `compose_template`.

## First-run notes

* **HF download time.** vLLM pulls the base model from Hugging Face on
  first start into the `hf-cache` volume. For Qwen2.5-0.5B-Instruct
  this is ~1 GB; for Qwen3-32B it's ~64 GB. Make sure the TEE has
  enough disk and outbound connectivity, or set `HF_ENDPOINT` to a
  reachable mirror.
* **GPU memory.** `--gpu-memory-utilization 0.5` is conservative;
  bump it for larger models. Qwen3-32B needs roughly 64 GB VRAM with
  KV cache.
* **vLLM healthcheck `start_period`.** Set to 600 s so the first HF
  download doesn't trigger restart loops.
* **LoRA module directory.** `lora-modules` is a named volume shared
  between vLLM, sllm-wrapper, and the broker. The broker writes
  decrypted adapter files there; sllm-wrapper points vLLM at the same
  paths. Keep `LORA_HOST_PREFIX` and `LORA_CONTAINER_PREFIX` equal.

## Capability discovery (issue #468)

After registration this broker advertises LoRA capability in three places:

1. **On-chain `additionalInfo`**:
   `{"LoRAEnabled":true, "LoRABaseModel":"...", "LoRAAdapterPrefix":"ft-", ...}`
2. **`GET /v1/models`**: the `supports_fine_tuned_adapters: true` field
   on the base model entry.
3. **`GET /v1/capabilities`**: a public, unauthenticated capability sheet.

```sh
curl -s https://<your-broker>/v1/capabilities | jq
{
  "base_model": "Qwen2.5-0.5B-Instruct",
  "fine_tuning": {
    "supports_fine_tuned_adapters": true,
    "base_model": "Qwen/Qwen2.5-0.5B-Instruct",
    "adapter_prefix": "ft-"
  }
}
```

## Quota & operator controls (issue #470)

The `lora.quota` block in `config.yaml` enforces:

| Quota | Default | What it stops |
|---|---|---|
| `maxAdaptersPerUser` | 5 | One user spamming many ack events |
| `maxAdaptersTotal` | 100 | Broker-wide adapter count overflow |
| `maxConcurrentDownloads` | 3 | Burst of acks → unbounded download goroutines |
| `maxAdapterDiskBytes` | 100 GiB | Disk exhaustion |

Capacity-based eviction (LRU) runs every `capacityCheckIntervalMinutes`
and archives the oldest idle adapter when over quota. Files are removed
from disk; the DB row is kept in `archived` state so the user can
re-acknowledge to redownload.

Operator kill-switch:

```sh
# Provider-key signed request (use the broker's CLI/SDK, not curl)
POST /v1/lora/admin/evict
{"adapterName": "ft-Qwen2-5-0-5B-Instruct-abc123", "purge": true}
```

`purge=false` archives only (DB row kept). `purge=true` removes the row.
Add user addresses to `lora.quota.blockedUsers` to reject all future
registrations from them.

## Operations checklist

* [ ] Replace `<Private_Key>` and `<Serving URL>` in `config.yaml`.
* [ ] Choose your base model (HF id) and adjust `--gpu-memory-utilization`.
* [ ] Verify `/v1/capabilities` returns `supports_fine_tuned_adapters: true`.
* [ ] Confirm on-chain `additionalInfo` contains `"LoRAEnabled":true`
      after the broker re-syncs the service.
* [ ] Tune `lora.quota.*` for your hardware. Defaults assume a single
      H100/H200 with `~80–143 GB` of HBM.
