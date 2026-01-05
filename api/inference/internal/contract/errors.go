package providercontract

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
)

// Common contract error types for better error handling
var (
	// InferenceAccount.sol errors
	ErrAccountNotExists    = errors.New("account not exists")
	ErrAccountExists       = errors.New("account already exists")
	ErrInsufficientBalance = errors.New("insufficient balance")
	ErrRefundInvalid       = errors.New("refund invalid")
	ErrRefundProcessed     = errors.New("refund processed")
	ErrRefundLocked        = errors.New("refund locked")
	ErrTooManyRefunds      = errors.New("too many refunds")

	// InferenceService.sol errors
	ErrServiceNotExist       = errors.New("service not exist")
	ErrAdditionalInfoTooLong = errors.New("additional info too long")

	// InferenceServing.sol errors
	ErrInvalidTEESignature = errors.New("invalid TEE signature")

	// LedgerManager.sol errors
	ErrLedgerNotExists        = errors.New("ledger not exists")
	ErrLedgerExists           = errors.New("ledger already exists")
	ErrTooManyProviders       = errors.New("too many providers")
	ErrInvalidServiceType     = errors.New("invalid service type")
	ErrServiceNotRegistered   = errors.New("service not registered")
	ErrServiceNameExists      = errors.New("service name already exists")
	ErrInvalidServiceAddress  = errors.New("invalid service address")
)

// contractABI is the parsed ABI of the InferenceServing contract
var contractABI *abi.ABI

func init() {
	// Parse the ABI once at initialization
	parsed, err := abi.JSON(strings.NewReader(contract.InferenceServingABI))
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

	// Return original error if no match
	return err
}

// formatContractError formats the unpacked error arguments into a readable error message
func formatContractError(errName string, args []interface{}) error {
	switch errName {
	// InferenceAccount.sol errors
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

	// InferenceService.sol errors
	case "ServiceNotExist":
		if len(args) >= 1 {
			provider, _ := args[0].(common.Address)
			return fmt.Errorf("%w: provider=%s", ErrServiceNotExist, provider.Hex())
		}
		return ErrServiceNotExist

	case "AdditionalInfoTooLong":
		return ErrAdditionalInfoTooLong

	// InferenceServing.sol errors
	case "InvalidTEESignature":
		if len(args) >= 1 {
			reason, _ := args[0].(string)
			return fmt.Errorf("%w: %s", ErrInvalidTEESignature, reason)
		}
		return ErrInvalidTEESignature

	// LedgerManager.sol errors
	case "LedgerNotExists":
		if len(args) >= 1 {
			user, _ := args[0].(common.Address)
			return fmt.Errorf("%w: user=%s", ErrLedgerNotExists, user.Hex())
		}
		return ErrLedgerNotExists

	case "LedgerExists":
		if len(args) >= 1 {
			user, _ := args[0].(common.Address)
			return fmt.Errorf("%w: user=%s", ErrLedgerExists, user.Hex())
		}
		return ErrLedgerExists

	case "TooManyProviders":
		if len(args) >= 2 {
			requested, _ := args[0].(*big.Int)
			maximum, _ := args[1].(*big.Int)
			return fmt.Errorf("%w: requested=%s, maximum=%s", ErrTooManyProviders, requested.String(), maximum.String())
		}
		return ErrTooManyProviders

	case "InvalidServiceType":
		if len(args) >= 1 {
			serviceType, _ := args[0].(string)
			return fmt.Errorf("%w: %s", ErrInvalidServiceType, serviceType)
		}
		return ErrInvalidServiceType

	case "ServiceNotRegistered":
		if len(args) >= 1 {
			serviceAddress, _ := args[0].(common.Address)
			return fmt.Errorf("%w: address=%s", ErrServiceNotRegistered, serviceAddress.Hex())
		}
		return ErrServiceNotRegistered

	case "ServiceNameExists":
		if len(args) >= 1 {
			serviceName, _ := args[0].(string)
			return fmt.Errorf("%w: %s", ErrServiceNameExists, serviceName)
		}
		return ErrServiceNameExists

	case "InvalidServiceAddress":
		if len(args) >= 1 {
			serviceAddress, _ := args[0].(common.Address)
			return fmt.Errorf("%w: address=%s", ErrInvalidServiceAddress, serviceAddress.Hex())
		}
		return ErrInvalidServiceAddress

	default:
		return fmt.Errorf("contract error: %s", errName)
	}
}

// IsAccountNotExists checks if the error is AccountNotExists
func IsAccountNotExists(err error) bool {
	return errors.Is(err, ErrAccountNotExists)
}

// IsAccountExists checks if the error is AccountExists
func IsAccountExists(err error) bool {
	return errors.Is(err, ErrAccountExists)
}

// IsInsufficientBalance checks if the error is InsufficientBalance
func IsInsufficientBalance(err error) bool {
	return errors.Is(err, ErrInsufficientBalance)
}

// IsRefundInvalid checks if the error is RefundInvalid
func IsRefundInvalid(err error) bool {
	return errors.Is(err, ErrRefundInvalid)
}

// IsRefundProcessed checks if the error is RefundProcessed
func IsRefundProcessed(err error) bool {
	return errors.Is(err, ErrRefundProcessed)
}

// IsRefundLocked checks if the error is RefundLocked
func IsRefundLocked(err error) bool {
	return errors.Is(err, ErrRefundLocked)
}

// IsTooManyRefunds checks if the error is TooManyRefunds
func IsTooManyRefunds(err error) bool {
	return errors.Is(err, ErrTooManyRefunds)
}

// IsServiceNotExist checks if the error is ServiceNotExist
func IsServiceNotExist(err error) bool {
	return errors.Is(err, ErrServiceNotExist)
}

// IsAdditionalInfoTooLong checks if the error is AdditionalInfoTooLong
func IsAdditionalInfoTooLong(err error) bool {
	return errors.Is(err, ErrAdditionalInfoTooLong)
}

// IsInvalidTEESignature checks if the error is InvalidTEESignature
func IsInvalidTEESignature(err error) bool {
	return errors.Is(err, ErrInvalidTEESignature)
}

// IsLedgerNotExists checks if the error is LedgerNotExists
func IsLedgerNotExists(err error) bool {
	return errors.Is(err, ErrLedgerNotExists)
}

// IsLedgerExists checks if the error is LedgerExists
func IsLedgerExists(err error) bool {
	return errors.Is(err, ErrLedgerExists)
}

// IsTooManyProviders checks if the error is TooManyProviders
func IsTooManyProviders(err error) bool {
	return errors.Is(err, ErrTooManyProviders)
}

// IsInvalidServiceType checks if the error is InvalidServiceType
func IsInvalidServiceType(err error) bool {
	return errors.Is(err, ErrInvalidServiceType)
}

// IsServiceNotRegistered checks if the error is ServiceNotRegistered
func IsServiceNotRegistered(err error) bool {
	return errors.Is(err, ErrServiceNotRegistered)
}

// IsServiceNameExists checks if the error is ServiceNameExists
func IsServiceNameExists(err error) bool {
	return errors.Is(err, ErrServiceNameExists)
}

// IsInvalidServiceAddress checks if the error is InvalidServiceAddress
func IsInvalidServiceAddress(err error) bool {
	return errors.Is(err, ErrInvalidServiceAddress)
}
