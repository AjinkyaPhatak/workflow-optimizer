package execution

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// inputNamespace is the reserved first path segment that addresses the
// workflow's initial input, as in {{input.query}}.
const inputNamespace = "input"

// variableScope is the read-only runtime data a {{reference}} in one node's
// configuration may observe. It has no access to persistence, providers,
// transport, or node execution.
//
// Resolution of an expression "s0.s1...sn" (grammar owned by
// workflow.VariableReferences):
//  1. s0 == "input"        → path s1..sn inside the workflow input map.
//  2. s0 is a node ID      → s1 names an output port of that node's
//     NodeOutput; s2..sn walk into the port Value's data. The node must be a
//     transitive upstream dependency of the node being resolved, so results
//     never depend on the tie-breaking order of independent nodes.
//  3. otherwise            → path s0..sn inside the workflow input map
//     (bare {{query}} or {{customer.name}}).
//
// Anything else is an ErrUnresolvedReference.
type variableScope struct {
	nodeID  string
	plan    *Plan
	results map[string]node.NodeOutput
	input   map[string]any
}

// resolveConfig returns a resolved deep copy of a node's static configuration.
// The workflow definition is never mutated.
func resolveConfig(cfg map[string]any, scope variableScope) (map[string]any, error) {
	if cfg == nil {
		return nil, nil
	}
	resolved, err := scope.resolveAny(cfg)
	if err != nil {
		return nil, err
	}
	return resolved.(map[string]any), nil
}

func (s variableScope) resolveAny(v any) (any, error) {
	switch typed := v.(type) {
	case string:
		return s.interpolate(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, nested := range typed {
			r, err := s.resolveAny(nested)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, nested := range typed {
			r, err := s.resolveAny(nested)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return typed, nil
	}
}

// interpolate resolves every reference in text. A string consisting of exactly
// one reference yields the referenced value with its type preserved; a string
// mixing literals and references yields a string. Substitution is single-pass,
// so resolved values are never re-interpreted as templates.
func (s variableScope) interpolate(text string) (any, error) {
	refs := workflow.VariableReferences(text)
	if len(refs) == 0 {
		return text, nil
	}
	if len(refs) == 1 && text == "{{"+refs[0].Expression+"}}" {
		v, err := s.lookup(refs[0].Expression)
		if err != nil {
			return nil, err
		}
		return deepCopy(v), nil
	}

	var b strings.Builder
	rest := text
	for {
		next := workflow.VariableReferences(rest)
		if len(next) == 0 {
			b.WriteString(rest)
			break
		}
		// The leftmost grammar match is also the leftmost occurrence of its
		// literal token, because identifier characters cannot contain "}".
		token := "{{" + next[0].Expression + "}}"
		at := strings.Index(rest, token)
		v, err := s.lookup(next[0].Expression)
		if err != nil {
			return nil, err
		}
		str, err := stringify(v)
		if err != nil {
			return nil, fmt.Errorf("%w: {{%s}}: %v", ErrUnresolvedReference, next[0].Expression, err)
		}
		b.WriteString(rest[:at])
		b.WriteString(str)
		rest = rest[at+len(token):]
	}
	return b.String(), nil
}

func (s variableScope) lookup(expr string) (any, error) {
	segs := strings.Split(expr, ".")
	for _, seg := range segs {
		if seg == "" {
			return nil, fmt.Errorf("%w: {{%s}} has an empty path segment", ErrUnresolvedReference, expr)
		}
	}
	input := s.input
	if input == nil {
		input = map[string]any{}
	}

	if segs[0] == inputNamespace {
		return walkPath(input, segs[1:], expr)
	}
	if _, isNode := s.plan.Node(segs[0]); isNode {
		ref := segs[0]
		if !s.plan.IsUpstream(ref, s.nodeID) {
			return nil, fmt.Errorf("%w: {{%s}} references node %q, which is not upstream of node %q", ErrUnresolvedReference, expr, ref, s.nodeID)
		}
		out, ok := s.results[ref]
		if !ok {
			return nil, fmt.Errorf("%w: {{%s}}: node %q has no result", ErrUnresolvedReference, expr, ref)
		}
		if len(segs) < 2 {
			return nil, fmt.Errorf("%w: {{%s}} must name an output port of node %q", ErrUnresolvedReference, expr, ref)
		}
		v, ok := out.GetPort(segs[1])
		if !ok {
			return nil, fmt.Errorf("%w: {{%s}}: node %q produced no output port %q", ErrUnresolvedReference, expr, ref, segs[1])
		}
		return walkPath(v.Data, segs[2:], expr)
	}
	return walkPath(input, segs, expr)
}

func walkPath(cur any, segs []string, expr string) (any, error) {
	for _, seg := range segs {
		if v, ok := cur.(node.Value); ok {
			cur = v.Data
		}
		switch typed := cur.(type) {
		case map[string]any:
			next, ok := typed[seg]
			if !ok {
				return nil, fmt.Errorf("%w: {{%s}}: key %q not found", ErrUnresolvedReference, expr, seg)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(typed) {
				return nil, fmt.Errorf("%w: {{%s}}: invalid index %q", ErrUnresolvedReference, expr, seg)
			}
			cur = typed[i]
		default:
			return nil, fmt.Errorf("%w: {{%s}}: cannot select %q from a %T value", ErrUnresolvedReference, expr, seg, cur)
		}
	}
	if v, ok := cur.(node.Value); ok {
		cur = v.Data
	}
	return cur, nil
}

func stringify(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// deepCopy copies the JSON-shaped containers so runtime values handed to a
// node cannot alias the workflow input or another node's output.
func deepCopy(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, nested := range typed {
			out[k] = deepCopy(nested)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, nested := range typed {
			out[i] = deepCopy(nested)
		}
		return out
	default:
		return typed
	}
}
