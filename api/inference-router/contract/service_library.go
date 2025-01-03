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

// ServiceLibraryMetaData contains all meta data concerning the ServiceLibrary contract.
var ServiceLibraryMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceNotexist\",\"type\":\"error\"}]",
}

// ServiceLibraryABI is the input ABI used to generate the binding from.
// Deprecated: Use ServiceLibraryMetaData.ABI instead.
var ServiceLibraryABI = ServiceLibraryMetaData.ABI

// ServiceLibrary is an auto generated Go binding around an Ethereum contract.
type ServiceLibrary struct {
	ServiceLibraryCaller     // Read-only binding to the contract
	ServiceLibraryTransactor // Write-only binding to the contract
	ServiceLibraryFilterer   // Log filterer for contract events
}

// ServiceLibraryCaller is an auto generated read-only Go binding around an Ethereum contract.
type ServiceLibraryCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ServiceLibraryTransactor is an auto generated write-only Go binding around an Ethereum contract.
type ServiceLibraryTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ServiceLibraryFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type ServiceLibraryFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ServiceLibrarySession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type ServiceLibrarySession struct {
	Contract     *ServiceLibrary   // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// ServiceLibraryCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type ServiceLibraryCallerSession struct {
	Contract *ServiceLibraryCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts         // Call options to use throughout this session
}

// ServiceLibraryTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type ServiceLibraryTransactorSession struct {
	Contract     *ServiceLibraryTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts         // Transaction auth options to use throughout this session
}

// ServiceLibraryRaw is an auto generated low-level Go binding around an Ethereum contract.
type ServiceLibraryRaw struct {
	Contract *ServiceLibrary // Generic contract binding to access the raw methods on
}

// ServiceLibraryCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type ServiceLibraryCallerRaw struct {
	Contract *ServiceLibraryCaller // Generic read-only contract binding to access the raw methods on
}

// ServiceLibraryTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type ServiceLibraryTransactorRaw struct {
	Contract *ServiceLibraryTransactor // Generic write-only contract binding to access the raw methods on
}

// NewServiceLibrary creates a new instance of ServiceLibrary, bound to a specific deployed contract.
func NewServiceLibrary(address common.Address, backend bind.ContractBackend) (*ServiceLibrary, error) {
	contract, err := bindServiceLibrary(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &ServiceLibrary{ServiceLibraryCaller: ServiceLibraryCaller{contract: contract}, ServiceLibraryTransactor: ServiceLibraryTransactor{contract: contract}, ServiceLibraryFilterer: ServiceLibraryFilterer{contract: contract}}, nil
}

// NewServiceLibraryCaller creates a new read-only instance of ServiceLibrary, bound to a specific deployed contract.
func NewServiceLibraryCaller(address common.Address, caller bind.ContractCaller) (*ServiceLibraryCaller, error) {
	contract, err := bindServiceLibrary(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ServiceLibraryCaller{contract: contract}, nil
}

// NewServiceLibraryTransactor creates a new write-only instance of ServiceLibrary, bound to a specific deployed contract.
func NewServiceLibraryTransactor(address common.Address, transactor bind.ContractTransactor) (*ServiceLibraryTransactor, error) {
	contract, err := bindServiceLibrary(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &ServiceLibraryTransactor{contract: contract}, nil
}

// NewServiceLibraryFilterer creates a new log filterer instance of ServiceLibrary, bound to a specific deployed contract.
func NewServiceLibraryFilterer(address common.Address, filterer bind.ContractFilterer) (*ServiceLibraryFilterer, error) {
	contract, err := bindServiceLibrary(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &ServiceLibraryFilterer{contract: contract}, nil
}

// bindServiceLibrary binds a generic wrapper to an already deployed contract.
func bindServiceLibrary(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := ServiceLibraryMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ServiceLibrary *ServiceLibraryRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ServiceLibrary.Contract.ServiceLibraryCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ServiceLibrary *ServiceLibraryRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ServiceLibrary.Contract.ServiceLibraryTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ServiceLibrary *ServiceLibraryRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ServiceLibrary.Contract.ServiceLibraryTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ServiceLibrary *ServiceLibraryCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ServiceLibrary.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ServiceLibrary *ServiceLibraryTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ServiceLibrary.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ServiceLibrary *ServiceLibraryTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ServiceLibrary.Contract.contract.Transact(opts, method, params...)
}
