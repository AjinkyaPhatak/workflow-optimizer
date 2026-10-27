package application

import (
	"context"

	"github.com/google/uuid"
	"workflow-optimizer/internal/automation"
)

// An answer about the intended outcome is a compiler input, not message copy.
// The saved draft remains unchanged if planning fails or is cancelled.
func (s *AssistantService) applyDraftIntent(ctx context.Context, user uuid.UUID, session automation.Session, q automation.Question, tools []automation.ToolDescriptor) (automation.Compilation, error) {
	if s.Planner == nil {
		return automation.Compilation{}, ErrUnavailable
	}
	token := s.beginBuild(session.ID, session.Revision)
	done := false
	defer func() {
		if !done {
			status := "failed"
			if ctx.Err() != nil {
				status = "cancelled"
			}
			s.finishBuild(session.ID, token, status)
		}
	}()
	planner, err := s.Planner(ctx, session)
	if err != nil {
		return automation.Compilation{}, assistantError(err)
	}
	choices, err := s.WorkflowChoices(ctx, user, session.WorkspaceID)
	if err != nil {
		return automation.Compilation{}, err
	}
	compiler := automation.Compiler{Planner: planner, Tools: tools, OnProgress: func(phase string, pass int, plan *automation.Plan) {
		s.buildProgress(session.ID, token, phase, pass, plan)
	}}
	compiled, err := compiler.Compile(ctx, automation.PlanRequest{Workflows: choices, Repair: true, IntentQuestion: &q, Messages: session.Messages, Previous: &session.Compilation.Plan, Answers: session.Answers})
	if err != nil {
		return compiled, &InvalidError{Message: err.Error()}
	}
	// Preserve compiler guard diagnostics when applying the ordinary answer check.
	checked := automation.CheckAnswered(automation.RefreshQuestionOptions(compiled.Plan, tools), tools, session.Answers)
	if len(compiled.Diagnostics) > 0 {
		// Keep the pending intent question so later read-time validation cannot
		// accidentally erase a failed semantic guard and enable a send.
		checked = automation.CheckAnswered(session.Compilation.Plan, tools, session.Answers)
		checked.Diagnostics = append(checked.Diagnostics, compiled.Diagnostics...)
		checked.Ready = false
	}
	if err := s.currentBuild(ctx, session.ID, token); err != nil {
		return checked, err
	}
	done = true
	s.finishBuild(session.ID, token, "completed")
	return checked, nil
}
