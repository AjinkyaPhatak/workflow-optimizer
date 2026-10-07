// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { ConnectedAccounts } from "@/components/settings/ConnectedAccounts";
import { useConnectedAccounts, type ConnectedAccountsState } from "@/features/connected-accounts/useConnectedAccounts";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { authApi, connectedAccountApi } from "@/lib/api";
import { accountLabel, callbackOutcome } from "@/lib/connectedAccounts";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { ConnectedAccount, NodeDefinition, OAuthProvider } from "@/types/api";
import { catalog, sampleDefinition } from "./fixtures";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const provider: OAuthProvider = { id: "fake_oauth", name: "Fake OAuth", description: "Test provider.", scopes: ["records.read"] };

const account = (over: Partial<ConnectedAccount> = {}): ConnectedAccount => ({
  id: "acc-1", workspace_id: "ws-1", provider: "fake_oauth", provider_name: "Fake OAuth", display_name: "Ada",
  email: "ada@example.test", status: "ACTIVE", credential_id: "cred-1", scopes: ["records.read"],
  created_at: "2026-10-07T10:00:00Z", updated_at: "2026-10-07T10:00:00Z", last_used_at: null, ...over,
});

function state(over: Partial<ConnectedAccountsState> = {}): ConnectedAccountsState {
  return {
    loading: false, error: null, workspaces: [{ id: "ws-1", name: "Main", role: "owner" }], workspace: { id: "ws-1", name: "Main", role: "owner" },
    setWorkspaceId: vi.fn(), providers: [provider], accounts: [], busy: null,
    connect: vi.fn().mockResolvedValue(undefined), disconnect: vi.fn().mockResolvedValue(undefined), ...over,
  };
}

describe("connected accounts helpers", () => {
  it("labels accounts and turns the callback outcome into a message", () => {
    expect(accountLabel(account())).toBe("Fake OAuth — ada@example.test");
    expect(accountLabel(account({ email: "" }))).toBe("Fake OAuth — Ada");
    expect(callbackOutcome(new URLSearchParams("connected=fake_oauth&account=acc-1"))).toEqual({ kind: "ok", text: "Account connected." });
    expect(callbackOutcome(new URLSearchParams("error=invalid_state"))?.text).toMatch(/expired or was already used/);
    expect(callbackOutcome(new URLSearchParams("error=authorization_denied"))?.text).toBe("Access was not granted.");
    expect(callbackOutcome(new URLSearchParams("error=<script>"))?.text).toBe("The account could not be connected.");
    expect(callbackOutcome(new URLSearchParams(""))).toBeNull();
  });
});

