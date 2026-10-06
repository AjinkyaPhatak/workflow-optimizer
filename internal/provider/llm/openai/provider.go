package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/provider/llm"
)

// Name is the provider's registry name and the credentials.provider value of
// credentials usable with it.
const Name = "openai"

// DefaultBaseURL is the public OpenAI API.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultTimeout bounds one HTTP exchange when the caller's context has no
// earlier deadline.
const DefaultTimeout = 5 * time.Minute

// maxResponseBytes caps how much of a response body is read.
const maxResponseBytes = 8 << 20

// Options configures the provider.
type Options struct {
	// BaseURL defaults to DefaultBaseURL (tests point it at a mock server).
	BaseURL string
	// HTTPClient is shared by every request; nil builds one with
	// DefaultTimeout and the default (TLS-verifying) transport.
	HTTPClient *http.Client
}

// Provider calls the OpenAI Chat Completions API. It authenticates only with
// the resolved credential carried by each request (never the environment),
// reuses one http.Client, and is safe for concurrent use: it holds no
// per-request state.
type Provider struct {
	baseURL string
	client  *http.Client
}

var _ llm.Provider = (*Provider)(nil)

// New builds the provider.
func New(opts Options) (*Provider, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, fmt.Errorf("%w: openai base URL must be http(s), got %q", llm.ErrInvalidProvider, base)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &Provider{baseURL: base, client: client}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Temperature         *float64      `json:"temperature,omitempty"`
	MaxCompletionTokens *int          `json:"max_completion_tokens,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type errorBody struct {
	Error struct {
		Type  string `json:"type"`
		Code  any    `json:"code"`
		Param string `json:"param"`
	} `json:"error"`
}

// Generate implements llm.Provider.
func (p *Provider) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	cred := req.Credential
	if cred.Provider != Name || cred.Type != credential.TypeAPIKey || cred.Secret.Empty() {
		return llm.Response{}, fmt.Errorf("%w: openai requires an %s credential for provider %q", credential.ErrInvalid, credential.TypeAPIKey, Name)
	}
	if strings.TrimSpace(req.Model) == "" {
		return llm.Response{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidRequest, Message: "model is required"}
	}
	body, err := json.Marshal(toChatRequest(req))
	if err != nil {
		return llm.Response{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidRequest, Message: "request could not be encoded"}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidRequest, Message: "request could not be built"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cred.Secret.Reveal())

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.Response{}, transportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return llm.Response{}, transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return llm.Response{}, statusError(resp, raw)
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == nil {
		return llm.Response{}, &llm.Error{Provider: Name, Code: llm.CodeInvalidResponse, StatusCode: resp.StatusCode,
			Retryable: true, Message: "response is not a chat completion with content"}
	}
	out := llm.Response{
		Content:      *parsed.Choices[0].Message.Content,
		Model:        parsed.Model,
		FinishReason: parsed.Choices[0].FinishReason,
	}
	if parsed.Usage != nil {
		out.Usage = llm.TokenUsage{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens, TotalTokens: parsed.Usage.TotalTokens}
		if out.Usage.TotalTokens == 0 {
			out.Usage.TotalTokens = out.Usage.InputTokens + out.Usage.OutputTokens
		}
	}
	return out, nil
}

func toChatRequest(req llm.Request) chatRequest {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: string(llm.RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessage{Role: string(m.Role), Content: m.Content})
	}
	return chatRequest{Model: req.Model, Messages: msgs, Temperature: req.Temperature, MaxCompletionTokens: req.MaxTokens}
}

// transportError classifies a failure to complete the exchange. The caller's
// own cancellation or deadline is returned as is, so the engine sees it as
// cancellation / the execution deadline / the node timeout, not as a
// provider fault.
func transportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &llm.Error{Provider: Name, Code: llm.CodeTimeout, Retryable: true, Message: "request timed out"}
	}
	// Network failures (connection refused, reset, DNS). The *url.Error
	// carries only the URL, never headers.
	return &llm.Error{Provider: Name, Code: llm.CodeUnavailable, Retryable: true, Message: "provider could not be reached", Err: err}
}

// statusError maps an unsuccessful HTTP response to a platform error. Only
// OpenAI's machine-readable error type/code are kept: the human-readable
// message can echo request data (an invalid key is partially echoed), so it
// is never read into the error.
func statusError(resp *http.Response, raw []byte) error {
	var eb errorBody
	_ = json.Unmarshal(raw, &eb)
	code := sanitize(fmt.Sprint(eb.Error.Code))
	if eb.Error.Code == nil {
		code = ""
	}
	kind := sanitize(eb.Error.Type)
	detail := strings.Trim(strings.Join([]string{kind, code}, " "), " ")

	e := &llm.Error{Provider: Name, StatusCode: resp.StatusCode, Message: detail}
	status := resp.StatusCode
	modelProblem := code == "model_not_found" || code == "unsupported_model" || eb.Error.Param == "model"
	switch {
	case status == http.StatusTooManyRequests:
		e.Code, e.Retryable, e.NotApplied = llm.CodeRateLimited, true, true
		if code == "insufficient_quota" {
			// Out of quota: waiting does not help.
			e.Retryable = false
		}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Code = llm.CodeAuthenticationFailed
	case modelProblem && (status == http.StatusNotFound || status == http.StatusBadRequest):
		e.Code = llm.CodeModelNotSupported
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		e.Code, e.Retryable = llm.CodeTimeout, true
	case status == http.StatusServiceUnavailable:
		e.Code, e.Retryable = llm.CodeUnavailable, true
	case status >= 500:
		e.Code, e.Retryable = llm.CodeServerError, true
	default:
		e.Code = llm.CodeInvalidRequest
	}
	if e.Retryable {
		e.RetryAfter = retryAfter(resp.Header)
	}
	return e
}

// retryAfter reads OpenAI's retry-after-ms or the standard Retry-After.
func retryAfter(h http.Header) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After-Ms")); v != "" {
		var ms float64
		if _, err := fmt.Sscanf(v, "%g", &ms); err == nil && ms > 0 {
			d := time.Duration(ms * float64(time.Millisecond))
			if d > node.MaxRetryAfter {
				d = node.MaxRetryAfter
			}
			return d
		}
	}
	if d, ok := node.ParseRetryAfter(h.Get("Retry-After"), time.Now()); ok {
		return d
	}
	return 0
}

// sanitize keeps a short machine-readable token ([a-z0-9_.-]).
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}
