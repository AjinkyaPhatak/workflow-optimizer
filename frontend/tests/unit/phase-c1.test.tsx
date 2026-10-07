// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NodeInfoCard } from "@/components/nodes/NodeInfoCard";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { searchNodes } from "@/features/nodes/catalog";
import { authNote } from "@/lib/workflow/labels";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { Credential, NodeDefinition } from "@/types/api";
import { catalog, sampleDefinition } from "./fixtures";

afterEach(cleanup);

// Integration metadata as GET /api/v1/nodes returns it (Phase C1).
const read: NodeDefinition = {
  type: "test_integration.read", name: "Test Integration: Read record", category: "integration",
  description: "Reads a record by key.", role: "", side_effects: "none",
  inputs: [{ name: "key", type: "string", required: false, multiple: false, description: "" }],
  outputs: [{ name: "record", type: "json", required: true, multiple: false, description: "" }],
  config: [
    { name: "credential_id", type: "string", required: true, default: "", description: "The Test Integration account to use.", label: "Account" },
    { name: "key", type: "string", required: false, default: "", description: "", label: "Key" },
  ],
  integration: { id: "test_integration", name: "Test Integration", action: "read", icon: "test" },
  auth: { required: true, provider: "test_integration", credential_type: "API_KEY" },
};

const cred = (id: string, provider: string): Credential => ({
  id, workspace_id: "ws", name: `${provider} key`, provider, credential_type: "API_KEY", created_at: "", updated_at: "",
});

describe("integration metadata", () => {
  it("describes the credential an action needs", () => {
    expect(authNote(read)).toBe("Requires a test_integration api key credential.");
    expect(authNote({ auth: { required: true, provider: "google", credential_type: "OAUTH2" } })).toBe("Requires a google connected account.");
    expect(authNote({})).toBeUndefined();
  });

  it("finds actions by integration name", () => {
    const defs = [...catalog.values(), read];
    expect(searchNodes(defs, "test integration").map((d) => d.type)).toEqual(["test_integration.read"]);
  });

  it("shows the integration and its auth requirement in the info card", () => {
    render(<NodeInfoCard def={read} />);
    const card = screen.getByTestId("node-info");
    expect(card.textContent).toContain("Integration · Test Integration");
    expect(card.textContent).toContain("Requires a test_integration api key credential.");
  });

  it("offers only credentials of the action's provider", () => {
    const def = sampleDefinition();
    def.nodes.push({ id: "read_1", type: read.type, name: "Read", position: { x: 0, y: 0 }, config: {} });
    useEditorStore.getState().load(def);
    useEditorStore.getState().select(["read_1"]);
    const testCatalog = new Map([...catalog, [read.type, read]]);
    const session = {
      loading: false, loadError: null, workflow: null, version: null, catalogList: [...testCatalog.values()], catalog: testCatalog,
      credentials: [cred("c-openai", "openai"), cred("c-test", "test_integration")], role: "owner", busy: null, notice: null,
      save: vi.fn(), validate: vi.fn(), publish: vi.fn(), canPublish: true, versions: null, refreshVersions: vi.fn(), openVersion: vi.fn(),
    } as unknown as EditorSession;
    render(
      <ReactFlowProvider>
        <EditorContext.Provider value={session}><ConfigPanel /></EditorContext.Provider>
      </ReactFlowProvider>,
    );
    const select = screen.getByLabelText(/^Account/) as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toEqual(["", "c-test"]);
  });
});
