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

// CreateAdapterKey stores a provider-encrypted AES key pushed by the fine-tuning
// broker. It is intentionally idempotent: if a row for the same TaskID already
// exists (e.g. from a previous push attempt that succeeded server-side but
// timed out / errored on the client side), the StorageHash and ProviderEncKey
// are updated rather than returning a unique-constraint violation.
//
// Why idempotency matters: the fine-tuning broker retries pushAdapterKey on
// transient HTTP failures, but the inference broker's stored state only
// depends on (TaskID, StorageHash, ProviderEncKey). Without upsert the retry
// loop dead-locks on `Duplicate entry for key 'task_id'` and the task never
// reaches Delivered (Bug Report — May 2026, Bug #3).
func (d *DB) CreateAdapterKey(key *model.AdapterKey) error {
	// Explicit read-then-write rather than Assign+FirstOrCreate, because the three
	// columns are ONE description of ONE artifact and the rules for a push that
	// omits teeSignerAddress cannot be expressed with Assign: a struct Assign drops
	// zero values, so it would apply the new storageHash and providerEncKey while
	// silently keeping the old signer.
	//
	// storageHash is keccak of the ENCRYPTED file, so it changes whenever the
	// artifact is re-encrypted — and re-encryption changes the chunk-tag stream that
	// the signature covers. That makes it the right discriminator:
	//
	//   - same storageHash, no signer supplied → the stored signer still describes
	//     these exact bytes, so preserve it. This is the version-skew retry case; a
	//     fine-tuning broker that predates the field must not wipe a good signer.
	//
	//   - storageHash changed, no signer supplied → the stored signer describes the
	//     PREVIOUS artifact. Keeping it would make verification fail as "signer
	//     mismatch", which reads as tampering and sends whoever debugs it down the
	//     wrong path. Clearing it fails closed too, but with the documented,
	//     recoverable "no usable TEE signer" error that names how to complete the
	//     row.
	//
	//   - signer supplied → it wins, which is what a rotation after an enclave-image
	//     change needs.
	return d.db.Transaction(func(tx *gorm.DB) error {
		var existing model.AdapterKey
		err := tx.Where("task_id = ?", key.TaskID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(key).Error
		}
		if err != nil {
			return err
		}

		signer := key.TeeSignerAddress
		if signer == "" && existing.StorageHash == key.StorageHash {
			signer = existing.TeeSignerAddress
		}

		// A map, not a struct: the deliberate clear above writes an empty string, and
		// a struct update would drop it as a zero value.
		//
		// The WHERE is explicit because model.AdapterKey has no primary key — its
		// only key is the unique index on task_id — so gorm cannot derive one from
		// the loaded row and Updates would fail with "WHERE conditions required".
		return tx.Model(&model.AdapterKey{}).
			Where("task_id = ?", key.TaskID).
			Updates(map[string]interface{}{
				"storage_hash":       key.StorageHash,
				"provider_enc_key":   key.ProviderEncKey,
				"tee_signer_address": signer,
			}).Error
	})
}

// GetAdapterKeyByTaskID retrieves a pre-pushed adapter key by its task ID.
func (d *DB) GetAdapterKeyByTaskID(taskID string) (*model.AdapterKey, error) {
	var key model.AdapterKey
	if err := d.db.Where("task_id = ?", taskID).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}
