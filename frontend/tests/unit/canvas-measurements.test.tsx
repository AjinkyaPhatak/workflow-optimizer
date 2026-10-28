// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { NodeChange, ReactFlowProps } from "@xyflow/react";
import type { PlanFlowNode } from "@/components/assistant/canvas/PlanNode";
import { WorkflowDraft } from "@/components/assistant/WorkflowDraft";
import { emptyPlan } from "@/lib/automation/editor";
import type { Plan } from "@/types/automation";

const flow = vi.hoisted(() => ({
  nodes: [] as PlanFlowNode[],
  change: null as ((changes: NodeChange<PlanFlowNode>[]) => void) | null,
  fitView: vi.fn(), setViewport: vi.fn(), screenToFlowPosition: vi.fn(),
}));
vi.mock("@xyflow/react", () => ({
  ReactFlowProvider: ({ children }: { children: ReactNode }) => children,
  ReactFlow: ({ nodes = [], onNodesChange, onNodeClick, children }: ReactFlowProps<PlanFlowNode>) => {
    flow.nodes = nodes; flow.change = onNodesChange ?? null;
    return <div>{nodes.filter(node => node.data.step).map(node => <button key={node.id} onClick={event => onNodeClick?.(event, node)}>View {node.data.step!.name}</button>)}{children}</div>;
  },
  Controls: () => null, Background: () => null, MiniMap: () => null,
  MarkerType: { ArrowClosed: "arrowclosed" }, useReactFlow: () => flow,
  Handle: () => null, useUpdateNodeInternals: () => vi.fn(), Position: { Left: "left", Right: "right" },
}));
beforeEach(() => { vi.useFakeTimers(); });
afterEach(() => { cleanup(); vi.useRealTimers(); });

const plan: Plan = { ...emptyPlan(), steps: [{
  id: "write", name: "Write a summary", kind: "tool", tool: "ai.write", inputs: [], children: [], otherwise: [],
  condition: null, question: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "",
}] };
const layout = { revision: 1, positions: { write: { x: 80, y: 80 } } };
const measure = (width: number, height: number) => act(() => {
  flow.change?.([{ id: "write", type: "dimensions", dimensions: { width, height } }]);
});

it("retains measured cards through inspector and semantic edits without saving measurements", async () => {
  const save = vi.fn(); const saveLayout = vi.fn(); const manual = vi.fn(); const dirty = vi.fn();
  render(<WorkflowDraft plan={plan} tools={[]} sessionId="first" layout={layout} onSave={save} onLayoutSave={saveLayout} onManualEdit={manual} onDirtyChange={dirty} busy={false} />);
  measure(280, 180);
  measure(280, 180);
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(flow.nodes[0].measured).toEqual({ width: 280, height: 180 });
  expect(save).not.toHaveBeenCalled(); expect(saveLayout).not.toHaveBeenCalled(); expect(manual).not.toHaveBeenCalled();
  expect(dirty).toHaveBeenLastCalledWith(false);

  fireEvent.click(screen.getByRole("button", { name: "View Write a summary" }));
  fireEvent.change(screen.getByLabelText("Step name"), { target: { value: "Updated summary" } });
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(flow.nodes[0].measured).toEqual({ width: 280, height: 180 });
  expect(flow.nodes[0].data.step?.name).toBe("Updated summary");
  measure(280, 220);
  expect(flow.nodes[0].measured).toEqual({ width: 280, height: 220 });
  expect(manual).toHaveBeenCalledTimes(1);
});

it("measures locked previews and resets dimensions when switching sessions", () => {
  const save = vi.fn();
  const view = render(<WorkflowDraft plan={plan} tools={[]} sessionId="first" layout={layout} onSave={save} busy />);
  measure(280, 180);
  view.rerender(<WorkflowDraft plan={plan} tools={[]} sessionId="first" layout={layout} onSave={save} busy={false} readOnly />);
  expect(flow.nodes[0].measured).toEqual({ width: 280, height: 180 });
  view.rerender(<WorkflowDraft plan={plan} tools={[]} sessionId="second" layout={layout} onSave={save} busy={false} />);
  expect(flow.nodes[0].measured).toBeUndefined();
  measure(280, 240);
  expect(flow.nodes[0].measured).toEqual({ width: 280, height: 240 });
  expect(save).not.toHaveBeenCalled();
});

