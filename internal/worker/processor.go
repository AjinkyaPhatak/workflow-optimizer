// Package worker runs workflow executions in the background: it consumes
// queue.Jobs and drives each execution through the existing Phase 8 lifecycle
// (execution.Runner), which in turn uses the Phase 7 GraphExecutor.
//
// Ownership rule: a job only says "try this execution". A worker executes it
// only if its atomic PostgreSQL claim (PENDING -> RUNNING, performed by
// execution.Runner via the Phase 8 state machine) succeeds. There is no
// process-local locking; the database claim is the ownership boundary, so the
// rule holds across any number of worker processes.
package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/queue"
)

// Outcome classifies what happened to one job.
type Outcome string

const (
	// The job's execution ran here and reached a terminal status.
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeSkipped: the execution was not PENDING when the job arrived
	// (already running elsewhere, or terminal). Nothing was executed or written.
	OutcomeSkipped Outcome = "skipped_not_pending"
	// OutcomeClaimLost: the execution was PENDING, but another worker won the
	// atomic claim. Nothing was executed.
	OutcomeClaimLost Outcome = "claim_lost"
	// OutcomeNotFound: no such execution. Nothing was executed.
	OutcomeNotFound Outcome = "not_found"
	// OutcomeNotAttempted: no claim was made (the worker was stopping, or the
	// claim was definitely not applied); the execution is still PENDING.
	OutcomeNotAttempted Outcome = "not_attempted"
	// OutcomeError: the job could not be handled because of an infrastructure
	// failure (e.g. PostgreSQL unreachable, or a claimed execution whose final
	// status could not be recorded). Result.Err says why.
	OutcomeError Outcome = "error"

	// Phase 10.

	// OutcomeRetryScheduled: the attempt ran and failed retryably; the
	// execution is PENDING with a persisted retry schedule. The scheduler,
	// not this worker, dispatches it when due.
	OutcomeRetryScheduled Outcome = "retry_scheduled"
	// OutcomeNotDue: the job arrived before the execution's scheduled retry
	// (a stale or duplicate delivery). The database refused the claim; the
	// scheduler dispatches the retry when it is due.
	OutcomeNotDue Outcome = "not_due"
	// OutcomeLeaseLost: this worker lost ownership of the attempt (its lease
	// expired and the attempt may have been recovered, or another actor
	// finalized it). It wrote nothing more.
	OutcomeLeaseLost Outcome = "lease_lost"
)

// Result describes how a job was handled.
type Result struct {
	ExecutionID uuid.UUID
	Outcome     Outcome
	// Status is the execution's status as last read from PostgreSQL ("" when
	// unknown).
	Status execution.ExecutionStatus
	// Err is the underlying error, if any (for completed runs it is nil; for
	// failed runs it is the execution error).
	Err error
	// DeadLetter is set when the execution failed and was dead-lettered.
	DeadLetter execution.DeadLetterReason
	// DeadLetterAttempt is the attempt that was dead-lettered.
	DeadLetterAttempt int
	// Requeue reports that this worker has definitely NOT claimed the
	// execution and could not decide what to do with it because of an
	// infrastructure failure (or because it is stopping), so the job must go
	// back to the queue instead of being dropped. Returning it is safe: the
	// next attempt goes through the same PENDING check and atomic database
	// claim, so it can never cause a second execution.
	//
	// It is set only when (a) the execution could not be loaded (never for
	// "not found"), or (b) the runner returned without the claim applied
	// (status still PENDING). It is never set once a claim may have been
	// made, for terminal or RUNNING executions, or for lost claims.
	Requeue bool
}

// Executed reports whether this worker ran the execution's graph.
func (r Result) Executed() bool {
	switch r.Outcome {
	case OutcomeCompleted, OutcomeFailed, OutcomeCancelled, OutcomeRetryScheduled:
		return true
	}
	return false
}

// JobProcessor handles one dequeued job.
type JobProcessor interface {
	Process(ctx context.Context, job queue.Job) Result
}

// ExecutionReader loads an execution (execution.ExecutionRepository satisfies it).
type ExecutionReader interface {
	Get(ctx context.Context, id uuid.UUID) (execution.Execution, error)
}

// ExecutionRunner claims and runs one execution (execution.Runner satisfies
// it: Start = atomic claim, then GraphExecutor, then finalization).
type ExecutionRunner interface {
	Run(ctx context.Context, executionID uuid.UUID) (execution.ExecutionResult, error)
}

// ExecutionProcessor is the production JobProcessor.
type ExecutionProcessor struct {
	executions  ExecutionReader
	runner      ExecutionRunner
	readTimeout time.Duration
}

var _ JobProcessor = (*ExecutionProcessor)(nil)

