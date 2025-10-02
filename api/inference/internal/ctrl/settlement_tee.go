package ctrl

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)


func (c *Ctrl) SettleFeesWithTEE(ctx context.Context) error {
	// Get unprocessed requests with output_fee set OR created more than 3 minutes ago
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed:             false,
		Sort:                  model.PtrOf("created_at ASC"),
		ExcludeZeroOutput:     true,
		RequireOutputFeeOrOld: true,
		OldRequestThreshold:   10 * time.Minute,
	})
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}
	if len(reqs) == 0 {
		return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
	}

	// Group requests by user
	userRequestsMap := make(map[string]*UserRequests)
	latestReqCreateAt := reqs[0].CreatedAt

	for _, req := range reqs {
		if latestReqCreateAt.Before(*req.CreatedAt) {
			latestReqCreateAt = req.CreatedAt
		}

		// Parse fee to big.Int
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			return errors.Wrap(err, "parse fee")
		}

		if userReqs, exists := userRequestsMap[req.UserAddress]; exists {
			// Add to existing user's requests
			userReqs.Requests = append(userReqs.Requests, &req)
			userReqs.TotalFee = new(big.Int).Add(userReqs.TotalFee, fee)
		} else {
			// Create new entry for user
			userRequestsMap[req.UserAddress] = &UserRequests{
				Requests: []*model.Request{&req},
				TotalFee: fee,
			}
		}
	}

	// Create settlements for each user
	var settlements []contract.TEESettlementData
	for userAddr, userReqs := range userRequestsMap {
		// Create hash of all requests for this user
		requestsHash := c.hashUserRequests(userReqs.Requests)

		// Generate nonce based on timestamp multiplied by 10000000 to avoid conflicts
		nonce := big.NewInt(time.Now().Unix())
		nonce.Mul(nonce, big.NewInt(10000000))

		settlementData := contract.TEESettlementData{
			User:         common.HexToAddress(userAddr),
			Provider:     common.HexToAddress(c.contract.ProviderAddress),
			TotalFee:     userReqs.TotalFee,
			RequestsHash: requestsHash,
			Nonce:        nonce,
		}

		// Create message hash for signing (matching Solidity order)
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
			return errors.Wrap(err, "TEE signing failed")
		}

		settlementData.Signature = signature
		settlements = append(settlements, settlementData)
	}

	// Log total settlements for debugging
	log.Printf("Total TEE settlements to process: %d users", len(settlements))

	// Process settlements with retry logic
	return c.processSettlementsWithRetry(ctx, settlements, latestReqCreateAt, userRequestsMap)
}



func (c *Ctrl) ProcessSettlement(ctx context.Context) error {
	settleTriggerThreshold := (c.Service.InputPrice + c.Service.OutputPrice) * constant.SettleTriggerThreshold

	// Use the optimized method that calculates unsettled fees with a single query
	accounts, err := c.db.ListUsersWithUnsettledFees(&model.UserListOptions{
		LowBalanceRisk:         model.PtrOf(time.Now().Add(-c.contract.LockTime + c.autoSettleBufferTime)),
		MinUnsettledFee:        model.PtrOf(int64(0)),
		SettleTriggerThreshold: &settleTriggerThreshold,
	}, c.Service.InputPrice, c.Service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}
	if len(accounts) == 0 {
		return nil
	}

	// Verify the available balance in the contract
	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

	// Re-check accounts after sync with current time using optimized query
	accounts, err = c.db.ListUsersWithUnsettledFees(&model.UserListOptions{
		MinUnsettledFee:        model.PtrOf(int64(0)),
		LowBalanceRisk:         model.PtrOf(time.Now()),
		SettleTriggerThreshold: &settleTriggerThreshold,
	}, c.Service.InputPrice, c.Service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db after sync")
	}
	if len(accounts) == 0 {
		return nil
	}

	log.Print("Accounts at risk of having insufficient funds and will be settled immediately with TEE.")
	return errors.Wrap(c.SettleFeesWithTEE(ctx), "settle fees with TEE")
}

