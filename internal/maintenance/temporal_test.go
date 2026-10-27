package maintenance

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	enums "go.temporal.io/api/enums/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

func TestTemporalResetClosesOnlyOwnedExecutions(t *testing.T) {
	address := os.Getenv("TEST_TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("TEST_TEMPORAL_ADDRESS is required")
	}
	if err := ValidateLocalAddress(address); err != nil {
		t.Fatal(err)
	}
	namespace := os.Getenv("TEMPORAL_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	queue := "maintenance-test-" + uuid.NewString()
	ownID, knowledgeID, otherID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, entry := range []struct{ id, kind, queue string }{{ownID, "workflow.v2", queue}, {knowledgeID, "knowledge.index", queue}, {otherID, "workflow.v2", queue + "-other"}} {
		name := "workflow-" + entry.id
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: entry.queue, WorkflowExecutionTimeout: 2 * time.Minute}, entry.kind)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = c.TerminateWorkflow(cleanup, name, run.GetRunID(), "maintenance test cleanup")
			_, _ = c.WorkflowService().DeleteWorkflowExecution(cleanup, &workflowservice.DeleteWorkflowExecutionRequest{Namespace: namespace, WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: name, RunId: run.GetRunID()}})
		})
	}
	automationID := uuid.NewString()
	scheduleName := "automation-" + automationID
	_, err = c.ScheduleClient().Create(ctx, client.ScheduleOptions{ID: scheduleName, Paused: true, Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Hour}}}, Action: &client.ScheduleWorkflowAction{ID: "scheduled-" + automationID, Workflow: "automation.trigger", TaskQueue: queue}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.ScheduleClient().GetHandle(context.Background(), scheduleName).Delete(context.Background())
	})
	inventory, err := InspectTemporal(ctx, c, namespace, queue)
	if err != nil {
		t.Fatal(err)
	}
	if err = IncludeKnownTemporal(ctx, c, queue, &inventory, []string{automationID}, []string{ownID}); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Executions) != 1 || len(inventory.Schedules) != 1 {
		t.Fatalf("inventory: %#v", inventory)
	}
	if err = CleanTemporal(ctx, c, namespace, queue, inventory); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DescribeWorkflowExecution(ctx, "workflow-"+ownID, ""); !isMissing(err) {
		t.Fatalf("owned execution not deleted: %v", err)
	}
	if _, err = c.ScheduleClient().GetHandle(ctx, scheduleName).Describe(ctx); !isMissing(err) {
		t.Fatalf("schedule not deleted: %v", err)
	}
	for _, id := range []string{knowledgeID, otherID} {
		desc, err := c.DescribeWorkflowExecution(ctx, "workflow-"+id, "")
		if err != nil {
			t.Fatal(err)
		}
		if desc.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
			t.Fatal("unrelated execution was stopped")
		}
	}
}
