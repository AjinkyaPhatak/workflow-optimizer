// Package orchestration is the Temporal execution backend for version 2 plans.
// Every external operation is an activity. Workflow code is deterministic.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"workflow-optimizer/internal/automation"
)

const TaskQueue = "workflow-optimizer-v2"
const WorkflowName = "workflow.v2"
const AnswerSignal = "answer"
const ControlSignal = "control"
const EventSignal = "event"

type Answer struct {
	QuestionID string
	Value      any
	Revision   int
}
type Control struct {
	Action string
	Budget *automation.Budget
}
type Input struct {
	RunID         uuid.UUID
	Answers       map[string]any
	Event         any
	Actions       int
	Tokens        int
	ActiveSeconds float64
	Cycles        int
	Budget        *automation.Budget
}
type Progress struct {
	RunID    uuid.UUID
	Status   string
	Outputs  map[string]map[string]any
	Question *automation.Question
	Message  string
	StepID   string
	Kind     string
}
type CallResult struct {
	Result   automation.ToolResult
	Question *automation.Question
}
type State struct {
	Budget        automation.Budget
	Status        string
	Paused        bool
	Question      *automation.Question
	Actions       int
	Tokens        int
	ActiveSeconds float64
}
type machine struct {
	ctx            workflow.Context
	input          Input
	run            automation.Run
	state          *State
	askLock        workflow.Mutex
	outputs        map[string]map[string]any
	answers        map[string]any
	failedStepID   string
	failedStepName string
	failureMessage string
}

