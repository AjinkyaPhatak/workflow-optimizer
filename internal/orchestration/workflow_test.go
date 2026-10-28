package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
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

func TestNiftyAlertSkipsEmptyAndNonQualifyingGainers(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hasRows       bool
		maximumGainer float64
		wantSends     int
	}{
		{"no data", false, 0, 0},
		{"losses only", true, -1.2, 0},
		{"below threshold", true, .6, 0},
		{"exact threshold", true, .7, 0},
		{"qualifying gainer", true, .71, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := automation.Plan{Mode: "once", Steps: []automation.Step{{ID: "has_data", Kind: "branch", Condition: &automation.Condition{Left: lit("", fmt.Sprint(tc.hasRows)), Op: "equals", Right: lit("", "true")}, Children: []automation.Step{{ID: "above_threshold", Kind: "branch", Condition: &automation.Condition{Left: lit("", fmt.Sprint(tc.maximumGainer)), Op: "greater", Right: lit("", "0.7")}, Children: []automation.Step{{ID: "email", Kind: "tool", Tool: "send", Inputs: []automation.Binding{lit("recipient", `"known"`)}}}}}}}}
			env, store, tools := environment(t, plan)
			env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			if len(tools.calls) != tc.wantSends {
				t.Fatalf("sends=%d; want %d", len(tools.calls), tc.wantSends)
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
	mu      sync.Mutex
	calls   []automation.ToolCall
	failure error
	results map[string]automation.ToolResult
}

func (t *fakeTools) Execute(_ context.Context, c automation.ToolCall) (automation.ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, c)
	if result, ok := t.results[c.Tool]; ok {
		return result, nil
	}
	if c.Tool == "fail" && t.failure != nil {
		return automation.ToolResult{}, t.failure
	}
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

func TestStopUnblocksDurableWaits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		plan   automation.Plan
		status string
	}{
		{"event", automation.Plan{Mode: "once", Steps: []automation.Step{{ID: "event", Kind: "wait"}}}, "WAITING_FOR_EVENT"},
		{"human", automation.Plan{Mode: "once", Steps: []automation.Step{{ID: "human", Kind: "ask", Question: &automation.Question{ID: "q", Kind: "text", Prompt: "Which recipient?", Required: true}}}}, "WAITING_FOR_INPUT"},
		{"budget", automation.Plan{Mode: "once", Budget: automation.Budget{MaxActions: 1, MaxTokens: 100, MaxActiveSeconds: 100}, Steps: []automation.Step{{ID: "one", Kind: "tool", Tool: "echo"}, {ID: "two", Kind: "tool", Tool: "echo"}}}, "PAUSED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, store, _ := environment(t, tc.plan)
			env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Second)
			env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
			if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
				t.Fatalf("expected cancellation, got %v", err)
			}
			if store.run.Status != "CANCELLED" {
				t.Fatalf("run remained %s after Stop", store.run.Status)
			}
			waited := false
			for _, event := range store.events {
				if event.Kind == "status" || event.Kind == "started" {
					continue
				}
				if (tc.status == "WAITING_FOR_EVENT" && event.Kind == "waiting") || (tc.status == "WAITING_FOR_INPUT" && event.Kind == "question") || (tc.status == "PAUSED" && event.Kind == "budget") {
					waited = true
				}
			}
			if !waited {
				t.Fatalf("did not exercise %s before Stop", tc.status)
			}
		})
	}
}

func TestStopWhileLoadingProjectsCancelled(t *testing.T) {
	env, store, _ := environment(t, automation.Plan{Mode: "once"})
	store.run.Status = "PENDING"
	env.RegisterActivityWithOptions(func(ctx context.Context, _ uuid.UUID) (automation.Run, error) {
		<-ctx.Done()
		return automation.Run{}, ctx.Err()
	}, activity.RegisterOptions{Name: "automation.load", DisableAlreadyRegisteredCheck: true})
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Millisecond)
	env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
	if err := env.GetWorkflowError(); !temporal.IsCanceledError(err) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if store.run.Status != "CANCELLED" {
		t.Fatalf("pending run remained %s after Stop", store.run.Status)
	}
}

