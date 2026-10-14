// Package calendar is the Google Calendar integration (Phase C4): two
// actions on the C1 integration framework, authenticated with Google
// connected accounts (C2, scope calendar.events).
//
//	list_events:  events [{event_id, summary, description, location, start, end, all_day,
//	              attendees [], organizer, html_link, meet_link, status}], count, event_id (first)
//	create_event: event_id, html_link, meet_link, start, end, status
//
// create_event can add a Google Meet link (Add Google Meet link); that needs
// no scope beyond calendar.events.
//
// create_event is idempotent: the event ID is derived from the node's
// idempotency key, so a retried attempt finds the event the first attempt
// created instead of adding a second one.
package calendar

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones on every platform (Windows has none)

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: google_calendar.list_events, .create_event.
const ID = "google_calendar"

// Provider is the OAuth provider whose connected accounts Calendar uses.
const Provider = "google"

// Bounds of list_events' settings.
const (
	MaxResults = 100
	MaxDays    = 365
)

// DefaultDuration is an event's length when no end is given.
const DefaultDuration = 30 * time.Minute

func str(name, label, desc string, required bool) node.ConfigField {
	return node.NewConfigField(name, node.ValueTypeString, required, "", desc).WithLabel(label)
}

func calendarField() node.ConfigField {
	return node.NewConfigField("calendar_id", node.ValueTypeString, false, "primary",
		"Which calendar: primary (the account's main calendar) or a calendar ID from Google Calendar's settings.").WithLabel("Calendar")
}

// Integration is Google Calendar's metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Google Calendar", Category: "Google", Icon: "calendar",
		Description: "List upcoming events and create events with a connected Google account.",
		DocsURL:     "https://developers.google.com/calendar/api",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{
				ID: "list_events", Name: "List Events", SideEffects: node.SideEffectsNone,
				Description: "Lists upcoming events from now on, earliest first (repeating events are listed once per occurrence).",
				Inputs:      []node.PortDefinition{node.NewPortDefinition("query", node.ValueTypeJSON, false, "Any upstream data; text is used as the search (overriding the Search setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("events", node.ValueTypeArray, true, "Events: event_id, summary, description, location, start, end, all_day, attendees, organizer, html_link, meet_link, status"),
					node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of events returned"),
					node.NewPortDefinition("event_id", node.ValueTypeString, true, "ID of the earliest event (empty when there is none)"),
				},
				Config: []node.ConfigField{
					calendarField(),
					node.NewConfigField("days_ahead", node.ValueTypeNumber, false, 7.0, "How far ahead to look, in days.").
						WithLabel("Days ahead").WithRange(node.Float(1), node.Float(MaxDays), node.Float(1)),
					str("query", "Search", "Only events containing this text (title, description, location, attendees). {{variables}} are filled in.", false),
					node.NewConfigField("max_results", node.ValueTypeNumber, false, 10.0, "How many events to return at most.").
						WithLabel("Max results").WithRange(node.Float(1), node.Float(MaxResults), node.Float(1)),
				},
			},
			{
				ID: "create_event", Name: "Create Event", SideEffects: node.SideEffectsIdempotent,
				Description: "Adds an event to a calendar. Attendees are only emailed when Notify attendees is on.",
				Inputs:      []node.PortDefinition{node.NewPortDefinition("description", node.ValueTypeJSON, false, "Any upstream data; text is used as the description (overriding the Description setting)")},
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("event_id", node.ValueTypeString, true, "Event ID"),
					node.NewPortDefinition("html_link", node.ValueTypeString, true, "Link to the event in Google Calendar"),
					node.NewPortDefinition("meet_link", node.ValueTypeString, true, "Google Meet link (empty unless Add Google Meet link is on)"),
					node.NewPortDefinition("start", node.ValueTypeString, true, "Start (RFC 3339, or a date for all-day events)"),
					node.NewPortDefinition("end", node.ValueTypeString, true, "End (RFC 3339, or the day after an all-day event)"),
					node.NewPortDefinition("status", node.ValueTypeString, true, `Event status, normally "confirmed"`),
				},
				Config: []node.ConfigField{
					calendarField(),
					str("summary", "Title", "{{variables}} are filled in.", true),
					str("start", "Start", "2026-10-20 15:00 (with a Time zone), 2026-10-20T15:00:00+05:30, or 2026-10-20 for an all-day event. {{variables}} are filled in.", true),
					str("end", "End", "Same formats as Start. Empty: Start plus Duration (or one day for an all-day event).", false),
					node.NewConfigField("duration_minutes", node.ValueTypeNumber, false, 30.0, "Length when End is empty.").
						WithLabel("Duration (minutes)").WithRange(node.Float(1), node.Float(24*60*14), node.Float(1)),
					str("time_zone", "Time zone", "IANA name, e.g. Asia/Kolkata or America/New_York. Needed when Start has no UTC offset.", false),
					str("location", "Location", "{{variables}} are filled in.", false),
					str("description", "Description", "Used when the description input is not connected; {{variables}} are filled in.", false).WithMultiline(),
					str("attendees", "Attendees", "Comma-separated email addresses to invite.", false),
					node.NewConfigField("notify_attendees", node.ValueTypeBoolean, false, false, "Email the invitation to the attendees.").WithLabel("Notify attendees"),
					node.NewConfigField("add_meet_link", node.ValueTypeBoolean, false, false, "Add a Google Meet video call to the event.").WithLabel("Add Google Meet link"),
				},
			},
		},
	}
}

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func invalid(msg string) error { return integration.NewError(integration.KindInvalidRequest, msg) }

