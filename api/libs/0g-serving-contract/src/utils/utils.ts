import { FACTORY_POSTFIX } from "@typechain/ethers-v6/dist/common";
import { HardhatRuntimeEnvironment } from "hardhat/types";

// We use the Typechain factory class objects to fill the `CONTRACTS` mapping. These objects are used
// by hardhat-deploy to locate compiled contract artifacts. However, an exception occurs if we import
// from Typechain files before they are generated. To avoid this, we follow a two-step process:
//
// 1. We import the types at compile time to ensure type safety. Hardhat does not report an error even
// if these files are not yet generated, as long as the "--typecheck" command-line argument is not used.
import { ContractFactory, ContractRunner, Signer } from "ethers";
import * as TypechainTypes from "../../typechain-types";
// 2. We import the values at runtime and silently ignore any exceptions.
export let Factories = {} as typeof TypechainTypes;
try {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    Factories = require("../../typechain-types") as typeof TypechainTypes;
} catch (err) {
    // ignore
}

interface TypechainFactory<T> {
    new (...args: ConstructorParameters<typeof ContractFactory>): ContractFactory;
    connect: (address: string, runner?: ContractRunner | null) => T;
}

class ContractMeta<T> {
    factory: TypechainFactory<T>;
    /** Deployment name */
    name: string;

    constructor(factory: TypechainFactory<T>, name?: string) {
        this.factory = factory;
        this.name = name ?? this.contractName();
    }

    contractName() {
        // this.factory is undefined when the typechain files are not generated yet

        return this.factory?.name.slice(0, -FACTORY_POSTFIX.length);
    }
}

export const CONTRACTS = {
    Serving: new ContractMeta(Factories.Serving__factory),
    Verifier: new ContractMeta(Factories.Wrapper__factory),
} as const;

const UPGRADEABLE_BEACON = "UpgradeableBeacon";
const BEACON_PROXY = "BeaconProxy";

export async function deployDirectly(
    hre: HardhatRuntimeEnvironment,
    contract: ContractMeta<unknown>,
    args: unknown[] = []
) {
    const { deployments, getNamedAccounts } = hre;
    const { deployer } = await getNamedAccounts();
    // deploy implementation
    await deployments.deploy(contract.name, {
        from: deployer,
        contract: contract.contractName(),
        args: args,
        log: true,
    });
}

export async function deployInBeaconProxy(
    hre: HardhatRuntimeEnvironment,
    contract: ContractMeta<unknown>,
    args: unknown[] = []
) {
    const { deployments, getNamedAccounts } = hre;
    const { deployer } = await getNamedAccounts();
    // deploy implementation
    await deployments.deploy(`${contract.name}Impl`, {
        from: deployer,
        contract: contract.contractName(),
        args: args,
        log: true,
    });
    const implementation = await hre.ethers.getContract(`${contract.name}Impl`);
    // deploy beacon
    const upgradeableBeacon = await deployments.deploy(`${contract.name}Beacon`, {
        from: deployer,
        contract: UPGRADEABLE_BEACON,
        args: [await implementation.getAddress()],
        log: true,
    });
    const beacon = await hre.ethers.getContract(`${contract.name}Beacon`);
    // deploy proxy
    const proxy = await deployments.deploy(contract.name, {
        from: deployer,
        contract: BEACON_PROXY,
        args: [await beacon.getAddress(), []],
        log: true,
    });

    const beaconArtifact = await deployments.getArtifact("UpgradeableBeacon");
    const proxyArtifact = await deployments.getArtifact(contract.name);

    await deployments.save("UpgradeableBeacon", { ...beaconArtifact, address: upgradeableBeacon.address });
    await deployments.save(contract.name, { ...proxyArtifact, address: proxy.address });
}

export async function upgradeImplementation(
    hre: HardhatRuntimeEnvironment,
    newContractName: string,
    beaconAddress: string
) {
    const { deployments, getNamedAccounts } = hre;
    const { deployer } = await getNamedAccounts();

    // new implantation
    await deployments.deploy(`${newContractName}Impl`, {
        from: deployer,
        contract: newContractName,
        args: [],
        log: true,
    });

    const newImplementation = await hre.ethers.getContract(`${newContractName}Impl`);
    const beacon = await hre.ethers.getContractAt("UpgradeableBeacon", beaconAddress);

    // update beacon address
    await (await beacon.upgradeTo(await newImplementation.getAddress())).wait();

    console.log(
        `Beacon contract at ${beaconAddress} is now pointing to new implementation at ${await newImplementation.getAddress()}`
    );
}

export async function getTypedContract<T>(
    hre: HardhatRuntimeEnvironment,
    contract: ContractMeta<T>,
    signer?: Signer | string
) {
    const address = await (await hre.ethers.getContract(contract.name)).getAddress();
    if (signer === undefined) {
        signer = (await hre.getNamedAccounts()).deployer;
    }
    if (typeof signer === "string") {
        signer = await hre.ethers.getSigner(signer);
    }
    return contract.factory.connect(address, signer);
}
