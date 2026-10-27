// @vitest-environment jsdom
import { cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowCard } from "@/components/workflow/WorkflowCard";
import { stepIcons, useWorkflowCards } from "@/features/workflows/useWorkflowCards";
import { assistantApi } from "@/lib/api/assistant";
import { executionApi, nodeApi, workflowApi } from "@/lib/api";
import { emptyPlan } from "@/lib/automation/editor";
import type { Workflow, WorkflowTemplate } from "@/types/api";
import type { AutomationRun, Step, WorkflowEnvelope } from "@/types/automation";

vi.mock("@/lib/api", () => ({
  nodeApi: { list: vi.fn() },
  workflowApi: { templates: vi.fn(), listVersions: vi.fn(), getVersion: vi.fn() },
  executionApi: { listByWorkflow: vi.fn() },
}));
vi.mock("@/lib/api/assistant", () => ({ assistantApi: { workflows: vi.fn(), workflow: vi.fn(), runs: vi.fn(), templates: vi.fn() } }));
vi.mock("@/components/ui/NodeIcon", () => ({ NodeIcon: () => <span /> }));
const workflow = (id: string): Workflow => ({ id, project_id: "project", name: id, description: null, active_version_id: null, created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-02T00:00:00Z" });
const step = (id: string, kind = "tool", children: Step[] = [], otherwise: Step[] = []): Step => ({ id, name: id, kind, tool: `node.${id}`, children, otherwise, inputs: [], condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" });
const envelope: WorkflowEnvelope = {
  workflow: { ...workflow("compiler"), workspace_id: "workspace" },
  layout: { revision: 1, positions: {} },
  session: {
    id: "compiler", workspace_id: "workspace", user_id: "user", revision: 1, credential_id: "", messages: [], answers: {},
    created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-02T00:00:00Z",
    compilation: { ready: true, diagnostics: [], plan: { ...emptyPlan(), steps: [step("branch", "condition", [step("email")], [step("slack")])] } },
  },
};
const run = (session_id: string, status: string, created_at: string): AutomationRun => ({
  id: `${session_id}-${created_at}`, session_id, status, created_at, updated_at: created_at, workspace_id: "workspace", revision: 1,
  execution_backend: "temporal", plan: emptyPlan(), question: null, error: "", outputs: {},
});
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(nodeApi.list).mockResolvedValue({ items: [] });
  vi.mocked(workflowApi.templates).mockResolvedValue({ items: [] });
  vi.mocked(assistantApi.templates).mockResolvedValue({ items: [] });
  vi.mocked(assistantApi.workflows).mockResolvedValue({ items: [envelope.workflow] });
  vi.mocked(assistantApi.runs).mockResolvedValue({ items: [] });
  vi.mocked(assistantApi.workflow).mockResolvedValue(envelope);
  vi.mocked(workflowApi.listVersions).mockResolvedValue({ items: [], page: 1, page_size: 1, total: 0 });
  vi.mocked(executionApi.listByWorkflow).mockResolvedValue({ items: [], page: 1, page_size: 1, total: 0 });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("Workflow card data", () => {
  it("shows built-in compiler tools and controls absent from the legacy node catalog", () => {
    const icons = stepIcons(["ai.write", "ai.extract", "web.search", "table.extract", "analysis.python"], new Map());
    expect(icons.map(icon => icon.key)).toEqual(["ai", "web.search", "table.extract", "analysis.python"]);
    expect(icons[0].def).toEqual({ type: "ai.write", category: "ai" });
    expect(stepIcons(["condition", "delay"], new Map()).map(icon => icon.def?.type)).toEqual(["condition", "delay"]);
  });
  it("withholds previous classifications when the workflow list changes", async () => {
    const { result, rerender } = renderHook(({ rows }) => useWorkflowCards(rows, "workspace"), { initialProps: { rows: [workflow("legacy")] } });
    await waitFor(() => expect(result.current.kinds.get("legacy")).toBe("legacy"));
    vi.mocked(assistantApi.workflows).mockImplementation(() => new Promise(() => {}));
    rerender({ rows: [workflow("compiler")] });
    expect(result.current.kinds.size).toBe(0);
  });

  it("loads compiler branches and newest matching run without any legacy requests for that workflow", async () => {
    const runs = [
      run("compiler", "FAILED", "2026-10-01T00:00:00Z"),
      run("other", "RUNNING", "2026-10-05T00:00:00Z"),
      run("compiler", "WAITING_FOR_INPUT", "2026-10-03T00:00:00Z"),
    ];
    vi.mocked(assistantApi.runs).mockResolvedValue({ items: runs });
    const { result } = renderHook(() => useWorkflowCards([workflow("compiler"), workflow("legacy")], "workspace"));
    await waitFor(() => expect(result.current.details.size).toBe(2));
    expect(result.current.kinds.get("compiler")).toBe("compiler");
    expect(result.current.details.get("compiler")).toEqual({ steps: ["condition", "email", "slack"], ready: true, last: runs[2] });
    expect(workflowApi.listVersions).toHaveBeenCalledExactlyOnceWith("legacy", 1, 1);
    expect(executionApi.listByWorkflow).toHaveBeenCalledExactlyOnceWith("legacy", 1, 1);
    expect(workflowApi.getVersion).not.toHaveBeenCalled();
    expect(assistantApi.workflows).toHaveBeenCalledTimes(1);
    expect(assistantApi.runs).toHaveBeenCalledTimes(1);
  });
  it("retains compiler classification and run status when its plan fails to load", async () => {
    vi.mocked(assistantApi.workflow).mockRejectedValue(new Error("Unavailable"));
    const { result } = renderHook(() => useWorkflowCards([workflow("compiler")], "workspace"));
    await waitFor(() => expect(result.current.details.size).toBe(1));
    expect(result.current.kinds.get("compiler")).toBe("compiler");
    expect(workflowApi.listVersions).not.toHaveBeenCalled();
    expect(executionApi.listByWorkflow).not.toHaveBeenCalled();
  });
  it("does not guess legacy IDs if compiler classification fails", async () => {
    vi.mocked(assistantApi.workflows).mockRejectedValue(new Error("Unavailable"));
    const { result } = renderHook(() => useWorkflowCards([workflow("compiler")], "workspace"));
    await waitFor(() => expect(assistantApi.workflows).toHaveBeenCalled());
    expect(result.current.kinds.size).toBe(0);
    expect(workflowApi.listVersions).not.toHaveBeenCalled();
  });
  it("prefers compiler templates when both APIs return the same ID", async () => {
    const duplicate: WorkflowTemplate = { saved: false, id: "shared", name: "Legacy label", description: "", category: "General", node_types: [], setup: [], definition: { version: 1, nodes: [], edges: [], settings: {} } };
    vi.mocked(workflowApi.templates).mockResolvedValue({ items: [duplicate, { ...duplicate, id: "legacy-only" }] });
    vi.mocked(assistantApi.templates).mockResolvedValue({ items: [{ id: "shared", name: "Compiler label", description: "", builtin: true, plan: emptyPlan() }] });
    const { result } = renderHook(() => useWorkflowCards([], "workspace"));
    await waitFor(() => expect(result.current.templates).toHaveLength(2));
    expect(result.current.templates[0]).toMatchObject({ id: "shared", name: "Compiler label", automation: true });
    expect(result.current.templates[1].id).toBe("legacy-only");
  });
  it("merges compiler suggestions by ID and still shows them when the legacy template service fails", async () => {
    vi.mocked(workflowApi.templates).mockRejectedValue(new Error("Unavailable"));
    vi.mocked(assistantApi.templates).mockResolvedValue({ items: [{ id: "modern", name: "Email", description: "Send email", builtin: true, plan: envelope.session.compilation!.plan }] });
    const { result } = renderHook(() => useWorkflowCards([], "workspace"));
    await waitFor(() => expect(result.current.templates).toHaveLength(1));
    expect(result.current.templates[0]).toMatchObject({ id: "modern", automation: true, node_types: ["condition", "email", "slack"] });
  });
});

describe("Workflow card actions and status", () => {
  const props = { wf: workflow("compiler"), catalog: new Map(), canEdit: true, onRename: vi.fn(), onDelete: vi.fn() };
  it("offers the editor for compiler history and hides unavailable mutations", () => {
    render(<WorkflowCard {...props} kind="compiler" details={{ steps: [], last: { status: "WAITING_FOR_INPUT", created_at: "2026-10-03T00:00:00Z" } }} />);
    expect(screen.getByText("Waiting for your answer")).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "compiler" })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "More for compiler" }));
    expect(screen.getByRole("menuitem", { name: "Run history" }).getAttribute("href")).toBe("/workflows/compiler/editor");
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Delete" })).toBeNull();
  });
  it("withholds mutating actions until the workflow has been classified", () => {
    render(<WorkflowCard {...props} details={undefined} />);
    fireEvent.click(screen.getByRole("button", { name: "More for compiler" }));
    expect(screen.queryByRole("menuitem", { name: "Rename" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Delete" })).toBeNull();
  });
  it("keeps legacy rename/delete and execution history", () => {
    render(<WorkflowCard {...props} kind="legacy" details={undefined} />);
    fireEvent.click(screen.getByRole("button", { name: "More for compiler" }));
    expect(screen.getByRole("menuitem", { name: "Run history" }).getAttribute("href")).toBe("/workflows/compiler/executions");
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeTruthy();
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.change(screen.getByLabelText("Workflow name"), { target: { value: "Renamed" } });
    fireEvent.submit(screen.getByLabelText("Workflow name").closest("form")!);
    expect(props.onRename).toHaveBeenCalledWith("Renamed");
  });
  it("shows rename errors and allows retry without losing the entered name", async () => {
    const onRename = vi.fn().mockRejectedValueOnce(new Error("Rename failed")).mockResolvedValue(undefined);
    render(<WorkflowCard {...props} kind="legacy" details={undefined} onRename={onRename} />);
    fireEvent.click(screen.getByRole("button", { name: "More for compiler" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    const input = screen.getByLabelText("Workflow name") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Retry name" } });
    fireEvent.submit(input.closest("form")!);
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "!Rename failed");
    expect(input.value).toBe("Retry name");
    fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(screen.queryByLabelText("Workflow name")).toBeNull());
    expect(onRename).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).toBeNull();
  });
  it("shows delete errors and keeps the action available for retry", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const onDelete = vi.fn().mockRejectedValueOnce(new Error("Delete failed")).mockResolvedValue(undefined);
    render(<WorkflowCard {...props} kind="legacy" details={undefined} onDelete={onDelete} />);
    fireEvent.click(screen.getByRole("button", { name: "More for compiler" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "!Delete failed");
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(onDelete).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).toBeNull();
  });
  it.each([
    ["PAUSED", "Run paused"], ["WAITING_FOR_EVENT", "Waiting for an event"],
    ["RUNNING", "Running now"], ["COMPLETED", "Last run completed"], ["CANCELLED", "Last run cancelled"], ["FAILED", "Last run didn't finish"],
  ])("shows %s compiler run state", (status, text) => {
    render(<WorkflowCard {...props} kind="compiler" details={{ steps: [], last: { status, created_at: "2026-10-03T00:00:00Z" } }} />);
    expect(screen.getByText(new RegExp(text))).toBeTruthy();
  });
});
