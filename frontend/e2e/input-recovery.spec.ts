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
  await expect(page.getByRole("button", { name: "Hold to speak" })).toBeDisabled();
  expect(voiceCalls).toBe(0);
  workspaceAvailable = true;
  await page.getByRole("button", { name: "Retry loading workspace" }).click();
  await expect(page.getByRole("button", { name: "Hold to speak" })).toBeEnabled();
  await page.getByRole("button", { name: "Hold to speak" }).focus();
  await page.keyboard.down("Space");
  await expect(page.locator(".voice-composer").getByRole("alert")).toContainText("Voice is unavailable on the server");
  await page.keyboard.up("Space");
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
  await expect(page.getByRole("textbox", { name: "Workflow name", exact: true })).toHaveValue("Nifty 50 daily report");
  await expect(input).toHaveValue("");
});

test("real voice client keeps later transcripts in the current session and recovers after quota failure", async ({ page }) => {
  let socket: WebSocketRoute | undefined;
  let utterance = "Make a market report";
  let commits = 0;
  let closes = 0;
  await page.routeWebSocket(/api\.elevenlabs\.io/, ws => {
    socket = ws;
    expect(new URL(ws.url()).searchParams.get("commit_strategy")).toBe("manual");
    ws.onMessage(message => {
      const data = JSON.parse(String(message)) as { commit?: boolean };
      if (data.commit) { commits++; ws.send(JSON.stringify({ message_type: "committed_transcript", text: utterance })); }
    });
    ws.onClose(() => { closes++; });
    ws.send(JSON.stringify({ message_type: "session_started", session_id: "mock-voice" }));
  });
  await page.route("**/api/v1/voice/token", route => route.fulfill({ json: { token: "mock-single-use-token" } }));
  await login(page);
  await page.getByLabel("Run when ready").uncheck();
  await page.getByLabel("Spoken replies").uncheck();
  const messageURLs: string[] = [];
  page.on("request", req => { if (req.method() === "POST" && /\/sessions\/[^/]+\/messages$/.test(req.url())) messageURLs.push(req.url()); });
  await page.getByRole("button", { name: "Hold to speak" }).focus();
  await page.keyboard.down("Space");
  await expect(page.getByRole("button", { name: "Release to send", exact: true })).toBeVisible();
  socket!.send(JSON.stringify({ message_type: "partial_transcript", text: "Make a market report" }));
  expect(commits).toBe(0);
  await page.keyboard.up("Space");
  await expect(page.getByRole("textbox", { name: "Workflow name", exact: true })).toHaveValue("Nifty 50 daily report");
  const firstSession = new URL(page.url()).searchParams.get("session");
  await expect.poll(() => closes).toBe(1);
  utterance = "Include the average too";
  await page.getByRole("button", { name: "Hold to speak" }).focus();
  await page.keyboard.down("Enter");
  await expect(page.getByRole("button", { name: "Release to send", exact: true })).toBeVisible();
  await page.keyboard.up("Enter");
  await expect(page.locator(".assistant-message.user").filter({ hasText: "Include the average too" })).toBeVisible();
  await expect.poll(() => messageURLs.length).toBe(2);
  expect(new Set(messageURLs).size).toBe(1);
  expect(new URL(page.url()).searchParams.get("session")).toBe(firstSession);
  await expect.poll(() => closes).toBe(2);
  expect(commits).toBe(2);
  await page.getByRole("button", { name: "Hold to speak" }).focus();
  await page.keyboard.down("Space");
  await expect(page.getByRole("button", { name: "Release to send", exact: true })).toBeVisible();
  socket!.send(JSON.stringify({ message_type: "quota_exceeded", error: "quota exceeded" }));
  await page.keyboard.up("Space");
  await expect(page.locator(".voice-composer").getByRole("alert")).toContainText("credits have run out");
  const input = page.getByLabel("What would you like to do?");
  await input.fill("Continue with text");
  await expect(input).toHaveValue("Continue with text");
  await expect(page.getByRole("button", { name: "Make it happen" })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Hold to speak" })).toBeEnabled();
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
  await page.getByRole("button", { name: "Hold to speak" }).focus();
  await page.keyboard.down("Space");
  await expect(page.locator(".voice-composer").getByRole("alert")).toContainText("Microphone access is blocked");
  await page.keyboard.up("Space");
  await expect(input).toHaveValue("Keep this request");
  await expect(page.getByRole("button", { name: "Hold to speak" })).toBeEnabled();
  await input.press("End");
  await input.pressSequentially(" and let me type");
  await expect(input).toHaveValue("Keep this request and let me type");
});

test("releasing while a voice token is pending cancels without opening a microphone", async ({ page }) => {
  let provideToken!: () => void;
  const tokenReady = new Promise<void>(resolve => { provideToken = resolve; });
  let tokenCalls = 0;
  let sockets = 0;
  await page.route("**/api/v1/voice/token", async route => {
    tokenCalls++; await tokenReady;
    await route.fulfill({ json: { token: "mock-cancelled-token" } }).catch(() => {});
  });
  await page.routeWebSocket(/api\.elevenlabs\.io/, () => { sockets++; });
  await page.addInitScript(() => {
    const original = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
    Object.assign(window, { microphoneRequests: 0 });
    navigator.mediaDevices.getUserMedia = constraints => {
      (window as unknown as { microphoneRequests: number }).microphoneRequests++;
      return original(constraints);
    };
  });
  await login(page);
  const input = page.getByLabel("What would you like to do?");
  await input.fill("Keep typing available");
  const microphone = page.getByRole("button", { name: "Hold to speak" });
  await microphone.hover();
  await page.mouse.down();
  await expect(page.getByRole("button", { name: "Connecting microphone. Release to cancel" })).toBeVisible();
  await expect.poll(() => tokenCalls).toBe(1);
  await page.mouse.up();
  await expect(microphone).toBeVisible();
  provideToken();
  await input.fill("Still editable after release");
  expect(sockets).toBe(0);
  expect(await page.evaluate(() => (window as unknown as { microphoneRequests: number }).microphoneRequests)).toBe(0);
  await expect(input).toHaveValue("Still editable after release");
});
