package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"workflow-optimizer/internal/automation"
	"workflow-optimizer/internal/workspace"
)

type AssistantService struct {
	buildMu          sync.Mutex
	builds           map[uuid.UUID]automation.BuildProgress
	BuiltinTemplates []automation.WorkflowTemplate
	Access           *Access
	Store            automation.Store
	Backend          automation.Backend
	Tools            []automation.ToolDescriptor
	Planner          func(context.Context, automation.Session) (automation.Planner, error)
	ToolOptions      func(context.Context, uuid.UUID, []automation.ToolDescriptor) ([]automation.ToolDescriptor, error)
}

func assistantError(err error) error {
	switch {
	case errors.Is(err, automation.ErrNotFound):
		return notFound("assistant resource")
	case errors.Is(err, automation.ErrConflict):
		return &ConflictError{Code: "REVISION_CHANGED", Message: "This conversation changed. Reload it before continuing."}
	case errors.Is(err, automation.ErrUnavailable):
		return ErrUnavailable
	default:
		return err
	}
}
func (s *AssistantService) require(ctx context.Context, user, ws uuid.UUID, action workspace.Action) error {
	_, err := s.Access.Require(ctx, user, ws, action, "workspace")
	return err
}
func (s *AssistantService) Create(ctx context.Context, user, ws, credentialID uuid.UUID) (automation.Session, error) {
	if err := s.require(ctx, user, ws, workspace.ActionWrite); err != nil {
		return automation.Session{}, err
	}
	now := time.Now().UTC()
	session := automation.Session{ID: uuid.New(), WorkspaceID: ws, UserID: user, CredentialID: credentialID, Revision: 1, Messages: []automation.Message{}, Answers: map[string]any{}, CreatedAt: now, UpdatedAt: now}
	return session, assistantError(s.Store.CreateSession(ctx, session))
}
func (s *AssistantService) Get(ctx context.Context, user, id uuid.UUID) (automation.Session, error) {
	session, err := s.Store.Session(ctx, id)
	if err != nil {
		return session, assistantError(err)
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionRead); err != nil {
		return session, err
	}
	if session.Compilation != nil {
		// Recheck old drafts against the current contract without writing a revision
		// or starting any actions. Existing comparison aliases are repaired locally.
		tools, err := s.Catalog(ctx, user, session.WorkspaceID)
		if err != nil {
			return session, err
		}
		plan := automation.RefreshQuestionOptions(session.Compilation.Plan, tools)
		session.Answers = automation.DiscardReferenceAnswers(plan, session.Answers)
		compiled := automation.CheckAnswered(automation.InsertConversions(plan, tools), tools, session.Answers)
		session.Compilation = &compiled
	}
	return session, nil
}
func (s *AssistantService) List(ctx context.Context, user, ws uuid.UUID) ([]automation.Session, error) {
	if err := s.require(ctx, user, ws, workspace.ActionRead); err != nil {
		return nil, err
	}
	return s.Store.ListSessions(ctx, ws)
}
func (s *AssistantService) Message(ctx context.Context, user, id uuid.UUID, revision int, text string) (automation.Session, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return session, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return session, err
	}
	if session.Revision != revision {
		return session, assistantError(automation.ErrConflict)
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 30000 {
		return session, &InvalidError{Message: "Describe what you want to do in up to 30,000 characters."}
	}
	if len(session.Messages) > 100 {
		return session, &InvalidError{Message: "Start a new conversation to keep this workflow focused."}
	}
	buildToken := s.beginBuild(id, revision)
	buildDone := false
	defer func() {
		if !buildDone {
			status := "failed"
			if ctx.Err() != nil {
				status = "cancelled"
			}
			s.finishBuild(id, buildToken, status)
		}
	}()
	planner, err := s.Planner(ctx, session)
	if err != nil {
		return session, assistantError(err)
	}
	session.Messages = append(session.Messages, automation.Message{Role: "user", Text: text, CreatedAt: time.Now().UTC()})
	var previous *automation.Plan
	if session.Compilation != nil {
		previous = &session.Compilation.Plan
	}
	tools := s.Tools
	if s.ToolOptions != nil {
		tools, err = s.ToolOptions(ctx, session.WorkspaceID, tools)
		if err != nil {
			return session, err
		}
	}
	compiler := automation.Compiler{Planner: planner, Tools: tools, OnProgress: func(phase string, pass int, plan *automation.Plan) {
		s.buildProgress(id, buildToken, phase, pass, plan)
	}}
	choices, err := s.WorkflowChoices(ctx, user, session.WorkspaceID)
	if err != nil {
		return session, err
	}
	compiled, err := compiler.Compile(ctx, automation.PlanRequest{Workflows: choices, Messages: session.Messages, Previous: previous, Answers: session.Answers})
	if err != nil {
		return session, &InvalidError{Message: err.Error()}
	}
	compiled = automation.CheckAnswered(automation.RefreshQuestionOptions(compiled.Plan, tools), tools, session.Answers)
	session.Compilation = &compiled
	session.Revision++
	message := compiled.Plan.Summary
	if len(compiled.Diagnostics) > 0 {
		message = "The draft needs another check before it can run. Your request and answers are saved."
	}
	session.Messages = append(session.Messages, automation.Message{Role: "assistant", Text: message, CreatedAt: time.Now().UTC()})
	if err := s.currentBuild(ctx, id, buildToken); err != nil {
		return session, err
	}
	s.buildProgress(id, buildToken, "saving", 0, &compiled.Plan)
	if err := s.Store.SaveSession(ctx, session, revision); err != nil {
		return session, assistantError(err)
	}
	buildDone = true
	s.finishBuild(id, buildToken, "completed")
	return session, nil
}

