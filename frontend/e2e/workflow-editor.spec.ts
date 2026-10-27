import { expect, test, type Locator, type Page } from "@playwright/test";
import { MOCK_OPENAI_PORT, MOCK_REPLY_PREFIX, type Seed } from "./stack";

// The acceptance path, in a real browser against the real backend (Go API +
// worker + PostgreSQL + Redis; only OpenAI is mocked), as a non-technical
// user does it since Phase I (technical details off):
//
// Login -> open workflow -> add Input (a question), Ask AI, Output -> connect
// -> fill in the cards -> saved automatically -> Run -> answer -> result ->
// History.

const seed = (): Seed => JSON.parse(process.env.E2E_SEED ?? "null");

const node = (page: Page, type: string) => page.locator(`[data-node-type="${type}"]`);
const handle = (page: Page, type: string, port: string) => node(page, type).locator(`.react-flow__handle[data-handleid="${port}"]`);

// The palette bar's tab of each built-in node used here (Phase H1).
const TABS: Record<string, string> = { input: "basics", output: "basics", text: "basics", llm: "ai", prompt: "ai", http: "apps" };
async function showTile(page: Page, type: string) {
  await page.getByTestId(`palette-tab-${TABS[type] ?? "basics"}`).click();
}

async function dragPaletteItem(page: Page, type: string, x: number, y: number) {
  await showTile(page, type);
  await page.getByTestId(`palette-${type}`).dragTo(page.getByTestId("canvas"), { targetPosition: { x, y } });
  await expect(node(page, type)).toHaveCount(1);
}

/** The element's box once the canvas has stopped moving (pan/zoom animations). */
async function settledBox(loc: Locator) {
  let prev = await loc.boundingBox();
  for (let i = 0; i < 20; i++) {
    await loc.page().waitForTimeout(100);
    const next = await loc.boundingBox();
    if (prev && next && prev.x === next.x && prev.y === next.y && prev.width === next.width) return next;
    prev = next;
  }
  return prev!;
}

async function connect(page: Page, from: Locator, to: Locator) {
  const a = await settledBox(from);
  const b = await settledBox(to);
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.mouse.down();
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 12 });
  await page.mouse.up();
}

// Technical details off: the plain editor (Phase H5/I1).
test.use({ storageState: { cookies: [], origins: [] } });

