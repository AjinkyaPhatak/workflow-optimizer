package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"workflow-optimizer/internal/automation"
)

type Backend struct {
	Client client.Client
	Queue  string
}

func (b *Backend) queue() string {
	if b.Queue != "" {
		return b.Queue
	}
	return TaskQueue
}

var _ automation.Backend = (*Backend)(nil)

func workflowID(id uuid.UUID) string { return "workflow-" + id.String() }
func started(err error) error {
	var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &duplicate) {
		return nil
	}
	return err
}
func (b *Backend) Start(ctx context.Context, run automation.Run, answers map[string]any, event any) error {
	_, err := b.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: workflowID(run.ID), TaskQueue: b.queue(), WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, WorkflowName, Input{RunID: run.ID, Answers: answers, Event: event})
	return started(err)
}
func (b *Backend) Answer(ctx context.Context, id uuid.UUID, q string, revision int, value any) error {
	return b.Client.SignalWorkflow(ctx, workflowID(id), "", AnswerSignal, Answer{q, value, revision})
}
func (b *Backend) Control(ctx context.Context, id uuid.UUID, action string, budget *automation.Budget) error {
	if action == "cancel" {
		return b.Client.CancelWorkflow(ctx, workflowID(id), "")
	}
	return b.Client.SignalWorkflow(ctx, workflowID(id), "", ControlSignal, Control{action, budget})
}
func (b *Backend) Event(ctx context.Context, id uuid.UUID, event any) error {
	return b.Client.SignalWorkflow(ctx, workflowID(id), "", EventSignal, event)
}

const TriggerWorkflowName = "automation.trigger"

type TriggerInput struct {
	AutomationID uuid.UUID
	RunID        uuid.UUID
	Event        any
}

func (b *Backend) Schedule(ctx context.Context, a automation.Automation) error {
	id := "automation-" + a.ID.String()
	handle := b.Client.ScheduleClient().GetHandle(ctx, id)
	if a.Cron == "" || !a.Enabled {
		_, err := handle.Describe(ctx)
		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			return nil
		}
		if err != nil {
			return err
		}
		return handle.Pause(ctx, client.SchedulePauseOptions{Note: "Paused from Workflow Optimizer"})
	}
	spec := client.ScheduleSpec{CronExpressions: []string{a.Cron}, TimeZoneName: a.Timezone}
	action := &client.ScheduleWorkflowAction{ID: "scheduled-" + a.ID.String(), Workflow: TriggerWorkflowName, TaskQueue: b.queue(), Args: []interface{}{TriggerInput{AutomationID: a.ID}}}
	_, err := b.Client.ScheduleClient().Create(ctx, client.ScheduleOptions{ID: id, Spec: spec, Action: action, Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP})
	if err == nil {
		return nil
	}
	var exists *serviceerror.AlreadyExists
	if !errors.As(err, &exists) && !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return err
	}
	err = handle.Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
		schedule := in.Description.Schedule
		schedule.Spec = &spec
		schedule.Action = action
		if schedule.State != nil {
			schedule.State.Paused = false
		}
		return &client.ScheduleUpdate{Schedule: &schedule}, nil
	}})
	return err
}
func (b *Backend) DeleteSchedule(ctx context.Context, id uuid.UUID) error {
	err := b.Client.ScheduleClient().GetHandle(ctx, "automation-"+id.String()).Delete(ctx)
	var missing *serviceerror.NotFound
	if errors.As(err, &missing) {
		return nil
	}
	return err
}
func (b *Backend) Trigger(ctx context.Context, a automation.Automation, eventID string, event any) error {
	id := uuid.NewSHA1(a.ID, []byte(eventID))
	_, err := b.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "trigger-" + id.String(), TaskQueue: b.queue(), WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, TriggerWorkflowName, TriggerInput{AutomationID: a.ID, RunID: id, Event: event})
	return started(err)
}
func TriggerWorkflow(ctx workflow.Context, in TriggerInput) error {
	if in.RunID == uuid.Nil {
		in.RunID = uuid.NewSHA1(in.AutomationID, []byte(workflow.GetInfo(ctx).WorkflowExecution.RunID))
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	var run automation.Run
	if err := workflow.ExecuteActivity(ctx, "automation.begin_trigger", in).Get(ctx, &run); err != nil {
		return err
	}
	child := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: workflowID(run.ID)})
	var envelope automation.EventEnvelope
	raw, _ := json.Marshal(in.Event)
	_ = json.Unmarshal(raw, &envelope)
	return workflow.ExecuteChildWorkflow(child, WorkflowName, Input{RunID: run.ID, Event: envelope.Data}).Get(ctx, nil)
}
func (a *Activities) BeginTrigger(ctx context.Context, in TriggerInput) (automation.Run, error) {
	config, err := a.Store.Automation(ctx, in.AutomationID)
	if err != nil {
		return automation.Run{}, err
	}
	if !config.Enabled {
		return automation.Run{}, errors.New("automation is paused")
	}
	if run, err := a.Store.Run(ctx, in.RunID); err == nil {
		return run, nil
	} else if !errors.Is(err, automation.ErrNotFound) {
		return automation.Run{}, err
	}
	var envelope automation.EventEnvelope
	raw, _ := json.Marshal(in.Event)
	_ = json.Unmarshal(raw, &envelope)
	trace := append(append([]uuid.UUID{}, envelope.Trace...), config.ID)
	if len(trace) > 16 {
		return automation.Run{}, errors.New("event chain reached its limit")
	}
	run := automation.Run{ID: in.RunID, WorkspaceID: config.WorkspaceID, UserID: config.UserID, Plan: config.Plan, CredentialID: config.CredentialID, Trace: trace, Revision: 1, Backend: "temporal", Status: "PENDING", Outputs: map[string]map[string]any{}, CreatedAt: time.Now().UTC()}
	return run, a.Store.CreateRun(ctx, run)
}
