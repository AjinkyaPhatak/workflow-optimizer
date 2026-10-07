package responses

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/auth"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/observability"
	"workflow-optimizer/internal/templates"
	"workflow-optimizer/internal/workflow"
	"workflow-optimizer/internal/workspace"
)

// --- auth -------------------------------------------------------------------

// User is a user's public profile (never the password hash).
type User struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
	Name  string    `json:"name"`
}

// Workspace is a workspace with the caller's role in it.
type Workspace struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role"`
}

// Token is an issued bearer token.
type Token struct {
	AccessToken string     `json:"access_token"`
	TokenType   string     `json:"token_type"`
	ExpiresAt   time.Time  `json:"expires_at"`
	User        User       `json:"user"`
	Workspace   *Workspace `json:"workspace,omitempty"`
}

// Me is the authenticated user and their workspaces.
type Me struct {
	User       User        `json:"user"`
	Workspaces []Workspace `json:"workspaces"`
}

func NewUser(u auth.User) User { return User{ID: u.ID, Email: u.Email, Name: u.Name} }

func NewToken(s application.Session, ws *workspace.Workspace) Token {
	t := Token{AccessToken: s.Token, TokenType: "Bearer", ExpiresAt: s.Claims.ExpiresAt, User: NewUser(s.User)}
	if ws != nil {
		t.Workspace = &Workspace{ID: ws.ID, Name: ws.Name, Role: string(workspace.RoleOwner)}
	}
	return t
}

func NewMe(u auth.User, ms []workspace.Membership) Me {
	out := Me{User: NewUser(u), Workspaces: make([]Workspace, 0, len(ms))}
	for _, m := range ms {
		out.Workspaces = append(out.Workspaces, Workspace{ID: m.Workspace.ID, Name: m.Workspace.Name, Role: string(m.Role)})
	}
	return out
}

// --- projects and workflows -------------------------------------------------

