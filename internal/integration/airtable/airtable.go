// Package airtable is the Airtable integration (Phase J): three actions on
// the C1 integration framework, authenticated with Airtable connected
// accounts (provider "airtable", internal/oauth/airtable).
//
// Records are objects keyed by field name, plus their record_id, so
// workflows use them like Sheets rows.
//
//	list_records:  records [{<field>: value, ..., record_id}], count, first_record {}, record_id (first)
//	create_record: record_id, record {}
//	update_record: record_id, record {}
//
// Airtable has no idempotency keys: create_record is unsafe (a retry after
// an attempt that applied would add a second record). update_record writes
// the same values again on a retry, so it is idempotent.
package airtable

import (
	"context"
	"sort"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: airtable.list_records, .create_record,
// .update_record.
const ID = "airtable"

// Provider is the OAuth provider whose connected accounts Airtable uses.
const Provider = "airtable"

// MaxRecords bounds list_records' max_records.
const MaxRecords = 1000

const recordIDKey = "record_id"

func str(name, label, desc string, required bool) node.ConfigField {
	return node.NewConfigField(name, node.ValueTypeString, required, "", desc).WithLabel(label)
}

func tableFields() []node.ConfigField {
	return []node.ConfigField{
		str("base", "Base", "Pick a base, or paste a link to it (or to a table in it) from the browser.", true).WithOptionsSource("bases"),
		str("table", "Table", "The table's name. Empty: the table in the base's link.", false).WithOptionsSource("tables", "base"),
	}
}

func matchFields(what string) []node.ConfigField {
	return []node.ConfigField{
		str("match_field", "Find by field", "The field to search, e.g. Email."+what, false).WithOptionsSource("fields", "base", "table"),
		str("match_value", "Find with value", "Records whose field equals this text. {{variables}} are filled in.", false),
	}
}

func fieldsField(desc string) node.ConfigField {
	return node.NewConfigField("fields", node.ValueTypeObject, false, nil,
		desc+" One value per field, e.g. Name → {{input.name}}. {{variables}} are filled in; text is converted to the field's type.").
		WithLabel("Fields").WithKeysSource("fields", "base", "table")
}

func upstream(desc string) []node.PortDefinition {
	return []node.PortDefinition{node.NewPortDefinition("fields", node.ValueTypeJSON, false, desc)}
}

const fieldsPortDesc = "Any upstream data. An object of field values is written when the Fields setting is empty; any node can be connected to use its outputs in {{variables}}"

func recordOutputs() []node.PortDefinition {
	return []node.PortDefinition{
		node.NewPortDefinition("record_id", node.ValueTypeString, true, "The record's ID (rec...)"),
		node.NewPortDefinition("record", node.ValueTypeObject, true, "The record's fields, with its record_id"),
	}
}

// Integration is Airtable's metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Airtable", Category: "Airtable", Icon: "airtable",
		Description: "Find, add and update records in Airtable bases with a connected Airtable account.",
		DocsURL:     "https://airtable.com/developers/web/api/introduction",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{
				ID: "list_records", Name: "Find Records", SideEffects: node.SideEffectsNone,
				Description: "Reads a table's records, optionally only those with a field equal to a value.",
				Inputs:      upstream("Any upstream data: connect a node here to run after it and use its outputs in {{variables}}"),
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("records", node.ValueTypeArray, true, "Records as objects keyed by field name, each with its record_id"),
					node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of records returned"),
					node.NewPortDefinition("first_record", node.ValueTypeObject, true, "The first record ({} when there is none)"),
					node.NewPortDefinition("record_id", node.ValueTypeString, true, "The first record's ID (empty when there is none)"),
				},
				Config: append(append(tableFields(), matchFields(" Empty: every record.")...),
					str("view", "View", "Only records shown in this view of the table, in its order. Empty: the whole table.", false),
					node.NewConfigField("max_records", node.ValueTypeNumber, false, 100.0, "How many records to return at most.").
						WithLabel("Max records").WithRange(node.Float(1), node.Float(MaxRecords), node.Float(1)),
				),
			},
			{
				ID: "create_record", Name: "Add Record", SideEffects: node.SideEffectsUnsafe,
				Description: "Adds a record to a table.",
				Inputs:      upstream(fieldsPortDesc),
				Outputs:     recordOutputs(),
				Config:      append(tableFields(), fieldsField("The new record.")),
			},
			{
				ID: "update_record", Name: "Update Record", SideEffects: node.SideEffectsIdempotent,
				Description: "Changes fields of one record, found by its ID or by a field value. Other fields are kept.",
				Inputs:      upstream(fieldsPortDesc),
				Outputs:     recordOutputs(),
				Config: append(append(tableFields(),
					str("record_id", "Record ID", "A specific record (rec...), e.g. from Find Records. {{variables}} are filled in. Empty: found by field.", false)),
					append(matchFields(" Used when Record ID is empty; the first match is updated."), fieldsField("The fields to change."))...),
			},
		},
	}
}

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func text(in node.NodeInput, name string) string {
	s, _ := in.GetStringConfig(name)
	return strings.TrimSpace(s)
}

