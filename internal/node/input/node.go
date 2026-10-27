package input

import (
	"context"
	"fmt"
	"strings"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Input nodes.
const NodeType = "input"

// Definition returns the canonical metadata definition for an Input node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Input",
		Description: "Where a run starts. With a Field name it asks for one value when you run the workflow (text, or a file from the Library); without one it passes on everything the run was given.",
		Category:    node.CategoryGeneral,
		Role:        node.SemanticRoleEntry,
		Inputs:      []node.PortDefinition{},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("data", node.ValueTypeJSON, true, "The value of the field (or the whole run input when no Field name is set)"),
		},
		// Phase I: one Input card per question in the Run form.
		Config: []node.ConfigField{
			node.NewConfigField(FieldConfig, node.ValueTypeString, false, "", "What this input is called, e.g. topic or email. The Run form asks for it.").WithLabel("Field name"),
			node.NewConfigField(TypeConfig, node.ValueTypeString, false, TypeText, "What kind of value to ask for.").WithLabel("Type").WithOptions(
				node.ConfigOption{Value: TypeText, Label: "Text"},
				node.ConfigOption{Value: TypeFile, Label: "File (from the Library)"},
			),
		},
	}
}

// Input settings (Phase I).
const (
	FieldConfig = "field"
	TypeConfig  = "field_type"
	TypeText    = "text"
	TypeFile    = "file"
)

// Node is the executable contract implementation for Input nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Input node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute emits the incoming data to the output port.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	if field, _ := in.GetStringConfig(FieldConfig); strings.TrimSpace(field) != "" {
		// One named field of the run input.
		field = strings.TrimSpace(field)
		var data map[string]any
		if val, ok := in.GetPort("data"); ok {
			data, _ = val.Data.(map[string]any)
		}
		v, ok := data[field]
		if !ok || v == nil || v == "" {
			return node.NodeOutput{}, node.NewNodeError(node.ErrCodeInvalidInput, fmt.Sprintf("fill in %q to run this workflow", field), false)
		}
		out.SetPort("data", node.NewJSONValue(v))
		return out, nil
	}
	if val, ok := in.GetPort("data"); ok {
		out.SetPort("data", val)
	} else if cfg := withoutSettings(in.Config); cfg != nil {
		out.SetPort("data", node.NewJSONValue(cfg))
	} else {
		out.SetPort("data", node.NewJSONValue(map[string]any{}))
	}
	return out, nil
}

// withoutSettings is the node's configuration minus its own settings (nil
// when nothing else is left).
func withoutSettings(cfg map[string]any) map[string]any {
	var out map[string]any
	for k, v := range cfg {
		if k == FieldConfig || k == TypeConfig {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	return out
}
