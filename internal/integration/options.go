package integration

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
)

// MaxOptions bounds one list of dynamic choices.
const MaxOptions = 200

// ErrUnknownOptionsSource: the node lists no choices of that name.
var ErrUnknownOptionsSource = errors.New("integration: unknown options source")

// OptionsHandler lists choices for a setting (Phase C8) with a connected
// client, e.g. the databases or channels the account can see. in carries
// the node's current (unresolved) settings; it never runs the action.
type OptionsHandler[C any] func(ctx context.Context, client C, in node.NodeInput) ([]node.ConfigOption, error)

// OptionsProvider is implemented by nodes that list dynamic choices.
type OptionsProvider interface {
	OptionSources() []string
	Options(ctx context.Context, scope node.Scope, source string, config map[string]any) ([]node.ConfigOption, error)
}

// WithOptions registers the action's options handlers by source name.
func (n *ActionNode[C]) WithOptions(handlers map[string]OptionsHandler[C]) *ActionNode[C] {
	n.options = handlers
	return n
}

// OptionSources implements OptionsProvider.
func (n *ActionNode[C]) OptionSources() []string {
	out := make([]string, 0, len(n.options))
	for s := range n.options {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Options implements OptionsProvider: the account in config is resolved in
// scope's workspace exactly as for a run, and the client lives only for
// this call.
func (n *ActionNode[C]) Options(ctx context.Context, scope node.Scope, source string, config map[string]any) ([]node.ConfigOption, error) {
	h, ok := n.options[source]
	if !ok {
		return nil, fmt.Errorf("%w: %s has no %q options", ErrUnknownOptionsSource, n.nodeType, source)
	}
	in := node.NodeInput{Config: config, Scope: scope}
	var cred credential.ResolvedCredential
	if n.auth.Required {
		resolved, err := n.resolve(ctx, in)
		if err != nil {
			return nil, err
		}
		cred = resolved
	}
	client, err := n.connector.Connect(ctx, cred)
	cred = credential.ResolvedCredential{}
	if err != nil {
		return nil, ToNodeError(ctx, n.integration, n.action, err)
	}
	opts, err := h(ctx, client, in)
	if err != nil {
		return nil, ToNodeError(ctx, n.integration, n.action, err)
	}
	if len(opts) > MaxOptions {
		opts = opts[:MaxOptions]
	}
	return opts, nil
}

var _ OptionsProvider = (*ActionNode[struct{}])(nil)
