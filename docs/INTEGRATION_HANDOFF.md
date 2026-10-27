# Integration handoff: frontend-overhaul + backend-overhaul

## Integration follow-up (2026-10-28)

The compatibility and design work below is implemented on `integration`. Public
navigation and assistant entry points are reconciled; a shared header-height
token prevents canvas overflow. Compiler workflow cards use compiler plans/runs
and omit unsupported legacy mutations, with real step icons, statuses and mixed
template suggestions. Legacy card mutations and Library file ingestion report
failures and remain usable for retry. Library retains URL ingestion, local-search
copying and source labels. `/knowledge/files` remains a compatibility redirect to
`/library`; its unused tabs component is removed. Templates and backend screens
use the new palette and control icons.

Backend baseline at `9edf2d1`: `make check` passed with 204 frontend tests;
all 17 browser tests passed. Integration before the final Pearson merge:
`make check` passed with 217 frontend tests and all 19 browser tests passed.
Added regression coverage exercises
compiler/legacy API routing, workflow-card failures, Library capabilities,
public/mobile navigation and canvas sizing. Final merged validation is recorded
in the completion section below.

The original handoff below records the initial merged checkpoint and the reasoning
behind each repair; its pending-gap list and original test counts are historical.
No source branch is rebased and `main` remains untouched.

### Completion record

The Pearson planning/recovery fix `9edf2d1` is merged after the integration
repairs. Its backend contracts and state recovery remain authoritative; the
redesigned screens retain the reviewed integration tests and routing changes.
The final validation record is added when the merged tree has completed checks.

For the agent finishing the integration on the machine that has the backend
stack (`make setup` done). Goal: one functional build with the backend's full
behaviour and the frontend overhaul's design, ready to fast-forward `main`.
Everything below was logic-checked against the code; nothing has been run in a
browser yet.

## Where things stand

Branch `integration` = `frontend-overhaul` + `git merge --no-ff backend-overhaul`,
with the 6 conflicts resolved (table below). Nothing else is changed yet.
Typecheck, lint, 193 unit tests, `next build` and `go build ./...` pass. The
browser and race suites have not run: the authoring machine has no Docker.

## Next steps, in order

1. Check out and install:
   - `git fetch origin && git switch integration`
   - `npm --prefix frontend ci`
2. Baseline the backend, so later failures can be attributed:
   - On a checkout of `origin/backend-overhaul`, run `make check` and `make test-e2e`.
   - Record what passes.
3. On `integration`:
   - Run `bash scripts/parity-check.sh`. It lists every open gap below.
   - Run `make check` and `make test-e2e`, and compare with step 2.
4. Fix the gaps in this order. Make one commit per item and rerun the parity check after each.
   1. **M1**: landing nav.
   2. **B2**: top-bar height token.
   3. **G1, G2, G3**: compiler-aware workflow cards and suggestions.
   4. **G4**: Library knowledge features and the orphaned Knowledge routes.
   5. **E2E**: the two spec fixes.
   6. **B1**: restyle the backend screens.
   7. **G5, G6**: template looks, top-bar gradient.
   8. **M2**: hero → `/home`, if the owner agrees.
5. Get everything green:
   - `parity-check` must print `RESULT: PASS`.
   - `make check` and `make test-e2e` must pass everything that passed in the baseline.
   - Run `make test-race` separately; its known pre-existing failure is described below.
   - Then run `make build`.
6. Do the manual pass at the end of this file with `make dev`, then push `integration`.
   `main` fast-forwards to it when the owner approves.

Do not edit backend-owned files beyond the allowed list in the parity script.
If a fix seems to need one (for example rename/delete endpoints for compiler
workflows), stop and ask the owner. Hiding the action in the UI is the fallback.

## Ground rules

- **Behaviour, UX, integrations and backend features: `backend-overhaul` wins.**
  It is the tested, more capable product. The merge must not change what it does.
- **Look and page design: `frontend-overhaul` wins.** Palette, landing page,
  motifs/backdrops, page layouts, the Library page, top bar design.
