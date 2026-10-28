// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { allSteps, availableSteps, copySteps, emptyPlan, literal, moveStep, pasteSteps, projectPlan, type Fragment } from "@/lib/automation/editor";
import { BindingEditor } from "@/components/assistant/canvas/BindingEditor";
import { useCanvasDraft } from "@/features/assistant/useCanvasDraft";
import type { Plan, Step } from "@/types/automation";
const step = (id: string, children: Step[] = [], otherwise: Step[] = [], kind = "tool"): Step => ({ id, name: id, kind, children, otherwise, tool: "example", inputs: [], question: null, condition: null, max_iterations: 0, duration_seconds: 0, workflow_ref: "" });
const plan = (...steps: Step[]): Plan => ({ ...emptyPlan(), steps });
afterEach(() => { cleanup(); vi.useRealTimers(); });
describe("Compiler canvas semantics", () => {
  it("joins both branch paths before continuing, without adding execution steps", () => {
    const value = plan(step("before"), step("decision", [step("yes")], [step("no")], "branch"), step("after"));
    const graph = projectPlan(value);
    expect(graph.edges).toEqual(expect.arrayContaining([
      expect.objectContaining({ source: "decision", target: "yes", label: "Yes" }),
      expect.objectContaining({ source: "decision", target: "no", label: "Otherwise" }),
      expect.objectContaining({ source: "yes", target: "__join_decision" }),
      expect.objectContaining({ source: "no", target: "__join_decision" }),
      expect.objectContaining({ source: "__join_decision", target: "after" }),
    ]));
    expect(allSteps(value.steps)).toHaveLength(5);
    expect(graph.edges.some(edge => edge.source === "decision" && edge.target === "after")).toBe(false);
  });
  it("shows parallel joins and repeat return edges without leaking sibling outputs", () => {
    const value = plan(step("source"), step("parallel", [step("a"), step("b")], [], "parallel"), step("repeat", [step("body")], [], "repeat"), step("end"));
    expect(availableSteps(value, "b").map(s => s.id)).toEqual(["source"]);
    expect(availableSteps(value, "end").map(s => s.id)).toEqual(["source", "parallel", "repeat"]);
    expect(availableSteps(value, "repeat", true).map(s => s.id)).toEqual(["source", "parallel", "body"]);
    expect(projectPlan(value).edges).toEqual(expect.arrayContaining([expect.objectContaining({ source: "body", target: "repeat", kind: "loop" })]));
  });
  it("renders repeated preview IDs as separate cards and joins without rewriting the plan", () => {
    const first = step("price_check_loop", [step("read_price")], [], "repeat");
    const second = { ...step("price_check_loop", [step("send_alert")], [], "repeat"), name: "Check the next price" };
    const after = step("summary");
    after.inputs = [{ name: "value", source: "step", ref: "price_check_loop.result", value_json: "" }];
    const value = plan(first, second, after), before = JSON.stringify(value);
    const graph = projectPlan(value);
    expect(graph.nodes.filter(node => node.step)).toHaveLength(5);
    expect(graph.nodes.filter(node => !node.step)).toHaveLength(2);
    expect(new Set(graph.nodes.map(node => node.id)).size).toBe(graph.nodes.length);
    expect(new Set(graph.edges.map(edge => edge.id)).size).toBe(graph.edges.length);
    expect(graph.ambiguousStepIDs).toEqual(["price_check_loop"]);
    expect(graph.nodes.filter(node => node.step?.id === "price_check_loop").map(node => node.step)).toEqual([first, second]);
    expect(graph.edges.some(edge => edge.kind === "data")).toBe(false);
    const nodeIDs = new Set(graph.nodes.map(node => node.id));
    expect(graph.edges.every(edge => nodeIDs.has(edge.source) && nodeIDs.has(edge.target))).toBe(true);
    expect(projectPlan(value)).toEqual(graph);
    expect(JSON.stringify(value)).toBe(before);
  });
  it("reserves actual step IDs when allocating joins and makes repeated input edges unique", () => {
    const consumer = step("summary");
    consumer.inputs = [0, 1].map(() => ({ name: "value", source: "step" as const, ref: "source.result", value_json: "" }));
    const value = plan(step("source"), step("choice", [step("yes")], [], "branch"), step("__join_choice"), consumer);
    const graph = projectPlan(value);
    expect(graph.ambiguousStepIDs).toEqual([]);
    expect(new Set(graph.nodes.map(node => node.id)).size).toBe(graph.nodes.length);
    expect(graph.nodes.find(node => node.id === "__join_choice")?.step?.id).toBe("__join_choice");
    const join = graph.nodes.find(node => !node.step)!;
    expect(join.id).not.toBe("__join_choice");
    expect(graph.edges).toContainEqual(expect.objectContaining({ source: "yes", target: join.id }));
    expect(graph.edges.filter(edge => edge.kind === "data")).toHaveLength(2);
    expect(new Set(graph.edges.map(edge => edge.id)).size).toBe(graph.edges.length);
  });
  it("keeps layout separate from order and remaps pasted nested references and questions", () => {
    const a = step("source"); const b = step("consumer");
    b.inputs = [{ name: "value", source: "step", ref: "source.result", value_json: "" }, { name: "name", source: "answer", ref: "who", value_json: "" }];
    const value = plan(a, b); value.questions = [{ id: "who", step_id: "consumer", input: "name", kind: "text", prompt: "Who?", required: true, allow_other: false, options: [], min: null, max: null, example: "" }];
    const fragment: Fragment = copySteps(value, ["source", "consumer"]);
    const copied = pasteSteps(value, fragment).plan;
    const [copyA, copyB] = copied.steps.slice(2);
    expect(copyB.inputs[0].ref).toBe(copyA.id + ".result");
    expect(copyB.inputs[1].ref).toBe(copied.questions[1].id);
    expect(copied.questions[1].step_id).toBe(copyB.id);
    expect(copied.steps.slice(0, 2)).toEqual(value.steps);
    expect(moveStep(value, "source", "", "children", 1).steps.map(s => s.id)).toEqual(["consumer", "source"]);
  });
  it("retains template binding mode and typed literals in the inspector", () => {
    const onChange = vi.fn();
    const binding = { name: "text", source: "template" as const, ref: "message_name", value_json: JSON.stringify("Hello [name]") };
    const view = render(<BindingEditor field={{ name: "text", type: "string", description: "", required: true }} binding={binding} steps={[]} tools={[]} plan={plan()} onChange={onChange} />);
    expect((screen.getByLabelText("Source for text") as HTMLSelectElement).value).toBe("template");
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("text"), { target: { value: "Welcome [name]" } });
    expect(onChange).toHaveBeenLastCalledWith({ ...binding, value_json: JSON.stringify("Welcome [name]") });
    view.unmount();
    render(<BindingEditor field={{ name: "number", type: "json", description: "", required: true }} binding={literal("number", 150)} steps={[]} tools={[]} plan={plan()} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText("number"), { target: { value: "200" } });
    expect(onChange).toHaveBeenLastCalledWith(literal("number", 200));
  });
});
describe("Canvas autosave and incoming versions", () => {
  it("serializes changes during an in-flight save and keeps undo history", async () => {
    const original = plan(step("one")); const first = { ...original, name: "First" }; const second = { ...original, name: "Second" };
    let finish!: (value: unknown) => void;
    const save = vi.fn().mockImplementationOnce(() => new Promise(resolve => { finish = resolve; })).mockImplementation(async (plan: Plan) => ({ compilation: { plan } }));
    const { result } = renderHook(() => useCanvasDraft(original, save));
    act(() => result.current.change(first));
    let pending!: Promise<void>; act(() => { pending = result.current.flush(); });
    act(() => result.current.change(second));
    await act(async () => { finish({ compilation: { plan: first } }); await pending; });
    expect(save.mock.calls.map(call => call[0].name)).toEqual(["First", "Second"]);
    expect(result.current.dirty).toBe(false);
    act(() => result.current.undo()); expect(result.current.draft.name).toBe("First");
    act(() => result.current.redo()); expect(result.current.draft.name).toBe("Second");
  });
  it("preserves dirty edits when another revision arrives and requires a choice", async () => {
    const original = plan(step("one")); const save = vi.fn();
    const { result, rerender } = renderHook(({ incoming }) => useCanvasDraft(incoming, save), { initialProps: { incoming: original } });
    act(() => result.current.change({ ...original, name: "Mine" }));
    rerender({ incoming: { ...original, name: "Theirs" } });
    expect(result.current.draft.name).toBe("Mine"); expect(result.current.conflict?.name).toBe("Theirs");
    await expect(result.current.flush()).rejects.toThrow("Resolve");
    expect(save).not.toHaveBeenCalled();
    act(() => result.current.resolve(false)); expect(result.current.draft.name).toBe("Theirs"); expect(result.current.dirty).toBe(false);
  });
  it("does not autosave after a failure until retry or a new edit", async () => {
    vi.useFakeTimers(); const original = plan(step("one")); const save = vi.fn().mockRejectedValue(new Error("Offline"));
    const { result } = renderHook(() => useCanvasDraft(original, save));
    act(() => result.current.change({ ...original, name: "Mine" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(1600); });
    expect(result.current.error).toBe("Offline");
    await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
    expect(save).toHaveBeenCalledTimes(1); expect(result.current.draft.name).toBe("Mine");
  });
  it("does not save incoming AI previews or treat an unchanged server echo as a conflict", async () => {
    vi.useFakeTimers(); const original = plan(step("one")); const save = vi.fn(); const manual = vi.fn();
    const { result, rerender } = renderHook(({ incoming }) => useCanvasDraft(incoming, save, manual), { initialProps: { incoming: original } });
    const preview = { ...original, name: "Generated preview", steps: [...original.steps, step("two")] };
    rerender({ incoming: preview });
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(result.current.draft).toEqual(preview); expect(result.current.dirty).toBe(false);
    expect(save).not.toHaveBeenCalled(); expect(manual).not.toHaveBeenCalled();
    act(() => result.current.change({ ...preview, name: "My changes" }));
    rerender({ incoming: structuredClone(preview) });
    expect(result.current.conflict).toBeNull(); expect(result.current.draft.name).toBe("My changes");
  });
});
