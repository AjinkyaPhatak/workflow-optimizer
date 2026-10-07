"use client";

// Starts an execution through the API and follows it until it finishes. The
// backend runs it (API -> PostgreSQL -> Redis -> worker); the browser only
// polls GET /executions/{id}.

import { useCallback, useEffect, useRef, useState } from "react";
import { executionApi } from "@/lib/api";
import { TERMINAL_STATUSES, type Execution, type NodeExecution } from "@/types/api";

const POLL_MS = 800;
const MAX_POLL_MS = 5 * 60 * 1000;

export function useExecution(workflowId: string) {
  const [execution, setExecution] = useState<Execution | null>(null);
  const [nodes, setNodes] = useState<NodeExecution[]>([]);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const cancelled = useRef(false);

  // Stop polling when the editor unmounts (reset on mount: StrictMode
  // mounts, unmounts and remounts in development).
  useEffect(() => {
    cancelled.current = false;
    return () => {
      cancelled.current = true;
    };
  }, []);

  const run = useCallback(
    async (input: Record<string, unknown>, versionId: string) => {
      setRunning(true);
      setError(null);
      setExecution(null);
      setNodes([]);
      try {
        const accepted = await executionApi.execute(workflowId, input, versionId);
        const started = Date.now();
        for (;;) {
          if (cancelled.current) return;
          const e = await executionApi.get(accepted.execution_id);
          setExecution(e);
          if (TERMINAL_STATUSES.includes(e.status)) break;
          if (Date.now() - started > MAX_POLL_MS) break;
          await new Promise((r) => setTimeout(r, POLL_MS));
        }
        setNodes((await executionApi.nodes(accepted.execution_id)).items);
      } catch (e) {
        setError(e);
      } finally {
        setRunning(false);
      }
    },
    [workflowId],
  );

  return { execution, nodes, running, error, run };
}
