# 0G Serving usage procedure

## Background

The 0G Serving Agent integrates with the 0G Serving Contract to provide a seamless settlement solution for chargeable data retrieval services. For example, if a provider has a chatbot service that can be called using the following command:

```sh
curl https://chatbot.com \
-H "Content-Type: application/json" \
-d '{
     "model": "someModel",
     "messages": [{"role": "user", "content": "Say this is a test!"}],
     "temperature": 0.7
}'
```

To upgrade this service to a chargeable model, the service provider first initiates a local **provider agent** and registers the original service with this agent. Once registered, the agent hosts the service and manages the billing process.

Users who wish to access the service can initiate a local **user agent** and send requests through it, just as they would with the original service.

The user agent handles the necessary conversion of requests and responses to comply with the protocol. The provider agent then verifies the processed requests sent from the user agent and saves them for later settlement.

For the example mentioned above, after everything is setting up, users who want to use the service: <service_name> from provider with address: <provider_address> could send request like this (assume user agent will be listening on 127.0.0.1:1034):

```sh
curl http://127.0.0.1:1034/v1/provider/<provider_address>/service/<service_name>/<optional_suffix> \
-H "Content-Type: application/json" \
-d '{
  "model": "someModel",
  "messages": [{"role": "user", "content": "Say this is a test!"}],
  "temperature": 0.7
}'
```

## Basic setup

![basic setup](./image/basic-setup.png)

The basic components of serving system includes: provider agent, user agent, and contract.

Provider agent responsibilities:

1. Register, check, update, and delete services
1. Handle incoming requests by:
   1. Verifying and recording requests
   1. Distributing requests to corresponding services
1. Perform settlements using recorded requests as vouchers

User agent responsibilities:

1. Check available services
1. Register, check, deposit, and request refunds to/from provider accounts
1. Handle incoming requests from users by:
   1. Extracting metadata from requests and signing them
   1. Sending the reorganized requests to the provider agent

Contract responsibilities:

1. Store critical variables during the serving process, such as account information (user address, provider address, balance, etc.), and service information (names, URLs, etc.)
1. Include the consensus logic of the serving system, such as:
   1. How to verify request settlements
   1. How to determine the legitimacy of settlement proof (requests)
   1. How users obtain refunds, etc.

Additional explanations:

1. The account mentioned above connects users and providers. Essentially, an account is a storage structure variable in the contract, identifiable by a pair of user and provider addresses. The most important property of an account is its balance, which records the funds available for the user to call the provider's services. In user agent APIs, this is referred to as a provider account because all related accounts share the same user address (set by users in the agent’s configuration) but have different provider addresses. Similarly, in the provider agent, this is referred to as a user account.

1. There is a protocol to determine whether a request is valid. Generally, this involves checking:
   1. Whether the nonce in the request is valid
   1. Whether the service has been modified since the request
   1. Whether the user's account balance is sufficient for payment

When a user agent processes incoming requests, it primarily extracts metadata to prepare for verification. Provider agents also adhere to this protocol to verify requests. The contract uses this protocol to determine the validity of requests when the provider performs settlements.
