import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import type { AssistantSession, EditorLayout, Plan, Step, WorkflowEnvelope } from "../types/automation";
import type { Seed } from "./stack";

async function signIn(page: Page, request: APIRequestContext) {
  const seed = JSON.parse(process.env.E2E_SEED!) as Seed;
  const auth = await request.post("/api/v1/auth/login", { data: { email: seed.email, password: seed.password } });
  expect(auth.ok()).toBeTruthy();
  const { access_token } = await auth.json() as { access_token: string };
  const headers = { Authorization: "Bearer " + access_token };
  const me = await request.get("/api/v1/auth/me", { headers });
  expect(me.ok()).toBeTruthy();
  const { workspaces } = await me.json() as { workspaces: { id: string }[] };
  await page.goto("/login");
  await page.getByLabel("Email").fill(seed.email);
  await page.getByLabel("Password", { exact: true }).fill(seed.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/home$/);
  await expect(page.getByLabel("Run when ready")).toBeEnabled();
  await page.getByLabel("Run when ready").uncheck();
  await page.getByLabel("Spoken replies").uncheck();
  return { headers, workspace: workspaces[0].id };
}

const textStep = (id: string, name: string, source: "literal" | "step", value: string): Step => ({
  id, name, kind: "tool", tool: "value.text",
  inputs: [{ name: "value", source, ref: source === "step" ? value : "", value_json: source === "literal" ? value : "" }],
  children: [], otherwise: [], condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "",
});

async function createNumberWorkflow(request: APIRequestContext, headers: Record<string, string>, workspace: string) {
  const created = await request.post("/api/v1/assistant/workflows", { headers, data: { workspace_id: workspace, name: "Canvas numbers" } });
  expect(created.status()).toBe(201);
  const document = await created.json() as WorkflowEnvelope;
  const plan: Plan = {
    ...document.session.compilation!.plan,
    steps: [textStep("format", "Format the number", "literal", "42"), textStep("echo", "Use the formatted number", "step", "format.text")],
  };
  const updated = await request.put("/api/v1/assistant/sessions/" + document.session.id + "/plan", {
    headers, data: { revision: document.session.revision, plan },
  });
  expect(updated.status()).toBe(200);
  const session = await updated.json() as AssistantSession;
  expect(session.compilation!.ready).toBe(true);
  const positioned = await request.put("/api/v1/assistant/workflows/" + document.workflow.id + "/layout", {
    headers, data: { revision: 0, positions: { format: { x: 80, y: 80 }, echo: { x: 480, y: 80 } } },
  });
  expect(positioned.status()).toBe(200);
  return { ...document, session, layout: await positioned.json() as EditorLayout };
}

test("home keeps the canvas and bottom composer available when conversation is hidden", async ({ page, request }) => {
  await signIn(page, request);
  await expect(page.getByRole("heading", { name: "Your workflow starts here" })).toBeVisible();
  const canvas = page.getByTestId("canvas");
  const composer = page.getByLabel("What would you like to do?");
  await expect(canvas).toBeVisible();
  await expect(composer).toBeVisible();
  const bounds = await canvas.boundingBox();
  expect(bounds!.width).toBeGreaterThan(700);
  expect(bounds!.height).toBeGreaterThan(350);
  const composerBounds = await composer.boundingBox();
  expect(composerBounds!.y).toBeGreaterThan(bounds!.y + bounds!.height - 2);
  expect(composerBounds!.y + composerBounds!.height).toBeLessThanOrEqual(page.viewportSize()!.height);
  await page.getByRole("button", { name: "Hide conversation" }).click();
  await expect(page.getByRole("button", { name: "Show conversation" })).toBeVisible();
  await expect(canvas).toBeVisible();
  await expect(composer).toBeEditable();
  expect((await canvas.boundingBox())!.width).toBeGreaterThan(bounds!.width);
  await composer.fill("Keep this request while I inspect the canvas.");
  await page.getByRole("button", { name: "Show conversation" }).click();
  await expect(composer).toHaveValue("Keep this request while I inspect the canvas.");
});