- Files the backend owns (API client, assistant state, types, stores, Go
  runtime, migrations, tests, config, dependencies) must stay byte-identical to
  `backend-overhaul`, except the two e2e specs listed below.
  `scripts/parity-check.sh` enforces this; rerun it after every change.

## Branch history (already done)

- `integration` started from `frontend-overhaul`. That branch contains main's
  landing-page commit `8f782c7`; `backend-overhaul` forked one commit earlier.
- `backend-overhaul` was merged with `--no-ff`. Do not rebase either branch.
- `main` is an ancestor, so it fast-forwards to `integration` at the end.

## Merge conflicts (6 files) and how they were resolved

| File | Resolution |
|---|---|
| `frontend/app/page.tsx` | frontend: public landing page (backend redirected `/` to `/home`) |
| `frontend/app/files/page.tsx`, `frontend/app/knowledge/page.tsx` | frontend: redirect to `/library` |
| `frontend/components/settings/Knowledge.tsx` | backend's file, plus `export` on `AddDocuments` (the Library imports `STATUS`, `CreateBase`, `AddDocuments`, `TrySearch`) |
| `frontend/app/login/page.tsx` | backend headline ("Say what you need") and `/home` redirect; frontend's "Back to home" link |
| `frontend/components/ui/TopBar.tsx` | `export const SECTIONS` = Home `/home`, Workflows, Templates, Library `/library`, Automations; backend's WhatsApp settings entry; frontend's mobile menu; logo `session ? "/home" : "/"` |

`frontend/app/globals.css` merges automatically. It ends up 130 lines shorter
than the backend's: that is the old indigo theme block, deliberately moved to
`app/styles/theme.css`. Every selector and token from it is redefined there.
All 32 of the backend's assistant/voice/clarification rules are kept.

State of the trial merge: typecheck, lint, all 193 unit tests, `next build`
and `go build ./...` pass. Browser tests not run (see Software).

## Gaps to fix after the merge

### From frontend-overhaul (my pages assumed the legacy workflow model)

Background: after the merge, "New workflow" and template "Use" create
**compiler workflows** (`automation_workflows`, via `assistantApi`). Their ID is
also their assistant-session ID. The Workflows page lists both kinds, but
`workflowSummary()` does not mark which is which, and the backend has **no
rename or delete endpoint** for compiler workflows.

**G1. Workflow cards: actions** (`app/workflows/page.tsx`, `components/workflow/WorkflowCard.tsx`)
- Now: Rename calls `PATCH /workflows/{id}`, Delete calls `DELETE /workflows/{id}`
  and Run history links to `/workflows/{id}/executions`. All are legacy-only, so for
  compiler workflows they 404 or open a broken page.
- Fix: identify compiler workflows in frontend code without touching backend-owned
  files: call `assistantApi.workflows(workspaceId)` once and build an ID set
  (`useWorkflowCards` is the natural place). For compiler workflows, hide Rename
  and Delete, and point Run history at the editor (`/workflows/{id}/editor`), whose
  History panel (`WorkflowManagement`) lists that workflow's runs.
- Optional backend follow-up: rename/delete endpoints for compiler workflows.

**G2. Workflow cards: steps and status** (`features/workflows/useWorkflowCards.ts`)
- Now: it calls `workflowApi.listVersions` / `getVersion` / `executions` for every
  workflow. For compiler workflows these 404 (two failed requests per card),
  so cards show no app icons and always say "Draft".
- Fix, compiler workflows only:
  - steps: `assistantApi.workflow(id)` returns `session.compilation.plan.steps`.
    Flatten `children`/`otherwise`, then map `kind === "tool"` to
    `tool.replace(/^node\./, "")`, as `templateSummary()` does.
  - last run: one `assistantApi.runs(workspaceId)` call, then the newest run with
    `session_id === workflow.id`.

