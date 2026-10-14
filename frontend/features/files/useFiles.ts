"use client";

// Server state of the Files page: the user's workspaces and the selected
// workspace's files (metadata). Uploads go one file at a time.

import { useCallback, useEffect, useState } from "react";
import { authApi, fileApi } from "@/lib/api";
import type { WorkspaceFile, WorkspaceRef } from "@/types/api";

export interface FilesState {
  loading: boolean;
  error: unknown;
  workspaces: WorkspaceRef[];
  workspace: WorkspaceRef | null;
  setWorkspaceId: (id: string) => void;
  files: WorkspaceFile[];
  /** "upload" while uploading, or the ID of the file being deleted. */
  busy: string | null;
  upload: (files: File[]) => Promise<void>;
  remove: (id: string) => Promise<void>;
  download: (f: WorkspaceFile) => Promise<void>;
}

/** Saves a blob through a temporary link. */
function save(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function useFiles(): FilesState {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [files, setFiles] = useState<WorkspaceFile[]>([]);
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
    fileApi.list(workspaceId)
      .then((l) => !cancelled && setFiles(l.items))
      .catch((e) => !cancelled && setError(e))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, version]);

  const upload = useCallback(async (picked: File[]) => {
    if (!workspaceId || picked.length === 0) return;
    setBusy("upload");
    setError(null);
    try {
      for (const f of picked) await fileApi.upload(workspaceId, f);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
      setVersion((v) => v + 1);
    }
  }, [workspaceId]);

  const remove = useCallback(async (id: string) => {
    setBusy(id);
    setError(null);
    try {
      await fileApi.remove(id);
      setVersion((v) => v + 1);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  }, []);

  const download = useCallback(async (f: WorkspaceFile) => {
    setError(null);
    try {
      save(await fileApi.content(f.id), f.name);
    } catch (e) {
      setError(e);
    }
  }, []);

  const workspace = workspaces.find((w) => w.id === workspaceId) ?? null;
  return { loading, error, workspaces, workspace, setWorkspaceId, files, busy, upload, remove, download };
}
