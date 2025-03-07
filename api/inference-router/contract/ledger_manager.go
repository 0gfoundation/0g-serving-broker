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

// Ledger is an auto generated low-level Go binding around an user-defined struct.
type Ledger struct {
	User                common.Address
	AvailableBalance    *big.Int
	TotalBalance        *big.Int
	InferenceSigner     [2]*big.Int
	AdditionalInfo      string
	InferenceProviders  []common.Address
	FineTuningProviders []common.Address
}

// LedgerManagerMetaData contains all meta data concerning the LedgerManager contract.
var LedgerManagerMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"name\":\"InsufficientBalance\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"name\":\"LedgerExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"name\":\"LedgerNotExists\",\"type\":\"error\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"uint256[2]\",\"name\":\"inferenceSigner\",\"type\":\"uint256[2]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"name\":\"addLedger\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"deleteLedger\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"fineTuningAddress\",\"outputs\":[{\"internalType\":\"addresspayable\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllLedgers\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"availableBalance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"totalBalance\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"inferenceSigner\",\"type\":\"uint256[2]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address[]\",\"name\":\"inferenceProviders\",\"type\":\"address[]\"},{\"internalType\":\"address[]\",\"name\":\"fineTuningProviders\",\"type\":\"address[]\"}],\"internalType\":\"structLedger[]\",\"name\":\"ledgers\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"name\":\"getLedger\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"availableBalance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"totalBalance\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"inferenceSigner\",\"type\":\"uint256[2]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address[]\",\"name\":\"inferenceProviders\",\"type\":\"address[]\"},{\"internalType\":\"address[]\",\"name\":\"fineTuningProviders\",\"type\":\"address[]\"}],\"internalType\":\"structLedger\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"inferenceAddress\",\"outputs\":[{\"internalType\":\"addresspayable\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"_inferenceAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_fineTuningAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"}],\"name\":\"initialize\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"initialized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"refund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address[]\",\"name\":\"providers\",\"type\":\"address[]\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"}],\"name\":\"retrieveFund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"spendFund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"serviceTypeStr\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"transferFund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"stateMutability\":\"payable\",\"type\":\"receive\"}]",
}

// LedgerManagerABI is the input ABI used to generate the binding from.
// Deprecated: Use LedgerManagerMetaData.ABI instead.
var LedgerManagerABI = LedgerManagerMetaData.ABI

// LedgerManager is an auto generated Go binding around an Ethereum contract.
type LedgerManager struct {
	LedgerManagerCaller     // Read-only binding to the contract
	LedgerManagerTransactor // Write-only binding to the contract
	LedgerManagerFilterer   // Log filterer for contract events
}

// LedgerManagerCaller is an auto generated read-only Go binding around an Ethereum contract.
type LedgerManagerCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// LedgerManagerTransactor is an auto generated write-only Go binding around an Ethereum contract.
type LedgerManagerTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// LedgerManagerFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type LedgerManagerFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// LedgerManagerSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type LedgerManagerSession struct {
	Contract     *LedgerManager    // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// LedgerManagerCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type LedgerManagerCallerSession struct {
	Contract *LedgerManagerCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts        // Call options to use throughout this session
}

// LedgerManagerTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type LedgerManagerTransactorSession struct {
	Contract     *LedgerManagerTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts        // Transaction auth options to use throughout this session
}

// LedgerManagerRaw is an auto generated low-level Go binding around an Ethereum contract.
type LedgerManagerRaw struct {
	Contract *LedgerManager // Generic contract binding to access the raw methods on
}

// LedgerManagerCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type LedgerManagerCallerRaw struct {
	Contract *LedgerManagerCaller // Generic read-only contract binding to access the raw methods on
}

// LedgerManagerTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type LedgerManagerTransactorRaw struct {
	Contract *LedgerManagerTransactor // Generic write-only contract binding to access the raw methods on
}

