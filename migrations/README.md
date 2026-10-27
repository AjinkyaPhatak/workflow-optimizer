# Migrations

Twelve ordered up/down migration pairs currently define the application schema.
The API and worker do not migrate on startup. After starting PostgreSQL, run:

```sh
make migrate
```

This invokes `cmd/migrate`, which reads `DATABASE_URL` and calls the existing
`postgres.ApplyMigrations` helper backed by golang-migrate. Pending migrations
run once; rerunning the command against an up-to-date database succeeds without
reapplying them. Use `go run ./cmd/migrate -path /absolute/path/to/migrations`
when running outside the repository root with the environment already loaded.

Add new numbered `.up.sql` and `.down.sql` files for schema changes. Preserve
published versions, append-only events/history, workspace constraints, and
execution claim fencing. Do not edit already-applied migrations to introduce
new behavior. Integration tests create their own schemas and exercise migration
and lifecycle behavior using the same helper.
