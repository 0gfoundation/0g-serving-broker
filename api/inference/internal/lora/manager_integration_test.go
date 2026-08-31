//go:build integration

package lora

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()

	container, err := tcmysql.Run(ctx, "mysql:8.0",
		tcmysql.WithDatabase("testdb"),
		tcmysql.WithUsername("test"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start mysql container: %v", err)
	}
	t.Cleanup(func() { testcontainers.CleanupContainer(t, container) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatalf("get container port: %v", err)
	}

	dsn := fmt.Sprintf("test:test@tcp(%s:%s)/testdb?charset=utf8mb4&parseTime=True&loc=Local", host, port.Port())
	gdb, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	if err != nil {
		t.Fatalf("connect to mysql: %v", err)
	}
	if err := gdb.AutoMigrate(&model.LoRAAdapter{}, &model.AdapterKey{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return db.NewTestDB(gdb)
}

// --- RegisterAdapter with DB persistence ---

func TestRegisterAdapter_PersistsToDB(t *testing.T) {
	tmpDir := t.TempDir()
	database := setupTestDB(t)

	m := &Manager{
		adapters:   make(map[string]*AdapterInfo),
		config:     cfgForTest(tmpDir),
		db:         database,
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	err := m.RegisterAdapter(context.Background(), "task-reg-001", "0xAlice", "Qwen2.5-7B", "0xdeadbeef", 500)
	if err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	expectedName := MakeAdapterName("Qwen2.5-7B", "task-reg-001")

	// Verify in-memory
	m.mu.RLock()
	info, ok := m.adapters[expectedName]
	m.mu.RUnlock()
	if !ok {
		t.Fatalf("adapter %s not found in memory", expectedName)
	}
	if info.State != model.AdapterStateLoading {
		t.Errorf("in-memory state = %s, want loading", info.State)
	}

	// Verify in DB
	dbAdapter, err := database.GetLoRAAdapterByName(expectedName)
	if err != nil {
		t.Fatalf("DB lookup: %v", err)
	}
	if dbAdapter.TaskID != "task-reg-001" {
		t.Errorf("DB taskID = %s, want task-reg-001", dbAdapter.TaskID)
	}
	if dbAdapter.UserAddress != "0xAlice" {
		t.Errorf("DB userAddress = %s, want 0xAlice", dbAdapter.UserAddress)
	}
	if dbAdapter.BlockNumber != 500 {
		t.Errorf("DB blockNumber = %d, want 500", dbAdapter.BlockNumber)
	}
}

func TestRegisterAdapter_DuplicateIsNoop(t *testing.T) {
	tmpDir := t.TempDir()
	database := setupTestDB(t)

	m := &Manager{
		adapters:   make(map[string]*AdapterInfo),
		config:     cfgForTest(tmpDir),
		db:         database,
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	err := m.RegisterAdapter(context.Background(), "task-dup-001", "0xAlice", "base", "0xhash", 100)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}

	// Second registration with same task should be a no-op
	err = m.RegisterAdapter(context.Background(), "task-dup-001", "0xAlice", "base", "0xhash", 200)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}

	// Only one adapter in memory
	m.mu.RLock()
	count := len(m.adapters)
	m.mu.RUnlock()
	if count != 1 {
		t.Errorf("expected 1 adapter, got %d", count)
	}
}

// --- downloadFromStorage error paths ---

func TestDownloadFromStorage_NilDownloader(t *testing.T) {
	m := &Manager{
		adapters:          make(map[string]*AdapterInfo),
		storageDownloader: nil,
		logger:            getTestLogger(),
	}

	info := &AdapterInfo{
		TaskID:      "task-nil-dl",
		AdapterName: "ft-test-nil",
	}

	err := m.downloadFromStorage(context.Background(), info)
	if err == nil {
		t.Fatal("expected error when storageDownloader is nil")
	}
	if !contains(err.Error(), "not configured") {
		t.Errorf("error = %q, want mention of 'not configured'", err.Error())
	}
}

func TestDownloadFromStorage_MissingAdapterKey(t *testing.T) {
	database := setupTestDB(t)

	m := &Manager{
		adapters:          make(map[string]*AdapterInfo),
		storageDownloader: &StorageDownloader{logger: getTestLogger()},
		db:                database,
		logger:            getTestLogger(),
	}

	info := &AdapterInfo{
		TaskID:      "task-no-key",
		AdapterName: "ft-no-key",
	}

	err := m.downloadFromStorage(context.Background(), info)
	if err == nil {
		t.Fatal("expected error when adapter key not found in DB")
	}
	if !contains(err.Error(), "adapter key") {
		t.Errorf("error = %q, want mention of 'adapter key'", err.Error())
	}
}

func TestDownloadFromStorage_BadHexKey(t *testing.T) {
	database := setupTestDB(t)

	database.CreateAdapterKey(&model.AdapterKey{
		TaskID:         "task-bad-hex",
		StorageHash:    "0xabc",
		ProviderEncKey: "not-valid-hex!!",
	})

	m := &Manager{
		adapters:          make(map[string]*AdapterInfo),
		storageDownloader: &StorageDownloader{logger: getTestLogger()},
		db:                database,
		logger:            getTestLogger(),
	}

	info := &AdapterInfo{
		TaskID:      "task-bad-hex",
		AdapterName: "ft-bad-hex",
	}

	err := m.downloadFromStorage(context.Background(), info)
	if err == nil {
		t.Fatal("expected error for invalid hex key")
	}
	if !contains(err.Error(), "decode") {
		t.Errorf("error = %q, want mention of 'decode'", err.Error())
	}
}

// --- redeployExistingAdapters ---

func TestRedeployExistingAdapters_MissingFilesMarksArchived(t *testing.T) {
	database := setupTestDB(t)

	m := &Manager{
		adapters:   make(map[string]*AdapterInfo),
		config:     cfgForTest(t.TempDir()),
		db:         database,
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	m.adapters["ft-ghost"] = &AdapterInfo{
		AdapterName: "ft-ghost",
		AdapterPath: "/nonexistent/path/that/wont/exist",
		BaseModel:   "base",
		State:       model.AdapterStateActive,
	}

	// Create the DB record so setAdapterState can update it
	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "ghost-task", UserAddress: "0x1", BaseModel: "base",
		AdapterName: "ft-ghost", StorageRootHash: "0x1",
		State: model.AdapterStateActive, AdapterPath: "/nonexistent",
	})

	err := m.redeployExistingAdapters(context.Background())
	if err != nil {
		t.Fatalf("redeployExistingAdapters: %v", err)
	}

	m.mu.RLock()
	state := m.adapters["ft-ghost"].State
	m.mu.RUnlock()

	if state != model.AdapterStateArchived {
		t.Errorf("expected state=archived for missing files, got %s", state)
	}

	dbAdapter, err := database.GetLoRAAdapterByName("ft-ghost")
	if err != nil {
		t.Fatalf("DB lookup: %v", err)
	}
	if dbAdapter.State != model.AdapterStateArchived {
		t.Errorf("DB state = %s, want archived", dbAdapter.State)
	}
}

func TestRedeployExistingAdapters_SkipsReadyWhenAutoDeployOff(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		config: func() config.LoRAConfig {
			c := cfgForTest(t.TempDir())
			c.AutoDeploy = false
			return c
		}(),
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	dir := t.TempDir()
	adapterDir := filepath.Join(dir, "ft-ready-skip")
	os.MkdirAll(adapterDir, 0755)
	os.WriteFile(filepath.Join(adapterDir, "config.json"), []byte("{}"), 0644)

	m.adapters["ft-ready-skip"] = &AdapterInfo{
		AdapterName: "ft-ready-skip",
		AdapterPath: adapterDir,
		BaseModel:   "base",
		State:       model.AdapterStateReady,
	}

	err := m.redeployExistingAdapters(context.Background())
	if err != nil {
		t.Fatalf("redeployExistingAdapters: %v", err)
	}

	m.mu.RLock()
	state := m.adapters["ft-ready-skip"].State
	m.mu.RUnlock()

	if state != model.AdapterStateReady {
		t.Errorf("expected state=ready (not redeployed when AutoDeploy=false), got %s", state)
	}
}

func TestRedeployExistingAdapters_DeploysWithSLLM(t *testing.T) {
	var deployed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/models/deploy" {
			deployed = append(deployed, r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	database := setupTestDB(t)

	dir := t.TempDir()
	adapterDir := filepath.Join(dir, "ft-active-redeploy")
	os.MkdirAll(adapterDir, 0755)
	os.WriteFile(filepath.Join(adapterDir, "config.json"), []byte("{}"), 0644)

	m := &Manager{
		adapters:   make(map[string]*AdapterInfo),
		config:     cfgForTest(dir),
		db:         database,
		sllmClient: NewSLLMClient(srv.URL, getTestLogger()),
		logger:     getTestLogger(),
	}

	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "redeploy-task", UserAddress: "0xUser", BaseModel: "base",
		AdapterName: "ft-active-redeploy", StorageRootHash: "0x1",
		State: model.AdapterStateActive, AdapterPath: adapterDir,
	})

	m.adapters["ft-active-redeploy"] = &AdapterInfo{
		AdapterName: "ft-active-redeploy",
		AdapterPath: adapterDir,
		BaseModel:   "base",
		State:       model.AdapterStateActive,
	}

	err := m.redeployExistingAdapters(context.Background())
	if err != nil {
		t.Fatalf("redeployExistingAdapters: %v", err)
	}

	if len(deployed) != 1 {
		t.Errorf("expected 1 deploy call, got %d", len(deployed))
	}

	m.mu.RLock()
	state := m.adapters["ft-active-redeploy"].State
	m.mu.RUnlock()
	if state != model.AdapterStateActive {
		t.Errorf("expected state=active after successful redeploy, got %s", state)
	}
}

// --- offloadIdleAdapters ---

func TestOffloadIdleAdapters_OffloadsViaClient(t *testing.T) {
	var deletedAdapters []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deletedAdapters = append(deletedAdapters, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	database := setupTestDB(t)

	old := time.Now().Add(-2 * time.Hour)
	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "idle-1", UserAddress: "0x1", BaseModel: "b",
		AdapterName: "ft-idle-1", StorageRootHash: "0x1",
		State: model.AdapterStateActive, LastAccessAt: &old,
	})

	recent := time.Now().Add(-5 * time.Minute)
	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "active-1", UserAddress: "0x1", BaseModel: "b",
		AdapterName: "ft-active-1", StorageRootHash: "0x1",
		State: model.AdapterStateActive, LastAccessAt: &recent,
	})

	m := &Manager{
		adapters: map[string]*AdapterInfo{
			"ft-idle-1":   {AdapterName: "ft-idle-1", State: model.AdapterStateActive, LastAccessAt: old},
			"ft-active-1": {AdapterName: "ft-active-1", State: model.AdapterStateActive, LastAccessAt: recent},
		},
		config: func() config.LoRAConfig {
			c := cfgForTest(t.TempDir())
			c.OffloadAfter = 60 * time.Minute
			return c
		}(),
		db:         database,
		sllmClient: NewSLLMClient(srv.URL, getTestLogger()),
		logger:     getTestLogger(),
	}

	m.offloadIdleAdapters(context.Background())

	mu.Lock()
	defer mu.Unlock()

	if len(deletedAdapters) != 1 {
		t.Fatalf("expected 1 offload, got %d: %v", len(deletedAdapters), deletedAdapters)
	}
	if deletedAdapters[0] != "/v1/models/ft-idle-1" {
		t.Errorf("offloaded wrong adapter: %s", deletedAdapters[0])
	}

	m.mu.RLock()
	state := m.adapters["ft-idle-1"].State
	m.mu.RUnlock()
	if state != model.AdapterStateOffloaded {
		t.Errorf("expected idle adapter state=offloaded, got %s", state)
	}
}

