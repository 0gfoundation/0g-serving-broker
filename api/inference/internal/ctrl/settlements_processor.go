package ctrl

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SettlementStatus represents the different states of settlement
type SettlementStatus uint8

const (
	SettlementSuccess     SettlementStatus = 0
	SettlementPartial     SettlementStatus = 1
	SettlementInsuffBal   SettlementStatus = 2
	SettlementNoSigner    SettlementStatus = 3
	SettlementInvalidNonce SettlementStatus = 4
	SettlementInvalidSig  SettlementStatus = 5
)

// String returns the string representation of SettlementStatus
func (s SettlementStatus) String() string {
	switch s {
	case SettlementSuccess:
		return "SUCCESS"
	case SettlementPartial:
		return "PARTIAL"
	case SettlementInsuffBal:
		return "INSUFFICIENT_BALANCE"
	case SettlementNoSigner:
		return "NO_TEE_SIGNER"
	case SettlementInvalidNonce:
		return "INVALID_NONCE"
	case SettlementInvalidSig:
		return "INVALID_SIGNATURE"
	default:
		return "UNKNOWN"
	}
}

// SettlementResult holds the result of processing settlements
type SettlementResult struct {
	AdjustedSettlements []contract.TEESettlementData
	FailedUsers         []common.Address
	FailureReasons      []SettlementStatus
	PartialUsers        []common.Address
	PartialAmounts      []*big.Int
	// Map from user address to actual settled requests for that user
	SettledRequestsMap  map[string][]*model.Request
}

// SettlementsProcessor handles the processing and adjustment of TEE settlements
type SettlementsProcessor struct {
	ctrl             *Ctrl
	userRequestsMap  map[string]*UserRequests
}

// NewSettlementsProcessor creates a new settlements processor
func NewSettlementsProcessor(ctrl *Ctrl, userRequestsMap map[string]*UserRequests) *SettlementsProcessor {
	return &SettlementsProcessor{
		ctrl:            ctrl,
		userRequestsMap: userRequestsMap,
	}
}

// ProcessSettlements performs static call preview and adjusts settlements accordingly
func (sp *SettlementsProcessor) ProcessSettlements(ctx context.Context, settlements []contract.TEESettlementData) (*SettlementResult, error) {
	result := &SettlementResult{
		AdjustedSettlements: make([]contract.TEESettlementData, 0, len(settlements)),
		FailedUsers:         make([]common.Address, 0),
		FailureReasons:      make([]SettlementStatus, 0),
		PartialUsers:        make([]common.Address, 0),
		PartialAmounts:      make([]*big.Int, 0),
		SettledRequestsMap:  make(map[string][]*model.Request),
	}

	// Perform static call to preview settlement results
	failedUsers, failureReasons, partialUsers, partialAmounts, err := sp.previewSettlements(ctx, settlements)
	if err != nil {
		return nil, err
	}

	// Process each settlement based on preview results
	for _, settlement := range settlements {
		
		// Check if this settlement failed in preview
		if sp.isFailedUser(settlement.User, failedUsers) {
			failureReason := sp.getFailureReason(settlement.User, failedUsers, failureReasons)
			result.FailedUsers = append(result.FailedUsers, settlement.User)
			result.FailureReasons = append(result.FailureReasons, failureReason)
			continue
		}

		// Check if this settlement is partial in preview
		if partialAmount := sp.getPartialAmount(settlement.User, partialUsers, partialAmounts); partialAmount != nil {
			// Adjust settlement for partial payment
			adjustedSettlement, settledRequests := sp.adjustPartialSettlementWithRequests(settlement, partialAmount)
			result.AdjustedSettlements = append(result.AdjustedSettlements, adjustedSettlement)
			result.PartialUsers = append(result.PartialUsers, settlement.User)
			result.PartialAmounts = append(result.PartialAmounts, partialAmount)
			// Record the actual settled requests for this user
			result.SettledRequestsMap[settlement.User.Hex()] = settledRequests
			continue
		}

		// Settlement is successful, use as-is - record all requests for this user
		userAddr := settlement.User.Hex()
		if userReqs, exists := sp.userRequestsMap[userAddr]; exists {
			result.SettledRequestsMap[userAddr] = userReqs.Requests
		}
		result.AdjustedSettlements = append(result.AdjustedSettlements, settlement)
	}

	return result, nil
}

// previewSettlements performs static call to preview settlement results
func (sp *SettlementsProcessor) previewSettlements(ctx context.Context, settlements []contract.TEESettlementData) (
	failedUsers []common.Address,
	failureReasons []uint8,
	partialUsers []common.Address,
	partialAmounts []*big.Int,
	err error) {
	
	callOpts := &bind.CallOpts{
		Context: ctx,
	}

	log.Printf("Performing static call preview for %d settlements", len(settlements))
	
	result, err := sp.ctrl.contract.Contract.InferenceServing.PreviewSettlementResults(callOpts, settlements)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	log.Printf("Static call preview completed: %d failed, %d partial", 
		len(result.FailedUsers), len(result.PartialUsers))
	
	return result.FailedUsers, result.FailureReasons, result.PartialUsers, result.PartialAmounts, nil
}

