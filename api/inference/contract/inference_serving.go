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

// Account is an auto generated low-level Go binding around an user-defined struct.
type Account struct {
	User           common.Address
	Provider       common.Address
	Nonce          *big.Int
	Balance        *big.Int
	PendingRefund  *big.Int
	Signer         [2]*big.Int
	Refunds        []Refund
	AdditionalInfo string
}

// Refund is an auto generated low-level Go binding around an user-defined struct.
type Refund struct {
	Index     *big.Int
	Amount    *big.Int
	CreatedAt *big.Int
	Processed bool
}

// Service is an auto generated low-level Go binding around an user-defined struct.
type Service struct {
	Provider      common.Address
	Name          string
	ServiceType   string
	Url           string
	InputPrice    *big.Int
	OutputPrice   *big.Int
	UpdatedAt     *big.Int
	Model         string
	Verifiability string
}

// VerifierInput is an auto generated low-level Go binding around an user-defined struct.
type VerifierInput struct {
	InProof     []*big.Int
	ProofInputs []*big.Int
	NumChunks   *big.Int
	SegmentSize []*big.Int
}

// InferenceServingMetaData contains all meta data concerning the InferenceServing contract.
var InferenceServingMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountNotExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"reason\",\"type\":\"string\"}],\"name\":\"InvalidProofInputs\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceNotExist\",\"type\":\"error\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"name\":\"BalanceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"RefundRequested\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceRemoved\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"}],\"name\":\"ServiceUpdated\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"accountExists\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256[2]\",\"name\":\"signer\",\"type\":\"uint256[2]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"name\":\"addAccount\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"}],\"name\":\"addOrUpdateService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"batchVerifierAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"deleteAccount\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getAccount\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"signer\",\"type\":\"uint256[2]\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"internalType\":\"structAccount\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllAccounts\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"signer\",\"type\":\"uint256[2]\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"internalType\":\"structAccount[]\",\"name\":\"\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllServices\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"}],\"internalType\":\"structService[]\",\"name\":\"services\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"getService\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"}],\"internalType\":\"structService\",\"name\":\"service\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"_batchVerifierAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_ledgerAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"}],\"name\":\"initialize\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"initialized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"ledgerAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"lockTime\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"processRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"totalAmount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"removeService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"requestRefundAll\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"uint256[]\",\"name\":\"inProof\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256[]\",\"name\":\"proofInputs\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256\",\"name\":\"numChunks\",\"type\":\"uint256\"},{\"internalType\":\"uint256[]\",\"name\":\"segmentSize\",\"type\":\"uint256[]\"}],\"internalType\":\"structVerifierInput\",\"name\":\"verifierInput\",\"type\":\"tuple\"}],\"name\":\"settleFees\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"_batchVerifierAddress\",\"type\":\"address\"}],\"name\":\"updateBatchVerifierAddress\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"}],\"name\":\"updateLockTime\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// InferenceServingABI is the input ABI used to generate the binding from.
// Deprecated: Use InferenceServingMetaData.ABI instead.
var InferenceServingABI = InferenceServingMetaData.ABI

// InferenceServing is an auto generated Go binding around an Ethereum contract.
type InferenceServing struct {
	InferenceServingCaller     // Read-only binding to the contract
	InferenceServingTransactor // Write-only binding to the contract
	InferenceServingFilterer   // Log filterer for contract events
}

// InferenceServingCaller is an auto generated read-only Go binding around an Ethereum contract.
type InferenceServingCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// InferenceServingTransactor is an auto generated write-only Go binding around an Ethereum contract.
type InferenceServingTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// InferenceServingFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type InferenceServingFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// InferenceServingSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type InferenceServingSession struct {
	Contract     *InferenceServing // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// InferenceServingCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type InferenceServingCallerSession struct {
	Contract *InferenceServingCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts           // Call options to use throughout this session
}

// InferenceServingTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type InferenceServingTransactorSession struct {
	Contract     *InferenceServingTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts           // Transaction auth options to use throughout this session
}

