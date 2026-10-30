package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/cleanup"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/embedder"
	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	pgstore "github.com/Victoroide/quarry-rag/internal/store/postgres"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func durableDatabase(t *testing.T) *pgstore.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	c, err := pgstore.NewClient(t.Context(), startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close(context.Background())) })
	require.NoError(t, c.RunMigrations(t.Context()))
	return c
}
func durableWorker(c *pgstore.Client, v *memory.FakeVectorStore, e domain.EmbeddingProvider) *worker.Worker {
	return worker.New(c, []pipeline.Stage{pipeline.NewChunkStage(chunker.NewChunker(32, 4)), pipeline.NewEmbedStage(e, 2), pipeline.NewPersistStage(v)}, v, worker.Options{Workers: 2, MaxAttempts: 3, Lease: time.Second, JobTimeout: time.Minute, PollInterval: 10 * time.Millisecond, Owner: "integration"})
}
func durableSubmit(t *testing.T, w *worker.Worker, c domain.ChunkStore, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.IngestHandler(w, c, nil, 1<<20)(rec, req)
	return rec
}
func durableStatus(t *testing.T, c *pgstore.Client, id, state string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var got string
		err := c.Pool.QueryRow(t.Context(), `SELECT status FROM documents WHERE id=$1`, id).Scan(&got)
		return err == nil && got == state
	}, 5*time.Second, 10*time.Millisecond)
}
func TestAcceptedDocumentSurvivesWorkerRestart(t *testing.T) {
	c := durableDatabase(t)
	v := memory.NewFakeVectorStore()
	w := durableWorker(c, v, embedder.NewFakeEmbedder(256))
	rec := durableSubmit(t, w, c, `{"content":"The archive retains reports for seven years."}`)
	require.Equal(t, 202, rec.Code, rec.Body.String())
	var response handler.IngestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	w.Shutdown()
	next := durableWorker(c, v, embedder.NewFakeEmbedder(256))
	next.Start(t.Context())
	defer next.Shutdown()
	durableStatus(t, c, response.DocumentID, "completed")
}

type durableErrorEmbedder struct{}

