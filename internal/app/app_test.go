// Phase 5 tests for ValidateWithRegistry and Bootstrap
package app_test

import (
	"strings"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
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
