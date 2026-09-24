# Workflow Optimizer

Architectural skeleton for a Go modular-monolith AI workflow orchestration platform.

The repository currently establishes package ownership, contracts, configuration, and local infrastructure boundaries only. It deliberately contains no workflow execution, persistence schema, queue worker behavior, authentication logic, or provider calls.

## Entry points

- `cmd/api`: transport process; it will accept execution requests in a later phase.
- `cmd/worker`: long-running execution process; it consumes execution jobs from Redis and runs them (Phase 9).

The intended future flow is API → queue → worker → executor.

## Worker (Phase 9)

`go run ./cmd/worker` starts `WORKER_COUNT` workers that consume jobs from the Redis list `REDIS_QUEUE_NAME` (default `workflow:executions`, filled with LPUSH, consumed with BRPOP). A job carries only an execution ID. Redis says which execution to try; PostgreSQL decides who owns it: each worker claims the execution with the atomic PENDING → RUNNING transition, and a worker that loses the claim, or finds the execution not PENDING, does nothing.

Configuration: `DATABASE_URL`, `REDIS_URL` (required), `REDIS_QUEUE_NAME`, `WORKER_COUNT`, `WORKER_SHUTDOWN_TIMEOUT` (for example `30s`). On SIGINT/SIGTERM the worker stops taking jobs, cancels in-flight executions (recorded as CANCELLED), and exits within the shutdown timeout.

Delivery is at-least-once at best and can lose a job: BRPOP removes a job before it is processed, so a worker process that dies between BRPOP and the claim leaves the execution PENDING until it is dispatched again. A worker process that dies after the claim leaves the execution RUNNING. There is no acknowledgement, retry, dead-letter queue, or stuck-execution recovery in Phase 9.
