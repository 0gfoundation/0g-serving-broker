package ctrl

import (
	"context"
	"encoding/json"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// UserRequests groups requests for a single user
type UserRequests struct {
	Requests []*model.Request
	TotalFee *big.Int
}

// SettleFeesWithTEENew implements the new settlement logic with proper retry and skipUntil handling
func (c *Ctrl) SettleFeesWithTEENew(ctx context.Context) error {
	// Clear expired skipUntil flags
	if err := c.db.ClearExpiredSkipUntil(); err != nil {
		log.Printf("Warning: failed to clear expired skipUntil: %v", err)
	}

	// Main settlement loop - will repeat until no more requests to process
	for {
		// Get unprocessed requests (excluding those with active skipUntil)
		reqs, _, err := c.db.ListRequest(model.RequestListOptions{
			Processed:             false,
			Sort:                  model.PtrOf("created_at ASC"),
			ExcludeZeroOutput:     true,
			RequireOutputFeeOrOld: true,
			OldRequestThreshold:   10 * time.Minute,
			IncludeSkipped:        false, // Exclude requests with skipUntil
		})
		if err != nil {
			return errors.Wrap(err, "list request from db")
		}
		
		if len(reqs) == 0 {
			log.Printf("No more requests to settle")
			return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
		}

		log.Printf("Processing settlement for %d requests", len(reqs))
		
		// Group requests by user
		userRequestsMap := c.groupRequestsByUser(reqs)
		
		// Create and sign settlements
		settlements, err := c.createSettlements(userRequestsMap)
		if err != nil {
			return errors.Wrap(err, "create settlements")
		}

		// Process with static call preview
		processor := NewSettlementsProcessor(c, userRequestsMap)
		result, err := processor.ProcessSettlements(ctx, settlements)
		if err != nil {
			log.Printf("Error processing settlements: %v", err)
			// On error, mark all requests to skip temporarily
			c.markAllRequestsToSkip(reqs, 5*time.Minute)
			return err
		}

		// Handle results
		err = c.handleSettlementResults(ctx, result, userRequestsMap)
		if err != nil {
			return errors.Wrap(err, "handle settlement results")
		}

		// If all settlements failed with permanent errors, stop
		if len(result.AdjustedSettlements) == 0 && c.allPermanentFailures(result.FailureReasons) {
			log.Printf("All remaining settlements have permanent failures, stopping")
			break
		}
	}

	return nil
}

// groupRequestsByUser groups requests by user address
func (c *Ctrl) groupRequestsByUser(reqs []model.Request) map[string]*UserRequests {
	userRequestsMap := make(map[string]*UserRequests)
	
	for _, req := range reqs {
		// Parse fee to big.Int
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			log.Printf("Error parsing fee for request %s: %v", req.RequestHash, err)
			continue
		}

		reqCopy := req // Important: create a copy
		if userReqs, exists := userRequestsMap[req.UserAddress]; exists {
			userReqs.Requests = append(userReqs.Requests, &reqCopy)
			userReqs.TotalFee = new(big.Int).Add(userReqs.TotalFee, fee)
		} else {
			userRequestsMap[req.UserAddress] = &UserRequests{
				Requests: []*model.Request{&reqCopy},
				TotalFee: fee,
			}
		}
	}
	
	return userRequestsMap
}

// createSettlements creates and signs settlement data for each user
func (c *Ctrl) createSettlements(userRequestsMap map[string]*UserRequests) ([]contract.TEESettlementData, error) {
	var settlements []contract.TEESettlementData
	
	for userAddr, userReqs := range userRequestsMap {
		settlementData, err := c.createUserSettlement(userAddr, userReqs)
		if err != nil {
			log.Printf("Error creating settlement for user %s: %v", userAddr, err)
			continue
		}
		settlements = append(settlements, settlementData)
	}
	
	return settlements, nil
}

