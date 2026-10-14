package condition

import (
	"context"
	"fmt"
	"strings"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Condition nodes.
const NodeType = "condition"

// MaxRules bounds a condition's rules.
const MaxRules = 10

func operatorField(name, label string) node.ConfigField {
	opts := make([]node.ConfigOption, len(Operators))
	for i, o := range Operators {
		opts[i] = node.Option(o.ID, o.Label)
	}
	return node.NewConfigField(name, node.ValueTypeString, false, "equals", "How the value is compared.").WithLabel(label).WithOptions(opts...)
}

// Definition returns the canonical metadata definition for a Condition node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Condition",
		Description: "Checks a rule (e.g. {{read.subject}} contains invoice) and continues on the true or the false output; the other branch is skipped.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("value", node.ValueTypeJSON, false, "Any upstream data: checked when the Value setting is empty, and passed on to the branch taken"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("true", node.ValueTypeJSON, false, "Continues here when the condition holds (with the input data)"),
			node.NewPortDefinition("false", node.ValueTypeJSON, false, "Continues here when it does not"),
			node.NewPortDefinition("result", node.ValueTypeBoolean, true, "The outcome, true or false (always emitted)"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("value", node.ValueTypeString, false, "", "What to check, e.g. {{read.subject}}. Empty: the connected input.").WithLabel("Value"),
			operatorField("operator", "Operator"),
			node.NewConfigField("compare", node.ValueTypeString, false, "", "What to compare with (not needed for is empty / is true...). {{variables}} are filled in.").WithLabel("Compare with"),
			node.NewConfigField("case_sensitive", node.ValueTypeBoolean, false, false, "Treat upper and lower case as different.").WithLabel("Case sensitive"),
			node.NewConfigField("combine", node.ValueTypeString, false, "all", "With more rules: whether all of them or any one must hold.").
				WithLabel("More rules: combine").WithOptions(node.Option("all", "All must hold (AND)"), node.Option("any", "Any one holds (OR)")),
			node.NewConfigField("rules", node.ValueTypeJSON, false, nil,
				`More rules, checked with the first: a list like [{"value": "{{a.b}}", "operator": "greater_than", "compare": 10}].`).WithLabel("More rules"),
		},
	}
}

// Node is the Condition node.
type Node struct{}

// New constructs an executable Condition node.
func New() *Node { return &Node{} }

// Type returns the unique node type identifier.
func (n *Node) Type() string { return NodeType }

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

// rules reads the first rule from the settings (its value from the input
// when the Value setting is empty) and any further rules.
func rules(in node.NodeInput) ([]Rule, error) {
	first := Rule{}
	raw, hasValue := in.GetConfig("value")
	if s, isStr := raw.(string); !hasValue || (isStr && s == "") {
		if v, ok := in.GetPort("value"); ok {
			first.Value = v.Data
		}
	} else {
		first.Value = raw
	}
	first.Operator, _ = in.GetStringConfig("operator")
	var hasCompare bool
	first.Compare, hasCompare = in.GetConfig("compare")
	if s, isStr := first.Compare.(string); first.Operator == "" && (!hasCompare || first.Compare == nil || (isStr && s == "")) {
		// Nothing configured: branch on whether the value is truthy.
		first.Operator = "is_true"
	}
	first.CaseSensitive, _ = in.GetBooleanConfig("case_sensitive")
	out := []Rule{first}
	more, ok := in.GetConfig("rules")
	if !ok || more == nil {
		return out, nil
	}
	list, ok := more.([]any)
	if !ok {
		return nil, configError(`More rules must be a list like [{"value": "...", "operator": "equals", "compare": "..."}]`)
	}
	if len(list)+1 > MaxRules {
		return nil, configError(fmt.Sprintf("at most %d rules", MaxRules))
	}
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, configError(fmt.Sprintf("rule %d must be an object with value, operator and compare", i+2))
		}
		r := Rule{Value: m["value"], Compare: m["compare"]}
		r.Operator, _ = m["operator"].(string)
		r.CaseSensitive, _ = m["case_sensitive"].(bool)
		out = append(out, r)
	}
	return out, nil
}

// Execute evaluates the rules and emits the input on exactly one of true or
// false; the executor skips whatever only the other output feeds.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	rs, err := rules(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	combine, _ := in.GetStringConfig("combine")
	anyOne := strings.TrimSpace(combine) == "any"
	outcome := !anyOne
	for i, r := range rs {
		ok, err := Evaluate(r)
		if err != nil {
			return node.NodeOutput{}, node.NewNodeError(node.ErrCodeInvalidInput, fmt.Sprintf("rule %d: %v", i+1, err), false)
		}
		if anyOne {
			outcome = outcome || ok
		} else {
			outcome = outcome && ok
		}
	}
	pass, ok := in.GetPort("value")
	if !ok {
		pass = node.NewJSONValue(rs[0].Value)
	}
	out := node.NewNodeOutput(nil)
	if outcome {
		out.SetPort("true", pass)
	} else {
		out.SetPort("false", pass)
	}
	out.SetPort("result", node.NewBooleanValue(outcome))
	return out, nil
}
