"use client";

import Link from "next/link";
import { useState } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useShowDetails } from "@/stores/ui/details";
import { useEditorStore, useIsDirty } from "@/stores/workflow-editor/store";
import { SaveTemplateDialog } from "./SaveTemplateDialog";
import { VersionsPanel } from "./VersionsPanel";

/** The editor's top bar (Phase I1): the workflow's name, whether it is
 * saved (saving is automatic), undo/redo, History and Run. Versions, Save,
 * Validate and Publish are shown with "Show technical details". */
export function EditorHeader({ onRun }: { onRun: () => void }) {
  const { workflow, version, busy, notice, save, validate, publish, canPublish, refreshVersions } = useEditorContext();
  const [history, setHistory] = useState(false);
  const [saveTemplate, setSaveTemplate] = useState(false);
  const details = useShowDetails();
  const dirty = useIsDirty();
  const readonly = useEditorStore((s) => s.mode === "readonly");
  const canUndo = useEditorStore((s) => s.past.length > 0);
  const canRedo = useEditorStore((s) => s.future.length > 0);
  const undo = useEditorStore((s) => s.undo);
  const redo = useEditorStore((s) => s.redo);
  const published = version?.status === "PUBLISHED";
  const saveFailed = notice?.kind === "error" && dirty;

  return (
    <header className="editor-header">
      <Link href="/workflows" aria-label="Back to workflows">←</Link>
      <h1>{workflow?.name}</h1>
      {details && (
        <span className="versions-anchor">
          <button
            type="button"
            className="version-button"
            data-versions-toggle
            aria-expanded={history}
            title="Version history"
            onClick={() => {
              if (!history) void refreshVersions();
              setHistory(!history);
            }}
          >
            {version ? (
              <span className={`badge ${published ? "published" : "draft"}`} data-testid="version-status">
                v{version.version_number} · {published ? "Published" : "Draft"}
              </span>
            ) : (
              <span className="badge draft" data-testid="version-status">No saved version</span>
            )}
            <span className="caret" aria-hidden>▾</span>
          </button>
          {history && <VersionsPanel onClose={() => setHistory(false)} />}
        </span>
      )}
      {readonly ? (
        <span className="badge">Read-only</span>
      ) : (
        <span className={`save-state ${saveFailed ? "error" : ""}`} data-testid="save-state">
          {busy === "saving" ? "Saving…" : saveFailed ? "Not saved: see the problems" : dirty ? "Unsaved changes" : version ? "All changes saved" : ""}
        </span>
      )}
      <span className="spacer" />
      {notice && details && (
        <span className="small" role="status" style={{ color: notice.kind === "ok" ? "var(--ok)" : "var(--danger)" }}>
          {notice.text}
        </span>
      )}
      <button onClick={undo} disabled={!canUndo || readonly} title="Undo (Ctrl+Z)" aria-label="Undo">↶</button>
      <button onClick={redo} disabled={!canRedo || readonly} title="Redo (Ctrl+Shift+Z)" aria-label="Redo">↷</button>
      {details && (
        <>
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
        </>
      )}
      {workflow && !readonly && (
        <button type="button" onClick={() => setSaveTemplate(true)} title="Let anyone in this workspace start from a copy of this workflow"
          data-testid="save-as-template">Save as template</button>
      )}
      {workflow && <Link className="button" href={`/workflows/${workflow.id}/executions`}>History</Link>}
      <button className="primary" onClick={onRun} disabled={readonly} data-testid="run-open">▶ Run</button>
      {saveTemplate && workflow && (
        <SaveTemplateDialog workflowId={workflow.id} defaultName={workflow.name} beforeSave={() => (dirty ? save() : Promise.resolve())}
          onClose={() => setSaveTemplate(false)} />
      )}
    </header>
  );
}
