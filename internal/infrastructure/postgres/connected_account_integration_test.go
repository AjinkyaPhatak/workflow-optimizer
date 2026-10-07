package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/connectedaccount"
	"workflow-optimizer/internal/credential"
	postgresinfra "workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/oauth"
	oauthfake "workflow-optimizer/internal/oauth/fake"
)

const (
	pgAccess  = "fake-access-PG-PLAINTEXT-0001"
	pgRefresh = "fake-refresh-PG-PLAINTEXT-0001"
)

// ownerOf returns a user of the workspace (its owner).
func (p *pgEnv) ownerOf(t *testing.T, ws uuid.UUID) uuid.UUID {
	t.Helper()
	var u uuid.UUID
	if err := p.raw.QueryRow(context.Background(), "SELECT owner_id FROM workspaces WHERE id = $1", ws).Scan(&u); err != nil {
		t.Fatal(err)
	}
	return u
}

func pgToken(access, refresh string) credential.OAuthToken {
	return credential.OAuthToken{AccessToken: credential.NewSecret(access), RefreshToken: credential.NewSecret(refresh), TokenType: "Bearer",
		Expiry: time.Now().Add(time.Hour).UTC(), Scopes: []string{"records.read"}, ProviderAccountID: "acct-1"}
}

func TestPGConnectedAccountSchemaHoldsNoTokens(t *testing.T) {
	p := newPGEnv(t)
	rows, err := p.raw.Query(context.Background(), `SELECT table_name, column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name IN ('connected_accounts', 'credentials')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string][]string{}
	for rows.Next() {
		var table, col string
		_ = rows.Scan(&table, &col)
		cols[table] = append(cols[table], col)
		for _, bad := range []string{"token", "secret", "code", "password"} {
			if strings.Contains(col, bad) {
				t.Errorf("%s.%s looks like a plaintext secret column", table, col)
			}
		}
	}
	if len(cols["connected_accounts"]) != 13 || len(cols["credentials"]) != 8 {
		t.Fatalf("columns: %v", cols)
	}
}

func TestPGConnectedAccountConstraints(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	creds := credentialService(t, p)
	repo := postgresinfra.NewConnectedAccountRepository(p.store)
	wsA, wsB := p.workspaceOf(t), p.workspaceOf(t)
	userA := p.ownerOf(t, wsA)

	credA, err := creds.CreateOAuth(ctx, credential.CreateOAuthInput{WorkspaceID: wsA, Name: "Fake — a", Provider: oauthfake.ID, Token: pgToken(pgAccess, pgRefresh)})
	if err != nil {
		t.Fatal(err)
	}
	// Encrypted at rest: no column of the credential row holds a token.
	var rowText string
	if err := p.raw.QueryRow(ctx, "SELECT row_to_json(c)::text FROM credentials c WHERE id = $1", credA.ID).Scan(&rowText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rowText, "PLAINTEXT") || strings.Contains(rowText, "acct-1") {
		t.Fatalf("plaintext OAuth material in the credentials row: %s", rowText)
	}

	acct := connectedaccount.Account{WorkspaceID: wsA, UserID: userA, Provider: oauthfake.ID, ProviderAccountID: "acct-1",
		Email: "a@example.test", DisplayName: "A", CredentialID: credA.ID, Status: connectedaccount.StatusActive, Scopes: []string{"records.read"}}
	created, err := repo.Create(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == uuid.Nil || created.Status != connectedaccount.StatusActive || created.LastUsedAt != nil || len(created.Scopes) != 1 {
		t.Fatalf("created: %+v", created)
	}
	// Same external identity in the same workspace: duplicate.
	credA2, _ := creds.CreateOAuth(ctx, credential.CreateOAuthInput{WorkspaceID: wsA, Name: "dup", Provider: oauthfake.ID, Token: pgToken("x-access", "x-refresh")})
	dup := acct
	dup.CredentialID = credA2.ID
	if _, err := repo.Create(ctx, dup); !errors.Is(err, connectedaccount.ErrDuplicate) {
		t.Fatalf("duplicate identity: %v", err)
	}
	// One credential backs one account.
	other := acct
	other.ProviderAccountID = "acct-2"
	if _, err := repo.Create(ctx, other); !errors.Is(err, connectedaccount.ErrDuplicate) {
		t.Fatalf("shared credential: %v", err)
	}
	// The credential must be in the account's workspace (composite FK).
	cross := acct
	cross.WorkspaceID, cross.UserID, cross.CredentialID = wsB, p.ownerOf(t, wsB), credA2.ID
	if _, err := repo.Create(ctx, cross); !errors.Is(err, connectedaccount.ErrInvalid) {
		t.Fatalf("cross-workspace credential reference: %v", err)
	}
	// The same external account may be connected by another workspace.
	credB, _ := creds.CreateOAuth(ctx, credential.CreateOAuthInput{WorkspaceID: wsB, Name: "b", Provider: oauthfake.ID, Token: pgToken("b-access", "b-refresh")})
	inB := cross
	inB.CredentialID = credB.ID
	if _, err := repo.Create(ctx, inB); err != nil {
		t.Fatalf("same identity, other workspace: %v", err)
	}
	// Status check constraint.
	if _, err := p.raw.Exec(ctx, "UPDATE connected_accounts SET status = 'BROKEN' WHERE id = $1", created.ID); err == nil {
		t.Fatal("unknown status accepted")
	}
	// A credential backing an account cannot be deleted directly.
	if err := creds.Delete(ctx, wsA, credA.ID); !errors.Is(err, credential.ErrInvalid) {
		t.Fatalf("deleting a backing credential: %v", err)
	}

	// Lookups and listing are workspace-scoped.
	if got, err := repo.FindByIdentity(ctx, wsA, oauthfake.ID, "acct-1"); err != nil || got.ID != created.ID {
		t.Fatalf("FindByIdentity: %v", err)
	}
	if _, err := repo.FindByIdentity(ctx, wsA, oauthfake.ID, "nobody"); !errors.Is(err, connectedaccount.ErrNotFound) {
		t.Fatalf("missing identity: %v", err)
	}
	if l, _ := repo.ListByWorkspace(ctx, wsA); len(l) != 1 || l[0].ID != created.ID {
		t.Fatalf("list A: %+v", l)
	}
	if _, err := repo.Get(ctx, uuid.New()); !errors.Is(err, connectedaccount.ErrNotFound) {
		t.Fatal("unknown id")
	}

	// Status and use updates by credential; a disconnected account keeps
	// its status.
	if err := repo.SetStatusByCredential(ctx, credA.ID, connectedaccount.StatusRevoked); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.TouchByCredential(ctx, credA.ID, now); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, created.ID)
	if got.Status != connectedaccount.StatusRevoked || got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Fatalf("after status/touch: %+v", got)
	}
	got.Status = connectedaccount.StatusDisconnected
	if _, err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	_ = repo.SetStatusByCredential(ctx, credA.ID, connectedaccount.StatusActive)
	if got, _ := repo.Get(ctx, created.ID); got.Status != connectedaccount.StatusDisconnected {
		t.Fatal("a refresh must not revive a disconnected account")
	}
	// Update is workspace-scoped too.
	wrong := got
	wrong.WorkspaceID = wsB
	if _, err := repo.Update(ctx, wrong); !errors.Is(err, connectedaccount.ErrNotFound) {
		t.Fatalf("update through another workspace: %v", err)
	}
}

func TestPGConnectReconnectDisconnect(t *testing.T) {
	p := newPGEnv(t)
	ctx := context.Background()
	creds := credentialService(t, p)
	provider := oauthfake.New(oauthfake.Config("https://app.example.test/api/v1/oauth/callback/fake_oauth"))
	providers, err := oauth.NewRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	svc := connectedaccount.NewService(postgresinfra.NewConnectedAccountRepository(p.store), creds, providers, nil)
	ws := p.workspaceOf(t)
	user := p.ownerOf(t, ws)
	id := oauth.Identity{ProviderAccountID: "acct-77", Email: "grace@example.test", DisplayName: "Grace"}

	first, created, err := svc.Connect(ctx, connectedaccount.ConnectInput{WorkspaceID: ws, UserID: user, Provider: oauthfake.ID, Identity: id, Token: pgToken(pgAccess, pgRefresh)})
	if err != nil || !created {
		t.Fatalf("connect: %v", err)
	}
	c, err := creds.Get(ctx, ws, first.CredentialID)
	if err != nil || c.CredentialType != credential.TypeOAuth2 || c.Name != "Fake OAuth (test) — grace@example.test" || c.Provider != oauthfake.ID {
		t.Fatalf("credential: %+v %v", c, err)
	}

	// Reconnecting the same external account (even with a new email)
	// updates the same account and credential.
	id.Email = "grace.h@example.test"
	again, created, err := svc.Connect(ctx, connectedaccount.ConnectInput{WorkspaceID: ws, UserID: user, Provider: oauthfake.ID, Identity: id, Token: pgToken("fake-access-SECOND", "fake-refresh-SECOND")})
	if err != nil || created || again.ID != first.ID || again.CredentialID != first.CredentialID || again.Email != "grace.h@example.test" {
		t.Fatalf("reconnect: %+v created=%v %v", again, created, err)
	}
	if _, tok, _ := creds.ResolveOAuth(ctx, ws, first.CredentialID, oauthfake.ID); tok.AccessToken.Reveal() != "fake-access-SECOND" {
		t.Fatal("reconnect replaces the tokens")
	}
	var n int
	_ = p.raw.QueryRow(ctx, "SELECT count(*) FROM credentials WHERE workspace_id = $1", ws).Scan(&n)
	if n != 1 {
		t.Fatalf("reconnect must not create credentials: %d", n)
	}

	// Disconnect: tokens removed, status DISCONNECTED, credential kept.
	out, revoked, err := svc.Disconnect(ctx, again)
	if err != nil || out.Status != connectedaccount.StatusDisconnected {
		t.Fatalf("disconnect: %+v %v", out, err)
	}
	if _, _, rev := provider.Counts(); rev != 1 || !revoked {
		t.Fatal("the provider is asked to revoke")
	}
	if _, _, err := creds.ResolveOAuth(ctx, ws, first.CredentialID, oauthfake.ID); !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("tokens must be gone: %v", err)
	}
	if _, err := creds.Resolve(ctx, ws, first.CredentialID, oauthfake.ID); !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("no further use: %v", err)
	}
	var rowText string
	_ = p.raw.QueryRow(ctx, "SELECT row_to_json(c)::text FROM credentials c WHERE id = $1", first.CredentialID).Scan(&rowText)
	if strings.Contains(rowText, "SECOND") || strings.Contains(rowText, "PLAINTEXT") {
		t.Fatalf("plaintext after disconnect: %s", rowText)
	}

	// A revocation failure does not prevent disconnecting.
	back, _, err := svc.Connect(ctx, connectedaccount.ConnectInput{WorkspaceID: ws, UserID: user, Provider: oauthfake.ID, Identity: id, Token: pgToken("fake-access-THIRD", "fake-refresh-THIRD")})
	if err != nil || back.Status != connectedaccount.StatusActive || back.ID != first.ID {
		t.Fatalf("reconnect after disconnect: %+v %v", back, err)
	}
	provider.Fail = func(op string) error { return oauth.NewError(oauth.KindProviderUnavailable, "down") }
	if out, revoked, err := svc.Disconnect(ctx, back); err != nil || revoked || out.Status != connectedaccount.StatusDisconnected {
		t.Fatalf("disconnect with provider down: %+v %v %v", out, revoked, err)
	}

	// Status reports from the token manager.
	back, _, _ = svc.Connect(ctx, connectedaccount.ConnectInput{WorkspaceID: ws, UserID: user, Provider: oauthfake.ID, Identity: id, Token: pgToken("fake-access-4", "fake-refresh-4")})
	svc.AuthorizationLost(ctx, back.CredentialID, oauth.KindTokenExpired)
	if got, _ := svc.Get(ctx, back.ID); got.Status != connectedaccount.StatusExpired {
		t.Fatalf("expired: %s", got.Status)
	}
	svc.TokenRefreshed(ctx, back.CredentialID)
	svc.CredentialUsed(ctx, back.CredentialID)
	if got, _ := svc.Get(ctx, back.ID); got.Status != connectedaccount.StatusActive || got.LastUsedAt == nil {
		t.Fatalf("active again: %+v", got)
	}

	// Invalid identities are refused.
	if _, _, err := svc.Connect(ctx, connectedaccount.ConnectInput{WorkspaceID: ws, UserID: user, Provider: oauthfake.ID, Identity: oauth.Identity{Email: "x@example.test"}, Token: pgToken("a", "b")}); !errors.Is(err, connectedaccount.ErrInvalid) {
		t.Fatalf("no provider account id: %v", err)
	}
}
