package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service coordinates credentials:
//
//	create:  plaintext -> SecretEncryptor -> envelope -> Repository
//	resolve: Repository -> workspace/provider checks -> SecretEncryptor -> ResolvedCredential
//
// It enforces workspace ownership on every operation. Get and List return the
// stored record (encrypted envelope), never plaintext.
//
// Phase C2: an OAUTH2 credential holds OAuth token material (OAuthToken) in
// the same encrypted envelope. Resolve returns its access token while it is
// valid; refreshing an expired token is the job of a token manager wrapped
// around the Service (internal/oauth), which reads and rewrites the material
// with ResolveOAuth and ReplaceOAuth.
type Service struct {
	repo Repository
	enc  SecretEncryptor
	now  func() time.Time
}

var _ Resolver = (*Service)(nil)

// NewService wires the repository and the encryptor. enc may be nil when no
// encryption key is configured: credentials can then be neither created nor
// resolved (ErrInvalid), but listing and deleting still work.
func NewService(repo Repository, enc SecretEncryptor) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("%w: credential service requires a repository", ErrInvalid)
	}
	return &Service{repo: repo, enc: enc, now: time.Now}, nil
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

// payload is the plaintext that gets encrypted: a single secret (API keys,
// Phase 11) or OAuth material (Phase C2). An OAUTH2 credential whose
// material was removed (disconnected, revoked) has neither.
type payload struct {
	Secret string        `json:"secret,omitempty"`
	OAuth  *oauthPayload `json:"oauth,omitempty"`
}

// oauthPayload is OAuthToken in plain form. It exists only inside seal/open.
type oauthPayload struct {
	AccessToken       string     `json:"access_token"`
	RefreshToken      string     `json:"refresh_token,omitempty"`
	TokenType         string     `json:"token_type,omitempty"`
	Expiry            *time.Time `json:"expiry,omitempty"`
	Scopes            []string   `json:"scopes,omitempty"`
	ProviderAccountID string     `json:"provider_account_id,omitempty"`
}

func (s *Service) seal(ctx context.Context, p payload) (json.RawMessage, error) {
	if s.enc == nil {
		return nil, fmt.Errorf("%w: no credential encryption key is configured", ErrInvalid)
	}
	plain, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: encode secret", ErrInvalid)
	}
	sealed, err := s.enc.Encrypt(ctx, plain)
	clear(plain)
	if err != nil {
		return nil, fmt.Errorf("credential: encrypt secret: %w", err)
	}
	data, err := json.Marshal(envelope{Version: 1, Ciphertext: base64.StdEncoding.EncodeToString(sealed)})
	if err != nil {
		return nil, fmt.Errorf("credential: encode envelope: %w", err)
	}
	return data, nil
}

func (s *Service) open(ctx context.Context, c Credential) (payload, error) {
	if s.enc == nil {
		return payload{}, fmt.Errorf("%w: no credential encryption key is configured", ErrDecryptionFailed)
	}
	var env envelope
	if err := json.Unmarshal(c.EncryptedData, &env); err != nil || env.Version != 1 {
		return payload{}, fmt.Errorf("%w: credential %s has an unreadable envelope", ErrDecryptionFailed, c.ID)
	}
	sealed, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return payload{}, fmt.Errorf("%w: credential %s has an unreadable envelope", ErrDecryptionFailed, c.ID)
	}
	plain, err := s.enc.Decrypt(ctx, sealed)
	if err != nil {
		// Deliberately not wrapped: nothing about the ciphertext leaks.
		return payload{}, fmt.Errorf("%w: credential %s", ErrDecryptionFailed, c.ID)
	}
	var p payload
	err = json.Unmarshal(plain, &p)
	clear(plain)
	if err != nil {
		return payload{}, fmt.Errorf("%w: credential %s holds no usable secret", ErrInvalid, c.ID)
	}
	return p, nil
}

