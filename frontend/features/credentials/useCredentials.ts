"use client";

// Server state of the Credentials page: the user's workspaces and the
// selected workspace's credential metadata. create() sends a secret once and
// keeps nothing of it; the list never contains secrets.

import { useCallback, useEffect, useState } from "react";
import { authApi, credentialApi } from "@/lib/api";
import type { CreateCredentialBody, Credential, WorkspaceRef } from "@/types/api";

export interface CredentialsState {
  loading: boolean;
  error: unknown;
  workspaces: WorkspaceRef[];
  workspace: WorkspaceRef | null;
  setWorkspaceId: (id: string) => void;
  credentials: Credential[];
  /** "create" while saving, or the ID of the credential being deleted. */
  busy: string | null;
  /** Resolves true when the credential was saved. */
  create: (body: Omit<CreateCredentialBody, "workspace_id">) => Promise<boolean>;
  remove: (id: string) => Promise<void>;
}

export function useCredentials(): CredentialsState {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [version, setVersion] = useState(0);

  useEffect(() => {
    authApi.me()
      .then((me) => {
        setWorkspaces(me.workspaces);
        setWorkspaceId((current) => current ?? me.workspaces[0]?.id ?? null);
        if (me.workspaces.length === 0) setLoading(false);
      })
      .catch((e) => {
        setError(e);
        setLoading(false);
      });
  }, []);

  useEffect(() => {
    if (!workspaceId) return;
    let cancelled = false;
    credentialApi
      .list(workspaceId)
      .then((l) => !cancelled && setCredentials(l.items))
      .catch((e) => !cancelled && setError(e))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, version]);

  const create = useCallback(async (body: Omit<CreateCredentialBody, "workspace_id">) => {
    if (!workspaceId) return false;
    setBusy("create");
    setError(null);
    try {
      await credentialApi.create({ ...body, workspace_id: workspaceId });
      setVersion((v) => v + 1);
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
    }
  }, [workspaceId]);

  const remove = useCallback(async (id: string) => {
    setBusy(id);
    setError(null);
    try {
      await credentialApi.remove(id);
      setVersion((v) => v + 1);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  }, []);

  const workspace = workspaces.find((w) => w.id === workspaceId) ?? null;
  return { loading, error, workspaces, workspace, setWorkspaceId, credentials, busy, create, remove };
}
