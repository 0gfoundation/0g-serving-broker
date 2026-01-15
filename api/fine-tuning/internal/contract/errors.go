package providercontract

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
)

// Common contract error types for better error handling
var (
	// AccountLibrary errors
	ErrAccountNotExists                   = errors.New("account not exists")
	ErrAccountExists                      = errors.New("account already exists")
	ErrInsufficientBalance                = errors.New("insufficient balance")
	ErrRefundInvalid                      = errors.New("refund invalid")
	ErrRefundProcessed                    = errors.New("refund processed")
	ErrRefundLocked                       = errors.New("refund locked")
	ErrTooManyRefunds                     = errors.New("too many refunds")
	ErrAdditionalInfoTooLong              = errors.New("additional info too long")
	ErrCannotRevokeWithNonZeroBalance     = errors.New("cannot revoke with non-zero balance")
	ErrDeliverableAlreadyExists           = errors.New("deliverable already exists")
	ErrDeliverableIdInvalidLength         = errors.New("deliverable id invalid length")
	ErrDeliverableNotExists               = errors.New("deliverable not exists")
	ErrPreviousDeliverableNotAcknowledged = errors.New("previous deliverable not acknowledged")
	ErrSecretShouldBeEmpty                = errors.New("secret should be empty")
	ErrSecretShouldNotBeEmpty             = errors.New("secret should not be empty")

	// ServiceLibrary errors
	ErrServiceNotExist              = errors.New("service not exist")
	ErrInsufficientStake            = errors.New("insufficient stake")
	ErrCannotAddStakeWhenUpdating   = errors.New("cannot add stake when updating")
	ErrInvalidLedgerAddress         = errors.New("invalid ledger address")
	ErrDirectDepositsDisabled       = errors.New("direct deposits disabled")
	ErrETHTransferFailed            = errors.New("eth transfer failed")
	ErrTransferToLedgerFailed       = errors.New("transfer to ledger failed")
	ErrLimitTooLarge                = errors.New("limit too large")
	ErrLockTimeOutOfRange           = errors.New("lock time out of range")
	ErrPenaltyPercentageTooHigh     = errors.New("penalty percentage too high")

	// VerifierLibrary errors
	ErrDeliverableIdTooLong = errors.New("deliverable id too long")
	ErrInvalidSignature     = errors.New("invalid signature")
	ErrInvalidVerifierInput = errors.New("invalid verifier input")
)

// contractABI is the parsed ABI of the FineTuningServing contract
var contractABI *abi.ABI

func init() {
	// Parse the ABI once at initialization
	parsed, err := abi.JSON(strings.NewReader(contract.FineTuningServingABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse contract ABI: %v", err))
	}
	contractABI = &parsed
}


// WrapContractError extracts and wraps contract errors from RPC responses
// It uses errors.As with rpc.DataError and abi.ABI.ErrorByID for robust error parsing
func WrapContractError(err error) error {
	if err == nil {
		return nil
	}

	// Try to cast to rpc.DataError interface to get the error data
	var dataErr rpc.DataError
	if errors.As(err, &dataErr) {
		// Get the error data (hex string like "0x08ca03ac...")
		errorData := dataErr.ErrorData()
		if errorData != nil {
			// Try to convert to string
			if dataStr, ok := errorData.(string); ok && dataStr != "" {
				// Decode hex data
				dataStr = strings.TrimPrefix(dataStr, "0x")
				data, decodeErr := hex.DecodeString(dataStr)
				if decodeErr == nil && len(data) >= 4 {
					// Extract error selector (first 4 bytes)
					selector := [4]byte(data[:4])

					// Use ABI to find the error definition
					abiErr, abiErrLookup := contractABI.ErrorByID(selector)
					if abiErrLookup == nil && abiErr != nil {
						// Unpack the error arguments
						unpacked, unpackErr := abiErr.Unpack(data)
						if unpackErr == nil {
							// Convert unpacked interface to []interface{}
							if unpackedSlice, ok := unpacked.([]interface{}); ok {
								return formatContractError(abiErr.Name, unpackedSlice)
							}
						}
					}
				}
			}
		}
	}

	// Fallback: check error message for keywords
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, "accountnotexists") ||
		(strings.Contains(errMsg, "account") && strings.Contains(errMsg, "not") && strings.Contains(errMsg, "exist")) {
		return errors.Join(ErrAccountNotExists, err)
	}

	if strings.Contains(errMsg, "servicenotexist") ||
		(strings.Contains(errMsg, "service") && strings.Contains(errMsg, "not") && strings.Contains(errMsg, "exist")) {
		return errors.Join(ErrServiceNotExist, err)
	}

	if strings.Contains(errMsg, "invalidsignature") ||
		(strings.Contains(errMsg, "invalid") && strings.Contains(errMsg, "signature")) {
		return errors.Join(ErrInvalidSignature, err)
	}

	// Return original error if no match
	return err
}

