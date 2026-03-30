package ctrl

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
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
	// Get service price from cache/contract instead of config
	service, err := c.GetCachedService(ctx)
	if err != nil {
		return errors.Wrap(err, "get cached service for settlement")
	}

	priceSum, err := util.Add(service.InputPrice, service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "calculate price sum")
	}
	threshold := big.NewInt(constant.SettleTriggerThreshold)
	settleTriggerThreshold := new(big.Int).Mul(priceSum, threshold).String()

	// Use the optimized method that calculates unsettled fees with a single query
	accounts, err := c.db.ListUsersWithUnsettledFees(&model.UserListOptions{
		LowBalanceRisk:         model.PtrOf(time.Now().Add(-c.contract.LockTime + c.autoSettleBufferTime)),
		MinUnsettledFee:        model.PtrOf(int64(0)),
		SettleTriggerThreshold: &settleTriggerThreshold,
	})
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}
	if len(accounts) == 0 {
		return nil
	}

	// Extract user addresses that need to be synced
	userAddresses := make([]string, len(accounts))
	for i, acc := range accounts {
		userAddresses[i] = acc.User
	}

	// Verify the available balance in the contract - only sync accounts that need settlement
	if err := c.SyncUserAccountsByAddresses(ctx, userAddresses); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

	// Re-check accounts after sync with current time using optimized query
	accounts, err = c.db.ListUsersWithUnsettledFees(&model.UserListOptions{
		MinUnsettledFee:        model.PtrOf(int64(0)),
		LowBalanceRisk:         model.PtrOf(time.Now()),
		SettleTriggerThreshold: &settleTriggerThreshold,
	})
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db after sync")
	}
	if len(accounts) == 0 {
		return nil
	}

	c.logger.Info("Accounts at risk of having insufficient funds and will be settled immediately with TEE.")
	return errors.Wrap(c.SettleFeesWithTEE(ctx), "settle fees with TEE")
}

// ResetSettlementState clears settling flags and skip_until for all
// pending requests and users. Call this on startup to allow all
// pending requests to be retried immediately.
func (c *Ctrl) ResetSettlementState() error {
	return c.db.ResetSettlementState()
}

// IsTeeSignerAcknowledged checks whether the TEE signer is acknowledged
// on-chain for this provider's service.
func (c *Ctrl) IsTeeSignerAcknowledged(ctx context.Context) bool {
	svc, err := c.contract.GetService(ctx)
	if err != nil {
		c.logger.Warnf("Failed to check TEE signer status: %v", err)
		return false
	}
	return svc.TeeSignerAcknowledged
}

