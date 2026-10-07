# Workflow Optimizer frontend

The visual workflow builder (Phase 13): a Next.js app that is a client of the
Go REST API (`/api/v1`). It has no database, queue or execution logic of its
own.

## Run

```
cd frontend
npm install
BACKEND_URL=http://localhost:8080 npm run dev
```

The Next.js server proxies `/api/v1/*` to `BACKEND_URL` (the Go API), so the
browser only ever talks to one origin and the API needs no CORS.

## Structure

- `app/` — routes: `/login`, `/workflows` (dashboard), `/workflows/[workflowId]/editor`, `/workflows/[workflowId]/executions` (execution list), `/executions/[executionId]` (execution debugger, Phase 14).
- `lib/api/` — the only place that does HTTP (`authApi`, `workflowApi`, `executionApi`, `nodeApi`, `credentialApi`, `projectApi`).
- `lib/auth/` — the session (the API's bearer token) and the route guard.
- `lib/workflow/` — pure logic: definition helpers, the workflow ↔ canvas mapper, port-compatibility feedback, variable suggestions.
- `stores/workflow-editor/` — editor state (working copy, selection, dirty, validation display, undo/redo).
- `features/` — server state and actions (dashboard, editor session: save / validate / publish, executions).
- `components/` — canvas, node renderer, palette, configuration panel, validation/run panel, header; `components/executions/` holds the debugger (timeline, node inspector, JSON viewer, event log, status graph).

The debugger only shows persisted data: the execution snapshot, node records and events from the API. `features/executions/timeline.ts` builds the timeline; `poller.ts` polls every 2 s while the execution is PENDING or RUNNING and stops once it is finished.

The workflow definition is the source of truth. The canvas is derived from it
(`toCanvas`) and canvas edits are written back through `fromCanvas`; only the
definition is sent to the API. Node types, ports and configuration fields come
from `GET /api/v1/nodes`; nothing is hardcoded.

Saving stores the definition as a new immutable DRAFT version (the API has no
version update). Publishing validates and publishes that version; only
published versions can be executed.

## Checks

```
npm run typecheck
npm run lint
npm test            # unit tests (Vitest)
npm run build
npm run test:e2e    # browser E2E (Playwright)
```

The E2E test needs PostgreSQL and Redis (`E2E_DATABASE_URL`/`E2E_REDIS_URL`,
or `TEST_DATABASE_URL`/`TEST_REDIS_URL`) and the Go toolchain. It creates a
fresh schema with the repository's migrations, builds and starts the real
`cmd/api` and `cmd/worker`, mocks only OpenAI, and drops everything afterwards.
Install the browser once with `npx playwright install chromium`.
`node e2e/dev-stack.mts` starts the same backend for manual testing.