// formatContractError formats the unpacked error arguments into a readable error message
func formatContractError(errName string, args []interface{}) error {
	switch errName {
	// AccountLibrary errors
	case "AccountNotExists":
		if len(args) >= 2 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			return fmt.Errorf("%w: user=%s, provider=%s", ErrAccountNotExists, user.Hex(), provider.Hex())
		}
		return ErrAccountNotExists

	case "AccountExists":
		if len(args) >= 2 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			return fmt.Errorf("%w: user=%s, provider=%s", ErrAccountExists, user.Hex(), provider.Hex())
		}
		return ErrAccountExists

	case "InsufficientBalance":
		if len(args) >= 2 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			return fmt.Errorf("%w: user=%s, provider=%s", ErrInsufficientBalance, user.Hex(), provider.Hex())
		}
		return ErrInsufficientBalance

	case "RefundInvalid":
		if len(args) >= 3 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			index, _ := args[2].(*big.Int)
			return fmt.Errorf("%w: user=%s, provider=%s, index=%s", ErrRefundInvalid, user.Hex(), provider.Hex(), index.String())
		}
		return ErrRefundInvalid

	case "RefundProcessed":
		if len(args) >= 3 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			index, _ := args[2].(*big.Int)
			return fmt.Errorf("%w: user=%s, provider=%s, index=%s", ErrRefundProcessed, user.Hex(), provider.Hex(), index.String())
		}
		return ErrRefundProcessed

	case "RefundLocked":
		if len(args) >= 3 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			index, _ := args[2].(*big.Int)
			return fmt.Errorf("%w: user=%s, provider=%s, index=%s", ErrRefundLocked, user.Hex(), provider.Hex(), index.String())
		}
		return ErrRefundLocked

	case "TooManyRefunds":
		if len(args) >= 2 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			return fmt.Errorf("%w: user=%s, provider=%s", ErrTooManyRefunds, user.Hex(), provider.Hex())
		}
		return ErrTooManyRefunds

	case "AdditionalInfoTooLong":
		return ErrAdditionalInfoTooLong

	case "CannotRevokeWithNonZeroBalance":
		if len(args) >= 3 {
			user, _ := args[0].(common.Address)
			provider, _ := args[1].(common.Address)
			balance, _ := args[2].(*big.Int)
			return fmt.Errorf("%w: user=%s, provider=%s, balance=%s", ErrCannotRevokeWithNonZeroBalance, user.Hex(), provider.Hex(), balance.String())
		}
		return ErrCannotRevokeWithNonZeroBalance

	case "DeliverableAlreadyExists":
		if len(args) >= 1 {
			id, _ := args[0].(string)
			return fmt.Errorf("%w: id=%s", ErrDeliverableAlreadyExists, id)
		}
		return ErrDeliverableAlreadyExists

	case "DeliverableIdInvalidLength":
		if len(args) >= 1 {
			length, _ := args[0].(*big.Int)
			return fmt.Errorf("%w: length=%s", ErrDeliverableIdInvalidLength, length.String())
		}
		return ErrDeliverableIdInvalidLength

	case "DeliverableNotExists":
		if len(args) >= 1 {
			id, _ := args[0].(string)
			return fmt.Errorf("%w: id=%s", ErrDeliverableNotExists, id)
		}
		return ErrDeliverableNotExists

	case "PreviousDeliverableNotAcknowledged":
		if len(args) >= 1 {
			id, _ := args[0].(string)
			return fmt.Errorf("%w: id=%s", ErrPreviousDeliverableNotAcknowledged, id)
		}
		return ErrPreviousDeliverableNotAcknowledged

	case "SecretShouldBeEmpty":
		return ErrSecretShouldBeEmpty

	case "SecretShouldNotBeEmpty":
		return ErrSecretShouldNotBeEmpty

	// ServiceLibrary errors
	case "ServiceNotExist":
		if len(args) >= 1 {
			provider, _ := args[0].(common.Address)
			return fmt.Errorf("%w: provider=%s", ErrServiceNotExist, provider.Hex())
		}
		return ErrServiceNotExist

	case "InsufficientStake":
		if len(args) >= 2 {
			provided, _ := args[0].(*big.Int)
			required, _ := args[1].(*big.Int)
			return fmt.Errorf("%w: provided=%s, required=%s", ErrInsufficientStake, provided.String(), required.String())
		}
		return ErrInsufficientStake

	case "CannotAddStakeWhenUpdating":
		return ErrCannotAddStakeWhenUpdating

	case "InvalidLedgerAddress":
		return ErrInvalidLedgerAddress

	case "DirectDepositsDisabled":
		return ErrDirectDepositsDisabled

	case "ETHTransferFailed":
		return ErrETHTransferFailed

	case "TransferToLedgerFailed":
		return ErrTransferToLedgerFailed

	case "LimitTooLarge":
		if len(args) >= 1 {
			limit, _ := args[0].(*big.Int)
			return fmt.Errorf("%w: limit=%s", ErrLimitTooLarge, limit.String())
		}
		return ErrLimitTooLarge

	case "LockTimeOutOfRange":
		if len(args) >= 1 {
			lockTime, _ := args[0].(*big.Int)
			return fmt.Errorf("%w: lockTime=%s", ErrLockTimeOutOfRange, lockTime.String())
		}
		return ErrLockTimeOutOfRange

	case "PenaltyPercentageTooHigh":
		if len(args) >= 1 {
			percentage, _ := args[0].(*big.Int)
			return fmt.Errorf("%w: percentage=%s", ErrPenaltyPercentageTooHigh, percentage.String())
		}
		return ErrPenaltyPercentageTooHigh

	// VerifierLibrary errors
	case "DeliverableIdTooLong":
		if len(args) >= 1 {
			length, _ := args[0].(*big.Int)
			return fmt.Errorf("%w: length=%s", ErrDeliverableIdTooLong, length.String())
		}
		return ErrDeliverableIdTooLong

	case "InvalidSignature":
		return ErrInvalidSignature

	case "InvalidVerifierInput":
		if len(args) >= 1 {
			reason, _ := args[0].(string)
			return fmt.Errorf("%w: reason=%s", ErrInvalidVerifierInput, reason)
		}
		return ErrInvalidVerifierInput

	default:
		return fmt.Errorf("contract error: %s", errName)
	}
}

