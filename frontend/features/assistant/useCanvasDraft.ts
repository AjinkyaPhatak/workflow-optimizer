"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import type { AssistantSession, Plan } from "@/types/automation";
import { emptyPlan } from "@/lib/automation/editor";

const same = (a: Plan, b: Plan) => JSON.stringify(a) === JSON.stringify(b);
const errorText = (error: unknown) => error instanceof Error ? error.message : "Your changes could not be saved. They are still here.";
export function useCanvasDraft(plan: Plan | null, onSave: (plan: Plan) => Promise<unknown>, onManualEdit?: () => void, sessionId?: string) {
  const [draft, setDraft] = useState<Plan>(() => plan ?? emptyPlan());
  const [past, setPast] = useState<Plan[]>([]), [future, setFuture] = useState<Plan[]>([]);
  const [saving, setSaving] = useState(false), [error, setError] = useState("");
  const [conflict, setConflict] = useState<Plan | null>(null);
  const [saved, setSaved] = useState<Plan>(() => plan ?? emptyPlan());
  const latest = useRef(draft), baseline = useRef(saved), incoming = useRef(plan), remote = useRef<Plan | null>(null);
  const callbacks = useRef({ onSave, onManualEdit });
  const scope = useRef(sessionId), generation = useRef(0);
  const flight = useRef<Promise<void> | null>(null), paused = useRef(false), mounted = useRef(true);
  useEffect(() => { callbacks.current = { onSave, onManualEdit }; }, [onSave, onManualEdit]);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    const before = scope.current; scope.current = sessionId;
    if (before === sessionId || !before && sessionId) return;
    generation.current++; const next = plan ?? emptyPlan();
    latest.current = next; baseline.current = next; incoming.current = plan;
    remote.current = null; flight.current = null; paused.current = false;
    setDraft(next); setSaved(next); setPast([]); setFuture([]); setSaving(false); setError(""); setConflict(null);
  }, [sessionId, plan]);
  const dirty = !same(draft, saved);
  useEffect(() => {
    if (!plan || plan === incoming.current) return;
    incoming.current = plan;
    if (same(plan, baseline.current)) return;
    if (flight.current) { remote.current = plan; return; }
    if (same(latest.current, baseline.current)) {
      if (!same(latest.current, plan)) { const previous = latest.current; setPast(history => [...history, previous].slice(-100)); setFuture([]); }
      baseline.current = plan; latest.current = plan;
      setSaved(plan); setDraft(plan); setError(""); setConflict(null);
    } else if (same(plan, latest.current)) {
      baseline.current = plan; setSaved(plan);
    } else {
      remote.current = plan; paused.current = true; setConflict(plan);
      setError("This workflow changed while you were editing. Your changes are still here.");
    }
  }, [plan]);
  const change = useCallback((next: Plan) => {
    if (same(latest.current, next)) return;
    callbacks.current.onManualEdit?.();
    const previous = latest.current;
    setPast(history => [...history, previous].slice(-100)); setFuture([]);
    latest.current = next; setDraft(next); setError(remote.current ? "This workflow changed while you were editing. Your changes are still here." : "");
    if (!remote.current) paused.current = false;
  }, []);
  const undo = useCallback(() => {
    if (!past.length) return;
    callbacks.current.onManualEdit?.();
    const next = past[past.length - 1];
    const previous = latest.current;
    setFuture(history => [previous, ...history]); setPast(past.slice(0, -1));
    latest.current = next; setDraft(next); setError(remote.current ? "This workflow changed while you were editing. Your changes are still here." : ""); if (!remote.current) paused.current = false;
  }, [past]);
  const redo = useCallback(() => {
    if (!future.length) return;
    callbacks.current.onManualEdit?.();
    const next = future[0];
    const previous = latest.current;
    setPast(history => [...history, previous]); setFuture(future.slice(1));
    latest.current = next; setDraft(next); setError(remote.current ? "This workflow changed while you were editing. Your changes are still here." : ""); if (!remote.current) paused.current = false;
  }, [future]);
  const flush = useCallback(async () => {
    if (flight.current) await flight.current;
    if (paused.current) throw new Error("Resolve the unsaved changes before continuing.");
    if (same(latest.current, baseline.current)) return;
    const current = generation.current;
    const run = async () => {
      setSaving(true);
      try {
        while (mounted.current && !same(latest.current, baseline.current)) {
          const snapshot = latest.current;
          const value = await callbacks.current.onSave(snapshot);
          if (generation.current !== current) return;
          const session = value as AssistantSession | undefined;
          const canonical = session?.compilation?.plan ?? remote.current ?? snapshot;
          baseline.current = canonical; remote.current = null;
          if (!mounted.current) return;
          setSaved(canonical); setError("");
          if (same(latest.current, snapshot)) { latest.current = canonical; setDraft(canonical); }
        }
      } catch (error) {
        if (generation.current !== current) return;
        paused.current = true;
        if (mounted.current) { setError(errorText(error)); if (remote.current) setConflict(remote.current); }
        throw error;
      } finally { if (generation.current === current) { flight.current = null; if (mounted.current) setSaving(false); } }
    };
    flight.current = run();
    await flight.current;
  }, []);
  useEffect(() => {
    if (!dirty || saving || error || conflict) return;
    const timer = setTimeout(() => { void flush().catch(() => undefined); }, 1500);
    return () => clearTimeout(timer);
  }, [draft, dirty, saving, error, conflict, flush]);
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (!same(latest.current, baseline.current)) event.preventDefault(); };
    window.addEventListener("beforeunload", warn); return () => window.removeEventListener("beforeunload", warn);
  }, []);
  const retry = useCallback(async () => { if (remote.current) throw new Error("Choose which version to keep before saving."); paused.current = false; setError(""); await flush(); }, [flush]);
  const resolve = useCallback((keep: boolean) => {
    const received = remote.current;
    if (received) { baseline.current = received; setSaved(received); if (!keep) { latest.current = received; setDraft(received); } }
    remote.current = null; paused.current = false; setConflict(null); setError("");
  }, []);
  return { draft, change, past, future, undo, redo, dirty, saving, error, conflict, flush, retry, resolve };
}
