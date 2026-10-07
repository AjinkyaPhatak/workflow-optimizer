package workflow

import (
	"context"
	"reflect"
	"testing"

	nodemeta "workflow-optimizer/internal/node"
)

type validatorTestNode struct{ typ string }

func (n validatorTestNode) Type() string { return n.typ }
func (validatorTestNode) Execute(context.Context, nodemeta.NodeInput) (nodemeta.NodeOutput, error) {
	return nodemeta.NodeOutput{}, nil
}

func validatorRegistry(t *testing.T, valueType nodemeta.ValueType) nodemeta.Registry {
	t.Helper()
	r := nodemeta.NewRegistry()
	defs := []nodemeta.NodeDefinition{
		{Type: "start", Name: "Start", Category: nodemeta.CategoryGeneral, Role: nodemeta.SemanticRoleEntry, Outputs: []nodemeta.PortDefinition{nodemeta.NewPortDefinition("out", valueType, true, "")}},
		{Type: "end", Name: "End", Category: nodemeta.CategoryGeneral, Role: nodemeta.SemanticRoleExit, Inputs: []nodemeta.PortDefinition{nodemeta.NewPortDefinition("in", valueType, true, "")}},
		{Type: "single", Name: "Single", Category: nodemeta.CategoryGeneral, Inputs: []nodemeta.PortDefinition{nodemeta.NewPortDefinition("in", valueType, true, "")}, Outputs: []nodemeta.PortDefinition{nodemeta.NewPortDefinition("out", valueType, true, "")}},
		{Type: "multi", Name: "Multi", Category: nodemeta.CategoryGeneral, Inputs: []nodemeta.PortDefinition{nodemeta.NewMultiPortDefinition("in", valueType, true, "")}, Outputs: []nodemeta.PortDefinition{nodemeta.NewPortDefinition("out", valueType, true, "")}},
	}
	for _, d := range defs {
		if err := r.RegisterNode(validatorTestNode{d.Type}, d); err != nil {
			t.Fatal(err)
		}
	}
	return r
}
func validatorNode(id, typ string) Node {
	return Node{ID: id, Type: typ, Name: id, Position: &Position{}, Config: map[string]any{}}
}
func validatorDefinition(nodes []Node, edges []Edge) Definition {
	return Definition{Version: DefinitionSchemaVersion, Nodes: nodes, Edges: edges, Settings: map[string]any{}}
}
func codes(result ValidationResult) []ValidationErrorCode {
	out := make([]ValidationErrorCode, len(result.Errors))
	for i, e := range result.Errors {
		out[i] = e.Code
	}
	return out
}
func containsCode(result ValidationResult, want ValidationErrorCode) bool {
	for _, e := range result.Errors {
		if e.Code == want {
			return true
		}
	}
	return false
}

func TestGraphValidatorExecutableAndDraftModes(t *testing.T) {
	v := NewValidator(validatorRegistry(t, nodemeta.ValueTypeString))
	valid := validatorDefinition([]Node{validatorNode("a", "start"), validatorNode("b", "end")}, []Edge{{ID: "e", Source: "a", SourcePort: "out", Target: "b", TargetPort: "in"}})
	if got := v.Validate(valid); !got.Valid {
		t.Fatalf("valid executable: %#v", got.Errors)
	}
	incomplete := validatorDefinition([]Node{validatorNode("x", "single")}, []Edge{})
	if got := v.ValidateWithOptions(incomplete, ValidationOptions{Mode: ValidationDraft}); !got.Valid {
		t.Fatalf("incomplete draft: %#v", got.Errors)
	}
	if got := v.Validate(incomplete); got.Valid || !containsCode(got, ErrMissingInputNode) || !containsCode(got, ErrMissingRequiredInput) {
		t.Fatalf("strict executable result: %#v", got.Errors)
	}
	bad := validatorDefinition([]Node{validatorNode("a", "start")}, []Edge{{ID: "bad", Source: "missing", SourcePort: "out", Target: "a", TargetPort: "out"}})
	if got := v.ValidateWithOptions(bad, ValidationOptions{Mode: ValidationDraft}); got.Valid || !containsCode(got, ErrInvalidSourceNode) {
		t.Fatalf("invalid draft accepted: %#v", got.Errors)
	}
}

