package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
	oauthnotion "workflow-optimizer/internal/oauth/notion"
)

// DefaultBaseURL is the Notion REST API root.
const DefaultBaseURL = "https://api.notion.com/v1"

// DefaultTimeout bounds one API request.
const DefaultTimeout = 30 * time.Second

const maxResponseBytes = 16 << 20

// Options configure the Notion client (tests point BaseURL at a mock API).
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is the Notion REST client for one action call. It holds the access
// token in memory only, sends it only as the Authorization header to the
// Notion API, and never logs.
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

// apiError is Notion's error body. Only the code is read; the message is
// untrusted and never copied into errors.
type apiError struct {
	Code string `json:"code"`
}

// classify maps an unsuccessful response to the integration error model.
func classify(resp *http.Response, body []byte, what string) *integration.Error {
	retryAfter, _ := node.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	e := integration.FromHTTPStatus(resp.StatusCode, retryAfter, what+" failed")
	var a apiError
	_ = json.Unmarshal(body, &a)
	switch e.Kind {
	case integration.KindAuthentication:
		e.Message = what + " failed: Notion rejected the connection; reconnect the account"
	case integration.KindPermissionDenied:
		e.Message = what + " failed: the Notion connection is not allowed to do this"
	case integration.KindNotFound:
		e.Message = what + " failed: not found, or not shared with the integration (in Notion: open it, ••• menu, Connections, add the integration)"
	case integration.KindInvalidRequest:
		if a.Code == "validation_error" {
			e.Message = what + " failed: Notion rejected a value (check the property names and value formats)"
		}
	}
	return e
}

// do sends one request and decodes a JSON answer into out (nil: ignored).
func (c *Client) do(ctx context.Context, method, path string, in, out any, what string) error {
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
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return integration.NewError(integration.KindInvalidRequest, what+": invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token.Reveal())
	req.Header.Set("Notion-Version", oauthnotion.APIVersion)
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
		return integration.NewError(integration.KindMalformedResponse, what+": Notion returned a malformed response")
	}
	return nil
}

var (
	hex32   = regexp.MustCompile(`[0-9a-fA-F]{32}$`)
	dashed  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	plainID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
)

// ObjectID accepts a Notion link or ID (32 hex digits, with or without
// dashes) and returns the dashed lowercase ID. In a link, the ID ends the
// path (the ?v= view ID is ignored).
func ObjectID(what, s string) (string, error) {
	s = strings.TrimSpace(s)
	raw := ""
	switch {
	case dashed.MatchString(s):
		raw = strings.ReplaceAll(s, "-", "")
	case plainID.MatchString(s):
		raw = s
	default:
		// A link ends with the ID: /workspace/Title-<id> or /<id>. The
		// title's letters can be hex digits too, so only the end counts.
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			segment := u.Path[strings.LastIndex(u.Path, "/")+1:]
			raw = hex32.FindString(strings.ReplaceAll(segment, "-", ""))
		}
	}
	if raw == "" {
		return "", integration.NewError(integration.KindInvalidRequest, "the "+what+" must be a Notion link or ID")
	}
	raw = strings.ToLower(raw)
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
}

// Property is one column of a data source's schema.
type Property struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// DataSource is a database's table: its ID and schema.
type DataSource struct {
	ID         string              `json:"id"`
	Properties map[string]Property `json:"properties"`
}

// Resolve finds the data source behind a database link or ID (the
// database's first data source), or takes the ID as a data source ID.
func (c *Client) Resolve(ctx context.Context, id string) (DataSource, error) {
	var db struct {
		DataSources []struct {
			ID string `json:"id"`
		} `json:"data_sources"`
	}
	err := c.do(ctx, http.MethodGet, "/databases/"+url.PathEscape(id), nil, &db, "open database")
	var ie *integration.Error
	switch {
	case err == nil && len(db.DataSources) > 0:
		id = db.DataSources[0].ID
	case err == nil:
		return DataSource{}, integration.NewError(integration.KindMalformedResponse, "open database: Notion returned no data source")
	case errors.As(err, &ie) && ie.Kind == integration.KindNotFound:
		// Not a database ID: perhaps a data source ID (from Search).
	default:
		return DataSource{}, err
	}
	var ds DataSource
	if err := c.do(ctx, http.MethodGet, "/data_sources/"+url.PathEscape(id), nil, &ds, "open database"); err != nil {
		return DataSource{}, err
	}
	if ds.ID == "" {
		ds.ID = id
	}
	return ds, nil
}

// Page is a Notion page (a database row) with its raw properties.
type Page struct {
	Object         string                     `json:"object"`
	ID             string                     `json:"id"`
	URL            string                     `json:"url"`
	CreatedTime    string                     `json:"created_time"`
	LastEditedTime string                     `json:"last_edited_time"`
	Properties     map[string]json.RawMessage `json:"properties"`
	Title          []richText                 `json:"title"` // data sources in search results
	Parent         struct {
		DatabaseID string `json:"database_id"`
	} `json:"parent"`
}

type richText struct {
	PlainText string `json:"plain_text"`
}

// Query returns up to max rows of a data source.
func (c *Client) Query(ctx context.Context, dataSourceID string, filter any, sorts []any, max int) ([]Page, error) {
	body := map[string]any{"page_size": max}
	if filter != nil {
		body["filter"] = filter
	}
	if len(sorts) > 0 {
		body["sorts"] = sorts
	}
	var out struct {
		Results []Page `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/data_sources/"+url.PathEscape(dataSourceID)+"/query", body, &out, "query database"); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// Search finds pages ("page") or databases ("data_source") shared with the
// integration whose title matches query; kind "" finds both.
func (c *Client) Search(ctx context.Context, query, kind string, max int) ([]Page, error) {
	body := map[string]any{"page_size": max}
	if query != "" {
		body["query"] = query
	}
	if kind != "" {
		body["filter"] = map[string]any{"property": "object", "value": kind}
	}
	var out struct {
		Results []Page `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/search", body, &out, "search"); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// CreatePage adds a row to a data source.
func (c *Client) CreatePage(ctx context.Context, dataSourceID string, properties map[string]any, children []any) (Page, error) {
	body := map[string]any{"parent": map[string]any{"type": "data_source_id", "data_source_id": dataSourceID}, "properties": properties}
	if len(children) > 0 {
		body["children"] = children
	}
	var out Page
	err := c.do(ctx, http.MethodPost, "/pages", body, &out, "add row")
	return out, err
}

// GetPage loads a page.
func (c *Client) GetPage(ctx context.Context, id string) (Page, error) {
	var out Page
	err := c.do(ctx, http.MethodGet, "/pages/"+url.PathEscape(id), nil, &out, "load page")
	return out, err
}

// UpdatePage changes a page's properties.
func (c *Client) UpdatePage(ctx context.Context, id string, properties map[string]any) (Page, error) {
	var out Page
	err := c.do(ctx, http.MethodPatch, "/pages/"+url.PathEscape(id), map[string]any{"properties": properties}, &out, "update row")
	return out, err
}
