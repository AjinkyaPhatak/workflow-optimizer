#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export GOTOOLCHAIN=auto
go build -o bin/api ./cmd/api
go build -o bin/worker ./cmd/worker
go build -o bin/orchestrator ./cmd/orchestrator
go build -o bin/connector ./cmd/connector

children=()
cleanup() {
  trap - EXIT INT TERM
  for pid in "${children[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done
  for pid in "${children[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
./bin/api &
children+=("$!")
./bin/worker &
children+=("$!")
./bin/orchestrator &
children+=("$!")
./bin/connector &
children+=("$!")
# exec avoids leaving the Next.js process behind when this script stops.
(cd frontend && exec node node_modules/next/dist/bin/next dev -p 3000) &
children+=("$!")
echo 'Frontend: http://localhost:3000 | Press Ctrl+C to stop the application.'
wait -n "${children[@]}"
