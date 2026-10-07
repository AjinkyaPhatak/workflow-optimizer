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

// ExecutionEventRepository is the append-only execution_events store
// (migration 000006). It has no update or delete operation, and the table
// refuses both.
type ExecutionEventRepository struct {
	pool *pgxpool.Pool
}

var _ execution.ExecutionEventRepository = (*ExecutionEventRepository)(nil)

// NewExecutionEventRepository uses the Store's existing connection pool.
func NewExecutionEventRepository(store *Store) *ExecutionEventRepository {
	return &ExecutionEventRepository{pool: store.Pool}
}

const eventColumns = `id, execution_id, node_id, type, "timestamp", data`

func scanEvent(row pgx.Row) (execution.ExecutionEvent, error) {
	var (
		e    execution.ExecutionEvent
		typ  string
		data []byte
	)
	if err := row.Scan(&e.ID, &e.ExecutionID, &e.NodeID, &typ, &e.Timestamp, &data); err != nil {
		return execution.ExecutionEvent{}, err
	}
	e.Type = execution.EventType(typ)
	if err := json.Unmarshal(data, &e.Data); err != nil {
		return execution.ExecutionEvent{}, fmt.Errorf("decode event data: %w", err)
	}
	return e, nil
}

// Append stores the event; the database assigns the timestamp.
func (r *ExecutionEventRepository) Append(ctx context.Context, e execution.ExecutionEvent) (execution.ExecutionEvent, error) {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	data, err := json.Marshal(e.Data)
	if err != nil {
		return execution.ExecutionEvent{}, fmt.Errorf("encode event data: %w", err)
	}
	stored, err := scanEvent(r.pool.QueryRow(ctx, `
INSERT INTO execution_events (id, execution_id, node_id, type, data) VALUES ($1, $2, $3, $4, $5)
RETURNING `+eventColumns, e.ID, e.ExecutionID, e.NodeID, string(e.Type), data))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		return execution.ExecutionEvent{}, fmt.Errorf("%w: %s", execution.ErrExecutionNotFound, e.ExecutionID)
	}
	if err != nil {
		return execution.ExecutionEvent{}, fmt.Errorf("append execution event: %w", err)
	}
	return stored, nil
}

// ListByExecution returns one page of the execution's events in
// chronological order ("timestamp", then insertion order) and the total.
func (r *ExecutionEventRepository) ListByExecution(ctx context.Context, executionID uuid.UUID, q execution.EventQuery) ([]execution.ExecutionEvent, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = 50
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM execution_events WHERE execution_id = $1`, executionID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count execution events: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT `+eventColumns+` FROM execution_events WHERE execution_id = $1
 ORDER BY "timestamp", seq LIMIT $2 OFFSET $3`, executionID, q.PageSize, (q.Page-1)*q.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list execution events: %w", err)
	}
	defer rows.Close()
	out := []execution.ExecutionEvent{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// ExecutionListRepository lists a workflow's executions for navigation
// (Phase 14). It reads through ExecutionRepository.Get, so list entries are
// exactly the execution snapshots the rest of the API returns.
type ExecutionListRepository struct {
	pool *pgxpool.Pool
}

// NewExecutionListRepository uses the Store's existing connection pool.
func NewExecutionListRepository(store *Store) *ExecutionListRepository {
	return &ExecutionListRepository{pool: store.Pool}
}

// ListByWorkflow returns one page of a workflow's executions, newest first,
// and the total.
func (r *ExecutionListRepository) ListByWorkflow(ctx context.Context, workflowID uuid.UUID, limit, offset int) ([]execution.Execution, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM executions WHERE workflow_id = $1`, workflowID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count executions: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT id FROM executions WHERE workflow_id = $1 ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`,
		workflowID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list executions: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, 0, fmt.Errorf("list executions: %w", err)
	}
	execs := NewExecutionRepository(&Store{Pool: r.pool})
	out := make([]execution.Execution, 0, len(ids))
	for _, id := range ids {
		e, err := execs.Get(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, nil
}
