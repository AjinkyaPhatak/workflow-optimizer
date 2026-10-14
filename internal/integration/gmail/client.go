package gmail

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration/googleapi"
	"workflow-optimizer/internal/integration"
)

// DefaultBaseURL is the Gmail REST API root.
const DefaultBaseURL = "https://gmail.googleapis.com/gmail/v1"

// Options configure the Gmail client (tests point BaseURL at a mock API).
type Options = googleapi.Options

// Client is the Gmail REST client for one action call, on the shared Google
// REST plumbing (googleapi): the access token stays in memory and is sent
// only as the Authorization header to the Gmail API.
type Client struct {
	api *googleapi.Client
}

// NewClient returns a client with the access token of a resolved credential.
func NewClient(o Options, token credential.Secret) *Client {
	return &Client{api: googleapi.New("Gmail", DefaultBaseURL, o, token)}
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any, what string) error {
	return c.api.Do(ctx, method, path, query, in, out, what)
}

// listResponse is users.messages.list.
type listResponse struct {
	Messages []struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"messages"`
}

// Search lists up to max messages matching a Gmail query and loads their
// metadata (From, Subject, Date, snippet, labels).
func (c *Client) Search(ctx context.Context, query string, max int) ([]apiMessage, error) {
	var l listResponse
	q := url.Values{"maxResults": {fmt.Sprint(max)}}
	if strings.TrimSpace(query) != "" {
		q.Set("q", query)
	}
	if err := c.do(ctx, http.MethodGet, "/users/me/messages", q, nil, &l, "search"); err != nil {
		return nil, err
	}
	out := make([]apiMessage, 0, len(l.Messages))
	for _, m := range l.Messages {
		if m.ID == "" {
			return nil, integration.NewError(integration.KindMalformedResponse, "search: Gmail returned a message without an ID")
		}
		full, err := c.Get(ctx, m.ID, "metadata")
		if err != nil {
			return nil, err
		}
		out = append(out, full)
	}
	return out, nil
}

// Get loads one message ("full" or "metadata" format).
func (c *Client) Get(ctx context.Context, id, format string) (apiMessage, error) {
	q := url.Values{"format": {format}}
	if format == "metadata" {
		q["metadataHeaders"] = []string{"From", "To", "Cc", "Subject", "Date", "Message-ID", "References", "Reply-To"}
	}
	var m apiMessage
	if err := c.do(ctx, http.MethodGet, "/users/me/messages/"+url.PathEscape(id), q, nil, &m, "read message"); err != nil {
		return apiMessage{}, err
	}
	if m.ID == "" || m.Payload == nil {
		return apiMessage{}, integration.NewError(integration.KindMalformedResponse, "read message: Gmail returned a malformed message")
	}
	return m, nil
}

type sentMessage struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type rawMessage struct {
	Raw      string `json:"raw"`
	ThreadID string `json:"threadId,omitempty"`
}

// Send sends a message (in threadID, for replies).
func (c *Client) Send(ctx context.Context, rfc822 []byte, threadID string) (sentMessage, error) {
	var out sentMessage
	if err := c.do(ctx, http.MethodPost, "/users/me/messages/send", nil, rawMessage{Raw: Raw(rfc822), ThreadID: threadID}, &out, "send"); err != nil {
		return sentMessage{}, err
	}
	if out.ID == "" {
		return sentMessage{}, integration.NewError(integration.KindMalformedResponse, "send: Gmail returned no message ID")
	}
	return out, nil
}

type draft struct {
	ID      string      `json:"id"`
	Message sentMessage `json:"message"`
}

// CreateDraft stores a draft.
func (c *Client) CreateDraft(ctx context.Context, rfc822 []byte) (draft, error) {
	var out draft
	in := map[string]any{"message": rawMessage{Raw: Raw(rfc822)}}
	if err := c.do(ctx, http.MethodPost, "/users/me/drafts", nil, in, &out, "create draft"); err != nil {
		return draft{}, err
	}
	if out.ID == "" {
		return draft{}, integration.NewError(integration.KindMalformedResponse, "create draft: Gmail returned no draft ID")
	}
	return out, nil
}
