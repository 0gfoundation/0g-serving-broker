package model

import "time"

type AdapterState string

const (
	AdapterStateActive    AdapterState = "active"    // Deployed in ServerlessLLM, ready to serve
	AdapterStateReady     AdapterState = "ready"     // Downloaded and decrypted, awaiting user-triggered deploy
	AdapterStateLoading   AdapterState = "loading"   // Being downloaded/decrypted
	AdapterStateOffloaded AdapterState = "offloaded" // Removed from ServerlessLLM, files may be on disk
	AdapterStateArchived  AdapterState = "archived"  // Files removed from disk, only in 0G Storage
	AdapterStateFailed    AdapterState = "failed"    // Download or deployment failed
)

// LoRAAdapter represents a fine-tuned LoRA adapter managed by the inference broker.
// Persisted to local DB for crash recovery.
type LoRAAdapter struct {
	Model
	TaskID          string       `json:"taskId" gorm:"type:varchar(255);not null;uniqueIndex"`
	UserAddress     string       `json:"userAddress" gorm:"type:varchar(255);not null;index"`
	BaseModel       string       `json:"baseModel" gorm:"type:varchar(255);not null"`
	AdapterName     string       `json:"adapterName" gorm:"type:varchar(255);not null;uniqueIndex"`
	StorageRootHash string       `json:"storageRootHash" gorm:"type:varchar(255);not null"`
	State           AdapterState `json:"state" gorm:"type:varchar(32);not null;default:'loading'"`
	LastAccessAt    *time.Time   `json:"lastAccessAt" gorm:"type:datetime"`
	AdapterPath     string       `json:"adapterPath" gorm:"type:varchar(512)"`
	DeployedAt      *time.Time   `json:"deployedAt" gorm:"type:datetime"`
	BlockNumber     uint64       `json:"blockNumber" gorm:"type:bigint unsigned;not null;default:0"`
}

// AdapterKey stores the provider-encrypted AES key pushed by the fine-tuning broker
// via HTTP. The inference broker looks up this key by TaskID when deploying an adapter.
type AdapterKey struct {
	Model
	TaskID         string `json:"taskId" gorm:"type:varchar(255);not null;uniqueIndex"`
	StorageHash    string `json:"storageHash" gorm:"type:varchar(255);not null"`
	ProviderEncKey string `json:"providerEncKey" gorm:"type:text;not null"`
	// TeeSignerAddress is the address the producing enclave signed the artifact's
	// chunk-tag stream with. AesDecryptLargeFile verifies the 65-byte signature at
	// the head of the downloaded artifact against it; without it the signature is
	// unverifiable and the adapter must not be deployed. Nullable so rows pushed
	// before this column existed are distinguishable from an explicit zero
	// address, and fail with a clear "re-push required" error rather than silently
	// verifying against 0x0.
	TeeSignerAddress string `json:"teeSignerAddress" gorm:"type:varchar(42)"`
}
