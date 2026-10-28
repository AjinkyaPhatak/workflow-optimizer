# Voice workflows

The authenticated landing is `/home`: type or speak a request, refine the
generated workflow, answer missing-value cards, and watch a durable run. “Run when
ready” executes explicit requests after validation. Corrections to an existing
run produce a draft for review. Stop cannot undo an already completed action.

## Setup and processes

`make setup` preserves existing secrets/data, upgrades PostgreSQL 16 to pgvector,
starts project-isolated Temporal and embeddings, builds the isolated Python
analysis image, and migrates the database. CUDA is selected when an NVIDIA
device and container runtime are available; otherwise CPU is used. The pinned
model downloads on its first embedding request and remains in a Docker volume.

`make dev` runs API, legacy worker, Temporal orchestrator, WhatsApp connector,
and Next.js. Individual targets: `make api`, `make worker`, `make orchestrator`,
`make connector`, `make frontend`.

| Local service | Address |
| --- | --- |
| Frontend | http://localhost:3000/home |
| API | http://127.0.0.1:18080 |
| PostgreSQL / Redis | 55432 / 56379 |
| Temporal / development UI | 57233 / http://localhost:58233 |
| Local embeddings | http://127.0.0.1:58081 |
| Internal WhatsApp connector | http://127.0.0.1:58082 |

Configure `OPENAI_API_KEY`, or choose an encrypted workspace OpenAI credential
when starting a conversation. `PLANNER_MODEL` defaults to `gpt-6-luna` and must
support Responses structured outputs/web search. Decisions uses `gpt-6-luna`
for fixed-choice classification and asks a person when uncertain.
Speech needs `ELEVENLABS_API_KEY` and `ELEVENLABS_VOICE_ID`.

Setup generates `COMPUTE_SERVICE_TOKEN`, `CONNECTOR_SERVICE_TOKEN`, and
`AUTOMATION_WEBHOOK_SECRET`. Keep these and the existing encryption/signing keys
private. Pair personal WhatsApp at `/settings/whatsapp`; this uses whatsmeow's
linked-device protocol. Session databases live in memory, with synchronous
encrypted snapshots in PostgreSQL after committed writes.

The bundled Temporal SQLite server is for local development. Production should
use a separately managed Temporal deployment. The orchestrator needs Docker
access to launch the fixed analysis image. Generated Python has no network,
credentials, host mounts, or writable root filesystem.

## Execution and knowledge

V1 published definitions and in-flight runs retain the legacy runtime.
V2 uses separate automation tables and Temporal task queues. Saved run plans
are immutable; assistant edits create new revisions. The old editor can create
an editable voice-builder copy without modifying its source.

The checker validates tools, inputs, types, references, control flow and budgets.
Its explicit conversion registry inserts visible steps. Eino bounds model
repair to three passes. Missing values/placeholders become typed cards.
Answers use revision checks; template substitutions preserve surrounding text.

Comparison names come from one shared contract exposed to the editor and the
planner's strict schema. Equivalent spellings such as `gt` and `>` normalize to
`greater` in a separate working copy. Old drafts are rechecked when opened without
writing a revision or executing actions. Unknown rules remain blocked rather than
being guessed. Repeated identical repair failures stop early.

The assistant automatically calls the workflow doctor when a draft has technical
diagnostics, after a message, answer, saved edit or reopening a draft. It shows
checking/repair progress instead of technical findings or a repair button,
and leaves missing user facts to clarification cards. Decision and repeat rules
have ordinary-English selectors populated from the backend contract. Repairs use
`POST /assistant/sessions/{id}/repair` with an optimistic revision, preserve answers,
and reject additions/removals of outgoing actions. The model's summary cannot
override the checker's result. Each draft revision gets one automatic doctor request
with up to three server-side repair/check passes; an exhausted attempt is remembered
in the tab across reloads. New user input or a saved edit allows a new check.
Repairing a reopened or manually edited draft never submits a run. A successful
repair of a newly submitted request or answer continues only when **Run when ready**
is still enabled. Starting a new conversation ignores late repair responses.

