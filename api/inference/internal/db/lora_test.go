//go:build integration

package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/0glabs/0g-serving-broker/inference/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func setupTestDB(t *testing.T) *DB {
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
	return &DB{db: gdb}
}

func ptr[T any](v T) *T { return &v }

func TestLoRAAdapterCRUD(t *testing.T) {
	d := setupTestDB(t)

	now := time.Now()
	adapter := &model.LoRAAdapter{
		TaskID:          "task-001",
		UserAddress:     "0xAlice",
		BaseModel:       "Qwen2.5-7B",
		AdapterName:     "ft-Qwen2-5-7B-task-001",
		StorageRootHash: "0xabc123",
		State:           model.AdapterStateLoading,
		LastAccessAt:    &now,
		AdapterPath:     "/data/adapters/ft-Qwen2-5-7B-task-001",
		BlockNumber:     100,
	}

	if err := d.CreateLoRAAdapter(adapter); err != nil {
		t.Fatalf("CreateLoRAAdapter: %v", err)
	}

	got, err := d.GetLoRAAdapterByName("ft-Qwen2-5-7B-task-001")
	if err != nil {
		t.Fatalf("GetLoRAAdapterByName: %v", err)
	}
	if got.TaskID != "task-001" || got.UserAddress != "0xAlice" || got.BlockNumber != 100 {
		t.Errorf("unexpected adapter: taskID=%s user=%s block=%d", got.TaskID, got.UserAddress, got.BlockNumber)
	}

	got2, err := d.GetLoRAAdapterByTaskID("task-001")
	if err != nil {
		t.Fatalf("GetLoRAAdapterByTaskID: %v", err)
	}
	if got2.AdapterName != "ft-Qwen2-5-7B-task-001" {
		t.Errorf("expected adapter name ft-Qwen2-5-7B-task-001, got %s", got2.AdapterName)
	}

	if err := d.UpdateLoRAAdapterState("ft-Qwen2-5-7B-task-001", model.AdapterStateActive); err != nil {
		t.Fatalf("UpdateLoRAAdapterState: %v", err)
	}
	got3, _ := d.GetLoRAAdapterByName("ft-Qwen2-5-7B-task-001")
	if got3.State != model.AdapterStateActive {
		t.Errorf("state = %s, want active", got3.State)
	}

	if err := d.UpdateLoRAAdapterPath("ft-Qwen2-5-7B-task-001", "/new/path"); err != nil {
		t.Fatalf("UpdateLoRAAdapterPath: %v", err)
	}
	got4, _ := d.GetLoRAAdapterByName("ft-Qwen2-5-7B-task-001")
	if got4.AdapterPath != "/new/path" {
		t.Errorf("path = %s, want /new/path", got4.AdapterPath)
	}

	if err := d.UpdateLoRAAdapterAccess("ft-Qwen2-5-7B-task-001"); err != nil {
		t.Fatalf("UpdateLoRAAdapterAccess: %v", err)
	}

	if err := d.DeleteLoRAAdapter("ft-Qwen2-5-7B-task-001"); err != nil {
		t.Fatalf("DeleteLoRAAdapter: %v", err)
	}
	_, err = d.GetLoRAAdapterByName("ft-Qwen2-5-7B-task-001")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestListLoRAAdapters(t *testing.T) {
	d := setupTestDB(t)

	for _, name := range []string{"a1", "a2", "a3"} {
		d.CreateLoRAAdapter(&model.LoRAAdapter{
			TaskID: name, UserAddress: "0xAlice", BaseModel: "base",
			AdapterName: name, StorageRootHash: "0x1", State: model.AdapterStateActive,
		})
	}
	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "b1", UserAddress: "0xBob", BaseModel: "base",
		AdapterName: "b1", StorageRootHash: "0x2", State: model.AdapterStateActive,
	})

	all, err := d.ListLoRAAdapters()
	if err != nil {
		t.Fatalf("ListLoRAAdapters: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("expected 4, got %d", len(all))
	}

	alice, err := d.ListLoRAAdaptersByUser("0xAlice")
	if err != nil {
		t.Fatalf("ListLoRAAdaptersByUser: %v", err)
	}
	if len(alice) != 3 {
		t.Errorf("expected 3 for Alice, got %d", len(alice))
	}

	bob, err := d.ListLoRAAdaptersByUser("0xBob")
	if err != nil {
		t.Fatalf("ListLoRAAdaptersByUser: %v", err)
	}
	if len(bob) != 1 {
		t.Errorf("expected 1 for Bob, got %d", len(bob))
	}
}

