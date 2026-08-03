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

func setupVideoPollDB(t *testing.T) *DB {
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
	if err := gdb.AutoMigrate(&model.VideoPollJob{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	return &DB{db: gdb}
}

// The behaviour a mock cannot see. The whole "no row is not an error" contract of
// GetVideoPollJobChatKey is a claim about what gorm does on a real driver, and the
// unit tests exercise a hand-written fake instead — so this is the only place the
// claim is actually tested.
func TestGetVideoPollJobChatKey_Integration(t *testing.T) {
	d := setupVideoPollDB(t)

	seedStatus := func(providerJobID, requestHash, chatKey string, status model.VideoPollStatus) {
		t.Helper()
		if err := d.db.Create(&model.VideoPollJob{
			Status:        status,
			ProviderJobID: providerJobID,
			RequestHash:   requestHash,
			PollURL:       "http://example.invalid/videos/" + providerJobID,
			OutputPrice:   "0",
			ChatKey:       chatKey,
			NextPollAt:    time.Now(),
			ExpiresAt:     time.Now().Add(time.Hour),
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", providerJobID, err)
		}
	}
	seed := func(providerJobID, requestHash, chatKey string) {
		t.Helper()
		seedStatus(providerJobID, requestHash, chatKey, model.VideoPollStatusCompleted)
	}

	seed("v0_signed", "hash-signed", "d4f1e2c3-aaaa-bbbb-cccc-000000000001")
	seed("v0_unsigned", "hash-unsigned", "")

	t.Run("returns the recorded handle", func(t *testing.T) {
		got, err := d.GetVideoPollJobChatKey("v0_signed")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "d4f1e2c3-aaaa-bbbb-cccc-000000000001" {
			t.Fatalf("got %q, want the seeded handle", got)
		}
	})

	// THE case the mock cannot reproduce. A synchronously-completed video job has
	// no poll row at all, so this is the ordinary path, not an edge case — if it
	// errors, every such poll logs a warning that looks like a DB fault.
	t.Run("no row is not an error", func(t *testing.T) {
		got, err := d.GetVideoPollJobChatKey("v0_no_such_job")
		if err != nil {
			t.Fatalf("a missing poll row must not be an error, got: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("a row that was never signed yields empty, not an error", func(t *testing.T) {
		got, err := d.GetVideoPollJobChatKey("v0_unsigned")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	// The authorization side of this pair is case-SENSITIVE by migration, so this
	// lookup must be too. If it folded case, a caller authorized for one id would be
	// handed the handle of a job differing only in case — and /signature/{key} takes
	// no session, so that is another user's proof.
	//
	// The column here is still ai_ci, deliberately: the case-significance comes from
	// the Go-side byte comparison in GetVideoPollJobChatKey, not from the schema. So
	// this is the test that pins that comparison — the SQL alone would match both
	// rows.
	t.Run("case-significant ids do not match each other", func(t *testing.T) {
		seed("v2_QUJD", "hash-upper", "alice-handle")
		seed("v2_qujd", "hash-lower", "bob-handle")
		for id, want := range map[string]string{"v2_QUJD": "alice-handle", "v2_qujd": "bob-handle"} {
			got, err := d.GetVideoPollJobChatKey(id)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", id, err)
			}
			if got != want {
				t.Fatalf("%s: got %q, want %q — the lookup folded case", id, got, want)
			}
		}
	})

	// Before the poller sees a terminal state the cached signature is the create-time
	// one, over the {"status":"queued"} envelope. Replaying the handle then would hand
	// the client a proof that cannot describe the body it just received — the same
	// objection that keeps the handle off /content. This is the guard the proxy-level
	// test cannot reach (that fixture never gets a poll row at all), so it is pinned
	// here, where the row's status is something a test can actually set.
	t.Run("a job that has not completed yields no handle", func(t *testing.T) {
		for _, st := range []model.VideoPollStatus{
			model.VideoPollStatusPending,
			model.VideoPollStatusPolling,
		} {
			id := "v0_" + string(st)
			seedStatus(id, "hash-"+string(st), "handle-not-yet-valid", st)
			got, err := d.GetVideoPollJobChatKey(id)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", st, err)
			}
			if got != "" {
				t.Errorf("status=%s returned %q; the signature still covers the queued envelope", st, got)
			}
		}
	})

	// An ambiguous id gets no answer rather than a guess. Ordering looked like it
	// resolved this ("the owner table's unique index means only the first creator can
	// poll, so take the lowest id"), but the owner insert and the poll insert are
	// separate non-transactional writes with a client-facing response write between
	// them — so the loser's row can hold the lower id and the guess can serve the
	// wrong creator's handle.
	t.Run("a duplicated job id yields no handle", func(t *testing.T) {
		seed("v0_ambiguous", "hash-amb-1", "first-handle")
		seed("v0_ambiguous", "hash-amb-2", "second-handle")
		got, err := d.GetVideoPollJobChatKey("v0_ambiguous")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q; an ambiguous id must not be guessed at", got)
		}
	})

	t.Run("empty id short-circuits without touching the DB", func(t *testing.T) {
		got, err := d.GetVideoPollJobChatKey("")
		if err != nil || got != "" {
			t.Fatalf("got %q/%v, want empty/nil", got, err)
		}
	})
}
