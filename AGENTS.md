# Workflow Optimizer

Go modular-monolith workflow platform with a Next.js/React visual editor.
Read `docs/DEVELOPMENT.md` for the code map and `README.md` for feature history.
Some phase-era comments describe earlier behavior; check implementation and tests.

## Start and verify

- `make setup`: install Go/npm dependencies, generate ignored local env files,
  start project PostgreSQL/Redis, Temporal and embeddings, and apply migrations. Existing env files and
  encryption keys are preserved. Requires Node 22.13+ (22.x), 24.x, or 26+, Go, Docker Compose v2, Bash, Make.
- `make dev`: build/start API, legacy worker, Temporal orchestrator, WhatsApp
  connector and Next.js at http://localhost:3000.
  Ctrl+C stops the application processes. Infrastructure remains running.
- `make api`, `make worker`, `make orchestrator`, `make connector`, `make frontend`:
  run each separately.
- `make check`: Go vet, frontend typecheck/lint, all Go tests and Vitest tests.
- `make test-integration`: Go tests with real project PostgreSQL and Redis.
- `make test-race`: same with `-race`; requires a C compiler.
- `make browsers`, then `make test-e2e`: install Chromium and run Playwright.
- `make build`: compile all four Go processes and build the production frontend.
- `make migrate`: apply pending migrations; `make down`: stop infrastructure.

Make exports `GOTOOLCHAIN=auto` and `GOSUMDB=sum.golang.org`. The host Go may
be older or configured with downloads disabled; do not downgrade `go.mod` to
work around that. Use npm and preserve `frontend/package-lock.json`.

Integration/race/browser Make targets create a disposable PostgreSQL database
through `scripts/test-services.mjs`, then remove it. The local database role has
the required CREATE DATABASE permission. Never reset `public` or reuse the
development database as a test sandbox; retain isolated schemas inside tests.

Go binaries do not load `.env`; Make's runtime targets use `scripts/with-env.sh`.
Default local ports: web 3000, API 18080, PostgreSQL 55432, Redis 56379.
Next.js proxies `/api/v1/*`; keep `BACKEND_URL` aligned with the API address.

## Architecture rules

- Legacy v1 flow: API → application services → PostgreSQL + Redis → legacy worker
  → execution lifecycle/graph executor → registered nodes.
- New v2 flow: assistant API → versioned compiler/checker → Temporal → orchestrator
  activities → tools. The API does not execute workflows or index documents.
- Temporal owns v2 execution. PostgreSQL holds its UI projection, artifacts,
  outbox and action ledger in separate automation tables. Legacy PostgreSQL
  claims/reapers must never own v2 runs. Redis transports legacy IDs only.
- Preserve atomic legacy claims, fencing, leases, cancellation, and retry rules.
- `internal/app` is the composition root. Wire concrete dependencies there.
  Queue, worker, execution, and domain code depend on interfaces rather than
  infrastructure drivers or provider implementations.
- The legacy graph executor stays sequential, generic, and unaware of node types,
  providers, HTTP, Redis, and database drivers. Architecture tests enforce this.
- Add built-in nodes under `internal/node`, integrations under
  `internal/integration`, and register them in `internal/app/app.go`.
  Declare metadata, ports, config schema, credentials, and side effects explicitly.
  Normalize external failures; unsafe actions must retain retry protections.
- v2 contracts/checker are under internal/automation; Temporal is in
  internal/orchestration, adapters in internal/toolbox, OpenAI in provider/planning.
  Side effects are separate activities with stable per-iteration operation IDs.
  Uncertain sends require reconciliation, never blind retries. Keep files and
  large results out of Temporal history; references hydrate inside activities.
- Use a separate TEMPORAL_TASK_QUEUE for each isolated test/development stack.
  Workflow code must be deterministic and blocking calls must use the current
  workflow coroutine's context. Human waits, event waits and delays are durable.
- Local embeddings use the pinned multilingual-e5-small revision and 384 values.
  Query/passage prefixes differ. Never mix models or dimensions in one base.
- Change lifecycle status through the state machine and conditional persistence.
  Observability reports durable transitions; event failures must not change outcomes.
- Add numbered up/down migrations; preserve already-applied migrations.
  API/worker startup must remain separate from migration execution.
- Workspace membership is the tenant boundary. Preserve authorization and
  not-found responses for resources belonging to other workspaces.
- Credentials and OAuth tokens use encrypted storage. Never log secrets or return
  decrypted values. Keep provider keys out of node configs and source files.

## Frontend rules

- `frontend/app`: routes; `features`: server interactions; `components`: UI;
  `stores`: Zustand state; `lib/api`: HTTP boundary; `lib/workflow`: pure helpers;
  `types/api.ts`: API contracts. Maintain these ownership boundaries.
- Node catalog/configuration comes from backend metadata. The workflow definition
  is authoritative; the canvas is derived presentation, not another execution engine.
- Existing draft versions are mutable through the draft endpoint; published
  versions remain immutable. Assistant drafts use optimistic revision checks.
- AI prose uses components/ui/AiMarkdown, including readable fallback. Technical
  JSON stays literal. Voice credentials are minted server-side, never persisted
  in the browser. Files are under Knowledge; authenticated landing is /home.
- Keep TypeScript strict and use the existing `@/*` aliases and React conventions.
  Match existing user-facing language; keep implementation details in technical views.

## Testing and delivery

- Use `gofmt` on changed Go files. Run checks appropriate to the changed behavior.
- Go tests requiring services skip if `TEST_DATABASE_URL`/`TEST_REDIS_URL` are
  missing. A plain `go test ./...` pass does not establish integration coverage.
  Run integration/race targets when changing persistence, API, workers, or lifecycle.
  These targets use `-p 1` to reduce contention on the shared test services;
  avoid running browser/build jobs alongside the short-lease reliability tests.
- Integration tests create isolated schemas and queue keys. Keep their cleanup and
  mock external providers; do not call real paid services as part of tests.
- Frontend tests: `npm --prefix frontend test`; browser tests use the real backend
  with an isolated schema and mocked AI via `frontend/e2e/stack.ts`.
- Report changes, actual validation, and remaining failures. Keep setup changes
  reviewable and do not commit env files, dependencies, binaries, or test artifacts.
- Preserve existing work; do not reset branches, publish, or push unless requested.
