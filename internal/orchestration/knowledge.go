package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/knowledge"
)

type IndexInput struct {
	WorkspaceID uuid.UUID
	DocumentID  uuid.UUID
	Generation  int
}
type Indexer struct {
	Client     client.Client
	Repository knowledge.Repository
	Queue      string
}

func (i *Indexer) SubmitIndex(ctx context.Context, ws, doc uuid.UUID) error {
	d, _, err := i.Repository.GetDocument(ctx, ws, doc)
	if err != nil {
		return err
	}
	queue := i.Queue
	if queue == "" {
		queue = TaskQueue
	}
	_, err = i.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: fmt.Sprintf("knowledge-%s-%d", doc, d.Generation), TaskQueue: queue}, "knowledge.index", IndexInput{ws, doc, d.Generation})
	return started(err)
}
func IndexWorkflow(ctx workflow.Context, in IndexInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Minute, HeartbeatTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 5, InitialInterval: 5 * time.Second}})
	return workflow.ExecuteActivity(ctx, "knowledge.index_document", in).Get(ctx, nil)
}

type KnowledgeActivities struct {
	Service *knowledge.Service
	Events  *automation.EventBus
}

func (a *KnowledgeActivities) Index(ctx context.Context, in IndexInput) error {
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
	d, text, err := a.Service.Repository().GetDocument(ctx, in.WorkspaceID, in.DocumentID)
	if err != nil {
		return err
	}
	if d.Generation != in.Generation || d.Status == knowledge.StatusReady {
		return nil
	}
	base, err := a.Service.Repository().GetBase(ctx, in.WorkspaceID, d.KnowledgeBaseID)
	if err != nil {
		return err
	}
	_, err = a.Service.Index(ctx, base, d, text)
	if err == nil && a.Events != nil {
		return a.Events.Emit(ctx, automation.SourceEvent{ID: fmt.Sprintf("indexed:%s:%d", d.ID, d.Generation), Kind: "knowledge.indexed", WorkspaceID: base.WorkspaceID, Source: base.ID.String(), Data: map[string]any{"document_id": d.ID, "base_id": base.ID}})
	}
	return err
}