describe("Connected Accounts page", () => {
  it("shows a clear empty state when no provider is configured", () => {
    render(<ConnectedAccounts state={state({ providers: [] })} outcome={null} />);
    expect(screen.getByTestId("no-providers").textContent).toContain("No account providers are configured");
    expect(screen.queryByRole("button", { name: /Connect/ })).toBeNull();
  });

  it("offers to connect a provider without accounts", () => {
    const s = state();
    render(<ConnectedAccounts state={s} outcome={null} />);
    const card = screen.getByTestId("provider-fake_oauth");
    expect(within(card).getByTestId("not-connected").textContent).toBe("Not connected");
    fireEvent.click(within(card).getByRole("button", { name: "Connect" }));
    expect(s.connect).toHaveBeenCalledWith("fake_oauth");
  });

  it("shows a connected account and disconnects it after confirmation", () => {
    const s = state({ accounts: [account()] });
    const confirm = vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true);
    render(<ConnectedAccounts state={s} outcome={{ kind: "ok", text: "Account connected." }} confirm={confirm} />);
    expect(screen.getByTestId("connect-outcome").textContent).toBe("Account connected.");
    const row = screen.getByTestId("account-acc-1");
    expect(row.textContent).toContain("ada@example.test");
    expect(within(row).getByTestId("account-status").textContent).toContain("Connected");
    fireEvent.click(within(row).getByRole("button", { name: "Disconnect" }));
    expect(s.disconnect).not.toHaveBeenCalled();
    fireEvent.click(within(row).getByRole("button", { name: "Disconnect" }));
    expect(s.disconnect).toHaveBeenCalledWith("acc-1");
    expect(screen.getByRole("button", { name: "Connect another account" })).toBeTruthy();
  });

  it("shows disconnected and revoked accounts with how to reconnect", () => {
    render(<ConnectedAccounts state={state({ accounts: [account({ status: "DISCONNECTED" }), account({ id: "acc-2", status: "REVOKED", email: "b@example.test" })] })} outcome={null} />);
    const off = screen.getByTestId("account-acc-1");
    expect(within(off).getByTestId("account-status").textContent).toContain("Disconnected");
    expect(within(off).queryByRole("button", { name: "Disconnect" })).toBeNull();
    expect(within(screen.getByTestId("account-acc-2")).getByTestId("account-status").textContent).toContain("Access revoked");
    expect(screen.getByText(/To reconnect an account/)).toBeTruthy();
  });

  it("lets members see but not manage accounts", () => {
    render(<ConnectedAccounts state={state({ accounts: [account()], workspace: { id: "ws-1", name: "Main", role: "member" } })} outcome={null} />);
    expect(screen.queryByRole("button", { name: /Connect|Disconnect/ })).toBeNull();
    expect(screen.getByText(/Only workspace owners and admins/)).toBeTruthy();
  });
});

describe("connect flow in the browser", () => {
  it("asks the backend to start the flow and goes to the provider; nothing is stored in the browser", async () => {
    vi.spyOn(authApi, "me").mockResolvedValue({ user: { id: "u", email: "a@example.test", name: "A" } as never, workspaces: [{ id: "ws-1", name: "Main", role: "owner" }] });
    vi.spyOn(connectedAccountApi, "providers").mockResolvedValue({ items: [provider] } as never);
    vi.spyOn(connectedAccountApi, "list").mockResolvedValue({ items: [] } as never);
    const authorize = vi.spyOn(connectedAccountApi, "authorize").mockResolvedValue({
      authorization_url: "https://oauth.fake.test/authorize?state=s&client_id=c", expires_at: "2026-10-07T12:10:00Z" });
    const navigate = vi.fn();
    const before = { local: { ...window.localStorage }, session: { ...window.sessionStorage } };
    const { result } = renderHook(() => useConnectedAccounts(navigate));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.providers).toEqual([provider]);
    await act(async () => {
      await result.current.connect("fake_oauth");
    });
    expect(authorize).toHaveBeenCalledWith("fake_oauth", "ws-1");
    expect(navigate).toHaveBeenCalledWith("https://oauth.fake.test/authorize?state=s&client_id=c");
    expect({ ...window.localStorage }).toEqual(before.local);
    expect({ ...window.sessionStorage }).toEqual(before.session);
  });

  it("disconnects through the API and reloads the list", async () => {
    vi.spyOn(authApi, "me").mockResolvedValue({ user: {} as never, workspaces: [{ id: "ws-1", name: "Main", role: "owner" }] });
    vi.spyOn(connectedAccountApi, "providers").mockResolvedValue({ items: [provider] } as never);
    const list = vi.spyOn(connectedAccountApi, "list")
      .mockResolvedValueOnce({ items: [account()] } as never)
      .mockResolvedValueOnce({ items: [account({ status: "DISCONNECTED" })] } as never);
    const disconnect = vi.spyOn(connectedAccountApi, "disconnect").mockResolvedValue(account({ status: "DISCONNECTED" }));
    const { result } = renderHook(() => useConnectedAccounts(vi.fn()));
    await waitFor(() => expect(result.current.accounts).toHaveLength(1));
    await act(async () => {
      await result.current.disconnect("acc-1");
    });
    expect(disconnect).toHaveBeenCalledWith("acc-1");
    await waitFor(() => expect(result.current.accounts[0].status).toBe("DISCONNECTED"));
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("sends the workspace to the authorize endpoint and nothing else", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ authorization_url: "https://x", expires_at: "" }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await connectedAccountApi.authorize("fake_oauth", "ws-1");
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/connected-accounts/fake_oauth/authorize");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ workspace_id: "ws-1" });
    vi.unstubAllGlobals();
  });
});

