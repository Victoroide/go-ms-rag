// Package worker provides an async ingest queue backed by a buffered channel.
package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/pipeline"
)

// IngestTask represents a document to be ingested asynchronously.
type IngestTask struct {
	DocumentID domain.DocumentID
	Content    string
	Metadata   map[string]any
	FileName   *string
	FileType   *string
	S3Key      *string
}

// Worker processes ingest tasks from a buffered channel queue.
type Worker struct {
	queue  chan IngestTask
	stages []pipeline.Stage
	cs     domain.ChunkStore
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// NewWorker creates a Worker with the given queue size and pipeline stages.
func NewWorker(queueSize int, stages []pipeline.Stage, cs domain.ChunkStore) *Worker {
	if queueSize < 1 {
		queueSize = 100
	}
	return &Worker{
		queue:  make(chan IngestTask, queueSize),
		stages: stages,
		cs:     cs,
	}
}

// Enqueue adds a task to the ingest queue. Returns false if the queue is full.
func (w *Worker) Enqueue(task IngestTask) bool {
	select {
	case w.queue <- task:
		return true
	default:
		log.Printf("worker: queue full, dropping task for document %s", task.DocumentID)
		return false
	}
}

// Start launches the single goroutine consumer.
func (w *Worker) Start(ctx context.Context) {
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-workerCtx.Done():
				log.Printf("worker: shutting down")
				return
			case task, ok := <-w.queue:
				if !ok {
					return
				}
				w.processTask(workerCtx, task)
			}
		}
	}()
	log.Printf("worker: started with queue size %d", cap(w.queue))
}

// Shutdown gracefully stops the worker and waits for in-flight tasks.
func (w *Worker) Shutdown() {
	if w.cancel != nil {
		w.cancel()
	}
	close(w.queue)
	w.wg.Wait()
	log.Printf("worker: shutdown complete")
}

func (w *Worker) processTask(ctx context.Context, task IngestTask) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("worker: panic processing document %s: %v", task.DocumentID, r)
			if err := w.cs.UpdateDocumentStatus(ctx, task.DocumentID, "failed"); err != nil {
				log.Printf("worker: update status after panic for %s: %v", task.DocumentID, err)
			}
		}
	}()

	// Check fingerprint for dedup.
	fp := chunker.Fingerprint(task.Content)
	existingID, exists, err := w.cs.CheckFingerprint(ctx, fp)
	if err != nil {
		log.Printf("worker: fingerprint check for document %s: %v", task.DocumentID, err)
	}
	if exists {
		log.Printf("worker: document %s is duplicate of %s, skipping", task.DocumentID, existingID)
		return
	}

	ic := pipeline.NewIngestContext(task.DocumentID, task.Content, task.Metadata)
	ic.FileName = task.FileName
	ic.FileType = task.FileType
	ic.S3Key = task.S3Key
	_, err = pipeline.RunIngestionPipeline(ctx, ic, w.stages)
	if err != nil {
		log.Printf("worker: ingest failed for document %s: %v", task.DocumentID, err)
		if statusErr := w.cs.UpdateDocumentStatus(ctx, task.DocumentID, "failed"); statusErr != nil {
			log.Printf("worker: update status to failed for %s: %v", task.DocumentID, statusErr)
		}
		return
	}

	if statusErr := w.cs.UpdateDocumentStatus(ctx, task.DocumentID, "completed"); statusErr != nil {
		log.Printf("worker: update status to completed for %s: %v", task.DocumentID, statusErr)
	}
	log.Printf("worker: document %s ingested in %s", task.DocumentID, time.Since(start))
}
