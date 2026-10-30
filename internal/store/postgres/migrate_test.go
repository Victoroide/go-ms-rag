package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

//go:embed migrations/*.sql
var testMigrationFiles embed.FS

type migrationCounter struct {
	queries atomic.Int32
}

func (c *migrationCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "CREATE EXTENSION") || strings.HasPrefix(data.SQL, "ALTER TABLE documents") || strings.HasPrefix(data.SQL, "DO $$") {
		c.queries.Add(1)
	}
	return ctx
}

func (*migrationCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func migrationTestClient(t *testing.T, url string) *Client {
	t.Helper()
	client, err := NewClient(t.Context(), url)
	require.NoError(t, err)
	t.Cleanup(client.Pool.Close)
	return client
}

func requireRecordedMigrations(t *testing.T, client *Client) {
	t.Helper()
	files, err := testMigrationFiles.ReadDir("migrations")
	require.NoError(t, err)
	var count int
	require.NoError(t, client.Pool.QueryRow(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&count))
	require.Len(t, files, count)
	for _, file := range files {
		body, readErr := testMigrationFiles.ReadFile("migrations/" + file.Name())
		require.NoError(t, readErr)
		var name, checksum string
		var appliedAt time.Time
		require.NoError(t, client.Pool.QueryRow(t.Context(), `SELECT name, checksum, applied_at FROM schema_migrations WHERE version=$1`, file.Name()[:3]).Scan(&name, &checksum, &appliedAt))
		require.Equal(t, file.Name(), name)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), checksum)
		require.False(t, appliedAt.IsZero())
	}
}

func TestMigrationsAreAppliedOnce(t *testing.T) {
	url := startTestDatabase(t)
	counter := &migrationCounter{}
	config, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	client := &Client{Pool: pool}
	require.NoError(t, client.RunMigrations(t.Context()))
	first := counter.queries.Load()
	require.Positive(t, first)
	require.NoError(t, client.RunMigrations(t.Context()))
	require.Equal(t, first, counter.queries.Load(), "restart re-executed migration SQL")
	requireRecordedMigrations(t, client)
}

func TestConcurrentMigrationsSucceed(t *testing.T) {
	url := startTestDatabase(t)
	clients := make([]*Client, 6)
	for i := range clients {
		clients[i] = migrationTestClient(t, url)
	}
	start := make(chan struct{})
	errors := make(chan error, len(clients))
	var group sync.WaitGroup
	ctx := t.Context()
	for _, client := range clients {
		group.Go(func() {
			<-start
			errors <- client.RunMigrations(ctx)
		})
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	requireRecordedMigrations(t, clients[0])
}

func TestLegacySchemaIsAdopted(t *testing.T) {
	client := migrationTestClient(t, startTestDatabase(t))
	files, err := testMigrationFiles.ReadDir("migrations")
	require.NoError(t, err)
	for _, file := range files {
		if file.Name()[:3] > "005" {
			continue
		}
		body, readErr := testMigrationFiles.ReadFile("migrations/" + file.Name())
		require.NoError(t, readErr)
		_, err = client.Pool.Exec(t.Context(), string(body))
		require.NoError(t, err)
	}
	var id string
	require.NoError(t, client.Pool.QueryRow(t.Context(), `INSERT INTO documents(content, metadata) VALUES ('preserved text', '{"doc_type":"reference"}') RETURNING id::text`).Scan(&id))
	require.NoError(t, client.RunMigrations(t.Context()))
	var content, docType string
	require.NoError(t, client.Pool.QueryRow(t.Context(), `SELECT content, doc_type FROM documents WHERE id=$1`, id).Scan(&content, &docType))
	require.Equal(t, "preserved text", content)
	require.Equal(t, "document", docType)
	requireRecordedMigrations(t, client)
}

func TestSteadyStateStartupTakesNoTableLock(t *testing.T) {
	client := migrationTestClient(t, startTestDatabase(t))
	require.NoError(t, client.RunMigrations(t.Context()))
	tx, err := client.Pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(context.Background())) }()
	_, err = tx.Exec(t.Context(), `LOCK TABLE documents IN ACCESS SHARE MODE`)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.RunMigrations(ctx))
}

func TestModifiedMigrationIsRejected(t *testing.T) {
	client := migrationTestClient(t, startTestDatabase(t))
	require.NoError(t, client.RunMigrations(t.Context()))
	_, err := client.Pool.Exec(t.Context(), `UPDATE schema_migrations SET checksum='changed' WHERE version=1`)
	require.NoError(t, err)
	err = client.RunMigrations(t.Context())
	require.ErrorContains(t, err, "migration 001 was modified after it was applied")
}

