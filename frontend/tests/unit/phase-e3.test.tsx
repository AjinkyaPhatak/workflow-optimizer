// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NodeTest } from "@/components/panels/NodeTest";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { sideEffectNodes, upstreamIds } from "@/features/workflows/useTestRun";
import { workflowApi } from "@/lib/api";
import { availableVariables } from "@/lib/workflow/variables";
import { useEditorStore } from "@/stores/workflow-editor/store";
import { useTestRunStore } from "@/stores/test-run/store";
import type { NodeDefinition, TestRunResult } from "@/types/api";
import { catalog, sampleDefinition } from "./fixtures";

afterEach(cleanup);
beforeEach(() => useTestRunStore.getState().reset());

const sendNode: NodeDefinition = {
  type: "gmail.send", name: "Gmail: Send Email", category: "integration", description: "", role: "", side_effects: "unsafe",
  inputs: [{ name: "body", type: "json", required: false, multiple: false, description: "" }],
  outputs: [{ name: "message_id", type: "string", required: true, multiple: false, description: "" }], config: [],
};
const fullCatalog = new Map<string, NodeDefinition>([...catalog, [sendNode.type, sendNode]]);

function Editor({ children }: { children: ReactNode }) {
  const s = { workflow: { id: "wf-1" }, catalog: fullCatalog, catalogList: [...fullCatalog.values()] } as unknown as EditorSession;
  return (
    <ReactFlowProvider>
      <EditorContext.Provider value={s}>{children}</EditorContext.Provider>
    </ReactFlowProvider>
  );
}

function withSend() {
  const def = sampleDefinition();
  const llm = def.nodes.find((n) => n.type === "llm")!;
  def.nodes.push({ id: "send", type: "gmail.send", name: "Notify", position: null, config: {} });
  def.edges.push({ id: "e-send", source: llm.id, source_port: "response", target: "send", target_port: "body" });
  return { def, llm: llm.id };
}

describe("test runs", () => {
  it("knows what a test up to a node runs, and which of it changes things", () => {
    const { def, llm } = withSend();
    expect([...upstreamIds(def, llm)].sort()).toEqual(["in", llm].sort());
    expect(sideEffectNodes(def, fullCatalog, llm)).toEqual([]);
    expect(sideEffectNodes(def, fullCatalog, "send")).toEqual(["Notify"]);
    expect(sideEffectNodes(def, fullCatalog, null)).toEqual(["Notify"]);
  });

  it("tests up to a node with the run input and shows its output", async () => {
    const { def, llm } = withSend();
    useEditorStore.getState().load(def);
    useTestRunStore.getState().setRows([{ key: "query", value: "hello" }]);
    const result: TestRunResult = { status: "COMPLETED", nodes: [
      { node_id: "in", node_type: "input", status: "COMPLETED", output: { data: { query: "hello" } } },
      { node_id: llm, node_type: "llm", status: "COMPLETED", output: { response: "Hi there" }, duration_ms: 812 },
    ] };
    const spy = vi.spyOn(workflowApi, "test").mockResolvedValue(result);
    render(<Editor><NodeTest nodeId={llm} /></Editor>);
    fireEvent.click(screen.getByRole("button", { name: "Test up to this node" }));
    await waitFor(() => expect(screen.getByTestId("node-test-output").textContent).toContain("Hi there"));
    expect(spy).toHaveBeenCalledWith("wf-1", { definition: useEditorStore.getState().definition, input: { query: "hello" }, until_node_id: llm });
    expect(screen.getByTestId("node-test-result").textContent).toContain("812 ms");
    spy.mockRestore();
  });

  it("asks before running nodes that change things", async () => {
    const { def } = withSend();
    useEditorStore.getState().load(def);
    const spy = vi.spyOn(workflowApi, "test").mockResolvedValue({ status: "COMPLETED", nodes: [] });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(<Editor><NodeTest nodeId="send" /></Editor>);
    fireEvent.click(screen.getByRole("button", { name: "Test up to this node" }));
    expect(confirm.mock.calls[0][0]).toContain("Notify");
    expect(spy).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    fireEvent.click(screen.getByRole("button", { name: "Test up to this node" }));
    await waitFor(() => expect(spy).toHaveBeenCalled());
    spy.mockRestore();
    confirm.mockRestore();
  });

  it("shows a failure and what was skipped", () => {
    useTestRunStore.getState().finish({ status: "FAILED", error: { code: "NODE_FAILED", message: "boom" }, nodes: [
      { node_id: "llm_1", node_type: "llm", status: "FAILED", error: { code: "RATE_LIMITED", message: "slow down" } },
    ] });
    render(<Editor><NodeTest nodeId="llm_1" /></Editor>);
    expect(screen.getByTestId("node-test-result").textContent).toContain("slow down");
    // The code shows only with technical details on (Phase H5).
    expect(screen.getByTestId("node-test-result").textContent).not.toContain("RATE_LIMITED");
  });

  it("offers example values from the last test in the variable picker", () => {
    const { def, llm } = withSend();
    const groups = availableVariables(def, fullCatalog, "send", new Map([[llm, { response: "Hi there" }]]));
    const option = groups.find((g) => g.id === "nodes")!.options.find((o) => o.expression === `${llm}.response`)!;
    expect(option.example).toBe("Hi there");
    const long = availableVariables(def, fullCatalog, "send", new Map([[llm, { response: "x".repeat(100) }]]));
    expect(long.find((g) => g.id === "nodes")!.options.find((o) => o.expression === `${llm}.response`)!.example).toHaveLength(58);
  });
});
