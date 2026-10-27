"use client";

import Link from "next/link";
import { useState } from "react";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { ConnectedAccountsState } from "@/features/connected-accounts/useConnectedAccounts";
import { ACCOUNT_PROVIDER_SETUP, accountProviderCards, statusLabel } from "@/lib/connectedAccounts";
import type { ConnectedAccount } from "@/types/api";

function AccountRow({ account, canManage, busy, onDisconnect }: {
  account: ConnectedAccount; canManage: boolean; busy: boolean; onDisconnect: () => void;
}) {
  const tone = account.status === "ACTIVE" ? "ok" : account.status === "DISCONNECTED" ? "off" : "bad";
  return (
    <li className="account-row" data-testid={`account-${account.id}`}>
      <div className="account-who">
        <b>{account.email || account.display_name || "Account"}</b>
        {account.email && account.display_name && <span className="muted small">{account.display_name}</span>}
      </div>
      <span className={`account-status ${tone}`} data-testid="account-status">
        <span aria-hidden>●</span> {statusLabel(account.status)}
      </span>
      <span className="spacer" />
      {canManage && account.status !== "DISCONNECTED" && (
        <button type="button" disabled={busy} onClick={onDisconnect}>
          {busy ? "Disconnecting…" : "Disconnect"}
        </button>
      )}
    </li>
  );
}

/** Supported apps remain discoverable before the server's OAuth client is configured. */
export function ConnectedAccounts({ state, outcome, confirm = (m) => window.confirm(m) }: {
  state: ConnectedAccountsState;
  outcome: { kind: "ok" | "error"; text: string } | null;
  confirm?: (message: string) => boolean;
}) {
  const s = state;
  const [setup, setSetup] = useState<{ provider: string; origin: string } | null>(null);
  const canManage = s.workspace?.role === "owner" || s.workspace?.role === "admin";
  return (
    <main className="dashboard stack" data-testid="connected-accounts">
      <div className="row">
        <h1>Connected accounts</h1>
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
        Workflow nodes in this workspace can use the accounts connected here. A workflow stores only a reference to the
        account; its access stays on the server.
      </p>
      {outcome && (
        <div className={`notice ${outcome.kind}`} role="status" data-testid="connect-outcome">{outcome.text}</div>
      )}
      <ErrorBanner error={s.error} />
      {s.loading ? (
        <div className="empty"><span className="spinner" aria-hidden /> Loading…</div>
      ) : (
        accountProviderCards(s.providers, s.accounts).map((p) => {
          const accounts = s.accounts.filter((a) => a.provider === p.id);
          const configured = s.providers.some(provider => provider.id === p.id);
          const instructions = ACCOUNT_PROVIDER_SETUP.find(provider => provider.id === p.id);
          const setupOpen = setup?.provider === p.id;
          return (
            <section key={p.id} className="card provider-card" data-testid={`provider-${p.id}`}>
              <div className="row">
                <h2>{p.name}</h2>
                <span className="spacer" />
                {canManage && (
                  <button type="button" className="primary" disabled={s.busy === p.id || !s.workspace}
                    aria-expanded={configured ? undefined : setupOpen}
                    aria-controls={configured ? undefined : `setup-${p.id}`}
                    onClick={() => configured ? void s.connect(p.id) : setSetup(setupOpen ? null : { provider: p.id, origin: window.location.origin })}>
                    {!configured ? `Connect ${p.name}` : s.busy === p.id ? "Redirecting…" : accounts.length ? "Connect another account" : "Connect"}
                  </button>
                )}
              </div>
              {p.description && <p className="muted small">{p.description}</p>}
              {!configured && <p className="hint">Server setup needed before {p.name} sign-in is available.</p>}
              {!configured && setupOpen && <div className="stack notice" id={`setup-${p.id}`} role="region" aria-label={`${p.name} connection setup`}>
                <b>Set up {p.name} once, then connect your account</b>
                {instructions ? <>
                  <p>Create a {p.name} OAuth application{p.id === "google" ? " with a Web application client" : ""}. Register this exact redirect URL:</p>
                  <code>{setup.origin}/api/v1/oauth/callback/{p.id}</code>
                  <p>In the server’s root <code>.env</code> file, set <code>{instructions.prefix}_CLIENT_ID</code> and <code>{instructions.prefix}_CLIENT_SECRET</code> from that application. Set <code>{instructions.prefix}_OAUTH_REDIRECT_URI</code> to the redirect URL above.</p>
                  {p.id === "discord" && <p>Also set <code>DISCORD_BOT_TOKEN</code> for the application’s bot.</p>}
                  <p>Restart the app with <code>make dev</code>, refresh this page, then choose Connect. Keep the client secret on the server.</p>
                </> : <p>Ask your server administrator to restore this provider’s OAuth configuration, then refresh this page.</p>}
              </div>}
              {accounts.length === 0 ? (
                <div className="muted small" data-testid="not-connected">Not connected</div>
              ) : (
                <ul className="account-list">
                  {accounts.map((a) => (
                    <AccountRow key={a.id} account={a} canManage={canManage} busy={s.busy === a.id}
                      onDisconnect={() => {
                        const who = a.email || a.display_name || "this account";
                        if (confirm(`Disconnect ${who}? Workflows using it will fail until it is connected again.`)) void s.disconnect(a.id);
                      }} />
                  ))}
                </ul>
              )}
              {canManage && configured && accounts.some((a) => a.status !== "ACTIVE") && (
                <div className="hint">To reconnect an account, choose Connect and sign in with the same account.</div>
              )}
            </section>
          );
        })
      )}
      <section className="card provider-card">
        <div className="row"><h2>WhatsApp</h2><span className="spacer" /><Link href="/settings/whatsapp">Connect WhatsApp →</Link></div>
        <p className="muted small">Link your personal account with a QR code to send messages and files.</p>
      </section>
      {!canManage && s.workspace && <div className="hint">Only workspace owners and admins can connect or disconnect accounts.</div>}
    </main>
  );
}
