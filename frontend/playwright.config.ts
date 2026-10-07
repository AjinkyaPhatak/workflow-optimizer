import { defineConfig, devices } from "@playwright/test";
import { API_PORT } from "./e2e/stack";

const WEB_PORT = Number(process.env.E2E_WEB_PORT ?? 3100);

export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  globalSetup: "./e2e/global-setup.ts",
  use: {
    baseURL: `http://127.0.0.1:${WEB_PORT}`,
    viewport: { width: 1600, height: 1000 },
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1600, height: 1000 } } }],
  webServer: {
    command: `npx next dev -p ${WEB_PORT} --hostname 127.0.0.1`,
    url: `http://127.0.0.1:${WEB_PORT}/login`,
    env: { BACKEND_URL: `http://127.0.0.1:${API_PORT}` },
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
