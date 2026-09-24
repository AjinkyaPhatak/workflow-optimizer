package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/execution"
)

// ExecutionRepository is the PostgreSQL execution.ExecutionRepository.
//
// Every state change is one SQL statement: a conditional UPDATE guarded by
// "WHERE status = <from>" inside a data-modifying CTE that also appends the
// execution_status_history row. The statement is atomic, so a transition can
// never be persisted without its history, and the row lock taken by UPDATE
// makes concurrent claims across processes serialize on PostgreSQL: exactly one
// PENDING -> RUNNING can match.
type ExecutionRepository struct {
	pool *pgxpool.Pool
}

var _ execution.ExecutionRepository = (*ExecutionRepository)(nil)

// NewExecutionRepository uses the Store's existing connection pool.
func NewExecutionRepository(store *Store) *ExecutionRepository {
	return &ExecutionRepository{pool: store.Pool}
}

const createExecutionSQL = `
WITH inserted AS (
    INSERT INTO executions (id, workflow_id, workflow_version_id, status, input)
    VALUES ($1, $2, $3, 'PENDING', $4)
    RETURNING id, created_at
)
INSERT INTO execution_status_history (execution_id, from_status, to_status, created_at, metadata)
SELECT id, NULL, 'PENDING', created_at, '{}'::jsonb FROM inserted`

// Create inserts a PENDING execution and its creation history record.
func (r *ExecutionRepository) Create(ctx context.Context, e execution.Execution) error {
	if e.ID == uuid.Nil || e.WorkflowID == uuid.Nil || e.WorkflowVersionID == uuid.Nil {
		return fmt.Errorf("%w: execution, workflow and version IDs are required", execution.ErrInvalidExecution)
	}
	if e.Status != execution.StatusPending || e.StartedAt != nil || e.FinishedAt != nil || e.Output != nil || e.Error != nil {
		return fmt.Errorf("%w: executions are created PENDING without start, finish, output or error", execution.ErrInvalidExecution)
	}
	input, err := encodeJSON(e.Input)
	if err != nil {
		return fmt.Errorf("%w: encode input: %v", execution.ErrInvalidExecution, err)
	}
	if _, err := r.pool.Exec(ctx, createExecutionSQL, e.ID, e.WorkflowID, e.WorkflowVersionID, input); err != nil {
		return mapExecutionWriteError(err)
	}
	return nil
}

const selectExecutionSQL = `
SELECT id, workflow_id, workflow_version_id, status, input, output, error,
       created_at, updated_at, started_at, finished_at
FROM executions WHERE id = $1`

// Get loads one execution.
func (r *ExecutionRepository) Get(ctx context.Context, id uuid.UUID) (execution.Execution, error) {
	var (
		e                   execution.Execution
		status              string
		input, output, eerr []byte
	)
	err := r.pool.QueryRow(ctx, selectExecutionSQL, id).Scan(
		&e.ID, &e.WorkflowID, &e.WorkflowVersionID, &status, &input, &output, &eerr,
		&e.CreatedAt, &e.UpdatedAt, &e.StartedAt, &e.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Execution{}, fmt.Errorf("%w: %s", execution.ErrExecutionNotFound, id)
	}
	if err != nil {
		return execution.Execution{}, fmt.Errorf("get execution %s: %w", id, err)
	}
	e.Status = execution.ExecutionStatus(status)
	if e.Input, err = decodeMap(input); err != nil {
		return execution.Execution{}, err
	}
	if e.Output, err = decodeMap(output); err != nil {
		return execution.Execution{}, err
	}
	if e.Error, err = decodeError(eerr); err != nil {
		return execution.Execution{}, err
	}
	return e, nil
}

const transitionExecutionSQL = `
WITH updated AS (
    UPDATE executions SET
        status      = $3::text,
        updated_at  = GREATEST(now(), updated_at),
        started_at  = CASE WHEN $3::text = 'RUNNING' THEN now() ELSE started_at END,
        finished_at = CASE WHEN $3::text IN ('COMPLETED', 'FAILED', 'CANCELLED')
                           THEN GREATEST(now(), started_at) ELSE finished_at END,
        output      = CASE WHEN $3::text = 'COMPLETED' THEN $4::jsonb ELSE output END,
        error       = COALESCE($5::jsonb, error)
    WHERE id = $1 AND status = $2::text
    RETURNING id
)
INSERT INTO execution_status_history (execution_id, from_status, to_status, metadata)
SELECT id, $2::text, $3::text, $6::jsonb FROM updated`

