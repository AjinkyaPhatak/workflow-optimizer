// Package notion is the Notion integration (Phase C5): four actions on the
// C1 integration framework, authenticated with Notion connected accounts
// (C2, the Notion OAuth provider). It uses Notion API version 2025-09-03,
// where a database's table is a "data source".
//
// Database rows are plain objects keyed by property name, so workflows
// never handle Notion's property JSON:
//
//	search:         results [{id, type, title, url, last_edited_time}], count, id (first)
//	query_database: rows [{page_id, url, created_time, last_edited_time, <property>: value}],
//	                count, page_id (first), first_row
//	create_page:    page_id, url
//	update_page:    page_id, url, row
package notion

import (
	"context"
	"fmt"
	"strings"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

// ID is the integration ID: notion.search, notion.query_database,
// notion.create_page, notion.update_page.
const ID = "notion"

// Provider is the OAuth provider whose connected accounts Notion uses.
const Provider = "notion"

// MaxResults bounds max_results (Notion's page size).
const MaxResults = 100

func str(name, label, desc string, required bool) node.ConfigField {
	return node.NewConfigField(name, node.ValueTypeString, required, "", desc).WithLabel(label)
}

func port(name, desc string) []node.PortDefinition {
	return []node.PortDefinition{node.NewPortDefinition(name, node.ValueTypeJSON, false, desc)}
}

func databaseField() node.ConfigField {
	return str("database", "Database", "Pick a database shared with the integration, or paste its link (••• menu, Copy link) or ID.", true).
		WithOptionsSource("databases")
}

func propertiesField(desc, source, dependsOn string) node.ConfigField {
	return node.NewConfigField("properties", node.ValueTypeObject, false, nil,
		desc+" One row per property (column): e.g. Name → {{read.subject}}, Status → To do, Tags → a, b, Due → 2026-10-20, Done → false. {{variables}} are filled in.").
		WithLabel("Properties").WithKeysSource(source, dependsOn)
}

func maxField(def float64) node.ConfigField {
	return node.NewConfigField("max_results", node.ValueTypeNumber, false, def, "How many results to return at most.").
		WithLabel("Max results").WithRange(node.Float(1), node.Float(MaxResults), node.Float(1))
}

func rowOutputs(what string) []node.PortDefinition {
	return []node.PortDefinition{
		node.NewPortDefinition("page_id", node.ValueTypeString, true, "ID of the "+what+" row (page)"),
		node.NewPortDefinition("url", node.ValueTypeString, true, "Link to the row in Notion"),
	}
}

// Integration is Notion's metadata.
func Integration() integration.Integration {
	return integration.Integration{
		ID: ID, Name: "Notion", Category: "Notion", Icon: "notion",
		Description: "Search pages and databases, and find, add and update database rows, with a connected Notion workspace.",
		DocsURL:     "https://developers.notion.com/reference",
		Auth:        integration.Auth{Required: true, Provider: Provider, CredentialType: credential.TypeOAuth2},
		Actions: []integration.Action{
			{
				ID: "search", Name: "Search", SideEffects: node.SideEffectsNone,
				Description: "Finds pages and databases shared with the integration by title.",
				Inputs:      port("query", "Any upstream data; text is used as the search (overriding the Search setting)"),
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("results", node.ValueTypeArray, true, "Matches: id, type (page or database), title, url, last_edited_time"),
					node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of matches returned"),
					node.NewPortDefinition("id", node.ValueTypeString, true, "ID of the first match (empty when there is none)"),
				},
				Config: []node.ConfigField{
					str("query", "Search", "Title text to look for. Empty: everything shared with the integration. {{variables}} are filled in.", false),
					node.NewConfigField("type", node.ValueTypeString, false, "", "What to look for.").WithLabel("Find").WithOptions(
						node.ConfigOption{Value: "", Label: "Pages and databases"},
						node.ConfigOption{Value: "page", Label: "Pages"},
						node.ConfigOption{Value: "database", Label: "Databases"},
					),
					maxField(10),
				},
			},
			{
				ID: "query_database", Name: "Find Database Rows", SideEffects: node.SideEffectsNone,
				Description: "Returns rows of a database, optionally only those whose property matches a value.",
				Inputs:      port("database", "Any upstream data; text is used as the database (overriding the Database setting)"),
				Outputs: []node.PortDefinition{
					node.NewPortDefinition("rows", node.ValueTypeArray, true, "Rows as objects: page_id, url, created_time, last_edited_time and each property by name"),
					node.NewPortDefinition("count", node.ValueTypeNumber, true, "Number of rows returned"),
					node.NewPortDefinition("page_id", node.ValueTypeString, true, "ID of the first row (empty when there is none)"),
					node.NewPortDefinition("first_row", node.ValueTypeObject, true, "The first row ({} when there is none)"),
				},
				Config: []node.ConfigField{
					databaseField(),
					str("filter_property", "Filter by property", "Only rows whose property matches, e.g. Status. Empty: all rows.", false).
						WithOptionsSource("properties", "database"),
					node.NewConfigField("filter_condition", node.ValueTypeString, false, "equals", "How the value is compared (contains: text properties).").
						WithLabel("Condition").WithOptions(node.ConfigOption{Value: "equals", Label: "Equals"}, node.ConfigOption{Value: "contains", Label: "Contains"}),
					str("filter_value", "Filter value", "e.g. Done, or {{read.sender}}. {{variables}} are filled in.", false),
					str("sort_property", "Sort by", "A property name, or created_time / last_edited_time. Empty: Notion's order.", false).
						WithOptionsSource("properties", "database"),
					node.NewConfigField("sort_direction", node.ValueTypeString, false, "descending", "").
						WithLabel("Sort direction").WithOptions(node.ConfigOption{Value: "ascending", Label: "Ascending"}, node.ConfigOption{Value: "descending", Label: "Descending"}),
					maxField(25),
					node.NewConfigField("filter", node.ValueTypeObject, false, nil, "Advanced: a Notion API filter object, used instead of the simple filter.").WithLabel("Advanced filter"),
				},
			},
			{
				// Notion has no idempotency key: a repeated call adds a second
				// row, so it is not retried after a failure that may have
				// applied.
				ID: "create_page", Name: "Add Database Row", SideEffects: node.SideEffectsUnsafe,
				Description: "Adds a row (page) to a database, optionally with text in the page.",
				Inputs:      port("properties", "Any upstream data. An object of property values is used when the Properties setting is empty; any node can be connected to use its outputs in {{variables}}"),
				Outputs:     rowOutputs("new"),
				Config: []node.ConfigField{
					databaseField(),
					propertiesField("The new row.", "writable_properties", "database"),
					str("content", "Page content", "Text written inside the new page (paragraphs split on blank lines). {{variables}} are filled in.", false).WithMultiline(),
				},
			},
			{
				ID: "update_page", Name: "Update Database Row", SideEffects: node.SideEffectsIdempotent,
				Description: "Changes properties of one row (page). Properties not listed are kept.",
				Inputs:      port("page_id", "Any upstream data; text is used as the row (overriding the Row setting), e.g. page_id from Find Database Rows"),
				Outputs: append(rowOutputs("updated"),
					node.NewPortDefinition("row", node.ValueTypeObject, true, "The row after the update")),
				Config: []node.ConfigField{
					str("page", "Row", "The row's link or page ID, e.g. page_id from Find Database Rows. {{variables}} are filled in.", false),
					propertiesField("The properties to change.", "page_properties", "page"),
				},
			},
		},
	}
}

