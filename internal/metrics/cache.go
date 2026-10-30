package metrics

import (
	"math"
	"sync"
	"time"
)

// CachedGauge shares a refresh across concurrent scrapes and retains the last
// successful value on error. Before the first success the value is NaN.
func CachedGauge(ttl time.Duration, read func() (float64, error)) func() float64 {
	return cachedGauge(ttl, read, time.Now)
}
func cachedGauge(ttl time.Duration, read func() (float64, error), now func() time.Time) func() float64 {
	var mu sync.Mutex
	var expires time.Time
	value := math.NaN()
	return func() float64 {
		mu.Lock()
		defer mu.Unlock()
		if !now().Before(expires) {
			if next, err := read(); err == nil {
				value = next
			}
			expires = now().Add(ttl)
		}
		return value
	}
}