// Project is a project.
type Project struct {
	ID          uuid.UUID `json:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Name        string    `json:"name"`
	Description *string   `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewProject(p workflow.Project) Project {
	return Project{ID: p.ID, WorkspaceID: p.WorkspaceID, Name: p.Name, Description: p.Description, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// Workflow is workflow metadata.
type Workflow struct {
	ID              uuid.UUID  `json:"id"`
	ProjectID       uuid.UUID  `json:"project_id"`
	Name            string     `json:"name"`
	Description     *string    `json:"description"`
	ActiveVersionID *uuid.UUID `json:"active_version_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func NewWorkflow(w workflow.Workflow) Workflow {
	return Workflow{ID: w.ID, ProjectID: w.ProjectID, Name: w.Name, Description: w.Description,
		ActiveVersionID: w.ActiveVersionID, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
}

// WorkflowFromTemplate is a workflow created from a template, with its
// first (DRAFT) version.
type WorkflowFromTemplate struct {
	Workflow
	Version VersionSummary `json:"version"`
}

func NewWorkflowFromTemplate(w workflow.Workflow, v workflow.Version) WorkflowFromTemplate {
	return WorkflowFromTemplate{Workflow: NewWorkflow(w), Version: NewVersionSummary(v)}
}

// Template is a workflow template: a ready-made workflow definition.
type Template struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	NodeTypes   []string            `json:"node_types"`
	Definition  workflow.Definition `json:"definition"`
}

func NewTemplate(t templates.Template) Template {
	types := []string{}
	for _, n := range t.Definition.Nodes {
		types = append(types, n.Type)
	}
	return Template{ID: t.ID, Name: t.Name, Description: t.Description, NodeTypes: types, Definition: t.Definition}
}

// VersionSummary is version metadata (no definition).
type VersionSummary struct {
	ID            uuid.UUID  `json:"id"`
	WorkflowID    uuid.UUID  `json:"workflow_id"`
	VersionNumber int        `json:"version_number"`
	Status        string     `json:"status"`
	CreatedBy     *uuid.UUID `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	PublishedAt   *time.Time `json:"published_at"`
}

// Version is a version with its definition.
type Version struct {
	VersionSummary
	Definition json.RawMessage `json:"definition"`
}

func NewVersionSummary(v workflow.Version) VersionSummary {
	return VersionSummary{ID: v.ID, WorkflowID: v.WorkflowID, VersionNumber: v.VersionNumber, Status: string(v.Status),
		CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt}
}

func NewVersion(v workflow.Version) Version {
	return Version{VersionSummary: NewVersionSummary(v), Definition: v.Definition}
}

// Validation is the result of validating a version.
type Validation struct {
	Valid    bool                       `json:"valid"`
	Errors   []workflow.ValidationError `json:"errors"`
	Warnings []workflow.ValidationError `json:"warnings"`
}

func NewValidation(r workflow.ValidationResult) Validation {
	errs := r.Errors
	if errs == nil {
		errs = []workflow.ValidationError{}
	}
	return Validation{Valid: r.Valid, Errors: errs, Warnings: []workflow.ValidationError{}}
}

// --- executions -------------------------------------------------------------

// ExecutionAccepted is the 202 body of POST /workflows/{id}/execute.
type ExecutionAccepted struct {
	ExecutionID uuid.UUID `json:"execution_id"`
	Status      string    `json:"status"`
}

// ExecutionError is a persisted, already-sanitized execution failure.
type ExecutionError struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	NodeID    *string `json:"node_id,omitempty"`
	Retryable bool    `json:"retryable"`
}

func newExecutionError(e *execution.ExecutionError) *ExecutionError {
	if e == nil {
		return nil
	}
	return &ExecutionError{Code: e.Code, Message: e.Message, NodeID: e.NodeID, Retryable: e.Retryable}
}

// Execution is an execution's state. Claim tokens, leases and other worker
// internals are deliberately absent.
type Execution struct {
	ID                uuid.UUID       `json:"id"`
	WorkflowID        uuid.UUID       `json:"workflow_id"`
	WorkflowVersionID uuid.UUID       `json:"workflow_version_id"`
	Status            string          `json:"status"`
	Input             map[string]any  `json:"input"`
	Output            map[string]any  `json:"output"`
	Error             *ExecutionError `json:"error"`
	Attempt           int             `json:"attempt"`
	MaxAttempts       int             `json:"max_attempts"`
	CancelRequested   bool            `json:"cancel_requested"`
	NextAttemptAt     *time.Time      `json:"next_attempt_at"`
	CreatedAt         time.Time       `json:"created_at"`
	StartedAt         *time.Time      `json:"started_at"`
	CompletedAt       *time.Time      `json:"completed_at"`
}

func NewExecution(e execution.Execution) Execution {
	return Execution{ID: e.ID, WorkflowID: e.WorkflowID, WorkflowVersionID: e.WorkflowVersionID, Status: string(e.Status),
		Input: e.Input, Output: e.Output, Error: newExecutionError(e.Error), Attempt: e.Attempt, MaxAttempts: e.MaxAttempts,
		CancelRequested: e.CancelRequested, NextAttemptAt: e.NextAttemptAt, CreatedAt: e.CreatedAt,
		StartedAt: e.StartedAt, CompletedAt: e.FinishedAt}
}

// NodeExecution is node execution metadata (no node inputs or outputs).
type NodeExecution struct {
	ID               uuid.UUID       `json:"id"`
	NodeID           string          `json:"node_id"`
	NodeType         string          `json:"node_type"`
	Status           string          `json:"status"`
	Attempt          int             `json:"attempt"`
	ExecutionAttempt int             `json:"execution_attempt"`
	StartedAt        *time.Time      `json:"started_at"`
	CompletedAt      *time.Time      `json:"completed_at"`
	DurationMS       *int64          `json:"duration_ms"`
	Error            *ExecutionError `json:"error"`
}

func NewNodeExecution(n execution.NodeExecution) NodeExecution {
	out := NodeExecution{ID: n.ID, NodeID: n.NodeID, NodeType: n.NodeType, Status: string(n.Status), Attempt: n.Attempt,
		ExecutionAttempt: n.ExecutionAttempt, StartedAt: n.StartedAt, CompletedAt: n.FinishedAt, Error: newExecutionError(n.Error)}
	if n.StartedAt != nil && n.FinishedAt != nil {
		d := n.FinishedAt.Sub(*n.StartedAt).Milliseconds()
		out.DurationMS = &d
	}
	return out
}

// --- credentials ------------------------------------------------------------

// Credential is credential metadata. There is no field for the secret or the
// encrypted data.
type Credential struct {
	ID             uuid.UUID `json:"id"`
	WorkspaceID    uuid.UUID `json:"workspace_id"`
	Name           string    `json:"name"`
	Provider       string    `json:"provider"`
	CredentialType string    `json:"credential_type"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func NewCredential(c credential.Credential) Credential {
	return Credential{ID: c.ID, WorkspaceID: c.WorkspaceID, Name: c.Name, Provider: c.Provider,
		CredentialType: strings.ToLower(string(c.CredentialType)), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// --- nodes ------------------------------------------------------------------

// Port is a node port.
type Port struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Multiple    bool   `json:"multiple"`
	Description string `json:"description"`
}

// ConfigField is a node configuration field.
type ConfigField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Default     any    `json:"default"`
	Description string `json:"description"`
	// Presentation metadata (Phase B); omitted when unset.
	Label       string         `json:"label,omitempty"`
	Options     []ConfigOption `json:"options,omitempty"`
	AllowCustom bool           `json:"allow_custom,omitempty"`
	Min         *float64       `json:"min,omitempty"`
	Max         *float64       `json:"max,omitempty"`
	Step        *float64       `json:"step,omitempty"`
	Multiline   bool           `json:"multiline,omitempty"`
}

// ConfigOption is one accepted value of a configuration field; When limits
// it to configurations where the named fields have the given values.
type ConfigOption struct {
	Value any            `json:"value"`
	Label string         `json:"label"`
	When  map[string]any `json:"when,omitempty"`
}

// NodeType is a node type from the node registry.
type NodeType struct {
	Type        string        `json:"type"`
	Name        string        `json:"name"`
	Category    string        `json:"category"`
	Description string        `json:"description"`
	Role        string        `json:"role"`
	SideEffects string        `json:"side_effects"`
	Inputs      []Port        `json:"inputs"`
	Outputs     []Port        `json:"outputs"`
	Config      []ConfigField `json:"config"`
}

func ports(ps []node.PortDefinition) []Port {
	out := make([]Port, 0, len(ps))
	for _, p := range ps {
		out = append(out, Port{Name: p.Name, Type: string(p.Type), Required: p.Required, Multiple: p.Multiple, Description: p.Description})
	}
	return out
}

func NewNodeType(d node.NodeDefinition) NodeType {
	cfg := make([]ConfigField, 0, len(d.Config))
	for _, f := range d.Config {
		var opts []ConfigOption
		for _, o := range f.Options {
			label := o.Label
			if label == "" {
				label = fmt.Sprint(o.Value)
			}
			opts = append(opts, ConfigOption{Value: o.Value, Label: label, When: o.When})
		}
		cfg = append(cfg, ConfigField{Name: f.Name, Type: string(f.Type), Required: f.Required, Default: f.Default, Description: f.Description,
			Label: f.Label, Options: opts, AllowCustom: f.AllowCustom, Min: f.Min, Max: f.Max, Step: f.Step, Multiline: f.Multiline})
	}
	side := string(d.SideEffects)
	if side == "" {
		side = "none"
	}
	return NodeType{Type: d.Type, Name: d.Name, Category: string(d.Category), Description: d.Description, Role: string(d.Role),
		SideEffects: side, Inputs: ports(d.Inputs), Outputs: ports(d.Outputs), Config: cfg}
}

// --- observability (Phase 14) -------------------------------------------------

// TokenUsage is provider-reported token consumption.
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

func newUsage(u *application.TokenUsage) *TokenUsage {
	if u == nil {
		return nil
	}
	return &TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
}

func durationMS(d *time.Duration) *int64 {
	if d == nil {
		return nil
	}
	ms := d.Milliseconds()
	return &ms
}

// ExecutionDetails is GET /executions/{id}: the execution plus aggregates
// derived from its node records. estimated_cost_usd is an estimate from
// configured model prices, not billing.
type ExecutionDetails struct {
	Execution
	DurationMS       *int64      `json:"duration_ms"`
	NodeCount        int         `json:"node_count"`
	Retries          int         `json:"retries"`
	Usage            *TokenUsage `json:"usage"`
	EstimatedCostUSD *float64    `json:"estimated_cost_usd"`
}

func NewExecutionDetails(d application.ExecutionDetails) ExecutionDetails {
	return ExecutionDetails{Execution: NewExecution(d.Execution), DurationMS: durationMS(d.Duration), NodeCount: d.NodeCount,
		Retries: d.Retries, Usage: newUsage(d.Usage), EstimatedCostUSD: d.EstimatedCost}
}

// NodeExecutionDetails is one node record for the debugger. Input and output
// are redacted copies (absent when the policy hides them).
type NodeExecutionDetails struct {
	NodeExecution
	ExecutionID      uuid.UUID      `json:"execution_id"`
	CreatedAt        time.Time      `json:"created_at"`
	Input            map[string]any `json:"input,omitempty"`
	Output           map[string]any `json:"output,omitempty"`
	Provider         *string        `json:"provider"`
	Model            *string        `json:"model"`
	Usage            *TokenUsage    `json:"usage"`
	EstimatedCostUSD *float64       `json:"estimated_cost_usd"`
}

func NewNodeExecutionDetails(d application.NodeExecutionDetails) NodeExecutionDetails {
	n := NewNodeExecution(d.Record)
	n.DurationMS = durationMS(d.Duration)
	return NodeExecutionDetails{NodeExecution: n, ExecutionID: d.Record.ExecutionID, CreatedAt: d.Record.CreatedAt,
		Input: d.Input, Output: d.Output, Provider: d.Provider, Model: d.Model, Usage: newUsage(d.Usage), EstimatedCostUSD: d.EstimatedCost}
}

// Event is one execution event.
type Event struct {
	ID          uuid.UUID      `json:"id"`
	ExecutionID uuid.UUID      `json:"execution_id"`
	NodeID      *string        `json:"node_id"`
	Type        string         `json:"type"`
	Timestamp   time.Time      `json:"timestamp"`
	Data        map[string]any `json:"data"`
}

// Events is GET /executions/{id}/events.
type Events struct {
	Events   []Event `json:"events"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Total    int     `json:"total"`
}

func NewEvent(e execution.ExecutionEvent) Event {
	data := observability.RedactMap(e.Data)
	if data == nil {
		data = map[string]any{}
	}
	return Event{ID: e.ID, ExecutionID: e.ExecutionID, NodeID: e.NodeID, Type: string(e.Type), Timestamp: e.Timestamp, Data: data}
}
