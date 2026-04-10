package db

import (
	"testing"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/google/uuid"
	logrus "github.com/sirupsen/logrus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type testLogger struct {
	inner *logrus.Logger
}

func newTestLogger() log.Logger                             { return &testLogger{inner: logrus.New()} }
func (l *testLogger) WithFields(fields logrus.Fields) log.Logger { return l }
func (l *testLogger) InnerLogger() *logrus.Logger           { return l.inner }
func (l *testLogger) Debugf(f string, a ...interface{})     { l.inner.Debugf(f, a...) }
func (l *testLogger) Infof(f string, a ...interface{})      { l.inner.Infof(f, a...) }
func (l *testLogger) Printf(f string, a ...interface{})     { l.inner.Printf(f, a...) }
func (l *testLogger) Warnf(f string, a ...interface{})      { l.inner.Warnf(f, a...) }
func (l *testLogger) Warningf(f string, a ...interface{})   { l.inner.Warningf(f, a...) }
func (l *testLogger) Errorf(f string, a ...interface{})     { l.inner.Errorf(f, a...) }
func (l *testLogger) Fatalf(f string, a ...interface{})     { l.inner.Fatalf(f, a...) }
func (l *testLogger) Panicf(f string, a ...interface{})     { l.inner.Panicf(f, a...) }
func (l *testLogger) Debug(a ...interface{})                { l.inner.Debug(a...) }
func (l *testLogger) Info(a ...interface{})                 { l.inner.Info(a...) }
func (l *testLogger) Print(a ...interface{})                { l.inner.Print(a...) }
func (l *testLogger) Warn(a ...interface{})                 { l.inner.Warn(a...) }
func (l *testLogger) Warning(a ...interface{})              { l.inner.Warning(a...) }
func (l *testLogger) Error(a ...interface{})                { l.inner.Error(a...) }
func (l *testLogger) Fatal(a ...interface{})                { l.inner.Fatal(a...) }
func (l *testLogger) Panic(a ...interface{})                { l.inner.Panic(a...) }
func (l *testLogger) Debugln(a ...interface{})              { l.inner.Debugln(a...) }
func (l *testLogger) Infoln(a ...interface{})               { l.inner.Infoln(a...) }
func (l *testLogger) Println(a ...interface{})              { l.inner.Println(a...) }
func (l *testLogger) Warnln(a ...interface{})               { l.inner.Warnln(a...) }
func (l *testLogger) Warningln(a ...interface{})            { l.inner.Warningln(a...) }
func (l *testLogger) Errorln(a ...interface{})              { l.inner.Errorln(a...) }
func (l *testLogger) Fatalln(a ...interface{})              { l.inner.Fatalln(a...) }
func (l *testLogger) Panicln(a ...interface{})              { l.inner.Panicln(a...) }

