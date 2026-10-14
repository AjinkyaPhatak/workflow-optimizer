package condition_test

import (
	"context"
	"testing"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/condition"
)

func TestEvaluate(t *testing.T) {
	cases := []struct {
		r    condition.Rule
		want bool
	}{
		{condition.Rule{Value: "Invoice October", Operator: "contains", Compare: "invoice"}, true},
		{condition.Rule{Value: "Invoice October", Operator: "contains", Compare: "invoice", CaseSensitive: true}, false},
		{condition.Rule{Value: []any{"vip", "new"}, Operator: "contains", Compare: "VIP"}, true},
		{condition.Rule{Value: "5", Operator: "equals", Compare: 5.0}, true},
		{condition.Rule{Value: "5.0", Operator: "equals", Compare: "5"}, true},
		{condition.Rule{Value: "abc", Operator: "not_equals", Compare: "ABC"}, false},
		{condition.Rule{Value: 12.0, Operator: "greater_than", Compare: "10"}, true},
		{condition.Rule{Value: "9", Operator: "greater_or_equal", Compare: 9.0}, true},
		{condition.Rule{Value: "", Operator: "is_empty"}, true},
		{condition.Rule{Value: []any{}, Operator: "is_empty"}, true},
		{condition.Rule{Value: map[string]any{"a": 1.0}, Operator: "is_not_empty"}, true},
		{condition.Rule{Value: "yes", Operator: "is_true"}, true},
		{condition.Rule{Value: false, Operator: "is_false"}, true},
		{condition.Rule{Value: "order-1234", Operator: "matches", Compare: `^ORDER-\d+$`}, true},
		{condition.Rule{Value: "report.pdf", Operator: "ends_with", Compare: ".PDF"}, true},
		{condition.Rule{Value: "Re: hello", Operator: "starts_with", Compare: "re:"}, true},
	}
	for _, c := range cases {
		got, err := condition.Evaluate(c.r)
		if err != nil || got != c.want {
			t.Errorf("%+v = %v %v", c.r, got, err)
		}
	}
	for _, bad := range []condition.Rule{
		{Value: "abc", Operator: "greater_than", Compare: 1.0},
		{Value: "x", Operator: "matches", Compare: "("},
		{Value: "maybe", Operator: "is_true"},
		{Value: "x", Operator: "sounds_like", Compare: "y"},
	} {
		if _, err := condition.Evaluate(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func run(t *testing.T, cfg map[string]any, value any) (string, error) {
	t.Helper()
	in := node.NodeInput{Config: cfg, Ports: map[string]node.Value{}}
	if value != nil {
		in.Ports["value"] = node.NewJSONValue(value)
	}
	out, err := condition.New().Execute(context.Background(), in)
	if err != nil {
		return "", err
	}
	_, onTrue := out.GetPort("true")
	_, onFalse := out.GetPort("false")
	r, _ := out.GetPort("result")
	if onTrue == onFalse || r.Data != onTrue {
		t.Fatalf("exactly one branch with a matching result: %v", out.Ports)
	}
	if onTrue {
		return "true", nil
	}
	return "false", nil
}

func TestConditionNode(t *testing.T) {
	for name, c := range map[string]struct {
		cfg   map[string]any
		value any
		want  string
	}{
		"setting":             {map[string]any{"value": "urgent: server down", "operator": "contains", "compare": "URGENT"}, nil, "true"},
		"input when no value": {map[string]any{"operator": "greater_than", "compare": "100"}, 250.0, "true"},
		"nothing configured":  {map[string]any{}, false, "false"},
		"truthy object":       {map[string]any{}, map[string]any{"flag": true}, "true"},
		"all rules (and)":     {map[string]any{"value": "a", "operator": "equals", "compare": "a", "rules": []any{map[string]any{"value": 3.0, "operator": "less_than", "compare": 2.0}}}, nil, "false"},
		"any rule (or)":       {map[string]any{"value": "a", "operator": "equals", "compare": "b", "combine": "any", "rules": []any{map[string]any{"value": 3.0, "operator": "less_than", "compare": 5.0}}}, nil, "true"},
	} {
		got, err := run(t, c.cfg, c.value)
		if err != nil || got != c.want {
			t.Errorf("%s: %s %v", name, got, err)
		}
	}
	if _, err := run(t, map[string]any{"rules": "not a list"}, true); err == nil {
		t.Fatal("bad rules accepted")
	}
	if _, err := run(t, map[string]any{"value": "x", "operator": "greater_than", "compare": "y"}, nil); err == nil {
		t.Fatal("non-numeric comparison accepted")
	}
}
