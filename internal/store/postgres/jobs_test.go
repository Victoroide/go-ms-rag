package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func jobsDatabase(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(t.Context(), startTestDatabase(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close(context.Background())) })
	require.NoError(t, c.RunMigrations(t.Context()))
	return c
}
func jobsRequest(content string) domain.AcceptRequest {
	return domain.AcceptRequest{Document: domain.Document{ID: domain.DocumentID(uuid.NewString()), Content: content}, Fingerprint: chunker.Fingerprint(content), MaxPending: 1000}
}
func jobsAccept(t *testing.T, c *Client, content string) domain.DocumentID {
	t.Helper()
	r, err := c.AcceptDocument(t.Context(), jobsRequest(content))
	require.NoError(t, err)
	require.Equal(t, domain.AcceptAccepted, r.Outcome)
	return r.DocumentID
}
func TestAcceptIsAtomicUnderConcurrency(t *testing.T) {
	c := jobsDatabase(t)
	var wg sync.WaitGroup
	ids := make(chan domain.DocumentID, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			r, err := c.AcceptDocument(t.Context(), jobsRequest("identical content"))
			ids <- r.DocumentID
			errs <- err
		})
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var id domain.DocumentID
	for got := range ids {
		if id == "" {
			id = got
		}
		require.Equal(t, id, got)
	}
	var docs, jobs int
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM documents),(SELECT count(*) FROM ingest_jobs)`).Scan(&docs, &jobs))
	require.Equal(t, 1, docs)
	require.Equal(t, 1, jobs)
}
func TestAcceptCallerIDConflict(t *testing.T) {
	c := jobsDatabase(t)
	req := jobsRequest("original")
	req.CallerSupplied = true
	a, err := c.AcceptDocument(t.Context(), req)
	require.NoError(t, err)
	req.Document.Content = "different"
	req.Fingerprint = chunker.Fingerprint("different")
	b, err := c.AcceptDocument(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, domain.AcceptConflict, b.Outcome)
	req = jobsRequest("original")
	req.CallerSupplied = true
	b, err = c.AcceptDocument(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, domain.AcceptAccepted, b.Outcome)
	require.NotEqual(t, a.DocumentID, b.DocumentID)
}
func TestAcceptRequeuesFailedDuplicate(t *testing.T) {
	c := jobsDatabase(t)
	id := jobsAccept(t, c, "retry content")
	job, err := c.ClaimJob(t.Context(), "first", time.Minute, 5)
	require.NoError(t, err)
	require.NoError(t, c.FailJob(t.Context(), *job, "failed"))
	r, err := c.AcceptDocument(t.Context(), jobsRequest("retry content"))
	require.NoError(t, err)
	require.Equal(t, domain.AcceptRequeued, r.Outcome)
	require.Equal(t, id, r.DocumentID)
	job, err = c.ClaimJob(t.Context(), "next", time.Minute, 5)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, 1, job.Attempts)
}
func TestAcceptQueueFull(t *testing.T) {
	c := jobsDatabase(t)
	jobsAccept(t, c, "one")
	req := jobsRequest("two")
	req.MaxPending = 1
	r, err := c.AcceptDocument(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, domain.AcceptQueueFull, r.Outcome)
	var count int
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT count(*) FROM documents`).Scan(&count))
	require.Equal(t, 1, count)
}
func TestClaimSkipsLockedJobs(t *testing.T) {
	c := jobsDatabase(t)
	first := jobsAccept(t, c, "first")
	second := jobsAccept(t, c, "second")
	tx, err := c.Pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), `SELECT 1 FROM ingest_jobs WHERE document_id=$1 FOR UPDATE`, string(first))
	require.NoError(t, err)
	job, err := c.ClaimJob(t.Context(), "owner", time.Minute, 5)
	require.NoError(t, err)
	require.Equal(t, second, job.Document.ID)
}
func TestExpiredLeaseIsReclaimed(t *testing.T) {
	c := jobsDatabase(t)
	jobsAccept(t, c, "expired")
	job, err := c.ClaimJob(t.Context(), "old", time.Minute, 5)
	require.NoError(t, err)
	_, err = c.Pool.Exec(t.Context(), `UPDATE ingest_jobs SET lease_until=NOW()-interval '1 second'`)
	require.NoError(t, err)
	next, err := c.ClaimJob(t.Context(), "new", time.Minute, 5)
	require.NoError(t, err)
	require.Equal(t, job.Document.ID, next.Document.ID)
	require.Equal(t, 2, next.Attempts)
	ok, err := c.HeartbeatJob(t.Context(), *job, time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	require.ErrorIs(t, c.FailJob(t.Context(), *job, "stale"), domain.ErrJobNotOwned)
	var count int
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT count(*) FROM ingest_jobs`).Scan(&count))
	require.Equal(t, 1, count)
}
func TestCompleteRequiresLeaseAndProcessingStatus(t *testing.T) {
	c := jobsDatabase(t)
	id := jobsAccept(t, c, "completion")
	job, err := c.ClaimJob(t.Context(), "owner", time.Minute, 5)
	require.NoError(t, err)
	chunks := []domain.Chunk{{ID: domain.ChunkID(uuid.NewString()), DocumentID: id, Text: "completion", TokenCount: 1}}
	stale := *job
	stale.LeaseOwner = "stale"
	ok, err := c.CompleteJob(t.Context(), stale, chunks)
	require.NoError(t, err)
	require.False(t, ok)
	var state string
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT status FROM documents WHERE id=$1`, string(id)).Scan(&state))
	require.Equal(t, "processing", state)
	_, err = c.Pool.Exec(t.Context(), `UPDATE documents SET status='failed' WHERE id=$1`, string(id))
	require.NoError(t, err)
	ok, err = c.CompleteJob(t.Context(), *job, chunks)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = c.Pool.Exec(t.Context(), `UPDATE documents SET status='processing' WHERE id=$1`, string(id))
	require.NoError(t, err)
	ok, err = c.CompleteJob(t.Context(), *job, chunks)
	require.NoError(t, err)
	require.True(t, ok)
	var count int
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT count(*) FROM chunks WHERE document_id=$1`, string(id)).Scan(&count))
	require.Equal(t, 1, count)
	duplicate, err := c.AcceptDocument(t.Context(), jobsRequest("completion"))
	require.NoError(t, err)
	require.Equal(t, domain.AcceptDuplicate, duplicate.Outcome)
}
func TestReleaseMakesJobClaimable(t *testing.T) {
	c := jobsDatabase(t)
	jobsAccept(t, c, "release")
	job, err := c.ClaimJob(t.Context(), "one", time.Minute, 5)
	require.NoError(t, err)
	require.NoError(t, c.ReleaseJob(t.Context(), *job))
	next, err := c.ClaimJob(t.Context(), "two", time.Minute, 5)
	require.NoError(t, err)
	require.Equal(t, 1, next.Attempts)
}
func TestClaimFailsJobsBeyondMaxAttempts(t *testing.T) {
	c := jobsDatabase(t)
	id := jobsAccept(t, c, "exhausted")
	_, err := c.Pool.Exec(t.Context(), `UPDATE ingest_jobs SET attempts=5`)
	require.NoError(t, err)
	job, err := c.ClaimJob(t.Context(), "one", time.Minute, 5)
	require.NoError(t, err)
	require.Nil(t, job)
	var state, message string
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT status,last_error FROM documents WHERE id=$1`, string(id)).Scan(&state, &message))
	require.Equal(t, "failed", state)
	require.Equal(t, "exceeded maximum attempts", message)
}
func TestLegacyProcessingRowsAreRequeued(t *testing.T) {
	c, err := NewClient(t.Context(), startTestDatabase(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close(context.Background())) })
	files, err := loadMigrations(migrationFiles)
	require.NoError(t, err)
	for _, f := range files {
		if f.version < 6 {
			_, err = c.Pool.Exec(t.Context(), f.sql)
			require.NoError(t, err)
		}
	}
	ids := []string{uuid.NewString(), uuid.NewString()}
	for i, id := range ids {
		_, err = c.Pool.Exec(t.Context(), `INSERT INTO documents(id,content) VALUES ($1,$2)`, id, fmt.Sprint("legacy ", i))
		require.NoError(t, err)
	}
	_, err = c.Pool.Exec(t.Context(), `INSERT INTO document_fingerprints(fingerprint,document_id) VALUES ('legacy',$1)`, ids[0])
	require.NoError(t, err)
	require.NoError(t, c.RunMigrations(t.Context()))
	var state string
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT status FROM documents WHERE id=$1`, ids[0]).Scan(&state))
	require.Equal(t, "completed", state)
	job, err := c.ClaimJob(t.Context(), "owner", time.Minute, 5)
	require.NoError(t, err)
	require.Equal(t, domain.DocumentID(ids[1]), job.Document.ID)
	ok, err := c.CompleteJob(t.Context(), *job, nil)
	require.NoError(t, err)
	require.True(t, ok)
	_, exists, err := c.CheckFingerprint(t.Context(), chunker.Fingerprint("legacy 1"))
	require.NoError(t, err)
	require.True(t, exists)
}

func TestConcurrentDuplicateAtQueueCapacity(t *testing.T) {
	c := jobsDatabase(t)
	lock, err := c.Pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = lock.Rollback(t.Context()) }()
	_, err = lock.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, int64(0x7175617272790002))
	require.NoError(t, err)
	results := make(chan domain.AcceptResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			req := jobsRequest("duplicate at capacity")
			req.MaxPending = 1
			r, acceptErr := c.AcceptDocument(t.Context(), req)
			results <- r
			errs <- acceptErr
		}()
	}
	require.Eventually(t, func() bool {
		var count int
		queryErr := c.Pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&count)
		return queryErr == nil && count >= 2
	}, time.Second, time.Millisecond)
	require.NoError(t, lock.Commit(t.Context()))
	first, second := <-results, <-results
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, first.DocumentID, second.DocumentID)
	require.ElementsMatch(t, []domain.AcceptOutcome{domain.AcceptAccepted, domain.AcceptInProgress}, []domain.AcceptOutcome{first.Outcome, second.Outcome})
}

func TestRetryRecordsErrorAndDefersClaim(t *testing.T) {
	c := jobsDatabase(t)
	id := jobsAccept(t, c, "retry later")
	j, err := c.ClaimJob(t.Context(), "owner", time.Minute, 5)
	require.NoError(t, err)
	require.NoError(t, c.RetryJob(t.Context(), *j, time.Minute, "temporary failure"))
	next, err := c.ClaimJob(t.Context(), "other", time.Minute, 5)
	require.NoError(t, err)
	require.Nil(t, next)
	var docError, jobError, state string
	var owner *string
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT d.last_error,j.last_error,j.state,j.lease_owner FROM documents d JOIN ingest_jobs j ON j.document_id=d.id WHERE d.id=$1`, string(id)).Scan(&docError, &jobError, &state, &owner))
	require.Equal(t, "temporary failure", docError)
	require.Equal(t, docError, jobError)
	require.Equal(t, "pending", state)
	require.Nil(t, owner)
}

