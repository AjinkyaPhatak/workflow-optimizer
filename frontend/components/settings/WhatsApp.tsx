"use client";
import { useEffect, useState } from "react";
import Image from "next/image";
import QRCode from "qrcode";
import { authApi } from "@/lib/api";
import { assistantApi } from "@/lib/api/assistant";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { Backdrop } from "@/components/ui/Backdrop";
import type { WorkspaceRef } from "@/types/api";
import type { WhatsAppStatus } from "@/types/automation";
export function WhatsAppSettings() {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspace, setWorkspace] = useState("");
  const [status, setStatus] = useState<WhatsAppStatus>({ state: "disconnected" });
  const [qr, setQR] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { authApi.me().then(me => { setWorkspaces(me.workspaces); setWorkspace(me.workspaces[0]?.id ?? ""); }).catch(setError); }, []);
  useEffect(() => {
    if (!workspace) return;
    let live = true;
    const load = () => assistantApi.whatsApp(workspace).then(next => { if (live) setStatus(next); }).catch(err => { if (live) setError(err); });
    void load(); const timer = setInterval(() => void load(), 2500);
    return () => { live = false; clearInterval(timer); };
  }, [workspace]);
  useEffect(() => {
    let live = true;
    if (status.qr) void QRCode.toDataURL(status.qr, { width: 280, margin: 2 }).then(data => { if (live) setQR(data); });
    return () => { live = false; };
  }, [status.qr]);
  const canConnect = ["owner", "admin"].includes(workspaces.find(w => w.id === workspace)?.role ?? "");
  return <main className="dashboard stack whatsapp-page">
    <header className="workspace-page-head">
      <Backdrop apps={["whatsapp", "google_sheets", "gmail", "google_calendar"]} />
      <span className="workspace-page-mark motif motif-teal" aria-hidden>↗</span>
      <h1>WhatsApp</h1><p className="muted">Connect your personal account to send messages and files from your workflows.</p>
    </header>
    {workspaces.length > 1 && <select aria-label="Workspace" value={workspace} onChange={e => { setWorkspace(e.target.value); setStatus({ state: "disconnected" }); }}>{workspaces.map(w => <option key={w.id} value={w.id}>{w.name}</option>)}</select>}
    <ErrorBanner error={error} />
    <section className="card whatsapp-card stack">
      <span className="badge">{status.state.replaceAll("_", " ")}</span>
      {status.qr && qr && <div className="whatsapp-qr"><Image unoptimized src={qr} alt="Scan this QR code with WhatsApp Linked devices" width={280} height={280} /><p>Open WhatsApp → Settings → Linked devices → Link a device.</p></div>}
      {status.state === "connected" ? <p>Your account is connected. Workflows will ask you to choose when a contact name is ambiguous.</p> : <p>Link your account with a QR code. Your session is stored encrypted.</p>}
      <div className="row"><button className="primary" disabled={busy || !workspace || !canConnect} onClick={async () => {
        setBusy(true); setError(null); try { setStatus(await assistantApi.connectWhatsApp(workspace)); } catch (err) { setError(err); } finally { setBusy(false); }
      }}>{busy ? "Connecting…" : status.state === "connected" ? "Reconnect" : "Link WhatsApp"}</button>
      <button disabled={busy || !canConnect} onClick={async () => { try { await assistantApi.disconnectWhatsApp(workspace); setStatus({ state: "disconnected" }); } catch (err) { setError(err); } }}>Disconnect</button></div>
    </section>
  </main>;
}
