// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AutomationCard } from "@/components/assistant/AutomationCard";
import { assistantApi } from "@/lib/api/assistant";
import type { Automation, AutomationRun, Plan } from "@/types/automation";

vi.mock("@/lib/api/assistant", () => ({ assistantApi: { automationRuns: vi.fn(), control: vi.fn() } }));
const plan: Plan = { schema_version: 2, name: "Report", summary: "", mode: "once", budget: { max_actions: 10, max_tokens: 100, max_active_seconds: 60 }, steps: [], questions: [] };
const item: Automation & { id: string } = { id: "automation-1", workspace_id: "workspace", name: "Daily report", plan, cron: "0 9 * * 1-5", timezone: "Asia/Kolkata", trigger: "", enabled: true };
const run = (id: string, status: string): AutomationRun => ({ id, workspace_id: "workspace", session_id: null, revision: 1, execution_backend: "temporal", plan, status, question: null, error: "", outputs: {}, created_at: "2026-10-28T03:30:00Z", updated_at: "2026-10-28T03:30:00Z" });
const load = vi.mocked(assistantApi.automationRuns);
const control = vi.mocked(assistantApi.control);
beforeEach(() => { vi.resetAllMocks(); load.mockResolvedValue({ items: [] }); control.mockResolvedValue(undefined); });
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("recurring automation management", () => {
  it("links the correct active and finished executions without changing the schedule", async () => {
    load.mockResolvedValue({ items: [run("running-1", "RUNNING"), run("finished-1", "COMPLETED")] });
    const toggle = vi.fn();
    render(<AutomationCard item={item} busy={false} onToggle={toggle} onDelete={vi.fn()} />);
    const open = await screen.findAllByRole("link", { name: "Open run" });
    expect(open.map(link => link.getAttribute("href"))).toEqual(["/home?run=running-1", "/home?run=finished-1"]);
    expect(screen.getByText("Active runs (1)")).toBeTruthy();
    expect(screen.getByText("Recent history (1)")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Delete automation" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Pause future runs" }));
    expect(toggle).toHaveBeenCalledOnce();
    expect(control).not.toHaveBeenCalled();
  });

  it("requests stop once, waits for execution confirmation and keeps future runs separate", async () => {
    load.mockResolvedValue({ items: [run("running-1", "RUNNING")] });
    let complete!: () => void;
    control.mockImplementation(() => new Promise<void>(resolve => { complete = resolve; }));
    const toggle = vi.fn();
    render(<AutomationCard item={item} busy={false} onToggle={toggle} onDelete={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: "Stop run" }));
    fireEvent.click(screen.getByRole("button", { name: "Stopping…" }));
    expect(control).toHaveBeenCalledExactlyOnceWith("running-1", "cancel");
    expect(toggle).not.toHaveBeenCalled();
    await act(async () => complete());
    expect(screen.getByText(/Stop requested for this run/)).toBeTruthy();
    expect(screen.getByText("running")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Stopping…" }) as HTMLButtonElement).disabled).toBe(true);
    load.mockResolvedValue({ items: [run("running-1", "CANCELLED")] });
    fireEvent.click(screen.getByRole("button", { name: "Refresh runs" }));
    await screen.findByText("No active runs.");
    expect(screen.queryByRole("button", { name: "Stopping…" })).toBeNull();
    expect(screen.getByText("cancelled")).toBeTruthy();
  });

  it("pauses only the chosen execution and provides a link for resuming its budget", async () => {
    load.mockResolvedValue({ items: [run("waiting-1", "WAITING_FOR_INPUT")] });
    render(<AutomationCard item={{ ...item, enabled: false }} busy={false} onToggle={vi.fn()} onDelete={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: "Pause run" }));
    await waitFor(() => expect(control).toHaveBeenCalledWith("waiting-1", "pause"));
    load.mockResolvedValue({ items: [run("waiting-1", "PAUSED")] });
    fireEvent.click(screen.getByRole("button", { name: "Refresh runs" }));
    await screen.findByText("paused");
    expect(screen.queryByRole("button", { name: "Pause run" })).toBeNull();
    expect(screen.getByText("Open the run to resume or renew its budget.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Enable future runs" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Stop run" })).toBeTruthy();
  });

  it("lets Stop supersede an unconfirmed pause and ignores its late failure", async () => {
    load.mockResolvedValue({ items: [run("running-1", "RUNNING")] });
    let rejectPause!: (error: Error) => void;
    control.mockImplementationOnce(() => new Promise<void>((_, reject) => { rejectPause = reject; }));
    render(<AutomationCard item={item} busy={false} onToggle={vi.fn()} onDelete={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: "Pause run" }));
    expect(screen.getByRole("button", { name: "Pausing…" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Stop run" }));
    await screen.findByText(/Stop requested for this run/);
    expect(control.mock.calls).toEqual([["running-1", "pause"], ["running-1", "cancel"]]);
    await act(async () => rejectPause(new Error("Late pause failure")));
    expect(screen.queryByRole("alert")).toBeNull();
    expect((screen.getByRole("button", { name: "Stopping…" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("keeps failed controls visible and allows a deliberate retry", async () => {
    load.mockResolvedValue({ items: [run("running-1", "RUNNING")] });
    control.mockRejectedValueOnce(new Error("Execution service unavailable"));
    render(<AutomationCard item={item} busy={false} onToggle={vi.fn()} onDelete={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: "Stop run" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Execution service unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Refresh runs" }));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("alert").textContent).toContain("Execution service unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Stop run" }));
    await waitFor(() => expect(control).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("does not present a failed history request as an empty or safely deletable automation", async () => {
    load.mockRejectedValueOnce(new Error("History unavailable"));
    render(<AutomationCard item={item} busy={false} onToggle={vi.fn()} onDelete={vi.fn()} />);
    await screen.findByRole("alert");
    expect(screen.queryByText("No active runs.")).toBeNull();
    expect((screen.getByRole("button", { name: "Delete automation" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Refresh runs" }));
    await screen.findByText("No active runs.");
  });

  it("refreshes status automatically, aborts on unmount and ignores stale responses", async () => {
    vi.useFakeTimers();
    let late!: (value: { items: AutomationRun[] }) => void;
    load.mockImplementationOnce(() => new Promise(resolve => { late = resolve; }));
    const view = render(<AutomationCard item={item} busy={false} onToggle={vi.fn()} onDelete={vi.fn()} />);
    const firstSignal = load.mock.calls[0][1]!;
    load.mockResolvedValue({ items: [run("new-1", "RUNNING")] });
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Refresh runs" })));
    expect(firstSignal.aborted).toBe(true);
    await act(async () => late({ items: [] }));
    expect(screen.getByText("Active runs (1)")).toBeTruthy();
    load.mockResolvedValue({ items: [run("new-1", "COMPLETED")] });
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.getByText("No active runs.")).toBeTruthy();
    const count = load.mock.calls.length;
    const lastSignal = load.mock.calls.at(-1)![1]!;
    view.unmount();
    await act(async () => { await vi.advanceTimersByTimeAsync(15000); });
    expect(lastSignal.aborted).toBe(true);
    expect(load).toHaveBeenCalledTimes(count);
  });
});
