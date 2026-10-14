package workflow

import (
	nodepkg "workflow-optimizer/internal/node"
)

// Error handling per node (Phase E2).
const (
	// OnErrorStop fails the run when the node fails (the default).
	OnErrorStop = "stop"
	// OnErrorContinue records the failure and goes on: whatever only this
	// node feeds is skipped.
	OnErrorContinue = "continue"
	// OnErrorOutput is OnErrorContinue that also emits the failure on the
	// node's ErrorPort, so a branch can handle it.
	OnErrorOutput = "error_output"
)

// ErrorPort is the output a node with OnErrorOutput emits on failure:
// {code, message, node_id}.
const ErrorPort = "error"

// Bounds of per-node retry settings.
const (
	MaxRetryAttempts = 5
	MaxRetryDelayMS  = 30000
)

// NodeSettings are execution settings of one node, outside its type's
// configuration. Every field is optional.
type NodeSettings struct {
	// OnError is OnErrorStop (empty), OnErrorContinue or OnErrorOutput.
	OnError string `json:"on_error,omitempty"`
	// RetryAttempts is how many times the node runs at most after a
	// temporary failure (1 = no retry; empty: the server default).
	RetryAttempts *int `json:"retry_attempts,omitempty"`
	// RetryDelayMS is the pause between attempts (a server's Retry-After
	// wins when longer).
	RetryDelayMS *int `json:"retry_delay_ms,omitempty"`
}

// OnErrorMode is the node's error handling (OnErrorStop when unset).
func (n Node) OnErrorMode() string {
	if n.Settings == nil || n.Settings.OnError == "" {
		return OnErrorStop
	}
	return n.Settings.OnError
}

// errorPortDefinition is the virtual output of OnErrorOutput nodes.
var errorPortDefinition = nodepkg.NewPortDefinition(ErrorPort, nodepkg.ValueTypeJSON, false,
	"The failure, when the node fails: code, message, node_id")

// OutputPort is a node's output port: one its type declares, or the
// ErrorPort of a node set to OnErrorOutput (a declared port of the same
// name wins).
func OutputPort(def nodepkg.NodeDefinition, n Node, name string) (nodepkg.PortDefinition, bool) {
	if p, ok := def.GetOutputPort(name); ok {
		return p, true
	}
	if name == ErrorPort && n.OnErrorMode() == OnErrorOutput {
		return errorPortDefinition, true
	}
	return nodepkg.PortDefinition{}, false
}
