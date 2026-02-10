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

// AccountDetails is an auto generated low-level Go binding around an user-defined struct.
type AccountDetails struct {
	User               common.Address
	Provider           common.Address
	Nonce              *big.Int
	Balance            *big.Int
	PendingRefund      *big.Int
	Refunds            []Refund
	AdditionalInfo     string
	Deliverables       []Deliverable
	ValidRefundsLength *big.Int
	DeliverablesHead   *big.Int
	DeliverablesCount  *big.Int
	Acknowledged       bool
}

// AccountSummary is an auto generated low-level Go binding around an user-defined struct.
type AccountSummary struct {
	User               common.Address
	Provider           common.Address
	Nonce              *big.Int
	Balance            *big.Int
	PendingRefund      *big.Int
	AdditionalInfo     string
	ValidRefundsLength *big.Int
	DeliverablesCount  *big.Int
	Acknowledged       bool
}

// Deliverable is an auto generated low-level Go binding around an user-defined struct.
type Deliverable struct {
	Id              string
	ModelRootHash   []byte
	EncryptedSecret []byte
	Acknowledged    bool
	Timestamp       *big.Int
	Settled         bool
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
	Index               *big.Int
	Amount              *big.Int
	CreatedAt           *big.Int
	DeprecatedProcessed bool
}

// Service is an auto generated low-level Go binding around an user-defined struct.
type Service struct {
	Provider              common.Address
	Url                   string
	Quota                 Quota
	PricePerToken         *big.Int
	Occupied              bool
	Models                []string
	TeeSignerAddress      common.Address
	TeeSignerAcknowledged bool
}

// VerifierInput is an auto generated low-level Go binding around an user-defined struct.
type VerifierInput struct {
	Id              string
	EncryptedSecret []byte
	ModelRootHash   []byte
	Nonce           *big.Int
	Signature       []byte
	TaskFee         *big.Int
	User            common.Address
}

// FineTuningServingMetaData contains all meta data concerning the FineTuningServing contract.
var FineTuningServingMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"AccountNotExists\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"AdditionalInfoTooLong\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"AlreadyInitialized\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"size\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"maxSize\",\"type\":\"uint256\"}],\"name\":\"BatchSizeTooLarge\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"CallerNotLedger\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"CannotAcknowledgeSettledDeliverable\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"CannotAddStakeWhenUpdating\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"CannotEvictUnsettledDeliverable\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"}],\"name\":\"CannotRevokeWithNonZeroBalance\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"DeliverableAlreadyExists\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"DeliverableAlreadySettled\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"length\",\"type\":\"uint256\"}],\"name\":\"DeliverableIdInvalidLength\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"DeliverableNotExists\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"DirectDepositsDisabled\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"ETHTransferFailed\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"provided\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"required\",\"type\":\"uint256\"}],\"name\":\"InsufficientStake\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"InvalidLedgerAddress\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"reason\",\"type\":\"string\"}],\"name\":\"InvalidVerifierInput\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"LimitTooLarge\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"lockTime\",\"type\":\"uint256\"}],\"name\":\"LockTimeOutOfRange\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"percentage\",\"type\":\"uint256\"}],\"name\":\"PenaltyPercentageTooHigh\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"PreviousDeliverableNotAcknowledged\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"SecretShouldBeEmpty\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"SecretShouldNotBeEmpty\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"ServiceNotExist\",\"type\":\"error\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"TooManyRefunds\",\"type\":\"error\"},{\"inputs\":[],\"name\":\"TransferToLedgerFailed\",\"type\":\"error\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"refundedAmount\",\"type\":\"uint256\"}],\"name\":\"AccountDeleted\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"name\":\"BalanceUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"deliverableId\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"DeliverableAcknowledged\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"deliverableId\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"DeliverableAdded\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"evictedDeliverableId\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"newDeliverableId\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"DeliverableEvicted\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"deliverableId\",\"type\":\"string\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"fee\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"}],\"name\":\"FeesSettled\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":false,\"internalType\":\"uint8\",\"name\":\"version\",\"type\":\"uint8\"}],\"name\":\"Initialized\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"oldLockTime\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"newLockTime\",\"type\":\"uint256\"}],\"name\":\"LockTimeUpdated\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"ProviderStakeReturned\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"ProviderStaked\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"name\":\"ProviderTEESignerAcknowledged\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"timestamp\",\"type\":\"uint256\"}],\"name\":\"RefundRequested\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"ServiceRemoved\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"indexed\":false,\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"}],\"name\":\"ServiceUpdated\",\"type\":\"event\"},{\"inputs\":[],\"name\":\"MAX_LOCKTIME\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"MIN_LOCKTIME\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"MIN_PROVIDER_STAKE\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"accountExists\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"acknowledgeDeliverable\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"name\":\"acknowledgeTEESigner\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"acknowledgeTEESignerByOwner\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"}],\"name\":\"addAccount\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"}],\"name\":\"addDeliverable\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"},{\"internalType\":\"string[]\",\"name\":\"models\",\"type\":\"string[]\"},{\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"}],\"name\":\"addOrUpdateService\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"deleteAccount\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"cancelRetrievingAmount\",\"type\":\"uint256\"}],\"name\":\"depositFund\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getAccount\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"index\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"createdAt\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"_deprecated_processed\",\"type\":\"bool\"}],\"internalType\":\"structRefund[]\",\"name\":\"refunds\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint248\",\"name\":\"timestamp\",\"type\":\"uint248\"},{\"internalType\":\"bool\",\"name\":\"settled\",\"type\":\"bool\"}],\"internalType\":\"structDeliverable[]\",\"name\":\"deliverables\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"deliverablesHead\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"deliverablesCount\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structAccountDetails\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"offset\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"getAccountsByProvider\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"deliverablesCount\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structAccountSummary[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"total\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"offset\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"getAccountsByUser\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"deliverablesCount\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structAccountSummary[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"total\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"offset\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"limit\",\"type\":\"uint256\"}],\"name\":\"getAllAccounts\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"deliverablesCount\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structAccountSummary[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"},{\"internalType\":\"uint256\",\"name\":\"total\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getAllServices\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"},{\"internalType\":\"string[]\",\"name\":\"models\",\"type\":\"string[]\"},{\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"teeSignerAcknowledged\",\"type\":\"bool\"}],\"internalType\":\"structService[]\",\"name\":\"services\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address[]\",\"name\":\"users\",\"type\":\"address[]\"}],\"name\":\"getBatchAccountsByUsers\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"additionalInfo\",\"type\":\"string\"},{\"internalType\":\"uint256\",\"name\":\"validRefundsLength\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"deliverablesCount\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"}],\"internalType\":\"structAccountSummary[]\",\"name\":\"accounts\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"}],\"name\":\"getDeliverable\",\"outputs\":[{\"components\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint248\",\"name\":\"timestamp\",\"type\":\"uint248\"},{\"internalType\":\"bool\",\"name\":\"settled\",\"type\":\"bool\"}],\"internalType\":\"structDeliverable\",\"name\":\"\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getDeliverables\",\"outputs\":[{\"components\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bool\",\"name\":\"acknowledged\",\"type\":\"bool\"},{\"internalType\":\"uint248\",\"name\":\"timestamp\",\"type\":\"uint248\"},{\"internalType\":\"bool\",\"name\":\"settled\",\"type\":\"bool\"}],\"internalType\":\"structDeliverable[]\",\"name\":\"\",\"type\":\"tuple[]\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getPendingRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"getService\",\"outputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"url\",\"type\":\"string\"},{\"components\":[{\"internalType\":\"uint256\",\"name\":\"cpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeMemory\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"gpuCount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"nodeStorage\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"gpuType\",\"type\":\"string\"}],\"internalType\":\"structQuota\",\"name\":\"quota\",\"type\":\"tuple\"},{\"internalType\":\"uint256\",\"name\":\"pricePerToken\",\"type\":\"uint256\"},{\"internalType\":\"bool\",\"name\":\"occupied\",\"type\":\"bool\"},{\"internalType\":\"string[]\",\"name\":\"models\",\"type\":\"string[]\"},{\"internalType\":\"address\",\"name\":\"teeSignerAddress\",\"type\":\"address\"},{\"internalType\":\"bool\",\"name\":\"teeSignerAcknowledged\",\"type\":\"bool\"}],\"internalType\":\"structService\",\"name\":\"service\",\"type\":\"tuple\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"_ledgerAddress\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"_penaltyPercentage\",\"type\":\"uint256\"}],\"name\":\"initialize\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"initialized\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"ledgerAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"lockTime\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"penaltyPercentage\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"processRefund\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"totalAmount\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"balance\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"pendingRefund\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"removeService\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"requestRefundAll\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"provider\",\"type\":\"address\"}],\"name\":\"revokeTEESignerAcknowledgement\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"string\",\"name\":\"id\",\"type\":\"string\"},{\"internalType\":\"bytes\",\"name\":\"encryptedSecret\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"modelRootHash\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"taskFee\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"user\",\"type\":\"address\"}],\"internalType\":\"structVerifierInput\",\"name\":\"verifierInput\",\"type\":\"tuple\"}],\"name\":\"settleFees\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"bytes4\",\"name\":\"interfaceId\",\"type\":\"bytes4\"}],\"name\":\"supportsInterface\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_locktime\",\"type\":\"uint256\"}],\"name\":\"updateLockTime\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"_penaltyPercentage\",\"type\":\"uint256\"}],\"name\":\"updatePenaltyPercentage\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"stateMutability\":\"payable\",\"type\":\"receive\"}]",
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

// MAXLOCKTIME is a free data retrieval call binding the contract method 0x3ea527cb.
//
// Solidity: function MAX_LOCKTIME() view returns(uint256)
func (_FineTuningServing *FineTuningServingCaller) MAXLOCKTIME(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "MAX_LOCKTIME")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// MAXLOCKTIME is a free data retrieval call binding the contract method 0x3ea527cb.
//
// Solidity: function MAX_LOCKTIME() view returns(uint256)
func (_FineTuningServing *FineTuningServingSession) MAXLOCKTIME() (*big.Int, error) {
	return _FineTuningServing.Contract.MAXLOCKTIME(&_FineTuningServing.CallOpts)
}

// MAXLOCKTIME is a free data retrieval call binding the contract method 0x3ea527cb.
//
// Solidity: function MAX_LOCKTIME() view returns(uint256)
func (_FineTuningServing *FineTuningServingCallerSession) MAXLOCKTIME() (*big.Int, error) {
	return _FineTuningServing.Contract.MAXLOCKTIME(&_FineTuningServing.CallOpts)
}

// MINLOCKTIME is a free data retrieval call binding the contract method 0xad6dca3f.
//
// Solidity: function MIN_LOCKTIME() view returns(uint256)
func (_FineTuningServing *FineTuningServingCaller) MINLOCKTIME(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "MIN_LOCKTIME")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// MINLOCKTIME is a free data retrieval call binding the contract method 0xad6dca3f.
//
// Solidity: function MIN_LOCKTIME() view returns(uint256)
func (_FineTuningServing *FineTuningServingSession) MINLOCKTIME() (*big.Int, error) {
	return _FineTuningServing.Contract.MINLOCKTIME(&_FineTuningServing.CallOpts)
}

// MINLOCKTIME is a free data retrieval call binding the contract method 0xad6dca3f.
//
// Solidity: function MIN_LOCKTIME() view returns(uint256)
func (_FineTuningServing *FineTuningServingCallerSession) MINLOCKTIME() (*big.Int, error) {
	return _FineTuningServing.Contract.MINLOCKTIME(&_FineTuningServing.CallOpts)
}

// MINPROVIDERSTAKE is a free data retrieval call binding the contract method 0x650190e7.
//
// Solidity: function MIN_PROVIDER_STAKE() view returns(uint256)
func (_FineTuningServing *FineTuningServingCaller) MINPROVIDERSTAKE(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "MIN_PROVIDER_STAKE")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// MINPROVIDERSTAKE is a free data retrieval call binding the contract method 0x650190e7.
//
// Solidity: function MIN_PROVIDER_STAKE() view returns(uint256)
func (_FineTuningServing *FineTuningServingSession) MINPROVIDERSTAKE() (*big.Int, error) {
	return _FineTuningServing.Contract.MINPROVIDERSTAKE(&_FineTuningServing.CallOpts)
}

// MINPROVIDERSTAKE is a free data retrieval call binding the contract method 0x650190e7.
//
// Solidity: function MIN_PROVIDER_STAKE() view returns(uint256)
func (_FineTuningServing *FineTuningServingCallerSession) MINPROVIDERSTAKE() (*big.Int, error) {
	return _FineTuningServing.Contract.MINPROVIDERSTAKE(&_FineTuningServing.CallOpts)
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
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,(string,bytes,bytes,bool,uint248,bool)[],uint256,uint256,uint256,bool))
func (_FineTuningServing *FineTuningServingCaller) GetAccount(opts *bind.CallOpts, user common.Address, provider common.Address) (AccountDetails, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getAccount", user, provider)

	if err != nil {
		return *new(AccountDetails), err
	}

	out0 := *abi.ConvertType(out[0], new(AccountDetails)).(*AccountDetails)

	return out0, err

}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,(string,bytes,bytes,bool,uint248,bool)[],uint256,uint256,uint256,bool))
func (_FineTuningServing *FineTuningServingSession) GetAccount(user common.Address, provider common.Address) (AccountDetails, error) {
	return _FineTuningServing.Contract.GetAccount(&_FineTuningServing.CallOpts, user, provider)
}

