package transform

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Transform nodes.
const NodeType = "transform"

// Definition returns the canonical metadata definition for a Transform node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Transform",
		Description: "Reshapes data: picks a field out of its input, or builds a new JSON value from a mapping.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("input", node.ValueTypeJSON, true, "Data to transform"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("output", node.ValueTypeJSON, true, "The transformed data"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("path", node.ValueTypeString, false, "",
				"Dot path of the value to pick from the input, such as action_items or customer.email (list items by index: items.0). Leave empty to pass the input through.").
				WithLabel("Field path"),
			node.NewConfigField("mapping", node.ValueTypeJSON, false, nil,
				`Builds the output from this JSON instead; {{references}} in it are filled in, e.g. {"title": "{{input.subject}}"}. When set, Field path is ignored.`).
				WithLabel("Output mapping"),
			// Kept so existing definitions stay valid; never applied.
			node.NewConfigField("expression", node.ValueTypeString, false, "",
				"Not applied: kept for workflows created before Field path and Output mapping existed.").
				WithLabel("Expression (legacy)"),
		},
	}
}

// Node transforms data. {{references}} in the mapping are resolved by the
// execution engine before the node runs; the node only selects and copies.
type Node struct{}

// New constructs an executable Transform node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute returns the mapping when one is configured, else the value at the
// field path of the input (the whole input for an empty path).
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	if mapping, ok := in.Config["mapping"]; ok && mapping != nil {
		out.SetPort("output", node.NewJSONValue(mapping))
		return out, nil
	}
	val, ok := in.GetPort("input")
	if !ok {
		out.SetPort("output", node.NewJSONValue(map[string]any{}))
		return out, nil
	}
	path, _ := in.GetStringConfig("path")
	picked, err := Select(val.Data, path)
	if err != nil {
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration, err.Error(), false)
	}
	out.SetPort("output", node.NewJSONValue(picked))
	return out, nil
}

// Select walks a dot path ("a.b.0.c") into JSON data.
func Select(data any, path string) (any, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return data, nil
	}
	cur := data
	for _, seg := range strings.Split(path, ".") {
		switch t := cur.(type) {
		case map[string]any:
			next, ok := t[seg]
			if !ok {
				return nil, fmt.Errorf("field path %q: %q not found", path, seg)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return nil, fmt.Errorf("field path %q: invalid list index %q", path, seg)
			}
			cur = t[i]
		default:
			return nil, fmt.Errorf("field path %q: cannot select %q from a %s", path, seg, kind(cur))
		}
	}
	return cur, nil
}

func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	}
	return fmt.Sprintf("%T", v)
}
