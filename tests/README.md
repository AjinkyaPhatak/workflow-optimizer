# Tests

Cross-package and integration tests belong here when behavior is introduced in later phases.

## Integration tests

Integration tests run against locally installed services and are skipped when their variable is unset:

- `TEST_DATABASE_URL` (PostgreSQL): each test creates and drops its own schema.
- `TEST_REDIS_URL` (Redis, for example `redis://127.0.0.1:6379/0`): each test uses its own keys under `test:phase9:` / `test:phase10:` and deletes them.

`tests/phase9` needs both and covers the queue → worker → claim → executor path end to end.

`tests/phase10` needs both and covers retries, dead letters, timeouts, leases and heartbeats, the reaper, the retry scheduler, migration 000003/000004 round trips (including a Phase 8 workload while rolled back), claim-token fencing of stale workers, unsafe worker-loss recovery, the worker runtime end to end, and a 100-execution / 5-worker concurrency run with failures, duplicate jobs and crashed workers. Run it with `-race` where the platform supports it (CGO).

`tests/phase11` needs `TEST_DATABASE_URL` and runs an LLM workflow end to end: an encrypted credential in PostgreSQL, the credential service and resolver, the LLM node, the provider registry and the OpenAI provider against a mock OpenAI server, through the Phase 8/10 lifecycle; it also covers cross-workspace credential isolation and provider errors flowing into retry classification.

`tests/phase12` needs both and drives the REST API over HTTP through the production composition (`app.NewAPI`), with real workers (`app.NewWorker`) where executions must run: POST /execute → PENDING row in PostgreSQL → job in Redis → worker → graph executor → GET /executions/{id} COMPLETED with output; workflow CRUD, versions, validation and publishing; cross-workspace isolation (every foreign resource is 404) and OWNER/ADMIN/MEMBER/VIEWER permissions; credential encryption and redaction; cancellation; readiness with real and closed dependencies.

`tests/phase14` needs both and covers execution observability end to end: the debugger API (`GET /executions/{id}`, `/nodes`, `/events`, `/workflows/{id}/executions`) on real LLM executions against a mock OpenAI server; event order for success, in-place node retries, execution retries, retry exhaustion, execution timeout and cancellation of a running execution; authorization (401 / cross-workspace 404 / missing 404); pagination; secrets absent from responses and from every persisted execution row; the node-data policy; and that an unavailable event store never changes an execution's outcome.