// InferenceServingRaw is an auto generated low-level Go binding around an Ethereum contract.
type InferenceServingRaw struct {
	Contract *InferenceServing // Generic contract binding to access the raw methods on
}

// InferenceServingCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type InferenceServingCallerRaw struct {
	Contract *InferenceServingCaller // Generic read-only contract binding to access the raw methods on
}

// InferenceServingTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type InferenceServingTransactorRaw struct {
	Contract *InferenceServingTransactor // Generic write-only contract binding to access the raw methods on
}

// NewInferenceServing creates a new instance of InferenceServing, bound to a specific deployed contract.
func NewInferenceServing(address common.Address, backend bind.ContractBackend) (*InferenceServing, error) {
	contract, err := bindInferenceServing(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &InferenceServing{InferenceServingCaller: InferenceServingCaller{contract: contract}, InferenceServingTransactor: InferenceServingTransactor{contract: contract}, InferenceServingFilterer: InferenceServingFilterer{contract: contract}}, nil
}

// NewInferenceServingCaller creates a new read-only instance of InferenceServing, bound to a specific deployed contract.
func NewInferenceServingCaller(address common.Address, caller bind.ContractCaller) (*InferenceServingCaller, error) {
	contract, err := bindInferenceServing(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &InferenceServingCaller{contract: contract}, nil
}

// NewInferenceServingTransactor creates a new write-only instance of InferenceServing, bound to a specific deployed contract.
func NewInferenceServingTransactor(address common.Address, transactor bind.ContractTransactor) (*InferenceServingTransactor, error) {
	contract, err := bindInferenceServing(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &InferenceServingTransactor{contract: contract}, nil
}

// NewInferenceServingFilterer creates a new log filterer instance of InferenceServing, bound to a specific deployed contract.
func NewInferenceServingFilterer(address common.Address, filterer bind.ContractFilterer) (*InferenceServingFilterer, error) {
	contract, err := bindInferenceServing(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &InferenceServingFilterer{contract: contract}, nil
}

// bindInferenceServing binds a generic wrapper to an already deployed contract.
func bindInferenceServing(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := InferenceServingMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_InferenceServing *InferenceServingRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _InferenceServing.Contract.InferenceServingCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_InferenceServing *InferenceServingRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _InferenceServing.Contract.InferenceServingTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_InferenceServing *InferenceServingRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _InferenceServing.Contract.InferenceServingTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_InferenceServing *InferenceServingCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _InferenceServing.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_InferenceServing *InferenceServingTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _InferenceServing.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_InferenceServing *InferenceServingTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _InferenceServing.Contract.contract.Transact(opts, method, params...)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_InferenceServing *InferenceServingCaller) AccountExists(opts *bind.CallOpts, user common.Address, provider common.Address) (bool, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "accountExists", user, provider)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_InferenceServing *InferenceServingSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _InferenceServing.Contract.AccountExists(&_InferenceServing.CallOpts, user, provider)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_InferenceServing *InferenceServingCallerSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _InferenceServing.Contract.AccountExists(&_InferenceServing.CallOpts, user, provider)
}

// BatchVerifierAddress is a free data retrieval call binding the contract method 0x371c22c5.
//
// Solidity: function batchVerifierAddress() view returns(address)
func (_InferenceServing *InferenceServingCaller) BatchVerifierAddress(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "batchVerifierAddress")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// BatchVerifierAddress is a free data retrieval call binding the contract method 0x371c22c5.
//
// Solidity: function batchVerifierAddress() view returns(address)
func (_InferenceServing *InferenceServingSession) BatchVerifierAddress() (common.Address, error) {
	return _InferenceServing.Contract.BatchVerifierAddress(&_InferenceServing.CallOpts)
}

// BatchVerifierAddress is a free data retrieval call binding the contract method 0x371c22c5.
//
// Solidity: function batchVerifierAddress() view returns(address)
func (_InferenceServing *InferenceServingCallerSession) BatchVerifierAddress() (common.Address, error) {
	return _InferenceServing.Contract.BatchVerifierAddress(&_InferenceServing.CallOpts)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string))
func (_InferenceServing *InferenceServingCaller) GetAccount(opts *bind.CallOpts, user common.Address, provider common.Address) (Account, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getAccount", user, provider)

	if err != nil {
		return *new(Account), err
	}

	out0 := *abi.ConvertType(out[0], new(Account)).(*Account)

	return out0, err

}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string))
func (_InferenceServing *InferenceServingSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _InferenceServing.Contract.GetAccount(&_InferenceServing.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string))
func (_InferenceServing *InferenceServingCallerSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _InferenceServing.Contract.GetAccount(&_InferenceServing.CallOpts, user, provider)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string)[])
func (_InferenceServing *InferenceServingCaller) GetAllAccounts(opts *bind.CallOpts) ([]Account, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getAllAccounts")

	if err != nil {
		return *new([]Account), err
	}

	out0 := *abi.ConvertType(out[0], new([]Account)).(*[]Account)

	return out0, err

}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string)[])
func (_InferenceServing *InferenceServingSession) GetAllAccounts() ([]Account, error) {
	return _InferenceServing.Contract.GetAllAccounts(&_InferenceServing.CallOpts)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string)[])