// NewLedgerManager creates a new instance of LedgerManager, bound to a specific deployed contract.
func NewLedgerManager(address common.Address, backend bind.ContractBackend) (*LedgerManager, error) {
	contract, err := bindLedgerManager(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &LedgerManager{LedgerManagerCaller: LedgerManagerCaller{contract: contract}, LedgerManagerTransactor: LedgerManagerTransactor{contract: contract}, LedgerManagerFilterer: LedgerManagerFilterer{contract: contract}}, nil
}

// NewLedgerManagerCaller creates a new read-only instance of LedgerManager, bound to a specific deployed contract.
func NewLedgerManagerCaller(address common.Address, caller bind.ContractCaller) (*LedgerManagerCaller, error) {
	contract, err := bindLedgerManager(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &LedgerManagerCaller{contract: contract}, nil
}

// NewLedgerManagerTransactor creates a new write-only instance of LedgerManager, bound to a specific deployed contract.
func NewLedgerManagerTransactor(address common.Address, transactor bind.ContractTransactor) (*LedgerManagerTransactor, error) {
	contract, err := bindLedgerManager(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &LedgerManagerTransactor{contract: contract}, nil
}

// NewLedgerManagerFilterer creates a new log filterer instance of LedgerManager, bound to a specific deployed contract.
func NewLedgerManagerFilterer(address common.Address, filterer bind.ContractFilterer) (*LedgerManagerFilterer, error) {
	contract, err := bindLedgerManager(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &LedgerManagerFilterer{contract: contract}, nil
}

// bindLedgerManager binds a generic wrapper to an already deployed contract.
func bindLedgerManager(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := LedgerManagerMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_LedgerManager *LedgerManagerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _LedgerManager.Contract.LedgerManagerCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_LedgerManager *LedgerManagerRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _LedgerManager.Contract.LedgerManagerTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_LedgerManager *LedgerManagerRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _LedgerManager.Contract.LedgerManagerTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_LedgerManager *LedgerManagerCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _LedgerManager.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_LedgerManager *LedgerManagerTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _LedgerManager.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_LedgerManager *LedgerManagerTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _LedgerManager.Contract.contract.Transact(opts, method, params...)
}

// FineTuningAddress is a free data retrieval call binding the contract method 0x382e1112.
//
// Solidity: function fineTuningAddress() view returns(address)
func (_LedgerManager *LedgerManagerCaller) FineTuningAddress(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _LedgerManager.contract.Call(opts, &out, "fineTuningAddress")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// FineTuningAddress is a free data retrieval call binding the contract method 0x382e1112.
//
// Solidity: function fineTuningAddress() view returns(address)
func (_LedgerManager *LedgerManagerSession) FineTuningAddress() (common.Address, error) {
	return _LedgerManager.Contract.FineTuningAddress(&_LedgerManager.CallOpts)
}

// FineTuningAddress is a free data retrieval call binding the contract method 0x382e1112.
//
// Solidity: function fineTuningAddress() view returns(address)
func (_LedgerManager *LedgerManagerCallerSession) FineTuningAddress() (common.Address, error) {
	return _LedgerManager.Contract.FineTuningAddress(&_LedgerManager.CallOpts)
}

// GetAllLedgers is a free data retrieval call binding the contract method 0x1665c79b.
//
// Solidity: function getAllLedgers() view returns((address,uint256,uint256,uint256[2],string,address[],address[])[] ledgers)
func (_LedgerManager *LedgerManagerCaller) GetAllLedgers(opts *bind.CallOpts) ([]Ledger, error) {
	var out []interface{}
	err := _LedgerManager.contract.Call(opts, &out, "getAllLedgers")

	if err != nil {
		return *new([]Ledger), err
	}

	out0 := *abi.ConvertType(out[0], new([]Ledger)).(*[]Ledger)

	return out0, err

}

// GetAllLedgers is a free data retrieval call binding the contract method 0x1665c79b.
//
// Solidity: function getAllLedgers() view returns((address,uint256,uint256,uint256[2],string,address[],address[])[] ledgers)
func (_LedgerManager *LedgerManagerSession) GetAllLedgers() ([]Ledger, error) {
	return _LedgerManager.Contract.GetAllLedgers(&_LedgerManager.CallOpts)
}

// GetAllLedgers is a free data retrieval call binding the contract method 0x1665c79b.
//
// Solidity: function getAllLedgers() view returns((address,uint256,uint256,uint256[2],string,address[],address[])[] ledgers)
func (_LedgerManager *LedgerManagerCallerSession) GetAllLedgers() ([]Ledger, error) {
	return _LedgerManager.Contract.GetAllLedgers(&_LedgerManager.CallOpts)
}

// GetLedger is a free data retrieval call binding the contract method 0xf7cd6af9.
//
// Solidity: function getLedger(address user) view returns((address,uint256,uint256,uint256[2],string,address[],address[]))
func (_LedgerManager *LedgerManagerCaller) GetLedger(opts *bind.CallOpts, user common.Address) (Ledger, error) {
	var out []interface{}
	err := _LedgerManager.contract.Call(opts, &out, "getLedger", user)

	if err != nil {
		return *new(Ledger), err
	}

	out0 := *abi.ConvertType(out[0], new(Ledger)).(*Ledger)

	return out0, err

}

// GetLedger is a free data retrieval call binding the contract method 0xf7cd6af9.
//
// Solidity: function getLedger(address user) view returns((address,uint256,uint256,uint256[2],string,address[],address[]))
func (_LedgerManager *LedgerManagerSession) GetLedger(user common.Address) (Ledger, error) {
	return _LedgerManager.Contract.GetLedger(&_LedgerManager.CallOpts, user)
}

// GetLedger is a free data retrieval call binding the contract method 0xf7cd6af9.
//
// Solidity: function getLedger(address user) view returns((address,uint256,uint256,uint256[2],string,address[],address[]))
func (_LedgerManager *LedgerManagerCallerSession) GetLedger(user common.Address) (Ledger, error) {
	return _LedgerManager.Contract.GetLedger(&_LedgerManager.CallOpts, user)
}

// InferenceAddress is a free data retrieval call binding the contract method 0xe5d9fdab.
//
// Solidity: function inferenceAddress() view returns(address)
func (_LedgerManager *LedgerManagerCaller) InferenceAddress(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _LedgerManager.contract.Call(opts, &out, "inferenceAddress")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// InferenceAddress is a free data retrieval call binding the contract method 0xe5d9fdab.
//
// Solidity: function inferenceAddress() view returns(address)
func (_LedgerManager *LedgerManagerSession) InferenceAddress() (common.Address, error) {
	return _LedgerManager.Contract.InferenceAddress(&_LedgerManager.CallOpts)
}

// InferenceAddress is a free data retrieval call binding the contract method 0xe5d9fdab.
//
// Solidity: function inferenceAddress() view returns(address)
func (_LedgerManager *LedgerManagerCallerSession) InferenceAddress() (common.Address, error) {
	return _LedgerManager.Contract.InferenceAddress(&_LedgerManager.CallOpts)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_LedgerManager *LedgerManagerCaller) Initialized(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _LedgerManager.contract.Call(opts, &out, "initialized")

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_LedgerManager *LedgerManagerSession) Initialized() (bool, error) {
	return _LedgerManager.Contract.Initialized(&_LedgerManager.CallOpts)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_LedgerManager *LedgerManagerCallerSession) Initialized() (bool, error) {
	return _LedgerManager.Contract.Initialized(&_LedgerManager.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_LedgerManager *LedgerManagerCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _LedgerManager.contract.Call(opts, &out, "owner")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_LedgerManager *LedgerManagerSession) Owner() (common.Address, error) {
	return _LedgerManager.Contract.Owner(&_LedgerManager.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_LedgerManager *LedgerManagerCallerSession) Owner() (common.Address, error) {
	return _LedgerManager.Contract.Owner(&_LedgerManager.CallOpts)
}

// AddLedger is a paid mutator transaction binding the contract method 0x72adc0d9.
//
// Solidity: function addLedger(uint256[2] inferenceSigner, string additionalInfo) payable returns(uint256, uint256)
func (_LedgerManager *LedgerManagerTransactor) AddLedger(opts *bind.TransactOpts, inferenceSigner [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "addLedger", inferenceSigner, additionalInfo)
}

// AddLedger is a paid mutator transaction binding the contract method 0x72adc0d9.
//
// Solidity: function addLedger(uint256[2] inferenceSigner, string additionalInfo) payable returns(uint256, uint256)
func (_LedgerManager *LedgerManagerSession) AddLedger(inferenceSigner [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _LedgerManager.Contract.AddLedger(&_LedgerManager.TransactOpts, inferenceSigner, additionalInfo)
}

// AddLedger is a paid mutator transaction binding the contract method 0x72adc0d9.
//
// Solidity: function addLedger(uint256[2] inferenceSigner, string additionalInfo) payable returns(uint256, uint256)
func (_LedgerManager *LedgerManagerTransactorSession) AddLedger(inferenceSigner [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _LedgerManager.Contract.AddLedger(&_LedgerManager.TransactOpts, inferenceSigner, additionalInfo)
}

// DeleteLedger is a paid mutator transaction binding the contract method 0x410b3815.
//
// Solidity: function deleteLedger() returns()
func (_LedgerManager *LedgerManagerTransactor) DeleteLedger(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "deleteLedger")
}

// DeleteLedger is a paid mutator transaction binding the contract method 0x410b3815.
//
// Solidity: function deleteLedger() returns()
func (_LedgerManager *LedgerManagerSession) DeleteLedger() (*types.Transaction, error) {
	return _LedgerManager.Contract.DeleteLedger(&_LedgerManager.TransactOpts)
}

// DeleteLedger is a paid mutator transaction binding the contract method 0x410b3815.
//
// Solidity: function deleteLedger() returns()
func (_LedgerManager *LedgerManagerTransactorSession) DeleteLedger() (*types.Transaction, error) {
	return _LedgerManager.Contract.DeleteLedger(&_LedgerManager.TransactOpts)
}

// DepositFund is a paid mutator transaction binding the contract method 0x8d0d8cb6.
//
// Solidity: function depositFund() payable returns()
func (_LedgerManager *LedgerManagerTransactor) DepositFund(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "depositFund")
}

// DepositFund is a paid mutator transaction binding the contract method 0x8d0d8cb6.
//
// Solidity: function depositFund() payable returns()
func (_LedgerManager *LedgerManagerSession) DepositFund() (*types.Transaction, error) {
	return _LedgerManager.Contract.DepositFund(&_LedgerManager.TransactOpts)
}

// DepositFund is a paid mutator transaction binding the contract method 0x8d0d8cb6.
//
// Solidity: function depositFund() payable returns()
func (_LedgerManager *LedgerManagerTransactorSession) DepositFund() (*types.Transaction, error) {
	return _LedgerManager.Contract.DepositFund(&_LedgerManager.TransactOpts)
}

// Initialize is a paid mutator transaction binding the contract method 0xc0c53b8b.
//
// Solidity: function initialize(address _inferenceAddress, address _fineTuningAddress, address owner) returns()
func (_LedgerManager *LedgerManagerTransactor) Initialize(opts *bind.TransactOpts, _inferenceAddress common.Address, _fineTuningAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "initialize", _inferenceAddress, _fineTuningAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xc0c53b8b.
//
// Solidity: function initialize(address _inferenceAddress, address _fineTuningAddress, address owner) returns()
func (_LedgerManager *LedgerManagerSession) Initialize(_inferenceAddress common.Address, _fineTuningAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _LedgerManager.Contract.Initialize(&_LedgerManager.TransactOpts, _inferenceAddress, _fineTuningAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xc0c53b8b.
//
// Solidity: function initialize(address _inferenceAddress, address _fineTuningAddress, address owner) returns()
func (_LedgerManager *LedgerManagerTransactorSession) Initialize(_inferenceAddress common.Address, _fineTuningAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _LedgerManager.Contract.Initialize(&_LedgerManager.TransactOpts, _inferenceAddress, _fineTuningAddress, owner)
}

// Refund is a paid mutator transaction binding the contract method 0x278ecde1.
//
// Solidity: function refund(uint256 amount) returns()
func (_LedgerManager *LedgerManagerTransactor) Refund(opts *bind.TransactOpts, amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "refund", amount)
}

// Refund is a paid mutator transaction binding the contract method 0x278ecde1.
//
// Solidity: function refund(uint256 amount) returns()
func (_LedgerManager *LedgerManagerSession) Refund(amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.Contract.Refund(&_LedgerManager.TransactOpts, amount)
}

// Refund is a paid mutator transaction binding the contract method 0x278ecde1.
//
// Solidity: function refund(uint256 amount) returns()
func (_LedgerManager *LedgerManagerTransactorSession) Refund(amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.Contract.Refund(&_LedgerManager.TransactOpts, amount)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_LedgerManager *LedgerManagerTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_LedgerManager *LedgerManagerSession) RenounceOwnership() (*types.Transaction, error) {
	return _LedgerManager.Contract.RenounceOwnership(&_LedgerManager.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_LedgerManager *LedgerManagerTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _LedgerManager.Contract.RenounceOwnership(&_LedgerManager.TransactOpts)
}

// RetrieveFund is a paid mutator transaction binding the contract method 0x31404a19.
//
// Solidity: function retrieveFund(address[] providers, string serviceType) returns()
func (_LedgerManager *LedgerManagerTransactor) RetrieveFund(opts *bind.TransactOpts, providers []common.Address, serviceType string) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "retrieveFund", providers, serviceType)
}

// RetrieveFund is a paid mutator transaction binding the contract method 0x31404a19.
//
// Solidity: function retrieveFund(address[] providers, string serviceType) returns()
func (_LedgerManager *LedgerManagerSession) RetrieveFund(providers []common.Address, serviceType string) (*types.Transaction, error) {
	return _LedgerManager.Contract.RetrieveFund(&_LedgerManager.TransactOpts, providers, serviceType)
}

// RetrieveFund is a paid mutator transaction binding the contract method 0x31404a19.
//
// Solidity: function retrieveFund(address[] providers, string serviceType) returns()
func (_LedgerManager *LedgerManagerTransactorSession) RetrieveFund(providers []common.Address, serviceType string) (*types.Transaction, error) {
	return _LedgerManager.Contract.RetrieveFund(&_LedgerManager.TransactOpts, providers, serviceType)
}

// SpendFund is a paid mutator transaction binding the contract method 0xdd8a4118.
//
// Solidity: function spendFund(address user, uint256 amount) returns()
func (_LedgerManager *LedgerManagerTransactor) SpendFund(opts *bind.TransactOpts, user common.Address, amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "spendFund", user, amount)
}

// SpendFund is a paid mutator transaction binding the contract method 0xdd8a4118.
//
// Solidity: function spendFund(address user, uint256 amount) returns()
func (_LedgerManager *LedgerManagerSession) SpendFund(user common.Address, amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.Contract.SpendFund(&_LedgerManager.TransactOpts, user, amount)
}

// SpendFund is a paid mutator transaction binding the contract method 0xdd8a4118.
//
// Solidity: function spendFund(address user, uint256 amount) returns()
func (_LedgerManager *LedgerManagerTransactorSession) SpendFund(user common.Address, amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.Contract.SpendFund(&_LedgerManager.TransactOpts, user, amount)
}

// TransferFund is a paid mutator transaction binding the contract method 0x2ba43b82.
//
// Solidity: function transferFund(address provider, string serviceTypeStr, uint256 amount) returns()
func (_LedgerManager *LedgerManagerTransactor) TransferFund(opts *bind.TransactOpts, provider common.Address, serviceTypeStr string, amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "transferFund", provider, serviceTypeStr, amount)
}

// TransferFund is a paid mutator transaction binding the contract method 0x2ba43b82.
//
// Solidity: function transferFund(address provider, string serviceTypeStr, uint256 amount) returns()
func (_LedgerManager *LedgerManagerSession) TransferFund(provider common.Address, serviceTypeStr string, amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.Contract.TransferFund(&_LedgerManager.TransactOpts, provider, serviceTypeStr, amount)
}

// TransferFund is a paid mutator transaction binding the contract method 0x2ba43b82.
//
// Solidity: function transferFund(address provider, string serviceTypeStr, uint256 amount) returns()
func (_LedgerManager *LedgerManagerTransactorSession) TransferFund(provider common.Address, serviceTypeStr string, amount *big.Int) (*types.Transaction, error) {
	return _LedgerManager.Contract.TransferFund(&_LedgerManager.TransactOpts, provider, serviceTypeStr, amount)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_LedgerManager *LedgerManagerTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _LedgerManager.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_LedgerManager *LedgerManagerSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _LedgerManager.Contract.TransferOwnership(&_LedgerManager.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_LedgerManager *LedgerManagerTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _LedgerManager.Contract.TransferOwnership(&_LedgerManager.TransactOpts, newOwner)
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_LedgerManager *LedgerManagerTransactor) Receive(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _LedgerManager.contract.RawTransact(opts, nil) // calldata is disallowed for receive function
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_LedgerManager *LedgerManagerSession) Receive() (*types.Transaction, error) {
	return _LedgerManager.Contract.Receive(&_LedgerManager.TransactOpts)
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_LedgerManager *LedgerManagerTransactorSession) Receive() (*types.Transaction, error) {
	return _LedgerManager.Contract.Receive(&_LedgerManager.TransactOpts)
}

// LedgerManagerOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the LedgerManager contract.
type LedgerManagerOwnershipTransferredIterator struct {
	Event *LedgerManagerOwnershipTransferred // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *LedgerManagerOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(LedgerManagerOwnershipTransferred)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(LedgerManagerOwnershipTransferred)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *LedgerManagerOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *LedgerManagerOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// LedgerManagerOwnershipTransferred represents a OwnershipTransferred event raised by the LedgerManager contract.
type LedgerManagerOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_LedgerManager *LedgerManagerFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*LedgerManagerOwnershipTransferredIterator, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _LedgerManager.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &LedgerManagerOwnershipTransferredIterator{contract: _LedgerManager.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_LedgerManager *LedgerManagerFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *LedgerManagerOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _LedgerManager.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(LedgerManagerOwnershipTransferred)
				if err := _LedgerManager.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseOwnershipTransferred is a log parse operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_LedgerManager *LedgerManagerFilterer) ParseOwnershipTransferred(log types.Log) (*LedgerManagerOwnershipTransferred, error) {
	event := new(LedgerManagerOwnershipTransferred)
	if err := _LedgerManager.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
