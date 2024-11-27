# 0G Serving Network Provider

## Overview

The 0G Serving Network Provider integrates with the [0G Serving Network Contract](https://github.com/0glabs/0g-serving-contract) and [0G Serving Network User Broker](https://github.com/0glabs/0g-serving-user-broker) to provide a seamless settlement solution for data retrieval services.

## System Architecture

![architecture](./doc/image/architecture.png)

The provider is a crucial component in the overall architecture of the 0G Serving Network (as shown in the diagram above). It is responsible for service registration, settlement operations, and proxying user requests.

The provider is launched as a container group, with four core components:

- **0g-serving-provider-broker**: Handles service registration, settlement, and request proxying operations.
- **0g-serving-provider-event**: Periodically performs fee settlements to ensure user balances are settled before they run out, while controlling settlement frequency to avoid high gas fees.
- **0g-serving-provider-broker-db**: A database that records service registrations, request information, and more.
- **zk-provider-server**: Verifies user requests to ensure they contain valid signatures.

## Documentation

For detailed steps on how to start the provider, please refer to the [0G Compute Network Provider](https://docs.0g.ai/build-with-0g/compute-network/provider) guide.
If you want to interact with an existing provider, please refer to the [0G Compute Network SDK](https://docs.0g.ai/build-with-0g/compute-network/sdk) guide.

## Support and Additional Resources

We want to do everything we can to help you be successful while working on your contribution and projects. Here you'll find various resources and communities that may help you complete a project or contribute to 0G.

### Communities

- [0G Telegram](https://t.me/web3_0glabs)
- [0G Discord](https://discord.com/invite/0glabs)
