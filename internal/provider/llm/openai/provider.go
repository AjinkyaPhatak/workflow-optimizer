package openai

import (
	"bytes"
	"context"
	"encoding/base64"
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

// Spec describes one OpenAI-compatible API: OpenAI itself, or another
// vendor's compatible endpoint (Gemini, Groq). The request and response
// formats are OpenAI's; only these details differ.
type Spec struct {
	// Name is the registry name and the credentials.provider value of the
	// API keys usable with it.
	Name  string
	Label string
	// BaseURL is the API root (Options.BaseURL overrides it).
	BaseURL string
	Models  []llm.Model
	// MaxTokensField is the request field for the response length limit:
	// "max_completion_tokens" (OpenAI) or "max_tokens".
	MaxTokensField string
	// TruncateEmbeddings: the API does not shorten embeddings itself, so
	// longer vectors are cut to the requested dimensions here (valid for
	// models trained to be shortened, such as gemini-embedding-001).
	TruncateEmbeddings bool
	// TranscriptionModels are the speech-to-text models offered (none: the
	// API cannot transcribe).
	TranscriptionModels []llm.Model
	// TranscribeWithChat: the API has no transcriptions endpoint, but its
	// chat models listen to audio, so a recording is sent to the chat
	// endpoint as an input_audio part with an instruction to write it down
	// (Gemini).
	TranscribeWithChat bool
	// FallbackModel is tried once when the API says the requested model is
	// not available to this key (HTTP 404, e.g. a legacy model) or is
	// overloaded (HTTP 503); "" never falls back.
	FallbackModel string
}

// OpenAI is the OpenAI API.
var OpenAI = Spec{
	Name: Name, Label: "OpenAI", BaseURL: DefaultBaseURL, MaxTokensField: "max_completion_tokens",
	Models: []llm.Model{
		{ID: "gpt-5", Label: "GPT-5"},
		{ID: "gpt-5-mini", Label: "GPT-5 mini"},
		{ID: "gpt-5-nano", Label: "GPT-5 nano"},
		{ID: "gpt-4.1", Label: "GPT-4.1"},
		{ID: "gpt-4.1-mini", Label: "GPT-4.1 mini"},
		{ID: "gpt-4o", Label: "GPT-4o"},
		{ID: "gpt-4o-mini", Label: "GPT-4o mini"},
	},
	TranscriptionModels: []llm.Model{{ID: "whisper-1", Label: "Whisper"}, {ID: "gpt-4o-mini-transcribe", Label: "GPT-4o mini transcribe"}},
}

// Gemini is the Gemini API through its OpenAI-compatible endpoint. Its free
// tier (an API key from Google AI Studio, no card) covers the Flash models,
// with low rate limits. The 2.5 models are legacy: Google serves them only
// to keys that used them before, so new keys get HTTP 404 for them.
var Gemini = Spec{
	Name: "gemini", Label: "Google Gemini (free)", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
	MaxTokensField: "max_tokens", TruncateEmbeddings: true, TranscribeWithChat: true, FallbackModel: "gemini-3.5-flash-lite",
	TranscriptionModels: []llm.Model{{ID: "gemini-3.5-flash-lite", Label: "Gemini 3.5 Flash-Lite (free)"}, {ID: "gemini-3.8-flash", Label: "Gemini 3.8 Flash (free)"}},
	Models: []llm.Model{
		{ID: "gemini-3.5-flash-lite", Label: "Gemini 3.5 Flash-Lite (free, fast)"},
		{ID: "gemini-3.8-flash", Label: "Gemini 3.8 Flash (free, smartest; sometimes busy)"},
		{ID: "gemini-3.6-flash", Label: "Gemini 3.6 Flash (free)"},
		{ID: "gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash-Lite (free)"},
		{ID: "gemini-3.1-pro-preview", Label: "Gemini 3.1 Pro preview (may need billing)"},
		{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash (legacy: only keys that used it before)"},
	},
}

// Groq is the Groq OpenAI-compatible API (free plan with rate limits). It
// also transcribes audio with Whisper.
var Groq = Spec{
	Name: "groq", Label: "Groq (free)", BaseURL: "https://api.groq.com/openai/v1", MaxTokensField: "max_completion_tokens",
	Models: []llm.Model{
		{ID: "openai/gpt-oss-120b", Label: "GPT-OSS 120B (free)"},
		{ID: "openai/gpt-oss-20b", Label: "GPT-OSS 20B (free, fastest)"},
		{ID: "meta-llama/llama-4-scout-17b-16e-instruct", Label: "Llama 4 Scout (free, reads images)"},
	},
	TranscriptionModels: []llm.Model{{ID: "whisper-large-v3-turbo", Label: "Whisper large v3 turbo (free)"}, {ID: "whisper-large-v3", Label: "Whisper large v3 (free)"}},
}

// Options configures the provider.
type Options struct {
	// BaseURL defaults to the spec's (tests point it at a mock server).
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
	spec    Spec
	name    string
	baseURL string
	client  *http.Client
}

var (
	_ llm.Provider  = (*Provider)(nil)
	_ llm.Describer = (*Provider)(nil)
)

// Describe implements llm.Describer: the models offered in configuration
// UIs. Other model IDs can still be configured and are sent as given.
func (p *Provider) Describe() llm.Info {
	return llm.Info{Name: p.name, Label: p.spec.Label, Models: append([]llm.Model(nil), p.spec.Models...)}
}

// Name returns the provider's registry name.
func (p *Provider) Name() string { return p.name }

// New builds the OpenAI provider.
func New(opts Options) (*Provider, error) { return NewCompatible(OpenAI, opts) }

// NewCompatible builds a provider for an OpenAI-compatible API.
func NewCompatible(spec Spec, opts Options) (*Provider, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("%w: an OpenAI-compatible provider needs a name", llm.ErrInvalidProvider)
	}
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = spec.BaseURL
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, fmt.Errorf("%w: %s base URL must be http(s), got %q", llm.ErrInvalidProvider, spec.Name, base)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &Provider{spec: spec, name: spec.Name, baseURL: base, client: client}, nil
}

// chatMessage content is text, or for messages with images a list of
// parts: {type: text} and {type: image_url, image_url: {url}}.
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type imageURL struct {
	URL string `json:"url"`
}

type contentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   *imageURL   `json:"image_url,omitempty"`
	InputAudio *inputAudio `json:"input_audio,omitempty"`
}

