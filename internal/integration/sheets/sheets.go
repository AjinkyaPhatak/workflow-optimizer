// Package sheets is the Google Sheets integration (Phase C4): three actions
// on the C1 integration framework, authenticated with Google connected
// accounts (C2, scope spreadsheets).
//
// Sheets are treated as tables: row 1 holds the column names, and rows are
// objects keyed by them, so workflows address columns by name, never by
// letter.
//
//	read_rows:  rows [{<column>: text, ..., row_number}], count, headers [], first_row {}
//	append_row: row_number, updated_range
//	update_row: row_number, updated_range, row {}
package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: google_sheets.read_rows, .append_row, .update_row.
const ID = "google_sheets"

// Provider is the OAuth provider whose connected accounts Sheets uses.
const Provider = "google"

// MaxRows bounds read_rows' max_rows.
const MaxRows = 1000

const rowNumberKey = "row_number"

func str(name, label, desc string, required bool) node.ConfigField {
	return node.NewConfigField(name, node.ValueTypeString, required, "", desc).WithLabel(label)
}

func tableFields() []node.ConfigField {
	return []node.ConfigField{
		str("spreadsheet", "Spreadsheet", "The spreadsheet's link (copy it from the browser) or its ID.", true),
		str("sheet", "Sheet", "The tab's name, e.g. Sheet1. Empty: the first tab.", false).WithOptionsSource("sheets", "spreadsheet"),
	}
}

// upstream is the input port every Sheets action has: any upstream node can
// be connected (so the action runs after it and its outputs can be used in
// {{variables}}).
func upstream(name, desc string) []node.PortDefinition {
	return []node.PortDefinition{node.NewPortDefinition(name, node.ValueTypeJSON, false, desc)}
}

func valuesField(desc string) node.ConfigField {
	return node.NewConfigField("values", node.ValueTypeObject, false, nil,
		desc+" One row per column (the names in row 1), e.g. Name → {{input.name}}. {{variables}} are filled in.").
		WithLabel("Values").WithKeysSource("columns", "spreadsheet", "sheet")
}

func inputModeField() node.ConfigField {
	return node.NewConfigField("input_mode", node.ValueTypeString, false, "USER_ENTERED",
		"How values are stored. Like typing: numbers, dates and formulas are recognized (text starting with = becomes a formula). Exact text: stored as plain text.").
		WithLabel("Value handling").WithOptions(
		node.ConfigOption{Value: "USER_ENTERED", Label: "Like typing in the sheet"},
		node.ConfigOption{Value: "RAW", Label: "Exact text"},
	)
}

const valuesPortDesc = "Any upstream data. An object of column values is written when the Values setting is empty; any node can be connected to use its outputs in {{variables}}"