Control flow includes conditions, parallel joins, maps, repeat-until limits,
immutable child plans, delays and event waits. Continuous mode repeats bounded
cycles with a one-minute cooldown and Continue-As-New. New workflows use
automatic execution without budget questions or cumulative action/token/time
pauses. Explicit custom caps remain available under Execution settings. Stop
and Pause remain available, and individual tool requests retain bounded timeouts.

`web.query` (Fetch current data) uses OpenAI live web search and a generic
structured-record schema. The workflow supplies the query and desired columns;
the tool returns rows, source URLs, notes and actual retrieval time. It tries an
alternative search once when sources are empty. Missing web data returns empty
rows and notes rather than asking the user to paste a CSV. `observed_at` is
retrieval time from the application's clock; any source publication or quote
time is a separate field. Missing source timestamps do not block ordinary
recurring checks. Numeric comparisons accept plain numeric text from fetched
records; currency symbols and missing or nonfinite values are rejected.

Schedules use five-field cron, IANA timezones (initially Asia/Kolkata), and skip
overlaps. Event triggers require a source: knowledge base ID, conversation ID,
or account-user/contact pair. Incoming WhatsApp messages only enter the outbox
when an enabled source-specific automation subscribes. Event lineage prevents
recursive chains. The outbox retries dispatch after outages.

At `/automations`, each schedule or trigger shows every active root execution
and its latest 20 finished runs, with links to `/home?run=…` for results,
questions and budget renewal. Status refreshes every five seconds. **Pause
future runs** disables new starts; **Pause run** and **Stop run** control only
the selected execution. To stop an automation completely, pause future runs
and stop its active executions. Deletion is disabled in the UI while runs are
active or their status is unavailable, keeping their controls reachable.
Schedules have no expiry; stopping a run does not disable its schedule or undo
completed actions. Run ownership uses the last event-trace entry, excluding
child workflows, so existing saved executions appear without a backfill.
Durable event and human-answer waits select on cancellation as well as signals;
stopping pending runs also records their terminal status before loading finishes.

Unsafe actions claim a durable operation before dispatch. Uncertain delivery
produces a reconciliation card and never blindly resends. Provider receipts
remain in the action ledger. Deliberate new runs have new request IDs.

WhatsApp resolves names against the connected account's contacts. Exact IDs and
names take priority; normalization handles punctuation, case and accents, and
minor spelling differences use the clear closest match. Near ties require a
contact choice before an action is claimed. Phone numbers and contact addresses
are matched exactly and never autocorrected to a nearby number.

Knowledge defaults to normalized 384-dimensional multilingual-e5-small,
revision `614241f622f53c4eeff9890bdc4f31cfecc418b3`. Retrieval combines cosine
similarity and keywords within a workspace/base. Hosted bases keep their
original models. “Copy to local search” stages a separate index and preserves
the original. Generated outputs are indexed only when explicitly added.

Public URL ingestion retains source URL, fetch time and content hash. Web
research retains citations. Source material cannot authorize tools. Market
requests identify their universe and trading session; missing figures must
not be synthesized. No paid market-data connector is required.

## API and validation

`/api/v1/assistant` manages sessions, messages, plans, answers, imports and runs.
`/automation-runs` provides authenticated SSE, input/event waits and controls.
`/automations` manages schedules/triggers; `/voice` handles short-lived tokens
and spoken replies; `/whatsapp` manages the authenticated user's connection.

Webhooks POST `{"event_id":"unique-id","data":{...}}` to
`/api/v1/triggers/{automation_id}`. Set `X-Workflow-Timestamp` to Unix seconds
and `X-Workflow-Signature` to hex HMAC-SHA256 of
`timestamp + "." + exact_request_body` using `AUTOMATION_WEBHOOK_SECRET`.
Requests expire after five minutes; event IDs deduplicate retries.

Run `make check`, `make test-race`, `make test-e2e`, and `make build`.
The browser fixture uses an isolated Temporal queue and mocked OpenAI/WhatsApp;
CSV/statistical analysis executes in the real isolated container. Real paid
providers are never called by automated tests. Keep browser/build work separate
from the legacy short-lease race tests.

## Verified locally

