// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { NodePalette } from "@/components/panels/NodePalette";
import { ConnectedAccounts } from "@/components/settings/ConnectedAccounts";
import type { ConnectedAccountsState } from "@/features/connected-accounts/useConnectedAccounts";
import { actionName, groupByCategory, originLabel, searchNodes } from "@/features/nodes/catalog";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { ConfigField, ConnectedAccount, NodeDefinition, PortDefinition } from "@/types/api";
import { catalog, sampleDefinition } from "./fixtures";

afterEach(cleanup);

// Gmail metadata as GET /api/v1/nodes returns it (Phase C3).
const port = (name: string, type = "string"): PortDefinition => ({ name, type, required: false, multiple: false, description: "" });
const account: ConfigField = { name: "credential_id", type: "string", required: true, default: "", description: "The Gmail account to use.", label: "Account" };
const text = (name: string, label: string, required = false): ConfigField => ({ name, type: "string", required, default: "", description: "", label });

function gmail(action: string, name: string, side: string, inputs: PortDefinition[], outputs: PortDefinition[], config: ConfigField[]): NodeDefinition {
  return {
    type: `gmail.${action}`, name: `Gmail: ${name}`, category: "integration", description: `${name} with Gmail.`, role: "",
    side_effects: side, inputs, outputs, config: [account, ...config],
    integration: { id: "gmail", name: "Gmail", action, category: "Google", icon: "gmail" },
    auth: { required: true, provider: "google", credential_type: "OAUTH2" },
  };
}

const gmailDefs = [
  gmail("search", "Search Emails", "none", [port("query")], [port("messages", "array"), port("count", "number")], [
    text("query", "Query"),
    { name: "max_results", type: "number", required: false, default: 10, description: "", label: "Max results", min: 1, max: 50, step: 1 },
  ]),
  gmail("read", "Read Email", "none", [port("message_id")], [port("body")], [text("message_id", "Message ID")]),
  gmail("create_draft", "Create Draft", "unsafe", [port("body")], [port("draft_id")], [text("to", "To"), text("subject", "Subject"), text("body", "Body")]),
  gmail("send", "Send Email", "unsafe", [port("body")], [port("message_id")], [text("to", "To", true), text("subject", "Subject"), text("body", "Body")]),
  gmail("reply", "Reply", "unsafe", [port("message_id"), port("body")], [port("message_id")], [text("message_id", "Message ID"), text("body", "Body")]),
];
const fullCatalog = new Map<string, NodeDefinition>([...catalog, ...gmailDefs.map((d) => [d.type, d] as [string, NodeDefinition])]);

const googleAccount = (over: Partial<ConnectedAccount> = {}): ConnectedAccount => ({
  id: "acc-g", workspace_id: "ws", provider: "google", provider_name: "Google", display_name: "Ada", email: "user@gmail.com",
  status: "ACTIVE", credential_id: "cred-g", scopes: [], created_at: "", updated_at: "", last_used_at: null, ...over,
});

function session(accounts: ConnectedAccount[] = []): EditorSession {
  return {
    loading: false, loadError: null, workflow: null, version: null, catalogList: [...fullCatalog.values()], catalog: fullCatalog,
    credentials: [], connectedAccounts: accounts, role: "owner", busy: null, notice: null,
    save: vi.fn(), validate: vi.fn(), publish: vi.fn(), canPublish: true, versions: null, refreshVersions: vi.fn(), openVersion: vi.fn(),
  } as unknown as EditorSession;
}

function Editor({ children, s = session() }: { children: ReactNode; s?: EditorSession }) {
  return (
    <ReactFlowProvider>
      <EditorContext.Provider value={s}>{children}</EditorContext.Provider>
    </ReactFlowProvider>
  );
}

