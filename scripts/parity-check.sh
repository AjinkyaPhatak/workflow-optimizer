#!/usr/bin/env bash
# Behaviour parity of an integration checkout against origin/backend-overhaul.
# Usage: bash scripts/parity-check.sh [checkout-dir]   (default: this repository)
# Rerun after every integration change. Exit code is non-zero on any failure.
set -uo pipefail
export LC_ALL=C.UTF-8
export GOTOOLCHAIN=auto
export GOSUMDB=sum.golang.org
DIR="${1:-$(git rev-parse --show-toplevel)}"
REF="${BACKEND_REF:-origin/backend-overhaul}"
cd "$DIR" || exit 2
fail=0
say() { printf '\n== %s\n' "$*"; }

# The working tree as a tree object (includes uncommitted edits).
idx=$(mktemp)
trap 'rm -f "$idx" "${test_log:-}"' EXIT
cp "$(git rev-parse --git-path index)" "$idx" || exit 2
TREE=$(GIT_INDEX_FILE="$idx" bash -c 'git add -A . >/dev/null 2>&1 && git write-tree') || exit 2

say "1. Behaviour files must be byte-identical to $REF"
# Contracts, state, runtime, tests and config: the backend branch owns these.
OWNED='^(internal/(?!node/|templates/catalog/)|cmd/|migrations/|workers/|scripts/|go\.(mod|sum)$|Makefile$|frontend/(lib/|features/(?!workflows/useWorkflowCards)|types/|stores/|e2e/|tests/|next\.config\.ts$|package(-lock)?\.json$))'
drift=$(git diff --name-only "$REF" "$TREE" | grep -P "$OWNED")
# Deliberate, reviewed exceptions (keep this list short and explained).
# Additive integration tests cover merged screens; existing backend tests remain owned.
ALLOW='^(frontend/e2e/(workflow-editor|connected-accounts|input-recovery|integration)\.spec\.ts|frontend/tests/unit/(workflow-cards|library-integration|canvas-measurements)\.test\.tsx|scripts/parity-check\.sh)$'
unexpected=$(printf '%s\n' "$drift" | grep -vP "$ALLOW" | grep .)
if [ -n "$unexpected" ]; then echo "UNEXPECTED drift:"; echo "$unexpected"; fail=1; else echo "ok"; fi
[ -n "$drift" ] && echo "allowed drift:" && printf '%s\n' "$drift" | grep -P "$ALLOW"

say "2. Go: only user-facing strings may differ in internal/node and the template catalog"
code=$(git diff -U0 "$REF" "$TREE" -- internal/node internal/templates/catalog | grep -E '^[-+][^-+]' | grep -vE 'Description|description|"setup"|Library|Files page|Knowledge page|note')
if [ -n "$code" ]; then echo "non-string Go/catalog change:"; echo "$code"; fail=1; else echo "ok"; fi

say "3. Backend behaviours reachable in the merged UI"
check() { grep -qF -- "$2" $1 && echo "  ok       $2" || { echo "  MISSING  $2   ($3)"; fail=1; }; }
check "frontend/components/library/Library.tsx" 'copyLocal(' 'Copy to local search'
check "frontend/components/library/Library.tsx" '"Web page"' 'URL source label'
check "frontend/components/library/Library.tsx" 'AddDocuments' 'web page / text / file ingestion'
check "frontend/components/library/Library.tsx" 'TrySearch' 'search with AiMarkdown'
check "frontend/components/library/Library.tsx" 'CreateBase' 'local search, no key needed'
check "frontend/components/ui/TopBar.tsx" '"/automations"' 'Automations tab'
check "frontend/components/ui/TopBar.tsx" '"/settings/whatsapp"' 'WhatsApp settings'
check "frontend/app/login/page.tsx" 'router.replace("/home")' 'sign-in lands on /home'
check "frontend/app/workflows/[[]workflowId]/editor/page.tsx frontend/app/workflows/*/editor/page.tsx" 'WorkflowEditorRoute' 'compiler/legacy editor routing'
check "frontend/app/dashboard/page.tsx" '"/home"' 'dashboard redirect'
# Workflow cards must not call legacy-only endpoints for compiler workflows.
if grep -q 'workflowApi\.\(update\|remove\)' frontend/app/workflows/page.tsx && ! grep -q 'kind === "legacy"' frontend/components/workflow/WorkflowCard.tsx; then
  echo "  MISSING  compiler-aware rename/delete/run history on workflow cards"; fail=1; fi
if grep -q 'workflowApi.listVersions' frontend/features/workflows/useWorkflowCards.ts && ! grep -q 'assistantApi' frontend/features/workflows/useWorkflowCards.ts; then
  echo "  MISSING  compiler-aware steps/last run in useWorkflowCards"; fail=1; fi

say "4. Design-side checks (frontend-overhaul priority)"
n=$(grep -rnoE '#(7[0-9a-f]{5}|8063ca|93829f)|(radial|linear)-gradient' frontend/components/assistant frontend/app/globals.css | grep -v wf-skeleton | wc -l)
[ "$n" -eq 0 ] && echo "  ok       no purple/gradients in backend screens" || { echo "  TODO     $n purple/gradient values in backend screens"; fail=1; }
n=$(grep -rn 'calc(100dvh - 52px)\|top:52px\|top: 52px' frontend/components/assistant frontend/app/globals.css | wc -l)
[ "$n" -eq 0 ] && echo "  ok       no hard-coded 52px top bar" || { echo "  TODO     $n hard-coded 52px top-bar offsets"; fail=1; }
grep -q 'href: "/home", label: "Home"' frontend/components/landing/Landing.tsx frontend/components/ui/TopBar.tsx && ! grep -q '"/home" ? "/"\|=== "/home"' frontend/components/landing/Landing.tsx \
  && { echo "  TODO     landing nav Home points at /home (signed-out visitors bounce to sign-in)"; fail=1; } || echo "  ok       landing nav Home"

say "5. Static checks"
if [[ "${PARITY_SKIP_STATIC:-0}" == "1" ]]; then
echo "  skipped (structural pass only; run without PARITY_SKIP_STATIC for full verification)"
else
( cd frontend && npx tsc --noEmit -p . >/dev/null 2>&1 ) && echo "  ok       typecheck" || { echo "  FAIL     typecheck"; fail=1; }
( cd frontend && npx eslint . >/dev/null 2>&1 ) && echo "  ok       lint" || { echo "  FAIL     lint"; fail=1; }
test_log=$(mktemp)
if (cd frontend && npx vitest run) >"$test_log" 2>&1; then
  grep -E 'Test Files|Tests ' "$test_log"
else
  cat "$test_log"; fail=1
fi
go build ./... >/dev/null 2>&1 && echo "  ok       go build" || { echo "  FAIL     go build"; fail=1; }
fi

say "6. Browser parity (backend's own Playwright suite)"
echo "  needs pgvector Postgres + Temporal (make setup); run 'make test-e2e' where those exist"

say "RESULT: $([ $fail -eq 0 ] && echo PASS || echo 'NOT YET (see above)')"
exit $fail
