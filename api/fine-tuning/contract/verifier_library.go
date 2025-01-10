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

// VerifierLibraryMetaData contains all meta data concerning the VerifierLibrary contract.
var VerifierLibraryMetaData = &bind.MetaData{
	ABI: "[]",
}

// VerifierLibraryABI is the input ABI used to generate the binding from.
// Deprecated: Use VerifierLibraryMetaData.ABI instead.
var VerifierLibraryABI = VerifierLibraryMetaData.ABI

// VerifierLibrary is an auto generated Go binding around an Ethereum contract.
type VerifierLibrary struct {
	VerifierLibraryCaller     // Read-only binding to the contract
	VerifierLibraryTransactor // Write-only binding to the contract
	VerifierLibraryFilterer   // Log filterer for contract events
}

// VerifierLibraryCaller is an auto generated read-only Go binding around an Ethereum contract.
type VerifierLibraryCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// VerifierLibraryTransactor is an auto generated write-only Go binding around an Ethereum contract.
type VerifierLibraryTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// VerifierLibraryFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type VerifierLibraryFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// VerifierLibrarySession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type VerifierLibrarySession struct {
	Contract     *VerifierLibrary  // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// VerifierLibraryCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type VerifierLibraryCallerSession struct {
	Contract *VerifierLibraryCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts          // Call options to use throughout this session
}

// VerifierLibraryTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type VerifierLibraryTransactorSession struct {
	Contract     *VerifierLibraryTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts          // Transaction auth options to use throughout this session
}

// VerifierLibraryRaw is an auto generated low-level Go binding around an Ethereum contract.
type VerifierLibraryRaw struct {
	Contract *VerifierLibrary // Generic contract binding to access the raw methods on
}

// VerifierLibraryCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type VerifierLibraryCallerRaw struct {
	Contract *VerifierLibraryCaller // Generic read-only contract binding to access the raw methods on
}

// VerifierLibraryTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type VerifierLibraryTransactorRaw struct {
	Contract *VerifierLibraryTransactor // Generic write-only contract binding to access the raw methods on
}

// NewVerifierLibrary creates a new instance of VerifierLibrary, bound to a specific deployed contract.
func NewVerifierLibrary(address common.Address, backend bind.ContractBackend) (*VerifierLibrary, error) {
	contract, err := bindVerifierLibrary(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &VerifierLibrary{VerifierLibraryCaller: VerifierLibraryCaller{contract: contract}, VerifierLibraryTransactor: VerifierLibraryTransactor{contract: contract}, VerifierLibraryFilterer: VerifierLibraryFilterer{contract: contract}}, nil
}

// NewVerifierLibraryCaller creates a new read-only instance of VerifierLibrary, bound to a specific deployed contract.
func NewVerifierLibraryCaller(address common.Address, caller bind.ContractCaller) (*VerifierLibraryCaller, error) {
	contract, err := bindVerifierLibrary(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &VerifierLibraryCaller{contract: contract}, nil
}

// NewVerifierLibraryTransactor creates a new write-only instance of VerifierLibrary, bound to a specific deployed contract.
func NewVerifierLibraryTransactor(address common.Address, transactor bind.ContractTransactor) (*VerifierLibraryTransactor, error) {
	contract, err := bindVerifierLibrary(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &VerifierLibraryTransactor{contract: contract}, nil
}

// NewVerifierLibraryFilterer creates a new log filterer instance of VerifierLibrary, bound to a specific deployed contract.
func NewVerifierLibraryFilterer(address common.Address, filterer bind.ContractFilterer) (*VerifierLibraryFilterer, error) {
	contract, err := bindVerifierLibrary(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &VerifierLibraryFilterer{contract: contract}, nil
}

// bindVerifierLibrary binds a generic wrapper to an already deployed contract.
func bindVerifierLibrary(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := VerifierLibraryMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_VerifierLibrary *VerifierLibraryRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _VerifierLibrary.Contract.VerifierLibraryCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_VerifierLibrary *VerifierLibraryRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _VerifierLibrary.Contract.VerifierLibraryTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_VerifierLibrary *VerifierLibraryRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _VerifierLibrary.Contract.VerifierLibraryTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_VerifierLibrary *VerifierLibraryCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _VerifierLibrary.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_VerifierLibrary *VerifierLibraryTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _VerifierLibrary.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_VerifierLibrary *VerifierLibraryTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _VerifierLibrary.Contract.contract.Transact(opts, method, params...)
}
