"use client";

import { useState } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { CredentialsState } from "@/features/credentials/useCredentials";
import type { CreateCredentialBody } from "@/types/api";

type Kind = "http" | "openai";
type HttpAuth = "bearer_token" | "api_key" | "basic_auth";

const TYPE_LABELS: Record<string, string> = {
  api_key: "API key", bearer_token: "Bearer token", basic_auth: "Basic auth", oauth2: "Connected account",
};
const KIND_LABELS: Record<string, string> = { http: "HTTP API", openai: "OpenAI" };

/** Splits the allowed-hosts box: one host per line (commas also work). */
export function parseHosts(text: string): string[] {
  return text.split(/[\n,]/).map((h) => h.trim()).filter(Boolean);
}

/** The add form. Its secret fields are cleared after every save attempt
 * that succeeds; nothing secret is kept anywhere else. */
function AddCredential({ busy, onCreate }: { busy: boolean; onCreate: CredentialsState["create"] }) {
  const [kind, setKind] = useState<Kind>("http");
  const [auth, setAuth] = useState<HttpAuth>("bearer_token");
  const [name, setName] = useState("");
  const [hosts, setHosts] = useState("");
  const [secret, setSecret] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const basic = kind === "http" && auth === "basic_auth";
  const ready = name.trim() !== "" && (basic ? username.trim() !== "" && password !== "" : secret !== "") &&
    (kind !== "http" || parseHosts(hosts).length > 0);

  const submit = async () => {
    const body: Omit<CreateCredentialBody, "workspace_id"> = kind === "http"
      ? { name: name.trim(), provider: "http", credential_type: auth, secret: basic ? `${username.trim()}:${password}` : secret, allowed_hosts: parseHosts(hosts) }
      : { name: name.trim(), provider: kind, credential_type: "api_key", secret };
    if (await onCreate(body)) {
      setName("");
      setHosts("");
      setSecret("");
      setUsername("");
      setPassword("");
    }
  };

  return (
    <section className="card stack" data-testid="add-credential">
      <h2>Add a credential</h2>
      <div className="field">
        <label htmlFor="cred-kind">For</label>
        <select id="cred-kind" value={kind} onChange={(e) => setKind(e.target.value as Kind)}>
          <option value="http">HTTP Request nodes (any API)</option>
          <option value="openai">OpenAI (LLM nodes)</option>
        </select>
      </div>
      <div className="field">
        <label htmlFor="cred-name">Name</label>
        <input id="cred-name" value={name} placeholder={kind === "http" ? "e.g. Stripe test key" : "e.g. OpenAI"} onChange={(e) => setName(e.target.value)} />
      </div>
      {kind === "http" && (
        <>
          <div className="field">
            <label htmlFor="cred-auth">Sent as</label>
            <select id="cred-auth" value={auth} onChange={(e) => setAuth(e.target.value as HttpAuth)}>
              <option value="bearer_token">Bearer token (Authorization: Bearer …)</option>
              <option value="api_key">API key (a header or query parameter you name in the node)</option>
              <option value="basic_auth">Basic auth (username and password)</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="cred-hosts">Allowed hosts</label>
            <div className="field-help">One per line, e.g. api.stripe.com or *.example.com. The credential is never sent anywhere else.</div>
            <textarea id="cred-hosts" rows={2} value={hosts} onChange={(e) => setHosts(e.target.value)} />
          </div>
        </>
      )}
      {basic ? (
        <div className="row">
          <div className="field" style={{ flex: 1 }}>
            <label htmlFor="cred-user">Username</label>
            <input id="cred-user" autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} />
          </div>
          <div className="field" style={{ flex: 1 }}>
            <label htmlFor="cred-pass">Password</label>
            <input id="cred-pass" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </div>
        </div>
      ) : (
        <div className="field">
          <label htmlFor="cred-secret">{kind === "http" && auth === "bearer_token" ? "Token" : "API key"}</label>
          <input id="cred-secret" type="password" autoComplete="new-password" value={secret} onChange={(e) => setSecret(e.target.value)} />
          <div className="field-help">Stored encrypted on the server. It can never be viewed again, only replaced.</div>
        </div>
      )}
      <div className="row">
        <span className="spacer" />
        <button type="button" className="primary" disabled={!ready || busy} onClick={() => void submit()}>
          {busy ? "Saving…" : "Save credential"}
        </button>
      </div>
    </section>
  );
}

/** Credentials: secrets that nodes use by reference (API keys for LLM and
 * HTTP Request nodes). The browser only ever sees metadata. */
export function Credentials({ state, confirm = (m) => window.confirm(m) }: {
  state: CredentialsState;
  confirm?: (message: string) => boolean;
}) {
  const s = state;
  const canManage = s.workspace?.role === "owner" || s.workspace?.role === "admin";
  const list = s.credentials.filter((c) => c.credential_type !== "oauth2");
  return (
    <main className="dashboard stack" data-testid="credentials">
      <div className="row">
        <h1>Credentials</h1>
        <span className="spacer" />
        {s.workspaces.length > 1 && (
          <select aria-label="Workspace" style={{ width: 240 }} value={s.workspace?.id ?? ""} onChange={(e) => s.setWorkspaceId(e.target.value)}>
            {s.workspaces.map((w) => (
              <option key={w.id} value={w.id}>{w.name}</option>
            ))}
          </select>
        )}
      </div>
      <p className="muted">
        API keys and tokens that nodes in this workspace use. Workflows store only a reference; the secret stays encrypted on
        the server. Accounts like Google or Notion are added under Connected accounts instead.
      </p>
      <ErrorBanner error={s.error} />
      {s.loading ? (
        <div className="empty"><span className="spinner" aria-hidden /> Loading…</div>
      ) : list.length === 0 ? (
        <section className="card empty" data-testid="no-credentials">No credentials yet.</section>
      ) : (
        <section className="card">
          <ul className="account-list">
            {list.map((c) => (
              <li key={c.id} className="account-row" data-testid={`credential-${c.id}`}>
                <div className="account-who">
                  <b>{c.name}</b>
                  <span className="muted small">{KIND_LABELS[c.provider] ?? c.provider} · {TYPE_LABELS[c.credential_type] ?? c.credential_type}</span>
                </div>
                <span className="spacer" />
                {canManage && (
                  <button type="button" disabled={s.busy === c.id}
                    onClick={() => {
                      if (confirm(`Delete ${c.name}? Workflows using it will fail until they use another credential.`)) void s.remove(c.id);
                    }}>
                    {s.busy === c.id ? "Deleting…" : "Delete"}
                  </button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
      {canManage ? (
        <AddCredential busy={s.busy === "create"} onCreate={s.create} />
      ) : (
        s.workspace && <div className="hint">Only workspace owners and admins can add or delete credentials.</div>
      )}
    </main>
  );
}
