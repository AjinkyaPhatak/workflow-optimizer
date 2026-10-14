package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for HTTP nodes.
const NodeType = "http"

// Limits.
const (
	DefaultTimeout   = 30 * time.Second
	MinTimeoutMS     = 1000
	MaxTimeoutMS     = 120000
	MaxResponseBytes = 10 << 20
	MaxRequestBytes  = 5 << 20
	maxRedirects     = 5
)

// userAgent identifies the platform to the APIs it calls.
const userAgent = "WorkflowOptimizer/1.0"

func str(name, label, desc string) node.ConfigField {
	return node.NewConfigField(name, node.ValueTypeString, false, "", desc).WithLabel(label)
}

// Definition returns the canonical metadata definition for an HTTP node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "HTTP Request",
		Description: "Calls any HTTP API: method, URL, query, headers, a JSON, form or text body, and an optional saved credential.",
		Category:    node.CategoryIntegration,
		// A request may be a non-idempotent write. Failures of GET, HEAD,
		// PUT and DELETE (idempotent by HTTP semantics) and refusals (429)
		// are marked NotApplied, so the engine still re-runs those.
		SideEffects: node.SideEffectsUnsafe,
		Auth:        &node.AuthRequirement{Required: false, Provider: credential.ProviderHTTP, CredentialType: string(credential.TypeAPIKey)},
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("url", node.ValueTypeJSON, false, "Any upstream data; text is used as the URL (overriding the URL setting)"),
			node.NewPortDefinition("body", node.ValueTypeJSON, false, "Request body, used when the Body setting is empty"),
			node.NewPortDefinition("headers", node.ValueTypeJSON, false, "An object of extra headers (the Headers setting wins on conflicts)"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("response", node.ValueTypeJSON, true, "Response body: parsed JSON, or text"),
			node.NewPortDefinition("status_code", node.ValueTypeNumber, true, "HTTP status code"),
			node.NewPortDefinition("headers", node.ValueTypeObject, true, "Response headers (lowercase names)"),
			node.NewPortDefinition("ok", node.ValueTypeBoolean, true, "Whether the status is 2xx"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("method", node.ValueTypeString, true, "GET", "HTTP method").WithLabel("Method").WithOptions(
				node.Option("GET", "GET"), node.Option("POST", "POST"), node.Option("PUT", "PUT"),
				node.Option("PATCH", "PATCH"), node.Option("DELETE", "DELETE"), node.Option("HEAD", "HEAD")),
			node.NewConfigField("url", node.ValueTypeString, false, "", "https://api.example.com/v1/items. {{variables}} are filled in.").WithLabel("URL"),
			node.NewConfigField("query", node.ValueTypeObject, false, nil, `Query parameters, e.g. {"q": "{{input.term}}", "limit": 10}. {{variables}} are filled in.`).WithLabel("Query parameters"),
			node.NewConfigField("headers", node.ValueTypeObject, false, nil, `Headers, e.g. {"Accept": "application/json"}. Put secrets in a credential, not here.`).WithLabel("Headers"),
			node.NewConfigField("body_type", node.ValueTypeString, false, "json", "How the body is sent.").WithLabel("Body type").WithOptions(
				node.Option("json", "JSON"), node.Option("form", "Form (URL-encoded)"), node.Option("text", "Text"), node.Option("none", "No body")),
			node.NewConfigField("body", node.ValueTypeJSON, false, nil, "JSON value, an object for a form, or text. {{variables}} are filled in.").WithLabel("Body"),
			node.NewConfigField(node.CredentialConfigField, node.ValueTypeString, false, "",
				"An HTTP credential: sent only to its allowed hosts. Bearer token: Authorization header; basic auth: username:password; API key: the header or query parameter below.").WithLabel("Credential"),
			str("api_key_name", "API key name", "Header (or query parameter) that carries an API key credential. Default: X-API-Key."),
			node.NewConfigField("api_key_in", node.ValueTypeString, false, "header", "Where an API key credential goes.").WithLabel("API key in").WithOptions(
				node.Option("header", "Header"), node.Option("query", "Query parameter")),
			node.NewConfigField("response_format", node.ValueTypeString, false, "auto", "How the response body is read.").WithLabel("Response").WithOptions(
				node.Option("auto", "Automatic (JSON when the server says so)"), node.Option("json", "JSON"), node.Option("text", "Text")),
			node.NewConfigField("fail_on_error", node.ValueTypeBoolean, false, true,
				"Fail the node on a 4xx or 5xx status. Off: the node succeeds and outputs the status and response, for your own handling.").WithLabel("Fail on error status"),
			node.NewConfigField("timeout_ms", node.ValueTypeNumber, false, 30000.0, "Request timeout in milliseconds.").WithLabel("Timeout (ms)").
				WithRange(node.Float(MinTimeoutMS), node.Float(MaxTimeoutMS), node.Float(1)),
		},
	}
}

