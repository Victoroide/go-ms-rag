package metrics

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExpositionFormat(t *testing.T) {
	r := NewRegistry()
	r.NewGaugeFunc("z_pending", "Pending jobs.", func() float64 { return 3 })
	c := r.NewCounterVec("a_requests_total", "Requests.", "route", "code")
	c.WithLabelValues("GET /z", "200").Inc()
	c.WithLabelValues("GET /a", "500").Add(2)
	h := r.NewHistogramVec("b_duration_seconds", "Duration.", []float64{0.1, 1}, "stage")
	h.WithLabelValues("embed").Observe(0.5)
	var b strings.Builder
	require.NoError(t, r.WriteText(&b))
	require.Equal(t, `# HELP a_requests_total Requests.
# TYPE a_requests_total counter
a_requests_total{route="GET /a",code="500"} 2
a_requests_total{route="GET /z",code="200"} 1
# HELP b_duration_seconds Duration.
# TYPE b_duration_seconds histogram
b_duration_seconds_bucket{stage="embed",le="0.1"} 0
b_duration_seconds_bucket{stage="embed",le="1"} 1
b_duration_seconds_bucket{stage="embed",le="+Inf"} 1
b_duration_seconds_sum{stage="embed"} 0.5
b_duration_seconds_count{stage="embed"} 1
# HELP z_pending Pending jobs.
# TYPE z_pending gauge
z_pending 3
`, b.String())
	require.ErrorIs(t, r.WriteText(metricErrorWriter{}), io.ErrClosedPipe)
}

type metricErrorWriter struct{}

func (metricErrorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLabelEscaping(t *testing.T) {
	r := NewRegistry()
	r.NewCounterVec("requests_total", "Requests.\nSecond\\line.", "route").WithLabelValues("quote\"\\\nend").Inc()
	var b strings.Builder
	require.NoError(t, r.WriteText(&b))
	require.Contains(t, b.String(), `# HELP requests_total Requests.\nSecond\\line.`)
	require.Contains(t, b.String(), `requests_total{route="quote\"\\\nend"} 1`)
}

func TestHistogramBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogramVec("duration", "Duration.", []float64{1, 2}, "stage")
	for _, v := range []float64{0.5, 1, 1.5, 3} {
		h.WithLabelValues("x").Observe(v)
	}
	var b strings.Builder
	require.NoError(t, r.WriteText(&b))
	for _, line := range []string{`duration_bucket{stage="x",le="1"} 2`, `duration_bucket{stage="x",le="2"} 3`, `duration_bucket{stage="x",le="+Inf"} 4`, `duration_sum{stage="x"} 6`, `duration_count{stage="x"} 4`} {
		require.Contains(t, b.String(), line)
	}
}

func TestConcurrentCollection(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounterVec("requests_total", "Requests.", "code")
	h := r.NewHistogramVec("duration", "Duration.", []float64{1})
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				c.WithLabelValues("200").Inc()
				h.WithLabelValues().Observe(0.5)
				require.NoError(t, r.WriteText(io.Discard))
			}
		})
	}
	wg.Wait()
	var b strings.Builder
	require.NoError(t, r.WriteText(&b))
	require.Contains(t, b.String(), `requests_total{code="200"} 1000`)
	require.Contains(t, b.String(), "duration_count 1000")
}

func TestCachedGauge(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	fail := false
	read := cachedGauge(10*time.Second, func() (float64, error) {
		calls++
		if fail {
			return 0, errors.New("unavailable")
		}
		return float64(calls), nil
	}, func() time.Time { return now })
	require.Equal(t, 1.0, read())
	now = now.Add(9 * time.Second)
	require.Equal(t, 1.0, read())
	require.Equal(t, 1, calls)
	now = now.Add(time.Second)
	require.Equal(t, 2.0, read())
	fail = true
	now = now.Add(10 * time.Second)
	require.Equal(t, 2.0, read())
	require.Equal(t, 2.0, read())
	require.Equal(t, 3, calls)
}
