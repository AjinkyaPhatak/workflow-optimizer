// Phase 5 tests for ValidateWithRegistry and Bootstrap
package app_test

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
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
	// Expect the 11 V1 nodes plus 5 Gmail actions (Phase C3), 3 Google
	// Sheets and 2 Google Calendar actions (C4), 4 Notion actions (C5) and
	// 2 Discord actions (C6), Extract Text (D1) and the 2 knowledge nodes (D2)
	got := len(a.NodeRegistry.List())
	if got != 30 {
		t.Fatalf("expected 30 nodes (11 V1 + 5 Gmail + 3 Sheets + 2 Calendar + 4 Notion + 2 Discord + 3 knowledge), got %d", got)
	}
	for _, typ := range []string{"gmail.search", "gmail.read", "gmail.create_draft", "gmail.send", "gmail.reply",
		"google_sheets.read_rows", "google_sheets.append_row", "google_sheets.update_row",
		"google_calendar.list_events", "google_calendar.create_event",
		"notion.search", "notion.query_database", "notion.create_page", "notion.update_page",
		"discord.send_message", "discord.read_messages"} {
		if !a.NodeRegistry.HasNodeType(typ) {
			t.Fatalf("%s is not registered", typ)
		}
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
			ID: "node1", Type: "trello.create_card", Name: "trello", Position: &workflow.Position{X: 0, Y: 0}, Config: map[string]any{},
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

// Phase C8: every choice list a setting declares is served by its node, and
// its dependencies are settings of that node.
func TestOptionSourcesAreServed(t *testing.T) {
	a, err := app.Bootstrap(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, d := range a.NodeRegistry.Definitions() {
		n, _ := a.NodeRegistry.Resolve(d.Type)
		p, _ := n.(integration.OptionsProvider)
		for _, f := range d.Config {
			for _, src := range []string{f.OptionsSource, f.KeysSource} {
				if src == "" {
					continue
				}
				found++
				if p == nil || !slices.Contains(p.OptionSources(), src) {
					t.Errorf("%s.%s: no %q options", d.Type, f.Name, src)
				}
			}
			if f.KeysSource != "" && (!f.KeyValue || f.Type != node.ValueTypeObject) {
				t.Errorf("%s.%s: keys need a key/value object field", d.Type, f.Name)
			}
			for _, dep := range f.DependsOn {
				if _, ok := d.GetConfigField(dep); !ok {
					t.Errorf("%s.%s depends on unknown %q", d.Type, f.Name, dep)
				}
			}
		}
	}
	if found < 12 {
		t.Fatalf("only %d option sources declared", found)
	}
}
