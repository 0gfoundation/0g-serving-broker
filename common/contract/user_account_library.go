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

// UserAccountLibraryMetaData contains all meta data concerning the UserAccountLibrary contract.
var UserAccountLibraryMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"InsufficientBalance\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundInvalid\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundLocked\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundProcessed\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"UserAccountNotexists\",\"type\":\"error\"}]",
}

// UserAccountLibraryABI is the input ABI used to generate the binding from.
// Deprecated: Use UserAccountLibraryMetaData.ABI instead.
var UserAccountLibraryABI = UserAccountLibraryMetaData.ABI

// UserAccountLibrary is an auto generated Go binding around an Ethereum contract.
type UserAccountLibrary struct {
	UserAccountLibraryCaller     // Read-only binding to the contract
	UserAccountLibraryTransactor // Write-only binding to the contract
	UserAccountLibraryFilterer   // Log filterer for contract events
}

// UserAccountLibraryCaller is an auto generated read-only Go binding around an Ethereum contract.
type UserAccountLibraryCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// UserAccountLibraryTransactor is an auto generated write-only Go binding around an Ethereum contract.
type UserAccountLibraryTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// UserAccountLibraryFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type UserAccountLibraryFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// UserAccountLibrarySession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type UserAccountLibrarySession struct {
	Contract     *UserAccountLibrary // Generic contract binding to set the session for
	CallOpts     bind.CallOpts       // Call options to use throughout this session
	TransactOpts bind.TransactOpts   // Transaction auth options to use throughout this session
}

// UserAccountLibraryCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type UserAccountLibraryCallerSession struct {
	Contract *UserAccountLibraryCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts             // Call options to use throughout this session
}

// UserAccountLibraryTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type UserAccountLibraryTransactorSession struct {
	Contract     *UserAccountLibraryTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts             // Transaction auth options to use throughout this session
}

// UserAccountLibraryRaw is an auto generated low-level Go binding around an Ethereum contract.
type UserAccountLibraryRaw struct {
	Contract *UserAccountLibrary // Generic contract binding to access the raw methods on
}

// UserAccountLibraryCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type UserAccountLibraryCallerRaw struct {
	Contract *UserAccountLibraryCaller // Generic read-only contract binding to access the raw methods on
}

// UserAccountLibraryTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type UserAccountLibraryTransactorRaw struct {
	Contract *UserAccountLibraryTransactor // Generic write-only contract binding to access the raw methods on
}

// NewUserAccountLibrary creates a new instance of UserAccountLibrary, bound to a specific deployed contract.
func NewUserAccountLibrary(address common.Address, backend bind.ContractBackend) (*UserAccountLibrary, error) {
	contract, err := bindUserAccountLibrary(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &UserAccountLibrary{UserAccountLibraryCaller: UserAccountLibraryCaller{contract: contract}, UserAccountLibraryTransactor: UserAccountLibraryTransactor{contract: contract}, UserAccountLibraryFilterer: UserAccountLibraryFilterer{contract: contract}}, nil
}

// NewUserAccountLibraryCaller creates a new read-only instance of UserAccountLibrary, bound to a specific deployed contract.
func NewUserAccountLibraryCaller(address common.Address, caller bind.ContractCaller) (*UserAccountLibraryCaller, error) {
	contract, err := bindUserAccountLibrary(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &UserAccountLibraryCaller{contract: contract}, nil
}

// NewUserAccountLibraryTransactor creates a new write-only instance of UserAccountLibrary, bound to a specific deployed contract.
func NewUserAccountLibraryTransactor(address common.Address, transactor bind.ContractTransactor) (*UserAccountLibraryTransactor, error) {
	contract, err := bindUserAccountLibrary(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &UserAccountLibraryTransactor{contract: contract}, nil
}

// NewUserAccountLibraryFilterer creates a new log filterer instance of UserAccountLibrary, bound to a specific deployed contract.
func NewUserAccountLibraryFilterer(address common.Address, filterer bind.ContractFilterer) (*UserAccountLibraryFilterer, error) {
	contract, err := bindUserAccountLibrary(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &UserAccountLibraryFilterer{contract: contract}, nil
}

// bindUserAccountLibrary binds a generic wrapper to an already deployed contract.
func bindUserAccountLibrary(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := UserAccountLibraryMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_UserAccountLibrary *UserAccountLibraryRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _UserAccountLibrary.Contract.UserAccountLibraryCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_UserAccountLibrary *UserAccountLibraryRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _UserAccountLibrary.Contract.UserAccountLibraryTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_UserAccountLibrary *UserAccountLibraryRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _UserAccountLibrary.Contract.UserAccountLibraryTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_UserAccountLibrary *UserAccountLibraryCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _UserAccountLibrary.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_UserAccountLibrary *UserAccountLibraryTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _UserAccountLibrary.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_UserAccountLibrary *UserAccountLibraryTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _UserAccountLibrary.Contract.contract.Transact(opts, method, params...)
}
