import { anyValue } from "@nomicfoundation/hardhat-chai-matchers/withArgs";
import { HardhatEthersSigner } from "@nomicfoundation/hardhat-ethers/signers";
import { time } from "@nomicfoundation/hardhat-toolbox/network-helpers";
import { expect } from "chai";
import { Block, ContractTransactionResponse, TransactionReceipt } from "ethers";
import { deployments, ethers } from "hardhat";
import { Deployment } from "hardhat-deploy/types";
import { beforeEach } from "mocha";
import {
    privateKey,
    publicKey,
    succeedFee,
    succeedInProof,
    succeedProofInputs,
} from "../src/utils/zk_settlement_calldata/golden";
import {
    doubleSpendingInProof,
    doubleSpendingProofInputs,
} from "../src/utils/zk_settlement_calldata/golden/double_spending";
import {
    insufficientBalanceInProof,
    insufficientBalanceProofInputs,
} from "../src/utils/zk_settlement_calldata/golden/insufficient_balance";
import { Serving } from "../typechain-types";
import {
    AccountStructOutput,
    ServiceStructOutput,
    VerifierInputStruct,
} from "../typechain-types/contracts/Serving.sol/Serving";

describe("Serving", () => {
    let serving: Serving;
    let servingDeployment: Deployment;
    let owner: HardhatEthersSigner,
        user1: HardhatEthersSigner,
        provider1: HardhatEthersSigner,
        provider2: HardhatEthersSigner;
    let ownerAddress: string, user1Address: string, provider1Address: string, provider2Address: string;

    const ownerInitialBalance = 1000;
    const user1InitialBalance = 2000;
    const lockTime = 24 * 60 * 60;

    const provider1ServiceName = "test-provider-1";
    const provider1ServiceType = "HTTP";
    const provider1InputPrice = 100;
    const provider1OutputPrice = 100;
    const provider1Url = "https://example-1.com";

    const provider2ServiceName = "test-provider-2";
    const provider2ServiceType = "HTTP";
    const provider2InputPrice = 100;
    const provider2OutputPrice = 100;
    const provider2Url = "https://example-2.com";

    beforeEach(async () => {
        await deployments.fixture(["Serving"]);
        servingDeployment = await deployments.get("Serving");
        serving = await ethers.getContractAt("Serving", servingDeployment.address);

        [owner, user1, provider1, provider2] = await ethers.getSigners();
        [ownerAddress, user1Address, provider1Address, provider2Address] = await Promise.all([
            owner.getAddress(),
            user1.getAddress(),
            provider1.getAddress(),
            provider2.getAddress(),
        ]);
    });

    beforeEach(async () => {
        const initializations: ContractTransactionResponse[] = await Promise.all([
            serving.depositFund(provider1Address, publicKey, { value: ownerInitialBalance }),
            serving.connect(user1).depositFund(provider1Address, publicKey, { value: user1InitialBalance }),
            serving
                .connect(provider1)
                .addOrUpdateService(
                    provider1ServiceName,
                    provider1ServiceType,
                    provider1Url,
                    provider1InputPrice,
                    provider1OutputPrice
                ),
            serving
                .connect(provider2)
                .addOrUpdateService(
                    provider2ServiceName,
                    provider2ServiceType,
                    provider2Url,
                    provider2InputPrice,
                    provider2OutputPrice
                ),
        ]);

        await initializations[2].wait();
    });

    describe("Owner", () => {
        it("should succeed in updating lock time succeed", async () => {
            const updatedLockTime = 2 * 24 * 60 * 60;
            await expect(serving.connect(owner).updateLockTime(updatedLockTime)).not.to.be.reverted;

            const result = await serving.lockTime();
            expect(result).to.equal(BigInt(updatedLockTime));
        });
    });

    describe("User", () => {
        it("should fail to update the lock time if it is not the owner", async () => {
            const updatedLockTime = 2 * 24 * 60 * 60;
            await expect(serving.connect(user1).updateLockTime(updatedLockTime)).to.be.reverted;
            const result = await serving.lockTime();
            expect(result).to.equal(BigInt(lockTime));
        });

        it("should deposit fund and update balance", async () => {
            const depositAmount = 1000;
            await serving.depositFund(provider1Address, privateKey, { value: depositAmount });

            const account = await serving.getAccount(ownerAddress, provider1);
            expect(account.balance).to.equal(BigInt(ownerInitialBalance + depositAmount));
        });

        it("should get all users", async () => {
            const accounts = await serving.getAllAccounts();
            const userAddresses = (accounts as AccountStructOutput[]).map((a) => a.user);
            const providerAddresses = (accounts as AccountStructOutput[]).map((a) => a.provider);
            const balances = (accounts as AccountStructOutput[]).map((a) => a.balance);

            expect(userAddresses).to.have.members([ownerAddress, user1Address]);
            expect(providerAddresses).to.have.members([provider1Address, provider1Address]);
            expect(balances).to.have.members([BigInt(ownerInitialBalance), BigInt(user1InitialBalance)]);
        });
    });

    describe("Process refund", () => {
        let unlockTime: number, refundIndex1: bigint, refundIndex2: bigint;
        const refundAmount1 = 100;
        const refundAmount2 = 200;

        beforeEach(async () => {
            const res1 = await serving.requestRefund(provider1, refundAmount1);
            await res1.wait();
            const res2 = await serving.requestRefund(provider1, refundAmount2);
            const receipt = await res2.wait();

            const block = await ethers.provider.getBlock((receipt as TransactionReceipt).blockNumber);
            unlockTime = (block as Block).timestamp + lockTime;
            refundIndex1 = (await serving.queryFilter(serving.filters.RefundRequested, -1))[0].args[2];
            refundIndex2 = (await serving.queryFilter(serving.filters.RefundRequested, -1))[1].args[2];
        });

        it("should revert if called too soon", async () => {
            await expect(serving.processRefund(provider1, [refundIndex1, refundIndex2])).to.be.reverted;
        });

        it("should succeeded if the unlockTime has arrived and called", async () => {
            await time.increaseTo(unlockTime);

            await expect(serving.processRefund(provider1, [refundIndex1, refundIndex2])).not.to.be.reverted;
            const account = await serving.getAccount(ownerAddress, provider1);
            expect(account.balance).to.be.equal(BigInt(ownerInitialBalance - refundAmount1 - refundAmount2));
        });
    });

    describe("Service provider", () => {
        it("should get service", async () => {
            const service = await serving.getService(provider1Address, provider1ServiceName);

            expect(service.serviceType).to.equal(provider1ServiceType);
            expect(service.url).to.equal(provider1Url);
            expect(service.inputPrice).to.equal(provider1InputPrice);
            expect(service.outputPrice).to.equal(provider1OutputPrice);
            expect(service.updatedAt).to.not.equal(0);
        });

        it("should get all services", async () => {
            const services = await serving.getAllServices();
            const addresses = (services as ServiceStructOutput[]).map((s) => s.provider);
            const names = (services as ServiceStructOutput[]).map((s) => s.name);
            const serviceTypes = (services as ServiceStructOutput[]).map((s) => s.serviceType);
            const urls = (services as ServiceStructOutput[]).map((s) => s.url);
            const inputPrices = (services as ServiceStructOutput[]).map((s) => s.inputPrice);
            const outputPrices = (services as ServiceStructOutput[]).map((s) => s.outputPrice);
            const updatedAts = (services as ServiceStructOutput[]).map((s) => s.updatedAt);

            expect(addresses).to.have.members([provider1Address, provider2Address]);
            expect(names).to.have.members([provider1ServiceName, provider2ServiceName]);
            expect(serviceTypes).to.have.members([provider1ServiceType, provider2ServiceType]);
            expect(urls).to.have.members([provider1Url, provider2Url]);
            expect(inputPrices).to.have.members([BigInt(provider1InputPrice), BigInt(provider2InputPrice)]);
            expect(outputPrices).to.have.members([BigInt(provider1OutputPrice), BigInt(provider2OutputPrice)]);
            expect(updatedAts[0]).to.not.equal(0);
            expect(updatedAts[1]).to.not.equal(0);
        });

        it("should update service", async () => {
            const modifiedServiceType = "RPC";
            const modifiedPriceUrl = "https://example-modified.com";
            const modifiedInputPrice = 200;
            const modifiedOutputPrice = 300;

            await expect(
                serving
                    .connect(provider1)
                    .addOrUpdateService(
                        provider1ServiceName,
                        modifiedServiceType,
                        modifiedPriceUrl,
                        modifiedInputPrice,
                        modifiedOutputPrice
                    )
            )
                .to.emit(serving, "ServiceUpdated")
                .withArgs(
                    provider1Address,
                    "0x" + Buffer.from(provider1ServiceName).toString("hex"),
                    modifiedServiceType,
                    modifiedPriceUrl,
                    modifiedInputPrice,
                    modifiedOutputPrice,
                    anyValue
                );

            const service = await serving.getService(provider1Address, provider1ServiceName);

            expect(service.serviceType).to.equal(modifiedServiceType);
            expect(service.url).to.equal(modifiedPriceUrl);
            expect(service.inputPrice).to.equal(modifiedInputPrice);
            expect(service.outputPrice).to.equal(modifiedOutputPrice);
            expect(service.updatedAt).to.not.equal(0);
        });

        it("should remove service correctly", async function () {
            await expect(serving.connect(provider1).removeService(provider1ServiceName))
                .to.emit(serving, "ServiceRemoved")
                .withArgs(provider1Address, "0x" + Buffer.from(provider1ServiceName).toString("hex"));

            const services = await serving.getAllServices();
            expect(services.length).to.equal(1);
        });
    });

    describe("Settle fees", () => {
        it("should succeed", async () => {
            const verifierInput: VerifierInputStruct = {
                inProof: succeedInProof,
                proofInputs: succeedProofInputs,
                numChunks: BigInt(2),
                segmentSize: [BigInt(7), BigInt(7)],
            };

            await expect(serving.connect(provider1).settleFees(verifierInput))
                .to.emit(serving, "BalanceUpdated")
                .withArgs(ownerAddress, provider1Address, ownerInitialBalance - succeedFee, 0)
                .and.to.emit(serving, "BalanceUpdated")
                .withArgs(user1Address, provider1Address, user1InitialBalance - succeedFee, 0);
        });

        it("should failed due to double spending", async () => {
            const verifierInput: VerifierInputStruct = {
                inProof: doubleSpendingInProof,
                proofInputs: doubleSpendingProofInputs,
                numChunks: BigInt(2),
                segmentSize: [BigInt(14)],
            };

            await expect(serving.connect(provider1).settleFees(verifierInput)).to.be.reverted;
        });

        it("should failed due to insufficient balance", async () => {
            const verifierInput: VerifierInputStruct = {
                inProof: insufficientBalanceInProof,
                proofInputs: insufficientBalanceProofInputs,
                numChunks: BigInt(1),
                segmentSize: [BigInt(7)],
            };

            await expect(serving.connect(provider1).settleFees(verifierInput)).to.be.reverted;
        });
    });
});
