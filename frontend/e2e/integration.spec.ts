import { expect, test } from "@playwright/test";
import type { AssistantSession, AutomationRun, Step, WorkflowEnvelope } from "../types/automation";
import type { Seed } from "./stack";

for (const width of [1600, 390]) {
  test(`public landing navigation stays public at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/");
    const navigation = page.getByRole("navigation", { name: "Main" });
    if (width < 800) await navigation.getByRole("button", { name: "Menu", exact: true }).click();
    const home = navigation.getByRole("link", { name: "Home", exact: true });
    await expect(home).toHaveAttribute("href", "/");
    await expect(home).toHaveAttribute("aria-current", "page");
    await expect(navigation.getByRole("link", { name: "Automations", exact: true })).toHaveCount(0);
    await home.click();
    await expect(page).toHaveURL(/\/$/);
    await navigation.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page).toHaveURL(/\/login$/);
  });
}

test("integrated dashboard preserves compiler cards, safe history, Library routes and canvas sizing", async ({ page, request }) => {
  const seed = JSON.parse(process.env.E2E_SEED!) as Seed;
  const auth = await request.post("/api/v1/auth/login", { data: { email: seed.email, password: seed.password } });
  expect(auth.ok()).toBeTruthy();
  const { access_token } = await auth.json() as { access_token: string };
  const headers = { Authorization: "Bearer " + access_token };
  const me = await (await request.get("/api/v1/auth/me", { headers })).json() as { workspaces: { id: string }[] };
  const name = "Integrated compiler report";
  const created = await request.post("/api/v1/assistant/workflows", { headers, data: { workspace_id: me.workspaces[0].id, name } });
  expect(created.status()).toBe(201);
  const workflow = await created.json() as WorkflowEnvelope;
  const step: Step = {
    id: "result", name: "Show the number", kind: "tool", tool: "value.text",
    inputs: [{ name: "value", source: "literal", ref: "", value_json: "42" }],
    children: [], otherwise: [], condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "",
  };
  const updated = await request.put(`/api/v1/assistant/sessions/${workflow.session.id}/plan`, {
    headers, data: { revision: workflow.session.revision, plan: { ...workflow.session.compilation!.plan, steps: [step] } },
  });
  expect(updated.ok()).toBeTruthy();
  const saved = await updated.json() as AssistantSession;
  expect(saved.compilation!.ready).toBe(true);
  const started = await request.post(`/api/v1/assistant/sessions/${saved.id}/runs`, { headers, data: { revision: saved.revision } });
  expect(started.ok()).toBeTruthy();
  const run = await started.json() as AutomationRun;
  await expect.poll(async () => (await (await request.get(`/api/v1/automation-runs/${run.id}`, { headers })).json() as AutomationRun).status).toBe("COMPLETED");

  const badRequests: string[] = [];
  page.on("response", response => {
    if (response.status() >= 400 && response.url().includes(`/api/v1/workflows/${saved.id}`)) badRequests.push(response.url());
  });
  await page.goto("/login");
  await page.getByLabel("Email").fill(seed.email);
  await page.getByLabel("Password", { exact: true }).fill(seed.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/home$/);
  await expect(page.getByLabel("What would you like to do?")).toBeEditable();
  expect(await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight)).toBeLessThanOrEqual(1);
  await page.goto("/workflows");
  await expect(page.getByRole("link", { name: "Build with the assistant →", exact: true })).toHaveAttribute("href", "/home");
  await page.getByTestId("recents").hover();
  const card = page.locator(".wf-card").filter({ has: page.getByRole("link", { name, exact: true }) });
  await expect(card).toContainText("Last run completed");
  await card.getByRole("button", { name: "More for " + name }).click();
  await expect(card.getByRole("menuitem", { name: "Rename", exact: true })).toHaveCount(0);
  await expect(card.getByRole("menuitem", { name: "Delete", exact: true })).toHaveCount(0);
  await card.getByRole("menuitem", { name: "Run history", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/workflows/${saved.id}/editor$`));
  await page.getByRole("button", { name: "History", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Execution history" })).toContainText("completed");
  expect(badRequests).toEqual([]);

  for (const path of ["/files", "/knowledge", "/knowledge/files"]) {
    await page.goto(path);
    await expect(page).toHaveURL(/\/library$/);
    await expect(page.getByTestId("files")).toBeVisible();
    await expect(page.getByTestId("knowledge")).toBeVisible();
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Menu", exact: true }).click();
  await expect(page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Automations" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);
});
