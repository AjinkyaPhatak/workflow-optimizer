// Package switchnode is the Switch node (Phase E1): routes to the first of
// up to four cases whose rule holds, else to "otherwise"; every other route
// is skipped.
package switchnode

import (
	"context"
	"fmt"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/condition"
)

// NodeType is the node type.
const NodeType = "switch"

// Cases is the number of case routes.
const Cases = 4

// Definition is the node's metadata.
func Definition() node.NodeDefinition {
	opts := make([]node.ConfigOption, len(condition.Operators))
	for i, o := range condition.Operators {
		opts[i] = node.Option(o.ID, o.Label)
	}
	d := node.NodeDefinition{
		Type: NodeType, Name: "Switch", Category: node.CategoryGeneral,
		Description: "Routes to the first case whose value matches (e.g. by email sender or ticket priority), else to Otherwise; the other routes are skipped.",
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("value", node.ValueTypeJSON, false, "Any upstream data: checked when the Value setting is empty, and passed on to the route taken"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("value", node.ValueTypeString, false, "", "What to check, e.g. {{ticket.priority}}. Empty: the connected input.").WithLabel("Value"),
			node.NewConfigField("operator", node.ValueTypeString, false, "equals", "How the value is compared with each case.").WithLabel("Operator").WithOptions(opts...),
			node.NewConfigField("case_sensitive", node.ValueTypeBoolean, false, false, "Treat upper and lower case as different.").WithLabel("Case sensitive"),
		},
	}
	for i := 1; i <= Cases; i++ {
		d.Config = append(d.Config, node.NewConfigField(fmt.Sprintf("case_%d", i), node.ValueTypeString, false, "",
			fmt.Sprintf("Compared with the value; empty: case %d is not used. {{variables}} are filled in.", i)).WithLabel(fmt.Sprintf("Case %d", i)))
		d.Outputs = append(d.Outputs, node.NewPortDefinition(fmt.Sprintf("case_%d", i), node.ValueTypeJSON, false, fmt.Sprintf("Continues here when case %d matches", i)))
	}
	d.Outputs = append(d.Outputs,
		node.NewPortDefinition("otherwise", node.ValueTypeJSON, false, "Continues here when no case matches"),
		node.NewPortDefinition("route", node.ValueTypeString, true, "The route taken: case_1 ... case_4 or otherwise (always emitted)"),
	)
	return d
}

// Node is the Switch node.
type Node struct{}

// New returns the node.
func New() *Node { return &Node{} }

// Type implements node.Node.
func (n *Node) Type() string { return NodeType }

// Execute emits the input on the first matching route only.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	var value any
	raw, has := in.GetConfig("value")
	if s, isStr := raw.(string); !has || (isStr && s == "") {
		if v, ok := in.GetPort("value"); ok {
			value = v.Data
		}
	} else {
		value = raw
	}
	op, _ := in.GetStringConfig("operator")
	cs, _ := in.GetBooleanConfig("case_sensitive")
	route := "otherwise"
	for i := 1; i <= Cases; i++ {
		name := fmt.Sprintf("case_%d", i)
		compare, set := in.GetConfig(name)
		if s, isStr := compare.(string); !set || compare == nil || (isStr && s == "") {
			continue
		}
		ok, err := condition.Evaluate(condition.Rule{Value: value, Operator: op, Compare: compare, CaseSensitive: cs})
		if err != nil {
			return node.NodeOutput{}, node.NewNodeError(node.ErrCodeInvalidInput, fmt.Sprintf("case %d: %v", i, err), false)
		}
		if ok {
			route = name
			break
		}
	}
	pass, ok := in.GetPort("value")
	if !ok {
		pass = node.NewJSONValue(value)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort(route, pass)
	out.SetPort("route", node.NewStringValue(route))
	return out, nil
}
