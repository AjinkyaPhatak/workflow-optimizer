package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// DefinitionLoader loads the immutable definition of an exact workflow version.
// A definition that cannot be decoded must be reported as
// ErrInvalidWorkflowDefinition; anything else is treated as transient.
type DefinitionLoader interface {
	LoadDefinition(ctx context.Context, versionID uuid.UUID) (workflow.Definition, error)
}

// WorkspaceLookup finds the workspace that owns a workflow (Phase 11). The
// Runner hands it to nodes as NodeInput.WorkspaceID, the boundary that
// workspace-scoped resources (credentials) are resolved in.
type WorkspaceLookup interface {
	WorkspaceOf(ctx context.Context, workflowID uuid.UUID) (uuid.UUID, error)
}

// RunnerConfig lists the Runner's collaborators. Executions, NodeExecutions,
// Definitions, Validator and Graph are required.
type RunnerConfig struct {
	Executions     ExecutionRepository
	NodeExecutions NodeExecutionRepository
	Definitions    DefinitionLoader
	Validator      workflow.Validator
	Graph          *GraphExecutor
	Persistence    PersistenceOptions

	// Phase 10 reliability. Zero values keep the Phase 8 behaviour.

	// Backoff enables execution-level retries of retryable failures, paced by
	// its delays and bounded by the execution's persisted attempt limit. Nil
	// never retries.
	Backoff Backoff
	// NodeRetry enables bounded in-place retries of a failed node.
	NodeRetry NodeRetryPolicy
	// NodeTimeout bounds each node invocation (0 = none).
	NodeTimeout time.Duration
	// Leases keeps the claimed attempt's lease alive while it runs; Lease is
	// the grant recorded with each claim. Both or neither.
	Leases LeaseKeeper
	Lease  *LeaseGrant

	// Workspaces (Phase 11) scopes each run to its workflow's workspace. Nil
	// runs nodes without a workspace: workspace-scoped resources such as
	// credentials then cannot be resolved (they fail closed).
	Workspaces WorkspaceLookup

	// Observer (Phase 14) is told about lifecycle facts after they are
	// durable. It cannot influence the run. Nil observes nothing.
	Observer ExecutionObserver
}

// Runner drives one claimed execution attempt through its lifecycle:
//
//	claim (atomic; with a lease) -> load exact version -> validate
//	  -> GraphExecutor (resuming from completed nodes, under the execution
//	     deadline, node timeouts and in-place node retries)
//	  -> COMPLETED | FAILED | CANCELLED | PENDING (retry scheduled)
//
// GraphExecutor stays responsible only for graph execution; the Runner and
// LifecycleService own status, timestamps, history and node records.
//
// Ownership contract: once the claim succeeds, Run drives the attempt to a
// terminal status or to a persisted retry schedule. Every write is fenced by
// the attempt's claim token, so a worker whose lease expired (and whose
// attempt may have been recovered) can no longer change the execution. Every
// lifecycle write runs on a context detached from the caller's cancellation
// and bounded by Persistence.WriteTimeout. A retry never waits in-process: it
// is a persisted schedule that a scheduler dispatches when due.
type Runner struct {
	cfg     RunnerConfig
	obs     ExecutionObserver
	service *LifecycleService
	machine ExecutionStateMachine
	nodes   NodeExecutionStateMachine
}

// NewRunner validates the configuration and constructs a Runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Executions == nil || cfg.NodeExecutions == nil || cfg.Definitions == nil || cfg.Validator == nil || cfg.Graph == nil {
		return nil, fmt.Errorf("%w: runner requires executions, node executions, definitions, validator and graph executor", ErrInvalidExecution)
	}
	if (cfg.Leases == nil) != (cfg.Lease == nil) {
		return nil, fmt.Errorf("%w: a lease keeper and a lease grant go together", ErrInvalidExecution)
	}
	if cfg.Lease != nil && (cfg.Lease.Owner == "" || cfg.Lease.Duration <= 0) {
		return nil, fmt.Errorf("%w: a lease grant requires an owner and a positive duration", ErrInvalidExecution)
	}
	service := NewLifecycleServiceWithOptions(cfg.Executions, cfg.Persistence)
	return &Runner{
		cfg:     cfg,
		obs:     observerOrNop(cfg.Observer),
		service: service,
		machine: service.machine,
		nodes:   NewNodeExecutionStateMachineWithOptions(cfg.NodeExecutions, cfg.Persistence),
	}, nil
}

