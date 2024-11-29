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

## Key Commands

1. **Register the Service**

   The compute network currently supports `chatbot` services. Additional services are in the pipeline to be released soon.

   ```bash
   curl -X POST http://127.0.0.1:4002/v1/service \
   -d '{
         "url": "<endpoint_of_the_prepared_service>",
         "inputPrice": "10000000",
         "outputPrice": "20000000",
         "type": "chatbot",
         "name": "llama8Bb",
         "model": "llama-3.1-8B-Instruct",
         "verifiability":"TeeML"
   }'
   ```

   - `inputPrice` and `outputPrice` vary by service type, for `chatbot`, they represent the cost per token. The unit is in neuron. 1 A0GI = 1e18 neuron.

2. **Settle the Fee**

   ```bash
   curl -X POST http://127.0.0.1:<PORT>/v1/settle
   ```

   - The provider broker has an automatic settlement engine that ensures you can collect fees promptly before your customer's account balance is insufficient, while also minimizing the frequency of charges to reduce gas consumption.

## Documentation

Please refer to the [0G Compute Network Provider](https://docs.0g.ai/build-with-0g/compute-network/provider) guide.
