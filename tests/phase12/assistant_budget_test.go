package phase12_test

import (
	"testing"

	"workflow-optimizer/internal/automation"
)

func TestBudgetFormConfirmsQuestionsAndPreservesTenantAndRevision(t *testing.T) {
	e := newEnv(t)
	owner, other := e.register("BudgetOwner"), e.register("OtherBudgetOwner")
	session := decodeEditor[automation.Session](t, e.call(owner.token, "POST", "/api/v1/assistant/sessions", map[string]any{"workspace_id": owner.workspace}), 201)
	path := "/api/v1/assistant/sessions/" + session.ID.String()
	plan := automation.EmptyPlan("Budget confirmation")
	plan.Budget.Automatic = false
	q := automation.Question{ID: "max_actions", StepID: "limits", Input: "answer", Kind: "number", Prompt: "How many actions?", Required: true}
	plan.Questions = []automation.Question{q}
	plan.Steps = []automation.Step{{ID: "limits", Kind: "ask", Question: &q}, {ID: "format", Kind: "tool", Tool: "value.text", Inputs: []automation.Binding{{Name: "value", Source: "literal", ValueJSON: "42"}}}}
	saved := decodeEditor[automation.Session](t, e.call(owner.token, "PUT", path+"/plan", map[string]any{"revision": 1, "plan": plan}), 200)
	if saved.Compilation.Ready {
		t.Fatal("model-supplied default limits were treated as human confirmation")
	}
	budget := plan.Budget // Clicking unchanged values still confirms the form.
	body := map[string]any{"revision": saved.Revision, "budget": budget}
	confirmed := decodeEditor[automation.Session](t, e.call(owner.token, "PUT", path+"/budget", body), 200)
	if !confirmed.Compilation.Ready || confirmed.Revision != saved.Revision+1 || confirmed.Answers[q.ID] != float64(budget.MaxActions) || confirmed.Compilation.Plan.Steps[1].ID != "format" {
		t.Fatalf("budget form did not confirm its questions: %+v", confirmed)
	}
	e.must(e.call(owner.token, "PUT", path+"/budget", body), 409)
	e.must(e.call(other.token, "PUT", path+"/budget", body), 404)
	e.must(e.call("", "PUT", path+"/budget", body), 401)
	e.must(e.call(owner.token, "PUT", path+"/budget", map[string]any{"revision": confirmed.Revision, "budget": automation.Budget{}}), 400)
	runs := e.must(e.call(owner.token, "GET", "/api/v1/automation-runs?workspace_id="+owner.workspace, nil), 200)
	if len(runs["items"].([]any)) != 0 {
		t.Fatal("confirming limits started execution")
	}
}
