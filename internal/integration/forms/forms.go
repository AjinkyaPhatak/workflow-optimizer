// Package forms is the Google Forms integration (Phase J): one action on the
// C1 integration framework, authenticated with Google connected accounts
// (C2, scopes forms.body.readonly and forms.responses.readonly: the form's
// questions and its responses, read only).
//
//	read_responses: responses [{submitted_at, email, response_id, <question>: answer}], count, latest {}, questions []
//
// Responses are rows keyed by the question titles (as in the form's linked
// spreadsheet), newest first, so workflows use them like Sheets rows.
package forms

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/googleapi"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: google_forms.read_responses.
const ID = "google_forms"

// Provider is the OAuth provider whose connected accounts Forms uses.
const Provider = "google"

// DefaultBaseURL is the Google Forms REST API root.
const DefaultBaseURL = "https://forms.googleapis.com/v1"

// MaxResponses bounds read_responses' max_responses.
const MaxResponses = 1000

// pageSize is the responses requested per page (Google's maximum is 5000).
const pageSize = 500

// Options configure the Forms client (tests point BaseURL at a mock API).
type Options = googleapi.Options

// Client is the Google Forms REST client for one action call.
type Client struct {
	api *googleapi.Client
}

// NewClient returns a client with the access token of a resolved credential.
func NewClient(o Options, token credential.Secret) *Client {
	return &Client{api: googleapi.New("Google Forms", DefaultBaseURL, o, token)}
}

// Form is the subset of a form the action reads: its questions.
type Form struct {
	FormID string `json:"formId"`
	Info   struct {
		Title string `json:"title"`
	} `json:"info"`
	Items []Item `json:"items"`
}

// Item is one form item; only questions and question groups (grids) hold
// questions.
type Item struct {
	Title        string `json:"title"`
	QuestionItem *struct {
		Question Question `json:"question"`
	} `json:"questionItem,omitempty"`
	QuestionGroupItem *struct {
		Questions []Question `json:"questions"`
	} `json:"questionGroupItem,omitempty"`
}

// Question identifies a question; a grid row has its own title.
type Question struct {
	QuestionID  string `json:"questionId"`
	RowQuestion *struct {
		Title string `json:"title"`
	} `json:"rowQuestion,omitempty"`
}

// Response is one submitted response.
type Response struct {
	ResponseID        string            `json:"responseId"`
	CreateTime        string            `json:"createTime"`
	LastSubmittedTime string            `json:"lastSubmittedTime"`
	RespondentEmail   string            `json:"respondentEmail"`
	Answers           map[string]Answer `json:"answers"`
}

// Answer is the answer to one question.
type Answer struct {
	TextAnswers *struct {
		Answers []struct {
			Value string `json:"value"`
		} `json:"answers"`
	} `json:"textAnswers,omitempty"`
	FileUploadAnswers *struct {
		Answers []struct {
			FileID   string `json:"fileId"`
			FileName string `json:"fileName"`
		} `json:"answers"`
	} `json:"fileUploadAnswers,omitempty"`
}

// Text is the answer as text: several answers (checkboxes) are joined with
// ", ", uploaded files are their names.
func (a Answer) Text() string {
	var parts []string
	if a.TextAnswers != nil {
		for _, t := range a.TextAnswers.Answers {
			parts = append(parts, t.Value)
		}
	}
	if a.FileUploadAnswers != nil {
		for _, f := range a.FileUploadAnswers.Answers {
			parts = append(parts, f.FileName)
		}
	}
	return strings.Join(parts, ", ")
}

// Get reads the form's questions.
func (c *Client) Get(ctx context.Context, id string) (Form, error) {
	var f Form
	err := c.api.Do(ctx, http.MethodGet, "/forms/"+url.PathEscape(id), nil, nil, &f, "read form")
	return f, err
}

