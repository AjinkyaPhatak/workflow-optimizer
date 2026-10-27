package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"workflow-optimizer/internal/automation"
)

type AutomationRepository struct{ pool *pgxpool.Pool }

func NewAutomationRepository(s *Store) *AutomationRepository { return &AutomationRepository{s.Pool} }

var _ automation.Store = (*AutomationRepository)(nil)

func autoErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return automation.ErrNotFound
	}
	return err
}
func (s *AutomationRepository) CreateSession(ctx context.Context, v automation.Session) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO assistant_sessions(id,workspace_id,user_id,revision,body) VALUES($1,$2,$3,$4,$5)`, v.ID, v.WorkspaceID, v.UserID, v.Revision, b); err != nil {
		return err
	}
	name := "New workflow"
	if v.Compilation != nil && v.Compilation.Plan.Name != "" {
		name = v.Compilation.Plan.Name
	}
	if _, err = tx.Exec(ctx, `INSERT INTO automation_workflows(id,workspace_id,name) VALUES($1,$2,$3)`, v.ID, v.WorkspaceID, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *AutomationRepository) Session(ctx context.Context, id uuid.UUID) (automation.Session, error) {
	var v automation.Session
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT body FROM assistant_sessions WHERE id=$1`, id).Scan(&b)
	if err != nil {
		return v, autoErr(err)
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *AutomationRepository) SaveSession(ctx context.Context, v automation.Session, expected int) error {
	v.UpdatedAt = time.Now().UTC()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE assistant_sessions SET body=$2,revision=$3,updated_at=now() WHERE id=$1 AND revision=$4`, v.ID, b, v.Revision, expected)
	if err == nil && tag.RowsAffected() == 0 {
		return automation.ErrConflict
	}
	if err != nil {
		return err
	}
	if v.Compilation != nil && v.Compilation.Plan.Name != "" {
		if _, err = tx.Exec(ctx, `UPDATE automation_workflows SET name=$2,updated_at=now() WHERE id=$1`, v.ID, v.Compilation.Plan.Name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *AutomationRepository) ListSessions(ctx context.Context, ws uuid.UUID) ([]automation.Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT body FROM assistant_sessions WHERE workspace_id=$1 ORDER BY updated_at DESC LIMIT 50`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.Session{}
	for rows.Next() {
		var b []byte
		var v automation.Session
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) CreateRun(ctx context.Context, v automation.Run) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO automation_runs(id,workspace_id,user_id,body) VALUES($1,$2,$3,$4)`, v.ID, v.WorkspaceID, v.UserID, b)
	return err
}
func (s *AutomationRepository) Run(ctx context.Context, id uuid.UUID) (automation.Run, error) {
	var v automation.Run
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT body FROM automation_runs WHERE id=$1`, id).Scan(&b)
	if err != nil {
		return v, autoErr(err)
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *AutomationRepository) ListRuns(ctx context.Context, ws uuid.UUID) ([]automation.Run, error) {
	rows, err := s.pool.Query(ctx, `SELECT body FROM automation_runs WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 50`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.Run{}
	for rows.Next() {
		var b []byte
		var v automation.Run
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) UpdateRun(ctx context.Context, id uuid.UUID, status string, outputs map[string]map[string]any, q *automation.Question, message string) error {
	// Patch fields atomically so concurrent progress cannot erase immutable inputs.
	patch := map[string]any{"status": status, "question": q, "error": message, "updated_at": time.Now().UTC()}
	if outputs != nil {
		patch["outputs"] = outputs
	}
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE automation_runs SET body=body || $2::jsonb,updated_at=now() WHERE id=$1`, id, b)
	if err == nil && tag.RowsAffected() == 0 {
		return automation.ErrNotFound
	}
	return err
}
func (s *AutomationRepository) AddEvent(ctx context.Context, e automation.Event) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO automation_events(run_id,kind,step_id,message) VALUES($1,$2,$3,$4)`, e.RunID, e.Kind, e.StepID, e.Message)
	return err
}
func (s *AutomationRepository) Events(ctx context.Context, id uuid.UUID, after int64) ([]automation.Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,run_id,kind,step_id,message,created_at FROM automation_events WHERE run_id=$1 AND id>$2 ORDER BY id LIMIT 200`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.Event{}
	for rows.Next() {
		var e automation.Event
		if err := rows.Scan(&e.ID, &e.RunID, &e.Kind, &e.StepID, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) ClaimAction(ctx context.Context, c automation.ToolCall) (bool, *automation.ToolResult, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO automation_actions(run_id,operation_id,tool,status) VALUES($1,$2,$3,'STARTED') ON CONFLICT DO NOTHING`, c.RunID, c.OperationID, c.Tool)
	if err != nil {
		return false, nil, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil, nil
	}
	var status string
	var b []byte
	if err := s.pool.QueryRow(ctx, `SELECT status,result FROM automation_actions WHERE run_id=$1 AND operation_id=$2`, c.RunID, c.OperationID).Scan(&status, &b); err != nil {
		return false, nil, err
	}
	if status == "COMPLETED" {
		var result automation.ToolResult
		if err := json.Unmarshal(b, &result); err != nil {
			return false, nil, err
		}
		return false, &result, nil
	}
	return false, nil, nil // STARTED/UNCERTAIN are deliberately never blindly resent.
}
func (s *AutomationRepository) FinishAction(ctx context.Context, c automation.ToolCall, result automation.ToolResult, status string) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE automation_actions SET result=$3,status=$4,provider_receipt=COALESCE($3::jsonb->'outputs'->'receipt'->>'id',''),updated_at=now() WHERE run_id=$1 AND operation_id=$2`, c.RunID, c.OperationID, b, status)
	return err
}
func (s *AutomationRepository) EnqueueEvent(ctx context.Context, event automation.SourceEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO automation_outbox(event_id,body) VALUES($1,$2) ON CONFLICT DO NOTHING`, event.ID, body)
	return err
}
func (s *AutomationRepository) PendingEvents(ctx context.Context, limit int) ([]automation.SourceEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT body FROM automation_outbox WHERE pending ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.SourceEvent{}
	for rows.Next() {
		var body []byte
		var event automation.SourceEvent
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) CompleteEvent(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE automation_outbox SET pending=false WHERE event_id=$1`, id)
	return err
}
func (s *AutomationRepository) SaveAutomation(ctx context.Context, v automation.Automation) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO automations(id,workspace_id,user_id,body) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET body=EXCLUDED.body WHERE automations.workspace_id=EXCLUDED.workspace_id`, v.ID, v.WorkspaceID, v.UserID, b)
	return err
}
func (s *AutomationRepository) Automation(ctx context.Context, id uuid.UUID) (automation.Automation, error) {
	var v automation.Automation
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT body FROM automations WHERE id=$1`, id).Scan(&b)
	if err != nil {
		return v, autoErr(err)
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *AutomationRepository) DeleteAutomation(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM automations WHERE id=$1`, id)
	return err
}
func (s *AutomationRepository) ListAutomations(ctx context.Context, ws uuid.UUID) ([]automation.Automation, error) {
	rows, err := s.pool.Query(ctx, `SELECT body FROM automations WHERE workspace_id=$1 ORDER BY created_at DESC`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.Automation{}
	for rows.Next() {
		var b []byte
		var v automation.Automation
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) AcceptTrigger(ctx context.Context, id uuid.UUID, eventID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO automation_triggers(automation_id,event_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, eventID)
	return tag.RowsAffected() == 1, err
}
