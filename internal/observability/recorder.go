// Package observability turns execution lifecycle facts into durable events,
// structured logs and metrics (Phase 14). It describes execution and never
// controls it: nothing here can fail, retry or alter a run.
package observability

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/integration"
)

// DefaultEventWriteTimeout bounds one event append.
const DefaultEventWriteTimeout = 2 * time.Second

// Recorder is the production execution.ExecutionObserver: each fact is
// appended to the event store, logged as one structured line and counted.
//
// Failure policy: the execution and node state are already durable when an
// observer method is called. If the event cannot be appended (database down,
// timeout), the failure is logged ("execution_event_persist_failed") and
// counted (EventPersistFailures), and the event is dropped. The execution is
// never failed, retried or delayed beyond the write timeout because of it.
// The event history can therefore have gaps; the execution and node records
// cannot.
type Recorder struct {
	events  execution.ExecutionEventRepository
	logger  *slog.Logger
	metrics *Metrics
	timeout time.Duration
}

var _ execution.ExecutionObserver = (*Recorder)(nil)

// NewRecorder returns a recorder. events may be nil (log and count only);
// metrics may be nil.
func NewRecorder(events execution.ExecutionEventRepository, logger *slog.Logger, metrics *Metrics) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{events: events, logger: logger, metrics: metrics, timeout: DefaultEventWriteTimeout}
}

func (r *Recorder) emit(ctx context.Context, level slog.Level, ev execution.ExecutionEvent, attrs ...any) {
	ev.Data = RedactMap(ev.Data)
	logAttrs := []any{"event", eventLogName(ev.Type), "execution_id", ev.ExecutionID.String()}
	if ev.NodeID != nil {
		logAttrs = append(logAttrs, "node_id", *ev.NodeID)
	}
	logAttrs = append(logAttrs, attrs...)
	r.logger.Log(ctx, level, "execution event", logAttrs...)
	if r.events == nil {
		return
	}
	// Detached from the run's cancellation (a cancelled run is still
	// described), but bounded.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.timeout)
	defer cancel()
	if _, err := r.events.Append(wctx, ev); err != nil {
		r.metrics.inc(MetricEventPersistFailures)
		r.logger.Error("execution event could not be recorded", "event", "execution_event_persist_failed",
			"execution_id", ev.ExecutionID.String(), "event_type", string(ev.Type), "error", err)
	}
}

func eventLogName(t execution.EventType) string {
	switch t {
	case execution.EventExecutionStarted:
		return "execution_started"
	case execution.EventExecutionCompleted:
		return "execution_completed"
	case execution.EventExecutionFailed:
		return "execution_failed"
	case execution.EventExecutionCancelled:
		return "execution_cancelled"
	case execution.EventNodeStarted:
		return "node_execution_started"
	case execution.EventNodeCompleted:
		return "node_execution_completed"
	case execution.EventNodeFailed:
		return "node_execution_failed"
	case execution.EventNodeSkipped:
		return "node_execution_skipped"
	case execution.EventRetryScheduled:
		return "retry_scheduled"
	case execution.EventRetryStarted:
		return "retry_started"
	}
	return string(t)
}

func event(id uuid.UUID, nodeID *string, t execution.EventType, data map[string]any) execution.ExecutionEvent {
	return execution.ExecutionEvent{ID: uuid.New(), ExecutionID: id, NodeID: nodeID, Type: t, Data: data}
}