// Service exposes the lifecycle service the Runner uses (Create, Cancel, ...).
func (r *Runner) Service() ExecutionService { return r.service }

// claimedAttempt is the attempt this Run owns.
type claimedAttempt struct {
	id    uuid.UUID
	owner uuid.UUID // the claim token: fences every write of this attempt
	exec  Execution // snapshot right after the claim
	start time.Time // when this Run claimed the attempt
}

// Run claims and executes one attempt of a PENDING execution. If the claim
// fails (another worker owns it, the retry is not due, the caller was already
// cancelled, the database gave up) nothing else happens and the claim error is
// returned. A failed attempt that is retried returns *RetryScheduledError.
func (r *Runner) Run(ctx context.Context, executionID uuid.UUID) (ExecutionResult, error) {
	a := claimedAttempt{id: executionID, owner: uuid.New(), start: time.Now()}
	var lease *LeaseGrant
	if r.cfg.Lease != nil {
		grant := *r.cfg.Lease
		lease = &grant
	}
	// The claim returns its own snapshot (attempt, deadline, limits), bound
	// to a.owner: this Run never adopts an attempt it did not claim.
	exec, err := r.service.claim(ctx, executionID, a.owner, lease)
	if errors.Is(err, errClaimUnread) {
		return ExecutionResult{}, r.fail(ctx, a, ExecutionError{
			Code: CodeExecutionFailed, Message: "load claimed execution: " + err.Error(), Retryable: true, Source: SourceDatabase,
		}, interruptedByFailure, "", err)
	}
	if err != nil {
		return ExecutionResult{}, err
	}
	a.exec = exec
	if exec.CancelRequested {
		// Cancellation beats retry: a cancel-requested execution is claimed
		// only to be cancelled.
		err := r.service.cancelAs(ctx, executionID, a.owner, map[string]any{"reason": "cancel_requested"})
		if err != nil {
			return ExecutionResult{}, r.explainFinalizationFailure(ctx, a, err)
		}
		r.obs.ExecutionCancelled(ctx, executionID, exec.Attempt, "cancel_requested")
		return ExecutionResult{}, fmt.Errorf("%w: %w", ErrExecutionCancelled, ErrCancelRequested)
	}
	if exec.Attempt > 1 {
		r.obs.RetryStarted(ctx, executionID, nil, exec.Attempt)
	} else {
		var queued time.Duration
		if exec.StartedAt != nil && !exec.CreatedAt.IsZero() {
			queued = exec.StartedAt.Sub(exec.CreatedAt)
		}
		r.obs.ExecutionStarted(ctx, executionID, exec.Attempt, exec.MaxAttempts, queued)
	}

	runCtx := ctx
	if exec.DeadlineAt != nil {
		var cancelDeadline context.CancelFunc
		runCtx, cancelDeadline = context.WithDeadline(runCtx, *exec.DeadlineAt)
		defer cancelDeadline()
	}
	if r.cfg.Leases != nil {
		var stop func()
		runCtx, stop = r.cfg.Leases.Keep(runCtx, executionID, a.owner)
		defer stop()
	}

	lctx, cancel := persistContext(ctx, r.cfg.Persistence)
	def, err := r.cfg.Definitions.LoadDefinition(lctx, exec.WorkflowVersionID)
	cancel()
	if err != nil {
		execErr := ExecutionError{Code: CodeDefinitionLoadFailed, Message: "load workflow version: " + err.Error(), Retryable: true, Source: SourceDatabase}
		if errors.Is(err, ErrInvalidWorkflowDefinition) {
			execErr = ExecutionError{Code: CodeInvalidWorkflow, Message: err.Error(), Source: SourceSystem}
		}
		return ExecutionResult{}, r.failOrRetry(runCtx, a, execErr, interruptedByFailure, err)
	}
	var workspaceID uuid.UUID
	if r.cfg.Workspaces != nil {
		wctx, cancel := persistContext(ctx, r.cfg.Persistence)
		workspaceID, err = r.cfg.Workspaces.WorkspaceOf(wctx, exec.WorkflowID)
		cancel()
		if err != nil {
			execErr := ExecutionError{Code: CodeDefinitionLoadFailed, Message: "load workflow workspace: " + err.Error(), Retryable: true, Source: SourceDatabase}
			if errors.Is(err, ErrWorkflowNotFound) {
				execErr = ExecutionError{Code: CodeInvalidWorkflow, Message: err.Error(), Source: SourceSystem}
			}
			return ExecutionResult{}, r.failOrRetry(runCtx, a, execErr, interruptedByFailure, err)
		}
	}
	if res := r.cfg.Validator.Validate(def); !res.Valid {
		verr := fmt.Errorf("%w: %s", ErrInvalidExecution, summarizeValidation(res))
		return ExecutionResult{}, r.fail(ctx, a, ExecutionError{Code: CodeInvalidWorkflow, Message: verr.Error(), Source: SourceSystem}, interruptedByFailure, "", verr)
	}

	completed, prior, err := r.resumeState(ctx, a)
	if err != nil {
		return ExecutionResult{}, r.failOrRetry(runCtx, a, ExecutionError{
			Code: CodeExecutionFailed, Message: "load completed nodes: " + err.Error(), Retryable: true, Source: SourceDatabase,
		}, interruptedByFailure, err)
	}

	recorder := &nodeRecorder{
		executionID: executionID,
		claim:       a.owner,
		sideEffects: r.cfg.Graph.SideEffects,
		machine:     r.nodes,
		ids:         map[string]uuid.UUID{},
		obs:         r.obs,
		running:     map[string]runningNode{},
		invocations: map[string]int{},
	}
	result, graphErr := r.cfg.Graph.ExecuteWithOptions(runCtx, def, exec.Input, ExecuteOptions{
		Observer:         recorder,
		Completed:        completed,
		PriorInvocations: prior,
		OperationKey:     executionID.String(),
		NodeTimeout:      r.cfg.NodeTimeout,
		NodeRetry:        r.cfg.NodeRetry,
		Scope:            node.Scope{WorkspaceID: workspaceID},
	})
	return result, r.finalize(runCtx, a, result, graphErr)
}

