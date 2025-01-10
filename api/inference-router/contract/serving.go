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

// ServingMetaData contains all meta data concerning the Serving contract.
var ServingMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountNotexists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"InsufficientBalance\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"reason\",\"type\":\"string\"}],\"name\":\"InvalidProofInputs\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundInvalid\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundLocked\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"RefundProcessed\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceNotexist\",\"type\":\"error\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"name\":\"BalanceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"RefundRequested\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceRemoved\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"}],\"name\":\"ServiceUpdated\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256[2]\",\"name\":\"signer\",\"type\":\"uint256[2]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"name\":\"addAccount\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"}],\"name\":\"addOrUpdateService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"batchVerifierAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"deleteAccount\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getAccount\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"signer\",\"type\":\"uint256[2]\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"internalType\":\"structAccount\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllAccounts\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"signer\",\"type\":\"uint256[2]\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"internalType\":\"structAccount[]\",\"name\":\"\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllServices\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"}],\"internalType\":\"structService[]\",\"name\":\"services\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"getService\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"}],\"internalType\":\"structService\",\"name\":\"service\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"_batchVerifierAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"}],\"name\":\"initialize\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"initialized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"lockTime\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256[]\",\"name\":\"indices\",\"type\":\"uint256[]\"}],\"name\":\"processRefund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"removeService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"requestRefund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"uint256[]\",\"name\":\"inProof\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256[]\",\"name\":\"proofInputs\",\"type\":\"uint256[]\"},{\"internalType\":\"uint256\",\"name\":\"numChunks\",\"type\":\"uint256\"},{\"internalType\":\"uint256[]\",\"name\":\"segmentSize\",\"type\":\"uint256[]\"}],\"internalType\":\"structVerifierInput\",\"name\":\"verifierInput\",\"type\":\"tuple\"}],\"name\":\"settleFees\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"_batchVerifierAddress\",\"type\":\"address\"}],\"name\":\"updateBatchVerifierAddress\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"}],\"name\":\"updateLockTime\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// ServingABI is the input ABI used to generate the binding from.
// Deprecated: Use ServingMetaData.ABI instead.
var ServingABI = ServingMetaData.ABI

// Serving is an auto generated Go binding around an Ethereum contract.
type Serving struct {
	ServingCaller     // Read-only binding to the contract
	ServingTransactor // Write-only binding to the contract
	ServingFilterer   // Log filterer for contract events
}

// ServingCaller is an auto generated read-only Go binding around an Ethereum contract.
type ServingCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ServingTransactor is an auto generated write-only Go binding around an Ethereum contract.
type ServingTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ServingFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type ServingFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ServingSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type ServingSession struct {
	Contract     *Serving          // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// ServingCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type ServingCallerSession struct {
	Contract *ServingCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts  // Call options to use throughout this session
}

// ServingTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type ServingTransactorSession struct {
	Contract     *ServingTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts  // Transaction auth options to use throughout this session
}

// ServingRaw is an auto generated low-level Go binding around an Ethereum contract.
type ServingRaw struct {
	Contract *Serving // Generic contract binding to access the raw methods on
}

// ServingCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type ServingCallerRaw struct {
	Contract *ServingCaller // Generic read-only contract binding to access the raw methods on
}

// ServingTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type ServingTransactorRaw struct {
	Contract *ServingTransactor // Generic write-only contract binding to access the raw methods on
}

