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

// ILedgerMetaData contains all meta data concerning the ILedger contract.
var ILedgerMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"spendFund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// ILedgerABI is the input ABI used to generate the binding from.
// Deprecated: Use ILedgerMetaData.ABI instead.
var ILedgerABI = ILedgerMetaData.ABI

// ILedger is an auto generated Go binding around an Ethereum contract.
type ILedger struct {
	ILedgerCaller     // Read-only binding to the contract
	ILedgerTransactor // Write-only binding to the contract
	ILedgerFilterer   // Log filterer for contract events
}

// ILedgerCaller is an auto generated read-only Go binding around an Ethereum contract.
type ILedgerCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ILedgerTransactor is an auto generated write-only Go binding around an Ethereum contract.
type ILedgerTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ILedgerFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type ILedgerFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ILedgerSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type ILedgerSession struct {
	Contract     *ILedger          // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// ILedgerCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type ILedgerCallerSession struct {
	Contract *ILedgerCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts  // Call options to use throughout this session
}

// ILedgerTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type ILedgerTransactorSession struct {
	Contract     *ILedgerTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts  // Transaction auth options to use throughout this session
}

// ILedgerRaw is an auto generated low-level Go binding around an Ethereum contract.
type ILedgerRaw struct {
	Contract *ILedger // Generic contract binding to access the raw methods on
}

// ILedgerCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type ILedgerCallerRaw struct {
	Contract *ILedgerCaller // Generic read-only contract binding to access the raw methods on
}

// ILedgerTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type ILedgerTransactorRaw struct {
	Contract *ILedgerTransactor // Generic write-only contract binding to access the raw methods on
}

// NewILedger creates a new instance of ILedger, bound to a specific deployed contract.
func NewILedger(address common.Address, backend bind.ContractBackend) (*ILedger, error) {
	contract, err := bindILedger(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &ILedger{ILedgerCaller: ILedgerCaller{contract: contract}, ILedgerTransactor: ILedgerTransactor{contract: contract}, ILedgerFilterer: ILedgerFilterer{contract: contract}}, nil
}

// NewILedgerCaller creates a new read-only instance of ILedger, bound to a specific deployed contract.
func NewILedgerCaller(address common.Address, caller bind.ContractCaller) (*ILedgerCaller, error) {
	contract, err := bindILedger(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ILedgerCaller{contract: contract}, nil
}

// NewILedgerTransactor creates a new write-only instance of ILedger, bound to a specific deployed contract.
func NewILedgerTransactor(address common.Address, transactor bind.ContractTransactor) (*ILedgerTransactor, error) {
	contract, err := bindILedger(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &ILedgerTransactor{contract: contract}, nil
}

// NewILedgerFilterer creates a new log filterer instance of ILedger, bound to a specific deployed contract.
func NewILedgerFilterer(address common.Address, filterer bind.ContractFilterer) (*ILedgerFilterer, error) {
	contract, err := bindILedger(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &ILedgerFilterer{contract: contract}, nil
}

// bindILedger binds a generic wrapper to an already deployed contract.
func bindILedger(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := ILedgerMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ILedger *ILedgerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ILedger.Contract.ILedgerCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ILedger *ILedgerRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ILedger.Contract.ILedgerTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ILedger *ILedgerRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ILedger.Contract.ILedgerTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_ILedger *ILedgerCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _ILedger.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_ILedger *ILedgerTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _ILedger.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_ILedger *ILedgerTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _ILedger.Contract.contract.Transact(opts, method, params...)
}

// SpendFund is a paid mutator transaction binding the contract method 0xdd8a4118.
//
// Solidity: function spendFund(address user, uint256 amount) returns()
func (_ILedger *ILedgerTransactor) SpendFund(opts *bind.TransactOpts, user common.Address, amount *big.Int) (*types.Transaction, error) {
	return _ILedger.contract.Transact(opts, "spendFund", user, amount)
}

// SpendFund is a paid mutator transaction binding the contract method 0xdd8a4118.
//
// Solidity: function spendFund(address user, uint256 amount) returns()
func (_ILedger *ILedgerSession) SpendFund(user common.Address, amount *big.Int) (*types.Transaction, error) {
	return _ILedger.Contract.SpendFund(&_ILedger.TransactOpts, user, amount)
}

// SpendFund is a paid mutator transaction binding the contract method 0xdd8a4118.
//
// Solidity: function spendFund(address user, uint256 amount) returns()
func (_ILedger *ILedgerTransactorSession) SpendFund(user common.Address, amount *big.Int) (*types.Transaction, error) {
	return _ILedger.Contract.SpendFund(&_ILedger.TransactOpts, user, amount)
}