// resumeState loads, for a retried attempt, the typed outputs of nodes that
// already completed in EARLIER attempts (they are not re-run) and how often
// each node ran. Records of this or later attempts are never reused: they
// are not this claim's past.
func (r *Runner) resumeState(ctx context.Context, a claimedAttempt) (map[string]node.NodeOutput, map[string]int, error) {
	if a.exec.Attempt <= 1 {
		return nil, nil, nil
	}
	rctx, cancel := persistContext(ctx, r.cfg.Persistence)
	defer cancel()
	records, err := r.cfg.NodeExecutions.ListByExecution(rctx, a.id)
	if err != nil {
		return nil, nil, err
	}
	completed := map[string]node.NodeOutput{}
	prior := map[string]int{}
	for _, n := range records {
		if n.ExecutionAttempt >= a.exec.Attempt {
			continue
		}
		prior[n.NodeID]++
		if n.Status == NodeStatusCompleted && n.OutputValues != nil {
			ports := make(map[string]node.Value, len(n.OutputValues))
			for k, v := range n.OutputValues {
				ports[k] = v
			}
			completed[n.NodeID] = node.NewNodeOutput(ports)
		}
	}
	return completed, prior, nil
}

// finalize records the attempt's outcome. Order matters:
//  1. success -> COMPLETED (work finished; a late cancel does not erase it);
//  2. ownership lost (lease expired or execution finalized elsewhere) ->
//     write nothing: the attempt belongs to the reaper or another actor now;
//  3. a node write rejected because another actor already finalized the
//     execution -> report that status, write nothing;
//  4. the caller cancelled, or cancellation was requested -> CANCELLED, even
//     if a node ignored its context and returned an unrelated error;
//  5. the execution deadline (or the caller's deadline) passed -> FAILED
//     with EXECUTION_TIMEOUT, never retried;
//  6. otherwise the failure's own classification decides: retry (persisted
//     schedule) or FAILED (dead-lettered when retries ran out).
func (r *Runner) finalize(ctx context.Context, a claimedAttempt, result ExecutionResult, graphErr error) error {
	cause := context.Cause(ctx)
	switch {
	case graphErr == nil:
		if err := r.service.completeAs(ctx, a.id, a.owner, workflowOutput(result)); err != nil {
			return r.explainFinalizationFailure(ctx, a, fmt.Errorf("record completion: %w", err))
		}
		r.obs.ExecutionCompleted(ctx, a.id, a.exec.Attempt, time.Since(a.start))
		return nil
	case errors.Is(cause, ErrLeaseLost), errors.Is(graphErr, ErrLeaseLost):
		// Ownership lost (heartbeat, or a node write fenced out as stale).
		return fmt.Errorf("%w: %w", ErrLeaseLost, graphErr)
	case errors.Is(graphErr, ErrExecutionNotRunning):
		return r.explainFinalizationFailure(ctx, a, graphErr)
	case errors.Is(ctx.Err(), context.Canceled):
		reason := "caller_cancelled"
		if errors.Is(cause, ErrCancelRequested) {
			reason = "cancel_requested"
		}
		causeErr := fmt.Errorf("execution cancelled by caller: %w", errors.Join(ctx.Err(), graphErr))
		meta := map[string]any{"reason": reason}
		if !errors.Is(graphErr, context.Canceled) {
			meta["graph_error"] = graphErr.Error()
		}
		if err := r.service.cancelAs(ctx, a.id, a.owner, meta); err != nil {
			return r.explainFinalizationFailure(ctx, a, errors.Join(causeErr, fmt.Errorf("record cancellation: %w", err)))
		}
		r.obs.ExecutionCancelled(ctx, a.id, a.exec.Attempt, reason)
		return causeErr
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		execErr := ErrorFromExecution(graphErr)
		execErr.Code, execErr.Retryable, execErr.RetryAfter, execErr.Source = CodeTimeout, false, 0, SourceSystem
		return r.fail(ctx, a, execErr, interruptedByFailure, "", graphErr)
	default:
		interrupted := interruptedByFailure
		var nodeErr *NodeExecutionError
		if errors.As(graphErr, &nodeErr) && nodeErr.Stage == StageObserve {
			interrupted = ExecutionError{Code: CodeNodeObservation,
				Message: "node executed but its result could not be recorded: " + nodeErr.Err.Error()}
		}
		return r.failOrRetry(ctx, a, ErrorFromExecution(graphErr), interrupted, graphErr)
	}
}

