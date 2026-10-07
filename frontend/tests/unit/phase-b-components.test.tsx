// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { NodePalette } from "@/components/panels/NodePalette";
import { NewWorkflow } from "@/components/workflow/NewWorkflow";
import { VersionsPanel } from "@/components/workflow/VersionsPanel";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { workflowApi } from "@/lib/api";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { NodeDefinition, VersionSummary } from "@/types/api";
import { catalog, sampleDefinition } from "./fixtures";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const llm: NodeDefinition = {
  ...catalog.get("llm")!,
  config: [
    { name: "provider", type: "string", required: true, default: "openai", description: "Which AI provider runs the model.", label: "Provider",
      options: [{ value: "openai", label: "OpenAI" }, { value: "other", label: "Other AI" }] },
    { name: "model", type: "string", required: true, default: "gpt-5", description: "", label: "Model", allow_custom: true,
      options: [
        { value: "gpt-5", label: "GPT-5", when: { provider: "openai" } },
        { value: "gpt-5-mini", label: "GPT-5 mini", when: { provider: "openai" } },
        { value: "o-1", label: "O1", when: { provider: "other" } },
      ] },
    { name: "temperature", type: "number", required: false, default: 0.2, description: "", label: "Temperature", min: 0, max: 2, step: 0.1 },
  ],
};
const testCatalog = new Map([...catalog, ["llm", llm]]);

const version = (n: number, status: VersionSummary["status"]): VersionSummary => ({
  id: `v${n}`, workflow_id: "wf", version_number: n, status, created_by: null, created_at: "2026-10-01T10:00:00Z", published_at: status === "PUBLISHED" ? "2026-10-01T11:00:00Z" : null,
});

function session(over: Partial<EditorSession> = {}): EditorSession {
  return {
    loading: false, loadError: null, workflow: { id: "wf", project_id: "p", name: "W", description: null, active_version_id: "v2", created_at: "", updated_at: "" },
    version: null, catalogList: [...testCatalog.values()], catalog: testCatalog, credentials: [], role: "owner", busy: null, notice: null,
    save: vi.fn(), validate: vi.fn(), publish: vi.fn(), canPublish: true, versions: null, refreshVersions: vi.fn(), openVersion: vi.fn(),
    ...over,
  };
}

function Editor({ children, s = session() }: { children: ReactNode; s?: EditorSession }) {
  return (
    <ReactFlowProvider>
      <EditorContext.Provider value={s}>{children}</EditorContext.Provider>
    </ReactFlowProvider>
  );
}

describe("option-driven configuration", () => {
  beforeEach(() => {
    useEditorStore.getState().load(sampleDefinition());
    useEditorStore.getState().setConfigValue("llm_1", "model", undefined);
    useEditorStore.getState().select(["llm_1"]);
  });

  it("renders backend options as dropdowns filtered by provider, with custom values", () => {
    render(<Editor><ConfigPanel /></Editor>);
    const provider = screen.getByLabelText(/^Provider/) as HTMLSelectElement;
    expect([...provider.options].map((o) => o.textContent)).toEqual(["Default (OpenAI)", "OpenAI", "Other AI"]);
    expect(provider.value).toBe("openai");
    const model = () => screen.getByLabelText(/^Model/) as HTMLSelectElement;
    expect([...model().options].map((o) => o.textContent)).toEqual(["Default (GPT-5)", "GPT-5", "GPT-5 mini", "Custom…"]);
    fireEvent.change(model(), { target: { value: "gpt-5-mini" } });
    expect(useEditorStore.getState().definition.nodes[1].config.model).toBe("gpt-5-mini");

    // Another provider offers its own models; the current one is flagged.
    fireEvent.change(provider, { target: { value: "other" } });
    expect([...model().options].map((o) => o.textContent)).toEqual(["Default (gpt-5)", "O1", "gpt-5-mini (not available)", "Custom…"]);
    expect(model().getAttribute("aria-invalid")).toBe("true");

    fireEvent.change(model(), { target: { value: "__custom__" } });
    const custom = screen.getByLabelText(/^Model/) as HTMLInputElement;
    fireEvent.change(custom, { target: { value: "my-fine-tune" } });
    fireEvent.blur(custom);
    expect(useEditorStore.getState().definition.nodes[1].config.model).toBe("my-fine-tune");

    const temp = screen.getByLabelText(/^Temperature/) as HTMLInputElement;
    expect([temp.min, temp.max, temp.step]).toEqual(["0", "2", "0.1"]);
  });
});

