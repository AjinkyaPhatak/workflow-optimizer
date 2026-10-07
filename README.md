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

Limits: worker shutdown still cancels in-flight executions (Phase 9 behaviour). A node that ignores cancellation keeps its worker busy until it returns, but its execution is still failed at the deadline by the reaper and its late writes are rejected. The HTTP node's transport is still a stub.

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

`go run ./cmd/api` serves `/api/v1` on `API_ADDR` (default `:8080`). It needs `DATABASE_URL`, `REDIS_URL`, `AUTH_TOKEN_SECRET` (at least 32 bytes) and, to store credentials, `CREDENTIAL_ENCRYPTION_KEY`; `AUTH_TOKEN_TTL` defaults to 24h. Apply the migrations first (`000005` adds workflow soft delete); the processes do not migrate.

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
