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
	Refunds        []Refund
	AdditionalInfo string
	ProviderSigner common.Address
	Deliverables   []Deliverable
}

// Deliverable is an auto generated low-level Go binding around an user-defined struct.
type Deliverable struct {
	ModelRootHash   []byte
	EncryptedSecret []byte
	Acknowledged    bool
}

// Quota is an auto generated low-level Go binding around an user-defined struct.
type Quota struct {
	CpuCount    *big.Int
	NodeMemory  *big.Int
	GpuCount    *big.Int
	NodeStorage *big.Int
	GpuType     string
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
	Provider       common.Address
	Name           string
	Url            string
	Quota          Quota
	PricePerToken  *big.Int
	ProviderSigner common.Address
	Occupied       bool
}

// VerifierInput is an auto generated low-level Go binding around an user-defined struct.
type VerifierInput struct {
	Index           *big.Int
	EncryptedSecret []byte
	ModelRootHash   []byte
	Nonce           *big.Int
	ProviderSigner  common.Address
	Signature       []byte
	TaskFee         *big.Int
	User            common.Address
}

// FineTuningServingMetaData contains all meta data concerning the FineTuningServing contract.
var FineTuningServingMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountNotExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"reason\",\"type\":\"string\"}],\"name\":\"InvalidVerifierInput\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceNotExist\",\"type\":\"error\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"name\":\"BalanceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"RefundRequested\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceRemoved\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"indexed\":false,\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"name\":\"ServiceUpdated\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"accountExists\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"acknowledgeDeliverable\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"}],\"name\":\"acknowledgeProviderSigner\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"name\":\"addAccount\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"}],\"name\":\"addDeliverable\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"name\":\"addOrUpdateService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"deleteAccount\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getAccount\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"components\":[{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structDeliverable[]\",\"name\":\"deliverables\",\"type\":\"tuple[]\"}],\"internalType\":\"structAccount\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllAccounts\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"components\":[{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structDeliverable[]\",\"name\":\"deliverables\",\"type\":\"tuple[]\"}],\"internalType\":\"structAccount[]\",\"name\":\"\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllServices\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"internalType\":\"structService[]\",\"name\":\"services\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"getService\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"internalType\":\"structService\",\"name\":\"service\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"_ledgerAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"}],\"name\":\"initialize\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"initialized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"ledgerAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"lockTime\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"processRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"totalAmount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"removeService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"requestRefundAll\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"taskFee\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"internalType\":\"structVerifierInput\",\"name\":\"verifierInput\",\"type\":\"tuple\"}],\"name\":\"settleFees\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"}],\"name\":\"updateLockTime\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// FineTuningServingABI is the input ABI used to generate the binding from.
// Deprecated: Use FineTuningServingMetaData.ABI instead.
var FineTuningServingABI = FineTuningServingMetaData.ABI

// FineTuningServing is an auto generated Go binding around an Ethereum contract.
type FineTuningServing struct {
	FineTuningServingCaller     // Read-only binding to the contract
	FineTuningServingTransactor // Write-only binding to the contract
	FineTuningServingFilterer   // Log filterer for contract events
}

// FineTuningServingCaller is an auto generated read-only Go binding around an Ethereum contract.
type FineTuningServingCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// FineTuningServingTransactor is an auto generated write-only Go binding around an Ethereum contract.
type FineTuningServingTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// FineTuningServingFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type FineTuningServingFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// FineTuningServingSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type FineTuningServingSession struct {
	Contract     *FineTuningServing // Generic contract binding to set the session for
	CallOpts     bind.CallOpts      // Call options to use throughout this session
	TransactOpts bind.TransactOpts  // Transaction auth options to use throughout this session
}

// FineTuningServingCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type FineTuningServingCallerSession struct {
	Contract *FineTuningServingCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts            // Call options to use throughout this session
}

// FineTuningServingTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type FineTuningServingTransactorSession struct {
	Contract     *FineTuningServingTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts            // Transaction auth options to use throughout this session
}