The full Go integration/race suite passed, including the earlier concurrency
stress case. Go vet and production Go/Next.js builds passed. Following the
connected-account, proxy timeout and workflow repair fixes, 155 frontend unit tests
and all eight browser flows passed. The browser flows also check Google setup discovery, existing
settings navigation, the Files redirect, and schedule
create/pause/resume/delete and real CPU knowledge indexing/retrieval.
The CPU embedder returned normalized 384-dimensional vectors.

Voice testing exposed Next.js closing workflow-planning requests at its default
30-second proxy timeout. The proxy now allows five minutes, beyond the API's
four-minute planning write deadline. A browser regression deliberately delays
the mocked planner by 32 seconds and verifies a successful response and usable
workflow without executing its actions.

The workflow-repair browser flow checks comparison normalization, step-specific
feedback, preserving answers across a model repair, readable condition editing,
stale-revision rejection and cross-workspace isolation. Temporal tests exercise
the average threshold: 200 dispatches, while 150 and 100 do not. A manual live
OpenAI smoke check passed the constrained schema and numeric branch, and the
previously blocked demo-sales draft revalidated with no diagnostics while keeping
its revision. No real email was sent during these checks.

The supplied OpenAI key passed live model discovery, strict workflow compilation,
local execution of the resulting plan, and web search. A regression test covers
an exported-but-empty endpoint setting overriding the SDK default.

Automated provider/WhatsApp tests use mocks. Live ElevenLabs realtime token
creation and speech generation passed with the built-in Sarah voice; the
previous library voice was rejected on the free plan. Physical microphone use,
a real paired WhatsApp account, and CUDA execution still need live verification.

## Workspace/input incident and recovery (2026-10-27)

The original `TestSchemaRelationshipsConstraintsJSONBAndHistory` reset the
`public` schema. The integration/race Make targets previously passed the
development database as `TEST_DATABASE_URL`, so running that test removed local
accounts, drafts and connected accounts. Earlier statements that every test used
an isolated schema were incorrect. The test now uses the existing isolated-schema
helper; integration, race and browser Make targets additionally create a separate
disposable database, including when `TEST_DATABASE_URL` is explicitly supplied.

The browser retained a signed token for the removed account. `/auth/me` returned
404, leaving the assistant without a workspace. Send stayed disabled, Enter
discarded the input, and voice sent an empty workspace ID and replaced the resulting
400 with misleading microphone/ElevenLabs advice. A missing account now returns
401 so the normal sign-in recovery runs. Workspace failures offer retry next to
the composer, and text remains editable and preserved on failed submissions.
Tool-catalog loading no longer blocks workspace discovery.

Voice startup waits for the actual microphone/connection readiness, guards duplicate
starts, reports permission/provider failures separately and allows retry. Callbacks
read the current conversation and question so later transcripts continue the
same draft. These browser regressions use the real ElevenLabs client with a fake
microphone and mocked WebSocket/token endpoints, never a paid transcription call.

Local recovery reconstructed the original user (including its password hash),
workspace, encrypted Google credential/connection and three assistant drafts from
retained PostgreSQL WAL. The credential decrypted with the existing key; the
demo-sales draft revalidated without changing its revision. Live profile,
connected-account and ElevenLabs token endpoints returned 200. Recovery evidence
and a restored-database dump are retained under ignored `.local/recovery/` with
restricted access. This was targeted recovery, not a verified full database restore.

Validation for this fix: the repository and phase12 API integration suites passed
with `-race` in a disposable database, along with application checks. All 155
frontend unit tests, TypeScript and ESLint passed. Four additional browser flows
passed: workspace/send retry with preserved text, consecutive spoken turns in one
session plus quota failure, invalid-session sign-in recovery, and microphone
permission denial with continued typing. The recovered development account,
three drafts and Google connection remained present after these test commands.

Automatic doctor validation: all 161 frontend unit tests, TypeScript and ESLint
passed. Six hook cases cover Strict Mode deduplication, unanswered facts, continuing
an authorized request after repair, stopping exhausted attempts across remounts,
ignoring late responses and turning auto-run off during repair. Five browser flows
passed, including automatic repair without a button, preserved answers, no run on
reopen, exhausted-attempt suppression after reload and the four input/voice recovery
flows. Application and phase12 API tests passed with `-race` in a disposable database.