// GetAccount is a free data retrieval call binding the contract method 0xfd590847.
//
// Solidity: function getAccount(address user, address provider) view returns((address,address,uint256,uint256,uint256,(uint256,uint256,uint256,bool)[],string,(string,bytes,bytes,bool,uint248,bool)[],uint256,uint256,uint256,bool))
func (_FineTuningServing *FineTuningServingCallerSession) GetAccount(user common.Address, provider common.Address) (AccountDetails, error) {
	return _FineTuningServing.Contract.GetAccount(&_FineTuningServing.CallOpts, user, provider)
}

// GetAccountsByProvider is a free data retrieval call binding the contract method 0x1d73b9f5.
//
// Solidity: function getAccountsByProvider(address provider, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingCaller) GetAccountsByProvider(opts *bind.CallOpts, provider common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getAccountsByProvider", provider, offset, limit)

	outstruct := new(struct {
		Accounts []AccountSummary
		Total    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Accounts = *abi.ConvertType(out[0], new([]AccountSummary)).(*[]AccountSummary)
	outstruct.Total = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAccountsByProvider is a free data retrieval call binding the contract method 0x1d73b9f5.
//
// Solidity: function getAccountsByProvider(address provider, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingSession) GetAccountsByProvider(provider common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	return _FineTuningServing.Contract.GetAccountsByProvider(&_FineTuningServing.CallOpts, provider, offset, limit)
}

// GetAccountsByProvider is a free data retrieval call binding the contract method 0x1d73b9f5.
//
// Solidity: function getAccountsByProvider(address provider, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingCallerSession) GetAccountsByProvider(provider common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	return _FineTuningServing.Contract.GetAccountsByProvider(&_FineTuningServing.CallOpts, provider, offset, limit)
}

