"use client";

// Server state and server actions of the editor: the workflow, its current
// version, the node catalog, credential metadata, and save / validate /
// publish against the API. The editable definition itself lives in the
// editor store.
//
// Versions follow the Phase 12 API exactly: every save stores the definition
// as a NEW immutable DRAFT version (there is no version update endpoint);
// publishing marks a version PUBLISHED and makes it the active version.
// Published versions are never modified.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, authApi, credentialApi, nodeApi, projectApi, workflowApi } from "@/lib/api";
import { isDirty, useEditorStore } from "@/stores/workflow-editor/store";
import type { Credential, NodeDefinition, Role, ValidationResult, VersionSummary, Workflow } from "@/types/api";

export type BusyAction = "saving" | "validating" | "publishing" | null;

export interface EditorSession {
  loading: boolean;
  loadError: unknown;
  workflow: Workflow | null;
  version: VersionSummary | null;
  catalogList: NodeDefinition[];
  catalog: Map<string, NodeDefinition>;
  credentials: Credential[];
  role: Role | null;
  busy: BusyAction;
  /** Last action outcome for the header ("Saved v3", errors, ...). */
  notice: { kind: "ok" | "error"; text: string } | null;
  save: () => Promise<VersionSummary | null>;
  validate: () => Promise<ValidationResult | null>;
  publish: () => Promise<boolean>;
  canPublish: boolean;
  /** Version history, newest first (loaded on demand). */
  versions: VersionSummary[] | null;
  refreshVersions: () => Promise<void>;
  /** Loads a stored version into the editor as the working copy. Saving
   * then creates a new draft; the stored version never changes. */
  openVersion: (versionId: string) => Promise<void>;
}

