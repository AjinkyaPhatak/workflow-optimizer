package execution_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

type loaderFunc func(ctx context.Context, id uuid.UUID) (workflow.Definition, error)

func (f loaderFunc) LoadDefinition(ctx context.Context, id uuid.UUID) (workflow.Definition, error) {
	return f(ctx, id)
}

type runnerEnv struct {
	*env
	repo   *memRepo
	runner *execution.Runner
}

func newRunnerEnv(t *testing.T, def workflow.Definition) *runnerEnv {
	t.Helper()
	e := newEnv(t)
	repo := newMemRepo()
	r, err := execution.NewRunner(execution.RunnerConfig{
		Executions:     repo,
		NodeExecutions: nodeRepo{repo},
		Definitions:    loaderFunc(func(context.Context, uuid.UUID) (workflow.Definition, error) { return def, nil }),
		Validator:      workflow.NewValidator(e.reg),
		Graph:          e.exec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &runnerEnv{env: e, repo: repo, runner: r}
}

func (r *runnerEnv) create(t *testing.T, input map[string]any) uuid.UUID {
	t.Helper()
	e, err := r.runner.Service().Create(context.Background(), uuid.New(), uuid.New(), input)
	if err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func (r *runnerEnv) nodeRecords(t *testing.T, id uuid.UUID) map[string]execution.NodeExecution {
	t.Helper()
	list, _ := nodeRepo{r.repo}.ListByExecution(context.Background(), id)
	out := map[string]execution.NodeExecution{}
	for _, n := range list {
		out[n.NodeID] = n
	}
	return out
}

func chainABC() workflow.Definition {
	return graph(nodes(entry("in"), probe("A"), probe("B"), probe("C"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"),
		edge("B", "out", "C", "in"), edge("C", "out", "out", "value"))
}

func TestRunnerCompletesAndRecordsEveryNode(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	id := r.create(t, map[string]any{"q": "hello"})
	res, err := r.runner.Run(context.Background(), id)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusCompleted || e.Error != nil || e.FinishedAt == nil {
		t.Fatalf("execution = %+v", e)
	}
	out, _ := e.Output["out"].(map[string]any)
	if labelOf(t, out["result"]) != "C" || len(res.Outputs) != 1 {
		t.Fatalf("output = %#v", e.Output)
	}
	recs := r.nodeRecords(t, id)
	for _, n := range []string{"in", "A", "B", "C", "out"} {
		rec, ok := recs[n]
		if !ok || rec.Status != execution.NodeStatusCompleted || rec.StartedAt == nil || rec.FinishedAt == nil || rec.Input == nil || rec.Output == nil || rec.ExecutionID != id {
			t.Fatalf("node %s record = %+v", n, rec)
		}
	}
	if recs["A"].NodeType != probeType {
		t.Fatalf("node type = %q", recs["A"].NodeType)
	}
	// Runtime input captured for the node: ports and resolved config.
	ports, _ := recs["B"].Input["ports"].(map[string]any)
	if labelOf(t, ports["in"]) != "A" {
		t.Fatalf("B input = %#v", recs["B"].Input)
	}
	h, _ := r.repo.History(context.Background(), id)
	if len(h) != 3 || h[2].To != execution.StatusCompleted {
		t.Fatalf("history = %+v", h)
	}
}

func TestRunnerFailsWithNodeErrorDetails(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	r.rec.hooks["B"] = func(context.Context) error {
		return node.NewNodeError(node.ErrCodeRateLimited, "provider throttled", true)
	}
	id := r.create(t, map[string]any{})
	_, err := r.runner.Run(context.Background(), id)
	var nodeErr *node.NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("run err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusFailed || e.Error == nil || e.Error.NodeID == nil || *e.Error.NodeID != "B" ||
		e.Error.Code != string(node.ErrCodeRateLimited) || !e.Error.Retryable || e.Output != nil {
		t.Fatalf("execution = %+v err=%+v", e, e.Error)
	}
	recs := r.nodeRecords(t, id)
	if recs["A"].Status != execution.NodeStatusCompleted || recs["B"].Status != execution.NodeStatusFailed {
		t.Fatalf("records = %+v", recs)
	}
	if recs["B"].Error == nil || *recs["B"].Error.NodeID != "B" || recs["B"].Error.Code != string(node.ErrCodeRateLimited) {
		t.Fatalf("B error = %+v", recs["B"].Error)
	}
	if _, ran := recs["C"]; ran {
		t.Fatal("fail-fast: C must have no record")
	}
	if got := r.rec.labels(); !equalStrings(got, []string{"A", "B"}) {
		t.Fatalf("executed = %v", got)
	}
}

func TestRunnerRecordsVariableResolutionFailure(t *testing.T) {
	def := graph(nodes(entry("in"), probeWith("A", map[string]any{"message": "{{input.missing}}"}), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	r := newRunnerEnv(t, def)
	id := r.create(t, map[string]any{})
	if _, err := r.runner.Run(context.Background(), id); !errors.Is(err, execution.ErrUnresolvedReference) {
		t.Fatalf("err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusFailed || e.Error.Code != execution.CodeVariableResolution || *e.Error.NodeID != "A" {
		t.Fatalf("execution error = %+v", e.Error)
	}
}

func TestRunnerCancellationBetweenNodesPersistsCancelled(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.rec.hooks["A"] = func(context.Context) error { cancel(); return nil }
	id := r.create(t, map[string]any{})
	_, err := r.runner.Run(ctx, id)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusCancelled || e.FinishedAt == nil || e.Error != nil {
		t.Fatalf("execution = %+v", e)
	}
	recs := r.nodeRecords(t, id)
	if recs["A"].Status != execution.NodeStatusCompleted {
		t.Fatalf("A = %+v", recs["A"])
	}
	if _, ran := recs["B"]; ran {
		t.Fatal("no work may be scheduled after cancellation")
	}
}

func TestRunnerCancellationDuringNodeIsNotANodeFailureCode(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.rec.hooks["B"] = func(nodeCtx context.Context) error { cancel(); <-nodeCtx.Done(); return nodeCtx.Err() }
	id := r.create(t, map[string]any{})
	if _, err := r.runner.Run(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusCancelled {
		t.Fatalf("status = %s", e.Status)
	}
	b := r.nodeRecords(t, id)["B"]
	if b.Status != execution.NodeStatusFailed || b.Error == nil || b.Error.Code != execution.CodeCancelled {
		t.Fatalf("B = %+v / %+v", b, b.Error)
	}
}

// With a done context the claim is never issued: the execution stays PENDING
// (the same behaviour PostgreSQL shows; see the integration tests).
func TestRunnerDoesNotClaimWithDoneContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	for name, ctx := range map[string]context.Context{"cancelled": cancelled, "deadline": expired} {
		t.Run(name, func(t *testing.T) {
			r := newRunnerEnv(t, chainABC())
			id := r.create(t, map[string]any{})
			writesBefore := len(r.repo.writes)
			_, err := r.runner.Run(ctx, id)
			if !errors.Is(err, ctx.Err()) {
				t.Fatalf("err = %v", err)
			}
			if statusOf(t, r.repo, id) != execution.StatusPending || len(r.rec.calls) != 0 || len(r.repo.writes) != writesBefore {
				t.Fatal("a done context must not claim, execute or write anything")
			}
		})
	}
}

func TestRunnerDeadlineDuringNodeIsTimeoutFailure(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r.rec.hooks["B"] = func(nodeCtx context.Context) error { <-nodeCtx.Done(); return nodeCtx.Err() }
	id := r.create(t, map[string]any{})
	if _, err := r.runner.Run(ctx, id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusFailed || e.Error.Code != execution.CodeTimeout || *e.Error.NodeID != "B" {
		t.Fatalf("execution = %+v %+v", e, e.Error)
	}
	if b := r.nodeRecords(t, id)["B"]; b.Status != execution.NodeStatusFailed || b.Error.Code != execution.CodeTimeout {
		t.Fatalf("B = %+v", b)
	}
	if _, ran := r.nodeRecords(t, id)["C"]; ran {
		t.Fatal("no node may start after the deadline")
	}
}

// F13: the caller cancelled, but the node ignored its context and returned an
// unrelated error. The execution is CANCELLED; the node keeps its own error.
func TestRunnerCallerCancellationWinsOverContextIgnoringNode(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.rec.hooks["B"] = func(context.Context) error { cancel(); return errors.New("upstream API said no") }
	id := r.create(t, map[string]any{})
	if _, err := r.runner.Run(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusCancelled {
		t.Fatalf("status = %s", e.Status)
	}
	b := r.nodeRecords(t, id)["B"]
	if b.Status != execution.NodeStatusFailed || b.Error.Code != execution.CodeNodeFailed || b.Error.Message == "" {
		t.Fatalf("B = %+v / %+v", b, b.Error)
	}
	h, _ := r.repo.History(context.Background(), id)
	if last := h[len(h)-1]; last.To != execution.StatusCancelled || last.Metadata["graph_error"] == nil {
		t.Fatalf("cancel history = %+v", last)
	}
}

// Without cancellation, the same node error is an ordinary failure.
func TestRunnerNodeErrorWithoutCancellationIsFailure(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	r.rec.hooks["B"] = func(context.Context) error { return errors.New("upstream API said no") }
	id := r.create(t, map[string]any{})
	_, _ = r.runner.Run(context.Background(), id)
	if e, _ := r.repo.Get(context.Background(), id); e.Status != execution.StatusFailed || e.Error.Code != execution.CodeNodeFailed {
		t.Fatalf("execution = %+v", e)
	}
}

// F10: finalization after caller cancellation runs on a context that is not
// cancelled but has its own bounded deadline.
func TestRunnerFinalizationUsesDetachedBoundedContext(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.rec.hooks["A"] = func(context.Context) error { cancel(); return nil }
	id := r.create(t, map[string]any{})
	_, _ = r.runner.Run(ctx, id)
	if statusOf(t, r.repo, id) != execution.StatusCancelled {
		t.Fatal("expected CANCELLED")
	}
	last := r.repo.writes[len(r.repo.writes)-1] // the CANCELLED transition
	if last.err != nil || !last.hasDeadline || last.remaining <= 0 || last.remaining > execution.DefaultWriteTimeout {
		t.Fatalf("finalization context = %+v; want live, bounded by %s", last, execution.DefaultWriteTimeout)
	}
}

// F2: another actor cancels while a node runs. The node's result write is
// rejected, the graph stops, and the Runner reports the external status.
func TestRunnerStopsWhenExecutionCancelledExternally(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	var id uuid.UUID
	r.rec.hooks["B"] = func(context.Context) error { return r.runner.Service().Cancel(context.Background(), id) }
	id = r.create(t, map[string]any{})
	_, err := r.runner.Run(context.Background(), id)
	var ext *execution.ExternallyFinalizedError
	if !errors.As(err, &ext) || ext.Status != execution.StatusCancelled || !errors.Is(err, execution.ErrExecutionCancelled) || !errors.Is(err, execution.ErrExecutionNotRunning) {
		t.Fatalf("err = %v", err)
	}
	if statusOf(t, r.repo, id) != execution.StatusCancelled {
		t.Fatal("execution must stay CANCELLED")
	}
	recs := r.nodeRecords(t, id)
	if b := recs["B"]; b.Status != execution.NodeStatusFailed || b.Error.Code != execution.CodeCancelled {
		t.Fatalf("B = %+v / %+v", b, b.Error)
	}
	if _, ran := recs["C"]; ran || len(r.rec.calls) != 2 {
		t.Fatalf("work continued after external cancellation: %v", r.rec.labels())
	}
}

// F9: the node ran but recording its completion failed. The execution fails,
// and the node is not left RUNNING nor presented as never having run.
func TestRunnerNodeFinishRecordingFailure(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	r.repo.failNodeFinish = true
	id := r.create(t, map[string]any{})
	if _, err := r.runner.Run(context.Background(), id); err == nil {
		t.Fatal("expected failure")
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusFailed || e.Error.Code != execution.CodeNodeObservation || *e.Error.NodeID != "in" {
		t.Fatalf("execution = %+v %+v", e, e.Error)
	}
	in := r.nodeRecords(t, id)["in"]
	if in.Status != execution.NodeStatusFailed || in.Error.Code != execution.CodeNodeObservation || in.StartedAt == nil ||
		!strings.Contains(in.Error.Message, "executed") {
		t.Fatalf("node 'in' = %+v / %+v", in, in.Error)
	}
	if len(r.rec.calls) != 0 {
		t.Fatal("no later node may run")
	}
}

// F14: a node failing during variable resolution has its own record.
func TestRunnerRecordsNodeFailingBeforeExecute(t *testing.T) {
	def := graph(nodes(entry("in"), probeWith("A", map[string]any{"message": "{{input.missing}}"}), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	r := newRunnerEnv(t, def)
	id := r.create(t, map[string]any{})
	_, _ = r.runner.Run(context.Background(), id)
	a, ok := r.nodeRecords(t, id)["A"]
	if !ok || a.Status != execution.NodeStatusFailed || a.Error.Code != execution.CodeVariableResolution || *a.Error.NodeID != "A" {
		t.Fatalf("A = %+v", a)
	}
	if len(r.rec.calls) != 0 {
		t.Fatal("A's Execute must not run")
	}
}

// F14: a transient definition-load failure is not INVALID_WORKFLOW.
func TestRunnerClassifiesDefinitionLoadFailures(t *testing.T) {
	cases := map[string]struct {
		err       error
		code      string
		retryable bool
	}{
		"transient": {errors.New("connection refused"), execution.CodeDefinitionLoadFailed, true},
		"corrupt":   {fmt.Errorf("%w: bad json", execution.ErrInvalidWorkflowDefinition), execution.CodeInvalidWorkflow, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			repo := newMemRepo()
			r, _ := execution.NewRunner(execution.RunnerConfig{
				Executions: repo, NodeExecutions: nodeRepo{repo}, Validator: workflow.NewValidator(e.reg), Graph: e.exec,
				Definitions: loaderFunc(func(context.Context, uuid.UUID) (workflow.Definition, error) { return workflow.Definition{}, tc.err }),
			})
			ex, _ := r.Service().Create(context.Background(), uuid.New(), uuid.New(), nil)
			_, _ = r.Run(context.Background(), ex.ID)
			got, _ := repo.Get(context.Background(), ex.ID)
			if got.Status != execution.StatusFailed || got.Error.Code != tc.code || got.Error.Retryable != tc.retryable {
				t.Fatalf("error = %+v", got.Error)
			}
		})
	}
}

func TestRunnerLostClaimDoesNothing(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	id := r.create(t, map[string]any{})
	if err := r.runner.Service().Start(context.Background(), id); err != nil { // another worker won
		t.Fatal(err)
	}
	if _, err := r.runner.Run(context.Background(), id); !errors.Is(err, execution.ErrExecutionNotClaimable) {
		t.Fatalf("err = %v", err)
	}
	if len(r.rec.calls) != 0 || len(r.nodeRecords(t, id)) != 0 {
		t.Fatal("losing worker must not execute or record anything")
	}
	if statusOf(t, r.repo, id) != execution.StatusRunning {
		t.Fatal("owner's RUNNING status must be untouched")
	}
}

func TestRunnerRejectsInvalidDefinitionAsFailed(t *testing.T) {
	invalid := graph(nodes(probe("A"))) // no entry or exit node
	r := newRunnerEnv(t, invalid)
	id := r.create(t, nil)
	if _, err := r.runner.Run(context.Background(), id); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("err = %v", err)
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusFailed || e.Error.Code != execution.CodeInvalidWorkflow || len(r.rec.calls) != 0 {
		t.Fatalf("execution = %+v %+v", e, e.Error)
	}
}

func TestRunnerNodePersistenceFailureStopsExecution(t *testing.T) {
	r := newRunnerEnv(t, chainABC())
	id := r.create(t, map[string]any{})
	r.repo.failNodeWrites = true
	if _, err := r.runner.Run(context.Background(), id); err == nil {
		t.Fatal("expected failure")
	}
	e, _ := r.repo.Get(context.Background(), id)
	if e.Status != execution.StatusFailed || e.Error.Code != execution.CodeNodeObservation || *e.Error.NodeID != "in" {
		t.Fatalf("execution = %+v %+v", e, e.Error)
	}
	if len(r.rec.calls) != 0 {
		t.Fatal("a node must not run when its record cannot be persisted")
	}
}

func TestNewRunnerRequiresCollaborators(t *testing.T) {
	if _, err := execution.NewRunner(execution.RunnerConfig{}); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("err = %v", err)
	}
}