func configError(msg string) error { return node.NewNodeError(node.ErrCodeConfiguration, msg, false) }

func text(in node.NodeInput, name string) string {
	s, _ := in.GetStringConfig(name)
	return strings.TrimSpace(s)
}

// textOrPort is the connected input's text, else the setting.
func textOrPort(in node.NodeInput, port, setting string) string {
	if v, ok := in.GetPort(port); ok {
		if s, ok := v.String(); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return text(in, setting)
}

func whole(in node.NodeInput, name string, def int) (int, error) {
	f, ok := in.GetNumberConfig(name)
	if !ok {
		return def, nil
	}
	if f < 1 || f > MaxResults || f != float64(int(f)) {
		return 0, configError(fmt.Sprintf("%s must be a whole number from 1 to %d", name, MaxResults))
	}
	return int(f), nil
}

func titleOf(p Page) string {
	if p.Object == "data_source" || p.Object == "database" {
		var b strings.Builder
		for _, t := range p.Title {
			b.WriteString(t.PlainText)
		}
		return b.String()
	}
	for name, typ := range pageTypes(p) {
		if typ == "title" {
			s, _ := Simplify(p.Properties[name]).(string)
			return s
		}
	}
	return ""
}

func search(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	max, err := whole(in, "max_results", 10)
	if err != nil {
		return node.NodeOutput{}, err
	}
	kind := text(in, "type")
	switch kind {
	case "", "page":
	case "database":
		kind = "data_source"
	default:
		return node.NodeOutput{}, configError("type must be page, database or empty")
	}
	found, err := c.Search(ctx, textOrPort(in, "query", "query"), kind, max)
	if err != nil {
		return node.NodeOutput{}, err
	}
	results := make([]any, 0, len(found))
	for _, p := range found {
		typ := p.Object
		if typ == "data_source" {
			typ = "database"
		}
		results = append(results, map[string]any{"id": p.ID, "type": typ, "title": titleOf(p), "url": p.URL, "last_edited_time": p.LastEditedTime})
	}
	first := ""
	if len(found) > 0 {
		first = found[0].ID
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("results", node.NewArrayValue(results))
	out.SetPort("count", node.NewNumberValue(float64(len(results))))
	out.SetPort("id", node.NewStringValue(first))
	return out, nil
}

func dataSource(ctx context.Context, c *Client, raw string) (DataSource, error) {
	id, err := ObjectID("database", raw)
	if err != nil {
		return DataSource{}, err
	}
	return c.Resolve(ctx, id)
}

func queryDatabase(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	max, err := whole(in, "max_results", 25)
	if err != nil {
		return node.NodeOutput{}, err
	}
	ds, err := dataSource(ctx, c, textOrPort(in, "database", "database"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	types := dataSourceTypes(ds)
	var filter any
	if raw, ok := in.GetConfig("filter"); ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return node.NodeOutput{}, configError("the advanced filter must be a Notion filter object")
		}
		if len(m) > 0 {
			filter = m
		}
	}
	if prop := text(in, "filter_property"); filter == nil && prop != "" {
		if filter, err = Filter(types, prop, text(in, "filter_value"), text(in, "filter_condition")); err != nil {
			return node.NodeOutput{}, err
		}
	}
	var sorts []any
	if prop := text(in, "sort_property"); prop != "" {
		dir := text(in, "sort_direction")
		if dir != "ascending" {
			dir = "descending"
		}
		switch prop {
		case "created_time", "last_edited_time":
			sorts = []any{map[string]any{"timestamp": prop, "direction": dir}}
		default:
			name, ok := types.find(prop)
			if !ok {
				return node.NodeOutput{}, integration.NewError(integration.KindInvalidRequest,
					fmt.Sprintf("the database has no property %q; its properties are: %s", prop, types.names()))
			}
			sorts = []any{map[string]any{"property": name, "direction": dir}}
		}
	}
	pages, err := c.Query(ctx, ds.ID, filter, sorts, max)
	if err != nil {
		return node.NodeOutput{}, err
	}
	rows := make([]any, 0, len(pages))
	for _, p := range pages {
		rows = append(rows, Row(p))
	}
	first, firstID := map[string]any{}, ""
	if len(pages) > 0 {
		first, firstID = rows[0].(map[string]any), pages[0].ID
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("rows", node.NewArrayValue(rows))
	out.SetPort("count", node.NewNumberValue(float64(len(rows))))
	out.SetPort("page_id", node.NewStringValue(firstID))
	out.SetPort("first_row", node.NewObjectValue(first))
	return out, nil
}

// properties is the Properties setting, else a non-empty object connected
// to the properties input.
func properties(in node.NodeInput, required bool) (map[string]any, error) {
	if raw, ok := in.GetConfig("properties"); ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, configError(`properties must be an object of property names, e.g. {"Name": "Ada"}`)
		}
		if len(m) > 0 {
			return m, nil
		}
	}
	if v, ok := in.GetPort("properties"); ok {
		if m, ok := v.Data.(map[string]any); ok && len(m) > 0 {
			return m, nil
		}
	}
	if required {
		return nil, configError("set the Properties to write")
	}
	return map[string]any{}, nil
}

// paragraphs turns text into paragraph blocks, one per blank-line-separated
// paragraph (at most 100, Notion's limit per request).
func paragraphs(s string) []any {
	var out []any
	for _, p := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if len(out) == 100 {
			break
		}
		out = append(out, map[string]any{"object": "block", "type": "paragraph", "paragraph": map[string]any{"rich_text": richTextValue(p)}})
	}
	return out
}

