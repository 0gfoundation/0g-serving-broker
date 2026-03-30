package db

import (
	"fmt"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// CreatePendingSettlement creates a new pending settlement record
func (d *DB) CreatePendingSettlement(ps *model.PendingSettlement) error {
	return d.db.Create(ps).Error
}

// UpdatePendingSettlementTxHash sets the tx hash after the transaction is submitted
func (d *DB) UpdatePendingSettlementTxHash(id uint64, txHash string) error {
	return d.db.Model(&model.PendingSettlement{}).
		Where("id = ?", id).
		Update("tx_hash", txHash).Error
}

// FindPendingSettlementByTxHash finds a pending settlement by tx hash and user.
// Returns (nil, nil) if no matching record is found — this is expected for events
// already handled by the primary (Layer 1) settlement flow.
func (d *DB) FindPendingSettlementByTxHash(txHash string, userAddress string) (*model.PendingSettlement, error) {
	var ps model.PendingSettlement
	err := d.db.Session(&gorm.Session{Logger: d.db.Logger.LogMode(logger.Silent)}).
		Where("tx_hash = ? AND user_address = ? AND status = ?", txHash, userAddress, "pending").
		First(&ps).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find pending settlement by tx hash %s user %s: %w", txHash, userAddress, err)
	}
	return &ps, nil
}

// FindStalePendingSettlements finds pending settlements submitted before the given block
func (d *DB) FindStalePendingSettlements(beforeBlock uint64) ([]model.PendingSettlement, error) {
	var results []model.PendingSettlement
	err := d.db.Where("status = ? AND submitted_block <= ?", "pending", beforeBlock).
		Find(&results).Error
	if err != nil {
		return nil, fmt.Errorf("find stale pending settlements before block %d: %w", beforeBlock, err)
	}
	return results, nil
}

// UpdatePendingSettlementStatus updates the status of a pending settlement
func (d *DB) UpdatePendingSettlementStatus(id uint64, status string) error {
	now := time.Now()
	return d.db.Model(&model.PendingSettlement{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":      status,
			"resolved_at": &now,
		}).Error
}

// ResetSettlementState clears settling flags and skip_until for all unprocessed requests
// and all user-level skip_until flags.
// This should be called on startup to recover from incomplete settlements
// and allow all pending requests to be retried immediately.
func (d *DB) ResetSettlementState() error {
	if err := d.db.Model(&model.Request{}).
		Where("processed = ?", false).
		Where("settling = ? OR skip_until IS NOT NULL", true).
		Updates(map[string]interface{}{
			"settling":   false,
			"skip_until": nil,
		}).Error; err != nil {
		return err
	}

	return d.db.Model(&model.User{}).
		Where("skip_until IS NOT NULL").
		Update("skip_until", nil).Error
}

// MarkRequestsSettling sets or clears the settling flag for requests
func (d *DB) MarkRequestsSettling(requestHashes []string, settling bool) error {
	if len(requestHashes) == 0 {
		return nil
	}
	return d.db.Model(&model.Request{}).
		Where("request_hash IN ?", requestHashes).
		Update("settling", settling).Error
}

// DeleteRequestsByHashesIfExist deletes requests by hashes, ignoring already-deleted ones (idempotent)
func (d *DB) DeleteRequestsByHashesIfExist(requestHashes []string) error {
	if len(requestHashes) == 0 {
		return nil
	}
	return d.db.Where("request_hash IN ?", requestHashes).Delete(&model.Request{}).Error
}

// GetReconciliationCursor returns the current reconciliation cursor
func (d *DB) GetReconciliationCursor() (*model.ReconciliationCursor, error) {
	var cursor model.ReconciliationCursor
	err := d.db.First(&cursor).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// Create initial cursor
			cursor = model.ReconciliationCursor{LastBlockNumber: 0}
			if createErr := d.db.Create(&cursor).Error; createErr != nil {
				return nil, fmt.Errorf("create initial reconciliation cursor: %w", createErr)
			}
			return &cursor, nil
		}
		return nil, fmt.Errorf("get reconciliation cursor: %w", err)
	}
	return &cursor, nil
}

// UpdateReconciliationCursor updates the cursor to the given block number
func (d *DB) UpdateReconciliationCursor(blockNumber uint64) error {
	now := time.Now()
	return d.db.Model(&model.ReconciliationCursor{}).
		Where("1 = 1").
		Updates(map[string]interface{}{
			"last_block_number": blockNumber,
			"updated_at":        &now,
		}).Error
}

// GetRequestsByHashes returns requests matching the given hashes
func (d *DB) GetRequestsByHashes(requestHashes []string) ([]model.Request, error) {
	if len(requestHashes) == 0 {
		return nil, nil
	}
	var requests []model.Request
	if err := d.db.Where("request_hash IN ?", requestHashes).Find(&requests).Error; err != nil {
		return nil, fmt.Errorf("get requests by hashes: %w", err)
	}
	return requests, nil
}
