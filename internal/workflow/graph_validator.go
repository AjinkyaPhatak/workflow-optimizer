package workflow

import (
	"fmt"
	"sort"
	"strings"
	nodepkg "workflow-optimizer/internal/node"
)

type ValidationErrorCode string

const (
	ErrInvalidWorkflow          ValidationErrorCode = "INVALID_WORKFLOW"
	ErrInvalidVersion           ValidationErrorCode = "INVALID_VERSION"
	ErrDuplicateNodeID          ValidationErrorCode = "DUPLICATE_NODE_ID"
	ErrInvalidNodeType          ValidationErrorCode = "INVALID_NODE_TYPE"
	ErrInvalidNodeConfig        ValidationErrorCode = "INVALID_NODE_CONFIG"
	ErrInvalidEdge              ValidationErrorCode = "INVALID_EDGE"
	ErrDuplicateEdgeID          ValidationErrorCode = "DUPLICATE_EDGE_ID"
	ErrInvalidSourceNode        ValidationErrorCode = "INVALID_SOURCE_NODE"
	ErrInvalidTargetNode        ValidationErrorCode = "INVALID_TARGET_NODE"
	ErrInvalidSourcePort        ValidationErrorCode = "INVALID_SOURCE_PORT"
	ErrInvalidTargetPort        ValidationErrorCode = "INVALID_TARGET_PORT"
	ErrInvalidPortDirection     ValidationErrorCode = "INVALID_PORT_DIRECTION"
	ErrIncompatiblePortTypes    ValidationErrorCode = "INCOMPATIBLE_PORT_TYPES"
	ErrMissingRequiredInput     ValidationErrorCode = "MISSING_REQUIRED_INPUT"
	ErrMultipleConnections      ValidationErrorCode = "MULTIPLE_CONNECTIONS_NOT_ALLOWED"
	ErrDuplicateEdge            ValidationErrorCode = "DUPLICATE_EDGE"
	ErrSelfReference            ValidationErrorCode = "SELF_REFERENCE"
	ErrGraphCycle               ValidationErrorCode = "GRAPH_CYCLE"
	ErrDisconnectedNode         ValidationErrorCode = "DISCONNECTED_NODE"
	ErrMissingInputNode         ValidationErrorCode = "MISSING_INPUT_NODE"
	ErrMissingOutputNode        ValidationErrorCode = "MISSING_OUTPUT_NODE"
	ErrInvalidVariableReference ValidationErrorCode = "INVALID_VARIABLE_REFERENCE"
)

type ValidationError struct {
	Code    ValidationErrorCode `json:"code"`
	Message string              `json:"message"`
	NodeID  string              `json:"node_id,omitempty"`
	EdgeID  string              `json:"edge_id,omitempty"`
	Port    string              `json:"port,omitempty"`
	Details map[string]any      `json:"details,omitempty"`
	Path    string              `json:"-"`
}
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors"`
}

// ValidationMode controls whether executable-only graph semantics are enforced.
type ValidationMode string

const (
	ValidationExecutable ValidationMode = "executable"
	ValidationDraft      ValidationMode = "draft"
)

type ValidationOptions struct {
	Mode ValidationMode
}
type Validator interface {
	Validate(Definition) ValidationResult
}
type GraphValidator struct{ reg nodepkg.Registry }

func NewValidator(r nodepkg.Registry) *GraphValidator { return &GraphValidator{r} }
func (v *GraphValidator) Validate(d Definition) ValidationResult {
	return v.ValidateWithOptions(d, ValidationOptions{Mode: ValidationExecutable})
}

