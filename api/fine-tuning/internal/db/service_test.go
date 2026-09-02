//go:build integration

package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// The two filters that take an address from the caller rather than from a row. Both
// are HIGH-severity if they stay byte-exact, and in opposite directions:
//
//   - UnFinishedTaskCount gates "one task per user at a time" (ctrl.validateNoUnfinishedTasks).
//     Miss the row and the gate opens: create with signer.address.toLowerCase(), then
//     again with the EIP-55 form, and a case-sensitive collation counts zero both times.
//   - ListTask is the primary read route. Miss the row and the user who created a task
//     with one spelling is shown an empty list with the other — which is the exact
//     symptom this PR exists to fix, on the busiest path.
//
// This has to be an integration test. Whether "0xAbC" = "0xabc" holds is decided by the
// column's collation, so a unit test with a hand-rolled matcher would assert about
// nothing. MySQL's default utf8mb4_0900_ai_ci is case-INSENSITIVE, which is precisely
// why the bug can pass review and then bite on a deployment configured otherwise —
// LOWER() takes the collation out of the answer.
func TestAddressFiltersFindARowStoredInAnotherSpelling(t *testing.T) {
	d := setupTestDB(t)

	const lower = "0xabcdef1111111111111111111111111111111111"
	const eip55 = "0xAbCdEf1111111111111111111111111111111111"
	const bare = "abcdef1111111111111111111111111111111111"
	const upper = "0xABCDEF1111111111111111111111111111111111"

	// One task per stored spelling Bind can produce, all for the SAME account.
	insertTask(t, d, lower, ProgressStateInit.String())
	insertTask(t, d, eip55, ProgressStateSettingUp.String())
	insertTask(t, d, bare, ProgressStateSetUp.String())
	// A different account, which must never be counted or listed for the one above.
	insertTask(t, d, "0xabcdef1111111111111111111111111111111112", ProgressStateInit.String())

	// Queried with every spelling, because the caller chooses it independently of
	// whatever created the row.
	for _, queried := range []string{lower, eip55, bare, upper} {
		count, err := d.UnFinishedTaskCount(queried)
		if err != nil {
			t.Fatalf("UnFinishedTaskCount(%q): %v", queried, err)
		}
		if count != 3 {
			t.Errorf("UnFinishedTaskCount(%q) = %d, want 3: the gate on one task per user opens for every spelling it misses", queried, count)
		}

		tasks, err := d.ListTask(queried, false, false)
		if err != nil {
			t.Fatalf("ListTask(%q): %v", queried, err)
		}
		if len(tasks) != 3 {
			t.Errorf("ListTask(%q) = %d task(s), want 3: a user must see their own tasks whichever spelling they ask with", queried, len(tasks))
		}
		for _, task := range tasks {
			if !strings.EqualFold(strings.TrimPrefix(task.UserAddress, "0x"), bare) {
				t.Errorf("ListTask(%q) returned a task owned by %q", queried, task.UserAddress)
			}
		}
	}

	// And the finished states are still excluded, so the fix did not widen the filter
	// into ignoring progress.
	insertTask(t, d, lower, ProgressStateFinished.String())
	insertTask(t, d, eip55, ProgressStateFailed.String())
	count, err := d.UnFinishedTaskCount(upper)
	if err != nil {
		t.Fatalf("UnFinishedTaskCount: %v", err)
	}
	if count != 3 {
		t.Errorf("UnFinishedTaskCount = %d after adding a finished and a failed task, want 3", count)
	}
}
