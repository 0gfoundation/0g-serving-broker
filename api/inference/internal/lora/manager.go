package lora

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
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

// Default quota values applied when LoRAConfig.Quota fields are zero. These
// match the documented defaults in config.go but are duplicated here so the
// manager applies them even if the YAML schema is older or the operator
// explicitly cleared the field. 0 means "no limit" only after passing
// through these helpers — see quotaXxx() methods below.
const (
	defaultMaxAdaptersPerUser           = 5
	defaultMaxAdaptersTotal             = 100
	defaultMaxConcurrentDownloads       = 3
	defaultMaxAdapterDiskBytes    int64 = 100 * 1024 * 1024 * 1024 // 100 GiB
	defaultCapacityCheckInterval        = 5 * time.Minute
)

// AdmissionError is returned by RegisterAdapter when admission control rejects
// a new adapter. It carries a stable code so on-chain event watchers and the
// admin tooling can distinguish the rejection reason.
type AdmissionError struct {
	Code    string
	Message string
}

func (e *AdmissionError) Error() string { return e.Message }

const (
	AdmissionRejectedBlocked       = "user_blocked"
	AdmissionRejectedPerUserQuota  = "per_user_quota_exceeded"
	AdmissionRejectedTotalQuota    = "total_quota_exceeded"
	AdmissionRejectedDiskQuota     = "disk_quota_exceeded"
	AdmissionRejectedManagerClosed = "manager_closed"
)

// Manager manages the lifecycle of LoRA adapters: discovery via on-chain events,
// download/decrypt from 0G Storage, deployment to ServerlessLLM, offloading, and restoration.
type Manager struct {
	mu       sync.RWMutex
	adapters map[string]*AdapterInfo // adapterName → info
	ctx      context.Context         // application-scoped context for graceful shutdown

	config            config.LoRAConfig
	db                *db.DB
	sllmClient        *SLLMClient
	storageDownloader *StorageDownloader
	logger            log.Logger

	// blockedUsers is the lower-cased operator kill-switch set. New adapter
	// registrations from these addresses are rejected at admission time
	// (issue #470).
	blockedUsers map[string]struct{}

	// downloadSemaphore caps concurrent 0G Storage download goroutines.
	// nil means no cap. Sized at construction from the quota config.
	downloadSemaphore chan struct{}
}

// NewManager creates a LoRA adapter manager with the given config, 0G Storage downloader,
// and ServerlessLLM client. Call Start() to begin background event processing.
func NewManager(cfg config.LoRAConfig, networks commonconfig.Networks, database *db.DB, logger log.Logger) (*Manager, error) {
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

	var downloader *StorageDownloader
	eciesKey := cfg.EciesPrivateKey
	if envKey := os.Getenv("LORA_ECIES_PRIVATE_KEY"); envKey != "" {
		eciesKey = envKey
		logger.Infof("using ECIES private key from LORA_ECIES_PRIVATE_KEY environment variable")
	}
	if eciesKey == "" {
		k, err := commonconfig.GetProviderPrivateKey(networks)
		if err != nil {
			logger.Warnf("provider private key not available for 0G Storage download: %v", err)
		} else {
			eciesKey = k
		}
	} else {
		logger.Infof("using explicit ECIES private key for adapter decryption (2-CVM mode)")
	}
	if eciesKey != "" && cfg.StorageIndexerUrl != "" {
		var err error
		downloader, err = NewStorageDownloader(cfg, eciesKey, logger)
		if err != nil {
			logger.Warnf("failed to create storage downloader: %v", err)
		} else {
			logger.Infof("0G Storage downloader initialized (indexer: %s)", cfg.StorageIndexerUrl)
		}
	}

	m := &Manager{
		adapters:          make(map[string]*AdapterInfo),
		config:            cfg,
		db:                database,
		sllmClient:        sllmClient,
		storageDownloader: downloader,
		logger:            logger,
	}

	// Build the blocklist as a lower-cased set for O(1) admission lookups.
	if len(cfg.Quota.BlockedUsers) > 0 {
		m.blockedUsers = make(map[string]struct{}, len(cfg.Quota.BlockedUsers))
		for _, u := range cfg.Quota.BlockedUsers {
			m.blockedUsers[strings.ToLower(strings.TrimSpace(u))] = struct{}{}
		}
		logger.Infof("LoRA blocklist: %d address(es) will have new registrations rejected", len(m.blockedUsers))
	}

	if cap := m.quotaMaxConcurrentDownloads(); cap > 0 {
		m.downloadSemaphore = make(chan struct{}, cap)
		logger.Infof("LoRA download concurrency capped at %d", cap)
	}

	return m, nil
}

