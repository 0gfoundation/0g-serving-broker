package model

import "time"

// PendingSettlement tracks settlement transactions sent to the blockchain.
// Used by the reconciliation system to verify on-chain results against local state.
type PendingSettlement struct {
	Model
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TxHash         string     `gorm:"type:varchar(66);index" json:"txHash"`
	UserAddress    string     `gorm:"type:varchar(255);not null;index" json:"userAddress"`
	TotalFee       string     `gorm:"type:varchar(255);not null" json:"totalFee"`
	Nonce          string     `gorm:"type:varchar(255);not null" json:"nonce"`
	RequestHashes  string     `gorm:"type:text;not null" json:"requestHashes"`
	Status         string     `gorm:"type:varchar(32);not null;default:'pending';index" json:"status"`
	SubmittedBlock uint64     `gorm:"type:bigint unsigned;not null;default:0" json:"submittedBlock"`
	ResolvedAt     *time.Time `gorm:"type:datetime" json:"resolvedAt,omitempty"`
}

// ReconciliationCursor tracks the last processed block for event scanning.
type ReconciliationCursor struct {
	ID              uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	LastBlockNumber uint64     `gorm:"type:bigint unsigned;not null;default:0" json:"lastBlockNumber"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
}