// handleSettlementResults processes the results from settlement attempts
func (c *Ctrl) handleSettlementResults(ctx context.Context, result *SettlementResult, userRequestsMap map[string]*UserRequests) error {
	// Execute actual settlements for adjusted ones
	if len(result.AdjustedSettlements) > 0 {
		actualFailures, partialSettlements, err := c.executeSettlementBatches(ctx, result.AdjustedSettlements)
		if err != nil {
			return errors.Wrap(err, "execute settlement batches")
		}

		// Log partial settlements
		for user, amount := range partialSettlements {
			log.Printf("Partial settlement completed for user %s: %s wei", user.Hex(), amount.String())
		}

		// Delete successfully settled requests (only the ones that were actually settled)
		successfulUsers := c.getSuccessfulUsers(result.AdjustedSettlements, actualFailures)
		for _, userAddr := range successfulUsers {
			userAddrHex := userAddr.Hex()
			if settledRequests, exists := result.SettledRequestsMap[userAddrHex]; exists {
				c.deleteUserRequests(settledRequests)
			}
		}
	}

	// Handle permanent failures - delete these requests
	for i, user := range result.FailedUsers {
		if c.isPermanentFailure(result.FailureReasons[i]) {
			log.Printf("Deleting requests for user %s due to permanent failure: %s", 
				user.Hex(), result.FailureReasons[i].String())
			userAddrHex := user.Hex()
			// For permanent failures, we delete all requests for the user (not partial)
			if userReqs, exists := userRequestsMap[userAddrHex]; exists {
				c.deleteUserRequests(userReqs.Requests)
			}
		}
	}

	// Partial settlements already have their unsettled requests marked with skipUntil
	// (handled in SettlementsProcessor.adjustPartialSettlement)

	return nil
}

// deleteUserRequests deletes processed requests from database
func (c *Ctrl) deleteUserRequests(requests []*model.Request) {
	requestHashes := make([]string, len(requests))
	for i, req := range requests {
		requestHashes[i] = req.RequestHash
	}
	
	err := c.db.DeleteRequestsByHashes(requestHashes)
	if err != nil {
		log.Printf("Error deleting settled requests: %v", err)
	} else {
		log.Printf("Deleted %d settled requests", len(requestHashes))
	}
}

// getSuccessfulUsers returns users whose settlements succeeded
func (c *Ctrl) getSuccessfulUsers(settlements []contract.TEESettlementData, failures map[common.Address]SettlementStatus) []common.Address {
	var successful []common.Address
	for _, settlement := range settlements {
		if _, failed := failures[settlement.User]; !failed {
			successful = append(successful, settlement.User)
		}
	}
	return successful
}

// markAllRequestsToSkip marks all requests to skip for a duration
func (c *Ctrl) markAllRequestsToSkip(reqs []model.Request, duration time.Duration) {
	requestHashes := make([]string, len(reqs))
	for i, req := range reqs {
		requestHashes[i] = req.RequestHash
	}
	
	skipUntil := time.Now().Add(duration)
	err := c.db.UpdateRequestsSkipUntil(requestHashes, &skipUntil)
	if err != nil {
		log.Printf("Error marking requests to skip: %v", err)
	}
}

// allPermanentFailures checks if all failures are permanent
func (c *Ctrl) allPermanentFailures(reasons []SettlementStatus) bool {
	if len(reasons) == 0 {
		return false
	}
	
	for _, reason := range reasons {
		if !c.isPermanentFailure(reason) {
			return false
		}
	}
	return true
}

