// Package metrics collects and writes Prometheus text metrics.
package metrics

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type collector interface{ writeText(*strings.Builder) }

// Registry holds metric families with unique names.
type Registry struct {
	mu       sync.Mutex
	families map[string]collector
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{families: make(map[string]collector)} }

func (r *Registry) register(name string, c collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.families[name]; exists {
		panic("metrics: duplicate family: " + name)
	}
	r.families[name] = c
}

// WriteText writes metric families and label sets in deterministic order.
func (r *Registry) WriteText(w io.Writer) error {
	r.mu.Lock()
	names := make([]string, 0, len(r.families))
	for name := range r.families {
		names = append(names, name)
	}
	slices.Sort(names)
	families := make([]collector, 0, len(names))
	for _, name := range names {
		families = append(families, r.families[name])
	}
	r.mu.Unlock()
	var b strings.Builder
	for _, family := range families {
		family.writeText(&b)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

type family struct {
	name, help string
	labels     []string
}

func (f family) header(b *strings.Builder, kind string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", f.name, escapeHelp(f.help), f.name, kind)
}
func escapeHelp(s string) string { return strings.NewReplacer("\\", "\\\\", "\n", "\\n").Replace(s) }
func escapeLabel(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(s)
}
func (f family) labelSet(values []string) string {
	if len(values) != len(f.labels) {
		panic("metrics: wrong label count for " + f.name)
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = f.labels[i] + "=\"" + escapeLabel(v) + "\""
	}
	return strings.Join(parts, ",")
}
func sample(b *strings.Builder, name, labels string, value float64) {
	if labels != "" {
		name += "{" + labels + "}"
	}
	fmt.Fprintf(b, "%s %s\n", name, strconv.FormatFloat(value, 'g', -1, 64))
}
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// CounterVec groups counters by label values.
type CounterVec struct {
	mu sync.Mutex
	family
	values map[string]float64
}

// Counter records nonnegative increments for one label set.
type Counter struct {
	vec    *CounterVec
	labels string
}

// NewCounterVec registers a counter family.
func (r *Registry) NewCounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{family: family{name, help, slices.Clone(labels)}, values: make(map[string]float64)}
	r.register(name, c)
	return c
}

// WithLabelValues selects or initializes a counter.
func (c *CounterVec) WithLabelValues(values ...string) *Counter {
	labels := c.labelSet(values)
	c.mu.Lock()
	if _, ok := c.values[labels]; !ok {
		c.values[labels] = 0
	}
	c.mu.Unlock()
	return &Counter{vec: c, labels: labels}
}

// Inc increments the counter by one.
func (c *Counter) Inc() { c.Add(1) }

// Add increments the counter by a finite, nonnegative value.
func (c *Counter) Add(value float64) {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		panic("metrics: counter increment must be finite and nonnegative")
	}
	c.vec.mu.Lock()
	c.vec.values[c.labels] += value
	c.vec.mu.Unlock()
}
func (c *CounterVec) writeText(b *strings.Builder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.header(b, "counter")
	for _, k := range sortedKeys(c.values) {
		sample(b, c.name, k, c.values[k])
	}
}

// HistogramVec groups cumulative histograms by label values.
type HistogramVec struct {
	mu sync.Mutex
	family
	buckets []float64
	values  map[string]*histogramValue
}
type histogramValue struct {
	buckets []uint64
	count   uint64
	sum     float64
}

// Histogram records observations for one label set.
type Histogram struct {
	vec    *HistogramVec
	labels string
}

// NewHistogramVec registers a histogram with finite, increasing bucket bounds.
func (r *Registry) NewHistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	bounds := slices.Clone(buckets)
	for i, v := range bounds {
		if math.IsNaN(v) || math.IsInf(v, 0) || (i > 0 && v <= bounds[i-1]) {
			panic("metrics: histogram bounds must be finite and increasing")
		}
	}
	h := &HistogramVec{family: family{name, help, slices.Clone(labels)}, buckets: bounds, values: make(map[string]*histogramValue)}
	r.register(name, h)
	return h
}

// WithLabelValues selects or initializes a histogram.
func (h *HistogramVec) WithLabelValues(values ...string) *Histogram {
	labels := h.labelSet(values)
	h.mu.Lock()
	if _, ok := h.values[labels]; !ok {
		h.values[labels] = &histogramValue{buckets: make([]uint64, len(h.buckets))}
	}
	h.mu.Unlock()
	return &Histogram{vec: h, labels: labels}
}

// Observe records a value in the histogram.
func (h *Histogram) Observe(value float64) {
	h.vec.mu.Lock()
	defer h.vec.mu.Unlock()
	v := h.vec.values[h.labels]
	v.count++
	v.sum += value
	for i, bound := range h.vec.buckets {
		if value <= bound {
			v.buckets[i]++
		}
	}
}
func (h *HistogramVec) writeText(b *strings.Builder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.header(b, "histogram")
	for _, k := range sortedKeys(h.values) {
		v := h.values[k]
		prefix := k
		if prefix != "" {
			prefix += ","
		}
		for i, bound := range h.buckets {
			sample(b, h.name+"_bucket", prefix+"le=\""+strconv.FormatFloat(bound, 'g', -1, 64)+"\"", float64(v.buckets[i]))
		}
		sample(b, h.name+"_bucket", prefix+`le="+Inf"`, float64(v.count))
		sample(b, h.name+"_sum", k, v.sum)
		sample(b, h.name+"_count", k, float64(v.count))
	}
}

// GaugeFunc collects a value at scrape time.
type GaugeFunc struct {
	family
	read func() float64
}

// NewGaugeFunc registers a gauge callback.
func (r *Registry) NewGaugeFunc(name, help string, read func() float64) *GaugeFunc {
	g := &GaugeFunc{family: family{name: name, help: help}, read: read}
	r.register(name, g)
	return g
}
func (g *GaugeFunc) writeText(b *strings.Builder) {
	g.header(b, "gauge")
	sample(b, g.name, "", g.read())
}
