import { expect, test } from "@playwright/test";
import type { Plan } from "../types/automation";
import type { Seed } from "./stack";

test("doctor repairs automatically, preserves answers, and does not execute a reopened draft", async ({ page, request }) => {
  const seed = JSON.parse(process.env.E2E_SEED!) as Seed;
  const auth = await (await request.post("/api/v1/auth/login", { data: { email: seed.email, password: seed.password } })).json();
  const headers = { Authorization: "Bearer " + auth.access_token };
  const me = await (await request.get("/api/v1/auth/me", { headers })).json();
  const workspace = me.workspaces[0].id;
  const created = await (await request.post("/api/v1/assistant/sessions", { headers, data: { workspace_id: workspace } })).json();
  const path = "/api/v1/assistant/sessions/" + created.id;
  const plan: Plan = {
    schema_version: 2, name: "Average rule demo", summary: "Show the report only when the average is above 150.", mode: "once",
    budget: { max_actions: 10, max_tokens: 2000, max_active_seconds: 60 },
    questions: [{ id: "report_name", prompt: "What should the report be called?", kind: "text", required: true, allow_other: false, options: [], example: "", min: null, max: null, step_id: "show", input: "value" }],
    steps: [{ id: "average_gate", name: "Show the report when the average is above 150", kind: "branch", tool: "", inputs: [],
      condition: { left: { name: "", source: "literal", ref: "", value_json: "200" }, op: "gt", right: { name: "", source: "literal", ref: "", value_json: "150" } },
      children: [{ id: "show", name: "Show the report", kind: "tool", tool: "value.text", inputs: [{ name: "value", source: "answer", ref: "report_name", value_json: "" }], children: [], otherwise: [], condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" }],
      otherwise: [], question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" }],
  };
  const normalizedResponse = await request.put(path + "/plan", { headers, data: { revision: created.revision, plan } });
  expect(normalizedResponse.status()).toBe(200);
  let saved = await normalizedResponse.json();
  expect(saved.compilation.plan.steps[0].condition.op).toBe("greater");
  expect(saved.compilation.diagnostics).toEqual([]);
  saved = await (await request.post(path + "/answers", { headers, data: { revision: saved.revision, question_id: "report_name", value: "Sales report" } })).json();
  expect(saved.compilation.ready).toBe(true);
  plan.steps[0].condition!.op = "approximately";
  saved = await (await request.put(path + "/plan", { headers, data: { revision: saved.revision, plan } })).json();
  const beforeRepair = saved.revision;
  await page.goto("/login");
  await page.getByLabel("Email").fill(seed.email);
  await page.getByLabel("Password", { exact: true }).fill(seed.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/home$/);
  let repairs = 0;
  page.on("request", req => { if (req.method() === "POST" && req.url().endsWith(path + "/repair")) repairs++; });
  const repaired = page.waitForResponse(res => res.request().method() === "POST" && res.url().endsWith(path + "/repair"));
  await page.goto("/home?session=" + created.id);
  const issues = page.getByRole("region", { name: "Workflow checks" });
  expect((await repaired).status()).toBe(200);
  await expect(issues).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Repair workflow" })).toHaveCount(0);
  await expect(page.getByText("Choose a supported comparison.", { exact: true })).toHaveCount(0);
  await page.getByText("Show the report when the average is above 150", { exact: true }).click();
  const inspector = page.getByRole("region", { name: "Edit Show the report when the average is above 150" });
  await expect(inspector).toBeVisible();
  await expect(inspector.getByLabel("Rule")).toContainText("is greater than");
  await page.screenshot({ path: "../.local/workflow-repair.png", fullPage: true });
  await expect(page.getByRole("button", { name: "Run workflow", exact: true })).toBeEnabled();
  saved = await (await request.get(path, { headers })).json();
  expect(saved.answers.report_name).toBe("Sales report");
  expect(saved.compilation.plan.steps[0].condition.op).toBe("greater");
  expect(saved.messages.filter((message: { role: string }) => message.role === "user")).toHaveLength(0);
  expect(repairs).toBe(1);
  expect((await request.post(path + "/repair", { headers, data: { revision: beforeRepair } })).status()).toBe(409);
  await inspector.getByLabel("Rule").selectOption("at_least");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect.poll(async () => (await (await request.get(path, { headers })).json()).compilation.plan.steps[0].condition.op).toBe("at_least");
  const runs = await (await request.get("/api/v1/automation-runs?workspace_id=" + workspace, { headers })).json();
  expect(runs.items.filter((run: { session_id: string }) => run.session_id === created.id)).toHaveLength(0);
  const other = await (await request.post("/api/v1/auth/register", { data: { email: "repair-" + crypto.randomUUID() + "@example.test", name: "Other workspace", password: seed.password } })).json();
  expect((await request.post(path + "/repair", { headers: { Authorization: "Bearer " + other.access_token }, data: { revision: saved.revision } })).status()).toBe(404);

  // A doctor that cannot fix a missing tool must stop, including after reload.
  saved = await (await request.get(path, { headers })).json();
  saved.compilation.plan.steps[0].children[0].tool = "missing.tool";
  expect((await request.put(path + "/plan", { headers, data: { revision: saved.revision, plan: saved.compilation.plan } })).status()).toBe(200);
  const unsuccessful = page.waitForResponse(res => res.request().method() === "POST" && res.url().endsWith(path + "/repair"));
  await page.reload();
  expect((await unsuccessful).status()).toBe(200);
  await expect(issues).toContainText("couldn’t finish checking this workflow automatically");
  await expect(issues.getByRole("button")).toHaveCount(0);
  expect(repairs).toBe(2);
  await page.reload();
  await expect(issues).toContainText("couldn’t finish checking this workflow automatically");
  expect(repairs).toBe(2);
  await expect(page.getByLabel("What would you like to do?")).toBeEditable();
  const finalRuns = await (await request.get("/api/v1/automation-runs?workspace_id=" + workspace, { headers })).json();
  expect(finalRuns.items.filter((run: { session_id: string }) => run.session_id === created.id)).toHaveLength(0);
});
