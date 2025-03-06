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

// IServingMetaData contains all meta data concerning the IServing contract.
var IServingMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"accountExists\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"deleteAccount\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"cancelRetrievingAmount\",\"type\":\"uint256\"}],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getPendingRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"processRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"totalAmount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"requestRefundAll\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// IServingABI is the input ABI used to generate the binding from.
// Deprecated: Use IServingMetaData.ABI instead.
var IServingABI = IServingMetaData.ABI

// IServing is an auto generated Go binding around an Ethereum contract.
type IServing struct {
	IServingCaller     // Read-only binding to the contract
	IServingTransactor // Write-only binding to the contract
	IServingFilterer   // Log filterer for contract events
}

// IServingCaller is an auto generated read-only Go binding around an Ethereum contract.
type IServingCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IServingTransactor is an auto generated write-only Go binding around an Ethereum contract.
type IServingTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IServingFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type IServingFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IServingSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type IServingSession struct {
	Contract     *IServing         // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// IServingCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type IServingCallerSession struct {
	Contract *IServingCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts   // Call options to use throughout this session
}

// IServingTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type IServingTransactorSession struct {
	Contract     *IServingTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts   // Transaction auth options to use throughout this session
}

// IServingRaw is an auto generated low-level Go binding around an Ethereum contract.
type IServingRaw struct {
	Contract *IServing // Generic contract binding to access the raw methods on
}

// IServingCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type IServingCallerRaw struct {
	Contract *IServingCaller // Generic read-only contract binding to access the raw methods on
}

// IServingTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type IServingTransactorRaw struct {
	Contract *IServingTransactor // Generic write-only contract binding to access the raw methods on
}

// NewIServing creates a new instance of IServing, bound to a specific deployed contract.
func NewIServing(address common.Address, backend bind.ContractBackend) (*IServing, error) {
	contract, err := bindIServing(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &IServing{IServingCaller: IServingCaller{contract: contract}, IServingTransactor: IServingTransactor{contract: contract}, IServingFilterer: IServingFilterer{contract: contract}}, nil
}

// NewIServingCaller creates a new read-only instance of IServing, bound to a specific deployed contract.
func NewIServingCaller(address common.Address, caller bind.ContractCaller) (*IServingCaller, error) {
	contract, err := bindIServing(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &IServingCaller{contract: contract}, nil
}

// NewIServingTransactor creates a new write-only instance of IServing, bound to a specific deployed contract.
func NewIServingTransactor(address common.Address, transactor bind.ContractTransactor) (*IServingTransactor, error) {
	contract, err := bindIServing(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &IServingTransactor{contract: contract}, nil
}

// NewIServingFilterer creates a new log filterer instance of IServing, bound to a specific deployed contract.
func NewIServingFilterer(address common.Address, filterer bind.ContractFilterer) (*IServingFilterer, error) {
	contract, err := bindIServing(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &IServingFilterer{contract: contract}, nil
}

// bindIServing binds a generic wrapper to an already deployed contract.
func bindIServing(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := IServingMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IServing *IServingRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IServing.Contract.IServingCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IServing *IServingRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IServing.Contract.IServingTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IServing *IServingRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IServing.Contract.IServingTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IServing *IServingCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IServing.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IServing *IServingTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IServing.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IServing *IServingTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IServing.Contract.contract.Transact(opts, method, params...)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_IServing *IServingCaller) AccountExists(opts *bind.CallOpts, user common.Address, provider common.Address) (bool, error) {
	var out []interface{}
	err := _IServing.contract.Call(opts, &out, "accountExists", user, provider)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_IServing *IServingSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _IServing.Contract.AccountExists(&_IServing.CallOpts, user, provider)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_IServing *IServingCallerSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _IServing.Contract.AccountExists(&_IServing.CallOpts, user, provider)
}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_IServing *IServingCaller) GetPendingRefund(opts *bind.CallOpts, user common.Address, provider common.Address) (*big.Int, error) {
	var out []interface{}
	err := _IServing.contract.Call(opts, &out, "getPendingRefund", user, provider)

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_IServing *IServingSession) GetPendingRefund(user common.Address, provider common.Address) (*big.Int, error) {
	return _IServing.Contract.GetPendingRefund(&_IServing.CallOpts, user, provider)
}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_IServing *IServingCallerSession) GetPendingRefund(user common.Address, provider common.Address) (*big.Int, error) {
	return _IServing.Contract.GetPendingRefund(&_IServing.CallOpts, user, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_IServing *IServingTransactor) DeleteAccount(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.contract.Transact(opts, "deleteAccount", user, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_IServing *IServingSession) DeleteAccount(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.Contract.DeleteAccount(&_IServing.TransactOpts, user, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_IServing *IServingTransactorSession) DeleteAccount(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.Contract.DeleteAccount(&_IServing.TransactOpts, user, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_IServing *IServingTransactor) DepositFund(opts *bind.TransactOpts, user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _IServing.contract.Transact(opts, "depositFund", user, provider, cancelRetrievingAmount)
}

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_IServing *IServingSession) DepositFund(user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _IServing.Contract.DepositFund(&_IServing.TransactOpts, user, provider, cancelRetrievingAmount)
}

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_IServing *IServingTransactorSession) DepositFund(user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _IServing.Contract.DepositFund(&_IServing.TransactOpts, user, provider, cancelRetrievingAmount)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_IServing *IServingTransactor) ProcessRefund(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.contract.Transact(opts, "processRefund", user, provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_IServing *IServingSession) ProcessRefund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.Contract.ProcessRefund(&_IServing.TransactOpts, user, provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_IServing *IServingTransactorSession) ProcessRefund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.Contract.ProcessRefund(&_IServing.TransactOpts, user, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_IServing *IServingTransactor) RequestRefundAll(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.contract.Transact(opts, "requestRefundAll", user, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_IServing *IServingSession) RequestRefundAll(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.Contract.RequestRefundAll(&_IServing.TransactOpts, user, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_IServing *IServingTransactorSession) RequestRefundAll(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _IServing.Contract.RequestRefundAll(&_IServing.TransactOpts, user, provider)
}
