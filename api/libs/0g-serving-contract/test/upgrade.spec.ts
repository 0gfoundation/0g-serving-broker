import { HardhatEthersSigner } from "@nomicfoundation/hardhat-ethers/signers";
import { expect } from "chai";
import { deployments, ethers, getNamedAccounts } from "hardhat";
import { Deployment } from "hardhat-deploy/types";
import { HardhatRuntimeEnvironment } from "hardhat/types";
import { beforeEach } from "mocha";
import { upgradeImplementation } from "../src/utils/utils";
import { publicKey } from "../src/utils/zk_settlement_calldata/golden";
import { Serving, ServingV2 } from "../typechain-types";
import { AccountStructOutput, ServiceStructOutput } from "../typechain-types/contracts/Serving.sol/Serving";

describe("Upgrade Serving", () => {
    let serving: Serving, servingV2: ServingV2;
    let servingDeployment: Deployment, beaconDeployment: Deployment;
    let owner: HardhatEthersSigner,
        user1: HardhatEthersSigner,
        provider1: HardhatEthersSigner,
        provider2: HardhatEthersSigner;
    let ownerAddress: string, user1Address: string, provider1Address: string, provider2Address: string;

    const ownerInitialBalance = 1000;
    const user1InitialBalance = 2000;

    const provider1Name = "test-provider-1";
    const provider1Type = "HTTP";
    const provider1InputPrice = 100;
    const provider1OutputPrice = 100;
    const provider1Url = "https://example-1.com";

    const provider2Name = "test-provider-2";
    const provider2Type = "HTTP";
    const provider2InputPrice = 100;
    const provider2OutputPrice = 100;
    const provider2Url = "https://example-2.com";

    beforeEach(async () => {
        await deployments.fixture(["Serving"]);

        [owner, user1, provider1, provider2] = await ethers.getSigners();
        [ownerAddress, user1Address, provider1Address, provider2Address] = await Promise.all([
            owner.getAddress(),
            user1.getAddress(),
            provider1.getAddress(),
            provider2.getAddress(),
        ]);

        beaconDeployment = await deployments.get("UpgradeableBeacon");
        servingDeployment = await deployments.get("Serving");
        serving = await ethers.getContractAt("Serving", servingDeployment.address);

        await Promise.all([
            serving.depositFund(provider1Address, publicKey, { value: ownerInitialBalance }),
            serving.connect(user1).depositFund(provider1Address, publicKey, {
                value: user1InitialBalance,
                from: await user1.getAddress(),
            }),
            serving
                .connect(provider1)
                .addOrUpdateService(
                    provider1Name,
                    provider1Type,
                    provider1Url,
                    provider1InputPrice,
                    provider1OutputPrice
                ),
            serving
                .connect(provider2)
                .addOrUpdateService(
                    provider2Name,
                    provider2Type,
                    provider2Url,
                    provider2InputPrice,
                    provider2OutputPrice
                ),
        ]);
    });

    it("should succeed in getting status set by old contract", async () => {
        await upgradeImplementation(
            { deployments, getNamedAccounts, ethers } as HardhatRuntimeEnvironment,
            "ServingV2",
            beaconDeployment.address
        );
        servingV2 = (await ethers.getContractAt("ServingV2", servingDeployment.address)) as ServingV2;

        const [accounts, services] = await servingV2.getAllData();

        const accountUserAddresses = (accounts as AccountStructOutput[]).map((a) => a.user);
        const accountProviderAddresses = (accounts as AccountStructOutput[]).map((a) => a.provider);
        const accountBalances = (accounts as AccountStructOutput[]).map((a) => a.balance);

        const providerAddresses = (services as ServiceStructOutput[]).map((s) => s.provider);
        const serviceNames = (services as ServiceStructOutput[]).map((s) => s.name);
        const serviceTypes = (services as ServiceStructOutput[]).map((s) => s.serviceType);
        const serviceUrls = (services as ServiceStructOutput[]).map((s) => s.url);
        const serviceInputPrices = (services as ServiceStructOutput[]).map((s) => s.inputPrice);
        const serviceOutputPrices = (services as ServiceStructOutput[]).map((s) => s.outputPrice);
        const serviceUpdatedAts = (services as ServiceStructOutput[]).map((s) => s.updatedAt);

        expect(accountUserAddresses).to.have.members([ownerAddress, user1Address]);
        expect(accountProviderAddresses).to.have.members([provider1Address, provider1Address]);
        expect(accountBalances).to.have.members([BigInt(ownerInitialBalance), BigInt(user1InitialBalance)]);
        expect(providerAddresses).to.have.members([provider1Address, provider2Address]);
        expect(serviceNames).to.have.members([provider1Name, provider2Name]);
        expect(serviceTypes).to.have.members([provider1Type, provider2Type]);
        expect(serviceUrls).to.have.members([provider1Url, provider2Url]);
        expect(serviceInputPrices).to.have.members([BigInt(provider1InputPrice), BigInt(provider2InputPrice)]);
        expect(serviceOutputPrices).to.have.members([BigInt(provider1OutputPrice), BigInt(provider2OutputPrice)]);
        expect(serviceUpdatedAts[0]).to.not.equal(0);
        expect(serviceUpdatedAts[1]).to.not.equal(0);
    });
});