// DefaultReadTimeout bounds the status read that classifies a finished job.
const DefaultReadTimeout = 10 * time.Second

// NewExecutionProcessor wires the processor to the Phase 8 repository and
// runner.
func NewExecutionProcessor(executions ExecutionReader, runner ExecutionRunner) (*ExecutionProcessor, error) {
	if executions == nil || runner == nil {
		return nil, errors.New("worker: processor requires an execution reader and a runner")
	}
	return &ExecutionProcessor{executions: executions, runner: runner, readTimeout: DefaultReadTimeout}, nil
}

// Process handles one job:
//
//  1. load the execution;
//  2. if it is not PENDING, skip it (duplicate or stale job): no claim, no write;
//  3. otherwise hand it to the runner, whose first step is the atomic
//     PENDING -> RUNNING claim; only if that claim succeeds is the graph run;
//  4. classify the result from the authoritative status in PostgreSQL.
//
// Result.Requeue marks the pre-claim infrastructure failures after which the
// job must be returned to the queue rather than dropped.
func (p *ExecutionProcessor) Process(ctx context.Context, job queue.Job) Result {
	res := Result{ExecutionID: job.ExecutionID}
	if err := job.Validate(); err != nil {
		res.Outcome, res.Err = OutcomeError, err
		return res
	}
	if err := ctx.Err(); err != nil {
		res.Outcome, res.Err, res.Requeue = OutcomeNotAttempted, err, true
		return res
	}

	current, err := p.executions.Get(ctx, job.ExecutionID)
	switch {
	case errors.Is(err, execution.ErrExecutionNotFound):
		res.Outcome, res.Err = OutcomeNotFound, err
		return res
	case err != nil && ctx.Err() != nil:
		res.Outcome, res.Err, res.Requeue = OutcomeNotAttempted, err, true
		return res
	case err != nil:
		// Infrastructure failure before any claim attempt: the execution may
		// still be PENDING and this job may be its only one, so hand it back.
		res.Outcome, res.Err, res.Requeue = OutcomeError, fmt.Errorf("load execution %s: %w", job.ExecutionID, err), true
		return res
	}
	res.Status = current.Status
	if current.Status != execution.StatusPending {
		res.Outcome = OutcomeSkipped
		return res
	}

	_, runErr := p.runner.Run(ctx, job.ExecutionID)
	var (
		scheduled *execution.RetryScheduledError
		dead      *execution.DeadLetteredError
	)
	switch {
	case errors.Is(runErr, execution.ErrExecutionNotClaimable):
		// Another worker's claim won between our read and our claim.
		res.Outcome, res.Err = OutcomeClaimLost, runErr
		res.Status = p.status(ctx, job.ExecutionID)
		return res
	case errors.Is(runErr, execution.ErrRetryNotDue):
		res.Outcome, res.Err, res.Status = OutcomeNotDue, runErr, execution.StatusPending
		return res
	case errors.As(runErr, &scheduled):
		res.Outcome, res.Err = OutcomeRetryScheduled, runErr
		res.Status = p.status(ctx, job.ExecutionID)
		return res
	case errors.Is(runErr, execution.ErrLeaseLost):
		res.Outcome, res.Err = OutcomeLeaseLost, runErr
		res.Status = p.status(ctx, job.ExecutionID)
		return res
	case errors.As(runErr, &dead):
		res.DeadLetter, res.DeadLetterAttempt = dead.Reason, dead.Attempt
	}
	res.Err = runErr
	res.Status = p.status(ctx, job.ExecutionID)
	switch res.Status {
	case execution.StatusCompleted:
		res.Outcome = OutcomeCompleted
	case execution.StatusFailed:
		res.Outcome = OutcomeFailed
	case execution.StatusCancelled:
		res.Outcome = OutcomeCancelled
	case execution.StatusPending:
		// The claim was never applied (caller already cancelled, or the
		// claim write failed): nobody owns the execution, so the job must go
		// back to the queue.
		res.Outcome, res.Requeue = OutcomeNotAttempted, true
	default:
		// RUNNING after Run returned: claimed here but the final status could
		// not be recorded (database failure). Phase 8 leaves this execution
		// RUNNING with this worker's claim token.
		res.Outcome = OutcomeError
		if res.Err == nil {
			res.Err = fmt.Errorf("execution %s is %q after running", job.ExecutionID, res.Status)
		}
	}
	return res
}

// status re-reads the authoritative status on a bounded context that is not
// cancelled by the worker stopping (the run may just have been cancelled).
func (p *ExecutionProcessor) status(ctx context.Context, id uuid.UUID) execution.ExecutionStatus {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.readTimeout)
	defer cancel()
	e, err := p.executions.Get(rctx, id)
	if err != nil {
		return ""
	}
	return e.Status
}
