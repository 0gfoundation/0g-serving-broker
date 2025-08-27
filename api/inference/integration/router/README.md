# High-Availability 0G Router Service

This Docker container provides a high-availability router service for 0G inference with multiple levels of failover:

1. **Provider-level failover**: Automatically switches between different inference providers
2. **Key-level failover**: Runs multiple router instances with different private keys for maximum reliability

## Features

- ✅ Multi-provider support with automatic failover
- ✅ Multi-key high availability with load balancing
- ✅ Health monitoring and automatic recovery
- ✅ Built-in health check endpoints
- ✅ Graceful shutdown handling
- ✅ Comprehensive logging and monitoring

## Prerequisites

Ensure you have an compute network account and deposit sufficient funds. If not, refer to the [0g-compute-cli documentation](https://docs.0g.ai/build-with-0g/compute-network/cli#create-account)

## Quick Start

### Using Docker Run

```bash
docker build -t 0g-router-ha .

docker run -d \
  --name 0g-router \
  -p 3000:3000 \
  -e PROVIDERS="0xProvider1,0xProvider2,0xProvider3" \
  -e KEYS="0x1234...,0x5678...,0x9abc..." \
  -e RPC_ENDPOINT="https://evmrpc-testnet.0g.ai" \
  0g-router-ha
```

### Using Docker Compose

1. Edit `docker-compose.yml` with your configuration
2. Run the service:

```bash
docker-compose up -d
```

## Configuration

### Required Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `PROVIDERS` | Comma-separated provider addresses | `0xProvider1,0xProvider2` |
| `KEYS` | Comma-separated private keys for HA | `0x1234...,0x5678...` |

### Optional Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `RPC_ENDPOINT` | 0G Chain RPC endpoint | Uses testnet default |
| `LEDGER_CA` | Ledger contract address | Auto-detected |
| `INFERENCE_CA` | Inference contract address | Auto-detected |
| `GAS_PRICE` | Gas price for transactions | Auto |
| `PORT` | Main service port | `3000` |
| `HOST` | Bind host | `0.0.0.0` |

## API Endpoints

### Main Service
- `POST /v1/chat/completions` - Chat completions with automatic failover
- `GET /v1/providers/status` - Provider status (proxied to active instance)

### Monitoring
- `GET /health` - Overall service health
- `GET /status` - Detailed status of all instances

## Architecture

The service runs multiple router instances, each with a different private key:

```
Load Balancer (Port 3000)
├── Router Instance 1 (Port 3100) - Key 1
├── Router Instance 2 (Port 3101) - Key 2
└── Router Instance 3 (Port 3102) - Key 3
```

Each router instance handles provider-level failover, while the load balancer provides key-level failover.

## Health Monitoring

The service provides comprehensive health monitoring:

```bash
# Check overall service health
curl http://localhost:3000/health

# Get detailed status
curl http://localhost:3000/status
```

Example status response:
```json
{
  "status": "healthy",
  "activeInstances": 3,
  "totalInstances": 3,
  "instances": [
    {
      "id": "router-0",
      "healthy": true,
      "port": 3000,
      "uptime": 125000,
      "restartCount": 0,
      "lastHealthCheck": 1640995200000
    }
  ]
}
```

## Legacy Single-Provider Mode

For backward compatibility, you can still run a single provider instance:

```sh
docker run -p 3000:3000 0g-router-server \
    0g-compute-cli inference serve \
    --provider <provider_address> \
    --key <your_key> \
    --port 3000
```

## Troubleshooting

### Common Issues

1. **No providers available**
   - Check provider addresses are correct
   - Verify network connectivity to providers
   - Check private key permissions

2. **Instance startup failures**
   - Verify private keys are valid
   - Check RPC endpoint connectivity
   - Ensure sufficient funds for gas fees

3. **Port conflicts**
   - Change `PORT` environment variable
   - Ensure main port (3000) and instance ports (3100-310X) are available

### Debug Mode

Enable verbose logging:
```bash
docker-compose up --no-detach
```

## Security Notes

- Store private keys securely (use Docker secrets or external key management)
- Use read-only filesystem mounts when possible
- Monitor for unauthorized access
- Regularly rotate private keys
