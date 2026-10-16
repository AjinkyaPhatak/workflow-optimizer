// Package docs is the Google Docs integration (Phase J): three actions on
// the C1 integration framework, authenticated with Google connected
// accounts (C2, scope documents).
//
//	read_document:   text, title, document_id, link
//	create_document: document_id, link, title
//	append_text:     document_id, link
//
// Documents are plain text to workflows: read_document returns the body's
// text (paragraphs, lists and table cells, one per line), and the write
// actions add plain text. Neither write can be deduplicated by Google, so
// both are unsafe: a retry after an attempt that applied would add a second
// document or a second copy of the text.
package docs

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/googleapi"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: google_docs.read_document, .create_document,
// .append_text.
const ID = "google_docs"

// Provider is the OAuth provider whose connected accounts Docs uses.
const Provider = "google"

// DefaultBaseURL is the Google Docs REST API root.
const DefaultBaseURL = "https://docs.googleapis.com/v1"

// MaxTextBytes bounds the text one action writes.
const MaxTextBytes = 1 << 20

// Options configure the Docs client (tests point BaseURL at a mock API).
type Options = googleapi.Options

// Client is the Google Docs REST client for one action call.
type Client struct {
	api *googleapi.Client
}

// NewClient returns a client with the access token of a resolved credential.
func NewClient(o Options, token credential.Secret) *Client {
	return &Client{api: googleapi.New("Google Docs", DefaultBaseURL, o, token)}
}

// Document is the subset of a Google Docs document the actions read.
type Document struct {
	DocumentID string `json:"documentId,omitempty"`
	Title      string `json:"title,omitempty"`
	Body       *Body  `json:"body,omitempty"`
}

// Body is a document's main text.
type Body struct {
	Content []StructuralElement `json:"content"`
}

// StructuralElement is a paragraph, a table or a section break.
type StructuralElement struct {
	Paragraph *Paragraph `json:"paragraph,omitempty"`
	Table     *Table     `json:"table,omitempty"`
}

// Paragraph is a run of text elements.
type Paragraph struct {
	Elements []ParagraphElement `json:"elements"`
}

// ParagraphElement holds text (other elements, like images, have none).
type ParagraphElement struct {
	TextRun *TextRun `json:"textRun,omitempty"`
}

// TextRun is text with one style.
type TextRun struct {
	Content string `json:"content"`
}

// Table is rows of cells, each holding structural elements.
type Table struct {
	TableRows []struct {
		TableCells []struct {
			Content []StructuralElement `json:"content"`
		} `json:"tableCells"`
	} `json:"tableRows"`
}

// Get reads a document.
func (c *Client) Get(ctx context.Context, id string) (Document, error) {
	var d Document
	err := c.api.Do(ctx, http.MethodGet, "/documents/"+url.PathEscape(id), nil, nil, &d, "read document")
	return d, err
}

// Create makes an empty document with a title.
func (c *Client) Create(ctx context.Context, title string) (Document, error) {
	var d Document
	err := c.api.Do(ctx, http.MethodPost, "/documents", nil, Document{Title: title}, &d, "create document")
	return d, err
}

// AppendText adds text at the end of the document's body.
func (c *Client) AppendText(ctx context.Context, id, text string) error {
	body := map[string]any{"requests": []any{map[string]any{
		"insertText": map[string]any{"endOfSegmentLocation": map[string]any{}, "text": text},
	}}}
	return c.api.Do(ctx, http.MethodPost, "/documents/"+url.PathEscape(id)+":batchUpdate", nil, body, nil, "add text")
}

// Text is the document body's plain text: paragraphs as written, and each
// table cell on its own line. The trailing newline Docs keeps is removed.
func (d Document) Text() string {
	if d.Body == nil {
		return ""
	}
	var b strings.Builder
	writeText(&b, d.Body.Content)
	return strings.TrimRight(b.String(), "\n")
}

func writeText(b *strings.Builder, content []StructuralElement) {
	for _, el := range content {
		switch {
		case el.Paragraph != nil:
			for _, pe := range el.Paragraph.Elements {
				if pe.TextRun != nil {
					b.WriteString(pe.TextRun.Content)
				}
			}
		case el.Table != nil:
			for _, row := range el.Table.TableRows {
				for _, cell := range row.TableCells {
					writeText(b, cell.Content)
				}
			}
		}
	}
}

var (
	urlID   = regexp.MustCompile(`/document/(?:u/\d+/)?d/([A-Za-z0-9_-]+)`)
	plainID = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
)

// DocumentID accepts a document ID or its browser URL.
func DocumentID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if m := urlID.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	if plainID.MatchString(s) {
		return s, nil
	}
	return "", integration.NewError(integration.KindInvalidRequest, "the document must be a Google Docs link or a document ID")
}

// Link is the document's browser URL.
func Link(id string) string { return "https://docs.google.com/document/d/" + id + "/edit" }

func upstream() []node.PortDefinition {
	return []node.PortDefinition{node.NewPortDefinition("data", node.ValueTypeJSON, false,
		"Any upstream data: connect a node here to run after it and use its outputs in {{variables}}")}
}

