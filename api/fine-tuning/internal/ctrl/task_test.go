//go:build integration

package ctrl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonerrors "github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/0glabs/0g-serving-broker/fine-tuning/schema"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormschema "gorm.io/gorm/schema"
)

// These tests live in the ctrl package because they exercise unexported
// state (Ctrl.db) without refactoring Ctrl to take an interface. They run
// under `-tags integration` to match the db-layer pattern and avoid
// blocking plain `go test` on Docker availability.

// setupCtrl spins up a mysql testcontainer, migrates the Task schema,
// and returns a Ctrl wired to it. The container is cleaned up by t.Cleanup.
func setupCtrl(t *testing.T) *Ctrl {
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
		NamingStrategy: gormschema.NamingStrategy{SingularTable: true},
	})
	if err != nil {
		t.Fatalf("connect to mysql: %v", err)
	}
	if err := gdb.AutoMigrate(&db.Task{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}

	// Ctrl.db is unexported; reach in via the same-package access this test
	// file enjoys. logger/contract/tee are not exercised by CancelTask or
	// GetLoRAModel, so we leave them zero-valued.
	return &Ctrl{db: db.NewDBFromGorm(gdb)}
}

// signTask signs task.ID with priv using the same Ethereum personal_sign
// convention as Ctrl.validateSignature. Returns the address derived from
// priv (to put in task.UserAddress) and the hex signature.
func signTask(t *testing.T, priv string, id uuid.UUID) (address, signature string) {
	t.Helper()
	pk, err := crypto.HexToECDSA(priv)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	addr := crypto.PubkeyToAddress(pk.PublicKey).Hex()

	idBytes, err := id.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal id: %v", err)
	}
	hash := accounts.TextHash(crypto.Keccak256(idBytes)[:])
	sig, err := crypto.Sign(hash, pk)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// crypto.Sign emits a raw 0/1. validateSignature now accepts that as well as
	// 27/28; this still produces the Ethereum-facing form so the test covers the
	// convention real clients send.
	sig[64] += 27
	return addr, hexutil.Encode(sig)
}

func seedTaskCtrl(t *testing.T, c *Ctrl, userAddress, progress string) *uuid.UUID {
	t.Helper()
	id := uuid.New()
	task := &db.Task{
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
	if err := c.db.AddTask(task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return &id
}

// statusOf extracts the HTTP status attached by the typed-error system.
// Returns 0 if err is nil and 400 if err is an untyped error (matching the
// default in errors.Response).
func statusOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var httpErr *commonerrors.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status()
	}
	return http.StatusBadRequest
}

// Stable private key — value doesn't matter, only that we can re-derive
// the address. Generated once with crypto.GenerateKey() and pinned to keep
// the test deterministic.
const testPriv = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"

// TestCtrl_CancelTask_StatusDisambiguation locks in the five-way status
// classification CancelTask does on top of db.CancelTask's RowsAffected==0
// ambiguity. The mapping is the SDK-visible contract described in the PR:
//
//	bad signature       -> 401
//	missing task        -> 404
//	wrong owner         -> 403
//	non-cancellable     -> 409
//	driver/preflight err is not exercised here (would need fault injection),
//	but the happy path returns nil.
func TestCtrl_CancelTask_StatusDisambiguation(t *testing.T) {
	c := setupCtrl(t)
	ctx := context.Background()

	// Derive the address from testPriv once; sign per-case since the
	// signature is over the task id and each case uses a fresh uuid.
	owner, _ := signTask(t, testPriv, uuid.Nil)

	t.Run("bad signature -> 401", func(t *testing.T) {
		id := seedTaskCtrl(t, c, owner, db.ProgressStateInit.String())
		task := &schema.Task{ID: id, UserAddress: owner, Signature: "0xdeadbeef"}
		err := c.CancelTask(ctx, task)
		if got := statusOf(t, err); got != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401, err = %v", got, err)
		}
	})

	t.Run("missing task -> 404", func(t *testing.T) {
		id := uuid.New()
		addr, sig := signTask(t, testPriv, id)
		task := &schema.Task{ID: &id, UserAddress: addr, Signature: sig}
		err := c.CancelTask(ctx, task)
		if got := statusOf(t, err); got != http.StatusNotFound {
			t.Fatalf("status = %d, want 404, err = %v", got, err)
		}
	})

	t.Run("wrong owner -> 403", func(t *testing.T) {
		// Seed under a different owner address; sign with our key but claim
		// our address — the preflight will read the row, see the owner
		// mismatch, and return 403.
		seededID := seedTaskCtrl(t, c, "0xSomeoneElse", db.ProgressStateInit.String())
		addr, sig := signTask(t, testPriv, *seededID)
		task := &schema.Task{ID: seededID, UserAddress: addr, Signature: sig}
		err := c.CancelTask(ctx, task)
		if got := statusOf(t, err); got != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, err = %v", got, err)
		}
	})

	t.Run("non-cancellable state -> 409 with current progress", func(t *testing.T) {
		// Need to derive the address from the key first, then seed under
		// THAT address, then sign for the resulting id.
		seededID := seedTaskCtrl(t, c, owner, db.ProgressStateTrained.String())
		_, sig := signTask(t, testPriv, *seededID)
		task := &schema.Task{ID: seededID, UserAddress: owner, Signature: sig}
		err := c.CancelTask(ctx, task)
		if got := statusOf(t, err); got != http.StatusConflict {
			t.Fatalf("status = %d, want 409, err = %v", got, err)
		}
		// 409 body must carry the actionable current state so the SDK can
		// surface it to the caller.
		if msg := err.Error(); !strings.Contains(msg, db.ProgressStateTrained.String()) {
			t.Fatalf("409 body %q missing current state %q", msg, db.ProgressStateTrained.String())
		}
	})

	t.Run("happy path -> nil, row flipped to Failed", func(t *testing.T) {
		seededID := seedTaskCtrl(t, c, owner, db.ProgressStateInit.String())
		_, sig := signTask(t, testPriv, *seededID)
		task := &schema.Task{ID: seededID, UserAddress: owner, Signature: sig}
		if err := c.CancelTask(ctx, task); err != nil {
			t.Fatalf("happy path: %v", err)
		}
		got, err := c.db.GetTask(seededID)
		if err != nil {
			t.Fatalf("re-read: %v", err)
		}
		if got.Progress != db.ProgressStateFailed.String() {
			t.Fatalf("after cancel, progress = %q, want %q", got.Progress, db.ProgressStateFailed.String())
		}
	})
}

