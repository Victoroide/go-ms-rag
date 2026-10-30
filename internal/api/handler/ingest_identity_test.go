package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type identityFakeStore struct {
	domain.ChunkStore
	documents       map[domain.DocumentID]domain.Document
	fingerprints    map[string]domain.DocumentID
	documentErr     error
	fingerprintErr  error
	fingerprintRead bool
}

func (s *identityFakeStore) GetDocument(_ context.Context, id domain.DocumentID) (*domain.Document, error) {
	if s.documentErr != nil {
		return nil, s.documentErr
	}
	doc, ok := s.documents[id]
	if !ok {
		return nil, nil
	}
	return &doc, nil
}

func (s *identityFakeStore) CheckFingerprint(_ context.Context, fp string) (domain.DocumentID, bool, error) {
	s.fingerprintRead = true
	id, ok := s.fingerprints[fp]
	return id, ok, s.fingerprintErr
}

func (*identityFakeStore) UpdateDocumentStatus(context.Context, domain.DocumentID, string) error {
	return nil
}

func identityRequest(t *testing.T, content, id string, upload bool) *http.Request {
	t.Helper()
	if !upload {
		body, err := json.Marshal(IngestRequest{DocumentID: id, Content: content})
		require.NoError(t, err)
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	var docx bytes.Buffer
	zw := zip.NewWriter(&docx)
	part, err := zw.Create("word/document.xml")
	require.NoError(t, err)
	_, err = part.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + content + `</w:t></w:r></w:p></w:body></w:document>`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	file, err := mw.CreateFormFile("file", "identity.docx")
	require.NoError(t, err)
	_, err = file.Write(docx.Bytes())
	require.NoError(t, err)
	require.NoError(t, mw.WriteField("document_id", id))
	require.NoError(t, mw.Close())
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func identityServe(t *testing.T, store *identityFakeStore, id, content string, upload bool) *httptest.ResponseRecorder {
	t.Helper()
	w := worker.New(nil, nil, nil, worker.Options{})
	t.Cleanup(w.Shutdown)
	rw := httptest.NewRecorder()
	IngestHandler(w, store, nil, 1<<20).ServeHTTP(rw, identityRequest(t, content, id, upload))
	return rw
}

func TestMalformedDocumentIDIs400(t *testing.T) {
	for _, upload := range []bool{false, true} {
		rw := identityServe(t, &identityFakeStore{}, "not-a-uuid", "Identity document", upload)
		require.Equal(t, http.StatusBadRequest, rw.Code, rw.Body.String())
		require.JSONEq(t, `{"error":"validation","message":"document_id must be a UUID"}`, rw.Body.String())
	}
}

func TestCallerIDWithSameContentIsIdempotent(t *testing.T) {
	for _, upload := range []bool{false, true} {
		id := uuid.NewString()
		store := &identityFakeStore{documents: map[domain.DocumentID]domain.Document{
			domain.DocumentID(id): {ID: domain.DocumentID(id), Content: "Identity document"},
		}}
		rw := identityServe(t, store, strings.ToUpper(id), "Identity document", upload)
		require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
		var response IngestResponse
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &response))
		require.Equal(t, IngestResponse{DocumentID: id, Status: "already_ingested"}, response)
		require.False(t, store.fingerprintRead)
	}
}

func TestCallerIDWithDifferentContentConflicts(t *testing.T) {
	for _, upload := range []bool{false, true} {
		id := uuid.NewString()
		store := &identityFakeStore{documents: map[domain.DocumentID]domain.Document{
			domain.DocumentID(id): {ID: domain.DocumentID(id), Content: "Original document"},
		}}
		rw := identityServe(t, store, id, "Different document", upload)
		require.Equal(t, http.StatusConflict, rw.Code, rw.Body.String())
		require.JSONEq(t, `{"error":"conflict","message":"document_id already exists with different content"}`, rw.Body.String())
		require.False(t, store.fingerprintRead)
	}
}

func TestCallerIDIsNeverReplacedByDedup(t *testing.T) {
	for _, upload := range []bool{false, true} {
		id := uuid.NewString()
		store := &identityFakeStore{fingerprints: map[string]domain.DocumentID{
			chunker.Fingerprint("Identity document"): domain.DocumentID(uuid.NewString()),
		}, fingerprintErr: errors.New("unavailable")}
		rw := identityServe(t, store, strings.ToUpper(id), "Identity document", upload)
		require.Equal(t, http.StatusAccepted, rw.Code, rw.Body.String())
		var response IngestResponse
		require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &response))
		require.Equal(t, IngestResponse{DocumentID: id, Status: "processing"}, response)
		require.False(t, store.fingerprintRead)
	}
}

func TestFingerprintLookupFailureReturns503(t *testing.T) {
	for _, upload := range []bool{false, true} {
		store := &identityFakeStore{fingerprintErr: errors.New("database details")}
		rw := identityServe(t, store, "", "Identity document", upload)
		require.Equal(t, http.StatusServiceUnavailable, rw.Code, rw.Body.String())
		require.JSONEq(t, `{"error":"unavailable","message":"document store unavailable"}`, rw.Body.String())
	}
}

func TestDocumentLookupFailureReturns503(t *testing.T) {
	for _, upload := range []bool{false, true} {
		store := &identityFakeStore{documentErr: errors.New("database details")}
		rw := identityServe(t, store, uuid.NewString(), "Identity document", upload)
		require.Equal(t, http.StatusServiceUnavailable, rw.Code, rw.Body.String())
		require.JSONEq(t, `{"error":"unavailable","message":"document store unavailable"}`, rw.Body.String())
		require.False(t, store.fingerprintRead)
	}
}

func TestMultipartDocumentIDIsHonored(t *testing.T) {
	id := uuid.NewString()
	rw := identityServe(t, &identityFakeStore{}, strings.ToUpper(id), "Uploaded identity", true)
	require.Equal(t, http.StatusAccepted, rw.Code, rw.Body.String())
	var response IngestResponse
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &response))
	require.Equal(t, id, response.DocumentID)
}

func (s *identityFakeStore) AcceptDocument(ctx context.Context, req domain.AcceptRequest) (domain.AcceptResult, error) {
	result := domain.AcceptResult{DocumentID: req.Document.ID, Outcome: domain.AcceptAccepted}
	if req.CallerSupplied {
		doc, err := s.GetDocument(ctx, req.Document.ID)
		if err != nil {
			return result, err
		}
		if doc != nil {
			result.Outcome = domain.AcceptDuplicate
			if chunker.Fingerprint(doc.Content) != req.Fingerprint {
				result.Outcome = domain.AcceptConflict
			}
		}
	} else {
		id, exists, err := s.CheckFingerprint(ctx, req.Fingerprint)
		if err != nil {
			return result, err
		}
		if exists {
			result.DocumentID = id
			result.Outcome = domain.AcceptDuplicate
		}
	}
	return result, nil
}

func TestIngestRequiresDurableAcceptor(t *testing.T) {
	store := struct{ domain.ChunkStore }{}
	rec := httptest.NewRecorder()
	IngestHandler(nil, store, nil, 1<<20)(rec, identityRequest(t, "valid content", "", false))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.JSONEq(t, `{"error":"unavailable","message":"document store unavailable"}`, rec.Body.String())
}
