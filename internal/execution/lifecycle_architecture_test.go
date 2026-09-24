package execution_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"workflow-optimizer/internal/execution"
)

func TestLifecycleForbiddenImports(t *testing.T) {
	forbidden := []string{
		"redis", "workflow-optimizer/internal/provider", "workflow-optimizer/internal/infrastructure",
		"net/http", "net", "database/sql", "github.com/jackc/pgx", "sync", "sync/atomic",
		"golang.org/x/sync", "workflow-optimizer/internal/node/",
	}
	fset, files := productionFiles(t)
	for _, f := range files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") || (strings.HasSuffix(bad, "/") && strings.HasPrefix(path, bad)) || strings.Contains(path, "redis") {
					t.Errorf("%s imports forbidden %q (Redis, providers, network, direct DB drivers, in-memory locks and node implementations stay out of the execution package)", fset.Position(imp.Pos()), path)
				}
			}
		}
	}
}

// Phase 7 graph files must not know about lifecycle persistence.
func TestGraphExecutorDoesNotOwnLifecyclePersistence(t *testing.T) {
	graphFiles := map[string]bool{"executor.go": true, "planner.go": true, "resolver.go": true, "variables.go": true, "result.go": true}
	lifecycleIdents := regexp.MustCompile(`^(ExecutionRepository|NodeExecutionRepository|ExecutionStateMachine|NodeExecutionStateMachine|LifecycleService|ExecutionService|Runner|RunnerConfig|TransitionUpdate|NodeTransitionUpdate|Execution|NodeExecution|ExecutionStatus|NodeExecutionStatus|uuid)$`)
	fset, files := productionFiles(t)
	for _, f := range files {
		name := filepath.Base(fset.Position(f.Pos()).Filename)
		if !graphFiles[name] {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && lifecycleIdents.MatchString(id.Name) {
				t.Errorf("%s: graph execution code references lifecycle type %q", fset.Position(id.Pos()), id.Name)
			}
			return true
		})
	}
	// GraphExecutor's only dependency is the node registry.
	typ := reflect.TypeOf(execution.GraphExecutor{})
	if typ.NumField() != 1 || typ.Field(0).Name != "registry" {
		t.Fatalf("GraphExecutor fields changed: %v", typ)
	}
}

// Status only changes through the state machine: no production code assigns a
// Status field, and repositories expose no generic update path.
func TestNoStatusMutationBypassesStateMachine(t *testing.T) {
	fset, files := productionFiles(t)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range as.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Status" {
						t.Errorf("%s: direct Status assignment bypasses the state machine", fset.Position(as.Pos()))
					}
				}
			}
			return true
		})
	}
	methods := func(v any) []string {
		it := reflect.TypeOf(v).Elem()
		var out []string
		for i := 0; i < it.NumMethod(); i++ {
			out = append(out, it.Method(i).Name)
		}
		sort.Strings(out)
		return out
	}
	if got := methods((*execution.ExecutionRepository)(nil)); !equalStrings(got, []string{"Create", "Get", "History", "Transition"}) {
		t.Fatalf("ExecutionRepository methods = %v", got)
	}
	if got := methods((*execution.NodeExecutionRepository)(nil)); !equalStrings(got, []string{"Begin", "Create", "Get", "ListByExecution", "Transition"}) {
		t.Fatalf("NodeExecutionRepository methods = %v", got)
	}
}

// Every UPDATE of lifecycle tables in the PostgreSQL adapter must be a
// conditional compare-and-set on the current status.
func TestPostgresUpdatesAreConditionalOnStatus(t *testing.T) {
	dir := filepath.Join("..", "infrastructure", "postgres")
	paths, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	fset := token.NewFileSet()
	found := 0
	updateRe := regexp.MustCompile(`(?i)UPDATE\s+(executions|node_executions)\b`)
	// Every lifecycle UPDATE is conditional on the row's current status: the
	// compare-and-set (WHERE id = $1 AND status = $2) or the sweep of RUNNING
	// nodes (WHERE execution_id = $1 AND status = 'RUNNING').
	guardRe := regexp.MustCompile(`WHERE\s+(id|execution_id)\s*=\s*\$1\s+AND\s+status\s*=\s*(\$2|'RUNNING')`)
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, _ := strconv.Unquote(lit.Value)
			if updateRe.MatchString(s) {
				found++
				if !guardRe.MatchString(s) {
					t.Errorf("%s: lifecycle UPDATE is not guarded by the current status", fset.Position(lit.Pos()))
				}
			}
			return true
		})
	}
	if found < 2 {
		t.Fatalf("expected execution and node UPDATE statements, found %d", found)
	}
}

func TestNoRetryOrBackoffInPhase8(t *testing.T) {
	retry := regexp.MustCompile(`(?i)(retry|retries|backoff|attempt)`)
	allowed := map[string]bool{"Retryable": true, "IsRetryable": true}
	lifecycleFiles := map[string]bool{"status.go": true, "statemachine.go": true, "service.go": true, "runner.go": true,
		"persistence.go": true, "execution_error.go": true, "lifecycle_errors.go": true}
	fset, files := productionFiles(t)
	for _, f := range files {
		isLifecycle := lifecycleFiles[filepath.Base(fset.Position(f.Pos()).Filename)]
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if retry.MatchString(x.Name) && !allowed[x.Name] {
					t.Errorf("%s: retry logic identifier %q", fset.Position(x.Pos()), x.Name)
				}
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "time" && x.Sel.Name == "Sleep" {
					t.Errorf("%s: sleeping/backoff in lifecycle code", fset.Position(x.Pos()))
				}
			case *ast.ForStmt:
				// A bare loop around lifecycle calls is how retries sneak in;
				// lifecycle code has no unbounded loops (graph code may).
				if isLifecycle && x.Cond == nil {
					t.Errorf("%s: unbounded loop in execution package", fset.Position(x.Pos()))
				}
			}
			return true
		})
	}
}

func TestMigrationDefinesLifecycleSchema(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000002_execution_state_machine.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, want := range []string{
		"CREATE TABLE execution_status_history",
		"executions_version_belongs_to_workflow_fkey",
		"'SKIPPED'",
		"execution_status_history is append-only",
		"RENAME COLUMN completed_at TO finished_at",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("migration 000002 missing %q", want)
		}
	}
}