// quotaMaxAdaptersPerUser returns the configured per-user quota with the
// default applied when the field is zero. A negative value disables the
// check entirely (operator opt-out).
func (m *Manager) quotaMaxAdaptersPerUser() int {
	v := m.config.Quota.MaxAdaptersPerUser
	if v < 0 {
		return 0 // disabled
	}
	if v == 0 {
		return defaultMaxAdaptersPerUser
	}
	return v
}

// quotaMaxAdaptersTotal returns the broker-wide adapter cap with default applied.
func (m *Manager) quotaMaxAdaptersTotal() int {
	v := m.config.Quota.MaxAdaptersTotal
	if v < 0 {
		return 0
	}
	if v == 0 {
		return defaultMaxAdaptersTotal
	}
	return v
}

// quotaMaxConcurrentDownloads returns the download concurrency cap with default applied.
func (m *Manager) quotaMaxConcurrentDownloads() int {
	v := m.config.Quota.MaxConcurrentDownloads
	if v < 0 {
		return 0
	}
	if v == 0 {
		return defaultMaxConcurrentDownloads
	}
	return v
}

// quotaMaxAdapterDiskBytes returns the disk usage cap with default applied.
// 0 disables; default is 100 GiB.
func (m *Manager) quotaMaxAdapterDiskBytes() int64 {
	v := m.config.Quota.MaxAdapterDiskBytes
	if v < 0 {
		return 0
	}
	if v == 0 {
		return defaultMaxAdapterDiskBytes
	}
	return v
}

// quotaCapacityCheckInterval returns how often the LRU eviction loop runs.
func (m *Manager) quotaCapacityCheckInterval() time.Duration {
	v := time.Duration(m.config.Quota.CapacityCheckIntervalMinutes) * time.Minute
	if v <= 0 {
		return defaultCapacityCheckInterval
	}
	return v
}

// isUserBlocked reports whether the address is on the operator kill-switch list.
// Comparison is case-insensitive on the hex address.
func (m *Manager) isUserBlocked(userAddress string) bool {
	if len(m.blockedUsers) == 0 {
		return false
	}
	_, ok := m.blockedUsers[strings.ToLower(strings.TrimSpace(userAddress))]
	return ok
}

// admissionCheck enforces per-user, broker-wide, and disk quotas before an
// adapter is persisted to the DB. Returning a non-nil error rejects the
// registration upstream — the event watcher will log it and move on.
func (m *Manager) admissionCheck(userAddress string) error {
	if m.isUserBlocked(userAddress) {
		return &AdmissionError{Code: AdmissionRejectedBlocked,
			Message: fmt.Sprintf("user %s is blocked from registering new adapters", userAddress)}
	}

	if m.db == nil {
		// Without a DB we have no way to enforce counts. Allow but log.
		m.logger.Warn("admissionCheck: DB not configured, skipping quota checks")
		return nil
	}

	if cap := m.quotaMaxAdaptersPerUser(); cap > 0 {
		n, err := m.db.CountAdaptersByUser(userAddress)
		if err != nil {
			m.logger.Warnf("admissionCheck: count by user %s: %v (allowing)", userAddress, err)
		} else if int(n) >= cap {
			return &AdmissionError{
				Code:    AdmissionRejectedPerUserQuota,
				Message: fmt.Sprintf("per-user quota exceeded: user %s already owns %d/%d adapters", userAddress, n, cap),
			}
		}
	}

	if cap := m.quotaMaxAdaptersTotal(); cap > 0 {
		n, err := m.db.CountTotalAdapters()
		if err != nil {
			m.logger.Warnf("admissionCheck: count total: %v (allowing)", err)
		} else if int(n) >= cap {
			return &AdmissionError{
				Code:    AdmissionRejectedTotalQuota,
				Message: fmt.Sprintf("broker-wide quota exceeded: %d/%d adapters", n, cap),
			}
		}
	}

	if cap := m.quotaMaxAdapterDiskBytes(); cap > 0 && m.config.LoraModulesDir != "" {
		if used, err := dirSize(m.config.LoraModulesDir); err == nil && used >= cap {
			return &AdmissionError{
				Code:    AdmissionRejectedDiskQuota,
				Message: fmt.Sprintf("disk quota exceeded: %d / %d bytes used", used, cap),
			}
		}
	}

	return nil
}

