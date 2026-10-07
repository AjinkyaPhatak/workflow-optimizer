package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ExecutionService coordinates the execution lifecycle. It owns no graph
// traversal; see Runner for the integration with GraphExecutor.
type ExecutionService interface {
	// Create persists a new PENDING execution and nothing else.
	Create(ctx context.Context, workflowID, versionID uuid.UUID, input map[string]any) (Execution, error)
	// Start atomically claims a PENDING execution (PENDING -> RUNNING). Exactly
	// one concurrent caller succeeds; the others get ErrExecutionNotClaimable.
	Start(ctx context.Context, executionID uuid.UUID) error
	Complete(ctx context.Context, executionID uuid.UUID, output map[string]any) error
	Fail(ctx context.Context, executionID uuid.UUID, executionError ExecutionError) error
	Cancel(ctx context.Context, executionID uuid.UUID) error
}

// LifecycleService is the persisted ExecutionService.
type LifecycleService struct {
	repo     ExecutionRepository
	machine  ExecutionStateMachine
	opts     PersistenceOptions
	defaults ExecutionDefaults
	cancels  CancellationStore
	obs      ExecutionObserver
}

// ExecutionDefaults configures new executions (Phase 10). Zero values keep
// the Phase 8 behaviour: one attempt, no deadline.
type ExecutionDefaults struct {
	// MaxAttempts is the attempt limit of new executions (0 = 1).
	MaxAttempts int
	// Timeout is the total time budget of new executions (0 = none).
	Timeout time.Duration
}

// WithDefaults returns a copy of the service that creates executions with d.
func (s *LifecycleService) WithDefaults(d ExecutionDefaults) *LifecycleService {
	c := *s
	c.defaults = d
	return &c
}

// WithCancellations returns a copy of the service that records durable
// cancellation requests in store (required by RequestCancel).
func (s *LifecycleService) WithCancellations(store CancellationStore) *LifecycleService {
	c := *s
	c.cancels = store
	return &c
}

// WithObserver returns a copy of the service that reports the cancellations
// it applies (Phase 14).
func (s *LifecycleService) WithObserver(o ExecutionObserver) *LifecycleService {
	c := *s
	c.obs = o
	return &c
}

var _ ExecutionService = (*LifecycleService)(nil)

// NewLifecycleService wires the service to its repository and state machine.
func NewLifecycleService(repo ExecutionRepository) *LifecycleService {
	return NewLifecycleServiceWithOptions(repo, PersistenceOptions{})
}

// NewLifecycleServiceWithOptions configures the persistence write timeout.
func NewLifecycleServiceWithOptions(repo ExecutionRepository, opts PersistenceOptions) *LifecycleService {
	return &LifecycleService{repo: repo, machine: NewExecutionStateMachineWithOptions(repo, opts), opts: opts}
}

// Default errors stamped on nodes that are still RUNNING when an execution is
// failed or cancelled; the node really started, so it is never shown as
// "never ran", and its outcome is explicitly unknown.
var (
	interruptedByFailure = ExecutionError{Code: CodeNodeInterrupted,
		Message: "execution failed while this node was running; the node's outcome was not recorded"}
	interruptedByCancel = ExecutionError{Code: CodeCancelled,
		Message: "execution was cancelled while this node was running; the node's outcome was not recorded"}
)

// Create refuses to start a write for an already-cancelled caller; once
// issued, the insert is resolved on a bounded, detached context and the
// persisted row is returned from the same statement (no separate read that
// could fail after a successful insert).
func (s *LifecycleService) Create(ctx context.Context, workflowID, versionID uuid.UUID, input map[string]any) (Execution, error) {
	if workflowID == uuid.Nil || versionID == uuid.Nil {
		return Execution{}, fmt.Errorf("%w: workflow and workflow version IDs are required", ErrInvalidExecution)
	}
	if err := ctx.Err(); err != nil {
		return Execution{}, fmt.Errorf("create not attempted: %w", err)
	}
	wctx, cancel := persistContext(ctx, s.opts)
	defer cancel()
	return s.repo.Create(wctx, Execution{
		ID:                uuid.New(),
		WorkflowID:        workflowID,
		WorkflowVersionID: versionID,
		Status:            StatusPending,
		Input:             input,
		MaxAttempts:       s.defaults.MaxAttempts,
		Timeout:           s.defaults.Timeout,
	})
}

// Start claims the execution under a fresh claim token. A caller whose
// context is already done never issues a claim; a claim that was issued is
// always resolved, so a caller is never told "not claimed" for a claim the
// database committed.
func (s *LifecycleService) Start(ctx context.Context, id uuid.UUID) error {
	return s.claimTransition(ctx, id, uuid.New(), nil)
}

// claimTransition moves PENDING -> RUNNING under token, recording lease (if
// any) in the same transaction. The database increments the attempt.
func (s *LifecycleService) claimTransition(ctx context.Context, id, token uuid.UUID, lease *LeaseGrant) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("claim not attempted: %w", err)
	}
	return s.machine.Transition(ctx, id, StatusRunning, TransitionUpdate{ClaimToken: token, Lease: lease})
}

// errClaimUnread marks a claim that was applied but whose snapshot could not
// be read: the caller owns the attempt and must finalize it.
var errClaimUnread = errors.New("execution: claimed execution could not be read")