// GetAccountsByUser is a free data retrieval call binding the contract method 0x4fe63f4d.
//
// Solidity: function getAccountsByUser(address user, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingCaller) GetAccountsByUser(opts *bind.CallOpts, user common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getAccountsByUser", user, offset, limit)

	outstruct := new(struct {
		Accounts []AccountSummary
		Total    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Accounts = *abi.ConvertType(out[0], new([]AccountSummary)).(*[]AccountSummary)
	outstruct.Total = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAccountsByUser is a free data retrieval call binding the contract method 0x4fe63f4d.
//
// Solidity: function getAccountsByUser(address user, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingSession) GetAccountsByUser(user common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	return _FineTuningServing.Contract.GetAccountsByUser(&_FineTuningServing.CallOpts, user, offset, limit)
}

// GetAccountsByUser is a free data retrieval call binding the contract method 0x4fe63f4d.
//
// Solidity: function getAccountsByUser(address user, uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingCallerSession) GetAccountsByUser(user common.Address, offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	return _FineTuningServing.Contract.GetAccountsByUser(&_FineTuningServing.CallOpts, user, offset, limit)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x5bd7ace2.
//
// Solidity: function getAllAccounts(uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingCaller) GetAllAccounts(opts *bind.CallOpts, offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getAllAccounts", offset, limit)

	outstruct := new(struct {
		Accounts []AccountSummary
		Total    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Accounts = *abi.ConvertType(out[0], new([]AccountSummary)).(*[]AccountSummary)
	outstruct.Total = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAllAccounts is a free data retrieval call binding the contract method 0x5bd7ace2.
//
// Solidity: function getAllAccounts(uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingSession) GetAllAccounts(offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	return _FineTuningServing.Contract.GetAllAccounts(&_FineTuningServing.CallOpts, offset, limit)
}

// GetAllAccounts is a free data retrieval call binding the contract method 0x5bd7ace2.
//
// Solidity: function getAllAccounts(uint256 offset, uint256 limit) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts, uint256 total)
func (_FineTuningServing *FineTuningServingCallerSession) GetAllAccounts(offset *big.Int, limit *big.Int) (struct {
	Accounts []AccountSummary
	Total    *big.Int
}, error) {
	return _FineTuningServing.Contract.GetAllAccounts(&_FineTuningServing.CallOpts, offset, limit)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,(uint256,uint256,uint256,uint256,string),uint256,bool,string[],address,bool)[] services)
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
// Solidity: function getAllServices() view returns((address,string,(uint256,uint256,uint256,uint256,string),uint256,bool,string[],address,bool)[] services)
func (_FineTuningServing *FineTuningServingSession) GetAllServices() ([]Service, error) {
	return _FineTuningServing.Contract.GetAllServices(&_FineTuningServing.CallOpts)
}

// GetAllServices is a free data retrieval call binding the contract method 0x21fe0f30.
//
// Solidity: function getAllServices() view returns((address,string,(uint256,uint256,uint256,uint256,string),uint256,bool,string[],address,bool)[] services)
func (_FineTuningServing *FineTuningServingCallerSession) GetAllServices() ([]Service, error) {
	return _FineTuningServing.Contract.GetAllServices(&_FineTuningServing.CallOpts)
}

// GetBatchAccountsByUsers is a free data retrieval call binding the contract method 0xba16a750.
//
// Solidity: function getBatchAccountsByUsers(address[] users) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts)
func (_FineTuningServing *FineTuningServingCaller) GetBatchAccountsByUsers(opts *bind.CallOpts, users []common.Address) ([]AccountSummary, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getBatchAccountsByUsers", users)

	if err != nil {
		return *new([]AccountSummary), err
	}

	out0 := *abi.ConvertType(out[0], new([]AccountSummary)).(*[]AccountSummary)

	return out0, err

}

// GetBatchAccountsByUsers is a free data retrieval call binding the contract method 0xba16a750.
//
// Solidity: function getBatchAccountsByUsers(address[] users) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts)
func (_FineTuningServing *FineTuningServingSession) GetBatchAccountsByUsers(users []common.Address) ([]AccountSummary, error) {
	return _FineTuningServing.Contract.GetBatchAccountsByUsers(&_FineTuningServing.CallOpts, users)
}

// GetBatchAccountsByUsers is a free data retrieval call binding the contract method 0xba16a750.
//
// Solidity: function getBatchAccountsByUsers(address[] users) view returns((address,address,uint256,uint256,uint256,string,uint256,uint256,bool)[] accounts)
func (_FineTuningServing *FineTuningServingCallerSession) GetBatchAccountsByUsers(users []common.Address) ([]AccountSummary, error) {
	return _FineTuningServing.Contract.GetBatchAccountsByUsers(&_FineTuningServing.CallOpts, users)
}

// GetDeliverable is a free data retrieval call binding the contract method 0xa134f9e1.
//
// Solidity: function getDeliverable(address user, address provider, string id) view returns((string,bytes,bytes,bool,uint248,bool))
func (_FineTuningServing *FineTuningServingCaller) GetDeliverable(opts *bind.CallOpts, user common.Address, provider common.Address, id string) (Deliverable, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getDeliverable", user, provider, id)

	if err != nil {
		return *new(Deliverable), err
	}

	out0 := *abi.ConvertType(out[0], new(Deliverable)).(*Deliverable)

	return out0, err

}

// GetDeliverable is a free data retrieval call binding the contract method 0xa134f9e1.
//
// Solidity: function getDeliverable(address user, address provider, string id) view returns((string,bytes,bytes,bool,uint248,bool))
func (_FineTuningServing *FineTuningServingSession) GetDeliverable(user common.Address, provider common.Address, id string) (Deliverable, error) {
	return _FineTuningServing.Contract.GetDeliverable(&_FineTuningServing.CallOpts, user, provider, id)
}

// GetDeliverable is a free data retrieval call binding the contract method 0xa134f9e1.
//
// Solidity: function getDeliverable(address user, address provider, string id) view returns((string,bytes,bytes,bool,uint248,bool))
func (_FineTuningServing *FineTuningServingCallerSession) GetDeliverable(user common.Address, provider common.Address, id string) (Deliverable, error) {
	return _FineTuningServing.Contract.GetDeliverable(&_FineTuningServing.CallOpts, user, provider, id)
}

// GetDeliverables is a free data retrieval call binding the contract method 0x9622e934.
//
// Solidity: function getDeliverables(address user, address provider) view returns((string,bytes,bytes,bool,uint248,bool)[])
func (_FineTuningServing *FineTuningServingCaller) GetDeliverables(opts *bind.CallOpts, user common.Address, provider common.Address) ([]Deliverable, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getDeliverables", user, provider)

	if err != nil {
		return *new([]Deliverable), err
	}

	out0 := *abi.ConvertType(out[0], new([]Deliverable)).(*[]Deliverable)

	return out0, err

}

// GetDeliverables is a free data retrieval call binding the contract method 0x9622e934.
//
// Solidity: function getDeliverables(address user, address provider) view returns((string,bytes,bytes,bool,uint248,bool)[])
func (_FineTuningServing *FineTuningServingSession) GetDeliverables(user common.Address, provider common.Address) ([]Deliverable, error) {
	return _FineTuningServing.Contract.GetDeliverables(&_FineTuningServing.CallOpts, user, provider)
}

// GetDeliverables is a free data retrieval call binding the contract method 0x9622e934.
//
// Solidity: function getDeliverables(address user, address provider) view returns((string,bytes,bytes,bool,uint248,bool)[])
func (_FineTuningServing *FineTuningServingCallerSession) GetDeliverables(user common.Address, provider common.Address) ([]Deliverable, error) {
	return _FineTuningServing.Contract.GetDeliverables(&_FineTuningServing.CallOpts, user, provider)
}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_FineTuningServing *FineTuningServingCaller) GetPendingRefund(opts *bind.CallOpts, user common.Address, provider common.Address) (*big.Int, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getPendingRefund", user, provider)

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_FineTuningServing *FineTuningServingSession) GetPendingRefund(user common.Address, provider common.Address) (*big.Int, error) {
	return _FineTuningServing.Contract.GetPendingRefund(&_FineTuningServing.CallOpts, user, provider)
}

// GetPendingRefund is a free data retrieval call binding the contract method 0x264173d6.
//
// Solidity: function getPendingRefund(address user, address provider) view returns(uint256)
func (_FineTuningServing *FineTuningServingCallerSession) GetPendingRefund(user common.Address, provider common.Address) (*big.Int, error) {
	return _FineTuningServing.Contract.GetPendingRefund(&_FineTuningServing.CallOpts, user, provider)
}

// GetService is a free data retrieval call binding the contract method 0x15a52302.
//
// Solidity: function getService(address provider) view returns((address,string,(uint256,uint256,uint256,uint256,string),uint256,bool,string[],address,bool) service)
func (_FineTuningServing *FineTuningServingCaller) GetService(opts *bind.CallOpts, provider common.Address) (Service, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "getService", provider)

	if err != nil {
		return *new(Service), err
	}

	out0 := *abi.ConvertType(out[0], new(Service)).(*Service)

	return out0, err

}

// GetService is a free data retrieval call binding the contract method 0x15a52302.
//
// Solidity: function getService(address provider) view returns((address,string,(uint256,uint256,uint256,uint256,string),uint256,bool,string[],address,bool) service)
func (_FineTuningServing *FineTuningServingSession) GetService(provider common.Address) (Service, error) {
	return _FineTuningServing.Contract.GetService(&_FineTuningServing.CallOpts, provider)
}

