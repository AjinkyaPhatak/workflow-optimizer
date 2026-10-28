"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { assistantApi } from "@/lib/api/assistant";
import type { AutomationRun } from "@/types/automation";

export function isActiveAutomationRun(run: AutomationRun): boolean {
  return !["COMPLETED", "FAILED", "CANCELLED"].includes(run.status);
}

export function useAutomationRuns(id: string) {
  const [runs, setRuns] = useState<AutomationRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [controlError, setControlError] = useState<unknown>(null);
  const [requested, setRequested] = useState<Record<string, "pause" | "cancel">>({});
  const [notice, setNotice] = useState("");
  const request = useRef<AbortController | null>(null);
  const live = useRef(false);
  const inFlight = useRef(new Set<string>());
  const controlGeneration = useRef<Record<string, number>>({});

  const refresh = useCallback(async () => {
    request.current?.abort();
    const abort = new AbortController();
    request.current = abort;
    try {
      const result = await assistantApi.automationRuns(id, abort.signal);
      if (abort.signal.aborted) return;
      setRuns(result.items);
      setLoadError(null);
      setRequested(previous => Object.fromEntries(Object.entries(previous).filter(([runId, action]) => {
        const run = result.items.find(item => item.id === runId);
        return run && isActiveAutomationRun(run) && !(action === "pause" && run.status === "PAUSED");
      })));
    } catch (err) {
      if (!abort.signal.aborted) setLoadError(err);
    } finally {
      if (!abort.signal.aborted) setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    live.current = true;
    let current = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      await refresh();
      if (current) timer = setTimeout(() => void poll(), 5000);
    };
    void poll();
    return () => { current = false; live.current = false; clearTimeout(timer); request.current?.abort(); };
  }, [refresh]);

  const control = async (run: AutomationRun, action: "pause" | "cancel") => {
    const key = run.id + ":" + action;
    // Stop can supersede a pause that has not yet been confirmed by the worker.
    if (inFlight.current.has(key) || requested[run.id] === "cancel" || (action === "pause" && requested[run.id])) return;
    inFlight.current.add(key);
    const generation = (controlGeneration.current[run.id] ?? 0) + 1;
    controlGeneration.current[run.id] = generation;
    setRequested(previous => ({ ...previous, [run.id]: action }));
    setNotice("");
    setControlError(null);
    try {
      await assistantApi.control(run.id, action);
      if (!live.current || controlGeneration.current[run.id] !== generation) return;
      setNotice(action === "cancel"
        ? "Stop requested for this run. The automation can still start future runs unless you pause it above."
        : "Pause requested for this run. Future runs are unchanged. Open the run to resume or adjust its limits.");
      await refresh();
    } catch (err) {
      if (!live.current || controlGeneration.current[run.id] !== generation) return;
      setControlError(err);
      setRequested(previous => { const next = { ...previous }; delete next[run.id]; return next; });
    } finally {
      inFlight.current.delete(key);
    }
  };

  return { runs, loading, error: controlError ?? loadError, loadError, requested, notice, refresh, control };
}