func (durableErrorEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, domain.Permanent(errors.New("invalid content"))
}
func TestPermanentFailureIsRecorded(t *testing.T) {
	c := durableDatabase(t)
	w := durableWorker(c, memory.NewFakeVectorStore(), durableErrorEmbedder{})
	rec := durableSubmit(t, w, c, `{"content":"Text that cannot be embedded."}`)
	require.Equal(t, 202, rec.Code)
	var response handler.IngestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	w.Start(t.Context())
	defer w.Shutdown()
	durableStatus(t, c, response.DocumentID, "failed")
	var message string
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT last_error FROM documents WHERE id=$1`, response.DocumentID).Scan(&message))
	require.Contains(t, message, "invalid content")
}

type durableBlockingEmbedder struct{ entered, finish chan struct{} }

func (e durableBlockingEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	select {
	case e.entered <- struct{}{}:
	default:
	}
	select {
	case <-e.finish:
		return []float32{1}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestDeleteDuringProcessingDoesNotResurrect(t *testing.T) {
	c := durableDatabase(t)
	v := memory.NewFakeVectorStore()
	e := durableBlockingEmbedder{make(chan struct{}, 1), make(chan struct{})}
	w := durableWorker(c, v, e)
	rec := durableSubmit(t, w, c, `{"content":"Delete this document during processing."}`)
	require.Equal(t, 202, rec.Code)
	var response handler.IngestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	w.Start(t.Context())
	select {
	case <-e.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("processing did not start")
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/documents/"+response.DocumentID, http.NoBody)
	req.SetPathValue("doc_id", response.DocumentID)
	deleted := httptest.NewRecorder()
	handler.DeleteHandler(c, v, nil)(deleted, req)
	require.Equal(t, 200, deleted.Code, deleted.Body.String())
	close(e.finish)
	w.Shutdown()
	exists, err := c.DocumentExists(t.Context(), domain.DocumentID(response.DocumentID))
	require.NoError(t, err)
	require.False(t, exists)
	chunks, err := v.Query(t.Context(), "", []float32{1}, 10, "")
	require.NoError(t, err)
	require.Empty(t, chunks)
}
func TestContentNULIsRemovedBeforeStorage(t *testing.T) {
	c := durableDatabase(t)
	w := durableWorker(c, memory.NewFakeVectorStore(), embedder.NewFakeEmbedder(256))
	rec := durableSubmit(t, w, c, `{"content":"Stored\u0000 text"}`)
	require.Equal(t, 202, rec.Code, rec.Body.String())
	var response handler.IngestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	doc, err := c.GetDocument(t.Context(), domain.DocumentID(response.DocumentID))
	require.NoError(t, err)
	require.Equal(t, "Stored text", doc.Content)
	again := durableSubmit(t, w, c, `{"content":"Stored text"}`)
	require.Equal(t, 202, again.Code)
	require.Contains(t, again.Body.String(), response.DocumentID)
}
func TestMetadataNULIsRejected(t *testing.T) {
	c := durableDatabase(t)
	w := durableWorker(c, memory.NewFakeVectorStore(), embedder.NewFakeEmbedder(256))
	for _, metadata := range []string{`{"bad\u0000key":"x"}`, `{"nested":[{"value":"bad\u0000"}]}`} {
		rec := durableSubmit(t, w, c, `{"content":"valid","metadata":`+metadata+`}`)
		require.Equal(t, 400, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "metadata must not contain NUL characters")
	}
	rec := durableSubmit(t, w, c, `{"content":"valid","doc_type":"a\u0000b"}`)
	require.Equal(t, 400, rec.Code, rec.Body.String())
	multipartRec := httptest.NewRecorder()
	handler.IngestHandler(w, c, nil, 1<<20, handler.WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) { return "valid text", nil }))(multipartRec, durableFile(t, map[string]string{"doc_type": "a\x00b"}))
	require.Equal(t, 400, multipartRec.Code, multipartRec.Body.String())

}

type durableObjectStore struct {
	fail             bool
	afterUpload      func()
	uploads, deletes int
}

func (s *durableObjectStore) Upload(context.Context, string, string, string, extractor.FileType, io.Reader) (string, error) {
	s.uploads++
	if s.fail {
		return "", errors.New("storage down")
	}
	if s.afterUpload != nil {
		s.afterUpload()
	}
	return "uploaded-object", nil
}
func (s *durableObjectStore) Delete(ctx context.Context, _ string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.deletes++
	return nil
}
func durableFile(t *testing.T, fields ...map[string]string) *http.Request {
	t.Helper()
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	f, err := m.CreateFormFile("file", "sample.docx")
	require.NoError(t, err)
	_, err = f.Write(buildTestDOCX("Original document source content."))
	require.NoError(t, err)
	for _, group := range fields {
		for key, value := range group {
			require.NoError(t, m.WriteField(key, value))
		}
	}
	require.NoError(t, m.Close())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", &b)
	req.Header.Set("Content-Type", m.FormDataContentType())
	return req
}
func TestS3UploadFailureReturns503(t *testing.T) {
	c := durableDatabase(t)
	w := durableWorker(c, memory.NewFakeVectorStore(), embedder.NewFakeEmbedder(256))
	s := &durableObjectStore{fail: true}
	rec := httptest.NewRecorder()
	handler.IngestHandler(w, c, s, 1<<20, handler.WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) { return "extracted text", nil }))(rec, durableFile(t))
	require.Equal(t, 503, rec.Code, rec.Body.String())
	require.Equal(t, "30", rec.Header().Get("Retry-After"))
	require.JSONEq(t, `{"error":"storage","message":"source file storage is unavailable"}`, rec.Body.String())
	var count int
	require.NoError(t, c.Pool.QueryRow(t.Context(), `SELECT count(*) FROM documents`).Scan(&count))
	require.Zero(t, count)
}

type durablePartialWriteStage struct {
	vectors         *memory.FakeVectorStore
	entered, finish chan struct{}
}

func (durablePartialWriteStage) ID() pipeline.StageID { return pipeline.StagePersist }
func (s durablePartialWriteStage) Run(ctx context.Context, ic *pipeline.IngestContext) error {
	close(s.entered)
	select {
	case <-s.finish:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := s.vectors.Upsert(ctx, "", []domain.Chunk{{ID: "late", DocumentID: ic.DocumentID, Text: "late partial write", Embedding: []float32{1}}}); err != nil {
		return err
	}
	return errors.New("transient partial write")
}
func TestDeleteDuringPartialWriteFailureRemovesVectors(t *testing.T) {
	c := durableDatabase(t)
	v := memory.NewFakeVectorStore()
	stage := durablePartialWriteStage{v, make(chan struct{}), make(chan struct{})}
	w := worker.New(c, []pipeline.Stage{stage}, v, worker.Options{Workers: 1, Owner: "partial", Lease: time.Minute, PollInterval: time.Millisecond})
	rec := durableSubmit(t, w, c, `{"content":"Remove partial output even when persistence fails."}`)
	require.Equal(t, 202, rec.Code)
	var response handler.IngestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	w.Start(t.Context())
	<-stage.entered
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/documents/"+response.DocumentID, http.NoBody)
	req.SetPathValue("doc_id", response.DocumentID)
	deleted := httptest.NewRecorder()
	handler.DeleteHandler(c, v, nil)(deleted, req)
	require.Equal(t, 200, deleted.Code)
	close(stage.finish)
	w.Shutdown()
	chunks, err := v.Query(t.Context(), "", []float32{1}, 10, "")
	require.NoError(t, err)
	require.Empty(t, chunks)
}

func TestUnacceptedUploadIsRemoved(t *testing.T) {
	c := durableDatabase(t)
	w := durableWorker(c, memory.NewFakeVectorStore(), embedder.NewFakeEmbedder(256))
	require.Equal(t, 202, durableSubmit(t, w, c, `{"content":"Fill the queue with another document."}`).Code)
	objects := &durableObjectStore{}
	rec := httptest.NewRecorder()
	handler.IngestHandler(w, c, objects, 1<<20, handler.WithMaxPending(1), handler.WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) { return "new extracted text", nil }))(rec, durableFile(t))
	require.Equal(t, 503, rec.Code)
	require.Equal(t, "30", rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), `"error":"queue"`)
	require.Equal(t, 1, objects.uploads)
	require.Equal(t, 1, objects.deletes)
}

func TestCallerUploadRetainsSourceForIdenticalContent(t *testing.T) {
	c := durableDatabase(t)
	v := memory.NewFakeVectorStore()
	w := durableWorker(c, v, embedder.NewFakeEmbedder(256))
	require.Equal(t, 202, durableSubmit(t, w, c, `{"content":"identical extracted content"}`).Code)
	id := uuid.NewString()
	objects := &durableObjectStore{}
	rec := httptest.NewRecorder()
	handler.IngestHandler(w, c, objects, 1<<20, handler.WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) {
		return "identical extracted content", nil
	}))(rec, durableFile(t, map[string]string{"document_id": id}))
	require.Equal(t, 202, rec.Code, rec.Body.String())
	require.Equal(t, 1, objects.uploads)
	key, err := c.GetDocumentS3Key(t.Context(), domain.DocumentID(id))
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, "uploaded-object", *key)
	require.Zero(t, objects.deletes)
}

type durableCompletionStore struct {
	*pgstore.Client
	completed chan struct{}
}

func (s durableCompletionStore) CompleteJob(ctx context.Context, j domain.Job, chunks []domain.Chunk) (bool, error) {
	ok, err := s.Client.CompleteJob(ctx, j, chunks)
	if ok && err == nil {
		close(s.completed)
	}
	return ok, err
}

type durableCompletionObject struct {
	finish    chan struct{}
	completed <-chan struct{}
}

func (o durableCompletionObject) Delete(ctx context.Context, _ string) error {
	close(o.finish)
	select {
	case <-o.completed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestDeleteWaitsForCompletionBeforeRemovingRow(t *testing.T) {
	c := durableDatabase(t)
	v := memory.NewFakeVectorStore()
	e := durableBlockingEmbedder{make(chan struct{}, 1), make(chan struct{})}
	store := durableCompletionStore{c, make(chan struct{})}
	w := worker.New(store, []pipeline.Stage{pipeline.NewChunkStage(chunker.NewChunker(32, 4)), pipeline.NewEmbedStage(e, 1), pipeline.NewPersistStage(v)}, v, worker.Options{Workers: 1, Owner: "completion", Lease: time.Minute, JobTimeout: 10 * time.Second, PollInterval: time.Millisecond})
	rec := durableSubmit(t, w, c, `{"content":"Remove all vectors when deletion overlaps completion."}`)
	require.Equal(t, 202, rec.Code)
	var response handler.IngestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	_, err := c.Pool.Exec(t.Context(), `UPDATE documents SET s3_key='source-object' WHERE id=$1`, response.DocumentID)
	require.NoError(t, err)
	doc, err := c.GetDocument(t.Context(), domain.DocumentID(response.DocumentID))
	require.NoError(t, err)
	w.Start(t.Context())
	defer w.Shutdown()
	select {
	case <-e.entered:
	case <-time.After(time.Second):
		t.Fatal("processing did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, cleanup.Remove(ctx, c, v, durableCompletionObject{e.finish, store.completed}, doc))
	w.Shutdown()
	exists, err := c.DocumentExists(t.Context(), doc.ID)
	require.NoError(t, err)
	require.False(t, exists)
	chunks, err := v.Query(t.Context(), "", []float32{1}, 10, "")
	require.NoError(t, err)
	require.Empty(t, chunks)
}

type durableAcceptanceStore struct {
	domain.ChunkStore
	key        *string
	lookupErr  error
	acceptErr  error
	commit     bool
	contextErr error
	deadline   time.Duration
}

func (*durableAcceptanceStore) CheckFingerprint(context.Context, string) (domain.DocumentID, bool, error) {
	return "", false, nil
}
func (s *durableAcceptanceStore) AcceptDocument(ctx context.Context, r domain.AcceptRequest) (domain.AcceptResult, error) {
	s.contextErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		s.deadline = time.Until(deadline)
	}
	if s.commit {
		s.key = r.Document.S3Key
	}
	if s.contextErr != nil {
		return domain.AcceptResult{}, s.contextErr
	}
	return domain.AcceptResult{DocumentID: r.Document.ID, Outcome: domain.AcceptAccepted}, s.acceptErr
}
func (s *durableAcceptanceStore) GetDocumentS3Key(ctx context.Context, _ domain.DocumentID) (*string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return s.key, s.lookupErr
}
func TestAcceptanceRetainsPossiblyCommittedSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		commit      bool
		lookupErr   error
		wantDeletes int
	}{{"committed", true, nil, 0}, {"unknown", false, errors.New("lookup unavailable"), 0}, {"absent", false, nil, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &durableAcceptanceStore{commit: tc.commit, lookupErr: tc.lookupErr, acceptErr: errors.New("commit outcome unknown")}
			objects := &durableObjectStore{}
			rec := httptest.NewRecorder()
			handler.IngestHandler(nil, s, objects, 1<<20, handler.WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) { return "content", nil }))(rec, durableFile(t))
			require.Equal(t, 503, rec.Code)
			require.Equal(t, tc.wantDeletes, objects.deletes)
		})
	}
}
func TestAcceptanceSurvivesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &durableAcceptanceStore{commit: true}
	objects := &durableObjectStore{afterUpload: cancel}
	req := durableFile(t).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.IngestHandler(nil, s, objects, 1<<20, handler.WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) { return "content", nil }))(rec, req)
	require.Equal(t, 202, rec.Code, rec.Body.String())
	require.NoError(t, s.contextErr)
	require.Greater(t, s.deadline, time.Duration(0))
	require.LessOrEqual(t, s.deadline, 10*time.Second)
	require.Zero(t, objects.deletes)
}
