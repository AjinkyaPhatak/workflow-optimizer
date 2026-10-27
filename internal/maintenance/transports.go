package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	commonpb "go.temporal.io/api/common/v1"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

// ValidateLocalURL prevents this development command from targeting a remote
// database or Redis service. Credentials are deliberately absent from errors.
func ValidateLocalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return errors.New("a loopback service URL is required")
	}
	if !loopback(u.Hostname()) {
		return errors.New("reset is restricted to loopback development services")
	}
	return nil
}
func ValidateLocalAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !loopback(host) {
		return errors.New("Temporal reset is restricted to a loopback development address")
	}
	return nil
}
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CleanRedis only removes records whose execution ID existed in the reset
// snapshot. OAuth state, lockout keys, foreign stack queues and malformed data
// are retained. No DEL, FLUSHDB or wildcard key scans are used.
func CleanRedis(ctx context.Context, r *redis.Client, queue, deadLetters string, ids []string) (int64, error) {
	owned := map[string]bool{}
	for _, id := range ids {
		owned[id] = true
	}
	var removed int64
	for _, key := range []string{queue, deadLetters} {
		values, err := r.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			return removed, err
		}
		for _, payload := range values {
			if !OwnsRedisPayload(payload, owned) {
				continue
			}
			n, err := r.LRem(ctx, key, 0, payload).Result()
			if err != nil {
				return removed, err
			}
			removed += n
		}
	}
	return removed, nil
}
func OwnsRedisPayload(payload string, owned map[string]bool) bool {
	var job struct {
		ExecutionID string `json:"execution_id"`
	}
	return json.Unmarshal([]byte(payload), &job) == nil && owned[job.ExecutionID]
}

type TemporalExecution struct {
	ID     string
	RunID  string
	Status enums.WorkflowExecutionStatus
}
type TemporalInventory struct {
	Schedules  []string
	Executions []TemporalExecution
}

