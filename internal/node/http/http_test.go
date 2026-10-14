package http_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
	nodehttp "workflow-optimizer/internal/node/http"
)

const secret = "s3cr3t-token-value"

type seen struct {
	Method, Path, Query, Body string
	Header                    stdhttp.Header
}

type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newServer(t *testing.T, h stdhttp.HandlerFunc) *server {
	s := &server{}
	s.Server = httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) last() seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[len(s.reqs)-1]
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func jsonOK(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Set-Cookie", "session=abc")
	w.Header().Set("X-Request-Id", "r-1")
	_, _ = w.Write([]byte(`{"items":[1,2],"next":null}`))
}

// resolver returns one HTTP credential of the given type and hosts.
type resolver struct {
	typ      credential.Type
	hosts    []string
	provider string
	ws       uuid.UUID
}

func (r *resolver) Resolve(_ context.Context, ws, id uuid.UUID, provider string) (credential.ResolvedCredential, error) {
	r.provider, r.ws = provider, ws
	if provider != credential.ProviderHTTP {
		return credential.ResolvedCredential{}, credential.ErrProviderMismatch
	}
	return credential.ResolvedCredential{ID: id, Provider: provider, Type: r.typ, Secret: credential.NewSecret(secret), AllowedHosts: r.hosts}, nil
}

func run(n *nodehttp.Node, cfg map[string]any, ports map[string]node.Value) (map[string]any, error) {
	out, err := n.Execute(context.Background(), node.NodeInput{Config: cfg, Ports: ports, Scope: node.Scope{WorkspaceID: uuid.New()}})
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	for k, v := range out.Ports {
		res[k] = v.Data
	}
	return res, nil
}

func nodeErr(t *testing.T, err error) *node.NodeError {
	t.Helper()
	var ne *node.NodeError
	if !errors.As(err, &ne) {
		t.Fatalf("expected a node error, got %v", err)
	}
	return ne
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func local(s *server, creds credential.Resolver) *nodehttp.Node {
	return nodehttp.NewWith(nodehttp.Options{Transport: s.Client().Transport, Credentials: creds})
}

func TestDefinition(t *testing.T) {
	d := nodehttp.Definition()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.Auth == nil || d.Auth.Provider != "http" || d.Auth.Required || d.SideEffects != node.SideEffectsUnsafe {
		t.Fatalf("definition: %+v", d)
	}
}

func TestGetWithQueryAndHeaders(t *testing.T) {
	s := newServer(t, jsonOK)
	out, err := run(local(s, nil), map[string]any{"url": s.URL + "/v1/items?page=2", "query": map[string]any{"q": "a b", "limit": 10.0, "tag": []any{"x", "y"}},
		"headers": map[string]any{"Accept": "application/json", "X-Trace": "t1"}}, map[string]node.Value{"headers": node.NewObjectValue(map[string]any{"X-Trace": "from-port", "X-Extra": "e"})})
	if err != nil {
		t.Fatal(err)
	}
	if out["status_code"] != 200.0 || out["ok"] != true || mustJSON(out["response"]) != `{"items":[1,2],"next":null}` ||
		mustJSON(out["headers"]) != `{"content-length":"27","content-type":"application/json","date":"`+out["headers"].(map[string]any)["date"].(string)+`","x-request-id":"r-1"}` {
		t.Fatalf("out: %v", out)
	}
	r := s.last()
	if r.Method != "GET" || r.Path != "/v1/items" || r.Query != "limit=10&page=2&q=a+b&tag=x&tag=y" || r.Body != "" ||
		r.Header.Get("X-Trace") != "t1" || r.Header.Get("X-Extra") != "e" || r.Header.Get("User-Agent") != "WorkflowOptimizer/1.0" ||
		r.Header.Get("Idempotency-Key") != "" {
		t.Fatalf("request: %+v", r)
	}
}

func TestBodies(t *testing.T) {
	s := newServer(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("created"))
	})
	n := local(s, nil)
	in := func(cfg map[string]any, ports map[string]node.Value, key string) (seen, map[string]any) {
		t.Helper()
		out, err := n.Execute(context.Background(), node.NodeInput{Config: cfg, Ports: ports, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		res := map[string]any{}
		for k, v := range out.Ports {
			res[k] = v.Data
		}
		return s.last(), res
	}
	r, out := in(map[string]any{"method": "POST", "url": s.URL, "body": map[string]any{"name": "Ada", "n": 1.0}}, nil, "exec-1/node-a")
	if r.Body != `{"n":1,"name":"Ada"}` || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Idempotency-Key") != "exec-1/node-a" ||
		out["response"] != "created" {
		t.Fatalf("json: %+v %v", r, out)
	}
	r, _ = in(map[string]any{"method": "POST", "url": s.URL, "body_type": "form", "body": map[string]any{"a": "1 2", "b": true}}, nil, "")
	if r.Body != "a=1+2&b=true" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Idempotency-Key") != "" {
		t.Fatalf("form: %+v", r)
	}
	r, _ = in(map[string]any{"method": "PUT", "url": s.URL, "body_type": "text", "body": "hello", "headers": map[string]any{"Content-Type": "text/csv"}}, nil, "k")
	if r.Body != "hello" || r.Header.Get("Content-Type") != "text/csv" || r.Header.Get("Idempotency-Key") != "" {
		t.Fatalf("text: %+v", r)
	}
	// The body input is used when the Body setting is empty.
	r, _ = in(map[string]any{"method": "PATCH", "url": s.URL}, map[string]node.Value{"body": node.NewObjectValue(map[string]any{"from": "port"})}, "")
	if r.Body != `{"from":"port"}` {
		t.Fatalf("body port: %+v", r)
	}
	r, _ = in(map[string]any{"method": "GET", "url": s.URL, "body": "ignored"}, nil, "")
	if r.Body != "" {
		t.Fatal("GET sends no body")
	}
	r, _ = in(map[string]any{"method": "POST", "url": s.URL, "body_type": "none", "body": "x"}, nil, "")
	if r.Body != "" {
		t.Fatal("body_type none sends no body")
	}
}