// target resolves the base ID and the table (name or ID).
func target(in node.NodeInput) (string, string, error) {
	base, linked, err := BaseID(text(in, "base"))
	if err != nil {
		return "", "", err
	}
	table := text(in, "table")
	if table == "" {
		table = linked
	}
	if table == "" {
		return "", "", configError("pick the Table")
	}
	return base, table, nil
}

// object is a record as fields plus its record_id.
func object(r Record) map[string]any {
	out := make(map[string]any, len(r.Fields)+1)
	for k, v := range r.Fields {
		out[k] = v
	}
	out[recordIDKey] = r.ID
	return out
}

// fields is the Fields setting, else a non-empty object connected to the
// fields input (the setting wins, as in Sheets).
func fields(in node.NodeInput) (map[string]any, error) {
	if raw, ok := in.GetConfig("fields"); ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, configError(`fields must be an object of field names, e.g. {"Name": "Ada"}`)
		}
		if len(m) > 0 {
			return m, nil
		}
	}
	if v, ok := in.GetPort("fields"); ok {
		if m, ok := v.Data.(map[string]any); ok && len(m) > 0 {
			// A record from Find Records can be written back: its
			// record_id is not a field.
			out := make(map[string]any, len(m))
			for k, val := range m {
				if k != recordIDKey {
					out[k] = val
				}
			}
			return out, nil
		}
	}
	return nil, configError("set the Fields to write")
}

func formula(in node.NodeInput) (string, error) {
	field, value := text(in, "match_field"), text(in, "match_value")
	if field == "" {
		return "", nil
	}
	return MatchFormula(field, value)
}

