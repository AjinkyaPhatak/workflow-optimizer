package merge

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Merge nodes.
const NodeType = "merge"

// Definition returns the canonical metadata definition for a Merge node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Merge",
		Description: "Joins branches back together: it runs when any connected branch ran (after a Condition, the one taken) and passes the data on.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewMultiPortDefinition("left", node.ValueTypeJSON, false, "First upstream branch data"),
			node.NewMultiPortDefinition("right", node.ValueTypeJSON, false, "Second upstream branch data"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("merged", node.ValueTypeJSON, true, "Combined output (see Mode)"),
			node.NewPortDefinition("value", node.ValueTypeJSON, true, "The first data that arrived (left before right); null when none"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("mode", node.ValueTypeString, false, "combine", "What merged contains.").WithLabel("Mode").WithOptions(
				node.Option("combine", "Combine: {left, right} of the branches that ran"),
				node.Option("first_available", "First available: the first data that arrived"),
				node.Option("array", "List: every arriving item, left then right"),
			),
		},
	}
}

// Node is the Merge node.
type Node struct{}

// New constructs an executable Merge node.
func New() *Node { return &Node{} }

// Type returns the unique node type identifier.
func (n *Node) Type() string { return NodeType }

// items are a multi-input port's values (each edge's data).
func items(in node.NodeInput, port string) ([]any, bool) {
	v, ok := in.GetPort(port)
	if !ok {
		return nil, false
	}
	if list, ok := v.Data.([]any); ok {
		return list, true
	}
	return []any{v.Data}, true
}

// Execute combines the inputs of the branches that ran.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	left, hasLeft := items(in, "left")
	right, hasRight := items(in, "right")
	all := append(append([]any{}, left...), right...)
	var first any
	if len(all) > 0 {
		first = all[0]
	}
	var merged any
	mode, _ := in.GetStringConfig("mode")
	switch mode {
	case "first_available":
		merged = first
	case "array":
		merged = all
	default:
		// As before Phase E: each input's data unchanged (a list per
		// multi-input port).
		m := map[string]any{}
		if v, ok := in.GetPort("left"); ok && hasLeft {
			m["left"] = v.Data
		}
		if v, ok := in.GetPort("right"); ok && hasRight {
			m["right"] = v.Data
		}
		merged = m
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("merged", node.NewJSONValue(merged))
	out.SetPort("value", node.NewJSONValue(first))
	return out, nil
}
