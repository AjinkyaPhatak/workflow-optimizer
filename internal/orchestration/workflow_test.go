package orchestration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	temporalworkflow "go.temporal.io/sdk/workflow"
	"workflow-optimizer/internal/automation"
)

func TestAverageThresholdOnlyDispatchesAbove150(t *testing.T) {
	for _, average := range []int{100, 150, 200} {
		t.Run(fmt.Sprint(average), func(t *testing.T) {
			plan := automation.Plan{Mode: "once", Steps: []automation.Step{{ID: "email_if_above", Kind: "branch", Condition: &automation.Condition{Left: lit("", fmt.Sprint(average)), Op: "gt", Right: lit("", "150")}, Children: []automation.Step{{ID: "email", Kind: "tool", Tool: "send", Inputs: []automation.Binding{lit("recipient", `"known"`)}}}}}}
			env, store, tools := environment(t, plan)
			env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			want := 0
			if average > 150 {
				want = 1
			}
			if store.run.Status != "COMPLETED" || len(tools.calls) != want {
				t.Fatalf("average %d: status=%s sends=%d, expected=%d", average, store.run.Status, len(tools.calls), want)
			}
		})
	}
}

type runStore struct {
	automation.Store
	mu     sync.Mutex
	run    automation.Run
	events []automation.Event
}

func (s *runStore) Run(context.Context, uuid.UUID) (automation.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, nil
}
func (s *runStore) UpdateRun(_ context.Context, _ uuid.UUID, status string, outputs map[string]map[string]any, q *automation.Question, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run.Status = status
	s.run.Outputs = outputs
	s.run.Question = q
	s.run.Error = message
	return nil
}
func (s *runStore) AddEvent(_ context.Context, e automation.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

type fakeTools struct {
	mu    sync.Mutex
	calls []automation.ToolCall
}

func (t *fakeTools) Execute(_ context.Context, c automation.ToolCall) (automation.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, c)
	if c.Tool == "send" && c.Inputs["recipient"] != "known" {
		return automation.ToolResult{}, &automation.NeedInput{Question: automation.Question{ID: "contact", Prompt: "Which Phatak?", Kind: "choice", Required: true, Input: "recipient", Options: []automation.Option{{Label: "Ajinkya Phatak", Value: "known"}}}}
	}
	return automation.ToolResult{Outputs: map[string]any{"result": c.Inputs["value"]}, Tokens: 2}, nil
}
func environment(t *testing.T, plan automation.Plan) (*testsuite.TestWorkflowEnvironment, *runStore, *fakeTools) {
	t.Helper()
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(10 * time.Second)
	store := &runStore{run: automation.Run{ID: uuid.New(), WorkspaceID: uuid.New(), UserID: uuid.New(), Plan: plan, Revision: 7}}
	tools := &fakeTools{}
	a := &Activities{Store: store, Tools: tools}
	env.RegisterWorkflowWithOptions(WorkflowV2, temporalworkflow.RegisterOptions{Name: WorkflowName})
	for name, fn := range map[string]any{"automation.load": a.Load, "automation.progress": a.Progress, "automation.execute": a.Execute} {
		env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
	return env, store, tools
}
func lit(name, json string) automation.Binding {
	return automation.Binding{Name: name, Source: "literal", ValueJSON: json}
}
func TestHumanWaitRejectsStaleAnswerAndContinues(t *testing.T) {
	plan := automation.Plan{Mode: "once", Steps: []automation.Step{{ID: "send", Name: "Send results", Kind: "tool", Tool: "send", Inputs: []automation.Binding{lit("recipient", `"Phatak"`)}}}}
	env, store, tools := environment(t, plan)
	qid := "0/send/contact"
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(AnswerSignal, Answer{QuestionID: qid, Revision: 6, Value: "known"}) }, time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(AnswerSignal, Answer{QuestionID: qid, Revision: 7, Value: "known"}) }, 2*time.Second)
	env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if store.run.Status != "COMPLETED" || len(tools.calls) != 2 {
		t.Fatalf("status=%s calls=%d", store.run.Status, len(tools.calls))
	}
	if tools.calls[0].OperationID != tools.calls[1].OperationID {
		t.Fatal("clarification changed the stable action ID")
	}
	waiting := false
	for _, e := range store.events {
		if e.Kind == "question" {
			waiting = true
		}
	}
	if !waiting {
		t.Fatal("human wait was not recorded")
	}
}
func TestBranchParallelAndRepeater(t *testing.T) {
	branch := automation.Step{ID: "choose", Kind: "branch", Condition: &automation.Condition{Left: lit("", `true`), Op: "equals", Right: lit("", `true`)},
		Children:  []automation.Step{{ID: "chosen", Kind: "tool", Tool: "echo", Inputs: []automation.Binding{lit("value", `"yes"`)}}},
		Otherwise: []automation.Step{{ID: "wrong", Kind: "tool", Tool: "echo"}}}
	parallel := automation.Step{ID: "together", Kind: "parallel", Children: []automation.Step{{ID: "first", Kind: "tool", Tool: "echo"}, {ID: "second", Kind: "tool", Tool: "echo"}}}
	repeat := automation.Step{ID: "each", Kind: "map", MaxIterations: 3, Inputs: []automation.Binding{lit("items", `[1,2,3]`)}, Children: []automation.Step{{ID: "item", Kind: "tool", Tool: "echo", Inputs: []automation.Binding{{Name: "value", Source: "item"}}}}}
	env, store, tools := environment(t, automation.Plan{Mode: "once", Steps: []automation.Step{branch, parallel, repeat}})
	env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 6 {
		t.Fatalf("calls=%d", len(tools.calls))
	}
	seen := map[string]bool{}
	for _, c := range tools.calls {
		if seen[c.OperationID] {
			t.Fatal("repetitions reused an action ID")
		}
		seen[c.OperationID] = true
		if c.Tool == "wrong" {
			t.Fatal("wrong branch ran")
		}
	}
}
func TestBudgetPauseAndRenewal(t *testing.T) {
	plan := automation.Plan{Mode: "once", Budget: automation.Budget{MaxActions: 1, MaxTokens: 100, MaxActiveSeconds: 100},
		Steps: []automation.Step{{ID: "one", Kind: "tool", Tool: "echo"}, {ID: "two", Kind: "tool", Tool: "echo"}}}
	env, store, tools := environment(t, plan)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(ControlSignal, Control{Action: "resume", Budget: &automation.Budget{MaxActions: 2, MaxTokens: 100, MaxActiveSeconds: 100}})
	}, time.Second)
	env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	paused := false
	for _, e := range store.events {
		if e.Kind == "budget" {
			paused = true
		}
	}
	if !paused || len(tools.calls) != 2 {
		t.Fatal("budget was not enforced")
	}
}
