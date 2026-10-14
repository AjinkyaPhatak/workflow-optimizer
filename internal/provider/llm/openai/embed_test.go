package openai_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/provider/llm"
)

func TestEmbed(t *testing.T) {
	s, p := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Answers out of order: the index decides.
		_, _ = fmt.Fprint(w, `{"data":[{"index":1,"embedding":[0,1,0]},{"index":0,"embedding":[1,0,0]}],"usage":{"total_tokens":7}}`)
	})
	resp, err := p.Embed(context.Background(), llm.EmbedRequest{Model: "text-embedding-3-small", Dimensions: 3, Inputs: []string{"a", "b"}, Credential: cred()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Tokens != 7 || resp.Vectors[0][0] != 1 || resp.Vectors[1][1] != 1 {
		t.Fatalf("vectors: %+v", resp)
	}
	body := s.bodies[0]
	if s.path != "/v1/embeddings" || body["model"] != "text-embedding-3-small" || body["dimensions"] != 3.0 || body["encoding_format"] != "float" ||
		fmt.Sprint(body["input"]) != "[a b]" || s.auth[0] != "Bearer "+apiKey {
		t.Fatalf("request: %s %v", s.path, body)
	}
}

func TestEmbedFailures(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		code      llm.ErrorCode
		retryable bool
	}{
		{"wrong count", 200, `{"data":[{"index":0,"embedding":[1,0,0]}]}`, llm.CodeInvalidResponse, true},
		{"wrong size", 200, `{"data":[{"index":0,"embedding":[1,0]},{"index":1,"embedding":[1,0]}]}`, llm.CodeInvalidResponse, true},
		{"bad key", 401, `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"Incorrect API key sk-test-DO-NOT-LEAK-42"}}`, llm.CodeAuthenticationFailed, false},
		{"quota", 429, `{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`, llm.CodeRateLimited, false},
		{"down", 503, `{}`, llm.CodeUnavailable, true},
	}
	for _, c := range cases {
		_, p := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = fmt.Fprint(w, c.body)
		})
		_, err := p.Embed(context.Background(), llm.EmbedRequest{Model: "m", Dimensions: 3, Inputs: []string{"a", "b"}, Credential: cred()})
		var pe *llm.Error
		if !errors.As(err, &pe) || pe.Code != c.code || pe.Retryable != c.retryable || strings.Contains(err.Error(), apiKey) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	_, p := newServer(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not be called") })
	if _, err := p.Embed(context.Background(), llm.EmbedRequest{Model: "m", Inputs: []string{"a"}, Credential: credential.ResolvedCredential{}}); !errors.Is(err, credential.ErrInvalid) {
		t.Fatalf("no credential: %v", err)
	}
	if _, err := p.Embed(context.Background(), llm.EmbedRequest{Model: "m", Credential: cred()}); err == nil {
		t.Fatal("no inputs")
	}
}
