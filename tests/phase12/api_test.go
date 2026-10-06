package phase12_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/queue"
)

// TestEndToEndExecutionThroughAPI is the Phase 12 acceptance path:
//
//	HTTP POST /execute -> PostgreSQL (PENDING) -> Redis job -> worker ->
//	graph executor -> nodes -> PostgreSQL -> HTTP GET /executions/{id}
func TestEndToEndExecutionThroughAPI(t *testing.T) {
	e := newEnv(t)
	a := e.register("Ada")

	project := e.project(a)
	wf := e.workflow(a, project, "Explainer")
	v := e.version(a, wf, validDefinition())
	if v["status"] != "DRAFT" || v["version_number"].(float64) != 1 {
		t.Fatalf("version = %v", v)
	}
	vid := v["id"].(string)
	val := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+vid+"/validate", nil), 200)
	if val["valid"] != true || len(val["errors"].([]any)) != 0 || val["warnings"] == nil {
		t.Fatalf("validate = %v", val)
	}
	pub := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+vid+"/publish", nil), 200)
	if pub["status"] != "PUBLISHED" || pub["published_at"] == nil {
		t.Fatalf("publish = %v", pub)
	}
	if got := e.must(e.call(a.token, "GET", "/api/v1/workflows/"+wf, nil), 200); got["active_version_id"] != vid {
		t.Fatalf("active version = %v", got["active_version_id"])
	}

	// No worker is running yet: the request path only persists and enqueues.
	start := time.Now()
	r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{
		"version_id": vid, "input": map[string]any{"query": "Explain quantum computing"}})
	acc := e.must(r, 202)
	if acc["status"] != "PENDING" || time.Since(start) > 5*time.Second {
		t.Fatalf("execute = %v after %s", acc, time.Since(start))
	}
	id := acc["execution_id"].(string)
	if loc := r.header.Get("Location"); loc != "/api/v1/executions/"+id {
		t.Fatalf("Location = %q", loc)
	}
	// Persisted PENDING in PostgreSQL, nothing executed.
	if s := e.sqlString(`SELECT status FROM executions WHERE id = $1`, id); s != "PENDING" {
		t.Fatalf("db status = %s", s)
	}
	if n := e.sqlString(`SELECT count(*)::text FROM node_executions WHERE execution_id = $1`, id); n != "0" {
		t.Fatalf("%s node records before any worker ran", n)
	}
	// The Redis job carries the execution ID of the persisted row.
	jobs, err := e.rawRedis.LRange(context.Background(), e.cfg.RedisQueueName, 0, -1).Result()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("queue = %v %v", jobs, err)
	}
	job, err := queue.DecodeJob([]byte(jobs[0]))
	if err != nil || job.ExecutionID.String() != id {
		t.Fatalf("job = %+v %v", job, err)
	}
	pending := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id, nil), 200)
	if pending["status"] != "PENDING" || pending["started_at"] != nil || pending["output"] != nil {
		t.Fatalf("pending = %v", pending)
	}

	// A worker picks the job up asynchronously.
	e.startWorkers()
	done, _ := e.waitStatus(a, id, 30*time.Second)
	if done["status"] != "COMPLETED" || done["error"] != nil || done["started_at"] == nil || done["completed_at"] == nil ||
		done["workflow_id"] != wf || done["workflow_version_id"] != vid {
		t.Fatalf("completed = %v", done)
	}
	out, _ := json.Marshal(done["output"])
	if !strings.Contains(string(out), "Explain quantum computing") {
		t.Fatalf("output = %s", out)
	}
	if input, _ := json.Marshal(done["input"]); !strings.Contains(string(input), "quantum") {
		t.Fatalf("input = %s", input)
	}
	if h := e.history(id); strings.Join(h, ",") != "NULL->PENDING,PENDING->RUNNING,RUNNING->COMPLETED" {
		t.Fatalf("lifecycle = %v", h)
	}
	for _, internal := range []string{"claim_token", "lease", "legacy"} {
		if strings.Contains(string(e.call(a.token, "GET", "/api/v1/executions/"+id, nil).body), internal) {
			t.Fatalf("execution response exposes %s", internal)
		}
	}

	nodes := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id+"/nodes", nil), 200)["items"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("nodes = %v", nodes)
	}
	seen := map[string]bool{}
	for _, raw := range nodes {
		n := raw.(map[string]any)
		seen[n["node_id"].(string)] = true
		if n["status"] != "COMPLETED" || n["started_at"] == nil || n["completed_at"] == nil || n["duration_ms"] == nil || n["attempt"].(float64) != 1 {
			t.Fatalf("node = %v", n)
		}
		if _, ok := n["input"]; ok {
			t.Fatalf("node response exposes input: %v", n)
		}
		if _, ok := n["output"]; ok {
			t.Fatalf("node response exposes output: %v", n)
		}
	}
	if !seen["in"] || !seen["shape"] || !seen["out"] || seen["x"] {
		t.Fatalf("node ids = %v", seen)
	}

	// Executing with the active version by default.
	again := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{"query": "again"}}), 202)
	if m, _ := e.waitStatus(a, again["execution_id"].(string), 30*time.Second); m["status"] != "COMPLETED" || m["workflow_version_id"] != vid {
		t.Fatalf("default-version run = %v", m)
	}
	// Finished executions cannot be cancelled.
	if r := e.call(a.token, "POST", "/api/v1/executions/"+id+"/cancel", nil); r.status != 409 || r.errCode(t) != "EXECUTION_FINISHED" {
		t.Fatalf("cancel finished: %d %s", r.status, r.body)
	}
}

