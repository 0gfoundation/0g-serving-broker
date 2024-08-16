// SPDX-License-Identifier: MIT
pragma solidity >=0.8.0 <0.9.0;

import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/structs/EnumerableMap.sol";
import "./utils/Initializable.sol";
import "./Account.sol";
import "./Service.sol";

struct VerifierInput {
    uint[] inProof;
    uint[] proofInputs;
    uint numChunks;
    uint[] segmentSize;
}

interface IBatchVerifier {
    function verifyBatch(
        uint[] calldata inProof,
        uint[] calldata proofInputs,
        uint numProofs
    ) external view returns (bool);
}

contract Serving is Ownable, Initializable {
    using AccountLibrary for AccountLibrary.AccountMap;
    using ServiceLibrary for ServiceLibrary.ServiceMap;

    uint public lockTime;
    address public batchVerifierAddress;
    IBatchVerifier private batchVerifier;
    AccountLibrary.AccountMap private accountMap;
    ServiceLibrary.ServiceMap private serviceMap;

    event BalanceUpdated(address indexed user, address indexed provider, uint amount, uint pendingRefund);
    event RefundRequested(address indexed user, address indexed provider, uint indexed index, uint timestamp);
    event ServiceUpdated(
        address indexed service,
        string indexed name,
        string serviceType,
        string url,
        uint inputPrice,
        uint outputPrice,
        uint updatedAt
    );
    event ServiceRemoved(address indexed service, string indexed name);

    error InvalidProofInputs(string reason);

    function initialize(uint _locktime, address _batchVerifierAddress, address owner) public onlyInitializeOnce {
        _transferOwnership(owner);
        lockTime = _locktime;
        batchVerifierAddress = _batchVerifierAddress;
        batchVerifier = IBatchVerifier(batchVerifierAddress);
    }

    function updateLockTime(uint _locktime) public onlyOwner {
        lockTime = _locktime;
    }

    function updateBatchVerifierAddress(address _batchVerifierAddress) public onlyOwner {
        batchVerifierAddress = _batchVerifierAddress;
        batchVerifier = IBatchVerifier(batchVerifierAddress);
    }

    function getAccount(address user, address provider) public view returns (Account memory) {
        return accountMap.getAccount(user, provider);
    }

    function getAllAccounts() public view returns (Account[] memory) {
        return accountMap.getAllAccounts();
    }

    function depositFund(address provider, uint[2] calldata signer) external payable {
        (uint balance, uint pendingRefund) = accountMap.depositFund(msg.sender, provider, signer, msg.value);
        emit BalanceUpdated(msg.sender, provider, balance, pendingRefund);
    }

    function requestRefund(address provider, uint amount) external {
        uint index = accountMap.requestRefund(msg.sender, provider, amount);
        emit RefundRequested(msg.sender, provider, index, block.timestamp);
    }

    function processRefund(address provider, uint[] calldata indices) external {
        (uint totalAmount, uint balance, uint pendingRefund) = accountMap.processRefund(
            msg.sender,
            provider,
            indices,
            lockTime
        );

        payable(msg.sender).transfer(totalAmount);
        emit BalanceUpdated(msg.sender, provider, balance, pendingRefund);
    }

    function getService(address provider, string memory name) public view returns (Service memory service) {
        service = serviceMap.getService(provider, name);
    }

    function getAllServices() public view returns (Service[] memory services) {
        services = serviceMap.getAllServices();
    }

    function addOrUpdateService(
        string memory name,
        string memory serviceType,
        string calldata url,
        uint inputPrice,
        uint outputPrice
    ) external {
        serviceMap.addOrUpdateService(msg.sender, name, serviceType, url, inputPrice, outputPrice);
        emit ServiceUpdated(msg.sender, name, serviceType, url, inputPrice, outputPrice, block.timestamp);
    }

    function removeService(string memory name) external {
        serviceMap.removeService(msg.sender, name);
        emit ServiceRemoved(msg.sender, name);
    }

    function settleFees(VerifierInput calldata verifierInput) external {
        bool zkPassed = batchVerifier.verifyBatch(
            verifierInput.inProof,
            verifierInput.proofInputs,
            verifierInput.numChunks
        );
        if (!zkPassed) {
            revert InvalidProofInputs("ZK settlement validation failed");
        }

        uint[] memory inputs = verifierInput.proofInputs;
        uint start = 0;
        uint expectedProviderAddress = uint(uint160(msg.sender));

        for (uint segmentIdx = 0; segmentIdx < verifierInput.segmentSize.length; segmentIdx++) {
            uint segmentSize = verifierInput.segmentSize[segmentIdx];
            uint end = start + segmentSize;

            uint totalCosts = 0;
            uint expectedUserAddress = inputs[start];
            uint firstRequestNonce = inputs[start + 2];
            uint lastRequestNonce = inputs[start + 3];
            Account storage account = accountMap.getAccount(address(uint160(expectedUserAddress)), msg.sender);

            if (account.nonce > firstRequestNonce) {
                revert InvalidProofInputs("initial nonce is incorrect");
            }
            for (uint chunkIdx = start; chunkIdx < end; chunkIdx += 7) {
                uint userAddress = inputs[chunkIdx];
                uint providerAddress = inputs[chunkIdx + 1];
                lastRequestNonce = inputs[chunkIdx + 3];
                uint cost = inputs[chunkIdx + 4];
                uint nextChunkFirstRequestNonce = chunkIdx + 9 < end ? inputs[chunkIdx + 9] : 0;

                if (nextChunkFirstRequestNonce != 0 && lastRequestNonce >= nextChunkFirstRequestNonce) {
                    revert InvalidProofInputs("nonce overlapped");
                }

                if (userAddress != expectedUserAddress || providerAddress != expectedProviderAddress) {
                    revert InvalidProofInputs(
                        userAddress != expectedUserAddress
                            ? "user address is incorrect"
                            : "provider address is incorrect"
                    );
                }

                totalCosts += cost;
            }
            if (account.balance < totalCosts) {
                revert InvalidProofInputs("insufficient balance");
            }
            _settleFees(account, totalCosts);
            start = end;
            account.nonce = lastRequestNonce;
        }
        if (start != inputs.length) {
            revert InvalidProofInputs("array segmentSize sum mismatches public input length");
        }
    }

    function _settleFees(Account storage account, uint amount) private {
        if (amount > (account.balance - account.pendingRefund)) {
            uint remainingFee = amount - (account.balance - account.pendingRefund);
            if (account.pendingRefund < remainingFee) {
                revert InvalidProofInputs("insufficient balance in pendingRefund");
            }

            account.pendingRefund -= remainingFee;
            for (int i = int(account.refunds.length - 1); i >= 0; i--) {
                Refund storage refund = account.refunds[uint(i)];
                if (refund.processed) {
                    continue;
                }

                if (refund.amount <= remainingFee) {
                    remainingFee -= refund.amount;
                } else {
                    refund.amount -= remainingFee;
                    remainingFee = 0;
                }

                if (remainingFee == 0) {
                    break;
                }
            }
        }
        account.balance -= amount;
        emit BalanceUpdated(account.user, msg.sender, account.balance, account.pendingRefund);
        payable(msg.sender).transfer(amount);
    }
}