test("canvas edits autosave, layout stays separate, and published versions and templates are reusable", async ({ page, request }) => {
  const { headers, workspace } = await signIn(page, request);
  const runsURL = "/api/v1/automation-runs?workspace_id=" + workspace;
  const beforeRuns = await (await request.get(runsURL, { headers })).json() as { items: { id: string }[] };
  const document = await createNumberWorkflow(request, headers, workspace);
  const sessionURL = "/api/v1/assistant/sessions/" + document.session.id;
  const workflowURL = "/api/v1/assistant/workflows/" + document.workflow.id;
  const savedSession = async () => await (await request.get(sessionURL, { headers })).json() as AssistantSession;
  const savedLayout = async () => await (await request.get(workflowURL + "/layout", { headers })).json() as EditorLayout;
  await page.goto("/workflows/" + document.workflow.id + "/editor");
  await expect(page.getByLabel("Workflow name")).toHaveValue("Canvas numbers");
  const number = page.getByTestId("node-format");
  await expect(number).toBeVisible();
  await expect(page.locator(".react-flow__edge.data")).toHaveCount(1);
  await number.getByLabel("value", { exact: true }).fill("73");
  await number.getByRole("button", { name: "Edit step" }).click();
  const inspector = page.getByRole("region", { name: "Edit Format the number", exact: true });
  await inspector.getByLabel("Step name", { exact: true }).fill("Format the saved number");
  await expect.poll(async () => {
    const saved = (await savedSession()).compilation!.plan.steps[0];
    return { name: saved.name, value: saved.inputs[0].value_json };
  }).toEqual({ name: "Format the saved number", value: "73" });
  await expect(page.locator(".save-state")).toHaveText("All changes saved");
  await page.getByRole("region", { name: "Edit Format the saved number", exact: true }).getByRole("button", { name: "Close", exact: true }).click();
  const revision = (await savedSession()).revision;
  const oldLayout = await savedLayout();
  const header = await number.locator(".wf-node-header").boundingBox();
  await page.mouse.move(header!.x + 50, header!.y + 18);
  await page.mouse.down();
  await page.mouse.move(header!.x + 178, header!.y + 114, { steps: 12 });
  await page.mouse.up();
  await expect.poll(async () => (await savedLayout()).revision).toBeGreaterThan(oldLayout.revision);
  const moved = await savedLayout();
  expect(moved.positions.format).not.toEqual(oldLayout.positions.format);
  expect((await savedSession()).revision).toBe(revision);
  await page.reload();
  await expect(page.getByTestId("node-format").getByLabel("value", { exact: true })).toHaveValue("73");
  expect((await savedLayout()).positions.format).toEqual(moved.positions.format);

  await page.getByRole("button", { name: "Versions", exact: true }).click();
  const versions = page.getByRole("dialog", { name: "Version history" });
  await versions.getByRole("button", { name: "Publish current version" }).click();
  await expect(versions.getByRole("status")).toHaveText("Version published. Nothing was run.");
  await expect(versions.getByText(/^Version 1 ·/)).toBeVisible();
  await versions.getByRole("button", { name: "Close history" }).click();
  await page.getByTestId("node-format").getByLabel("value", { exact: true }).fill("99");
  await expect.poll(async () => (await savedSession()).compilation!.plan.steps[0].inputs[0].value_json).toBe("99");
  await page.getByRole("button", { name: "Versions", exact: true }).click();
  await versions.getByRole("button", { name: "Restore as draft" }).click();
  await expect(versions.getByRole("status")).toHaveText("Version restored as your editable draft. Nothing was run.");
  await versions.getByRole("button", { name: "Close history" }).click();
  await expect(page.getByTestId("node-format").getByLabel("value", { exact: true })).toHaveValue("73");

  await page.getByRole("button", { name: "Save as template", exact: true }).click();
  const template = page.getByRole("dialog", { name: "Save as template", exact: true });
  await template.getByLabel("Template name").fill("Canvas reusable number");
  await template.getByRole("button", { name: "Save template", exact: true }).click();
  await expect(template.getByRole("status")).toHaveText("Template saved. You can reuse it from Templates.");
  await template.getByRole("button", { name: "Close history" }).click();
  await page.goto("/templates");
  await page.getByLabel("Search templates").fill("Canvas reusable number");
  await page.getByRole("button", { name: /Canvas reusable number/ }).click();
  await page.getByLabel("Name your workflow").fill("Canvas number copy");
  await page.getByTestId("use-template").click();
  await expect(page).toHaveURL(/\/workflows\/[^/]+\/editor$/);
  const copiedID = new URL(page.url()).pathname.split("/")[2];
  expect(copiedID).not.toBe(document.workflow.id);
  await expect(page.getByLabel("Workflow name")).toHaveValue("Canvas number copy");
  await expect(page.getByTestId("node-format").getByLabel("value", { exact: true })).toHaveValue("73");
  const copied = await (await request.get("/api/v1/assistant/workflows/" + copiedID, { headers })).json() as WorkflowEnvelope;
  expect(copied.layout.positions.format).toEqual(moved.positions.format);
  const afterRuns = await (await request.get(runsURL, { headers })).json() as { items: { id: string }[] };
  expect(afterRuns.items.map(run => run.id).sort()).toEqual(beforeRuns.items.map(run => run.id).sort());
});

