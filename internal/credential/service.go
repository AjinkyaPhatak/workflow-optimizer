package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Service coordinates credentials:
//
//	create:  plaintext -> SecretEncryptor -> envelope -> Repository
//	resolve: Repository -> workspace/provider checks -> SecretEncryptor -> ResolvedCredential
//
// It enforces workspace ownership on every operation. Get and List return the
// stored record (encrypted envelope), never plaintext.
type Service struct {
	repo Repository
	enc  SecretEncryptor
}

var _ Resolver = (*Service)(nil)

// NewService wires the repository and the encryptor. enc may be nil when no
// encryption key is configured: credentials can then be neither created nor
// resolved (ErrInvalid), but listing and deleting still work.
func NewService(repo Repository, enc SecretEncryptor) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("%w: credential service requires a repository", ErrInvalid)
	}
	return &Service{repo: repo, enc: enc}, nil
}

// CreateInput describes a new credential. Secret is the plaintext; it is
// encrypted before it reaches the repository.
type CreateInput struct {
	WorkspaceID uuid.UUID
	Name        string
	Provider    string
	Type        Type
	Secret      Secret
}

// envelope is the at-rest JSON stored in credentials.encrypted_data. The
// ciphertext (nonce + sealed data + tag) is self-contained except for the key.
type envelope struct {
	Version    int    `json:"v"`
	Ciphertext string `json:"ciphertext"`
}

// payload is the plaintext that gets encrypted.
type payload struct {
	Secret string `json:"secret"`
}

// Create encrypts the secret and stores the credential.
func (s *Service) Create(ctx context.Context, in CreateInput) (Credential, error) {
	switch {
	case in.WorkspaceID == uuid.Nil:
		return Credential{}, fmt.Errorf("%w: workspace is required", ErrInvalid)
	case strings.TrimSpace(in.Name) == "":
		return Credential{}, fmt.Errorf("%w: name is required", ErrInvalid)
	case strings.TrimSpace(in.Provider) == "":
		return Credential{}, fmt.Errorf("%w: provider is required", ErrInvalid)
	case !in.Type.Valid():
		return Credential{}, fmt.Errorf("%w: unknown credential type %q", ErrInvalid, in.Type)
	case in.Secret.Empty():
		return Credential{}, fmt.Errorf("%w: secret is required", ErrInvalid)
	case s.enc == nil:
		return Credential{}, fmt.Errorf("%w: no credential encryption key is configured", ErrInvalid)
	}
	plain, err := json.Marshal(payload{Secret: in.Secret.Reveal()})
	if err != nil {
		return Credential{}, fmt.Errorf("%w: encode secret", ErrInvalid)
	}
	sealed, err := s.enc.Encrypt(ctx, plain)
	clear(plain)
	if err != nil {
		return Credential{}, fmt.Errorf("credential: encrypt secret: %w", err)
	}
	data, err := json.Marshal(envelope{Version: 1, Ciphertext: base64.StdEncoding.EncodeToString(sealed)})
	if err != nil {
		return Credential{}, fmt.Errorf("credential: encode envelope: %w", err)
	}
	return s.repo.Create(ctx, Credential{
		ID:             uuid.New(),
		WorkspaceID:    in.WorkspaceID,
		Name:           strings.TrimSpace(in.Name),
		Provider:       strings.TrimSpace(in.Provider),
		CredentialType: in.Type,
		EncryptedData:  data,
	})
}

// Get returns the workspace's credential record (encrypted, never plaintext).
func (s *Service) Get(ctx context.Context, workspaceID, id uuid.UUID) (Credential, error) {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	if workspaceID == uuid.Nil || c.WorkspaceID != workspaceID {
		return Credential{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return c, nil
}

// List returns the workspace's credential records (encrypted, never
// plaintext).
func (s *Service) List(ctx context.Context, workspaceID uuid.UUID) ([]Credential, error) {
	return s.repo.ListByWorkspace(ctx, workspaceID)
}

// Delete removes the workspace's credential.
func (s *Service) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	return s.repo.Delete(ctx, workspaceID, id)
}

// Resolve implements Resolver: the credential must belong to workspaceID and
// to provider; its secret is decrypted into runtime memory only.
func (s *Service) Resolve(ctx context.Context, workspaceID, id uuid.UUID, provider string) (ResolvedCredential, error) {
	if id == uuid.Nil {
		return ResolvedCredential{}, fmt.Errorf("%w: credential id is required", ErrInvalid)
	}
	if workspaceID == uuid.Nil {
		return ResolvedCredential{}, fmt.Errorf("%w: the execution has no workspace to resolve credentials in", ErrInvalid)
	}
	c, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return ResolvedCredential{}, err
	}
	if c.Provider != provider {
		return ResolvedCredential{}, fmt.Errorf("%w: credential %s is for provider %q, not %q", ErrProviderMismatch, id, c.Provider, provider)
	}
	if s.enc == nil {
		return ResolvedCredential{}, fmt.Errorf("%w: no credential encryption key is configured", ErrDecryptionFailed)
	}
	var env envelope
	if err := json.Unmarshal(c.EncryptedData, &env); err != nil || env.Version != 1 {
		return ResolvedCredential{}, fmt.Errorf("%w: credential %s has an unreadable envelope", ErrDecryptionFailed, id)
	}
	sealed, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return ResolvedCredential{}, fmt.Errorf("%w: credential %s has an unreadable envelope", ErrDecryptionFailed, id)
	}
	plain, err := s.enc.Decrypt(ctx, sealed)
	if err != nil {
		// Deliberately not wrapped: nothing about the ciphertext leaks.
		return ResolvedCredential{}, fmt.Errorf("%w: credential %s", ErrDecryptionFailed, id)
	}
	var p payload
	err = json.Unmarshal(plain, &p)
	clear(plain)
	if err != nil || p.Secret == "" {
		return ResolvedCredential{}, fmt.Errorf("%w: credential %s holds no usable secret", ErrInvalid, id)
	}
	return ResolvedCredential{ID: c.ID, Provider: c.Provider, Type: c.CredentialType, Secret: NewSecret(p.Secret)}, nil
}

// IsCredentialError reports whether err is one of the credential errors.
func IsCredentialError(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrDecryptionFailed) ||
		errors.Is(err, ErrProviderMismatch) || errors.Is(err, ErrInvalid)
}
