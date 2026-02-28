package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/google/uuid"
)

// ServedModel represents a LoRA adapter tracked by the serving system.
// State indicates its current storage tier (active on disk, archived in 0G Storage, or loading).
type ServedModel struct {
	TaskID         uuid.UUID
	UserAddress    string
	BaseModel      string
	LoRAPath       string
	ModelName      string
	RegisteredAt   time.Time
	LastAccessedAt time.Time
	State          ModelState
	OutputRootHash string
}

// Manager controls the lifecycle of the vLLM process, LoRA adapter loading/unloading,
// and multi-tier caching (GPU → CPU → Disk → 0G Storage).
type Manager struct {
	mu             sync.RWMutex
	servedModels   map[string]*ServedModel // key: modelName
	modelReadyChs  map[string]chan struct{} // closed when model becomes active or restore fails
	db             *db.DB
	logger         log.Logger
	config         ServingConfig
	vllmProcess    *exec.Cmd
	vllmReady      bool
	loraModulesDir string
	httpClient     *http.Client
	storageClient  StorageDownloader
}

// ServingConfig holds configuration for the LoRA inference serving subsystem.
type ServingConfig struct {
	Enable              bool   `yaml:"enable"`
	BaseModelPath       string `yaml:"baseModelPath"`
	InferenceGPUIDs     string `yaml:"inferenceGpuIds"`
	VLLMPort            int    `yaml:"vllmPort"`
	MaxLoraRank         int    `yaml:"maxLoraRank"`
	MaxLoraModules      int    `yaml:"maxLoraModules"`
	MaxCpuLoras         int    `yaml:"maxCpuLoras"`
	LoraModulesDir      string `yaml:"loraModulesDir"`
	OffloadAfterMinutes     int  `yaml:"offloadAfterMinutes"`
	EnableColdStorage       bool `yaml:"enableColdStorage"`
	ModelLoadTimeoutSeconds int  `yaml:"modelLoadTimeoutSeconds"`
}

// NewManager creates a new serving Manager with the given database, config, logger,
// and optional storage client for cold-storage offload/restore.
func NewManager(db *db.DB, config ServingConfig, logger log.Logger, storageClient StorageDownloader) *Manager {
	loraDir := config.LoraModulesDir
	if loraDir == "" {
		loraDir = "/tmp/lora-modules"
	}

	return &Manager{
		servedModels:   make(map[string]*ServedModel),
		modelReadyChs:  make(map[string]chan struct{}),
		db:             db,
		logger:         logger,
		config:         config,
		loraModulesDir: loraDir,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		storageClient: storageClient,
	}
}

// Start launches the vLLM subprocess and begins polling for finished fine-tuning tasks.
func (m *Manager) Start(ctx context.Context) error {
	if !m.config.Enable {
		m.logger.Info("LoRA serving is disabled")
		return nil
	}

	if m.config.BaseModelPath == "" {
		return errors.New("serving.baseModelPath is required when serving is enabled")
	}

	if err := os.MkdirAll(m.loraModulesDir, 0755); err != nil {
		return errors.Wrap(err, "create lora modules directory")
	}

	go m.startVLLM(ctx)
	go m.pollFinishedTasks(ctx)
	go m.offloadLoop(ctx)
	return nil
}

// Stop gracefully terminates the vLLM process and all its child processes.
// vLLM runs a multi-process architecture (APIServer + EngineCore), so we kill
// the entire process group to avoid orphaned GPU-holding processes.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.vllmProcess == nil || m.vllmProcess.Process == nil {
		return nil
	}

	pid := m.vllmProcess.Process.Pid
	m.logger.Infof("stopping vLLM process group (pid %d)", pid)

	// Kill the entire process group (negative PID) to ensure EngineCore
	// and other child processes are terminated and GPU memory is released.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		m.logger.Warnf("SIGTERM to process group failed: %v, escalating to SIGKILL", err)
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			m.logger.Errorf("SIGKILL to process group also failed: %v", err)
			return err
		}
	}

	// Wait briefly for graceful shutdown, then force kill if still alive.
	done := make(chan struct{})
	go func() {
		m.vllmProcess.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Info("vLLM process group terminated gracefully")
	case <-time.After(10 * time.Second):
		m.logger.Warn("vLLM did not exit within 10s, sending SIGKILL")
		syscall.Kill(-pid, syscall.SIGKILL)
	}

	return nil
}

