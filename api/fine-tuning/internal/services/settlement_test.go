package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/google/uuid"
	logrus "github.com/sirupsen/logrus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// --- test logger (implements log.Logger) ---

type testLogger struct{ inner *logrus.Logger }

func newTestLogger() log.Logger                                  { return &testLogger{inner: logrus.New()} }
func (l *testLogger) WithFields(fields logrus.Fields) log.Logger { return l }
func (l *testLogger) InnerLogger() *logrus.Logger                { return l.inner }
func (l *testLogger) Debugf(f string, a ...interface{})          { l.inner.Debugf(f, a...) }
func (l *testLogger) Infof(f string, a ...interface{})           { l.inner.Infof(f, a...) }
func (l *testLogger) Printf(f string, a ...interface{})          { l.inner.Printf(f, a...) }
func (l *testLogger) Warnf(f string, a ...interface{})           { l.inner.Warnf(f, a...) }
func (l *testLogger) Warningf(f string, a ...interface{})        { l.inner.Warningf(f, a...) }
func (l *testLogger) Errorf(f string, a ...interface{})          { l.inner.Errorf(f, a...) }
func (l *testLogger) Fatalf(f string, a ...interface{})          { l.inner.Fatalf(f, a...) }
func (l *testLogger) Panicf(f string, a ...interface{})          { l.inner.Panicf(f, a...) }
func (l *testLogger) Debug(a ...interface{})                     { l.inner.Debug(a...) }
func (l *testLogger) Info(a ...interface{})                      { l.inner.Info(a...) }
func (l *testLogger) Print(a ...interface{})                     { l.inner.Print(a...) }
func (l *testLogger) Warn(a ...interface{})                      { l.inner.Warn(a...) }
func (l *testLogger) Warning(a ...interface{})                   { l.inner.Warning(a...) }
func (l *testLogger) Error(a ...interface{})                     { l.inner.Error(a...) }
func (l *testLogger) Fatal(a ...interface{})                     { l.inner.Fatal(a...) }
func (l *testLogger) Panic(a ...interface{})                     { l.inner.Panic(a...) }
func (l *testLogger) Debugln(a ...interface{})                   { l.inner.Debugln(a...) }
func (l *testLogger) Infoln(a ...interface{})                    { l.inner.Infoln(a...) }
func (l *testLogger) Println(a ...interface{})                   { l.inner.Println(a...) }
func (l *testLogger) Warnln(a ...interface{})                    { l.inner.Warnln(a...) }
func (l *testLogger) Warningln(a ...interface{})                 { l.inner.Warningln(a...) }
func (l *testLogger) Errorln(a ...interface{})                   { l.inner.Errorln(a...) }
func (l *testLogger) Fatalln(a ...interface{})                   { l.inner.Fatalln(a...) }
func (l *testLogger) Panicln(a ...interface{})                   { l.inner.Panicln(a...) }

// --- mock contract ---

type mockContract struct {
	deliverable    contract.Deliverable
	deliverableErr error
	settleFeesErr  error
	settleCount    int
}

func (m *mockContract) GetDeliverable(_ context.Context, _ ethcommon.Address, _ string) (contract.Deliverable, error) {
	return m.deliverable, m.deliverableErr
}

func (m *mockContract) SettleFees(_ context.Context, _ contract.VerifierInput) error {
	m.settleCount++
	return m.settleFeesErr
}

func (m *mockContract) GetLockTime(_ context.Context) (int64, error) {
	return 86400, nil
}

func (m *mockContract) ChainID(_ context.Context) (*big.Int, error) {
	return big.NewInt(16602), nil
}

func (m *mockContract) ContractAddr() string {
	return "0x0000000000000000000000000000000000000001"
}

// --- mock tee signer ---

type mockTeeSigner struct{}

func (m *mockTeeSigner) SignEIP712(_ []byte) ([]byte, error) {
	return make([]byte, 65), nil
}