// processSettlementsWithRetry handles the settlement process with static calls and retry logic
func (c *Ctrl) processSettlementsWithRetry(ctx context.Context, settlements []contract.TEESettlementData, latestReqCreateAt *time.Time, userRequestsMap map[string]*UserRequests) error {
	maxRetries := 3
	backoffSeconds := []int{1, 3, 9} // Exponential backoff: 1s, 3s, 9s
	
	currentSettlements := settlements
	
	for retry := 0; retry <= maxRetries; retry++ {
		if len(currentSettlements) == 0 {
			log.Printf("All settlements processed successfully")
			break
		}
		
		if retry > 0 {
			log.Printf("Retry attempt %d/%d for %d remaining settlements", retry, maxRetries, len(currentSettlements))
			time.Sleep(time.Duration(backoffSeconds[retry-1]) * time.Second)
		}
		
		// Perform static call first to preview results
		validSettlements, staticCallFailures := c.previewSettlements(ctx, currentSettlements, userRequestsMap)
		
		// Log static call results
		if len(staticCallFailures) > 0 {
			for user, status := range staticCallFailures {
				log.Printf("Static call failure for user %s: %s", user.Hex(), status.String())
			}
		}
		
		if len(validSettlements) == 0 {
			log.Printf("No valid settlements remaining after static call preview")
			break
		}
		
		// Process settlements in batches
		actualFailures, partialSettlements, networkError := c.executeSettlementBatches(ctx, validSettlements)
		
		// Handle network errors with retry
		if networkError != nil {
			if retry == maxRetries {
				return errors.Wrapf(networkError, "settlement failed after %d retries", maxRetries)
			}
			log.Printf("Network error during settlement (retry %d/%d): %v", retry+1, maxRetries, networkError)
			continue // Retry with same settlements
		}
		
		// Log and handle partial settlements
		if len(partialSettlements) > 0 {
			for user, amount := range partialSettlements {
				log.Printf("Partial settlement for user %s: settled %s wei", user.Hex(), amount.String())
			}
		}
		
		// Prepare settlements for next retry (only actual failures)
		var remainingSettlements []contract.TEESettlementData
		allFailureUsers := make(map[common.Address]bool)
		
		// Add static call failures
		for user := range staticCallFailures {
			allFailureUsers[user] = true
		}
		
		// Add actual execution failures
		for user := range actualFailures {
			allFailureUsers[user] = true
		}
		
		// Filter out failed users for next retry
		for _, settlement := range currentSettlements {
			if !allFailureUsers[settlement.User] {
				// Settlement succeeded or was partial (both cases are acceptable)
				continue
			}
			
			// Check if this is a permanent failure that shouldn't be retried
			if staticFailure, exists := staticCallFailures[settlement.User]; exists {
				if c.isPermanentFailure(staticFailure) {
					log.Printf("Permanent failure for user %s: %s - removing from retry queue", 
						settlement.User.Hex(), staticFailure.String())
					continue
				}
			}
			
			// Add to remaining settlements for retry
			remainingSettlements = append(remainingSettlements, settlement)
		}
		
		currentSettlements = remainingSettlements
		
		// If no more retryable settlements, break
		if len(currentSettlements) == 0 {
			break
		}
	}
	
	// Final cleanup: delete settled requests and update state  
	allFailedUsers := c.collectAllFailedUsers(currentSettlements)
	return c.finalizeSettlement(ctx, settlements, userRequestsMap, latestReqCreateAt, allFailedUsers)
}

// previewSettlements performs static calls to preview settlement results and adjusts partial settlements
func (c *Ctrl) previewSettlements(ctx context.Context, settlements []contract.TEESettlementData, userRequestsMap map[string]*UserRequests) ([]contract.TEESettlementData, map[common.Address]SettlementStatus) {
	callOpts := &bind.CallOpts{Context: ctx}
	failures := make(map[common.Address]SettlementStatus)
	var adjustedSettlements []contract.TEESettlementData
	
	log.Printf("Performing static call preview for %d settlements", len(settlements))
	
	// Process settlements in batches for static calls
	for i := 0; i < len(settlements); i += constant.TEESettlementBatchSize {
		end := i + constant.TEESettlementBatchSize
		if end > len(settlements) {
			end = len(settlements)
		}
		
		batch := settlements[i:end]
		batchAdjusted, batchFailures := c.staticCallBatch(ctx, callOpts, batch, userRequestsMap)
		
		adjustedSettlements = append(adjustedSettlements, batchAdjusted...)
		for user, status := range batchFailures {
			failures[user] = status
		}
	}
	
	log.Printf("Static call completed: %d adjusted, %d failed settlements", len(adjustedSettlements), len(failures))
	return adjustedSettlements, failures
}

