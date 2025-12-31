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
	User               common.Address
	Provider           common.Address
	Nonce              *big.Int
	Balance            *big.Int
	PendingRefund      *big.Int
	Refunds            []Refund
	AdditionalInfo     string
	Acknowledged       bool
	ValidRefundsLength *big.Int
	Generation         *big.Int
	RevokedBitmap      *big.Int
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
	Provider              common.Address
	ServiceType           string
	Url                   string
	InputPrice            *big.Int
	OutputPrice           *big.Int
	UpdatedAt             *big.Int
	Model                 string
	Verifiability         string
	AdditionalInfo        string
	TeeSignerAddress      common.Address
	TeeSignerAcknowledged bool
}

// ServiceParams is an auto generated low-level Go binding around an user-defined struct.
type ServiceParams struct {
	ServiceType      string
	Url              string
	Model            string
	Verifiability    string
	InputPrice       *big.Int
	OutputPrice      *big.Int
	AdditionalInfo   string
	TeeSignerAddress common.Address
}

// TEESettlementData is an auto generated low-level Go binding around an user-defined struct.
type TEESettlementData struct {
	User         common.Address
	Provider     common.Address
	TotalFee     *big.Int
	RequestsHash [32]byte
	Nonce        *big.Int
	Signature    []byte
}

// InferenceServingMetaData contains all meta data concerning the InferenceServing contract.
var InferenceServingMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountNotExists\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"AdditionalInfoTooLong\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"size\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"max\",\"type\":\"uint256\"}],\"name\":\"BatchSizeTooLarge\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"caller\",\"type\":\"address\"}],\"name\":\"CallerNotLedger\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"CannotAddStakeWhenUpdating\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"}],\"name\":\"CannotRevokeWithNonZeroBalance\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"provided\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"required\",\"type\":\"uint256\"}],\"name\":\"InsufficientStake\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"addr\",\"type\":\"address\"}],\"name\":\"InvalidAddress\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"reason\",\"type\":\"string\"}],\"name\":\"InvalidTEESignature\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"max\",\"type\":\"uint256\"}],\"name\":\"LimitTooLarge\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"lockTime\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"min\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"max\",\"type\":\"uint256\"}],\"name\":\"LockTimeOutOfRange\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"NoSettlementsProvided\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"ServiceNotExist\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"TooManyRefunds\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"count\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"max\",\"type\":\"uint256\"}],\"name\":\"TooManySettlements\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"TransferFailed\",\"type\":\"error\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"newGeneration\",\"type\":\"uint256\"}],\"name\":\"AllTokensRevoked\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"name\":\"BalanceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":false,\"internalType\":\"address[]\",\"name\":\"users\",\"type\":\"address[]\"},{\"indexed\":false,\"internalType\":\"uint256[]\",\"name\":\"balances\",\"type\":\"uint256[]\"},{\"indexed\":false,\"internalType\":\"uint256[]\",\"name\":\"pendingRefunds\",\"type\":\"uint256[]\"}],\"name\":\"BatchBalanceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"lockTime\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"address\",\"name\":\"ledgerAddress\",\"type\":\"address\"}],\"name\":\"ContractInitialized\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":false,\"internalType\":\"uint8\",\"name\":\"version\",\"type\":\"uint8\"}],\"name\":\"Initialized\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"oldLockTime\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"newLockTime\",\"type\":\"uint256\"}],\"name\":\"LockTimeUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"ProviderStakeReturned\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"ProviderStaked\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"name\":\"ProviderTEESignerAcknowledged\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"RefundRequested\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"}],\"name\":\"ServiceRemoved\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"service\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"}],\"name\":\"ServiceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"enumSettlementStatus\",\"name\":\"status\",\"type\":\"uint8\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"unsettledAmount\",\"type\":\"uint256\"}],\"name\":\"TEESettlementResult\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint8\",\"name\":\"tokenId\",\"type\":\"uint8\"}],\"name\":\"TokenRevoked\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint8[]\",\"name\":\"tokenIds\",\"type\":\"uint8[]\"}],\"name\":\"TokensRevoked\",\"type\":\"event\"},{\"inputs\":[],\"name\":\"MAX_LOCKTIME\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"MIN_LOCKTIME\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"MIN_PROVIDER_STAKE\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"accountExists\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"name\":\"acknowledgeTEESigner\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"acknowledgeTEESignerByOwner\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"name\":\"addAccount\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"}],\"internalType\":\"structServiceParams\",\"name\":\"params\",\"type\":\"tuple\"}],\"name\":\"addOrUpdateService\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"deleteAccount\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"cancelRetrievingAmount\",\"type\":\"uint256\"}],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getAccount\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"generation\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"revokedBitmap\",\"type\":\"uint256\"}],\"internalType\":\"structAccount\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"offset\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"getAccountsByProvider\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"generation\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"revokedBitmap\",\"type\":\"uint256\"}],\"internalType\":\"structAccount[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"total\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"offset\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"getAccountsByUser\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"generation\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"revokedBitmap\",\"type\":\"uint256\"}],\"internalType\":\"structAccount[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"total\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"offset\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"getAllAccounts\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"generation\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"revokedBitmap\",\"type\":\"uint256\"}],\"internalType\":\"structAccount[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"total\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"offset\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"getAllServices\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"teeSignerAcknowledged\",\"type\":\"bool\"}],\"internalType\":\"structService[]\",\"name\":\"services\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"total\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address[]\",\"name\":\"users\",\"type\":\"address[]\"}],\"name\":\"getBatchAccountsByUsers\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"generation\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"revokedBitmap\",\"type\":\"uint256\"}],\"internalType\":\"structAccount[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getPendingRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getService\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"serviceType\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"inputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"outputPrice\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"model\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"verifiability\",\"type\":\"string\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"teeSignerAcknowledged\",\"type\":\"bool\"}],\"internalType\":\"structService\",\"name\":\"service\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"_ledgerAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"}],\"name\":\"initialize\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"initialized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint8\",\"name\":\"tokenId\",\"type\":\"uint8\"}],\"name\":\"isTokenRevoked\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"ledgerAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"lockTime\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address[]\",\"name\":\"users\",\"type\":\"address[]\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"migrateRefunds\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"cleanedCount\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"totalFee\",\"type\":\"uint256\"},{\"internalType\":\"bytes32\",\"name\":\"requestsHash\",\"type\":\"bytes32\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"}],\"internalType\":\"structTEESettlementData[]\",\"name\":\"settlements\",\"type\":\"tuple[]\"}],\"name\":\"previewSettlementResults\",\"outputs\":[{\"internalType\":\"address[]\",\"name\":\"failedUsers\",\"type\":\"address[]\"},{\"internalType\":\"enumSettlementStatus[]\",\"name\":\"failureReasons\",\"type\":\"uint8[]\"},{\"internalType\":\"address[]\",\"name\":\"partialUsers\",\"type\":\"address[]\"},{\"internalType\":\"uint256[]\",\"name\":\"partialAmounts\",\"type\":\"uint256[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"processRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"totalAmount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"removeService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"requestRefundAll\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"revokeAllTokens\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"revokeTEESignerAcknowledgement\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint8\",\"name\":\"tokenId\",\"type\":\"uint8\"}],\"name\":\"revokeToken\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint8[]\",\"name\":\"tokenIds\",\"type\":\"uint8[]\"}],\"name\":\"revokeTokens\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"serviceExists\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"totalFee\",\"type\":\"uint256\"},{\"internalType\":\"bytes32\",\"name\":\"requestsHash\",\"type\":\"bytes32\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"}],\"internalType\":\"structTEESettlementData[]\",\"name\":\"settlements\",\"type\":\"tuple[]\"}],\"name\":\"settleFeesWithTEE\",\"outputs\":[{\"internalType\":\"uint8[]\",\"name\":\"statuses\",\"type\":\"uint8[]\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"bytes4\",\"name\":\"interfaceId\",\"type\":\"bytes4\"}],\"name\":\"supportsInterface\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"}],\"name\":\"updateLockTime\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"stateMutability\":\"payable\",\"type\":\"receive\"}]",
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

// MAXLOCKTIME is a free data retrieval call binding the contract method 0x3ea527cb.
//
// Solidity: function MAX_LOCKTIME() view returns(uint256)
func (_InferenceServing *InferenceServingCaller) MAXLOCKTIME(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "MAX_LOCKTIME")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// MAXLOCKTIME is a free data retrieval call binding the contract method 0x3ea527cb.
//
// Solidity: function MAX_LOCKTIME() view returns(uint256)
func (_InferenceServing *InferenceServingSession) MAXLOCKTIME() (*big.Int, error) {
	return _InferenceServing.Contract.MAXLOCKTIME(&_InferenceServing.CallOpts)
}

// MAXLOCKTIME is a free data retrieval call binding the contract method 0x3ea527cb.
//
// Solidity: function MAX_LOCKTIME() view returns(uint256)
func (_InferenceServing *InferenceServingCallerSession) MAXLOCKTIME() (*big.Int, error) {
	return _InferenceServing.Contract.MAXLOCKTIME(&_InferenceServing.CallOpts)
}

