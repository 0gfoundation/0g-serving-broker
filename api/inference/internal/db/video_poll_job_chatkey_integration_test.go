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

	seed := func(providerJobID, requestHash, chatKey string) {
		t.Helper()
		if err := d.db.Create(&model.VideoPollJob{
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

	// provider_job_id is only a plain index, so a duplicate is insertable. The
	// caller who can actually poll is the FIRST creator (VideoJobOwner's unique
	// index rejects the second), so the first row's handle is the right answer.
	//
	// Coverage limit, stated so nobody reads more into this than it holds: dropping
	// the ORDER BY does NOT turn this red. InnoDB happens to return this scan in
	// primary-key order anyway, and there is no reliable way to make it not. So the
	// ORDER BY is a determinism guarantee this test cannot falsify — it pins the
	// ANSWER, not the clause that forces it.
	t.Run("a duplicated job id resolves to the first row, deterministically", func(t *testing.T) {
		seed("v0_dup", "hash-dup-1", "first-creators-handle")
		seed("v0_dup", "hash-dup-2", "second-creators-handle")
		for i := 0; i < 5; i++ {
			got, err := d.GetVideoPollJobChatKey("v0_dup")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != "first-creators-handle" {
				t.Fatalf("attempt %d: got %q, want the first row's handle", i, got)
			}
		}
	})

	t.Run("empty id short-circuits without touching the DB", func(t *testing.T) {
		got, err := d.GetVideoPollJobChatKey("")
		if err != nil || got != "" {
			t.Fatalf("got %q/%v, want empty/nil", got, err)
		}
	})
}
