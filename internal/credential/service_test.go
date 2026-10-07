package credential_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/infrastructure/encryption"
)

// memRepo is an in-memory credential.Repository.
type memRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]credential.Credential
}

func newMemRepo() *memRepo { return &memRepo{rows: map[uuid.UUID]credential.Credential{}} }

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
		return credential.Credential{}, fmt.Errorf("%w: %s", credential.ErrNotFound, id)
	}
	return c, nil
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

func (m *memRepo) Delete(_ context.Context, ws, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.rows[id]; !ok || c.WorkspaceID != ws {
		return credential.ErrNotFound
	}
	delete(m.rows, id)
	return nil
}

func (m *memRepo) ListByWorkspace(_ context.Context, ws uuid.UUID) ([]credential.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []credential.Credential
	for _, c := range m.rows {
		if c.WorkspaceID == ws {
			out = append(out, c)
		}
	}
	return out, nil
}

func newEncryptor(t *testing.T) *encryption.AESGCM {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	_, _ = rand.Read(key)
	a, err := encryption.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

const secret = "sk-proj-SUPERSECRET-0123456789"

func TestServiceCreateResolveAndIsolation(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo()
	svc, err := credential.NewService(repo, newEncryptor(t))
	if err != nil {
		t.Fatal(err)
	}
	wsA, wsB := uuid.New(), uuid.New()
	c, err := svc.Create(ctx, credential.CreateInput{WorkspaceID: wsA, Name: "OpenAI", Provider: "openai", Type: credential.TypeAPIKey, Secret: credential.NewSecret(secret)})
	if err != nil {
		t.Fatal(err)
	}
	// Encrypted at rest: the stored envelope does not contain the secret.
	if strings.Contains(string(repo.rows[c.ID].EncryptedData), secret) || strings.Contains(string(repo.rows[c.ID].EncryptedData), "SUPERSECRET") {
		t.Fatal("secret stored in plaintext")
	}
	// Get/List return the encrypted record only.
	got, err := svc.Get(ctx, wsA, c.ID)
	if err != nil || got.Name != "OpenAI" || strings.Contains(fmt.Sprintf("%+v", got), secret) {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, _ := svc.List(ctx, wsA)
	if b, _ := json.Marshal(list); len(list) != 1 || strings.Contains(string(b), secret) {
		t.Fatalf("list leaks or is wrong: %s", b)
	}

	r, err := svc.Resolve(ctx, wsA, c.ID, "openai")
	if err != nil || r.Secret.Reveal() != secret || r.Provider != "openai" || r.Type != credential.TypeAPIKey {
		t.Fatalf("resolve: %+v %v", r, err)
	}
	// The resolved secret never prints or marshals.
	b, _ := json.Marshal(r)
	if s := fmt.Sprintf("%v %+v %#v %s", r, r, r, b); strings.Contains(s, secret) {
		t.Fatalf("resolved credential leaks when formatted: %s", s)
	}

	// Workspace isolation: another workspace cannot see, resolve or delete it.
	if _, err := svc.Resolve(ctx, wsB, c.ID, "openai"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("cross-workspace resolve: %v", err)
	}
	if _, err := svc.Get(ctx, wsB, c.ID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("cross-workspace get: %v", err)
	}
	if err := svc.Delete(ctx, wsB, c.ID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("cross-workspace delete: %v", err)
	}
	if _, err := svc.Resolve(ctx, uuid.Nil, c.ID, "openai"); !errors.Is(err, credential.ErrInvalid) {
		t.Fatalf("resolve without workspace: %v", err)
	}
	// Provider mismatch, missing credential.
	if _, err := svc.Resolve(ctx, wsA, c.ID, "anthropic"); !errors.Is(err, credential.ErrProviderMismatch) {
		t.Fatalf("provider mismatch: %v", err)
	}
	if _, err := svc.Resolve(ctx, wsA, uuid.New(), "openai"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// Delete.
	if err := svc.Delete(ctx, wsA, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, wsA, c.ID, "openai"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("resolve after delete: %v", err)
	}
}

func TestServiceDecryptionFailures(t *testing.T) {
	ctx := context.Background()
	repo := newMemRepo()
	svc, _ := credential.NewService(repo, newEncryptor(t))
	ws := uuid.New()
	c, err := svc.Create(ctx, credential.CreateInput{WorkspaceID: ws, Name: "k", Provider: "openai", Type: credential.TypeAPIKey, Secret: credential.NewSecret(secret)})
	if err != nil {
		t.Fatal(err)
	}
	// Another key (rotated or wrong master key).
	other, _ := credential.NewService(repo, newEncryptor(t))
	_, err = other.Resolve(ctx, ws, c.ID, "openai")
	if !errors.Is(err, credential.ErrDecryptionFailed) || strings.Contains(err.Error(), string(repo.rows[c.ID].EncryptedData)) {
		t.Fatalf("wrong key: %v", err)
	}
	// Tampered ciphertext.
	stored := repo.rows[c.ID]
	var env map[string]any
	_ = json.Unmarshal(stored.EncryptedData, &env)
	ct := []byte(env["ciphertext"].(string))
	ct[len(ct)/2] ^= 0x01
	env["ciphertext"] = string(ct)
	stored.EncryptedData, _ = json.Marshal(env)
	repo.rows[c.ID] = stored
	if _, err := svc.Resolve(ctx, ws, c.ID, "openai"); !errors.Is(err, credential.ErrDecryptionFailed) {
		t.Fatalf("tampered: %v", err)
	}
	// No key configured.
	nokey, _ := credential.NewService(repo, nil)
	if _, err := nokey.Resolve(ctx, ws, c.ID, "openai"); !errors.Is(err, credential.ErrDecryptionFailed) {
		t.Fatalf("no key resolve: %v", err)
	}
	if _, err := nokey.Create(ctx, credential.CreateInput{WorkspaceID: ws, Name: "k", Provider: "openai", Type: credential.TypeAPIKey, Secret: credential.NewSecret("x")}); !errors.Is(err, credential.ErrInvalid) {
		t.Fatalf("no key create: %v", err)
	}
}

func TestServiceCreateValidation(t *testing.T) {
	svc, _ := credential.NewService(newMemRepo(), newEncryptor(t))
	ok := credential.CreateInput{WorkspaceID: uuid.New(), Name: "k", Provider: "openai", Type: credential.TypeAPIKey, Secret: credential.NewSecret("x")}
	for name, mutate := range map[string]func(*credential.CreateInput){
		"workspace": func(in *credential.CreateInput) { in.WorkspaceID = uuid.Nil },
		"name":      func(in *credential.CreateInput) { in.Name = " " },
		"provider":  func(in *credential.CreateInput) { in.Provider = "" },
		"type":      func(in *credential.CreateInput) { in.Type = "PASSWORD" },
		"secret":    func(in *credential.CreateInput) { in.Secret = credential.Secret{} },
	} {
		in := ok
		mutate(&in)
		if _, err := svc.Create(context.Background(), in); !errors.Is(err, credential.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
