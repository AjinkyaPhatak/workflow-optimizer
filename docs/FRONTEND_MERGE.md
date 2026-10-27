# Merging a frontend overhaul

This checkpoint separates backend/runtime, local tooling, frontend contracts and
state, and frontend presentation. Merge the complete series into the redesign
branch, then resolve presentation conflicts while retaining the contracts below.
Cherry-picking only the screen commit is insufficient: it depends on the preceding
API/state commit and backend endpoints. No frontend overhaul branch has been
merged or tested against this checkpoint yet.

## Commit map

The series starts after upstream `290b95c`:

| Commit | Scope |
| --- | --- |
| `4b8bd5f` | Database test isolation and invalid-session recovery |
| `9eefaaa` | Backend contracts, compiler, Temporal runtime and integrations |
| `4eab87e` | Local setup, infrastructure and disposable test databases |
| `6b2e402` | Frontend API contracts, request state, dependencies and doctor logic |
| `79b7e11` | Screens, navigation, styling and UI/browser regression coverage |
| This documentation commit | Development instructions and merge guidance |

The backend snapshot passed service-independent `go test ./...`. The frontend
API/state snapshot passed TypeScript and all 156 unit tests while retaining the
previous screens. The final combined Go/Next.js production build passed. Earlier
checks of the complete implementation include 161 frontend unit tests and the
targeted doctor/input browser flows; see [OVERHAUL.md](OVERHAUL.md) for the wider
integration/race record and live-provider limits. These checks validate this
checkpoint, not an as-yet-unmerged redesign.

## Boundaries

| Area | Files | Merge intent |
| --- | --- | --- |
| Backend and runtime | `internal/`, `cmd/`, `migrations/`, `workers/`, `go.mod`, `go.sum` | Preserve v1 behavior and v2 Temporal ownership; no layout coupling. |
| Local tooling | `Makefile`, `scripts/`, Compose/Docker files, env examples | Keep isolated test databases and preserve existing local secrets/data. |
| Frontend contracts and state | `frontend/types/automation.ts`, `frontend/types/api.ts`, `frontend/lib/api/`, `frontend/features/assistant/` | Keep endpoint contracts, revision checks, recovery and automatic repair behavior. |
| Presentation | `frontend/app/`, `frontend/components/`, `frontend/app/globals.css` | Reconcile with the new design; preserve behavior and accessible controls. |
| Knowledge and account support | `frontend/features/knowledge/useKnowledge.ts`, `frontend/lib/connectedAccounts.ts` | Kept with presentation because the existing UI and test fixtures change with them. |
| Dependencies and browser configuration | `frontend/package.json`, `frontend/package-lock.json`, `frontend/next.config.ts` | Merge required capabilities and regenerate the lockfile with npm if dependencies differ. |
| Regression coverage | `frontend/tests/unit/`, `frontend/e2e/` | Adapt selectors to the new design; retain the behavioral assertions. |

Likely presentation conflicts are the navigation in `TopBar.tsx`, root/login/
dashboard redirects, Knowledge and Connected Accounts screens, the workflow
editor header, and appended styles in `globals.css`. Avoid taking either side of
these entire files without reviewing the behaviors below.

## Behaviors to retain

- Authenticated landing is `/home`. Templates and the legacy editor remain
  available. `/files` redirects to `/knowledge/files`; Knowledge has files,
  indexing and URL ingestion. The legacy editor can create an assistant copy
  without modifying the original version.
- `useAssistant` owns asynchronous conversation state. Typed text remains editable
  during workspace discovery and survives failed sends. Workspace discovery can
  recover independently of the tool catalog. A missing account returns 401 and
  follows the existing sign-in recovery flow.
- Assistant updates carry the current revision. A 409 refreshes current state;
  it must not trigger a stale edit or execution. Starting a new conversation
  ignores late responses from the previous one. Keep answers when refining drafts.
- Technical diagnostics trigger the doctor automatically, with no repair button
  or technical issue checklist. `workflowDoctor.ts` limits each revision to one
  automatic request and remembers attempts in session storage across reloads.
  The backend bounds each repair request to three passes. Show progress and a
  readable stopped state when repair is exhausted; do not create an endless loop.
- Missing user facts still use typed clarification cards (including choices with
  a free-text alternative). Repair cannot guess these facts. Decision controls
  use comparison options from the backend catalog, not a separate enum.
- Successful repair may continue a new request or answer only while **Run when
  ready** remains enabled. Opening, repairing or manually editing an existing
  draft must not submit a run. Keep run pause/resume/stop, human waits and
  uncertain-action reconciliation controls.
- Voice tokens are minted by the backend and never persisted in browser storage.
  Wait for actual microphone/SDK readiness, prevent duplicate starts, retain the
  current conversation in callbacks and distinguish permission/provider failures.
  Typed input remains available after voice failure. Keep spoken replies optional.
- Keep `next.config.ts`'s five-minute proxy timeout for planning requests and
  `microphone=(self)` permission policy alongside the existing security headers.
- Render AI prose through `AiMarkdown`, including its readable fallback. Keep
  technical JSON literal. Treat the backend plan as authoritative; the canvas is
  derived presentation and node options come from metadata.
- Connected Accounts shows Google, Notion, Airtable and Discord even before
  configuration, plus stored/additional providers. Unconfigured providers show
  setup guidance; configured providers retain their server-controlled OAuth flow.
  Keep the frontend-origin callback. Personal WhatsApp pairing has its own screen.

## Verification after merging

Use the supported Node version and npm. Stop the development app before the
production build or browser suite because they share Next.js build artifacts.

```sh
npm --prefix frontend ci
make check
make test-e2e
make build
make dev
```

Run `make test-race` as well if backend, persistence or runtime code changes;
keep it separate from build/browser jobs. Service tests must use the Make targets'
disposable database wrapper, not the development database directly.

Retain the browser flows in `workflow-repair.spec.ts`, `input-recovery.spec.ts`,
`connected-accounts.spec.ts` and `voice-workflow.spec.ts`, plus the original editor,
execution and template flows. Browser tests use mocked external providers; they
do not establish physical microphone, live WhatsApp pairing or CUDA coverage.

See [OVERHAUL.md](OVERHAUL.md) for implementation details and the verification
record, including the recovered local-data incident and remaining live checks.
