// Phase 5 tests for ValidateWithRegistry and Bootstrap
package app_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	providerllm "workflow-optimizer/internal/provider/llm"
	"workflow-optimizer/internal/workflow"
)

// helper to create a minimal config (uses zero value)
func testConfig() config.Config { return config.Config{} }

func TestBootstrapRegistersAllV1Nodes(t *testing.T) {
	cfg := testConfig()
	a, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatalf("Bootstrap error: %v", err)
	}
	// Expect 11 V1 nodes registered
	got := len(a.NodeRegistry.List())
	if got != 11 {
		t.Fatalf("expected 11 V1 nodes, got %d", got)
	}
	// Provider registry should be non-nil
	if a.ProviderRegistry == nil {
		t.Fatalf("provider registry is nil")
	}
}

func TestValidateWithRegistry_KnownNodeTypes(t *testing.T) {
	cfg := testConfig()
	a, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatalf("Bootstrap error: %v", err)
	}
	def := workflow.Definition{Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{{
			ID: "input_1", Type: "input", Name: "input", Position: &workflow.Position{X: 0, Y: 0}, Config: map[string]any{},
		}},
		Edges:    []workflow.Edge{},
		Settings: map[string]any{},
	}
	if err := def.ValidateWithRegistry(a.NodeRegistry); err != nil {
		t.Fatalf("ValidateWithRegistry failed for known node type: %v", err)
	}
}

func TestValidateWithRegistry_UnknownNodeType(t *testing.T) {
	cfg := testConfig()
	a, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatalf("Bootstrap error: %v", err)
	}
	def := workflow.Definition{Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{{
			ID: "node1", Type: "gmail.send", Name: "gmail", Position: &workflow.Position{X: 0, Y: 0}, Config: map[string]any{},
		}},
		Edges:    []workflow.Edge{},
		Settings: map[string]any{},
	}
	err = def.ValidateWithRegistry(a.NodeRegistry)
	if err == nil || !strings.Contains(err.Error(), "unknown node type") {
		t.Fatalf("expected unknown node type error, got %v", err)
	}
}

func TestValidateWithRegistry_NilProvider(t *testing.T) {
	// Reuse simple definition from workflow tests
	def := workflow.Definition{Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{{
			ID: "input_1", Type: "input", Name: "input", Position: &workflow.Position{X: 0, Y: 0}, Config: map[string]any{},
		}},
		Edges:    []workflow.Edge{},
		Settings: map[string]any{},
	}
	if err := def.ValidateWithRegistry(nil); err != nil {
		t.Fatalf("ValidateWithRegistry with nil provider should succeed, got %v", err)
	}
}

// Phase 11: the provider registry holds OpenAI and is frozen at startup; the
// graph executor's package never imports a provider.
func TestBootstrapProviderRegistry(t *testing.T) {
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.ProviderRegistry.List(); len(got) != 1 || got[0] != "openai" {
		t.Fatalf("providers = %v", got)
	}
	openAI, _ := a.ProviderRegistry.Get("openai")
	if err := a.ProviderRegistry.Register("late", openAI); !errors.Is(err, providerllm.ErrRegistryFrozen) {
		t.Fatalf("registration after startup: %v", err)
	}
	if _, err := app.BootstrapWith(config.Config{OpenAIBaseURL: "ftp://nope"}, app.Dependencies{}); err == nil {
		t.Fatal("invalid OpenAI base URL accepted")
	}
	out, err := exec.Command("go", "list", "-deps", "workflow-optimizer/internal/execution").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "workflow-optimizer/internal/provider") || strings.HasPrefix(dep, "workflow-optimizer/internal/credential") {
			t.Fatalf("the execution package depends on %s", dep)
		}
	}
}

// The canonical V1 graph Input -> LLM -> Output is executable with the
// built-in catalog (Phase 13: json ports connect to typed ports).
func TestCanonicalLLMWorkflowIsExecutable(t *testing.T) {
	a, err := app.Bootstrap(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	pos := &workflow.Position{}
	def := workflow.Definition{
		Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "llm", Type: "llm", Name: "LLM", Position: pos, Config: map[string]any{"provider": "openai", "model": "gpt-5"}},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "llm", TargetPort: "prompt"},
			{ID: "e2", Source: "llm", SourcePort: "response", Target: "out", TargetPort: "value"},
		},
		Settings: map[string]any{},
	}
	if res := workflow.NewValidator(a.NodeRegistry).Validate(def); !res.Valid {
		t.Fatalf("canonical workflow invalid: %+v", res.Errors)
	}
}
