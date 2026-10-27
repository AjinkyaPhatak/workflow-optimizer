package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"unicode/utf8"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/knowledge"
	"workflow-optimizer/internal/workspace"
)

func (s *AssistantService) ImportLegacy(ctx context.Context, user, wfID, versionID, credentialID uuid.UUID, workflows *WorkflowService) (automation.Session, error) {
	wf, err := workflows.GetWorkflow(ctx, user, wfID)
	if err != nil {
		return automation.Session{}, err
	}
	project, err := workflows.GetProject(ctx, user, wf.ProjectID)
	if err != nil {
		return automation.Session{}, err
	}
	version, err := workflows.GetVersion(ctx, user, wf.ID, versionID)
	if err != nil {
		return automation.Session{}, err
	}
	session, err := s.Create(ctx, user, project.WorkspaceID, credentialID)
	if err != nil {
		return session, err
	}
	text := fmt.Sprintf("Create an editable copy of the following existing workflow, named %q. Preserve its intent, configuration, credential references, branching, and repeat behavior. Ask me when a conversion is unclear. Do not add actions. The original version is immutable and must remain usable. Source workflow JSON:\n%s", wf.Name, string(version.Definition))
	return s.Message(ctx, user, session.ID, session.Revision, text)
}

// CopyLocal stages an independent local index, preserving the original until
// every copied source is ready and the user chooses to switch.
func (s *KnowledgeService) CopyLocal(ctx context.Context, user, baseID uuid.UUID) (knowledge.Base, error) {
	original, err := s.base(ctx, user, baseID, workspace.ActionWrite)
	if err != nil {
		return knowledge.Base{}, err
	}
	repo := s.svc.Repository()
	targetID := uuid.NewSHA1(baseID, []byte("local:614241f622f53c4eeff9890bdc4f31cfecc418b3"))
	target, err := repo.GetBase(ctx, original.WorkspaceID, targetID)
	if errors.Is(err, knowledge.ErrNotFound) {
		name := original.Name
		for utf8.RuneCountInString(name) > 105 {
			name = string([]rune(name)[:105])
		}
		target, err = repo.CreateBase(ctx, knowledge.Base{ID: targetID, WorkspaceID: original.WorkspaceID, Name: name + " (local copy)", Description: original.Description, Model: "intfloat/multilingual-e5-small", ModelRevision: "614241f622f53c4eeff9890bdc4f31cfecc418b3", Dimensions: 384})
		if err != nil {
			target, err = repo.GetBase(ctx, original.WorkspaceID, targetID)
		}
	}
	if err != nil {
		return target, knowledgeError(err, "knowledge base")
	}
	docs, err := repo.ListDocuments(ctx, original.WorkspaceID, baseID)
	if err != nil {
		return target, err
	}
	for _, doc := range docs {
		_, text, err := repo.GetDocument(ctx, original.WorkspaceID, doc.ID)
		if err != nil {
			return target, err
		}
		_, copy, created, err := s.svc.Begin(ctx, knowledge.AddInput{WorkspaceID: target.WorkspaceID, BaseID: target.ID, ID: knowledge.DocumentID(target.ID, doc.ID.String()+":"+doc.ContentHash), Title: doc.Title, Text: text, Source: doc.Source, SourceURL: doc.SourceURL, FileID: doc.FileID})
		if err != nil {
			return target, err
		}
		if !created && copy.Status == knowledge.StatusReady {
			continue
		}
		if !created && copy.Status == knowledge.StatusFailed {
			_, copy, text, err = s.svc.Reindex(ctx, target.WorkspaceID, copy.ID)
			if err != nil {
				return target, err
			}
		}
		if err := s.index(target, copy, text); err != nil {
			return target, err
		}
	}
	return repo.GetBase(ctx, target.WorkspaceID, target.ID)
}
