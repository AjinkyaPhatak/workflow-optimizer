package node

import (
	"context"
	"encoding/json"
)

// NodeInput is the canonical runtime input supplied to a node during execution.
// It keeps runtime port inputs cleanly separated from node-level configuration.
// Upstream scheduling, graph traversal, variable resolution, and persistence
// are owned by the execution engine and do not belong here.
type NodeInput struct {
	Ports  map[string]Value `json:"ports"`
	Config map[string]any   `json:"config"`
}

// GetPort retrieves a Value from runtime input ports by name.
func (in NodeInput) GetPort(name string) (Value, bool) {
	if in.Ports == nil {
		return Value{}, false
	}
	v, ok := in.Ports[name]
	return v, ok
}

// GetConfig retrieves a configuration setting by name.
func (in NodeInput) GetConfig(name string) (any, bool) {
	if in.Config == nil {
		return nil, false
	}
	v, ok := in.Config[name]
	return v, ok
}

// GetStringConfig retrieves a configuration value as a string.
func (in NodeInput) GetStringConfig(name string) (string, bool) {
	v, ok := in.GetConfig(name)
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// GetNumberConfig retrieves a configuration value as a float64.
func (in NodeInput) GetNumberConfig(name string) (float64, bool) {
	v, ok := in.GetConfig(name)
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// GetBooleanConfig retrieves a configuration value as a bool.
func (in NodeInput) GetBooleanConfig(name string) (bool, bool) {
	v, ok := in.GetConfig(name)
	if !ok || v == nil {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// NodeOutput is the canonical runtime output produced by a node execution.
// Outputs are port-oriented. Nodes do not return arbitrary workflow state.
type NodeOutput struct {
	Ports map[string]Value `json:"ports"`
}

// NewNodeOutput constructs a NodeOutput with the given ports map.
func NewNodeOutput(ports map[string]Value) NodeOutput {
	if ports == nil {
		ports = make(map[string]Value)
	}
	return NodeOutput{Ports: ports}
}

// GetPort retrieves a named output port value.
func (out NodeOutput) GetPort(name string) (Value, bool) {
	if out.Ports == nil {
		return Value{}, false
	}
	v, ok := out.Ports[name]
	return v, ok
}

// SetPort assigns an output port value.
func (out *NodeOutput) SetPort(name string, v Value) {
	if out.Ports == nil {
		out.Ports = make(map[string]Value)
	}
	out.Ports[name] = v
}

// Backward-compatibility aliases for earlier phase placeholders.
type Input = NodeInput
type Output = NodeOutput

// Node is the fundamental executable contract implemented by every workflow node.
//
// The central architectural principle is:
// "The execution engine orchestrates nodes; nodes implement operations."
//
// Nodes must be stateless and concurrency-safe: request-specific data belongs
// strictly in the NodeInput argument and local execution stack.
type Node interface {
	// Type returns the stable unique node type identifier (e.g., "llm", "input", "condition").
	Type() string

	// Execute runs the node operation against the provided input.
	// Cancellation via ctx must be respected by all implementations.
	Execute(ctx context.Context, input NodeInput) (NodeOutput, error)
}