// Helper functions to check specific errors
func IsAccountNotExists(err error) bool {
	return errors.Is(err, ErrAccountNotExists)
}

func IsAccountExists(err error) bool {
	return errors.Is(err, ErrAccountExists)
}

func IsInsufficientBalance(err error) bool {
	return errors.Is(err, ErrInsufficientBalance)
}

func IsRefundInvalid(err error) bool {
	return errors.Is(err, ErrRefundInvalid)
}

func IsRefundProcessed(err error) bool {
	return errors.Is(err, ErrRefundProcessed)
}

func IsRefundLocked(err error) bool {
	return errors.Is(err, ErrRefundLocked)
}

func IsTooManyRefunds(err error) bool {
	return errors.Is(err, ErrTooManyRefunds)
}

func IsServiceNotExist(err error) bool {
	return errors.Is(err, ErrServiceNotExist)
}

func IsInvalidSignature(err error) bool {
	return errors.Is(err, ErrInvalidSignature)
}

func IsDeliverableAlreadyExists(err error) bool {
	return errors.Is(err, ErrDeliverableAlreadyExists)
}

func IsDeliverableNotExists(err error) bool {
	return errors.Is(err, ErrDeliverableNotExists)
}

func IsInvalidVerifierInput(err error) bool {
	return errors.Is(err, ErrInvalidVerifierInput)
}

func IsInsufficientStake(err error) bool {
	return errors.Is(err, ErrInsufficientStake)
}

func IsCannotAddStakeWhenUpdating(err error) bool {
	return errors.Is(err, ErrCannotAddStakeWhenUpdating)
}

func IsInvalidLedgerAddress(err error) bool {
	return errors.Is(err, ErrInvalidLedgerAddress)
}

func IsDirectDepositsDisabled(err error) bool {
	return errors.Is(err, ErrDirectDepositsDisabled)
}

func IsETHTransferFailed(err error) bool {
	return errors.Is(err, ErrETHTransferFailed)
}

func IsTransferToLedgerFailed(err error) bool {
	return errors.Is(err, ErrTransferToLedgerFailed)
}

func IsSecretShouldBeEmpty(err error) bool {
	return errors.Is(err, ErrSecretShouldBeEmpty)
}

func IsSecretShouldNotBeEmpty(err error) bool {
	return errors.Is(err, ErrSecretShouldNotBeEmpty)
}

func IsLimitTooLarge(err error) bool {
	return errors.Is(err, ErrLimitTooLarge)
}

func IsLockTimeOutOfRange(err error) bool {
	return errors.Is(err, ErrLockTimeOutOfRange)
}

func IsPenaltyPercentageTooHigh(err error) bool {
	return errors.Is(err, ErrPenaltyPercentageTooHigh)
}
