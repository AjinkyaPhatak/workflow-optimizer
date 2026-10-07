"use client";

import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { ConnectedAccountsState } from "@/features/connected-accounts/useConnectedAccounts";
import { statusLabel } from "@/lib/connectedAccounts";
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

/** Connected Accounts: one card per OAuth provider configured on the server
 * (none by default), listing the workspace's accounts of that provider. The
 * browser only ever sees account metadata. */
export function ConnectedAccounts({ state, outcome, confirm = (m) => window.confirm(m) }: {
  state: ConnectedAccountsState;
  outcome: { kind: "ok" | "error"; text: string } | null;
  confirm?: (message: string) => boolean;
}) {
  const s = state;
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
      ) : s.providers.length === 0 ? (
        <section className="card empty" data-testid="no-providers">
          <b>No account providers are configured</b>
          <div className="small">A provider has to be set up on the server before accounts can be connected.</div>
        </section>
      ) : (
        s.providers.map((p) => {
          const accounts = s.accounts.filter((a) => a.provider === p.id);
          return (
            <section key={p.id} className="card provider-card" data-testid={`provider-${p.id}`}>
              <div className="row">
                <h2>{p.name}</h2>
                <span className="spacer" />
                {canManage && (
                  <button type="button" className="primary" disabled={s.busy === p.id} onClick={() => void s.connect(p.id)}>
                    {s.busy === p.id ? "Redirecting…" : accounts.length ? "Connect another account" : "Connect"}
                  </button>
                )}
              </div>
              {p.description && <p className="muted small">{p.description}</p>}
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
              {canManage && accounts.some((a) => a.status !== "ACTIVE") && (
                <div className="hint">To reconnect an account, choose Connect and sign in with the same account.</div>
              )}
            </section>
          );
        })
      )}
      {!canManage && s.workspace && <div className="hint">Only workspace owners and admins can connect or disconnect accounts.</div>}
    </main>
  );
}
