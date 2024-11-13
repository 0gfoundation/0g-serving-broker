# 0G Serving Network

[[_TOC_]]

This document focuses on aligning product design without delving into technical specifics. It covers:

- Objectives
- Serving Network Design Protocol
- Product Structure
- Integration of LLM Inference and Storage Registry Services into the serving network (i.e., their implementation of the design protocol)

For detailed interface and usage information, refer to the component design document:

- [Provider Broker](./provider.md#0g-serving-network-provider-broker)
- [Chat service SDK](./sdk-chat.md#0g-serving-network-sdk-for-chat-service)
- [Storage registry SDK](./sdk-storage.md#0g-serving-network-sdk-for-storage-registry)
- Router API (currently in development)
- Retail Page (currently in development)

## Objectives

The 0G Serving Network is designed to bridge service providers with service users. Providers can offer a range of AI services, like LLM inference, and model and dataset downloads. Customers can engage with these services in three roles: users, developers, or architects.

### For Providers

1. Ensure a versatile settlement system.

### For Customers

1. Offer varied access levels to services:
   - **Customer as User**: Engage directly with AI services via a Retail UI.
   - **Customer as Developer**: Utilize OpenAI-compatible APIs with high availability through a router server.
   - **Customer as Architect**: Customize applications by incorporating provider services using an SDK.
1. Ensure service verification for reliability and validity.

## Terminology

- **Provider**: The service provider.
- **Retail UI**: The user interface for direct service access, such as chat and model downloads.
- **Router**: A 0G server that serves as an intermediary between providers and users.
- **Customer**: A general term encompassing users, developers, and architects.
  - **User**: Interacts with services through the Retail UI.
  - **Developer**: Engages with services via the 0G Router API.
  - **Architect**: Directly interacts with Provider Services using the SDK.

## Protocol

Various services should reuse the same rules for contracts, ZK components, and provider broker components to standardize processes and minimize redundant development.

### Account Setup

- Customers must create an account with the 0G Serving Contract to use services.
- The account is identified by a combination of "customer address + provider address".
- Customers must preemptively deposit 0G tokens before accessing services.
- Upon account creation, a key pair is generated:
  - Customers sign requests using their private key.
  - Providers verify requests with the corresponding public key.
  - The contract uses the public key to verify requests during settlements.

### Service Registration

Providers register their services using the contract's registration function. Services metadata:

| Field Name            | Description                                                                                                                                        |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| Name                  | The name of the service. A service is identified by a combination of "provider address + name".                                                    |
| URL                   | A public IP address is used to accept requests from customers.                                                                                     |
| Service Type          | Categories of services include:<br>Chat (Text-to-Text LLM inference)<br>Model Download<br>Dataset Download                                         |
| AdditionalProperties  | JSON structure stored as a string in the contract, allowing varied attributes for different service types.                                         |
| Verifiability         | Verification methods:<br>Chat (OpML, TeeML)<br>Model/Dataset Download (details pending)                                                            |
| InputPrice            | Optional, specifies the cost per request unit, e.g., per token in chat scenarios.                                                                  |
| OutputPrice           | Optional, specifies the cost per response unit, e.g., per token in chat scenarios.                                                                 |

### Settlement

- Communication between customer and provider occurs over HTTP, with customer-signed requests serving as settlement receipts. Providers can submit these to the contract for settlement.
- Cost Calculation: Quantify the request/response into a single value, then multiply by the unit price.

  1. LLM chat: number of tokens \* price per token
  1. Model download: number of bytes downloaded \* price per byte

- Cost for each round: Current request cost + previous response cost. For example:

  1. LLM chat: Current question token cost + previous answer token cost
  1. Model download: Current request cost (0) + previous download cost

- Based on the above, the first and last requests in a sequence can be exploited:

  1. First: Customer sends a signed request, but the provider doesn't respond.
  1. Last: Provider responds, but the customer stops sending requests, so the provider's response can't be charged.

     To prevent this, serving network requests should be small. For instance, in model download services, the provider side should split the model into small parts, transfer them multiple times, and reassemble them on the customer side.

### Settlement Process

- Customers and providers exchange data via HTTP, using customer-signed requests as settlement receipts. Providers can then submit these for payment.
- Cost Calculation: Transform the request/response into a value and multiply by the unit price.

  1. LLM chat: Number of tokens \* price per token
  1. Model download: Bytes downloaded \* price per byte
  1. Each request calculates cost as: Current request cost + cost of previous response. For example:
     1. LLM chat: Cost of current question tokens + cost of previous response tokens
     1. Model download: Cost of current request (in this case is 0) + cost of previous download

- Potential Exploits:

  1. Initial request in a sequence: The customer sends a signed request, but the provider doesn't respond.
  1. Final request in a sequence: The provider responds, but the customer stops sending requests, preventing the provider's response from being invoiced.

  To prevent these issues, keep request sizes small. For model downloads, providers should split the model into smaller parts, send them sequentially, and reassemble them on the customer's end.

### Service Verification

- LLM inference

  To be decided

- Model/dataset download

  To be decided

## Product Structure

Structured as a three-layer architecture to reflect different customer needs and capabilities:

### Contract Layer [For Architects]

![architecture](./image/architecture.png)

- No server involvement; data is acquired from the contract.
- Architects conduct P2P communication with providers.

#### usage

1. Providers utilize the [0G Provider Broker](./provider.md#0g-serving-network-provider-broker) for service registration and settlement.
2. Architects use the [0G SDK](./sdk.md#0g-serving-network-sdk) to manage accounts and query services.

### Router Layer [For Developers]

- Not Included in v0.1
- Provides OpenAI-compatible, high-availability API interfaces via a router server
- Built on the [Contract Layer](#contract-layer-for-architects)

### Retail Layer [For Users]

- Not included in v0.1
- Offers a user-friendly interface for direct service access.
- Built on the [Router Layer](#router-layer-for-developers)

## Integration

### LLM Inference

#### LLM Inference Workflow

#### Design Protocol for Implementing LLM Inference

### Storage Registry

#### Storage Registry Workflow

#### Design Protocol for Implementing Storage Registry
