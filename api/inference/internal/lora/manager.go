package lora

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// AdapterInfo is the in-memory representation of a LoRA adapter.
type AdapterInfo struct {
	TaskID          string
	UserAddress     string
	BaseModel       string
	AdapterName     string
	StorageRootHash string
	State           model.AdapterState
	LastAccessAt    time.Time
	AdapterPath     string
}

// Manager manages the lifecycle of LoRA adapters: discovery via on-chain events,
// download/decrypt from 0G Storage, deployment to ServerlessLLM, offloading, and restoration.
type Manager struct {
	mu       sync.RWMutex
	adapters map[string]*AdapterInfo // adapterName → info

	config     config.LoRAConfig
	db         *db.DB
	sllmClient *SLLMClient
	logger     log.Logger
}

func NewManager(cfg config.LoRAConfig, database *db.DB, logger log.Logger) (*Manager, error) {
	if cfg.LoraModulesDir != "" {
		if err := os.MkdirAll(cfg.LoraModulesDir, 0755); err != nil {
			return nil, errors.Wrap(err, "create lora modules directory")
		}
	}

	sllmURL := cfg.SllmUrl
	if sllmURL == "" {
		sllmURL = "http://sllm:8343"
	}
	sllmClient := NewSLLMClient(sllmURL, logger)

	m := &Manager{
		adapters:   make(map[string]*AdapterInfo),
		config:     cfg,
		db:         database,
		sllmClient: sllmClient,
		logger:     logger,
	}

	return m, nil
}

// Start loads known adapters from DB and begins background loops.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.loadFromDB(); err != nil {
		m.logger.Errorf("failed to load adapters from DB: %v", err)
	}

	if err := m.redeployExistingAdapters(ctx); err != nil {
		m.logger.Errorf("failed to redeploy adapters: %v", err)
	}

	go m.offloadLoop(ctx)

	m.logger.Infof("LoRA Manager started: %d adapters loaded", len(m.adapters))
	return nil
}

// GetAdapter returns adapter info by name (thread-safe).
func (m *Manager) GetAdapter(adapterName string) *AdapterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.adapters[adapterName]
}

// GetAdaptersByUser returns all adapters owned by a specific user.
func (m *Manager) GetAdaptersByUser(userAddress string) []*AdapterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*AdapterInfo
	normalized := strings.ToLower(userAddress)
	for _, a := range m.adapters {
		if strings.EqualFold(a.UserAddress, normalized) {
			result = append(result, a)
		}
	}
	return result
}

// IsModelOwner checks if userAddress owns the adapter (case-insensitive).
func (m *Manager) IsModelOwner(adapterName, userAddress string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	a, ok := m.adapters[adapterName]
	if !ok {
		return false
	}
	return strings.EqualFold(a.UserAddress, userAddress)
}

// IsLoRAModel returns true if the model name represents a fine-tuned LoRA adapter.
func IsLoRAModel(modelName string) bool {
	return strings.HasPrefix(modelName, "ft-")
}

// RecordAccess updates last access time for an adapter.
func (m *Manager) RecordAccess(adapterName string) {
	m.mu.Lock()
	if a, ok := m.adapters[adapterName]; ok {
		a.LastAccessAt = time.Now()
	}
	m.mu.Unlock()

	if m.db != nil {
		_ = m.db.UpdateLoRAAdapterAccess(adapterName)
	}
}

