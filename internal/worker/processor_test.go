package worker_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/worker"
)

// These tests cover the processor's decision logic with fakes. The real
// atomic claim is proven against PostgreSQL in tests/phase9.

type fakeReader struct {
	mu       sync.Mutex
	statuses []execution.ExecutionStatus // successive Get results
	err      error
	calls    int
}

func (f *fakeReader) Get(_ context.Context, id uuid.UUID) (execution.Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return execution.Execution{}, f.err
	}
	s := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return execution.Execution{ID: id, Status: s}, nil
}

type fakeRunner struct {
	calls int
	err   error
}

func (f *fakeRunner) Run(context.Context, uuid.UUID) (execution.ExecutionResult, error) {
	f.calls++
	return execution.ExecutionResult{}, f.err
}

func TestProcessorSkipsNonPendingWithoutClaiming(t *testing.T) {
	for _, s := range []execution.ExecutionStatus{execution.StatusRunning, execution.StatusCompleted, execution.StatusFailed, execution.StatusCancelled} {
		runner := &fakeRunner{}
		p, _ := worker.NewExecutionProcessor(&fakeReader{statuses: []execution.ExecutionStatus{s}}, runner)
		res := p.Process(context.Background(), queue.Job{ExecutionID: uuid.New()})
		if res.Outcome != worker.OutcomeSkipped || res.Status != s || runner.calls != 0 || res.Executed() {
			t.Fatalf("%s: result=%+v runner calls=%d", s, res, runner.calls)
		}
	}
}

func TestProcessorClassifiesRuns(t *testing.T) {
	notClaimable := &execution.TransitionError{From: execution.StatusRunning, To: execution.StatusRunning}
	cases := []struct {
		name     string
		after    execution.ExecutionStatus
		runErr   error
		want     worker.Outcome
		executed bool
	}{
		{"completed", execution.StatusCompleted, nil, worker.OutcomeCompleted, true},
		{"failed", execution.StatusFailed, errors.New("node failed"), worker.OutcomeFailed, true},
		{"cancelled", execution.StatusCancelled, context.Canceled, worker.OutcomeCancelled, true},
		{"claim lost", execution.StatusRunning, notClaimable, worker.OutcomeClaimLost, false},
		{"claim not applied", execution.StatusPending, execution.ErrPersistenceTimeout, worker.OutcomeNotAttempted, false},
		{"unrecorded", execution.StatusRunning, errors.New("db down"), worker.OutcomeError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{err: tc.runErr}
			reader := &fakeReader{statuses: []execution.ExecutionStatus{execution.StatusPending, tc.after}}
			p, _ := worker.NewExecutionProcessor(reader, runner)
			res := p.Process(context.Background(), queue.Job{ExecutionID: uuid.New()})
			if res.Outcome != tc.want || res.Executed() != tc.executed || runner.calls != 1 || res.Status != tc.after {
				t.Fatalf("result = %+v (runner calls %d)", res, runner.calls)
			}
			if tc.runErr != nil && !errors.Is(res.Err, tc.runErr) {
				t.Fatalf("error not preserved: %v", res.Err)
			}
		})
	}
}

func TestProcessorNotFoundAndReadErrors(t *testing.T) {
	runner := &fakeRunner{}
	p, _ := worker.NewExecutionProcessor(&fakeReader{err: execution.ErrExecutionNotFound}, runner)
	if res := p.Process(context.Background(), queue.Job{ExecutionID: uuid.New()}); res.Outcome != worker.OutcomeNotFound || runner.calls != 0 {
		t.Fatalf("not found: %+v", res)
	}
	boom := errors.New("connection refused")
	p, _ = worker.NewExecutionProcessor(&fakeReader{err: boom}, runner)
	if res := p.Process(context.Background(), queue.Job{ExecutionID: uuid.New()}); res.Outcome != worker.OutcomeError || !errors.Is(res.Err, boom) || runner.calls != 0 {
		t.Fatalf("read error: %+v", res)
	}
}

func TestProcessorDoesNothingWhenAlreadyCancelled(t *testing.T) {
	reader, runner := &fakeReader{statuses: []execution.ExecutionStatus{execution.StatusPending}}, &fakeRunner{}
	p, _ := worker.NewExecutionProcessor(reader, runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := p.Process(ctx, queue.Job{ExecutionID: uuid.New()})
	if res.Outcome != worker.OutcomeNotAttempted || reader.calls != 0 || runner.calls != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestProcessorRejectsInvalidJob(t *testing.T) {
	runner := &fakeRunner{}
	p, _ := worker.NewExecutionProcessor(&fakeReader{statuses: []execution.ExecutionStatus{execution.StatusPending}}, runner)
	if res := p.Process(context.Background(), queue.Job{}); res.Outcome != worker.OutcomeError || !errors.Is(res.Err, queue.ErrInvalidJob) || runner.calls != 0 {
		t.Fatalf("result = %+v", res)
	}
}
