import { expect, test } from "@playwright/test";
import type { Seed } from "./stack";

test("account settings keep Google setup and existing settings accessible", async ({ page }) => {
  const seed = JSON.parse(process.env.E2E_SEED!) as Seed;
  await page.goto("/login");
  await page.getByLabel("Email").fill(seed.email);
  await page.getByLabel("Password", { exact: true }).fill(seed.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page).toHaveURL(/\/home$/);
  await page.getByTestId("account-link").click();
  for (const name of ["Connected accounts", "AI keys", "Account", "Usage", "WhatsApp"]) {
    await expect(page.getByRole("menuitem").filter({ has: page.locator("span", { hasText: new RegExp("^" + name + "$") }) })).toBeVisible();
  }
  await page.getByRole("menuitem", { name: /Connected accounts/ }).click();
  const google = page.getByTestId("provider-google");
  await expect(google).toBeVisible();
  await google.getByRole("button", { name: "Connect Google", exact: true }).click();
  const setup = page.getByRole("region", { name: "Google connection setup" });
  await expect(setup).toContainText("GOOGLE_CLIENT_ID");
  await expect(setup).toContainText("GOOGLE_CLIENT_SECRET");
  await expect(setup).toContainText(new URL(page.url()).origin + "/api/v1/oauth/callback/google");
  await page.screenshot({ path: "../.local/connected-accounts.png", fullPage: true });
  for (const provider of ["notion", "airtable", "discord"]) await expect(page.getByTestId("provider-" + provider)).toBeVisible();
  await expect(page.getByRole("link", { name: "Connect WhatsApp →" })).toHaveAttribute("href", "/settings/whatsapp");
  await page.reload();
  await expect(page.getByTestId("provider-google")).toBeVisible();
  await page.goto("/files");
  await expect(page).toHaveURL(/\/knowledge\/files$/);
  await expect(page.getByRole("navigation", { name: "Knowledge sections" }).getByRole("link", { name: "Files & outputs" })).toHaveAttribute("aria-current", "page");
});
