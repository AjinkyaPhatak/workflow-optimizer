package automation

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Workflow is the durable identity of an assistant draft. The ID is also its
// session ID, so existing conversation links remain valid.
type Workflow struct {
	ID              uuid.UUID  `json:"id"`
	WorkspaceID     uuid.UUID  `json:"workspace_id"`
	ProjectID       *uuid.UUID `json:"project_id,omitempty"`
	Name            string     `json:"name"`
	ActiveVersionID *uuid.UUID `json:"active_version_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
type WorkflowVersion struct {
	ID            uuid.UUID      `json:"id"`
	WorkflowID    uuid.UUID      `json:"workflow_id"`
	WorkspaceID   uuid.UUID      `json:"workspace_id"`
	VersionNumber int            `json:"version_number"`
	Revision      int            `json:"revision"`
	Status        string         `json:"status"`
	Plan          Plan           `json:"plan"`
	Answers       map[string]any `json:"answers"`
	CredentialID  uuid.UUID      `json:"credential_id"`
	CreatedAt     time.Time      `json:"created_at"`
}
type WorkflowChoice struct {
	Ref           string    `json:"ref"`
	WorkflowID    uuid.UUID `json:"workflow_id"`
	Name          string    `json:"name"`
	VersionNumber int       `json:"version_number"`
}
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
type EditorLayout struct {
	Revision  int                 `json:"revision"`
	Positions map[string]Position `json:"positions"`
}
type EditorDocument struct {
	Workflow Workflow     `json:"workflow"`
	Session  Session      `json:"session"`
	Layout   EditorLayout `json:"layout"`
}
type WorkflowTemplate struct {
	ID          string       `json:"id"`
	WorkspaceID uuid.UUID    `json:"workspace_id,omitempty"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Category    string       `json:"category,omitempty"`
	Builtin     bool         `json:"builtin"`
	Plan        Plan         `json:"plan"`
	Layout      EditorLayout `json:"layout"`
	CreatedAt   time.Time    `json:"created_at"`
}

// WorkflowStore is separate from execution storage. Tests and workers that only
// execute immutable runs do not need to implement editor persistence.
type WorkflowStore interface {
	Workflow(context.Context, uuid.UUID) (Workflow, error)
	ListWorkflows(context.Context, uuid.UUID) ([]Workflow, error)
	SetWorkflowProject(context.Context, uuid.UUID, uuid.UUID) error
	Versions(context.Context, uuid.UUID) ([]WorkflowVersion, error)
	Version(context.Context, uuid.UUID) (WorkflowVersion, error)
	PublishVersion(context.Context, WorkflowVersion, int) (WorkflowVersion, error)
	Layout(context.Context, uuid.UUID) (EditorLayout, error)
	SaveLayout(context.Context, uuid.UUID, EditorLayout) (EditorLayout, error)
	SaveWorkflowTemplate(context.Context, WorkflowTemplate) error
	WorkflowTemplates(context.Context, uuid.UUID) ([]WorkflowTemplate, error)
	WorkflowTemplate(context.Context, string) (WorkflowTemplate, error)
	DeleteWorkflowTemplate(context.Context, string, uuid.UUID) error
}

func EmptyPlan(name string) Plan {
	return Plan{SchemaVersion: SchemaVersion, Name: name, Mode: "once", Budget: Budget{MaxActions: 30, MaxTokens: 50000, MaxActiveSeconds: 600}, Steps: []Step{}, Questions: []Question{}}
}

// RefreshQuestionOptions keeps resource choices current without changing IDs.
func RefreshQuestionOptions(plan Plan, tools []ToolDescriptor) Plan {
	plan = ClonePlan(plan)
	steps := map[string]Step{}
	var walk func([]Step)
	walk = func(list []Step) {
		for _, s := range list {
			steps[s.ID] = s
			walk(s.Children)
			walk(s.Otherwise)
		}
	}
	walk(plan.Steps)
	catalog := map[string]ToolDescriptor{}
	for _, tool := range tools {
		catalog[tool.ID] = tool
	}
	for i := range plan.Questions {
		q := &plan.Questions[i]
		tool := catalog[steps[q.StepID].Tool]
		for _, f := range tool.Inputs {
			if f.Name != q.Input {
				continue
			}
			q.Min = f.Min
			q.Max = f.Max
			resource := f.Name == "config.credential_id" || f.Name == "config.file_id" || f.Name == "config.audio_file" || f.Name == "config.image_file" || f.Name == "config.knowledge_base_id" || f.Name == "base_id"
			if len(f.Options) > 0 || resource {
				q.Kind = "choice"
				q.Options = append([]Option{}, f.Options...)
				q.AllowOther = f.AllowCustom
			}
			if f.Name == "config.credential_id" && len(f.Options) == 0 {
				q.Prompt = "Connect an account for " + tool.Name + " in Connected accounts, then return here."
			}
		}
	}
	return plan
}

type ControlDescriptor struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Inputs      []Field `json:"inputs"`
	Outputs     []Field `json:"outputs"`
}

func ControlDescriptors() []ControlDescriptor {
	result := []Field{{Name: "result", Type: "json", Description: "The result of this group"}}
	return []ControlDescriptor{
		{Kind: "branch", Name: "Decision", Description: "Take the Yes or Otherwise path after comparing two values.", Inputs: []Field{}, Outputs: result},
		{Kind: "parallel", Name: "Do together", Description: "Run independent steps together, then continue when all finish.", Inputs: []Field{}, Outputs: result},
		{Kind: "map", Name: "For each item", Description: "Repeat these steps for each item in a list.", Inputs: []Field{{Name: "items", Label: "List", Type: "array", Required: true}}, Outputs: result},
		{Kind: "repeat", Name: "Repeat until", Description: "Repeat these steps until the stopping rule is met, within a limit.", Inputs: []Field{}, Outputs: result},
		{Kind: "ask", Name: "Ask a person", Description: "Wait for a specific answer before continuing.", Inputs: []Field{}, Outputs: result},
		{Kind: "wait", Name: "Wait", Description: "Wait for a duration or an incoming event.", Inputs: []Field{}, Outputs: result},
		{Kind: "workflow", Name: "Run a saved workflow", Description: "Run a pinned published version and wait for its result.", Inputs: []Field{}, Outputs: result},
	}
}