describe("workflow variables editor", () => {
  beforeEach(() => {
    useEditorStore.getState().load(sampleDefinition());
  });

  it("adds, edits and removes variables in the definition", () => {
    render(<Editor><ConfigPanel /></Editor>);
    fireEvent.click(screen.getByRole("button", { name: "+ Add variable" }));
    expect(useEditorStore.getState().definition.variables).toEqual([{ name: "variable_1", type: "string", default: "" }]);

    const name = screen.getByLabelText("Name", { selector: "#var-0-name" });
    fireEvent.change(name, { target: { value: "customer_name" } });
    fireEvent.blur(name);
    const def = screen.getByLabelText("Default", { selector: "#var-0-default" });
    fireEvent.change(def, { target: { value: "friend" } });
    fireEvent.blur(def);
    expect(useEditorStore.getState().definition.variables?.[0]).toMatchObject({ name: "customer_name", default: "friend" });
    expect(screen.getByTestId("variable-0").textContent).toContain("{{customer_name}}");

    fireEvent.change(screen.getByLabelText("Type", { selector: "#var-0-type" }), { target: { value: "number" } });
    expect(useEditorStore.getState().definition.variables?.[0]).toMatchObject({ type: "number", default: null });
    fireEvent.click(screen.getByLabelText("Required (no default)"));
    const num = screen.getByLabelText("Default", { selector: "#var-0-default" });
    fireEvent.change(num, { target: { value: "3" } });
    fireEvent.blur(num);
    expect(useEditorStore.getState().definition.variables?.[0].default).toBe(3);

    fireEvent.click(screen.getByRole("button", { name: "Remove variable customer_name" }));
    expect(useEditorStore.getState().definition.variables).toBeUndefined();
  });
});

describe("palette search", () => {
  beforeEach(() => useEditorStore.getState().load(sampleDefinition()));

  it("filters, moves with the arrow keys, adds with Enter and clears with Escape", () => {
    const onAdd = vi.fn();
    render(<Editor><NodePalette onAdd={onAdd} /></Editor>);
    const search = screen.getByLabelText("Search nodes");
    fireEvent.change(search, { target: { value: "status_code" } });
    expect(screen.getByTestId("palette-count").textContent).toBe("1 node");
    fireEvent.change(search, { target: { value: "o" } });
    const options = screen.getAllByRole("option");
    expect(options[0].getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(search, { key: "ArrowDown" });
    expect(screen.getAllByRole("option")[1].getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(search, { key: "Enter" });
    expect(onAdd).toHaveBeenCalledWith(expect.objectContaining({ type: screen.getAllByRole("option")[1].dataset.testid!.replace("palette-", "") }));
    fireEvent.keyDown(search, { key: "Escape" });
    expect((search as HTMLInputElement).value).toBe("");
    expect(screen.queryAllByRole("option")).toHaveLength(0); // back to categories
    expect(screen.getByText("General")).toBeTruthy();
  });
});

describe("versions panel", () => {
  beforeEach(() => useEditorStore.getState().load(sampleDefinition()));

  it("shows active, published and draft versions and opens one", async () => {
    const openVersion = vi.fn().mockResolvedValue(undefined);
    const s = session({ versions: [version(3, "DRAFT"), version(2, "PUBLISHED"), version(1, "PUBLISHED")], version: version(3, "DRAFT"), openVersion });
    const onClose = vi.fn();
    render(<Editor s={s}><VersionsPanel onClose={onClose} /></Editor>);
    expect(screen.getByTestId("version-3-state").textContent).toBe("Draft");
    expect(screen.getByTestId("version-3").textContent).toContain("Latest draft");
    expect(screen.getByTestId("version-3").textContent).toContain("Open in editor");
    expect(screen.getByTestId("version-2-state").textContent).toBe("Published · active");
    expect(screen.getByTestId("version-1-state").textContent).toBe("Published");
    await act(async () => {
      fireEvent.click(within(screen.getByTestId("version-2")).getByRole("button", { name: "Open as new draft" }));
    });
    expect(openVersion).toHaveBeenCalledWith("v2");
    expect(onClose).toHaveBeenCalled();
  });
});

describe("new workflow", () => {
  it("offers blank or a template and creates from the choice", async () => {
    vi.spyOn(workflowApi, "templates").mockResolvedValue({ items: [
      { id: "basic-ai-prompt", name: "Basic AI Prompt", description: "Prompt a model.", node_types: ["input", "prompt", "llm", "output"],
        definition: { version: 1, settings: {}, edges: [], nodes: [
          { id: "a", type: "input", name: "Input", position: null, config: {} },
          { id: "b", type: "llm", name: "LLM", position: null, config: {} },
        ] } },
    ] });
    const create = vi.fn().mockResolvedValue({ id: "wf-1" });
    const onCreated = vi.fn();
    render(<NewWorkflow projects={[]} create={create} onCreated={onCreated} />);
    const card = await screen.findByTestId("template-basic-ai-prompt");
    expect(card.textContent).toContain("Input → LLM");
    expect(screen.getByRole("radio", { name: /Blank workflow/ }).getAttribute("aria-checked")).toBe("true");
    fireEvent.click(card);
    fireEvent.change(screen.getByLabelText("Workflow name"), { target: { value: "Summaries" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Create workflow" }));
    });
    expect(create).toHaveBeenCalledWith("Summaries", undefined, "basic-ai-prompt");
    expect(onCreated).toHaveBeenCalledWith({ id: "wf-1" });
  });
});