// TestCtrl_GetLoRAModel_StatusDisambiguation locks in the six-way status
// classification GetLoRAModel returns. The 503-when-file-missing branch
// is the SDK-visible contract from the PR: clients polling for the LoRA
// artifact must see 503 (retryable) rather than 500 (terminal) when the
// finalizer hasn't yet written the file.
func TestCtrl_GetLoRAModel_StatusDisambiguation(t *testing.T) {
	c := setupCtrl(t)

	// Redirect the data dir to a per-test temp dir so we control whether
	// the encrypted file exists for the 503/200 cases.
	tmp := t.TempDir()
	utils.SetDataDir(tmp)

	const owner = "0xLoraOwner"

	t.Run("nil id -> 400", func(t *testing.T) {
		_, err := c.GetLoRAModel(nil, owner)
		if got := statusOf(t, err); got != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, err = %v", got, err)
		}
	})

	t.Run("missing task -> 404", func(t *testing.T) {
		id := uuid.New()
		_, err := c.GetLoRAModel(&id, owner)
		if got := statusOf(t, err); got != http.StatusNotFound {
			t.Fatalf("status = %d, want 404, err = %v", got, err)
		}
	})

	t.Run("wrong owner -> 403", func(t *testing.T) {
		id := seedTaskCtrl(t, c, "0xOther", db.ProgressStateDelivered.String())
		_, err := c.GetLoRAModel(id, owner)
		if got := statusOf(t, err); got != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, err = %v", got, err)
		}
	})

	t.Run("not delivered -> 409", func(t *testing.T) {
		id := seedTaskCtrl(t, c, owner, db.ProgressStateTraining.String())
		_, err := c.GetLoRAModel(id, owner)
		if got := statusOf(t, err); got != http.StatusConflict {
			t.Fatalf("status = %d, want 409, err = %v", got, err)
		}
	})

	t.Run("delivered but file missing -> 503 retry-friendly", func(t *testing.T) {
		id := seedTaskCtrl(t, c, owner, db.ProgressStateDelivered.String())
		_, err := c.GetLoRAModel(id, owner)
		if got := statusOf(t, err); got != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503, err = %v", got, err)
		}
		// Body must include "retry shortly" so the SDK knows this is
		// transient — that's the half of the PR contract that distinguishes
		// 503 from a generic 500.
		if msg := err.Error(); !strings.Contains(msg, "retry shortly") {
			t.Fatalf("503 body %q missing retry hint", msg)
		}
	})

	t.Run("delivered + file present -> path returned", func(t *testing.T) {
		id := seedTaskCtrl(t, c, owner, db.ProgressStateDelivered.String())
		// Materialize the encrypted artifact at the path GetLoRAModel
		// computes: <dataDir>/<id>/output_model_encrypted.data
		taskDir := filepath.Join(tmp, id.String())
		if err := os.MkdirAll(taskDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		expected := filepath.Join(taskDir, utils.OutputPath+"_encrypted.data")
		if err := os.WriteFile(expected, []byte("ciphertext"), 0o644); err != nil {
			t.Fatalf("write artifact: %v", err)
		}

		got, err := c.GetLoRAModel(id, owner)
		if err != nil {
			t.Fatalf("happy path: %v", err)
		}
		if got != expected {
			t.Fatalf("path = %q, want %q", got, expected)
		}
	})
}
