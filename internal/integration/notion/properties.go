package notion

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"workflow-optimizer/internal/integration"
)

// maxText is Notion's limit for one rich text item.
const maxText = 2000

// Simplify turns a raw page property into a plain value: text for title,
// rich text, select, status, URLs, dates (start) and times; a number;
// a boolean (checkbox); a list for multi-select, people, relations and
// files. Unknown types are nil.
func Simplify(raw json.RawMessage) any {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	var typ string
	_ = json.Unmarshal(p["type"], &typ)
	v := p[typ]
	switch typ {
	case "title", "rich_text":
		var items []richText
		_ = json.Unmarshal(v, &items)
		var b strings.Builder
		for _, i := range items {
			b.WriteString(i.PlainText)
		}
		return b.String()
	case "number":
		var n *float64
		_ = json.Unmarshal(v, &n)
		if n == nil {
			return nil
		}
		return *n
	case "checkbox":
		var b bool
		_ = json.Unmarshal(v, &b)
		return b
	case "select", "status":
		var s *struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(v, &s)
		if s == nil {
			return ""
		}
		return s.Name
	case "multi_select":
		var items []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(v, &items)
		out := []any{}
		for _, i := range items {
			out = append(out, i.Name)
		}
		return out
	case "date":
		var d *struct {
			Start string `json:"start"`
		}
		_ = json.Unmarshal(v, &d)
		if d == nil {
			return ""
		}
		return d.Start
	case "url", "email", "phone_number", "created_time", "last_edited_time":
		var s *string
		_ = json.Unmarshal(v, &s)
		if s == nil {
			return ""
		}
		return *s
	case "people":
		var items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(v, &items)
		out := []any{}
		for _, i := range items {
			if i.Name != "" {
				out = append(out, i.Name)
			} else {
				out = append(out, i.ID)
			}
		}
		return out
	case "created_by", "last_edited_by":
		var u struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(v, &u)
		if u.Name != "" {
			return u.Name
		}
		return u.ID
	case "relation":
		var items []struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(v, &items)
		out := []any{}
		for _, i := range items {
			out = append(out, i.ID)
		}
		return out
	case "files":
		var items []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(v, &items)
		out := []any{}
		for _, i := range items {
			out = append(out, i.Name)
		}
		return out
	case "unique_id":
		var u struct {
			Prefix *string  `json:"prefix"`
			Number *float64 `json:"number"`
		}
		_ = json.Unmarshal(v, &u)
		if u.Number == nil {
			return ""
		}
		if u.Prefix != nil && *u.Prefix != "" {
			return fmt.Sprintf("%s-%d", *u.Prefix, int64(*u.Number))
		}
		return *u.Number
	case "formula":
		var f map[string]json.RawMessage
		_ = json.Unmarshal(v, &f)
		var ft string
		_ = json.Unmarshal(f["type"], &ft)
		var out any
		_ = json.Unmarshal(f[ft], &out)
		if ft == "date" {
			if m, ok := out.(map[string]any); ok {
				return m["start"]
			}
		}
		return out
	case "rollup":
		var r map[string]json.RawMessage
		_ = json.Unmarshal(v, &r)
		var rt string
		_ = json.Unmarshal(r["type"], &rt)
		switch rt {
		case "number":
			var n *float64
			_ = json.Unmarshal(r["number"], &n)
			if n != nil {
				return *n
			}
		case "date":
			var d *struct {
				Start string `json:"start"`
			}
			_ = json.Unmarshal(r["date"], &d)
			if d != nil {
				return d.Start
			}
		case "array":
			var items []json.RawMessage
			_ = json.Unmarshal(r["array"], &items)
			out := []any{}
			for _, i := range items {
				out = append(out, Simplify(i))
			}
			return out
		}
		return nil
	}
	return nil
}

// Row is a page as a plain object: page_id, url, created_time,
// last_edited_time, then each property by name.
func Row(p Page) map[string]any {
	row := map[string]any{"page_id": p.ID, "url": p.URL, "created_time": p.CreatedTime, "last_edited_time": p.LastEditedTime}
	for name, raw := range p.Properties {
		row[name] = Simplify(raw)
	}
	return row
}

// schemaTypes is property name -> type, from a data source or a page.
type schemaTypes map[string]string

func pageTypes(p Page) schemaTypes {
	t := schemaTypes{}
	for name, raw := range p.Properties {
		var x struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &x)
		t[name] = x.Type
	}
	return t
}

func dataSourceTypes(ds DataSource) schemaTypes {
	t := schemaTypes{}
	for name, p := range ds.Properties {
		t[name] = p.Type
	}
	return t
}

