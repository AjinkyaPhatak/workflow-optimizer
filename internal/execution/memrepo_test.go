package execution_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

// memRepo is a unit-test double for lifecycle *logic*. It mirrors the
// repository contract and the database rules that shape control flow (context
// honouring, compare-and-set, claim token, parent-must-be-RUNNING, sweeping
// RUNNING nodes on FAILED/CANCELLED). It proves nothing about PostgreSQL:
// atomicity, locking, triggers, timeouts and ambiguity resolution are tested
// against a real database in internal/infrastructure/postgres.
type memRepo struct {
	mu      sync.Mutex
	clock   time.Time
	execs   map[uuid.UUID]execution.Execution
	history map[uuid.UUID][]execution.StatusTransition
	nodes   map[uuid.UUID]execution.NodeExecution
	order   []uuid.UUID
	// failNodeWrites makes every node write fail; failNodeFinish only the
	// COMPLETED/FAILED node transitions (a node ran but its result could not
	// be recorded).
	failNodeWrites bool
	failNodeFinish bool
	// writes records the context state of every write at call time, to prove
	// lifecycle writes are detached from caller cancellation yet bounded.
	writes []writeCtx
}

type writeCtx struct {
	err         error
	hasDeadline bool
	remaining   time.Duration
}

func newMemRepo() *memRepo {
	return &memRepo{
		clock:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		execs:   map[uuid.UUID]execution.Execution{},
		history: map[uuid.UUID][]execution.StatusTransition{},
		nodes:   map[uuid.UUID]execution.NodeExecution{},
	}
}

// begin honours ctx exactly like a database call would, then locks.
func (m *memRepo) begin(ctx context.Context, write bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if write {
		deadline, ok := ctx.Deadline()
		m.writes = append(m.writes, writeCtx{err: ctx.Err(), hasDeadline: ok, remaining: time.Until(deadline)})
	}
	return nil
}

// tick advances a deterministic clock so timestamps strictly increase.
func (m *memRepo) tick() time.Time { m.clock = m.clock.Add(time.Second); return m.clock }

// jsonCopy mimics JSONB round-tripping (numbers keep precision, like the
// PostgreSQL adapter's UseNumber decoding).
func jsonCopy(v map[string]any) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	err = dec.Decode(&out)
	return out, err
}

func (m *memRepo) Create(ctx context.Context, e execution.Execution) (execution.Execution, error) {
	if err := m.begin(ctx, true); err != nil {
		return execution.Execution{}, err
	}
	defer m.mu.Unlock()
	if e.Status != execution.StatusPending || e.StartedAt != nil || e.FinishedAt != nil || e.Output != nil || e.Error != nil || e.ClaimToken != nil {
		return execution.Execution{}, execution.ErrInvalidExecution
	}
	if _, dup := m.execs[e.ID]; dup {
		return execution.Execution{}, execution.ErrInvalidExecution
	}
	now := m.tick()
	e.CreatedAt, e.UpdatedAt = now, now
	in, err := jsonCopy(e.Input)
	if err != nil {
		return execution.Execution{}, err
	}
	e.Input = in
	m.execs[e.ID] = e
	m.history[e.ID] = append(m.history[e.ID], execution.StatusTransition{ID: uuid.New(), ExecutionID: e.ID, To: execution.StatusPending, CreatedAt: now, Metadata: map[string]any{}})
	return e, nil
}

func (m *memRepo) Get(ctx context.Context, id uuid.UUID) (execution.Execution, error) {
	if err := m.begin(ctx, false); err != nil {
		return execution.Execution{}, err
	}
	defer m.mu.Unlock()
	e, ok := m.execs[id]
	if !ok {
		return execution.Execution{}, fmt.Errorf("%w: %s", execution.ErrExecutionNotFound, id)
	}
	return e, nil
}

