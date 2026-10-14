// @vitest-environment jsdom
import { ReactFlowProvider } from "@xyflow/react";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfigPanel } from "@/components/panels/ConfigPanel";
import { DynamicSelect, KeyValueField, rowsToObject } from "@/components/panels/SettingFields";
import { clearFieldOptionsCache, optionsBlocker, type FieldOptions } from "@/features/nodes/useFieldOptions";
import { EditorContext } from "@/features/workflows/EditorContext";
import type { EditorSession } from "@/features/workflows/useEditorSession";
import { nodeApi } from "@/lib/api";
import { fieldControl } from "@/lib/workflow/labels";
import { useEditorStore } from "@/stores/workflow-editor/store";
import type { ConfigField, ConnectedAccount, NodeDefinition } from "@/types/api";
import { catalog, sampleDefinition } from "./fixtures";

afterEach(cleanup);
beforeEach(() => clearFieldOptionsCache());

const choices = (over: Partial<FieldOptions> = {}): FieldOptions => ({ options: [], loading: false, hint: null, reload: vi.fn(), ...over });

const field = (over: Partial<ConfigField>): ConfigField => ({ name: "f", type: "string", required: false, default: "", description: "", ...over });

// Discord Send Message and Sheets Append Row, as GET /nodes describes them.
const discordSend: NodeDefinition = {
  type: "discord.send_message", name: "Discord: Send Message", category: "integration", description: "", role: "", side_effects: "unsafe",
  inputs: [{ name: "content", type: "json", required: false, multiple: false, description: "" }], outputs: [],
  config: [
    { name: "credential_id", type: "string", required: true, default: "", description: "", label: "Account" },
    field({ name: "channel", label: "Channel", required: true, options_source: "channels" }),
  ],
  integration: { id: "discord", name: "Discord", action: "send_message", category: "Discord", icon: "discord" },
  auth: { required: true, provider: "discord", credential_type: "OAUTH2" },
};
const sheetsAppend: NodeDefinition = {
  type: "google_sheets.append_row", name: "Google Sheets: Append Row", category: "integration", description: "", role: "", side_effects: "unsafe",
  inputs: [], outputs: [],
  config: [
    { name: "credential_id", type: "string", required: true, default: "", description: "", label: "Account" },
    field({ name: "spreadsheet", label: "Spreadsheet", required: true }),
    field({ name: "sheet", label: "Sheet", options_source: "sheets", depends_on: ["spreadsheet"] }),
    field({ name: "values", type: "object", label: "Values", default: null, key_value: true, keys_source: "columns", depends_on: ["spreadsheet", "sheet"] }),
  ],
  integration: { id: "google_sheets", name: "Google Sheets", action: "append_row", category: "Google", icon: "sheets" },
  auth: { required: true, provider: "google", credential_type: "OAUTH2" },
};
const fullCatalog = new Map<string, NodeDefinition>([...catalog, [discordSend.type, discordSend], [sheetsAppend.type, sheetsAppend]]);

const account = (provider: string, credential: string): ConnectedAccount => ({
  id: `acc-${provider}`, workspace_id: "ws", provider, provider_name: provider, display_name: "Server", email: "", status: "ACTIVE",
  credential_id: credential, scopes: [], created_at: "", updated_at: "", last_used_at: null,
});

function Editor({ children }: { children: ReactNode }) {
  const s = {
    loading: false, loadError: null, workflow: null, workspaceId: "ws", version: null, catalogList: [...fullCatalog.values()], catalog: fullCatalog,
    credentials: [], connectedAccounts: [account("discord", "cred-d"), account("google", "cred-g")], role: "owner", busy: null, notice: null,
  } as unknown as EditorSession;
  return (
    <ReactFlowProvider>
      <EditorContext.Provider value={s}>{children}</EditorContext.Provider>
    </ReactFlowProvider>
  );
}

function select(type: string, config: Record<string, unknown>) {
  const d = sampleDefinition();
  d.nodes.push({ id: "n_1", type, name: "N", position: { x: 0, y: 0 }, config });
  useEditorStore.getState().load(d);
  useEditorStore.getState().select(["n_1"]);
}

const stored = () => useEditorStore.getState().definition.nodes.find((n) => n.id === "n_1")!.config;

describe("field controls", () => {
  it("maps C8 hints to controls", () => {
    expect(fieldControl(field({ type: "object", key_value: true }))).toBe("key_value");
    expect(fieldControl(field({ options_source: "channels" }))).toBe("dynamic");
    expect(fieldControl(field({ type: "object" }))).toBe("json");
  });

  it("explains what a choice list waits for", () => {
    expect(optionsBlocker(discordSend, [], {})).toBe("Choose an account to list choices.");
    expect(optionsBlocker(discordSend, [], { credential_id: "c" })).toBeNull();
    expect(optionsBlocker(sheetsAppend, ["spreadsheet", "sheet"], { credential_id: "c" })).toBe("Set Spreadsheet first.");
    // Sheet is optional: the columns of the first tab load without it.
    expect(optionsBlocker(sheetsAppend, ["spreadsheet", "sheet"], { credential_id: "c", spreadsheet: "x" })).toBeNull();
    expect(optionsBlocker(sheetsAppend, ["spreadsheet"], { credential_id: "c", spreadsheet: "{{input.id}}" })).toBe("Set Spreadsheet first.");
  });

  it("stores rows as an object", () => {
    expect(rowsToObject([{ key: " Name ", value: "{{a.b}}" }, { key: "", value: "x" }, { key: "Name", value: "Ada" }])).toEqual({ Name: "Ada" });
    expect(rowsToObject([{ key: "Score", value: "7", original: 7 }, { key: "Done", value: "yes", original: false }])).toEqual({ Score: 7, Done: "yes" });
    // A new empty row is not stored (it would clear the cell); a stored one stays.
    expect(rowsToObject([{ key: "Status", value: "" }])).toBeUndefined();
    expect(rowsToObject([{ key: "Status", value: "", original: "" }])).toEqual({ Status: "" });
  });
});