**G3. Workflow page suggestions** (`useWorkflowCards.ts`, line ~55)
- Now: reads only `workflowApi.templates`, so it never suggests compiler templates.
- Fix: merge `assistantApi.templates` the same way `useTemplateGallery` does.

**G4. Library missing two backend knowledge features** (`components/library/Library.tsx`)
- Missing: the "Web page" label for `source === "url"` documents.
- Missing: "Copy to local search", shown when `kb.embedding_model !== "intfloat/multilingual-e5-small"`.
  It calls `s.copyLocal()` and shows "Copying…" while `s.busy === "copy"`.
- Both exist in the backend's `Knowledge` component, which no route renders any more.
- Also delete the unreachable `app/knowledge/files/page.tsx` and
  `components/settings/KnowledgeTabs.tsx`. Leave `Knowledge`/`Files`: unit tests use them.

**G5. Template looks (cosmetic)** (`TemplateGallery.tsx`)
- Compiler templates default to category "Workflows", which has no `CATEGORY_LOOK` entry.
- Steps like `condition`, `map` or `delay` have no node icon.

**G6. Top-bar gradient (pre-existing)** (`app/styles/theme.css`, `.topbar`)
- The top bar still uses a `linear-gradient`, against the no-gradients rule.

### From backend-overhaul (screens not yet in the new design)

**B1. Off-palette styling** (`app/globals.css`, assistant block)
- 26 hard-coded purples and a `radial-gradient` on `.welcome-orbit`.
- Move them to palette tokens: `--charcoal`, `--verdigris`, `--tuscan`, `--sandy`, `--peach`.
- The `/home` assistant, compiler canvas, Automations and WhatsApp pages are plain
  `dashboard` layouts; give them the Backdrop and motif treatment.

**B2. 52px top-bar assumption.** The new top bar is 60px. Replace these with one token
(for example `--topbar-h: 60px` in `theme.css`):
- `components/assistant/workspace.css:2`: `height: calc(100dvh - 52px)`
- `components/assistant/workspace.css:32`: `top: 52px`
- `app/globals.css:32`: `calc(100dvh - 52px)`
- `app/globals.css:158`: `calc(100dvh - 52px)`, twice

Leave `height: 52px` / `min-height: 52px` on the editor headers alone: those are
the headers' own heights. Until this is fixed, `/home` and the compiler editor
overflow by 8px.

### Created by the merge itself

**M1. Landing nav** (`components/landing/Landing.tsx`)
- `SECTIONS` now starts with Home → `/home`, which requires sign-in, so signed-out
  visitors get bounced to /login.
- The "is-on" check `s.href === "/"` no longer matches.
- Automations is listed for visitors.
- Fix: on the landing page, map Home → `/` and hide Automations when signed out.

**M2. Two creation entry points (UX decision)**
- The backend's primary creation surface is the `/home` assistant.
- The Workflows hero ("What would you like to automate?") is a second one.
- Suggested: the hero leads into `/home`; "New workflow" stays as the blank-canvas option.

## Browser tests that will fail after the merge (logic-checked)

1. **`e2e/workflow-editor.spec.ts:65`**: `getByRole("link", { name: workflowName })`.
   - The card has two matching links (band `aria-label="Open X"` plus the name), which breaks Playwright's strict mode.
   - The Recent rail also keeps the list off-screen until hover.
   - Fix the card: give the band link `aria-hidden` and `tabIndex={-1}`.
   - Fix the spec: hover `getByTestId("recents")` before clicking.
2. **`e2e/connected-accounts.spec.ts:28-30`**: expects `/files` → `/knowledge/files`
   and the "Knowledge sections" nav. Change it to expect `/library`, and assert the
   Library (for example `getByTestId("files")` and `getByTestId("knowledge")`).

Expected to pass unchanged:
- phase-b: `new-workflow`/`browse-templates` test IDs and the Advanced `role="tab"` are kept.
- canvas-workspace: the "Search templates" label, the search shows one grid so there's
  one button match, and "Name your workflow" / `use-template` are kept.
