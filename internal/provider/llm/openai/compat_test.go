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
	"testing"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/provider/llm"
	"workflow-optimizer/internal/provider/llm/openai"
)

// compat starts a mock OpenAI-compatible API for a spec; handler sees the
// raw request.
func compat(t *testing.T, spec openai.Spec, handler http.HandlerFunc) *openai.Provider {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	p, err := openai.NewCompatible(spec, openai.Options{BaseURL: ts.URL + "/v1", HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func keyFor(provider string) credential.ResolvedCredential {
	c := cred()
	c.Provider = provider
	return c
}

// Phase G: Gemini and Groq speak the OpenAI format with their own names,
// labels, models and length field.
func TestCompatibleProviders(t *testing.T) {
	for _, spec := range []openai.Spec{openai.Gemini, openai.Groq} {
		var body map[string]any
		var path, auth string
		p := compat(t, spec, func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			path, auth = r.URL.Path, r.Header.Get("Authorization")
			_, _ = fmt.Fprint(w, okBody)
		})
		info := p.Describe()
		if info.Name != spec.Name || !strings.Contains(info.Label, "free") || len(info.Models) == 0 || p.Name() != spec.Name {
			t.Fatalf("%s: %+v", spec.Name, info)
		}
		req := request()
		req.Credential = keyFor(spec.Name)
		resp, err := p.Generate(context.Background(), req)
		if err != nil || resp.Content != "Hello there" {
			t.Fatalf("%s: %v %+v", spec.Name, err, resp)
		}
		if path != "/v1/chat/completions" || auth != "Bearer "+apiKey {
			t.Fatalf("%s: %s %s", spec.Name, path, auth)
		}
		field, other := "max_completion_tokens", "max_tokens"
		if spec.MaxTokensField == "max_tokens" {
			field, other = other, field
		}
		if body[field] != 64.0 || body[other] != nil {
			t.Fatalf("%s length field: %v", spec.Name, body)
		}
		// A key of another provider is refused before any request.
		req.Credential = cred()
		if _, err := p.Generate(context.Background(), req); !errors.Is(err, credential.ErrInvalid) {
			t.Fatalf("%s with an OpenAI key: %v", spec.Name, err)
		}
		// Errors name the provider.
		bad := compat(t, spec, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) })
		req.Credential = keyFor(spec.Name)
		var pe *llm.Error
		if _, err := bad.Generate(context.Background(), req); !errors.As(err, &pe) || pe.Provider != spec.Name || pe.Code != llm.CodeRateLimited {
			t.Fatalf("%s: %v", spec.Name, err)
		}
	}
	if openai.Gemini.MaxTokensField != "max_tokens" || openai.OpenAI.MaxTokensField != "max_completion_tokens" {
		t.Fatal("length fields")
	}
}

// Gemini returns full-length embeddings: they are cut to the requested size
// (the dimensions are not sent).
func TestGeminiEmbeddingsAreShortened(t *testing.T) {
	var body map[string]any
	p := compat(t, openai.Gemini, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = fmt.Fprint(w, `{"data":[{"index":0,"embedding":[0.1,0.2,0.3,0.4,0.5,0.6]}]}`)
	})
	resp, err := p.Embed(context.Background(), llm.EmbedRequest{Model: "gemini-embedding-001", Dimensions: 4, Inputs: []string{"a"}, Credential: keyFor("gemini")})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Vectors[0]) != 4 || resp.Vectors[0][3] != float32(0.4) || body["dimensions"] != nil || body["model"] != "gemini-embedding-001" {
		t.Fatalf("vectors %v, request %v", resp.Vectors, body)
	}
}

func TestTranscribe(t *testing.T) {
	var got struct {
		path, auth, model, format, language, filename string
		audio                                         []byte
	}
	p := compat(t, openai.Groq, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		got.model, got.format, got.language = r.FormValue("model"), r.FormValue("response_format"), r.FormValue("language")
		f, h, err := r.FormFile("file")
		if err == nil {
			got.filename = h.Filename
			got.audio, _ = io.ReadAll(f)
		}
		_, _ = fmt.Fprint(w, `{"text":"  Hello from the meeting. "}`)
	})
	if ms := p.TranscriptionModels(); len(ms) == 0 || ms[0].ID != "whisper-large-v3-turbo" {
		t.Fatalf("models: %v", ms)
	}
	resp, err := p.Transcribe(context.Background(), llm.TranscribeRequest{Model: "whisper-large-v3-turbo", Audio: []byte("ID3fake"),
		Filename: "standup.mp3", Language: "en", Credential: keyFor("groq")})
	if err != nil || resp.Text != "Hello from the meeting." {
		t.Fatalf("%v %+v", err, resp)
	}
	if got.path != "/v1/audio/transcriptions" || got.auth != "Bearer "+apiKey || got.model != "whisper-large-v3-turbo" || got.format != "json" ||
		got.language != "en" || got.filename != "standup.mp3" || string(got.audio) != "ID3fake" {
		t.Fatalf("request: %+v", got)
	}

	// Failures: classified, the key never in the error; Gemini cannot
	// transcribe; empty and oversized audio never leave.
	bad := compat(t, openai.Groq, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = fmt.Fprint(w, `{"error":{"message":"bad key `+apiKey+`","type":"invalid_request_error","code":"invalid_api_key"}}`)
	})
	var pe *llm.Error
	_, err = bad.Transcribe(context.Background(), llm.TranscribeRequest{Model: "m", Audio: []byte("x"), Credential: keyFor("groq")})
	if !errors.As(err, &pe) || pe.Code != llm.CodeAuthenticationFailed || strings.Contains(err.Error(), apiKey) {
		t.Fatalf("bad key: %v", err)
	}
	gemini := compat(t, openai.Gemini, func(w http.ResponseWriter, r *http.Request) { t.Error("must not be called") })
	if _, err := gemini.Transcribe(context.Background(), llm.TranscribeRequest{Model: "m", Audio: []byte("x"), Credential: keyFor("gemini")}); !errors.As(err, &pe) || pe.Code != llm.CodeModelNotSupported {
		t.Fatalf("gemini: %v", err)
	}
	never := compat(t, openai.Groq, func(w http.ResponseWriter, r *http.Request) { t.Error("must not be called") })
	for _, audio := range [][]byte{nil, make([]byte, openai.MaxAudioBytes+1)} {
		if _, err := never.Transcribe(context.Background(), llm.TranscribeRequest{Model: "m", Audio: audio, Credential: keyFor("groq")}); err == nil {
			t.Fatal("invalid audio was sent")
		}
	}
}