// find returns the property's exact name, matching case-insensitively.
func (t schemaTypes) find(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if _, ok := t[name]; ok {
		return name, true
	}
	for n := range t {
		if strings.EqualFold(n, name) {
			return n, true
		}
	}
	return "", false
}

func (t schemaTypes) names() string {
	n := make([]string, 0, len(t))
	for k := range t {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

func invalid(msg string) error { return integration.NewError(integration.KindInvalidRequest, msg) }

func asText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// list is an array value or comma-separated text.
func list(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if s := strings.TrimSpace(asText(x)); s != "" {
				out = append(out, s)
			}
		}
	default:
		for _, s := range strings.Split(asText(v), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// richTextValue splits text into items of at most maxText runes.
func richTextValue(s string) []any {
	r := []rune(s)
	out := []any{}
	for len(r) > 0 {
		n := min(len(r), maxText)
		out = append(out, map[string]any{"type": "text", "text": map[string]any{"content": string(r[:n])}})
		r = r[n:]
	}
	return out
}

// Encode turns plain values into Notion property values using the schema:
// keys are property names (matched ignoring case).
func Encode(types schemaTypes, values map[string]any) (map[string]any, error) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]any{}
	for _, k := range keys {
		switch k {
		case "page_id", "url", "created_time", "last_edited_time":
			continue // carried over from a row output
		}
		name, ok := types.find(k)
		if !ok {
			return nil, invalid(fmt.Sprintf("the database has no property %q; its properties are: %s", k, types.names()))
		}
		v := values[k]
		text := strings.TrimSpace(asText(v))
		var enc any
		switch typ := types[name]; typ {
		case "title", "rich_text":
			enc = richTextValue(asText(v))
		case "number":
			switch n := v.(type) {
			case float64:
				enc = n
			default:
				if text == "" {
					enc = nil
				} else if f, err := strconv.ParseFloat(text, 64); err == nil {
					enc = f
				} else {
					return nil, invalid(fmt.Sprintf("property %q is a number; %q is not", name, text))
				}
			}
		case "checkbox":
			switch b := v.(type) {
			case bool:
				enc = b
			default:
				switch strings.ToLower(text) {
				case "true", "yes", "1", "on", "checked":
					enc = true
				case "false", "no", "0", "off", "", "unchecked":
					enc = false
				default:
					return nil, invalid(fmt.Sprintf("property %q is a checkbox: use true or false", name))
				}
			}
		case "select", "status":
			if text == "" {
				enc = nil
			} else {
				enc = map[string]any{"name": text}
			}
		case "multi_select":
			items := []any{}
			for _, s := range list(v) {
				items = append(items, map[string]any{"name": s})
			}
			enc = items
		case "date":
			if text == "" {
				enc = nil
			} else {
				enc = map[string]any{"start": text}
			}
		case "url", "email", "phone_number":
			if text == "" {
				enc = nil
			} else {
				enc = text
			}
		case "relation", "people":
			items := []any{}
			for _, s := range list(v) {
				items = append(items, map[string]any{"id": s})
			}
			enc = items
		default:
			return nil, invalid(fmt.Sprintf("property %q (%s) cannot be set", name, typ))
		}
		out[name] = map[string]any{types[name]: enc}
	}
	return out, nil
}

// Filter builds a query filter on one property: equals or contains.
func Filter(types schemaTypes, property string, value any, condition string) (map[string]any, error) {
	name, ok := types.find(property)
	if !ok {
		return nil, invalid(fmt.Sprintf("the database has no property %q; its properties are: %s", property, types.names()))
	}
	typ := types[name]
	text := strings.TrimSpace(asText(value))
	var cond map[string]any
	switch typ {
	case "title", "rich_text", "url", "email", "phone_number":
		if condition != "contains" {
			condition = "equals"
		}
		cond = map[string]any{condition: text}
	case "number":
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, invalid(fmt.Sprintf("property %q is a number; %q is not", name, text))
		}
		cond = map[string]any{"equals": f}
	case "checkbox":
		b, err := strconv.ParseBool(strings.ToLower(text))
		if err != nil {
			return nil, invalid(fmt.Sprintf("property %q is a checkbox: filter with true or false", name))
		}
		cond = map[string]any{"equals": b}
	case "select", "status":
		cond = map[string]any{"equals": text}
	case "multi_select", "relation", "people":
		cond = map[string]any{"contains": text}
	case "date":
		cond = map[string]any{"equals": text}
	default:
		return nil, invalid(fmt.Sprintf("property %q (%s) cannot be filtered on here; use the Advanced filter", name, typ))
	}
	return map[string]any{"property": name, typ: cond}, nil
}