// failOrRetry applies DecideFailure to a failed attempt and writes the
// result. The database re-checks the decision when the retry is written
// (attempts left, deadline, cancellation); a refusal turns into the matching
// terminal outcome, never into a silent requeue.
func (r *Runner) failOrRetry(ctx context.Context, a claimedAttempt, execErr ExecutionError, interrupted ExecutionError, cause error) error {
	if errors.Is(context.Cause(ctx), ErrLeaseLost) {
		return fmt.Errorf("%w: %w", ErrLeaseLost, cause)
	}
	d := DecideFailure(AttemptState{
		Attempt: a.exec.Attempt, MaxAttempts: a.exec.MaxAttempts, DeadlineAt: a.exec.DeadlineAt, Now: time.Now(),
	}, execErr, r.cfg.Backoff)
	if d.Action == ActionRetry {
		err := r.service.scheduleRetry(ctx, a.id, a.owner, d.Delay, execErr, interrupted, nil)
		switch {
		case err == nil:
			r.obs.RetryScheduled(ctx, a.id, nil, a.exec.Attempt+1, d.Delay, execErr)
			return &RetryScheduledError{ExecutionID: a.id, Attempt: a.exec.Attempt, Delay: d.Delay, Err: cause}
		case errors.Is(err, ErrCancelRequested):
			if cerr := r.service.cancelAs(ctx, a.id, a.owner, map[string]any{"reason": "cancel_requested"}); cerr != nil {
				return r.explainFinalizationFailure(ctx, a, errors.Join(cause, cerr))
			}
			r.obs.ExecutionCancelled(ctx, a.id, a.exec.Attempt, "cancel_requested")
			return fmt.Errorf("%w: %w", ErrCancelRequested, cause)
		case errors.Is(err, ErrRetryAfterDeadline):
			d = FailureDecision{Action: ActionFail, DeadLetter: DeadLetterDeadlineExceeded}
		case errors.Is(err, ErrAttemptsExhausted):
			d = FailureDecision{Action: ActionFail, DeadLetter: DeadLetterAttemptsExhausted}
		default:
			return r.explainFinalizationFailure(ctx, a, errors.Join(cause, fmt.Errorf("schedule retry: %w", err)))
		}
	}
	return r.fail(ctx, a, execErr, interrupted, d.DeadLetter, cause)
}

