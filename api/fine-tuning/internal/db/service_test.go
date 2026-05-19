//go:build integration

package db

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
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
	if err := gdb.AutoMigrate(&Task{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return &DB{db: gdb}
}

func insertTask(t *testing.T, d *DB, userAddress, progress string) *uuid.UUID {
	t.Helper()
	id := uuid.New()
	task := &Task{
		ID:                  &id,
		UserAddress:         userAddress,
		UserPublicKey:       "0xpub",
		PreTrainedModelHash: "0xmodel",
		DatasetHash:         "0xdata",
		TrainingParams:      "{}",
		Fee:                 "0x0",
		Nonce:               "0x0",
		Signature:           "0xsig",
		Progress:            progress,
	}
	if err := d.AddTask(task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return &id
}

// TestCancelTask_OnlyAffectsTargetedRow is a regression guard for a
// data-integrity bug: an earlier version of DB.CancelTask's WHERE clause
// omitted "id = ?", meaning a cancel request for task A would flip every
// cancellable task owned by the same user (including sibling B) to Failed.
// The fixed query targets a single row; this test asserts exactly that.
func TestCancelTask_OnlyAffectsTargetedRow(t *testing.T) {
	d := setupTestDB(t)

	user := "0xUser"
	// Three siblings owned by the same user, all in a cancellable state.
	targetID := insertTask(t, d, user, ProgressStateInit.String())
	siblingA := insertTask(t, d, user, ProgressStateSettingUp.String())
	siblingB := insertTask(t, d, user, ProgressStateSetUp.String())
	// A different user's task in a cancellable state — must also be untouched.
	otherUserID := insertTask(t, d, "0xOther", ProgressStateInit.String())

	if err := d.CancelTask(targetID, user); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	// Only the targeted task should be Failed; the others keep their state.
	assertProgress(t, d, targetID, ProgressStateFailed.String())
	assertProgress(t, d, siblingA, ProgressStateSettingUp.String())
	assertProgress(t, d, siblingB, ProgressStateSetUp.String())
	assertProgress(t, d, otherUserID, ProgressStateInit.String())
}

// TestCancelTask_RowsAffectedZeroReturnsRecordNotFound exercises the
// sentinel contract with db/ctrl: RowsAffected==0 (task missing or in a
// non-cancellable state) must surface as gorm.ErrRecordNotFound so callers
// can distinguish it from driver errors.
func TestCancelTask_RowsAffectedZeroReturnsRecordNotFound(t *testing.T) {
	d := setupTestDB(t)

	cases := []struct {
		name string
		seed func() *uuid.UUID
	}{
		{
			name: "missing task",
			seed: func() *uuid.UUID { id := uuid.New(); return &id },
		},
		{
			name: "non-cancellable state (Trained)",
			seed: func() *uuid.UUID {
				return insertTask(t, d, "0xUser", ProgressStateTrained.String())
			},
		},
		{
			name: "wrong owner",
			seed: func() *uuid.UUID {
				return insertTask(t, d, "0xOwner", ProgressStateInit.String())
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.seed()
			err := d.CancelTask(id, "0xUser")
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("want gorm.ErrRecordNotFound, got %v", err)
			}
		})
	}
}

func assertProgress(t *testing.T, d *DB, id *uuid.UUID, want string) {
	t.Helper()
	task, err := d.GetTask(id)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", id, err)
	}
	if task.Progress != want {
		t.Fatalf("task %s progress = %q, want %q", id, task.Progress, want)
	}
}
