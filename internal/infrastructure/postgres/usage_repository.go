package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/usage"
)

// UsageRepository is the PostgreSQL usage.Repository (Phase M): read-only
// aggregates over executions, node executions and files of one workspace.
type UsageRepository struct {
	pool *pgxpool.Pool
}

var _ usage.Repository = (*UsageRepository)(nil)

// NewUsageRepository uses the Store's pool.
func NewUsageRepository(store *Store) *UsageRepository { return &UsageRepository{pool: store.Pool} }

// workspaceRuns are the executions of the workspace's workflows since $2.
const workspaceRuns = `
  FROM executions e
  JOIN workflows w ON w.id = e.workflow_id
  JOIN projects p ON p.id = w.project_id
 WHERE p.workspace_id = $1 AND e.created_at >= $2`

// WorkspaceUsage implements usage.Repository.
func (r *UsageRepository) WorkspaceUsage(ctx context.Context, ws uuid.UUID, since time.Time) (usage.Report, error) {
	rep := usage.Report{Since: since, Daily: []usage.Day{}, Workflows: []usage.WorkflowRuns{}}
	if err := r.pool.QueryRow(ctx, `SELECT count(*),
       count(*) FILTER (WHERE e.status = 'COMPLETED'),
       count(*) FILTER (WHERE e.status = 'FAILED'),
       count(*) FILTER (WHERE e.status = 'CANCELLED'),
       count(*) FILTER (WHERE e.status IN ('PENDING', 'RUNNING'))`+workspaceRuns, ws, since).
		Scan(&rep.Runs.Total, &rep.Runs.Completed, &rep.Runs.Failed, &rep.Runs.Cancelled, &rep.Runs.Active); err != nil {
		return usage.Report{}, fmt.Errorf("usage runs: %w", err)
	}

	rows, err := r.pool.Query(ctx, `SELECT to_char(e.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
       count(*) FILTER (WHERE e.status = 'COMPLETED'),
       count(*) FILTER (WHERE e.status = 'FAILED'),
       count(*) FILTER (WHERE e.status = 'CANCELLED')`+workspaceRuns+`
 GROUP BY day ORDER BY day`, ws, since)
	if err != nil {
		return usage.Report{}, fmt.Errorf("usage daily: %w", err)
	}
	for rows.Next() {
		var d usage.Day
		if err := rows.Scan(&d.Date, &d.Completed, &d.Failed, &d.Cancelled); err != nil {
			rows.Close()
			return usage.Report{}, err
		}
		rep.Daily = append(rep.Daily, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return usage.Report{}, err
	}

	// Token counts are what the AI steps reported (their "usage" output).
	if err := r.pool.QueryRow(ctx, `SELECT
       count(*) FILTER (WHERE n.node_type IN ('llm', 'transcribe_audio')),
       COALESCE(sum((n.output->'usage'->>'input_tokens')::numeric) FILTER (WHERE n.node_type = 'llm'), 0)::bigint,
       COALESCE(sum((n.output->'usage'->>'output_tokens')::numeric) FILTER (WHERE n.node_type = 'llm'), 0)::bigint,
       COALESCE(sum((n.output->'usage'->>'total_tokens')::numeric) FILTER (WHERE n.node_type = 'llm'), 0)::bigint,
       count(*) FILTER (WHERE n.node_type LIKE '%.%')
  FROM node_executions n
  JOIN executions e ON e.id = n.execution_id
  JOIN workflows w ON w.id = e.workflow_id
  JOIN projects p ON p.id = w.project_id
 WHERE p.workspace_id = $1 AND e.created_at >= $2 AND n.status = 'COMPLETED'`, ws, since).
		Scan(&rep.AI.Steps, &rep.AI.InputTokens, &rep.AI.OutputTokens, &rep.AI.TotalTokens, &rep.AppSteps); err != nil {
		return usage.Report{}, fmt.Errorf("usage steps: %w", err)
	}

	rows, err = r.pool.Query(ctx, `SELECT w.id, w.name, count(*), count(*) FILTER (WHERE e.status = 'FAILED'), max(e.created_at)`+workspaceRuns+`
 GROUP BY w.id, w.name ORDER BY count(*) DESC, max(e.created_at) DESC LIMIT 10`, ws, since)
	if err != nil {
		return usage.Report{}, fmt.Errorf("usage workflows: %w", err)
	}
	for rows.Next() {
		var x usage.WorkflowRuns
		if err := rows.Scan(&x.WorkflowID, &x.Name, &x.Runs, &x.Failed, &x.LastRunAt); err != nil {
			rows.Close()
			return usage.Report{}, err
		}
		rep.Workflows = append(rep.Workflows, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return usage.Report{}, err
	}

	if err := r.pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(size_bytes), 0)::bigint FROM files WHERE workspace_id = $1`, ws).
		Scan(&rep.Files.Count, &rep.Files.Bytes); err != nil {
		return usage.Report{}, fmt.Errorf("usage files: %w", err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM workflows w JOIN projects p ON p.id = w.project_id
 WHERE p.workspace_id = $1 AND w.deleted_at IS NULL`, ws).Scan(&rep.LiveWorkflows); err != nil {
		return usage.Report{}, fmt.Errorf("usage workflows count: %w", err)
	}
	return rep, nil
}