func TestAutomaticExecutionDoesNotPauseForUserBudgetForms(t *testing.T) {
	plan := automation.Plan{Mode: "once", Budget: automation.Budget{Automatic: true, MaxActions: 1, MaxTokens: 1, MaxActiveSeconds: 1}, Steps: []automation.Step{{ID: "one", Kind: "tool", Tool: "echo"}, {ID: "two", Kind: "tool", Tool: "echo"}, {ID: "three", Kind: "tool", Tool: "echo"}}}
	env, store, tools := environment(t, plan)
	env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
	if err := env.GetWorkflowError(); err != nil || store.run.Status != "COMPLETED" || len(tools.calls) != 3 {
		t.Fatalf("automatic execution stopped for a budget: status=%s calls=%d err=%v", store.run.Status, len(tools.calls), err)
	}
	for _, event := range store.events {
		if event.Kind == "budget" {
			t.Fatal("automatic execution asked for a budget")
		}
	}
}

type publicToolFailure string

func (e publicToolFailure) Error() string       { return string(e) }
func (e publicToolFailure) UserMessage() string { return string(e) }

func TestToolFailureNamesStepAndPreservesCompletedResults(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		message string
	}{
		{"safe connection failure", publicToolFailure("WhatsApp is reconnecting. Check Settings."), "WhatsApp is reconnecting. Check Settings."},
		{"private provider failure", errors.New("private_provider_key=do-not-expose"), "Check its connection and inputs."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := automation.Plan{Mode: "once", Steps: []automation.Step{
				{ID: "fetch", Name: "Fetch current data", Kind: "tool", Tool: "read", Inputs: []automation.Binding{lit("value", `"checked quote"`)}},
				{ID: "notify", Name: "Send Ajinkya the quote", Kind: "tool", Tool: "fail"},
			}}
			env, store, tools := environment(t, plan)
			tools.failure = tc.err
			env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
			if env.GetWorkflowError() == nil || store.run.Status != "FAILED" {
				t.Fatal("expected failed execution")
			}
			if !strings.Contains(store.run.Error, "Send Ajinkya the quote") || !strings.Contains(store.run.Error, tc.message) || strings.Contains(store.run.Error, "private_provider_key") {
				t.Fatalf("unhelpful or unsafe failure: %q", store.run.Error)
			}
			if store.run.Outputs["fetch"]["result"] != "checked quote" {
				t.Fatal("completed result was lost")
			}
			final := store.events[len(store.events)-1]
			if final.StepID != "notify" || final.Kind != "FAILED" {
				t.Fatalf("missing failed step: %+v", final)
			}
		})
	}
}

func TestFetchedNumericTextRoutesThresholdWithoutSourceTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []any
		send bool
	}{
		{"below", []any{map[string]any{"price_usd": "80784.8"}}, false},
		{"equal", []any{map[string]any{"price_usd": "80800"}}, false},
		{"above", []any{map[string]any{"price_usd": "80894"}}, true},
		{"missing", []any{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := automation.Binding{Source: "step", Ref: "fetch.rows"}
			price := automation.Binding{Source: "step", Ref: "fetch.rows.0.price_usd"}
			plan := automation.Plan{Mode: "once", Steps: []automation.Step{
				{ID: "fetch", Kind: "tool", Tool: "web.query"},
				{ID: "available", Kind: "branch", Condition: &automation.Condition{Left: rows, Op: "not_equals", Right: lit("", "[]")}, Children: []automation.Step{
					{ID: "threshold", Kind: "branch", Condition: &automation.Condition{Left: price, Op: "greater", Right: lit("", "80800")}, Children: []automation.Step{
						{ID: "send", Kind: "tool", Tool: "send", Inputs: []automation.Binding{lit("recipient", `"known"`)}},
					}},
				}},
			}}
			env, store, tools := environment(t, plan)
			tools.results = map[string]automation.ToolResult{"web.query": {Outputs: map[string]any{"rows": tc.rows}}}
			env.ExecuteWorkflow(WorkflowName, Input{RunID: store.run.ID})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			sends := 0
			for _, call := range tools.calls {
				if call.Tool == "send" {
					sends++
				}
			}
			if (sends == 1) != tc.send || store.run.Status != "COMPLETED" {
				t.Fatalf("sends=%d status=%s", sends, store.run.Status)
			}
		})
	}
}
