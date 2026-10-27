# Development guide

The v2 voice/runtime overhaul is documented in [OVERHAUL.md](OVERHAUL.md).
The unified canvas, compiler editor and saved-workflow API are documented in
[CANVAS_EDITOR.md](CANVAS_EDITOR.md).
The repository audit and initial verification sections retain the pre-overhaul
baseline. See [FRONTEND_MERGE.md](FRONTEND_MERGE.md) when merging a frontend redesign.

## Setup and run

Run `make setup`, then `make dev`. Register an account at http://localhost:3000.
Setup generates `.env` and `frontend/.env.local` only if absent. The root env
file contains random local signing and encryption keys and is created with mode
0600. Preserve it when keeping the PostgreSQL volume.

Compose starts this project's PostgreSQL 16 with pgvector, Redis 7, Temporal,
and local embeddings. PostgreSQL and Redis bind to loopback on ports 55432 and
56379 by default; the other ports are listed in [OVERHAUL.md](OVERHAUL.md).
Other local containers and system
services can keep using 5432/6379/8080. Change `POSTGRES_PORT`, `REDIS_PORT`, and
the corresponding URLs together in `.env` if needed. The API listens on
127.0.0.1:18080; `BACKEND_URL` in both local env files must match.

`make dev` builds four Go binaries (API, legacy worker, Temporal orchestrator,
and WhatsApp connector) and supervises them with Next.js. It stops
the application when any child exits or when interrupted. It does not restart
Go processes on source changes; restart it after backend edits. Next.js provides
frontend hot reload. To run processes in separate terminals, use `make api`,
`make worker`, `make orchestrator`, `make connector`, and `make frontend`.
Keep production build and browser-test runs
separate from the frontend dev server: they share Next.js build artifacts.

Use `curl http://127.0.0.1:18080/health` for liveness and `/ready` for database
and Redis readiness. `docker compose ps` shows infrastructure health;
`docker compose logs postgres redis` shows service logs. `make down` preserves
PostgreSQL data. Redis is an ephemeral delivery queue; PostgreSQL retains state.

## Code map

| Path | Responsibility |
| --- | --- |
| `cmd/api`, `cmd/worker`, `cmd/orchestrator`, `cmd/connector`, `cmd/migrate` | Process entry points |
| `internal/app` | Dependency composition and process lifecycle |
| `internal/api` | Routes, middleware, request/response contracts, HTTP handlers |
| `internal/application` | Authenticated use cases and tenant access checks |
| `internal/automation`, `internal/provider/planning` | V2 contracts, compiler/checker, structured planning |
| `internal/orchestration`, `internal/toolbox` | Temporal workflow ownership, activities and tools |
| `internal/workflow` | Definitions, graph validation, node configuration |
| `internal/execution` | Generic graph execution, lifecycle state machines, resolution |
| `internal/queue`, `internal/worker`, `internal/reliability` | Delivery, workers, retry scheduling, leases/recovery |
| `internal/node` | Node contracts, metadata/registry, built-in implementations |
| `internal/provider/llm` | Provider abstraction; OpenAI-compatible adapters |
| `internal/integration`, `internal/oauth`, `internal/connectedaccount` | External app nodes, sign-in, account tokens |
| `internal/infrastructure` | PostgreSQL/Redis adapters and encryption |
| `internal/auth`, `internal/credential`, `internal/workspace` | Identity, protected credentials, workspace contracts |
| `internal/files`, `internal/knowledge`, `internal/templates`, `internal/usage` | Files, retrieval, template catalog, usage |
| `internal/observability` | Execution events, metrics, redaction, cost estimates |
| `migrations` | Versioned PostgreSQL schema (up/down) |
| `tests` | Cross-package acceptance and integration tests |
| `frontend/app`, `frontend/components` | Next.js routes and React UI |
| `frontend/features`, `frontend/stores` | Server actions/state and editor state |
| `frontend/lib/api`, `frontend/types/api.ts` | Browser HTTP client and API types |
| `frontend/lib/workflow` | Pure editor mapping, references, labels, validation presentation |
| `frontend/tests/unit`, `frontend/e2e` | Vitest and Playwright suites |

For legacy v1 workflows, the API persists and submits jobs; the worker claims and executes them.
PostgreSQL decides ownership and state; Redis carries only execution IDs.
Registered nodes own external behavior; the graph executor stays provider-neutral.
V2 runs use Temporal with separate PostgreSQL projections, artifacts and action
ledgers. Legacy claims and Redis queues never own v2 execution.
The browser talks to Next.js, which proxies `/api/v1/*` to Go. Configuration
controls in the editor come from backend node metadata.
The Next.js proxy timeout is five minutes, exceeding the assistant compilation
and import handlers' four-minute write deadline. The default thirty-second proxy
timeout otherwise resets slow planner requests and surfaces a generic HTTP 500.

## Validation

