"use client";

// "Save as template" (Phase K): copies the workflow as it is now into the
// workspace's templates, for anyone in the workspace to start from.

import Link from "next/link";
import { useState, type FormEvent } from "react";
import { Spinner } from "@/components/ui/BrandMark";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { workflowApi } from "@/lib/api";

export function SaveTemplateDialog({ workflowId, defaultName, beforeSave, onClose }: {
  workflowId: string;
  defaultName: string;
  /** Saves pending changes first, so the template has them. */
  beforeSave: () => Promise<unknown>;
  onClose: () => void;
}) {
  const [name, setName] = useState(defaultName);
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await beforeSave();
      await workflowApi.saveTemplate(workflowId, name.trim(), description.trim());
      setSaved(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="run-overlay" role="dialog" aria-modal="true" aria-labelledby="save-template-title" data-testid="save-template"
      onMouseDown={(ev) => ev.target === ev.currentTarget && onClose()}>
      <form className="run-card template-details" onSubmit={submit}>
        <div className="run-head">
          <h2 id="save-template-title">Save as template</h2>
          <span className="spacer" />
          <button type="button" className="icon-button" aria-label="Close" onClick={onClose}>✕</button>
        </div>
        <div className="template-body">
          {saved ? (
            <p role="status">Saved. Find it under <Link href="/templates">Templates</Link> → Saved, to start new workflows from it.</p>
          ) : (
            <>
              <p className="muted small">Everyone in this workspace can start a new workflow from it. Later changes to this workflow do not change the template.</p>
              <ErrorBanner error={error} />
              <div className="field">
                <label htmlFor="tpl-name">Template name</label>
                <input id="tpl-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={255} />
              </div>
              <div className="field">
                <label htmlFor="tpl-desc">What it does <span className="muted small">(optional)</span></label>
                <textarea id="tpl-desc" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
              </div>
            </>
          )}
        </div>
        <div className="row template-actions">
          <span className="spacer" />
          {saved ? (
            <button type="button" className="primary" onClick={onClose}>Done</button>
          ) : (
            <button type="submit" className="primary" disabled={busy || !name.trim()} data-testid="save-template-submit">
              {busy && <Spinner />}{busy ? "Saving…" : "Save template"}
            </button>
          )}
        </div>
      </form>
    </div>
  );
}
