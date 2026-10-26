//go:build integration

package instancetest

import (
	"context"
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PrimaryDatabase is the database the MySQL server starts with, which the
// tests name in DATABASE_DSN; an extra instance's database is derived from it.
const PrimaryDatabase = "test_db"

// StartMySQL starts a MySQL 8.0 server holding PrimaryDatabase and returns a
// root DSN for it, and a func that stops the server. It is for a TestMain,
// which has no testing.T to clean up with.
func StartMySQL(ctx context.Context) (dsn string, stop func(), err error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mysql:8.0",
			ExposedPorts: []string{"3306/tcp"},
			Env: map[string]string{
				"MYSQL_ROOT_PASSWORD": "testpass",
				"MYSQL_DATABASE":      PrimaryDatabase,
			},
			WaitingFor: wait.ForLog("port: 3306  MySQL Community Server").
				WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start mysql container: %w", err)
	}
	stop = func() { _ = container.Terminate(context.Background()) }
	host, err := container.Host(ctx)
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("mysql container host: %w", err)
	}
	port, err := container.MappedPort(ctx, "3306")
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("mysql container port: %w", err)
	}
	dsn = fmt.Sprintf("root:testpass@tcp(%s:%s)/%s?parseTime=true", host, port.Port(), PrimaryDatabase)
	return dsn, stop, nil
}
