# Per-User Rate Limiting Design

This document describes the design rationale, token bucket mechanics, and configuration
guidance for the broker's per-user rate limiting system.

## Overview

The broker enforces per-user rate limits across three dimensions, each targeting a
different resource type:

| Dimension | Unit | Applies To | Purpose |
|-----------|------|------------|---------|
| **RPM** (Requests Per Minute) | Request count | All services | Prevent cancel-retry abuse and general request flooding |
| **TPM** (Tokens Per Minute) | Token count | chatbot, speech-to-text, embedding | Prevent GPU exhaustion via large-context requests |
| **IPM** (Images Per Minute) | Image count | text-to-image, image-editing | Prevent GPU exhaustion via bulk image generation |

Whitelisted users (internal services, monitoring) are exempt from all per-user limits
but remain subject to the global concurrency limit.

## Request Flow

```
Request In
  │
  ├─ RPM check ─────── (all services, cheap, no resource acquisition)
  ├─ TPM check ─────── (chatbot/speech-to-text/embedding only)
  ├─ IPM check ─────── (text-to-image/image-editing only)
  ├─ Concurrency check  (acquire slot, defer Release)
  │
  ├─ Set rate limit response headers (pre-request remaining values)
  ├─ Forward to backend
  │
  └─ Response complete
       ├─ chatbot/stt/embedding ──→ ConsumeTokens(actual_tokens)
       └─ image services → ConsumeTokens(image_count)
```

Checks are ordered by cost: cheap read-only checks first (RPM, TPM, IPM), expensive
slot acquisition last (concurrency). This ensures rejected requests release no resources.

## Token Bucket Mechanics

All three dimensions use Go's `golang.org/x/time/rate.Limiter` (token bucket algorithm)
with the same core behavior:

```
Parameters:
  rate  = configured limit / 60   (units per second)
  burst = max bucket capacity     (units)

Bucket starts full at `burst`.
Refills at `rate` units per second.
Never exceeds `burst`.
```

### Post-Consume Model (TPM/IPM)

Unlike RPM (which consumes on check via `Allow()`), TPM and IPM use a **post-consume
model** because the actual resource consumption is unknown until after the response:

1. **Entry check**: `Allow()` reads `Tokens() > 0` — does NOT consume
2. **Forward request** to backend
3. **Response completes**: `ConsumeTokens(actual)` deducts from bucket

This means the bucket can go **negative** after a large request. Subsequent requests
see `Tokens() <= 0` and are rejected until the bucket refills past zero.

```
Example: TPM=60000, burst=10000

t=0   bucket=10000   Allow() → true (10000 > 0)
      Request consumes 15000 tokens
      ConsumeTokens(15000) → bucket = -5000

t=0+  bucket=-5000   Allow() → false (-5000 ≤ 0)
      Next request blocked

t=5s  bucket=-5000 + 5×1000 = 0   Still blocked (≤ 0)
t=6s  bucket=1000                  Allow() → true again
```

### Burst vs Sustained Rate

The token bucket separates two concerns:

- **`burst`** — Maximum instantaneous spike (how many units can be consumed at once)
- **`rate`** (= TPM/60) — Sustained throughput (units per second, long-term average)

This creates a predictable pattern:

| Time Window | Available Units | Governed By |
|-------------|-----------------|-------------|
| Instant (spike) | Up to `burst` | burst parameter |
| Per minute (sustained) | `rate × 60` = TPM | rate parameter |
| First minute (fresh user) | `burst` + `rate × 60` | Both |

**Key insight**: When `burst > rate × 60` (i.e., burst > TPM), the first minute allows
consumption above the TPM rate. This is inherent to the token bucket algorithm and matches
industry behavior (OpenAI, Anthropic all allow burst above per-minute rates).

## Burst and Context Length Relationship

### The Problem

For chatbot services, `context_length` defines the maximum tokens a single request can
consume. If `burst < context_length`, a single max-context request drives the bucket
deeply negative, causing an extended lockout:

```
TPM=60000, burst=10000, context_length=128000

User sends 128K request:
  bucket: 10000 → 10000 - 128000 = -118000
  Recovery time: 118000 / 1000 = 118 seconds

One request → locked for ~2 minutes
```

### The Solution

The broker automatically raises TPM burst to `context_length` when configured:

```
effective_burst = max(configured_burst, context_length)
```

This ensures a single max-context request is always allowed without excessive lockout.

### Trade-off

With `burst = context_length = 128000` and `TPM = 60000`:
- A fresh user can consume up to 128K tokens instantly (2x TPM)
- After that, sustained rate is 60K tokens/min (= TPM)
- This matches how OpenAI/Anthropic handle the same situation

The alternative (pre-estimate input tokens before forwarding) was considered and rejected
because it contradicts the advertised `context_length` — users would get 429 errors on
requests the model can technically handle.

## Configuration Guide

