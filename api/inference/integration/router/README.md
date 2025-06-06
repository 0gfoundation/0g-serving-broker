# 0g-compute-cli Router Docker Image Usage Guide

## prerequisites

Ensure you have an compute network account and deposit sufficient funds. If not, refer to the [0g-compute-cli documentation](https://docs.0g.ai/build-with-0g/compute-network/cli#create-account)

## Build the Image

Run the following command in the project root directory:

```sh
docker build --no-cache -t 0g-router-server api/inference/integration/router
```

## Run the Container

When starting the service, you need to pass the required parameters (`provider` and `key`), while other parameters are optional:

```sh
docker run -p <node_port>:<port> 0g-router-server \
    --provider <provider_address> \
    --key <your_key> \
    --rpc=<rpc_url> \
    --ledger-ca=<ledger_ca> \
    --inference-ca=<inference_ca> \
    --gas-price=<gas_price> \
    --port=<port> \
    --host=<host>
```
