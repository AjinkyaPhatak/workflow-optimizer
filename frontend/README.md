# Workflow Optimizer frontend

The voice workspace and visual workflow builder: a Next.js app that is a client of the
Go REST API (`/api/v1`). It has no database, queue or execution logic of its
own.

The authenticated landing is `/home`, with voice/typed requests, automatic workflow
repair, clarification cards and an editable draft. `/automations` manages schedules
and triggers, `/knowledge` holds retrieval and ingestion, `/knowledge/files` holds
files, and `/settings/whatsapp` handles personal pairing. The legacy workflow editor
and templates remain available. See [the merge guide](../docs/FRONTEND_MERGE.md)
before replacing screens or styles.

## Run

```sh
# From the repository root:
make setup
make dev
```

Or run `make api`, `make worker`, `make orchestrator`, `make connector`, and
`make frontend` in separate terminals.
Setup creates `frontend/.env.local` pointing to the API at
`http://127.0.0.1:18080`. From this directory, `npm ci` installs locked
dependencies and `npm run dev` starts just the frontend (the backend processes
must already be running). Use `nvm use` at the root to select Node 22.

The Next.js server proxies `/api/v1/*` to `BACKEND_URL` (the Go API), so the
browser only ever talks to one origin and the API needs no CORS.

## Structure

- `app/` — routes: `/login`, `/workflows` (dashboard), `/workflows/[workflowId]/editor`, `/workflows/[workflowId]/executions` (execution list), `/executions/[executionId]` (execution debugger, Phase 14).
- `lib/api/` — the only place that does HTTP (`authApi`, `workflowApi`, `executionApi`, `nodeApi`, `credentialApi`, `projectApi`).
- `lib/auth/` — the session (the API's bearer token) and the route guard.
- `lib/workflow/` — pure logic: definition helpers, the workflow ↔ canvas mapper, port-compatibility feedback, `{{variable}}` discovery for the picker (`variables.ts`), readable field labels (`labels.ts`), the wording of backend validation findings (`issues.ts`) and copy/paste fragments (`clipboard.ts`).
- `stores/workflow-editor/` — editor state (working copy, selection, dirty, validation display, undo/redo).
- `features/` — server state and actions (dashboard, editor session: save / validate / publish, executions).
- `features/assistant/` — conversation, revision checks, automatic doctor and request recovery; keep this behavior separate from the screen layout.
- `types/automation.ts`, `lib/api/assistant.ts` — v2 contracts and HTTP boundary.
- `components/assistant/`, `components/ui/AiMarkdown.tsx` — assistant presentation, typed questions, draft controls and safe Markdown rendering with a readable fallback.
- `components/` — canvas, node renderer, palette, configuration panel, validation/run panel, header; `components/executions/` holds the debugger (timeline, node inspector, JSON viewer, event log, status graph).

The debugger only shows persisted data: the execution snapshot, node records and events from the API. `features/executions/timeline.ts` builds the timeline; `poller.ts` polls every 2 s while the execution is PENDING or RUNNING and stops once it is finished.

The workflow definition is the source of truth. The canvas is derived from it
(`toCanvas`) and canvas edits are written back through `fromCanvas`; only the
definition is sent to the API. Node types, ports and configuration fields come
from `GET /api/v1/nodes`; nothing is hardcoded.

Editor assistance is presentation only: hover cards and the configuration
panel render the backend NodeDefinition (descriptions, ports, config fields);
the variable picker lists the references the backend resolver accepts (the run
input and outputs of upstream nodes) but resolves nothing; validation findings
come from the backend and are only reworded and located on the canvas.

Phase B: configuration controls follow the backend field metadata (labels,
options with conditions, ranges); workflow variables are edited in the
workflow panel and saved in the definition; the version badge opens the
version history; the palette search covers names, descriptions, categories
and ports with keyboard navigation; new workflows can start from the
backend's templates (`GET /api/v1/templates`).

Existing legacy drafts can be updated through the draft endpoint; published
versions remain immutable. The editor also supports temporary draft execution.
Assistant drafts have optimistic revisions; saved v2 run plans are immutable.

## Checks

```
npm run typecheck
npm run lint
npm test            # unit tests (Vitest)
npm run build
# From the repository root, with infrastructure running:
make test-e2e      # browser E2E in a disposable database
```

The E2E fixture needs PostgreSQL, Redis, Temporal, embeddings, the analysis image
and the Go toolchain (`make setup`). The Make target creates a disposable database;
the fixture additionally isolates its schema, queues and Temporal task queue,
starts real backend processes, and mocks external providers. Voice tests use a
fake microphone and mocked realtime transport. Install Chromium once with
`make browsers`. Stop the development app before browser tests or production builds
because they share Next.js build artifacts. See [development](../docs/DEVELOPMENT.md)
for environment overrides and validation boundaries.
