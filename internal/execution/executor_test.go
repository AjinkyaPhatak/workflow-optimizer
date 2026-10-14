package execution_test

import (
	"context"
	"reflect"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
)

func TestLinearExecutionWithRealNodes(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), wfNode("json_1", "json", nil),
		wfNode("transform_1", "transform", map[string]any{"expression": "."}), exit("out")),
		edge("in", "data", "json_1", "input"),
		edge("json_1", "output", "transform_1", "input"),
		edge("transform_1", "output", "out", "value"))
	input := map[string]any{"query": "hello"}

	res, err := e.run(t, def, input)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Pure ID order would be [in json_1 out transform_1]; dependencies must win.
	if want := []string{"in", "json_1", "transform_1", "out"}; !equalStrings(res.State.Completed, want) {
		t.Fatalf("completed = %v, want %v", res.State.Completed, want)
	}
	if len(res.State.Results) != 4 {
		t.Fatalf("stored results = %d, want 4", len(res.State.Results))
	}
	got, ok := res.Outputs["out"].GetPort("result")
	if !ok || !reflect.DeepEqual(got.Data, input) {
		t.Fatalf("workflow output = %#v, want %#v", got, input)
	}
}

func TestLinearExecutionInvokesNodesAndPassesOutputsDownstream(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"),
		edge("B", "out", "C", "in"), edge("C", "out", "out", "value"))

	res, err := e.run(t, def, map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A", "B", "C"}) {
		t.Fatalf("executed = %v, want [A B C]", got)
	}
	// Entry node received the workflow input and forwarded it to A.
	if got := portData(t, e.rec.call(t, "A"), "in"); !reflect.DeepEqual(got, map[string]any{"k": "v"}) {
		t.Fatalf("A.in = %#v", got)
	}
	// B.in is exactly A.out as stored in the runtime state.
	aOut, _ := res.State.Results["A"].GetPort("out")
	if got := portData(t, e.rec.call(t, "B"), "in"); !reflect.DeepEqual(got, aOut.Data) {
		t.Fatalf("B.in = %#v, want A.out %#v", got, aOut.Data)
	}
	for _, id := range []string{"in", "A", "B", "C", "out"} {
		if _, ok := res.State.Results[id]; !ok {
			t.Fatalf("result for %q not stored", id)
		}
	}
}

func TestMultipleInputPortsArePopulated(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("M"), exit("out")),
		edge("in", "data", "A", "in"), edge("in", "data", "B", "in"),
		edge("A", "out", "M", "a"), edge("B", "out", "M", "b"),
		edge("M", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	m := e.rec.call(t, "M")
	if labelOf(t, portData(t, m, "a")) != "A" || labelOf(t, portData(t, m, "b")) != "B" {
		t.Fatalf("M ports wired incorrectly: %#v", m.Input.Ports)
	}
	if len(m.Input.Ports) != 2 {
		t.Fatalf("M received unexpected ports: %#v", m.Input.Ports)
	}
}

func TestMultiplePortPreservesAllValuesInEdgeOrder(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), probe("M"), exit("out")),
		edge("in", "data", "A", "in"), edge("in", "data", "B", "in"), edge("in", "data", "C", "in"),
		// Deliberately not in ID order: element order follows edge definition order.
		edge("C", "out", "M", "many"), edge("A", "out", "M", "many"), edge("B", "out", "M", "many"),
		edge("M", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	v, ok := e.rec.call(t, "M").Input.GetPort("many")
	if !ok || v.Type != node.ValueTypeArray {
		t.Fatalf("many = %#v, want array value", v)
	}
	items, _ := v.Array()
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = labelOf(t, it)
	}
	if !equalStrings(got, []string{"C", "A", "B"}) {
		t.Fatalf("many labels = %v, want [C A B]", got)
	}
}