// Integration is Google Sheets' metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Google Sheets", Category: "Google", Icon: "sheets",
		Description: "Read, add and update rows of a Google Sheets table with a connected Google account.",
		DocsURL:     "https://developers.google.com/sheets/api",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{
				ID: "read_rows", Name: "Read Rows", SideEffects: node.SideEffectsNone,
				Description: "Reads a sheet as a table: each row is an object keyed by the column names in row 1.",
				Inputs:      upstream("data", "Any upstream data: connect a node here to run after it and use its outputs in {{variables}}"),
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("rows", node.ValueTypeArray, true, "Rows as objects keyed by column name, each with its row_number"),
					node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of rows returned"),
					node.NewPortDefinition("headers", node.ValueTypeArray, true, "Column names (row 1)"),
					node.NewPortDefinition("first_row", node.ValueTypeObject, true, "The first returned row ({} when there is none)"),
				},
				Config: append(tableFields(),
					node.NewConfigField("max_rows", node.ValueTypeNumber, false, 100.0, "How many rows to return at most.").
						WithLabel("Max rows").WithRange(node.Float(1), node.Float(MaxRows), node.Float(1)),
				),
			},
			{
				// Sheets has no idempotency key for appends: a repeated call
				// adds a second row, so it is not retried after a failure
				// that may have applied.
				ID: "append_row", Name: "Append Row", SideEffects: node.SideEffectsUnsafe,
				Description: "Adds a row at the end of the table, placing each value under its column.",
				Inputs:      upstream("values", valuesPortDesc),
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("row_number", node.ValueTypeNumber, true, "The new row's number"),
					node.NewPortDefinition("updated_range", node.ValueTypeString, true, "The cells written, e.g. Sheet1!A7:D7"),
				},
				Config: append(tableFields(), valuesField("The new row."), inputModeField()),
			},
			{
				ID: "update_row", Name: "Update Row", SideEffects: node.SideEffectsIdempotent,
				Description: "Changes cells of one row, found by row number or by a column value. Other cells are kept.",
				Inputs:      upstream("values", valuesPortDesc),
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("row_number", node.ValueTypeNumber, true, "The updated row's number"),
					node.NewPortDefinition("updated_range", node.ValueTypeString, true, "The cells written"),
					node.NewPortDefinition("row", node.ValueTypeObject, true, "The row after the update"),
				},
				Config: append(tableFields(),
					str("match_column", "Find row by column", "The column to search, e.g. Email. Used when Row number is empty.", false).
						WithOptionsSource("columns", "spreadsheet", "sheet"),
					str("match_value", "Find row with value", "The first row whose cell in that column equals this text is updated. {{variables}} are filled in.", false),
					node.NewConfigField("row_number", node.ValueTypeNumber, false, nil, "A specific row (2 or more; row 1 holds the column names), e.g. from Read Rows.").
						WithLabel("Row number").WithRange(node.Float(2), nil, node.Float(1)),
					valuesField("The cells to change."),
					inputModeField(),
				),
			},
		},
	}
}

func invalid(msg string) error { return integration.NewError(integration.KindInvalidRequest, msg) }

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func text(in node.NodeInput, name string) string {
	s, _ := in.GetStringConfig(name)
	return strings.TrimSpace(s)
}

// table is a sheet read as rows of text with row 1 as the header.
type table struct {
	sheet   string
	headers []string
	rows    [][]string // data rows, rows[i] is sheet row start+i
	start   int        // sheet row number of rows[0]
}

// target resolves the spreadsheet ID and the sheet name (first tab if unset).
func target(ctx context.Context, c *Client, in node.NodeInput) (string, string, error) {
	id, err := SpreadsheetID(text(in, "spreadsheet"))
	if err != nil {
		return "", "", err
	}
	sheet := text(in, "sheet")
	if sheet == "" {
		if sheet, err = c.FirstSheet(ctx, id); err != nil {
			return "", "", err
		}
	}
	return id, sheet, nil
}

func cellText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// headerNames makes row 1 usable as object keys: trimmed, an empty cell
// named by its column letter, duplicates numbered.
func headerNames(row []any) []string {
	seen := map[string]int{}
	out := make([]string, len(row))
	for i, v := range row {
		h := strings.TrimSpace(cellText(v))
		if h == "" {
			h = ColumnLetter(i + 1)
		}
		if seen[h]++; seen[h] > 1 {
			h = fmt.Sprintf("%s_%d", h, seen[h])
		}
		out[i] = h
	}
	return out
}

func readTable(ctx context.Context, c *Client, id, sheet string) (table, error) {
	v, err := c.Get(ctx, id, SheetRange(sheet, ""))
	if err != nil {
		return table{}, err
	}
	t := table{sheet: sheet, start: StartRow(v.Range) + 1}
	if len(v.Values) == 0 {
		return t, nil
	}
	t.headers = headerNames(v.Values[0])
	for _, r := range v.Values[1:] {
		row := make([]string, len(r))
		for i, cell := range r {
			row[i] = cellText(cell)
		}
		t.rows = append(t.rows, row)
	}
	return t, nil
}

