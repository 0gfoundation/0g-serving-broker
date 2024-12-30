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
	User                       common.Address
	Provider                   common.Address
	Nonce                      *big.Int
	Balance                    *big.Int
	PendingRefund              *big.Int
	UserSigner                 [2]*big.Int
	Refunds                    []Refund
	AdditionalInfo             string
	ProviderSigner             common.Address
	ProviderSignerAcknowledged bool
	Deliverables               []Deliverable
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
	Provider      common.Address
	Name          string
	Url           string
	Quota         Quota
	PricePerToken *big.Int
	Occupied      bool
}

// VerifierInput is an auto generated low-level Go binding around an user-defined struct.
type VerifierInput struct {
	Index           *big.Int
	Signature       []byte
	ModelRootHash   []byte
	EncryptedSecret []byte
	TaskFee         *big.Int
	Nonce           *big.Int
	User            common.Address
}

// FineTuneServingMetaData contains all meta data concerning the FineTuneServing contract.
var FineTuneServingMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountNotExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"InsufficientBalance\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"reason\",\"type\":\"string\"}],\"name\":\"InvalidProofInputs\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceNotExist\",\"type\":\"error\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"name\":\"BalanceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"RefundRequested\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"ServiceRemoved\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"indexed\":false,\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"indexed\":false,\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"name\":\"ServiceUpdated\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"accountExists\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"}],\"name\":\"acknowledgeDeliverable\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"acknowledgeProviderSigner\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256[2]\",\"name\":\"signer\",\"type\":\"uint256[2]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"name\":\"addAccount\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"}],\"name\":\"addDeliverable\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"name\":\"addOrUpdateService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"}],\"name\":\"addProviderSigner\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"deleteAccount\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getAccount\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"userSigner\",\"type\":\"uint256[2]\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"providerSignerAcknowledged\",\"type\":\"bool\"},{\"components\":[{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structDeliverable[]\",\"name\":\"deliverables\",\"type\":\"tuple[]\"}],\"internalType\":\"structAccount\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllAccounts\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"uint256[2]\",\"name\":\"userSigner\",\"type\":\"uint256[2]\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address\",\"name\":\"providerSigner\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"providerSignerAcknowledged\",\"type\":\"bool\"},{\"components\":[{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structDeliverable[]\",\"name\":\"deliverables\",\"type\":\"tuple[]\"}],\"internalType\":\"structAccount[]\",\"name\":\"\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllServices\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"internalType\":\"structService[]\",\"name\":\"services\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"getService\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"internalType\":\"structService\",\"name\":\"service\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"}],\"name\":\"initialize\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"initialized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"lockTime\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"processRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"totalAmount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"name\",\"type\":\"string\"}],\"name\":\"removeService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"requestRefund\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"requestRefundAll\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"taskFee\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"internalType\":\"structVerifierInput\",\"name\":\"verifierInput\",\"type\":\"tuple\"}],\"name\":\"settleFees\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"}],\"name\":\"updateLockTime\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// FineTuneServingABI is the input ABI used to generate the binding from.
// Deprecated: Use FineTuneServingMetaData.ABI instead.
var FineTuneServingABI = FineTuneServingMetaData.ABI

// FineTuneServing is an auto generated Go binding around an Ethereum contract.
type FineTuneServing struct {
	FineTuneServingCaller     // Read-only binding to the contract
	FineTuneServingTransactor // Write-only binding to the contract
	FineTuneServingFilterer   // Log filterer for contract events
}

// FineTuneServingCaller is an auto generated read-only Go binding around an Ethereum contract.
type FineTuneServingCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// FineTuneServingTransactor is an auto generated write-only Go binding around an Ethereum contract.
type FineTuneServingTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// FineTuneServingFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type FineTuneServingFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// FineTuneServingSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type FineTuneServingSession struct {
	Contract     *FineTuneServing  // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// FineTuneServingCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type FineTuneServingCallerSession struct {
	Contract *FineTuneServingCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts          // Call options to use throughout this session
}

// FineTuneServingTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type FineTuneServingTransactorSession struct {
	Contract     *FineTuneServingTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts          // Transaction auth options to use throughout this session
}

// FineTuneServingRaw is an auto generated low-level Go binding around an Ethereum contract.
type FineTuneServingRaw struct {
	Contract *FineTuneServing // Generic contract binding to access the raw methods on
}

// FineTuneServingCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type FineTuneServingCallerRaw struct {
	Contract *FineTuneServingCaller // Generic read-only contract binding to access the raw methods on
}

// FineTuneServingTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type FineTuneServingTransactorRaw struct {
	Contract *FineTuneServingTransactor // Generic write-only contract binding to access the raw methods on
}

