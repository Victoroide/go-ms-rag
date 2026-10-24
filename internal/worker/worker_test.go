package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/pipeline"
)

func TestShutdownIsIdempotentAndRejectsEnqueue(t *testing.T) {
	w := NewWorker(1, nil, nil)
	w.Shutdown()
	w.Shutdown()
	if w.Enqueue(IngestTask{}) {
		t.Fatal("stopped worker accepted task")
	}
}
func TestConcurrentShutdownAndEnqueue(t *testing.T) {
	w := NewWorker(1, nil, nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 10 {
				w.Enqueue(IngestTask{})
			}
		})
		wg.Go(w.Shutdown)
	}
	wg.Wait()
	if w.Enqueue(IngestTask{}) {
		t.Fatal("stopped worker accepted task")
	}
}

type cancelStore struct{ domain.ChunkStore }

func (cancelStore) CheckFingerprint(context.Context, string) (domain.DocumentID, bool, error) {
	return "", false, nil
}
func (cancelStore) UpdateDocumentStatus(context.Context, domain.DocumentID, string) error { return nil }

type waitingStage struct{ started chan struct{} }

func (waitingStage) ID() pipeline.StageID { return pipeline.StageEmbed }
func (s waitingStage) Run(ctx context.Context, _ *pipeline.IngestContext) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}
func TestShutdownDeadlineCancelsActiveStage(t *testing.T) {
	stage := waitingStage{started: make(chan struct{})}
	w := NewWorker(1, []pipeline.Stage{stage}, cancelStore{})
	w.Start(t.Context())
	if !w.Enqueue(IngestTask{Content: "test"}) {
		t.Fatal("could not enqueue")
	}
	<-stage.started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.ShutdownContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v want cancellation", err)
	}
	if w.Enqueue(IngestTask{}) {
		t.Fatal("shutdown worker accepted a task")
	}
}

type statusStore struct {
	domain.ChunkStore
	called           bool
	statusContextErr error
	deadline         bool
}

func (s *statusStore) CheckFingerprint(context.Context, string) (domain.DocumentID, bool, error) {
	return "", false, nil
}
func (s *statusStore) UpdateDocumentStatus(ctx context.Context, _ domain.DocumentID, state string) error {
	s.called = state == "failed"
	s.statusContextErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	s.deadline = ok && time.Until(deadline) > 0 && time.Until(deadline) <= 5*time.Second
	return nil
}

type failingStage struct {
	panicValue bool
	cancel     context.CancelFunc
}

func (failingStage) ID() pipeline.StageID { return pipeline.StagePersist }
func (s failingStage) Run(ctx context.Context, _ *pipeline.IngestContext) error {
	if s.panicValue {
		s.cancel()
		panic("failure")
	}
	return ctx.Err()
}
func TestFailureStatusSurvivesTaskCancellation(t *testing.T) {
	for _, panicValue := range []bool{false, true} {
		store := &statusStore{}
		ctx, cancel := context.WithCancel(t.Context())
		if !panicValue {
			cancel()
		}
		w := NewWorker(1, []pipeline.Stage{failingStage{panicValue: panicValue, cancel: cancel}}, store)
		w.processTask(ctx, IngestTask{Content: "A failed document"})
		cancel()
		if !store.called || store.statusContextErr != nil || !store.deadline {
			t.Fatalf("invalid failure status context: %+v", store)
		}
	}
}
