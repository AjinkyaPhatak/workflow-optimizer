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
