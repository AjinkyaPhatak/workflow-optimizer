# Local workflow reset

`cmd/reset-workflows` is an explicit maintenance command for the configured local
stack. It is never called by startup, setup, migrations, or tests against the
development database. Preview is the default:

```sh
GOTOOLCHAIN=auto GOSUMDB=sum.golang.org bash scripts/with-env.sh go run ./cmd/reset-workflows
```

The preview prints table counts and the number of application Temporal schedules
and executions. It never prints account data, tokens, database URLs, or credentials.
The command rejects non-loopback PostgreSQL, Redis, and Temporal targets.

## What is removed

The reset removes legacy workflows and versions; executions, node results, events,
status history, leases, dispatch/cancellation/dead-letter records; assistant
conversations; automation runs, action ledgers, events, schedules, triggers and
outbox rows; saved workflow templates; and compiler workflow metadata, published
versions and canvas layouts. Bundled templates live in source and remain available.

The reset preserves users, workspaces, memberships, projects, encrypted credentials,
connected accounts, WhatsApp sessions, files (including generated files), Knowledge
bases/documents/chunks, and all environment keys. It compares hashes of every
preserved row before committing. Knowledge indexing executions in Temporal are
excluded.

## Execute after stopping the application

Stop `make dev`, plus any separately started API, legacy worker, orchestrator or
connector. Close database clients using this database. Execution refuses to run
while PostgreSQL reports any other client connection, including an idle application
pool. Infrastructure stays running.

Create a fresh private custom-format backup. These commands use the default local
database; if configuration differs, use the matching database and credentials.
Do not reuse a backup from another database or overwrite your only backup.

```sh
mkdir -p .local/recovery
chmod 700 .local/recovery
umask 077
docker compose exec -T postgres sh -c 'pg_dump --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" --format=custom' > .local/recovery/before-canvas-reset.dump
chmod 600 .local/recovery/before-canvas-reset.dump
GOTOOLCHAIN=auto GOSUMDB=sum.golang.org bash scripts/with-env.sh go run ./cmd/reset-workflows --execute --database workflow_optimizer --backup .local/recovery/before-canvas-reset.dump
make dev
```

Execution requires an exact database name, a nonempty `PGDMP` archive less than an
hour old, and private file permissions. The tool checks the archive format, not
its provenance: the operator must create the backup from the database named in
the preview after stopping the application. Restore procedures should be tested
separately before relying on a dump for production recovery. This command is for
local development only.

Temporal cleanup deletes only schedules with this application's ID prefix, trigger
workflow type and configured task queue. It inventories only `workflow.v2` and
`automation.trigger` executions on that queue with recognized application IDs.
It requests cancellation and terminates remaining open executions before removing
their history. This is offline teardown; completed external actions cannot be
undone. Redis cleanup removes only queue/dead-letter entries containing legacy
execution IDs found in the database snapshot. OAuth state, lockout keys, other
queues and malformed entries remain untouched.

Database cleanup locks an explicit table allowlist, refuses changes since its
snapshot, and uses `TRUNCATE ... RESTRICT`, without `CASCADE` or identity resets.
An unexpected foreign key makes it roll back. The two append-only history
TRUNCATE guards are disabled and restored in the same transaction; all other
lifecycle guards remain enabled. No schema, Redis database or Temporal namespace
is reset. A transport cleanup failure stops before database cleanup; a database
failure rolls back database changes, though already cancelled Temporal work and
removed queue messages stay cancelled/removed. Keep the backup until verification
is complete.

## Verification

The maintenance tests use `TEST_DATABASE_URL` and `TEST_REDIS_URL`; run them through
the disposable database wrapper:

```sh
GOTOOLCHAIN=auto GOSUMDB=sum.golang.org bash scripts/with-env.sh node scripts/test-services.mjs go test -race -p 1 -count=1 ./internal/maintenance ./cmd/reset-workflows
```

Set `TEST_TEMPORAL_ADDRESS=127.0.0.1:57233` in that command to also exercise real
Temporal cleanup on a fresh, isolated test queue with no workers or provider calls.

Coverage includes retained credential/connection/Knowledge/file rows, published
legacy versions and append-only history, stale-snapshot rejection, rollback on an
unknown foreign key, restored guards, Temporal ownership filtering, and selective
Redis cleanup. The optional Temporal test creates pending executions and verifies
that reset removes only its own work while preserving Knowledge and another queue.
Tests never call real message, model or transcription providers.