// MINLOCKTIME is a free data retrieval call binding the contract method 0xad6dca3f.
//
// Solidity: function MIN_LOCKTIME() view returns(uint256)
func (_InferenceServing *InferenceServingCaller) MINLOCKTIME(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "MIN_LOCKTIME")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// MINLOCKTIME is a free data retrieval call binding the contract method 0xad6dca3f.
//
// Solidity: function MIN_LOCKTIME() view returns(uint256)
func (_InferenceServing *InferenceServingSession) MINLOCKTIME() (*big.Int, error) {
	return _InferenceServing.Contract.MINLOCKTIME(&_InferenceServing.CallOpts)
}

// MINLOCKTIME is a free data retrieval call binding the contract method 0xad6dca3f.
//
// Solidity: function MIN_LOCKTIME() view returns(uint256)
func (_InferenceServing *InferenceServingCallerSession) MINLOCKTIME() (*big.Int, error) {
	return _InferenceServing.Contract.MINLOCKTIME(&_InferenceServing.CallOpts)
}

// MINPROVIDERSTAKE is a free data retrieval call binding the contract method 0x650190e7.
//
// Solidity: function MIN_PROVIDER_STAKE() view returns(uint256)
func (_InferenceServing *InferenceServingCaller) MINPROVIDERSTAKE(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "MIN_PROVIDER_STAKE")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// MINPROVIDERSTAKE is a free data retrieval call binding the contract method 0x650190e7.
//
// Solidity: function MIN_PROVIDER_STAKE() view returns(uint256)
func (_InferenceServing *InferenceServingSession) MINPROVIDERSTAKE() (*big.Int, error) {
	return _InferenceServing.Contract.MINPROVIDERSTAKE(&_InferenceServing.CallOpts)
}

// MINPROVIDERSTAKE is a free data retrieval call binding the contract method 0x650190e7.
//
// Solidity: function MIN_PROVIDER_STAKE() view returns(uint256)
func (_InferenceServing *InferenceServingCallerSession) MINPROVIDERSTAKE() (*big.Int, error) {
	return _InferenceServing.Contract.MINPROVIDERSTAKE(&_InferenceServing.CallOpts)
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

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256))
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
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256))
func (_InferenceServing *InferenceServingSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _InferenceServing.Contract.GetAccount(&_InferenceServing.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256))
func (_InferenceServing *InferenceServingCallerSession) GetAccount(user common.Address, provider common.Address) (Account, error) {
	return _InferenceServing.Contract.GetAccount(&_InferenceServing.CallOpts, user, provider)
}

// GetAccountsByProvider is a free data retrieval call binding the contract method 0x1d73b9f5.
//
// Solidity: function getAccountsByProvider(address provider, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingCaller) GetAccountsByProvider(opts *bind.CallOpts, provider common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getAccountsByProvider", provider, offset, limit)

	outstruct := new(struct {
		Accounts []Account
		Total    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Accounts = *abi.ConvertType(out[0], new([]Account)).(*[]Account)
	outstruct.Total = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAccountsByProvider is a free data retrieval call binding the contract method 0x1d73b9f5.
//
// Solidity: function getAccountsByProvider(address provider, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingSession) GetAccountsByProvider(provider common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAccountsByProvider(&_InferenceServing.CallOpts, provider, offset, limit)
}

// GetAccountsByProvider is a free data retrieval call binding the contract method 0x1d73b9f5.
//
// Solidity: function getAccountsByProvider(address provider, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingCallerSession) GetAccountsByProvider(provider common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAccountsByProvider(&_InferenceServing.CallOpts, provider, offset, limit)
}

