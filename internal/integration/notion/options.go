package notion

import (
	"context"
	"sort"

	"workflow-optimizer/internal/node"
)

// Property types Find Database Rows can filter on and Add/Update Database
// Row can write (see Filter and Encode).
var (
	filterable = map[string]bool{"title": true, "rich_text": true, "url": true, "email": true, "phone_number": true, "number": true,
		"checkbox": true, "select": true, "status": true, "multi_select": true, "relation": true, "people": true, "date": true}
	writable = map[string]bool{"title": true, "rich_text": true, "url": true, "email": true, "phone_number": true, "number": true,
		"checkbox": true, "select": true, "status": true, "multi_select": true, "relation": true, "people": true, "date": true}
)

// databaseOptions lists the databases shared with the integration.
func databaseOptions(ctx context.Context, c *Client, _ node.NodeInput) ([]node.ConfigOption, error) {
	found, err := c.Search(ctx, "", "data_source", MaxResults)
	if err != nil {
		return nil, err
	}
	out := make([]node.ConfigOption, 0, len(found))
	for _, p := range found {
		if p.Object != "data_source" && p.Object != "database" {
			continue
		}
		title := titleOf(p)
		if title == "" {
			title = "Untitled"
		}
		out = append(out, node.ConfigOption{Value: p.ID, Label: title})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

func typedOptions(types schemaTypes, keep map[string]bool) []node.ConfigOption {
	names := make([]string, 0, len(types))
	for n, t := range types {
		if keep[t] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]node.ConfigOption, 0, len(names))
	for _, n := range names {
		out = append(out, node.ConfigOption{Value: n, Label: n + " (" + types[n] + ")"})
	}
	return out
}

// propertyOptions lists the database's properties of the kept types.
func propertyOptions(keep map[string]bool) func(context.Context, *Client, node.NodeInput) ([]node.ConfigOption, error) {
	return func(ctx context.Context, c *Client, in node.NodeInput) ([]node.ConfigOption, error) {
		ds, err := dataSource(ctx, c, text(in, "database"))
		if err != nil {
			return nil, err
		}
		return typedOptions(dataSourceTypes(ds), keep), nil
	}
}

// pagePropertyOptions lists a row's writable properties.
func pagePropertyOptions(ctx context.Context, c *Client, in node.NodeInput) ([]node.ConfigOption, error) {
	id, err := ObjectID("row", text(in, "page"))
	if err != nil {
		return nil, err
	}
	p, err := c.GetPage(ctx, id)
	if err != nil {
		return nil, err
	}
	return typedOptions(pageTypes(p), writable), nil
}