// claim claims the execution under token and returns the claim's own
// snapshot: the attempt, deadline and limits the database assigned to this
// claim. The snapshot is bound to token: if the execution is no longer
// RUNNING under token when it is read (the lease expired before the read and
// the attempt was recovered, possibly re-claimed), the claim's attempt is no
// longer this caller's and claim returns ErrLeaseLost instead of adopting
// whatever attempt is current. Since the database assigns a new attempt only
// together with a new claim token, a snapshot carrying token describes
// exactly the attempt this claim created.
func (s *LifecycleService) claim(ctx context.Context, id, token uuid.UUID, lease *LeaseGrant) (Execution, error) {
	if err := s.claimTransition(ctx, id, token, lease); err != nil {
		return Execution{}, err
	}
	rctx, cancel := persistContext(ctx, s.opts)
	defer cancel()
	e, err := s.repo.Get(rctx, id)
	if err != nil {
		return Execution{}, fmt.Errorf("%w: %w", errClaimUnread, err)
	}
	if e.Status != StatusRunning || e.ClaimToken == nil || *e.ClaimToken != token {
		return Execution{}, fmt.Errorf("%w: execution %s is %s at attempt %d under another claim before its claim could be read",
			ErrLeaseLost, id, e.Status, e.Attempt)
	}
	return e, nil
}

// RequestCancel records a durable cancellation request, then cancels a
// RUNNING execution at once (its worker notices through its lease). A PENDING
// execution (for example one waiting for a retry) can no longer be retried:
// the next claim cancels it without running anything. A terminal execution
// reports a TransitionError.
func (s *LifecycleService) RequestCancel(ctx context.Context, id uuid.UUID) error {
	if s.cancels == nil {
		return fmt.Errorf("%w: no cancellation store configured", ErrInvalidExecution)
	}
	wctx, cancel := persistContext(ctx, s.opts)
	err := s.cancels.RequestCancel(wctx, id)
	cancel()
	if err != nil {
		return err
	}
	err = s.cancel(ctx, id, map[string]any{"reason": "cancel_requested"})
	var te *TransitionError
	if errors.As(err, &te) && te.From == StatusPending {
		return nil // recorded; enforced at the next claim (the Runner reports it)
	}
	if err == nil {
		observerOrNop(s.obs).ExecutionCancelled(ctx, id, 0, "cancel_requested")
	}
	return err
}

func (s *LifecycleService) Complete(ctx context.Context, id uuid.UUID, output map[string]any) error {
	if output == nil {
		output = map[string]any{}
	}
	return s.machine.Transition(ctx, id, StatusCompleted, TransitionUpdate{Output: output})
}

func (s *LifecycleService) Fail(ctx context.Context, id uuid.UUID, executionError ExecutionError) error {
	return s.fail(ctx, id, executionError, interruptedByFailure)
}

func (s *LifecycleService) fail(ctx context.Context, id uuid.UUID, executionError ExecutionError, interrupted ExecutionError) error {
	meta := map[string]any{"error_code": executionError.Code}
	if executionError.NodeID != nil {
		meta["node_id"] = *executionError.NodeID
	}
	return s.machine.Transition(ctx, id, StatusFailed, TransitionUpdate{
		Error: &executionError, Metadata: meta, InterruptedNodeError: &interrupted,
	})
}

func (s *LifecycleService) Cancel(ctx context.Context, id uuid.UUID) error {
	return s.cancel(ctx, id, map[string]any{"reason": "cancelled"})
}

func (s *LifecycleService) cancel(ctx context.Context, id uuid.UUID, meta map[string]any) error {
	return s.cancelAs(ctx, id, uuid.Nil, meta)
}

// cancelAs is cancel fenced by owner (uuid.Nil = unfenced).
func (s *LifecycleService) cancelAs(ctx context.Context, id, owner uuid.UUID, meta map[string]any) error {
	interrupted := interruptedByCancel
	return s.machine.Transition(ctx, id, StatusCancelled, TransitionUpdate{Metadata: meta, InterruptedNodeError: &interrupted, Owner: owner})
}

// completeAs is Complete fenced by owner.
func (s *LifecycleService) completeAs(ctx context.Context, id, owner uuid.UUID, output map[string]any) error {
	if output == nil {
		output = map[string]any{}
	}
	return s.machine.Transition(ctx, id, StatusCompleted, TransitionUpdate{Output: output, Owner: owner})
}

// failAs is fail fenced by owner, optionally recording a dead letter.
func (s *LifecycleService) failAs(ctx context.Context, id, owner uuid.UUID, executionError ExecutionError, interrupted ExecutionError, dead DeadLetterReason) error {
	meta := map[string]any{"error_code": executionError.Code}
	if executionError.NodeID != nil {
		meta["node_id"] = *executionError.NodeID
	}
	if dead != "" {
		meta["dead_letter"] = string(dead)
	}
	return s.machine.Transition(ctx, id, StatusFailed, TransitionUpdate{
		Error: &executionError, Metadata: meta, InterruptedNodeError: &interrupted, Owner: owner, DeadLetter: dead,
	})
}

// scheduleRetry moves the owner's failed attempt RUNNING -> PENDING; the
// retry is due delay after the transition.
func (s *LifecycleService) scheduleRetry(ctx context.Context, id, owner uuid.UUID, delay time.Duration, lastError ExecutionError, interrupted ExecutionError, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["error_code"] = lastError.Code
	meta["retry_delay_ms"] = delay.Milliseconds()
	return s.machine.Transition(ctx, id, StatusPending, TransitionUpdate{
		Owner: owner, RetryDelay: delay, LastError: &lastError, InterruptedNodeError: &interrupted, Metadata: meta,
	})
}
