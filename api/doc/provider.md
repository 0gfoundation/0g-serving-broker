# 0G Serving Network Provider Broker

[[_TOC_]]

Service registration and proxying; request validation and automated settlement mechanisms.

## Usage

- Download the 0G Provider Broker package.
- Fill out configuration files and deploy.
- After deployment, use the appropriate script to start registration.

## Configuration

```yaml
interval:
  forceSettlementProcessor: 86400    // Fixed frequency cycle for settlements
  settlementProcessor: 600           // Detection frequency: Check balance risk and auto-settle if needed.
servingUrl: "http://ip:port"  // Public IP address
networks:
  ethereum0g:
    url: "https://evmrpc-testnet.0g.ai"
    chainID: 16600
    privateKeys:
      - aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  // 0G private key
    transactionLimit: 1000000      // Transaction gas limit
    gasEstimationBuffer: 10000     // Transaction gas buffer
service:
  name:
  url:
  serviceType:
  serviceSubtype:
  verifiability:
  inputPrice:
  outputPrice:
```

## settlement