// Start loads known adapters from DB and begins background loops.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx

	if err := m.loadFromDB(); err != nil {
		m.logger.Errorf("failed to load adapters from DB: %v", err)
	}

	if err := m.redeployExistingAdapters(ctx); err != nil {
		m.logger.Errorf("failed to redeploy adapters: %v", err)
	}

	m.mu.RLock()
	adapterCount := len(m.adapters)
	m.mu.RUnlock()

	go m.offloadLoop(ctx)
	// Capacity-based eviction is enforced separately from idle-based offload
	// (issue #470). The latter only releases GPU slots; this one releases
	// disk and DB rows when the broker is over its quota.
	go m.capacityEvictionLoop(ctx)

	m.logger.Infof("LoRA Manager started: %d adapters loaded", adapterCount)
	return nil
}

// dirSize sums the size of all regular files under root. Errors on individual
// files are tolerated so a single unreadable file does not block the quota
// check; the caller logs and continues.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// GetBaseModel returns the base model path configured for this LoRA manager.
func (m *Manager) GetBaseModel() string {
	return m.config.BaseModel
}

// GetAdapter returns a snapshot copy of adapter info by name (thread-safe).
func (m *Manager) GetAdapter(adapterName string) *AdapterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.adapters[adapterName]
	if !ok {
		return nil
	}
	cp := *a
	return &cp
}

// FindAdapterByTaskID returns a snapshot copy of the first adapter matching the given task ID.
func (m *Manager) FindAdapterByTaskID(taskID string) *AdapterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.adapters {
		if a.TaskID == taskID {
			cp := *a
			return &cp
		}
	}
	return nil
}

