// Package googleapi is the REST plumbing shared by the Google integrations
// (Gmail, Sheets, Calendar): one JSON request with the resolved access token,
// a bounded response read, and classification of Google's error answers into
// the integration error model (Phase C1).
//
// It holds the access token in memory only, sends it only as the
// Authorization header to the configured API root, and never logs. It does
// not refresh tokens: the token manager handed it a valid one.
package googleapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// DefaultTimeout bounds one API request.
const DefaultTimeout = 30 * time.Second

// maxResponseBytes bounds a response read into memory.
const maxResponseBytes = 32 << 20

// Options configure a client (tests point BaseURL at a mock API).
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client calls one Google REST API for one action call.
type Client struct {
	service string // "Gmail", "Google Sheets": used in error messages
	base    string
	http    *http.Client
	timeout time.Duration
	token   credential.Secret
}

// New returns a client for service rooted at o.BaseURL (else defaultBase).
func New(service, defaultBase string, o Options, token credential.Secret) *Client {
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = defaultBase
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{service: service, base: base, http: hc, timeout: timeout, token: token}
}

// apiError is Google's error body. Only the reasons and status are read (to
// tell rate limiting and missing scopes from other 403s); messages are
// untrusted and are never copied into errors.
type apiError struct {
	Error struct {
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Status  string `json:"status"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

var rateLimitReasons = map[string]bool{"rateLimitExceeded": true, "userRateLimitExceeded": true, "RATE_LIMIT_EXCEEDED": true}

// Classify maps an unsuccessful response to the integration error model.
// what names the operation ("search", "append row").
func Classify(resp *http.Response, body []byte, what string) *integration.Error {
	retryAfter, _ := node.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	e := integration.FromHTTPStatus(resp.StatusCode, retryAfter, what+" failed")
	scopes := false
	if resp.StatusCode == http.StatusForbidden {
		var a apiError
		if json.Unmarshal(body, &a) == nil {
			reasons := []string{}
			for _, r := range a.Error.Errors {
				reasons = append(reasons, r.Reason)
			}
			for _, d := range a.Error.Details {
				reasons = append(reasons, d.Reason)
			}
			for _, r := range reasons {
				switch {
				case rateLimitReasons[r]:
					// Google reports per-user rate limits as 403.
					e = integration.FromHTTPStatus(http.StatusTooManyRequests, retryAfter, what+" was rate limited")
					e.StatusCode = http.StatusForbidden
				case r == "insufficientPermissions" || r == "ACCESS_TOKEN_SCOPE_INSUFFICIENT":
					scopes = true
				}
			}
		}
	}
	switch e.Kind {
	case integration.KindAuthentication:
		e.Message = what + " failed: Google rejected the account's access; reconnect the account"
	case integration.KindPermissionDenied:
		if scopes {
			e.Message = what + " failed: the Google account was connected without this permission; reconnect it on the Connected accounts page"
		} else {
			e.Message = what + " failed: the account has no access to this resource"
		}
	case integration.KindNotFound:
		e.Message = what + " failed: not found"
	}
	return e
}

// Do sends one request to path (relative to the API root) and decodes a JSON
// answer into out (nil: ignored).
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, in, out any, what string) error {
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
		return Classify(resp, data, what)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return integration.NewError(integration.KindMalformedResponse, what+": "+c.service+" returned a malformed response")
	}
	return nil
}
