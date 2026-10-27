"use client";
import { useEffect, useState } from "react";
import Link from "next/link";
import { authApi } from "@/lib/api";
import { assistantApi } from "@/lib/api/assistant";
import { materializePlan } from "@/features/assistant/useAssistant";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import type { AssistantSession, Automation } from "@/types/automation";
export function Automations() {
  const [workspace, setWorkspace] = useState("");
  const [items, setItems] = useState<Automation[]>([]);
  const [sessions, setSessions] = useState<AssistantSession[]>([]);
  const [selected, setSelected] = useState("");
  const [kind, setKind] = useState("schedule");
  const [source, setSource] = useState("");
  const [cron, setCron] = useState("0 9 * * 1-5");
  const [timezone, setTimezone] = useState("Asia/Kolkata");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const reload = async (ws: string) => { const [automations, drafts] = await Promise.all([assistantApi.automations(ws), assistantApi.sessions(ws)]); setItems(automations.items); setSessions(drafts.items); };
  useEffect(() => { authApi.me().then(async me => { const ws = me.workspaces[0]?.id; if (ws) { setWorkspace(ws); await reload(ws); } }).catch(setError); }, []);
  return <main className="dashboard stack">
    <div className="row"><h1>Schedules & triggers</h1><span className="spacer" /><Link href="/home">Build a workflow ↗</Link></div>
    <p className="muted">Keep a workflow moving on a schedule or start it when an event arrives.</p><ErrorBanner error={error} />
    <form className="card automation-form stack" onSubmit={async e => {
      e.preventDefault(); const session = sessions.find(s => s.id === selected); if (!session?.compilation) return;
      setBusy(true); setError(null);
      try { await assistantApi.saveAutomation({ workspace_id: workspace, name: session.compilation.plan.name, plan: materializePlan(session.compilation.plan, session.answers),
        credential_id: session.credential_id, source, cron: kind === "schedule" ? cron : "", timezone, trigger: kind === "schedule" ? "" : kind, enabled: true }); await reload(workspace); } catch (err) { setError(err); } finally { setBusy(false); }
    }}>
      <h2>New automation</h2><label>Workflow<select value={selected} onChange={e => setSelected(e.target.value)} required><option value="">Choose a ready workflow…</option>
        {sessions.filter(s => s.compilation?.ready).map(s => <option key={s.id} value={s.id}>{s.compilation!.plan.name}</option>)}</select></label>
      <label>Start when<select value={kind} onChange={e => setKind(e.target.value)}><option value="schedule">A schedule is due</option><option value="webhook">A signed webhook arrives</option><option value="knowledge.indexed">A knowledge source is indexed</option><option value="workflow.completed">Another workflow completes</option><option value="whatsapp.message">A WhatsApp message arrives</option></select></label>
      {kind === "knowledge.indexed" && <label>Knowledge base ID<input value={source} onChange={e => setSource(e.target.value)} required /></label>}
      {kind === "workflow.completed" && <label>Conversation ID<input value={source} onChange={e => setSource(e.target.value)} required placeholder="From the workflow’s home URL" /></label>}
      {kind === "whatsapp.message" && <label>Account and exact sender<input value={source} onChange={e => setSource(e.target.value)} required placeholder="account user ID / WhatsApp contact ID" /></label>}
      {kind === "schedule" && <><label>Cron expression<input value={cron} onChange={e => setCron(e.target.value)} required /></label><p className="hint">0 9 * * 1-5 means 9 am on weekdays. Overlapping runs are skipped.</p><label>Timezone<input value={timezone} onChange={e => setTimezone(e.target.value)} required /></label></>}
      <button className="primary" disabled={busy || !selected}>{busy ? "Saving…" : "Create automation"}</button>
    </form>
    {items.map(item => <section key={item.id} className="card automation-row">
      <div><h3>{item.name}</h3><p className="muted">{item.cron ? item.cron + " · " + item.timezone : item.trigger}</p>
        {item.trigger === "webhook" && <p className="hint">POST /api/v1/triggers/{item.id} · signed with your deployment’s webhook key.</p>}</div>
      <button disabled={busy} onClick={async () => { setBusy(true); try { await assistantApi.saveAutomation({ ...item, enabled: !item.enabled }); await reload(workspace); } catch (err) { setError(err); } finally { setBusy(false); } }}>{item.enabled ? "Pause" : "Enable"}</button>
      <button className="small-button danger" disabled={busy} onClick={async () => { setBusy(true);try { await assistantApi.deleteAutomation(item.id!);await reload(workspace); } catch (err) { setError(err); } finally { setBusy(false); } }}>Delete</button>
    </section>)}
  </main>;
}
