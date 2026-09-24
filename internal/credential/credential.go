package credential

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Credential is the durable encrypted-payload container. Encryption itself is
// intentionally outside the Phase 2 persistence layer.
type Credential struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	Name           string
	Provider       string
	CredentialType string
	EncryptedData  json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Repository is the persistence boundary for credentials.
type Repository interface {
	FindByID(context.Context, uuid.UUID) (Credential, error)
	ListByWorkspace(context.Context, uuid.UUID) ([]Credential, error)
}
