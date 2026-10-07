"use client";

import Link from "next/link";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useEditorStore, useIsDirty } from "@/stores/workflow-editor/store";

export function EditorHeader({ onExecute }: { onExecute: () => void }) {
  const { workflow, version, busy, notice, save, validate, publish, canPublish } = useEditorContext();
  const dirty = useIsDirty();
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const canUndo = useEditorStore((s) => s.past.length > 0);
  const canRedo = useEditorStore((s) => s.future.length > 0);
  const undo = useEditorStore((s) => s.undo);
  const redo = useEditorStore((s) => s.redo);
  const published = version?.status === "PUBLISHED";

  return (
    <header className="editor-header">
      <Link href="/workflows" aria-label="Back to workflows">←</Link>
      <h1>{workflow?.name}</h1>
      {version ? (
        <span className={`badge ${published ? "published" : "draft"}`} data-testid="version-status">
          v{version.version_number} · {published ? "Published" : "Draft"}
        </span>
      ) : (
        <span className="badge draft" data-testid="version-status">No saved version</span>
      )}
      {dirty && <span className="dirty" data-testid="dirty">• Unsaved changes{published ? " (saving creates a new draft)" : ""}</span>}
      {readonly && <span className="badge">Read-only</span>}
      <span className="spacer" />
      {notice && (
        <span className="small" role="status" style={{ color: notice.kind === "ok" ? "var(--ok)" : "var(--danger)" }}>
          {notice.text}
        </span>
      )}
      <button onClick={undo} disabled={!canUndo || readonly} title="Undo (Ctrl+Z)">↶</button>
      <button onClick={redo} disabled={!canRedo || readonly} title="Redo (Ctrl+Shift+Z)">↷</button>
      <button onClick={() => void save()} disabled={readonly || busy !== null || !dirty}>
        {busy === "saving" ? "Saving…" : "Save"}
      </button>
      <button onClick={() => void validate()} disabled={busy !== null || (readonly && !version)}>
        {busy === "validating" ? "Validating…" : "Validate"}
      </button>
      <button
        onClick={() => void publish()}
        disabled={!canPublish || busy !== null || (published && !dirty)}
        title={canPublish ? "Validate and publish this version" : "Only workspace owners and admins can publish"}
      >
        {busy === "publishing" ? "Publishing…" : "Publish"}
      </button>
      <button className="primary" onClick={onExecute} disabled={readonly}>Execute</button>
    </header>
  );
}
