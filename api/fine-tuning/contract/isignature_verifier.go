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

// ISignatureVerifierMetaData contains all meta data concerning the ISignatureVerifier contract.
var ISignatureVerifierMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"string\",\"name\":\"message\",\"type\":\"string\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"},{\"internalType\":\"address\",\"name\":\"expectedAddress\",\"type\":\"address\"}],\"name\":\"verifySignature\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
}

// ISignatureVerifierABI is the input ABI used to generate the binding from.
// Deprecated: Use ISignatureVerifierMetaData.ABI instead.
var ISignatureVerifierABI = ISignatureVerifierMetaData.ABI

// ISignatureVerifier is an auto generated Go binding around an Ethereum contract.
type ISignatureVerifier struct {
	ISignatureVerifierCaller     // Read-only binding to the contract
	ISignatureVerifierTransactor // Write-only binding to the contract
	ISignatureVerifierFilterer   // Log filterer for contract events
}

// ISignatureVerifierCaller is an auto generated read-only Go binding around an Ethereum contract.
type ISignatureVerifierCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ISignatureVerifierTransactor is an auto generated write-only Go binding around an Ethereum contract.
type ISignatureVerifierTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ISignatureVerifierFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type ISignatureVerifierFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ISignatureVerifierSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type ISignatureVerifierSession struct {
	Contract     *ISignatureVerifier // Generic contract binding to set the session for
	CallOpts     bind.CallOpts       // Call options to use throughout this session
	TransactOpts bind.TransactOpts   // Transaction auth options to use throughout this session
}

// ISignatureVerifierCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type ISignatureVerifierCallerSession struct {
	Contract *ISignatureVerifierCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts             // Call options to use throughout this session
}

// ISignatureVerifierTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type ISignatureVerifierTransactorSession struct {
	Contract     *ISignatureVerifierTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts             // Transaction auth options to use throughout this session
}

// ISignatureVerifierRaw is an auto generated low-level Go binding around an Ethereum contract.
type ISignatureVerifierRaw struct {
	Contract *ISignatureVerifier // Generic contract binding to access the raw methods on
}

// ISignatureVerifierCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type ISignatureVerifierCallerRaw struct {
	Contract *ISignatureVerifierCaller // Generic read-only contract binding to access the raw methods on
}

// ISignatureVerifierTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type ISignatureVerifierTransactorRaw struct {
	Contract *ISignatureVerifierTransactor // Generic write-only contract binding to access the raw methods on
}

// NewISignatureVerifier creates a new instance of ISignatureVerifier, bound to a specific deployed contract.
func NewISignatureVerifier(address common.Address, backend bind.ContractBackend) (*ISignatureVerifier, error) {
	contract, err := bindISignatureVerifier(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &ISignatureVerifier{ISignatureVerifierCaller: ISignatureVerifierCaller{contract: contract}, ISignatureVerifierTransactor: ISignatureVerifierTransactor{contract: contract}, ISignatureVerifierFilterer: ISignatureVerifierFilterer{contract: contract}}, nil
}

// NewISignatureVerifierCaller creates a new read-only instance of ISignatureVerifier, bound to a specific deployed contract.
func NewISignatureVerifierCaller(address common.Address, caller bind.ContractCaller) (*ISignatureVerifierCaller, error) {
	contract, err := bindISignatureVerifier(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ISignatureVerifierCaller{contract: contract}, nil
}

// NewISignatureVerifierTransactor creates a new write-only instance of ISignatureVerifier, bound to a specific deployed contract.
func NewISignatureVerifierTransactor(address common.Address, transactor bind.ContractTransactor) (*ISignatureVerifierTransactor, error) {
	contract, err := bindISignatureVerifier(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &ISignatureVerifierTransactor{contract: contract}, nil
}

// NewISignatureVerifierFilterer creates a new log filterer instance of ISignatureVerifier, bound to a specific deployed contract.
func NewISignatureVerifierFilterer(address common.Address, filterer bind.ContractFilterer) (*ISignatureVerifierFilterer, error) {
	contract, err := bindISignatureVerifier(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &ISignatureVerifierFilterer{contract: contract}, nil
}

// bindISignatureVerifier binds a generic wrapper to an already deployed contract.
func bindISignatureVerifier(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := ISignatureVerifierMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ISignatureVerifier *ISignatureVerifierRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ISignatureVerifier.Contract.ISignatureVerifierCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ISignatureVerifier *ISignatureVerifierRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ISignatureVerifier.Contract.ISignatureVerifierTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ISignatureVerifier *ISignatureVerifierRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ISignatureVerifier.Contract.ISignatureVerifierTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ISignatureVerifier *ISignatureVerifierCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ISignatureVerifier.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ISignatureVerifier *ISignatureVerifierTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ISignatureVerifier.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ISignatureVerifier *ISignatureVerifierTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ISignatureVerifier.Contract.contract.Transact(opts, method, params...)
}

// VerifySignature is a free data retrieval call binding the contract method 0xc95571a4.
//
// Solidity: function verifySignature(string message, bytes signature, address expectedAddress) view returns(bool)
func (_ISignatureVerifier *ISignatureVerifierCaller) VerifySignature(opts *bind.CallOpts, message string, signature []byte, expectedAddress common.Address) (bool, error) {
	var out []interface{}
	err := _ISignatureVerifier.contract.Call(opts, &out, "verifySignature", message, signature, expectedAddress)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// VerifySignature is a free data retrieval call binding the contract method 0xc95571a4.
//
// Solidity: function verifySignature(string message, bytes signature, address expectedAddress) view returns(bool)
func (_ISignatureVerifier *ISignatureVerifierSession) VerifySignature(message string, signature []byte, expectedAddress common.Address) (bool, error) {
	return _ISignatureVerifier.Contract.VerifySignature(&_ISignatureVerifier.CallOpts, message, signature, expectedAddress)
}

// VerifySignature is a free data retrieval call binding the contract method 0xc95571a4.
//
// Solidity: function verifySignature(string message, bytes signature, address expectedAddress) view returns(bool)
func (_ISignatureVerifier *ISignatureVerifierCallerSession) VerifySignature(message string, signature []byte, expectedAddress common.Address) (bool, error) {
	return _ISignatureVerifier.Contract.VerifySignature(&_ISignatureVerifier.CallOpts, message, signature, expectedAddress)
}