func (m *Manager) startVLLM(ctx context.Context) {
	port := m.config.VLLMPort
	if port == 0 {
		port = 8000
	}

	maxLoraRank := m.config.MaxLoraRank
	if maxLoraRank == 0 {
		maxLoraRank = 64
	}

	args := []string{
		"serve", m.config.BaseModelPath,
		"--port", fmt.Sprintf("%d", port),
		"--enable-lora",
		"--max-lora-rank", fmt.Sprintf("%d", maxLoraRank),
	}

	if m.config.MaxLoraModules > 0 {
		args = append(args, "--max-loras", fmt.Sprintf("%d", m.config.MaxLoraModules))
	}
	if m.config.MaxCpuLoras > 0 {
		args = append(args, "--max-cpu-loras", fmt.Sprintf("%d", m.config.MaxCpuLoras))
	}

	m.logger.Infof("starting vLLM with args: %v", args)

	cmd := exec.CommandContext(ctx, "vllm", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Env = os.Environ()
	if m.config.InferenceGPUIDs != "" {
		cmd.Env = append(cmd.Env, "CUDA_VISIBLE_DEVICES="+m.config.InferenceGPUIDs)
	}
	cmd.Env = append(cmd.Env,
		"VLLM_ALLOW_RUNTIME_LORA_UPDATING=True",
		"VLLM_PLUGINS=lora_filesystem_resolver",
		"VLLM_LORA_RESOLVER_CACHE_DIR="+m.loraModulesDir,
	)

	m.mu.Lock()
	m.vllmProcess = cmd
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		m.logger.Errorf("failed to start vLLM: %v", err)
		return
	}

	go m.waitForVLLMReady(ctx)

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			m.logger.Info("vLLM stopped due to context cancellation")
			return
		}
		m.logger.Errorf("vLLM exited with error: %v", err)
	}
}

func (m *Manager) waitForVLLMReady(ctx context.Context) {
	endpoint := m.GetVLLMEndpoint()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/health", nil)
			if err != nil {
				continue
			}
			resp, err := m.httpClient.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					m.mu.Lock()
					m.vllmReady = true
					m.mu.Unlock()
					m.logger.Info("vLLM is ready (health check passed)")
					return
				}
			}
		}
	}
}

func (m *Manager) pollFinishedTasks(ctx context.Context) {
	// Wait for vLLM to be ready before polling
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
			if m.IsReady() {
				goto poll
			}
			m.logger.Debug("waiting for vLLM to be ready before polling tasks...")
		}
	}

poll:
	m.logger.Info("starting finished task polling for LoRA auto-registration")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	m.discoverAndRegisterModels()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.discoverAndRegisterModels()
		}
	}
}

func (m *Manager) discoverAndRegisterModels() {
	tasks, err := m.db.GetFinishedTasksForServing()
	if err != nil {
		m.logger.Warnf("failed to query finished tasks for serving: %v", err)
		return
	}

	for _, task := range tasks {
		taskDir := filepath.Join(utils.GetDataDir(), task.ID.String())
		paths := utils.NewTaskPaths(taskDir)
		loraPath := paths.Output

		if _, err := os.Stat(loraPath); os.IsNotExist(err) {
			continue
		}

		m.mu.RLock()
		modelName := m.makeModelName(task.PreTrainedModelHash, *task.ID)
		_, exists := m.servedModels[modelName]
		m.mu.RUnlock()

		if exists {
			continue
		}

		registeredName, err := m.RegisterModel(*task.ID, task.UserAddress, task.PreTrainedModelHash, loraPath, task.OutputRootHash)
		if err != nil {
			m.logger.Warnf("failed to auto-register model for task %s: %v", task.ID, err)
			continue
		}

		m.logger.Infof("auto-registered LoRA model: %s (task: %s, user: %s)", registeredName, task.ID, task.UserAddress)
	}

	m.pruneStaleModels()
}

func (m *Manager) pruneStaleModels() {
	if m.config.MaxLoraModules <= 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.servedModels) <= m.config.MaxLoraModules {
		return
	}

	var oldest *ServedModel
	for _, model := range m.servedModels {
		if oldest == nil || model.RegisteredAt.Before(oldest.RegisteredAt) {
			oldest = model
		}
	}

	if oldest != nil {
		destDir := filepath.Join(m.loraModulesDir, oldest.ModelName)
		if err := os.RemoveAll(destDir); err != nil {
			m.logger.Warnf("failed to remove pruned model directory %s: %v", destDir, err)
		}
		delete(m.servedModels, oldest.ModelName)
		m.logger.Infof("pruned oldest served model: %s (task: %s)", oldest.ModelName, oldest.TaskID)
	}
}