// NewFineTuneServing creates a new instance of FineTuneServing, bound to a specific deployed contract.
func NewFineTuneServing(address common.Address, backend bind.ContractBackend) (*FineTuneServing, error) {
	contract, err := bindFineTuneServing(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &FineTuneServing{FineTuneServingCaller: FineTuneServingCaller{contract: contract}, FineTuneServingTransactor: FineTuneServingTransactor{contract: contract}, FineTuneServingFilterer: FineTuneServingFilterer{contract: contract}}, nil
}

// NewFineTuneServingCaller creates a new read-only instance of FineTuneServing, bound to a specific deployed contract.
func NewFineTuneServingCaller(address common.Address, caller bind.ContractCaller) (*FineTuneServingCaller, error) {
	contract, err := bindFineTuneServing(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingCaller{contract: contract}, nil
}

// NewFineTuneServingTransactor creates a new write-only instance of FineTuneServing, bound to a specific deployed contract.
func NewFineTuneServingTransactor(address common.Address, transactor bind.ContractTransactor) (*FineTuneServingTransactor, error) {
	contract, err := bindFineTuneServing(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingTransactor{contract: contract}, nil
}

// NewFineTuneServingFilterer creates a new log filterer instance of FineTuneServing, bound to a specific deployed contract.
func NewFineTuneServingFilterer(address common.Address, filterer bind.ContractFilterer) (*FineTuneServingFilterer, error) {
	contract, err := bindFineTuneServing(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingFilterer{contract: contract}, nil
}

// bindFineTuneServing binds a generic wrapper to an already deployed contract.
func bindFineTuneServing(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := FineTuneServingMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_FineTuneServing *FineTuneServingRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _FineTuneServing.Contract.FineTuneServingCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_FineTuneServing *FineTuneServingRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuneServing.Contract.FineTuneServingTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_FineTuneServing *FineTuneServingRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _FineTuneServing.Contract.FineTuneServingTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_FineTuneServing *FineTuneServingCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _FineTuneServing.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_FineTuneServing *FineTuneServingTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuneServing.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_FineTuneServing *FineTuneServingTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _FineTuneServing.Contract.contract.Transact(opts, method, params...)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_FineTuneServing *FineTuneServingCaller) AccountExists(opts *bind.CallOpts, user common.Address, provider common.Address) (bool, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "accountExists", user, provider)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_FineTuneServing *FineTuneServingSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _FineTuneServing.Contract.AccountExists(&_FineTuneServing.CallOpts, user, provider)
}

// AccountExists is a free data retrieval call binding the contract method 0x147500e3.
//
// Solidity: function accountExists(address user, address provider) view returns(bool)
func (_FineTuneServing *FineTuneServingCallerSession) AccountExists(user common.Address, provider common.Address) (bool, error) {
	return _FineTuneServing.Contract.AccountExists(&_FineTuneServing.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string,address,bool,(bytes,bytes,bool)[]))
func (_FineTuneServing *FineTuneServingCaller) GetAccount(opts *bind.CallOpts, user common.Address, provider common.Address) (Account, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "getAccount", user, provider)

	if err != nil {
		return *new(Account), err
	}

	out0 := *abi.ConvertType(out[0], new(Account)).(*Account)

	return out0, err

}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string,address,bool,(bytes,bytes,bool)[]))
func (_FineTuneServing *FineTuneServingSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _FineTuneServing.Contract.GetAccount(&_FineTuneServing.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string,address,bool,(bytes,bytes,bool)[]))
func (_FineTuneServing *FineTuneServingCallerSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _FineTuneServing.Contract.GetAccount(&_FineTuneServing.CallOpts, user, provider)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string,address,bool,(bytes,bytes,bool)[])[])
func (_FineTuneServing *FineTuneServingCaller) GetAllAccounts(opts *bind.CallOpts) ([]Account, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "getAllAccounts")

	if err != nil {
		return *new([]Account), err
	}

	out0 := *abi.ConvertType(out[0], new([]Account)).(*[]Account)

	return out0, err

}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string,address,bool,(bytes,bytes,bool)[])[])
func (_FineTuneServing *FineTuneServingSession) GetAllAccounts() ([]Account, error) {
	return _FineTuneServing.Contract.GetAllAccounts(&_FineTuneServing.CallOpts)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x08e93d0a.
//
// Solidity: function getAllAccounts() view returns((address,address,uint256,uint256,uint256,uint256[2],(uint256,uint256,uint256,bool)[],string,address,bool,(bytes,bytes,bool)[])[])
func (_FineTuneServing *FineTuneServingCallerSession) GetAllAccounts() ([]Account, error) {
	return _FineTuneServing.Contract.GetAllAccounts(&_FineTuneServing.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,bool)[] services)
func (_FineTuneServing *FineTuneServingCaller) GetAllServices(opts *bind.CallOpts) ([]Service, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "getAllServices")

	if err != nil {
		return *new([]Service), err
	}

	out0 := *abi.ConvertType(out[0], new([]Service)).(*[]Service)

	return out0, err

}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,bool)[] services)
func (_FineTuneServing *FineTuneServingSession) GetAllServices() ([]Service, error) {
	return _FineTuneServing.Contract.GetAllServices(&_FineTuneServing.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,bool)[] services)
