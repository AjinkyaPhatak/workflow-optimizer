package sheets

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/googleapi"
)

// DefaultBaseURL is the Google Sheets REST API root.
const DefaultBaseURL = "https://sheets.googleapis.com/v4"

// Options configure the Sheets client (tests point BaseURL at a mock API).
type Options = googleapi.Options

// Client is the Google Sheets REST client for one action call.
type Client struct {
	api *googleapi.Client
}

// NewClient returns a client with the access token of a resolved credential.
func NewClient(o Options, token credential.Secret) *Client {
	return &Client{api: googleapi.New("Google Sheets", DefaultBaseURL, o, token)}
}

var (
	urlID   = regexp.MustCompile(`/spreadsheets/d/([A-Za-z0-9_-]+)`)
	plainID = regexp.MustCompile(`^[A-Za-z0-9_-]{10,}$`)
)

// SpreadsheetID accepts a spreadsheet ID or its browser URL.
func SpreadsheetID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if m := urlID.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	if plainID.MatchString(s) {
		return s, nil
	}
	return "", integration.NewError(integration.KindInvalidRequest, "the spreadsheet must be a Google Sheets link or a spreadsheet ID")
}

// SheetRange prefixes an A1 range with a quoted sheet name: 'My sheet'!A1:C.
// An empty a1 addresses the whole sheet.
func SheetRange(sheet, a1 string) string {
	r := "'" + strings.ReplaceAll(sheet, "'", "''") + "'"
	if a1 != "" {
		r += "!" + a1
	}
	return r
}

// ColumnLetter is the A1 column name of a 1-based column index.
func ColumnLetter(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

var a1Row = regexp.MustCompile(`![A-Za-z]*(\d+)`)

// StartRow is the first row number of a returned range ("Sheet1!A5:C9" -> 5);
// 1 when the range names no row.
func StartRow(r string) int {
	if m := a1Row.FindStringSubmatch(r); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func spreadsheetPath(id string) string { return "/spreadsheets/" + url.PathEscape(id) }

func valuesPath(id, rng string) string {
	return spreadsheetPath(id) + "/values/" + url.PathEscape(rng)
}

// FirstSheet is the title of the spreadsheet's first tab.
func (c *Client) FirstSheet(ctx context.Context, id string) (string, error) {
	var meta struct {
		Sheets []struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := c.api.Do(ctx, http.MethodGet, spreadsheetPath(id), url.Values{"fields": {"sheets.properties.title"}}, nil, &meta, "open spreadsheet"); err != nil {
		return "", err
	}
	if len(meta.Sheets) == 0 || meta.Sheets[0].Properties.Title == "" {
		return "", integration.NewError(integration.KindMalformedResponse, "open spreadsheet: Google Sheets returned no sheets")
	}
	return meta.Sheets[0].Properties.Title, nil
}

// ValueRange is a block of cells as Sheets returns it (formatted text).
type ValueRange struct {
	Range  string  `json:"range"`
	Values [][]any `json:"values"`
}

// Get reads a range, row by row, as displayed in the sheet.
func (c *Client) Get(ctx context.Context, id, rng string) (ValueRange, error) {
	var v ValueRange
	err := c.api.Do(ctx, http.MethodGet, valuesPath(id, rng),
		url.Values{"majorDimension": {"ROWS"}, "valueRenderOption": {"FORMATTED_VALUE"}}, nil, &v, "read rows")
	return v, err
}

// Append adds one row after the table in rng.
func (c *Client) Append(ctx context.Context, id, rng string, row []any, inputOption string) (string, error) {
	var out struct {
		Updates struct {
			UpdatedRange string `json:"updatedRange"`
		} `json:"updates"`
	}
	err := c.api.Do(ctx, http.MethodPost, valuesPath(id, rng)+":append",
		url.Values{"valueInputOption": {inputOption}, "insertDataOption": {"INSERT_ROWS"}},
		map[string]any{"majorDimension": "ROWS", "values": [][]any{row}}, &out, "append row")
	if err == nil && out.Updates.UpdatedRange == "" {
		return "", integration.NewError(integration.KindMalformedResponse, "append row: Google Sheets did not say where the row was added")
	}
	return out.Updates.UpdatedRange, err
}

// Update overwrites the cells of rng with one row.
func (c *Client) Update(ctx context.Context, id, rng string, row []any, inputOption string) (string, error) {
	var out struct {
		UpdatedRange string `json:"updatedRange"`
	}
	err := c.api.Do(ctx, http.MethodPut, valuesPath(id, rng), url.Values{"valueInputOption": {inputOption}},
		map[string]any{"range": rng, "majorDimension": "ROWS", "values": [][]any{row}}, &out, "update row")
	if err == nil && out.UpdatedRange == "" {
		out.UpdatedRange = rng
	}
	return out.UpdatedRange, err
}

// rowRange is the A1 range of one row across width columns of sheet.
func rowRange(sheet string, row, width int) string {
	return SheetRange(sheet, fmt.Sprintf("A%d:%s%d", row, ColumnLetter(width), row))
}
