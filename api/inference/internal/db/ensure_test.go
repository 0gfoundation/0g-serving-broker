//go:build integration

package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// An extra instance's database does not exist until EnsureDatabase creates it,
// and creating it again is a no-op: every broker restart calls it.
func TestEnsureDatabaseCreatesOnceAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	container, err := tcmysql.Run(ctx, "mysql:8.0",
		tcmysql.WithDatabase("provider"),
		tcmysql.WithUsername("root"),
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

	cfg := &config.Config{}
	cfg.Database.DSN = fmt.Sprintf("root:test@tcp(%s:%s)/provider_image_gen?parseTime=true", host, port.Port())

	// gorm pings on open, so this fails while the database is absent.
	if _, err := NewDB(cfg); err == nil {
		t.Fatal("provider_image_gen exists before EnsureDatabase; the test proves nothing")
	}

	for i := 0; i < 2; i++ {
		if err := EnsureDatabase(cfg.Database.DSN); err != nil {
			t.Fatalf("EnsureDatabase (call %d): %v", i+1, err)
		}
	}
	d, err := NewDB(cfg)
	if err != nil {
		t.Fatalf("NewDB after EnsureDatabase: %v", err)
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate into the created database: %v", err)
	}
}
