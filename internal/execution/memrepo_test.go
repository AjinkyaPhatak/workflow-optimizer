package execution_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

// memRepo is a unit-test double honouring the repository contracts
// (conditional compare-and-set, timestamps, history). It exists only to test
// lifecycle logic quickly; the PostgreSQL adapter is proven separately against
// a real database in internal/infrastructure/postgres.
type memRepo struct {
	mu      sync.Mutex
	clock   time.Time
	execs   map[uuid.UUID]execution.Execution
	history map[uuid.UUID][]execution.StatusTransition
	nodes   map[uuid.UUID]execution.NodeExecution
	order   []uuid.UUID
	// failNodeWrites makes node repository writes fail (persistence outage).
	failNodeWrites bool
}

func newMemRepo() *memRepo {
	return &memRepo{
		clock:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		execs:   map[uuid.UUID]execution.Execution{},
		history: map[uuid.UUID][]execution.StatusTransition{},
		nodes:   map[uuid.UUID]execution.NodeExecution{},
	}
}

// tick advances a deterministic clock so timestamps strictly increase.
func (m *memRepo) tick() time.Time { m.clock = m.clock.Add(time.Second); return m.clock }

// jsonCopy mimics JSONB round-tripping (and catches unserializable values).
func jsonCopy[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func (m *memRepo) Create(_ context.Context, e execution.Execution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Status != execution.StatusPending || e.StartedAt != nil || e.FinishedAt != nil || e.Output != nil || e.Error != nil {
		return execution.ErrInvalidExecution
	}
	if _, dup := m.execs[e.ID]; dup {
		return execution.ErrInvalidExecution
	}
	now := m.tick()
	e.CreatedAt, e.UpdatedAt = now, now
	if e.Input != nil {
		in, err := jsonCopy(e.Input)
		if err != nil {
			return err
		}
		e.Input = in
	}
	m.execs[e.ID] = e
	m.history[e.ID] = append(m.history[e.ID], execution.StatusTransition{ID: uuid.New(), ExecutionID: e.ID, To: execution.StatusPending, CreatedAt: now, Metadata: map[string]any{}})
	return nil
}

func (m *memRepo) Get(_ context.Context, id uuid.UUID) (execution.Execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.execs[id]
	if !ok {
		return execution.Execution{}, fmt.Errorf("%w: %s", execution.ErrExecutionNotFound, id)
	}
	return e, nil
}

func (m *memRepo) Transition(_ context.Context, id uuid.UUID, from, to execution.ExecutionStatus, u execution.TransitionUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !from.CanTransitionTo(to) {
		return &execution.TransitionError{ExecutionID: id, From: from, To: to}
	}
	e, ok := m.execs[id]
	if !ok || e.Status != from {
		return execution.ErrTransitionConflict
	}
	now := m.tick()
	e.Status, e.UpdatedAt = to, now
	if to == execution.StatusRunning {
		e.StartedAt = &now
	}
	if to.IsTerminal() {
		e.FinishedAt = &now
	}
	if to == execution.StatusCompleted {
		out, err := jsonCopy(u.Output)
		if err != nil {
			return err
		}
		e.Output = out
	}
	if u.Error != nil {
		errCopy := *u.Error
		e.Error = &errCopy
	}
	m.execs[id] = e
	f := from
	m.history[id] = append(m.history[id], execution.StatusTransition{ID: uuid.New(), ExecutionID: id, From: &f, To: to, CreatedAt: now, Metadata: u.Metadata})
	return nil
}

func (m *memRepo) History(_ context.Context, id uuid.UUID) ([]execution.StatusTransition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]execution.StatusTransition(nil), m.history[id]...), nil
}

// nodeRepo exposes the node half of memRepo under the node interface.
type nodeRepo struct{ *memRepo }

func (n nodeRepo) Create(_ context.Context, rec execution.NodeExecution) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failNodeWrites {
		return fmt.Errorf("simulated node persistence outage")
	}
	if _, ok := n.execs[rec.ExecutionID]; !ok {
		return execution.ErrExecutionNotFound
	}
	if rec.Status != execution.NodeStatusPending {
		return execution.ErrInvalidExecution
	}
	now := n.tick()
	rec.CreatedAt, rec.UpdatedAt = now, now
	n.nodes[rec.ID] = rec
	n.order = append(n.order, rec.ID)
	return nil
}

func (n nodeRepo) Get(_ context.Context, id uuid.UUID) (execution.NodeExecution, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	rec, ok := n.nodes[id]
	if !ok {
		return execution.NodeExecution{}, execution.ErrNodeExecutionNotFound
	}
	return rec, nil
}

func (n nodeRepo) Transition(_ context.Context, id uuid.UUID, from, to execution.NodeExecutionStatus, u execution.NodeTransitionUpdate) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failNodeWrites {
		return fmt.Errorf("simulated node persistence outage")
	}
	if !from.CanTransitionTo(to) {
		return &execution.NodeTransitionError{NodeExecutionID: id, From: from, To: to}
	}
	rec, ok := n.nodes[id]
	if !ok || rec.Status != from {
		return execution.ErrTransitionConflict
	}
	now := n.tick()
	rec.Status, rec.UpdatedAt = to, now
	if to == execution.NodeStatusRunning {
		rec.StartedAt = &now
		in, err := jsonCopy(u.Input)
		if err != nil {
			return err
		}
		rec.Input = in
	}
	if to.IsTerminal() {
		rec.FinishedAt = &now
	}
	if to == execution.NodeStatusCompleted {
		out, err := jsonCopy(u.Output)
		if err != nil {
			return err
		}
		rec.Output = out
	}
	if u.Error != nil {
		errCopy := *u.Error
		rec.Error = &errCopy
	}
	n.nodes[id] = rec
	return nil
}

func (n nodeRepo) ListByExecution(_ context.Context, executionID uuid.UUID) ([]execution.NodeExecution, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []execution.NodeExecution
	for _, id := range n.order {
		if rec := n.nodes[id]; rec.ExecutionID == executionID {
			out = append(out, rec)
		}
	}
	return out, nil
}
