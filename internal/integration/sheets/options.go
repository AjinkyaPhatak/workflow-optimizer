package sheets

import (
	"context"

	"workflow-optimizer/internal/node"
)

// sheetOptions lists the spreadsheet's tabs.
func sheetOptions(ctx context.Context, c *Client, in node.NodeInput) ([]node.ConfigOption, error) {
	id, err := SpreadsheetID(text(in, "spreadsheet"))
	if err != nil {
		return nil, err
	}
	titles, err := c.Sheets(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]node.ConfigOption, 0, len(titles))
	for _, t := range titles {
		out = append(out, node.ConfigOption{Value: t, Label: t})
	}
	return out, nil
}

// columnOptions lists the column names (row 1) of the sheet.
func columnOptions(ctx context.Context, c *Client, in node.NodeInput) ([]node.ConfigOption, error) {
	id, sheet, err := target(ctx, c, in)
	if err != nil {
		return nil, err
	}
	head, err := c.Get(ctx, id, SheetRange(sheet, "1:1"))
	if err != nil {
		return nil, err
	}
	var out []node.ConfigOption
	if len(head.Values) > 0 {
		for _, h := range headerNames(head.Values[0]) {
			out = append(out, node.ConfigOption{Value: h, Label: h})
		}
	}
	return out, nil
}
