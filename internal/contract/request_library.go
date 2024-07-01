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

// RequestLibraryMetaData contains all meta data concerning the RequestLibrary contract.
var RequestLibraryMetaData = &bind.MetaData{
	ABI: "[]",
}

// RequestLibraryABI is the input ABI used to generate the binding from.
// Deprecated: Use RequestLibraryMetaData.ABI instead.
var RequestLibraryABI = RequestLibraryMetaData.ABI

// RequestLibrary is an auto generated Go binding around an Ethereum contract.
type RequestLibrary struct {
	RequestLibraryCaller     // Read-only binding to the contract
	RequestLibraryTransactor // Write-only binding to the contract
	RequestLibraryFilterer   // Log filterer for contract events
}

// RequestLibraryCaller is an auto generated read-only Go binding around an Ethereum contract.
type RequestLibraryCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// RequestLibraryTransactor is an auto generated write-only Go binding around an Ethereum contract.
type RequestLibraryTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// RequestLibraryFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type RequestLibraryFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// RequestLibrarySession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type RequestLibrarySession struct {
	Contract     *RequestLibrary   // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// RequestLibraryCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type RequestLibraryCallerSession struct {
	Contract *RequestLibraryCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts         // Call options to use throughout this session
}

// RequestLibraryTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type RequestLibraryTransactorSession struct {
	Contract     *RequestLibraryTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts         // Transaction auth options to use throughout this session
}

// RequestLibraryRaw is an auto generated low-level Go binding around an Ethereum contract.
type RequestLibraryRaw struct {
	Contract *RequestLibrary // Generic contract binding to access the raw methods on
}

// RequestLibraryCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type RequestLibraryCallerRaw struct {
	Contract *RequestLibraryCaller // Generic read-only contract binding to access the raw methods on
}

// RequestLibraryTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type RequestLibraryTransactorRaw struct {
	Contract *RequestLibraryTransactor // Generic write-only contract binding to access the raw methods on
}

// NewRequestLibrary creates a new instance of RequestLibrary, bound to a specific deployed contract.
func NewRequestLibrary(address common.Address, backend bind.ContractBackend) (*RequestLibrary, error) {
	contract, err := bindRequestLibrary(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &RequestLibrary{RequestLibraryCaller: RequestLibraryCaller{contract: contract}, RequestLibraryTransactor: RequestLibraryTransactor{contract: contract}, RequestLibraryFilterer: RequestLibraryFilterer{contract: contract}}, nil
}

// NewRequestLibraryCaller creates a new read-only instance of RequestLibrary, bound to a specific deployed contract.
func NewRequestLibraryCaller(address common.Address, caller bind.ContractCaller) (*RequestLibraryCaller, error) {
	contract, err := bindRequestLibrary(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &RequestLibraryCaller{contract: contract}, nil
}

// NewRequestLibraryTransactor creates a new write-only instance of RequestLibrary, bound to a specific deployed contract.
func NewRequestLibraryTransactor(address common.Address, transactor bind.ContractTransactor) (*RequestLibraryTransactor, error) {
	contract, err := bindRequestLibrary(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &RequestLibraryTransactor{contract: contract}, nil
}

// NewRequestLibraryFilterer creates a new log filterer instance of RequestLibrary, bound to a specific deployed contract.
func NewRequestLibraryFilterer(address common.Address, filterer bind.ContractFilterer) (*RequestLibraryFilterer, error) {
	contract, err := bindRequestLibrary(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &RequestLibraryFilterer{contract: contract}, nil
}

// bindRequestLibrary binds a generic wrapper to an already deployed contract.
func bindRequestLibrary(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := RequestLibraryMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_RequestLibrary *RequestLibraryRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _RequestLibrary.Contract.RequestLibraryCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_RequestLibrary *RequestLibraryRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _RequestLibrary.Contract.RequestLibraryTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_RequestLibrary *RequestLibraryRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _RequestLibrary.Contract.RequestLibraryTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_RequestLibrary *RequestLibraryCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _RequestLibrary.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_RequestLibrary *RequestLibraryTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _RequestLibrary.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_RequestLibrary *RequestLibraryTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _RequestLibrary.Contract.contract.Transact(opts, method, params...)
}