func (t table) object(i int) map[string]any {
	obj := make(map[string]any, len(t.headers)+1)
	for j, h := range t.headers {
		cell := ""
		if j < len(t.rows[i]) {
			cell = t.rows[i][j]
		}
		obj[h] = cell
	}
	obj[rowNumberKey] = float64(t.start + i)
	return obj
}

func empty(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func readRows(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	max := 100
	if f, ok := in.GetNumberConfig("max_rows"); ok {
		if f < 1 || f > MaxRows || f != float64(int(f)) {
			return node.NodeOutput{}, configError(fmt.Sprintf("max_rows must be a whole number from 1 to %d", MaxRows))
		}
		max = int(f)
	}
	id, sheet, err := target(ctx, c, in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	t, err := readTable(ctx, c, id, sheet)
	if err != nil {
		return node.NodeOutput{}, err
	}
	rows := []any{}
	for i := range t.rows {
		if len(rows) == max {
			break
		}
		if !empty(t.rows[i]) {
			rows = append(rows, t.object(i))
		}
	}
	headers := make([]any, len(t.headers))
	for i, h := range t.headers {
		headers[i] = h
	}
	first := map[string]any{}
	if len(rows) > 0 {
		first = rows[0].(map[string]any)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("rows", node.NewArrayValue(rows))
	out.SetPort("count", node.NewNumberValue(float64(len(rows))))
	out.SetPort("headers", node.NewArrayValue(headers))
	out.SetPort("first_row", node.NewObjectValue(first))
	return out, nil
}

// values is the Values setting, else a non-empty object connected to the
// values input (connected reports which). The setting wins: the input also
// carries whatever upstream node was connected just to run first.
func values(in node.NodeInput) (vals map[string]any, connected bool, err error) {
	if raw, ok := in.GetConfig("values"); ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, false, configError(`values must be an object of column names, e.g. {"Name": "Ada"}`)
		}
		if len(m) > 0 {
			return m, false, nil
		}
	}
	if v, ok := in.GetPort("values"); ok {
		if m, ok := v.Data.(map[string]any); ok && len(m) > 0 {
			return m, true, nil
		}
	}
	return nil, false, configError("set the Values to write")
}

// column finds a header by name, ignoring case and surrounding spaces.
func column(headers []string, name string) int {
	name = strings.TrimSpace(name)
	for i, h := range headers {
		if strings.EqualFold(h, name) {
			return i
		}
	}
	return -1
}

// place writes vals into row under their columns. Typed-in Values must each
// name a column; a connected object (a form response, a record) may carry
// more fields than the sheet has columns: those are skipped, as long as one
// matches (lenient).
func place(row []any, headers []string, vals map[string]any, lenient bool) error {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	placed := 0
	for _, k := range keys {
		if k == rowNumberKey {
			continue // carried over from Read Rows
		}
		i := column(headers, k)
		if i < 0 && lenient {
			continue
		}
		if i < 0 {
			return invalid(fmt.Sprintf("the sheet has no column %q; its columns are: %s", k, strings.Join(headers, ", ")))
		}
		switch v := vals[k].(type) {
		case nil:
			row[i] = ""
		case string, float64, bool:
			row[i] = v
		default:
			b, _ := json.Marshal(v)
			row[i] = string(b)
		}
		placed++
	}
	if lenient && placed == 0 {
		return invalid("none of the connected values match a column; the sheet's columns are: " + strings.Join(headers, ", "))
	}
	return nil
}

func inputMode(in node.NodeInput) string {
	if m := text(in, "input_mode"); m == "RAW" {
		return m
	}
	return "USER_ENTERED"
}

func appendRow(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	vals, connected, err := values(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	id, sheet, err := target(ctx, c, in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	head, err := c.Get(ctx, id, SheetRange(sheet, "1:1"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	if len(head.Values) == 0 || len(head.Values[0]) == 0 {
		return node.NodeOutput{}, invalid("the sheet has no column names: put them in row 1 first")
	}
	headers := headerNames(head.Values[0])
	row := make([]any, len(headers))
	for i := range row {
		row[i] = ""
	}
	if err := place(row, headers, vals, connected); err != nil {
		return node.NodeOutput{}, err
	}
	updated, err := c.Append(ctx, id, SheetRange(sheet, "A1"), row, inputMode(in))
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("row_number", node.NewNumberValue(float64(StartRow(updated))))
	out.SetPort("updated_range", node.NewStringValue(updated))
	return out, nil
}

func updateRow(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	vals, connected, err := values(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	rowNumber, byNumber := in.GetNumberConfig("row_number")
	matchColumn, matchValue := text(in, "match_column"), text(in, "match_value")
	switch {
	case byNumber && (rowNumber < 2 || rowNumber != float64(int(rowNumber))):
		return node.NodeOutput{}, configError("row_number must be a whole number of 2 or more (row 1 holds the column names)")
	case !byNumber && matchColumn == "":
		return node.NodeOutput{}, configError("set a Row number, or a column and value to find the row by")
	}
	id, sheet, err := target(ctx, c, in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	t, err := readTable(ctx, c, id, sheet)
	if err != nil {
		return node.NodeOutput{}, err
	}
	if len(t.headers) == 0 {
		return node.NodeOutput{}, invalid("the sheet has no column names: put them in row 1 first")
	}
	idx := -1
	if byNumber {
		idx = int(rowNumber) - t.start
		if idx >= len(t.rows) {
			// A row past the table: written as a new, initially empty row.
			for len(t.rows) <= idx {
				t.rows = append(t.rows, nil)
			}
		}
	} else {
		col := column(t.headers, matchColumn)
		if col < 0 {
			return node.NodeOutput{}, invalid(fmt.Sprintf("the sheet has no column %q; its columns are: %s", matchColumn, strings.Join(t.headers, ", ")))
		}
		for i, r := range t.rows {
			if col < len(r) && strings.TrimSpace(r[col]) == matchValue {
				idx = i
				break
			}
		}
		if idx < 0 {
			return node.NodeOutput{}, integration.NewError(integration.KindNotFound, fmt.Sprintf("no row has %q in column %q", matchValue, t.headers[col]))
		}
	}
	row := make([]any, len(t.headers))
	for j := range row {
		row[j] = ""
		if j < len(t.rows[idx]) {
			row[j] = t.rows[idx][j]
		}
	}
	if err := place(row, t.headers, vals, connected); err != nil {
		return node.NodeOutput{}, err
	}
	number := t.start + idx
	updated, err := c.Update(ctx, id, rowRange(sheet, number, len(t.headers)), row, inputMode(in))
	if err != nil {
		return node.NodeOutput{}, err
	}
	after := make(map[string]any, len(row)+1)
	for j, h := range t.headers {
		after[h] = row[j]
	}
	after[rowNumberKey] = float64(number)
	out := node.NewNodeOutput(nil)
	out.SetPort("row_number", node.NewNumberValue(float64(number)))
	out.SetPort("updated_range", node.NewStringValue(updated))
	out.SetPort("row", node.NewObjectValue(after))
	return out, nil
}

// Connector builds a Sheets client around a resolved Google credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Google Sheets with its executable nodes. creds is the
// runtime credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	handlers := map[string]integration.Handler[*Client]{"read_rows": readRows, "append_row": appendRow, "update_row": updateRow}
	nodes := map[string]node.Node{}
	for id, h := range handlers {
		n, err := integration.NewActionNode(in, id, creds, Connector(o), h)
		if err != nil {
			return integration.Module{}, err
		}
		nodes[id] = n.WithOptions(map[string]integration.OptionsHandler[*Client]{"sheets": sheetOptions, "columns": columnOptions})
	}
	return integration.Module{Integration: in, Nodes: nodes}, nil
}