// RegisterModel creates a symlink for a LoRA adapter and adds it to the served model set.
// outputRootHash is the 0G Storage root hash used for cold-storage restore if the adapter
// is later offloaded.
func (m *Manager) RegisterModel(taskID uuid.UUID, userAddress, baseModel, loraPath, outputRootHash string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enable {
		return "", errors.New("LoRA serving is not enabled")
	}

	modelName := m.makeModelName(baseModel, taskID)

	destDir := filepath.Join(m.loraModulesDir, modelName)
	if err := os.MkdirAll(filepath.Dir(destDir), 0755); err != nil {
		return "", errors.Wrap(err, "create lora destination directory")
	}

	if err := os.Symlink(loraPath, destDir); err != nil && !os.IsExist(err) {
		return "", errors.Wrap(err, "symlink lora adapter")
	}

	now := time.Now()
	served := &ServedModel{
		TaskID:         taskID,
		UserAddress:    userAddress,
		BaseModel:      baseModel,
		LoRAPath:       loraPath,
		ModelName:      modelName,
		RegisteredAt:   now,
		LastAccessedAt: now,
		State:          ModelStateActive,
		OutputRootHash: outputRootHash,
	}

	m.servedModels[modelName] = served
	m.logger.Infof("registered LoRA model for serving: %s (task: %s, user: %s, hash: %s)",
		modelName, taskID, userAddress, outputRootHash)

	return modelName, nil
}

// UnregisterModel removes a LoRA adapter from serving and cleans up its symlink.
func (m *Manager) UnregisterModel(modelName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	served, exists := m.servedModels[modelName]
	if !exists {
		return fmt.Errorf("model not found: %s", modelName)
	}

	destDir := filepath.Join(m.loraModulesDir, modelName)
	if err := os.RemoveAll(destDir); err != nil {
		m.logger.Warnf("failed to remove model directory %s: %v", destDir, err)
	}

	delete(m.servedModels, modelName)
	m.logger.Infof("unregistered LoRA model: %s (task: %s)", modelName, served.TaskID)

	return nil
}

// ListServedModels returns all currently served LoRA models.
func (m *Manager) ListServedModels() []*ServedModel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	models := make([]*ServedModel, 0, len(m.servedModels))
	for _, model := range m.servedModels {
		models = append(models, model)
	}
	return models
}

// ListServedModelsForUser returns served models owned by the given user address.
func (m *Manager) ListServedModelsForUser(userAddress string) []*ServedModel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var models []*ServedModel
	for _, model := range m.servedModels {
		if strings.EqualFold(model.UserAddress, userAddress) {
			models = append(models, model)
		}
	}
	return models
}

// GetServedModel retrieves a served model by name.
func (m *Manager) GetServedModel(modelName string) (*ServedModel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	model, exists := m.servedModels[modelName]
	return model, exists
}

// IsModelOwner checks whether the given user address owns the specified model.
func (m *Manager) IsModelOwner(modelName, userAddress string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	model, exists := m.servedModels[modelName]
	if !exists {
		return false
	}
	return strings.EqualFold(model.UserAddress, userAddress)
}

// IsReady reports whether the vLLM backend has passed its health check.
func (m *Manager) IsReady() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.vllmReady
}

// GetVLLMEndpoint returns the base URL of the local vLLM server.
func (m *Manager) GetVLLMEndpoint() string {
	port := m.config.VLLMPort
	if port == 0 {
		port = 8000
	}
	return fmt.Sprintf("http://localhost:%d", port)
}

// makeModelName builds a deterministic, vLLM-safe model identifier from the
// base model name and task UUID. Non-alphanumeric characters (except - and _)
// are replaced with hyphens so the name is valid for vLLM's model registry.
func (m *Manager) makeModelName(baseModel string, taskID uuid.UUID) string {
	shortBase := baseModel
	if len(shortBase) > 16 {
		shortBase = shortBase[:16]
	}
	shortBase = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, shortBase)
	return fmt.Sprintf("ft-%s-%s", shortBase, taskID.String()[:12])
}

// GetVLLMModels queries the vLLM server for its currently loaded model names.
func (m *Manager) GetVLLMModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.GetVLLMEndpoint()+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []string
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models, nil
}