// --- RestoreAdapter ---

func TestRestoreAdapter_ActiveIsNoop(t *testing.T) {
	m := &Manager{
		adapters: map[string]*AdapterInfo{
			"ft-active": {AdapterName: "ft-active", State: model.AdapterStateActive},
		},
		logger: getTestLogger(),
	}

	m.ctx = context.Background()
	err := m.RestoreAdapter("ft-active")
	if err != nil {
		t.Fatalf("RestoreAdapter on active: %v", err)
	}

	m.mu.RLock()
	state := m.adapters["ft-active"].State
	m.mu.RUnlock()
	if state != model.AdapterStateActive {
		t.Errorf("expected active state unchanged, got %s", state)
	}
}

func TestRestoreAdapter_LoadingIsNoop(t *testing.T) {
	m := &Manager{
		adapters: map[string]*AdapterInfo{
			"ft-loading": {AdapterName: "ft-loading", State: model.AdapterStateLoading},
		},
		logger: getTestLogger(),
	}

	m.ctx = context.Background()
	err := m.RestoreAdapter("ft-loading")
	if err != nil {
		t.Fatalf("RestoreAdapter on loading: %v", err)
	}

	m.mu.RLock()
	state := m.adapters["ft-loading"].State
	m.mu.RUnlock()
	if state != model.AdapterStateLoading {
		t.Errorf("expected loading state unchanged, got %s", state)
	}
}