func TestMigrationsWaitForAdvisoryLock(t *testing.T) {
	client := migrationTestClient(t, startTestDatabase(t))
	tx, err := client.Pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, int64(0x7175617272790001))
	require.NoError(t, err)
	url := client.Pool.Config().ConnString() + "&statement_timeout=20&lock_timeout=20"
	waiter := migrationTestClient(t, url)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- waiter.RunMigrations(ctx) }()
	select {
	case err = <-done:
		t.Fatalf("migration returned while advisory lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Rollback(t.Context()))
	require.NoError(t, <-done)
	conn, err := waiter.Pool.Acquire(t.Context())
	require.NoError(t, err)
	defer conn.Release()
	var statementTimeout, lockTimeout string
	require.NoError(t, conn.QueryRow(t.Context(), `SHOW statement_timeout`).Scan(&statementTimeout))
	require.NoError(t, conn.QueryRow(t.Context(), `SHOW lock_timeout`).Scan(&lockTimeout))
	require.Equal(t, "20ms", statementTimeout)
	require.Equal(t, "20ms", lockTimeout)
}

func TestFailedMigrationRollsBack(t *testing.T) {
	client := migrationTestClient(t, startTestDatabase(t))
	_, err := client.Pool.Exec(t.Context(), `CREATE TABLE documents (id UUID PRIMARY KEY)`)
	require.NoError(t, err)
	require.Error(t, client.RunMigrations(t.Context()))
	var tables int
	require.NoError(t, client.Pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('chunks', 'schema_migrations', 'document_fingerprints')`).Scan(&tables))
	require.Zero(t, tables, "failed migration left partially applied schema")
}

func TestPoolDefaultsApplyWhenAbsent(t *testing.T) {
	for _, url := range []string{
		"postgres://localhost/rag",
		"host=localhost dbname=rag",
		"postgres://user:pool_max_conns@localhost/rag",
	} {
		t.Run(url, func(t *testing.T) {
			config, err := poolConfig(url)
			require.NoError(t, err)
			require.EqualValues(t, 16, config.MaxConns)
			require.Equal(t, "30000", config.ConnConfig.RuntimeParams["statement_timeout"])
		})
	}
}

func TestURLSettingsOverrideDefaults(t *testing.T) {
	for _, url := range []string{
		"postgres://localhost/rag?pool_max_conns=7&statement_timeout=1200",
		"host=localhost dbname=rag pool_max_conns=7 statement_timeout=1200",
	} {
		t.Run(url, func(t *testing.T) {
			config, err := poolConfig(url)
			require.NoError(t, err)
			require.EqualValues(t, 7, config.MaxConns)
			require.Equal(t, "1200", config.ConnConfig.RuntimeParams["statement_timeout"])
		})
	}
	config, err := poolConfig("postgres://localhost/rag?statement_timeout=0")
	require.NoError(t, err)
	require.Equal(t, "0", config.ConnConfig.RuntimeParams["statement_timeout"])
	_, err = poolConfig("postgres://localhost/rag?pool_max_conns=invalid")
	require.Error(t, err)
}

func TestMigrationFilesAreDiscoveredInOrder(t *testing.T) {
	files := fstest.MapFS{
		"migrations/012_later.sql": {Data: []byte("SELECT 12;")},
		"migrations/001_first.sql": {Data: []byte("SELECT 1;")},
		"migrations/006_next.sql":  {Data: []byte("SELECT 6;")},
	}
	migrations, err := loadMigrations(files)
	require.NoError(t, err)
	require.Len(t, migrations, 3)
	require.Equal(t, []int{1, 6, 12}, []int{migrations[0].version, migrations[1].version, migrations[2].version})
	require.Equal(t, "006_next.sql", migrations[1].name)
	require.Equal(t, "SELECT 6;", migrations[1].sql)
	require.Equal(t, "17db4fd369edb9244b9f91d9aeed145c3d04ad8ba6e95d06247f07a63527d11a", migrations[0].checksum)
}

func TestInvalidMigrationFilesAreRejected(t *testing.T) {
	for _, names := range [][]string{
		{"001_first.sql", "001_duplicate.sql"},
		{"invalid.sql"},
		{"01_short.sql"},
		{"001_Upper.sql"},
	} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			files := fstest.MapFS{}
			for _, name := range names {
				files["migrations/"+name] = &fstest.MapFile{Data: []byte("SELECT 1;")}
			}
			_, err := loadMigrations(files)
			require.Error(t, err)
		})
	}
}
