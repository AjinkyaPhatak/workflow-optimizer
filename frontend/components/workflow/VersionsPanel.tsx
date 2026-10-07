"use client";

import { useEffect, useRef } from "react";
import { Spinner } from "@/components/ui/BrandMark";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useEditorStore, useIsDirty } from "@/stores/workflow-editor/store";
import type { VersionSummary } from "@/types/api";

function when(iso: string | null): string {
  return iso ? new Date(iso).toLocaleString() : "";
}

/** The version's state for people: the workflow's active (published)
 * version, other published ones, and drafts. */
export function versionState(v: VersionSummary, activeId: string | null): { label: string; cls: string } {
  if (v.id === activeId) return { label: "Published · active", cls: "published" };
  if (v.status === "PUBLISHED") return { label: "Published", cls: "published-old" };
  if (v.status === "ARCHIVED") return { label: "Archived", cls: "" };
  return { label: "Draft", cls: "draft" };
}

/** The workflow's version history. Versions are immutable: opening one loads
 * it as the working copy, and saving always creates a new draft. */
export function VersionsPanel({ onClose }: { onClose: () => void }) {
  const { workflow, version, versions, refreshVersions, openVersion, busy } = useEditorContext();
  const dirty = useIsDirty();
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const ref = useRef<HTMLDivElement>(null);
  const activeId = workflow?.active_version_id ?? null;
  const latestDraft = versions?.find((v) => v.status === "DRAFT" && (!activeId || v.version_number > (versions.find((x) => x.id === activeId)?.version_number ?? 0)));

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node) && !(e.target as Element).closest?.("[data-versions-toggle]")) onClose();
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [onClose]);

  const open = async (v: VersionSummary) => {
    if (dirty && !window.confirm("Discard your unsaved changes and open this version?")) return;
    await openVersion(v.id);
    onClose();
  };

  return (
    <div className="versions-panel" ref={ref} role="dialog" aria-label="Versions" data-testid="versions-panel">
      <div className="versions-head">
        <b>Versions</b>
        <span className="spacer" />
        <button type="button" className="small-button" onClick={() => void refreshVersions()}>Refresh</button>
      </div>
      <p className="small muted versions-note">
        Published versions never change. Editing and saving always creates a new draft; publish a draft to make it the version that runs.
      </p>
      {versions === null ? (
        <div className="muted small"><Spinner /> Loading versions…</div>
      ) : versions.length === 0 ? (
        <p className="muted small">Nothing saved yet. Save to create the first draft.</p>
      ) : (
        <ol className="versions-list" data-testid="versions-list">
          {versions.map((v) => {
            const st = versionState(v, activeId);
            const editing = v.id === version?.id;
            return (
              <li key={v.id} className={editing ? "editing" : ""} data-testid={`version-${v.version_number}`}>
                <div className="version-main">
                  <b>v{v.version_number}</b>
                  <span className={`badge ${st.cls}`} data-testid={`version-${v.version_number}-state`}>{st.label}</span>
                  {v.id === latestDraft?.id && <span className="badge">Latest draft</span>}
                  {editing && <span className="small editing-tag">{dirty ? "Editing (unsaved changes)" : "Open in editor"}</span>}
                </div>
                <div className="small muted">
                  Saved {when(v.created_at)}
                  {v.published_at ? ` · published ${when(v.published_at)}` : ""}
                </div>
                {!editing && (
                  <button type="button" className="small-button" disabled={busy !== null} onClick={() => void open(v)}>
                    {readonly ? "View" : v.status === "PUBLISHED" ? "Open as new draft" : "Open"}
                  </button>
                )}
              </li>
            );
          })}
        </ol>
      )}
    </div>
  );
}
