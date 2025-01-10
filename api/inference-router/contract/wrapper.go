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

// WrapperMetaData contains all meta data concerning the Wrapper contract.
var WrapperMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"uint256[]\",\"name\":\"in_proof\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256[]\",\"name\":\"proof_inputs\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256\",\"name\":\"num_proofs\",\"type\":\"uint256\"}],\"name\":\"verifyBatch\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"success\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
}

// WrapperABI is the input ABI used to generate the binding from.
// Deprecated: Use WrapperMetaData.ABI instead.
var WrapperABI = WrapperMetaData.ABI

// Wrapper is an auto generated Go binding around an Ethereum contract.
type Wrapper struct {
	WrapperCaller     // Read-only binding to the contract
	WrapperTransactor // Write-only binding to the contract
	WrapperFilterer   // Log filterer for contract events
}

// WrapperCaller is an auto generated read-only Go binding around an Ethereum contract.
type WrapperCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// WrapperTransactor is an auto generated write-only Go binding around an Ethereum contract.
type WrapperTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// WrapperFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type WrapperFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// WrapperSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type WrapperSession struct {
	Contract     *Wrapper          // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// WrapperCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type WrapperCallerSession struct {
	Contract *WrapperCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts  // Call options to use throughout this session
}

// WrapperTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type WrapperTransactorSession struct {
	Contract     *WrapperTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts  // Transaction auth options to use throughout this session
}

// WrapperRaw is an auto generated low-level Go binding around an Ethereum contract.
type WrapperRaw struct {
	Contract *Wrapper // Generic contract binding to access the raw methods on
}

// WrapperCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type WrapperCallerRaw struct {
	Contract *WrapperCaller // Generic read-only contract binding to access the raw methods on
}

// WrapperTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type WrapperTransactorRaw struct {
	Contract *WrapperTransactor // Generic write-only contract binding to access the raw methods on
}

// NewWrapper creates a new instance of Wrapper, bound to a specific deployed contract.
func NewWrapper(address common.Address, backend bind.ContractBackend) (*Wrapper, error) {
	contract, err := bindWrapper(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Wrapper{WrapperCaller: WrapperCaller{contract: contract}, WrapperTransactor: WrapperTransactor{contract: contract}, WrapperFilterer: WrapperFilterer{contract: contract}}, nil
}

// NewWrapperCaller creates a new read-only instance of Wrapper, bound to a specific deployed contract.
func NewWrapperCaller(address common.Address, caller bind.ContractCaller) (*WrapperCaller, error) {
	contract, err := bindWrapper(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &WrapperCaller{contract: contract}, nil
}

// NewWrapperTransactor creates a new write-only instance of Wrapper, bound to a specific deployed contract.
func NewWrapperTransactor(address common.Address, transactor bind.ContractTransactor) (*WrapperTransactor, error) {
	contract, err := bindWrapper(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &WrapperTransactor{contract: contract}, nil
}

// NewWrapperFilterer creates a new log filterer instance of Wrapper, bound to a specific deployed contract.
func NewWrapperFilterer(address common.Address, filterer bind.ContractFilterer) (*WrapperFilterer, error) {
	contract, err := bindWrapper(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &WrapperFilterer{contract: contract}, nil
}

// bindWrapper binds a generic wrapper to an already deployed contract.
func bindWrapper(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := WrapperMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Wrapper *WrapperRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Wrapper.Contract.WrapperCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Wrapper *WrapperRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Wrapper.Contract.WrapperTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Wrapper *WrapperRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Wrapper.Contract.WrapperTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Wrapper *WrapperCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Wrapper.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Wrapper *WrapperTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Wrapper.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Wrapper *WrapperTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Wrapper.Contract.contract.Transact(opts, method, params...)
}

// VerifyBatch is a free data retrieval call binding the contract method 0xad12259a.
//
// Solidity: function verifyBatch(uint256[] in_proof, uint256[] proof_inputs, uint256 num_proofs) view returns(bool success)
func (_Wrapper *WrapperCaller) VerifyBatch(opts *bind.CallOpts, in_proof []*big.Int, proof_inputs []*big.Int, num_proofs *big.Int) (bool, error) {
	var out []interface{}
	err := _Wrapper.contract.Call(opts, &out, "verifyBatch", in_proof, proof_inputs, num_proofs)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// VerifyBatch is a free data retrieval call binding the contract method 0xad12259a.
//
// Solidity: function verifyBatch(uint256[] in_proof, uint256[] proof_inputs, uint256 num_proofs) view returns(bool success)
func (_Wrapper *WrapperSession) VerifyBatch(in_proof []*big.Int, proof_inputs []*big.Int, num_proofs *big.Int) (bool, error) {
	return _Wrapper.Contract.VerifyBatch(&_Wrapper.CallOpts, in_proof, proof_inputs, num_proofs)
}

// VerifyBatch is a free data retrieval call binding the contract method 0xad12259a.
//
// Solidity: function verifyBatch(uint256[] in_proof, uint256[] proof_inputs, uint256 num_proofs) view returns(bool success)
func (_Wrapper *WrapperCallerSession) VerifyBatch(in_proof []*big.Int, proof_inputs []*big.Int, num_proofs *big.Int) (bool, error) {
	return _Wrapper.Contract.VerifyBatch(&_Wrapper.CallOpts, in_proof, proof_inputs, num_proofs)
}
