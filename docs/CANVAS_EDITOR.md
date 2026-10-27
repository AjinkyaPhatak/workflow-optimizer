# Canvas workflow workspace

`/home` opens an empty, full-height canvas with the existing editor's node cards,
palette, icons and form styling. The bottom text/voice composer stays available
when the conversation panel is collapsed. Selecting a step opens its settings.
New entries in Workflows and compiler templates open the same workspace at
`/workflows/{id}/editor`; legacy definitions retain their original editor adapter.

## Editing and execution

AI builds and repairs lock semantic editing, dragging, connections, palette
actions, undo/redo and arrangement until completion. Pan, zoom, selection and
inspection remain available. The composer can buffer the next typed or spoken
instruction; it cannot submit another build while one is active. Stop building
aborts the request. Cancelled or superseded planner responses cannot save a draft.

Progress reports actual planning/checking/repair/saving checkpoints and generated
plan previews. It does not stream individual provider tokens or pretend that a
node-by-node animation is live generation. Previews never autosave or execute.

After a build, manual edits autosave after 1.5 seconds. Pending changes flush
before AI messages, execution, publishing and scheduling. Revision conflicts keep
local changes and offer Keep my changes or Use latest version. Undo/redo and
copy/paste operate on plan edits; references within copied groups are remapped.

The compiler plan is authoritative. Branch paths, joins, parallel groups, list
iterations, repeat-until rules, human questions, waits and linked workflows are
projected onto the canvas. Data wires represent bindings; layout coordinates do
not determine execution order. Layout has its own revision and cannot trigger a
planner request, doctor pass or run. An explicit Arrange action is available.

The doctor still runs automatically on technical diagnostics, with bounded
attempts and no repair button. Questions ask for missing user facts. New requests
and answers may run automatically when Run when ready is enabled; opening,
restoring, arranging and manually editing never submit a run.

The microphone uses push-to-talk: hold with a pointer, or hold Space/Enter while
the button has focus, then release to send one utterance. Releasing during startup
cancels; Escape or losing focus while holding cancels. After release the microphone
is muted while the final transcript finishes, then its connection closes. Failed
partials remain in the typed composer. Spoken replies can play after release.

Clarification cards identify the step, field and relevant context. Internal
`{{step.output}}` references are compiler wiring, never questions for the person.
Embedded references become explicit data-only fill steps; invalid references stay
blocked for automatic repair. Existing genuine fact answers retain their IDs.
When the email's purpose is unclear, an intent answer replans the content rather
than becoming literal outgoing copy. Replanning preserves recipients, conditions
and budgets. A missing report source blocks generation, and the Text tool emits
its configured template instead of returning an empty result.

## Saved workflows and API

Workflow IDs equal their assistant session IDs. Separate `automation_workflows`,
`automation_workflow_versions`, `automation_editor_layout` and
`automation_workflow_templates` tables keep editor persistence separate from the
legacy runtime and Temporal execution projections. Migration 13 adds these tables
and backfills workflow identities for existing assistant sessions.

- `/api/v1/assistant/workflows`: list/create workflow identities and their drafts.
- `/assistant/workflows/{id}`: workflow, session and independent layout envelope.
- `/assistant/workflows/{id}/versions`: list/publish immutable checked snapshots;
  `/restore` restores a published snapshot as an editable draft.
- `/assistant/workflows/{id}/layout`: read/update layout with its own revision.
- `/assistant/workflows/{id}/template` and `/assistant/workflow-templates`: save,
  list and reuse compiler templates. The 28 bundled templates have deterministic
  compiler equivalents; template use does not require a planner call.
- `/assistant/sessions/{id}/check`: check a proposed plan without persisting it;
  `/build` exposes authenticated, request-fenced build progress.

All paths above use the `/api/v1` prefix. Workspace membership protects every
resource. Catalog fields include real account, file and Knowledge choices plus
control kinds, comparison options and supported conversions. Missing resources
cannot remain available through a stale choice list.

Publish freezes a checked plan with answered values materialized. Restoring or
copying does not mutate published versions. Child workflows can reference an
immutable published version using `version:<uuid>`; existing bare run references
remain supported. Temporal still owns all v2 execution and unsafe action handling.

## Local reset and verification

The approved reset is a separate explicit maintenance command, documented in
[WORKFLOW_RESET.md](WORKFLOW_RESET.md). It clears workflow state while retaining
accounts, credentials, connected apps, WhatsApp, Knowledge and files. Setup and
migrations never invoke it.

Browser coverage includes the empty canvas and bottom composer, conversation
collapse, autosave, layout-only revisions, publish/restore, template round trips,
AI build checkpoints and edit locks. Existing editor, voice, account and doctor
flows remain regression coverage. Use `make check`, `make test-e2e`, and
`make build`; persistence/runtime changes also require disposable-database race
coverage. See the development guide for isolation and process restrictions.

Verified for this checkpoint: Go vet and the full service-independent Go suite;
real isolated-database editor race integration plus focused service/orchestration
race tests; reset race tests including isolated real Temporal teardown; all 28
native bundled-template conversions; TypeScript and ESLint; 176 frontend unit
tests and 15 browser flows. A follow-up hook run passed all 10 cases, including
removed draft/run bookmark recovery. Production Go/Next.js builds passed.

The authorized local reset completed with a fresh private backup. Every protected
record hash was unchanged and workflow state was empty afterward. Backup and
verification artifacts remain under ignored `.local/`; they are never source files.
