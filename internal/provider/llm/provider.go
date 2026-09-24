// Package llm defines the provider-neutral LLM contract.
package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	ErrProviderAlreadyRegistered = errors.New("llm provider already registered")
	ErrProviderNotFound          = errors.New("llm provider not found")
	ErrInvalidProvider           = errors.New("invalid llm provider")
)

// Request is a provider-neutral generation request.
type Request struct {
	Prompt      string         `json:"prompt"`
	System      string         `json:"system,omitempty"`
	Model       string         `json:"model,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Options     map[string]any `json:"options,omitempty"`
}

// Response is a provider-neutral generation response.
type Response struct {
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Raw          any    `json:"raw,omitempty"`
}

// Provider is implemented by concrete LLM integrations (e.g. OpenAI, Anthropic, Gemini).
// LLM nodes interact strictly with this interface, never with provider-specific HTTP clients.
type Provider interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

// Registry maps provider names (e.g., "openai", "anthropic") to concrete Provider implementations.
type Registry interface {
	Register(name string, provider Provider) error
	Get(name string) (Provider, error)
	List() []string
}

type inMemoryRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
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
