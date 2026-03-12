package model

import "time"

type AdapterState string

const (
	AdapterStateActive    AdapterState = "active"    // Deployed in ServerlessLLM, ready to serve
	AdapterStateLoading   AdapterState = "loading"   // Being downloaded/deployed
	AdapterStateOffloaded AdapterState = "offloaded"  // Removed from ServerlessLLM, files may be on disk
	AdapterStateArchived  AdapterState = "archived"   // Files removed from disk, only in 0G Storage
	AdapterStateFailed    AdapterState = "failed"     // Deployment failed
)

// LoRAAdapter represents a fine-tuned LoRA adapter managed by the inference broker.
// Persisted to local DB for crash recovery.
type LoRAAdapter struct {
	Model
	TaskID          string       `json:"taskId" gorm:"type:varchar(255);not null;uniqueIndex"`
	UserAddress     string       `json:"userAddress" gorm:"type:varchar(255);not null;index"`
	BaseModel       string       `json:"baseModel" gorm:"type:varchar(255);not null"`
	AdapterName     string       `json:"adapterName" gorm:"type:varchar(255);not null;uniqueIndex"`
	StorageRootHash string       `json:"storageRootHash" gorm:"type:text;not null"`
	State           AdapterState `json:"state" gorm:"type:varchar(32);not null;default:'loading'"`
	LastAccessAt    *time.Time   `json:"lastAccessAt" gorm:"type:datetime"`
	AdapterPath     string       `json:"adapterPath" gorm:"type:varchar(512)"`
	DeployedAt      *time.Time   `json:"deployedAt" gorm:"type:datetime"`
	BlockNumber     uint64       `json:"blockNumber" gorm:"type:bigint unsigned;not null;default:0"`
}
