// @vitest-environment jsdom
import { StrictMode } from "react";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAssistant } from "@/features/assistant/useAssistant";
import { ApiError } from "@/lib/api/client";
import type { AssistantSession } from "@/types/automation";

const api = vi.hoisted(() => Object.fromEntries([
  "tools", "get", "create", "sessions", "runs", "repair", "message", "answer", "edit", "setBudget", "start", "run", "stream",
].map(name => [name, vi.fn()])));
vi.mock("@/lib/api/assistant", () => ({ assistantApi: api }));
vi.mock("@/lib/api", () => ({
  authApi: { me: async () => ({ workspaces: [{ id: "workspace", name: "Mine", role: "owner" }] }) },
  credentialApi: { list: async () => ({ items: [] }) },
}));
function draft(id: string, revision: number, broken = true): AssistantSession {
  return { id, workspace_id: "workspace", revision, answers: { report_name: "Sales report" }, messages: [],
    compilation: { ready: !broken, diagnostics: broken ? [{ code: "REFERENCE", step_id: "calculate", message: "Missing output" }] : [],
      plan: { schema_version: 2, name: "Report", summary: "Calculate a report", mode: "once", steps: [], questions: [], budget: { max_actions: 10, max_tokens: 2000, max_active_seconds: 60 } } },
  } as unknown as AssistantSession;
}
beforeEach(() => {
  vi.resetAllMocks(); window.sessionStorage.clear();
  api.tools.mockResolvedValue({ items: [], comparisons: [] });
  api.sessions.mockResolvedValue({ items: [] }); api.runs.mockResolvedValue({ items: [] });
  api.stream.mockImplementation(() => new Promise(() => {}));
});
afterEach(cleanup);
function mount(saved: AssistantSession) {
  api.get.mockResolvedValue(saved);
  window.history.replaceState(null, "", "/home?session=" + saved.id);
  return renderHook(() => useAssistant(), { wrapper: StrictMode });
}