// GetAdaptersByUser returns snapshot copies of all adapters owned by a specific user.
func (m *Manager) GetAdaptersByUser(userAddress string) []*AdapterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*AdapterInfo
	for _, a := range m.adapters {
		if strings.EqualFold(a.UserAddress, userAddress) {
			cp := *a
			result = append(result, &cp)
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

// LoRAAdapterPrefix is the naming prefix for fine-tuned LoRA adapter models.
const LoRAAdapterPrefix = "ft-"

// IsLoRAModel returns true if the model name represents a fine-tuned LoRA adapter.
func IsLoRAModel(modelName string) bool {
	return strings.HasPrefix(modelName, LoRAAdapterPrefix)
}

// RecordAccess updates last access time for an adapter.
func (m *Manager) RecordAccess(adapterName string) {
	m.mu.Lock()
	if a, ok := m.adapters[adapterName]; ok {
		a.LastAccessAt = time.Now()
	}
	m.mu.Unlock()

	if m.db != nil {
		if err := m.db.UpdateLoRAAdapterAccess(adapterName); err != nil {
			m.logger.Errorf("failed to update adapter %s access time in DB: %v", adapterName, err)
		}
	}
}

// RegisterAdapter adds a new LoRA adapter (called when on-chain event detected).
// DB is written first so that a failed DB write doesn't leave orphaned in-memory state.
//
// Admission control (issue #470): rejects the registration if the user is
// blocked or any of the per-user / broker-wide / disk quotas would be
// exceeded. Returns an *AdmissionError so the caller can distinguish a
// rejection from an internal error.
func (m *Manager) RegisterAdapter(ctx context.Context, taskID, userAddress, baseModel, storageRootHash string, blockNumber uint64) error {
	adapterName := MakeAdapterName(baseModel, taskID)
	adapterPath := filepath.Join(m.config.LoraModulesDir, adapterName)

	m.mu.RLock()
	_, exists := m.adapters[adapterName]
	m.mu.RUnlock()
	if exists {
		m.logger.Infof("adapter %s already registered, skipping", adapterName)
		return nil
	}

	// Admission control runs before any DB write so a rejected registration
	// does not consume disk, bandwidth, or a DB row. Re-running for the same
	// task is idempotent because the duplicate-name check above handles it.
	if err := m.admissionCheck(userAddress); err != nil {
		m.logger.Warnf("admission rejected for adapter %s (user %s): %v", adapterName, userAddress, err)
		return err
	}

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
		return fmt.Errorf("persist adapter %s to DB: %w", adapterName, err)
	}

	info := &AdapterInfo{
		TaskID:          taskID,
		UserAddress:     userAddress,
		BaseModel:       baseModel,
		AdapterName:     adapterName,
		StorageRootHash: storageRootHash,
		State:           model.AdapterStateLoading,
		LastAccessAt:    now,
		AdapterPath:     adapterPath,
	}

	m.mu.Lock()
	if _, dup := m.adapters[adapterName]; dup {
		m.mu.Unlock()
		return nil
	}
	m.adapters[adapterName] = info
	infoCopy := *info
	m.mu.Unlock()

	if m.ctx != nil {
		select {
		case <-m.ctx.Done():
			m.setAdapterState(adapterName, model.AdapterStateFailed)
			return fmt.Errorf("manager context cancelled, cannot start download for %s", adapterName)
		default:
		}
	}

	go m.downloadAdapter(m.ctx, &infoCopy)
	return nil
}

// downloadAdapter downloads the adapter from 0G Storage and decrypts it.
// If AutoDeploy is enabled, it also deploys to ServerlessLLM immediately.
// Otherwise, the adapter is left in "ready" state for user-triggered deployment.
//
// The actual 0G Storage transfer is gated by downloadSemaphore (issue #470)
// so a burst of on-chain ack events cannot spawn unbounded concurrent
// downloads. State stays Loading for the entire wait + download window.
func (m *Manager) downloadAdapter(ctx context.Context, info *AdapterInfo) {
	m.logger.Infof("downloading adapter %s (hash: %s)", info.AdapterName, info.StorageRootHash)

	if _, err := os.Stat(info.AdapterPath); os.IsNotExist(err) {
		// Acquire a download slot; bail out cleanly on shutdown.
		if m.downloadSemaphore != nil {
			select {
			case m.downloadSemaphore <- struct{}{}:
			case <-ctx.Done():
				m.logger.Warnf("context cancelled before acquiring download slot for %s", info.AdapterName)
				m.setAdapterState(info.AdapterName, model.AdapterStateFailed)
				return
			}
			defer func() { <-m.downloadSemaphore }()
		}

		if err := m.downloadFromStorage(ctx, info); err != nil {
			m.logger.Errorf("failed to download adapter %s from 0G Storage: %v", info.AdapterName, err)
			os.RemoveAll(info.AdapterPath)
			m.setAdapterState(info.AdapterName, model.AdapterStateFailed)
			return
		}
	}

	// Persist updated adapter path to DB so it survives broker restarts.
	if err := m.db.UpdateLoRAAdapterPath(info.AdapterName, info.AdapterPath); err != nil {
		m.logger.Errorf("failed to persist adapter path for %s: %v", info.AdapterName, err)
	}

	if m.config.AutoDeploy {
		m.logger.Infof("auto-deploy enabled, deploying adapter %s", info.AdapterName)
		m.deployToVLLM(ctx, info)
	} else {
		m.logger.Infof("adapter %s downloaded and ready (auto-deploy disabled, awaiting user deploy)", info.AdapterName)
		m.setAdapterState(info.AdapterName, model.AdapterStateReady)
	}
}

// deployToVLLM loads the adapter into ServerlessLLM/vLLM.
func (m *Manager) deployToVLLM(ctx context.Context, info *AdapterInfo) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			m.logger.Warnf("context cancelled before deploying adapter %s", info.AdapterName)
			m.setAdapterState(info.AdapterName, model.AdapterStateFailed)
			return
		default:
		}
	}

	m.setAdapterState(info.AdapterName, model.AdapterStateLoading)

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

	if err := m.db.UpdateLoRAAdapterState(info.AdapterName, model.AdapterStateActive); err != nil {
		m.logger.Errorf("failed to update adapter %s state in DB: %v", info.AdapterName, err)
	}
	m.logger.Infof("adapter %s deployed successfully", info.AdapterName)
}