describe("KeyValueField", () => {
  it("edits, removes and adds suggested names", () => {
    const onCommit = vi.fn();
    const suggestions = choices({ options: [{ value: "Name", label: "Name" }, { value: "Email", label: "Email" }, { value: "Status", label: "Status" }] });
    // Stored like the editor stores it: a commit becomes the new value.
    function Stateful() {
      const [v, setV] = useState<unknown>({ Name: "{{in.name}}" });
      return <KeyValueField id="kv" value={v} groups={[]} suggestions={suggestions} onCommit={(x) => { onCommit(x); setV(x); }} />;
    }
    render(<Stateful />);
    const key = screen.getByLabelText("Name 1") as HTMLInputElement;
    expect(key.value).toBe("Name");
    fireEvent.change(key, { target: { value: "Full name" } });
    fireEvent.blur(key);
    expect(onCommit).toHaveBeenLastCalledWith({ "Full name": "{{in.name}}" });
    fireEvent.click(screen.getByRole("button", { name: /Add all 3 names/ }));
    expect(screen.getAllByPlaceholderText("Name")).toHaveLength(4);
    fireEvent.click(screen.getByRole("button", { name: "Remove Full name" }));
    expect(onCommit).toHaveBeenLastCalledWith(undefined); // the added names are still empty
  });

  it("shows why names are not listed yet", () => {
    render(<KeyValueField id="kv" value={undefined} onCommit={vi.fn()} groups={[]} suggestions={choices({ hint: "Set Spreadsheet first." })} />);
    expect(screen.getByTestId("kv-hint").textContent).toBe("Set Spreadsheet first.");
  });
});

describe("DynamicSelect", () => {
  it("picks from the list or takes a typed value", () => {
    const onCommit = vi.fn();
    const c = choices({ options: [{ value: "1", label: "#general" }, { value: "2", label: "#news" }] });
    const { rerender } = render(<DynamicSelect id="ch" value={undefined} choices={c} onCommit={onCommit} />);
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "2" } });
    expect(onCommit).toHaveBeenLastCalledWith("2");
    // A value the list does not have (a link, a name) shows as text.
    rerender(<DynamicSelect id="ch" value="https://discord.com/channels/1/9" choices={c} onCommit={onCommit} />);
    const input = screen.getByRole("textbox") as HTMLInputElement;
    expect(input.value).toBe("https://discord.com/channels/1/9");
    fireEvent.click(screen.getByRole("button", { name: "Choose from the list" }));
    expect(onCommit).toHaveBeenLastCalledWith(undefined);
  });

  it("falls back to typing when nothing is listed", () => {
    const onCommit = vi.fn();
    render(<DynamicSelect id="ch" value={undefined} choices={choices({ hint: "Choose an account to list choices." })} onCommit={onCommit} />);
    const input = screen.getByRole("textbox");
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: " general " } });
    fireEvent.blur(input);
    expect(onCommit).toHaveBeenCalledWith("general");
    expect(screen.getByTestId("ch-hint").textContent).toContain("Choose an account");
  });
});

describe("config panel with account-backed choices", () => {
  it("loads Discord channels once an account is chosen", async () => {
    const spy = vi.spyOn(nodeApi, "options").mockResolvedValue({ items: [{ value: "200", label: "#general" }] });
    select("discord.send_message", { credential_id: "cred-d" });
    render(<Editor><ConfigPanel /></Editor>);
    await waitFor(() => expect(screen.getByRole("option", { name: "#general" })).toBeTruthy());
    expect(spy).toHaveBeenCalledWith("discord.send_message", { workspace_id: "ws", source: "channels", config: { credential_id: "cred-d" } });
    fireEvent.change(screen.getByLabelText(/^Channel/), { target: { value: "200" } });
    expect(stored().channel).toBe("200");
    spy.mockRestore();
  });

  it("suggests sheet columns as Values names and sends only settings", async () => {
    const spy = vi.spyOn(nodeApi, "options").mockImplementation(async (_t, body) => ({
      items: body.source === "columns" ? [{ value: "Email", label: "Email" }] : [{ value: "Leads", label: "Leads" }],
    }));
    select("google_sheets.append_row", { credential_id: "cred-g", spreadsheet: "sheet-1", values: { Email: "{{in.data}}" } });
    render(<Editor><ConfigPanel /></Editor>);
    await waitFor(() => expect(spy).toHaveBeenCalledWith("google_sheets.append_row",
      { workspace_id: "ws", source: "columns", config: { credential_id: "cred-g", spreadsheet: "sheet-1" } }));
    expect((screen.getByLabelText("Name 1") as HTMLInputElement).value).toBe("Email");
    spy.mockRestore();
  });

  it("does not call the API before an account is chosen", () => {
    const spy = vi.spyOn(nodeApi, "options");
    select("discord.send_message", {});
    render(<Editor><ConfigPanel /></Editor>);
    expect(spy).not.toHaveBeenCalled();
    expect(screen.getByTestId("cfg-channel-hint").textContent).toBe("Choose an account to list choices.");
    spy.mockRestore();
  });
});
