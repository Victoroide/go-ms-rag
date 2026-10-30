package cleanup

import (
	"context"
	"errors"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

type fileCleanupStore struct {
	domain.ChunkStore
	deletions int
}

func (s *fileCleanupStore) DeleteDocument(context.Context, domain.DocumentID) error {
	s.deletions++
	return nil
}

type fileCleanupVectors struct{ deletions int }

func (v *fileCleanupVectors) DeleteAllVectorsInNamespace(context.Context, string) error {
	v.deletions++
	return nil
}

func (v *fileCleanupVectors) DeleteByDocID(context.Context, string) error {
	v.deletions++
	return nil
}

func TestDeleteRefusesWhenStoredFileCannotBeRemoved(t *testing.T) {
	key := "documents/document/id/file.pdf"
	store := &fileCleanupStore{}
	vectors := &fileCleanupVectors{}
	err := Remove(t.Context(), store, vectors, nil, &domain.Document{ID: "id", S3Key: &key})
	if !errors.Is(err, ErrFileStorageUnavailable) {
		t.Errorf("expected unavailable file storage, got %v", err)
	}
	if vectors.deletions != 0 || store.deletions != 0 {
		t.Fatalf("deleted vectors %d times and metadata %d times before checking storage", vectors.deletions, store.deletions)
	}
}
