"use client";

// Server state of the Knowledge page: the workspace's knowledge bases, the
// selected base with its documents (polled while any is indexing), and the
// OpenAI credentials and files a base can use.

import { useCallback, useEffect, useState } from "react";
import { authApi, credentialApi, fileApi, knowledgeApi } from "@/lib/api";
import type { Credential, KnowledgeBase, KnowledgeBaseDetail, KnowledgeResult, WorkspaceFile, WorkspaceRef } from "@/types/api";

export const POLL_MS = 2000;

export interface KnowledgeState {
  loading: boolean;
  error: unknown;
  workspaces: WorkspaceRef[];
  workspace: WorkspaceRef | null;
  setWorkspaceId: (id: string) => void;
  bases: KnowledgeBase[];
  /** OpenAI API key credentials (what a base embeds with). */
  credentials: Credential[];
  files: WorkspaceFile[];
  selected: KnowledgeBaseDetail | null;
  select: (id: string | null) => void;
  busy: string | null;
  create: (name: string, description: string, credentialId: string) => Promise<boolean>;
  removeBase: (id: string) => Promise<void>;
  addText: (title: string, text: string) => Promise<boolean>;
  addFiles: (fileIds: string[]) => Promise<void>;
  uploadAndAdd: (files: File[]) => Promise<void>;
  removeDocument: (docId: string) => Promise<void>;
  reindex: (docId: string) => Promise<void>;
  search: (query: string) => Promise<KnowledgeResult[] | null>;
}

export function useKnowledge(): KnowledgeState {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState<string | null>(null);
  const [bases, setBases] = useState<KnowledgeBase[]>([]);
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [files, setFiles] = useState<WorkspaceFile[]>([]);
  const [selectedId, select] = useState<string | null>(null);
  const [selected, setSelected] = useState<KnowledgeBaseDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const refresh = () => setVersion((v) => v + 1);

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
    Promise.all([
      knowledgeApi.list(workspaceId),
      credentialApi.list(workspaceId).catch(() => ({ items: [] as Credential[] })),
      fileApi.list(workspaceId).catch(() => ({ items: [] as WorkspaceFile[] })),
    ])
      .then(([b, c, f]) => {
        if (cancelled) return;
        setBases(b.items);
        setCredentials(c.items.filter((x) => x.provider === "openai" && x.credential_type === "api_key"));
        setFiles(f.items);
      })
      .catch((e) => !cancelled && setError(e))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [workspaceId, version]);

  // The selected base; polled while a document is indexing.
  useEffect(() => {
    if (!selectedId) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = () => {
      knowledgeApi.get(selectedId)
        .then((d) => {
          if (cancelled) return;
          setSelected(d);
          if (d.items.some((x) => x.status === "INDEXING")) timer = setTimeout(load, POLL_MS);
        })
        .catch((e) => !cancelled && setError(e));
    };
    load();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [selectedId, version]);

  const run = useCallback(async (key: string, action: () => Promise<unknown>): Promise<boolean> => {
    setBusy(key);
    setError(null);
    try {
      await action();
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(null);
      setVersion((v) => v + 1);
    }
  }, []);

  const create = useCallback(async (name: string, description: string, credentialId: string) => {
    if (!workspaceId) return false;
    let created: KnowledgeBase | null = null;
    const ok = await run("create", async () => {
      created = await knowledgeApi.create({ workspace_id: workspaceId, name, description, credential_id: credentialId });
    });
    if (ok && created) select((created as KnowledgeBase).id);
    return ok;
  }, [workspaceId, run]);

  const removeBase = useCallback(async (id: string) => {
    if (await run(id, () => knowledgeApi.remove(id))) {
      select(null);
      setSelected(null);
    }
  }, [run]);

  const addText = useCallback((title: string, text: string) =>
    selectedId ? run("add", () => knowledgeApi.addText(selectedId, title, text)) : Promise.resolve(false), [selectedId, run]);

  const addFiles = useCallback(async (ids: string[]) => {
    if (!selectedId) return;
    await run("add", async () => {
      for (const id of ids) await knowledgeApi.addFile(selectedId, id);
    });
  }, [selectedId, run]);

  const uploadAndAdd = useCallback(async (picked: File[]) => {
    if (!selectedId || !workspaceId) return;
    await run("add", async () => {
      for (const f of picked) {
        const stored = await fileApi.upload(workspaceId, f);
        await knowledgeApi.addFile(selectedId, stored.id);
      }
    });
  }, [selectedId, workspaceId, run]);

  const removeDocument = useCallback(async (docId: string) => {
    if (selectedId) await run(docId, () => knowledgeApi.removeDocument(selectedId, docId));
  }, [selectedId, run]);

  const reindex = useCallback(async (docId: string) => {
    if (selectedId) await run(docId, () => knowledgeApi.reindex(selectedId, docId));
  }, [selectedId, run]);

  const search = useCallback(async (query: string) => {
    if (!selectedId) return null;
    setBusy("search");
    setError(null);
    try {
      return (await knowledgeApi.search(selectedId, query)).items;
    } catch (e) {
      setError(e);
      return null;
    } finally {
      setBusy(null);
    }
  }, [selectedId]);

  const workspace = workspaces.find((w) => w.id === workspaceId) ?? null;
  return {
    loading, error, workspaces, workspace, setWorkspaceId: (id) => {
      setWorkspaceId(id);
      select(null);
      setSelected(null);
      refresh();
    },
    bases, credentials, files, selected: selectedId ? selected : null, select, busy,
    create, removeBase, addText, addFiles, uploadAndAdd, removeDocument, reindex, search,
  };
}