func TestWorkflowCRUD(t *testing.T) {
	e := newEnv(t)
	a := e.register("Grace")
	project := e.project(a)
	first := e.workflow(a, project, "First")
	second := e.must(e.call(a.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Second", "description": "two"}), 201)
	if second["description"] != "two" || second["active_version_id"] != nil {
		t.Fatalf("create = %v", second)
	}
	// Creating workflow metadata executes nothing.
	if n := e.sqlString(`SELECT count(*)::text FROM executions`); n != "0" {
		t.Fatalf("%s executions after create", n)
	}

	page := e.must(e.call(a.token, "GET", "/api/v1/workflows?project_id="+project+"&page=1&page_size=1", nil), 200)
	if page["total"].(float64) != 2 || page["page_size"].(float64) != 1 || len(page["items"].([]any)) != 1 {
		t.Fatalf("page 1 = %v", page)
	}
	page2 := e.must(e.call(a.token, "GET", "/api/v1/workflows?project_id="+project+"&page=2&page_size=1", nil), 200)
	if page2["items"].([]any)[0].(map[string]any)["id"] == page["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("pages overlap")
	}

	got := e.must(e.call(a.token, "GET", "/api/v1/workflows/"+first, nil), 200)
	if got["name"] != "First" || got["project_id"] != project {
		t.Fatalf("get = %v", got)
	}
	upd := e.must(e.call(a.token, "PATCH", "/api/v1/workflows/"+second["id"].(string), map[string]any{"name": "Renamed", "description": ""}), 200)
	if upd["name"] != "Renamed" || upd["description"] != nil {
		t.Fatalf("update = %v", upd)
	}
	if r := e.call(a.token, "PATCH", "/api/v1/workflows/"+first, map[string]any{}); r.status != 400 || r.errCode(t) != "INVALID_REQUEST" {
		t.Fatalf("empty patch: %d %s", r.status, r.body)
	}
	if r := e.call(a.token, "POST", "/api/v1/workflows", `{"project_id": "`+project+`", "name": `); r.status != 400 {
		t.Fatalf("malformed: %d", r.status)
	}
	if r := e.call(a.token, "POST", "/api/v1/workflows", map[string]any{"project_id": uuid.NewString(), "name": "x"}); r.status != 404 || r.errCode(t) != "PROJECT_NOT_FOUND" {
		t.Fatalf("unknown project: %d %s", r.status, r.body)
	}
	if r := e.call(a.token, "GET", "/api/v1/workflows/"+uuid.NewString(), nil); r.status != 404 || r.errCode(t) != "WORKFLOW_NOT_FOUND" {
		t.Fatalf("nonexistent: %d %s", r.status, r.body)
	}

	// Delete keeps history: an execution of the workflow stays readable.
	wf, _ := e.published(a)
	ex := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{}), 202)["execution_id"].(string)
	e.must(e.call(a.token, "DELETE", "/api/v1/workflows/"+wf, nil), 204)
	if r := e.call(a.token, "GET", "/api/v1/workflows/"+wf, nil); r.status != 404 {
		t.Fatalf("deleted workflow: %d", r.status)
	}
	if r := e.call(a.token, "DELETE", "/api/v1/workflows/"+wf, nil); r.status != 404 {
		t.Fatalf("second delete: %d", r.status)
	}
	if r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{}); r.status != 404 {
		t.Fatalf("execute deleted: %d", r.status)
	}
	if m := e.must(e.call(a.token, "GET", "/api/v1/executions/"+ex, nil), 200); m["workflow_id"] != wf {
		t.Fatalf("execution after delete = %v", m)
	}
	if n := e.sqlString(`SELECT count(*)::text FROM workflows WHERE id = $1`, wf); n != "1" {
		t.Fatal("workflow row was removed")
	}
	list := e.must(e.call(a.token, "GET", "/api/v1/workflows?project_id="+project, nil), 200)
	if list["total"].(float64) != 2 {
		t.Fatalf("list after delete in another project = %v", list["total"])
	}
}