// GetAccountsByUser is a free data retrieval call binding the contract method 0x4fe63f4d.
//
// Solidity: function getAccountsByUser(address user, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingCaller) GetAccountsByUser(opts *bind.CallOpts, user common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getAccountsByUser", user, offset, limit)

	outstruct := new(struct {
		Accounts []Account
		Total    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Accounts = *abi.ConvertType(out[0], new([]Account)).(*[]Account)
	outstruct.Total = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAccountsByUser is a free data retrieval call binding the contract method 0x4fe63f4d.
//
// Solidity: function getAccountsByUser(address user, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingSession) GetAccountsByUser(user common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAccountsByUser(&_InferenceServing.CallOpts, user, offset, limit)
}

// GetAccountsByUser is a free data retrieval call binding the contract method 0x4fe63f4d.
//
// Solidity: function getAccountsByUser(address user, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingCallerSession) GetAccountsByUser(user common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAccountsByUser(&_InferenceServing.CallOpts, user, offset, limit)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x5bd7ace2.
//
// Solidity: function getAllAccounts(uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingCaller) GetAllAccounts(opts *bind.CallOpts, offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getAllAccounts", offset, limit)

	outstruct := new(struct {
		Accounts []Account
		Total    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Accounts = *abi.ConvertType(out[0], new([]Account)).(*[]Account)
	outstruct.Total = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAllAccounts is a free data retrieval call binding the contract method 0x5bd7ace2.
//
// Solidity: function getAllAccounts(uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingSession) GetAllAccounts(offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAllAccounts(&_InferenceServing.CallOpts, offset, limit)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x5bd7ace2.
//
// Solidity: function getAllAccounts(uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts, uint256 total)
func (_InferenceServing *InferenceServingCallerSession) GetAllAccounts(offset *big.Int, limit *big.Int) (struct {
	Accounts []Account
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAllAccounts(&_InferenceServing.CallOpts, offset, limit)
}

// GetAllServices is a free data retrieval call binding the contract method 0xa09cfca9.
//
// Solidity: function getAllServices(uint256 offset, uint256 limit) view returns((address,string,string,uint256,uint256,uint256,string,string,string,address,bool)[] services, uint256 total)
func (_InferenceServing *InferenceServingCaller) GetAllServices(opts *bind.CallOpts, offset *big.Int, limit *big.Int) (struct {
	Services []Service
	Total    *big.Int
}, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getAllServices", offset, limit)

	outstruct := new(struct {
		Services []Service
		Total    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Services = *abi.ConvertType(out[0], new([]Service)).(*[]Service)
	outstruct.Total = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAllServices is a free data retrieval call binding the contract method 0xa09cfca9.
//
// Solidity: function getAllServices(uint256 offset, uint256 limit) view returns((address,string,string,uint256,uint256,uint256,string,string,string,address,bool)[] services, uint256 total)
func (_InferenceServing *InferenceServingSession) GetAllServices(offset *big.Int, limit *big.Int) (struct {
	Services []Service
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAllServices(&_InferenceServing.CallOpts, offset, limit)
}

// GetAllServices is a free data retrieval call binding the contract method 0xa09cfca9.
//
// Solidity: function getAllServices(uint256 offset, uint256 limit) view returns((address,string,string,uint256,uint256,uint256,string,string,string,address,bool)[] services, uint256 total)
func (_InferenceServing *InferenceServingCallerSession) GetAllServices(offset *big.Int, limit *big.Int) (struct {
	Services []Service
	Total    *big.Int
}, error) {
	return _InferenceServing.Contract.GetAllServices(&_InferenceServing.CallOpts, offset, limit)
}

// GetBatchAccountsByUsers is a free data retrieval call binding the contract method 0xba16a750.
//
// Solidity: function getBatchAccountsByUsers(address[] users) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts)
func (_InferenceServing *InferenceServingCaller) GetBatchAccountsByUsers(opts *bind.CallOpts, users []common.Address) ([]Account, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getBatchAccountsByUsers", users)

	if err != nil {
		return *new([]Account), err
	}

	out0 := *abi.ConvertType(out[0], new([]Account)).(*[]Account)

	return out0, err

}

// GetBatchAccountsByUsers is a free data retrieval call binding the contract method 0xba16a750.
//
// Solidity: function getBatchAccountsByUsers(address[] users) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts)
func (_InferenceServing *InferenceServingSession) GetBatchAccountsByUsers(users []common.Address) ([]Account, error) {
	return _InferenceServing.Contract.GetBatchAccountsByUsers(&_InferenceServing.CallOpts, users)
}

// GetBatchAccountsByUsers is a free data retrieval call binding the contract method 0xba16a750.
//
// Solidity: function getBatchAccountsByUsers(address[] users) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,bool,uint256,uint256,uint256)[] accounts)
func (_InferenceServing *InferenceServingCallerSession) GetBatchAccountsByUsers(users []common.Address) ([]Account, error) {
	return _InferenceServing.Contract.GetBatchAccountsByUsers(&_InferenceServing.CallOpts, users)
}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_InferenceServing *InferenceServingCaller) GetPendingRefund(opts *bind.CallOpts, user common.Address, provider common.Address) (*big.Int, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getPendingRefund", user, provider)

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_InferenceServing *InferenceServingSession) GetPendingRefund(user common.Address, provider common.Address) (*big.Int, error) {
	return _InferenceServing.Contract.GetPendingRefund(&_InferenceServing.CallOpts, user, provider)
}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_InferenceServing *InferenceServingCallerSession) GetPendingRefund(user common.Address, provider common.Address) (*big.Int, error) {
	return _InferenceServing.Contract.GetPendingRefund(&_InferenceServing.CallOpts, user, provider)
}

// GetService is a free data retrieval call binding the contract method 0x15a52302.
//
// Solidity: function getService(address provider) view returns((address,string,string,uint256,uint256,uint256,string,string,string,address,bool) service)
func (_InferenceServing *InferenceServingCaller) GetService(opts *bind.CallOpts, provider common.Address) (Service, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "getService", provider)

	if err != nil {
		return *new(Service), err
	}

	out0 := *abi.ConvertType(out[0], new(Service)).(*Service)

	return out0, err

}

// GetService is a free data retrieval call binding the contract method 0x15a52302.
//
// Solidity: function getService(address provider) view returns((address,string,string,uint256,uint256,uint256,string,string,string,address,bool) service)
func (_InferenceServing *InferenceServingSession) GetService(provider common.Address) (Service, error) {
	return _InferenceServing.Contract.GetService(&_InferenceServing.CallOpts, provider)
}

// GetService is a free data retrieval call binding the contract method 0x15a52302.
//
// Solidity: function getService(address provider) view returns((address,string,string,uint256,uint256,uint256,string,string,string,address,bool) service)
func (_InferenceServing *InferenceServingCallerSession) GetService(provider common.Address) (Service, error) {
	return _InferenceServing.Contract.GetService(&_InferenceServing.CallOpts, provider)
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

// IsTokenRevoked is a free data retrieval call binding the contract method 0x405a85e8.
//
// Solidity: function isTokenRevoked(address user, address provider, uint8 tokenId) view returns(bool)
func (_InferenceServing *InferenceServingCaller) IsTokenRevoked(opts *bind.CallOpts, user common.Address, provider common.Address, tokenId uint8) (bool, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "isTokenRevoked", user, provider, tokenId)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// IsTokenRevoked is a free data retrieval call binding the contract method 0x405a85e8.
//
// Solidity: function isTokenRevoked(address user, address provider, uint8 tokenId) view returns(bool)
func (_InferenceServing *InferenceServingSession) IsTokenRevoked(user common.Address, provider common.Address, tokenId uint8) (bool, error) {
	return _InferenceServing.Contract.IsTokenRevoked(&_InferenceServing.CallOpts, user, provider, tokenId)
}

// IsTokenRevoked is a free data retrieval call binding the contract method 0x405a85e8.
//
// Solidity: function isTokenRevoked(address user, address provider, uint8 tokenId) view returns(bool)
func (_InferenceServing *InferenceServingCallerSession) IsTokenRevoked(user common.Address, provider common.Address, tokenId uint8) (bool, error) {
	return _InferenceServing.Contract.IsTokenRevoked(&_InferenceServing.CallOpts, user, provider, tokenId)
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

// PreviewSettlementResults is a free data retrieval call binding the contract method 0x28b60476.
//
// Solidity: function previewSettlementResults((address,address,uint256,bytes32,uint256,bytes)[] settlements) view returns(address[] failedUsers, uint8[] failureReasons, address[] partialUsers, uint256[] partialAmounts)
func (_InferenceServing *InferenceServingCaller) PreviewSettlementResults(opts *bind.CallOpts, settlements []TEESettlementData) (struct {
	FailedUsers    []common.Address
	FailureReasons []uint8
	PartialUsers   []common.Address
	PartialAmounts []*big.Int
}, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "previewSettlementResults", settlements)

	outstruct := new(struct {
		FailedUsers    []common.Address
		FailureReasons []uint8
		PartialUsers   []common.Address
		PartialAmounts []*big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.FailedUsers = *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)
	outstruct.FailureReasons = *abi.ConvertType(out[1], new([]uint8)).(*[]uint8)
	outstruct.PartialUsers = *abi.ConvertType(out[2], new([]common.Address)).(*[]common.Address)
	outstruct.PartialAmounts = *abi.ConvertType(out[3], new([]*big.Int)).(*[]*big.Int)

	return *outstruct, err

}

// PreviewSettlementResults is a free data retrieval call binding the contract method 0x28b60476.
//
// Solidity: function previewSettlementResults((address,address,uint256,bytes32,uint256,bytes)[] settlements) view returns(address[] failedUsers, uint8[] failureReasons, address[] partialUsers, uint256[] partialAmounts)
func (_InferenceServing *InferenceServingSession) PreviewSettlementResults(settlements []TEESettlementData) (struct {
	FailedUsers    []common.Address
	FailureReasons []uint8
	PartialUsers   []common.Address
	PartialAmounts []*big.Int
}, error) {
	return _InferenceServing.Contract.PreviewSettlementResults(&_InferenceServing.CallOpts, settlements)
}

// PreviewSettlementResults is a free data retrieval call binding the contract method 0x28b60476.
//
// Solidity: function previewSettlementResults((address,address,uint256,bytes32,uint256,bytes)[] settlements) view returns(address[] failedUsers, uint8[] failureReasons, address[] partialUsers, uint256[] partialAmounts)
func (_InferenceServing *InferenceServingCallerSession) PreviewSettlementResults(settlements []TEESettlementData) (struct {
	FailedUsers    []common.Address
	FailureReasons []uint8
	PartialUsers   []common.Address
	PartialAmounts []*big.Int
}, error) {
	return _InferenceServing.Contract.PreviewSettlementResults(&_InferenceServing.CallOpts, settlements)
}

// ServiceExists is a free data retrieval call binding the contract method 0x0a2a8f88.
//
// Solidity: function serviceExists(address provider) view returns(bool)
func (_InferenceServing *InferenceServingCaller) ServiceExists(opts *bind.CallOpts, provider common.Address) (bool, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "serviceExists", provider)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// ServiceExists is a free data retrieval call binding the contract method 0x0a2a8f88.
//
// Solidity: function serviceExists(address provider) view returns(bool)
func (_InferenceServing *InferenceServingSession) ServiceExists(provider common.Address) (bool, error) {
	return _InferenceServing.Contract.ServiceExists(&_InferenceServing.CallOpts, provider)
}

// ServiceExists is a free data retrieval call binding the contract method 0x0a2a8f88.
//
// Solidity: function serviceExists(address provider) view returns(bool)
func (_InferenceServing *InferenceServingCallerSession) ServiceExists(provider common.Address) (bool, error) {
	return _InferenceServing.Contract.ServiceExists(&_InferenceServing.CallOpts, provider)
}

// SupportsInterface is a free data retrieval call binding the contract method 0x01ffc9a7.
//
// Solidity: function supportsInterface(bytes4 interfaceId) view returns(bool)
func (_InferenceServing *InferenceServingCaller) SupportsInterface(opts *bind.CallOpts, interfaceId [4]byte) (bool, error) {
	var out []interface{}
	err := _InferenceServing.contract.Call(opts, &out, "supportsInterface", interfaceId)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// SupportsInterface is a free data retrieval call binding the contract method 0x01ffc9a7.
//
// Solidity: function supportsInterface(bytes4 interfaceId) view returns(bool)
func (_InferenceServing *InferenceServingSession) SupportsInterface(interfaceId [4]byte) (bool, error) {
	return _InferenceServing.Contract.SupportsInterface(&_InferenceServing.CallOpts, interfaceId)
}

// SupportsInterface is a free data retrieval call binding the contract method 0x01ffc9a7.
//
// Solidity: function supportsInterface(bytes4 interfaceId) view returns(bool)
func (_InferenceServing *InferenceServingCallerSession) SupportsInterface(interfaceId [4]byte) (bool, error) {
	return _InferenceServing.Contract.SupportsInterface(&_InferenceServing.CallOpts, interfaceId)
}

// AcknowledgeTEESigner is a paid mutator transaction binding the contract method 0x7ff6fc1c.
//
// Solidity: function acknowledgeTEESigner(address provider, bool acknowledged) returns()
func (_InferenceServing *InferenceServingTransactor) AcknowledgeTEESigner(opts *bind.TransactOpts, provider common.Address, acknowledged bool) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "acknowledgeTEESigner", provider, acknowledged)
}

// AcknowledgeTEESigner is a paid mutator transaction binding the contract method 0x7ff6fc1c.
//
// Solidity: function acknowledgeTEESigner(address provider, bool acknowledged) returns()
func (_InferenceServing *InferenceServingSession) AcknowledgeTEESigner(provider common.Address, acknowledged bool) (*types.Transaction, error) {
	return _InferenceServing.Contract.AcknowledgeTEESigner(&_InferenceServing.TransactOpts, provider, acknowledged)
}

// AcknowledgeTEESigner is a paid mutator transaction binding the contract method 0x7ff6fc1c.
//
// Solidity: function acknowledgeTEESigner(address provider, bool acknowledged) returns()
func (_InferenceServing *InferenceServingTransactorSession) AcknowledgeTEESigner(provider common.Address, acknowledged bool) (*types.Transaction, error) {
	return _InferenceServing.Contract.AcknowledgeTEESigner(&_InferenceServing.TransactOpts, provider, acknowledged)
}

// AcknowledgeTEESignerByOwner is a paid mutator transaction binding the contract method 0xb2394d09.
//
// Solidity: function acknowledgeTEESignerByOwner(address provider) returns()
func (_InferenceServing *InferenceServingTransactor) AcknowledgeTEESignerByOwner(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "acknowledgeTEESignerByOwner", provider)
}

// AcknowledgeTEESignerByOwner is a paid mutator transaction binding the contract method 0xb2394d09.
//
// Solidity: function acknowledgeTEESignerByOwner(address provider) returns()
func (_InferenceServing *InferenceServingSession) AcknowledgeTEESignerByOwner(provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.AcknowledgeTEESignerByOwner(&_InferenceServing.TransactOpts, provider)
}

// AcknowledgeTEESignerByOwner is a paid mutator transaction binding the contract method 0xb2394d09.
//
// Solidity: function acknowledgeTEESignerByOwner(address provider) returns()
func (_InferenceServing *InferenceServingTransactorSession) AcknowledgeTEESignerByOwner(provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.AcknowledgeTEESignerByOwner(&_InferenceServing.TransactOpts, provider)
}

// AddAccount is a paid mutator transaction binding the contract method 0xe50688f9.
//
// Solidity: function addAccount(address user, address provider, string additionalInfo) payable returns()
func (_InferenceServing *InferenceServingTransactor) AddAccount(opts *bind.TransactOpts, user common.Address, provider common.Address, additionalInfo string) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "addAccount", user, provider, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0xe50688f9.
//
// Solidity: function addAccount(address user, address provider, string additionalInfo) payable returns()
func (_InferenceServing *InferenceServingSession) AddAccount(user common.Address, provider common.Address, additionalInfo string) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddAccount(&_InferenceServing.TransactOpts, user, provider, additionalInfo)
}

// AddAccount is a paid mutator transaction binding the contract method 0xe50688f9.
//
// Solidity: function addAccount(address user, address provider, string additionalInfo) payable returns()
func (_InferenceServing *InferenceServingTransactorSession) AddAccount(user common.Address, provider common.Address, additionalInfo string) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddAccount(&_InferenceServing.TransactOpts, user, provider, additionalInfo)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x398c8e4e.
//
// Solidity: function addOrUpdateService((string,string,string,string,uint256,uint256,string,address) params) payable returns()
func (_InferenceServing *InferenceServingTransactor) AddOrUpdateService(opts *bind.TransactOpts, params ServiceParams) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "addOrUpdateService", params)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x398c8e4e.
//
// Solidity: function addOrUpdateService((string,string,string,string,uint256,uint256,string,address) params) payable returns()
func (_InferenceServing *InferenceServingSession) AddOrUpdateService(params ServiceParams) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddOrUpdateService(&_InferenceServing.TransactOpts, params)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x398c8e4e.
//
// Solidity: function addOrUpdateService((string,string,string,string,uint256,uint256,string,address) params) payable returns()
func (_InferenceServing *InferenceServingTransactorSession) AddOrUpdateService(params ServiceParams) (*types.Transaction, error) {
	return _InferenceServing.Contract.AddOrUpdateService(&_InferenceServing.TransactOpts, params)
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

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_InferenceServing *InferenceServingTransactor) DepositFund(opts *bind.TransactOpts, user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "depositFund", user, provider, cancelRetrievingAmount)
}

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_InferenceServing *InferenceServingSession) DepositFund(user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _InferenceServing.Contract.DepositFund(&_InferenceServing.TransactOpts, user, provider, cancelRetrievingAmount)
}

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_InferenceServing *InferenceServingTransactorSession) DepositFund(user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _InferenceServing.Contract.DepositFund(&_InferenceServing.TransactOpts, user, provider, cancelRetrievingAmount)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner) returns()
func (_InferenceServing *InferenceServingTransactor) Initialize(opts *bind.TransactOpts, _locktime *big.Int, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "initialize", _locktime, _ledgerAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner) returns()
func (_InferenceServing *InferenceServingSession) Initialize(_locktime *big.Int, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.Initialize(&_InferenceServing.TransactOpts, _locktime, _ledgerAddress, owner)
}

