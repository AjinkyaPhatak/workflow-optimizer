import { expect, test, type Locator, type Page } from "@playwright/test";
import { type Seed } from "./stack";

// Phase B acceptance path in a real browser against the real backend (Go API
// + worker + PostgreSQL + Redis; only OpenAI is mocked, answering JSON when
// the prompt asks for it):
//
// sign in -> create a workflow from the "Structured AI Response" template ->
// define a workflow variable -> use it in the Prompt Template via the picker
// -> configure the LLM (model option, credential, system prompt) -> check the
// Structured Output schema -> search the palette and add nodes -> connect ->
// save -> publish -> run -> save a new draft -> the published version is
// unchanged.

const seed = (): Seed => JSON.parse(process.env.E2E_SEED ?? "null");
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

async function selectNode(page: Page, type: string) {
  await node(page, type).locator(".wf-node-header").click();
}

test("template, variables, prompt template, LLM, structured output, search and versions", async ({ page, request }) => {
  const s = seed();

  // --- sign in and create a workflow from a template ------------------------
  await page.goto("/login");
  await page.getByLabel("Email").fill(s.email);
  await page.getByLabel("Password").fill(s.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/home$/);
  await page.goto("/workflows");

  // Phase K: templates live in the gallery (Advanced shows with technical
  // details on, as in this spec).
  await page.getByTestId("new-workflow").getByTestId("browse-templates").click();
  await expect(page).toHaveURL(/\/templates$/);
  await page.getByRole("tab", { name: "Advanced" }).click();
  await page.getByTestId("template-structured-ai-response").click();
  const details = page.getByTestId("template-details");
  await expect(details).toContainText("What it does");
  // The gallery now creates compiler workflows. Keep this regression focused
  // on the retained legacy editor/version contract; the canvas suite covers
  // using compiler templates through the gallery.
  const legacyToken = await page.evaluate(() => JSON.parse(localStorage.getItem("workflow-optimizer.session")!).token as string);
  const legacyHeaders = { Authorization: `Bearer ${legacyToken}` };
  const legacyMe = await (await request.get("/api/v1/auth/me", { headers: legacyHeaders })).json();
  const legacyProjects = await (await request.get("/api/v1/projects?workspace_id=" + legacyMe.workspaces[0].id, { headers: legacyHeaders })).json();
  const legacyCreated = await request.post("/api/v1/workflows", { headers: legacyHeaders, data: { project_id: legacyProjects.items[0].id, name: "Meeting Summarizer", template_id: "structured-ai-response" } });
  expect(legacyCreated.status()).toBe(201);
  const workflowId = (await legacyCreated.json()).id;
  await page.goto(`/workflows/${workflowId}/editor`);
  await expect(page.getByTestId("version-status")).toHaveText("v1 · Draft");
  for (const type of ["input", "prompt", "llm", "structured_output", "output"]) await expect(node(page, type)).toHaveCount(1);
  await expect(page.locator(".react-flow__edge")).toHaveCount(4);

  // --- define a workflow variable ---------------------------------------------
  await page.locator(".react-flow__pane").click({ position: { x: 10, y: 10 } });
  const variables = page.getByTestId("variables-editor");
  await expect(variables.getByTestId("variable-0")).toContainText("{{audience}}"); // from the template
  await variables.getByRole("button", { name: "+ Add variable" }).click();
  await variables.locator("#var-1-name").fill("tone");
  await variables.locator("#var-1-name").blur();
  await variables.locator("#var-1-default").fill("friendly");
  await variables.locator("#var-1-default").blur();
  await variables.locator("#var-1-description").fill("How the summary should sound");
  await variables.locator("#var-1-description").blur();
  await expect(variables.getByTestId("variable-1")).toContainText("{{tone}}");

  // --- Prompt Template: insert the variable with the picker -------------------
  await selectNode(page, "prompt");
  const panel = page.getByTestId("config-panel");
  await expect(panel.getByRole("heading", { name: "Prompt Template" })).toBeVisible();
  const template = panel.locator("#cfg-template");
  await expect(template).toHaveValue(/Summarize the text below for \{\{audience\}\}/);
  await template.click();
  await template.press("Control+End");
  await template.pressSequentially("\nUse a {{to");
  const picker = page.getByTestId("variable-picker");
  await expect(picker).toContainText("Workflow variables");
  await picker.getByRole("option", { name: /\{\{tone\}\}/ }).click();
  await expect(template).toHaveValue(/Use a \{\{tone\}\}$/);
  await panel.locator("#cfg-node-name").click(); // blur commits

  // --- LLM: model from the provider's options, credential, system prompt -----
  await selectNode(page, "llm");
  await expect(panel.getByRole("heading", { name: "Ask AI" })).toBeVisible();
  // Gemini (free) is the default since Phase G; this stack mocks OpenAI.
  await panel.locator("#cfg-provider").selectOption("openai");
  await expect(panel.locator("#cfg-model option", { hasText: "GPT-5 mini" })).toHaveCount(1);
  await panel.locator("#cfg-model").selectOption("gpt-5-mini");
  await panel.locator("#cfg-credential_id").selectOption({ label: `${s.credentialName} (OpenAI)` });
  await panel.locator("#cfg-system").fill("Keep it short.");
  await panel.locator("#cfg-node-name").click();
  await expect(panel.locator("#cfg-temperature")).toHaveAttribute("max", "2");

  // --- Structured Output: the schema -------------------------------------------
  await selectNode(page, "structured_output");
  await expect(panel.locator("#cfg-schema")).toHaveValue(/"action_items": \[\s*"string"\s*\]/);

  // --- search the palette: add a Transform, replace the Output -----------------
  const search = page.getByLabel("Search nodes");
  await search.fill("reshape");
  await expect(page.getByTestId("palette-count")).toHaveText("1 node");
  await search.press("Enter");
  await expect(node(page, "transform")).toHaveCount(1);
  await search.press("Escape");
  await expect(search).toHaveValue("");

  await selectNode(page, "output");
  await page.keyboard.press("Delete");
  await expect(node(page, "output")).toHaveCount(0);
  await search.fill("outp");
  await search.press("Enter");
  await expect(node(page, "output")).toHaveCount(1);
  await page.getByRole("button", { name: "Fit View" }).click();

  await connect(page, handle(page, "structured_output", "output"), handle(page, "transform", "input"));
  await connect(page, handle(page, "transform", "output"), handle(page, "output", "value"));
  await expect(page.locator(".react-flow__edge")).toHaveCount(5);
  await selectNode(page, "transform");
  await panel.locator("#cfg-path").fill("summary");
  await panel.locator("#cfg-path").press("Enter");

  // --- saved automatically into the draft (Phase I), publish ------------------------
  await expect(page.getByTestId("save-state")).toHaveText("All changes saved");
  await expect(page.getByTestId("version-status")).toHaveText("v1 · Draft");
  await page.getByRole("button", { name: "Publish" }).click();
  await expect(page.getByTestId("version-status")).toHaveText("v1 · Published");

  const token = await page.evaluate(() => JSON.parse(localStorage.getItem("workflow-optimizer.session")!).token as string);
  const auth = { Authorization: `Bearer ${token}` };
  const wf = await (await request.get(`/api/v1/workflows/${workflowId}`, { headers: auth })).json();
  const published = await (await request.get(`/api/v1/workflows/${workflowId}/versions/${wf.active_version_id}`, { headers: auth })).json();
  expect(published.version_number).toBe(1);
  expect(published.status).toBe("PUBLISHED");
  const def = published.definition;
  expect(def.variables.map((v: { name: string }) => v.name)).toEqual(["audience", "tone"]);
  expect(def.variables[1]).toMatchObject({ type: "string", default: "friendly", description: "How the summary should sound" });
  const llm = def.nodes.find((n: { type: string }) => n.type === "llm");
  expect(llm.config).toMatchObject({ model: "gpt-5-mini", credential_id: s.credentialId, system: "Keep it short." });
  expect(def.nodes.find((n: { type: string }) => n.type === "prompt").config.template).toMatch(/Use a \{\{tone\}\}$/);
  expect(JSON.stringify(def)).not.toMatch(/sk-e2e/);

  // --- run: variables are run fields; the result is structured ----------------------
  await page.getByRole("tab", { name: "Run" }).click(); // the header opens the Run window since Phase I
  const run = page.locator(".bottom-content:not([hidden])");
  await run.getByRole("button", { name: "Reset fields" }).click();
  await expect(run.getByLabel("Input tone")).toHaveAttribute("placeholder", "default: friendly");
  await run.getByLabel("Input query").fill("The launch moved to May; sales needs to know.");
  await run.getByRole("button", { name: "Execute" }).click();
  await expect(page.getByTestId("execution-status")).toHaveText("COMPLETED", { timeout: 60_000 });
  await expect(page.getByTestId("execution-output")).toContainText("Launch moved to May");

  // --- a new draft; the published version stays as it was ----------------------------
  await selectNode(page, "prompt");
  await panel.locator("#cfg-template").fill("A different prompt about {{input.query}}. Respond with JSON.");
  await panel.locator("#cfg-node-name").click();
  // A published version never changes: the change is saved as a new draft.
  await expect(page.getByTestId("version-status")).toHaveText("v2 · Draft");

  await page.getByTestId("version-status").click();
  const history = page.getByTestId("versions-panel");
  await expect(history.getByTestId("version-2-state")).toHaveText("Draft");
  await expect(history.getByTestId("version-2")).toContainText("Latest draft");
  await expect(history.getByTestId("version-1-state")).toHaveText("Published · active");

  const again = await (await request.get(`/api/v1/workflows/${workflowId}/versions/${wf.active_version_id}`, { headers: auth })).json();
  expect(again.definition).toEqual(def);
  expect((await (await request.get(`/api/v1/workflows/${workflowId}`, { headers: auth })).json()).active_version_id).toBe(wf.active_version_id);

  // Opening the published version shows it again (editing it would make v3).
  await history.getByTestId("version-1").getByRole("button", { name: "Open as new draft" }).click();
  await expect(page.getByTestId("version-status")).toHaveText("v1 · Published");
  await selectNode(page, "prompt");
  await expect(panel.locator("#cfg-template")).toHaveValue(/Use a \{\{tone\}\}$/);
});
