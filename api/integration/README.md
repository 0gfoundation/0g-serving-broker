# 0G Serving Usage Procedure

## Background

Please refer to [introduction](./introduction.md).

## Prerequisites

- Docker Compose: 1.27+

## Basic Usage Process

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

2. User Starts the User Broker:

   1. Copy the file [config](./user/config.example.yaml) and make the following modifications:

      - Update `privateKeys` to the private key of your wallet for the 0G blockchain.

   2. Save the modified file as `./user/config.local.yaml`.
   3. Start the user broker

      ```sh
      docker compose -f ./user/docker-compose.yml up -d

      # It costs around a few minutes to start, the broker will be listening on 127.0.0.1:1034
      ```

3. Provider Registers the Service:

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

4. User Checks Available Services:

   ```sh
   curl -X GET http://localhost:1034/v1/service
   ```

   An example output might be:

   ```json
   {
     "Metadata": {
       "total": 1
     },
     "Items": [
       {
         "UpdatedAt": "2024-08-22T14:38:52+08:00",
         "Provider": "<provider_address>",
         "Name": "llama7b",
         "Type": "chatbot",
         "URL": "<provider_publicly_exposed_URL>",
         "InputPrice": "100000",
         "OutputPrice": "200000"
       }
     ]
   }
   ```

   - Please note that the URLs provided to the user differ from those used in the API requests to register services. The former are publicly exposed URLs of the provider broker, while the latter represent the original, unchangeable services proxied by the provider broker.

5. User Creates an Account for the Desired Service:

   ```sh
   curl -X POST http://127.0.0.1:1034/v1/provider \
   -H "Content-Type: application/json" \
   -d '{
        "provider": "<provider_address>",
        "balance": "10000000"
   }'
   ```

   - The created account is specifically for the owner of the desired service, i.e., the provider with the address <provider_address>. This account can be reused if the user wishes to utilize another service offered by this provider..

   - The balance unit is neuron. 1 A0GI = 10\*18 neuron.

6. User Utilizes the Provider's Service:

   1. Inference (chatbot)

      ```sh
      curl http://127.0.0.1:1034/v1/provider/<provider_address>/service/<service_name>/<optional_suffix> \
      -H "Content-Type: application/json" \
      -d '{
           "messages": [{"role": "user", "content": "Say this is a test!"}],
           "temperature": 0.7
      }'
      ```

      - To utilize the services, the basic route in the API endpoint provided by the user broker is `/v1/provider/<provider_address>/service/<service_name>`. An `<optional_suffix>` can be included, as original services may offer multiple endpoints for different purposes.

        For example, if a service supports two endpoints:

        - `root_url/fine_tuning/jobs`
        - `root_url/chat/completions`

        and if the service owner registers the service using `root_url` as the value of `<endpoint_of_the_prepared_service>` in API mentioned before. In this case, users should set the `<optional_suffix>` to either `fine_tuning/jobs` or `chat/completions` to specify which service they want to access.

        Conversely, if the service owner registers the service using `root_url/fine_tuning/jobs`, only one service will be exposed, and users should not include any `<optional_suffix>`.

      - The header and body in the request should adhere to the same usage standards as the original service.

7. Provider Settles the Fee:

   ```sh
   curl -X POST http://127.0.0.1:3080/v1/settle
   ```

   - The provider broker also incorporates an engine that automatically settles fees. This engine follows a specific rule to ensure that all users in debt are charged before their remaining balance becomes insufficient (users can request refunds, but each refund will be temporarily locked). At the same time, it manages the frequency of settlements to avoid incurring excessive gas costs.

## Additional Operations

Please refer [user API document](./user/api.html) and [provider API document](./provider/api.html) for more information.
