package application

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/workspace"
)

type buildMembers struct{}

func (buildMembers) MemberRole(context.Context, uuid.UUID, uuid.UUID) (workspace.Role, error) {
	return workspace.RoleOwner, nil
}

type buildStore struct {
	automation.Store
	mu      sync.Mutex
	session automation.Session
	saves   int
}

func (s *buildStore) Session(context.Context, uuid.UUID) (automation.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.session)
	var copy automation.Session
	_ = json.Unmarshal(raw, &copy)
	return copy, nil
}

func (s *buildStore) SaveSession(ctx context.Context, session automation.Session, expected int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected != s.session.Revision {
		return automation.ErrConflict
	}
	s.session = session
	s.saves++
	return nil
}

type buildPlanner func(context.Context, automation.PlanRequest) (automation.Plan, error)

func (p buildPlanner) Generate(ctx context.Context, req automation.PlanRequest) (automation.Plan, error) {
	return p(ctx, req)
}

func buildFixture(planner buildPlanner) (*AssistantService, *buildStore, automation.Plan) {
	store := &buildStore{session: automation.Session{ID: uuid.New(), WorkspaceID: uuid.New(), UserID: uuid.New(), Revision: 1, Answers: map[string]any{}}}
	plan := automation.EmptyPlan("Checked draft")
	plan.Steps = []automation.Step{{ID: "step", Kind: "tool", Tool: "echo", Inputs: []automation.Binding{{Name: "value", Source: "literal", ValueJSON: `"hello"`}}}}
	service := &AssistantService{Access: NewAccess(buildMembers{}), Store: store, Tools: []automation.ToolDescriptor{{ID: "echo", Inputs: []automation.Field{{Name: "value", Type: "string", Required: true}}}}, Planner: func(context.Context, automation.Session) (automation.Planner, error) { return planner, nil }}
	return service, store, plan
}

func awaitBuild(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("build did not finish")
		return nil
	}
}

func TestCancelledBuildCannotSaveLatePlannerResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var plan automation.Plan
	service, store, draft := buildFixture(func(context.Context, automation.PlanRequest) (automation.Plan, error) {
		cancel()
		return plan, nil // A provider may return a result after cancellation.
	})
	plan = draft
	_, err := service.Message(ctx, store.session.UserID, store.session.ID, 1, "Build this")
	if err == nil || store.saves != 0 || store.session.Revision != 1 {
		t.Fatalf("cancelled build persisted: err=%v saves=%d revision=%d", err, store.saves, store.session.Revision)
	}
	progress, err := service.BuildProgress(context.Background(), store.session.UserID, store.session.ID)
	if err != nil || progress.Status != "cancelled" {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
}

func TestSupersededBuildCannotOverwriteNewerRequest(t *testing.T) {
	entered := make(chan string, 2)
	first, second := make(chan struct{}), make(chan struct{})
	var plan automation.Plan
	service, store, draft := buildFixture(func(_ context.Context, req automation.PlanRequest) (automation.Plan, error) {
		text := req.Messages[len(req.Messages)-1].Text
		entered <- text
		if text == "first" {
			<-first
		} else {
			<-second
		}
		return plan, nil
	})
	plan = draft
	user, id := store.session.UserID, store.session.ID
	oldDone, newDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := service.Message(context.Background(), user, id, 1, "first"); oldDone <- err }()
	<-entered
	oldProgress, _ := service.BuildProgress(context.Background(), user, id)
	go func() { _, err := service.Message(context.Background(), user, id, 1, "second"); newDone <- err }()
	<-entered
	newProgress, _ := service.BuildProgress(context.Background(), user, id)
	if newProgress.RequestID == oldProgress.RequestID || newProgress.Status != "building" {
		t.Fatal("new request did not replace the previous progress token")
	}
	close(first)
	if err := awaitBuild(t, oldDone); err == nil {
		t.Fatal("superseded build was accepted")
	}
	progress, _ := service.BuildProgress(context.Background(), user, id)
	if progress.RequestID != newProgress.RequestID || progress.Status != "building" || progress.Plan != nil {
		t.Fatalf("old build overwrote current preview: %+v", progress)
	}
	close(second)
	if err := awaitBuild(t, newDone); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || store.session.Messages[0].Text != "second" {
		t.Fatalf("wrong draft persisted: saves=%d messages=%+v", store.saves, store.session.Messages)
	}
}