// staticCallBatch performs a static call on a batch of settlements and adjusts partial cases
func (c *Ctrl) staticCallBatch(ctx context.Context, callOpts *bind.CallOpts, batch []contract.TEESettlementData, userRequestsMap map[string]*UserRequests) ([]contract.TEESettlementData, map[common.Address]SettlementStatus) {
	failures := make(map[common.Address]SettlementStatus)
	
	if !c.canPerformStaticCall() {
		log.Printf("Static call not available, processing all %d settlements", len(batch))
		return batch, failures
	}
	
	// Use the new settlements processor
	processor := NewSettlementsProcessor(c, userRequestsMap)
	result, err := processor.ProcessSettlements(ctx, batch)
	if err != nil {
		log.Printf("Error processing settlements: %v, falling back to original batch", err)
		return batch, failures
	}
	
	// Convert result failures to map format
	for i, user := range result.FailedUsers {
		failures[user] = result.FailureReasons[i]
	}
	
	log.Printf("Settlements processed: %d adjusted, %d failed, %d partial", 
		len(result.AdjustedSettlements), len(result.FailedUsers), len(result.PartialUsers))
	
	return result.AdjustedSettlements, failures
}



// canPerformStaticCall checks if static call functionality is available
func (c *Ctrl) canPerformStaticCall() bool {
	// Enable static call functionality
	// The implementation will use the new previewSettlementResults view function
	return true
}

// staticCallSettleFeesWithTEE performs a static call to preview settlement results
func (c *Ctrl) staticCallSettleFeesWithTEE(opts *bind.CallOpts, settlements []contract.TEESettlementData) (
	failedUsers []common.Address,
	failureReasons []uint8,
	partialUsers []common.Address,
	partialAmounts []*big.Int,
	err error) {
	
	// Use the new previewSettlementResults view function
	log.Printf("Performing static call preview for %d settlements", len(settlements))
	
	// Call the contract's preview function
	result, err := c.contract.Contract.InferenceServing.PreviewSettlementResults(opts, settlements)
	if err != nil {
		log.Printf("Static call failed: %v", err)
		return nil, nil, nil, nil, err
	}
	
	log.Printf("Static call completed: %d failed, %d partial settlements detected", 
		len(result.FailedUsers), len(result.PartialUsers))
	
	return result.FailedUsers, result.FailureReasons, result.PartialUsers, result.PartialAmounts, nil
}


// collectAllFailedUsers collects all users who had permanent failures
func (c *Ctrl) collectAllFailedUsers(failedSettlements []contract.TEESettlementData) []string {
	var failedUserStrings []string
	for _, settlement := range failedSettlements {
		failedUserStrings = append(failedUserStrings, settlement.User.Hex())
	}
	return failedUserStrings
}

// finalizeSettlement performs final cleanup after settlement processing
func (c *Ctrl) finalizeSettlement(ctx context.Context, allSettlements []contract.TEESettlementData, 
	userRequestsMap map[string]*UserRequests, latestReqCreateAt *time.Time, failedUsers []string) error {
	
	log.Printf("Finalizing settlement: %d total settlements, %d failed users", len(allSettlements), len(failedUsers))
	
	// Delete settled requests from database, excluding permanently failed users
	if err := c.db.DeleteSettledRequestsExcludingUsers(latestReqCreateAt, failedUsers); err != nil {
		return errors.Wrap(err, "delete settled requests from db")
	}
	
	// Log failed users for debugging
	if len(failedUsers) > 0 {
		log.Printf("Settlement permanently failed for users: %v", failedUsers)
	}
	
	// Sync user accounts
	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}
	
	// Reset unsettled fees
	return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
}