func TestSingleEdgeOnMultiplePortStillArray(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("M"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "M", "many"), edge("M", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	items, ok := e.rec.call(t, "M").Input.Ports["many"].Array()
	if !ok || len(items) != 1 || labelOf(t, items[0]) != "A" {
		t.Fatalf("many = %#v, want one-element array", e.rec.call(t, "M").Input.Ports["many"])
	}
}

func TestRealMergeNodeReceivesEveryBranch(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), wfNode("merge_1", "merge", nil), exit("out")),
		edge("in", "data", "A", "in"), edge("in", "data", "B", "in"), edge("in", "data", "C", "in"),
		edge("A", "out", "merge_1", "left"), edge("B", "out", "merge_1", "left"),
		edge("C", "out", "merge_1", "right"), edge("merge_1", "merged", "out", "value"))
	res, err := e.run(t, def, map[string]any{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	result, _ := res.Outputs["out"].GetPort("result")
	merged := result.Data.(map[string]any)
	left, _ := merged["left"].([]any)
	right, _ := merged["right"].([]any)
	if len(left) != 2 || labelOf(t, left[0]) != "A" || labelOf(t, left[1]) != "B" {
		t.Fatalf("merged.left = %#v", merged["left"])
	}
	if len(right) != 1 || labelOf(t, right[0]) != "C" {
		t.Fatalf("merged.right = %#v", merged["right"])
	}
}

func TestBranchingExecutesAllBranches(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), exit("out_b"), exit("out_c")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("A", "out", "C", "in"),
		edge("B", "out", "out_b", "value"), edge("C", "out", "out_c", "value"))
	res, err := e.run(t, def, map[string]any{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"A", "B", "C"}) {
		t.Fatalf("executed = %v", got)
	}
	for _, id := range []string{"B", "C"} {
		if labelOf(t, portData(t, e.rec.call(t, id), "in")) != "A" {
			t.Fatalf("%s did not receive A's output", id)
		}
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("outputs = %v", res.OutputNodeIDs)
	}
}

func TestMergeRunsOnlyAfterAllDependencies(t *testing.T) {
	e := newEnv(t)
	// Branch C is longer (C1 -> C2) and its IDs sort before D, so D must wait on dependencies, not ID order.
	def := graph(nodes(entry("in"), probe("A"), probe("B"), probe("C1"), probe("C2"), probe("D"), exit("out")),
		edge("in", "data", "A", "in"),
		edge("A", "out", "B", "in"), edge("A", "out", "C1", "in"), edge("C1", "out", "C2", "in"),
		edge("B", "out", "D", "many"), edge("C2", "out", "D", "many"),
		edge("D", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	order := e.rec.labels()
	assertBefore(t, order, "B", "D")
	assertBefore(t, order, "C2", "D")
	items, _ := e.rec.call(t, "D").Input.Ports["many"].Array()
	if len(items) != 2 {
		t.Fatalf("D received %d values, want 2", len(items))
	}
}

func TestOutputRoleNodesFormWorkflowResult(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	res, err := e.run(t, def, map[string]any{"x": 1.0})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !equalStrings(res.OutputNodeIDs, []string{"out"}) || len(res.Outputs) != 1 {
		t.Fatalf("outputs = %v", res.OutputNodeIDs)
	}
	v, ok := res.Outputs["out"].GetPort("result")
	if !ok || labelOf(t, v.Data) != "A" {
		t.Fatalf("out.result = %#v", v)
	}
	if _, isOutput := res.Outputs["A"]; isOutput {
		t.Fatal("non-exit node must not appear in workflow outputs")
	}
}

func TestMultipleOutputNodesDeterministic(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("B"), exit("out_z"), exit("out_a")),
		edge("in", "data", "A", "in"), edge("in", "data", "B", "in"),
		edge("A", "out", "out_z", "value"), edge("B", "out", "out_a", "value"))
	for i := 0; i < 20; i++ {
		res, err := e.run(t, def, map[string]any{})
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !equalStrings(res.OutputNodeIDs, []string{"out_a", "out_z"}) {
			t.Fatalf("output ids = %v", res.OutputNodeIDs)
		}
		za, _ := res.Outputs["out_z"].GetPort("result")
		aa, _ := res.Outputs["out_a"].GetPort("result")
		if labelOf(t, za.Data) != "A" || labelOf(t, aa.Data) != "B" {
			t.Fatal("output nodes mapped to wrong results")
		}
		if !equalStrings(res.State.Completed, []string{"in", "A", "B", "out_a", "out_z"}) {
			t.Fatalf("completed = %v", res.State.Completed)
		}
	}
}