// Options configure the node's dependencies.
type Options struct {
	// Credentials resolves HTTP credentials (nil: requests with a
	// credential fail with a configuration error).
	Credentials credential.Resolver
	// AllowPrivateNetworks permits requests to loopback, private and
	// link-local addresses (HTTP_ALLOW_PRIVATE_NETWORKS; off by default so
	// workflows cannot reach the server's own network, e.g. the database
	// or a cloud metadata service).
	AllowPrivateNetworks bool
	// Transport replaces the network transport (tests). The private-network
	// check is part of the default transport's dialer.
	Transport stdhttp.RoundTripper
}

// Node is the executable HTTP request node.
type Node struct {
	creds     credential.Resolver
	transport stdhttp.RoundTripper
}

// New constructs an HTTP node without credentials and with the default,
// private-network-blocking transport.
func New() *Node { return NewWith(Options{}) }

// NewWith constructs an HTTP node.
func NewWith(o Options) *Node {
	t := o.Transport
	if t == nil {
		t = newTransport(o.AllowPrivateNetworks)
	}
	return &Node{creds: o.Credentials, transport: t}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string { return NodeType }

// errBlocked is returned by the dialer for a forbidden destination.
var errBlocked = errors.New("destination address is not allowed")

var cgnat = netip.MustParsePrefix("100.64.0.0/10")
var thisNetwork = netip.MustParsePrefix("0.0.0.0/8")

// Blocked reports whether an address is loopback, private, link-local,
// shared (CGNAT), unspecified or multicast.
func Blocked(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || cgnat.Contains(a) || thisNetwork.Contains(a) || a == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// newTransport is a transport without proxies whose dialer checks each
// resolved address just before connecting (so DNS answers and redirects
// cannot reach a blocked address).
func newTransport(allowPrivate bool) *stdhttp.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || Blocked(ap.Addr()) {
				return errBlocked
			}
			return nil
		}
	}
	return &stdhttp.Transport{
		Proxy: nil, DialContext: d.DialContext, ForceAttemptHTTP2: true,
		MaxIdleConns: 50, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func text(in node.NodeInput, name string) string {
	s, _ := in.GetStringConfig(name)
	return strings.TrimSpace(s)
}

func idempotent(method string) bool {
	switch method {
	case stdhttp.MethodGet, stdhttp.MethodHead, stdhttp.MethodPut, stdhttp.MethodDelete:
		return true
	}
	return false
}

func valueText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64, bool:
		b, _ := json.Marshal(t)
		return string(b)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// object reads an object setting (nil when unset).
func object(in node.NodeInput, name string) (map[string]any, error) {
	raw, ok := in.GetConfig(name)
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, configError(name + " must be an object of names and values")
	}
	return m, nil
}

var forbiddenHeaders = map[string]bool{"host": true, "content-length": true, "transfer-encoding": true, "connection": true}

// request is everything one call needs.
type request struct {
	method  string
	url     *url.URL
	headers stdhttp.Header
	body    []byte
	cred    *credential.ResolvedCredential
	timeout time.Duration
}