// inputAudio is a recording sent to a chat model (base64, with its format:
// "mp3", "wav"...).
type inputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// content is a message's text, with its images as parts (bytes as a data
// URL).
func content(m llm.Message) any {
	if len(m.Images) == 0 {
		return m.Content
	}
	parts := []contentPart{{Type: "text", Text: m.Content}}
	for _, img := range m.Images {
		u := img.URL
		if len(img.Data) > 0 {
			u = "data:" + img.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
		}
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: u}})
	}
	return parts
}

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Temperature         *float64      `json:"temperature,omitempty"`
	MaxCompletionTokens *int          `json:"max_completion_tokens,omitempty"`
	MaxTokens           *int          `json:"max_tokens,omitempty"`
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
	if err := p.checkCredential(cred); err != nil {
		return llm.Response{}, err
	}
	if strings.TrimSpace(req.Model) == "" {
		return llm.Response{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "model is required"}
	}
	return p.send(ctx, cred, req.Model, func(model string) any {
		r := req
		r.Model = model
		return p.toChatRequest(r)
	})
}

// send encodes the request for model and completes it. When the API says
// the model is not available to this key or is overloaded, the spec's
// FallbackModel is tried once.
func (p *Provider) send(ctx context.Context, cred credential.ResolvedCredential, model string, request func(model string) any) (llm.Response, error) {
	body, err := json.Marshal(request(model))
	if err != nil {
		return llm.Response{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "request could not be encoded"}
	}
	resp, err := p.complete(ctx, cred, body)
	var e *llm.Error
	fallback := p.spec.FallbackModel
	if err != nil && fallback != "" && model != fallback && errors.As(err, &e) && (e.StatusCode == http.StatusNotFound || e.StatusCode == http.StatusServiceUnavailable) {
		if body, err2 := json.Marshal(request(fallback)); err2 == nil {
			return p.complete(ctx, cred, body)
		}
	}
	return resp, err
}

// complete sends an encoded chat request and reads the answer.
func (p *Provider) complete(ctx context.Context, cred credential.ResolvedCredential, body []byte) (llm.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidRequest, Message: "request could not be built"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cred.Secret.Reveal())

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.Response{}, p.transportError(ctx, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return llm.Response{}, p.transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return llm.Response{}, p.statusError(resp, raw)
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == nil {
		return llm.Response{}, &llm.Error{Provider: p.name, Code: llm.CodeInvalidResponse, StatusCode: resp.StatusCode,
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

// checkCredential accepts only an API key of this provider.
func (p *Provider) checkCredential(cred credential.ResolvedCredential) error {
	if cred.Provider != p.name || cred.Type != credential.TypeAPIKey || cred.Secret.Empty() {
		return fmt.Errorf("%w: %s requires an %s credential for provider %q", credential.ErrInvalid, p.name, credential.TypeAPIKey, p.name)
	}
	return nil
}

func (p *Provider) toChatRequest(req llm.Request) chatRequest {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: string(llm.RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessage{Role: string(m.Role), Content: content(m)})
	}
	out := chatRequest{Model: req.Model, Messages: msgs, Temperature: req.Temperature}
	if p.spec.MaxTokensField == "max_tokens" {
		out.MaxTokens = req.MaxTokens
	} else {
		out.MaxCompletionTokens = req.MaxTokens
	}
	return out
}

// transportError classifies a failure to complete the exchange. The caller's
// own cancellation or deadline is returned as is, so the engine sees it as
// cancellation / the execution deadline / the node timeout, not as a
// provider fault.
func (p *Provider) transportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &llm.Error{Provider: p.name, Code: llm.CodeTimeout, Retryable: true, Message: "request timed out"}
	}
	// Network failures (connection refused, reset, DNS). The *url.Error
	// carries only the URL, never headers.
	return &llm.Error{Provider: p.name, Code: llm.CodeUnavailable, Retryable: true, Message: "provider could not be reached", Err: err}
}

// statusError maps an unsuccessful HTTP response to a platform error. Only
// OpenAI's machine-readable error type/code are kept: the human-readable
// message can echo request data (an invalid key is partially echoed), so it
// is never read into the error.
func (p *Provider) statusError(resp *http.Response, raw []byte) error {
	var eb errorBody
	_ = json.Unmarshal(raw, &eb)
	code := sanitize(fmt.Sprint(eb.Error.Code))
	if eb.Error.Code == nil {
		code = ""
	}
	kind := sanitize(eb.Error.Type)
	detail := strings.Trim(strings.Join([]string{kind, code}, " "), " ")

	e := &llm.Error{Provider: p.name, StatusCode: resp.StatusCode, Message: detail}
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
