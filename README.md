# 0G Serving Broker

## Overview

The 0G Serving Broker enables you to become a provider on the 0G Compute Network. It handles service registration, settlement operations, and proxies user requests for both inference and fine-tuning services.

## Provider Types

### Inference Provider

Transform your AI services into verifiable, revenue-generating endpoints on the 0G Compute Network.

**Benefits:**
- Monetize your GPU infrastructure
- Automated billing and settlements
- Trust through TEE verification

**Prerequisites:**
- Docker Compose 1.27+
- OpenAI-compatible model service
- Wallet with 0G tokens for gas fees

**Service Requirements:**
- Your AI service must implement the [OpenAI API Interface](https://platform.openai.com/docs/api-reference/chat)
- TEE Verification (TeeML) requires:
  - Intel TDX enabled CPU
  - NVIDIA H100 or H200 GPU with TEE support

### Fine-tuning Provider

Offer computing power for model fine-tuning tasks on the 0G Compute Network.

**Prerequisites:**
- Docker and Docker Compose
- TDX-enabled Intel CPU
- Compatible NVIDIA GPU (H100/H200 with TEE support)
- Wallet with 0G tokens for gas fees
- Publicly accessible server

## Quick Start

### Download

Visit the [releases page](https://github.com/0gfoundation/0g-serving-broker/releases) to download the latest version.

### Inference Broker Setup

```bash
# Download and extract
tar -xzf inference-broker.tar.gz
cd inference-broker

# Generate configuration files
./config
```

### Fine-tuning Broker Setup

```bash
# Copy config template
cp config.example.yaml config.local.yaml

# Edit config.local.yaml:
# - Set servingUrl to your publicly accessible URL
# - Set privateKeys with your wallet's private key

# Replace port in docker-compose.yml
sed -i 's/#PORT#/8080/g' docker-compose.yml
```

### TEE Node Setup

For TEE-verified services, you need to set up a TEE node:

- **Option 1:** Follow the [Dstack Getting Started Guide](https://github.com/Dstack-TEE/dstack?tab=readme-ov-file#-getting-started)
- **Option 2:** Follow the [0G-TAPP README](https://github.com/0gfoundation/0g-tapp/blob/main/README.md)

## Troubleshooting

**Broker fails to start:**
- Verify Docker Compose is installed correctly
- Check port availability
- Ensure config.local.yaml syntax is valid
- Review logs: `docker compose logs`

**Service not accessible:**
- Confirm firewall allows incoming connections
- Verify public IP/domain is correct
- Test local service connectivity

**Settlement issues:**
- Check wallet has sufficient gas
- Verify network connectivity
- Monitor settlement logs in broker output

## Documentation

- [Inference Provider Guide](https://docs.0g.ai/build-with-0g/compute-network/inference-provider)
- [Fine-tuning Provider Guide](https://docs.0g.ai/build-with-0g/compute-network/fine-tuning-provider)
- [0G Compute Network SDK](https://docs.0g.ai/build-with-0g/compute-network/sdk)

## Support

- [0G Telegram](https://t.me/web3_0glabs)
- [0G Discord](https://discord.com/invite/0glabs)
