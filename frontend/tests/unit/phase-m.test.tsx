// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AccountSettings } from "@/components/settings/Account";
import { fillDays, formatBytes, UsageView } from "@/components/settings/Usage";
import { accountApi, authApi } from "@/lib/api";
import { useSession } from "@/lib/auth/session";
import type { Usage } from "@/types/api";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const usage: Usage = {
  since: "2026-10-17T00:00:00Z", days: 7,
  runs: { total: 5, completed: 3, failed: 1, cancelled: 0, active: 1 },
  daily: [{ date: "2026-10-18", completed: 2, failed: 1, cancelled: 0 }, { date: "2026-10-23", completed: 1, failed: 0, cancelled: 0 }],
  ai: { steps: 4, input_tokens: 900, output_tokens: 300, total_tokens: 1200 },
  app_steps: 6,
  workflows: [{ workflow_id: "w1", name: "Inbox summary", runs: 4, failed: 1, last_run_at: new Date().toISOString() }],
  files: { count: 2, bytes: 2048 },
  live_workflows: 3,
};

describe("usage", () => {
  it("fills every day of the period and formats sizes", () => {
    const days = fillDays(usage);
    expect(days).toHaveLength(7);
    expect(days[0]).toEqual({ date: "2026-10-17", completed: 0, failed: 0, cancelled: 0 });
    expect(days[1].completed).toBe(2);
    expect(days[6].date).toBe("2026-10-23");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
    expect(formatBytes(30 * 1024 * 1024)).toBe("30 MB");
  });

  it("shows the numbers in plain words", () => {
    render(<UsageView usage={usage} />);
    expect(screen.getByText("75%")).toBeTruthy(); // 3 of 4 finished runs worked
    expect(screen.getByText("1 still running or waiting")).toBeTruthy();
    expect(screen.getByText("1,200 tokens")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Inbox summary" }).getAttribute("href")).toBe("/workflows/w1/executions");
    expect(screen.getByTestId("daily-runs")).toBeTruthy();
  });
});

describe("account settings", () => {
  it("saves the name into the session and checks the new password twice", async () => {
    useSession.setState({ session: { token: "t", expiresAt: new Date(Date.now() + 3600_000).toISOString(), user: { id: "u1", email: "ada@example.test", name: "Ada" } } });
    vi.spyOn(authApi, "me").mockResolvedValue({ user: { id: "u1", email: "ada@example.test", name: "Ada" }, workspaces: [{ id: "ws1", name: "Ada's workspace", role: "member" }] });
    const update = vi.spyOn(accountApi, "updateProfile").mockResolvedValue({ id: "u1", email: "ada@example.test", name: "Ada L" });
    const change = vi.spyOn(accountApi, "changePassword").mockResolvedValue(undefined);
    render(<AccountSettings />);

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Ada L" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Save name" }));
    });
    expect(update).toHaveBeenCalledWith("Ada L");
    expect(useSession.getState().session?.user.name).toBe("Ada L");

    fireEvent.change(screen.getByLabelText("Current password"), { target: { value: "old-password" } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "new-password-1" } });
    fireEvent.change(screen.getByLabelText("New password again"), { target: { value: "new-password-2" } });
    expect(screen.getByRole("alert").textContent).toContain("different");
    expect((screen.getByRole("button", { name: "Change password" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("New password again"), { target: { value: "new-password-1" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    });
    expect(change).toHaveBeenCalledWith("old-password", "new-password-1");

    // A member cannot rename the workspace.
    const wsName = await screen.findByLabelText("Workspace name");
    expect((wsName as HTMLInputElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: "Save workspace name" })).toBeNull();
  });
});
