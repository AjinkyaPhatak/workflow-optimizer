package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/encryption"
	postgresinfra "workflow-optimizer/internal/infrastructure/postgres"
)

const pgSecret = "sk-pg-integration-SECRET-31337"

// workspaceOf seeds a workflow and returns its workspace (through the
// repository's own lookup).
func (p *pgEnv) workspaceOf(t *testing.T) uuid.UUID {
	t.Helper()
	wf, _ := p.seedWorkflow(t, nil)
	ws, err := postgresinfra.NewWorkflowVersionRepository(p.store).WorkspaceOf(context.Background(), wf)
	if err != nil || ws == uuid.Nil {
		t.Fatalf("workspace of workflow: %s %v", ws, err)
	}
	return ws
}

func credentialService(t *testing.T, p *pgEnv) *credential.Service {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	_, _ = rand.Read(key)
	enc, err := encryption.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := credential.NewService(postgresinfra.NewCredentialRepository(p.store), enc)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestPGCredentialPersistenceEncryptionAndIsolation(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	svc := credentialService(t, p)
	wsA, wsB := p.workspaceOf(t), p.workspaceOf(t)

	c, err := svc.Create(ctx, credential.CreateInput{WorkspaceID: wsA, Name: "OpenAI prod", Provider: "openai", Type: credential.TypeAPIKey, Secret: credential.NewSecret(pgSecret)})
	if err != nil {
		t.Fatal(err)
	}
	// Encrypted at rest: no column of the row contains the secret.
	var rowText string
	if err := p.raw.QueryRow(ctx, "SELECT row_to_json(c)::text FROM credentials c WHERE id = $1", c.ID).Scan(&rowText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rowText, pgSecret) || strings.Contains(rowText, "SECRET-31337") || !strings.Contains(rowText, `"ciphertext"`) {
		t.Fatalf("stored row = %s", rowText)
	}
	// Get / List return the encrypted record only.
	got, err := svc.Get(ctx, wsA, c.ID)
	if err != nil || got.Name != "OpenAI prod" || got.Provider != "openai" || got.CredentialType != credential.TypeAPIKey || got.CreatedAt.IsZero() {
		t.Fatalf("get = %+v %v", got, err)
	}
	list, err := svc.List(ctx, wsA)
	if err != nil || len(list) != 1 || list[0].ID != c.ID || strings.Contains(string(list[0].EncryptedData), pgSecret) {
		t.Fatalf("list = %+v %v", list, err)
	}
	if other, _ := svc.List(ctx, wsB); len(other) != 0 {
		t.Fatalf("workspace B lists A's credential: %+v", other)
	}
	// Resolution decrypts in the owning workspace only.
	r, err := svc.Resolve(ctx, wsA, c.ID, "openai")
	if err != nil || r.Secret.Reveal() != pgSecret {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := svc.Resolve(ctx, wsB, c.ID, "openai"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("cross-workspace resolve: %v", err)
	}
	if _, err := svc.Resolve(ctx, wsA, c.ID, "anthropic"); !errors.Is(err, credential.ErrProviderMismatch) {
		t.Fatalf("provider mismatch: %v", err)
	}
	if _, err := svc.Resolve(ctx, wsA, uuid.New(), "openai"); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// A different master key cannot decrypt.
	if _, err := credentialService(t, p).Resolve(ctx, wsA, c.ID, "openai"); !errors.Is(err, credential.ErrDecryptionFailed) {
		t.Fatalf("wrong key: %v", err)
	}
	// Delete is workspace-scoped.
	if err := svc.Delete(ctx, wsB, c.ID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("cross-workspace delete: %v", err)
	}
	if err := svc.Delete(ctx, wsA, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, wsA, c.ID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	// Unknown workspace / workflow.
	if _, err := svc.Create(ctx, credential.CreateInput{WorkspaceID: uuid.New(), Name: "x", Provider: "openai", Type: credential.TypeAPIKey, Secret: credential.NewSecret("x")}); !errors.Is(err, credential.ErrInvalid) {
		t.Fatalf("create in unknown workspace: %v", err)
	}
	if _, err := postgresinfra.NewWorkflowVersionRepository(p.store).WorkspaceOf(ctx, uuid.New()); !errors.Is(err, execution.ErrWorkflowNotFound) {
		t.Fatalf("workspace of unknown workflow: %v", err)
	}
}