```yaml
concurrencyLimit:
  maxGlobalConcurrent: 20    # Total concurrent requests to backend
  maxPerUserConcurrent: 5    # Per-user concurrent requests
  perUserRPM: 30             # Requests per minute (0 = disabled)
  perUserBurst: 5            # RPM burst size
  perUserTPM: 60000          # Tokens per minute (0 = disabled)
  perUserTPMBurst: 0         # TPM burst (0 = auto: max(TPM/6, context_length))
  perUserIPM: 10             # Images per minute (0 = disabled)
  perUserIPMBurst: 5         # IPM burst (0 = auto: IPM/6, minimum 1)
```

### Default Burst Calculation

| Dimension | Default Burst | Floor |
|-----------|---------------|-------|
| RPM | Explicit (default: 5) | — |
| TPM | `max(TPM/6, context_length)` | 1 |
| IPM | `IPM/6` | 1 |

### Choosing TPM Values

| Model Context | Suggested TPM | Effective Burst | Behavior |
|---------------|---------------|-----------------|----------|
| 8K (small) | 30000 | 8000 | Burst ≈ TPM/4, tight limiting |
| 32K (medium) | 60000 | 32000 | Burst ≈ TPM/2, moderate |
| 128K (large) | 60000 | 128000 | Burst > TPM, loose first window |
| 1M (very large) | 200000 | 1000000 | Burst >> TPM, very loose first window |

For very large context models, consider increasing TPM proportionally or accepting
that the first burst window will exceed the per-minute rate.

## Rate Limit Response Headers

The broker returns rate limit state in response headers so clients/SDKs can throttle
proactively.

### OpenAI Format (default)

```
x-ratelimit-limit-requests: 30
x-ratelimit-remaining-requests: 25
x-ratelimit-reset-requests: 2s
x-ratelimit-limit-tokens: 60000        # or: x-ratelimit-limit-images
x-ratelimit-remaining-tokens: 45000    # or: x-ratelimit-remaining-images
x-ratelimit-reset-tokens: 0s           # or: x-ratelimit-reset-images
```

### Anthropic Format (for /messages endpoints)

```
anthropic-ratelimit-requests-limit: 30
anthropic-ratelimit-requests-remaining: 25
anthropic-ratelimit-requests-reset: 2025-01-01T00:00:02Z
anthropic-ratelimit-tokens-limit: 60000
anthropic-ratelimit-tokens-remaining: 45000
anthropic-ratelimit-tokens-reset: 2025-01-01T00:00:00Z
```

The resource dimension (`tokens` vs `images`) is determined by service type, not
endpoint path. Headers reflect pre-request remaining values; the current request's
consumption is reflected in the next response.

## Rate Limits in /v1/models

The `/v1/models` endpoint exposes configured rate limits so SDKs can perform
client-side throttling:

```json
{
  "id": "gpt-4o",
  "type": "chatbot",
  "rate_limits": {
    "requests_per_minute": 30,
    "tokens_per_minute": 60000
  }
}
```

For image services:
```json
{
  "id": "flux-1",
  "type": "text-to-image",
  "rate_limits": {
    "requests_per_minute": 30,
    "images_per_minute": 10
  }
}
```

The `rate_limits` field is omitted entirely when no limits are configured.
Only the dimensions relevant to the service type are included (`omitempty`).

## Error Response Format

All three dimensions return consistent 429 error responses:

### OpenAI Format
```json
{
  "error": {
    "type": "rate_limit_error",
    "message": "Rate limit exceeded. Please wait 5 seconds (limit: 30 requests/min)."
  }
}
```

### Anthropic Format (for /messages endpoints)
```json
{
  "type": "error",
  "error": {
    "type": "rate_limit_error",
    "message": "Token rate limit exceeded. Please wait 5 seconds (limit: 60000 tokens/min)."
  }
}
```

## Implementation Files

| File | Role |
|------|------|
| `api/common/middleware/per_user_ratelimit.go` | RPM limiter + RPM helpers |
| `api/common/middleware/per_user_tpm_ratelimit.go` | TPM/IPM limiter + admission checks |
| `api/common/middleware/ratelimit_headers.go` | Response header formatting |
| `api/inference/config/config.go` | `ConcurrencyLimitConfig` struct |
| `api/inference/internal/proxy/proxy.go` | Wiring: init, check chain, context injection, headers, Close() |
| `api/inference/internal/ctrl/chatbot.go` | TPM post-consume (chatbot) |
| `api/inference/internal/ctrl/speech_to_text.go` | TPM post-consume (speech-to-text) |
| `api/inference/internal/ctrl/embedding.go` | TPM post-consume (embedding) |
| `api/inference/internal/ctrl/text_to_image.go` | IPM post-consume (text-to-image) |
| `api/inference/internal/ctrl/image_editing.go` | IPM post-consume (image-editing) |
| `api/inference/internal/handler/models.go` | Rate limits in /v1/models response |
