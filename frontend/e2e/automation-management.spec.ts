import { createHmac, randomUUID } from "node:crypto";
import { expect, test } from "@playwright/test";
import { E2E_WEBHOOK_SECRET, type Seed } from "./stack";
import type { Automation, AutomationRun, Plan, Step } from "../types/automation";

test("automation controls open the exact run, pause future starts and cancel its durable execution", async ({ page, request }) => {
  const seed = JSON.parse(process.env.E2E_SEED!) as Seed;
  const login = await request.post("/api/v1/auth/login", { data: { email: seed.email, password: seed.password } });
  expect(login.ok()).toBeTruthy();
  const { access_token } = await login.json() as { access_token: string };
  const headers = { Authorization: "Bearer " + access_token };
  const me = await (await request.get("/api/v1/auth/me", { headers })).json() as { workspaces: { id: string }[] };
  const wait: Step = { id: "wait", name: "Wait for an event", kind: "wait", tool: "", inputs: [], children: [], otherwise: [], condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" };
  const plan: Plan = { schema_version: 2, name: "Same underlying report", summary: "Wait for an event in a durable run.", mode: "once", budget: { max_actions: 10, max_tokens: 1000, max_active_seconds: 60 }, questions: [], steps: [wait] };
  const create = async (name: string) => {
    const result = await request.put("/api/v1/automations", { headers, data: { workspace_id: me.workspaces[0].id, name, plan, cron: "", trigger: "webhook", timezone: "Asia/Kolkata", enabled: true } });
    expect(result.ok()).toBeTruthy();
    return await result.json() as Automation & { id: string };
  };
  const name = "Managed recurring execution " + randomUUID().slice(0, 8);
  const automation = await create(name);
  const unrelated = await create(name + " unrelated");
  const trigger = async () => {
    const data = JSON.stringify({ event_id: randomUUID(), data: { source: "synthetic-browser-test" } });
    const timestamp = Math.floor(Date.now() / 1000).toString();
    const signature = createHmac("sha256", E2E_WEBHOOK_SECRET).update(timestamp + "." + data).digest("hex");
    return request.post(`/api/v1/triggers/${automation.id}`, { data, headers: { "Content-Type": "application/json", "X-Workflow-Timestamp": timestamp, "X-Workflow-Signature": signature } });
  };
  expect((await trigger()).status()).toBe(202);
  const runs = async () => {
    const result = await request.get(`/api/v1/automations/${automation.id}/runs`, { headers });
    expect(result.ok()).toBeTruthy();
    return (await result.json() as { items: AutomationRun[] }).items;
  };
  await expect.poll(async () => (await runs())[0]?.status).toBe("WAITING_FOR_EVENT");
  const run = (await runs())[0];
  const unrelatedRuns = await request.get(`/api/v1/automations/${unrelated.id}/runs`, { headers });
  expect((await unrelatedRuns.json() as { items: AutomationRun[] }).items).toEqual([]);

  await page.goto("/login");
  await page.getByLabel("Email").fill(seed.email);
  await page.getByLabel("Password", { exact: true }).fill(seed.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/home$/);
  await page.goto("/automations");
  const card = page.getByRole("region", { name, exact: true });
  await expect(card.getByText("Active runs (1)")).toBeVisible();
  await expect(card.getByRole("link", { name: "Open run" })).toHaveAttribute("href", "/home?run=" + run.id);
  await expect(card.getByRole("button", { name: "Delete automation" })).toBeDisabled();
  await card.getByRole("button", { name: "Pause run", exact: true }).click();
  await expect(card.locator(".run-status")).toHaveText("paused");
  await card.getByRole("link", { name: "Open run" }).click();
  await expect(page).toHaveURL(new RegExp(`/home\\?run=${run.id}$`));
  await expect(page.getByRole("heading", { name: "Execution paused" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Stop", exact: true })).toBeVisible();

  await page.goto("/automations");
  await card.getByRole("button", { name: "Pause future runs", exact: true }).click();
  await expect(card.getByRole("button", { name: "Enable future runs", exact: true })).toBeVisible();
  await expect(card.locator(".run-status")).toHaveText("paused");
  expect((await trigger()).status()).toBe(400);
  await card.getByRole("button", { name: "Stop run", exact: true }).click();
  await expect(card.getByText("No active runs.")).toBeVisible();
  await expect.poll(async () => (await runs())[0]?.status).toBe("CANCELLED");
  await card.getByText("Recent history (1)", { exact: true }).click();
  await expect(card.locator(".automation-history .run-status")).toHaveText("cancelled");
  await expect(card.getByRole("link", { name: "Open run" })).toHaveAttribute("href", "/home?run=" + run.id);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);
  await card.getByRole("button", { name: "Delete automation" }).click();
  await expect(card).toHaveCount(0);
  expect((await request.delete(`/api/v1/automations/${unrelated.id}`, { headers })).status()).toBe(204);
});
