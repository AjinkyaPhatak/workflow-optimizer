package calendar_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/integration/calendar"
	"workflow-optimizer/internal/integration/googleapi/apitest"
	"workflow-optimizer/internal/node"
)

type request struct {
	Method, Path string
	Query        map[string][]string
	Body         map[string]any
	Authorized   bool
}

// mockCalendar is a tiny Google Calendar API with one calendar, "primary".
type mockCalendar struct {
	mu       sync.Mutex
	srv      *httptest.Server
	events   []map[string]any
	requests []request
	failNext int  // answer the next insert with this status
	applied  bool // ...after storing the event (a failure that applied)
}

func newMock(t *testing.T) *mockCalendar {
	m := &mockCalendar{events: []map[string]any{
		{"id": "ev1", "status": "confirmed", "summary": "Standup", "htmlLink": "https://calendar.google.com/event?eid=ev1",
			"start": map[string]any{"dateTime": "2026-10-15T09:00:00+05:30"}, "end": map[string]any{"dateTime": "2026-10-15T09:15:00+05:30"},
			"attendees": []any{map[string]any{"email": "ada@example.test"}}, "organizer": map[string]any{"email": "me@example.test"}},
		{"id": "ev2", "status": "confirmed", "summary": "Holiday", "start": map[string]any{"date": "2026-10-16"}, "end": map[string]any{"date": "2026-10-17"}},
	}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockCalendar) options() app.Dependencies {
	return app.Dependencies{Calendar: calendar.Options{BaseURL: m.srv.URL + "/calendar/v3", HTTPClient: m.srv.Client()}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockCalendar) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	req := request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Authorized: apitest.Authorized(r)}
	_ = json.Unmarshal(body, &req.Body)
	m.requests = append(m.requests, req)
	const base = "/calendar/v3/calendars/primary/events"
	switch {
	case !req.Authorized:
		writeJSON(w, 401, map[string]any{"error": map[string]any{"code": 401}})
	case r.URL.Path == base && r.Method == http.MethodGet:
		items := []any{}
		for _, e := range m.events {
			if q := r.URL.Query().Get("q"); q == "" || strings.Contains(strings.ToLower(e["summary"].(string)), strings.ToLower(q)) {
				items = append(items, e)
			}
		}
		writeJSON(w, 200, map[string]any{"items": items, "timeZone": "Asia/Kolkata"})
	case r.URL.Path == base && r.Method == http.MethodPost:
		id, _ := req.Body["id"].(string)
		for _, e := range m.events {
			if id != "" && e["id"] == id {
				writeJSON(w, 409, map[string]any{"error": map[string]any{"code": 409, "errors": []any{map[string]any{"reason": "duplicate"}}}})
				return
			}
		}
		if m.failNext != 0 && !m.applied {
			writeJSON(w, m.failNext, map[string]any{"error": map[string]any{"code": m.failNext}})
			m.failNext = 0
			return
		}
		e := req.Body
		if id == "" {
			id = "generated1"
		}
		e["id"], e["status"], e["htmlLink"] = id, "confirmed", "https://calendar.google.com/event?eid="+id
		if e["conferenceData"] != nil && r.URL.Query().Get("conferenceDataVersion") == "1" {
			e["hangoutLink"] = "https://meet.google.com/abc-defg-hij"
		}
		m.events = append(m.events, e)
		if m.failNext != 0 {
			writeJSON(w, m.failNext, map[string]any{"error": map[string]any{"code": m.failNext}})
			m.failNext = 0
			return
		}
		writeJSON(w, 200, e)
	case strings.HasPrefix(r.URL.Path, base+"/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(r.URL.Path, base+"/")
		for _, e := range m.events {
			if e["id"] == id {
				writeJSON(w, 200, e)
				return
			}
		}
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": 404}})
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": 404}})
	}
}

func (m *mockCalendar) last() request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[len(m.requests)-1]
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestMetadata(t *testing.T) {
	h := apitest.New(t, newMock(t).options())
	for typ, side := range map[string]node.SideEffects{
		"google_calendar.list_events": node.SideEffectsNone, "google_calendar.create_event": node.SideEffectsIdempotent,
	} {
		d, err := h.App.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if d.SideEffects != side || d.Integration == nil || d.Integration.Name != "Google Calendar" || d.Auth == nil || d.Auth.Provider != "google" {
			t.Fatalf("%s: %+v", typ, d)
		}
	}
}

func TestListEvents(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("google_calendar.list_events", map[string]any{"days_ahead": 3.0, "max_results": 5.0}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	events := out["events"].([]any)
	want := `{"all_day":false,"attendees":["ada@example.test"],"description":"","end":"2026-10-15T09:15:00+05:30","event_id":"ev1",` +
		`"html_link":"https://calendar.google.com/event?eid=ev1","location":"","meet_link":"","organizer":"me@example.test","start":"2026-10-15T09:00:00+05:30","status":"confirmed","summary":"Standup"}`
	if out["count"] != 2.0 || out["event_id"] != "ev1" || mustJSON(events[0]) != want ||
		events[1].(map[string]any)["all_day"] != true || events[1].(map[string]any)["start"] != "2026-10-16" {
		t.Fatalf("list: %v", out)
	}
	q := m.last().Query
	from, err1 := time.Parse(time.RFC3339, q["timeMin"][0])
	to, err2 := time.Parse(time.RFC3339, q["timeMax"][0])
	if err1 != nil || err2 != nil || to.Sub(from) != 72*time.Hour || time.Since(from) > time.Minute ||
		q["singleEvents"][0] != "true" || q["orderBy"][0] != "startTime" || q["maxResults"][0] != "5" || !m.last().Authorized {
		t.Fatalf("list query: %v", q)
	}
	// Search text from the setting (with variables) or from upstream text.
	out, err = h.Result("google_calendar.list_events", map[string]any{"query": "{{input.what}}"}, map[string]any{"what": "holiday"})
	if err != nil || out["count"] != 1.0 || out["event_id"] != "ev2" {
		t.Fatalf("search: %v %v", out, err)
	}
}

func TestCreateEvent(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("google_calendar.create_event", map[string]any{"summary": "Call with {{input.name}}", "start": "2026-10-20 15:00",
		"time_zone": "Asia/Kolkata", "attendees": "Grace <grace@example.test>, linus@example.test", "location": "Online", "description": "Agenda"},
		map[string]any{"name": "Grace"})
	if err != nil {
		t.Fatal(err)
	}
	if out["start"] != "2026-10-20T15:00:00+05:30" || out["end"] != "2026-10-20T15:30:00+05:30" || out["status"] != "confirmed" ||
		!strings.HasPrefix(out["html_link"].(string), "https://calendar.google.com/") {
		t.Fatalf("create: %v", out)
	}
	req := m.last()
	if req.Query["sendUpdates"][0] != "none" || req.Body["summary"] != "Call with Grace" || req.Body["description"] != "Agenda" ||
		mustJSON(req.Body["attendees"]) != `[{"email":"grace@example.test"},{"email":"linus@example.test"}]` ||
		mustJSON(req.Body["start"]) != `{"dateTime":"2026-10-20T15:00:00+05:30","timeZone":"Asia/Kolkata"}` {
		t.Fatalf("insert request: %+v", req)
	}
	// The ID is derived from the idempotency key: base32hex, lowercase.
	id := req.Body["id"].(string)
	if out["event_id"] != id || len(id) < 5 || strings.Trim(id, "0123456789abcdefghijklmnopqrstuv") != "" {
		t.Fatalf("event id %q", id)
	}

	// All-day, explicit end, notify attendees.
	out, err = h.Result("google_calendar.create_event", map[string]any{"summary": "Offsite", "start": "2026-11-02", "end": "2026-11-04",
		"attendees": "ada@example.test", "notify_attendees": true}, map[string]any{})
	if err != nil || out["start"] != "2026-11-02" || out["end"] != "2026-11-04" || m.last().Query["sendUpdates"][0] != "all" {
		t.Fatalf("all day: %v %v %+v", out, err, m.last())
	}
	// An RFC 3339 start needs no time zone; the duration sets the end.
	out, err = h.Result("google_calendar.create_event", map[string]any{"summary": "x", "start": "2026-10-20T10:00:00Z", "duration_minutes": 90.0}, map[string]any{})
	if err != nil || out["end"] != "2026-10-20T11:30:00Z" {
		t.Fatalf("rfc3339: %v %v", out, err)
	}

	before := len(m.requests)
	for name, cfg := range map[string]map[string]any{
		"bad zone":       {"summary": "x", "start": "2026-10-20 15:00", "time_zone": "Mars/Base"},
		"bad start":      {"summary": "x", "start": "next tuesday"},
		"end before":     {"summary": "x", "start": "2026-10-20T10:00:00Z", "end": "2026-10-20T09:00:00Z"},
		"mixed kinds":    {"summary": "x", "start": "2026-10-20", "end": "2026-10-20T09:00:00Z"},
		"bad attendees":  {"summary": "x", "start": "2026-10-20", "attendees": "not an address"},
		"title variable": {"summary": "{{input.missing}}", "start": "2026-10-20"},
	} {
		_, err := h.Result("google_calendar.create_event", cfg, map[string]any{})
		if f := apitest.Failure(t, err); f.Retryable {
			t.Fatalf("%s: %+v", name, f)
		}
	}
	if len(m.requests) != before {
		t.Fatal("invalid settings must not reach Google Calendar")
	}

	// A time without a Time zone is in the calendar's own zone.
	out, err = h.Result("google_calendar.create_event", map[string]any{"summary": "x", "start": "2026-10-20 15:00"}, map[string]any{})
	if err != nil || out["start"] != "2026-10-20T15:00:00+05:30" || m.last().Body["start"].(map[string]any)["timeZone"] != "Asia/Kolkata" {
		t.Fatalf("calendar zone: %v %v %+v", out, err, m.last().Body)
	}
}

func TestCreateEventWithMeetLink(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	out, err := h.Result("google_calendar.create_event", map[string]any{"summary": "Sync", "start": "2026-10-20T10:00:00Z", "add_meet_link": true}, map[string]any{})
	if err != nil || out["meet_link"] != "https://meet.google.com/abc-defg-hij" {
		t.Fatalf("create: %v %v", out, err)
	}
	req := m.last()
	create := req.Body["conferenceData"].(map[string]any)["createRequest"].(map[string]any)
	if req.Query["conferenceDataVersion"][0] != "1" || mustJSON(create["conferenceSolutionKey"]) != `{"type":"hangoutsMeet"}` ||
		create["requestId"] != req.Body["id"] {
		t.Fatalf("insert request: %+v", req)
	}
	// Off (the default): no conference is requested.
	out, err = h.Result("google_calendar.create_event", map[string]any{"summary": "Solo", "start": "2026-10-21T10:00:00Z"}, map[string]any{})
	if err != nil || out["meet_link"] != "" || m.last().Body["conferenceData"] != nil || m.last().Query["conferenceDataVersion"] != nil {
		t.Fatalf("no meet: %v %v %+v", out, err, m.last())
	}
}

// A retried create (same idempotency key) after a failure that applied
// finds the first attempt's event instead of creating another.
func TestCreateEventIsIdempotent(t *testing.T) {
	m := newMock(t)
	h := apitest.New(t, m.options())
	def := h.Definition("google_calendar.create_event", map[string]any{"summary": "Once", "start": "2026-10-20T10:00:00Z"})
	m.failNext, m.applied = http.StatusServiceUnavailable, true
	err := h.Run(def, map[string]any{}, "exec-42")
	if f := apitest.Failure(t, err); f.Code != "PROVIDER_UNAVAILABLE" || !f.Retryable {
		t.Fatalf("first attempt: %+v", f)
	}
	if err := h.Run(def, map[string]any{}, "exec-42"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range m.events {
		if e["summary"] == "Once" {
			n++
		}
	}
	if n != 1 || m.last().Method != http.MethodGet {
		t.Fatalf("events named Once: %d, last request %+v", n, m.last())
	}
	// A different execution creates a new event.
	if err := h.Run(def, map[string]any{}, "exec-43"); err != nil {
		t.Fatal(err)
	}
	if len(m.events) != 4 {
		t.Fatalf("events: %d", len(m.events))
	}
}

func TestScopeErrorAsksToReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 403, map[string]any{"error": map[string]any{"code": 403, "status": "PERMISSION_DENIED",
			"errors": []any{map[string]any{"reason": "insufficientPermissions"}}}})
	}))
	defer srv.Close()
	h := apitest.New(t, app.Dependencies{Calendar: calendar.Options{BaseURL: srv.URL, HTTPClient: srv.Client()}})
	_, err := h.Result("google_calendar.list_events", map[string]any{}, map[string]any{})
	f := apitest.Failure(t, err)
	if f.Code != "PERMISSION_DENIED" || f.Retryable || !strings.Contains(f.Message, "reconnect it on the Connected accounts page") {
		t.Fatalf("%+v", f)
	}
	apitest.NoToken(t, "error", mustJSON(f))
}
