package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// DefaultBaseURL is the Gmail REST API root.
const DefaultBaseURL = "https://gmail.googleapis.com/gmail/v1"

// DefaultTimeout bounds one Gmail API request.
const DefaultTimeout = 30 * time.Second

// maxResponseBytes bounds a Gmail response read into memory.
const maxResponseBytes = 32 << 20

// Options configure the Gmail client (tests point BaseURL at a mock API).
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is the Gmail REST client for one action call. It holds the
// resolved access token in memory only, sends it only as the Authorization
// header to the Gmail API, and never logs. It does not refresh tokens: the
// token manager handed it a valid one.
type Client struct {
	base    string
	http    *http.Client
	timeout time.Duration
	token   credential.Secret
}

// NewClient returns a client with the access token of a resolved credential.
func NewClient(o Options, token credential.Secret) *Client {
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{base: base, http: hc, timeout: timeout, token: token}
}

// apiError is Gmail's error body. Only the reasons are read (to tell rate
// limiting from permission problems on 403); messages are untrusted and are
// never copied into errors.
type apiError struct {
	Error struct {
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Status string `json:"status"`
	} `json:"error"`
}

var rateLimitReasons = map[string]bool{"rateLimitExceeded": true, "userRateLimitExceeded": true, "dailyLimitExceeded": false, "quotaExceeded": false}

// classify maps an unsuccessful response to the integration error model.
func classify(resp *http.Response, body []byte, what string) error {
	retryAfter, _ := node.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	e := integration.FromHTTPStatus(resp.StatusCode, retryAfter, what+" failed")
	if resp.StatusCode == http.StatusForbidden {
		var a apiError
		if json.Unmarshal(body, &a) == nil {
			for _, r := range a.Error.Errors {
				if rateLimitReasons[r.Reason] {
					// Gmail reports per-user rate limits as 403.
					e = integration.FromHTTPStatus(http.StatusTooManyRequests, retryAfter, what+" was rate limited")
					e.StatusCode = http.StatusForbidden
				}
			}
		}
	}
	switch e.Kind {
	case integration.KindAuthentication:
		e.Message = what + " failed: Google rejected the account's access; reconnect the account"
	case integration.KindPermissionDenied:
		e.Message = what + " failed: the account did not grant this permission"
	case integration.KindNotFound:
		e.Message = what + " failed: not found"
	}
	return e
}

// do sends one request and decodes a JSON answer into out (nil: ignored).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any, what string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return integration.NewError(integration.KindInvalidRequest, what+": the request could not be encoded")
		}
		body = bytes.NewReader(b)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return integration.NewError(integration.KindInvalidRequest, what+": invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token.Reveal())
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// A transport error (its text can contain the URL, never the
		// header): integration.ToNodeError classifies timeouts and network
		// failures without printing it.
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classify(resp, data, what)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return integration.NewError(integration.KindMalformedResponse, what+": Gmail returned a malformed response")
	}
	return nil
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