// GetService is a free data retrieval call binding the contract method 0x15a52302.
//
// Solidity: function getService(address provider) view returns((address,string,(uint256,uint256,uint256,uint256,string),uint256,bool,string[],address,bool) service)
func (_FineTuningServing *FineTuningServingCallerSession) GetService(provider common.Address) (Service, error) {
	return _FineTuningServing.Contract.GetService(&_FineTuningServing.CallOpts, provider)
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

// PenaltyPercentage is a free data retrieval call binding the contract method 0x15908d51.
//
// Solidity: function penaltyPercentage() view returns(uint256)
func (_FineTuningServing *FineTuningServingCaller) PenaltyPercentage(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "penaltyPercentage")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// PenaltyPercentage is a free data retrieval call binding the contract method 0x15908d51.
//
// Solidity: function penaltyPercentage() view returns(uint256)
func (_FineTuningServing *FineTuningServingSession) PenaltyPercentage() (*big.Int, error) {
	return _FineTuningServing.Contract.PenaltyPercentage(&_FineTuningServing.CallOpts)
}

// PenaltyPercentage is a free data retrieval call binding the contract method 0x15908d51.
//
// Solidity: function penaltyPercentage() view returns(uint256)
func (_FineTuningServing *FineTuningServingCallerSession) PenaltyPercentage() (*big.Int, error) {
	return _FineTuningServing.Contract.PenaltyPercentage(&_FineTuningServing.CallOpts)
}

// SupportsInterface is a free data retrieval call binding the contract method 0x01ffc9a7.
//
// Solidity: function supportsInterface(bytes4 interfaceId) view returns(bool)
func (_FineTuningServing *FineTuningServingCaller) SupportsInterface(opts *bind.CallOpts, interfaceId [4]byte) (bool, error) {
	var out []interface{}
	err := _FineTuningServing.contract.Call(opts, &out, "supportsInterface", interfaceId)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// SupportsInterface is a free data retrieval call binding the contract method 0x01ffc9a7.
//
// Solidity: function supportsInterface(bytes4 interfaceId) view returns(bool)
func (_FineTuningServing *FineTuningServingSession) SupportsInterface(interfaceId [4]byte) (bool, error) {
	return _FineTuningServing.Contract.SupportsInterface(&_FineTuningServing.CallOpts, interfaceId)
}

// SupportsInterface is a free data retrieval call binding the contract method 0x01ffc9a7.
//
// Solidity: function supportsInterface(bytes4 interfaceId) view returns(bool)
func (_FineTuningServing *FineTuningServingCallerSession) SupportsInterface(interfaceId [4]byte) (bool, error) {
	return _FineTuningServing.Contract.SupportsInterface(&_FineTuningServing.CallOpts, interfaceId)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0xa296bf4f.
//
// Solidity: function acknowledgeDeliverable(address provider, string id) returns()
func (_FineTuningServing *FineTuningServingTransactor) AcknowledgeDeliverable(opts *bind.TransactOpts, provider common.Address, id string) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "acknowledgeDeliverable", provider, id)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0xa296bf4f.
//
// Solidity: function acknowledgeDeliverable(address provider, string id) returns()
func (_FineTuningServing *FineTuningServingSession) AcknowledgeDeliverable(provider common.Address, id string) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeDeliverable(&_FineTuningServing.TransactOpts, provider, id)
}

// AcknowledgeDeliverable is a paid mutator transaction binding the contract method 0xa296bf4f.
//
// Solidity: function acknowledgeDeliverable(address provider, string id) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AcknowledgeDeliverable(provider common.Address, id string) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeDeliverable(&_FineTuningServing.TransactOpts, provider, id)
}

// AcknowledgeTEESigner is a paid mutator transaction binding the contract method 0x7ff6fc1c.
//
// Solidity: function acknowledgeTEESigner(address provider, bool acknowledged) returns()
func (_FineTuningServing *FineTuningServingTransactor) AcknowledgeTEESigner(opts *bind.TransactOpts, provider common.Address, acknowledged bool) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "acknowledgeTEESigner", provider, acknowledged)
}

// AcknowledgeTEESigner is a paid mutator transaction binding the contract method 0x7ff6fc1c.
//
// Solidity: function acknowledgeTEESigner(address provider, bool acknowledged) returns()
func (_FineTuningServing *FineTuningServingSession) AcknowledgeTEESigner(provider common.Address, acknowledged bool) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeTEESigner(&_FineTuningServing.TransactOpts, provider, acknowledged)
}

// AcknowledgeTEESigner is a paid mutator transaction binding the contract method 0x7ff6fc1c.
//
// Solidity: function acknowledgeTEESigner(address provider, bool acknowledged) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AcknowledgeTEESigner(provider common.Address, acknowledged bool) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeTEESigner(&_FineTuningServing.TransactOpts, provider, acknowledged)
}

// AcknowledgeTEESignerByOwner is a paid mutator transaction binding the contract method 0xb2394d09.
//
// Solidity: function acknowledgeTEESignerByOwner(address provider) returns()
func (_FineTuningServing *FineTuningServingTransactor) AcknowledgeTEESignerByOwner(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "acknowledgeTEESignerByOwner", provider)
}

// AcknowledgeTEESignerByOwner is a paid mutator transaction binding the contract method 0xb2394d09.
//
// Solidity: function acknowledgeTEESignerByOwner(address provider) returns()
func (_FineTuningServing *FineTuningServingSession) AcknowledgeTEESignerByOwner(provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeTEESignerByOwner(&_FineTuningServing.TransactOpts, provider)
}

// AcknowledgeTEESignerByOwner is a paid mutator transaction binding the contract method 0xb2394d09.
//
// Solidity: function acknowledgeTEESignerByOwner(address provider) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AcknowledgeTEESignerByOwner(provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AcknowledgeTEESignerByOwner(&_FineTuningServing.TransactOpts, provider)
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

// AddDeliverable is a paid mutator transaction binding the contract method 0x6dc7513d.
//
// Solidity: function addDeliverable(address user, string id, bytes modelRootHash) returns()
func (_FineTuningServing *FineTuningServingTransactor) AddDeliverable(opts *bind.TransactOpts, user common.Address, id string, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "addDeliverable", user, id, modelRootHash)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0x6dc7513d.
//
// Solidity: function addDeliverable(address user, string id, bytes modelRootHash) returns()
func (_FineTuningServing *FineTuningServingSession) AddDeliverable(user common.Address, id string, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddDeliverable(&_FineTuningServing.TransactOpts, user, id, modelRootHash)
}

// AddDeliverable is a paid mutator transaction binding the contract method 0x6dc7513d.
//
// Solidity: function addDeliverable(address user, string id, bytes modelRootHash) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AddDeliverable(user common.Address, id string, modelRootHash []byte) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddDeliverable(&_FineTuningServing.TransactOpts, user, id, modelRootHash)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x43d96bb3.
//
// Solidity: function addOrUpdateService(string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, bool occupied, string[] models, address teeSignerAddress) payable returns()
func (_FineTuningServing *FineTuningServingTransactor) AddOrUpdateService(opts *bind.TransactOpts, url string, quota Quota, pricePerToken *big.Int, occupied bool, models []string, teeSignerAddress common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "addOrUpdateService", url, quota, pricePerToken, occupied, models, teeSignerAddress)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x43d96bb3.
//
// Solidity: function addOrUpdateService(string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, bool occupied, string[] models, address teeSignerAddress) payable returns()
func (_FineTuningServing *FineTuningServingSession) AddOrUpdateService(url string, quota Quota, pricePerToken *big.Int, occupied bool, models []string, teeSignerAddress common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddOrUpdateService(&_FineTuningServing.TransactOpts, url, quota, pricePerToken, occupied, models, teeSignerAddress)
}

// AddOrUpdateService is a paid mutator transaction binding the contract method 0x43d96bb3.
//
// Solidity: function addOrUpdateService(string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, bool occupied, string[] models, address teeSignerAddress) payable returns()
func (_FineTuningServing *FineTuningServingTransactorSession) AddOrUpdateService(url string, quota Quota, pricePerToken *big.Int, occupied bool, models []string, teeSignerAddress common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.AddOrUpdateService(&_FineTuningServing.TransactOpts, url, quota, pricePerToken, occupied, models, teeSignerAddress)
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

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_FineTuningServing *FineTuningServingTransactor) DepositFund(opts *bind.TransactOpts, user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "depositFund", user, provider, cancelRetrievingAmount)
}

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_FineTuningServing *FineTuningServingSession) DepositFund(user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.DepositFund(&_FineTuningServing.TransactOpts, user, provider, cancelRetrievingAmount)
}

// DepositFund is a paid mutator transaction binding the contract method 0x745e87f7.
//
// Solidity: function depositFund(address user, address provider, uint256 cancelRetrievingAmount) payable returns()
func (_FineTuningServing *FineTuningServingTransactorSession) DepositFund(user common.Address, provider common.Address, cancelRetrievingAmount *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.DepositFund(&_FineTuningServing.TransactOpts, user, provider, cancelRetrievingAmount)
}

// Initialize is a paid mutator transaction binding the contract method 0xe37259e9.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner, uint256 _penaltyPercentage) returns()
func (_FineTuningServing *FineTuningServingTransactor) Initialize(opts *bind.TransactOpts, _locktime *big.Int, _ledgerAddress common.Address, owner common.Address, _penaltyPercentage *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "initialize", _locktime, _ledgerAddress, owner, _penaltyPercentage)
}

// Initialize is a paid mutator transaction binding the contract method 0xe37259e9.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner, uint256 _penaltyPercentage) returns()
func (_FineTuningServing *FineTuningServingSession) Initialize(_locktime *big.Int, _ledgerAddress common.Address, owner common.Address, _penaltyPercentage *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.Initialize(&_FineTuningServing.TransactOpts, _locktime, _ledgerAddress, owner, _penaltyPercentage)
}

