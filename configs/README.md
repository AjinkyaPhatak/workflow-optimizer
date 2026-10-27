# Configuration

Configuration is read from the process environment by `internal/config/config.go`.
The Go API, worker, and migration command do **not** load `.env` themselves.
Use `make setup` to create local configuration, then `make dev`, `make api`,
`make worker`, or `make migrate`; these commands load the root `.env` through
`scripts/with-env.sh`.

The frontend reads `frontend/.env.local` when started directly from that directory.
Its `BACKEND_URL` must point to the Go API. `make setup` creates this file if absent.
The browser uses `/api/v1`; Next.js proxies those requests to the backend.

`.env.example` documents runtime settings. Local `.env` files are shell-sourced:
quote values containing spaces, JSON, or shell metacharacters. Never commit real
secrets. Preserve `CREDENTIAL_ENCRYPTION_KEY` while retaining database contents:
changing it makes existing encrypted credentials unreadable.

Provider API keys for workflow nodes belong in encrypted workspace credentials
through the UI/API. The assistant also accepts a server-side `OPENAI_API_KEY`;
speech uses server-side `ELEVENLABS_API_KEY` and `ELEVENLABS_VOICE_ID`. These keys
must never be exposed to the browser. See [voice setup](../docs/OVERHAUL.md).
OAuth clients are optional and must be configured with all required fields together.
