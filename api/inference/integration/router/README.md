# High-Availability 0G Router Service

This Docker container provides a high-availability router service for 0G inference with multiple levels of failover:

1. **Provider-level failover**: Automatically switches between different inference providers
2. **Key-level failover**: Runs multiple router instances with different private keys for maximum reliability

## Features

- ✅ Multi-provider support with automatic failover (on-chain and direct endpoints)
- ✅ Priority-based request routing
- ✅ Direct endpoint support (OpenAI, Anthropic, Fireworks, etc.)
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

# For on-chain providers only
docker run -d \
  --name 0g-router \
  -p 3000:3000 \
  -e PROVIDERS="0xProvider1,10;0xProvider2,20;0xProvider3,30" \
  -e KEYS="0x1234...,0x5678...,0x9abc..." \
  -e RPC_ENDPOINT="https://evmrpc-testnet.0g.ai" \
  0g-router-ha

# For mixed providers (on-chain + direct endpoints)
docker run -d \
  --name 0g-router \
  -p 3000:3000 \
  -e PROVIDERS="0xProvider1,20;0xProvider2,30" \
  -e DIRECT_ENDPOINTS="openai,https://api.openai.com/v1,sk-key,gpt-4,10;fireworks,https://api.fireworks.ai/inference/v1,fw-key,llama-model,5" \
  -e KEYS="0x1234...,0x5678..." \
  -e RPC_ENDPOINT="https://evmrpc-testnet.0g.ai" \
  0g-router-ha

# For direct endpoints only (no blockchain required)
docker run -d \
  --name 0g-router \
  -p 3000:3000 \
  -e DIRECT_ENDPOINTS="openai,https://api.openai.com/v1,sk-key,gpt-4,10;anthropic,https://api.anthropic.com/v1,ant-key,claude-3,15" \
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

**For on-chain providers:**
| Variable | Description | Example |
|----------|-------------|---------|
| `PROVIDERS` | Semicolon-separated provider configs | `address1,priority1;address2,priority2` |
| `KEYS` | Comma-separated private keys for HA | `0x1234...,0x5678...` |

**For direct endpoints (no keys required):**
| Variable | Description | Example |
|----------|-------------|---------|
| `DIRECT_ENDPOINTS` | Semicolon-separated endpoint configs | `id,url,key,model,priority;...` |

### Optional Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DEFAULT_PROVIDER_PRIORITY` | Default priority for on-chain providers | `100` |
| `DEFAULT_ENDPOINT_PRIORITY` | Default priority for direct endpoints | `50` |
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

## Provider Configuration

### On-chain Providers

The `PROVIDERS` environment variable uses semicolon-separated entries with comma-separated fields:

```
address1,priority1;address2,priority2
```

**Fields:**
- `address`: Provider address (e.g., "0x1234567890abcdef...")
- `priority`: Lower number = higher priority (optional, defaults to 100)

**Examples:**
```bash
# Two providers with different priorities
"0x1234567890abcdef,10;0x9876543210fedcba,20"

# Single provider with default priority
"0x1234567890abcdef"
```

### Direct Endpoint Configuration

The `DIRECT_ENDPOINTS` environment variable uses semicolon-separated entries with comma-separated fields:

```
id,endpoint,apikey,model,priority;id2,endpoint2,apikey2,model2,priority2
```

**Fields:**
- `id`: Endpoint identifier (e.g., "openai", "anthropic")
- `endpoint`: Full API URL
- `apikey`: API key (optional, leave empty for no auth)
- `model`: Model name (optional, defaults to "gpt-3.5-turbo")
- `priority`: Lower number = higher priority (optional, defaults to 50)

**Examples:**
```bash
# OpenAI with priority 10
"openai,https://api.openai.com/v1,sk-xxx,gpt-4,10"

# Fireworks AI with priority 5 (highest priority)
"fireworks,https://api.fireworks.ai/inference/v1,fw-xxx,llama-model,5"

# Local model without auth
"local,http://localhost:8080/v1,,local-llm,90"
```

## Priority System

**Priority Rules:**
- Lower numbers = higher priority (1 > 10 > 100)
- System routes requests to highest priority provider first
- Automatic failover follows priority order
- Defaults: Direct endpoints (50), On-chain providers (100)

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

## Example Configurations

### High-Performance Setup (Direct endpoints priority)
```bash
export DIRECT_ENDPOINTS="openai,https://api.openai.com/v1,sk-xxx,gpt-4,5;anthropic,https://api.anthropic.com/v1,ant-xxx,claude-3,10"
export PROVIDERS="0xBackupProvider1,100;0xBackupProvider2,110"
export KEYS="0xKey1,0xKey2"
```

### Cost-Optimized Setup (On-chain priority)
```bash
export PROVIDERS="0xCheapProvider1,10;0xCheapProvider2,20"
export DIRECT_ENDPOINTS="openai,https://api.openai.com/v1,sk-xxx,gpt-4,100"
export KEYS="0xKey1,0xKey2"
```

### Direct-Only Setup (No blockchain)
```bash
export DIRECT_ENDPOINTS="openai,https://api.openai.com/v1,sk-xxx,gpt-4,10;local,http://localhost:8080/v1,,llama,20"
# No KEYS or PROVIDERS needed
```

## Security Notes

- Store private keys securely (use Docker secrets or external key management)
- Use read-only filesystem mounts when possible
- Monitor for unauthorized access
- Regularly rotate private keys
