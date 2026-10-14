package discord_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration/discord"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/node"
)

const (
	botToken = "bot-secret-token"
	guild    = "100000000000000001"
	general  = "200000000000000001"
	news     = "200000000000000002"
	foreign  = "900000000000000009" // a channel of another server the bot is in
)

type request struct {
	Method, Path, Query, Auth, UA string
	Body                          map[string]any
}

// mockDiscord is a tiny Discord API: the connected server's channels, plus
// one channel of another server.
type mockDiscord struct {
	mu       sync.Mutex
	srv      *httptest.Server
	requests []request
	sent     []map[string]any
	fail     int
	failBody string
}

func newMock(t *testing.T) *mockDiscord {
	m := &mockDiscord{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockDiscord) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req := request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), UA: r.Header.Get("User-Agent")}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &req.Body)
	m.requests = append(m.requests, req)
	if req.Auth != "Bot "+botToken {
		writeJSON(w, 401, map[string]any{"message": "401: Unauthorized", "code": 0})
		return
	}
	if m.fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.fail)
		_, _ = io.WriteString(w, m.failBody)
		m.fail = 0
		return
	}
	channels := map[string]map[string]any{
		general: {"id": general, "guild_id": guild, "name": "general", "type": 0},
		news:    {"id": news, "guild_id": guild, "name": "News", "type": 5},
		foreign: {"id": foreign, "guild_id": "900000000000000001", "name": "secret", "type": 0},
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v10")
	switch {
	case r.Method == http.MethodGet && p == "/guilds/"+guild+"/channels":
		writeJSON(w, 200, []any{map[string]any{"id": "300000000000000001", "name": "Text Channels", "type": 4}, channels[general], channels[news]})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/channels/") && !strings.HasSuffix(p, "/messages"):
		if ch, ok := channels[strings.TrimPrefix(p, "/channels/")]; ok {
			writeJSON(w, 200, ch)
			return
		}
		writeJSON(w, 404, map[string]any{"message": "Unknown Channel", "code": 10003})
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/messages"):
		m.sent = append(m.sent, req.Body)
		writeJSON(w, 200, map[string]any{"id": fmt.Sprintf("40000000000000000%d", len(m.sent)), "channel_id": strings.Split(p, "/")[2]})
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/messages"):
		writeJSON(w, 200, []any{ // newest first
			map[string]any{"id": "500000000000000003", "content": "Ship it", "timestamp": "2026-10-14T10:03:00Z",
				"author": map[string]any{"id": "1", "username": "ada", "global_name": "Ada"}, "attachments": []any{map[string]any{"filename": "plan.pdf"}}},
			map[string]any{"id": "500000000000000002", "content": "Build passed\nall green", "timestamp": "2026-10-14T10:02:00Z",
				"author": map[string]any{"id": "2", "username": "ci-bot", "bot": true}},
			map[string]any{"id": "500000000000000001", "content": "Morning!", "timestamp": "2026-10-14T10:01:00Z",
				"author": map[string]any{"id": "3", "username": "grace", "global_name": ""}},
		})
	default:
		writeJSON(w, 404, map[string]any{"message": "Not Found", "code": 0})
	}
}

