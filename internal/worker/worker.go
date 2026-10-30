// Package worker processes durable ingestion jobs under renewable leases.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/metrics"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/aws/smithy-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Options bounds parallel processing, retries, and lease lifetimes.
type Options struct {
	Workers, MaxAttempts            int
	Lease, JobTimeout, PollInterval time.Duration
	Owner                           string
	Metrics                         *metrics.Service
}

var errLeaseLost = errors.New("ingestion lease lost")
var errShutdown = errors.New("ingestion shutdown")

// Worker claims and processes persisted ingestion jobs.
type Worker struct {
	store            domain.JobStore
	stages           []pipeline.Stage
	vectors          domain.VectorDeleter
	opts             Options
	wake, stop       chan struct{}
	wg               sync.WaitGroup
	mu               sync.Mutex
	cancel           context.CancelCauseFunc
	started, stopped bool
}

// New constructs a durable ingestion processor.
func New(store domain.JobStore, stages []pipeline.Stage, vectors domain.VectorDeleter, opts Options) *Worker {
	if opts.Metrics == nil {
		opts.Metrics = metrics.Default
	}
	if opts.Workers < 1 {
		opts.Workers = 2
	}
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 5
	}
	if opts.Lease <= 0 {
		opts.Lease = 2 * time.Minute
	}
	if opts.JobTimeout <= 0 {
		opts.JobTimeout = 15 * time.Minute
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	return &Worker{store: store, stages: stages, vectors: vectors, opts: opts, wake: make(chan struct{}, 1), stop: make(chan struct{})}
}

// Notify wakes a claim loop; polling also discovers work accepted by other replicas.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start launches the configured number of claim loops once.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.stopped {
		return
	}
	w.started = true
	jobCtx, cancel := context.WithCancelCause(ctx)
	w.cancel = cancel
	for range w.opts.Workers {
		w.wg.Go(func() { w.loop(jobCtx) })
	}
}
func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()
	for {
		for {
			select {
			case <-w.stop:
				return
			case <-ctx.Done():
				return
			default:
			}
			job, err := w.store.ClaimJob(ctx, w.opts.Owner, w.opts.Lease, w.opts.MaxAttempts)
			if err != nil {
				slog.ErrorContext(ctx, "claim ingestion job", "error", err)
				break
			}
			if job == nil {
				break
			}
			w.process(ctx, *job)
		}
		select {
		case <-w.stop:
			return
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

// Shutdown waits for current jobs to finish.
func (w *Worker) Shutdown() { _ = w.ShutdownContext(context.Background()) }

// ShutdownContext stops claiming and releases unfinished leases at the deadline.
func (w *Worker) ShutdownContext(ctx context.Context) error {
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		close(w.stop)
	}
	cancel := w.cancel
	w.mu.Unlock()
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		if cancel != nil {
			cancel(errShutdown)
		}
		return nil
	case <-ctx.Done():
		if cancel != nil {
			cancel(errShutdown)
		}
		<-done
		return ctx.Err()
	}
}