// UserDeployAdapter is called by the HTTP API to deploy an adapter that is in "ready" or "failed" state.
// Uses CAS (Compare-And-Swap) pattern: checks state and transitions to Loading atomically under write lock
// to prevent duplicate deploys from concurrent requests.
func (m *Manager) UserDeployAdapter(ctx context.Context, adapterName string) error {
	m.mu.Lock()
	info, ok := m.adapters[adapterName]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("adapter %s not found", adapterName)
	}

	switch info.State {
	case model.AdapterStateReady, model.AdapterStateFailed:
		info.State = model.AdapterStateLoading
		infoCopy := *info
		m.mu.Unlock()
		go m.deployToVLLM(m.ctx, &infoCopy)
		return nil
	case model.AdapterStateActive:
		m.mu.Unlock()
		return fmt.Errorf("adapter %s is already deployed", adapterName)
	case model.AdapterStateLoading:
		m.mu.Unlock()
		return fmt.Errorf("adapter %s is still being downloaded or deployed, please wait", adapterName)
	default:
		m.mu.Unlock()
		return fmt.Errorf("adapter %s is in state %s, cannot deploy", adapterName, info.State)
	}
}

// downloadFromStorage handles the full 0G Storage download → ECIES decrypt → AES decrypt → unzip pipeline.
// It looks up the provider-encrypted AES key from the local adapter_keys table (pre-pushed by fine-tuning broker via HTTP).
func (m *Manager) downloadFromStorage(ctx context.Context, info *AdapterInfo) error {
	if m.storageDownloader == nil {
		return errors.New("0G Storage downloader not configured; set storageIndexerUrl and provider private key")
	}

	adapterKey, err := m.db.GetAdapterKeyByTaskID(info.TaskID)
	if err != nil {
		return errors.Wrapf(err, "look up adapter key for task %s (was it pushed by fine-tuning broker?)", info.TaskID)
	}

	encKeyHex := strings.TrimPrefix(adapterKey.ProviderEncKey, "0x")
	providerEncKey, err := hex.DecodeString(encKeyHex)
	if err != nil {
		return errors.Wrapf(err, "decode provider encrypted key for task %s", info.TaskID)
	}

	storageHashHex := adapterKey.StorageHash
	if storageHashHex == "" {
		storageHashHex = info.StorageRootHash
	}
	m.logger.Infof("adapter key found: storage=%s, encrypted key=%d bytes", storageHashHex, len(providerEncKey))

	actualPath, err := m.storageDownloader.DownloadAndDecrypt(ctx, storageHashHex, providerEncKey, info.AdapterPath)
	if err != nil {
		return err
	}

	// The zip may contain a top-level wrapper directory (e.g. "adapter/"),
	// so the actual adapter files may be in a subdirectory of info.AdapterPath.
	// Update the map entry by name (info may be a copy) and also update the caller's copy.
	if actualPath != "" && actualPath != info.AdapterPath {
		m.logger.Infof("adapter path updated: %s → %s", info.AdapterPath, actualPath)
		m.mu.Lock()
		if a, ok := m.adapters[info.AdapterName]; ok {
			a.AdapterPath = actualPath
		}
		m.mu.Unlock()
		info.AdapterPath = actualPath
	}
	return nil
}