// createUserSettlement creates a single user's settlement with TEE signature
func (c *Ctrl) createUserSettlement(userAddr string, userReqs *UserRequests) (contract.TEESettlementData, error) {
	// Create hash of all requests for this user
	requestsHash := c.hashUserRequests(userReqs.Requests)

	// Generate nonce based on timestamp
	nonce := big.NewInt(time.Now().Unix())
	nonce.Mul(nonce, big.NewInt(10000000))

	settlementData := contract.TEESettlementData{
		User:         common.HexToAddress(userAddr),
		Provider:     common.HexToAddress(c.contract.ProviderAddress),
		TotalFee:     userReqs.TotalFee,
		RequestsHash: requestsHash,
		Nonce:        nonce,
	}

	// Create message hash for signing
	messageHash := crypto.Keccak256(
		requestsHash[:],
		common.LeftPadBytes(nonce.Bytes(), 32),
		settlementData.Provider.Bytes(),
		settlementData.User.Bytes(),
		common.LeftPadBytes(userReqs.TotalFee.Bytes(), 32),
	)

	// Sign with TEE service
	signature, err := c.teeService.Sign(messageHash)
	if err != nil {
		return settlementData, errors.Wrap(err, "TEE signing failed")
	}

	settlementData.Signature = signature
	return settlementData, nil
}

// markRequestsWithSkipUntil marks requests to be skipped until a certain time
func (c *Ctrl) markRequestsWithSkipUntil(requestHashes []string, skipDuration time.Duration) error {
	if len(requestHashes) == 0 {
		return nil
	}
	
	skipUntil := time.Now().Add(skipDuration)
	err := c.db.UpdateRequestsSkipUntil(requestHashes, &skipUntil)
	if err != nil {
		return errors.Wrap(err, "update requests skipUntil")
	}
	
	log.Printf("Marked %d requests to skip until %v", len(requestHashes), skipUntil)
	return nil
}

// hashUserRequests creates a deterministic hash of all requests for a user
func (c *Ctrl) hashUserRequests(requests []*model.Request) [32]byte {
	var requestData []byte
	for _, req := range requests {
		// Concatenate request data: RequestHash + UserAddress + Fee + InputFee + OutputFee
		requestData = append(requestData, []byte(req.RequestHash)...)
		requestData = append(requestData, []byte(req.UserAddress)...)
		requestData = append(requestData, []byte(req.Fee)...)
		requestData = append(requestData, []byte(req.InputFee)...)
		requestData = append(requestData, []byte(req.OutputFee)...)
	}
	return crypto.Keccak256Hash(requestData)
}

// isPermanentFailure determines if a settlement failure is permanent and shouldn't be retried
func (c *Ctrl) isPermanentFailure(status SettlementStatus) bool {
	return status == SettlementNoSigner
}

// executeSettlementBatches executes settlements in batches and handles the results
func (c *Ctrl) executeSettlementBatches(ctx context.Context, settlements []contract.TEESettlementData) (
	actualFailures map[common.Address]SettlementStatus,
	partialSettlements map[common.Address]*big.Int,
	networkError error) {
	
	actualFailures = make(map[common.Address]SettlementStatus)
	partialSettlements = make(map[common.Address]*big.Int)
	
	// Process settlements in batches
	for i := 0; i < len(settlements); i += constant.TEESettlementBatchSize {
		end := i + constant.TEESettlementBatchSize
		if end > len(settlements) {
			end = len(settlements)
		}
		
		batch := settlements[i:end]
		
		// Log batch processing
		batchJSON, _ := json.Marshal(batch)
		log.Printf("Processing TEE settlements batch %d-%d (users %d-%d of %d): %s",
			i/constant.TEESettlementBatchSize+1, (end-1)/constant.TEESettlementBatchSize+1, 
			i+1, end, len(settlements), string(batchJSON))
		
		// Execute batch settlement 
		failedUsers, err := c.contract.SettleFeesWithTEE(ctx, batch)
		if err != nil {
			return actualFailures, partialSettlements, errors.Wrapf(err, "settlement batch %d-%d failed", i, end-1)
		}
		
		// Record failures for this batch
		for _, user := range failedUsers {
			actualFailures[user] = SettlementInsuffBal // Default to insufficient balance
		}
		
		log.Printf("Completed batch %d-%d: %d failed users", i+1, end, len(failedUsers))
	}
	
	return actualFailures, partialSettlements, nil
}