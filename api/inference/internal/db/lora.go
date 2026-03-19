package db

import (
	"time"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (d *DB) CreateLoRAAdapter(adapter *model.LoRAAdapter) error {
	return d.db.Create(adapter).Error
}

func (d *DB) GetLoRAAdapterByName(adapterName string) (*model.LoRAAdapter, error) {
	var adapter model.LoRAAdapter
	if err := d.db.Where("adapter_name = ?", adapterName).First(&adapter).Error; err != nil {
		return nil, err
	}
	return &adapter, nil
}

func (d *DB) GetLoRAAdapterByTaskID(taskID string) (*model.LoRAAdapter, error) {
	var adapter model.LoRAAdapter
	if err := d.db.Where("task_id = ?", taskID).First(&adapter).Error; err != nil {
		return nil, err
	}
	return &adapter, nil
}

func (d *DB) ListLoRAAdapters() ([]model.LoRAAdapter, error) {
	var adapters []model.LoRAAdapter
	if err := d.db.Find(&adapters).Error; err != nil {
		return nil, err
	}
	return adapters, nil
}

func (d *DB) ListLoRAAdaptersByUser(userAddress string) ([]model.LoRAAdapter, error) {
	var adapters []model.LoRAAdapter
	if err := d.db.Where("user_address = ?", userAddress).Find(&adapters).Error; err != nil {
		return nil, err
	}
	return adapters, nil
}

func (d *DB) UpdateLoRAAdapterState(adapterName string, state model.AdapterState) error {
	return d.db.Model(&model.LoRAAdapter{}).Where("adapter_name = ?", adapterName).
		Update("state", state).Error
}

func (d *DB) UpdateLoRAAdapterPath(adapterName, path string) error {
	return d.db.Model(&model.LoRAAdapter{}).Where("adapter_name = ?", adapterName).
		Update("adapter_path", path).Error
}

func (d *DB) UpdateLoRAAdapterAccess(adapterName string) error {
	now := time.Now()
	return d.db.Model(&model.LoRAAdapter{}).Where("adapter_name = ?", adapterName).
		Update("last_access_at", &now).Error
}

func (d *DB) DeleteLoRAAdapter(adapterName string) error {
	return d.db.Where("adapter_name = ?", adapterName).Delete(&model.LoRAAdapter{}).Error
}

func (d *DB) GetLastProcessedBlock() (uint64, error) {
	var adapter model.LoRAAdapter
	if err := d.db.Order("block_number DESC").First(&adapter).Error; err != nil {
		return 0, nil // No adapters yet, start from 0
	}
	return adapter.BlockNumber, nil
}

func (d *DB) ListIdleAdapters(idleThreshold time.Duration) ([]model.LoRAAdapter, error) {
	cutoff := time.Now().Add(-idleThreshold)
	var adapters []model.LoRAAdapter
	if err := d.db.Where("state = ? AND (last_access_at IS NULL OR last_access_at < ?)",
		model.AdapterStateActive, cutoff).Find(&adapters).Error; err != nil {
		return nil, err
	}
	return adapters, nil
}

func (d *DB) CreateAdapterKey(key *model.AdapterKey) error {
	return d.db.Create(key).Error
}

func (d *DB) GetAdapterKeyByTaskID(taskID string) (*model.AdapterKey, error) {
	var key model.AdapterKey
	if err := d.db.Where("task_id = ?", taskID).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}