func TestVersionsValidationAndPublishing(t *testing.T) {
	e := newEnv(t)
	a := e.register("Linus")
	wf := e.workflow(a, e.project(a), "Versions")

	// Invalid definitions are rejected before anything is stored.
	bad := validDefinition()
	bad["nodes"] = append(bad["nodes"].([]any), map[string]any{"id": "x", "type": "no.such.node", "name": "X", "position": map[string]any{"x": 0, "y": 0}, "config": map[string]any{}})
	r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": bad})
	if r.status != 422 || r.errCode(t) != "VALIDATION_ERROR" || !strings.Contains(string(r.body), "INVALID_NODE_TYPE") {
		t.Fatalf("unknown node type: %d %s", r.status, r.body)
	}
	secretCfg := validDefinition()
	secretCfg["nodes"].([]any)[1].(map[string]any)["config"] = map[string]any{"api_key": "sk-not-allowed"}
	if r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": secretCfg}); r.status != 422 || !strings.Contains(string(r.body), "INVALID_NODE_CONFIG") {
		t.Fatalf("secret in config: %d %s", r.status, r.body)
	}
	badPorts := validDefinition()
	badPorts["edges"].([]any)[0].(map[string]any)["target_port"] = "nope"
	if r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": badPorts}); r.status != 422 || !strings.Contains(string(r.body), "INVALID_TARGET_PORT") {
		t.Fatalf("bad port: %d %s", r.status, r.body)
	}
	if r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": map[string]any{"version": 1, "nodes": []any{}, "edges": []any{}, "settings": map[string]any{}, "extra": 1}}); r.status != 400 {
		t.Fatalf("unknown definition field: %d %s", r.status, r.body)
	}
	if n := e.sqlString(`SELECT count(*)::text FROM workflow_versions WHERE workflow_id = $1`, wf); n != "0" {
		t.Fatalf("%s versions stored from invalid definitions", n)
	}

	// A draft may be incomplete (no output node) but cannot be published.
	draft := validDefinition()
	draft["nodes"] = draft["nodes"].([]any)[:2]
	draft["edges"] = draft["edges"].([]any)[:1]
	d := e.version(a, wf, draft)
	did := d["id"].(string)
	val := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+did+"/validate", nil), 200)
	if val["valid"] != false || !strings.Contains(string(mustJSON(val["errors"])), "MISSING_OUTPUT_NODE") {
		t.Fatalf("validate draft = %v", val)
	}
	if r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+did+"/publish", nil); r.status != 422 || r.errCode(t) != "VALIDATION_ERROR" {
		t.Fatalf("publish invalid: %d %s", r.status, r.body)
	}
	if s := e.sqlString(`SELECT status FROM workflow_versions WHERE id = $1`, did); s != "DRAFT" {
		t.Fatalf("invalid version became %s", s)
	}
	if got := e.must(e.call(a.token, "GET", "/api/v1/workflows/"+wf, nil), 200); got["active_version_id"] != nil {
		t.Fatalf("active version after failed publish = %v", got["active_version_id"])
	}
	// Executing an unpublished version is refused.
	if r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"version_id": did}); r.status != 409 || r.errCode(t) != "VERSION_NOT_PUBLISHED" {
		t.Fatalf("execute draft: %d %s", r.status, r.body)
	}
	if r := e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{}); r.status != 409 || r.errCode(t) != "NO_ACTIVE_VERSION" {
		t.Fatalf("execute without active version: %d %s", r.status, r.body)
	}

	v2 := e.version(a, wf, validDefinition())
	vid := v2["id"].(string)
	if v2["version_number"].(float64) != 2 {
		t.Fatalf("version number = %v", v2["version_number"])
	}
	pub := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+vid+"/publish", nil), 200)
	if pub["status"] != "PUBLISHED" {
		t.Fatalf("publish = %v", pub)
	}
	if got := e.must(e.call(a.token, "GET", "/api/v1/workflows/"+wf, nil), 200); got["active_version_id"] != vid {
		t.Fatalf("active version = %v", got["active_version_id"])
	}
	// Re-publishing is idempotent and keeps the publication time.
	again := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+vid+"/publish", nil), 200)
	if again["published_at"] != pub["published_at"] {
		t.Fatalf("published_at moved: %v -> %v", pub["published_at"], again["published_at"])
	}
	// Published versions are immutable: there is no update route, and the
	// database refuses a direct change.
	if r := e.call(a.token, "PATCH", "/api/v1/workflows/"+wf+"/versions/"+vid, map[string]any{"definition": map[string]any{}}); r.status != 405 {
		t.Fatalf("version update route: %d", r.status)
	}
	if _, err := e.raw.Exec(context.Background(), `UPDATE workflow_versions SET definition = '{}' WHERE id = $1`, vid); err == nil {
		t.Fatal("published definition was modified")
	}
	if r := e.call(a.token, "DELETE", "/api/v1/workflows/"+wf+"/versions/"+vid, nil); r.status != 405 {
		t.Fatalf("version delete route: %d", r.status)
	}

	list := e.must(e.call(a.token, "GET", "/api/v1/workflows/"+wf+"/versions", nil), 200)
	items := list["items"].([]any)
	if list["total"].(float64) != 2 || len(items) != 2 || items[0].(map[string]any)["id"] != vid {
		t.Fatalf("versions = %v", list)
	}
	if _, ok := items[0].(map[string]any)["definition"]; ok {
		t.Fatal("version list includes definitions")
	}
	full := e.must(e.call(a.token, "GET", "/api/v1/workflows/"+wf+"/versions/"+vid, nil), 200)
	if len(full["definition"].(map[string]any)["nodes"].([]any)) != 3 {
		t.Fatalf("version definition = %v", full["definition"])
	}
	if r := e.call(a.token, "GET", "/api/v1/workflows/"+wf+"/versions/"+uuid.NewString(), nil); r.status != 404 || r.errCode(t) != "WORKFLOW_VERSION_NOT_FOUND" {
		t.Fatalf("missing version: %d %s", r.status, r.body)
	}
	// A version is only reachable through its own workflow.
	other := e.workflow(a, e.project(a), "Other")
	if r := e.call(a.token, "GET", "/api/v1/workflows/"+other+"/versions/"+vid, nil); r.status != 404 {
		t.Fatalf("version through another workflow: %d", r.status)
	}
}

