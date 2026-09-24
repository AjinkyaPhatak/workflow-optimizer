package fake

import (
    "context"
    "workflow-optimizer/internal/node"
)

// GmailSend is a minimal fake node used only in tests.
// It implements the node.Node interface but performs no work.

type GmailSend struct{}

func (g *GmailSend) Type() string { return "gmail.send" }
func (g *GmailSend) Execute(ctx context.Context, input node.NodeInput) (node.NodeOutput, error) { return node.NodeOutput{}, nil }