describe("account selection on OAuth nodes", () => {
  const oauthNode: NodeDefinition = {
    type: "test_oauth_integration.read", name: "Test OAuth Integration: Read record", category: "integration",
    description: "Reads a record.", role: "", side_effects: "none",
    inputs: [{ name: "key", type: "string", required: false, multiple: false, description: "" }],
    outputs: [{ name: "record", type: "json", required: true, multiple: false, description: "" }],
    config: [{ name: "credential_id", type: "string", required: true, default: "", description: "The account to use.", label: "Account" }],
    integration: { id: "test_oauth_integration", name: "Test OAuth Integration", action: "read" },
    auth: { required: true, provider: "fake_oauth", credential_type: "OAUTH2" },
  };

  function renderPanel(accounts: ConnectedAccount[]) {
    const def = sampleDefinition();
    def.nodes.push({ id: "read_1", type: oauthNode.type, name: "Read", position: { x: 0, y: 0 }, config: {} });
    useEditorStore.getState().load(def);
    useEditorStore.getState().select(["read_1"]);
    const testCatalog = new Map([...catalog, [oauthNode.type, oauthNode]]);
    const session = {
      loading: false, loadError: null, workflow: null, version: null, catalogList: [...testCatalog.values()], catalog: testCatalog,
      credentials: [{ id: "cred-1", workspace_id: "ws-1", name: "Fake OAuth — ada@example.test", provider: "fake_oauth", credential_type: "oauth2", created_at: "", updated_at: "" }],
      connectedAccounts: accounts, role: "owner", busy: null, notice: null,
      save: vi.fn(), validate: vi.fn(), publish: vi.fn(), canPublish: true, versions: null, refreshVersions: vi.fn(), openVersion: vi.fn(),
    } as unknown as EditorSession;
    render(
      <ReactFlowProvider>
        <EditorContext.Provider value={session}><ConfigPanel /></EditorContext.Provider>
      </ReactFlowProvider>,
    );
  }

  it("lists the provider's accounts and stores only the credential reference", () => {
    renderPanel([account(), account({ id: "acc-2", credential_id: "cred-2", email: "old@example.test", status: "DISCONNECTED" }),
      account({ id: "acc-3", provider: "other", credential_id: "cred-3", email: "x@example.test" })]);
    const select = screen.getByLabelText(/^Account/) as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual([
      "Select an account…", "Fake OAuth — ada@example.test", "Fake OAuth — old@example.test (disconnected)"]);
    fireEvent.change(select, { target: { value: "cred-1" } });
    const node = useEditorStore.getState().definition.nodes.find((n) => n.id === "read_1")!;
    expect(node.config).toEqual({ credential_id: "cred-1" });
    const json = JSON.stringify(useEditorStore.getState().definition);
    expect(json).not.toContain("ada@example.test");
    expect(json).not.toMatch(/token|secret/i);
    expect(screen.getByRole("link", { name: "Manage connected accounts" }).getAttribute("href")).toBe("/settings/connected-accounts");
  });

  it("warns when the selected account needs reconnecting", () => {
    renderPanel([account({ status: "EXPIRED" })]);
    fireEvent.change(screen.getByLabelText(/^Account/), { target: { value: "cred-1" } });
    expect(screen.getByRole("alert").textContent).toContain("Reconnect it to run this node");
  });

  it("explains when no account is connected", () => {
    renderPanel([]);
    const select = screen.getByLabelText(/^Account/) as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["No account connected"]);
    expect(screen.getByText(/No account of this provider is connected/)).toBeTruthy();
  });
});
