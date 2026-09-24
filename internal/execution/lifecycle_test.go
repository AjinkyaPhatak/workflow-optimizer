package execution_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

func newService(t *testing.T) (*execution.LifecycleService, *memRepo) {
	t.Helper()
	repo := newMemRepo()
	return execution.NewLifecycleService(repo), repo
}

func mustCreate(t *testing.T, svc *execution.LifecycleService) execution.Execution {
	t.Helper()
	e, err := svc.Create(context.Background(), uuid.New(), uuid.New(), map[string]any{"query": "q"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return e
}

func statusOf(t *testing.T, repo *memRepo, id uuid.UUID) execution.ExecutionStatus {
	t.Helper()
	e, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e.Status
}

func TestCreateLeavesExecutionPending(t *testing.T) {
	svc, repo := newService(t)
	e := mustCreate(t, svc)
	if e.Status != execution.StatusPending || e.StartedAt != nil || e.FinishedAt != nil || e.Output != nil || e.Error != nil {
		t.Fatalf("created execution = %+v", e)
	}
	if e.CreatedAt.IsZero() || !e.UpdatedAt.Equal(e.CreatedAt) || e.Input["query"] != "q" {
		t.Fatalf("created execution = %+v", e)
	}
	h, _ := repo.History(context.Background(), e.ID)
	if len(h) != 1 || h[0].From != nil || h[0].To != execution.StatusPending {
		t.Fatalf("history = %+v", h)
	}
	if _, err := svc.Create(context.Background(), uuid.Nil, uuid.New(), nil); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("nil workflow id: %v", err)
	}
}

func TestStartClaimsPendingExactlyOnce(t *testing.T) {
	svc, repo := newService(t)
	e := mustCreate(t, svc)
	if err := svc.Start(context.Background(), e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	err := svc.Start(context.Background(), e.ID)
	var te *execution.TransitionError
	if !errors.Is(err, execution.ErrExecutionNotClaimable) || !errors.Is(err, execution.ErrInvalidTransition) || !errors.As(err, &te) || te.From != execution.StatusRunning {
		t.Fatalf("second start err = %v", err)
	}
	if statusOf(t, repo, e.ID) != execution.StatusRunning {
		t.Fatal("status must remain RUNNING")
	}
	if err := svc.Start(context.Background(), uuid.New()); !errors.Is(err, execution.ErrExecutionNotFound) {
		t.Fatalf("unknown execution: %v", err)
	}
}

func TestValidTransitionsThroughService(t *testing.T) {
	ctx := context.Background()
	nodeID := "B"
	terminal := map[execution.ExecutionStatus]func(*execution.LifecycleService, uuid.UUID) error{
		execution.StatusCompleted: func(s *execution.LifecycleService, id uuid.UUID) error {
			return s.Complete(ctx, id, map[string]any{"out": map[string]any{"result": 1.0}})
		},
		execution.StatusFailed: func(s *execution.LifecycleService, id uuid.UUID) error {
			return s.Fail(ctx, id, execution.ExecutionError{Code: "X", Message: "m", NodeID: &nodeID, Retryable: true})
		},
		execution.StatusCancelled: func(s *execution.LifecycleService, id uuid.UUID) error { return s.Cancel(ctx, id) },
	}
	for target, apply := range terminal {
		t.Run(string(target), func(t *testing.T) {
			svc, repo := newService(t)
			e := mustCreate(t, svc)
			if err := svc.Start(ctx, e.ID); err != nil {
				t.Fatal(err)
			}
			running, _ := repo.Get(ctx, e.ID)
			if running.ClaimToken == nil || *running.ClaimToken == uuid.Nil {
				t.Fatal("claim must record a claim token")
			}
			if running.StartedAt == nil || running.FinishedAt != nil || !running.UpdatedAt.After(e.UpdatedAt) {
				t.Fatalf("RUNNING timestamps = %+v", running)
			}
			if err := apply(svc, e.ID); err != nil {
				t.Fatalf("%s: %v", target, err)
			}
			done, _ := repo.Get(ctx, e.ID)
			if done.Status != target || done.FinishedAt == nil || !done.StartedAt.Equal(*running.StartedAt) ||
				done.FinishedAt.Before(*done.StartedAt) || !done.UpdatedAt.After(running.UpdatedAt) {
				t.Fatalf("terminal timestamps = %+v", done)
			}
			switch target {
			case execution.StatusCompleted:
				if done.Output == nil || done.Error != nil {
					t.Fatalf("completed = %+v", done)
				}
			case execution.StatusFailed:
				if done.Error == nil || *done.Error.NodeID != "B" || !done.Error.Retryable || done.Output != nil {
					t.Fatalf("failed = %+v", done)
				}
			case execution.StatusCancelled:
				if done.Output != nil || done.Error != nil {
					t.Fatalf("cancelled = %+v", done)
				}
			}
			h, _ := repo.History(ctx, e.ID)
			if len(h) != 3 || *h[1].From != execution.StatusPending || h[1].To != execution.StatusRunning ||
				*h[2].From != execution.StatusRunning || h[2].To != target {
				t.Fatalf("history = %+v", h)
			}
		})
	}
}

// op applies one lifecycle operation by its target status.
func op(ctx context.Context, svc *execution.LifecycleService, id uuid.UUID, target execution.ExecutionStatus) error {
	switch target {
	case execution.StatusRunning:
		return svc.Start(ctx, id)
	case execution.StatusCompleted:
		return svc.Complete(ctx, id, nil)
	case execution.StatusFailed:
		return svc.Fail(ctx, id, execution.ExecutionError{Code: "X", Message: "m"})
	default:
		return svc.Cancel(ctx, id)
	}
}

func TestInvalidTransitionsAreRejected(t *testing.T) {
	ctx := context.Background()
	cases := []struct{ from, to execution.ExecutionStatus }{
		{execution.StatusPending, execution.StatusCompleted},
		{execution.StatusPending, execution.StatusFailed},
		{execution.StatusPending, execution.StatusCancelled},
		{execution.StatusCompleted, execution.StatusRunning},
		{execution.StatusCompleted, execution.StatusFailed},
		{execution.StatusFailed, execution.StatusRunning},
		{execution.StatusFailed, execution.StatusCompleted},
		{execution.StatusCancelled, execution.StatusRunning},
		{execution.StatusCancelled, execution.StatusCompleted},
		// terminal immutability beyond the spec's minimum list
		{execution.StatusCompleted, execution.StatusCancelled},
		{execution.StatusFailed, execution.StatusCancelled},
		{execution.StatusCancelled, execution.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			svc, repo := newService(t)
			e := mustCreate(t, svc)
			if tc.from != execution.StatusPending {
				if err := svc.Start(ctx, e.ID); err != nil {
					t.Fatal(err)
				}
				if err := op(ctx, svc, e.ID, tc.from); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := repo.Get(ctx, e.ID)
			histBefore, _ := repo.History(ctx, e.ID)
			err := op(ctx, svc, e.ID, tc.to)
			var te *execution.TransitionError
			if !errors.Is(err, execution.ErrInvalidTransition) || !errors.As(err, &te) || te.From != tc.from || te.To != tc.to {
				t.Fatalf("err = %v", err)
			}
			after, _ := repo.Get(ctx, e.ID)
			histAfter, _ := repo.History(ctx, e.ID)
			if after.Status != tc.from || !after.UpdatedAt.Equal(before.UpdatedAt) || len(histAfter) != len(histBefore) {
				t.Fatalf("rejected transition mutated state: %+v", after)
			}
		})
	}
}

func TestStateMachineRejectsUnreachableTargetsAndBadPayloads(t *testing.T) {
	ctx := context.Background()
	svc, repo := newService(t)
	machine := execution.NewExecutionStateMachine(repo)
	e := mustCreate(t, svc)
	for _, target := range []execution.ExecutionStatus{execution.StatusPending, "BOGUS"} {
		if err := machine.Transition(ctx, e.ID, target, execution.TransitionUpdate{}); !errors.Is(err, execution.ErrInvalidTransition) {
			t.Fatalf("target %q: %v", target, err)
		}
	}
	if err := machine.Transition(ctx, e.ID, execution.StatusRunning, execution.TransitionUpdate{}); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("claim without token: %v", err)
	}
	_ = svc.Start(ctx, e.ID)
	bad := []struct {
		target execution.ExecutionStatus
		update execution.TransitionUpdate
	}{
		{execution.StatusFailed, execution.TransitionUpdate{}},                                            // missing error
		{execution.StatusFailed, execution.TransitionUpdate{Error: &execution.ExecutionError{Code: "X"}}}, // no message
		{execution.StatusFailed, execution.TransitionUpdate{Output: map[string]any{"x": 1}, Error: &execution.ExecutionError{Code: "X", Message: "m"}}},
		{execution.StatusCancelled, execution.TransitionUpdate{Output: map[string]any{"x": 1}}}, // output only on COMPLETED
		{execution.StatusCompleted, execution.TransitionUpdate{Error: &execution.ExecutionError{Code: "X", Message: "m"}}},
		{execution.StatusCompleted, execution.TransitionUpdate{ClaimToken: uuid.New()}}, // token only on claim
		{execution.StatusCompleted, execution.TransitionUpdate{InterruptedNodeError: &execution.ExecutionError{Code: "X", Message: "m"}}},
	}
	for i, b := range bad {
		if err := machine.Transition(ctx, e.ID, b.target, b.update); !errors.Is(err, execution.ErrInvalidExecution) {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	if statusOf(t, repo, e.ID) != execution.StatusRunning {
		t.Fatal("rejected payloads must not change status")
	}
}

func TestNodeExecutionLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, repo := newService(t)
	nodes := nodeRepo{repo}
	machine := execution.NewNodeExecutionStateMachine(nodes)
	e := mustCreate(t, svc)
	pendingParent := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "early", NodeType: "t", Status: execution.NodeStatusPending}
	if err := nodes.Create(ctx, pendingParent); !errors.Is(err, execution.ErrExecutionNotRunning) {
		t.Fatalf("node under PENDING execution: %v", err)
	}
	if err := svc.Start(ctx, e.ID); err != nil {
		t.Fatal(err)
	}

	newNode := func(id string) uuid.UUID {
		rec := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: id, NodeType: "test", Status: execution.NodeStatusPending}
		if err := nodes.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	// Ownership: a node record cannot exist without its execution.
	orphan := execution.NodeExecution{ID: uuid.New(), ExecutionID: uuid.New(), NodeID: "x", NodeType: "t", Status: execution.NodeStatusPending}
	if err := nodes.Create(ctx, orphan); !errors.Is(err, execution.ErrExecutionNotFound) {
		t.Fatalf("orphan: %v", err)
	}

	ok := newNode("A")
	if err := machine.Transition(ctx, ok, execution.NodeStatusCompleted, execution.NodeTransitionUpdate{}); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("PENDING->COMPLETED: %v", err)
	}
	if err := machine.Transition(ctx, ok, execution.NodeStatusRunning, execution.NodeTransitionUpdate{Input: map[string]any{"ports": map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	if err := machine.Transition(ctx, ok, execution.NodeStatusCompleted, execution.NodeTransitionUpdate{Output: map[string]any{"out": "v"}}); err != nil {
		t.Fatal(err)
	}
	rec, _ := nodes.Get(ctx, ok)
	if rec.ExecutionID != e.ID || rec.Status != execution.NodeStatusCompleted || rec.StartedAt == nil || rec.FinishedAt == nil || rec.Output["out"] != "v" || rec.Input == nil {
		t.Fatalf("node = %+v", rec)
	}
	if err := machine.Transition(ctx, ok, execution.NodeStatusRunning, execution.NodeTransitionUpdate{}); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("COMPLETED->RUNNING: %v", err)
	}

	failed := newNode("B")
	_ = machine.Transition(ctx, failed, execution.NodeStatusRunning, execution.NodeTransitionUpdate{})
	if err := machine.Transition(ctx, failed, execution.NodeStatusFailed, execution.NodeTransitionUpdate{}); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("FAILED without error: %v", err)
	}
	nid := "B"
	if err := machine.Transition(ctx, failed, execution.NodeStatusFailed, execution.NodeTransitionUpdate{Error: &execution.ExecutionError{Code: "X", Message: "m", NodeID: &nid}}); err != nil {
		t.Fatal(err)
	}

	skipped := newNode("C")
	_ = machine.Transition(ctx, skipped, execution.NodeStatusRunning, execution.NodeTransitionUpdate{})
	if err := machine.Transition(ctx, skipped, execution.NodeStatusSkipped, execution.NodeTransitionUpdate{}); err != nil {
		t.Fatalf("RUNNING->SKIPPED: %v", err)
	}
	list, _ := nodes.ListByExecution(ctx, e.ID)
	if len(list) != 3 {
		t.Fatalf("records = %d", len(list))
	}

	// Begin creates the record and moves it to RUNNING in one step.
	begun := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "D", NodeType: "t", Status: execution.NodeStatusPending}
	if err := machine.Begin(ctx, begun, map[string]any{"ports": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := nodes.Get(ctx, begun.ID); rec.Status != execution.NodeStatusRunning || rec.StartedAt == nil || rec.Input == nil {
		t.Fatalf("begun node = %+v", rec)
	}
	// Terminal parent: cancelling sweeps the RUNNING node and rejects every later node write.
	if err := svc.Cancel(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := nodes.Get(ctx, begun.ID); rec.Status != execution.NodeStatusFailed || rec.Error == nil || rec.Error.Code != execution.CodeCancelled || *rec.Error.NodeID != "D" {
		t.Fatalf("swept node = %+v / %+v", rec, rec.Error)
	}
	late := execution.NodeExecution{ID: uuid.New(), ExecutionID: e.ID, NodeID: "late", NodeType: "t", Status: execution.NodeStatusPending}
	if err := machine.Begin(ctx, late, nil); !errors.Is(err, execution.ErrExecutionNotRunning) {
		t.Fatalf("node begin under CANCELLED execution: %v", err)
	}
}
