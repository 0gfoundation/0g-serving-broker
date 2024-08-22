# 0G Serving usage procedure

## Background

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

## Basic Usage Process

1. Provider Starts the Provider Agent:

   ```sh
   # Assuming it will be listening on 127.0.0.1:3080
   ```

1. User Starts the User Agent:

   ```sh
   # Assuming it will be listening on 127.0.0.1:1034
   ```

1. Provider Registers the Service with the Provider Agent:

   The serving system now support type of services: inference and 0G Storage. Service type of first one is `chatbot`, and that of the second one is `zgStorage`

   ```sh
   curl -X POST http://127.0.0.1:3080/v1/service \
   -H "Content-Type: application/json" \
   -d '{
           "URL": "http://192.168.1.1:8080",
           "inputPrice": 1,
           "outputPrice": 2,
           "Type": "chatbot",
           "Name": "llama7b"
   }'
   ```

   - `inputPrice` and `outputPrice` have different meanings depending on the service type. For chatbots, they represent the cost per token. For storage services, they are prices per unit of network traffic. The price unit is Wei.

1. User Checks Available Services:

   ```sh
   curl -X GET http://localhost:1034/v1/service
   ```

   An example output might be:

   ```json
   {
     "Metadata": {
       "total": 2
     },
     "Items": [
       {
         "UpdatedAt": "2024-08-22T14:38:52+08:00",
         "Provider": "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
         "Name": "llama7b",
         "Type": "chatbot",
         "URL": "http://192.168.1.1:8080",
         "InputPrice": "100000",
         "OutputPrice": "200000"
       },
       {
         "UpdatedAt": "2024-08-22T14:38:55+08:00",
         "Provider": "0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
         "Name": "llama13b",
         "Type": "chatbot",
         "URL": "http://192.168.1.1:8080",
         "InputPrice": "500000",
         "OutputPrice": "600000"
       }
     ]
   }
   ```

1. User Requests Additional Information about the Desired Service [This API is still in development; further discussion regarding the approach is welcome]:

   ```sh
   curl -X GET http://127.0.0.1:1034/v1/provider/<provider_address>/service/<service_name>/description
   ```

1. User Creates an Account for the Desired Service:

   ```sh
   curl -X POST http://127.0.0.1:1034/v1/provider \
   -H "Content-Type: application/json" \
   -d '{
        "provider": "<provider_address>",
        "balance": "10000000"
   }'
   ```

   - The balance unit is neuron. 1 A0GI = 10\*18 neuron.

1. User Utilizes the Provider's Service:

   1. Inference (chatbot)

      ```sh
      curl http://127.0.0.1:1034/v1/provider/<provider_address>/service/<service_name>/<optional_suffix> \
      -H "Content-Type: application/json" \
      -d '{
           "messages": [{"role": "user", "content": "Say this is a test!"}],
           "temperature": 0.7
      }'
      ```

   2. 0G Storage

      We can refer to the [0G Storage Documentation](https://docs.0g.ai/0g-doc/docs/0g-storage) to access storage via the Command Line Interface [CLI](https://docs.0g.ai/0g-doc/run-a-node/storage-node-cli) or by using the [Go SDK](https://docs.0g.ai/0g-doc/docs/0g-storage/sdk). Simply change the node parameter in the command/SDK to the user agent's getData API route. Here, we'll use the CLI approach as an example:

      Upload file

      ```sh
      ./0g-storage-client upload --url <blockchain_rpc_endpoint>    --contract <log_contract_address> --key <private_key> --node http://localhost:1034/v1/provider/<provider_address>/service/<service_name>  --file <file_path>
      ```

      Download file

      ```sh
      ./0g-storage-client download --node http://localhost:1034/v1/provider/<provider_address>/service/<service_name>  --root <file_root_hash>  --file <output_file_path>
      ```

   - The provider agent will log the requests in its database.

1. Provider Settles the Fee:

   ```sh
   curl -X POST http://127.0.0.1:3080/v1/settle
   ```

   - The provider agent also integrates an engine to automatically settle the fee.

## Additional Operations

1. Provider Updates the Service:

   ```sh
   curl -X PUT http://127.0.0.1:3080/v1/service/<service_name> \
   -H "Content-Type: application/json" \
   -d '{
           "URL": "http://192.168.1.1:8080",
           "inputPrice": 3,
           "outputPrice": 4,
           "Type": "chatbot",
           "Name": "name"
   }'
   ```

1. Provider Deletes the Service:

   ```sh
   curl -X DELETE http://127.0.0.1:3080/v1/service/<service_name>
   ```

1. Provider Checks Accounts that have Sent Requests:

   ```sh
   curl -X GET http://127.0.0.1:3080/v1/user
   ```

   ```sh
   curl -X GET http://127.0.0.1:3080/v1/user/<user_address>
   ```

1. User Checks Provider Accounts:

   - This operation can be used to check the remaining balance.

   ```sh
   curl -X GET http://127.0.0.1:1034/v1/provider
   ```

   ```sh
   curl -X GET http://127.0.0.1:1034/v1/provider/<provider_address>
   ```

1. User Charge a Account:

   ```sh
   curl -X POST http://localhost:1034/v1/provider/<provider_address>/charge \
   -H "Content-Type: application/json" \
   -d '{
           "balance": 10000
   }'
   ```

1. User Requests Refunds

   ```sh
   curl -X POST http://localhost:1034/v1/provider/<provider_address>/refund \
   -H "Content-Type: application/json" \
   -d '{
           "amount": 10000
   }'
   ```
