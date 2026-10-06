package credential

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Type is the kind of secret a credential holds. The set establishes the
// generic abstraction; Phase 11 implements API_KEY only.
type Type string

const (
	TypeAPIKey         Type = "API_KEY"
	TypeOAuth2         Type = "OAUTH2"
	TypeBearerToken    Type = "BEARER_TOKEN"
	TypeBasicAuth      Type = "BASIC_AUTH"
	TypeServiceAccount Type = "SERVICE_ACCOUNT"
	TypeCustom         Type = "CUSTOM"
)

// Valid reports whether t is a known credential type.
func (t Type) Valid() bool {
	switch t {
	case TypeAPIKey, TypeOAuth2, TypeBearerToken, TypeBasicAuth, TypeServiceAccount, TypeCustom:
		return true
	}
	return false
}

// Credential is the durable credential record. It never holds plaintext:
// EncryptedData is the at-rest envelope produced by the Service with its
// SecretEncryptor (the repository stores it opaquely).
type Credential struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	Name           string
	Provider       string
	CredentialType Type
	EncryptedData  json.RawMessage
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Errors. They are distinct from provider errors and never retryable: a
// missing, undecryptable or mismatched credential cannot fix itself.
var (
	// ErrNotFound: no such credential in the caller's workspace. A credential
	// of another workspace is reported the same way, so its existence does
	// not leak across workspaces.
	ErrNotFound = errors.New("credential: not found")
	// ErrDecryptionFailed: the stored secret could not be decrypted (wrong
	// key, tampered or corrupt data). The error never carries the ciphertext.
	ErrDecryptionFailed = errors.New("credential: decryption failed")
	// ErrProviderMismatch: the credential belongs to another provider.
	ErrProviderMismatch = errors.New("credential: provider mismatch")
	// ErrInvalid: the credential (or the request for it) is unusable.
	ErrInvalid = errors.New("credential: invalid")
)

// Repository is the persistence boundary for credentials. It stores and
// returns the encrypted envelope only; it never encrypts or decrypts.
type Repository interface {
	Create(ctx context.Context, c Credential) (Credential, error)
	// Get returns ErrNotFound when the credential does not exist.
	Get(ctx context.Context, id uuid.UUID) (Credential, error)
	// Delete removes the workspace's credential (ErrNotFound if it has none
	// with this ID).
	Delete(ctx context.Context, workspaceID, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]Credential, error)
}

// SecretEncryptor is the narrow encryption boundary (authenticated
// encryption). The ciphertext is self-contained except for the master key.
type SecretEncryptor interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// Secret is a plaintext secret held in runtime memory only. It does not
// print, format or marshal its value; Reveal is the single explicit way to
// read it (e.g. to build an Authorization header).
type Secret struct{ value string }

// NewSecret wraps a plaintext secret.
func NewSecret(value string) Secret { return Secret{value: value} }

// Reveal returns the plaintext.
func (s Secret) Reveal() string { return s.value }

// Empty reports whether no secret is held.
func (s Secret) Empty() bool { return s.value == "" }

const redacted = "[REDACTED]"

func (s Secret) String() string   { return redacted }
func (s Secret) GoString() string { return redacted }

// MarshalJSON never emits the plaintext.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// MarshalText never emits the plaintext (also covers encoding/xml, slog text).
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// ResolvedCredential is the runtime-only form of a credential: never
// persisted, never returned by list/get, discarded after use.
type ResolvedCredential struct {
	ID       uuid.UUID
	Provider string
	Type     Type
	Secret   Secret
}

// Resolver resolves a credential for use by one execution. workspaceID is
// the executing workflow's workspace: a credential of any other workspace is
// not found. provider is the provider the credential will be used with.
type Resolver interface {
	Resolve(ctx context.Context, workspaceID, credentialID uuid.UUID, provider string) (ResolvedCredential, error)
}