// Initialize is a paid mutator transaction binding the contract method 0xb4988fd0.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner) returns()
func (_InferenceServing *InferenceServingTransactorSession) Initialize(_locktime *big.Int, _ledgerAddress common.Address, owner common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.Initialize(&_InferenceServing.TransactOpts, _locktime, _ledgerAddress, owner)
}

// MigrateRefunds is a paid mutator transaction binding the contract method 0x72ca77d9.
//
// Solidity: function migrateRefunds(address[] users, address provider) returns(uint256 cleanedCount)
func (_InferenceServing *InferenceServingTransactor) MigrateRefunds(opts *bind.TransactOpts, users []common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "migrateRefunds", users, provider)
}

// MigrateRefunds is a paid mutator transaction binding the contract method 0x72ca77d9.
//
// Solidity: function migrateRefunds(address[] users, address provider) returns(uint256 cleanedCount)
func (_InferenceServing *InferenceServingSession) MigrateRefunds(users []common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.MigrateRefunds(&_InferenceServing.TransactOpts, users, provider)
}

// MigrateRefunds is a paid mutator transaction binding the contract method 0x72ca77d9.
//
// Solidity: function migrateRefunds(address[] users, address provider) returns(uint256 cleanedCount)
func (_InferenceServing *InferenceServingTransactorSession) MigrateRefunds(users []common.Address, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.MigrateRefunds(&_InferenceServing.TransactOpts, users, provider)
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

// RemoveService is a paid mutator transaction binding the contract method 0xbbee42d9.
//
// Solidity: function removeService() returns()
func (_InferenceServing *InferenceServingTransactor) RemoveService(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "removeService")
}

// RemoveService is a paid mutator transaction binding the contract method 0xbbee42d9.
//
// Solidity: function removeService() returns()
func (_InferenceServing *InferenceServingSession) RemoveService() (*types.Transaction, error) {
	return _InferenceServing.Contract.RemoveService(&_InferenceServing.TransactOpts)
}

// RemoveService is a paid mutator transaction binding the contract method 0xbbee42d9.
//
// Solidity: function removeService() returns()
func (_InferenceServing *InferenceServingTransactorSession) RemoveService() (*types.Transaction, error) {
	return _InferenceServing.Contract.RemoveService(&_InferenceServing.TransactOpts)
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

// RevokeAllTokens is a paid mutator transaction binding the contract method 0xcab33d8f.
//
// Solidity: function revokeAllTokens(address provider) returns()
func (_InferenceServing *InferenceServingTransactor) RevokeAllTokens(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "revokeAllTokens", provider)
}

// RevokeAllTokens is a paid mutator transaction binding the contract method 0xcab33d8f.
//
// Solidity: function revokeAllTokens(address provider) returns()
func (_InferenceServing *InferenceServingSession) RevokeAllTokens(provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeAllTokens(&_InferenceServing.TransactOpts, provider)
}

// RevokeAllTokens is a paid mutator transaction binding the contract method 0xcab33d8f.
//
// Solidity: function revokeAllTokens(address provider) returns()
func (_InferenceServing *InferenceServingTransactorSession) RevokeAllTokens(provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeAllTokens(&_InferenceServing.TransactOpts, provider)
}

// RevokeTEESignerAcknowledgement is a paid mutator transaction binding the contract method 0xddf96abd.
//
// Solidity: function revokeTEESignerAcknowledgement(address provider) returns()
func (_InferenceServing *InferenceServingTransactor) RevokeTEESignerAcknowledgement(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "revokeTEESignerAcknowledgement", provider)
}

// RevokeTEESignerAcknowledgement is a paid mutator transaction binding the contract method 0xddf96abd.
//
// Solidity: function revokeTEESignerAcknowledgement(address provider) returns()
func (_InferenceServing *InferenceServingSession) RevokeTEESignerAcknowledgement(provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeTEESignerAcknowledgement(&_InferenceServing.TransactOpts, provider)
}

// RevokeTEESignerAcknowledgement is a paid mutator transaction binding the contract method 0xddf96abd.
//
// Solidity: function revokeTEESignerAcknowledgement(address provider) returns()
func (_InferenceServing *InferenceServingTransactorSession) RevokeTEESignerAcknowledgement(provider common.Address) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeTEESignerAcknowledgement(&_InferenceServing.TransactOpts, provider)
}

// RevokeToken is a paid mutator transaction binding the contract method 0x1d07cb97.
//
// Solidity: function revokeToken(address provider, uint8 tokenId) returns()
func (_InferenceServing *InferenceServingTransactor) RevokeToken(opts *bind.TransactOpts, provider common.Address, tokenId uint8) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "revokeToken", provider, tokenId)
}

// RevokeToken is a paid mutator transaction binding the contract method 0x1d07cb97.
//
// Solidity: function revokeToken(address provider, uint8 tokenId) returns()
func (_InferenceServing *InferenceServingSession) RevokeToken(provider common.Address, tokenId uint8) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeToken(&_InferenceServing.TransactOpts, provider, tokenId)
}

// RevokeToken is a paid mutator transaction binding the contract method 0x1d07cb97.
//
// Solidity: function revokeToken(address provider, uint8 tokenId) returns()
func (_InferenceServing *InferenceServingTransactorSession) RevokeToken(provider common.Address, tokenId uint8) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeToken(&_InferenceServing.TransactOpts, provider, tokenId)
}

