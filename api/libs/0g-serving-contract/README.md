# 0G Serving Contract

## Compile

```shell
yarn
yarn compile
```

## Deploy

```shell
yarn deploy zg
```

## Upgrade

```shell
BEACON_ADDRESS=<old beacon address> SERVING_ADDRESS=<proxy address> yarn upgradeBeacon zg
```

## Test

1. Unit Test

    ```shell
    yarn test
    ```

2. Manually Test

    For example, deposit funds, update the contract, and verify the state of the new contract.

    1. Deploy the contract and note down the beacon and proxy addresses from the output

        ```shell
        yarn deploy zg
        ```

    2. Access the Hardhat console and deposit funds:

        ```shell
        yarn hardhat console --network zg
        # Commands in the Hardhat console:
        const Serving = await ethers.getContractFactory("Serving")
        const serving = await Serving.attach("<proxy address>")
        await serving.depositFund("0x0000000000000000000000000000000000000000", { value: 1000 })
        await serving.lockTime()
        ```

    3. Upgrade the contract

        ```shell
        BEACON_ADDRESS=<old beacon address> SERVING_ADDRESS=<proxy address> yarn upgradeBeacon zg
        ```

    4. Re-enter the Hardhat console and check the contract states:

        ```shell
        yarn hardhat console --network zg
        # Commands in the Hardhat console:
        const servingV2 = await ethers.getContractFactory("ServingV2")
        const servingV2 = await servingV2.attach("<proxy address>")
        const [accounts, services] = (await servingV2.getAllData())
        # accounts[0].balance should be equal to 1000
        ```
