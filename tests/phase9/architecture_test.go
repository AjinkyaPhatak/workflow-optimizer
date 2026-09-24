package phase9_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
)

// Static checks on the Phase 9 boundaries. They need no database or Redis.

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// parseDir parses the production (non-test) files of one directory.
func parseDir(t *testing.T, rel string) (*token.FileSet, []*ast.File) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), rel, "*.go"))
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
		t.Fatalf("no production files in %s", rel)
	}
	return fset, files
}

func checkImports(t *testing.T, rel string, forbidden []string, why string) {
	t.Helper()
	fset, files := parseDir(t, rel)
	for _, f := range files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				// A trailing "/" forbids only sub-packages of bad.
				sub := strings.HasSuffix(bad, "/") && strings.HasPrefix(path, bad)
				if sub || path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %q: %s", fset.Position(imp.Pos()), path, why)
				}
			}
		}
	}
}

func TestQueueAndWorkerDependOnlyOnAbstractions(t *testing.T) {
	forbidden := []string{
		"github.com/jackc", "github.com/redis", "net/http",
		"workflow-optimizer/internal/infrastructure", "workflow-optimizer/internal/app",
		"workflow-optimizer/internal/node/", // node implementations
		"workflow-optimizer/internal/provider",
	}
	checkImports(t, "internal/queue", forbidden, "the queue contract must not depend on a backend or on node implementations")
	checkImports(t, "internal/worker", forbidden, "workers reach PostgreSQL and Redis only through interfaces")
}

func TestRedisAdapterDoesNotExecute(t *testing.T) {
	checkImports(t, "internal/infrastructure/redis", []string{
		"github.com/jackc", "workflow-optimizer/internal/execution", "workflow-optimizer/internal/worker",
		"workflow-optimizer/internal/workflow", "workflow-optimizer/internal/node",
	}, "Redis transports execution identity only; it never executes or owns state")
}

func TestPhase9CodeHasNoNodeTypeLiterals(t *testing.T) {
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, typ := range a.NodeRegistry.List() {
		types[typ] = true
	}
	for _, rel := range []string{"internal/queue", "internal/worker", "internal/infrastructure/redis"} {
		fset, files := parseDir(t, rel)
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil && types[s] {
					t.Errorf("%s: node type literal %q; workers must not branch on node types", fset.Position(lit.Pos()), s)
				}
				return true
			})
		}
	}
}

// The queue name is defined once, in config.
func TestQueueNameIsNotHardCoded(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			if filepath.ToSlash(filepath.Dir(p)) == filepath.ToSlash(filepath.Join(root, "internal", "config")) {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, _ := strconv.Unquote(lit.Value); s == config.DefaultRedisQueueName {
						t.Errorf("%s hard-codes the queue name; use config.RedisQueueName", fset.Position(lit.Pos()))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
