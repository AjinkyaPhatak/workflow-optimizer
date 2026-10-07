"use client";

// Server state of the Connected Accounts page: the user's workspaces, the
// configured OAuth providers and the selected workspace's accounts. Connect
// asks the backend to start a flow and sends the browser to the provider;
// the backend completes it and redirects back here.

import { useCallback, useEffect, useState } from "react";
import { authApi, connectedAccountApi } from "@/lib/api";
import type { ConnectedAccount, OAuthProvider, WorkspaceRef } from "@/types/api";

export interface ConnectedAccountsState {
  loading: boolean;
  error: unknown;
  workspaces: WorkspaceRef[];
  workspace: WorkspaceRef | null;
  setWorkspaceId: (id: string) => void;
  providers: OAuthProvider[];
  accounts: ConnectedAccount[];
  /** The provider being connected or the account being disconnected. */
  busy: string | null;
  connect: (provider: string) => Promise<void>;
  disconnect: (accountId: string) => Promise<void>;
}

export function useConnectedAccounts(navigate: (url: string) => void = (u) => window.location.assign(u)): ConnectedAccountsState {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [providers, setProviders] = useState<OAuthProvider[]>([]);
  const [accounts, setAccounts] = useState<ConnectedAccount[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    Promise.all([authApi.me(), connectedAccountApi.providers()])
      .then(([me, ps]) => {
        setWorkspaces(me.workspaces);
        setWorkspaceId((current) => current ?? me.workspaces[0]?.id ?? null);
        setProviders(ps.items);
        if (me.workspaces.length === 0) setLoading(false);
      })
      .catch((e) => {
        setError(e);
        setLoading(false);
      });
  }, []);

  const [version, setVersion] = useState(0);
  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    connectedAccountApi
      .list(workspaceId)
      .then((l) => !cancelled && setAccounts(l.items))
      .catch((e) => !cancelled && setError(e))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, version]);

  const connect = useCallback(async (provider: string) => {
    if (!workspaceId) return;
    setBusy(provider);
    setError(null);
    try {
      const started = await connectedAccountApi.authorize(provider, workspaceId);
      navigate(started.authorization_url);
    } catch (e) {
      setError(e);
      setBusy(null);
    }
  }, [workspaceId, navigate]);

  const disconnect = useCallback(async (accountId: string) => {
    setBusy(accountId);
    setError(null);
    try {
      await connectedAccountApi.disconnect(accountId);
      setVersion((v) => v + 1);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  }, []);

  return {
    loading, error, workspaces, workspace: workspaces.find((w) => w.id === workspaceId) ?? null,
    setWorkspaceId, providers, accounts, busy, connect, disconnect,
  };
}
