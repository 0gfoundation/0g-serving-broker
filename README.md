# 0G Serving Agent

## Introduction

The 0G Serving Agent integrates with the [0G Serving Contract](https://github.com/0glabs/0g-serving-contract) to provide a seamless settlement solution for data retrieval services. For example, if a provider has a chatbot service that can be called using the following command:

```sh
curl https://chatbot.com \
-H "Content-Type: application/json" \
-d '{
     "model": "someModel",
     "messages": [{"role": "user", "content": "Say this is a test!"}],
     "temperature": 0.7
}'
```

To upgrade this service into a chargeable one, the provider first initiates the provider agent service locally and registers the original service with the agent. Once registered, the agent will host the service and manage the charging process. Users who wish to access the service can start a user agent service locally and send requests to it just as they would with the original service. The user agent will handle the necessary conversions of requests and responses to comply with the protocol.

## Setup

1. Copy and Modify Configuration File

   Copy and modify the configuration file from the [example](config-example.yaml) to config.yaml.

## Running the Agent

1. Start the Provider Agent

   ```sh
   docker compose -f ./integration/provider/docker-compose.yml up -d
   ```

   The provider agent will be listening on 127.0.0.1:3080

2. Start the User Agent

   ```sh
   PORT=<PORT> CONFIG_FILE=<path_to_config> go run main.go 0g-user-server
   docker compose -f ./integration/user/docker-compose.yml up -d
   ```

   The user agent will be listening on 127.0.0.1:1034

## Basic Usage Process

1. Provider Registers the Service with the Provider Agent:

   ```sh
   curl -X POST http://127.0.0.1:3080>/v1/service \
   -H "Content-Type: application/json" \
   -d '{
        "URL": "https://chatbot.com",
        "inputPrice": 1,
        "outputPrice": 2,
        "Type": "chatbot",
        "Name": "chargeableChat"
   }'
   ```

2. User Creates an Account:
   The user creates an provider account to access the services registered by the provider.

   ```sh
   curl -X POST http://127.0.0.1:1034/v1/provider \
   -H "Content-Type: application/json" \
   -d '{
     "provider": "<provider_address>",
     "balance": "<balance>"
   }'
   ```

3. User Calls a Provider's Service for Several Rounds:
   The provider agent will record the requests in the database.

   ```sh
   curl http://127.0.0.1:1034/v1/provider/<provider_address>/service/<service_name>/<optional_suffix> \
   -H "Content-Type: application/json" \
   -d '{
     "model": "someModel",
     "messages": [{"role": "user", "content": "Say this is a test!"}],
     "temperature": 0.7
   }'
   ```

4. Provider Settles the Fee:

   ```sh
   curl -X POST http://127.0.0.1:3080/v1/settle
   ```

5. Provider Deletes the Service:

   ```sh
   curl -X DELETE http://127.0.0.1:3080/v1/service/<service_name>
   ```

6. User Checks Remaining Balance:

   ```sh
   curl -X GET http://127.0.0.1:1034/v1/provider
   ```