test("build a workflow on the cards and run it", async ({ page, request }) => {
  const s = seed();
  expect(s, "global setup must seed the backend").toBeTruthy();

  // --- login and open the workflow ------------------------------------------
  await page.goto("/workflows");
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel("Email").fill(s.email);
  await page.getByLabel("Password").fill(s.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/home$/);
  await page.goto("/workflows");
  await page.getByTestId("recents").hover();
  await page.getByRole("link", { name: s.workflowName }).click();
  await expect(page).toHaveURL(new RegExp(`/workflows/${s.workflowId}/editor$`), { timeout: 60_000 });
  // Versions, Validate and Publish are technical details.
  await expect(page.getByTestId("version-status")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Publish" })).toHaveCount(0);

  // The palette is the backend node registry.
  for (const type of ["input", "llm", "output", "prompt", "http"]) {
    await showTile(page, type);
    await expect(page.getByTestId(`palette-${type}`)).toBeVisible();
  }

  // --- build: Input (a question) -> Ask AI -> Output ------------------------
  await dragPaletteItem(page, "input", 160, 220);
  await node(page, "input").getByLabel("Field name").fill("question");
  await node(page, "input").getByLabel("Field name").press("Tab");
  await dragPaletteItem(page, "llm", 560, 220);

  // An invalid connection (output to output) is refused.
  await connect(page, handle(page, "input", "data"), handle(page, "llm", "response"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(0);
  await connect(page, handle(page, "input", "data"), handle(page, "llm", "prompt"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(1);

  // The AI step: service and key on its card, the model in More settings.
  const ai = node(page, "llm");
  await ai.getByLabel("Provider").selectOption("openai");
  await ai.getByLabel("Credential").selectOption({ label: `${s.credentialName} (OpenAI)` });
  await ai.locator(".wf-node-header").click();
  const panel = page.getByTestId("config-panel");
  await expect(panel.getByRole("heading", { name: "Ask AI" })).toBeVisible();
  await panel.locator("#cfg-model").selectOption("gpt-5-mini");

  await dragPaletteItem(page, "output", 960, 220);
  await page.getByRole("button", { name: "Fit View" }).dispatchEvent("click"); // the dev-mode Next.js badge covers it
  await connect(page, handle(page, "llm", "response"), handle(page, "output", "value"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(2);

  // Undo / redo.
  await page.locator(".react-flow__pane").click({ position: { x: 20, y: 20 } });
  await page.keyboard.press("Control+z");
  await expect(page.locator(".react-flow__edge")).toHaveCount(1);
  await page.keyboard.press("Control+Shift+z");
  await expect(page.locator(".react-flow__edge")).toHaveCount(2);

  // Saved automatically.
  await expect(page.getByTestId("save-state")).toHaveText("All changes saved", { timeout: 30_000 });

  // --- run: answer the question, see the answer -----------------------------
  await page.getByTestId("run-open").click();
  const dialog = page.getByTestId("run-dialog");
  await dialog.getByTestId("run-start").click();
  await expect(dialog.getByRole("alert")).toHaveText("Fill in Question.");
  await dialog.getByLabel(/^Question/).fill("What is quantum computing?");
  await dialog.getByTestId("run-start").click();
  const result = dialog.getByTestId("run-success");
  await expect(result).toContainText("✓ Done", { timeout: 60_000 });
  await expect(result).toContainText(MOCK_REPLY_PREFIX);
  await expect(result).toContainText("What is quantum computing?");
  await expect(result.locator("pre")).toHaveCount(0); // no JSON

  // The provider was called by the worker with the stored credential.
  const all: { prompt: string }[] = await (await request.get(`http://127.0.0.1:${MOCK_OPENAI_PORT}/__calls`)).json();
  const calls = all.filter((c) => c.prompt.includes("What is quantum computing?"));
  expect(calls).toHaveLength(1);
  expect(calls[0]).toMatchObject({ authorized: true, model: "gpt-5-mini" });

  // Running published exactly what the canvas shows; no secret in it.
  const token = await page.evaluate(() => JSON.parse(localStorage.getItem("workflow-optimizer.session")!).token as string);
  const auth = { Authorization: `Bearer ${token}` };
  const wf = await (await request.get(`/api/v1/workflows/${s.workflowId}`, { headers: auth })).json();
  const version = await (await request.get(`/api/v1/workflows/${s.workflowId}/versions/${wf.active_version_id}`, { headers: auth })).json();
  expect(version.status).toBe("PUBLISHED");
  const def = version.definition;
  expect(def.nodes.map((n: { type: string }) => n.type).sort()).toEqual(["input", "llm", "output"]);
  expect(def.nodes.find((n: { type: string }) => n.type === "input").config).toMatchObject({ field: "question" });
  expect(def.nodes.find((n: { type: string }) => n.type === "llm").config).toMatchObject({ provider: "openai", model: "gpt-5-mini", credential_id: s.credentialId });
  expect(def.edges.map((e: { source_port: string; target_port: string }) => `${e.source_port}->${e.target_port}`).sort()).toEqual(["data->prompt", "response->value"]);
  expect(JSON.stringify(def)).not.toMatch(/sk-e2e/);

  // --- history: the run in plain words --------------------------------------
  await dialog.getByRole("link", { name: "All runs" }).click();
  const list = page.getByTestId("execution-list");
  await expect(list.locator("li")).toHaveCount(1, { timeout: 30_000 });
  await expect(list.locator("li").first()).toContainText("Worked");
  await list.locator(".history-summary").first().click();
  await expect(list).toContainText("You gave: Question = What is quantum computing?");

  // The browser never held the provider secret.
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  expect(stored).not.toMatch(/sk-e2e/);
});