func (r *Runner) fail(ctx context.Context, a claimedAttempt, execErr ExecutionError, interrupted ExecutionError, dead DeadLetterReason, cause error) error {
	if err := r.service.failAs(ctx, a.id, a.owner, execErr, interrupted, dead); err != nil {
		return r.explainFinalizationFailure(ctx, a, errors.Join(cause, fmt.Errorf("record failure: %w", err)))
	}
	r.obs.ExecutionFailed(ctx, a.id, a.exec.Attempt, execErr, dead)
	if dead != "" {
		return &DeadLetteredError{ExecutionID: a.id, Attempt: a.exec.Attempt, Reason: dead, Err: cause}
	}
	return cause
}

// explainFinalizationFailure distinguishes "another actor already finalized
// this execution" (typed error) from a genuine persistence failure. A write
// refused because the execution moved on to another attempt (this attempt was
// recovered by the reaper, possibly re-claimed and finished by another worker)
// is reported as ErrLeaseLost: this worker lost ownership, nothing was written.
func (r *Runner) explainFinalizationFailure(ctx context.Context, a claimedAttempt, err error) error {
	if errors.Is(err, ErrLeaseLost) {
		return err
	}
	id := a.id
	rctx, cancel := persistContext(ctx, r.cfg.Persistence)
	defer cancel()
	current, getErr := r.cfg.Executions.Get(rctx, id)
	if getErr == nil && a.exec.Attempt > 0 && (current.Attempt != a.exec.Attempt ||
		(!current.Status.IsTerminal() && (current.ClaimToken == nil || *current.ClaimToken != a.owner))) {
		return fmt.Errorf("%w: execution %s is %s at attempt %d: %w", ErrLeaseLost, id, current.Status, current.Attempt, err)
	}
	if getErr == nil && current.Status.IsTerminal() {
		return &ExternallyFinalizedError{ExecutionID: id, Status: current.Status, Err: err}
	}
	return err
}

func summarizeValidation(res workflow.ValidationResult) string {
	parts := make([]string, 0, len(res.Errors))
	for _, e := range res.Errors {
		p := string(e.Code)
		if e.NodeID != "" {
			p += " node=" + e.NodeID
		}
		parts = append(parts, p)
	}
	return "workflow failed executable validation: " + strings.Join(parts, ", ")
}

// nodeRecorder is the NodeObserver that persists one NodeExecution per node
// GraphExecutor reaches. A started node is created RUNNING atomically
// (PENDING -> RUNNING in one transaction). A write rejected because the
// execution is no longer RUNNING stops the graph with ErrExecutionNotRunning.
// The ids map is per-run bookkeeping (node ID -> record ID), not a lock.
type nodeRecorder struct {
	executionID uuid.UUID
	// claim fences every node write to this Run's claim: the store rejects
	// it (ErrLeaseLost) once the claim no longer owns the execution.
	claim uuid.UUID
	// sideEffects looks up a node type's declaration, persisted with each
	// record so recovery knows whether an interrupted node may run again.
	sideEffects func(nodeType string) node.SideEffects
	machine     NodeExecutionStateMachine
	ids         map[string]uuid.UUID

	// Phase 14: events are reported after each node write succeeded.
	obs         ExecutionObserver
	running     map[string]runningNode
	invocations map[string]int // invocations started in this Run
}

type runningNode struct {
	ref   NodeRef
	start time.Time
}

var (
	_ NodeObserver         = (*nodeRecorder)(nil)
	_ NodeProgressObserver = (*nodeRecorder)(nil)
)

func (n *nodeRecorder) begin(ctx context.Context, nodeID, nodeType string, input map[string]any) error {
	rec := NodeExecution{
		ID:          uuid.New(),
		ExecutionID: n.executionID,
		NodeID:      nodeID,
		NodeType:    nodeType,
		Status:      NodeStatusPending,
		ClaimToken:  n.claim,
		SideEffects: n.sideEffects(nodeType),
	}
	if err := n.machine.Begin(ctx, rec, input); err != nil {
		return err
	}
	n.ids[nodeID] = rec.ID
	return nil
}

// started reports a node invocation whose RUNNING record was just written; a
// second invocation within this Run is an in-place retry.
func (n *nodeRecorder) started(ctx context.Context, nodeID, nodeType string, invocation int) {
	if n.invocations[nodeID] > 0 {
		id := nodeID
		n.obs.RetryStarted(ctx, n.executionID, &id, invocation)
	}
	n.invocations[nodeID]++
	ref := NodeRef{ID: nodeID, Type: nodeType, Invocation: invocation}
	n.running[nodeID] = runningNode{ref: ref, start: time.Now()}
	n.obs.NodeStarted(ctx, n.executionID, ref)
}

