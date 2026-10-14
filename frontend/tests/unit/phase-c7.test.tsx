// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Credentials, parseHosts } from "@/components/settings/Credentials";
import type { CredentialsState } from "@/features/credentials/useCredentials";
import type { Credential } from "@/types/api";

afterEach(cleanup);

const cred = (over: Partial<Credential> = {}): Credential => ({
  id: "c1", workspace_id: "ws", name: "Stripe", provider: "http", credential_type: "bearer_token", created_at: "", updated_at: "", ...over,
});

function state(over: Partial<CredentialsState> = {}): CredentialsState {
  return {
    loading: false, error: null, workspaces: [{ id: "ws", name: "Main", role: "owner" }], workspace: { id: "ws", name: "Main", role: "owner" },
    setWorkspaceId: vi.fn(), credentials: [], busy: null, create: vi.fn().mockResolvedValue(true), remove: vi.fn(), ...over,
  };
}

const type = (label: RegExp, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

describe("Credentials page", () => {
  it("parses allowed hosts one per line or comma-separated", () => {
    expect(parseHosts(" api.stripe.com \n\n*.example.com, b.test ")).toEqual(["api.stripe.com", "*.example.com", "b.test"]);
  });

  it("lists credentials without OAuth ones and deletes after confirming", () => {
    const s = state({ credentials: [cred(), cred({ id: "c2", name: "OpenAI", provider: "openai", credential_type: "api_key" }), cred({ id: "c3", credential_type: "oauth2" })] });
    render(<Credentials state={s} confirm={() => true} />);
    expect(screen.getByTestId("credential-c1").textContent).toContain("HTTP API · Bearer token");
    expect(screen.getByTestId("credential-c2").textContent).toContain("OpenAI · API key");
    expect(screen.queryByTestId("credential-c3")).toBeNull();
    fireEvent.click(screen.getByTestId("credential-c1").querySelector("button")!);
    expect(s.remove).toHaveBeenCalledWith("c1");
  });

  it("creates an HTTP bearer credential with its hosts and clears the secret", async () => {
    const s = state();
    render(<Credentials state={s} />);
    const save = screen.getByRole("button", { name: "Save credential" }) as HTMLButtonElement;
    type(/^Name/, "Stripe test");
    type(/^Token/, "sk_test_123");
    expect(save.disabled).toBe(true); // hosts are required for HTTP credentials
    type(/^Allowed hosts/, "api.stripe.com\n*.stripe.com");
    expect((screen.getByLabelText(/^Token/) as HTMLInputElement).type).toBe("password");
    fireEvent.click(save);
    expect(s.create).toHaveBeenCalledWith({ name: "Stripe test", provider: "http", credential_type: "bearer_token", secret: "sk_test_123",
      allowed_hosts: ["api.stripe.com", "*.stripe.com"] });
    await waitFor(() => expect((screen.getByLabelText(/^Token/) as HTMLInputElement).value).toBe(""));
  });

  it("creates basic auth from a username and password, and OpenAI keys without hosts", () => {
    const s = state();
    render(<Credentials state={s} />);
    type(/^Name/, "Jira");
    fireEvent.change(screen.getByLabelText(/^Sent as/), { target: { value: "basic_auth" } });
    type(/^Allowed hosts/, "acme.atlassian.net");
    type(/^Username/, "ada@example.test");
    type(/^Password/, "p:w");
    fireEvent.click(screen.getByRole("button", { name: "Save credential" }));
    expect(s.create).toHaveBeenLastCalledWith(expect.objectContaining({ credential_type: "basic_auth", secret: "ada@example.test:p:w" }));

    fireEvent.change(screen.getByLabelText(/^For/), { target: { value: "openai" } });
    expect(screen.queryByLabelText(/^Allowed hosts/)).toBeNull();
    type(/^API key/, "sk-openai");
    fireEvent.click(screen.getByRole("button", { name: "Save credential" }));
    expect(s.create).toHaveBeenLastCalledWith({ name: "Jira", provider: "openai", credential_type: "api_key", secret: "sk-openai" });
  });

  it("only lets owners and admins manage credentials", () => {
    render(<Credentials state={state({ credentials: [cred()], workspace: { id: "ws", name: "Main", role: "member" } })} />);
    expect(screen.queryByTestId("add-credential")).toBeNull();
    expect(screen.getByTestId("credential-c1").querySelector("button")).toBeNull();
    expect(screen.getByText(/Only workspace owners and admins/)).toBeTruthy();
  });
});
