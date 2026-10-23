"use client";

// Account settings (Phase M): your name, your password, and the
// workspace's name (owners and admins).

import { useEffect, useState, type FormEvent } from "react";
import { Spinner } from "@/components/ui/BrandMark";
import { ErrorBanner } from "@/components/ui/ErrorBanner";
import { accountApi, authApi } from "@/lib/api";
import { useSession } from "@/lib/auth/session";
import type { WorkspaceRef } from "@/types/api";

function useSaved() {
  const [saved, setSaved] = useState<string | null>(null);
  useEffect(() => {
    if (!saved) return;
    const t = setTimeout(() => setSaved(null), 4000);
    return () => clearTimeout(t);
  }, [saved]);
  return [saved, setSaved] as const;
}

function Profile() {
  const user = useSession((s) => s.session?.user);
  const setUser = useSession((s) => s.setUser);
  const [name, setName] = useState(user?.name ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useSaved();

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setUser(await accountApi.updateProfile(name.trim()));
      setSaved("Saved.");
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="card stack" onSubmit={submit} data-testid="profile">
      <h2>Your profile</h2>
      <ErrorBanner error={error} />
      <div className="field">
        <label htmlFor="profile-email">Email</label>
        <input id="profile-email" value={user?.email ?? ""} disabled />
        <div className="hint">You sign in with this address.</div>
      </div>
      <div className="field">
        <label htmlFor="profile-name">Name</label>
        <input id="profile-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={255} />
      </div>
      <div className="row">
        <button className="primary" type="submit" disabled={busy || !name.trim() || name.trim() === user?.name}>
          {busy && <Spinner />}Save name
        </button>
        {saved && <span className="small" role="status" style={{ color: "var(--ok)" }}>{saved}</span>}
      </div>
    </form>
  );
}

function Password() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeat, setRepeat] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useSaved();
  const mismatch = repeat !== "" && next !== repeat;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (mismatch) return;
    setBusy(true);
    setError(null);
    try {
      await accountApi.changePassword(current, next);
      setCurrent("");
      setNext("");
      setRepeat("");
      setSaved("Password changed. Use it the next time you sign in.");
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="card stack" onSubmit={submit} data-testid="password">
      <h2>Password</h2>
      <ErrorBanner error={error} />
      <div className="field">
        <label htmlFor="pw-current">Current password</label>
        <input id="pw-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
      </div>
      <div className="field">
        <label htmlFor="pw-new">New password</label>
        <input id="pw-new" type="password" autoComplete="new-password" minLength={8} maxLength={72} value={next} onChange={(e) => setNext(e.target.value)} required />
        <div className="hint">At least 8 characters.</div>
      </div>
      <div className="field">
        <label htmlFor="pw-repeat">New password again</label>
        <input id="pw-repeat" type="password" autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} required
          aria-invalid={mismatch || undefined} />
        {mismatch && <div className="field-error" role="alert">The two new passwords are different.</div>}
      </div>
      <div className="row">
        <button className="primary" type="submit" disabled={busy || !current || next.length < 8 || next !== repeat}>
          {busy && <Spinner />}Change password
        </button>
        {saved && <span className="small" role="status" style={{ color: "var(--ok)" }}>{saved}</span>}
      </div>
    </form>
  );
}

function Workspace({ workspace, onRenamed }: { workspace: WorkspaceRef; onRenamed: (w: WorkspaceRef) => void }) {
  const [name, setName] = useState(workspace.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useSaved();
  const canRename = workspace.role === "owner" || workspace.role === "admin";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      onRenamed(await accountApi.renameWorkspace(workspace.id, name.trim()));
      setSaved("Saved.");
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="card stack" onSubmit={submit} data-testid="workspace">
      <h2>Workspace</h2>
      <p className="muted small">Your workflows, files, keys and connected accounts belong to the workspace. You are its {workspace.role}.</p>
      <ErrorBanner error={error} />
      <div className="field">
        <label htmlFor="ws-name">Workspace name</label>
        <input id="ws-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={255} disabled={!canRename} />
        {!canRename && <div className="hint">Only the workspace&apos;s owner or an admin can rename it.</div>}
      </div>
      {canRename && (
        <div className="row">
          <button className="primary" type="submit" disabled={busy || !name.trim() || name.trim() === workspace.name}>
            {busy && <Spinner />}Save workspace name
          </button>
          {saved && <span className="small" role="status" style={{ color: "var(--ok)" }}>{saved}</span>}
        </div>
      )}
    </form>
  );
}

export function AccountSettings() {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    authApi.me().then((me) => setWorkspaces(me.workspaces)).catch(setError);
  }, []);
  return (
    <main className="dashboard stack settings-page">
      <h1>Account</h1>
      <ErrorBanner error={error} />
      <Profile />
      <Password />
      {workspaces === null ? (
        !error && <div className="empty"><Spinner /> Loading workspace…</div>
      ) : (
        workspaces.map((w) => (
          <Workspace key={w.id} workspace={w} onRenamed={(r) => setWorkspaces((l) => (l ?? []).map((x) => (x.id === r.id ? { ...x, name: r.name } : x)))} />
        ))
      )}
    </main>
  );
}
