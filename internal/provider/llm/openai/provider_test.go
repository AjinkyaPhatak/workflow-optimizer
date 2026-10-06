package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/provider/llm"
	"workflow-optimizer/internal/provider/llm/openai"
)

const apiKey = "sk-test-DO-NOT-LEAK-42"

func cred() credential.ResolvedCredential {
	return credential.ResolvedCredential{ID: uuid.New(), Provider: openai.Name, Type: credential.TypeAPIKey, Secret: credential.NewSecret(apiKey)}
}

// server answers every request with handler and records what it received.
type server struct {
	mu      sync.Mutex
	auth    []string
	bodies  []map[string]any
	path    string
	handler func(w http.ResponseWriter, r *http.Request)
}

func newServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*server, *openai.Provider) {
	t.Helper()
	s := &server{handler: handler}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.bodies = append(s.bodies, body)
		s.path = r.URL.Path
		s.mu.Unlock()
		s.handler(w, r)
	}))
	t.Cleanup(ts.Close)
	p, err := openai.New(openai.Options{BaseURL: ts.URL + "/v1", HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

const okBody = `{"id":"chatcmpl-1","model":"gpt-5-2025","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there"},"finish_reason":"stop"}],
"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`

func request() llm.Request {
	temp := 0.7
	max := 64
	return llm.Request{Model: "gpt-5", System: "be brief", Temperature: &temp, MaxTokens: &max,
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "Say hi"}}, Credential: cred()}
}

func TestSuccessfulGeneration(t *testing.T) {
	s, p := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody)
	})
	resp, err := p.Generate(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Response{Content: "Hello there", Model: "gpt-5-2025", FinishReason: "stop",
		Usage: llm.TokenUsage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15}}
	if resp != want {
		t.Fatalf("response = %+v", resp)
	}
	if s.path != "/v1/chat/completions" {
		t.Fatalf("path = %s", s.path)
	}
	if s.auth[0] != "Bearer "+apiKey {
		t.Fatalf("authorization header = %q", s.auth[0])
	}
	body := s.bodies[0]
	msgs, _ := json.Marshal(body["messages"])
	if body["model"] != "gpt-5" || body["temperature"] != 0.7 || body["max_completion_tokens"] != float64(64) ||
		string(msgs) != `[{"content":"be brief","role":"system"},{"content":"Say hi","role":"user"}]` {
		t.Fatalf("request body = %v", body)
	}
	if _, leaked := body["api_key"]; leaked {
		t.Fatal("api key in body")
	}
}

func TestErrorNormalization(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		header     map[string]string
		body       string
		code       llm.ErrorCode
		retryable  bool
		retryAfter time.Duration
		notApplied bool
	}{
		{"rate limited", 429, map[string]string{"Retry-After": "7"}, `{"error":{"type":"requests","code":"rate_limit_exceeded"}}`, llm.CodeRateLimited, true, 7 * time.Second, true},
		{"rate limited ms", 429, map[string]string{"Retry-After-Ms": "1500"}, `{}`, llm.CodeRateLimited, true, 1500 * time.Millisecond, true},
		{"quota exhausted", 429, nil, `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`, llm.CodeRateLimited, false, 0, true},
		{"server error", 500, nil, `{"error":{"type":"server_error"}}`, llm.CodeServerError, true, 0, false},
		{"bad gateway", 502, nil, `<html>bad gateway</html>`, llm.CodeServerError, true, 0, false},
		{"unavailable", 503, map[string]string{"Retry-After": "2"}, ``, llm.CodeUnavailable, true, 2 * time.Second, false},
		{"gateway timeout", 504, nil, ``, llm.CodeTimeout, true, 0, false},
		{"invalid api key", 401, nil, `{"error":{"message":"Incorrect API key provided: sk-test-DO-NOT-LEAK-42","type":"invalid_request_error","code":"invalid_api_key"}}`, llm.CodeAuthenticationFailed, false, 0, false},
		{"forbidden", 403, nil, `{}`, llm.CodeAuthenticationFailed, false, 0, false},
		{"unknown model", 404, nil, `{"error":{"message":"The model gpt-x does not exist","type":"invalid_request_error","code":"model_not_found"}}`, llm.CodeModelNotSupported, false, 0, false},
		{"invalid request", 400, nil, `{"error":{"message":"bad temperature","type":"invalid_request_error","param":"temperature"}}`, llm.CodeInvalidRequest, false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, p := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := p.Generate(context.Background(), request())
			var perr *llm.Error
			if !errors.As(err, &perr) {
				t.Fatalf("err = %v", err)
			}
			if perr.Code != tc.code || perr.Retryable != tc.retryable || perr.RetryAfter != tc.retryAfter ||
				perr.NotApplied != tc.notApplied || perr.StatusCode != tc.status || perr.Provider != openai.Name {
				t.Fatalf("got %+v", perr)
			}
			// Neither the key nor the provider's human-readable message
			// (which can echo it) reaches the error.
			if s := err.Error(); strings.Contains(s, apiKey) || strings.Contains(s, "Incorrect API key") || strings.Contains(s, "does not exist") {
				t.Fatalf("error leaks provider text: %s", s)
			}
		})
	}
}