func TestAuthenticationAndAuthorization(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("Alice"), e.register("Bob")

	// Login.
	email := e.sqlString(`SELECT email FROM users WHERE id = $1`, a.userID)
	if r := e.call("", "POST", "/api/v1/auth/login", map[string]any{"email": email, "password": "wrong-password"}); r.status != 401 || r.errCode(t) != "UNAUTHENTICATED" {
		t.Fatalf("wrong password: %d %s", r.status, r.body)
	}
	unknown := e.call("", "POST", "/api/v1/auth/login", map[string]any{"email": "nobody@example.test", "password": "whatever-1"})
	if unknown.status != 401 || unknown.errCode(t) != "UNAUTHENTICATED" {
		t.Fatalf("unknown email: %d %s", unknown.status, unknown.body)
	}
	login := e.must(e.call("", "POST", "/api/v1/auth/login", map[string]any{"email": strings.ToUpper(email), "password": "password-Alice"}), 200)
	if login["token_type"] != "Bearer" || login["access_token"] == "" {
		t.Fatalf("login = %v", login)
	}
	if r := e.call("", "POST", "/api/v1/auth/register", map[string]any{"email": email, "name": "Again", "password": "password-1"}); r.status != 409 || r.errCode(t) != "EMAIL_TAKEN" {
		t.Fatalf("duplicate email: %d %s", r.status, r.body)
	}
	me := e.must(e.call(login["access_token"].(string), "GET", "/api/v1/auth/me", nil), 200)
	if ws := me["workspaces"].([]any); len(ws) != 1 || ws[0].(map[string]any)["id"] != a.workspace || ws[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("me = %v", me)
	}
	if strings.Contains(string(e.call(a.token, "GET", "/api/v1/auth/me", nil).body), "password") {
		t.Fatal("password hash exposed")
	}
	if r := e.call("", "GET", "/api/v1/auth/me", nil); r.status != 401 {
		t.Fatalf("me without token: %d", r.status)
	}

	// A's resources.
	project := e.project(a)
	wf, vid := e.published(a)
	ex := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{}), 202)["execution_id"].(string)
	cred := e.must(e.call(a.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": a.workspace, "name": "OpenAI",
		"provider": "openai", "credential_type": "api_key", "secret": "sk-alice-secret"}), 201)["id"].(string)

	// B knows every UUID but is not a member of A's workspace: everything is
	// "not found", nothing changes.
	for _, c := range []struct{ method, path, code string }{
		{"GET", "/api/v1/workflows/" + wf, "WORKFLOW_NOT_FOUND"},
		{"PATCH", "/api/v1/workflows/" + wf, "WORKFLOW_NOT_FOUND"},
		{"DELETE", "/api/v1/workflows/" + wf, "WORKFLOW_NOT_FOUND"},
		{"GET", "/api/v1/workflows/" + wf + "/versions", "WORKFLOW_NOT_FOUND"},
		{"GET", "/api/v1/workflows/" + wf + "/versions/" + vid, "WORKFLOW_NOT_FOUND"},
		{"POST", "/api/v1/workflows/" + wf + "/versions", "WORKFLOW_NOT_FOUND"},
		{"POST", "/api/v1/workflows/" + wf + "/versions/" + vid + "/validate", "WORKFLOW_NOT_FOUND"},
		{"POST", "/api/v1/workflows/" + wf + "/versions/" + vid + "/publish", "WORKFLOW_NOT_FOUND"},
		{"POST", "/api/v1/workflows/" + wf + "/execute", "WORKFLOW_NOT_FOUND"},
		{"GET", "/api/v1/workflows?project_id=" + project, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/projects/" + project, "PROJECT_NOT_FOUND"},
		{"GET", "/api/v1/projects?workspace_id=" + a.workspace, "WORKSPACE_NOT_FOUND"},
		{"GET", "/api/v1/executions/" + ex, "EXECUTION_NOT_FOUND"},
		{"GET", "/api/v1/executions/" + ex + "/nodes", "EXECUTION_NOT_FOUND"},
		{"POST", "/api/v1/executions/" + ex + "/cancel", "EXECUTION_NOT_FOUND"},
		{"GET", "/api/v1/credentials?workspace_id=" + a.workspace, "WORKSPACE_NOT_FOUND"},
		{"DELETE", "/api/v1/credentials/" + cred, "CREDENTIAL_NOT_FOUND"},
	} {
		var body any
		switch {
		case c.method == "PATCH":
			body = map[string]any{"name": "hijacked"}
		case strings.HasSuffix(c.path, "/versions") && c.method == "POST":
			body = map[string]any{"definition": validDefinition()}
		case strings.HasSuffix(c.path, "/execute"):
			body = map[string]any{}
		}
		if r := e.call(b.token, c.method, c.path, body); r.status != 404 || r.errCode(t) != c.code {
			t.Errorf("B %s %s: %d %s (want 404 %s)", c.method, c.path, r.status, r.body, c.code)
		}
	}
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/workflows", map[string]any{"project_id": project, "name": "intrusion"}},
		{"/api/v1/projects", map[string]any{"workspace_id": a.workspace, "name": "intrusion"}},
		{"/api/v1/credentials", map[string]any{"workspace_id": a.workspace, "name": "x", "provider": "openai", "credential_type": "api_key", "secret": "sk-x"}},
	} {
		if r := e.call(b.token, "POST", c.path, c.body); r.status != 404 {
			t.Errorf("B POST %s: %d %s", c.path, r.status, r.body)
		}
	}
	if n := e.sqlString(`SELECT name FROM workflows WHERE id = $1`, wf); n != "Published" {
		t.Fatalf("workflow renamed by B: %s", n)
	}
	if n := e.sqlString(`SELECT count(*)::text FROM executions`); n != "1" {
		t.Fatalf("B started executions: %s", n)
	}
	if n := e.sqlString(`SELECT count(*)::text FROM credentials`); n != "1" {
		t.Fatalf("credentials = %s", n)
	}

	// Roles inside a workspace: B becomes a MEMBER of A's workspace.
	if _, err := e.raw.Exec(context.Background(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, a.workspace, b.userID); err != nil {
		t.Fatal(err)
	}
	e.must(e.call(b.token, "GET", "/api/v1/workflows/"+wf, nil), 200)
	e.must(e.call(b.token, "GET", "/api/v1/executions/"+ex, nil), 200)
	e.must(e.call(b.token, "PATCH", "/api/v1/workflows/"+wf, map[string]any{"description": "edited by member"}), 200)
	draft := e.version(b, wf, validDefinition())["id"].(string)
	e.must(e.call(b.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{}), 202)
	listed := e.must(e.call(b.token, "GET", "/api/v1/credentials?workspace_id="+a.workspace, nil), 200)
	if len(listed["items"].([]any)) != 1 {
		t.Fatalf("member credential list = %v", listed)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/workflows/" + wf + "/versions/" + draft + "/publish", nil},
		{"DELETE", "/api/v1/workflows/" + wf, nil},
		{"DELETE", "/api/v1/credentials/" + cred, nil},
		{"POST", "/api/v1/credentials", map[string]any{"workspace_id": a.workspace, "name": "x", "provider": "openai", "credential_type": "api_key", "secret": "sk-x"}},
		{"POST", "/api/v1/projects", map[string]any{"workspace_id": a.workspace, "name": "member project"}},
	} {
		if r := e.call(b.token, c.method, c.path, c.body); r.status != 403 || r.errCode(t) != "FORBIDDEN" {
			t.Errorf("member %s %s: %d %s (want 403)", c.method, c.path, r.status, r.body)
		}
	}
	// A VIEWER only reads.
	if _, err := e.raw.Exec(context.Background(), `UPDATE workspace_members SET role = 'viewer' WHERE workspace_id = $1 AND user_id = $2`, a.workspace, b.userID); err != nil {
		t.Fatal(err)
	}
	e.must(e.call(b.token, "GET", "/api/v1/workflows/"+wf, nil), 200)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/workflows/" + wf + "/execute", map[string]any{}},
		{"POST", "/api/v1/executions/" + ex + "/cancel", nil},
		{"PATCH", "/api/v1/workflows/" + wf, map[string]any{"name": "viewer"}},
	} {
		if r := e.call(b.token, c.method, c.path, c.body); r.status != 403 {
			t.Errorf("viewer %s %s: %d %s (want 403)", c.method, c.path, r.status, r.body)
		}
	}
	// ADMIN may publish and manage credentials.
	if _, err := e.raw.Exec(context.Background(), `UPDATE workspace_members SET role = 'admin' WHERE workspace_id = $1 AND user_id = $2`, a.workspace, b.userID); err != nil {
		t.Fatal(err)
	}
	e.must(e.call(b.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+draft+"/publish", nil), 200)
	e.must(e.call(b.token, "DELETE", "/api/v1/credentials/"+cred, nil), 204)
}