func TestNilWorkflowInputLeavesEntryNodeToItsOwnContract(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	if _, err := e.run(t, def, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// The Input node falls back to its (empty) config when no workflow input is supplied.
	if got := portData(t, e.rec.call(t, "A"), "in"); !reflect.DeepEqual(got, map[string]any{}) {
		t.Fatalf("A.in = %#v", got)
	}
}

// Phase E1: the untaken output of a Condition makes everything it alone
// feeds skip; a Merge continues with the branch that ran.
func TestUntakenBranchIsSkipped(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), wfNode("cond", "condition", nil), probe("T"), probe("F"), probe("F2"),
		wfNode("merge_1", "merge", nil), probeWith("after", map[string]any{"message": "T={{T.text}} F={{F.text}}"}), exit("out")),
		edge("in", "data", "cond", "value"),
		edge("cond", "true", "T", "in"), edge("cond", "false", "F", "in"), edge("F", "out", "F2", "in"),
		edge("T", "out", "merge_1", "left"), edge("F2", "out", "merge_1", "right"),
		edge("merge_1", "value", "after", "in"), edge("after", "out", "out", "value"))
	def.Nodes[2].Config["message"] = "yes"
	res, err := e.run(t, def, map[string]any{"flag": true})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"T", "after"}) {
		t.Fatalf("executed %v, want only the taken branch and what follows the merge", got)
	}
	if !res.State.Skipped["F"] || !res.State.Skipped["F2"] || res.State.Skipped["merge_1"] {
		t.Fatalf("skipped: %v", res.State.Skipped)
	}
	// A reference to a skipped node resolves to nothing instead of failing.
	if msg := e.rec.call(t, "after").Input.Config["message"]; msg != "T=yes F=" {
		t.Fatalf("message = %q", msg)
	}
	if _, ok := res.Outputs["out"]; !ok {
		t.Fatal("the exit after the merge ran")
	}
}

// A skipped exit node produces no workflow output, and skips are observed.
func TestSkippedExitAndObserver(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), wfNode("cond", "condition", map[string]any{"value": "{{input.n}}", "operator": "greater_than", "compare": "10"}),
		wfNode("big", "output", nil), wfNode("small", "output", nil)),
		edge("in", "data", "cond", "value"), edge("cond", "true", "big", "value"), edge("cond", "false", "small", "value"))
	e.mustValid(t, def)
	obs := &skipRecorder{}
	res, err := e.exec.ExecuteWithObserver(context.Background(), def, map[string]any{"n": 3.0}, obs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Outputs["big"]; ok || len(res.OutputNodeIDs) != 1 || res.OutputNodeIDs[0] != "small" {
		t.Fatalf("outputs: %v", res.OutputNodeIDs)
	}
	if !equalStrings(obs.skipped, []string{"big"}) {
		t.Fatalf("observed skips: %v", obs.skipped)
	}
}

type skipRecorder struct{ skipped []string }

func (r *skipRecorder) NodeStarted(context.Context, string, string, node.NodeInput) error { return nil }
func (r *skipRecorder) NodeFinished(context.Context, string, node.NodeOutput, error) error {
	return nil
}
func (r *skipRecorder) NodeFailedBeforeExecute(context.Context, string, string, execution.Stage, error) error {
	return nil
}
func (r *skipRecorder) NodeBranchSkipped(_ context.Context, id, _ string) {
	r.skipped = append(r.skipped, id)
}

// Switch takes exactly one route.
func TestSwitchRoutes(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), wfNode("sw", "switch", map[string]any{"value": "{{input.priority}}", "case_1": "high", "case_2": "low"}),
		probe("H"), probe("L"), probe("O"), exit("oH"), exit("oL"), exit("oO")),
		edge("in", "data", "sw", "value"), edge("sw", "case_1", "H", "in"), edge("sw", "case_2", "L", "in"), edge("sw", "otherwise", "O", "in"),
		edge("H", "out", "oH", "value"), edge("L", "out", "oL", "value"), edge("O", "out", "oO", "value"))
	for priority, want := range map[string]string{"High": "H", "low": "L", "urgent": "O"} {
		e.rec.calls = nil
		res, err := e.run(t, def, map[string]any{"priority": priority})
		if err != nil {
			t.Fatal(err)
		}
		if got := e.rec.labels(); !equalStrings(got, []string{want}) || len(res.OutputNodeIDs) != 1 || res.OutputNodeIDs[0] != "o"+want {
			t.Fatalf("%s: executed %v, outputs %v", priority, got, res.OutputNodeIDs)
		}
	}
}

func TestDefinitionIsNotMutated(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probeWith("A", map[string]any{
		"message": "{{input.query}}",
		"payload": map[string]any{"nested": "{{input.query}}"},
	}), exit("out")), edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{"query": "resolved"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	cfg := def.Nodes[1].Config
	if cfg["message"] != "{{input.query}}" || cfg["payload"].(map[string]any)["nested"] != "{{input.query}}" {
		t.Fatalf("definition config mutated: %#v", cfg)
	}
	if e.rec.call(t, "A").Input.Config["message"] != "resolved" {
		t.Fatal("runtime config was not resolved")
	}
}