func (_InferenceServing *InferenceServingCallerSession) GetAllAccounts() ([]Account, error) {
	return _InferenceServing.Contract.GetAllAccounts(&_InferenceServing.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,string,uint256,uint256,uint256,string,string)[] services)
func (_InferenceServing *InferenceServingCaller) GetAllServices(opts *bind.CallOpts) ([]Service, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getAllServices")

	if err != nil {
		return *new([]Service), err
	}

	out0 := *abi.ConvertType(out[0], new([]Service)).(*[]Service)

	return out0, err

}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,string,uint256,uint256,uint256,string,string)[] services)
func (_InferenceServing *InferenceServingSession) GetAllServices() ([]Service, error) {
	return _InferenceServing.Contract.GetAllServices(&_InferenceServing.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,string,uint256,uint256,uint256,string,string)[] services)
func (_InferenceServing *InferenceServingCallerSession) GetAllServices() ([]Service, error) {
	return _InferenceServing.Contract.GetAllServices(&_InferenceServing.CallOpts)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,string,uint256,uint256,uint256,string,string) service)
func (_InferenceServing *InferenceServingCaller) GetService(opts *bind.CallOpts, provider common.Address, name string) (Service, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getService", provider, name)

	if err != nil {
		return *new(Service), err
	}

	out0 := *abi.ConvertType(out[0], new(Service)).(*Service)

	return out0, err

}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,string,uint256,uint256,uint256,string,string) service)
func (_InferenceServing *InferenceServingSession) GetService(provider common.Address, name string) (Service, error) {
	return _InferenceServing.Contract.GetService(&_InferenceServing.CallOpts, provider, name)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,string,uint256,uint256,uint256,string,string) service)
func (_InferenceServing *InferenceServingCallerSession) GetService(provider common.Address, name string) (Service, error) {
	return _InferenceServing.Contract.GetService(&_InferenceServing.CallOpts, provider, name)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_InferenceServing *InferenceServingCaller) Initialized(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "initialized")

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_InferenceServing *InferenceServingSession) Initialized() (bool, error) {
	return _InferenceServing.Contract.Initialized(&_InferenceServing.CallOpts)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_InferenceServing *InferenceServingCallerSession) Initialized() (bool, error) {
	return _InferenceServing.Contract.Initialized(&_InferenceServing.CallOpts)
}

// LedgerAddress is a free data retrieval call binding the contract method 0xd1d20056.
//
// Solidity: function ledgerAddress() view returns(address)
func (_InferenceServing *InferenceServingCaller) LedgerAddress(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "ledgerAddress")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// LedgerAddress is a free data retrieval call binding the contract method 0xd1d20056.
//
// Solidity: function ledgerAddress() view returns(address)
func (_InferenceServing *InferenceServingSession) LedgerAddress() (common.Address, error) {
	return _InferenceServing.Contract.LedgerAddress(&_InferenceServing.CallOpts)
}