func TestRestoreAdapter_NotFoundReturnsError(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.ctx = context.Background()
	err := m.RestoreAdapter("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent adapter")
	}
}

func TestRestoreAdapter_OffloadedTransitionsToLoading(t *testing.T) {
	database := setupTestDB(t)

	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "offloaded-task", UserAddress: "0x1", BaseModel: "base",
		AdapterName: "ft-offloaded", StorageRootHash: "0x1",
		State: model.AdapterStateOffloaded, AdapterPath: "/data/ft-offloaded",
	})

	m := &Manager{
		adapters: map[string]*AdapterInfo{
			"ft-offloaded": {
				AdapterName: "ft-offloaded",
				State:       model.AdapterStateOffloaded,
				AdapterPath: "/data/ft-offloaded",
				BaseModel:   "base",
				TaskID:      "offloaded-task",
			},
		},
		db:         database,
		sllmClient: NewSLLMClient("http://fake:8343", getTestLogger()),
		logger:     getTestLogger(),
	}

	m.ctx = context.Background()
	err := m.RestoreAdapter("ft-offloaded")
	if err != nil {
		t.Fatalf("RestoreAdapter: %v", err)
	}

	// Should transition to loading immediately (async goroutine does the download)
	m.mu.RLock()
	state := m.adapters["ft-offloaded"].State
	m.mu.RUnlock()
	if state != model.AdapterStateLoading {
		t.Errorf("expected state=loading after restore trigger, got %s", state)
	}
}

