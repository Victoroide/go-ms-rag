package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/metrics"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/stretchr/testify/require"
)

type metricJobStore struct {
	*leaseTestStore
	transitionErr error
	complete      bool
}

func (s metricJobStore) CompleteJob(context.Context, domain.Job, []domain.Chunk) (bool, error) {
	return s.complete, s.transitionErr
}
func (s metricJobStore) RetryJob(context.Context, domain.Job, time.Duration, string) error {
	return s.transitionErr
}
func (s metricJobStore) FailJob(context.Context, domain.Job, string) error { return s.transitionErr }
func (s metricJobStore) ReleaseJob(context.Context, domain.Job) error      { return s.transitionErr }

func TestJobMetricsCountOnlyAppliedTransitions(t *testing.T) {
	for _, result := range []string{"completed", "retried", "failed", "released"} {
		for _, rejected := range []bool{false, true} {
			t.Run(result+"/"+map[bool]string{false: "applied", true: "rejected"}[rejected], func(t *testing.T) {
				m := metrics.NewService(nil)
				store := metricJobStore{leaseTestStore: leaseStore(0), complete: true}
				if rejected {
					store.transitionErr = domain.ErrJobNotOwned
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				stage := leaseTestStage{func(context.Context) error {
					switch result {
					case "retried":
						return errors.New("temporary")
					case "failed":
						return domain.Permanent(errors.New("invalid"))
					case "released":
						cancel()
						return context.Canceled
					}
					return nil
				}}
				w := New(store, []pipeline.Stage{stage}, nil, Options{Metrics: m})
				w.process(ctx, domain.Job{Document: domain.Document{ID: "doc", Content: "text"}, Attempts: 1})
				var b strings.Builder
				require.NoError(t, m.Registry.WriteText(&b))
				sample := `quarry_ingest_jobs_total{result="` + result + `"} 1`
				if rejected {
					require.NotContains(t, b.String(), sample)
				} else {
					require.Contains(t, b.String(), sample)
				}
			})
		}
	}
	t.Run("completion_not_applied", func(t *testing.T) {
		m := metrics.NewService(nil)
		store := metricJobStore{leaseTestStore: leaseStore(0)}
		w := New(store, nil, nil, Options{Metrics: m})
		w.process(t.Context(), domain.Job{Document: domain.Document{ID: "doc"}})
		var b strings.Builder
		require.NoError(t, m.Registry.WriteText(&b))
		require.NotContains(t, b.String(), `quarry_ingest_jobs_total{result="completed"} 1`)
	})
}
