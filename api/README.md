# 0G Serving Broker

## Introduction

The 0G Serving Broker integrates with the [0G Serving Contract](https://github.com/0glabs/0g-serving-contract) to provide a seamless settlement solution for data retrieval services. For example, if a provider has a chatbot service that can be called using the following command:

```sh
curl https://chatbot.com \
-H "Content-Type: application/json" \
-d '{
     "model": "someModel",
     "messages": [{"role": "user", "content": "Say this is a test!"}],
     "temperature": 0.7
}'
```

To upgrade this service into a chargeable one, the provider first initiates the provider broker service locally and registers the original service with the broker. Once registered, the broker will host the service and manage the charging process. Users who wish to access the service can start a user broker service locally and send requests to it just as they would with the original service. The user broker will handle the necessary conversions of requests and responses to comply with the protocol.

## Setup

1. Copy and Modify Configuration File

   Copy and modify the configuration file from the [provider config example](config-example-provider.yaml) to config.yaml.

   The meaning of each setting can be found in the [config example](config-example-all.yaml).

## Running the Broker

1. Start the Provider Broker

   ```sh
   docker compose -f ./integration/provider/docker-compose.yml up -d
   ```

   The provider broker will be listening on 127.0.0.1:3080

## Basic Usage Process

1. Provider Registers the Service with the Provider Broker:

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

2. Provider Settles the Fee:

   ```sh
   curl -X POST http://127.0.0.1:3080/v1/settle
   ```

3. Provider Deletes the Service:

   ```sh
   curl -X DELETE http://127.0.0.1:3080/v1/service/<service_name>
   ```
