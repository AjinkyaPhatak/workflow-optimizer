import { expect, test, type Locator, type Page } from "@playwright/test";
import { API_PORT, type Seed } from "./stack";

// Phase A acceptance path in a real browser against the real backend:
//
// sign in -> open a workflow -> add nodes -> hover a node and read what it
// does -> configure a field -> insert a variable with the picker -> connect
// nodes -> validate -> see the problems -> click one -> its node is selected
// and brought into view; plus the editor keyboard (Esc, select all, copy,
// paste, undo).

const seed = (): Seed => JSON.parse(process.env.E2E_SEED ?? "null");
const api = (path: string) => `http://127.0.0.1:${API_PORT}/api/v1${path}`;
const node = (page: Page, type: string) => page.locator(`[data-node-type="${type}"]`);
const handle = (page: Page, type: string, port: string) => node(page, type).locator(`.react-flow__handle[data-handleid="${port}"]`);

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

test("hover info, configuration, variable picker and validation focus in the editor", async ({ page, request }) => {
  const s = seed();
  // A fresh workflow of its own (the seeded one belongs to the Phase 13 test).
  const login = await (await request.post(api("/auth/login"), { data: { email: s.email, password: s.password } })).json();
  const headers = { Authorization: `Bearer ${login.access_token}` };
  const me = await (await request.get(api("/auth/me"), { headers })).json();
  const project = await (await request.post(api("/projects"), { headers, data: { workspace_id: me.workspaces[0].id, name: "Phase A" } })).json();
  const wf = await (await request.post(api("/workflows"), { headers, data: { project_id: project.id, name: "Editor UX" } })).json();

  // --- sign in (redesigned page) --------------------------------------------
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.getByLabel("Email").fill(s.email);
  const password = page.getByLabel("Password");
  await password.fill(s.password);
  await expect(password).toHaveAttribute("type", "password");
  await page.getByRole("button", { name: "Show" }).click();
  await expect(password).toHaveAttribute("type", "text");
  await page.getByRole("button", { name: "Hide" }).click();
  await expect(password).toHaveAttribute("type", "password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/workflows$/);

  // --- open the workflow: empty state ------------------------------------------
  await page.goto(`/workflows/${wf.id}/editor`);
  const empty = page.getByTestId("canvas-empty");
  await expect(empty).toContainText("Build your workflow");
  await empty.getByRole("button", { name: "+ Add Input node" }).click();
  await expect(node(page, "input")).toHaveCount(1);
  await expect(empty).toHaveCount(0);

  // --- add nodes -----------------------------------------------------------------
  const canvas = page.getByTestId("canvas");
  const box = (await canvas.boundingBox())!;
  await page.getByTestId("palette-prompt").dragTo(canvas, { targetPosition: { x: box.width * 0.5, y: 120 } });
  await page.getByTestId("palette-llm").dragTo(canvas, { targetPosition: { x: box.width * 0.8, y: 120 } });
  await expect(node(page, "prompt")).toHaveCount(1);
  await expect(node(page, "llm")).toHaveCount(1);
  await page.getByRole("button", { name: "Fit View" }).click();

  // --- hover a node: what it does, from the backend definition ----------------
  const llmHeader = node(page, "llm").locator(".wf-node-header");
  const hb = await settledBox(llmHeader);
  await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
  const info = page.getByTestId("node-hover");
  await expect(info).toBeVisible();
  await expect(info).toContainText("Executes generative text inference");
  await expect(info).toContainText("Inputs");
  await expect(info).toContainText("prompt");
  await expect(info).toContainText("Outputs");
  await expect(info).toContainText("response");
  await expect(info).toContainText("workspace credential");
  // It never blocks the canvas, and goes away when the pointer leaves.
  await expect(info).toHaveCSS("pointer-events", "none");
  await page.mouse.move(box.x + 10, box.y + box.height - 10);
  await expect(info).toHaveCount(0);

  // --- connect: Input -> Prompt (the LLM's prompt input stays unconnected) ----
  await connect(page, handle(page, "input", "data"), handle(page, "prompt", "variables"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(1);

  // --- configure: variable picker in the prompt template -----------------------
  await node(page, "prompt").locator(".wf-node-header").click();
  const panel = page.getByTestId("config-panel");
  await expect(panel.getByRole("heading", { name: "Prompt" })).toBeVisible();
  await expect(panel).toContainText("Prompt template with placeholder expressions");
  const template = panel.locator("#cfg-template");
  await template.click();
  await template.pressSequentially("Summarize: {{inp");
  const picker = page.getByTestId("variable-picker");
  await expect(picker).toContainText("Workflow input");
  await expect(picker).toContainText("Previous nodes");
  await picker.getByRole("option", { name: /Input › data/ }).click();
  await expect(picker).toHaveCount(0);
  await expect(template).toHaveValue(/^Summarize: \{\{input_[0-9a-f]{4}\.data\}\}$/);
  await panel.locator("#cfg-node-name").click(); // blur commits the template

  // --- configure the LLM: definition-driven fields -----------------------------
  await node(page, "llm").locator(".wf-node-header").click();
  await expect(panel.getByRole("heading", { name: "LLM" })).toBeVisible();
  await expect(panel.getByText("Which credential should this node use?")).toBeVisible();
  await expect(panel.getByLabel("Max tokens")).toHaveAttribute("type", "number");
  await panel.locator("#cfg-model").fill("gpt-5-mini");
  await panel.locator("#cfg-model").press("Enter");
  await expect(panel.locator("#cfg-model")).toHaveValue("gpt-5-mini");

  // --- validate: problems are explained and located -----------------------------
  await page.getByRole("button", { name: "Validate", exact: true }).click();
  const summary = page.getByTestId("validation-summary");
  await expect(summary).toContainText("problem");
  const list = page.getByTestId("validation-errors");
  await expect(list).toContainText('Input "prompt" needs a connection.');
  await expect(list).toContainText("The workflow has no final node.");
  await expect(node(page, "llm")).toHaveClass(/invalid/);
  await expect(node(page, "llm").locator(".wf-node-badge")).toBeVisible();
  await expect(node(page, "llm").locator(".wf-node-errors")).toContainText('Input "prompt" needs a connection.');
  await expect(node(page, "llm").locator('.wf-port[data-port="prompt"]')).toHaveClass(/port-error/);

  // Clicking a problem selects its node and brings it into view.
  await page.locator(".react-flow__pane").click({ position: { x: 10, y: 10 } });
  await expect(panel).toHaveCount(0);
  await list.getByRole("button", { name: /Input "prompt" needs a connection/ }).click();
  await expect(panel.getByRole("heading", { name: "LLM" })).toBeVisible();
  await expect(node(page, "llm")).toHaveClass(/selected/);
  await expect(node(page, "llm")).toBeInViewport();
  await expect(panel.getByTestId("panel-issues")).toContainText('Input "prompt" needs a connection.');
  // Selection only: the edits made before are intact.
  await expect(panel.locator("#cfg-model")).toHaveValue("gpt-5-mini");
  await node(page, "prompt").locator(".wf-node-header").click();
  await expect(panel.locator("#cfg-template")).toHaveValue(/\{\{input_[0-9a-f]{4}\.data\}\}/);

  // Fix it: connect Prompt -> LLM.
  await connect(page, handle(page, "prompt", "prompt"), handle(page, "llm", "prompt"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(2);

  // --- keyboard: Esc, select all, copy, paste, undo -------------------------------
  await page.keyboard.press("Escape");
  await expect(page.locator(".wf-node.selected")).toHaveCount(0);
  await page.keyboard.press("Control+a");
  await expect(page.locator(".wf-node.selected")).toHaveCount(3);
  await page.keyboard.press("Control+c");
  await page.keyboard.press("Control+v");
  await expect(page.locator(".wf-node")).toHaveCount(6);
  await expect(page.locator(".react-flow__edge")).toHaveCount(4);
  await page.keyboard.press("Control+z");
  await expect(page.locator(".wf-node")).toHaveCount(3);
});
