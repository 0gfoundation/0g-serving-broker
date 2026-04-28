package db

import (
	"errors"
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/gorm"
)

// CreateLoRAAdapter persists a new LoRA adapter record to the database.
func (d *DB) CreateLoRAAdapter(adapter *model.LoRAAdapter) error {
	return d.db.Create(adapter).Error
}

// GetLoRAAdapterByName retrieves a single LoRA adapter by its unique adapter name.
func (d *DB) GetLoRAAdapterByName(adapterName string) (*model.LoRAAdapter, error) {
	var adapter model.LoRAAdapter
	if err := d.db.Where("adapter_name = ?", adapterName).First(&adapter).Error; err != nil {
		return nil, err
	}
	return &adapter, nil
}

// GetLoRAAdapterByTaskID retrieves a single LoRA adapter by its fine-tuning task ID.
func (d *DB) GetLoRAAdapterByTaskID(taskID string) (*model.LoRAAdapter, error) {
	var adapter model.LoRAAdapter
	if err := d.db.Where("task_id = ?", taskID).First(&adapter).Error; err != nil {
		return nil, err
	}
	return &adapter, nil
}

// ListLoRAAdapters returns all LoRA adapters in the database.
func (d *DB) ListLoRAAdapters() ([]model.LoRAAdapter, error) {
	var adapters []model.LoRAAdapter
	if err := d.db.Find(&adapters).Error; err != nil {
		return nil, err
	}
	return adapters, nil
}

// ListLoRAAdaptersByUser returns all LoRA adapters owned by the given user address.
func (d *DB) ListLoRAAdaptersByUser(userAddress string) ([]model.LoRAAdapter, error) {
	var adapters []model.LoRAAdapter
	if err := d.db.Where("user_address = ?", userAddress).Find(&adapters).Error; err != nil {
		return nil, err
	}
	return adapters, nil
}

// UpdateLoRAAdapterState updates the lifecycle state of a LoRA adapter.
func (d *DB) UpdateLoRAAdapterState(adapterName string, state model.AdapterState) error {
	return d.db.Model(&model.LoRAAdapter{}).Where("adapter_name = ?", adapterName).
		Update("state", state).Error
}

// UpdateLoRAAdapterPath updates the local filesystem path for an adapter's files.
func (d *DB) UpdateLoRAAdapterPath(adapterName, path string) error {
	return d.db.Model(&model.LoRAAdapter{}).Where("adapter_name = ?", adapterName).
		Update("adapter_path", path).Error
}

// UpdateLoRAAdapterAccess sets last_access_at to now for idle-detection purposes.
func (d *DB) UpdateLoRAAdapterAccess(adapterName string) error {
	now := time.Now()
	return d.db.Model(&model.LoRAAdapter{}).Where("adapter_name = ?", adapterName).
		Update("last_access_at", &now).Error
}

// DeleteLoRAAdapter removes a LoRA adapter record from the database.
func (d *DB) DeleteLoRAAdapter(adapterName string) error {
	return d.db.Where("adapter_name = ?", adapterName).Delete(&model.LoRAAdapter{}).Error
}

// GetLastProcessedBlock returns the highest block number across all adapters,
// used by the event watcher to resume from the correct block.
func (d *DB) GetLastProcessedBlock() (uint64, error) {
	var adapter model.LoRAAdapter
	if err := d.db.Order("block_number DESC").First(&adapter).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return adapter.BlockNumber, nil
}

// ListIdleAdapters returns active adapters whose last access time exceeds the threshold.
func (d *DB) ListIdleAdapters(idleThreshold time.Duration) ([]model.LoRAAdapter, error) {
	cutoff := time.Now().Add(-idleThreshold)
	var adapters []model.LoRAAdapter
	if err := d.db.Where("state = ? AND last_access_at IS NOT NULL AND last_access_at < ?",
		model.AdapterStateActive, cutoff).Find(&adapters).Error; err != nil {
		return nil, err
	}
	return adapters, nil
}

// CountAdaptersByUser returns the number of adapters owned by a given user
// that count toward the per-user quota (issue #470). Failed adapters do
// not count, since they consumed no GPU and are typically purged.
// Comparison is case-insensitive on the address.
func (d *DB) CountAdaptersByUser(userAddress string) (int64, error) {
	var n int64
	err := d.db.Model(&model.LoRAAdapter{}).
		Where("LOWER(user_address) = LOWER(?) AND state <> ?", userAddress, model.AdapterStateFailed).
		Count(&n).Error
	return n, err
}

// CountTotalAdapters returns total adapters (excluding Failed) across all users.
func (d *DB) CountTotalAdapters() (int64, error) {
	var n int64
	err := d.db.Model(&model.LoRAAdapter{}).
		Where("state <> ?", model.AdapterStateFailed).
		Count(&n).Error
	return n, err
}

// ListLRUEvictionCandidates returns adapters in {Active, Ready, Offloaded}
// states ordered by last_access_at ascending (oldest first) — capacity-based
// eviction targets (issue #470). Loading and Failed states are excluded so
// in-progress work is not interrupted.
func (d *DB) ListLRUEvictionCandidates(limit int) ([]model.LoRAAdapter, error) {
	var adapters []model.LoRAAdapter
	q := d.db.Where("state IN ?",
		[]model.AdapterState{
			model.AdapterStateActive,
			model.AdapterStateReady,
			model.AdapterStateOffloaded,
		}).
		Order("last_access_at ASC NULLS FIRST")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&adapters).Error; err != nil {
		// Fallback for SQL dialects that reject `NULLS FIRST` (MySQL):
		// retry with COALESCE(last_access_at, 0).
		q2 := d.db.Where("state IN ?",
			[]model.AdapterState{
				model.AdapterStateActive,
				model.AdapterStateReady,
				model.AdapterStateOffloaded,
			}).
			Order("COALESCE(last_access_at, '1970-01-01') ASC")
		if limit > 0 {
			q2 = q2.Limit(limit)
		}
		if err2 := q2.Find(&adapters).Error; err2 != nil {
			return nil, err
		}
	}
	return adapters, nil
}

// CreateAdapterKey stores a provider-encrypted AES key pushed by the fine-tuning broker.
// Uses upsert semantics: if a key for the same task already exists (e.g. from a
// previous delivery attempt that partially succeeded), the storage hash and
// encrypted key are updated rather than returning a duplicate-key error.
func (d *DB) CreateAdapterKey(key *model.AdapterKey) error {
	return d.db.
		Where("task_id = ?", key.TaskID).
		Assign(model.AdapterKey{
			StorageHash:    key.StorageHash,
			ProviderEncKey: key.ProviderEncKey,
		}).
		FirstOrCreate(key).Error
}

// GetAdapterKeyByTaskID retrieves a pre-pushed adapter key by its task ID.
func (d *DB) GetAdapterKeyByTaskID(taskID string) (*model.AdapterKey, error) {
	var key model.AdapterKey
	if err := d.db.Where("task_id = ?", taskID).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}
