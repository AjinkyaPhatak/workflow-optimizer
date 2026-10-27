#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
if [[ ! -f .env ]]; then
  echo 'Missing .env. Run make setup first.' >&2
  exit 1
fi
# .env is a trusted local shell file; quote values containing spaces or JSON.
set -a
source .env
set +a
export GOTOOLCHAIN=auto
export GOSUMDB=sum.golang.org
exec "$@"
