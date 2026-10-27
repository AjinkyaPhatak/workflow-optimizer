package application

import (
	"context"
	"testing"

	"workflow-optimizer/internal/automation"
)

func TestIntentAnswerReplansInsteadOfSendingGuidance(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(map[bool]string{false: "result_dataflow", true: "verbatim_rejected"}[bad], func(t *testing.T) {
			calls := 0
			var plan automation.Plan
			service, store, _ := buildFixture(func(_ context.Context, req automation.PlanRequest) (automation.Plan, error) {
				calls++
				if req.IntentQuestion == nil || req.Answers["email_content"] != "summarize the gainers" || !req.Repair {
					t.Fatalf("missing intent context: %+v", req)
				}
				next := automation.ClonePlan(plan)
				next.Questions = nil
				if bad {
					next.Steps[1].Inputs[1] = automation.Binding{Name: "config.body", Source: "literal", ValueJSON: `"summarize the gainers"`}
				} else {
					next.Steps[1].Inputs[1] = automation.Binding{Name: "config.body", Source: "step", Ref: "research.text"}
				}
				return next, nil
			})
			service.Tools = []automation.ToolDescriptor{{ID: "lookup", Outputs: []automation.Field{{Name: "text", Type: "string"}}}, {ID: "node.gmail.send", Name: "Send email", SideEffects: "unsafe", Inputs: []automation.Field{{Name: "config.to", Type: "string", Required: true}, {Name: "config.body", Type: "string", Required: true}}}}
			plan = automation.EmptyPlan("Email Nifty results")
			plan.Steps = []automation.Step{{ID: "research", Kind: "tool", Tool: "lookup"}, {ID: "email", Name: "Email results", Kind: "tool", Tool: "node.gmail.send", Inputs: []automation.Binding{{Name: "config.to", Source: "literal", ValueJSON: `"me@example.com"`}, {Name: "config.body", Source: "answer", Ref: "email_content"}}}}
			plan.Questions = []automation.Question{{ID: "email_content", Prompt: "Which results should the email include?", Kind: "text", Purpose: "intent", Required: true, StepID: "email", Input: "config.body"}}
			compiled := automation.Check(plan, service.Tools)
			store.session.Compilation = &compiled
			got, err := service.AnswerDraft(context.Background(), store.session.UserID, store.session.ID, 1, "email_content", "summarize the gainers")
			if err != nil || calls == 0 || store.saves != 1 || got.Revision != 2 {
				t.Fatalf("answer not replanned: %+v %v calls=%d saves=%d", got, err, calls, store.saves)
			}
			if got.Compilation.Ready == bad {
				t.Fatalf("wrong readiness: %+v", got.Compilation)
			}
			reread, err := service.Get(context.Background(), store.session.UserID, store.session.ID)
			if err != nil || reread.Compilation.Ready == bad {
				t.Fatalf("read erased intent guard: %+v %v", reread.Compilation, err)
			}
		})
	}
}

func TestDelegatedPlanningDefaultCompletesScaffoldOnce(t *testing.T) {
	const delegated = "No, just use whatever is considered good in industry, I guess"
	const scope = "Do pearson correlation, and just check past 10 days open/close trend for each top mover"
	calls := 0
	service, store, _ := buildFixture(func(_ context.Context, req automation.PlanRequest) (automation.Plan, error) {
		calls++
		if req.Repair || req.IntentQuestion == nil || req.IntentQuestion.ID != "weak_correlation_cutoff" || req.Answers["weak_correlation_cutoff"] != delegated || req.Answers["correlation_scope"] != scope || len(req.Messages) != 1 {
			t.Fatalf("planning answer lost its original request or answers: %+v", req)
		}
		next := automation.EmptyPlan("Email qualifying market analysis")
		next.Summary = "Analyze the requested ten-day open/close trend using Pearson correlation. Assume weak means absolute r below 0.3; email only qualifying results."
		next.Steps = []automation.Step{
			{ID: "research", Kind: "tool", Tool: "lookup"},
			{ID: "analysis", Kind: "tool", Tool: "analyze", Inputs: []automation.Binding{{Name: "rows", Source: "step", Ref: "research.rows"}}},
			{ID: "weak", Kind: "branch", Condition: &automation.Condition{Left: automation.Binding{Source: "step", Ref: "analysis.absolute_r"}, Op: "less", Right: automation.Binding{Source: "literal", ValueJSON: "0.3"}}, Children: []automation.Step{{ID: "email", Kind: "tool", Tool: "node.gmail.send", Inputs: []automation.Binding{{Name: "config.to", Source: "literal", ValueJSON: `"me@example.com"`}, {Name: "config.body", Source: "step", Ref: "analysis.text"}}}}},
		}
		return next, nil
	})
	service.Tools = []automation.ToolDescriptor{
		{ID: "lookup", Outputs: []automation.Field{{Name: "rows", Type: "array"}}},
		{ID: "analyze", Inputs: []automation.Field{{Name: "rows", Type: "array", Required: true}}, Outputs: []automation.Field{{Name: "text", Type: "string"}, {Name: "absolute_r", Type: "number"}}},
		{ID: "node.gmail.send", SideEffects: "unsafe", Inputs: []automation.Field{{Name: "config.to", Type: "string", Required: true}, {Name: "config.body", Type: "string", Required: true}}},
	}
	q := automation.Question{ID: "weak_correlation_cutoff", Prompt: "What should count as weak correlation?", Kind: "text", Purpose: "intent", Required: true, StepID: "clarify"}
	plan := automation.EmptyPlan("Clarify market analysis")
	// The persisted Pearson scaffold had no execution budget yet. Completing
	// initial planning may choose normal limits rather than retry indefinitely.
	plan.Budget = automation.Budget{}
	plan.Steps = []automation.Step{{ID: "clarify", Kind: "ask", Question: &q}}
	plan.Questions = []automation.Question{q}
	compiled := automation.Check(plan, service.Tools)
	store.session.Compilation = &compiled
	store.session.Messages = []automation.Message{{Role: "user", Text: "Send mail to me@example.com if there is weak correlation."}}
	store.session.Answers["correlation_scope"] = scope
	got, err := service.AnswerDraft(context.Background(), store.session.UserID, store.session.ID, 1, q.ID, delegated)
	if err != nil || calls != 1 || store.saves != 1 || got.Revision != 2 || !got.Compilation.Ready || len(got.Compilation.Plan.Questions) != 0 || len(got.Compilation.Plan.Steps) != 3 {
		t.Fatalf("delegated answer failed to finish planning: %+v err=%v calls=%d saves=%d", got, err, calls, store.saves)
	}
	reread, err := service.Get(context.Background(), store.session.UserID, store.session.ID)
	if err != nil || !reread.Compilation.Ready || calls != 1 {
		t.Fatalf("read lost completed planning or replanned: %+v %v calls=%d", reread, err, calls)
	}
}
