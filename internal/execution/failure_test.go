package execution_test

import (
	"context"
	"errors"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
)

var errBoom = errors.New("boom")

func TestNodeFailureReturnsStructuredError(t *testing.T) {
	e := newEnv(t)
	e.rec.hooks["B"] = func(context.Context) error {
		return node.NewNodeError(node.ErrCodeExecutionFailed, "b exploded", false)
	}
	def := graph(nodes(entry("in"), probe("A"), probe("B"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "out", "value"))
	res, err := e.run(t, def, map[string]any{})
	if err == nil {
		t.Fatal("expected failure")
	}
	var nodeErr *execution.NodeExecutionError
	if !errors.As(err, &nodeErr) || nodeErr.NodeID != "B" || nodeErr.NodeType != probeType || nodeErr.Stage != execution.StageExecute {
		t.Fatalf("err = %#v", err)
	}
	var cause *node.NodeError
	if !errors.As(err, &cause) || cause.Code != node.ErrCodeExecutionFailed {
		t.Fatalf("node error not preserved: %v", err)
	}
	if res.Outputs != nil || res.OutputNodeIDs != nil {
		t.Fatal("failed execution must not report workflow outputs")
	}
	if !equalStrings(res.State.Completed, []string{"in", "A"}) {
		t.Fatalf("partial state = %v", res.State.Completed)
	}
}

func TestDownstreamNodesDoNotRunAfterFailure(t *testing.T) {
	e := newEnv(t)
	e.rec.hooks["B"] = func(context.Context) error { return errBoom }
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), probe("D"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "C", "in"),
		edge("C", "out", "D", "in"), edge("D", "out", "out", "value"))
	_, err := e.run(t, def, map[string]any{})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A", "B"}) {
		t.Fatalf("executed = %v, want [A B]", got)
	}
}

func TestIndependentNodesDoNotRunAfterFailure(t *testing.T) {
	e := newEnv(t)
	e.rec.hooks["a_fail"] = func(context.Context) error { return errBoom }
	// z_ok does not depend on a_fail but is planned after it (ID order).
	def := graph(nodes(entry("in"), probe("a_fail"), probe("z_ok"), wfNode("merge_1", "merge", nil), exit("out")),
		edge("in", "data", "a_fail", "in"), edge("in", "data", "z_ok", "in"),
		edge("a_fail", "out", "merge_1", "left"), edge("z_ok", "out", "merge_1", "right"),
		edge("merge_1", "merged", "out", "value"))
	res, err := e.run(t, def, map[string]any{})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"a_fail"}) {
		t.Fatalf("executed = %v, want [a_fail]", got)
	}
	if _, ran := res.State.Results["z_ok"]; ran {
		t.Fatal("independent node executed after fail-fast stop")
	}
}

// silentNode declares a required output but never produces it: a broken node contract.
type silentNode struct{}

func (silentNode) Type() string { return "test.silent" }
func (silentNode) Execute(ctx context.Context, _ node.NodeInput) (node.NodeOutput, error) {
	return node.NewNodeOutput(nil), ctx.Err()
}

func TestMissingRequiredSourcePortFailsSafely(t *testing.T) {
	e := newEnv(t)
	def := probeDefinition("test.silent")
	if err := e.reg.RegisterNode(silentNode{}, def); err != nil {
		t.Fatal(err)
	}
	wf := graph(nodes(entry("in"), wfNode("S", "test.silent", nil), probe("B"), exit("out")),
		edge("in", "data", "S", "in"), edge("S", "out", "B", "in"), edge("B", "out", "out", "value"))
	_, err := e.run(t, wf, map[string]any{})
	if !errors.Is(err, execution.ErrMissingSourcePort) {
		t.Fatalf("err = %v, want ErrMissingSourcePort", err)
	}
	var nodeErr *execution.NodeExecutionError
	if !errors.As(err, &nodeErr) || nodeErr.NodeID != "B" || nodeErr.Stage != execution.StageResolveInputs {
		t.Fatalf("err = %#v", err)
	}
	if len(e.rec.calls) != 0 {
		t.Fatal("B must not execute without its input")
	}
}

func TestDefensiveGraphErrorsDoNotPanic(t *testing.T) {
	e := newEnv(t)
	cyclic := graph(nodes(entry("in"), probe("A"), probe("B"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "A", "a"),
		edge("B", "out", "out", "value"))
	// Bypass the validator on purpose: impossible input reaching the executor.
	if _, err := e.exec.Execute(context.Background(), cyclic, nil); !errors.Is(err, execution.ErrGraphCycle) {
		t.Fatalf("err = %v, want ErrGraphCycle", err)
	}
	badPort := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "not_a_port"), edge("A", "out", "out", "value"))
	if _, err := e.exec.Execute(context.Background(), badPort, nil); !errors.Is(err, execution.ErrInvalidConnection) {
		t.Fatalf("err = %v, want ErrInvalidConnection", err)
	}
	fanIn := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), exit("out")),
		edge("in", "data", "A", "in"), edge("in", "data", "B", "in"),
		edge("A", "out", "C", "in"), edge("B", "out", "C", "in"), edge("C", "out", "out", "value"))
	e.rec.calls = nil
	if _, err := e.exec.Execute(context.Background(), fanIn, nil); !errors.Is(err, execution.ErrInvalidConnection) {
		t.Fatalf("err = %v, want ErrInvalidConnection", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A", "B"}) {
		t.Fatalf("executed = %v, want [A B] (C must not run)", got)
	}
}

func TestNilRegistryAndContext(t *testing.T) {
	def := graph(nodes(entry("in")))
	if _, err := execution.NewGraphExecutor(nil).Execute(context.Background(), def, nil); !errors.Is(err, execution.ErrNilRegistry) {
		t.Fatalf("err = %v", err)
	}
	e := newEnv(t)
	var nilCtx context.Context // deliberately nil: exercises the guard
	if _, err := e.exec.Execute(nilCtx, def, nil); !errors.Is(err, execution.ErrNilContext) {
		t.Fatalf("err = %v", err)
	}
}