export function useEditorSession(workflowId: string): EditorSession {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [version, setVersion] = useState<VersionSummary | null>(null);
  const [catalogList, setCatalogList] = useState<NodeDefinition[]>([]);
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [role, setRole] = useState<Role | null>(null);
  const [busy, setBusy] = useState<BusyAction>(null);
  const [notice, setNotice] = useState<EditorSession["notice"]>(null);
  const [versions, setVersions] = useState<VersionSummary[] | null>(null);
  /** Whether the history has been loaded (then saves/publishes refresh it). */
  const historyLoaded = useRef(false);
  const refreshVersionsRef = useRef<() => Promise<void>>(async () => {});

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const wf = await workflowApi.get(workflowId);
        const [project, me, versions, nodes] = await Promise.all([
          projectApi.get(wf.project_id),
          authApi.me(),
          workflowApi.listVersions(workflowId, 1, 1),
          nodeApi.list(),
        ]);
        const myRole = me.workspaces.find((w) => w.id === project.workspace_id)?.role ?? null;
        const creds = await credentialApi.list(project.workspace_id).catch(() => ({ items: [] as Credential[] }));
        const latest = versions.items[0] ?? null;
        const full = latest ? await workflowApi.getVersion(workflowId, latest.id) : null;
        if (cancelled) return;
        setWorkflow(wf);
        setRole(myRole);
        setCatalogList(nodes.items);
        setCredentials(creds.items);
        setVersion(latest);
        useEditorStore.getState().load(full?.definition ?? null, myRole === "viewer" ? "readonly" : "editing");
      } catch (e) {
        if (!cancelled) setLoadError(e);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [workflowId]);

  const catalog = useMemo(() => new Map(catalogList.map((d) => [d.type, d])), [catalogList]);

  /** Shows the backend's validation findings when it rejects a definition. */
  const reportFailure = useCallback((e: unknown, what: string) => {
    if (e instanceof ApiError && e.validationErrors.length > 0) {
      useEditorStore.getState().setValidation({ valid: false, errors: e.validationErrors, warnings: [] });
      setNotice({ kind: "error", text: `${what}: the workflow has validation errors` });
    } else {
      setNotice({ kind: "error", text: `${what}: ${e instanceof Error ? e.message : "failed"}` });
    }
  }, []);

  const saveInner = useCallback(async (): Promise<VersionSummary | null> => {
    const editor = useEditorStore.getState();
    const definition = editor.definition;
    try {
      const v = await workflowApi.createVersion(workflowId, definition);
      useEditorStore.getState().markSaved(definition);
      useEditorStore.getState().setValidation(null);
      const summary: VersionSummary = {
        id: v.id, workflow_id: v.workflow_id, version_number: v.version_number, status: v.status,
        created_by: v.created_by, created_at: v.created_at, published_at: v.published_at,
      };
      setVersion(summary);
      setNotice({ kind: "ok", text: `Saved as version ${v.version_number}` });
      if (historyLoaded.current) void refreshVersionsRef.current();
      return summary;
    } catch (e) {
      reportFailure(e, "Save failed");
      return null;
    }
  }, [workflowId, reportFailure]);

  /** The saved version matching the working copy (saving first if needed). */
  const ensureSaved = useCallback(async () => {
    if (version && !isDirty(useEditorStore.getState())) return version;
    return saveInner();
  }, [version, saveInner]);

  const save = useCallback(async () => {
    setBusy("saving");
    try {
      return await saveInner();
    } finally {
      setBusy(null);
    }
  }, [saveInner]);

  const validate = useCallback(async () => {
    setBusy("validating");
    try {
      const v = await ensureSaved();
      if (!v) return null;
      const result = await workflowApi.validate(workflowId, v.id);
      useEditorStore.getState().setValidation(result);
      setNotice(result.valid ? { kind: "ok", text: "Workflow is valid" } : { kind: "error", text: `${result.errors.length} validation error(s)` });
      return result;
    } catch (e) {
      reportFailure(e, "Validation failed");
      return null;
    } finally {
      setBusy(null);
    }
  }, [ensureSaved, workflowId, reportFailure]);

  const publish = useCallback(async () => {
    setBusy("publishing");
    try {
      const v = await ensureSaved();
      if (!v) return false;
      const published = await workflowApi.publish(workflowId, v.id);
      setVersion(published);
      setWorkflow((wf) => (wf ? { ...wf, active_version_id: published.id } : wf));
      useEditorStore.getState().setValidation({ valid: true, errors: [], warnings: [] });
      setNotice({ kind: "ok", text: `Published version ${published.version_number}` });
      if (historyLoaded.current) void refreshVersionsRef.current();
      return true;
    } catch (e) {
      reportFailure(e, "Publish failed");
      return false;
    } finally {
      setBusy(null);
    }
  }, [ensureSaved, workflowId, reportFailure]);

  const refreshVersions = useCallback(async () => {
    try {
      historyLoaded.current = true;
      setVersions((await workflowApi.listVersions(workflowId, 1, 100)).items);
    } catch (e) {
      setNotice({ kind: "error", text: `Could not load versions: ${e instanceof Error ? e.message : "failed"}` });
    }
  }, [workflowId]);

  useEffect(() => {
    refreshVersionsRef.current = refreshVersions;
  }, [refreshVersions]);

  const openVersion = useCallback(
    async (versionId: string) => {
      try {
        const v = await workflowApi.getVersion(workflowId, versionId);
        const { definition: _definition, ...summary } = v;
        void _definition;
        useEditorStore.getState().load(v.definition, useEditorStore.getState().mode);
        setVersion(summary);
        setNotice({ kind: "ok", text: `Opened version ${v.version_number}` });
      } catch (e) {
        setNotice({ kind: "error", text: `Could not open the version: ${e instanceof Error ? e.message : "failed"}` });
      }
    },
    [workflowId],
  );

  return {
    loading, loadError, workflow, version, catalogList, catalog, credentials, role, busy, notice,
    save, validate, publish,
    canPublish: role === "owner" || role === "admin",
    versions, refreshVersions, openVersion,
  };
}
