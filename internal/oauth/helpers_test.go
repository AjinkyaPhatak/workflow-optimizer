package oauth_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/infrastructure/encryption"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/fake"
)

// memRepo is an in-memory credential.Repository (encrypted envelopes only).
type memRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]credential.Credential
}

func (m *memRepo) Create(_ context.Context, c credential.Credential) (credential.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[c.ID] = c
	return c, nil
}

func (m *memRepo) Get(_ context.Context, id uuid.UUID) (credential.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok {
		return credential.Credential{}, credential.ErrNotFound
	}
	return c, nil
}

func (m *memRepo) Delete(_ context.Context, ws, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	return nil
}

func (m *memRepo) ListByWorkspace(_ context.Context, ws uuid.UUID) ([]credential.Credential, error) {
	return nil, nil
}

func (m *memRepo) UpdateData(_ context.Context, ws, id uuid.UUID, data json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok || c.WorkspaceID != ws {
		return credential.ErrNotFound
	}
	c.EncryptedData = data
	m.rows[id] = c
	return nil
}

func (m *memRepo) raw(id uuid.UUID) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.rows[id])
	return string(b)
}

func newCredentialService(t *testing.T) (*credential.Service, *memRepo) {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	_, _ = rand.Read(key)
	enc, err := encryption.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := &memRepo{rows: map[uuid.UUID]credential.Credential{}}
	svc, err := credential.NewService(repo, enc)
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo
}

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const callbackURL = "https://app.example.test/api/v1/oauth/callback/fake_oauth"

func fakeProvider(c *clock) *fake.Provider {
	return fake.New(fake.Config(callbackURL)).WithClock(c.Now)
}

func registry(t *testing.T, ps ...oauth.Provider) *oauth.Registry {
	t.Helper()
	r, err := oauth.NewRegistry(ps...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var ada = oauth.Identity{ProviderAccountID: "acct-1001", Email: "ada@example.test", DisplayName: "Ada"}
