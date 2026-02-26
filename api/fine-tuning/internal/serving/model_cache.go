package serving

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ModelState represents the storage tier of a LoRA adapter.
type ModelState int

const (
	// ModelStateActive means the adapter is on local disk and available for vLLM to load.
	// vLLM manages the GPU↔CPU transitions internally via its own LRU cache.
	ModelStateActive ModelState = iota
	// ModelStateArchived means the adapter has been removed from disk and only
	// exists in 0G Storage. A download is required before vLLM can serve it.
	ModelStateArchived
	// ModelStateLoading means the adapter is being downloaded from 0G Storage.
	ModelStateLoading
)

func (s ModelState) String() string {
	return [...]string{"active", "archived", "loading"}[s]
}

// StorageDownloader abstracts 0G Storage download operations so the serving
// package does not depend on the concrete storage.Client type.
type StorageDownloader interface {
	DownloadFromStorage(ctx context.Context, hash, filePath string, isTurbo bool) (string, error)
}

// RecordAccess updates the last-accessed timestamp for a model.
// Called on every inference request to prevent premature offloading.
func (m *Manager) RecordAccess(modelName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if model, exists := m.servedModels[modelName]; exists {
		model.LastAccessedAt = time.Now()
	}
}

// GetModelState returns the current storage tier state of a served model.
func (m *Manager) GetModelState(modelName string) (ModelState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	model, exists := m.servedModels[modelName]
	if !exists {
		return 0, false
	}
	return model.State, true
}

// offloadLoop periodically checks for inactive models and moves them to cold
// storage by deleting local files. Models can be restored on demand via RestoreModel.
func (m *Manager) offloadLoop(ctx context.Context) {
	if m.config.OffloadAfterMinutes <= 0 || !m.config.EnableColdStorage {
		m.logger.Info("cold storage offloading disabled")
		return
	}

	m.logger.Infof("cold storage offload loop started (threshold: %d minutes)", m.config.OffloadAfterMinutes)
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.offloadStaleModels()
		}
	}
}

func (m *Manager) offloadStaleModels() {
	threshold := time.Now().Add(-time.Duration(m.config.OffloadAfterMinutes) * time.Minute)

	m.mu.Lock()
	defer m.mu.Unlock()

	for name, model := range m.servedModels {
		if model.State != ModelStateActive {
			continue
		}
		if model.OutputRootHash == "" {
			continue
		}
		if model.LastAccessedAt.Before(threshold) {
			destDir := filepath.Join(m.loraModulesDir, name)
			if err := os.RemoveAll(destDir); err != nil {
				m.logger.Warnf("failed to remove symlink %s during offload: %v", destDir, err)
			}
			if err := os.RemoveAll(model.LoRAPath); err != nil {
				m.logger.Warnf("failed to remove LoRA files %s during offload: %v", model.LoRAPath, err)
			}
			model.State = ModelStateArchived
			m.logger.Infof("offloaded model %s to cold storage (last accessed: %s)",
				name, model.LastAccessedAt.Format(time.RFC3339))
		}
	}
}

// RestoreModel triggers an async download of an archived model from 0G Storage.
// Returns nil immediately if the model is already active or already loading.
func (m *Manager) RestoreModel(ctx context.Context, modelName string) error {
	m.mu.Lock()
	model, exists := m.servedModels[modelName]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("model not found: %s", modelName)
	}

	switch model.State {
	case ModelStateActive:
		m.mu.Unlock()
		return nil
	case ModelStateLoading:
		m.mu.Unlock()
		return nil
	case ModelStateArchived:
		model.State = ModelStateLoading
		m.mu.Unlock()
	}

	go func() {
		if err := m.downloadAndActivate(ctx, modelName); err != nil {
			m.logger.Errorf("failed to restore model %s from cold storage: %v", modelName, err)
			m.mu.Lock()
			if mdl, ok := m.servedModels[modelName]; ok {
				mdl.State = ModelStateArchived
			}
			m.mu.Unlock()
		}
	}()

	return nil
}

func (m *Manager) downloadAndActivate(ctx context.Context, modelName string) error {
	m.mu.RLock()
	model, exists := m.servedModels[modelName]
	if !exists {
		m.mu.RUnlock()
		return fmt.Errorf("model not found: %s", modelName)
	}
	hash := model.OutputRootHash
	loraPath := model.LoRAPath
	m.mu.RUnlock()

	if m.storageClient == nil {
		return fmt.Errorf("storage client not configured")
	}

	m.logger.Infof("downloading model %s from 0G Storage (hash: %s)", modelName, hash)

	if err := os.MkdirAll(filepath.Dir(loraPath), 0755); err != nil {
		return fmt.Errorf("create lora directory: %w", err)
	}

	if _, err := m.storageClient.DownloadFromStorage(ctx, hash, loraPath, false); err != nil {
		return fmt.Errorf("download from storage: %w", err)
	}

	destDir := filepath.Join(m.loraModulesDir, modelName)
	_ = os.Remove(destDir)
	if err := os.Symlink(loraPath, destDir); err != nil && !os.IsExist(err) {
		return fmt.Errorf("symlink lora adapter: %w", err)
	}

	m.mu.Lock()
	if mdl, ok := m.servedModels[modelName]; ok {
		mdl.State = ModelStateActive
		mdl.LastAccessedAt = time.Now()
	}
	m.mu.Unlock()

	m.logger.Infof("model %s restored from cold storage and activated", modelName)
	return nil
}
