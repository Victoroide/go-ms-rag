// Package main reconciles vectors and legacy namespaces with stored documents.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	pgstore "github.com/Victoroide/quarry-rag/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
)

type options struct {
	Apply bool
	Limit int
	Page  int
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.BoolVar(&opts.Apply, "apply", false, "delete the selected orphaned vectors and namespaces")
	flags.IntVar(&opts.Limit, "limit", 100000, "maximum vector count selected across vectors and whole namespaces")
	flags.IntVar(&opts.Page, "page", 100, "vector listing page size (1-1000)")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 || opts.Limit < 1 || opts.Page < 1 || opts.Page > 1000 {
		return opts, errors.New("provide -limit greater than zero and -page between 1 and 1000, with no positional arguments")
	}
	return opts, nil
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		slog.Error("reconcile vectors", "error", err)
		os.Exit(1)
	}
}

func run() error {
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.FakeMode || cfg.PineconeAPIKey == "" || cfg.PineconeIndexName == "" {
		return errors.New("reconciliation requires a configured hosted vector index")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	pg, err := pgstore.NewClient(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := pg.Close(ctx); closeErr != nil {
			slog.Error("close document store", "error", closeErr)
		}
	}()
	pc, err := pinecone.NewClient(pinecone.NewClientParams{ApiKey: cfg.PineconeAPIKey})
	if err != nil {
		return fmt.Errorf("create index client: %w", err)
	}
	startupCtx, startupCancel := context.WithTimeout(ctx, cfg.PineconeTimeout)
	defer startupCancel()
	idx, err := pc.DescribeIndex(startupCtx, cfg.PineconeIndexName)
	if err != nil {
		return fmt.Errorf("describe index: %w", err)
	}
	conn, err := pc.Index(pinecone.NewIndexConnParams{Host: idx.Host})
	if err != nil {
		return fmt.Errorf("connect to index: %w", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			slog.Error("close index connection", "error", closeErr)
		}
	}()
	lookup := func(lookupCtx context.Context, ids []string) (map[string]documentState, error) {
		return lookupDocuments(lookupCtx, pg, ids)
	}
	deleteNamespace := func(deleteCtx context.Context, name string) error {
		return conn.WithNamespace(name).DeleteAllVectorsInNamespace(deleteCtx)
	}
	result, err := reconcile(ctx, conn, deleteNamespace, lookup, opts, os.Stderr)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func lookupDocuments(ctx context.Context, pg *pgstore.Client, ids []string) (map[string]documentState, error) {
	canonical := make([]string, 0, len(ids))
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			canonical = append(canonical, parsed.String())
		}
	}
	states := make(map[string]documentState)
	if len(canonical) == 0 {
		return states, nil
	}
	rows, err := pg.Pool.Query(ctx, `SELECT d.id::text, d.status,
        EXISTS (SELECT 1 FROM ingest_jobs j WHERE j.document_id = d.id)
        FROM documents d WHERE d.id = ANY($1::uuid[])`, canonical)
	if err != nil {
		return nil, fmt.Errorf("look up documents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var state documentState
		if err := rows.Scan(&id, &state.Status, &state.HasJob); err != nil {
			return nil, fmt.Errorf("read document state: %w", err)
		}
		states[id] = state
	}
	return states, rows.Err()
}
