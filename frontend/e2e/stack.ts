// Starts the real backend for the browser E2E test: a fresh PostgreSQL schema
// migrated with the repository's own migrations, the Go API (cmd/api) and
// worker (cmd/worker) built from this repository, Redis keys under a unique
// prefix, and a mock OpenAI server (no real provider is ever called).
//
// Requires E2E_DATABASE_URL / E2E_REDIS_URL (or TEST_DATABASE_URL /
// TEST_REDIS_URL) and the Go toolchain.

import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { connect } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import pg from "pg";

export const API_PORT = Number(process.env.E2E_API_PORT ?? 18180);
export const MOCK_OPENAI_PORT = Number(process.env.E2E_OPENAI_PORT ?? 18190);
export const MOCK_REPLY_PREFIX = "Mock answer to: ";

// Playwright and the scripts run from frontend/; the backend is its parent.
const repoRoot = resolve(process.cwd(), "..");

function required(...names: string[]): string {
  for (const n of names) if (process.env[n]) return process.env[n]!;
  throw new Error(`E2E needs ${names.join(" or ")} (PostgreSQL / Redis of a test environment)`);
}

async function applyMigrations(databaseUrl: string, schema: string) {
  const admin = new pg.Client({ connectionString: databaseUrl });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  await admin.end();
  const client = new pg.Client({ connectionString: databaseUrl, options: `-c search_path=${schema}` });
  await client.connect();
  try {
    const dir = join(repoRoot, "migrations");
    for (const f of readdirSync(dir).filter((f) => f.endsWith(".up.sql")).sort()) {
      await client.query(readFileSync(join(dir, f), "utf8"));
    }
  } finally {
    await client.end();
  }
}

async function dropSchema(databaseUrl: string, schema: string) {
  const admin = new pg.Client({ connectionString: databaseUrl });
  await admin.connect();
  await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin.end();
}

/** Deletes Redis keys by prefix with plain RESP (no client library needed). */
async function deleteRedisKeys(redisUrl: string, keys: string[]) {
  const u = new URL(redisUrl);
  const cmd = (args: string[]) => `*${args.length}\r\n${args.map((a) => `$${Buffer.byteLength(a)}\r\n${a}\r\n`).join("")}`;
  await new Promise<void>((done) => {
    const sock = connect(Number(u.port || 6379), u.hostname, () => {
      const db = u.pathname.replace("/", "") || "0";
      sock.write(cmd(["SELECT", db]) + cmd(["DEL", ...keys]) + cmd(["QUIT"]));
    });
    sock.on("data", () => {});
    sock.on("close", () => done());
    sock.on("error", () => done());
  });
}

interface MockCall {
  authorized: boolean;
  model: string;
  prompt: string;
}

function startMockOpenAI(expectedKey: string): Promise<Server> {
  const calls: MockCall[] = [];
  const server = createServer((req, res) => {
    if (req.method === "GET" && req.url === "/__calls") {
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify(calls));
      return;
    }
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      const parsed = JSON.parse(body || "{}") as { model?: string; messages?: { role: string; content: string }[] };
      const prompt = parsed.messages?.filter((m) => m.role === "user").map((m) => m.content).join("\n") ?? "";
      const authorized = req.headers.authorization === `Bearer ${expectedKey}`;
      calls.push({ authorized, model: parsed.model ?? "", prompt });
      res.setHeader("Content-Type", "application/json");
      if (!req.url?.endsWith("/chat/completions") || !authorized) {
        res.statusCode = authorized ? 404 : 401;
        res.end(JSON.stringify({ error: { type: "invalid_request_error", code: authorized ? "not_found" : "invalid_api_key" } }));
        return;
      }
      res.end(JSON.stringify({
        model: parsed.model,
        choices: [{ index: 0, message: { role: "assistant", content: MOCK_REPLY_PREFIX + prompt }, finish_reason: "stop" }],
        usage: { prompt_tokens: 7, completion_tokens: 5, total_tokens: 12 },
      }));
    });
  });
  return new Promise((ok) => server.listen(MOCK_OPENAI_PORT, "127.0.0.1", () => ok(server)));
}

function goBuild(pkg: string, out: string) {
  const r = spawnSync("go", ["build", "-o", out, pkg], { cwd: repoRoot, stdio: "inherit", shell: false });
  if (r.status !== 0) throw new Error(`go build ${pkg} failed`);
}

