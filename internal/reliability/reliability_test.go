package reliability_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/reliability"
	"workflow-optimizer/internal/retry"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeLeases scripts Renew results.
type fakeLeases struct {
	mu     sync.Mutex
	calls  int
	result func(call int) (execution.LeaseState, error)
}

func (f *fakeLeases) Renew(context.Context, uuid.UUID, uuid.UUID, time.Duration) (execution.LeaseState, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	return f.result(n)
}

func (f *fakeLeases) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func held() (execution.LeaseState, error) { return execution.LeaseState{Held: true}, nil }

func TestHeartbeatValidation(t *testing.T) {
	if _, err := reliability.NewHeartbeat(nil, time.Second, time.Millisecond, quiet); !errors.Is(err, reliability.ErrInvalidConfig) {
		t.Fatal("nil store accepted")
	}
	if _, err := reliability.NewHeartbeat(&fakeLeases{}, time.Second, 600*time.Millisecond, quiet); !errors.Is(err, reliability.ErrInvalidConfig) {
		t.Fatal("a lease shorter than two heartbeats must be rejected")
	}
}

func TestHeartbeatRenewsWhileRunning(t *testing.T) {
	store := &fakeLeases{result: func(int) (execution.LeaseState, error) { return held() }}
	h, _ := reliability.NewHeartbeat(store, 200*time.Millisecond, 20*time.Millisecond, quiet)
	ctx, stop := h.Keep(context.Background(), uuid.New(), uuid.New())
	time.Sleep(150 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("healthy attempt stopped: %v", context.Cause(ctx))
	}
	if n := store.count(); n < 4 {
		t.Fatalf("only %d renewals in 150ms at a 20ms interval", n)
	}
	before := runtime.NumGoroutine()
	stop()
	if ctx.Err() == nil {
		t.Fatal("stop must end the kept context")
	}
	time.Sleep(50 * time.Millisecond)
	n := store.count()
	time.Sleep(60 * time.Millisecond)
	if store.count() != n {
		t.Fatal("renewals continued after stop")
	}
	if runtime.NumGoroutine() > before {
		t.Fatal("heartbeat goroutine leaked")
	}
}

func TestHeartbeatLostLeaseStopsTheAttempt(t *testing.T) {
	store := &fakeLeases{result: func(call int) (execution.LeaseState, error) {
		if call >= 2 {
			return execution.LeaseState{Held: false}, nil
		}
		return held()
	}}
	h, _ := reliability.NewHeartbeat(store, 200*time.Millisecond, 20*time.Millisecond, quiet)
	ctx, stop := h.Keep(context.Background(), uuid.New(), uuid.New())
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lost lease did not stop the attempt")
	}
	if !errors.Is(context.Cause(ctx), execution.ErrLeaseLost) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
}

func TestHeartbeatPropagatesCancellationRequest(t *testing.T) {
	store := &fakeLeases{result: func(int) (execution.LeaseState, error) {
		return execution.LeaseState{Held: true, CancelRequested: true}, nil
	}}
	h, _ := reliability.NewHeartbeat(store, 200*time.Millisecond, 20*time.Millisecond, quiet)
	ctx, stop := h.Keep(context.Background(), uuid.New(), uuid.New())
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancellation request did not reach the running attempt")
	}
	if !errors.Is(context.Cause(ctx), execution.ErrCancelRequested) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
}

// Renewals that keep failing make the worker stop before its lease can
// expire, so the reaper can never hand the attempt to a second live owner.
func TestHeartbeatSelfFencesBeforeLeaseExpiry(t *testing.T) {
	down := errors.New("connection refused")
	store := &fakeLeases{result: func(int) (execution.LeaseState, error) { return execution.LeaseState{}, down }}
	const lease = 300 * time.Millisecond
	h, _ := reliability.NewHeartbeat(store, lease, 50*time.Millisecond, quiet)
	start := time.Now()
	ctx, stop := h.Keep(context.Background(), uuid.New(), uuid.New())
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker kept running without being able to renew its lease")
	}
	if elapsed := time.Since(start); elapsed >= lease {
		t.Fatalf("stopped after %v, not before the lease (%v) could expire", elapsed, lease)
	}
	if !errors.Is(context.Cause(ctx), execution.ErrLeaseLost) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
}

// A transient renewal failure followed by success keeps the attempt alive.
func TestHeartbeatSurvivesTransientRenewalFailure(t *testing.T) {
	store := &fakeLeases{result: func(call int) (execution.LeaseState, error) {
		if call == 2 {
			return execution.LeaseState{}, errors.New("blip")
		}
		return held()
	}}
	h, _ := reliability.NewHeartbeat(store, 300*time.Millisecond, 30*time.Millisecond, quiet)
	ctx, stop := h.Keep(context.Background(), uuid.New(), uuid.New())
	defer stop()
	time.Sleep(200 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("one failed renewal stopped the attempt: %v", context.Cause(ctx))
	}
}

// fakeDispatch returns scripted batches.
type fakeDispatch struct {
	mu      sync.Mutex
	batches [][]reliability.DueExecution
}

func (f *fakeDispatch) ClaimDue(_ context.Context, limit int, _ time.Duration) ([]reliability.DueExecution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.batches) == 0 {
		return nil, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}

type refusingQueue struct {
	*queue.MemoryQueue
	refuse uuid.UUID
}

func (r refusingQueue) Enqueue(ctx context.Context, j queue.Job) error {
	if j.ExecutionID == r.refuse {
		return errors.New("redis down")
	}
	return r.MemoryQueue.Enqueue(ctx, j)
}