func (_FineTuneServing *FineTuneServingCallerSession) GetAllServices() ([]Service, error) {
	return _FineTuneServing.Contract.GetAllServices(&_FineTuneServing.CallOpts)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,bool) service)
func (_FineTuneServing *FineTuneServingCaller) GetService(opts *bind.CallOpts, provider common.Address, name string) (Service, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "getService", provider, name)

	if err != nil {
		return *new(Service), err
	}

	out0 := *abi.ConvertType(out[0], new(Service)).(*Service)

	return out0, err

}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,bool) service)
func (_FineTuneServing *FineTuneServingSession) GetService(provider common.Address, name string) (Service, error) {
	return _FineTuneServing.Contract.GetService(&_FineTuneServing.CallOpts, provider, name)
}

// GetService is a free data retrieval call binding the contract method 0x0e61d158.
//
// Solidity: function getService(address provider, string name) view returns((address,string,string,(uint256,uint256,uint256,uint256,string),uint256,bool) service)
func (_FineTuneServing *FineTuneServingCallerSession) GetService(provider common.Address, name string) (Service, error) {
	return _FineTuneServing.Contract.GetService(&_FineTuneServing.CallOpts, provider, name)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_FineTuneServing *FineTuneServingCaller) Initialized(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "initialized")

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_FineTuneServing *FineTuneServingSession) Initialized() (bool, error) {
	return _FineTuneServing.Contract.Initialized(&_FineTuneServing.CallOpts)
}

// Initialized is a free data retrieval call binding the contract method 0x158ef93e.
//
// Solidity: function initialized() view returns(bool)
func (_FineTuneServing *FineTuneServingCallerSession) Initialized() (bool, error) {
	return _FineTuneServing.Contract.Initialized(&_FineTuneServing.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_FineTuneServing *FineTuneServingCaller) LockTime(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "lockTime")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_FineTuneServing *FineTuneServingSession) LockTime() (*big.Int, error) {
	return _FineTuneServing.Contract.LockTime(&_FineTuneServing.CallOpts)
}

