// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NodeInfoCard } from "@/components/nodes/NodeInfoCard";
import { BottomPanel } from "@/components/panels/BottomPanel";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { VariableField } from "@/components/panels/VariableField";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { availableVariables } from "@/lib/workflow/variables";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition } from "@/types/api";
import { catalog, catalogList, def, sampleDefinition } from "./fixtures";

afterEach(cleanup);

const session = (): EditorSession => ({
  loading: false, loadError: null, workflow: null, version: null, catalogList, catalog,
  credentials: [
    { id: "c-1", workspace_id: "w", name: "Work OpenAI", provider: "openai", credential_type: "api_key", created_at: "", updated_at: "" },
    { id: "c-2", workspace_id: "w", name: "Other", provider: "anthropic", credential_type: "api_key", created_at: "", updated_at: "" },
  ],
  connectedAccounts: [],
  role: "owner", busy: null, notice: null,
  save: vi.fn(), validate: vi.fn(), publish: vi.fn(), canPublish: true,
  versions: null, refreshVersions: vi.fn(), openVersion: vi.fn(),
});

function Editor({ children }: { children: ReactNode }) {
  return (
    <ReactFlowProvider>
      <EditorContext.Provider value={session()}>{children}</EditorContext.Provider>
    </ReactFlowProvider>
  );
}

const described: NodeDefinition = {
  ...def("llm"),
  description: "Executes generative text inference.",
  inputs: def("llm").inputs.map((p) => ({ ...p, description: `${p.name} port` })),
};

describe("node hover information", () => {
  it("shows what a node does from its definition", () => {
    render(<NodeInfoCard def={described} title="Ask" />);
    const card = screen.getByTestId("node-info");
    expect(card.textContent).toContain("Ask");
    expect(card.textContent).toContain("LLM · AI");
    expect(card.textContent).toContain("Executes generative text inference.");
    expect(within(card).getByText("Inputs")).toBeTruthy();
    expect(card.textContent).toContain("prompt");
    expect(card.textContent).toContain("prompt port");
    expect(within(card).getByText("Outputs")).toBeTruthy();
    expect(card.textContent).toContain("Can use a workspace credential.");
  });

  it("notes side effects and handles missing metadata", () => {
    render(<NodeInfoCard def={def("http")} />);
    expect(screen.getByTestId("node-info").textContent).toContain("Changes external systems");
    cleanup();
    render(<NodeInfoCard def={undefined} title="Mystery" />);
    expect(screen.getByTestId("node-info").textContent).toContain("not in the node catalog");
  });
});

describe("variable picker", () => {
  it("suggests upstream variables after {{ and inserts the chosen one", () => {
    const onCommit = vi.fn();
    const groups = availableVariables(sampleDefinition(), catalog, "out");
    render(<VariableField id="cfg-body" value="" groups={groups} onCommit={onCommit} />);
    const input = screen.getByRole("combobox") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "Summary: {{ll", selectionStart: 13 } });
    const list = screen.getByTestId("variable-picker");
    const options = within(list).getAllByRole("option");
    expect(options.map((o) => o.querySelector("code")!.textContent)).toEqual(["{{llm_1.response}}", "{{llm_1.usage}}"]);
    expect(list.textContent).toContain("Ask › response");
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(input.value).toBe("Summary: {{llm_1.usage}}");
    expect(screen.queryByTestId("variable-picker")).toBeNull();
    fireEvent.blur(input);
    expect(onCommit).toHaveBeenCalledWith("Summary: {{llm_1.usage}}");
  });

  it("opens from the insert button and explains when no node is upstream", () => {
    render(<VariableField id="cfg-x" value="" groups={availableVariables(sampleDefinition(), catalog, "in")} onCommit={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Insert variable" }));
    const list = screen.getByTestId("variable-picker");
    expect(list.textContent).toContain("{{input.query}}");
    expect(list.textContent).toContain("Connect nodes before this one");
    fireEvent.click(within(list).getByText("{{input}}"));
    expect((screen.getByRole("combobox") as HTMLInputElement).value).toBe("{{input}}");
  });
});

describe("configuration panel", () => {
  beforeEach(() => {
    useEditorStore.getState().load(sampleDefinition());
  });

  it("renders definition-driven fields with labels, help and validation errors", () => {
    const s = useEditorStore.getState();
    s.select(["llm_1"]);
    s.setValidation({ valid: false, warnings: [], errors: [
      { code: "INVALID_NODE_CONFIG", message: "required configuration field is missing", node_id: "llm_1", port: "model" },
    ] });
    render(<Editor><ConfigPanel /></Editor>);
    const panel = screen.getByTestId("config-panel");
    expect(within(panel).getByRole("heading", { name: "LLM" })).toBeTruthy();
    expect(screen.getByLabelText(/^Max tokens/).getAttribute("type")).toBe("number");
    expect(screen.getByTestId("field-error-model").textContent).toContain("Model is required.");
    expect(screen.getByLabelText(/^Model/).getAttribute("aria-invalid")).toBe("true");
    // Credentials: metadata only, filtered by the node's provider.
    const credential = screen.getByLabelText(/^Credential/) as HTMLSelectElement;
    expect([...credential.options].map((o) => o.textContent)).toEqual(["Select a credential…", "Work OpenAI (openai)"]);
    expect(credential.value).toBe("c-1");
    expect(panel.textContent).toContain("Which credential should this node use?");
  });

  it("explains a selected connection with its port types", () => {
    useEditorStore.getState().select([], ["e1"]);
    render(<Editor><ConfigPanel /></Editor>);
    const panel = screen.getByTestId("config-panel");
    expect(panel.textContent).toContain("Input · data json");
    expect(panel.textContent).toContain("Ask · prompt string");
  });
});

describe("validation summary", () => {
  beforeEach(() => useEditorStore.getState().load(sampleDefinition()));

  it("lists problems and selects the node of a clicked problem without changing the workflow", () => {
    const before = useEditorStore.getState().definition;
    useEditorStore.getState().setValidation({ valid: false, warnings: [], errors: [
      { code: "MISSING_REQUIRED_INPUT", message: "required input has no connection", node_id: "out", port: "value" },
      { code: "INVALID_NODE_CONFIG", message: "required configuration field is missing", node_id: "llm_1", port: "model" },
    ] });
    render(<Editor><BottomPanel tab="validation" onTab={() => {}} workflowId="wf" /></Editor>);
    expect(screen.getByTestId("validation-summary").textContent).toContain("2 problems found");
    const list = screen.getByTestId("validation-errors");
    fireEvent.click(within(list).getByText('Input "value" needs a connection.'));
    expect(useEditorStore.getState().selectedNodeIds).toEqual(["out"]);
    fireEvent.click(within(list).getByText("Model is required."));
    expect(useEditorStore.getState().selectedNodeIds).toEqual(["llm_1"]);
    expect(useEditorStore.getState().definition).toBe(before);
  });
});