func TestCompleteRollsBackInvalidChunks(t *testing.T) {
	c := jobsDatabase(t)
	id := jobsAccept(t, c, "atomic completion")
	j, err := c.ClaimJob(t.Context(), "owner", time.Minute, 5)
	require.NoError(t, err)
	ok, err := c.CompleteJob(t.Context(), *j, []domain.Chunk{{ID: "invalid", Text: "text"}})
	require.Error(t, err)
	require.False(t, ok)
	var state string
	var jobs int
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM ingest_jobs WHERE document_id=$1) FROM documents WHERE id=$1`, string(id)).Scan(&state, &jobs))
	require.Equal(t, "processing", state)
	require.Equal(t, 1, jobs)
}

func TestAcceptStoresDocumentFields(t *testing.T) {
	c := jobsDatabase(t)
	for _, label := range []string{"", "manual", "custom/type α"} {
		content := strings.Repeat("界", 150)
		req := jobsRequest(content)
		req.CallerSupplied = true
		req.DocType = label
		name, kind, key := "original.docx", "docx", "source-object"
		req.Document.FileName = &name
		req.Document.FileType = &kind
		req.Document.S3Key = &key
		req.Document.Metadata = map[string]any{"source": "archive"}
		r, err := c.AcceptDocument(t.Context(), req)
		require.NoError(t, err)
		require.Equal(t, domain.AcceptAccepted, r.Outcome)
		var stored, preview, docType, fileName, fileType, s3Key string
		var count int
		var metadata []byte
		require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT content,content_preview,metadata,doc_type,file_name,file_type,s3_key,chunk_count FROM documents WHERE id=$1`, string(r.DocumentID)).Scan(&stored, &preview, &metadata, &docType, &fileName, &fileType, &s3Key, &count))
		want := label
		if want == "" {
			want = "document"
		}
		require.Equal(t, content, stored)
		require.Equal(t, strings.Repeat("界", 120), preview)
		require.Equal(t, want, docType)
		require.Equal(t, name, fileName)
		require.Equal(t, kind, fileType)
		require.Equal(t, key, s3Key)
		require.Zero(t, count)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(metadata, &decoded))
		require.Equal(t, want, decoded["doc_type"])
		require.Equal(t, "archive", decoded["source"])
	}
}

func TestFailureTransitionRejectsLostLease(t *testing.T) {
	c := jobsDatabase(t)
	jobsAccept(t, c, "lost lease")
	j, err := c.ClaimJob(t.Context(), "current", time.Minute, 5)
	require.NoError(t, err)
	j.LeaseOwner = "stale"
	require.ErrorIs(t, c.FailJob(t.Context(), *j, "stale failure"), domain.ErrJobNotOwned)
}