// --- loadFromDB ---

func TestLoadFromDB(t *testing.T) {
	database := setupTestDB(t)

	now := time.Now()
	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "db-task-1", UserAddress: "0xAlice", BaseModel: "base",
		AdapterName: "ft-db-1", StorageRootHash: "0x1",
		State: model.AdapterStateActive, LastAccessAt: &now,
		AdapterPath: "/data/ft-db-1", BlockNumber: 42,
	})
	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "db-task-2", UserAddress: "0xBob", BaseModel: "base",
		AdapterName: "ft-db-2", StorageRootHash: "0x2",
		State: model.AdapterStateReady, AdapterPath: "/data/ft-db-2",
		BlockNumber: 99,
	})

	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		db:       database,
		logger:   getTestLogger(),
	}

	if err := m.loadFromDB(); err != nil {
		t.Fatalf("loadFromDB: %v", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.adapters) != 2 {
		t.Fatalf("expected 2 adapters loaded, got %d", len(m.adapters))
	}

	a1 := m.adapters["ft-db-1"]
	if a1 == nil {
		t.Fatal("ft-db-1 not loaded")
	}
	if a1.TaskID != "db-task-1" || a1.UserAddress != "0xAlice" || a1.State != model.AdapterStateActive {
		t.Errorf("ft-db-1: unexpected values: task=%s user=%s state=%s", a1.TaskID, a1.UserAddress, a1.State)
	}

	a2 := m.adapters["ft-db-2"]
	if a2 == nil {
		t.Fatal("ft-db-2 not loaded")
	}
	if a2.State != model.AdapterStateReady {
		t.Errorf("ft-db-2: state = %s, want ready", a2.State)
	}
}

