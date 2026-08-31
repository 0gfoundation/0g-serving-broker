package providercontract

import "github.com/ethereum/go-ethereum/accounts/abi"

// ContractABIForTest exposes the parsed ABI so tests in other packages can build
// a faithful revert payload (selector + packed args) instead of hand-rolling a
// stub that only satisfies the code under test.
func ContractABIForTest() *abi.ABI { return contractABI }
