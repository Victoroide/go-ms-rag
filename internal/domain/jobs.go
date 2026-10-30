package domain

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AcceptOutcome describes the atomic acceptance decision.
type AcceptOutcome int

// Acceptance outcomes distinguish existing documents from newly queued work.
const (
	AcceptAccepted AcceptOutcome = iota
	AcceptDuplicate
	AcceptInProgress
	AcceptRequeued
	AcceptConflict
	AcceptQueueFull
)

// AcceptRequest supplies document identity, content, and queue admission limits.
type AcceptRequest struct {
	Document       Document
	CallerSupplied bool
	Fingerprint    string
	DocType        string
	MaxPending     int
}

// AcceptResult identifies the document selected by acceptance.
type AcceptResult struct {
	DocumentID DocumentID
	Outcome    AcceptOutcome
}

// DocumentAcceptor durably stores accepted documents and their pending work.
type DocumentAcceptor interface {
	AcceptDocument(context.Context, AcceptRequest) (AcceptResult, error)
}

// Job carries the document and the current lease identity.
type Job struct {
	Document   Document
	DocType    string
	Attempts   int
	LeaseOwner string
}

// JobStore coordinates durable ingestion through owned leases.
type JobStore interface {
	ClaimJob(context.Context, string, time.Duration, int) (*Job, error)
	HeartbeatJob(context.Context, Job, time.Duration) (bool, error)
	CompleteJob(context.Context, Job, []Chunk) (bool, error)
	RetryJob(context.Context, Job, time.Duration, string) error
	FailJob(context.Context, Job, string) error
	ReleaseJob(context.Context, Job) error
	DocumentExists(context.Context, DocumentID) (bool, error)
}

// ErrPermanent marks failures that cannot succeed on retry.
var ErrPermanent = errors.New("permanent ingestion failure")

// Permanent preserves the original error while marking it non-retryable.
func Permanent(err error) error { return fmt.Errorf("%w: %w", ErrPermanent, err) }

// ErrJobNotOwned indicates that a lease transition did not apply.
var ErrJobNotOwned = errors.New("ingestion job lease is not owned")