func newTestDB(t *testing.T) *DB {
	t.Helper()
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,
		},
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := gormDB.AutoMigrate(&Task{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return &DB{db: gormDB, logger: newTestLogger()}
}

func createTestTask(t *testing.T, d *DB, progress ProgressState) *uuid.UUID {
	t.Helper()
	id := uuid.New()
	task := &Task{
		ID:                  &id,
		UserAddress:         "0x1234567890abcdef",
		Progress:            progress.String(),
		Fee:                 "100",
		Nonce:               "1",
		Signature:           "0xdead",
		PreTrainedModelHash: "hash",
		DatasetHash:         "hash",
		TrainingParams:      "{}",
	}
	if err := d.AddTask(task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	return &id
}

func TestHandleSettlementFailure_DeliveredTask_RetryIncrements(t *testing.T) {
	d := newTestDB(t)
	taskID := createTestTask(t, d, ProgressStateDelivered)

	task, err := d.GetTask(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	retried, err := d.HandleSettlementFailure(&task, 3, ProgressStateDelivered)
	if err != nil {
		t.Fatalf("HandleSettlementFailure error: %v", err)
	}
	if !retried {
		t.Fatal("expected retried=true")
	}

	updated, _ := d.GetTask(taskID)
	if updated.SettlementRetries != 1 {
		t.Fatalf("expected SettlementRetries=1, got %d", updated.SettlementRetries)
	}
	if updated.Progress != ProgressStateDelivered.String() {
		t.Fatalf("expected progress=Delivered, got %s", updated.Progress)
	}
}

func TestHandleSettlementFailure_UserAcknowledgedTask_RetryIncrements(t *testing.T) {
	d := newTestDB(t)
	taskID := createTestTask(t, d, ProgressStateUserAcknowledged)

	task, err := d.GetTask(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}

	retried, err := d.HandleSettlementFailure(&task, 3, ProgressStateUserAcknowledged)
	if err != nil {
		t.Fatalf("HandleSettlementFailure error: %v", err)
	}
	if !retried {
		t.Fatal("expected retried=true")
	}

	updated, _ := d.GetTask(taskID)
	if updated.SettlementRetries != 1 {
		t.Fatalf("expected SettlementRetries=1, got %d", updated.SettlementRetries)
	}
	if updated.Progress != ProgressStateUserAcknowledged.String() {
		t.Fatalf("expected progress=UserAcknowledged, got %s", updated.Progress)
	}
}

func TestHandleSettlementFailure_WrongProgressState_Fails(t *testing.T) {
	d := newTestDB(t)
	taskID := createTestTask(t, d, ProgressStateDelivered)

	task, _ := d.GetTask(taskID)

	// Passing ProgressStateInit (the old bug) for a Delivered task should fail
	_, err := d.HandleSettlementFailure(&task, 3, ProgressStateInit)
	if err == nil {
		t.Fatal("expected error when using wrong progress state, got nil")
	}

	// Retry counter should NOT have incremented
	updated, _ := d.GetTask(taskID)
	if updated.SettlementRetries != 0 {
		t.Fatalf("expected SettlementRetries=0 (unchanged), got %d", updated.SettlementRetries)
	}
}

func TestHandleSettlementFailure_MaxRetriesReached_MarkedFailed(t *testing.T) {
	d := newTestDB(t)
	taskID := createTestTask(t, d, ProgressStateDelivered)
	maxRetries := uint(3)

	for i := uint(0); i < maxRetries; i++ {
		task, _ := d.GetTask(taskID)
		retried, err := d.HandleSettlementFailure(&task, maxRetries, ProgressStateDelivered)
		if err != nil {
			t.Fatalf("retry %d: unexpected error: %v", i, err)
		}
		if !retried {
			t.Fatalf("retry %d: expected retried=true", i)
		}
	}

	task, _ := d.GetTask(taskID)
	retried, err := d.HandleSettlementFailure(&task, maxRetries, ProgressStateDelivered)
	if err != nil {
		t.Fatalf("final call: unexpected error: %v", err)
	}
	if retried {
		t.Fatal("expected retried=false after max retries")
	}

	task, _ = d.GetTask(taskID)
	if task.Progress != ProgressStateFailed.String() {
		t.Fatalf("expected progress=Failed, got %s", task.Progress)
	}
}

func TestHandleSettlementFailure_UserBlockedByStuckTask(t *testing.T) {
	d := newTestDB(t)
	userAddr := "0xUserBlocked"

	id := uuid.New()
	task := &Task{
		ID:                  &id,
		UserAddress:         userAddr,
		Progress:            ProgressStateDelivered.String(),
		Fee:                 "100",
		Nonce:               "1",
		Signature:           "0xdead",
		PreTrainedModelHash: "hash",
		DatasetHash:         "hash",
		TrainingParams:      "{}",
	}
	if err := d.AddTask(task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// User should be blocked from creating new tasks
	count, err := d.UnFinishedTaskCount(userAddr)
	if err != nil {
		t.Fatalf("UnFinishedTaskCount error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 unfinished task, got %d", count)
	}

	// Exhaust retries to mark task as Failed
	maxRetries := uint(2)
	for i := uint(0); i < maxRetries; i++ {
		dbTask, _ := d.GetTask(&id)
		d.HandleSettlementFailure(&dbTask, maxRetries, ProgressStateDelivered)
	}
	dbTask, _ := d.GetTask(&id)
	d.HandleSettlementFailure(&dbTask, maxRetries, ProgressStateDelivered)

	// After task is Failed, user should be unblocked
	count, err = d.UnFinishedTaskCount(userAddr)
	if err != nil {
		t.Fatalf("UnFinishedTaskCount error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unfinished tasks after failure, got %d", count)
	}
}
