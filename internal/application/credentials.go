package application

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/workspace"
)

// CredentialLocator finds a credential by ID alone, to learn its workspace
// before authorizing (the Phase 11 credential.Repository).
type CredentialLocator interface {
	Get(ctx context.Context, id uuid.UUID) (credential.Credential, error)
}

// ProviderCatalog lists the registered providers (the Phase 11 provider
// registry).
type ProviderCatalog interface {
	List() []string
}

// CredentialService authorizes credential management and delegates to the
// Phase 11 credential.Service (encryption -> repository). It never returns
// or logs a secret; listed records hold only the encrypted envelope, which
// callers must not expose either.
type CredentialService struct {
	access     *Access
	service    *credential.Service
	locator    CredentialLocator
	providers  ProviderCatalog
	canEncrypt bool
}

// NewCredentialService wires the service. canEncrypt reports whether the
// encryption key is configured; without it credentials cannot be created.
func NewCredentialService(access *Access, service *credential.Service, locator CredentialLocator, providers ProviderCatalog, canEncrypt bool) *CredentialService {
	return &CredentialService{access: access, service: service, locator: locator, providers: providers, canEncrypt: canEncrypt}
}

// CreateCredential is a new credential (the secret travels only in memory).
type CreateCredential struct {
	WorkspaceID uuid.UUID
	Name        string
	Provider    string
	Type        credential.Type
	Secret      credential.Secret
}

// Create encrypts and stores a credential. Owners and admins only.
func (s *CredentialService) Create(ctx context.Context, user uuid.UUID, in CreateCredential) (credential.Credential, error) {
	if _, err := s.access.Require(ctx, user, in.WorkspaceID, workspace.ActionManageCredentials, "workspace"); err != nil {
		return credential.Credential{}, err
	}
	if !slices.Contains(s.providers.List(), in.Provider) {
		return credential.Credential{}, &InvalidError{Message: "unknown provider"}
	}
	if !s.canEncrypt {
		return credential.Credential{}, ErrUnavailable
	}
	c, err := s.service.Create(ctx, credential.CreateInput{WorkspaceID: in.WorkspaceID, Name: in.Name,
		Provider: in.Provider, Type: in.Type, Secret: in.Secret})
	if errors.Is(err, credential.ErrInvalid) {
		return credential.Credential{}, &InvalidError{Message: "invalid credential"}
	}
	return c, err
}

// List returns the workspace's credential metadata (any member: workflow
// authors need credential IDs).
func (s *CredentialService) List(ctx context.Context, user, workspaceID uuid.UUID) ([]credential.Credential, error) {
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionRead, "workspace"); err != nil {
		return nil, err
	}
	return s.service.List(ctx, workspaceID)
}

// Delete removes a credential. Owners and admins only.
func (s *CredentialService) Delete(ctx context.Context, user, id uuid.UUID) error {
	c, err := s.locator.Get(ctx, id)
	if errors.Is(err, credential.ErrNotFound) {
		return notFound("credential")
	}
	if err != nil {
		return err
	}
	if _, err := s.access.Require(ctx, user, c.WorkspaceID, workspace.ActionManageCredentials, "credential"); err != nil {
		return err
	}
	if err := s.service.Delete(ctx, c.WorkspaceID, id); errors.Is(err, credential.ErrNotFound) {
		return notFound("credential")
	} else {
		return err
	}
}