// FineTuningServingRaw is an auto generated low-level Go binding around an Ethereum contract.
type FineTuningServingRaw struct {
	Contract *FineTuningServing // Generic contract binding to access the raw methods on
}

// FineTuningServingCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type FineTuningServingCallerRaw struct {
	Contract *FineTuningServingCaller // Generic read-only contract binding to access the raw methods on
}

// FineTuningServingTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type FineTuningServingTransactorRaw struct {
	Contract *FineTuningServingTransactor // Generic write-only contract binding to access the raw methods on
}

// NewFineTuningServing creates a new instance of FineTuningServing, bound to a specific deployed contract.
func NewFineTuningServing(address common.Address, backend bind.ContractBackend) (*FineTuningServing, error) {
	contract, err := bindFineTuningServing(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &FineTuningServing{FineTuningServingCaller: FineTuningServingCaller{contract: contract}, FineTuningServingTransactor: FineTuningServingTransactor{contract: contract}, FineTuningServingFilterer: FineTuningServingFilterer{contract: contract}}, nil
}

// NewFineTuningServingCaller creates a new read-only instance of FineTuningServing, bound to a specific deployed contract.
func NewFineTuningServingCaller(address common.Address, caller bind.ContractCaller) (*FineTuningServingCaller, error) {
	contract, err := bindFineTuningServing(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingCaller{contract: contract}, nil
}

// NewFineTuningServingTransactor creates a new write-only instance of FineTuningServing, bound to a specific deployed contract.
func NewFineTuningServingTransactor(address common.Address, transactor bind.ContractTransactor) (*FineTuningServingTransactor, error) {
	contract, err := bindFineTuningServing(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingTransactor{contract: contract}, nil
}

// NewFineTuningServingFilterer creates a new log filterer instance of FineTuningServing, bound to a specific deployed contract.
func NewFineTuningServingFilterer(address common.Address, filterer bind.ContractFilterer) (*FineTuningServingFilterer, error) {
	contract, err := bindFineTuningServing(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingFilterer{contract: contract}, nil
}

// bindFineTuningServing binds a generic wrapper to an already deployed contract.
func bindFineTuningServing(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := FineTuningServingMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_FineTuningServing *FineTuningServingRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _FineTuningServing.Contract.FineTuningServingCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_FineTuningServing *FineTuningServingRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuningServing.Contract.FineTuningServingTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_FineTuningServing *FineTuningServingRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _FineTuningServing.Contract.FineTuningServingTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_FineTuningServing *FineTuningServingCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _FineTuningServing.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_FineTuningServing *FineTuningServingTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuningServing.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_FineTuningServing *FineTuningServingTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _FineTuningServing.Contract.contract.Transact(opts, method, params...)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_FineTuningServing *FineTuningServingCaller) AccountExists(opts *bind.CallOpts, user common.Address, provider common.Address) (bool, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "accountExists", user, provider)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_FineTuningServing *FineTuningServingSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _FineTuningServing.Contract.AccountExists(&_FineTuningServing.CallOpts, user, provider)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_FineTuningServing *FineTuningServingCallerSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _FineTuningServing.Contract.AccountExists(&_FineTuningServing.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,address,(bytes,bytes,bool)[]))
func (_FineTuningServing *FineTuningServingCaller) GetAccount(opts *bind.CallOpts, user common.Address, provider common.Address) (Account, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getAccount", user, provider)

	if err != nil {
		return *new(Account), err
	}

	out0 := *abi.ConvertType(out[0], new(Account)).(*Account)

	return out0, err

}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,address,(bytes,bytes,bool)[]))
func (_FineTuningServing *FineTuningServingSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _FineTuningServing.Contract.GetAccount(&_FineTuningServing.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,address,(bytes,bytes,bool)[]))
func (_FineTuningServing *FineTuningServingCallerSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _FineTuningServing.Contract.GetAccount(&_FineTuningServing.CallOpts, user, provider)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,address,(bytes,bytes,bool)[])[])
func (_FineTuningServing *FineTuningServingCaller) GetAllAccounts(opts *bind.CallOpts) ([]Account, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getAllAccounts")

	if err != nil {
		return *new([]Account), err
	}

	out0 := *abi.ConvertType(out[0], new([]Account)).(*[]Account)

	return out0, err

}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,address,(bytes,bytes,bool)[])[])
func (_FineTuningServing *FineTuningServingSession) GetAllAccounts() ([]Account, error) {
	return _FineTuningServing.Contract.GetAllAccounts(&_FineTuningServing.CallOpts)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,address,(bytes,bytes,bool)[])[])
