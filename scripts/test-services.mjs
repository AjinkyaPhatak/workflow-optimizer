// Give each verification command its own database, even if TEST_DATABASE_URL
// points at a development database. Individual suites still isolate schemas.
import pg from "../frontend/node_modules/pg/lib/index.js";
import { randomUUID } from "node:crypto";
import { spawn } from "node:child_process";

const [command, ...args] = process.argv.slice(2);
const base = process.env.TEST_DATABASE_URL || process.env.DATABASE_URL;
if (!command || !base) {
  console.error("A test command and DATABASE_URL (or TEST_DATABASE_URL) are required.");
  process.exit(1);
}
const name = "wo_test_" + randomUUID().replaceAll("-", "");
const admin = new pg.Client({ connectionString: base });
let created = false;
let child;
process.on("SIGINT", () => { child?.kill("SIGINT"); });
process.on("SIGTERM", () => { child?.kill("SIGTERM"); });
try {
  await admin.connect();
  await admin.query(`CREATE DATABASE "${name}"`);
  created = true;
  const isolated = new URL(base);
  isolated.pathname = "/" + name;
  isolated.searchParams.delete("search_path");
  isolated.searchParams.delete("options");
  console.log("Running tests in disposable database " + name);
  const result = await new Promise((resolve, reject) => {
    child = spawn(command, args, { stdio: "inherit", env: {
      ...process.env,
      TEST_DATABASE_URL: isolated.toString(),
      E2E_DATABASE_URL: isolated.toString(),
      TEST_REDIS_URL: process.env.TEST_REDIS_URL || process.env.REDIS_URL,
    } });
    child.once("error", reject);
    child.once("exit", (code, signal) => resolve(code ?? (signal === "SIGINT" ? 130 : 143)));
  });
  process.exitCode = result;
} catch {
  // Connection strings and provider keys must never be printed in test errors.
  console.error("Test database setup or command failed. Check PostgreSQL connectivity and CREATE DATABASE permission.");
  process.exitCode = 1;
} finally {
  if (created) {
    try { await admin.query(`DROP DATABASE "${name}" WITH (FORCE)`); }
    catch { console.error("Could not clean up disposable database " + name); process.exitCode = 1; }
  }
  await admin.end();
}