`make check` runs service-independent Go tests, Go vet, TypeScript, ESLint, and
frontend unit tests. If `TEST_*` variables are already exported, Go tests also
use those services. To explicitly use the project services, run
`make test-integration` or `make test-race`; neither starts infrastructure, so
run `make setup` or `make infra` first. Race tests require CGO and a C compiler.
The integration, race and browser Make targets now create a disposable database
using `scripts/test-services.mjs`. This requires the installed frontend npm
dependencies and PostgreSQL CREATE DATABASE permission (the local role has it).
`TEST_DATABASE_URL`, when provided, selects the server used to create that
database; the supplied database itself is never the test target. Cleanup removes
only the generated database. Each suite additionally isolates schemas and queues.
Both integration targets run packages one at a time (`-p 1`): the shared
database and subsecond lease tests are sensitive to contention when all packages,
browser tests, and builds compete for resources. Tests within each package still
exercise worker concurrency. Keep browser/build runs separate from reliability tests.

Each integration test creates its own schema and Redis keys. Tests deliberately
skip when required URLs are unset. External provider calls use local mock servers
or fake implementations and do not need real credentials. Use the existing
cross-workspace, lifecycle, retry, redaction, and architecture tests as regression
coverage when changing those boundaries.

Install the browser with `make browsers`, then use `make test-e2e`. Playwright
builds and runs the real Go API/worker, creates a fresh schema, seeds test-only
users/credentials, and starts a mock AI endpoint. Browser ports default to
3100 (web), 18180 (API), and 18190 (mock); `E2E_WEB_PORT`, `E2E_API_PORT`, and
`E2E_OPENAI_PORT` override them. The fixture cleans up schemas/queues and child
processes at teardown. It can also run manually from `frontend/` via
`node e2e/dev-stack.mts` with `TEST_DATABASE_URL` and `TEST_REDIS_URL` set.

## Configuration and external accounts

Go reads the environment directly. Make's runtime commands source `.env`;
when invoking `go run` yourself, export the variables first. See
`configs/README.md` and `.env.example`. Put workflow-node provider API keys into
encrypted workspace credentials through the UI/API. Assistant and voice defaults
can use server-side environment keys as documented in [OVERHAUL.md](OVERHAUL.md).
OAuth requires your own provider app
registration and the exact frontend-origin callback URI. Local startup and
mocked tests work without those registrations.

Connected accounts always lists Google, Notion, Airtable and Discord, plus any
additional configured providers or stored accounts. Before a provider is configured,
its Connect button opens setup instructions with the callback for the current web
origin; it does not start an invalid OAuth request. Google needs `GOOGLE_CLIENT_ID`,
`GOOGLE_CLIENT_SECRET` and `GOOGLE_OAUTH_REDIRECT_URI` in the root `.env`, followed
by an application restart. Register the exact callback URL with the provider.
Provider keys and the OpenAI/ElevenLabs API keys cannot replace an OAuth client.
Configured providers retain their server-controlled authorization flow.

## Repository audit notes

The checkout started at upstream `main`, commit `290b95c` (2026-10-26).
The initial audit found stale skeleton descriptions in the root README and
configuration/migration docs, no local env loader or migration entry point,
and default port conflicts on the development host. The setup files in this
change address those gaps. Some older phase comments remain historical;
check production registration in `internal/app/app.go` for current integrations.

No CI workflow existed in the upstream checkout. Local checks are exposed through
Make. AI credentials and OAuth application registrations are local configuration
and are not provisioned by setup.

## Initial verification (2026-10-27)

- Setup and repeated setup succeeded, preserving both existing environment files
  and their secrets. The database reports migration version 10 with no dirty state.
- Go vet and module verification passed. TypeScript, ESLint, and all 150 frontend
  unit tests (25 files) passed. npm reported no known dependency vulnerabilities.
- Go API/worker builds and the Next.js production build passed.
- All four Playwright flows passed against the real API, worker, database, and
  queue with mocked AI: editor assistance, debugger/retries, templates/versions,
  and building/running a workflow.
- The Docker image built successfully and contains both binaries and CA certificates.
- `make dev` started all three processes. API liveness/readiness and frontend
  login returned HTTP 200; the proxied authenticated API correctly returned 401
  without a token. Ctrl+C stopped every application listener cleanly.
- The initial concurrent Go race run passed every package except `tests/phase10`.
  Four short-lease/cancellation assertions failed under load and passed in a
  subsequent isolated run. That isolated run then failed the 100-execution stress
  test (`tests/phase10/runtime_test.go`): a permanent-error execution used two
  attempts where one was expected. These are assertion failures; no Go data-race
  report was emitted. Test and application behavior were preserved. Integration
  targets now serialize packages to reduce contention, but the stress result
  remains a finding to investigate rather than a proven runtime fix.
- A full serial race run reproduced the stress assertion: a retryable-error
  execution completed on attempt 3 where the test expected attempt 2. Serializing
  packages resolves neither that assertion nor its root cause. Do not report the
  race suite as green. Start investigation at `tests/phase10/runtime_test.go:276`,
  its 400 ms lease configuration, and the persisted lease/recovery history.
- The serial run finished: all other Go packages passed. Full setup, build,
  browser, and race-run logs are retained locally under `.local/verification/`
  (ignored by Git). The development app was left running on port 3000.