test("AI build checkpoints stay visible while manual controls lock and the composer buffers text", async ({ page, request }) => {
  const { headers, workspace } = await signIn(page, request);
  const document = await createNumberWorkflow(request, headers, workspace);
  await page.goto("/workflows/" + document.workflow.id + "/editor");
  await page.getByLabel("Run when ready").uncheck();
  await page.getByLabel("Spoken replies").uncheck();
  await page.getByTestId("node-format").getByRole("button", { name: "Edit step" }).click();
  const checkpoint: Plan = {
    ...document.session.compilation!.plan,
    steps: [...document.session.compilation!.plan.steps, textStep("checkpoint", "Checked draft checkpoint", "literal", "150")],
  };
  await page.route("**/api/v1/assistant/sessions/" + document.session.id + "/build", route => route.fulfill({ json: {
    status: "building", phase: "checking", pass: 2, revision: document.session.revision,
    request_id: "canvas-checkpoint", plan: checkpoint, updated_at: new Date().toISOString(),
  } }));
  let release = () => {};
  const gate = new Promise<void>(resolve => { release = resolve; });
  await page.route("**/api/v1/assistant/sessions/" + document.session.id + "/messages", async route => {
    await gate;
    await route.continue();
  });
  const composer = page.getByLabel("What would you like to do?");
  await composer.fill("Add a checked step to this workflow.");
  const response = page.waitForResponse(res => res.request().method() === "POST" && res.url().endsWith("/" + document.session.id + "/messages"));
  await page.getByRole("button", { name: /Make it happen/ }).click();
  try {
    await expect(page.getByText("Checking the generated steps · pass 2", { exact: false })).toBeVisible();
    await expect(page.getByTestId("node-checkpoint")).toBeAttached();
    await expect(page.getByLabel("Workflow name")).toBeDisabled();
    await expect(page.getByRole("region", { name: "Edit Format the number", exact: true }).getByLabel("Step name")).toBeDisabled();
    await expect(page.getByTestId("node-format").getByLabel("value", { exact: true })).toBeDisabled();
    const palette = page.locator(".plan-palette .palette-tile");
    await expect(palette.first()).toBeVisible();
    for (const tile of await palette.all()) await expect(tile).toBeDisabled();
    await expect(page.getByRole("button", { name: "Arrange", exact: true })).toBeDisabled();
    await composer.fill("Keep this next instruction until the build finishes.");
    await expect(composer).toBeEditable();
    await expect(page.getByRole("button", { name: /Make it happen/ })).toBeDisabled();
    await page.getByRole("button", { name: "Hide conversation" }).click();
    await expect(composer).toBeVisible();
  } finally {
    release();
  }
  expect((await response).status()).toBe(200);
  await expect(page.getByLabel("Workflow name")).toBeEnabled();
  await expect(composer).toHaveValue("Keep this next instruction until the build finishes.");
  await expect(page.getByRole("button", { name: /Make it happen/ })).toBeEnabled();
});
