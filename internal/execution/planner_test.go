package execution_test

import (
	"errors"
	"math/rand"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/workflow"
)

// Planner tests operate on raw graphs; the planner only needs structure.

func planOf(t *testing.T, def workflow.Definition) []string {
	t.Helper()
	p, err := execution.NewPlan(def)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p.Order
}

func indexOf(order []string, id string) int {
	for i, x := range order {
		if x == id {
			return i
		}
	}
	return -1
}

func assertBefore(t *testing.T, order []string, first, then string) {
	t.Helper()
	if indexOf(order, first) < 0 || indexOf(order, then) < 0 || indexOf(order, first) >= indexOf(order, then) {
		t.Fatalf("expected %q before %q in %v", first, then, order)
	}
}

func TestPlannerLinear(t *testing.T) {
	// Nodes listed in reverse to prove definition order does not drive planning.
	def := graph(nodes(probe("C"), probe("B"), probe("A")),
		edge("B", "out", "C", "in"), edge("A", "out", "B", "in"))
	if got := planOf(t, def); !equalStrings(got, []string{"A", "B", "C"}) {
		t.Fatalf("plan = %v, want [A B C]", got)
	}
}

func TestPlannerBranching(t *testing.T) {
	def := graph(nodes(probe("C"), probe("B"), probe("A")),
		edge("A", "out", "B", "in"), edge("A", "out", "C", "in"))
	order := planOf(t, def)
	assertBefore(t, order, "A", "B")
	assertBefore(t, order, "A", "C")
	if !equalStrings(order, []string{"A", "B", "C"}) {
		t.Fatalf("plan = %v, want [A B C]", order)
	}
}

func TestPlannerMerge(t *testing.T) {
	def := graph(nodes(probe("D"), probe("C"), probe("B"), probe("A")),
		edge("A", "out", "B", "in"), edge("A", "out", "C", "in"),
		edge("B", "out", "D", "many"), edge("C", "out", "D", "many"))
	order := planOf(t, def)
	assertBefore(t, order, "A", "B")
	assertBefore(t, order, "A", "C")
	assertBefore(t, order, "B", "D")
	assertBefore(t, order, "C", "D")
}

func TestPlannerDependenciesOverrideIDOrdering(t *testing.T) {
	// "a" sorts first but depends on "z"; ties among ready nodes use ID order.
	def := graph(nodes(probe("a"), probe("b"), probe("z")), edge("z", "out", "a", "in"))
	if got := planOf(t, def); !equalStrings(got, []string{"b", "z", "a"}) {
		t.Fatalf("plan = %v, want [b z a]", got)
	}
}

func TestPlannerDeterministicForIndependentNodes(t *testing.T) {
	ns := nodes(probe("root"), probe("c"), probe("a"), probe("z"), probe("b"), probe("sink"))
	es := []workflow.Edge{
		edge("root", "out", "c", "in"), edge("root", "out", "a", "in"),
		edge("root", "out", "z", "in"), edge("root", "out", "b", "in"),
		edge("a", "out", "sink", "many"), edge("b", "out", "sink", "many"),
		edge("c", "out", "sink", "many"), edge("z", "out", "sink", "many"),
	}
	want := []string{"root", "a", "b", "c", "z", "sink"}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 100; i++ {
		shuffledNodes := append([]workflow.Node(nil), ns...)
		shuffledEdges := append([]workflow.Edge(nil), es...)
		rng.Shuffle(len(shuffledNodes), func(a, b int) { shuffledNodes[a], shuffledNodes[b] = shuffledNodes[b], shuffledNodes[a] })
		rng.Shuffle(len(shuffledEdges), func(a, b int) { shuffledEdges[a], shuffledEdges[b] = shuffledEdges[b], shuffledEdges[a] })
		if got := planOf(t, graph(shuffledNodes, shuffledEdges...)); !equalStrings(got, want) {
			t.Fatalf("iteration %d: plan = %v, want %v", i, got, want)
		}
	}
}

func TestPlannerRejectsImpossibleInputWithoutPanicking(t *testing.T) {
	cases := map[string]struct {
		def  workflow.Definition
		want error
	}{
		"cycle": {graph(nodes(probe("A"), probe("B"), probe("C")),
			edge("A", "out", "B", "in"), edge("B", "out", "C", "in"), edge("C", "out", "B", "a")), execution.ErrGraphCycle},
		"self loop":      {graph(nodes(probe("A")), edge("A", "out", "A", "in")), execution.ErrGraphCycle},
		"unknown source": {graph(nodes(probe("A")), edge("ghost", "out", "A", "in")), execution.ErrInvalidPlanInput},
		"unknown target": {graph(nodes(probe("A")), edge("A", "out", "ghost", "in")), execution.ErrInvalidPlanInput},
		"duplicate id":   {graph(nodes(probe("A"), probe("A"))), execution.ErrInvalidPlanInput},
		"empty node id":  {graph(nodes(probe(""))), execution.ErrInvalidPlanInput},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := execution.NewPlan(tc.def)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPlanUpstreamRelation(t *testing.T) {
	def := graph(nodes(probe("A"), probe("B"), probe("C"), probe("X")),
		edge("A", "out", "B", "in"), edge("B", "out", "C", "in"), edge("A", "out", "X", "in"))
	p, err := execution.NewPlan(def)
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsUpstream("A", "C") || !p.IsUpstream("B", "C") {
		t.Fatal("transitive ancestors must be upstream")
	}
	if p.IsUpstream("X", "C") || p.IsUpstream("C", "A") || p.IsUpstream("C", "C") {
		t.Fatal("siblings, descendants and self must not be upstream")
	}
}
