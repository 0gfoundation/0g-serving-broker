# 0G Serving

## Prerequisites

- Docker Compose: 1.27+

## Key Commands

1. Provider Starts the Provider Broker:

   1. Copy the file [config](./provider/config.example.yaml) and make the following modifications:

      - Update `servingUrl` to the publicly exposed URL.
      - Update `privateKeys` to the private key of your wallet for the 0G blockchain.

   2. Save the modified file as `./provider/config.local.yaml`.
   3. Start the provider broker

      ```sh
      docker compose -f ./provider/docker-compose.yml up -d

      # It costs around a few minutes to start, the broker will be listening on 127.0.0.1:3080
      ```

2. Provider Registers the Service:

   The serving system now support type of services: LLM inference and 0G Storage. Service type of first one is `chatbot`, and that of the second one is `zgStorage`. In this document, we will use the example of LLM inference services to illustrate.

   1. A service should be prepared, and it should expose an endpoint.

   2. Register the service using provider broker API

   ```sh
   curl -X POST http://127.0.0.1:3080/v1/service \
   -H "Content-Type: application/json" \
   -d '{
           "URL": "<endpoint_of_the_prepared_service>",
           "inputPrice": 100000,
           "outputPrice": 200000,
           "Type": "chatbot",
           "Name": "llama7b"
   }'
   ```

   - `inputPrice` and `outputPrice` have different meanings depending on the service type. For `chatbot`, they represent the cost per token. For `zgStorage`, they are prices per unit of network traffic. The price unit is neuron.

3. Provider Settles the Fee:

   ```sh
   curl -X POST http://127.0.0.1:3080/v1/settle
   ```

   - The provider broker also incorporates an engine that automatically settles fees. This engine follows a specific rule to ensure that all users in debt are charged before their remaining balance becomes insufficient (users can request refunds, but each refund will be temporarily locked). At the same time, it manages the frequency of settlements to avoid incurring excessive gas costs.

## Other API

Please refer [Provider API](./api.html) for more information.
