# Workflow Optimizer

Architectural skeleton for a Go modular-monolith AI workflow orchestration platform.

The repository currently establishes package ownership, contracts, configuration, and local infrastructure boundaries only. It deliberately contains no workflow execution, persistence schema, queue worker behavior, authentication logic, or provider calls.

## Entry points

- `cmd/api`: REST API process (Phase 12); it persists and enqueues executions but never runs them.
- `cmd/worker`: long-running execution process; it consumes execution jobs from Redis and runs them (Phase 9).

The intended future flow is API → queue → worker → executor.

## Worker (Phase 9)

`go run ./cmd/worker` starts `WORKER_COUNT` workers that consume jobs from the Redis list `REDIS_QUEUE_NAME` (default `workflow:executions`, filled with LPUSH, consumed with BRPOP). A job carries only an execution ID. Redis says which execution to try; PostgreSQL decides who owns it: each worker claims the execution with the atomic PENDING → RUNNING transition, and a worker that loses the claim, or finds the execution not PENDING, does nothing.

Configuration: `DATABASE_URL`, `REDIS_URL` (required), `REDIS_QUEUE_NAME`, `WORKER_COUNT`, `WORKER_SHUTDOWN_TIMEOUT` (for example `30s`). On SIGINT/SIGTERM the worker stops taking jobs, cancels in-flight executions (recorded as CANCELLED), and exits within the shutdown timeout.

Delivery is at-least-once at best and can lose a job: BRPOP removes a job before it is processed, so a worker process that dies between BRPOP and the claim leaves the execution PENDING until it is dispatched again. A worker process that dies after the claim leaves the execution RUNNING. If the worker is still alive but cannot load the execution or apply the claim (for example a transient PostgreSQL error), it puts the job back on the queue and pauses before its next job; this is safe because the claim prevents a second execution. A panic inside a node fails that execution (FAILED) instead of crashing the worker. Phase 9 itself had no workflow retry, dead-letter handling or stuck-execution recovery; Phase 10 adds them (below).

## Retry, timeout and reliability (Phase 10)

PostgreSQL stays the source of truth; Redis still carries only execution IDs.

- **Classification.** Every failure is an `ExecutionError` with `retryable` and `source` (`node`, `provider`, `worker`, `database`, `queue`, `system`) decided where the error is created (for example HTTP 429/408/5xx are retryable, 401/403 and other 4xx are not). Nodes that declare `SideEffects: unsafe` (the HTTP node) are never retried after a retryable failure unless the provider reported the request was not applied (429).
- **Execution retries are scheduled, never slept.** A failed attempt with attempts and time left moves RUNNING → PENDING with `next_attempt_at` (exponential backoff with jitter, capped, `Retry-After` honoured) and releases its worker. The retry scheduler (every `SCHEDULER_INTERVAL`) claims due retries atomically (`FOR UPDATE SKIP LOCKED`, safe with many schedulers) and enqueues their IDs. The database refuses a claim before `next_attempt_at`, beyond `max_attempts`, and a retry after a cancellation request or past the deadline.
- **Attempts** are counted by the database on each claim; completed nodes are not re-run on a retry (their outputs are reused). Nodes get an idempotency key (`<execution id>/<node id>`) that is stable across attempts.
- **Dead letters.** An execution whose retryable failure cannot be retried any more (attempts exhausted, deadline) is FAILED and gets a row in `execution_dead_letters` in the same transaction (authoritative). A notice (IDs and codes only) is also pushed to the Redis list `REDIS_DEAD_LETTER_QUEUE`; it is informational and never consumed as work.
- **Timeouts.** `DEFAULT_EXECUTION_TIMEOUT` sets `deadline_at` at the first claim; it never moves and a timed-out execution is FAILED (`EXECUTION_TIMEOUT`), never retried. `DEFAULT_NODE_TIMEOUT` bounds one node invocation (`NODE_TIMEOUT`, retryable). A failing node may be retried in place up to `DEFAULT_NODE_MAX_ATTEMPTS` invocations when the pause is short (≤ 5s); otherwise it becomes an execution-level retry.
- **Leases and recovery.** A claimed attempt holds a lease (`WORKER_LEASE_DURATION`) renewed every `WORKER_HEARTBEAT_INTERVAL`. A worker that cannot renew stops its attempt before the lease can expire. The reaper (every `REAPER_INTERVAL`) recovers attempts whose lease expired (retryable `WORKER_LOST`: retried or dead-lettered) or that overran the deadline (FAILED `EXECUTION_TIMEOUT`), in one transaction fenced by the attempt's claim, so a heartbeating worker is never reaped and a stale worker can no longer write anything. Node records are fenced by the claim token that wrote them (migration 000004): a worker whose attempt was recovered gets `ErrLeaseLost` on its next node write, even if the execution is RUNNING again under a newer claim. Each node record also stores its node's side-effect declaration; an attempt lost while a node with unsafe (or unknown) side effects was running is not retried but FAILED with a `retry_unsafe` dead letter, because that node may already have applied its effect.
- **Cancellation beats retry.** A cancellation request (`execution_cancel_requests`) stops a running attempt at its next heartbeat and cancels a waiting retry instead of running it.

Configuration (defaults): `DEFAULT_MAX_ATTEMPTS` (3, includes the first attempt), `DEFAULT_INITIAL_RETRY_DELAY` (1s), `DEFAULT_MAX_RETRY_DELAY` (5m), `DEFAULT_BACKOFF_MULTIPLIER` (2), `DEFAULT_EXECUTION_TIMEOUT` (1h), `DEFAULT_NODE_TIMEOUT` (5m), `DEFAULT_NODE_MAX_ATTEMPTS` (2), `REAPER_INTERVAL` (15s), `WORKER_LEASE_DURATION` (30s, at least twice the heartbeat), `WORKER_HEARTBEAT_INTERVAL` (10s), `SCHEDULER_INTERVAL` (1s), `REDIS_DEAD_LETTER_QUEUE` (`workflow:dead-letter`).

Limits: worker shutdown still cancels in-flight executions (Phase 9 behaviour). A node that ignores cancellation keeps its worker busy until it returns, but its execution is still failed at the deadline by the reaper and its late writes are rejected. (The HTTP node became a real transport in Phase C7.)

## Credentials and providers (Phase 11)

LLM nodes call real providers through two registries and a credential boundary:

```
LLM node -> provider registry ("openai") -> OpenAI provider (Chat Completions)
         -> credential resolver -> credential.Service -> AES-256-GCM -> credentials table
```