// NewServing creates a new instance of Serving, bound to a specific deployed contract.
func NewServing(address common.Address, backend bind.ContractBackend) (*Serving, error) {
	contract, err := bindServing(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Serving{ServingCaller: ServingCaller{contract: contract}, ServingTransactor: ServingTransactor{contract: contract}, ServingFilterer: ServingFilterer{contract: contract}}, nil
}

// NewServingCaller creates a new read-only instance of Serving, bound to a specific deployed contract.
func NewServingCaller(address common.Address, caller bind.ContractCaller) (*ServingCaller, error) {
	contract, err := bindServing(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ServingCaller{contract: contract}, nil
}

// NewServingTransactor creates a new write-only instance of Serving, bound to a specific deployed contract.
func NewServingTransactor(address common.Address, transactor bind.ContractTransactor) (*ServingTransactor, error) {
	contract, err := bindServing(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &ServingTransactor{contract: contract}, nil
}

// NewServingFilterer creates a new log filterer instance of Serving, bound to a specific deployed contract.
func NewServingFilterer(address common.Address, filterer bind.ContractFilterer) (*ServingFilterer, error) {
	contract, err := bindServing(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &ServingFilterer{contract: contract}, nil
}

// bindServing binds a generic wrapper to an already deployed contract.
func bindServing(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := ServingMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Serving *ServingRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Serving.Contract.ServingCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Serving *ServingRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Serving.Contract.ServingTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Serving *ServingRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Serving.Contract.ServingTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Serving *ServingCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Serving.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Serving *ServingTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Serving.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Serving *ServingTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Serving.Contract.contract.Transact(opts, method, params...)
}

// BatchVerifierAddress is a free data retrieval call binding the contract method 0x371c22c5.
//
// Solidity: function batchVerifierAddress() view returns(address)
func (_Serving *ServingCaller) BatchVerifierAddress(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "batchVerifierAddress")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// BatchVerifierAddress is a free data retrieval call binding the contract method 0x371c22c5.
//
// Solidity: function batchVerifierAddress() view returns(address)
func (_Serving *ServingSession) BatchVerifierAddress() (common.Address, error) {
	return _Serving.Contract.BatchVerifierAddress(&_Serving.CallOpts)
}

// BatchVerifierAddress is a free data retrieval call binding the contract method 0x371c22c5.
//
// Solidity: function batchVerifierAddress() view returns(address)
func (_Serving *ServingCallerSession) BatchVerifierAddress() (common.Address, error) {
	return _Serving.Contract.BatchVerifierAddress(&_Serving.CallOpts)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string))
func (_Serving *ServingCaller) GetAccount(opts *bind.CallOpts, user common.Address, provider common.Address) (Account, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "getAccount", user, provider)

	if err != nil {
		return *new(Account), err
	}

	out0 := *abi.ConvertType(out[0], new(Account)).(*Account)

	return out0, err

}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string))
func (_Serving *ServingSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _Serving.Contract.GetAccount(&_Serving.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string))
func (_Serving *ServingCallerSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _Serving.Contract.GetAccount(&_Serving.CallOpts, user, provider)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string)[])
func (_Serving *ServingCaller) GetAllAccounts(opts *bind.CallOpts) ([]Account, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "getAllAccounts")

	if err != nil {
		return *new([]Account), err
	}

	out0 := *abi.ConvertType(out[0], new([]Account)).(*[]Account)

	return out0, err

}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string)[])
func (_Serving *ServingSession) GetAllAccounts() ([]Account, error) {
	return _Serving.Contract.GetAllAccounts(&_Serving.CallOpts)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string)[])
func (_Serving *ServingCallerSession) GetAllAccounts() ([]Account, error) {
	return _Serving.Contract.GetAllAccounts(&_Serving.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,string,uint256,uint256,uint256,string,string)[] services)
func (_Serving *ServingCaller) GetAllServices(opts *bind.CallOpts) ([]Service, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "getAllServices")

	if err != nil {
		return *new([]Service), err
	}

	out0 := *abi.ConvertType(out[0], new([]Service)).(*[]Service)

	return out0, err

}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,string,uint256,uint256,uint256,string,string)[] services)
func (_Serving *ServingSession) GetAllServices() ([]Service, error) {
	return _Serving.Contract.GetAllServices(&_Serving.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,string,uint256,uint256,uint256,string,string)[] services)
func (_Serving *ServingCallerSession) GetAllServices() ([]Service, error) {
	return _Serving.Contract.GetAllServices(&_Serving.CallOpts)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,string,uint256,uint256,uint256,string,string) service)