func text(in node.NodeInput, name string) string {
	s, _ := in.GetStringConfig(name)
	return strings.TrimSpace(s)
}

func calendarID(in node.NodeInput) string {
	if id := text(in, "calendar_id"); id != "" {
		return id
	}
	return "primary"
}

func whole(in node.NodeInput, name string, def, min, max int) (int, error) {
	f, ok := in.GetNumberConfig(name)
	if !ok {
		return def, nil
	}
	if f < float64(min) || f > float64(max) || f != float64(int(f)) {
		return 0, configError(fmt.Sprintf("%s must be a whole number from %d to %d", name, min, max))
	}
	return int(f), nil
}

func boundary(t EventTime) string {
	if t.Date != "" {
		return t.Date
	}
	return t.DateTime
}

func summary(e Event) map[string]any {
	attendees := []any{}
	for _, a := range e.Attendees {
		attendees = append(attendees, a.Email)
	}
	organizer := ""
	if e.Organizer != nil {
		organizer = e.Organizer.Email
	}
	return map[string]any{
		"event_id": e.ID, "summary": e.Summary, "description": e.Description, "location": e.Location,
		"start": boundary(e.Start), "end": boundary(e.End), "all_day": e.Start.Date != "",
		"attendees": attendees, "organizer": organizer, "html_link": e.HTMLLink, "meet_link": e.HangoutLink, "status": e.Status,
	}
}

// listEvents uses now as the window start; tests replace it.
var now = time.Now

