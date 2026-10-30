package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api"
	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/stretchr/testify/require"
)

const summaryFirstID = "00000000-0000-4000-8000-000000000001"
const summarySecondID = "00000000-0000-4000-8000-000000000002"

type summaryLegacyStore struct{ domain.ChunkStore }

func (summaryLegacyStore) ListDocuments(context.Context) ([]domain.DocumentRef, error) {
	return nil, nil
}

type summaryStore struct {
	summaryLegacyStore
	documents []domain.DocumentSummary
	err       error
	beforeRow func(int)
}

func (s summaryStore) StreamDocuments(ctx context.Context, visit func(domain.DocumentSummary) error) error {
	for i, doc := range s.documents {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.beforeRow != nil {
			s.beforeRow(i)
		}
		if err := visit(doc); err != nil {
			return err
		}
	}
	return s.err
}

func (s summaryStore) ListDocumentsPage(_ context.Context, after string, limit int) ([]domain.DocumentSummary, error) {
	var page []domain.DocumentSummary
	for _, doc := range s.documents {
		if doc.ID > after && len(page) < limit {
			page = append(page, doc)
		}
	}
	return page, s.err
}

func (s summaryStore) GetDocumentSummary(_ context.Context, id domain.DocumentID) (*domain.DocumentSummary, error) {
	for _, doc := range s.documents {
		if doc.ID == string(id) {
			return &doc, s.err
		}
	}
	return nil, s.err
}

func summaryRequest(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
	req.Header.Set("X-Service-Token", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestEmptyListingIsArray(t *testing.T) {
	for name, store := range map[string]domain.ChunkStore{"legacy": summaryLegacyStore{}, "summary": summaryStore{}} {
		t.Run(name, func(t *testing.T) {
			w := summaryRequest(t, handler.ListDocumentsHandler(store), "/documents", "")
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), `"documents":[]`)
		})
	}
}

func TestDocumentStatusEndpoint(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	name, kind, key, lastError := "manual.pdf", "pdf", "documents/manual/source.pdf", "extraction failed"
	for _, status := range []string{"processing", "completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			doc := domain.DocumentSummary{ID: summaryFirstID, DocType: "manual", S3Key: &key, Status: status, ChunkCount: 4, FileName: &name, FileType: &kind, CreatedAt: now, UpdatedAt: now, LastError: &lastError}
			if status == "completed" {
				doc.CompletedAt = &now
			}
			router := api.NewRouter(&api.RouterDeps{Config: &config.Config{ServiceToken: "secret"}, ChunkStore: summaryStore{documents: []domain.DocumentSummary{doc}}})
			w := summaryRequest(t, router, "/documents/"+summaryFirstID, "secret")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, summaryFirstID, body["document_id"])
			require.Equal(t, status, body["status"])
			require.Equal(t, "manual", body["doc_type"])
			require.Equal(t, key, body["s3_key"])
			require.Equal(t, float64(4), body["chunk_count"])
			require.Equal(t, name, body["file_name"])
			require.Equal(t, kind, body["file_type"])
			require.Equal(t, "2026-01-02T03:04:05Z", body["created_at"])
			require.Equal(t, body["created_at"], body["updated_at"])
			if status == "completed" {
				require.Equal(t, body["created_at"], body["completed_at"])
			} else {
				require.Nil(t, body["completed_at"])
			}
			if status == "failed" {
				require.Equal(t, lastError, body["last_error"])
			} else {
				require.NotContains(t, body, "last_error")
			}
			require.NotContains(t, body, "content")
		})
	}
}

func TestDocumentStatusNotFound(t *testing.T) {
	router := api.NewRouter(&api.RouterDeps{Config: &config.Config{ServiceToken: "secret"}, ChunkStore: summaryStore{}})
	for _, tc := range []struct {
		path, token string
		want        int
	}{
		{"/documents/" + summaryFirstID, "secret", http.StatusNotFound},
		{"/documents/not-a-uuid", "secret", http.StatusBadRequest},
		{"/documents/" + summaryFirstID, "", http.StatusUnauthorized},
		{"/documents/" + summaryFirstID, "wrong", http.StatusUnauthorized},
	} {
		w := summaryRequest(t, router, tc.path, tc.token)
		require.Equal(t, tc.want, w.Code, w.Body.String())
	}
}

func TestListingPagination(t *testing.T) {
	store := summaryStore{documents: []domain.DocumentSummary{{ID: summaryFirstID, Status: "processing"}, {ID: summarySecondID, Status: "completed"}}}
	for _, tc := range []struct {
		query, id string
		count     int
		next      any
	}{
		{"?limit=1", summaryFirstID, 1, summaryFirstID},
		{"?limit=1&after=" + summaryFirstID, summarySecondID, 1, summarySecondID},
		{"?limit=1&after=" + summarySecondID, "", 0, nil},
		{"?limit=1000", summaryFirstID, 2, nil},
	} {
		w := summaryRequest(t, handler.ListDocumentsHandler(store), "/documents"+tc.query, "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body struct {
			Documents []domain.DocumentSummary `json:"documents"`
			Count     int                      `json:"count"`
			Next      any                      `json:"next_after"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Len(t, body.Documents, tc.count)
		require.Equal(t, tc.count, body.Count)
		require.Equal(t, tc.next, body.Next)
		if tc.count > 0 {
			require.Equal(t, tc.id, body.Documents[0].ID)
		} else {
			require.Contains(t, w.Body.String(), `"documents":[]`)
		}
	}
}

func TestListingIncludesStatus(t *testing.T) {
	w := httptest.NewRecorder()
	store := summaryStore{documents: []domain.DocumentSummary{{ID: summaryFirstID, DocType: "document", Status: "processing"}, {ID: summarySecondID, Status: "completed", ChunkCount: 3}}}
	store.beforeRow = func(i int) {
		if i == 1 {
			require.Contains(t, w.Body.String(), summaryFirstID, "first row must be written before requesting the second")
		}
	}
	handler.ListDocumentsHandler(store)(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/documents", http.NoBody))
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, strings.HasPrefix(w.Body.String(), `{"documents":[`))
	require.True(t, strings.HasSuffix(strings.TrimSpace(w.Body.String()), `],"count":2}`))
	var body struct {
		Documents []map[string]any `json:"documents"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "processing", body.Documents[0]["status"])
	require.Equal(t, float64(3), body.Documents[1]["chunk_count"])
	require.Contains(t, body.Documents[0], "created_at")
	require.NotContains(t, body.Documents[0], "s3_key")
}

func TestListingRejectsBadLimit(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=-1", "limit=1001", "limit=abc", "limit=", "limit=999999999999999999999999", "limit=1&after=bad", "limit=1&after=", "after=" + summaryFirstID, "limit=1&limit=2", "limit=1&after=" + summaryFirstID + "&after=" + summarySecondID} {
		t.Run(query, func(t *testing.T) {
			w := summaryRequest(t, handler.ListDocumentsHandler(summaryStore{}), "/documents?"+query, "")
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

func TestDocumentSummaryStorageErrors(t *testing.T) {
	store := summaryStore{err: errors.New("database unavailable")}
	router := api.NewRouter(&api.RouterDeps{Config: &config.Config{}, ChunkStore: store})
	for _, path := range []string{"/documents", "/documents?limit=1", "/documents/" + summaryFirstID} {
		w := summaryRequest(t, router, path, "")
		require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
		var body map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "internal", body["error"])
	}
}
