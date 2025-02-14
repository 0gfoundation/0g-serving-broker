# 0G Serving Network Provider

## Prerequisites

- Docker Compose: 1.27+

## Download the Installation Package

Please visit the [releases page](https://github.com/0glabs/0g-serving-broker/releases) to download and extract the latest version of the installation package.

## Configuration Setup

- Copy the `config.example.yaml` file.
- Modify `servingUrl` to point to your publicly exposed URL.
- Set `privateKeys` to your wallet's private key for the 0G blockchain.
- Save the file as `config.local.yaml`.
- Replace `#PORT#` in `docker-compose.yml` with the port you want to use. It should be the same as the port of `servingUrl` in `config.local.yaml`.

## Start the Provider Broker

```bash
docker compose -f docker-compose.yml up -d
```

The provider broker has an automatic settlement engine that ensures you can collect fees promptly after fine-tuning is completed and acknowledged by the consumer.

## Documentation

Please refer to the [0G Compute Network Provider](https://docs.0g.ai/build-with-0g/compute-network/provider) guide.
