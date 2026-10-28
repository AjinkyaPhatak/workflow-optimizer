package phase12_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"workflow-optimizer/internal/automation"
)

func TestAutomationRunHistoryKeepsActiveRunsAndTenantBoundary(t *testing.T) {
	e := newEnv(t)
	owner, other := e.register("ScheduleOwner"), e.register("OtherScheduleOwner")
	id, unrelated := uuid.New(), uuid.New()
	ws, user := uuid.MustParse(owner.workspace), uuid.MustParse(owner.userID)
	config := automation.Automation{ID: id, WorkspaceID: ws, UserID: user, Name: "Recurring report", Enabled: true}
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.raw.Exec(context.Background(), `INSERT INTO automations(id,workspace_id,user_id,body) VALUES($1,$2,$3,$4)`, id, ws, user, body); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/automations/" + id.String() + "/runs"
	read := func() []automation.Run {
		return decodeEditor[struct {
			Items []automation.Run `json:"items"`
		}](t, e.call(owner.token, "GET", path, nil), 200).Items
	}
	if items := read(); items == nil || len(items) != 0 {
		t.Fatalf("empty history must be an array: %+v", items)
	}
	insert := func(status string, trace, ancestors []uuid.UUID, workspaceID, userID uuid.UUID, at time.Time) uuid.UUID {
		t.Helper()
		run := automation.Run{ID: uuid.New(), WorkspaceID: workspaceID, UserID: userID, Status: status, Trace: trace, Ancestors: ancestors, CreatedAt: at}
		body, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.raw.Exec(context.Background(), `INSERT INTO automation_runs(id,workspace_id,user_id,body,created_at) VALUES($1,$2,$3,$4,$5)`, run.ID, workspaceID, userID, body, at); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	old := time.Now().Add(-24 * time.Hour)
	wantActive := map[uuid.UUID]bool{}
	for _, status := range []string{"PENDING", "RUNNING", "PAUSED", "WAITING_FOR_INPUT", "WAITING_FOR_EVENT"} {
		wantActive[insert(status, []uuid.UUID{unrelated, id}, nil, ws, user, old)] = true
	}
	// More than the workspace-wide history limit must not bury old active runs.
	wantRecent := map[uuid.UUID]bool{}
	for i := 0; i < 65; i++ {
		runID := insert([]string{"COMPLETED", "FAILED", "CANCELLED"}[i%3], []uuid.UUID{id}, nil, ws, user, old.Add(time.Duration(i+1)*time.Minute))
		if i >= 45 {
			wantRecent[runID] = true
		}
	}
	// A trace is event ancestry, not ownership by every automation in the chain.
	insert("RUNNING", []uuid.UUID{id, unrelated}, nil, ws, user, time.Now())
	// Child workflows inherit traces; they belong under their root's run view.
	insert("RUNNING", []uuid.UUID{id}, []uuid.UUID{uuid.New()}, ws, user, time.Now())
	// Even a matching trace cannot escape the workspace filter.
	insert("RUNNING", []uuid.UUID{id}, nil, uuid.MustParse(other.workspace), uuid.MustParse(other.userID), time.Now())
	items := read()
	activeCount := len(wantActive)
	if len(items) != activeCount+20 {
		t.Fatalf("got %d runs; want all 5 active and latest 20 finished", len(items))
	}
	for i, run := range items {
		if i < activeCount {
			if !wantActive[run.ID] {
				t.Fatalf("missing/misplaced active run %s", run.ID)
			}
			delete(wantActive, run.ID)
		} else if !wantRecent[run.ID] {
			t.Fatalf("unexpected history run %s (%s)", run.ID, run.Status)
		}
	}
	for _, token := range []string{"", other.token} {
		status := 404
		if token == "" {
			status = 401
		}
		e.must(e.call(token, "GET", path, nil), status)
	}
	e.must(e.call(owner.token, "GET", fmt.Sprintf("/api/v1/automations/%s/runs", uuid.New()), nil), 404)
	e.must(e.call(owner.token, "GET", "/api/v1/automations/not-a-uuid/runs", nil), 400)
}