// Initialize is a paid mutator transaction binding the contract method 0xe37259e9.
//
// Solidity: function initialize(uint256 _locktime, address _ledgerAddress, address owner, uint256 _penaltyPercentage) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) Initialize(_locktime *big.Int, _ledgerAddress common.Address, owner common.Address, _penaltyPercentage *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.Initialize(&_FineTuningServing.TransactOpts, _locktime, _ledgerAddress, owner, _penaltyPercentage)
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

// RemoveService is a paid mutator transaction binding the contract method 0xbbee42d9.
//
// Solidity: function removeService() returns()
func (_FineTuningServing *FineTuningServingTransactor) RemoveService(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "removeService")
}

// RemoveService is a paid mutator transaction binding the contract method 0xbbee42d9.
//
// Solidity: function removeService() returns()
func (_FineTuningServing *FineTuningServingSession) RemoveService() (*types.Transaction, error) {
	return _FineTuningServing.Contract.RemoveService(&_FineTuningServing.TransactOpts)
}

// RemoveService is a paid mutator transaction binding the contract method 0xbbee42d9.
//
// Solidity: function removeService() returns()
func (_FineTuningServing *FineTuningServingTransactorSession) RemoveService() (*types.Transaction, error) {
	return _FineTuningServing.Contract.RemoveService(&_FineTuningServing.TransactOpts)
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

// RevokeTEESignerAcknowledgement is a paid mutator transaction binding the contract method 0xddf96abd.
//
// Solidity: function revokeTEESignerAcknowledgement(address provider) returns()
func (_FineTuningServing *FineTuningServingTransactor) RevokeTEESignerAcknowledgement(opts *bind.TransactOpts, provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "revokeTEESignerAcknowledgement", provider)
}

// RevokeTEESignerAcknowledgement is a paid mutator transaction binding the contract method 0xddf96abd.
//
// Solidity: function revokeTEESignerAcknowledgement(address provider) returns()
func (_FineTuningServing *FineTuningServingSession) RevokeTEESignerAcknowledgement(provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.RevokeTEESignerAcknowledgement(&_FineTuningServing.TransactOpts, provider)
}

// RevokeTEESignerAcknowledgement is a paid mutator transaction binding the contract method 0xddf96abd.
//
// Solidity: function revokeTEESignerAcknowledgement(address provider) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) RevokeTEESignerAcknowledgement(provider common.Address) (*types.Transaction, error) {
	return _FineTuningServing.Contract.RevokeTEESignerAcknowledgement(&_FineTuningServing.TransactOpts, provider)
}

