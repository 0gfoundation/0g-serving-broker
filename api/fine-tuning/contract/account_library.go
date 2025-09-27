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

// AccountLibraryMetaData contains all meta data concerning the AccountLibrary contract.
var AccountLibraryMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountNotExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"InsufficientBalance\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundInvalid\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundLocked\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundProcessed\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"TooManyRefunds\",\"type\":\"error\"}]",
}

// AccountLibraryABI is the input ABI used to generate the binding from.
// Deprecated: Use AccountLibraryMetaData.ABI instead.
var AccountLibraryABI = AccountLibraryMetaData.ABI

// AccountLibrary is an auto generated Go binding around an Ethereum contract.
type AccountLibrary struct {
	AccountLibraryCaller     // Read-only binding to the contract
	AccountLibraryTransactor // Write-only binding to the contract
	AccountLibraryFilterer   // Log filterer for contract events
}

// AccountLibraryCaller is an auto generated read-only Go binding around an Ethereum contract.
type AccountLibraryCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// AccountLibraryTransactor is an auto generated write-only Go binding around an Ethereum contract.
type AccountLibraryTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// AccountLibraryFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type AccountLibraryFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// AccountLibrarySession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type AccountLibrarySession struct {
	Contract     *AccountLibrary   // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// AccountLibraryCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type AccountLibraryCallerSession struct {
	Contract *AccountLibraryCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts         // Call options to use throughout this session
}

// AccountLibraryTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type AccountLibraryTransactorSession struct {
	Contract     *AccountLibraryTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts         // Transaction auth options to use throughout this session
}

// AccountLibraryRaw is an auto generated low-level Go binding around an Ethereum contract.
type AccountLibraryRaw struct {
	Contract *AccountLibrary // Generic contract binding to access the raw methods on
}

// AccountLibraryCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type AccountLibraryCallerRaw struct {
	Contract *AccountLibraryCaller // Generic read-only contract binding to access the raw methods on
}

// AccountLibraryTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type AccountLibraryTransactorRaw struct {
	Contract *AccountLibraryTransactor // Generic write-only contract binding to access the raw methods on
}

// NewAccountLibrary creates a new instance of AccountLibrary, bound to a specific deployed contract.
func NewAccountLibrary(address common.Address, backend bind.ContractBackend) (*AccountLibrary, error) {
	contract, err := bindAccountLibrary(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &AccountLibrary{AccountLibraryCaller: AccountLibraryCaller{contract: contract}, AccountLibraryTransactor: AccountLibraryTransactor{contract: contract}, AccountLibraryFilterer: AccountLibraryFilterer{contract: contract}}, nil
}

// NewAccountLibraryCaller creates a new read-only instance of AccountLibrary, bound to a specific deployed contract.
func NewAccountLibraryCaller(address common.Address, caller bind.ContractCaller) (*AccountLibraryCaller, error) {
	contract, err := bindAccountLibrary(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &AccountLibraryCaller{contract: contract}, nil
}

// NewAccountLibraryTransactor creates a new write-only instance of AccountLibrary, bound to a specific deployed contract.
func NewAccountLibraryTransactor(address common.Address, transactor bind.ContractTransactor) (*AccountLibraryTransactor, error) {
	contract, err := bindAccountLibrary(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &AccountLibraryTransactor{contract: contract}, nil
}

// NewAccountLibraryFilterer creates a new log filterer instance of AccountLibrary, bound to a specific deployed contract.
func NewAccountLibraryFilterer(address common.Address, filterer bind.ContractFilterer) (*AccountLibraryFilterer, error) {
	contract, err := bindAccountLibrary(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &AccountLibraryFilterer{contract: contract}, nil
}

// bindAccountLibrary binds a generic wrapper to an already deployed contract.
func bindAccountLibrary(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := AccountLibraryMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_AccountLibrary *AccountLibraryRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _AccountLibrary.Contract.AccountLibraryCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_AccountLibrary *AccountLibraryRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _AccountLibrary.Contract.AccountLibraryTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_AccountLibrary *AccountLibraryRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _AccountLibrary.Contract.AccountLibraryTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_AccountLibrary *AccountLibraryCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _AccountLibrary.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_AccountLibrary *AccountLibraryTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _AccountLibrary.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_AccountLibrary *AccountLibraryTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _AccountLibrary.Contract.contract.Transact(opts, method, params...)
}