// RegisterAdapter adds a new LoRA adapter (called when on-chain event detected).
func (m *Manager) RegisterAdapter(ctx context.Context, taskID, userAddress, baseModel, storageRootHash string, blockNumber uint64) error {
	adapterName := MakeAdapterName(baseModel, taskID)
	adapterPath := filepath.Join(m.config.LoraModulesDir, adapterName)

	m.mu.Lock()
	if _, exists := m.adapters[adapterName]; exists {
		m.mu.Unlock()
		m.logger.Infof("adapter %s already registered, skipping", adapterName)
		return nil
	}

	info := &AdapterInfo{
		TaskID:          taskID,
		UserAddress:     userAddress,
		BaseModel:       baseModel,
		AdapterName:     adapterName,
		StorageRootHash: storageRootHash,
		State:           model.AdapterStateLoading,
		LastAccessAt:    time.Now(),
		AdapterPath:     adapterPath,
	}
	m.adapters[adapterName] = info
	m.mu.Unlock()

	now := time.Now()
	dbAdapter := &model.LoRAAdapter{
		TaskID:          taskID,
		UserAddress:     userAddress,
		BaseModel:       baseModel,
		AdapterName:     adapterName,
		StorageRootHash: storageRootHash,
		State:           model.AdapterStateLoading,
		LastAccessAt:    &now,
		AdapterPath:     adapterPath,
		BlockNumber:     blockNumber,
	}
	if err := m.db.CreateLoRAAdapter(dbAdapter); err != nil {
		m.logger.Errorf("failed to persist adapter %s to DB: %v", adapterName, err)
	}

	go m.downloadAndDeploy(ctx, info)
	return nil
}

// downloadAndDeploy downloads the adapter from 0G Storage, decrypts, and deploys to ServerlessLLM.
func (m *Manager) downloadAndDeploy(ctx context.Context, info *AdapterInfo) {
	m.logger.Infof("downloading adapter %s from 0G Storage (hash: %s)", info.AdapterName, info.StorageRootHash)

	if _, err := os.Stat(info.AdapterPath); os.IsNotExist(err) {
		if m.config.MockDeploy {
			m.logger.Infof("mock deploy: creating placeholder adapter at %s", info.AdapterPath)
			if mkErr := os.MkdirAll(info.AdapterPath, 0755); mkErr != nil {
				m.logger.Errorf("mock deploy: failed to create dir: %v", mkErr)
				m.setAdapterState(info.AdapterName, model.AdapterStateFailed)
				return
			}
			placeholder := filepath.Join(info.AdapterPath, "adapter_config.json")
			_ = os.WriteFile(placeholder, []byte(`{"mock":true}`), 0644)
		} else {
			// TODO: Integrate with 0G Storage client for actual download + decryption.
			m.logger.Warnf("adapter path %s not found on disk; 0G Storage download not yet implemented", info.AdapterPath)
			m.setAdapterState(info.AdapterName, model.AdapterStateFailed)
			return
		}
	}

	if err := m.sllmClient.DeployAdapter(ctx, info.BaseModel, info.AdapterName, info.AdapterPath); err != nil {
		m.logger.Errorf("failed to deploy adapter %s to ServerlessLLM: %v", info.AdapterName, err)
		m.setAdapterState(info.AdapterName, model.AdapterStateFailed)
		return
	}

	now := time.Now()
	m.mu.Lock()
	if a, ok := m.adapters[info.AdapterName]; ok {
		a.State = model.AdapterStateActive
		a.LastAccessAt = now
	}
	m.mu.Unlock()

	_ = m.db.UpdateLoRAAdapterState(info.AdapterName, model.AdapterStateActive)
	m.logger.Infof("adapter %s deployed successfully", info.AdapterName)
}

// RestoreAdapter downloads and redeploys an offloaded/archived adapter.
func (m *Manager) RestoreAdapter(ctx context.Context, adapterName string) error {
	m.mu.RLock()
	info, ok := m.adapters[adapterName]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("adapter %s not found", adapterName)
	}

	if info.State == model.AdapterStateActive || info.State == model.AdapterStateLoading {
		return nil
	}

	m.setAdapterState(adapterName, model.AdapterStateLoading)
	go m.downloadAndDeploy(ctx, info)
	return nil
}

func (m *Manager) setAdapterState(adapterName string, state model.AdapterState) {
	m.mu.Lock()
	if a, ok := m.adapters[adapterName]; ok {
		a.State = state
	}
	m.mu.Unlock()
	if m.db != nil {
		_ = m.db.UpdateLoRAAdapterState(adapterName, state)
	}
}

