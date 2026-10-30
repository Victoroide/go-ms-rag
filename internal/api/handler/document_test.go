package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

type fileDeleteContextKey struct{}

type fileDeleteStore struct {
	domain.ChunkStore
	document *domain.Document
	deleted  bool
	check    func(context.Context) error
}

func (s *fileDeleteStore) GetDocument(ctx context.Context, _ domain.DocumentID) (*domain.Document, error) {
	if s.check != nil {
		if err := s.check(ctx); err != nil {
			return nil, err
		}
	}
	return s.document, nil
}

func (s *fileDeleteStore) DeleteDocument(ctx context.Context, _ domain.DocumentID) error {
	if s.check != nil {
		if err := s.check(ctx); err != nil {
			return err
		}
	}
	s.deleted = true
	return nil
}

func TestDeleteSurvivesClientDisconnect(t *testing.T) {
	const docID = "8a3e792c-5df6-4b6e-a612-4f7db4959db2"
	store := &fileDeleteStore{document: &domain.Document{ID: docID}}
	store.check = func(ctx context.Context) error {
		if ctx.Value(fileDeleteContextKey{}) != "request-value" {
			t.Error("deletion lost request context values")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Minute {
			t.Error("deletion has no bounded independent deadline")
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), fileDeleteContextKey{}, "request-value"))
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodDelete, "/documents/"+docID, http.NoBody)
	req.SetPathValue("doc_id", docID)
	resp := httptest.NewRecorder()
	DeleteHandler(store, nil, nil)(resp, req)
	if resp.Code != http.StatusOK || !store.deleted {
		t.Fatalf("status = %d, deleted = %v, body = %s", resp.Code, store.deleted, resp.Body.String())
	}
}

func TestDeleteReturnsStorageUnavailable(t *testing.T) {
	const docID = "8a3e792c-5df6-4b6e-a612-4f7db4959db2"
	key := "documents/document/" + docID + "/file.pdf"
	store := &fileDeleteStore{document: &domain.Document{ID: docID, S3Key: &key}}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/documents/"+docID, http.NoBody)
	req.SetPathValue("doc_id", docID)
	resp := httptest.NewRecorder()
	DeleteHandler(store, nil, nil)(resp, req)
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if resp.Code != http.StatusServiceUnavailable || body.Error != "storage" || body.Message != "stored file cannot be deleted because file storage is not configured" || store.deleted {
		t.Fatalf("status = %d, body = %+v, deleted = %v", resp.Code, body, store.deleted)
	}
}
