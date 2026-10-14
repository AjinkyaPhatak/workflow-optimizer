package phasee

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/infrastructure/postgres"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	cfg config.Config
	rt  *app.APIRuntime
}

// newEnv starts the production API composition on a fresh schema.
func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith lets a test adjust the configuration first.
func newEnvWith(t *testing.T, adjust func(*config.Config)) *env {
	t.Helper()
	base, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if base == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase E tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phasee_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	_, file, _, _ := runtime.Caller(0)
	if err := postgres.ApplyMigrations(u.String(), filepath.Join(filepath.Dir(file), "..", "..", "migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	cfg := config.Config{
		DatabaseURL: u.String(), RedisURL: redisURL, RedisQueueName: "test:phasee:" + uuid.NewString(),
		WorkerCount: 1, WorkerShutdownTimeout: 5 * time.Second, Reliability: config.DefaultReliability(),
		CredentialEncryptionKey: base64.StdEncoding.EncodeToString(key), APIAddr: "127.0.0.1:0",
		AuthTokenSecret: base64.StdEncoding.EncodeToString(secret), AuthTokenTTL: time.Hour,
	}
	if adjust != nil {
		adjust(&cfg)
	}
	rt, err := app.NewAPIWith(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), app.Extensions{})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, srv: httptest.NewServer(rt.Handler()), cfg: cfg, rt: rt}
	t.Cleanup(func() { e.srv.Close(); rt.Close() })
	return e
}

type resp struct {
	status int
	body   []byte
	header http.Header
}

func (e *env) call(token, method, path string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, body: b, header: r.Header}
}

// upload posts a multipart file upload.
func (e *env) upload(token, workspace, name string, data []byte) resp {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if workspace != "" {
		_ = mw.WriteField("workspace_id", workspace)
	}
	if name != "" {
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, body: b, header: r.Header}
}

func (e *env) must(r resp, status int) map[string]any {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		e.t.Fatalf("decode %s: %v", r.body, err)
	}
	return m
}

type user struct{ token, workspace string }

func (e *env) register(name string) user {
	reg := e.must(e.call("", "POST", "/api/v1/auth/register", map[string]any{
		"email": name + "-" + uuid.NewString()[:8] + "@example.test", "name": name, "password": "password-" + name}), 201)
	return user{token: reg["access_token"].(string), workspace: reg["workspace"].(map[string]any)["id"].(string)}
}