describe("Gmail in the catalog", () => {
  it("groups Gmail actions under Google with their action names", () => {
    const groups = groupByCategory([...fullCatalog.values()]);
    const google = groups.find(([g]) => g === "Google");
    expect(google?.[1].map(actionName)).toEqual(["Create Draft", "Read Email", "Reply", "Search Emails", "Send Email"]);
    // Non-Gmail integration nodes (HTTP) stay in their category.
    expect(groups.find(([g]) => g === "integration")?.[1].every((d) => !d.integration)).toBe(true);
    expect(originLabel(gmailDefs[0])).toBe("Google · Gmail");
    expect(searchNodes([...fullCatalog.values()], "gmail").map((d) => d.type)).toHaveLength(5);
    expect(searchNodes([...fullCatalog.values()], "google send").map((d) => d.type)).toEqual(["gmail.send"]);
  });

  it("shows Google > Gmail > actions in the palette", () => {
    useEditorStore.getState().load(sampleDefinition());
    const onAdd = vi.fn();
    render(<Editor><NodePalette onAdd={onAdd} /></Editor>);
    const heading = screen.getByRole("heading", { name: "Google" });
    const section = heading.closest("section")!;
    expect(within(section).getByRole("heading", { name: "Gmail" })).toBeTruthy();
    expect([...section.querySelectorAll(".palette-name")].map((n) => n.textContent)).toEqual(
      ["Create Draft", "Read Email", "Reply", "Search Emails", "Send Email"]);
    fireEvent.doubleClick(within(section).getByTestId("palette-gmail.search"));
    expect(onAdd).toHaveBeenCalledWith(expect.objectContaining({ type: "gmail.search" }));
    fireEvent.change(screen.getByLabelText("Search nodes"), { target: { value: "send email" } });
    expect(screen.getByTestId("palette-gmail.send").textContent).toContain("Google · Gmail");
  });
});

describe("Gmail node configuration", () => {
  function renderNode(type: string, accounts: ConnectedAccount[]) {
    const def = sampleDefinition();
    def.nodes.push({ id: "g_1", type, name: "Gmail", position: { x: 0, y: 0 }, config: {} });
    useEditorStore.getState().load(def);
    useEditorStore.getState().select(["g_1"]);
    render(<Editor s={session(accounts)}><ConfigPanel /></Editor>);
  }

  it("selects a Google account and stores only its credential reference", () => {
    renderNode("gmail.search", [googleAccount(), googleAccount({ id: "acc-x", provider: "fake_oauth", credential_id: "cred-x", email: "x@example.test" })]);
    const select = screen.getByLabelText(/^Account/) as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["Select an account…", "Google — user@gmail.com"]);
    fireEvent.change(select, { target: { value: "cred-g" } });
    const query = screen.getByLabelText(/^Query/) as HTMLInputElement;
    fireEvent.change(query, { target: { value: "from:foo@example.com" } });
    fireEvent.blur(query);
    const max = screen.getByLabelText(/^Max results/) as HTMLInputElement;
    expect([max.min, max.max, max.step]).toEqual(["1", "50", "1"]);
    const node = useEditorStore.getState().definition.nodes.find((n) => n.id === "g_1")!;
    expect(node.config).toEqual({ credential_id: "cred-g", query: "from:foo@example.com" });
    expect(JSON.stringify(useEditorStore.getState().definition)).not.toContain("user@gmail.com");
  });

  it("explains when no Google account is connected", () => {
    renderNode("gmail.send", []);
    expect([...(screen.getByLabelText(/^Account/) as HTMLSelectElement).options].map((o) => o.textContent)).toEqual(["No account connected"]);
    expect(screen.getByRole("link", { name: "Manage connected accounts" })).toBeTruthy();
    expect(screen.getByLabelText(/^To/)).toBeTruthy();
    expect(screen.getByLabelText(/^Subject/)).toBeTruthy();
  });
});

describe("Google on the Connected Accounts page", () => {
  const state = (accounts: ConnectedAccount[]): ConnectedAccountsState => ({
    loading: false, error: null, workspaces: [{ id: "ws", name: "Main", role: "owner" }], workspace: { id: "ws", name: "Main", role: "owner" },
    setWorkspaceId: vi.fn(), providers: [{ id: "google", name: "Google", description: "Gmail: search and read email, create drafts, send and reply.", scopes: [] }],
    accounts, busy: null, connect: vi.fn(), disconnect: vi.fn(),
  });

  it("offers Connect for Google and shows a connected Gmail account", () => {
    const s = state([]);
    const { unmount } = render(<ConnectedAccounts state={s} outcome={null} />);
    const card = screen.getByTestId("provider-google");
    expect(card.textContent).toContain("Gmail: search and read email");
    fireEvent.click(within(card).getByRole("button", { name: "Connect" }));
    expect(s.connect).toHaveBeenCalledWith("google");
    unmount();
    render(<ConnectedAccounts state={state([googleAccount()])} outcome={null} />);
    const row = screen.getByTestId("account-acc-g");
    expect(row.textContent).toContain("user@gmail.com");
    expect(within(row).getByTestId("account-status").textContent).toContain("Connected");
  });
});
