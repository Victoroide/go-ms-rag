package worker

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/stretchr/testify/require"
)

type leaseTestStore struct {
	mu        sync.Mutex
	jobs      []domain.Job
	events    chan string
	heartbeat bool
	delay     time.Duration
}

func leaseStore(n int) *leaseTestStore {
	s := &leaseTestStore{events: make(chan string, 100), heartbeat: true}
	for range n {
		s.jobs = append(s.jobs, domain.Job{Document: domain.Document{ID: "doc", Content: "text"}, Attempts: 1})
	}
	return s
}
func (s *leaseTestStore) ClaimJob(_ context.Context, owner string, _ time.Duration, _ int) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		return nil, nil
	}
	j := s.jobs[0]
	j.LeaseOwner = owner
	s.jobs = s.jobs[1:]
	return &j, nil
}
func (s *leaseTestStore) HeartbeatJob(context.Context, domain.Job, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeat, nil
}
func (s *leaseTestStore) CompleteJob(context.Context, domain.Job, []domain.Chunk) (bool, error) {
	s.events <- "completed"
	return true, nil
}
func (s *leaseTestStore) RetryJob(ctx context.Context, _ domain.Job, d time.Duration, _ string) error {
	s.mu.Lock()
	s.delay = d
	s.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.events <- "retry"
	return nil
}
func (s *leaseTestStore) FailJob(context.Context, domain.Job, string) error {
	s.events <- "failed"
	return nil
}
func (s *leaseTestStore) ReleaseJob(ctx context.Context, _ domain.Job) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.events <- "released"
	return nil
}
func (*leaseTestStore) DocumentExists(context.Context, domain.DocumentID) (bool, error) {
	return true, nil
}

type leaseTestStage struct{ run func(context.Context) error }

func (leaseTestStage) ID() pipeline.StageID                                       { return pipeline.StageEmbed }
func (s leaseTestStage) Run(ctx context.Context, _ *pipeline.IngestContext) error { return s.run(ctx) }
func leaseWorker(s *leaseTestStore, st pipeline.Stage, workers int) *Worker {
	return New(s, []pipeline.Stage{st}, nil, Options{Workers: workers, MaxAttempts: 5, Lease: 30 * time.Millisecond, JobTimeout: time.Second, PollInterval: time.Millisecond, Owner: "test"})
}
func leaseEvent(t *testing.T, s *leaseTestStore, want string) {
	t.Helper()
	select {
	case got := <-s.events:
		require.Equal(t, want, got)
	case <-time.After(3 * time.Second):
		t.Fatal("no job state transition")
	}
}
func TestRetryableErrorIsRescheduled(t *testing.T) {
	s := leaseStore(1)
	w := leaseWorker(s, leaseTestStage{func(context.Context) error { return errors.New("temporary") }}, 1)
	w.Start(t.Context())
	defer w.Shutdown()
	leaseEvent(t, s, "retry")
	s.mu.Lock()
	defer s.mu.Unlock()
	require.GreaterOrEqual(t, s.delay, 5*time.Second)
	require.LessOrEqual(t, s.delay, 6*time.Second)
}
func TestPermanentErrorMarksDocumentFailed(t *testing.T) {
	s := leaseStore(1)
	w := leaseWorker(s, leaseTestStage{func(context.Context) error { return domain.Permanent(errors.New("invalid")) }}, 1)
	w.Start(t.Context())
	defer w.Shutdown()
	leaseEvent(t, s, "failed")
}
func TestHeartbeatLossCancelsJob(t *testing.T) {
	s := leaseStore(1)
	s.heartbeat = false
	cause := make(chan error, 1)
	w := leaseWorker(s, leaseTestStage{func(ctx context.Context) error { <-ctx.Done(); cause <- context.Cause(ctx); return ctx.Err() }}, 1)
	w.Start(t.Context())
	defer w.Shutdown()
	select {
	case err := <-cause:
		require.ErrorIs(t, err, errLeaseLost)
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel processing")
	}
	w.Shutdown()
	select {
	case event := <-s.events:
		t.Fatalf("lost owner changed job: %s", event)
	default:
	}
}
func TestShutdownReleasesLease(t *testing.T) {
	s := leaseStore(1)
	started := make(chan struct{})
	w := leaseWorker(s, leaseTestStage{func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }}, 1)
	w.Start(t.Context())
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, w.ShutdownContext(ctx), context.Canceled)
	leaseEvent(t, s, "released")
	w.Shutdown()
}
func TestWorkersProcessJobsInParallel(t *testing.T) {
	s := leaseStore(2)
	entered := make(chan struct{}, 2)
	finish := make(chan struct{})
	w := leaseWorker(s, leaseTestStage{func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}, 2)
	w.Start(t.Context())
	defer w.Shutdown()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("documents did not process in parallel")
		}
	}
	close(finish)
	leaseEvent(t, s, "completed")
	leaseEvent(t, s, "completed")
}

