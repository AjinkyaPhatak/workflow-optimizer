// Package fake is a test-only integration ("test_integration") that
// exercises the integration framework end to end without any external
// service: an in-memory backend, a small client that checks an API key the
// way a real API would, and two actions. It is never registered in
// production; tests install it with app.Dependencies.Integrations or
// integration.Install.
package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// Identity of the fake integration.
const (
	ID       = "test_integration"
	Provider = "test_integration"
	// ReadType and WriteType are the node types of its actions.
	ReadType  = ID + ".read"
	WriteType = ID + ".write"
)

// OAuthID is the variant authenticated with OAuth (Phase C2): node types
// test_oauth_integration.read / .write, accounts of OAuthProvider.
const OAuthID = "test_oauth_integration"

// Integration is the fake integration's metadata (API key).
func Integration() integration.Integration {
	return integrationOf(ID, "Test Integration", integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeAPIKey})
}

// OAuthIntegration is the same integration authenticated with connected
// accounts of the OAuth provider oauthProvider.
func OAuthIntegration(oauthProvider string) integration.Integration {
	return integrationOf(OAuthID, "Test OAuth Integration", integration.Auth{Required: true, Provider: oauthProvider, CredentialType: credential.TypeOAuth2})
}

func integrationOf(id, name string, auth integration.Auth) integration.Integration {
	return integration.Integration{
		ID:          id,
		Name:        name,
		Description: "A fake integration for tests. It talks to an in-memory service.",
		Category:    "Testing",
		Icon:        "test",
		Auth:        auth,
		Actions: []integration.Action{
			{
				ID: "read", Name: "Read record", Description: "Reads a record by key.",
				SideEffects: node.SideEffectsNone,
				Inputs:      []node.PortDefinition{node.NewPortDefinition("key", node.ValueTypeString, false, "Record key (overrides the Key setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("record", node.ValueTypeJSON, true, "The stored record"),
				},
				Config: []node.ConfigField{node.NewConfigField("key", node.ValueTypeString, false, "", "Record key, used when the key input is not connected.").WithLabel("Key")},
			},
			{
				ID: "write", Name: "Write record", Description: "Stores a record under a key.",
				SideEffects: node.SideEffectsUnsafe,
				Inputs:      []node.PortDefinition{node.NewPortDefinition("value", node.ValueTypeJSON, true, "Value to store")},
				Outputs:     []node.PortDefinition{node.NewPortDefinition("result", node.ValueTypeJSON, true, "Write confirmation")},
				Config:      []node.ConfigField{node.NewConfigField("key", node.ValueTypeString, true, "", "Record key.").WithLabel("Key")},
			},
		},
	}
}

// Call is one request the service received (never with the credential).
type Call struct {
	Operation      string
	Key            string
	Authorized     bool
	IdempotencyKey string
}

// Service is the fake external service: an in-memory record store guarded
// by an API key. Fail, when set, makes a call fail with the returned error
// (e.g. integration.FromHTTPStatus(503, 0, "...")); Delay makes calls slow
// (they honour cancellation).
type Service struct {
	accept func(token string) bool

	mu      sync.Mutex
	records map[string]any
	calls   []Call

	Fail  func(operation, key string) error
	Delay time.Duration
}

// NewService returns a service that accepts apiKey, seeded with records.
func NewService(apiKey string, records map[string]any) *Service {
	return NewServiceFunc(func(t string) bool { return t == apiKey }, records)
}

// NewServiceFunc returns a service that accepts the tokens accept accepts
// (e.g. the fake OAuth provider's valid access tokens).
func NewServiceFunc(accept func(token string) bool, records map[string]any) *Service {
	r := map[string]any{}
	for k, v := range records {
		r[k] = v
	}
	return &Service{accept: accept, records: r}
}

// Calls returns the calls received so far.
func (s *Service) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call{}, s.calls...)
}

// Record returns a stored record.
func (s *Service) Record(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.records[key]
	return v, ok
}

// handle is the service side of one request: authentication, then the
// operation. Its errors are what a provider-specific client would produce
// from HTTP responses.
func (s *Service) handle(ctx context.Context, auth credential.Secret, c Call, op func() (any, error)) (any, error) {
	c.Authorized = !auth.Empty() && s.accept(auth.Reveal())
	s.mu.Lock()
	s.calls = append(s.calls, c)
	fail := s.Fail
	s.mu.Unlock()
	if s.Delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.Delay):
		}
	}
	if !c.Authorized {
		return nil, integration.FromHTTPStatus(401, 0, "the API key was rejected")
	}
	if fail != nil {
		if err := fail(c.Operation, c.Key); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return op()
}

// Client is the provider-specific client of the fake service. It holds the
// resolved secret for one action call only.
type Client struct {
	svc    *Service
	apiKey credential.Secret
}

// Read returns the record stored under key.
func (c *Client) Read(ctx context.Context, key string) (any, error) {
	return c.svc.handle(ctx, c.apiKey, Call{Operation: "read", Key: key}, func() (any, error) {
		v, ok := c.svc.records[key]
		if !ok {
			return nil, integration.FromHTTPStatus(404, 0, fmt.Sprintf("no record %q", key))
		}
		return v, nil
	})
}

// Write stores value under key. idempotencyKey is recorded the way an API
// would receive it in a header.
func (c *Client) Write(ctx context.Context, key string, value any, idempotencyKey string) error {
	_, err := c.svc.handle(ctx, c.apiKey, Call{Operation: "write", Key: key, IdempotencyKey: idempotencyKey}, func() (any, error) {
		c.svc.records[key] = value
		return nil, nil
	})
	return err
}

// Connector returns the connector that builds clients of svc.
func Connector(svc *Service) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return &Client{svc: svc, apiKey: cred.Secret}, nil
	})
}

func keyOf(in node.NodeInput) string {
	if v, ok := in.GetPort("key"); ok {
		if s, ok := v.String(); ok {
			return strings.TrimSpace(s)
		}
	}
	s, _ := in.GetStringConfig("key")
	return strings.TrimSpace(s)
}

func read(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	key := keyOf(in)
	if key == "" {
		return node.NodeOutput{}, integration.NewError(integration.KindInvalidRequest, "a key is required")
	}
	v, err := c.Read(ctx, key)
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("record", node.NewJSONValue(v))
	return out, nil
}

func write(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	key := keyOf(in)
	if key == "" {
		return node.NodeOutput{}, integration.NewError(integration.KindInvalidRequest, "a key is required")
	}
	v, _ := in.GetPort("value")
	if err := c.Write(ctx, key, v.Data, in.IdempotencyKey); err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("result", node.NewJSONValue(map[string]any{"key": key, "written": true}))
	return out, nil
}

// Module returns the fake integration with its executable nodes, resolving
// credentials with creds and talking to svc.
func Module(creds credential.Resolver, svc *Service) (integration.Module, error) {
	return moduleOf(Integration(), creds, svc)
}

// OAuthModule is Module for OAuthIntegration.
func OAuthModule(creds credential.Resolver, svc *Service, oauthProvider string) (integration.Module, error) {
	return moduleOf(OAuthIntegration(oauthProvider), creds, svc)
}

func moduleOf(in integration.Integration, creds credential.Resolver, svc *Service) (integration.Module, error) {
	r, err := integration.NewActionNode(in, "read", creds, Connector(svc), read)
	if err != nil {
		return integration.Module{}, err
	}
	w, err := integration.NewActionNode(in, "write", creds, Connector(svc), write)
	if err != nil {
		return integration.Module{}, err
	}
	return integration.Module{Integration: in, Nodes: map[string]node.Node{"read": r, "write": w}}, nil
}
