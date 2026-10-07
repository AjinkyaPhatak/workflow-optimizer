package execution_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/workflow"
)

// eventLog is an ExecutionObserver that records "TYPE[:node]" lines and
// snapshots the execution at each call (to prove events follow durable
// state).
type eventLog struct {
	mu     sync.Mutex
	repo   *memRepo
	lines  []string
	status []execution.ExecutionStatus // execution status when each event fired
	errs   map[string]execution.ExecutionError
}

func newEventLog(repo *memRepo) *eventLog {
	return &eventLog{repo: repo, errs: map[string]execution.ExecutionError{}}
}

func (l *eventLog) add(id uuid.UUID, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	e, _ := l.repo.Get(context.Background(), id)
	l.status = append(l.status, e.Status)
}

func (l *eventLog) String() string { return strings.Join(l.lines, " ") }

func nodeLine(t execution.EventType, id string) string { return string(t) + ":" + id }

func (l *eventLog) ExecutionStarted(_ context.Context, id uuid.UUID, attempt, _ int, _ time.Duration) {
	l.add(id, fmt.Sprintf("%s#%d", execution.EventExecutionStarted, attempt))
}
func (l *eventLog) ExecutionCompleted(_ context.Context, id uuid.UUID, _ int, _ time.Duration) {
	l.add(id, string(execution.EventExecutionCompleted))
}
func (l *eventLog) ExecutionFailed(_ context.Context, id uuid.UUID, _ int, err execution.ExecutionError, _ execution.DeadLetterReason) {
	l.errs["execution"] = err
	l.add(id, string(execution.EventExecutionFailed))
}
func (l *eventLog) ExecutionCancelled(_ context.Context, id uuid.UUID, _ int, reason string) {
	l.add(id, string(execution.EventExecutionCancelled)+"("+reason+")")
}
func (l *eventLog) NodeStarted(_ context.Context, id uuid.UUID, n execution.NodeRef) {
	l.add(id, nodeLine(execution.EventNodeStarted, n.ID))
}
func (l *eventLog) NodeCompleted(_ context.Context, id uuid.UUID, n execution.NodeRef, _ time.Duration) {
	l.add(id, nodeLine(execution.EventNodeCompleted, n.ID))
}
func (l *eventLog) NodeFailed(_ context.Context, id uuid.UUID, n execution.NodeRef, err execution.ExecutionError, _ time.Duration) {
	l.errs[n.ID] = err
	l.add(id, nodeLine(execution.EventNodeFailed, n.ID))
}
func (l *eventLog) NodeSkipped(_ context.Context, id uuid.UUID, nodeID, _ string) {
	l.add(id, nodeLine(execution.EventNodeSkipped, nodeID))
}
func (l *eventLog) RetryScheduled(_ context.Context, id uuid.UUID, nodeID *string, _ int, _ time.Duration, _ execution.ExecutionError) {
	l.add(id, retryLine(execution.EventRetryScheduled, nodeID))
}
func (l *eventLog) RetryStarted(_ context.Context, id uuid.UUID, nodeID *string, _ int) {
	l.add(id, retryLine(execution.EventRetryStarted, nodeID))
}

func retryLine(t execution.EventType, nodeID *string) string {
	if nodeID == nil {
		return string(t)
	}
	return nodeLine(t, *nodeID)
}