func TestCredentialsAPI(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("Ken"), e.register("Rob")
	const secret = "sk-phase12-TOP-SECRET-value-0042"

	created := e.must(e.call(a.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": a.workspace,
		"name": "OpenAI Production", "provider": "openai", "credential_type": "api_key", "secret": secret}), 201)
	id := created["id"].(string)
	if created["credential_type"] != "api_key" || created["provider"] != "openai" || created["name"] != "OpenAI Production" {
		t.Fatalf("created = %v", created)
	}
	for _, k := range []string{"secret", "encrypted_data", "ciphertext"} {
		if _, ok := created[k]; ok {
			t.Fatalf("response has %q", k)
		}
	}
	// Encrypted at rest.
	row := e.sqlString(`SELECT row_to_json(c)::text FROM credentials c WHERE id = $1`, id)
	if strings.Contains(row, secret) || strings.Contains(row, "TOP-SECRET") || !strings.Contains(row, "ciphertext") {
		t.Fatalf("stored row = %s", row)
	}
	list := e.must(e.call(a.token, "GET", "/api/v1/credentials?workspace_id="+a.workspace, nil), 200)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != id {
		t.Fatalf("list = %v", list)
	}
	if other := e.must(e.call(b.token, "GET", "/api/v1/credentials?workspace_id="+b.workspace, nil), 200)["items"].([]any); len(other) != 0 {
		t.Fatalf("B sees A's credentials: %v", other)
	}
	if r := e.call(a.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": a.workspace, "name": "x",
		"provider": "nosuchprovider", "credential_type": "api_key", "secret": "s"}); r.status != 400 {
		t.Fatalf("unknown provider: %d %s", r.status, r.body)
	}
	if r := e.call(b.token, "DELETE", "/api/v1/credentials/"+id, nil); r.status != 404 {
		t.Fatalf("cross-workspace delete: %d", r.status)
	}
	e.must(e.call(a.token, "DELETE", "/api/v1/credentials/"+id, nil), 204)
	if r := e.call(a.token, "DELETE", "/api/v1/credentials/"+id, nil); r.status != 404 || r.errCode(t) != "CREDENTIAL_NOT_FOUND" {
		t.Fatalf("delete again: %d %s", r.status, r.body)
	}

	// Without CREDENTIAL_ENCRYPTION_KEY the API refuses to store secrets.
	noKey := e.cfg
	noKey.CredentialEncryptionKey = ""
	_, srv := e.startAPI(noKey)
	if r := e.callAt(srv, a.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": a.workspace, "name": "x",
		"provider": "openai", "credential_type": "api_key", "secret": secret}); r.status != 503 || r.errCode(t) != "SERVICE_UNAVAILABLE" {
		t.Fatalf("no key: %d %s", r.status, r.body)
	}

	// The secret never appeared in any response.
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, body := range e.bodies {
		if strings.Contains(body, "TOP-SECRET") {
			t.Fatalf("secret leaked in response: %s", body)
		}
	}
}