- **Credentials** belong to a workspace and are stored encrypted (`credentials.encrypted_data` holds an AES-256-GCM envelope: nonce, ciphertext and tag). The master key is `CREDENTIAL_ENCRYPTION_KEY` (32 random bytes, base64, e.g. `openssl rand -base64 32`; never commit it). Listing and reading credentials never returns plaintext; only resolution for an execution decrypts, into memory, and the secret type redacts itself when printed or marshalled.
- **Workflows reference credentials by `credential_id`** in the LLM node config (`{"provider": "openai", "credential_id": "<uuid>", "model": "gpt-5", "temperature": 0.7}`); secrets in node config are rejected. A run resolves credentials only inside its workflow's workspace: another workspace's credential is `CREDENTIAL_NOT_FOUND`.
- **Errors** are normalized to platform codes that the Phase 10 engine retries or not: `RATE_LIMITED` (429, with Retry-After), `PROVIDER_SERVER_ERROR` (500/502), `PROVIDER_UNAVAILABLE` (503, network), `PROVIDER_TIMEOUT` (408/504, client timeout) are retryable; `AUTHENTICATION_FAILED`, `INVALID_PROVIDER_REQUEST`, `MODEL_NOT_SUPPORTED`, and the credential errors `CREDENTIAL_NOT_FOUND`, `CREDENTIAL_DECRYPTION_FAILED`, `CREDENTIAL_PROVIDER_MISMATCH`, `CREDENTIAL_INVALID` are not. Provider response bodies (which can echo a key) are never copied into errors.
- `OPENAI_BASE_URL` optionally overrides the OpenAI endpoint. Provider keys are never read from the environment.

## REST API (Phase 12)

`go run ./cmd/api` serves `/api/v1` on `API_ADDR` (default `:8080`). It needs `DATABASE_URL`, `REDIS_URL`, `AUTH_TOKEN_SECRET` (at least 32 bytes) and, to store credentials, `CREDENTIAL_ENCRYPTION_KEY`; `AUTH_TOKEN_TTL` defaults to 24h. Apply the migrations first (`000005` adds workflow soft delete, `000007` connected accounts, `000008` files, `000009` knowledge bases); the processes do not migrate.

```
HTTP -> handler -> application service -> repositories / queue.Submitter -> PostgreSQL, Redis -> worker -> graph executor
```

- **Authentication.** `POST /auth/register` (creates the user and a workspace they own) and `POST /auth/login` return an HS256 bearer token (`sub`, `iat`, `exp` only). Every other `/api/v1` route needs `Authorization: Bearer <token>`.
- **Authorization.** Workspace membership is the tenant boundary. A resource in a workspace the caller does not belong to is reported as not found (`WORKFLOW_NOT_FOUND`, …), so UUIDs cannot be probed. Roles: owner and admin may do everything; member reads, creates and edits workflows and versions, and executes; viewer only reads. Publishing, deleting workflows, creating projects and managing credentials need owner or admin.
- **Routes.** `GET /auth/me`; `POST|GET /projects`, `GET /projects/{id}`; `POST|GET /workflows` (`?project_id=&page=&page_size=`, max 100), `GET|PATCH|DELETE /workflows/{id}` (delete hides the workflow, history is kept); `POST|GET /workflows/{id}/versions`, `GET .../versions/{vid}`, `POST .../versions/{vid}/validate`, `POST .../versions/{vid}/publish`; `POST /workflows/{id}/execute` (202 `{execution_id, status: PENDING}`; `version_id` defaults to the active version); `GET /executions/{id}`, `GET /executions/{id}/nodes`, `POST /executions/{id}/cancel`; `POST|GET /credentials` (`?workspace_id=`), `DELETE /credentials/{id}`; `GET /nodes` (from the node registry). `GET /health` and `GET /ready` (PostgreSQL and Redis) are outside `/api/v1`.
- **Versions** are validated by the graph validator: in draft mode when created (a draft may be incomplete) and in executable mode when validated, published and executed. Versions have no update route; published versions are immutable in the database.
- **Errors** always have the shape `{"error": {"code", "message", "details"}}`; internal errors are logged with the request ID and answered with a generic `INTERNAL_ERROR`.
- **Credentials** are created through the Phase 11 credential service (encrypted before storage). Responses carry metadata only, never the secret.

## Observability (Phase 14)

Observability describes execution and never controls it. The execution row, its status history and `node_executions` stay the source of truth; on top of them:

- **Execution events** (`execution_events`, migration 000006, append-only: updates and deletes are refused by the database): `EXECUTION_STARTED|COMPLETED|FAILED|CANCELLED`, `NODE_STARTED|COMPLETED|FAILED|SKIPPED`, `RETRY_SCHEDULED|STARTED` (with a node ID: an in-place node retry; without: a new execution attempt; `NODE_SKIPPED`: a node reused from an earlier attempt). The Runner, the node recorder, the API's cancellation and the reaper report each fact to an `execution.ExecutionObserver` only after the corresponding state change is durable. Events hold IDs, codes, counters and durations only (never node payloads, provider requests or credentials) and are ordered by the database clock.
- **Failure policy**: if an event cannot be stored, the failure is logged (`execution_event_persist_failed`) and counted, the event is dropped, and the execution continues unchanged. The event history may have gaps; execution and node state cannot.
- **API**: `GET /api/v1/executions/{id}` (adds `duration_ms`, `node_count`, `retries`, aggregate `usage` and `estimated_cost_usd`, derived from node records on read), `GET /executions/{id}/nodes` (per record: status, timing, attempt, structured error, provider/model/token usage taken from what the LLM node recorded, estimated cost, and redacted input/output), `GET /executions/{id}/events?page=&page_size=` and `GET /workflows/{id}/executions`. All are authenticated and workspace-scoped (other workspaces get 404).
- **Redaction**: credential-like fields (`api_key`, `authorization`, `password`, `token`, ...) and bearer/API-key-shaped strings are replaced with `[REDACTED]`; long strings are truncated. `OBSERVABILITY_EXPOSE_NODE_DATA=false` hides node inputs/outputs entirely.
- **Cost** is an estimate from `MODEL_PRICING` (USD per million tokens, matched by model name or prefix); without a configured price there is no estimate.
- **Logs and metrics**: one structured log line per event (`event`, `execution_id`, `node_id`, `node_type`, `attempt`, `error_code`); API logs carry `request_id` (from `X-Request-ID`, generated when absent) and `execution_submitted` links a request to its execution. A minimal in-process metrics registry counts executions by outcome, node failures, provider errors, retries and event-store failures, and records execution/node durations and queue latency; the worker logs a snapshot at shutdown (no exporter yet).
- **Frontend**: `/executions/{id}` is the execution debugger (timeline, node inspector, JSON viewers, event log, status graph, polling while PENDING/RUNNING); `/workflows/{id}/executions` lists a workflow's executions.

