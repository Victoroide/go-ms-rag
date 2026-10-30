package metrics

import "context"

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

// Service holds the service's fixed metric families.
type Service struct {
	Registry      *Registry
	HTTPRequests  *CounterVec
	HTTPDuration  *HistogramVec
	StageDuration *HistogramVec
	IngestJobs    *CounterVec
	QueryMode     *CounterVec
}

// NewService creates the fixed metric families and pending-job gauge.
func NewService(pending func() float64) *Service {
	r := NewRegistry()
	if pending == nil {
		pending = func() float64 { return 0 }
	}
	r.NewGaugeFunc("quarry_ingest_jobs_pending", "Pending ingestion jobs in the shared database.", pending)
	return &Service{
		Registry:      r,
		HTTPRequests:  r.NewCounterVec("quarry_http_requests_total", "HTTP requests.", "route", "code"),
		HTTPDuration:  r.NewHistogramVec("quarry_http_request_duration_seconds", "HTTP request duration in seconds.", durationBuckets, "route"),
		StageDuration: r.NewHistogramVec("quarry_pipeline_stage_duration_seconds", "Pipeline stage duration in seconds.", durationBuckets, "stage"),
		IngestJobs:    r.NewCounterVec("quarry_ingest_jobs_total", "Applied ingestion job outcomes.", "result"),
		QueryMode:     r.NewCounterVec("quarry_query_mode_total", "Queries by effective retrieval mode.", "mode"),
	}
}

// Default collects metrics when no service is supplied in the context.
var Default = NewService(nil)

type serviceKey struct{}

// WithService attaches a collection to pipeline execution.
func WithService(ctx context.Context, s *Service) context.Context {
	return context.WithValue(ctx, serviceKey{}, s)
}

// FromContext returns the attached collection or Default.
func FromContext(ctx context.Context) *Service {
	if s, ok := ctx.Value(serviceKey{}).(*Service); ok && s != nil {
		return s
	}
	return Default
}
