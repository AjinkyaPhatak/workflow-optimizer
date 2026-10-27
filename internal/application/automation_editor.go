package application

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/workspace"
)

func (s *AssistantService) workflowStore() (automation.WorkflowStore, error) {
	r, ok := s.Store.(automation.WorkflowStore)
	if !ok {
		return nil, ErrUnavailable
	}
	return r, nil
}
func (s *AssistantService) Catalog(ctx context.Context, user, ws uuid.UUID) ([]automation.ToolDescriptor, error) {
	if ws == uuid.Nil {
		return s.Tools, nil
	}
	if err := s.require(ctx, user, ws, workspace.ActionRead); err != nil {
		return nil, err
	}
	if s.ToolOptions != nil {
		return s.ToolOptions(ctx, ws, s.Tools)
	}
	return s.Tools, nil
}
func (s *AssistantService) Editor(ctx context.Context, user, id uuid.UUID) (automation.EditorDocument, error) {
	var out automation.EditorDocument
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return out, err
	}
	r, err := s.workflowStore()
	if err != nil {
		return out, err
	}
	out.Session = session
	out.Workflow, err = r.Workflow(ctx, id)
	if err != nil {
		return out, assistantError(err)
	}
	out.Layout, err = r.Layout(ctx, id)
	return out, assistantError(err)
}
func (s *AssistantService) Workflows(ctx context.Context, user, ws uuid.UUID) ([]automation.Workflow, error) {
	if err := s.require(ctx, user, ws, workspace.ActionRead); err != nil {
		return nil, err
	}
	r, err := s.workflowStore()
	if err != nil {
		return nil, err
	}
	out, err := r.ListWorkflows(ctx, ws)
	return out, assistantError(err)
}

type CreateAutomationWorkflow struct {
	WorkspaceID  uuid.UUID `json:"workspace_id"`
	ProjectID    uuid.UUID `json:"project_id"`
	CredentialID uuid.UUID `json:"credential_id"`
	Name         string    `json:"name"`
	TemplateID   string    `json:"template_id"`
}

