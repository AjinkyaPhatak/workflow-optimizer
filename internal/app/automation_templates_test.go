package app_test

import (
	"testing"
	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/templates"
	"workflow-optimizer/internal/toolbox"
)

func TestNativeAutomationTemplatesPreserveCatalogAndActions(t *testing.T) {
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	box := &toolbox.Toolbox{Nodes: a.NodeRegistry}
	converted, err := box.BuiltinTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != len(templates.List()) {
		t.Fatal("missing bundled templates")
	}
	for i, original := range templates.List() {
		got := converted[i]
		if got.ID != original.ID || got.Name != original.Name {
			t.Fatal("template identity changed")
		}
		if check := automation.Check(got.Plan, box.Catalog()); len(check.Diagnostics) > 0 {
			t.Fatalf("%s: %+v", got.ID, check.Diagnostics)
		}
		want := map[string]int{}
		actual := map[string]int{}
		for _, n := range original.Definition.Nodes {
			for _, d := range a.NodeRegistry.Definitions() {
				if d.Type == n.Type && d.SideEffects != "" {
					want["node."+n.Type]++
				}
			}
		}
		for _, s := range got.Plan.Steps {
			for _, d := range box.Catalog() {
				if d.ID == s.Tool && d.SideEffects != "" {
					actual[s.Tool]++
				}
			}
		}
		if len(want) != len(actual) {
			t.Fatalf("%s changed outgoing actions", got.ID)
		}
		for tool, count := range want {
			if actual[tool] != count {
				t.Fatalf("%s changed %s", got.ID, tool)
			}
		}
	}
}