func (_FineTuningServing *FineTuningServingCallerSession) GetAllAccounts() ([]Account, error) {
	return _FineTuningServing.Contract.GetAllAccounts(&_FineTuningServing.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,address,bool)[] services)
func (_FineTuningServing *FineTuningServingCaller) GetAllServices(opts *bind.CallOpts) ([]Service, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getAllServices")

	if err != nil {
		return *new([]Service), err
	}

	out0 := *abi.ConvertType(out[0], new([]Service)).(*[]Service)

	return out0, err

}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,address,bool)[] services)
func (_FineTuningServing *FineTuningServingSession) GetAllServices() ([]Service, error) {
	return _FineTuningServing.Contract.GetAllServices(&_FineTuningServing.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,address,bool)[] services)
func (_FineTuningServing *FineTuningServingCallerSession) GetAllServices() ([]Service, error) {
	return _FineTuningServing.Contract.GetAllServices(&_FineTuningServing.CallOpts)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,address,bool) service)
func (_FineTuningServing *FineTuningServingCaller) GetService(opts *bind.CallOpts, provider common.Address, name string) (Service, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getService", provider, name)

	if err != nil {
		return *new(Service), err
	}

	out0 := *abi.ConvertType(out[0], new(Service)).(*Service)

	return out0, err

}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,address,bool) service)
func (_FineTuningServing *FineTuningServingSession) GetService(provider common.Address, name string) (Service, error) {
	return _FineTuningServing.Contract.GetService(&_FineTuningServing.CallOpts, provider, name)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,address,bool) service)
func (_FineTuningServing *FineTuningServingCallerSession) GetService(provider common.Address, name string) (Service, error) {
	return _FineTuningServing.Contract.GetService(&_FineTuningServing.CallOpts, provider, name)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_FineTuningServing *FineTuningServingCaller) Initialized(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "initialized")

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_FineTuningServing *FineTuningServingSession) Initialized() (bool, error) {
	return _FineTuningServing.Contract.Initialized(&_FineTuningServing.CallOpts)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_FineTuningServing *FineTuningServingCallerSession) Initialized() (bool, error) {
	return _FineTuningServing.Contract.Initialized(&_FineTuningServing.CallOpts)
}

// LedgerAddress is a free data retrieval call binding the contract method 0xd1d20056.
//
// Solidity: function ledgerAddress() view returns(address)
func (_FineTuningServing *FineTuningServingCaller) LedgerAddress(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "ledgerAddress")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// LedgerAddress is a free data retrieval call binding the contract method 0xd1d20056.
//
// Solidity: function ledgerAddress() view returns(address)
func (_FineTuningServing *FineTuningServingSession) LedgerAddress() (common.Address, error) {
	return _FineTuningServing.Contract.LedgerAddress(&_FineTuningServing.CallOpts)
}

