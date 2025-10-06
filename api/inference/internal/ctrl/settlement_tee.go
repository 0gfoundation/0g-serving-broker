package ctrl

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SettlementStatus represents the different states of settlement
// Must match the enum in InferenceServing.sol
type SettlementStatus uint8

const (
	SettlementSuccess        SettlementStatus = 0 // Full settlement success
	SettlementPartial        SettlementStatus = 1 // Partial settlement (insufficient balance)
	SettlementProviderMismatch SettlementStatus = 2 // Provider mismatch
	SettlementNoSigner       SettlementStatus = 3 // TEE signer not acknowledged
	SettlementInvalidNonce   SettlementStatus = 4 // Invalid or duplicate nonce
	SettlementInvalidSig     SettlementStatus = 5 // Signature verification failed
)

// String returns the string representation of SettlementStatus
func (s SettlementStatus) String() string {
	switch s {
	case SettlementSuccess:
		return "SUCCESS"
	case SettlementPartial:
		return "PARTIAL"
	case SettlementProviderMismatch:
		return "PROVIDER_MISMATCH"
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

// UserRequests groups requests for a single user
type UserRequests struct {
	Requests []*model.Request
	TotalFee *big.Int
}

// PreviewResult represents the result of previewing a single settlement
type PreviewResult struct {
	Status          SettlementStatus
	UnsettledAmount *big.Int
}

// SettlementOutcome represents the result for a single user's settlement
type SettlementOutcome struct {
	User            common.Address
	Status          SettlementStatus
	OriginalRequest contract.TEESettlementData
	AdjustedRequest *contract.TEESettlementData // nil if failed completely
	SettledRequests []*model.Request            // requests that were actually settled
	UnsettledAmount *big.Int                    // amount that couldn't be settled (for partial)
}

// SettlementBatch represents a complete settlement operation
type SettlementBatch struct {
	Outcomes        []*SettlementOutcome
	ExecutableItems []contract.TEESettlementData // items that can be sent to contract
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

// SettleFeesWithTEE implements the optimized settlement logic
func (c *Ctrl) SettleFeesWithTEE(ctx context.Context) error {
	// Clear expired skipUntil flags
	if err := c.db.ClearExpiredSkipUntil(); err != nil {
		log.Printf("Warning: failed to clear expired skipUntil: %v", err)
	}

	// Prune old requests with zero output
	pruneThreshold := 1 * time.Hour // Prune requests older than 1 hours with zero output
	if err := c.db.PruneRequest(pruneThreshold); err != nil {
		log.Printf("Warning: failed to prune old zero-output requests: %v", err)
	}

	// Main settlement loop with limited iterations
	const maxSettlementRounds = 10
	for round := 1; round <= maxSettlementRounds; round++ {
		log.Printf("Settlement round %d/%d", round, maxSettlementRounds)
		
		// Get unprocessed requests (excluding those with active skipUntil)
		reqs, _, err := c.db.ListRequest(model.RequestListOptions{
			Processed:             false,
			Sort:                  model.PtrOf("created_at ASC"),
			ExcludeZeroOutput:     true,
			IncludeSkipped:        false,
		})
		if err != nil {
			return errors.Wrap(err, "list request from db")
		}
		
		if len(reqs) == 0 {
			log.Printf("No more requests to settle after %d rounds", round)
			return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
		}

		log.Printf("Processing settlement for %d requests", len(reqs))
		
		// Process settlement batch
		batch, err := c.createSettlementBatch(reqs)
		if err != nil {
			return errors.Wrap(err, "create settlement batch")
		}

		// Execute settlements if we have any
		if len(batch.ExecutableItems) > 0 {
			err = c.executeAndProcessResults(ctx, batch)
			if err != nil {
				return errors.Wrap(err, "execute settlement batch")
			}
		}

		// Process outcomes (delete/skip requests)
		c.processOutcomes(batch.Outcomes)

		// If no executable items, we're done
		if len(batch.ExecutableItems) == 0 {
			log.Printf("No executable settlements remaining after %d rounds", round)
			break
		}
	}

	return nil
}

// createSettlementBatch creates a batch with preview and adjustment
func (c *Ctrl) createSettlementBatch(reqs []model.Request) (*SettlementBatch, error) {
	// Group requests by user
	userRequestsMap := c.groupRequestsByUser(reqs)
	
	// Create initial settlements for all users
	var settlements []contract.TEESettlementData
	userSettlementMap := make(map[common.Address]*UserRequests)
	
	for userAddr, userReqs := range userRequestsMap {
		settlement, err := c.createUserSettlement(userAddr, userReqs)
		if err != nil {
			log.Printf("Error creating settlement for user %s: %v", userAddr, err)
			continue
		}
		settlements = append(settlements, settlement)
		userSettlementMap[settlement.User] = userReqs
	}

	// Batch preview all settlements at once
	previewResults, err := c.batchPreviewSettlements(settlements)
	if err != nil {
		return nil, errors.Wrap(err, "batch preview settlements")
	}

	// Process results and create outcomes
	outcomes := make([]*SettlementOutcome, 0, len(settlements))
	
	for i, settlement := range settlements {
		userReqs := userSettlementMap[settlement.User]
		result := previewResults[i]
		
		outcome := &SettlementOutcome{
			User:            settlement.User,
			OriginalRequest: settlement,
			Status:          result.Status,
		}

		switch result.Status {
		case SettlementSuccess:
			// Full settlement - all requests will be settled
			outcome.AdjustedRequest = &settlement
			outcome.SettledRequests = userReqs.Requests
			
		case SettlementPartial:
			// Partial settlement - adjust and split requests
			adjustedSettlement, settledRequests := c.adjustForPartialSettlement(settlement, userReqs, result.UnsettledAmount)
			outcome.AdjustedRequest = &adjustedSettlement
			outcome.SettledRequests = settledRequests
			outcome.UnsettledAmount = result.UnsettledAmount
			
			// Mark unsettled requests with skipUntil
			unsettledRequests := c.getUnsettledRequests(userReqs.Requests, settledRequests)
			c.markRequestsWithSkipUntil(c.getRequestHashes(unsettledRequests), 5*time.Minute)
			
		default:
			// Failed settlement - no adjustment needed
			outcome.UnsettledAmount = settlement.TotalFee
		}

		outcomes = append(outcomes, outcome)
	}

	// Create executable items (only successful and partial settlements)
	var executableItems []contract.TEESettlementData
	for _, outcome := range outcomes {
		if outcome.AdjustedRequest != nil {
			executableItems = append(executableItems, *outcome.AdjustedRequest)
		}
	}

	return &SettlementBatch{
		Outcomes:        outcomes,
		ExecutableItems: executableItems,
	}, nil
}

// batchPreviewSettlements previews multiple settlements using batching to avoid gas limit issues
func (c *Ctrl) batchPreviewSettlements(settlements []contract.TEESettlementData) ([]*PreviewResult, error) {
	if len(settlements) == 0 {
		return []*PreviewResult{}, nil
	}

	// Use the contract's preview function for accurate prediction
	callOpts := &bind.CallOpts{
		Context: context.Background(),
		From:    common.HexToAddress(c.contract.ProviderAddress),
	}

	log.Printf("Batch previewing %d settlements", len(settlements))
	
	// Initialize results for all settlements
	results := make([]*PreviewResult, len(settlements))
	
	// Process in batches to avoid gas limit issues (same as executeBatches)
	for i := 0; i < len(settlements); i += constant.TEESettlementBatchSize {
		end := i + constant.TEESettlementBatchSize
		if end > len(settlements) {
			end = len(settlements)
		}
		
		batch := settlements[i:end]
		log.Printf("Previewing settlement batch %d-%d (size: %d)", i+1, end, len(batch))
		
		result, err := c.contract.Contract.InferenceServing.PreviewSettlementResults(callOpts, batch)
		if err != nil {
			log.Printf("Batch preview settlements failed for batch %d-%d: %v", i+1, end, err)
			// Default this batch to failure on error
			for j := i; j < end; j++ {
				results[j] = &PreviewResult{
					Status:          SettlementPartial,
					UnsettledAmount: settlements[j].TotalFee,
				}
			}
			continue // Continue with next batch even if this one fails
		}

		// Create result maps for easier lookup for this batch
		failureMap := make(map[common.Address]SettlementStatus)
		for idx, user := range result.FailedUsers {
			if idx < len(result.FailureReasons) {
				log.Printf("User %s failed with reason %s", user.Hex(), SettlementStatus(result.FailureReasons[idx]).String())
				failureMap[user] = SettlementStatus(result.FailureReasons[idx])
			}
		}

		partialMap := make(map[common.Address]*big.Int)
		for idx, user := range result.PartialUsers {
			if idx < len(result.PartialAmounts) {
				partialMap[user] = result.PartialAmounts[idx]
			}
		}

		// Process results for each settlement in this batch
		for j := 0; j < len(batch); j++ {
			settlementIdx := i + j
			settlement := settlements[settlementIdx]
			
			if status, isFailed := failureMap[settlement.User]; isFailed {
				results[settlementIdx] = &PreviewResult{
					Status:          status,
					UnsettledAmount: settlement.TotalFee,
				}
			} else if unsettledAmount, isPartial := partialMap[settlement.User]; isPartial {
				results[settlementIdx] = &PreviewResult{
					Status:          SettlementPartial,
					UnsettledAmount: unsettledAmount,
				}
			} else {
				results[settlementIdx] = &PreviewResult{
					Status:          SettlementSuccess,
					UnsettledAmount: big.NewInt(0),
				}
			}
		}
	}

	return results, nil
}

// adjustForPartialSettlement adjusts settlement for partial payment
func (c *Ctrl) adjustForPartialSettlement(settlement contract.TEESettlementData, userReqs *UserRequests, unsettledAmount *big.Int) (contract.TEESettlementData, []*model.Request) {
	settleableAmount := new(big.Int).Sub(settlement.TotalFee, unsettledAmount)
	settledRequests := c.getRequestsWithinBudget(userReqs.Requests, settleableAmount)
	
	// Calculate actual total fee of settled requests
	actualTotalFee := big.NewInt(0)
	for _, req := range settledRequests {
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			log.Printf("Error parsing fee for request %s: %v", req.RequestHash, err)
			continue
		}
		actualTotalFee.Add(actualTotalFee, fee)
	}

	// Create adjusted settlement
	adjustedSettlement := settlement
	adjustedSettlement.TotalFee = actualTotalFee
	adjustedSettlement.RequestsHash = c.hashUserRequests(settledRequests)

	return adjustedSettlement, settledRequests
}

// executeAndProcessResults executes the settlement batch
func (c *Ctrl) executeAndProcessResults(ctx context.Context, batch *SettlementBatch) error {
	if len(batch.ExecutableItems) == 0 {
		return nil
	}

	// Execute settlements in contract batches
	actualFailures, err := c.executeBatches(ctx, batch.ExecutableItems)
	if err != nil {
		return errors.Wrap(err, "execute contract batches")
	}

	// Update outcomes with execution results
	for _, outcome := range batch.Outcomes {
		if outcome.AdjustedRequest == nil {
			continue // Already marked as failed
		}

		if _, failed := actualFailures[outcome.User]; failed {
			// Execution failed - revert to failed status
			outcome.Status = SettlementPartial // insufficient balance or other failure
			outcome.AdjustedRequest = nil
			outcome.SettledRequests = nil
		}
	}

	return nil
}

// processOutcomes handles the final outcome processing
func (c *Ctrl) processOutcomes(outcomes []*SettlementOutcome) {
	for _, outcome := range outcomes {
		switch outcome.Status {
		case SettlementSuccess, SettlementPartial:
			if len(outcome.SettledRequests) > 0 {
				// Delete successfully settled requests
				c.deleteRequests(outcome.SettledRequests)
				log.Printf("User %s: deleted %d settled requests", 
					outcome.User.Hex(), len(outcome.SettledRequests))
			}
			
		case SettlementNoSigner:
			// Permanent failure - delete all requests for this user
			// For permanent failures, we should delete all user's requests (not just settled ones)
			userReqs, err := c.getUserRequestsForAddress(outcome.User.Hex())
			if err != nil {
				log.Printf("Error getting requests for permanent failure user %s: %v", outcome.User.Hex(), err)
			} else if userReqs != nil {
				c.deleteRequests(userReqs.Requests)
				log.Printf("User %s: deleted %d requests due to permanent failure", 
					outcome.User.Hex(), len(userReqs.Requests))
			}
			
		default:
			// Temporary failure - already handled by skipUntil logic
			log.Printf("User %s: temporary failure %s", outcome.User.Hex(), outcome.Status.String())
		}
	}
}

// Helper functions (simplified and consolidated)

func (c *Ctrl) groupRequestsByUser(reqs []model.Request) map[string]*UserRequests {
	userRequestsMap := make(map[string]*UserRequests)
	
	for _, req := range reqs {
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			log.Printf("Error parsing fee for request %s: %v", req.RequestHash, err)
			continue
		}

		reqCopy := req
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

func (c *Ctrl) createUserSettlement(userAddr string, userReqs *UserRequests) (contract.TEESettlementData, error) {
	requestsHash := c.hashUserRequests(userReqs.Requests)
	nonce := big.NewInt(time.Now().Unix())
	nonce.Mul(nonce, big.NewInt(10000000))

	settlementData := contract.TEESettlementData{
		User:         common.HexToAddress(userAddr),
		Provider:     common.HexToAddress(c.contract.ProviderAddress),
		TotalFee:     userReqs.TotalFee,
		RequestsHash: requestsHash,
		Nonce:        nonce,
	}

	// Sign with TEE service
	messageHash := crypto.Keccak256(
		requestsHash[:],
		common.LeftPadBytes(nonce.Bytes(), 32),
		settlementData.Provider.Bytes(),
		settlementData.User.Bytes(),
		common.LeftPadBytes(userReqs.TotalFee.Bytes(), 32),
	)

	signature, err := c.teeService.Sign(messageHash)
	if err != nil {
		return settlementData, errors.Wrap(err, "TEE signing failed")
	}

	settlementData.Signature = signature
	return settlementData, nil
}

func (c *Ctrl) getRequestsWithinBudget(requests []*model.Request, budget *big.Int) []*model.Request {
	var result []*model.Request
	remaining := new(big.Int).Set(budget)
	
	for _, req := range requests {
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			continue
		}
		
		if remaining.Cmp(fee) >= 0 {
			result = append(result, req)
			remaining.Sub(remaining, fee)
		} else {
			break
		}
	}
	
	return result
}

func (c *Ctrl) getUnsettledRequests(allRequests, settledRequests []*model.Request) []*model.Request {
	settledMap := make(map[string]bool)
	for _, req := range settledRequests {
		settledMap[req.RequestHash] = true
	}
	
	var unsettled []*model.Request
	for _, req := range allRequests {
		if !settledMap[req.RequestHash] {
			unsettled = append(unsettled, req)
		}
	}
	
	return unsettled
}

func (c *Ctrl) getRequestHashes(requests []*model.Request) []string {
	hashes := make([]string, len(requests))
	for i, req := range requests {
		hashes[i] = req.RequestHash
	}
	return hashes
}

func (c *Ctrl) deleteRequests(requests []*model.Request) {
	if len(requests) == 0 {
		return
	}
	
	requestHashes := c.getRequestHashes(requests)
	err := c.db.DeleteRequestsByHashes(requestHashes)
	if err != nil {
		log.Printf("Error deleting requests: %v", err)
	}
}

func (c *Ctrl) executeBatches(ctx context.Context, settlements []contract.TEESettlementData) (map[common.Address]SettlementStatus, error) {
	failures := make(map[common.Address]SettlementStatus)
	
	// Process in batches
	for i := 0; i < len(settlements); i += constant.TEESettlementBatchSize {
		end := i + constant.TEESettlementBatchSize
		if end > len(settlements) {
			end = len(settlements)
		}
		
		batch := settlements[i:end]
		log.Printf("Executing settlement batch %d-%d", i+1, end)
		
		failedUsers, err := c.contract.SettleFeesWithTEE(ctx, batch)
		if err != nil {
			return failures, errors.Wrapf(err, "settlement batch %d-%d failed", i, end-1)
		}
		
		for _, user := range failedUsers {
			failures[user] = SettlementPartial
		}
	}
	
	return failures, nil
}

// getUserRequestsForAddress gets all unprocessed requests for a specific user
func (c *Ctrl) getUserRequestsForAddress(userAddress string) (*UserRequests, error) {
	// Query database for all unprocessed requests for this user
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed:         false,
		IncludeSkipped:    true, // Include skipped requests for permanent failures
		Sort:              model.PtrOf("created_at ASC"),
	})
	if err != nil {
		return nil, errors.Wrap(err, "list requests for user")
	}

	// Filter for this specific user and calculate total fee
	var userRequests []*model.Request
	totalFee := big.NewInt(0)
	
	for _, req := range reqs {
		if req.UserAddress == userAddress {
			reqCopy := req
			userRequests = append(userRequests, &reqCopy)
			
			fee, err := util.HexadecimalStringToBigInt(req.Fee)
			if err != nil {
				log.Printf("Error parsing fee for request %s: %v", req.RequestHash, err)
				continue
			}
			totalFee.Add(totalFee, fee)
		}
	}

	if len(userRequests) == 0 {
		return nil, nil
	}

	return &UserRequests{
		Requests: userRequests,
		TotalFee: totalFee,
	}, nil
}

// Other required methods (from original file)

func (c *Ctrl) hashUserRequests(requests []*model.Request) [32]byte {
	var requestData []byte
	for _, req := range requests {
		requestData = append(requestData, []byte(req.RequestHash)...)
		requestData = append(requestData, []byte(req.UserAddress)...)
		requestData = append(requestData, []byte(req.Fee)...)
		requestData = append(requestData, []byte(req.InputFee)...)
		requestData = append(requestData, []byte(req.OutputFee)...)
	}
	return crypto.Keccak256Hash(requestData)
}

func (c *Ctrl) isPermanentFailure(status SettlementStatus) bool {
	return status == SettlementNoSigner
}

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