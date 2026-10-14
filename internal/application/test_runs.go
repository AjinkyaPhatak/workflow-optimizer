package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/observability"
	"workflow-optimizer/internal/workflow"
	"workflow-optimizer/internal/workspace"
)

// Test run limits (Phase E3).
const (
	TestRunTimeout     = 2 * time.Minute
	TestRunNodeTimeout = time.Minute
)

// TestRunService runs the editor's draft (Phase E3): the whole workflow, or
// only the nodes a selected node depends on and that node, synchronously
// inside the API, without saving, publishing or queueing anything. Nodes
// really run (an email node sends): the editor asks before including any
// node with side effects.
type TestRunService struct {
	workflows *WorkflowService
	nodes     node.Registry
	// expose lets node outputs into the result (redacted), like the
	// execution debugger (OBSERVABILITY_EXPOSE_NODE_DATA).
	expose bool
}

// NewTestRunService wires the service.
func NewTestRunService(workflows *WorkflowService, nodes node.Registry, expose bool) *TestRunService {
	return &TestRunService{workflows: workflows, nodes: nodes, expose: expose}
}

// TestNodeResult is one node of a test run.
type TestNodeResult struct {
	NodeID     string         `json:"node_id"`
	NodeType   string         `json:"node_type"`
	Status     string         `json:"status"` // COMPLETED, FAILED or SKIPPED
	Output     map[string]any `json:"output,omitempty"`
	Error      *TestNodeError `json:"error,omitempty"`
	DurationMS *int64         `json:"duration_ms,omitempty"`
}

// TestNodeError is a node failure.
type TestNodeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// TestRunResult is the outcome of a test run.
type TestRunResult struct {
	Status string           `json:"status"` // COMPLETED or FAILED
	Error  *TestNodeError   `json:"error,omitempty"`
	Nodes  []TestNodeResult `json:"nodes"`
	// Output is the exit nodes' output (a full-workflow test).
	Output map[string]any `json:"output,omitempty"`
}

// Upstream is the definition reduced to target and every node it depends
// on (with the connections among them).
func Upstream(def workflow.Definition, target string) (workflow.Definition, bool) {
	keep := map[string]bool{}
	var visit func(id string)
	visit = func(id string) {
		if keep[id] {
			return
		}
		keep[id] = true
		for _, e := range def.Edges {
			if e.Target == id {
				visit(e.Source)
			}
		}
	}
	found := false
	for _, n := range def.Nodes {
		found = found || n.ID == target
	}
	if !found {
		return def, false
	}
	visit(target)
	out := def
	out.Nodes, out.Edges = nil, []workflow.Edge{}
	for _, n := range def.Nodes {
		if keep[n.ID] {
			out.Nodes = append(out.Nodes, n)
		}
	}
	for _, e := range def.Edges {
		if keep[e.Source] && keep[e.Target] {
			out.Edges = append(out.Edges, e)
		}
	}
	return out, true
}

// testObserver records each node's outcome in execution order.
type testObserver struct {
	results []TestNodeResult
	index   map[string]int
	started map[string]time.Time
	expose  bool
}

func (o *testObserver) at(id, typ string) *TestNodeResult {
	if i, ok := o.index[id]; ok {
		return &o.results[i]
	}
	o.index[id] = len(o.results)
	o.results = append(o.results, TestNodeResult{NodeID: id, NodeType: typ})
	return &o.results[len(o.results)-1]
}

func (o *testObserver) NodeStarted(_ context.Context, id, typ string, _ node.NodeInput) error {
	o.at(id, typ)
	o.started[id] = time.Now()
	return nil
}

func testError(err error) *TestNodeError {
	e := execution.ErrorFromExecution(err)
	return &TestNodeError{Code: string(e.Code), Message: e.Message}
}

func (o *testObserver) NodeFinished(_ context.Context, id string, out node.NodeOutput, err error) error {
	r := o.at(id, "")
	ms := time.Since(o.started[id]).Milliseconds()
	r.DurationMS = &ms
	if err != nil {
		r.Status, r.Error, r.Output = "FAILED", testError(err), nil
		return nil
	}
	r.Status, r.Error = "COMPLETED", nil
	if o.expose {
		ports := make(map[string]any, len(out.Ports))
		for k, v := range out.Ports {
			ports[k] = v.Data
		}
		r.Output = observability.RedactMap(ports)
	}
	return nil
}

func (o *testObserver) NodeFailedBeforeExecute(_ context.Context, id, typ string, stage execution.Stage, err error) error {
	r := o.at(id, typ)
	r.Status, r.Error = "FAILED", testError(&execution.NodeExecutionError{NodeID: id, NodeType: typ, Stage: stage, Err: err})
	return nil
}

func (o *testObserver) NodeBranchSkipped(_ context.Context, id, typ string) {
	o.at(id, typ).Status = "SKIPPED"
}

// Run tests a draft definition. untilNodeID limits the run to that node and
// what it depends on ("" runs everything).
func (s *TestRunService) Run(ctx context.Context, user, workflowID uuid.UUID, raw json.RawMessage, input map[string]any, untilNodeID string) (TestRunResult, error) {
	_, workspaceID, err := s.workflows.workflow(ctx, user, workflowID, workspace.ActionExecute)
	if err != nil {
		return TestRunResult{}, err
	}
	def, err := DecodeDefinition(raw)
	if err != nil {
		return TestRunResult{}, err
	}
	if untilNodeID = strings.TrimSpace(untilNodeID); untilNodeID != "" {
		var ok bool
		if def, ok = Upstream(def, untilNodeID); !ok {
			return TestRunResult{}, &InvalidError{Message: "the node to test is not in the workflow"}
		}
	}
	if res := s.workflows.validate(def, workflow.ValidationDraft); !res.Valid {
		return TestRunResult{}, &ValidationFailedError{Errors: res.Errors}
	}
	if err := s.workflows.checkCredentials(ctx, workspaceID, def); err != nil {
		return TestRunResult{}, err
	}
	if input == nil {
		input = map[string]any{}
	}
	input, problems := def.ApplyVariables(input)
	if len(problems) > 0 {
		return TestRunResult{}, &InvalidError{Message: "invalid input: " + strings.Join(problems, "; ")}
	}

	ctx, cancel := context.WithTimeout(ctx, TestRunTimeout)
	defer cancel()
	obs := &testObserver{index: map[string]int{}, started: map[string]time.Time{}, expose: s.expose}
	res, runErr := execution.NewGraphExecutor(s.nodes).ExecuteWithOptions(ctx, def, input, execution.ExecuteOptions{
		Observer: obs, Scope: node.Scope{WorkspaceID: workspaceID}, OperationKey: "test-" + uuid.NewString(), NodeTimeout: TestRunNodeTimeout,
	})
	out := TestRunResult{Status: "COMPLETED", Nodes: obs.results}
	if out.Nodes == nil {
		out.Nodes = []TestNodeResult{}
	}
	if runErr != nil {
		out.Status, out.Error = "FAILED", testError(runErr)
		if errors.Is(runErr, context.DeadlineExceeded) {
			out.Error = &TestNodeError{Code: "TIMEOUT", Message: "the test run took longer than 2 minutes"}
		}
		return out, nil
	}
	if s.expose && len(res.OutputNodeIDs) > 0 {
		out.Output = map[string]any{}
		for _, id := range res.OutputNodeIDs {
			ports := map[string]any{}
			for k, v := range res.Outputs[id].Ports {
				ports[k] = v.Data
			}
			out.Output[id] = observability.RedactMap(ports)
		}
	}
	return out, nil
}