it("waits for the saved arrangement without persisting derived positions", async () => {
  const save = vi.fn(); const saveLayout = vi.fn().mockResolvedValue(undefined);
  const props = { plan, tools: [], sessionId: "first", onSave: save, onLayoutSave: saveLayout, busy: false };
  const view = render(<WorkflowDraft {...props} layout={{ revision: 0, positions: {} }} />);
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(saveLayout).not.toHaveBeenCalled();
  const savedPosition = { x: 192, y: 160 };
  view.rerender(<WorkflowDraft {...props} layout={{ revision: 7, positions: { write: savedPosition } }} />);
  expect(flow.nodes[0].position).toEqual(savedPosition);
  view.rerender(<WorkflowDraft {...props} layout={{ revision: 0, positions: {} }} />);
  expect(flow.nodes[0].position).toEqual(savedPosition);
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(saveLayout).not.toHaveBeenCalled();

  act(() => { flow.change?.([{ id: "write", type: "position", position: { x: 256, y: 192 } }]); });
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  expect(saveLayout).toHaveBeenCalledExactlyOnceWith({ write: { x: 256, y: 192 } });
  expect(save).not.toHaveBeenCalled();
});

it("preserves saved positions when a restored plan replaces the semantic graph", async () => {
  const saveLayout = vi.fn(); const save = vi.fn();
  const saved = { revision: 2, positions: { write: { x: 192, y: 160 } } };
  const props = { tools: [], sessionId: "first", onSave: save, onLayoutSave: saveLayout, busy: false };
  const view = render(<WorkflowDraft {...props} plan={plan} layout={saved} />);
  view.rerender(<WorkflowDraft {...props} plan={{ ...plan, name: "Restored workflow", steps: plan.steps.map(step => ({ ...step })) }} layout={saved} />);
  expect(flow.nodes[0].position).toEqual(saved.positions.write);
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(flow.nodes[0].position).toEqual(saved.positions.write);
  expect(saveLayout).not.toHaveBeenCalled(); expect(save).not.toHaveBeenCalled();
});

it("keeps duplicate-ID previews inspectable and locked without saving display IDs", async () => {
  const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
  try {
    const save = vi.fn(); const saveLayout = vi.fn(); const manual = vi.fn();
    const first = { ...plan.steps[0], id: "price_check_loop", name: "Check the first price", kind: "repeat" };
    const second = { ...first, name: "Check the next price" };
    const incoming = { ...plan, steps: [first, second] };
    const original = JSON.stringify(incoming);
    const props = { tools: [], sessionId: "preview", onSave: save, onLayoutSave: saveLayout, onManualEdit: manual };
    const view = render(<WorkflowDraft {...props} plan={incoming} busy />);
    expect(new Set(flow.nodes.map(node => node.id)).size).toBe(flow.nodes.length);
    expect(flow.nodes.filter(node => node.data.step)).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "View Check the next price" }));
    expect((screen.getByLabelText("Step name") as HTMLInputElement).value).toBe("Check the next price");
    expect(screen.getByLabelText("Step name").matches(":disabled")).toBe(true);
    view.rerender(<WorkflowDraft {...props} plan={incoming} busy={false} />);
    expect(screen.getByText("Draft needs repair")).toBeTruthy();
    expect(screen.getByLabelText("Workflow name").matches(":disabled")).toBe(true);
    fireEvent.keyDown(document.body, { key: "Delete" });
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(save).not.toHaveBeenCalled(); expect(saveLayout).not.toHaveBeenCalled(); expect(manual).not.toHaveBeenCalled();
    expect(JSON.stringify(incoming)).toBe(original);
    expect(errors).not.toHaveBeenCalled();
    // A checked revision restores normal editable IDs and retains authored layout.
    const repaired = { ...plan, steps: [first, { ...second, id: "next_price_check" }] };
    view.rerender(<WorkflowDraft {...props} plan={repaired} busy={false} />);
    expect(screen.getByLabelText("Workflow name").matches(":disabled")).toBe(false);
    expect(flow.nodes.filter(node => node.data.step).map(node => node.id)).toEqual(["price_check_loop", "next_price_check"]);
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(save).not.toHaveBeenCalled(); expect(saveLayout).not.toHaveBeenCalled();
  } finally { errors.mockRestore(); }
});