// SettleFeesWithTEE implements the optimized settlement logic.
func (c *Ctrl) SettleFeesWithTEE(ctx context.Context) error {
	// Clear expired skipUntil flags for both requests and users
	if err := c.db.ClearExpiredSkipUntil(); err != nil {
		c.logger.Warnf("Failed to clear expired skipUntil for requests: %v", err)
	}
	if err := c.db.ClearExpiredUserSkipUntil(); err != nil {
		c.logger.Warnf("Failed to clear expired skipUntil for users: %v", err)
	}

	// Prune old requests with zero output
	pruneThreshold := 1 * time.Hour // Prune requests older than 1 hours with zero output
	if err := c.db.PruneRequest(pruneThreshold); err != nil {
		c.logger.Warnf("Failed to prune old zero-output requests: %v", err)
	}

	// Main settlement loop with limited iterations
	const maxSettlementRounds = 1
	for round := 1; round <= maxSettlementRounds; round++ {
		c.logger.Infof("Settlement round %d/%d", round, maxSettlementRounds)
		
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
			c.logger.Infof("No more requests to settle after %d rounds", round)
			return nil
		}

		c.logger.Infof("Processing settlement for %d requests", len(reqs))
		
		// Process settlement batch
		batch, err := c.createSettlementBatch(reqs)
		if err != nil {
			return errors.Wrap(err, "create settlement batch")
		}

		// Execute settlements if we have any
		var execErr error
		if len(batch.ExecutableItems) > 0 {
			execErr = c.executeAndProcessResults(ctx, batch)
			// Don't return immediately — always process outcomes for completed batches
		}

		// Process outcomes (delete/skip requests) — runs even if execution had partial errors
		c.processOutcomes(batch.Outcomes)

		// Sync user balances from contract AFTER settlement execution.
		// Settlement deducts fees on-chain, so the DB lockBalance is now stale
		// (it was synced before settlement). Without this sync, the balance check
		// in ValidateRequestWithEstimatedFee uses the pre-settlement balance,
		// allowing users to accumulate more unsettled fees than their actual balance.
		if len(batch.ExecutableItems) > 0 {
			settledUsers := make([]string, 0)
			for _, outcome := range batch.Outcomes {
				if outcome.Status == SettlementSuccess || outcome.Status == SettlementPartial {
					settledUsers = append(settledUsers, outcome.User.Hex())
				}
			}
			if len(settledUsers) > 0 {
				if err := c.SyncUserAccountsByAddresses(ctx, settledUsers); err != nil {
					c.logger.Warnf("Failed to sync user balances after settlement: %v", err)
				} else {
					c.logger.Infof("Synced %d user balances after settlement", len(settledUsers))
				}
			}
		}

		if execErr != nil {
			return errors.Wrap(execErr, "execute settlement batch")
		}

		// If no executable items, we're done
		if len(batch.ExecutableItems) == 0 {
			c.logger.Infof("No executable settlements remaining after %d rounds", round)
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
			c.logger.Infof("Error creating settlement for user %s: %v", userAddr, err)
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
			
			// Set user-level skip_until since user will have insufficient balance after this settlement
			userSkipUntil := time.Now().Add(constant.SkipUntilDuration)
			if err := c.db.UpdateUserSkipUntil(settlement.User.Hex(), &userSkipUntil); err != nil {
				c.logger.Infof("Error setting skip_until for user %s: %v", settlement.User.Hex(), err)
			} else {
				c.logger.Infof("User %s will have insufficient balance after settlement, skipping until %v", 
					settlement.User.Hex(), userSkipUntil)
			}
			
			// Set outcome based on settlement result
			outcome.AdjustedRequest = adjustedSettlement
			outcome.SettledRequests = settledRequests
			outcome.UnsettledAmount = result.UnsettledAmount
			
			// Mark unsettled requests with skipUntil for forceSettlement
			unsettledRequests := c.getUnsettledRequests(userReqs.Requests, settledRequests)
			c.markRequestsWithSkipUntil(c.getRequestHashes(unsettledRequests), constant.SkipUntilDuration)
			
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

	c.logger.Infof("Batch previewing %d settlements", len(settlements))
	
	// Initialize results for all settlements
	results := make([]*PreviewResult, len(settlements))
	
	// Process in batches to avoid gas limit issues (same as executeBatches)
	for i := 0; i < len(settlements); i += constant.TEESettlementBatchSize {
		end := i + constant.TEESettlementBatchSize
		if end > len(settlements) {
			end = len(settlements)
		}
		
		batch := settlements[i:end]
		c.logger.Infof("Previewing settlement batch %d-%d (size: %d)", i+1, end, len(batch))
		
		result, err := c.contract.Contract.InferenceServing.PreviewSettlementResults(callOpts, batch)
		if err != nil {
			c.logger.Infof("Batch preview settlements failed for batch %d-%d: %v", i+1, end, err)
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
				c.logger.Infof("User %s failed with reason %s", user.Hex(), SettlementStatus(result.FailureReasons[idx]).String())
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
func (c *Ctrl) adjustForPartialSettlement(settlement contract.TEESettlementData, userReqs *UserRequests, unsettledAmount *big.Int) (*contract.TEESettlementData, []*model.Request) {
	settleableAmount := new(big.Int).Sub(settlement.TotalFee, unsettledAmount)
	settledRequests, actualTotalFee := c.getRequestsWithinBudget(userReqs.Requests, settleableAmount)

	// If no actual fee can be settled, return nil to indicate failure
	if actualTotalFee.Cmp(big.NewInt(0)) == 0 {
		return nil, nil
	}

	// Re-create settlement with fresh nonce and signature for the adjusted subset
	adjustedUserReqs := &UserRequests{
		Requests: settledRequests,
		TotalFee: actualTotalFee,
	}
	adjustedSettlement, err := c.createUserSettlement(settlement.User.Hex(), adjustedUserReqs)
	if err != nil {
		c.logger.Errorf("Error re-signing adjusted settlement for user %s: %v", settlement.User.Hex(), err)
		return nil, nil
	}

	return &adjustedSettlement, settledRequests
}

// executeAndProcessResults executes the settlement batch
func (c *Ctrl) executeAndProcessResults(ctx context.Context, batch *SettlementBatch) error {
	if len(batch.ExecutableItems) == 0 {
		return nil
	}

	// Record pending settlements and mark requests as settling BEFORE sending tx
	pendingIDs := c.recordPendingSettlements(ctx, batch)

	// Execute settlements in contract batches
	execResult, execErr := c.executeBatches(ctx, batch.ExecutableItems)

	// Update pending_settlement records with tx hashes
	c.updatePendingTxHashes(pendingIDs, execResult.TxHashes)

	// Update outcomes with execution results
	for _, outcome := range batch.Outcomes {
		if outcome.AdjustedRequest == nil {
			continue // Already marked as failed
		}

		result, hasResult := execResult.Results[outcome.User]
		if !hasResult {
			continue // No failure event — settlement fully succeeded
		}

		switch result.Status {
		case uint8(SettlementPartial):
			// PARTIAL: some funds were deducted on-chain
			settledAmount := new(big.Int).Sub(outcome.AdjustedRequest.TotalFee, result.UnsettledAmount)
			if settledAmount.Cmp(big.NewInt(0)) > 0 && len(outcome.SettledRequests) > 0 {
				settledReqs, _ := c.getRequestsWithinBudget(outcome.SettledRequests, settledAmount)
				outcome.SettledRequests = settledReqs
				outcome.Status = SettlementPartial
				outcome.UnsettledAmount = result.UnsettledAmount
			} else {
				outcome.AdjustedRequest = nil
				outcome.SettledRequests = nil
				outcome.Status = SettlementPartial
			}
		default:
			// Complete failure
			outcome.Status = SettlementStatus(result.Status)
			outcome.AdjustedRequest = nil
			outcome.SettledRequests = nil
		}
	}

	return execErr
}

// pendingSettlementRecord tracks a pending settlement ID and which batch index it belongs to
type pendingSettlementRecord struct {
	ID         uint64
	BatchIndex int // which TEESettlementBatchSize chunk this belongs to
}

// recordPendingSettlements creates pending_settlement records and marks requests as settling
func (c *Ctrl) recordPendingSettlements(ctx context.Context, batch *SettlementBatch) []pendingSettlementRecord {
	currentBlock, err := c.contract.Contract.Client.Client.BlockNumber(ctx)
	if err != nil {
		c.logger.Warnf("Failed to get block number for pending settlement, using 1 as fallback: %v", err)
		currentBlock = 1 // Use 1 instead of 0 so expiry logic still works
	}

	// Map each executable item to its batch index
	executableUserBatch := make(map[common.Address]int)
	for i, item := range batch.ExecutableItems {
		batchIdx := i / constant.TEESettlementBatchSize
		executableUserBatch[item.User] = batchIdx
	}

	var records []pendingSettlementRecord
	for _, outcome := range batch.Outcomes {
		if outcome.AdjustedRequest == nil {
			continue
		}

		requestHashes := c.getRequestHashes(outcome.SettledRequests)
		hashesJSON, err := json.Marshal(requestHashes)
		if err != nil {
			c.logger.Errorf("Failed to marshal request hashes for user %s: %v", outcome.User.Hex(), err)
			continue
		}

		ps := &model.PendingSettlement{
			UserAddress:    outcome.User.Hex(),
			TotalFee:       outcome.AdjustedRequest.TotalFee.String(),
			Nonce:          outcome.AdjustedRequest.Nonce.String(),
			RequestHashes:  string(hashesJSON),
			Status:         "pending",
			SubmittedBlock: currentBlock,
		}
		if err := c.db.CreatePendingSettlement(ps); err != nil {
			c.logger.Warnf("Failed to record pending settlement for user %s: %v", outcome.User.Hex(), err)
			continue
		}

		batchIdx := executableUserBatch[outcome.User]
		records = append(records, pendingSettlementRecord{ID: ps.ID, BatchIndex: batchIdx})

		// Mark requests as settling so they won't be picked up by the next settlement cycle
		if err := c.db.MarkRequestsSettling(requestHashes, true); err != nil {
			c.logger.Warnf("Failed to mark requests as settling for user %s: %v", outcome.User.Hex(), err)
		}
	}
	return records
}

// updatePendingTxHashes updates pending_settlement records with the correct tx hash per batch
func (c *Ctrl) updatePendingTxHashes(records []pendingSettlementRecord, txHashes []common.Hash) {
	if len(records) == 0 || len(txHashes) == 0 {
		return
	}
	for _, rec := range records {
		if rec.BatchIndex < len(txHashes) {
			txHash := txHashes[rec.BatchIndex].Hex()
			if err := c.db.UpdatePendingSettlementTxHash(rec.ID, txHash); err != nil {
				c.logger.Warnf("Failed to update tx hash for pending settlement %d: %v", rec.ID, err)
			}
		}
	}
}

// processOutcomes handles the final outcome processing
func (c *Ctrl) processOutcomes(outcomes []*SettlementOutcome) {
	for _, outcome := range outcomes {
		switch outcome.Status {
		case SettlementSuccess, SettlementPartial:
			if len(outcome.SettledRequests) > 0 {
				if err := c.deleteRequests(outcome.SettledRequests); err != nil {
					c.logger.Errorf("User %s: failed to delete %d settled requests: %v",
						outcome.User.Hex(), len(outcome.SettledRequests), err)
					continue
				}
				c.logger.Infof("User %s: deleted %d settled requests",
					outcome.User.Hex(), len(outcome.SettledRequests))
			}

		case SettlementNoSigner:
			// Permanent failure - delete all requests for this user
			userReqs, err := c.getUserRequestsForAddress(outcome.User.Hex())
			if err != nil {
				c.logger.Infof("Error getting requests for permanent failure user %s: %v", outcome.User.Hex(), err)
			} else if userReqs != nil {
				if err := c.deleteRequests(userReqs.Requests); err != nil {
					c.logger.Errorf("User %s: failed to delete requests for permanent failure: %v",
						outcome.User.Hex(), err)
				} else {
					c.logger.Infof("User %s: deleted %d requests due to permanent failure",
						outcome.User.Hex(), len(userReqs.Requests))
				}
			}

		default:
			// Temporary failure - already handled by skipUntil logic
			c.logger.Infof("User %s: temporary failure %s", outcome.User.Hex(), outcome.Status.String())
		}
	}
}

// Helper functions (simplified and consolidated)

func (c *Ctrl) groupRequestsByUser(reqs []model.Request) map[string]*UserRequests {
	userRequestsMap := make(map[string]*UserRequests)
	
	for _, req := range reqs {
		fee, err := util.ConvertToBigInt(req.Fee)
		if err != nil {
			c.logger.Infof("Error parsing fee for request %s: %v", req.RequestHash, err)
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

	// Create EIP-712 signature
	digest, err := c.createEIP712Digest(settlementData)
	if err != nil {
		return settlementData, errors.Wrap(err, "create EIP-712 digest failed")
	}

	// Use SignEIP712 for EIP-712 typed data (not Sign which is for personal_sign)
	signature, err := c.teeService.SignEIP712(digest)
	if err != nil {
		return settlementData, errors.Wrap(err, "TEE signing failed")
	}

	settlementData.Signature = signature
	return settlementData, nil
}

func (c *Ctrl) getRequestsWithinBudget(requests []*model.Request, budget *big.Int) ([]*model.Request, *big.Int) {
	var result []*model.Request
	remaining := new(big.Int).Set(budget)
	actualTotalFee := big.NewInt(0)
	
	for _, req := range requests {
		fee, err := util.ConvertToBigInt(req.Fee)
		if err != nil {
			continue
		}
		
		if remaining.Cmp(fee) >= 0 {
			result = append(result, req)
			remaining.Sub(remaining, fee)
			actualTotalFee.Add(actualTotalFee, fee)
		} else {
			break
		}
	}
	
	return result, actualTotalFee
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

func (c *Ctrl) deleteRequests(requests []*model.Request) error {
	if len(requests) == 0 {
		return nil
	}

	requestHashes := c.getRequestHashes(requests)
	if err := c.db.DeleteRequestsByHashes(requestHashes); err != nil {
		c.logger.Errorf("CRITICAL: failed to delete settled requests: %v, hashes: %v", err, requestHashes)
		return err
	}
	return nil
}

// executeBatchesResult holds the results of executing settlement batches
type executeBatchesResult struct {
	Results  map[common.Address]*providercontract.SettlementResult
	TxHashes []common.Hash
}

func (c *Ctrl) executeBatches(ctx context.Context, settlements []contract.TEESettlementData) (*executeBatchesResult, error) {
	result := &executeBatchesResult{
		Results: make(map[common.Address]*providercontract.SettlementResult),
	}
	var batchErr error

	for i := 0; i < len(settlements); i += constant.TEESettlementBatchSize {
		end := i + constant.TEESettlementBatchSize
		if end > len(settlements) {
			end = len(settlements)
		}

		batch := settlements[i:end]
		c.logger.Infof("Executing settlement batch %d-%d", i+1, end)

		txHash, batchResults, err := c.contract.SettleFeesWithTEE(ctx, batch)
		if txHash != (common.Hash{}) {
			result.TxHashes = append(result.TxHashes, txHash)
		}
		if err != nil {
			// Mark all users in this and remaining batches as failed
			for j := i; j < len(settlements); j++ {
				result.Results[settlements[j].User] = &providercontract.SettlementResult{
					User:            settlements[j].User,
					Status:          uint8(SettlementPartial),
					UnsettledAmount: settlements[j].TotalFee,
				}
			}
			batchErr = errors.Wrapf(err, "settlement batch %d-%d failed", i, end-1)
			break // Stop sending more batches, but return results so far
		}

		for _, r := range batchResults {
			rCopy := r
			result.Results[r.User] = &rCopy
		}
	}

	return result, batchErr
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
			
			fee, err := util.ConvertToBigInt(req.Fee)
			if err != nil {
				c.logger.Infof("Error parsing fee for request %s: %v", req.RequestHash, err)
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

	c.logger.Infof("Marked %d requests to skip until %v", len(requestHashes), skipUntil)
	return nil
}

// domainSeparator calculates the EIP-712 domain separator
func (c *Ctrl) domainSeparator() (common.Hash, error) {
	chainID, err := c.contract.Contract.Client.Client.ChainID(context.Background())
	if err != nil {
		return common.Hash{}, errors.Wrap(err, "get chain ID")
	}

	// Calculate domain separator: keccak256(abi.encode(DOMAIN_TYPEHASH, keccak256(name), keccak256(version), chainId, verifyingContract))
	domainSep := crypto.Keccak256Hash(
		constant.DomainTypehash.Bytes(),
		crypto.Keccak256([]byte(constant.DomainName)),
		crypto.Keccak256([]byte(constant.DomainVersion)),
		common.LeftPadBytes(chainID.Bytes(), 32),
		common.LeftPadBytes(common.HexToAddress(c.contract.ContractAddress).Bytes(), 32),
	)

	return domainSep, nil
}

// createEIP712Digest creates the EIP-712 digest for a settlement
// Returns: Keccak256(\x19\x01 || domainSeparator || structHash)
func (c *Ctrl) createEIP712Digest(settlement contract.TEESettlementData) ([]byte, error) {
	// Calculate domain separator
	domainSep, err := c.domainSeparator()
	if err != nil {
		return nil, errors.Wrap(err, "calculate domain separator")
	}

	// Calculate struct hash: keccak256(abi.encode(SETTLEMENT_TYPEHASH, requestsHash, nonce, provider, user, totalFee))
	structHash := crypto.Keccak256Hash(
		constant.SettlementTypehash.Bytes(),
		settlement.RequestsHash[:],
		common.LeftPadBytes(settlement.Nonce.Bytes(), 32),
		common.LeftPadBytes(settlement.Provider.Bytes(), 32),
		common.LeftPadBytes(settlement.User.Bytes(), 32),
		common.LeftPadBytes(settlement.TotalFee.Bytes(), 32),
	)

	// Calculate EIP-712 digest: keccak256("\x19\x01" || domainSeparator || structHash)
	digest := crypto.Keccak256(
		[]byte("\x19\x01"),
		domainSep.Bytes(),
		structHash.Bytes(),
	)

	return digest, nil
}