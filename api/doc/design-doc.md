# 0G Serving Broker

## Objectives

The Serving Broker (hereafter referred to as the broker) is an HTTP service that can proxy HTTP and RPC requests. Both the user and service provider need to start the broker to proxy communication between each other and with smart contracts. The broker will assist the user/service by verifying requests and constructing request traces during the proxying process. The broker's role in the end-to-end process is as follows:

1. Both the service provider and user start a local broker service: provider/user broker.
2. The service provider sends a request to the provider broker to register a service.
   - Parameters include service type, URL, name, and price. The broker records this information in the database.
   - The provider broker creates the corresponding proxy service based on the service type (RPC/HTTP) and calls a contract function to add the service to the contract. The URL stored in the provider broker's database is the address of the registered service, while the URL stored on the contract is the public address of the provider broker service.
3. The user queries the user broker for registered services, and the user broker retrieves and returns information from the contract.
4. The user selects an appropriate service and sends a request. The request header contains metadata: userAddress, nonce, serviceName, inputFee, previousOutputFee, signature, and createdAt.
5. Upon receiving a request, the provider broker verifies and records the metadata:
   - Whether the signature can be correctly parsed.
   - Check if the userAddress has sufficient balance.
   - Verify if the nonce is valid.
   - Confirm whether the serviceName corresponds to an existing service.
   - Ensure inputFee matches the number of tokens in the request payload (useful for services like chatbots). The provider broker should verify the consistency with the token count in the request body.
   - previousOutputFee is the token count of the previous response (used as a billing basis if it’s included in the user-signed feedback for the current request). The provider broker must compare it to the previous answer data recorded in the database.
   - Ensure createdAt is close to the current time.
   - If all validations pass, the broker constructs the request structure with the metadata, records it in the database, and forwards the request to the proxied service.
6. The service provider submits settlement requests to the broker. The broker constructs a request trace from the request and submits the settlement request to the contract.

## What to Do

1. Support `service provider` to register multiple services via API to the broker.
2. Proxy communication between user and service, validating metadata parameters.
3. Save request trace information in the database for future settlements.
4. Proxy various CRUD operations between user/service provider and contract, including:
   - `owner` modifies the refund window.
   - `user` deposits funds to the contract.
   - `user` requests a refund (requestRefund) and processes a refund after the refund window (processRefund).
   - `service provider` registers `service url/type/price` to the contract.
   - `service provider` modifies `url/price` for a `service type`.
   - `service provider` deletes a `service type`.
   - `service provider` sends multiple request traces with user signatures to the contract for settlement.
   - Query the balance of a `user` account.
   - Query `service url/type/price`.

## What Not to Do

1. Temporary exclusion of listening to contract logs and syncing service/user information to the database.

## Design

### Technical Stack

Data broker serves as the intermediary layer for data exchange between the service provider, user, and smart contracts. It must proxy various types of requests among these entities and support diverse use cases. For example, in a chatbot scenario, user-service communication may use HTTP requests, while communication between a 0G storage client (user) and a storage node (service) may utilize RPC requests. Additionally, the Data broker should support proxying user/service interactions with smart contracts.

Gin is chosen as the HTTP web framework. Essentially, it handles HTTP requests and can flexibly manage and process various types of HTTP requests, including standard RPC requests, regular HTTP requests, and blockchain interactions. Although gRPC gateway was also considered, it supports gRPC and HTTP but gRPC, being based on HTTP/2 and Protocol Buffers, is not compatible with Go's standard `net/rpc` library, making it less suitable for integration with the existing 0G storage node. Hence, it was not chosen.

There are no special requirements for the database; MySQL is currently preferred.

### Demo Architecture

```markdown
project
├── main.py
├── internal
│ ├── config        // Configuration reading
│ ├── contract     // Interaction with serving contract
│ ├── db            // Database migrations
│ ├── error         // Error handling utilities
│ ├── handler     // API definitions and implementations
│ ├── model         // Data types
│ └── proxy         // Proxy implementations
└── libs             // Serving contract repository, used to generate ABI files during development
```

### Process Diagram

![Process Diagram](image/0g-serving-broker.png)

## Test Cases

Please refer to the README.md under the repo for test cases.

## Gas consumption per settlement

| Number of requests in settlement | Gas consumption |
| -------------------------------- | --------------- |
| 0                                | 30059           |
| 10                               | 192553          |
| 20                               | 320426          |
| 30                               | 448717          |
| 40                               | 577653          |
