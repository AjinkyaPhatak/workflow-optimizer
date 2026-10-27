import { test, expect, type Page, type WebSocketRoute } from "@playwright/test";
import { type Seed } from "./stack";

test.use({ permissions: ["microphone"], launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream"] } });
async function login(page: Page) {
  const seed = JSON.parse(process.env.E2E_SEED!) as Seed;
  await page.goto("/login");
  await page.getByLabel("Email").fill(seed.email);
  await page.getByLabel("Password", { exact: true }).fill(seed.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/home$/);
}

test("workspace and send failures preserve text and offer recovery", async ({ page }) => {
  let workspaceAvailable = false;
  let voiceCalls = 0;
  await page.route("**/api/v1/auth/me", route => workspaceAvailable ? route.continue() : route.fulfill({ status: 503,
    json: { error: { code: "SERVICE_UNAVAILABLE", message: "Workspace temporarily unavailable" } } }));
  await page.route("**/api/v1/voice/token", route => { voiceCalls++; return route.fulfill({ status: 503,
    json: { error: { code: "SERVICE_UNAVAILABLE", message: "Voice unavailable" } } }); });
  await login(page);
  const input = page.getByLabel("What would you like to do?");
  await input.fill("Make my report");
  await input.press("Enter");
  await expect(input).toHaveValue("Make my report");
  await expect(page.getByRole("button", { name: "Start microphone" })).toBeDisabled();
  expect(voiceCalls).toBe(0);
  workspaceAvailable = true;
  await page.getByRole("button", { name: "Retry loading workspace" }).click();
  await expect(page.getByRole("button", { name: "Start microphone" })).toBeEnabled();
  await page.getByRole("button", { name: "Start microphone" }).click();
  await expect(page.locator(".voice-composer").getByRole("alert")).toContainText("Voice is unavailable on the server");
  await expect(input).toHaveValue("Make my report");
  let sendFails = true;
  await page.route("**/assistant/sessions/*/messages", route => sendFails ? route.fulfill({ status: 503,
    json: { error: { code: "SERVICE_UNAVAILABLE", message: "Please retry your request" } } }) : route.continue());
  await page.getByLabel("Run when ready").uncheck();
  await input.press("Enter");
  await expect(page.locator(".error-banner").filter({ hasText: "Please retry" })).toBeVisible();
  await expect(input).toHaveValue("Make my report");
  sendFails = false;
  await page.getByRole("button", { name: "Make it happen" }).click();
  await expect(page.getByRole("heading", { name: "Nifty 50 daily report" })).toBeVisible();
  await expect(input).toHaveValue("");
});

test("real voice client keeps later transcripts in the current session and recovers after quota failure", async ({ page }) => {
  let socket: WebSocketRoute | undefined;
  await page.routeWebSocket(/api\.elevenlabs\.io/, ws => {
    socket = ws;
    ws.send(JSON.stringify({ message_type: "session_started", session_id: "mock-voice" }));
  });
  await page.route("**/api/v1/voice/token", route => route.fulfill({ json: { token: "mock-single-use-token" } }));
  await login(page);
  await page.getByLabel("Run when ready").uncheck();
  await page.getByLabel("Spoken replies").uncheck();
  const messageURLs: string[] = [];
  page.on("request", req => { if (req.method() === "POST" && /\/sessions\/[^/]+\/messages$/.test(req.url())) messageURLs.push(req.url()); });
  await page.getByRole("button", { name: "Start microphone" }).click();
  await expect(page.getByRole("button", { name: "Stop microphone" })).toBeVisible();
  socket!.send(JSON.stringify({ message_type: "committed_transcript", text: "Make a market report" }));
  await expect(page.getByRole("heading", { name: "Nifty 50 daily report" })).toBeVisible();
  const firstSession = new URL(page.url()).searchParams.get("session");
  socket!.send(JSON.stringify({ message_type: "committed_transcript", text: "Include the average too" }));
  await expect(page.locator(".assistant-message.user").filter({ hasText: "Include the average too" })).toBeVisible();
  await expect.poll(() => messageURLs.length).toBe(2);
  expect(new Set(messageURLs).size).toBe(1);
  expect(new URL(page.url()).searchParams.get("session")).toBe(firstSession);
  socket!.send(JSON.stringify({ message_type: "quota_exceeded", error: "quota exceeded" }));
  await expect(page.locator(".voice-composer").getByRole("alert")).toContainText("credits have run out");
  const input = page.getByLabel("What would you like to do?");
  await input.fill("Continue with text");
  await expect(input).toHaveValue("Continue with text");
  await expect(page.getByRole("button", { name: "Make it happen" })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Start microphone" })).toBeEnabled();
});

test("a browser session whose account is gone returns to sign in", async ({ page }) => {
  await page.route("**/api/v1/auth/me", route => route.fulfill({ status: 401,
    json: { error: { code: "UNAUTHENTICATED", message: "Your session is no longer valid. Please sign in again." } } }));
  await page.goto("/login");
  const seed = JSON.parse(process.env.E2E_SEED!) as Seed;
  await page.getByLabel("Email").fill(seed.email);
  await page.getByLabel("Password", { exact: true }).fill(seed.password);
  const profile = page.waitForResponse("**/api/v1/auth/me");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  expect((await profile).status()).toBe(401);
  await expect(page).toHaveURL(/\/login$/);
  expect(await page.evaluate(() => localStorage.getItem("workflow-optimizer.session"))).toBeNull();
});

test("microphone permission failure keeps typed text and allows another attempt", async ({ page }) => {
  await page.addInitScript(() => {
    navigator.mediaDevices.getUserMedia = async () => { throw new DOMException("Permission denied", "NotAllowedError"); };
  });
  await page.routeWebSocket(/api\.elevenlabs\.io/, ws => {
    ws.send(JSON.stringify({ message_type: "session_started", session_id: "mock-voice-denied" }));
  });
  await page.route("**/api/v1/voice/token", route => route.fulfill({ json: { token: "mock-single-use-token" } }));
  await login(page);
  const input = page.getByLabel("What would you like to do?");
  await input.fill("Keep this request");
  await page.getByRole("button", { name: "Start microphone" }).click();
  await expect(page.locator(".voice-composer").getByRole("alert")).toContainText("Microphone access is blocked");
  await expect(input).toHaveValue("Keep this request");
  await expect(page.getByRole("button", { name: "Start microphone" })).toBeEnabled();
  await input.press("End");
  await input.pressSequentially(" and let me type");
  await expect(input).toHaveValue("Keep this request and let me type");
});
