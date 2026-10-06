package worker_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

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
		if res.Outcome != worker.OutcomeSkipped || res.Status != s || runner.calls != 0 || res.Executed() || res.Requeue {
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
		requeue  bool
	}{
		{"completed", execution.StatusCompleted, nil, worker.OutcomeCompleted, true, false},
		{"failed", execution.StatusFailed, errors.New("node failed"), worker.OutcomeFailed, true, false},
		{"cancelled", execution.StatusCancelled, context.Canceled, worker.OutcomeCancelled, true, false},
		{"claim lost", execution.StatusRunning, notClaimable, worker.OutcomeClaimLost, false, false},
		// Only an unapplied claim (still PENDING) is handed back to the queue.
		{"claim not applied", execution.StatusPending, execution.ErrPersistenceTimeout, worker.OutcomeNotAttempted, false, true},
		// Claimed here but not finalized: never requeued.
		{"unrecorded", execution.StatusRunning, errors.New("db down"), worker.OutcomeError, false, false},
		{"status unknown", "", errors.New("db down"), worker.OutcomeError, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{err: tc.runErr}
			reader := &fakeReader{statuses: []execution.ExecutionStatus{execution.StatusPending, tc.after}}
			p, _ := worker.NewExecutionProcessor(reader, runner)
			res := p.Process(context.Background(), queue.Job{ExecutionID: uuid.New()})
			if res.Outcome != tc.want || res.Executed() != tc.executed || runner.calls != 1 || res.Status != tc.after || res.Requeue != tc.requeue {
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
	if res := p.Process(context.Background(), queue.Job{ExecutionID: uuid.New()}); res.Outcome != worker.OutcomeNotFound || runner.calls != 0 || res.Requeue {
		t.Fatalf("not found: %+v", res)
	}
	// A transient read failure happens before any claim: the job must be
	// handed back (Requeue), and nothing is claimed or run.
	boom := errors.New("connection refused")
	p, _ = worker.NewExecutionProcessor(&fakeReader{err: boom}, runner)
	if res := p.Process(context.Background(), queue.Job{ExecutionID: uuid.New()}); res.Outcome != worker.OutcomeError || !errors.Is(res.Err, boom) || runner.calls != 0 || !res.Requeue {
		t.Fatalf("read error: %+v", res)
	}
}

func TestProcessorDoesNothingWhenAlreadyCancelled(t *testing.T) {
	reader, runner := &fakeReader{statuses: []execution.ExecutionStatus{execution.StatusPending}}, &fakeRunner{}
	p, _ := worker.NewExecutionProcessor(reader, runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := p.Process(ctx, queue.Job{ExecutionID: uuid.New()})
	if res.Outcome != worker.OutcomeNotAttempted || reader.calls != 0 || runner.calls != 0 || !res.Requeue {
		t.Fatalf("result = %+v", res)
	}
}

func TestProcessorRejectsInvalidJob(t *testing.T) {
	runner := &fakeRunner{}
	p, _ := worker.NewExecutionProcessor(&fakeReader{statuses: []execution.ExecutionStatus{execution.StatusPending}}, runner)
	if res := p.Process(context.Background(), queue.Job{}); res.Outcome != worker.OutcomeError || !errors.Is(res.Err, queue.ErrInvalidJob) || runner.calls != 0 || res.Requeue {
		t.Fatalf("result = %+v", res)
	}
}

// cancellingReader simulates the worker being stopped while the execution is
// being loaded: it cancels the job context and fails the read.
type cancellingReader struct{ cancel context.CancelFunc }

func (c cancellingReader) Get(ctx context.Context, _ uuid.UUID) (execution.Execution, error) {
	c.cancel()
	return execution.Execution{}, ctx.Err()
}

func TestProcessorLoadInterruptedByShutdownIsReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{}
	p, _ := worker.NewExecutionProcessor(cancellingReader{cancel: cancel}, runner)
	res := p.Process(ctx, queue.Job{ExecutionID: uuid.New()})
	if res.Outcome != worker.OutcomeNotAttempted || !res.Requeue || runner.calls != 0 {
		t.Fatalf("result = %+v", res)
	}
}

// Phase 10: outcomes of attempts whose work is owned elsewhere afterwards are
// never re-enqueued by the worker (the scheduler or the reaper owns them).
func TestProcessorPhase10Outcomes(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name     string
		runErr   error
		after    execution.ExecutionStatus
		want     worker.Outcome
		executed bool
		dead     execution.DeadLetterReason
	}{
		{"retry scheduled", &execution.RetryScheduledError{ExecutionID: id, Attempt: 1, Delay: time.Second, Err: errors.New("503")},
			execution.StatusPending, worker.OutcomeRetryScheduled, true, ""},
		{"not due", fmt.Errorf("claim: %w", execution.ErrRetryNotDue), execution.StatusPending, worker.OutcomeNotDue, false, ""},
		{"lease lost", fmt.Errorf("%w: reaped", execution.ErrLeaseLost), execution.StatusPending, worker.OutcomeLeaseLost, false, ""},
		{"dead lettered", &execution.DeadLetteredError{ExecutionID: id, Attempt: 3, Reason: execution.DeadLetterAttemptsExhausted, Err: errors.New("503")},
			execution.StatusFailed, worker.OutcomeFailed, true, execution.DeadLetterAttemptsExhausted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{err: tc.runErr}
			reader := &fakeReader{statuses: []execution.ExecutionStatus{execution.StatusPending, tc.after}}
			p, _ := worker.NewExecutionProcessor(reader, runner)
			res := p.Process(context.Background(), queue.Job{ExecutionID: id})
			if res.Outcome != tc.want || res.Executed() != tc.executed || res.Requeue || res.DeadLetter != tc.dead ||
				(tc.dead != "" && res.DeadLetterAttempt != 3) {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}
