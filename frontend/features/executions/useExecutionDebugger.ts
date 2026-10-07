"use client";

// Server state of the execution debugger: the execution snapshot, its node
// records and its events (all pages), refreshed by polling while the
// execution is PENDING or RUNNING. The backend stays authoritative.

import { useCallback, useEffect, useState } from "react";
import { executionApi, workflowApi } from "@/lib/api";
import { normalizeDefinition } from "@/lib/workflow/definition";
import type { ExecutionDetails, ExecutionEvent, NodeExecutionDetails, Workflow, WorkflowDefinition } from "@/types/api";
import { startPolling } from "./poller";
import { isTerminal } from "./timeline";

export interface DebuggerSnapshot {
  execution: ExecutionDetails;
  nodes: NodeExecutionDetails[];
  events: ExecutionEvent[];
}

const EVENT_PAGE_SIZE = 100;
const MAX_EVENT_PAGES = 20;

export async function loadSnapshot(id: string): Promise<DebuggerSnapshot> {
  const [execution, nodes] = await Promise.all([executionApi.get(id), executionApi.nodes(id)]);
  const events: ExecutionEvent[] = [];
  for (let page = 1; page <= MAX_EVENT_PAGES; page++) {
    const p = await executionApi.events(id, page, EVENT_PAGE_SIZE);
    events.push(...p.events);
    if (events.length >= p.total || p.events.length === 0) break;
  }
  return { execution, nodes: nodes.items, events };
}

const TERMINAL_EVENTS = new Set(["EXECUTION_COMPLETED", "EXECUTION_FAILED", "EXECUTION_CANCELLED"]);

/** Done when the execution is terminal and its terminal event is visible. */
export function snapshotSettled(s: DebuggerSnapshot): boolean {
  return isTerminal(s.execution.status) && s.events.some((e) => TERMINAL_EVENTS.has(e.type));
}

export function useExecutionDebugger(executionId: string) {
  const [snapshot, setSnapshot] = useState<DebuggerSnapshot | null>(null);
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [definition, setDefinition] = useState<WorkflowDefinition | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [polling, setPolling] = useState(true);
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const poller = startPolling({
      load: () => loadSnapshot(executionId),
      isDone: snapshotSettled,
      isTerminal: (s) => isTerminal(s.execution.status),
      onData: (s) => {
        setSnapshot(s);
        setError(null);
        setPolling(!snapshotSettled(s));
      },
      onError: setError,
    });
    return () => poller.stop();
  }, [executionId, generation]);

  // The workflow and its exact version (for names and the graph). Either may
  // be gone (deleted workflow): the debugger then works from the records.
  const workflowId = snapshot?.execution.workflow_id;
  const versionId = snapshot?.execution.workflow_version_id;
  useEffect(() => {
    if (!workflowId || !versionId) return;
    let cancelled = false;
    workflowApi.get(workflowId).then((w) => !cancelled && setWorkflow(w)).catch(() => {});
    workflowApi
      .getVersion(workflowId, versionId)
      .then((v) => !cancelled && setDefinition(normalizeDefinition(v.definition)))
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [workflowId, versionId]);

  const refresh = useCallback(() => setGeneration((g) => g + 1), []);
  return { snapshot, workflow, definition, error, polling, refresh };
}
