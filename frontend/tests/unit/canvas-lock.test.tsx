// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { Compilation, Plan, Step, ToolDescriptor } from "@/types/automation";
import { WorkflowDraft } from "@/components/assistant/WorkflowDraft";
import { emptyPlan, literal } from "@/lib/automation/editor";
import type { Connection } from "@xyflow/react";
const flow = vi.hoisted(() => ({ fitView: vi.fn(), setViewport: vi.fn(), screenToFlowPosition: vi.fn((position: { x: number; y: number }) => position), connect: null as ((connection: Connection) => void) | null }));
vi.mock("@xyflow/react", () => ({
  ReactFlowProvider: ({ children }: { children: ReactNode }) => children,
  ReactFlow: ({ children, nodes, onNodeClick, onConnect, nodesConnectable }: { children: ReactNode; nodes: { id: string; draggable?: boolean; data: { step?: Step } }[]; onNodeClick: (event: unknown, node: unknown) => void; onConnect: (connection: Connection) => void; nodesConnectable: boolean }) => {
    flow.connect = onConnect;
    return <div data-testid="flow" data-connectable={nodesConnectable}>{nodes.filter(node => node.data.step).map(node => <button key={node.id} data-draggable={node.draggable} onClick={event => onNodeClick(event, node)}>View {node.data.step!.name}</button>)}{children}</div>;
  },
  Controls: () => <button>Fit view</button>, Background: () => null, MiniMap: () => null,
  MarkerType: { ArrowClosed: "arrowclosed" }, useReactFlow: () => flow,
  Handle: () => null, useUpdateNodeInternals: () => vi.fn(), Position: { Left: "left", Right: "right" },
}));
afterEach(() => { cleanup(); vi.useRealTimers(); });
const value: Plan = { ...emptyPlan(), steps: [{ id: "write", name: "Write a summary", kind: "tool", tool: "ai.write", inputs: [literal("text", "Hello")], children: [], otherwise: [], condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" }] };
const tools: ToolDescriptor[] = [{ id: "ai.write", name: "Write", description: "Write a summary", inputs: [{ name: "text", type: "string", description: "Source text", required: true }], outputs: [{ name: "text", type: "string", description: "Summary", required: true }], side_effects: "none" }];
it("locks manual changes while AI builds but leaves view and inspector available", () => {
  const save = vi.fn(); const manual = vi.fn();
  const view = render(<WorkflowDraft plan={value} tools={tools} onSave={save} onManualEdit={manual} busy />);
  expect((screen.getByLabelText("Workflow name") as HTMLInputElement).disabled).toBe(true);
  expect(screen.getByTestId("flow").dataset.connectable).toBe("false");
  expect(screen.getByRole("button", { name: "View Write a summary" }).dataset.draggable).toBe("false");
  fireEvent.click(screen.getByRole("button", { name: "View Write a summary" }));
  expect(screen.getByRole("region", { name: "Edit Write a summary" })).toBeTruthy();
  expect(screen.getByLabelText("Step name").matches(":disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "Remove step" }).matches(":disabled")).toBe(true);
  fireEvent.keyDown(document.body, { key: "Delete" });
  expect(manual).not.toHaveBeenCalled(); expect(save).not.toHaveBeenCalled();
  expect((screen.getByRole("button", { name: "Fit view" }) as HTMLButtonElement).disabled).toBe(false);
  view.rerender(<WorkflowDraft plan={value} tools={tools} onSave={save} onManualEdit={manual} busy={false} />);
  expect(screen.getByLabelText("Step name").matches(":disabled")).toBe(false);
  fireEvent.change(screen.getByLabelText("Step name"), { target: { value: "My summary" } });
  expect(manual).toHaveBeenCalledTimes(1);
});
it("keeps viewer controls read-only after building finishes", () => {
  render(<WorkflowDraft plan={value} tools={tools} onSave={vi.fn()} busy={false} readOnly />);
  expect((screen.getByLabelText("Workflow name") as HTMLInputElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Arrange" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.queryByText(/Building your workflow/)).toBeNull();
});
it("renders null server slices in nested plans without editing or saving the preview", async () => {
  vi.useFakeTimers();
  const save = vi.fn(); const manual = vi.fn(); const dirty = vi.fn();
  // Match JSON returned for Go nil slices, including unused control inputs.
  const incoming = JSON.parse(JSON.stringify({ ...value, questions: null, steps: [{
    ...value.steps[0], id: "branch", name: "Choose a path", kind: "branch", inputs: null, otherwise: null,
    children: [{ ...value.steps[0], inputs: null, children: null, otherwise: null, question: {
      id: "choice", step_id: "write", input: "text", kind: "choice", prompt: "Choose", required: true,
      allow_other: false, options: null, min: null, max: null, example: "",
    } }],
  }] })) as Plan;
  const original = JSON.stringify(incoming);
  const view = render(<WorkflowDraft plan={incoming} tools={tools} onSave={save} onManualEdit={manual} onDirtyChange={dirty} busy />);
  expect(screen.getByRole("button", { name: "View Choose a path" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "View Write a summary" }));
  expect((screen.getByLabelText("Choices, one per line") as HTMLTextAreaElement).value).toBe("");
  expect(screen.getByLabelText("Step name").matches(":disabled")).toBe(true);
  view.rerender(<WorkflowDraft plan={incoming} tools={tools} onSave={save} onManualEdit={manual} onDirtyChange={dirty} busy={false} readOnly />);
  fireEvent.keyDown(document.body, { key: "Delete" });
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(dirty).toHaveBeenLastCalledWith(false);
  expect(manual).not.toHaveBeenCalled(); expect(save).not.toHaveBeenCalled();
  expect(JSON.stringify(incoming)).toBe(original);
});
it("reports clean state after undoing or discarding local edits", () => {
  const dirty = vi.fn(); const save = vi.fn();
  const view = render(<WorkflowDraft plan={value} tools={tools} onSave={save} onDirtyChange={dirty} busy={false} />);
  fireEvent.change(screen.getByLabelText("Workflow name"), { target: { value: "My workflow" } });
  expect(dirty).toHaveBeenLastCalledWith(true);
  fireEvent.click(screen.getByRole("button", { name: "Undo" }));
  expect(dirty).toHaveBeenLastCalledWith(false);
  fireEvent.change(screen.getByLabelText("Workflow name"), { target: { value: "Local workflow" } });
  view.rerender(<WorkflowDraft plan={{ ...value, name: "Latest workflow" }} tools={tools} onSave={save} onDirtyChange={dirty} busy={false} />);
  fireEvent.click(screen.getByRole("button", { name: "Use latest version" }));
  expect(dirty).toHaveBeenLastCalledWith(false);
  expect((screen.getByLabelText("Workflow name") as HTMLInputElement).value).toBe("Latest workflow");
  expect(save).not.toHaveBeenCalled();
});
it("does not apply a connection check that finishes after AI starts building", async () => {
  let finish!: (value: Compilation) => void;
  const check = vi.fn(() => new Promise<Compilation>(resolve => { finish = resolve; }));
  const manual = vi.fn(); const save = vi.fn();
  const source = { ...value.steps[0], id: "source", name: "Source" };
  const twoSteps = { ...value, steps: [source, value.steps[0]] };
  const view = render(<WorkflowDraft plan={twoSteps} tools={tools} onSave={save} onManualEdit={manual} checkPlan={check} busy={false} />);
  act(() => { flow.connect?.({ source: "source", target: "write", sourceHandle: "out:text", targetHandle: "in:text" }); });
  expect(check).toHaveBeenCalledTimes(1);
  view.rerender(<WorkflowDraft plan={twoSteps} tools={tools} onSave={save} onManualEdit={manual} checkPlan={check} busy />);
  await act(async () => { finish({ plan: twoSteps, ready: true, diagnostics: [] }); });
  expect(manual).not.toHaveBeenCalled(); expect(save).not.toHaveBeenCalled();
});
