package execution_test

import (
	"context"
	"errors"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
)

func TestExecutorDispatchesThroughSuppliedRegistry(t *testing.T) {
	// Two canonical registries bind the same node type to different instances;
	// only the registry given to the executor may be used.
	first, second := newEnv(t), newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	first.mustValid(t, def)
	if _, err := first.exec.Execute(context.Background(), def, map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(first.rec.calls) != 1 || len(second.rec.calls) != 0 {
		t.Fatalf("dispatch did not go through the supplied registry: first=%v second=%v", first.rec.labels(), second.rec.labels())
	}
}

func TestMissingRegistryImplementationIsCleanError(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), wfNode("X", "not.registered", nil), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "X", "in"), edge("X", "out", "out", "value"))
	// Not validator-approved on purpose: this state should be impossible.
	_, err := e.exec.Execute(context.Background(), def, map[string]any{})
	if !errors.Is(err, execution.ErrNodeImplementationMissing) || !errors.Is(err, node.ErrNodeNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(e.rec.calls) != 0 {
		t.Fatal("no node may run when a registration is missing")
	}
}

func TestMissingRegistryDefinitionIsCleanError(t *testing.T) {
	e := newEnv(t)
	if err := e.reg.Register(&probeNode{typ: "test.nodef", rec: e.rec}); err != nil {
		t.Fatal(err)
	}
	def := graph(nodes(entry("in"), wfNode("X", "test.nodef", nil), exit("out")),
		edge("in", "data", "X", "in"), edge("X", "out", "out", "value"))
	_, err := e.exec.Execute(context.Background(), def, map[string]any{})
	if !errors.Is(err, execution.ErrNodeDefinitionMissing) || !errors.Is(err, node.ErrDefinitionNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(e.rec.calls) != 0 {
		t.Fatal("no node may run when a definition is missing")
	}
}

func TestGraphExecutorSatisfiesExecutorInterface(t *testing.T) {
	var _ execution.Executor = execution.NewGraphExecutor(node.NewRegistry())
	// node.Registry remains usable as the legacy NodeResolver boundary.
	var _ execution.NodeResolver = node.NewRegistry()
}