func (n *Node) build(ctx context.Context, in node.NodeInput) (request, error) {
	r := request{method: strings.ToUpper(text(in, "method")), headers: stdhttp.Header{}, timeout: DefaultTimeout}
	if r.method == "" {
		r.method = stdhttp.MethodGet
	}
	switch r.method {
	case stdhttp.MethodGet, stdhttp.MethodPost, stdhttp.MethodPut, stdhttp.MethodPatch, stdhttp.MethodDelete, stdhttp.MethodHead:
	default:
		return r, configError("method must be GET, POST, PUT, PATCH, DELETE or HEAD")
	}
	if f, ok := in.GetNumberConfig("timeout_ms"); ok {
		if f < MinTimeoutMS || f > MaxTimeoutMS {
			return r, configError(fmt.Sprintf("timeout_ms must be from %d to %d", MinTimeoutMS, MaxTimeoutMS))
		}
		r.timeout = time.Duration(f) * time.Millisecond
	}

	raw := text(in, "url")
	if v, ok := in.GetPort("url"); ok {
		if s, ok := v.String(); ok && strings.TrimSpace(s) != "" {
			raw = strings.TrimSpace(s)
		}
	}
	if raw == "" {
		return r, configError("set the URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return r, configError("the URL must be an absolute http:// or https:// URL")
	}
	if u.User != nil {
		return r, configError("put credentials in a saved credential, not in the URL")
	}
	query, err := object(in, "query")
	if err != nil {
		return r, err
	}
	if len(query) > 0 {
		q := u.Query()
		for k, v := range query {
			if list, ok := v.([]any); ok {
				for _, x := range list {
					q.Add(k, valueText(x))
				}
				continue
			}
			q.Set(k, valueText(v))
		}
		u.RawQuery = q.Encode()
	}
	r.url = u

	// Headers: connected object first, the setting wins.
	if v, ok := in.GetPort("headers"); ok {
		if m, ok := v.Data.(map[string]any); ok {
			for k, x := range m {
				r.headers.Set(k, valueText(x))
			}
		}
	}
	hs, err := object(in, "headers")
	if err != nil {
		return r, err
	}
	for k, x := range hs {
		r.headers.Set(k, valueText(x))
	}
	for k := range r.headers {
		if forbiddenHeaders[strings.ToLower(k)] {
			return r, configError("the " + k + " header is set automatically")
		}
	}

	// Body.
	bodyType := text(in, "body_type")
	if bodyType == "" {
		bodyType = "json"
	}
	body, hasBody := in.GetConfig("body")
	if !hasBody || body == nil || body == "" {
		hasBody = false
		if v, ok := in.GetPort("body"); ok && v.Data != nil {
			body, hasBody = v.Data, true
		}
	}
	if r.method == stdhttp.MethodGet || r.method == stdhttp.MethodHead {
		hasBody = false
	}
	if hasBody && bodyType != "none" {
		switch bodyType {
		case "json":
			b, err := json.Marshal(body)
			if err != nil {
				return r, configError("the body is not valid JSON")
			}
			r.body = b
			setDefault(r.headers, "Content-Type", "application/json")
		case "form":
			m, ok := body.(map[string]any)
			if !ok {
				return r, configError("a form body must be an object of fields")
			}
			form := url.Values{}
			for k, v := range m {
				form.Set(k, valueText(v))
			}
			r.body = []byte(form.Encode())
			setDefault(r.headers, "Content-Type", "application/x-www-form-urlencoded")
		case "text":
			r.body = []byte(valueText(body))
			setDefault(r.headers, "Content-Type", "text/plain; charset=utf-8")
		default:
			return r, configError("body_type must be json, form, text or none")
		}
		if len(r.body) > MaxRequestBytes {
			return r, configError("the request body is too large")
		}
	}
	setDefault(r.headers, "User-Agent", userAgent)
	setDefault(r.headers, "Accept", "application/json, text/plain;q=0.9, */*;q=0.8")
	if in.IdempotencyKey != "" && !idempotent(r.method) {
		// Lets APIs that support it (Stripe-style) drop a repeated write.
		setDefault(r.headers, "Idempotency-Key", in.IdempotencyKey)
	}

	// Credential, applied last so it wins over any header setting.
	if id := text(in, node.CredentialConfigField); id != "" {
		if err := n.authorize(ctx, in, id, &r); err != nil {
			return r, err
		}
	}
	return r, nil
}

func setDefault(h stdhttp.Header, k, v string) {
	if h.Get(k) == "" {
		h.Set(k, v)
	}
}

func (n *Node) authorize(ctx context.Context, in node.NodeInput, id string, r *request) error {
	if n.creds == nil {
		return configError("credentials are not available on this server")
	}
	credID, err := uuid.Parse(id)
	if err != nil {
		return node.NewNodeError(integration.ErrCodeCredentialInvalid, node.CredentialConfigField+" is not a valid identifier", false)
	}
	cred, err := n.creds.Resolve(ctx, in.Scope.WorkspaceID, credID, credential.ProviderHTTP)
	if err != nil {
		return integration.ClassifyCredentialError(err)
	}
	if !credential.HostAllowed(cred.AllowedHosts, r.url.Hostname()) {
		return node.NewNodeError("CREDENTIAL_HOST_NOT_ALLOWED",
			fmt.Sprintf("this credential may only be sent to %s, not %s", strings.Join(cred.AllowedHosts, ", "), r.url.Hostname()), false)
	}
	secret := cred.Secret.Reveal()
	switch cred.Type {
	case credential.TypeBearerToken:
		r.headers.Set("Authorization", "Bearer "+secret)
	case credential.TypeBasicAuth:
		r.headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(secret)))
	case credential.TypeAPIKey:
		name := text(in, "api_key_name")
		if name == "" {
			name = "X-API-Key"
		}
		if text(in, "api_key_in") == "query" {
			q := r.url.Query()
			q.Set(name, secret)
			r.url.RawQuery = q.Encode()
		} else {
			if forbiddenHeaders[strings.ToLower(name)] {
				return configError("api_key_name cannot be " + name)
			}
			r.headers.Set(name, secret)
		}
	default:
		return node.NewNodeError(integration.ErrCodeCredentialInvalid, "this credential type cannot be used for HTTP requests", false)
	}
	r.cred = &cred
	return nil
}

