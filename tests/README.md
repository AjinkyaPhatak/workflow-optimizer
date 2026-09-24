# Tests

Cross-package and integration tests belong here when behavior is introduced in later phases.

## Integration tests

Integration tests run against locally installed services and are skipped when their variable is unset:

- `TEST_DATABASE_URL` (PostgreSQL): each test creates and drops its own schema.
- `TEST_REDIS_URL` (Redis, for example `redis://127.0.0.1:6379/0`): each test uses its own key under `test:phase9:` and deletes it.

`tests/phase9` needs both and covers the queue → worker → claim → executor path end to end.
