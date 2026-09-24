package execution

import (
	"container/heap"
	"fmt"
	"sort"

	"workflow-optimizer/internal/workflow"
)

// Plan is a deterministic, sequential execution plan for a workflow graph.
//
// Order lists every node exactly once such that each node appears after all
// nodes it depends on through edges. When several nodes are ready at the same
// time, the lexicographically smallest node ID runs first. Go map iteration
// never influences the order.
//
// A Plan is pure data derived from the definition; it performs no validation
// beyond what is needed to order the graph safely (Phase 6 owns validation).
type Plan struct {
	Order []string

	nodes     map[string]workflow.Node
	incoming  map[string][]workflow.Edge // in definition order
	ancestors map[string]map[string]struct{}
}

// NewPlan builds the deterministic topological execution order for a
// definition using Kahn's algorithm with a node-ID min-heap as tie-breaker.
// It returns ErrInvalidPlanInput or ErrGraphCycle instead of panicking when
// handed a graph that should have been rejected by the validator.
func NewPlan(def workflow.Definition) (Plan, error) {
	nodes := make(map[string]workflow.Node, len(def.Nodes))
	for i, n := range def.Nodes {
		if n.ID == "" {
			return Plan{}, fmt.Errorf("%w: nodes[%d] has an empty ID", ErrInvalidPlanInput, i)
		}
		if _, dup := nodes[n.ID]; dup {
			return Plan{}, fmt.Errorf("%w: duplicate node ID %q", ErrInvalidPlanInput, n.ID)
		}
		nodes[n.ID] = n
	}

	indegree := make(map[string]int, len(nodes))
	successors := make(map[string][]string, len(nodes))
	incoming := make(map[string][]workflow.Edge, len(nodes))
	seenDependency := make(map[[2]string]bool, len(def.Edges))
	for i, e := range def.Edges {
		if _, ok := nodes[e.Source]; !ok {
			return Plan{}, fmt.Errorf("%w: edges[%d] (%q) references unknown source node %q", ErrInvalidPlanInput, i, e.ID, e.Source)
		}
		if _, ok := nodes[e.Target]; !ok {
			return Plan{}, fmt.Errorf("%w: edges[%d] (%q) references unknown target node %q", ErrInvalidPlanInput, i, e.ID, e.Target)
		}
		incoming[e.Target] = append(incoming[e.Target], e)
		dep := [2]string{e.Source, e.Target}
		if seenDependency[dep] {
			continue // several port edges between the same nodes are one dependency
		}
		seenDependency[dep] = true
		successors[e.Source] = append(successors[e.Source], e.Target)
		indegree[e.Target]++
	}

	ready := &idHeap{}
	for id := range nodes {
		if indegree[id] == 0 {
			*ready = append(*ready, id)
		}
	}
	heap.Init(ready)

	order := make([]string, 0, len(nodes))
	for ready.Len() > 0 {
		id := heap.Pop(ready).(string)
		order = append(order, id)
		for _, next := range successors[id] {
			indegree[next]--
			if indegree[next] == 0 {
				heap.Push(ready, next)
			}
		}
	}
	if len(order) != len(nodes) {
		blocked := make([]string, 0, len(nodes)-len(order))
		for id, d := range indegree {
			if d > 0 {
				blocked = append(blocked, id)
			}
		}
		sort.Strings(blocked)
		return Plan{}, fmt.Errorf("%w: nodes %v cannot be ordered", ErrGraphCycle, blocked)
	}

	ancestors := make(map[string]map[string]struct{}, len(order))
	for _, id := range order {
		set := make(map[string]struct{})
		for _, e := range incoming[id] {
			set[e.Source] = struct{}{}
			for a := range ancestors[e.Source] {
				set[a] = struct{}{}
			}
		}
		ancestors[id] = set
	}

	return Plan{Order: order, nodes: nodes, incoming: incoming, ancestors: ancestors}, nil
}

// Node returns the workflow node instance with the given ID.
func (p Plan) Node(id string) (workflow.Node, bool) {
	n, ok := p.nodes[id]
	return n, ok
}

// Incoming returns the edges targeting a node, in definition order.
func (p Plan) Incoming(id string) []workflow.Edge {
	return p.incoming[id]
}

// IsUpstream reports whether ancestorID is a transitive dependency of nodeID.
func (p Plan) IsUpstream(ancestorID, nodeID string) bool {
	_, ok := p.ancestors[nodeID][ancestorID]
	return ok
}

// idHeap is a min-heap of node IDs used as the deterministic ready queue.
type idHeap []string

func (h idHeap) Len() int           { return len(h) }
func (h idHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h idHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *idHeap) Push(x any)        { *h = append(*h, x.(string)) }
func (h *idHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
