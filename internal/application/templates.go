package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/templates"
	"workflow-optimizer/internal/workflow"
	"workflow-optimizer/internal/workspace"
)

// Saved templates (Phase K): "Save as template" copies a workflow's current
// definition into the workspace's gallery; members who may edit workflows
// save and delete them, every member lists and uses them.

// MaxTemplateNameLength bounds a saved template's name.
const MaxTemplateNameLength = 255

// WithSavedTemplates enables saved templates.
func (s *WorkflowService) WithSavedTemplates(repo templates.SavedRepository) *WorkflowService {
	s.saved = repo
	return s
}

func (s *WorkflowService) savedRepo() (templates.SavedRepository, error) {
	if s.saved == nil {
		return nil, &InvalidError{Message: "saved templates are not available"}
	}
	return s.saved, nil
}

// SaveTemplate saves the workflow's newest version as a template of its
// workspace.
func (s *WorkflowService) SaveTemplate(ctx context.Context, user, workflowID uuid.UUID, name, description string) (templates.Saved, error) {
	repo, err := s.savedRepo()
	if err != nil {
		return templates.Saved{}, err
	}
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" || len(name) > MaxTemplateNameLength {
		return templates.Saved{}, &InvalidError{Message: "name is required (at most 255 characters)"}
	}
	_, ws, err := s.workflow(ctx, user, workflowID, workspace.ActionWrite)
	if err != nil {
		return templates.Saved{}, err
	}
	latest, _, err := s.store.ListVersions(ctx, workflowID, 1, 0)
	if err != nil {
		return templates.Saved{}, err
	}
	if len(latest) == 0 {
		return templates.Saved{}, &InvalidError{Message: "the workflow has no steps yet"}
	}
	v, err := s.version(ctx, workflowID, latest[0].ID)
	if err != nil {
		return templates.Saved{}, err
	}
	def, err := DecodeDefinition(v.Definition)
	if err != nil {
		return templates.Saved{}, err
	}
	if len(def.Nodes) == 0 {
		return templates.Saved{}, &InvalidError{Message: "the workflow has no steps yet"}
	}
	return repo.Create(ctx, templates.Saved{WorkspaceID: ws, Name: name, Description: description, Definition: def, CreatedBy: user})
}

// ListSavedTemplates returns the workspace's saved templates.
func (s *WorkflowService) ListSavedTemplates(ctx context.Context, user, workspaceID uuid.UUID) ([]templates.Saved, error) {
	repo, err := s.savedRepo()
	if err != nil {
		return nil, err
	}
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionRead, "workspace"); err != nil {
		return nil, err
	}
	return repo.List(ctx, workspaceID)
}

// savedTemplate finds a saved template the user may act on; one of another
// workspace is not found.
func (s *WorkflowService) savedTemplate(ctx context.Context, user, id uuid.UUID, action workspace.Action) (templates.Saved, error) {
	repo, err := s.savedRepo()
	if err != nil {
		return templates.Saved{}, err
	}
	t, err := repo.Locate(ctx, id)
	if errors.Is(err, templates.ErrSavedNotFound) {
		return templates.Saved{}, notFound("template")
	}
	if err != nil {
		return templates.Saved{}, err
	}
	if _, err := s.access.Require(ctx, user, t.WorkspaceID, action, "template"); err != nil {
		return templates.Saved{}, err
	}
	return t, nil
}

// DeleteSavedTemplate removes a saved template. Workflows made from it are
// unaffected.
func (s *WorkflowService) DeleteSavedTemplate(ctx context.Context, user, id uuid.UUID) error {
	t, err := s.savedTemplate(ctx, user, id, workspace.ActionWrite)
	if err != nil {
		return err
	}
	if err := s.saved.Delete(ctx, t.WorkspaceID, id); errors.Is(err, templates.ErrSavedNotFound) {
		return notFound("template")
	} else if err != nil {
		return err
	}
	return nil
}

// templateDefinition is a fresh copy of a built-in template, or of a saved
// template (a UUID) of the project's workspace.
func (s *WorkflowService) templateDefinition(ctx context.Context, user uuid.UUID, project workflow.Project, templateID string) (json.RawMessage, error) {
	var def workflow.Definition
	var err error
	if id, perr := uuid.Parse(templateID); perr == nil {
		t, terr := s.savedTemplate(ctx, user, id, workspace.ActionRead)
		var nf *NotFoundError
		if errors.As(terr, &nf) || (terr == nil && t.WorkspaceID != project.WorkspaceID) {
			return nil, &InvalidError{Message: "unknown template_id"}
		}
		if terr != nil {
			return nil, terr
		}
		def, err = templates.InstantiateDefinition(t.Definition)
	} else {
		def, err = templates.Instantiate(templateID)
		if errors.Is(err, templates.ErrNotFound) {
			return nil, &InvalidError{Message: "unknown template_id"}
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(def)
}
