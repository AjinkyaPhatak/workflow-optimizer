package structured_output

import (
	"context"

	"workflow-optimizer/internal/node"
	providerllm "workflow-optimizer/internal/provider/llm"
)

// NodeType is the stable identifier for Structured Output nodes.
const NodeType = "structured_output"

// Definition returns the canonical metadata definition for a Structured Output node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Structured Output",
		Description: "Generates or extracts schema-conforming structured JSON from language model output.",
		Category:    node.CategoryAI,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("prompt", node.ValueTypeString, true, "User prompt requesting structured output"),
			node.NewPortDefinition("system", node.ValueTypeString, false, "System guidance or constraint prompt"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("output", node.ValueTypeJSON, true, "Schema-validated structured JSON output"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("schema", node.ValueTypeJSON, true, map[string]any{}, "Target JSON Schema definition"),
			node.NewConfigField("provider", node.ValueTypeString, true, "openai", "AI provider identifier"),
			node.NewConfigField("model", node.ValueTypeString, true, "gpt-5", "Model name or checkpoint"),
		},
	}
}

// Node is the executable contract implementation for Structured Output nodes.
type Node struct {
	provider providerllm.Provider
}

// New constructs an executable Structured Output node.
func New(provider providerllm.Provider) *Node {
	return &Node{
		provider: provider,
	}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute extracts structured data via the provider or stub.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	out.SetPort("output", node.NewJSONValue(map[string]any{"result": "stub:structured output"}))
	return out, nil
}