func (m *Manager) loadFromDB() error {
	adapters, err := m.db.ListLoRAAdapters()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, a := range adapters {
		lastAccess := time.Now()
		if a.LastAccessAt != nil {
			lastAccess = *a.LastAccessAt
		}
		m.adapters[a.AdapterName] = &AdapterInfo{
			TaskID:          a.TaskID,
			UserAddress:     a.UserAddress,
			BaseModel:       a.BaseModel,
			AdapterName:     a.AdapterName,
			StorageRootHash: a.StorageRootHash,
			State:           a.State,
			LastAccessAt:    lastAccess,
			AdapterPath:     a.AdapterPath,
		}
	}
	return nil
}

func (m *Manager) redeployExistingAdapters(ctx context.Context) error {
	m.mu.RLock()
	var toRedeploy []*AdapterInfo
	for _, a := range m.adapters {
		if a.State == model.AdapterStateActive || a.State == model.AdapterStateLoading {
			toRedeploy = append(toRedeploy, a)
		}
	}
	m.mu.RUnlock()

	for _, a := range toRedeploy {
		if _, err := os.Stat(a.AdapterPath); err == nil {
			if err := m.sllmClient.DeployAdapter(ctx, a.BaseModel, a.AdapterName, a.AdapterPath); err != nil {
				m.logger.Errorf("failed to redeploy adapter %s: %v", a.AdapterName, err)
				m.setAdapterState(a.AdapterName, model.AdapterStateFailed)
			} else {
				m.setAdapterState(a.AdapterName, model.AdapterStateActive)
				m.logger.Infof("redeployed adapter %s on startup", a.AdapterName)
			}
		} else {
			m.logger.Warnf("adapter %s files not on disk, marking as archived", a.AdapterName)
			m.setAdapterState(a.AdapterName, model.AdapterStateArchived)
		}
	}
	return nil
}

func (m *Manager) offloadLoop(ctx context.Context) {
	if !m.config.EnableColdStorage {
		m.logger.Info("cold storage offloading disabled")
		return
	}

	interval := time.Duration(m.config.OffloadAfterMinutes) * time.Minute
	if interval <= 0 {
		interval = 60 * time.Minute
	}
	ticker := time.NewTicker(interval / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.offloadIdleAdapters(ctx)
		}
	}
}

func (m *Manager) offloadIdleAdapters(ctx context.Context) {
	threshold := time.Duration(m.config.OffloadAfterMinutes) * time.Minute
	idle, err := m.db.ListIdleAdapters(threshold)
	if err != nil {
		m.logger.Errorf("failed to list idle adapters: %v", err)
		return
	}

	for _, a := range idle {
		m.logger.Infof("offloading idle adapter %s (last access: %v)", a.AdapterName, a.LastAccessAt)

		if err := m.sllmClient.DeleteAdapter(ctx, a.AdapterName); err != nil {
			m.logger.Errorf("failed to offload adapter %s: %v", a.AdapterName, err)
			continue
		}
		m.setAdapterState(a.AdapterName, model.AdapterStateOffloaded)
	}
}

// MakeAdapterName builds a deterministic adapter name from base model and task ID.
func MakeAdapterName(baseModel, taskID string) string {
	sanitized := strings.NewReplacer("/", "-", ".", "-", " ", "-").Replace(baseModel)
	short := taskID
	if len(taskID) > 12 {
		short = taskID[:12]
	}
	return fmt.Sprintf("ft-%s-%s", sanitized, short)
}

// InjectTestAdapter adds an adapter directly to the in-memory map (for testing only).
func (m *Manager) InjectTestAdapter(name string, info *AdapterInfo) {
	if m.adapters == nil {
		m.adapters = make(map[string]*AdapterInfo)
	}
	m.mu.Lock()
	m.adapters[name] = info
	m.mu.Unlock()
}

// ListAdapters returns all known adapters (for API/debug).
func (m *Manager) ListAdapters() []*AdapterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*AdapterInfo, 0, len(m.adapters))
	for _, a := range m.adapters {
		result = append(result, a)
	}
	return result
}