func (s *AssistantService) CreateWorkflow(ctx context.Context, user uuid.UUID, in CreateAutomationWorkflow, projects *WorkflowService) (automation.EditorDocument, error) {
	var out automation.EditorDocument
	if err := s.require(ctx, user, in.WorkspaceID, workspace.ActionWrite); err != nil {
		return out, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "New workflow"
	}
	if len(name) > 255 {
		return out, &InvalidError{Message: "Use a workflow name of up to 255 characters."}
	}
	if in.ProjectID != uuid.Nil {
		p, err := projects.GetProject(ctx, user, in.ProjectID)
		if err != nil {
			return out, err
		}
		if p.WorkspaceID != in.WorkspaceID {
			return out, notFound("project")
		}
	}
	r, err := s.workflowStore()
	if err != nil {
		return out, err
	}
	plan := automation.EmptyPlan(name)
	layout := automation.EditorLayout{Positions: map[string]automation.Position{}}
	if in.TemplateID != "" {
		t, err := s.template(ctx, user, in.WorkspaceID, in.TemplateID)
		if err != nil {
			return out, err
		}
		plan = automation.ClonePlan(t.Plan)
		if in.Name != "" {
			plan.Name = name
		}
		layout = t.Layout
		layout.Revision = 0
	}
	now := time.Now().UTC()
	tools, err := s.Catalog(ctx, user, in.WorkspaceID)
	if err != nil {
		return out, err
	}
	check := automation.Check(automation.RefreshQuestionOptions(plan, tools), tools)
	session := automation.Session{ID: uuid.New(), WorkspaceID: in.WorkspaceID, UserID: user, Revision: 1, CredentialID: in.CredentialID, Messages: []automation.Message{}, Answers: map[string]any{}, Compilation: &check, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.CreateSession(ctx, session); err != nil {
		return out, assistantError(err)
	}
	if in.ProjectID != uuid.Nil {
		if err := r.SetWorkflowProject(ctx, session.ID, in.ProjectID); err != nil {
			return out, assistantError(err)
		}
	}
	if len(layout.Positions) > 0 {
		if _, err := r.SaveLayout(ctx, session.ID, layout); err != nil {
			return out, assistantError(err)
		}
	}
	return s.Editor(ctx, user, session.ID)
}
func (s *AssistantService) CheckPlan(ctx context.Context, user, id uuid.UUID, revision int, plan automation.Plan) (automation.Compilation, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return automation.Compilation{}, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return automation.Compilation{}, err
	}
	if revision != session.Revision {
		return automation.Compilation{}, assistantError(automation.ErrConflict)
	}
	tools, err := s.Catalog(ctx, user, session.WorkspaceID)
	if err != nil {
		return automation.Compilation{}, err
	}
	return automation.CheckAnswered(automation.InsertConversions(plan, tools), tools, session.Answers), nil
}
func (s *AssistantService) WorkflowVersions(ctx context.Context, user, id uuid.UUID) ([]automation.WorkflowVersion, error) {
	if _, err := s.Get(ctx, user, id); err != nil {
		return nil, err
	}
	r, err := s.workflowStore()
	if err != nil {
		return nil, err
	}
	out, err := r.Versions(ctx, id)
	return out, assistantError(err)
}
func (s *AssistantService) PublishWorkflow(ctx context.Context, user, id uuid.UUID, revision int) (automation.WorkflowVersion, error) {
	var out automation.WorkflowVersion
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return out, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionPublish); err != nil {
		return out, err
	}
	if session.Revision != revision {
		return out, assistantError(automation.ErrConflict)
	}
	if session.Compilation == nil {
		return out, &InvalidError{Message: "Add some steps before publishing."}
	}
	checked := *session.Compilation // Get already checked the current workspace catalog.
	if !checked.Ready {
		return out, &InvalidError{Message: "Finish the workflow checks and questions before publishing."}
	}
	plan, err := automation.Materialize(checked.Plan, session.Answers)
	if err != nil {
		return out, &InvalidError{Message: "Answer the remaining questions before publishing."}
	}
	r, err := s.workflowStore()
	if err != nil {
		return out, err
	}
	out = automation.WorkflowVersion{WorkflowID: id, WorkspaceID: session.WorkspaceID, Plan: plan, Answers: map[string]any{}, CredentialID: session.CredentialID}
	out, err = r.PublishVersion(ctx, out, revision)
	return out, assistantError(err)
}
func (s *AssistantService) RestoreWorkflow(ctx context.Context, user, id, versionID uuid.UUID, revision int) (automation.Session, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return session, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return session, err
	}
	if revision != session.Revision {
		return session, assistantError(automation.ErrConflict)
	}
	r, err := s.workflowStore()
	if err != nil {
		return session, err
	}
	v, err := r.Version(ctx, versionID)
	if err != nil {
		return session, assistantError(err)
	}
	if v.WorkflowID != id || v.WorkspaceID != session.WorkspaceID {
		return session, notFound("version")
	}
	tools, err := s.Catalog(ctx, user, session.WorkspaceID)
	if err != nil {
		return session, err
	}
	check := automation.CheckAnswered(automation.RefreshQuestionOptions(v.Plan, tools), tools, v.Answers)
	session.Compilation = &check
	session.Answers = v.Answers
	session.CredentialID = v.CredentialID
	session.Revision++
	return session, assistantError(s.Store.SaveSession(ctx, session, revision))
}
func (s *AssistantService) WorkflowLayout(ctx context.Context, user, id uuid.UUID) (automation.EditorLayout, error) {
	if _, err := s.Get(ctx, user, id); err != nil {
		return automation.EditorLayout{}, err
	}
	r, err := s.workflowStore()
	if err != nil {
		return automation.EditorLayout{}, err
	}
	out, err := r.Layout(ctx, id)
	return out, assistantError(err)
}
func (s *AssistantService) SaveWorkflowLayout(ctx context.Context, user, id uuid.UUID, l automation.EditorLayout) (automation.EditorLayout, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return l, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return l, err
	}
	if l.Revision < 0 || len(l.Positions) > 1000 {
		return l, &InvalidError{Message: "The canvas layout is too large or out of date."}
	}
	for id, p := range l.Positions {
		if len(id) > 100 || math.IsNaN(p.X) || math.IsNaN(p.Y) || math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) || math.Abs(p.X) > 1e7 || math.Abs(p.Y) > 1e7 {
			return l, &InvalidError{Message: "Use valid canvas positions."}
		}
	}
	r, err := s.workflowStore()
	if err != nil {
		return l, err
	}
	out, err := r.SaveLayout(ctx, id, l)
	return out, assistantError(err)
}
func (s *AssistantService) WorkflowTemplates(ctx context.Context, user, ws uuid.UUID) ([]automation.WorkflowTemplate, error) {
	if err := s.require(ctx, user, ws, workspace.ActionRead); err != nil {
		return nil, err
	}
	r, err := s.workflowStore()
	if err != nil {
		return nil, err
	}
	saved, err := r.WorkflowTemplates(ctx, ws)
	if err != nil {
		return nil, err
	}
	return append(s.BuiltinTemplates, saved...), nil
}
func (s *AssistantService) template(ctx context.Context, user, ws uuid.UUID, id string) (automation.WorkflowTemplate, error) {
	for _, t := range s.BuiltinTemplates {
		if t.ID == id {
			return t, nil
		}
	}
	r, err := s.workflowStore()
	if err != nil {
		return automation.WorkflowTemplate{}, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return automation.WorkflowTemplate{}, notFound("template")
	}
	t, err := r.WorkflowTemplate(ctx, id)
	if err != nil {
		return t, assistantError(err)
	}
	if t.WorkspaceID != ws {
		return t, notFound("template")
	}
	return t, s.require(ctx, user, ws, workspace.ActionRead)
}
func (s *AssistantService) SaveWorkflowTemplate(ctx context.Context, user, id uuid.UUID, name, description string) (automation.WorkflowTemplate, error) {
	var out automation.WorkflowTemplate
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return out, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return out, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 || len(description) > 2000 {
		return out, &InvalidError{Message: "Give the template a name of up to 255 characters and a short description."}
	}
	if session.Compilation == nil || len(session.Compilation.Plan.Steps) == 0 {
		return out, &InvalidError{Message: "Add some steps before saving a template."}
	}
	tools, err := s.Catalog(ctx, user, session.WorkspaceID)
	if err != nil {
		return out, err
	}
	check := automation.Check(session.Compilation.Plan, tools)
	if len(check.Diagnostics) > 0 {
		return out, &InvalidError{Message: "Finish checking the workflow before saving a template."}
	}
	r, err := s.workflowStore()
	if err != nil {
		return out, err
	}
	layout, err := r.Layout(ctx, id)
	if err != nil {
		return out, err
	}
	layout.Revision = 0
	out = automation.WorkflowTemplate{ID: uuid.NewString(), WorkspaceID: session.WorkspaceID, Name: name, Description: strings.TrimSpace(description), Plan: check.Plan, Layout: layout, CreatedAt: time.Now().UTC()}
	return out, assistantError(r.SaveWorkflowTemplate(ctx, out))
}