// RevokeTokens is a paid mutator transaction binding the contract method 0x17c30a03.
//
// Solidity: function revokeTokens(address provider, uint8[] tokenIds) returns()
func (_InferenceServing *InferenceServingTransactor) RevokeTokens(opts *bind.TransactOpts, provider common.Address, tokenIds []uint8) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "revokeTokens", provider, tokenIds)
}

// RevokeTokens is a paid mutator transaction binding the contract method 0x17c30a03.
//
// Solidity: function revokeTokens(address provider, uint8[] tokenIds) returns()
func (_InferenceServing *InferenceServingSession) RevokeTokens(provider common.Address, tokenIds []uint8) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeTokens(&_InferenceServing.TransactOpts, provider, tokenIds)
}

// RevokeTokens is a paid mutator transaction binding the contract method 0x17c30a03.
//
// Solidity: function revokeTokens(address provider, uint8[] tokenIds) returns()
func (_InferenceServing *InferenceServingTransactorSession) RevokeTokens(provider common.Address, tokenIds []uint8) (*types.Transaction, error) {
	return _InferenceServing.Contract.RevokeTokens(&_InferenceServing.TransactOpts, provider, tokenIds)
}

// SettleFeesWithTEE is a paid mutator transaction binding the contract method 0x8be74119.
//
// Solidity: function settleFeesWithTEE((address,address,uint256,bytes32,uint256,bytes)[] settlements) returns(uint8[] statuses)
func (_InferenceServing *InferenceServingTransactor) SettleFeesWithTEE(opts *bind.TransactOpts, settlements []TEESettlementData) (*types.Transaction, error) {
	return _InferenceServing.contract.Transact(opts, "settleFeesWithTEE", settlements)
}

// SettleFeesWithTEE is a paid mutator transaction binding the contract method 0x8be74119.
//
// Solidity: function settleFeesWithTEE((address,address,uint256,bytes32,uint256,bytes)[] settlements) returns(uint8[] statuses)
func (_InferenceServing *InferenceServingSession) SettleFeesWithTEE(settlements []TEESettlementData) (*types.Transaction, error) {
	return _InferenceServing.Contract.SettleFeesWithTEE(&_InferenceServing.TransactOpts, settlements)
}

// SettleFeesWithTEE is a paid mutator transaction binding the contract method 0x8be74119.
//
// Solidity: function settleFeesWithTEE((address,address,uint256,bytes32,uint256,bytes)[] settlements) returns(uint8[] statuses)
func (_InferenceServing *InferenceServingTransactorSession) SettleFeesWithTEE(settlements []TEESettlementData) (*types.Transaction, error) {
	return _InferenceServing.Contract.SettleFeesWithTEE(&_InferenceServing.TransactOpts, settlements)
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

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_InferenceServing *InferenceServingTransactor) Receive(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _InferenceServing.contract.RawTransact(opts, nil) // calldata is disallowed for receive function
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_InferenceServing *InferenceServingSession) Receive() (*types.Transaction, error) {
	return _InferenceServing.Contract.Receive(&_InferenceServing.TransactOpts)
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_InferenceServing *InferenceServingTransactorSession) Receive() (*types.Transaction, error) {
	return _InferenceServing.Contract.Receive(&_InferenceServing.TransactOpts)
}