func (w *Worker) process(parent context.Context, job domain.Job) {
	parent = metrics.WithService(parent, w.opts.Metrics)
	timeoutCtx, stop := context.WithTimeout(parent, w.opts.JobTimeout)
	defer stop()
	ctx, cancel := context.WithCancelCause(timeoutCtx)
	defer cancel(nil)
	leaseCtx, cancelLease := context.WithCancelCause(parent)
	hbDone, hbStop := make(chan struct{}), make(chan struct{})
	defer func() { close(hbStop); cancelLease(nil); <-hbDone }()
	go func() {
		defer close(hbDone)
		ticker := time.NewTicker(w.opts.Lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-hbStop:
				return
			case <-ticker.C:
				ok, err := w.store.HeartbeatJob(leaseCtx, job, w.opts.Lease)
				if err != nil {
					slog.ErrorContext(leaseCtx, "renew ingestion lease", "document_id", job.Document.ID, "error", err)
				}
				if err != nil || !ok {
					cancelLease(errLeaseLost)
					cancel(errLeaseLost)
					return
				}
			}
		}
	}()
	ic := pipeline.NewIngestContext(job.Document.ID, job.Document.Content, job.Document.Metadata)
	ic.FileName = job.Document.FileName
	ic.FileType = job.Document.FileType
	ic.S3Key = job.Document.S3Key
	ic.DocType = job.DocType
	err := w.run(ctx, job, ic)
	if parent.Err() != nil {
		w.release(parent, job)
		return
	}
	if errors.Is(context.Cause(leaseCtx), errLeaseLost) {
		w.compensateDeleted(parent, job)
		return
	}
	if err == nil {
		return
	}
	w.compensateDeleted(parent, job)
	if permanent(err) || job.Attempts >= w.opts.MaxAttempts {
		confirmCtx, confirmCancel := context.WithTimeout(leaseCtx, 5*time.Second)
		owned, confirmErr := w.store.HeartbeatJob(confirmCtx, job, w.opts.Lease)
		confirmCancel()
		if confirmErr != nil || !owned {
			w.logStateError(job, confirmErr)
			w.compensateDeleted(parent, job)
			return
		}
		w.deleteVectors(leaseCtx, job)
		if parent.Err() != nil {
			w.release(parent, job)
			return
		}
		if errors.Is(context.Cause(leaseCtx), errLeaseLost) {
			w.compensateDeleted(parent, job)
			return
		}
		stateCtx, stateCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer stateCancel()
		failErr := w.store.FailJob(stateCtx, job, err.Error())
		w.recordOutcome(job, "failed", failErr)
		if errors.Is(failErr, domain.ErrJobNotOwned) {
			w.compensateDeleted(parent, job)
		}
		return
	}
	stateCtx, stateCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer stateCancel()
	delay := 5 * time.Second
	for i := 1; i < job.Attempts && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	delay = min(delay, 5*time.Minute) + time.Duration(rand.Int64N(int64(time.Second)+1)) // #nosec G404 -- Retry jitter does not require cryptographic randomness.
	w.recordOutcome(job, "retried", w.store.RetryJob(stateCtx, job, delay, err.Error()))
}

func (w *Worker) release(ctx context.Context, job domain.Job) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	w.recordOutcome(job, "released", w.store.ReleaseJob(releaseCtx, job))
}

func (w *Worker) run(ctx context.Context, job domain.Job, ic *pipeline.IngestContext) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("ingestion panic: %v", recovered)
		}
	}()
	if w.vectors != nil {
		if err = w.vectors.DeleteByDocID(ctx, string(job.Document.ID)); err != nil {
			return fmt.Errorf("remove previous vectors: %w", err)
		}
	}
	if _, err = pipeline.RunIngestionPipeline(ctx, ic, w.stages); err != nil {
		return err
	}
	ok, err := w.store.CompleteJob(ctx, job, ic.Chunks)
	if err != nil {
		return err
	}
	if !ok {
		w.compensateDeleted(ctx, job)
	} else {
		w.opts.Metrics.IngestJobs.WithLabelValues("completed").Inc()
	}
	return nil
}
func permanent(err error) bool {
	if errors.Is(err, domain.ErrPermanent) || errors.Is(err, pipeline.ErrNoChunks) || status.Code(err) == codes.InvalidArgument {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationException"
}
func (w *Worker) compensateDeleted(ctx context.Context, job domain.Job) {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	exists, err := w.store.DocumentExists(checkCtx, job.Document.ID)
	if err != nil {
		w.logStateError(job, err)
		return
	}
	if !exists {
		w.compensate(ctx, job)
	}
}
func (w *Worker) compensate(ctx context.Context, job domain.Job) {
	w.deleteVectors(context.WithoutCancel(ctx), job)
}
func (w *Worker) deleteVectors(ctx context.Context, job domain.Job) {
	if w.vectors == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	w.logStateError(job, w.vectors.DeleteByDocID(cleanupCtx, string(job.Document.ID)))
}

func (w *Worker) logStateError(job domain.Job, err error) {
	if err != nil {
		slog.Error("update ingestion state", "document_id", job.Document.ID, "error", err)
	}
}

func (w *Worker) recordOutcome(job domain.Job, result string, err error) {
	w.logStateError(job, err)
	if err == nil {
		w.opts.Metrics.IngestJobs.WithLabelValues(result).Inc()
	}
}
