package extractor

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrBusy indicates that no upload slot became available before the queue timeout.
var ErrBusy = errors.New("extractor: too many concurrent uploads")

// Limiter bounds concurrent uploads, including the time spent reading their bodies.
type Limiter struct {
	slots chan struct{}
	wait  time.Duration
}

// NewLimiter creates a limiter with n slots and a maximum queue wait.
func NewLimiter(n int, wait time.Duration) *Limiter {
	if n < 1 {
		n = 1
	}
	return &Limiter{slots: make(chan struct{}, n), wait: wait}
}

// Acquire reserves a slot and returns an idempotent release function.
func (l *Limiter) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case l.slots <- struct{}{}:
		return l.release(), nil
	default:
	}
	if l.wait <= 0 {
		return nil, ErrBusy
	}
	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	select {
	case l.slots <- struct{}{}:
		return l.release(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrBusy
	}
}

func (l *Limiter) release() func() {
	var once sync.Once
	return func() { once.Do(func() { <-l.slots }) }
}