// Responses reads the form's responses (every page, up to a bound).
func (c *Client) Responses(ctx context.Context, id string) ([]Response, error) {
	var all []Response
	token := ""
	for {
		q := url.Values{"pageSize": {strconv.Itoa(pageSize)}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var page struct {
			Responses     []Response `json:"responses"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := c.api.Do(ctx, http.MethodGet, "/forms/"+url.PathEscape(id)+"/responses", q, nil, &page, "read responses"); err != nil {
			return nil, err
		}
		all = append(all, page.Responses...)
		// Google returns responses in no promised order: every page is
		// read (up to a bound) so the newest can be picked.
		if page.NextPageToken == "" || len(all) >= 10*MaxResponses {
			return all, nil
		}
		token = page.NextPageToken
	}
}

// column is one question as a response column.
type column struct {
	id, title string
}

// Columns are the form's questions in form order, titled by their question
// (a grid row is "Question [Row]"); repeated titles get " (2)", " (3)".
func (f Form) Columns() []column {
	var cols []column
	seen := map[string]int{}
	add := func(id, title string) {
		title = strings.TrimSpace(title)
		if title == "" {
			title = "Question"
		}
		seen[title]++
		if n := seen[title]; n > 1 {
			title += " (" + strconv.Itoa(n) + ")"
		}
		cols = append(cols, column{id: id, title: title})
	}
	for _, it := range f.Items {
		switch {
		case it.QuestionItem != nil:
			add(it.QuestionItem.Question.QuestionID, it.Title)
		case it.QuestionGroupItem != nil:
			for _, q := range it.QuestionGroupItem.Questions {
				row := ""
				if q.RowQuestion != nil {
					row = q.RowQuestion.Title
				}
				add(q.QuestionID, it.Title+" ["+row+"]")
			}
		}
	}
	return cols
}

var (
	editURL      = regexp.MustCompile(`/forms/(?:u/\d+/)?d/([A-Za-z0-9_-]{20,})`)
	responderURL = regexp.MustCompile(`/forms/(?:u/\d+/)?d/e/|forms\.gle/`)
	plainID      = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
)

// FormID accepts a form ID or the link of the form's edit page. The link
// people fill the form in at (…/d/e/…/viewform or forms.gle) does not name
// the form and is rejected with a hint.
func FormID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if responderURL.MatchString(s) {
		return "", integration.NewError(integration.KindInvalidRequest,
			"that is the link people fill the form in at; open the form for editing and copy that link (it ends with /edit)")
	}
	if m := editURL.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	if plainID.MatchString(s) {
		return s, nil
	}
	return "", integration.NewError(integration.KindInvalidRequest, "the form must be a Google Forms link (from its edit page) or a form ID")
}

// Integration is Google Forms' metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Google Forms", Category: "Google", Icon: "forms",
		Description: "Read the responses of a Google Form with a connected Google account.",
		DocsURL:     "https://developers.google.com/forms/api",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{{
			ID: "read_responses", Name: "Read Responses", SideEffects: node.SideEffectsNone,
			Description: "Reads a form's responses, newest first: each response is a row of answers keyed by the question.",
			Inputs: []node.PortDefinition{node.NewPortDefinition("data", node.ValueTypeJSON, false,
				"Any upstream data: connect a node here to run after it and use its outputs in {{variables}}")},
			Outputs: []node.PortDefinition{
				node.NewPortDefinition("responses", node.ValueTypeArray, true, "Responses, newest first: the answers keyed by question, plus submitted_at, email and response_id"),
				node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of responses returned"),
				node.NewPortDefinition("latest", node.ValueTypeObject, true, "The newest response ({} when there is none)"),
				node.NewPortDefinition("questions", node.ValueTypeArray, true, "The form's questions"),
			},
			Config: []node.ConfigField{
				node.NewConfigField("form", node.ValueTypeString, true, "",
					"The form's link from its edit page (it ends with /edit), or its ID.").WithLabel("Form"),
				node.NewConfigField("max_responses", node.ValueTypeNumber, false, 50.0, "How many of the newest responses to return at most.").
					WithLabel("Max responses").WithRange(node.Float(1), node.Float(MaxResponses), node.Float(1)),
			},
		}},
	}
}

func readResponses(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	raw, _ := in.GetStringConfig("form")
	id, err := FormID(raw)
	if err != nil {
		return node.NodeOutput{}, err
	}
	limit := 50
	if n, ok := in.GetNumberConfig("max_responses"); ok {
		limit = int(n)
	}
	if limit < 1 || limit > MaxResponses {
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration, "Max responses must be between 1 and 1000", false)
	}
	f, err := c.Get(ctx, id)
	if err != nil {
		return node.NodeOutput{}, err
	}
	responses, err := c.Responses(ctx, id)
	if err != nil {
		return node.NodeOutput{}, err
	}
	sort.SliceStable(responses, func(i, j int) bool { return submitted(responses[i]) > submitted(responses[j]) })
	if len(responses) > limit {
		responses = responses[:limit]
	}
	cols := f.Columns()
	questions := make([]any, 0, len(cols))
	for _, col := range cols {
		questions = append(questions, col.title)
	}
	rows := make([]any, 0, len(responses))
	for _, r := range responses {
		row := map[string]any{"submitted_at": submitted(r), "email": r.RespondentEmail, "response_id": r.ResponseID}
		for _, col := range cols {
			row[col.title] = r.Answers[col.id].Text()
		}
		rows = append(rows, row)
	}
	latest := map[string]any{}
	if len(rows) > 0 {
		latest = rows[0].(map[string]any)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("responses", node.NewArrayValue(rows))
	out.SetPort("count", node.NewNumberValue(float64(len(rows))))
	out.SetPort("latest", node.NewObjectValue(latest))
	out.SetPort("questions", node.NewArrayValue(questions))
	return out, nil
}

// submitted is when a response was last submitted (RFC 3339 in UTC, so it
// sorts as text).
func submitted(r Response) string {
	if r.LastSubmittedTime != "" {
		return r.LastSubmittedTime
	}
	return r.CreateTime
}

// Connector builds a Forms client around a resolved Google credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Google Forms with its executable node. creds is the
// runtime credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	n, err := integration.NewActionNode(in, "read_responses", creds, Connector(o), readResponses)
	if err != nil {
		return integration.Module{}, err
	}
	return integration.Module{Integration: in, Nodes: map[string]node.Node{"read_responses": n}}, nil
}
