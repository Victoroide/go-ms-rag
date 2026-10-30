package postgres

import (
	"context"
	"fmt"
)

// PendingIngestJobs counts queued jobs, including those waiting for retry.
func (c *Client) PendingIngestJobs(ctx context.Context) (int64, error) {
	var count int64
	if err := c.Pool.QueryRow(ctx, `SELECT count(*) FROM ingest_jobs WHERE state='pending'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count pending ingestion jobs: %w", err)
	}
	return count, nil
}