// LockTime is a free data retrieval call binding the contract method 0x0d668087.
//
// Solidity: function lockTime() view returns(uint256)
func (_FineTuneServing *FineTuneServingCallerSession) LockTime() (*big.Int, error) {
	return _FineTuneServing.Contract.LockTime(&_FineTuneServing.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_FineTuneServing *FineTuneServingCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _FineTuneServing.contract.Call(opts, &out, "owner")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_FineTuneServing *FineTuneServingSession) Owner() (common.Address, error) {
	return _FineTuneServing.Contract.Owner(&_FineTuneServing.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_FineTuneServing *FineTuneServingCallerSession) Owner() (common.Address, error) {
	return _FineTuneServing.Contract.Owner(&_FineTuneServing.CallOpts)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0x5f7069db.
//
// Solidity: function acknowledgeDeliverable(address provider, uint256 index) returns()
func (_FineTuneServing *FineTuneServingTransactor) AcknowledgeDeliverable(opts *bind.TransactOpts, provider common.Address, index *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "acknowledgeDeliverable", provider, index)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0x5f7069db.
//
// Solidity: function acknowledgeDeliverable(address provider, uint256 index) returns()
func (_FineTuneServing *FineTuneServingSession) AcknowledgeDeliverable(provider common.Address, index *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AcknowledgeDeliverable(&_FineTuneServing.TransactOpts, provider, index)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0x5f7069db.
//
// Solidity: function acknowledgeDeliverable(address provider, uint256 index) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) AcknowledgeDeliverable(provider common.Address, index *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AcknowledgeDeliverable(&_FineTuneServing.TransactOpts, provider, index)
}

// AcknowledgeProviderSigner is a paid mutator transaction binding the contract method 0xdaa2dffb.
//
// Solidity: function acknowledgeProviderSigner(address provider) returns()
func (_FineTuneServing *FineTuneServingTransactor) AcknowledgeProviderSigner(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "acknowledgeProviderSigner", provider)
}

// AcknowledgeProviderSigner is a paid mutator transaction binding the contract method 0xdaa2dffb.
//
// Solidity: function acknowledgeProviderSigner(address provider) returns()
func (_FineTuneServing *FineTuneServingSession) AcknowledgeProviderSigner(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AcknowledgeProviderSigner(&_FineTuneServing.TransactOpts, provider)
}

// AcknowledgeProviderSigner is a paid mutator transaction binding the contract method 0xdaa2dffb.
//
// Solidity: function acknowledgeProviderSigner(address provider) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) AcknowledgeProviderSigner(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AcknowledgeProviderSigner(&_FineTuneServing.TransactOpts, provider)
}

// AddAccount is a paid mutator transaction binding the contract method 0x0b1d1392.
//
// Solidity: function addAccount(address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_FineTuneServing *FineTuneServingTransactor) AddAccount(opts *bind.TransactOpts, provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "addAccount", provider, signer, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0x0b1d1392.
//
// Solidity: function addAccount(address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_FineTuneServing *FineTuneServingSession) AddAccount(provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddAccount(&_FineTuneServing.TransactOpts, provider, signer, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0x0b1d1392.
//
// Solidity: function addAccount(address provider, uint256[2] signer, string additionalInfo) payable returns()
func (_FineTuneServing *FineTuneServingTransactorSession) AddAccount(provider common.Address, signer [2]*big.Int, additionalInfo string) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddAccount(&_FineTuneServing.TransactOpts, provider, signer, additionalInfo)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0xb4b91c38.
//
// Solidity: function addDeliverable(address user, uint256 index, bytes modelRootHash) returns()
func (_FineTuneServing *FineTuneServingTransactor) AddDeliverable(opts *bind.TransactOpts, user common.Address, index *big.Int, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "addDeliverable", user, index, modelRootHash)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0xb4b91c38.
//
// Solidity: function addDeliverable(address user, uint256 index, bytes modelRootHash) returns()
func (_FineTuneServing *FineTuneServingSession) AddDeliverable(user common.Address, index *big.Int, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddDeliverable(&_FineTuneServing.TransactOpts, user, index, modelRootHash)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0xb4b91c38.
//
// Solidity: function addDeliverable(address user, uint256 index, bytes modelRootHash) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) AddDeliverable(user common.Address, index *big.Int, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddDeliverable(&_FineTuneServing.TransactOpts, user, index, modelRootHash)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x1ded73bb.
//
// Solidity: function addOrUpdateService(string name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, bool occupied) returns()
func (_FineTuneServing *FineTuneServingTransactor) AddOrUpdateService(opts *bind.TransactOpts, name string, url string, quota Quota, pricePerToken *big.Int, occupied bool) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "addOrUpdateService", name, url, quota, pricePerToken, occupied)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x1ded73bb.
//
// Solidity: function addOrUpdateService(string name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, bool occupied) returns()
func (_FineTuneServing *FineTuneServingSession) AddOrUpdateService(name string, url string, quota Quota, pricePerToken *big.Int, occupied bool) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddOrUpdateService(&_FineTuneServing.TransactOpts, name, url, quota, pricePerToken, occupied)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x1ded73bb.
//
// Solidity: function addOrUpdateService(string name, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, bool occupied) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) AddOrUpdateService(name string, url string, quota Quota, pricePerToken *big.Int, occupied bool) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddOrUpdateService(&_FineTuneServing.TransactOpts, name, url, quota, pricePerToken, occupied)
}

// AddProviderSigner is a paid mutator transaction binding the contract method 0xa4f66036.
//
// Solidity: function addProviderSigner(address user, address providerSigner) payable returns()
func (_FineTuneServing *FineTuneServingTransactor) AddProviderSigner(opts *bind.TransactOpts, user common.Address, providerSigner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "addProviderSigner", user, providerSigner)
}

// AddProviderSigner is a paid mutator transaction binding the contract method 0xa4f66036.
//
// Solidity: function addProviderSigner(address user, address providerSigner) payable returns()
func (_FineTuneServing *FineTuneServingSession) AddProviderSigner(user common.Address, providerSigner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddProviderSigner(&_FineTuneServing.TransactOpts, user, providerSigner)
}

