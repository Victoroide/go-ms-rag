package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockID int64 = 0x7175617272790001

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func loadMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	pattern := regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)
	versions := make(map[int]bool, len(entries))
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		parts := pattern.FindStringSubmatch(name)
		if entry.IsDir() || parts == nil {
			return nil, fmt.Errorf("invalid migration name %q", name)
		}
		version, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("parse migration version %q: %w", name, err)
		}
		if versions[version] {
			return nil, fmt.Errorf("duplicate migration version %03d", version)
		}
		versions[version] = true
		body, err := fs.ReadFile(files, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		migrations = append(migrations, migration{
			version: version, name: name, sql: string(body), checksum: fmt.Sprintf("%x", sha256.Sum256(body)),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

// RunMigrations applies missing embedded migrations in one transaction under an advisory lock.
func (c *Client) RunMigrations(ctx context.Context) error {
	if err := c.runMigrations(ctx); err != nil {
		return fmt.Errorf("postgres: run migrations: %w", err)
	}
	slog.InfoContext(ctx, "postgres migrations applied")
	return nil
}

func (c *Client) runMigrations(ctx context.Context) error {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	conn, err := c.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = 0; SET LOCAL lock_timeout = 0`); err != nil {
		return fmt.Errorf("disable migration timeouts: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("create migration history: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	applied, err := func() (map[int]string, error) {
		defer rows.Close()
		history := make(map[int]string)
		for rows.Next() {
			var version int
			var checksum string
			if scanErr := rows.Scan(&version, &checksum); scanErr != nil {
				return nil, scanErr
			}
			history[version] = checksum
		}
		return history, rows.Err()
	}()
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	for _, migration := range migrations {
		if checksum, ok := applied[migration.version]; ok && checksum != migration.checksum {
			return fmt.Errorf("migration %03d was modified after it was applied", migration.version)
		}
	}
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		return fmt.Errorf("set migration lock timeout: %w", err)
	}
	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}
		if _, err := tx.Exec(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %03d: %w", migration.version, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`, migration.version, migration.name, migration.checksum); err != nil {
			return fmt.Errorf("record migration %03d: %w", migration.version, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
