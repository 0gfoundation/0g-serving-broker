# Session Token Design

## Overview

Session Token is used for user authentication when accessing Provider services. The system adopts a dual-mode design, supporting both temporary access and long-term API Key scenarios, with precise revocation and batch revocation capabilities.

---

## Dual-Mode Token System

The system provides two Token modes for different use cases:

```text
TokenId 0-254:  Persistent Token (API Key)
                - Can be individually revoked
                - Consumes tokenId quota
                - Suitable for long-term use and managed scenarios

TokenId 255:    Ephemeral Token (Temporary Token)
                - Cannot be individually revoked (only via revokeAllTokens)
                - Does not consume tokenId quota
                - Suitable for temporary use, SDK default behavior
```

### Mode Comparison

| Feature | Ephemeral Token (tokenId=255) | Persistent Token (tokenId=0-254) |
|---------|------------------------------|----------------------------------|
| Purpose | SDK default calls, temporary access | API Key, long-term access |
| Quota consumption | None | Consumes 1 tokenId |
| Individual revocation | Not supported | Supported |
| Batch revocation | Supported | Supported |
| Expiration | Required, max 24 hours | Can be set to never expire |
| Typical scenario | `getRequestHeaders()` | `createApiKey()` |

### Ephemeral Token Restrictions

Ephemeral Token must satisfy the following conditions:

1. **Must have expiration time**: `expiresAt` cannot be 0
2. **Maximum validity of 24 hours**: `expiresAt - timestamp <= 24 hours`

---

## Token Structure

| Field | Description |
|-------|-------------|
| address | User address |
| provider | Target Provider address |
| timestamp | Token creation timestamp |
| expiresAt | Token expiration timestamp (0 means never expire, only for Persistent Token) |
| nonce | Random nonce to prevent replay attacks |
| generation | Version number for batch revocation |
| tokenId | 0-254: Persistent Token; 255: Ephemeral Token |

---

## Revocation Mechanism: Generation + Bitmap

### Principle

- **generation**: Version number, incremented on `revokeAllTokens()`, invalidating all old version tokens
- **revokedBitmap**: 256-bit bitmap, each bit corresponds to a tokenId, used for precise Persistent Token revocation

```text
Generation 0:  tokenId 0, 1, 2, ... 255  (bitmap: 0b0010 means tokenId=1 is revoked)
     |
     | revokeAllTokens()
     v
Generation 1:  tokenId 0, 1, 2, ... 255  (bitmap: 0, all tokenIds available for reuse)

At this point, all tokens from Generation 0 are automatically invalidated
```

### Contract Storage

Each (user, provider) account stores:

| Field | Size | Description |
|-------|------|-------------|
| generation | 32 bytes | Version number |
| revokedBitmap | 32 bytes | Revocation bitmap, supports 255 Persistent tokenIds |

### Revocation Operations

| Operation | Effect |
|-----------|--------|
| `revokeToken(tokenId)` | Sets corresponding bit in bitmap, precisely revokes a single Persistent Token |
| `revokeTokens(tokenIds[])` | Batch sets bitmap, revokes multiple Persistent Tokens |
| `revokeAllTokens()` | Increments generation, resets bitmap, invalidates all tokens |

---

## Token Validation Flow

Provider validates tokens in the following order:

1. **Expiration check**: If `expiresAt` is set, check if expired
2. **Generation check**: `token.generation` must equal `account.generation`
   - Less than: token has been batch revoked
   - Greater than: invalid token
3. **Bitmap check** (Persistent Token only): Check if corresponding bit is 1
   - Ephemeral Token (tokenId=255) skips this check
4. **Signature verification**: Verify Ethereum signature

---

## Token Lifecycle

### Ephemeral Token

```text
Created (generation=N, tokenId=255, expiresAt=24h later)
     |
     v
  Valid state
     |
     +---- Expired -----------> Invalidated, SDK auto-generates new token
     |
     +---- revokeAllTokens() -> Invalidated (generation mismatch)
```

### Persistent Token

```text
Created (generation=N, tokenId=X, expiresAt=0 never expires)
     |
     v
  Valid state
     |
     +---- revokeToken(X) ----> Invalidated (bitmap marked)
     |
     +---- revokeAllTokens() -> Invalidated (generation mismatch)
                                tokenId available for reuse
```

### Operation Comparison

| Operation | Ephemeral Token | Persistent Token |
|-----------|-----------------|------------------|
| Natural expiration | SDK auto-regenerates | Requires manual creation of new Key |
| `revokeToken(tokenId)` | Not supported | Precise revocation |
| `revokeAllTokens()` | Invalidated | Invalidated |

---

## Caching Strategy

Provider uses 10-minute cache to reduce contract queries. Revocation operations take effect within 10 minutes at most.