// --- test DB helper ---

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := gormDB.AutoMigrate(&db.Task{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db.NewDBForTest(gormDB, newTestLogger())
}

func createTestTask(t *testing.T, d *db.DB, progress db.ProgressState) *uuid.UUID {
	t.Helper()
	id := uuid.New()
	task := &db.Task{
		ID:                  &id,
		UserAddress:         "0x1234567890abcdef1234567890abcdef12345678",
		Progress:            progress.String(),
		Fee:                 "100",
		Nonce:               "42",
		Signature:           "0xdead",
		PreTrainedModelHash: "hash",
		DatasetHash:         "hash",
		TrainingParams:      "{}",
		OutputRootHash:      hexutil.Encode(make([]byte, 32)),
		EncryptedSecret:     hexutil.Encode(make([]byte, 16)),
	}
	if err := d.AddTask(task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	return &id
}

func newTestSettlement(d *db.DB, mc *mockContract) *Settlement {
	return &Settlement{
		db:         d,
		contract:   mc,
		teeService: &mockTeeSigner{},
		config: SettlementConfig{
			MaxNumRetriesPerTask: 3,
			SettlementBatchSize:  10,
		},
		logger: newTestLogger(),
	}
}

// --- Bug 2 tests ---

func TestDoSettlement_AlreadySettledOnChain_SkipsSettleFees(t *testing.T) {
	testDB := newTestDB(t)
	taskID := createTestTask(t, testDB, db.ProgressStateUserAcknowledged)

	mc := &mockContract{
		deliverable: contract.Deliverable{Settled: true},
	}
	s := newTestSettlement(testDB, mc)

	task, err := testDB.GetTask(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	err = s.doSettlement(context.Background(), &task, true)
	if err != nil {
		t.Fatalf("doSettlement error: %v", err)
	}

	if mc.settleCount != 0 {
		t.Fatalf("expected SettleFees NOT called, but was called %d times", mc.settleCount)
	}

	updated, _ := testDB.GetTask(taskID)
	if updated.Progress != db.ProgressStateFinished.String() {
		t.Fatalf("expected progress=Finished, got %s", updated.Progress)
	}
}

func TestDoSettlement_NotSettledOnChain_CallsSettleFees(t *testing.T) {
	testDB := newTestDB(t)
	taskID := createTestTask(t, testDB, db.ProgressStateUserAcknowledged)

	mc := &mockContract{
		deliverable: contract.Deliverable{Settled: false},
	}
	s := newTestSettlement(testDB, mc)

	task, err := testDB.GetTask(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	err = s.doSettlement(context.Background(), &task, true)
	if err != nil {
		t.Fatalf("doSettlement error: %v", err)
	}

	if mc.settleCount != 1 {
		t.Fatalf("expected SettleFees called once, got %d", mc.settleCount)
	}

	updated, _ := testDB.GetTask(taskID)
	if updated.Progress != db.ProgressStateFinished.String() {
		t.Fatalf("expected progress=Finished, got %s", updated.Progress)
	}
}

func TestDoSettlement_GetDeliverableFails_ContinuesNormally(t *testing.T) {
	testDB := newTestDB(t)
	taskID := createTestTask(t, testDB, db.ProgressStateUserAcknowledged)

	mc := &mockContract{
		deliverableErr: context.DeadlineExceeded,
		deliverable:    contract.Deliverable{},
	}
	s := newTestSettlement(testDB, mc)

	task, err := testDB.GetTask(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	err = s.doSettlement(context.Background(), &task, true)
	if err != nil {
		t.Fatalf("doSettlement error: %v", err)
	}

	// Should still call SettleFees despite GetDeliverable error
	if mc.settleCount != 1 {
		t.Fatalf("expected SettleFees called once, got %d", mc.settleCount)
	}
}

func TestTrySettle_SettlementFails_RetryWithCorrectState(t *testing.T) {
	testDB := newTestDB(t)
	taskID := createTestTask(t, testDB, db.ProgressStateDelivered)

	mc := &mockContract{
		deliverable:   contract.Deliverable{Settled: false},
		settleFeesErr: context.DeadlineExceeded,
	}
	s := newTestSettlement(testDB, mc)

	task, _ := testDB.GetTask(taskID)

	// trySettle should handle the error and increment retry counter
	_ = s.trySettle(context.Background(), task, false)

	updated, _ := testDB.GetTask(taskID)
	if updated.SettlementRetries != 1 {
		t.Fatalf("expected SettlementRetries=1, got %d", updated.SettlementRetries)
	}
	if updated.Progress != db.ProgressStateDelivered.String() {
		t.Fatalf("expected progress=Delivered (unchanged), got %s", updated.Progress)
	}
}

func TestTrySettle_MaxRetriesExhausted_TaskMarkedFailed(t *testing.T) {
	testDB := newTestDB(t)
	taskID := createTestTask(t, testDB, db.ProgressStateDelivered)

	mc := &mockContract{
		deliverable:   contract.Deliverable{Settled: false},
		settleFeesErr: context.DeadlineExceeded,
	}
	s := newTestSettlement(testDB, mc)
	s.config.MaxNumRetriesPerTask = 2

	for i := 0; i < 3; i++ {
		task, _ := testDB.GetTask(taskID)
		_ = s.trySettle(context.Background(), task, false)
	}

	updated, _ := testDB.GetTask(taskID)
	if updated.Progress != db.ProgressStateFailed.String() {
		t.Fatalf("expected progress=Failed after exhausting retries, got %s", updated.Progress)
	}
}
