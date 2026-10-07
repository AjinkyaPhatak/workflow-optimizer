import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { API_PORT, MOCK_FAIL_ONCE, MOCK_REPLY_PREFIX, type Seed } from "./stack";

// Phase 14 acceptance path against the real stack (Go API + worker +
// PostgreSQL + Redis; only OpenAI is mocked):
//
// create + publish a workflow -> execute from the editor -> worker runs it ->
// open the execution debugger -> timeline, node inspector, input/output,
// status/duration/attempts, tokens and estimated cost -> event history; then
// a run whose provider call fails once, shown as a retry.

const seed = (): Seed => JSON.parse(process.env.E2E_SEED ?? "null");
const api = (path: string) => `http://127.0.0.1:${API_PORT}/api/v1${path}`;

async function publishLLMWorkflow(request: APIRequestContext, s: Seed): Promise<string> {
  const login = await (await request.post(api("/auth/login"), { data: { email: s.email, password: s.password } })).json();
  const headers = { Authorization: `Bearer ${login.access_token}` };
  const me = await (await request.get(api("/auth/me"), { headers })).json();
  const project = await (await request.post(api("/projects"), { headers, data: { workspace_id: me.workspaces[0].id, name: "Debugger" } })).json();
  const wf = await (await request.post(api("/workflows"), { headers, data: { project_id: project.id, name: "Debugged Assistant" } })).json();
  const definition = {
    version: 1,
    settings: {},
    nodes: [
      { id: "in", type: "input", name: "Input", position: { x: 0, y: 0 }, config: {} },
      { id: "llm_1", type: "llm", name: "Answer", position: { x: 300, y: 0 }, config: { provider: "openai", model: "gpt-5-mini", credential_id: s.credentialId } },
      { id: "out", type: "output", name: "Output", position: { x: 600, y: 0 }, config: {} },
    ],
    edges: [
      { id: "e1", source: "in", source_port: "data", target: "llm_1", target_port: "prompt" },
      { id: "e2", source: "llm_1", source_port: "response", target: "out", target_port: "value" },
    ],
  };
  const v = await request.post(api(`/workflows/${wf.id}/versions`), { headers, data: { definition } });
  expect(v.status()).toBe(201);
  const version = await v.json();
  expect((await request.post(api(`/workflows/${wf.id}/versions/${version.id}/publish`), { headers })).status()).toBe(200);
  return wf.id;
}

async function runFromEditor(page: Page, workflowId: string, query: string) {
  await page.goto(`/workflows/${workflowId}/editor`);
  await page.getByRole("button", { name: "Execute" }).first().click();
  const run = page.locator(".bottom-content:not([hidden])");
  await run.getByLabel("Input query").fill(query);
  await run.getByRole("button", { name: "Execute" }).click();
  await expect(run.getByTestId("execution-status")).toHaveText("COMPLETED", { timeout: 60_000 });
  await run.getByTestId("open-debugger").click();
  await expect(page).toHaveURL(/\/executions\/[0-9a-f-]{36}$/);
}

test("inspect a completed execution and a retried one in the execution debugger", async ({ page, request }) => {
  const s = seed();
  const workflowId = await publishLLMWorkflow(request, s);

  await page.goto("/login");
  await page.getByLabel("Email").fill(s.email);
  await page.getByLabel("Password").fill(s.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/workflows$/);

  // --- a completed execution -------------------------------------------------
  await runFromEditor(page, workflowId, "Explain observability");
  await expect(page.getByRole("heading", { name: "Debugged Assistant" })).toBeVisible();
  await expect(page.getByTestId("execution-status")).toHaveText("COMPLETED");
  await expect(page.getByText("Final")).toBeVisible(); // polling stopped at the terminal state
  await expect(page.getByTestId("summary-attempts")).toHaveText("1 / 1");
  await expect(page.getByTestId("summary-nodes")).toHaveText("3");
  await expect(page.getByTestId("summary-tokens")).toHaveText("12");
  await expect(page.getByTestId("summary-estimated-cost")).toHaveText("$0.0170"); // 7*1000/1e6 + 5*2000/1e6
  await expect(page.getByTestId("summary-duration")).not.toHaveText("—");

  for (const id of ["in", "llm_1", "out"]) await expect(page.getByTestId(`timeline-${id}`)).toHaveAttribute("data-status", "COMPLETED");
  await page.getByTestId("timeline-llm_1").getByRole("button").click();
  const inspector = page.getByTestId("node-inspector");
  await expect(inspector.getByRole("heading", { name: "Answer" })).toBeVisible();
  await expect(page.getByTestId("inspector-status")).toHaveText("COMPLETED");
  await expect(page.getByTestId("inspector-attempt")).toHaveText("1");
  await expect(page.getByTestId("inspector-provider")).toHaveText("openai");
  await expect(page.getByTestId("inspector-model")).toHaveText("gpt-5-mini");
  await expect(page.getByTestId("inspector-tokens")).toHaveText("Input 7 · Output 5 · Total 12");
  await expect(page.getByTestId("inspector-duration")).not.toHaveText("—");
  await expect(page.getByTestId("json-input")).toContainText("Explain observability");
  await expect(page.getByTestId("json-output")).toContainText(MOCK_REPLY_PREFIX);

  // The persisted event history.
  await page.getByRole("tab", { name: /Events/ }).click();
  const log = page.getByTestId("event-log");
  for (const type of ["EXECUTION_STARTED", "NODE_STARTED", "NODE_COMPLETED", "EXECUTION_COMPLETED"]) await expect(log).toContainText(type);
  // No credential material anywhere on the page.
  expect(await page.content()).not.toMatch(/sk-e2e/);

  // --- a run whose provider call fails once, retried in place ---------------
  await runFromEditor(page, workflowId, `a ${MOCK_FAIL_ONCE} question`);
  await expect(page.getByTestId("execution-status")).toHaveText("COMPLETED");
  await expect(page.getByTestId("summary-retries")).toHaveText("1");
  await expect(page.getByTestId("attempt-llm_1-1")).toContainText("Attempt 1: FAILED · PROVIDER_UNAVAILABLE → retry");
  await expect(page.getByTestId("attempt-llm_1-2")).toContainText("Attempt 2: COMPLETED");
  await page.getByTestId("timeline-llm_1").getByRole("button").click();
  await page.getByTestId("node-inspector").getByRole("tab", { name: "Attempt 1" }).click();
  await expect(page.getByTestId("inspector-error")).toContainText("PROVIDER_UNAVAILABLE");
  await page.getByRole("tab", { name: /Events/ }).click();
  for (const type of ["NODE_FAILED", "RETRY_SCHEDULED", "RETRY_STARTED"]) await expect(page.getByTestId("event-log")).toContainText(type);

  // --- navigation: workflow -> executions -> execution ------------------------
  await page.getByRole("link", { name: "← Executions" }).click();
  const list = page.getByTestId("execution-list");
  await expect(list.locator("li")).toHaveCount(2);
  await list.locator("li").last().getByRole("link").click();
  await expect(page).toHaveURL(/\/executions\/[0-9a-f-]{36}$/);
  await expect(page.getByTestId("execution-status")).toHaveText("COMPLETED");
  await expect(page.getByTestId("summary-retries")).toHaveText("0");
});