// SettleFees is a paid mutator transaction binding the contract method 0x3d60456a.
//
// Solidity: function settleFees((string,bytes,bytes,uint256,bytes,uint256,address) verifierInput) returns()
func (_FineTuningServing *FineTuningServingTransactor) SettleFees(opts *bind.TransactOpts, verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "settleFees", verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x3d60456a.
//
// Solidity: function settleFees((string,bytes,bytes,uint256,bytes,uint256,address) verifierInput) returns()
func (_FineTuningServing *FineTuningServingSession) SettleFees(verifierInput VerifierInput) (*types.Transaction, error) {
	return _FineTuningServing.Contract.SettleFees(&_FineTuningServing.TransactOpts, verifierInput)
}

// SettleFees is a paid mutator transaction binding the contract method 0x3d60456a.
//
// Solidity: function settleFees((string,bytes,bytes,uint256,bytes,uint256,address) verifierInput) returns()
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

// UpdatePenaltyPercentage is a paid mutator transaction binding the contract method 0xeb961693.
//
// Solidity: function updatePenaltyPercentage(uint256 _penaltyPercentage) returns()
func (_FineTuningServing *FineTuningServingTransactor) UpdatePenaltyPercentage(opts *bind.TransactOpts, _penaltyPercentage *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.contract.Transact(opts, "updatePenaltyPercentage", _penaltyPercentage)
}

// UpdatePenaltyPercentage is a paid mutator transaction binding the contract method 0xeb961693.
//
// Solidity: function updatePenaltyPercentage(uint256 _penaltyPercentage) returns()
func (_FineTuningServing *FineTuningServingSession) UpdatePenaltyPercentage(_penaltyPercentage *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.UpdatePenaltyPercentage(&_FineTuningServing.TransactOpts, _penaltyPercentage)
}

// UpdatePenaltyPercentage is a paid mutator transaction binding the contract method 0xeb961693.
//
// Solidity: function updatePenaltyPercentage(uint256 _penaltyPercentage) returns()
func (_FineTuningServing *FineTuningServingTransactorSession) UpdatePenaltyPercentage(_penaltyPercentage *big.Int) (*types.Transaction, error) {
	return _FineTuningServing.Contract.UpdatePenaltyPercentage(&_FineTuningServing.TransactOpts, _penaltyPercentage)
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_FineTuningServing *FineTuningServingTransactor) Receive(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _FineTuningServing.contract.RawTransact(opts, nil) // calldata is disallowed for receive function
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_FineTuningServing *FineTuningServingSession) Receive() (*types.Transaction, error) {
	return _FineTuningServing.Contract.Receive(&_FineTuningServing.TransactOpts)
}

// Receive is a paid mutator transaction binding the contract receive function.
//
// Solidity: receive() payable returns()
func (_FineTuningServing *FineTuningServingTransactorSession) Receive() (*types.Transaction, error) {
	return _FineTuningServing.Contract.Receive(&_FineTuningServing.TransactOpts)
}

// FineTuningServingAccountDeletedIterator is returned from FilterAccountDeleted and is used to iterate over the raw logs and unpacked data for AccountDeleted events raised by the FineTuningServing contract.
type FineTuningServingAccountDeletedIterator struct {
	Event *FineTuningServingAccountDeleted // Event containing the contract specifics and raw log

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
func (it *FineTuningServingAccountDeletedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingAccountDeleted)
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
		it.Event = new(FineTuningServingAccountDeleted)
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
func (it *FineTuningServingAccountDeletedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingAccountDeletedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingAccountDeleted represents a AccountDeleted event raised by the FineTuningServing contract.
type FineTuningServingAccountDeleted struct {
	User           common.Address
	Provider       common.Address
	RefundedAmount *big.Int
	Raw            types.Log // Blockchain specific contextual infos
}

// FilterAccountDeleted is a free log retrieval operation binding the contract event 0x342d961f860d5b1c27877a790eff2b213c020d1955a4903d6a9bf3ed590b7cd7.
//
// Solidity: event AccountDeleted(address indexed user, address indexed provider, uint256 refundedAmount)
func (_FineTuningServing *FineTuningServingFilterer) FilterAccountDeleted(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*FineTuningServingAccountDeletedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "AccountDeleted", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingAccountDeletedIterator{contract: _FineTuningServing.contract, event: "AccountDeleted", logs: logs, sub: sub}, nil
}

// WatchAccountDeleted is a free log subscription operation binding the contract event 0x342d961f860d5b1c27877a790eff2b213c020d1955a4903d6a9bf3ed590b7cd7.
//
// Solidity: event AccountDeleted(address indexed user, address indexed provider, uint256 refundedAmount)
func (_FineTuningServing *FineTuningServingFilterer) WatchAccountDeleted(opts *bind.WatchOpts, sink chan<- *FineTuningServingAccountDeleted, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "AccountDeleted", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingAccountDeleted)
				if err := _FineTuningServing.contract.UnpackLog(event, "AccountDeleted", log); err != nil {
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

// ParseAccountDeleted is a log parse operation binding the contract event 0x342d961f860d5b1c27877a790eff2b213c020d1955a4903d6a9bf3ed590b7cd7.
//
// Solidity: event AccountDeleted(address indexed user, address indexed provider, uint256 refundedAmount)
func (_FineTuningServing *FineTuningServingFilterer) ParseAccountDeleted(log types.Log) (*FineTuningServingAccountDeleted, error) {
	event := new(FineTuningServingAccountDeleted)
	if err := _FineTuningServing.contract.UnpackLog(event, "AccountDeleted", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
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

// FineTuningServingDeliverableAcknowledgedIterator is returned from FilterDeliverableAcknowledged and is used to iterate over the raw logs and unpacked data for DeliverableAcknowledged events raised by the FineTuningServing contract.
type FineTuningServingDeliverableAcknowledgedIterator struct {
	Event *FineTuningServingDeliverableAcknowledged // Event containing the contract specifics and raw log

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
func (it *FineTuningServingDeliverableAcknowledgedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingDeliverableAcknowledged)
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
		it.Event = new(FineTuningServingDeliverableAcknowledged)
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
func (it *FineTuningServingDeliverableAcknowledgedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingDeliverableAcknowledgedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingDeliverableAcknowledged represents a DeliverableAcknowledged event raised by the FineTuningServing contract.
type FineTuningServingDeliverableAcknowledged struct {
	User          common.Address
	Provider      common.Address
	DeliverableId string
	Timestamp     *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterDeliverableAcknowledged is a free log retrieval operation binding the contract event 0x0e09c5eac33273dd5442d3969e1ffe7c8fd49636d6e1a9c1a2ad6fe53933a949.
//
// Solidity: event DeliverableAcknowledged(address indexed user, address indexed provider, string deliverableId, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) FilterDeliverableAcknowledged(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*FineTuningServingDeliverableAcknowledgedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "DeliverableAcknowledged", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingDeliverableAcknowledgedIterator{contract: _FineTuningServing.contract, event: "DeliverableAcknowledged", logs: logs, sub: sub}, nil
}

// WatchDeliverableAcknowledged is a free log subscription operation binding the contract event 0x0e09c5eac33273dd5442d3969e1ffe7c8fd49636d6e1a9c1a2ad6fe53933a949.
//
// Solidity: event DeliverableAcknowledged(address indexed user, address indexed provider, string deliverableId, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) WatchDeliverableAcknowledged(opts *bind.WatchOpts, sink chan<- *FineTuningServingDeliverableAcknowledged, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "DeliverableAcknowledged", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingDeliverableAcknowledged)
				if err := _FineTuningServing.contract.UnpackLog(event, "DeliverableAcknowledged", log); err != nil {
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

// ParseDeliverableAcknowledged is a log parse operation binding the contract event 0x0e09c5eac33273dd5442d3969e1ffe7c8fd49636d6e1a9c1a2ad6fe53933a949.
//
// Solidity: event DeliverableAcknowledged(address indexed user, address indexed provider, string deliverableId, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) ParseDeliverableAcknowledged(log types.Log) (*FineTuningServingDeliverableAcknowledged, error) {
	event := new(FineTuningServingDeliverableAcknowledged)
	if err := _FineTuningServing.contract.UnpackLog(event, "DeliverableAcknowledged", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingDeliverableAddedIterator is returned from FilterDeliverableAdded and is used to iterate over the raw logs and unpacked data for DeliverableAdded events raised by the FineTuningServing contract.
type FineTuningServingDeliverableAddedIterator struct {
	Event *FineTuningServingDeliverableAdded // Event containing the contract specifics and raw log

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
func (it *FineTuningServingDeliverableAddedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingDeliverableAdded)
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
		it.Event = new(FineTuningServingDeliverableAdded)
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
func (it *FineTuningServingDeliverableAddedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingDeliverableAddedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingDeliverableAdded represents a DeliverableAdded event raised by the FineTuningServing contract.
type FineTuningServingDeliverableAdded struct {
	User          common.Address
	Provider      common.Address
	DeliverableId string
	ModelRootHash []byte
	Timestamp     *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterDeliverableAdded is a free log retrieval operation binding the contract event 0x8271eb00644a0e6352667cb763a339f72506be16fb91fd41e76b852c8951d93a.
//
// Solidity: event DeliverableAdded(address indexed user, address indexed provider, string deliverableId, bytes modelRootHash, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) FilterDeliverableAdded(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*FineTuningServingDeliverableAddedIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "DeliverableAdded", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingDeliverableAddedIterator{contract: _FineTuningServing.contract, event: "DeliverableAdded", logs: logs, sub: sub}, nil
}

// WatchDeliverableAdded is a free log subscription operation binding the contract event 0x8271eb00644a0e6352667cb763a339f72506be16fb91fd41e76b852c8951d93a.
//
// Solidity: event DeliverableAdded(address indexed user, address indexed provider, string deliverableId, bytes modelRootHash, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) WatchDeliverableAdded(opts *bind.WatchOpts, sink chan<- *FineTuningServingDeliverableAdded, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "DeliverableAdded", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingDeliverableAdded)
				if err := _FineTuningServing.contract.UnpackLog(event, "DeliverableAdded", log); err != nil {
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

// ParseDeliverableAdded is a log parse operation binding the contract event 0x8271eb00644a0e6352667cb763a339f72506be16fb91fd41e76b852c8951d93a.
//
// Solidity: event DeliverableAdded(address indexed user, address indexed provider, string deliverableId, bytes modelRootHash, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) ParseDeliverableAdded(log types.Log) (*FineTuningServingDeliverableAdded, error) {
	event := new(FineTuningServingDeliverableAdded)
	if err := _FineTuningServing.contract.UnpackLog(event, "DeliverableAdded", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingDeliverableEvictedIterator is returned from FilterDeliverableEvicted and is used to iterate over the raw logs and unpacked data for DeliverableEvicted events raised by the FineTuningServing contract.
type FineTuningServingDeliverableEvictedIterator struct {
	Event *FineTuningServingDeliverableEvicted // Event containing the contract specifics and raw log

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
func (it *FineTuningServingDeliverableEvictedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingDeliverableEvicted)
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
		it.Event = new(FineTuningServingDeliverableEvicted)
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
func (it *FineTuningServingDeliverableEvictedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingDeliverableEvictedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingDeliverableEvicted represents a DeliverableEvicted event raised by the FineTuningServing contract.
type FineTuningServingDeliverableEvicted struct {
	Provider             common.Address
	User                 common.Address
	EvictedDeliverableId string
	NewDeliverableId     string
	Timestamp            *big.Int
	Raw                  types.Log // Blockchain specific contextual infos
}

// FilterDeliverableEvicted is a free log retrieval operation binding the contract event 0x0f101bde80f9fd46f64cedcd834d063011c90a84007611bc3103214f6ba3ce6b.
//
// Solidity: event DeliverableEvicted(address indexed provider, address indexed user, string evictedDeliverableId, string newDeliverableId, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) FilterDeliverableEvicted(opts *bind.FilterOpts, provider []common.Address, user []common.Address) (*FineTuningServingDeliverableEvictedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "DeliverableEvicted", providerRule, userRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingDeliverableEvictedIterator{contract: _FineTuningServing.contract, event: "DeliverableEvicted", logs: logs, sub: sub}, nil
}

// WatchDeliverableEvicted is a free log subscription operation binding the contract event 0x0f101bde80f9fd46f64cedcd834d063011c90a84007611bc3103214f6ba3ce6b.
//
// Solidity: event DeliverableEvicted(address indexed provider, address indexed user, string evictedDeliverableId, string newDeliverableId, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) WatchDeliverableEvicted(opts *bind.WatchOpts, sink chan<- *FineTuningServingDeliverableEvicted, provider []common.Address, user []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "DeliverableEvicted", providerRule, userRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingDeliverableEvicted)
				if err := _FineTuningServing.contract.UnpackLog(event, "DeliverableEvicted", log); err != nil {
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

// ParseDeliverableEvicted is a log parse operation binding the contract event 0x0f101bde80f9fd46f64cedcd834d063011c90a84007611bc3103214f6ba3ce6b.
//
// Solidity: event DeliverableEvicted(address indexed provider, address indexed user, string evictedDeliverableId, string newDeliverableId, uint256 timestamp)
func (_FineTuningServing *FineTuningServingFilterer) ParseDeliverableEvicted(log types.Log) (*FineTuningServingDeliverableEvicted, error) {
	event := new(FineTuningServingDeliverableEvicted)
	if err := _FineTuningServing.contract.UnpackLog(event, "DeliverableEvicted", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingFeesSettledIterator is returned from FilterFeesSettled and is used to iterate over the raw logs and unpacked data for FeesSettled events raised by the FineTuningServing contract.
type FineTuningServingFeesSettledIterator struct {
	Event *FineTuningServingFeesSettled // Event containing the contract specifics and raw log

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
func (it *FineTuningServingFeesSettledIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingFeesSettled)
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
		it.Event = new(FineTuningServingFeesSettled)
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
func (it *FineTuningServingFeesSettledIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingFeesSettledIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingFeesSettled represents a FeesSettled event raised by the FineTuningServing contract.
type FineTuningServingFeesSettled struct {
	User          common.Address
	Provider      common.Address
	DeliverableId string
	Fee           *big.Int
	Acknowledged  bool
	Nonce         *big.Int
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterFeesSettled is a free log retrieval operation binding the contract event 0x395fa6798087d6b0e2073c2397d19fcdada968eff962d887479501fe7e22f3c1.
//
// Solidity: event FeesSettled(address indexed user, address indexed provider, string deliverableId, uint256 fee, bool acknowledged, uint256 nonce)
func (_FineTuningServing *FineTuningServingFilterer) FilterFeesSettled(opts *bind.FilterOpts, user []common.Address, provider []common.Address) (*FineTuningServingFeesSettledIterator, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "FeesSettled", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingFeesSettledIterator{contract: _FineTuningServing.contract, event: "FeesSettled", logs: logs, sub: sub}, nil
}

// WatchFeesSettled is a free log subscription operation binding the contract event 0x395fa6798087d6b0e2073c2397d19fcdada968eff962d887479501fe7e22f3c1.
//
// Solidity: event FeesSettled(address indexed user, address indexed provider, string deliverableId, uint256 fee, bool acknowledged, uint256 nonce)
func (_FineTuningServing *FineTuningServingFilterer) WatchFeesSettled(opts *bind.WatchOpts, sink chan<- *FineTuningServingFeesSettled, user []common.Address, provider []common.Address) (event.Subscription, error) {

	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "FeesSettled", userRule, providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingFeesSettled)
				if err := _FineTuningServing.contract.UnpackLog(event, "FeesSettled", log); err != nil {
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

// ParseFeesSettled is a log parse operation binding the contract event 0x395fa6798087d6b0e2073c2397d19fcdada968eff962d887479501fe7e22f3c1.
//
// Solidity: event FeesSettled(address indexed user, address indexed provider, string deliverableId, uint256 fee, bool acknowledged, uint256 nonce)
func (_FineTuningServing *FineTuningServingFilterer) ParseFeesSettled(log types.Log) (*FineTuningServingFeesSettled, error) {
	event := new(FineTuningServingFeesSettled)
	if err := _FineTuningServing.contract.UnpackLog(event, "FeesSettled", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingInitializedIterator is returned from FilterInitialized and is used to iterate over the raw logs and unpacked data for Initialized events raised by the FineTuningServing contract.
type FineTuningServingInitializedIterator struct {
	Event *FineTuningServingInitialized // Event containing the contract specifics and raw log

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
func (it *FineTuningServingInitializedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingInitialized)
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
		it.Event = new(FineTuningServingInitialized)
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
func (it *FineTuningServingInitializedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingInitializedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingInitialized represents a Initialized event raised by the FineTuningServing contract.
type FineTuningServingInitialized struct {
	Version uint8
	Raw     types.Log // Blockchain specific contextual infos
}

// FilterInitialized is a free log retrieval operation binding the contract event 0x7f26b83ff96e1f2b6a682f133852f6798a09c465da95921460cefb3847402498.
//
// Solidity: event Initialized(uint8 version)
func (_FineTuningServing *FineTuningServingFilterer) FilterInitialized(opts *bind.FilterOpts) (*FineTuningServingInitializedIterator, error) {

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "Initialized")
	if err != nil {
		return nil, err
	}
	return &FineTuningServingInitializedIterator{contract: _FineTuningServing.contract, event: "Initialized", logs: logs, sub: sub}, nil
}

// WatchInitialized is a free log subscription operation binding the contract event 0x7f26b83ff96e1f2b6a682f133852f6798a09c465da95921460cefb3847402498.
//
// Solidity: event Initialized(uint8 version)
func (_FineTuningServing *FineTuningServingFilterer) WatchInitialized(opts *bind.WatchOpts, sink chan<- *FineTuningServingInitialized) (event.Subscription, error) {

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "Initialized")
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingInitialized)
				if err := _FineTuningServing.contract.UnpackLog(event, "Initialized", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseInitialized(log types.Log) (*FineTuningServingInitialized, error) {
	event := new(FineTuningServingInitialized)
	if err := _FineTuningServing.contract.UnpackLog(event, "Initialized", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingLockTimeUpdatedIterator is returned from FilterLockTimeUpdated and is used to iterate over the raw logs and unpacked data for LockTimeUpdated events raised by the FineTuningServing contract.
type FineTuningServingLockTimeUpdatedIterator struct {
	Event *FineTuningServingLockTimeUpdated // Event containing the contract specifics and raw log

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
func (it *FineTuningServingLockTimeUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingLockTimeUpdated)
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
		it.Event = new(FineTuningServingLockTimeUpdated)
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
func (it *FineTuningServingLockTimeUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingLockTimeUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingLockTimeUpdated represents a LockTimeUpdated event raised by the FineTuningServing contract.
type FineTuningServingLockTimeUpdated struct {
	OldLockTime *big.Int
	NewLockTime *big.Int
	Raw         types.Log // Blockchain specific contextual infos
}

// FilterLockTimeUpdated is a free log retrieval operation binding the contract event 0x5707a70527b6cbb892bfe5d8739a8f0643d3212d9b1139bc31c742e731c65270.
//
// Solidity: event LockTimeUpdated(uint256 oldLockTime, uint256 newLockTime)
func (_FineTuningServing *FineTuningServingFilterer) FilterLockTimeUpdated(opts *bind.FilterOpts) (*FineTuningServingLockTimeUpdatedIterator, error) {

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "LockTimeUpdated")
	if err != nil {
		return nil, err
	}
	return &FineTuningServingLockTimeUpdatedIterator{contract: _FineTuningServing.contract, event: "LockTimeUpdated", logs: logs, sub: sub}, nil
}

// WatchLockTimeUpdated is a free log subscription operation binding the contract event 0x5707a70527b6cbb892bfe5d8739a8f0643d3212d9b1139bc31c742e731c65270.
//
// Solidity: event LockTimeUpdated(uint256 oldLockTime, uint256 newLockTime)
func (_FineTuningServing *FineTuningServingFilterer) WatchLockTimeUpdated(opts *bind.WatchOpts, sink chan<- *FineTuningServingLockTimeUpdated) (event.Subscription, error) {

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "LockTimeUpdated")
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingLockTimeUpdated)
				if err := _FineTuningServing.contract.UnpackLog(event, "LockTimeUpdated", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseLockTimeUpdated(log types.Log) (*FineTuningServingLockTimeUpdated, error) {
	event := new(FineTuningServingLockTimeUpdated)
	if err := _FineTuningServing.contract.UnpackLog(event, "LockTimeUpdated", log); err != nil {
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

// FineTuningServingProviderStakeReturnedIterator is returned from FilterProviderStakeReturned and is used to iterate over the raw logs and unpacked data for ProviderStakeReturned events raised by the FineTuningServing contract.
type FineTuningServingProviderStakeReturnedIterator struct {
	Event *FineTuningServingProviderStakeReturned // Event containing the contract specifics and raw log

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
func (it *FineTuningServingProviderStakeReturnedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingProviderStakeReturned)
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
		it.Event = new(FineTuningServingProviderStakeReturned)
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
func (it *FineTuningServingProviderStakeReturnedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingProviderStakeReturnedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingProviderStakeReturned represents a ProviderStakeReturned event raised by the FineTuningServing contract.
type FineTuningServingProviderStakeReturned struct {
	Provider common.Address
	Amount   *big.Int
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterProviderStakeReturned is a free log retrieval operation binding the contract event 0x17f7db034d4b59fadec3e44a684cb4396ca10fd036c4e4f718bf06e993715882.
//
// Solidity: event ProviderStakeReturned(address indexed provider, uint256 amount)
func (_FineTuningServing *FineTuningServingFilterer) FilterProviderStakeReturned(opts *bind.FilterOpts, provider []common.Address) (*FineTuningServingProviderStakeReturnedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "ProviderStakeReturned", providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingProviderStakeReturnedIterator{contract: _FineTuningServing.contract, event: "ProviderStakeReturned", logs: logs, sub: sub}, nil
}

// WatchProviderStakeReturned is a free log subscription operation binding the contract event 0x17f7db034d4b59fadec3e44a684cb4396ca10fd036c4e4f718bf06e993715882.
//
// Solidity: event ProviderStakeReturned(address indexed provider, uint256 amount)
func (_FineTuningServing *FineTuningServingFilterer) WatchProviderStakeReturned(opts *bind.WatchOpts, sink chan<- *FineTuningServingProviderStakeReturned, provider []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "ProviderStakeReturned", providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingProviderStakeReturned)
				if err := _FineTuningServing.contract.UnpackLog(event, "ProviderStakeReturned", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseProviderStakeReturned(log types.Log) (*FineTuningServingProviderStakeReturned, error) {
	event := new(FineTuningServingProviderStakeReturned)
	if err := _FineTuningServing.contract.UnpackLog(event, "ProviderStakeReturned", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingProviderStakedIterator is returned from FilterProviderStaked and is used to iterate over the raw logs and unpacked data for ProviderStaked events raised by the FineTuningServing contract.
type FineTuningServingProviderStakedIterator struct {
	Event *FineTuningServingProviderStaked // Event containing the contract specifics and raw log

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
func (it *FineTuningServingProviderStakedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingProviderStaked)
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
		it.Event = new(FineTuningServingProviderStaked)
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
func (it *FineTuningServingProviderStakedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingProviderStakedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingProviderStaked represents a ProviderStaked event raised by the FineTuningServing contract.
type FineTuningServingProviderStaked struct {
	Provider common.Address
	Amount   *big.Int
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterProviderStaked is a free log retrieval operation binding the contract event 0xcd6dbb0e62eeb71e114bae8b2e2547921dd19209bebf32b595be3e7d247dbbb4.
//
// Solidity: event ProviderStaked(address indexed provider, uint256 amount)
func (_FineTuningServing *FineTuningServingFilterer) FilterProviderStaked(opts *bind.FilterOpts, provider []common.Address) (*FineTuningServingProviderStakedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "ProviderStaked", providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingProviderStakedIterator{contract: _FineTuningServing.contract, event: "ProviderStaked", logs: logs, sub: sub}, nil
}

// WatchProviderStaked is a free log subscription operation binding the contract event 0xcd6dbb0e62eeb71e114bae8b2e2547921dd19209bebf32b595be3e7d247dbbb4.
//
// Solidity: event ProviderStaked(address indexed provider, uint256 amount)
func (_FineTuningServing *FineTuningServingFilterer) WatchProviderStaked(opts *bind.WatchOpts, sink chan<- *FineTuningServingProviderStaked, provider []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "ProviderStaked", providerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingProviderStaked)
				if err := _FineTuningServing.contract.UnpackLog(event, "ProviderStaked", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseProviderStaked(log types.Log) (*FineTuningServingProviderStaked, error) {
	event := new(FineTuningServingProviderStaked)
	if err := _FineTuningServing.contract.UnpackLog(event, "ProviderStaked", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// FineTuningServingProviderTEESignerAcknowledgedIterator is returned from FilterProviderTEESignerAcknowledged and is used to iterate over the raw logs and unpacked data for ProviderTEESignerAcknowledged events raised by the FineTuningServing contract.
type FineTuningServingProviderTEESignerAcknowledgedIterator struct {
	Event *FineTuningServingProviderTEESignerAcknowledged // Event containing the contract specifics and raw log

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
func (it *FineTuningServingProviderTEESignerAcknowledgedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(FineTuningServingProviderTEESignerAcknowledged)
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
		it.Event = new(FineTuningServingProviderTEESignerAcknowledged)
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
func (it *FineTuningServingProviderTEESignerAcknowledgedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *FineTuningServingProviderTEESignerAcknowledgedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// FineTuningServingProviderTEESignerAcknowledged represents a ProviderTEESignerAcknowledged event raised by the FineTuningServing contract.
type FineTuningServingProviderTEESignerAcknowledged struct {
	Provider         common.Address
	TeeSignerAddress common.Address
	Acknowledged     bool
	Raw              types.Log // Blockchain specific contextual infos
}

// FilterProviderTEESignerAcknowledged is a free log retrieval operation binding the contract event 0x4909107c46469d21135443e891c6ecae55b5baa31b338d50f391935308b08f89.
//
// Solidity: event ProviderTEESignerAcknowledged(address indexed provider, address indexed teeSignerAddress, bool acknowledged)
func (_FineTuningServing *FineTuningServingFilterer) FilterProviderTEESignerAcknowledged(opts *bind.FilterOpts, provider []common.Address, teeSignerAddress []common.Address) (*FineTuningServingProviderTEESignerAcknowledgedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var teeSignerAddressRule []interface{}
	for _, teeSignerAddressItem := range teeSignerAddress {
		teeSignerAddressRule = append(teeSignerAddressRule, teeSignerAddressItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "ProviderTEESignerAcknowledged", providerRule, teeSignerAddressRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingProviderTEESignerAcknowledgedIterator{contract: _FineTuningServing.contract, event: "ProviderTEESignerAcknowledged", logs: logs, sub: sub}, nil
}

// WatchProviderTEESignerAcknowledged is a free log subscription operation binding the contract event 0x4909107c46469d21135443e891c6ecae55b5baa31b338d50f391935308b08f89.
//
// Solidity: event ProviderTEESignerAcknowledged(address indexed provider, address indexed teeSignerAddress, bool acknowledged)
func (_FineTuningServing *FineTuningServingFilterer) WatchProviderTEESignerAcknowledged(opts *bind.WatchOpts, sink chan<- *FineTuningServingProviderTEESignerAcknowledged, provider []common.Address, teeSignerAddress []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}
	var teeSignerAddressRule []interface{}
	for _, teeSignerAddressItem := range teeSignerAddress {
		teeSignerAddressRule = append(teeSignerAddressRule, teeSignerAddressItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "ProviderTEESignerAcknowledged", providerRule, teeSignerAddressRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(FineTuningServingProviderTEESignerAcknowledged)
				if err := _FineTuningServing.contract.UnpackLog(event, "ProviderTEESignerAcknowledged", log); err != nil {
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
func (_FineTuningServing *FineTuningServingFilterer) ParseProviderTEESignerAcknowledged(log types.Log) (*FineTuningServingProviderTEESignerAcknowledged, error) {
	event := new(FineTuningServingProviderTEESignerAcknowledged)
	if err := _FineTuningServing.contract.UnpackLog(event, "ProviderTEESignerAcknowledged", log); err != nil {
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
	Provider common.Address
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterServiceRemoved is a free log retrieval operation binding the contract event 0x29d546abb6e94f4f04d5bdccb6682316f597d43776078f47e273f000e77b2a91.
//
// Solidity: event ServiceRemoved(address indexed provider)
func (_FineTuningServing *FineTuningServingFilterer) FilterServiceRemoved(opts *bind.FilterOpts, provider []common.Address) (*FineTuningServingServiceRemovedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "ServiceRemoved", providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingServiceRemovedIterator{contract: _FineTuningServing.contract, event: "ServiceRemoved", logs: logs, sub: sub}, nil
}

// WatchServiceRemoved is a free log subscription operation binding the contract event 0x29d546abb6e94f4f04d5bdccb6682316f597d43776078f47e273f000e77b2a91.
//
// Solidity: event ServiceRemoved(address indexed provider)
func (_FineTuningServing *FineTuningServingFilterer) WatchServiceRemoved(opts *bind.WatchOpts, sink chan<- *FineTuningServingServiceRemoved, provider []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "ServiceRemoved", providerRule)
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

// ParseServiceRemoved is a log parse operation binding the contract event 0x29d546abb6e94f4f04d5bdccb6682316f597d43776078f47e273f000e77b2a91.
//
// Solidity: event ServiceRemoved(address indexed provider)
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
	Provider         common.Address
	Url              string
	Quota            Quota
	PricePerToken    *big.Int
	TeeSignerAddress common.Address
	Occupied         bool
	Raw              types.Log // Blockchain specific contextual infos
}

// FilterServiceUpdated is a free log retrieval operation binding the contract event 0x9657518f02d23efc8a15c042c006a06464dd791f65394ff87310a287c6949462.
//
// Solidity: event ServiceUpdated(address indexed provider, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address teeSignerAddress, bool occupied)
func (_FineTuningServing *FineTuningServingFilterer) FilterServiceUpdated(opts *bind.FilterOpts, provider []common.Address) (*FineTuningServingServiceUpdatedIterator, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.FilterLogs(opts, "ServiceUpdated", providerRule)
	if err != nil {
		return nil, err
	}
	return &FineTuningServingServiceUpdatedIterator{contract: _FineTuningServing.contract, event: "ServiceUpdated", logs: logs, sub: sub}, nil
}

// WatchServiceUpdated is a free log subscription operation binding the contract event 0x9657518f02d23efc8a15c042c006a06464dd791f65394ff87310a287c6949462.
//
// Solidity: event ServiceUpdated(address indexed provider, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address teeSignerAddress, bool occupied)
func (_FineTuningServing *FineTuningServingFilterer) WatchServiceUpdated(opts *bind.WatchOpts, sink chan<- *FineTuningServingServiceUpdated, provider []common.Address) (event.Subscription, error) {

	var providerRule []interface{}
	for _, providerItem := range provider {
		providerRule = append(providerRule, providerItem)
	}

	logs, sub, err := _FineTuningServing.contract.WatchLogs(opts, "ServiceUpdated", providerRule)
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

// ParseServiceUpdated is a log parse operation binding the contract event 0x9657518f02d23efc8a15c042c006a06464dd791f65394ff87310a287c6949462.
//
// Solidity: event ServiceUpdated(address indexed provider, string url, (uint256,uint256,uint256,uint256,string) quota, uint256 pricePerToken, address teeSignerAddress, bool occupied)
func (_FineTuningServing *FineTuningServingFilterer) ParseServiceUpdated(log types.Log) (*FineTuningServingServiceUpdated, error) {
	event := new(FineTuningServingServiceUpdated)
	if err := _FineTuningServing.contract.UnpackLog(event, "ServiceUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