func TestGetLastProcessedBlock(t *testing.T) {
	d := setupTestDB(t)

	block, err := d.GetLastProcessedBlock()
	if err != nil {
		t.Fatalf("empty table: %v", err)
	}
	if block != 0 {
		t.Errorf("expected 0 for empty table, got %d", block)
	}

	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "t1", UserAddress: "0x1", BaseModel: "b", AdapterName: "a1",
		StorageRootHash: "0x1", State: model.AdapterStateActive, BlockNumber: 50,
	})
	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "t2", UserAddress: "0x1", BaseModel: "b", AdapterName: "a2",
		StorageRootHash: "0x1", State: model.AdapterStateActive, BlockNumber: 200,
	})
	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "t3", UserAddress: "0x1", BaseModel: "b", AdapterName: "a3",
		StorageRootHash: "0x1", State: model.AdapterStateActive, BlockNumber: 100,
	})

	block, err = d.GetLastProcessedBlock()
	if err != nil {
		t.Fatalf("GetLastProcessedBlock: %v", err)
	}
	if block != 200 {
		t.Errorf("expected 200, got %d", block)
	}
}

func TestListIdleAdapters(t *testing.T) {
	d := setupTestDB(t)

	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-5 * time.Minute)

	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "idle1", UserAddress: "0x1", BaseModel: "b", AdapterName: "idle1",
		StorageRootHash: "0x1", State: model.AdapterStateActive, LastAccessAt: &old,
	})
	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "active1", UserAddress: "0x1", BaseModel: "b", AdapterName: "active1",
		StorageRootHash: "0x1", State: model.AdapterStateActive, LastAccessAt: &recent,
	})
	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "nil-access", UserAddress: "0x1", BaseModel: "b", AdapterName: "nil-access",
		StorageRootHash: "0x1", State: model.AdapterStateActive, LastAccessAt: nil,
	})
	d.CreateLoRAAdapter(&model.LoRAAdapter{
		TaskID: "loading1", UserAddress: "0x1", BaseModel: "b", AdapterName: "loading1",
		StorageRootHash: "0x1", State: model.AdapterStateLoading, LastAccessAt: &old,
	})

	idle, err := d.ListIdleAdapters(1 * time.Hour)
	if err != nil {
		t.Fatalf("ListIdleAdapters: %v", err)
	}

	names := make(map[string]bool)
	for _, a := range idle {
		names[a.AdapterName] = true
	}

	if !names["idle1"] {
		t.Error("expected idle1 (old access, active state) to be idle")
	}
	if names["nil-access"] {
		t.Error("nil-access (null last_access_at) should NOT be idle — newly deployed, never accessed")
	}
	if names["active1"] {
		t.Error("active1 (recent access) should NOT be idle")
	}
	if names["loading1"] {
		t.Error("loading1 (non-active state) should NOT be idle")
	}
}

func TestAdapterKeyCRUD(t *testing.T) {
	d := setupTestDB(t)

	key := &model.AdapterKey{
		TaskID:         "task-key-001",
		StorageHash:    "0xdeadbeef",
		ProviderEncKey: "0xencryptedkey123",
	}

	if err := d.CreateAdapterKey(key); err != nil {
		t.Fatalf("CreateAdapterKey: %v", err)
	}

	got, err := d.GetAdapterKeyByTaskID("task-key-001")
	if err != nil {
		t.Fatalf("GetAdapterKeyByTaskID: %v", err)
	}
	if got.StorageHash != "0xdeadbeef" || got.ProviderEncKey != "0xencryptedkey123" {
		t.Errorf("unexpected key: hash=%s key=%s", got.StorageHash, got.ProviderEncKey)
	}

	_, err = d.GetAdapterKeyByTaskID("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent key")
	}
}

