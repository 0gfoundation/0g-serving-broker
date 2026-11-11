# Admin overlay: changing prices and limits without a redeploy

A private (TeeML) deployment runs without the controller: everything that decides where a
prompt goes and who can read it is fixed by the deployment, and changing it is a redeploy.
What still needs to change while the model stays loaded — prices, rate limits, the free-use
whitelist, settlement timing — goes through the **config overlay**: a second config file,
merged over the main one when the process starts, that may contain only allowlisted keys.

The broker serves `GET` / `PUT /v1/admin/overlay` to read and replace it. The broker and the
event service watch the file and restart on a new one. Nothing here needs the docker socket.

Code: `api/inference/config/overlay.go` (allowlist, merge), `overlay_watch.go` (restart on
change), `api/inference/internal/handler/admin_overlay.go` (endpoint, auth).

## What can change

Everything not listed is refused — by the endpoint (400, nothing written) and, if a refused
key reaches the file some other way, by the process at start (it will not run without the
setting rather than silently drop it). A key added to the broker later is not changeable
until it is added to the allowlist.

| Section | Keys |
|---|---|
| `service` | `inputPrice`, `outputPrice`, `inputPriceUSDPerMillionTokens`, `outputPriceUSDPerMillionTokens`, `outputPriceUSDPerImage`, `outputPriceUSDPerSecond`, `priceDenomination`, `enforceMaxCompletionTokens`, `modelInfo`, `ownedBy`, `providerName`, `providerCountry` |
| `service.modelPricing[]` | entries are matched by `model`; an existing model's price fields (as above, without `outputPriceUSDPerImage`), `tiers`, `billing`, `cacheTokenBilling`, `modelInfo`. A model the main config does not have cannot be added. |
| whole sections | `whitelist`, `concurrencyLimit`, `tieredPricing`, `cacheTokenBilling`, `modalityPricing`, `providerHttp`, `chatCacheExpiration`, `interval`, `settlement`, `gasPrice`, `maxGasPrice`, `reconciliation`, `userUsageStats` |
| `overloadGuard` | `enabled`, `maxQueueRequests`, `maxTokenUsage`, `pollInterval`, `retryAfter` (not `metricsUrl`) |

Deliberately not changeable: every upstream and model-identity field (`targetUrl`,
`upstreamModel`, `modelAliases`, `canonicalId`, `providerType`, …), request rewriting
(`injectBodyFields`, `stripBodyFields`, `requestProfile`), `logger`, `database`, `assay`,
`lora`, `zk`, `monitor`, `servingUrl` (on-chain URL — rewriting on-chain URLs was the
2026-11-05 attack), `priceFeed` (source URLs and an API key), `revenueTransfer` (where
earnings go).

Maps merge key by key; any other value replaces the main config's. `PUT` replaces the
whole overlay: send everything that should stay changed, not just the delta. An empty body
(or `{}`) clears it.

## Deployment wiring

```yaml
  0g-serving-provider-broker:
    environment:
      - CONFIG_OVERLAY_FILE=/etc/overlay/overlay.yaml
      # Literal in the compose file, so the compose hash covers who may change prices.
      # Never put it in the encrypted config.
      - ZG_ADMIN_ADDRESSES=0xAdmin1,0xAdmin2
    volumes:
      - broker-config:/etc/config:ro
      - broker-overlay:/etc/overlay
    restart: unless-stopped

  0g-serving-provider-event:
    environment:
      - CONFIG_OVERLAY_FILE=/etc/overlay/overlay.yaml
    volumes:
      - broker-overlay:/etc/overlay:ro
    restart: unless-stopped

volumes:
  broker-overlay:
```

The endpoint exists only when both `CONFIG_OVERLAY_FILE` and `ZG_ADMIN_ADDRESSES` are set on
the broker; otherwise `/v1/admin/overlay` is 404. It is served on the broker's port, behind
the ingress — no extra port is published.

Use admin keys that are not the contract owner key and not a provider key.

## Authentication

Two headers on every request:

- `X-ZG-Admin-Token`: base64url (no padding) of a JSON object
  ```json
  {"address":"0xAdmin1","method":"PUT","path":"/v1/admin/overlay",
   "bodySha256":"<hex sha256 of the exact request body>",
   "issuedAt":1762850000000,"expiresAt":1762850120000,"nonce":"<random>"}
  ```
  `issuedAt` / `expiresAt` are unix milliseconds; `expiresAt - issuedAt` must be at most
  5 minutes. For `GET` the body is empty (its sha256 is
  `e3b0c442…b855`).
- `X-ZG-Admin-Signature`: EIP-191 `personal_sign` by `address` over the token's JSON bytes
  (the decoded bytes, exactly as encoded), hex with `0x`.

The broker checks the address is listed, the token names this method, path and body, the
time window, the signature, and that the nonce has not been used. Every failure is `401
unauthorized` to the caller, with the reason in the broker log.

## What happens after a PUT

1. The broker merges the overlay with the main config and runs the same validation it runs at
   start; a failure is `400` and nothing is written.
2. The file is replaced atomically and the change is logged (`[ADMIN-OVERLAY] <admin> set
   the overlay (sha256 …): <keys>`). The response is `202` with the sha256 and the keys set.
3. Within ~10 seconds the broker and the event service notice the new file and shut down
   gracefully; `restart: unless-stopped` starts them on it. The broker gives in-flight
   requests 30 seconds — a stream longer than that is cut. The engine keeps running.
4. On start the broker publishes the new prices on-chain as it does on every start.

## Things to know

- The overlay volume outlives the CVM's restarts and redeploys. A redeploy with a new main
  config still gets the old overlay on top; if the new main config dropped a model the
  overlay names, the broker refuses to start. Clear the overlay (`PUT {}`) before such a
  redeploy, or fold its values into the main config.
- Extra provider instances (`CONFIG_DIR`) do not read the overlay.
- A verifier does not need to know the overlay exists: nothing it can change affects where a
  prompt goes or who can read it.