// LedgerAddress is a free data retrieval call binding the contract method 0xd1d20056.
//
// Solidity: function ledgerAddress() view returns(address)
func (_FineTuningServing *FineTuningServingCallerSession) LedgerAddress() (common.Address, error) {
	return _FineTuningServing.Contract.LedgerAddress(&_FineTuningServing.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_FineTuningServing *FineTuningServingCaller) LockTime(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "lockTime")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_FineTuningServing *FineTuningServingSession) LockTime() (*big.Int, error) {
	return _FineTuningServing.Contract.LockTime(&_FineTuningServing.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_FineTuningServing *FineTuningServingCallerSession) LockTime() (*big.Int, error) {
	return _FineTuningServing.Contract.LockTime(&_FineTuningServing.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_FineTuningServing *FineTuningServingCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "owner")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_FineTuningServing *FineTuningServingSession) Owner() (common.Address, error) {
	return _FineTuningServing.Contract.Owner(&_FineTuningServing.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_FineTuningServing *FineTuningServingCallerSession) Owner() (common.Address, error) {
	return _FineTuningServing.Contract.Owner(&_FineTuningServing.CallOpts)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0x5f7069db.
//
// Solidity: function acknowledgeDeliverable(address provider, uint256 index) returns()
func (_FineTuningServing *FineTuningServingTransactor) AcknowledgeDeliverable(opts *bind.TransactOpts, provider common.Address, index *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "acknowledgeDeliverable", provider, index)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0x5f7069db.
//
// Solidity: function acknowledgeDeliverable(address provider, uint256 index) returns()
func (_FineTuningServing *FineTuningServingSession) AcknowledgeDeliverable(provider common.Address, index *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeDeliverable(&_FineTuningServing.TransactOpts, provider, index)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0x5f7069db.
//
// Solidity: function acknowledgeDeliverable(address provider, uint256 index) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AcknowledgeDeliverable(provider common.Address, index *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeDeliverable(&_FineTuningServing.TransactOpts, provider, index)
}

// AcknowledgeProviderSigner is a paid mutator transaction binding the contract method 0xf2c6741a.
//
// Solidity: function acknowledgeProviderSigner(address provider, address providerSigner) returns()
func (_FineTuningServing *FineTuningServingTransactor) AcknowledgeProviderSigner(opts *bind.TransactOpts, provider common.Address, providerSigner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "acknowledgeProviderSigner", provider, providerSigner)
}

// AcknowledgeProviderSigner is a paid mutator transaction binding the contract method 0xf2c6741a.
//
// Solidity: function acknowledgeProviderSigner(address provider, address providerSigner) returns()
func (_FineTuningServing *FineTuningServingSession) AcknowledgeProviderSigner(provider common.Address, providerSigner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeProviderSigner(&_FineTuningServing.TransactOpts, provider, providerSigner)
}

// AcknowledgeProviderSigner is a paid mutator transaction binding the contract method 0xf2c6741a.
//
// Solidity: function acknowledgeProviderSigner(address provider, address providerSigner) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AcknowledgeProviderSigner(provider common.Address, providerSigner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeProviderSigner(&_FineTuningServing.TransactOpts, provider, providerSigner)
}

// AddAccount is a paid mutator transaction binding the contract method 0xe50688f9.
//
// Solidity: function addAccount(address user, address provider, string additionalInfo) payable returns()
func (_FineTuningServing *FineTuningServingTransactor) AddAccount(opts *bind.TransactOpts, user common.Address, provider common.Address, additionalInfo string) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "addAccount", user, provider, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0xe50688f9.
//
// Solidity: function addAccount(address user, address provider, string additionalInfo) payable returns()
func (_FineTuningServing *FineTuningServingSession) AddAccount(user common.Address, provider common.Address, additionalInfo string) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddAccount(&_FineTuningServing.TransactOpts, user, provider, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0xe50688f9.
//
// Solidity: function addAccount(address user, address provider, string additionalInfo) payable returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AddAccount(user common.Address, provider common.Address, additionalInfo string) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddAccount(&_FineTuningServing.TransactOpts, user, provider, additionalInfo)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0x98248997.
//
// Solidity: function addDeliverable(address user, bytes modelRootHash) returns()
func (_FineTuningServing *FineTuningServingTransactor) AddDeliverable(opts *bind.TransactOpts, user common.Address, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "addDeliverable", user, modelRootHash)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0x98248997.
//
// Solidity: function addDeliverable(address user, bytes modelRootHash) returns()
func (_FineTuningServing *FineTuningServingSession) AddDeliverable(user common.Address, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddDeliverable(&_FineTuningServing.TransactOpts, user, modelRootHash)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0x98248997.
//
// Solidity: function addDeliverable(address user, bytes modelRootHash) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AddDeliverable(user common.Address, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddDeliverable(&_FineTuningServing.TransactOpts, user, modelRootHash)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0xd02773ea.
//
// Solidity: function addOrUpdateService(string name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address providerSigner, bool occupied) returns()
func (_FineTuningServing *FineTuningServingTransactor) AddOrUpdateService(opts *bind.TransactOpts, name string, url string, quota Quota, pricePerToken *big.Int, providerSigner common.Address, occupied bool) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "addOrUpdateService", name, url, quota, pricePerToken, providerSigner, occupied)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0xd02773ea.
//
// Solidity: function addOrUpdateService(string name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address providerSigner, bool occupied) returns()
func (_FineTuningServing *FineTuningServingSession) AddOrUpdateService(name string, url string, quota Quota, pricePerToken *big.Int, providerSigner common.Address, occupied bool) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddOrUpdateService(&_FineTuningServing.TransactOpts, name, url, quota, pricePerToken, providerSigner, occupied)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0xd02773ea.
//
// Solidity: function addOrUpdateService(string name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address providerSigner, bool occupied) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AddOrUpdateService(name string, url string, quota Quota, pricePerToken *big.Int, providerSigner common.Address, occupied bool) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddOrUpdateService(&_FineTuningServing.TransactOpts, name, url, quota, pricePerToken, providerSigner, occupied)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_FineTuningServing *FineTuningServingTransactor) DeleteAccount(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "deleteAccount", user, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_FineTuningServing *FineTuningServingSession) DeleteAccount(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.DeleteAccount(&_FineTuningServing.TransactOpts, user, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x97216725.
//
// Solidity: function deleteAccount(address user, address provider) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) DeleteAccount(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.DeleteAccount(&_FineTuningServing.TransactOpts, user, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0x3f54d973.
//
// Solidity: function depositFund(address user, address provider) payable returns()
func (_FineTuningServing *FineTuningServingTransactor) DepositFund(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "depositFund", user, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0x3f54d973.
//
// Solidity: function depositFund(address user, address provider) payable returns()
func (_FineTuningServing *FineTuningServingSession) DepositFund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.DepositFund(&_FineTuningServing.TransactOpts, user, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0x3f54d973.
//
// Solidity: function depositFund(address user, address provider) payable returns()
func (_FineTuningServing *FineTuningServingTransactorSession) DepositFund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.DepositFund(&_FineTuningServing.TransactOpts, user, provider)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner) returns()
func (_FineTuningServing *FineTuningServingTransactor) Initialize(opts *bind.TransactOpts, _locktime *big.Int, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "initialize", _locktime, _ledgerAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner) returns()
func (_FineTuningServing *FineTuningServingSession) Initialize(_locktime *big.Int, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.Initialize(&_FineTuningServing.TransactOpts, _locktime, _ledgerAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) Initialize(_locktime *big.Int, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.Initialize(&_FineTuningServing.TransactOpts, _locktime, _ledgerAddress, owner)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_FineTuningServing *FineTuningServingTransactor) ProcessRefund(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "processRefund", user, provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_FineTuningServing *FineTuningServingSession) ProcessRefund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.ProcessRefund(&_FineTuningServing.TransactOpts, user, provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0x4e3c4f22.
//
// Solidity: function processRefund(address user, address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_FineTuningServing *FineTuningServingTransactorSession) ProcessRefund(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.ProcessRefund(&_FineTuningServing.TransactOpts, user, provider)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_FineTuningServing *FineTuningServingTransactor) RemoveService(opts *bind.TransactOpts, name string) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "removeService", name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_FineTuningServing *FineTuningServingSession) RemoveService(name string) (*types.Transaction, error) {
	return _FineTuningServing.Contract.RemoveService(&_FineTuningServing.TransactOpts, name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) RemoveService(name string) (*types.Transaction, error) {
	return _FineTuningServing.Contract.RemoveService(&_FineTuningServing.TransactOpts, name)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_FineTuningServing *FineTuningServingTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_FineTuningServing *FineTuningServingSession) RenounceOwnership() (*types.Transaction, error) {
	return _FineTuningServing.Contract.RenounceOwnership(&_FineTuningServing.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_FineTuningServing *FineTuningServingTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _FineTuningServing.Contract.RenounceOwnership(&_FineTuningServing.TransactOpts)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_FineTuningServing *FineTuningServingTransactor) RequestRefundAll(opts *bind.TransactOpts, user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "requestRefundAll", user, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_FineTuningServing *FineTuningServingSession) RequestRefundAll(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.RequestRefundAll(&_FineTuningServing.TransactOpts, user, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0x6c79158d.
//
// Solidity: function requestRefundAll(address user, address provider) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) RequestRefundAll(user common.Address, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.RequestRefundAll(&_FineTuningServing.TransactOpts, user, provider)
}

// SettleFees is a paid mutator transaction binding the contract method 0x97e19403.
//
// Solidity: function settleFees((uint256,bytes,bytes,uint256,address,bytes,uint256,address) verifierInput) returns()
func (_FineTuningServing *FineTuningServingTransactor) SettleFees(opts *bind.TransactOpts, verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "settleFees", verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x97e19403.
//
// Solidity: function settleFees((uint256,bytes,bytes,uint256,address,bytes,uint256,address) verifierInput) returns()
func (_FineTuningServing *FineTuningServingSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuningServing.Contract.SettleFees(&_FineTuningServing.TransactOpts, verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x97e19403.
//
// Solidity: function settleFees((uint256,bytes,bytes,uint256,address,bytes,uint256,address) verifierInput) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuningServing.Contract.SettleFees(&_FineTuningServing.TransactOpts, verifierInput)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_FineTuningServing *FineTuningServingTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_FineTuningServing *FineTuningServingSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.TransferOwnership(&_FineTuningServing.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.TransferOwnership(&_FineTuningServing.TransactOpts, newOwner)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_FineTuningServing *FineTuningServingTransactor) UpdateLockTime(opts *bind.TransactOpts, _locktime *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "updateLockTime", _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_FineTuningServing *FineTuningServingSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.UpdateLockTime(&_FineTuningServing.TransactOpts, _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.UpdateLockTime(&_FineTuningServing.TransactOpts, _locktime)
}

// FineTuningServingBalanceUpdatedIterator is returned from FilterBalanceUpdated and is used to iterate over the raw logs and unpacked data for BalanceUpdated events raised by the FineTuningServing contract.
type FineTuningServingBalanceUpdatedIterator struct {
	Event *FineTuningServingBalanceUpdated // Event containing the contract specifics and raw log

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
func (it *FineTuningServingBalanceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingBalanceUpdated)
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
		it.Event = new(FineTuningServingBalanceUpdated)
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
func (it *FineTuningServingBalanceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingBalanceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingBalanceUpdated represents a BalanceUpdated event raised by the FineTuningServing contract.
type FineTuningServingBalanceUpdated struct {
	User          common.Address
	Provider      common.Address
	Amount        *big.Int
	PendingRefund *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterBalanceUpdated is a free log retrieval operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_FineTuningServing *FineTuningServingFilterer) FilterBalanceUpdated(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*FineTuningServingBalanceUpdatedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingBalanceUpdatedIterator{contract: _FineTuningServing.contract, event: "BalanceUpdated", logs: logs, sub: sub}, nil
}

// WatchBalanceUpdated is a free log subscription operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_FineTuningServing *FineTuningServingFilterer) WatchBalanceUpdated(opts *bind.WatchOpts, sink chan<- *FineTuningServingBalanceUpdated, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingBalanceUpdated)
				if err := _FineTuningServing.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseBalanceUpdated(log types.Log) (*FineTuningServingBalanceUpdated, error) {
	event := new(FineTuningServingBalanceUpdated)
	if err := _FineTuningServing.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the FineTuningServing contract.
type FineTuningServingOwnershipTransferredIterator struct {
	Event *FineTuningServingOwnershipTransferred // Event containing the contract specifics and raw log

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
func (it *FineTuningServingOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingOwnershipTransferred)
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
		it.Event = new(FineTuningServingOwnershipTransferred)
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
func (it *FineTuningServingOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingOwnershipTransferred represents a OwnershipTransferred event raised by the FineTuningServing contract.
type FineTuningServingOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_FineTuningServing *FineTuningServingFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*FineTuningServingOwnershipTransferredIterator, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingOwnershipTransferredIterator{contract: _FineTuningServing.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_FineTuningServing *FineTuningServingFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *FineTuningServingOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingOwnershipTransferred)
				if err := _FineTuningServing.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseOwnershipTransferred(log types.Log) (*FineTuningServingOwnershipTransferred, error) {
	event := new(FineTuningServingOwnershipTransferred)
	if err := _FineTuningServing.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingRefundRequestedIterator is returned from FilterRefundRequested and is used to iterate over the raw logs and unpacked data for RefundRequested events raised by the FineTuningServing contract.
type FineTuningServingRefundRequestedIterator struct {
	Event *FineTuningServingRefundRequested // Event containing the contract specifics and raw log

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
func (it *FineTuningServingRefundRequestedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingRefundRequested)
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
		it.Event = new(FineTuningServingRefundRequested)
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
func (it *FineTuningServingRefundRequestedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingRefundRequestedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingRefundRequested represents a RefundRequested event raised by the FineTuningServing contract.
type FineTuningServingRefundRequested struct {
	User      common.Address
	Provider  common.Address
	Index     *big.Int
	Timestamp *big.Int
	Raw       types.Log // Blockchain specific contextual infos
}

// FilterRefundRequested is a free log retrieval operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) FilterRefundRequested(opts *bind.FilterOpts, user []common.Address, provider []common.Address, index []*big.Int) (*FineTuningServingRefundRequestedIterator, error) {

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

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingRefundRequestedIterator{contract: _FineTuningServing.contract, event: "RefundRequested", logs: logs, sub: sub}, nil
}

// WatchRefundRequested is a free log subscription operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) WatchRefundRequested(opts *bind.WatchOpts, sink chan<- *FineTuningServingRefundRequested, user []common.Address, provider []common.Address, index []*big.Int) (event.Subscription, error) {

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

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingRefundRequested)
				if err := _FineTuningServing.contract.UnpackLog(event, "RefundRequested", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseRefundRequested(log types.Log) (*FineTuningServingRefundRequested, error) {
	event := new(FineTuningServingRefundRequested)
	if err := _FineTuningServing.contract.UnpackLog(event, "RefundRequested", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingServiceRemovedIterator is returned from FilterServiceRemoved and is used to iterate over the raw logs and unpacked data for ServiceRemoved events raised by the FineTuningServing contract.
type FineTuningServingServiceRemovedIterator struct {
	Event *FineTuningServingServiceRemoved // Event containing the contract specifics and raw log

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
func (it *FineTuningServingServiceRemovedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingServiceRemoved)
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
		it.Event = new(FineTuningServingServiceRemoved)
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
func (it *FineTuningServingServiceRemovedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingServiceRemovedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingServiceRemoved represents a ServiceRemoved event raised by the FineTuningServing contract.
type FineTuningServingServiceRemoved struct {
	User common.Address
	Name common.Hash
	Raw  types.Log // Blockchain specific contextual infos
}

// FilterServiceRemoved is a free log retrieval operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed user, string indexed name)
func (_FineTuningServing *FineTuningServingFilterer) FilterServiceRemoved(opts *bind.FilterOpts, user []common.Address, name []string) (*FineTuningServingServiceRemovedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "ServiceRemoved", userRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingServiceRemovedIterator{contract: _FineTuningServing.contract, event: "ServiceRemoved", logs: logs, sub: sub}, nil
}

// WatchServiceRemoved is a free log subscription operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed user, string indexed name)
func (_FineTuningServing *FineTuningServingFilterer) WatchServiceRemoved(opts *bind.WatchOpts, sink chan<- *FineTuningServingServiceRemoved, user []common.Address, name []string) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "ServiceRemoved", userRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingServiceRemoved)
				if err := _FineTuningServing.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
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
// Solidity: event ServiceRemoved(address indexed user, string indexed name)
func (_FineTuningServing *FineTuningServingFilterer) ParseServiceRemoved(log types.Log) (*FineTuningServingServiceRemoved, error) {
	event := new(FineTuningServingServiceRemoved)
	if err := _FineTuningServing.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingServiceUpdatedIterator is returned from FilterServiceUpdated and is used to iterate over the raw logs and unpacked data for ServiceUpdated events raised by the FineTuningServing contract.
type FineTuningServingServiceUpdatedIterator struct {
	Event *FineTuningServingServiceUpdated // Event containing the contract specifics and raw log

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
func (it *FineTuningServingServiceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingServiceUpdated)
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
		it.Event = new(FineTuningServingServiceUpdated)
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
func (it *FineTuningServingServiceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingServiceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingServiceUpdated represents a ServiceUpdated event raised by the FineTuningServing contract.
type FineTuningServingServiceUpdated struct {
	User           common.Address
	Name           common.Hash
	Url            string
	Quota          Quota
	PricePerToken  *big.Int
	ProviderSigner common.Address
	Occupied       bool
	Raw            types.Log // Blockchain specific contextual infos
}

// FilterServiceUpdated is a free log retrieval operation binding the contract event 0x58eddeb83ccb48626f6914d464160b1085488d5cf6130ba3323980123ad14016.
//
// Solidity: event ServiceUpdated(address indexed user, string indexed name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address providerSigner, bool occupied)
func (_FineTuningServing *FineTuningServingFilterer) FilterServiceUpdated(opts *bind.FilterOpts, user []common.Address, name []string) (*FineTuningServingServiceUpdatedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "ServiceUpdated", userRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingServiceUpdatedIterator{contract: _FineTuningServing.contract, event: "ServiceUpdated", logs: logs, sub: sub}, nil
}

// WatchServiceUpdated is a free log subscription operation binding the contract event 0x58eddeb83ccb48626f6914d464160b1085488d5cf6130ba3323980123ad14016.
//
// Solidity: event ServiceUpdated(address indexed user, string indexed name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address providerSigner, bool occupied)
func (_FineTuningServing *FineTuningServingFilterer) WatchServiceUpdated(opts *bind.WatchOpts, sink chan<- *FineTuningServingServiceUpdated, user []common.Address, name []string) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "ServiceUpdated", userRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingServiceUpdated)
				if err := _FineTuningServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
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

// ParseServiceUpdated is a log parse operation binding the contract event 0x58eddeb83ccb48626f6914d464160b1085488d5cf6130ba3323980123ad14016.
//
// Solidity: event ServiceUpdated(address indexed user, string indexed name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address providerSigner, bool occupied)
func (_FineTuningServing *FineTuningServingFilterer) ParseServiceUpdated(log types.Log) (*FineTuningServingServiceUpdated, error) {
	event := new(FineTuningServingServiceUpdated)
	if err := _FineTuningServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
