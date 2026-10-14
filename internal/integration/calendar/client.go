package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration/googleapi"
)

// DefaultBaseURL is the Google Calendar REST API root.
const DefaultBaseURL = "https://www.googleapis.com/calendar/v3"

// Options configure the Calendar client (tests point BaseURL at a mock API).
type Options = googleapi.Options

// Client is the Google Calendar REST client for one action call.
type Client struct {
	api *googleapi.Client
}

// NewClient returns a client with the access token of a resolved credential.
func NewClient(o Options, token credential.Secret) *Client {
	return &Client{api: googleapi.New("Google Calendar", DefaultBaseURL, o, token)}
}

// EventTime is an event boundary: Date for all-day events, else DateTime.
type EventTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

// Attendee is one invited address.
type Attendee struct {
	Email string `json:"email"`
}

// Event is the subset of a Calendar event the actions read and write.
type Event struct {
	ID          string     `json:"id,omitempty"`
	Status      string     `json:"status,omitempty"`
	HTMLLink    string     `json:"htmlLink,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Description string     `json:"description,omitempty"`
	Location    string     `json:"location,omitempty"`
	Start       EventTime  `json:"start"`
	End         EventTime  `json:"end"`
	Attendees   []Attendee `json:"attendees,omitempty"`
	// HangoutLink is the event's Google Meet link (set by Google).
	HangoutLink string `json:"hangoutLink,omitempty"`
	// ConferenceData asks Google to add a Meet link when the event is
	// created (sent with conferenceDataVersion=1).
	ConferenceData *ConferenceData `json:"conferenceData,omitempty"`
	Organizer      *struct {
		Email string `json:"email"`
	} `json:"organizer,omitempty"`
}

// ConferenceData requests a conference for a new event.
type ConferenceData struct {
	CreateRequest *CreateConferenceRequest `json:"createRequest,omitempty"`
}

// CreateConferenceRequest asks for a conference of one solution type.
type CreateConferenceRequest struct {
	RequestID             string                `json:"requestId"`
	ConferenceSolutionKey ConferenceSolutionKey `json:"conferenceSolutionKey"`
}

// ConferenceSolutionKey names the conference type ("hangoutsMeet": Google Meet).
type ConferenceSolutionKey struct {
	Type string `json:"type"`
}

func eventsPath(calendarID string) string {
	return "/calendars/" + url.PathEscape(calendarID) + "/events"
}

// List returns events between timeMin and timeMax (RFC 3339), recurring
// events expanded, ordered by start time.
func (c *Client) List(ctx context.Context, calendarID, timeMin, timeMax, query string, max int) ([]Event, error) {
	q := url.Values{
		"timeMin": {timeMin}, "timeMax": {timeMax}, "singleEvents": {"true"}, "orderBy": {"startTime"},
		"maxResults": {fmt.Sprint(max)},
	}
	if query != "" {
		q.Set("q", query)
	}
	var out struct {
		Items []Event `json:"items"`
	}
	if err := c.api.Do(ctx, http.MethodGet, eventsPath(calendarID), q, nil, &out, "list events"); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Insert creates an event. sendUpdates is "all" or "none".
func (c *Client) Insert(ctx context.Context, calendarID string, e Event, sendUpdates string) (Event, error) {
	var out Event
	q := url.Values{"sendUpdates": {sendUpdates}}
	if e.ConferenceData != nil {
		q.Set("conferenceDataVersion", "1")
	}
	err := c.api.Do(ctx, http.MethodPost, eventsPath(calendarID), q, e, &out, "create event")
	return out, err
}

// Get loads one event.
func (c *Client) Get(ctx context.Context, calendarID, eventID string) (Event, error) {
	var out Event
	err := c.api.Do(ctx, http.MethodGet, eventsPath(calendarID)+"/"+url.PathEscape(eventID), nil, nil, &out, "load event")
	return out, err
}
