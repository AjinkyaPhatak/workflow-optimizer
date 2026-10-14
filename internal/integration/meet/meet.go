// Package meet is the Google Meet integration: one action on the C1
// integration framework, authenticated with Google connected accounts (C2,
// scope meetings.space.created).
//
//	create_meeting: meeting_link, meeting_code, space
//
// create_meeting makes a new meeting space (a link anyone can open right
// away; nothing is scheduled). It cannot deduplicate, so it is unsafe: a
// retry after an attempt that applied would make a second, unused link.
// Meetings with a date and attendees are Calendar's Create Event with Add
// Google Meet link on.
package meet

import (
	"context"
	"net/http"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/googleapi"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: google_meet.create_meeting.
const ID = "google_meet"

// Provider is the OAuth provider whose connected accounts Meet uses.
const Provider = "google"

// DefaultBaseURL is the Google Meet REST API root.
const DefaultBaseURL = "https://meet.googleapis.com/v2"

// Options configure the Meet client (tests point BaseURL at a mock API).
type Options = googleapi.Options

// Client is the Google Meet REST client for one action call.
type Client struct {
	api *googleapi.Client
}

// NewClient returns a client with the access token of a resolved credential.
func NewClient(o Options, token credential.Secret) *Client {
	return &Client{api: googleapi.New("Google Meet", DefaultBaseURL, o, token)}
}

// Space is the subset of a Meet space the action reads and writes.
type Space struct {
	Name        string       `json:"name,omitempty"`
	MeetingURI  string       `json:"meetingUri,omitempty"`
	MeetingCode string       `json:"meetingCode,omitempty"`
	Config      *SpaceConfig `json:"config,omitempty"`
}

// SpaceConfig holds who can join without asking.
type SpaceConfig struct {
	AccessType string `json:"accessType,omitempty"`
}

// CreateSpace makes a new meeting space.
func (c *Client) CreateSpace(ctx context.Context, s Space) (Space, error) {
	var out Space
	err := c.api.Do(ctx, http.MethodPost, "/spaces", nil, s, &out, "create meeting")
	return out, err
}

// AccessTypes are the values of the Who can join setting ("" is the
// account's default).
var AccessTypes = []string{"OPEN", "TRUSTED", "RESTRICTED"}

// Integration is Google Meet's metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Google Meet", Category: "Google", Icon: "meet",
		Description: "Create Google Meet links with a connected Google account.",
		DocsURL:     "https://developers.google.com/meet/api",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{{
			ID: "create_meeting", Name: "Create Meeting", SideEffects: node.SideEffectsUnsafe,
			Description: "Creates a new Google Meet link that can be joined right away. To schedule a meeting with a time and invitees, use Google Calendar's Create Event with Add Google Meet link on.",
			Inputs:      []node.PortDefinition{node.NewPortDefinition("input", node.ValueTypeJSON, false, "Any upstream data; connect a node to create the meeting after it")},
			Outputs: []node.PortDefinition{
				node.NewPortDefinition("meeting_link", node.ValueTypeString, true, "Link to join, e.g. https://meet.google.com/abc-mnop-xyz"),
				node.NewPortDefinition("meeting_code", node.ValueTypeString, true, "Meeting code, e.g. abc-mnop-xyz"),
				node.NewPortDefinition("space", node.ValueTypeString, true, "Meet's ID for the meeting space (spaces/...)"),
			},
			Config: []node.ConfigField{
				node.NewConfigField("access_type", node.ValueTypeString, false, nil,
					"Who can join without being let in by the host. Empty: the account's default.").WithLabel("Who can join").WithOptions(
					node.ConfigOption{Value: "OPEN", Label: "Anyone with the link"},
					node.ConfigOption{Value: "TRUSTED", Label: "People in your organization and invitees"},
					node.ConfigOption{Value: "RESTRICTED", Label: "Only invitees"},
				),
			},
		}},
	}
}

func createMeeting(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	access, _ := in.GetStringConfig("access_type")
	access = strings.ToUpper(strings.TrimSpace(access))
	var s Space
	if access != "" {
		known := false
		for _, a := range AccessTypes {
			known = known || a == access
		}
		if !known {
			return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration, "Who can join must be OPEN, TRUSTED or RESTRICTED", false)
		}
		s.Config = &SpaceConfig{AccessType: access}
	}
	created, err := c.CreateSpace(ctx, s)
	if err != nil {
		return node.NodeOutput{}, err
	}
	if created.MeetingURI == "" {
		return node.NodeOutput{}, integration.NewError(integration.KindMalformedResponse, "create meeting: Google Meet returned no meeting link")
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("meeting_link", node.NewStringValue(created.MeetingURI))
	out.SetPort("meeting_code", node.NewStringValue(created.MeetingCode))
	out.SetPort("space", node.NewStringValue(created.Name))
	return out, nil
}

// Connector builds a Meet client around a resolved Google credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Google Meet with its executable node. creds is the runtime
// credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	n, err := integration.NewActionNode(in, "create_meeting", creds, Connector(o), createMeeting)
	if err != nil {
		return integration.Module{}, err
	}
	return integration.Module{Integration: in, Nodes: map[string]node.Node{"create_meeting": n}}, nil
}
