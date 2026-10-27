"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { assistantApi } from "@/lib/api/assistant";
import { authApi, credentialApi } from "@/lib/api";
import { ApiError } from "@/lib/api/client";
import { DOCTOR_STOPPED, markDoctored, wasDoctored } from "./workflowDoctor";
import type { Credential, WorkspaceRef } from "@/types/api";
import type { AssistantSession, AutomationRun, Plan, RunEvent, ToolDescriptor } from "@/types/automation";

export function useAssistant() {
  const [workspaces, setWorkspaces] = useState<WorkspaceRef[]>([]);
  const [workspaceId, setWorkspaceId] = useState("");
  const [credentialId, setCredentialId] = useState("");
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [sessions, setSessions] = useState<AssistantSession[]>([]);
  const [session, setSession] = useState<AssistantSession | null>(null);
  const [tools, setTools] = useState<ToolDescriptor[]>([]);
  const [comparisons, setComparisons] = useState<{ label: string; value: string }[]>([]);
  const [repairing, setRepairing] = useState(false);
  const [doctorFailure, setDoctorFailure] = useState<string | null>(null);
  const [run, setRun] = useState<AutomationRun | null>(null);
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [workspaceError, setWorkspaceError] = useState<unknown>(null);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState("");
  const [error, setError] = useState<unknown>(null);
  const turn = useRef(0);
  const request = useRef<AbortController | null>(null);
  const [autoExecute, setAutoExecute] = useState(true);
  const autoRunAfterDoctor = useRef<string | null>(null);
  const executionPolicy = useRef({ autoExecute, run });
  useEffect(() => { executionPolicy.current = { autoExecute, run }; }, [autoExecute, run]);
  useEffect(() => {
    let live = true;
    authApi.me().then(async me => {
      if (!live) return;
      setWorkspaces(me.workspaces); setWorkspaceId(me.workspaces[0]?.id ?? "");
      if (!me.workspaces.length) throw new Error("Your account has no workspace. Ask the workspace owner to add you.");
      const id = new URL(window.location.href).searchParams.get("session");
      if (id) {
        const saved = await assistantApi.get(id);
        if (live) { setWorkspaceId(saved.workspace_id); setSession(saved); }
      }
      const runId = new URL(window.location.href).searchParams.get("run");
      if (runId) {
        const saved = await assistantApi.run(runId);
        if (live) { setWorkspaceId(saved.workspace_id); setRun(saved); }
      }
    }).catch(err => live && setWorkspaceError(err)).finally(() => live && setLoading(false));
    return () => { live = false; request.current?.abort(); };
  }, [loadAttempt]);
  useEffect(() => {
    let live = true;
    assistantApi.tools().then(catalog => {
      if (live) { setTools(catalog.items); setComparisons(catalog.comparisons ?? []); }
    }).catch(err => live && setError(err));
    return () => { live = false; };
  }, [loadAttempt]);
  useEffect(() => {
    if (!workspaceId) return;
    let live = true;
    Promise.all([assistantApi.sessions(workspaceId), credentialApi.list(workspaceId), assistantApi.runs(workspaceId)])
      .then(([list, keys, runs]) => {
        if (!live) return;
        setSessions(list.items);
        setCredentials(keys.items.filter(key => key.provider === "openai"));
        if (session?.id) setRun(runs.items.find(r => r.session_id === session.id) ?? null);
      }).catch(err => live && setError(err));
    return () => { live = false; };
  }, [workspaceId, session?.id]);
  useEffect(() => {
    if (!run?.id) return;
    const id = run.id;
    const abort = new AbortController();
    let after = "";
    let timer: ReturnType<typeof setTimeout> | undefined;
    let terminal = false;
    const poll = setInterval(() => {
      assistantApi.run(id).then(current => {
        if (abort.signal.aborted) return;
        setRun(current); terminal = ["COMPLETED", "FAILED", "CANCELLED"].includes(current.status);
        if (terminal) clearInterval(poll);
      }).catch(err => { if (!abort.signal.aborted) setError(err); });
    }, 2000);
    const connect = () => {
      assistantApi.stream(id, after, (kind, data, eventId) => {
        if (abort.signal.aborted) return;
        if (eventId) after = eventId;
        if (kind === "run") {
          const current = data as AutomationRun;
          setRun(current); terminal = ["COMPLETED", "FAILED", "CANCELLED"].includes(current.status);
        }
        if (kind === "progress") setEvents(previous => {
          const event = data as RunEvent;
          return previous.some(e => e.id === event.id) ? previous : [...previous.slice(-99), event];
        });
      }, abort.signal).catch(() => { /* Polling keeps the run usable while the stream reconnects. */ })
        .finally(() => { if (!abort.signal.aborted && !terminal) timer = setTimeout(connect, 2000); });
    };
    connect();
    return () => { abort.abort(); clearTimeout(timer); clearInterval(poll); };
  }, [run?.id]);
  const remember = useCallback((saved: AssistantSession) => {
    setSession(saved); setSessions(previous => [saved, ...previous.filter(s => s.id !== saved.id)]);
    window.history.replaceState(null, "", "/home?session=" + encodeURIComponent(saved.id));
  }, []);
  const send = useCallback(async (text: string) => {
    if (!text.trim()) return false;
    if (!workspaceId) {
      setWorkspaceError(new Error("Your workspace is not ready. Retry loading it below; your text will stay here."));
      return false;
    }
    const current = ++turn.current;
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    autoRunAfterDoctor.current = null;
    setBusy(true); setRepairing(false); setDoctorFailure(null); setPending(text); setError(null);
    let accepted = false;
    try {
      const active = session ? await assistantApi.get(session.id) : await assistantApi.create(workspaceId, credentialId || undefined);
      if (current !== turn.current) return false;
      remember(active);
      const next = await assistantApi.message(active.id, active.revision, text, controller.signal);
      if (current !== turn.current) return false;
      remember(next); setPending("");
      accepted = true;
      const policy = executionPolicy.current;
      if (policy.autoExecute && !policy.run && next.compilation?.diagnostics.length) autoRunAfterDoctor.current = next.id;
      if (policy.autoExecute && next.compilation?.ready && !policy.run) {
        const started = await assistantApi.start(next.id, next.revision);
        if (current === turn.current) setRun(started);
      }
    } catch (err) {
      if (current === turn.current && (err as Error).name !== "AbortError") setError(err);
    } finally { if (current === turn.current) { setBusy(false); setPending(""); } }
    return accepted;
  }, [workspaceId, session, credentialId, remember]);
  const answer = useCallback(async (id: string, value: unknown) => {
    if (run?.question) { await assistantApi.answerRun(run, id, value); return; }
    if (!session) return;
    const current = ++turn.current;
    request.current?.abort(); autoRunAfterDoctor.current = null;
    setBusy(true); setRepairing(false); setDoctorFailure(null);
    try {
      const next = await assistantApi.answer(session.id, session.revision, id, value);
      if (current !== turn.current) return;
      remember(next);
      const policy = executionPolicy.current;
      if (policy.autoExecute && !policy.run && next.compilation?.diagnostics.length) autoRunAfterDoctor.current = next.id;
      if (policy.autoExecute && next.compilation?.ready && !policy.run) {
        const started = await assistantApi.start(next.id, next.revision);
        if (current === turn.current) setRun(started);
      }
    } finally { if (current === turn.current) setBusy(false); }
  }, [session, run, remember]);
  const repair = useCallback(async () => {
    if (!session) return;
    const current = ++turn.current;
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    markDoctored(session.id, session.revision);
    setBusy(true); setRepairing(true); setDoctorFailure(null); setError(null); setPending("");
    try {
      const next = await assistantApi.repair(session.id, session.revision, controller.signal);
      if (current !== turn.current) return;
      markDoctored(next.id, next.revision);
      remember(next);
      if (next.compilation?.diagnostics.length) setDoctorFailure(DOCTOR_STOPPED);
      const policy = executionPolicy.current;
      if (autoRunAfterDoctor.current === next.id && policy.autoExecute && next.compilation?.ready && !policy.run) {
        const started = await assistantApi.start(next.id, next.revision);
        if (current === turn.current) setRun(started);
      }
    } catch (err) {
      if (current !== turn.current || (err as Error).name === "AbortError") return;
      if (err instanceof ApiError && err.status === 409) {
        // Another tab may have completed the repair. Refresh that revision;
        // never resubmit a stale repair or execute an unfamiliar result.
        try {
          const latest = await assistantApi.get(session.id);
          if (current !== turn.current) return;
          markDoctored(latest.id, latest.revision); remember(latest);
          if (latest.compilation?.diagnostics.length) setDoctorFailure(DOCTOR_STOPPED);
        } catch { if (current === turn.current) setDoctorFailure(DOCTOR_STOPPED); }
      } else { setDoctorFailure(DOCTOR_STOPPED); setError(err); }
    } finally {
      if (current === turn.current) { autoRunAfterDoctor.current = null; setBusy(false); setRepairing(false); }
    }
  }, [session, remember]);
  useEffect(() => {
    if (loading || busy || !session?.compilation?.diagnostics.length || doctorFailure) return;
    // Schedule once after commit. Strict Mode cleanup cancels the first setup
    // before it can issue a duplicate repair or update state synchronously.
    const timer = setTimeout(() => {
      if (wasDoctored(session.id, session.revision)) setDoctorFailure(DOCTOR_STOPPED);
      else void repair();
    }, 0);
    return () => clearTimeout(timer);
  }, [loading, busy, session, doctorFailure, repair]);
  const edit = useCallback(async (plan: Plan) => {
    if (!session) return;
    const current = ++turn.current;
    request.current?.abort(); autoRunAfterDoctor.current = null;
    setError(null); setBusy(true); setRepairing(false); setDoctorFailure(null);
    try { const next = await assistantApi.edit(session.id, session.revision, plan); if (current === turn.current) remember(next); }
    catch (err) { if (current === turn.current) setError(err); }
    finally { if (current === turn.current) setBusy(false); }
  }, [session, remember]);
  const start = useCallback(async () => {
    if (!session) return; setBusy(true); setError(null);
    try { setRun(await assistantApi.start(session.id, session.revision, !!run)); } catch (err) { setError(err); } finally { setBusy(false); }
  }, [session, run]);
  const open = useCallback(async (id: string) => {
    request.current?.abort(); const current = ++turn.current; autoRunAfterDoctor.current = null;
    setBusy(true); setRepairing(false); setDoctorFailure(null); setError(null); setRun(null);
    try { const next = await assistantApi.get(id); if (current === turn.current) remember(next); }
    catch (err) { if (current === turn.current) setError(err); }
    finally { if (current === turn.current) setBusy(false); }
  }, [remember]);
  const fresh = useCallback(() => {
    request.current?.abort(); ++turn.current; autoRunAfterDoctor.current = null;
    setBusy(false); setRepairing(false); setDoctorFailure(null); setSession(null); setRun(null); setError(null); setPending("");
    window.history.replaceState(null, "", "/home");
  }, []);
  const fork = useCallback(async () => {
    if (!session?.compilation) return;
    const current = ++turn.current;
    request.current?.abort(); autoRunAfterDoctor.current = null;
    setBusy(true); setRepairing(false); setDoctorFailure(null);
    try {
      const created = await assistantApi.create(workspaceId, credentialId || undefined);
      if (current !== turn.current) return;
      const plan = materializePlan(session.compilation.plan, session.answers);
      const next = await assistantApi.edit(created.id, created.revision, { ...plan, name: plan.name + " (copy)" });
      if (current !== turn.current) return;
      remember(next);
      setRun(null);
    } catch (err) { if (current === turn.current) setError(err); }
    finally { if (current === turn.current) setBusy(false); }
  }, [session, workspaceId, credentialId, remember]);
  const interrupt = useCallback(() => {
    request.current?.abort(); ++turn.current; autoRunAfterDoctor.current = null;
    setBusy(false); setRepairing(false); setPending("");
  }, []);
  return { workspaces, workspaceId, setWorkspaceId, credentialId, setCredentialId, credentials, sessions, session,
    tools, comparisons, run, events, loading, workspaceError,
    reloadWorkspace: () => { setLoading(true); setWorkspaceError(null); setLoadAttempt(value => value + 1); },
    busy, repairing, doctorFailure, pending, error, setError, autoExecute, setAutoExecute, send, answer, edit, start, open, fresh, fork, interrupt };
}
export function materializePlan(plan: Plan, answers: Record<string, unknown>): Plan {
  // Bind answered draft slots so scheduled runs have no conversation dependency.
  const visit = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(visit);
    if (value && typeof value === "object") {
      const item = value as Record<string, unknown>;
      if (item.source === "answer" && typeof item.ref === "string" && Object.hasOwn(answers, item.ref))
        return { ...item, source: "literal", ref: "", value_json: JSON.stringify(answers[item.ref]) };
      if (item.source === "template" && typeof item.value_json === "string" && typeof item.ref === "string") {
        let index = 0;
        const template = JSON.parse(item.value_json) as string;
        const text = template.replace(/\[(?:amount|name|recipient|email|phone|value|insert[^\]]*|your[^\]]*)\]|<\s*(?:name|amount|email|recipient)\s*>|\{\{\s*[^}]+\s*\}\}/gi, token => {
          const key = item.ref + "_" + index++;
          return Object.hasOwn(answers, key) ? String(answers[key]) : token;
        });
        return { ...item, source: "literal", ref: "", value_json: JSON.stringify(text) };
      }
      return Object.fromEntries(Object.entries(item).map(([key, child]) => [key, visit(child)]));
    }
    return value;
  };
  return { ...(visit(plan) as Plan), questions: plan.questions.filter(q => !Object.hasOwn(answers, q.id)) };
}