func TestGraphValidatorTypedConnectionsAndInterpolation(t *testing.T) {
	v := NewValidator(validatorRegistry(t, nodemeta.ValueTypeString))
	wrong := validatorDefinition([]Node{validatorNode("a", "start"), validatorNode("b", "end")}, []Edge{{ID: "e", Source: "a", SourcePort: "out", Target: "b", TargetPort: "in"}})
	// Replace end metadata with number to exercise incompatible type through the registry.
	r := nodemeta.NewRegistry()
	for _, d := range []nodemeta.NodeDefinition{{Type: "start", Name: "Start", Category: nodemeta.CategoryGeneral, Role: nodemeta.SemanticRoleEntry, Outputs: []nodemeta.PortDefinition{nodemeta.NewPortDefinition("out", nodemeta.ValueTypeString, true, "")}}, {Type: "end", Name: "End", Category: nodemeta.CategoryGeneral, Role: nodemeta.SemanticRoleExit, Inputs: []nodemeta.PortDefinition{nodemeta.NewPortDefinition("in", nodemeta.ValueTypeNumber, true, "")}}} {
		if err := r.RegisterNode(validatorTestNode{d.Type}, d); err != nil {
			t.Fatal(err)
		}
	}
	v = NewValidator(r)
	got := v.Validate(wrong)
	if !containsCode(got, ErrIncompatiblePortTypes) || !containsCode(got, ErrMissingRequiredInput) {
		t.Fatalf("typed invalid edge must not satisfy input: %#v", got.Errors)
	}
	valid := validatorDefinition([]Node{validatorNode("a", "start"), validatorNode("b", "end")}, []Edge{{ID: "e", Source: "a", SourcePort: "out", Target: "b", TargetPort: "in"}})
	v = NewValidator(validatorRegistry(t, nodemeta.ValueTypeString))
	valid.Settings["text"] = "{{variable}} {{query}} {{customer.name}} {{a.b}}"
	if got := v.Validate(valid); !got.Valid {
		t.Fatalf("valid static references: %#v", got.Errors)
	}
	valid.Settings["text"] = "{{broken"
	if got := v.Validate(valid); !containsCode(got, ErrInvalidVariableReference) {
		t.Fatalf("malformed interpolation: %#v", got.Errors)
	}
}

func TestGraphValidatorDeterministicStructuredErrors(t *testing.T) {
	v := NewValidator(validatorRegistry(t, nodemeta.ValueTypeString))
	d := validatorDefinition([]Node{validatorNode("a", "unknown"), validatorNode("a", "start")}, []Edge{{ID: "e", Source: "none", SourcePort: "x", Target: "also-none", TargetPort: "y"}, {ID: "e", Source: "none", SourcePort: "x", Target: "also-none", TargetPort: "y"}})
	first := v.Validate(d)
	if len(first.Errors) < 3 {
		t.Fatalf("wanted independent errors: %#v", first.Errors)
	}
	for i := 0; i < 10; i++ {
		if got := v.Validate(d); !reflect.DeepEqual(codes(first), codes(got)) {
			t.Fatalf("nondeterministic: %v != %v", codes(first), codes(got))
		}
	}
}

