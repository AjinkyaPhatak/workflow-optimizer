package observability

import (
	"sort"
	"sync"
	"time"
)

// Metric names. Counters count; durations record count, sum and max.
const (
	MetricExecutionsStarted    = "executions_started_total"
	MetricExecutionsCompleted  = "executions_completed_total"
	MetricExecutionsFailed     = "executions_failed_total"
	MetricExecutionsCancelled  = "executions_cancelled_total"
	MetricNodeFailures         = "node_failures_total"
	MetricProviderErrors       = "provider_errors_total"
	MetricRetries              = "retries_scheduled_total"
	MetricEventPersistFailures = "execution_event_persist_failures_total"

	MetricExecutionDuration = "execution_duration"
	MetricNodeDuration      = "node_duration"
	MetricQueueLatency      = "queue_latency"
)

// Metrics is a minimal in-process metrics registry: instrumentation points
// for counters and durations, readable as a snapshot. It deliberately has no
// exporter; one can read Snapshot later (Prometheus, OTLP) without touching
// the instrumentation. A nil *Metrics records nothing.
type Metrics struct {
	mu        sync.Mutex
	counters  map[string]int64
	durations map[string]*DurationStats
}

// DurationStats summarizes recorded durations.
type DurationStats struct {
	Count int64         `json:"count"`
	Sum   time.Duration `json:"sum"`
	Max   time.Duration `json:"max"`
}

// NewMetrics returns an empty registry.
func NewMetrics() *Metrics {
	return &Metrics{counters: map[string]int64{}, durations: map[string]*DurationStats{}}
}

func (m *Metrics) inc(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counters[name]++
	m.mu.Unlock()
}

func (m *Metrics) observe(name string, d time.Duration) {
	if m == nil || d < 0 {
		return
	}
	m.mu.Lock()
	s := m.durations[name]
	if s == nil {
		s = &DurationStats{}
		m.durations[name] = s
	}
	s.Count++
	s.Sum += d
	if d > s.Max {
		s.Max = d
	}
	m.mu.Unlock()
}

// Snapshot is a point-in-time copy of all metrics.
type Snapshot struct {
	Counters  map[string]int64         `json:"counters"`
	Durations map[string]DurationStats `json:"durations"`
}

// Snapshot copies the current values.
func (m *Metrics) Snapshot() Snapshot {
	s := Snapshot{Counters: map[string]int64{}, Durations: map[string]DurationStats{}}
	if m == nil {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.counters {
		s.Counters[k] = v
	}
	for k, v := range m.durations {
		s.Durations[k] = *v
	}
	return s
}

// LogAttrs flattens the snapshot for one structured log line.
func (s Snapshot) LogAttrs() []any {
	keys := make([]string, 0, len(s.Counters))
	for k := range s.Counters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := []any{}
	for _, k := range keys {
		attrs = append(attrs, k, s.Counters[k])
	}
	dkeys := make([]string, 0, len(s.Durations))
	for k := range s.Durations {
		dkeys = append(dkeys, k)
	}
	sort.Strings(dkeys)
	for _, k := range dkeys {
		d := s.Durations[k]
		attrs = append(attrs, k+"_count", d.Count, k+"_sum_ms", d.Sum.Milliseconds(), k+"_max_ms", d.Max.Milliseconds())
	}
	return attrs
}