func (m *memRepo) Transition(ctx context.Context, id uuid.UUID, from, to execution.ExecutionStatus, u execution.TransitionUpdate) error {
	if err := m.begin(ctx, true); err != nil {
		return err
	}
	defer m.mu.Unlock()
	if !from.CanTransitionTo(to) {
		return &execution.TransitionError{ExecutionID: id, From: from, To: to}
	}
	e, ok := m.execs[id]
	if !ok || e.Status != from {
		return execution.ErrTransitionConflict
	}
	now := m.tick()
	if to.IsTerminal() {
		for nid, n := range m.nodes {
			if n.ExecutionID != id || n.Status != execution.NodeStatusRunning {
				continue
			}
			if u.InterruptedNodeError == nil {
				return execution.ErrExecutionHasRunningNodes
			}
			stamped := *u.InterruptedNodeError
			nodeID := n.NodeID
			stamped.NodeID = &nodeID
			n.Status, n.Error, n.FinishedAt, n.UpdatedAt = execution.NodeStatusFailed, &stamped, &now, now
			m.nodes[nid] = n
		}
	}
	e.Status, e.UpdatedAt = to, now
	if to == execution.StatusRunning {
		e.StartedAt = &now
		token := u.ClaimToken
		e.ClaimToken = &token
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
	m.history[id] = append(m.history[id], execution.StatusTransition{ID: u.TransitionID, ExecutionID: id, From: &f, To: to, CreatedAt: now, Metadata: u.Metadata})
	return nil
}

func (m *memRepo) History(ctx context.Context, id uuid.UUID) ([]execution.StatusTransition, error) {
	if err := m.begin(ctx, false); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	if _, ok := m.execs[id]; !ok {
		return nil, execution.ErrExecutionNotFound
	}
	return append([]execution.StatusTransition(nil), m.history[id]...), nil
}

// nodeRepo exposes the node half of memRepo under the node interface.
type nodeRepo struct{ *memRepo }

// parentRunning mirrors the database rule: node writes need a RUNNING parent.
func (n nodeRepo) parentRunning(executionID uuid.UUID) error {
	e, ok := n.execs[executionID]
	if !ok {
		return execution.ErrExecutionNotFound
	}
	if e.Status != execution.StatusRunning {
		return fmt.Errorf("%w: %s", execution.ErrExecutionNotRunning, e.Status)
	}
	return nil
}

func (n nodeRepo) insert(rec execution.NodeExecution) error {
	if n.failNodeWrites {
		return fmt.Errorf("simulated node persistence outage")
	}
	if err := n.parentRunning(rec.ExecutionID); err != nil {
		return err
	}
	if rec.Status != execution.NodeStatusPending {
		return execution.ErrInvalidExecution
	}
	for _, other := range n.nodes {
		if other.ExecutionID == rec.ExecutionID && other.NodeID == rec.NodeID {
			return execution.ErrInvalidExecution
		}
	}
	now := n.tick()
	rec.CreatedAt, rec.UpdatedAt = now, now
	n.nodes[rec.ID] = rec
	n.order = append(n.order, rec.ID)
	return nil
}

func (n nodeRepo) Create(ctx context.Context, rec execution.NodeExecution) error {
	if err := n.begin(ctx, true); err != nil {
		return err
	}
	defer n.mu.Unlock()
	return n.insert(rec)
}

func (n nodeRepo) Begin(ctx context.Context, rec execution.NodeExecution, input map[string]any) error {
	if err := n.begin(ctx, true); err != nil {
		return err
	}
	defer n.mu.Unlock()
	if err := n.insert(rec); err != nil {
		return err
	}
	in, err := jsonCopy(input)
	if err != nil {
		delete(n.nodes, rec.ID)
		return err
	}
	stored := n.nodes[rec.ID]
	now := n.tick()
	stored.Status, stored.StartedAt, stored.Input, stored.UpdatedAt = execution.NodeStatusRunning, &now, in, now
	n.nodes[rec.ID] = stored
	return nil
}

func (n nodeRepo) Get(ctx context.Context, id uuid.UUID) (execution.NodeExecution, error) {
	if err := n.begin(ctx, false); err != nil {
		return execution.NodeExecution{}, err
	}
	defer n.mu.Unlock()
	rec, ok := n.nodes[id]
	if !ok {
		return execution.NodeExecution{}, execution.ErrNodeExecutionNotFound
	}
	return rec, nil
}

func (n nodeRepo) Transition(ctx context.Context, id uuid.UUID, from, to execution.NodeExecutionStatus, u execution.NodeTransitionUpdate) error {
	if err := n.begin(ctx, true); err != nil {
		return err
	}
	defer n.mu.Unlock()
	if n.failNodeWrites || (n.failNodeFinish && to.IsTerminal()) {
		return fmt.Errorf("simulated node persistence outage")
	}
	if !from.CanTransitionTo(to) {
		return &execution.NodeTransitionError{NodeExecutionID: id, From: from, To: to}
	}
	rec, ok := n.nodes[id]
	if !ok {
		return execution.ErrTransitionConflict
	}
	if err := n.parentRunning(rec.ExecutionID); err != nil {
		return err
	}
	if rec.Status != from {
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

func (n nodeRepo) ListByExecution(ctx context.Context, executionID uuid.UUID) ([]execution.NodeExecution, error) {
	if err := n.begin(ctx, false); err != nil {
		return nil, err
	}
	defer n.mu.Unlock()
	var out []execution.NodeExecution
	for _, id := range n.order {
		if rec := n.nodes[id]; rec.ExecutionID == executionID {
			out = append(out, rec)
		}
	}
	return out, nil
}