func WorkflowV2(ctx workflow.Context, in Input) (result map[string]map[string]any, err error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute, HeartbeatTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second},
	})
	m := &machine{ctx: ctx, input: in, outputs: map[string]map[string]any{}, answers: in.Answers, state: &State{Status: "RUNNING", Actions: in.Actions, Tokens: in.Tokens, ActiveSeconds: in.ActiveSeconds}, askLock: workflow.NewMutex(ctx)}
	if m.answers == nil {
		m.answers = map[string]any{}
	}
	// Install terminal projection before loading so cancelling a pending run
	// cannot leave its UI projection permanently PENDING.
	defer func() {
		if workflow.IsContinueAsNewError(err) {
			return
		}
		m.outputs["_usage"] = map[string]any{"actions": m.state.Actions, "tokens": m.state.Tokens, "active_seconds": m.state.ActiveSeconds}
		status, message := "COMPLETED", ""
		if err != nil {
			status = "FAILED"
			message = "A step could not finish. Check the execution details."
			if m.failedStepID != "" {
				message = "“" + m.failedStepName + "” could not finish. " + m.failureMessage
			}
		}
		if temporal.IsCanceledError(err) {
			status = "CANCELLED"
			message = ""
		}
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		_ = workflow.ExecuteActivity(dctx, "automation.progress", Progress{RunID: in.RunID, Status: status, Outputs: m.outputs, Message: message, StepID: m.failedStepID, Kind: status}).Get(dctx, nil)
	}()
	if err := workflow.ExecuteActivity(ctx, "automation.load", in.RunID).Get(ctx, &m.run); err != nil {
		return nil, err
	}
	if in.Budget != nil {
		m.run.Plan.Budget = *in.Budget
	}
	m.state.Budget = m.run.Plan.Budget
	_ = workflow.SetQueryHandler(ctx, "state", func() (State, error) { return *m.state, nil })
	workflow.Go(ctx, func(ctx workflow.Context) {
		ch := workflow.GetSignalChannel(ctx, ControlSignal)
		report := func(status, kind string) {
			current := *m
			current.ctx = ctx
			_ = current.progress(status, "", "", kind)
		}
		for {
			var c Control
			if err := receiveSignal(ctx, ch, &c); err != nil {
				return
			}
			switch c.Action {
			case "pause":
				m.state.Paused = true
				report("PAUSED", "paused")
			case "resume":
				m.state.Paused = false
				if c.Budget != nil && c.Budget.Valid() {
					m.state.Budget = *c.Budget
					m.state.Actions = 0
					m.state.Tokens = 0
					m.state.ActiveSeconds = 0
				}
				status := "RUNNING"
				if m.state.Question != nil {
					status = "WAITING_FOR_INPUT"
				}
				report(status, "resumed")
			}
		}
	})
	if err = m.progress("RUNNING", "", "", "started"); err != nil {
		return nil, err
	}
	for {
		if err = m.steps(m.run.Plan.Steps, "", nil); err != nil {
			return nil, err
		}
		if m.run.Plan.Mode != "continuous" {
			return m.outputs, nil
		}
		// No busy loop: continuous work yields for a minute between bounded cycles.
		if err = workflow.Sleep(ctx, time.Minute); err != nil {
			return nil, err
		}
		in.Cycles++
		in.Actions = m.state.Actions
		in.Tokens = m.state.Tokens
		in.ActiveSeconds = m.state.ActiveSeconds
		in.Answers = m.answers
		in.Budget = &m.state.Budget
		if in.Cycles%20 == 0 || workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			return nil, workflow.NewContinueAsNewError(ctx, WorkflowName, in)
		}
		m.input.Cycles = in.Cycles
		m.outputs = map[string]map[string]any{}
	}
}
func (m *machine) progress(status, step, message, kind string) error {
	m.state.Status = status
	return workflow.ExecuteActivity(m.ctx, "automation.progress", Progress{RunID: m.input.RunID, Status: status, Outputs: m.outputs, Question: m.state.Question, Message: message, StepID: step, Kind: kind}).Get(m.ctx, nil)
}
func (m *machine) checkpoint() error {
	if m.state.Paused {
		if err := m.progress("PAUSED", "", "", "paused"); err != nil {
			return err
		}
		if err := workflow.Await(m.ctx, func() bool { return !m.state.Paused }); err != nil {
			return err
		}
	}
	b := m.state.Budget
	if !b.Automatic && b.Valid() && (m.state.Actions >= b.MaxActions || m.state.Tokens >= b.MaxTokens || m.state.ActiveSeconds >= float64(b.MaxActiveSeconds)) {
		m.state.Paused = true
		if err := m.progress("PAUSED", "", "The work reached its budget. Set a new budget to continue.", "budget"); err != nil {
			return err
		}
		if err := workflow.Await(m.ctx, func() bool { return !m.state.Paused }); err != nil {
			return err
		}
		// A resume without a renewed budget cannot bypass the limit.
		return m.checkpoint()
	}
	return nil
}
func (m *machine) ask(q automation.Question) (any, error) {
	if err := m.askLock.Lock(m.ctx); err != nil {
		return nil, err
	}
	defer m.askLock.Unlock()
	m.state.Question = &q
	if err := m.progress("WAITING_FOR_INPUT", q.StepID, q.Prompt, "question"); err != nil {
		return nil, err
	}
	ch := workflow.GetSignalChannel(m.ctx, AnswerSignal)
	for {
		var a Answer
		if err := receiveSignal(m.ctx, ch, &a); err != nil {
			return nil, err
		}
		if a.QuestionID != q.ID || a.Revision != m.run.Revision {
			continue
		}
		if automation.ValidateAnswer(q, a.Value) != nil {
			continue
		}
		if err := m.checkpoint(); err != nil {
			return nil, err
		}
		m.state.Question = nil
		m.answers[q.ID] = a.Value
		if err := m.progress("RUNNING", q.StepID, "", "answered"); err != nil {
			return nil, err
		}
		return a.Value, nil
	}
}

// A signal channel's Receive does not unblock when its context is cancelled.
// Select on cancellation as well so durable event/human waits can be stopped.
// This adds no commands to history and preserves existing signal payloads.
func receiveSignal(ctx workflow.Context, ch workflow.ReceiveChannel, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	selector := workflow.NewSelector(ctx)
	selector.AddReceive(ch, func(ready workflow.ReceiveChannel, _ bool) { ready.Receive(ctx, value) })
	selector.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {})
	selector.Select(ctx)
	return ctx.Err()
}
func (m *machine) condition(c *automation.Condition, item any) (bool, error) {
	if c == nil {
		return false, errors.New("missing condition")
	}
	left, err := automation.Resolve(c.Left, m.outputs, m.answers, item, m.input.Event)
	if err != nil {
		return false, err
	}
	right, err := automation.Resolve(c.Right, m.outputs, m.answers, item, m.input.Event)
	if err != nil {
		return false, err
	}
	if isArtifact(left) || isArtifact(right) {
		var matched bool
		err = workflow.ExecuteActivity(m.ctx, "automation.compare", CompareInput{RunID: m.input.RunID, Op: c.Op, Left: left, Right: right}).Get(m.ctx, &matched)
		return matched, err
	}
	return automation.CompareValues(c.Op, left, right)
}
func isArtifact(value any) bool { v, ok := value.(map[string]any); return ok && v["$artifact"] != nil }