func listEvents(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	days, err := whole(in, "days_ahead", 7, 1, MaxDays)
	if err != nil {
		return node.NodeOutput{}, err
	}
	max, err := whole(in, "max_results", 10, 1, MaxResults)
	if err != nil {
		return node.NodeOutput{}, err
	}
	query := text(in, "query")
	if v, ok := in.GetPort("query"); ok {
		if s, ok := v.String(); ok {
			query = strings.TrimSpace(s)
		}
	}
	from := now().UTC()
	events, err := c.List(ctx, calendarID(in), from.Format(time.RFC3339), from.AddDate(0, 0, days).Format(time.RFC3339), query, max)
	if err != nil {
		return node.NodeOutput{}, err
	}
	list := make([]any, 0, len(events))
	for _, e := range events {
		list = append(list, summary(e))
	}
	first := ""
	if len(events) > 0 {
		first = events[0].ID
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("events", node.NewArrayValue(list))
	out.SetPort("count", node.NewNumberValue(float64(len(list))))
	out.SetPort("event_id", node.NewStringValue(first))
	return out, nil
}

const dateLayout = "2006-01-02"

var localLayouts = []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"}

// moment is a parsed Start or End: a date (all-day) or an instant.
type moment struct {
	date    time.Time
	allDay  bool
	instant time.Time
}

func parseMoment(field, s string, loc *time.Location) (moment, error) {
	if d, err := time.Parse(dateLayout, s); err == nil {
		return moment{date: d, allDay: true}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return moment{instant: t}, nil
	}
	for _, l := range localLayouts {
		if _, err := time.Parse(l, s); err == nil {
			if loc == nil {
				return moment{}, configError(field + " has no UTC offset: set a Time zone (e.g. Asia/Kolkata) or write it like 2026-10-20T15:00:00+05:30")
			}
			t, _ := time.ParseInLocation(l, s, loc)
			return moment{instant: t}, nil
		}
	}
	return moment{}, configError(field + " must look like 2026-10-20 15:00, 2026-10-20T15:00:00+05:30 or 2026-10-20")
}

func (m moment) eventTime(zone string) EventTime {
	if m.allDay {
		return EventTime{Date: m.date.Format(dateLayout)}
	}
	return EventTime{DateTime: m.instant.Format(time.RFC3339), TimeZone: zone}
}

// eventID derives a valid Calendar event ID (base32hex, lowercase) from the
// idempotency key; "" (Google assigns one) outside lifecycle-managed runs.
func eventID(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("google_calendar.create_event:" + key))
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

// meetRequestID identifies a Meet link request: the event ID (so a retry
// asks for the same conference), else a random one.
func meetRequestID(eventID string) string {
	if eventID != "" {
		return eventID
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func eventSpec(in node.NodeInput) (Event, string, error) {
	title := text(in, "summary")
	if title == "" {
		return Event{}, "", configError("set a Title")
	}
	zone := text(in, "time_zone")
	var loc *time.Location
	if zone != "" {
		l, err := time.LoadLocation(zone)
		if err != nil {
			return Event{}, "", configError(fmt.Sprintf("unknown time zone %q: use an IANA name such as Asia/Kolkata", zone))
		}
		loc = l
	}
	start, err := parseMoment("Start", text(in, "start"), loc)
	if err != nil {
		return Event{}, "", err
	}
	var end moment
	if s := text(in, "end"); s != "" {
		if end, err = parseMoment("End", s, loc); err != nil {
			return Event{}, "", err
		}
		if end.allDay != start.allDay {
			return Event{}, "", configError("Start and End must both be dates (all-day) or both be times")
		}
	} else if start.allDay {
		end = moment{date: start.date.AddDate(0, 0, 1), allDay: true}
	} else {
		minutes, err := whole(in, "duration_minutes", int(DefaultDuration/time.Minute), 1, 24*60*14)
		if err != nil {
			return Event{}, "", err
		}
		end = moment{instant: start.instant.Add(time.Duration(minutes) * time.Minute)}
	}
	if (start.allDay && !end.date.After(start.date)) || (!start.allDay && !end.instant.After(start.instant)) {
		return Event{}, "", configError("End must be after Start")
	}
	description := text(in, "description")
	if v, ok := in.GetPort("description"); ok {
		if s, ok := v.String(); ok {
			description = s
		}
	}
	e := Event{Summary: title, Description: description, Location: text(in, "location"), Start: start.eventTime(zone), End: end.eventTime(zone)}
	if raw := text(in, "attendees"); raw != "" {
		list, err := mail.ParseAddressList(raw)
		if err != nil {
			return Event{}, "", invalid("attendees must be comma-separated email addresses")
		}
		for _, a := range list {
			e.Attendees = append(e.Attendees, Attendee{Email: a.Address})
		}
	}
	send := "none"
	if b, _ := in.GetBooleanConfig("notify_attendees"); b {
		send = "all"
	}
	return e, send, nil
}

func createEvent(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	e, send, err := eventSpec(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	cal := calendarID(in)
	e.ID = eventID(in.IdempotencyKey)
	if b, _ := in.GetBooleanConfig("add_meet_link"); b {
		e.ConferenceData = &ConferenceData{CreateRequest: &CreateConferenceRequest{
			RequestID: meetRequestID(e.ID), ConferenceSolutionKey: ConferenceSolutionKey{Type: "hangoutsMeet"}}}
	}
	created, err := c.Insert(ctx, cal, e, send)
	var ie *integration.Error
	if err != nil && e.ID != "" && errors.As(err, &ie) && ie.StatusCode == http.StatusConflict {
		// An earlier attempt of this same operation created it.
		created, err = c.Get(ctx, cal, e.ID)
	}
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("event_id", node.NewStringValue(created.ID))
	out.SetPort("html_link", node.NewStringValue(created.HTMLLink))
	out.SetPort("meet_link", node.NewStringValue(created.HangoutLink))
	out.SetPort("start", node.NewStringValue(boundary(created.Start)))
	out.SetPort("end", node.NewStringValue(boundary(created.End)))
	out.SetPort("status", node.NewStringValue(created.Status))
	return out, nil
}

// Connector builds a Calendar client around a resolved Google credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Google Calendar with its executable nodes. creds is the
// runtime credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	handlers := map[string]integration.Handler[*Client]{"list_events": listEvents, "create_event": createEvent}
	nodes := map[string]node.Node{}
	for id, h := range handlers {
		n, err := integration.NewActionNode(in, id, creds, Connector(o), h)
		if err != nil {
			return integration.Module{}, err
		}
		nodes[id] = n
	}
	return integration.Module{Integration: in, Nodes: nodes}, nil
}