// Create encrypts the secret and stores the credential. OAUTH2 credentials
// are created with CreateOAuth.
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
	case in.Type == TypeOAuth2:
		return Credential{}, fmt.Errorf("%w: OAuth credentials are created by connecting an account", ErrInvalid)
	case in.Secret.Empty():
		return Credential{}, fmt.Errorf("%w: secret is required", ErrInvalid)
	case s.enc == nil:
		return Credential{}, fmt.Errorf("%w: no credential encryption key is configured", ErrInvalid)
	}
	data, err := s.seal(ctx, payload{Secret: in.Secret.Reveal()})
	if err != nil {
		return Credential{}, err
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

// CreateOAuthInput describes a new OAUTH2 credential.
type CreateOAuthInput struct {
	WorkspaceID uuid.UUID
	Name        string
	Provider    string
	Token       OAuthToken
}

func checkToken(t OAuthToken) error {
	if t.AccessToken.Empty() {
		return fmt.Errorf("%w: an OAuth credential needs an access token", ErrInvalid)
	}
	return nil
}

// CreateOAuth encrypts the token material and stores an OAUTH2 credential.
func (s *Service) CreateOAuth(ctx context.Context, in CreateOAuthInput) (Credential, error) {
	switch {
	case in.WorkspaceID == uuid.Nil:
		return Credential{}, fmt.Errorf("%w: workspace is required", ErrInvalid)
	case strings.TrimSpace(in.Name) == "":
		return Credential{}, fmt.Errorf("%w: name is required", ErrInvalid)
	case strings.TrimSpace(in.Provider) == "":
		return Credential{}, fmt.Errorf("%w: provider is required", ErrInvalid)
	}
	if err := checkToken(in.Token); err != nil {
		return Credential{}, err
	}
	data, err := s.seal(ctx, payload{OAuth: toPayload(in.Token)})
	if err != nil {
		return Credential{}, err
	}
	return s.repo.Create(ctx, Credential{
		ID:             uuid.New(),
		WorkspaceID:    in.WorkspaceID,
		Name:           strings.TrimSpace(in.Name),
		Provider:       strings.TrimSpace(in.Provider),
		CredentialType: TypeOAuth2,
		EncryptedData:  data,
	})
}

// ReplaceOAuth re-encrypts the OAuth material of the workspace's OAUTH2
// credential (refresh, reconnect). A nil token removes the material: the
// credential stays (workflows keep referencing its ID) but resolves to
// ErrRevoked until the account is connected again.
func (s *Service) ReplaceOAuth(ctx context.Context, workspaceID, id uuid.UUID, token *OAuthToken) error {
	c, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if c.CredentialType != TypeOAuth2 {
		return fmt.Errorf("%w: credential %s is not an OAuth credential", ErrInvalid, id)
	}
	p := payload{}
	if token != nil {
		if err := checkToken(*token); err != nil {
			return err
		}
		p.OAuth = toPayload(*token)
	}
	data, err := s.seal(ctx, p)
	if err != nil {
		return err
	}
	return s.repo.UpdateData(ctx, workspaceID, id, data)
}

// ResolveOAuth decrypts the OAuth material of the workspace's OAUTH2
// credential of provider (runtime memory only). It returns ErrRevoked when
// the material was removed.
func (s *Service) ResolveOAuth(ctx context.Context, workspaceID, id uuid.UUID, provider string) (Credential, OAuthToken, error) {
	c, err := s.load(ctx, workspaceID, id, provider)
	if err != nil {
		return Credential{}, OAuthToken{}, err
	}
	if c.CredentialType != TypeOAuth2 {
		return Credential{}, OAuthToken{}, fmt.Errorf("%w: credential %s is not an OAuth credential", ErrInvalid, id)
	}
	p, err := s.open(ctx, c)
	if err != nil {
		return Credential{}, OAuthToken{}, err
	}
	if p.OAuth == nil || p.OAuth.AccessToken == "" {
		return Credential{}, OAuthToken{}, fmt.Errorf("%w: credential %s is no longer connected", ErrRevoked, id)
	}
	return c, fromPayload(*p.OAuth), nil
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

// load is the shared lookup of Resolve and ResolveOAuth: the credential must
// belong to workspaceID and to provider.
func (s *Service) load(ctx context.Context, workspaceID, id uuid.UUID, provider string) (Credential, error) {
	if id == uuid.Nil {
		return Credential{}, fmt.Errorf("%w: credential id is required", ErrInvalid)
	}
	if workspaceID == uuid.Nil {
		return Credential{}, fmt.Errorf("%w: the execution has no workspace to resolve credentials in", ErrInvalid)
	}
	c, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return Credential{}, err
	}
	if c.Provider != provider {
		return Credential{}, fmt.Errorf("%w: credential %s is for provider %q, not %q", ErrProviderMismatch, id, c.Provider, provider)
	}
	return c, nil
}

// Resolve implements Resolver: the credential must belong to workspaceID and
// to provider; its secret is decrypted into runtime memory only. For an
// OAUTH2 credential the secret is the access token, returned only while it
// is valid (a token manager refreshes it otherwise).
func (s *Service) Resolve(ctx context.Context, workspaceID, id uuid.UUID, provider string) (ResolvedCredential, error) {
	c, err := s.load(ctx, workspaceID, id, provider)
	if err != nil {
		return ResolvedCredential{}, err
	}
	p, err := s.open(ctx, c)
	if err != nil {
		return ResolvedCredential{}, err
	}
	if c.CredentialType == TypeOAuth2 {
		if p.OAuth == nil || p.OAuth.AccessToken == "" {
			return ResolvedCredential{}, fmt.Errorf("%w: credential %s is no longer connected", ErrRevoked, id)
		}
		t := fromPayload(*p.OAuth)
		if t.Expired(s.now()) {
			return ResolvedCredential{}, fmt.Errorf("%w: the access token of credential %s has expired and cannot be refreshed here", ErrInvalid, id)
		}
		return ResolvedCredential{ID: c.ID, Provider: c.Provider, Type: c.CredentialType, Secret: t.AccessToken}, nil
	}
	if p.Secret == "" {
		return ResolvedCredential{}, fmt.Errorf("%w: credential %s holds no usable secret", ErrInvalid, id)
	}
	return ResolvedCredential{ID: c.ID, Provider: c.Provider, Type: c.CredentialType, Secret: NewSecret(p.Secret)}, nil
}

// IsCredentialError reports whether err is one of the credential errors.
func IsCredentialError(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrDecryptionFailed) ||
		errors.Is(err, ErrProviderMismatch) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrRevoked)
}