describe("automatic workflow doctor", () => {
  it("applies a saved intent answer once even after structural checks become clean", async () => {
    const saved = draft("intent-still-pending", 1, false);
    saved.compilation!.ready = false;
    saved.compilation!.plan.questions = [{ id: "parity", purpose: "intent", kind: "choice", prompt: "Round or truncate?", required: true, options: [], allow_other: false, step_id: "clarify", input: "answer", example: "", min: null, max: null }];
    saved.answers.parity = "nearest_whole_usd";
    api.repair.mockResolvedValue(draft("intent-still-pending", 2, false));
    const view = mount(saved);
    await waitFor(() => expect(view.result.current.session?.revision).toBe(2));
    expect(api.repair).toHaveBeenCalledTimes(1);
    expect(api.answer).not.toHaveBeenCalled();
    expect(api.start).not.toHaveBeenCalled();
  });
  it("confirms limits against the latest flushed revision without resaving a stale plan or starting it", async () => {
    const view = mount(draft("budget-confirmation", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const confirm = view.result.current.setBudget;
    const edited = draft("budget-confirmation", 2, false);
    edited.compilation!.plan.name = "My latest edits";
    api.edit.mockResolvedValue(edited);
    await act(async () => { await view.result.current.edit(edited.compilation!.plan); });
    const budget = { max_actions: 25, max_tokens: 25000, max_active_seconds: 1800 };
    const confirmed = draft("budget-confirmation", 3, false);
    confirmed.compilation!.plan = { ...edited.compilation!.plan, budget };
    api.setBudget.mockResolvedValue(confirmed);
    await act(async () => { await confirm(budget); });
    expect(api.setBudget).toHaveBeenCalledExactlyOnceWith("budget-confirmation", 2, budget);
    expect(view.result.current.session?.compilation?.plan.name).toBe("My latest edits");
    expect(api.edit).toHaveBeenCalledTimes(1);
    expect(api.start).not.toHaveBeenCalled();
    expect(api.repair).not.toHaveBeenCalled();
  });
  it.each([new TypeError("Failed to fetch"), new ApiError(504, "TIMEOUT", "Timed out", null), new ApiError(409, "CONFLICT", "Stale revision", null)])("recovers a saved answer after %s without executing it", async failure => {
    const view = mount(draft("answer-recovery", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const latest = draft("answer-recovery", 2, false);
    api.get.mockResolvedValue(latest);
    api.answer.mockRejectedValue(failure);
    const reads = api.get.mock.calls.length;
    await act(async () => { await view.result.current.answer("report_name", "Updated report"); });
    expect(api.get).toHaveBeenCalledTimes(reads + 1);
    expect(view.result.current.session?.revision).toBe(2);
    expect(view.result.current.busy).toBe(false);
    expect(view.result.current.building).toBe(false);
    expect((view.result.current.error as Error).message).toContain("latest saved workflow");
    expect(api.answer).toHaveBeenCalledTimes(1);
    expect(api.start).not.toHaveBeenCalled();
    expect(api.repair).not.toHaveBeenCalled();
  });
  it("preserves the original answer failure when the saved revision has not advanced", async () => {
    const view = mount(draft("answer-unchanged", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const failure = new TypeError("Failed to fetch");
    api.answer.mockRejectedValue(failure);
    await act(async () => { await expect(view.result.current.answer("report_name", "Updated report")).rejects.toBe(failure); });
    expect(view.result.current.session?.revision).toBe(1);
    expect(view.result.current.building).toBe(false);
    expect(api.start).not.toHaveBeenCalled();
    expect(api.repair).not.toHaveBeenCalled();
  });
  it("preserves the original failure if recovery cannot read the saved session", async () => {
    const view = mount(draft("recovery-unavailable", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const failure = new TypeError("Failed to fetch");
    api.answer.mockRejectedValue(failure);
    api.get.mockRejectedValue(new TypeError("Still offline"));
    await act(async () => { await expect(view.result.current.answer("report_name", "Updated report")).rejects.toBe(failure); });
    expect(view.result.current.session?.revision).toBe(1);
    expect(view.result.current.building).toBe(false);
  });
  it("keeps validation failures without fetching or retrying the answer", async () => {
    const view = mount(draft("answer-invalid", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const failure = new ApiError(422, "VALIDATION_ERROR", "Invalid answer", null);
    const reads = api.get.mock.calls.length;
    api.answer.mockRejectedValue(failure);
    await act(async () => { await expect(view.result.current.answer("report_name", "")).rejects.toBe(failure); });
    expect(api.get).toHaveBeenCalledTimes(reads);
    expect(api.answer).toHaveBeenCalledTimes(1);
  });
  it.each([true, false])("clears a lost send only when persisted user text matches: %s", async matches => {
    const initial = draft("send-recovery", 1, false);
    const view = mount(initial);
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const latest = draft("send-recovery", 2, false);
    latest.messages = [{ role: "user", text: matches ? "Update report" : "Another tab changed this", created_at: "" }, { role: "assistant", text: "Done", created_at: "" }];
    api.get.mockResolvedValueOnce(initial).mockResolvedValue(latest);
    api.message.mockRejectedValue(new TypeError("Failed to fetch"));
    await act(async () => { expect(await view.result.current.send("Update report")).toBe(matches); });
    expect(view.result.current.session?.revision).toBe(2);
    expect(view.result.current.building).toBe(false);
    expect(api.message).toHaveBeenCalledTimes(1);
    expect(api.start).not.toHaveBeenCalled();
    expect(api.repair).not.toHaveBeenCalled();
  });
  it("does not automatically repair a recovered broken compilation", async () => {
    const view = mount(draft("recovered-broken", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    api.get.mockResolvedValue(draft("recovered-broken", 2));
    api.answer.mockRejectedValue(new TypeError("Failed to fetch"));
    await act(async () => { await view.result.current.answer("report_name", "Updated report"); });
    expect(view.result.current.session?.revision).toBe(2);
    expect(view.result.current.doctorFailure).toBeTruthy();
    expect(api.repair).not.toHaveBeenCalled();
    expect(api.start).not.toHaveBeenCalled();
  });
  it("ignores saved recovery arriving after an explicit interruption", async () => {
    const view = mount(draft("recovery-interrupted", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    let finish!: (saved: AssistantSession) => void;
    api.get.mockImplementation(() => new Promise<AssistantSession>(resolve => { finish = resolve; }));
    api.answer.mockRejectedValue(new TypeError("Failed to fetch"));
    let answering!: Promise<void>;
    act(() => { answering = view.result.current.answer("report_name", "Updated report"); });
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    act(() => view.result.current.interrupt());
    await act(async () => { finish(draft("recovery-interrupted", 2, false)); await answering; });
    expect(view.result.current.session?.revision).toBe(1);
    expect(view.result.current.error).toBeNull();
    expect(view.result.current.building).toBe(false);
    expect(api.start).not.toHaveBeenCalled();
  });
  it("does not recover an explicitly aborted request", async () => {
    const view = mount(draft("answer-aborted", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const reads = api.get.mock.calls.length;
    api.answer.mockRejectedValue(new DOMException("Aborted", "AbortError"));
    await act(async () => { await view.result.current.answer("report_name", "Updated report"); });
    expect(api.get).toHaveBeenCalledTimes(reads);
    expect(view.result.current.building).toBe(false);
  });
  it.each(["session", "run"])("opens an empty canvas for a removed %s without treating the workspace as broken", async kind => {
    api.get.mockRejectedValue(new ApiError(404, "NOT_FOUND", "Unavailable", null));
    api.run.mockRejectedValue(new ApiError(404, "NOT_FOUND", "Unavailable", null));
    window.history.replaceState(null, "", "/home?" + kind + "=removed");
    const view = renderHook(() => useAssistant(), { wrapper: StrictMode });
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    expect(view.result.current.workspaceId).toBe("workspace");
    expect(view.result.current.workspaceError).toBeNull();
    expect(view.result.current.session).toBeNull();
    expect(view.result.current.run).toBeNull();
    expect(window.location.pathname + window.location.search).toBe("/home");
    expect(api.start).not.toHaveBeenCalled();
  });
  it("runs the latest saved revision even through a callback captured before autosave", async () => {
    const initial = draft("latest-save", 1, false);
    const view = mount(initial);
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const start = view.result.current.start;
    const saved = draft("latest-save", 2, false);
    api.edit.mockResolvedValue(saved); api.start.mockResolvedValue(null);
    act(() => view.result.current.beginManualEdit());
    await act(async () => { await view.result.current.edit(saved.compilation!.plan); });
    expect(api.start).not.toHaveBeenCalled();
    await act(async () => { await start(); });
    expect(api.start).toHaveBeenCalledWith("latest-save", 2, false);
  });
  it("forks the saved plan rather than the plan from before a flush", async () => {
    const view = mount(draft("latest-fork", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    const fork = view.result.current.fork;
    const saved = draft("latest-fork", 2, false);
    saved.compilation!.plan.name = "Updated report";
    api.edit.mockResolvedValueOnce(saved);
    await act(async () => { await view.result.current.edit(saved.compilation!.plan); });
    api.create.mockResolvedValue(draft("forked", 1, false));
    api.edit.mockResolvedValue(draft("forked", 2, false));
    await act(async () => { await fork(); });
    expect(api.edit).toHaveBeenLastCalledWith("forked", 1, expect.objectContaining({ name: "Updated report (copy)" }));
    expect(api.start).not.toHaveBeenCalled();
  });
  it("leaves genuine missing facts as questions instead of retrying the doctor", async () => {
    const clarified = draft("doctor-question", 2, false);
    clarified.compilation!.ready = false;
    clarified.compilation!.plan.questions = [{ id: "recipient", prompt: "Who should receive this?", kind: "contact", required: true, allow_other: true, options: [], example: "", min: null, max: null, step_id: "send", input: "recipient" }];
    api.repair.mockResolvedValue(clarified);
    const view = mount(draft("doctor-question", 1));
    await waitFor(() => expect(view.result.current.session?.revision).toBe(2));
    expect(view.result.current.session?.compilation?.plan.questions[0].prompt).toBe("Who should receive this?");
    expect(view.result.current.doctorFailure).toBeNull();
    expect(view.result.current.repairing).toBe(false);
    expect(api.repair).toHaveBeenCalledTimes(1);
    expect(api.start).not.toHaveBeenCalled();
  });
  it("repairs a reopened draft once under Strict Mode, preserving answers without running it", async () => {
    api.repair.mockResolvedValue(draft("doctor-open", 2, false));
    const view = mount(draft("doctor-open", 1));
    await waitFor(() => expect(view.result.current.session?.compilation?.ready).toBe(true));
    expect(api.repair).toHaveBeenCalledTimes(1);
    expect(view.result.current.session?.answers.report_name).toBe("Sales report");
    expect(api.start).not.toHaveBeenCalled();
  });
  it("continues a newly submitted request after the doctor succeeds when Run when ready is enabled", async () => {
    const view = mount(draft("doctor-send", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    api.message.mockResolvedValue(draft("doctor-send", 2));
    api.repair.mockResolvedValue(draft("doctor-send", 3, false));
    api.start.mockResolvedValue(null);
    await act(async () => { await view.result.current.send("Calculate the average too"); });
    await waitFor(() => expect(api.start).toHaveBeenCalledWith("doctor-send", 3));
    expect(api.start).toHaveBeenCalledTimes(1);
    expect(api.repair).toHaveBeenCalledTimes(1);
  });
  it("stops after an unsuccessful doctor pass, including reopening the same revision", async () => {
    const stillBroken = draft("doctor-stopped", 2);
    api.repair.mockResolvedValue(stillBroken);
    const view = mount(draft("doctor-stopped", 1));
    await waitFor(() => expect(view.result.current.doctorFailure).toContain("couldn’t finish"));
    expect(view.result.current.busy).toBe(false);
    view.unmount();
    const reopened = mount(stillBroken);
    await waitFor(() => expect(reopened.result.current.doctorFailure).toContain("couldn’t finish"));
    expect(api.repair).toHaveBeenCalledTimes(1);
    expect(api.start).not.toHaveBeenCalled();
    api.answer.mockResolvedValue(draft("doctor-stopped", 3));
    api.repair.mockResolvedValue(draft("doctor-stopped", 4, false));
    act(() => reopened.result.current.setAutoExecute(false));
    await act(async () => { await reopened.result.current.answer("report_name", "Updated report"); });
    await waitFor(() => expect(reopened.result.current.session?.revision).toBe(4));
    expect(api.repair).toHaveBeenCalledTimes(2);
    expect(api.start).not.toHaveBeenCalled();
  });
  it("ignores a late repair response after starting a new conversation", async () => {
    let finish!: (saved: AssistantSession) => void;
    api.repair.mockReturnValue(new Promise<AssistantSession>(resolve => { finish = resolve; }));
    const view = mount(draft("doctor-stale", 1));
    await waitFor(() => expect(api.repair).toHaveBeenCalledTimes(1));
    act(() => view.result.current.fresh());
    await act(async () => { finish(draft("doctor-stale", 2, false)); });
    expect(view.result.current.session).toBeNull();
    expect(view.result.current.repairing).toBe(false);
    expect(api.start).not.toHaveBeenCalled();
  });
  it("honors turning Run when ready off while the doctor is working", async () => {
    const view = mount(draft("doctor-toggle", 1, false));
    await waitFor(() => expect(view.result.current.loading).toBe(false));
    api.message.mockResolvedValue(draft("doctor-toggle", 2));
    let finish!: (saved: AssistantSession) => void;
    api.repair.mockReturnValue(new Promise<AssistantSession>(resolve => { finish = resolve; }));
    await act(async () => { await view.result.current.send("Update the report"); });
    await waitFor(() => expect(view.result.current.repairing).toBe(true));
    act(() => view.result.current.setAutoExecute(false));
    await act(async () => { finish(draft("doctor-toggle", 3, false)); });
    expect(view.result.current.session?.compilation?.ready).toBe(true);
    expect(api.start).not.toHaveBeenCalled();
  });
});