type CompareInput struct {
	RunID       uuid.UUID
	Op          string
	Left, Right any
}

func (a *Activities) Compare(ctx context.Context, in CompareInput) (bool, error) {
	run, err := a.Store.Run(ctx, in.RunID)
	if err != nil {
		return false, err
	}
	resolver, ok := a.Tools.(automation.ValueResolver)
	if !ok {
		return false, errors.New("artifact comparison is not configured")
	}
	call := automation.ToolCall{RunID: run.ID, WorkspaceID: run.WorkspaceID, UserID: run.UserID}
	left, err := resolver.Hydrate(ctx, call, in.Left)
	if err != nil {
		return false, err
	}
	right, err := resolver.Hydrate(ctx, call, in.Right)
	if err != nil {
		return false, err
	}
	return automation.CompareValues(in.Op, left, right)
}
func (m *machine) steps(steps []automation.Step, path string, item any) error {
	for _, s := range steps {
		if err := m.checkpoint(); err != nil {
			return err
		}
		inputs := map[string]any{}
		for _, b := range s.Inputs {
			v, err := automation.Resolve(b, m.outputs, m.answers, item, m.input.Event)
			if err != nil {
				return err
			}
			inputs[b.Name] = v
		}
		op := fmt.Sprintf("%d/%s%s", m.input.Cycles, path, s.ID)
		start := workflow.Now(m.ctx)
		switch s.Kind {
		case "tool":
			for {
				if err := m.checkpoint(); err != nil {
					return err
				}
				m.state.Actions++
				opts := workflow.GetActivityOptions(m.ctx)
				// Side-effect deduplication is implemented by the activity's durable ledger.
				var out CallResult
				actx := workflow.WithActivityOptions(m.ctx, opts)
				call := automation.ToolCall{WorkspaceID: m.run.WorkspaceID, UserID: m.run.UserID, RunID: m.input.RunID, OperationID: op, Tool: s.Tool, Inputs: inputs}
				if err := workflow.ExecuteActivity(actx, "automation.execute", call).Get(m.ctx, &out); err != nil {
					m.failedStepID, m.failedStepName = s.ID, s.Name
					if m.failedStepName == "" {
						m.failedStepName = s.Tool
					}
					m.failureMessage = "Check its connection and inputs."
					var failure *temporal.ApplicationError
					if errors.As(err, &failure) && failure.Type() == "tool_failed" {
						m.failureMessage = failure.Message()
					}
					return err
				}
				if out.Question != nil {
					q := *out.Question
					q.ID = op + "/" + q.ID
					q.StepID = s.ID
					q.StepName = s.Name
					if q.Context == "" {
						q.Context = "This question is for “" + s.Name + "”."
					}
					value, err := m.ask(q)
					if err != nil {
						return err
					}
					inputs[q.Input] = value
					start = workflow.Now(m.ctx)
					continue
				}
				m.outputs[s.ID] = out.Result.Outputs
				m.state.Tokens += out.Result.Tokens
				break
			}
		case "ask":
			if s.Question == nil {
				return errors.New("missing question")
			}
			q := *s.Question
			q.ID = op + "/" + q.ID
			q.StepID = s.ID
			q.StepName = s.Name
			if q.Context == "" {
				q.Context = "This question is for “" + s.Name + "”."
			}
			answer, err := m.ask(q)
			if err != nil {
				return err
			}
			m.outputs[s.ID] = map[string]any{"result": answer}
			start = workflow.Now(m.ctx) // Human waiting time never consumes active time.
		case "branch":
			yes, err := m.condition(s.Condition, item)
			if err != nil {
				return err
			}
			children := s.Otherwise
			if yes {
				children = s.Children
			}
			if err := m.steps(children, op+"/", item); err != nil {
				return err
			}
			selected := map[string]any{}
			for _, child := range children {
				selected[child.ID] = m.outputs[child.ID]
			}
			m.outputs[s.ID] = map[string]any{"result": map[string]any{"matched": yes, "outputs": selected}}
		case "parallel":
			// Each branch owns a private output map; joins happen in declared order.
			futures := make([]workflow.Future, len(s.Children))
			branches := make([]*machine, len(s.Children))
			for i, child := range s.Children {
				i, child := i, child
				future, set := workflow.NewFuture(m.ctx)
				futures[i] = future
				branch := *m
				branch.outputs = copyOutputs(m.outputs)
				branch.answers = copyAnswers(m.answers)
				branches[i] = &branch
				workflow.Go(m.ctx, func(ctx workflow.Context) {
					branch.ctx = ctx
					set.Set(nil, branch.steps([]automation.Step{child}, op+"/", item))
				})
			}
			joined := map[string]any{}
			for i, f := range futures {
				if err := f.Get(m.ctx, nil); err != nil {
					return err
				}
				branch := branches[i]
				child := s.Children[i]
				joined[child.ID] = branch.outputs[child.ID]
			}
			m.outputs[s.ID] = map[string]any{"result": joined}
		case "map", "repeat":
			values := []any{item}
			if s.Kind == "map" {
				var ok bool
				values, ok = inputs["items"].([]any)
				if !ok {
					return errors.New("repeat needs a list")
				}
				if len(values) > s.MaxIterations {
					return errors.New("list exceeds repeat limit")
				}
			}
			results := []any{}
			for i := 0; i < s.MaxIterations; i++ {
				if s.Kind == "map" && i >= len(values) {
					break
				}
				current := item
				if s.Kind == "map" {
					current = values[i]
				}
				if err := m.steps(s.Children, fmt.Sprintf("%s/%d/", op, i), current); err != nil {
					return err
				}
				row := map[string]any{}
				for _, child := range s.Children {
					row[child.ID] = m.outputs[child.ID]
				}
				results = append(results, row)
				if s.Kind == "repeat" {
					done, err := m.condition(s.Condition, current)
					if err != nil {
						return err
					}
					if done {
						break
					}
					if i == s.MaxIterations-1 {
						return errors.New("repeat reached its limit before its stop condition")
					}
				}
			}
			m.outputs[s.ID] = map[string]any{"result": results}
		case "wait":
			if s.DurationSeconds > 0 {
				if err := workflow.Sleep(m.ctx, time.Duration(s.DurationSeconds)*time.Second); err != nil {
					return err
				}
				m.outputs[s.ID] = map[string]any{"result": true}
			} else {
				if err := m.progress("WAITING_FOR_EVENT", s.ID, "Waiting for an event.", "waiting"); err != nil {
					return err
				}
				var event any
				if err := receiveSignal(m.ctx, workflow.GetSignalChannel(m.ctx, EventSignal), &event); err != nil {
					return err
				}
				m.input.Event = event
				m.outputs[s.ID] = map[string]any{"result": event}
			}
			start = workflow.Now(m.ctx)
		case "workflow":
			var child Input
			if err := workflow.ExecuteActivity(m.ctx, "automation.child", ChildRequest{Parent: m.input.RunID, Reference: s.WorkflowRef, OperationID: op}).Get(m.ctx, &child); err != nil {
				return err
			}
			child.Event = inputs
			if m.state.Budget.Automatic {
				budget := m.state.Budget
				child.Budget = &budget
			} else if m.state.Budget.Valid() {
				remaining := m.state.Budget
				remaining.MaxActions -= m.state.Actions
				remaining.MaxTokens -= m.state.Tokens
				remaining.MaxActiveSeconds -= int(m.state.ActiveSeconds)
				child.Budget = &remaining
			}
			m.outputs[s.ID] = map[string]any{"child_run_id": child.RunID.String()}
			if err := m.progress("RUNNING", s.ID, "Waiting for the linked workflow.", "child_started"); err != nil {
				return err
			}
			cctx := workflow.WithChildOptions(m.ctx, workflow.ChildWorkflowOptions{WorkflowID: "workflow-" + child.RunID.String()})
			var outputs map[string]map[string]any
			if err := workflow.ExecuteChildWorkflow(cctx, WorkflowName, child).Get(m.ctx, &outputs); err != nil {
				return err
			}
			if usage := outputs["_usage"]; usage != nil {
				number := func(key string) float64 { value, _ := usage[key].(float64); return value }
				m.state.Actions += int(number("actions"))
				m.state.Tokens += int(number("tokens"))
				m.state.ActiveSeconds += number("active_seconds")
			}
			m.outputs[s.ID] = map[string]any{"result": outputs, "child_run_id": child.RunID.String()}
		default:
			return fmt.Errorf("unsupported step %s", s.Kind)
		}
		if s.Kind == "tool" {
			m.state.ActiveSeconds += workflow.Now(m.ctx).Sub(start).Seconds()
		}
		if err := m.progress("RUNNING", s.ID, s.Name, "step_completed"); err != nil {
			return err
		}
	}
	return nil
}
func copyOutputs(in map[string]map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func copyAnswers(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

type ChildRequest struct {
	Parent      uuid.UUID
	Reference   string
	OperationID string
}
type Activities struct {
	Store  automation.Store
	Tools  automation.ToolExecutor
	Events *automation.EventBus
}

func (a *Activities) Load(ctx context.Context, id uuid.UUID) (automation.Run, error) {
	return a.Store.Run(ctx, id)
}
func (a *Activities) Progress(ctx context.Context, p Progress) error {
	if err := a.Store.UpdateRun(ctx, p.RunID, p.Status, p.Outputs, p.Question, p.Message); err != nil {
		return err
	}
	if err := a.Store.AddEvent(ctx, automation.Event{RunID: p.RunID, Kind: p.Kind, StepID: p.StepID, Message: p.Message}); err != nil {
		return err
	}
	if p.Status == "COMPLETED" && a.Events != nil {
		run, err := a.Store.Run(ctx, p.RunID)
		if err != nil {
			return err
		}
		source := run.ID.String()
		if run.SessionID != nil {
			source = run.SessionID.String()
		}
		return a.Events.Emit(ctx, automation.SourceEvent{ID: "completed:" + run.ID.String(), Kind: "workflow.completed", WorkspaceID: run.WorkspaceID, Source: source, Data: map[string]any{"run_id": run.ID, "outputs": p.Outputs}, Trace: run.Trace})
	}
	return nil
}
func (a *Activities) Execute(ctx context.Context, c automation.ToolCall) (CallResult, error) {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	result, err := a.Tools.Execute(ctx, c)
	var need *automation.NeedInput
	if errors.As(err, &need) {
		return CallResult{Question: &need.Question}, nil
	}
	if err != nil {
		message := "Check its connection and inputs."
		var public interface{ UserMessage() string }
		if errors.As(err, &public) {
			message = public.UserMessage()
		}
		return CallResult{}, temporal.NewNonRetryableApplicationError(message, "tool_failed", nil)
	}
	return CallResult{Result: result}, nil
}
func (a *Activities) Child(ctx context.Context, req ChildRequest) (Input, error) {
	parent, err := a.Store.Run(ctx, req.Parent)
	if err != nil {
		return Input{}, err
	}
	ref, err := uuid.Parse(strings.TrimPrefix(req.Reference, "version:"))
	if err != nil {
		return Input{}, err
	}
	var source automation.Run
	if strings.HasPrefix(req.Reference, "version:") {
		versions, ok := a.Store.(automation.WorkflowStore)
		if !ok {
			return Input{}, automation.ErrUnavailable
		}
		version, e := versions.Version(ctx, ref)
		if e != nil {
			return Input{}, e
		}
		source = automation.Run{ID: version.ID, WorkspaceID: version.WorkspaceID, Plan: version.Plan, CredentialID: version.CredentialID}
	} else {
		source, err = a.Store.Run(ctx, ref)
		if err != nil {
			return Input{}, err
		}
	}
	if source.WorkspaceID != parent.WorkspaceID {
		return Input{}, automation.ErrNotFound
	}
	if source.ID == parent.ID || len(parent.Ancestors) > 30 {
		return Input{}, errors.New("recursive workflow calls need an explicit repeat step")
	}
	for _, id := range parent.Ancestors {
		if id == source.ID {
			return Input{}, errors.New("recursive workflow calls need an explicit repeat step")
		}
	}
	id := uuid.NewSHA1(parent.ID, []byte(req.OperationID))
	if _, err := a.Store.Run(ctx, id); errors.Is(err, automation.ErrNotFound) {
		ancestors := append(append([]uuid.UUID{}, parent.Ancestors...), parent.ID, source.ID)
		err = a.Store.CreateRun(ctx, automation.Run{ID: id, WorkspaceID: parent.WorkspaceID, UserID: parent.UserID, Plan: source.Plan, CredentialID: source.CredentialID, Trace: parent.Trace, Ancestors: ancestors, Revision: 1, Backend: "temporal", Status: "PENDING", CreatedAt: time.Now().UTC(), Outputs: map[string]map[string]any{}})
		if err != nil {
			return Input{}, err
		}
	} else if err != nil {
		return Input{}, err
	}
	return Input{RunID: id}, nil
}
