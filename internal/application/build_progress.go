package application

import (
	"context"
	"github.com/google/uuid"
	"time"
	"workflow-optimizer/internal/automation"
)

func (s *AssistantService) beginBuild(id uuid.UUID, revision int) string {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if s.builds == nil {
		s.builds = map[uuid.UUID]automation.BuildProgress{}
	}
	for key, value := range s.builds {
		if time.Since(value.UpdatedAt) > 10*time.Minute {
			delete(s.builds, key)
		}
	}
	token := uuid.NewString()
	s.builds[id] = automation.BuildProgress{Status: "building", Phase: "planning", Revision: revision, RequestID: token, UpdatedAt: time.Now().UTC()}
	return token
}
func (s *AssistantService) buildProgress(id uuid.UUID, token, phase string, pass int, plan *automation.Plan) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	v, ok := s.builds[id]
	if !ok || v.RequestID != token {
		return
	}
	v.Phase = phase
	if pass > 0 {
		v.Pass = pass
	}
	v.UpdatedAt = time.Now().UTC()
	if plan != nil {
		copy := automation.ClonePlan(*plan)
		v.Plan = &copy
	}
	s.builds[id] = v
}
func (s *AssistantService) finishBuild(id uuid.UUID, token, status string) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	v, ok := s.builds[id]
	if !ok || v.RequestID != token {
		return
	}
	v.Status = status
	v.Phase = status
	if status == "completed" {
		v.Phase = "complete"
	}
	v.UpdatedAt = time.Now().UTC()
	s.builds[id] = v
}
func (s *AssistantService) currentBuild(ctx context.Context, id uuid.UUID, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if s.builds[id].RequestID != token {
		return assistantError(automation.ErrConflict)
	}
	return nil
}
func (s *AssistantService) BuildProgress(ctx context.Context, user, id uuid.UUID) (automation.BuildProgress, error) {
	if _, err := s.Get(ctx, user, id); err != nil {
		return automation.BuildProgress{}, err
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	v, ok := s.builds[id]
	if !ok || time.Since(v.UpdatedAt) > 10*time.Minute {
		return automation.BuildProgress{Status: "idle"}, nil
	}
	if v.Plan != nil {
		copy := automation.ClonePlan(*v.Plan)
		v.Plan = &copy
	}
	return v, nil
}
