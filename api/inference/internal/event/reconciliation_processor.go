package event

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/util"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

const (
	// DefaultConfirmationBlocks is the number of blocks to wait before considering a block finalized.
	DefaultConfirmationBlocks = uint64(12)
	// DefaultExpiryBlocks is the number of blocks after which a pending settlement without a matching
	// on-chain event is considered expired and its requests are released.
	DefaultExpiryBlocks = uint64(200)
	// MaxScanBlockRange caps the block window passed to a single FilterTEESettlementResult call.
	// Public RPC nodes typically reject queries whose result set exceeds 10,000 logs; this
	// value must be small enough that no single window can produce that many events even on
	// high-traffic days. At ~1000 settlements/day across ~90,000 blocks/day, 50k blocks bounds
	// any single call to a few hundred logs with comfortable headroom.
	MaxScanBlockRange = uint64(50000)
)

// ReconciliationProcessor scans on-chain TEESettlementResult events and reconciles
// them against pending_settlement records. This acts as a safety net behind the
// primary receipt-based settlement flow.
type ReconciliationProcessor struct {
	db       *db.DB
	contract *providercontract.ProviderContract
	logger   log.Logger

	interval           int    // seconds between scans
	confirmationBlocks uint64 // blocks to wait for finality
	expiryBlocks       uint64 // blocks after which a pending settlement is expired
}

// NewReconciliationProcessor creates a processor that periodically scans on-chain settlement
// events and reconciles them against local pending_settlement records.
func NewReconciliationProcessor(
	db *db.DB,
	contract *providercontract.ProviderContract,
	interval int,
	logger log.Logger,
) *ReconciliationProcessor {
	if interval <= 0 {
		interval = 60
	}
	return &ReconciliationProcessor{
		db:                 db,
		contract:           contract,
		logger:             logger,
		interval:           interval,
		confirmationBlocks: DefaultConfirmationBlocks,
		expiryBlocks:       DefaultExpiryBlocks,
	}
}