// Transition performs the atomic compare-and-set described on the type.
func (r *ExecutionRepository) Transition(ctx context.Context, id uuid.UUID, from, to execution.ExecutionStatus, u execution.TransitionUpdate) error {
	if !from.CanTransitionTo(to) {
		return &execution.TransitionError{ExecutionID: id, From: from, To: to}
	}
	output, err := encodeJSON(u.Output)
	if err != nil {
		return fmt.Errorf("%w: encode output: %v", execution.ErrInvalidExecution, err)
	}
	execErr, err := encodeJSON(u.Error)
	if err != nil {
		return fmt.Errorf("%w: encode error: %v", execution.ErrInvalidExecution, err)
	}
	meta := u.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metadata, err := encodeJSON(meta)
	if err != nil {
		return fmt.Errorf("%w: encode metadata: %v", execution.ErrInvalidExecution, err)
	}
	tag, err := r.pool.Exec(ctx, transitionExecutionSQL, id, string(from), string(to), output, execErr, metadata)
	if err != nil {
		return mapExecutionWriteError(err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: execution %s is not %s", execution.ErrTransitionConflict, id, from)
	}
	return nil
}

const historySQL = `
SELECT id, execution_id, from_status, to_status, created_at, metadata
FROM execution_status_history
WHERE execution_id = $1
ORDER BY created_at,
         CASE to_status WHEN 'PENDING' THEN 0 WHEN 'RUNNING' THEN 1 ELSE 2 END`

// History returns the execution's append-only transition history.
func (r *ExecutionRepository) History(ctx context.Context, id uuid.UUID) ([]execution.StatusTransition, error) {
	rows, err := r.pool.Query(ctx, historySQL, id)
	if err != nil {
		return nil, fmt.Errorf("query execution history %s: %w", id, err)
	}
	defer rows.Close()
	var out []execution.StatusTransition
	for rows.Next() {
		var (
			t    execution.StatusTransition
			from *string
			to   string
			meta []byte
		)
		if err := rows.Scan(&t.ID, &t.ExecutionID, &from, &to, &t.CreatedAt, &meta); err != nil {
			return nil, fmt.Errorf("scan execution history: %w", err)
		}
		if from != nil {
			s := execution.ExecutionStatus(*from)
			t.From = &s
		}
		t.To = execution.ExecutionStatus(to)
		if t.Metadata, err = decodeMap(meta); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NodeExecutionRepository is the PostgreSQL execution.NodeExecutionRepository.
type NodeExecutionRepository struct {
	pool *pgxpool.Pool
}

var _ execution.NodeExecutionRepository = (*NodeExecutionRepository)(nil)

// NewNodeExecutionRepository uses the Store's existing connection pool.
func NewNodeExecutionRepository(store *Store) *NodeExecutionRepository {
	return &NodeExecutionRepository{pool: store.Pool}
}

// Create inserts a PENDING node execution owned by an existing execution.
func (r *NodeExecutionRepository) Create(ctx context.Context, n execution.NodeExecution) error {
	if n.ID == uuid.Nil || n.ExecutionID == uuid.Nil || n.NodeID == "" || n.NodeType == "" {
		return fmt.Errorf("%w: node execution ID, execution ID, node ID and node type are required", execution.ErrInvalidExecution)
	}
	if n.Status != execution.NodeStatusPending || n.StartedAt != nil || n.FinishedAt != nil || n.Output != nil || n.Error != nil {
		return fmt.Errorf("%w: node executions are created PENDING without start, finish, output or error", execution.ErrInvalidExecution)
	}
	input, err := encodeJSON(n.Input)
	if err != nil {
		return fmt.Errorf("%w: encode node input: %v", execution.ErrInvalidExecution, err)
	}
	_, err = r.pool.Exec(ctx, `
INSERT INTO node_executions (id, execution_id, node_id, node_type, status, input)
VALUES ($1, $2, $3, $4, 'PENDING', $5)`, n.ID, n.ExecutionID, n.NodeID, n.NodeType, input)
	if err != nil {
		return mapNodeWriteError(err)
	}
	return nil
}

const nodeColumns = `id, execution_id, node_id, node_type, status, input, output, error,
       created_at, updated_at, started_at, finished_at`

func scanNode(row pgx.Row) (execution.NodeExecution, error) {
	var (
		n                   execution.NodeExecution
		status              string
		input, output, eerr []byte
	)
	if err := row.Scan(&n.ID, &n.ExecutionID, &n.NodeID, &n.NodeType, &status, &input, &output, &eerr,
		&n.CreatedAt, &n.UpdatedAt, &n.StartedAt, &n.FinishedAt); err != nil {
		return execution.NodeExecution{}, err
	}
	n.Status = execution.NodeExecutionStatus(status)
	var err error
	if n.Input, err = decodeMap(input); err != nil {
		return execution.NodeExecution{}, err
	}
	if n.Output, err = decodeMap(output); err != nil {
		return execution.NodeExecution{}, err
	}
	if n.Error, err = decodeError(eerr); err != nil {
		return execution.NodeExecution{}, err
	}
	return n, nil
}

// Get loads one node execution.
func (r *NodeExecutionRepository) Get(ctx context.Context, id uuid.UUID) (execution.NodeExecution, error) {
	n, err := scanNode(r.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM node_executions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.NodeExecution{}, fmt.Errorf("%w: %s", execution.ErrNodeExecutionNotFound, id)
	}
	if err != nil {
		return execution.NodeExecution{}, fmt.Errorf("get node execution %s: %w", id, err)
	}
	return n, nil
}

const transitionNodeSQL = `
UPDATE node_executions SET
    status      = $3::text,
    updated_at  = GREATEST(now(), updated_at),
    started_at  = CASE WHEN $3::text = 'RUNNING' THEN now() ELSE started_at END,
    finished_at = CASE WHEN $3::text IN ('COMPLETED', 'FAILED', 'SKIPPED')
                       THEN GREATEST(now(), started_at) ELSE finished_at END,
    input       = CASE WHEN $3::text = 'RUNNING' THEN COALESCE($4::jsonb, input) ELSE input END,
    output      = CASE WHEN $3::text = 'COMPLETED' THEN $5::jsonb ELSE output END,
    error       = COALESCE($6::jsonb, error)
WHERE id = $1 AND status = $2::text`

// Transition is the node-level atomic compare-and-set.
func (r *NodeExecutionRepository) Transition(ctx context.Context, id uuid.UUID, from, to execution.NodeExecutionStatus, u execution.NodeTransitionUpdate) error {
	if !from.CanTransitionTo(to) {
		return &execution.NodeTransitionError{NodeExecutionID: id, From: from, To: to}
	}
	input, err := encodeJSON(u.Input)
	if err != nil {
		return fmt.Errorf("%w: encode node input: %v", execution.ErrInvalidExecution, err)
	}
	output, err := encodeJSON(u.Output)
	if err != nil {
		return fmt.Errorf("%w: encode node output: %v", execution.ErrInvalidExecution, err)
	}
	nodeErr, err := encodeJSON(u.Error)
	if err != nil {
		return fmt.Errorf("%w: encode node error: %v", execution.ErrInvalidExecution, err)
	}
	tag, err := r.pool.Exec(ctx, transitionNodeSQL, id, string(from), string(to), input, output, nodeErr)
	if err != nil {
		return mapNodeWriteError(err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: node execution %s is not %s", execution.ErrTransitionConflict, id, from)
	}
	return nil
}

// ListByExecution returns an execution's node records in start order.
func (r *NodeExecutionRepository) ListByExecution(ctx context.Context, executionID uuid.UUID) ([]execution.NodeExecution, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+nodeColumns+` FROM node_executions
WHERE execution_id = $1 ORDER BY created_at, started_at NULLS LAST, node_id`, executionID)
	if err != nil {
		return nil, fmt.Errorf("list node executions %s: %w", executionID, err)
	}
	defer rows.Close()
	var out []execution.NodeExecution
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node execution: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// encodeJSON marshals v for a JSONB parameter; a nil map/pointer becomes SQL
// NULL. encoding/json sorts map keys, so the encoding is deterministic.
func encodeJSON(v any) ([]byte, error) {
	switch typed := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		if typed == nil {
			return nil, nil
		}
	case *execution.ExecutionError:
		if typed == nil {
			return nil, nil
		}
	}
	return json.Marshal(v)
}

func decodeMap(raw []byte) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode JSONB object: %w", err)
	}
	return m, nil
}

func decodeError(raw []byte) (*execution.ExecutionError, error) {
	if raw == nil {
		return nil, nil
	}
	var e execution.ExecutionError
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("decode execution error: %w", err)
	}
	return &e, nil
}

