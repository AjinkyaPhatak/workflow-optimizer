package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/templates"
	"workflow-optimizer/internal/workflow"
	"workflow-optimizer/internal/workspace"
)

// WorkflowStore persists projects, workflows and versions.
type WorkflowStore interface {
	CreateProject(ctx context.Context, p workflow.Project) (workflow.Project, error)
	FindProject(ctx context.Context, id uuid.UUID) (workflow.Project, error)
	ListProjects(ctx context.Context, workspaceID uuid.UUID) ([]workflow.Project, error)

	CreateWorkflow(ctx context.Context, wf workflow.Workflow) (workflow.Workflow, error)
	// FindWorkflow returns a live workflow and its workspace.
	FindWorkflow(ctx context.Context, id uuid.UUID) (workflow.Workflow, uuid.UUID, error)
	ListWorkflows(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]workflow.Workflow, int, error)
	UpdateWorkflow(ctx context.Context, wf workflow.Workflow) (workflow.Workflow, error)
	DeleteWorkflow(ctx context.Context, id uuid.UUID) error

	CreateVersion(ctx context.Context, workflowID uuid.UUID, definition json.RawMessage, createdBy uuid.UUID) (workflow.Version, error)
	FindVersion(ctx context.Context, workflowID, versionID uuid.UUID) (workflow.Version, error)
	ListVersions(ctx context.Context, workflowID uuid.UUID, limit, offset int) ([]workflow.Version, int, error)
	PublishVersion(ctx context.Context, workflowID, versionID uuid.UUID, check func(workflow.Version) error) (workflow.Version, error)
}

// GraphValidator is the existing workflow.GraphValidator.
type GraphValidator interface {
	ValidateWithOptions(workflow.Definition, workflow.ValidationOptions) workflow.ValidationResult
}

// Page selects a slice of a list.
type Page struct {
	Number int // 1-based
	Size   int
}

func (p Page) offset() int { return (p.Number - 1) * p.Size }

// WorkflowService manages projects, workflows and immutable versions.
type WorkflowService struct {
	access    *Access
	store     WorkflowStore
	validator GraphValidator
	// credentials, when set, makes saving check that every literal
	// credential_id names a credential of the workflow's workspace.
	credentials CredentialLocator
}

// WithCredentialCheck makes CreateVersion refuse credential references to
// credentials that do not exist in the workflow's workspace (Phase C2).
func (s *WorkflowService) WithCredentialCheck(locator CredentialLocator) *WorkflowService {
	s.credentials = locator
	return s
}

// checkCredentials reports node credential references that are not
// credentials of workspaceID. References that are not literal IDs are left
// to the validator and to run time.
func (s *WorkflowService) checkCredentials(ctx context.Context, workspaceID uuid.UUID, def workflow.Definition) error {
	if s.credentials == nil {
		return nil
	}
	var problems []workflow.ValidationError
	for _, n := range def.Nodes {
		raw, _ := n.Config[node.CredentialConfigField].(string)
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		c, err := s.credentials.Get(ctx, id)
		if err != nil && !errors.Is(err, credential.ErrNotFound) {
			return err
		}
		if err != nil || c.WorkspaceID != workspaceID {
			problems = append(problems, workflow.ValidationError{Code: workflow.ErrInvalidNodeConfig,
				Message: "credential not found in this workspace", NodeID: n.ID, Port: node.CredentialConfigField})
		}
	}
	if len(problems) > 0 {
		return &ValidationFailedError{Errors: problems}
	}
	return nil
}

// NewWorkflowService wires the authorizer, the store and the graph validator.
func NewWorkflowService(access *Access, store WorkflowStore, validator GraphValidator) *WorkflowService {
	return &WorkflowService{access: access, store: store, validator: validator}
}

// --- projects ---------------------------------------------------------------

// CreateProject creates a project in the workspace.
func (s *WorkflowService) CreateProject(ctx context.Context, user, workspaceID uuid.UUID, name string, description *string) (workflow.Project, error) {
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionManageProjects, "workspace"); err != nil {
		return workflow.Project{}, err
	}
	return s.store.CreateProject(ctx, workflow.Project{WorkspaceID: workspaceID, Name: name, Description: description})
}

// ListProjects lists the workspace's projects.
func (s *WorkflowService) ListProjects(ctx context.Context, user, workspaceID uuid.UUID) ([]workflow.Project, error) {
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionRead, "workspace"); err != nil {
		return nil, err
	}
	return s.store.ListProjects(ctx, workspaceID)
}

// project loads a project the user may act on.
func (s *WorkflowService) project(ctx context.Context, user, id uuid.UUID, action workspace.Action) (workflow.Project, error) {
	p, err := s.store.FindProject(ctx, id)
	if errors.Is(err, workflow.ErrProjectNotFound) {
		return workflow.Project{}, notFound("project")
	}
	if err != nil {
		return workflow.Project{}, err
	}
	if _, err := s.access.Require(ctx, user, p.WorkspaceID, action, "project"); err != nil {
		return workflow.Project{}, err
	}
	return p, nil
}