// InferenceServingAllTokensRevokedIterator is returned from FilterAllTokensRevoked and is used to iterate over the raw logs and unpacked data for AllTokensRevoked events raised by the InferenceServing contract.
type InferenceServingAllTokensRevokedIterator struct {
	Event *InferenceServingAllTokensRevoked // Event containing the contract specifics and raw log

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
func (it *InferenceServingAllTokensRevokedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingAllTokensRevoked)
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
		it.Event = new(InferenceServingAllTokensRevoked)
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
func (it *InferenceServingAllTokensRevokedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingAllTokensRevokedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingAllTokensRevoked represents a AllTokensRevoked event raised by the InferenceServing contract.
type InferenceServingAllTokensRevoked struct {
	User          common.Address
	Provider      common.Address
	NewGeneration *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterAllTokensRevoked is a free log retrieval operation binding the contract event 0x989726e0ba0fa7747d8a6618329b929bfcc4f3ac46de5930a368310944f75547.
//
// Solidity: event AllTokensRevoked(address indexed user, address indexed provider, uint256 newGeneration)
func (_InferenceServing *InferenceServingFilterer) FilterAllTokensRevoked(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*InferenceServingAllTokensRevokedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "AllTokensRevoked", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingAllTokensRevokedIterator{contract: _InferenceServing.contract, event: "AllTokensRevoked", logs: logs, sub: sub}, nil
}

// WatchAllTokensRevoked is a free log subscription operation binding the contract event 0x989726e0ba0fa7747d8a6618329b929bfcc4f3ac46de5930a368310944f75547.
//
// Solidity: event AllTokensRevoked(address indexed user, address indexed provider, uint256 newGeneration)
func (_InferenceServing *InferenceServingFilterer) WatchAllTokensRevoked(opts *bind.WatchOpts, sink chan<- *InferenceServingAllTokensRevoked, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "AllTokensRevoked", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingAllTokensRevoked)
				if err := _InferenceServing.contract.UnpackLog(event, "AllTokensRevoked", log); err != nil {
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

// ParseAllTokensRevoked is a log parse operation binding the contract event 0x989726e0ba0fa7747d8a6618329b929bfcc4f3ac46de5930a368310944f75547.
//
// Solidity: event AllTokensRevoked(address indexed user, address indexed provider, uint256 newGeneration)
func (_InferenceServing *InferenceServingFilterer) ParseAllTokensRevoked(log types.Log) (*InferenceServingAllTokensRevoked, error) {
	event := new(InferenceServingAllTokensRevoked)
	if err := _InferenceServing.contract.UnpackLog(event, "AllTokensRevoked", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
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

// InferenceServingBatchBalanceUpdatedIterator is returned from FilterBatchBalanceUpdated and is used to iterate over the raw logs and unpacked data for BatchBalanceUpdated events raised by the InferenceServing contract.
type InferenceServingBatchBalanceUpdatedIterator struct {
	Event *InferenceServingBatchBalanceUpdated // Event containing the contract specifics and raw log

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
func (it *InferenceServingBatchBalanceUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingBatchBalanceUpdated)
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
		it.Event = new(InferenceServingBatchBalanceUpdated)
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
func (it *InferenceServingBatchBalanceUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingBatchBalanceUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingBatchBalanceUpdated represents a BatchBalanceUpdated event raised by the InferenceServing contract.
type InferenceServingBatchBalanceUpdated struct {
	Users          []common.Address
	Balances       []*big.Int
	PendingRefunds []*big.Int
	Raw            types.Log // Blockchain specific contextual infos
}

// FilterBatchBalanceUpdated is a free log retrieval operation binding the contract event 0xd384db5eeac71810277e2e83c4ea8a5de5c7e8c62ed87dc2855b73e005932adc.
//
// Solidity: event BatchBalanceUpdated(address[] users, uint256[] balances, uint256[] pendingRefunds)
func (_InferenceServing *InferenceServingFilterer) FilterBatchBalanceUpdated(opts *bind.FilterOpts) (*InferenceServingBatchBalanceUpdatedIterator, error) {

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "BatchBalanceUpdated")
	if err != nil {
		return nil, err
	}
	return &InferenceServingBatchBalanceUpdatedIterator{contract: _InferenceServing.contract, event: "BatchBalanceUpdated", logs: logs, sub: sub}, nil
}

// WatchBatchBalanceUpdated is a free log subscription operation binding the contract event 0xd384db5eeac71810277e2e83c4ea8a5de5c7e8c62ed87dc2855b73e005932adc.
//
// Solidity: event BatchBalanceUpdated(address[] users, uint256[] balances, uint256[] pendingRefunds)
func (_InferenceServing *InferenceServingFilterer) WatchBatchBalanceUpdated(opts *bind.WatchOpts, sink chan<- *InferenceServingBatchBalanceUpdated) (event.Subscription, error) {

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "BatchBalanceUpdated")
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingBatchBalanceUpdated)
				if err := _InferenceServing.contract.UnpackLog(event, "BatchBalanceUpdated", log); err != nil {
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

// ParseBatchBalanceUpdated is a log parse operation binding the contract event 0xd384db5eeac71810277e2e83c4ea8a5de5c7e8c62ed87dc2855b73e005932adc.
//
// Solidity: event BatchBalanceUpdated(address[] users, uint256[] balances, uint256[] pendingRefunds)
func (_InferenceServing *InferenceServingFilterer) ParseBatchBalanceUpdated(log types.Log) (*InferenceServingBatchBalanceUpdated, error) {
	event := new(InferenceServingBatchBalanceUpdated)
	if err := _InferenceServing.contract.UnpackLog(event, "BatchBalanceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingContractInitializedIterator is returned from FilterContractInitialized and is used to iterate over the raw logs and unpacked data for ContractInitialized events raised by the InferenceServing contract.
type InferenceServingContractInitializedIterator struct {
	Event *InferenceServingContractInitialized // Event containing the contract specifics and raw log

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
func (it *InferenceServingContractInitializedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingContractInitialized)
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
		it.Event = new(InferenceServingContractInitialized)
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
func (it *InferenceServingContractInitializedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingContractInitializedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingContractInitialized represents a ContractInitialized event raised by the InferenceServing contract.
type InferenceServingContractInitialized struct {
	Owner         common.Address
	LockTime      *big.Int
	LedgerAddress common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterContractInitialized is a free log retrieval operation binding the contract event 0x9e1c480deaf2caaa9170098a3275b53d208beaeb277601363025975cf8274eb6.
//
// Solidity: event ContractInitialized(address indexed owner, uint256 lockTime, address ledgerAddress)
func (_InferenceServing *InferenceServingFilterer) FilterContractInitialized(opts *bind.FilterOpts, owner []common.Address) (*InferenceServingContractInitializedIterator, error) {

	var ownerRule []interface{}
	for _, ownerItem := range owner {
		ownerRule = append(ownerRule, ownerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ContractInitialized", ownerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingContractInitializedIterator{contract: _InferenceServing.contract, event: "ContractInitialized", logs: logs, sub: sub}, nil
}

// WatchContractInitialized is a free log subscription operation binding the contract event 0x9e1c480deaf2caaa9170098a3275b53d208beaeb277601363025975cf8274eb6.
//
// Solidity: event ContractInitialized(address indexed owner, uint256 lockTime, address ledgerAddress)
func (_InferenceServing *InferenceServingFilterer) WatchContractInitialized(opts *bind.WatchOpts, sink chan<- *InferenceServingContractInitialized, owner []common.Address) (event.Subscription, error) {

	var ownerRule []interface{}
	for _, ownerItem := range owner {
		ownerRule = append(ownerRule, ownerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ContractInitialized", ownerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingContractInitialized)
				if err := _InferenceServing.contract.UnpackLog(event, "ContractInitialized", log); err != nil {
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

// ParseContractInitialized is a log parse operation binding the contract event 0x9e1c480deaf2caaa9170098a3275b53d208beaeb277601363025975cf8274eb6.
//
// Solidity: event ContractInitialized(address indexed owner, uint256 lockTime, address ledgerAddress)
func (_InferenceServing *InferenceServingFilterer) ParseContractInitialized(log types.Log) (*InferenceServingContractInitialized, error) {
	event := new(InferenceServingContractInitialized)
	if err := _InferenceServing.contract.UnpackLog(event, "ContractInitialized", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingInitializedIterator is returned from FilterInitialized and is used to iterate over the raw logs and unpacked data for Initialized events raised by the InferenceServing contract.
type InferenceServingInitializedIterator struct {
	Event *InferenceServingInitialized // Event containing the contract specifics and raw log

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
func (it *InferenceServingInitializedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingInitialized)
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
		it.Event = new(InferenceServingInitialized)
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
func (it *InferenceServingInitializedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingInitializedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingInitialized represents a Initialized event raised by the InferenceServing contract.
type InferenceServingInitialized struct {
	Version uint8
	Raw     types.Log // Blockchain specific contextual infos
}

// FilterInitialized is a free log retrieval operation binding the contract event 0x7f26b83ff96e1f2b6a682f133852f6798a09c465da95921460cefb3847402498.
//
// Solidity: event Initialized(uint8 version)
func (_InferenceServing *InferenceServingFilterer) FilterInitialized(opts *bind.FilterOpts) (*InferenceServingInitializedIterator, error) {

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "Initialized")
	if err != nil {
		return nil, err
	}
	return &InferenceServingInitializedIterator{contract: _InferenceServing.contract, event: "Initialized", logs: logs, sub: sub}, nil
}

// WatchInitialized is a free log subscription operation binding the contract event 0x7f26b83ff96e1f2b6a682f133852f6798a09c465da95921460cefb3847402498.
//
// Solidity: event Initialized(uint8 version)
func (_InferenceServing *InferenceServingFilterer) WatchInitialized(opts *bind.WatchOpts, sink chan<- *InferenceServingInitialized) (event.Subscription, error) {

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "Initialized")
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingInitialized)
				if err := _InferenceServing.contract.UnpackLog(event, "Initialized", log); err != nil {
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

// ParseInitialized is a log parse operation binding the contract event 0x7f26b83ff96e1f2b6a682f133852f6798a09c465da95921460cefb3847402498.
//
// Solidity: event Initialized(uint8 version)
func (_InferenceServing *InferenceServingFilterer) ParseInitialized(log types.Log) (*InferenceServingInitialized, error) {
	event := new(InferenceServingInitialized)
	if err := _InferenceServing.contract.UnpackLog(event, "Initialized", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingLockTimeUpdatedIterator is returned from FilterLockTimeUpdated and is used to iterate over the raw logs and unpacked data for LockTimeUpdated events raised by the InferenceServing contract.
type InferenceServingLockTimeUpdatedIterator struct {
	Event *InferenceServingLockTimeUpdated // Event containing the contract specifics and raw log

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
func (it *InferenceServingLockTimeUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingLockTimeUpdated)
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
		it.Event = new(InferenceServingLockTimeUpdated)
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
func (it *InferenceServingLockTimeUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingLockTimeUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingLockTimeUpdated represents a LockTimeUpdated event raised by the InferenceServing contract.
type InferenceServingLockTimeUpdated struct {
	OldLockTime *big.Int
	NewLockTime *big.Int
	Raw         types.Log // Blockchain specific contextual infos
}

// FilterLockTimeUpdated is a free log retrieval operation binding the contract event 0x5707a70527b6cbb892bfe5d8739a8f0643d3212d9b1139bc31c742e731c65270.
//
// Solidity: event LockTimeUpdated(uint256 oldLockTime, uint256 newLockTime)
func (_InferenceServing *InferenceServingFilterer) FilterLockTimeUpdated(opts *bind.FilterOpts) (*InferenceServingLockTimeUpdatedIterator, error) {

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "LockTimeUpdated")
	if err != nil {
		return nil, err
	}
	return &InferenceServingLockTimeUpdatedIterator{contract: _InferenceServing.contract, event: "LockTimeUpdated", logs: logs, sub: sub}, nil
}

// WatchLockTimeUpdated is a free log subscription operation binding the contract event 0x5707a70527b6cbb892bfe5d8739a8f0643d3212d9b1139bc31c742e731c65270.
//
// Solidity: event LockTimeUpdated(uint256 oldLockTime, uint256 newLockTime)
func (_InferenceServing *InferenceServingFilterer) WatchLockTimeUpdated(opts *bind.WatchOpts, sink chan<- *InferenceServingLockTimeUpdated) (event.Subscription, error) {

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "LockTimeUpdated")
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingLockTimeUpdated)
				if err := _InferenceServing.contract.UnpackLog(event, "LockTimeUpdated", log); err != nil {
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

// ParseLockTimeUpdated is a log parse operation binding the contract event 0x5707a70527b6cbb892bfe5d8739a8f0643d3212d9b1139bc31c742e731c65270.
//
// Solidity: event LockTimeUpdated(uint256 oldLockTime, uint256 newLockTime)
func (_InferenceServing *InferenceServingFilterer) ParseLockTimeUpdated(log types.Log) (*InferenceServingLockTimeUpdated, error) {
	event := new(InferenceServingLockTimeUpdated)
	if err := _InferenceServing.contract.UnpackLog(event, "LockTimeUpdated", log); err != nil {
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

// InferenceServingProviderStakeReturnedIterator is returned from FilterProviderStakeReturned and is used to iterate over the raw logs and unpacked data for ProviderStakeReturned events raised by the InferenceServing contract.
type InferenceServingProviderStakeReturnedIterator struct {
	Event *InferenceServingProviderStakeReturned // Event containing the contract specifics and raw log

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
func (it *InferenceServingProviderStakeReturnedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingProviderStakeReturned)
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
		it.Event = new(InferenceServingProviderStakeReturned)
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
func (it *InferenceServingProviderStakeReturnedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingProviderStakeReturnedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingProviderStakeReturned represents a ProviderStakeReturned event raised by the InferenceServing contract.
type InferenceServingProviderStakeReturned struct {
	Provider common.Address
	Amount   *big.Int
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterProviderStakeReturned is a free log retrieval operation binding the contract event 0x17f7db034d4b59fadec3e44a684cb4396ca10fd036c4e4f718bf06e993715882.
//
// Solidity: event ProviderStakeReturned(address indexed provider, uint256 amount)
func (_InferenceServing *InferenceServingFilterer) FilterProviderStakeReturned(opts *bind.FilterOpts, provider []common.Address) (*InferenceServingProviderStakeReturnedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ProviderStakeReturned", providerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingProviderStakeReturnedIterator{contract: _InferenceServing.contract, event: "ProviderStakeReturned", logs: logs, sub: sub}, nil
}

// WatchProviderStakeReturned is a free log subscription operation binding the contract event 0x17f7db034d4b59fadec3e44a684cb4396ca10fd036c4e4f718bf06e993715882.
//
// Solidity: event ProviderStakeReturned(address indexed provider, uint256 amount)
func (_InferenceServing *InferenceServingFilterer) WatchProviderStakeReturned(opts *bind.WatchOpts, sink chan<- *InferenceServingProviderStakeReturned, provider []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ProviderStakeReturned", providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingProviderStakeReturned)
				if err := _InferenceServing.contract.UnpackLog(event, "ProviderStakeReturned", log); err != nil {
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

// ParseProviderStakeReturned is a log parse operation binding the contract event 0x17f7db034d4b59fadec3e44a684cb4396ca10fd036c4e4f718bf06e993715882.
//
// Solidity: event ProviderStakeReturned(address indexed provider, uint256 amount)
func (_InferenceServing *InferenceServingFilterer) ParseProviderStakeReturned(log types.Log) (*InferenceServingProviderStakeReturned, error) {
	event := new(InferenceServingProviderStakeReturned)
	if err := _InferenceServing.contract.UnpackLog(event, "ProviderStakeReturned", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingProviderStakedIterator is returned from FilterProviderStaked and is used to iterate over the raw logs and unpacked data for ProviderStaked events raised by the InferenceServing contract.
type InferenceServingProviderStakedIterator struct {
	Event *InferenceServingProviderStaked // Event containing the contract specifics and raw log

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
func (it *InferenceServingProviderStakedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingProviderStaked)
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
		it.Event = new(InferenceServingProviderStaked)
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
func (it *InferenceServingProviderStakedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingProviderStakedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingProviderStaked represents a ProviderStaked event raised by the InferenceServing contract.
type InferenceServingProviderStaked struct {
	Provider common.Address
	Amount   *big.Int
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterProviderStaked is a free log retrieval operation binding the contract event 0xcd6dbb0e62eeb71e114bae8b2e2547921dd19209bebf32b595be3e7d247dbbb4.
//
// Solidity: event ProviderStaked(address indexed provider, uint256 amount)
func (_InferenceServing *InferenceServingFilterer) FilterProviderStaked(opts *bind.FilterOpts, provider []common.Address) (*InferenceServingProviderStakedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ProviderStaked", providerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingProviderStakedIterator{contract: _InferenceServing.contract, event: "ProviderStaked", logs: logs, sub: sub}, nil
}

// WatchProviderStaked is a free log subscription operation binding the contract event 0xcd6dbb0e62eeb71e114bae8b2e2547921dd19209bebf32b595be3e7d247dbbb4.
//
// Solidity: event ProviderStaked(address indexed provider, uint256 amount)
func (_InferenceServing *InferenceServingFilterer) WatchProviderStaked(opts *bind.WatchOpts, sink chan<- *InferenceServingProviderStaked, provider []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ProviderStaked", providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingProviderStaked)
				if err := _InferenceServing.contract.UnpackLog(event, "ProviderStaked", log); err != nil {
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

// ParseProviderStaked is a log parse operation binding the contract event 0xcd6dbb0e62eeb71e114bae8b2e2547921dd19209bebf32b595be3e7d247dbbb4.
//
// Solidity: event ProviderStaked(address indexed provider, uint256 amount)
func (_InferenceServing *InferenceServingFilterer) ParseProviderStaked(log types.Log) (*InferenceServingProviderStaked, error) {
	event := new(InferenceServingProviderStaked)
	if err := _InferenceServing.contract.UnpackLog(event, "ProviderStaked", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingProviderTEESignerAcknowledgedIterator is returned from FilterProviderTEESignerAcknowledged and is used to iterate over the raw logs and unpacked data for ProviderTEESignerAcknowledged events raised by the InferenceServing contract.
type InferenceServingProviderTEESignerAcknowledgedIterator struct {
	Event *InferenceServingProviderTEESignerAcknowledged // Event containing the contract specifics and raw log

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
func (it *InferenceServingProviderTEESignerAcknowledgedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingProviderTEESignerAcknowledged)
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
		it.Event = new(InferenceServingProviderTEESignerAcknowledged)
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
func (it *InferenceServingProviderTEESignerAcknowledgedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingProviderTEESignerAcknowledgedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingProviderTEESignerAcknowledged represents a ProviderTEESignerAcknowledged event raised by the InferenceServing contract.
type InferenceServingProviderTEESignerAcknowledged struct {
	Provider         common.Address
	TeeSignerAddress common.Address
	Acknowledged     bool
	Raw              types.Log // Blockchain specific contextual infos
}

// FilterProviderTEESignerAcknowledged is a free log retrieval operation binding the contract event 0x4909107c46469d21135443e891c6ecae55b5baa31b338d50f391935308b08f89.
//
// Solidity: event ProviderTEESignerAcknowledged(address indexed provider, address indexed teeSignerAddress, bool acknowledged)
func (_InferenceServing *InferenceServingFilterer) FilterProviderTEESignerAcknowledged(opts *bind.FilterOpts, provider []common.Address, teeSignerAddress []common.Address) (*InferenceServingProviderTEESignerAcknowledgedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var teeSignerAddressRule []interface{}
	for _, teeSignerAddressItem := range teeSignerAddress {
		teeSignerAddressRule = append(teeSignerAddressRule, teeSignerAddressItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ProviderTEESignerAcknowledged", providerRule, teeSignerAddressRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingProviderTEESignerAcknowledgedIterator{contract: _InferenceServing.contract, event: "ProviderTEESignerAcknowledged", logs: logs, sub: sub}, nil
}

// WatchProviderTEESignerAcknowledged is a free log subscription operation binding the contract event 0x4909107c46469d21135443e891c6ecae55b5baa31b338d50f391935308b08f89.
//
// Solidity: event ProviderTEESignerAcknowledged(address indexed provider, address indexed teeSignerAddress, bool acknowledged)
func (_InferenceServing *InferenceServingFilterer) WatchProviderTEESignerAcknowledged(opts *bind.WatchOpts, sink chan<- *InferenceServingProviderTEESignerAcknowledged, provider []common.Address, teeSignerAddress []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var teeSignerAddressRule []interface{}
	for _, teeSignerAddressItem := range teeSignerAddress {
		teeSignerAddressRule = append(teeSignerAddressRule, teeSignerAddressItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ProviderTEESignerAcknowledged", providerRule, teeSignerAddressRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingProviderTEESignerAcknowledged)
				if err := _InferenceServing.contract.UnpackLog(event, "ProviderTEESignerAcknowledged", log); err != nil {
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

// ParseProviderTEESignerAcknowledged is a log parse operation binding the contract event 0x4909107c46469d21135443e891c6ecae55b5baa31b338d50f391935308b08f89.
//
// Solidity: event ProviderTEESignerAcknowledged(address indexed provider, address indexed teeSignerAddress, bool acknowledged)
func (_InferenceServing *InferenceServingFilterer) ParseProviderTEESignerAcknowledged(log types.Log) (*InferenceServingProviderTEESignerAcknowledged, error) {
	event := new(InferenceServingProviderTEESignerAcknowledged)
	if err := _InferenceServing.contract.UnpackLog(event, "ProviderTEESignerAcknowledged", log); err != nil {
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
	Raw     types.Log // Blockchain specific contextual infos
}

// FilterServiceRemoved is a free log retrieval operation binding the contract event 0x29d546abb6e94f4f04d5bdccb6682316f597d43776078f47e273f000e77b2a91.
//
// Solidity: event ServiceRemoved(address indexed service)
func (_InferenceServing *InferenceServingFilterer) FilterServiceRemoved(opts *bind.FilterOpts, service []common.Address) (*InferenceServingServiceRemovedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ServiceRemoved", serviceRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingServiceRemovedIterator{contract: _InferenceServing.contract, event: "ServiceRemoved", logs: logs, sub: sub}, nil
}

// WatchServiceRemoved is a free log subscription operation binding the contract event 0x29d546abb6e94f4f04d5bdccb6682316f597d43776078f47e273f000e77b2a91.
//
// Solidity: event ServiceRemoved(address indexed service)
func (_InferenceServing *InferenceServingFilterer) WatchServiceRemoved(opts *bind.WatchOpts, sink chan<- *InferenceServingServiceRemoved, service []common.Address) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ServiceRemoved", serviceRule)
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

// ParseServiceRemoved is a log parse operation binding the contract event 0x29d546abb6e94f4f04d5bdccb6682316f597d43776078f47e273f000e77b2a91.
//
// Solidity: event ServiceRemoved(address indexed service)
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
	ServiceType   string
	Url           string
	InputPrice    *big.Int
	OutputPrice   *big.Int
	UpdatedAt     *big.Int
	Model         string
	Verifiability string
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterServiceUpdated is a free log retrieval operation binding the contract event 0x30ecc203691b2d18e17ee75d47caf34a3fb9f86e855f7e0414d3cec26d0c424b.
//
// Solidity: event ServiceUpdated(address indexed service, string serviceType, string url, uint256 inputPrice, uint256 outputPrice, uint256 updatedAt, string model, string verifiability)
func (_InferenceServing *InferenceServingFilterer) FilterServiceUpdated(opts *bind.FilterOpts, service []common.Address) (*InferenceServingServiceUpdatedIterator, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "ServiceUpdated", serviceRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingServiceUpdatedIterator{contract: _InferenceServing.contract, event: "ServiceUpdated", logs: logs, sub: sub}, nil
}

// WatchServiceUpdated is a free log subscription operation binding the contract event 0x30ecc203691b2d18e17ee75d47caf34a3fb9f86e855f7e0414d3cec26d0c424b.
//
// Solidity: event ServiceUpdated(address indexed service, string serviceType, string url, uint256 inputPrice, uint256 outputPrice, uint256 updatedAt, string model, string verifiability)
func (_InferenceServing *InferenceServingFilterer) WatchServiceUpdated(opts *bind.WatchOpts, sink chan<- *InferenceServingServiceUpdated, service []common.Address) (event.Subscription, error) {

	var serviceRule []interface{}
	for _, serviceItem := range service {
		serviceRule = append(serviceRule, serviceItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "ServiceUpdated", serviceRule)
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

// ParseServiceUpdated is a log parse operation binding the contract event 0x30ecc203691b2d18e17ee75d47caf34a3fb9f86e855f7e0414d3cec26d0c424b.
//
// Solidity: event ServiceUpdated(address indexed service, string serviceType, string url, uint256 inputPrice, uint256 outputPrice, uint256 updatedAt, string model, string verifiability)
func (_InferenceServing *InferenceServingFilterer) ParseServiceUpdated(log types.Log) (*InferenceServingServiceUpdated, error) {
	event := new(InferenceServingServiceUpdated)
	if err := _InferenceServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingTEESettlementResultIterator is returned from FilterTEESettlementResult and is used to iterate over the raw logs and unpacked data for TEESettlementResult events raised by the InferenceServing contract.
type InferenceServingTEESettlementResultIterator struct {
	Event *InferenceServingTEESettlementResult // Event containing the contract specifics and raw log

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
func (it *InferenceServingTEESettlementResultIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingTEESettlementResult)
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
		it.Event = new(InferenceServingTEESettlementResult)
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
func (it *InferenceServingTEESettlementResultIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingTEESettlementResultIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingTEESettlementResult represents a TEESettlementResult event raised by the InferenceServing contract.
type InferenceServingTEESettlementResult struct {
	User            common.Address
	Status          uint8
	UnsettledAmount *big.Int
	Raw             types.Log // Blockchain specific contextual infos
}

// FilterTEESettlementResult is a free log retrieval operation binding the contract event 0x1f69e5b87fd0ce34b3760ba6e5d8aa95a36e316c3ba44e1e65a9d0eb9e96d0bf.
//
// Solidity: event TEESettlementResult(address indexed user, uint8 status, uint256 unsettledAmount)
func (_InferenceServing *InferenceServingFilterer) FilterTEESettlementResult(opts *bind.FilterOpts, user []common.Address) (*InferenceServingTEESettlementResultIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "TEESettlementResult", userRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingTEESettlementResultIterator{contract: _InferenceServing.contract, event: "TEESettlementResult", logs: logs, sub: sub}, nil
}

// WatchTEESettlementResult is a free log subscription operation binding the contract event 0x1f69e5b87fd0ce34b3760ba6e5d8aa95a36e316c3ba44e1e65a9d0eb9e96d0bf.
//
// Solidity: event TEESettlementResult(address indexed user, uint8 status, uint256 unsettledAmount)
func (_InferenceServing *InferenceServingFilterer) WatchTEESettlementResult(opts *bind.WatchOpts, sink chan<- *InferenceServingTEESettlementResult, user []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "TEESettlementResult", userRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingTEESettlementResult)
				if err := _InferenceServing.contract.UnpackLog(event, "TEESettlementResult", log); err != nil {
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

// ParseTEESettlementResult is a log parse operation binding the contract event 0x1f69e5b87fd0ce34b3760ba6e5d8aa95a36e316c3ba44e1e65a9d0eb9e96d0bf.
//
// Solidity: event TEESettlementResult(address indexed user, uint8 status, uint256 unsettledAmount)
func (_InferenceServing *InferenceServingFilterer) ParseTEESettlementResult(log types.Log) (*InferenceServingTEESettlementResult, error) {
	event := new(InferenceServingTEESettlementResult)
	if err := _InferenceServing.contract.UnpackLog(event, "TEESettlementResult", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingTokenRevokedIterator is returned from FilterTokenRevoked and is used to iterate over the raw logs and unpacked data for TokenRevoked events raised by the InferenceServing contract.
type InferenceServingTokenRevokedIterator struct {
	Event *InferenceServingTokenRevoked // Event containing the contract specifics and raw log

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
func (it *InferenceServingTokenRevokedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingTokenRevoked)
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
		it.Event = new(InferenceServingTokenRevoked)
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
func (it *InferenceServingTokenRevokedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingTokenRevokedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingTokenRevoked represents a TokenRevoked event raised by the InferenceServing contract.
type InferenceServingTokenRevoked struct {
	User     common.Address
	Provider common.Address
	TokenId  uint8
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterTokenRevoked is a free log retrieval operation binding the contract event 0xcf558f061a5a4e28657b8e5c662b95b8258d880896e7a859299457be036ed4a4.
//
// Solidity: event TokenRevoked(address indexed user, address indexed provider, uint8 tokenId)
func (_InferenceServing *InferenceServingFilterer) FilterTokenRevoked(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*InferenceServingTokenRevokedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "TokenRevoked", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingTokenRevokedIterator{contract: _InferenceServing.contract, event: "TokenRevoked", logs: logs, sub: sub}, nil
}

// WatchTokenRevoked is a free log subscription operation binding the contract event 0xcf558f061a5a4e28657b8e5c662b95b8258d880896e7a859299457be036ed4a4.
//
// Solidity: event TokenRevoked(address indexed user, address indexed provider, uint8 tokenId)
func (_InferenceServing *InferenceServingFilterer) WatchTokenRevoked(opts *bind.WatchOpts, sink chan<- *InferenceServingTokenRevoked, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "TokenRevoked", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingTokenRevoked)
				if err := _InferenceServing.contract.UnpackLog(event, "TokenRevoked", log); err != nil {
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

// ParseTokenRevoked is a log parse operation binding the contract event 0xcf558f061a5a4e28657b8e5c662b95b8258d880896e7a859299457be036ed4a4.
//
// Solidity: event TokenRevoked(address indexed user, address indexed provider, uint8 tokenId)
func (_InferenceServing *InferenceServingFilterer) ParseTokenRevoked(log types.Log) (*InferenceServingTokenRevoked, error) {
	event := new(InferenceServingTokenRevoked)
	if err := _InferenceServing.contract.UnpackLog(event, "TokenRevoked", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// InferenceServingTokensRevokedIterator is returned from FilterTokensRevoked and is used to iterate over the raw logs and unpacked data for TokensRevoked events raised by the InferenceServing contract.
type InferenceServingTokensRevokedIterator struct {
	Event *InferenceServingTokensRevoked // Event containing the contract specifics and raw log

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
func (it *InferenceServingTokensRevokedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(InferenceServingTokensRevoked)
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
		it.Event = new(InferenceServingTokensRevoked)
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
func (it *InferenceServingTokensRevokedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *InferenceServingTokensRevokedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// InferenceServingTokensRevoked represents a TokensRevoked event raised by the InferenceServing contract.
type InferenceServingTokensRevoked struct {
	User     common.Address
	Provider common.Address
	TokenIds []uint8
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterTokensRevoked is a free log retrieval operation binding the contract event 0x70147140547b3e1660123a6725577c5574b4d40747abe6ff7c458f449e18b84f.
//
// Solidity: event TokensRevoked(address indexed user, address indexed provider, uint8[] tokenIds)
func (_InferenceServing *InferenceServingFilterer) FilterTokensRevoked(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*InferenceServingTokensRevokedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.FilterLogs(opts, "TokensRevoked", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &InferenceServingTokensRevokedIterator{contract: _InferenceServing.contract, event: "TokensRevoked", logs: logs, sub: sub}, nil
}

// WatchTokensRevoked is a free log subscription operation binding the contract event 0x70147140547b3e1660123a6725577c5574b4d40747abe6ff7c458f449e18b84f.
//
// Solidity: event TokensRevoked(address indexed user, address indexed provider, uint8[] tokenIds)
func (_InferenceServing *InferenceServingFilterer) WatchTokensRevoked(opts *bind.WatchOpts, sink chan<- *InferenceServingTokensRevoked, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _InferenceServing.contract.WatchLogs(opts, "TokensRevoked", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(InferenceServingTokensRevoked)
				if err := _InferenceServing.contract.UnpackLog(event, "TokensRevoked", log); err != nil {
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

// ParseTokensRevoked is a log parse operation binding the contract event 0x70147140547b3e1660123a6725577c5574b4d40747abe6ff7c458f449e18b84f.
//
// Solidity: event TokensRevoked(address indexed user, address indexed provider, uint8[] tokenIds)
func (_InferenceServing *InferenceServingFilterer) ParseTokensRevoked(log types.Log) (*InferenceServingTokensRevoked, error) {
	event := new(InferenceServingTokensRevoked)
	if err := _InferenceServing.contract.UnpackLog(event, "TokensRevoked", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
