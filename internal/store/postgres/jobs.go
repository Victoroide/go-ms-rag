package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// AcceptDocument atomically reserves document identity and queues ingestion.
func (c *Client) AcceptDocument(ctx context.Context, req domain.AcceptRequest) (domain.AcceptResult, error) {
	for attempt := 0; attempt < 2; attempt++ {
		result, err := c.acceptDocument(ctx, req)
		var pgErr *pgconn.PgError
		if attempt == 0 && errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("postgres: accept document: %w", err)
		}
		return result, nil
	}
	return domain.AcceptResult{}, errors.New("postgres: acceptance retry exhausted")
}

func (c *Client) acceptDocument(ctx context.Context, req domain.AcceptRequest) (domain.AcceptResult, error) {
	result := domain.AcceptResult{DocumentID: req.Document.ID}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Serialize admission so concurrent requests cannot overfill the durable queue.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x7175617272790002)); err != nil {
		return result, err
	}
	var id, state string
	if req.CallerSupplied {
		var fingerprint *string
		var content string
		err = tx.QueryRow(ctx, `SELECT status,fingerprint,content FROM documents WHERE id=$1 FOR UPDATE`, string(req.Document.ID)).Scan(&state, &fingerprint, &content)
		if err == nil {
			stored := chunker.Fingerprint(content)
			if fingerprint != nil {
				stored = *fingerprint
			}
			if stored != req.Fingerprint {
				result.Outcome = domain.AcceptConflict
				return result, nil
			}
			id = string(req.Document.ID)
		}
	} else {
		err = tx.QueryRow(ctx, `SELECT d.id::text,d.status FROM document_fingerprints f JOIN documents d ON d.id=f.document_id WHERE f.fingerprint=$1 FOR UPDATE OF d`, req.Fingerprint).Scan(&id, &state)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if id != "" {
		result.DocumentID = domain.DocumentID(id)
		switch state {
		case "completed":
			result.Outcome = domain.AcceptDuplicate
			return result, nil
		case "processing":
			result.Outcome = domain.AcceptInProgress
			return result, nil
		case "failed":
			_, err = tx.Exec(ctx, `UPDATE documents SET status='processing',last_error=NULL,updated_at=NOW() WHERE id=$1`, id)
			if err != nil {
				return result, err
			}
			_, err = tx.Exec(ctx, `INSERT INTO ingest_jobs(document_id) VALUES($1) ON CONFLICT DO NOTHING`, id)
			if err != nil {
				return result, err
			}
			result.Outcome = domain.AcceptRequeued
			return result, tx.Commit(ctx)
		default:
			return result, fmt.Errorf("unexpected document status %q", state)
		}
	}

	var count int
	if countErr := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM ingest_jobs LIMIT $1) q`, req.MaxPending).Scan(&count); countErr != nil {
		return result, countErr
	}
	if count >= req.MaxPending {
		result.Outcome = domain.AcceptQueueFull
		return result, nil
	}
	metadata := req.Document.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["doc_type"] = req.DocType
	if req.DocType == "" {
		metadata["doc_type"] = domain.DefaultDocType
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return result, err
	}
	preview := []rune(req.Document.Content)
	if len(preview) > 120 {
		preview = preview[:120]
	}
	_, err = tx.Exec(ctx, `INSERT INTO documents(id,content,content_preview,metadata,chunk_count,file_name,file_type,s3_key,doc_type,status,fingerprint) VALUES($1,$2,$3,$4,0,$5,$6,$7,$8,'processing',$9)`, string(req.Document.ID), req.Document.Content, string(preview), encoded, req.Document.FileName, req.Document.FileType, req.Document.S3Key, metadata["doc_type"], req.Fingerprint)
	if err != nil {
		return result, err
	}
	query := `INSERT INTO document_fingerprints(fingerprint,document_id) VALUES($1,$2)`
	if req.CallerSupplied {
		query += ` ON CONFLICT(fingerprint) DO NOTHING`
	}
	if _, err = tx.Exec(ctx, query, req.Fingerprint, string(req.Document.ID)); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ingest_jobs(document_id) VALUES($1)`, string(req.Document.ID)); err != nil {
		return result, err
	}
	result.Outcome = domain.AcceptAccepted
	return result, tx.Commit(ctx)
}

// ClaimJob leases ready work, reclaiming expired leases and recording exhaustion.
func (c *Client) ClaimJob(ctx context.Context, owner string, lease time.Duration, maxAttempts int) (*domain.Job, error) {
	for range 10 {
		j := &domain.Job{LeaseOwner: owner}
		var id string
		err := c.Pool.QueryRow(ctx, `UPDATE ingest_jobs j SET state='running',attempts=j.attempts+1,lease_owner=$1,lease_until=NOW()+make_interval(secs=>$2),updated_at=NOW()
   WHERE j.document_id=(SELECT document_id FROM ingest_jobs WHERE (state='pending' AND next_attempt_at<=NOW()) OR (state='running' AND lease_until<NOW()) ORDER BY next_attempt_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING j.document_id::text,j.attempts`, owner, lease.Seconds()).Scan(&id, &j.Attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("postgres: claim job: %w", err)
		}
		j.Document.ID = domain.DocumentID(id)
		if j.Attempts > maxAttempts {
			if failErr := c.FailJob(ctx, *j, "exceeded maximum attempts"); failErr != nil && !errors.Is(failErr, domain.ErrJobNotOwned) {
				return nil, failErr
			}
			continue
		}
		var metadata []byte
		var fingerprint *string
		err = c.Pool.QueryRow(ctx, `SELECT content,metadata,file_name,file_type,s3_key,doc_type,fingerprint FROM documents WHERE id=$1`, id).Scan(&j.Document.Content, &metadata, &j.Document.FileName, &j.Document.FileType, &j.Document.S3Key, &j.DocType, &fingerprint)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("postgres: load job document: %w", err)
		}
		if fingerprint != nil {
			j.Document.Fingerprint = *fingerprint
		}
		if err = json.Unmarshal(metadata, &j.Document.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: decode job metadata: %w", err)
		}
		return j, nil
	}
	return nil, nil
}