const (
	pgForeignKeyViolation = "23503"
	pgUniqueViolation     = "23505"
	pgCheckViolation      = "23514"
)

func mapExecutionWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("write execution: %w", err)
	}
	switch pgErr.Code {
	case pgForeignKeyViolation:
		switch pgErr.ConstraintName {
		case "executions_workflow_id_fkey":
			return fmt.Errorf("%w: %s", execution.ErrWorkflowNotFound, pgErr.Message)
		case "executions_workflow_version_id_fkey":
			return fmt.Errorf("%w: %s", execution.ErrWorkflowVersionNotFound, pgErr.Message)
		case "executions_version_belongs_to_workflow_fkey":
			return fmt.Errorf("%w: %s", execution.ErrWorkflowVersionMismatch, pgErr.Message)
		}
	case pgUniqueViolation:
		return fmt.Errorf("%w: %s", execution.ErrInvalidExecution, pgErr.Message)
	case pgCheckViolation:
		return fmt.Errorf("%w: %s", execution.ErrInvalidTransition, pgErr.Message)
	}
	return fmt.Errorf("write execution: %w", err)
}

func mapNodeWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("write node execution: %w", err)
	}
	switch pgErr.Code {
	case pgForeignKeyViolation:
		return fmt.Errorf("%w: %s", execution.ErrExecutionNotFound, pgErr.Message)
	case pgUniqueViolation:
		return fmt.Errorf("%w: %s", execution.ErrInvalidExecution, pgErr.Message)
	case pgCheckViolation:
		return fmt.Errorf("%w: %s", execution.ErrInvalidTransition, pgErr.Message)
	}
	return fmt.Errorf("write node execution: %w", err)
}