// failure classifies a transport error. Only idempotent methods are marked
// NotApplied: a write may have reached the server before the connection
// broke.
func failure(method string, err error) error {
	if errors.Is(err, errBlocked) {
		return node.NewNodeError(node.ErrCodeInvalidInput,
			"requests to private network addresses (localhost, 10.x, 192.168.x, cloud metadata…) are blocked; set HTTP_ALLOW_PRIVATE_NETWORKS=true to allow them, e.g. for local development", false)
	}
	var ne net.Error
	e := node.WrapNodeError(node.ErrCodeUnavailable, "the server could not be reached", true, nil)
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		e = node.WrapNodeError(node.ErrCodeTimeout, "the request timed out", true, nil)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		e = node.NewNodeError(node.ErrCodeInvalidInput, "the host name does not exist", false)
	}
	e.Source = node.SourceProvider
	e.NotApplied = idempotent(method)
	return e
}

// Execute performs the request.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	r, err := n.build(ctx, in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	req, err := stdhttp.NewRequestWithContext(ctx, r.method, r.url.String(), bytes.NewReader(r.body))
	if err != nil {
		return node.NodeOutput{}, configError("the request could not be built")
	}
	if r.body == nil {
		req.Body, req.ContentLength = stdhttp.NoBody, 0
	}
	req.Header = r.headers
	origin := r.url.Host
	client := &stdhttp.Client{Transport: n.transport, CheckRedirect: func(next *stdhttp.Request, via []*stdhttp.Request) error {
		if len(via) >= maxRedirects {
			return stdhttp.ErrUseLastResponse
		}
		if next.URL.Scheme != "https" && next.URL.Scheme != "http" {
			return stdhttp.ErrUseLastResponse
		}
		// A credential never follows a redirect to another host: the 3xx
		// response is returned instead.
		if r.cred != nil && next.URL.Host != origin {
			return stdhttp.ErrUseLastResponse
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		if pctx := ctx.Err(); pctx != nil && errors.Is(pctx, context.Canceled) {
			return node.NodeOutput{}, pctx
		}
		return node.NodeOutput{}, failure(r.method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return node.NodeOutput{}, failure(r.method, err)
	}
	if len(data) > MaxResponseBytes {
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeExecutionFailed, "the response is larger than 10 MB", false)
	}

	if fail, ok := in.GetBooleanConfig("fail_on_error"); (!ok || fail) && resp.StatusCode >= 400 {
		retryAfter, _ := node.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		e := node.HTTPStatusError(resp.StatusCode, retryAfter,
			fmt.Sprintf("the server answered HTTP %d (turn off Fail on error status to inspect the response)", resp.StatusCode))
		if idempotent(r.method) {
			e.NotApplied = true
		}
		return node.NodeOutput{}, e
	}

	headers := map[string]any{}
	names := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if strings.EqualFold(k, "Set-Cookie") {
			continue // session material is not workflow data
		}
		headers[strings.ToLower(k)] = strings.Join(resp.Header.Values(k), ", ")
	}
	body, err := decode(data, resp.Header.Get("Content-Type"), text(in, "response_format"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("response", node.NewJSONValue(body))
	out.SetPort("status_code", node.NewNumberValue(float64(resp.StatusCode)))
	out.SetPort("headers", node.NewObjectValue(headers))
	out.SetPort("ok", node.NewBooleanValue(resp.StatusCode >= 200 && resp.StatusCode <= 299))
	return out, nil
}

// decode reads a response body: JSON when asked for (or announced, in auto
// mode, falling back to text when it does not parse), else text; bytes that
// are not UTF-8 text come back base64-encoded.
func decode(data []byte, contentType, format string) (any, error) {
	if len(data) == 0 {
		return "", nil
	}
	ct := strings.ToLower(contentType)
	wantJSON := format == "json" || (format == "" || format == "auto") && (strings.Contains(ct, "json") || ct == "" && json.Valid(data))
	if wantJSON {
		var v any
		if err := json.Unmarshal(data, &v); err == nil {
			return v, nil
		}
		if format == "json" {
			return nil, node.NewNodeError(node.ErrCodeExecutionFailed, "the response is not valid JSON", false)
		}
	}
	if !utf8.Valid(data) {
		return map[string]any{"base64": base64.StdEncoding.EncodeToString(data), "content_type": contentType, "size": len(data)}, nil
	}
	return string(data), nil
}