func observedRunner(t *testing.T, def workflow.Definition, mutate func(*execution.RunnerConfig)) (*runnerEnv, *eventLog) {
	t.Helper()
	e := newEnv(t)
	repo := newMemRepo()
	log := newEventLog(repo)
	cfg := execution.RunnerConfig{
		Executions:     repo,
		NodeExecutions: nodeRepo{repo},
		Definitions:    loaderFunc(func(context.Context, uuid.UUID) (workflow.Definition, error) { return def, nil }),
		Validator:      workflow.NewValidator(e.reg),
		Graph:          e.exec,
		Observer:       log,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	r, err := execution.NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &runnerEnv{env: e, repo: repo, runner: r}, log
}

func chainInAOut() workflow.Definition {
	return graph(nodes(entry("in"), probe("A"), exit("out")), edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
}

func TestEventsForSuccessfulExecution(t *testing.T) {
	r, log := observedRunner(t, chainInAOut(), nil)
	id := r.create(t, map[string]any{"q": "x"})
	if _, err := r.runner.Run(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	want := "EXECUTION_STARTED#1 NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:A NODE_COMPLETED:A NODE_STARTED:out NODE_COMPLETED:out EXECUTION_COMPLETED"
	if log.String() != want {
		t.Fatalf("events:\n got %s\nwant %s", log, want)
	}
	// EXECUTION_COMPLETED is reported only once the execution is COMPLETED.
	if last := log.status[len(log.status)-1]; last != execution.StatusCompleted {
		t.Fatalf("completion reported while %s", last)
	}
	for i, s := range log.status[:len(log.status)-1] {
		if s != execution.StatusRunning {
			t.Fatalf("event %d (%s) fired while %s", i, log.lines[i], s)
		}
	}
}

func TestEventsForFailedExecution(t *testing.T) {
	r, log := observedRunner(t, chainInAOut(), nil)
	r.rec.hooks = map[string]func(context.Context) error{"A": func(context.Context) error { return unavailable() }}
	id := r.create(t, nil)
	if _, err := r.runner.Run(context.Background(), id); err == nil {
		t.Fatal("expected failure")
	}
	want := "EXECUTION_STARTED#1 NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:A NODE_FAILED:A EXECUTION_FAILED"
	if log.String() != want {
		t.Fatalf("events:\n got %s\nwant %s", log, want)
	}
	if e := log.errs["A"]; e.Code == "" || !e.Retryable || e.NodeID == nil || *e.NodeID != "A" {
		t.Fatalf("node error = %+v", e)
	}
	if last := log.status[len(log.status)-1]; last != execution.StatusFailed {
		t.Fatalf("failure reported while %s", last)
	}
}

func TestEventsForInPlaceNodeRetry(t *testing.T) {
	r, log := observedRunner(t, chainInAOut(), func(c *execution.RunnerConfig) { c.NodeRetry = nodeRetry(3) })
	calls := 0
	r.rec.hooks = map[string]func(context.Context) error{"A": func(context.Context) error {
		calls++
		if calls == 1 {
			return unavailable()
		}
		return nil
	}}
	id := r.create(t, nil)
	if _, err := r.runner.Run(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	want := "EXECUTION_STARTED#1 NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:A NODE_FAILED:A RETRY_SCHEDULED:A RETRY_STARTED:A NODE_STARTED:A NODE_COMPLETED:A NODE_STARTED:out NODE_COMPLETED:out EXECUTION_COMPLETED"
	if log.String() != want {
		t.Fatalf("events:\n got %s\nwant %s", log, want)
	}
}

func TestEventsForRetryExhaustion(t *testing.T) {
	r, log := observedRunner(t, chainInAOut(), func(c *execution.RunnerConfig) { c.NodeRetry = nodeRetry(2) })
	r.rec.hooks = map[string]func(context.Context) error{"A": func(context.Context) error { return unavailable() }}
	id := r.create(t, nil)
	if _, err := r.runner.Run(context.Background(), id); err == nil {
		t.Fatal("expected failure")
	}
	want := "EXECUTION_STARTED#1 NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:A NODE_FAILED:A RETRY_SCHEDULED:A RETRY_STARTED:A NODE_STARTED:A NODE_FAILED:A EXECUTION_FAILED"
	if log.String() != want {
		t.Fatalf("events:\n got %s\nwant %s", log, want)
	}
}

func TestEventsForCancellation(t *testing.T) {
	r, log := observedRunner(t, chainInAOut(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	r.rec.hooks = map[string]func(context.Context) error{"A": func(context.Context) error { cancel(); return context.Canceled }}
	id := r.create(t, nil)
	if _, err := r.runner.Run(ctx, id); err == nil {
		t.Fatal("expected cancellation")
	}
	s := log.String()
	if !strings.HasPrefix(s, "EXECUTION_STARTED#1 NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:A") ||
		!strings.HasSuffix(s, "EXECUTION_CANCELLED(caller_cancelled)") || strings.Contains(s, "EXECUTION_COMPLETED") || strings.Contains(s, "NODE_COMPLETED:A") {
		t.Fatalf("events: %s", s)
	}
	if last := log.status[len(log.status)-1]; last != execution.StatusCancelled {
		t.Fatalf("cancellation reported while %s", last)
	}
}

func TestEventsForExecutionTimeout(t *testing.T) {
	r, log := observedRunner(t, chainInAOut(), nil)
	r.rec.hooks = map[string]func(context.Context) error{"A": func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	id := r.create(t, nil)
	if _, err := r.runner.Run(ctx, id); err == nil {
		t.Fatal("expected timeout")
	}
	want := "EXECUTION_STARTED#1 NODE_STARTED:in NODE_COMPLETED:in NODE_STARTED:A NODE_FAILED:A EXECUTION_FAILED"
	if log.String() != want {
		t.Fatalf("events:\n got %s\nwant %s", log, want)
	}
	if log.errs["execution"].Code != execution.CodeTimeout {
		t.Fatalf("execution error = %+v", log.errs["execution"])
	}
}

// The observer has no way to fail a run: its methods return nothing, and the
// run's outcome is identical with or without one.
func TestObserverDoesNotChangeTheOutcome(t *testing.T) {
	for _, observed := range []bool{false, true} {
		r, _ := observedRunner(t, chainInAOut(), func(c *execution.RunnerConfig) {
			if !observed {
				c.Observer = nil
			}
		})
		id := r.create(t, map[string]any{"q": "x"})
		if _, err := r.runner.Run(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		e, _ := r.repo.Get(context.Background(), id)
		h, _ := r.repo.History(context.Background(), id)
		if e.Status != execution.StatusCompleted || len(h) != 3 || len(r.nodeRecords(t, id)) != 3 {
			t.Fatalf("observed=%v: %+v history=%d", observed, e, len(h))
		}
	}
}