func errorData(e execution.ExecutionError) map[string]any {
	m := map[string]any{"code": e.Code, "message": e.Message, "retryable": e.Retryable}
	if e.Source != "" {
		m["source"] = e.Source
	}
	if e.NodeID != nil {
		m["node_id"] = *e.NodeID
	}
	return m
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

func (r *Recorder) ExecutionStarted(ctx context.Context, id uuid.UUID, attempt, maxAttempts int, queued time.Duration) {
	r.metrics.inc(MetricExecutionsStarted)
	r.metrics.observe(MetricQueueLatency, queued)
	r.emit(ctx, slog.LevelInfo, event(id, nil, execution.EventExecutionStarted,
		map[string]any{"attempt": attempt, "max_attempts": maxAttempts, "queued_ms": ms(queued)}),
		"attempt", attempt)
}

func (r *Recorder) ExecutionCompleted(ctx context.Context, id uuid.UUID, attempt int, ran time.Duration) {
	r.metrics.inc(MetricExecutionsCompleted)
	r.metrics.observe(MetricExecutionDuration, ran)
	r.emit(ctx, slog.LevelInfo, event(id, nil, execution.EventExecutionCompleted,
		map[string]any{"attempt": attempt, "duration_ms": ms(ran)}), "attempt", attempt, "duration_ms", ms(ran))
}

func (r *Recorder) ExecutionFailed(ctx context.Context, id uuid.UUID, attempt int, err execution.ExecutionError, deadLetter execution.DeadLetterReason) {
	r.metrics.inc(MetricExecutionsFailed)
	data := map[string]any{"attempt": attempt, "error": errorData(err)}
	if deadLetter != "" {
		data["dead_letter"] = string(deadLetter)
	}
	r.emit(ctx, slog.LevelError, event(id, nil, execution.EventExecutionFailed, data),
		"attempt", attempt, "error_code", err.Code, "dead_letter", string(deadLetter))
}

func (r *Recorder) ExecutionCancelled(ctx context.Context, id uuid.UUID, attempt int, reason string) {
	r.metrics.inc(MetricExecutionsCancelled)
	data := map[string]any{"reason": reason}
	if attempt > 0 {
		data["attempt"] = attempt
	}
	r.emit(ctx, slog.LevelWarn, event(id, nil, execution.EventExecutionCancelled, data), "reason", reason)
}

// nodeData identifies a node in an event. Integration actions ("gmail.send")
// are also identified by integration and action (Phase C1), derived from the
// node type alone: no credential or config is ever read here.
func nodeData(n execution.NodeRef) map[string]any {
	m := map[string]any{"node_type": n.Type, "invocation": n.Invocation}
	if i, a, ok := integration.SplitNodeType(n.Type); ok {
		m["integration"], m["action"] = i, a
	}
	return m
}

func (r *Recorder) NodeStarted(ctx context.Context, id uuid.UUID, n execution.NodeRef) {
	nid := n.ID
	r.emit(ctx, slog.LevelInfo, event(id, &nid, execution.EventNodeStarted, nodeData(n)),
		"node_type", n.Type, "attempt", n.Invocation)
}

func (r *Recorder) NodeCompleted(ctx context.Context, id uuid.UUID, n execution.NodeRef, ran time.Duration) {
	r.metrics.observe(MetricNodeDuration, ran)
	nid := n.ID
	data := nodeData(n)
	data["duration_ms"] = ms(ran)
	r.emit(ctx, slog.LevelInfo, event(id, &nid, execution.EventNodeCompleted, data),
		"node_type", n.Type, "attempt", n.Invocation, "duration_ms", ms(ran))
}

func (r *Recorder) NodeFailed(ctx context.Context, id uuid.UUID, n execution.NodeRef, err execution.ExecutionError, ran time.Duration) {
	r.metrics.inc(MetricNodeFailures)
	r.metrics.observe(MetricNodeDuration, ran)
	if err.Source == execution.SourceProvider {
		r.metrics.inc(MetricProviderErrors)
	}
	nid := n.ID
	data := nodeData(n)
	data["duration_ms"] = ms(ran)
	data["error"] = errorData(err)
	r.emit(ctx, slog.LevelError, event(id, &nid, execution.EventNodeFailed, data),
		"node_type", n.Type, "attempt", n.Invocation, "error_code", err.Code, "retryable", err.Retryable)
}

func (r *Recorder) NodeSkipped(ctx context.Context, id uuid.UUID, nodeID, reason string) {
	nid := nodeID
	r.emit(ctx, slog.LevelInfo, event(id, &nid, execution.EventNodeSkipped, map[string]any{"reason": reason}), "reason", reason)
}

func (r *Recorder) RetryScheduled(ctx context.Context, id uuid.UUID, nodeID *string, attempt int, delay time.Duration, cause execution.ExecutionError) {
	r.metrics.inc(MetricRetries)
	scope := "execution"
	if nodeID != nil {
		scope = "node"
	}
	r.emit(ctx, slog.LevelWarn, event(id, nodeID, execution.EventRetryScheduled,
		map[string]any{"scope": scope, "attempt": attempt, "delay_ms": ms(delay), "error": errorData(cause)}),
		"scope", scope, "attempt", attempt, "delay_ms", ms(delay), "error_code", cause.Code)
}

func (r *Recorder) RetryStarted(ctx context.Context, id uuid.UUID, nodeID *string, attempt int) {
	scope := "execution"
	if nodeID != nil {
		scope = "node"
	}
	r.emit(ctx, slog.LevelInfo, event(id, nodeID, execution.EventRetryStarted,
		map[string]any{"scope": scope, "attempt": attempt}), "scope", scope, "attempt", attempt)
}
