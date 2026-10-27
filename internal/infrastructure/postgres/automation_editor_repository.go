package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"workflow-optimizer/internal/automation"
)

var _ automation.WorkflowStore = (*AutomationRepository)(nil)

const autoWorkflowColumns = "id,workspace_id,project_id,name,active_version_id,created_at,updated_at"

func scanAutoWorkflow(row pgx.Row) (automation.Workflow, error) {
	var w automation.Workflow
	err := row.Scan(&w.ID, &w.WorkspaceID, &w.ProjectID, &w.Name, &w.ActiveVersionID, &w.CreatedAt, &w.UpdatedAt)
	return w, autoErr(err)
}
func (s *AutomationRepository) Workflow(ctx context.Context, id uuid.UUID) (automation.Workflow, error) {
	return scanAutoWorkflow(s.pool.QueryRow(ctx, `SELECT `+autoWorkflowColumns+` FROM automation_workflows WHERE id=$1`, id))
}
func (s *AutomationRepository) ListWorkflows(ctx context.Context, ws uuid.UUID) ([]automation.Workflow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+autoWorkflowColumns+` FROM automation_workflows WHERE workspace_id=$1 ORDER BY updated_at DESC,id LIMIT 500`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.Workflow{}
	for rows.Next() {
		w, err := scanAutoWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) SetWorkflowProject(ctx context.Context, id, project uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `UPDATE automation_workflows w SET project_id=p.id FROM projects p WHERE w.id=$1 AND p.id=$2 AND p.workspace_id=w.workspace_id`, id, project)
	if err == nil && tag.RowsAffected() == 0 {
		return automation.ErrNotFound
	}
	return err
}
func (s *AutomationRepository) Versions(ctx context.Context, id uuid.UUID) ([]automation.WorkflowVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT body FROM automation_workflow_versions WHERE workflow_id=$1 ORDER BY version_number DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.WorkflowVersion{}
	for rows.Next() {
		var raw []byte
		var v automation.WorkflowVersion
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) Version(ctx context.Context, id uuid.UUID) (automation.WorkflowVersion, error) {
	var v automation.WorkflowVersion
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT body FROM automation_workflow_versions WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return v, autoErr(err)
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}
func (s *AutomationRepository) PublishVersion(ctx context.Context, v automation.WorkflowVersion, expected int) (automation.WorkflowVersion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	var revision int
	if err := tx.QueryRow(ctx, `SELECT revision FROM assistant_sessions WHERE id=$1 FOR UPDATE`, v.WorkflowID).Scan(&revision); err != nil {
		return v, autoErr(err)
	}
	if revision != expected {
		return v, automation.ErrConflict
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT body FROM automation_workflow_versions WHERE workflow_id=$1 AND revision=$2`, v.WorkflowID, expected).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &v)
		return v, err
	}
	if err != pgx.ErrNoRows {
		return v, err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(version_number),0)+1 FROM automation_workflow_versions WHERE workflow_id=$1`, v.WorkflowID).Scan(&v.VersionNumber); err != nil {
		return v, err
	}
	v.ID = uuid.New()
	v.Revision = expected
	v.Status = "PUBLISHED"
	v.CreatedAt = time.Now().UTC()
	raw, err = json.Marshal(v)
	if err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO automation_workflow_versions(id,workflow_id,version_number,revision,body) VALUES($1,$2,$3,$4,$5)`, v.ID, v.WorkflowID, v.VersionNumber, v.Revision, raw); err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, `UPDATE automation_workflows SET active_version_id=$2 WHERE id=$1`, v.WorkflowID, v.ID); err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}
func (s *AutomationRepository) Layout(ctx context.Context, id uuid.UUID) (automation.EditorLayout, error) {
	l := automation.EditorLayout{Positions: map[string]automation.Position{}}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT revision,positions FROM automation_editor_layout WHERE workflow_id=$1`, id).Scan(&l.Revision, &raw)
	if err == pgx.ErrNoRows {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	err = json.Unmarshal(raw, &l.Positions)
	return l, err
}
func (s *AutomationRepository) SaveLayout(ctx context.Context, id uuid.UUID, l automation.EditorLayout) (automation.EditorLayout, error) {
	if l.Positions == nil {
		l.Positions = map[string]automation.Position{}
	}
	raw, err := json.Marshal(l.Positions)
	if err != nil {
		return l, err
	}
	if l.Revision == 0 {
		err = s.pool.QueryRow(ctx, `INSERT INTO automation_editor_layout(workflow_id,revision,positions) VALUES($1,1,$2) ON CONFLICT DO NOTHING RETURNING revision`, id, raw).Scan(&l.Revision)
	} else {
		err = s.pool.QueryRow(ctx, `UPDATE automation_editor_layout SET revision=revision+1,positions=$3 WHERE workflow_id=$1 AND revision=$2 RETURNING revision`, id, l.Revision, raw).Scan(&l.Revision)
	}
	if err == pgx.ErrNoRows {
		return l, automation.ErrConflict
	}
	return l, err
}
func (s *AutomationRepository) SaveWorkflowTemplate(ctx context.Context, t automation.WorkflowTemplate) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO automation_workflow_templates(id,workspace_id,body) VALUES($1,$2,$3)`, t.ID, t.WorkspaceID, raw)
	return err
}
func (s *AutomationRepository) WorkflowTemplates(ctx context.Context, ws uuid.UUID) ([]automation.WorkflowTemplate, error) {
	rows, err := s.pool.Query(ctx, `SELECT body FROM automation_workflow_templates WHERE workspace_id=$1 ORDER BY created_at DESC`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []automation.WorkflowTemplate{}
	for rows.Next() {
		var raw []byte
		var t automation.WorkflowTemplate
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *AutomationRepository) WorkflowTemplate(ctx context.Context, id string) (automation.WorkflowTemplate, error) {
	var t automation.WorkflowTemplate
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT body FROM automation_workflow_templates WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return t, autoErr(err)
	}
	err = json.Unmarshal(raw, &t)
	return t, err
}

func (s *AutomationRepository) DeleteWorkflowTemplate(ctx context.Context, id string, ws uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM automation_workflow_templates WHERE id=$1 AND workspace_id=$2`, id, ws)
	if err == nil && tag.RowsAffected() == 0 {
		return automation.ErrNotFound
	}
	return err
}
