import { expect, test, type Locator, type Page } from "@playwright/test";
import { MOCK_OPENAI_PORT, MOCK_REPLY_PREFIX, type Seed } from "./stack";

// The Phase 13 acceptance path, in a real browser against the real backend
// (Go API + worker + PostgreSQL + Redis; only OpenAI is mocked):
//
// Login -> open workflow -> add Input, LLM, Output -> connect -> configure LLM
// (credential by metadata) -> save -> validate -> publish -> execute.

const seed = (): Seed => JSON.parse(process.env.E2E_SEED ?? "null");

const node = (page: Page, type: string) => page.locator(`[data-node-type="${type}"]`);
const handle = (page: Page, type: string, port: string) => node(page, type).locator(`.react-flow__handle[data-handleid="${port}"]`);

async function dragPaletteItem(page: Page, type: string, x: number, y: number) {
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

test("build, validate, publish and execute a workflow in the visual editor", async ({ page, request }) => {
  const s = seed();
  expect(s, "global setup must seed the backend").toBeTruthy();

  // --- login and open the workflow ------------------------------------------
  await page.goto("/workflows");
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel("Email").fill(s.email);
  await page.getByLabel("Password").fill(s.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/workflows$/);
  await page.getByRole("link", { name: s.workflowName }).click();
  await expect(page).toHaveURL(new RegExp(`/workflows/${s.workflowId}/editor$`));
  await expect(page.getByTestId("version-status")).toHaveText("No saved version");

  // The palette is the backend node registry.
  for (const type of ["input", "llm", "output", "prompt", "http"]) await expect(page.getByTestId(`palette-${type}`)).toBeVisible();

  // --- build: Input -> LLM -> Output ----------------------------------------
  await dragPaletteItem(page, "input", 160, 220);
  await dragPaletteItem(page, "llm", 520, 220);
  await expect(page.getByTestId("dirty")).toBeVisible();

  // An invalid connection (output port to output port) is refused.
  await connect(page, handle(page, "input", "data"), handle(page, "llm", "response"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(0);

  await connect(page, handle(page, "input", "data"), handle(page, "llm", "prompt"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(1);

  // Configure the LLM: credential chosen from metadata, model edited.
  await node(page, "llm").locator(".wf-node-header").click();
  const panel = page.getByTestId("config-panel");
  await expect(panel.getByRole("heading", { name: "LLM" })).toBeVisible();
  await panel.locator("#cfg-credential_id").selectOption({ label: `${s.credentialName} (openai)` });
  // The model is a backend-provided option (Phase B).
  await panel.locator("#cfg-model").selectOption("gpt-5-mini");
  await panel.locator("#cfg-node-name").fill("Answer");
  await panel.locator("#cfg-node-name").press("Enter");
  await expect(node(page, "llm").locator(".wf-node-title")).toHaveText("Answer");

  // Undo / redo of a node addition and deletion.
  await dragPaletteItem(page, "output", 880, 220);
  await node(page, "output").locator(".wf-node-header").click();
  await page.keyboard.press("Delete");
  await expect(node(page, "output")).toHaveCount(0);
  await page.locator(".react-flow__pane").click({ position: { x: 20, y: 20 } });
  await page.keyboard.press("Control+z");
  await expect(node(page, "output")).toHaveCount(1);
  await page.keyboard.press("Control+Shift+z");
  await expect(node(page, "output")).toHaveCount(0);
  await page.keyboard.press("Control+z");
  await expect(node(page, "output")).toHaveCount(1);

  // --- validate before the output is connected: backend errors are shown ----
  await page.getByRole("button", { name: "Validate", exact: true }).click();
  await expect(page.getByTestId("version-status")).toHaveText("v1 · Draft");
  const errors = page.getByTestId("validation-errors");
  await expect(errors).toContainText("MISSING_REQUIRED_INPUT");
  await expect(node(page, "output")).toHaveClass(/invalid/);
  await expect(node(page, "output").locator(".wf-node-errors")).toContainText("value");
  // Clicking an error selects its node.
  await errors.locator("li").filter({ has: page.locator(".issue-title", { hasText: /^Output$/ }) }).getByRole("button").first().click();
  await expect(panel.getByRole("heading", { name: "Output" })).toBeVisible();

  // --- fix, save, validate, publish -----------------------------------------
  await page.getByRole("button", { name: "Fit View" }).click();
  await connect(page, handle(page, "llm", "response"), handle(page, "output", "value"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(2);
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByTestId("version-status")).toHaveText("v2 · Draft");
  await expect(page.getByTestId("dirty")).toHaveCount(0);

  await page.getByRole("button", { name: "Validate", exact: true }).click();
  await expect(page.getByTestId("validation-ok")).toBeVisible();
  await expect(node(page, "output")).not.toHaveClass(/invalid/);

  await page.getByRole("button", { name: "Publish" }).click();
  await expect(page.getByTestId("version-status")).toHaveText("v2 · Published");

  // The saved definition is exactly what the canvas shows.
  const token = await page.evaluate(() => JSON.parse(localStorage.getItem("workflow-optimizer.session")!).token as string);
  const auth = { Authorization: `Bearer ${token}` };
  const wf = await (await request.get(`/api/v1/workflows/${s.workflowId}`, { headers: auth })).json();
  const version = await (await request.get(`/api/v1/workflows/${s.workflowId}/versions/${wf.active_version_id}`, { headers: auth })).json();
  expect(version.status).toBe("PUBLISHED");
  const def = version.definition;
  expect(def.nodes.map((n: { type: string }) => n.type).sort()).toEqual(["input", "llm", "output"]);
  const llm = def.nodes.find((n: { type: string }) => n.type === "llm");
  expect(llm.name).toBe("Answer");
  expect(llm.config).toMatchObject({ provider: "openai", model: "gpt-5-mini", credential_id: s.credentialId });
  expect(def.edges.map((e: { source_port: string; target_port: string }) => `${e.source_port}->${e.target_port}`).sort()).toEqual(["data->prompt", "response->value"]);
  // No secret anywhere in the definition.
  expect(JSON.stringify(def)).not.toMatch(/sk-e2e/);

  // Editing a published version creates a new draft on save (never mutates it).
  await page.locator(".react-flow__pane").click({ position: { x: 20, y: 20 } });
  await node(page, "input").locator(".wf-node-header").click();
  await panel.locator("#cfg-node-name").fill("Question");
  await panel.locator("#cfg-node-name").press("Enter");
  await expect(page.getByTestId("dirty")).toContainText("saving creates a new draft");
  await page.keyboard.press("Control+z");
  await expect(page.getByTestId("dirty")).toHaveCount(0);

  // --- execute with test input -------------------------------------------------
  await page.getByRole("button", { name: "Execute" }).first().click();
  const run = page.locator(".bottom-content:not([hidden])");
  await run.getByLabel("Input query").fill("What is quantum computing?");
  await run.getByRole("button", { name: "Execute" }).click();
  await expect(page.getByTestId("execution-status")).toHaveText("COMPLETED", { timeout: 60_000 });
  const output = page.getByTestId("execution-output");
  await expect(output).toContainText(MOCK_REPLY_PREFIX);
  await expect(output).toContainText("What is quantum computing?");

  // The provider was called by the worker with the stored credential.
  const all: { prompt: string }[] = await (await request.get(`http://127.0.0.1:${MOCK_OPENAI_PORT}/__calls`)).json();
  const calls = all.filter((c) => c.prompt.includes("What is quantum computing?"));
  expect(calls).toHaveLength(1);
  expect(calls[0]).toMatchObject({ authorized: true, model: "gpt-5-mini" });
  expect(calls[0].prompt).toContain("What is quantum computing?");

  // The browser never held the provider secret.
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
  expect(stored).not.toMatch(/sk-e2e/);
});