func (m *mockDiscord) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func harness(t *testing.T, m *mockDiscord, token string) *apitest.Harness {
	h := apitest.New(t, app.Dependencies{Discord: discord.Options{BotToken: credential.NewSecret(token), BaseURL: m.srv.URL + "/api/v10", HTTPClient: m.srv.Client()}})
	h.Creds.AccountID = guild
	return h
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestMetadata(t *testing.T) {
	h := harness(t, newMock(t), botToken)
	for typ, side := range map[string]node.SideEffects{"discord.send_message": node.SideEffectsUnsafe, "discord.read_messages": node.SideEffectsNone} {
		d, err := h.App.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if d.SideEffects != side || d.Integration == nil || d.Integration.Name != "Discord" || d.Auth == nil || d.Auth.Provider != "discord" ||
			d.Config[0].Name != "credential_id" || d.Inputs[0].Type != node.ValueTypeJSON {
			t.Fatalf("%s: %+v", typ, d)
		}
	}
}

func TestSendMessage(t *testing.T) {
	m := newMock(t)
	h := harness(t, m, botToken)
	out, err := h.Result("discord.send_message", map[string]any{"channel": "#General", "content": "Hello {{input.name}} @everyone",
		"embed_title": "Report", "embed_description": "All good", "embed_url": "https://example.test/r", "embed_color": "#5865F2"}, map[string]any{"name": "team"})
	if err != nil {
		t.Fatal(err)
	}
	if out["message_id"] != "400000000000000001" || out["channel_id"] != general || mustJSON(out["message_ids"]) != `["400000000000000001"]` ||
		out["url"] != "https://discord.com/channels/"+guild+"/"+general+"/400000000000000001" {
		t.Fatalf("send: %v", out)
	}
	body := m.sent[0]
	if body["content"] != "Hello team @everyone" || mustJSON(body["allowed_mentions"]) != `{"parse":[]}` || body["enforce_nonce"] != true ||
		len(body["nonce"].(string)) != 25 || mustJSON(body["embeds"]) != `[{"color":5793266,"description":"All good","title":"Report","url":"https://example.test/r"}]` {
		t.Fatalf("send body: %v", body)
	}
	last := m.requests[len(m.requests)-1]
	if last.Path != "/api/v10/channels/"+general+"/messages" || !strings.HasPrefix(last.UA, "DiscordBot (") {
		t.Fatalf("send request: %+v", last)
	}
	// The user's own OAuth token is never sent to Discord's API.
	for _, r := range m.requests {
		if strings.Contains(r.Auth, apitest.Token) {
			t.Fatal("the connected user's token was used")
		}
	}

	// An empty message fails; by link, with mentions allowed.
	if _, err := h.Result("discord.send_message", map[string]any{"channel": news, "allow_mentions": true}, map[string]any{}); err == nil {
		t.Fatal("an empty message must fail")
	}
	out, err = h.Result("discord.send_message", map[string]any{"channel": "https://discord.com/channels/" + guild + "/" + news, "allow_mentions": true,
		"content": "{{input.msg}}"}, map[string]any{"msg": "Hi"})
	if err != nil || out["channel_id"] != news || mustJSON(m.sent[len(m.sent)-1]["allowed_mentions"]) != `{"parse":["users","roles","everyone"]}` {
		t.Fatalf("by link: %v %v", out, err)
	}

	// Long text is split at line breaks; the embed goes with the last part.
	long := strings.Repeat("line of text\n", 350) // 4550 characters
	before := len(m.sent)
	out, err = h.Result("discord.send_message", map[string]any{"channel": general, "content": long, "embed_title": "End"}, map[string]any{})
	if err != nil || len(out["message_ids"].([]any)) != 3 || len(m.sent)-before != 3 {
		t.Fatalf("split: %v %v", out, err)
	}
	for i, b := range m.sent[before:] {
		if n := len([]rune(b["content"].(string))); n > discord.MaxContent || !strings.HasSuffix(b["content"].(string), "text") {
			t.Fatalf("part %d: %d characters", i, n)
		}
		if _, has := b["embeds"]; has != (i == 2) {
			t.Fatalf("embed on part %d", i)
		}
	}
	if m.sent[before]["nonce"] == m.sent[before+1]["nonce"] {
		t.Fatal("each part needs its own nonce")
	}
}

// The bot is shared by every workspace: a workflow may only reach channels
// of the server its account connected.
func TestOtherServersAreRefused(t *testing.T) {
	m := newMock(t)
	h := harness(t, m, botToken)
	before := m.count()
	_, err := h.Result("discord.send_message", map[string]any{"channel": foreign, "content": "leak"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "PERMISSION_DENIED" || !strings.Contains(f.Message, "not in the connected Discord server") {
		t.Fatalf("foreign channel by ID: %+v", f)
	}
	_, err = h.Result("discord.read_messages", map[string]any{"channel": "https://discord.com/channels/900000000000000001/" + foreign}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "PERMISSION_DENIED" {
		t.Fatalf("foreign channel by link: %+v", f)
	}
	if len(m.sent) != 0 || m.count() != before+1 { // one channel lookup, nothing sent or read
		t.Fatalf("requests after refusal: %d, sent %d", m.count()-before, len(m.sent))
	}
	_, err = h.Result("discord.send_message", map[string]any{"channel": "random", "content": "x"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "RESOURCE_NOT_FOUND" || !strings.Contains(f.Message, "general, News") || strings.Contains(f.Message, "Text Channels") {
		t.Fatalf("unknown name: %+v", f)
	}
	h.Creds.AccountID = ""
	_, err = h.Result("discord.send_message", map[string]any{"channel": general, "content": "x"}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "CREDENTIAL_INVALID" {
		t.Fatalf("account without a server: %+v", f)
	}
}

func TestSendIsDeduplicatedByNonce(t *testing.T) {
	m := newMock(t)
	h := harness(t, m, botToken)
	def := h.Definition("discord.send_message", map[string]any{"channel": general, "content": "once"})
	for i := 0; i < 2; i++ {
		if err := h.Run(def, map[string]any{}, "exec-7"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Run(def, map[string]any{}, "exec-8"); err != nil {
		t.Fatal(err)
	}
	if m.sent[0]["nonce"] != m.sent[1]["nonce"] || m.sent[0]["nonce"] == m.sent[2]["nonce"] {
		t.Fatalf("nonces: %v %v %v", m.sent[0]["nonce"], m.sent[1]["nonce"], m.sent[2]["nonce"])
	}
}

func TestReadMessages(t *testing.T) {
	m := newMock(t)
	h := harness(t, m, botToken)
	out, err := h.Result("discord.read_messages", map[string]any{"channel": "general", "limit": 3.0}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	if out["count"] != 3.0 || out["message_id"] != "500000000000000003" || out["text"] != "grace: Morning!\nci-bot: Build passed all green\nAda: Ship it" ||
		mustJSON(msgs[2]) != `{"attachments":["plan.pdf"],"author":"Ada","author_id":"1","content":"Ship it","is_bot":false,"message_id":"500000000000000003","timestamp":"2026-10-14T10:03:00Z","url":"https://discord.com/channels/`+guild+`/`+general+`/500000000000000003"}` ||
		msgs[1].(map[string]any)["is_bot"] != true {
		t.Fatalf("read: %v", out)
	}
	if last := m.requests[len(m.requests)-1]; last.Query != "limit=3" {
		t.Fatalf("read request: %+v", last)
	}
}

func TestErrors(t *testing.T) {
	m := newMock(t)
	h := harness(t, m, botToken)
	cases := []struct {
		status    int
		body      string
		code      string
		retryable bool
		message   string
	}{
		{403, `{"message":"Missing Access","code":50001}`, "PERMISSION_DENIED", false, "check its permissions"},
		{429, `{"message":"You are being rate limited.","retry_after":1.5,"global":false}`, "RATE_LIMITED", true, ""},
		{502, `{}`, "PROVIDER_UNAVAILABLE", true, ""},
	}
	for _, c := range cases {
		m.mu.Lock()
		m.fail, m.failBody = c.status, c.body
		m.mu.Unlock()
		_, err := h.Result("discord.read_messages", map[string]any{"channel": general}, map[string]any{})
		f := apitest.Failure(t, err)
		if f.Code != c.code || f.Retryable != c.retryable || !strings.Contains(f.Message, c.message) || strings.Contains(f.Message, "Missing Access") {
			t.Fatalf("%d: %+v", c.status, f)
		}
	}
	// A wrong bot token, and none at all.
	bad := harness(t, m, "wrong")
	_, err := bad.Result("discord.read_messages", map[string]any{"channel": general}, map[string]any{})
	f := apitest.Failure(t, err)
	if f.Code != "AUTHENTICATION_FAILED" || !strings.Contains(f.Message, "DISCORD_BOT_TOKEN") || strings.Contains(mustJSON(f), "wrong") {
		t.Fatalf("bad token: %+v", f)
	}
	none := harness(t, m, "")
	_, err = none.Result("discord.read_messages", map[string]any{"channel": general}, map[string]any{})
	if f := apitest.Failure(t, err); f.Code != "CONFIGURATION_ERROR" || f.Retryable {
		t.Fatalf("no token: %+v", f)
	}
	for _, bad := range []map[string]any{
		{"channel": general, "content": "x", "embed_color": "blue"},
		{"channel": general, "embed_url": "https://x.test"},
		{"channel": general, "content": strings.Repeat("x", discord.MaxContent*discord.MaxParts+1)},
	} {
		before := len(m.sent)
		_, err := h.Result("discord.send_message", bad, map[string]any{})
		if f := apitest.Failure(t, err); f.Retryable || len(m.sent) != before {
			t.Fatalf("%v: %+v", bad["embed_color"], f)
		}
	}
}
