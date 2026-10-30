package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/stretchr/testify/require"
)

type identityWorkerStore struct {
	domain.ChunkStore
	document    *domain.Document
	documentErr error
	fingerprint bool
	statuses    []string
}

func (s *identityWorkerStore) GetDocument(context.Context, domain.DocumentID) (*domain.Document, error) {
	return s.document, s.documentErr
}

func (s *identityWorkerStore) CheckFingerprint(context.Context, string) (domain.DocumentID, bool, error) {
	return "other-document", s.fingerprint, nil
}

func (s *identityWorkerStore) UpdateDocumentStatus(_ context.Context, _ domain.DocumentID, status string) error {
	s.statuses = append(s.statuses, status)
	return nil
}

type identityWorkerStage struct {
	processed []domain.DocumentID
	err       error
}

func (*identityWorkerStage) ID() pipeline.StageID { return pipeline.StagePersist }

func (s *identityWorkerStage) Run(_ context.Context, ic *pipeline.IngestContext) error {
	s.processed = append(s.processed, ic.DocumentID)
	return s.err
}

func TestWorkerSkipsExistingCallerID(t *testing.T) {
	store := &identityWorkerStore{document: &domain.Document{ID: "caller-document", Content: "Original document"}}
	stage := &identityWorkerStage{}
	w := NewWorker(1, []pipeline.Stage{stage}, store)
	w.processTask(t.Context(), IngestTask{DocumentID: "caller-document", Content: "Different document", CallerSupplied: true})
	require.Empty(t, stage.processed)
	require.Empty(t, store.statuses)
}

func TestWorkerProcessesAcceptedFingerprint(t *testing.T) {
	for _, callerSupplied := range []bool{false, true} {
		store := &identityWorkerStore{fingerprint: true}
		stage := &identityWorkerStage{}
		w := NewWorker(1, []pipeline.Stage{stage}, store)
		w.processTask(t.Context(), IngestTask{DocumentID: "accepted-document", Content: "Duplicate content", CallerSupplied: callerSupplied})
		require.Equal(t, []domain.DocumentID{"accepted-document"}, stage.processed)
		require.Equal(t, []string{"completed"}, store.statuses)
	}
}

func TestWorkerDocumentConflictDoesNotRecordFailure(t *testing.T) {
	store := &identityWorkerStore{}
	stage := &identityWorkerStage{err: fmt.Errorf("insert document: %w", domain.ErrDocumentExists)}
	w := NewWorker(1, []pipeline.Stage{stage}, store)
	w.processTask(t.Context(), IngestTask{DocumentID: "caller-document", CallerSupplied: true})
	require.Equal(t, []domain.DocumentID{"caller-document"}, stage.processed)
	require.Empty(t, store.statuses)
}

func TestWorkerDocumentLookupFailureStopsProcessing(t *testing.T) {
	store := &identityWorkerStore{documentErr: errors.New("unavailable")}
	stage := &identityWorkerStage{}
	w := NewWorker(1, []pipeline.Stage{stage}, store)
	w.processTask(t.Context(), IngestTask{DocumentID: "caller-document", CallerSupplied: true})
	require.Empty(t, stage.processed)
	require.Empty(t, store.statuses)
}
