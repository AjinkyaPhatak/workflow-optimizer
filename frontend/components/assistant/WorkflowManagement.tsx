"use client";

import { useState } from "react";
import Link from "next/link";
import { assistantApi } from "@/lib/api/assistant";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { AssistantSession, AutomationRun, AutomationVersion } from "@/types/automation";

export function WorkflowManagement({ session, currentSession, flush, onRestore, disabled }: {
  session: AssistantSession | null; currentSession: () => AssistantSession | null;
  flush: () => Promise<void>; onRestore: (session: AssistantSession) => void; disabled: boolean;
}) {
  const [panel, setPanel] = useState<"versions" | "runs" | "template" | null>(null);
  const [versions, setVersions] = useState<AutomationVersion[]>([]);
  const [runs, setRuns] = useState<AutomationRun[]>([]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState("");
  if (!session) return null;
  async function perform(action: (saved: AssistantSession) => Promise<void>) {
    setBusy(true); setError(null); setNotice("");
    try {
      await flush(); const saved = currentSession();
      if (!saved) throw new Error("Save a workflow first.");
      await action(saved);
    } catch (err) { setError(err); } finally { setBusy(false); }
  }
  return <>
    <button disabled={disabled || busy} onClick={() => { setPanel("versions"); void perform(async saved => setVersions((await assistantApi.versions(saved.id)).items)); }}>Versions</button>
    <button disabled={busy} onClick={() => { setPanel("runs"); void perform(async saved => setRuns((await assistantApi.runs(saved.workspace_id)).items.filter(run => run.session_id === saved.id))); }}>History</button>
    <button disabled={disabled || busy} onClick={() => { setName(session.compilation?.plan.name ?? "My workflow"); setPanel("template"); setError(null); setNotice(""); }}>Save as template</button>
    {panel && <div className="run-overlay" role="dialog" aria-modal="true" aria-label={panel === "versions" ? "Version history" : panel === "runs" ? "Execution history" : "Save as template"} onMouseDown={e => { if (e.target === e.currentTarget && !busy) setPanel(null); }}>
      <section className="run-card stack" style={{ maxWidth: 640 }}>
        <header className="row"><h2>{panel === "versions" ? "Version history" : panel === "runs" ? "Execution history" : "Save as template"}</h2><span className="spacer" /><button aria-label="Close history" onClick={() => setPanel(null)}>✕</button></header>
        <ErrorBanner error={error} />{notice && <p role="status">{notice}</p>}
        {panel === "template" && <form className="stack" onSubmit={e => { e.preventDefault(); void perform(async saved => { await assistantApi.saveTemplate(saved.id, name.trim(), saved.compilation?.plan.summary ?? ""); setNotice("Template saved. You can reuse it from Templates."); }); }}>
          <label>Template name<input value={name} onChange={e => setName(e.target.value)} required maxLength={255} /></label>
          <button className="primary" disabled={busy || !name.trim()}>Save template</button>
        </form>}
        {panel === "versions" && <>
          <p className="small muted">Your draft saves automatically. Publish a checked snapshot to reuse or link from another workflow. Restoring a version only changes the draft.</p>
          <button className="primary" disabled={busy || !session.compilation?.ready} onClick={() => void perform(async saved => { await assistantApi.publish(saved.id, saved.revision); setVersions((await assistantApi.versions(saved.id)).items); setNotice("Version published. Nothing was run."); })}>Publish current version</button>
          {versions.length === 0 && <p className="muted">No published versions yet.</p>}
          {versions.map(version => <div className="row" key={version.id}><span>Version {version.version_number} · {new Date(version.created_at).toLocaleString()}</span><span className="spacer" />
            <button disabled={busy} onClick={() => void perform(async saved => { onRestore(await assistantApi.restore(saved.id, saved.revision, version.id)); setNotice("Version restored as your editable draft. Nothing was run."); })}>Restore as draft</button></div>)}
        </>}
        {panel === "runs" && <>{runs.length === 0 && <p className="muted">This workflow has not run yet.</p>}{runs.map(run => <div className="row" key={run.id}>
          <span>{new Date(run.created_at).toLocaleString()} · {run.status.toLowerCase().replaceAll("_", " ")}</span><span className="spacer" /><Link className="button" href={"/home?run=" + encodeURIComponent(run.id)} onClick={() => setPanel(null)}>Open run</Link>
        </div>)}</>}
      </section>
    </div>}
  </>;
}
