package whatsapp

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"workflow-optimizer/internal/infrastructure/encryption"
)

func TestEncryptedSQLStoreRestoresCommittedStateAndScopesSnapshots(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "wa_" + uuid.NewString()[:8]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE whatsapp_sessions(workspace_id UUID,user_id UUID,encrypted_data BYTEA,updated_at TIMESTAMPTZ DEFAULT now(),PRIMARY KEY(workspace_id,user_id))`); err != nil {
		t.Fatal(err)
	}
	enc, err := encryption.NewAESGCM(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ws, user := uuid.New(), uuid.New()
	connector := &snapshotConnector{pool: pool, encryptor: enc, workspace: ws, user: user}
	db := connector.DB()
	if _, err := db.ExecContext(ctx, "CREATE TABLE private_state(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO private_state(value) VALUES (?)", "secret-signal-key"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE private_state SET value='uncommitted'"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, "SELECT encrypted_data FROM whatsapp_sessions WHERE workspace_id=$1 AND user_id=$2", ws, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("secret-signal-key")) {
		t.Fatal("session secrets were stored in clear text")
	}
	restored := connector.DB()
	defer restored.Close()
	var value string
	if err := restored.QueryRowContext(ctx, "SELECT value FROM private_state").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "secret-signal-key" {
		t.Fatalf("committed state was not restored: %q", value)
	}
	other := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO whatsapp_sessions VALUES($1,$2,$3,now())", ws, other, stored); err != nil {
		t.Fatal(err)
	}
	wrong := (&snapshotConnector{pool: pool, encryptor: enc, workspace: ws, user: other}).DB()
	defer wrong.Close()
	if err := wrong.PingContext(ctx); err == nil {
		t.Fatal("an account accepted another account's snapshot")
	}
}
