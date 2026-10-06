package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api"
	"workflow-optimizer/internal/api/handlers"
	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/auth"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/node"
)

// These tests cover the transport boundary without infrastructure; the
// services behind the handlers are exercised end to end in tests/phase12.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type harness struct {
	srv    *httptest.Server
	tokens *auth.HMACTokenService
	nodes  node.Registry
	pg     error
	redis  error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	engine, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewHMACTokenService([]byte(strings.Repeat("s", 32)), time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{tokens: tokens, nodes: engine.NodeRegistry}
	handlers := &handlers.Handlers{
		Nodes: engine.NodeRegistry,
		Ready: []handlers.Check{
			{Name: "postgres", Check: func(context.Context) error { return h.pg }},
			{Name: "redis", Check: func(context.Context) error { return h.redis }},
		},
		Logger: quiet,
	}
	h.srv = httptest.NewServer(api.NewRouter(handlers, tokens, quiet))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) token(t *testing.T) string {
	t.Helper()
	tok, _, err := h.tokens.Generate(context.Background(), auth.User{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type apiError struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func (h *harness) do(t *testing.T, method, path, token, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func expectError(t *testing.T, resp *http.Response, body []byte, status int, code string) apiError {
	t.Helper()
	var e apiError
	if resp.StatusCode != status || json.Unmarshal(body, &e) != nil || e.Error.Code != code || e.Error.Message == "" {
		t.Fatalf("got %d %s, want %d %s", resp.StatusCode, body, status, code)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	return e
}

func TestAuthentication(t *testing.T) {
	h := newHarness(t)
	expired, _ := auth.NewHMACTokenService([]byte(strings.Repeat("s", 32)), time.Hour,
		func() time.Time { return time.Now().Add(-2 * time.Hour) })
	old, _, _ := expired.Generate(context.Background(), auth.User{ID: uuid.New()})

	resp, body := h.do(t, "GET", "/api/v1/nodes", "", "")
	expectError(t, resp, body, 401, "UNAUTHENTICATED")
	if resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing WWW-Authenticate")
	}
	resp, body = h.do(t, "GET", "/api/v1/nodes", "garbage.token.value", "")
	expectError(t, resp, body, 401, "UNAUTHENTICATED")
	resp, body = h.do(t, "GET", "/api/v1/nodes", old, "")
	if e := expectError(t, resp, body, 401, "UNAUTHENTICATED"); !strings.Contains(e.Error.Message, "expired") {
		t.Fatalf("expired message = %q", e.Error.Message)
	}
	// A scheme other than Bearer.
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Basic "+h.token(t))
	r, _ := http.DefaultClient.Do(req)
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatalf("basic scheme: %d", r.StatusCode)
	}
	resp, _ = h.do(t, "GET", "/api/v1/nodes", h.token(t), "")
	if resp.StatusCode != 200 {
		t.Fatalf("valid token: %d", resp.StatusCode)
	}
}

func TestNodeCatalogComesFromRegistry(t *testing.T) {
	h := newHarness(t)
	late := node.NodeDefinition{Type: "test.late", Name: "Late", Category: node.CategoryUtilities, Description: "added after start",
		Inputs:  []node.PortDefinition{node.NewPortDefinition("in", node.ValueTypeString, true, "text")},
		Outputs: []node.PortDefinition{node.NewPortDefinition("out", node.ValueTypeString, true, "")},
		Config:  []node.ConfigField{node.NewConfigField("mode", node.ValueTypeString, false, "fast", "how")}}
	if err := h.nodes.RegisterNode(lateNode{}, late); err != nil {
		t.Fatal(err)
	}
	resp, body := h.do(t, "GET", "/api/v1/nodes", h.token(t), "")
	var out struct {
		Items []struct {
			Type, Name, Category, Description string
			SideEffects                       string `json:"side_effects"`
			Inputs                            []struct {
				Name, Type string
				Required   bool
			}
			Config []struct {
				Name    string
				Default any
			}
		} `json:"items"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(body, &out) != nil {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	defs := h.nodes.Definitions()
	if len(out.Items) != len(defs) {
		t.Fatalf("%d items for %d registered definitions", len(out.Items), len(defs))
	}
	types := map[string]bool{}
	for i, it := range out.Items {
		types[it.Type] = true
		if it.Type != defs[i].Type || it.Name == "" || it.Category == "" {
			t.Fatalf("item %d = %+v", i, it)
		}
		if it.Type == "test.late" && (it.Description != "added after start" || len(it.Inputs) != 1 || it.Inputs[0].Type != "string" ||
			!it.Inputs[0].Required || len(it.Config) != 1 || it.Config[0].Default != "fast" || it.SideEffects != "none") {
			t.Fatalf("late node = %+v", it)
		}
	}
	for _, want := range []string{"input", "output", "llm", "http", "test.late"} {
		if !types[want] {
			t.Fatalf("node %q missing from %s", want, body)
		}
	}
}

type lateNode struct{}

func (lateNode) Type() string { return "test.late" }
func (lateNode) Execute(context.Context, node.NodeInput) (node.NodeOutput, error) {
	return node.NewNodeOutput(nil), nil
}

func TestHealthAndReadiness(t *testing.T) {
	h := newHarness(t)
	resp, body := h.do(t, "GET", "/health", "", "")
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok"`) {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(t, "GET", "/ready", "", "")
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"status":"ready"`) {
		t.Fatalf("ready: %d %s", resp.StatusCode, body)
	}
	for _, c := range []struct {
		name      string
		pg, redis error
	}{{"postgres", errors.New("connection refused"), nil}, {"redis", nil, errors.New("connection refused")}} {
		h.pg, h.redis = c.pg, c.redis
		resp, body = h.do(t, "GET", "/ready", "", "")
		e := expectError(t, resp, body, 503, "SERVICE_UNAVAILABLE")
		if !strings.Contains(string(e.Error.Details), `"`+c.name+`":"unavailable"`) || strings.Contains(string(body), "refused") {
			t.Fatalf("%s down: %s", c.name, body)
		}
		// Process health does not depend on dependencies.
		if resp, _ := h.do(t, "GET", "/health", "", ""); resp.StatusCode != 200 {
			t.Fatalf("health with %s down: %d", c.name, resp.StatusCode)
		}
	}
}

func TestRoutingAndMalformedRequests(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t)
	resp, body := h.do(t, "GET", "/api/v1/nope", tok, "")
	expectError(t, resp, body, 404, "NOT_FOUND")
	resp, body = h.do(t, "PUT", "/api/v1/workflows/"+uuid.NewString(), tok, "{}")
	expectError(t, resp, body, 405, "METHOD_NOT_ALLOWED")
	if a := resp.Header.Values("Allow"); len(a) != 3 {
		t.Fatalf("Allow = %v", a)
	}
	// Versions have no update route: they are immutable through the API.
	resp, body = h.do(t, "PATCH", "/api/v1/workflows/"+uuid.NewString()+"/versions/"+uuid.NewString(), tok, "{}")
	expectError(t, resp, body, 405, "METHOD_NOT_ALLOWED")

	for name, c := range map[string]struct{ method, path, body string }{
		"malformed json":  {"POST", "/api/v1/workflows", `{"project_id":`},
		"unknown field":   {"POST", "/api/v1/workflows", `{"project_id":"` + uuid.NewString() + `","name":"x","api_key":"sk"}`},
		"wrong type":      {"POST", "/api/v1/workflows", `{"project_id":1,"name":"x"}`},
		"not an object":   {"POST", "/api/v1/workflows", `[1,2]`},
		"trailing data":   {"POST", "/api/v1/workflows", `{"name":"x"} {}`},
		"empty body":      {"POST", "/api/v1/workflows", ``},
		"missing name":    {"POST", "/api/v1/workflows", `{"project_id":"` + uuid.NewString() + `"}`},
		"bad project id":  {"POST", "/api/v1/workflows", `{"project_id":"123","name":"x"}`},
		"long name":       {"POST", "/api/v1/workflows", `{"project_id":"` + uuid.NewString() + `","name":"` + strings.Repeat("n", 256) + `"}`},
		"bad path uuid":   {"GET", "/api/v1/workflows/not-a-uuid", ``},
		"page size":       {"GET", "/api/v1/workflows?project_id=" + uuid.NewString() + "&page_size=101", ``},
		"page zero":       {"GET", "/api/v1/workflows?project_id=" + uuid.NewString() + "&page=0", ``},
		"missing project": {"GET", "/api/v1/workflows", ``},
		"bad cred type":   {"POST", "/api/v1/credentials", `{"workspace_id":"` + uuid.NewString() + `","name":"n","provider":"openai","credential_type":"password","secret":"s"}`},
		"no secret":       {"POST", "/api/v1/credentials", `{"workspace_id":"` + uuid.NewString() + `","name":"n","provider":"openai","credential_type":"api_key"}`},
		"input not obj":   {"POST", "/api/v1/workflows/" + uuid.NewString() + "/execute", `{"input":[1]}`},
		"bad version id":  {"POST", "/api/v1/workflows/" + uuid.NewString() + "/execute", `{"version_id":"v1"}`},
		"definition":      {"POST", "/api/v1/workflows/" + uuid.NewString() + "/versions", `{"definition":"text"}`},
	} {
		resp, body := h.do(t, c.method, c.path, tok, c.body)
		e := expectError(t, resp, body, 400, "INVALID_REQUEST")
		if name == "unknown field" && strings.Contains(e.Error.Message, "sk") {
			t.Fatalf("value echoed: %s", body)
		}
	}
	// Content type must be JSON.
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/v1/workflows", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "text/plain")
	r, _ := http.DefaultClient.Do(req)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatalf("text/plain: %d", r.StatusCode)
	}
}

func TestPanicRecoveryAndRequestID(t *testing.T) {
	h := newHarness(t)
	// The harness has no workflow service: a valid request reaches a nil
	// service and panics inside the handler.
	resp, body := h.do(t, "POST", "/api/v1/workflows", h.token(t), `{"project_id":"`+uuid.NewString()+`","name":"x"}`)
	expectError(t, resp, body, 500, "INTERNAL_ERROR")
	if strings.Contains(string(body), "goroutine") || strings.Contains(string(body), "nil pointer") {
		t.Fatalf("internals leaked: %s", body)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("no request ID")
	}
	req, _ := http.NewRequest("GET", h.srv.URL+"/health", nil)
	req.Header.Set("X-Request-ID", "client-req-42")
	r, _ := http.DefaultClient.Do(req)
	r.Body.Close()
	if r.Header.Get("X-Request-ID") != "client-req-42" {
		t.Fatalf("request id = %q", r.Header.Get("X-Request-ID"))
	}
	// The server survives.
	if resp, _ := h.do(t, "GET", "/health", "", ""); resp.StatusCode != 200 {
		t.Fatal("server down after panic")
	}
}