func TestGraphValidatorFanInInBothModes(t *testing.T) {
	v := NewValidator(validatorRegistry(t, nodemeta.ValueTypeString))
	for _, test := range []struct {
		name, target string
		mode         ValidationMode
		valid        bool
	}{
		{"draft-single", "single", ValidationDraft, false}, {"draft-multi", "multi", ValidationDraft, true},
		{"exec-single", "single", ValidationExecutable, false}, {"exec-multi", "multi", ValidationExecutable, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			nodes := []Node{validatorNode("a", "start"), validatorNode("b", "start"), validatorNode("m", test.target), validatorNode("z", "end")}
			edges := []Edge{{ID: "a", Source: "a", SourcePort: "out", Target: "m", TargetPort: "in"}, {ID: "b", Source: "b", SourcePort: "out", Target: "m", TargetPort: "in"}, {ID: "z", Source: "m", SourcePort: "out", Target: "z", TargetPort: "in"}}
			got := v.ValidateWithOptions(validatorDefinition(nodes, edges), ValidationOptions{Mode: test.mode})
			if got.Valid != test.valid {
				t.Fatalf("errors: %#v", got.Errors)
			}
			if !test.valid && !containsCode(got, ErrMultipleConnections) {
				t.Fatalf("missing fan-in error: %#v", got.Errors)
			}
		})
	}
}

func TestGraphValidatorPortsDuplicatesAndCycles(t *testing.T) {
	v := NewValidator(validatorRegistry(t, nodemeta.ValueTypeString))
	base := []Node{validatorNode("a", "start"), validatorNode("b", "end"), validatorNode("c", "single")}
	for _, test := range []struct {
		name string
		edge Edge
		code ValidationErrorCode
	}{
		{"missing-source", Edge{ID: "e", Source: "x", SourcePort: "out", Target: "b", TargetPort: "in"}, ErrInvalidSourceNode},
		{"missing-target", Edge{ID: "e", Source: "a", SourcePort: "out", Target: "x", TargetPort: "in"}, ErrInvalidTargetNode},
		{"missing-source-port", Edge{ID: "e", Source: "a", SourcePort: "x", Target: "b", TargetPort: "in"}, ErrInvalidSourcePort},
		{"missing-target-port", Edge{ID: "e", Source: "a", SourcePort: "out", Target: "b", TargetPort: "x"}, ErrInvalidTargetPort},
		{"input-as-source", Edge{ID: "e", Source: "c", SourcePort: "in", Target: "b", TargetPort: "in"}, ErrInvalidPortDirection},
		{"output-as-target", Edge{ID: "e", Source: "a", SourcePort: "out", Target: "a", TargetPort: "out"}, ErrInvalidPortDirection},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := v.ValidateWithOptions(validatorDefinition(base, []Edge{test.edge}), ValidationOptions{Mode: ValidationDraft}); !containsCode(got, test.code) {
				t.Fatalf("%#v", got.Errors)
			}
		})
	}
	d := validatorDefinition([]Node{validatorNode("a", "start"), validatorNode("b", "end")}, []Edge{{ID: "e", Source: "a", SourcePort: "out", Target: "b", TargetPort: "in"}, {ID: "e", Source: "a", SourcePort: "out", Target: "b", TargetPort: "in"}})
	got := v.Validate(d)
	if !containsCode(got, ErrDuplicateEdgeID) || !containsCode(got, ErrDuplicateEdge) {
		t.Fatalf("%#v", got.Errors)
	}
	loop := validatorDefinition([]Node{validatorNode("a", "single")}, []Edge{{ID: "e", Source: "a", SourcePort: "out", Target: "a", TargetPort: "in"}})
	got = v.ValidateWithOptions(loop, ValidationOptions{Mode: ValidationDraft})
	if !containsCode(got, ErrSelfReference) || !containsCode(got, ErrGraphCycle) {
		t.Fatalf("%#v", got.Errors)
	}
}

