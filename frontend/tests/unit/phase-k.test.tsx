// @vitest-environment jsdom
import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { categoriesOf, filterTemplates } from "@/components/templates/TemplateGallery";
import { TemplateDetails } from "@/components/templates/TemplateDetails";
import { appsOf, credentialFills, requirements } from "@/lib/workflow/requirements";
import type { ConfigField, ConnectedAccount, Credential, NodeDefinition, WorkflowNode, WorkflowTemplate } from "@/types/api";

const field = (name: string, extra: Partial<ConfigField> = {}): ConfigField => ({ name, type: "string", required: false, default: "", description: "", ...extra });

function app(type: string, integration: string, name: string, provider: string): NodeDefinition {
  return {
    type, name: `${name}: action`, category: "integration", description: "", role: "", side_effects: "none", inputs: [], outputs: [],
    config: [field("credential_id", { required: true })],
    integration: { id: integration, name, action: "x", category: "" }, auth: { required: true, provider, credential_type: "OAUTH2" },
  };
}
const llm: NodeDefinition = {
  type: "llm", name: "LLM", category: "ai", description: "", role: "", side_effects: "none", inputs: [], outputs: [],
  config: [field("provider", { default: "gemini", options: [{ value: "gemini", label: "Gemini" }] }), field("credential_id")],
};
const input: NodeDefinition = { ...llm, type: "input", name: "Input", category: "general", config: [field("field")] };
const catalog = new Map([
  ["gmail.read", app("gmail.read", "gmail", "Gmail", "google")],
  ["google_sheets.append_row", app("google_sheets.append_row", "google_sheets", "Google Sheets", "google")],
  ["airtable.create_record", app("airtable.create_record", "airtable", "Airtable", "airtable")],
  ["llm", llm], ["input", input],
]);
const node = (id: string, type: string, config: Record<string, unknown> = {}): WorkflowNode => ({ id, type, name: id, position: null, config });

const account = (provider: string, credential_id: string, status: ConnectedAccount["status"] = "ACTIVE") =>
  ({ id: `a-${credential_id}`, provider, credential_id, status, provider_name: provider, email: "", display_name: "" } as ConnectedAccount);
const key = (provider: string, id: string) => ({ id, provider } as Credential);

describe("before you start", () => {
  const def = { nodes: [node("in", "input", { field: "topic" }), node("read", "gmail.read"), node("ai", "llm"), node("log", "google_sheets.append_row")] };

  it("lists one account per service with its apps, the AI key, then the template's notes", () => {
    const needs = requirements(def, catalog, null, ["Make a sheet with the columns Date and Shop."]);
    expect(needs.map((r) => r.label)).toEqual([
      "Connect your Google account (for Gmail, Google Sheets)",
      "Add a free Google Gemini key, so the AI steps can work",
      "Make a sheet with the columns Date and Shop.",
    ]);
    expect(needs[0]).toMatchObject({ kind: "account", href: "/settings/connected-accounts", done: undefined });
  });

  it("ticks off what the workspace already has", () => {
    const needs = requirements(def, catalog, { accounts: [account("google", "c1", "EXPIRED")], credentials: [key("gemini", "k1")] });
    expect(needs.map((r) => r.done)).toEqual([false, true]); // an expired account does not count
    const ready = requirements(def, catalog, { accounts: [account("google", "c1")], credentials: [key("gemini", "k1")] });
    expect(ready.every((r) => r.done)).toBe(true);
  });

  it("fills in accounts and keys only when there is exactly one choice", () => {
    const d = { nodes: [node("read", "gmail.read"), node("ai", "llm"), node("rec", "airtable.create_record"), node("set", "gmail.read", { credential_id: "chosen" })] };
    const fills = credentialFills(d, catalog, [account("google", "g1"), account("airtable", "t1"), account("airtable", "t2"), account("google", "old", "REVOKED")],
      [key("gemini", "k1"), key("openai", "k2")]);
    expect(fills).toEqual([{ nodeId: "read", credentialId: "g1" }, { nodeId: "ai", credentialId: "k1" }]);
  });

  it("names the apps a template uses", () => {
    expect(appsOf(["input", "gmail.read", "llm", "google_sheets.append_row", "gmail.read"], catalog).map((a) => a.name)).toEqual(["Gmail", "Google Sheets"]);
  });
});

const tpl = (id: string, category: string, name: string, extra: Partial<WorkflowTemplate> = {}): WorkflowTemplate => ({
  id, name, description: `${name} description`, category, setup: [], node_types: ["input", "llm", "output"], saved: false,
  definition: { version: 1, settings: {}, edges: [], nodes: [node("in", "input", { field: "question" }), { ...node("ai", "llm"), name: "Answer" }] },
  ...extra,
});

describe("templates gallery", () => {
  const list = [tpl("mine", "Saved", "Our posts", { saved: true }), tpl("inbox", "Email", "Summarize my email", { node_types: ["input", "gmail.read", "llm"] }),
    tpl("blog", "Writing", "Write a blog post"), tpl("dev", "Advanced", "Structured AI Response")];

  it("offers categories in order and hides Advanced unless technical details are on", () => {
    expect(categoriesOf(list, false)).toEqual(["All", "Saved", "Email", "Writing"]);
    expect(categoriesOf(list, true)).toContain("Advanced");
    expect(filterTemplates(list, "All", "", false, catalog).map((t) => t.id)).toEqual(["mine", "inbox", "blog"]);
  });

  it("searches names, descriptions and app names", () => {
    expect(filterTemplates(list, "All", "gmail", false, catalog).map((t) => t.id)).toEqual(["inbox"]);
    expect(filterTemplates(list, "Writing", "blog", false, catalog).map((t) => t.id)).toEqual(["blog"]);
    expect(filterTemplates(list, "Email", "blog", false, catalog)).toHaveLength(0);
  });

  it("shows the steps and the checklist, and uses the template with the chosen name", async () => {
    const onUse = vi.fn().mockResolvedValue(undefined);
    const t = tpl("inbox", "Email", "Summarize my email", { setup: ["Pick the label to read."],
      definition: { version: 1, settings: {}, edges: [], nodes: [node("start", "input"), { ...node("read", "gmail.read"), name: "Read the newest email" }] } });
    render(<TemplateDetails template={t} catalog={catalog} have={{ accounts: [], credentials: [] }} canCreate onClose={() => {}} onUse={onUse} />);
    expect(screen.getByText("Read the newest email")).toBeTruthy();
    const checklist = screen.getByTestId("before-you-start");
    expect(checklist.textContent).toContain("Connect your Google account");
    expect(checklist.textContent).toContain("Pick the label to read.");
    expect(screen.getByRole("link", { name: "Connect Google" }).getAttribute("href")).toBe("/settings/connected-accounts");
    fireEvent.change(screen.getByLabelText("Name your workflow"), { target: { value: "My inbox" } });
    await act(async () => {
      fireEvent.click(screen.getByTestId("use-template"));
    });
    expect(onUse).toHaveBeenCalledWith("My inbox");
  });
});
