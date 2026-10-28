package application

import (
	"context"
	"testing"

	"workflow-optimizer/internal/automation"
)

func TestConfirmDraftBudgetDoesNotPlanOrExecuteAndChecksRevision(t *testing.T) {
	service, store, plan := buildFixture(func(context.Context, automation.PlanRequest) (automation.Plan, error) {
		t.Fatal("budget confirmation must not call a model")
		return automation.Plan{}, nil
	})
	q := automation.Question{ID: "max_actions", StepID: "limits", Input: "budget.max_actions", Kind: "number", Prompt: "How many actions?", Required: true}
	plan.Steps = append([]automation.Step{{ID: "limits", Kind: "ask", Question: &q}}, plan.Steps...)
	plan.Budget.Automatic = false
	plan.Questions = []automation.Question{q}
	compiled := automation.Check(plan, service.Tools)
	store.session.Compilation = &compiled
	ctx, user, id := context.Background(), store.session.UserID, store.session.ID
	if _, err := service.SetDraftBudget(ctx, user, id, 1, automation.Budget{}); err == nil || store.saves != 0 {
		t.Fatal("invalid limits were saved")
	}
	budget := automation.Budget{MaxActions: 25, MaxTokens: 25000, MaxActiveSeconds: 1800}
	saved, err := service.SetDraftBudget(ctx, user, id, 1, budget)
	if err != nil || saved.Revision != 2 || !saved.Compilation.Ready || saved.Answers[q.ID] != float64(25) || saved.Compilation.Plan.Steps[1].ID != plan.Steps[1].ID {
		t.Fatalf("budget confirmation failed: %+v %v", saved, err)
	}
	if _, err := service.SetDraftBudget(ctx, user, id, 1, budget); err == nil || store.saves != 1 {
		t.Fatal("a stale form overwrote the new revision")
	}
}
