// Package llm defines the provider-neutral LLM contract: the request and
// response every provider speaks, the platform-level provider errors, and the
// provider registry. Concrete providers (internal/provider/llm/openai) adapt
// a vendor API to this contract; nothing here is vendor-specific.
package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"workflow-optimizer/internal/credential"
)

var (
	ErrProviderAlreadyRegistered = errors.New("llm provider already registered")
	ErrProviderNotFound          = errors.New("llm provider not found")
	ErrInvalidProvider           = errors.New("invalid llm provider")
	// ErrRegistryFrozen rejects registration after application startup.
	ErrRegistryFrozen = errors.New("llm provider registry is frozen")
)

// Role is the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of a conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Request is a provider-neutral generation request.
type Request struct {
	Model string `json:"model"`
	// System holds the system instructions (providers place them where
	// their API expects them).
	System      string            `json:"system,omitempty"`
	Messages    []Message         `json:"messages"`
	Temperature *float64          `json:"temperature,omitempty"`
	MaxTokens   *int              `json:"max_tokens,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Credential is the resolved credential the provider authenticates
	// with. Runtime only: never serialized, never logged.
	Credential credential.ResolvedCredential `json:"-"`
}

// TokenUsage reports token consumption.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Response is a provider-neutral generation response.
type Response struct {
	Content      string     `json:"content"`
	Model        string     `json:"model,omitempty"`
	Usage        TokenUsage `json:"usage"`
	FinishReason string     `json:"finish_reason,omitempty"`
}

// Provider is implemented by concrete LLM integrations (OpenAI; later
// Anthropic, Gemini). LLM nodes use only this interface. Implementations must
// be safe for concurrent use and must authenticate with req.Credential only.
type Provider interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

// ErrorCode is a platform-level provider error code.
type ErrorCode string

const (
	CodeRateLimited          ErrorCode = "RATE_LIMITED"
	CodeServerError          ErrorCode = "PROVIDER_SERVER_ERROR"
	CodeUnavailable          ErrorCode = "PROVIDER_UNAVAILABLE"
	CodeTimeout              ErrorCode = "PROVIDER_TIMEOUT"
	CodeAuthenticationFailed ErrorCode = "AUTHENTICATION_FAILED"
	CodeInvalidRequest       ErrorCode = "INVALID_PROVIDER_REQUEST"
	CodeModelNotSupported    ErrorCode = "MODEL_NOT_SUPPORTED"
	CodeInvalidResponse      ErrorCode = "INVALID_PROVIDER_RESPONSE"
)

// Error is a provider failure normalized to platform semantics. Providers
// classify (Code, Retryable, RetryAfter, NotApplied); the Phase 10 engine
// owns retrying. Message is written by the provider adapter and must never
// contain secrets or raw response bodies.
type Error struct {
	Provider   string
	Code       ErrorCode
	Message    string
	StatusCode int // HTTP status, 0 when none
	Retryable  bool
	// RetryAfter is the provider's explicit delay (Retry-After), if any.
	RetryAfter time.Duration
	// NotApplied states the provider refused the request without processing
	// it (e.g. rate limiting).
	NotApplied bool
	// Err is a non-sensitive cause (e.g. a context or network error).
	Err error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s: %s", e.Provider, e.Code)
	if e.StatusCode != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Registry maps provider names ("openai") to providers. It is distinct from
// the node registry. Lookups are safe for concurrent use; registration is a
// startup activity, refused once the registry is frozen.
type Registry interface {
	Register(name string, provider Provider) error
	Get(name string) (Provider, error)
	List() []string
	// Freeze makes the registry immutable (end of application startup).
	Freeze()
}

type inMemoryRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	frozen    bool
}

// NewRegistry constructs a thread-safe in-memory provider registry.
func NewRegistry() Registry {
	return &inMemoryRegistry{
		providers: make(map[string]Provider),
	}
}

// Register adds an LLM provider implementation under a unique name.
func (r *inMemoryRegistry) Register(name string, provider Provider) error {
	if name == "" || provider == nil {
		return fmt.Errorf("%w: name and provider must not be empty/nil", ErrInvalidProvider)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frozen {
		return fmt.Errorf("%w: cannot register %q", ErrRegistryFrozen, name)
	}
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("%w: %q", ErrProviderAlreadyRegistered, name)
	}
	r.providers[name] = provider
	return nil
}

// Get looks up a registered LLM provider by name.
func (r *inMemoryRegistry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, exists := r.providers[name]
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrProviderNotFound, name)
	}
	return p, nil
}

// List returns a sorted list of registered provider names.
func (r *inMemoryRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]string, 0, len(r.providers))
	for k := range r.providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Freeze implements Registry.
func (r *inMemoryRegistry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Model is a model a provider offers.
type Model struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Info describes a provider for configuration UIs: its display name and the
// models it offers. It is metadata only; any model ID may still be sent.
type Info struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	Models []Model `json:"models"`
}

// Describer is implemented by providers that describe themselves.
type Describer interface {
	Describe() Info
}

// Describe returns the registered providers' descriptions, sorted by name.
// A provider that does not implement Describer is listed by name only.
func Describe(reg Registry) []Info {
	out := []Info{}
	for _, name := range reg.List() {
		info := Info{Name: name, Label: name}
		if p, err := reg.Get(name); err == nil {
			if d, ok := p.(Describer); ok {
				info = d.Describe()
				info.Name = name
			}
		}
		out = append(out, info)
	}
	return out
}
