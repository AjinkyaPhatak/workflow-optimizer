// Package requests defines the API's request bodies and their transport-level
// validation (required fields, lengths, UUIDs, enums). The backend is the
// authority: domain rules are enforced again by the application services.
package requests

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/credential"
)

// Field limits.
const (
	MaxNameLength        = 255
	MaxDescriptionLength = 10_000
	MaxSecretBytes       = 16 << 10
)

// ParseUUID parses a UUID field or path parameter.
func ParseUUID(field, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, httpx.BadRequest(field + " must be a valid UUID")
	}
	return id, nil
}

func name(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", httpx.BadRequest(field + " is required")
	}
	if utf8.RuneCountInString(v) > MaxNameLength {
		return "", httpx.BadRequest(fmt.Sprintf("%s must be at most %d characters", field, MaxNameLength))
	}
	return v, nil
}

func description(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	d := strings.TrimSpace(*v)
	if utf8.RuneCountInString(d) > MaxDescriptionLength {
		return nil, httpx.BadRequest(fmt.Sprintf("description must be at most %d characters", MaxDescriptionLength))
	}
	return &d, nil
}

// --- auth -------------------------------------------------------------------

// Register is POST /auth/register.
type Register struct {
	Email         string `json:"email"`
	Name          string `json:"name"`
	Password      string `json:"password"`
	WorkspaceName string `json:"workspace_name"`
}

// Validate returns the application input.
func (r Register) Validate() (application.RegisterInput, error) {
	email, err := emailAddress(r.Email)
	if err != nil {
		return application.RegisterInput{}, err
	}
	n, err := name("name", r.Name)
	if err != nil {
		return application.RegisterInput{}, err
	}
	if len(r.Password) < 8 || len(r.Password) > 72 {
		return application.RegisterInput{}, httpx.BadRequest("password must be 8 to 72 bytes")
	}
	ws := strings.TrimSpace(r.WorkspaceName)
	if ws != "" {
		if ws, err = name("workspace_name", ws); err != nil {
			return application.RegisterInput{}, err
		}
	}
	return application.RegisterInput{Email: email, Name: n, Password: r.Password, WorkspaceName: ws}, nil
}

func emailAddress(v string) (string, error) {
	v = strings.TrimSpace(v)
	addr, err := mail.ParseAddress(v)
	if v == "" || len(v) > MaxNameLength || err != nil || addr.Address != v {
		return "", httpx.BadRequest("email must be a valid email address")
	}
	return v, nil
}

// Login is POST /auth/login.
type Login struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Validate checks presence only (wrong values fail authentication).
func (r Login) Validate() error {
	if strings.TrimSpace(r.Email) == "" || r.Password == "" {
		return httpx.BadRequest("email and password are required")
	}
	if len(r.Email) > MaxNameLength || len(r.Password) > 1024 {
		return httpx.BadRequest("email or password is too long")
	}
	return nil
}

// --- projects and workflows -------------------------------------------------

// CreateProject is POST /projects.
type CreateProject struct {
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// Validate returns the parsed fields.
func (r CreateProject) Validate() (uuid.UUID, string, *string, error) {
	ws, err := ParseUUID("workspace_id", r.WorkspaceID)
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	n, err := name("name", r.Name)
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	d, err := description(r.Description)
	return ws, n, d, err
}

// CreateWorkflow is POST /workflows.
type CreateWorkflow struct {
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// Validate returns the parsed fields.
func (r CreateWorkflow) Validate() (uuid.UUID, string, *string, error) {
	p, err := ParseUUID("project_id", r.ProjectID)
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	n, err := name("name", r.Name)
	if err != nil {
		return uuid.Nil, "", nil, err
	}
	d, err := description(r.Description)
	return p, n, d, err
}

// UpdateWorkflow is PATCH /workflows/{id}. Omitted fields are unchanged; an
// empty description clears it.
type UpdateWorkflow struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// Validate returns the update.
func (r UpdateWorkflow) Validate() (application.WorkflowUpdate, error) {
	if r.Name == nil && r.Description == nil {
		return application.WorkflowUpdate{}, httpx.BadRequest("at least one of name or description is required")
	}
	var u application.WorkflowUpdate
	if r.Name != nil {
		n, err := name("name", *r.Name)
		if err != nil {
			return u, err
		}
		u.Name = &n
	}
	d, err := description(r.Description)
	u.Description = d
	return u, err
}

// CreateVersion is POST /workflows/{id}/versions.
type CreateVersion struct {
	Definition json.RawMessage `json:"definition"`
}

// Validate requires a JSON object definition.
func (r CreateVersion) Validate() (json.RawMessage, error) {
	if !isObject(r.Definition) {
		return nil, httpx.BadRequest("definition is required and must be a JSON object")
	}
	return r.Definition, nil
}

func isObject(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return strings.HasPrefix(s, "{")
}

// --- executions -------------------------------------------------------------

// Execute is POST /workflows/{id}/execute. version_id defaults to the
// workflow's active (published) version.
type Execute struct {
	VersionID *string         `json:"version_id"`
	Input     json.RawMessage `json:"input"`
}

// Validate returns the version (nil = active) and the input object.
func (r Execute) Validate() (*uuid.UUID, map[string]any, error) {
	var version *uuid.UUID
	if r.VersionID != nil {
		v, err := ParseUUID("version_id", *r.VersionID)
		if err != nil {
			return nil, nil, err
		}
		version = &v
	}
	input := map[string]any{}
	if s := strings.TrimSpace(string(r.Input)); s != "" && s != "null" {
		if !isObject(r.Input) || json.Unmarshal(r.Input, &input) != nil {
			return nil, nil, httpx.BadRequest("input must be a JSON object")
		}
	}
	return version, input, nil
}

// --- credentials ------------------------------------------------------------

// CreateCredential is POST /credentials. Secret is write-only: it is never
// returned or logged.
type CreateCredential struct {
	WorkspaceID    string `json:"workspace_id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	CredentialType string `json:"credential_type"`
	Secret         string `json:"secret"`
}

// Validate returns the application input.
func (r CreateCredential) Validate() (application.CreateCredential, error) {
	ws, err := ParseUUID("workspace_id", r.WorkspaceID)
	if err != nil {
		return application.CreateCredential{}, err
	}
	n, err := name("name", r.Name)
	if err != nil {
		return application.CreateCredential{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(r.Provider))
	if provider == "" || len(provider) > MaxNameLength {
		return application.CreateCredential{}, httpx.BadRequest("provider is required")
	}
	typ := credential.Type(strings.ToUpper(strings.TrimSpace(r.CredentialType)))
	if !typ.Valid() {
		return application.CreateCredential{}, httpx.BadRequest("credential_type must be one of api_key, oauth2, bearer_token, basic_auth, service_account, custom")
	}
	if strings.TrimSpace(r.Secret) == "" {
		return application.CreateCredential{}, httpx.BadRequest("secret is required")
	}
	if len(r.Secret) > MaxSecretBytes {
		return application.CreateCredential{}, httpx.BadRequest("secret is too long")
	}
	return application.CreateCredential{WorkspaceID: ws, Name: n, Provider: provider, Type: typ,
		Secret: credential.NewSecret(r.Secret)}, nil
}

// String redacts the secret if the request is ever formatted.
func (r CreateCredential) String() string {
	return fmt.Sprintf("CreateCredential{workspace_id:%s name:%q provider:%q type:%q secret:[REDACTED]}",
		r.WorkspaceID, r.Name, r.Provider, r.CredentialType)
}

// GoString redacts the secret under %#v.
func (r CreateCredential) GoString() string { return r.String() }