// adjustPartialSettlementWithRequests adjusts a settlement for partial payment and returns settled requests
func (sp *SettlementsProcessor) adjustPartialSettlementWithRequests(settlement contract.TEESettlementData, settleableAmount *big.Int) (contract.TEESettlementData, []*model.Request) {
	userAddr := settlement.User.Hex()
	userReqs, exists := sp.userRequestsMap[userAddr]
	if !exists {
		log.Printf("Warning: User %s not found in request map for partial adjustment", userAddr)
		return settlement, []*model.Request{}
	}

	// Get settled and unsettled requests in one pass
	settledRequests, unsettledRequests := sp.getSettledAndUnsettledRequests(userReqs, settleableAmount)
	
	// Mark unsettled requests with skipUntil in database
	if len(unsettledRequests) > 0 {
		unsettledHashes := make([]string, len(unsettledRequests))
		for i, req := range unsettledRequests {
			unsettledHashes[i] = req.RequestHash
		}
		// Set skip until 5 minutes from now for insufficient balance requests
		skipDuration := 5 * time.Minute
		err := sp.ctrl.markRequestsWithSkipUntil(unsettledHashes, skipDuration)
		if err != nil {
			log.Printf("Error marking requests with skipUntil: %v", err)
		}
	}

	// Calculate the actual total fee of settled requests
	actualTotalFee := big.NewInt(0)
	for _, req := range settledRequests {
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			log.Printf("Error parsing fee for request %s: %v", req.RequestHash, err)
			continue
		}
		actualTotalFee.Add(actualTotalFee, fee)
	}

	// Create adjusted settlement with actual settled requests total fee
	adjustedSettlement := settlement
	adjustedSettlement.TotalFee = actualTotalFee
	adjustedSettlement.RequestsHash = sp.ctrl.hashUserRequests(settledRequests)

	return adjustedSettlement, settledRequests
}

// adjustPartialSettlement adjusts a settlement for partial payment (legacy method)
func (sp *SettlementsProcessor) adjustPartialSettlement(settlement contract.TEESettlementData, settleableAmount *big.Int) contract.TEESettlementData {
	adjustedSettlement, _ := sp.adjustPartialSettlementWithRequests(settlement, settleableAmount)
	return adjustedSettlement
}

// getSettledAndUnsettledRequests returns both settled and unsettled requests in one pass
func (sp *SettlementsProcessor) getSettledAndUnsettledRequests(userReqs *UserRequests, settleableAmount *big.Int) ([]*model.Request, []*model.Request) {
	if settleableAmount.Sign() <= 0 {
		return []*model.Request{}, userReqs.Requests
	}

	// Sort requests by creation time (oldest first for FIFO settlement)
	requests := make([]*model.Request, len(userReqs.Requests))
	copy(requests, userReqs.Requests)

	var settledRequests []*model.Request
	var unsettledRequests []*model.Request
	remainingToSettle := new(big.Int).Set(settleableAmount)

	for _, req := range requests {
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			log.Printf("Error parsing fee for request %s: %v", req.RequestHash, err)
			unsettledRequests = append(unsettledRequests, req)
			continue
		}

		if remainingToSettle.Sign() > 0 && remainingToSettle.Cmp(fee) >= 0 {
			// Can afford this request
			settledRequests = append(settledRequests, req)
			remainingToSettle.Sub(remainingToSettle, fee)
		} else {
			// Cannot afford this request
			unsettledRequests = append(unsettledRequests, req)
		}
	}

	return settledRequests, unsettledRequests
}

// Helper methods for checking preview results

func (sp *SettlementsProcessor) isFailedUser(user common.Address, failedUsers []common.Address) bool {
	for _, failedUser := range failedUsers {
		if failedUser == user {
			return true
		}
	}
	return false
}

func (sp *SettlementsProcessor) getFailureReason(user common.Address, failedUsers []common.Address, failureReasons []uint8) SettlementStatus {
	for i, failedUser := range failedUsers {
		if failedUser == user {
			return SettlementStatus(failureReasons[i])
		}
	}
	return SettlementSuccess
}

func (sp *SettlementsProcessor) getPartialAmount(user common.Address, partialUsers []common.Address, partialAmounts []*big.Int) *big.Int {
	for i, partialUser := range partialUsers {
		if partialUser == user {
			return partialAmounts[i]
		}
	}
	return nil
}