// Start implements the controller-runtime Runnable interface
func (r *ReconciliationProcessor) Start(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(r.interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *ReconciliationProcessor) reconcile(ctx context.Context) {
	// 1. Get cursor
	cursor, err := r.db.GetReconciliationCursor()
	if err != nil {
		r.logger.Errorf("Reconciliation: failed to get cursor: %v", err)
		return
	}

	// 2. Get current block
	currentBlock, err := r.contract.Contract.Client.Client.BlockNumber(ctx)
	if err != nil {
		r.logger.Errorf("Reconciliation: failed to get current block: %v", err)
		return
	}

	// 3. Calculate safe block (with confirmation buffer)
	if currentBlock <= r.confirmationBlocks {
		return
	}
	safeBlock := currentBlock - r.confirmationBlocks
	if safeBlock <= cursor.LastBlockNumber {
		return // Nothing new to process
	}

	// 4-5. Scan and process events in chunks bounded by MaxScanBlockRange so a
	// single call cannot exceed the RPC's per-query log limit. Advance the cursor
	// after every successful chunk so progress survives mid-loop failures.
	fromBlock := cursor.LastBlockNumber + 1
	for fromBlock <= safeBlock {
		if ctx.Err() != nil {
			return
		}
		toBlock := fromBlock + MaxScanBlockRange - 1
		if toBlock > safeBlock {
			toBlock = safeBlock
		}

		events, err := r.scanEvents(ctx, fromBlock, toBlock)
		if err != nil {
			r.logger.Errorf("Reconciliation: failed to scan events from block %d to %d: %v", fromBlock, toBlock, err)
			return
		}

		if len(events) > 0 {
			r.logger.Infof("Reconciliation: processing %d events from block %d to %d", len(events), fromBlock, toBlock)
		}

		for _, evt := range events {
			r.processEvent(evt)
		}

		if err := r.db.UpdateReconciliationCursor(toBlock); err != nil {
			r.logger.Errorf("Reconciliation: failed to update cursor to block %d: %v", toBlock, err)
			return
		}

		fromBlock = toBlock + 1
	}

	// 6. Expire stale pending settlements once per tick, after all chunks scanned.
	if safeBlock > r.expiryBlocks {
		r.expireStaleSettlements(safeBlock - r.expiryBlocks)
	}
}

func (r *ReconciliationProcessor) scanEvents(ctx context.Context, fromBlock, toBlock uint64) ([]settlementEvent, error) {
	filterOpts := &bind.FilterOpts{
		Start:   fromBlock,
		End:     &toBlock,
		Context: ctx,
	}

	iter, err := r.contract.Contract.FilterTEESettlementResult(filterOpts, nil)
	if err != nil {
		return nil, fmt.Errorf("filter TEESettlementResult events from block %d to %d: %w", fromBlock, toBlock, err)
	}
	defer iter.Close()

	var events []settlementEvent
	for iter.Next() {
		events = append(events, settlementEvent{
			User:            iter.Event.User,
			Status:          iter.Event.Status,
			UnsettledAmount: iter.Event.UnsettledAmount,
			TxHash:          iter.Event.Raw.TxHash,
		})
	}
	return events, iter.Error()
}

type settlementEvent struct {
	User            common.Address
	Status          uint8
	UnsettledAmount *big.Int
	TxHash          common.Hash
}

func (r *ReconciliationProcessor) processEvent(evt settlementEvent) {
	txHash := evt.TxHash.Hex()
	userAddr := evt.User.Hex()

	// Find matching pending settlement
	pending, err := r.db.FindPendingSettlementByTxHash(txHash, userAddr)
	if err != nil {
		r.logger.Errorf("Reconciliation: error finding pending settlement for tx %s user %s: %v", txHash, userAddr, err)
		return
	}
	if pending == nil {
		return // No matching record -- already handled by Layer 1 or not our tx
	}

	var requestHashes []string
	if err := json.Unmarshal([]byte(pending.RequestHashes), &requestHashes); err != nil {
		r.logger.Errorf("Reconciliation: failed to unmarshal request hashes for pending %d: %v", pending.ID, err)
		return
	}

	switch evt.Status {
	case 0: // SUCCESS
		// Delete requests (idempotent -- Layer 1 may have already done this)
		if err := r.db.DeleteRequestsByHashesIfExist(requestHashes); err != nil {
			r.logger.Errorf("Reconciliation: failed to delete requests for tx %s: %v", txHash, err)
			return
		}
		if err := r.db.UpdatePendingSettlementStatus(pending.ID, "confirmed"); err != nil {
			r.logger.Errorf("Reconciliation: failed to update status for pending %d: %v", pending.ID, err)
		}
		r.logger.Infof("Reconciliation: confirmed settlement for user %s (tx %s), %d requests",
			userAddr, txHash, len(requestHashes))

	case 1: // PARTIAL
		r.handlePartialEvent(pending, requestHashes, evt.UnsettledAmount)
		if err := r.db.UpdatePendingSettlementStatus(pending.ID, "confirmed"); err != nil {
			r.logger.Errorf("Reconciliation: failed to update status for pending %d: %v", pending.ID, err)
		}
		r.logger.Infof("Reconciliation: confirmed partial settlement for user %s (tx %s)", userAddr, txHash)

	default: // FAILURE (status 2-5)
		// Clear settling state so requests become available again
		if err := r.db.MarkRequestsSettling(requestHashes, false); err != nil {
			r.logger.Errorf("Reconciliation: failed to clear settling state for tx %s: %v", txHash, err)
			return
		}
		if err := r.db.UpdatePendingSettlementStatus(pending.ID, "failed"); err != nil {
			r.logger.Errorf("Reconciliation: failed to update status for pending %d: %v", pending.ID, err)
		}
		r.logger.Infof("Reconciliation: settlement failed for user %s (tx %s, status %d)",
			userAddr, txHash, evt.Status)
	}
}

func (r *ReconciliationProcessor) handlePartialEvent(
	pending *model.PendingSettlement,
	requestHashes []string,
	unsettledAmount *big.Int,
) {
	totalFee, ok := new(big.Int).SetString(pending.TotalFee, 10)
	if !ok {
		r.logger.Errorf("Reconciliation: invalid totalFee %s for pending %d", pending.TotalFee, pending.ID)
		return
	}
	settledAmount := new(big.Int).Sub(totalFee, unsettledAmount)

	// Load requests that still exist in DB
	requests, err := r.db.GetRequestsByHashes(requestHashes)
	if err != nil || len(requests) == 0 {
		return // Already handled
	}

	// Determine which requests fit within the settled amount
	accumulated := big.NewInt(0)
	var settledHashes, unsettledHashes []string

	for _, req := range requests {
		fee, feeErr := util.ConvertToBigInt(req.Fee)
		if feeErr != nil {
			unsettledHashes = append(unsettledHashes, req.RequestHash)
			continue
		}
		if new(big.Int).Add(accumulated, fee).Cmp(settledAmount) <= 0 {
			accumulated.Add(accumulated, fee)
			settledHashes = append(settledHashes, req.RequestHash)
		} else {
			unsettledHashes = append(unsettledHashes, req.RequestHash)
		}
	}

	// Delete settled requests, clear settling flag for unsettled ones
	if len(settledHashes) > 0 {
		if err := r.db.DeleteRequestsByHashesIfExist(settledHashes); err != nil {
			r.logger.Errorf("Reconciliation: failed to delete settled requests for pending %d: %v", pending.ID, err)
		}
	}
	if len(unsettledHashes) > 0 {
		if err := r.db.MarkRequestsSettling(unsettledHashes, false); err != nil {
			r.logger.Errorf("Reconciliation: failed to clear settling for unsettled requests for pending %d: %v", pending.ID, err)
		}
	}
}

func (r *ReconciliationProcessor) expireStaleSettlements(beforeBlock uint64) {
	stale, err := r.db.FindStalePendingSettlements(beforeBlock)
	if err != nil {
		r.logger.Errorf("Reconciliation: failed to find stale settlements: %v", err)
		return
	}

	for _, ps := range stale {
		var requestHashes []string
		if err := json.Unmarshal([]byte(ps.RequestHashes), &requestHashes); err != nil {
			r.logger.Errorf("Reconciliation: failed to unmarshal hashes for stale pending %d: %v", ps.ID, err)
			continue
		}

		if err := r.db.MarkRequestsSettling(requestHashes, false); err != nil {
			r.logger.Errorf("Reconciliation: failed to clear settling for stale pending %d: %v", ps.ID, err)
			continue
		}

		if err := r.db.UpdatePendingSettlementStatus(ps.ID, "expired"); err != nil {
			r.logger.Errorf("Reconciliation: failed to update status for stale pending %d: %v", ps.ID, err)
			continue
		}
		r.logger.Warnf("Reconciliation: expired stale settlement for user %s (submitted block %d, threshold %d)",
			ps.UserAddress, ps.SubmittedBlock, beforeBlock)
	}
}
