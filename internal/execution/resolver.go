package execution

import (
	"fmt"
	"sort"

	"workflow-optimizer/internal/node"
)

// resolveInputs builds the runtime input ports for one node from its incoming
// edges and the outputs of already-completed upstream nodes.
//
// Mapping rules:
//   - An edge copies source.Ports[SourcePort] to target port TargetPort.
//   - A single-valued input port receives that Value unchanged.
//   - A Multiple=true input port always receives one node.ValueTypeArray Value
//     whose elements are the upstream Value.Data payloads in edge definition
//     order, so no incoming value is dropped and the shape does not depend on
//     how many edges are connected.
//   - If a source node did not emit an output port its NodeDefinition declares
//     optional (e.g. the untaken branch of a condition), the edge carries no
//     value and the target port is left unset. A missing port that is
//     declared required is a node contract violation and fails execution.
func resolveInputs(
	nodeID string,
	def node.NodeDefinition,
	plan *Plan,
	state *ExecutionState,
	bound map[string]boundNode,
) (map[string]node.Value, error) {
	byPort := make(map[string][]int)
	edges := plan.Incoming(nodeID)
	for i, e := range edges {
		byPort[e.TargetPort] = append(byPort[e.TargetPort], i)
	}
	portNames := make([]string, 0, len(byPort))
	for p := range byPort {
		portNames = append(portNames, p)
	}
	sort.Strings(portNames)

	ports := make(map[string]node.Value, len(portNames))
	for _, portName := range portNames {
		portDef, ok := def.GetInputPort(portName)
		if !ok {
			return nil, fmt.Errorf("%w: target port %q is not a declared input of type %q", ErrInvalidConnection, portName, def.Type)
		}
		idx := byPort[portName]
		if !portDef.Multiple && len(idx) > 1 {
			return nil, fmt.Errorf("%w: single-valued input %q has %d incoming edges", ErrInvalidConnection, portName, len(idx))
		}

		values := make([]node.Value, 0, len(idx))
		for _, i := range idx {
			e := edges[i]
			out, ok := state.Results[e.Source]
			if !ok {
				return nil, fmt.Errorf("%w: edge %q from node %q has not produced a result", ErrMissingUpstreamResult, e.ID, e.Source)
			}
			v, ok := out.GetPort(e.SourcePort)
			if !ok {
				srcPort, declared := bound[e.Source].def.GetOutputPort(e.SourcePort)
				if declared && !srcPort.Required {
					continue // optional output not emitted: this edge is inactive
				}
				return nil, fmt.Errorf("%w: edge %q expects port %q on node %q", ErrMissingSourcePort, e.ID, e.SourcePort, e.Source)
			}
			values = append(values, v)
		}
		if len(values) == 0 {
			continue
		}
		if portDef.Multiple {
			items := make([]any, len(values))
			for i, v := range values {
				items[i] = v.Data
			}
			ports[portName] = node.NewArrayValue(items)
			continue
		}
		ports[portName] = values[0]
	}
	return ports, nil
}