// --- downloadAdapter behavior ---

func TestDownloadAdapter_AutoDeploy(t *testing.T) {
	var deployCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/models/deploy" {
			deployCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	database := setupTestDB(t)
	tmpDir := t.TempDir()

	adapterDir := filepath.Join(tmpDir, "ft-autodeploy")
	os.MkdirAll(adapterDir, 0755)
	os.WriteFile(filepath.Join(adapterDir, "adapter_model.safetensors"), []byte("fake"), 0644)

	cfg := cfgForTest(tmpDir)
	cfg.AutoDeploy = true

	m := &Manager{
		adapters:   make(map[string]*AdapterInfo),
		config:     cfg,
		db:         database,
		sllmClient: NewSLLMClient(srv.URL, getTestLogger()),
		logger:     getTestLogger(),
	}

	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "auto-task", UserAddress: "0x1", BaseModel: "base",
		AdapterName: "ft-autodeploy", StorageRootHash: "0x1",
		State: model.AdapterStateLoading, AdapterPath: adapterDir,
	})

	info := &AdapterInfo{
		AdapterName: "ft-autodeploy",
		AdapterPath: adapterDir,
		BaseModel:   "base",
		State:       model.AdapterStateLoading,
	}
	m.adapters["ft-autodeploy"] = info

	m.downloadAdapter(context.Background(), info)

	if !deployCalled {
		t.Error("expected SLLM deploy to be called when AutoDeploy=true")
	}
}

func TestDownloadAdapter_ManualDeploy(t *testing.T) {
	var deployCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/models/deploy" {
			deployCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	database := setupTestDB(t)
	tmpDir := t.TempDir()

	adapterDir := filepath.Join(tmpDir, "ft-manual")
	os.MkdirAll(adapterDir, 0755)
	os.WriteFile(filepath.Join(adapterDir, "adapter_model.safetensors"), []byte("fake"), 0644)

	cfg := cfgForTest(tmpDir)
	cfg.AutoDeploy = false

	m := &Manager{
		adapters:   make(map[string]*AdapterInfo),
		config:     cfg,
		db:         database,
		sllmClient: NewSLLMClient(srv.URL, getTestLogger()),
		logger:     getTestLogger(),
	}

	database.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "manual-task", UserAddress: "0x1", BaseModel: "base",
		AdapterName: "ft-manual", StorageRootHash: "0x1",
		State: model.AdapterStateLoading, AdapterPath: adapterDir,
	})

	info := &AdapterInfo{
		AdapterName: "ft-manual",
		AdapterPath: adapterDir,
		BaseModel:   "base",
		State:       model.AdapterStateLoading,
	}
	m.adapters["ft-manual"] = info

	m.downloadAdapter(context.Background(), info)

	if deployCalled {
		t.Error("SLLM deploy should NOT be called when AutoDeploy=false")
	}

	m.mu.RLock()
	state := m.adapters["ft-manual"].State
	m.mu.RUnlock()
	if state != model.AdapterStateReady {
		t.Errorf("expected state=ready after manual download, got %s", state)
	}
}

// --- Concurrent safety ---

func TestConcurrentGetAndSet(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	m.adapters["ft-concurrent"] = &AdapterInfo{
		AdapterName: "ft-concurrent",
		State:       model.AdapterStateActive,
	}

	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			m.GetAdapter("ft-concurrent")
		}()
		go func() {
			defer wg.Done()
			m.RecordAccess("ft-concurrent")
		}()
	}

	wg.Wait()
}