// TestAdapterKeyIdempotent reproduces the production failure mode reported in
// May 2026 (Bug Report #3): the fine-tuning broker retries pushAdapterKey on
// transient HTTP errors, hitting the inference broker's
// CreateAdapterKey twice for the same TaskID. With a plain Create() the
// second call fails with `Duplicate entry … for key 'task_id'` and the
// retry loop dead-locks with HTTP 500. Upsert must instead overwrite the
// existing row and return nil so the deliver pipeline can advance.
func TestAdapterKeyIdempotent(t *testing.T) {
	d := setupTestDB(t)

	first := &model.AdapterKey{
		TaskID:         "task-key-idem",
		StorageHash:    "0x1111111111111111111111111111111111111111111111111111111111111111",
		ProviderEncKey: "0xenckey-v1",
	}
	if err := d.CreateAdapterKey(first); err != nil {
		t.Fatalf("first CreateAdapterKey: %v", err)
	}

	// Second call with *same* TaskID but updated hash + key (this is what
	// the fine-tuning broker does on a retry after, e.g., re-encrypting
	// the AES key). Must NOT return a duplicate-key error.
	second := &model.AdapterKey{
		TaskID:         "task-key-idem",
		StorageHash:    "0x2222222222222222222222222222222222222222222222222222222222222222",
		ProviderEncKey: "0xenckey-v2",
	}
	if err := d.CreateAdapterKey(second); err != nil {
		t.Fatalf("idempotent CreateAdapterKey (second push): got error %v, want nil", err)
	}

	// The latest values must win.
	got, err := d.GetAdapterKeyByTaskID("task-key-idem")
	if err != nil {
		t.Fatalf("GetAdapterKeyByTaskID after upsert: %v", err)
	}
	if got.StorageHash != second.StorageHash {
		t.Errorf("StorageHash = %q, want %q (upsert should overwrite)", got.StorageHash, second.StorageHash)
	}
	if got.ProviderEncKey != second.ProviderEncKey {
		t.Errorf("ProviderEncKey = %q, want %q (upsert should overwrite)", got.ProviderEncKey, second.ProviderEncKey)
	}

	// Third call repeating identical payload (the most common retry shape:
	// the fine-tuning broker retries because the network blipped, but the
	// payload itself is unchanged). Must also be a no-op success.
	if err := d.CreateAdapterKey(second); err != nil {
		t.Fatalf("idempotent CreateAdapterKey (third push, identical payload): %v", err)
	}
}

// A row written before tee_signer_address existed has it empty, and lora.Manager
// then refuses to deploy the adapter and tells the operator to have the
// fine-tuning broker re-push. That remediation only works if the upsert actually
// assigns the column: FirstOrCreate's found-branch issues Updates() with the
// assigned columns only, and its preceding Find has already overwritten the
// struct's field with the stored value — so omitting it from Assign makes the
// re-push a silent no-op and strands the adapter permanently.
func TestAdapterKeyRePushFillsTeeSignerAddress(t *testing.T) {
	d := setupTestDB(t)

	const taskID = "task-key-repush-signer"
	legacy := &model.AdapterKey{
		TaskID:         taskID,
		StorageHash:    "0x3333333333333333333333333333333333333333333333333333333333333333",
		ProviderEncKey: "0xenckey",
	}
	if err := d.CreateAdapterKey(legacy); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	got, err := d.GetAdapterKeyByTaskID(taskID)
	if err != nil {
		t.Fatalf("read back seed: %v", err)
	}
	if got.TeeSignerAddress != "" {
		t.Fatalf("setup: want an empty signer on the legacy row, got %q", got.TeeSignerAddress)
	}

	const wantSigner = "0x71562b71999873DB5b286dF957af199Ec94617F7"
	repush := &model.AdapterKey{
		TaskID:           taskID,
		StorageHash:      legacy.StorageHash,
		ProviderEncKey:   legacy.ProviderEncKey,
		TeeSignerAddress: wantSigner,
	}
	if err := d.CreateAdapterKey(repush); err != nil {
		t.Fatalf("re-push: %v", err)
	}

	got, err = d.GetAdapterKeyByTaskID(taskID)
	if err != nil {
		t.Fatalf("read back after re-push: %v", err)
	}
	if got.TeeSignerAddress != wantSigner {
		t.Errorf("TeeSignerAddress = %q, want %q; the documented re-push remediation is a no-op", got.TeeSignerAddress, wantSigner)
	}

	// And a later re-push must be able to REPLACE it, which is what an
	// enclave-image change requires.
	const rotated = "0x1e65079A4d283D1890071140dFAf0af203DebCF2"
	if err := d.CreateAdapterKey(&model.AdapterKey{
		TaskID:           taskID,
		StorageHash:      legacy.StorageHash,
		ProviderEncKey:   legacy.ProviderEncKey,
		TeeSignerAddress: rotated,
	}); err != nil {
		t.Fatalf("rotating re-push: %v", err)
	}
	got, err = d.GetAdapterKeyByTaskID(taskID)
	if err != nil {
		t.Fatalf("read back after rotation: %v", err)
	}
	if got.TeeSignerAddress != rotated {
		t.Errorf("TeeSignerAddress = %q, want %q after a rotating re-push", got.TeeSignerAddress, rotated)
	}
}

