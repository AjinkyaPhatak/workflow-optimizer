#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
for tool in node npm go docker; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }
done
node -e 'const [major, minor] = process.versions.node.split(".").map(Number); if (!((major === 22 && minor >= 13) || major === 24 || major >= 26)) { console.error("Use Node.js 22.13+, 24.x, or 26+ (nvm use)"); process.exit(1); }'
docker compose version >/dev/null
docker info >/dev/null

# Never overwrite existing configuration or rotate keys protecting stored credentials.
node --input-type=module <<'NODE'
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
if (!existsSync('.env')) {
  const config = readFileSync('.env.example', 'utf8')
    .replace(/^CREDENTIAL_ENCRYPTION_KEY=$/m, `CREDENTIAL_ENCRYPTION_KEY=${randomBytes(32).toString('base64')}`)
    .replace(/^AUTH_TOKEN_SECRET=$/m, `AUTH_TOKEN_SECRET=${randomBytes(48).toString('base64')}`);
  writeFileSync('.env', config, { flag: 'wx', mode: 0o600 });
  console.log('Created .env with fresh local signing and encryption keys.');
}
// Add new settings without changing existing values or encryption keys.
const current = readFileSync('.env', 'utf8');
const existing = new Set(current.split('\n').filter(line => line.includes('=')).map(line => line.split('=')[0]));
const secretNames = new Set(['COMPUTE_SERVICE_TOKEN', 'CONNECTOR_SERVICE_TOKEN', 'AUTOMATION_WEBHOOK_SECRET']);
const added = readFileSync('.env.example', 'utf8').split('\n').filter(line => /^[A-Z_]+=/.test(line) && !existing.has(line.split('=')[0])).map(line => {
  const key = line.split('=')[0];
  return secretNames.has(key) ? key + '=' + randomBytes(32).toString('hex') : line;
});
let updated = current + '\n' + added.join('\n') + '\n';
for (const key of secretNames) updated = updated.replace(new RegExp('^' + key + '=$', 'm'), key + '=' + randomBytes(32).toString('hex'));
writeFileSync('.env', updated, {mode: 0o600});
NODE

export GOTOOLCHAIN=auto
export GOSUMDB=sum.golang.org
go version
go mod download
npm --prefix frontend ci
bash scripts/with-env.sh node --input-type=module <<'NODE'
import { existsSync, writeFileSync } from 'node:fs';
if (!existsSync('frontend/.env.local')) {
  writeFileSync('frontend/.env.local', `BACKEND_URL=${JSON.stringify(process.env.BACKEND_URL || 'http://127.0.0.1:18080')}\n`, { flag: 'wx', mode: 0o600 });
  console.log('Created frontend/.env.local.');
}
NODE
if command -v nvidia-smi >/dev/null && nvidia-smi >/dev/null 2>&1 && docker info --format '{{json .Runtimes}}' | grep -q '"nvidia"'; then
  docker compose -f docker-compose.yml -f docker-compose.cuda.yml up -d --build --wait
else
  docker compose up -d --build --wait
fi
docker compose --profile images build analysis-image
bash scripts/with-env.sh go run ./cmd/migrate
echo 'Setup complete. Run make dev, then open http://localhost:3000.'
