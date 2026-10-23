package db

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// EnsureDatabase creates the database dsn names on its server when it does not
// exist yet. An extra provider instance gets a database of its own on the
// server the primary uses, and nothing else creates it: the compose's mysql
// init script only knows the primary's.
func EnsureDatabase(dsn string) error {
	c, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parsing database dsn: %w", err)
	}
	name := c.DBName
	if name == "" {
		return nil
	}
	c.DBName = ""
	conn, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Exec("CREATE DATABASE IF NOT EXISTS `" + strings.ReplaceAll(name, "`", "``") + "`"); err != nil {
		return fmt.Errorf("creating database %q: %w", name, err)
	}
	return nil
}
