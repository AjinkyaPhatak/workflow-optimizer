package execution_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/node"
)

// productionFiles parses the non-test Go files of the execution package.
func productionFiles(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no production files found")
	}
	return fset, files
}

func TestExecutorImportsOnlyAbstractions(t *testing.T) {
	allowed := map[string]bool{
		"container/heap": true, "context": true, "encoding/json": true, "errors": true,
		"fmt": true, "sort": true, "strconv": true, "strings": true, "time": true,
		"github.com/google/uuid":               true, // Phase 2 persistence record types (pre-existing)
		"workflow-optimizer/internal/node":     true,
		"workflow-optimizer/internal/workflow": true,
	}
	fset, files := productionFiles(t)
	for _, f := range files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !allowed[path] {
				t.Errorf("%s imports %q: the executor may depend only on node/workflow abstractions (no node implementations, providers, PostgreSQL, Redis, HTTP, or concurrency packages)",
					fset.Position(imp.Pos()), path)
			}
		}
	}
}

func TestExecutorHasNoNodeTypeBranchingOrConcurrency(t *testing.T) {
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	nodeTypes := map[string]bool{}
	for _, typ := range a.NodeRegistry.List() {
		nodeTypes[typ] = true
	}
	// Literals that coincide with a node type name but are documented grammar,
	// not node-type dispatch: {{input.*}} is the workflow-input namespace.
	allowedConsts := map[string]bool{"inputNamespace": true}

	isTypeSelector := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Type"
	}

	fset, files := productionFiles(t)
	for _, f := range files {
		exempt := map[*ast.BasicLit]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok {
				for i, name := range vs.Names {
					if allowedConsts[name.Name] && i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
							exempt[lit] = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SwitchStmt:
				if x.Tag != nil && isTypeSelector(x.Tag) {
					t.Errorf("%s: switch over a node type", fset.Position(x.Pos()))
				}
			case *ast.BinaryExpr:
				if (x.Op == token.EQL || x.Op == token.NEQ) && (isTypeSelector(x.X) || isTypeSelector(x.Y)) {
					t.Errorf("%s: comparison against a node type", fset.Position(x.Pos()))
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING && !exempt[x] {
					if s, _ := strconv.Unquote(x.Value); nodeTypes[s] {
						t.Errorf("%s: hard-coded node type literal %q", fset.Position(x.Pos()), s)
					}
				}
			case *ast.GoStmt:
				t.Errorf("%s: goroutine in sequential V1 executor", fset.Position(x.Pos()))
			case *ast.ChanType:
				t.Errorf("%s: channel in sequential V1 executor", fset.Position(x.Pos()))
			}
			return true
		})
	}
}

// uppercaseNode is a brand-new node type unknown to the executor source.
type uppercaseNode struct{}

func (uppercaseNode) Type() string { return "test.uppercase" }
func (uppercaseNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	v, _ := in.GetPort("in")
	s, _ := v.String()
	out := node.NewNodeOutput(nil)
	out.SetPort("out", node.NewStringValue(strings.ToUpper(s)))
	return out, ctx.Err()
}

func TestNewRegisteredNodeRequiresNoExecutorChanges(t *testing.T) {
	e := newEnv(t)
	err := e.reg.RegisterNode(uppercaseNode{}, node.NodeDefinition{
		Type: "test.uppercase", Name: "Uppercase", Category: node.CategoryUtilities,
		Inputs:  []node.PortDefinition{node.NewPortDefinition("in", node.ValueTypeString, true, "")},
		Outputs: []node.PortDefinition{node.NewPortDefinition("out", node.ValueTypeString, true, "")},
	})
	if err != nil {
		t.Fatal(err)
	}
	def := graph(nodes(entry("in"), probeWith("A", map[string]any{"message": "{{input.word}}"}),
		wfNode("up", "test.uppercase", nil), probe("B"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "text", "up", "in"),
		edge("up", "out", "B", "s"), edge("B", "out", "out", "value"))
	if _, err := e.run(t, def, map[string]any{"word": "dag"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := portData(t, e.rec.call(t, "B"), "s"); got != "DAG" {
		t.Fatalf("B.s = %#v, want DAG", got)
	}
}