## Workflow capabilities (Phase B)

- **Config field metadata**: node configuration fields can carry a `label`, `options` (with optional `when` conditions and `allow_custom`), `min`/`max`/`step` and `multiline`; `GET /api/v1/nodes` returns them and the editor renders dropdowns, bounded number inputs and text areas from them. The graph validator rejects values outside strict options or ranges (`{{references}}` are checked at run time).
- **LLM**: provider and model options come from the provider registry (`providerllm.Describe`; OpenAI lists its models, any other model ID can still be entered). New `system` and `prompt` settings (System prompt / User prompt) are used when the corresponding input port is not connected; the node stays provider-neutral.
- **Prompt Template** (`prompt`): emits its template after the execution engine has filled in `{{references}}` (no second resolver).
- **Structured Output** (`structured_output`): parses JSON out of text (such as an LLM response; fenced blocks and surrounding prose are tolerated) and checks it against a simple schema: `"string" | "number" | "boolean" | "object" | "array" | "any"`, nested objects, `["type"]` lists, `"type?"` for optional fields. Fields outside the schema are dropped. It calls no model.
- **Transform**: `path` (Field path) picks a value out of its input; `mapping` (Output mapping) builds a JSON value whose `{{references}}` the engine fills in. The old `expression` field is kept for existing definitions and is not applied.
- **Workflow variables**: the definition's optional `variables` list (`name`, `type`, `default`, `description`). Nodes reference them as `{{name}}`. When an execution is submitted, defaults are filled into the run input and supplied values are type-checked; a variable without a default must be supplied (400 otherwise). Definitions without variables serialize exactly as before, so the schema version is still 1.
- **Templates**: `GET /api/v1/templates` lists ready-made workflow definitions (`internal/templates/catalog/*.json`); `POST /api/v1/workflows` with `template_id` creates the workflow and its first DRAFT version from a copy with fresh node and edge IDs.
- **Versions**: unchanged model (every save is a new immutable DRAFT; publishing makes a version active). The editor shows the history and can open any version as the working copy.

## Integration framework (Phase C1)

Third-party integrations are ordinary workflow nodes; the executor knows
nothing about them (`internal/integration`).

- **Naming**: an action's node type is `<integration>.<action>`, both lower
  snake case (`gmail.send`, `google_docs.append`). Built-in types have no dot.
- **Metadata** (`integration.Integration`, `Action`): name, category, icon,
  docs link, auth (`Provider` + `CredentialType`, e.g. `google` + `OAUTH2`),
  and per action its ports, config and side effects (`none`, `idempotent`,
  `unsafe`). `Integration.Definition` turns an action into a normal
  `node.NodeDefinition` carrying `integration` and `auth`, which
  `GET /api/v1/nodes` returns. No new endpoint, table or env var.
- **Registries**: `integration.Registry` is discovery metadata only; the node
  registry stays the only executable registry. `integration.Install`
  registers a `Module` (metadata + one node per action) into both.
  `app.Dependencies.Integrations` is the install point; production installs
  none yet.
- **Credentials**: workflows store only `credential_id`.
  `integration.ActionNode` resolves it through `credential.Resolver`
  (workspace, provider and credential type checked), hands the
  `ResolvedCredential` to a provider-specific `Connector`, and keeps nothing.
  Integration configs can never declare secret fields (`api_key`,
  `access_token`, ...); the validator rejects unknown config keys.
- **Errors**: clients return `integration.Error` with a `Kind`
  (`AUTHENTICATION_FAILED`, `PERMISSION_DENIED`, `RATE_LIMITED`,
  `RESOURCE_NOT_FOUND`, `INVALID_REQUEST`, `PROVIDER_UNAVAILABLE`, `TIMEOUT`,
  `MALFORMED_RESPONSE`, `INTEGRATION_ERROR`); `FromHTTPStatus` maps status
  codes. Only rate limits, unavailability and timeouts are retryable, and the
  Phase 10 engine still refuses to repeat an `unsafe` action unless the
  failure was not applied (429). `Error()` never prints the wrapped cause.
- **Observability**: node events of integration actions carry `integration`
  and `action`; the error code is the normalized kind.
- **Fake integration**: `internal/integration/fake` (`test_integration.read`,
  `test_integration.write`) is test-only and never registered in production.

## Connected accounts and OAuth (Phase C2)

Provider-neutral OAuth 2.0 (authorization code + PKCE) and connected accounts.
No provider is configured by default: `GET /api/v1/oauth/providers` is empty
and the Connected accounts page says so. Providers are `oauth.Provider`
implementations registered through `app.Extensions` (tests use
`internal/oauth/fake`).

- **Connect**: `POST /api/v1/connected-accounts/{provider}/authorize`
  (`{workspace_id}`, owners/admins) stores a single-use state in Redis (10
  minutes, keyed by its SHA-256, holding workspace, user, provider, PKCE
  verifier and the hash of a browser binding), sets the binding as an
  HttpOnly `SameSite=Lax` cookie scoped to the callback path, and returns
  the provider URL. The provider redirects to the public
  `GET /api/v1/oauth/callback/{provider}`, which takes the state atomically,
  checks provider, expiry and browser binding, re-checks that the user may
  still manage credentials in the workspace, exchanges the code, and
  redirects to `/settings/connected-accounts?connected=...` or `?error=<kind>`.
  The callback must reach the API through the frontend origin (the Next.js
  `/api/v1` proxy) so the relative redirect lands on the frontend.
- **Storage**: tokens (access, refresh, expiry, scopes, account ID) are
  encrypted inside the existing credential envelope (AES-256-GCM) of an
  `OAUTH2` credential. `connected_accounts` (migration `000007`) holds
  metadata only and references that credential; a composite foreign key
  keeps both in one workspace. Workflows reference `credential_id`, exactly
  like API keys.
- **Run time**: `oauth.TokenManager` is every node's `credential.Resolver`.
  It returns a valid access token, refreshing it (once per credential per
  process, concurrent resolutions wait) when it expires within 2 minutes,
  keeping the refresh token when the provider omits one, and storing the
  result encrypted. A revoked grant removes the tokens and marks the account
  `REVOKED` (`CREDENTIAL_REVOKED`, not retried); a provider outage is
  retryable.
- **Disconnect**: `DELETE /api/v1/connected-accounts/{id}` revokes at the
  provider (best effort), removes the tokens and marks the account
  `DISCONNECTED`; workflows using it fail with `CREDENTIAL_REVOKED`.
  Connecting the same external account again (`provider_account_id`)
  reactivates the same account and credential.
- **Saving workflows** now rejects a `credential_id` that is not a credential
  of the workflow's workspace.