var queueName = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,200}$`)

func OwnedWorkflow(id, kind, queue, wantedQueue string) bool {
	if queue != wantedQueue {
		return false
	}
	prefix := ""
	switch kind {
	case "workflow.v2":
		prefix = "workflow-"
	case "automation.trigger":
		if strings.HasPrefix(id, "scheduled-") {
			prefix = "scheduled-"
		} else {
			prefix = "trigger-"
		}
	default:
		return false
	}
	rest, ok := strings.CutPrefix(id, prefix)
	if !ok || len(rest) < 36 {
		return false
	}
	if _, err := uuid.Parse(rest[:36]); err != nil {
		return false
	}
	return len(rest) == 36 || (kind == "automation.trigger" && rest[36] == '-')
}
func InspectTemporal(ctx context.Context, c client.Client, namespace, queue string) (TemporalInventory, error) {
	out := TemporalInventory{}
	if !queueName.MatchString(queue) {
		return out, errors.New("unsupported task queue name")
	}
	iter, err := c.ScheduleClient().List(ctx, client.ScheduleListOptions{PageSize: 100})
	if err != nil {
		return out, err
	}
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			return out, err
		}
		id, ok := strings.CutPrefix(entry.ID, "automation-")
		if !ok {
			continue
		}
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		desc, err := c.ScheduleClient().GetHandle(ctx, entry.ID).Describe(ctx)
		if isMissing(err) {
			continue
		}
		if err != nil {
			return out, err
		}
		action, ok := desc.Schedule.Action.(*client.ScheduleWorkflowAction)
		if ok && action.TaskQueue == queue && action.Workflow == "automation.trigger" {
			out.Schedules = append(out.Schedules, entry.ID)
		}
	}
	req := &workflowservice.ListWorkflowExecutionsRequest{Namespace: namespace, PageSize: 100, Query: "TaskQueue = '" + queue + "'"}
	for {
		page, err := c.ListWorkflow(ctx, req)
		if err != nil {
			return out, err
		}
		for _, info := range page.Executions {
			if OwnedWorkflow(info.GetExecution().GetWorkflowId(), info.GetType().GetName(), info.GetTaskQueue(), queue) {
				out.Executions = append(out.Executions, TemporalExecution{ID: info.Execution.WorkflowId, RunID: info.Execution.RunId, Status: info.Status})
			}
		}
		if len(page.NextPageToken) == 0 {
			break
		}
		req.NextPageToken = page.NextPageToken
	}
	return out, nil
}
func isMissing(err error) bool { var missing *serviceerror.NotFound; return errors.As(err, &missing) }

// CleanTemporal is administrative teardown after all application workers have
// stopped. It requests cancellation, then terminates still-open executions so
// work cannot resume when the app restarts. Only exact inventory IDs are touched.
// Knowledge indexing uses another workflow type and is never inventoried.
func CleanTemporal(ctx context.Context, c client.Client, namespace, queue string, inventory TemporalInventory) error {
	for _, id := range inventory.Schedules {
		if err := c.ScheduleClient().GetHandle(ctx, id).Delete(ctx); err != nil && !isMissing(err) {
			return err
		}
	}
	// Rescan after deleting schedules to include triggers launched just before
	// deletion. Worker processes must already be stopped, so no new children run.
	latest, err := InspectTemporal(ctx, c, namespace, queue)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, execution := range append(inventory.Executions, latest.Executions...) {
		key := execution.ID + ":" + execution.RunID
		if seen[key] {
			continue
		}
		seen[key] = true
		desc, err := c.DescribeWorkflowExecution(ctx, execution.ID, execution.RunID)
		if isMissing(err) {
			continue
		}
		if err != nil {
			return err
		}
		if desc.WorkflowExecutionInfo.Status == enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
			if err := c.CancelWorkflow(ctx, execution.ID, execution.RunID); err != nil && !isMissing(err) {
				return err
			}
			if err := c.TerminateWorkflow(ctx, execution.ID, execution.RunID, "Explicit local workflow reset"); err != nil && !isMissing(err) {
				return err
			}
			// Describe is strongly consistent; do not delete until closed.
			desc, err = c.DescribeWorkflowExecution(ctx, execution.ID, execution.RunID)
			if isMissing(err) {
				continue
			}
			if err != nil {
				return err
			}
			if desc.WorkflowExecutionInfo.Status == enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
				return fmt.Errorf("workflow %s is still running", execution.ID)
			}
		}
		_, err = c.WorkflowService().DeleteWorkflowExecution(ctx, &workflowservice.DeleteWorkflowExecutionRequest{Namespace: namespace, WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: execution.ID, RunId: execution.RunID}})
		if err != nil && !isMissing(err) {
			return err
		}
		// Temporal deletion is asynchronous even when the RPC succeeds. Wait
		// for the exact run to disappear before declaring reset complete.
		deadline := time.NewTimer(45 * time.Second)
		deleted := false
		for !deleted {
			_, describeErr := c.DescribeWorkflowExecution(ctx, execution.ID, execution.RunID)
			if isMissing(describeErr) {
				deleted = true
				break
			}
			if describeErr != nil {
				deadline.Stop()
				return describeErr
			}
			select {
			case <-ctx.Done():
				deadline.Stop()
				return ctx.Err()
			case <-deadline.C:
				return fmt.Errorf("Temporal is still deleting workflow %s; retry the reset after cleanup finishes", execution.ID)
			case <-time.After(100 * time.Millisecond):
			}
		}
		deadline.Stop()
	}
	return nil
}

// WaitOffline bounds connection cleanup after graceful process shutdown.
func WaitOffline(ctx context.Context, check func(context.Context) error) error {
	var last error
	for attempt := 0; attempt < 10; attempt++ {
		if last = check(ctx); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return last
}

// IncludeKnownTemporal supplements eventually-consistent visibility with strong
// lookups of IDs recorded in PostgreSQL. A queue mismatch is a blocker, not a
// reason to leave a known scheduler alive while deleting its database record.
func IncludeKnownTemporal(ctx context.Context, c client.Client, queue string, out *TemporalInventory, scheduleIDs, runIDs []string) error {
	schedules := map[string]bool{}
	for _, id := range out.Schedules {
		schedules[id] = true
	}
	for _, id := range scheduleIDs {
		name := "automation-" + id
		if schedules[name] {
			continue
		}
		desc, err := c.ScheduleClient().GetHandle(ctx, name).Describe(ctx)
		if isMissing(err) {
			continue
		}
		if err != nil {
			return err
		}
		action, ok := desc.Schedule.Action.(*client.ScheduleWorkflowAction)
		if !ok || action.TaskQueue != queue || action.Workflow != "automation.trigger" {
			return errors.New("a known automation schedule belongs to another queue; use the stack's original task queue")
		}
		out.Schedules = append(out.Schedules, name)
		schedules[name] = true
	}
	executions := map[string]bool{}
	for _, e := range out.Executions {
		executions[e.ID+":"+e.RunID] = true
	}
	for _, id := range runIDs {
		name := "workflow-" + id
		desc, err := c.DescribeWorkflowExecution(ctx, name, "")
		if isMissing(err) {
			continue
		}
		if err != nil {
			return err
		}
		info := desc.WorkflowExecutionInfo
		if !OwnedWorkflow(name, info.GetType().GetName(), info.GetTaskQueue(), queue) {
			return errors.New("a known workflow belongs to another queue; use the stack's original task queue")
		}
		key := name + ":" + info.Execution.RunId
		if !executions[key] {
			out.Executions = append(out.Executions, TemporalExecution{ID: name, RunID: info.Execution.RunId, Status: info.Status})
			executions[key] = true
		}
	}
	return nil
}