func TestMalformedResponse(t *testing.T) {
	for _, body := range []string{`not json`, `{"choices":[]}`, `{"choices":[{"message":{}}]}`} {
		_, p := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		_, err := p.Generate(context.Background(), request())
		var perr *llm.Error
		if !errors.As(err, &perr) || perr.Code != llm.CodeInvalidResponse || !perr.Retryable {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

func TestCancellationAndTimeouts(t *testing.T) {
	release := make(chan struct{})
	_, p := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	slowURL := serverURL(t, release)
	// Registered after the servers, so it runs before they close (cleanups
	// are LIFO) and no handler is left blocked.
	t.Cleanup(func() { close(release) })
	// The caller's cancellation and deadline pass through as such (the
	// engine decides what they mean), never as a provider fault.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := p.Generate(ctx, request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	dctx, dcancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer dcancel()
	if _, err := p.Generate(dctx, request()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	// The provider's own HTTP timeout is a retryable PROVIDER_TIMEOUT.
	slow, err := openai.New(openai.Options{BaseURL: slowURL, HTTPClient: &http.Client{Timeout: 50 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = slow.Generate(context.Background(), request())
	var perr *llm.Error
	if !errors.As(err, &perr) || perr.Code != llm.CodeTimeout || !perr.Retryable {
		t.Fatalf("client timeout: %v", err)
	}
}

func serverURL(t *testing.T, release chan struct{}) string {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestUnreachableProviderIsRetryable(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	p, _ := openai.New(openai.Options{BaseURL: url})
	_, err := p.Generate(context.Background(), request())
	var perr *llm.Error
	if !errors.As(err, &perr) || perr.Code != llm.CodeUnavailable || !perr.Retryable {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestCredentialIsRequiredAndProviderMatched(t *testing.T) {
	_, p := newServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request sent without a usable credential") })
	for name, c := range map[string]credential.ResolvedCredential{
		"none":           {},
		"other provider": {Provider: "anthropic", Type: credential.TypeAPIKey, Secret: credential.NewSecret("x")},
		"other type":     {Provider: openai.Name, Type: credential.TypeOAuth2, Secret: credential.NewSecret("x")},
	} {
		req := request()
		req.Credential = c
		if _, err := p.Generate(context.Background(), req); !errors.Is(err, credential.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestConcurrentRequestsShareTheProvider(t *testing.T) {
	s, p := newServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, okBody) })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := request()
			req.Messages = []llm.Message{{Role: llm.RoleUser, Content: fmt.Sprint(i)}}
			if _, err := p.Generate(context.Background(), req); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if len(s.bodies) != 20 {
		t.Fatalf("%d requests", len(s.bodies))
	}
}
