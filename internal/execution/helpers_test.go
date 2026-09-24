package execution_test

import (
	"context"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

const probeType = "test.probe"

// call is one observed Execute invocation of a probe node.
type call struct {
	Label string
	Input node.NodeInput
	Ctx   context.Context
}

// recorder observes probe executions and lets a test inject behaviour for a
// specific node label (failure, cancellation, context checks).
type recorder struct {
	calls []call
	hooks map[string]func(ctx context.Context) error
}

func (r *recorder) labels() []string {
	out := make([]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = c.Label
	}
	return out
}

func (r *recorder) call(t *testing.T, label string) call {
	t.Helper()
	for _, c := range r.calls {
		if c.Label == label {
			return c
		}
	}
	t.Fatalf("node %q was not executed; executed: %v", label, r.labels())
	return call{}
}

// probeNode is a test-only node.Node that makes execution observable. Its
// identity inside a workflow comes from its "label" config field.
type probeNode struct {
	typ string
	rec *recorder
}

func (p *probeNode) Type() string { return p.typ }

func (p *probeNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	// Record the invocation before honouring ctx, so tests observe whether the
	// executor itself stopped scheduling (not just the node refusing to work).
	label, _ := in.GetStringConfig("label")
	p.rec.calls = append(p.rec.calls, call{Label: label, Input: in, Ctx: ctx})
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	if hook := p.rec.hooks[label]; hook != nil {
		if err := hook(ctx); err != nil {
			return node.NodeOutput{}, err
		}
	}
	received := map[string]any{}
	for name, v := range in.Ports {
		received[name] = v.Data
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("out", node.NewJSONValue(map[string]any{"label": label, "ports": received}))
	if msg, ok := in.GetStringConfig("message"); ok {
		out.SetPort("text", node.NewStringValue(msg))
	}
	return out, nil
}

func probeDefinition(typ string) node.NodeDefinition {
	return node.NodeDefinition{
		Type:     typ,
		Name:     "Probe",
		Category: node.CategoryUtilities,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("in", node.ValueTypeJSON, false, ""),
			node.NewPortDefinition("a", node.ValueTypeJSON, false, ""),
			node.NewPortDefinition("b", node.ValueTypeJSON, false, ""),
			node.NewMultiPortDefinition("many", node.ValueTypeJSON, false, ""),
			node.NewPortDefinition("s", node.ValueTypeString, false, ""),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("out", node.ValueTypeJSON, true, ""),
			node.NewPortDefinition("text", node.ValueTypeString, false, ""),
		},
		Config: []node.ConfigField{
			node.NewConfigField("label", node.ValueTypeString, false, nil, ""),
			node.NewConfigField("message", node.ValueTypeString, false, nil, ""),
			node.NewConfigField("payload", node.ValueTypeObject, false, nil, ""),
		},
	}
}

type env struct {
	reg  node.Registry
	rec  *recorder
	exec *execution.GraphExecutor
}

// newEnv bootstraps the real application registry (all V1 nodes) and adds the
// test-only probe node through the canonical registration API.
func newEnv(t *testing.T) *env {
	t.Helper()
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	rec := &recorder{hooks: map[string]func(context.Context) error{}}
	if err := a.NodeRegistry.RegisterNode(&probeNode{typ: probeType, rec: rec}, probeDefinition(probeType)); err != nil {
		t.Fatalf("register probe: %v", err)
	}
	return &env{reg: a.NodeRegistry, rec: rec, exec: execution.NewGraphExecutor(a.NodeRegistry)}
}

// mustValid asserts the definition passes Phase 6 executable validation, so
// execution tests only exercise graphs the executor is contracted to accept.
func (e *env) mustValid(t *testing.T, def workflow.Definition) {
	t.Helper()
	if r := workflow.NewValidator(e.reg).Validate(def); !r.Valid {
		t.Fatalf("test workflow is not executable-valid: %+v", r.Errors)
	}
}

func (e *env) run(t *testing.T, def workflow.Definition, input map[string]any) (execution.ExecutionResult, error) {
	t.Helper()
	e.mustValid(t, def)
	return e.exec.Execute(context.Background(), def, input)
}

func wfNode(id, typ string, cfg map[string]any) workflow.Node {
	if cfg == nil {
		cfg = map[string]any{}
	}
	return workflow.Node{ID: id, Type: typ, Name: id, Position: &workflow.Position{}, Config: cfg}
}

func probe(id string) workflow.Node {
	return wfNode(id, probeType, map[string]any{"label": id})
}

func probeWith(id string, cfg map[string]any) workflow.Node {
	c := map[string]any{"label": id}
	for k, v := range cfg {
		c[k] = v
	}
	return wfNode(id, probeType, c)
}

func entry(id string) workflow.Node { return wfNode(id, "input", nil) }
func exit(id string) workflow.Node  { return wfNode(id, "output", nil) }

func edge(src, srcPort, dst, dstPort string) workflow.Edge {
	return workflow.Edge{ID: src + "." + srcPort + "->" + dst + "." + dstPort, Source: src, SourcePort: srcPort, Target: dst, TargetPort: dstPort}
}

func graph(nodes []workflow.Node, edges ...workflow.Edge) workflow.Definition {
	if edges == nil {
		edges = []workflow.Edge{}
	}
	return workflow.Definition{Version: workflow.DefinitionSchemaVersion, Nodes: nodes, Edges: edges, Settings: map[string]any{}}
}

func nodes(ns ...workflow.Node) []workflow.Node { return ns }

// portData returns the Data carried by a probe's recorded input port.
func portData(t *testing.T, c call, port string) any {
	t.Helper()
	v, ok := c.Input.GetPort(port)
	if !ok {
		t.Fatalf("node %q: input port %q not populated; ports=%v", c.Label, port, c.Input.Ports)
	}
	return v.Data
}

// labelOf extracts the upstream probe label from data produced on a probe "out" port.
func labelOf(t *testing.T, data any) string {
	t.Helper()
	m, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("expected probe output object, got %#v", data)
	}
	s, _ := m["label"].(string)
	return s
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
