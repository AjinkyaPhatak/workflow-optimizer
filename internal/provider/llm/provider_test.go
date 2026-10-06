package llm_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	providerllm "workflow-optimizer/internal/provider/llm"
)

type dummyProvider struct {
	response providerllm.Response
	err      error
}

func (d *dummyProvider) Generate(ctx context.Context, req providerllm.Request) (providerllm.Response, error) {
	if d.err != nil {
		return providerllm.Response{}, d.err
	}
	return d.response, nil
}

func TestProviderRegistry(t *testing.T) {
	reg := providerllm.NewRegistry()

	prov := &dummyProvider{
		response: providerllm.Response{Content: "dummy answer"},
	}

	// Successful register
	if err := reg.Register("dummy", prov); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Duplicate register
	if err := reg.Register("dummy", prov); !errors.Is(err, providerllm.ErrProviderAlreadyRegistered) {
		t.Fatalf("expected ErrProviderAlreadyRegistered, got %v", err)
	}

	// Invalid register
	if err := reg.Register("", prov); !errors.Is(err, providerllm.ErrInvalidProvider) {
		t.Fatalf("expected ErrInvalidProvider, got %v", err)
	}
	if err := reg.Register("valid", nil); !errors.Is(err, providerllm.ErrInvalidProvider) {
		t.Fatalf("expected ErrInvalidProvider, got %v", err)
	}

	// Successful lookup
	retrieved, err := reg.Get("dummy")
	if err != nil {
		t.Fatalf("Get('dummy') failed: %v", err)
	}

	res, err := retrieved.Generate(context.Background(), providerllm.Request{Model: "m"})
	if err != nil || res.Content != "dummy answer" {
		t.Fatalf("Generate returned unexpected result: (%#v, %v)", res, err)
	}

	// Unknown lookup
	_, err = reg.Get("unknown")
	if !errors.Is(err, providerllm.ErrProviderNotFound) {
		t.Fatalf("expected ErrProviderNotFound, got %v", err)
	}

	// List
	list := reg.List()
	if len(list) != 1 || list[0] != "dummy" {
		t.Fatalf("List() = %v, want ['dummy']", list)
	}
}

func TestProviderRegistryFreezeAndConcurrentLookup(t *testing.T) {
	reg := providerllm.NewRegistry()
	if err := reg.Register("openai", &dummyProvider{}); err != nil {
		t.Fatal(err)
	}
	reg.Freeze()
	if err := reg.Register("late", &dummyProvider{}); !errors.Is(err, providerllm.ErrRegistryFrozen) {
		t.Fatalf("register after freeze: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := reg.Get("openai"); err != nil {
					t.Error(err)
					return
				}
				if _, err := reg.Get("missing"); !errors.Is(err, providerllm.ErrProviderNotFound) {
					t.Error(err)
					return
				}
				_ = reg.List()
			}
		}()
	}
	wg.Wait()
}
