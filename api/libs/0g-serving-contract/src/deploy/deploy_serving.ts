import { DeployFunction } from "hardhat-deploy/types";
import { HardhatRuntimeEnvironment } from "hardhat/types";
import { CONTRACTS, deployDirectly, deployInBeaconProxy, getTypedContract } from "../utils/utils";

const lockTime = parseInt(process.env["LOCK_TIME"] || "86400");

const deploy: DeployFunction = async function (hre: HardhatRuntimeEnvironment) {
    const { deployer } = await hre.getNamedAccounts();

    await deployDirectly(hre, CONTRACTS.Verifier);
    const verifier_ = await getTypedContract(hre, CONTRACTS.Verifier);
    const verifierAddress = await verifier_.getAddress();

    await deployInBeaconProxy(hre, CONTRACTS.Serving);

    const serving_ = await getTypedContract(hre, CONTRACTS.Serving);

    if (!(await serving_.initialized())) {
        await (await serving_.initialize(lockTime, verifierAddress, deployer)).wait();
    }
};

deploy.tags = [CONTRACTS.Serving.name];
deploy.dependencies = [];
export default deploy;