func (n *nodeRecorder) NodeStarted(ctx context.Context, nodeID, nodeType string, input node.NodeInput) error {
	if err := n.begin(ctx, nodeID, nodeType, nodeInputMap(input)); err != nil {
		return err
	}
	n.started(ctx, nodeID, nodeType, input.Attempt)
	return nil
}

// NodeReused implements NodeProgressObserver.
func (n *nodeRecorder) NodeReused(ctx context.Context, nodeID, _ string) {
	n.obs.NodeSkipped(ctx, n.executionID, nodeID, "completed_in_previous_attempt")
}

// NodeRetryScheduled implements NodeProgressObserver.
func (n *nodeRecorder) NodeRetryScheduled(ctx context.Context, nodeID string, _ int, delay time.Duration, failure error) {
	id := nodeID
	n.obs.RetryScheduled(ctx, n.executionID, &id, n.running[nodeID].ref.Invocation+1, delay, nodeFailure(nodeID, failure))
}

func (n *nodeRecorder) NodeFinished(ctx context.Context, nodeID string, output node.NodeOutput, err error) error {
	id, ok := n.ids[nodeID]
	if !ok {
		return fmt.Errorf("%w: node %q finished without a started record", ErrNodeExecutionNotFound, nodeID)
	}
	run := n.running[nodeID]
	if err == nil {
		values := output.Ports
		if values == nil {
			values = map[string]node.Value{}
		}
		if terr := n.machine.Transition(ctx, id, NodeStatusCompleted, NodeTransitionUpdate{Output: portsMap(output.Ports), OutputValues: values}); terr != nil {
			return terr
		}
		n.obs.NodeCompleted(ctx, n.executionID, run.ref, time.Since(run.start))
		return nil
	}
	nodeErr := nodeFailure(nodeID, err)
	if terr := n.machine.Transition(ctx, id, NodeStatusFailed, NodeTransitionUpdate{Error: &nodeErr}); terr != nil {
		return terr
	}
	n.obs.NodeFailed(ctx, n.executionID, run.ref, nodeErr, time.Since(run.start))
	return nil
}

// NodeFailedBeforeExecute records a node whose configuration or inputs could
// not be resolved: it was reached (RUNNING) and failed before Execute.
func (n *nodeRecorder) NodeFailedBeforeExecute(ctx context.Context, nodeID, nodeType string, stage Stage, err error) error {
	if beginErr := n.begin(ctx, nodeID, nodeType, nil); beginErr != nil {
		return beginErr
	}
	n.started(ctx, nodeID, nodeType, n.invocations[nodeID]+1)
	nodeErr := nodeFailure(nodeID, &NodeExecutionError{NodeID: nodeID, NodeType: nodeType, Stage: stage, Err: err})
	if terr := n.machine.Transition(ctx, n.ids[nodeID], NodeStatusFailed, NodeTransitionUpdate{Error: &nodeErr}); terr != nil {
		return terr
	}
	run := n.running[nodeID]
	n.obs.NodeFailed(ctx, n.executionID, run.ref, nodeErr, time.Since(run.start))
	return nil
}

// nodeFailure builds the node-level ExecutionError, keeping node-reported
// codes and distinguishing cancellation/timeouts from ordinary failures.
func nodeFailure(nodeID string, err error) ExecutionError {
	e := ErrorFromExecution(err)
	if e.Code == CodeExecutionFailed {
		e.Code = CodeNodeFailed
	}
	id := nodeID
	e.NodeID = &id
	return e
}

func portsMap(ports map[string]node.Value) map[string]any {
	out := make(map[string]any, len(ports))
	for name, v := range ports {
		out[name] = v.Data
	}
	return out
}

func nodeInputMap(in node.NodeInput) map[string]any {
	m := map[string]any{"ports": portsMap(in.Ports)}
	if in.Config != nil {
		m["config"] = in.Config
	}
	return m
}

// workflowOutput maps exit-node results to {exitNodeID: {port: data}}.
func workflowOutput(res ExecutionResult) map[string]any {
	out := make(map[string]any, len(res.Outputs))
	for id, o := range res.Outputs {
		out[id] = portsMap(o.Ports)
	}
	return out
}
