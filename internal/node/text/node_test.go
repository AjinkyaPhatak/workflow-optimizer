package text

import (
	"context"
	"testing"

	"workflow-optimizer/internal/node"
)

func TestTemplateConfigurationProducesTextAndRetainsInputPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input node.NodeInput
		want  string
	}{
		{"rendered report", node.NodeInput{Config: map[string]any{"template": "Nifty gainers: A +1.2%; source: published report"}}, "Nifty gainers: A +1.2%; source: published report"},
		{"connected input wins", node.NodeInput{Config: map[string]any{"template": "fallback"}, Ports: map[string]node.Value{"input": node.NewStringValue("connected report")}}, "connected report"},
		{"static text wins", node.NodeInput{Config: map[string]any{"text": "chosen text", "template": "fallback"}}, "chosen text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := New().Execute(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			value, ok := out.GetPort("output")
			if !ok || value.Data != tc.want {
				t.Fatalf("output = %#v; want %q", value.Data, tc.want)
			}
		})
	}
}