func TestGraphValidatorGraphSemanticsAndInterpolation(t *testing.T) {
	v := NewValidator(validatorRegistry(t, nodemeta.ValueTypeString))
	// This is a branching DAG that merges through a Multiple=true port.
	valid := validatorDefinition([]Node{validatorNode("a", "start"), validatorNode("b", "single"), validatorNode("c", "single"), validatorNode("m", "multi"), validatorNode("z", "end")}, []Edge{{ID: "ab", Source: "a", SourcePort: "out", Target: "b", TargetPort: "in"}, {ID: "ac", Source: "a", SourcePort: "out", Target: "c", TargetPort: "in"}, {ID: "bm", Source: "b", SourcePort: "out", Target: "m", TargetPort: "in"}, {ID: "cm", Source: "c", SourcePort: "out", Target: "m", TargetPort: "in"}, {ID: "mz", Source: "m", SourcePort: "out", Target: "z", TargetPort: "in"}})
	if got := v.Validate(valid); !got.Valid {
		t.Fatalf("valid branching merge: %#v", got.Errors)
	}
	valid.Nodes = append(valid.Nodes, validatorNode("orphan", "single"))
	if got := v.Validate(valid); !containsCode(got, ErrDisconnectedNode) {
		t.Fatalf("missing disconnected error: %#v", got.Errors)
	}
	noEntry := validatorDefinition([]Node{validatorNode("x", "single"), validatorNode("z", "end")}, []Edge{{ID: "e", Source: "x", SourcePort: "out", Target: "z", TargetPort: "in"}})
	if got := v.Validate(noEntry); !containsCode(got, ErrMissingInputNode) {
		t.Fatalf("%#v", got.Errors)
	}
	noExit := validatorDefinition([]Node{validatorNode("a", "start"), validatorNode("x", "single")}, []Edge{{ID: "e", Source: "a", SourcePort: "out", Target: "x", TargetPort: "in"}})
	if got := v.Validate(noExit); !containsCode(got, ErrMissingOutputNode) {
		t.Fatalf("%#v", got.Errors)
	}
	cycle := validatorDefinition([]Node{validatorNode("a", "single"), validatorNode("b", "single"), validatorNode("c", "single")}, []Edge{{ID: "ab", Source: "a", SourcePort: "out", Target: "b", TargetPort: "in"}, {ID: "bc", Source: "b", SourcePort: "out", Target: "c", TargetPort: "in"}, {ID: "ca", Source: "c", SourcePort: "out", Target: "a", TargetPort: "in"}})
	if got := v.ValidateWithOptions(cycle, ValidationOptions{Mode: ValidationDraft}); !containsCode(got, ErrGraphCycle) {
		t.Fatalf("%#v", got.Errors)
	}
	valid.Nodes = valid.Nodes[:5]
	valid.Settings["refs"] = "hello {{variable}} {{query}} {{input.query}} {{llm_1.response}} {{customer.name}} from {{does_not_exist}}"
	if got := v.Validate(valid); !got.Valid {
		t.Fatalf("valid static references: %#v", got.Errors)
	}
	valid.Settings["refs"] = "{{}}"
	if got := v.Validate(valid); !containsCode(got, ErrInvalidVariableReference) {
		t.Fatalf("empty interpolation: %#v", got.Errors)
	}
}

// json is the dynamic port type: it connects to and from any type; other
// types must match exactly.
func TestPortTypesCompatible(t *testing.T) {
	cases := []struct {
		source, target nodemeta.ValueType
		want           bool
	}{
		{nodemeta.ValueTypeString, nodemeta.ValueTypeString, true},
		{nodemeta.ValueTypeJSON, nodemeta.ValueTypeString, true},
		{nodemeta.ValueTypeString, nodemeta.ValueTypeJSON, true},
		{nodemeta.ValueTypeJSON, nodemeta.ValueTypeObject, true},
		{nodemeta.ValueTypeString, nodemeta.ValueTypeNumber, false},
		{nodemeta.ValueTypeObject, nodemeta.ValueTypeArray, false},
		{nodemeta.ValueTypeString, nodemeta.ValueTypeObject, false},
	}
	for _, c := range cases {
		if got := PortTypesCompatible(c.source, c.target); got != c.want {
			t.Errorf("%s -> %s = %v, want %v", c.source, c.target, got, c.want)
		}
	}
}
