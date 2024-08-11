// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package contract

import (
	"errors"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Reference imports to suppress errors if they are not otherwise used.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
	_ = abi.ConvertType
)

// IBatchVerifierMetaData contains all meta data concerning the IBatchVerifier contract.
var IBatchVerifierMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"uint256[]\",\"name\":\"inProof\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256[]\",\"name\":\"proofInputs\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256\",\"name\":\"numProofs\",\"type\":\"uint256\"}],\"name\":\"verifyBatch\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
}

// IBatchVerifierABI is the input ABI used to generate the binding from.
// Deprecated: Use IBatchVerifierMetaData.ABI instead.
var IBatchVerifierABI = IBatchVerifierMetaData.ABI

// IBatchVerifier is an auto generated Go binding around an Ethereum contract.
type IBatchVerifier struct {
	IBatchVerifierCaller     // Read-only binding to the contract
	IBatchVerifierTransactor // Write-only binding to the contract
	IBatchVerifierFilterer   // Log filterer for contract events
}

// IBatchVerifierCaller is an auto generated read-only Go binding around an Ethereum contract.
type IBatchVerifierCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IBatchVerifierTransactor is an auto generated write-only Go binding around an Ethereum contract.
type IBatchVerifierTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IBatchVerifierFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type IBatchVerifierFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IBatchVerifierSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type IBatchVerifierSession struct {
	Contract     *IBatchVerifier   // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// IBatchVerifierCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type IBatchVerifierCallerSession struct {
	Contract *IBatchVerifierCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts         // Call options to use throughout this session
}

// IBatchVerifierTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type IBatchVerifierTransactorSession struct {
	Contract     *IBatchVerifierTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts         // Transaction auth options to use throughout this session
}

// IBatchVerifierRaw is an auto generated low-level Go binding around an Ethereum contract.
type IBatchVerifierRaw struct {
	Contract *IBatchVerifier // Generic contract binding to access the raw methods on
}

// IBatchVerifierCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type IBatchVerifierCallerRaw struct {
	Contract *IBatchVerifierCaller // Generic read-only contract binding to access the raw methods on
}

// IBatchVerifierTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type IBatchVerifierTransactorRaw struct {
	Contract *IBatchVerifierTransactor // Generic write-only contract binding to access the raw methods on
}

// NewIBatchVerifier creates a new instance of IBatchVerifier, bound to a specific deployed contract.
func NewIBatchVerifier(address common.Address, backend bind.ContractBackend) (*IBatchVerifier, error) {
	contract, err := bindIBatchVerifier(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &IBatchVerifier{IBatchVerifierCaller: IBatchVerifierCaller{contract: contract}, IBatchVerifierTransactor: IBatchVerifierTransactor{contract: contract}, IBatchVerifierFilterer: IBatchVerifierFilterer{contract: contract}}, nil
}

// NewIBatchVerifierCaller creates a new read-only instance of IBatchVerifier, bound to a specific deployed contract.
func NewIBatchVerifierCaller(address common.Address, caller bind.ContractCaller) (*IBatchVerifierCaller, error) {
	contract, err := bindIBatchVerifier(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &IBatchVerifierCaller{contract: contract}, nil
}

// NewIBatchVerifierTransactor creates a new write-only instance of IBatchVerifier, bound to a specific deployed contract.
func NewIBatchVerifierTransactor(address common.Address, transactor bind.ContractTransactor) (*IBatchVerifierTransactor, error) {
	contract, err := bindIBatchVerifier(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &IBatchVerifierTransactor{contract: contract}, nil
}

// NewIBatchVerifierFilterer creates a new log filterer instance of IBatchVerifier, bound to a specific deployed contract.
func NewIBatchVerifierFilterer(address common.Address, filterer bind.ContractFilterer) (*IBatchVerifierFilterer, error) {
	contract, err := bindIBatchVerifier(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &IBatchVerifierFilterer{contract: contract}, nil
}

// bindIBatchVerifier binds a generic wrapper to an already deployed contract.
func bindIBatchVerifier(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := IBatchVerifierMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IBatchVerifier *IBatchVerifierRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IBatchVerifier.Contract.IBatchVerifierCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IBatchVerifier *IBatchVerifierRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IBatchVerifier.Contract.IBatchVerifierTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IBatchVerifier *IBatchVerifierRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IBatchVerifier.Contract.IBatchVerifierTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IBatchVerifier *IBatchVerifierCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IBatchVerifier.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IBatchVerifier *IBatchVerifierTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IBatchVerifier.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IBatchVerifier *IBatchVerifierTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IBatchVerifier.Contract.contract.Transact(opts, method, params...)
}

// VerifyBatch is a free data retrieval call binding the contract method 0xad12259a.
//
// Solidity: function verifyBatch(uint256[] inProof, uint256[] proofInputs, uint256 numProofs) view returns(bool)
func (_IBatchVerifier *IBatchVerifierCaller) VerifyBatch(opts *bind.CallOpts, inProof []*big.Int, proofInputs []*big.Int, numProofs *big.Int) (bool, error) {
	var out []interface{}
	err := _IBatchVerifier.contract.Call(opts, &out, "verifyBatch", inProof, proofInputs, numProofs)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// VerifyBatch is a free data retrieval call binding the contract method 0xad12259a.
//
// Solidity: function verifyBatch(uint256[] inProof, uint256[] proofInputs, uint256 numProofs) view returns(bool)
func (_IBatchVerifier *IBatchVerifierSession) VerifyBatch(inProof []*big.Int, proofInputs []*big.Int, numProofs *big.Int) (bool, error) {
	return _IBatchVerifier.Contract.VerifyBatch(&_IBatchVerifier.CallOpts, inProof, proofInputs, numProofs)
}

// VerifyBatch is a free data retrieval call binding the contract method 0xad12259a.
//
// Solidity: function verifyBatch(uint256[] inProof, uint256[] proofInputs, uint256 numProofs) view returns(bool)
func (_IBatchVerifier *IBatchVerifierCallerSession) VerifyBatch(inProof []*big.Int, proofInputs []*big.Int, numProofs *big.Int) (bool, error) {
	return _IBatchVerifier.Contract.VerifyBatch(&_IBatchVerifier.CallOpts, inProof, proofInputs, numProofs)
}