// Repair preserves the user's conversation and answers; it never executes actions.
// Straightforward contract/wiring repairs do not require a model call.
func (s *AssistantService) Repair(ctx context.Context, user, id uuid.UUID, revision int) (automation.Session, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return session, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return session, err
	}
	if session.Revision != revision {
		return session, assistantError(automation.ErrConflict)
	}
	if session.Compilation == nil {
		return session, &InvalidError{Message: "Describe a workflow first."}
	}
	buildToken := s.beginBuild(id, revision)
	buildDone := false
	defer func() {
		if !buildDone {
			status := "failed"
			if ctx.Err() != nil {
				status = "cancelled"
			}
			s.finishBuild(id, buildToken, status)
		}
	}()
	s.buildProgress(id, buildToken, "checking", 1, &session.Compilation.Plan)
	tools := s.Tools
	if s.ToolOptions != nil {
		tools, err = s.ToolOptions(ctx, session.WorkspaceID, tools)
		if err != nil {
			return session, err
		}
	}
	compiled := automation.CheckAnswered(automation.InsertConversions(session.Compilation.Plan, tools), tools, session.Answers)
	if len(compiled.Diagnostics) > 0 {
		planner, err := s.Planner(ctx, session)
		if err != nil {
			return session, assistantError(err)
		}
		compiler := automation.Compiler{Planner: planner, Tools: tools, OnProgress: func(phase string, pass int, plan *automation.Plan) {
			s.buildProgress(id, buildToken, phase, pass, plan)
		}}
		choices, err := s.WorkflowChoices(ctx, user, session.WorkspaceID)
		if err != nil {
			return session, err
		}
		compiled, err = compiler.Compile(ctx, automation.PlanRequest{Workflows: choices, Repair: true, Messages: session.Messages, Previous: &compiled.Plan, Answers: session.Answers, Diagnostics: compiled.Diagnostics})
		if err != nil {
			return session, &InvalidError{Message: "I couldn’t finish the repair. Your draft and answers are saved; try again or describe the change you want."}
		}
		checked := automation.CheckAnswered(compiled.Plan, tools, session.Answers)
		for _, diagnostic := range compiled.Diagnostics {
			if diagnostic.Code == "ACTION_CHANGE" {
				checked.Diagnostics = append(checked.Diagnostics, diagnostic)
				checked.Ready = false
			}
		}
		compiled = checked
	}
	session.Compilation = &compiled
	session.Revision++
	message := "The workflow is checked and ready to run."
	if len(compiled.Diagnostics) > 0 {
		message = "I couldn’t finish checking this workflow automatically. Your request and answers are saved."
	} else if !compiled.Ready {
		message = "The workflow is checked. I need the details in the questions below before it can run."
	}
	session.Messages = append(session.Messages, automation.Message{Role: "assistant", Text: message, CreatedAt: time.Now().UTC()})
	if err := s.currentBuild(ctx, id, buildToken); err != nil {
		return session, err
	}
	s.buildProgress(id, buildToken, "saving", 0, &compiled.Plan)
	if err := s.Store.SaveSession(ctx, session, revision); err != nil {
		return session, assistantError(err)
	}
	buildDone = true
	s.finishBuild(id, buildToken, "completed")
	return session, nil
}
func (s *AssistantService) Edit(ctx context.Context, user, id uuid.UUID, revision int, plan automation.Plan) (automation.Session, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return session, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return session, err
	}
	if session.Revision != revision {
		return session, assistantError(automation.ErrConflict)
	}
	tools, err := s.Catalog(ctx, user, session.WorkspaceID)
	if err != nil {
		return session, err
	}
	compiled := automation.CheckAnswered(automation.InsertConversions(automation.RefreshQuestionOptions(plan, tools), tools), tools, session.Answers)
	session.Compilation = &compiled
	session.Revision++
	return session, assistantError(s.Store.SaveSession(ctx, session, revision))
}
func (s *AssistantService) AnswerDraft(ctx context.Context, user, id uuid.UUID, revision int, questionID string, value any) (automation.Session, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return session, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return session, err
	}
	if session.Revision != revision {
		return session, assistantError(automation.ErrConflict)
	}
	if session.Compilation == nil {
		return session, &InvalidError{Message: "Describe a workflow first."}
	}
	var q *automation.Question
	for _, question := range session.Compilation.Plan.Questions {
		if question.ID == questionID {
			copy := question
			q = &copy
			break
		}
	}
	if q == nil {
		return session, notFound("question")
	}
	if err := automation.ValidateAnswer(*q, value); err != nil {
		return session, &InvalidError{Message: err.Error()}
	}
	if session.Answers == nil {
		session.Answers = map[string]any{}
	}
	session.Answers[questionID] = value
	tools, err := s.Catalog(ctx, user, session.WorkspaceID)
	if err != nil {
		return session, err
	}
	compiled := automation.CheckAnswered(session.Compilation.Plan, tools, session.Answers)
	if q.Purpose == "intent" {
		compiled, err = s.applyDraftIntent(ctx, user, session, *q, tools)
		if err != nil {
			return session, err
		}
	}
	session.Compilation = &compiled
	session.Revision++
	return session, assistantError(s.Store.SaveSession(ctx, session, revision))
}