async function waitReady(url: string, child: ChildProcess, timeoutMs = 60_000) {
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    if (child.exitCode !== null) throw new Error(`backend process exited with ${child.exitCode}`);
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`${url} not ready`);
}

export interface Seed {
  email: string;
  password: string;
  workflowId: string;
  workflowName: string;
  credentialId: string;
  credentialName: string;
}

async function api<T>(method: string, path: string, body?: unknown, token?: string): Promise<T> {
  const r = await fetch(`http://127.0.0.1:${API_PORT}/api/v1${path}`, {
    method,
    headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`${method} ${path}: ${r.status} ${await r.text()}`);
  return (await r.json()) as T;
}

/** Seeds through the public API only: a user with a workspace, a project,
 * an empty workflow, and an OpenAI credential (whose secret only the mock
 * provider ever sees). */
async function seed(providerKey: string): Promise<Seed> {
  const email = `e2e-${randomBytes(4).toString("hex")}@example.test`;
  const password = "e2e-password-1";
  const reg = await api<{ access_token: string; workspace: { id: string } }>("POST", "/auth/register", { email, name: "E2E User", password });
  const t = reg.access_token;
  const project = await api<{ id: string }>("POST", "/projects", { workspace_id: reg.workspace.id, name: "E2E" }, t);
  const workflowName = "Customer Support Agent";
  const wf = await api<{ id: string }>("POST", "/workflows", { project_id: project.id, name: workflowName }, t);
  const credentialName = "OpenAI Test";
  const cred = await api<{ id: string }>("POST", "/credentials", {
    workspace_id: reg.workspace.id, name: credentialName, provider: "openai", credential_type: "api_key", secret: providerKey,
  }, t);
  return { email, password, workflowId: wf.id, workflowName, credentialId: cred.id, credentialName };
}

export async function startStack(): Promise<{ seed: Seed; stop: () => Promise<void> }> {
  const databaseUrl = required("E2E_DATABASE_URL", "TEST_DATABASE_URL");
  const redisUrl = required("E2E_REDIS_URL", "TEST_REDIS_URL");
  const schema = `e2e_${randomBytes(8).toString("hex")}`;
  const prefix = `test:e2e:${schema}`;
  const providerKey = `sk-e2e-${randomBytes(8).toString("hex")}`;
  const children: ChildProcess[] = [];
  let mock: Server | null = null;

  const stop = async () => {
    for (const c of children) c.kill();
    await Promise.all(children.map((c) => (c.exitCode !== null ? null : new Promise((r) => c.once("exit", r)))));
    await new Promise((r) => (mock ? mock.close(() => r(null)) : r(null)));
    await dropSchema(databaseUrl, schema).catch(() => {});
    await deleteRedisKeys(redisUrl, [`${prefix}:executions`, `${prefix}:dead-letter`]);
  };

  try {
    await applyMigrations(databaseUrl, schema);
    mock = await startMockOpenAI(providerKey);
    const bin = join(tmpdir(), `wo-e2e-${schema}`);
    const exe = process.platform === "win32" ? ".exe" : "";
    goBuild("./cmd/api", `${bin}-api${exe}`);
    goBuild("./cmd/worker", `${bin}-worker${exe}`);
    const u = new URL(databaseUrl);
    u.searchParams.set("search_path", schema);
    const env = {
      ...process.env,
      DATABASE_URL: u.toString(),
      REDIS_URL: redisUrl,
      REDIS_QUEUE_NAME: `${prefix}:executions`,
      REDIS_DEAD_LETTER_QUEUE: `${prefix}:dead-letter`,
      API_ADDR: `127.0.0.1:${API_PORT}`,
      AUTH_TOKEN_SECRET: randomBytes(48).toString("base64"),
      CREDENTIAL_ENCRYPTION_KEY: randomBytes(32).toString("base64"),
      OPENAI_BASE_URL: `http://127.0.0.1:${MOCK_OPENAI_PORT}/v1`,
      DEFAULT_MAX_ATTEMPTS: "1",
    };
    for (const name of ["api", "worker"]) {
      const c = spawn(`${bin}-${name}${exe}`, [], { env, stdio: process.env.E2E_VERBOSE ? "inherit" : "ignore" });
      children.push(c);
    }
    await waitReady(`http://127.0.0.1:${API_PORT}/ready`, children[0]);
    return { seed: await seed(providerKey), stop };
  } catch (e) {
    await stop();
    throw e;
  }
}