// AddProviderSigner is a paid mutator transaction binding the contract method 0xa4f66036.
//
// Solidity: function addProviderSigner(address user, address providerSigner) payable returns()
func (_FineTuneServing *FineTuneServingTransactorSession) AddProviderSigner(user common.Address, providerSigner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.AddProviderSigner(&_FineTuneServing.TransactOpts, user, providerSigner)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x4c1b64cb.
//
// Solidity: function deleteAccount(address provider) returns()
func (_FineTuneServing *FineTuneServingTransactor) DeleteAccount(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "deleteAccount", provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x4c1b64cb.
//
// Solidity: function deleteAccount(address provider) returns()
func (_FineTuneServing *FineTuneServingSession) DeleteAccount(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.DeleteAccount(&_FineTuneServing.TransactOpts, provider)
}

// DeleteAccount is a paid mutator transaction binding the contract method 0x4c1b64cb.
//
// Solidity: function deleteAccount(address provider) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) DeleteAccount(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.DeleteAccount(&_FineTuneServing.TransactOpts, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0xe12d4a52.
//
// Solidity: function depositFund(address provider) payable returns()
func (_FineTuneServing *FineTuneServingTransactor) DepositFund(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "depositFund", provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0xe12d4a52.
//
// Solidity: function depositFund(address provider) payable returns()
func (_FineTuneServing *FineTuneServingSession) DepositFund(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.DepositFund(&_FineTuneServing.TransactOpts, provider)
}

// DepositFund is a paid mutator transaction binding the contract method 0xe12d4a52.
//
// Solidity: function depositFund(address provider) payable returns()
func (_FineTuneServing *FineTuneServingTransactorSession) DepositFund(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.DepositFund(&_FineTuneServing.TransactOpts, provider)
}

// Initialize is a paid mutator transaction binding the contract method 0xda35a26f.
//
// Solidity: function initialize(uint256 _locktime, address owner) returns()
func (_FineTuneServing *FineTuneServingTransactor) Initialize(opts *bind.TransactOpts, _locktime *big.Int, owner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "initialize", _locktime, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xda35a26f.
//
// Solidity: function initialize(uint256 _locktime, address owner) returns()
func (_FineTuneServing *FineTuneServingSession) Initialize(_locktime *big.Int, owner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.Initialize(&_FineTuneServing.TransactOpts, _locktime, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xda35a26f.
//
// Solidity: function initialize(uint256 _locktime, address owner) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) Initialize(_locktime *big.Int, owner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.Initialize(&_FineTuneServing.TransactOpts, _locktime, owner)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0xb9795b6d.
//
// Solidity: function processRefund(address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_FineTuneServing *FineTuneServingTransactor) ProcessRefund(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "processRefund", provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0xb9795b6d.
//
// Solidity: function processRefund(address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_FineTuneServing *FineTuneServingSession) ProcessRefund(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.ProcessRefund(&_FineTuneServing.TransactOpts, provider)
}

// ProcessRefund is a paid mutator transaction binding the contract method 0xb9795b6d.
//
// Solidity: function processRefund(address provider) returns(uint256 totalAmount, uint256 balance, uint256 pendingRefund)
func (_FineTuneServing *FineTuneServingTransactorSession) ProcessRefund(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.ProcessRefund(&_FineTuneServing.TransactOpts, provider)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_FineTuneServing *FineTuneServingTransactor) RemoveService(opts *bind.TransactOpts, name string) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "removeService", name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_FineTuneServing *FineTuneServingSession) RemoveService(name string) (*types.Transaction, error) {
	return _FineTuneServing.Contract.RemoveService(&_FineTuneServing.TransactOpts, name)
}

// RemoveService is a paid mutator transaction binding the contract method 0xf51acaea.
//
// Solidity: function removeService(string name) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) RemoveService(name string) (*types.Transaction, error) {
	return _FineTuneServing.Contract.RemoveService(&_FineTuneServing.TransactOpts, name)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_FineTuneServing *FineTuneServingTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_FineTuneServing *FineTuneServingSession) RenounceOwnership() (*types.Transaction, error) {
	return _FineTuneServing.Contract.RenounceOwnership(&_FineTuneServing.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_FineTuneServing *FineTuneServingTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _FineTuneServing.Contract.RenounceOwnership(&_FineTuneServing.TransactOpts)
}

// RequestRefund is a paid mutator transaction binding the contract method 0x99652de7.
//
// Solidity: function requestRefund(address provider, uint256 amount) returns()
func (_FineTuneServing *FineTuneServingTransactor) RequestRefund(opts *bind.TransactOpts, provider common.Address, amount *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "requestRefund", provider, amount)
}

// RequestRefund is a paid mutator transaction binding the contract method 0x99652de7.
//
// Solidity: function requestRefund(address provider, uint256 amount) returns()
func (_FineTuneServing *FineTuneServingSession) RequestRefund(provider common.Address, amount *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.Contract.RequestRefund(&_FineTuneServing.TransactOpts, provider, amount)
}

// RequestRefund is a paid mutator transaction binding the contract method 0x99652de7.
//
// Solidity: function requestRefund(address provider, uint256 amount) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) RequestRefund(provider common.Address, amount *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.Contract.RequestRefund(&_FineTuneServing.TransactOpts, provider, amount)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0xd81577ef.
//
// Solidity: function requestRefundAll(address provider) returns()
func (_FineTuneServing *FineTuneServingTransactor) RequestRefundAll(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "requestRefundAll", provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0xd81577ef.
//
// Solidity: function requestRefundAll(address provider) returns()
func (_FineTuneServing *FineTuneServingSession) RequestRefundAll(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.RequestRefundAll(&_FineTuneServing.TransactOpts, provider)
}

// RequestRefundAll is a paid mutator transaction binding the contract method 0xd81577ef.
//
// Solidity: function requestRefundAll(address provider) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) RequestRefundAll(provider common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.RequestRefundAll(&_FineTuneServing.TransactOpts, provider)
}

// SettleFees is a paid mutator transaction binding the contract method 0xf5cefe43.
//
// Solidity: function settleFees((uint256,bytes,bytes,bytes,uint256,uint256,address) verifierInput) returns()
func (_FineTuneServing *FineTuneServingTransactor) SettleFees(opts *bind.TransactOpts, verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "settleFees", verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0xf5cefe43.
//
// Solidity: function settleFees((uint256,bytes,bytes,bytes,uint256,uint256,address) verifierInput) returns()
func (_FineTuneServing *FineTuneServingSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuneServing.Contract.SettleFees(&_FineTuneServing.TransactOpts, verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0xf5cefe43.
//
// Solidity: function settleFees((uint256,bytes,bytes,bytes,uint256,uint256,address) verifierInput) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuneServing.Contract.SettleFees(&_FineTuneServing.TransactOpts, verifierInput)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_FineTuneServing *FineTuneServingTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_FineTuneServing *FineTuneServingSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.TransferOwnership(&_FineTuneServing.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _FineTuneServing.Contract.TransferOwnership(&_FineTuneServing.TransactOpts, newOwner)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_FineTuneServing *FineTuneServingTransactor) UpdateLockTime(opts *bind.TransactOpts, _locktime *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.contract.Transact(opts, "updateLockTime", _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_FineTuneServing *FineTuneServingSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.Contract.UpdateLockTime(&_FineTuneServing.TransactOpts, _locktime)
}

// UpdateLockTime is a paid mutator transaction binding the contract method 0xfbfa4e11.
//
// Solidity: function updateLockTime(uint256 _locktime) returns()
func (_FineTuneServing *FineTuneServingTransactorSession) UpdateLockTime(_locktime *big.Int) (*types.Transaction, error) {
	return _FineTuneServing.Contract.UpdateLockTime(&_FineTuneServing.TransactOpts, _locktime)
}

// FineTuneServingBalanceUpdatedIterator is returned from FilterBalanceUpdated and is used to iterate over the raw logs and unpacked data for BalanceUpdated events raised by the FineTuneServing contract.
type FineTuneServingBalanceUpdatedIterator struct {
	Event *FineTuneServingBalanceUpdated // Event containing the contract specifics and raw log

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
func (it *FineTuneServingBalanceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuneServingBalanceUpdated)
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
		it.Event = new(FineTuneServingBalanceUpdated)
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
func (it *FineTuneServingBalanceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuneServingBalanceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuneServingBalanceUpdated represents a BalanceUpdated event raised by the FineTuneServing contract.
type FineTuneServingBalanceUpdated struct {
	User          common.Address
	Provider      common.Address
	Amount        *big.Int
	PendingRefund *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterBalanceUpdated is a free log retrieval operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_FineTuneServing *FineTuneServingFilterer) FilterBalanceUpdated(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*FineTuneServingBalanceUpdatedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuneServing.contract.FilterLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingBalanceUpdatedIterator{contract: _FineTuneServing.contract, event: "BalanceUpdated", logs: logs, sub: sub}, nil
}

// WatchBalanceUpdated is a free log subscription operation binding the contract event 0x526824944047da5b81071fb6349412005c5da81380b336103fbe5dd34556c776.
//
// Solidity: event BalanceUpdated(address indexed user, address indexed provider, uint256 amount, uint256 pendingRefund)
func (_FineTuneServing *FineTuneServingFilterer) WatchBalanceUpdated(opts *bind.WatchOpts, sink chan<- *FineTuneServingBalanceUpdated, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuneServing.contract.WatchLogs(opts, "BalanceUpdated", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuneServingBalanceUpdated)
				if err := _FineTuneServing.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
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
func (_FineTuneServing *FineTuneServingFilterer) ParseBalanceUpdated(log types.Log) (*FineTuneServingBalanceUpdated, error) {
	event := new(FineTuneServingBalanceUpdated)
	if err := _FineTuneServing.contract.UnpackLog(event, "BalanceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuneServingOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the FineTuneServing contract.
type FineTuneServingOwnershipTransferredIterator struct {
	Event *FineTuneServingOwnershipTransferred // Event containing the contract specifics and raw log

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
func (it *FineTuneServingOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuneServingOwnershipTransferred)
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
		it.Event = new(FineTuneServingOwnershipTransferred)
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
func (it *FineTuneServingOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuneServingOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuneServingOwnershipTransferred represents a OwnershipTransferred event raised by the FineTuneServing contract.
type FineTuneServingOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_FineTuneServing *FineTuneServingFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*FineTuneServingOwnershipTransferredIterator, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _FineTuneServing.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingOwnershipTransferredIterator{contract: _FineTuneServing.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_FineTuneServing *FineTuneServingFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *FineTuneServingOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _FineTuneServing.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuneServingOwnershipTransferred)
				if err := _FineTuneServing.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
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
func (_FineTuneServing *FineTuneServingFilterer) ParseOwnershipTransferred(log types.Log) (*FineTuneServingOwnershipTransferred, error) {
	event := new(FineTuneServingOwnershipTransferred)
	if err := _FineTuneServing.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuneServingRefundRequestedIterator is returned from FilterRefundRequested and is used to iterate over the raw logs and unpacked data for RefundRequested events raised by the FineTuneServing contract.
type FineTuneServingRefundRequestedIterator struct {
	Event *FineTuneServingRefundRequested // Event containing the contract specifics and raw log

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
func (it *FineTuneServingRefundRequestedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuneServingRefundRequested)
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
		it.Event = new(FineTuneServingRefundRequested)
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
func (it *FineTuneServingRefundRequestedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuneServingRefundRequestedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuneServingRefundRequested represents a RefundRequested event raised by the FineTuneServing contract.
type FineTuneServingRefundRequested struct {
	User      common.Address
	Provider  common.Address
	Index     *big.Int
	Timestamp *big.Int
	Raw       types.Log // Blockchain specific contextual infos
}

// FilterRefundRequested is a free log retrieval operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_FineTuneServing *FineTuneServingFilterer) FilterRefundRequested(opts *bind.FilterOpts, user []common.Address, provider []common.Address, index []*big.Int) (*FineTuneServingRefundRequestedIterator, error) {

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

	logs, sub, err := _FineTuneServing.contract.FilterLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingRefundRequestedIterator{contract: _FineTuneServing.contract, event: "RefundRequested", logs: logs, sub: sub}, nil
}

// WatchRefundRequested is a free log subscription operation binding the contract event 0x54377dfdebf06f6df53fbda737d2dcd7e141f95bbfb0c1223437e856b9de3ac3.
//
// Solidity: event RefundRequested(address indexed user, address indexed provider, uint256 indexed index, uint256 timestamp)
func (_FineTuneServing *FineTuneServingFilterer) WatchRefundRequested(opts *bind.WatchOpts, sink chan<- *FineTuneServingRefundRequested, user []common.Address, provider []common.Address, index []*big.Int) (event.Subscription, error) {

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

	logs, sub, err := _FineTuneServing.contract.WatchLogs(opts, "RefundRequested", userRule, providerRule, indexRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuneServingRefundRequested)
				if err := _FineTuneServing.contract.UnpackLog(event, "RefundRequested", log); err != nil {
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
func (_FineTuneServing *FineTuneServingFilterer) ParseRefundRequested(log types.Log) (*FineTuneServingRefundRequested, error) {
	event := new(FineTuneServingRefundRequested)
	if err := _FineTuneServing.contract.UnpackLog(event, "RefundRequested", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuneServingServiceRemovedIterator is returned from FilterServiceRemoved and is used to iterate over the raw logs and unpacked data for ServiceRemoved events raised by the FineTuneServing contract.
type FineTuneServingServiceRemovedIterator struct {
	Event *FineTuneServingServiceRemoved // Event containing the contract specifics and raw log

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
func (it *FineTuneServingServiceRemovedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuneServingServiceRemoved)
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
		it.Event = new(FineTuneServingServiceRemoved)
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
func (it *FineTuneServingServiceRemovedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuneServingServiceRemovedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuneServingServiceRemoved represents a ServiceRemoved event raised by the FineTuneServing contract.
type FineTuneServingServiceRemoved struct {
	Service common.Address
	Name    common.Hash
	Raw     types.Log // Blockchain specific contextual infos
}

// FilterServiceRemoved is a free log retrieval operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed service, string indexed name)
func (_FineTuneServing *FineTuneServingFilterer) FilterServiceRemoved(opts *bind.FilterOpts, service []common.Address, name []string) (*FineTuneServingServiceRemovedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuneServing.contract.FilterLogs(opts, "ServiceRemoved", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingServiceRemovedIterator{contract: _FineTuneServing.contract, event: "ServiceRemoved", logs: logs, sub: sub}, nil
}

// WatchServiceRemoved is a free log subscription operation binding the contract event 0x68026479739e3662c0651578523384b94455e79bfb701ce111a3164591ceba73.
//
// Solidity: event ServiceRemoved(address indexed service, string indexed name)
func (_FineTuneServing *FineTuneServingFilterer) WatchServiceRemoved(opts *bind.WatchOpts, sink chan<- *FineTuneServingServiceRemoved, service []common.Address, name []string) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuneServing.contract.WatchLogs(opts, "ServiceRemoved", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuneServingServiceRemoved)
				if err := _FineTuneServing.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
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
func (_FineTuneServing *FineTuneServingFilterer) ParseServiceRemoved(log types.Log) (*FineTuneServingServiceRemoved, error) {
	event := new(FineTuneServingServiceRemoved)
	if err := _FineTuneServing.contract.UnpackLog(event, "ServiceRemoved", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuneServingServiceUpdatedIterator is returned from FilterServiceUpdated and is used to iterate over the raw logs and unpacked data for ServiceUpdated events raised by the FineTuneServing contract.
type FineTuneServingServiceUpdatedIterator struct {
	Event *FineTuneServingServiceUpdated // Event containing the contract specifics and raw log

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
func (it *FineTuneServingServiceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuneServingServiceUpdated)
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
		it.Event = new(FineTuneServingServiceUpdated)
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
func (it *FineTuneServingServiceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuneServingServiceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuneServingServiceUpdated represents a ServiceUpdated event raised by the FineTuneServing contract.
type FineTuneServingServiceUpdated struct {
	Service  common.Address
	Name     common.Hash
	Url      string
	Quota    Quota
	Occupied bool
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterServiceUpdated is a free log retrieval operation binding the contract event 0x33062556b04f2d601ba4449c1eddd323e0764abaa16546941dea98f564bc3d25.
//
// Solidity: event ServiceUpdated(address indexed service, string indexed name, string url, (uint256,uint256,uint256,uint256,string) quota, bool occupied)
func (_FineTuneServing *FineTuneServingFilterer) FilterServiceUpdated(opts *bind.FilterOpts, service []common.Address, name []string) (*FineTuneServingServiceUpdatedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuneServing.contract.FilterLogs(opts, "ServiceUpdated", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return &FineTuneServingServiceUpdatedIterator{contract: _FineTuneServing.contract, event: "ServiceUpdated", logs: logs, sub: sub}, nil
}

// WatchServiceUpdated is a free log subscription operation binding the contract event 0x33062556b04f2d601ba4449c1eddd323e0764abaa16546941dea98f564bc3d25.
//
// Solidity: event ServiceUpdated(address indexed service, string indexed name, string url, (uint256,uint256,uint256,uint256,string) quota, bool occupied)
func (_FineTuneServing *FineTuneServingFilterer) WatchServiceUpdated(opts *bind.WatchOpts, sink chan<- *FineTuneServingServiceUpdated, service []common.Address, name []string) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}
	var nameRule []interface{}
	for _, nameItem := range name {
		nameRule = append(nameRule, nameItem)
	}

	logs, sub, err := _FineTuneServing.contract.WatchLogs(opts, "ServiceUpdated", serviceRule, nameRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuneServingServiceUpdated)
				if err := _FineTuneServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
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

// ParseServiceUpdated is a log parse operation binding the contract event 0x33062556b04f2d601ba4449c1eddd323e0764abaa16546941dea98f564bc3d25.
//
// Solidity: event ServiceUpdated(address indexed service, string indexed name, string url, (uint256,uint256,uint256,uint256,string) quota, bool occupied)
func (_FineTuneServing *FineTuneServingFilterer) ParseServiceUpdated(log types.Log) (*FineTuneServingServiceUpdated, error) {
	event := new(FineTuneServingServiceUpdated)
	if err := _FineTuneServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
