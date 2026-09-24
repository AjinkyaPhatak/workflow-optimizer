package node_test

import (
	"context"
	"fmt"
	"testing"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/node/http"
	"workflow-optimizer/internal/node/input"
	"workflow-optimizer/internal/node/llm"
)

// GenericStepExecutor represents an execution engine component that executes
// workflow nodes via dynamic registry resolution.
// It has ZERO hardcoded knowledge of specific node types: no switch statements,
// no if/else cascades, and no type-specific handling.
type GenericStepExecutor struct {
	registry node.Registry
}

func NewGenericStepExecutor(reg node.Registry) *GenericStepExecutor {
	return &GenericStepExecutor{registry: reg}
}

// ExecuteStep orchestrates node execution polymorphically.
func (e *GenericStepExecutor) ExecuteStep(ctx context.Context, nodeType string, in node.NodeInput) (node.NodeOutput, error) {
	execNode, err := e.registry.Get(nodeType)
	if err != nil {
		return node.NodeOutput{}, fmt.Errorf("resolve node type %q: %w", nodeType, err)
	}

	// Dynamic, polymorphic execution — executor knows nothing about node internals.
	return execNode.Execute(ctx, in)
}

// GmailSendNode is a hypothetical future integration node added without
// modifying the GenericStepExecutor or any core execution engine code.
type GmailSendNode struct{}

func (g *GmailSendNode) Type() string {
	return "gmail.send"
}

func (g *GmailSendNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	toVal, _ := in.GetPort("to")
	toAddr, _ := toVal.String()

	out := node.NewNodeOutput(nil)
	out.SetPort("status", node.NewStringValue(fmt.Sprintf("email sent to %s", toAddr)))
	return out, nil
}

func TestArchitecturalGenericExecutionWithoutTypeBranching(t *testing.T) {
	registry := node.NewRegistry()

	// Register existing V1 nodes
	_ = registry.Register(input.New())
	_ = registry.Register(llm.New(nil))
	_ = registry.Register(http.New())

	// Register the brand new future node
	_ = registry.Register(&GmailSendNode{})

	executor := NewGenericStepExecutor(registry)
	ctx := context.Background()

	// Test case 1: Execute existing input node
	outInput, err := executor.ExecuteStep(ctx, "input", node.NodeInput{
		Config: map[string]any{"source": "cli"},
	})
	if err != nil {
		t.Fatalf("execute input node: %v", err)
	}
	if _, ok := outInput.GetPort("data"); !ok {
		t.Fatal("expected 'data' port from input node")
	}

	// Test case 2: Execute existing llm node
	outLLM, err := executor.ExecuteStep(ctx, "llm", node.NodeInput{
		Ports: map[string]node.Value{
			"prompt": node.NewStringValue("Hello world"),
		},
	})
	if err != nil {
		t.Fatalf("execute llm node: %v", err)
	}
	if _, ok := outLLM.GetPort("response"); !ok {
		t.Fatal("expected 'response' port from llm node")
	}

	// Test case 3: Execute brand new "gmail.send" node
	// Crucially, this succeeds through the generic executor without modifying a single line of executor code!
	outGmail, err := executor.ExecuteStep(ctx, "gmail.send", node.NodeInput{
		Ports: map[string]node.Value{
			"to":      node.NewStringValue("user@example.com"),
			"subject": node.NewStringValue("Welcome"),
		},
	})
	if err != nil {
		t.Fatalf("execute gmail.send node: %v", err)
	}
	statusVal, ok := outGmail.GetPort("status")
	if !ok {
		t.Fatal("expected 'status' port from gmail.send node")
	}
	s, _ := statusVal.String()
	if s != "email sent to user@example.com" {
		t.Fatalf("got %q, want 'email sent to user@example.com'", s)
	}
}