func (s *AssistantService) SetDraftBudget(ctx context.Context, user, id uuid.UUID, revision int, budget automation.Budget) (automation.Session, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return session, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionWrite); err != nil {
		return session, err
	}
	if session.Revision != revision {
		return session, assistantError(automation.ErrConflict)
	}
	if session.Compilation == nil {
		return session, &InvalidError{Message: "Describe a workflow first."}
	}
	if !budget.Valid() {
		return session, &InvalidError{Message: "Set positive action, token, and active-time limits."}
	}
	tools, err := s.Catalog(ctx, user, session.WorkspaceID)
	if err != nil {
		return session, err
	}
	plan, answers := automation.ConfirmBudget(session.Compilation.Plan, budget, session.Answers)
	compiled := automation.CheckAnswered(automation.InsertConversions(plan, tools), tools, answers)
	session.Compilation, session.Answers = &compiled, answers
	session.Revision++
	return session, assistantError(s.Store.SaveSession(ctx, session, revision))
}
func (s *AssistantService) Start(ctx context.Context, user, id uuid.UUID, revision int, requestID string) (automation.Run, error) {
	session, err := s.Get(ctx, user, id)
	if err != nil {
		return automation.Run{}, err
	}
	if err := s.require(ctx, user, session.WorkspaceID, workspace.ActionExecute); err != nil {
		return automation.Run{}, err
	}
	if session.Revision != revision {
		return automation.Run{}, assistantError(automation.ErrConflict)
	}
	if session.Compilation == nil {
		return automation.Run{}, &InvalidError{Message: "Describe a workflow first."}
	}
	check := *session.Compilation // Get already checked the current workspace catalog.
	if !check.Ready {
		return automation.Run{}, &InvalidError{Message: "Answer the remaining questions and fix the highlighted steps first."}
	}
	if s.Backend == nil {
		return automation.Run{}, ErrUnavailable
	}
	if requestID != "" {
		if _, err := uuid.Parse(requestID); err != nil {
			return automation.Run{}, &InvalidError{Message: "The run request ID must be a UUID."}
		}
	}
	runID := uuid.NewSHA1(session.ID, []byte("revision:"+strconv.Itoa(revision)+":"+requestID))
	run, err := s.Store.Run(ctx, runID)
	if errors.Is(err, automation.ErrNotFound) {
		run = automation.Run{ID: runID, WorkspaceID: session.WorkspaceID, UserID: user, SessionID: &session.ID, CredentialID: session.CredentialID, Plan: check.Plan, Revision: revision, Backend: "temporal", Status: "PENDING", Outputs: map[string]map[string]any{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := s.Store.CreateRun(ctx, run); err != nil {
			return run, err
		}
	} else if err != nil {
		return run, err
	}
	if err := s.Backend.Start(ctx, run, session.Answers, nil); err != nil {
		return run, &InvalidError{Message: "The execution service is unavailable. Your workflow is saved; try running it again."}
	}
	return run, nil
}
func (s *AssistantService) Run(ctx context.Context, user, id uuid.UUID) (automation.Run, error) {
	run, err := s.Store.Run(ctx, id)
	if err != nil {
		return run, assistantError(err)
	}
	return run, s.require(ctx, user, run.WorkspaceID, workspace.ActionRead)
}
func (s *AssistantService) Runs(ctx context.Context, user, ws uuid.UUID) ([]automation.Run, error) {
	if err := s.require(ctx, user, ws, workspace.ActionRead); err != nil {
		return nil, err
	}
	return s.Store.ListRuns(ctx, ws)
}
func (s *AssistantService) AnswerRun(ctx context.Context, user, id uuid.UUID, qid string, revision int, value any) error {
	if s.Backend == nil {
		return ErrUnavailable
	}
	run, err := s.Run(ctx, user, id)
	if err != nil {
		return err
	}
	if err := s.require(ctx, user, run.WorkspaceID, workspace.ActionExecute); err != nil {
		return err
	}
	if run.Revision != revision {
		return assistantError(automation.ErrConflict)
	}
	if run.Question == nil || run.Question.ID != qid {
		return &ConflictError{Code: "QUESTION_CHANGED", Message: "This question has already changed. Reload the run."}
	}
	if err := automation.ValidateAnswer(*run.Question, value); err != nil {
		return &InvalidError{Message: err.Error()}
	}
	return s.Backend.Answer(ctx, id, qid, revision, value)
}
func (s *AssistantService) Control(ctx context.Context, user, id uuid.UUID, action string, budget *automation.Budget) error {
	if s.Backend == nil {
		return ErrUnavailable
	}
	run, err := s.Run(ctx, user, id)
	if err != nil {
		return err
	}
	if err := s.require(ctx, user, run.WorkspaceID, workspace.ActionExecute); err != nil {
		return err
	}
	if action != "pause" && action != "resume" && action != "cancel" {
		return &InvalidError{Message: "Choose pause, resume, or cancel."}
	}
	if budget != nil && !budget.Valid() {
		return &InvalidError{Message: "Set positive action, token, and active-time limits."}
	}
	return s.Backend.Control(ctx, id, action, budget)
}
func (s *AssistantService) Events(ctx context.Context, user, id uuid.UUID, after int64) ([]automation.Event, error) {
	if _, err := s.Run(ctx, user, id); err != nil {
		return nil, err
	}
	return s.Store.Events(ctx, id, after)
}
func (s *AssistantService) Automations(ctx context.Context, user, ws uuid.UUID) ([]automation.Automation, error) {
	if err := s.require(ctx, user, ws, workspace.ActionRead); err != nil {
		return nil, err
	}
	return s.Store.ListAutomations(ctx, ws)
}

func (s *AssistantService) AutomationRuns(ctx context.Context, user, id uuid.UUID) ([]automation.Run, error) {
	a, err := s.Store.Automation(ctx, id)
	if err != nil {
		return nil, assistantError(err)
	}
	if err := s.require(ctx, user, a.WorkspaceID, workspace.ActionRead); err != nil {
		return nil, err
	}
	return s.Store.ListAutomationRuns(ctx, a.WorkspaceID, a.ID)
}
func (s *AssistantService) SaveAutomation(ctx context.Context, user uuid.UUID, a automation.Automation) (automation.Automation, error) {
	if s.Backend == nil {
		return a, ErrUnavailable
	}
	if err := s.require(ctx, user, a.WorkspaceID, workspace.ActionExecute); err != nil {
		return a, err
	}
	if a.ID != uuid.Nil {
		old, err := s.Store.Automation(ctx, a.ID)
		if err != nil {
			return a, assistantError(err)
		}
		if old.WorkspaceID != a.WorkspaceID {
			return a, notFound("automation")
		}
	}
	if a.Timezone == "" {
		a.Timezone = "Asia/Kolkata"
	}
	if _, err := time.LoadLocation(a.Timezone); err != nil {
		return a, &InvalidError{Message: "Choose a valid timezone."}
	}
	if a.Cron != "" {
		if _, err := cron.ParseStandard(a.Cron); err != nil {
			return a, &InvalidError{Message: "Use a five-field cron expression."}
		}
	}
	if a.Trigger != "" && a.Trigger != "webhook" && a.Trigger != "knowledge.indexed" && a.Trigger != "workflow.completed" && a.Trigger != "whatsapp.message" {
		return a, &InvalidError{Message: "Choose a supported event trigger."}
	}
	if a.Trigger != "" && a.Trigger != "webhook" && strings.TrimSpace(a.Source) == "" {
		return a, &InvalidError{Message: "Choose the knowledge base, conversation, or exact WhatsApp sender that may start this workflow."}
	}
	if a.Cron == "" && a.Trigger == "" {
		return a, &InvalidError{Message: "Choose a schedule or an event trigger."}
	}
	if !automation.Check(a.Plan, s.Tools).Ready {
		return a, &InvalidError{Message: "Save a complete workflow with no unanswered questions before scheduling it."}
	}
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	a.UserID = user
	a.CreatedAt = time.Now().UTC()
	if err := s.Store.SaveAutomation(ctx, a); err != nil {
		return a, err
	}
	if err := s.Backend.Schedule(ctx, a); err != nil {
		return a, &InvalidError{Message: "The schedule was saved but could not be activated. Check the execution service and save it again."}
	}
	return a, nil
}
func (s *AssistantService) Trigger(ctx context.Context, id uuid.UUID, eventID string, event any) error {
	if s.Backend == nil {
		return ErrUnavailable
	}
	a, err := s.Store.Automation(ctx, id)
	if err != nil {
		return assistantError(err)
	}
	if !a.Enabled {
		return &InvalidError{Message: "This automation is paused."}
	}
	if strings.TrimSpace(eventID) == "" || len(eventID) > 200 {
		return &InvalidError{Message: "Provide a unique event ID of up to 200 characters."}
	}
	// Temporal's deterministic workflow ID closes the database/dispatch retry gap.
	if _, err := s.Store.AcceptTrigger(ctx, id, eventID); err != nil {
		return err
	}
	if err := s.Backend.Trigger(ctx, a, eventID, automation.EventEnvelope{Data: event}); err != nil {
		return fmt.Errorf("trigger could not be dispatched")
	}
	return nil
}
func (s *AssistantService) DeleteAutomation(ctx context.Context, user, id uuid.UUID) error {
	a, err := s.Store.Automation(ctx, id)
	if err != nil {
		return assistantError(err)
	}
	if err := s.require(ctx, user, a.WorkspaceID, workspace.ActionExecute); err != nil {
		return err
	}
	if cleaner, ok := s.Backend.(interface {
		DeleteSchedule(context.Context, uuid.UUID) error
	}); ok {
		if err := cleaner.DeleteSchedule(ctx, id); err != nil {
			return err
		}
	}
	return s.Store.DeleteAutomation(ctx, id)
}
func (s *AssistantService) SendEvent(ctx context.Context, user, id uuid.UUID, event any) error {
	if s.Backend == nil {
		return ErrUnavailable
	}
	run, err := s.Run(ctx, user, id)
	if err != nil {
		return err
	}
	if err := s.require(ctx, user, run.WorkspaceID, workspace.ActionExecute); err != nil {
		return err
	}
	return s.Backend.Event(ctx, id, event)
}