func TestCancelExecutionThroughAPI(t *testing.T) {
	e := newEnv(t)
	a := e.register("Barbara")
	wf, _ := e.published(a)
	id := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{}), 202)["execution_id"].(string)

	// Waiting in the queue: the cancellation is recorded through the
	// lifecycle service and enforced when a worker claims it.
	c := e.must(e.call(a.token, "POST", "/api/v1/executions/"+id+"/cancel", nil), 202)
	if c["cancel_requested"] != true {
		t.Fatalf("cancel = %v", c)
	}
	e.startWorkers()
	done, _ := e.waitStatus(a, id, 30*time.Second)
	if done["status"] != "CANCELLED" {
		t.Fatalf("after cancel = %v", done)
	}
	if n := e.sqlString(`SELECT count(*)::text FROM node_executions WHERE execution_id = $1`, id); n != "0" {
		t.Fatalf("%s nodes ran after cancellation", n)
	}
	if r := e.call(a.token, "POST", "/api/v1/executions/"+id+"/cancel", nil); r.status != 409 {
		t.Fatalf("cancel twice: %d %s", r.status, r.body)
	}
	if r := e.call(a.token, "POST", "/api/v1/executions/"+uuid.NewString()+"/cancel", nil); r.status != 404 {
		t.Fatalf("cancel unknown: %d", r.status)
	}
}

func TestReadinessWithRealDependencies(t *testing.T) {
	e := newEnv(t)
	if m := e.must(e.call("", "GET", "/ready", nil), 200); m["status"] != "ready" {
		t.Fatalf("ready = %v", m)
	}
	if m := e.must(e.call("", "GET", "/health", nil), 200); m["status"] != "ok" {
		t.Fatalf("health = %v", m)
	}
	// Dependencies gone (the runtime's PostgreSQL pool and Redis client are
	// closed): not ready, but the process is still healthy.
	e.api.Close()
	r := e.call("", "GET", "/ready", nil)
	if r.status != 503 || r.errCode(t) != "SERVICE_UNAVAILABLE" || !strings.Contains(string(r.body), `"postgres":"unavailable"`) ||
		!strings.Contains(string(r.body), `"redis":"unavailable"`) {
		t.Fatalf("ready after close: %d %s", r.status, r.body)
	}
	e.must(e.call("", "GET", "/health", nil), 200)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
