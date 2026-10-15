"use client";

// One-click run (Phase I1): saves what is on screen, publishes it when it
// is not the version that runs yet, starts it and follows it to the end.
// The workflow runs on the worker, so it appears in History like any run.

import { useCallback, useState } from "react";
import { useEditorContext } from "@/features/workflows/EditorContext";
import { useExecution } from "@/features/executions/useExecution";
import { isDirty, useEditorStore } from "@/stores/workflow-editor/store";

export type RunPhase = "idle" | "preparing" | "running";

export function useRunWorkflow(workflowId: string) {
  const session = useEditorContext();
  const exec = useExecution(workflowId);
  const [phase, setPhase] = useState<RunPhase>("idle");
  /** Why the run could not start (the workflow has problems, no rights). */
  const [blocked, setBlocked] = useState<"problems" | "not_allowed" | null>(null);

  const run = useCallback(
    async (input: Record<string, unknown>) => {
      setBlocked(null);
      setPhase("preparing");
      try {
        const active = session.workflow?.active_version_id ?? null;
        let versionId = active && session.version?.id === active && !isDirty(useEditorStore.getState()) ? active : null;
        if (!versionId) {
          if (!session.canPublish) {
            setBlocked("not_allowed");
            return;
          }
          const published = await session.publish();
          if (!published) {
            setBlocked("problems");
            return;
          }
          versionId = published.id;
        }
        setPhase("running");
        await exec.run(input, versionId);
      } finally {
        setPhase("idle");
      }
    },
    [session, exec],
  );

  return { ...exec, phase, blocked, run, busy: phase !== "idle" || exec.running };
}