func TestURLFromInput(t *testing.T) {
	s := newServer(t, jsonOK)
	if _, err := run(local(s, nil), map[string]any{"url": "https://ignored.example.test"}, map[string]node.Value{"url": node.NewStringValue(s.URL + "/from-port")}); err != nil || s.last().Path != "/from-port" {
		t.Fatalf("url port: %v %+v", err, s.last())
	}
	// A connected object (not text) leaves the setting in charge.
	if _, err := run(local(s, nil), map[string]any{"url": s.URL + "/setting"}, map[string]node.Value{"url": node.NewObjectValue(map[string]any{"a": 1.0})}); err != nil || s.last().Path != "/setting" {
		t.Fatalf("url setting: %v", err)
	}
}

func TestStatusHandling(t *testing.T) {
	status := 0
	s := newServer(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if status == 429 {
			w.Header().Set("Retry-After", "7")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"echo ` + secret + `"}`))
	})
	n := local(s, nil)
	cases := []struct {
		status     int
		method     string
		code       node.ErrorCode
		retryable  bool
		notApplied bool
	}{
		{404, "GET", node.ErrCodeInvalidInput, false, true},
		{401, "GET", node.ErrCodeInvalidCredentials, false, true},
		{503, "GET", node.ErrCodeUnavailable, true, true},
		{503, "POST", node.ErrCodeUnavailable, true, false},
		{429, "POST", node.ErrCodeRateLimited, true, true},
	}
	for _, c := range cases {
		status = c.status
		_, err := run(n, map[string]any{"url": s.URL, "method": c.method}, nil)
		e := nodeErr(t, err)
		if e.Code != c.code || e.Retryable != c.retryable || e.NotApplied != c.notApplied || strings.Contains(e.Error(), secret) {
			t.Fatalf("%d %s: %+v", c.status, c.method, e)
		}
		if c.status == 429 && e.RetryAfter != 7*time.Second {
			t.Fatalf("retry after: %v", e.RetryAfter)
		}
	}
	status = 500
	out, err := run(n, map[string]any{"url": s.URL, "fail_on_error": false}, nil)
	if err != nil || out["status_code"] != 500.0 || out["ok"] != false || mustJSON(out["response"]) != `{"error":"echo `+secret+`"}` {
		t.Fatalf("fail_on_error off: %v %v", out, err)
	}
}

func TestResponseFormats(t *testing.T) {
	var body []byte
	ct := ""
	s := newServer(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write(body)
	})
	n := local(s, nil)
	check := func(b, contentType, format, want string) {
		t.Helper()
		body, ct = []byte(b), contentType
		out, err := run(n, map[string]any{"url": s.URL, "response_format": format}, nil)
		if err != nil || mustJSON(out["response"]) != want {
			t.Fatalf("%q %q %q: %v %v", b, contentType, format, out["response"], err)
		}
	}
	check(`{"a":1}`, "application/json; charset=utf-8", "auto", `{"a":1}`)
	check(`{"a":1}`, "text/plain", "auto", `"{\"a\":1}"`)
	check(`{"a":1}`, "text/plain", "json", `{"a":1}`)
	check(`not json`, "application/json", "auto", `"not json"`)
	check(`{"a":1}`, "application/json", "text", `"{\"a\":1}"`)
	check("", "application/json", "auto", `""`)
	body, ct = []byte{0xff, 0x00, 0xfe}, "image/png"
	out, err := run(n, map[string]any{"url": s.URL}, nil)
	if err != nil || mustJSON(out["response"]) != `{"base64":"/wD+","content_type":"image/png","size":3}` {
		t.Fatalf("binary: %v %v", out, err)
	}
	body, ct = []byte("nope"), "text/plain"
	if _, err := run(n, map[string]any{"url": s.URL, "response_format": "json"}, nil); nodeErr(t, err).Retryable {
		t.Fatal("bad JSON is not retryable")
	}
}

func TestCredentials(t *testing.T) {
	s := newServer(t, jsonOK)
	id := uuid.NewString()
	for _, c := range []struct {
		typ   credential.Type
		cfg   map[string]any
		check func(seen) bool
	}{
		{credential.TypeBearerToken, nil, func(r seen) bool { return r.Header.Get("Authorization") == "Bearer "+secret }},
		{credential.TypeBasicAuth, nil, func(r seen) bool {
			return r.Header.Get("Authorization") == "Basic "+base64.StdEncoding.EncodeToString([]byte(secret))
		}},
		{credential.TypeAPIKey, nil, func(r seen) bool { return r.Header.Get("X-API-Key") == secret }},
		{credential.TypeAPIKey, map[string]any{"api_key_name": "Api-Token"}, func(r seen) bool { return r.Header.Get("Api-Token") == secret }},
		{credential.TypeAPIKey, map[string]any{"api_key_name": "key", "api_key_in": "query"}, func(r seen) bool { return r.Query == "key="+secret }},
	} {
		res := &resolver{typ: c.typ, hosts: []string{"127.0.0.1"}}
		cfg := map[string]any{"url": s.URL, "credential_id": id, "headers": map[string]any{"Authorization": "Bearer user-set"}}
		for k, v := range c.cfg {
			cfg[k] = v
		}
		out, err := run(local(s, res), cfg, nil)
		if err != nil || !c.check(s.last()) || res.provider != "http" {
			t.Fatalf("%s %v: %v %+v", c.typ, c.cfg, err, s.last().Header)
		}
		if strings.Contains(mustJSON(out), secret) {
			t.Fatal("the secret leaked into the output")
		}
	}

	// Another host than the credential's: refused before any request.
	before := s.count()
	_, err := run(local(s, &resolver{typ: credential.TypeBearerToken, hosts: []string{"api.example.com"}}), map[string]any{"url": s.URL, "credential_id": id}, nil)
	if e := nodeErr(t, err); e.Code != "CREDENTIAL_HOST_NOT_ALLOWED" || e.Retryable || s.count() != before || strings.Contains(e.Error(), secret) {
		t.Fatalf("host binding: %+v", e)
	}
	_, err = run(local(s, nil), map[string]any{"url": s.URL, "credential_id": id}, nil)
	if nodeErr(t, err).Code != node.ErrCodeConfiguration {
		t.Fatal("no resolver")
	}
	_, err = run(local(s, &resolver{}), map[string]any{"url": s.URL, "credential_id": "not-a-uuid"}, nil)
	if nodeErr(t, err).Code != "CREDENTIAL_INVALID" {
		t.Fatal("bad credential id")
	}
}

func TestCredentialNeverFollowsRedirectToAnotherHost(t *testing.T) {
	other := newServer(t, jsonOK)
	s := newServer(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path == "/same" {
			stdhttp.Redirect(w, r, "/final", stdhttp.StatusFound)
			return
		}
		if r.URL.Path == "/final" {
			jsonOK(w, r)
			return
		}
		stdhttp.Redirect(w, r, other.URL+"/steal", stdhttp.StatusFound)
	})
	res := &resolver{typ: credential.TypeAPIKey, hosts: []string{"127.0.0.1"}}
	n := local(s, res)
	out, err := run(n, map[string]any{"url": s.URL + "/away", "credential_id": uuid.NewString()}, nil)
	if err != nil || out["status_code"] != 302.0 || other.count() != 0 {
		t.Fatalf("cross-host redirect with a credential: %v %v (other hit %d)", out, err, other.count())
	}
	out, err = run(n, map[string]any{"url": s.URL + "/same", "credential_id": uuid.NewString()}, nil)
	if err != nil || out["status_code"] != 200.0 || s.last().Path != "/final" || s.last().Header.Get("X-API-Key") != secret {
		t.Fatalf("same-host redirect: %v %v", out, err)
	}
	// Without a credential, redirects are followed.
	if out, err := run(n, map[string]any{"url": s.URL + "/away"}, nil); err != nil || out["status_code"] != 200.0 || other.count() != 1 {
		t.Fatalf("plain redirect: %v %v", out, err)
	}
}

func TestPrivateNetworksAreBlockedByDefault(t *testing.T) {
	s := newServer(t, jsonOK)
	_, err := run(nodehttp.New(), map[string]any{"url": s.URL}, nil) // 127.0.0.1
	if e := nodeErr(t, err); e.Code != node.ErrCodeInvalidInput || e.Retryable || !strings.Contains(e.Message, "HTTP_ALLOW_PRIVATE_NETWORKS") || s.count() != 0 {
		t.Fatalf("loopback: %+v (hits %d)", e, s.count())
	}
	if _, err := run(nodehttp.NewWith(nodehttp.Options{AllowPrivateNetworks: true}), map[string]any{"url": s.URL}, nil); err != nil || s.count() != 1 {
		t.Fatalf("allowed: %v", err)
	}
	for addr, blocked := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true, "169.254.169.254": true, "100.64.0.1": true,
		"0.0.0.0": true, "::1": true, "fe80::1": true, "fd00::1": true, "::ffff:127.0.0.1": true, "224.0.0.1": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700:4700::1111": false,
	} {
		if nodehttp.Blocked(netip.MustParseAddr(addr)) != blocked {
			t.Errorf("%s blocked = %v", addr, !blocked)
		}
	}
}

func TestInvalidSettingsFailBeforeAnyRequest(t *testing.T) {
	s := newServer(t, jsonOK)
	n := local(s, nil)
	for name, cfg := range map[string]map[string]any{
		"no url":        {},
		"relative":      {"url": "/v1/items"},
		"scheme":        {"url": "ftp://example.test/x"},
		"userinfo":      {"url": "https://user:pass@example.test/"},
		"method":        {"url": s.URL, "method": "TRACE"},
		"host header":   {"url": s.URL, "headers": map[string]any{"Host": "evil.test"}},
		"timeout":       {"url": s.URL, "timeout_ms": 10.0},
		"form not obj":  {"url": s.URL, "method": "POST", "body_type": "form", "body": "a=1"},
		"query not obj": {"url": s.URL, "query": "a=1"},
	} {
		_, err := run(n, cfg, nil)
		if e := nodeErr(t, err); e.Code != node.ErrCodeConfiguration || e.Retryable {
			t.Fatalf("%s: %+v", name, e)
		}
	}
	if s.count() != 0 {
		t.Fatal("an invalid request reached the server")
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	s := newServer(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	_, err := run(local(s, nil), map[string]any{"url": s.URL, "method": "DELETE", "timeout_ms": 1000.0}, nil)
	if e := nodeErr(t, err); e.Code != node.ErrCodeTimeout || !e.Retryable || !e.NotApplied {
		t.Fatalf("timeout: %+v", e)
	}
}