// A push that omits teeSignerAddress must not WIPE a signer already stored. The
// field is optional at the handler (an older fine-tuning broker does not send it),
// and it is in the upsert's Assign set so a re-push can fill and rotate it — those
// two together would let a stale-version push overwrite a good address with the
// empty string and strand a working adapter.
func TestAdapterKeyPushWithoutSignerDoesNotWipeIt(t *testing.T) {
	d := setupTestDB(t)

	const taskID = "task-key-no-wipe"
	const signer = "0x71562b71999873DB5b286dF957af199Ec94617F7"
	if err := d.CreateAdapterKey(&model.AdapterKey{
		TaskID:           taskID,
		StorageHash:      "0x4444444444444444444444444444444444444444444444444444444444444444",
		ProviderEncKey:   "0xenckey",
		TeeSignerAddress: signer,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The same task, same artifact bytes, pushed again by a broker that predates the
	// field. storageHash is unchanged, so the stored signer still describes these
	// bytes and must survive.
	if err := d.CreateAdapterKey(&model.AdapterKey{
		TaskID:         taskID,
		StorageHash:    "0x4444444444444444444444444444444444444444444444444444444444444444",
		ProviderEncKey: "0xenckey-v2",
	}); err != nil {
		t.Fatalf("push without signer: %v", err)
	}

	got, err := d.GetAdapterKeyByTaskID(taskID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.TeeSignerAddress != signer {
		t.Errorf("TeeSignerAddress = %q, want %q preserved; a same-artifact push without the field wiped a good signer", got.TeeSignerAddress, signer)
	}
	// The fields the push DID carry must still have been applied.
	if got.ProviderEncKey != "0xenckey-v2" {
		t.Errorf("ProviderEncKey = %q, want the pushed value", got.ProviderEncKey)
	}
}

// The converse: a push that CHANGES the artifact without supplying a signer must
// not leave the row describing artifact B with signer A. Keeping the stale signer
// makes verification fail as "signer mismatch", which reads as tampering; clearing
// it fails closed with the documented, recoverable "no usable TEE signer" error.
func TestAdapterKeyPushWithNewArtifactClearsStaleSigner(t *testing.T) {
	d := setupTestDB(t)

	const taskID = "task-key-stale-signer"
	const oldHash = "0x6666666666666666666666666666666666666666666666666666666666666666"
	const newHash = "0x7777777777777777777777777777777777777777777777777777777777777777"
	if err := d.CreateAdapterKey(&model.AdapterKey{
		TaskID:           taskID,
		StorageHash:      oldHash,
		ProviderEncKey:   "0xenckey-a",
		TeeSignerAddress: "0x71562b71999873DB5b286dF957af199Ec94617F7",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Re-encrypted by a rolled-back broker: new artifact, new AES key, no signer.
	if err := d.CreateAdapterKey(&model.AdapterKey{
		TaskID:         taskID,
		StorageHash:    newHash,
		ProviderEncKey: "0xenckey-b",
	}); err != nil {
		t.Fatalf("push new artifact without signer: %v", err)
	}

	got, err := d.GetAdapterKeyByTaskID(taskID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.StorageHash != newHash || got.ProviderEncKey != "0xenckey-b" {
		t.Errorf("artifact fields not applied: hash=%q key=%q", got.StorageHash, got.ProviderEncKey)
	}
	if got.TeeSignerAddress != "" {
		t.Errorf("TeeSignerAddress = %q, want cleared; a signer describing the previous artifact was kept alongside a new one", got.TeeSignerAddress)
	}
}