func (_Serving *ServingCaller) GetService(opts *bind.CallOpts, provider common.Address, name string) (Service, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "getService", provider, name)

	if err != nil {
		return *new(Service), err
	}

	out0 := *abi.ConvertType(out[0], new(Service)).(*Service)

	return out0, err

}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,string,uint256,uint256,uint256,string,string) service)
func (_Serving *ServingSession) GetService(provider common.Address, name string) (Service, error) {
	return _Serving.Contract.GetService(&_Serving.CallOpts, provider, name)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,string,uint256,uint256,uint256,string,string) service)
func (_Serving *ServingCallerSession) GetService(provider common.Address, name string) (Service, error) {
	return _Serving.Contract.GetService(&_Serving.CallOpts, provider, name)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_Serving *ServingCaller) Initialized(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "initialized")

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_Serving *ServingSession) Initialized() (bool, error) {
	return _Serving.Contract.Initialized(&_Serving.CallOpts)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_Serving *ServingCallerSession) Initialized() (bool, error) {
	return _Serving.Contract.Initialized(&_Serving.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_Serving *ServingCaller) LockTime(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "lockTime")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_Serving *ServingSession) LockTime() (*big.Int, error) {
	return _Serving.Contract.LockTime(&_Serving.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_Serving *ServingCallerSession) LockTime() (*big.Int, error) {
	return _Serving.Contract.LockTime(&_Serving.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Serving *ServingCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Serving.contract.Call(opts, &out, "owner")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Serving *ServingSession) Owner() (common.Address, error) {
	return _Serving.Contract.Owner(&_Serving.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Serving *ServingCallerSession) Owner() (common.Address, error) {
	return _Serving.Contract.Owner(&_Serving.CallOpts)
}

// AddAccount is a paid mutator transaction binding the contract method 0x0b1d1392.
//
// Solidity: function addAccount(address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_Serving *ServingTransactor) AddAccount(opts *bind.TransactOpts, provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "addAccount", provider, signer, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0x0b1d1392.
//
// Solidity: function addAccount(address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_Serving *ServingSession) AddAccount(provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _Serving.Contract.AddAccount(&_Serving.TransactOpts, provider, signer, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0x0b1d1392.
//
// Solidity: function addAccount(address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_Serving *ServingTransactorSession) AddAccount(provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _Serving.Contract.AddAccount(&_Serving.TransactOpts, provider, signer, additionalInfo)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x6341b2d1.
//
// Solidity: function addOrUpdateService(string name, string serviceType, string url, string model, string verifiability, uint256 inputPrice, uint256 outputPrice) returns()
func (_Serving *ServingTransactor) AddOrUpdateService(opts *bind.TransactOpts, name string, serviceType string, url string, model string, verifiability string, inputPrice *big.Int, outputPrice *big.Int) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "addOrUpdateService", name, serviceType, url, model, verifiability, inputPrice, outputPrice)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x6341b2d1.
//
// Solidity: function addOrUpdateService(string name, string serviceType, string url, string model, string verifiability, uint256 inputPrice, uint256 outputPrice) returns()
func (_Serving *ServingSession) AddOrUpdateService(name string, serviceType string, url string, model string, verifiability string, inputPrice *big.Int, outputPrice *big.Int) (*types.Transaction, error) {
	return _Serving.Contract.AddOrUpdateService(&_Serving.TransactOpts, name, serviceType, url, model, verifiability, inputPrice, outputPrice)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x6341b2d1.
//
// Solidity: function addOrUpdateService(string name, string serviceType, string url, string model, string verifiability, uint256 inputPrice, uint256 outputPrice) returns()
func (_Serving *ServingTransactorSession) AddOrUpdateService(name string, serviceType string, url string, model string, verifiability string, inputPrice *big.Int, outputPrice *big.Int) (*types.Transaction, error) {
	return _Serving.Contract.AddOrUpdateService(&_Serving.TransactOpts, name, serviceType, url, model, verifiability, inputPrice, outputPrice)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x4c1b64cb.
//
// Solidity: function deleteAccount(address provider) returns()
func (_Serving *ServingTransactor) DeleteAccount(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "deleteAccount", provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x4c1b64cb.
//
// Solidity: function deleteAccount(address provider) returns()
func (_Serving *ServingSession) DeleteAccount(provider common.Address) (*types.Transaction, error) {
	return _Serving.Contract.DeleteAccount(&_Serving.TransactOpts, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x4c1b64cb.
//
// Solidity: function deleteAccount(address provider) returns()
func (_Serving *ServingTransactorSession) DeleteAccount(provider common.Address) (*types.Transaction, error) {
	return _Serving.Contract.DeleteAccount(&_Serving.TransactOpts, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0xe12d4a52.
//
// Solidity: function depositFund(address provider) payable returns()
func (_Serving *ServingTransactor) DepositFund(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "depositFund", provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0xe12d4a52.
//
// Solidity: function depositFund(address provider) payable returns()
func (_Serving *ServingSession) DepositFund(provider common.Address) (*types.Transaction, error) {
	return _Serving.Contract.DepositFund(&_Serving.TransactOpts, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0xe12d4a52.
//
// Solidity: function depositFund(address provider) payable returns()
func (_Serving *ServingTransactorSession) DepositFund(provider common.Address) (*types.Transaction, error) {
	return _Serving.Contract.DepositFund(&_Serving.TransactOpts, provider)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _batchVerifierAddress, address owner) returns()
func (_Serving *ServingTransactor) Initialize(opts *bind.TransactOpts, _locktime *big.Int, _batchVerifierAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "initialize", _locktime, _batchVerifierAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _batchVerifierAddress, address owner) returns()
func (_Serving *ServingSession) Initialize(_locktime *big.Int, _batchVerifierAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _Serving.Contract.Initialize(&_Serving.TransactOpts, _locktime, _batchVerifierAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _batchVerifierAddress, address owner) returns()
func (_Serving *ServingTransactorSession) Initialize(_locktime *big.Int, _batchVerifierAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _Serving.Contract.Initialize(&_Serving.TransactOpts, _locktime, _batchVerifierAddress, owner)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4da824a8.
//
// Solidity: function processRefund(address provider, uint256[] indices) returns()
func (_Serving *ServingTransactor) ProcessRefund(opts *bind.TransactOpts, provider common.Address, indices []*big.Int) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "processRefund", provider, indices)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4da824a8.
//
// Solidity: function processRefund(address provider, uint256[] indices) returns()
func (_Serving *ServingSession) ProcessRefund(provider common.Address, indices []*big.Int) (*types.Transaction, error) {
	return _Serving.Contract.ProcessRefund(&_Serving.TransactOpts, provider, indices)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4da824a8.
//
// Solidity: function processRefund(address provider, uint256[] indices) returns()
func (_Serving *ServingTransactorSession) ProcessRefund(provider common.Address, indices []*big.Int) (*types.Transaction, error) {
	return _Serving.Contract.ProcessRefund(&_Serving.TransactOpts, provider, indices)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_Serving *ServingTransactor) RemoveService(opts *bind.TransactOpts, name string) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "removeService", name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_Serving *ServingSession) RemoveService(name string) (*types.Transaction, error) {
	return _Serving.Contract.RemoveService(&_Serving.TransactOpts, name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_Serving *ServingTransactorSession) RemoveService(name string) (*types.Transaction, error) {
	return _Serving.Contract.RemoveService(&_Serving.TransactOpts, name)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Serving *ServingTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Serving *ServingSession) RenounceOwnership() (*types.Transaction, error) {
	return _Serving.Contract.RenounceOwnership(&_Serving.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Serving *ServingTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _Serving.Contract.RenounceOwnership(&_Serving.TransactOpts)
}

// RequestRefund is a paid mutator transaction binding the contract method 0x99652de7.
//
// Solidity: function requestRefund(address provider, uint256 amount) returns()
func (_Serving *ServingTransactor) RequestRefund(opts *bind.TransactOpts, provider common.Address, amount *big.Int) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "requestRefund", provider, amount)
}

// RequestRefund is a paid mutator transaction binding the contract method 0x99652de7.
//
// Solidity: function requestRefund(address provider, uint256 amount) returns()
func (_Serving *ServingSession) RequestRefund(provider common.Address, amount *big.Int) (*types.Transaction, error) {
	return _Serving.Contract.RequestRefund(&_Serving.TransactOpts, provider, amount)
}

// RequestRefund is a paid mutator transaction binding the contract method 0x99652de7.
//
// Solidity: function requestRefund(address provider, uint256 amount) returns()
func (_Serving *ServingTransactorSession) RequestRefund(provider common.Address, amount *big.Int) (*types.Transaction, error) {
	return _Serving.Contract.RequestRefund(&_Serving.TransactOpts, provider, amount)
}

// SettleFees is a paid mutator transaction binding the contract method 0x78c00436.
//
// Solidity: function settleFees((uint256[],uint256[],uint256,uint256[]) verifierInput) returns()
func (_Serving *ServingTransactor) SettleFees(opts *bind.TransactOpts, verifierInput VerifierInput) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "settleFees", verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x78c00436.
//
// Solidity: function settleFees((uint256[],uint256[],uint256,uint256[]) verifierInput) returns()
func (_Serving *ServingSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _Serving.Contract.SettleFees(&_Serving.TransactOpts, verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x78c00436.
//
// Solidity: function settleFees((uint256[],uint256[],uint256,uint256[]) verifierInput) returns()
func (_Serving *ServingTransactorSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _Serving.Contract.SettleFees(&_Serving.TransactOpts, verifierInput)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Serving *ServingTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Serving *ServingSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Serving.Contract.TransferOwnership(&_Serving.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Serving *ServingTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Serving.Contract.TransferOwnership(&_Serving.TransactOpts, newOwner)
}

// UpdateBatchVerifierAddress is a paid mutator transaction binding the contract method 0x746e78d7.
//
// Solidity: function updateBatchVerifierAddress(address _batchVerifierAddress) returns()
func (_Serving *ServingTransactor) UpdateBatchVerifierAddress(opts *bind.TransactOpts, _batchVerifierAddress common.Address) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "updateBatchVerifierAddress", _batchVerifierAddress)
}

// UpdateBatchVerifierAddress is a paid mutator transaction binding the contract method 0x746e78d7.
//
// Solidity: function updateBatchVerifierAddress(address _batchVerifierAddress) returns()
func (_Serving *ServingSession) UpdateBatchVerifierAddress(_batchVerifierAddress common.Address) (*types.Transaction, error) {
	return _Serving.Contract.UpdateBatchVerifierAddress(&_Serving.TransactOpts, _batchVerifierAddress)
}

// UpdateBatchVerifierAddress is a paid mutator transaction binding the contract method 0x746e78d7.
//
// Solidity: function updateBatchVerifierAddress(address _batchVerifierAddress) returns()
func (_Serving *ServingTransactorSession) UpdateBatchVerifierAddress(_batchVerifierAddress common.Address) (*types.Transaction, error) {
	return _Serving.Contract.UpdateBatchVerifierAddress(&_Serving.TransactOpts, _batchVerifierAddress)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_Serving *ServingTransactor) UpdateLockTime(opts *bind.TransactOpts, _locktime *big.Int) (*types.Transaction, error) {
	return _Serving.contract.Transact(opts, "updateLockTime", _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_Serving *ServingSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _Serving.Contract.UpdateLockTime(&_Serving.TransactOpts, _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_Serving *ServingTransactorSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _Serving.Contract.UpdateLockTime(&_Serving.TransactOpts, _locktime)
}

// ServingBalanceUpdatedIterator is returned from FilterBalanceUpdated and is used to iterate over the raw logs and unpacked data for BalanceUpdated events raised by the Serving contract.
type ServingBalanceUpdatedIterator struct {
	Event *ServingBalanceUpdated // Event containing the contract specifics and raw log

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
func (it *ServingBalanceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(ServingBalanceUpdated)
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
		it.Event = new(ServingBalanceUpdated)
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
func (it *ServingBalanceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *ServingBalanceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// ServingBalanceUpdated represents a BalanceUpdated event raised by the Serving contract.
type ServingBalanceUpdated struct {
	User          common.Address
	Provider      common.Address
	Amount        *big.Int
	PendingRefund *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterBalanceUpdated is a free log retrieval operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_Serving *ServingFilterer) FilterBalanceUpdated(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*ServingBalanceUpdatedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _Serving.contract.FilterLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &ServingBalanceUpdatedIterator{contract: _Serving.contract, event: "BalanceUpdated", logs: logs, sub: sub}, nil
}

// WatchBalanceUpdated is a free log subscription operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_Serving *ServingFilterer) WatchBalanceUpdated(opts *bind.WatchOpts, sink chan<- *ServingBalanceUpdated, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _Serving.contract.WatchLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(ServingBalanceUpdated)
				if err := _Serving.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
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
func (_Serving *ServingFilterer) ParseBalanceUpdated(log types.Log) (*ServingBalanceUpdated, error) {
	event := new(ServingBalanceUpdated)
	if err := _Serving.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// ServingOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the Serving contract.
type ServingOwnershipTransferredIterator struct {
	Event *ServingOwnershipTransferred // Event containing the contract specifics and raw log

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
func (it *ServingOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(ServingOwnershipTransferred)
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
		it.Event = new(ServingOwnershipTransferred)
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
func (it *ServingOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *ServingOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// ServingOwnershipTransferred represents a OwnershipTransferred event raised by the Serving contract.
type ServingOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Serving *ServingFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*ServingOwnershipTransferredIterator, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Serving.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &ServingOwnershipTransferredIterator{contract: _Serving.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Serving *ServingFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *ServingOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Serving.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(ServingOwnershipTransferred)
				if err := _Serving.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
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
func (_Serving *ServingFilterer) ParseOwnershipTransferred(log types.Log) (*ServingOwnershipTransferred, error) {
	event := new(ServingOwnershipTransferred)
	if err := _Serving.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// ServingRefundRequestedIterator is returned from FilterRefundRequested and is used to iterate over the raw logs and unpacked data for RefundRequested events raised by the Serving contract.
type ServingRefundRequestedIterator struct {
	Event *ServingRefundRequested // Event containing the contract specifics and raw log

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
func (it *ServingRefundRequestedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(ServingRefundRequested)
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
		it.Event = new(ServingRefundRequested)
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
func (it *ServingRefundRequestedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *ServingRefundRequestedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// ServingRefundRequested represents a RefundRequested event raised by the Serving contract.
type ServingRefundRequested struct {
	User      common.Address
	Provider  common.Address
	Index     *big.Int
	Timestamp *big.Int
	Raw       types.Log // Blockchain specific contextual infos
}

// FilterRefundRequested is a free log retrieval operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_Serving *ServingFilterer) FilterRefundRequested(opts *bind.FilterOpts, user []common.Address, provider []common.Address, index []*big.Int) (*ServingRefundRequestedIterator, error) {

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

	logs, sub, err := _Serving.contract.FilterLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return &ServingRefundRequestedIterator{contract: _Serving.contract, event: "RefundRequested", logs: logs, sub: sub}, nil
}

// WatchRefundRequested is a free log subscription operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_Serving *ServingFilterer) WatchRefundRequested(opts *bind.WatchOpts, sink chan<- *ServingRefundRequested, user []common.Address, provider []common.Address, index []*big.Int) (event.Subscription, error) {

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

	logs, sub, err := _Serving.contract.WatchLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(ServingRefundRequested)
				if err := _Serving.contract.UnpackLog(event, "RefundRequested", log); err != nil {
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
func (_Serving *ServingFilterer) ParseRefundRequested(log types.Log) (*ServingRefundRequested, error) {
	event := new(ServingRefundRequested)
	if err := _Serving.contract.UnpackLog(event, "RefundRequested", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// ServingServiceRemovedIterator is returned from FilterServiceRemoved and is used to iterate over the raw logs and unpacked data for ServiceRemoved events raised by the Serving contract.
type ServingServiceRemovedIterator struct {
	Event *ServingServiceRemoved // Event containing the contract specifics and raw log

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
func (it *ServingServiceRemovedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(ServingServiceRemoved)
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
		it.Event = new(ServingServiceRemoved)
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
func (it *ServingServiceRemovedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *ServingServiceRemovedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// ServingServiceRemoved represents a ServiceRemoved event raised by the Serving contract.
type ServingServiceRemoved struct {
	Service common.Address
	Name    common.Hash
	Raw     types.Log // Blockchain specific contextual infos
}

// FilterServiceRemoved is a free log retrieval operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed service, string indexed name)
func (_Serving *ServingFilterer) FilterServiceRemoved(opts *bind.FilterOpts, service []common.Address, name []string) (*ServingServiceRemovedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _Serving.contract.FilterLogs(opts, "ServiceRemoved", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &ServingServiceRemovedIterator{contract: _Serving.contract, event: "ServiceRemoved", logs: logs, sub: sub}, nil
}

// WatchServiceRemoved is a free log subscription operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed service, string indexed name)
func (_Serving *ServingFilterer) WatchServiceRemoved(opts *bind.WatchOpts, sink chan<- *ServingServiceRemoved, service []common.Address, name []string) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _Serving.contract.WatchLogs(opts, "ServiceRemoved", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(ServingServiceRemoved)
				if err := _Serving.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
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
func (_Serving *ServingFilterer) ParseServiceRemoved(log types.Log) (*ServingServiceRemoved, error) {
	event := new(ServingServiceRemoved)
	if err := _Serving.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// ServingServiceUpdatedIterator is returned from FilterServiceUpdated and is used to iterate over the raw logs and unpacked data for ServiceUpdated events raised by the Serving contract.
type ServingServiceUpdatedIterator struct {
	Event *ServingServiceUpdated // Event containing the contract specifics and raw log

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
func (it *ServingServiceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(ServingServiceUpdated)
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
		it.Event = new(ServingServiceUpdated)
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
func (it *ServingServiceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *ServingServiceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// ServingServiceUpdated represents a ServiceUpdated event raised by the Serving contract.
type ServingServiceUpdated struct {
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
func (_Serving *ServingFilterer) FilterServiceUpdated(opts *bind.FilterOpts, service []common.Address, name []string) (*ServingServiceUpdatedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _Serving.contract.FilterLogs(opts, "ServiceUpdated", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &ServingServiceUpdatedIterator{contract: _Serving.contract, event: "ServiceUpdated", logs: logs, sub: sub}, nil
}

// WatchServiceUpdated is a free log subscription operation binding the contract event 0x95e1ef74a36b7d6ac766d338a4468c685d593739c3b7dc39e2aa5921a1e13932.
//
// Solidity: event ServiceUpdated(address indexed service, string indexed name, string serviceType, string url, uint256 inputPrice, uint256 outputPrice, uint256 updatedAt, string model, string verifiability)
func (_Serving *ServingFilterer) WatchServiceUpdated(opts *bind.WatchOpts, sink chan<- *ServingServiceUpdated, service []common.Address, name []string) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _Serving.contract.WatchLogs(opts, "ServiceUpdated", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(ServingServiceUpdated)
				if err := _Serving.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
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
func (_Serving *ServingFilterer) ParseServiceUpdated(log types.Log) (*ServingServiceUpdated, error) {
	event := new(ServingServiceUpdated)
	if err := _Serving.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}