func documentField() node.ConfigField {
	return node.NewConfigField("document", node.ValueTypeString, true, "",
		"The document's link (copy it from the browser) or its ID. {{variables}} are filled in, e.g. the link from Create Document.").WithLabel("Document")
}

func textField(desc string, required bool) node.ConfigField {
	return node.NewConfigField("text", node.ValueTypeString, required, "", desc+" {{variables}} are filled in.").WithLabel("Text").WithMultiline()
}

func idOutputs() []node.PortDefinition {
	return []node.PortDefinition{
		node.NewPortDefinition("document_id", node.ValueTypeString, true, "The document's ID"),
		node.NewPortDefinition("link", node.ValueTypeString, true, "Link to open the document"),
	}
}

// Integration is Google Docs' metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Google Docs", Category: "Google", Icon: "docs",
		Description: "Read, create and add text to Google Docs with a connected Google account.",
		DocsURL:     "https://developers.google.com/docs/api",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{
				ID: "read_document", Name: "Read Document", SideEffects: node.SideEffectsNone,
				Description: "Reads a document's text, e.g. to summarize it or answer questions about it.",
				Inputs:      upstream(),
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("text", node.ValueTypeString, true, "The document's text"),
					node.NewPortDefinition("title", node.ValueTypeString, true, "The document's title"),
					node.NewPortDefinition("document_id", node.ValueTypeString, true, "The document's ID"),
					node.NewPortDefinition("link", node.ValueTypeString, true, "Link to open the document"),
				},
				Config: []node.ConfigField{documentField()},
			},
			{
				ID: "create_document", Name: "Create Document", SideEffects: node.SideEffectsUnsafe,
				Description: "Creates a new document in the account's Google Drive, optionally with text in it.",
				Inputs:      upstream(),
				Outputs: append(idOutputs(),
					node.NewPortDefinition("title", node.ValueTypeString, true, "The document's title")),
				Config: []node.ConfigField{
					node.NewConfigField("title", node.ValueTypeString, true, "", "The new document's name. {{variables}} are filled in.").WithLabel("Title"),
					textField("What to write in it. Empty: an empty document.", false),
				},
			},
			{
				ID: "append_text", Name: "Add Text", SideEffects: node.SideEffectsUnsafe,
				Description: "Adds text at the end of a document, on a new line.",
				Inputs:      upstream(),
				Outputs:     idOutputs(),
				Config:      []node.ConfigField{documentField(), textField("The text to add.", true)},
			},
		},
	}
}

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func text(in node.NodeInput, name string) string {
	s, _ := in.GetStringConfig(name)
	return s
}

func checkSize(s string) error {
	if len(s) > MaxTextBytes {
		return configError("the text is too long (at most 1 MB)")
	}
	return nil
}

func ids(id string) node.NodeOutput {
	out := node.NewNodeOutput(nil)
	out.SetPort("document_id", node.NewStringValue(id))
	out.SetPort("link", node.NewStringValue(Link(id)))
	return out
}

func readDocument(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	id, err := DocumentID(text(in, "document"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	d, err := c.Get(ctx, id)
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := ids(id)
	out.SetPort("text", node.NewStringValue(d.Text()))
	out.SetPort("title", node.NewStringValue(d.Title))
	return out, nil
}

func createDocument(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	title := strings.TrimSpace(text(in, "title"))
	if title == "" {
		return node.NodeOutput{}, configError("set the new document's Title")
	}
	body := text(in, "text")
	if err := checkSize(body); err != nil {
		return node.NodeOutput{}, err
	}
	d, err := c.Create(ctx, title)
	if err != nil {
		return node.NodeOutput{}, err
	}
	if d.DocumentID == "" {
		return node.NodeOutput{}, integration.NewError(integration.KindMalformedResponse, "create document: Google Docs returned no document ID")
	}
	if strings.TrimSpace(body) != "" {
		if err := c.AppendText(ctx, d.DocumentID, body); err != nil {
			return node.NodeOutput{}, err
		}
	}
	out := ids(d.DocumentID)
	out.SetPort("title", node.NewStringValue(d.Title))
	return out, nil
}

func appendText(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	id, err := DocumentID(text(in, "document"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	add := text(in, "text")
	if strings.TrimSpace(add) == "" {
		return node.NodeOutput{}, configError("set the Text to add")
	}
	if err := checkSize(add); err != nil {
		return node.NodeOutput{}, err
	}
	// The body always ends with a newline, and text is inserted before
	// it: a document with text gets the new text on a line of its own.
	d, err := c.Get(ctx, id)
	if err != nil {
		return node.NodeOutput{}, err
	}
	if d.Text() != "" && !strings.HasPrefix(add, "\n") {
		add = "\n" + add
	}
	if err := c.AppendText(ctx, id, add); err != nil {
		return node.NodeOutput{}, err
	}
	return ids(id), nil
}

// Connector builds a Docs client around a resolved Google credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Google Docs with its executable nodes. creds is the runtime
// credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	handlers := map[string]integration.Handler[*Client]{"read_document": readDocument, "create_document": createDocument, "append_text": appendText}
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
