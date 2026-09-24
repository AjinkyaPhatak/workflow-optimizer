package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefinitionJSONRoundTrip(t *testing.T) {
	original := simpleDefinition()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Definition
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("validate round trip: %v", err)
	}
	if decoded.Nodes[1].Config["prompt"] != "Explain {{input.topic}} in simple terms." {
		t.Fatalf("interpolation was not preserved: %#v", decoded.Nodes[1].Config["prompt"])
	}
	if decoded.Nodes[1].Config["credential_id"] != "credential_1" {
		t.Fatalf("credential reference was not preserved")
	}
}

func TestValidGraphFixtures(t *testing.T) {
	for name, definition := range map[string]Definition{
		"simple":    simpleDefinition(),
		"branching": branchingDefinition(),
		"merge":     mergeDefinition(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := definition.Validate(); err != nil {
				t.Fatalf("valid definition rejected: %v", err)
			}
		})
	}
}

func TestStructuralValidation(t *testing.T) {
	tests := []struct {
		name       string
		definition Definition
		contains   string
	}{
		{"duplicate node", duplicateNodeDefinition(), "duplicates a node ID"},
		{"duplicate edge", duplicateEdgeDefinition(), "duplicates an edge ID"},
		{"missing node", missingNodeDefinition(), "references an unknown node"},
		{"cycle", cyclicDefinition(), "directed acyclic graph"},
		{"missing top-level field", Definition{Version: DefinitionSchemaVersion, Edges: []Edge{}, Settings: map[string]any{}}, "nodes: is required"},
		{"unsupported version", Definition{Version: 2, Nodes: []Node{}, Edges: []Edge{}, Settings: map[string]any{}}, "unsupported schema version"},
		{"missing node fields", Definition{Version: DefinitionSchemaVersion, Nodes: []Node{{}}, Edges: []Edge{}, Settings: map[string]any{}}, "nodes[0].id: is required"},
		{"invalid interpolation", Definition{Version: DefinitionSchemaVersion, Nodes: []Node{node("input", "input")}, Edges: []Edge{}, Settings: map[string]any{"prompt": "{{not valid}}"}}, "invalid variable reference"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.definition.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Validate() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestVariableReferences(t *testing.T) {
	references := VariableReferences("Hello {{customer.name}}, ask {{llm_1.response}}.")
	if len(references) != 2 || references[0].Expression != "customer.name" || references[1].Expression != "llm_1.response" {
		t.Fatalf("references = %#v", references)
	}
}

func simpleDefinition() Definition {
	return Definition{
		Version: DefinitionSchemaVersion,
		Nodes: []Node{
			node("input_1", "input"),
			{ID: "llm_1", Type: "llm", Name: "LLM", Position: &Position{X: 240, Y: 0}, Config: map[string]any{"provider": "openai", "model": "gpt-5", "credential_id": "credential_1", "temperature": 0.2, "prompt": "Explain {{input.topic}} in simple terms.", "options": map[string]any{"stream": false}}},
			node("output_1", "output"),
		},
		Edges: []Edge{
			{ID: "edge_1", Source: "input_1", SourcePort: "query", Target: "llm_1", TargetPort: "prompt"},
			{ID: "edge_2", Source: "llm_1", SourcePort: "response", Target: "output_1", TargetPort: "result"},
		},
		Settings: map[string]any{"timeout_ms": 300000, "max_retries": 3},
	}
}

func branchingDefinition() Definition {
	return Definition{Version: DefinitionSchemaVersion, Nodes: []Node{node("input", "input"), node("condition", "condition"), node("success", "output"), node("failure", "output")}, Edges: []Edge{{ID: "input-condition", Source: "input", SourcePort: "value", Target: "condition", TargetPort: "value"}, {ID: "condition-success", Source: "condition", SourcePort: "true", Target: "success", TargetPort: "value"}, {ID: "condition-failure", Source: "condition", SourcePort: "false", Target: "failure", TargetPort: "value"}}, Settings: map[string]any{}}
}

func mergeDefinition() Definition {
	return Definition{Version: DefinitionSchemaVersion, Nodes: []Node{node("input", "input"), node("a", "text"), node("b", "text"), node("merge", "merge")}, Edges: []Edge{{ID: "input-a", Source: "input", SourcePort: "value", Target: "a", TargetPort: "input"}, {ID: "input-b", Source: "input", SourcePort: "value", Target: "b", TargetPort: "input"}, {ID: "a-merge", Source: "a", SourcePort: "output", Target: "merge", TargetPort: "left"}, {ID: "b-merge", Source: "b", SourcePort: "output", Target: "merge", TargetPort: "right"}}, Settings: map[string]any{}}
}

func duplicateNodeDefinition() Definition {
	d := simpleDefinition()
	d.Nodes = append(d.Nodes, node("input_1", "text"))
	return d
}

func duplicateEdgeDefinition() Definition {
	d := simpleDefinition()
	d.Edges = append(d.Edges, d.Edges[0])
	return d
}

func missingNodeDefinition() Definition {
	d := simpleDefinition()
	d.Edges[0].Target = "missing"
	return d
}

func cyclicDefinition() Definition {
	d := simpleDefinition()
	d.Edges = append(d.Edges, Edge{ID: "cycle", Source: "output_1", SourcePort: "result", Target: "input_1", TargetPort: "query"})
	return d
}

func node(id, nodeType string) Node {
	return Node{ID: id, Type: nodeType, Name: id, Position: &Position{}, Config: map[string]any{}}
}