// RestoreAdapter downloads and redeploys an offloaded/archived adapter.
// When triggered by chat request, always deploy to make adapter immediately usable.
// Uses CAS pattern: atomically checks and transitions state under write lock.
func (m *Manager) RestoreAdapter(adapterName string) error {
	m.mu.Lock()
	info, ok := m.adapters[adapterName]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("adapter %s not found", adapterName)
	}

	if info.State == model.AdapterStateActive || info.State == model.AdapterStateLoading {
		m.mu.Unlock()
		return nil
	}

	info.State = model.AdapterStateLoading
	infoCopy := *info
	m.mu.Unlock()

	if m.db != nil {
		if err := m.db.UpdateLoRAAdapterState(adapterName, model.AdapterStateLoading); err != nil {
			m.logger.Errorf("failed to update adapter %s state to loading in DB: %v", adapterName, err)
		}
	}

	go func() {
		m.logger.Infof("restore: downloading adapter %s from 0G Storage", infoCopy.AdapterName)

		if _, err := os.Stat(infoCopy.AdapterPath); os.IsNotExist(err) {
			if dlErr := m.downloadFromStorage(m.ctx, &infoCopy); dlErr != nil {
				m.logger.Errorf("restore: failed to download adapter %s from 0G Storage: %v", infoCopy.AdapterName, dlErr)
				m.setAdapterState(infoCopy.AdapterName, model.AdapterStateFailed)
				return
			}
		}

		if m.db != nil {
			if err := m.db.UpdateLoRAAdapterPath(infoCopy.AdapterName, infoCopy.AdapterPath); err != nil {
				m.logger.Errorf("restore: failed to persist adapter path for %s: %v", infoCopy.AdapterName, err)
			}
		}

		m.mu.Lock()
		a, ok := m.adapters[infoCopy.AdapterName]
		if !ok {
			m.mu.Unlock()
			m.logger.Warnf("restore: adapter %s removed from map during download, aborting", infoCopy.AdapterName)
			return
		}
		if a.State == model.AdapterStateFailed {
			m.mu.Unlock()
			return
		}
		a.State = model.AdapterStateLoading
		infoCopy.State = model.AdapterStateLoading
		m.mu.Unlock()

		m.logger.Infof("restore: auto-deploying adapter %s to ServerlessLLM", infoCopy.AdapterName)
		m.deployToVLLM(m.ctx, &infoCopy)
	}()

	return nil
}