// GetProject returns a project.
func (s *WorkflowService) GetProject(ctx context.Context, user, id uuid.UUID) (workflow.Project, error) {
	return s.project(ctx, user, id, workspace.ActionRead)
}

// --- workflows --------------------------------------------------------------

// workflow loads a live workflow the user may act on.
func (s *WorkflowService) workflow(ctx context.Context, user, id uuid.UUID, action workspace.Action) (workflow.Workflow, uuid.UUID, error) {
	wf, ws, err := s.store.FindWorkflow(ctx, id)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return workflow.Workflow{}, uuid.Nil, notFound("workflow")
	}
	if err != nil {
		return workflow.Workflow{}, uuid.Nil, err
	}
	if _, err := s.access.Require(ctx, user, ws, action, "workflow"); err != nil {
		return workflow.Workflow{}, uuid.Nil, err
	}
	return wf, ws, nil
}

// CreateWorkflow creates workflow metadata only (no version, no execution).
func (s *WorkflowService) CreateWorkflow(ctx context.Context, user, projectID uuid.UUID, name string, description *string) (workflow.Workflow, error) {
	if _, err := s.project(ctx, user, projectID, workspace.ActionWrite); err != nil {
		return workflow.Workflow{}, err
	}
	wf, err := s.store.CreateWorkflow(ctx, workflow.Workflow{ProjectID: projectID, Name: name, Description: description})
	if errors.Is(err, workflow.ErrProjectNotFound) {
		return workflow.Workflow{}, notFound("project")
	}
	return wf, err
}

// CreateWorkflowFromTemplate creates a workflow whose first version is a
// DRAFT copy of the template's definition (fresh node IDs). The draft goes
// through CreateVersion, so it is validated and stored like any other.
func (s *WorkflowService) CreateWorkflowFromTemplate(ctx context.Context, user, projectID uuid.UUID, name string, description *string, templateID string) (workflow.Workflow, workflow.Version, error) {
	def, err := templates.Instantiate(templateID)
	if errors.Is(err, templates.ErrNotFound) {
		return workflow.Workflow{}, workflow.Version{}, &InvalidError{Message: "unknown template_id"}
	}
	if err != nil {
		return workflow.Workflow{}, workflow.Version{}, err
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return workflow.Workflow{}, workflow.Version{}, err
	}
	wf, err := s.CreateWorkflow(ctx, user, projectID, name, description)
	if err != nil {
		return workflow.Workflow{}, workflow.Version{}, err
	}
	v, err := s.CreateVersion(ctx, user, wf.ID, raw)
	if err != nil {
		return wf, workflow.Version{}, err
	}
	return wf, v, nil
}

// ListWorkflows lists one page of the project's workflows.
func (s *WorkflowService) ListWorkflows(ctx context.Context, user, projectID uuid.UUID, page Page) ([]workflow.Workflow, int, error) {
	if _, err := s.project(ctx, user, projectID, workspace.ActionRead); err != nil {
		return nil, 0, err
	}
	return s.store.ListWorkflows(ctx, projectID, page.Size, page.offset())
}

// GetWorkflow returns the workflow.
func (s *WorkflowService) GetWorkflow(ctx context.Context, user, id uuid.UUID) (workflow.Workflow, error) {
	wf, _, err := s.workflow(ctx, user, id, workspace.ActionRead)
	return wf, err
}

// WorkflowUpdate changes workflow metadata; nil fields are unchanged. An
// empty Description clears it.
type WorkflowUpdate struct {
	Name        *string
	Description *string
}

// UpdateWorkflow changes the workflow's metadata.
func (s *WorkflowService) UpdateWorkflow(ctx context.Context, user, id uuid.UUID, u WorkflowUpdate) (workflow.Workflow, error) {
	wf, _, err := s.workflow(ctx, user, id, workspace.ActionWrite)
	if err != nil {
		return workflow.Workflow{}, err
	}
	if u.Name != nil {
		wf.Name = *u.Name
	}
	if u.Description != nil {
		wf.Description = u.Description
		if *u.Description == "" {
			wf.Description = nil
		}
	}
	wf, err = s.store.UpdateWorkflow(ctx, wf)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return workflow.Workflow{}, notFound("workflow")
	}
	return wf, err
}

// DeleteWorkflow hides the workflow; versions and executions are kept.
func (s *WorkflowService) DeleteWorkflow(ctx context.Context, user, id uuid.UUID) error {
	if _, _, err := s.workflow(ctx, user, id, workspace.ActionDelete); err != nil {
		return err
	}
	if err := s.store.DeleteWorkflow(ctx, id); errors.Is(err, workflow.ErrWorkflowNotFound) {
		return notFound("workflow")
	} else {
		return err
	}
}

// --- versions ---------------------------------------------------------------