func listRecords(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	base, table, err := target(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	limit := 100
	if n, ok := in.GetNumberConfig("max_records"); ok {
		limit = int(n)
	}
	if limit < 1 || limit > MaxRecords {
		return node.NodeOutput{}, configError("Max records must be between 1 and 1000")
	}
	f, err := formula(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	recs, err := c.List(ctx, base, table, text(in, "view"), f, limit)
	if err != nil {
		return node.NodeOutput{}, err
	}
	// Airtable leaves empty fields out: every record gets every field
	// seen, so rows line up as a table.
	seen := map[string]bool{}
	for _, r := range recs {
		for k := range r.Fields {
			seen[k] = true
		}
	}
	rows := make([]any, 0, len(recs))
	for _, r := range recs {
		row := object(r)
		for k := range seen {
			if _, ok := row[k]; !ok {
				row[k] = ""
			}
		}
		rows = append(rows, row)
	}
	first, firstID := map[string]any{}, ""
	if len(rows) > 0 {
		first, firstID = rows[0].(map[string]any), recs[0].ID
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("records", node.NewArrayValue(rows))
	out.SetPort("count", node.NewNumberValue(float64(len(rows))))
	out.SetPort("first_record", node.NewObjectValue(first))
	out.SetPort("record_id", node.NewStringValue(firstID))
	return out, nil
}

func recordOutput(r Record) node.NodeOutput {
	out := node.NewNodeOutput(nil)
	out.SetPort("record_id", node.NewStringValue(r.ID))
	out.SetPort("record", node.NewObjectValue(object(r)))
	return out
}

func createRecord(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	base, table, err := target(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	vals, err := fields(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	r, err := c.Create(ctx, base, table, vals)
	if err != nil {
		return node.NodeOutput{}, err
	}
	if r.ID == "" {
		return node.NodeOutput{}, integration.NewError(integration.KindMalformedResponse, "create record: Airtable returned no record ID")
	}
	return recordOutput(r), nil
}

func updateRecord(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	base, table, err := target(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	vals, err := fields(in)
	if err != nil {
		return node.NodeOutput{}, err
	}
	id := text(in, "record_id")
	switch {
	case id != "":
		if id, err = RecordID(id); err != nil {
			return node.NodeOutput{}, err
		}
	case text(in, "match_field") != "":
		f, err := formula(in)
		if err != nil {
			return node.NodeOutput{}, err
		}
		found, err := c.List(ctx, base, table, "", f, 1)
		if err != nil {
			return node.NodeOutput{}, err
		}
		if len(found) == 0 {
			return node.NodeOutput{}, integration.NewError(integration.KindNotFound,
				"update record: no record has "+text(in, "match_field")+" equal to "+text(in, "match_value"))
		}
		id = found[0].ID
	default:
		return node.NodeOutput{}, configError("set the Record ID, or a field and value to find the record by")
	}
	r, err := c.Update(ctx, base, table, id, vals)
	if err != nil {
		return node.NodeOutput{}, err
	}
	return recordOutput(r), nil
}

// baseOptions lists the bases the connection can open.
func baseOptions(ctx context.Context, c *Client, _ node.NodeInput) ([]node.ConfigOption, error) {
	bases, err := c.Bases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]node.ConfigOption, 0, len(bases))
	for _, b := range bases {
		out = append(out, node.ConfigOption{Value: b.ID, Label: b.Name})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

// tableOptions lists the base's tables by name.
func tableOptions(ctx context.Context, c *Client, in node.NodeInput) ([]node.ConfigOption, error) {
	base, _, err := BaseID(text(in, "base"))
	if err != nil {
		return nil, err
	}
	tables, err := c.Tables(ctx, base)
	if err != nil {
		return nil, err
	}
	out := make([]node.ConfigOption, 0, len(tables))
	for _, t := range tables {
		out = append(out, node.ConfigOption{Value: t.Name, Label: t.Name})
	}
	return out, nil
}

// fieldOptions lists the table's fields by name.
func fieldOptions(ctx context.Context, c *Client, in node.NodeInput) ([]node.ConfigOption, error) {
	base, table, err := target(in)
	if err != nil {
		return nil, err
	}
	tables, err := c.Tables(ctx, base)
	if err != nil {
		return nil, err
	}
	for _, t := range tables {
		if t.ID == table || strings.EqualFold(t.Name, table) {
			out := make([]node.ConfigOption, 0, len(t.Fields))
			for _, f := range t.Fields {
				out = append(out, node.ConfigOption{Value: f.Name, Label: f.Name})
			}
			return out, nil
		}
	}
	return nil, integration.NewError(integration.KindNotFound, "the base has no table named "+table)
}

// Connector builds an Airtable client around a resolved credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Airtable with its executable nodes. creds is the runtime
// credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	handlers := map[string]integration.Handler[*Client]{"list_records": listRecords, "create_record": createRecord, "update_record": updateRecord}
	options := map[string]integration.OptionsHandler[*Client]{"bases": baseOptions, "tables": tableOptions, "fields": fieldOptions}
	nodes := map[string]node.Node{}
	for id, h := range handlers {
		n, err := integration.NewActionNode(in, id, creds, Connector(o), h)
		if err != nil {
			return integration.Module{}, err
		}
		nodes[id] = n.WithOptions(options)
	}
	return integration.Module{Integration: in, Nodes: nodes}, nil
}
