// Package templates provides workflow templates: ready-made workflow
// definitions (the canonical workflow JSON, nothing else) that a new
// workflow can start from. Instantiating a template copies its definition
// with fresh node and edge IDs.
package templates

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"workflow-optimizer/internal/workflow"
)

//go:embed catalog/*.json
var files embed.FS

// ErrNotFound reports an unknown template ID.
var ErrNotFound = errors.New("template not found")

// Template is a named starting-point workflow definition (Phase K: with
// its gallery category and what to prepare before running it).
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Category groups the gallery; Order sorts within it.
	Category string `json:"category"`
	Order    int    `json:"order"`
	// Setup lists what to prepare besides the accounts and keys the steps
	// use (the editor works those out from the steps), e.g. a sheet's
	// column names.
	Setup      []string            `json:"setup,omitempty"`
	Definition workflow.Definition `json:"definition"`
}

// Categories are the gallery's categories in display order. "Advanced"
// holds the developer examples.
var Categories = []string{"Writing", "Email", "Sales & customers", "Meetings", "Team & projects", "Advanced"}

func categoryRank(c string) int {
	for i, x := range Categories {
		if x == c {
			return i
		}
	}
	return len(Categories)
}

var catalog = mustLoad()

func mustLoad() []Template {
	entries, err := files.ReadDir("catalog")
	if err != nil {
		panic(err)
	}
	out := make([]Template, 0, len(entries))
	for _, e := range entries {
		raw, err := files.ReadFile(path.Join("catalog", e.Name()))
		if err != nil {
			panic(err)
		}
		var t Template
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			panic(fmt.Sprintf("template %s: %v", e.Name(), err))
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := categoryRank(a.Category), categoryRank(b.Category); ra != rb {
			return ra < rb
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.ID < b.ID
	})
	return out
}

// List returns all templates (callers must not modify them).
func List() []Template {
	return catalog
}

// Get returns one template.
func Get(id string) (Template, error) {
	for _, t := range catalog {
		if t.ID == id {
			return t, nil
		}
	}
	return Template{}, ErrNotFound
}

// Instantiate returns a copy of the template's definition with fresh node and
// edge IDs. Edges and {{node_id.port}} references follow the renamed nodes;
// positions, configuration and variables are kept.
func Instantiate(id string) (workflow.Definition, error) {
	t, err := Get(id)
	if err != nil {
		return workflow.Definition{}, err
	}
	return InstantiateDefinition(t.Definition)
}

// InstantiateDefinition is Instantiate for any definition (a saved
// template's): a copy with fresh node and edge IDs.
func InstantiateDefinition(src workflow.Definition) (workflow.Definition, error) {
	raw, err := json.Marshal(src)
	if err != nil {
		return workflow.Definition{}, err
	}
	var def workflow.Definition
	if err := json.Unmarshal(raw, &def); err != nil { // deep copy
		return workflow.Definition{}, err
	}

	ids := make(map[string]string, len(def.Nodes))
	taken := map[string]bool{}
	for i, n := range def.Nodes {
		fresh := freshID(n.Type, taken)
		ids[n.ID] = fresh
		def.Nodes[i].ID = fresh
	}
	for i, n := range def.Nodes {
		def.Nodes[i].Config = remap(n.Config, ids).(map[string]any)
	}
	for i, e := range def.Edges {
		def.Edges[i].ID = freshID("edge", taken)
		def.Edges[i].Source = ids[e.Source]
		def.Edges[i].Target = ids[e.Target]
	}
	return def, nil
}

// idUnsafe matches what a node ID cannot contain: references are
// {{id.port}}, so an integration type such as "gmail.send" becomes the
// prefix "gmail_send".
var idUnsafe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func freshID(prefix string, taken map[string]bool) string {
	prefix = idUnsafe.ReplaceAllString(prefix, "_")
	for {
		b := make([]byte, 2)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		id := prefix + "_" + hex.EncodeToString(b)
		if !taken[id] {
			taken[id] = true
			return id
		}
	}
}

var reference = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)((?:\.[A-Za-z0-9_]+)*)\}\}`)

// remap rewrites {{old_id...}} references to renamed nodes.
func remap(v any, ids map[string]string) any {
	switch t := v.(type) {
	case string:
		return reference.ReplaceAllStringFunc(t, func(m string) string {
			sub := reference.FindStringSubmatch(m)
			// {{input...}} always means the workflow input, never a node.
			if fresh, ok := ids[sub[1]]; ok && sub[1] != "input" {
				return "{{" + fresh + sub[2] + "}}"
			}
			return m
		})
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = remap(x, ids)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = remap(x, ids)
		}
		return out
	}
	return v
}
