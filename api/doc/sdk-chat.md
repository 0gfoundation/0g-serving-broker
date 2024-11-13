# 0G Serving Network SDK For Chat Service

**Functions**:

- Generate headers required for settlement.
- Verify the integrity and validity of returned data.
- Support account management.

## TypeScript SDK Interface

Implement:

1. [TypeScript SDK for Web UI Development](https://github.com/0glabs/0g-serving-user-broker)
2. TypeScript SDK for Node.js UI Development (currently under development)

### `createZGServingNetworkBroker`

| Parameter                | Type     | Description                                                                                 |
| ------------------------ | -------- | ------------------------------------------------------------------------------------------- |
| `signer`                 | Signer   | An instance used to sign transactions for a specific Ethereum account.                      |

**Output**

| Type             | Description                                                         |
| ---------------- | ------------------------------------------------------------------- |
| `broker`         | The broker instance, allowing interaction with 0G serving services. |

**Description**

Initializes a broker instance using an ethers.js signer.

---

### `broker.listService`

| Parameter | Type | Description |
| --------- | ---- | ----------- |
| None      | N/A  | N/A         |

**Output**

| Type                             | Description                                                                                                                                   |
| -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `ServiceStructOutput[]`          | An array of service descriptions, each including service information, which is determined by the services defined on the 0G Serving Contract. |

**Description**

Retrieves a list of available services.

---

### `broker.addAccount`

| Parameter         | Type   | Description                                                                          |
| ----------------- | ------ | ------------------------------------------------------------------------------------ |
| `providerAddress` | string | The Ethereum address of the service provider for whom you want to create an account. |

**Output**

| Type         | Description                                                                         |
| ------------ | ----------------------------------------------------------------------------------- |
| void         | No return value. Completion of the promise indicates successful account creation.   |

**Description**

Creates an account for a specific provider.

---

### `broker.depositFund`

| Parameter         | Type   | Description                                                                                     |
| ----------------- | ------ | ----------------------------------------------------------------------------------------------- |
| `providerAddress` | string | The Ethereum address of the service provider.                                                   |
| `amount`          | bigint | The amount to deposit, in the smallest currency unit.                                           |

**Output**

| Type         | Description                                                                       |
| ------------ | --------------------------------------------------------------------------------- |
| void         | No return value. Completion of the promise indicates successful fund deposit.     |

**Description**

Deposits funds into a provider-specific account.

---

### `broker.processRequest`

| Parameter         | Type   | Description                                                                                                         |
| ----------------- | ------ | ------------------------------------------------------------------------------------------------------------------- |
| `providerAddress` | string | The Ethereum address of the service provider.                                                                       |
| `serviceName`     | string | The name of the service to request.                                                                                 |
| `content`         | string | The data to be processed, e.g., user input for a chatbot.                                                           |

**Output**

| Type      | Description                                                                                 |
| --------- | ------------------------------------------------------------------------------------------- |
| `Headers` | An object containing HTTP headers with request cost and user signature information.         |

**Description**

Generates headers to make a valid request to a provider.

---

### `broker.processResponse`

| Parameter         | Type   | Description                                                                                     |
| ----------------- | ------ | ----------------------------------------------------------------------------------------------- |
| `providerAddress` | string | The Ethereum address of the service provider.                                                   |
| `serviceName`     | string | The name of the service that provided the response.                                             |
| `content`         | string | The content of the provider's response.                                                         |
| `chatID`          | string | The chat ID from the provider's response.                                                       |

**Output**

| Type      | Description                                                     |
| --------- | --------------------------------------------------------------- |
| `boolean` | Returns `true` if the response is valid, `false` otherwise.     |

**Description**

Verifies the legitimacy of the provider's response and stores necessary info for future requests.

## Python SDK Interface

(Currently Under Design)
