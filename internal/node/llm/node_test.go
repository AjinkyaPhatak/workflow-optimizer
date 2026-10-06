package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/llm"
	providerllm "workflow-optimizer/internal/provider/llm"
)

const secret = "sk-node-test-SECRET"

type fakeProvider struct {
	got  providerllm.Request
	resp providerllm.Response
	err  error
}

func (f *fakeProvider) Generate(_ context.Context, req providerllm.Request) (providerllm.Response, error) {
	f.got = req
	return f.resp, f.err
}

type fakeResolver struct {
	workspace, id uuid.UUID
	provider      string
	err           error
}

func (f *fakeResolver) Resolve(_ context.Context, ws, id uuid.UUID, provider string) (credential.ResolvedCredential, error) {
	f.workspace, f.id, f.provider = ws, id, provider
	if f.err != nil {
		return credential.ResolvedCredential{}, f.err
	}
	return credential.ResolvedCredential{ID: id, Provider: provider, Type: credential.TypeAPIKey, Secret: credential.NewSecret(secret)}, nil
}

func setup(t *testing.T, p *fakeProvider, r *fakeResolver) *llm.Node {
	t.Helper()
	reg := providerllm.NewRegistry()
	if err := reg.Register("fake", p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("other", &fakeProvider{err: errors.New("wrong provider selected")}); err != nil {
		t.Fatal(err)
	}
	reg.Freeze()
	return llm.New(llm.Dependencies{Providers: reg, Credentials: r})
}

func input(ws, credID uuid.UUID, extra map[string]any) node.NodeInput {
	cfg := map[string]any{"provider": "fake", "model": "m-1", "temperature": 0.3, "max_tokens": float64(50), "credential_id": credID.String()}
	for k, v := range extra {
		cfg[k] = v
	}
	return node.NodeInput{
		Ports:  map[string]node.Value{"prompt": node.NewStringValue("What is 2+2?"), "system": node.NewStringValue("Answer tersely")},
		Config: cfg,
		Scope:  node.Scope{WorkspaceID: ws},
	}
}

func TestLLMNodeBuildsRequestAndOutput(t *testing.T) {
	p := &fakeProvider{resp: providerllm.Response{Content: "4", Model: "m-1-2025", FinishReason: "stop",
		Usage: providerllm.TokenUsage{InputTokens: 9, OutputTokens: 1, TotalTokens: 10}}}
	r := &fakeResolver{}
	ws, credID := uuid.New(), uuid.New()
	out, err := setup(t, p, r).Execute(context.Background(), input(ws, credID, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Credential resolved in the execution's workspace, for the selected provider.
	if r.workspace != ws || r.id != credID || r.provider != "fake" {
		t.Fatalf("resolve(%s, %s, %q)", r.workspace, r.id, r.provider)
	}
	got := p.got
	if got.Model != "m-1" || got.System != "Answer tersely" || *got.Temperature != 0.3 || *got.MaxTokens != 50 ||
		len(got.Messages) != 1 || got.Messages[0] != (providerllm.Message{Role: providerllm.RoleUser, Content: "What is 2+2?"}) ||
		got.Credential.Secret.Reveal() != secret {
		t.Fatalf("request = %+v", got)
	}
	resp, _ := out.GetPort("response")
	if s, _ := resp.String(); s != "4" {
		t.Fatalf("response = %v", resp)
	}
	usage, _ := out.GetPort("usage")
	if fmt.Sprint(usage.Data) != "map[input_tokens:9 output_tokens:1 total_tokens:10]" {
		t.Fatalf("usage = %v", usage.Data)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), secret) {
		t.Fatal("secret in node output")
	}
}

func TestLLMNodeErrors(t *testing.T) {
	ws, credID := uuid.New(), uuid.New()
	cases := []struct {
		name      string
		provider  error
		resolver  error
		extra     map[string]any
		code      node.ErrorCode
		retryable bool
	}{
		{"rate limited keeps retry-after", &providerllm.Error{Provider: "fake", Code: providerllm.CodeRateLimited, StatusCode: 429, Retryable: true, RetryAfter: 3 * time.Second, NotApplied: true}, nil, nil, "RATE_LIMITED", true},
		{"auth failure", &providerllm.Error{Provider: "fake", Code: providerllm.CodeAuthenticationFailed, StatusCode: 401}, nil, nil, "AUTHENTICATION_FAILED", false},
		{"server error", &providerllm.Error{Provider: "fake", Code: providerllm.CodeServerError, StatusCode: 500, Retryable: true}, nil, nil, "PROVIDER_SERVER_ERROR", true},
		{"unknown provider error", errors.New("boom"), nil, nil, node.ErrCodeExecutionFailed, true},
		{"credential not found", nil, fmt.Errorf("%w: x", credential.ErrNotFound), nil, llm.ErrCodeCredentialNotFound, false},
		{"decryption failed", nil, credential.ErrDecryptionFailed, nil, llm.ErrCodeCredentialDecryptionFailed, false},
		{"provider mismatch", nil, credential.ErrProviderMismatch, nil, llm.ErrCodeCredentialProviderMismatch, false},
		{"credential store down", nil, errors.New("connection refused"), nil, node.ErrCodeUnavailable, true},
		{"missing credential id", nil, nil, map[string]any{"credential_id": ""}, llm.ErrCodeCredentialInvalid, false},
		{"bad credential id", nil, nil, map[string]any{"credential_id": "cred_123"}, llm.ErrCodeCredentialInvalid, false},
		{"unknown provider", nil, nil, map[string]any{"provider": "nope"}, llm.ErrCodeProviderNotFound, false},
		{"api key in config", nil, nil, map[string]any{"api_key": secret}, node.ErrCodeConfiguration, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{err: tc.provider}
			_, err := setup(t, p, &fakeResolver{err: tc.resolver}).Execute(context.Background(), input(ws, credID, tc.extra))
			var ne *node.NodeError
			if !errors.As(err, &ne) || ne.Code != tc.code || ne.Retryable != tc.retryable {
				t.Fatalf("err = %#v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks the secret: %v", err)
			}
			if tc.code == "RATE_LIMITED" && (ne.RetryAfter != 3*time.Second || !ne.NotApplied || ne.Source != node.SourceProvider) {
				t.Fatalf("rate limit classification lost: %+v", ne)
			}
		})
	}
}

func TestLLMNodeSelectsProviderFromConfig(t *testing.T) {
	p := &fakeProvider{resp: providerllm.Response{Content: "ok"}}
	n := setup(t, p, &fakeResolver{})
	if _, err := n.Execute(context.Background(), input(uuid.New(), uuid.New(), map[string]any{"provider": "other"})); err == nil ||
		!strings.Contains(err.Error(), "wrong provider selected") {
		t.Fatalf("provider from config not used: %v", err)
	}
	if p.got.Model != "" {
		t.Fatal("unselected provider was called")
	}
}

func TestLLMNodeCancellationPassesThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := setup(t, &fakeProvider{}, &fakeResolver{}).Execute(ctx, input(uuid.New(), uuid.New(), nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	p := &fakeProvider{err: context.DeadlineExceeded}
	if _, err := setup(t, p, &fakeResolver{}).Execute(context.Background(), input(uuid.New(), uuid.New(), nil)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("provider deadline: %v", err)
	}
}