- editor-ux, execution-debugger: these go straight to editor URLs.
- input-recovery, voice-workflow, workflow-repair.

These two existing spec edits plus the new `integration.spec.ts`,
`workflow-cards.test.tsx` and `library-integration.test.tsx` are the reviewed test
exceptions in the parity script. All existing backend contracts and state tests
remain protected. `BACKEND_REF` can pin the pre-Pearson checkpoint while the final
fix is deliberately deferred. `PARITY_SKIP_STATIC=1` runs structural checks only;
the default runs the static checks too and propagates test-process failures.

## Software needed to run the full checks

Already on this Windows machine:
- Go 1.27.3
- Node 22.20 (needs 22.13+)
- Python 3
- PostgreSQL 18 (no pgvector)

Missing here, needed on the test machine:

| Tool | Why |
|---|---|
| Docker + Docker Compose | `make setup`/`make infra`: PostgreSQL 16 with **pgvector** (migration 000011 runs `CREATE EXTENSION vector`), Redis, **Temporal** dev server, local embeddings, analysis image |
| GNU Make | every `make` target |
| C compiler (gcc/clang) with CGO | `make test-race` |
| Playwright Chromium | `make browsers` |
| Optional: NVIDIA runtime | CUDA embeddings; CPU works |

No real AI keys are needed for the tests. The browser suite mocks OpenAI and
WhatsApp. Running the app for real needs `OPENAI_API_KEY` (planner, paid) and,
for voice, `ELEVENLABS_*`.

## Test sequence on the other machine

Stop any running dev app first: builds and browser tests share `.next`.

```sh
git fetch origin && git switch integration
npm --prefix frontend ci
bash scripts/parity-check.sh                    # structure + behaviour + static checks
make check                                      # Go tests/vet, tsc, eslint, unit tests
make browsers                                   # first time only
make test-e2e                                   # backend's 9 browser specs, mocked providers
make test-race                                  # separately from e2e/build
make build
make dev                                        # manual pass, below
```

Compare against `backend-overhaul`: run `make test-e2e` on a checkout of
`origin/backend-overhaul` first, record the result, then on the integration branch.
Any test that passes there and fails here is a regression from the merge.

**Known pre-existing failure.** `tests/phase10/runtime_test.go:276`, the lease
stress test under `make test-race`, also fails on `backend-overhaul` (see
`docs/DEVELOPMENT.md`). It is not caused by this merge.

### Manual pass after `make dev` (http://localhost:3000)

1. Signed out:
   - `/` shows the landing page.
   - Nav Home stays on `/`.
   - "Sign in" works.
   - After sign-in you land on `/home`.
2. `/home`:
   - The assistant builds a draft from a typed request.
   - Clarification cards appear.
   - With "Run when ready" off, nothing runs.
   - There is no 8px page scroll.
3. Workflows:
   - New workflow opens the canvas editor.
   - The new card shows its app icons and status.
   - Its menu has no Rename/Delete (or working ones).
   - Run history opens the editor's History.
   - Legacy workflows still rename and delete.
4. Templates:
   - Built-in and saved compiler templates appear with categories.
   - "Use" creates a workflow and opens the editor.
   - Deleting a saved template works.
5. Library:
   - Upload a file and add it to a knowledge base.
   - Add a web page: it shows "Web page".
   - Create a base with "Local search — no API key needed".
   - Search returns formatted passages.
   - "Copy to local search" appears on a hosted base.
   - `/files` and `/knowledge` both redirect to `/library`.
6. Top bar: Automations and WhatsApp settings open, and the logo goes to `/home` when signed in.
7. Legacy editor: "Open in voice builder" makes a copy and opens `/home?session=…`.
8. Phone width: the top-bar menu, the Recent block that opens on tap, and the Library upload strip.

## Provider setup

The backend planner uses the configured OpenAI model; voice uses ElevenLabs.
The owner has already configured these providers. Integration verification uses
mock providers and synthetic inputs, preserving those local credentials.