func TestJobTimeoutReschedulesWithDetachedContext(t *testing.T) {
	s := leaseStore(1)
	w := leaseWorker(s, leaseTestStage{func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}, 1)
	w.opts.JobTimeout = 5 * time.Millisecond
	w.opts.Lease = time.Second
	w.Start(t.Context())
	defer w.Shutdown()
	leaseEvent(t, s, "retry")
}
func TestExhaustedAttemptMarksDocumentFailed(t *testing.T) {
	s := leaseStore(1)
	s.jobs[0].Attempts = 5
	w := leaseWorker(s, leaseTestStage{func(context.Context) error { return errors.New("temporary") }}, 1)
	w.Start(t.Context())
	defer w.Shutdown()
	leaseEvent(t, s, "failed")
}

type leaseVectorRecorder struct{ events chan string }

func (leaseVectorRecorder) DeleteAllVectorsInNamespace(context.Context, string) error { return nil }
func (v leaseVectorRecorder) DeleteByDocID(context.Context, string) error {
	v.events <- "delete"
	return nil
}
func TestEveryAttemptDeletesVectorsBeforeStages(t *testing.T) {
	for _, attempt := range []int{1, 2} {
		t.Run(strconv.Itoa(attempt), func(t *testing.T) {
			s := leaseStore(1)
			s.jobs[0].Attempts = attempt
			w := leaseWorker(s, leaseTestStage{func(context.Context) error { s.events <- "stage"; return nil }}, 1)
			w.vectors = leaseVectorRecorder{s.events}
			w.Start(t.Context())
			defer w.Shutdown()
			leaseEvent(t, s, "delete")
			leaseEvent(t, s, "stage")
			leaseEvent(t, s, "completed")
		})
	}
}
func TestTerminalCleanupPrecedesFailure(t *testing.T) {
	s := leaseStore(1)
	s.jobs[0].Attempts = 2
	w := leaseWorker(s, leaseTestStage{func(context.Context) error { s.events <- "stage"; return domain.Permanent(errors.New("invalid")) }}, 1)
	w.vectors = leaseVectorRecorder{s.events}
	w.Start(t.Context())
	defer w.Shutdown()
	leaseEvent(t, s, "delete")
	leaseEvent(t, s, "stage")
	leaseEvent(t, s, "delete")
	leaseEvent(t, s, "failed")
}
func TestTerminalFailureDoesNotDeleteAfterLeaseLoss(t *testing.T) {
	s := leaseStore(1)
	s.jobs[0].Attempts = 2
	w := leaseWorker(s, leaseTestStage{func(context.Context) error {
		s.mu.Lock()
		s.heartbeat = false
		s.mu.Unlock()
		return domain.Permanent(errors.New("invalid"))
	}}, 1)
	w.vectors = leaseVectorRecorder{s.events}
	w.process(t.Context(), s.jobs[0])
	leaseEvent(t, s, "delete")
	require.Empty(t, s.events)
}

type leaseRenewalStore struct {
	*leaseTestStore
	renewals atomic.Int32
	renewed  chan struct{}
}

func (s *leaseRenewalStore) HeartbeatJob(context.Context, domain.Job, time.Duration) (bool, error) {
	if s.renewals.Add(1) == 3 {
		close(s.renewed)
	}
	return true, nil
}

type leaseBlockingVectors struct {
	calls   int
	renewed <-chan struct{}
}

func (*leaseBlockingVectors) DeleteAllVectorsInNamespace(context.Context, string) error { return nil }
func (v *leaseBlockingVectors) DeleteByDocID(ctx context.Context, _ string) error {
	v.calls++
	if v.calls == 1 {
		return nil
	}
	select {
	case <-v.renewed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return errors.New("lease was not renewed during cleanup")
	}
}
func TestTerminalCleanupKeepsHeartbeatActive(t *testing.T) {
	s := &leaseRenewalStore{leaseTestStore: leaseStore(1), renewed: make(chan struct{})}
	vectors := &leaseBlockingVectors{renewed: s.renewed}
	w := New(s, []pipeline.Stage{leaseTestStage{func(context.Context) error { return domain.Permanent(errors.New("invalid")) }}}, vectors, Options{Workers: 1, MaxAttempts: 5, Lease: 30 * time.Millisecond, JobTimeout: 5 * time.Millisecond, PollInterval: time.Millisecond, Owner: "renewal"})
	w.Start(t.Context())
	defer w.Shutdown()
	leaseEvent(t, s.leaseTestStore, "failed")
	w.Shutdown()
	require.GreaterOrEqual(t, s.renewals.Load(), int32(3))
	require.Equal(t, 2, vectors.calls)
}