func (s *AssistantService) DeleteWorkflowTemplate(ctx context.Context, user, id uuid.UUID) error {
	r, err := s.workflowStore()
	if err != nil {
		return err
	}
	t, err := r.WorkflowTemplate(ctx, id.String())
	if err != nil {
		return assistantError(err)
	}
	if err := s.require(ctx, user, t.WorkspaceID, workspace.ActionDelete); err != nil {
		return err
	}
	return assistantError(r.DeleteWorkflowTemplate(ctx, id.String(), t.WorkspaceID))
}

func (s *AssistantService) WorkflowChoices(ctx context.Context, user, ws uuid.UUID) ([]automation.WorkflowChoice, error) {
	out := []automation.WorkflowChoice{}
	if ws == uuid.Nil {
		return out, nil
	}
	if err := s.require(ctx, user, ws, workspace.ActionRead); err != nil {
		return nil, err
	}
	r, ok := s.Store.(automation.WorkflowStore)
	if !ok {
		return out, nil
	}
	workflows, err := r.ListWorkflows(ctx, ws)
	if err != nil {
		return nil, err
	}
	for _, w := range workflows {
		if w.ActiveVersionID == nil {
			continue
		}
		v, err := r.Version(ctx, *w.ActiveVersionID)
		if err != nil {
			return nil, err
		}
		out = append(out, automation.WorkflowChoice{Ref: "version:" + v.ID.String(), WorkflowID: w.ID, Name: w.Name, VersionNumber: v.VersionNumber})
	}
	return out, nil
}
