package application

import (
	"context"
	"github.com/google/uuid"
	"workflow-optimizer/internal/integration/whatsapp"
	"workflow-optimizer/internal/workspace"
)

type WhatsAppService struct {
	Access *Access
	Client *whatsapp.Client
}

func (s *WhatsAppService) Status(ctx context.Context, user, ws uuid.UUID) (whatsapp.Status, error) {
	if _, err := s.Access.Require(ctx, user, ws, workspace.ActionRead, "workspace"); err != nil {
		return whatsapp.Status{}, err
	}
	var out whatsapp.Status
	err := s.Client.Request(ctx, ws, user, "GET", "status", nil, &out)
	if err != nil {
		return out, &InvalidError{Message: err.Error()}
	}
	return out, nil
}
func (s *WhatsAppService) Connect(ctx context.Context, user, ws uuid.UUID) (whatsapp.Status, error) {
	if _, err := s.Access.Require(ctx, user, ws, workspace.ActionManageCredentials, "workspace"); err != nil {
		return whatsapp.Status{}, err
	}
	var out whatsapp.Status
	err := s.Client.Request(ctx, ws, user, "POST", "connect", nil, &out)
	if err != nil {
		return out, &InvalidError{Message: err.Error()}
	}
	return out, nil
}
func (s *WhatsAppService) Disconnect(ctx context.Context, user, ws uuid.UUID) error {
	if _, err := s.Access.Require(ctx, user, ws, workspace.ActionManageCredentials, "workspace"); err != nil {
		return err
	}
	err := s.Client.Request(ctx, ws, user, "POST", "disconnect", nil, nil)
	if err != nil {
		return &InvalidError{Message: err.Error()}
	}
	return nil
}
