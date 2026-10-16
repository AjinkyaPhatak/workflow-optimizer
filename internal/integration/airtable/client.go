package airtable

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// DefaultBaseURL is the Airtable Web API root.
const DefaultBaseURL = "https://api.airtable.com/v0"

// DefaultTimeout bounds one API request.
const DefaultTimeout = 30 * time.Second

// rateLimitWait is how long Airtable asks clients to wait after a 429 (it
// sends no Retry-After).
const rateLimitWait = 30 * time.Second

const maxResponseBytes = 16 << 20

// Options configure the Airtable client (tests point BaseURL at a mock API).
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is the Airtable REST client for one action call. It holds the
// access token in memory only, sends it only as the Authorization header to
// the Airtable API, and never logs.
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

// apiError is Airtable's error body: {"error": {"type": ...}} or
// {"error": "NOT_FOUND"}. Only the type is read; messages are untrusted and
// never copied into errors.
type apiError struct {
	Error json.RawMessage `json:"error"`
}

func (a apiError) kind() string {
	var s string
	if json.Unmarshal(a.Error, &s) == nil {
		return s
	}
	var o struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(a.Error, &o)
	return o.Type
}

// classify maps an unsuccessful response to the integration error model.
func classify(resp *http.Response, body []byte, what string) *integration.Error {
	retryAfter, _ := node.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	if resp.StatusCode == http.StatusTooManyRequests && retryAfter <= 0 {
		retryAfter = rateLimitWait
	}
	e := integration.FromHTTPStatus(resp.StatusCode, retryAfter, what+" failed")
	var a apiError
	_ = json.Unmarshal(body, &a)
	typ := a.kind()
	switch {
	case e.Kind == integration.KindAuthentication:
		e.Message = what + " failed: Airtable rejected the connection; reconnect the account"
	case typ == "INVALID_PERMISSIONS_OR_MODEL_NOT_FOUND", e.Kind == integration.KindNotFound:
		// Airtable answers 403 for a base that exists but was not shared.
		e.Kind = integration.KindNotFound
		e.Message = what + " failed: not found, or the base was not shared when Airtable was connected (reconnect Airtable and add the base)"
	case e.Kind == integration.KindPermissionDenied:
		e.Message = what + " failed: the Airtable connection is not allowed to do this"
	case typ == "UNKNOWN_FIELD_NAME":
		e.Message = what + " failed: the table has no field with one of these names (check the field names)"
	case typ == "INVALID_VALUE_FOR_COLUMN", typ == "INVALID_MULTIPLE_CHOICE_OPTIONS":
		e.Message = what + " failed: Airtable rejected a value (check it fits the field's type)"
	case typ == "INVALID_FILTER_BY_FORMULA":
		e.Message = what + " failed: Airtable could not use the search (check the field name)"
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
		return err // classified by integration.ToNodeError, never printed
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
		return integration.NewError(integration.KindMalformedResponse, what+": Airtable returned a malformed response")
	}
	return nil
}

// Record is one Airtable record. Empty fields are left out by Airtable.
type Record struct {
	ID          string         `json:"id,omitempty"`
	CreatedTime string         `json:"createdTime,omitempty"`
	Fields      map[string]any `json:"fields"`
}

// Base is a base the connection can open.
type Base struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Table is a table of a base with its fields.
type Table struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Fields []Field `json:"fields"`
}

// Field is a table's field (column).
type Field struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func tablePath(base, table string) string {
	return "/" + url.PathEscape(base) + "/" + url.PathEscape(table)
}

// List reads up to max records, optionally of a view and matching a
// formula (all pages).
func (c *Client) List(ctx context.Context, base, table, view, formula string, max int) ([]Record, error) {
	var all []Record
	offset := ""
	for {
		q := url.Values{"pageSize": {"100"}, "maxRecords": {strconv.Itoa(max)}}
		if view != "" {
			q.Set("view", view)
		}
		if formula != "" {
			q.Set("filterByFormula", formula)
		}
		if offset != "" {
			q.Set("offset", offset)
		}
		var page struct {
			Records []Record `json:"records"`
			Offset  string   `json:"offset"`
		}
		if err := c.do(ctx, http.MethodGet, tablePath(base, table), q, nil, &page, "list records"); err != nil {
			return nil, err
		}
		all = append(all, page.Records...)
		if page.Offset == "" || len(all) >= max {
			if len(all) > max {
				all = all[:max]
			}
			return all, nil
		}
		offset = page.Offset
	}
}

// Create adds one record. typecast lets Airtable convert text to the
// field's type (dates, numbers, new select options).
func (c *Client) Create(ctx context.Context, base, table string, fields map[string]any) (Record, error) {
	var r Record
	err := c.do(ctx, http.MethodPost, tablePath(base, table), nil, map[string]any{"fields": fields, "typecast": true}, &r, "create record")
	return r, err
}

// Update changes the given fields of one record; others are kept.
func (c *Client) Update(ctx context.Context, base, table, id string, fields map[string]any) (Record, error) {
	var r Record
	err := c.do(ctx, http.MethodPatch, tablePath(base, table)+"/"+url.PathEscape(id), nil, map[string]any{"fields": fields, "typecast": true}, &r, "update record")
	return r, err
}

// Bases lists the bases the connection can open.
func (c *Client) Bases(ctx context.Context) ([]Base, error) {
	var all []Base
	offset := ""
	for range 20 {
		q := url.Values{}
		if offset != "" {
			q.Set("offset", offset)
		}
		var page struct {
			Bases  []Base `json:"bases"`
			Offset string `json:"offset"`
		}
		if err := c.do(ctx, http.MethodGet, "/meta/bases", q, nil, &page, "list bases"); err != nil {
			return nil, err
		}
		all = append(all, page.Bases...)
		if page.Offset == "" {
			break
		}
		offset = page.Offset
	}
	return all, nil
}

// Tables lists a base's tables with their fields.
func (c *Client) Tables(ctx context.Context, base string) ([]Table, error) {
	var out struct {
		Tables []Table `json:"tables"`
	}
	err := c.do(ctx, http.MethodGet, "/meta/bases/"+url.PathEscape(base)+"/tables", nil, nil, &out, "list tables")
	return out.Tables, err
}

var (
	baseRE   = regexp.MustCompile(`\bapp[A-Za-z0-9]{14}\b`)
	tableRE  = regexp.MustCompile(`\btbl[A-Za-z0-9]{14}\b`)
	recordRE = regexp.MustCompile(`^rec[A-Za-z0-9]{14}$`)
)

// BaseID accepts a base ID or an Airtable link to the base (or a table or
// view in it). The second result is the table ID in the link, if any.
func BaseID(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	base := baseRE.FindString(s)
	if base == "" {
		return "", "", integration.NewError(integration.KindInvalidRequest, "the base must be an Airtable link or a base ID (it starts with app)")
	}
	return base, tableRE.FindString(s), nil
}

// RecordID checks a record ID (it starts with rec).
func RecordID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !recordRE.MatchString(s) {
		return "", integration.NewError(integration.KindInvalidRequest, "the record ID must start with rec, e.g. from Find Records")
	}
	return s, nil
}

// MatchFormula is an Airtable formula matching records whose field, as
// text, equals value (surrounding spaces ignored).
func MatchFormula(field, value string) (string, error) {
	if strings.ContainsAny(field, "{}") {
		return "", integration.NewError(integration.KindInvalidRequest, "field names with { or } cannot be searched")
	}
	quoted := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(strings.TrimSpace(value))
	return "TRIM({" + strings.TrimSpace(field) + "}&'')='" + quoted + "'", nil
}