// ValidateWithOptions validates a draft or an executable workflow. The zero
// value is executable so existing callers retain strict validation semantics.
func (v *GraphValidator) ValidateWithOptions(d Definition, options ValidationOptions) ValidationResult {
	strict := options.Mode != ValidationDraft
	es := []ValidationError{}
	add := func(c ValidationErrorCode, m, n, e, p string, x map[string]any) {
		es = append(es, ValidationError{Code: c, Message: m, NodeID: n, EdgeID: e, Port: p, Details: x})
	}
	if d.Version != DefinitionSchemaVersion {
		add(ErrInvalidVersion, "unsupported schema version", "", "", "", nil)
	}
	if d.Nodes == nil || d.Edges == nil || d.Settings == nil {
		add(ErrInvalidWorkflow, "top-level graph fields are required", "", "", "", nil)
	}
	ns := map[string]Node{}
	ds := map[string]nodepkg.NodeDefinition{}
	seen := map[string]bool{}
	entries, exits := []string{}, []string{}
	for _, n := range d.Nodes {
		if n.ID == "" || n.Type == "" || n.Name == "" || n.Position == nil || n.Config == nil {
			add(ErrInvalidWorkflow, "node has required fields missing", n.ID, "", "", nil)
		}
		if seen[n.ID] {
			add(ErrDuplicateNodeID, "duplicate node ID", n.ID, "", "", nil)
			continue
		}
		seen[n.ID] = true
		ns[n.ID] = n
		def, err := v.def(n.Type)
		if err != nil {
			add(ErrInvalidNodeType, "node type is not registered", n.ID, "", "", nil)
			continue
		}
		ds[n.ID] = def
		if def.Role == nodepkg.SemanticRoleEntry {
			entries = append(entries, n.ID)
		}
		if def.Role == nodepkg.SemanticRoleExit {
			exits = append(exits, n.ID)
		}
		v.config(n, def, add)
	}
	in := map[string]map[string]int{}
	adj := map[string][]string{}
	eids := map[string]bool{}
	logical := map[string]bool{}
	for _, e := range d.Edges {
		if e.ID == "" || e.Source == "" || e.Target == "" || e.SourcePort == "" || e.TargetPort == "" {
			add(ErrInvalidEdge, "edge has required fields missing", "", e.ID, "", nil)
		}
		if eids[e.ID] {
			add(ErrDuplicateEdgeID, "duplicate edge ID", "", e.ID, "", nil)
		}
		eids[e.ID] = true
		s, so := ns[e.Source]
		t, to := ns[e.Target]
		if !so {
			add(ErrInvalidSourceNode, "source node does not exist", "", e.ID, "", nil)
		}
		if !to {
			add(ErrInvalidTargetNode, "target node does not exist", "", e.ID, "", nil)
		}
		if !so || !to {
			continue
		}
		if e.Source == e.Target {
			add(ErrSelfReference, "edge connects node to itself", s.ID, e.ID, "", nil)
		}
		sp, out := ds[s.ID].GetOutputPort(e.SourcePort)
		_, sin := ds[s.ID].GetInputPort(e.SourcePort)
		tp, inp := ds[t.ID].GetInputPort(e.TargetPort)
		_, tout := ds[t.ID].GetOutputPort(e.TargetPort)
		if !out {
			c := ErrInvalidSourcePort
			if sin {
				c = ErrInvalidPortDirection
			}
			add(c, "source port must be an output", s.ID, e.ID, e.SourcePort, nil)
		}
		if !inp {
			c := ErrInvalidTargetPort
			if tout {
				c = ErrInvalidPortDirection
			}
			add(c, "target port must be an input", t.ID, e.ID, e.TargetPort, nil)
		}
		if !out || !inp {
			continue
		}
		key := e.Source + "|" + e.SourcePort + "|" + e.Target + "|" + e.TargetPort
		if logical[key] {
			add(ErrDuplicateEdge, "duplicate logical connection", "", e.ID, "", nil)
		}
		logical[key] = true
		// Structural adjacency is retained for cycle validation even when the
		// edge is type-invalid; only typed edges are valid input connections.
		adj[s.ID] = append(adj[s.ID], t.ID)
		if sp.Type != tp.Type {
			add(ErrIncompatiblePortTypes, "connected port types differ", s.ID, e.ID, e.SourcePort, nil)
			continue
		}
		if in[t.ID] == nil {
			in[t.ID] = map[string]int{}
		}
		in[t.ID][e.TargetPort]++
	}
	if strict {
		for id, def := range ds {
			for _, p := range def.Inputs {
				c := in[id][p.Name]
				if p.Required && c == 0 {
					add(ErrMissingRequiredInput, "required input has no connection", id, "", p.Name, nil)
				}
			}
		}
	}
	// Fan-in conflicts are structural defects, so drafts must reject them too.
	for id, def := range ds {
		for _, p := range def.Inputs {
			if c := in[id][p.Name]; !p.Multiple && c > 1 {
				add(ErrMultipleConnections, "input accepts one connection", id, "", p.Name, nil)
			}
		}
	}
	if len(entries) == 0 {
		if strict {
			add(ErrMissingInputNode, "workflow has no entry node", "", "", "", nil)
		}
	}
	if len(exits) == 0 {
		if strict {
			add(ErrMissingOutputNode, "workflow has no exit node", "", "", "", nil)
		}
	}
	if strict {
		reach := walk(entries, adj)
		for id, def := range ds {
			if def.Role != nodepkg.SemanticRoleEntry && !reach[id] {
				add(ErrDisconnectedNode, "node is unreachable from entry", id, "", "", nil)
			}
		}
	}
	if strict {
		reverse := map[string][]string{}
		for source, targets := range adj {
			for _, target := range targets {
				reverse[target] = append(reverse[target], source)
			}
		}
		canReachExit := walk(exits, reverse)
		for id, def := range ds {
			if def.Role != nodepkg.SemanticRoleExit && !canReachExit[id] {
				add(ErrDisconnectedNode, "node cannot reach an exit", id, "", "", nil)
			}
		}
	}
	if cyc(ns, adj) {
		add(ErrGraphCycle, "workflow contains a directed cycle", "", "", "", nil)
	}
	v.refs(d.Settings, add)
	for _, n := range d.Nodes {
		v.refs(n.Config, add)
	}
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		return fmt.Sprintf("%s|%s|%s|%s|%s", a.Code, a.NodeID, a.EdgeID, a.Port, a.Message) < fmt.Sprintf("%s|%s|%s|%s|%s", b.Code, b.NodeID, b.EdgeID, b.Port, b.Message)
	})
	return ValidationResult{len(es) == 0, es}
}
func (v *GraphValidator) def(t string) (nodepkg.NodeDefinition, error) {
	if v.reg == nil {
		return nodepkg.NodeDefinition{}, fmt.Errorf("nil registry")
	}
	return v.reg.GetDefinition(t)
}
func (v *GraphValidator) config(n Node, d nodepkg.NodeDefinition, add func(ValidationErrorCode, string, string, string, string, map[string]any)) {
	for k, x := range n.Config {
		f, ok := d.GetConfigField(k)
		if !ok {
			add(ErrInvalidNodeConfig, "unknown configuration field", n.ID, "", k, nil)
			continue
		}
		if !kind(x, f.Type) {
			add(ErrInvalidNodeConfig, "configuration value has invalid type", n.ID, "", k, nil)
		}
	}
	for _, f := range d.Config {
		if _, ok := n.Config[f.Name]; f.Required && !ok && f.Default == nil {
			add(ErrInvalidNodeConfig, "required configuration field is missing", n.ID, "", f.Name, nil)
		}
	}
}
func kind(x any, t nodepkg.ValueType) bool {
	switch t {
	case nodepkg.ValueTypeString:
		_, ok := x.(string)
		return ok
	case nodepkg.ValueTypeNumber:
		switch x.(type) {
		case float64, float32, int, int32, int64:
			return true
		}
	case nodepkg.ValueTypeBoolean:
		_, ok := x.(bool)
		return ok
	case nodepkg.ValueTypeObject:
		_, ok := x.(map[string]any)
		return ok
	case nodepkg.ValueTypeArray:
		_, ok := x.([]any)
		return ok
	case nodepkg.ValueTypeJSON:
		return x != nil
	}
	return false
}
func walk(q []string, a map[string][]string) map[string]bool {
	r := map[string]bool{}
	for len(q) > 0 {
		x := q[0]
		q = q[1:]
		if r[x] {
			continue
		}
		r[x] = true
		q = append(q, a[x]...)
	}
	return r
}
func cyc(ns map[string]Node, a map[string][]string) bool {
	s := map[string]int{}
	var f func(string) bool
	f = func(x string) bool {
		if s[x] == 1 {
			return true
		}
		if s[x] == 2 {
			return false
		}
		s[x] = 1
		for _, y := range a[x] {
			if f(y) {
				return true
			}
		}
		s[x] = 2
		return false
	}
	for x := range ns {
		if f(x) {
			return true
		}
	}
	return false
}
func (v *GraphValidator) refs(x any, add func(ValidationErrorCode, string, string, string, string, map[string]any)) {
	switch z := x.(type) {
	case string:
		if malformedInterpolation(z) {
			add(ErrInvalidVariableReference, "malformed variable reference", "", "", "", nil)
		}
		for _, r := range VariableReferences(z) {
			_ = r // VariableReferences has already checked the static grammar.
		}
	case map[string]any:
		for _, y := range z {
			v.refs(y, add)
		}
	case []any:
		for _, y := range z {
			v.refs(y, add)
		}
	}
}

func malformedInterpolation(text string) bool {
	for rest := text; ; {
		start := strings.Index(rest, "{{")
		if start < 0 {
			return strings.Contains(rest, "}}")
		}
		rest = rest[start+2:]
		end := strings.Index(rest, "}}")
		if end < 0 || len(VariableReferences("{{"+rest[:end]+"}}")) != 1 {
			return true
		}
		rest = rest[end+2:]
	}
}