func createPage(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	values, err := properties(in, true)
	if err != nil {
		return node.NodeOutput{}, err
	}
	ds, err := dataSource(ctx, c, text(in, "database"))
	if err != nil {
		return node.NodeOutput{}, err
	}
	props, err := Encode(dataSourceTypes(ds), values)
	if err != nil {
		return node.NodeOutput{}, err
	}
	content, _ := in.GetStringConfig("content")
	p, err := c.CreatePage(ctx, ds.ID, props, paragraphs(content))
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("page_id", node.NewStringValue(p.ID))
	out.SetPort("url", node.NewStringValue(p.URL))
	return out, nil
}

func updatePage(ctx context.Context, c *Client, in node.NodeInput) (node.NodeOutput, error) {
	values, err := properties(in, true)
	if err != nil {
		return node.NodeOutput{}, err
	}
	raw := textOrPort(in, "page_id", "page")
	if raw == "" {
		return node.NodeOutput{}, configError("set the Row (a link or page ID), or connect a page_id")
	}
	id, err := ObjectID("row", raw)
	if err != nil {
		return node.NodeOutput{}, err
	}
	current, err := c.GetPage(ctx, id)
	if err != nil {
		return node.NodeOutput{}, err
	}
	props, err := Encode(pageTypes(current), values)
	if err != nil {
		return node.NodeOutput{}, err
	}
	p, err := c.UpdatePage(ctx, id, props)
	if err != nil {
		return node.NodeOutput{}, err
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("page_id", node.NewStringValue(p.ID))
	out.SetPort("url", node.NewStringValue(p.URL))
	out.SetPort("row", node.NewObjectValue(Row(p)))
	return out, nil
}

// Connector builds a Notion client around a resolved Notion credential.
func Connector(o Options) integration.Connector[*Client] {
	return integration.ConnectorFunc[*Client](func(_ context.Context, cred credential.ResolvedCredential) (*Client, error) {
		return NewClient(o, cred.Secret), nil
	})
}

// Module returns Notion with its executable nodes. creds is the runtime
// credential resolver (the OAuth token manager).
func Module(creds credential.Resolver, o Options) (integration.Module, error) {
	in := Integration()
	handlers := map[string]integration.Handler[*Client]{
		"search": search, "query_database": queryDatabase, "create_page": createPage, "update_page": updatePage,
	}
	options := map[string]map[string]integration.OptionsHandler[*Client]{
		"query_database": {"databases": databaseOptions, "properties": propertyOptions(filterable)},
		"create_page":    {"databases": databaseOptions, "writable_properties": propertyOptions(writable)},
		"update_page":    {"page_properties": pagePropertyOptions},
	}
	nodes := map[string]node.Node{}
	for id, h := range handlers {
		n, err := integration.NewActionNode(in, id, creds, Connector(o), h)
		if err != nil {
			return integration.Module{}, err
		}
		nodes[id] = n.WithOptions(options[id])
	}
	return integration.Module{Integration: in, Nodes: nodes}, nil
}