// DecodeDefinition parses a workflow definition strictly (unknown fields are
// rejected).
func DecodeDefinition(raw json.RawMessage) (workflow.Definition, error) {
	var def workflow.Definition
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&def); err != nil {
		return workflow.Definition{}, &InvalidError{Message: "definition is not a valid workflow definition: " + err.Error()}
	}
	if dec.More() {
		return workflow.Definition{}, &InvalidError{Message: "definition must be a single JSON object"}
	}
	return def, nil
}

func (s *WorkflowService) validate(def workflow.Definition, mode workflow.ValidationMode) workflow.ValidationResult {
	return s.validator.ValidateWithOptions(def, workflow.ValidationOptions{Mode: mode})
}

// CreateVersion validates the definition (node types, ports, configuration
// and graph structure through the graph validator, in draft mode: a draft
// may still be incomplete) and stores it as a new immutable DRAFT version.
func (s *WorkflowService) CreateVersion(ctx context.Context, user, workflowID uuid.UUID, raw json.RawMessage) (workflow.Version, error) {
	_, workspaceID, err := s.workflow(ctx, user, workflowID, workspace.ActionWrite)
	if err != nil {
		return workflow.Version{}, err
	}
	def, err := DecodeDefinition(raw)
	if err != nil {
		return workflow.Version{}, err
	}
	if res := s.validate(def, workflow.ValidationDraft); !res.Valid {
		return workflow.Version{}, &ValidationFailedError{Errors: res.Errors}
	}
	if err := s.checkCredentials(ctx, workspaceID, def); err != nil {
		return workflow.Version{}, err
	}
	canonical, err := json.Marshal(def)
	if err != nil {
		return workflow.Version{}, err
	}
	v, err := s.store.CreateVersion(ctx, workflowID, canonical, user)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return workflow.Version{}, notFound("workflow")
	}
	return v, err
}

// ListVersions lists one page of version metadata.
func (s *WorkflowService) ListVersions(ctx context.Context, user, workflowID uuid.UUID, page Page) ([]workflow.Version, int, error) {
	if _, _, err := s.workflow(ctx, user, workflowID, workspace.ActionRead); err != nil {
		return nil, 0, err
	}
	return s.store.ListVersions(ctx, workflowID, page.Size, page.offset())
}

func (s *WorkflowService) version(ctx context.Context, workflowID, versionID uuid.UUID) (workflow.Version, error) {
	v, err := s.store.FindVersion(ctx, workflowID, versionID)
	if errors.Is(err, workflow.ErrVersionNotFound) {
		return workflow.Version{}, notFound("workflow version")
	}
	return v, err
}

// GetVersion returns a version with its definition.
func (s *WorkflowService) GetVersion(ctx context.Context, user, workflowID, versionID uuid.UUID) (workflow.Version, error) {
	if _, _, err := s.workflow(ctx, user, workflowID, workspace.ActionRead); err != nil {
		return workflow.Version{}, err
	}
	return s.version(ctx, workflowID, versionID)
}

// ValidateVersion validates a stored version in executable mode.
func (s *WorkflowService) ValidateVersion(ctx context.Context, user, workflowID, versionID uuid.UUID) (workflow.ValidationResult, error) {
	if _, _, err := s.workflow(ctx, user, workflowID, workspace.ActionRead); err != nil {
		return workflow.ValidationResult{}, err
	}
	v, err := s.version(ctx, workflowID, versionID)
	if err != nil {
		return workflow.ValidationResult{}, err
	}
	return s.validateStored(v)
}

func (s *WorkflowService) validateStored(v workflow.Version) (workflow.ValidationResult, error) {
	def, err := DecodeDefinition(v.Definition)
	if err != nil {
		return workflow.ValidationResult{Valid: false, Errors: []workflow.ValidationError{{
			Code: workflow.ErrInvalidWorkflow, Message: "stored definition cannot be decoded"}}}, nil
	}
	res := s.validate(def, workflow.ValidationExecutable)
	if res.Errors == nil {
		res.Errors = []workflow.ValidationError{}
	}
	return res, nil
}

// PublishVersion validates the version in executable mode and, atomically,
// marks it PUBLISHED and makes it the workflow's active version.
func (s *WorkflowService) PublishVersion(ctx context.Context, user, workflowID, versionID uuid.UUID) (workflow.Version, error) {
	if _, _, err := s.workflow(ctx, user, workflowID, workspace.ActionPublish); err != nil {
		return workflow.Version{}, err
	}
	v, err := s.store.PublishVersion(ctx, workflowID, versionID, func(v workflow.Version) error {
		res, err := s.validateStored(v)
		if err != nil {
			return err
		}
		if !res.Valid {
			return &ValidationFailedError{Errors: res.Errors}
		}
		return nil
	})
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound):
		return workflow.Version{}, notFound("workflow")
	case errors.Is(err, workflow.ErrVersionNotFound):
		return workflow.Version{}, notFound("workflow version")
	case errors.Is(err, workflow.ErrVersionArchived):
		return workflow.Version{}, &ConflictError{Code: "VERSION_ARCHIVED", Message: "Archived versions cannot be published"}
	}
	return v, err
}
