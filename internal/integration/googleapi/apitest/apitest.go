// Package apitest runs integration nodes (Google, Notion) in tests: the
// production composition with a static OAuth credential (no OAuth flow) and
// the provider APIs pointed at test servers, executed by the real graph
// executor.
package apitest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// Token is the access token the static credential resolves to.
const Token = "ya29.test-access-token"

// Resolver resolves every credential of Workspace to Token (for whichever
// provider asks), and nothing else.
type Resolver struct {
	Workspace uuid.UUID
	// AccountID is the provider account the credential names (e.g. a
	// Discord server ID).
	AccountID string
	Calls     int
}

// Resolve implements credential.Resolver.
func (r *Resolver) Resolve(_ context.Context, ws, id uuid.UUID, provider string) (credential.ResolvedCredential, error) {
	r.Calls++
	if ws != r.Workspace {
		return credential.ResolvedCredential{}, credential.ErrNotFound
	}
	return credential.ResolvedCredential{ID: id, Provider: provider, Type: credential.TypeOAuth2, Secret: credential.NewSecret(Token), AccountID: r.AccountID}, nil
}

// Authorized reports whether a request carries exactly the test token.
func Authorized(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+Token }

// Harness runs one integration node at a time.
type Harness struct {
	t       *testing.T
	App     *app.Application
	Creds   *Resolver
	CredID  uuid.UUID
	outputs []node.NodeOutput
}

// New composes the application with deps (Credentials is set here).
func New(t *testing.T, deps app.Dependencies) *Harness {
	t.Helper()
	h := &Harness{t: t, Creds: &Resolver{Workspace: uuid.New()}, CredID: uuid.New()}
	deps.Credentials = h.Creds
	a, err := app.BootstrapWith(config.Config{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	h.App = a
	return h
}

// NodeStarted implements execution.Observer.
func (h *Harness) NodeStarted(context.Context, string, string, node.NodeInput) error { return nil }

// NodeFinished implements execution.Observer.
func (h *Harness) NodeFinished(_ context.Context, _ string, out node.NodeOutput, _ error) error {
	h.outputs = append(h.outputs, out)
	return nil
}

// NodeFailedBeforeExecute implements execution.Observer.
func (h *Harness) NodeFailedBeforeExecute(context.Context, string, string, execution.Stage, error) error {
	return nil
}

var pos = &workflow.Position{}

// Definition is input -> typ (cfg, with the test account) -> output. The
// input object is wired to the node's first input port.
func (h *Harness) Definition(typ string, cfg map[string]any) workflow.Definition {
	h.t.Helper()
	def, err := h.App.NodeRegistry.GetDefinition(typ)
	if err != nil {
		h.t.Fatal(err)
	}
	cfg["credential_id"] = h.CredID.String()
	d := workflow.Definition{Version: workflow.DefinitionSchemaVersion, Settings: map[string]any{},
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "g", Type: typ, Name: "Google", Position: pos, Config: cfg},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{{ID: "e2", Source: "g", SourcePort: def.Outputs[0].Name, Target: "out", TargetPort: "value"}}}
	if len(def.Inputs) > 0 {
		d.Edges = append(d.Edges, workflow.Edge{ID: "e1", Source: "in", SourcePort: "data", Target: "g", TargetPort: def.Inputs[0].Name})
	}
	return d
}

// Run validates and executes def. operationKey fixes the idempotency keys.
func (h *Harness) Run(def workflow.Definition, input map[string]any, operationKey string) error {
	h.t.Helper()
	if res := workflow.NewValidator(h.App.NodeRegistry).Validate(def); !res.Valid {
		h.t.Fatalf("invalid workflow: %+v", res.Errors)
	}
	_, err := execution.NewGraphExecutor(h.App.NodeRegistry).ExecuteWithOptions(context.Background(), def, input,
		execution.ExecuteOptions{Observer: h, Scope: node.Scope{WorkspaceID: h.Creds.Workspace}, OperationKey: operationKey})
	return err
}

// Result runs a single-node workflow and returns all of the node's ports.
func (h *Harness) Result(typ string, cfg map[string]any, input map[string]any) (map[string]any, error) {
	h.t.Helper()
	if err := h.Run(h.Definition(typ, cfg), input, "op-"+uuid.NewString()); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range h.outputs[len(h.outputs)-2].Ports {
		out[k] = v.Data
	}
	return out, nil
}

// Failure is the execution error of a failed run.
func Failure(t *testing.T, err error) execution.ExecutionError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a failure")
	}
	return execution.ErrorFromExecution(err)
}

// NoToken fails when s contains the access token.
func NoToken(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, Token) {
		t.Fatalf("%s contains the access token", what)
	}
}