// The TEE signer guard is what makes the whole verification fail closed, so its
// three refusal branches are pinned: absent, explicitly zero, and malformed. Each
// must be rejected BEFORE any download or decrypt, and the message must name the
// action an operator can actually take — pushAdapterKey has one caller inside
// Finalizer.Execute, so an already-delivered task is never re-pushed on its own.
func TestDownloadFromStorage_RefusesUnusableTeeSigner(t *testing.T) {
	cases := []struct {
		name   string
		signer string
	}{
		{"absent", ""},
		{"explicit zero address", "0x0000000000000000000000000000000000000000"},
		{"malformed", "not-an-address"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database := setupTestDB(t)

			taskID := "task-signer-" + tc.name
			if err := database.CreateAdapterKey(&model.AdapterKey{
				TaskID:           taskID,
				StorageHash:      "0x8888888888888888888888888888888888888888888888888888888888888888",
				ProviderEncKey:   "0xabcdef",
				TeeSignerAddress: tc.signer,
			}); err != nil {
				t.Fatalf("seed adapter key: %v", err)
			}

			m := &Manager{
				adapters:          make(map[string]*AdapterInfo),
				storageDownloader: &StorageDownloader{logger: getTestLogger()},
				db:                database,
				logger:            getTestLogger(),
			}
			info := &AdapterInfo{TaskID: taskID, AdapterName: "ft-signer-" + tc.name}

			err := m.downloadFromStorage(context.Background(), info)
			if err == nil {
				t.Fatal("expected a refusal for an unusable TEE signer address")
			}
			if !contains(err.Error(), "no usable TEE signer address") {
				t.Errorf("error = %q, want the signer refusal", err.Error())
			}
			// The remediation must be actionable, not "wait for the fine-tuning broker".
			if !contains(err.Error(), "/internal/v1/adapter-keys") {
				t.Errorf("error = %q, want it to name the push endpoint an operator can call", err.Error())
			}
		})
	}
}

// A refused adapter is left in Failed, and downloadFromStorage has removed its
// files — so the remediation the refusal names is only real if a Failed adapter
// can re-enter the DOWNLOAD path, not just the deploy step. UserDeployAdapter used
// to send Failed straight to deployToVLLM, which could never succeed against a
// removed directory, making every refusal terminal and recoverable only by hand.
func TestUserDeployAdapter_FailedAdapterReEntersDownload(t *testing.T) {
	database := setupTestDB(t)

	const taskID = "task-failed-recovers"
	const adapterName = "ft-failed-recovers"
	// No signer: the first attempt is refused, exactly as an upgraded broker would
	// refuse a key row that predates the column.
	if err := database.CreateAdapterKey(&model.AdapterKey{
		TaskID:         taskID,
		StorageHash:    "0x9999999999999999999999999999999999999999999999999999999999999999",
		ProviderEncKey: "0xabcdef",
	}); err != nil {
		t.Fatalf("seed adapter key: %v", err)
	}

	m := &Manager{
		adapters:          make(map[string]*AdapterInfo),
		storageDownloader: &StorageDownloader{logger: getTestLogger()},
		db:                database,
		logger:            getTestLogger(),
		ctx:               context.Background(),
	}
	// A path that does not exist, i.e. the state downloadFromStorage leaves behind.
	m.adapters[adapterName] = &AdapterInfo{
		TaskID:      taskID,
		AdapterName: adapterName,
		AdapterPath: filepath.Join(t.TempDir(), "not-downloaded"),
		State:       model.AdapterStateFailed,
	}

	if err := m.UserDeployAdapter(context.Background(), adapterName); err != nil {
		t.Fatalf("UserDeployAdapter on a Failed adapter: %v", err)
	}

	// The worker runs in a goroutine. UserDeployAdapter set the state to Loading
	// synchronously, so wait for it to settle back to Failed: the key still has no
	// signer, so the re-download is refused again.
	//
	// That it settles at all is the discriminator. sllmClient is nil here, so if the
	// state machine had gone straight to deployToVLLM — what it did before this
	// change — the goroutine would have panicked on the nil client and taken the
	// test binary with it. Reaching Failed cleanly means downloadFromStorage ran and
	// refused first, i.e. the download path was re-entered.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.GetAdapter(adapterName); got != nil && got.State == model.AdapterStateFailed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	got := m.GetAdapter(adapterName)
	if got == nil {
		t.Fatal("adapter vanished")
	}
	if got.State != model.AdapterStateFailed {
		t.Errorf("state = %s, want failed after a refused re-download", got.State)
	}
}