// LedgerAddress is a free data retrieval call binding the contract method 0xd1d20056.
//
// Solidity: function ledgerAddress() view returns(address)
func (_InferenceServing *InferenceServingCallerSession) LedgerAddress() (common.Address, error) {
	return _InferenceServing.Contract.LedgerAddress(&_InferenceServing.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_InferenceServing *InferenceServingCaller) LockTime(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "lockTime")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_InferenceServing *InferenceServingSession) LockTime() (*big.Int, error) {
	return _InferenceServing.Contract.LockTime(&_InferenceServing.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_InferenceServing *InferenceServingCallerSession) LockTime() (*big.Int, error) {
	return _InferenceServing.Contract.LockTime(&_InferenceServing.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_InferenceServing *InferenceServingCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "owner")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_InferenceServing *InferenceServingSession) Owner() (common.Address, error) {
	return _InferenceServing.Contract.Owner(&_InferenceServing.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_InferenceServing *InferenceServingCallerSession) Owner() (common.Address, error) {
	return _InferenceServing.Contract.Owner(&_InferenceServing.CallOpts)
}

// AddAccount is a paid mutator transaction binding the contract method 0x4bc3aff4.
//
// Solidity: function addAccount(address user, address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_InferenceServing *InferenceServingTransactor) AddAccount(opts *bind.TransactOpts, user common.Address, provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "addAccount", user, provider, signer, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0x4bc3aff4.
//
// Solidity: function addAccount(address user, address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_InferenceServing *InferenceServingSession) AddAccount(user common.Address, provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddAccount(&_InferenceServing.TransactOpts, user, provider, signer, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0x4bc3aff4.
//
// Solidity: function addAccount(address user, address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_InferenceServing *InferenceServingTransactorSession) AddAccount(user common.Address, provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddAccount(&_InferenceServing.TransactOpts, user, provider, signer, additionalInfo)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x6341b2d1.
//
// Solidity: function addOrUpdateService(string name, string serviceType, string url, string model, string verifiability, uint256 inputPrice, uint256 outputPrice) returns()
func (_InferenceServing *InferenceServingTransactor) AddOrUpdateService(opts *bind.TransactOpts, name string, serviceType string, url string, model string, verifiability string, inputPrice *big.Int, outputPrice *big.Int) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "addOrUpdateService", name, serviceType, url, model, verifiability, inputPrice, outputPrice)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x6341b2d1.
//
// Solidity: function addOrUpdateService(string name, string serviceType, string url, string model, string verifiability, uint256 inputPrice, uint256 outputPrice) returns()
func (_InferenceServing *InferenceServingSession) AddOrUpdateService(name string, serviceType string, url string, model string, verifiability string, inputPrice *big.Int, outputPrice *big.Int) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddOrUpdateService(&_InferenceServing.TransactOpts, name, serviceType, url, model, verifiability, inputPrice, outputPrice)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x6341b2d1.
//
// Solidity: function addOrUpdateService(string name, string serviceType, string url, string model, string verifiability, uint256 inputPrice, uint256 outputPrice) returns()
func (_InferenceServing *InferenceServingTransactorSession) AddOrUpdateService(name string, serviceType string, url string, model string, verifiability string, inputPrice *big.Int, outputPrice *big.Int) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddOrUpdateService(&_InferenceServing.TransactOpts, name, serviceType, url, model, verifiability, inputPrice, outputPrice)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_InferenceServing *InferenceServingTransactor) DeleteAccount(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "deleteAccount", user, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_InferenceServing *InferenceServingSession) DeleteAccount(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.DeleteAccount(&_InferenceServing.TransactOpts, user, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_InferenceServing *InferenceServingTransactorSession) DeleteAccount(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.DeleteAccount(&_InferenceServing.TransactOpts, user, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0x3f54d973.
//
// Solidity: function depositFund(address user, address provider) payable returns()
func (_InferenceServing *InferenceServingTransactor) DepositFund(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "depositFund", user, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0x3f54d973.
//
// Solidity: function depositFund(address user, address provider) payable returns()
func (_InferenceServing *InferenceServingSession) DepositFund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.DepositFund(&_InferenceServing.TransactOpts, user, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0x3f54d973.
//
// Solidity: function depositFund(address user, address provider) payable returns()
func (_InferenceServing *InferenceServingTransactorSession) DepositFund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.DepositFund(&_InferenceServing.TransactOpts, user, provider)
}

// Initialize is a paid mutator transaction binding the contract method 0x754d1d54.
//
// Solidity: function initialize(uint256 _locktime, address _batchVerifierAddress, address _ledgerAddress, address owner) returns()
func (_InferenceServing *InferenceServingTransactor) Initialize(opts *bind.TransactOpts, _locktime *big.Int, _batchVerifierAddress common.Address, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "initialize", _locktime, _batchVerifierAddress, _ledgerAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0x754d1d54.
//
// Solidity: function initialize(uint256 _locktime, address _batchVerifierAddress, address _ledgerAddress, address owner) returns()
func (_InferenceServing *InferenceServingSession) Initialize(_locktime *big.Int, _batchVerifierAddress common.Address, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.Initialize(&_InferenceServing.TransactOpts, _locktime, _batchVerifierAddress, _ledgerAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0x754d1d54.
//
// Solidity: function initialize(uint256 _locktime, address _batchVerifierAddress, address _ledgerAddress, address owner) returns()
func (_InferenceServing *InferenceServingTransactorSession) Initialize(_locktime *big.Int, _batchVerifierAddress common.Address, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.Initialize(&_InferenceServing.TransactOpts, _locktime, _batchVerifierAddress, _ledgerAddress, owner)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_InferenceServing *InferenceServingTransactor) ProcessRefund(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "processRefund", user, provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_InferenceServing *InferenceServingSession) ProcessRefund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.ProcessRefund(&_InferenceServing.TransactOpts, user, provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_InferenceServing *InferenceServingTransactorSession) ProcessRefund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.ProcessRefund(&_InferenceServing.TransactOpts, user, provider)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_InferenceServing *InferenceServingTransactor) RemoveService(opts *bind.TransactOpts, name string) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "removeService", name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_InferenceServing *InferenceServingSession) RemoveService(name string) (*types.Transaction, error) {
	return _InferenceServing.Contract.RemoveService(&_InferenceServing.TransactOpts, name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_InferenceServing *InferenceServingTransactorSession) RemoveService(name string) (*types.Transaction, error) {
	return _InferenceServing.Contract.RemoveService(&_InferenceServing.TransactOpts, name)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_InferenceServing *InferenceServingTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_InferenceServing *InferenceServingSession) RenounceOwnership() (*types.Transaction, error) {
	return _InferenceServing.Contract.RenounceOwnership(&_InferenceServing.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_InferenceServing *InferenceServingTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _InferenceServing.Contract.RenounceOwnership(&_InferenceServing.TransactOpts)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_InferenceServing *InferenceServingTransactor) RequestRefundAll(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "requestRefundAll", user, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_InferenceServing *InferenceServingSession) RequestRefundAll(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.RequestRefundAll(&_InferenceServing.TransactOpts, user, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_InferenceServing *InferenceServingTransactorSession) RequestRefundAll(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.RequestRefundAll(&_InferenceServing.TransactOpts, user, provider)
}

// SettleFees is a paid mutator transaction binding the contract method 0x78c00436.
//
// Solidity: function settleFees((uint256[],uint256[],uint256,uint256[]) verifierInput) returns()
func (_InferenceServing *InferenceServingTransactor) SettleFees(opts *bind.TransactOpts, verifierInput VerifierInput) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "settleFees", verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x78c00436.
//
// Solidity: function settleFees((uint256[],uint256[],uint256,uint256[]) verifierInput) returns()
func (_InferenceServing *InferenceServingSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _InferenceServing.Contract.SettleFees(&_InferenceServing.TransactOpts, verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x78c00436.
//
// Solidity: function settleFees((uint256[],uint256[],uint256,uint256[]) verifierInput) returns()
func (_InferenceServing *InferenceServingTransactorSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _InferenceServing.Contract.SettleFees(&_InferenceServing.TransactOpts, verifierInput)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_InferenceServing *InferenceServingTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_InferenceServing *InferenceServingSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.TransferOwnership(&_InferenceServing.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_InferenceServing *InferenceServingTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.TransferOwnership(&_InferenceServing.TransactOpts, newOwner)
}

// UpdateBatchVerifierAddress is a paid mutator transaction binding the contract method 0x746e78d7.
//
// Solidity: function updateBatchVerifierAddress(address _batchVerifierAddress) returns()
func (_InferenceServing *InferenceServingTransactor) UpdateBatchVerifierAddress(opts *bind.TransactOpts, _batchVerifierAddress common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "updateBatchVerifierAddress", _batchVerifierAddress)
}

// UpdateBatchVerifierAddress is a paid mutator transaction binding the contract method 0x746e78d7.
//
// Solidity: function updateBatchVerifierAddress(address _batchVerifierAddress) returns()
func (_InferenceServing *InferenceServingSession) UpdateBatchVerifierAddress(_batchVerifierAddress common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.UpdateBatchVerifierAddress(&_InferenceServing.TransactOpts, _batchVerifierAddress)
}

// UpdateBatchVerifierAddress is a paid mutator transaction binding the contract method 0x746e78d7.
//
// Solidity: function updateBatchVerifierAddress(address _batchVerifierAddress) returns()
func (_InferenceServing *InferenceServingTransactorSession) UpdateBatchVerifierAddress(_batchVerifierAddress common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.UpdateBatchVerifierAddress(&_InferenceServing.TransactOpts, _batchVerifierAddress)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_InferenceServing *InferenceServingTransactor) UpdateLockTime(opts *bind.TransactOpts, _locktime *big.Int) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "updateLockTime", _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_InferenceServing *InferenceServingSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _InferenceServing.Contract.UpdateLockTime(&_InferenceServing.TransactOpts, _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_InferenceServing *InferenceServingTransactorSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _InferenceServing.Contract.UpdateLockTime(&_InferenceServing.TransactOpts, _locktime)
}

// InferenceServingBalanceUpdatedIterator is returned from FilterBalanceUpdated and is used to iterate over the raw logs and unpacked data for BalanceUpdated events raised by the InferenceServing contract.
type InferenceServingBalanceUpdatedIterator struct {
	Event *InferenceServingBalanceUpdated // Event containing the contract specifics and raw log

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
func (it *InferenceServingBalanceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingBalanceUpdated)
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
		it.Event = new(InferenceServingBalanceUpdated)
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
func (it *InferenceServingBalanceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingBalanceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingBalanceUpdated represents a BalanceUpdated event raised by the InferenceServing contract.
type InferenceServingBalanceUpdated struct {
	User          common.Address
	Provider      common.Address
	Amount        *big.Int
	PendingRefund *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterBalanceUpdated is a free log retrieval operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_InferenceServing *InferenceServingFilterer) FilterBalanceUpdated(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*InferenceServingBalanceUpdatedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingBalanceUpdatedIterator{contract: _InferenceServing.contract, event: "BalanceUpdated", logs: logs, sub: sub}, nil
}

// WatchBalanceUpdated is a free log subscription operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_InferenceServing *InferenceServingFilterer) WatchBalanceUpdated(opts *bind.WatchOpts, sink chan<- *InferenceServingBalanceUpdated, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingBalanceUpdated)
				if err := _InferenceServing.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
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

// ParseBalanceUpdated is a log parse operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_InferenceServing *InferenceServingFilterer) ParseBalanceUpdated(log types.Log) (*InferenceServingBalanceUpdated, error) {
	event := new(InferenceServingBalanceUpdated)
	if err := _InferenceServing.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the InferenceServing contract.
type InferenceServingOwnershipTransferredIterator struct {
	Event *InferenceServingOwnershipTransferred // Event containing the contract specifics and raw log

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
func (it *InferenceServingOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingOwnershipTransferred)
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
		it.Event = new(InferenceServingOwnershipTransferred)
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
func (it *InferenceServingOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingOwnershipTransferred represents a OwnershipTransferred event raised by the InferenceServing contract.
type InferenceServingOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_InferenceServing *InferenceServingFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*InferenceServingOwnershipTransferredIterator, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingOwnershipTransferredIterator{contract: _InferenceServing.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_InferenceServing *InferenceServingFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *InferenceServingOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingOwnershipTransferred)
				if err := _InferenceServing.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
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
func (_InferenceServing *InferenceServingFilterer) ParseOwnershipTransferred(log types.Log) (*InferenceServingOwnershipTransferred, error) {
	event := new(InferenceServingOwnershipTransferred)
	if err := _InferenceServing.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingRefundRequestedIterator is returned from FilterRefundRequested and is used to iterate over the raw logs and unpacked data for RefundRequested events raised by the InferenceServing contract.
type InferenceServingRefundRequestedIterator struct {
	Event *InferenceServingRefundRequested // Event containing the contract specifics and raw log

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
func (it *InferenceServingRefundRequestedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingRefundRequested)
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
		it.Event = new(InferenceServingRefundRequested)
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
func (it *InferenceServingRefundRequestedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingRefundRequestedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingRefundRequested represents a RefundRequested event raised by the InferenceServing contract.
type InferenceServingRefundRequested struct {
	User      common.Address
	Provider  common.Address
	Index     *big.Int
	Timestamp *big.Int
	Raw       types.Log // Blockchain specific contextual infos
}

// FilterRefundRequested is a free log retrieval operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_InferenceServing *InferenceServingFilterer) FilterRefundRequested(opts *bind.FilterOpts, user []common.Address, provider []common.Address, index []*big.Int) (*InferenceServingRefundRequestedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var indexRule []interface{}
	for _, indexItem := range index {
		indexRule = append(indexRule, indexItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingRefundRequestedIterator{contract: _InferenceServing.contract, event: "RefundRequested", logs: logs, sub: sub}, nil
}

// WatchRefundRequested is a free log subscription operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_InferenceServing *InferenceServingFilterer) WatchRefundRequested(opts *bind.WatchOpts, sink chan<- *InferenceServingRefundRequested, user []common.Address, provider []common.Address, index []*big.Int) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var indexRule []interface{}
	for _, indexItem := range index {
		indexRule = append(indexRule, indexItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingRefundRequested)
				if err := _InferenceServing.contract.UnpackLog(event, "RefundRequested", log); err != nil {
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

// ParseRefundRequested is a log parse operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_InferenceServing *InferenceServingFilterer) ParseRefundRequested(log types.Log) (*InferenceServingRefundRequested, error) {
	event := new(InferenceServingRefundRequested)
	if err := _InferenceServing.contract.UnpackLog(event, "RefundRequested", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingServiceRemovedIterator is returned from FilterServiceRemoved and is used to iterate over the raw logs and unpacked data for ServiceRemoved events raised by the InferenceServing contract.
type InferenceServingServiceRemovedIterator struct {
	Event *InferenceServingServiceRemoved // Event containing the contract specifics and raw log

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
func (it *InferenceServingServiceRemovedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingServiceRemoved)
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
		it.Event = new(InferenceServingServiceRemoved)
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
func (it *InferenceServingServiceRemovedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingServiceRemovedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingServiceRemoved represents a ServiceRemoved event raised by the InferenceServing contract.
type InferenceServingServiceRemoved struct {
	Service common.Address
	Name    common.Hash
	Raw     types.Log // Blockchain specific contextual infos
}

// FilterServiceRemoved is a free log retrieval operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed service, string indexed name)
func (_InferenceServing *InferenceServingFilterer) FilterServiceRemoved(opts *bind.FilterOpts, service []common.Address, name []string) (*InferenceServingServiceRemovedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ServiceRemoved", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingServiceRemovedIterator{contract: _InferenceServing.contract, event: "ServiceRemoved", logs: logs, sub: sub}, nil
}

// WatchServiceRemoved is a free log subscription operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed service, string indexed name)
func (_InferenceServing *InferenceServingFilterer) WatchServiceRemoved(opts *bind.WatchOpts, sink chan<- *InferenceServingServiceRemoved, service []common.Address, name []string) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ServiceRemoved", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingServiceRemoved)
				if err := _InferenceServing.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
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

// ParseServiceRemoved is a log parse operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed service, string indexed name)
func (_InferenceServing *InferenceServingFilterer) ParseServiceRemoved(log types.Log) (*InferenceServingServiceRemoved, error) {
	event := new(InferenceServingServiceRemoved)
	if err := _InferenceServing.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingServiceUpdatedIterator is returned from FilterServiceUpdated and is used to iterate over the raw logs and unpacked data for ServiceUpdated events raised by the InferenceServing contract.
type InferenceServingServiceUpdatedIterator struct {
	Event *InferenceServingServiceUpdated // Event containing the contract specifics and raw log

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
func (it *InferenceServingServiceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingServiceUpdated)
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
		it.Event = new(InferenceServingServiceUpdated)
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
func (it *InferenceServingServiceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingServiceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingServiceUpdated represents a ServiceUpdated event raised by the InferenceServing contract.
type InferenceServingServiceUpdated struct {
	Service       common.Address
	Name          common.Hash
	ServiceType   string
	Url           string
	InputPrice    *big.Int
	OutputPrice   *big.Int
	UpdatedAt     *big.Int
	Model         string
	Verifiability string
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterServiceUpdated is a free log retrieval operation binding the contract event 0x95e1ef74a36b7d6ac766d338a4468c685d593739c3b7dc39e2aa5921a1e13932.
//
// Solidity: event ServiceUpdated(address indexed service, string indexed name, string serviceType, string url, uint256 inputPrice, uint256 outputPrice, uint256 updatedAt, string model, string verifiability)
func (_InferenceServing *InferenceServingFilterer) FilterServiceUpdated(opts *bind.FilterOpts, service []common.Address, name []string) (*InferenceServingServiceUpdatedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ServiceUpdated", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingServiceUpdatedIterator{contract: _InferenceServing.contract, event: "ServiceUpdated", logs: logs, sub: sub}, nil
}

// WatchServiceUpdated is a free log subscription operation binding the contract event 0x95e1ef74a36b7d6ac766d338a4468c685d593739c3b7dc39e2aa5921a1e13932.
//
// Solidity: event ServiceUpdated(address indexed service, string indexed name, string serviceType, string url, uint256 inputPrice, uint256 outputPrice, uint256 updatedAt, string model, string verifiability)
func (_InferenceServing *InferenceServingFilterer) WatchServiceUpdated(opts *bind.WatchOpts, sink chan<- *InferenceServingServiceUpdated, service []common.Address, name []string) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ServiceUpdated", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingServiceUpdated)
				if err := _InferenceServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
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

// ParseServiceUpdated is a log parse operation binding the contract event 0x95e1ef74a36b7d6ac766d338a4468c685d593739c3b7dc39e2aa5921a1e13932.
//
// Solidity: event ServiceUpdated(address indexed service, string indexed name, string serviceType, string url, uint256 inputPrice, uint256 outputPrice, uint256 updatedAt, string model, string verifiability)
func (_InferenceServing *InferenceServingFilterer) ParseServiceUpdated(log types.Log) (*InferenceServingServiceUpdated, error) {
	event := new(InferenceServingServiceUpdated)
	if err := _InferenceServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}