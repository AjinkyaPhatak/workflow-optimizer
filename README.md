# Workflow Optimizer

Architectural skeleton for a Go modular-monolith AI workflow orchestration platform.

The repository currently establishes package ownership, contracts, configuration, and local infrastructure boundaries only. It deliberately contains no workflow execution, persistence schema, queue worker behavior, authentication logic, or provider calls.

## Entry points

- `cmd/api`: transport process; it will accept execution requests in a later phase.
- `cmd/worker`: long-running execution process; it will consume queued work in a later phase.

The intended future flow is API → queue → worker → executor.
