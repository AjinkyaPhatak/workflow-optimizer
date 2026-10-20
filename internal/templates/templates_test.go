package templates_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/templates"
	"workflow-optimizer/internal/workflow"
)

func TestTemplatesAreValidExecutableWorkflows(t *testing.T) {
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	v := workflow.NewValidator(a.NodeRegistry)
	list := templates.List()
	if len(list) < 3 {
		t.Fatalf("templates: %d", len(list))
	}
	for _, tpl := range list {
		if tpl.ID == "" || tpl.Name == "" || tpl.Description == "" {
			t.Errorf("%q: metadata missing", tpl.ID)
		}
		// Only templates whose nodes and wiring are executable ship.
		if res := v.Validate(tpl.Definition); !res.Valid {
			t.Errorf("%s: %+v", tpl.ID, res.Errors)
		}
		def, err := templates.Instantiate(tpl.ID)
		if err != nil {
			t.Fatal(err)
		}
		if res := v.Validate(def); !res.Valid {
			t.Errorf("%s instance: %+v", tpl.ID, res.Errors)
		}
	}
}

func TestInstantiateFreshIDsAndPreservedEdges(t *testing.T) {
	tpl, err := templates.Get("structured-ai-response")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := templates.Instantiate(tpl.ID)
	b, _ := templates.Instantiate(tpl.ID)

	oldToNew := map[string]string{}
	seen := map[string]bool{}
	for i, n := range a.Nodes {
		orig := tpl.Definition.Nodes[i]
		if n.ID == orig.ID || !strings.HasPrefix(n.ID, n.Type+"_") || seen[n.ID] {
			t.Fatalf("node %q: fresh unique ID expected, got %q", orig.ID, n.ID)
		}
		if n.ID == b.Nodes[i].ID {
			t.Fatalf("two instances share node ID %q", n.ID)
		}
		seen[n.ID] = true
		oldToNew[orig.ID] = n.ID
		if *n.Position != *orig.Position || n.Name != orig.Name || n.Type != orig.Type {
			t.Fatalf("node %q changed beyond its ID", orig.ID)
		}
	}
	for i, e := range a.Edges {
		orig := tpl.Definition.Edges[i]
		if e.ID == orig.ID || e.Source != oldToNew[orig.Source] || e.Target != oldToNew[orig.Target] ||
			e.SourcePort != orig.SourcePort || e.TargetPort != orig.TargetPort {
			t.Fatalf("edge %q not preserved: %+v", orig.ID, e)
		}
	}
	if len(a.Variables) != len(tpl.Definition.Variables) || a.Variables[0].Name != "audience" {
		t.Fatalf("variables: %+v", a.Variables)
	}
	// {{input.query}} is the workflow input even though a node was named
	// "input" in the template.
	var template string
	for _, n := range a.Nodes {
		if n.Type == "prompt" {
			template = n.Config["template"].(string)
		}
	}
	if !strings.Contains(template, "{{input.query}}") || !strings.Contains(template, "{{audience}}") {
		t.Fatalf("template references: %q", template)
	}
	// The catalog itself is never modified by instantiation.
	if tpl2, _ := templates.Get(tpl.ID); tpl2.Definition.Nodes[0].ID != tpl.Definition.Nodes[0].ID {
		t.Fatal("catalog mutated")
	}
	if _, err := templates.Instantiate("nope"); err != templates.ErrNotFound {
		t.Fatalf("unknown template: %v", err)
	}
}

func TestInstantiateKeepsTransformMapping(t *testing.T) {
	def, _ := templates.Instantiate("data-transformation")
	var mapping map[string]any
	for _, n := range def.Nodes {
		if n.Type == "transform" {
			mapping = n.Config["mapping"].(map[string]any)
		}
	}
	if mapping["question"] != "{{input.query}}" || mapping["source"] != "{{source}}" || mapping["received"] != true {
		t.Fatalf("mapping: %#v", mapping)
	}
}

// The gallery templates (Phase K) are built for people: every one has a
// known category, each Input card asks one named question, and every
// {{name}} a step uses is fed by a connection, so the Run window asks only
// the Input cards' questions.
func TestGalleryTemplatesAreSelfContained(t *testing.T) {
	list := templates.List()
	if len(list) < 25 {
		t.Fatalf("the gallery has %d templates", len(list))
	}
	known := map[string]bool{}
	for _, c := range templates.Categories {
		known[c] = true
	}
	ref := regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)`)
	names := map[string]bool{}
	for _, tpl := range list {
		if !known[tpl.Category] {
			t.Errorf("%s: unknown category %q", tpl.ID, tpl.Category)
		}
		if names[tpl.Name] {
			t.Errorf("%s: duplicate name %q", tpl.ID, tpl.Name)
		}
		names[tpl.Name] = true
		if tpl.Category == "Advanced" {
			continue // the developer examples use {{input.x}} and variables
		}
		if len(tpl.Definition.Variables) > 0 {
			t.Errorf("%s: gallery templates ask through Input cards, not variables", tpl.ID)
		}
		fed := map[string]map[string]bool{}
		for _, e := range tpl.Definition.Edges {
			if name, ok := workflow.VariableInput(e.TargetPort); ok {
				if fed[e.Target] == nil {
					fed[e.Target] = map[string]bool{}
				}
				fed[e.Target][name] = true
			}
		}
		for _, n := range tpl.Definition.Nodes {
			raw, _ := json.Marshal(n.Config)
			used := map[string]bool{}
			for _, m := range ref.FindAllStringSubmatch(string(raw), -1) {
				used[m[1]] = true
				if !fed[n.ID][m[1]] {
					t.Errorf("%s: %s uses {{%s}} but nothing is connected to it", tpl.ID, n.ID, m[1])
				}
			}
			for name := range fed[n.ID] {
				if !used[name] {
					t.Errorf("%s: %s has {{%s}} connected but does not use it", tpl.ID, n.ID, name)
				}
			}
			if n.Type == "input" && len(tpl.Definition.Nodes) > 0 && n.Name != "Start" && n.Config["field"] == nil {
				t.Errorf("%s: input %s has no Field name", tpl.ID, n.ID)
			}
		}
	}
}

// A {{name}} fed by a variable input stays {{name}} in a new workflow, even
// when the template has a node with that ID (e.g. an Input card "question"
// feeding {{question}}): only node references are renamed.
func TestInstanceKeepsVariableInputNames(t *testing.T) {
	ref := regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)`)
	for _, tpl := range templates.List() {
		if tpl.Category == "Advanced" {
			continue
		}
		def, err := templates.Instantiate(tpl.ID)
		if err != nil {
			t.Fatal(err)
		}
		fed := map[string]map[string]bool{}
		for _, e := range def.Edges {
			if name, ok := workflow.VariableInput(e.TargetPort); ok {
				if fed[e.Target] == nil {
					fed[e.Target] = map[string]bool{}
				}
				fed[e.Target][name] = true
			}
		}
		for _, n := range def.Nodes {
			raw, _ := json.Marshal(n.Config)
			for _, m := range ref.FindAllStringSubmatch(string(raw), -1) {
				if !fed[n.ID][m[1]] {
					t.Errorf("%s: %s uses {{%s}} in the new workflow but nothing feeds it", tpl.ID, n.Name, m[1])
				}
			}
		}
	}
}
