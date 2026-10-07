package node_test

import (
	"encoding/json"
	"testing"

	"workflow-optimizer/internal/node"
)

func TestConfigFieldOptions(t *testing.T) {
	method := node.NewConfigField("method", node.ValueTypeString, true, "GET", "").WithOptions(node.Option("GET", "GET"), node.Option("POST", "POST"))
	if !method.Allows("POST", nil) || method.Allows("TRACE", nil) {
		t.Fatal("strict options must accept listed values only")
	}
	if !method.WithCustom().Allows("TRACE", nil) {
		t.Fatal("allow_custom options are suggestions")
	}
	free := node.NewConfigField("note", node.ValueTypeString, false, "", "")
	if !free.Allows("anything", nil) {
		t.Fatal("a field without options accepts any value")
	}

	model := node.NewConfigField("model", node.ValueTypeString, true, "a1", "").WithOptions(
		node.ConfigOption{Value: "a1", When: map[string]any{"provider": "a"}},
		node.ConfigOption{Value: "b1", When: map[string]any{"provider": "b"}},
	)
	if !model.Allows("b1", map[string]any{"provider": "b"}) || model.Allows("b1", map[string]any{"provider": "a"}) {
		t.Fatal("conditional options apply only when their condition holds")
	}

	temp := node.NewConfigField("temperature", node.ValueTypeNumber, false, 0.2, "").WithRange(node.Float(0), node.Float(2), node.Float(0.1))
	if !temp.InRange(0) || !temp.InRange(2) || temp.InRange(2.5) || temp.InRange(-1) {
		t.Fatal("range bounds are inclusive")
	}
	open := node.NewConfigField("max_tokens", node.ValueTypeNumber, false, nil, "").WithRange(node.Float(1), nil, nil)
	if !open.InRange(1e9) || open.InRange(0) {
		t.Fatal("a nil bound is open")
	}

	// The metadata is part of the definition's JSON (omitted when unset).
	b, _ := json.Marshal(temp.WithLabel("Temperature"))
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["label"] != "Temperature" || got["min"] != 0.0 || got["max"] != 2.0 || got["step"] != 0.1 {
		t.Fatalf("metadata JSON: %s", b)
	}
	b, _ = json.Marshal(free)
	if string(b) != `{"name":"note","type":"string","required":false,"default":""}` {
		t.Fatalf("unset metadata must be omitted: %s", b)
	}
}