func TestSchedulerEnqueuesClaimedWork(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	store := &fakeDispatch{batches: [][]reliability.DueExecution{{{ExecutionID: a}, {ExecutionID: b}}, {{ExecutionID: c}}}}
	q := refusingQueue{MemoryQueue: queue.NewMemoryQueue(), refuse: b}
	s, err := reliability.NewScheduler(store, q, time.Second, time.Minute, quiet)
	if err != nil {
		t.Fatal(err)
	}
	s.Batch = 2
	n, err := s.RunOnce(context.Background())
	// b's enqueue failed: it stays claimed-but-undelivered and is re-dispatched
	// by the database after the re-dispatch interval (not by this pass).
	if err != nil || n != 2 || q.Len() != 2 {
		t.Fatalf("sent=%d len=%d err=%v", n, q.Len(), err)
	}
}

func TestReaperPlan(t *testing.T) {
	r, _ := reliability.NewReaper(&noopRecovery{}, retry.Policy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: time.Minute, BackoffMultiplier: 2}, time.Second, quiet)
	now := time.Now()
	far := now.Add(time.Hour)
	near := now.Add(100 * time.Millisecond)
	lost := reliability.RecoveryCandidate{ExecutionID: uuid.New(), Owner: "w1", Reason: reliability.RecoverLeaseExpired}
	overdue := reliability.RecoveryCandidate{ExecutionID: uuid.New(), Reason: reliability.RecoverDeadlineExceeded}
	snap := func(attempt, max int, deadline *time.Time, cancel bool) reliability.RecoverySnapshot {
		return reliability.RecoverySnapshot{AttemptState: execution.AttemptState{Attempt: attempt, MaxAttempts: max, DeadlineAt: deadline, Now: now, CancelRequested: cancel}}
	}
	unsafe := func(s reliability.RecoverySnapshot) reliability.RecoverySnapshot {
		node := "post"
		s.UnsafeNodeID = &node
		return s
	}
	cases := []struct {
		name string
		c    reliability.RecoveryCandidate
		s    reliability.RecoverySnapshot
		to   execution.ExecutionStatus
		dead execution.DeadLetterReason
		code string
	}{
		{"lost worker, attempts left -> retry", lost, snap(1, 3, &far, false), execution.StatusPending, "", execution.CodeWorkerLost},
		{"lost worker, exhausted -> dead letter", lost, snap(3, 3, &far, false), execution.StatusFailed, execution.DeadLetterAttemptsExhausted, execution.CodeWorkerLost},
		{"lost worker, no time left -> dead letter", lost, snap(1, 3, &near, false), execution.StatusFailed, execution.DeadLetterDeadlineExceeded, execution.CodeWorkerLost},
		{"cancel beats recovery retry", lost, snap(1, 3, &far, true), execution.StatusCancelled, "", ""},
		{"overdue -> timeout", overdue, snap(1, 3, &near, false), execution.StatusFailed, "", execution.CodeTimeout},
		{"overdue but cancelled", overdue, snap(1, 3, &near, true), execution.StatusCancelled, "", ""},
		{"lost inside unsafe node -> retry_unsafe", lost, unsafe(snap(1, 3, &far, false)), execution.StatusFailed, execution.DeadLetterRetryUnsafe, execution.CodeWorkerLost},
		{"cancel beats unsafe", lost, unsafe(snap(1, 3, &far, true)), execution.StatusCancelled, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := r.Plan(tc.c)(tc.s)
			if p.To != tc.to || p.DeadLetter != tc.dead || p.Error.Code != tc.code {
				t.Fatalf("plan = %+v", p)
			}
			if p.DeadLetter == execution.DeadLetterRetryUnsafe &&
				(p.Error.Retryable || p.Error.NodeID == nil || *p.Error.NodeID != "post") {
				t.Fatalf("retry_unsafe plan must name the node and not be retryable: %+v", p.Error)
			}
			if p.To == execution.StatusPending && (p.RetryDelay <= 0 || !p.Error.Retryable || p.Error.Source != execution.SourceWorker) {
				t.Fatalf("retry plan = %+v", p)
			}
		})
	}
}

type noopRecovery struct{}

func (noopRecovery) Candidates(context.Context, int) ([]reliability.RecoveryCandidate, error) {
	return nil, nil
}
func (noopRecovery) Recover(context.Context, reliability.RecoveryCandidate, func(reliability.RecoverySnapshot) reliability.RecoveryPlan) (reliability.RecoveryResult, error) {
	return reliability.RecoveryResult{}, nil
}

type listRecorder struct {
	mu    sync.Mutex
	items [][]byte
}

func (l *listRecorder) Push(_ context.Context, p []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, p)
	return nil
}

func TestDeadLetterNotice(t *testing.T) {
	l := &listRecorder{}
	id := uuid.New()
	reliability.DeadLetterNotifier(l, quiet)(context.Background(), reliability.DeadLetterNotice{ExecutionID: id, Attempt: 3, Reason: execution.DeadLetterAttemptsExhausted, Code: "UNAVAILABLE"})
	want := `{"v":1,"execution_id":"` + id.String() + `","attempt":3,"reason":"attempts_exhausted","code":"UNAVAILABLE"}`
	if len(l.items) != 1 || string(l.items[0]) != want {
		t.Fatalf("notice = %q", l.items)
	}
	// A dead-letter notice is never a job: it does not decode as one.
	if _, err := queue.DecodeJob(l.items[0]); err == nil {
		t.Fatal("a dead-letter notice must not be consumable as a job")
	}
}