## Gmail (Phase C3)

Gmail is the first real integration: five nodes on the C1 framework that use
Google connected accounts (C2). Nodes never see OAuth: the token manager
hands the Gmail client a valid access token (refreshing it when needed).

| Node | Side effects | Output |
|---|---|---|
| `gmail.search` | none | `messages` [{`message_id`, `thread_id`, `sender`, `subject`, `snippet`, `timestamp`, `unread`}], `count`, and the newest match's `message_id` / `thread_id` (empty when nothing matched) |
| `gmail.read` | none | `message_id`, `thread_id`, `sender`, `recipients` [], `subject`, `body` (plain text; HTML-only mail converted), `timestamp` (RFC 3339), `attachments` [{`filename`, `mime_type`, `size`, `attachment_id`}] |
| `gmail.create_draft` | unsafe | `draft_id`, `message_id`, `status` = `draft` |
| `gmail.send` | unsafe | `message_id`, `thread_id`, `status` = `sent` |
| `gmail.reply` | unsafe | `message_id`, `thread_id`, `status` = `sent` (same thread, to Reply-To/From, `Re:` subject, In-Reply-To/References) |

**Choosing a message** (C3.1): Read Email and Reply act on the `message_id`
input (connect Search Emails' `message_id` output to it), else the **Message
ID** setting, else **Find message**: a Gmail query whose newest match is used
(e.g. `from:boss@example.com is:unread`), so a workflow never needs a copied
ID. An empty connected ID (the search found nothing) or a query without a
match fails with `RESOURCE_NOT_FOUND`.

Gmail has no idempotency key, so the three writing actions are `unsafe`: the
Phase 10 engine never repeats them after a failure that may have applied
(5xx, timeout), only after a refusal (429 / rate-limit 403). Errors map to the
integration kinds: 401 `AUTHENTICATION_FAILED`, 403 `PERMISSION_DENIED`, 404
`RESOURCE_NOT_FOUND`, other 4xx `INVALID_REQUEST`, 429 `RATE_LIMITED`
(Retry-After kept), 5xx `PROVIDER_UNAVAILABLE`, timeouts `TIMEOUT`,
unreadable answers `MALFORMED_RESPONSE`. A Google `invalid_grant` on refresh
marks the account REVOKED and fails with `CREDENTIAL_REVOKED` (not retried).

**Scopes** (minimum; C4 adds two, see below): `openid` and `email` (the account's stable ID `sub`,
used as `provider_account_id`, and its address as the label);
`gmail.readonly` (search, read); `gmail.compose` (drafts, send, reply). Not
requested: full mailbox access (`https://mail.google.com/`) or
`gmail.modify`. Authorization asks for offline access with PKCE.

**Configuration**: `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`,
`GOOGLE_OAUTH_REDIRECT_URI` (all or none; see `.env.example`), read by both the
API and the worker (the worker refreshes tokens). Without them Google is not
offered; the Gmail nodes stay in the catalog and ask for a Google account.

## Google Sheets and Google Calendar (Phase C4)

Two more integrations on the same Google connected account (one connection
covers Gmail, Sheets and Calendar). They share the Google REST plumbing in
`internal/integration/googleapi` with Gmail.

| Node | Side effects | Output |
|---|---|---|
| `google_sheets.read_rows` | none | `rows` [{`<column>`: text, ..., `row_number`}], `count`, `headers` [], `first_row` {} |
| `google_sheets.append_row` | unsafe | `row_number`, `updated_range` |
| `google_sheets.update_row` | idempotent | `row_number`, `updated_range`, `row` {} (the row after the update) |
| `google_calendar.list_events` | none | `events` [{`event_id`, `summary`, `description`, `location`, `start`, `end`, `all_day`, `attendees` [], `organizer`, `html_link`, `meet_link`, `status`}], `count`, `event_id` (earliest) |
| `google_calendar.create_event` | idempotent | `event_id`, `html_link`, `meet_link`, `start`, `end`, `status` |

**Sheets are tables**: row 1 holds the column names and rows are objects keyed
by them; Values are an object of column names (`{"Email": "{{read.sender}}"}`,
names matched ignoring case; an unknown column fails with
`INVALID_REQUEST` listing the sheet's columns). The spreadsheet setting takes
the browser link or the ID; an empty Sheet means the first tab. Update Row
finds its row by Row number or by the first row whose cell in a column equals
a value, and keeps the other cells. Value handling `USER_ENTERED` (default)
treats values like typing (numbers, dates, and text starting with `=` as a
formula); `RAW` stores exact text, safer for untrusted text such as email
content.

**Calendar**: List Events looks from now to Days ahead (repeating events
expanded, earliest first). Create Event takes Start as `2026-10-20 15:00`
with a Time zone (IANA, e.g. `Asia/Kolkata`), an RFC 3339 time with offset,
or a date for an all-day event; End defaults to Start + Duration (or one day).
Attendees are emailed only when Notify attendees is on. Its event ID is
derived from the node's idempotency key, so a retry after a failure that
applied finds the first attempt's event (Google answers 409) instead of
creating a second one.

**Connecting nodes**: each Sheets and Calendar action has one input that
accepts any upstream data (`json`), so it can follow any node and use that
node's outputs in `{{variables}}`. A settings value wins over the input
(Values) or the input's text wins (Calendar search, description), as noted per
port.

**Scopes** added for C4: `spreadsheets` (spreadsheets the account can open; no
other Drive files) and `calendar.events` (events, not calendar management).
Not requested: `drive` or `calendar`. An account connected before C4 lacks
them: its Sheets and Calendar calls fail with `PERMISSION_DENIED` asking to
reconnect the account. In Google Cloud, the Sheets API and the Calendar API
must be enabled and both scopes added to the consent screen's Data Access.

## Google Meet

Meet links come two ways, both on the same Google connected account:

- **Calendar Create Event → Add Google Meet link** puts a Meet call on a
  scheduled event (`meet_link` output; List Events also returns each event's
  `meet_link`). It needs no scope beyond `calendar.events`. The conference
  request ID is the event ID, so a retry asks for the same call.
- **`google_meet.create_meeting`** (unsafe) creates an instant meeting space
  through the Meet REST API: outputs `meeting_link`, `meeting_code` and
  `space`. **Who can join** sets the space's access type (`OPEN`: anyone with
  the link; `TRUSTED`: the organization and invitees; `RESTRICTED`: invitees
  only; empty: the account's default). It cannot deduplicate, so a failure
  that may have applied is not retried automatically.

**Scope** added: `meetings.space.created` (create spaces and manage only the
spaces this app created; no access to other meetings, recordings or
transcripts). Transcripts and recordings are not offered: they need
`meetings.space.readonly` and a paid Google Workspace plan. In Google Cloud,
enable the **Google Meet REST API**, add the scope to Data Access, and
reconnect the Google account; an account connected earlier fails Create
Meeting with `PERMISSION_DENIED` asking to reconnect.

## Google Docs and Google Forms (Phase J)

Both use the same Google connected account.

| Node | Side effects | Outputs |
|---|---|---|
| `google_docs.read_document` | none | `text`, `title`, `document_id`, `link` |
| `google_docs.create_document` | unsafe | `document_id`, `link`, `title` |
| `google_docs.append_text` (Add Text) | unsafe | `document_id`, `link` |
| `google_forms.read_responses` | none | `responses` [{`<question>`: answer, `submitted_at`, `email`, `response_id`}], `count`, `latest` {}, `questions` [] |

- **Document** takes the document's browser link or ID. Read Document returns
  the body as plain text (paragraphs, list items and table cells, one per
  line). Create Document makes a document in the account's Drive with an
  optional text; Add Text puts text at the end, on a new line. Neither write
  can be deduplicated by Google, so a failure that may have applied is not
  retried automatically.
- **Form** takes the link of the form's **edit** page (it ends with `/edit`)
  or its ID. The link people fill the form in at (`…/d/e/…/viewform`,
  `forms.gle`) does not name the form and is rejected with that hint.
  Responses are rows keyed by question title, newest first (grid rows are
  `Question [Row]`, several checkbox answers are joined with `, `, uploads
  are file names), up to **Max responses** (default 50, at most 1000).

**Scopes** added: `documents` (read and edit Google Docs the account can
open; no other Drive files), `forms.body.readonly` and
`forms.responses.readonly` (a form's questions and responses, read only). In
Google Cloud, enable the **Google Docs API** and the **Google Forms API**, add
the three scopes to Data Access, and reconnect the Google account; an account
connected earlier fails these nodes with `PERMISSION_DENIED` asking to
reconnect.

## Airtable (Phase J)

Airtable accounts connect with OAuth (an Airtable **OAuth integration**;
provider `airtable`, `internal/oauth/airtable`) with PKCE, which Airtable
requires. In Airtable's consent screen the user picks the bases to share.
Scopes: `data.records:read`, `data.records:write`, `schema.bases:read` (the
pickers) and `user.email:read` (the account's label); a connection missing
any of them fails. The account is the Airtable user ID. Refresh tokens
rotate; `invalid_grant` marks the account REVOKED. Airtable has no token
revocation: disconnecting removes the tokens here, and the grant can be
removed in Airtable's account settings.

| Node | Side effects | Outputs |
|---|---|---|
| `airtable.list_records` (Find Records) | none | `records` [{`<field>`: value, `record_id`}], `count`, `first_record` {}, `record_id` (first) |
| `airtable.create_record` (Add Record) | unsafe | `record_id`, `record` {} |
| `airtable.update_record` (Update Record) | idempotent | `record_id`, `record` {} |

- **Base** is picked from the shared bases, or a link to the base (or a table
  in it; the table then needs no setting). **Table** and the field names are
  picked from the base's schema.
- Find Records can keep only records whose **field** equals a **value** (as
  text, surrounding spaces ignored), only those of a **View**, and at most
  **Max records** (default 100, at most 1000). Airtable leaves empty fields
  out; every returned record gets every field seen, empty as `""`.
- **Fields** are written with `typecast`, so text is converted to the field's
  type (numbers, dates, new select options). When the setting is empty, an
  object connected to the input is written (a record's `record_id` is not).
- Update Record finds the record by **Record ID** or else by field and value
  (the first match).
- A base that exists but was not shared answers 403 at Airtable; it fails
  with `RESOURCE_NOT_FOUND` asking to reconnect Airtable and add the base.
  Rate limits (5 requests a second per base) wait 30 seconds.

Configuration: `AIRTABLE_CLIENT_ID`, `AIRTABLE_CLIENT_SECRET` and
`AIRTABLE_OAUTH_REDIRECT_URI` (all three or none; the redirect URI ends with
`/api/v1/oauth/callback/airtable`). Create the integration at
airtable.com/create/oauth, generate a client secret, register the redirect
URI and tick the four scopes.

## Notion (Phase C5)

Notion workspaces connect with OAuth like Google (a Notion **public
integration**; provider `notion`, `internal/oauth/notion`). In Notion's consent
screen the user picks which pages and databases to share; Notion has no
scopes. Notion does not support PKCE, so the flow's challenge is not sent (the
single-use state and browser binding still apply). The account is the
authorizing user in that workspace (`<workspace_id>/<user_id>`). Refresh
tokens rotate on every refresh; `invalid_grant` marks the account REVOKED.
Disconnecting revokes the access token at Notion.

The API client uses `Notion-Version: 2025-09-03`, where a database's table is
a **data source**: the Database setting takes a database link or ID (its first
data source is used) or a data source ID from Search.

| Node | Side effects | Output |
|---|---|---|
| `notion.search` | none | `results` [{`id`, `type` (page/database), `title`, `url`, `last_edited_time`}], `count`, `id` (first) |
| `notion.query_database` (Find Database Rows) | none | `rows` [{`page_id`, `url`, `created_time`, `last_edited_time`, `<property>`: value}], `count`, `page_id` (first), `first_row` |
| `notion.create_page` (Add Database Row) | unsafe | `page_id`, `url` |
| `notion.update_page` (Update Database Row) | idempotent | `page_id`, `url`, `row` |

**Rows are plain objects** keyed by property name. Reading: title and text as
text, number, checkbox as boolean, select/status as the option name,
multi-select/people/relations/files as lists, dates as their start, formulas
and rollups as their value. Writing (Properties, matched ignoring case):
text, numbers (or numeric text), checkboxes (true/false, yes/no), select and
status by option name, multi-select as a list or comma-separated text, dates
as `2026-10-20` or RFC 3339, relations and people as IDs; empty text clears a
value. Unknown or read-only properties (formula, rollup, created time...) fail
with `INVALID_REQUEST` before anything is written. Find Database Rows filters
on one property (equals, or contains for text) or takes an advanced Notion
filter object, and can sort by a property or created/edited time. Add Database
Row can also write text into the page (paragraphs split on blank lines).

Errors: 404 `RESOURCE_NOT_FOUND` says the object may not be shared with the
integration (••• → Connections in Notion); Notion's own error messages are
never copied into errors.

**Configuration**: `NOTION_CLIENT_ID`, `NOTION_CLIENT_SECRET`,
`NOTION_OAUTH_REDIRECT_URI` (all or none; the redirect URI ends with
`/api/v1/oauth/callback/notion`), read by both the API and the worker.

## Discord (Phase C6)

Discord works through **one bot** (the Discord application configured on this
server). Connecting Discord adds that bot to a server the user picks
(Discord's bot authorization with scopes `identify bot`, provider `discord`);
the connected account **is that server** (its ID is the provider account ID).
The bot asks only for View Channels, Send Messages, Embed Links and Read
Message History. Disconnecting revokes the user's authorization; the bot
stays in the server until someone removes it there.

Actions call Discord with the **bot token** (`DISCORD_BOT_TOKEN`), never the
user's token. Because every workspace shares the bot, each action first checks
that the channel belongs to the connected server (resolved credentials carry
the account ID, `credential.ResolvedCredential.AccountID`) and refuses any
other with `PERMISSION_DENIED`.

| Node | Side effects | Output |
|---|---|---|
| `discord.send_message` | unsafe | `message_id`, `message_ids` [], `channel_id`, `url` |
| `discord.read_messages` | none | `messages` [{`message_id`, `author`, `author_id`, `is_bot`, `content`, `timestamp`, `url`, `attachments` []}] (oldest first), `count`, `text` (transcript), `message_id` (latest) |

The Channel setting takes a channel name (`general`), a channel link or an
ID. Send Message splits text over 2000 characters into up to 10 messages,
can add an embed (title, text, link, hex color), and by default lets mentions
render without notifying anyone (`allowed_mentions` empty; turn on Allow
mentions to ping). Each message carries a nonce derived from the idempotency
key with `enforce_nonce`, so Discord drops a duplicate sent within a few
minutes; the action is still `unsafe` (not retried after a failure that may
have applied). Reading message text needs the bot's **Message Content
Intent** enabled in the Discord developer portal; without it, `content` is
empty for most messages.

**Configuration**: `DISCORD_CLIENT_ID`, `DISCORD_CLIENT_SECRET`,
`DISCORD_OAUTH_REDIRECT_URI` (ends with `/api/v1/oauth/callback/discord`) and
`DISCORD_BOT_TOKEN`, all four or none, read by both the API and the worker.

## HTTP Request node and credentials (Phase C7)

The `http` node (named **HTTP Request**; it was a stub before C7) calls any
HTTP API: method (GET, POST, PUT, PATCH, DELETE, HEAD), URL (or text on the
`url` input), query parameters and headers as objects, and a body sent as
JSON, a form, text or nothing (the Body setting, else the `body` input). Its
outputs are `response` (parsed JSON when the server sends JSON, else text;
non-UTF-8 bytes as `{base64, content_type, size}`), `status_code`, `headers`
(lowercase names, without `Set-Cookie`) and `ok`. Limits: 10 MB responses,
5 MB request bodies, timeout 1–120 s (default 30 s), 5 redirects.

**Errors and retries**: with **Fail on error status** (default on) a 4xx/5xx
fails the node with the platform's HTTP classification (429/408/5xx
retryable, 401/403 `INVALID_CREDENTIALS`, other 4xx `INVALID_INPUT`); the
response body is not copied into the error (turn the option off to inspect
it). The node is `unsafe`, but failures of GET, HEAD, PUT and DELETE and
429 refusals are marked not-applied, so the engine still retries those;
writes (POST, PATCH) carry an `Idempotency-Key` header derived from the
node's idempotency key.

**Credentials**: an HTTP credential (provider `http`) is a bearer token, an
API key (sent in a header or query parameter named in the node, default
`X-API-Key`) or basic auth (`username:password`), and is **bound to allowed
hosts** (`api.example.com` or `*.example.com`), sealed with the secret. The
node refuses any other host (`CREDENTIAL_HOST_NOT_ALLOWED`) and never follows
a redirect to another host while a credential is attached. Credentials of
other providers (e.g. an OpenAI key) cannot be used by the HTTP node.
`POST /credentials` takes `allowed_hosts` for provider `http`. The
**Credentials** settings page (`/settings/credentials`) lists, adds and
deletes credentials (owners and admins); a secret is sent once and can never
be read back.

**Private networks are blocked**: the node's dialer refuses loopback,
private, link-local (including cloud metadata `169.254.169.254`), CGNAT,
unspecified and multicast addresses after DNS resolution, and ignores proxy
settings, so workflows cannot reach this server's database, Redis or
internal services. `HTTP_ALLOW_PRIVATE_NETWORKS=true` lifts this (e.g. to
call an API on localhost during development).

## Integration UX (Phase C8)

**Choices from the connected account.** A setting can declare
`options_source` (choices for its value) or, on a key/value setting,
`keys_source` (suggested names), with `depends_on` naming the settings that
are sent along (required ones must be set first). The editor loads them with
`POST /api/v1/nodes/{type}/options` `{workspace_id, source, config}` →
`{items: [{value, label}]}`. Anyone who may edit workflows in the workspace
may call it; the account is resolved in that workspace exactly as for a run,
nothing is executed or changed, `{{variable}}` settings are not sent, and
errors carry only the platform's own messages. Integrations register
handlers with `ActionNode.WithOptions` (`integration.OptionsProvider`):

| Node(s) | Setting | Source |
|---|---|---|
| Notion Find / Add Database Row | Database | databases shared with the integration |
| Notion Find Database Rows | Filter by / Sort by | the database's filterable properties |
| Notion Add / Update Database Row | Properties (names) | the database's (or row's) writable properties |
| Discord Send / Read Messages | Channel | the connected server's text channels |
| Google Sheets (all) | Sheet | the spreadsheet's tabs |
| Google Sheets Append / Update Row | Values (names), Find row by column | the sheet's column names |

Choice lists keep typing as a fallback (links, IDs, names the list does not
show). Google Calendar and Gmail have no pickers (listing calendars would need
a broader scope).

**Name → value rows.** Object settings marked `key_value` (Sheets Values,
Notion Properties, HTTP Query parameters and Headers) are edited as rows with
the `{{variable}}` picker instead of JSON; a new row left empty is not stored
(an empty value clears a cell).

**Connect anything.** Gmail's inputs now accept any upstream data (`json`),
like the C4–C7 nodes: text is used as the port describes, anything else
leaves the settings in charge. Any node can therefore precede any
integration node and feed its `{{variables}}`.

## Files (Phase D1)

Workspace files (migration `000008_files`): documents and images that
workflows read by file ID. The bytes are stored in PostgreSQL (`bytea`)
next to their metadata, at most 20 MB per file. `POST /files`
(multipart/form-data: `workspace_id`, `file`), `GET /files?workspace_id=`,
`GET /files/{id}`, `GET /files/{id}/content`, `DELETE /files/{id}`; members
who may edit workflows upload and delete, any member lists and downloads.
Downloads are always attachments with `nosniff` and a sandboxing CSP, and
HTML/SVG/XML are served as `application/octet-stream`, so an uploaded page
never runs on this site. Names are cleaned (no paths or control
characters); the type comes from the extension, else the content. The
frontend proxy accepts request bodies up to 25 MB
(`experimental.proxyClientMaxBodySize`).

The **Extract Text** node (`extract_text`, category knowledge base) reads a
file's text: PDF (`github.com/ledongthuc/pdf`), Word `.docx`, HTML (visible
text), plain text, Markdown, CSV, JSON and XML, up to 2 MB of text
(`truncated` tells). The file is the connected file ID (text or
`{file_id}`), else the File setting, a picker of the workspace's files. A
file of another workspace is not found. The **Files** page (`/files`)
uploads (drag and drop), lists, downloads and deletes files.

## Knowledge bases (Phase D2)

Retrieval for workflows (migration `000009_knowledge`). A knowledge base
belongs to a workspace and embeds with one of its **OpenAI API key
credentials** (`text-embedding-3-small`, 512 dimensions); that credential
cannot be deleted while a base uses it (409 `CREDENTIAL_IN_USE`). Documents
come from pasted text, workspace files (their extracted text) or the **Add
to Knowledge Base** node; each is split into overlapping passages of about
1,200 characters, embedded in batches, and stored with unit vectors as
float32 bytes. **Search** embeds the question and ranks the base's
passages by cosine similarity in Go, so PostgreSQL needs no vector
extension (a base of tens of thousands of passages is searched in memory;
a pgvector store can replace this behind `knowledge.Repository`).

API: `POST|GET /knowledge-bases` (`?workspace_id=`), `GET|DELETE
/knowledge-bases/{id}` (GET includes the documents),
`POST /knowledge-bases/{id}/documents` (`{file_id}` or `{title, text}`;
202, indexed in the background: `INDEXING` → `READY` or `FAILED` with a
short reason such as "OpenAI rejected the API key"),
`DELETE .../documents/{doc}`, `POST .../documents/{doc}/reindex`,
`POST /knowledge-bases/{id}/search` (`{query, top_k}`). Members who edit
workflows manage bases; any member searches. A document left `INDEXING`
for 30 minutes (its process stopped) is reported `FAILED`; shutting the API
down stops background indexing cleanly.

Nodes (category knowledge base): **Knowledge Search** (`knowledge_search`)
returns `context` (numbered passages ready for an LLM prompt), `results`
(text, score, document_title, document_id) and `count`, with Passages
(top_k, 1–20) and Minimum similarity; **Add to Knowledge Base**
(`knowledge_add`) indexes text from the workflow and is idempotent (the
document ID derives from the node's idempotency key, so a retried run finds
the first document). The **Knowledge** page (`/knowledge`) creates bases,
adds files or text, shows indexing status with Retry, and has a test search.

## Images in the LLM node (Phase D3)

The LLM node sends images with the prompt to models that accept them
(OpenAI GPT-4o, GPT-4.1, GPT-5...): the **Image** setting (a picker of the
workspace's PNG, JPEG, GIF and WebP files), **Image URLs** (one `https://`
URL per line, `{{variables}}` filled in) and the `images` input (a file ID
or URL, an object with `file_id` or `url`, or a list of these). Up to 10
images per request; files are read from the execution's workspace (20 MB
each, 30 MB together) and sent inline as data URLs; URLs are fetched by the
provider. A file of another workspace is not found, a non-image file or a
non-https URL fails with `INVALID_INPUT` before any provider call.
Provider-neutral: `llm.Message.Images`; the OpenAI provider sends text and
`image_url` content parts.

## Free AI: Gemini and Groq (Phase G)

Nothing has to cost money: two free AI services join OpenAI. Both speak
the OpenAI format, so one client (`internal/provider/llm/openai`, an
`openai.Spec` per service) serves all three:

| Provider | Free? | Used for | Key from |
|---|---|---|---|
| `gemini` | yes (free tier, rate limited) | LLM node (default: `gemini-2.5-flash`), images, knowledge base embeddings, Transcribe Audio (default) | https://aistudio.google.com/apikey |
| `groq` | yes (free plan, rate limited; optional) | LLM node (`openai/gpt-oss-120b`...), Transcribe Audio (Whisper) | https://console.groq.com/keys |
| `openai` | no | LLM node, embeddings, Transcribe Audio | https://platform.openai.com/api-keys |

Keys are workspace credentials (Credentials page; the provider is the
service name). The LLM node and the templates default to Gemini.
On the free tiers Google and Groq may use what is sent to improve their
services, and the limits are a few requests per minute: fine for personal
workflows, not for private data or heavy use.

**Knowledge bases** embed with the provider of the chosen key: a Gemini key
uses `gemini-embedding-001` (its 3072 values cut to 768 by the client; the
model is trained to be shortened, and the dimensions are not sent), an
OpenAI key `text-embedding-3-small` at 512. A base keeps its model; Groq
keys cannot embed (400).

**Transcribe Audio** (`transcribe_audio`, multimodal): the text of an
uploaded recording (MP3, M4A, WAV, OGG, WebM, FLAC, MP4; the Files page now
takes them, 20 MB each), with Service (Gemini, free and the default; Groq
or OpenAI Whisper), Model,
Credential, Recording (picker of the workspace's audio files, or a
connected file ID) and an optional Language hint. Outputs `text` and
`name`. Gemini has no transcription endpoint: the recording is sent to its
chat endpoint as an `input_audio` part (up to 14 MB, base64 inline) with an
instruction to write down exactly what is said. Use it for meeting recordings: Transcribe Audio, then an LLM to
summarize.

`GEMINI_BASE_URL` and `GROQ_BASE_URL` override the endpoints (tests).

## Branching (Phase E1)

The engine runs a node only if at least one of its incoming connections is
**active**: its source ran and emitted that output. A connection is inactive
when its source was skipped, or did not emit that (optional) output, e.g.
the untaken side of a Condition. A node whose every connection is inactive
is **skipped**, and so is everything only it feeds; a node with at least one
active input runs (inactive inputs are unset), so **Merge** continues with
whichever branch ran. A node that fails to emit a *required* output is still
a failure. `{{variables}}` pointing at a skipped node resolve to nothing
(empty text, or null as a whole value). Skips are reported as `NODE_SKIPPED`
events with reason `branch_not_taken` (reused nodes keep
`completed_in_previous_attempt`) and the run view shows them as Skipped;
skipped nodes have no node execution record. A skipped output node gives no
workflow output.

**Condition** checks Value (or, when empty, the connected input) against
Compare with an operator: equals, does not equal, contains (text, or an item
of a list), does not contain, starts/ends with, greater/less than (or equal),
is empty / not empty, is true / false, matches a regex; text comparisons
ignore case unless Case sensitive, numbers compare numerically. More rules (a
JSON list) combine with all (AND) or any (OR). It emits the input on exactly
one of `true`/`false`, plus `result`. Unconfigured, it branches on whether
the input is truthy (as before). **Switch** (`switch`) compares one value with
Case 1–4 (first match wins) and continues on that route or `otherwise`, plus
`route`. **Merge** adds `value` (the first data that arrived) and modes
combine (unchanged default), first available and list.

## Error handling per node (Phase E2)

Each node may carry `settings` beside its `config` in the definition:
`on_error` (`stop`, the default; `continue`; `error_output`),
`retry_attempts` (1–5) and `retry_delay_ms` (0–30000); the validator checks
them (`INVALID_NODE_SETTINGS`). With **continue** a failure (including a
settings/`{{variable}}` resolution failure) is recorded (the node shows
FAILED, a `NODE_FAILED` event) and the run goes on: what only that node
feeds is skipped, and the execution can still complete. **error_output**
also gives the node an extra `error` output (`{code, message, node_id}`) to
connect a handler branch; it exists only with that setting
(`workflow.OutputPort`), and switching the setting off in the editor removes
its connections. Retry settings override the server default for that node
(fixed pause, or the server's longer Retry-After) but never change *which*
failures are retried: only temporary ones, and never a write that may have
applied. The editor's **When this node fails** section edits them.

## Test runs from the editor (Phase E3)

`POST /api/v1/workflows/{id}/test` `{definition, input, until_node_id?}`
runs the editor's **draft** now: nothing is saved, published or queued. With
`until_node_id` only that node and every node it depends on run. The API
checks access (execute), validates the draft (draft mode), checks credential
references, applies workflow variables, and runs the real engine in the API
process with the workspace scope, a fresh idempotency key per test, a
1-minute node timeout and a 2-minute limit. It answers 200 with `status`
(COMPLETED or FAILED), `error`, `nodes` in run order (`status` COMPLETED,
FAILED or SKIPPED, redacted `output`, `error`, `duration_ms`) and the exit
nodes' `output`; outputs are left out when `OBSERVABILITY_EXPOSE_NODE_DATA`
is false. Nodes really run, so the editor asks before a test that includes
nodes which change external systems (side effects unsafe or idempotent).

In the editor: **Test draft** (Run tab) and **Test up to this node** (node
panel) share the Run tab input; each tested node shows ✓ / ✕ / — on the
canvas, the panel shows its output or error, and the `{{variable}}` picker
shows example values from the last test (e.g. `{{read.subject}} = Invoice
October`). Test results are cleared when another workflow opens.

## Editor for everyone (Phase H)

The editor is built for people who do not want to see how it works inside.

**Palette bar** (H1): above the canvas, tabs Basics, AI, Knowledge, Apps,
Media and Logic (from the node catalog; `features/nodes/palette.ts`). An app
(integration) is one tile with its logo (`simple-icons`); clicking it asks
"What should Gmail do?" and lists its actions. Apps that do not exist yet
show as greyed "Coming soon" tiles until an integration with that ID is in
the catalog. Search still lists every matching step.

**Cards** (H2/H3): a card shows the step's account or key and its main
settings (at most three: required ones without a default, then text the user
writes, then the rest; fine-tuning and technical IDs stay under "More
settings", which opens the side panel; `lib/workflow/card.ts`). An app's card
has a menu to switch its action; settings both actions share (the account)
are kept and connections the new action cannot take are removed, as one
undoable step (`changeNodeType`). New app cards are named after the app
("Gmail"). Connection points show plain names, not data types.

**`{{name}}` inputs** (H4): typing `{{topic}}` in a step's settings adds a
`{{topic}}` connection point to its card. Whatever is connected there fills
`{{topic}}` (an exact `{{topic}}` keeps the value's type; `{{topic.field}}`
reads inside it). In the definition such an edge targets the port
`var:topic` (`workflow.VariableInputPrefix`); any step but an entry step takes
them, one connection each, of any type; they are not node ports. Without a
connection `{{topic}}` reads the run input, as before. Names follow variable
rules and cannot be `input`.

**Plain language** (H5): error codes, data types and step IDs are hidden
unless "Show technical details" (workflow panel → Display; remembered per
browser) is on. Built-in steps have plain names ("Ask AI", "Read a file",
"If / else"); key pickers name the service ("Google Gemini") and link to
adding a key. Zoom-to-fit never zooms past 100%. An empty workflow shows the
three steps: add, fill in, connect.

## Running, for people (Phase I)

**Saving is automatic** (I1): a moment after each change the editor saves
into the workflow's current DRAFT version (`PUT
/api/v1/workflows/{id}/versions/{versionID}`, draft validation as on create;
`409 VERSION_NOT_DRAFT` once the version is published or has run, and the
editor then starts a new draft). The header shows "All changes saved"; Save,
Validate, Publish and versions are under "Show technical details".

**Run** opens the Run window: one question per Input card with a **Field
name** (Text, or a File from the Files page; the Input then passes on just
that value: `field`, `field_type` settings), per declared variable, and per
run-input name the settings use (`{{input.topic}}`, or a bare `{{topic}}`
nothing connects to). Run saves, publishes when what is on screen is not
the running version yet (owners and admins), executes on the worker and
shows the result readably (text with Copy, lists of records as a table,
records as labelled lines; JSON only with technical details). Problems that
block running are listed with "Show me". "Test up to this step" uses the
same answers.

**History** (I2): `/workflows/{id}/executions` lists runs as sentences
("Worked", "Did not work: the service asked us to slow down"), when, how
long, and a preview; opening one shows the full result and the answers given.

**Plain errors** (I3): failure codes map to a title, what to do, and a fix
(`lib/workflow/friendlyErrors.ts`): "Show me" selects the step, account
problems link to Connected accounts, busy services offer "Try again". A card
whose account is not connected shows a "Connect Google" button.

**Repeat for each item** (I4): a step's `settings.repeat_over` names an input
(a port, or `var:<name>` for a `{{name}}`); the step then runs once per item
of the list arriving there (at most `MaxRepeatItems` = 100; nothing is no
items, a single value one), with `{{name}}` / that input set to the item and
the idempotency key `<key>#<index>`. Its outputs are lists, item by item, and
typed `array` for connections. A retry after an item failed continues with
that item: finished items never run again. Publishing requires the list to
be connected; entry and exit steps cannot repeat.