func (m *Manager) setAdapterState(adapterName string, state model.AdapterState) {
	m.mu.Lock()
	if a, ok := m.adapters[adapterName]; ok {
		a.State = state
	}
	m.mu.Unlock()
	if m.db != nil {
		if err := m.db.UpdateLoRAAdapterState(adapterName, state); err != nil {
			m.logger.Errorf("failed to update adapter %s state to %s in DB: %v", adapterName, state, err)
		}
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
	var toRedeploy []AdapterInfo
	for _, a := range m.adapters {
		switch a.State {
		case model.AdapterStateActive, model.AdapterStateLoading:
			toRedeploy = append(toRedeploy, *a)
		case model.AdapterStateReady:
			if m.config.AutoDeploy {
				toRedeploy = append(toRedeploy, *a)
			}
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
	// Check at half the offload interval so an idle adapter is detected
	// within [interval/2, interval] of its last access.
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

// capacityEvictionLoop runs periodically and drives capacity-based eviction
// of LoRA adapters when the broker exceeds the per-broker count quota or
// the disk quota (issue #470). Unlike offloadLoop which only releases the
// GPU slot, this loop reclaims disk and DB rows by transitioning oldest
// adapters all the way to Archived.
func (m *Manager) capacityEvictionLoop(ctx context.Context) {
	interval := m.quotaCapacityCheckInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.evictForCapacity(ctx)
		}
	}
}

// evictForCapacity computes the over-quota delta and archives that many
// LRU adapters. Eviction stops as soon as either count and disk caps are
// satisfied, even if the disk-walk reports stale data.
func (m *Manager) evictForCapacity(ctx context.Context) {
	if m.db == nil {
		return
	}

	totalCap := m.quotaMaxAdaptersTotal()
	diskCap := m.quotaMaxAdapterDiskBytes()

	currentCount, err := m.db.CountTotalAdapters()
	if err != nil {
		m.logger.Errorf("evict: count total: %v", err)
		return
	}

	var diskUsed int64
	if m.config.LoraModulesDir != "" {
		if du, err := dirSize(m.config.LoraModulesDir); err == nil {
			diskUsed = du
		} else {
			m.logger.Warnf("evict: dir size for %s: %v", m.config.LoraModulesDir, err)
		}
	}

	overCount := totalCap > 0 && int(currentCount) > totalCap
	overDisk := diskCap > 0 && diskUsed > diskCap
	if !overCount && !overDisk {
		return
	}

	m.logger.Infof("evict: over capacity (count=%d/%d, disk=%d/%d), starting LRU eviction",
		currentCount, totalCap, diskUsed, diskCap)

	// Pick at most a small batch per tick so a flood of evictions does not
	// stall request handling.
	const maxEvictPerTick = 20
	candidates, err := m.db.ListLRUEvictionCandidates(maxEvictPerTick)
	if err != nil {
		m.logger.Errorf("evict: list LRU: %v", err)
		return
	}

	for _, a := range candidates {
		if !overCount && !overDisk {
			break
		}
		if err := m.archiveAdapter(ctx, a.AdapterName); err != nil {
			m.logger.Warnf("evict: archive %s: %v", a.AdapterName, err)
			continue
		}
		currentCount--
		if a.AdapterPath != "" {
			if fi, err := os.Stat(a.AdapterPath); err == nil {
				diskUsed -= fi.Size()
			}
		}
		overCount = totalCap > 0 && int(currentCount) > totalCap
		overDisk = diskCap > 0 && diskUsed > diskCap
	}
}

// archiveAdapter releases ServerlessLLM slot, deletes adapter files on disk,
// transitions the DB row to Archived. The DB row is intentionally retained
// so a future on-chain re-acknowledge can re-download via the same path.
//
// Provider operators wanting full row removal should call DeleteAdapter via
// the admin endpoint, which is gated by provider-key auth.
func (m *Manager) archiveAdapter(ctx context.Context, adapterName string) error {
	m.mu.RLock()
	a, ok := m.adapters[adapterName]
	var path string
	if ok {
		path = a.AdapterPath
	}
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("adapter %s not in memory", adapterName)
	}

	if err := m.sllmClient.DeleteAdapter(ctx, adapterName); err != nil {
		// Continue even if SLLM unload fails — the adapter may already
		// be offloaded; we still want to reclaim disk.
		m.logger.Warnf("archive %s: SLLM delete: %v", adapterName, err)
	}
	if path != "" {
		if err := os.RemoveAll(path); err != nil {
			m.logger.Warnf("archive %s: remove %s: %v", adapterName, path, err)
		}
	}
	m.setAdapterState(adapterName, model.AdapterStateArchived)
	m.logger.Infof("archived adapter %s (LRU eviction)", adapterName)
	return nil
}

// EvictAdapter is the operator-facing eviction entry point. It is exposed via
// the provider-only admin endpoint (issue #470) and goes through the same
// archiveAdapter path used by capacity eviction.
//
// purge: when true the DB row is also deleted, otherwise it is kept in
// Archived state so the user can later re-acknowledge to redownload.
func (m *Manager) EvictAdapter(ctx context.Context, adapterName string, purge bool) error {
	if err := m.archiveAdapter(ctx, adapterName); err != nil {
		return err
	}
	if !purge {
		return nil
	}

	m.mu.Lock()
	delete(m.adapters, adapterName)
	m.mu.Unlock()
	if m.db != nil {
		if err := m.db.DeleteLoRAAdapter(adapterName); err != nil {
			return fmt.Errorf("delete adapter %s from DB: %w", adapterName, err)
		}
	}
	return nil
}

// MakeAdapterName builds a deterministic adapter name from base model and task ID.
// It strips any directory path from baseModel so that "/models/Qwen2.5-0.5B-Instruct"
// and "Qwen2.5-0.5B-Instruct" produce the same name.
func MakeAdapterName(baseModel, taskID string) string {
	base := filepath.Base(baseModel)
	sanitized := strings.NewReplacer("/", "-", ".", "-", " ", "-").Replace(base)
	sanitized = strings.Trim(sanitized, "-")
	short := taskID
	if len(taskID) > 12 {
		short = taskID[:12]
	}
	return fmt.Sprintf("%s%s-%s", LoRAAdapterPrefix, sanitized, short)
}

// InjectTestAdapter adds an adapter directly to the in-memory map (for testing only).
func (m *Manager) InjectTestAdapter(name string, info *AdapterInfo) {
	m.mu.Lock()
	if m.adapters == nil {
		m.adapters = make(map[string]*AdapterInfo)
	}
	m.adapters[name] = info
	m.mu.Unlock()
}

// ListAdapters returns snapshot copies of all known adapters (for API/debug).
func (m *Manager) ListAdapters() []*AdapterInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*AdapterInfo, 0, len(m.adapters))
	for _, a := range m.adapters {
		cp := *a
		result = append(result, &cp)
	}
	return result
}