// HeartbeatJob renews a live lease without reviving an expired attempt.
func (c *Client) HeartbeatJob(ctx context.Context, j domain.Job, lease time.Duration) (bool, error) {
	tag, err := c.Pool.Exec(ctx, `UPDATE ingest_jobs SET lease_until=NOW()+make_interval(secs=>$3),updated_at=NOW() WHERE document_id=$1 AND lease_owner=$2 AND state='running' AND attempts=$4 AND lease_until>NOW()`, string(j.Document.ID), j.LeaseOwner, lease.Seconds(), j.Attempts)
	return tag.RowsAffected() == 1, err
}

// CompleteJob commits chunks and completion only while the document and lease remain valid.
func (c *Client) CompleteJob(ctx context.Context, j domain.Job, chunks []domain.Chunk) (bool, error) {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, `UPDATE documents SET status='completed',chunk_count=$2,completed_at=NOW(),updated_at=NOW(),last_error=NULL WHERE id=$1 AND status='processing'`, string(j.Document.ID), len(chunks))
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	tag, err = tx.Exec(ctx, `DELETE FROM ingest_jobs WHERE document_id=$1 AND lease_owner=$2 AND attempts=$3 AND state='running' AND lease_until>NOW()`, string(j.Document.ID), j.LeaseOwner, j.Attempts)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `DELETE FROM chunks WHERE document_id=$1`, string(j.Document.ID)); err != nil {
		return false, err
	}
	batch := &pgx.Batch{}
	for _, ch := range chunks {
		batch.Queue(`INSERT INTO chunks(id,document_id,chunk_index,text,token_count) VALUES($1,$2,$3,$4,$5)`, string(ch.ID), string(j.Document.ID), ch.Index, ch.Text, ch.TokenCount)
	}
	if batchErr := tx.SendBatch(ctx, batch).Close(); batchErr != nil {
		return false, batchErr
	}
	fingerprint := j.Document.Fingerprint
	if fingerprint == "" {
		fingerprint = chunker.Fingerprint(j.Document.Content)
	}
	if _, err = tx.Exec(ctx, `UPDATE documents SET fingerprint=$2 WHERE id=$1 AND fingerprint IS NULL`, string(j.Document.ID), fingerprint); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO document_fingerprints(fingerprint,document_id) VALUES($1,$2) ON CONFLICT(fingerprint) DO NOTHING`, fingerprint, string(j.Document.ID)); err != nil {
		return false, err
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return false, commitErr
	}
	return true, nil
}

func jobError(message string) string {
	r := []rune(message)
	if len(r) > 1000 {
		r = r[:1000]
	}
	return string(r)
}

func (c *Client) changeJob(ctx context.Context, j domain.Job, query string, args []any, message *string) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM documents WHERE id=$1 FOR UPDATE`, string(j.Document.ID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrJobNotOwned
	}
	if err != nil {
		return err
	}
	params := []any{id, j.LeaseOwner, j.Attempts}
	params = append(params, args...)
	tag, err := tx.Exec(ctx, query, params...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrJobNotOwned
	}
	if message != nil {
		state := "processing"
		if args == nil {
			state = "failed"
		}
		_, err = tx.Exec(ctx, `UPDATE documents SET status=$2,last_error=$3,updated_at=NOW() WHERE id=$1`, id, state, jobError(*message))
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RetryJob reschedules an owned attempt and records its latest failure.
func (c *Client) RetryJob(ctx context.Context, j domain.Job, delay time.Duration, message string) error {
	return c.changeJob(ctx, j, `UPDATE ingest_jobs SET state='pending',lease_owner=NULL,lease_until=NULL,next_attempt_at=NOW()+make_interval(secs=>$4),last_error=$5,updated_at=NOW() WHERE document_id=$1 AND lease_owner=$2 AND attempts=$3 AND state='running' AND lease_until>NOW()`, []any{delay.Seconds(), jobError(message)}, &message)
}

// FailJob records a terminal failure and removes the owned work.
func (c *Client) FailJob(ctx context.Context, j domain.Job, message string) error {
	return c.changeJob(ctx, j, `DELETE FROM ingest_jobs WHERE document_id=$1 AND lease_owner=$2 AND attempts=$3 AND state='running' AND lease_until>NOW()`, nil, &message)
}

// ReleaseJob returns interrupted work without consuming an attempt.
func (c *Client) ReleaseJob(ctx context.Context, j domain.Job) error {
	return c.changeJob(ctx, j, `UPDATE ingest_jobs SET state='pending',lease_owner=NULL,lease_until=NULL,next_attempt_at=NOW(),attempts=GREATEST(attempts-1,0),updated_at=NOW() WHERE document_id=$1 AND lease_owner=$2 AND attempts=$3 AND state='running'`, nil, nil)
}

// DocumentExists reports whether deletion has removed a document.
func (c *Client) DocumentExists(ctx context.Context, id domain.DocumentID) (bool, error) {
	var exists bool
	err := c.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1)`, string(id)).Scan(&exists)
	return exists, err
}
